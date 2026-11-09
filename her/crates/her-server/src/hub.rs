//! One task per loaded session: the only place that orders events, attaches, detaches and
//! operations for that session. It never waits on a connection.

use her_core::Kernel;
use her_core::skills::SkillRef;
use her_llm::Timestamp;
use her_protocol::ApprovalDecision;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::Op;
use her_protocol::api::DetachReason;
use her_protocol::api::EventFilter;
use her_protocol::api::SessionDetachedParams;
use her_protocol::api::SessionInfo;
use her_protocol::api::SessionSnapshot;
use her_protocol::api::TranscriptItem;
use her_protocol::api::TranscriptPage;
use her_rpc::RpcError;
use std::collections::HashSet;
use std::sync::Arc;
use std::sync::OnceLock;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::AtomicU32;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::AtomicUsize;
use std::sync::atomic::Ordering;
use std::time::Duration;
use std::time::Instant;
use tokio::sync::mpsc;
use tokio::sync::oneshot;
use tokio::sync::watch;
use tokio::sync::mpsc::error::TrySendError;

use crate::conn::ConnHandle;
use crate::conn::ConnId;
use crate::conn::Outgoing;
use crate::conn::PushError;
use crate::errors;
use crate::frame::EventFrame;
use crate::frame::SessionShared;
use crate::list;
use crate::list::ListFeed;
use crate::mirror::SessionMirror;
use her_protocol::api::SessionState;
use her_protocol::api::SessionSummary;

const CONTROL_CAPACITY: usize = 64;

/// Turns the snapshot into what the requester sends back, queued before any later event.
pub type Responder = Box<dyn FnOnce(&SessionSnapshot) -> Vec<Outgoing> + Send>;
/// Turns the outcome of an operation into what the requester sends back.
pub type OpResponder = Box<dyn FnOnce(Result<(), RpcError>) -> Vec<Outgoing> + Send>;
pub type DetachResponder = Box<dyn FnOnce() -> Vec<Outgoing> + Send>;

pub struct AttachRequest {
    pub conn: ConnHandle,
    pub filter: EventFilter,
    /// Transcript items in the snapshot; `None` for all of them.
    pub history_limit: Option<usize>,
    pub respond: Responder,
}

pub(crate) enum HubCmd {
    /// An operation the user asked for from the extension.
    FromExtension {
        op: Op,
    },
    Attach(AttachRequest),
    Detach {
        conn: ConnHandle,
        respond: Option<DetachResponder>,
    },
    Op {
        conn: ConnHandle,
        op: Op,
        /// Reject the operation while a task runs (a prompt from a client that cannot queue).
        require_idle: bool,
        respond: OpResponder,
    },
    History {
        before: String,
        limit: usize,
        reply: oneshot::Sender<Option<TranscriptPage>>,
    },
    Transcript {
        reply: oneshot::Sender<(SessionInfo, Vec<TranscriptItem>)>,
    },
    Info {
        reply: oneshot::Sender<SessionInfo>,
    },
    /// The session's skills and whether skills are on. Answered after every earlier command,
    /// an attach's reply included, is queued.
    Skills {
        reply: oneshot::Sender<(Arc<[SkillRef]>, bool)>,
    },
    /// The tools the session has now.
    Tools {
        reply: oneshot::Sender<Vec<her_protocol::api::ToolEntry>>,
    },
    /// What fills the session's context window; `None` while it has no kernel.
    Context {
        reply: oneshot::Sender<Option<her_protocol::ContextReport>>,
    },
    /// The session's files for the file panel (file-panel-design §9); `None` while it has no kernel.
    FileScope {
        reply: oneshot::Sender<Option<her_core::files::FileScope>>,
    },
    Close {
        reply: Option<oneshot::Sender<()>>,
    },
    /// Unload the session if nothing uses it: no client attached, no task running. Replies
    /// whether it is being unloaded.
    Unload {
        reply: oneshot::Sender<bool>,
    },
    ConnGone {
        conn: ConnId,
    },
    /// A metadata operation (title, archive) from the server, which needs no attached connection.
    Meta {
        op: Op,
        reply: oneshot::Sender<Result<(), RpcError>>,
    },
    /// The browsers' and desktop control's settings, after the configuration changed.
    System(Box<her_core::SystemSettings>),
    /// A bot's conversation: the bot's new name (bot-design §2.4).
    BotName { name: String },
    /// A bot's conversation: what its model is to know of its routines (routine-design §4.4).
    Routines { text: String },
    /// A bot's conversation: starts a routine's run (routine-design §7.2). `None` when the
    /// session is closing, so another attempt can load it again.
    StartRun {
        run: her_core::routine::RoutineRun,
        reply: oneshot::Sender<Option<Result<oneshot::Receiver<her_core::routine::RunOutcome>, String>>>,
    },
    StopRun { run_id: String },
    /// The conversation's secure cards, `card_id` taken back from its history if need be; `None`
    /// when it has none.
    Cards {
        card_id: String,
        reply: oneshot::Sender<Option<Arc<her_core::cards::Cards>>>,
    },
    /// A run started here has ended.
    RunEnded,
}

