//! The session as the events describe it, kept by the session hub to answer attaches with a
//! snapshot that lines up exactly with the following events.

use her_core::RestoredAgent;
use her_llm::Timestamp;
use her_llm::UsageTotals;
use her_protocol::AgentStatus;
use her_protocol::CompletionReason;
use her_protocol::Delta;
use her_protocol::ErrorInfo;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::Role;
use her_protocol::api::ActiveTask;
use her_protocol::api::AgentSnapshot;
use her_protocol::api::PartialBlock;
use her_protocol::api::PartialMessage;
use her_protocol::api::PendingApproval;
use her_protocol::api::SessionInfo;
use her_protocol::api::SessionSnapshot;
use her_protocol::api::QueuedInput;
use her_protocol::api::SessionState;
use her_protocol::api::TaskOutcome;
use her_protocol::api::ToolState;
use her_protocol::api::TranscriptItem;
use her_protocol::api::TranscriptPage;

/// Progress output kept per running tool.
const PROGRESS_TAIL: usize = 16 * 1024;

pub struct SessionMirror {
    info: SessionInfo,
    last_seq: u64,
    transcript: Vec<TranscriptItem>,
    active: Option<ActiveTask>,
    usage: UsageTotals,
    /// When the running task started.
    task_started: Option<Timestamp>,
    /// The last task failed.
    failed: bool,
    /// Latest status of each MCP server, by name.
    mcp_servers: std::collections::BTreeMap<String, her_protocol::McpServerInfo>,
    /// Sub-agents started since the session was loaded, in the order they started.
    agents: Vec<AgentMirror>,
    /// Prompts waiting for the running task, oldest first.
    queued: Vec<QueuedInput>,
    /// Secure cards shown and not ended, oldest first: their ids, and what a bot's row says while
    /// each waits (docs/her-secure-cards.md §3.5).
    cards: Vec<(String, String)>,
}

/// A sub-agent: what `agent_started` and `agent_status` said, and its own session as its
/// `agent_event`s describe it.
struct AgentMirror {
    agent_id: String,
    name: String,
    description: String,
    call_id: String,
    status: AgentStatus,
    since: Timestamp,
    summary: Option<String>,
    usage: Option<UsageTotals>,
    error: Option<ErrorInfo>,
    elapsed_ms: Option<u64>,
    session: SessionMirror,
}

impl SessionMirror {
    pub fn new(info: SessionInfo, transcript: Vec<TranscriptItem>) -> Self {
        let failed = last_task_failed(&transcript);
        let mut cards = Vec::new();
        for item in &transcript {
            match item {
                TranscriptItem::ToolResult { result, .. } => cards.extend(card_shown(result)),
                TranscriptItem::CardResolved { card_id, .. } => cards.retain(|(id, _)| id != card_id),
                _ => {}
            }
        }
        Self {
            info,
            last_seq: 0,
            transcript,
            active: None,
            usage: UsageTotals::default(),
            task_started: None,
            failed,
            mcp_servers: std::collections::BTreeMap::new(),
            agents: Vec::new(),
            queued: Vec::new(),
            cards,
        }
    }

    pub fn info(&self) -> &SessionInfo {
        &self.info
    }

    /// What the session is doing, as lists show it.
    pub fn state(&self) -> SessionState {
        if self.first_approval().is_some() {
            return SessionState::AwaitingApproval;
        }
        match &self.active {
            Some(_) => SessionState::Running,
            None if self.failed => SessionState::Failed,
            None => SessionState::Idle,
        }
    }

    pub fn task_started(&self) -> Option<Timestamp> {
        self.task_started
    }

    pub fn last_seq(&self) -> u64 {
        self.last_seq
    }

    pub fn busy(&self) -> bool {
        self.active.is_some()
    }

    /// A sub-agent is working.
    pub fn agents_running(&self) -> bool {
        self.agents.iter().any(|a| a.status == AgentStatus::Running)
    }

    /// How many sub-agents are working.
    pub fn agents_working(&self) -> u32 {
        self.agents.iter().filter(|a| a.status == AgentStatus::Running).count() as u32
    }

    /// What the oldest pending approval asks about, the session's own or a sub-agent's.
    pub fn first_approval(&self) -> Option<&str> {
        self.active
            .as_ref()
            .and_then(|task| task.approvals.first())
            .map(|a| a.summary.as_str())
            .or_else(|| self.agents.iter().filter(|a| a.status == AgentStatus::Running).find_map(|a| a.session.first_approval()))
    }

    /// What the oldest card waiting for the owner says: the words above it, else its title.
    pub fn first_card(&self) -> Option<&str> {
        self.cards.first().map(|(_, waits)| waits.as_str())
    }

