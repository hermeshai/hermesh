//! A bot's session (bot-design): scripted model, real tools, real JSONL session log.

use her_core::BotIdentity;
use her_core::Config;
use her_core::HerHome;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::KernelDeps;
use her_core::ResumeOptions;
use her_core::SessionRef;
use her_core::StartOptions;
use her_core::TaskEnd;
use her_core::store::CreatedSession;
use her_core::store::Entry;
use her_core::store::EntryKind;
use her_core::store::LoadedSession;
use her_core::store::SessionHeader;
use her_core::store::SessionStore;
use her_core::store::SessionSummary;
use her_core::store::SessionWriter;
use her_core::store::StoreError;
use her_core::store::writer::LineSink;
use her_llm::Catalog;
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
use her_protocol::Op;
use her_protocol::PermissionMode;
use her_tools::BashTool;
use her_tools::LocalEnvironment;
use her_tools::builtin_registry;
use serde_json::json;
use std::io;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use tempfile::TempDir;

struct Harness {
    kernel: Kernel,
    api: Arc<ScriptedApi>,
    home: HerHome,
    folder: PathBuf,
    _dir: TempDir,
}

fn deps(api: &Arc<ScriptedApi>, work: &Path, home: &HerHome) -> KernelDeps {
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
        routines: None,
    }
}

/// A bot named `name` in its own folder, as the server starts one.
async fn bot(name: &str, mode: PermissionMode, scripts: Vec<Script>) -> Harness {
    bot_on(None, name, mode, scripts).await
}

/// The same, with its log in `store` instead of the usual file.
async fn bot_on(store: Option<Arc<dyn SessionStore>>, name: &str, mode: PermissionMode, scripts: Vec<Script>) -> Harness {
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
        interactive_approvals: false,
        bot: Some(BotIdentity {
            id: "b1".into(),
            name: name.into(),
        }),
    };
    let mut deps = deps(&api, &folder, &home);
    if let Some(store) = store {
        deps.store = store;
    }
    let kernel = Kernel::start(options, deps).await.unwrap();
    Harness {
        kernel,
        api,
        home,
        folder,
        _dir: dir,
    }
}

fn send(id: &str, text: &str) -> Script {
    Script::tool_call(id, "send_to_user", json!({ "text": text }))
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

/// The texts the user saw, in order: each `send_to_user` call.
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

#[tokio::test]
async fn a_bot_talks_through_send_to_user_and_ends_its_turn() {
    let mut h = bot(
        "研究员",
        PermissionMode::Manual,
        vec![send("c1", "在查了。"), send_last("c2", "三种：甲、乙、丙。")],
    )
    .await;
    let result = h.kernel.run(prompt("常见的方案有哪些？")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert_eq!(sent(&result.events), ["在查了。", "三种：甲、乙、丙。"]);
    // end_turn: no request after the last message.
    let requests = h.api.requests();
    assert_eq!(requests.len(), 2);
    assert_eq!(h.api.remaining(), 0);

    // A bot's own prompt, and its tool.
    let system = &requests[0].system;
    assert!(system.starts_with("You are 研究员, one of the user's bots in Hermesh."), "{system}");
    assert!(!system.contains("You are Her"));
    assert!(!system.contains("{{"), "every mark is filled in");
    assert!(system.contains("Numbers and data:") && system.contains("Acting with care:"));
    let names: Vec<&str> = requests[0].tools.iter().map(|t| t.name.as_str()).collect();
    assert_eq!(names, ["read", "write", "edit", "bash", "spawn_agent", "send_message", "send_to_user", "manage_routine", "tool_search", "call_tool"]);
    // Its sub-agents always work in the background: there is no waiting to choose
    // (bot-turn-design §5.1).
    for tool in requests[0].tools.iter().filter(|t| t.name == "spawn_agent" || t.name == "send_message") {
        assert!(tool.parameters["properties"].get("wait").is_none(), "{}", tool.name);
    }
    let spawn = requests[0].tools.iter().find(|t| t.name == "spawn_agent").unwrap();
    assert!(!spawn.description.contains("wait="), "{}", spawn.description);
    assert!(system.contains("- agents: See how your agents stand, or stop them"));

    // The header names the bot; lists leave the session out.
    let store = JsonlStore::new(h.home.sessions_dir());
    let (header, _) = store.header(&SessionRef::Id(h.kernel.session_id().to_string())).unwrap();
    let bot = header.bot.expect("a bot's header");
    assert_eq!((bot.id.as_str(), bot.name.as_str()), ("b1", "研究员"));
    assert!(store.list(10, None).unwrap().is_empty());
}

#[tokio::test]
async fn an_empty_message_is_an_error_and_ends_nothing() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![
            Script::tool_call("c1", "send_to_user", json!({ "text": "  ", "end_turn": true })),
            send_last("c2", "好的。"),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("hi")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    let requests = h.api.requests();
    assert_eq!(requests.len(), 2, "the failed send did not end the task");
    let Some(Message::ToolResult(result)) = requests[1].messages.last() else {
        panic!("the result of the empty send")
    };
    assert!(result.is_error);
    assert!(result.text_content().contains("text must not be empty"));
}

/// The latest of the owner's messages in a request.
fn owners_last(request: &her_llm::Context) -> String {
    let said = request.messages.iter().rev().filter(|m| matches!(m, Message::User(_))).map(texts);
    said.into_iter().find(|text| !text.starts_with("<reminder>")).unwrap()
}

/// Right after each of the owner's messages comes a word on how to reply, for the model alone
/// (bot-design §3.14). A report brings none: it may not call for a reply.
#[tokio::test]
async fn a_word_follows_each_of_the_owners_messages() {
    let mut h = bot(
        "Her",
        PermissionMode::BypassPermissions,
        vec![
            send("c1", "好。").then_tool_call("c2", "spawn_agent", json!({ "description": "查资料", "task": "Find it." })),
            Script::text("Waiting for its report."),
            Script::text("Found: 42."),
            send_last("c3", "查到了：42。"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("帮我查一下")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    let requests = h.api.requests();
    let word = "<reminder>Reply with send_to_user, and write no text outside the call. Think after it is sent. Long work goes to an agent.</reminder>";
    assert_eq!(texts(requests[0].messages.last().unwrap()), word);
    assert_eq!(texts(&requests[0].messages[requests[0].messages.len() - 2]), "帮我查一下");
    // The sub-agent's message and the report have none.
    assert!(requests[2].messages.iter().all(|m| texts(m) != word), "{:?}", requests[2].messages);
    assert!(texts(requests[3].messages.last().unwrap()).starts_with("<agent_report"));
    // Nor does it show.
    assert!(!events.iter().any(|e| matches!(&e.msg, EventMsg::MessageCompleted { message, .. } if texts(message) == word)));
}

/// A message that came while the bot worked is answered before the task ends, even when the
/// bot said its last word (bot-design §3.3).
#[tokio::test]
async fn end_turn_waits_for_input_that_came_meanwhile() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![
            send_last("c1", "春天的诗。").then_sleep(Duration::from_millis(400)),
            send_last("c2", "夏天的诗。"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("写一首春天的诗")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TurnStarted { .. })).await;
    h.kernel.submit(Op::prompt("再写一首夏天的")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert_eq!(sent(&events), ["春天的诗。", "夏天的诗。"]);
    let requests = h.api.requests();
    assert_eq!(requests.len(), 2);
    assert_eq!(owners_last(&requests[1]), "再写一首夏天的");
}

/// A message during a tool stops nothing: the command finishes, and the message goes in at the
/// boundary (bot-turn-design §4.1).
#[tokio::test]
async fn a_message_while_a_tool_runs_interrupts_nothing() {
    let mut h = bot(
        "Her",
        PermissionMode::BypassPermissions,
        vec![
            Script::tool_call("c1", "send_to_user", json!({ "text": "我跑一下。" })).then_tool_call("c2", "bash", json!({ "command": "sleep 1 && echo finished" })),
            send_last("c3", "跑完了，1 公里是 0.62 英里。"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("跑一下")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::ToolStarted { name, .. } if name == "bash")).await;
    h.kernel.submit(Op::prompt("1 公里是多少英里")).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(matches!(events.last().unwrap().msg, EventMsg::TaskCompleted { .. }), "{:?}", events.last().unwrap().msg);
    assert_eq!(sent(&events), ["跑完了，1 公里是 0.62 英里。"]);
    let requests = h.api.requests();
    let last = requests.last().unwrap();
    let result = last.messages.iter().find_map(|m| match m {
        Message::ToolResult(r) if r.tool_name == "bash" => Some(r.text_content()),
        _ => None,
    });
    assert!(result.unwrap().contains("finished"), "the command ran to its end");
    assert_eq!(owners_last(last), "1 公里是多少英里");
}

const REMINDER: &str = "<reply_reminder>";

fn entries(h: &Harness) -> Vec<her_core::store::Entry> {
    let store = JsonlStore::new(h.home.sessions_dir());
    store.read(&SessionRef::Id(h.kernel.session_id().to_string())).unwrap().1
}

fn reminders(h: &Harness) -> usize {
    entries(h).iter().filter(|e| matches!(e.kind, EntryKind::Unanswered { .. })).count()
}

/// Reminders of what came of its work (bot-turn-design §6).
fn undelivered(h: &Harness) -> usize {
    entries(h).iter().filter(|e| matches!(e.kind, EntryKind::Undelivered)).count()
}

/// A bot that writes its answer as plain text, which the user never sees, is reminded once and
/// sends it (bot-design §3.11).
#[tokio::test]
async fn a_bot_that_answers_in_plain_text_is_reminded_to_send() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![Script::text("2。"), send_last("c1", "2。")],
    )
    .await;
    let result = h.kernel.run(prompt("1 加 1 等于几？")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert_eq!(sent(&result.events), ["2。"]);
    let requests = h.api.requests();
    assert_eq!(requests.len(), 2);
    let last = requests[1].messages.last().unwrap();
    assert!(texts(last).starts_with(REMINDER), "{}", texts(last));
    assert_eq!(reminders(&h), 1);
    // The reminder is the model's alone: no event shows it.
    assert!(result.events.iter().all(|e| !format!("{:?}", e.msg).contains(REMINDER)));
}

/// Once per task: a bot that again sends nothing ends its task.
#[tokio::test]
async fn the_reminder_comes_once_per_task() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![Script::text("嗯。"), Script::text("嗯嗯。"), Script::text("在。"), send_last("c1", "在。")],
    )
    .await;
    let result = h.kernel.run(prompt("在吗")).await.unwrap();
    // Reminded and still silent: the owner is told (D-321).
    let TaskEnd::Failed(error) = &result.end else { panic!("{:?}", result.end) };
    assert_eq!((error.kind.as_str(), error.retryable), ("no_reply", true));
    assert!(sent(&result.events).is_empty());
    assert_eq!(h.api.requests().len(), 2);
    // The next task gets its own.
    let result = h.kernel.run(prompt("在吗？")).await.unwrap();
    assert_eq!(sent(&result.events), ["在。"]);
    assert_eq!((h.api.requests().len(), reminders(&h)), (4, 2));
}

/// A retry after a failure answers the failed task's message: it is reminded too.
#[tokio::test]
async fn a_retried_task_is_reminded_too() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![Script::fail(ErrorKind::QuotaExceeded, "quota exceeded"), Script::text("2。"), send_last("c1", "2。")],
    )
    .await;
    let result = h.kernel.run(prompt("1 加 1 等于几？")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Failed(_)), "{:?}", result.end);
    h.kernel.submit(Op::Continue).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert_eq!(sent(&events), ["2。"]);
    assert_eq!(reminders(&h), 1);
}

