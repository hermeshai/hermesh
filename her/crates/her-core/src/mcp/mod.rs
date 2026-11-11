//! The MCP servers of an agent tree: started with the main session, shared by its sub-agents.
//!
//! Tools of deferred servers (the default) stay out of the tool list. The model sees one
//! catalog line per server, finds tools with `tool_search` and runs them with `call_tool`,
//! so the tool list and the system prompt never change during a session. Tools of direct
//! servers (and a deferred server's `direct_tools`) go into the tool list.

pub mod cache;
pub mod views;
pub(crate) mod tools;

use her_mcp::McpServer;
use her_mcp::McpTool;
use her_mcp::ServerContext;
use her_mcp::ServerStatus;
use her_mcp::ToolInfo;
use her_mcp::naming;
use her_mcp::search::SearchDoc;
use her_mcp::search::SearchIndex;
use her_protocol::EventMsg;
use her_protocol::McpExposure;
use her_protocol::McpServerConfig;
use her_protocol::McpServerState;
use her_tools::Tool;
use parking_lot::Mutex;
use serde_json::Value;
use std::collections::BTreeMap;
use std::collections::HashSet;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use std::time::Duration;
use std::time::Instant;
use tokio_util::sync::CancellationToken;

use crate::config::McpSettings;
use crate::emitter::Emitter;

/// Longest catalog section, in bytes.
const CATALOG_LIMIT: usize = 4096;
/// Longest one-line server description in the catalog, in characters.
const DESCRIPTION_LIMIT: usize = 250;
/// Longest server instructions shown to the model, in bytes.
pub(crate) const INSTRUCTIONS_LIMIT: usize = 16 * 1024;

/// her's OAuth client metadata document (mcp-design §13.8.3). The address is her's client
/// id, so it never changes; the file's source is `site/oauth/client.json`.
pub const CLIENT_METADATA_URL: Option<&str> = Some("https://hermesh.io/oauth/client.json");

const CATALOG_INTRO: &str = "Tools of these groups are not in your tool list. Find them with tool_search, then run them with call_tool.";

/// One offered tool, deferred or direct.
pub(crate) struct Entry {
    pub(crate) qualified: String,
    pub(crate) server: Arc<McpServer>,
    pub(crate) info: ToolInfo,
    pub(crate) parameters: Value,
    pub(crate) deferred: bool,
}

/// Every server's tools at one generation.
pub(crate) struct Catalog {
    pub(crate) generation: u64,
    pub(crate) entries: Vec<Entry>,
    /// BM25 over the deferred entries; `index_entries[i]` is the entry of document `i`.
    pub(crate) index: SearchIndex,
    pub(crate) index_entries: Vec<usize>,
    /// Tool-list tools: direct servers' tools and deferred servers' `direct_tools`.
    pub(crate) direct: Vec<Arc<dyn Tool>>,
}

impl Catalog {
    pub(crate) fn find(&self, qualified: &str) -> Option<&Entry> {
        self.entries.iter().find(|e| e.qualified == qualified)
    }
}

pub(crate) struct McpManager {
    /// Only ever added to, so an index into one list stays good in the next.
    servers: Mutex<Arc<Vec<Arc<McpServer>>>>,
    exposure: Mutex<Vec<McpExposure>>,
    /// What a server added later starts with (`add`).
    ctx: ServerContext,
    default_exposure: McpExposure,
    /// Servers added after the start: each changes the generation.
    added: AtomicU64,
    /// Every server, for the status callback, which must not keep them.
    made: Arc<Mutex<Vec<std::sync::Weak<McpServer>>>>,
    startup_wait: Duration,
    started: Instant,
    /// The main session's emitter, bound once it exists.
    events: Arc<Mutex<Option<Arc<Emitter>>>>,
    catalog: Mutex<Option<Arc<Catalog>>>,
    /// Tools already reported as not offered, so each is reported once.
    reported: Mutex<HashSet<String>>,
}

