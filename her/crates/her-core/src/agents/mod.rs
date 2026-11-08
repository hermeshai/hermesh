//! Sub-agents: sessions that the main agent starts with `spawn_agent` and talks to with
//! `send_message`. Each is an ordinary session with its own log, whose questions go to the main
//! session's user, and it cannot start agents of its own. When one has nothing left to do, its
//! report goes back to the main session: as the result of a call that waits for it, or as mail
//! that joins the main session's history at its next turn boundary and wakes it when it is idle.
//! While it works, a sub-agent can use `send_message` too: to the main agent, which wakes it, or
//! to another working sub-agent. Only the main agent sets an idle agent to work
//! (agent-messaging-design §3). A bot's sub-agents also tell its owner how the work goes with
//! `send_to_user`, which wakes nobody (§17).

mod driver;
mod mail;
mod restore;
mod run;
mod tools;

use her_llm::Message;
use her_llm::ModelRegistry;
use her_llm::UsageTotals;
use her_llm::UserContent;
use her_protocol::AgentRef;
use her_protocol::AgentStatus;
use her_protocol::CommandRule;
use her_protocol::ErrorInfo;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::Op;
use her_protocol::PermissionMode;
use her_protocol::PromptMode;
use her_tools::Environment;
use her_tools::ToolError;
use her_tools::ToolOutput;
use her_tools::ToolRegistry;
use parking_lot::Mutex;
use serde_json::json;
use std::collections::VecDeque;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::OnceLock;
use std::sync::Weak;
use std::time::Duration;
use std::time::Instant;
use tokio::sync::mpsc;
use tokio::sync::oneshot;
use tokio::sync::watch;
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

pub(crate) use mail::Mail;
pub use restore::RestoredAgent;
pub(crate) use restore::restore;
pub(crate) use tools::agent_tools;
pub(crate) use tools::bot_agent_tools;
pub(crate) use tools::bot_sub_agent_tools;
pub(crate) use tools::deferred_agent_tools;
pub(crate) use tools::deferred_sub_agent_tools;
pub(crate) use tools::sub_agent_tools;

use crate::config::Config;
use crate::emitter::Emitter;
use crate::hooks::Hooks;
use crate::kernel;
use crate::kernel::ChildLaunch;
use crate::kernel::KernelDeps;
use crate::paths::HerHome;
use crate::session::Internal;
use crate::session::Session;
use crate::store::AgentOrigin;
use crate::store::AgentSpawnedEntry;
use crate::store::EntryKind;
use crate::store::SessionHeader;
use crate::store::SessionStore;

/// Ends the results of calls that leave an agent working in the background. Models take ending
/// their turn for handing in the answer, so when they need the report first they wait for it with
/// `sleep`, which also holds back the user's input: input reaches a running task only between
/// turns. Saying that ending the turn is the way to wait, and is not handing in, stops this
/// (subagent-design §15).
const REPORT_LATER: &str = "To wait for its report, end your turn: that is not your final answer. The report \
    comes to you as a new message when the agent finishes, and you continue your work from there.";

/// How long `stop_agents` waits for what it stopped: past the time a cancelled tool has to stop.
const STOP_WAIT: Duration = Duration::from_secs(10);
/// The steps `list_agents` shows of an agent.
const RECENT_STEPS: usize = 5;

/// Characters of a step that `list_agents` shows.
const STEP_CHARS: usize = 120;

/// Characters of what an agent last said that `list_agents` shows.
const SAID_CHARS: usize = 200;

/// What a sub-agent calls the main agent. Taken: no sub-agent gets one of these names.
const MAIN: &str = "main";
/// Its task calls the main agent its parent, so it may well write this.
const PARENT: &str = "parent";

/// What a sub-agent's session is built from: the main session's dependencies.
pub(crate) struct ChildDeps {
    pub(crate) llm: Arc<ModelRegistry>,
    /// The main session's tools without the agent tools.
    pub(crate) tools: Arc<ToolRegistry>,
    pub(crate) env: Arc<dyn Environment>,
    pub(crate) store: Arc<dyn SessionStore>,
    /// Extra hooks; the approval policy is added for every session.
    pub(crate) hooks: Vec<Arc<dyn Hooks>>,
    pub(crate) home: HerHome,
    pub(crate) config: Arc<Config>,
    pub(crate) trusted: bool,
    /// The main session's MCP servers; sub-agents use the same connections.
    pub(crate) mcp: Option<Arc<crate::mcp::McpManager>>,
    /// The main session's browsers: a sub-agent has its own tabs in the same browser.
    pub(crate) browser_pool: Option<Arc<her_browser::BrowserPool>>,
    pub(crate) docs: Option<Arc<her_docs::DocStore>>,
    pub(crate) decks: Option<Arc<her_docs::deck::DeckStore>>,
    pub(crate) words: Option<Arc<her_docs::word::WordStore>>,
}

/// How a sub-agent's tools find the tree and who is calling. Weak: the tree owns its agents.
pub(crate) struct AgentLink {
    tree: Weak<AgentTree>,
    id: String,
}

impl AgentLink {
    fn tree(&self) -> Result<Arc<AgentTree>, ToolError> {
        self.tree.upgrade().ok_or_else(|| ToolError::Failed("the session is closing".into()))
    }
}

/// The main session, once it runs. Weak: the session owns the tree, not the other way round.
struct Root {
    session: Weak<Session>,
    emitter: Arc<Emitter>,
    internal: mpsc::UnboundedSender<Box<Internal>>,
}

