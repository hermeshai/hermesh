//! A bot's routines in the kernel (routine-design §2, §4, §6): scripted model, real tools, real
//! session logs. The schedule and the records are the service's.

use async_trait::async_trait;
use her_core::BotIdentity;
use her_core::Config;
use her_core::HerHome;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::KernelDeps;
use her_core::SessionRef;
use her_core::StartOptions;
use her_core::routine::NewRoutine;
use her_core::routine::RoutineChange;
use her_core::routine::RoutineDetail;
use her_core::routine::RoutineHost;
use her_core::routine::RoutineLine;
use her_core::routine::RoutineRun;
use her_core::routine::RoutineSaved;
use her_core::routine::RunOutcome;
use her_core::routine::listing;
use her_core::store::Entry;
use her_core::store::EntryKind;
use her_core::store::SessionStore;
use her_llm::Catalog;
use her_llm::Context;
use her_llm::ErrorKind;
use her_llm::Message;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use her_llm::ThinkingLevel;
use her_llm::UserContent;
use her_llm::api::scripted::Script;
use her_llm::api::scripted::ScriptedApi;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::MailKind;
use her_protocol::Op;
use her_protocol::PermissionMode;
use her_protocol::bot::RoutineOutcome;
use her_protocol::bot::RoutineState;
use her_tools::BashTool;
use her_tools::LocalEnvironment;
use her_tools::builtin_registry;
use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use tempfile::TempDir;
use tokio::sync::oneshot;

struct Harness {
    kernel: Kernel,
    api: Arc<ScriptedApi>,
    home: HerHome,
    _dir: TempDir,
}

fn deps(api: &Arc<ScriptedApi>, work: &Path, home: &HerHome, host: Option<Arc<dyn RoutineHost>>) -> KernelDeps {
    let mut registry = ModelRegistry::new(Catalog::default(), RegistryOptions::default());
    registry.register_api(api.clone());
    registry.insert_provider(ScriptedApi::provider());
    KernelDeps {
        llm: Arc::new(registry),
        tools: Arc::new(builtin_registry(BashTool::default())),
        env: Arc::new(LocalEnvironment::new(work.to_path_buf())),
        store: Arc::new(JsonlStore::new(home.sessions_dir())),
        hooks: vec![],
        mcp_servers: Vec::new(),
        browser_pool: None,
        docs: None,
        decks: None,
        words: None,
        routines: host,
        connectors: None,
    }
}

async fn bot_with(mode: PermissionMode, interactive: bool, scripts: Vec<Script>, host: Option<Arc<dyn RoutineHost>>) -> Harness {
    let dir = tempfile::tempdir().unwrap();
    let home = HerHome::new(dir.path().canonicalize().unwrap().join("home"));
    home.ensure().unwrap();
    let folder = home.bot_files_dir("b1");
    std::fs::create_dir_all(&folder).unwrap();
    let folder = folder.canonicalize().unwrap();
    let api = ScriptedApi::new();
    for script in scripts {
        api.push(script);
    }
    let options = StartOptions {
        model: "test/scripted".into(),
        thinking: ThinkingLevel::Off,
        permission_mode: mode,
        cwd: folder.clone(),
        home: home.clone(),
        config: Arc::new(Config::default()),
        project_root: None,
        trusted: false,
        interactive_approvals: interactive,
        bot: Some(BotIdentity {
            id: "b1".into(),
            name: "研究员".into(),
        }),
    };
    let kernel = Kernel::start(options, deps(&api, &folder, &home, host)).await.unwrap();
    Harness { kernel, api, home, _dir: dir }
}

async fn bot(scripts: Vec<Script>) -> Harness {
    bot_with(PermissionMode::BypassPermissions, true, scripts, None).await
}

fn a_run() -> RoutineRun {
    RoutineRun {
        id: "run_1".into(),
        routine_id: "r_1".into(),
        name: "跌破 300".into(),
        prompt: "Check the price. Tell the user below 300.".into(),
        scheduled: "Thu 2026-11-05 09:00".into(),
        now: "Thu 2026-11-05 09:00".into(),
        late: false,
        report_failure: true,
    }
}

fn send_last(id: &str, text: &str) -> Script {
    Script::tool_call(id, "send_to_user", json!({ "text": text, "end_turn": true }))
}

fn prompt(text: &str) -> Vec<UserContent> {
    vec![UserContent::Text { text: text.into() }]
}

/// Collects events until `stop` matches (inclusive). Fails after 10 seconds.
async fn until(kernel: &mut Kernel, stop: impl Fn(&EventMsg) -> bool) -> Vec<Event> {
    let mut events = Vec::new();
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        let remaining = deadline.saturating_duration_since(Instant::now());
        let event = tokio::time::timeout(remaining, kernel.next_event())
            .await
            .expect("timed out waiting for an event")
            .expect("session closed");
        let done = stop(&event.msg);
        events.push(event);
        if done {
            return events;
        }
    }
}