/// Where a session's changes are listed.
pub(crate) enum Listing {
    /// The session list (`sessions/changed`).
    Sessions(Arc<ListFeed>),
    /// A bot's conversation: its bot's row instead (bot-design §5.1).
    Bot {
        id: String,
        bots: Arc<crate::bots::Registry>,
        routines: Arc<crate::routines::Routines>,
    },
}

/// What the session's log says about it that its events do not: when it was created and how
/// its first message starts.
pub struct SummaryBase {
    pub created_at: String,
    pub preview: Option<String>,
    /// When a task last started or ended (the creation time before any): what "recently active"
    /// sorts by. Loading, closing, renaming, archiving or changing settings leave it alone.
    pub updated_at: String,
    /// Started without a project.
    pub no_project: bool,
}

/// State readable without asking the hub.
pub struct HubShared {
    pub session: Arc<SessionShared>,
    pub busy: AtomicBool,
    /// Routines' runs under way: none shows, but the session stays loaded for them
    /// (routine-design §7.2).
    pub routine_runs: AtomicUsize,
    pub attached: AtomicU32,
    /// The session as the list shows it, kept current by the hub.
    pub summary: parking_lot::Mutex<SessionSummary>,
    /// When the session last became unused (no client attached, no task running), on
    /// [`idle_clock`]; 0 while it is in use or closing. A full server unloads the session
    /// unused the longest.
    pub idle_since: AtomicU64,
    /// Each MCP server's latest status in this session, for the 自定义 page (`mcp/list`).
    pub mcp: parking_lot::Mutex<std::collections::BTreeMap<String, her_protocol::McpServerInfo>>,
    /// The approval that waits for the user to do something in a page.
    pub handoff: parking_lot::Mutex<Option<Handoff>>,
    done: watch::Receiver<bool>,
}

/// An approval that waits for the user to do something in a page.
pub struct Handoff {
    /// The sub-agent that asks, if one does.
    pub agent: Option<String>,
    pub call_id: String,
    /// What the user is asked to do.
    pub what: String,
}

impl Handoff {
    /// What says the user did it.
    pub fn done(&self) -> Op {
        let (call_id, decision) = (self.call_id.clone(), ApprovalDecision::Approve);
        match &self.agent {
            Some(agent) => Op::AgentApproval { agent: agent.clone(), call_id, decision },
            None => Op::Approval { call_id, decision },
        }
    }
}

/// An approval among those answered. Sessions choose their call ids apart from each other, so a
/// sub-agent's goes by its id too.
fn answered_key(agent: Option<&str>, call_id: &str) -> String {
    match agent {
        Some(agent) => format!("{agent}/{call_id}"),
        None => call_id.to_string(),
    }
}

/// Milliseconds since the first call, plus one, so that 0 can mean "in use".
pub fn idle_clock() -> u64 {
    static START: OnceLock<Instant> = OnceLock::new();
    START.get_or_init(Instant::now).elapsed().as_millis() as u64 + 1
}

impl HubShared {
    /// Waits until the hub has closed the session.
    pub async fn wait_closed(&self) {
        let mut done = self.done.clone();
        let _ = done.wait_for(|d| *d).await;
    }
}

