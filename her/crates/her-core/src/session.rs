//! Session state shared by the submission loop, the running task and tool executions.

use her_llm::Context;
use her_llm::Message;
use her_llm::Model;
use her_llm::ModelRegistry;
use her_llm::ThinkingLevel;
use her_llm::Usage;
use her_llm::UsageTotals;
use her_llm::UserMessage;
use her_llm::now_ms;
use her_protocol::ApprovalDecision;
use her_protocol::CommandRule;
use her_protocol::EventMsg;
use her_protocol::MailInfo;
use her_protocol::MailKind;
use her_protocol::PermissionMode;
use her_protocol::PromptMode;
use her_protocol::QueuedInput;
use her_protocol::SiteGrant;
use her_protocol::SkillUse;
use her_protocol::WarningCode;
use her_tools::Environment;
use her_tools::FileTracker;
use her_tools::PathLocks;
use her_tools::ToolRegistry;
use parking_lot::Mutex;
use std::collections::HashMap;
use std::collections::VecDeque;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::Weak;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::AtomicU64;
use tokio::sync::oneshot;
use tokio::sync::watch;
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

use crate::agents::AgentTree;
use crate::agents::Mail;
use crate::config::Config;
use crate::context;
use crate::emitter::Emitter;
use crate::history::History;
use crate::hooks::Hooks;
use crate::hooks::SessionView;
use crate::mcp::McpManager;
use crate::mcp::tools::Shown;
use crate::skills::SkillRef;
use crate::store::CardResolvedEntry;
use crate::store::Entry;
use crate::store::EntryKind;
use crate::store::SessionWriter;
use crate::store::SkillText;

/// User input on its way into the history: the message as the user wrote it, and the
/// instructions of the skills it mentions, read when it arrived.
pub(crate) struct Input {
    /// Its entry's id once it joins the history, given when it arrived: queued input is known
    /// by it (`withdraw`, `requeue`).
    pub(crate) id: String,
    pub(crate) message: UserMessage,
    pub(crate) skills: Vec<SkillText>,
}

/// A `resend` waiting for the running task to stop.
pub(crate) struct Resend {
    /// The user message the conversation goes back to just before.
    pub(crate) target: String,
    /// What takes its place.
    pub(crate) input: Input,
}

pub(crate) struct ActiveTask {
    pub(crate) id: String,
    pub(crate) cancel: CancellationToken,
    /// The running turn's token, a child of `cancel`: `steer_now` ends the turn alone.
    pub(crate) turn: Arc<Mutex<CancellationToken>>,
    pub(crate) handle: Option<JoinHandle<()>>,
}

pub(crate) struct SessionState {
    pub(crate) history: History,
    /// What is sent: `base_prompt`, then `mcp_section` when there is one.
    pub(crate) system_prompt: Arc<str>,
    /// Fixed when the session started, and kept on resume.
    pub(crate) base_prompt: Arc<str>,
    /// What follows `base_prompt`: the guidelines of tools that came after it was written (a
    /// browser turned on later, connectors-entry-design §9.4), then the MCP server catalog and
    /// instructions. Changes only with those.
    pub(crate) mcp_section: Option<String>,
    /// The skills of the session, frozen with the system prompt (its catalog lists the ones
    /// the model may load). Sub-agents get the main session's.
    pub(crate) skills: Arc<[SkillRef]>,
    pub(crate) model: Arc<Model>,
    pub(crate) thinking: ThinkingLevel,
    pub(crate) permission_mode: PermissionMode,
    pub(crate) session_rules: Vec<CommandRule>,
    /// Site permissions from "approve for this session"; in memory only, like the rules.
    pub(crate) site_grants: Vec<SiteGrant>,
    pub(crate) active_task: Option<ActiveTask>,
    pub(crate) steer: VecDeque<Input>,
    pub(crate) follow_up: VecDeque<Input>,
    /// Goes ahead when the running task has ended.
    pub(crate) pending_resend: Option<Resend>,
    pub(crate) pending_approvals: HashMap<String, oneshot::Sender<ApprovalDecision>>,
    pub(crate) pending_model: Option<(Arc<Model>, ThinkingLevel)>,
    pub(crate) usage: UsageTotals,
    pub(crate) next_task_no: u32,
    /// Correction factor for token estimates, learned from reported usage.
    pub(crate) estimate_ratio: f64,
    /// Where measuring the largest turn stopped; see `budget`.
    pub(crate) turn_scan: crate::budget::TurnScan,
    /// Sub-agents' reports and messages, and how cards ended, not yet in the history, oldest first.
    pub(crate) inbox: VecDeque<Delivery>,
    /// News on its way into the history, by its id, with who waits to hear that it is there
    /// (`Kernel::news`).
    pub(crate) news: HashMap<String, Vec<oneshot::Sender<()>>>,
    /// What sub-agents told the user during the running task, which joins the history at its next
    /// boundary (agent-messaging-design §17).
    pub(crate) told: Vec<Told>,
    /// The last task was interrupted: reports wait for new input instead of starting a task.
    pub(crate) hold: bool,
    /// `Idle` was sent and nothing has started since.
    pub(crate) idle: bool,
    /// The name given with `set_title`, and whether the session is archived.
    pub(crate) title: Option<String>,
    pub(crate) archived: bool,
    /// A bot's session: its names (bot-design §2.4).
    pub(crate) bot: Option<BotNames>,
    /// A bot's session: what the model is to know of its routines, once the service said
    /// (routine-design §4.4).
    pub(crate) routines: Option<String>,
    /// A bot's session: what its sub-agents' prompts start from instead of `base_prompt`,
    /// built like a session's (bot-design §2.3).
    pub(crate) sub_agent_prompt: Option<Arc<str>>,
    /// A bot's session: which `bot.md` its prompt comes from (bot-turn-design §8).
    pub(crate) prompt_template: Option<String>,
}

