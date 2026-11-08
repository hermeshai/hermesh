//! One configured server: its connection, its tool list and its status over the session.
//!
//! A server connects in the background. Calls wait while it connects; after a crash or a lost
//! connection the next call reconnects once, at most every 30 seconds. Configuration errors
//! (a missing variable, a command that does not exist) are not retried.

use her_protocol::McpRemoteTransport;
use her_protocol::McpServerConfig;
use her_protocol::McpServerState;
use her_protocol::mcp::McpTransportKind;
use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use std::time::Duration;
use std::time::Instant;
use tokio::sync::mpsc;
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;

use crate::McpError;
use crate::client::ClientEvent;
use crate::client::McpClient;
use crate::client::ProgressFn;
use crate::client::RequestOptions;
use crate::env;
use crate::oauth::BearerSource;
use crate::oauth::OAuthPaths;
use crate::oauth::TokenSource;
use crate::transport::Transport;
use crate::transport::http;
use crate::transport::sse;
use crate::transport::stdio;
use crate::types::CallToolResult;
use crate::types::Implementation;
use crate::types::ToolInfo;

pub const DEFAULT_STARTUP_TIMEOUT: Duration = Duration::from_secs(30);
pub const DEFAULT_TOOL_TIMEOUT: Duration = Duration::from_secs(300);
/// Least time between two reconnection attempts.
pub const RECONNECT_INTERVAL: Duration = Duration::from_secs(30);

/// Receives every status change, for events.
pub type StatusFn = Arc<dyn Fn(&str, &ServerStatus) + Send + Sync>;

/// A stage of one connection attempt, for a probe's progress (mcp-design §13.6).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Phase {
    /// The process was started, or the remote transport opened.
    Started,
    /// `initialize` answered.
    Initialized,
    /// `tools/list` answered.
    Listed,
}

pub(crate) type PhaseFn = Arc<dyn Fn(Phase) + Send + Sync>;

/// Why an attempt failed, which decides what happens next.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Failure {
    /// Lost or refused: the next call may reconnect.
    Retry,
    /// The configuration is wrong; retrying cannot help.
    Permanent,
    /// Someone has to sign in.
    Login,
}

/// What a server needs from the session around it.
#[derive(Clone)]
pub struct ServerContext {
    /// Shared HTTP client for remote servers; `None` makes them fail with an explanation.
    pub http: Option<reqwest::Client>,
    /// Workspace roots, answered to `roots/list`.
    pub roots: Vec<PathBuf>,
    /// Default working directory of local servers.
    pub cwd: PathBuf,
    /// Base for relative `cwd` values.
    pub project_root: Option<PathBuf>,
    pub on_status: Option<StatusFn>,
    /// her's home, for OAuth tokens' refresh lock; `None` refreshes without it.
    pub oauth: Option<OAuthPaths>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ServerStatus {
    pub state: McpServerState,
    pub tools: u32,
    pub error: Option<String>,
    pub elapsed_ms: Option<u64>,
}

#[derive(Clone)]
pub struct CallOptions {
    pub cancel: CancellationToken,
    pub progress: Option<ProgressFn>,
}

enum State {
    Starting,
    Ready(McpClient),
    Failed { error: String, failure: Failure },
    Closed,
}

/// One successful connection: the client, its filtered tools, and what the handshake said.
pub(crate) struct Connected {
    pub client: McpClient,
    pub tools: Vec<ToolInfo>,
    pub instructions: Option<String>,
    pub info: Option<Implementation>,
}

struct Inner {
    state: State,
    connecting: bool,
    /// The last reconnection after a failure; the first connection does not count.
    last_reconnect: Option<Instant>,
    /// Filtered tools of the last successful listing.
    tools: Arc<Vec<ToolInfo>>,
    instructions: Option<String>,
    /// What the server said about itself in its last handshake.
    info: Option<Implementation>,
}

pub struct McpServer {
    config: McpServerConfig,
    ctx: ServerContext,
    inner: Mutex<Inner>,
    /// Bumped whenever the tools or the instructions change.
    generation: AtomicU64,
    refreshing: AtomicBool,
    status_tx: watch::Sender<ServerStatus>,
}

impl McpServer {
    pub fn new(config: McpServerConfig, ctx: ServerContext) -> Arc<McpServer> {
        let (status_tx, _) = watch::channel(ServerStatus {
            state: McpServerState::Starting,
            tools: 0,
            error: None,
            elapsed_ms: None,
        });
        Arc::new(McpServer {
            config,
            ctx,
            inner: Mutex::new(Inner {
                state: State::Starting,
                connecting: false,
                last_reconnect: None,
                tools: Arc::new(Vec::new()),
                instructions: None,
                info: None,
            }),
            generation: AtomicU64::new(0),
            refreshing: AtomicBool::new(false),
            status_tx,
        })
    }

