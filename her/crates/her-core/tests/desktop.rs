//! The desktop tools end to end: a scripted model reads and types into a real TextEdit window
//! through the kernel and agent-desktop, with per-app approvals, grants kept in the user's
//! configuration, a denied app and a ref used on the wrong app.
//!
//! It operates the desktop, so it runs only with `HER_DESKTOP_E2E=1`, on macOS, with
//! agent-desktop installed and allowed to use accessibility.

#![allow(clippy::print_stderr)]

use her_core::HerHome;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::KernelDeps;
use her_core::StartOptions;
use her_llm::Catalog;
use her_llm::Message;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use her_llm::ThinkingLevel;
use her_llm::api::scripted::Script;
use her_llm::api::scripted::ScriptedApi;
use her_protocol::ApprovalDecision;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::GrantAuthority;
use her_protocol::Op;
use her_protocol::PermissionMode;
use her_protocol::SiteGrant;
use her_tools::BashTool;
use her_tools::LocalEnvironment;
use her_tools::builtin_registry;
use serde_json::json;
use std::path::PathBuf;
use std::process::Command;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use tempfile::TempDir;

const TEXTEDIT: &str = "com.apple.TextEdit";

/// The tests share TextEdit's first document: one at a time.
static TEXTEDIT_TURN: tokio::sync::Mutex<()> = tokio::sync::Mutex::const_new(());

struct Harness {
    kernel: Kernel,
    api: Arc<ScriptedApi>,
    config: PathBuf,
    _dir: TempDir,
}

fn enabled() -> bool {
    if std::env::var("HER_DESKTOP_E2E").as_deref() != Ok("1") {
        eprintln!("set HER_DESKTOP_E2E=1 to run the desktop tests; skipping");
        return false;
    }
    if !cfg!(target_os = "macos") || her_desktop::locate(None).is_err() {
        eprintln!("needs macOS and agent-desktop; skipping");
        return false;
    }
    true
}

fn osascript(script: &str) -> String {
    let out = Command::new("osascript").args(["-e", script]).output().unwrap();
    String::from_utf8_lossy(&out.stdout).trim().to_string()
}

async fn harness(mode: PermissionMode) -> Harness {
    let dir = tempfile::tempdir().unwrap();
    let work = dir.path().join("work");
    std::fs::create_dir_all(&work).unwrap();
    let home = HerHome::new(dir.path().join("home"));
    home.ensure().unwrap();
    let user = home.config_file();
    std::fs::write(&user, "[desktop]\nenabled = true\n[desktop.apps]\n\"com.apple.finder\" = \"deny\"\n").unwrap();
    let files = her_core::config::LayerFiles {
        user: &user,
        project: None,
        user_project: None,
        project_root: None,
    };
    let config = her_core::config::load_layers(&files, true, &[]).unwrap().config;
    let api = ScriptedApi::new();
    let mut registry = ModelRegistry::new(Catalog::default(), RegistryOptions::default());
    registry.register_api(api.clone());
    registry.insert_provider(ScriptedApi::provider());
    let deps = KernelDeps {
        llm: Arc::new(registry),
        tools: Arc::new(builtin_registry(BashTool::default())),
        env: Arc::new(LocalEnvironment::new(work.clone())),
        store: Arc::new(JsonlStore::new(home.sessions_dir())),
        hooks: vec![],
        mcp_servers: Vec::new(),
        browser_pool: None,
        docs: None,
        decks: None,
        words: None,
        routines: None,
        connectors: None,
        mesh: None,
    };
    let options = StartOptions {
        model: "test/scripted".into(),
        thinking: ThinkingLevel::Off,
        permission_mode: mode,
        cwd: work,
        home,
        config: Arc::new(config),
        project_root: None,
        trusted: true,
        interactive_approvals: true,
        bot: None,
    };
    let kernel = Kernel::start(options, deps).await.unwrap();
    Harness {
        kernel,
        api,
        config: user,
        _dir: dir,
    }
}

/// The tool results of the last request to the model, since the last prompt.
fn last_results(api: &ScriptedApi) -> Vec<String> {
    let requests = api.requests();
    let messages = &requests.last().unwrap().messages;
    let prompt = messages.iter().rposition(|m| matches!(m, Message::User(_))).unwrap_or(0);
    messages[prompt..]
        .iter()
        .filter_map(|m| match m {
            Message::ToolResult(r) => Some(r.text_content()),
            _ => None,
        })
        .collect()
}

