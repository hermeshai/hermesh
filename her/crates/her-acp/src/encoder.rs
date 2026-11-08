//! The writer side of an ACP connection: maps kernel events to `session/update`
//! notifications, permission requests and prompt responses, in queue order.

use bytes::Bytes;
use her_core::config::AcpSettings;
use her_llm::Currency;
use her_protocol::AgentStatus;
use her_protocol::Delta;
use her_protocol::EventMsg;
use her_protocol::Role;
use her_protocol::api::SessionInfo;
use her_rpc::Id;
use her_server::Encoder;
use her_server::Outgoing;
use her_server::Server;
use her_server::errors;
use her_server::frame::EventFrame;
use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use std::time::Instant;

use crate::content;
use crate::out;
use crate::out::Block;
use crate::out::Chunk;
use crate::out::LocationOut;
use crate::out::ToolCallOut;
use crate::out::ToolCallUpdateOut;
use crate::out::ToolContent;

/// Instructions from the reader, kept in queue order with the events.
pub(crate) enum Control {
    /// A session became visible to this connection.
    Opened { session: Arc<str>, info: SessionInfo },
    /// A `session/prompt` that is answered when the task ends.
    Prompt { session: Arc<str>, id: Id },
}

/// State the reader and the writer both need.
#[derive(Default)]
pub(crate) struct Shared {
    pub notices: AtomicBool,
    pub compaction: AtomicBool,
    /// Our permission requests, by request id.
    pub permissions: Mutex<HashMap<Id, Asked>>,
    /// Open prompts: request id → session, for `$/cancel_request`.
    pub prompts: Mutex<HashMap<Id, String>>,
    next_request: AtomicU64,
}

/// What one of our permission requests asks about.
pub(crate) struct Asked {
    pub session: String,
    /// The sub-agent that asks, if one does.
    pub agent: Option<String>,
    pub call_id: String,
}

struct ToolInfo {
    name: String,
    title: String,
    kind: &'static str,
    args: Value,
    path: Option<PathBuf>,
}

#[derive(Default)]
struct Progress {
    tail: String,
    last_sent: Option<Instant>,
}

struct SessionState {
    prompt: Option<Id>,
    /// How the open prompt ends, from the last task; sent when the session is idle.
    answer: Option<Result<Value, her_rpc::RpcError>>,
    /// The next user message is the prompt itself and is not echoed back.
    skip_user: bool,
    tools: HashMap<String, ToolInfo>,
    progress: HashMap<String, Progress>,
    /// Permission request per call, until answered or resolved elsewhere.
    permission_by_call: HashMap<String, Id>,
    /// What each sub-agent goes by, by its id.
    agents: HashMap<String, String>,
    info: Option<SessionInfo>,
    cost: f64,
    currency: &'static str,
    compaction: Option<String>,
}

impl SessionState {
    fn new(info: Option<SessionInfo>) -> Self {
        Self {
            prompt: None,
            answer: None,
            skip_user: false,
            tools: HashMap::new(),
            progress: HashMap::new(),
            permission_by_call: HashMap::new(),
            agents: HashMap::new(),
            info,
            cost: 0.0,
            currency: "USD",
            compaction: None,
        }
    }
}

pub(crate) struct AcpEncoder {
    server: Server,
    shared: Arc<Shared>,
    settings: AcpSettings,
    sessions: HashMap<Arc<str>, SessionState>,
}

fn category_kind(category: her_protocol::ToolCategory) -> &'static str {
    category.as_str()
}

fn currency_code(currency: Currency) -> &'static str {
    match currency {
        Currency::Usd => "USD",
        Currency::Cny => "CNY",
    }
}

/// Answers the open prompt of a session, if any.
fn answer_prompt(shared: &Shared, state: &mut SessionState, result: Result<Value, her_rpc::RpcError>, out: &mut Vec<Bytes>) {
    if let Some(id) = state.prompt.take() {
        shared.prompts.lock().remove(&id);
        out.push(Bytes::from(match result {
            Ok(value) => her_rpc::response(&id, &value),
            Err(error) => her_rpc::error_response(Some(&id), &error),
        }));
    }
}

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

impl AcpEncoder {
    pub(crate) fn new(server: Server, shared: Arc<Shared>) -> Self {
        Self {
            settings: server.acp_settings().clone(),
            server,
            shared,
            sessions: HashMap::new(),
        }
    }

