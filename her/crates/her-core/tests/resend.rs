//! Starting again from an earlier message (`Op::Resend`), end to end: a scripted model per
//! session, real tools, real session logs (docs/kernel/message-resend-design.md).

use async_trait::async_trait;
use her_core::BotIdentity;
use her_core::Config;
use her_core::HerHome;
use her_core::Hooks;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::KernelDeps;
use her_core::ResumeOptions;
use her_core::SessionRef;
use her_core::SessionStore;
use her_core::StartOptions;
use her_core::TurnDecision;
use her_core::store::EntryKind;
use her_core::store::TaskStatus;
use her_llm::AssistantMessage;
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
use her_protocol::AgentStatus;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::Op;
use her_protocol::PermissionMode;
use her_protocol::PromptMode;
use her_protocol::WarningCode;
use her_tools::BashTool;
use her_tools::LocalEnvironment;
use her_tools::builtin_registry;
use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;
use std::collections::VecDeque;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::Ordering;
use std::time::Duration;
use std::time::Instant;
use tempfile::TempDir;
use tokio::sync::Notify;

const ROOT: &str = "root";
const LONG: Duration = Duration::from_secs(30);

#[derive(Default)]
struct Lanes(Mutex<HashMap<String, VecDeque<Script>>>);

/// The session a request comes from: a sub-agent's name, or `root`.
fn lane_of(context: &Context) -> String {
    let first = context.messages.iter().find_map(|m| match m {
        Message::User(u) => Some(u.text_content()),
        _ => None,
    });
    first
        .and_then(|text| {
            text.strip_prefix("You are a sub-agent named \"")
                .and_then(|rest| rest.split('"').next())
                .map(str::to_string)
        })
        .unwrap_or_else(|| ROOT.to_string())
}

struct Harness {
    kernel: Kernel,
    api: Arc<ScriptedApi>,
    lanes: Arc<Lanes>,
    work: PathBuf,
    home: HerHome,
    _dir: TempDir,
}

fn deps(api: &Arc<ScriptedApi>, work: &Path, home: &HerHome, hooks: Vec<Arc<dyn Hooks>>) -> KernelDeps {
    let mut registry = ModelRegistry::new(Catalog::default(), RegistryOptions::default());
    registry.register_api(api.clone());
    registry.insert_provider(ScriptedApi::provider());
    KernelDeps {
        llm: Arc::new(registry),
        tools: Arc::new(builtin_registry(BashTool::default())),
        env: Arc::new(LocalEnvironment::new(work.to_path_buf())),
        store: Arc::new(JsonlStore::new(home.sessions_dir())),
        hooks,
        mcp_servers: Vec::new(),
        browser_pool: None,
        docs: None,
        decks: None,
        words: None,
        routines: None,
    }
}

impl Harness {
    async fn start(mode: PermissionMode) -> Harness {
        Self::start_with(mode, Vec::new(), None).await
    }

    async fn start_with(mode: PermissionMode, hooks: Vec<Arc<dyn Hooks>>, bot: Option<BotIdentity>) -> Harness {
        let dir = tempfile::tempdir().unwrap();
        let work = dir.path().join("work");
        std::fs::create_dir_all(&work).unwrap();
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        let api = ScriptedApi::new();
        let lanes = Arc::new(Lanes::default());
        let routed = lanes.clone();
        api.respond_with(move |context| routed.0.lock().get_mut(&lane_of(context))?.pop_front());
        let options = StartOptions {
            model: "test/scripted".into(),
            thinking: ThinkingLevel::Off,
            permission_mode: mode,
            cwd: work.clone(),
            home: home.clone(),
            config: Arc::new(Config::default()),
            project_root: None,
            trusted: false,
            interactive_approvals: true,
            bot,
        };
        let kernel = Kernel::start(options, deps(&api, &work, &home, hooks)).await.unwrap();
        Harness { kernel, api, lanes, work, home, _dir: dir }
    }

    /// What the model answers next, in the main session.
    fn answer(&self, script: Script) {
        self.script(ROOT, script);
    }

    fn script(&self, lane: &str, script: Script) {
        self.lanes.0.lock().entry(lane.to_string()).or_default().push_back(script);
    }

    /// Requests of the main session, oldest first.
    fn requests(&self) -> Vec<Context> {
        self.api.calls().into_iter().filter(|call| lane_of(&call.context) == ROOT).map(|call| call.context).collect()
    }

    async fn submit(&self, op: Op) {
        self.kernel.submit(op).await.unwrap();
    }

    async fn prompt(&self, text: &str) {
        self.submit(Op::prompt(text)).await;
    }

    async fn resend(&self, message_id: &str, text: &str) {
        self.submit(resend_op(message_id, text)).await;
    }

