//! Sub-agents end to end: a scripted model per session, real tools, real session logs.
//!
//! Sessions run concurrently, so each one gets its own queue of scripts ("lane"), picked by
//! the first message of the request: a sub-agent's starts with its name.

use her_core::Config;
use her_core::HerHome;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::KernelDeps;
use her_core::SessionRef;
use her_core::SessionStore;
use her_core::StartOptions;
use her_core::store::EntryKind;
use her_llm::Catalog;
use her_llm::Context;
use her_llm::Message;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use her_llm::ThinkingLevel;
use her_llm::api::scripted::Script;
use her_llm::api::scripted::ScriptedApi;
use her_protocol::AgentStatus;
use her_protocol::ApprovalDecision;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::MailKind;
use her_protocol::Op;
use her_protocol::PermissionMode;
use her_protocol::TaskTrigger;
use her_tools::BashTool;
use her_tools::LocalEnvironment;
use her_tools::builtin_registry;
use parking_lot::Mutex;
use serde_json::json;
use std::collections::HashMap;
use std::collections::VecDeque;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use tempfile::TempDir;

const ROOT: &str = "root";

#[derive(Default)]
struct Lanes(Mutex<HashMap<String, VecDeque<Script>>>);

impl Lanes {
    fn push(&self, lane: &str, script: Script) {
        self.0.lock().entry(lane.to_string()).or_default().push_back(script);
    }
}

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

impl Harness {
    async fn start(mode: PermissionMode, interactive: bool) -> Harness {
        let dir = tempfile::tempdir().unwrap();
        let work = dir.path().join("work");
        std::fs::create_dir_all(&work).unwrap();
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        let api = ScriptedApi::new();
        let lanes = Arc::new(Lanes::default());
        let routed = lanes.clone();
        api.respond_with(move |context| routed.0.lock().get_mut(&lane_of(context))?.pop_front());
        let deps = deps(&api, &work, &home);
        let options = StartOptions {
            model: "test/scripted".into(),
            thinking: ThinkingLevel::Off,
            permission_mode: mode,
            cwd: work.clone(),
            home: home.clone(),
            config: Arc::new(Config::default()),
            project_root: None,
            trusted: false,
            interactive_approvals: interactive,
            bot: None,
        };
        let kernel = Kernel::start(options, deps).await.unwrap();
        Harness {
            kernel,
            api,
            lanes,
            work,
            home,
            _dir: dir,
        }
    }

    fn script(&self, lane: &str, script: Script) {
        self.lanes.push(lane, script);
    }

    /// Requests from one session, oldest first.
    fn requests(&self, lane: &str) -> Vec<Context> {
        self.api
            .calls()
            .into_iter()
            .filter(|call| lane_of(&call.context) == lane)
            .map(|call| call.context)
            .collect()
    }

    async fn prompt(&mut self, text: &str) {
        self.kernel.submit(Op::prompt(text)).await.unwrap();
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

    fn store(&self) -> JsonlStore {
        JsonlStore::new(self.home.sessions_dir())
    }
}

fn deps(api: &Arc<ScriptedApi>, work: &std::path::Path, home: &HerHome) -> KernelDeps {
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

fn spawn(id: &str, name: &str, task: &str, wait: bool) -> Script {
    Script::tool_call(id, "spawn_agent", json!({ "description": format!("{name} work"), "task": task, "name": name, "wait": wait }))
}

fn user_texts(context: &Context) -> Vec<String> {
    context
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::User(u) => Some(u.text_content()),
            _ => None,
        })
        .collect()
}

fn tool_results(context: &Context) -> Vec<String> {
    context
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::ToolResult(r) => Some(r.text_content()),
            _ => None,
        })
        .collect()
}

fn agent_ids(events: &[Event]) -> Vec<(String, String, PathBuf)> {
    events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::AgentStarted {
                agent_id,
                name,
                session_file,
                ..
            } => Some((agent_id.clone(), name.clone(), session_file.clone())),
            _ => None,
        })
        .collect()
}

fn mails(events: &[Event]) -> Vec<(String, AgentStatus)> {
    events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::MessageCompleted { mail: Some(mail), .. } => Some((mail.from.name.clone(), mail.status)),
            _ => None,
        })
        .collect()
}

#[tokio::test]
async fn a_background_agent_reports_and_wakes_the_main_agent() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "counter", "Count the files.", false));
    h.script(ROOT, Script::text("I started an agent to count the files."));
    h.script(ROOT, Script::text("The agent counted 3 files."));
    h.script("counter", Script::text("3 files").then_sleep(Duration::from_millis(200)));
    h.prompt("how many files?").await;
    let events = h.until_idle().await;

    // The main task ends while the agent works; its report starts a second task.
    let triggers: Vec<TaskTrigger> = events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::TaskStarted { trigger, .. } => Some(*trigger),
            _ => None,
        })
        .collect();
    assert_eq!(triggers, [TaskTrigger::User, TaskTrigger::Mail]);
    assert_eq!(mails(&events), [("counter".to_string(), AgentStatus::Completed)]);
    let last = events.iter().rev().find_map(|e| match &e.msg {
        EventMsg::TaskCompleted { last_message, .. } => last_message.clone(),
        _ => None,
    });
    assert_eq!(last.as_deref(), Some("The agent counted 3 files."));
    assert!(matches!(events.last().unwrap().msg, EventMsg::Idle));

    // The tool returned at once; the report reached the model as a user message.
    let root = h.requests(ROOT);
    assert_eq!(root.len(), 3);
    assert!(tool_results(&root[1])[0].starts_with("Agent \"counter\" started in the background"));
    let report = user_texts(&root[2]).pop().unwrap();
    assert_eq!(report, "<agent_report agent=\"counter\" status=\"completed\">\n3 files\n</agent_report>");

    // The sub-agent got the task, with who it is and how to report.
    let child = h.requests("counter");
    let task = &user_texts(&child[0])[0];
    assert!(task.contains("Count the files."), "{task}");
    assert!(task.contains("Markdown report"), "{task}");
    // The same prompt, up to the catalog: the main session can also stop agents.
    let prompt = |system: &str| system.split("<deferred_tools>").next().unwrap().to_string();
    assert_eq!(prompt(&child[0].system), prompt(&root[0].system), "same system prompt");
    assert!(root[0].system.contains("- agents: See how your agents stand, or stop them"), "{}", root[0].system);
    assert!(child[0].system.contains("- agents: See the other agents in this session"), "{}", child[0].system);
    let child_tools: Vec<&str> = child[0].tools.iter().map(|t| t.name.as_str()).collect();
    assert_eq!(child_tools, ["read", "write", "edit", "bash", "send_message", "tool_search", "call_tool"], "it starts no agents");

    // The label the user sees comes with its start and stays in the main session's log.
    let label = events.iter().find_map(|e| match &e.msg {
        EventMsg::AgentStarted { description, .. } => Some(description.clone()),
        _ => None,
    });
    assert_eq!(label.as_deref(), Some("counter work"));
    let log = std::fs::read_to_string(h.kernel.session_file()).unwrap();
    assert!(log.lines().any(|l| l.contains("\"agent_spawned\"") && l.contains("\"description\":\"counter work\"")), "{log}");

    // Its session is an ordinary log that names the parent, but it is not listed.
    let (agent_id, _, file) = agent_ids(&events).pop().unwrap();
    let store = h.store();
    let (header, _) = store.header(&SessionRef::Path(file)).unwrap();
    let origin = header.agent.expect("a sub-agent's header names its parent");
    assert_eq!(origin.name, "counter");
    assert_eq!(origin.call_id, "c1");
    assert_eq!(origin.parent_id, h.kernel.session_id());
    let listed: Vec<String> = store.list(10, None).unwrap().into_iter().map(|s| s.id).collect();
    assert_eq!(listed, [h.kernel.session_id().to_string()]);
    assert_eq!(store.header(&SessionRef::Last).unwrap().0.id, h.kernel.session_id());
    assert_eq!(store.header(&SessionRef::Id(agent_id.clone())).unwrap().0.id, agent_id);
}

#[tokio::test]
async fn a_waiting_spawn_returns_the_report_as_its_result() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "solver", "Compute the answer.", true));
    h.script(ROOT, Script::text("It is 42."));
    h.script("solver", Script::text("The answer is 42."));
    h.prompt("answer?").await;
    let events = h.until_idle().await;

    let result = &tool_results(&h.requests(ROOT)[1])[0];
    assert!(result.starts_with("Agent \"solver\" completed after"), "{result}");
    assert!(result.ends_with("The answer is 42."), "{result}");
    assert!(mails(&events).is_empty(), "a waited report is not mailed too");
    let tasks = events.iter().filter(|e| matches!(e.msg, EventMsg::TaskStarted { .. })).count();
    assert_eq!(tasks, 1);
}

