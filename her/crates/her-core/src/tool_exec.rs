//! One batch of tool calls: sequential preflight (argument repair, lookup, hooks, checks,
//! approval), concurrent execution, results in call order.

use her_llm::JsonText;
use her_llm::ToolCall;
use her_llm::ToolResultMessage;
use her_llm::UserContent;
use her_llm::now_ms;
use her_protocol::ApprovalDecision;
use her_protocol::CommandRule;
use her_protocol::EventMsg;
use her_protocol::Location;
use her_protocol::PermissionMode;
use her_protocol::SiteGrant;
use her_protocol::ToolCategory;
use her_tools::Authority;
use her_tools::ProgressSink;
use her_tools::Tool;
use her_tools::ToolCtx;
use her_tools::ToolDescription;
use her_tools::ToolError;
use her_tools::ToolKind;
use her_tools::ToolOutput;
use her_tools::cap_result;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use tokio::sync::OwnedMutexGuard;
use tokio::sync::oneshot;

use crate::context::NOT_EXECUTED;
use crate::hooks::Gate;
use crate::session::Session;
use crate::task::TaskCtx;

pub(crate) const NO_APPROVER: &str = "This tool call needs approval, but nobody can approve it in this \
    non-interactive run. Do not retry it; tell the user what you wanted to do instead.";
/// The same for a sub-agent: its parent decides what to do instead.
pub(crate) const SUB_AGENT_NO_APPROVER: &str = "This tool call needs approval, but nobody can approve it in this \
    non-interactive run. Do not retry it or work around it; list it in your report so your parent can handle it.";
pub(crate) const TRUNCATED_CALL: &str = "The response hit the output token limit, so the arguments may be \
    incomplete. Re-issue the tool call.";
/// How long a cancelled tool may take to stop on its own.
const CANCEL_GRACE: Duration = Duration::from_secs(3);

enum Prepared {
    Done(Arc<ToolResultMessage>),
    /// With what the call was approved to do, for the tool to hold execution to.
    Run(Arc<dyn Tool>, ToolCall, Arc<[Authority]>),
}

enum Approval {
    Granted,
    Denied(Option<String>),
    /// Carries the reason the call needed approval.
    NoApprover(Option<String>),
    Aborted,
}

