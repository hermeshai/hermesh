//! Routines (docs/kernel/routine-design.md): saved instructions that a bot runs on a schedule.
//! The service keeps and schedules them. Here is what a bot's session has of them: the tool that
//! changes them, the line that tells the model which there are, and what a run needs and comes
//! to. The run itself is in `agents/run.rs`.

use async_trait::async_trait;
use her_llm::UsageTotals;
use her_protocol::api::TranscriptItem;
use her_protocol::bot::RoutineOutcome;
use her_protocol::bot::RoutineState;
use her_tools::Environment;
use her_tools::Tool;
use her_tools::ToolCategory;
use her_tools::ToolCtx;
use her_tools::ToolDescription;
use her_tools::ToolError;
use her_tools::ToolKind;
use her_tools::ToolOutput;
use her_tools::Typed;
use her_tools::TypedTool;
use schemars::JsonSchema;
use serde::Deserialize;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;

use crate::history::History;
use crate::store::SessionRef;
use crate::store::SessionStore;

pub const MANAGE_ROUTINE: &str = "manage_routine";

/// How a run's reply says there is nothing to tell (routine-design §2.2).
const NOTHING_TO_REPORT: &str = "NOTHING_TO_REPORT";

/// What the context says of a bot that had routines and has none left.
pub(crate) const NO_ROUTINES: &str = "your routines: none";

/// The service's side of `manage_routine`. Errors are one sentence for the model.
#[async_trait]
pub trait RoutineHost: Send + Sync + 'static {
    async fn create(&self, bot: &str, new: NewRoutine) -> Result<RoutineSaved, String>;
    async fn get(&self, bot: &str, id: &str) -> Result<RoutineDetail, String>;
    async fn update(&self, bot: &str, id: &str, change: RoutineChange) -> Result<RoutineSaved, String>;
    async fn set_enabled(&self, bot: &str, id: &str, enabled: bool) -> Result<RoutineSaved, String>;
    async fn delete(&self, bot: &str, id: &str) -> Result<RoutineSaved, String>;
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct NewRoutine {
    pub name: String,
    pub prompt: String,
    pub schedule: String,
    pub when: String,
}

/// The fields to change.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct RoutineChange {
    pub name: Option<String>,
    pub prompt: Option<String>,
    pub schedule: Option<String>,
    pub when: Option<String>,
}

/// A routine as it was left.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RoutineSaved {
    pub id: String,
    pub name: String,
    pub when: String,
    /// RFC 3339, for clients. None when it will not run.
    pub next_run: Option<String>,
    /// The next runs in local time, for the model: `Thu Nov 5 00:00, 01:00, 02:00`. Empty when
    /// it will not run.
    pub upcoming: String,
}

/// What `get` tells the model.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RoutineDetail {
    pub saved: RoutineSaved,
    pub prompt: String,
    pub schedule: String,
    pub state: RoutineState,
    /// Newest first.
    pub recent: Vec<RecentRun>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RecentRun {
    /// In local time.
    pub at: String,
    /// `None` while it runs.
    pub outcome: Option<RoutineOutcome>,
    /// The start of what it said.
    pub text: Option<String>,
}

/// One line of what the model is told of a bot's routines.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RoutineLine {
    pub id: String,
    pub name: String,
    pub when: String,
    pub paused: bool,
}

/// What the model is told of a bot's routines: what `Kernel::set_routines` takes. Definitions
/// only, so that a run does not change it (routine-design §4.4).
pub fn listing(routines: &[RoutineLine]) -> String {
    if routines.is_empty() {
        return NO_ROUTINES.to_string();
    }
    let mut text = "your routines:".to_string();
    for routine in routines {
        text.push_str(&format!("\n- {} {}: {}", routine.id, routine.name, routine.when));
        if routine.paused {
            text.push_str(" (paused)");
        }
    }
    text
}

/// A run to carry out.
#[derive(Clone, Debug)]
pub struct RoutineRun {
    /// The run's id in the service's records.
    pub id: String,
    pub routine_id: String,
    pub name: String,
    pub prompt: String,
    /// When it was due and when it starts, in local time as the model reads it.
    pub scheduled: String,
    pub now: String,
    /// It starts well after it was due.
    pub late: bool,
    /// A failure goes to the bot: the first of a streak does (routine-design D-423).
    pub report_failure: bool,
}