    async fn until(&mut self, stop: impl Fn(&EventMsg) -> bool) -> Vec<Event> {
        let mut events = Vec::new();
        let deadline = Instant::now() + Duration::from_secs(20);
        loop {
            let remaining = deadline.saturating_duration_since(Instant::now());
            let event = tokio::time::timeout(remaining, self.kernel.next_event())
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

    async fn until_idle(&mut self) -> Vec<Event> {
        self.until(|m| matches!(m, EventMsg::Idle)).await
    }

    /// Sends a message, runs until the session is idle, and gives the message's id.
    async fn say(&mut self, text: &str) -> String {
        self.prompt(text).await;
        let events = self.until_idle().await;
        id_of(&events, text)
    }

    /// `say` while a sub-agent works: to the end of the message's task.
    async fn say_while_working(&mut self, text: &str) -> String {
        self.prompt(text).await;
        let events = self.until(EventMsg::is_task_end).await;
        id_of(&events, text)
    }

    /// What the conversation holds now, in words.
    fn conversation(&self) -> Vec<String> {
        self.kernel
            .transcript()
            .iter()
            .filter_map(|item| match item {
                her_protocol::api::TranscriptItem::Message { message, .. } => Some(words(message)),
                _ => None,
            })
            .collect()
    }

    /// The lines of the session's log, as JSON.
    fn log(&self) -> Vec<Value> {
        let text = std::fs::read_to_string(self.kernel.session_file()).unwrap();
        text.lines().map(|line| serde_json::from_str(line).unwrap()).collect()
    }

    fn store(&self) -> JsonlStore {
        JsonlStore::new(self.home.sessions_dir())
    }
}

fn resend_op(message_id: &str, text: &str) -> Op {
    Op::Resend {
        message_id: message_id.to_string(),
        input: vec![UserContent::Text { text: text.to_string() }],
        commands: true,
    }
}

fn words(message: &Message) -> String {
    match message {
        Message::User(user) => user.text_content(),
        Message::Assistant(reply) => reply.text(),
        Message::ToolResult(result) => format!("[result] {}", result.text_content()),
    }
}

fn request_words(context: &Context) -> Vec<String> {
    context.messages.iter().map(words).collect()
}

/// The id of the user's message with these words.
fn id_of(events: &[Event], text: &str) -> String {
    events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::MessageCompleted {
                message_id,
                message: Message::User(user),
                mail: None,
                ..
            } if user.text_content() == text => Some(message_id.clone()),
            _ => None,
        })
        .unwrap_or_else(|| panic!("no message {text:?} in {events:#?}"))
}

/// The events by name, in order, without the noise around them.
fn outline(events: &[Event]) -> Vec<String> {
    events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::TaskStarted { .. } => Some("task_started".to_string()),
            EventMsg::TaskCompleted { .. } => Some("task_completed".to_string()),
            EventMsg::TaskFailed { .. } => Some("task_failed".to_string()),
            EventMsg::TaskInterrupted { .. } => Some("task_interrupted".to_string()),
            EventMsg::SessionRewound { .. } => Some("session_rewound".to_string()),
            EventMsg::MessageCompleted { message, mail: Some(_), .. } => Some(format!("report: {}", words(message))),
            EventMsg::MessageCompleted { message: Message::User(user), .. } => Some(format!("user: {}", user.text_content())),
            EventMsg::Idle => Some("idle".to_string()),
            _ => None,
        })
        .collect()
}

fn rewinds(events: &[Event]) -> Vec<(String, Vec<String>, Vec<String>)> {
    events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::SessionRewound { message_id, removed_agents, returned_input } => Some((
                message_id.clone(),
                removed_agents.clone(),
                returned_input.iter().map(|m| m.text_content()).collect(),
            )),
            _ => None,
        })
        .collect()
}

fn refusals(events: &[Event]) -> Vec<String> {
    events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::Warning {
                message,
                code: Some(WarningCode::ResendRejected),
            } => Some(message.clone()),
            _ => None,
        })
        .collect()
}

fn spawn(id: &str, name: &str, task: &str) -> Script {
    Script::tool_call(id, "spawn_agent", json!({ "description": format!("{name} work"), "task": task, "name": name, "wait": false }))
}

// ---- What the model gets ----

#[tokio::test]
async fn the_model_gets_what_came_before_and_the_new_message() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(Script::text("answer one").with_usage(1_000, 10));
    h.say("one").await;
    h.answer(Script::text("answer two").with_usage(5_000, 20));
    let two = h.say("two").await;
    h.answer(Script::text("answer three").with_usage(7_000, 30));
    h.say("three").await;
    assert_eq!(h.conversation(), ["one", "answer one", "two", "answer two", "three", "answer three"]);

    h.answer(Script::text("answer TWO").with_usage(1_200, 40));
    h.resend(&two, "TWO").await;
    let events = h.until_idle().await;
    assert_eq!(outline(&events), ["session_rewound", "task_started", "user: TWO", "task_completed", "idle"]);
    assert_eq!(rewinds(&events), [(two.clone(), Vec::new(), Vec::new())]);
    assert_ne!(id_of(&events, "TWO"), two, "the new message is a message of its own");
    assert_eq!(h.conversation(), ["one", "answer one", "TWO", "answer TWO"]);

    // What came before is what the model got then, to the byte: the prompt cache can still serve it.
    let requests = h.requests();
    let (then, now) = (&requests[1], &requests[3]);
    assert_eq!(request_words(then), ["one", "answer one", "two"]);
    assert_eq!(request_words(now), ["one", "answer one", "TWO"]);
    assert_eq!(serde_json::to_string(&now.messages[..2]).unwrap(), serde_json::to_string(&then.messages[..2]).unwrap());
    assert_eq!(now.system, then.system);
    assert_eq!(serde_json::to_string(&now.tools).unwrap(), serde_json::to_string(&then.tools).unwrap());

    // The context is measured from the answer now last on the branch, not from the later ones.
    let context = events.iter().find_map(|e| match &e.msg {
        EventMsg::TurnStarted { context, .. } => *context,
        _ => None,
    });
    let tokens = context.unwrap().tokens;
    assert!((1_010..5_000).contains(&tokens), "{tokens}");

    // The log keeps everything; a branch entry continues from where the message was.
    let log = h.log();
    let entry = |id: &str| log.iter().find(|line| line["id"] == id).unwrap().clone();
    let marker = log.iter().find(|line| line["type"] == "branch").unwrap();
    assert_eq!(marker["parent"], entry(&two)["parent"]);
    assert_eq!(marker.get("before_first"), None);
    let texts: Vec<&str> = log
        .iter()
        .filter(|line| line["type"] == "message" && line["message"]["role"] == "user")
        .map(|line| line["message"]["content"][0]["text"].as_str().unwrap())
        .collect();
    assert_eq!(texts, ["one", "two", "three", "TWO"], "nothing is removed from the file");
}