#[tokio::test]
async fn waiting_spawns_in_one_reply_run_in_parallel() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(
        ROOT,
        Script::tool_call("c1", "spawn_agent", json!({ "description": "a work", "task": "a", "name": "a", "wait": true }))
            .then_tool_call("c2", "spawn_agent", json!({ "description": "b work", "task": "b", "name": "b", "wait": true }))
            .then_tool_call("c3", "spawn_agent", json!({ "description": "c work", "task": "c", "name": "c", "wait": true })),
    );
    h.script(ROOT, Script::text("all done"));
    for name in ["a", "b", "c"] {
        h.script(name, Script::default().then_sleep(Duration::from_millis(500)).then_text(format!("{name} done")));
    }
    let started = Instant::now();
    h.prompt("go").await;
    h.until_idle().await;
    assert!(started.elapsed() < Duration::from_millis(1400), "took {:?}", started.elapsed());
    let results = tool_results(&h.requests(ROOT)[1]);
    assert_eq!(results.len(), 3);
    for (result, name) in results.iter().zip(["a", "b", "c"]) {
        assert!(result.ends_with(&format!("{name} done")), "{result}");
    }
}

#[tokio::test]
async fn a_message_to_a_running_agent_joins_its_next_turn() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "worker", "Do part a.", false));
    // Sent while the agent runs its command, so it arrives at the agent's next turn boundary.
    h.script(
        ROOT,
        Script::default().then_sleep(Duration::from_millis(200)).then_tool_call(
            "c2",
            "send_message",
            json!({ "to": "worker", "message": "Also do part b." }),
        ),
    );
    h.script(ROOT, Script::text("Both parts are on their way."));
    h.script(ROOT, Script::text("Done."));
    h.script("worker", Script::tool_call("w1", "bash", json!({ "command": "sleep 0.5" })));
    h.script("worker", Script::text("Did a and b."));
    h.prompt("do a and b").await;
    let events = h.until_idle().await;

    let queued = tool_results(&h.requests(ROOT)[2]).pop().unwrap();
    assert_eq!(queued, "Queued for \"worker\"; it reads the message at its next step.");
    // The message arrived at the agent's turn boundary, after its tool result.
    let second = &h.requests("worker")[1];
    let last = second.messages.last().unwrap();
    assert!(matches!(last, Message::User(u) if u.text_content() == "Also do part b."));
    // One round of work, one report.
    assert_eq!(mails(&events), [("worker".to_string(), AgentStatus::Completed)]);
}

#[tokio::test]
async fn an_idle_agent_takes_new_work_and_keeps_its_history() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "worker", "First job.", true));
    h.script(
        ROOT,
        Script::tool_call("c2", "send_message", json!({ "to": "worker", "message": "Second job.", "wait": true })),
    );
    h.script(ROOT, Script::text("ok"));
    h.script("worker", Script::text("first result"));
    h.script("worker", Script::text("second result"));
    h.prompt("two jobs").await;
    h.until_idle().await;

    let second = tool_results(&h.requests(ROOT)[2]).pop().unwrap();
    assert!(second.ends_with("second result"), "{second}");
    let history = user_texts(&h.requests("worker")[1]);
    assert!(history[0].contains("First job."));
    assert_eq!(history.last().unwrap(), "Second job.");
}

/// What a sub-agent asks, as the main session's events carry it: its id and the call's.
fn asked(msg: &EventMsg) -> Option<(String, String)> {
    let EventMsg::AgentEvent { agent_id, event } = msg else {
        return None;
    };
    let EventMsg::ApprovalRequested { call_id, .. } = &event.msg else {
        return None;
    };
    Some((agent_id.clone(), call_id.clone()))
}

fn answer(asking: &(String, String), decision: ApprovalDecision) -> Op {
    Op::AgentApproval {
        agent: asking.0.clone(),
        call_id: asking.1.clone(),
        decision,
    }
}

impl Harness {
    /// Until a sub-agent asks: who asks, and what came until then.
    async fn until_asked(&mut self) -> ((String, String), Vec<Event>) {
        let events = self.until(|m| asked(m).is_some()).await;
        (asked(&events.last().unwrap().msg).unwrap(), events)
    }

    /// The events of the next while, in which nothing is expected to end.
    async fn for_a_while(&mut self) -> Vec<Event> {
        let mut events = Vec::new();
        while let Ok(Some(event)) = tokio::time::timeout(Duration::from_millis(400), self.kernel.next_event()).await {
            events.push(event);
        }
        events
    }
}

#[tokio::test]
async fn a_sub_agent_asks_the_user_and_goes_on() {
    let mut h = Harness::start(PermissionMode::Manual, true).await;
    h.script(ROOT, spawn("c1", "writer", "Write x.txt.", false));
    h.script(ROOT, Script::text("started"));
    h.script(ROOT, Script::text("it is written"));
    h.script("writer", Script::tool_call("w1", "write", json!({ "path": "x.txt", "content": "hi" })));
    h.script("writer", Script::text("wrote x.txt"));
    h.prompt("write x").await;
    let (asking, events) = h.until_asked().await;

    // The question is the sub-agent's: the main session has none of its own.
    assert_eq!(asking.1, "w1");
    assert!(!events.iter().any(|e| matches!(e.msg, EventMsg::ApprovalRequested { .. })));
    assert!(!h.work.join("x.txt").exists());

    h.kernel.submit(answer(&asking, ApprovalDecision::Approve)).await.unwrap();
    let events = h.until_idle().await;
    assert_eq!(std::fs::read_to_string(h.work.join("x.txt")).unwrap(), "hi");
    assert!(events.iter().any(|e| matches!(
        &e.msg,
        EventMsg::AgentEvent { event, .. } if matches!(event.msg, EventMsg::ApprovalResolved { .. })
    )));
    assert_eq!(mails(&events), [("writer".to_string(), AgentStatus::Completed)]);
}

#[tokio::test]
async fn denying_a_sub_agents_call_refuses_that_call_and_it_goes_on() {
    let mut h = Harness::start(PermissionMode::Manual, true).await;
    h.script(ROOT, spawn("c1", "writer", "Write x.txt.", false));
    h.script(ROOT, Script::text("started"));
    h.script(ROOT, Script::text("it was refused"));
    h.script("writer", Script::tool_call("w1", "write", json!({ "path": "x.txt", "content": "hi" })));
    h.script("writer", Script::text("The user refused x.txt."));
    h.prompt("write x").await;
    let (asking, _) = h.until_asked().await;
    h.kernel
        .submit(answer(&asking, ApprovalDecision::Deny { feedback: Some("not that file".into()) }))
        .await
        .unwrap();
    let events = h.until_idle().await;

    let refused = &tool_results(&h.requests("writer")[1])[0];
    assert!(refused.contains("The user denied this tool call. Feedback: not that file"), "{refused}");
    assert!(!h.work.join("x.txt").exists());
    // It was not stopped: it reports as usual.
    assert_eq!(mails(&events), [("writer".to_string(), AgentStatus::Completed)]);
}

#[tokio::test]
async fn a_call_waiting_for_a_sub_agent_waits_for_its_question_too() {
    let mut h = Harness::start(PermissionMode::Manual, true).await;
    h.script(ROOT, spawn("c1", "writer", "Write x.txt.", true));
    h.script(ROOT, Script::text("it is written"));
    h.script("writer", Script::tool_call("w1", "write", json!({ "path": "x.txt", "content": "hi" })));
    h.script("writer", Script::text("wrote x.txt"));
    h.prompt("write x").await;
    let (asking, _) = h.until_asked().await;
    h.kernel.submit(answer(&asking, ApprovalDecision::Approve)).await.unwrap();
    h.until_idle().await;
    assert!(tool_results(&h.requests(ROOT)[1])[0].contains("wrote x.txt"));
}