    pub fn approval_pending(&self, call_id: &str) -> bool {
        self.active
            .as_ref()
            .is_some_and(|task| task.approvals.iter().any(|a| a.call_id == call_id))
    }

    /// A sub-agent's approval, which its own session's mirror holds.
    pub fn agent_approval_pending(&self, agent: &str, call_id: &str) -> bool {
        self.agents.iter().any(|a| a.agent_id == agent && a.session.approval_pending(call_id))
    }

    pub fn transcript(&self) -> &[TranscriptItem] {
        &self.transcript
    }

    /// Whether the conversation holds a message of the user's: reports (`mail`) do not count.
    pub fn has_user_message(&self) -> bool {
        self.transcript.iter().any(|item| {
            matches!(
                item,
                TranscriptItem::Message {
                    message: her_llm::Message::User(_),
                    mail: None,
                    ..
                }
            )
        })
    }

    pub fn apply(&mut self, event: &Event) {
        self.last_seq = event.seq;
        match &event.msg {
            EventMsg::SessionStarted {
                model,
                cwd,
                session_file,
                thinking,
                permission_mode,
                context_window,
                usage,
                ..
            } => {
                self.info.model = model.clone();
                self.info.cwd = cwd.clone();
                self.info.session_file = session_file.clone();
                self.info.thinking = *thinking;
                self.info.permission_mode = *permission_mode;
                self.info.context_window = *context_window;
                self.usage = usage.clone();
            }
            EventMsg::McpServerStatus { server, state, tools, error, .. } => {
                self.mcp_servers.insert(
                    server.clone(),
                    her_protocol::McpServerInfo {
                        server: server.clone(),
                        state: *state,
                        tools: *tools,
                        error: error.clone(),
                    },
                );
            }
            EventMsg::SettingsChanged {
                model,
                thinking,
                permission_mode,
                context_window,
            } => {
                self.info.model = model.clone();
                self.info.thinking = *thinking;
                self.info.permission_mode = *permission_mode;
                if *context_window > 0 {
                    self.info.context_window = *context_window;
                }
            }
            EventMsg::TaskStarted { task_id, .. } => {
                self.active = Some(ActiveTask {
                    task_id: task_id.clone(),
                    turn: 0,
                    streaming: None,
                    tools: Vec::new(),
                    approvals: Vec::new(),
                });
                self.task_started = Some(event.ts);
            }
            EventMsg::TitleChanged { title } => self.info.title = Some(title.clone()),
            EventMsg::ArchivedChanged { archived } => self.info.archived = *archived,
            EventMsg::TurnStarted { turn, .. } => {
                if let Some(task) = &mut self.active {
                    task.turn = *turn;
                }
            }
            EventMsg::MessageStarted {
                message_id,
                role: Role::Assistant,
            } => {
                if let Some(task) = &mut self.active {
                    task.streaming = Some(PartialMessage {
                        message_id: message_id.clone(),
                        blocks: Vec::new(),
                    });
                }
            }
            EventMsg::StreamRetry { .. } => {
                if let Some(task) = &mut self.active {
                    task.streaming = None;
                }
            }
            EventMsg::MessageDelta { message_id, delta } => {
                if let Some(partial) = self
                    .active
                    .as_mut()
                    .and_then(|t| t.streaming.as_mut())
                    .filter(|p| &p.message_id == message_id)
                {
                    apply_delta(partial, delta);
                }
            }
            EventMsg::MessageCompleted {
                message_id,
                message,
                mail,
                skills,
            } => {
                self.transcript.push(TranscriptItem::Message {
                    message_id: message_id.clone(),
                    message: message.clone(),
                    skills: skills.clone(),
                    mail: mail.clone().map(Box::new),
                });
                if let Some(task) = &mut self.active
                    && task.streaming.as_ref().is_some_and(|p| &p.message_id == message_id)
                {
                    task.streaming = None;
                }
            }
            EventMsg::ToolStarted {
                call_id,
                name,
                args,
                category,
                title,
                ..
            } => {
                if let Some(task) = &mut self.active {
                    task.tools.push(ToolState {
                        call_id: call_id.clone(),
                        name: name.clone(),
                        category: *category,
                        title: title.clone(),
                        args: args.clone(),
                        running: false,
                        progress_tail: String::new(),
                    });
                }
            }
            EventMsg::ToolRunning { call_id } => {
                if let Some(tool) = self.tool_mut(call_id) {
                    tool.running = true;
                }
            }
            EventMsg::ToolProgress { call_id, text } => {
                if let Some(tool) = self.tool_mut(call_id) {
                    tool.progress_tail.push_str(text);
                    keep_tail(&mut tool.progress_tail, PROGRESS_TAIL);
                }
            }
            EventMsg::ToolCompleted { call_id, result } => {
                self.cards.extend(card_shown(result));
                self.transcript.push(TranscriptItem::ToolResult {
                    call_id: call_id.clone(),
                    result: result.clone(),
                });
                if let Some(task) = &mut self.active {
                    task.tools.retain(|t| &t.call_id != call_id);
                    task.approvals.retain(|a| &a.call_id != call_id);
                }
            }
            EventMsg::ApprovalRequested {
                call_id,
                name,
                args,
                summary,
                reason,
                proposed_rule,
                proposed_mode,
                proposed_grants,
                own_browser,
            } => {
                if let Some(task) = &mut self.active {
                    task.approvals.push(PendingApproval {
                        call_id: call_id.clone(),
                        name: name.clone(),
                        args: args.clone(),
                        summary: summary.clone(),
                        reason: reason.clone(),
                        proposed_rule: proposed_rule.clone(),
                        proposed_mode: *proposed_mode,
                        proposed_grants: proposed_grants.clone(),
                        own_browser: *own_browser,
                    });
                }
            }
            EventMsg::ApprovalResolved { call_id, .. } => {
                if let Some(task) = &mut self.active {
                    task.approvals.retain(|a| &a.call_id != call_id);
                }
            }
            EventMsg::TaskCompleted {
                task_id, usage, reason, ..
            } => self.end_task(event.ts, task_id, TaskOutcome::Completed, Some(*reason), None, usage),
            EventMsg::QueueChanged { queued } => self.queued = queued.clone(),
            // A device that opens the conversation later draws the card ended.
            EventMsg::CardChanged { card_id, state, note } if state.is_ended() => {
                self.cards.retain(|(id, _)| id != card_id);
                self.transcript.push(TranscriptItem::CardResolved {
                    card_id: card_id.clone(),
                    outcome: *state,
                    note: note.clone().unwrap_or_else(|| state.as_str().to_string()),
                });
            }
            // What was spent stays in `usage`; the rest is as the new branch has it.
            EventMsg::SessionRewound {
                message_id, removed_agents, ..
            } => {
                let at = self
                    .transcript
                    .iter()
                    .position(|item| matches!(item, TranscriptItem::Message { message_id: id, .. } if id == message_id));
                if let Some(at) = at {
                    self.transcript.truncate(at);
                }
                self.agents.retain(|a| !removed_agents.contains(&a.agent_id));
                self.failed = last_task_failed(&self.transcript);
            }
            EventMsg::TaskFailed { task_id, error, usage, .. } => {
                self.end_task(event.ts, task_id, TaskOutcome::Failed, None, Some(error.clone()), usage)
            }
            EventMsg::TaskInterrupted { task_id, usage, .. } => {
                self.end_task(event.ts, task_id, TaskOutcome::Interrupted, None, None, usage)
            }
            EventMsg::AgentStarted {
                agent_id,
                name,
                description,
                session_file,
                call_id,
            } => {
                let info = self.agent_info(agent_id, name, description, session_file);
                self.agents.push(AgentMirror {
                    agent_id: agent_id.clone(),
                    name: name.clone(),
                    description: description.clone(),
                    call_id: call_id.clone(),
                    status: AgentStatus::Running,
                    since: event.ts,
                    summary: None,
                    usage: None,
                    error: None,
                    elapsed_ms: None,
                    session: SessionMirror::new(info, Vec::new()),
                });
            }
            EventMsg::AgentStatus {
                agent_id,
                status,
                summary,
                usage,
                error,
                elapsed_ms,
                ..
            } => {
                if let Some(agent) = self.agents.iter_mut().find(|a| &a.agent_id == agent_id) {
                    agent.status = *status;
                    agent.since = event.ts;
                    // A new round keeps what the last one reported until it reports itself.
                    if *status != AgentStatus::Running {
                        agent.summary.clone_from(summary);
                        agent.usage.clone_from(usage);
                        agent.error.clone_from(error);
                        agent.elapsed_ms = *elapsed_ms;
                    }
                }
            }
            EventMsg::AgentEvent { agent_id, event } => {
                if let Some(agent) = self.agents.iter_mut().find(|a| &a.agent_id == agent_id) {
                    agent.session.apply(event);
                }
            }
            _ => {}
        }
    }

