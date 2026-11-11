//! The service: loaded sessions, connections and the operations both protocols build on.

use her_core::HerHome;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::ResumeOptions;
use her_core::SessionRef;
use her_core::SessionStore;
use her_core::StartOptions;
use her_core::Workspace;
use her_core::config::AcpSettings;
use her_core::config::ServerSettings;
use her_core::instructions;
use her_core::runtime;
use her_core::skills;
use her_core::runtime::RuntimeError;
use her_llm::ModelRegistry;
use her_llm::ThinkingLevel;
use her_protocol::McpServerConfig;
use her_protocol::Op;
use her_protocol::PermissionMode;
use her_protocol::api::ResourceReadParams;
use her_protocol::api::ResourceReadResult;
use her_protocol::api::SessionInfo;
use her_protocol::api::SessionListParams;
use her_protocol::api::SessionListResult;
use her_protocol::api::ToolsListParams;
use her_protocol::api::ToolsListResult;
use her_protocol::api::SessionSummary;
use her_protocol::api::SkillsListParams;
use her_protocol::api::SkillsListResult;
use her_protocol::api::TranscriptItem;
use her_protocol::api::TranscriptPage;
use her_rpc::RpcError;
use parking_lot::Mutex;
use std::collections::HashMap;
use std::io::Read;
use std::io::Seek;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use std::time::Duration;
use tokio::sync::oneshot;
use tokio_util::sync::CancellationToken;

use crate::conn::ConnHandle;
use crate::conn::OutQueue;
use crate::errors;
use crate::hub;
use crate::hub::AttachRequest;
use crate::hub::DetachResponder;
use crate::hub::HubCmd;
use crate::hub::HubHandle;
use crate::hub::OpResponder;
use crate::hub::SummaryBase;
use crate::list;
use crate::list::ListFeed;
use her_core::store::EntryKind;
use her_core::store::TaskStatus;
use her_protocol::api::SessionDefaults;
use her_protocol::api::SessionState;

const RESOURCE_DEFAULT_LIMIT: u64 = 1 << 20;
const RESOURCE_MAX_LIMIT: u64 = 8 << 20;

pub struct ServerOptions {
    pub home: HerHome,
    /// Directory whose configuration provides the server settings and model registry.
    pub cwd: PathBuf,
    /// `key=value` settings applied to every session, after both configuration files.
    pub overrides: Vec<String>,
    /// MCP servers for every session (`--mcp-config`), over the configured ones.
    pub mcp_servers: Vec<McpServerConfig>,
    /// How documents' PDFium worker runs: `her doc-worker`, or a thread in tests.
    pub doc_worker: her_docs::Launcher,
}

/// Protocol-neutral arguments for a new session.
#[derive(Debug, Clone, Default)]
pub struct NewSession {
    /// None: no project, an empty folder made for the session.
    pub cwd: Option<PathBuf>,
    pub extra_roots: Vec<PathBuf>,
    pub model: Option<String>,
    pub thinking: Option<ThinkingLevel>,
    pub permission_mode: Option<PermissionMode>,
    /// Refuse a model whose provider has no credentials yet, for clients that have a way to send
    /// the person to sign in (ACP); others hear of it when a task fails.
    pub require_credentials: bool,
    /// MCP servers the client brings for this session; the same name replaces a configured one.
    pub mcp_servers: Vec<McpServerConfig>,
}

/// Settings to change when resuming.
#[derive(Debug, Clone, Default)]
pub struct ResumeOverrides {
    pub model: Option<String>,
    pub thinking: Option<ThinkingLevel>,
    pub permission_mode: Option<PermissionMode>,
    pub mcp_servers: Vec<McpServerConfig>,
}

impl ResumeOverrides {
    fn is_empty(&self) -> bool {
        self.model.is_none() && self.thinking.is_none() && self.permission_mode.is_none() && self.mcp_servers.is_empty()
    }
}

#[derive(Clone)]
pub struct Server(pub(crate) Arc<Inner>);