/// What a sub-agent told the user, not yet in the history. The user has it: `id` is the message's
/// in the event they got it by.
pub(crate) struct Told {
    id: String,
    info: MailInfo,
    message: Message,
}

/// A bot session's names.
pub(crate) struct BotNames {
    /// The one its system prompt gives.
    pub(crate) given: String,
    /// The bot's name now.
    pub(crate) current: String,
}

impl SessionState {
    pub(crate) fn new(
        history: History,
        system_prompt: Arc<str>,
        model: Arc<Model>,
        thinking: ThinkingLevel,
        permission_mode: PermissionMode,
    ) -> Self {
        Self {
            history,
            base_prompt: system_prompt.clone(),
            system_prompt,
            mcp_section: None,
            skills: Arc::from([]),
            model,
            thinking,
            permission_mode,
            session_rules: Vec::new(),
            site_grants: Vec::new(),
            active_task: None,
            steer: VecDeque::new(),
            follow_up: VecDeque::new(),
            pending_resend: None,
            pending_approvals: HashMap::new(),
            pending_model: None,
            usage: UsageTotals::default(),
            next_task_no: 0,
            estimate_ratio: 1.0,
            turn_scan: crate::budget::TurnScan::default(),
            inbox: VecDeque::new(),
            news: HashMap::new(),
            told: Vec::new(),
            hold: false,
            idle: false,
            title: None,
            archived: false,
            bot: None,
            routines: None,
            sub_agent_prompt: None,
            prompt_template: None,
        }
    }

    /// A waiting report would start a task now. Stopping the bot's work holds back the reports
    /// of that work; a routine's is a new thing the owner asked for, and it would be lost with
    /// the session if it waited (routine-design §2.4). So is how a card the owner answered ended.
    pub(crate) fn has_waking_mail(&self) -> bool {
        self.inbox.iter().any(|delivery| match delivery {
            Delivery::Mail(mail) => mail.wake && (!self.hold || mail.info.kind == MailKind::Routine),
            Delivery::Card(_) => true,
        })
    }

    /// Whether `id` is a message of the user's on the current branch, which `resend` may name.
    pub(crate) fn has_user_message(&self, id: &str) -> bool {
        self.history.branch_back().any(|entry| entry.id == id && is_user_message(entry))
    }

    /// The id of input that arrives now: free in the history and among the input that waits.
    pub(crate) fn input_id(&self) -> String {
        loop {
            let id = self.history.new_id();
            if !self.steer.iter().chain(&self.follow_up).any(|input| input.id == id) && !self.told.iter().any(|told| told.id == id) {
                return id;
            }
        }
    }
}

/// A message the user wrote: reports (`mail`) and what the kernel adds are other kinds of entry.
fn is_user_message(entry: &Entry) -> bool {
    matches!(&entry.kind, EntryKind::Message { message: Message::User(_), .. })
}

/// What waits in a session's inbox: a sub-agent's or a routine run's mail, or how a card ended.
pub(crate) enum Delivery {
    Mail(Mail),
    Card(Box<CardResolvedEntry>),
}

/// Messages from inside the process, handled by the submission loop in order with the ops.
/// They travel boxed: a channel keeps a block of 32 slots for as long as it lives, and slots
/// sized for a mail would cost every session 4 KiB.
pub(crate) enum Internal {
    /// A sub-agent's report for this session.
    Mail(Mail),
    /// How one of its cards ended.
    Card(Box<CardResolvedEntry>),
    /// The main session's permissions changed; a sub-agent takes them over.
    Permissions {
        mode: PermissionMode,
        rules: Vec<CommandRule>,
        grants: Vec<SiteGrant>,
    },
}