    /// The sub-agents of an earlier run of the session, as their logs left them: they come
    /// before any the session starts now.
    pub fn restore_agents(&mut self, agents: Vec<RestoredAgent>) {
        let restored = agents.into_iter().map(|a| {
            let info = self.agent_info(&a.agent_id, &a.name, &a.description, &a.session_file);
            let mut session = SessionMirror::new(info, a.transcript);
            session.usage = a.usage;
            AgentMirror {
                agent_id: a.agent_id,
                name: a.name,
                description: a.description,
                call_id: a.call_id,
                status: a.status,
                since: a.since,
                summary: None,
                usage: None,
                error: a.error,
                elapsed_ms: None,
                session,
            }
        });
        self.agents.splice(0..0, restored.collect::<Vec<_>>());
    }

    /// A sub-agent's own session shares this one's settings.
    fn agent_info(&self, agent_id: &str, name: &str, description: &str, session_file: &std::path::Path) -> SessionInfo {
        SessionInfo {
            session_id: agent_id.to_string(),
            session_file: session_file.to_path_buf(),
            title: Some(if description.is_empty() { name } else { description }.to_string()),
            archived: false,
            ..self.info.clone()
        }
    }

    fn end_task(
        &mut self,
        ts: Timestamp,
        task_id: &str,
        outcome: TaskOutcome,
        reason: Option<CompletionReason>,
        error: Option<ErrorInfo>,
        usage: &UsageTotals,
    ) {
        self.usage.merge(usage);
        self.active = None;
        self.task_started = None;
        self.failed = outcome == TaskOutcome::Failed;
        self.transcript.push(TranscriptItem::TaskEnd {
            task_id: task_id.to_string(),
            outcome,
            reason,
            error: error.map(Box::new),
            usage: Some(usage.clone()),
            ts,
        });
    }

