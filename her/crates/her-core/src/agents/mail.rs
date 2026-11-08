//! Reports: what a sub-agent's round of work comes to, as mail for the main session or as the
//! result of a call that waited for it. Messages: what it sends while it works, as mail for the
//! main session or another sub-agent.

use her_llm::UserContent;
use her_llm::UserMessage;
use her_protocol::AgentRef;
use her_protocol::AgentStatus;
use her_protocol::MailInfo;
use her_protocol::MailKind;
use her_tools::cap_result;
use std::time::Duration;

use super::Report;
use crate::routine::RoutineRun;
use crate::session::Session;

const TASK_TEMPLATE: &str = include_str!("prompts/child_task.md");
/// The part of a sub-agent's first message that says who hears of its work on the way: a session's
/// user follows its status, a bot's owner has only what it tells them (agent-messaging-design §17).
const ON_THE_WAY: &str = include_str!("prompts/child_on_the_way.md");
const BOT_ON_THE_WAY: &str = include_str!("prompts/bot_child_on_the_way.md");

/// Characters of a report shown in `agent_status`.
const SUMMARY_CHARS: usize = 200;

/// Follows the report of a stopped agent. Without it models take a stopped agent for a dead one
/// and start new agents on the same work when asked to continue.
const RESUMABLE: &str = "It keeps its work and context: send_message lets it continue where it stopped.";

/// Follow a routine's report: the bot writes what the owner reads (routine-design §2.3). Here
/// rather than in the bot's prompt, which is frozen when a conversation starts.
const ROUTINE_REPORTED: &str = "This routine is the user's own request: pass on what it found with send_to_user, in your own \
    words and without talking about runs or reports. If it holds nothing they asked to hear, send nothing and write only \
    NOTHING_TO_REPORT. If the job is finished, delete the routine.";
const ROUTINE_FAILED: &str = "This routine failed. Tell the user what failed and what would fix it with send_to_user. You will \
    not hear about later failures until it works again.";

/// The first message of a sub-agent: who it is, how to report, and the task. `bot`: a bot started it.
pub(crate) fn task_message(name: &str, task: &str, bot: bool) -> String {
    let on_the_way = if bot { BOT_ON_THE_WAY } else { ON_THE_WAY };
    TASK_TEMPLATE
        .trim_end()
        .replace("{on_the_way}", on_the_way.trim_end())
        .replace("{name}", name)
        .replace("{task}", task)
}

/// What a bot's sub-agent told the user: where it came from and what the bot's model reads of it.
pub(crate) fn to_user(from: AgentRef, text: &str, seq: u64) -> (MailInfo, UserMessage) {
    let message = UserMessage::text(format!("<sent_to_user from=\"{}\">\n{text}\n</sent_to_user>", from.name));
    let info = MailInfo {
        id: format!("m{seq}"),
        from,
        kind: MailKind::ToUser,
        status: AgentStatus::Running,
        no_reply: false,
        routine_id: None,
    };
    (info, message)
}

/// A report or a message waiting in a session's inbox. The text is final: rendered and cut once.
pub(crate) struct Mail {
    pub(crate) info: MailInfo,
    pub(crate) message: UserMessage,
    /// Starts a task when the session has none.
    pub(crate) wake: bool,
}

impl Mail {
    pub(crate) fn report(root: &Session, from: AgentRef, report: &Report, seq: u64) -> Mail {
        let body = cut(root, &format!("agent-{}-{seq}", from.name), body(report));
        let mut text = format!(
            "<agent_report agent=\"{}\" status=\"{}\">\n{}\n</agent_report>",
            from.name,
            report.status.as_str(),
            body.trim_end()
        );
        if report.status == AgentStatus::Interrupted {
            text.push('\n');
            text.push_str(RESUMABLE);
        }
        Mail {
            info: MailInfo {
                id: format!("m{seq}"),
                from,
                kind: MailKind::Report,
                status: report.status,
                no_reply: no_reply(report),
                routine_id: None,
            },
            message: UserMessage::text(text),
            // Work that was stopped waits until someone asks for more.
            wake: matches!(report.status, AgentStatus::Completed | AgentStatus::Failed),
        }
    }

    /// What a working sub-agent sends with `send_message` (agent-messaging-design §5.1). Cut like
    /// a report, and saved in the main session's data as its reports are.
    pub(crate) fn message(root: &Session, from: AgentRef, text: &str, seq: u64, wake: bool) -> Mail {
        let body = cut(root, &format!("agent-{}-{seq}", from.name), text.to_string());
        let text = format!("<agent_message from=\"{}\">\n{}\n</agent_message>", from.name, body.trim_end());
        Mail {
            info: MailInfo {
                id: format!("m{seq}"),
                from,
                kind: MailKind::Message,
                status: AgentStatus::Running,
                no_reply: false,
                routine_id: None,
            },
            message: UserMessage::text(text),
            wake,
        }
    }

    /// What a routine's run says to its bot. `from` is the run and the routine's name.
    pub(crate) fn routine_report(run: &RoutineRun, from: AgentRef, status: AgentStatus, body: &str, seq: u64) -> Mail {
        let failed = status == AgentStatus::Failed;
        let text = format!(
            "<routine_report routine=\"{}\" id=\"{}\"{} scheduled=\"{}\" ran=\"{}\">\n{}\n</routine_report>\n{}",
            run.name.replace('"', "'"),
            run.routine_id,
            if failed { " status=\"failed\"" } else { "" },
            run.scheduled,
            run.now,
            body.trim_end(),
            if failed { ROUTINE_FAILED } else { ROUTINE_REPORTED },
        );
        Mail {
            info: MailInfo {
                id: format!("m{seq}"),
                from,
                kind: MailKind::Routine,
                status,
                no_reply: false,
                routine_id: Some(run.routine_id.clone()),
            },
            message: UserMessage::text(text),
            wake: true,
        }
    }
}

