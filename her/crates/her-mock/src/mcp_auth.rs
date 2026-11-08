//! Signing in to the MCP test server: a protected resource with an OAuth 2.1 authorization
//! server beside it (metadata, dynamic registration, authorization with PKCE, tokens,
//! refresh, revocation), or a fixed bearer token. The authorization endpoint approves at once
//! and redirects, so following its redirect stands in for the person in the browser.

use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;
use std::io;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use std::time::Duration;
use std::time::Instant;
use tokio::io::AsyncReadExt;
use tokio::io::AsyncWriteExt;
use tokio::net::TcpStream;

use crate::mcp::HttpRequest;
use crate::mcp::write_response;

/// Who may call the test server.
#[derive(Clone, Debug, Default)]
pub enum McpTestAuth {
    #[default]
    None,
    /// `Authorization: Bearer <token>`; a 401 without resource metadata otherwise.
    Token(String),
    OAuth(OAuthOptions),
}

#[derive(Clone, Debug)]
pub struct OAuthOptions {
    /// Advertise `client_id_metadata_document_supported`.
    pub metadata_document: bool,
    /// Offer a `registration_endpoint`.
    pub registration: bool,
    /// Lifetime of access tokens, in seconds.
    pub token_ttl_secs: u64,
}

impl Default for OAuthOptions {
    fn default() -> Self {
        Self {
            metadata_document: true,
            registration: true,
            token_ttl_secs: 3600,
        }
    }
}

#[derive(Default)]
pub(crate) struct OAuthState {
    /// Registered clients and their redirect URIs.
    clients: Mutex<HashMap<String, Vec<String>>>,
    /// Authorization codes: client, redirect URI, PKCE challenge, resource.
    codes: Mutex<HashMap<String, (String, String, String, String)>>,
    access: Mutex<HashMap<String, Instant>>,
    refresh: Mutex<HashMap<String, String>>,
    next: AtomicU64,
    pub(crate) registrations: AtomicU64,
    pub(crate) grants: AtomicU64,
    pub(crate) refreshes: AtomicU64,
    pub(crate) revocations: AtomicU64,
    /// The `client_id` of the last authorization request.
    pub(crate) last_client: Mutex<Option<String>>,
}

impl OAuthState {
    fn id(&self, prefix: &str) -> String {
        format!("{prefix}-{}", self.next.fetch_add(1, Ordering::Relaxed) + 1)
    }

    fn issue(&self, ttl: u64, client: &str) -> Value {
        let access = self.id("at");
        let refresh = self.id("rt");
        self.access.lock().insert(access.clone(), Instant::now() + Duration::from_secs(ttl));
        self.refresh.lock().insert(refresh.clone(), client.to_string());
        json!({"access_token": access, "token_type": "Bearer", "expires_in": ttl, "refresh_token": refresh, "scope": "read"})
    }

    /// Whether `header` carries a token this server issued and that has not expired.
    pub(crate) fn valid(&self, header: Option<&String>) -> bool {
        let Some(token) = header.and_then(|h| h.strip_prefix("Bearer ")) else { return false };
        self.access.lock().get(token).is_some_and(|until| Instant::now() < *until)
    }
}

/// Whether a request to the MCP endpoint may go on. Writes the 401 when it may not.
pub(crate) async fn admit(stream: &mut TcpStream, request: &HttpRequest, auth: &McpTestAuth, oauth: &OAuthState, base: &str, path: &str) -> io::Result<bool> {
    let header = request.headers.get("authorization");
    let challenge = match auth {
        McpTestAuth::None => return Ok(true),
        McpTestAuth::Token(token) => {
            if header.is_some_and(|h| h.strip_prefix("Bearer ") == Some(token.as_str())) {
                return Ok(true);
            }
            "WWW-Authenticate: Bearer\r\n".to_string()
        }
        McpTestAuth::OAuth(_) => {
            if oauth.valid(header) {
                return Ok(true);
            }
            format!("WWW-Authenticate: Bearer resource_metadata=\"{base}/.well-known/oauth-protected-resource{path}\"\r\n")
        }
    };
    write_response(stream, "401 Unauthorized", &challenge, b"").await?;
    Ok(false)
}

/// Answers the metadata and authorization server endpoints. `None` when `path` is not one.
/// The server's sign-in side: how it is protected, its state, its address and the MCP path.
pub(crate) struct Site<'a> {
    pub(crate) auth: &'a McpTestAuth,
    pub(crate) oauth: &'a OAuthState,
    pub(crate) base: &'a str,
    pub(crate) resource_path: &'a str,
}