    pub fn name(&self) -> &str {
        &self.config.name
    }

    pub fn config(&self) -> &McpServerConfig {
        &self.config
    }

    pub fn tool_timeout(&self) -> Duration {
        self.config
            .tool_timeout_secs
            .map(|s| Duration::from_secs(s.max(1)))
            .unwrap_or(DEFAULT_TOOL_TIMEOUT)
    }

    fn startup_timeout(&self) -> Duration {
        self.config
            .startup_timeout_secs
            .map(|s| Duration::from_secs(s.max(1)))
            .unwrap_or(DEFAULT_STARTUP_TIMEOUT)
    }

    pub fn status(&self) -> ServerStatus {
        self.status_tx.borrow().clone()
    }

    pub fn generation(&self) -> u64 {
        self.generation.load(Ordering::Acquire)
    }

    pub fn tools(&self) -> Arc<Vec<ToolInfo>> {
        self.inner.lock().tools.clone()
    }

    pub fn instructions(&self) -> Option<String> {
        self.inner.lock().instructions.clone()
    }

    pub fn info(&self) -> Option<Implementation> {
        self.inner.lock().info.clone()
    }

    pub fn offers(&self, tool: &str) -> bool {
        self.inner.lock().tools.iter().any(|t| t.name == tool)
    }

    fn publish(&self, status: ServerStatus) {
        if let Some(on_status) = &self.ctx.on_status {
            on_status(&self.config.name, &status);
        }
        self.status_tx.send_replace(status);
    }

    /// Starts connecting in the background, unless an attempt is running or it is closed.
    pub fn connect(self: &Arc<Self>) {
        {
            let mut inner = self.inner.lock();
            if inner.connecting || matches!(inner.state, State::Closed) {
                return;
            }
            inner.connecting = true;
            inner.state = State::Starting;
        }
        self.publish(ServerStatus {
            state: McpServerState::Starting,
            tools: 0,
            error: None,
            elapsed_ms: None,
        });
        let server = self.clone();
        tokio::spawn(async move { server.run_connect().await });
    }

    async fn run_connect(self: Arc<Self>) {
        let started = Instant::now();
        let timeout = self.startup_timeout();
        let result = match tokio::time::timeout(timeout, self.clone().connect_once(timeout, None)).await {
            Ok(result) => result,
            Err(_) => Err((format!("no answer within {} s", timeout.as_secs()), Failure::Retry)),
        };
        let elapsed_ms = Some(started.elapsed().as_millis() as u64);
        let status = {
            let mut inner = self.inner.lock();
            inner.connecting = false;
            if matches!(inner.state, State::Closed) {
                // Shut down while connecting.
                if let Ok(Connected { client, .. }) = result {
                    tokio::spawn(async move { client.close().await });
                }
                return;
            }
            match result {
                Ok(Connected { client, tools, instructions, info }) => {
                    let changed = *inner.tools != tools || inner.instructions != instructions;
                    let count = tools.len() as u32;
                    inner.tools = Arc::new(tools);
                    inner.instructions = instructions;
                    inner.info = info;
                    inner.state = State::Ready(client);
                    if changed {
                        self.generation.fetch_add(1, Ordering::AcqRel);
                    }
                    ServerStatus {
                        state: McpServerState::Ready,
                        tools: count,
                        error: None,
                        elapsed_ms,
                    }
                }
                Err((error, failure)) => {
                    inner.state = State::Failed { error: error.clone(), failure };
                    ServerStatus {
                        state: if failure == Failure::Login { McpServerState::NeedsLogin } else { McpServerState::Failed },
                        tools: 0,
                        error: Some(error),
                        elapsed_ms,
                    }
                }
            }
        };
        self.publish(status);
    }