/// The ref of the first line in `text` that contains `what`.
fn ref_of(text: &str, what: &str) -> String {
    let line = text
        .lines()
        .find(|l| l.contains(what) && l.contains("[ref="))
        .unwrap_or_else(|| panic!("no ref for {what} in:\n{text}"));
    let start = line.find("[ref=").unwrap() + 5;
    line[start..].split(']').next().unwrap().to_string()
}

async fn until(kernel: &mut Kernel, stop: impl Fn(&EventMsg) -> bool) -> Vec<Event> {
    let mut events = Vec::new();
    let deadline = Instant::now() + Duration::from_secs(60);
    loop {
        let remaining = deadline.saturating_duration_since(Instant::now());
        let event = tokio::time::timeout(remaining, kernel.next_event())
            .await
            .expect("timed out waiting for an event")
            .expect("the session ended");
        let done = stop(&event.msg);
        events.push(event);
        if done {
            return events;
        }
    }
}

fn approval(events: &[Event]) -> (String, String, Vec<SiteGrant>) {
    match &events.last().unwrap().msg {
        EventMsg::ApprovalRequested {
            call_id,
            summary,
            proposed_grants,
            ..
        } => (call_id.clone(), summary.clone(), proposed_grants.clone()),
        other => panic!("expected an approval request, got {other:?}"),
    }
}

fn asked(events: &[Event]) -> bool {
    events.iter().any(|e| matches!(e.msg, EventMsg::ApprovalRequested { .. }))
}

fn approval_or_end(m: &EventMsg) -> bool {
    matches!(m, EventMsg::ApprovalRequested { .. } | EventMsg::TaskCompleted { .. })
}

async fn approve_for_session(h: &mut Harness, call_id: String) {
    let decision = ApprovalDecision::ApproveForSession { grant: Some(0), persist: true };
    h.kernel.submit(Op::Approval { call_id, decision }).await.unwrap();
}

