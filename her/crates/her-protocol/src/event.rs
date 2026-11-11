use her_llm::LlmError;
use her_llm::Message;
use her_llm::StreamEvent;
use her_llm::ThinkingLevel;
use her_llm::Timestamp;
use her_llm::ToolResultMessage;
use her_llm::Usage;
use her_llm::UsageTotals;
use her_llm::UserMessage;
use schemars::JsonSchema;
use serde::Deserialize;
use serde::Serialize;
use serde_json::Value;
use std::path::PathBuf;
use std::sync::Arc;

use crate::op::ApprovalDecision;
use crate::op::CommandRule;
use crate::op::PermissionMode;
use crate::op::PromptMode;
use crate::op::SiteGrant;

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct Event {
    /// Strictly increasing within a session, starting at 1.
    pub seq: u64,
    pub ts: Timestamp,
    #[serde(flatten)]
    pub msg: EventMsg,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum EventMsg {
    SessionStarted {
        session_id: String,
        protocol_version: u32,
        model: String,
        cwd: PathBuf,
        resumed: bool,
        session_file: PathBuf,
        #[serde(default)]
        thinking: ThinkingLevel,
        #[serde(default, alias = "approval_mode")]
        permission_mode: PermissionMode,
        #[serde(default)]
        context_window: u64,
        /// Totals recorded in the log so far; zero for a new session.
        #[serde(default)]
        usage: UsageTotals,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        context: Option<ContextUsage>,
    },
    TaskStarted {
        task_id: String,
        /// What started the task.
        #[serde(default, skip_serializing_if = "TaskTrigger::is_user")]
        trigger: TaskTrigger,
    },
    TurnStarted {
        task_id: String,
        turn: u32,
        /// The context size checked before this turn's request.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        context: Option<ContextUsage>,
    },
    MessageStarted {
        message_id: String,
        role: Role,
    },
    MessageDelta {
        message_id: String,
        delta: Delta,
    },
    /// Complete user or assistant message. Tool results arrive in `ToolCompleted`.
    MessageCompleted {
        message_id: String,
        message: Message,
        /// Set when the message is a sub-agent's report delivered to this session.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        mail: Option<MailInfo>,
        /// The skills the user's message mentions with `/skill:name`, in order: show those
        /// mentions as tags.
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        skills: Vec<SkillUse>,
    },
    /// The model request failed and is tried again. The assistant message being streamed, if
    /// any, is dropped: the next attempt starts a new one.
    StreamRetry {
        attempt: u32,
        delay_ms: u64,
        error: ErrorInfo,
    },
    /// The agent opened a document (`doc_open`): clients show it beside the conversation
    /// (document-editor-design §6).
    DocOpened {
        info: crate::doc::DocInfo,
    },
    ToolStarted {
        call_id: String,
        name: String,
        args: Value,
        #[serde(default)]
        category: ToolCategory,
        /// One-line description for display, e.g. the command or `read <path>`.
        #[serde(default)]
        title: String,
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        locations: Vec<Location>,
    },
    /// The call passed preflight and approval and is now executing.
    ToolRunning {
        call_id: String,
    },
    ToolProgress {
        call_id: String,
        text: String,
    },
    ToolCompleted {
        call_id: String,
        /// Shared with the history; the JSON is the plain message.
        result: Arc<ToolResultMessage>,
    },
    ApprovalRequested {
        call_id: String,
        name: String,
        args: Value,
        summary: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        reason: Option<String>,
        /// Command prefix that `approve_for_session` allows from then on.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        proposed_rule: Option<CommandRule>,
        /// Mode that `approve_for_session` switches the session to. With none of this,
        /// `proposed_rule` and `proposed_grants`, approving for the session approves this
        /// call only.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        proposed_mode: Option<PermissionMode>,
        /// Site permissions that `approve_for_session` may keep; its `grant` picks one, the
        /// first by default.
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        proposed_grants: Vec<SiteGrant>,
        /// The call works in the user's own browser, with their real sign-ins, rather than
        /// the built-in one (connectors-entry-design §3.4): clients say so.
        #[serde(default, skip_serializing_if = "std::ops::Not::not")]
        own_browser: bool,
    },
    ApprovalResolved {
        call_id: String,
        decision: ApprovalDecision,
    },
    CompactionStarted {
        reason: CompactionReason,
    },
    CompactionCompleted {
        reason: CompactionReason,
        /// The session's compaction count, from 1. Distinct from the event sequence.
        #[serde(rename = "compaction_seq")]
        seq: u32,
        tokens_before: u64,
        tokens_after: u64,
        /// User messages kept word for word.
        retained_messages: u32,
        /// Skills in use whose instructions were kept, most recently used first.
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        kept_skills: Vec<String>,
        summary: String,
        context: ContextUsage,
    },
    CompactionFailed {
        reason: CompactionReason,
        error: ErrorInfo,
    },
    TurnCompleted {
        task_id: String,
        turn: u32,
        usage: Usage,
    },
    TaskCompleted {
        task_id: String,
        usage: UsageTotals,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        last_message: Option<String>,
        #[serde(default)]
        reason: CompletionReason,
    },
    TaskFailed {
        task_id: String,
        error: ErrorInfo,
        usage: UsageTotals,
        /// What still waited, handed back as with an interruption.
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        returned_input: Vec<UserMessage>,
    },
    TaskInterrupted {
        task_id: String,
        returned_input: Vec<UserMessage>,
        usage: UsageTotals,
    },
    /// The input waiting for the running task changed: the whole queue, in the order it joins
    /// the history, steering input first. Each joins as a user message whose `message_id` is its
    /// `id`; a task that fails or is interrupted hands what still waits back in `returned_input`.
    QueueChanged {
        queued: Vec<QueuedInput>,
    },
    /// `withdraw` took this input out of the queue before it joined the history.
    InputWithdrawn {
        id: String,
    },
    /// `resend`: the conversation went back to just before `message_id`. That message and all
    /// after it are no longer part of it, and the input that replaces it follows.
    SessionRewound {
        message_id: String,
        /// The sub-agents started in the part taken back: they are gone.
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        removed_agents: Vec<String>,
        /// What still waited for the running task, handed back as with an interruption.
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        returned_input: Vec<UserMessage>,
    },
    SettingsChanged {
        model: String,
        thinking: ThinkingLevel,
        #[serde(alias = "approval_mode")]
        permission_mode: PermissionMode,
        #[serde(default)]
        context_window: u64,
    },
    /// The session was renamed (`set_title`).
    TitleChanged {
        title: String,
    },
    /// The session was archived or brought back (`set_archived`).
    ArchivedChanged {
        archived: bool,
    },
    /// A call that needs approval was refused because nothing here can give it: a run without a
    /// person, such as `her exec` in a script.
    ApprovalRefused {
        call_id: String,
        name: String,
        summary: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        reason: Option<String>,
    },
    Warning {
        message: String,
        /// What the warning is about, for a client that words or places it itself. Absent for
        /// one-off warnings, which are only shown as `message`.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        code: Option<WarningCode>,
    },
    /// An MCP server's connection changed: starting, ready (with its tool count), failed or
    /// closed. Only the main session of an agent tree sends these.
    McpServerStatus {
        server: String,
        state: crate::McpServerState,
        /// Tools offered after filtering, when ready.
        #[serde(default)]
        tools: u32,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        error: Option<String>,
        /// Time since the connection attempt began.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        elapsed_ms: Option<u64>,
    },
    /// `spawn_agent` started a sub-agent: a session of its own, hidden from session lists.
    AgentStarted {
        agent_id: String,
        /// Its handle: how the main agent addresses it.
        name: String,
        /// What the user sees for it: a few words on what it works on.
        #[serde(default, skip_serializing_if = "String::is_empty")]
        description: String,
        session_file: PathBuf,
        /// The `spawn_agent` call that started it.
        call_id: String,
    },
    /// A sub-agent's status changed. An idle status also carries what its report says.
    AgentStatus {
        agent_id: String,
        name: String,
        status: AgentStatus,
        /// The start of its report.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        summary: Option<String>,
        /// What its last round of work cost.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        usage: Option<UsageTotals>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        error: Option<ErrorInfo>,
        /// How long its last round of work took (an idle status).
        #[serde(default, skip_serializing_if = "Option::is_none")]
        elapsed_ms: Option<u64>,
    },
    /// An event of a sub-agent's own session, passed on so that a client can follow its work.
    /// Only what changes its conversation comes: no deltas, tool progress or settings.
    AgentEvent {
        agent_id: String,
        event: Box<Event>,
    },
    /// A secure card's state changed (docs/her-secure-cards.md §3.5). `note` is her's words: why
    /// it waits for another try, or how it ended.
    CardChanged {
        card_id: String,
        state: crate::card::CardState,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        note: Option<String>,
    },
    /// What fills the context window now: the answer to `/context`.
    ContextReported {
        report: ContextReport,
    },
    /// Nothing is left to do: no task, no queued input or report that starts one, and every
    /// sub-agent idle. Sent once each time the session gets there.
    Idle,
    SessionClosed,
}

impl EventMsg {
    /// Terminal events of a task.
    pub fn is_task_end(&self) -> bool {
        matches!(
            self,
            EventMsg::TaskCompleted { .. } | EventMsg::TaskFailed { .. } | EventMsg::TaskInterrupted { .. }
        )
    }
}

/// How full the model's context window is, as checked before a request.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct ContextUsage {
    /// The last usage the provider reported, plus an estimate of what was added after it.
    pub tokens: u64,
    /// The estimated part of `tokens`.
    pub estimated: u64,
    /// Automatic compaction starts at this size.
    pub soft_limit: u64,
    /// The context window minus the room kept for the reply. Larger requests fail.
    pub hard_limit: u64,
    pub window: u64,
}