/// Answered, or nobody waiting: no reminder. A message that came after the bot's last one is
/// waiting for its own answer.
#[tokio::test]
async fn only_an_unanswered_message_brings_a_reminder() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![
            // Answered, then a private last word.
            send("c1", "好的。"),
            Script::text("Done."),
            // A report wakes the bot, which has nothing to tell: asked once, it stays quiet.
            Script::tool_call("c2", "spawn_agent", json!({ "description": "查资料", "task": "Find it." }))
                .then_tool_call("c3", "send_to_user", json!({ "text": "交给子智能体了。", "end_turn": true })),
            Script::text("Found: 42."),
            Script::text("Nothing to add."),
            Script::text(""),
        ],
    )
    .await;
    h.kernel.run(prompt("记一下")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    h.kernel.submit(Op::prompt("帮我查一下")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert_eq!(h.api.requests().len(), 6);
    assert_eq!(reminders(&h), 0);
    assert_eq!(undelivered(&h), 1);

    // A message that comes while the bot works, then a private last word: reminded.
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![
            send("c1", "春天的诗。").then_sleep(Duration::from_millis(400)),
            Script::text("Both done."),
            send_last("c2", "夏天的诗。"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("写一首春天的诗")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TurnStarted { .. })).await;
    h.kernel.submit(Op::prompt("再写一首夏天的")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert_eq!(sent(&events), ["春天的诗。", "夏天的诗。"]);
    assert_eq!(reminders(&h), 1);
}

/// An answer only written as plain text while another message waits: the reply to that one would
/// count as answering both, and the owner would never see this answer. It is reminded first, with
/// the reminder the last thing the model reads; the message joins after (bot-design §3.11).
#[tokio::test]
async fn a_private_answer_is_reminded_before_the_next_message_joins() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![
            Script::text("春天的诗。").then_sleep(Duration::from_millis(400)),
            send("c1", "春天的诗。"),
            send_last("c2", "夏天的诗。"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("写一首春天的诗")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TurnStarted { .. })).await;
    h.kernel.submit(Op::prompt("再写一首夏天的")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert_eq!(sent(&events), ["春天的诗。", "夏天的诗。"]);
    assert_eq!(reminders(&h), 1);
    let requests = h.api.requests();
    assert_eq!(requests.len(), 3);
    assert!(texts(requests[1].messages.last().unwrap()).starts_with(REMINDER));
    assert!(requests[1].messages.iter().all(|m| texts(m) != "再写一首夏天的"));
    assert_eq!(owners_last(&requests[2]), "再写一首夏天的");
}

/// A message that lands before the first request joins it: the model reads both at once, and the
/// reminder still stands if it answers neither.
#[tokio::test]
async fn a_message_before_the_first_request_joins_it() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![
            Script::text("春。夏。"),
            send("c1", "春。").then_tool_call("c2", "send_to_user", json!({ "text": "夏。", "end_turn": true })),
        ],
    )
    .await;
    // Nothing else runs between these two, so the task starts with both already in.
    h.kernel.submit(Op::prompt("写一首春天的诗")).await.unwrap();
    h.kernel.submit(Op::prompt("写一首夏天的诗")).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(matches!(events.last().unwrap().msg, EventMsg::TaskCompleted { .. }), "{:?}", events.last().unwrap().msg);
    assert_eq!(sent(&events), ["春。", "夏。"]);
    assert_eq!(reminders(&h), 1);
    let requests = h.api.requests();
    assert_eq!(requests.len(), 2);
    let asked: Vec<String> = requests[0].messages.iter().filter(|m| matches!(m, Message::User(_))).map(texts).filter(|t| !t.starts_with("<reminder>")).collect();
    assert_eq!(asked, ["写一首春天的诗", "写一首夏天的诗"]);
}