/// The events that are there already, and those that come within a moment.
async fn drain(kernel: &mut Kernel) -> Vec<Event> {
    let mut events = Vec::new();
    while let Ok(Some(event)) = tokio::time::timeout(Duration::from_millis(100), kernel.next_event()).await {
        events.push(event);
    }
    events
}

async fn outcome(rx: oneshot::Receiver<RunOutcome>) -> RunOutcome {
    tokio::time::timeout(Duration::from_secs(10), rx).await.expect("timed out waiting for the run").expect("the run went away")
}

fn sent(events: &[Event]) -> Vec<String> {
    events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::ToolStarted { name, args, .. } if name == "send_to_user" => args["text"].as_str().map(str::to_string),
            _ => None,
        })
        .collect()
}

fn texts(message: &Message) -> String {
    match message {
        Message::User(user) => user
            .content
            .iter()
            .filter_map(|c| match c {
                UserContent::Text { text } => Some(text.as_str()),
                _ => None,
            })
            .collect::<Vec<_>>()
            .join("\n"),
        _ => String::new(),
    }
}

fn tool_names(request: &Context) -> Vec<String> {
    request.tools.iter().map(|t| t.name.clone()).collect()
}

fn bot_entries(h: &Harness) -> Vec<Entry> {
    let store = JsonlStore::new(h.home.sessions_dir());
    store.read(&SessionRef::Id(h.kernel.session_id().to_string())).unwrap().1
}

/// A run sees the saved instructions, not the chat; it has a session's prompt and neither of the
/// bot's tools, nor sub-agents (routine-design §2.1).
#[tokio::test]
async fn a_run_sees_no_chat_and_cannot_message_or_change_routines() {
    let mut h = bot(vec![send_last("c1", "好的。"), Script::text("NOTHING_TO_REPORT 412")]).await;
    h.kernel.run(prompt("暗号是天王盖地虎")).await.unwrap();
    outcome(h.kernel.start_run(a_run()).unwrap()).await;

    let requests = h.api.requests();
    let run = &requests[1];
    assert_eq!(run.messages.len(), 1, "only the brief");
    let brief = texts(&run.messages[0]);
    assert!(brief.starts_with("You are carrying out a routine for 研究员, the user's bot"), "{brief}");
    assert!(brief.contains("Routine: \"跌破 300\". Scheduled for Thu 2026-11-05 09:00; it is now Thu 2026-11-05 09:00, local time.\n"), "{brief}");
    assert!(brief.ends_with("<instructions>\nCheck the price. Tell the user below 300.\n</instructions>"), "{brief}");
    assert!(!brief.contains("天王盖地虎"));
    assert!(run.system.starts_with("You are Her"), "{}", run.system);
    let names = tool_names(run);
    for name in ["send_to_user", "manage_routine", "spawn_agent", "send_message"] {
        assert!(!names.iter().any(|n| n == name), "{name} in {names:?}");
    }
    // The bot has them.
    assert!(!run.system.contains("- agents:"), "it is on its own: {}", run.system);
    let names = tool_names(&requests[0]);
    assert!(names.iter().any(|n| n == "manage_routine") && names.iter().any(|n| n == "send_to_user"), "{names:?}");
}

/// Nothing to tell: the bot's conversation does not move, no event shows it, and nothing is paid
/// for reading the conversation (routine-design RG3).
#[tokio::test]
async fn a_quiet_run_leaves_the_bot_alone() {
    let mut h = bot(vec![Script::text("  NOTHING_TO_REPORT: 价格 412，没到 300。\n")]).await;
    drain(&mut h.kernel).await;
    let ended = outcome(h.kernel.start_run(a_run()).unwrap()).await;
    assert_eq!(ended.outcome, RoutineOutcome::Quiet);
    assert_eq!(ended.note.as_deref(), Some("价格 412，没到 300。"));
    assert_eq!(ended.report, None);
    assert!(ended.log.as_ref().is_some_and(|log| log.is_file()));
    assert_eq!(h.api.requests().len(), 1);
    let events = drain(&mut h.kernel).await;
    assert!(events.is_empty(), "{:?}", events.iter().map(|e| &e.msg).collect::<Vec<_>>());
    assert!(h.kernel.transcript().is_empty());
    // Nor does its log: only what any new session writes.
    let id = h.kernel.session_id().to_string();
    h.kernel.shutdown().await;
    let store = JsonlStore::new(h.home.sessions_dir());
    let (_, entries) = store.read(&SessionRef::Id(id)).unwrap();
    assert!(entries.iter().all(|e| matches!(e.kind, EntryKind::SystemPrompt(_) | EntryKind::Settings { .. })), "{entries:?}");
}

