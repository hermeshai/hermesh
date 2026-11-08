//! The ACP (Agent Client Protocol) projection: editors drive her sessions over stdio.
//!
//! Requests are parsed with the official schema types; outgoing messages are serialized
//! straight from kernel events in this process (see `out`). Approvals become
//! `session/request_permission` requests, and a `session/prompt` is answered when its task ends.

mod content;
mod encoder;
mod mcp;
mod out;
mod schema;

use bytes::Bytes;
use her_llm::ThinkingLevel;
use her_protocol::ApprovalDecision;
use her_protocol::PermissionMode;
use her_protocol::Op;
use her_protocol::PromptMode;
use her_protocol::api::EventClass;
use her_protocol::api::EventFilter;
use her_protocol::api::SessionListParams;
use her_protocol::api::SessionSnapshot;
use her_protocol::api::SkillsListParams;
use her_rpc::Id;
use her_rpc::Kind;
use her_rpc::RawMessage;
use her_rpc::RpcError;
use her_server::AttachRequest;
use her_server::ConnHandle;
use her_server::Encoder;
use her_server::NewSession;
use her_server::Outgoing;
use her_server::Protocol;
use her_server::ResumeOverrides;
use her_server::Server;
use parking_lot::Mutex;
use serde::Deserialize;
use serde_json::Value;
use serde_json::json;
use serde_json::value::RawValue;
use std::collections::HashMap;
use std::future::Future;
use std::str::FromStr;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::Ordering;
use tokio::task::AbortHandle;

use crate::encoder::AcpEncoder;
use crate::encoder::Control;
use crate::encoder::Shared;
use crate::schema::InitializeRequest;
use crate::schema::ListSessionsRequest;
use crate::schema::NewSessionRequest;
use crate::schema::OpenSessionRequest;
use crate::schema::Outcome;
use crate::schema::PromptRequest;
use crate::schema::RequestPermissionResponse;
use crate::schema::SessionRequest;
use crate::schema::SetConfigOptionRequest;

/// The ACP version implemented here.
pub const ACP_VERSION: u16 = 1;