pub(crate) struct Inner {
    pub(crate) connections: Arc<her_connections::Manager>,
    pub(crate) model_watchers: Mutex<HashMap<crate::conn::ConnId, ConnHandle>>,
    /// Connections that called `browser/status`, the extensions connected, and why the last
    /// host was refused (crate::browser_status).
    pub(crate) browser_watchers: Mutex<HashMap<crate::conn::ConnId, ConnHandle>>,
    pub(crate) extensions: Mutex<Vec<crate::browser_status::ExtensionEntry>>,
    pub(crate) host_refused: Mutex<Option<String>>,
    /// Model lists and the catalog (crate::models).
    pub(crate) model_upkeep: crate::models::Upkeep,
    pub(crate) home: HerHome,
    pub(crate) overrides: Vec<String>,
    mcp_servers: Vec<McpServerConfig>,
    settings: ServerSettings,
    acp: AcpSettings,
    pub(crate) llm: Arc<ModelRegistry>,
    store: Arc<JsonlStore>,
    sessions: Mutex<HashMap<String, HubHandle>>,
    /// Serializes loading so a session is opened once even when resumed concurrently.
    loading: tokio::sync::Mutex<()>,
    next_conn: AtomicU64,
    shutdown: CancellationToken,
    closing: AtomicBool,
    /// Detect create/run/close bursts shorter than the memory-upkeep polling interval.
    closed_sessions: AtomicU64,
    /// Connections following the session list.
    pub(crate) feed: Arc<ListFeed>,
    /// What `session/new` uses for settings it leaves out (the user's configuration).
    pub(crate) defaults: SessionDefaults,
    /// The 自定义 page's subscriptions and file watching (crate::customize).
    pub(crate) customize: crate::customize::State,
    /// What the file panel watches (crate::files).
    pub(crate) files: crate::files::Watches,
    /// Connections that opened a document: they hear `doc/changed` (crate::docs).
    pub(crate) doc_watchers: Mutex<HashMap<crate::conn::ConnId, ConnHandle>>,
    /// The browsers of every session here, one per profile (browser-design §6.1).
    pub(crate) browser_pool: Arc<her_core::BrowserPool>,
    /// The documents every session here shares (document-editor-design §5.2).
    pub(crate) docs: Arc<her_docs::DocStore>,
    /// The presentations every session here shares (presentation-kernel-authority).
    pub(crate) decks: Arc<her_docs::deck::DeckStore>,
    /// Who opened which deck (crate::decks).
    pub(crate) deck_watchers: crate::decks::DeckWatchers,
    /// The desktop app's connection that writes .docx files (crate::docx_host).
    pub(crate) docx_host: Arc<crate::docx_host::DocxHost>,
    /// The Word documents every session here shares (word-design §4), on the decks' worker.
    pub(crate) words: Arc<her_docs::word::WordStore>,
    /// Who opened which document (crate::words).
    pub(crate) docx_watchers: crate::decks::DeckWatchers,
    /// Every bot and who follows their list (crate::bots).
    pub(crate) bots: Arc<crate::bots::Registry>,
    /// Their routines (crate::routines).
    pub(crate) routines: Arc<crate::routines::Routines>,
    /// The connectors Her's conversation reaches, and its cards that wait for a sign-in
    /// (crate::cards).
    pub(crate) connectors: Arc<crate::cards::Connectors>,
    /// The connection that gives a bot's conversation the `mesh` group (crate::mesh_host).
    pub(crate) mesh: Arc<crate::mesh_host::MeshHosts>,
}

impl Server {
    pub fn new(options: ServerOptions) -> Result<Server, RuntimeError> {
        let workspace = Workspace::open(&options.home, &options.cwd, &options.overrides)?;
        for warning in &workspace.warnings {
            tracing::warn!("{warning}");
        }
        let llm = Arc::new(runtime::model_registry(&options.home, &workspace.config)?);
        for warning in llm.warnings() {
            tracing::warn!("{warning}");
        }
        let defaults = SessionDefaults {
            model: workspace.config.model.clone(),
            thinking: workspace.config.thinking,
            permission_mode: workspace.config.permission_mode,
        };
        let connections = her_connections::Manager::new(options.home.root().to_path_buf(), &llm);
        // A person may watch: show a window where there is a display.
        let browser_pool = Arc::new(her_core::BrowserPool::new(options.home.browser_dir(), true));
        let decks = Arc::new(her_docs::deck::DeckStore::new(
            options.home.root().join("decks"),
            her_docs::deck::DeckLauncher::like(&options.doc_worker),
        ));
        let docx_host = Arc::new(crate::docx_host::DocxHost::default());
        let words = Arc::new(her_docs::word::WordStore::new(options.home.root().join("docx"), &decks, docx_host.clone()));
        let docs = Arc::new(her_docs::DocStore::new(options.home.root().join("docs"), options.doc_worker));
        let bots = Arc::new(crate::bots::Registry::new(options.home.clone()));
        let routines = crate::routines::Routines::new(options.home.clone(), bots.clone());
        let server = Server(Arc::new(Inner {
            connections: connections.clone(),
            model_watchers: Mutex::new(HashMap::new()),
            browser_watchers: Mutex::new(HashMap::new()),
            extensions: Mutex::new(Vec::new()),
            host_refused: Mutex::new(None),
            model_upkeep: crate::models::Upkeep::new(workspace.config.models_network),
            feed: Arc::new(ListFeed::default()),
            bots,
            routines,
            connectors: Arc::default(),
            mesh: Arc::default(),
            defaults,
            store: Arc::new(JsonlStore::new(options.home.sessions_dir())),
            home: options.home,
            overrides: options.overrides,
            mcp_servers: options.mcp_servers,
            settings: workspace.config.server.clone(),
            acp: workspace.config.acp.clone(),
            llm,
            sessions: Mutex::new(HashMap::new()),
            loading: tokio::sync::Mutex::new(()),
            next_conn: AtomicU64::new(1),
            shutdown: CancellationToken::new(),
            closing: AtomicBool::new(false),
            closed_sessions: AtomicU64::new(0),
            customize: Default::default(),
            files: Default::default(),
            doc_watchers: Mutex::default(),
            // A person may watch: show a window where there is a display.
            browser_pool,
            docs,
            decks,
            deck_watchers: Default::default(),
            docx_host,
            words,
            docx_watchers: Default::default(),
        }));
        server.0.routines.bind(Arc::downgrade(&server.0));
        server.0.connectors.bind(Arc::downgrade(&server.0));
        let weak = Arc::downgrade(&server.0);
        connections.on_change(Arc::new(move || {
            if let Some(inner) = weak.upgrade() { Server(inner).models_changed(); }
        }));
        server.follow_doc_changes();
        server.follow_deck_changes();
        server.follow_word_changes();
        server.follow_browser_changes();
        spawn_memory_upkeep(&server.0);
        Ok(server)
    }

    pub fn settings(&self) -> &ServerSettings {
        &self.0.settings
    }

    pub fn acp_settings(&self) -> &AcpSettings {
        &self.0.acp
    }

    pub fn connections(&self) -> &Arc<her_connections::Manager> {
        &self.0.connections
    }

    pub fn models(&self) -> &Arc<ModelRegistry> {
        &self.0.llm
    }

    pub fn home(&self) -> &HerHome {
        &self.0.home
    }