#[tokio::test]
async fn a_message_sent_again_can_be_taken_back_again() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(Script::text("answer one"));
    let one = h.say("one").await;
    h.answer(Script::text("answer again"));
    h.resend(&one, "again").await;
    let events = h.until_idle().await;
    let again = id_of(&events, "again");
    // The same words, unchanged: answering them once more.
    h.answer(Script::text("answer once more"));
    h.resend(&again, "again").await;
    h.until_idle().await;
    assert_eq!(h.conversation(), ["again", "answer once more"]);
    assert_eq!(request_words(h.requests().last().unwrap()), ["again"]);
    // The old id is gone with its message.
    h.resend(&one, "too late").await;
    let events = h.until(|m| matches!(m, EventMsg::Warning { .. })).await;
    assert_eq!(refusals(&events).len(), 1);
    assert_eq!(h.conversation(), ["again", "answer once more"]);
}

#[tokio::test]
async fn going_back_before_the_first_message_changes_what_lists_show() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(Script::text("answer"));
    let first = h.say("the first").await;
    h.answer(Script::text("answer"));
    h.say("the second").await;
    let file = SessionRef::Path(h.kernel.session_file().to_path_buf());
    assert_eq!(h.store().summary(&file).unwrap().first_message.as_deref(), Some("the first"));

    h.answer(Script::text("answer FIRST"));
    h.resend(&first, "FIRST").await;
    h.until_idle().await;
    assert_eq!(h.store().summary(&file).unwrap().first_message.as_deref(), Some("FIRST"));
    let log = h.log();
    let marker = log.iter().find(|line| line["type"] == "branch").unwrap();
    assert_eq!(marker["before_first"], true);
    assert_eq!(request_words(h.requests().last().unwrap()), ["FIRST"]);
}

#[tokio::test]
async fn a_message_that_was_interjected_can_be_taken_back_too() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(Script::tool_call("c1", "bash", json!({ "command": "sleep 0.3; echo done" })));
    h.answer(Script::text("after the interjection"));
    h.prompt("one").await;
    h.until(|m| matches!(m, EventMsg::ToolRunning { .. })).await;
    h.submit(Op::Prompt {
        input: vec![UserContent::Text { text: "by the way".into() }],
        mode: PromptMode::Steer,
        commands: true,
    })
    .await;
    let events = h.until_idle().await;
    let interjected = id_of(&events, "by the way");
    h.answer(Script::text("answer"));
    h.resend(&interjected, "on second thoughts").await;
    h.until_idle().await;
    // A request as valid as any: the call, its result, then the new message.
    let last = request_words(h.requests().last().unwrap());
    assert_eq!(last.len(), 4, "{last:?}");
    assert!(last[2].starts_with("[result]") && last[2].contains("done"), "{last:?}");
    assert_eq!((last[0].as_str(), last[3].as_str()), ("one", "on second thoughts"));
    assert_eq!(h.conversation().last().map(String::as_str), Some("answer"));
    assert!(!h.conversation().contains(&"by the way".to_string()));
}

#[tokio::test]
async fn a_failed_task_can_be_tried_again_with_other_words() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(Script::text("answer one"));
    h.say("one").await;
    h.answer(Script::fail(ErrorKind::Auth, "bad key"));
    h.prompt("two").await;
    let events = h.until_idle().await;
    assert!(events.iter().any(|e| matches!(e.msg, EventMsg::TaskFailed { .. })));
    let two = id_of(&events, "two");
    h.answer(Script::text("answer TWO"));
    h.resend(&two, "TWO").await;
    h.until_idle().await;
    assert_eq!(h.conversation(), ["one", "answer one", "TWO", "answer TWO"]);
}

// ---- A task that runs ----