/// A message that lands while the reminder's reply is written joins after it, like one that lands
/// before: the reply it brings answers the first message.
#[tokio::test]
async fn a_message_during_the_reminders_reply_joins_after_it() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![
            Script::text("春天的诗。"),
            send_last("c1", "春天的诗。").then_sleep(Duration::from_millis(400)),
            send_last("c2", "夏天的诗。"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("写一首春天的诗")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TurnStarted { turn: 1, .. })).await;
    h.kernel.submit(Op::prompt("再写一首夏天的")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert_eq!(sent(&events), ["春天的诗。", "夏天的诗。"]);
    assert_eq!(reminders(&h), 1);
    let requests = h.api.requests();
    assert_eq!(requests.len(), 3);
    assert!(requests[1].messages.iter().all(|m| texts(m) != "再写一首夏天的"));
    assert_eq!(owners_last(&requests[2]), "再写一首夏天的");
}

/// A log whose sync takes a while, the only wait between a task's last look at the queue and its
/// end. Nothing is read back.
struct SlowSyncStore;

struct SlowSyncLog;

impl LineSink for SlowSyncLog {
    fn write_line(&mut self, _line: &str) -> io::Result<()> {
        Ok(())
    }

    fn sync(&mut self) -> io::Result<()> {
        std::thread::sleep(Duration::from_millis(200));
        Ok(())
    }
}

impl SessionStore for SlowSyncStore {
    fn create(&self, _header: &SessionHeader) -> Result<CreatedSession, StoreError> {
        Ok(CreatedSession {
            location: PathBuf::from("memory://slow"),
            writer: SessionWriter::spawn(SlowSyncLog).unwrap(),
        })
    }

    fn header(&self, reference: &SessionRef) -> Result<(SessionHeader, PathBuf), StoreError> {
        Err(StoreError::NotFound(format!("{reference:?}")))
    }

    fn open(&self, reference: &SessionRef) -> Result<LoadedSession, StoreError> {
        Err(StoreError::NotFound(format!("{reference:?}")))
    }

    fn read(&self, reference: &SessionRef) -> Result<(SessionHeader, Vec<Entry>), StoreError> {
        Err(StoreError::NotFound(format!("{reference:?}")))
    }

    fn list(&self, _limit: usize, _within: Option<&Path>) -> Result<Vec<SessionSummary>, StoreError> {
        Ok(Vec::new())
    }
}

/// A message that lands as an answered task ends, past its last look at the queue, starts the next.
#[tokio::test]
async fn a_message_that_lands_as_a_task_ends_starts_the_next() {
    let mut h = bot_on(
        Some(Arc::new(SlowSyncStore)),
        "Her",
        PermissionMode::Manual,
        vec![send_last("c1", "春。"), send_last("c2", "夏。")],
    )
    .await;
    h.kernel.submit(Op::prompt("写一首春天的诗")).await.unwrap();
    // The task goes straight on from this event to the sync: this test runs again only then.
    until(&mut h.kernel, |m| matches!(m, EventMsg::TurnCompleted { turn: 0, .. })).await;
    h.kernel.submit(Op::prompt("写一首夏天的诗")).await.unwrap();
    let first = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(matches!(first.last().unwrap().msg, EventMsg::TaskCompleted { .. }), "{:?}", first.last().unwrap().msg);
    let second = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(matches!(second.last().unwrap().msg, EventMsg::TaskCompleted { .. }), "{:?}", second.last().unwrap().msg);
    assert_eq!(sent(&second), ["夏。"]);
}

/// A message that lands as a silent bot's task ends, past its last look at the queue: the owner
/// hears the task failed and the message is answered, as when it lands a moment later. It does
/// not go back to the composer: the model works, and nobody would have answered it.
#[tokio::test]
async fn a_message_that_lands_as_a_silent_task_ends_is_answered() {
    let mut h = bot_on(
        Some(Arc::new(SlowSyncStore)),
        "Her",
        PermissionMode::Manual,
        vec![Script::text("春。"), Script::text("还是春。"), send_last("c1", "夏。")],
    )
    .await;
    h.kernel.submit(Op::prompt("写一首春天的诗")).await.unwrap();
    // The task goes straight on from this event to the sync: this test runs again only then.
    until(&mut h.kernel, |m| matches!(m, EventMsg::TurnCompleted { turn: 1, .. })).await;
    h.kernel.submit(Op::prompt("写一首夏天的诗")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let failure = events.iter().find_map(|e| match &e.msg {
        EventMsg::TaskFailed { error, returned_input, .. } => Some((error.kind.as_str(), returned_input.len())),
        _ => None,
    });
    assert_eq!(failure, Some(("no_reply", 0)));
    assert_eq!(sent(&events), ["夏。"]);
}

/// Each message in a row counts anew: the first one's reminder does not use up the next one's,
/// which would fail the task with both already answered.
#[tokio::test]
async fn each_message_in_a_row_gets_its_own_reminder() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![
            Script::text("春。").then_sleep(Duration::from_millis(400)),
            send("c1", "春。"),
            Script::text("夏。"),
            send_last("c2", "夏。"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("写一首春天的诗")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TurnStarted { .. })).await;
    h.kernel.submit(Op::prompt("写一首夏天的诗")).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(matches!(events.last().unwrap().msg, EventMsg::TaskCompleted { .. }), "{:?}", events.last().unwrap().msg);
    assert_eq!(sent(&events), ["春。", "夏。"]);
    assert_eq!(reminders(&h), 2);
}

/// Reminded and still silent with another message waiting: that message gets its turn. Failing
/// here would hand it back to the owner unread.
#[tokio::test]
async fn silence_after_a_reminder_gives_way_to_the_next_message() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![
            Script::text("春。").then_sleep(Duration::from_millis(400)),
            Script::text("还是春。"),
            send_last("c1", "夏。"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("写一首春天的诗")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TurnStarted { .. })).await;
    h.kernel.submit(Op::prompt("写一首夏天的诗")).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(matches!(events.last().unwrap().msg, EventMsg::TaskCompleted { .. }), "{:?}", events.last().unwrap().msg);
    assert_eq!(sent(&events), ["夏。"]);
    assert_eq!(reminders(&h), 1);
}