    /// Cancelled when a client asked the server to stop.
    pub fn shutdown_token(&self) -> CancellationToken {
        self.0.shutdown.clone()
    }

    pub fn request_shutdown(&self) {
        self.0.shutdown.cancel();
    }

    pub fn new_connection(&self) -> ConnHandle {
        let coalesce = Arc::new(AtomicBool::new(true));
        ConnHandle {
            id: self.0.next_conn.fetch_add(1, Ordering::Relaxed),
            queue: Arc::new(OutQueue::new(
                self.0.settings.queue_hard_bytes,
                coalesce.clone(),
            )),
            coalesce,
            peer_pid: None,
        }
    }

    /// Detaches a closed connection from every session and from the session list.
    pub fn connection_closed(&self, conn: &ConnHandle) {
        self.forget_model_watcher(conn.id);
        self.forget_customize(conn.id);
        self.forget_files(conn.id);
        self.forget_browser_watcher(conn.id);
        self.forget_doc_watcher(conn.id);
        self.forget_deck_watcher(conn.id);
        self.forget_docx_watcher(conn.id);
        self.0.feed.cancel(conn.id);
        for hub in self.hubs() {
            if hub.tx.try_send(Box::new(HubCmd::ConnGone { conn: conn.id })).is_err() {
                let tx = hub.tx.clone();
                let id = conn.id;
                tokio::spawn(async move {
                    let _ = tx.send(Box::new(HubCmd::ConnGone { conn: id })).await;
                });
            }
        }
    }

    pub(crate) fn hubs(&self) -> Vec<HubHandle> {
        self.0.sessions.lock().values().cloned().collect()
    }

    pub(crate) fn hub(&self, id: &str) -> Option<HubHandle> {
        self.0.sessions.lock().get(id).cloned()
    }

    pub fn is_loaded(&self, id: &str) -> bool {
        self.0.sessions.lock().contains_key(id)
    }

    /// Whether any loaded session is running a task, or a routine's run.
    pub fn busy(&self) -> bool {
        self.hubs()
            .iter()
            .any(|h| h.shared.busy.load(Ordering::Relaxed) || h.shared.routine_runs.load(Ordering::Relaxed) > 0)
    }

    pub(crate) fn is_closing(&self) -> bool {
        self.0.closing.load(Ordering::Relaxed)
    }

    pub fn loaded_sessions(&self) -> usize {
        self.0.sessions.lock().len()
    }

    /// Closes every loaded session and waits until their logs are closed.
    pub async fn close_all(&self) {
        self.0.closing.store(true, Ordering::Relaxed);
        let hubs = self.hubs();
        for hub in &hubs {
            let _ = hub.send(HubCmd::Close { reply: None }).await;
        }
        for hub in &hubs {
            hub.shared.wait_closed().await;
        }
        self.0.routines.close().await;
    }

    /// Makes room for one more session. A full server unloads the session that has gone
    /// unused the longest (no client attached, no task running): its log is on disk, and
    /// resuming loads it again. Fails only when every loaded session is in use.
    pub(crate) async fn make_room(&self) -> Result<(), RpcError> {
        if self.0.closing.load(Ordering::Relaxed) {
            return Err(RpcError::internal("the server is shutting down"));
        }
        let max = self.0.settings.max_sessions;
        if self.loaded_sessions() < max {
            return Ok(());
        }
        let mut unused: Vec<(u64, HubHandle)> = self
            .hubs()
            .into_iter()
            .map(|hub| (hub.shared.idle_since.load(Ordering::Relaxed), hub))
            .filter(|(since, _)| *since > 0)
            .collect();
        unused.sort_unstable_by_key(|(since, _)| *since);
        for (since, hub) in unused {
            // Claimed first, so that requests arriving together unload different sessions.
            if hub
                .shared
                .idle_since
                .compare_exchange(since, 0, Ordering::AcqRel, Ordering::Relaxed)
                .is_err()
            {
                continue;
            }
            // The hub checks again: a client may have attached since.
            let (reply, unloaded) = oneshot::channel();
            if hub.send(HubCmd::Unload { reply }).await.is_ok() && unloaded.await.unwrap_or(false) {
                tracing::info!(session = %hub.shared.session.id, "unloading the session unused the longest to make room");
                return Ok(());
            }
        }
        Err(errors::server_full(max))
    }

    fn idle_after(&self) -> Option<Duration> {
        let secs = self.0.settings.idle_session_secs;
        (secs > 0).then(|| Duration::from_secs(secs))
    }

    /// Starts the hub of a loaded session; `bot` names the bot whose conversation it is.
    pub(crate) fn register(
        &self,
        kernel: Kernel,
        transcript: Vec<TranscriptItem>,
        attach: Option<AttachRequest>,
        base: SummaryBase,
        bot: Option<String>,
    ) -> String {
        let id = kernel.session_id().to_string();
        let server = Arc::downgrade(&self.0);
        let exit_id = id.clone();
        let exit_bot = bot.clone();
        let on_exit = Box::new(move |last: SessionSummary| {
            if let Some(inner) = server.upgrade() {
                inner.sessions.lock().remove(&exit_id);
                inner.closed_sessions.fetch_add(1, Ordering::Relaxed);
                match &exit_bot {
                    Some(bot) => inner.bots.on_unloaded(bot),
                    None => inner.feed.publish(vec![last], Vec::new()),
                }
            }
        });
        let listing = match bot {
            Some(id) => hub::Listing::Bot {
                id,
                bots: self.0.bots.clone(),
                routines: self.0.routines.clone(),
            },
            None => hub::Listing::Sessions(self.0.feed.clone()),
        };
        // Hold the table lock while the hub starts, so `on_exit` cannot run first.
        let mut sessions = self.0.sessions.lock();
        let handle = hub::spawn(
            kernel,
            transcript,
            self.idle_after(),
            attach,
            listing,
            base,
            on_exit,
        );
        sessions.insert(id.clone(), handle);
        id
    }