    /// One attempt: transport, handshake, tool list. `Err` carries the reason and what kind of
    /// failure it is. `phase` hears each stage that passes.
    pub(crate) async fn connect_once(
        self: Arc<Self>,
        timeout: Duration,
        phase: Option<PhaseFn>,
    ) -> Result<Connected, (String, Failure)> {
        let passed = |p: Phase| {
            if let Some(phase) = &phase {
                phase(p);
            }
        };
        // Saved credentials are read from the keychain off the async threads, once per attempt.
        let names = env::credential_names(
            [&self.config.command, &self.config.url]
                .into_iter()
                .flatten()
                .map(String::as_str)
                .chain(self.config.args.iter().map(String::as_str))
                .chain(self.config.env.values().map(String::as_str))
                .chain(self.config.headers.values().map(String::as_str)),
        );
        let credentials: std::collections::HashMap<String, String> = if names.is_empty() {
            Default::default()
        } else {
            tokio::task::spawn_blocking(move || {
                names.into_iter().filter_map(|name| her_llm::credentials::get(&name).map(|value| (name, value))).collect()
            })
            .await
            .unwrap_or_default()
        };
        let lookup = |name: &str| match name.strip_prefix("credential:") {
            Some(credential) => credentials.get(credential).cloned(),
            None => env::process_lookup(name),
        };
        let expand = |value: &str| env::expand(value, &lookup).map_err(|e| (e, Failure::Permanent));
        let (inbound_tx, inbound_rx) = mpsc::unbounded_channel();
        let kind = self.config.transport().map_err(|e| (e, Failure::Permanent))?;
        let mut stderr_hint: Option<Arc<stdio::StdioTransport>> = None;
        let transport: Arc<dyn Transport> = match kind {
            McpTransportKind::Stdio => {
                let program = expand(self.config.command.as_deref().unwrap_or_default())?;
                let args = self.config.args.iter().map(|a| expand(a)).collect::<Result<Vec<_>, _>>()?;
                let env = env::server_env(&self.config.env_vars, &self.config.env, &lookup, std::env::vars())
                    .map_err(|e| (e, Failure::Permanent))?;
                let cwd = match &self.config.cwd {
                    Some(cwd) => {
                        let cwd = PathBuf::from(expand(&cwd.to_string_lossy())?);
                        if cwd.is_absolute() {
                            cwd
                        } else {
                            self.ctx.project_root.as_deref().unwrap_or(&self.ctx.cwd).join(cwd)
                        }
                    }
                    None => self.ctx.cwd.clone(),
                };
                let spec = stdio::StdioSpec {
                    server: self.config.name.clone(),
                    program,
                    args,
                    env,
                    cwd,
                };
                let transport = stdio::spawn(spec, inbound_tx).map_err(|e| (e.message, if e.permanent { Failure::Permanent } else { Failure::Retry }))?;
                stderr_hint = Some(transport.clone());
                transport
            }
            McpTransportKind::Http => {
                let Some(client) = self.ctx.http.clone() else {
                    return Err(("remote MCP servers need an HTTP client, which is not available here".into(), Failure::Permanent));
                };
                let url = expand(self.config.url.as_deref().unwrap_or_default())?;
                let mut headers = Vec::new();
                for (name, value) in &self.config.headers {
                    headers.push((name.clone(), expand(value)?));
                }
                if let Some(var) = &self.config.bearer_token_env_var {
                    let token = lookup(var)
                        .filter(|t| !t.is_empty())
                        .ok_or_else(|| (format!("environment variable {var} (bearer_token_env_var) is not set"), Failure::Permanent))?;
                    headers.push(("Authorization".into(), format!("Bearer {token}")));
                }
                let auth: Option<Arc<dyn BearerSource>> = self
                    .config
                    .uses_oauth()
                    .then(|| TokenSource::new(&self.config.name, &url, client.clone(), self.ctx.oauth.clone()) as Arc<dyn BearerSource>);
                if let Some(auth) = &auth {
                    // Without a usable token nothing is sent: someone has to sign in.
                    auth.token().await.map_err(|e| (e.to_string(), Failure::Login))?;
                }
                match self.config.remote_transport() {
                    McpRemoteTransport::StreamableHttp => {
                        Arc::new(http::connect(self.config.name.clone(), client, url, &headers, auth, inbound_tx).map_err(|e| (e, Failure::Permanent))?)
                    }
                    McpRemoteTransport::Sse => {
                        let wait = timeout.min(Duration::from_secs(15));
                        let transport = sse::connect(self.config.name.clone(), client, url, &headers, auth, inbound_tx, wait).await;
                        Arc::new(transport.map_err(|e| self.classify(e))?)
                    }
                }
            }
        };
        passed(Phase::Started);
        let (events_tx, events_rx) = mpsc::unbounded_channel();
        let client = McpClient::start(self.config.name.clone(), transport, inbound_rx, self.root_uris(), events_tx);
        let failed = |err: McpError| {
            let message = err.to_string();
            let message = match &stderr_hint {
                Some(stdio) => stdio.stderr_tail(message),
                None => message,
            };
            (message, self.classify(err).1)
        };
        let init = match client.initialize(timeout).await {
            Ok(init) => init,
            Err(err) => {
                let reason = failed(err);
                client.close().await;
                return Err(reason);
            }
        };
        passed(Phase::Initialized);
        let (tools, problems) = if init.capabilities.tools.is_some() {
            match client.list_tools(timeout).await {
                Ok(listed) => listed,
                Err(err) => {
                    let reason = failed(err);
                    client.close().await;
                    return Err(reason);
                }
            }
        } else {
            (Vec::new(), Vec::new())
        };
        passed(Phase::Listed);
        for problem in problems {
            tracing::warn!(server = %self.config.name, "skipped a malformed tool: {problem}");
        }
        self.clone().watch_events(events_rx);
        let instructions = init.instructions.filter(|i| !i.trim().is_empty());
        Ok(Connected { client, tools: self.filter(tools), instructions, info: init.server_info })
    }