impl McpManager {
    /// `None` when there is nothing to start, unless the manager `grows`: Her's conversation
    /// takes up the connectors the owner connects later (`add`). `configs` are validated
    /// already; disabled ones are left out.
    pub(crate) fn new(
        configs: Vec<McpServerConfig>,
        settings: &McpSettings,
        http: Option<reqwest::Client>,
        roots: Vec<PathBuf>,
        cwd: PathBuf,
        project_root: Option<PathBuf>,
        grows: bool,
    ) -> Option<Arc<McpManager>> {
        let mut configs: Vec<McpServerConfig> = configs.into_iter().filter(|s| s.is_enabled() && (s.connector.is_none() || settings.her_home.as_deref().is_some_and(|home| crate::connectors::authorized(home, s)))).collect();
        if (configs.is_empty() && !grows) || !settings.enabled {
            return None;
        }
        let events: Arc<Mutex<Option<Arc<Emitter>>>> = Arc::new(Mutex::new(None));
        let sink = events.clone();
        // Ready or failed: the result goes to the cache the 自定义 page reads.
        let cache = settings.cache_dir.clone().map(|dir| (dir, project_root.clone().unwrap_or_else(|| cwd.clone())));
        // Weak: each server holds this callback, and must still go away with the session.
        let made: Arc<Mutex<Vec<std::sync::Weak<McpServer>>>> = Arc::default();
        let made_for_status = made.clone();
        let on_status = Arc::new(move |server: &str, status: &ServerStatus| {
            if let Some(emitter) = sink.lock().as_ref() {
                emitter.emit(EventMsg::McpServerStatus {
                    server: server.to_string(),
                    state: status.state,
                    tools: status.tools,
                    error: status.error.clone(),
                    elapsed_ms: status.elapsed_ms,
                });
            }
            if let Some((dir, workspace)) = &cache
                && matches!(status.state, McpServerState::Ready | McpServerState::Failed | McpServerState::NeedsLogin)
                && let Some(found) = made_for_status.lock().iter().filter_map(std::sync::Weak::upgrade).find(|s| s.name() == server)
            {
                let (dir, workspace, status) = (dir.clone(), workspace.clone(), status.clone());
                let write = move || record(&dir, &found, &workspace, &status, true);
                match tokio::runtime::Handle::try_current() {
                    Ok(handle) => drop(handle.spawn_blocking(write)),
                    Err(_) => write(),
                }
            }
        });
        let ctx = ServerContext {
            http,
            roots,
            cwd,
            project_root,
            on_status: Some(on_status),
            oauth: settings.her_home.clone().map(|her_home| her_mcp::oauth::OAuthPaths { her_home }),
        };
        configs.sort_by(|a, b| a.name.cmp(&b.name));
        let exposure = configs.iter().map(|c| c.exposure.unwrap_or(settings.exposure)).collect();
        let servers: Vec<Arc<McpServer>> = configs.into_iter().map(|c| McpServer::new(c, ctx.clone())).collect();
        *made.lock() = servers.iter().map(Arc::downgrade).collect();
        Some(Arc::new(McpManager {
            servers: Mutex::new(Arc::new(servers)),
            exposure: Mutex::new(exposure),
            ctx,
            default_exposure: settings.exposure,
            added: AtomicU64::new(0),
            made,
            startup_wait: settings.startup_wait,
            started: Instant::now(),
            events,
            catalog: Mutex::new(None),
            reported: Mutex::new(HashSet::new()),
        }))
    }

    /// Sends status events to `emitter` from now on, then starts every server.
    pub(crate) fn start(&self, emitter: Arc<Emitter>) {
        *self.events.lock() = Some(emitter);
        for server in self.servers().iter() {
            server.connect();
        }
    }

    /// Adds a server configured after the session started, and connects it once the session
    /// has: the owner just connected it on a card (docs/her-secure-cards.md §6). A name the
    /// session has already is left alone. Returns the server under that name.
    pub(crate) fn add(&self, config: McpServerConfig) -> Arc<McpServer> {
        let server = {
            let mut servers = self.servers.lock();
            if let Some(had) = servers.iter().find(|s| s.name() == config.name) {
                return had.clone();
            }
            self.exposure.lock().push(config.exposure.unwrap_or(self.default_exposure));
            let server = McpServer::new(config, self.ctx.clone());
            self.made.lock().push(Arc::downgrade(&server));
            let mut list = (**servers).clone();
            list.push(server.clone());
            *servers = Arc::new(list);
            server
        };
        self.added.fetch_add(1, Ordering::AcqRel);
        if self.events.lock().is_some() {
            server.connect();
        }
        server
    }

    /// How long a turn waits for a server still connecting.
    pub(crate) fn startup_wait(&self) -> Duration {
        self.startup_wait
    }

    pub(crate) fn servers(&self) -> Arc<Vec<Arc<McpServer>>> {
        self.servers.lock().clone()
    }

    pub(crate) fn is_deferred(&self, i: usize) -> bool {
        self.exposure.lock()[i] == McpExposure::Deferred
    }

    /// Whether `tool_search` and `call_tool` belong in the tool list.
    pub(crate) fn has_deferred(&self) -> bool {
        self.exposure.lock().iter().any(|e| *e == McpExposure::Deferred)
    }

    /// Servers whose tools go into the tool list, so the first request waits for them.
    fn waits_for(&self, i: usize) -> bool {
        !self.is_deferred(i) || !self.servers()[i].config().direct_tools.is_empty()
    }

    /// Changes with every change of any server's tools or instructions, and with every server
    /// added.
    pub(crate) fn generation(&self) -> u64 {
        self.servers().iter().map(|s| s.generation()).sum::<u64>() + self.added.load(Ordering::Acquire)
    }

    /// Before the first request: waits for the servers whose tools go into the tool list,
    /// at most `startup_wait`, and for required servers until they are ready or failed.
    /// Returns the required servers that failed, with their errors.
    pub(crate) async fn settle(&self, cancel: &CancellationToken) -> Vec<(String, String)> {
        let deadline = self.started + self.startup_wait;
        let servers = self.servers();
        let waits = servers.iter().enumerate().filter_map(|(i, server)| {
            let required = server.config().required;
            if !(self.waits_for(i) || required) {
                return None;
            }
            let until = if required {
                Instant::now() + Duration::from_secs(server.config().startup_timeout_secs.unwrap_or(30) + 1)
            } else {
                deadline
            };
            Some(server.wait_settled(until))
        });
        tokio::select! {
            _ = futures::future::join_all(waits) => {}
            _ = cancel.cancelled() => return Vec::new(),
        }
        servers
            .iter()
            .filter(|s| s.config().required && s.status().state != McpServerState::Ready)
            .map(|s| {
                let status = s.status();
                let error = status.error.unwrap_or_else(|| "it is not ready".to_string());
                (s.name().to_string(), error)
            })
            .collect()
    }

