//! Finding how to sign in to a server: the `WWW-Authenticate` challenge of its 401, its
//! protected resource metadata (RFC 9728) and its authorization server's metadata (RFC 8414,
//! or OpenID Connect discovery).

use reqwest::Url;
use serde::Deserialize;
use serde_json::Value;
use std::time::Duration;

/// Longest wait for one metadata document.
const FETCH_TIMEOUT: Duration = Duration::from_secs(15);

/// What a 401's `WWW-Authenticate: Bearer …` says.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Challenge {
    pub resource_metadata: Option<String>,
    pub scope: Option<String>,
    pub error: Option<String>,
}

/// The parameters of a Bearer challenge, quoted or not.
pub fn parse_challenge(header: &str) -> Challenge {
    let rest = header.trim();
    let rest = rest.strip_prefix("Bearer").or_else(|| rest.strip_prefix("bearer")).unwrap_or(rest);
    let mut out = Challenge::default();
    let mut chars = rest.chars().peekable();
    loop {
        while chars.peek().is_some_and(|c| c.is_whitespace() || *c == ',') {
            chars.next();
        }
        let key: String = chars.by_ref().take_while(|c| *c != '=').collect::<String>().trim().to_ascii_lowercase();
        if key.is_empty() {
            break;
        }
        let value: String = if chars.peek() == Some(&'"') {
            chars.next();
            let mut v = String::new();
            while let Some(c) = chars.next() {
                match c {
                    '\\' => v.extend(chars.next()),
                    '"' => break,
                    c => v.push(c),
                }
            }
            v
        } else {
            chars.by_ref().take_while(|c| *c != ',').collect::<String>().trim().to_string()
        };
        match key.as_str() {
            "resource_metadata" => out.resource_metadata = Some(value),
            "scope" => out.scope = Some(value),
            "error" => out.error = Some(value),
            _ => {}
        }
    }
    out
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct ResourceMeta {
    #[serde(default)]
    pub resource: Option<String>,
    #[serde(default)]
    pub authorization_servers: Vec<String>,
    #[serde(default)]
    pub scopes_supported: Vec<String>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct AuthServerMeta {
    pub issuer: String,
    pub authorization_endpoint: String,
    pub token_endpoint: String,
    #[serde(default)]
    pub registration_endpoint: Option<String>,
    #[serde(default)]
    pub revocation_endpoint: Option<String>,
    #[serde(default)]
    pub code_challenge_methods_supported: Vec<String>,
    #[serde(default)]
    pub client_id_metadata_document_supported: bool,
    #[serde(default)]
    pub authorization_response_iss_parameter_supported: bool,
    #[serde(default)]
    pub scopes_supported: Vec<String>,
}

impl AuthServerMeta {
    /// PKCE with S256, which her requires (D-181).
    pub fn supports_s256(&self) -> bool {
        self.code_challenge_methods_supported.iter().any(|m| m == "S256")
    }
}

/// `https://host/.well-known/<name>/path`, then `https://host/.well-known/<name>`.
fn well_known(url: &Url, name: &str) -> Vec<String> {
    let mut origin = url.clone();
    origin.set_query(None);
    origin.set_fragment(None);
    let path = url.path().trim_end_matches('/').to_string();
    origin.set_path("");
    let base = origin.as_str().trim_end_matches('/').to_string();
    let mut out = Vec::new();
    if !path.is_empty() {
        out.push(format!("{base}/.well-known/{name}{path}"));
    }
    out.push(format!("{base}/.well-known/{name}"));
    out
}

async fn fetch_json(http: &reqwest::Client, url: &str) -> Result<Value, String> {
    let request = http.get(url).header("accept", "application/json").timeout(FETCH_TIMEOUT);
    let response = request.send().await.map_err(|e| format!("{url}: {e}"))?;
    if !response.status().is_success() {
        return Err(format!("{url}: HTTP {}", response.status().as_u16()));
    }
    response.json::<Value>().await.map_err(|e| format!("{url}: not JSON ({e})"))
}

/// The protected resource metadata of `server_url`: from the challenge's address when it gave
/// one, else from the well-known addresses.
pub async fn resource_metadata(http: &reqwest::Client, server_url: &str, hint: Option<&str>) -> Result<ResourceMeta, String> {
    let url = Url::parse(server_url).map_err(|e| format!("invalid URL {server_url}: {e}"))?;
    let mut candidates: Vec<String> = hint.map(|h| vec![h.to_string()]).unwrap_or_default();
    candidates.extend(well_known(&url, "oauth-protected-resource"));
    let mut last = String::from("no protected resource metadata");
    for candidate in candidates {
        match fetch_json(http, &candidate).await {
            Ok(value) => {
                let meta: ResourceMeta = serde_json::from_value(value).map_err(|e| format!("{candidate}: {e}"))?;
                if meta.authorization_servers.is_empty() {
                    return Err(format!("{candidate} names no authorization server"));
                }
                return Ok(meta);
            }
            Err(err) => last = err,
        }
    }
    Err(last)
}

/// The metadata of the authorization server `issuer`, trying RFC 8414 first and OpenID
/// Connect discovery after.
pub async fn auth_server_metadata(http: &reqwest::Client, issuer: &str) -> Result<AuthServerMeta, String> {
    let url = Url::parse(issuer).map_err(|e| format!("invalid issuer {issuer}: {e}"))?;
    let mut candidates = well_known(&url, "oauth-authorization-server");
    candidates.truncate(1);
    candidates.extend(well_known(&url, "openid-configuration").into_iter().take(1));
    candidates.push(format!("{}/.well-known/openid-configuration", issuer.trim_end_matches('/')));
    if url.path().trim_end_matches('/').is_empty() {
        candidates.dedup();
    }
    let mut last = String::from("no authorization server metadata");
    for candidate in candidates {
        match fetch_json(http, &candidate).await {
            Ok(value) => return serde_json::from_value(value).map_err(|e| format!("{candidate}: {e}")),
            Err(err) => last = err,
        }
    }
    Err(last)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn challenges_with_and_without_quotes() {
        let c = parse_challenge(r#"Bearer error="invalid_request", error_description="No access token", resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mcp/""#);
        assert_eq!(c.resource_metadata.as_deref(), Some("https://api.example.com/.well-known/oauth-protected-resource/mcp/"));
        assert_eq!(c.error.as_deref(), Some("invalid_request"));
        let c = parse_challenge("Bearer scope=read, resource_metadata=https://x.test/m");
        assert_eq!((c.scope.as_deref(), c.resource_metadata.as_deref()), (Some("read"), Some("https://x.test/m")));
        assert_eq!(parse_challenge("Bearer"), Challenge::default());
    }

    #[test]
    fn well_known_addresses_keep_the_path() {
        let url = Url::parse("https://mcp.example.com/v1/mcp?x=1").unwrap();
        assert_eq!(
            well_known(&url, "oauth-protected-resource"),
            vec!["https://mcp.example.com/.well-known/oauth-protected-resource/v1/mcp", "https://mcp.example.com/.well-known/oauth-protected-resource"]
        );
        let root = Url::parse("https://hermesh.io").unwrap();
        assert_eq!(well_known(&root, "oauth-authorization-server"), vec!["https://hermesh.io/.well-known/oauth-authorization-server"]);
    }
}