#[derive(Clone)]
pub struct HubHandle {
    /// Commands travel boxed so that the channel's slots stay small while the hub is idle.
    pub(crate) tx: mpsc::Sender<Box<HubCmd>>,
    pub shared: Arc<HubShared>,
}

impl HubHandle {
    pub(crate) async fn send(&self, cmd: HubCmd) -> Result<(), RpcError> {
        self.tx
            .send(Box::new(cmd))
            .await
            .map_err(|_| errors::session_closed(&self.shared.session.id))
    }
}

struct Subscriber {
    conn: ConnHandle,
    filter: EventFilter,
}

struct Hub {
    shared: Arc<HubShared>,
    kernel: Option<Kernel>,
    ops: mpsc::Sender<Op>,
    mirror: SessionMirror,
    subscribers: Vec<Subscriber>,
    /// Approvals forwarded to the kernel and not yet confirmed by `approval_resolved`.
    answered: HashSet<String>,
    control: mpsc::Receiver<Box<HubCmd>>,
    control_open: bool,
    idle_after: Option<Duration>,
    idle_deadline: Option<tokio::time::Instant>,
    closing: bool,
    close_reason: DetachReason,
    close_waiters: Vec<oneshot::Sender<()>>,
    /// Called with the session's last summary once it is closed.
    on_exit: Option<Box<dyn FnOnce(SessionSummary) + Send>>,
    done: watch::Sender<bool>,
    listing: Listing,
    base: SummaryBase,
    /// When the session last got busy: a task started while it was idle. Sub-agents keep it busy
    /// after that task ends, until `idle`.
    busy_since: Option<Timestamp>,
}

/// Starts the hub of a freshly started or resumed kernel. `first` attaches before any event
/// is consumed, so its snapshot has sequence number 0 and it sees every event.
pub(crate) fn spawn(
    mut kernel: Kernel,
    transcript: Vec<TranscriptItem>,
    idle_after: Option<Duration>,
    first: Option<AttachRequest>,
    listing: Listing,
    base: SummaryBase,
    on_exit: Box<dyn FnOnce(SessionSummary) + Send>,
) -> HubHandle {
    let info = kernel.info();
    let (tx, control) = mpsc::channel(CONTROL_CAPACITY);
    let (done_tx, done_rx) = watch::channel(false);
    let mut mirror = SessionMirror::new(info, transcript);
    mirror.restore_agents(kernel.take_restored_agents());
    let shared = Arc::new(HubShared {
        session: SessionShared::new(&mirror.info().session_id),
        busy: AtomicBool::new(false),
        routine_runs: AtomicUsize::new(0),
        attached: AtomicU32::new(0),
        summary: parking_lot::Mutex::new(summary_of(&mirror, &base, true, 0)),
        idle_since: AtomicU64::new(0),
        mcp: parking_lot::Mutex::new(std::collections::BTreeMap::new()),
        handoff: parking_lot::Mutex::new(None),
        done: done_rx,
    });
    let mut hub = Hub {
        shared: shared.clone(),
        ops: kernel.op_sender(),
        kernel: Some(kernel),
        mirror,
        subscribers: Vec::new(),
        answered: HashSet::new(),
        control,
        control_open: true,
        idle_after,
        idle_deadline: None,
        closing: false,
        close_reason: DetachReason::Closed,
        close_waiters: Vec::new(),
        on_exit: Some(on_exit),
        done: done_tx,
        listing,
        base,
        busy_since: None,
    };
    if let Some(first) = first {
        hub.attach(first);
    }
    hub.update_idle();
    tokio::spawn(hub.run());
    HubHandle { tx, shared }
}

async fn next_event(kernel: &mut Option<Kernel>) -> Option<Event> {
    match kernel {
        Some(kernel) => kernel.next_event().await,
        None => std::future::pending().await,
    }
}

async fn sleep_until(deadline: Option<tokio::time::Instant>) {
    match deadline {
        Some(deadline) => tokio::time::sleep_until(deadline).await,
        None => std::future::pending().await,
    }
}