    fn event(&mut self, frame: &EventFrame, out: &mut Vec<Bytes>) {
        let sid: &str = &frame.session.id;
        let state = self
            .sessions
            .entry(frame.session.id.clone())
            .or_insert_with(|| SessionState::new(None));
        match &frame.event.msg {
            EventMsg::MessageDelta { message_id, delta } => {
                let (kind, text) = match delta {
                    Delta::Text { text, .. } => ("agent_message_chunk", text),
                    Delta::Thinking { text, .. } => ("agent_thought_chunk", text),
                    _ => return,
                };
                out.push(out::update(
                    sid,
                    Chunk {
                        session_update: kind,
                        content: Block::Text { text },
                        message_id: Some(message_id),
                    },
                ));
            }
            EventMsg::MessageCompleted {
                message_id,
                message: her_llm::Message::User(user),
                mail,
                ..
            } => {
                // A sub-agent's report is shown with its tool call, not as a user message.
                if mail.is_some() {
                    return;
                }
                if state.skip_user {
                    state.skip_user = false;
                    return;
                }
                for content in &user.content {
                    out.push(out::update(
                        sid,
                        Chunk {
                            session_update: "user_message_chunk",
                            content: Block::from_user(content),
                            message_id: Some(message_id),
                        },
                    ));
                }
            }
            EventMsg::MessageStarted { role: Role::User, .. } => {}
            EventMsg::ToolStarted {
                call_id,
                name,
                args,
                category,
                title,
                locations,
            } => {
                out.push(out::update(
                    sid,
                    ToolCallOut {
                        session_update: "tool_call",
                        tool_call_id: call_id,
                        title,
                        name,
                        kind: category_kind(*category),
                        status: "pending",
                        locations: locations
                            .iter()
                            .map(|l| LocationOut {
                                path: &l.path,
                                line: l.line,
                            })
                            .collect(),
                        content: Vec::new(),
                        raw_input: args,
                        raw_output: None,
                    },
                ));
                state.tools.insert(
                    call_id.clone(),
                    ToolInfo {
                        name: name.clone(),
                        title: title.clone(),
                        kind: category_kind(*category),
                        args: args.clone(),
                        path: locations.first().map(|l| l.path.clone()),
                    },
                );
            }
            EventMsg::ToolRunning { call_id } => {
                out.push(out::update(sid, ToolCallUpdateOut::status(call_id, "in_progress")));
            }
            EventMsg::ToolProgress { call_id, text } => {
                let interval = self.settings.progress_interval;
                let max = self.settings.progress_tail_bytes;
                let progress = state.progress.entry(call_id.clone()).or_default();
                progress.tail.push_str(text);
                keep_tail(&mut progress.tail, max);
                if progress.last_sent.is_none_or(|t| t.elapsed() >= interval) {
                    progress.last_sent = Some(Instant::now());
                    out.push(out::update(
                        sid,
                        ToolCallUpdateOut {
                            session_update: "tool_call_update",
                            tool_call_id: call_id,
                            status: None,
                            content: Some(vec![ToolContent::text(&progress.tail)]),
                            raw_output: None,
                        },
                    ));
                }
            }
            EventMsg::ToolCompleted { call_id, result } => {
                state.progress.remove(call_id);
                let tool = state.tools.remove(call_id);
                let text = result.text_content();
                let null = Value::Null;
                let (name, args, path) = match &tool {
                    Some(t) => (t.name.as_str(), &t.args, t.path.as_deref()),
                    None => (result.tool_name.as_str(), &null, None),
                };
                let content = content::completion_content(name, args, path, result, &text);
                out.push(out::update(
                    sid,
                    ToolCallUpdateOut {
                        session_update: "tool_call_update",
                        tool_call_id: call_id,
                        status: Some(if result.is_error { "failed" } else { "completed" }),
                        content: Some(content),
                        raw_output: result.details.as_ref(),
                    },
                ));
            }
            EventMsg::ApprovalRequested { .. } => request_permission(&self.shared, state, sid, None, &frame.event.msg, out),
            EventMsg::ApprovalResolved { call_id, .. } => withdraw_permission(&self.shared, state, call_id, out),
            // What a sub-agent asks goes to the client like the session's own.
            EventMsg::AgentEvent { agent_id, event } => match &event.msg {
                EventMsg::ApprovalRequested { .. } => request_permission(&self.shared, state, sid, Some(agent_id), &event.msg, out),
                EventMsg::ApprovalResolved { call_id, .. } => withdraw_permission(&self.shared, state, &agent_call_id(agent_id, call_id), out),
                _ => {}
            },
            EventMsg::TurnCompleted { usage, .. } => {
                let amount = usage.cost.input + usage.cost.output + usage.cost.cache_read + usage.cost.cache_write;
                state.cost += amount;
                state.currency = currency_code(usage.cost.currency);
                let size = state.info.as_ref().map_or(0, |i| i.context_window);
                out.push(out::update(
                    sid,
                    out::UsageOut {
                        session_update: "usage_update",
                        used: usage.input + usage.cache_read + usage.cache_write + usage.output,
                        size,
                        cost: (state.cost > 0.0).then_some(out::Cost {
                            amount: state.cost,
                            currency: state.currency,
                        }),
                    },
                ));
            }
            EventMsg::SettingsChanged {
                model,
                thinking,
                permission_mode,
                context_window,
            } => {
                if let Some(info) = &mut state.info {
                    info.model = model.clone();
                    info.thinking = *thinking;
                    info.permission_mode = *permission_mode;
                    if *context_window > 0 {
                        info.context_window = *context_window;
                    }
                    out.push(out::update(
                        sid,
                        out::ConfigUpdateOut {
                            session_update: "config_option_update",
                            config_options: out::config_options(&self.server, info),
                        },
                    ));
                }
            }
            EventMsg::Warning { message, .. } => {
                if self.shared.notices.load(Ordering::Relaxed) {
                    out.push(out::update(
                        sid,
                        out::NoticeOut {
                            session_update: "notice",
                            severity: "warning",
                            title: message,
                        },
                    ));
                }
            }
            EventMsg::StreamRetry { error, .. } => {
                if self.shared.notices.load(Ordering::Relaxed) {
                    let title = format!("Retrying the model request: {}", error.message);
                    out.push(out::update(
                        sid,
                        out::NoticeOut {
                            session_update: "notice",
                            severity: "info",
                            title: &title,
                        },
                    ));
                }
            }
            EventMsg::CompactionStarted { .. } | EventMsg::CompactionCompleted { .. } => {
                if self.shared.compaction.load(Ordering::Relaxed) {
                    let started = matches!(frame.event.msg, EventMsg::CompactionStarted { .. });
                    let id = if started {
                        let id = format!("c{}", frame.event.seq);
                        state.compaction = Some(id.clone());
                        id
                    } else {
                        state.compaction.take().unwrap_or_else(|| format!("c{}", frame.event.seq))
                    };
                    out.push(out::update(
                        sid,
                        out::CompactionOut {
                            session_update: "compaction_update",
                            compaction_id: &id,
                            status: if started { "in_progress" } else { "completed" },
                        },
                    ));
                }
            }
            // `/context`: the report as the agent's text, the only way an ACP client shows it.
            EventMsg::ContextReported { report } => {
                let text = format!("```\n{}```", her_core::context_report::text(report));
                out.push(out::update(
                    sid,
                    Chunk {
                        session_update: "agent_message_chunk",
                        content: Block::Text { text: &text },
                        message_id: None,
                    },
                ));
            }
            // A prompt lasts until the session is idle: reports from sub-agents can start more
            // tasks, and their output belongs to the same prompt. Its answer is the last task's.
            EventMsg::TaskCompleted { reason, .. } => {
                state.answer = Some(Ok(json!({ "stopReason": out::stop_reason(*reason) })));
            }
            EventMsg::TaskInterrupted { .. } => {
                state.answer = Some(Ok(json!({ "stopReason": "cancelled" })));
            }
            EventMsg::TaskFailed { error, .. } => {
                state.answer = Some(Err(if error.kind == "auth" {
                    her_rpc::RpcError::with_kind(-32000, "auth_required", error.message.clone())
                } else {
                    errors::task_failed(error)
                }));
            }
            EventMsg::Idle => {
                if let Some(answer) = state.answer.take() {
                    answer_prompt(&self.shared, state, answer, out);
                }
            }
            // A sub-agent shows as a tool call of its own while it works.
            EventMsg::AgentStarted {
                agent_id,
                name,
                description,
                ..
            } => {
                let call_id = format!("agent-{agent_id}");
                // Its label reads best; agents from before it had one go by their handle.
                let title = if description.is_empty() {
                    format!("agent {name}")
                } else {
                    description.clone()
                };
                state.agents.insert(agent_id.clone(), title.clone());
                let input = json!({ "name": name, "description": description });
                out.push(out::update(
                    sid,
                    ToolCallOut {
                        session_update: "tool_call",
                        tool_call_id: &call_id,
                        title: &title,
                        name: "agent",
                        kind: "other",
                        status: "in_progress",
                        locations: Vec::new(),
                        content: Vec::new(),
                        raw_input: &input,
                        raw_output: None,
                    },
                ));
            }
            EventMsg::AgentStatus {
                agent_id,
                status,
                summary,
                ..
            } => {
                let call_id = format!("agent-{agent_id}");
                let status = match status {
                    AgentStatus::Running => "in_progress",
                    AgentStatus::Completed => "completed",
                    AgentStatus::Failed | AgentStatus::Interrupted | AgentStatus::Closed => "failed",
                };
                out.push(out::update(
                    sid,
                    ToolCallUpdateOut {
                        session_update: "tool_call_update",
                        tool_call_id: &call_id,
                        status: Some(status),
                        content: summary.as_deref().map(|text| vec![ToolContent::text(text)]),
                        raw_output: None,
                    },
                ));
            }
            EventMsg::SessionClosed => {
                answer_prompt(&self.shared, state, Err(errors::session_closed(sid)), out);
                self.sessions.remove(sid);
            }
            _ => {}
        }
    }
}