/// The control plane of a main session's sub-agents: who they are, what they are doing, and
/// where each report goes.
pub(crate) struct AgentTree {
    deps: ChildDeps,
    root: OnceLock<Root>,
    /// Never held across an await.
    state: Mutex<TreeState>,
    /// Bumped whenever an agent stops working.
    settled: watch::Sender<u64>,
}

#[derive(Default)]
struct TreeState {
    agents: Vec<Agent>,
    /// Agents of an earlier run of the session, whose sessions are not loaded: their names
    /// stay taken, and they take no messages.
    restored: Vec<(String, String)>,
    /// Routines' runs under way (routine-design §2.1): apart from `agents`, as no message, name
    /// or idle check concerns them.
    runs: Vec<run::ActiveRun>,
    /// The drivers of agents that `retire` took off the tree, until their sessions have closed.
    retiring: Vec<JoinHandle<()>>,
    /// Reports and messages handed over but not yet in the main session's inbox, or in a
    /// waiting call.
    in_flight: usize,
    mails: u64,
    closing: bool,
}

struct Agent {
    id: String,
    name: String,
    /// What the user sees for it.
    description: String,
    status: AgentStatus,
    /// Kept by its driver.
    progress: Arc<Mutex<Progress>>,
    ops: mpsc::Sender<Op>,
    internal: mpsc::UnboundedSender<Box<Internal>>,
    route: Route,
    /// `send_message` with `interrupt`: the interrupted task's input starts the next one.
    redirect: bool,
    driver: Option<JoinHandle<()>>,
    /// What the kernel's tools keep for the agent's session: its browser.
    tool_state: Option<Arc<her_tools::SessionState>>,
}

/// What `list_agents` tells of an agent's work.
pub(super) struct Progress {
    /// When its current or last round of work began.
    since: Instant,
    /// When its session last did anything.
    last: Instant,
    /// Its latest tool calls, oldest first.
    steps: VecDeque<String>,
    /// What it waits for the user to approve.
    asking: Option<String>,
    /// The start of what it last wrote in this round of work, which nobody sees until it reports.
    said: Option<String>,
}

impl Progress {
    fn new() -> Self {
        let now = Instant::now();
        Self {
            since: now,
            last: now,
            steps: VecDeque::new(),
            asking: None,
            said: None,
        }
    }

    /// A new round of work.
    pub(super) fn begin(&mut self) {
        self.since = Instant::now();
        self.steps.clear();
        self.said = None;
    }

    /// An event of its session.
    pub(super) fn saw(&mut self, msg: &EventMsg) {
        self.last = Instant::now();
        match msg {
            EventMsg::ToolStarted { title, .. } => {
                if self.steps.len() == RECENT_STEPS {
                    self.steps.pop_front();
                }
                self.steps.push_back(step_line(title));
            }
            EventMsg::ApprovalRequested { summary, .. } => self.asking = Some(step_line(summary)),
            EventMsg::ApprovalResolved { .. } => self.asking = None,
            EventMsg::MessageCompleted {
                message: Message::Assistant(reply),
                ..
            } => {
                let text = reply.text();
                if !text.trim().is_empty() {
                    self.said = Some(said_line(&text));
                }
            }
            _ => {}
        }
    }
}

/// What an agent said, as `list_agents` shows it: the start of it, on one line.
fn said_line(text: &str) -> String {
    let line = text.split_whitespace().collect::<Vec<_>>().join(" ");
    match line.char_indices().nth(SAID_CHARS) {
        Some((end, _)) => format!("{}…", &line[..end]),
        None => line,
    }
}

/// A step as `list_agents` shows it: a command can run to pages.
fn step_line(title: &str) -> String {
    let line = title.lines().next().unwrap_or_default();
    match line.char_indices().nth(STEP_CHARS) {
        Some((end, _)) => format!("{}…", &line[..end]),
        None if line.len() < title.trim_end().len() => format!("{line}…"),
        None => line.to_string(),
    }
}

/// Where the report of the current round of work goes.
enum Route {
    Mail,
    /// A call is waiting for it.
    Waiter(oneshot::Sender<Report>),
}

/// What a round of work came to: from the first task after being idle until idle again.
pub(crate) struct Report {
    pub(crate) status: AgentStatus,
    /// The last reply.
    pub(crate) reply: Option<String>,
    pub(crate) error: Option<ErrorInfo>,
    /// Messages from the main agent that a stop left unprocessed.
    pub(crate) unprocessed: Vec<String>,
    pub(crate) usage: UsageTotals,
    pub(crate) elapsed: Duration,
}

impl AgentTree {
    pub(crate) fn new(deps: ChildDeps) -> Arc<Self> {
        Arc::new(Self {
            deps,
            root: OnceLock::new(),
            state: Mutex::new(TreeState::default()),
            settled: watch::channel(0).0,
        })
    }

    pub(crate) fn bind_root(&self, session: &Arc<Session>, internal: mpsc::UnboundedSender<Box<Internal>>) {
        let _ = self.root.set(Root {
            session: Arc::downgrade(session),
            emitter: session.emitter.clone(),
            internal,
        });
    }

    fn root(&self) -> Result<Arc<Session>, ToolError> {
        self.root
            .get()
            .and_then(|root| root.session.upgrade())
            .ok_or_else(|| ToolError::Failed("the session is closing".into()))
    }

    fn emit(&self, msg: EventMsg) {
        if let Some(root) = self.root.get() {
            root.emitter.emit(msg);
        }
    }

    /// The agents of an earlier run of the session (by id and name).
    pub(crate) fn restore(&self, agents: &[RestoredAgent]) {
        self.state.lock().restored = agents.iter().map(|a| (a.agent_id.clone(), a.name.clone())).collect();
    }