/// What fills the context window of the session's current model (`session/context`,
/// `/context`). The parts add up to `usage.tokens`; with `free` and `reserved` they fill the
/// window.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct ContextReport {
    /// The model whose window this is, as `provider/model`.
    pub model: String,
    pub usage: ContextUsage,
    /// In a fixed order: messages, system prompt, tools, skills, then MCP when there is any.
    pub parts: Vec<ContextPart>,
    /// Room left before automatic compaction starts (before `hard_limit` when it is off).
    pub free: u64,
    /// The rest of the window: room kept for the reply, and for compacting in time.
    pub reserved: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct ContextPart {
    pub kind: ContextPartKind,
    pub tokens: u64,
}

/// What a part of the context is. A client meeting a kind it does not know shows it as other.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ContextPartKind {
    /// The conversation: the user's messages, the replies, tool calls and their results.
    Messages,
    /// The system prompt, without the skill catalog and the MCP section.
    SystemPrompt,
    /// Definitions of the built-in tools.
    Tools,
    /// The skill catalog, and the instructions of skills loaded in this context.
    Skills,
    /// MCP tool definitions and the MCP part of the system prompt.
    Mcp,
}

/// Warnings a client may recognise. A client meeting a code it does not know shows `message`.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum WarningCode {
    /// The session runs in bypass-permissions: sent when it starts, resumes or switches into it.
    BypassPermissions,
    /// MCP servers that were not ready for the session's first request. A client that shows
    /// `mcp_server_status` already has this.
    McpServersNotReady,
    /// A `resend` was refused and nothing changed: its input is still the client's.
    ResendRejected,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum CompactionReason {
    /// The context reached the soft limit.
    Threshold,
    /// The provider rejected a request as too large.
    Overflow,
    /// The session switched to a model with a smaller window.
    ModelSwitch,
    Manual,
}

