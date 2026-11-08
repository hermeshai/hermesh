//! Sign in with ChatGPT: direct subscription Responses, independent of the Codex harness.
//! The profile is public; refresh/access/ID tokens never cross RPC or enter models.toml.
use crate::{ConnectionError, Result};
use async_trait::async_trait;
use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD as B64};
use her_llm::connection::{Authorization, ConnectionRequest};
use her_llm::credentials::CredentialStore;
use her_llm::{AuthConfig, Credentials, ModelRegistry, ProviderConfig};
use her_protocol::api::{
    ChatGptLoginState, ChatGptStatus, ConnectionModel, ConnectionState, ConnectionView,
};
use parking_lot::{Mutex, RwLock};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::{
    fs::File,
    io::Write,
    path::PathBuf,
    sync::{Arc, Weak},
    time::Duration,
};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio_util::sync::CancellationToken;
use url::Url;

pub const ID: &str = "chatgpt-api";
const DIRECT_SCOPE: &str = "chatgpt.tokens.use.direct";
const SCOPES: &str =
    "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct";
const RESOURCE: &str = "https://api.openai.com/v1";
const AGENT: &str = "Hermesh";

#[derive(Clone)]
struct Endpoints {
    issuer: String,
    api: String,
}
impl Default for Endpoints {
    fn default() -> Self {
        // A debug-build-only loopback fixture. Distribution builds cannot redirect credentials.
        #[cfg(debug_assertions)]
        if std::env::var("HER_CREDENTIAL_STORE").as_deref() == Ok("file")
            && let Ok(base) = std::env::var("HER_TEST_CHATGPT_BASE")
            && let Ok(u) = Url::parse(&base)
            && u.scheme() == "http"
            && u.host_str() == Some("127.0.0.1")
            && u.username().is_empty()
            && u.password().is_none()
        {
            return Self {
                issuer: base.trim_end_matches('/').into(),
                api: format!("{}/v1", base.trim_end_matches('/')),
            };
        }
        Self {
            issuer: "https://auth.openai.com".into(),
            api: RESOURCE.into(),
        }
    }
}
#[derive(Default, Clone, Serialize, Deserialize)]
struct Profile {
    installation: String,
    client_id: Option<String>,
    subject: Option<String>,
    email: Option<String>,
    enabled: bool,
    #[serde(default)]
    needs_login: bool,
    #[serde(default)]
    plan_permission_required: bool,
    #[serde(default)]
    models: Vec<ConnectionModel>,
    fetched_at: Option<String>,
}
#[derive(Serialize, Deserialize)]
struct Tokens {
    access: String,
    refresh: String,
    id_token: String,
    expires_at: i64,
    subject: String,
}
struct Login {
    id: String,
    url: String,
    state: ChatGptLoginState,
    message: Option<String>,
    cancel: CancellationToken,
}
pub struct ChatGpt {
    dir: PathBuf,
    credential_key: String,
    registry: Weak<ModelRegistry>,
    endpoints: Endpoints,
    login: Mutex<Option<Login>>,
    seen: Mutex<Option<Vec<u8>>>,
    changed: RwLock<Option<crate::Changed>>,
    cancellation: RwLock<CancellationToken>,
}
fn failure(code: &str, message: &str) -> ConnectionError {
    ConnectionError::new(code, message)
}
fn storage() -> ConnectionError {
    failure(
        "credential_storage",
        "Cannot read or save the ChatGPT connection securely",
    )
}
fn login_required() -> ConnectionError {
    failure("connection_login_required", "Sign in to ChatGPT API again")
}
fn now() -> i64 {
    chrono::Utc::now().timestamp()
}
fn random() -> String {
    let mut data = [0u8; 32];
    ring::rand::SecureRandom::fill(&ring::rand::SystemRandom::new(), &mut data)
        .expect("OS random generator");
    B64.encode(data)
}