    /// Waits a little for deferred servers that are still connecting, before a search.
    pub(crate) async fn wait_for_deferred(&self, cancel: &CancellationToken) {
        let deadline = Instant::now() + self.startup_wait;
        let servers = self.servers();
        let waits = servers
            .iter()
            .filter(|s| s.status().state == McpServerState::Starting)
            .map(|s| s.wait_settled(deadline));
        tokio::select! {
            _ = futures::future::join_all(waits) => {}
            _ = cancel.cancelled() => {}
        }
    }

    /// Whether a server has yet to say what its tools are.
    pub(crate) fn connecting(&self) -> bool {
        self.servers().iter().any(|s| s.status().state == McpServerState::Starting)
    }

    /// One line per server that is not usable now, for search results and warnings.
    pub(crate) fn unavailable(&self) -> Vec<String> {
        self.servers()
            .iter()
            .filter_map(|s| {
                let status = s.status();
                match status.state {
                    McpServerState::Ready => None,
                    McpServerState::Starting => Some(format!("{} (still connecting)", s.name())),
                    _ => Some(format!(
                        "{} ({})",
                        s.name(),
                        status.error.unwrap_or_else(|| "not connected".to_string())
                    )),
                }
            })
            .collect()
    }

    /// The tools of every server at the current generation; rebuilt only when it changes.
    pub(crate) fn catalog(&self) -> Arc<Catalog> {
        let generation = self.generation();
        if let Some(catalog) = self.catalog.lock().as_ref()
            && catalog.generation == generation
        {
            return catalog.clone();
        }
        let catalog = Arc::new(self.build_catalog(generation));
        *self.catalog.lock() = Some(catalog.clone());
        catalog
    }

    fn build_catalog(&self, generation: u64) -> Catalog {
        let mut entries = Vec::new();
        let mut direct: Vec<Arc<dyn Tool>> = Vec::new();
        let mut docs = Vec::new();
        let mut index_entries = Vec::new();
        for (i, server) in self.servers().iter().enumerate() {
            let mut tools: Vec<ToolInfo> = server.tools().iter().cloned().collect();
            tools.sort_by(|a, b| a.name.cmp(&b.name));
            let raw: Vec<&str> = tools.iter().map(|t| t.name.as_str()).collect();
            let names = naming::qualify(server.name(), &raw);
            for (info, qualified) in tools.iter().zip(names) {
                let parameters = match her_mcp::schema::normalize(&info.input_schema) {
                    Ok(parameters) => parameters,
                    Err(reason) => {
                        self.report(&qualified, &format!("MCP tool '{}' of server '{}' is not offered: {reason}", info.name, server.name()));
                        continue;
                    }
                };
                let deferred = self.is_deferred(i) && !server.config().direct_tools.contains(&info.name);
                if deferred {
                    index_entries.push(entries.len());
                    docs.push(SearchDoc {
                        qualified: qualified.clone(),
                        server: server.name().to_string(),
                        tool: info.name.clone(),
                        title: info.title.clone(),
                        description: info.description.clone().unwrap_or_default(),
                        params: parameter_docs(&parameters),
                    });
                } else {
                    match McpTool::new(server.clone(), info, qualified.clone()) {
                        Ok(tool) => direct.push(Arc::new(tool)),
                        Err(reason) => {
                            self.report(&qualified, &format!("MCP tool '{}' is not offered: {reason}", info.name));
                            continue;
                        }
                    }
                }
                entries.push(Entry {
                    qualified,
                    server: server.clone(),
                    info: info.clone(),
                    parameters,
                    deferred,
                });
            }
        }
        Catalog {
            generation,
            entries,
            index: SearchIndex::new(&docs),
            index_entries,
            direct,
        }
    }

    fn report(&self, key: &str, message: &str) {
        if self.reported.lock().insert(key.to_string()) {
            tracing::warn!("{message}");
            if let Some(emitter) = self.events.lock().as_ref() {
                emitter.emit(EventMsg::Warning {
                    message: message.to_string(),
                    code: None,
                });
            }
        }
    }

    /// The deferred servers for the catalog, with their descriptions. They come from the
    /// configuration alone, so the catalog is the same from the first request on, whatever the
    /// servers' state.
    fn deferred_servers(&self) -> Vec<(String, Option<String>)> {
        self.servers()
            .iter()
            .enumerate()
            .filter(|(i, _)| self.is_deferred(*i))
            .map(|(_, s)| (s.name().to_string(), s.config().description.clone()))
            .collect()
    }

    /// The instructions of direct servers, each in its own part.
    fn direct_instructions(&self) -> Vec<String> {
        let mut parts = Vec::new();
        for (i, server) in self.servers().iter().enumerate() {
            if self.is_deferred(i) {
                continue;
            }
            if let Some(instructions) = server.instructions() {
                parts.push(format!(
                    "<mcp_instructions server=\"{}\">\n{}\n</mcp_instructions>",
                    server.name(),
                    cap(instructions.trim(), INSTRUCTIONS_LIMIT)
                ));
            }
        }
        parts
    }

    pub(crate) async fn shutdown(&self) {
        let servers = self.servers();
        futures::future::join_all(servers.iter().map(|s| s.shutdown())).await;
    }
}