pub struct AcpProtocol {
    server: Server,
    shared: Arc<Shared>,
    initialized: AtomicBool,
    /// Spawned requests that `$/cancel_request` can abort (session/load replay and the like).
    running: Arc<Mutex<HashMap<Id, AbortHandle>>>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct CancelRequest {
    request_id: Id,
}

const AUTH_REQUIRED: i64 = -32000;
const CHATGPT: &str = "her-chatgpt";
const SAVED: &str = "her-saved";
const TERMINAL: &str = "her-terminal";

/// The ways in (standalone-cli-design §10.1). The terminal one is only for a client that can run it.
fn auth_methods(terminal: bool) -> Value {
    let mut methods = Vec::new();
    if terminal {
        methods.push(json!({ "id": TERMINAL, "name": "Sign in from a terminal", "type": "terminal", "args": ["--login"] }));
    }
    methods.push(json!({ "id": CHATGPT, "name": "Sign in with ChatGPT", "description": "Opens your browser" }));
    methods.push(json!({ "id": SAVED, "name": "Use the credentials saved for her", "description": "Run `her login` in a terminal first" }));
    Value::Array(methods)
}

fn auth_required(message: String) -> RpcError {
    RpcError::with_kind(AUTH_REQUIRED, "auth_required", message)
}

/// What is missing for a new session at `cwd` to run, if anything.
fn missing_credentials(server: &Server, cwd: &std::path::Path) -> Option<String> {
    let model = server.default_model_at(cwd)?;
    let resolved = server.models().resolve(&model).ok()?;
    let provider = server.models().provider(&resolved.model.provider)?;
    matches!(provider.auth.status(&provider.id), her_llm::AuthStatus::Missing(_))
        .then(|| format!("{model} has no credentials yet: run `her login {}` in a terminal, or sign in here", provider.id))
}

async fn sign_in_with_chatgpt(server: &Server) -> Result<(), RpcError> {
    use her_protocol::api::ChatGptLoginState;
    let chatgpt = server.connections().chatgpt.clone();
    let started = chatgpt.start().await.map_err(|err| auth_required(err.to_string()))?;
    if let Some(url) = &started.authorize_url {
        her_core::open_in_browser(url);
    }
    // Aborting the request (`$/cancel_request`) drops this, and with it the sign-in.
    struct Cancel(Arc<her_connections::chatgpt::ChatGpt>, Option<String>);
    impl Drop for Cancel {
        fn drop(&mut self) {
            if let Some(id) = &self.1 {
                let _ = self.0.cancel(id);
            }
        }
    }
    let mut cancel = Cancel(chatgpt.clone(), started.login_id.clone());
    loop {
        tokio::time::sleep(std::time::Duration::from_millis(300)).await;
        let status = chatgpt.status().map_err(|err| auth_required(err.to_string()))?;
        match status.state {
            ChatGptLoginState::Pending => {}
            ChatGptLoginState::Connected => {
                cancel.1 = None;
                return Ok(());
            }
            _ => {
                cancel.1 = None;
                return Err(auth_required(status.message.unwrap_or_else(|| "the sign-in did not finish".to_string())));
            }
        }
    }
}

/// A session cannot start until the person sets something up: in ACP, `auth_required`.
fn auth_required_for_no_model(err: RpcError) -> RpcError {
    if matches!(err.kind(), Some("no_model" | "no_credentials")) {
        return RpcError::with_kind(AUTH_REQUIRED, "auth_required", err.message);
    }
    err
}

fn reply(id: &Id, result: Result<Value, RpcError>) -> Vec<u8> {
    match result {
        Ok(value) => her_rpc::response(id, &value),
        Err(error) => her_rpc::error_response(Some(id), &error),
    }
}

/// Tells the client what typing `/` offers in a session it opened, right after the reply to
/// the open: the hub has queued that reply by the time `list_skills` returns.
async fn announce_commands(server: &Server, conn: &ConnHandle, session: &str) {
    let params = SkillsListParams {
        session_id: Some(session.to_string()),
        cwd: None,
    };
    match server.list_skills(params).await {
        Ok(list) => conn.queue.push(Outgoing::Frame(out::update(session, out::CommandsOut::new(&list.commands)))),
        Err(err) => tracing::debug!(session, error = %err.message, "no commands to announce"),
    }
}

/// ACP never sees tool-argument deltas; the tool call is sent once complete.
fn acp_filter() -> EventFilter {
    EventFilter {
        exclude: vec![EventClass::ToolCallDelta],
    }
}

impl AcpProtocol {
    #[allow(clippy::new_ret_no_self)]
    pub fn new(server: Server) -> Arc<dyn Protocol> {
        Arc::new(Self {
            server,
            shared: Arc::new(Shared::default()),
            initialized: AtomicBool::new(false),
            running: Arc::new(Mutex::new(HashMap::new())),
        })
    }

    fn spawn<F>(&self, id: &Id, conn: &ConnHandle, work: F)
    where
        F: Future<Output = Option<Vec<u8>>> + Send + 'static,
    {
        let running = self.running.clone();
        let conn = conn.clone();
        let key = id.clone();
        let mut guard = self.running.lock();
        let task = tokio::spawn(async move {
            let out = work.await;
            if running.lock().remove(&key).is_some()
                && let Some(out) = out
            {
                conn.send(out);
            }
        });
        guard.insert(id.clone(), task.abort_handle());
    }

