//! The `mesh` group in a bot's own conversation (crate::mesh): a host outside the kernel gives the
//! group's words and runs its tools, the approval policy asks about its writes in the host's
//! words, and the host's news wakes the conversation. Scripted model, a host of the test's own,
//! real JSONL session log.

use async_trait::async_trait;
use her_core::BotIdentity;
use her_core::Config;
use her_core::HerHome;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::KernelDeps;
use her_core::SessionRef;
use her_core::StartOptions;
use her_core::mesh::MeshHost;
use her_core::store::EntryKind;
use her_core::store::SessionStore;
use her_llm::Catalog;
use her_llm::Context;
use her_llm::Message;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use her_llm::ThinkingLevel;
use her_llm::UserContent;
use her_llm::api::scripted::Script;
use her_llm::api::scripted::ScriptedApi;
use her_protocol::AgentRef;
use her_protocol::ApprovalDecision;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::MailKind;
use her_protocol::Op;
use her_protocol::PermissionMode;
use her_protocol::TaskTrigger;
use her_protocol::api::MeshCallResult;
use her_protocol::api::MeshDescribeResult;
use her_protocol::api::MeshHostParams;
use her_protocol::api::MeshTool;
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

const SUMMARY: &str = "Work in the user's other workshops: hand over anything that is built there";
const GUIDELINE: &str = "A workshop works on alone: never wait for it.";

/// A host with three tools: `look` reads, `start` writes, and `broken` reads and fails. It says
/// what a `start` would do and gives its workshop as the rule; for the workshop `nowhere` it has
/// no answer.
#[derive(Default)]
struct Host {
    group: Mutex<(u64, Option<Arc<MeshHostParams>>)>,
    described: Mutex<Vec<(String, Value)>>,
    called: Mutex<Vec<(String, String, Value)>>,
    /// The host left after the turn's request went out.
    gone: Mutex<bool>,
}

impl Host {
    fn offer(&self, group: Option<MeshHostParams>) {
        let mut current = self.group.lock();
        *current = (current.0 + 1, group.map(Arc::new));
    }
}

fn group() -> MeshHostParams {
    let tool = |name: &str, description: &str, writes| MeshTool {
        name: name.into(),
        description: description.into(),
        parameters: json!({"type": "object", "properties": {"workshop": {"type": "string"}}}),
        writes,
    };
    MeshHostParams {
        summary: SUMMARY.into(),
        guidelines: vec![GUIDELINE.into()],
        tools: vec![tool("look", "See what a workshop holds.", false), tool("start", "Start work in a workshop.", true), tool("broken", "Never works.", false)],
    }
}

#[async_trait]
impl MeshHost for Host {
    fn group(&self) -> (u64, Option<Arc<MeshHostParams>>) {
        self.group.lock().clone()
    }

    async fn describe(&self, name: &str, arguments: &Value) -> Result<MeshDescribeResult, String> {
        self.described.lock().push((name.to_string(), arguments.clone()));
        let workshop = arguments["workshop"].as_str().unwrap_or_default();
        if workshop == "nowhere" {
            return Err("The mesh host did not answer within 15 seconds.".into());
        }
        Ok(MeshDescribeResult { summary: format!("Start work in {workshop}"), rule: Some(vec!["mesh".into(), workshop.into()]) })
    }

    async fn call(&self, call_id: &str, name: &str, arguments: Value) -> Result<MeshCallResult, String> {
        if *self.gone.lock() {
            return Err("The mesh host is not connected, so these tools cannot run now.".into());
        }
        self.called.lock().push((call_id.to_string(), name.to_string(), arguments));
        Ok(match name {
            "broken" => MeshCallResult { output: "it broke".into(), is_error: true },
            _ => MeshCallResult { output: format!("{name} done"), is_error: false },
        })
    }
}

struct Harness {
    kernel: Kernel,
    api: Arc<ScriptedApi>,
    host: Arc<Host>,
    home: HerHome,
    _dir: TempDir,
}

