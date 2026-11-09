//! Signing in to remote MCP servers with OAuth 2.1 (mcp-design §13.8): discovery from the
//! server's protected resource metadata, the client identity (the user's own client, her's
//! client metadata document, or dynamic registration reused per authorization server), the
//! authorization code flow with PKCE and a loopback redirect, and tokens kept in the credential
//! store, refreshed under a cross-process lock.

mod client;
pub mod discovery;
mod login;

use async_trait::async_trait;
use serde::Deserialize;
use serde::Serialize;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use std::time::SystemTime;
use std::time::UNIX_EPOCH;

pub use client::ClientChoice;
pub use client::choose_kind;
pub use login::Completion;
pub use login::LoginRequest;
pub use login::PendingLogin;
pub use login::start_login;

use crate::McpError;

/// Where her keeps what OAuth needs on disk: `run/locks` for refreshing, `mcp` for client
/// registrations. Both live under her's home.
#[derive(Clone, Debug)]
pub struct OAuthPaths {
    pub her_home: PathBuf,
}

impl OAuthPaths {
    fn lock_dir(&self) -> PathBuf {
        self.her_home.join("run").join("locks")
    }

    fn registrations(&self) -> PathBuf {
        self.her_home.join("mcp").join("oauth-clients.json")
    }
}

/// Gives a transport the access token for each request.
#[async_trait]
pub trait BearerSource: Send + Sync {
    /// The token to send now, refreshed first when it is about to expire.
    async fn token(&self) -> Result<String, McpError>;
    /// The server refused `stale`: a newer token, or why someone has to sign in again.
    async fn refresh(&self, stale: &str) -> Result<String, McpError>;
}

/// What a sign-in leaves in the credential store, as one line of JSON under
/// [`credential_name`]. Bound to the server's address: another address needs a new sign-in.
#[derive(Clone, Serialize, Deserialize)]
pub struct StoredToken {
    pub access_token: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub refresh_token: Option<String>,
    /// Seconds since the epoch.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub expires_at: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub scope: Option<String>,
    /// The server address the token was issued for (`resource`).
    pub resource: String,
    pub issuer: String,
    pub token_endpoint: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub revocation_endpoint: Option<String>,
    pub client_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub client_secret: Option<String>,
}

/// The credential holding a server's tokens.
pub fn credential_name(server: &str) -> String {
    format!("mcp-oauth.{server}")
}

pub(crate) fn now_secs() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)
}

/// The saved tokens of `server`, read from the store itself (not the process cache, which
/// another process may have made stale).
pub fn load(server: &str) -> Option<StoredToken> {
    let name = credential_name(server);
    her_llm::credentials::forget(&name);
    her_llm::credentials::get(&name).and_then(|text| serde_json::from_str(&text).ok())
}

pub(crate) fn save(server: &str, token: &StoredToken) -> Result<(), String> {
    let text = serde_json::to_string(token).map_err(|e| e.to_string())?;
    her_llm::credentials::set(&credential_name(server), &text)
}

/// How someone signs in to `server` again: the words an error that needs a sign-in ends with.
pub fn sign_in_way(server: &str) -> String {
    format!("sign in with `her mcp login {server}` or from the 自定义 page")
}

/// Whether `server` has tokens for `url`.
pub fn signed_in(server: &str, url: &str) -> bool {
    load(server).is_some_and(|t| t.resource == url)
}

/// Signs out: revokes the refresh token when the authorization server offers revocation,
/// then deletes the saved tokens. Revocation failures do not stop the deletion.
pub async fn logout(server: &str, http: &reqwest::Client) -> Result<(), String> {
    let token = tokio::task::spawn_blocking({
        let server = server.to_string();
        move || load(&server)
    })
    .await
    .map_err(|e| e.to_string())?;
    if let Some(token) = &token
        && let Some(endpoint) = &token.revocation_endpoint
    {
        let value = token.refresh_token.as_deref().unwrap_or(&token.access_token);
        let mut form = vec![("token", value), ("client_id", token.client_id.as_str())];
        if token.refresh_token.is_some() {
            form.push(("token_type_hint", "refresh_token"));
        }
        let request = http.post(endpoint).header("content-type", "application/x-www-form-urlencoded").body(login::form(&form));
        if let Err(err) = tokio::time::timeout(Duration::from_secs(10), request.send()).await {
            tracing::debug!(server, %err, "token revocation timed out");
        }
    }
    let name = credential_name(server);
    tokio::task::spawn_blocking(move || match her_llm::credentials::get(&name) {
        Some(_) => her_llm::credentials::delete(&name),
        None => Ok(()),
    })
    .await
    .map_err(|e| e.to_string())?
}

