//! MCP servers as the kernel receives them, and their status.
//!
//! Configuration files, the native protocol and the command line all produce
//! [`McpServerConfig`]; the kernel never reads any of those sources itself.

use schemars::JsonSchema;
use serde::Deserialize;
use serde::Serialize;
use std::collections::BTreeMap;
use std::fmt;
use std::path::PathBuf;

/// One MCP server: a local command (stdio) or a URL (Streamable HTTP).
///
/// String values may contain `${VAR}` and `${VAR:-default}`; they are expanded from her's
/// environment when the server connects, so a missing variable fails only this server.
#[derive(Clone, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpServerConfig {
    /// A curated CLI connector managed by her. Its command is the internal MCP adapter.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub connector: Option<String>,
    /// The key of `[mcp_servers.<name>]`; 1-32 letters, digits, `_` or `-`.
    #[serde(default)]
    pub name: String,
    /// Local server: the program to start. Exactly one of `command` and `url` is set.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub command: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub args: Vec<String>,
    /// Variables set for the program, on top of the safe base set.
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub env: BTreeMap<String, String>,
    /// Variables passed through from her's environment, on top of the safe base set.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub env_vars: Vec<String>,
    /// Working directory; relative to the project root. Default: the session's directory.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cwd: Option<PathBuf>,
    /// Remote server: the Streamable HTTP endpoint.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub url: Option<String>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub headers: BTreeMap<String, String>,
    /// Sends `Authorization: Bearer $VAR`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bearer_token_env_var: Option<String>,
    /// Remote server: Streamable HTTP, or the older HTTP+SSE. Absent: SSE when the URL ends
    /// in `/sse`, else Streamable HTTP.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub transport: Option<McpRemoteTransport>,
    /// Remote server: `oauth` signs in with OAuth; the token is kept in the credential store.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub auth: Option<McpAuthMode>,
    /// Remote server: the user's own OAuth client, used before any other identity.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub oauth: Option<McpOAuthClient>,
    /// `false` removes the server; only meaningful in configuration files.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub enabled: Option<bool>,
    /// How its tools reach the model. Default: `[mcp] exposure`, which defaults to deferred.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exposure: Option<McpExposure>,
    /// One line for the server catalog the model sees.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// Tools of a deferred server that go straight into the tool list (names on the server).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub direct_tools: Vec<String>,
    /// Only these tools (names on the server). Absent: all of them.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub enabled_tools: Option<Vec<String>>,
    /// Removes these tools, after `enabled_tools`.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub disabled_tools: Vec<String>,
    /// Longest connection attempt: start, handshake and tool list. Default 30.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub startup_timeout_secs: Option<u64>,
    /// Longest call without progress. Default 300.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_timeout_secs: Option<u64>,
    /// Every tool of this server may run in parallel with other read-only tools.
    #[serde(default, skip_serializing_if = "is_false")]
    pub parallel_tools: bool,
    /// The first request always waits for this server, and a task fails without it.
    #[serde(default, skip_serializing_if = "is_false")]
    pub required: bool,
    /// `never`: its tools run without asking in every permission mode. Taken only from the
    /// user's own configuration file.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub approval: Option<McpApproval>,
    /// A project defines it, so what it says about its own tools is not taken at its word. Worked
    /// out when the configuration is read; no file sets it.
    #[serde(skip)]
    pub project_defined: bool,
}

/// How much a server's tools are trusted to run unasked.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpApproval {
    Never,
}

impl McpServerConfig {
    /// Whether a tool of this server runs without asking in the modes that ask. `read_only` is
    /// the tool's `readOnlyHint`, which is the server's own claim.
    pub fn runs_unasked(&self, read_only: bool) -> bool {
        self.approval == Some(McpApproval::Never) || (read_only && !self.project_defined)
    }
}

fn is_false(value: &bool) -> bool {
    !*value
}

/// Values that may hold secrets are never printed.
impl fmt::Debug for McpServerConfig {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let mut s = f.debug_struct("McpServerConfig");
        s.field("name", &self.name);
        if let Some(command) = &self.command {
            s.field("command", command).field("args", &self.args.len());
        }
        if let Some(url) = &self.url {
            s.field("url", &url_host(url));
        }
        s.field("env", &self.env.keys().collect::<Vec<_>>())
            .field("headers", &self.headers.keys().collect::<Vec<_>>())
            .field("exposure", &self.exposure)
            .finish_non_exhaustive()
    }
}

/// The host part of a URL, for logs: no path, query or credentials.
pub fn url_host(url: &str) -> &str {
    let rest = url.split_once("://").map_or(url, |(_, rest)| rest);
    let host = rest.split(['/', '?', '#']).next().unwrap_or(rest);
    host.rsplit_once('@').map_or(host, |(_, host)| host)
}

/// Where the transport fields point.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum McpTransportKind {
    Stdio,
    Http,
}

/// How a remote server is reached.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpRemoteTransport {
    StreamableHttp,
    /// The 2024-11-05 transport: a GET event stream names the address to POST messages to.
    Sse,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpAuthMode {
    Oauth,
}