    /// The workspace's MCP servers, then the service's (`--mcp-config`), then the client's.
    pub(crate) fn session_mcp_servers(&self, workspace: &Workspace, client: &[McpServerConfig]) -> Vec<McpServerConfig> {
        let extra: Vec<McpServerConfig> = self.0.mcp_servers.iter().chain(client).cloned().collect();
        let (servers, warnings) = workspace.mcp_servers_with(&extra);
        for warning in warnings {
            tracing::warn!(cwd = %workspace.cwd.display(), "{warning}");
        }
        servers
    }

    /// The model a new session runs on. May read saved keys (the keychain) when nobody named one.
    pub(crate) async fn session_model(&self, asked: Option<String>, configured: Option<String>) -> Result<String, RpcError> {
        if let Some(model) = asked.or(configured) {
            return Ok(model);
        }
        let llm = self.0.llm.clone();
        tokio::task::spawn_blocking(move || her_core::models::session_model(&llm, None, None))
            .await
            .map_err(|e| RpcError::internal(e.to_string()))?
            .map_err(|e| errors::from_kernel(e, "new"))
    }

    /// The model a new session at `cwd` would start on, if there is one. Blocking I/O.
    pub fn default_model_at(&self, cwd: &Path) -> Option<String> {
        let workspace = Workspace::open(&self.0.home, cwd, &self.0.overrides).ok()?;
        her_core::models::default_model(&self.0.llm, workspace.config.model.as_deref())
    }

    /// Starts a session; `attach` receives its snapshot before any event. Returns the id.
    pub async fn create_session(&self, args: NewSession, attach: AttachRequest) -> Result<String, RpcError> {
        if args.cwd.as_ref().is_some_and(|cwd| !cwd.is_absolute()) {
            return Err(RpcError::invalid_params("cwd must be an absolute path"));
        }
        self.make_room().await?;
        let cwd = match args.cwd {
            Some(cwd) => cwd,
            None => {
                // Before a folder is made for it: only the user's own configuration applies here.
                if args.model.is_none() {
                    self.session_model(None, self.0.defaults.model.clone()).await?;
                }
                self.0
                    .home
                    .new_chat_dir()
                    .map_err(|e| RpcError::internal(format!("cannot make a folder for the session: {e}")))?
            }
        };
        let workspace = Workspace::open(&self.0.home, &cwd, &self.0.overrides).map_err(errors::from_runtime)?;
        let model = self.session_model(args.model, workspace.config.model.clone()).await?;
        if args.require_credentials
            && let Some((provider, _)) = model.split_once('/')
            && matches!(self.0.llm.auth_status(provider), Some(her_llm::AuthStatus::Missing(_)))
        {
            return Err(errors::no_credentials(&model, provider));
        }
        let no_project = self.0.home.has_no_project(&workspace.cwd);
        for warning in &workspace.warnings {
            tracing::warn!(cwd = %workspace.cwd.display(), "{warning}");
        }
        let store: Arc<dyn SessionStore> = self.0.store.clone();
        let mut deps = workspace
            .kernel_deps(self.0.llm.clone(), store, &args.extra_roots)
            .map_err(errors::from_runtime)?;
        deps.browser_pool = Some(self.0.browser_pool.clone());
        deps.docs = Some(self.0.docs.clone());
        deps.decks = Some(self.0.decks.clone());
        deps.words = Some(self.0.words.clone());
        deps.mcp_servers = self.session_mcp_servers(&workspace, &args.mcp_servers);
        let options = StartOptions {
            model,
            thinking: args.thinking.unwrap_or(workspace.config.thinking),
            permission_mode: args
                .permission_mode
                .unwrap_or(workspace.config.permission_mode),
            cwd: workspace.cwd.clone(),
            home: self.0.home.clone(),
            project_root: workspace.project_root.clone(),
            trusted: workspace.trusted,
            config: Arc::new(workspace.config),
            interactive_approvals: true,
            bot: None,
        };
        let kernel = Kernel::start(options, deps)
            .await
            .map_err(|e| errors::from_kernel(e, "new"))?;
        let now = list::now();
        let base = SummaryBase {
            created_at: now.clone(),
            preview: None,
            updated_at: now,
            no_project,
        };
        Ok(self.register(kernel, Vec::new(), Some(attach), base, None))
    }

    /// Loads a session from disk, or attaches when it is already loaded here.
    pub async fn resume_session(
        &self,
        id: &str,
        overrides: ResumeOverrides,
        attach: AttachRequest,
    ) -> Result<(), RpcError> {
        self.load_session(id, overrides, Some(attach)).await
    }