#[tokio::test]
async fn a_reply_being_written_stops_first() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(Script::text("answer one"));
    h.say("one").await;
    h.answer(Script::text("partial").then_sleep(LONG).then_text("never"));
    h.prompt("two").await;
    let events = h.until(|m| matches!(m, EventMsg::MessageDelta { .. })).await;
    let two = id_of(&events, "two");

    let started = Instant::now();
    h.answer(Script::text("answer TWO"));
    h.resend(&two, "TWO").await;
    let events = h.until_idle().await;
    assert!(started.elapsed() < Duration::from_secs(5));
    assert_eq!(outline(&events), ["task_interrupted", "session_rewound", "task_started", "user: TWO", "task_completed", "idle"]);
    assert_eq!(h.conversation(), ["one", "answer one", "TWO", "answer TWO"]);
    assert_eq!(request_words(h.requests().last().unwrap()), ["one", "answer one", "TWO"]);
}

#[tokio::test]
async fn a_running_command_stops_first() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(Script::tool_call("c1", "bash", json!({ "command": "sleep 30" })));
    h.prompt("one").await;
    let events = h.until(|m| matches!(m, EventMsg::ToolRunning { .. })).await;
    let one = id_of(&events, "one");
    let started = Instant::now();
    h.answer(Script::text("answer ONE"));
    h.resend(&one, "ONE").await;
    h.until_idle().await;
    assert!(started.elapsed() < Duration::from_secs(5));
    assert_eq!(h.conversation(), ["ONE", "answer ONE"]);
    assert_eq!(request_words(h.requests().last().unwrap()), ["ONE"]);
}

#[tokio::test]
async fn a_call_waiting_for_approval_never_runs() {
    let mut h = Harness::start(PermissionMode::Manual).await;
    h.answer(Script::tool_call("c1", "bash", json!({ "command": "touch made.txt" })));
    h.prompt("one").await;
    let events = h.until(|m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let one = id_of(&events, "one");
    h.answer(Script::text("answer ONE"));
    h.resend(&one, "ONE").await;
    h.until_idle().await;
    assert!(!h.work.join("made.txt").exists());
    assert_eq!(h.conversation(), ["ONE", "answer ONE"]);
}

#[tokio::test]
async fn what_waits_goes_back_to_the_composer_and_never_to_the_model() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(Script::text("partial").then_sleep(LONG).then_text("never"));
    h.prompt("one").await;
    let events = h.until(|m| matches!(m, EventMsg::MessageDelta { .. })).await;
    let one = id_of(&events, "one");
    for (text, mode) in [("next step", PromptMode::Steer), ("afterwards", PromptMode::FollowUp)] {
        h.submit(Op::Prompt {
            input: vec![UserContent::Text { text: text.into() }],
            mode,
            commands: true,
        })
        .await;
    }
    h.until(|m| matches!(m, EventMsg::QueueChanged { queued } if queued.len() == 2)).await;

    h.answer(Script::text("answer ONE"));
    h.resend(&one, "ONE").await;
    let events = h.until_idle().await;
    assert_eq!(rewinds(&events), [(one, Vec::new(), vec!["next step".to_string(), "afterwards".to_string()])]);
    let interrupted = events.iter().find_map(|e| match &e.msg {
        EventMsg::TaskInterrupted { returned_input, .. } => Some(returned_input.len()),
        _ => None,
    });
    assert_eq!(interrupted, Some(0), "told once, with the rewind");
    let emptied = events.iter().any(|e| matches!(&e.msg, EventMsg::QueueChanged { queued } if queued.is_empty()));
    assert!(emptied);
    assert_eq!(request_words(h.requests().last().unwrap()), ["ONE"]);
    assert_eq!(h.conversation(), ["ONE", "answer ONE"]);
}

/// Holds the end of a turn while `armed`: a `resend` then arrives after the task's last look at
/// its cancellation, and the task ends as it would have.
#[derive(Default)]
struct HoldTurnEnd {
    armed: AtomicBool,
    reached: Notify,
    release: Notify,
}

#[async_trait]
impl Hooks for HoldTurnEnd {
    async fn finish_turn(&self, _message: &AssistantMessage) -> TurnDecision {
        if self.armed.swap(false, Ordering::SeqCst) {
            self.reached.notify_one();
            self.release.notified().await;
        }
        TurnDecision::Default
    }
}

#[tokio::test]
async fn a_task_that_completes_as_the_resend_arrives_still_hands_back_what_waits() {
    let hold = Arc::new(HoldTurnEnd::default());
    let mut h = Harness::start_with(PermissionMode::BypassPermissions, vec![hold.clone()], None).await;
    h.answer(Script::text("answer one"));
    let one = h.say("one").await;
    hold.armed.store(true, Ordering::SeqCst);
    h.answer(Script::text("answer two"));
    h.prompt("two").await;
    hold.reached.notified().await;
    h.submit(Op::Prompt {
        input: vec![UserContent::Text { text: "afterwards".into() }],
        mode: PromptMode::FollowUp,
        commands: true,
    })
    .await;
    h.until(|m| matches!(m, EventMsg::QueueChanged { queued } if queued.len() == 1)).await;
    h.answer(Script::text("answer ONE"));
    h.resend(&one, "ONE").await;
    // The op is handled in order after the prompt above; a second look proves it took effect.
    h.resend(&one, "ONE").await;
    let refused = h.until(|m| matches!(m, EventMsg::Warning { .. })).await;
    assert_eq!(refusals(&refused).len(), 1, "one resend at a time");
    hold.release.notify_one();
    let events = h.until_idle().await;
    assert_eq!(outline(&events), ["task_completed", "session_rewound", "task_started", "user: ONE", "task_completed", "idle"]);
    assert_eq!(rewinds(&events), [(one, Vec::new(), vec!["afterwards".to_string()])]);
    assert_eq!(h.conversation(), ["ONE", "answer ONE"]);
    assert_eq!(h.requests().len(), 3, "what waited did not start a task");
}