    /// What an error means for this server: a 401 or a missing sign-in asks someone to sign
    /// in when it uses OAuth, and is a configuration error otherwise.
    fn classify(&self, err: McpError) -> (String, Failure) {
        let failure = match &err {
            McpError::NeedsLogin(_) => Failure::Login,
            McpError::AuthRequired if self.config.uses_oauth() => Failure::Login,
            McpError::AuthRequired => Failure::Permanent,
            _ => Failure::Retry,
        };
        let message = match (&err, failure) {
            (McpError::AuthRequired, Failure::Login) => format!("{err}; sign in with `her mcp login {}` or from the 自定义 page", self.config.name),
            _ => err.to_string(),
        };
        (message, failure)
    }

    fn root_uris(&self) -> Vec<Value> {
        self.ctx
            .roots
            .iter()
            .map(|root| {
                let name = root.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
                json!({ "uri": file_uri(root), "name": name })
            })
            .collect()
    }

    /// `enabled_tools`, then `disabled_tools`; tools meant only for user interfaces go too.
    fn filter(&self, tools: Vec<ToolInfo>) -> Vec<ToolInfo> {
        tools
            .into_iter()
            .filter(|t| !t.app_only)
            .filter(|t| self.config.enabled_tools.as_ref().is_none_or(|only| only.contains(&t.name)))
            .filter(|t| !self.config.disabled_tools.contains(&t.name))
            .collect()
    }

    fn watch_events(self: Arc<Self>, mut events: mpsc::UnboundedReceiver<ClientEvent>) {
        let weak = Arc::downgrade(&self);
        drop(self);
        tokio::spawn(async move {
            while let Some(event) = events.recv().await {
                let Some(server) = weak.upgrade() else { return };
                match event {
                    ClientEvent::ToolsChanged => server.refresh_tools(),
                    ClientEvent::Closed(reason) => {
                        server.connection_lost(reason);
                        return;
                    }
                }
            }
        });
    }

    fn connection_lost(&self, reason: String) {
        {
            let mut inner = self.inner.lock();
            if !matches!(inner.state, State::Ready(_)) {
                return;
            }
            inner.state = State::Failed {
                error: reason.clone(),
                failure: Failure::Retry,
            };
        }
        tracing::warn!(server = %self.config.name, "MCP server disconnected: {reason}");
        self.publish(ServerStatus {
            state: McpServerState::Failed,
            tools: 0,
            error: Some(reason),
            elapsed_ms: None,
        });
    }