    /// Every sub-agent is idle and no report is on its way.
    pub(crate) fn settled(&self) -> bool {
        let state = self.state.lock();
        state.in_flight == 0 && state.agents.iter().all(|a| a.status != AgentStatus::Running)
    }

    /// The main session took a report into its inbox.
    pub(crate) fn mail_received(&self) {
        let mut state = self.state.lock();
        state.in_flight = state.in_flight.saturating_sub(1);
    }

    /// Sub-agents judge tool calls by the main session's current permissions.
    pub(crate) fn share_permissions(&self, mode: PermissionMode, rules: Vec<CommandRule>, grants: Vec<her_protocol::SiteGrant>) {
        let state = self.state.lock();
        for agent in state.agents.iter().filter(|a| a.status != AgentStatus::Closed) {
            let _ = agent.internal.send(Box::new(Internal::Permissions {
                mode,
                rules: rules.clone(),
                grants: grants.clone(),
            }));
        }
        for internal in state.runs.iter().filter_map(|run| run.internal.as_ref()) {
            let _ = internal.send(Box::new(Internal::Permissions {
                mode,
                rules: rules.clone(),
                grants: grants.clone(),
            }));
        }
    }

    /// The agents started in the part of the conversation a `resend` took back: they stop, their
    /// sessions close, and they leave the tree, so that their names are free again and whatever
    /// they were about to report is dropped.
    pub(crate) fn retire(&self, ids: &[String]) {
        if ids.is_empty() {
            return;
        }
        let mut state = self.state.lock();
        state.restored.retain(|(id, _)| !ids.contains(id));
        state.retiring.retain(|driver| !driver.is_finished());
        let (gone, stay): (Vec<Agent>, Vec<Agent>) = std::mem::take(&mut state.agents).into_iter().partition(|a| ids.contains(&a.id));
        state.agents = stay;
        for mut agent in gone {
            send_now(&agent.ops, Op::Shutdown);
            state.retiring.extend(agent.driver.take());
        }
    }

    /// Whether this agent is on the tree: not when a `resend` took it off.
    pub(crate) fn has(&self, id: &str) -> bool {
        self.state.lock().agents.iter().any(|a| a.id == id)
    }

    /// Stops what every sub-agent is doing. Their sessions stay; a message continues them.
    pub(crate) fn interrupt_all(&self) {
        let state = self.state.lock();
        for agent in state.agents.iter().filter(|a| a.status == AgentStatus::Running) {
            send_now(&agent.ops, Op::Interrupt);
        }
    }

    /// Stops one sub-agent's current task (`agent_interrupt`). False when no agent of that id or
    /// name is running.
    pub(crate) fn interrupt_one(&self, agent: &str) -> bool {
        let state = self.state.lock();
        let Some(agent) = state
            .agents
            .iter()
            .find(|a| (a.id == agent || a.name == agent) && a.status == AgentStatus::Running)
        else {
            return false;
        };
        send_now(&agent.ops, Op::Interrupt);
        true
    }

    /// The user's answer to what agent `agent` asks. False when there is no such agent.
    pub(crate) fn answer(&self, agent: &str, call_id: &str, decision: her_protocol::ApprovalDecision) -> bool {
        let state = self.state.lock();
        let Some(agent) = state.agents.iter().find(|a| a.id == agent) else {
            return false;
        };
        send_now(&agent.ops, Op::Approval { call_id: call_id.to_string(), decision });
        true
    }

    /// How the agents stand, the ones named or all of them (`list_agents`). `caller` is the
    /// sub-agent asking, which is also told about the main agent and which of them it is.
    pub(crate) fn list(&self, names: Option<&[String]>, caller: Option<&str>) -> String {
        // Before the tree's lock: nothing holds both.
        let main_line = caller.map(|_| {
            let working = self.root().is_ok_and(|root| root.is_busy());
            format!("{MAIN}: {}", if working { "working" } else { "idle" })
        });
        let state = self.state.lock();
        let mut lines = Vec::new();
        let agent_line = |agent: &Agent| {
            let mut label = if agent.description.is_empty() { agent.name.clone() } else { format!("{} ({})", agent.name, agent.description) };
            if caller == Some(agent.id.as_str()) {
                label.push_str(" (you)");
            }
            let progress = agent.progress.lock();
            let mut line = if agent.status == AgentStatus::Running {
                let mut line = format!("{label}: running for {}", mail::duration(progress.since.elapsed()));
                match &progress.asking {
                    Some(what) => line.push_str(&format!(", waiting for the user to approve: {what}")),
                    None => line.push_str(&format!(", last activity {} ago", mail::duration(progress.last.elapsed()))),
                }
                line
            } else {
                format!("{label}: {}, {} ago", agent.status.as_str(), mail::duration(progress.last.elapsed()))
            };
            if !progress.steps.is_empty() {
                let steps: Vec<&str> = progress.steps.iter().map(String::as_str).collect();
                line.push_str(&format!("\n  recent steps: {}", steps.join("; ")));
            }
            // Once it is idle, its report has said it.
            if agent.status == AgentStatus::Running
                && let Some(said) = &progress.said
            {
                line.push_str(&format!("\n  last said: {said}"));
            }
            line
        };
        match names {
            Some(names) => {
                for name in names {
                    let name = name.trim();
                    if let Some(agent) = state.agents.iter().find(|a| a.name == name || a.id == name) {
                        lines.push(agent_line(agent));
                    } else if let Some(main) = main_line.as_ref().filter(|_| is_main(name)) {
                        lines.push(main.clone());
                    } else if state.restored.iter().any(|(id, n)| n == name || id == name) {
                        lines.push(format!("{name}: closed, from an earlier run"));
                    } else {
                        lines.push(format!("{name}: no such agent"));
                    }
                }
            }
            None => {
                lines.extend(main_line.clone());
                lines.extend(state.restored.iter().map(|(_, name)| format!("{name}: closed, from an earlier run")));
                lines.extend(state.agents.iter().map(agent_line));
            }
        }
        if lines.is_empty() {
            return "You have no agents.".to_string();
        }
        lines.join("\n")
    }