/// Something to tell: one letter wakes the bot, which writes the bubble itself (routine-design §2.3).
#[tokio::test]
async fn a_report_wakes_the_bot_and_it_tells_the_user() {
    let mut h = bot(vec![Script::text("跌到 290 了。"), send_last("c1", "特斯拉跌到 290 了。")]).await;
    let rx = h.kernel.start_run(a_run()).unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert_eq!(sent(&events), ["特斯拉跌到 290 了。"]);
    let ended = outcome(rx).await;
    assert_eq!(ended.outcome, RoutineOutcome::Reported);
    assert_eq!(ended.report.as_deref(), Some("跌到 290 了。"));

    let requests = h.api.requests();
    assert_eq!(requests.len(), 2);
    assert!(requests[1].system.starts_with("You are 研究员"), "{}", requests[1].system);
    assert_eq!(
        texts(requests[1].messages.last().unwrap()),
        "<routine_report routine=\"跌破 300\" id=\"r_1\" scheduled=\"Thu 2026-11-05 09:00\" ran=\"Thu 2026-11-05 09:00\">\n\
         跌到 290 了。\n</routine_report>\n\
         This routine is the user's own request: pass on what it found with send_to_user, in your own words and without \
         talking about runs or reports. If it holds nothing they asked to hear, send nothing and write only \
         NOTHING_TO_REPORT. If the job is finished, delete the routine."
    );
    // Clients know where it came from; no agent ever showed.
    let mail = events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::MessageCompleted { mail: Some(mail), .. } => Some(mail.clone()),
            _ => None,
        })
        .expect("the letter");
    assert_eq!(mail.kind, MailKind::Routine);
    assert_eq!(mail.routine_id.as_deref(), Some("r_1"));
    assert_eq!((mail.from.id.as_str(), mail.from.name.as_str()), ("run_1", "跌破 300"));
    assert!(!events.iter().any(|e| matches!(e.msg, EventMsg::AgentStarted { .. } | EventMsg::AgentEvent { .. } | EventMsg::AgentStatus { .. })));
    // The log has the letter, and no agent for a later load to read.
    let entries = bot_entries(&h);
    assert_eq!(entries.iter().filter(|e| matches!(e.kind, EntryKind::Mail { .. })).count(), 1);
    assert!(!entries.iter().any(|e| matches!(e.kind, EntryKind::AgentSpawned(_))));
}

/// Forgetting the phrase costs one wake, and no text is still something to tell.
#[tokio::test]
async fn a_run_without_a_reply_says_so() {
    let mut h = bot(vec![Script::text(""), send_last("c1", "那次检查没有结果。")]).await;
    let rx = h.kernel.start_run(a_run()).unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    let ended = outcome(rx).await;
    assert_eq!(ended.outcome, RoutineOutcome::Reported);
    assert_eq!(ended.report.as_deref(), Some("(It finished without a reply.)"));
}

/// The phrase counts only at the start.
#[tokio::test]
async fn a_report_may_mention_the_phrase() {
    let mut h = bot(vec![Script::text("It fell. Yesterday's check said NOTHING_TO_REPORT."), send_last("c1", "跌了。")]).await;
    let rx = h.kernel.start_run(a_run()).unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert_eq!(outcome(rx).await.outcome, RoutineOutcome::Reported);
}

/// The first failure of a streak goes to the bot, with its kind; the next ones are only kept
/// (routine-design D-423).
#[tokio::test]
async fn only_the_first_failure_of_a_streak_reaches_the_bot() {
    let mut h = bot(vec![
        Script::fail(ErrorKind::QuotaExceeded, "quota exceeded"),
        send_last("c1", "定时任务失败了：额度用完了。"),
        Script::fail(ErrorKind::QuotaExceeded, "quota exceeded"),
    ])
    .await;
    let rx = h.kernel.start_run(a_run()).unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert_eq!(sent(&events), ["定时任务失败了：额度用完了。"]);
    let ended = outcome(rx).await;
    assert_eq!(ended.outcome, RoutineOutcome::Failed);
    let report = ended.report.unwrap();
    assert!(report.starts_with("Error (") && report.contains("quota exceeded"), "{report}");
    let requests = h.api.requests();
    let letter = texts(requests[1].messages.last().unwrap());
    assert!(letter.starts_with("<routine_report routine=\"跌破 300\" id=\"r_1\" status=\"failed\" scheduled="), "{letter}");
    assert!(letter.ends_with("You will not hear about later failures until it works again."), "{letter}");

    let run = RoutineRun { report_failure: false, ..a_run() };
    let ended = outcome(h.kernel.start_run(run).unwrap()).await;
    assert_eq!(ended.outcome, RoutineOutcome::Failed);
    assert!(ended.report.is_some(), "kept for the panel");
    assert_eq!(h.api.requests().len(), 3, "the bot was not woken");
    assert!(drain(&mut h.kernel).await.is_empty());
}

