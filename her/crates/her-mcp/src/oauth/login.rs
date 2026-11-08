//! The authorization code flow with PKCE (S256) and a loopback redirect on 127.0.0.1 (RFC
//! 8252), or on the client's own device, which hands back what the redirect carried; the token
//! exchange with `resource` (RFC 8707), and refreshing.

use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use her_protocol::McpOAuthClient;
use her_protocol::customize::McpOAuthClientKind;
use rand::RngCore;
use reqwest::Url;
use serde::Deserialize;
use serde_json::json;
use std::time::Duration;
use tokio::io::AsyncReadExt;
use tokio::io::AsyncWriteExt;
use tokio::net::TcpListener;
use tokio::net::TcpStream;
use tokio::sync::mpsc;
use tokio::sync::oneshot;
use tokio_util::sync::CancellationToken;

use super::OAuthPaths;
use super::StoredToken;
use super::client;
use super::client::ClientChoice;
use super::discovery;
use super::discovery::AuthServerMeta;

const TOKEN_TIMEOUT: Duration = Duration::from_secs(20);

/// A sign-in to start.
pub struct LoginRequest {
    /// The server's name: the tokens are saved under it.
    pub server: String,
    /// The server's address, which is also the `resource` of the tokens.
    pub url: String,
    /// The user's own client, its secret already expanded.
    pub own: Option<McpOAuthClient>,
    /// her's client metadata document, when one is published.
    pub metadata_url: Option<String>,
    /// Where registrations are kept; `None` registers anew each time (tests).
    pub paths: Option<OAuthPaths>,
    pub http: reqwest::Client,
    /// The identity the person chose, when the server allows it.
    pub prefer: Option<McpOAuthClientKind>,
    /// A loopback address on the client's device, already checked, for a sign-in whose browser
    /// is there: nothing listens here, and [`Completion::complete`] brings what the redirect
    /// carried.
    pub redirect_uri: Option<String>,
}

/// A sign-in waiting for the browser to come back.
pub struct PendingLogin {
    /// Open this in the system browser.
    pub authorize_url: String,
    pub client: McpOAuthClientKind,
    done: oneshot::Receiver<Result<(), String>>,
    cancel: CancellationToken,
    completion: Option<Completion>,
}

impl PendingLogin {
    /// Waits for the redirect and the token exchange; gives up after `timeout`.
    pub async fn wait(self, timeout: Duration) -> Result<(), String> {
        let cancel = self.cancel.clone();
        let result = match tokio::time::timeout(timeout, self.done).await {
            Ok(Ok(result)) => result,
            Ok(Err(_)) => Err("the sign-in was cancelled".into()),
            Err(_) => Err(format!("no answer from the browser within {} minutes", timeout.as_secs() / 60)),
        };
        cancel.cancel();
        result
    }

    /// Ends the wait; the browser then finds nothing listening.
    pub fn canceller(&self) -> CancellationToken {
        self.cancel.clone()
    }

    /// How the client finishes a sign-in started with a `redirect_uri`; `None` when her listens
    /// for the browser itself.
    pub fn completion(&self) -> Option<Completion> {
        self.completion.clone()
    }
}

/// What a redirect to the client's device carried, and where the outcome goes.
type Redirected = (Redirect, oneshot::Sender<Result<(), String>>);

struct Redirect {
    code: String,
    state: String,
    iss: Option<String>,
}

/// Finishes a sign-in started with a `redirect_uri`, with what its redirect carried.
#[derive(Clone)]
pub struct Completion {
    state: String,
    redirects: mpsc::Sender<Redirected>,
}

impl Completion {
    /// Whether `state` is this sign-in's.
    pub fn matches(&self, state: &str) -> bool {
        self.state == state
    }

    /// Hands over the redirect's `code`, `state` and `iss`, and waits for the tokens to be
    /// saved. A state that is not the sign-in's leaves it waiting, as a stray callback does.
    pub async fn complete(&self, code: String, state: String, iss: Option<String>) -> Result<(), String> {
        let ended = || "the sign-in has ended".to_string();
        let (outcome, answer) = oneshot::channel();
        self.redirects.send((Redirect { code, state, iss }, outcome)).await.map_err(|_| ended())?;
        answer.await.unwrap_or_else(|_| Err(ended()))
    }
}

fn random(len: usize) -> String {
    let mut bytes = vec![0u8; len];
    rand::rng().fill_bytes(&mut bytes);
    URL_SAFE_NO_PAD.encode(bytes)
}