    /// Loads a session from disk unless it is loaded here, and attaches `attach`. Without one the
    /// session is there for what the service itself does, until nothing uses it.
    pub(crate) async fn load_session(
        &self,
        id: &str,
        overrides: ResumeOverrides,
        attach: Option<AttachRequest>,
    ) -> Result<(), RpcError> {
        let attached = async |hub: HubHandle, attach: Option<AttachRequest>| match attach {
            Some(attach) => hub.send(HubCmd::Attach(attach)).await,
            None => Ok(()),
        };
        if let Some(hub) = self.hub(id) {
            if !overrides.is_empty() {
                return Err(RpcError::invalid_params(
                    "the session is already loaded; change settings with session/op instead",
                ));
            }
            return attached(hub, attach).await;
        }
        let _loading = self.0.loading.lock().await;
        if let Some(hub) = self.hub(id) {
            return attached(hub, attach).await;
        }
        self.make_room().await?;
        let (header, location) = self
            .0
            .store
            .header(&SessionRef::Id(id.to_string()))
            .map_err(|e| errors::from_store(e, id))?;
        if header.id != id {
            return Err(errors::session_not_found(id));
        }
        let workspace = Workspace::open(&self.0.home, &header.cwd, &self.0.overrides)
            .map_err(errors::from_runtime)?;
        let no_project = self.0.home.has_no_project(&workspace.cwd);
        let store: Arc<dyn SessionStore> = self.0.store.clone();
        let mut deps = workspace
            .kernel_deps(self.0.llm.clone(), store, &[])
            .map_err(errors::from_runtime)?;
        deps.browser_pool = Some(self.0.browser_pool.clone());
        deps.docs = Some(self.0.docs.clone());
        deps.decks = Some(self.0.decks.clone());
        deps.words = Some(self.0.words.clone());
        deps.mcp_servers = self.session_mcp_servers(&workspace, &overrides.mcp_servers);
        deps.routines = header.bot.is_some().then(|| self.0.routines.clone() as Arc<dyn her_core::routine::RoutineHost>);
        deps.connectors = header.bot.is_some().then(|| self.0.connectors.clone() as Arc<dyn her_core::connect::ConnectorHost>);
        deps.mesh = header.bot.as_ref().map(|bot| self.bot_mesh(&bot.id));
        let base = match self.0.store.summary(&SessionRef::Path(location.clone())) {
            Ok(found) => SummaryBase {
                updated_at: last_active(&found),
                created_at: found.created_at,
                preview: found.first_message.as_deref().map(hub::preview),
                no_project,
            },
            Err(_) => SummaryBase {
                created_at: header.created_at.clone(),
                preview: None,
                updated_at: header.created_at.clone(),
                no_project,
            },
        };
        let options = ResumeOptions {
            session: SessionRef::Path(location),
            model: overrides.model,
            thinking: overrides.thinking,
            permission_mode: overrides.permission_mode,
            home: self.0.home.clone(),
            project_root: workspace.project_root.clone(),
            trusted: workspace.trusted,
            config: Arc::new(workspace.config),
            interactive_approvals: true,
            // A bot's conversation learns a name given while it was not loaded.
            bot_name: header.bot.as_ref().and_then(|bot| self.0.bots.name(&bot.id)),
        };
        let kernel = Kernel::resume(options, deps)
            .await
            .map_err(|e| errors::from_kernel(e, id))?;
        let transcript = kernel.transcript();
        if let Some(text) = header.bot.as_ref().and_then(|bot| self.0.routines.listing_of(&bot.id)) {
            kernel.set_routines(text);
        }
        self.register(kernel, transcript, attach, base, header.bot.map(|bot| bot.id));
        Ok(())
    }

    /// Starts following the session list on `conn`: the first page now, then every change.
    /// Blocking I/O.
    pub fn subscribe_sessions(
        &self,
        conn: &ConnHandle,
        params: &SessionListParams,
        respond: impl FnOnce(Result<SessionListResult, RpcError>) -> Vec<u8>,
    ) {
        self.0.feed.watch(conn);
        let result = self.list_sessions(params);
        let failed = result.is_err();
        self.0.feed.start(conn, respond(result));
        if failed {
            self.forget_model_watcher(conn.id);
            self.0.feed.cancel(conn.id);
        }
    }

    pub fn unsubscribe_sessions(&self, conn: &ConnHandle) {
        self.forget_model_watcher(conn.id);
        self.0.feed.cancel(conn.id);
    }

    /// Renames a session (`Op::SetTitle`) or archives it (`Op::SetArchived`). A loaded session
    /// records it through its kernel; for any other, the entry is appended to its log here.
    pub async fn set_metadata(&self, id: &str, op: Op) -> Result<(), RpcError> {
        let kind = match &op {
            Op::SetTitle { title } => EntryKind::Title {
                title: her_core::clean_title(title)
                    .ok_or_else(|| RpcError::invalid_params("the title is empty"))?,
            },
            Op::SetArchived { archived } => EntryKind::Archived {
                archived: *archived,
            },
            _ => return Err(RpcError::internal("not a metadata operation")),
        };
        // No resume can start while this holds, so an unloaded session stays unloaded.
        let _loading = self.0.loading.lock().await;
        if let Some(hub) = self.hub(id) {
            let (tx, rx) = oneshot::channel();
            hub.send(HubCmd::Meta { op, reply: tx }).await?;
            return rx.await.map_err(|_| errors::session_closed(id))?;
        }
        let server = self.clone();
        let id = id.to_string();
        tokio::task::spawn_blocking(move || {
            let reference = SessionRef::Id(id.clone());
            let (header, _) = server
                .0
                .store
                .header(&reference)
                .map_err(|e| errors::from_store(e, &id))?;
            if header.id != id {
                return Err(errors::session_not_found(&id));
            }
            server
                .0
                .store
                .append(&reference, vec![kind])
                .map_err(|e| errors::from_store(e, &id))?;
            let found = server
                .0
                .store
                .summary(&reference)
                .map_err(|e| errors::from_store(e, &id))?;
            server
                .0
                .feed
                .publish(vec![disk_summary(&server.0.home, &found)], Vec::new());
            Ok(())
        })
        .await
        .unwrap_or_else(|e| Err(RpcError::internal(e.to_string())))
    }