/// A bot's sub-agents write reports, with a word to the owner on the way: a session's prompt, and
/// a send_to_user of their own that only takes words (bot-design §2.3, agent-messaging-design §17).
#[tokio::test]
async fn a_bots_sub_agents_get_a_sessions_prompt_and_a_send_to_user_of_their_own() {
    let mut h = bot(
        "研究员",
        PermissionMode::Manual,
        vec![
            Script::tool_call("c1", "spawn_agent", json!({ "description": "查资料", "task": "Find it." }))
                .then_tool_call("c2", "send_to_user", json!({ "text": "交给子智能体了。", "end_turn": true })),
            // The sub-agent: a word to the owner, which needs no approval here, and its report.
            Script::tool_call("k1", "send_to_user", json!({ "text": "查到一半了。" })),
            Script::text("Found: 42."),
            // The bot, woken by the report.
            send_last("c3", "查到了：42。"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("帮我查一下")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert_eq!(sent(&events), ["交给子智能体了。", "查到了：42。"]);
    assert!(events.iter().any(|e| is_told(&e.msg)));
    assert!(!events.iter().any(|e| matches!(&e.msg, EventMsg::AgentEvent { event, .. } if matches!(event.msg, EventMsg::ApprovalRequested { .. }))));
    let requests = h.api.requests();
    assert_eq!(requests.len(), 4);
    let agent = &requests[1];
    assert!(agent.system.starts_with("You are Her"), "{}", agent.system);
    let names: Vec<&str> = agent.tools.iter().map(|t| t.name.as_str()).collect();
    assert_eq!(names, ["read", "write", "edit", "bash", "send_message", "send_to_user", "tool_search", "call_tool"]);
    let send = agent.tools.iter().find(|t| t.name == "send_to_user").unwrap();
    assert_eq!(send.parameters["properties"].as_object().unwrap().keys().collect::<Vec<_>>(), ["text"], "no end_turn, files or questions");
    assert!(texts(&agent.messages[0]).contains("call send_to_user with a brief update"));
    assert!(agent.system.contains("- agents: See the other agents in this session"), "{}", agent.system);
    // The sub-agent works in the bot's folder too.
    assert!(agent.system.contains(&format!("cwd: {}", h.folder.display())));
    assert!(requests[3].system.starts_with("You are 研究员"));
}

/// A new name reaches the model once, at the next turn, as the date does (bot-design §2.4).
#[tokio::test]
async fn a_new_name_is_told_once() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![send_last("c1", "在。"), send_last("c2", "我是研究员。"), send_last("c3", "好。")],
    )
    .await;
    h.kernel.run(prompt("在吗")).await.unwrap();
    h.kernel.set_bot_name("研究员".into());
    h.kernel.run(prompt("你叫什么")).await.unwrap();
    h.kernel.run(prompt("好")).await.unwrap();
    let update = "<environment_update>\nyour name: 研究员\n</environment_update>";
    let requests = h.api.requests();
    let count = |i: usize| requests[i].messages.iter().filter(|m| texts(m) == update).count();
    assert_eq!((count(0), count(1), count(2)), (0, 1, 1), "told at the second task, then kept");
    // The system prompt stays as it was.
    assert!(requests[2].system.starts_with("You are Her, one of the user's bots"));
    let store = JsonlStore::new(h.home.sessions_dir());
    let (_, entries) = store.read(&SessionRef::Id(h.kernel.session_id().to_string())).unwrap();
    let names: Vec<&str> = entries
        .iter()
        .filter_map(|e| match &e.kind {
            EntryKind::BotName { name } => Some(name.as_str()),
            _ => None,
        })
        .collect();
    assert_eq!(names, ["研究员"]);
}

/// "On it", then the result in plain text: the first message must not count as delivering it
/// (bot-turn-design §6).
#[tokio::test]
async fn a_result_written_after_the_first_message_is_reminded_and_sent() {
    let mut h = bot(
        "Her",
        PermissionMode::BypassPermissions,
        vec![
            send("c1", "在查了。"),
            Script::tool_call("c2", "bash", json!({ "command": "echo 42" })),
            Script::text("结果是 42。"),
            send_last("c3", "结果是 42。"),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("算一下")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert_eq!(sent(&result.events), ["在查了。", "结果是 42。"]);
    assert_eq!((reminders(&h), undelivered(&h)), (0, 1));
    let requests = h.api.requests();
    let reminder = requests.last().unwrap().messages.iter().map(texts).rfind(|t| t.contains(REMINDER)).unwrap();
    assert!(reminder.contains("has not reached the user"), "{reminder}");
}

/// A report that wakes the bot, written up only in plain text.
#[tokio::test]
async fn a_report_written_up_in_plain_text_is_reminded_and_sent() {
    let mut h = bot(
        "Her",
        PermissionMode::BypassPermissions,
        vec![
            Script::tool_call("c1", "send_to_user", json!({ "text": "好，我查一下。" })).then_tool_call("c2", "spawn_agent", json!({ "description": "查资料", "task": "Find it." })),
            // Ending the turn after handing the work over owes nothing: no reminder here.
            Script::text("Waiting for its report."),
            Script::text("Found: 42."),
            Script::text("查到了：42。"),
            send_last("c3", "查到了：42。"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("帮我查一下")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert_eq!(sent(&events), ["好，我查一下。", "查到了：42。"]);
    assert_eq!((reminders(&h), undelivered(&h)), (0, 1));
}

/// With the work handed over there is nothing to say, and a model may say just that: an empty reply
/// ends the turn, and the report wakes the bot as usual (D-464).
#[tokio::test]
async fn an_empty_reply_after_handing_work_over_ends_the_turn() {
    let mut h = bot(
        "Her",
        PermissionMode::BypassPermissions,
        vec![
            Script::tool_call("c1", "send_to_user", json!({ "text": "好，我查一下。" })).then_tool_call("c2", "spawn_agent", json!({ "description": "查资料", "task": "Find it." })),
            Script::text(""),
            Script::text("Found: 42."),
            send_last("c3", "查到了：42。"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("帮我查一下")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert_eq!(sent(&events), ["好，我查一下。", "查到了：42。"]);
    assert_eq!((reminders(&h), undelivered(&h)), (0, 0));
    assert!(!events.iter().any(|e| matches!(e.msg, EventMsg::TaskFailed { .. })), "{events:?}");
}

/// Reminded of what came of its work and still silent: that may be right, so the task just ends.
#[tokio::test]
async fn silence_after_a_reminder_of_its_work_is_no_failure() {
    let mut h = bot(
        "Her",
        PermissionMode::BypassPermissions,
        vec![
            send("c1", "在查了。"),
            Script::tool_call("c2", "bash", json!({ "command": "echo 42" })),
            Script::text("Nothing worth saying."),
            Script::text("Still nothing."),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("看一下")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }), "{:?}", result.end);
    assert_eq!(undelivered(&h), 1);
}

/// A new message from the owner counts anew: the reminder before it does not use up the next.
#[tokio::test]
async fn a_new_message_counts_anew_after_a_reminder() {
    let mut h = bot(
        "Her",
        PermissionMode::BypassPermissions,
        vec![
            // Written only, reminded, and a new message comes while it is reminded.
            Script::text("Only written."),
            Script::default().then_sleep(Duration::from_millis(500)).then_text("Written again."),
            // The new message is unanswered too: a second reminder, not a failure.
            Script::text("Written a third time."),
            send_last("c1", "都回了。"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("第一句")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TurnStarted { turn: 1, .. })).await;
    h.kernel.submit(Op::prompt("第二句")).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(matches!(events.last().unwrap().msg, EventMsg::TaskCompleted { .. }), "{:?}", events.last().unwrap().msg);
    assert_eq!(sent(&events), ["都回了。"]);
    assert_eq!(reminders(&h), 2);
}

/// `wait` is gone from a bot's tools; a model that sends it anyway still works in the background.
#[tokio::test]
async fn a_bots_agents_work_in_the_background_whatever_the_model_sends() {
    let mut h = bot(
        "Her",
        PermissionMode::BypassPermissions,
        vec![
            Script::tool_call("c1", "send_to_user", json!({ "text": "好。" })).then_tool_call("c2", "spawn_agent", json!({ "description": "查资料", "task": "Find it.", "wait": true })),
            Script::text("Waiting for its report."),
            Script::text("Found: 42."),
            send_last("c3", "查到了：42。"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("帮我查一下")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert_eq!(sent(&events), ["好。", "查到了：42。"]);
    // The call returned at once, and the report came as mail.
    let started = h.api.requests().iter().flat_map(|r| r.messages.clone()).find_map(|m| match m {
        Message::ToolResult(r) if r.tool_name == "spawn_agent" => Some(r.text_content()),
        _ => None,
    });
    assert!(started.unwrap().contains("started in the background"));
    assert!(events.iter().any(|e| matches!(&e.msg, EventMsg::MessageCompleted { mail: Some(_), .. })));
}

/// A bot's requests and its sub-agent's run side by side: each takes from its own list.
fn scripted_apart(h: &Harness, bot: Vec<Script>, agent: Vec<Script>) {
    let lists = parking_lot::Mutex::new((std::collections::VecDeque::from(bot), std::collections::VecDeque::from(agent)));
    h.api.respond_with(move |context| {
        let first = context.messages.iter().find_map(|m| match m {
            Message::User(user) => Some(user.text_content()),
            _ => None,
        });
        let mut lists = lists.lock();
        if first.is_some_and(|text| text.starts_with("You are a sub-agent")) { lists.1.pop_front() } else { lists.0.pop_front() }
    });
}

fn handing_over() -> Script {
    send("c1", "好，我来。").then_tool_call("c2", "spawn_agent", json!({ "description": "汇总", "task": "Sum it.", "name": "sales" }))
}

/// A sub-agent that speaks up when the test says so, and finishes when it says so: the bot must have
/// ended its turn by then, however fast the machine is.
fn speaking_up() -> Vec<Script> {
    let wait_for = |id: &str, cue: &str| Script::tool_call(id, "bash", json!({ "command": format!("while [ ! -f {cue} ]; do sleep 0.02; done") }));
    vec![
        wait_for("k0", "speak"),
        Script::tool_call("k1", "send_message", json!({ "to": "main", "message": "3 月的数据整月缺失。" })),
        wait_for("k2", "finish"),
        Script::text("汇总好了。"),
    ]
}

/// Until the bot has heard the agent speak and then report.
async fn through_both(h: &mut Harness) -> Vec<Event> {
    let done = |m: &EventMsg| matches!(m, EventMsg::TaskCompleted { .. });
    let mut events = until(&mut h.kernel, done).await;
    std::fs::write(h.folder.join("speak"), "").unwrap();
    events.extend(until(&mut h.kernel, done).await);
    std::fs::write(h.folder.join("finish"), "").unwrap();
    events.extend(until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await);
    events
}

/// What a sub-agent sends wakes the bot, and the owner hears it from the bot while the work goes
/// on (agent-messaging-design §3.1).
#[tokio::test]
async fn a_bot_relays_what_a_working_agent_sends() {
    let mut h = bot("Her", PermissionMode::BypassPermissions, vec![]).await;
    let bot_scripts = vec![handing_over(), Script::text("It is at work."), send_last("c3", "3 月的数据缺了，我先按其余 11 个月做。"), send_last("c4", "做好了。")];
    scripted_apart(&h, bot_scripts, speaking_up());
    h.kernel.submit(Op::prompt("把销售额按月汇总")).await.unwrap();
    let events = through_both(&mut h).await;
    assert_eq!(sent(&events), ["好，我来。", "3 月的数据缺了，我先按其余 11 个月做。", "做好了。"]);
    assert_eq!((reminders(&h), undelivered(&h)), (0, 0), "each came with what it owed");
}

/// It may have nothing the owner needs: one word about it, like after any work, and no failure.
#[tokio::test]
async fn a_bot_that_keeps_a_message_to_itself_is_reminded_once() {
    let mut h = bot("Her", PermissionMode::BypassPermissions, vec![]).await;
    let bot_scripts = vec![handing_over(), Script::text("It is at work."), Script::text("Nothing for them yet."), Script::text("Really nothing."), send_last("c4", "做好了。")];
    scripted_apart(&h, bot_scripts, speaking_up());
    h.kernel.submit(Op::prompt("把销售额按月汇总")).await.unwrap();
    let events = through_both(&mut h).await;
    assert_eq!(sent(&events), ["好，我来。", "做好了。"]);
    assert_eq!((reminders(&h), undelivered(&h)), (0, 1));
    assert!(!events.iter().any(|e| matches!(e.msg, EventMsg::TaskFailed { .. })));
}

const TOLD: &str = "3 月的数据整月缺失，我先按其余 11 个月做。";

/// A sub-agent that tells the owner when the test says so, and finishes when it says so.
fn telling() -> Vec<Script> {
    let wait_for = |id: &str, cue: &str| Script::tool_call(id, "bash", json!({ "command": format!("while [ ! -f {cue} ]; do sleep 0.02; done") }));
    vec![
        wait_for("k0", "speak"),
        Script::tool_call("k1", "send_to_user", json!({ "text": TOLD })),
        wait_for("k2", "finish"),
        Script::text("汇总好了。"),
    ]
}

fn is_told(m: &EventMsg) -> bool {
    matches!(m, EventMsg::MessageCompleted { mail: Some(mail), .. } if mail.kind == her_protocol::MailKind::ToUser)
}

fn told_in_log(h: &Harness) -> usize {
    entries(h).iter().filter(|e| matches!(e.kind, EntryKind::AgentToUser { .. })).count()
}

/// Until the log says so: it is written behind the events.
async fn until_told_in_log(h: &Harness) {
    for _ in 0..200 {
        if told_in_log(h) > 0 {
            return;
        }
        tokio::time::sleep(std::time::Duration::from_millis(10)).await;
    }
    panic!("what the agent said is not in the log");
}

/// What the bot's own requests were, not its agent's.
fn bots_requests(h: &Harness) -> Vec<her_llm::Context> {
    h.api.requests().into_iter().filter(|r| r.system.starts_with("You are Her, one of the user's bots")).collect()
}

const HEARD: &str = "<sent_to_user from=\"sales\">\n3 月的数据整月缺失，我先按其余 11 个月做。\n</sent_to_user>";

/// A busy bot: this runs until the test says go.
fn busy(id: &str) -> Script {
    Script::tool_call(id, "bash", json!({ "command": "while [ ! -f go ]; do sleep 0.02; done" }))
}

fn own_tool_started(m: &EventMsg) -> bool {
    matches!(m, EventMsg::ToolStarted { name, .. } if name == "bash")
}

/// What a sub-agent tells the owner reaches them at once and costs no request: the bot sleeps on,
/// and reads it with whatever wakes it next (agent-messaging-design §17).
#[tokio::test]
async fn what_an_agent_tells_the_owner_wakes_nobody() {
    let mut h = bot("Her", PermissionMode::BypassPermissions, vec![]).await;
    scripted_apart(&h, vec![handing_over(), Script::text("It is at work."), send_last("c4", "做好了。")], telling());
    h.kernel.submit(Op::prompt("把销售额按月汇总")).await.unwrap();
    let opening = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert_eq!(sent(&opening), ["好，我来。"]);

    std::fs::write(h.folder.join("speak"), "").unwrap();
    let spoke = until(&mut h.kernel, is_told).await;
    assert!(!spoke.iter().any(|e| matches!(e.msg, EventMsg::TaskStarted { .. })), "{spoke:?}");
    assert_eq!(bots_requests(&h).len(), 2);
    let EventMsg::MessageCompleted { message, mail: Some(mail), .. } = &spoke.last().unwrap().msg else {
        unreachable!()
    };
    assert_eq!(texts(message), HEARD);
    assert_eq!((mail.from.name.as_str(), mail.status), ("sales", her_protocol::AgentStatus::Running));
    // Nothing runs, so nothing stands in its way: a restart now would keep it.
    until_told_in_log(&h).await;

    std::fs::write(h.folder.join("finish"), "").unwrap();
    let rest = until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert_eq!(sent(&rest), ["做好了。"]);
    let requests = bots_requests(&h);
    assert_eq!(requests.len(), 3, "only the report woke it");
    let heard: Vec<String> = requests[2].messages.iter().map(texts).filter(|t| t.starts_with("<sent_to_user") || t.starts_with("<agent_report")).collect();
    assert_eq!(heard.len(), 2, "{heard:?}");
    assert_eq!(heard[0], HEARD);
    assert!(heard[1].starts_with("<agent_report agent=\"sales\""), "{heard:?}");
    assert_eq!(told_in_log(&h), 1);
    assert_eq!((reminders(&h), undelivered(&h)), (0, 0), "it owes nothing for what the owner has");
}

/// While the bot works the owner still hears at once. The model reads it at the next boundary,
/// never between a call and its result.
#[tokio::test]
async fn what_an_agent_tells_the_owner_joins_a_working_bots_history_at_the_next_boundary() {
    let mut h = bot("Her", PermissionMode::BypassPermissions, vec![]).await;
    let bots = vec![handing_over(), busy("c3"), send_last("c4", "这边好了。"), send_last("c5", "做好了。")];
    scripted_apart(&h, bots, telling());
    h.kernel.submit(Op::prompt("把销售额按月汇总")).await.unwrap();
    until(&mut h.kernel, own_tool_started).await;

    std::fs::write(h.folder.join("speak"), "").unwrap();
    until(&mut h.kernel, is_told).await;
    assert_eq!(told_in_log(&h), 0);

    std::fs::write(h.folder.join("go"), "").unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let requests = bots_requests(&h);
    let at = requests[2].messages.iter().position(|m| texts(m) == HEARD).expect("the next request has it");
    assert!(matches!(&requests[2].messages[at - 1], Message::ToolResult(result) if result.call_id == "c3"), "{:?}", requests[2].messages[at - 1]);

    std::fs::write(h.folder.join("finish"), "").unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert_eq!(told_in_log(&h), 1, "written once");
    let last = bots_requests(&h).pop().unwrap();
    assert_eq!(last.messages.iter().filter(|m| texts(m) == HEARD).count(), 1);
}

/// When no boundary follows, the end of the task is the place: the next may be a long way off.
#[tokio::test]
async fn what_an_agent_tells_the_owner_as_a_task_ends_is_written_with_its_end() {
    let mut h = bot("Her", PermissionMode::BypassPermissions, vec![]).await;
    let last_step = busy("c3").then_tool_call("c4", "send_to_user", json!({ "text": "你先忙。", "end_turn": true }));
    scripted_apart(&h, vec![handing_over(), last_step, send_last("c5", "做好了。")], telling());
    h.kernel.submit(Op::prompt("把销售额按月汇总")).await.unwrap();
    until(&mut h.kernel, own_tool_started).await;

    std::fs::write(h.folder.join("speak"), "").unwrap();
    until(&mut h.kernel, is_told).await;
    assert_eq!(told_in_log(&h), 0);
    std::fs::write(h.folder.join("go"), "").unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    until_told_in_log(&h).await;
    assert_eq!(bots_requests(&h).len(), 2, "no request for it");

    std::fs::write(h.folder.join("finish"), "").unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    assert_eq!(told_in_log(&h), 1, "written once");
    let last = bots_requests(&h).pop().unwrap();
    assert_eq!(last.messages.iter().filter(|m| texts(m) == HEARD).count(), 1);
}

/// Read back from the log: the owner still sees it, and the model still knows.
#[tokio::test]
async fn what_an_agent_told_the_owner_survives_a_restart() {
    let mut h = bot("Her", PermissionMode::BypassPermissions, vec![]).await;
    scripted_apart(&h, vec![handing_over(), Script::text("It is at work."), send_last("c9", "在。")], telling());
    h.kernel.submit(Op::prompt("把销售额按月汇总")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    std::fs::write(h.folder.join("speak"), "").unwrap();
    until(&mut h.kernel, is_told).await;
    until_told_in_log(&h).await;
    let mut kernel = h.kernel;
    let id = kernel.session_id().to_string();
    kernel.shutdown().await;

    let options = ResumeOptions {
        session: SessionRef::Id(id),
        model: None,
        thinking: None,
        permission_mode: None,
        home: h.home.clone(),
        config: Arc::new(Config::default()),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot_name: None,
    };
    let mut kernel = Kernel::resume(options, deps(&h.api, &h.folder, &h.home)).await.unwrap();
    let told = kernel.transcript().into_iter().find_map(|item| match item {
        her_protocol::api::TranscriptItem::Message { message, mail: Some(mail), .. } if mail.kind == her_protocol::MailKind::ToUser => Some(texts(&message)),
        _ => None,
    });
    assert_eq!(told.as_deref(), Some(HEARD));
    kernel.run(prompt("还在吗")).await.unwrap();
    let requests = h.api.requests();
    assert_eq!(requests.last().unwrap().messages.iter().filter(|m| texts(m) == HEARD).count(), 1);
}

/// What an agent says is no reply of the bot's: the owner's message is still owed one.
#[tokio::test]
async fn what_an_agent_tells_the_owner_answers_nothing_for_the_bot() {
    let mut h = bot("Her", PermissionMode::BypassPermissions, vec![]).await;
    let hand_over = Script::tool_call("c1", "spawn_agent", json!({ "description": "汇总", "task": "Sum it.", "name": "sales" }));
    let bots = vec![hand_over, busy("c2"), Script::text("Only my own thoughts."), send_last("c3", "好，在做了。"), send_last("c4", "做好了。")];
    scripted_apart(&h, bots, telling());
    h.kernel.submit(Op::prompt("把销售额按月汇总")).await.unwrap();
    until(&mut h.kernel, own_tool_started).await;
    std::fs::write(h.folder.join("speak"), "").unwrap();
    until(&mut h.kernel, is_told).await;
    std::fs::write(h.folder.join("go"), "").unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert_eq!(sent(&events), ["好，在做了。"]);
    assert_eq!(reminders(&h), 1);
    std::fs::write(h.folder.join("finish"), "").unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
}

/// A new `bot.md` reaches a bot that already has a conversation, once; it costs a new
/// `system_prompt` entry (bot-turn-design §8).
#[tokio::test]
async fn a_new_bot_prompt_reaches_a_resumed_bot_once() {
    let h = bot("Her", PermissionMode::Manual, vec![send_last("c1", "在。")]).await;
    let mut kernel = h.kernel;
    kernel.run(prompt("在吗")).await.unwrap();
    let id = kernel.session_id().to_string();
    kernel.shutdown().await;
    let file = h.folder.join("bot-prompt.md");
    std::fs::write(&file, "You are {{name}}, with new rules.\n").unwrap();
    let resume = |config: Config| ResumeOptions {
        session: SessionRef::Id(id.clone()),
        model: None,
        thinking: None,
        permission_mode: None,
        home: h.home.clone(),
        config: Arc::new(config),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot_name: None,
    };
    let prompts = || {
        let store = JsonlStore::new(h.home.sessions_dir());
        let (_, entries) = store.read(&SessionRef::Id(id.clone())).unwrap();
        entries.iter().filter(|e| matches!(e.kind, EntryKind::SystemPrompt(_))).count()
    };
    assert_eq!(prompts(), 1);

    // The same `bot.md`: nothing changes.
    h.api.push(send_last("c2", "还在。"));
    let mut kernel = Kernel::resume(resume(Config::default()), deps(&h.api, &h.folder, &h.home)).await.unwrap();
    kernel.run(prompt("还在吗")).await.unwrap();
    kernel.shutdown().await;
    assert_eq!(prompts(), 1);
    assert!(h.api.requests().last().unwrap().system.starts_with("You are Her, one of the user's bots"));

    // A new one: the next request uses it, under the name in the header.
    let config = || Config {
        bot_prompt_file: Some(file.clone()),
        ..Config::default()
    };
    h.api.push(send_last("c3", "新规则。"));
    let mut kernel = Kernel::resume(resume(config()), deps(&h.api, &h.folder, &h.home)).await.unwrap();
    kernel.run(prompt("有什么变化")).await.unwrap();
    kernel.shutdown().await;
    assert_eq!(prompts(), 2);
    assert!(h.api.requests().last().unwrap().system.starts_with("You are Her, with new rules."));

    // And only once.
    h.api.push(send_last("c4", "好。"));
    let mut kernel = Kernel::resume(resume(config()), deps(&h.api, &h.folder, &h.home)).await.unwrap();
    kernel.run(prompt("好")).await.unwrap();
    kernel.shutdown().await;
    assert_eq!(prompts(), 2);
}

/// A resumed bot session keeps its tool and prompt; a name changed meanwhile is told.
#[tokio::test]
async fn a_resumed_bot_keeps_send_to_user_and_learns_its_new_name() {
    let h = bot("Her", PermissionMode::Manual, vec![send_last("c1", "在。")]).await;
    let mut kernel = h.kernel;
    kernel.run(prompt("在吗")).await.unwrap();
    let id = kernel.session_id().to_string();
    kernel.shutdown().await;
    h.api.push(send_last("c2", "我叫运维。"));
    let options = ResumeOptions {
        session: SessionRef::Id(id),
        model: None,
        thinking: None,
        permission_mode: None,
        home: h.home.clone(),
        config: Arc::new(Config::default()),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot_name: Some("运维".into()),
    };
    let mut kernel = Kernel::resume(options, deps(&h.api, &h.folder, &h.home)).await.unwrap();
    let result = kernel.run(prompt("你叫什么")).await.unwrap();
    assert_eq!(sent(&result.events), ["我叫运维。"]);
    let requests = h.api.requests();
    let last = requests.last().unwrap();
    assert!(last.system.starts_with("You are Her, one of the user's bots"), "the logged prompt");
    assert!(last.tools.iter().any(|t| t.name == "send_to_user"));
    let update = "<environment_update>\nyour name: 运维\n</environment_update>";
    assert!(last.messages.iter().any(|m| texts(m) == update));
}

/// Writing in the bot's own folder needs no approval where the workspace's files need none,
/// though the folder is in the data directory (bot-design §2.5).
#[tokio::test]
async fn a_bot_writes_in_its_own_folder_without_asking() {
    let mut h = bot(
        "Her",
        PermissionMode::AcceptEdits,
        vec![
            Script::tool_call("c1", "write", json!({ "path": "report.md", "content": "# 周报" })),
            Script::tool_call("c2", "write", json!({ "path": "../bot.json", "content": "{}" })),
            send_last("c3", "写好了。"),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("写一份周报")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert_eq!(std::fs::read_to_string(h.folder.join("report.md")).unwrap(), "# 周报");
    // Outside its folder, the data directory stays protected: nobody can approve here.
    assert!(!h.folder.parent().unwrap().join("bot.json").exists());
    let results: Vec<String> = h.api.requests()[2]
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::ToolResult(r) => Some(r.text_content()),
            _ => None,
        })
        .collect();
    assert!(results[0].starts_with("Wrote"), "{results:?}");
    assert!(!results[1].starts_with("Wrote"), "{results:?}");
}

// ---- Message types (bot-design §3.12) ----

fn attach(id: &str, files: &[&str]) -> Script {
    let files: Vec<_> = files.iter().map(|path| json!({ "path": path })).collect();
    Script::tool_call(id, "send_to_user", json!({ "type": "attachment", "files": files, "end_turn": true }))
}

/// The head of a PNG of that size: enough to tell what it is.
fn png(width: u32, height: u32) -> Vec<u8> {
    let mut bytes = b"\x89PNG\r\n\x1a\n\0\0\0\rIHDR".to_vec();
    bytes.extend(width.to_be_bytes());
    bytes.extend(height.to_be_bytes());
    bytes.extend([8, 6, 0, 0, 0, 0, 0, 0, 0]);
    bytes
}

/// What the `send_to_user` results told user interfaces.
fn details(events: &[Event]) -> Vec<serde_json::Value> {
    events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::ToolCompleted { result, .. } if result.tool_name == "send_to_user" => {
                result.details.as_ref().map(|d| serde_json::from_str(d.as_str()).unwrap())
            }
            _ => None,
        })
        .collect()
}

/// The result text of each call the model was told about, in the last request.
fn results(h: &Harness) -> Vec<(bool, String)> {
    h.api
        .requests()
        .last()
        .unwrap()
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::ToolResult(r) => Some((r.is_error, r.text_content())),
            _ => None,
        })
        .collect()
}

#[tokio::test]
async fn an_attachment_is_one_message_of_files_and_says_what_they_are() {
    let mut h = bot("Her", PermissionMode::Manual, vec![attach("c1", &["毛利率.png", "./报告.pdf"])]).await;
    std::fs::write(h.folder.join("毛利率.png"), png(960, 600)).unwrap();
    std::fs::write(h.folder.join("报告.pdf"), b"%PDF-1.7").unwrap();
    let result = h.kernel.run(prompt("发我")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert_eq!(h.api.requests().len(), 1, "end_turn ends it");
    let image = h.folder.join("毛利率.png");
    let file = h.folder.join("报告.pdf");
    assert_eq!(
        details(&result.events),
        [json!({ "files": [
            { "path": image, "size": png(960, 600).len(), "image": true, "width": 960, "height": 600 },
            { "path": file, "size": 8 },
        ] })]
    );
}

#[tokio::test]
async fn an_image_is_told_by_its_contents_and_only_up_to_the_panels_limit() {
    let mut h = bot("Her", PermissionMode::Manual, vec![attach("c1", &["photo.pdf", "chart.png", "huge.png"])]).await;
    std::fs::write(h.folder.join("photo.pdf"), png(10, 20)).unwrap();
    std::fs::write(h.folder.join("chart.png"), b"not a picture").unwrap();
    let mut huge = png(4000, 3000);
    huge.resize(her_core::files::MAX_IMAGE as usize + 1, 0);
    std::fs::write(h.folder.join("huge.png"), huge).unwrap();
    let result = h.kernel.run(prompt("发我")).await.unwrap();
    let sent = details(&result.events);
    let files = sent[0]["files"].as_array().unwrap();
    assert_eq!(files[0]["image"], true, "a picture under another name");
    assert_eq!((files[0]["width"].as_u64(), files[0]["height"].as_u64()), (Some(10), Some(20)));
    assert!(files[1].get("image").is_none(), "only named like one");
    assert!(files[2].get("image").is_none(), "too large to show");
}

#[tokio::test]
async fn a_file_made_in_the_same_step_can_be_sent() {
    let mut h = bot(
        "Her",
        PermissionMode::AcceptEdits,
        vec![Script::tool_call("c1", "write", json!({ "path": "report.md", "content": "# 周报" })).then_tool_call(
            "c2",
            "send_to_user",
            json!({ "type": "attachment", "files": [{ "path": "report.md" }], "end_turn": true }),
        )],
    )
    .await;
    let result = h.kernel.run(prompt("写一份周报发我")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert_eq!(details(&result.events)[0]["files"][0]["size"], 8);
}

#[tokio::test]
async fn a_wrong_path_sends_nothing_and_names_each_one() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![
            attach("c1", &["there.txt", "gone.pdf", ".", "https://example.com/a.png"]),
            attach("c2", &["there.txt"]),
        ],
    )
    .await;
    std::fs::write(h.folder.join("there.txt"), "x").unwrap();
    let result = h.kernel.run(prompt("发我")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    let told = results(&h);
    assert!(told[0].0, "{told:?}");
    for part in ["gone.pdf: no such file", ".: not a file", "https://example.com/a.png: a web address, save it to a file first"] {
        assert!(told[0].1.contains(part), "{told:?}");
    }
    assert!(!told[0].1.contains("there.txt"), "{told:?}");
    assert_eq!(details(&result.events).len(), 1, "only the second call went out");
}

/// What a real model does on its own: words and files in one call, often with no type
/// (19 of 59 calls were refused before this was allowed, bot-design §3.12).
#[tokio::test]
async fn files_may_come_with_words_and_need_no_type() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![
            Script::tool_call("c1", "send_to_user", json!({ "type": "attachment", "text": "在这里。", "files": [{ "path": "a.txt" }] })),
            Script::tool_call("c2", "send_to_user", json!({ "text": "还有这个。", "files": [{ "path": "a.txt" }], "end_turn": true })),
        ],
    )
    .await;
    std::fs::write(h.folder.join("a.txt"), "x").unwrap();
    let result = h.kernel.run(prompt("发我")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    let told = details(&result.events);
    assert_eq!(told.len(), 2, "both went out: {told:?}");
    assert_eq!((told[0]["text"].as_str(), told[1]["text"].as_str()), (Some("在这里。"), Some("还有这个。")));
    assert_eq!(h.api.requests().len(), 2, "no request wasted on a refusal");
}

/// A question whose answers cannot be listed is still a question (2 of 12 were refused before).
#[tokio::test]
async fn a_question_may_have_no_options_and_still_ends_the_turn() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![Script::tool_call("c1", "send_to_user", json!({ "type": "question", "text": "发给谁？" }))],
    )
    .await;
    let result = h.kernel.run(prompt("发出去")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert_eq!(sent(&result.events), ["发给谁？"]);
    assert_eq!(h.api.requests().len(), 1);
}

#[tokio::test]
async fn options_without_a_type_make_a_question() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![Script::tool_call("c1", "send_to_user", json!({ "text": "发给谁？", "options": [{ "label": "财务组" }] }))],
    )
    .await;
    h.kernel.run(prompt("发出去")).await.unwrap();
    assert_eq!(h.api.requests().len(), 1, "a question ends the turn");
}

#[tokio::test]
async fn a_question_ends_the_turn_and_its_answer_is_an_ordinary_message() {
    let question = json!({ "type": "question", "text": "发给谁？", "options": [{ "label": "财务组" }, { "label": "全部门" }] });
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![Script::tool_call("c1", "send_to_user", question), send_last("c2", "发好了。")],
    )
    .await;
    let result = h.kernel.run(prompt("整理好发出去")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert_eq!(sent(&result.events), ["发给谁？"]);
    assert_eq!(h.api.requests().len(), 1, "no end_turn needed: it waits for the answer");
    let result = h.kernel.run(prompt("财务组")).await.unwrap();
    assert_eq!(sent(&result.events), ["发好了。"]);
    let requests = h.api.requests();
    assert_eq!(owners_last(&requests[1]), "财务组", "the answer is the owner's message");
    assert!(!requests[1].messages.iter().any(|m| texts(m).contains("<reply_reminder>")), "a question is a reply");
}

#[tokio::test]
async fn a_type_takes_its_own_parameters_and_says_what_is_wrong() {
    let cases = [
        (json!({ "type": "attachment" }), "give at least one file"),
        (json!({ "type": "text", "text": "你好", "files": [{ "path": "a" }] }), "files goes with type attachment"),
        (json!({ "type": "text", "text": "你好", "options": [{ "label": "好" }] }), "options goes with type question"),
        (json!({ "text": "你好", "files": [{ "path": "a" }], "options": [{ "label": "好" }] }), "options goes with type question"),
        (json!({ "type": "question", "options": [{ "label": "好" }] }), "text must not be empty"),
        (json!({ "type": "question", "text": "好吗", "options": [{ "label": " " }] }), "label must not be empty"),
        (json!({ "type": "question", "text": "好吗", "options": [{ "label": "好" }, { "label": "好" }] }), "the same label: 好"),
        (json!({ "type": "image", "text": "你好" }), "unknown variant"),
    ];
    for (args, expected) in cases {
        let mut h = bot(
            "Her",
            PermissionMode::Manual,
            vec![Script::tool_call("c1", "send_to_user", args.clone()), send_last("c2", "好的。")],
        )
        .await;
        let result = h.kernel.run(prompt("hi")).await.unwrap();
        assert!(matches!(result.end, TaskEnd::Completed { .. }));
        let told = results(&h);
        assert!(told[0].0 && told[0].1.contains(expected), "{args}: {told:?}");
    }
}

#[tokio::test]
async fn an_empty_value_counts_as_left_out() {
    let mut h = bot(
        "Her",
        PermissionMode::Manual,
        vec![Script::tool_call(
            "c1",
            "send_to_user",
            json!({ "type": "text", "text": "你好", "files": [], "options": [], "end_turn": true }),
        )],
    )
    .await;
    let result = h.kernel.run(prompt("hi")).await.unwrap();
    assert_eq!(sent(&result.events), ["你好"]);
    assert_eq!(h.api.requests().len(), 1);
}

/// What every request carries: plain enough for any provider (bot-design §3.12).
#[tokio::test]
async fn the_tools_parameters_are_flat_with_a_plain_enum() {
    let mut h = bot("Her", PermissionMode::Manual, vec![send_last("c1", "好的。")]).await;
    h.kernel.run(prompt("hi")).await.unwrap();
    let requests = h.api.requests();
    let tool = requests[0].tools.iter().find(|t| t.name == "send_to_user").unwrap();
    let schema = &tool.parameters;
    assert_eq!(schema["properties"]["type"]["type"], "string");
    assert_eq!(schema["properties"]["type"]["enum"], json!(["text", "attachment", "question"]));
    assert!(schema.get("required").is_none_or(|r| r.as_array().unwrap().is_empty()), "{schema}");
    assert_eq!(schema["properties"]["files"]["items"]["required"], json!(["path"]));
    assert_eq!(schema["properties"]["options"]["items"]["required"], json!(["label"]));
    let text = schema.to_string();
    assert!(!text.contains("oneOf") && !text.contains("anyOf") && !text.contains("$ref"), "{text}");
}