#[tokio::test]
async fn approving_for_the_session_holds_for_the_whole_tree() {
    let mut h = Harness::start(PermissionMode::Manual, true).await;
    h.script(ROOT, spawn("c1", "writer", "Write x.txt and y.txt.", false));
    h.script(ROOT, Script::text("started"));
    // Woken by the report, it writes as well.
    h.script(ROOT, Script::tool_call("r1", "write", json!({ "path": "z.txt", "content": "root" })));
    h.script(ROOT, Script::text("all written"));
    h.script("writer", Script::tool_call("w1", "write", json!({ "path": "x.txt", "content": "hi" })));
    h.script("writer", Script::tool_call("w2", "write", json!({ "path": "y.txt", "content": "hi" })));
    h.script("writer", Script::text("wrote both"));
    h.prompt("write them").await;
    let (asking, _) = h.until_asked().await;
    h.kernel
        .submit(answer(&asking, ApprovalDecision::ApproveForSession { grant: None, persist: false }))
        .await
        .unwrap();
    let events = h.until_idle().await;

    // The main session changed, and nobody was asked again.
    assert!(events.iter().any(|e| matches!(
        e.msg,
        EventMsg::SettingsChanged {
            permission_mode: PermissionMode::AcceptEdits,
            ..
        }
    )));
    assert!(!events.iter().any(|e| asked(&e.msg).is_some() || matches!(e.msg, EventMsg::ApprovalRequested { .. })));
    for file in ["x.txt", "y.txt", "z.txt"] {
        assert!(h.work.join(file).exists(), "{file}");
    }
}

#[tokio::test]
async fn the_tree_asks_one_question_at_a_time() {
    let mut h = Harness::start(PermissionMode::Manual, true).await;
    h.script(ROOT, spawn("c1", "one", "Write a.txt.", false).then_tool_call("c2", "spawn_agent", json!({ "description": "two work", "task": "Write b.txt.", "name": "two", "wait": false })));
    // Later than the sub-agents' first calls, the main session has a question of its own.
    h.script(ROOT, Script::default().then_sleep(Duration::from_millis(800)).then_tool_call("r1", "write", json!({ "path": "c.txt", "content": "root" })));
    h.script(ROOT, Script::text("started"));
    h.script(ROOT, Script::text("all reported"));
    // The same call id in both: each session chooses its own.
    for (name, file) in [("one", "a.txt"), ("two", "b.txt")] {
        h.script(name, Script::tool_call("same", "write", json!({ "path": file, "content": name })));
        h.script(name, Script::text("written"));
    }
    h.prompt("write them").await;
    let (first, _) = h.until_asked().await;
    let quiet = h.for_a_while().await;
    assert!(!quiet.iter().any(|e| asked(&e.msg).is_some() || matches!(e.msg, EventMsg::ApprovalRequested { .. })), "one at a time");

    h.kernel.submit(answer(&first, ApprovalDecision::Approve)).await.unwrap();
    let (second, _) = h.until_asked().await;
    assert_ne!(second.0, first.0);
    let quiet = h.for_a_while().await;
    assert!(!quiet.iter().any(|e| asked(&e.msg).is_some() || matches!(e.msg, EventMsg::ApprovalRequested { .. })), "one at a time");

    // The main session asks in its turn, the last to come.
    h.kernel.submit(answer(&second, ApprovalDecision::Approve)).await.unwrap();
    h.until(|m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    h.kernel
        .submit(Op::Approval {
            call_id: "r1".into(),
            decision: ApprovalDecision::Approve,
        })
        .await
        .unwrap();
    h.until_idle().await;
    for file in ["a.txt", "b.txt", "c.txt"] {
        assert!(h.work.join(file).exists(), "{file}");
    }
}

#[tokio::test]
async fn an_answer_settles_the_question_still_waiting_its_turn() {
    let mut h = Harness::start(PermissionMode::Manual, true).await;
    h.script(ROOT, spawn("c1", "one", "Write a.txt.", false).then_tool_call("c2", "spawn_agent", json!({ "description": "two work", "task": "Write b.txt.", "name": "two", "wait": false })));
    h.script(ROOT, Script::text("started"));
    h.script(ROOT, Script::text("all reported"));
    for (name, file) in [("one", "a.txt"), ("two", "b.txt")] {
        h.script(name, Script::tool_call("same", "write", json!({ "path": file, "content": name })));
        h.script(name, Script::text("written"));
    }
    h.prompt("write them").await;
    let (first, _) = h.until_asked().await;
    h.for_a_while().await;
    h.kernel
        .submit(answer(&first, ApprovalDecision::ApproveForSession { grant: None, persist: false }))
        .await
        .unwrap();
    let events = h.until_idle().await;
    assert!(!events.iter().any(|e| asked(&e.msg).is_some()), "the other no longer has to ask");
    assert!(h.work.join("a.txt").exists() && h.work.join("b.txt").exists());
}

#[tokio::test]
async fn stopping_a_sub_agent_ends_its_question_and_the_next_one_asks() {
    let mut h = Harness::start(PermissionMode::Manual, true).await;
    h.script(ROOT, spawn("c1", "one", "Write a.txt.", false).then_tool_call("c2", "spawn_agent", json!({ "description": "two work", "task": "Write b.txt.", "name": "two", "wait": false })));
    h.script(ROOT, Script::text("started"));
    h.script(ROOT, Script::text("reported"));
    for (name, file) in [("one", "a.txt"), ("two", "b.txt")] {
        h.script(name, Script::tool_call("same", "write", json!({ "path": file, "content": name })));
        h.script(name, Script::text("written"));
    }
    h.prompt("write them").await;
    let (first, _) = h.until_asked().await;
    h.kernel.submit(Op::AgentInterrupt { agent: first.0.clone() }).await.unwrap();
    let (second, events) = h.until_asked().await;
    assert_ne!(second.0, first.0);
    assert!(events.iter().any(|e| matches!(
        &e.msg,
        EventMsg::AgentEvent { agent_id, event } if *agent_id == first.0
            && matches!(event.msg, EventMsg::ApprovalResolved { decision: ApprovalDecision::Abort, .. })
    )));
    h.kernel.submit(answer(&second, ApprovalDecision::Approve)).await.unwrap();
    h.until_idle().await;
    assert_eq!(h.work.read_dir().unwrap().filter_map(|e| e.ok()).filter(|e| e.file_name().to_string_lossy().ends_with(".txt")).count(), 1);
}

#[tokio::test]
async fn stopping_everything_ends_the_question_and_the_ones_in_line() {
    let mut h = Harness::start(PermissionMode::Manual, true).await;
    h.script(ROOT, spawn("c1", "one", "Write a.txt.", false).then_tool_call("c2", "spawn_agent", json!({ "description": "two work", "task": "Write b.txt.", "name": "two", "wait": false })));
    h.script(ROOT, Script::text("started"));
    for (name, file) in [("one", "a.txt"), ("two", "b.txt")] {
        h.script(name, Script::tool_call("same", "write", json!({ "path": file, "content": name })));
    }
    h.prompt("write them").await;
    h.until_asked().await;
    h.for_a_while().await;
    h.kernel.submit(Op::Interrupt).await.unwrap();
    let events = h.until_idle().await;
    // The stop reaches each sub-agent in turn, so the next in line may just get to ask. Nothing
    // stays open, and nothing ran.
    let asked_now = events.iter().filter(|e| asked(&e.msg).is_some()).count();
    let ended = events
        .iter()
        .filter(|e| matches!(&e.msg, EventMsg::AgentEvent { event, .. } if matches!(event.msg, EventMsg::ApprovalResolved { decision: ApprovalDecision::Abort, .. })))
        .count();
    assert_eq!(ended, asked_now + 1, "every question ended, the open one too");
    assert!(!h.work.join("a.txt").exists() && !h.work.join("b.txt").exists());
}

#[tokio::test]
async fn the_main_session_goes_on_while_a_sub_agent_asks() {
    let mut h = Harness::start(PermissionMode::Manual, true).await;
    h.script(ROOT, spawn("c1", "writer", "Write x.txt.", false));
    h.script(ROOT, Script::text("started"));
    h.script(ROOT, Script::text("an answer meanwhile"));
    h.script(ROOT, Script::text("it is written"));
    h.script("writer", Script::tool_call("w1", "write", json!({ "path": "x.txt", "content": "hi" })));
    h.script("writer", Script::text("wrote x.txt"));
    h.prompt("write x").await;
    let (asking, _) = h.until_asked().await;
    h.until(|m| matches!(m, EventMsg::TaskCompleted { .. })).await;

    h.prompt("something else").await;
    let events = h.until(|m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert!(events.iter().any(|e| matches!(
        &e.msg,
        EventMsg::MessageCompleted { message: Message::Assistant(reply), .. } if reply.text() == "an answer meanwhile"
    )));
    assert!(!h.work.join("x.txt").exists(), "the question still waits");

    h.kernel.submit(answer(&asking, ApprovalDecision::Approve)).await.unwrap();
    h.until_idle().await;
    assert!(h.work.join("x.txt").exists());
}

#[tokio::test]
async fn with_nobody_to_ask_a_sub_agents_call_is_refused() {
    let mut h = Harness::start(PermissionMode::Manual, false).await;
    h.script(ROOT, spawn("c1", "writer", "Write x.txt.", true));
    h.script(ROOT, Script::text("ok"));
    h.script("writer", Script::tool_call("w1", "write", json!({ "path": "x.txt", "content": "hi" })));
    h.script("writer", Script::text("I could not write x.txt."));
    h.prompt("write x").await;
    let events = h.until_idle().await;

    assert!(
        !events.iter().any(|e| asked(&e.msg).is_some() || matches!(e.msg, EventMsg::ApprovalRequested { .. })),
        "nobody is asked"
    );
    let refused = &tool_results(&h.requests("writer")[1])[0];
    assert!(refused.contains("nobody can approve it") && refused.contains("list it in your report"), "{refused}");
    assert!(!h.work.join("x.txt").exists());
}

#[tokio::test]
async fn an_answer_for_no_such_agent_is_a_warning() {
    let mut h = Harness::start(PermissionMode::Manual, true).await;
    h.kernel
        .submit(Op::AgentApproval {
            agent: "nobody".into(),
            call_id: "c".into(),
            decision: ApprovalDecision::Approve,
        })
        .await
        .unwrap();
    h.until(|m| matches!(m, EventMsg::Warning { message, .. } if message.contains("nobody"))).await;
}

/// A deferred tool of the main session, as the model runs it.
fn call_tool(id: &str, name: &str, arguments: serde_json::Value) -> Script {
    Script::tool_call(id, "call_tool", json!({ "name": name, "arguments": arguments }))
}

#[tokio::test]
async fn list_agents_tells_how_they_stand() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "slow", "Take your time.", false));
    // After the agent's first step, and while it is on its next.
    h.script(ROOT, wait_for("w1", "stepped"));
    h.script(ROOT, Script::tool_call("s1", "tool_search", json!({ "query": "list_agents" })));
    h.script(ROOT, call_tool("c2", "list_agents", json!({})));
    h.script(ROOT, call_tool("c3", "list_agents", json!({ "agents": ["nobody"] })));
    h.script(ROOT, give("w2", "listed"));
    h.script(ROOT, Script::text("it is still at it"));
    h.script(ROOT, Script::text("it is done"));
    h.script("slow", Script::text("Looking into it now.\nFirst the logs.").then_tool_call("k1", "bash", json!({ "command": "echo hi\ntouch stepped" })));
    h.script("slow", wait_for("k2", "listed"));
    h.script("slow", Script::text("all done"));
    h.prompt("how is it going?").await;
    h.until_idle().await;
    let last = h.requests(ROOT).into_iter().nth(5).unwrap();
    let results = tool_results(&last);
    let found = &results[2];
    assert!(found.contains("list_agents") && found.contains("stop_agents"), "the group comes whole: {found}");
    let status = &results[3];
    assert!(status.contains("slow (slow work): running for"), "{status}");
    assert!(status.contains("last activity") && status.contains("recent steps: echo hi…"), "{status}");
    assert!(status.ends_with("last said: Looking into it now. First the logs."), "on one line: {status}");
    assert_eq!(results[4], "nobody: no such agent");
}