/// `application/x-www-form-urlencoded` of `pairs`.
pub(crate) fn form(pairs: &[(&str, &str)]) -> String {
    let encode = |s: &str| -> String {
        s.bytes()
            .map(|b| match b {
                b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => (b as char).to_string(),
                _ => format!("%{b:02X}"),
            })
            .collect()
    };
    pairs.iter().map(|(k, v)| format!("{}={}", encode(k), encode(v))).collect::<Vec<_>>().join("&")
}

/// The server's challenge without credentials: its 401 says where its metadata is.
async fn challenge(http: &reqwest::Client, url: &str) -> discovery::Challenge {
    let body = json!({"jsonrpc": "2.0", "id": 0, "method": "initialize", "params": {
        "protocolVersion": crate::types::LATEST_PROTOCOL_VERSION, "capabilities": {}, "clientInfo": {"name": "her", "version": env!("CARGO_PKG_VERSION")}}});
    let post = http.post(url).header("accept", "application/json, text/event-stream").json(&body).timeout(Duration::from_secs(15)).send();
    let header = |r: &reqwest::Response| r.headers().get("www-authenticate").and_then(|v| v.to_str().ok()).map(discovery::parse_challenge);
    if let Ok(response) = post.await {
        if let Some(c) = header(&response) {
            return c;
        }
        if response.status().as_u16() != 405 && response.status().as_u16() != 404 {
            return discovery::Challenge::default();
        }
    }
    // The older transport refuses POST; its event stream carries the challenge.
    let get = http.get(url).header("accept", "text/event-stream").timeout(Duration::from_secs(15)).send();
    match get.await {
        Ok(response) => header(&response).unwrap_or_default(),
        Err(_) => discovery::Challenge::default(),
    }
}