/// Stopped from the panel: kept as interrupted, and nothing goes to the bot.
#[tokio::test]
async fn a_stopped_run_says_nothing() {
    let mut h = bot(vec![Script::text("Looking…").then_sleep(Duration::from_secs(30))]).await;
    let rx = h.kernel.start_run(a_run()).unwrap();
    let deadline = Instant::now() + Duration::from_secs(5);
    while h.api.requests().is_empty() {
        assert!(Instant::now() < deadline, "the run never asked the model");
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    assert!(h.kernel.stop_run("run_1"));
    let ended = outcome(rx).await;
    assert_eq!(ended.outcome, RoutineOutcome::Interrupted);
    assert_eq!(ended.report, None);
    assert!(!h.kernel.stop_run("run_1"), "it is over");
    assert!(!drain(&mut h.kernel).await.iter().any(|e| matches!(e.msg, EventMsg::MessageCompleted { .. } | EventMsg::TaskStarted { .. })));
}

/// The bot's session closes: its run stops with it, and whoever waits hears how it went.
#[tokio::test]
async fn closing_the_session_stops_a_run() {
    let h = bot(vec![Script::text("Looking…").then_sleep(Duration::from_secs(30))]).await;
    let rx = h.kernel.start_run(a_run()).unwrap();
    let deadline = Instant::now() + Duration::from_secs(5);
    while h.api.requests().is_empty() {
        assert!(Instant::now() < deadline, "the run never asked the model");
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    h.kernel.shutdown().await;
    assert_eq!(outcome(rx).await.outcome, RoutineOutcome::Interrupted);
}

/// What needs approval is refused at once: nobody is there, and no window opens (routine-design §10).
#[tokio::test]
async fn a_run_is_refused_what_needs_approval() {
    let mut h = bot_with(
        PermissionMode::Manual,
        true,
        vec![
            Script::tool_call("c1", "bash", json!({ "command": "touch made-by-run" })),
            Script::text("NOTHING_TO_REPORT 没有写成：需要批准。"),
        ],
        None,
    )
    .await;
    let ended = outcome(h.kernel.start_run(a_run()).unwrap()).await;
    assert_eq!(ended.outcome, RoutineOutcome::Quiet);
    let requests = h.api.requests();
    let Some(Message::ToolResult(result)) = requests[1].messages.last() else {
        panic!("the refused call's result")
    };
    assert!(result.text_content().contains("nobody can approve it"), "{}", result.text_content());
    assert!(!h.home.bot_files_dir("b1").join("made-by-run").exists());
    assert!(!drain(&mut h.kernel).await.iter().any(|e| matches!(e.msg, EventMsg::ApprovalRequested { .. } | EventMsg::AgentEvent { .. })));
}

/// A late run is told so.
#[tokio::test]
async fn a_late_run_is_told_it_is_late() {
    let h = bot(vec![Script::text("NOTHING_TO_REPORT")]).await;
    let run = RoutineRun {
        now: "Thu 2026-11-05 23:00".into(),
        late: true,
        // The user's words stay as they are.
        prompt: "Keep {name} and {now} as written.".into(),
        ..a_run()
    };
    outcome(h.kernel.start_run(run).unwrap()).await;
    let requests = h.api.requests();
    let brief = texts(&requests[0].messages[0]);
    assert!(brief.contains("it is now Thu 2026-11-05 23:00, local time. This run is late because"), "{brief}");
    assert!(brief.contains("<instructions>\nKeep {name} and {now} as written.\n</instructions>"), "{brief}");
}

/// A run keeps its own log, out of every list, naming its routine.
#[tokio::test]
async fn a_runs_log_names_its_routine_and_stays_out_of_lists() {
    let h = bot(vec![Script::text("NOTHING_TO_REPORT")]).await;
    let ended = outcome(h.kernel.start_run(a_run()).unwrap()).await;
    let store = JsonlStore::new(h.home.sessions_dir());
    let (header, entries) = store.read(&SessionRef::Path(ended.log.unwrap())).unwrap();
    let agent = header.agent.expect("a sub-agent's header");
    assert_eq!(agent.routine.as_deref(), Some("r_1"));
    assert_eq!(agent.root_id, h.kernel.session_id());
    assert!(store.list(10, None).unwrap().is_empty());
    assert!(entries.iter().any(|e| matches!(&e.kind, EntryKind::Task { .. })));
}

const ROUTINE_REMINDER: &str = "<reply_reminder>\nThe routine report above has not reached the user";

fn routine_reminders(h: &Harness) -> usize {
    bot_entries(h).iter().filter(|e| matches!(e.kind, EntryKind::Unanswered { routine: true })).count()
}

/// A bot that only writes privately after a report is reminded once, in other words than for a
/// message; the report does not vanish (routine-design D-414).
#[tokio::test]
async fn a_bot_is_reminded_to_pass_a_report_on() {
    let mut h = bot(vec![Script::text("跌到 290。"), Script::text("Passed on."), send_last("c1", "跌到 290 了。")]).await;
    let rx = h.kernel.start_run(a_run()).unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    outcome(rx).await;
    assert_eq!(sent(&events), ["跌到 290 了。"]);
    let requests = h.api.requests();
    assert_eq!(requests.len(), 3);
    let last = texts(requests[2].messages.last().unwrap());
    assert!(last.starts_with(ROUTINE_REMINDER), "{last}");
    assert_eq!(routine_reminders(&h), 1);
}

/// Reminded and still silent: the task fails, so the owner sees a card and an alert, and a retry
/// asks again.
#[tokio::test]
async fn a_bot_silent_after_the_reminder_fails_the_task() {
    let mut h = bot(vec![Script::text("跌到 290。"), Script::text("嗯。"), Script::text("嗯嗯。"), send_last("c1", "跌到 290 了。")]).await;
    let rx = h.kernel.start_run(a_run()).unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskFailed { .. })).await;
    outcome(rx).await;
    let failed = events.iter().find_map(|e| match &e.msg {
        EventMsg::TaskFailed { error, .. } => Some(error.clone()),
        _ => None,
    });
    assert_eq!(failed.map(|e| (e.kind, e.retryable)), Some(("no_reply".to_string(), true)));
    assert!(sent(&events).is_empty());
    // A retry asks again in the same words.
    h.kernel.submit(Op::Continue).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert_eq!(sent(&events), ["跌到 290 了。"]);
    assert_eq!(routine_reminders(&h), 2);
}