#[tokio::test]
async fn list_agents_says_when_an_agent_waits_for_the_user() {
    let mut h = Harness::start(PermissionMode::Manual, true).await;
    h.script(ROOT, spawn("c1", "writer", "Write x.txt.", false));
    h.script(ROOT, Script::default().then_sleep(Duration::from_millis(500)).then_tool_call("c2", "list_agents", json!({})));
    h.script(ROOT, Script::text("it needs you"));
    h.script(ROOT, Script::text("it is written"));
    h.script("writer", Script::tool_call("w1", "write", json!({ "path": "x.txt", "content": "hi" })));
    h.script("writer", Script::text("wrote x.txt"));
    h.prompt("write x").await;
    let (asking, _) = h.until_asked().await;
    h.until(|m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let status = tool_results(&h.requests(ROOT)[2]).pop().unwrap();
    assert!(status.contains("waiting for the user to approve: "), "{status}");
    h.kernel.submit(answer(&asking, ApprovalDecision::Approve)).await.unwrap();
    h.until_idle().await;
}

#[tokio::test]
async fn stop_agents_stops_them_and_says_what_it_did() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "slow", "Take your time.", false).then_tool_call("c2", "spawn_agent", json!({ "description": "quick work", "task": "Be quick.", "name": "quick", "wait": true })));
    // Nobody is named: an error, not everything stopped.
    h.script(ROOT, call_tool("c3", "stop_agents", json!({})));
    h.script(ROOT, call_tool("c4", "stop_agents", json!({ "agents": ["slow", "quick", "nobody"] })));
    h.script(ROOT, call_tool("c5", "stop_agents", json!({ "all": true })));
    h.script(ROOT, Script::text("stopped"));
    h.script("slow", Script::tool_call("k1", "bash", json!({ "command": "sleep 30" })));
    h.script("quick", Script::text("quick done"));
    h.prompt("then stop").await;
    let started = Instant::now();
    let events = h.until_idle().await;
    assert!(started.elapsed() < Duration::from_secs(20), "the command was stopped");

    let results = tool_results(&h.requests(ROOT).into_iter().nth(4).unwrap());
    assert!(results[2].contains("name the agents to stop, or set all to true"), "{}", results[2]);
    assert_eq!(results[3], "Stopped: slow. Already finished: quick. No such agent: nobody.");
    assert_eq!(results[4], "No agents are running.");
    assert!(events.iter().any(|e| matches!(
        &e.msg,
        EventMsg::AgentStatus { name, status: AgentStatus::Interrupted, .. } if name == "slow"
    )));
    // It does not wake the main session: its request came only because the task went on.
    assert!(!events.iter().any(|e| matches!(e.msg, EventMsg::TaskStarted { trigger: TaskTrigger::Mail, .. })));
}

/// A stopped agent keeps its session: `send_message` lets it go on (bot-turn-design §5.3).
#[tokio::test]
async fn a_stopped_agent_can_go_on() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "slow", "Take your time.", false));
    h.script(ROOT, Script::default().then_sleep(Duration::from_millis(300)).then_tool_call("c2", "stop_agents", json!({ "agents": ["slow"] })));
    h.script(ROOT, Script::text("stopped"));
    h.script("slow", Script::tool_call("k1", "bash", json!({ "command": "sleep 30" })));
    h.prompt("go, then stop").await;
    h.until_idle().await;

    h.script(ROOT, Script::tool_call("c3", "send_message", json!({ "to": "slow", "message": "Go on.", "wait": true })));
    h.script(ROOT, Script::text("it finished"));
    h.script("slow", Script::text("finished now"));
    h.prompt("carry on").await;
    h.until_idle().await;
    let root = h.requests(ROOT);
    let results = tool_results(root.last().unwrap());
    let answer = results.last().unwrap();
    assert!(answer.starts_with("Agent \"slow\" completed after") && answer.contains("finished now"), "{results:?}");
    assert!(user_texts(&h.requests("slow").pop().unwrap()).iter().any(|t| t.contains("Go on.")));
}