    pub(crate) fn loaded(&self, id: &str) -> Result<HubHandle, RpcError> {
        if let Some(hub) = self.hub(id) {
            return Ok(hub);
        }
        match self.0.store.header(&SessionRef::Id(id.to_string())) {
            Ok((header, _)) if header.id == id => Err(errors::session_not_loaded(id)),
            _ => Err(errors::session_not_found(id)),
        }
    }

    pub async fn attach(&self, id: &str, attach: AttachRequest) -> Result<(), RpcError> {
        self.loaded(id)?.send(HubCmd::Attach(attach)).await
    }

    pub async fn detach(
        &self,
        id: &str,
        conn: ConnHandle,
        respond: Option<DetachResponder>,
    ) -> Result<(), RpcError> {
        self.loaded(id)?
            .send(HubCmd::Detach { conn, respond })
            .await
    }

    /// Submits an operation; `respond` is queued on `conn` before any event the operation causes.
    pub async fn submit(
        &self,
        id: &str,
        conn: ConnHandle,
        op: Op,
        require_idle: bool,
        respond: OpResponder,
    ) -> Result<(), RpcError> {
        self.loaded(id)?
            .send(HubCmd::Op {
                conn,
                op,
                require_idle,
                respond,
            })
            .await
    }

    pub async fn history(
        &self,
        id: &str,
        before: String,
        limit: usize,
    ) -> Result<TranscriptPage, RpcError> {
        let (tx, rx) = oneshot::channel();
        self.loaded(id)?
            .send(HubCmd::History {
                before,
                limit,
                reply: tx,
            })
            .await?;
        rx.await
            .map_err(|_| errors::session_closed(id))?
            .ok_or_else(|| RpcError::invalid_params("unknown history cursor"))
    }

    /// What fills a loaded session's context window. The kernel keeps the report until the
    /// session changes, so asking is cheap.
    pub async fn context(&self, id: &str) -> Result<her_protocol::ContextReport, RpcError> {
        let (tx, rx) = oneshot::channel();
        self.loaded(id)?.send(HubCmd::Context { reply: tx }).await?;
        rx.await.ok().flatten().ok_or_else(|| errors::session_closed(id))
    }

    /// The whole transcript of a loaded session.
    pub async fn transcript(
        &self,
        id: &str,
    ) -> Result<(SessionInfo, Vec<TranscriptItem>), RpcError> {
        let (tx, rx) = oneshot::channel();
        self.loaded(id)?
            .send(HubCmd::Transcript { reply: tx })
            .await?;
        rx.await.map_err(|_| errors::session_closed(id))
    }

    /// Current settings of a loaded session.
    pub async fn info(&self, id: &str) -> Result<SessionInfo, RpcError> {
        let (tx, rx) = oneshot::channel();
        self.loaded(id)?.send(HubCmd::Info { reply: tx }).await?;
        rx.await.map_err(|_| errors::session_closed(id))
    }

    /// `skills/list`: a loaded session's skills and `/` commands, or what a session started in a
    /// directory would find. For a session, everything queued for an earlier attach has been
    /// sent when this returns.
    pub async fn list_skills(&self, params: SkillsListParams) -> Result<SkillsListResult, RpcError> {
        match (params.session_id, params.cwd) {
            (Some(id), None) => {
                let (tx, rx) = oneshot::channel();
                self.loaded(&id)?.send(HubCmd::Skills { reply: tx }).await?;
                let (found, enabled) = rx.await.map_err(|_| errors::session_closed(&id))?;
                tokio::task::spawn_blocking(move || skills::session_report(&found, enabled))
                    .await
                    .map_err(|e| RpcError::internal(e.to_string()))
            }
            (None, Some(cwd)) => {
                if !cwd.is_absolute() {
                    return Err(RpcError::invalid_params("cwd must be an absolute path"));
                }
                let server = self.clone();
                tokio::task::spawn_blocking(move || server.scan_skills(&cwd))
                    .await
                    .unwrap_or_else(|e| Err(RpcError::internal(e.to_string())))
            }
            (None, None) => {
                let server = self.clone();
                tokio::task::spawn_blocking(move || {
                    let chats = server.0.home.chats_dir();
                    std::fs::create_dir_all(&chats).map_err(|e| RpcError::internal(e.to_string()))?;
                    server.scan_skills(&chats)
                })
                .await
                .unwrap_or_else(|e| Err(RpcError::internal(e.to_string())))
            }
            _ => Err(RpcError::invalid_params("give either session_id or cwd")),
        }
    }

    /// What a session started in `cwd` would find, as `her skills --all --json` reports it.
    /// Blocking I/O.
    /// A loaded session's tools, or the built-in tools under the configuration of a session
    /// without a project.
    pub async fn list_tools(&self, params: ToolsListParams) -> Result<ToolsListResult, RpcError> {
        if let Some(id) = params.session_id {
            let (tx, rx) = oneshot::channel();
            self.loaded(&id)?.send(HubCmd::Tools { reply: tx }).await?;
            let tools = rx.await.map_err(|_| errors::session_closed(&id))?;
            return Ok(ToolsListResult { tools });
        }
        let server = self.clone();
        tokio::task::spawn_blocking(move || {
            let chats = server.0.home.chats_dir();
            std::fs::create_dir_all(&chats).map_err(|e| RpcError::internal(e.to_string()))?;
            let workspace = Workspace::open(&server.0.home, &chats, &server.0.overrides).map_err(errors::from_runtime)?;
            Ok(ToolsListResult { tools: her_core::tool_list::builtin(&workspace.config) })
        })
        .await
        .unwrap_or_else(|e| Err(RpcError::internal(e.to_string())))
    }