pub(crate) struct Session {
    pub(crate) id: String,
    pub(crate) llm: Arc<ModelRegistry>,
    /// The tool list; replaced between requests when MCP tools in it change, or the system
    /// connectors do.
    pub(crate) tools: Mutex<Arc<ToolRegistry>>,
    /// Built-in tools and the agent tools: what every session of the tree has.
    pub(crate) core_tools: Arc<ToolRegistry>,
    /// `core_tools` and the search tools: everything but the MCP tools that go into the list
    /// directly. Replaced when the system connectors change.
    pub(crate) base_tools: Mutex<Arc<ToolRegistry>>,
    /// The browsers' and desktop control's settings, shared with the sub-agents.
    pub(crate) system: Arc<crate::system::SystemCell>,
    /// Their generation in `base_tools`, the handles and the prompt.
    pub(crate) system_applied: AtomicU64,
    /// The process's browsers, when it has any.
    pub(crate) browser_pool: Option<Arc<her_browser::BrowserPool>>,
    /// The main session's id: the desktop app shows its sub-agents' browsers in its window.
    pub(crate) root_id: String,
    /// The agent tree's MCP servers, shared with sub-agents.
    pub(crate) mcp: Option<Arc<McpManager>>,
    /// MCP generation in `tools` and the system prompt.
    pub(crate) mcp_applied: AtomicU64,
    /// The first request already waited for the MCP servers.
    pub(crate) mcp_settled: AtomicBool,
    /// What tool searches showed since the last compaction.
    pub(crate) mcp_shown: Arc<Mutex<Shown>>,
    /// The browsers' and computer use's tools, out of the tool list (computer-use-design §5).
    pub(crate) deferred: crate::deferred::DeferredCell,
    pub(crate) env: Arc<dyn Environment>,
    pub(crate) hooks: Vec<Arc<dyn Hooks>>,
    pub(crate) config: Arc<Config>,
    pub(crate) emitter: Arc<Emitter>,
    pub(crate) writer: SessionWriter,
    /// Read lock for read-only tools, write lock for mutating ones.
    pub(crate) tool_gate: tokio::sync::RwLock<()>,
    pub(crate) tool_slots: tokio::sync::Semaphore,
    pub(crate) files: Arc<FileTracker>,
    /// Full output of cut tool results: `tool-output` in the session's data directory.
    pub(crate) output_dir: PathBuf,
    /// The data directory is temporary (no session log) and goes away with the session.
    pub(crate) temporary_data: Option<PathBuf>,
    /// Estimate of the system prompt and the tool definitions, in parts; see `budget`.
    pub(crate) fixed_tokens: Mutex<Option<crate::context_report::Fixed>>,
    /// The last context report and what it was made from; see `context_report`.
    pub(crate) context_report: Mutex<Option<(crate::context_report::ReportKey, her_protocol::ContextReport)>>,
    /// The messages of the last request with their estimates; see `estimate_request`.
    pub(crate) request_estimates: Mutex<Vec<(her_llm::Message, u64)>>,
    /// her's data directory, canonical.
    pub(crate) data_dir: PathBuf,
    /// See [`SessionView::own_folder`].
    pub(crate) own_folder: Option<PathBuf>,
    /// See [`SessionView::skill_roots`].
    pub(crate) skill_roots: Arc<[PathBuf]>,
    pub(crate) trusted: bool,
    pub(crate) interactive_approvals: bool,
    /// One lock per file, shared with the sub-agents: changes to a file never interleave.
    pub(crate) locks: Arc<PathLocks>,
    /// The sub-agents of a main session. `None` for a sub-agent, which cannot start any.
    pub(crate) agents: Option<Arc<AgentTree>>,
    /// A sub-agent's session.
    pub(crate) sub_agent: bool,
    /// What it can do about other agents.
    pub(crate) agent_role: crate::deferred::AgentRole,
    /// A sub-agent's main session, which holds the permissions the whole tree follows.
    pub(crate) main: Option<Weak<Session>>,
    /// Shared with the sub-agents: whoever holds it may ask the user, so that the tree asks one
    /// question at a time. Fair: they ask in the order they came.
    pub(crate) approval_line: Arc<tokio::sync::Mutex<()>>,
    /// What the kernel's tools keep for this session: its sub-agents, its browser.
    pub(crate) tool_state: Arc<her_tools::SessionState>,
    /// Bumped whenever input is queued during a task; tools waiting for a sub-agent watch it.
    pub(crate) input_seq: watch::Sender<u64>,
    /// Never held across an await, and no I/O happens under it.
    pub(crate) state: Mutex<SessionState>,
}

impl Session {
    pub(crate) fn emit(&self, msg: EventMsg) {
        self.emitter.emit(msg);
    }

    /// The current tool list.
    pub(crate) fn tools(&self) -> Arc<ToolRegistry> {
        self.tools.lock().clone()
    }