/// Writes a server's last connection to the cache.
fn record(dir: &Path, server: &McpServer, workspace: &Path, status: &ServerStatus, throttle: bool) {
    let tools = server.tools();
    let outcome = cache::Outcome {
        ok: status.state == McpServerState::Ready,
        error: status.error.clone(),
        elapsed_ms: status.elapsed_ms,
        instructions: server.instructions(),
        tools: tools.iter().map(|t| (t.name.as_str(), t.title.as_deref(), t.description.as_deref())).collect(),
    };
    if let Err(err) = cache::write(dir, server.config(), workspace, outcome, throttle) {
        tracing::debug!(server = server.name(), %err, "cannot write the MCP cache");
    }
}

/// What connecting to one server found, for `her mcp list --check` and `mcp/check`.
#[derive(Debug, Clone)]
pub struct ServerCheck {
    pub name: String,
    pub state: McpServerState,
    pub error: Option<String>,
    pub elapsed_ms: Option<u64>,
    /// Qualified names of the tools it offers, after filtering.
    pub tools: Vec<String>,
    /// Every tool the server lists, as the page shows them.
    pub summaries: Vec<her_protocol::customize::McpToolSummary>,
    /// What the server said about itself in the handshake.
    pub info: Option<her_mcp::types::Implementation>,
}

/// Connects to every enabled server in `configs` at once, waits until each is ready or has
/// failed, then disconnects. With `cache_dir`, each result is written to the cache.
pub async fn check_servers(
    configs: Vec<McpServerConfig>,
    http: Option<reqwest::Client>,
    roots: Vec<PathBuf>,
    cwd: PathBuf,
    project_root: Option<PathBuf>,
    cache_dir: Option<PathBuf>,
    her_home: Option<PathBuf>,
) -> Vec<ServerCheck> {
    let workspace = project_root.clone().unwrap_or_else(|| cwd.clone());
    let settings = McpSettings {
        enabled: true,
        project_json: false,
        startup_wait: Duration::ZERO,
        exposure: McpExposure::Deferred,
        cache_dir: None,
        her_home,
        client_metadata_url: None,
    };
    let Some(manager) = McpManager::new(configs, &settings, http, roots, cwd, project_root, false) else {
        return Vec::new();
    };
    let servers = manager.servers();
    for server in servers.iter() {
        server.connect();
    }
    let waits = servers.iter().map(|server| {
        let limit = Duration::from_secs(server.config().startup_timeout_secs.unwrap_or(30) + 1);
        server.wait_settled(Instant::now() + limit)
    });
    futures::future::join_all(waits).await;
    let catalog = manager.catalog();
    let checks = servers
        .iter()
        .map(|server| {
            let status = server.status();
            if let Some(dir) = &cache_dir {
                record(dir, server, &workspace, &status, false);
            }
            ServerCheck {
                summaries: server
                    .tools()
                    .iter()
                    .map(|t| her_protocol::customize::McpToolSummary {
                        name: t.name.clone(),
                        title: t.title.clone(),
                        description: t.description.as_deref().map(|d| cap(d.lines().next().unwrap_or(d), 300)),
                    })
                    .collect(),
                name: server.name().to_string(),
                info: server.info(),
                state: status.state,
                error: status.error,
                elapsed_ms: status.elapsed_ms,
                tools: catalog
                    .entries
                    .iter()
                    .filter(|e| Arc::ptr_eq(&e.server, server))
                    .map(|e| e.qualified.clone())
                    .collect(),
            }
        })
        .collect();
    manager.shutdown().await;
    checks
}

/// Parameter names and descriptions of a schema's top level, for search.
fn parameter_docs(schema: &Value) -> Vec<(String, String)> {
    schema
        .get("properties")
        .and_then(Value::as_object)
        .map(|props| {
            props
                .iter()
                .map(|(name, prop)| {
                    let description = prop.get("description").and_then(Value::as_str).unwrap_or_default();
                    (name.clone(), description.to_string())
                })
                .collect()
        })
        .unwrap_or_default()
}

/// At most `limit` bytes, cut at a character boundary, with `…` when cut.
pub(crate) fn cap(text: &str, limit: usize) -> String {
    if text.len() <= limit {
        return text.to_string();
    }
    let mut end = limit;
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    format!("{}…", &text[..end])
}

/// The prompt's part after the frozen base: the catalog of deferred tools (the built-in groups,
/// then the deferred MCP servers), then the instructions of direct MCP servers.
pub(crate) fn prompt_section(mcp: Option<&Arc<McpManager>>, deferred: &crate::deferred::Deferred) -> Option<String> {
    let mut groups: Vec<(&str, Option<&str>)> = deferred.groups.iter().map(|g| (g.name, Some(g.summary.as_str()))).collect();
    let servers = mcp.map(|mcp| mcp.deferred_servers()).unwrap_or_default();
    groups.extend(servers.iter().map(|(name, description)| (name.as_str(), description.as_deref())));
    let mut parts = Vec::new();
    if !groups.is_empty() {
        parts.push(catalog_section(&groups));
    }
    if let Some(mcp) = mcp {
        parts.extend(mcp.direct_instructions());
    }
    (!parts.is_empty()).then(|| parts.join("\n\n"))
}