/// A sub-agent sees the agents, and stopping them stays the main agent's decision
/// (agent-messaging-design §6.2).
#[tokio::test]
async fn a_sub_agent_can_list_the_agents_but_not_stop_them() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "slow", "Take your time.", false).then_tool_call("c2", "spawn_agent", json!({ "description": "looking around", "task": "Look around.", "name": "curious", "wait": true })));
    // Still at work now: the other agent's try did nothing.
    h.script(ROOT, Script::tool_call("c3", "stop_agents", json!({ "all": true })));
    h.script(ROOT, Script::text("done"));
    h.script("slow", Script::tool_call("s1", "bash", json!({ "command": "sleep 30" })));
    h.script("curious", Script::tool_call("k1", "tool_search", json!({ "query": "agents" })));
    h.script(
        "curious",
        call_tool("k2", "list_agents", json!({}))
            .then_tool_call("k3", "call_tool", json!({ "name": "list_agents", "arguments": { "agents": ["main"] } }))
            .then_tool_call("k4", "call_tool", json!({ "name": "stop_agents", "arguments": { "all": true } })),
    );
    h.script("curious", Script::text("I could not stop them."));
    h.prompt("look").await;
    h.until_idle().await;
    let requests = h.requests("curious");
    let names: Vec<&str> = requests[0].tools.iter().map(|t| t.name.as_str()).collect();
    assert_eq!(names, ["read", "write", "edit", "bash", "send_message", "tool_search", "call_tool"], "it starts no agents");
    let send = requests[0].tools.iter().find(|t| t.name == "send_message").unwrap();
    let properties: Vec<&String> = send.parameters["properties"].as_object().unwrap().keys().collect();
    assert_eq!(properties, ["message", "to"], "neither waits nor interrupts");
    assert!(requests[0].system.contains("- agents: See the other agents in this session"), "{}", requests[0].system);
    let found = tool_results(&requests[1]).pop().unwrap();
    assert!(found.contains("list_agents") && !found.contains("stop_agents"), "{found}");
    let results = tool_results(&requests[2]);
    let list = &results[1];
    assert!(list.starts_with("main: working\n"), "the main agent waits for it: {list}");
    assert!(list.contains("slow (slow work): running for"), "{list}");
    assert!(list.contains("curious (looking around) (you): running for"), "{list}");
    assert_eq!(results[2], "main: working");
    assert!(results[3].contains("stop_agents") && !results[3].contains("Stopped"), "{}", results[3]);
    assert_eq!(tool_results(h.requests(ROOT).last().unwrap()).pop().unwrap(), "Stopped: slow.");
}

/// A step that runs until its cue: sessions run side by side, and these tests must not lean on how
/// fast the machine is.
fn wait_for(id: &str, cue: &str) -> Script {
    Script::tool_call(id, "bash", json!({ "command": format!("while [ ! -f {cue} ]; do sleep 0.02; done") }))
}

/// A step that gives a cue.
fn give(id: &str, cue: &str) -> Script {
    Script::tool_call(id, "bash", json!({ "command": format!("touch {cue}") }))
}

impl Harness {
    /// The test gives a cue.
    fn cue(&self, cue: &str) {
        std::fs::write(self.work.join(cue), "").unwrap();
    }

    /// Until a session has given this cue.
    async fn until_cue(&self, cue: &str) {
        let deadline = Instant::now() + Duration::from_secs(20);
        while !self.work.join(cue).exists() {
            assert!(Instant::now() < deadline, "timed out waiting for the cue {cue}");
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
    }
}

fn send_to(id: &str, to: &str, message: &str) -> Script {
    Script::tool_call(id, "send_message", json!({ "to": to, "message": message }))
}

fn kinds(events: &[Event]) -> Vec<(MailKind, String)> {
    events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::MessageCompleted { mail: Some(mail), .. } => Some((mail.kind, mail.from.name.clone())),
            _ => None,
        })
        .collect()
}

fn mail_tasks(events: &[Event]) -> usize {
    events.iter().filter(|e| matches!(e.msg, EventMsg::TaskStarted { trigger: TaskTrigger::Mail, .. })).count()
}

fn task_done(msg: &EventMsg) -> bool {
    matches!(msg, EventMsg::TaskCompleted { .. })
}

/// The way a sub-agent speaks up: the idle main agent is woken, the sub-agent goes on, and its
/// report still follows (agent-messaging-design §3.1).
#[tokio::test]
async fn a_message_wakes_the_idle_main_agent_and_the_report_still_follows() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "sales", "Sum sales.csv by month.", false));
    h.script(ROOT, Script::text("handed over"));
    h.script(ROOT, Script::text("heard"));
    h.script(ROOT, Script::text("reported"));
    h.script("sales", wait_for("k0", "handed-over"));
    // A sub-agent's call has only these two; the others are ignored.
    h.script("sales", Script::tool_call("k1", "send_message", json!({ "to": "main", "message": " March is missing; going on with the other months. ", "wait": true, "interrupt": true })));
    h.script("sales", wait_for("k2", "heard"));
    h.script("sales", Script::text("Summed eleven months."));
    h.prompt("sum the sales").await;
    let mut events = h.until(task_done).await;
    h.cue("handed-over");
    events.extend(h.until(task_done).await);
    h.cue("heard");
    events.extend(h.until_idle().await);

    assert_eq!(kinds(&events), [(MailKind::Message, "sales".to_string()), (MailKind::Report, "sales".to_string())]);
    assert_eq!(mail_tasks(&events), 2, "each woke it");
    let agent = h.requests("sales");
    assert_eq!(tool_results(&agent[2]).pop().unwrap(), "Sent to main. Keep working: your final report still follows.");
    let root = h.requests(ROOT);
    assert_eq!(root.len(), 4);
    let heard = user_texts(&root[2]).pop().unwrap();
    assert_eq!(heard, "<agent_message from=\"sales\">\nMarch is missing; going on with the other months.\n</agent_message>");
    let reported = user_texts(&root[3]);
    assert_eq!(reported.len(), 3, "{reported:?}");
    assert_eq!(reported[1], heard, "it stays in the history");
    assert!(reported[2].starts_with("<agent_report agent=\"sales\" status=\"completed\">"), "{reported:?}");

    // It stays in the log as an entry of its own, and comes back with a resume.
    let log = std::fs::read_to_string(h.kernel.session_file()).unwrap();
    assert!(log.lines().any(|l| l.contains("\"type\":\"agent_message\"") && l.contains("March is missing")), "{log}");
    let session = h.kernel.session_id().to_string();
    let Harness { kernel, api, lanes, work, home, _dir } = h;
    kernel.shutdown().await;
    // While other tests here start commands on a very busy machine, the log can stay locked for a
    // moment after the shutdown. That is not what this test is about, so it waits it out.
    let deadline = Instant::now() + Duration::from_secs(10);
    let kernel = loop {
        let options = her_core::ResumeOptions {
            session: SessionRef::Id(session.clone()),
            model: None,
            thinking: None,
            permission_mode: None,
            home: home.clone(),
            config: Arc::new(Config::default()),
            project_root: None,
            trusted: false,
            interactive_approvals: false,
            bot_name: None,
        };
        match Kernel::resume(options, deps(&api, &work, &home)).await {
            Ok(kernel) => break kernel,
            Err(err) if format!("{err:?}").contains("Locked") && Instant::now() < deadline => tokio::time::sleep(Duration::from_millis(50)).await,
            Err(err) => panic!("{err:?}"),
        }
    };
    let mut h = Harness { kernel, api, lanes, work, home, _dir };
    assert_eq!(h.kernel.take_restored_agents()[0].status, AgentStatus::Completed, "a message says nothing of how it ended");
    h.script(ROOT, Script::text("I remember"));
    h.prompt("what did it say?").await;
    h.until_idle().await;
    let told = user_texts(h.requests(ROOT).last().unwrap());
    assert!(told.contains(&heard), "{told:?}");
}

/// Nothing interrupts the main agent: whatever comes while it works goes in together at its next
/// boundary, for one more request at most (agent-messaging-design §5.3).
#[tokio::test]
async fn messages_wait_for_the_boundary_of_a_busy_main_agent() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "a", "Work.", false));
    h.script(ROOT, Script::tool_call("r1", "bash", json!({ "command": "touch busy; while [ ! -f sent ]; do sleep 0.02; done" })));
    h.script(ROOT, Script::text("both heard"));
    h.script(ROOT, Script::text("reported"));
    h.script("a", wait_for("k0", "busy"));
    h.script("a", send_to("k1", "main", "one").then_tool_call("k2", "send_message", json!({ "to": "main", "message": "two" })));
    h.script("a", give("k3", "sent"));
    h.script("a", wait_for("k4", "heard"));
    h.script("a", Script::text("a done"));
    h.prompt("go").await;
    let mut events = h.until(task_done).await;
    h.cue("heard");
    events.extend(h.until_idle().await);

    let root = h.requests(ROOT);
    assert_eq!(root.len(), 4, "only the report's request came on top");
    let told = user_texts(&root[2]);
    assert!(told.iter().any(|t| t.contains("\none\n")) && told.iter().any(|t| t.contains("\ntwo\n")), "{told:?}");
    assert!(!user_texts(&root[1]).iter().any(|t| t.contains("agent_message")));
    assert_eq!(mail_tasks(&events), 1, "the report's");
}