impl Hub {
    async fn run(mut self) {
        loop {
            let deadline = self.idle_deadline;
            tokio::select! {
                event = next_event(&mut self.kernel) => match event {
                    Some(event) => {
                        let closed = matches!(event.msg, EventMsg::SessionClosed);
                        self.dispatch(event);
                        if closed {
                            break;
                        }
                    }
                    None => break,
                },
                cmd = self.control.recv(), if self.control_open => match cmd {
                    Some(cmd) => self.handle(*cmd),
                    None => {
                        self.control_open = false;
                        self.begin_close(None);
                    }
                },
                _ = sleep_until(deadline), if deadline.is_some() => self.on_idle(),
            }
        }
        self.finish().await;
    }

    fn dispatch(&mut self, event: Event) {
        self.mirror.apply(&event);
        if let Listing::Bot { id, bots, routines } = &self.listing {
            bots.on_event(id, &event, &self.mirror);
            routines.on_event(id, &event);
        }
        // The session's approvals, and a sub-agent's as its `agent_event` carries them.
        let (agent, asked) = match &event.msg {
            EventMsg::AgentEvent { agent_id, event } => (Some(agent_id.as_str()), &event.msg),
            msg => (None, msg),
        };
        match asked {
            EventMsg::ApprovalRequested { call_id, name, args, .. } => {
                // The browser's tools are deferred: the model runs them through `call_tool`.
                let (name, args) = if name == "call_tool" { (args.get("name").and_then(|v| v.as_str()).unwrap_or_default(), args.get("arguments")) } else { (name.as_str(), Some(args)) };
                if let Some(what) = args.and_then(|a| a.get("user")).and_then(|v| v.as_str()).filter(|_| name == "browser_wait") {
                    *self.shared.handoff.lock() = Some(Handoff {
                        agent: agent.map(str::to_string),
                        call_id: call_id.clone(),
                        what: what.to_string(),
                    });
                }
            }
            EventMsg::ApprovalResolved { call_id, .. } => {
                self.answered.remove(&answered_key(agent, call_id));
                let mut handoff = self.shared.handoff.lock();
                if handoff.as_ref().is_some_and(|h| &h.call_id == call_id && h.agent.as_deref() == agent) {
                    *handoff = None;
                }
            }
            _ => {}
        }
        match &event.msg {
            EventMsg::McpServerStatus { server, state, tools, error, .. } => {
                self.shared.mcp.lock().insert(
                    server.clone(),
                    her_protocol::McpServerInfo { server: server.clone(), state: *state, tools: *tools, error: error.clone() },
                );
            }
            EventMsg::TaskStarted { .. } => {
                if !self.shared.busy.swap(true, Ordering::Relaxed) {
                    self.busy_since = Some(event.ts);
                }
                self.base.updated_at = list::rfc3339(event.ts);
            }
            // Sub-agents may still work after a task ends: busy until the session is idle.
            msg if msg.is_task_end() => self.base.updated_at = list::rfc3339(event.ts),
            EventMsg::Idle => {
                self.shared.busy.store(false, Ordering::Relaxed);
                self.busy_since = None;
            }
            _ => {}
        }
        if self.changes_summary(&event.msg) {
            self.publish();
        }
        if !self.subscribers.is_empty() {
            let frame = Arc::new(EventFrame::new(self.shared.session.clone(), event));
            let class = frame.class();
            let mut dropped = Vec::new();
            for (i, sub) in self.subscribers.iter().enumerate() {
                if class.is_some_and(|c| sub.filter.exclude.contains(&c)) {
                    continue;
                }
                if let Err(err) = sub.conn.queue.push_event(frame.clone()) {
                    dropped.push((i, err));
                }
            }
            for (i, err) in dropped.into_iter().rev() {
                let sub = self.subscribers.remove(i);
                if err == PushError::Overflow {
                    tracing::warn!(session = %self.shared.session.id, conn = sub.conn.id, "connection fell behind; detached");
                    sub.conn.queue.push(Outgoing::Detached(self.detached(DetachReason::Overflow)));
                }
            }
            self.count_attached();
        }
        self.update_idle();
    }