pub(crate) async fn execute_batch(
    session: &Arc<Session>,
    task: &TaskCtx,
    calls: Vec<ToolCall>,
) -> Vec<Arc<ToolResultMessage>> {
    let mut prepared = Vec::with_capacity(calls.len());
    // Every call of a batch is looked up in the same registry.
    let tools = session.tools();
    // Set once a call is going to run before the rest of the batch. A check sees the state
    // before the batch, which a call that runs first may change (a read, too: write checks what
    // was read), so from then on checks could refuse calls that would succeed.
    let mut runs_before = false;
    for mut call in calls {
        let tool = session.tool(&tools, &call.name);
        if let Some(tool) = &tool
            && call.arguments_error.is_none()
        {
            call.arguments = tool.prepare_arguments(call.arguments.to_value()).into();
        }
        session.emit(started(session, &call, tool.as_deref()));
        if task.turn().is_cancelled() {
            prepared.push(Prepared::Done(immediate(session, &call, NOT_EXECUTED)));
            continue;
        }
        let Some(tool) = tool else {
            let text = format!(
                "Unknown tool '{}'. Available tools: {}.",
                call.name,
                tools.names().join(", ")
            );
            prepared.push(Prepared::Done(immediate(session, &call, &text)));
            continue;
        };
        if let Some(problem) = &call.arguments_error {
            let text = format!("Invalid arguments: {problem}");
            prepared.push(Prepared::Done(immediate(session, &call, &text)));
            continue;
        }
        // What the call does on which site, judged from the session's state before the batch
        // runs (browser-design §7.1); execution is held to it.
        let authorities: Arc<[Authority]> = match authorities(session, task, tool.as_ref(), &call).await {
            Ok(authorities) => authorities.into(),
            Err(err) => {
                let text = match err {
                    ToolError::Cancelled => NOT_EXECUTED.to_string(),
                    err => err.to_string(),
                };
                prepared.push(Prepared::Done(immediate(session, &call, &text)));
                continue;
            }
        };
        let Some((verdict, _asking)) = judge(session, task, &call, tool.as_ref(), &authorities).await else {
            prepared.push(Prepared::Done(immediate(session, &call, NOT_EXECUTED)));
            continue;
        };
        match verdict {
            Gate::Allow => {
                runs_before = true;
                prepared.push(Prepared::Run(tool, call, authorities));
            }
            Gate::Deny(reason) => {
                let text = format!("Tool call denied: {reason}");
                prepared.push(Prepared::Done(immediate(session, &call, &text)));
            }
            Gate::Ask {
                summary,
                reason,
                proposed_rule,
                proposed_mode,
                proposed_grants,
                own_browser,
            } => {
                // Nobody should be asked about a call that cannot succeed.
                if !runs_before && let Err(err) = check(session, task, tool.as_ref(), &call).await {
                    let text = match err {
                        ToolError::Cancelled => NOT_EXECUTED.to_string(),
                        err => err.to_string(),
                    };
                    prepared.push(Prepared::Done(immediate(session, &call, &text)));
                    continue;
                }
                let proposal = Proposal {
                    rule: proposed_rule,
                    mode: proposed_mode,
                    grants: proposed_grants,
                    own_browser,
                };
                match request_approval(session, task, &call, summary, reason, proposal).await {
                    Approval::Granted => {
                        runs_before = true;
                        prepared.push(Prepared::Run(tool, call, authorities));
                    }
                    Approval::Denied(feedback) => {
                        let mut text = "The user denied this tool call.".to_string();
                        if let Some(feedback) = feedback.filter(|f| !f.trim().is_empty()) {
                            text.push_str(&format!(" Feedback: {feedback}"));
                        }
                        prepared.push(Prepared::Done(immediate(session, &call, &text)));
                    }
                    Approval::NoApprover(reason) => {
                        let base = if session.sub_agent { SUB_AGENT_NO_APPROVER } else { NO_APPROVER };
                        let text = match reason {
                            Some(reason) => format!("{base} Why it needs approval: {reason}"),
                            None => base.to_string(),
                        };
                        prepared.push(Prepared::Done(immediate(session, &call, &text)));
                    }
                    Approval::Aborted => {
                        task.cancel.cancel();
                        prepared.push(Prepared::Done(immediate(session, &call, NOT_EXECUTED)));
                    }
                }
            }
        }
    }
    let runs = prepared.into_iter().map(|item| async move {
        match item {
            Prepared::Done(result) => result,
            Prepared::Run(tool, call, authorities) => run_tool(session, task, tool, call, authorities).await,
        }
    });
    futures::future::join_all(runs).await
}

/// Error results for every call of a truncated response, with the usual events.
pub(crate) fn reject_all(session: &Session, calls: &[ToolCall], text: &str) -> Vec<Arc<ToolResultMessage>> {
    calls
        .iter()
        .map(|call| {
            let tool = session.tool(&session.tools(), &call.name);
            session.emit(started(session, call, tool.as_deref()));
            immediate(session, call, text)
        })
        .collect()
}

/// The tool_started event, with the tool's own description of the call.
fn started(session: &Session, call: &ToolCall, tool: Option<&dyn Tool>) -> EventMsg {
    let description = match tool {
        Some(tool) => tool.describe(&call.arguments.to_value(), session.env.as_ref()),
        None => ToolDescription::named(&call.name),
    };
    EventMsg::ToolStarted {
        call_id: call.id.clone(),
        name: call.name.clone(),
        args: call.arguments.to_value(),
        category: match description.category {
            her_tools::ToolCategory::Read => ToolCategory::Read,
            her_tools::ToolCategory::Edit => ToolCategory::Edit,
            her_tools::ToolCategory::Delete => ToolCategory::Delete,
            her_tools::ToolCategory::Move => ToolCategory::Move,
            her_tools::ToolCategory::Search => ToolCategory::Search,
            her_tools::ToolCategory::Execute => ToolCategory::Execute,
            her_tools::ToolCategory::Fetch => ToolCategory::Fetch,
            her_tools::ToolCategory::Other => ToolCategory::Other,
        },
        title: description.title,
        locations: description
            .locations
            .into_iter()
            .map(|(path, line)| Location { path, line })
            .collect(),
    }
}

async fn authorities(session: &Session, task: &TaskCtx, tool: &dyn Tool, call: &ToolCall) -> Result<Vec<Authority>, ToolError> {
    let ctx = tool_ctx(session, task, call, Arc::from([]));
    until_cancelled(task, tool.authorities(&call.arguments.to_value(), &ctx)).await
}

async fn gate(session: &Session, call: &ToolCall, tool: &dyn Tool, authorities: &[Authority]) -> Gate {
    let view = session.view();
    for hook in &session.hooks {
        match hook.before_tool(call, tool, authorities, &view).await {
            Gate::Allow => continue,
            other => return other,
        }
    }
    Gate::Allow
}