    fn scan_skills(&self, cwd: &Path) -> Result<SkillsListResult, RpcError> {
        let workspace = Workspace::open(&self.0.home, cwd, &self.0.overrides).map_err(errors::from_runtime)?;
        let window = her_core::models::default_model(&self.0.llm, workspace.config.model.as_deref())
            .and_then(|model| self.0.llm.resolve(&model).ok())
            .map_or(u64::MAX, |resolved| u64::from(resolved.model.context_window));
        let config = &workspace.config;
        let sources = skills::Sources::new(&self.0.home, &workspace.cwd, workspace.project_root.as_deref(), &config.skills);
        Ok(skills::Scan::new(&sources, &config.enabled_tools, window).report(true))
    }

    /// Closes a loaded session and waits until its log is closed.
    pub async fn close_session(&self, id: &str) -> Result<(), RpcError> {
        let hub = self.loaded(id)?;
        let (tx, rx) = oneshot::channel();
        hub.send(HubCmd::Close { reply: Some(tx) }).await?;
        let _ = rx.await;
        self.docs_forget(id).await;
        Ok(())
    }

    /// Sessions on disk, most recent first, with their state in this server. Blocking I/O.
    pub fn list_sessions(&self, params: &SessionListParams) -> Result<SessionListResult, RpcError> {
        let offset: usize = params
            .cursor
            .as_deref()
            .and_then(|c| c.parse().ok())
            .unwrap_or(0);
        let limit = params.limit.unwrap_or(50).clamp(1, 500) as usize;
        let within = params.cwd.as_deref().map(project_dir);
        let found = self
            .0
            .store
            .list(offset + limit + 1, within.as_deref())
            .map_err(|e| RpcError::internal(e.to_string()))?;
        let hubs = self.0.sessions.lock().clone();
        let sessions = found
            .iter()
            .skip(offset)
            .take(limit)
            .map(|s| {
                let on_disk = disk_summary(&self.0.home, s);
                match hubs.get(&s.id) {
                    // A loaded session's state is the hub's; the log may have been written since.
                    Some(hub) => {
                        let mut live = hub.shared.summary.lock().clone();
                        live.updated_at = live.updated_at.max(on_disk.updated_at);
                        live.busy = hub.shared.busy.load(Ordering::Relaxed);
                        live.attached_clients = hub.shared.attached.load(Ordering::Relaxed);
                        live
                    }
                    None => on_disk,
                }
            })
            .collect();
        Ok(SessionListResult {
            sessions,
            next_cursor: (found.len() > offset + limit).then(|| (offset + limit).to_string()),
        })
    }

    /// Moves the log of an unloaded session to `~/.her/trash/sessions/`. Blocking I/O.
    pub fn delete_session(&self, id: &str) -> Result<(), RpcError> {
        self.trash_log(id)?;
        self.0.feed.publish(Vec::new(), vec![id.to_string()]);
        Ok(())
    }

    /// [`Self::delete_session`] without telling the session list: a bot's conversation is not
    /// in it. Blocking I/O.
    pub(crate) fn trash_log(&self, id: &str) -> Result<(), RpcError> {
        if self.is_loaded(id) {
            return Err(errors::session_busy(format!(
                "session {id} is loaded; close it first"
            )));
        }
        let (header, _) = self
            .0
            .store
            .header(&SessionRef::Id(id.to_string()))
            .map_err(|e| errors::from_store(e, id))?;
        if header.id != id {
            return Err(errors::session_not_found(id));
        }
        let trash = self.0.home.root().join("trash").join("sessions");
        self.0.store.move_to_trash(&SessionRef::Id(id.to_string()), &trash).map_err(|e| errors::from_store(e, id))?;
        self.forget_browser_session(id);
        Ok(())
    }

    pub(crate) fn store(&self) -> &Arc<JsonlStore> {
        &self.0.store
    }

    /// Reads part of a saved tool output (`her://tool-output/<session>/<call>`). Blocking I/O.
    pub fn read_resource(
        &self,
        params: &ResourceReadParams,
    ) -> Result<ResourceReadResult, RpcError> {
        let rest = params
            .uri
            .strip_prefix("her://tool-output/")
            .ok_or_else(|| RpcError::invalid_params("unsupported resource uri"))?;
        let (session, call) = rest
            .split_once('/')
            .filter(|(s, c)| !s.is_empty() && !c.is_empty())
            .ok_or_else(|| {
                RpcError::invalid_params("expected her://tool-output/<session>/<call>")
            })?;
        if !session.chars().all(|c| c.is_ascii_hexdigit() || c == '-') {
            return Err(RpcError::invalid_params("invalid session id"));
        }
        let name: String = call
            .chars()
            .map(|c| {
                if c.is_ascii_alphanumeric() || c == '-' || c == '_' {
                    c
                } else {
                    '_'
                }
            })
            .collect();
        // Saved outputs live in the session's data directory, next to its log.
        let not_found = || {
            RpcError::with_kind(
                her_rpc::code::INVALID_PARAMS,
                "resource_not_found",
                "no saved output for this call",
            )
        };
        let (header, location) = self
            .0
            .store
            .header(&SessionRef::Id(session.to_string()))
            .map_err(|_| not_found())?;
        if header.id != session {
            return Err(not_found());
        }
        let (data_dir, _) = HerHome::session_data_dir(&location, session);
        let path = data_dir.join("tool-output").join(format!("{name}.txt"));
        let mut file = std::fs::File::open(&path).map_err(|_| {
            RpcError::with_kind(
                her_rpc::code::INVALID_PARAMS,
                "resource_not_found",
                "no saved output for this call",
            )
        })?;
        let total = file.metadata().map(|m| m.len()).unwrap_or(0);
        let offset = params.offset.min(total);
        let limit = params
            .limit
            .unwrap_or(RESOURCE_DEFAULT_LIMIT)
            .min(RESOURCE_MAX_LIMIT);
        let mut buf = Vec::with_capacity(limit.min(total - offset) as usize);
        file.seek(std::io::SeekFrom::Start(offset))
            .and_then(|_| file.take(limit).read_to_end(&mut buf))
            .map_err(|e| RpcError::internal(e.to_string()))?;
        let end = offset + buf.len() as u64;
        Ok(ResourceReadResult {
            text: String::from_utf8_lossy(&buf).into_owned(),
            offset,
            eof: end >= total,
            total_bytes: total,
        })
    }

}