    /// Whether a list row would look different after this event. Output and progress do not
    /// count, so a long task does not flood the list.
    fn changes_summary(&mut self, msg: &EventMsg) -> bool {
        match msg {
            EventMsg::SessionStarted { .. }
            | EventMsg::TaskStarted { .. }
            | EventMsg::ApprovalRequested { .. }
            | EventMsg::ApprovalResolved { .. }
            | EventMsg::SettingsChanged { .. }
            | EventMsg::TitleChanged { .. }
            | EventMsg::ArchivedChanged { .. }
            | EventMsg::Idle => true,
            msg if msg.is_task_end() => true,
            // What a sub-agent asks shows in the list like the session's own.
            EventMsg::AgentEvent { event, .. } => matches!(event.msg, EventMsg::ApprovalRequested { .. } | EventMsg::ApprovalResolved { .. }),
            // Back before the first message: the one that takes its place becomes the preview.
            EventMsg::SessionRewound { .. } => {
                if !self.mirror.has_user_message() {
                    self.base.preview = None;
                }
                true
            }
            // A new session's first message becomes its preview.
            EventMsg::MessageCompleted {
                message: her_llm::Message::User(user),
                ..
            } if self.base.preview.is_none() => {
                self.base.preview = Some(preview(&user.text_content()));
                true
            }
            _ => false,
        }
    }

    fn publish(&self) {
        let mut summary = summary_of(&self.mirror, &self.base, true, self.subscribers.len() as u32);
        // Busy until the session is idle, as in session lists: sub-agents may outlive the task.
        // While they work, lists show the whole stretch as one running task.
        summary.busy = self.working();
        if summary.state == SessionState::Idle && self.mirror.agents_running() {
            summary.state = SessionState::Running;
        }
        if summary.state == SessionState::Running
            && let Some(since) = self.busy_since
        {
            summary.task_started_at = Some(list::rfc3339(since));
        }
        *self.shared.summary.lock() = summary.clone();
        if let Listing::Sessions(feed) = &self.listing {
            feed.publish(vec![summary], Vec::new());
        }
    }

    fn detached(&self, reason: DetachReason) -> SessionDetachedParams {
        SessionDetachedParams {
            session_id: self.shared.session.id.to_string(),
            reason,
            last_seq: self.mirror.last_seq(),
        }
    }

    fn count_attached(&self) {
        self.shared.attached.store(self.subscribers.len() as u32, Ordering::Relaxed);
    }

    fn attach(&mut self, req: AttachRequest) {
        let snapshot = self.mirror.snapshot(req.history_limit);
        req.conn.queue.push_all((req.respond)(&snapshot));
        self.subscribers.retain(|s| s.conn.id != req.conn.id);
        self.subscribers.push(Subscriber {
            conn: req.conn,
            filter: req.filter,
        });
        self.count_attached();
        self.update_idle();
    }