impl ChatGpt {
    pub(crate) fn new(home: &std::path::Path, registry: &Arc<ModelRegistry>) -> Arc<Self> {
        let key = crate::platform::digest(home.to_string_lossy().as_bytes());
        Arc::new(Self {
            dir: home.join("connections/chatgpt"),
            credential_key: format!("chatgpt-api-{}", &key[..16]),
            registry: Arc::downgrade(registry),
            endpoints: Endpoints::default(),
            login: Mutex::new(None),
            seen: Mutex::new(None),
            changed: RwLock::new(None),
            cancellation: RwLock::new(CancellationToken::new()),
        })
    }
    pub(crate) fn on_change(&self, callback: crate::Changed) {
        *self.changed.write() = Some(callback);
    }
    fn store(&self) -> Result<Box<dyn CredentialStore>> {
        her_llm::credentials::default_store(self.dir.join("tokens.toml")).ok_or_else(storage)
    }
    fn read(&self) -> Result<Profile> {
        match std::fs::read(self.dir.join("profile.json")) {
            Ok(data) => serde_json::from_slice(&data).map_err(|_| storage()),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Profile::default()),
            Err(_) => Err(storage()),
        }
    }
    fn save(&self, p: &Profile) -> Result<()> {
        let mut opts = std::fs::OpenOptions::new();
        opts.write(true).create(true).truncate(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            opts.mode(0o600);
        }
        let path = self.dir.join("profile.tmp");
        let mut file = opts.open(&path).map_err(|_| storage())?;
        file.write_all(&serde_json::to_vec(p).map_err(|_| storage())?)
            .map_err(|_| storage())?;
        file.sync_all().map_err(|_| storage())?;
        std::fs::rename(path, self.dir.join("profile.json")).map_err(|_| storage())
    }
    fn lock_file(&self, name: &str) -> Result<File> {
        std::fs::create_dir_all(&self.dir).map_err(|_| storage())?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&self.dir, std::fs::Permissions::from_mode(0o700))
                .map_err(|_| storage())?;
        }
        let mut opts = std::fs::OpenOptions::new();
        opts.read(true).write(true).create(true).truncate(false);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            opts.mode(0o600);
        }
        opts.open(self.dir.join(name)).map_err(|_| storage())
    }
    /// Lock across CLI/desktop processes; always reread rotating credentials while holding it.
    async fn lock(&self) -> Result<File> {
        let file = self.lock_file("state.lock")?;
        tokio::time::timeout(Duration::from_secs(90), async {
            loop {
                match file.try_lock() {
                    Ok(()) => return Ok(file),
                    Err(std::fs::TryLockError::WouldBlock) => {
                        tokio::time::sleep(Duration::from_millis(50)).await
                    }
                    Err(_) => return Err(storage()),
                }
            }
        })
        .await
        .map_err(|_| {
            failure(
                "connection_busy",
                "ChatGPT connection is busy; retry shortly",
            )
        })?
    }
    fn tokens(&self) -> Result<Tokens> {
        let raw = self
            .store()?
            .get(&self.credential_key)
            .map_err(|_| storage())?
            .ok_or_else(login_required)?;
        serde_json::from_str(&raw).map_err(|_| storage())
    }
    fn save_tokens(&self, t: &Tokens) -> Result<()> {
        self.store()?
            .set(
                &self.credential_key,
                &serde_json::to_string(t).map_err(|_| storage())?,
            )
            .map_err(|_| storage())
    }
    fn provider(p: &Profile) -> Option<String> {
        p.subject.as_ref().and_then(|s| {
            p.client_id
                .as_ref()
                .map(|c| crate::provider_id(ID, &format!("{c}\0{s}")))
        })
    }
    fn view(p: &Profile) -> Option<ConnectionView> {
        if !p.enabled {
            return None;
        }
        Some(ConnectionView {
            connection_id: ID.into(),
            provider_id: Self::provider(p)?,
            adapter_id: ID.into(),
            display_name: "ChatGPT API".into(),
            state: if p.needs_login {
                ConnectionState::NeedsLogin
            } else if !p.enabled {
                ConnectionState::Disabled
            } else if p.models.is_empty() {
                ConnectionState::Unavailable
            } else {
                ConnectionState::Ready
            },
            reason_code: p.needs_login.then(|| "connection_login_required".into()),
            diagnostic: None,
            models: p.models.clone(),
            models_fetched_at: p.fetched_at.clone(),
        })
    }
    /// Reload public configuration, including changes made by another her process.
    pub fn sync(&self) -> Result<Option<ConnectionView>> {
        let p = self.read()?;
        let bytes = serde_json::to_vec(&p).map_err(|_| storage())?;
        let mut seen = self.seen.lock();
        if seen.as_ref() != Some(&bytes) {
            let mut providers = vec![];
            if p.enabled && !p.needs_login {
                if let Some(id) = Self::provider(&p) {
                    providers.push(ProviderConfig {
                        plan: None,
                        id,
                        name: "ChatGPT API".into(),
                        names: Default::default(),
                        api: "openai-responses".into(),
                        base_url: self.endpoints.api.clone(),
                        auth: AuthConfig::Managed,
                        headers: Default::default(),
                        currency: Default::default(),
                        compat: Default::default(),
                        listing: her_llm::catalog::Listing {
                            entries: p.models.iter().map(crate::declared_model).collect(),
                            ..Default::default()
                        },
                    });
                }
            } else {
                self.cancellation.read().cancel();
            }
            if let Some(registry) = self.registry.upgrade() {
                registry
                    .replace_source("chatgpt-api", providers)
                    .map_err(|_| storage())?;
            }
            // A different process (or a successful catalog retry) supersedes old UI outcomes.
            {
                let mut login = self.login.lock();
                if login
                    .as_ref()
                    .is_some_and(|l| !matches!(l.state, ChatGptLoginState::Pending))
                {
                    *login = None;
                }
            }
            *seen = Some(bytes);
            drop(seen);
            if let Some(callback) = self.changed.read().clone() {
                callback();
            }
        }
        Ok(Self::view(&p))
    }
    pub fn status(&self) -> Result<ChatGptStatus> {
        let connection = self.sync()?;
        let p = self.read()?;
        let login = self.login.lock();
        Ok(ChatGptStatus {
            state: login.as_ref().map(|l| l.state.clone()).unwrap_or(
                if p.enabled && !p.needs_login {
                    ChatGptLoginState::Connected
                } else {
                    ChatGptLoginState::Idle
                },
            ),
            login_id: login.as_ref().map(|l| l.id.clone()),
            authorize_url: login
                .as_ref()
                .filter(|l| matches!(l.state, ChatGptLoginState::Pending))
                .map(|l| l.url.clone()),
            account: p.email,
            message: login.as_ref().and_then(|l| l.message.clone()).or_else(|| {
                p.plan_permission_required
                    .then(|| "Authorize ChatGPT plan usage to connect ChatGPT API".into())
            }),
            connection,
        })
    }
    fn http(&self) -> Result<reqwest::Client> {
        self.registry
            .upgrade()
            .ok_or_else(ConnectionError::unavailable)?
            .http_client()
            .map_err(|_| ConnectionError::unavailable())
    }
    pub async fn start(self: &Arc<Self>) -> Result<ChatGptStatus> {
        if self
            .login
            .lock()
            .as_ref()
            .is_some_and(|l| matches!(l.state, ChatGptLoginState::Pending))
        {
            return self.status();
        }
        let login_lock = self.lock_file("login.lock")?;
        login_lock.try_lock().map_err(|_| {
            failure(
                "connection_busy",
                "A ChatGPT sign-in is already running in another window or process",
            )
        })?;
        let listener = tokio::net::TcpListener::bind((std::net::Ipv4Addr::LOCALHOST, 0))
            .await
            .map_err(|_| {
                failure(
                    "callback_unavailable",
                    "Cannot open the local ChatGPT sign-in callback",
                )
            })?;
        let redirect = format!(
            "http://127.0.0.1:{}/auth/callback",
            listener
                .local_addr()
                .map_err(|_| ConnectionError::unavailable())?
                .port()
        );
        let state = random();
        let nonce = random();
        let verifier = random();
        let challenge = B64.encode(ring::digest::digest(
            &ring::digest::SHA256,
            verifier.as_bytes(),
        ));
        let mut url = Url::parse(&format!("{}/api/accounts/authorize", self.endpoints.issuer))
            .map_err(|_| ConnectionError::unsupported())?;
        {
            let _lock = self.lock().await?;
            let mut p = self.read()?;
            if p.installation.is_empty() {
                p.installation = format!("urn:uuid:{}", uuid::Uuid::new_v4());
                self.save(&p)?;
            }
            let mut q = url.query_pairs_mut();
            q.extend_pairs([
                (
                    "client_id",
                    p.client_id.as_deref().unwrap_or("dynamic_agent_client"),
                ),
                ("response_type", "code"),
                ("redirect_uri", &redirect),
                ("scope", SCOPES),
                ("resource", RESOURCE),
                ("state", &state),
                ("nonce", &nonce),
                ("code_challenge", &challenge),
                ("code_challenge_method", "S256"),
                ("ext_agent_host_id", &p.installation),
            ]);
            if p.client_id.is_none() {
                q.append_pair("agent_name_hint", AGENT);
            }
            if p.plan_permission_required {
                q.append_pair("prompt", "consent");
            }
        }
        let id = uuid::Uuid::new_v4().to_string();
        let cancel = CancellationToken::new();
        *self.login.lock() = Some(Login {
            id: id.clone(),
            url: url.into(),
            state: ChatGptLoginState::Pending,
            message: None,
            cancel: cancel.clone(),
        });
        let this = self.clone();
        tokio::spawn(async move {
            let _login_lock = login_lock;
            // Cancellation only stops the callback wait. Once exchanging a rotating credential,
            // finish its atomic save rather than losing a refresh token in the middle of a write.
            let result = tokio::select! {
                _=cancel.cancelled()=>Err(failure("cancelled","ChatGPT sign-in cancelled")),
                r=tokio::time::timeout(Duration::from_secs(600),this.callback(listener,&state))=>r.unwrap_or_else(|_|Err(failure("login_timeout","ChatGPT sign-in timed out"))),
            };
            let result = match result {
                Ok(query) => {
                    this.complete(query, &redirect, &verifier, &nonce, &cancel)
                        .await
                }
                Err(e) => Err(e),
            };
            let _ = this.sync();
            let mut login = this.login.lock();
            if let Some(l) = login.as_mut().filter(|l| l.id == id) {
                match result {
                    Ok(()) => {
                        l.state = ChatGptLoginState::Connected;
                        l.message = None;
                    }
                    Err(e) => {
                        l.state = if e.code == "cancelled" {
                            ChatGptLoginState::Cancelled
                        } else {
                            ChatGptLoginState::Failed
                        };
                        l.message = Some(e.into_message());
                    }
                }
            }
            drop(login);
        });
        self.status()
    }
    async fn callback(
        &self,
        listener: tokio::net::TcpListener,
        state: &str,
    ) -> Result<std::collections::HashMap<String, String>> {
        loop {
            let (mut socket, _) = listener
                .accept()
                .await
                .map_err(|_| ConnectionError::unavailable())?;
            let mut data = vec![0u8; 8192];
            let mut len = 0;
            let read = tokio::time::timeout(Duration::from_secs(3), async {
                while len < data.len() {
                    let n = socket.read(&mut data[len..]).await?;
                    if n == 0 {
                        break;
                    }
                    len += n;
                    if data[..len].windows(4).any(|w| w == b"\r\n\r\n") {
                        break;
                    }
                }
                Ok::<_, std::io::Error>(())
            })
            .await;
            if !matches!(read, Ok(Ok(()))) {
                continue;
            }
            let request = String::from_utf8_lossy(&data[..len]);
            let target = request
                .lines()
                .next()
                .and_then(|l| l.strip_prefix("GET "))
                .and_then(|l| l.strip_suffix(" HTTP/1.1"));
            let url = target.and_then(|t| Url::parse(&format!("http://127.0.0.1{t}")).ok());
            let query = url.as_ref().map(|u| {
                u.query_pairs()
                    .into_owned()
                    .collect::<std::collections::HashMap<_, _>>()
            });
            let valid = url.as_ref().is_some_and(|u| u.path() == "/auth/callback")
                && query
                    .as_ref()
                    .is_some_and(|q| q.get("state").is_some_and(|s| s == state));
            let (status, body) = if valid {
                (
                    "200 OK",
                    "Sign-in received. Return to Hermesh to see the result; you can close this tab.",
                )
            } else {
                (
                    "400 Bad Request",
                    "Invalid sign-in callback. Return to Hermesh and try again.",
                )
            };
            let response = format!(
                "HTTP/1.1 {status}\r\nContent-Type: text/plain; charset=utf-8\r\nCache-Control: no-store\r\nContent-Security-Policy: default-src 'none'\r\nConnection: close\r\nContent-Length: {}\r\n\r\n{body}",
                body.len()
            );
            let _ = socket.write_all(response.as_bytes()).await;
            if valid {
                return Ok(query.unwrap());
            }
        }
    }
    pub fn cancel(&self, id: &str) -> Result<ChatGptStatus> {
        {
            let login = self.login.lock();
            let l = login
                .as_ref()
                .filter(|l| l.id == id)
                .ok_or_else(|| failure("not_found", "Sign-in attempt no longer exists"))?;
            l.cancel.cancel();
        }
        self.status()
    }
    async fn post_token(&self, form: &[(&str, &str)]) -> Result<Value> {
        let r = self
            .http()?
            .post(format!(
                "{}/api/accounts/oauth/token",
                self.endpoints.issuer
            ))
            .timeout(Duration::from_secs(30))
            .form(form)
            .send()
            .await
            .map_err(|_| ConnectionError::unavailable())?;
        let status = r.status();
        let v: Value = r.json().await.map_err(|_| ConnectionError::unsupported())?;
        if !status.is_success() {
            let code = v["error"].as_str().or_else(|| v["error"]["code"].as_str());
            return Err(match code {
                Some("invalid_client") => failure(
                    "invalid_client",
                    "ChatGPT did not accept this client registration",
                ),
                Some(
                    "invalid_grant"
                    | "invalid_refresh_token"
                    | "token_expired"
                    | "refresh_token_expired"
                    | "refresh_token_invalidated"
                    | "refresh_token_reused",
                ) => login_required(),
                _ if status.as_u16() == 401 => login_required(),
                _ if status.is_server_error() => ConnectionError::unavailable(),
                _ => failure(
                    "oauth_exchange_failed",
                    "ChatGPT authorization failed; retry sign-in",
                ),
            });
        }
        Ok(v)
    }
    async fn discovery(&self) -> Result<Value> {
        let v: Value = self
            .http()?
            .get(format!(
                "{}/.well-known/openid-configuration",
                self.endpoints.issuer
            ))
            .timeout(Duration::from_secs(20))
            .send()
            .await
            .map_err(|_| ConnectionError::unavailable())?
            .error_for_status()
            .map_err(|_| ConnectionError::unavailable())?
            .json()
            .await
            .map_err(|_| ConnectionError::unsupported())?;
        if v["issuer"] != self.endpoints.issuer {
            return Err(failure("invalid_id_token", "Unexpected ChatGPT issuer"));
        }
        Ok(v)
    }
    fn trusted_endpoint(&self, v: &Value, key: &str) -> Result<String> {
        let value = v[key].as_str().ok_or_else(ConnectionError::unsupported)?;
        let u = Url::parse(value).map_err(|_| ConnectionError::unsupported())?;
        let issuer =
            Url::parse(&self.endpoints.issuer).map_err(|_| ConnectionError::unsupported())?;
        if u.origin() != issuer.origin() || !u.username().is_empty() || u.password().is_some() {
            return Err(ConnectionError::unsupported());
        }
        Ok(value.into())
    }
    async fn verify_id(&self, token: &str, client: &str, nonce: Option<&str>) -> Result<Value> {
        let invalid = || failure("invalid_id_token", "ChatGPT identity verification failed");
        let parts: Vec<_> = token.split('.').collect();
        if parts.len() != 3 {
            return Err(invalid());
        }
        let header: Value = serde_json::from_slice(&B64.decode(parts[0]).map_err(|_| invalid())?)
            .map_err(|_| invalid())?;
        if header["alg"] != "RS256" {
            return Err(invalid());
        }
        let kid = header["kid"].as_str().ok_or_else(invalid)?;
        let discovery = self.discovery().await?;
        let keys: Value = self
            .http()?
            .get(self.trusted_endpoint(&discovery, "jwks_uri")?)
            .timeout(Duration::from_secs(20))
            .send()
            .await
            .map_err(|_| ConnectionError::unavailable())?
            .error_for_status()
            .map_err(|_| invalid())?
            .json()
            .await
            .map_err(|_| invalid())?;
        let key = keys["keys"]
            .as_array()
            .and_then(|keys| {
                keys.iter().find(|k| {
                    k["kid"] == kid
                        && k["kty"] == "RSA"
                        && k["use"].as_str().is_none_or(|u| u == "sig")
                        && k["alg"].as_str().is_none_or(|a| a == "RS256")
                })
            })
            .ok_or_else(invalid)?;
        let n = B64
            .decode(key["n"].as_str().ok_or_else(invalid)?)
            .map_err(|_| invalid())?;
        let e = B64
            .decode(key["e"].as_str().ok_or_else(invalid)?)
            .map_err(|_| invalid())?;
        let signature = B64.decode(parts[2]).map_err(|_| invalid())?;
        ring::signature::RsaPublicKeyComponents { n: &n, e: &e }
            .verify(
                &ring::signature::RSA_PKCS1_2048_8192_SHA256,
                format!("{}.{}", parts[0], parts[1]).as_bytes(),
                &signature,
            )
            .map_err(|_| invalid())?;
        let claims: Value = serde_json::from_slice(&B64.decode(parts[1]).map_err(|_| invalid())?)
            .map_err(|_| invalid())?;
        let aud = &claims["aud"];
        let audience = aud == client
            || aud
                .as_array()
                .is_some_and(|a| a.iter().any(|v| v == client));
        if claims["iss"] != self.endpoints.issuer
            || !audience
            || claims["exp"].as_i64().is_none_or(|e| e <= now())
            || claims["iat"].as_i64().is_none_or(|i| i > now() + 60)
            || claims["nbf"].as_i64().is_some_and(|n| n > now() + 60)
            || claims["sub"].as_str().is_none_or(|s| s.is_empty())
            || nonce.is_some_and(|n| claims["nonce"] != n)
            || claims["azp"].as_str().is_some_and(|a| a != client)
            || (aud.as_array().is_some_and(|a| a.len() > 1) && claims["azp"] != client)
        {
            return Err(invalid());
        }
        Ok(claims)
    }
    fn token_values(v: &Value, old: Option<&Tokens>, subject: String) -> Result<Tokens> {
        let strval = |key: &str| v[key].as_str().filter(|s| !s.is_empty()).map(String::from);
        let scopes = v["scope"].as_str();
        if (old.is_none() && scopes.is_none())
            || scopes.is_some_and(|s| !s.split_whitespace().any(|s| s == DIRECT_SCOPE))
        {
            return Err(failure(
                "scope_not_granted",
                "Authorize ChatGPT plan usage to connect ChatGPT API",
            ));
        }
        if v["token_type"]
            .as_str()
            .is_none_or(|s| !s.eq_ignore_ascii_case("bearer"))
        {
            return Err(ConnectionError::unsupported());
        }
        let expires = v["expires_in"]
            .as_i64()
            .filter(|n| *n > 0 && *n <= 86400)
            .ok_or_else(ConnectionError::unsupported)?;
        Ok(Tokens {
            access: strval("access_token").ok_or_else(ConnectionError::unsupported)?,
            refresh: strval("refresh_token")
                .or_else(|| old.map(|t| t.refresh.clone()))
                .ok_or_else(ConnectionError::unsupported)?,
            id_token: strval("id_token")
                .or_else(|| old.map(|t| t.id_token.clone()))
                .ok_or_else(ConnectionError::unsupported)?,
            expires_at: now() + expires,
            subject,
        })
    }
    async fn complete(
        &self,
        q: std::collections::HashMap<String, String>,
        redirect: &str,
        verifier: &str,
        nonce: &str,
        cancel: &CancellationToken,
    ) -> Result<()> {
        if cancel.is_cancelled() {
            return Err(failure("cancelled", "ChatGPT sign-in cancelled"));
        }
        let _lock = self.lock().await?;
        let mut p = self.read()?;
        if let Some(client) = q
            .get("client_id")
            .filter(|s| !s.is_empty() && s.len() < 512)
        {
            if client == "dynamic_agent_client"
                || p.client_id.as_ref().is_some_and(|saved| saved != client)
            {
                return Err(failure(
                    "invalid_client",
                    "ChatGPT returned a different registration; the existing connection was kept",
                ));
            }
            p.client_id = Some(client.clone());
            self.save(&p)?;
        }
        if q.contains_key("error") {
            return Err(failure(
                "authorization_denied",
                "ChatGPT sign-in was not authorized",
            ));
        }
        let client = p
            .client_id
            .as_deref()
            .ok_or_else(|| failure("missing_client", "ChatGPT did not issue a client ID"))?;
        let code = q
            .get("code")
            .filter(|s| !s.is_empty())
            .ok_or_else(ConnectionError::unsupported)?;
        let v = self
            .post_token(&[
                ("grant_type", "authorization_code"),
                ("client_id", client),
                ("code", code),
                ("code_verifier", verifier),
                ("redirect_uri", redirect),
                ("resource", RESOURCE),
            ])
            .await?;
        let claims = self
            .verify_id(
                v["id_token"]
                    .as_str()
                    .ok_or_else(ConnectionError::unsupported)?,
                client,
                Some(nonce),
            )
            .await?;
        let subject = claims["sub"].as_str().unwrap().to_string();
        if p.subject.as_ref().is_some_and(|s| s != &subject) {
            return Err(failure(
                "identity_changed",
                "Sign in with the registered ChatGPT account, or disconnect and forget it before changing accounts",
            ));
        }
        if !v["scope"]
            .as_str()
            .is_some_and(|s| s.split_whitespace().any(|s| s == DIRECT_SCOPE))
        {
            // Identity alone is not permission to infer. Keep the verified registration and
            // offer explicit consent on the next user-initiated sign-in.
            p.subject = Some(subject);
            p.email = claims["email"].as_str().map(String::from);
            p.enabled = false;
            p.needs_login = false;
            p.plan_permission_required = true;
            p.models.clear();
            self.save(&p)?;
            self.store()?
                .delete(&self.credential_key)
                .map_err(|_| storage())?;
            self.sync()?;
            return Err(failure(
                "scope_not_granted",
                "Authorize ChatGPT plan usage to connect ChatGPT API",
            ));
        }
        let tokens = Self::token_values(&v, None, subject.clone())?;
        let result = self.fetch_models(&tokens.access).await;
        if cancel.is_cancelled() {
            let revoked = self.revoke(&tokens, &p).await.is_ok();
            return Err(failure(
                "cancelled",
                if revoked {
                    "ChatGPT sign-in cancelled"
                } else {
                    "ChatGPT sign-in cancelled. Remote revocation was not confirmed; remove Hermesh in ChatGPT settings."
                },
            ));
        }
        self.save_tokens(&tokens)?;
        p.subject = Some(subject);
        p.email = claims["email"].as_str().map(String::from);
        p.enabled = true;
        p.needs_login = false;
        p.plan_permission_required = false;
        // Retain a validated grant even if catalog discovery failed, so it can be retried.
        if let Ok(models) = &result {
            p.models = models.clone();
            p.fetched_at = Some(her_llm::discovery::now());
        }
        self.save(&p)?;
        *self.cancellation.write() = CancellationToken::new();
        self.sync()?;
        result.map(|_| ())
    }
    async fn access(&self, rejected: Option<&str>) -> Result<Tokens> {
        let _lock = self.lock().await?;
        let mut p = self.read()?;
        if !p.enabled || p.needs_login {
            return Err(login_required());
        }
        let tokens = self.tokens()?;
        if p.subject.as_deref() != Some(&tokens.subject) {
            return Err(login_required());
        }
        if tokens.expires_at > now() + 60 && rejected.is_none_or(|r| r != tokens.access) {
            return Ok(tokens);
        }
        let client = p.client_id.as_deref().ok_or_else(login_required)?;
        let v = match self
            .post_token(&[
                ("grant_type", "refresh_token"),
                ("client_id", client),
                ("refresh_token", &tokens.refresh),
                ("resource", RESOURCE),
            ])
            .await
        {
            Ok(v) => v,
            Err(e) => {
                if e.code == "connection_login_required" {
                    p.needs_login = true;
                    self.save(&p)?;
                    self.store()?
                        .delete(&self.credential_key)
                        .map_err(|_| storage())?;
                    self.sync()?;
                }
                return Err(e);
            }
        };
        let refreshed = async {
            if let Some(id) = v["id_token"].as_str() {
                let claims = self.verify_id(id, client, None).await?;
                if claims["sub"] != tokens.subject {
                    return Err(login_required());
                }
            }
            Self::token_values(&v, Some(&tokens), tokens.subject.clone())
        }
        .await;
        let refreshed = match refreshed {
            Ok(tokens) => tokens,
            Err(_) => {
                // The server may already have rotated the refresh token. Do not replay the old
                // token or install an identity that failed validation.
                p.needs_login = true;
                self.save(&p)?;
                self.store()?
                    .delete(&self.credential_key)
                    .map_err(|_| storage())?;
                self.sync()?;
                return Err(login_required());
            }
        };
        self.save_tokens(&refreshed)?;
        Ok(refreshed)
    }
    async fn fetch_models(&self, access: &str) -> Result<Vec<ConnectionModel>> {
        let r = self
            .http()?
            .get(format!("{}/models", self.endpoints.api))
            .bearer_auth(access)
            .timeout(Duration::from_secs(30))
            .send()
            .await
            .map_err(|_| ConnectionError::unavailable())?;
        if r.status().as_u16() == 401 {
            return Err(login_required());
        }
        if !r.status().is_success() {
            return Err(failure(
                "models_unavailable",
                "Cannot load ChatGPT models; check plan usage and retry",
            ));
        }
        let v: Value = r.json().await.map_err(|_| ConnectionError::unsupported())?;
        let rows = v["models"]
            .as_array()
            .ok_or_else(ConnectionError::unsupported)?;
        let mut models = vec![];
        for m in rows.iter().filter(|m| m["visibility"] == "list") {
            let id = m["slug"]
                .as_str()
                .filter(|s| crate::valid_id(s))
                .ok_or_else(ConnectionError::unsupported)?;
            if models.iter().any(|m: &ConnectionModel| m.id == id) {
                continue;
            }
            let parse = |s: &str| {
                if s == "none" {
                    Some(her_llm::ThinkingLevel::Off)
                } else {
                    s.parse::<her_llm::ThinkingLevel>().ok()
                }
            };
            let thinking = m["supported_reasoning_levels"]
                .as_array()
                .map(|a| {
                    a.iter()
                        .filter_map(|v| v["effort"].as_str().and_then(parse))
                        .collect::<Vec<_>>()
                })
                .unwrap_or_default();
            let default = m["default_reasoning_level"]
                .as_str()
                .and_then(parse)
                .filter(|t| thinking.contains(t))
                .unwrap_or_default();
            models.push(ConnectionModel {
                id: id.into(),
                name: m["display_name"].as_str().unwrap_or(id).into(),
                names: Default::default(),
                context_window: m["context_window"]
                    .as_u64()
                    .map(|v| v.clamp(1024, 2_000_000) as u32),
                max_output_tokens: None,
                reasoning: !thinking.is_empty(),
                images: m["input_modalities"]
                    .as_array()
                    .is_some_and(|a| a.iter().any(|v| v == "image")),
                thinking,
                default_thinking: default,
            });
        }
        if models.is_empty() {
            return Err(failure(
                "models_unavailable",
                "No models are available for this ChatGPT account",
            ));
        }
        Ok(models)
    }
    pub async fn refresh(&self) -> Result<()> {
        let mut tokens = self.access(None).await?;
        let models = match self.fetch_models(&tokens.access).await {
            Err(e) if e.code == "connection_login_required" => {
                tokens = self.access(Some(&tokens.access)).await?;
                self.fetch_models(&tokens.access).await?
            }
            result => result?,
        };
        let _lock = self.lock().await?;
        let mut p = self.read()?;
        if !p.enabled || p.subject.as_deref() != Some(&tokens.subject) {
            return Err(login_required());
        }
        p.models = models;
        p.fetched_at = Some(her_llm::discovery::now());
        self.save(&p)?;
        self.sync()?;
        Ok(())
    }
    async fn revoke(&self, tokens: &Tokens, p: &Profile) -> Result<()> {
        let discovery = self.discovery().await?;
        let endpoint = self.trusted_endpoint(&discovery, "revocation_endpoint")?;
        self.http()?
            .post(endpoint)
            .timeout(Duration::from_secs(20))
            .form(&[
                ("token", tokens.refresh.as_str()),
                ("token_type_hint", "refresh_token"),
                ("client_id", p.client_id.as_deref().unwrap_or_default()),
            ])
            .send()
            .await
            .map_err(|_| ConnectionError::unavailable())?
            .error_for_status()
            .map_err(|_| ConnectionError::unavailable())?;
        Ok(())
    }
    pub async fn logout(&self) -> Result<ChatGptStatus> {
        // Do not race an exchange already in progress; it owns login.lock until credentials save.
        let login_lock = self.lock_file("login.lock")?;
        login_lock.try_lock().map_err(|_| {
            failure(
                "connection_busy",
                "Cancel the pending sign-in and wait for it to finish first",
            )
        })?;
        let _lock = self.lock().await?;
        let mut p = self.read()?;
        p.enabled = false;
        self.save(&p)?;
        self.cancellation.read().cancel();
        self.sync()?;
        let mut revoke_failed = false;
        if let Ok(tokens) = self.tokens() {
            revoke_failed = self.revoke(&tokens, &p).await.is_err();
        }
        self.store()?
            .delete(&self.credential_key)
            .map_err(|_| storage())?;
        // Keep the verified account/client mapping for reauthorization on this host.
        p.models.clear();
        p.fetched_at = None;
        p.needs_login = false;
        self.save(&p)?;
        *self.login.lock() = None;
        self.sync()?;
        let mut status = self.status()?;
        if revoke_failed {
            status.message = Some(
                "Disconnected locally. Remote revocation failed; remove Hermesh in ChatGPT settings to revoke the grant."
                    .into(),
            );
        }
        Ok(status)
    }
    /// Explicit removal forgets a registration; ordinary sign-out retains it.
    pub async fn forget(&self) -> Result<()> {
        let login_lock = self.lock_file("login.lock")?;
        login_lock.try_lock().map_err(|_| {
            failure(
                "connection_busy",
                "Cancel the pending ChatGPT sign-in first",
            )
        })?;
        let _lock = self.lock().await?;
        let mut p = self.read()?;
        if p.enabled {
            return Err(failure(
                "conflict",
                "Disconnect ChatGPT API before forgetting the account",
            ));
        }
        self.store()?
            .delete(&self.credential_key)
            .map_err(|_| storage())?;
        p.plan_permission_required = false;
        p.client_id = None;
        p.subject = None;
        p.email = None;
        p.needs_login = false;
        p.models.clear();
        p.fetched_at = None;
        self.save(&p)?;
        *self.login.lock() = None;
        self.sync()?;
        Ok(())
    }
    pub(crate) async fn begin(
        self: &Arc<Self>,
        provider: &str,
        model: &str,
    ) -> std::result::Result<Box<dyn ConnectionRequest>, her_llm::LlmError> {
        self.sync().map_err(|e| e.llm())?;
        let p = self.read().map_err(|e| e.llm())?;
        if !p.enabled
            || p.needs_login
            || Self::provider(&p).as_deref() != Some(provider)
            || !p.models.iter().any(|m| m.id == model)
        {
            return Err(login_required().llm());
        }
        if self.cancellation.read().is_cancelled() {
            *self.cancellation.write() = CancellationToken::new();
        }
        Ok(Box::new(Request {
            owner: self.clone(),
            subject: p.subject.unwrap(),
            used: Mutex::new(None),
            cancel: self.cancellation.read().clone(),
        }))
    }
}
struct Request {
    owner: Arc<ChatGpt>,
    subject: String,
    used: Mutex<Option<String>>,
    cancel: CancellationToken,
}
#[async_trait]
impl ConnectionRequest for Request {
    async fn authorize(
        &self,
        rejected: bool,
    ) -> std::result::Result<Authorization, her_llm::LlmError> {
        let old = self.used.lock().clone();
        let tokens = self
            .owner
            .access(if rejected { old.as_deref() } else { None })
            .await
            .map_err(|e| e.llm())?;
        if tokens.subject != self.subject {
            return Err(login_required().llm());
        }
        *self.used.lock() = Some(tokens.access.clone());
        Ok(Authorization {
            base_url: self.owner.endpoints.api.clone(),
            credentials: Credentials::with_key(tokens.access),
            transport: None,
        })
    }
    fn cancellation(&self) -> CancellationToken {
        self.cancel.clone()
    }
    fn failed(&self, error: &mut her_llm::LlmError) {
        // Keep actionable quota/eligibility messages. Never substitute an API key or change route.
        if error.kind == her_llm::ErrorKind::Auth {
            error.message = login_required().into_message();
        }
    }
}