/// Starts signing in: discovery, the client identity, the loopback listener. The returned
/// address goes to the browser; [`PendingLogin::wait`] ends when the tokens are saved.
pub async fn start_login(req: LoginRequest) -> Result<PendingLogin, String> {
    let http = &req.http;
    let found = challenge(http, &req.url).await;
    let resource = discovery::resource_metadata(http, &req.url, found.resource_metadata.as_deref())
        .await
        .map_err(|e| format!("the server publishes no sign-in settings ({e})"))?;
    let issuer = resource.authorization_servers[0].clone();
    let meta = discovery::auth_server_metadata(http, &issuer).await?;
    if !meta.supports_s256() {
        return Err(format!("{} does not support PKCE with S256, which signing in requires; use a token instead", meta.issuer));
    }
    let kind = client::choose_kind(req.own.is_some(), &meta, req.metadata_url.as_deref(), req.prefer).ok_or_else(|| {
        format!(
            "{} neither reads client metadata documents nor offers registration; set your own OAuth client (oauth.client_id) or use a token",
            meta.issuer
        )
    })?;
    // A reused registration keeps its redirect URI, port and all; otherwise any free port. A
    // sign-in whose browser is on the client's device uses the address it was given.
    let previous = match (&kind, &req.paths) {
        (McpOAuthClientKind::Registration, Some(paths)) => {
            let (paths, issuer) = (paths.clone(), meta.issuer.clone());
            tokio::task::spawn_blocking(move || client::registered(&paths, &issuer)).await.ok().flatten()
        }
        _ => None,
    };
    let (listener, redirect_uri) = match req.redirect_uri.clone() {
        Some(uri) => (None, uri),
        None => {
            let reuse_port = previous.as_ref().and_then(|(reg, _)| Url::parse(&reg.redirect_uri).ok()).and_then(|u| u.port());
            let listener = match reuse_port {
                Some(port) => match TcpListener::bind(("127.0.0.1", port)).await {
                    Ok(listener) => listener,
                    Err(_) => TcpListener::bind(("127.0.0.1", 0)).await.map_err(|e| format!("cannot listen for the sign-in: {e}"))?,
                },
                None => TcpListener::bind(("127.0.0.1", 0)).await.map_err(|e| format!("cannot listen for the sign-in: {e}"))?,
            };
            let port = listener.local_addr().map_err(|e| e.to_string())?.port();
            (Some(listener), format!("http://127.0.0.1:{port}/callback"))
        }
    };
    let choice = match kind {
        McpOAuthClientKind::Managed => return Err("managed CLI authorization is handled by the connector runtime".into()),
        McpOAuthClientKind::Own => {
            let own = req.own.clone().unwrap_or_default();
            ClientChoice { kind, client_id: own.client_id, client_secret: own.client_secret, redirect_uri: None }
        }
        McpOAuthClientKind::MetadataDocument => ClientChoice {
            kind,
            client_id: req.metadata_url.clone().unwrap_or_default(),
            client_secret: None,
            redirect_uri: None,
        },
        McpOAuthClientKind::Registration => match previous.filter(|(reg, _)| reg.redirect_uri == redirect_uri) {
            Some((reg, secret)) => ClientChoice { kind, client_id: reg.client_id, client_secret: secret, redirect_uri: Some(reg.redirect_uri) },
            None => {
                let (reg, secret) = client::register(http, &meta, &redirect_uri).await?;
                if let Some(paths) = req.paths.clone() {
                    let (issuer, saved, secret) = (meta.issuer.clone(), reg.clone(), secret.clone());
                    if let Ok(Err(err)) = tokio::task::spawn_blocking(move || client::remember(&paths, &issuer, &saved, secret.as_deref())).await {
                        tracing::warn!(%err, "cannot keep the client registration");
                    }
                }
                ClientChoice { kind, client_id: reg.client_id, client_secret: secret, redirect_uri: Some(reg.redirect_uri) }
            }
        },
    };
    let verifier = random(32);
    let digest = ring::digest::digest(&ring::digest::SHA256, verifier.as_bytes());
    let code_challenge = URL_SAFE_NO_PAD.encode(digest.as_ref());
    let state = random(16);
    let scopes: Vec<String> = match &req.own {
        Some(own) if !own.scopes.is_empty() => own.scopes.clone(),
        _ => match &found.scope {
            Some(scope) => scope.split_whitespace().map(String::from).collect(),
            None => resource.scopes_supported.clone(),
        },
    };
    let mut authorize = Url::parse(&meta.authorization_endpoint).map_err(|e| format!("invalid authorization endpoint: {e}"))?;
    {
        let mut q = authorize.query_pairs_mut();
        q.append_pair("response_type", "code")
            .append_pair("client_id", &choice.client_id)
            .append_pair("redirect_uri", &redirect_uri)
            .append_pair("code_challenge", &code_challenge)
            .append_pair("code_challenge_method", "S256")
            .append_pair("state", &state)
            .append_pair("resource", &req.url);
        if !scopes.is_empty() {
            q.append_pair("scope", &scopes.join(" "));
        }
    }
    let (tx, done) = oneshot::channel();
    let cancel = CancellationToken::new();
    let exchange = Exchange {
        server: req.server.clone(),
        resource: req.url.clone(),
        meta,
        client: choice,
        redirect_uri,
        verifier,
        state,
        http: req.http.clone(),
        paths: req.paths.clone(),
    };
    let completion = match listener {
        Some(listener) => {
            tokio::spawn(serve_callback(listener, exchange, tx, cancel.clone()));
            None
        }
        None => {
            let (redirects, received) = mpsc::channel(4);
            let completion = Completion { state: exchange.state.clone(), redirects };
            tokio::spawn(await_redirect(received, exchange, tx, cancel.clone()));
            Some(completion)
        }
    };
    Ok(PendingLogin { authorize_url: authorize.to_string(), client: kind, done, cancel, completion })
}

struct Exchange {
    server: String,
    resource: String,
    meta: AuthServerMeta,
    client: ClientChoice,
    redirect_uri: String,
    verifier: String,
    state: String,
    http: reqwest::Client,
    paths: Option<OAuthPaths>,
}

#[derive(Deserialize)]
struct TokenResponse {
    access_token: String,
    #[serde(default)]
    refresh_token: Option<String>,
    #[serde(default)]
    expires_in: Option<u64>,
    #[serde(default)]
    scope: Option<String>,
}

/// Posts to the token endpoint; a secret, when there is one, goes in Basic authentication.
async fn token_request(http: &reqwest::Client, endpoint: &str, client_id: &str, secret: Option<&str>, pairs: &[(&str, &str)]) -> Result<TokenResponse, String> {
    let mut request = http.post(endpoint).header("content-type", "application/x-www-form-urlencoded").header("accept", "application/json").timeout(TOKEN_TIMEOUT);
    let mut pairs = pairs.to_vec();
    match secret {
        Some(secret) => request = request.basic_auth(client_id, Some(secret)),
        None => pairs.push(("client_id", client_id)),
    }
    let response = request.body(form(&pairs)).send().await.map_err(|e| format!("the token request failed: {e}"))?;
    let status = response.status();
    let value: serde_json::Value = response.json().await.unwrap_or_default();
    if !status.is_success() {
        let error = value["error"].as_str().unwrap_or("error");
        let why = value["error_description"].as_str().map(|d| format!(": {d}")).unwrap_or_default();
        return Err(format!("{error}{why} (HTTP {})", status.as_u16()));
    }
    serde_json::from_value(value).map_err(|e| format!("the token response is not usable: {e}"))
}