/// A call that waits for an agent waits for its report, not for what it sends along the way
/// (agent-messaging-design §5.6).
#[tokio::test]
async fn a_message_does_not_end_a_wait() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "a", "Work.", true));
    h.script(ROOT, Script::text("done"));
    h.script("a", send_to("k1", "main", "halfway"));
    h.script("a", Script::default().then_sleep(Duration::from_millis(300)).then_text("a done"));
    h.prompt("go").await;
    let events = h.until_idle().await;

    assert_eq!(mail_tasks(&events), 0);
    assert_eq!(kinds(&events), [(MailKind::Message, "a".to_string())], "the report is the call's result");
    let last = h.requests(ROOT).pop().unwrap();
    assert!(tool_results(&last)[0].starts_with("Agent \"a\" completed after"), "{:?}", tool_results(&last));
    assert!(user_texts(&last).pop().unwrap().contains("halfway"));
}

/// Work stopped on purpose stays stopped: what waits does not start it again until there is new
/// input, as for reports (subagent-design D-46).
#[tokio::test]
async fn a_message_does_not_wake_a_stopped_main_agent() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "a", "Work.", false));
    h.script(ROOT, Script::tool_call("r1", "bash", json!({ "command": "touch busy; sleep 30" })));
    h.script(ROOT, Script::text("I see what it said"));
    h.script("a", wait_for("k0", "busy"));
    h.script("a", send_to("k1", "main", "something came up"));
    h.script("a", Script::tool_call("k2", "bash", json!({ "command": "touch sent; sleep 30" })));
    h.prompt("go").await;
    h.until_cue("sent").await;
    h.kernel.submit(Op::Interrupt).await.unwrap();
    let events = h.until_idle().await;
    assert_eq!(mail_tasks(&events), 0);
    assert_eq!(h.requests(ROOT).len(), 2);

    h.prompt("what did it say?").await;
    h.until_idle().await;
    let told = user_texts(&h.requests(ROOT)[2]);
    assert!(told.iter().any(|t| t.contains("something came up")), "{told:?}");
    assert_eq!(told.last().unwrap(), "what did it say?", "the user has the last word");
}

/// Two agents at work can talk: neither stops, and nobody else is woken.
#[tokio::test]
async fn a_working_agent_reads_a_peers_message_at_its_next_step() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "api", "Write the API.", false).then_tool_call("c2", "spawn_agent", json!({ "description": "caller", "task": "Write the caller.", "name": "caller", "wait": false })));
    h.script(ROOT, Script::text("started"));
    h.script(ROOT, Script::text("a report"));
    h.script(ROOT, Script::text("another report"));
    h.script("api", wait_for("a0", "both-started"));
    h.script("api", send_to("a1", "caller", "The API is in api.rs."));
    h.script("api", give("a2", "told"));
    h.script("api", Script::text("api done"));
    h.script("caller", wait_for("k1", "told"));
    h.script("caller", Script::text("caller done"));
    h.prompt("go").await;
    let mut events = h.until(task_done).await;
    h.cue("both-started");
    events.extend(h.until_idle().await);

    let api = h.requests("api");
    assert_eq!(tool_results(&api[2]).pop().unwrap(), "Sent to \"caller\"; it reads the message at its next step.");
    let caller = h.requests("caller");
    assert_eq!(user_texts(&caller[1]).pop().unwrap(), "<agent_message from=\"api\">\nThe API is in api.rs.\n</agent_message>");
    let mut seen = kinds(&events);
    seen.sort_by(|a, b| a.1.cmp(&b.1));
    assert_eq!(seen, [(MailKind::Report, "api".to_string()), (MailKind::Report, "caller".to_string())], "the main agent hears only reports");
}

/// Only the main agent sets an idle agent to work: what it stopped stays stopped, however its
/// peers feel about it (agent-messaging-design §3).
#[tokio::test]
async fn an_idle_agent_takes_no_message_from_a_peer_but_does_from_the_main_agent() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "quick", "Be quick.", true).then_tool_call("c2", "spawn_agent", json!({ "description": "slow", "task": "Take your time.", "name": "slow", "wait": false })));
    h.script(ROOT, wait_for("c3", "slow-started"));
    h.script(ROOT, Script::tool_call("c4", "stop_agents", json!({ "agents": ["slow"] })));
    h.script(ROOT, spawn("c5", "peer", "Wake them.", true));
    h.script(ROOT, Script::tool_call("c6", "send_message", json!({ "to": "slow", "message": "Go on.", "wait": true })));
    h.script(ROOT, Script::text("done"));
    h.script("quick", Script::text("quick done"));
    h.script("slow", Script::tool_call("k1", "bash", json!({ "command": "touch slow-started; sleep 30" })));
    h.script("slow", Script::text("went on"));
    h.script("peer", send_to("p1", "quick", "More!").then_tool_call("p2", "send_message", json!({ "to": "slow", "message": "Wake up!" })));
    h.script("peer", Script::text("they would not"));
    h.prompt("go").await;
    h.until_idle().await;

    let results = tool_results(&h.requests("peer")[1]);
    for (result, name) in results.iter().zip(["quick", "slow"]) {
        assert_eq!(*result, format!("\"{name}\" is not working now, and its report went to main. Only main can give it more work: tell main if it should."));
    }
    assert_eq!(h.requests("quick").len(), 1, "it never started again");
    let slow = h.requests("slow");
    assert_eq!(slow.len(), 2, "only the main agent's message moved it");
    let told = user_texts(&slow[1]);
    assert!(told.iter().any(|t| t == "Go on."), "{told:?}");
    assert!(!told.iter().any(|t| t.contains("Wake up!")), "{told:?}");
}

#[tokio::test]
async fn messages_say_what_is_wrong_with_where_they_went() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    // `main` is taken: nobody can be mistaken for the main agent.
    h.script(ROOT, spawn("c1", "main", "Work.", true).then_tool_call("c2", "send_message", json!({ "to": "main", "message": "hello" })));
    h.script(ROOT, Script::text("done"));
    h.script(
        "main-2",
        send_to("k1", "parent", "by the other word")
            .then_tool_call("k2", "send_message", json!({ "to": "nobody", "message": "hi" }))
            .then_tool_call("k3", "send_message", json!({ "to": "main-2", "message": "hi" }))
            .then_tool_call("k4", "send_message", json!({ "to": "main", "message": "  " })),
    );
    h.script("main-2", Script::text("done"));
    h.prompt("go").await;
    h.until_idle().await;

    let results = tool_results(&h.requests("main-2")[1]);
    assert_eq!(results[0], "Sent to main. Keep working: your final report still follows.");
    assert_eq!(results[1], "No agent named \"nobody\". Agents: main, main-2.");
    assert_eq!(results[2], "That is you.");
    assert!(results[3].contains("message must not be empty"), "{}", results[3]);
    let root = tool_results(&h.requests(ROOT)[1]);
    assert_eq!(root[1], "That is you.");
}

/// Input that came before a wait began ends it too: the model has not read it yet, and would not
/// until the agent is done (bot-turn-design §4.3).
#[tokio::test]
async fn a_wait_ends_at_once_when_input_is_already_queued() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    // The call comes after the message.
    h.script(ROOT, Script::default().then_sleep(Duration::from_millis(600)).then_tool_call("c1", "spawn_agent", json!({ "description": "slow work", "task": "Take your time.", "name": "slow", "wait": true })));
    h.script(ROOT, Script::text("1 km is 0.62 miles"));
    h.script(ROOT, Script::text("it is done"));
    h.script("slow", Script::default().then_sleep(Duration::from_secs(2)).then_text("all done"));
    h.prompt("do it").await;
    h.until(|m| matches!(m, EventMsg::TurnStarted { .. })).await;
    h.prompt("how many miles is 1 km?").await;
    let started = Instant::now();
    h.until(|m| matches!(m, EventMsg::AgentStarted { .. })).await;
    let events = h.until(|m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert!(started.elapsed() < Duration::from_millis(1800), "the question did not wait for the agent");
    assert!(events.iter().any(|e| matches!(
        &e.msg,
        EventMsg::MessageCompleted { message: Message::Assistant(reply), .. } if reply.text() == "1 km is 0.62 miles"
    )));
    let waited = tool_results(&h.requests(ROOT)[1]).pop().unwrap();
    assert!(waited.starts_with("Stopped waiting because the user sent a new message."), "{waited}");
    // Its report still comes.
    let events = h.until_idle().await;
    assert_eq!(mails(&events), [("slow".to_string(), AgentStatus::Completed)]);
}