    /// A tool to run by its name: in the tool list, or a deferred built-in tool the model called
    /// directly rather than through call_tool, as it may once it has seen the definition.
    pub(crate) fn tool(&self, tools: &ToolRegistry, name: &str) -> Option<Arc<dyn her_tools::Tool>> {
        tools.get(name).or_else(|| self.deferred.lock().find(name).map(|(_, tool)| tool.clone()))
    }

    pub(crate) fn warn(&self, message: impl Into<String>) {
        let message = message.into();
        tracing::warn!(session = %self.id, "{message}");
        self.emit(EventMsg::Warning { message, code: None });
    }

    /// The bypass-permissions warning, sent when the session starts, resumes or switches into it.
    pub(crate) fn warn_bypass(&self) {
        tracing::warn!(session = %self.id, "{BYPASS_WARNING}");
        self.emit(EventMsg::Warning {
            message: BYPASS_WARNING.to_string(),
            code: Some(WarningCode::BypassPermissions),
        });
    }

    pub(crate) fn allocate_id(&self) -> String {
        self.state.lock().history.new_id()
    }

    /// Adds an entry to the in-memory history and queues it for the log, in the same order.
    pub(crate) fn record(&self, kind: EntryKind) -> Entry {
        let mut state = self.state.lock();
        let id = state.history.new_id();
        let entry = state.history.append(id, kind, now_ms());
        self.writer.append(entry.clone());
        entry
    }

    pub(crate) fn record_with_id(&self, id: String, kind: EntryKind) -> Entry {
        let mut state = self.state.lock();
        let entry = state.history.append(id, kind, now_ms());
        self.writer.append(entry.clone());
        entry
    }

    pub(crate) fn record_user(&self, input: Input) {
        let message = Message::from(input.message);
        let uses = input
            .skills
            .iter()
            .map(|skill| SkillUse {
                name: skill.name.clone(),
                path: skill.path.clone(),
            })
            .collect();
        let entry = self.record_with_id(
            input.id,
            EntryKind::Message {
                message: message.clone(),
                skills: input.skills,
                skill: None,
            },
        );
        self.emit(EventMsg::MessageCompleted {
            message_id: entry.id,
            message,
            mail: None,
            skills: uses,
        });
        if self.state.lock().bot.is_some() {
            self.record(EntryKind::ReplyNudge);
        }
    }

    /// Gives the model today's date when the context last gave another one, or none: the
    /// system prompt keeps the date the session started on (technical-design §7.2).
    pub(crate) fn note_date(&self) {
        let today = chrono::Local::now().date_naive();
        let given = {
            let state = self.state.lock();
            crate::context::date_in_context(&state.history, &state.base_prompt)
        };
        if given != Some(today) {
            self.record(EntryKind::Date {
                date: today.format("%Y-%m-%d").to_string(),
            });
        }
    }

    /// Gives a bot's new name, as `note_date` does a new date (bot-design §2.4).
    pub(crate) fn note_bot_name(&self) {
        let renamed = {
            let state = self.state.lock();
            let Some(bot) = &state.bot else {
                return;
            };
            let known = crate::context::name_in_context(&state.history).unwrap_or(&bot.given);
            (known != bot.current).then(|| bot.current.clone())
        };
        if let Some(name) = renamed {
            self.record(EntryKind::BotName { name });
        }
    }

    /// What a bot's session still owes its owner (bot-turn-design §6).
    pub(crate) fn owed(&self) -> crate::bot::Owed {
        let state = self.state.lock();
        if state.bot.is_some() {
            crate::bot::owed(&state.history)
        } else {
            crate::bot::Owed::Nothing
        }
    }

    /// Gives a bot's routines as they are now, when the context last gave another list. A bot
    /// that never had any hears nothing of them (routine-design §4.4).
    pub(crate) fn note_routines(&self) {
        let changed = {
            let state = self.state.lock();
            let Some(text) = &state.routines else {
                return;
            };
            match crate::context::routines_in_context(&state.history) {
                Some(given) => given != text,
                None => text != crate::routine::NO_ROUTINES,
            }
            .then(|| text.clone())
        };
        if let Some(text) = changed {
            self.record(EntryKind::Routines { text });
        }
    }

    /// A sub-agent told the user something: they have it from now on, and no task starts for it.
    /// The model reads it where it reads mail, never between a call and its result, so while a
    /// task runs it joins the history at the next boundary (agent-messaging-design §17).
    pub(crate) fn tell_user(&self, info: MailInfo, message: UserMessage) {
        let mut guard = self.state.lock();
        let state = &mut *guard;
        let id = state.input_id();
        let message = Message::from(message);
        // Under the lock, so that it cannot overtake the start of a task it is written before.
        self.emit(EventMsg::MessageCompleted {
            message_id: id.clone(),
            message: message.clone(),
            mail: Some(info.clone()),
            skills: Vec::new(),
        });
        let told = Told { id, info, message };
        if state.active_task.is_none() {
            self.write_told(state, told);
        } else {
            state.told.push(told);
        }
    }