/// The `<deferred_tools>` section: every name first; descriptions share the space left.
pub(crate) fn catalog_section(servers: &[(&str, Option<&str>)]) -> String {
    let open = format!("<deferred_tools>\n{CATALOG_INTRO}\n");
    let close = "</deferred_tools>";
    let mut budget = CATALOG_LIMIT.saturating_sub(open.len() + close.len());
    let mut lines: Vec<String> = Vec::new();
    let mut dropped = 0;
    for (name, _) in servers {
        let line = format!("- {name}\n");
        // Room for the closing line about the servers left out.
        if line.len() + 64 > budget && lines.len() < servers.len() {
            dropped = servers.len() - lines.len();
            break;
        }
        budget -= line.len();
        lines.push(line);
    }
    if dropped > 0 {
        lines.push(format!("- … and {dropped} more servers; tool_search finds their tools too.\n"));
    } else {
        // Descriptions share what is left, evenly.
        let with_description = servers.iter().filter(|(_, d)| d.is_some_and(|d| !d.trim().is_empty())).count();
        let share = budget.checked_div(with_description).unwrap_or(0);
        for (line, (_, description)) in lines.iter_mut().zip(servers) {
            let Some(description) = description.map(str::trim).filter(|d| !d.is_empty()) else {
                continue;
            };
            let one_line = description.lines().next().unwrap_or(description);
            let chars: String = one_line.chars().take(DESCRIPTION_LIMIT).collect();
            // ": " before the text, and room for the "…" that `cap` adds when it cuts.
            let room = share.saturating_sub(2 + '…'.len_utf8());
            if room < 8 {
                continue;
            }
            let text = cap(&chars, room);
            line.truncate(line.len() - 1);
            line.push_str(": ");
            line.push_str(&text);
            line.push('\n');
        }
    }
    format!("{open}{}{close}", lines.concat())
}

/// `.mcp.json` (and `--mcp-config` files): `{"mcpServers": {"name": {...}}}`. Fields other
/// tools use are ignored; `sse` servers are skipped with a warning.
pub fn parse_mcp_json(text: &str, source: &Path) -> Result<(Vec<McpServerConfig>, Vec<String>), String> {
    let value: Value = serde_json::from_str(text).map_err(|e| format!("{}: {e}", source.display()))?;
    let Some(servers) = value.get("mcpServers").and_then(Value::as_object) else {
        return Err(format!("{}: no \"mcpServers\" object", source.display()));
    };
    let mut out = Vec::new();
    let mut warnings = Vec::new();
    for (name, entry) in servers {
        let kind = entry.get("type").and_then(Value::as_str).unwrap_or("");
        let text = |key: &str| entry.get(key).and_then(Value::as_str).map(str::to_string);
        let map = |key: &str| -> BTreeMap<String, String> {
            entry
                .get(key)
                .and_then(Value::as_object)
                .map(|m| m.iter().filter_map(|(k, v)| v.as_str().map(|v| (k.clone(), v.to_string()))).collect())
                .unwrap_or_default()
        };
        let mut server = McpServerConfig {
            name: name.clone(),
            ..Default::default()
        };
        match kind {
            "sse" => {
                warnings.push(format!("{}: skipped MCP server '{name}': the SSE transport is not supported", source.display()));
                continue;
            }
            "http" | "streamable-http" | "streamableHttp" => {
                server.url = text("url");
                server.headers = map("headers");
            }
            "" | "stdio" if entry.get("command").is_some() => {
                server.command = text("command");
                server.args = entry
                    .get("args")
                    .and_then(Value::as_array)
                    .map(|a| a.iter().filter_map(|v| v.as_str().map(str::to_string)).collect())
                    .unwrap_or_default();
                server.env = map("env");
                server.cwd = text("cwd").map(PathBuf::from);
            }
            "" if entry.get("url").is_some() => {
                server.url = text("url");
                server.headers = map("headers");
            }
            other => {
                warnings.push(format!("{}: skipped MCP server '{name}': unknown type '{other}'", source.display()));
                continue;
            }
        }
        if entry.get("disabled").and_then(Value::as_bool) == Some(true) {
            server.enabled = Some(false);
        }
        out.push(server);
    }
    Ok((out, warnings))
}

/// Says neither how to start the server nor where it is: settings for a server defined
/// elsewhere, such as turning it off for one project.
pub fn is_settings_only(server: &McpServerConfig) -> bool {
    server.command.is_none() && server.url.is_none()
}

/// `base` with the settings `over` sets.
fn overlay(base: &McpServerConfig, over: &McpServerConfig) -> McpServerConfig {
    let (Ok(Value::Object(mut merged)), Ok(Value::Object(set))) = (serde_json::to_value(base), serde_json::to_value(over)) else {
        return base.clone();
    };
    merged.extend(set);
    serde_json::from_value(Value::Object(merged)).unwrap_or_else(|_| base.clone())
}