fn deps(api: &Arc<ScriptedApi>, work: &Path, home: &HerHome, host: &Arc<Host>) -> KernelDeps {
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
        connectors: None,
        mesh: Some(host.clone()),
    }
}

/// Her's conversation with the host's group, when `offered`.
async fn her(mode: PermissionMode, offered: bool) -> Harness {
    let dir = tempfile::tempdir().unwrap();
    let home = HerHome::new(dir.path().canonicalize().unwrap().join("home"));
    home.ensure().unwrap();
    let folder = home.bot_files_dir("b1");
    std::fs::create_dir_all(&folder).unwrap();
    let folder = folder.canonicalize().unwrap();
    let api = ScriptedApi::new();
    let host = Arc::new(Host::default());
    if offered {
        host.offer(Some(group()));
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
        interactive_approvals: true,
        bot: Some(BotIdentity { id: "b1".into(), name: "Her".into() }),
    };
    let kernel = Kernel::start(options, deps(&api, &folder, &home, &host)).await.unwrap();
    Harness { kernel, api, host, home, _dir: dir }
}

fn text_of(message: &Message) -> String {
    match message {
        Message::User(user) => user.text_content(),
        Message::ToolResult(result) => result.text_content(),
        _ => String::new(),
    }
}

fn send_last(id: &str, text: &str) -> Script {
    Script::tool_call(id, "send_to_user", json!({ "text": text, "end_turn": true }))
}

fn call(id: &str, name: &str, arguments: Value) -> Script {
    Script::tool_call(id, "call_tool", json!({ "name": name, "arguments": arguments }))
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
        let event = tokio::time::timeout(remaining, kernel.next_event()).await.expect("timed out waiting for an event").expect("session closed");
        let done = stop(&event.msg);
        events.push(event);
        if done {
            return events;
        }
    }
}

fn ended(msg: &EventMsg) -> bool {
    matches!(msg, EventMsg::TaskCompleted { .. } | EventMsg::TaskFailed { .. } | EventMsg::TaskInterrupted { .. })
}

/// The result of the call `id` among `events`: its text, and whether it failed.
fn result_of(events: &[Event], id: &str) -> (String, bool) {
    events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::ToolCompleted { call_id, result } if call_id == id => Some((result.text_content(), result.is_error)),
            _ => None,
        })
        .unwrap_or_else(|| panic!("no result of {id}"))
}

/// The catalog's line for the group in a request's system prompt.
fn in_catalog(request: &Context) -> bool {
    request.system.contains(&format!("- mesh: {SUMMARY}"))
}

/// The model of the catalog test: the main conversation starts an agent when asked to, and ends
/// each turn with a message; the agent reports.
fn spawning(ctx: &Context) -> Option<Script> {
    let texts: Vec<String> = ctx.messages.iter().map(text_of).collect();
    if texts.first().is_some_and(|task| task.contains("Find it.")) {
        return Some(Script::text("Found: 42."));
    }
    if matches!(ctx.messages.last()?, Message::ToolResult(_)) {
        return Some(Script::text("Waiting for its report."));
    }
    // The owner's words, before the reminder that follows them.
    if texts.iter().rev().find(|text| !text.starts_with("<reminder>"))? == "spawn" {
        return Some(Script::tool_call("c1", "send_to_user", json!({ "text": "On it." })).then_tool_call("c2", "spawn_agent", json!({ "description": "look it up", "task": "Find it." })));
    }
    Some(send_last("c9", "ok"))
}