#[tokio::test]
async fn interjected_input_does_not_slip_in_while_the_task_stops() {
    let hold = Arc::new(HoldTurnEnd::default());
    let mut h = Harness::start_with(PermissionMode::BypassPermissions, vec![hold.clone()], None).await;
    hold.armed.store(true, Ordering::SeqCst);
    h.answer(Script::text("answer one"));
    h.prompt("one").await;
    hold.reached.notified().await;
    let events = h.until(|m| matches!(m, EventMsg::TurnCompleted { .. })).await;
    let one = id_of(&events, "one");
    // At the boundary it would otherwise join the history, and go with what is taken back.
    h.submit(Op::Prompt {
        input: vec![UserContent::Text { text: "by the way".into() }],
        mode: PromptMode::Steer,
        commands: true,
    })
    .await;
    h.until(|m| matches!(m, EventMsg::QueueChanged { queued } if queued.len() == 1)).await;
    h.answer(Script::text("answer ONE"));
    h.resend(&one, "ONE").await;
    // Handled after the prompt above once this is refused: the resend waits.
    h.resend(&one, "ONE").await;
    h.until(|m| matches!(m, EventMsg::Warning { .. })).await;
    hold.release.notify_one();
    let events = h.until_idle().await;
    assert_eq!(rewinds(&events), [(one, Vec::new(), vec!["by the way".to_string()])]);
    assert!(!outline(&events).contains(&"user: by the way".to_string()));
    assert_eq!(h.conversation(), ["ONE", "answer ONE"]);
}

#[tokio::test]
async fn a_resend_still_waiting_when_the_session_closes_does_not_go_ahead() {
    let hold = Arc::new(HoldTurnEnd::default());
    let mut h = Harness::start_with(PermissionMode::BypassPermissions, vec![hold.clone()], None).await;
    h.answer(Script::text("answer one"));
    let one = h.say("one").await;
    hold.armed.store(true, Ordering::SeqCst);
    h.answer(Script::text("answer two"));
    h.prompt("two").await;
    hold.reached.notified().await;
    h.answer(Script::text("never asked"));
    h.resend(&one, "ONE").await;
    // Refused while the first waits: it has been handled.
    h.resend(&one, "ONE").await;
    h.until(|m| matches!(m, EventMsg::Warning { .. })).await;

    let file = h.kernel.session_file().to_path_buf();
    let requests = h.requests().len();
    let release = hold.clone();
    tokio::spawn(async move {
        tokio::time::sleep(Duration::from_millis(100)).await;
        release.release.notify_one();
    });
    let Harness { kernel, api, .. } = h;
    kernel.shutdown().await;
    tokio::time::sleep(Duration::from_millis(100)).await;
    let asked = api.calls().into_iter().filter(|call| lane_of(&call.context) == ROOT).count();
    assert_eq!(asked, requests, "no task started as the session closed");
    let text = std::fs::read_to_string(file).unwrap();
    assert!(!text.contains(r#""type":"branch""#), "nothing went back");
}

// ---- Compaction ----

#[tokio::test]
async fn going_back_before_a_compaction_undoes_it_and_after_it_keeps_it() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(Script::text("answer one"));
    h.say("one").await;
    h.answer(Script::text("answer two"));
    let two = h.say("two").await;
    h.answer(Script::text("the summary"));
    h.submit(Op::Compact).await;
    h.until_idle().await;
    h.answer(Script::text("answer three"));
    let three = h.say("three").await;
    let last = request_words(h.requests().last().unwrap());
    assert!(last.iter().any(|m| m.contains("the summary")), "{last:?}");

    // After it: the checkpoint stays.
    h.answer(Script::text("answer THREE"));
    h.resend(&three, "THREE").await;
    h.until_idle().await;
    let mut expected = last;
    *expected.last_mut().unwrap() = "THREE".to_string();
    assert_eq!(request_words(h.requests().last().unwrap()), expected);

    // Before it: as if it never happened.
    h.answer(Script::text("answer TWO"));
    h.resend(&two, "TWO").await;
    h.until_idle().await;
    assert_eq!(request_words(h.requests().last().unwrap()), ["one", "answer one", "TWO"]);
}

#[tokio::test]
async fn back_to_a_context_too_large_it_compacts_again_first() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    // The scripted model's window is 128,000: compaction starts at 107,827.
    h.answer(Script::text("a long answer").with_usage(110_000, 10));
    h.say("one").await;
    h.answer(Script::text("the first summary"));
    h.answer(Script::text("answer two"));
    h.prompt("two").await;
    let events = h.until_idle().await;
    let two = id_of(&events, "two");
    assert!(events.iter().any(|e| matches!(e.msg, EventMsg::CompactionCompleted { .. })));

    h.answer(Script::text("the second summary"));
    h.answer(Script::text("answer TWO"));
    h.resend(&two, "TWO").await;
    let events = h.until_idle().await;
    assert!(events.iter().any(|e| matches!(e.msg, EventMsg::CompactionCompleted { .. })));
    let last = request_words(h.requests().last().unwrap());
    assert!(last.iter().any(|m| m.contains("the second summary")), "{last:?}");
    assert!(!last.iter().any(|m| m.contains("the first summary")), "{last:?}");
    assert!(last.iter().any(|m| m.contains("TWO")), "{last:?}");
    assert!(!last.iter().any(|m| m == "two"), "{last:?}");
}