/// The servers a session uses: `configured` (all configuration layers merged), plus the
/// `.mcp.json` servers whose names are not configured, then `extra` (command line, clients)
/// replacing servers of the same name. A configured entry that only changes settings (it
/// says neither how to start the server nor where it is) applies to the `.mcp.json` server
/// of its name. Invalid servers are dropped, and the warnings say why; disabled ones are
/// kept, unchecked, and not started.
pub fn merge_servers(
    configured: &[McpServerConfig],
    project_json: Vec<McpServerConfig>,
    extra: &[McpServerConfig],
) -> (Vec<McpServerConfig>, Vec<String>) {
    let mut by_name: BTreeMap<String, McpServerConfig> = BTreeMap::new();
    for server in configured {
        by_name.insert(server.name.clone(), server.clone());
    }
    for server in project_json {
        match by_name.get_mut(&server.name) {
            Some(settings) if is_settings_only(settings) => *settings = overlay(&server, settings),
            Some(_) => {}
            None => {
                by_name.insert(server.name.clone(), server);
            }
        }
    }
    for server in extra {
        by_name.insert(server.name.clone(), server.clone());
    }
    let mut warnings = Vec::new();
    let mut sanitized_names: BTreeMap<String, String> = BTreeMap::new();
    let mut out = Vec::new();
    for (name, server) in by_name {
        // Kept for listing; not checked, since turning a server off may leave it incomplete.
        // Turning off a server that is not here (an untrusted project's) leaves nothing.
        if !server.is_enabled() {
            if !is_settings_only(&server) {
                out.push(server);
            }
            continue;
        }
        if let Err(problem) = server.validate() {
            warnings.push(format!("MCP server '{name}' is not started: {problem}"));
            continue;
        }
        // Names that only differ in '-' and '_' would give the same tool names.
        let sanitized = naming::sanitize(&name);
        if let Some(first) = sanitized_names.get(&sanitized) {
            warnings.push(format!(
                "MCP server '{name}' is not started: its tools would have the same names as those of '{first}'"
            ));
            continue;
        }
        sanitized_names.insert(sanitized, name.clone());
        out.push(server);
    }
    (out, warnings)
}


/// The guidelines of `tools` that the frozen base prompt lacks: those of tools that came after
/// it was written, such as a browser turned on mid-session (connectors-entry-design §9.4).
/// Guidelines of tools gone since stay in the base prompt; they cost a few lines, rewriting the
/// prefix would cost the cache.
fn late_guidelines(base: &str, tools: &her_tools::ToolRegistry) -> Option<String> {
    let missing: Vec<&str> = tools.guidelines().into_iter().filter(|line| !base.contains(line)).collect();
    if missing.is_empty() {
        return None;
    }
    let mut text = String::from("<tool_guidelines>\n");
    for line in missing {
        text.push_str("- ");
        text.push_str(line);
        text.push('\n');
    }
    text.push_str("</tool_guidelines>");
    Some(text)
}

/// `base`, then the part after it when there is one.
pub(crate) fn compose(base: &str, section: Option<&str>) -> String {
    match section {
        Some(section) => format!("{}\n\n{section}\n", base.trim_end()),
        None => base.to_string(),
    }
}

/// The tools a session starts with: `tools`, plus `tool_search` and `call_tool` when some
/// tools are deferred, built-in or of an MCP server. Without any the session shares `tools`
/// itself, like every other session with the same tools.
pub(crate) fn base_tools(
    tools: &Arc<her_tools::ToolRegistry>,
    manager: Option<&Arc<McpManager>>,
    deferred: &crate::deferred::DeferredCell,
    shown: &Arc<Mutex<tools::Shown>>,
) -> Arc<her_tools::ToolRegistry> {
    if !manager.is_some_and(|m| m.has_deferred()) && deferred.lock().is_empty() {
        return tools.clone();
    }
    let mut registry = (**tools).clone();
    for tool in tools::kernel_tools(manager, deferred, shown) {
        // A custom registry may already define these names; it wins.
        let _ = registry.register(tool);
    }
    Arc::new(registry)
}

impl crate::session::Session {
    /// Brings the tool list and the system prompt up to the MCP servers' current state.
    /// Returns whether either changed. Records nothing.
    pub(crate) fn refresh_mcp(&self) -> bool {
        self.refresh_tools(false)
    }

    /// The tool list and the system prompt from `base_tools` and the MCP servers: when the
    /// servers changed, or always with `force` (the system connectors changed). Returns whether
    /// either changed. Records nothing.
    pub(crate) fn refresh_tools(&self, force: bool) -> bool {
        use std::sync::atomic::Ordering;
        match &self.mcp {
            Some(mcp) => {
                let generation = mcp.generation();
                if self.mcp_applied.swap(generation, Ordering::AcqRel) == generation && !force {
                    return false;
                }
            }
            None if !force => return false,
            None => {}
        }
        let mut tools = (**self.base_tools.lock()).clone();
        if let Some(mcp) = &self.mcp {
            for tool in &mcp.catalog().direct {
                let _ = tools.register(tool.clone());
            }
        }
        let mcp_section = prompt_section(self.mcp.as_ref(), &self.deferred.lock());
        let late = late_guidelines(&self.state.lock().base_prompt, &tools);
        let section = match (late, mcp_section) {
            (Some(late), Some(mcp)) => Some(format!("{late}\n\n{mcp}")),
            (late, mcp) => late.or(mcp),
        };
        let tools_changed = !self.tools().iter_specs().eq(tools.iter_specs());
        let prompt_changed = {
            let mut state = self.state.lock();
            let changed = state.mcp_section != section;
            if changed {
                state.mcp_section = section;
                state.compose_prompt();
            }
            changed
        };
        if tools_changed {
            *self.tools.lock() = Arc::new(tools);
        }
        let changed = tools_changed || prompt_changed;
        if changed {
            *self.fixed_tokens.lock() = None;
        }
        changed
    }

