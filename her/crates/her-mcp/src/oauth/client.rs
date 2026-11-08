//! The client identity a sign-in uses (D-182): the user's own client, else her's client
//! metadata document when the authorization server reads them, else a dynamic registration,
//! made once per authorization server and reused, so reconnecting never piles up clients.

use her_protocol::customize::McpOAuthClientKind;
use serde::Deserialize;
use serde::Serialize;
use serde_json::json;
use std::collections::BTreeMap;
use std::time::Duration;

use super::OAuthPaths;
use super::discovery::AuthServerMeta;

/// Which identity a sign-in will use, before it starts: `prefer` when it is possible here,
/// else the user's own client, the metadata document, then registration.
pub fn choose_kind(own: bool, meta: &AuthServerMeta, metadata_url: Option<&str>, prefer: Option<McpOAuthClientKind>) -> Option<McpOAuthClientKind> {
    let possible = |kind: McpOAuthClientKind| match kind {
        McpOAuthClientKind::Own => own,
        McpOAuthClientKind::Managed => false,
        McpOAuthClientKind::MetadataDocument => meta.client_id_metadata_document_supported && metadata_url.is_some(),
        McpOAuthClientKind::Registration => meta.registration_endpoint.is_some(),
    };
    if let Some(kind) = prefer.filter(|k| possible(*k)) {
        return Some(kind);
    }
    if own {
        Some(McpOAuthClientKind::Own)
    } else if meta.client_id_metadata_document_supported && metadata_url.is_some() {
        Some(McpOAuthClientKind::MetadataDocument)
    } else if meta.registration_endpoint.is_some() {
        Some(McpOAuthClientKind::Registration)
    } else {
        None
    }
}

/// The client a sign-in uses.
#[derive(Clone, Debug)]
pub struct ClientChoice {
    pub kind: McpOAuthClientKind,
    pub client_id: String,
    pub client_secret: Option<String>,
    /// A registered client's redirect URI, which the sign-in must use as it is.
    pub redirect_uri: Option<String>,
}

/// One dynamic registration, kept in `~/.her/mcp/oauth-clients.json` by issuer. A secret, if
/// the server issued one, is in the credential store, not in the file.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub(crate) struct Registration {
    pub client_id: String,
    pub redirect_uri: String,
    pub registered_at: u64,
    #[serde(default)]
    pub has_secret: bool,
}

fn secret_name(issuer: &str) -> String {
    let digest = ring::digest::digest(&ring::digest::SHA256, issuer.as_bytes());
    let id: String = digest.as_ref()[..6].iter().map(|b| format!("{b:02x}")).collect();
    format!("mcp-oauth-client.{id}")
}

fn read_all(paths: &OAuthPaths) -> BTreeMap<String, Registration> {
    std::fs::read_to_string(paths.registrations())
        .ok()
        .and_then(|text| serde_json::from_str(&text).ok())
        .unwrap_or_default()
}

fn write_all(paths: &OAuthPaths, all: &BTreeMap<String, Registration>) -> Result<(), String> {
    let path = paths.registrations();
    let dir = path.parent().unwrap_or(&path);
    std::fs::create_dir_all(dir).map_err(|e| format!("{}: {e}", dir.display()))?;
    let text = serde_json::to_string_pretty(all).map_err(|e| e.to_string())?;
    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, text).map_err(|e| format!("{}: {e}", tmp.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o600));
    }
    std::fs::rename(&tmp, &path).map_err(|e| format!("{}: {e}", path.display()))
}

/// The registration made earlier with `issuer`, with its secret.
pub(crate) fn registered(paths: &OAuthPaths, issuer: &str) -> Option<(Registration, Option<String>)> {
    let reg = read_all(paths).remove(issuer)?;
    let secret = reg.has_secret.then(|| her_llm::credentials::get(&secret_name(issuer))).flatten();
    Some((reg, secret))
}

pub(crate) fn remember(paths: &OAuthPaths, issuer: &str, reg: &Registration, secret: Option<&str>) -> Result<(), String> {
    if let Some(secret) = secret {
        her_llm::credentials::set(&secret_name(issuer), secret)?;
    }
    let mut all = read_all(paths);
    all.insert(issuer.to_string(), reg.clone());
    write_all(paths, &all)
}

pub(crate) fn forget(paths: &OAuthPaths, issuer: &str) {
    let mut all = read_all(paths);
    if let Some(reg) = all.remove(issuer) {
        if reg.has_secret {
            let _ = her_llm::credentials::delete(&secret_name(issuer));
        }
        let _ = write_all(paths, &all);
    }
}

/// Registers her as a public client (RFC 7591) for `redirect_uri`.
pub(crate) async fn register(http: &reqwest::Client, meta: &AuthServerMeta, redirect_uri: &str) -> Result<(Registration, Option<String>), String> {
    let endpoint = meta.registration_endpoint.as_deref().ok_or("the authorization server does not offer registration")?;
    let body = json!({
        "client_name": "Hermesh",
        "redirect_uris": [redirect_uri],
        "grant_types": ["authorization_code", "refresh_token"],
        "response_types": ["code"],
        "token_endpoint_auth_method": "none",
    });
    let response = http
        .post(endpoint)
        .json(&body)
        .timeout(Duration::from_secs(15))
        .send()
        .await
        .map_err(|e| format!("registering the client failed: {e}"))?;
    let status = response.status();
    let value: serde_json::Value = response.json().await.unwrap_or_default();
    if !status.is_success() {
        let why = value["error_description"].as_str().or(value["error"].as_str()).unwrap_or("no reason given");
        return Err(format!("registering the client failed: HTTP {} ({why})", status.as_u16()));
    }
    let client_id = value["client_id"].as_str().ok_or("the registration returned no client_id")?.to_string();
    let secret = value["client_secret"].as_str().filter(|s| !s.is_empty()).map(String::from);
    Ok((
        Registration {
            client_id,
            redirect_uri: redirect_uri.to_string(),
            registered_at: super::now_secs(),
            has_secret: secret.is_some(),
        },
        secret,
    ))
}
