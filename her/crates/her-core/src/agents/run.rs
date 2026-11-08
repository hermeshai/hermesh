//! A routine's run (docs/kernel/routine-design.md §2.1): a session like a sub-agent's that the
//! tree does not list. It writes nothing to the main session's log and sends it no events; what
//! it has to say arrives as one mail, and whoever started it gets how it went.

use her_llm::UserContent;
use her_protocol::AgentRef;
use her_protocol::AgentStatus;
use her_protocol::ErrorInfo;
use her_protocol::EventMsg;
use her_protocol::Op;
use her_protocol::PromptMode;
use her_protocol::bot::RoutineOutcome;
use std::path::PathBuf;
use std::sync::Arc;
use tokio::sync::mpsc;
use tokio::sync::oneshot;
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

use super::AgentTree;
use super::Mail;
use super::Report;
use super::driver::Round;
use super::mail;
use crate::kernel;
use crate::kernel::Kernel;
use crate::routine::RoutineRun;
use crate::routine::RunOutcome;
use crate::routine::quiet_note;
use crate::session::Internal;
use crate::session::Session;
use crate::store::AgentOrigin;
use crate::store::SessionHeader;

const BRIEF: &str = include_str!("prompts/routine_run.md");
/// Follows the time in a brief when the run starts well after it was due.
const LATE: &str = " This run is late because the computer was not running at the scheduled time: judge what still makes sense.";

/// A run under way.
pub(super) struct ActiveRun {
    id: String,
    stop: CancellationToken,
    /// Its session, once it has one: it follows the main session's permissions.
    pub(super) internal: Option<mpsc::UnboundedSender<Box<Internal>>>,
    driver: Option<JoinHandle<()>>,
}

impl ActiveRun {
    /// For the tree's shutdown: stops the run and gives what to wait for.
    pub(super) fn close(&mut self) -> Option<JoinHandle<()>> {
        self.stop.cancel();
        self.driver.take()
    }
}

/// The first message of a run.
fn brief(bot: &str, run: &RoutineRun) -> String {
    BRIEF
        .trim_end()
        .replace("{late}", if run.late { LATE } else { "" })
        .replace("{scheduled}", &run.scheduled)
        .replace("{now}", &run.now)
        .replace("{bot}", bot)
        .replace("{name}", &run.name)
        // Last: the user's words stay as they are, whatever they look like.
        .replace("{prompt}", run.prompt.trim())
}

/// A round that never got to run.
fn unstarted(status: AgentStatus, error: Option<ErrorInfo>) -> Report {
    let mut report = Round::new().finish();
    report.status = status;
    report.error = error;
    report
}

impl AgentTree {
    /// Starts a run in the background; the receiver gets how it went, after any mail it sent.
    pub(crate) fn start_run(self: &Arc<Self>, run: RoutineRun) -> Result<oneshot::Receiver<RunOutcome>, String> {
        let stop = CancellationToken::new();
        let (done, outcome) = oneshot::channel();
        let mut state = self.state.lock();
        if state.closing {
            return Err("the session is closing".into());
        }
        let id = run.id.clone();
        let driver = tokio::spawn({
            let (tree, stop, id) = (self.clone(), stop.clone(), id.clone());
            async move {
                let (report, log) = tree.carry_out(&run, &stop).await;
                let outcome = tree.conclude(&run, report, log);
                tree.state.lock().runs.retain(|active| active.id != id);
                let _ = done.send(outcome);
            }
        });
        state.runs.push(ActiveRun { id, stop, internal: None, driver: Some(driver) });
        Ok(outcome)
    }

    /// Stops a run. False when no run of that id is under way.
    pub(crate) fn stop_run(&self, id: &str) -> bool {
        let state = self.state.lock();
        let Some(run) = state.runs.iter().find(|active| active.id == id) else {
            return false;
        };
        run.stop.cancel();
        true
    }

    /// Starts the run's session. It starts from what the main session has now, as a sub-agent
    /// does, and nobody answers its questions: they are refused, and it says so in its report.
    fn start_run_session(&self, root: &Arc<Session>, run: &RoutineRun) -> Result<(Kernel, String), String> {
        let id = uuid::Uuid::now_v7().to_string();
        let mut header = SessionHeader::new(id, root.env.cwd().to_path_buf());
        header.agent = Some(AgentOrigin {
            root_id: root.id.clone(),
            parent_id: root.id.clone(),
            name: run.name.clone(),
            depth: 1,
            call_id: String::new(),
            parent_task: String::new(),
            routine: Some(run.routine_id.clone()),
        });
        let kernel = kernel::start_child(self.child_launch(root, header, false)).map_err(|err| format!("cannot start the run: {err}"))?;
        let bot = root.state.lock().bot.as_ref().map_or_else(|| "the bot".to_string(), |bot| bot.current.clone());
        Ok((kernel, brief(&bot, run)))
    }