    /// Stops the agents named, or all that run, and waits until they did (`stop_agents`). They
    /// keep their sessions, and their reports do not wake the main session.
    pub(crate) async fn stop(&self, names: Option<&[String]>, cancel: &CancellationToken) -> Result<String, ToolError> {
        let mut settled = self.settled.subscribe();
        settled.borrow_and_update();
        let mut stopping: Vec<String> = Vec::new();
        let mut finished: Vec<String> = Vec::new();
        let mut unknown: Vec<String> = Vec::new();
        {
            let state = self.state.lock();
            let targets: Vec<&Agent> = match names {
                Some(names) => names
                    .iter()
                    .filter_map(|name| {
                        let name = name.trim();
                        let agent = state.agents.iter().find(|a| a.name == name || a.id == name);
                        if agent.is_none() {
                            if state.restored.iter().any(|(id, n)| n == name || id == name) {
                                finished.push(name.to_string());
                            } else {
                                unknown.push(name.to_string());
                            }
                        }
                        agent
                    })
                    .collect(),
                None => state.agents.iter().filter(|a| a.status == AgentStatus::Running).collect(),
            };
            for agent in targets {
                if agent.status == AgentStatus::Running {
                    send_now(&agent.ops, Op::Interrupt);
                    stopping.push(agent.id.clone());
                } else {
                    finished.push(agent.name.clone());
                }
            }
        }
        let deadline = tokio::time::sleep(STOP_WAIT);
        tokio::pin!(deadline);
        loop {
            let working = {
                let state = self.state.lock();
                state.agents.iter().any(|a| stopping.contains(&a.id) && a.status == AgentStatus::Running)
            };
            if !working {
                break;
            }
            tokio::select! {
                _ = settled.changed() => {}
                _ = &mut deadline => break,
                _ = cancel.cancelled() => return Err(ToolError::Cancelled),
            }
        }
        let mut stopped: Vec<String> = Vec::new();
        let mut still: Vec<String> = Vec::new();
        {
            let state = self.state.lock();
            for agent in state.agents.iter().filter(|a| stopping.contains(&a.id)) {
                match agent.status {
                    AgentStatus::Running => still.push(agent.name.clone()),
                    AgentStatus::Interrupted => stopped.push(agent.name.clone()),
                    // It got there first.
                    _ => finished.push(agent.name.clone()),
                }
            }
        }
        let mut parts = Vec::new();
        for (what, who) in [("Stopped", &stopped), ("Already finished", &finished), ("Still stopping", &still), ("No such agent", &unknown)] {
            if !who.is_empty() {
                parts.push(format!("{what}: {}.", who.join(", ")));
            }
        }
        if parts.is_empty() {
            return Ok("No agents are running.".to_string());
        }
        Ok(parts.join(" "))
    }

    /// Passes an event of agent `id`'s session on to the main session's clients (`agent_event`).
    fn forward(&self, id: &str, event: &Event) {
        if followed(&event.msg) {
            self.emit(EventMsg::AgentEvent {
                agent_id: id.to_string(),
                event: Box::new(event.clone()),
            });
        }
    }

    /// Closes every sub-agent's session and every run's, and waits until their logs are closed.
    pub(crate) async fn shutdown(&self) {
        let (agents, runs, retiring) = {
            let mut state = self.state.lock();
            state.closing = true;
            let runs: Vec<JoinHandle<()>> = state.runs.iter_mut().filter_map(run::ActiveRun::close).collect();
            let agents: Vec<_> = state.agents.iter_mut().map(|a| (a.ops.clone(), a.driver.take())).collect();
            (agents, runs, std::mem::take(&mut state.retiring))
        };
        for (ops, _) in &agents {
            let _ = ops.send(Op::Shutdown).await;
        }
        let drivers = agents.into_iter().filter_map(|(_, driver)| driver).chain(runs).chain(retiring);
        let _ = tokio::time::timeout(Duration::from_secs(5), futures::future::join_all(drivers)).await;
    }

    /// What a new session of the tree starts from: the main session's current settings. A bot's
    /// start from a session's prompt, not the bot's (bot-design §2.3).
    fn child_launch(&self, root: &Arc<Session>, header: SessionHeader, interactive_approvals: bool) -> ChildLaunch {
        let state = root.state.lock();
        let (system_prompt, base_prompt) = match &state.sub_agent_prompt {
            Some(prompt) => (crate::mcp::compose(prompt, state.mcp_section.as_deref()).into(), prompt.clone()),
            None => (state.system_prompt.clone(), state.base_prompt.clone()),
        };
        ChildLaunch {
            header,
            bot: state.bot.is_some(),
            system_prompt,
            base_prompt,
            mcp_section: state.mcp_section.clone(),
            mcp: self.deps.mcp.clone(),
            skills: state.skills.clone(),
            model: state.model.clone(),
            thinking: state.thinking,
            permission_mode: state.permission_mode,
            session_rules: state.session_rules.clone(),
            site_grants: state.site_grants.clone(),
            deps: KernelDeps {
                llm: self.deps.llm.clone(),
                tools: self.deps.tools.clone(),
                env: self.deps.env.clone(),
                store: self.deps.store.clone(),
                hooks: self.deps.hooks.clone(),
                mcp_servers: Vec::new(),
                browser_pool: self.deps.browser_pool.clone(),
                docs: self.deps.docs.clone(),
                decks: self.deps.decks.clone(),
                words: self.deps.words.clone(),
                routines: None,
            },
            home: self.deps.home.clone(),
            config: self.deps.config.clone(),
            system: root.system.clone(),
            trusted: self.deps.trusted,
            interactive_approvals,
            main: Arc::downgrade(root),
            approval_line: root.approval_line.clone(),
            locks: root.locks.clone(),
        }
    }