    fn tool_mut(&mut self, call_id: &str) -> Option<&mut ToolState> {
        self.active.as_mut()?.tools.iter_mut().find(|t| t.call_id == call_id)
    }

    /// The current state with the last `limit` transcript items (all with `None`).
    pub fn snapshot(&self, limit: Option<usize>) -> SessionSnapshot {
        let end = self.transcript.len();
        let start = limit.map_or(0, |n| end.saturating_sub(n));
        SessionSnapshot {
            session: self.info.clone(),
            last_seq: self.last_seq,
            transcript: self.page(start, end),
            active_task: self.active.clone(),
            usage: self.usage.clone(),
            mcp_servers: self.mcp_servers.values().cloned().collect(),
            agents: self.agents.iter().map(|a| a.snapshot(limit)).collect(),
            queued: self.queued.clone(),
        }
    }

    /// Items before the `before` cursor of an earlier page.
    pub fn history(&self, before: &str, limit: usize) -> Option<TranscriptPage> {
        let end: usize = before.strip_prefix('i')?.parse().ok()?;
        let end = end.min(self.transcript.len());
        Some(self.page(end.saturating_sub(limit), end))
    }

    fn page(&self, start: usize, end: usize) -> TranscriptPage {
        TranscriptPage {
            items: self.transcript[start..end].to_vec(),
            before: (start > 0).then(|| format!("i{start}")),
        }
    }
}

impl AgentMirror {
    fn snapshot(&self, limit: Option<usize>) -> AgentSnapshot {
        let own = self.session.snapshot(limit);
        AgentSnapshot {
            agent_id: self.agent_id.clone(),
            name: self.name.clone(),
            description: self.description.clone(),
            call_id: self.call_id.clone(),
            session_file: self.session.info.session_file.clone(),
            status: self.status,
            since: self.since,
            summary: self.summary.clone(),
            usage: self.usage.clone(),
            error: self.error.clone(),
            elapsed_ms: self.elapsed_ms,
            transcript: own.transcript,
            active_task: own.active_task,
        }
    }
}

/// A card `send_to_user` showed: its id, and what a bot's row says while it waits.
fn card_shown(result: &her_llm::ToolResultMessage) -> Option<(String, String)> {
    if result.tool_name != her_core::bot::SEND_TO_USER || result.is_error {
        return None;
    }
    let (card, text) = her_core::bot::shown_card(result.details.as_ref()?.as_str())?;
    Some((card.id, text.filter(|text| !text.trim().is_empty()).unwrap_or(card.title)))
}

/// Whether the last task of these items failed.
fn last_task_failed(transcript: &[TranscriptItem]) -> bool {
    transcript
        .iter()
        .rev()
        .find_map(|item| match item {
            TranscriptItem::TaskEnd { outcome, .. } => Some(*outcome == TaskOutcome::Failed),
            _ => None,
        })
        .unwrap_or(false)
}