    fn write_told(&self, state: &mut SessionState, told: Told) {
        let kind = EntryKind::AgentToUser {
            mail: Box::new(told.info),
            message: told.message,
        };
        let entry = state.history.append(told.id, kind, now_ms());
        self.writer.append(entry);
    }

    /// What sub-agents told the user joins the history: at a boundary, or as a task ends.
    pub(crate) fn write_all_told(&self, state: &mut SessionState) {
        for told in std::mem::take(&mut state.told) {
            self.write_told(state, told);
        }
    }

    /// Writes what sub-agents told the user and the waiting reports, messages and cards' ends into
    /// the history, oldest first. True when a routine's report or a card's end is among them.
    pub(crate) fn deliver_mail(&self) -> bool {
        let deliveries: Vec<Delivery> = {
            let mut guard = self.state.lock();
            let state = &mut *guard;
            self.write_all_told(state);
            state.inbox.drain(..).collect()
        };
        let new = deliveries.iter().any(|delivery| match delivery {
            Delivery::Mail(mail) => mail.info.kind == MailKind::Routine,
            Delivery::Card(_) => true,
        });
        for delivery in deliveries {
            let mail = match delivery {
                Delivery::Mail(mail) => mail,
                Delivery::Card(card) => {
                    self.record(EntryKind::CardResolved(card));
                    continue;
                }
            };
            let message = Message::from(mail.message);
            let info = Box::new(mail.info.clone());
            let entry = self.record(match mail.info.kind {
                MailKind::Report | MailKind::Routine | MailKind::News => EntryKind::Mail { mail: info, message: message.clone() },
                MailKind::Message => EntryKind::AgentMessage { mail: info, message: message.clone() },
                // It never waits in the inbox: see `tell_user`.
                MailKind::ToUser => EntryKind::AgentToUser { mail: info, message: message.clone() },
            });
            // Whoever brought the news hears that the conversation has it.
            if mail.info.kind == MailKind::News {
                for waiting in self.state.lock().news.remove(&mail.info.id).into_iter().flatten() {
                    let _ = waiting.send(());
                }
            }
            self.emit(EventMsg::MessageCompleted {
                message_id: entry.id,
                message,
                mail: Some(mail.info),
                skills: Vec::new(),
            });
        }
        new
    }

    /// Sends `Idle` once nothing is left to do: no task, no queued input, no report that
    /// would start a task, and every sub-agent idle.
    pub(crate) fn check_idle(&self) {
        {
            let state = self.state.lock();
            let quiet = state.active_task.is_none()
                && state.steer.is_empty()
                && state.follow_up.is_empty()
                && !state.has_waking_mail();
            if !quiet || state.idle {
                return;
            }
        }
        if self.agents.as_ref().is_some_and(|tree| !tree.settled()) {
            return;
        }
        let mut state = self.state.lock();
        if state.active_task.is_none() && !state.idle {
            state.idle = true;
            // Under the lock, so that it cannot overtake the start of a new task.
            self.emit(EventMsg::Idle);
        }
    }

    pub(crate) fn take_steer(&self) -> Vec<Input> {
        let mut state = self.state.lock();
        // Going back: what waits is handed back rather than joining what is taken back.
        if state.steer.is_empty() || state.pending_resend.is_some() {
            return Vec::new();
        }
        let taken = state.steer.drain(..).collect();
        self.emit_queue(&state);
        taken
    }

    pub(crate) fn has_steer(&self) -> bool {
        !self.state.lock().steer.is_empty()
    }

    /// Input that waits for the running task, in either queue.
    pub(crate) fn has_queued_input(&self) -> bool {
        let state = self.state.lock();
        !state.steer.is_empty() || !state.follow_up.is_empty()
    }

    pub(crate) fn take_follow_up(&self) -> Option<Input> {
        let mut state = self.state.lock();
        if state.pending_resend.is_some() {
            return None;
        }
        let taken = state.follow_up.pop_front();
        if taken.is_some() {
            self.emit_queue(&state);
        }
        taken
    }

    /// `queue_changed`, under the state lock so that events keep the order of the changes.
    pub(crate) fn emit_queue(&self, state: &SessionState) {
        let entries = |inputs: &VecDeque<Input>, mode| {
            inputs
                .iter()
                .map(|input| QueuedInput {
                    id: input.id.clone(),
                    message: input.message.clone(),
                    mode,
                })
                .collect::<Vec<_>>()
        };
        let mut queued = entries(&state.steer, PromptMode::Steer);
        queued.extend(entries(&state.follow_up, PromptMode::FollowUp));
        self.emit(EventMsg::QueueChanged { queued });
    }