    /// The `system_prompt` entry for the current prompt and tools.
    pub(crate) fn record_system_prompt(&self) {
        let (text, mcp, memory, skills, template) = {
            let state = self.state.lock();
            (state.base_prompt.clone(), state.mcp_section.clone(), state.memory.clone(), state.skills.to_vec(), state.prompt_template.clone())
        };
        self.record(crate::store::EntryKind::SystemPrompt(Box::new(crate::store::SystemPromptEntry {
            text,
            tools: self.tools().names(),
            skills,
            mcp,
            memory,
            template,
        })));
    }

    /// At a turn boundary, before the request. The first time in a main session it waits for
    /// the servers whose tools go into the tool list; at the start of every task it checks
    /// required servers. Then the tool list and the prompt follow the servers (a change is
    /// recorded, and costs one prompt-cache miss).
    pub(crate) async fn prepare_mcp(&self, cancel: &CancellationToken, task_start: bool) -> Result<(), her_protocol::ErrorInfo> {
        use std::sync::atomic::Ordering;
        let Some(mcp) = self.mcp.clone() else { return Ok(()) };
        if !self.sub_agent && !self.mcp_settled.swap(true, Ordering::AcqRel) {
            let required = mcp.settle(cancel).await;
            if cancel.is_cancelled() {
                // Not settled after all: the next task waits again.
                self.mcp_settled.store(false, Ordering::Release);
                return Ok(());
            }
            // For clients that do not show server status: the servers that failed, and those
            // with tool-list tools that are still connecting (deferred ones were not waited for).
            let missing: Vec<String> = mcp
                .servers()
                .iter()
                .enumerate()
                .filter(|(i, s)| {
                    !s.config().required
                        && match s.status().state {
                            McpServerState::Ready => false,
                            McpServerState::Starting => mcp.waits_for(*i),
                            McpServerState::Failed | McpServerState::NeedsLogin | McpServerState::Closed => true,
                        }
                })
                .map(|(_, s)| {
                    let status = s.status();
                    match status.state {
                        McpServerState::Starting => format!("'{}' (still connecting)", s.name()),
                        _ => format!("'{}' ({})", s.name(), status.error.unwrap_or_default()),
                    }
                })
                .collect();
            if !missing.is_empty() {
                let message = format!("MCP servers not ready: {}", missing.join(", "));
                tracing::warn!(session = %self.id, "{message}");
                self.emit(EventMsg::Warning {
                    message,
                    code: Some(her_protocol::WarningCode::McpServersNotReady),
                });
            }
            if !required.is_empty() {
                return Err(required_error(&required));
            }
        } else if task_start && !self.sub_agent {
            let required: Vec<(String, String)> = mcp
                .servers()
                .iter()
                .filter(|s| s.config().required && matches!(s.status().state, McpServerState::Failed | McpServerState::NeedsLogin))
                .map(|s| (s.name().to_string(), s.status().error.unwrap_or_default()))
                .collect();
            if !required.is_empty() {
                return Err(required_error(&required));
            }
        }
        if self.refresh_mcp() {
            self.record_system_prompt();
        }
        Ok(())
    }
}