/// What a run came to.
#[derive(Clone, Debug, PartialEq)]
pub struct RunOutcome {
    pub outcome: RoutineOutcome,
    /// What a quiet run says it checked.
    pub note: Option<String>,
    /// What it reported, or the error it failed with; as long as a tool result may be.
    pub report: Option<String>,
    /// Its log; none when it failed before its session started.
    pub log: Option<PathBuf>,
    pub usage: UsageTotals,
}

/// Whether a run's reply says there is nothing to tell, and what it says it checked. Only at the
/// start, as a report may well mention the phrase later on, and only the phrase itself: a real
/// report opened with "NOTHING_TO_REPORT? No, …". What is not plainly quiet goes to the bot.
pub(crate) fn quiet_note(reply: &str) -> Option<String> {
    let rest = reply.trim().strip_prefix(NOTHING_TO_REPORT)?;
    let note = rest.trim_start_matches(|c: char| c.is_whitespace() || ":：-–—.。,，;；".contains(c));
    if note.len() == rest.len() && !rest.is_empty() && !rest.starts_with(['(', '（']) {
        return None;
    }
    Some(note.trim().to_string())
}

/// A run's steps as clients show them, from its log: empty when the log is gone. The brief it
/// started from is left out: it is ours, not what the run did.
pub fn run_transcript(store: &dyn SessionStore, log: &Path) -> Vec<TranscriptItem> {
    let Ok((_, entries)) = store.read(&SessionRef::Path(log.to_path_buf())) else {
        return Vec::new();
    };
    let history = History::from_entries(entries);
    let mut items = crate::kernel::transcript_of(history.branch());
    if matches!(items.first(), Some(TranscriptItem::Message { .. })) {
        items.remove(0);
    }
    items
}

/// How a bot's session reaches its routines.
pub(crate) struct RoutineLink {
    pub(crate) host: Arc<dyn RoutineHost>,
    pub(crate) bot: String,
}

pub(crate) fn tool() -> Arc<dyn Tool> {
    Arc::new(Typed::new(ManageRoutine))
}

/// What an approval asks about: the call in words, not its JSON.
pub(crate) fn approval_summary(args: &Value) -> String {
    let text = |key: &str| args.get(key).and_then(Value::as_str).map(str::trim).filter(|s| !s.is_empty());
    let action = text("action").unwrap_or("change");
    match (text("name").or(text("id")), text("when")) {
        (Some(who), Some(when)) => format!("{action} routine \"{who}\": {when}"),
        (Some(who), None) => format!("{action} routine \"{who}\""),
        (None, _) => format!("{action} a routine"),
    }
}

#[derive(Clone, Copy, Debug, Deserialize, JsonSchema, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
enum Action {
    Create,
    Get,
    Update,
    Pause,
    Resume,
    Delete,
}

impl Action {
    fn as_str(self) -> &'static str {
        match self {
            Action::Create => "create",
            Action::Get => "get",
            Action::Update => "update",
            Action::Pause => "pause",
            Action::Resume => "resume",
            Action::Delete => "delete",
        }
    }
}

#[derive(Debug, Deserialize, JsonSchema)]
struct ManageArgs {
    /// What to do.
    action: Action,
    /// The routine's id. Required except on create.
    #[serde(default)]
    id: Option<String>,
    /// A short name the user sees, in their language. Required on create.
    #[serde(default)]
    name: Option<String>,
    /// What to do on each run, in the user's language, written as an intent rather than fixed tool steps. A run cannot see this conversation, message the user or change routines: its report comes back to you, and you do those. Say what to check, what is worth reporting, when to report nothing, what to do if the check fails, when the job is finished, and where to keep anything the next run needs.
    #[serde(default)]
    prompt: Option<String>,
    /// 5-field cron in the user's local time, using the time they said ("8am" is "0 8 * * *"). Prefix CRON_TZ=<IANA zone> when the time belongs elsewhere, such as a meeting in another city. Or one moment, which runs once and finishes by itself: "in 20m" or "2026-11-10 09:00". Work that must be ready by a time starts early enough to finish.
    #[serde(default)]
    schedule: Option<String>,
    /// The schedule as the user would say it, in their language. The user sees this. Required on create and whenever schedule changes.
    #[serde(default)]
    when: Option<String>,
}