    /// `withdraw`: takes waiting input out of the queue. Once it has joined the history there
    /// is nothing to take, and nothing is said: its `message_completed` came before.
    pub(crate) fn withdraw(&self, id: &str) {
        let mut guard = self.state.lock();
        let state = &mut *guard;
        let found = [&mut state.steer, &mut state.follow_up]
            .into_iter()
            .any(|inputs| match inputs.iter().position(|input| input.id == id) {
                Some(i) => inputs.remove(i).is_some(),
                None => false,
            });
        if found {
            self.emit(EventMsg::InputWithdrawn { id: id.to_string() });
            self.emit_queue(state);
        }
    }

    /// A `resend` that cannot go ahead: nothing changes, and its input stays with the client.
    pub(crate) fn reject_resend(&self, why: &str) {
        self.emit(EventMsg::Warning {
            message: format!("cannot resend: {why}"),
            code: Some(WarningCode::ResendRejected),
        });
    }

    /// `resend`: the conversation goes back to just before `target`, a user message on the branch.
    /// The log keeps what is taken back; what the session holds because of it goes
    /// (docs/kernel/message-resend-design.md §3.4, §3.5).
    pub(crate) fn rewind(&self, state: &mut SessionState, target: &str, returned_input: Vec<UserMessage>) {
        let (parent, before_first, removed, again) = {
            let branch = state.history.branch();
            // It was on the branch when accepted, and only this takes an entry off the branch.
            let Some(at) = branch.iter().position(|entry| entry.id == target) else {
                return;
            };
            let taken_back = &branch[at..];
            let removed: Vec<String> = taken_back
                .iter()
                .filter_map(|entry| match &entry.kind {
                    EntryKind::AgentSpawned(spawned) => Some(spawned.agent_id.clone()),
                    _ => None,
                })
                .collect();
            // An agent started before keeps its place, and the model, back where it was still
            // waiting for this report or message, gets it again.
            let again: Vec<Mail> = taken_back
                .iter()
                .filter_map(|entry| match &entry.kind {
                    EntryKind::Mail { mail, message: Message::User(user) } | EntryKind::AgentMessage { mail, message: Message::User(user) } if !removed.contains(&mail.from.id) => Some(Mail {
                        info: (**mail).clone(),
                        message: (**user).clone(),
                        wake: false,
                    }),
                    _ => None,
                })
                .collect();
            let before_first = !branch[..at].iter().any(|entry| is_user_message(entry));
            (branch[at].parent.clone(), before_first, removed, again)
        };
        let id = state.history.new_id();
        let marker = state.history.rewind(id, parent, before_first, now_ms());
        self.writer.append(marker);
        state.inbox.retain(|delivery| !matches!(delivery, Delivery::Mail(mail) if removed.contains(&mail.info.from.id)));
        for mail in again.into_iter().rev() {
            state.inbox.push_front(Delivery::Mail(mail));
        }
        // New input ends the hold after an interruption.
        state.hold = false;
        // The context no longer holds what the model read or the tools a search showed it.
        self.files.clear();
        self.mcp_shown.lock().clear();
        if let Some(tree) = &self.agents {
            tree.retire(&removed);
        }
        self.emit(EventMsg::SessionRewound {
            message_id: target.to_string(),
            removed_agents: removed,
            returned_input,
        });
    }

    /// `requeue`: moves waiting input to the end of the other mode.
    pub(crate) fn requeue(&self, id: &str, mode: PromptMode) {
        let mut guard = self.state.lock();
        let state = &mut *guard;
        let (from, to) = match mode {
            PromptMode::Steer => (&mut state.follow_up, &mut state.steer),
            PromptMode::FollowUp => (&mut state.steer, &mut state.follow_up),
        };
        let Some(input) = from.iter().position(|input| input.id == id).and_then(|i| from.remove(i)) else {
            return;
        };
        to.push_back(input);
        self.emit_queue(state);
    }

    /// `steer_now`: ends the running turn so that waiting steering input joins the history now.
    /// Not while an approval waits: answering it goes first, and the input follows.
    pub(crate) fn steer_now(&self) {
        let state = self.state.lock();
        if state.steer.is_empty() || !state.pending_approvals.is_empty() {
            return;
        }
        if let Some(task) = &state.active_task {
            task.turn.lock().cancel();
        }
    }

    pub(crate) fn is_busy(&self) -> bool {
        self.state.lock().active_task.is_some()
    }

    pub(crate) fn current_model(&self) -> (Arc<Model>, ThinkingLevel) {
        let state = self.state.lock();
        (state.model.clone(), state.thinking)
    }

    pub(crate) fn add_usage(&self, model_ref: &str, usage: &Usage) {
        self.state.lock().usage.add(model_ref, usage);
    }