    fn handle(&mut self, cmd: HubCmd) {
        match cmd {
            HubCmd::Attach(req) => self.attach(req),
            HubCmd::Detach { conn, respond } => {
                self.subscribers.retain(|s| s.conn.id != conn.id);
                self.count_attached();
                if let Some(respond) = respond {
                    conn.queue.push_all(respond());
                }
                self.update_idle();
            }
            HubCmd::Op {
                conn,
                op,
                require_idle,
                respond,
            } => {
                let result = self.submit(conn.id, op, require_idle);
                conn.queue.push_all(respond(result));
            }
            // The user asked for it from the extension, which attaches to no session.
            HubCmd::FromExtension { op } => {
                if let Err(err) = self.apply(op, false) {
                    tracing::debug!(%err, "an operation from the extension");
                }
            }
            HubCmd::History { before, limit, reply } => {
                let _ = reply.send(self.mirror.history(&before, limit));
            }
            HubCmd::Transcript { reply } => {
                let _ = reply.send((self.mirror.info().clone(), self.mirror.transcript().to_vec()));
            }
            HubCmd::Info { reply } => {
                let _ = reply.send(self.mirror.info().clone());
            }
            HubCmd::Skills { reply } => {
                let skills = match &self.kernel {
                    Some(kernel) => kernel.skills(),
                    None => (Arc::from(Vec::new()), false),
                };
                let _ = reply.send(skills);
            }
            HubCmd::Tools { reply } => {
                let _ = reply.send(self.kernel.as_ref().map(|kernel| kernel.tools()).unwrap_or_default());
            }
            HubCmd::Context { reply } => {
                let _ = reply.send(self.kernel.as_ref().and_then(|kernel| kernel.context()));
            }
            HubCmd::FileScope { reply } => {
                let _ = reply.send(self.kernel.as_ref().and_then(|kernel| kernel.file_scope()));
            }
            HubCmd::Close { reply } => {
                self.close_reason = DetachReason::Closed;
                self.begin_close(reply);
            }
            HubCmd::Unload { reply } => {
                let _ = reply.send(self.unload_if_unused());
            }
            HubCmd::ConnGone { conn } => {
                self.subscribers.retain(|s| s.conn.id != conn);
                self.count_attached();
                self.update_idle();
            }
            HubCmd::System(settings) => {
                if let Some(kernel) = &self.kernel {
                    kernel.set_system_connectors(*settings);
                }
            }
            HubCmd::BotName { name } => {
                if let Some(kernel) = &self.kernel {
                    kernel.set_bot_name(name);
                }
            }
            HubCmd::Routines { text } => {
                if let Some(kernel) = &self.kernel {
                    kernel.set_routines(text);
                }
            }
            HubCmd::StartRun { run, reply } => {
                let started = match &self.kernel {
                    Some(kernel) if !self.closing => Some(kernel.start_run(run)),
                    _ => None,
                };
                if matches!(started, Some(Ok(_))) {
                    self.shared.routine_runs.fetch_add(1, Ordering::Relaxed);
                    self.update_idle();
                }
                let _ = reply.send(started);
            }
            HubCmd::StopRun { run_id } => {
                if let Some(kernel) = &self.kernel {
                    kernel.stop_run(&run_id);
                }
            }
            HubCmd::Cards { card_id, reply } => {
                let _ = reply.send(self.kernel.as_ref().and_then(|kernel| kernel.cards(&card_id)));
            }
            HubCmd::RunEnded => {
                self.shared.routine_runs.fetch_sub(1, Ordering::Relaxed);
                self.update_idle();
            }
            HubCmd::Meta { op, reply } => {
                let result = if self.closing {
                    Err(errors::session_closed(&self.shared.session.id))
                } else {
                    self.forward(op)
                };
                let _ = reply.send(result);
            }
        }
    }

    fn submit(&mut self, conn: ConnId, op: Op, require_idle: bool) -> Result<(), RpcError> {
        let id = &self.shared.session.id;
        if !self.subscribers.iter().any(|s| s.conn.id == conn) {
            return Err(errors::not_attached(id));
        }
        self.apply(op, require_idle)
    }

    fn apply(&mut self, op: Op, require_idle: bool) -> Result<(), RpcError> {
        let id = &self.shared.session.id;
        if self.closing {
            return Err(errors::session_closed(id));
        }
        if require_idle && self.mirror.busy() {
            return Err(errors::prompt_in_progress(id));
        }
        match &op {
            Op::Shutdown => {
                self.close_reason = DetachReason::Closed;
                self.begin_close(None);
                return Ok(());
            }
            Op::Approval { call_id, .. } => {
                if !self.mirror.approval_pending(call_id) || self.answered.contains(call_id) {
                    return Err(errors::approval_not_pending(call_id));
                }
                let call_id = call_id.clone();
                self.forward(op)?;
                self.answered.insert(call_id);
                return Ok(());
            }
            Op::AgentApproval { agent, call_id, .. } => {
                let key = answered_key(Some(agent), call_id);
                if !self.mirror.agent_approval_pending(agent, call_id) || self.answered.contains(&key) {
                    return Err(errors::approval_not_pending(call_id));
                }
                self.forward(op)?;
                self.answered.insert(key);
                return Ok(());
            }
            _ => {}
        }
        self.forward(op)
    }

    fn forward(&self, op: Op) -> Result<(), RpcError> {
        self.ops.try_send(op).map_err(|err| match err {
            TrySendError::Full(_) => errors::overloaded(),
            TrySendError::Closed(_) => errors::session_closed(&self.shared.session.id),
        })
    }