#[tokio::test]
async fn sub_agents_follow_the_main_sessions_permissions() {
    let mut h = Harness::start(PermissionMode::Manual, false).await;
    h.script(ROOT, spawn("c1", "writer", "Write x.txt.", false));
    h.script(ROOT, Script::text("started"));
    h.script("writer", Script::default().then_sleep(Duration::from_millis(400)).then_tool_call(
        "w1",
        "write",
        json!({ "path": "x.txt", "content": "hi" }),
    ));
    h.script("writer", Script::text("wrote x.txt"));
    h.prompt("write x").await;
    h.until(|m| matches!(m, EventMsg::AgentStarted { .. })).await;
    h.kernel
        .submit(Op::SetPermissionMode {
            mode: PermissionMode::AcceptEdits,
        })
        .await
        .unwrap();
    h.until_idle().await;
    assert_eq!(std::fs::read_to_string(h.work.join("x.txt")).unwrap(), "hi");
}

#[tokio::test]
async fn stopping_interrupts_every_agent_and_they_can_continue() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "slow", "Take your time.", false));
    h.script(ROOT, Script::text("It is working."));
    h.script("slow", Script::text("half way").then_sleep(Duration::from_secs(30)).then_text(" never"));
    h.prompt("start").await;
    h.until(|m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    // Let the agent write "half way" (its events are its own, not the main session's).
    tokio::time::sleep(Duration::from_millis(300)).await;

    // Nothing runs in the main session; stopping stops the agent, which does not wake it.
    h.kernel.submit(Op::Interrupt).await.unwrap();
    let events = h.until_idle().await;
    let statuses: Vec<AgentStatus> = events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::AgentStatus { status, .. } => Some(*status),
            _ => None,
        })
        .collect();
    assert_eq!(statuses, [AgentStatus::Interrupted]);
    assert!(!events.iter().any(|e| matches!(e.msg, EventMsg::TaskStarted { .. })));

    // The report waits for the user's next message, and comes before it.
    h.script(
        ROOT,
        Script::tool_call("c2", "send_message", json!({ "to": "slow", "message": "Go on.", "wait": true })),
    );
    h.script(ROOT, Script::text("finished"));
    h.script("slow", Script::text("all done"));
    h.prompt("continue").await;
    h.until_idle().await;
    let root = h.requests(ROOT);
    let texts = user_texts(&root[2]);
    let report = texts.iter().position(|t| t.starts_with("<agent_report agent=\"slow\" status=\"interrupted\">"));
    let next = texts.iter().position(|t| t == "continue");
    assert!(report.is_some() && report < next, "{texts:?}");
    assert!(texts[report.unwrap()].contains("half way"));
    // It says the agent can go on, so the model resumes it instead of starting a new one.
    assert!(texts[report.unwrap()].ends_with("send_message lets it continue where it stopped."));
    assert!(tool_results(&root[3]).pop().unwrap().ends_with("all done"));
}

#[tokio::test]
async fn a_working_agents_log_is_on_disk_before_its_task_ends() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "slow", "Take your time.", false));
    h.script(ROOT, Script::text("It is working."));
    h.script("slow", Script::text("half way").then_sleep(Duration::from_secs(30)).then_text(" never"));
    h.prompt("start").await;
    let events = h.until(|m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let (_, _, file) = agent_ids(&events).pop().unwrap();
    let deadline = Instant::now() + Duration::from_secs(5);
    while !std::fs::read_to_string(&file).is_ok_and(|log| log.contains("Take your time.")) {
        assert!(Instant::now() < deadline, "the agent's log is not on disk while it works");
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    h.kernel.submit(Op::Interrupt).await.unwrap();
    h.until_idle().await;
}

#[tokio::test]
async fn a_sub_agents_work_is_passed_on_to_the_main_session() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "counter", "Count the files.", false));
    h.script(ROOT, Script::text("Started."));
    h.script(ROOT, Script::text("It counted 3."));
    h.script("counter", Script::tool_call("t1", "bash", json!({ "command": "echo 3" })));
    h.script("counter", Script::text("3 files"));
    h.prompt("count").await;
    let events = h.until_idle().await;
    let (agent_id, _, _) = agent_ids(&events).pop().unwrap();

    // What its conversation shows comes as agent_event, in order; deltas stay behind.
    let forwarded: Vec<&EventMsg> = events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::AgentEvent { agent_id: id, event } if *id == agent_id => Some(&event.msg),
            _ => None,
        })
        .collect();
    let kinds: Vec<&str> = forwarded
        .iter()
        .map(|m| match m {
            EventMsg::TaskStarted { .. } => "task_started",
            EventMsg::MessageCompleted { .. } => "message",
            EventMsg::ToolStarted { .. } => "tool_started",
            EventMsg::ToolRunning { .. } => "tool_running",
            EventMsg::ToolCompleted { .. } => "tool_completed",
            EventMsg::TaskCompleted { .. } => "task_completed",
            EventMsg::TurnStarted { .. } | EventMsg::TurnCompleted { .. } => "turn",
            _ => "other",
        })
        .filter(|k| *k != "turn")
        .collect();
    assert_eq!(
        kinds,
        ["task_started", "message", "message", "tool_started", "tool_running", "tool_completed", "message", "task_completed"],
        "{forwarded:?}"
    );
    assert!(!forwarded.iter().any(|m| matches!(m, EventMsg::MessageDelta { .. })));

    // The idle status says how long the round took.
    let elapsed = events.iter().find_map(|e| match &e.msg {
        EventMsg::AgentStatus {
            status: AgentStatus::Completed,
            elapsed_ms,
            ..
        } => *elapsed_ms,
        _ => None,
    });
    assert!(elapsed.is_some());
}

#[tokio::test]
async fn stopping_one_agent_leaves_the_others_working() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "slow", "Take your time.", false));
    h.script(ROOT, spawn("c2", "quick", "Be quick.", false));
    h.script(ROOT, Script::text("Both started."));
    h.script(ROOT, Script::text("quick reported."));
    h.script("slow", Script::text("half way").then_sleep(Duration::from_secs(30)).then_text(" never"));
    h.script("quick", Script::text("done").then_sleep(Duration::from_millis(1500)));
    h.prompt("start").await;
    h.until(|m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    tokio::time::sleep(Duration::from_millis(300)).await;

    h.kernel.submit(Op::AgentInterrupt { agent: "slow".into() }).await.unwrap();
    let events = h.until_idle().await;
    let statuses: Vec<(String, AgentStatus)> = events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::AgentStatus { name, status, .. } => Some((name.clone(), *status)),
            _ => None,
        })
        .collect();
    assert_eq!(
        statuses,
        [("slow".to_string(), AgentStatus::Interrupted), ("quick".to_string(), AgentStatus::Completed)]
    );
    // Only the finished agent's report woke the main session; the stopped one came along with it.
    let woken = events
        .iter()
        .filter(|e| matches!(e.msg, EventMsg::TaskStarted { trigger: TaskTrigger::Mail, .. }))
        .count();
    assert_eq!(woken, 1);
    let texts = user_texts(h.requests(ROOT).last().unwrap());
    assert!(texts.iter().any(|t| t.starts_with("<agent_report agent=\"slow\" status=\"interrupted\">")), "{texts:?}");
    assert!(texts.iter().any(|t| t.starts_with("<agent_report agent=\"quick\" status=\"completed\">")), "{texts:?}");

    // An agent that does not run is a warning, not an error.
    h.kernel.submit(Op::AgentInterrupt { agent: "nobody".into() }).await.unwrap();
    let warned = h.until(|m| matches!(m, EventMsg::Warning { .. })).await;
    assert!(matches!(&warned.last().unwrap().msg, EventMsg::Warning { message, .. } if message.contains("nobody")));
}