#[tokio::test]
async fn reads_and_types_into_textedit_with_per_app_approval() {
    if !enabled() {
        return;
    }
    let _turn = TEXTEDIT_TURN.lock().await;
    // Document 1 is the new one, focused, with TextEdit at the front.
    osascript("tell application \"TextEdit\" to make new document");
    osascript("tell application \"TextEdit\" to activate");
    tokio::time::sleep(Duration::from_secs(1)).await;
    let mut h = harness(PermissionMode::Manual).await;
    let app_origin = format!("app://{TEXTEDIT}");

    // 1. Listing apps asks nothing; the first read of TextEdit asks, named by its bundle id
    // whatever case the model used, and offers the read grant.
    h.api.push(
        Script::tool_call("c1", "cua_apps", json!({}))
            .then_tool_call("c2", "cua_snapshot", json!({ "app": "com.apple.textedit" })),
    );
    h.api.push(Script::text("read"));
    h.kernel.submit(Op::prompt("look at my TextEdit window")).await.unwrap();
    let events = until(&mut h.kernel, approval_or_end).await;
    let (call_id, summary, grants) = approval(&events);
    assert_eq!(summary, format!("read the window of {TEXTEDIT}"));
    assert_eq!(grants, vec![SiteGrant { authority: GrantAuthority::Navigate, origin: app_origin.clone() }]);
    approve_for_session(&mut h, call_id).await;
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let results = last_results(&h.api);
    assert!(results[0].contains(TEXTEDIT), "{}", results[0]);
    let mut window = results[1].clone();
    let mut snapshot = json!({ "app": TEXTEDIT });
    // With other TextEdit windows open, the result names them; retry on ours, as a model would.
    if window.contains("AMBIGUOUS_TARGET") {
        let ours = format!("\"{}\"", osascript("tell application \"TextEdit\" to get name of document 1"));
        let id = window
            .split(", ")
            .find(|w| w.ends_with(&ours) || w.ends_with(&format!("{ours}.")))
            .and_then(|w| w.split_whitespace().rev().nth(1))
            .unwrap_or_else(|| panic!("our window {ours} is not listed: {window}"))
            .to_string();
        snapshot["window_id"] = json!(id);
        h.api.push(Script::tool_call("c2b", "cua_snapshot", snapshot.clone()));
        h.api.push(Script::text("read"));
        h.kernel.submit(Op::prompt("that window")).await.unwrap();
        let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
        assert!(!asked(&events), "TextEdit may be read for the session");
        window = last_results(&h.api).pop().unwrap();
    }
    assert!(window.contains("<app-content") && window.contains("textfield"), "{window}");
    let field = ref_of(&window, "textfield");

    // 2. Reading again asks nothing; the first action asks, with the act grant.
    let typed = "hello from the her desktop e2e";
    h.api.push(
        Script::tool_call("c3", "cua_snapshot", snapshot.clone())
            .then_tool_call("c4", "cua_act", json!({ "app": TEXTEDIT, "action": "set_value", "ref": field, "text": typed })),
    );
    h.api.push(Script::text("typed"));
    h.kernel.submit(Op::prompt("write a line")).await.unwrap();
    let events = until(&mut h.kernel, approval_or_end).await;
    let (call_id, summary, grants) = approval(&events);
    assert!(summary.starts_with(&format!("set-value {field}")) && summary.ends_with(&format!("in {TEXTEDIT}")), "{summary}");
    assert_eq!(grants, vec![SiteGrant { authority: GrantAuthority::Act, origin: app_origin.clone() }]);
    approve_for_session(&mut h, call_id).await;
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert_eq!(events.iter().filter(|e| matches!(e.msg, EventMsg::ApprovalRequested { .. })).count(), 0);
    let results = last_results(&h.api);
    assert!(results[1].contains("done") || results[1].contains("sent"), "{}", results[1]);
    assert_eq!(osascript("tell application \"TextEdit\" to get text of document 1"), typed);

    // 3. With the act grant nothing asks; a denied app and a ref used on another app fail
    // without asking.
    let finder_ref = field.clone();
    h.api.push(
        Script::tool_call("c5", "cua_act", json!({ "app": TEXTEDIT, "action": "press", "text": "cmd+a" }))
            .then_tool_call("c6", "cua_snapshot", json!({ "app": "com.apple.finder" }))
            .then_tool_call("c7", "cua_act", json!({ "app": "com.apple.finder", "action": "click", "ref": finder_ref })),
    );
    h.api.push(Script::text("done"));
    h.kernel.submit(Op::prompt("more")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert!(!asked(&events), "nothing asks");
    let results = last_results(&h.api);
    assert!(results[0].contains("press"), "{}", results[0]);
    assert!(results[1].contains("denied"), "{}", results[1]);
    assert!(results[2].contains("not com.apple.finder"), "{}", results[2]);

    // 4. "Remember" kept the grant in the user's configuration, as act.
    let kept: toml::Table = toml::from_str(&std::fs::read_to_string(&h.config).unwrap()).unwrap();
    assert_eq!(kept["desktop"]["apps"][TEXTEDIT].as_str(), Some("act"));
    assert_eq!(kept["desktop"]["apps"]["com.apple.finder"].as_str(), Some("deny"));

    osascript("tell application \"TextEdit\" to close document 1 saving no");
}

fn clipboard() -> String {
    String::from_utf8_lossy(&Command::new("pbpaste").output().unwrap().stdout).to_string()
}

fn frontmost() -> String {
    osascript("tell application \"System Events\" to get bundle identifier of first process whose frontmost is true")
}

#[tokio::test]
async fn types_and_presses_into_textedit_in_the_background() {
    if !enabled() {
        return;
    }
    let _turn = TEXTEDIT_TURN.lock().await;
    // TextEdit's new document has the focus inside TextEdit, while another app is at the front.
    osascript("tell application \"TextEdit\" to make new document");
    // A TextEdit that was not running comes to the front once it has started.
    tokio::time::sleep(Duration::from_secs(1)).await;
    osascript("tell application \"Finder\" to activate");
    tokio::time::sleep(Duration::from_millis(500)).await;
    let front = frontmost();
    assert_ne!(front, TEXTEDIT, "another app must be at the front");
    let clip = clipboard();
    let mut h = harness(PermissionMode::BypassPermissions).await;

    let act = |action: &str, text: &str| json!({ "app": TEXTEDIT, "action": action, "text": text });
    h.api.push(
        Script::tool_call("c1", "cua_act", act("type_text", "你好 background"))
            .then_tool_call("c2", "cua_act", act("press", "return"))
            .then_tool_call("c3", "cua_act", act("type_text", "second")),
    );
    h.api.push(Script::text("typed"));
    h.kernel.submit(Op::prompt("write two lines")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let results = last_results(&h.api);
    assert!(results[0].contains("type_text: done, and the app confirmed it."), "{}", results[0]);
    assert!(results[1].contains("press: sent to the app, whose window stayed where it was."), "{}", results[1]);
    assert!(results[2].contains("type_text: done"), "{}", results[2]);
    // The key reached the field before the text that followed it.
    assert!(results[2].contains(r#"你好 background\nsecond"."#), "{}", results[2]);
    // The user may bring another app forward meanwhile; TextEdit is never brought.
    assert_ne!(frontmost(), TEXTEDIT, "TextEdit stayed behind");
    assert_eq!(clipboard(), clip, "the clipboard was not touched");

    osascript("tell application \"TextEdit\" to close (every document whose text contains \"你好 background\") saving no");
}