    pub(crate) async fn spawn(
        self: &Arc<Self>,
        args: tools::SpawnArgs,
        call_id: &str,
        cancel: &CancellationToken,
    ) -> Result<ToolOutput, ToolError> {
        let task = args.task.trim();
        if task.is_empty() {
            return Err(ToolError::InvalidArgs("task must not be empty".into()));
        }
        let root = self.root()?;
        let parent_task = root.state.lock().active_task.as_ref().map(|t| t.id.clone()).unwrap_or_default();
        let name = self.unique_name(args.name.as_deref())?;
        let id = uuid::Uuid::now_v7().to_string();
        let mut header = SessionHeader::new(id.clone(), root.env.cwd().to_path_buf());
        header.agent = Some(AgentOrigin {
            root_id: root.id.clone(),
            parent_id: root.id.clone(),
            name: name.clone(),
            depth: 1,
            call_id: call_id.to_string(),
            parent_task,
            routine: None,
        });
        let kernel = kernel::start_child(self.child_launch(&root, header, root.interactive_approvals))
            .map_err(|err| ToolError::Failed(format!("cannot start the agent: {err}")))?;
        let session_file: PathBuf = kernel.session_file().to_path_buf();
        let ops = kernel.op_sender();
        let (route, waiter) = route(args.wait);
        let description = args.description.trim().to_string();
        let progress = Arc::new(Mutex::new(Progress::new()));
        let tool_state = kernel.tool_state();
        if let Some(tool_state) = &tool_state {
            tool_state.insert(Arc::new(AgentLink {
                tree: Arc::downgrade(self),
                id: id.clone(),
            }));
        }
        self.state.lock().agents.push(Agent {
            id: id.clone(),
            name: name.clone(),
            description: description.clone(),
            status: AgentStatus::Running,
            progress: progress.clone(),
            ops: ops.clone(),
            internal: kernel.internal.clone(),
            route,
            redirect: false,
            driver: None,
            tool_state,
        });
        root.record(EntryKind::AgentSpawned(Box::new(AgentSpawnedEntry {
            agent_id: id.clone(),
            name: name.clone(),
            description: description.clone(),
            session_file: session_file.clone(),
            call_id: call_id.to_string(),
        })));
        self.emit(EventMsg::AgentStarted {
            agent_id: id.clone(),
            name: name.clone(),
            description,
            session_file: session_file.clone(),
            call_id: call_id.to_string(),
        });
        let driver = tokio::spawn(driver::drive(self.clone(), id.clone(), kernel, progress));
        if let Some(agent) = self.state.lock().agents.iter_mut().find(|a| a.id == id) {
            agent.driver = Some(driver);
        }
        let bot = root.state.lock().bot.is_some();
        let _ = ops
            .send(Op::Prompt {
                input: vec![UserContent::Text {
                    text: mail::task_message(&name, task, bot),
                }],
                mode: PromptMode::Steer,
                // A task brief is not the user's input: a leading `/name` stays text.
                commands: false,
            })
            .await;
        // `background`: the report arrives later, as mail; `reported`: this result is the report.
        let details = |reported: bool| {
            json!({ "agent_id": id, "name": name, "session_file": session_file, "status": "running", "background": !reported, "reported": reported })
        };
        match waiter {
            Some(rx) => {
                let (output, reported) = self.wait(&root, &id, &name, rx, cancel).await?;
                Ok(output.with_details(details(reported)))
            }
            None => Ok(ToolOutput::text(format!(
                "Agent \"{name}\" started in the background. {REPORT_LATER}"
            ))
            .with_details(details(false))),
        }
    }