/// A sub-agent's calls are no tool calls of the session's, and sessions choose their call ids
/// apart from each other: what the client knows one by.
fn agent_call_id(agent: &str, call_id: &str) -> String {
    format!("agent-{agent}/{call_id}")
}

/// Asks the client about a call, the session's or a sub-agent's: `session/request_permission`.
fn request_permission(shared: &Shared, state: &mut SessionState, sid: &str, agent: Option<&str>, asked: &EventMsg, out: &mut Vec<Bytes>) {
    let EventMsg::ApprovalRequested {
        call_id,
        summary,
        proposed_rule,
        proposed_mode,
        proposed_grants,
        ..
    } = asked
    else {
        return;
    };
    let id = Id::Str(format!("her-{}", shared.next_request.fetch_add(1, Ordering::Relaxed)));
    shared.permissions.lock().insert(
        id.clone(),
        Asked {
            session: sid.to_string(),
            agent: agent.map(str::to_string),
            call_id: call_id.clone(),
        },
    );
    let (tool_call_id, title, kind) = match agent {
        // The client has not seen this call: the question says who asks.
        Some(agent) => {
            let who = state.agents.get(agent).map_or("agent", String::as_str);
            (agent_call_id(agent, call_id), format!("{who}: {summary}"), "other")
        }
        None => {
            let tool = state.tools.get(call_id);
            let title = tool.filter(|t| !t.title.is_empty()).map_or(summary.as_str(), |t| t.title.as_str());
            (call_id.clone(), title.to_string(), tool.map_or("other", |t| t.kind))
        }
    };
    let params = json!({
        "sessionId": sid,
        "toolCall": { "toolCallId": tool_call_id, "title": title, "kind": kind, "status": "pending" },
        "options": out::permission_options(proposed_rule.as_ref().map(|r| r.prefix.as_slice()), *proposed_mode, proposed_grants),
    });
    state.permission_by_call.insert(tool_call_id, id.clone());
    out.push(Bytes::from(her_rpc::request(&id, out::REQUEST_PERMISSION, &params)));
}

