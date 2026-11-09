//! The kernel handle: operations in, events out. A background task owns the session loop.

use her_llm::Message;
use her_llm::Model;
use her_llm::ModelRegistry;
use her_llm::ResolveError;
use her_llm::ThinkingLevel;
use her_llm::UsageTotals;
use her_llm::UserContent;
use her_protocol::CommandRule;
use her_protocol::ErrorInfo;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::Op;
use her_protocol::PROTOCOL_VERSION;
use her_protocol::PermissionMode;
use her_protocol::PromptMode;
use her_protocol::api::SessionInfo;
use her_protocol::api::TaskOutcome;
use her_protocol::api::TranscriptItem;
use her_tools::Environment;
use her_tools::FileTracker;
use her_tools::PathLocks;
use her_tools::Tool;
use her_tools::ToolRegistry;
use parking_lot::Mutex;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::OnceLock;
use std::sync::Weak;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::AtomicU64;
use std::time::Duration;
use tokio::sync::mpsc;
use tokio::sync::oneshot;
use tokio::sync::watch;
use tokio::task::JoinHandle;

use crate::agents;
use crate::agents::AgentTree;
use crate::agents::ChildDeps;
use crate::deferred::AgentRole;
use crate::approval::ApprovalPolicy;
use crate::bot;
use crate::bot::BotIdentity;
use crate::cards::Cards;
use crate::connect::ConnectorHost;
use crate::connect::ConnectorLink;
use crate::browser::BrowserHandle;
use crate::desktop::DesktopHandle;
use crate::config::Config;
use crate::context;
use crate::emitter::Emitter;
use crate::history::History;
use crate::hooks::Hooks;
use crate::instructions;
use crate::mcp;
use crate::mcp::McpManager;
use crate::mcp::tools::Shown;
use crate::paths::HerHome;
use crate::prompt;
use crate::prompt::PromptInputs;
use crate::prompt::PromptProfile;
use crate::routine::RoutineHost;
use crate::routine::RoutineLink;
use crate::routine::RoutineRun;
use crate::routine::RunOutcome;
use crate::session::Internal;
use crate::session::BotNames;
use crate::session::Session;
use crate::session::SessionState;
use crate::skills;
use crate::skills::SkillRef;
use crate::skills::SkillState;
use crate::store::BotRef;
use crate::store::Entry;
use crate::store::EntryKind;
use crate::store::SessionHeader;
use crate::store::SessionRef;
use crate::store::SessionStore;
use crate::store::SessionWriter;
use crate::store::WriterClosed;
use crate::store::StoreError;
use crate::store::TaskStatus;
use crate::system::SystemCell;
use crate::system::SystemSettings;
use crate::task;

const OP_CHANNEL_CAPACITY: usize = 64;

pub struct KernelDeps {
    pub llm: Arc<ModelRegistry>,
    pub tools: Arc<ToolRegistry>,
    pub env: Arc<dyn Environment>,
    pub store: Arc<dyn SessionStore>,
    /// Extra hooks; the approval policy is always added last.
    pub hooks: Vec<Arc<dyn Hooks>>,
    /// MCP servers for the session, merged and validated by the front end. Started with the
    /// session; empty means no MCP.
    pub mcp_servers: Vec<her_protocol::McpServerConfig>,
    /// The process's browsers, shared by its sessions (browser-design §6.1). `None` leaves
    /// the browser tools out, whatever `[browser] enabled` says.
    pub browser_pool: Option<Arc<her_browser::BrowserPool>>,
    /// The service's documents, shared by its sessions (document-editor-design §5.2). `None`
    /// leaves the document tools out.
    pub docs: Option<Arc<her_docs::DocStore>>,
    /// The service's presentations (presentation-kernel-authority): the deck tools read an open
    /// deck's entry, unsaved edits included. `None` reads the files only.
    pub decks: Option<Arc<her_docs::deck::DeckStore>>,
    /// The service's Word documents (word-design §4): the docx tools work on their entries.
    /// `None` leaves them unable to read or change a document.
    pub words: Option<Arc<her_docs::word::WordStore>>,
    /// The service's routines (routine-design §4.3), for a bot's session. Without it the bot's
    /// `manage_routine` says they are not available.
    pub routines: Option<Arc<dyn RoutineHost>>,
    /// The service's connectors, for Her's own conversation: it gets the `connectors` group and
    /// may ask the owner on a secure card (docs/her-secure-cards.md §2.2).
    pub connectors: Option<Arc<dyn ConnectorHost>>,
}

pub struct StartOptions {
    pub model: String,
    pub thinking: ThinkingLevel,
    pub permission_mode: PermissionMode,
    pub cwd: PathBuf,
    pub home: HerHome,
    pub config: Arc<Config>,
    pub project_root: Option<PathBuf>,
    pub trusted: bool,
    /// Whether a front end can answer approval requests. Without one, "ask" means "deny".
    pub interactive_approvals: bool,
    /// A bot's session (bot-design §2.2): its header names the bot, its prompt is a bot's and
    /// it gets `send_to_user`.
    pub bot: Option<BotIdentity>,
}

pub struct ResumeOptions {
    pub session: SessionRef,
    /// Overrides; `None` keeps the session's own settings.
    pub model: Option<String>,
    pub thinking: Option<ThinkingLevel>,
    pub permission_mode: Option<PermissionMode>,
    pub home: HerHome,
    pub config: Arc<Config>,
    pub project_root: Option<PathBuf>,
    pub trusted: bool,
    pub interactive_approvals: bool,
    /// A bot's session: the bot's name now, when it may have changed (bot-design §2.4).
    pub bot_name: Option<String>,
}