/// An OAuth client the user registered with the provider.
#[derive(Clone, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpOAuthClient {
    pub client_id: String,
    /// Only for providers that require one; best written as a `${credential:…}` reference.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub client_secret: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub scopes: Vec<String>,
}

impl fmt::Debug for McpOAuthClient {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("McpOAuthClient").field("client_id", &self.client_id).field("scopes", &self.scopes).finish_non_exhaustive()
    }
}

impl McpServerConfig {
    /// The transport, or why the fields are inconsistent.
    pub fn transport(&self) -> Result<McpTransportKind, String> {
        match (&self.command, &self.url) {
            (Some(_), None) => {
                if self.bearer_token_env_var.is_some()
                    || !self.headers.is_empty()
                    || self.transport.is_some()
                    || self.auth.is_some()
                    || self.oauth.is_some()
                {
                    return Err("`headers`, `bearer_token_env_var`, `transport`, `auth` and `oauth` only apply to servers with a `url`".into());
                }
                Ok(McpTransportKind::Stdio)
            }
            (None, Some(_)) => {
                if !self.args.is_empty() || !self.env.is_empty() || !self.env_vars.is_empty() || self.cwd.is_some() {
                    return Err("`args`, `env`, `env_vars` and `cwd` only apply to servers with a `command`".into());
                }
                Ok(McpTransportKind::Http)
            }
            (Some(_), Some(_)) => Err("set either `command` or `url`, not both".into()),
            (None, None) => Err("set `command` (a local server) or `url` (a remote server)".into()),
        }
    }

    /// Checks the name and the transport fields.
    pub fn validate(&self) -> Result<(), String> {
        if !valid_server_name(&self.name) {
            return Err(format!(
                "invalid MCP server name '{}': use 1-32 letters, digits, '_' or '-'",
                self.name
            ));
        }
        self.transport().map(|_| ())
    }

    pub fn is_enabled(&self) -> bool {
        self.enabled != Some(false)
    }

    /// The transport of a remote server: as configured, or SSE for a URL ending in `/sse`.
    pub fn remote_transport(&self) -> McpRemoteTransport {
        self.transport.unwrap_or_else(|| match &self.url {
            Some(url) if url.split(['?', '#']).next().unwrap_or(url).trim_end_matches('/').ends_with("/sse") => {
                McpRemoteTransport::Sse
            }
            _ => McpRemoteTransport::StreamableHttp,
        })
    }

    pub fn uses_oauth(&self) -> bool {
        self.auth == Some(McpAuthMode::Oauth)
    }
}

pub fn valid_server_name(name: &str) -> bool {
    !name.is_empty() && name.len() <= 32 && name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '-')
}

/// How a server's tools reach the model.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpExposure {
    /// Not in the tool list: the model finds them with `tool_search` and runs them with
    /// `call_tool`; only the server's catalog line is always in the prompt.
    #[default]
    Deferred,
    /// Every tool goes into the tool list.
    Direct,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpServerState {
    /// Connecting: starting the process, the handshake, the tool list.
    Starting,
    Ready,
    /// Could not connect, or the connection was lost. Calls try to reconnect.
    Failed,
    /// Signs in with OAuth, and there is no usable token: someone has to sign in.
    NeedsLogin,
    /// Shut down with the session.
    Closed,
}

/// The latest status of one server, as kept in a session snapshot.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpServerInfo {
    pub server: String,
    pub state: McpServerState,
    /// Tools offered after filtering, when ready.
    #[serde(default)]
    pub tools: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn transport_fields_must_match() {
        let stdio = McpServerConfig {
            name: "a".into(),
            command: Some("x".into()),
            ..Default::default()
        };
        assert_eq!(stdio.transport(), Ok(McpTransportKind::Stdio));
        let both = McpServerConfig {
            url: Some("http://h/mcp".into()),
            ..stdio.clone()
        };
        assert!(both.transport().is_err());
        let http_with_args = McpServerConfig {
            name: "b".into(),
            url: Some("http://h/mcp".into()),
            args: vec!["-x".into()],
            ..Default::default()
        };
        assert!(http_with_args.transport().unwrap_err().contains("args"));
        assert!(
            McpServerConfig::default().validate().is_err(),
            "no name, no transport"
        );
    }

    #[test]
    fn debug_hides_secrets() {
        let config = McpServerConfig {
            name: "docs".into(),
            url: Some("https://user:pw@mcp.example.com/mcp?key=secret".into()),
            headers: BTreeMap::from([("Authorization".to_string(), "Bearer hunter2".to_string())]),
            ..Default::default()
        };
        let text = format!("{config:?}");
        assert!(text.contains("mcp.example.com"), "{text}");
        assert!(!text.contains("hunter2") && !text.contains("secret") && !text.contains("pw"), "{text}");
    }

    #[test]
    fn config_round_trips_with_snake_case_fields() {
        let json = serde_json::json!({
            "name": "fs", "command": "npx", "args": ["-y", "server"], "exposure": "direct",
            "tool_timeout_secs": 60, "parallel_tools": true
        });
        let config: McpServerConfig = serde_json::from_value(json.clone()).unwrap();
        assert_eq!(config.exposure, Some(McpExposure::Direct));
        assert!(config.parallel_tools);
        assert_eq!(serde_json::to_value(&config).unwrap(), json);
    }
}