    /// The reply to new/load/resume, queued before the session's first event, then a notice
    /// for each line of `notices` when the client shows notices.
    fn opened(&self, conn: &ConnHandle, id: &Id, replay: bool, notices: Vec<String>) -> AttachRequest {
        let server = self.server.clone();
        let reply_id = id.clone();
        let notices = if self.shared.notices.load(Ordering::Relaxed) { notices } else { Vec::new() };
        AttachRequest {
            conn: conn.clone(),
            filter: acp_filter(),
            history_limit: if replay { None } else { Some(0) },
            respond: Box::new(move |snapshot: &SessionSnapshot| {
                let session = snapshot.session.session_id.as_str();
                let mut items = vec![Outgoing::Control(Box::new(Control::Opened {
                    session: session.into(),
                    info: snapshot.session.clone(),
                }))];
                if replay {
                    items.extend(
                        content::replay(session, &snapshot.session.cwd, &snapshot.transcript.items)
                            .into_iter()
                            .map(Outgoing::Frame),
                    );
                }
                let mut result = json!({ "configOptions": out::config_options(&server, &snapshot.session) });
                if !replay && snapshot.last_seq == 0 && snapshot.transcript.items.is_empty() {
                    result["sessionId"] = json!(session);
                }
                items.push(Outgoing::Frame(Bytes::from(her_rpc::response(&reply_id, &result))));
                for notice in &notices {
                    let update = out::NoticeOut {
                        session_update: "notice",
                        severity: "warning",
                        title: notice,
                    };
                    items.push(Outgoing::Frame(out::update(session, update)));
                }
                items
            }),
        }
    }