/// A report may hold nothing the owner asked to hear: the bot says so, and that is no failure,
/// whether it says so at once or when reminded (routine-design D-441).
#[tokio::test]
async fn a_bot_may_find_nothing_to_tell_in_a_report() {
    for (bot_replies, reminders) in [(vec!["NOTHING_TO_REPORT"], 0), (vec!["没有告警，不用打扰。", "NOTHING_TO_REPORT: 没有告警"], 1)] {
        let mut scripts = vec![Script::text("无 ALERT")];
        scripts.extend(bot_replies.iter().map(|reply| Script::text(*reply)));
        let mut h = bot(scripts).await;
        let rx = h.kernel.start_run(a_run()).unwrap();
        let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
        outcome(rx).await;
        assert!(events.iter().any(|e| matches!(e.msg, EventMsg::TaskCompleted { .. })), "{bot_replies:?}");
        assert!(!events.iter().any(|e| matches!(e.msg, EventMsg::TaskFailed { .. })), "{bot_replies:?}");
        assert!(sent(&events).is_empty());
        assert_eq!(routine_reminders(&h), reminders);
        assert_eq!(h.api.requests().len(), 1 + bot_replies.len());
    }
}

/// The phrase is not for the owner, even when a model sends it instead of writing it.
#[tokio::test]
async fn the_phrase_never_reaches_the_owner() {
    let mut h = bot(vec![
        Script::text("无 ALERT"),
        Script::tool_call("c1", "send_to_user", json!({ "text": "NOTHING_TO_REPORT", "end_turn": true })),
        Script::text("NOTHING_TO_REPORT"),
    ])
    .await;
    let rx = h.kernel.start_run(a_run()).unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    outcome(rx).await;
    let refused = events.iter().find_map(|e| match &e.msg {
        EventMsg::ToolCompleted { result, .. } if result.tool_name == "send_to_user" => Some(result.is_error),
        _ => None,
    });
    assert_eq!(refused, Some(true));
    assert!(events.iter().any(|e| matches!(e.msg, EventMsg::TaskCompleted { .. })));
    assert_eq!(routine_reminders(&h), 0);
}

/// Nothing to tell about a report says nothing about what the owner asked and never heard back on.
#[tokio::test]
async fn nothing_to_tell_does_not_answer_the_owner() {
    let mut h = bot(vec![
        Script::text("想一想。"),
        Script::text("再想想。"),
        Script::text("无 ALERT"),
        Script::text("NOTHING_TO_REPORT"),
        send_last("c1", "这才回你。"),
    ])
    .await;
    h.kernel.submit(Op::prompt("写一首诗")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert!(events.iter().any(|e| matches!(e.msg, EventMsg::TaskFailed { .. })));
    let rx = h.kernel.start_run(a_run()).unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    outcome(rx).await;
    assert_eq!(sent(&events), ["这才回你。"]);
}

/// Told already, nobody waits; and a sub-agent's report never counted.
#[tokio::test]
async fn a_report_passed_on_needs_no_reminder() {
    let mut h = bot(vec![
        Script::text("跌到 290。"),
        Script::tool_call("c1", "send_to_user", json!({ "text": "跌到 290 了。" })),
        Script::text("Done."),
    ])
    .await;
    let rx = h.kernel.start_run(a_run()).unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    outcome(rx).await;
    assert_eq!(h.api.requests().len(), 3);
    assert_eq!(routine_reminders(&h), 0);
}

/// The owner stopped the bot earlier; that stops that work, not what they asked for at 9 o'clock.
/// Held back, the report would be lost when the session unloads.
#[tokio::test]
async fn a_report_wakes_a_bot_that_was_stopped() {
    let mut h = bot(vec![
        Script::text("写到一半").then_sleep(Duration::from_secs(30)),
        Script::text("跌到 290。"),
        send_last("c1", "跌到 290 了。"),
    ])
    .await;
    h.kernel.submit(Op::prompt("写一首诗")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TurnStarted { .. })).await;
    h.kernel.submit(Op::Interrupt).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    let rx = h.kernel.start_run(a_run()).unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert_eq!(sent(&events), ["跌到 290 了。"]);
    outcome(rx).await;
}