/// `base` with what a token response carries; a refresh token that is not renewed stays.
fn apply(mut base: StoredToken, t: TokenResponse) -> StoredToken {
    base.access_token = t.access_token;
    base.refresh_token = t.refresh_token.or(base.refresh_token);
    base.expires_at = t.expires_in.map(|s| super::now_secs() + s);
    base.scope = t.scope.or(base.scope);
    base
}

/// A new access token from `refresh`, keeping the refresh token when no new one is issued.
pub(crate) async fn refresh(http: &reqwest::Client, token: &StoredToken, refresh: &str) -> Result<StoredToken, String> {
    let t = token_request(
        http,
        &token.token_endpoint,
        &token.client_id,
        token.client_secret.as_deref(),
        &[("grant_type", "refresh_token"), ("refresh_token", refresh), ("resource", &token.resource)],
    )
    .await?;
    Ok(apply(token.clone(), t))
}

const PAGE_OK: &str = "<!doctype html><meta charset=utf-8><title>Hermesh</title><body style=\"font:15px -apple-system,system-ui,sans-serif;display:flex;align-items:center;justify-content:center;height:90vh;color:#333\"><div style=\"text-align:center\"><h2 style=\"font-weight:600\">已登录</h2><p>可以回到 Hermesh 了。</p><p style=\"color:#888\">Signed in. You can return to Hermesh.</p></div>";

fn page_failed(why: &str) -> String {
    let why: String = why.chars().filter(|c| !matches!(c, '<' | '>' | '&' | '"')).collect();
    format!(
        "<!doctype html><meta charset=utf-8><title>Hermesh</title><body style=\"font:15px -apple-system,system-ui,sans-serif;display:flex;align-items:center;justify-content:center;height:90vh;color:#333\"><div style=\"text-align:center;max-width:520px\"><h2 style=\"font-weight:600\">没有登录成功</h2><p>{why}</p><p style=\"color:#888\">Sign-in failed. Return to Hermesh and try again.</p></div>"
    )
}