// ---- Sub-agents ----

#[tokio::test]
async fn an_agent_started_in_what_is_taken_back_stops_and_leaves() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(Script::text("answer one"));
    h.say("one").await;
    h.answer(spawn("c1", "worker", "Work for long."));
    h.answer(Script::text("started it"));
    h.script("worker", Script::text("working").then_sleep(LONG).then_text("never"));
    h.prompt("two").await;
    let events = h.until(EventMsg::is_task_end).await;
    let two = id_of(&events, "two");
    let (agent, log) = events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::AgentStarted { agent_id, session_file, .. } => Some((agent_id.clone(), session_file.clone())),
            _ => None,
        })
        .unwrap();

    h.answer(Script::text("answer TWO"));
    h.resend(&two, "TWO").await;
    // Idle at once: the session no longer waits for it.
    let events = h.until_idle().await;
    assert_eq!(rewinds(&events), [(two, vec![agent], Vec::new())]);
    assert!(!outline(&events).iter().any(|e| e.starts_with("report")));
    assert_eq!(h.conversation(), ["one", "answer one", "TWO", "answer TWO"]);

    // Its session stopped, and says so.
    let store = h.store();
    let stopped = || {
        store.read(&SessionRef::Path(log.clone())).is_ok_and(|(_, entries)| {
            entries.iter().rev().find_map(|e| match &e.kind {
                EntryKind::Task { status, .. } => Some(*status),
                _ => None,
            }) == Some(TaskStatus::Interrupted)
        })
    };
    let deadline = Instant::now() + Duration::from_secs(5);
    while !stopped() {
        assert!(Instant::now() < deadline, "the agent's session did not stop");
        tokio::time::sleep(Duration::from_millis(20)).await;
    }

    // Its name is free again.
    h.answer(spawn("c2", "worker", "Work again."));
    h.answer(Script::text("started it again"));
    h.answer(Script::text("read the report"));
    h.script("worker", Script::text("the result"));
    h.prompt("three").await;
    let events = h.until_idle().await;
    let names: Vec<&str> = events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::AgentStarted { name, .. } => Some(name.as_str()),
            _ => None,
        })
        .collect();
    assert_eq!(names, ["worker"]);
}

#[tokio::test]
async fn reopened_after_going_back_only_the_agents_of_the_conversation_are_there() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(spawn("c1", "kept", "Work for a moment."));
    h.answer(Script::text("started it"));
    h.answer(Script::text("read its report"));
    h.script("kept", Script::text("kept result"));
    h.prompt("one").await;
    h.until_idle().await;
    h.answer(spawn("c2", "gone", "Work for a moment."));
    h.answer(Script::text("started it too"));
    h.answer(Script::text("read that report"));
    h.script("gone", Script::text("gone result"));
    h.prompt("two").await;
    let events = h.until_idle().await;
    let two = id_of(&events, "two");
    h.answer(Script::text("answer TWO"));
    h.resend(&two, "TWO").await;
    let events = h.until_idle().await;
    assert_eq!(rewinds(&events).len(), 1);
    assert_eq!(rewinds(&events)[0].1.len(), 1, "the one started after that message");

    let session = h.kernel.session_id().to_string();
    let Harness { kernel, api, lanes: _lanes, work, home, _dir } = h;
    kernel.shutdown().await;
    let options = ResumeOptions {
        session: SessionRef::Id(session),
        model: None,
        thinking: None,
        permission_mode: None,
        home: home.clone(),
        config: Arc::new(Config::default()),
        project_root: None,
        trusted: false,
        interactive_approvals: true,
        bot_name: None,
    };
    let mut kernel = Kernel::resume(options, deps(&api, &work, &home, Vec::new())).await.unwrap();
    let names: Vec<String> = kernel.take_restored_agents().into_iter().map(|a| a.name).collect();
    assert_eq!(names, ["kept"]);
    kernel.shutdown().await;
}

#[tokio::test]
async fn an_agent_started_earlier_works_on_and_its_report_comes() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(spawn("c1", "worker", "Work for a while."));
    h.answer(Script::text("started it"));
    h.script("worker", Script::text("the result").then_sleep(Duration::from_millis(600)));
    h.prompt("one").await;
    h.until(EventMsg::is_task_end).await;
    h.answer(Script::text("answer two"));
    let two = h.say_while_working("two").await;

    h.answer(Script::text("answer TWO"));
    h.answer(Script::text("read the report"));
    h.resend(&two, "TWO").await;
    let events = h.until_idle().await;
    assert_eq!(rewinds(&events), [(two, Vec::new(), Vec::new())]);
    assert!(outline(&events).iter().any(|e| e.starts_with("report") && e.contains("the result")));
    let last = request_words(h.requests().last().unwrap());
    assert!(last.iter().any(|m| m.contains("the result")), "{last:?}");
    assert!(last.contains(&"TWO".to_string()) && !last.contains(&"two".to_string()), "{last:?}");
}