/// The verdict on a call and, when it has to ask, its turn to: the whole tree asks the user one
/// question at a time (subagent-design §10.1). A call that had to wait is judged again, as an
/// answer given meanwhile may settle it. `None` when the turn ended while it waited.
async fn judge(
    session: &Session,
    task: &TaskCtx,
    call: &ToolCall,
    tool: &dyn Tool,
    authorities: &[Authority],
) -> Option<(Gate, Option<OwnedMutexGuard<()>>)> {
    let verdict = gate(session, call, tool, authorities).await;
    if !matches!(verdict, Gate::Ask { .. }) || !session.interactive_approvals {
        return Some((verdict, None));
    }
    if let Ok(asking) = session.approval_line.clone().try_lock_owned() {
        return Some((verdict, Some(asking)));
    }
    let turn = task.turn();
    let asking = tokio::select! {
        biased;
        _ = turn.cancelled() => return None,
        asking = session.approval_line.clone().lock_owned() => asking,
    };
    session.follow_main();
    let verdict = gate(session, call, tool, authorities).await;
    Some((verdict, Some(asking)))
}

/// What approving for the session would keep, and where the call works.
struct Proposal {
    rule: Option<CommandRule>,
    mode: Option<PermissionMode>,
    grants: Vec<SiteGrant>,
    own_browser: bool,
}

async fn request_approval(
    session: &Session,
    task: &TaskCtx,
    call: &ToolCall,
    summary: String,
    reason: Option<String>,
    proposal: Proposal,
) -> Approval {
    if !session.interactive_approvals {
        session.emit(EventMsg::ApprovalRefused {
            call_id: call.id.clone(),
            name: call.name.clone(),
            summary,
            reason: reason.clone(),
        });
        return Approval::NoApprover(reason);
    }
    let (tx, rx) = oneshot::channel();
    session.state.lock().pending_approvals.insert(call.id.clone(), tx);
    session.emit(EventMsg::ApprovalRequested {
        call_id: call.id.clone(),
        name: call.name.clone(),
        args: call.arguments.to_value(),
        summary,
        reason,
        proposed_rule: proposal.rule.clone(),
        proposed_mode: proposal.mode,
        proposed_grants: proposal.grants.clone(),
        own_browser: proposal.own_browser,
    });
    let decision = tokio::select! {
        decision = rx => decision.unwrap_or(ApprovalDecision::Abort),
        _ = task.cancel.cancelled() => ApprovalDecision::Abort,
    };
    session.state.lock().pending_approvals.remove(&call.id);
    session.emit(EventMsg::ApprovalResolved {
        call_id: call.id.clone(),
        decision: decision.clone(),
    });
    match decision {
        ApprovalDecision::Approve => Approval::Granted,
        ApprovalDecision::ApproveForSession { grant, persist } => {
            // What is kept is the main session's: the whole tree follows it.
            let main = session.main_session();
            let keeper = main.as_deref().unwrap_or(session);
            // With nothing proposed, this approves the call only.
            if let Some(rule) = proposal.rule {
                keeper.add_session_rule(rule);
            }
            if let Some(mode) = proposal.mode {
                keeper.set_permission_mode(mode);
            }
            // Only a grant that was proposed: a client cannot name a wider one. An index out
            // of range keeps nothing beyond this call.
            if let Some(grant) = proposal.grants.into_iter().nth(grant.unwrap_or(0)) {
                if persist {
                    keeper.remember_site(&grant);
                }
                keeper.add_site_grant(grant);
            }
            // The call goes on now, before the change reaches a sub-agent by itself.
            session.follow_main();
            Approval::Granted
        }
        ApprovalDecision::Deny { feedback } => Approval::Denied(feedback),
        ApprovalDecision::Abort => Approval::Aborted,
    }
}

fn immediate(session: &Session, call: &ToolCall, text: &str) -> Arc<ToolResultMessage> {
    let result = Arc::new(ToolResultMessage {
        call_id: call.id.clone(),
        tool_name: call.name.clone(),
        content: vec![UserContent::Text { text: text.to_string() }],
        is_error: true,
        details: None,
        ts: now_ms(),
        duration_ms: None,
    });
    session.emit(EventMsg::ToolCompleted {
        call_id: call.id.clone(),
        result: result.clone(),
    });
    result
}