    pub(crate) async fn send(&self, args: tools::SendArgs, cancel: &CancellationToken) -> Result<ToolOutput, ToolError> {
        let message = args.message.trim();
        if message.is_empty() {
            return Err(ToolError::InvalidArgs("message must not be empty".into()));
        }
        let root = self.root()?;
        let (id, name, ops, was_running, waiter) = {
            let mut state = self.state.lock();
            let names: Vec<String> = state.restored.iter().map(|(_, name)| name.clone()).chain(state.agents.iter().map(|a| a.name.clone())).collect();
            let to = args.to.trim();
            if let Some((_, name)) = state.restored.iter().find(|(id, name)| name == to || id == to) {
                return Err(ToolError::Failed(format!("Agent \"{name}\" is closed. Start a new one with spawn_agent.")));
            }
            let Some(agent) = state.agents.iter_mut().find(|a| a.name == to || a.id == to) else {
                if is_main(to) {
                    return Err(ToolError::Failed("That is you.".into()));
                }
                return Err(ToolError::Failed(if names.is_empty() {
                    format!("No agent named \"{to}\". Start one with spawn_agent.")
                } else {
                    format!("No agent named \"{to}\". Your agents: {}.", names.join(", "))
                }));
            };
            if agent.status == AgentStatus::Closed {
                return Err(ToolError::Failed(format!(
                    "Agent \"{}\" is closed. Start a new one with spawn_agent.",
                    agent.name
                )));
            }
            let waiter = if args.wait {
                if matches!(agent.route, Route::Waiter(_)) {
                    return Err(ToolError::Failed(format!("Another call is already waiting for \"{}\".", agent.name)));
                }
                let (route, waiter) = route(true);
                agent.route = route;
                waiter
            } else {
                None
            };
            let was_running = agent.status == AgentStatus::Running;
            if args.interrupt && was_running {
                agent.redirect = true;
            }
            // Busy from now on: the message starts work, so the main session is not idle.
            agent.status = AgentStatus::Running;
            (agent.id.clone(), agent.name.clone(), agent.ops.clone(), was_running, waiter)
        };
        if !was_running {
            self.emit(status_event(&id, &name, AgentStatus::Running, None));
        }
        if args.interrupt && was_running {
            let _ = ops.send(Op::Interrupt).await;
        }
        let _ = ops
            .send(Op::Prompt {
                input: vec![UserContent::Text {
                    text: message.to_string(),
                }],
                mode: PromptMode::Steer,
                commands: false,
            })
            .await;
        // As spawn_agent's: `background` when this starts a round of work that reports by mail.
        let details = |background: bool, reported: bool| {
            json!({ "agent_id": id, "name": name, "background": background, "reported": reported })
        };
        match waiter {
            Some(rx) => {
                let (output, reported) = self.wait(&root, &id, &name, rx, cancel).await?;
                Ok(output.with_details(details(!reported && !was_running, reported)))
            }
            None if was_running => Ok(ToolOutput::text(if args.interrupt {
                format!("\"{name}\" started working on your message. {REPORT_LATER}")
            } else {
                format!("Queued for \"{name}\"; it reads the message at its next step.")
            })
            .with_details(details(false, false))),
            None => Ok(ToolOutput::text(format!(
                "\"{name}\" started working on your message. {REPORT_LATER}"
            ))
            .with_details(details(true, false))),
        }
    }

    /// A message from sub-agent `from` (agent-messaging-design §4.2): `main` hears at its next
    /// boundary, woken if it is idle; another agent at work at its next step, and an idle one is
    /// not sent anything, as only the main agent sets agents to work. It never stops the sender.
    pub(crate) fn message_from(&self, from: &str, to: &str, message: &str) -> Result<ToolOutput, ToolError> {
        let message = message.trim();
        if message.is_empty() {
            return Err(ToolError::InvalidArgs("message must not be empty".into()));
        }
        let root = self.root()?;
        let to = to.trim();
        let closed = |name: &str| ToolError::Failed(format!("Agent \"{name}\" is closed."));
        let (sender, seq, peer) = {
            let mut state = self.state.lock();
            let Some(sender) = state.agents.iter().find(|a| a.id == from).filter(|_| !state.closing) else {
                return Err(ToolError::Failed("the session is closing".into()));
            };
            let sender = AgentRef {
                id: sender.id.clone(),
                name: sender.name.clone(),
            };
            let peer = if is_main(to) {
                None
            } else {
                if let Some((_, name)) = state.restored.iter().find(|(id, name)| name == to || id == to) {
                    return Err(closed(name));
                }
                let Some(agent) = state.agents.iter().find(|a| a.name == to || a.id == to) else {
                    let names: Vec<&str> = std::iter::once(MAIN)
                        .chain(state.restored.iter().map(|(_, name)| name.as_str()))
                        .chain(state.agents.iter().map(|a| a.name.as_str()))
                        .collect();
                    return Err(ToolError::Failed(format!("No agent named \"{to}\". Agents: {}.", names.join(", "))));
                };
                if agent.id == from {
                    return Err(ToolError::Failed("That is you.".into()));
                }
                match agent.status {
                    AgentStatus::Running => Some((agent.internal.clone(), agent.name.clone())),
                    AgentStatus::Closed => return Err(closed(&agent.name)),
                    _ => {
                        return Err(ToolError::Failed(format!(
                            "\"{}\" is not working now, and its report went to {MAIN}. Only {MAIN} can give it more work: tell {MAIN} if it should.",
                            agent.name
                        )));
                    }
                }
            };
            if peer.is_none() {
                // Counted until it arrives, like a report.
                state.in_flight += 1;
            }
            state.mails += 1;
            (sender, state.mails, peer)
        };
        match peer {
            Some((internal, name)) => {
                let mail = Mail::message(&root, sender, message, seq, false);
                if internal.send(Box::new(Internal::Mail(mail))).is_err() {
                    return Err(closed(&name));
                }
                Ok(ToolOutput::text(format!("Sent to \"{name}\"; it reads the message at its next step.")))
            }
            None => {
                let mail = Mail::message(&root, sender, message, seq, true);
                let sent = self.root.get().is_some_and(|link| link.internal.send(Box::new(Internal::Mail(mail))).is_ok());
                if !sent {
                    self.mail_received();
                    return Err(ToolError::Failed("the session is closing".into()));
                }
                Ok(ToolOutput::text(format!("Sent to {MAIN}. Keep working: your final report still follows.")))
            }
        }
    }