pub(crate) async fn route(stream: &mut TcpStream, request: &HttpRequest, path: &str, query: &str, site: &Site<'_>) -> Option<io::Result<()>> {
    let Site { auth, oauth, base, resource_path } = *site;
    let McpTestAuth::OAuth(options) = auth else {
        if path.starts_with("/.well-known/") {
            return Some(write_response(stream, "404 Not Found", "", b"").await);
        }
        return None;
    };
    let json_ok = |body: Value| (String::from("200 OK"), body);
    let reply = match (request.method.as_str(), path) {
        ("GET", p) if p.starts_with("/.well-known/oauth-protected-resource") => json_ok(json!({
            "resource": format!("{base}{resource_path}"),
            "authorization_servers": [base],
            "scopes_supported": ["read"],
            "bearer_methods_supported": ["header"],
        })),
        ("GET", "/.well-known/oauth-authorization-server") => {
            let mut meta = json!({
                "issuer": base,
                "authorization_endpoint": format!("{base}/authorize"),
                "token_endpoint": format!("{base}/token"),
                "revocation_endpoint": format!("{base}/revoke"),
                "response_types_supported": ["code"],
                "grant_types_supported": ["authorization_code", "refresh_token"],
                "code_challenge_methods_supported": ["S256"],
                "token_endpoint_auth_methods_supported": ["none"],
                "authorization_response_iss_parameter_supported": true,
                "client_id_metadata_document_supported": options.metadata_document,
            });
            if options.registration {
                meta["registration_endpoint"] = json!(format!("{base}/register"));
            }
            json_ok(meta)
        }
        ("POST", "/register") if options.registration => {
            let body: Value = serde_json::from_slice(&request.body).unwrap_or_default();
            let uris: Vec<String> = body["redirect_uris"].as_array().map(|a| a.iter().filter_map(|u| u.as_str().map(String::from)).collect()).unwrap_or_default();
            let client = oauth.id("client");
            oauth.clients.lock().insert(client.clone(), uris.clone());
            oauth.registrations.fetch_add(1, Ordering::Relaxed);
            (String::from("201 Created"), json!({"client_id": client, "redirect_uris": uris, "token_endpoint_auth_method": "none"}))
        }
        ("GET", "/authorize") => {
            let q = form(query);
            let get = |k: &str| q.get(k).cloned().unwrap_or_default();
            let (client, redirect) = (get("client_id"), get("redirect_uri"));
            *oauth.last_client.lock() = Some(client.clone());
            let known = if client.starts_with("https://") {
                options.metadata_document && is_loopback(&redirect)
            } else {
                oauth.clients.lock().get(&client).is_some_and(|uris| uris.contains(&redirect))
            };
            if !known || get("code_challenge_method") != "S256" || get("code_challenge").is_empty() {
                return Some(write_json(stream, "400 Bad Request", &json!({"error": "invalid_request", "error_description": format!("unknown client {client} or redirect {redirect}")})).await);
            }
            let code = oauth.id("code");
            oauth.codes.lock().insert(code.clone(), (client, redirect.clone(), get("code_challenge"), get("resource")));
            let sep = if redirect.contains('?') { '&' } else { '?' };
            let location = format!("{redirect}{sep}code={code}&state={}&iss={}", encode(&get("state")), encode(base));
            return Some(write_response(stream, "302 Found", &format!("Location: {location}\r\n"), b"").await);
        }
        ("POST", "/token") => {
            let q = form(&String::from_utf8_lossy(&request.body));
            let get = |k: &str| q.get(k).cloned().unwrap_or_default();
            match get("grant_type").as_str() {
                "authorization_code" => {
                    let Some((client, redirect, challenge, resource)) = oauth.codes.lock().remove(&get("code")) else {
                        return Some(write_json(stream, "400 Bad Request", &json!({"error": "invalid_grant"})).await);
                    };
                    let verifier = get("code_verifier");
                    let digest = ring::digest::digest(&ring::digest::SHA256, verifier.as_bytes());
                    let ok = client == get("client_id")
                        && redirect == get("redirect_uri")
                        && URL_SAFE_NO_PAD.encode(digest.as_ref()) == challenge
                        && resource == format!("{base}{resource_path}");
                    if !ok {
                        return Some(write_json(stream, "400 Bad Request", &json!({"error": "invalid_grant", "error_description": "code, client, redirect, verifier or resource does not match"})).await);
                    }
                    oauth.grants.fetch_add(1, Ordering::Relaxed);
                    json_ok(oauth.issue(options.token_ttl_secs, &client))
                }
                "refresh_token" => {
                    let old = get("refresh_token");
                    let Some(client) = oauth.refresh.lock().remove(&old) else {
                        return Some(write_json(stream, "400 Bad Request", &json!({"error": "invalid_grant"})).await);
                    };
                    oauth.refreshes.fetch_add(1, Ordering::Relaxed);
                    json_ok(oauth.issue(options.token_ttl_secs, &client))
                }
                _ => (String::from("400 Bad Request"), json!({"error": "unsupported_grant_type"})),
            }
        }
        ("POST", "/revoke") => {
            let q = form(&String::from_utf8_lossy(&request.body));
            if let Some(token) = q.get("token") {
                oauth.refresh.lock().remove(token);
                oauth.access.lock().remove(token);
            }
            oauth.revocations.fetch_add(1, Ordering::Relaxed);
            json_ok(json!({}))
        }
        _ if path.starts_with("/.well-known/") => return Some(write_response(stream, "404 Not Found", "", b"").await),
        _ => return None,
    };
    Some(write_json(stream, &reply.0, &reply.1).await)
}