    fn unused(&self) -> bool {
        self.subscribers.is_empty() && !self.working() && self.shared.routine_runs.load(Ordering::Relaxed) == 0 && !self.closing
    }

    fn update_idle(&mut self) {
        let idle = self.unused();
        if !idle {
            self.shared.idle_since.store(0, Ordering::Relaxed);
        } else if self.shared.idle_since.load(Ordering::Relaxed) == 0 {
            self.shared.idle_since.store(idle_clock(), Ordering::Relaxed);
        }
        match (idle, self.idle_after) {
            (true, Some(after)) => {
                if self.idle_deadline.is_none() {
                    self.idle_deadline = Some(tokio::time::Instant::now() + after);
                }
            }
            _ => self.idle_deadline = None,
        }
    }

    /// A task runs, or sub-agents are still working.
    fn working(&self) -> bool {
        self.mirror.busy() || self.shared.busy.load(Ordering::Relaxed)
    }

    fn on_idle(&mut self) {
        self.idle_deadline = None;
        if self.unload_if_unused() {
            tracing::info!(session = %self.shared.session.id, "unloading idle session");
        }
    }

    /// Closes the session when nothing uses it. Its log is on disk, and resuming loads it again.
    fn unload_if_unused(&mut self) -> bool {
        if !self.unused() {
            return false;
        }
        self.close_reason = DetachReason::Unloaded;
        self.begin_close(None);
        true
    }

    fn begin_close(&mut self, reply: Option<oneshot::Sender<()>>) {
        if let Some(reply) = reply {
            self.close_waiters.push(reply);
        }
        if self.closing {
            return;
        }
        self.closing = true;
        self.idle_deadline = None;
        self.shared.idle_since.store(0, Ordering::Relaxed);
        match self.ops.try_send(Op::Shutdown) {
            Ok(()) => {}
            Err(TrySendError::Full(op)) => {
                let ops = self.ops.clone();
                tokio::spawn(async move {
                    let _ = ops.send(op).await;
                });
            }
            Err(TrySendError::Closed(_)) => {}
        }
    }

    async fn finish(mut self) {
        if let Some(kernel) = self.kernel.take() {
            // The session loop has ended; this waits for the log to be closed.
            let _ = kernel.shutdown().await;
        }
        self.shared.busy.store(false, Ordering::Relaxed);
        let reason = self.close_reason;
        for sub in std::mem::take(&mut self.subscribers) {
            sub.conn.queue.push(Outgoing::Detached(self.detached(reason)));
        }
        self.count_attached();
        if let Some(on_exit) = self.on_exit.take() {
            let mut last = summary_of(&self.mirror, &self.base, false, 0);
            last.task_started_at = None;
            on_exit(last);
        }
        for waiter in self.close_waiters.drain(..) {
            let _ = waiter.send(());
        }
        let _ = self.done.send(true);
    }
}

/// The list row of a loaded session (or of one just closed, with `loaded` false).
fn summary_of(mirror: &SessionMirror, base: &SummaryBase, loaded: bool, attached: u32) -> SessionSummary {
    let info = mirror.info();
    let state = match mirror.state() {
        SessionState::Failed => SessionState::Failed,
        _ if !loaded => SessionState::Idle,
        state => state,
    };
    SessionSummary {
        session_id: info.session_id.clone(),
        cwd: info.cwd.clone(),
        no_project: base.no_project,
        created_at: base.created_at.clone(),
        updated_at: base.updated_at.clone(),
        preview: base.preview.clone(),
        title: info.title.clone(),
        state,
        model: (!info.model.is_empty()).then(|| info.model.clone()),
        archived: info.archived,
        task_started_at: mirror.task_started().map(list::rfc3339),
        loaded,
        busy: loaded && mirror.busy(),
        attached_clients: attached,
    }
}

/// The start of a first message, as a list shows it: its first line, at most 120 characters.
pub(crate) fn preview(text: &str) -> String {
    let line = text.lines().next().unwrap_or("").trim();
    let mut out: String = line.chars().take(120).collect();
    if out.len() < line.len() {
        out.push('…');
    }
    out
}