/// What the report says: the agent's last reply, after the error if it failed. The error's kind
/// comes with it: it tells the main agent whether trying again can help.
pub(crate) fn body(report: &Report) -> String {
    let mut out = String::new();
    if let Some(error) = &report.error {
        out.push_str(&format!("Error ({}): {}\n\n", error.kind, error.message));
    }
    match &report.reply {
        Some(reply) => out.push_str(reply.trim()),
        None if no_reply(report) => out.push_str(match report.status {
            AgentStatus::Interrupted => "(It had not replied yet.)",
            _ => "(It finished without a reply.)",
        }),
        None => {}
    }
    if !report.unprocessed.is_empty() {
        out.push_str("\n\nMessages it had not processed:");
        for message in &report.unprocessed {
            out.push_str("\n- ");
            out.push_str(message.trim());
        }
    }
    out.trim().to_string()
}

/// The start of the report, for user interfaces; none when the agent had not replied, as the
/// report then only says so to the model.
pub(crate) fn summary(report: &Report) -> Option<String> {
    if no_reply(report) {
        return None;
    }
    let body = body(report);
    Some(match body.char_indices().nth(SUMMARY_CHARS) {
        Some((end, _)) => format!("{}…", &body[..end]),
        None => body,
    })
}

/// Whether the agent had neither replied nor failed: its report then is only a note saying so.
fn no_reply(report: &Report) -> bool {
    report.reply.is_none() && report.error.is_none()
}

/// The result of a call that waited for the agent.
pub(crate) fn waited(name: &str, report: &Report) -> String {
    let mut text = format!(
        "Agent \"{name}\" {} after {}.\n\n{}",
        report.status.as_str(),
        duration(report.elapsed),
        body(report)
    );
    if report.status == AgentStatus::Interrupted {
        text.push_str("\n\n");
        text.push_str(RESUMABLE);
    }
    text
}

/// Cuts a long report like a tool result: head and tail stay, the full text is saved as `label`.
pub(crate) fn cut(root: &Session, label: &str, body: String) -> String {
    let limits = root.tool_limits();
    let mut content = vec![UserContent::Text { text: body }];
    cap_result(
        &mut content,
        limits.max_tokens,
        Some(&root.output_dir),
        label,
        limits.max_saved_bytes,
    );
    content
        .into_iter()
        .find_map(|c| match c {
            UserContent::Text { text } => Some(text),
            UserContent::Image { .. } => None,
        })
        .unwrap_or_default()
}

pub(super) fn duration(elapsed: Duration) -> String {
    let secs = elapsed.as_secs();
    if secs < 60 {
        format!("{secs}s")
    } else {
        format!("{}m{:02}s", secs / 60, secs % 60)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_llm::UsageTotals;
    use her_protocol::ErrorInfo;

    fn report(status: AgentStatus, reply: Option<&str>) -> Report {
        Report {
            status,
            reply: reply.map(str::to_string),
            error: None,
            unprocessed: Vec::new(),
            usage: UsageTotals::default(),
            elapsed: Duration::from_secs(125),
        }
    }

    #[test]
    fn the_task_message_names_the_agent_and_carries_the_task() {
        let text = task_message("explore-api", "Find the {name} handlers.", false);
        assert!(text.starts_with("You are a sub-agent named \"explore-api\""));
        assert!(text.ends_with("<task>\nFind the {name} handlers.\n</task>"));
    }

    /// Only a bot's owner hears from its sub-agents on the way (agent-messaging-design §17).
    #[test]
    fn a_bots_sub_agent_is_told_to_keep_its_owner_posted() {
        let session = task_message("a", "Do it.", false);
        assert!(session.contains("Send no routine progress and no acknowledgements."));
        assert!(!session.contains("send_to_user"));
        let bot = task_message("a", "Do it.", true);
        assert!(bot.contains("call send_to_user with a brief update"));
        assert!(!bot.contains("Send no routine progress"));
        assert!(!bot.contains("{on_the_way}") && !session.contains("{on_the_way}"));
        assert!(bot.ends_with("<task>\nDo it.\n</task>"));
    }

    #[test]
    fn report_bodies() {
        assert_eq!(body(&report(AgentStatus::Completed, Some("  done\n"))), "done");
        assert_eq!(body(&report(AgentStatus::Completed, None)), "(It finished without a reply.)");
        let mut failed = report(AgentStatus::Failed, Some("half"));
        failed.error = Some(ErrorInfo::new("rate_limited", "rate limited (429)"));
        assert_eq!(body(&failed), "Error (rate_limited): rate limited (429)\n\nhalf");
        let mut stopped = report(AgentStatus::Interrupted, None);
        stopped.unprocessed = vec!["also check b".into()];
        assert_eq!(
            body(&stopped),
            "(It had not replied yet.)\n\nMessages it had not processed:\n- also check b"
        );
        assert_eq!(
            waited("a", &report(AgentStatus::Completed, Some("ok"))),
            "Agent \"a\" completed after 2m05s.\n\nok"
        );
        let long = report(AgentStatus::Completed, Some(&"x".repeat(500)));
        assert_eq!(summary(&long).unwrap().chars().count(), SUMMARY_CHARS + 1);
        // No reply: the note is for the model, so user interfaces get no summary.
        assert_eq!(summary(&report(AgentStatus::Interrupted, None)), None);
        assert_eq!(summary(&failed).as_deref(), Some("Error (rate_limited): rate limited (429)\n\nhalf"));
    }
}