impl CompactionReason {
    pub fn as_str(self) -> &'static str {
        match self {
            CompactionReason::Threshold => "threshold",
            CompactionReason::Overflow => "overflow",
            CompactionReason::ModelSwitch => "model_switch",
            CompactionReason::Manual => "manual",
        }
    }
}

/// What a tool call does, for display. The values are a subset of the ACP tool kinds.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ToolCategory {
    Read,
    Edit,
    Delete,
    Move,
    Search,
    Execute,
    Fetch,
    #[default]
    Other,
}

impl ToolCategory {
    pub fn as_str(self) -> &'static str {
        match self {
            ToolCategory::Read => "read",
            ToolCategory::Edit => "edit",
            ToolCategory::Delete => "delete",
            ToolCategory::Move => "move",
            ToolCategory::Search => "search",
            ToolCategory::Execute => "execute",
            ToolCategory::Fetch => "fetch",
            ToolCategory::Other => "other",
        }
    }
}

/// A file a tool call reads or changes.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct Location {
    /// Absolute path.
    pub path: PathBuf,
    /// 1-based line.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line: Option<u32>,
}

/// Why a task completed normally.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum CompletionReason {
    /// The model needed no further turns.
    #[default]
    Done,
    /// The `max_turns` limit was reached.
    MaxTurns,
    /// The last reply was cut off by the output token limit.
    OutputLimit,
    /// A hook ended the task.
    Hook,
}

/// What started a task.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum TaskTrigger {
    /// Input from the user, or from the agent that started this session.
    #[default]
    User,
    /// A sub-agent's report arrived while nothing was running.
    Mail,
}

