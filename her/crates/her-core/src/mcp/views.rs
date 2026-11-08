//! MCP servers as the 自定义 page lists them (docs/desktop/customize-design.md §8.1): where
//! each is defined, whether its credentials are saved, and its state, from an open session
//! first, then from the cache, without starting anything.

use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;

use her_protocol::McpServerConfig;
use her_protocol::McpServerState;
use her_protocol::customize::ConfigLayer;
use her_protocol::customize::McpAuthKind;
use her_protocol::customize::McpAuthState;
use her_protocol::customize::McpListResult;
use her_protocol::customize::McpServerStatusView;
use her_protocol::customize::McpServerView;
use her_protocol::customize::McpStatusSource;
use her_protocol::customize::McpStatusState;
use her_protocol::customize::McpToolSummary;
use her_protocol::customize::McpToolsResult;
use her_protocol::customize::McpToolsSource;
use her_protocol::customize::McpTransportKind;

use super::cache;
use crate::config;
use crate::config_edit;
use crate::paths::HerHome;
use crate::runtime::Workspace;

/// One server as the open sessions of a workspace see it.
#[derive(Clone, Debug)]
pub struct Live {
    pub state: McpServerState,
    pub tools: u32,
    pub error: Option<String>,
    /// Sessions connected to it.
    pub sessions: u32,
}

/// Where the workspace's servers are recorded: the project root, or the directory itself.
pub fn workspace_dir(workspace: &Workspace) -> PathBuf {
    workspace.project_root.clone().unwrap_or_else(|| workspace.cwd.clone())
}

/// `mcp/list`
pub fn list(workspace: &Workspace, home: &HerHome, live: &HashMap<String, Live>) -> McpListResult {
    let mut servers: Vec<McpServerView> = workspace.mcp_servers.iter().map(|s| view(workspace, home, s, live.get(&s.name))).collect();
    servers.extend(untrusted(workspace, home));
    let warnings = workspace.warnings.iter().filter(|w| w.contains("MCP") || w.contains("mcp")).cloned().collect();
    McpListResult { servers, warnings }
}

/// One server's view.
pub fn view(workspace: &Workspace, home: &HerHome, server: &McpServerConfig, live: Option<&Live>) -> McpServerView {
    let key = |field: &str| config::join_key(&config::join_key("mcp_servers", &server.name), field);
    let origin = |field: &str| workspace.origins.get(&key(field)).copied();
    let sources = workspace.mcp_sources.get(&server.name);
    let layer = origin("command")
        .or_else(|| origin("url"))
        .unwrap_or_else(|| match sources.map(|s| s.iter().any(|l| l == ".mcp.json")) {
            Some(true) => ConfigLayer::ProjectJson,
            _ => ConfigLayer::Flag,
        });
    let dir = workspace_dir(workspace);
    let file = match layer {
        ConfigLayer::User => Some(home.config_file()),
        ConfigLayer::Project => Some(dir.join(".her").join("config.toml")),
        ConfigLayer::UserProject => workspace.user_project_file.clone(),
        ConfigLayer::ProjectJson => Some(dir.join(".mcp.json")),
        ConfigLayer::Flag => None,
    };
    let enabled = server.is_enabled() && crate::connectors::authorized(home.root(), server);
    let credentials = config_edit::server_credentials(server);
    let (auth, mut auth_state) = if credentials.is_empty() {
        (McpAuthKind::None, None)
    } else {
        let saved = credentials.iter().all(|name| her_llm::credentials::get(name).is_some());
        (McpAuthKind::Credential, Some(if saved { McpAuthState::Saved } else { McpAuthState::Missing }))
    };
    let mut status = status(home, workspace, server, live);
    let mut auth = auth;
    let managed = crate::connectors::for_server(home.root(), server);
    if let Some(m) = &managed {
        auth = McpAuthKind::Oauth;
        let signed_in = crate::connectors::signed_in(home.root(), m);
        auth_state = Some(if signed_in { McpAuthState::LoggedIn } else { McpAuthState::NeedsLogin });
        if !signed_in && status.state != McpStatusState::Untrusted { status.state = McpStatusState::NeedsLogin; }
    }
    if server.uses_oauth() {
        // Signs in with OAuth: signed in when tokens for this address are saved (mcp-design §13.8).
        auth = McpAuthKind::Oauth;
        let signed_in = server.url.as_deref().is_some_and(|url| her_mcp::oauth::signed_in(&server.name, url));
        auth_state = Some(if signed_in { McpAuthState::LoggedIn } else { McpAuthState::NeedsLogin });
        if !signed_in && status.state != McpStatusState::Untrusted {
            status.state = McpStatusState::NeedsLogin;
        }
    } else if status.state == McpStatusState::Failed && server.url.is_some() && status.error.as_deref().is_some_and(wants_login) {
        // A remote server that refuses without a login wants OAuth (§8.2).
        status.state = McpStatusState::NeedsLogin;
        auth = McpAuthKind::Oauth;
        auth_state = Some(McpAuthState::NeedsLogin);
    }
    McpServerView {
        auth_partial: managed.as_ref().filter(|m| crate::connectors::partial_permissions(home.root(), m)).map(|_| true),
        connector: managed.map(|m| m.id),
        name: server.name.clone(),
        layer,
        file,
        editable: layer.writable(),
        enabled,
        disabled_by: (!enabled).then(|| origin("enabled")).flatten(),
        transport: transport_kind(server),
        summary: summary(server),
        description: server.description.clone(),
        auth,
        auth_state,
        credentials,
        status,
        identity: her_mcp::identity::identity(server),
        icon: icon(home, server),
    }
}