async fn write_json(stream: &mut TcpStream, status: &str, body: &Value) -> io::Result<()> {
    write_response(stream, status, "Content-Type: application/json\r\n", body.to_string().as_bytes()).await
}

fn is_loopback(uri: &str) -> bool {
    uri.starts_with("http://127.0.0.1:") || uri.starts_with("http://localhost:") || uri.starts_with("http://127.0.0.1/") || uri.starts_with("http://localhost/")
}

/// `a=b&c=d`, percent-decoded.
pub(crate) fn form(text: &str) -> HashMap<String, String> {
    text.split('&')
        .filter(|p| !p.is_empty())
        .map(|pair| {
            let (k, v) = pair.split_once('=').unwrap_or((pair, ""));
            (decode(k), decode(v))
        })
        .collect()
}

fn decode(s: &str) -> String {
    let bytes = s.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        match bytes[i] {
            b'+' => out.push(b' '),
            b'%' if i + 2 < bytes.len() => match std::str::from_utf8(&bytes[i + 1..i + 3]).ok().and_then(|h| u8::from_str_radix(h, 16).ok()) {
                Some(b) => {
                    out.push(b);
                    i += 3;
                    continue;
                }
                None => out.push(b'%'),
            },
            b => out.push(b),
        }
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn encode(s: &str) -> String {
    s.bytes()
        .map(|b| match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => (b as char).to_string(),
            _ => format!("%{b:02X}"),
        })
        .collect()
}

/// Plays the person in the browser: GETs `url` and follows redirects (plain http only) until
/// a page answers. Returns the last page's status line and body.
pub async fn browse(url: &str) -> io::Result<(String, String)> {
    let mut url = url.to_string();
    for _ in 0..5 {
        let rest = url.strip_prefix("http://").ok_or_else(|| io::Error::other(format!("only http:// can be followed: {url}")))?;
        let (host, path) = rest.split_once('/').map(|(h, p)| (h.to_string(), format!("/{p}"))).unwrap_or((rest.to_string(), "/".into()));
        let mut stream = TcpStream::connect(&host).await?;
        stream.write_all(format!("GET {path} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\r\n").as_bytes()).await?;
        // Read the head, then as much body as it announces: servers may keep the connection.
        let mut raw = Vec::new();
        let mut chunk = [0u8; 8192];
        let head_end = loop {
            if let Some(pos) = raw.windows(4).position(|w| w == b"\r\n\r\n") {
                break pos;
            }
            let n = stream.read(&mut chunk).await?;
            if n == 0 {
                break raw.len();
            }
            raw.extend_from_slice(&chunk[..n]);
        };
        let head_text = String::from_utf8_lossy(&raw[..head_end]).into_owned();
        let length = head_text.lines().find_map(|l| l.split_once(':').filter(|(k, _)| k.eq_ignore_ascii_case("content-length")).and_then(|(_, v)| v.trim().parse::<usize>().ok()));
        let body_start = (head_end + 4).min(raw.len());
        match length {
            Some(length) => {
                while raw.len() < body_start + length {
                    let n = stream.read(&mut chunk).await?;
                    if n == 0 {
                        break;
                    }
                    raw.extend_from_slice(&chunk[..n]);
                }
            }
            None => {
                stream.read_to_end(&mut raw).await?;
            }
        }
        let head = head_text.as_str();
        let body = String::from_utf8_lossy(&raw[body_start.min(raw.len())..]).into_owned();
        let body = body.as_str();
        let status = head.lines().next().unwrap_or_default().to_string();
        let location = head.lines().find_map(|l| l.split_once(':').filter(|(k, _)| k.eq_ignore_ascii_case("location")).map(|(_, v)| v.trim().to_string()));
        match location {
            Some(next) if status.contains(" 30") => url = next,
            _ => return Ok((status, body.to_string())),
        }
    }
    Err(io::Error::other("too many redirects"))
}