    /// System prompt, normalized history and tool specs, after the context hooks.
    pub(crate) fn build_context(&self) -> Context {
        let (system, messages) = {
            let state = self.state.lock();
            let branch = state.history.branch();
            (state.system_prompt.to_string(), context::branch_messages(&branch))
        };
        let mut ctx = Context {
            system,
            messages: context::normalize(messages),
            tools: self.tools().specs(),
        };
        for hook in &self.hooks {
            hook.transform_context(&mut ctx);
        }
        ctx
    }

    pub(crate) fn view(&self) -> SessionView {
        let state = self.state.lock();
        SessionView {
            permission_mode: state.permission_mode,
            config_rules: self.config.rules.clone(),
            session_rules: state.session_rules.clone(),
            trusted: self.trusted,
            env: self.env.clone(),
            data_dir: self.data_dir.clone(),
            own_folder: self.own_folder.clone(),
            skill_roots: self.skill_roots.clone(),
            site_grants: state.site_grants.clone(),
            browser: self.system.get().browser.policy.clone(),
            desktop: self.system.get().desktop.clone(),
        }
    }

    /// Stops the current task and, in a main session, the work of every sub-agent.
    pub(crate) fn interrupt(&self) {
        if let Some(task) = &self.state.lock().active_task {
            task.cancel.cancel();
        }
        if let Some(tree) = &self.agents {
            tree.interrupt_all();
        }
    }

    /// `agent_interrupt`: stops one sub-agent and nothing else.
    pub(crate) fn interrupt_agent(&self, agent: &str) {
        let stopped = self.agents.as_ref().is_some_and(|tree| tree.interrupt_one(agent));
        if !stopped {
            self.warn(format!("no sub-agent \"{agent}\" is running"));
        }
    }

    /// `agent_approval`: the user's answer to what a sub-agent asks.
    pub(crate) fn answer_agent(&self, agent: &str, call_id: &str, decision: ApprovalDecision) {
        let sent = self.agents.as_ref().is_some_and(|tree| tree.answer(agent, call_id, decision));
        if !sent {
            self.warn(format!("no sub-agent \"{agent}\" to answer"));
        }
    }

    pub(crate) fn resolve_approval(&self, call_id: &str, decision: ApprovalDecision) {
        let sender = self.state.lock().pending_approvals.remove(call_id);
        match sender {
            Some(tx) => {
                let _ = tx.send(decision);
            }
            None => self.warn(format!("no approval is pending for call {call_id}")),
        }
    }

    pub(crate) fn emit_settings(&self) {
        let (model, thinking, permission_mode, context_window) = {
            let state = self.state.lock();
            (
                state.model.reference(),
                state.thinking,
                state.permission_mode,
                u64::from(state.model.context_window),
            )
        };
        self.emit(EventMsg::SettingsChanged {
            model,
            thinking,
            permission_mode,
            context_window,
        });
    }

    fn change_model(&self, model: Arc<Model>, thinking: ThinkingLevel) {
        {
            let mut state = self.state.lock();
            state.model = model.clone();
            state.thinking = thinking;
        }
        self.record(EntryKind::Settings {
            model: Some(model.reference()),
            thinking: Some(thinking),
            permission_mode: None,
        });
        self.emit_settings();
    }

    /// Takes effect immediately when idle, otherwise at the next turn boundary.
    pub(crate) fn set_model(&self, reference: &str, thinking: Option<ThinkingLevel>) {
        let resolved = match self.llm.resolve(reference) {
            Ok(resolved) => resolved,
            Err(err) => {
                self.warn(format!("cannot switch model: {err}"));
                return;
            }
        };
        if resolved.ad_hoc {
            self.warn(format!("model '{reference}' is not in the catalog; default limits apply"));
        }
        // A level chosen here is remembered for the model; a model switch keeps the session's
        // level when the new model offers it, else takes the level that model starts with
        // (docs/kernel/model-catalog-design.md §18).
        let thinking = match thinking {
            Some(level) => {
                let level = resolved.model.resolve_thinking(level);
                if let Err(err) = self.llm.directory().remember_thinking(&resolved.model.reference(), level) {
                    tracing::warn!("cannot remember the thinking level: {}", err.message);
                }
                level
            }
            None => {
                let current = self.state.lock().thinking;
                crate::models::thinking_for(&self.llm, &resolved.model, current)
            }
        };
        if self.is_busy() {
            self.state.lock().pending_model = Some((resolved.model, thinking));
        } else {
            self.change_model(resolved.model, thinking);
        }
    }

    pub(crate) fn apply_pending_model(&self) {
        let pending = self.state.lock().pending_model.take();
        if let Some((model, thinking)) = pending {
            self.change_model(model, thinking);
        }
    }