/// A sub-agent's work and a run don't mix: a run under way does not keep the bot from being idle,
/// and the bot's sub-agents don't see it.
#[tokio::test]
async fn a_run_does_not_keep_the_bot_busy() {
    let mut h = bot(vec![
        Script::text("Looking…").then_sleep(Duration::from_secs(30)),
        send_last("c1", "在。"),
    ])
    .await;
    let rx = h.kernel.start_run(a_run()).unwrap();
    let deadline = Instant::now() + Duration::from_secs(5);
    while h.api.requests().is_empty() {
        assert!(Instant::now() < deadline, "the run never asked the model");
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    h.kernel.submit(Op::prompt("在吗")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert_eq!(sent(&events), ["在。"]);
    // Stopping the bot leaves it running; stopping it is its own thing.
    h.kernel.submit(Op::Interrupt).await.unwrap();
    assert!(h.kernel.stop_run("run_1"));
    assert_eq!(outcome(rx).await.outcome, RoutineOutcome::Interrupted);
}

fn line(id: &str, name: &str, when: &str, paused: bool) -> RoutineLine {
    RoutineLine { id: id.into(), name: name.into(), when: when.into(), paused }
}

fn updates(request: &Context, text: &str) -> usize {
    let update = format!("<environment_update>\n{text}\n</environment_update>");
    request.messages.iter().filter(|m| texts(m) == update).count()
}

/// The model hears of its routines as of a new name: once per change, and not at all when there
/// have never been any (routine-design §4.4).
#[tokio::test]
async fn the_list_of_routines_is_told_when_it_changes() {
    let mut h = bot(vec![
        send_last("c1", "在。"),
        send_last("c2", "在。"),
        send_last("c3", "在。"),
        send_last("c4", "在。"),
        send_last("c5", "在。"),
    ])
    .await;
    h.kernel.set_routines(listing(&[]));
    h.kernel.run(prompt("一")).await.unwrap();
    let one = listing(&[line("r_1", "喝水", "每天 8 点", false)]);
    h.kernel.set_routines(one.clone());
    h.kernel.run(prompt("二")).await.unwrap();
    h.kernel.set_routines(one.clone());
    h.kernel.run(prompt("三")).await.unwrap();
    let paused = listing(&[line("r_1", "喝水", "每天 8 点", true)]);
    h.kernel.set_routines(paused.clone());
    h.kernel.run(prompt("四")).await.unwrap();
    h.kernel.set_routines(listing(&[]));
    h.kernel.run(prompt("五")).await.unwrap();
    let requests = h.api.requests();
    assert_eq!(updates(&requests[0], "your routines: none"), 0);
    assert_eq!(updates(&requests[0], &one), 0);
    assert_eq!(updates(&requests[1], &one), 1);
    assert_eq!(updates(&requests[2], &one), 1, "kept, not told again");
    assert_eq!(updates(&requests[3], &paused), 1);
    assert_eq!(updates(&requests[4], "your routines: none"), 1);
    assert!(requests[4].system.starts_with("You are 研究员"), "the system prompt stays");
}

/// A compaction leaves the old entry behind: the list is told again.
#[tokio::test]
async fn the_list_is_told_again_after_a_compaction() {
    let mut h = bot(vec![send_last("c1", "在。"), Script::text("SUMMARY"), send_last("c2", "在。")]).await;
    let one = listing(&[line("r_1", "喝水", "每天 8 点", false)]);
    h.kernel.set_routines(one.clone());
    h.kernel.run(prompt("一")).await.unwrap();
    h.kernel.submit(Op::Compact).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::CompactionCompleted { .. })).await;
    until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    h.kernel.run(prompt("二")).await.unwrap();
    let requests = h.api.requests();
    assert_eq!(updates(requests.last().unwrap(), &one), 1);
    let told = bot_entries(&h).iter().filter(|e| matches!(e.kind, EntryKind::Routines { .. })).count();
    assert_eq!(told, 2);
}