#[derive(Debug, thiserror::Error)]
pub enum KernelError {
    /// Nobody named a model and no provider is connected.
    #[error("no model is set up")]
    NoModel,
    #[error(transparent)]
    Model(#[from] ResolveError),
    #[error(transparent)]
    Store(#[from] StoreError),
    #[error("{0}")]
    Other(String),
}

#[derive(Debug, thiserror::Error)]
#[error("the session is closed")]
pub struct KernelClosed;

#[derive(Debug, Clone, PartialEq)]
pub enum TaskEnd {
    Completed { last_message: Option<String> },
    Failed(ErrorInfo),
    Interrupted,
}

#[derive(Debug)]
pub struct TaskResult {
    pub end: TaskEnd,
    pub events: Vec<Event>,
}

pub struct Kernel {
    ops: mpsc::Sender<Op>,
    events: mpsc::UnboundedReceiver<Box<Event>>,
    session_id: String,
    session_file: PathBuf,
    submission: JoinHandle<()>,
    log_closed: WriterClosed,
    /// Weak, so the event channel still closes when the session loop ends.
    session: Weak<Session>,
    /// Messages from inside the process: reports, permissions for a sub-agent.
    pub(crate) internal: mpsc::UnboundedSender<Box<Internal>>,
    /// Sub-agents of an earlier run of a resumed session, until taken.
    restored_agents: Vec<agents::RestoredAgent>,
}

struct Launch {
    id: String,
    /// The main session: this one, or a sub-agent's parent. Its window shows the browser.
    root_id: String,
    location: PathBuf,
    writer: SessionWriter,
    home: HerHome,
    trusted: bool,
    interactive_approvals: bool,
    config: Arc<Config>,
    deps: KernelDeps,
    state: SessionState,
    agents: Option<Arc<AgentTree>>,
    sub_agent: bool,
    role: AgentRole,
    main: Option<Weak<Session>>,
    approval_line: Arc<tokio::sync::Mutex<()>>,
    locks: Arc<PathLocks>,
    mcp: Option<Arc<McpManager>>,
    /// The tree's browser and desktop settings (connectors-entry-design §9.4).
    system: Arc<SystemCell>,
}

fn launch(launch: Launch) -> (Kernel, Arc<Session>) {
    let (event_tx, event_rx) = mpsc::unbounded_channel();
    let (op_tx, op_rx) = mpsc::channel(OP_CHANNEL_CAPACITY);
    let (internal_tx, internal_rx) = mpsc::unbounded_channel();
    let mut hooks = launch.deps.hooks;
    hooks.push(Arc::new(ApprovalPolicy));
    let log_closed = launch.writer.closed();
    let (session_dir, temporary) = HerHome::session_data_dir(&launch.location, &launch.id);
    let shown = Arc::new(Mutex::new(Shown::default()));
    let settings = launch.system.get();
    let pool = launch.deps.browser_pool.clone();
    let embedded = pool.as_ref().is_some_and(|p| p.has_host());
    let role = launch.role;
    let connectors = launch.deps.connectors.is_some();
    let deferred: crate::deferred::DeferredCell =
        Arc::new(Mutex::new(Arc::new(crate::deferred::Deferred::of(&settings, pool.is_some(), embedded, launch.deps.docs.is_some(), role, connectors))));
    let base_tools = mcp::base_tools(&launch.deps.tools, launch.mcp.as_ref(), &deferred, &shown);
    let skill_roots =
        crate::protected::skill_locations(&launch.config.skills, &launch.home, launch.deps.env.cwd());
    let tool_state = Arc::new(her_tools::SessionState::default());
    if let Some(tree) = &launch.agents {
        tool_state.insert(tree.clone());
    }
    tool_state.insert(Arc::new(crate::web_search::SearchHandle { llm: launch.deps.llm.clone() }));
    // A browser handle whenever there is a pool: the browsers may be turned on later (§9.4).
    if let Some(pool) = &pool {
        tool_state.insert(Arc::new(BrowserHandle::new(
            pool.clone(),
            settings.browser.clone(),
            embedded,
            session_dir.join("downloads"),
            launch.deps.env.workspace_roots().to_vec(),
            launch.state.site_grants.clone(),
            launch.root_id.clone(),
        )));
    }
    if settings.desktop.on(embedded) {
        tool_state.insert(Arc::new(DesktopHandle::new(settings.desktop.clone(), launch.root_id.clone())));
    }
    let emitter = Arc::new(Emitter::new(event_tx));
    if let Some(host) = &launch.deps.connectors {
        tool_state.insert(Arc::new(ConnectorLink { host: host.clone() }));
        tool_state.insert(Arc::new(Cards::new(emitter.clone(), internal_tx.clone())));
    }
    if let Some(decks) = &launch.deps.decks {
        tool_state.insert(Arc::new(crate::deck::DecksHandle { store: decks.clone(), session: launch.root_id.clone() }));
    }
    if let Some(words) = &launch.deps.words {
        tool_state.insert(Arc::new(crate::word::WordsHandle { store: words.clone(), session: launch.root_id.clone() }));
    }
    if let Some(store) = &launch.deps.docs {
        let weak = Arc::downgrade(&emitter);
        tool_state.insert(Arc::new(crate::docs::DocsHandle {
            store: store.clone(),
            view_session: launch.root_id.clone(),
            opened: Box::new(move |info| {
                if let Some(emitter) = weak.upgrade() {
                    emitter.emit(EventMsg::DocOpened { info });
                }
            }),
        }));
    }
    let own_folder = launch.home.private_folder(&crate::protected::canonical_lossy(launch.deps.env.cwd()));
    let session = Arc::new(Session {
        id: launch.id.clone(),
        llm: launch.deps.llm,
        tools: Mutex::new(base_tools.clone()),
        core_tools: launch.deps.tools.clone(),
        base_tools: Mutex::new(base_tools),
        // The first request after a start or a resume brings the prompt up to the tools.
        system_applied: AtomicU64::new(u64::MAX),
        system: launch.system,
        browser_pool: pool,
        root_id: launch.root_id.clone(),
        mcp: launch.mcp,
        mcp_applied: AtomicU64::new(u64::MAX),
        mcp_settled: AtomicBool::new(false),
        mcp_shown: shown,
        deferred,
        env: launch.deps.env,
        hooks,
        emitter,
        writer: launch.writer,
        tool_gate: tokio::sync::RwLock::new(()),
        tool_slots: tokio::sync::Semaphore::new(launch.config.max_parallel_tools),
        files: Arc::new(FileTracker::new()),
        output_dir: session_dir.join("tool-output"),
        temporary_data: temporary.then_some(session_dir),
        fixed_tokens: Mutex::new(None),
        context_report: Mutex::new(None),
        request_estimates: Mutex::new(Vec::new()),
        data_dir: crate::protected::canonical_data_dir(launch.home.root()),
        own_folder,
        skill_roots,
        trusted: launch.trusted,
        interactive_approvals: launch.interactive_approvals,
        locks: launch.locks,
        agents: launch.agents,
        sub_agent: launch.sub_agent,
        agent_role: role,
        main: launch.main,
        approval_line: launch.approval_line,
        tool_state,
        input_seq: watch::channel(0).0,
        config: launch.config,
        state: Mutex::new(launch.state),
    });
    // A browser batch judges its later steps by the session's approval policy (§12.1).
    if let Some(browser) = session.tool_state.get::<BrowserHandle>() {
        let weak = Arc::downgrade(&session);
        browser.set_judge(Arc::new(move |authorities| {
            let weak = weak.clone();
            Box::pin(async move {
                match weak.upgrade() {
                    Some(session) => crate::site_policy::evaluate(&authorities, &session.view()).await,
                    None => crate::hooks::Gate::Deny("the session has ended".into()),
                }
            })
        }));
    }
    let handle = tokio::spawn(submission_loop(session.clone(), op_rx, internal_rx));
    let kernel = Kernel {
        ops: op_tx,
        events: event_rx,
        session_id: launch.id,
        session_file: launch.location,
        submission: handle,
        log_closed,
        session: Arc::downgrade(&session),
        internal: internal_tx,
        restored_agents: Vec::new(),
    };
    (kernel, session)
}

/// What a main session is among agents: it has a tree of sub-agents, or they are turned off.
fn main_role(tree: bool) -> AgentRole {
    if tree { AgentRole::Main } else { AgentRole::None }
}

/// The sub-agents of a main session: adds `spawn_agent` and `send_message` to its tools.
/// Sub-agents start from the tools as they were before, without these two (`start_child` gives
/// them their own `send_message`), and share the MCP servers.
/// A bot's main session has the two without `wait` (bot-turn-design §5.1).
fn agent_tree(
    deps: &mut KernelDeps,
    home: &HerHome,
    config: &Arc<Config>,
    trusted: bool,
    mcp: Option<Arc<McpManager>>,
    bot: bool,
) -> Option<Arc<AgentTree>> {
    if !config.agents_enabled {
        return None;
    }
    let tree = AgentTree::new(ChildDeps {
        llm: deps.llm.clone(),
        tools: deps.tools.clone(),
        env: deps.env.clone(),
        store: deps.store.clone(),
        hooks: deps.hooks.clone(),
        home: home.clone(),
        config: config.clone(),
        trusted,
        mcp,
        browser_pool: deps.browser_pool.clone(),
        docs: deps.docs.clone(),
        decks: deps.decks.clone(),
        words: deps.words.clone(),
    });
    deps.tools = if bot { extended(&deps.tools, "bot-agents", agents::bot_agent_tools) } else { with_agent_tools(&deps.tools) };
    Some(tree)
}

/// `tools` plus `spawn_agent` and `send_message`, built once for each registry: the two tools
/// hold no session, so main sessions with the same tools share the result.
fn with_agent_tools(tools: &Arc<ToolRegistry>) -> Arc<ToolRegistry> {
    extended(tools, "agents", agents::agent_tools)
}

/// `tools` plus the tools `make` returns, built once for each registry and `kind`: these tools
/// hold no session, so sessions with the same tools share the result.
fn extended(tools: &Arc<ToolRegistry>, kind: &'static str, make: fn() -> Vec<Arc<dyn Tool>>) -> Arc<ToolRegistry> {
    type Built = Vec<(Weak<ToolRegistry>, &'static str, Arc<ToolRegistry>)>;
    static BUILT: OnceLock<Mutex<Built>> = OnceLock::new();
    let mut built = BUILT.get_or_init(Default::default).lock();
    built.retain(|(base, _, _)| base.strong_count() > 0);
    if let Some((_, _, with)) = built
        .iter()
        .find(|(base, k, _)| *k == kind && std::ptr::eq(base.as_ptr(), Arc::as_ptr(tools)))
    {
        return with.clone();
    }
    let mut with = (**tools).clone();
    for tool in make() {
        // A custom registry may already define these names; it wins.
        let _ = with.register(tool);
    }
    let with = Arc::new(with);
    built.push((Arc::downgrade(tools), kind, with.clone()));
    with
}

/// What a sub-agent's session starts from: the main session's current settings.
pub(crate) struct ChildLaunch {
    pub(crate) header: SessionHeader,
    /// Started for a bot: it can tell the owner how the work goes (agent-messaging-design §17).
    pub(crate) bot: bool,
    /// The main session's prompt as sent, its fixed part and its MCP part.
    pub(crate) system_prompt: Arc<str>,
    pub(crate) base_prompt: Arc<str>,
    pub(crate) mcp_section: Option<String>,
    pub(crate) mcp: Option<Arc<McpManager>>,
    /// The main session's skills, which its system prompt lists.
    pub(crate) skills: Arc<[SkillRef]>,
    pub(crate) model: Arc<Model>,
    pub(crate) thinking: ThinkingLevel,
    pub(crate) permission_mode: PermissionMode,
    pub(crate) session_rules: Vec<CommandRule>,
    pub(crate) site_grants: Vec<her_protocol::SiteGrant>,
    pub(crate) deps: KernelDeps,
    pub(crate) home: HerHome,
    pub(crate) config: Arc<Config>,
    pub(crate) trusted: bool,
    /// The main session's own: where its questions go, or that nobody answers them.
    pub(crate) interactive_approvals: bool,
    pub(crate) main: Weak<Session>,
    pub(crate) approval_line: Arc<tokio::sync::Mutex<()>>,
    pub(crate) locks: Arc<PathLocks>,
    /// The main session's browser and desktop settings, which the sub-agent follows.
    pub(crate) system: Arc<SystemCell>,
}

/// Starts a sub-agent's session: an ordinary session with its own log, whose questions go to
/// the main session's user.
pub(crate) fn start_child(mut child: ChildLaunch) -> Result<Kernel, KernelError> {
    // A routine's run is on its own: no other agent to talk to, nor one that knows of it.
    let run = child.header.agent.as_ref().is_some_and(|agent| agent.routine.is_some());
    if !run {
        child.deps.tools = if child.bot {
            extended(&child.deps.tools, "bot-sub-agent", agents::bot_sub_agent_tools)
        } else {
            extended(&child.deps.tools, "sub-agent", agents::sub_agent_tools)
        };
    }
    let created = child.deps.store.create(&child.header)?;
    let model_ref = child.model.reference();
    let context_window = u64::from(child.model.context_window);
    let mut state = SessionState::new(
        History::default(),
        child.system_prompt.clone(),
        child.model,
        child.thinking,
        child.permission_mode,
    );
    state.session_rules = child.session_rules;
    state.site_grants = child.site_grants;
    state.base_prompt = child.base_prompt;
    state.mcp_section = child.mcp_section;
    state.skills = child.skills.clone();
    let root_id = child.header.agent.as_ref().map_or_else(|| child.header.id.clone(), |a| a.root_id.clone());
    let (kernel, session) = launch(Launch {
        id: child.header.id.clone(),
        root_id,
        location: created.location.clone(),
        writer: created.writer,
        home: child.home,
        trusted: child.trusted,
        interactive_approvals: child.interactive_approvals,
        config: child.config,
        deps: child.deps,
        state,
        agents: None,
        sub_agent: true,
        role: if run { AgentRole::None } else { AgentRole::SubAgent },
        main: Some(child.main),
        approval_line: child.approval_line,
        locks: child.locks,
        mcp: child.mcp,
        system: child.system,
    });
    // The main session has waited for the servers already: their tools are in from the start.
    session.refresh_mcp();
    session.record_system_prompt();
    session.record(EntryKind::Settings {
        model: Some(model_ref.clone()),
        thinking: Some(child.thinking),
        permission_mode: Some(child.permission_mode),
    });
    // The task brief comes next: from it on, the log is on disk while the agent works, not only
    // once its task ends.
    session.writer.settle();
    session.emit(EventMsg::SessionStarted {
        session_id: child.header.id.clone(),
        protocol_version: PROTOCOL_VERSION,
        model: model_ref,
        cwd: child.header.cwd.clone(),
        resumed: false,
        session_file: created.location,
        thinking: child.thinking,
        permission_mode: child.permission_mode,
        context_window,
        usage: UsageTotals::default(),
        context: Some(session.budget().usage),
    });
    Ok(kernel)
}

/// A bot's session reaches its routines through the service (routine-design §4.3).
fn link_routines(session: &Session, host: Option<Arc<dyn RoutineHost>>, bot: Option<String>) {
    if let (Some(host), Some(bot)) = (host, bot) {
        session.tool_state.insert(Arc::new(RoutineLink { host, bot }));
    }
}

/// The MCP servers of a main session, or `None` without any. Her's own conversation has them
/// even without any yet: it takes up a connector the owner connects on a card. Not started yet:
/// status events need the session's emitter.
fn mcp_manager(deps: &KernelDeps, config: &Config, project_root: Option<PathBuf>) -> Option<Arc<McpManager>> {
    let grows = deps.connectors.is_some();
    if deps.mcp_servers.is_empty() && !grows {
        return None;
    }
    // Only remote servers need HTTP; local-only setups skip creating the client.
    let http = (grows || deps.mcp_servers.iter().any(|s| s.is_enabled() && s.url.is_some()))
        .then(|| deps.llm.http_client().ok())
        .flatten();
    McpManager::new(
        deps.mcp_servers.clone(),
        &config.mcp,
        http,
        deps.env.workspace_roots().to_vec(),
        deps.env.cwd().to_path_buf(),
        project_root,
        grows,
    )
}

/// A new session's system prompt, with the skills it lists and warnings for the user.
struct BuiltPrompt {
    text: String,
    /// A bot's session: its sub-agents' prompt, a session's built from the same inputs.
    sub_agent: Option<String>,
    skills: Vec<SkillRef>,
    warnings: Vec<String>,
}

/// `context_window` sizes the skill catalog.
/// The deferred built-in tools a session starts with, for the catalog in its first prompt.
fn starting_deferred(deps: &KernelDeps, system: &SystemCell, tree: bool) -> crate::deferred::Deferred {
    let embedded = deps.browser_pool.as_ref().is_some_and(|p| p.has_host());
    crate::deferred::Deferred::of(&system.get(), deps.browser_pool.is_some(), embedded, deps.docs.is_some(), main_role(tree), deps.connectors.is_some())
}

/// The tools a session starts with, before MCP: what its prompt describes. The browsers' and
/// computer use's are deferred, and their guidelines come with their definitions.
fn starting_tools(deps: &KernelDeps, _system: &SystemCell) -> Arc<ToolRegistry> {
    deps.tools.clone()
}

fn build_prompt(
    home: &HerHome,
    config: &Config,
    deps: &KernelDeps,
    tools: &ToolRegistry,
    project_root: Option<&Path>,
    context_window: u64,
    profile: &PromptProfile,
) -> Result<BuiltPrompt, KernelError> {
    let cwd = deps.env.cwd().to_path_buf();
    let (files, mut warnings) = instructions::discover(home, &cwd, project_root, &config.instruction_files);
    let sources = skills::Sources::new(home, &cwd, project_root, &config.skills);
    let scan = skills::Scan::new(&sources, &tools.names(), context_window);
    for shadowed in scan.set.entries.iter().filter(|e| e.state == SkillState::Shadowed) {
        let path = shadowed.path.display();
        tracing::info!(skill = %shadowed.name, %path, by = ?shadowed.detail, "skill shadowed");
    }
    warnings.extend(scan.warnings());
    let git_branch = project_root.and_then(instructions::git_branch);
    let date = prompt::date_text(chrono::Local::now().date_naive());
    let build = |profile: &PromptProfile| {
        prompt::build_system_prompt(&PromptInputs {
            profile,
            config,
            tools,
            env: deps.env.as_ref(),
            instructions: &files,
            skills: &scan.catalog.text,
            project_root,
            git_branch: git_branch.clone(),
            date: date.clone(),
            // The desktop app being connected, as for the browser's and computer use's defaults.
            in_app: deps.browser_pool.as_ref().is_some_and(|p| p.has_host()),
        })
        .map_err(|e| KernelError::Other(format!("cannot build the system prompt: {e}")))
    };
    let text = build(profile)?;
    let sub_agent = match profile {
        PromptProfile::Bot { .. } => Some(build(&PromptProfile::Session)?),
        PromptProfile::Session => None,
    };
    Ok(BuiltPrompt {
        text,
        sub_agent,
        skills: scan.set.references(),
        warnings,
    })
}

impl Kernel {
    /// What the kernel's tools keep for the session, while it runs.
    pub(crate) fn tool_state(&self) -> Option<Arc<her_tools::SessionState>> {
        self.session.upgrade().map(|session| session.tool_state.clone())
    }

    pub async fn start(opts: StartOptions, mut deps: KernelDeps) -> Result<Kernel, KernelError> {
        let resolved = deps.llm.resolve(&opts.model)?;
        let mcp = mcp_manager(&deps, &opts.config, opts.project_root.clone());
        let agents = agent_tree(&mut deps, &opts.home, &opts.config, opts.trusted, mcp.clone(), opts.bot.is_some());
        // After the sub-agents took their tools: they have a `send_to_user` of their own.
        if opts.bot.is_some() {
            deps.tools = extended(&deps.tools, "bot", bot::tools);
        }
        let profile = match &opts.bot {
            Some(bot) => PromptProfile::Bot { name: bot.name.clone() },
            None => PromptProfile::Session,
        };
        let system = SystemCell::new(&opts.config);
        let starting = starting_tools(&deps, &system);
        let mut warnings = Vec::new();
        if resolved.ad_hoc {
            warnings.push(format!("model '{}' is not in the catalog; default limits apply", opts.model));
        }
        let context_window = u64::from(resolved.model.context_window);
        let BuiltPrompt {
            text: system_prompt,
            sub_agent,
            skills,
            warnings: prompt_warnings,
        } = build_prompt(&opts.home, &opts.config, &deps, &starting, opts.project_root.as_deref(), context_window, &profile)?;
        warnings.extend(prompt_warnings);
        let id = uuid::Uuid::now_v7().to_string();
        let mut header = SessionHeader::new(id.clone(), opts.cwd.clone());
        header.bot = opts.bot.as_ref().map(|bot| BotRef {
            id: bot.id.clone(),
            name: bot.name.clone(),
        });
        let created = deps.store.create(&header)?;
        let model_ref = resolved.model.reference();
        let mcp_section = mcp::prompt_section(mcp.as_ref(), &starting_deferred(&deps, &system, agents.is_some()));
        // The level asked for, when the model offers it; else the provider decides. A level
        // asked for is remembered for the model, as one chosen in a session is (§18).
        let thinking = resolved.model.resolve_thinking(opts.thinking);
        if thinking != ThinkingLevel::Default
            && let Err(err) = deps.llm.directory().remember_thinking(&resolved.model.reference(), thinking)
        {
            tracing::warn!("cannot remember the thinking level: {}", err.message);
        }
        let mut state = SessionState::new(
            History::default(),
            mcp::compose(&system_prompt, mcp_section.as_deref()).into(),
            resolved.model,
            thinking,
            opts.permission_mode,
        );
        state.base_prompt = system_prompt.into();
        state.mcp_section = mcp_section;
        state.skills = Arc::from(skills);
        state.sub_agent_prompt = sub_agent.map(Arc::from);
        state.prompt_template = opts.bot.as_ref().and_then(|_| crate::prompt::bot_template(&opts.config).ok());
        let bot_id = opts.bot.as_ref().map(|bot| bot.id.clone());
        state.bot = opts.bot.map(|bot| BotNames {
            given: bot.name.clone(),
            current: bot.name,
        });
        let routines = deps.routines.clone();
        let (kernel, session) = launch(Launch {
            id: id.clone(),
            root_id: id.clone(),
            location: created.location.clone(),
            writer: created.writer,
            home: opts.home.clone(),
            trusted: opts.trusted,
            interactive_approvals: opts.interactive_approvals,
            config: opts.config,
            deps,
            state,
            agents: agents.clone(),
            sub_agent: false,
            role: main_role(agents.is_some()),
            main: None,
            approval_line: Arc::default(),
            locks: Arc::new(PathLocks::new()),
            mcp: mcp.clone(),
            system,
        });
        if let Some(tree) = &agents {
            tree.bind_root(&session, kernel.internal.clone());
        }
        link_routines(&session, routines, bot_id);
        session.record_system_prompt();
        session.record(EntryKind::Settings {
            model: Some(model_ref.clone()),
            thinking: Some(opts.thinking),
            permission_mode: Some(opts.permission_mode),
        });
        // A session that closes without doing anything needs no forced flush of these.
        session.writer.settle();
        session.emit(EventMsg::SessionStarted {
            session_id: id,
            protocol_version: PROTOCOL_VERSION,
            model: model_ref,
            cwd: opts.cwd,
            resumed: false,
            session_file: created.location,
            thinking: opts.thinking,
            permission_mode: opts.permission_mode,
            context_window,
            usage: UsageTotals::default(),
            context: Some(session.budget().usage),
        });
        if opts.permission_mode == PermissionMode::BypassPermissions {
            session.warn_bypass();
        }
        for warning in warnings {
            session.warn(warning);
        }
        if let Some(mcp) = &mcp {
            mcp.start(session.emitter.clone());
        }
        Ok(kernel)
    }

    pub async fn resume(opts: ResumeOptions, mut deps: KernelDeps) -> Result<Kernel, KernelError> {
        let loaded = deps.store.open(&opts.session)?;
        let mcp = mcp_manager(&deps, &opts.config, opts.project_root.clone());
        let bot_ref = loaded.header.bot.clone();
        let agents = agent_tree(&mut deps, &opts.home, &opts.config, opts.trusted, mcp.clone(), bot_ref.is_some());
        if bot_ref.is_some() {
            deps.tools = extended(&deps.tools, "bot", bot::tools);
        }
        let system = SystemCell::new(&opts.config);
        let starting = starting_tools(&deps, &system);
        let history = History::from_entries(loaded.entries);
        let mut system_prompt = None;
        let mut last_prompt: Option<(Vec<String>, Option<String>)> = None;
        let mut last_template = None;
        let mut model_ref = None;
        let mut thinking = None;
        let mut permission_mode = None;
        // What was spent, and all that belongs to the session rather than to the conversation,
        // comes from the whole log: going back takes none of it back, and a permission mode
        // tightened after the point gone back to stays tightened.
        let usage = usage_of(history.in_order());
        let restored_agents = agents::restore(&history.branch(), deps.store.as_ref());
        let mut max_task = 0u32;
        let mut open_task: Option<String> = None;
        let mut title = None;
        let mut archived = false;
        let mut skills = Vec::new();
        for entry in history.in_order() {
            match &entry.kind {
                EntryKind::Title { title: t } => title = Some(t.clone()),
                EntryKind::Archived { archived: a } => archived = *a,
                EntryKind::Settings {
                    model,
                    thinking: t,
                    permission_mode: a,
                } => {
                    model_ref = model.clone().or(model_ref);
                    thinking = t.or(thinking);
                    permission_mode = a.or(permission_mode);
                }
                EntryKind::Task { task_id, .. } => {
                    if let Some(n) = task_id.strip_prefix('t').and_then(|n| n.parse::<u32>().ok()) {
                        max_task = max_task.max(n);
                    }
                }
                _ => {}
            }
        }
        for entry in history.branch() {
            match &entry.kind {
                EntryKind::SystemPrompt(prompt) => {
                    system_prompt = Some(prompt.text.clone());
                    last_prompt = Some((prompt.tools.clone(), prompt.mcp.clone()));
                    last_template = prompt.template.clone();
                    // Entries written again later (a new tool set) may leave the skills out.
                    if !prompt.skills.is_empty() {
                        skills = prompt.skills.clone();
                    }
                }
                EntryKind::Task { task_id, status, .. } => {
                    open_task = (*status == TaskStatus::Started).then(|| task_id.clone());
                }
                _ => {}
            }
        }
        let configured = opts.config.model.as_deref();
        let reference = crate::models::session_model(&deps.llm, opts.model.clone().or(model_ref), configured)?;
        // The model the session last used may belong to a provider that is gone (a disconnected
        // or removed connection): the session still opens, on the default model when there is
        // one, and says so. The log keeps the old model, so the session goes back to it once the
        // provider is back. A model asked for by name is not replaced.
        let mut replaced = None;
        let resolved = match deps.llm.resolve(&reference) {
            Ok(resolved) => resolved,
            Err(ResolveError::UnknownProvider { .. }) if opts.model.is_none() => {
                let default = crate::models::default_model(&deps.llm, configured).filter(|model| *model != reference);
                let default = default.and_then(|model| deps.llm.resolve(&model).ok().map(|resolved| (model, resolved)));
                match default {
                    Some((default, resolved)) => {
                        replaced = Some(format!(
                            "model '{reference}' is not available (its provider is disconnected or removed); this session uses '{default}' until it is back"
                        ));
                        resolved
                    }
                    // Nothing to run on, but its history can still be read.
                    None => {
                        replaced = Some(format!(
                            "model '{reference}' is not available (its provider is disconnected or removed); connect a model to go on with this session"
                        ));
                        deps.llm.unavailable(&reference)?
                    }
                }
            }
            Err(error) => return Err(error.into()),
        };
        // Asked for now: kept when the model offers it. Saved or configured: kept when the model
        // offers it, else the level the model starts with (§18).
        let thinking = match opts.thinking {
            Some(level) => resolved.model.resolve_thinking(level),
            None => crate::models::thinking_for(&deps.llm, &resolved.model, thinking.unwrap_or(opts.config.thinking)),
        };
        let permission_mode = opts.permission_mode.or(permission_mode).unwrap_or(opts.config.permission_mode);
        let mut warnings = loaded.warnings;
        warnings.extend(replaced);
        let context_window = u64::from(resolved.model.context_window);
        let project_root = opts.project_root.as_deref();
        let profile = match &bot_ref {
            Some(bot) => PromptProfile::Bot { name: bot.name.clone() },
            None => PromptProfile::Session,
        };
        // A bot's conversation never ends, so its prompt follows a new `bot.md`, at the cost of one
        // prompt-cache miss (bot-turn-design §8). Logs from before this have none on record.
        let template = bot_ref.as_ref().and_then(|_| crate::prompt::bot_template(&opts.config).ok());
        let new_template = template.is_some() && template != last_template;
        if new_template {
            system_prompt = None;
        }
        let mut sub_agent = None;
        let system_prompt = match system_prompt {
            Some(text) => text,
            None => {
                let built = build_prompt(&opts.home, &opts.config, &deps, &starting, project_root, context_window, &profile)?;
                warnings.extend(built.warnings);
                skills = built.skills;
                sub_agent = built.sub_agent;
                built.text.into()
            }
        };
        // A bot's sub-agents are new sessions: their prompt is built as things are now.
        if bot_ref.is_some() && sub_agent.is_none() {
            let built = build_prompt(
                &opts.home,
                &opts.config,
                &deps,
                &starting,
                project_root,
                context_window,
                &PromptProfile::Session,
            )?;
            sub_agent = Some(built.text);
        }
        let mcp_section = mcp::prompt_section(mcp.as_ref(), &starting_deferred(&deps, &system, agents.is_some()));
        let mut state = SessionState::new(
            history,
            mcp::compose(&system_prompt, mcp_section.as_deref()).into(),
            resolved.model,
            thinking,
            permission_mode,
        );
        state.base_prompt = system_prompt;
        state.mcp_section = mcp_section.clone();
        state.skills = Arc::from(skills);
        state.usage = usage.clone();
        state.next_task_no = max_task;
        state.title = title;
        state.archived = archived;
        state.sub_agent_prompt = sub_agent.map(Arc::from);
        state.prompt_template = template;
        let bot_id = bot_ref.as_ref().map(|bot| bot.id.clone());
        state.bot = bot_ref.map(|bot| BotNames {
            current: opts.bot_name.clone().unwrap_or_else(|| bot.name.clone()),
            given: bot.name,
        });
        let routines = deps.routines.clone();
        let model_ref = state.model.reference();
        let overridden = opts.model.is_some() || opts.thinking.is_some() || opts.permission_mode.is_some();
        let (mut kernel, session) = launch(Launch {
            id: loaded.header.id.clone(),
            root_id: loaded.header.id.clone(),
            location: loaded.location.clone(),
            writer: loaded.writer,
            home: opts.home.clone(),
            trusted: opts.trusted,
            interactive_approvals: opts.interactive_approvals,
            config: opts.config,
            deps,
            state,
            agents: agents.clone(),
            sub_agent: false,
            role: main_role(agents.is_some()),
            main: None,
            approval_line: Arc::default(),
            locks: Arc::new(PathLocks::new()),
            mcp: mcp.clone(),
            system,
        });
        if let Some(tree) = &agents {
            tree.bind_root(&session, kernel.internal.clone());
            tree.restore(&restored_agents);
        }
        link_routines(&session, routines, bot_id);
        kernel.restored_agents = restored_agents;
        // A new entry only when the prompt, the tools or the MCP part differ from what the log has.
        let current = (session.tools().names(), mcp_section);
        if new_template || last_prompt.as_ref() != Some(&current) {
            session.record_system_prompt();
        }
        if let Some(task_id) = open_task {
            // The process stopped in the middle of this task.
            session.record(EntryKind::Task {
                task_id,
                status: TaskStatus::Interrupted,
                error: None,
                reason: None,
                usage: None,
            });
        }
        if overridden {
            session.record(EntryKind::Settings {
                model: Some(model_ref.clone()),
                thinking: Some(thinking),
                permission_mode: Some(permission_mode),
            });
        }
        session.emit(EventMsg::SessionStarted {
            session_id: loaded.header.id,
            protocol_version: PROTOCOL_VERSION,
            model: model_ref,
            cwd: loaded.header.cwd,
            resumed: true,
            session_file: loaded.location,
            thinking,
            permission_mode,
            context_window,
            usage,
            // From the usage that the log keeps with every response.
            context: Some(session.budget().usage),
        });
        if permission_mode == PermissionMode::BypassPermissions {
            session.warn_bypass();
        }
        for warning in warnings {
            session.warn(warning);
        }
        if let Some(mcp) = &mcp {
            mcp.start(session.emitter.clone());
        }
        Ok(kernel)
    }

    /// The browsers' and desktop control's settings, after the configuration changed: the
    /// session and its sub-agents take them up before their next request
    /// (connectors-entry-design §9.4). Returns whether they differ from the current ones.
    pub fn set_system_connectors(&self, settings: SystemSettings) -> bool {
        self.session.upgrade().is_some_and(|session| session.system.set(settings))
    }

    /// A bot's new name, which the model is told at the next turn boundary (bot-design §2.4).
    pub fn set_bot_name(&self, name: String) {
        if let Some(session) = self.session.upgrade()
            && let Some(bot) = session.state.lock().bot.as_mut()
        {
            bot.current = name;
        }
    }

    /// What the model is to know of a bot's routines (`routine::listing`), which it is told when
    /// that differs from what its context last gave (routine-design §4.4).
    pub fn set_routines(&self, text: String) {
        if let Some(session) = self.session.upgrade() {
            session.state.lock().routines = Some(text);
        }
    }

    /// Starts a routine's run in the background (routine-design §2.1): the receiver gets how it
    /// went. It needs sub-agents to be on, as it is built like one.
    pub fn start_run(&self, run: RoutineRun) -> Result<oneshot::Receiver<RunOutcome>, String> {
        let session = self.session.upgrade().ok_or("the session is closed")?;
        let tree = session.agents.as_ref().ok_or("sub-agents are turned off in the configuration, and a routine runs as one")?;
        tree.start_run(run)
    }

    /// The session's secure cards, with `card_id` taken back from the history when it was shown
    /// before the session was loaded and has not ended (docs/her-secure-cards.md §3.4). None in a
    /// session that has no cards.
    pub fn cards(&self, card_id: &str) -> Option<Arc<Cards>> {
        let session = self.session.upgrade()?;
        let cards = session.tool_state.get::<Cards>()?;
        if !cards.has(card_id) {
            let found = crate::cards::unresolved(&session.state.lock().history, card_id);
            if let Some(card) = found {
                cards.take_back(card);
            }
        }
        Some(cards)
    }

    /// Her's conversation takes up a connector added after it started, which the owner just
    /// connected on a card: its tools are there from the next turn on. False when the session
    /// has no MCP servers to add to (MCP is turned off).
    pub fn add_mcp_server(&self, server: her_protocol::McpServerConfig) -> bool {
        let Some(mcp) = self.session.upgrade().and_then(|session| session.mcp.clone()) else {
            return false;
        };
        mcp.add(server);
        true
    }

    /// Stops a routine's run. False when none of that id is under way.
    pub fn stop_run(&self, run_id: &str) -> bool {
        self.session.upgrade().is_some_and(|session| session.agents.as_ref().is_some_and(|tree| tree.stop_run(run_id)))
    }

    pub fn session_id(&self) -> &str {
        &self.session_id
    }

    pub fn session_file(&self) -> &Path {
        &self.session_file
    }

    /// The session's files as the desktop's file panel reaches them (file-panel-design §9):
    /// its environment and the locks its tools write under. None after the session closed.
    pub fn file_scope(&self) -> Option<crate::files::FileScope> {
        self.session.upgrade().map(|session| crate::files::FileScope { env: session.env.clone(), locks: session.locks.clone() })
    }

    /// Current settings. After the session closed, only the id and the file are meaningful.
    pub fn info(&self) -> SessionInfo {
        let mut info = SessionInfo {
            session_id: self.session_id.clone(),
            cwd: PathBuf::new(),
            session_file: self.session_file.clone(),
            model: String::new(),
            thinking: ThinkingLevel::Off,
            permission_mode: PermissionMode::Manual,
            context_window: 0,
            title: None,
            archived: false,
        };
        if let Some(session) = self.session.upgrade() {
            info.cwd = session.env.cwd().to_path_buf();
            let state = session.state.lock();
            info.model = state.model.reference();
            info.thinking = state.thinking;
            info.permission_mode = state.permission_mode;
            info.context_window = u64::from(state.model.context_window);
            info.title = state.title.clone();
            info.archived = state.archived;
        }
        info
    }

    /// The skills the session started with, and whether skills are turned on. Nothing after
    /// the session closed.
    pub fn skills(&self) -> (Arc<[SkillRef]>, bool) {
        match self.session.upgrade() {
            Some(session) => (session.state.lock().skills.clone(), session.config.skills.enabled),
            None => (Arc::from(Vec::new()), false),
        }
    }

    /// The tools the session has now; nothing after it closed.
    pub fn tools(&self) -> Vec<her_protocol::api::ToolEntry> {
        self.session.upgrade().map(|session| crate::tool_list::of_session(&session)).unwrap_or_default()
    }

    /// What fills the context window now; `None` after the session closed. Kept until the
    /// history, the model or the tools change, so it may be asked for often.
    pub fn context(&self) -> Option<her_protocol::ContextReport> {
        self.session.upgrade().map(|session| session.context_report())
    }

    /// Messages and tool results on the current branch, oldest first. Clones `Arc`s only.
    pub fn transcript(&self) -> Vec<TranscriptItem> {
        let Some(session) = self.session.upgrade() else {
            return Vec::new();
        };
        let state = session.state.lock();
        transcript_of(state.history.branch())
    }

    /// The sub-agents this session started before it was loaded, as their logs left them; read
    /// once, by whoever shows the session. Empty for a started session and after the first call.
    pub fn take_restored_agents(&mut self) -> Vec<agents::RestoredAgent> {
        std::mem::take(&mut self.restored_agents)
    }

    /// A sender for use from other tasks (signal handlers, stdin readers).
    pub fn op_sender(&self) -> mpsc::Sender<Op> {
        self.ops.clone()
    }

    pub async fn submit(&self, op: Op) -> Result<(), KernelClosed> {
        self.ops.send(op).await.map_err(|_| KernelClosed)
    }

    /// `None` once the session is closed.
    pub async fn next_event(&mut self) -> Option<Event> {
        self.events.recv().await.map(|event| *event)
    }

    /// Submits input and collects events until the task ends. Approval requests are not
    /// answered here; use `submit` and `next_event` for interactive use.
    pub async fn run(&mut self, input: Vec<UserContent>) -> Result<TaskResult, KernelClosed> {
        self.submit(Op::Prompt {
            input,
            mode: PromptMode::Steer,
            commands: true,
        })
        .await?;
        let mut events = Vec::new();
        while let Some(event) = self.next_event().await {
            let end = match &event.msg {
                EventMsg::TaskCompleted { last_message, .. } => Some(TaskEnd::Completed {
                    last_message: last_message.clone(),
                }),
                EventMsg::TaskFailed { error, .. } => Some(TaskEnd::Failed(error.clone())),
                EventMsg::TaskInterrupted { .. } => Some(TaskEnd::Interrupted),
                _ => None,
            };
            events.push(event);
            if let Some(end) = end {
                return Ok(TaskResult { end, events });
            }
        }
        Err(KernelClosed)
    }

    /// Stops the session and waits until it is closed.
    pub async fn shutdown(mut self) -> Vec<Event> {
        let _ = self.ops.send(Op::Shutdown).await;
        let mut rest = Vec::new();
        while let Some(event) = self.events.recv().await.map(|event| *event) {
            let closed = matches!(event.msg, EventMsg::SessionClosed);
            rest.push(event);
            if closed {
                break;
            }
        }
        // The loop owns the session. Once it ends, the log writer drains, closes the file and
        // releases its lock, so the session can be reopened right away.
        let _ = self.submission.await;
        let _ = tokio::time::timeout(Duration::from_secs(5), self.log_closed.wait()).await;
        rest
    }

    /// After `session_closed`: waits until the loop has ended and the log is closed.
    pub(crate) async fn closed(self) {
        let _ = self.submission.await;
        let _ = tokio::time::timeout(Duration::from_secs(5), self.log_closed.wait()).await;
    }
}

async fn submission_loop(
    session: Arc<Session>,
    mut ops: mpsc::Receiver<Op>,
    mut internal: mpsc::UnboundedReceiver<Box<Internal>>,
) {
    loop {
        tokio::select! {
            op = ops.recv() => {
                let Some(op) = op else { break };
                match op {
                    Op::Prompt { input, mode, commands } => task::on_prompt(&session, input, mode, commands),
                    Op::Resend { message_id, input, commands } => task::on_resend(&session, message_id, input, commands),
                    Op::Continue => task::on_continue(&session),
                    Op::Interrupt => session.interrupt(),
                    Op::Withdraw { id } => session.withdraw(&id),
                    Op::Requeue { id, mode } => session.requeue(&id, mode),
                    Op::SteerNow => session.steer_now(),
                    Op::AgentInterrupt { agent } => session.interrupt_agent(&agent),
                    Op::Approval { call_id, decision } => session.resolve_approval(&call_id, decision),
                    Op::AgentApproval { agent, call_id, decision } => session.answer_agent(&agent, &call_id, decision),
                    Op::SetModel { model, thinking } => session.set_model(&model, thinking),
                    Op::SetPermissionMode { mode } => session.set_permission_mode(mode),
                    Op::SetTitle { title } => session.set_title(&title),
                    Op::SetArchived { archived } => session.set_archived(archived),
                    Op::Compact => task::on_compact(&session),
                    Op::Shutdown => break,
                }
            }
            Some(message) = internal.recv() => match *message {
                Internal::Mail(mail) => task::on_mail(&session, mail),
                Internal::Card(card) => task::on_card(&session, card),
                Internal::Permissions { mode, rules, grants } => session.apply_permissions(mode, rules, grants),
            },
        }
    }
    let handle = {
        let mut state = session.state.lock();
        // A resend still waiting does not go ahead: no new task starts as the session closes.
        state.pending_resend = None;
        state.active_task.as_mut().and_then(|task| {
            task.cancel.cancel();
            task.handle.take()
        })
    };
    if let Some(handle) = handle {
        let _ = handle.await;
    }
    // The sub-agents end with the main session, then the MCP servers they shared.
    if let Some(tree) = &session.agents {
        tree.shutdown().await;
    }
    if !session.sub_agent
        && let Some(mcp) = &session.mcp
    {
        mcp.shutdown().await;
    }
    if let Err(err) = session.writer.sync().await {
        tracing::warn!(%err, "failed to sync the session log on shutdown");
    }
    if let Some(dir) = &session.temporary_data {
        let _ = std::fs::remove_dir_all(dir);
    }
    session.emit(EventMsg::SessionClosed);
}

/// What the requests on a branch of entries cost.
pub(crate) fn usage_of<'a>(branch: impl IntoIterator<Item = &'a Entry>) -> UsageTotals {
    let mut usage = UsageTotals::default();
    for entry in branch {
        match &entry.kind {
            EntryKind::Message {
                message: Message::Assistant(assistant),
                ..
            } => usage.add(&format!("{}/{}", assistant.provider, assistant.model), &assistant.usage),
            // Summary requests are paid for too.
            EntryKind::Compaction(checkpoint) => usage.add(&checkpoint.model, &checkpoint.usage),
            _ => {}
        }
    }
    usage
}

/// What clients show of a branch of entries: messages, tool results and task ends.
pub(crate) fn transcript_of<'a>(branch: impl IntoIterator<Item = &'a Entry>) -> Vec<TranscriptItem> {
    branch
        .into_iter()
        .filter_map(|entry| match &entry.kind {
            EntryKind::Message {
                message: Message::ToolResult(result),
                ..
            } => Some(TranscriptItem::ToolResult {
                call_id: result.call_id.clone(),
                result: result.clone(),
            }),
            EntryKind::Message { message, skills, skill } => Some(TranscriptItem::Message {
                message_id: entry.id.clone(),
                message: context::written(message, skill.as_deref()),
                skills: context::skill_uses(skills, skill.as_deref()),
                mail: None,
            }),
            EntryKind::Mail { mail, message } | EntryKind::AgentMessage { mail, message } | EntryKind::AgentToUser { mail, message } => Some(TranscriptItem::Message {
                message_id: entry.id.clone(),
                message: message.clone(),
                skills: Vec::new(),
                mail: Some(mail.clone()),
            }),
            EntryKind::CardResolved(card) => Some(TranscriptItem::CardResolved {
                card_id: card.card_id.clone(),
                // A later version's outcome is left out.
                outcome: serde_json::from_value(serde_json::Value::String(card.outcome.clone())).ok()?,
                note: card.note.clone(),
            }),
            EntryKind::Task {
                task_id,
                status,
                error,
                reason,
                usage,
            } => {
                let outcome = match status {
                    TaskStatus::Started => return None,
                    TaskStatus::Completed => TaskOutcome::Completed,
                    TaskStatus::Failed => TaskOutcome::Failed,
                    TaskStatus::Interrupted => TaskOutcome::Interrupted,
                };
                Some(TranscriptItem::TaskEnd {
                    task_id: task_id.clone(),
                    outcome,
                    reason: *reason,
                    error: error.clone(),
                    usage: usage.as_deref().cloned(),
                    ts: entry.ts,
                })
            }
            _ => None,
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_tools::BashTool;
    use her_tools::builtin_registry;

    #[test]
    fn main_sessions_share_one_registry_with_the_agent_tools() {
        let base = Arc::new(builtin_registry(BashTool::default()));
        let with = with_agent_tools(&base);
        assert!(Arc::ptr_eq(&with, &with_agent_tools(&base)));
        assert!(with.get("spawn_agent").is_some() && with.get("send_message").is_some());
        assert!(base.get("spawn_agent").is_none(), "sub-agents keep the tools without these two");
        let other = Arc::new(builtin_registry(BashTool::default()));
        assert!(!Arc::ptr_eq(&with_agent_tools(&other), &with));
    }

    /// Computer use is on by default with the desktop app, off in the command line, and the
    /// user's switch decides either way (computer-use-design §3); its tools are a deferred
    /// group, never in the tool list.
    #[test]
    fn computer_use_is_on_with_the_desktop_app_unless_turned_off() {
        let mut settings = SystemSettings::of(&Config::default());
        let has = |settings: &SystemSettings, app: bool| crate::deferred::Deferred::of(settings, false, app, false, crate::deferred::AgentRole::None, false).find("cua_snapshot").is_some();
        assert!(has(&settings, true), "the desktop app connected");
        assert!(!has(&settings, false), "the command line");
        settings.desktop.enabled = Some(false);
        assert!(!has(&settings, true), "turned off");
        settings.desktop.enabled = Some(true);
        assert!(has(&settings, false), "turned on");
    }
}