impl TaskTrigger {
    pub fn is_user(&self) -> bool {
        *self == TaskTrigger::User
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum AgentStatus {
    Running,
    /// Idle; its last task ended normally.
    Completed,
    /// Idle; its last task failed.
    Failed,
    /// Idle; its last task was interrupted.
    Interrupted,
    /// Takes no more messages.
    Closed,
}

impl AgentStatus {
    pub fn as_str(self) -> &'static str {
        match self {
            AgentStatus::Running => "running",
            AgentStatus::Completed => "completed",
            AgentStatus::Failed => "failed",
            AgentStatus::Interrupted => "interrupted",
            AgentStatus::Closed => "closed",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct AgentRef {
    pub id: String,
    pub name: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum MailKind {
    /// What a sub-agent reports when it has finished a round of work.
    Report,
    /// What a sub-agent sends with `send_message` while it works: its round goes on.
    Message,
    /// What a run of a routine has to say, or that it failed: `from` is the run and the
    /// routine's name (docs/kernel/routine-design.md §2.3).
    Routine,
    /// What a bot's sub-agent told the user with `send_to_user`: they have it already, and its
    /// round goes on (docs/kernel/agent-messaging-design.md §17).
    ToUser,
    /// What the mesh host says happened to work started through its tools (`mesh/news`): `from`
    /// is whoever the host names.
    News,
}

/// Where a delivered message came from.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct MailInfo {
    pub id: String,
    pub from: AgentRef,
    pub kind: MailKind,
    pub status: AgentStatus,
    /// The agent had not replied: the report only says so, in words for the model. Clients
    /// show their own words for it.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub no_reply: bool,
    /// The routine, when a run of it sent this.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub routine_id: Option<String>,
}

/// A skill the user mentioned with `/skill:name`; its instructions went to the model with the
/// message, which stays as the user wrote it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillUse {
    pub name: String,
    /// The skill's SKILL.md.
    pub path: PathBuf,
}

/// Input waiting for the running task.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct QueuedInput {
    /// Given when it was queued: the `message_id` it has once it joins the history.
    pub id: String,
    pub message: UserMessage,
    pub mode: PromptMode,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum Role {
    User,
    Assistant,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum Delta {
    Text { index: usize, text: String },
    Thinking { index: usize, text: String },
    ToolCallStart { index: usize, call_id: String, name: String },
    ToolCallArgs { index: usize, json: String },
}

impl Delta {
    pub fn from_stream(event: &StreamEvent) -> Option<Delta> {
        match event {
            StreamEvent::TextDelta { index, text } => Some(Delta::Text {
                index: *index,
                text: text.clone(),
            }),
            StreamEvent::ThinkingDelta { index, text } => Some(Delta::Thinking {
                index: *index,
                text: text.clone(),
            }),
            StreamEvent::ToolCallStart { index, id, name } => Some(Delta::ToolCallStart {
                index: *index,
                call_id: id.clone(),
                name: name.clone(),
            }),
            StreamEvent::ToolCallArgs { index, json } => Some(Delta::ToolCallArgs {
                index: *index,
                json: json.clone(),
            }),
            _ => None,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct ErrorInfo {
    pub kind: String,
    pub message: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub status: Option<u16>,
    pub retryable: bool,
}

impl ErrorInfo {
    pub fn new(kind: &str, message: impl Into<String>) -> Self {
        Self {
            kind: kind.to_string(),
            message: message.into(),
            status: None,
            retryable: false,
        }
    }
}

impl From<&LlmError> for ErrorInfo {
    fn from(err: &LlmError) -> Self {
        Self {
            kind: err.kind.as_str().to_string(),
            message: err.message.clone(),
            status: err.status,
            // Whether a context overflow is worth retrying depends on compaction; the kernel
            // decides that.
            retryable: err.kind.is_retryable(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn compaction_count_does_not_shadow_the_event_sequence() {
        let event = Event {
            seq: 486,
            ts: 10,
            msg: EventMsg::CompactionCompleted {
                reason: CompactionReason::Threshold,
                seq: 1,
                tokens_before: 1000,
                tokens_after: 100,
                retained_messages: 2,
                kept_skills: Vec::new(),
                summary: "verified progress".into(),
                context: ContextUsage::default(),
            },
        };
        let wire = serde_json::to_string(&event).unwrap();
        assert_eq!(wire.matches("\"seq\":").count(), 1);
        let value: Value = serde_json::from_str(&wire).unwrap();
        assert_eq!(value["seq"], 486);
        assert_eq!(value["compaction_seq"], 1);
        assert_eq!(serde_json::from_str::<Event>(&wire).unwrap(), event);
    }

    #[test]
    fn events_flatten_the_message() {
        let event = Event {
            seq: 3,
            ts: 10,
            msg: EventMsg::TurnStarted {
                task_id: "t1".into(),
                turn: 0,
                context: None,
            },
        };
        let json = serde_json::to_string(&event).unwrap();
        assert_eq!(json, r#"{"seq":3,"ts":10,"type":"turn_started","task_id":"t1","turn":0}"#);
        let back: Event = serde_json::from_str(&json).unwrap();
        assert_eq!(back, event);
        let closed = serde_json::to_string(&Event {
            seq: 4,
            ts: 11,
            msg: EventMsg::SessionClosed,
        })
        .unwrap();
        assert_eq!(closed, r#"{"seq":4,"ts":11,"type":"session_closed"}"#);
    }
}