/// A field as kept: trimmed, and absent when empty.
fn given(field: Option<String>) -> Option<String> {
    field.map(|text| text.trim().to_string()).filter(|text| !text.is_empty())
}

fn required(field: Option<String>, name: &str, action: Action) -> Result<String, ToolError> {
    given(field).ok_or_else(|| ToolError::InvalidArgs(format!("{name} is required on {}", action.as_str())))
}

/// `Saved routine "…" (id r_7k3m2p). Next runs in local time: Thu Nov 5 00:00, 01:00.`
fn saved_line(saved: &RoutineSaved) -> String {
    let start = format!("Saved routine \"{}\" (id {}).", saved.name, saved.id);
    if saved.upcoming.is_empty() {
        format!("{start} It will not run until it is resumed.")
    } else {
        format!("{start} Next runs in local time: {}.", saved.upcoming)
    }
}

/// What clients show in the chat for a change (routine-design §4.3).
fn details(action: Action, saved: &RoutineSaved) -> Value {
    json!({
        "action": action.as_str(),
        "routine_id": saved.id,
        "name": saved.name,
        "when": saved.when,
        "next_run": saved.next_run,
    })
}

fn outcome_word(outcome: Option<RoutineOutcome>) -> &'static str {
    match outcome {
        None => "running",
        Some(RoutineOutcome::Reported) => "reported",
        Some(RoutineOutcome::Quiet) => "nothing to report",
        Some(RoutineOutcome::Failed) => "failed",
        Some(RoutineOutcome::Interrupted) => "interrupted",
        Some(RoutineOutcome::Skipped) => "skipped",
    }
}

fn detail_text(detail: &RoutineDetail) -> String {
    let saved = &detail.saved;
    let state = match detail.state {
        RoutineState::Active => "active",
        RoutineState::Paused => "paused",
        RoutineState::Finished => "finished",
    };
    let mut text = format!(
        "Routine \"{}\" (id {}), {state}.\nWhen: {}\nSchedule: {}\n",
        saved.name, saved.id, saved.when, detail.schedule
    );
    if !saved.upcoming.is_empty() {
        text.push_str(&format!("Next runs in local time: {}.\n", saved.upcoming));
    }
    text.push_str(&format!("\n<instructions>\n{}\n</instructions>\n", detail.prompt.trim()));
    if detail.recent.is_empty() {
        text.push_str("\nIt has not run yet.");
    } else {
        text.push_str("\nRecent runs, newest first:");
        for run in &detail.recent {
            text.push_str(&format!("\n- {}: {}", run.at, outcome_word(run.outcome)));
            if let Some(said) = run.text.as_deref().map(str::trim).filter(|said| !said.is_empty()) {
                text.push_str(&format!(": {said}"));
            }
        }
    }
    text
}

struct ManageRoutine;

#[async_trait]
impl TypedTool for ManageRoutine {
    type Args = ManageArgs;
    const NAME: &'static str = MANAGE_ROUTINE;
    const DESCRIPTION: &'static str = "Read, create or change your routines: saved instructions that run in the background on a \
        schedule, even while the user is away. Each run reports back to you, and you pass on what the user asked for.\n\n\
        When the user asks for something recurring, at a later time, or \"tell me when X\", create a routine instead of doing it \
        once, waiting, or using crontab or launchd, which cannot reach this chat. If it would only be useful and they did not ask, \
        offer it in one sentence and wait for their yes.\n\n\
        Your routines are in the latest <environment_update>, without their instructions; with none listed, you have none.\n\
        - create: name, prompt, schedule, when. get: id; returns the saved text and recent runs.\n  \
        update: id and only the fields to change. pause, resume, delete: id.\n\n\
        Before you change a routine or describe what it does, get it and work from the saved text. When the user changes how they \
        want it done, update it: runs do not see this conversation. Scheduled checks poll: never promise to notice something the \
        moment it happens.\n\n\
        Routines run only while this computer is awake. Tell the user the next run time from the result, and when it falls at \
        night, that the computer has to be awake then.";