    /// `tools/list_changed`: lists again. Notifications that arrive during a refresh are folded
    /// into it.
    fn refresh_tools(self: Arc<Self>) {
        if self.refreshing.swap(true, Ordering::AcqRel) {
            return;
        }
        tokio::spawn(async move {
            tokio::time::sleep(Duration::from_millis(50)).await;
            self.refreshing.store(false, Ordering::Release);
            let client = match &self.inner.lock().state {
                State::Ready(client) => client.clone(),
                _ => return,
            };
            let tools = match client.list_tools(self.startup_timeout()).await {
                Ok((tools, _)) => self.filter(tools),
                Err(err) => {
                    // The old list stays; the server will be listed again on its next change.
                    tracing::warn!(server = %self.config.name, %err, "could not refresh the tool list");
                    return;
                }
            };
            let count = tools.len() as u32;
            let changed = {
                let mut inner = self.inner.lock();
                let changed = *inner.tools != tools;
                if changed {
                    inner.tools = Arc::new(tools);
                }
                changed
            };
            if changed {
                self.generation.fetch_add(1, Ordering::AcqRel);
                self.publish(ServerStatus {
                    state: McpServerState::Ready,
                    tools: count,
                    error: None,
                    elapsed_ms: None,
                });
            }
        });
    }

    /// Waits until the current connection attempt ends or `deadline` passes.
    pub async fn wait_settled(&self, deadline: Instant) {
        let mut rx = self.status_tx.subscribe();
        let wait = rx.wait_for(|status| status.state != McpServerState::Starting);
        let _ = tokio::time::timeout_at(tokio::time::Instant::from_std(deadline), wait).await;
    }

    /// A ready client: waits for a running attempt, and reconnects once after a lost
    /// connection.
    async fn client(self: &Arc<Self>, cancel: &CancellationToken) -> Result<McpClient, McpError> {
        let mut reconnected = false;
        loop {
            let action = {
                let mut inner = self.inner.lock();
                let due = inner.last_reconnect.is_none_or(|t| t.elapsed() >= RECONNECT_INTERVAL);
                match &inner.state {
                    State::Ready(client) => return Ok(client.clone()),
                    State::Closed => return Err(McpError::Unavailable("the server was shut down".into())),
                    State::Starting => None,
                    State::Failed { error, failure } => {
                        // Waiting for a sign-in: try again as soon as one is saved.
                        let signed_in = *failure == Failure::Login
                            && self.config.url.as_deref().is_some_and(|url| crate::oauth::signed_in(&self.config.name, url));
                        let blocked = match failure {
                            Failure::Permanent => true,
                            Failure::Login => !signed_in,
                            Failure::Retry => !due,
                        };
                        if blocked || reconnected {
                            return Err(if *failure == Failure::Login { McpError::NeedsLogin(error.clone()) } else { McpError::Unavailable(error.clone()) });
                        }
                        inner.last_reconnect = Some(Instant::now());
                        Some(())
                    }
                }
            };
            if action.is_some() {
                reconnected = true;
                self.connect();
            }
            let deadline = Instant::now() + self.startup_timeout() + Duration::from_secs(1);
            tokio::select! {
                _ = cancel.cancelled() => return Err(McpError::Cancelled),
                _ = self.wait_settled(deadline) => {}
            }
            if matches!(self.inner.lock().state, State::Starting) {
                return Err(McpError::Unavailable("still connecting".into()));
            }
        }
    }

    pub async fn call_tool(self: &Arc<Self>, tool: &str, arguments: Value, options: CallOptions) -> Result<CallToolResult, McpError> {
        let client = self.client(&options.cancel).await?;
        if !self.offers(tool) {
            return Err(McpError::UnknownTool(tool.to_string()));
        }
        let request = RequestOptions {
            timeout: self.tool_timeout(),
            cancel: Some(options.cancel),
            progress: options.progress,
            notify_cancel: true,
        };
        client.call_tool(tool, arguments, request).await
    }

    pub async fn shutdown(&self) {
        let client = {
            let mut inner = self.inner.lock();
            match std::mem::replace(&mut inner.state, State::Closed) {
                State::Ready(client) => Some(client),
                _ => None,
            }
        };
        if let Some(client) = client {
            client.close().await;
        }
        self.publish(ServerStatus {
            state: McpServerState::Closed,
            tools: 0,
            error: None,
            elapsed_ms: None,
        });
    }
}

/// `file://` URI of an absolute path, with the characters URIs reserve percent-encoded.
pub fn file_uri(path: &Path) -> String {
    let mut out = String::from("file://");
    for byte in path.to_string_lossy().as_bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'/' | b'-' | b'_' | b'.' | b'~' => out.push(*byte as char),
            _ => out.push_str(&format!("%{byte:02X}")),
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn file_uris_escape_reserved_characters() {
        assert_eq!(file_uri(Path::new("/work/my project")), "file:///work/my%20project");
        assert_eq!(file_uri(Path::new("/a/b_c.d")), "file:///a/b_c.d");
    }
}