fn apply_delta(partial: &mut PartialMessage, delta: &Delta) {
    match delta {
        Delta::Text { index, text } => match block_mut(partial, *index) {
            Some(PartialBlock::Text { text: existing, .. }) => existing.push_str(text),
            Some(_) => {}
            None => partial.blocks.push(PartialBlock::Text {
                index: *index,
                text: text.clone(),
            }),
        },
        Delta::Thinking { index, text } => match block_mut(partial, *index) {
            Some(PartialBlock::Thinking { text: existing, .. }) => existing.push_str(text),
            Some(_) => {}
            None => partial.blocks.push(PartialBlock::Thinking {
                index: *index,
                text: text.clone(),
            }),
        },
        Delta::ToolCallStart { index, call_id, name } => {
            if block_mut(partial, *index).is_none() {
                partial.blocks.push(PartialBlock::ToolCall {
                    index: *index,
                    call_id: call_id.clone(),
                    name: name.clone(),
                    args_json: String::new(),
                });
            }
        }
        Delta::ToolCallArgs { index, json } => {
            if let Some(PartialBlock::ToolCall { args_json, .. }) = block_mut(partial, *index) {
                args_json.push_str(json);
            }
        }
    }
    partial.blocks.sort_by_key(PartialBlock::index);
}

fn block_mut(partial: &mut PartialMessage, index: usize) -> Option<&mut PartialBlock> {
    partial.blocks.iter_mut().find(|b| b.index() == index)
}