    fn request(&self, id: &Id, method: &str, params: Option<&RawValue>, conn: &ConnHandle) {
        if method != "initialize" && !self.initialized.load(Ordering::Relaxed) {
            conn.send_error(Some(id), &her_server::errors::not_initialized());
            return;
        }
        let server = self.server.clone();
        match method {
            "initialize" => match her_rpc::params::<InitializeRequest>(params) {
                Ok(request) => {
                    self.shared
                        .notices
                        .store(request.session_capability("notices"), Ordering::Relaxed);
                    self.shared
                        .compaction
                        .store(request.session_capability("compaction"), Ordering::Relaxed);
                    self.initialized.store(true, Ordering::Relaxed);
                    let methods = auth_methods(request.can_run_terminal_auth());
                    let result = json!({
                        "protocolVersion": ACP_VERSION,
                        "agentInfo": { "name": "her", "title": "her", "version": env!("CARGO_PKG_VERSION") },
                        "authMethods": methods,
                        "agentCapabilities": {
                            "loadSession": true,
                            "promptCapabilities": { "image": true, "audio": false, "embeddedContext": true },
                            "mcpCapabilities": { "http": true, "sse": false },
                            "sessionCapabilities": {
                                "list": {}, "resume": {}, "close": {}, "delete": {}, "additionalDirectories": {}
                            }
                        }
                    });
                    conn.send(her_rpc::response(id, &result));
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            "authenticate" => match her_rpc::params::<schema::AuthenticateRequest>(params) {
                Ok(request) => {
                    let reply_id = id.clone();
                    self.spawn(id, conn, async move {
                        let result = match request.method_id.as_str() {
                            SAVED => {
                                let at = std::env::current_dir().unwrap_or_else(|_| std::path::PathBuf::from("/"));
                                let checked = server.clone();
                                let found = tokio::task::spawn_blocking(move || (checked.default_model_at(&at), missing_credentials(&checked, &at))).await.ok();
                                match found {
                                    Some((Some(_), None)) => Ok(json!({})),
                                    Some((_, Some(missing))) => Err(auth_required(missing)),
                                    _ => Err(auth_required("no credentials are saved for her: run `her login` in a terminal".to_string())),
                                }
                            }
                            CHATGPT => sign_in_with_chatgpt(&server).await.map(|()| json!({})),
                            other => Err(RpcError::invalid_params(format!("no sign-in method {other}"))),
                        };
                        Some(reply(&reply_id, result))
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            "session/new" => match her_rpc::params::<NewSessionRequest>(params) {
                Ok(request) => {
                    let (mcp_servers, skipped) = mcp::servers(&request.mcp_servers);
                    for line in &skipped {
                        tracing::warn!("{line}");
                    }
                    let attach = self.opened(conn, id, false, skipped);
                    let args = NewSession {
                        cwd: Some(request.cwd),
                        extra_roots: request.additional_directories,
                        mcp_servers,
                        require_credentials: true,
                        ..NewSession::default()
                    };
                    let (reply_id, conn_out) = (id.clone(), conn.clone());
                    self.spawn(id, conn, async move {
                        match server.create_session(args, attach).await {
                            Ok(session) => {
                                announce_commands(&server, &conn_out, &session).await;
                                None
                            }
                            Err(err) => Some(her_rpc::error_response(Some(&reply_id), &auth_required_for_no_model(err))),
                        }
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            "session/load" | "session/resume" => {
                match her_rpc::params::<OpenSessionRequest>(params) {
                    Ok(request) => {
                        let session = request.session_id;
                        // A loaded session keeps the servers it started with.
                        let (mcp_servers, skipped) = if server.is_loaded(&session) {
                            (Vec::new(), Vec::new())
                        } else {
                            mcp::servers(&request.mcp_servers)
                        };
                        for line in &skipped {
                            tracing::warn!("{line}");
                        }
                        let attach = self.opened(conn, id, method == "session/load", skipped);
                        let overrides = ResumeOverrides {
                            mcp_servers,
                            ..ResumeOverrides::default()
                        };
                        let (reply_id, conn_out) = (id.clone(), conn.clone());
                        self.spawn(id, conn, async move {
                            match server.resume_session(&session, overrides, attach).await {
                                Ok(()) => {
                                    announce_commands(&server, &conn_out, &session).await;
                                    None
                                }
                                Err(err) => Some(her_rpc::error_response(Some(&reply_id), &err)),
                            }
                        });
                    }
                    Err(err) => conn.send_error(Some(id), &err),
                }
            }
            "session/list" => match her_rpc::params::<Option<ListSessionsRequest>>(params) {
                Ok(request) => {
                    let params = SessionListParams {
                        cwd: request.as_ref().and_then(|r| r.cwd.clone()),
                        cursor: request.as_ref().and_then(|r| r.cursor.clone()),
                        limit: None,
                    };
                    let reply_id = id.clone();
                    self.spawn(id, conn, async move {
                        let result = tokio::task::spawn_blocking(move || server.list_sessions(&params))
                            .await
                            .unwrap_or_else(|e| Err(RpcError::internal(e.to_string())))
                            .map(|list| {
                                let sessions: Vec<Value> = list
                                    .sessions
                                    .iter()
                                    .map(|s| {
                                        json!({ "sessionId": s.session_id, "cwd": s.cwd,
                                            "title": s.preview, "updatedAt": s.updated_at })
                                    })
                                    .collect();
                                let mut result = json!({ "sessions": sessions });
                                if let Some(next) = list.next_cursor {
                                    result["nextCursor"] = json!(next);
                                }
                                result
                            });
                        Some(reply(&reply_id, result))
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            "session/close" => match her_rpc::params::<SessionRequest>(params) {
                Ok(request) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        let result = server.close_session(&request.session_id).await.map(|()| json!({}));
                        conn.send(reply(&id, result));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            "session/delete" => match her_rpc::params::<SessionRequest>(params) {
                Ok(request) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        let session = request.session_id;
                        if server.is_loaded(&session) {
                            let _ = server.close_session(&session).await;
                        }
                        let result = tokio::task::spawn_blocking(move || server.delete_session(&session))
                            .await
                            .unwrap_or_else(|e| Err(RpcError::internal(e.to_string())))
                            .map(|()| json!({}));
                        conn.send(reply(&id, result));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            "session/prompt" => match her_rpc::params::<PromptRequest>(params) {
                Ok(request) => match content::prompt_input(&request.prompt) {
                    Ok(input) => {
                        let session: Arc<str> = request.session_id.as_str().into();
                        let (conn, id) = (conn.clone(), id.clone());
                        let shared = self.shared.clone();
                        tokio::spawn(async move {
                            let reply_id = id.clone();
                            let registered = session.clone();
                            let prompts = shared.clone();
                            let respond: her_server::hub::OpResponder = Box::new(move |result| match result {
                                Ok(()) => {
                                    prompts.prompts.lock().insert(reply_id.clone(), registered.to_string());
                                    vec![Outgoing::Control(Box::new(Control::Prompt {
                                        session: registered,
                                        id: reply_id,
                                    }))]
                                }
                                Err(err) => vec![Outgoing::Frame(Bytes::from(her_rpc::error_response(Some(&reply_id), &err)))],
                            });
                            // `/compact` and `/<skill>` are read by the kernel, as for every front end.
                            let op = Op::Prompt {
                                input,
                                mode: PromptMode::Steer,
                                commands: true,
                            };
                            if let Err(err) = server.submit(&session, conn.clone(), op, true, respond).await {
                                conn.send_error(Some(&id), &err);
                            }
                        });
                    }
                    Err(err) => conn.send_error(Some(id), &err),
                },
                Err(err) => conn.send_error(Some(id), &err),
            },
            "session/set_config_option" => match her_rpc::params::<SetConfigOptionRequest>(params) {
                Ok(request) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        let result = set_config(&server, &conn, request).await;
                        conn.send(reply(&id, result));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            other => conn.send_error(Some(id), &RpcError::method_not_found(other)),
        }
    }

    fn notification(&self, method: &str, params: Option<&RawValue>, conn: &ConnHandle) {
        match method {
            "session/cancel" => {
                if let Ok(request) = her_rpc::params::<SessionRequest>(params) {
                    self.interrupt(request.session_id, conn);
                }
            }
            "$/cancel_request" => {
                let Ok(request) = her_rpc::params::<CancelRequest>(params) else { return };
                let prompt = self.shared.prompts.lock().get(&request.request_id).cloned();
                if let Some(session) = prompt {
                    self.interrupt(session, conn);
                } else if let Some(task) = self.running.lock().remove(&request.request_id) {
                    task.abort();
                    conn.send_error(Some(&request.request_id), &RpcError::cancelled());
                }
            }
            _ => {}
        }
    }

    fn interrupt(&self, session: String, conn: &ConnHandle) {
        let server = self.server.clone();
        let conn = conn.clone();
        tokio::spawn(async move {
            let _ = server
                .submit(&session, conn, Op::Interrupt, false, Box::new(|_| Vec::new()))
                .await;
        });
    }

    /// The client answered one of our permission requests.
    /// (See [`decode_option`] for the option ids.)
    fn permission_answer(&self, id: &Id, result: Result<&RawValue, &RpcError>, conn: &ConnHandle) {
        let Some(asked) = self.shared.permissions.lock().remove(id) else {
            return;
        };
        let decision = match result {
            Ok(raw) => match serde_json::from_str::<RequestPermissionResponse>(raw.get()) {
                Ok(response) => match response.outcome {
                    Outcome::Selected { option_id } => decode_option(&option_id),
                    Outcome::Cancelled => ApprovalDecision::Abort,
                },
                Err(_) => ApprovalDecision::Deny { feedback: None },
            },
            Err(_) => ApprovalDecision::Deny { feedback: None },
        };
        let server = self.server.clone();
        let conn = conn.clone();
        tokio::spawn(async move {
            let op = match asked.agent {
                Some(agent) => Op::AgentApproval { agent, call_id: asked.call_id, decision },
                None => Op::Approval { call_id: asked.call_id, decision },
            };
            let _ = server.submit(&asked.session, conn, op, false, Box::new(|_| Vec::new())).await;
        });
    }
}

async fn set_config(server: &Server, conn: &ConnHandle, request: SetConfigOptionRequest) -> Result<Value, RpcError> {
    let session = request.session_id;
    let mut info = server.info(&session).await?;
    let value = match &request.value {
        Value::String(value) => value.clone(),
        _ => return Err(RpcError::invalid_params("her options take a value id")),
    };
    let op = match request.config_id.as_str() {
        "model" => {
            info.model = value.clone();
            Op::SetModel {
                model: value,
                thinking: None,
            }
        }
        "thinking" => {
            let level = ThinkingLevel::from_str(&value).map_err(|_| RpcError::invalid_params(format!("unknown level '{value}'")))?;
            info.thinking = level;
            Op::SetModel {
                model: info.model.clone(),
                thinking: Some(level),
            }
        }
        // `approval_mode` is the id this option had before permission modes were renamed.
        "permission_mode" | "approval_mode" => {
            let (mode, _) = PermissionMode::parse_with_notice(&value).map_err(RpcError::invalid_params)?;
            info.permission_mode = mode;
            Op::SetPermissionMode { mode }
        }
        other => return Err(RpcError::invalid_params(format!("unknown config option '{other}'"))),
    };
    let (tx, rx) = tokio::sync::oneshot::channel();
    let respond: her_server::hub::OpResponder = Box::new(move |result| {
        let _ = tx.send(result);
        Vec::new()
    });
    server.submit(&session, conn.clone(), op, false, respond).await?;
    rx.await.map_err(|_| RpcError::internal("the session closed"))??;
    Ok(json!({ "configOptions": out::config_options(server, &info) }))
}

impl Protocol for AcpProtocol {
    fn on_line(self: Arc<Self>, line: Bytes, conn: &ConnHandle) {
        let message = match RawMessage::parse(&line) {
            Ok(message) => message,
            Err(err) => {
                conn.send_error(None, &err);
                return;
            }
        };
        match message.kind() {
            Kind::Request { id, method, params } => self.request(id, method, params, conn),
            Kind::Notification { method, params } => self.notification(method, params, conn),
            Kind::Response { id, result } => self.permission_answer(id, result, conn),
            Kind::Invalid => conn.send_error(message.id.as_ref(), &RpcError::invalid_request("not a request, notification or response")),
        }
    }

    fn encoder(&self, _conn: &ConnHandle) -> Box<dyn Encoder> {
        Box::new(AcpEncoder::new(self.server.clone(), self.shared.clone()))
    }

    fn on_closed(&self, _conn: &ConnHandle) {
        for (_, task) in self.running.lock().drain() {
            task.abort();
        }
    }
}

/// The decision for a permission option the client picked: `approve`, `approve_for_session`,
/// `approve_for_session:<index>` for one of the proposed site grants, or
/// `approve_always:<index>` to keep that grant in the configuration.
fn decode_option(option_id: &str) -> ApprovalDecision {
    match option_id {
        "approve" => ApprovalDecision::Approve,
        "approve_for_session" => ApprovalDecision::ApproveForSession { grant: None, persist: false },
        other => {
            let (index, persist) = match (other.strip_prefix("approve_for_session:"), other.strip_prefix("approve_always:")) {
                (Some(index), _) => (index, false),
                (_, Some(index)) => (index, true),
                _ => return ApprovalDecision::Deny { feedback: None },
            };
            match index.parse::<usize>() {
                Ok(index) => ApprovalDecision::ApproveForSession { grant: Some(index), persist },
                Err(_) => ApprovalDecision::Deny { feedback: None },
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_protocol::GrantAuthority;
    use her_protocol::SiteGrant;

    #[test]
    fn site_grants_round_trip_through_permission_options() {
        let grants = [
            SiteGrant {
                authority: GrantAuthority::Navigate,
                origin: "https://example.com".into(),
            },
            SiteGrant {
                authority: GrantAuthority::Navigate,
                origin: "*".into(),
            },
        ];
        let options = out::permission_options(None, None, &grants);
        let ids: Vec<&str> = options.as_array().unwrap().iter().map(|o| o["optionId"].as_str().unwrap()).collect();
        assert_eq!(
            ids,
            ["approve", "approve_for_session:0", "approve_for_session:1", "approve_always:0", "approve_always:1", "deny"],
            "every site can be kept too"
        );
        assert_eq!(decode_option("approve"), ApprovalDecision::Approve);
        assert_eq!(decode_option("approve_for_session"), ApprovalDecision::ApproveForSession { grant: None, persist: false });
        assert_eq!(decode_option("approve_for_session:1"), ApprovalDecision::ApproveForSession { grant: Some(1), persist: false });
        assert_eq!(decode_option("approve_always:0"), ApprovalDecision::ApproveForSession { grant: Some(0), persist: true });
        assert_eq!(decode_option("approve_always:x"), ApprovalDecision::Deny { feedback: None });
        assert_eq!(decode_option("deny"), ApprovalDecision::Deny { feedback: None });
    }
}