#[tokio::test]
async fn a_report_delivered_in_what_is_taken_back_comes_again() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(spawn("c1", "worker", "Work for a moment."));
    h.answer(Script::text("started it"));
    h.script("worker", Script::text("the result").then_sleep(Duration::from_millis(200)));
    h.prompt("one").await;
    h.until(EventMsg::is_task_end).await;
    // The report joins at the boundary after this command, in the task "two" started.
    h.answer(Script::tool_call("c2", "bash", json!({ "command": "sleep 1" })));
    h.answer(Script::text("answer two"));
    h.prompt("two").await;
    let events = h.until_idle().await;
    let two = id_of(&events, "two");
    let outlined = outline(&events);
    let report = outlined.iter().position(|e| e.starts_with("report")).expect("the report came");
    assert!(report > outlined.iter().position(|e| e == "user: two").unwrap(), "{outlined:?}");
    // A report is not the user's to take back.
    let mail = events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::MessageCompleted { message_id, mail: Some(_), .. } => Some(message_id.clone()),
            _ => None,
        })
        .unwrap();
    h.resend(&mail, "no").await;
    let refused = h.until(|m| matches!(m, EventMsg::Warning { .. })).await;
    assert_eq!(refusals(&refused).len(), 1);

    let before = h.requests().len();
    h.answer(Script::text("answer TWO"));
    h.resend(&two, "TWO").await;
    let events = h.until_idle().await;
    assert_eq!(rewinds(&events), [(two, Vec::new(), Vec::new())]);
    // Once, before the new message, and it wakes nothing of its own.
    let last = request_words(h.requests().last().unwrap());
    assert_eq!((last.first().map(String::as_str), last.last().map(String::as_str)), (Some("one"), Some("TWO")), "{last:?}");
    assert!(!last.iter().any(|m| m == "two" || m == "answer two"), "{last:?}");
    let reports = last.iter().filter(|m| m.starts_with("<agent_report") && m.contains("the result")).count();
    assert_eq!(reports, 1, "{last:?}");
    assert_eq!(last[last.len() - 2], last.iter().find(|m| m.starts_with("<agent_report")).unwrap().clone(), "right before the new message");
    assert_eq!(h.requests().len(), before + 1);
    assert!(outline(&events).iter().position(|e| e.starts_with("report")) < outline(&events).iter().position(|e| e == "user: TWO"));
}

#[tokio::test]
async fn a_waiting_report_from_an_agent_taken_back_never_arrives() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(Script::text("answer one"));
    h.say("one").await;
    h.answer(spawn("c1", "worker", "Work for a moment."));
    // The agent reports while this reply is written: no boundary takes the report in.
    h.answer(Script::text("partial").then_sleep(LONG).then_text("never"));
    h.script("worker", Script::text("the result"));
    h.prompt("two").await;
    let events = h.until(|m| matches!(m, EventMsg::AgentStatus { status: AgentStatus::Completed, .. })).await;
    let two = id_of(&events, "two");
    let agent = events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::AgentStarted { agent_id, .. } => Some(agent_id.clone()),
            _ => None,
        })
        .unwrap();

    h.answer(Script::text("answer TWO"));
    h.resend(&two, "TWO").await;
    let events = h.until_idle().await;
    assert_eq!(rewinds(&events), [(two, vec![agent], Vec::new())]);
    assert!(!outline(&events).iter().any(|e| e.starts_with("report")));
    let last = request_words(h.requests().last().unwrap());
    assert_eq!(last, ["one", "answer one", "TWO"]);
}

// ---- What the session keeps about the conversation ----

#[tokio::test]
async fn a_file_read_only_in_what_is_taken_back_must_be_read_again_before_a_write() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    std::fs::write(h.work.join("a.txt"), "old a").unwrap();
    std::fs::write(h.work.join("b.txt"), "old b").unwrap();
    h.answer(Script::tool_call("c1", "read", json!({ "path": "a.txt" })).then_tool_call("c2", "read", json!({ "path": "b.txt" })));
    h.answer(Script::text("read them"));
    h.prompt("one").await;
    let events = h.until_idle().await;
    let one = id_of(&events, "one");
    // Having read it, it may write it.
    h.answer(Script::tool_call("c3", "write", json!({ "path": "b.txt", "content": "new b" })));
    h.answer(Script::text("wrote it"));
    h.prompt("two").await;
    h.until_idle().await;
    assert_eq!(std::fs::read_to_string(h.work.join("b.txt")).unwrap(), "new b");

    h.answer(Script::tool_call("c4", "write", json!({ "path": "a.txt", "content": "new a" })));
    h.answer(Script::text("could not"));
    h.resend(&one, "ONE").await;
    h.until_idle().await;
    assert_eq!(std::fs::read_to_string(h.work.join("a.txt")).unwrap(), "old a");
    let result = h.requests().last().unwrap().messages.iter().find_map(|m| match m {
        Message::ToolResult(result) => Some((result.is_error, result.text_content())),
        _ => None,
    });
    let (is_error, text) = result.unwrap();
    assert!(is_error, "{text}");
    assert!(text.contains("read"), "{text}");
    // Files stay as they are: going back takes back the conversation.
    assert_eq!(std::fs::read_to_string(h.work.join("b.txt")).unwrap(), "new b");
}