/// The tokens of one configured server, for its transport.
pub struct TokenSource {
    server: String,
    url: String,
    http: reqwest::Client,
    paths: Option<OAuthPaths>,
    cached: parking_lot::Mutex<Option<StoredToken>>,
}

/// Tokens closer to expiry than this are refreshed before use.
const EXPIRY_MARGIN_SECS: u64 = 60;

impl TokenSource {
    pub fn new(server: &str, url: &str, http: reqwest::Client, paths: Option<OAuthPaths>) -> Arc<TokenSource> {
        Arc::new(TokenSource {
            server: server.to_string(),
            url: url.to_string(),
            http,
            paths,
            cached: parking_lot::Mutex::new(None),
        })
    }

    fn sign_in_hint(&self, why: &str) -> McpError {
        McpError::NeedsLogin(format!("{why}; {}", sign_in_way(&self.server)))
    }

    async fn stored(&self) -> Result<StoredToken, McpError> {
        if let Some(token) = self.cached.lock().clone() {
            return Ok(token);
        }
        let server = self.server.clone();
        let token = tokio::task::spawn_blocking(move || load(&server)).await.ok().flatten();
        match token {
            Some(token) if token.resource == self.url => {
                *self.cached.lock() = Some(token.clone());
                Ok(token)
            }
            Some(_) => Err(self.sign_in_hint("the saved sign-in was for another address")),
            None => Err(self.sign_in_hint("not signed in")),
        }
    }

    /// Refreshes under the lock, unless another process already has.
    async fn renew(&self, stale: &str) -> Result<String, McpError> {
        let lock = match &self.paths {
            Some(paths) => {
                let dir = paths.lock_dir();
                let path = dir.join(format!("mcp-oauth-{}.lock", self.server));
                Some(tokio::task::spawn_blocking(move || lock_file(&dir, &path)).await.ok().flatten())
            }
            None => None,
        };
        let server = self.server.clone();
        let fresh = tokio::task::spawn_blocking(move || load(&server)).await.ok().flatten();
        let Some(token) = fresh.filter(|t| t.resource == self.url) else {
            return Err(self.sign_in_hint("not signed in"));
        };
        let usable = |t: &StoredToken| t.expires_at.is_none_or(|at| at > now_secs() + EXPIRY_MARGIN_SECS);
        if token.access_token != stale && usable(&token) {
            *self.cached.lock() = Some(token.clone());
            return Ok(token.access_token);
        }
        let Some(refresh) = token.refresh_token.clone() else {
            *self.cached.lock() = None;
            return Err(self.sign_in_hint("the sign-in expired"));
        };
        let renewed = login::refresh(&self.http, &token, &refresh).await;
        drop(lock);
        match renewed {
            Ok(renewed) => {
                let server = self.server.clone();
                let saved = renewed.clone();
                if let Ok(Err(err)) = tokio::task::spawn_blocking(move || save(&server, &saved)).await {
                    tracing::warn!(server = %self.server, %err, "cannot save the refreshed token");
                }
                let access = renewed.access_token.clone();
                *self.cached.lock() = Some(renewed);
                Ok(access)
            }
            Err(err) => {
                *self.cached.lock() = None;
                Err(self.sign_in_hint(&format!("refreshing the sign-in failed ({err})")))
            }
        }
    }
}

#[async_trait]
impl BearerSource for TokenSource {
    async fn token(&self) -> Result<String, McpError> {
        let token = self.stored().await?;
        let expiring = token.expires_at.is_some_and(|at| at <= now_secs() + EXPIRY_MARGIN_SECS);
        if expiring {
            return self.renew(&token.access_token).await;
        }
        Ok(token.access_token)
    }

    async fn refresh(&self, stale: &str) -> Result<String, McpError> {
        self.renew(stale).await
    }
}

/// An exclusive lock on `path`, waited for up to ten seconds; `None` when it cannot be had,
/// and the refresh goes ahead without it.
fn lock_file(dir: &std::path::Path, path: &std::path::Path) -> Option<std::fs::File> {
    std::fs::create_dir_all(dir).ok()?;
    let file = std::fs::File::options().create(true).truncate(false).write(true).open(path).ok()?;
    let start = Instant::now();
    loop {
        match file.try_lock() {
            Ok(()) => return Some(file),
            Err(std::fs::TryLockError::WouldBlock) if start.elapsed() < Duration::from_secs(10) => std::thread::sleep(Duration::from_millis(25)),
            Err(_) => return None,
        }
    }
}