#[tokio::test]
async fn the_group_is_in_her_catalog_only_while_a_host_offers_tools() {
    let mut h = her(PermissionMode::BypassPermissions, false).await;
    h.api.respond_with(spawning);
    h.kernel.run(prompt("hello")).await.unwrap();
    assert!(!in_catalog(&h.api.requests()[0]), "no host yet");
    assert!(h.api.requests()[0].system.contains("<deferred_tools>"), "the catalog is there: {}", h.api.requests()[0].system);

    // The host comes: the running conversation has the group from its next request.
    h.host.offer(Some(group()));
    h.kernel.run(prompt("spawn")).await.unwrap();
    // The agent's report reaches her in that task or wakes her for another.
    until(&mut h.kernel, |m| matches!(m, EventMsg::Idle)).await;
    let requests = h.api.requests();
    let (agents, own): (Vec<&Context>, Vec<&Context>) = requests[1..].iter().partition(|r| r.messages.first().is_some_and(|m| text_of(m).contains("Find it.")));
    assert!(!own.is_empty() && own.iter().all(|r| in_catalog(r)), "her own requests have it");
    assert!(!agents.is_empty() && agents.iter().all(|r| !r.system.contains("- mesh")), "a sub-agent's have not");

    // Its tools change: the catalog's line follows.
    let mut other = group();
    other.summary = "Other words".into();
    h.host.offer(Some(other));
    h.kernel.run(prompt("hello")).await.unwrap();
    assert!(h.api.requests().last().unwrap().system.contains("- mesh: Other words"));

    // The host leaves: the group goes.
    h.host.offer(None);
    h.kernel.run(prompt("hello")).await.unwrap();
    assert!(!h.api.requests().last().unwrap().system.contains("- mesh"));
}

#[tokio::test]
async fn tool_search_gives_the_hosts_words_and_call_tool_runs_its_tools() {
    let mut h = her(PermissionMode::Manual, true).await;
    for script in [
        Script::tool_call("c1", "tool_search", json!({ "server": "mesh" })),
        call("c2", "look", json!({ "workshop": "shed" })),
        call("c3", "broken", json!({})),
        // Called by its own name, as a model may once it has the definition.
        Script::tool_call("c4", "look", json!({ "workshop": "attic" })),
        send_last("c5", "done"),
    ] {
        h.api.push(script);
    }
    let result = h.kernel.run(prompt("look around")).await.unwrap();
    let (found, failed) = result_of(&result.events, "c1");
    assert!(!failed);
    assert!(found.starts_with(&format!("Group mesh: {SUMMARY}.\n  {GUIDELINE}\n- look: See what a workshop holds.\n")), "{found}");
    // Key order follows serde_json's features, which the whole workspace's build can change.
    assert!(found.contains(&group().tools[0].parameters.to_string()), "{found}");
    assert!(found.contains("- start: Start work in a workshop.") && found.contains("- broken: Never works."), "{found}");
    assert_eq!(result_of(&result.events, "c2"), ("look done".to_string(), false));
    assert_eq!(result_of(&result.events, "c3"), ("it broke".to_string(), true));
    assert_eq!(result_of(&result.events, "c4"), ("look done".to_string(), false));
    assert_eq!(
        *h.host.called.lock(),
        [
            ("c2".to_string(), "look".to_string(), json!({ "workshop": "shed" })),
            ("c3".to_string(), "broken".to_string(), json!({})),
            ("c4".to_string(), "look".to_string(), json!({ "workshop": "attic" })),
        ]
    );
    // A tool that only reads is never asked about, nor described.
    assert!(!result.events.iter().any(|e| matches!(e.msg, EventMsg::ApprovalRequested { .. })));
    assert!(h.host.described.lock().is_empty());

    // The host leaves in the middle of a turn: the call says so, in the host's sentence.
    *h.host.gone.lock() = true;
    h.api.push(call("c6", "look", json!({})));
    h.api.push(send_last("c7", "done"));
    let result = h.kernel.run(prompt("again")).await.unwrap();
    assert_eq!(result_of(&result.events, "c6"), ("The mesh host is not connected, so these tools cannot run now.".to_string(), true));

    // Gone before the turn: the tool is unknown.
    h.host.offer(None);
    h.api.push(call("c8", "look", json!({})));
    h.api.push(send_last("c9", "done"));
    let result = h.kernel.run(prompt("again")).await.unwrap();
    assert_eq!(result_of(&result.events, "c8"), ("Unknown tool 'look'. Use tool_search to find tools.".to_string(), true));
}