/// The project a directory belongs to (its git checkout), or the directory itself.
/// How often the service checks whether it has just gone quiet.
const UPKEEP_EVERY: Duration = Duration::from_secs(2);

/// Returns free memory to the system when the service goes quiet: after its last running task
/// ends, or after sessions close. The allocator would otherwise keep the pages of the peak.
fn spawn_memory_upkeep(inner: &Arc<Inner>) {
    let Ok(runtime) = tokio::runtime::Handle::try_current() else {
        return;
    };
    let weak = Arc::downgrade(inner);
    runtime.spawn(async move {
        let mut tick = tokio::time::interval(UPKEEP_EVERY);
        tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
        let (mut was_busy, mut had, mut closed) = (false, 0, 0);
        loop {
            tick.tick().await;
            let Some(inner) = weak.upgrade() else {
                return;
            };
            let server = Server(inner);
            let (busy, sessions) = (server.busy(), server.loaded_sessions());
            let now_closed = server.0.closed_sessions.load(Ordering::Relaxed);
            let quieter = (was_busy && !busy) || sessions < had || (!busy && now_closed != closed);
            if quieter { closed = now_closed; }
            (was_busy, had) = (busy, sessions);
            drop(server);
            if quieter {
                let started = std::time::Instant::now();
                let released = tokio::task::spawn_blocking(crate::memory::release_free_memory)
                    .await
                    .unwrap_or(0);
                tracing::debug!(released, sessions, elapsed_ms = started.elapsed().as_millis() as u64, "returned free memory to the system");
            }
        }
    });
}

fn project_dir(dir: &Path) -> PathBuf {
    let dir = std::fs::canonicalize(dir).unwrap_or_else(|_| dir.to_path_buf());
    instructions::find_project_root(&dir, &[".git".to_string()]).unwrap_or(dir)
}

/// When the session last ran a task, else when it was created: not when its log was last
/// written, which opening, renaming or archiving it also does.
fn last_active(s: &her_core::store::SessionSummary) -> String {
    s.last_task_at
        .map(list::rfc3339)
        .unwrap_or_else(|| s.created_at.clone())
}

/// The list row of a session that is not loaded here, from its log.
fn disk_summary(home: &HerHome, s: &her_core::store::SessionSummary) -> SessionSummary {
    SessionSummary {
        session_id: s.id.clone(),
        no_project: home.has_no_project(&s.cwd),
        cwd: s.cwd.clone(),
        created_at: s.created_at.clone(),
        updated_at: last_active(s),
        preview: s.first_message.as_deref().map(hub::preview),
        title: s.title.clone(),
        state: if s.last_task == Some(TaskStatus::Failed) {
            SessionState::Failed
        } else {
            SessionState::Idle
        },
        model: s.model.clone(),
        archived: s.archived,
        task_started_at: None,
        loaded: false,
        busy: false,
        attached_clients: 0,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_core::store::SessionHeader;

    #[test]
    fn saved_output_is_read_from_the_session_data_directory_and_trashed_with_the_log() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        let server = Server::new(ServerOptions {
            home: home.clone(),
            cwd: dir.path().to_path_buf(),
            overrides: Vec::new(),
            mcp_servers: Vec::new(),
            doc_worker: her_docs::Launcher::Thread,
        })
        .unwrap();
        let id = "0193f0c2-7c1e-7d3a-9b1f-4c2d8e6a5b10".to_string();
        let created = server
            .0
            .store
            .create(&SessionHeader::new(id.clone(), dir.path().to_path_buf()))
            .unwrap();
        let location = created.location.clone();
        drop(created);
        // The writer creates the log in the background.
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
        while !location.metadata().is_ok_and(|m| m.len() > 0) && std::time::Instant::now() < deadline {
            std::thread::sleep(std::time::Duration::from_millis(5));
        }
        let (data_dir, _) = HerHome::session_data_dir(&location, &id);
        std::fs::create_dir_all(data_dir.join("tool-output")).unwrap();
        std::fs::write(
            data_dir.join("tool-output").join("call_1.txt"),
            "line 1\nline 2\n",
        )
        .unwrap();

        let read = |uri: String| {
            server.read_resource(&ResourceReadParams {
                uri,
                offset: 7,
                limit: None,
            })
        };
        let result = read(format!("her://tool-output/{id}/call_1")).unwrap();
        assert_eq!(result.text, "line 2\n");
        assert!(result.eof);
        assert!(read(format!("her://tool-output/{id}/call_2")).is_err());

        // The writer releases the log's lock in the background.
        std::thread::sleep(std::time::Duration::from_millis(200));
        server.delete_session(&id).unwrap();
        assert!(!location.exists() && !data_dir.exists());
        let trash = home.root().join("trash").join("sessions");
        assert!(trash.join(location.file_name().unwrap()).exists());
        assert!(
            trash
                .join(data_dir.file_name().unwrap())
                .join("tool-output/call_1.txt")
                .exists()
        );
    }
}