    fn kind_for(&self, args: &Value) -> ToolKind {
        match args.get("action").and_then(Value::as_str) {
            Some("get") => ToolKind::ReadOnly,
            _ => ToolKind::Mutating,
        }
    }

    fn describe(&self, args: &Value, _env: &dyn Environment) -> ToolDescription {
        ToolDescription {
            category: ToolCategory::Other,
            title: approval_summary(args),
            locations: Vec::new(),
        }
    }

    async fn run(&self, args: ManageArgs, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        let link = ctx
            .session_state
            .get::<RoutineLink>()
            .ok_or_else(|| ToolError::Failed("routines are not available in this session".into()))?;
        let (host, bot) = (&link.host, link.bot.as_str());
        let action = args.action;
        if action == Action::Create {
            let new = NewRoutine {
                name: required(args.name, "name", action)?,
                prompt: required(args.prompt, "prompt", action)?,
                schedule: required(args.schedule, "schedule", action)?,
                when: required(args.when, "when", action)?,
            };
            let saved = host.create(bot, new).await.map_err(ToolError::Failed)?;
            return Ok(ToolOutput::text(saved_line(&saved)).with_details(details(action, &saved)));
        }
        let id = required(args.id, "id", action)?;
        match action {
            Action::Create => unreachable!("handled above"),
            Action::Get => {
                let detail = host.get(bot, &id).await.map_err(ToolError::Failed)?;
                Ok(ToolOutput::text(detail_text(&detail)))
            }
            Action::Update => {
                let change = RoutineChange {
                    name: given(args.name),
                    prompt: given(args.prompt),
                    schedule: given(args.schedule),
                    when: given(args.when),
                };
                if change == RoutineChange::default() {
                    return Err(ToolError::InvalidArgs("update needs at least one of name, prompt, schedule, when".into()));
                }
                if change.schedule.is_some() && change.when.is_none() {
                    return Err(ToolError::InvalidArgs("when is required whenever schedule changes".into()));
                }
                let saved = host.update(bot, &id, change).await.map_err(ToolError::Failed)?;
                Ok(ToolOutput::text(saved_line(&saved)).with_details(details(action, &saved)))
            }
            Action::Pause => {
                let saved = host.set_enabled(bot, &id, false).await.map_err(ToolError::Failed)?;
                Ok(ToolOutput::text("Paused.").with_details(details(action, &saved)))
            }
            Action::Resume => {
                let saved = host.set_enabled(bot, &id, true).await.map_err(ToolError::Failed)?;
                Ok(ToolOutput::text(saved_line(&saved)).with_details(details(action, &saved)))
            }
            Action::Delete => {
                let saved = host.delete(bot, &id).await.map_err(ToolError::Failed)?;
                Ok(ToolOutput::text("Deleted.").with_details(details(action, &saved)))
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_reply_is_quiet_only_when_it_starts_with_the_phrase() {
        assert_eq!(quiet_note("NOTHING_TO_REPORT").as_deref(), Some(""));
        assert_eq!(quiet_note("  NOTHING_TO_REPORT: price is 412, above 300\n").as_deref(), Some("price is 412, above 300"));
        assert_eq!(quiet_note("NOTHING_TO_REPORT 价格 412").as_deref(), Some("价格 412"));
        assert_eq!(quiet_note("NOTHING_TO_REPORT — 没有变化。").as_deref(), Some("没有变化。"));
        assert_eq!(quiet_note("NOTHING_TO_REPORT\n无 ALERT").as_deref(), Some("无 ALERT"));
        assert_eq!(quiet_note("NOTHING_TO_REPORT（无 ALERT）").as_deref(), Some("（无 ALERT）"));
        assert_eq!(quiet_note("It fell to 290. Earlier checks said NOTHING_TO_REPORT."), None);
        assert_eq!(quiet_note(""), None);
    }

    /// From the real thing: a run that found an alert opened like this, and the alert was lost.
    #[test]
    fn a_reply_that_questions_the_phrase_is_a_report() {
        assert_eq!(quiet_note("NOTHING_TO_REPORT? No — there is something to report.\n\nALERT 磁盘只剩 3%"), None);
        assert_eq!(quiet_note("NOTHING_TO_REPORT？不，有告警"), None);
        assert_eq!(quiet_note("NOTHING_TO_REPORTS"), None);
        assert_eq!(quiet_note("NOTHING_TO_REPORT_X"), None);
    }

    #[test]
    fn the_list_has_definitions_only() {
        let line = |id: &str, name: &str, when: &str, paused| RoutineLine { id: id.into(), name: name.into(), when: when.into(), paused };
        assert_eq!(listing(&[]), "your routines: none");
        assert_eq!(
            listing(&[line("r_7k3m2p", "提醒交报告", "11 月 10 日早上 9 点", false), line("r_8a2c1d", "跌破 300 提醒", "工作日每小时", true)]),
            "your routines:\n- r_7k3m2p 提醒交报告: 11 月 10 日早上 9 点\n- r_8a2c1d 跌破 300 提醒: 工作日每小时 (paused)"
        );
    }

    #[test]
    fn the_saved_line_gives_the_next_runs() {
        let mut saved = RoutineSaved {
            id: "r_7k3m2p".into(),
            name: "喝水".into(),
            when: "每小时".into(),
            next_run: Some("2026-11-05T00:00:00+08:00".into()),
            upcoming: "Thu Nov 5 00:00, 01:00, 02:00".into(),
        };
        assert_eq!(saved_line(&saved), "Saved routine \"喝水\" (id r_7k3m2p). Next runs in local time: Thu Nov 5 00:00, 01:00, 02:00.");
        saved.upcoming.clear();
        assert_eq!(saved_line(&saved), "Saved routine \"喝水\" (id r_7k3m2p). It will not run until it is resumed.");
    }

    #[test]
    fn an_approval_says_what_the_call_does() {
        let summary = |args: Value| approval_summary(&args);
        assert_eq!(summary(json!({"action": "create", "name": "喝水", "when": "每小时"})), "create routine \"喝水\": 每小时");
        assert_eq!(summary(json!({"action": "delete", "id": "r_7k3m2p"})), "delete routine \"r_7k3m2p\"");
        assert_eq!(summary(json!({"action": "pause"})), "pause a routine");
    }

    #[test]
    fn only_reading_is_read_only() {
        let tool = ManageRoutine;
        assert_eq!(tool.kind_for(&json!({"action": "get", "id": "r_1"})), ToolKind::ReadOnly);
        for action in ["create", "update", "pause", "resume", "delete"] {
            assert_eq!(tool.kind_for(&json!({"action": action})), ToolKind::Mutating, "{action}");
        }
        // Arguments that make no sense count as a change until they fail.
        assert_eq!(tool.kind_for(&json!({})), ToolKind::Mutating);
    }

    #[test]
    fn get_shows_the_saved_text_and_what_the_runs_did() {
        let detail = RoutineDetail {
            saved: RoutineSaved {
                id: "r_1".into(),
                name: "跌破 300".into(),
                when: "工作日每小时".into(),
                next_run: None,
                upcoming: "Thu Nov 5 10:00".into(),
            },
            prompt: "Check the price.\n".into(),
            schedule: "0 10-16 * * 1-5".into(),
            state: RoutineState::Active,
            recent: vec![
                RecentRun { at: "Wed Nov 4 16:00".into(), outcome: Some(RoutineOutcome::Quiet), text: Some("412".into()) },
                RecentRun { at: "Wed Nov 4 15:00".into(), outcome: Some(RoutineOutcome::Skipped), text: None },
            ],
        };
        assert_eq!(
            detail_text(&detail),
            "Routine \"跌破 300\" (id r_1), active.\nWhen: 工作日每小时\nSchedule: 0 10-16 * * 1-5\nNext runs in local time: Thu Nov 5 10:00.\n\n\
             <instructions>\nCheck the price.\n</instructions>\n\n\
             Recent runs, newest first:\n- Wed Nov 4 16:00: nothing to report: 412\n- Wed Nov 4 15:00: skipped"
        );
    }
}