    /// Runs it to its end and closes its session. The main session stays out of it: held only
    /// for the start, so that it can close meanwhile.
    async fn carry_out(&self, run: &RoutineRun, stop: &CancellationToken) -> (Report, Option<PathBuf>) {
        // A session just loaded is still connecting. A sub-agent's tool list starts from the
        // connectors as they are, and its main session has always waited for them by then.
        if let Some(mcp) = &self.deps.mcp {
            let failed = mcp.settle(stop).await;
            if stop.is_cancelled() {
                return (unstarted(AgentStatus::Interrupted, None), None);
            }
            if !failed.is_empty() {
                return (unstarted(AgentStatus::Failed, Some(crate::mcp::required_error(&failed))), None);
            }
        }
        let started = self.root().map_err(|err| err.to_string()).and_then(|root| self.start_run_session(&root, run));
        let (mut kernel, brief) = match started {
            Ok(started) => started,
            Err(message) => return (unstarted(AgentStatus::Failed, Some(ErrorInfo::new("run_not_started", message))), None),
        };
        let log = kernel.session_file().to_path_buf();
        if let Some(active) = self.state.lock().runs.iter_mut().find(|active| active.id == run.id) {
            active.internal = Some(kernel.internal.clone());
        }
        let _ = kernel
            .submit(Op::Prompt {
                input: vec![UserContent::Text { text: brief }],
                mode: PromptMode::Steer,
                commands: false,
            })
            .await;
        let mut round = Round::new();
        let mut finished = false;
        let mut stopping = false;
        loop {
            tokio::select! {
                event = kernel.next_event() => {
                    let Some(event) = event else { break };
                    round.note(&event.msg);
                    match event.msg {
                        EventMsg::Idle => {
                            finished = true;
                            break;
                        }
                        EventMsg::SessionClosed => break,
                        _ => {}
                    }
                }
                _ = stop.cancelled(), if !stopping => {
                    stopping = true;
                    let _ = kernel.submit(Op::Interrupt).await;
                }
            }
        }
        kernel.shutdown().await;
        let mut report = round.finish();
        if !finished {
            report.status = AgentStatus::Failed;
            report.error = Some(ErrorInfo::new("closed", "the run's session stopped unexpectedly"));
        }
        (report, Some(log))
    }

    /// What the run came to, and the mail when it has something to say or failed for the first
    /// time in a while (routine-design §2.2).
    fn conclude(&self, run: &RoutineRun, report: Report, log: Option<PathBuf>) -> RunOutcome {
        let usage = report.usage.clone();
        let ended = |outcome, note, report| RunOutcome { outcome, note, report, log: log.clone(), usage: usage.clone() };
        match report.status {
            AgentStatus::Interrupted => return ended(RoutineOutcome::Interrupted, None, None),
            AgentStatus::Failed => {}
            _ => {
                if let Some(note) = report.reply.as_deref().and_then(quiet_note) {
                    return ended(RoutineOutcome::Quiet, Some(note), None);
                }
            }
        }
        let failed = report.status == AgentStatus::Failed;
        let body = self.send_report(run, &report, !failed || run.report_failure);
        ended(if failed { RoutineOutcome::Failed } else { RoutineOutcome::Reported }, None, Some(body))
    }

    /// The report as kept: cut like a tool result. Sends it to the main session when `send`.
    fn send_report(&self, run: &RoutineRun, report: &Report, send: bool) -> String {
        let body = mail::body(report);
        let Some(link) = self.root.get() else {
            return body;
        };
        let Some(root) = link.session.upgrade() else {
            return body;
        };
        let seq = {
            let mut state = self.state.lock();
            state.mails += 1;
            // Counted until it arrives, so that the main session cannot go idle before.
            if send && !state.closing {
                state.in_flight += 1;
            }
            state.mails
        };
        let body = mail::cut(&root, &format!("routine-{}-{seq}", run.routine_id), body);
        if send && !self.state.lock().closing {
            let from = AgentRef { id: run.id.clone(), name: run.name.clone() };
            let message = Mail::routine_report(run, from, report.status, &body, seq);
            if link.internal.send(Box::new(Internal::Mail(message))).is_err() {
                tracing::debug!(routine = %run.routine_id, "the main session is gone; report dropped");
                self.mail_received();
            }
        }
        body
    }
}