/// Runs one `start` in `workshop`: what the approval asked, if it asked (answered with `decision`),
/// and the call's result.
async fn start(h: &mut Harness, id: &str, workshop: &str, decision: ApprovalDecision) -> (Option<(String, Option<Vec<String>>)>, (String, bool)) {
    h.api.push(call(id, "start", json!({ "workshop": workshop })));
    h.api.push(send_last(&format!("{id}-said"), "done"));
    h.kernel.submit(Op::prompt("start")).await.unwrap();
    let mut events = until(&mut h.kernel, |m| ended(m) || matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let asked = match &events.last().unwrap().msg {
        EventMsg::ApprovalRequested { call_id, summary, proposed_rule, .. } => {
            assert_eq!(call_id, id);
            Some((summary.clone(), proposed_rule.as_ref().map(|rule| rule.prefix.clone())))
        }
        _ => None,
    };
    if asked.is_some() {
        h.kernel.submit(Op::Approval { call_id: id.to_string(), decision }).await.unwrap();
        events.extend(until(&mut h.kernel, ended).await);
    }
    (asked, result_of(&events, id))
}

#[tokio::test]
async fn a_write_asks_in_the_hosts_words_and_its_rule_is_kept_for_the_session() {
    let mut h = her(PermissionMode::Manual, true).await;
    let ran = ("start done".to_string(), false);
    let rule = |workshop: &str| Some(vec!["mesh".to_string(), workshop.to_string()]);
    let for_session = ApprovalDecision::ApproveForSession { grant: None, persist: false };

    let (asked, result) = start(&mut h, "c1", "shed", for_session.clone()).await;
    assert_eq!(asked, Some(("Start work in shed".to_string(), rule("shed"))));
    assert_eq!(result, ran);
    // The rule is kept: the same workshop runs unasked, another one asks.
    let (asked, result) = start(&mut h, "c2", "shed", ApprovalDecision::Approve).await;
    assert_eq!((asked, result), (None, ran.clone()));
    let (asked, result) = start(&mut h, "c3", "attic", ApprovalDecision::Deny { feedback: None }).await;
    assert_eq!(asked, Some(("Start work in attic".to_string(), rule("attic"))));
    assert_eq!(result, ("The user denied this tool call.".to_string(), true));
    // A host that does not say: the call and its arguments, and nothing to keep.
    let (asked, result) = start(&mut h, "c4", "nowhere", for_session).await;
    assert_eq!(asked, Some((r#"start {"workshop":"nowhere"}"#.to_string(), None)));
    assert_eq!(result, ran);
    let (asked, _) = start(&mut h, "c5", "nowhere", ApprovalDecision::Approve).await;
    assert!(asked.is_some(), "approving it for the session kept nothing");
    assert_eq!(h.host.described.lock().len(), 5);
    assert_eq!(h.host.called.lock().len(), 4, "the denied call never reached the host");

    // With every action allowed the host is not asked what a call would do.
    h.kernel.submit(Op::SetPermissionMode { mode: PermissionMode::BypassPermissions }).await.unwrap();
    let (asked, result) = start(&mut h, "c6", "attic", ApprovalDecision::Approve).await;
    assert_eq!((asked, result), (None, ran));
    assert_eq!(h.host.described.lock().len(), 5);
}

fn from() -> AgentRef {
    AgentRef { id: "shed".into(), name: "Shed".into() }
}

const NEWS: &str = "<news id=\"n1\">The shelf is built.</news>\nNews of work you started.";

/// How often the model read the news in its latest request.
fn read(h: &Harness) -> usize {
    h.api.requests().last().unwrap().messages.iter().filter(|m| text_of(m) == NEWS).count()
}

#[tokio::test]
async fn news_wakes_an_idle_conversation_and_is_recorded_as_mail() {
    let mut h = her(PermissionMode::Manual, true).await;
    h.api.push(send_last("c1", "The shelf is built."));
    let mut heard = h.kernel.news("n1".into(), from(), NEWS.into());
    let events = until(&mut h.kernel, ended).await;
    assert!(events.iter().any(|e| matches!(&e.msg, EventMsg::TaskStarted { trigger: TaskTrigger::Mail, .. })), "{events:#?}");
    let mail = events.iter().find_map(|e| match &e.msg {
        EventMsg::MessageCompleted { mail: Some(mail), .. } => Some(mail.clone()),
        _ => None,
    });
    let mail = mail.expect("the news as mail");
    assert_eq!((mail.id.as_str(), mail.kind, mail.from), ("n1", MailKind::News, from()));
    assert_eq!(read(&h), 1);
    assert!(heard.try_recv().is_ok(), "whoever brought it heard that it is in the history");

    let id = h.kernel.session_id().to_string();
    h.kernel.shutdown().await;
    let (_, entries) = JsonlStore::new(h.home.sessions_dir()).read(&SessionRef::Id(id)).unwrap();
    let kept: Vec<&str> = entries.iter().filter_map(|e| match &e.kind {
        EntryKind::Mail { mail, .. } => Some(mail.id.as_str()),
        _ => None,
    }).collect();
    assert_eq!(kept, ["n1"]);
}

#[tokio::test]
async fn news_waits_while_her_work_is_stopped_and_comes_once_with_the_next_message() {
    let mut h = her(PermissionMode::Manual, true).await;
    // The owner stops her in the middle of a reply.
    h.api.push(Script::default().then_sleep(Duration::from_secs(30)));
    h.kernel.submit(Op::prompt("work")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TurnStarted { .. })).await;
    h.kernel.submit(Op::Interrupt).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskInterrupted { .. })).await;

    // News does not wake her now, and is not in the history yet; the same news again is not added.
    let mut heard = h.kernel.news("n1".into(), from(), NEWS.into());
    let mut again = h.kernel.news("n1".into(), from(), NEWS.into());
    let quiet = tokio::time::timeout(Duration::from_millis(300), until(&mut h.kernel, |m| matches!(m, EventMsg::TaskStarted { .. }))).await;
    assert!(quiet.is_err(), "nothing started");
    assert!(heard.try_recv().is_err() && again.try_recv().is_err());

    // The owner's next message brings it, once, and both hear.
    h.api.push(send_last("c1", "The shelf is built."));
    let result = h.kernel.run(prompt("go on")).await.unwrap();
    assert_eq!(read(&h), 1);
    let texts: Vec<String> = h.api.requests().last().unwrap().messages.iter().map(text_of).collect();
    assert!(texts.iter().position(|t| t == NEWS) < texts.iter().position(|t| t == "go on"), "the owner has the last word: {texts:?}");
    let mails = result.events.iter().filter(|e| matches!(&e.msg, EventMsg::MessageCompleted { mail: Some(_), .. })).count();
    assert_eq!(mails, 1);
    assert!(heard.try_recv().is_ok() && again.try_recv().is_ok());

    // In a turn, it joins at the next step.
    h.api.push(Script::tool_call("c2", "bash", json!({ "command": "sleep 1" })));
    h.api.push(send_last("c3", "Both done."));
    h.kernel.submit(Op::SetPermissionMode { mode: PermissionMode::BypassPermissions }).await.unwrap();
    h.kernel.submit(Op::prompt("more")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::ToolRunning { .. })).await;
    let later = "<news id=\"n2\">The door is hung.</news>";
    let mut heard = h.kernel.news("n2".into(), from(), later.into());
    let events = until(&mut h.kernel, ended).await;
    assert!(!events.iter().any(|e| matches!(e.msg, EventMsg::TaskStarted { .. })), "no task of its own");
    assert!(h.api.requests().last().unwrap().messages.iter().any(|m| text_of(m) == later));
    assert!(heard.try_recv().is_ok());
}