/// Keeps the last `max` bytes, cut at a character boundary.
fn keep_tail(text: &mut String, max: usize) {
    if text.len() <= max {
        return;
    }
    let mut cut = text.len() - max;
    while !text.is_char_boundary(cut) {
        cut += 1;
    }
    text.drain(..cut);
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_llm::UserMessage;
    use her_protocol::PermissionMode;
    use her_protocol::CompletionReason;
    use her_protocol::ToolCategory;
    use std::path::PathBuf;

    fn info() -> SessionInfo {
        SessionInfo {
            session_id: "s".into(),
            cwd: PathBuf::from("/w"),
            session_file: PathBuf::from("/f"),
            model: "p/m".into(),
            thinking: Default::default(),
            permission_mode: PermissionMode::Manual,
            context_window: 1000,
            title: None,
            archived: false,
        }
    }

    fn ev(seq: u64, msg: EventMsg) -> Event {
        Event { seq, ts: 0, msg }
    }

    #[test]
    fn keeps_each_sub_agent_and_its_own_conversation() {
        let mut m = SessionMirror::new(info(), Vec::new());
        m.apply(&ev(
            1,
            EventMsg::AgentStarted {
                agent_id: "a1".into(),
                name: "counter".into(),
                description: "统计文件".into(),
                session_file: PathBuf::from("/a1"),
                call_id: "c1".into(),
            },
        ));
        let child = |seq, msg| EventMsg::AgentEvent {
            agent_id: "a1".into(),
            event: Box::new(ev(seq, msg)),
        };
        m.apply(&ev(
            2,
            child(
                1,
                EventMsg::TaskStarted {
                    task_id: "t1".into(),
                    trigger: Default::default(),
                },
            ),
        ));
        m.apply(&ev(
            3,
            child(
                2,
                EventMsg::MessageCompleted {
                    message_id: "u1".into(),
                    message: UserMessage::text("count").into(),
                    mail: None,
                    skills: Vec::new(),
                },
            ),
        ));
        let snap = m.snapshot(None);
        assert!(snap.transcript.items.is_empty(), "its events are not the main session's");
        let agent = &snap.agents[0];
        assert_eq!((agent.name.as_str(), agent.description.as_str(), agent.status), ("counter", "统计文件", AgentStatus::Running));
        assert_eq!(agent.transcript.items.len(), 1);
        assert_eq!(agent.active_task.as_ref().map(|t| t.task_id.as_str()), Some("t1"));

        m.apply(&ev(
            4,
            EventMsg::AgentStatus {
                agent_id: "a1".into(),
                name: "counter".into(),
                status: AgentStatus::Completed,
                summary: Some("3 files".into()),
                usage: None,
                error: None,
                elapsed_ms: Some(1200),
            },
        ));
        // Its report reaches the main session as mail, and stays marked as one.
        m.apply(&ev(
            5,
            EventMsg::MessageCompleted {
                message_id: "m1".into(),
                message: UserMessage::text("<agent_report agent=\"counter\" status=\"completed\">\n3 files\n</agent_report>").into(),
                mail: Some(her_protocol::MailInfo {
                    id: "m1".into(),
                    from: her_protocol::AgentRef {
                        id: "a1".into(),
                        name: "counter".into(),
                    },
                    kind: her_protocol::MailKind::Report,
                    status: AgentStatus::Completed,
                    no_reply: false,
                    routine_id: None,
                }),
                skills: Vec::new(),
            },
        ));
        let snap = m.snapshot(None);
        let agent = &snap.agents[0];
        assert_eq!(agent.status, AgentStatus::Completed);
        assert_eq!((agent.summary.as_deref(), agent.elapsed_ms), (Some("3 files"), Some(1200)));
        assert!(matches!(&snap.transcript.items[0], TranscriptItem::Message { mail: Some(mail), .. } if mail.from.name == "counter"));
    }

    #[test]
    fn agents_of_an_earlier_run_come_first_as_their_logs_left_them() {
        let mut m = SessionMirror::new(info(), Vec::new());
        m.restore_agents(vec![RestoredAgent {
            agent_id: "a0".into(),
            name: "macro".into(),
            description: "利率与估值".into(),
            call_id: "c0".into(),
            session_file: PathBuf::from("/a0"),
            status: AgentStatus::Completed,
            since: 7,
            error: None,
            transcript: vec![TranscriptItem::Message {
                message_id: "u1".into(),
                message: UserMessage::text("look into rates").into(),
                skills: Vec::new(),
                mail: None,
            }],
            usage: UsageTotals::default(),
        }]);
        m.apply(&ev(
            1,
            EventMsg::AgentStarted {
                agent_id: "a1".into(),
                name: "macro-2".into(),
                description: String::new(),
                session_file: PathBuf::from("/a1"),
                call_id: "c1".into(),
            },
        ));
        assert!(m.agents_running());
        let snap = m.snapshot(None);
        let seen: Vec<(&str, AgentStatus)> = snap.agents.iter().map(|a| (a.agent_id.as_str(), a.status)).collect();
        assert_eq!(seen, [("a0", AgentStatus::Completed), ("a1", AgentStatus::Running)]);
        let earlier = &snap.agents[0];
        assert_eq!((earlier.call_id.as_str(), earlier.since, earlier.transcript.items.len()), ("c0", 7, 1));
    }

    #[test]
    fn the_queue_is_the_last_one_announced() {
        let mut m = SessionMirror::new(info(), Vec::new());
        let queued = |id: &str, text: &str, mode| QueuedInput {
            id: id.into(),
            message: UserMessage::text(text),
            mode,
        };
        m.apply(&ev(
            1,
            EventMsg::TaskStarted {
                task_id: "t1".into(),
                trigger: Default::default(),
            },
        ));
        let both = vec![
            queued("a", "now", her_protocol::PromptMode::Steer),
            queued("b", "later", her_protocol::PromptMode::FollowUp),
        ];
        m.apply(&ev(2, EventMsg::QueueChanged { queued: both.clone() }));
        assert_eq!(m.snapshot(None).queued, both);
        // "now" joins the history: the queue goes first, then its message, under the same id.
        m.apply(&ev(3, EventMsg::QueueChanged { queued: both[1..].to_vec() }));
        m.apply(&ev(
            4,
            EventMsg::MessageCompleted {
                message_id: "a".into(),
                message: UserMessage::text("now").into(),
                mail: None,
                skills: Vec::new(),
            },
        ));
        let snap = m.snapshot(None);
        assert_eq!(snap.queued, both[1..]);
        assert!(matches!(&snap.transcript.items[0], TranscriptItem::Message { message_id, .. } if message_id == "a"));
        m.apply(&ev(5, EventMsg::QueueChanged { queued: Vec::new() }));
        m.apply(&ev(
            6,
            EventMsg::TaskInterrupted {
                task_id: "t1".into(),
                returned_input: vec![UserMessage::text("later")],
                usage: UsageTotals::default(),
            },
        ));
        assert!(m.snapshot(None).queued.is_empty());
    }

    #[test]
    fn a_rewind_cuts_the_conversation_at_the_message_and_keeps_what_was_spent() {
        let mut m = SessionMirror::new(info(), Vec::new());
        let say = |id: &str, text: &str| EventMsg::MessageCompleted {
            message_id: id.into(),
            message: UserMessage::text(text).into(),
            mail: None,
            skills: Vec::new(),
        };
        let spent = |tokens: u64| {
            let mut usage = UsageTotals::default();
            usage.add(
                "p/m",
                &her_llm::Usage {
                    input: tokens,
                    ..Default::default()
                },
            );
            usage
        };
        let started = |agent: &str| EventMsg::AgentStarted {
            agent_id: agent.into(),
            name: agent.into(),
            description: String::new(),
            session_file: PathBuf::from("/a"),
            call_id: format!("call-{agent}"),
        };
        let fail = EventMsg::TaskFailed {
            task_id: "t2".into(),
            error: her_protocol::ErrorInfo::new("network", "down"),
            usage: spent(3),
            returned_input: Vec::new(),
        };
        let events = [
            EventMsg::TaskStarted { task_id: "t1".into(), trigger: Default::default() },
            say("u1", "first"),
            started("a1"),
            EventMsg::TaskCompleted {
                task_id: "t1".into(),
                usage: spent(2),
                last_message: None,
                reason: CompletionReason::Done,
            },
            EventMsg::TaskStarted { task_id: "t2".into(), trigger: Default::default() },
            say("u2", "second"),
            started("a2"),
            fail,
        ];
        for (i, msg) in events.into_iter().enumerate() {
            m.apply(&ev(i as u64 + 1, msg));
        }
        assert_eq!(m.state(), SessionState::Failed);
        assert_eq!(m.snapshot(None).transcript.items.len(), 4);
        m.apply(&ev(
            9,
            EventMsg::SessionRewound {
                message_id: "u2".into(),
                removed_agents: vec!["a2".into()],
                returned_input: Vec::new(),
            },
        ));
        let snap = m.snapshot(None);
        assert_eq!(snap.last_seq, 9);
        assert!(matches!(snap.transcript.items.last(), Some(TranscriptItem::TaskEnd { task_id, .. }) if task_id == "t1"));
        assert_eq!(snap.transcript.items.len(), 2, "the first message and where its task ended");
        assert_eq!(snap.agents.iter().map(|a| a.agent_id.as_str()).collect::<Vec<_>>(), ["a1"]);
        assert_eq!(m.state(), SessionState::Idle, "the failure was in what went");
        assert_eq!(snap.usage, spent(5), "what was spent stays");
        assert!(m.has_user_message());
        // Back before the first message.
        m.apply(&ev(
            10,
            EventMsg::SessionRewound {
                message_id: "u1".into(),
                removed_agents: vec!["a1".into()],
                returned_input: Vec::new(),
            },
        ));
        assert!(m.snapshot(None).transcript.items.is_empty());
        assert!(!m.has_user_message());
    }

    #[test]
    fn tracks_a_task_until_it_ends() {
        let mut m = SessionMirror::new(info(), Vec::new());
        m.apply(&ev(
            1,
            EventMsg::TaskStarted {
                task_id: "t1".into(),
                trigger: Default::default(),
            },
        ));
        m.apply(&ev(
            2,
            EventMsg::MessageCompleted {
                message_id: "u1".into(),
                message: UserMessage::text("hi").into(),
                mail: None,
                skills: Vec::new(),
            },
        ));
        m.apply(&ev(
            3,
            EventMsg::MessageStarted {
                message_id: "a1".into(),
                role: Role::Assistant,
            },
        ));
        for (seq, t) in [(4, "Hel"), (5, "lo")] {
            m.apply(&ev(
                seq,
                EventMsg::MessageDelta {
                    message_id: "a1".into(),
                    delta: Delta::Text {
                        index: 0,
                        text: t.into(),
                    },
                },
            ));
        }
        m.apply(&ev(
            6,
            EventMsg::ToolStarted {
                call_id: "c1".into(),
                name: "bash".into(),
                args: serde_json::json!({"command": "ls"}),
                category: ToolCategory::Execute,
                title: "ls".into(),
                locations: Vec::new(),
            },
        ));
        m.apply(&ev(
            7,
            EventMsg::ApprovalRequested {
                call_id: "c1".into(),
                name: "bash".into(),
                args: serde_json::json!({"command": "ls"}),
                summary: "ls".into(),
                reason: None,
                proposed_rule: None,
                proposed_mode: None,
                proposed_grants: Vec::new(),
                own_browser: false,
            },
        ));
        let snap = m.snapshot(None);
        assert_eq!(snap.last_seq, 7);
        let task = snap.active_task.unwrap();
        assert_eq!(
            task.streaming.unwrap().blocks,
            vec![PartialBlock::Text {
                index: 0,
                text: "Hello".into()
            }]
        );
        assert_eq!(task.tools.len(), 1);
        assert!(m.approval_pending("c1"));
        m.apply(&ev(
            8,
            EventMsg::TaskCompleted {
                task_id: "t1".into(),
                usage: Default::default(),
                last_message: None,
                reason: CompletionReason::Done,
            },
        ));
        assert!(!m.busy());
        assert_eq!(m.state(), SessionState::Idle);
        // The message, then where the task ended.
        let items = m.snapshot(None).transcript.items;
        assert_eq!(items.len(), 2);
        assert!(matches!(
            &items[1],
            TranscriptItem::TaskEnd { task_id, outcome: TaskOutcome::Completed, reason: Some(CompletionReason::Done), .. } if task_id == "t1"
        ));
    }

    #[test]
    fn what_a_sub_agent_asks_shows_while_the_session_itself_is_idle() {
        let mut m = SessionMirror::new(info(), Vec::new());
        m.apply(&ev(
            1,
            EventMsg::AgentStarted {
                agent_id: "a1".into(),
                name: "writer".into(),
                description: "写文件".into(),
                session_file: PathBuf::from("/a1"),
                call_id: "spawn".into(),
            },
        ));
        let inner = |seq, msg| EventMsg::AgentEvent {
            agent_id: "a1".into(),
            event: Box::new(ev(seq, msg)),
        };
        m.apply(&ev(
            2,
            inner(
                1,
                EventMsg::TaskStarted {
                    task_id: "t1".into(),
                    trigger: Default::default(),
                },
            ),
        ));
        m.apply(&ev(
            3,
            inner(
                2,
                EventMsg::ApprovalRequested {
                    call_id: "c1".into(),
                    name: "bash".into(),
                    args: serde_json::json!({"command": "rm x"}),
                    summary: "rm x".into(),
                    reason: None,
                    proposed_rule: None,
                    proposed_mode: None,
                    proposed_grants: Vec::new(),
                    own_browser: false,
                },
            ),
        ));
        assert!(!m.busy());
        assert_eq!(m.state(), SessionState::AwaitingApproval);
        assert_eq!(m.first_approval(), Some("rm x"));
        assert!(m.agent_approval_pending("a1", "c1"));
        assert!(!m.approval_pending("c1"), "the session's own are apart");
        assert!(!m.agent_approval_pending("a2", "c1"));
        let snap = m.snapshot(None);
        assert_eq!(snap.agents[0].active_task.as_ref().unwrap().approvals[0].call_id, "c1");

        m.apply(&ev(
            4,
            inner(
                3,
                EventMsg::ApprovalResolved {
                    call_id: "c1".into(),
                    decision: her_protocol::ApprovalDecision::Approve,
                },
            ),
        ));
        assert_eq!(m.state(), SessionState::Idle);
        assert_eq!(m.first_approval(), None);
        assert!(!m.agent_approval_pending("a1", "c1"));
    }

    #[test]
    fn the_list_state_follows_approvals_and_failures() {
        let mut m = SessionMirror::new(info(), Vec::new());
        assert_eq!(m.state(), SessionState::Idle);
        m.apply(&Event {
            seq: 1,
            ts: 1_700_000_000_000,
            msg: EventMsg::TaskStarted {
                task_id: "t1".into(),
                trigger: Default::default(),
            },
        });
        assert_eq!(m.state(), SessionState::Running);
        assert_eq!(m.task_started(), Some(1_700_000_000_000));
        m.apply(&ev(
            2,
            EventMsg::ApprovalRequested {
                call_id: "c1".into(),
                name: "bash".into(),
                args: serde_json::json!({"command": "rm x"}),
                summary: "rm x".into(),
                reason: None,
                proposed_rule: None,
                proposed_mode: None,
                proposed_grants: Vec::new(),
                own_browser: false,
            },
        ));
        assert_eq!(m.state(), SessionState::AwaitingApproval);
        m.apply(&ev(
            3,
            EventMsg::TaskFailed {
                task_id: "t1".into(),
                error: her_protocol::ErrorInfo::new("auth", "no key"),
                usage: Default::default(),
                returned_input: Vec::new(),
            },
        ));
        assert_eq!(m.state(), SessionState::Failed);
        assert_eq!(m.task_started(), None);
        m.apply(&ev(4, EventMsg::TitleChanged { title: "Fix the build".into() }));
        m.apply(&ev(5, EventMsg::ArchivedChanged { archived: true }));
        assert_eq!(m.info().title.as_deref(), Some("Fix the build"));
        assert!(m.info().archived);
        // A resumed session remembers that its last task failed.
        let again = SessionMirror::new(info(), m.transcript().to_vec());
        assert_eq!(again.state(), SessionState::Failed);
    }

    #[test]
    fn pages_through_history() {
        let items: Vec<TranscriptItem> = (0..5)
            .map(|i| TranscriptItem::Message {
                message_id: format!("m{i}"),
                message: UserMessage::text(format!("{i}")).into(),
                skills: Vec::new(),
                mail: None,
            })
            .collect();
        let m = SessionMirror::new(info(), items);
        let snap = m.snapshot(Some(2));
        assert_eq!(snap.transcript.items.len(), 2);
        let before = snap.transcript.before.unwrap();
        let page = m.history(&before, 2).unwrap();
        assert_eq!(page.items.len(), 2);
        let page = m.history(page.before.as_deref().unwrap(), 2).unwrap();
        assert_eq!(page.items.len(), 1);
        assert!(page.before.is_none());
    }

    #[test]
    fn progress_tail_is_capped_at_a_char_boundary() {
        let mut s = "é".repeat(10);
        keep_tail(&mut s, 5);
        assert!(s.len() <= 6 && s.chars().all(|c| c == 'é'));
    }
}