/// The logo kept for the service, if one was found.
fn icon(home: &HerHome, server: &McpServerConfig) -> Option<String> {
    let identity = her_mcp::identity::identity(server)?;
    her_mcp::icon::load(&home.mcp_icons_dir(), &identity)?.data_uri()
}

fn transport_kind(server: &McpServerConfig) -> McpTransportKind {
    match (&server.url, server.remote_transport()) {
        (None, _) => McpTransportKind::Stdio,
        (Some(_), her_protocol::McpRemoteTransport::Sse) => McpTransportKind::Sse,
        (Some(_), _) => McpTransportKind::Http,
    }
}

fn wants_login(error: &str) -> bool {
    error.contains("401") || error.to_ascii_lowercase().contains("unauthorized")
}

fn status(home: &HerHome, workspace: &Workspace, server: &McpServerConfig, live: Option<&Live>) -> McpServerStatusView {
    let cached = cache::read(&home.mcp_cache_dir(), server, &workspace_dir(workspace));
    if let Some(live) = live.filter(|l| l.state != McpServerState::Starting && l.state != McpServerState::Closed) {
        let ok = live.state == McpServerState::Ready;
        return McpServerStatusView {
            state: match live.state {
                McpServerState::Ready => McpStatusState::Ok,
                McpServerState::NeedsLogin => McpStatusState::NeedsLogin,
                _ => McpStatusState::Failed,
            },
            source: Some(McpStatusSource::Session),
            sessions: Some(live.sessions),
            tools: ok.then_some(live.tools),
            error: live.error.clone(),
            checked_at: cached.as_ref().map(|c| c.checked_at.clone()),
            elapsed_ms: None,
        };
    }
    match cached {
        Some(entry) => McpServerStatusView {
            state: if entry.ok() { McpStatusState::Ok } else { McpStatusState::Failed },
            source: Some(McpStatusSource::Cache),
            sessions: None,
            tools: entry.ok().then_some(entry.tools.len() as u32),
            error: entry.error,
            checked_at: Some(entry.checked_at),
            elapsed_ms: entry.elapsed_ms,
        },
        None => McpServerStatusView {
            state: McpStatusState::Unknown,
            source: None,
            sessions: None,
            tools: None,
            error: None,
            checked_at: None,
            elapsed_ms: None,
        },
    }
}

/// `mcp/tools`: the server's tools from its last connection.
pub fn tools(workspace: &Workspace, home: &HerHome, server: &McpServerConfig, live: Option<&Live>) -> McpToolsResult {
    match cache::read(&home.mcp_cache_dir(), server, &workspace_dir(workspace)) {
        Some(entry) if entry.ok() => McpToolsResult {
            tools: entry.tools,
            source: if live.is_some_and(|l| l.state == McpServerState::Ready) { McpToolsSource::Session } else { McpToolsSource::Cache },
            checked_at: Some(entry.checked_at),
        },
        _ => McpToolsResult { tools: Vec::<McpToolSummary>::new(), source: McpToolsSource::None, checked_at: None },
    }
}

/// The command line, or the URL without its query.
fn summary(server: &McpServerConfig) -> String {
    if let Some(url) = &server.url {
        return url.split('?').next().unwrap_or(url).to_string();
    }
    let mut words = vec![server.command.clone().unwrap_or_default()];
    words.extend(server.args.iter().map(|a| if a.contains(' ') { format!("\"{a}\"") } else { a.clone() }));
    words.join(" ")
}

/// Servers of an untrusted project's configuration: listed, never started.
fn untrusted(workspace: &Workspace, home: &HerHome) -> Vec<McpServerView> {
    let Some(root) = &workspace.project_root else { return Vec::new() };
    if workspace.trusted {
        return Vec::new();
    }
    let path = root.join(".her").join("config.toml");
    let Some(servers) = std::fs::read_to_string(&path)
        .ok()
        .and_then(|text| text.parse::<toml::Table>().ok())
        .and_then(|table| table.get("mcp_servers").and_then(|s| s.as_table()).cloned())
    else {
        return Vec::new();
    };
    servers
        .into_iter()
        .filter(|(name, _)| !workspace.mcp_servers.iter().any(|s| &s.name == name))
        .filter_map(|(name, value)| {
            let mut server: McpServerConfig = value.try_into().ok()?;
            server.name = name;
            Some(McpServerView {
                connector: None,
                name: server.name.clone(),
                layer: ConfigLayer::Project,
                file: Some(path.clone()),
                editable: false,
                enabled: server.is_enabled(),
                disabled_by: None,
                transport: transport_kind(&server),
                summary: summary(&server),
                description: server.description.clone(),
                auth: McpAuthKind::None,
                auth_state: None,
                auth_partial: None,
                credentials: Vec::new(),
                status: McpServerStatusView {
                    state: McpStatusState::Untrusted,
                    source: None,
                    sessions: None,
                    tools: None,
                    error: None,
                    checked_at: None,
                    elapsed_ms: None,
                },
                identity: her_mcp::identity::identity(&server),
                icon: icon(home, &server),
            })
        })
        .collect()
}

/// The server named `name` in the workspace, enabled for a check.
pub fn for_check(workspace: &Workspace, name: &str) -> Option<McpServerConfig> {
    let mut server = workspace.mcp_servers.iter().find(|s| s.name == name)?.clone();
    server.enabled = None;
    Some(server)
}

/// Whether `path` is inside `dir` (both as given).
pub fn within(path: &Path, dir: &Path) -> bool {
    path.starts_with(dir)
}