pub(crate) fn required_error(failed: &[(String, String)]) -> her_protocol::ErrorInfo {
    let list: Vec<String> = failed.iter().map(|(name, error)| format!("'{name}': {error}")).collect();
    her_protocol::ErrorInfo::new(
        "mcp_unavailable",
        format!("required MCP servers are not available: {}", list.join("; ")),
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sessions_without_deferred_servers_share_the_registry() {
        let tools = Arc::new(her_tools::builtin_registry(her_tools::BashTool::default()));
        let shown = Arc::new(Mutex::new(tools::Shown::default()));
        assert!(Arc::ptr_eq(&base_tools(&tools, None, &Default::default(), &shown), &tools));
        let settings = McpSettings {
            enabled: true,
            project_json: false,
            startup_wait: Duration::ZERO,
            exposure: McpExposure::Deferred,
            cache_dir: None,
            her_home: None,
            client_metadata_url: None,
        };
        let server = McpServerConfig {
            name: "docs".into(),
            command: Some("docs-mcp".into()),
            ..Default::default()
        };
        // Not started: building the manager runs nothing.
        let manager = McpManager::new(vec![server], &settings, None, Vec::new(), PathBuf::from("/"), None, false).unwrap();
        let with = base_tools(&tools, Some(&manager), &Default::default(), &shown);
        assert!(!Arc::ptr_eq(&with, &tools));
        assert!(with.get("tool_search").is_some() && with.get("call_tool").is_some());
        // The servers go away with the session: the status callback does not keep them.
        let servers: Vec<_> = manager.servers().iter().map(Arc::downgrade).collect();
        drop((with, manager));
        assert!(servers.iter().all(|s| s.upgrade().is_none()));
    }

    /// Her's conversation starts with no server and takes up one connected later: the catalog
    /// names it from then on, and adding it again changes nothing.
    #[test]
    fn a_growing_manager_takes_up_a_server_added_later() {
        let settings = McpSettings {
            enabled: true,
            project_json: false,
            startup_wait: Duration::ZERO,
            exposure: McpExposure::Deferred,
            cache_dir: None,
            her_home: None,
            client_metadata_url: None,
        };
        assert!(McpManager::new(Vec::new(), &settings, None, Vec::new(), PathBuf::from("/"), None, false).is_none());
        let manager = McpManager::new(Vec::new(), &settings, None, Vec::new(), PathBuf::from("/"), None, true).unwrap();
        let none = crate::deferred::Deferred::default();
        assert_eq!(prompt_section(Some(&manager), &none), None, "the prompt is the same as without a manager");
        let before = manager.generation();
        let tracker = McpServerConfig { name: "tracker".into(), url: Some("https://tracker.example.com/mcp".into()), ..Default::default() };
        manager.add(tracker.clone());
        assert!(manager.generation() > before);
        assert!(manager.has_deferred());
        assert!(prompt_section(Some(&manager), &none).is_some_and(|section| section.contains("- tracker")));
        let after = manager.generation();
        manager.add(tracker);
        assert_eq!((manager.servers().len(), manager.generation()), (1, after));
    }

    #[test]
    fn catalog_lists_every_server_within_the_limit() {
        let small = catalog_section(&[("github", Some("Issues and pull requests")), ("docs", None)]);
        assert!(small.starts_with("<deferred_tools>\n") && small.ends_with("</deferred_tools>"));
        assert!(small.contains("- github: Issues and pull requests\n- docs\n"), "{small}");
        let long_description = "x".repeat(500);
        let many: Vec<(String, Option<&str>)> = (0..100).map(|i| (format!("server{i:03}"), Some(long_description.as_str()))).collect();
        let refs: Vec<(&str, Option<&str>)> = many.iter().map(|(n, d)| (n.as_str(), *d)).collect();
        let section = catalog_section(&refs);
        assert!(section.len() <= CATALOG_LIMIT, "{}", section.len());
        assert!((0..100).all(|i| section.contains(&format!("- server{i:03}"))), "every name is kept");
        let huge: Vec<(String, Option<&str>)> = (0..1000).map(|i| (format!("a-rather-long-server-name-{i:04}"), None)).collect();
        let refs: Vec<(&str, Option<&str>)> = huge.iter().map(|(n, d)| (n.as_str(), *d)).collect();
        let section = catalog_section(&refs);
        assert!(section.len() <= CATALOG_LIMIT);
        assert!(section.contains("more servers; tool_search finds their tools too."), "{section}");
    }

    #[test]
    fn parses_the_common_json_format() {
        let text = r#"{"mcpServers": {
            "fs": {"command": "npx", "args": ["-y", "server-filesystem", "."], "env": {"A": "1"}, "someOtherTool": true},
            "docs": {"type": "http", "url": "https://mcp.example.com/mcp", "headers": {"Authorization": "Bearer ${T}"}},
            "old": {"type": "sse", "url": "https://x/sse"}
        }}"#;
        let (servers, warnings) = parse_mcp_json(text, Path::new(".mcp.json")).unwrap();
        assert_eq!(servers.len(), 2);
        let fs = servers.iter().find(|s| s.name == "fs").unwrap();
        assert_eq!(fs.args, vec!["-y", "server-filesystem", "."]);
        assert_eq!(fs.env.get("A").map(String::as_str), Some("1"));
        let docs = servers.iter().find(|s| s.name == "docs").unwrap();
        assert_eq!(docs.url.as_deref(), Some("https://mcp.example.com/mcp"));
        assert!(warnings[0].contains("SSE"));
        assert!(parse_mcp_json("{}", Path::new("x.json")).is_err());
    }

    #[test]
    fn merging_follows_the_precedence() {
        let server = |name: &str, command: &str| McpServerConfig {
            name: name.into(),
            command: Some(command.into()),
            ..Default::default()
        };
        let configured = vec![server("a", "config-a"), server("b", "config-b")];
        let json = vec![server("a", "json-a"), server("c", "json-c")];
        let mut disabled = server("b", "x");
        disabled.enabled = Some(false);
        let extra = vec![disabled, server("d-x", "extra"), server("d_x", "clash"), McpServerConfig {
            name: "bad name".into(),
            ..Default::default()
        }];
        let (servers, warnings) = merge_servers(&configured, json, &extra);
        let names: Vec<(&str, &str, bool)> =
            servers.iter().map(|s| (s.name.as_str(), s.command.as_deref().unwrap(), s.is_enabled())).collect();
        assert_eq!(names, vec![("a", "config-a", true), ("b", "x", false), ("c", "json-c", true), ("d-x", "extra", true)]);
        assert_eq!(warnings.len(), 2, "{warnings:?}");
    }

    #[test]
    fn settings_apply_to_the_mcp_json_server_of_their_name() {
        let off = |name: &str| McpServerConfig { name: name.into(), enabled: Some(false), ..Default::default() };
        let json = vec![McpServerConfig {
            name: "docs".into(),
            command: Some("docs-mcp".into()),
            args: vec!["--stdio".into()],
            ..Default::default()
        }];
        let (servers, warnings) = merge_servers(&[off("docs"), off("gone")], json, &[]);
        assert!(warnings.is_empty(), "{warnings:?}");
        assert_eq!(servers.len(), 1, "turning off a server that is not here leaves nothing");
        assert_eq!((servers[0].command.as_deref(), servers[0].args.len(), servers[0].is_enabled()), (Some("docs-mcp"), 1, false));
    }
}