async fn respond(stream: &mut TcpStream, status: &str, body: &str) {
    let head = format!("HTTP/1.1 {status}\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len());
    let _ = stream.write_all(head.as_bytes()).await;
    let _ = stream.write_all(body.as_bytes()).await;
    let _ = stream.flush().await;
}

/// The query of the first request line, when it is `GET /callback?…`; `None` for anything else.
async fn read_callback(stream: &mut TcpStream) -> Option<std::collections::HashMap<String, String>> {
    let mut buf = Vec::new();
    let mut chunk = [0u8; 4096];
    while !buf.windows(4).any(|w| w == b"\r\n\r\n") && buf.len() < 16 * 1024 {
        let n = tokio::time::timeout(Duration::from_secs(10), stream.read(&mut chunk)).await.ok()?.ok()?;
        if n == 0 {
            break;
        }
        buf.extend_from_slice(&chunk[..n]);
    }
    let head = String::from_utf8_lossy(&buf);
    let target = head.lines().next()?.split(' ').nth(1)?.to_string();
    let url = Url::parse(&format!("http://127.0.0.1{target}")).ok()?;
    (url.path() == "/callback").then(|| url.query_pairs().into_owned().collect())
}

async fn serve_callback(listener: TcpListener, ex: Exchange, done: oneshot::Sender<Result<(), String>>, cancel: CancellationToken) {
    // Each connection is read on its own, so one that sends nothing (a browser's speculative
    // connection) does not hold up the redirect behind it.
    let (tx, mut rx) = tokio::sync::mpsc::channel::<(TcpStream, Option<std::collections::HashMap<String, String>>)>(8);
    let result = loop {
        let (mut stream, query) = tokio::select! {
            _ = cancel.cancelled() => return,
            accepted = listener.accept() => {
                if let Ok((mut stream, _)) = accepted {
                    let tx = tx.clone();
                    tokio::spawn(async move {
                        let query = read_callback(&mut stream).await;
                        let _ = tx.send((stream, query)).await;
                    });
                }
                continue;
            }
            Some(read) = rx.recv() => read,
        };
        let Some(query) = query else {
            respond(&mut stream, "404 Not Found", "").await;
            continue;
        };
        if query.get("state") != Some(&ex.state) {
            respond(&mut stream, "400 Bad Request", &page_failed("这个链接不属于这次登录。")).await;
            continue;
        }
        if let Some(error) = query.get("error") {
            let why = query.get("error_description").map(|d| format!("{error}: {d}")).unwrap_or_else(|| error.clone());
            respond(&mut stream, "200 OK", &page_failed(&why)).await;
            break Err(format!("the authorization server refused: {why}"));
        }
        if ex.meta.authorization_response_iss_parameter_supported && query.get("iss") != Some(&ex.meta.issuer) {
            respond(&mut stream, "400 Bad Request", &page_failed("回调来自另一个授权服务器。")).await;
            break Err("the redirect came from another authorization server (iss does not match)".into());
        }
        let Some(code) = query.get("code") else {
            respond(&mut stream, "400 Bad Request", &page_failed("回调里没有授权码。")).await;
            continue;
        };
        let saved = redeem(&ex, code).await;
        match &saved {
            Ok(()) => respond(&mut stream, "200 OK", PAGE_OK).await,
            Err(err) => respond(&mut stream, "200 OK", &page_failed(err)).await,
        }
        break saved;
    };
    let _ = done.send(result);
}

/// A sign-in whose browser is on the client's device: what the redirect carried comes from the
/// client, through [`Completion::complete`].
async fn await_redirect(mut redirects: mpsc::Receiver<Redirected>, ex: Exchange, done: oneshot::Sender<Result<(), String>>, cancel: CancellationToken) {
    let result = loop {
        let (redirect, outcome) = tokio::select! {
            _ = cancel.cancelled() => return,
            next = redirects.recv() => match next {
                Some(next) => next,
                None => return,
            },
        };
        if redirect.state != ex.state {
            let _ = outcome.send(Err("the state does not belong to this sign-in".into()));
            continue;
        }
        let result = if ex.meta.authorization_response_iss_parameter_supported && redirect.iss.as_ref() != Some(&ex.meta.issuer) {
            Err("the redirect came from another authorization server (iss does not match)".into())
        } else {
            redeem(&ex, &redirect.code).await
        };
        let _ = outcome.send(result.clone());
        break result;
    };
    let _ = done.send(result);
}

/// Trades the code for tokens and saves them under the server's name.
async fn redeem(ex: &Exchange, code: &str) -> Result<(), String> {
    let pairs = [
        ("grant_type", "authorization_code"),
        ("code", code),
        ("redirect_uri", ex.redirect_uri.as_str()),
        ("code_verifier", ex.verifier.as_str()),
        ("resource", ex.resource.as_str()),
    ];
    let exchanged = token_request(&ex.http, &ex.meta.token_endpoint, &ex.client.client_id, ex.client.client_secret.as_deref(), &pairs).await;
    match exchanged {
        Ok(t) => {
            let base = StoredToken {
                access_token: String::new(),
                refresh_token: None,
                expires_at: None,
                scope: None,
                resource: ex.resource.clone(),
                issuer: ex.meta.issuer.clone(),
                token_endpoint: ex.meta.token_endpoint.clone(),
                revocation_endpoint: ex.meta.revocation_endpoint.clone(),
                client_id: ex.client.client_id.clone(),
                client_secret: ex.client.client_secret.clone(),
            };
            let token = apply(base, t);
            let server = ex.server.clone();
            tokio::task::spawn_blocking(move || super::save(&server, &token)).await.map_err(|e| e.to_string()).and_then(|r| r)
        }
        Err(err) => {
            // A registration the server forgot: register again next time.
            if err.starts_with("invalid_client")
                && ex.client.kind == McpOAuthClientKind::Registration
                && let Some(paths) = ex.paths.clone()
            {
                let issuer = ex.meta.issuer.clone();
                let _ = tokio::task::spawn_blocking(move || client::forget(&paths, &issuer)).await;
            }
            Err(err)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn form_encoding() {
        assert_eq!(form(&[("a b", "x/y?z"), ("k", "~ok")]), "a%20b=x%2Fy%3Fz&k=~ok");
    }
}