/// Logs written before these fields read as they did, and the new fields stay out when unset.
#[test]
fn old_logs_read_the_same() {
    let old: Entry = serde_json::from_value(json!({ "id": "a", "parent": null, "ts": 1, "type": "unanswered" })).unwrap();
    assert_eq!(old.kind, EntryKind::Unanswered { routine: false });
    let back = serde_json::to_value(&old).unwrap();
    assert_eq!(back, json!({ "id": "a", "parent": null, "ts": 1, "type": "unanswered" }));
    let new = Entry { kind: EntryKind::Unanswered { routine: true }, ..old };
    assert_eq!(serde_json::to_value(&new).unwrap()["routine"], json!(true));
}

/// Stands in for the service: says what it was asked.
#[derive(Default)]
struct FakeHost {
    calls: Mutex<Vec<String>>,
    refuse: Mutex<Option<String>>,
}

fn saved(id: &str, name: &str) -> RoutineSaved {
    RoutineSaved {
        id: id.into(),
        name: name.into(),
        when: "每天 8 点".into(),
        next_run: Some("2026-11-06T08:00:00+08:00".into()),
        upcoming: "Fri Nov 6 08:00, Sat Nov 7 08:00, Sun Nov 8 08:00".into(),
    }
}

impl FakeHost {
    fn answer<T>(&self, call: String, answer: T) -> Result<T, String> {
        self.calls.lock().push(call);
        match self.refuse.lock().clone() {
            Some(why) => Err(why),
            None => Ok(answer),
        }
    }
}

#[async_trait]
impl RoutineHost for FakeHost {
    async fn create(&self, bot: &str, new: NewRoutine) -> Result<RoutineSaved, String> {
        self.answer(format!("create {bot} {} [{}] [{}]", new.name, new.schedule, new.when), saved("r_new", &new.name))
    }

    async fn get(&self, bot: &str, id: &str) -> Result<RoutineDetail, String> {
        self.answer(
            format!("get {bot} {id}"),
            RoutineDetail {
                saved: saved(id, "喝水"),
                prompt: "Remind them.".into(),
                schedule: "0 8 * * *".into(),
                state: RoutineState::Active,
                recent: Vec::new(),
            },
        )
    }

    async fn update(&self, bot: &str, id: &str, change: RoutineChange) -> Result<RoutineSaved, String> {
        self.answer(format!("update {bot} {id} {change:?}"), saved(id, "喝水"))
    }

    async fn set_enabled(&self, bot: &str, id: &str, enabled: bool) -> Result<RoutineSaved, String> {
        self.answer(format!("set_enabled {bot} {id} {enabled}"), saved(id, "喝水"))
    }

    async fn delete(&self, bot: &str, id: &str) -> Result<RoutineSaved, String> {
        self.answer(format!("delete {bot} {id}"), saved(id, "喝水"))
    }
}

/// The results of a task's tool calls by name: (text, is_error, details).
fn results(events: &[Event], name: &str) -> Vec<(String, bool, Option<Value>)> {
    events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::ToolCompleted { result, .. } if result.tool_name == name => {
                Some((result.text_content(), result.is_error, result.details.as_ref().map(|details| details.to_value())))
            }
            _ => None,
        })
        .collect()
}

#[tokio::test]
async fn manage_routine_goes_to_the_service_and_tells_the_next_runs() {
    let host = Arc::new(FakeHost::default());
    let scripts = vec![
        Script::tool_call(
            "c1",
            "manage_routine",
            json!({ "action": "create", "name": " 喝水 ", "prompt": "Remind them.", "schedule": "0 8 * * *", "when": "每天 8 点" }),
        ),
        Script::tool_call("c2", "manage_routine", json!({ "action": "get", "id": "r_new" })),
        Script::tool_call("c3", "manage_routine", json!({ "action": "update", "id": "r_new", "prompt": "Remind them kindly." })),
        Script::tool_call("c4", "manage_routine", json!({ "action": "pause", "id": "r_new" })),
        Script::tool_call("c5", "manage_routine", json!({ "action": "resume", "id": "r_new" })),
        Script::tool_call("c6", "manage_routine", json!({ "action": "delete", "id": "r_new" })),
        send_last("c7", "好了。"),
    ];
    let mut h = bot_with(PermissionMode::BypassPermissions, true, scripts, Some(host.clone())).await;
    let result = h.kernel.run(prompt("每天 8 点提醒我喝水")).await.unwrap();
    let all = results(&result.events, "manage_routine");
    assert_eq!(all[0].0, "Saved routine \"喝水\" (id r_new). Next runs in local time: Fri Nov 6 08:00, Sat Nov 7 08:00, Sun Nov 8 08:00.");
    assert_eq!(
        all[0].2,
        Some(json!({ "action": "create", "routine_id": "r_new", "name": "喝水", "when": "每天 8 点", "next_run": "2026-11-06T08:00:00+08:00" }))
    );
    assert!(all[1].0.contains("<instructions>\nRemind them.\n</instructions>"), "{}", all[1].0);
    assert_eq!(all[1].2, None, "reading is not worth a line in the chat");
    assert_eq!(all[3].0, "Paused.");
    assert!(all[4].0.starts_with("Saved routine"), "{}", all[4].0);
    assert_eq!(all[5].0, "Deleted.");
    assert_eq!(
        *host.calls.lock(),
        [
            "create b1 喝水 [0 8 * * *] [每天 8 点]",
            "get b1 r_new",
            "update b1 r_new RoutineChange { name: None, prompt: Some(\"Remind them kindly.\"), schedule: None, when: None }",
            "set_enabled b1 r_new false",
            "set_enabled b1 r_new true",
            "delete b1 r_new",
        ]
    );
}