#[tokio::test]
async fn a_resumed_session_has_the_new_branch_and_all_that_belongs_to_the_session() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(Script::text("answer one").with_usage(100, 10));
    h.say("one").await;
    h.answer(Script::text("answer two").with_usage(200, 20));
    let two = h.say("two").await;
    // These come after the point gone back to, and stand.
    h.submit(Op::SetTitle { title: "Named".into() }).await;
    h.submit(Op::SetPermissionMode { mode: PermissionMode::Manual }).await;
    h.until(|m| matches!(m, EventMsg::SettingsChanged { .. })).await;
    h.answer(Script::text("answer TWO").with_usage(300, 30));
    h.resend(&two, "TWO").await;
    h.until_idle().await;
    assert_eq!(h.kernel.info().permission_mode, PermissionMode::Manual);

    let session = h.kernel.session_id().to_string();
    let Harness { kernel, api, lanes, work, home, _dir } = h;
    kernel.shutdown().await;
    let options = ResumeOptions {
        session: SessionRef::Id(session),
        model: None,
        thinking: None,
        permission_mode: None,
        home: home.clone(),
        config: Arc::new(Config::default()),
        project_root: None,
        trusted: false,
        interactive_approvals: true,
        bot_name: None,
    };
    let kernel = Kernel::resume(options, deps(&api, &work, &home, Vec::new())).await.unwrap();
    let mut h = Harness { kernel, api, lanes, work, home, _dir };

    let events = h.until(|m| matches!(m, EventMsg::SessionStarted { .. })).await;
    let EventMsg::SessionStarted { permission_mode, usage, resumed, .. } = &events.last().unwrap().msg else {
        unreachable!()
    };
    assert!(resumed);
    assert_eq!(*permission_mode, PermissionMode::Manual, "tightened after that point, and still tightened");
    let spent = &usage.per_model["test/scripted"];
    assert_eq!((spent.input, spent.output), (600, 60), "what the part taken back cost was paid");
    assert_eq!(h.kernel.info().title.as_deref(), Some("Named"));
    assert_eq!(h.conversation(), ["one", "answer one", "TWO", "answer TWO"]);

    // Task numbers go on past the ones taken back.
    h.answer(Script::text("answer three"));
    h.prompt("three").await;
    let events = h.until_idle().await;
    let task = events.iter().find_map(|e| match &e.msg {
        EventMsg::TaskStarted { task_id, .. } => Some(task_id.clone()),
        _ => None,
    });
    assert_eq!(task.as_deref(), Some("t4"));
    assert_eq!(request_words(h.requests().last().unwrap()), ["one", "answer one", "TWO", "answer TWO", "three"]);
}

// ---- What is refused ----

#[tokio::test]
async fn what_cannot_go_ahead_is_refused_and_changes_nothing() {
    let mut h = Harness::start(PermissionMode::BypassPermissions).await;
    h.answer(Script::text("answer one"));
    h.prompt("one").await;
    let events = h.until_idle().await;
    let one = id_of(&events, "one");
    let reply = events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::MessageCompleted {
                message_id,
                message: Message::Assistant(_),
                ..
            } => Some(message_id.clone()),
            _ => None,
        })
        .unwrap();
    let lines = h.log().len();
    let refused = [
        resend_op("nothing-like-it", "x"),
        resend_op(&reply, "x"),
        resend_op(&one, "  "),
        Op::Resend { message_id: one.clone(), input: Vec::new(), commands: true },
        resend_op(&one, "/compact"),
        resend_op(&one, "/context"),
    ];
    let count = refused.len();
    for op in refused {
        h.submit(op).await;
        let events = h.until(|m| matches!(m, EventMsg::Warning { .. })).await;
        assert_eq!(refusals(&events).len(), 1, "{events:#?}");
        assert!(rewinds(&events).is_empty());
    }
    assert_eq!(h.conversation(), ["one", "answer one"]);
    assert_eq!(h.log().len(), lines, "nothing went into the log after {count} refusals");
    assert_eq!(h.requests().len(), 1);

    // Sent as words rather than as a command, the same text goes through.
    h.answer(Script::text("answer"));
    h.submit(Op::Resend {
        message_id: one,
        input: vec![UserContent::Text { text: "/compact".into() }],
        commands: false,
    })
    .await;
    h.until_idle().await;
    assert_eq!(h.conversation(), ["/compact", "answer"]);
}

#[tokio::test]
async fn a_bots_conversation_does_not_go_back() {
    let bot = BotIdentity { id: "b1".into(), name: "Her".into() };
    let mut h = Harness::start_with(PermissionMode::BypassPermissions, Vec::new(), Some(bot)).await;
    h.answer(Script::tool_call("c1", "send_to_user", json!({ "text": "hi" })));
    h.prompt("one").await;
    let events = h.until_idle().await;
    let one = id_of(&events, "one");
    let lines = h.log().len();
    h.resend(&one, "ONE").await;
    let events = h.until(|m| matches!(m, EventMsg::Warning { .. })).await;
    assert_eq!(refusals(&events).len(), 1);
    assert_eq!(h.log().len(), lines);
}