#[tokio::test]
async fn a_report_of_an_agent_stopped_before_it_replied_is_marked_as_one() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "slow", "Take your time.", false));
    h.script(ROOT, Script::text("Started."));
    h.script(ROOT, Script::text("It had stopped."));
    h.script("slow", Script::default().then_sleep(Duration::from_secs(30)).then_text("never"));
    h.prompt("start").await;
    h.until(|m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    tokio::time::sleep(Duration::from_millis(300)).await;

    h.kernel.submit(Op::AgentInterrupt { agent: "slow".into() }).await.unwrap();
    let events = h.until_idle().await;
    // Nothing to sum up: the status carries no summary.
    assert!(events.iter().any(|e| matches!(
        e.msg,
        EventMsg::AgentStatus {
            status: AgentStatus::Interrupted,
            summary: None,
            ..
        }
    )));

    // The report waits for the next input. The model reads that it had not replied; clients
    // get the mark.
    h.prompt("go on").await;
    let events = h.until_idle().await;
    let mail = events.iter().find_map(|e| match &e.msg {
        EventMsg::MessageCompleted { mail: Some(mail), .. } => Some(mail.clone()),
        _ => None,
    });
    assert!(mail.is_some_and(|m| m.no_reply && m.status == AgentStatus::Interrupted));
    let texts = user_texts(h.requests(ROOT).last().unwrap());
    assert!(texts.iter().any(|t| t.contains("(It had not replied yet.)")), "{texts:?}");
}

#[tokio::test]
async fn stopping_a_waiting_call_stops_its_agent() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "slow", "Take your time.", true));
    h.script("slow", Script::default().then_sleep(Duration::from_secs(30)).then_text("never"));
    h.prompt("start").await;
    h.until(|m| matches!(m, EventMsg::AgentStarted { .. })).await;
    tokio::time::sleep(Duration::from_millis(100)).await;
    h.kernel.submit(Op::Interrupt).await.unwrap();
    let events = h.until_idle().await;
    assert!(events.iter().any(|e| matches!(e.msg, EventMsg::TaskInterrupted { .. })));
    assert!(events.iter().any(|e| matches!(
        e.msg,
        EventMsg::AgentStatus {
            status: AgentStatus::Interrupted,
            ..
        }
    )));
}

#[tokio::test]
async fn user_input_ends_a_wait_and_the_report_arrives_by_mail() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "slow", "Take your time.", true));
    h.script(ROOT, Script::text("Noted."));
    h.script(ROOT, Script::text("Got the report."));
    h.script("slow", Script::default().then_sleep(Duration::from_millis(800)).then_text("slow result"));
    h.prompt("start").await;
    h.until(|m| matches!(m, EventMsg::AgentStarted { .. })).await;
    h.prompt("by the way, use tabs").await;
    let events = h.until_idle().await;

    let root = h.requests(ROOT);
    let stopped = &tool_results(&root[1])[0];
    assert!(stopped.starts_with("Stopped waiting because the user sent a new message."), "{stopped}");
    assert_eq!(user_texts(&root[1]).last().unwrap(), "by the way, use tabs");
    assert_eq!(mails(&events), [("slow".to_string(), AgentStatus::Completed)]);
}

#[tokio::test]
async fn shutdown_closes_the_sub_agents_sessions() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "slow", "Take your time.", false));
    h.script(ROOT, Script::text("It is working."));
    h.script("slow", Script::default().then_sleep(Duration::from_secs(30)).then_text("never"));
    h.prompt("start").await;
    let events = h.until(|m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let (_, _, file) = agent_ids(&events).pop().unwrap();
    let store = h.store();
    let started = Instant::now();
    h.kernel.shutdown().await;
    assert!(started.elapsed() < Duration::from_secs(5));
    // The agent's log is closed (it can be opened again) and its task ended as interrupted.
    let loaded = store.open(&SessionRef::Path(file)).unwrap();
    let last_task = loaded.entries.iter().rev().find_map(|e| match &e.kind {
        EntryKind::Task { status, .. } => Some(*status),
        _ => None,
    });
    assert_eq!(last_task, Some(her_core::store::TaskStatus::Interrupted));
}

#[tokio::test]
async fn a_resumed_session_knows_its_earlier_agents_as_their_logs_left_them() {
    let mut h = Harness::start(PermissionMode::BypassPermissions, false).await;
    h.script(ROOT, spawn("c1", "counter", "Count the files.", false));
    h.script(ROOT, spawn("c2", "slow", "Take your time.", false));
    h.script(ROOT, Script::text("Two agents work."));
    h.script(ROOT, Script::text("The counter is done."));
    h.script("counter", Script::text("3 files"));
    h.script("slow", Script::default().then_sleep(Duration::from_secs(30)).then_text("never"));
    h.prompt("go").await;
    h.until(|m| matches!(m, EventMsg::MessageCompleted { mail: Some(_), .. })).await;
    h.until(|m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let session = h.kernel.session_id().to_string();
    let Harness { kernel, api, lanes, work, home, _dir } = h;
    kernel.shutdown().await;

    let options = her_core::ResumeOptions {
        session: SessionRef::Id(session),
        model: None,
        thinking: None,
        permission_mode: None,
        home: home.clone(),
        config: Arc::new(Config::default()),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot_name: None,
    };
    let kernel = Kernel::resume(options, deps(&api, &work, &home)).await.unwrap();
    let mut h = Harness { kernel, api, lanes, work, home, _dir };
    let restored = h.kernel.take_restored_agents();
    let seen: Vec<(&str, &str, AgentStatus)> = restored.iter().map(|a| (a.name.as_str(), a.description.as_str(), a.status)).collect();
    assert_eq!(
        seen,
        [("counter", "counter work", AgentStatus::Completed), ("slow", "slow work", AgentStatus::Interrupted)]
    );
    assert_eq!(restored[0].call_id, "c1");
    // Its own session, as clients show it: the task it got, its reply, where it ended.
    let replied = restored[0].transcript.iter().any(|item| match item {
        her_protocol::api::TranscriptItem::Message { message: Message::Assistant(m), .. } => m.text() == "3 files",
        _ => false,
    });
    assert!(replied, "{:?}", restored[0].transcript);
    assert!(h.kernel.take_restored_agents().is_empty(), "taken once");

    // Their sessions are not loaded: they take no messages, and their names stay taken.
    h.script(ROOT, Script::tool_call("c3", "send_message", json!({ "to": "counter", "message": "Count again." })));
    h.script(ROOT, spawn("c4", "counter", "Count again.", true));
    h.script(ROOT, Script::text("Counted again."));
    h.script("counter-2", Script::text("4 files"));
    h.prompt("count again").await;
    h.until_idle().await;
    let root = h.requests(ROOT);
    let results = tool_results(root.last().unwrap());
    assert_eq!(results[results.len() - 2], "Agent \"counter\" is closed. Start a new one with spawn_agent.");
    assert!(results[results.len() - 1].starts_with("Agent \"counter-2\" completed after"), "{results:?}");
    // Resumed, the main session has the group for looking and stopping, and its agents the part for looking.
    assert!(root.last().unwrap().system.contains("- agents:"));
    let new = &h.requests("counter-2")[0];
    assert!(new.system.contains("- agents: See the other agents in this session"), "{}", new.system);
}

#[tokio::test]
async fn sub_agents_can_be_turned_off() {
    let dir = tempfile::tempdir().unwrap();
    let home = HerHome::new(dir.path().join("home"));
    home.ensure().unwrap();
    let api = ScriptedApi::new();
    api.push(Script::text("hi"));
    let mut registry = ModelRegistry::new(Catalog::default(), RegistryOptions::default());
    registry.register_api(api.clone());
    registry.insert_provider(ScriptedApi::provider());
    let config = Config {
        agents_enabled: false,
        ..Config::default()
    };
    let mut kernel = Kernel::start(
        StartOptions {
            model: "test/scripted".into(),
            thinking: ThinkingLevel::Off,
            permission_mode: PermissionMode::Manual,
            cwd: dir.path().to_path_buf(),
            home: home.clone(),
            config: Arc::new(config),
            project_root: None,
            trusted: false,
            interactive_approvals: false,
            bot: None,
        },
        KernelDeps {
            llm: Arc::new(registry),
            tools: Arc::new(builtin_registry(BashTool::default())),
            env: Arc::new(LocalEnvironment::new(dir.path().to_path_buf())),
            store: Arc::new(JsonlStore::new(home.sessions_dir())),
            hooks: vec![],
            mcp_servers: Vec::new(),
            browser_pool: None,
            docs: None,
            decks: None,
            words: None,
            routines: None,
        },
    )
    .await
    .unwrap();
    kernel.run(vec![her_llm::UserContent::Text { text: "hi".into() }]).await.unwrap();
    let names: Vec<String> = api.requests()[0].tools.iter().map(|t| t.name.clone()).collect();
    assert_eq!(names, ["read", "write", "edit", "bash", "tool_search", "call_tool"]);
    assert!(!api.requests()[0].system.contains("- agents:"), "no agents, nothing to check on");
}