async fn run_tool(
    session: &Arc<Session>,
    task: &TaskCtx,
    tool: Arc<dyn Tool>,
    call: ToolCall,
    authorities: Arc<[Authority]>,
) -> Arc<ToolResultMessage> {
    let started = Instant::now();
    let _slot = session.tool_slots.acquire().await.ok();
    let ctx = tool_ctx(session, task, &call, authorities);
    let limits = ctx.limits.clone();
    let running = || session.emit(EventMsg::ToolRunning { call_id: call.id.clone() });
    let result = match tool.kind_for(&call.arguments.to_value()) {
        ToolKind::ReadOnly => {
            let _guard = session.tool_gate.read().await;
            running();
            execute(tool.as_ref(), &call, ctx, task).await
        }
        ToolKind::Mutating => {
            let _guard = session.tool_gate.write().await;
            running();
            execute(tool.as_ref(), &call, ctx, task).await
        }
    };
    let mut output = match result {
        Ok(output) => output,
        Err(ToolError::Cancelled) => ToolOutput::error("Cancelled by user."),
        Err(err) => ToolOutput::error(err.to_string()),
    };
    for hook in &session.hooks {
        output = hook.after_tool(&call, output).await;
    }
    if output.ends_task && !output.is_error {
        task.request_end();
    }
    // Safety net on top of each tool's own limits (a fifth over them, room for their notes). The
    // result is cut once, here, and never changes afterwards; the full text stays readable in a
    // file.
    cap_result(
        &mut output.content,
        limits.safety_net_tokens(),
        Some(&session.output_dir),
        &call.id,
        limits.max_saved_bytes,
    );
    // The result stays in the history for the whole session; drop the slack left from
    // building the text, which can be as large as the text itself.
    for content in &mut output.content {
        if let UserContent::Text { text } = content {
            text.shrink_to_fit();
        }
    }
    let result = Arc::new(ToolResultMessage {
        call_id: call.id.clone(),
        tool_name: call.name.clone(),
        content: output.content,
        is_error: output.is_error,
        details: output.details.map(JsonText::from),
        ts: now_ms(),
        duration_ms: Some(started.elapsed().as_millis() as u64),
    });
    session.emit(EventMsg::ToolCompleted {
        call_id: call.id.clone(),
        result: result.clone(),
    });
    result
}

fn tool_ctx(session: &Session, task: &TaskCtx, call: &ToolCall, authorities: Arc<[Authority]>) -> ToolCtx {
    let (model, _) = session.current_model();
    let emitter = session.emitter.clone();
    let progress_id = call.id.clone();
    ToolCtx {
        call_id: call.id.clone(),
        env: session.env.clone(),
        files: session.files.clone(),
        locks: session.locks.clone(),
        // A small window gets smaller results, so one call cannot fill it.
        limits: session.tool_limits(),
        progress: ProgressSink::new(move |text| {
            emitter.emit(EventMsg::ToolProgress {
                call_id: progress_id.clone(),
                text: text.to_string(),
            })
        }),
        cancel: task.turn().child_token(),
        model_accepts_images: model.accepts_images(),
        output_dir: session.output_dir.clone(),
        session_state: session.tool_state.clone(),
        authorities,
    }
}

/// The tool's own check of a call that is about to be put to the user.
async fn check(session: &Session, task: &TaskCtx, tool: &dyn Tool, call: &ToolCall) -> Result<(), ToolError> {
    let ctx = tool_ctx(session, task, call, Arc::from([]));
    until_cancelled(task, tool.check(&call.arguments.to_value(), &ctx)).await
}

async fn execute(tool: &dyn Tool, call: &ToolCall, ctx: ToolCtx, task: &TaskCtx) -> Result<ToolOutput, ToolError> {
    if task.turn().is_cancelled() {
        return Err(ToolError::Cancelled);
    }
    until_cancelled(task, tool.execute(call.arguments.to_value(), ctx)).await
}

/// Waits for a tool, but not forever once the turn is cancelled (the task interrupted, or
/// `steer_now`): the tool gets a grace period to
/// stop on its own (a command is killed within about 2.5 s), then the kernel stops waiting and
/// drops it, so a stuck call cannot hold the session.
async fn until_cancelled<T>(
    task: &TaskCtx,
    run: impl Future<Output = Result<T, ToolError>>,
) -> Result<T, ToolError> {
    let mut run = std::pin::pin!(run);
    let turn = task.turn();
    tokio::select! {
        result = &mut run => result,
        _ = turn.cancelled() => {
            tokio::time::timeout(CANCEL_GRACE, run).await.unwrap_or(Err(ToolError::Cancelled))
        }
    }
}