    pub(crate) fn set_permission_mode(&self, mode: PermissionMode) {
        let previous = std::mem::replace(&mut self.state.lock().permission_mode, mode);
        self.record(EntryKind::Settings {
            model: None,
            thinking: None,
            permission_mode: Some(mode),
        });
        self.emit_settings();
        if mode == PermissionMode::BypassPermissions && previous != mode {
            self.warn_bypass();
        }
        self.share_permissions();
    }

    /// A rule from "approve for this session".
    pub(crate) fn add_session_rule(&self, rule: CommandRule) {
        self.state.lock().session_rules.push(rule);
        self.share_permissions();
    }

    /// A site permission from "approve for this session".
    pub(crate) fn add_site_grant(&self, grant: SiteGrant) {
        {
            let mut state = self.state.lock();
            if !state.site_grants.contains(&grant) {
                state.site_grants.push(grant);
            }
        }
        self.sync_browser_grants();
        self.share_permissions();
    }

    /// "Always allow": the grant goes into the user's configuration as well (D-197). A
    /// failure to write is a warning; the grant still holds for the session.
    pub(crate) fn remember_site(&self, grant: &SiteGrant) {
        let file = self.data_dir.join("config.toml");
        if let Err(err) = crate::site_policy::remember(&file, grant) {
            self.emit(EventMsg::Warning {
                message: format!("could not save the site permission in {}: {err}", file.display()),
                code: None,
            });
        }
    }

    /// Navigations the page makes may reach the private sites the session allowed.
    fn sync_browser_grants(&self) {
        if let Some(browser) = self.tool_state.get::<crate::browser::BrowserHandle>() {
            browser.set_grants(self.state.lock().site_grants.clone());
        }
    }

    fn permissions(&self) -> (PermissionMode, Vec<CommandRule>, Vec<SiteGrant>) {
        let state = self.state.lock();
        (state.permission_mode, state.session_rules.clone(), state.site_grants.clone())
    }

    /// Sub-agents always judge tool calls by the main session's current permissions.
    fn share_permissions(&self) {
        if let Some(tree) = &self.agents {
            let (mode, rules, grants) = self.permissions();
            tree.share_permissions(mode, rules, grants);
        }
    }

    /// A sub-agent's main session, while it runs.
    pub(crate) fn main_session(&self) -> Option<Arc<Session>> {
        self.main.as_ref().and_then(Weak::upgrade)
    }

    /// A sub-agent takes over the main session's permissions now, not when they arrive.
    pub(crate) fn follow_main(&self) {
        if let Some(main) = self.main_session() {
            let (mode, rules, grants) = main.permissions();
            self.apply_permissions(mode, rules, grants);
        }
    }

    /// A sub-agent takes over the main session's permissions.
    pub(crate) fn apply_permissions(&self, mode: PermissionMode, rules: Vec<CommandRule>, grants: Vec<SiteGrant>) {
        let changed = {
            let mut state = self.state.lock();
            state.session_rules = rules;
            state.site_grants = grants;
            std::mem::replace(&mut state.permission_mode, mode) != mode
        };
        self.sync_browser_grants();
        if changed {
            self.record(EntryKind::Settings {
                model: None,
                thinking: None,
                permission_mode: Some(mode),
            });
            self.emit_settings();
        }
    }

    /// Names the session. Allowed while a task runs: it only adds a log entry.
    pub(crate) fn set_title(&self, title: &str) {
        let Some(title) = clean_title(title) else {
            self.warn("ignored an empty session title");
            return;
        };
        if self.state.lock().title.as_deref() == Some(title.as_str()) {
            return;
        }
        self.state.lock().title = Some(title.clone());
        self.record(EntryKind::Title { title: title.clone() });
        self.emit(EventMsg::TitleChanged { title });
    }

    pub(crate) fn set_archived(&self, archived: bool) {
        if std::mem::replace(&mut self.state.lock().archived, archived) == archived {
            return;
        }
        self.record(EntryKind::Archived { archived });
        self.emit(EventMsg::ArchivedChanged { archived });
    }
}

/// Longest session title, in characters.
pub const MAX_TITLE_CHARS: usize = 200;

/// A title as stored: one line, trimmed, at most `MAX_TITLE_CHARS`; `None` when nothing is left.
pub fn clean_title(title: &str) -> Option<String> {
    let line = title.split_whitespace().collect::<Vec<_>>().join(" ");
    let cut: String = line.chars().take(MAX_TITLE_CHARS).collect();
    (!cut.is_empty()).then_some(cut)
}

/// Shown whenever a session starts, resumes or switches into bypass-permissions.
const BYPASS_WARNING: &str = "Permission mode bypass-permissions: tools run without asking, \
    including changes to protected files such as .git and shell profiles and to files outside the \
    workspace. Only deny rules, ask rules and deleting critical paths still stop an action. Use it \
    only in an isolated environment such as a container or a throwaway checkout.";