    /// What a bot's sub-agent `from` tells the user: a message in the bot's conversation from now
    /// on, which wakes nobody (agent-messaging-design §17).
    pub(crate) fn to_user(&self, from: &str, text: &str) -> Result<ToolOutput, ToolError> {
        let text = text.trim();
        if text.is_empty() {
            return Err(ToolError::InvalidArgs("text must not be empty".into()));
        }
        let root = self.root()?;
        let (sender, seq) = {
            let mut state = self.state.lock();
            let Some(sender) = state.agents.iter().find(|a| a.id == from).filter(|_| !state.closing) else {
                return Err(ToolError::Failed("the session is closing".into()));
            };
            let sender = AgentRef {
                id: sender.id.clone(),
                name: sender.name.clone(),
            };
            state.mails += 1;
            (sender, state.mails)
        };
        let (info, message) = mail::to_user(sender, text, seq);
        root.tell_user(info, message);
        Ok(ToolOutput::text("Sent. Keep working: your final report still follows."))
    }

    /// Waits for the report of `id`'s current round of work. New input from the user ends the
    /// wait early; the report then arrives as mail. True with the output when it is the report.
    async fn wait(
        &self,
        root: &Session,
        id: &str,
        name: &str,
        mut rx: oneshot::Receiver<Report>,
        cancel: &CancellationToken,
    ) -> Result<(ToolOutput, bool), ToolError> {
        let mut input = root.input_seq.subscribe();
        input.borrow_and_update();
        // Input that came before the wait began ends it too: the model has not read it yet.
        let queued = root.has_queued_input();
        let new_input = async {
            if !queued {
                let _ = input.changed().await;
            }
        };
        let report = tokio::select! {
            report = &mut rx => match report {
                Ok(report) => report,
                Err(_) => return Err(ToolError::Failed(format!("Agent \"{name}\" stopped without a report."))),
            },
            _ = cancel.cancelled() => match self.stop_waiting(id, &mut rx) {
                Some(report) => report,
                None => return Err(ToolError::Cancelled),
            },
            _ = new_input => match self.stop_waiting(id, &mut rx) {
                Some(report) => report,
                None => return Ok((ToolOutput::text(format!(
                    "Stopped waiting because the user sent a new message. Agent \"{name}\" keeps running. {REPORT_LATER}"
                )), false)),
            },
        };
        Ok((ToolOutput::text(mail::waited(name, &report)), true))
    }

    /// A waiting call gives up. The report goes by mail instead, unless it has just arrived.
    fn stop_waiting(&self, id: &str, rx: &mut oneshot::Receiver<Report>) -> Option<Report> {
        {
            let mut state = self.state.lock();
            if let Some(agent) = state.agents.iter_mut().find(|a| a.id == id)
                && matches!(agent.route, Route::Waiter(_))
            {
                agent.route = Route::Mail;
                return None;
            }
        }
        rx.try_recv().ok()
    }

    fn unique_name(&self, requested: Option<&str>) -> Result<String, ToolError> {
        let state = self.state.lock();
        if state.closing {
            return Err(ToolError::Failed("the session is closing".into()));
        }
        let taken = |name: &str| is_main(name) || state.agents.iter().any(|a| a.name == name) || state.restored.iter().any(|(_, n)| n == name);
        let requested = requested.map(sanitize).filter(|name| !name.is_empty());
        if let Some(name) = &requested
            && !taken(name)
        {
            return Ok(name.clone());
        }
        let (stem, first) = match requested {
            Some(name) => (name, 2),
            None => ("agent".to_string(), 1),
        };
        Ok((first..)
            .map(|n| format!("{stem}-{n}"))
            .find(|name| !taken(name))
            .unwrap_or(stem))
    }

    /// The agent started a task: busy, if it was idle.
    fn running(&self, id: &str) {
        let name = {
            let mut state = self.state.lock();
            match state.agents.iter_mut().find(|a| a.id == id) {
                Some(agent) if !matches!(agent.status, AgentStatus::Running | AgentStatus::Closed) => {
                    agent.status = AgentStatus::Running;
                    Some(agent.name.clone())
                }
                _ => None,
            }
        };
        if let Some(name) = name {
            self.emit(status_event(id, &name, AgentStatus::Running, None));
        }
    }

    fn take_redirect(&self, id: &str) -> bool {
        let mut state = self.state.lock();
        state
            .agents
            .iter_mut()
            .find(|a| a.id == id)
            .is_some_and(|a| std::mem::take(&mut a.redirect))
    }

    /// The agent has nothing left to do: its report goes to the waiting call or by mail.
    fn settle(&self, id: &str, report: Report) {
        let handover = {
            let mut state = self.state.lock();
            let closing = state.closing;
            let Some(agent) = state.agents.iter_mut().find(|a| a.id == id) else {
                return;
            };
            agent.status = report.status;
            agent.redirect = false;
            // A finished agent keeps its session for later messages, but not its tabs
            // (browser-design D-206); a later browser call starts afresh.
            if let Some(tool_state) = &agent.tool_state {
                tool_state.remove::<her_browser::BrowserSession>();
            }
            let route = std::mem::replace(&mut agent.route, Route::Mail);
            let name = agent.name.clone();
            if closing {
                None
            } else {
                // Counted until it arrives, so that the main session cannot go idle before.
                state.in_flight += 1;
                Some((name, route))
            }
        };
        let Some((name, route)) = handover else {
            return;
        };
        let mut event = status_event(id, &name, report.status, Some(&report));
        if let EventMsg::AgentStatus { error, .. } = &mut event {
            error.clone_from(&report.error);
        }
        self.emit(event);
        self.hand_over(id, &name, route, report);
        self.settled.send_modify(|n| *n += 1);
    }