/// What the model can fix is said to it; what the service says goes through as it is.
#[tokio::test]
async fn manage_routine_explains_what_it_cannot_do() {
    let host = Arc::new(FakeHost::default());
    let scripts = vec![
        Script::tool_call("c1", "manage_routine", json!({ "action": "create", "name": "喝水", "prompt": "x", "when": "每天" })),
        Script::tool_call("c2", "manage_routine", json!({ "action": "update", "id": "r_1", "schedule": "0 9 * * *" })),
        Script::tool_call("c3", "manage_routine", json!({ "action": "update", "id": "r_1" })),
        Script::tool_call("c4", "manage_routine", json!({ "action": "pause" })),
        Script::tool_call("c5", "manage_routine", json!({ "action": "list" })),
        send_last("c6", "好了。"),
    ];
    let mut h = bot_with(PermissionMode::BypassPermissions, true, scripts, Some(host.clone())).await;
    let result = h.kernel.run(prompt("试试")).await.unwrap();
    let all = results(&result.events, "manage_routine");
    assert!(all.iter().all(|r| r.1), "{all:?}");
    assert!(all[0].0.contains("schedule is required on create"), "{}", all[0].0);
    assert!(all[1].0.contains("when is required whenever schedule changes"), "{}", all[1].0);
    assert!(all[2].0.contains("update needs at least one of"), "{}", all[2].0);
    assert!(all[3].0.contains("id is required on pause"), "{}", all[3].0);
    assert!(all[4].0.contains("list"), "{}", all[4].0);
    assert!(host.calls.lock().is_empty(), "nothing reached the service");

    *host.refuse.lock() = Some("That time has already passed: it is now Thu 2026-11-05 22:14.".into());
    h.api.push(Script::tool_call(
        "c7",
        "manage_routine",
        json!({ "action": "create", "name": "喝水", "prompt": "x", "schedule": "2026-01-01 08:00", "when": "元旦 8 点" }),
    ));
    h.api.push(send_last("c8", "时间已过。"));
    let result = h.kernel.run(prompt("再试")).await.unwrap();
    let all = results(&result.events, "manage_routine");
    assert!(all[0].1 && all[0].0.contains("That time has already passed: it is now Thu 2026-11-05 22:14."), "{all:?}");
}

/// Writes ask where other changes ask, in words a person reads; reading never asks.
#[tokio::test]
async fn changing_routines_follows_the_permission_mode() {
    let host = Arc::new(FakeHost::default());
    let scripts = vec![
        Script::tool_call("c1", "manage_routine", json!({ "action": "get", "id": "r_1" })),
        Script::tool_call("c2", "manage_routine", json!({ "action": "create", "name": "喝水", "prompt": "x", "schedule": "0 8 * * *", "when": "每天 8 点" })),
        send_last("c3", "好了。"),
    ];
    let mut h = bot_with(PermissionMode::Manual, true, scripts, Some(host.clone())).await;
    h.kernel.submit(Op::prompt("每天 8 点提醒我喝水")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let asked = events.iter().find_map(|e| match &e.msg {
        EventMsg::ApprovalRequested { name, summary, call_id, .. } => Some((name.clone(), summary.clone(), call_id.clone())),
        _ => None,
    });
    let (name, summary, call_id) = asked.unwrap();
    assert_eq!(name, "manage_routine");
    assert_eq!(summary, "create routine \"喝水\": 每天 8 点");
    assert_eq!(host.calls.lock().len(), 1, "the read went through without asking");
    h.kernel
        .submit(Op::Approval { call_id, decision: her_protocol::ApprovalDecision::Approve })
        .await
        .unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert_eq!(host.calls.lock().len(), 2);
}

/// Without the service there is nothing to manage, and the model hears why.
#[tokio::test]
async fn manage_routine_without_a_service_says_so() {
    let scripts = vec![
        Script::tool_call("c1", "manage_routine", json!({ "action": "delete", "id": "r_1" })),
        send_last("c2", "不行。"),
    ];
    let mut h = bot(scripts).await;
    let result = h.kernel.run(prompt("删掉")).await.unwrap();
    let all = results(&result.events, "manage_routine");
    assert!(all[0].1 && all[0].0.contains("not available"), "{all:?}");
}