/// Answered some other way: withdraws our request, if it is still open.
fn withdraw_permission(shared: &Shared, state: &mut SessionState, tool_call_id: &str, out: &mut Vec<Bytes>) {
    if let Some(id) = state.permission_by_call.remove(tool_call_id)
        && shared.permissions.lock().remove(&id).is_some()
    {
        out.push(Bytes::from(her_rpc::notification(
            "$/cancel_request",
            &json!({ "requestId": id }),
        )));
    }
}

impl Encoder for AcpEncoder {
    fn encode(&mut self, item: Outgoing, out: &mut Vec<Bytes>) {
        match item {
            Outgoing::Event(frame) => self.event(&frame, out),
            Outgoing::Frame(bytes) => out.push(bytes),
            Outgoing::Detached(params) => {
                if let Some(mut state) = self.sessions.remove(params.session_id.as_str()) {
                    answer_prompt(&self.shared, &mut state, Err(errors::session_closed(&params.session_id)), out);
                }
            }
            Outgoing::Control(control) => {
                let Ok(control) = control.downcast::<Control>() else { return };
                match *control {
                    Control::Opened { session, info } => {
                        let state = self.sessions.entry(session).or_insert_with(|| SessionState::new(None));
                        state.info = Some(info);
                    }
                    Control::Prompt { session, id } => {
                        let state = self.sessions.entry(session).or_insert_with(|| SessionState::new(None));
                        state.prompt = Some(id);
                        state.skip_user = true;
                    }
                }
            }
        }
    }
}