    /// The agent's session ended. Unless the tree is closing, that was unexpected.
    fn closed(&self, id: &str, unexpected: bool) {
        let handover = {
            let mut state = self.state.lock();
            let closing = state.closing;
            let Some(agent) = state.agents.iter_mut().find(|a| a.id == id) else {
                return;
            };
            agent.status = AgentStatus::Closed;
            self.settled.send_modify(|n| *n += 1);
            let route = std::mem::replace(&mut agent.route, Route::Mail);
            let name = agent.name.clone();
            if closing || !unexpected {
                None
            } else {
                state.in_flight += 1;
                Some((name, route))
            }
        };
        let Some((name, route)) = handover else {
            return;
        };
        self.emit(status_event(id, &name, AgentStatus::Closed, None));
        let report = Report {
            status: AgentStatus::Failed,
            reply: None,
            error: Some(ErrorInfo::new("closed", "the agent's session stopped unexpectedly")),
            unprocessed: Vec::new(),
            usage: UsageTotals::default(),
            elapsed: Duration::ZERO,
        };
        self.hand_over(id, &name, route, report);
    }

    /// Gives a report to the waiting call, or else sends it to the main session as mail.
    /// The caller counted it as in flight.
    fn hand_over(&self, id: &str, name: &str, route: Route, report: Report) {
        let report = match route {
            Route::Waiter(tx) => match tx.send(report) {
                Ok(()) => {
                    self.mail_received();
                    return;
                }
                Err(report) => report,
            },
            Route::Mail => report,
        };
        let sent = self.root.get().zip(self.root.get().and_then(|r| r.session.upgrade())).is_some_and(|(link, root)| {
            let seq = {
                let mut state = self.state.lock();
                state.mails += 1;
                state.mails
            };
            let from = AgentRef {
                id: id.to_string(),
                name: name.to_string(),
            };
            link.internal.send(Box::new(Internal::Mail(Mail::report(&root, from, &report, seq)))).is_ok()
        });
        if !sent {
            tracing::debug!(agent = %name, "the main session is gone; report dropped");
            self.mail_received();
        }
    }
}

/// Whether a sub-agent addressing `name` means the main agent.
fn is_main(name: &str) -> bool {
    name == MAIN || name == PARENT
}

/// `agent_status`; an idle status also carries the start of the report and its cost.
fn status_event(id: &str, name: &str, status: AgentStatus, report: Option<&Report>) -> EventMsg {
    EventMsg::AgentStatus {
        agent_id: id.to_string(),
        name: name.to_string(),
        status,
        summary: report.and_then(mail::summary),
        usage: report.map(|r| r.usage.clone()),
        error: None,
        elapsed_ms: report.map(|r| r.elapsed.as_millis() as u64),
    }
}

/// What a client following a sub-agent needs of its events: what changes its conversation.
fn followed(msg: &EventMsg) -> bool {
    matches!(
        msg,
        EventMsg::TaskStarted { .. }
            | EventMsg::TurnStarted { .. }
            | EventMsg::MessageCompleted { .. }
            | EventMsg::StreamRetry { .. }
            | EventMsg::ToolStarted { .. }
            | EventMsg::ToolRunning { .. }
            | EventMsg::ToolCompleted { .. }
            | EventMsg::ApprovalRequested { .. }
            | EventMsg::ApprovalResolved { .. }
            | EventMsg::CompactionStarted { .. }
            | EventMsg::CompactionCompleted { .. }
            | EventMsg::CompactionFailed { .. }
            | EventMsg::TurnCompleted { .. }
            | EventMsg::TaskCompleted { .. }
            | EventMsg::TaskFailed { .. }
            | EventMsg::TaskInterrupted { .. }
            | EventMsg::Warning { .. }
    )
}

fn route(wait: bool) -> (Route, Option<oneshot::Receiver<Report>>) {
    if wait {
        let (tx, rx) = oneshot::channel();
        (Route::Waiter(tx), Some(rx))
    } else {
        (Route::Mail, None)
    }
}

/// Sends without waiting; a full channel gets the op from a task instead.
fn send_now(ops: &mpsc::Sender<Op>, op: Op) {
    if let Err(mpsc::error::TrySendError::Full(op)) = ops.try_send(op) {
        let ops = ops.clone();
        tokio::spawn(async move {
            let _ = ops.send(op).await;
        });
    }
}

/// A name the model can type: lowercase letters, digits, '-' and '_', at most 32 characters.
fn sanitize(name: &str) -> String {
    let mut out = String::new();
    for c in name.trim().chars() {
        let c = c.to_ascii_lowercase();
        if c.is_ascii_alphanumeric() || c == '_' {
            out.push(c);
        } else if !out.is_empty() && !out.ends_with('-') {
            out.push('-');
        }
        if out.len() >= 32 {
            break;
        }
    }
    out.trim_end_matches('-').to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn what_an_agent_said_is_cut_to_one_line() {
        assert_eq!(said_line("  First the logs.\n\n  Then the  rest. "), "First the logs. Then the rest.");
        let long = said_line(&"说".repeat(500));
        assert_eq!(long.chars().count(), SAID_CHARS + 1);
        assert!(long.ends_with('…'));
    }

    #[test]
    fn names_are_made_typeable() {
        assert_eq!(sanitize("  Explore API  "), "explore-api");
        assert_eq!(sanitize("tests/parser!!"), "tests-parser");
        assert_eq!(sanitize("测试"), "");
        assert_eq!(sanitize(&"a".repeat(40)).len(), 32);
    }
}
