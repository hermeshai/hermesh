//! Recovery through the real CDP transport, router and session, with an in-memory host.
//! No Chrome, native host registration or user profile is involved.

use her_browser::pool::Stream;
use her_browser::router::{OpenGuard, TabNumbers};
use her_browser::session::{Action, Place, SnapshotRequest};
use her_browser::{Backend, BrowserConfig, BrowserError, BrowserPool, BrowserSession, CdpError, EmbeddedHost, SessionOptions};
use serde_json::{Value, json};
use std::collections::HashSet;
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio_util::sync::CancellationToken;

struct Host {
    targets: Vec<&'static str>,
    error: &'static str,
    navigate_while_reporting: bool,
}

fn info(id: &str) -> Value {
    json!({"targetId": id, "type": "page", "url": "https://recovery.test/", "title": "Kept page"})
}

fn frame(navigated: bool) -> Value {
    if navigated {
        json!({"id": "frame", "loaderId": "next", "url": "https://recovery.test/welcome"})
    } else {
        json!({"id": "frame", "loaderId": "document", "url": "https://recovery.test/"})
    }
}

impl EmbeddedHost for Host {
    fn id(&self) -> u64 {
        1
    }
    fn keeps_tabs(&self) -> bool {
        true
    }

    fn connect(&self) -> Result<Stream, BrowserError> {
        let (client, server) = tokio::io::duplex(65536);
        let (read, mut write) = tokio::io::split(server);
        let targets = self.targets.clone();
        let error = self.error;
        let navigate_while_reporting = self.navigate_while_reporting;
        tokio::spawn(async move {
            let mut read = BufReader::new(read);
            let mut attached = HashSet::new();
            let mut submitted = false;
            let mut navigated = false;
            loop {
                let mut bytes = Vec::new();
                if read.read_until(0, &mut bytes).await.unwrap_or(0) == 0 {
                    break;
                }
                bytes.pop();
                let c: Value = serde_json::from_slice(&bytes).unwrap();
                let method = c["method"].as_str().unwrap();
                let target = c["params"]["targetId"].as_str().unwrap_or("");
                if method == "Target.attachToTarget" && target == "T7" && error == "disconnect" {
                    break;
                }
                let mut reply = json!({"id": c["id"], "result": {}});
                match method {
                    "Target.getTargets" => {
                        reply["result"] = json!({"targetInfos": targets.iter().filter(|t| !attached.contains(**t)).map(|t| info(t)).collect::<Vec<_>>()})
                    }
                    "Target.attachToTarget" if target == "T7" => {
                        reply = json!({"id": c["id"], "error": {"code": -32000, "message": error}});
                    }
                    "Target.attachToTarget" => {
                        attached.insert(target.to_string());
                        let session = format!("S{target}");
                        let event = json!({"method": "Target.attachedToTarget", "params": {"sessionId": session, "targetInfo": info(target), "waitingForDebugger": false}});
                        let mut bytes = serde_json::to_vec(&event).unwrap();
                        bytes.push(0);
                        if write.write_all(&bytes).await.is_err() {
                            break;
                        }
                        reply["result"] = json!({"sessionId": session});
                    }
                    "Target.detachFromTarget" => {
                        attached.remove(target);
                    }
                    "Target.createTarget" => reply["result"] = json!({"targetId": "T100"}),
                    "Target.getTargetInfo" => {
                        let mut info = info(target);
                        if navigated {
                            info["url"] = frame(true)["url"].clone();
                            info["title"] = json!("Welcome");
                        }
                        reply["result"] = json!({"targetInfo": info});
                    }
                    "Page.getFrameTree" => {
                        reply["result"] = json!({"frameTree": {"frame": frame(navigated)}});
                    }
                    "Input.dispatchKeyEvent" if navigate_while_reporting => submitted = true,
                    "Accessibility.getFullAXTree" => {
                        // The submit commits during the post-action snapshot, after an earlier
                        // Target.getTargetInfo could already have returned the old document.
                        if submitted && !navigated {
                            navigated = true;
                            for (method, params) in [
                                ("Page.frameNavigated", json!({"frame": frame(true)})),
                                ("Page.loadEventFired", json!({})),
                            ] {
                                let event = json!({"method": method, "sessionId": c["sessionId"], "params": params});
                                let mut bytes = serde_json::to_vec(&event).unwrap();
                                bytes.push(0);
                                if write.write_all(&bytes).await.is_err() {
                                    return;
                                }
                            }
                        }
                        let title = if navigated { "Welcome back" } else { "Kept page" };
                        reply["result"] = json!({"nodes": [
                            {"nodeId": "1", "role": {"value": "RootWebArea"}, "childIds": ["2"]},
                            {"nodeId": "2", "role": {"value": "heading"}, "name": {"value": title}},
                        ]});
                    }
                    _ => {}
                }
                let mut bytes = serde_json::to_vec(&reply).unwrap();
                bytes.push(0);
                if write.write_all(&bytes).await.is_err() {
                    break;
                }
            }
        });
        let (read, write) = tokio::io::split(client);
        Ok((Box::new(read), Box::new(write)))
    }
}

fn options(dir: &std::path::Path, numbers: TabNumbers) -> SessionOptions {
    SessionOptions {
        guard: Arc::new(OpenGuard),
        downloads: dir.join("downloads"),
        navigation_timeout: Duration::from_secs(1),
        action_timeout: Duration::from_secs(1),
        label: Some("conversation".into()),
        prefix: "u",
        numbers,
        note: None,
    }
}

fn pool(dir: &std::path::Path, targets: Vec<&'static str>, error: &'static str) -> BrowserPool {
    let pool = BrowserPool::new(dir.join("browser"), false);
    pool.add_extension(Arc::new(Host { targets, error, navigate_while_reporting: false }), None);
    pool
}

fn config() -> BrowserConfig {
    BrowserConfig {
        backend: Backend::Extension,
        ..Default::default()
    }
}

#[tokio::test]
async fn a_navigation_during_the_action_snapshot_reports_the_new_document() {
    let dir = tempfile::tempdir().unwrap();
    let pool = BrowserPool::new(dir.path().join("browser"), false);
    pool.add_extension(Arc::new(Host { targets: vec![], error: "", navigate_while_reporting: true }), None);
    let browser = pool.acquire(&config()).await.unwrap();
    let session = BrowserSession::new(browser, options(dir.path(), TabNumbers::default()));
    session.open_tab().await.unwrap();
    let page = session.page(None).unwrap();
    session.snapshot(&SnapshotRequest::default()).await.unwrap();

    let result = session.act(&Action::Press("Enter".into()), &Place::Page(page), &CancellationToken::new()).await.unwrap();
    assert_eq!(result.title, "Welcome");
    assert_eq!(result.url, "https://recovery.test/welcome");
    assert!(result.text.contains("Page: \"Welcome\" https://recovery.test/welcome"), "{}", result.text);
    assert!(result.text.contains("Snapshot:\n") && result.text.contains("Welcome back"), "{}", result.text);
}

#[tokio::test]
async fn a_stale_tab_does_not_block_the_others_or_snapshot_and_recovery() {
    let dir = tempfile::tempdir().unwrap();
    let pool = pool(dir.path(), vec!["T6", "T7", "T8"], "No tab with given id 7.");
    let browser = pool.acquire(&config()).await.unwrap();
    let numbers = TabNumbers {
        tabs: [("T6".into(), "u1".into()), ("T7".into(), "u2".into()), ("T8".into(), "u3".into())].into(),
        next_tab: 4,
        current: Some("u2".into()),
        ..Default::default()
    };
    let session = BrowserSession::new(browser.clone(), options(dir.path(), numbers));
    session.adopt_pending().await.unwrap();
    assert_eq!(session.tabs().iter().map(|t| t.target_id.as_str()).collect::<Vec<_>>(), ["T6", "T8"]);
    assert_eq!(session.tabs().iter().map(|t| t.id.as_str()).collect::<Vec<_>>(), ["u1", "u3"]);
    assert!(!session.numbers().tabs.contains_key("T7"));
    assert!(session.snapshot(&SnapshotRequest::default()).await.unwrap().text.contains("Kept page"));
    let numbers = session.numbers();
    drop(session);
    drop(browser);
    let browser = pool.acquire(&config()).await.unwrap();
    let restored = BrowserSession::new(browser, options(dir.path(), numbers));
    restored.adopt_pending().await.unwrap();
    assert_eq!(restored.tabs().len(), 2);
    assert!(restored.snapshot(&SnapshotRequest::default()).await.unwrap().text.contains("Kept page"));
    assert_eq!(restored.open_tab().await.unwrap().id, "u4");
}

#[tokio::test]
async fn only_stale_tabs_can_recover_to_empty_and_open_a_new_page() {
    for error in ["No tab with given id 7", "No tab with given id 7.", "No target with given id T7", "the tab was closed"] {
        let dir = tempfile::tempdir().unwrap();
        let pool = pool(dir.path(), vec!["T7"], error);
        let browser = pool.acquire(&config()).await.unwrap();
        let session = BrowserSession::new(browser, options(dir.path(), TabNumbers::default()));
        session.adopt_pending().await.unwrap();
        assert!(session.tabs().is_empty());
        session.open_tab().await.unwrap();
        assert!(session.snapshot(&SnapshotRequest::default()).await.unwrap().text.contains("Kept page"));
    }
}

#[tokio::test]
async fn recovery_does_not_swallow_permission_or_connection_errors() {
    for error in [
        "Permission denied",
        "Another debugger is already attached",
        "No tab with given id 999",
        "No tab with given id 999.",
        "Permission denied: No tab with given id 7.",
        "Target.attachToTarget: No tab with given id 7.",
        "No tab with given id 7.extra",
        "disconnect",
    ] {
        let dir = tempfile::tempdir().unwrap();
        let pool = pool(dir.path(), vec!["T7", "T8"], error);
        let browser = pool.acquire(&config()).await.unwrap();
        let session = BrowserSession::new(browser, options(dir.path(), TabNumbers::default()));
        let failed = session.adopt_pending().await.unwrap_err();
        if error == "disconnect" {
            assert!(matches!(failed, BrowserError::Cdp(CdpError::Closed)));
        } else {
            assert_eq!(failed.to_string(), format!("Target.attachToTarget: {error}"));
        }
    }
}

#[tokio::test]
async fn kernel_tools_list_snapshot_and_open_after_recovering_stale_tabs() {
    use her_core::{HerHome, JsonlStore, Kernel, KernelDeps, StartOptions, TaskEnd};
    use her_llm::api::scripted::{Script, ScriptedApi};
    use her_llm::{Catalog, Message, ModelRegistry, RegistryOptions, ThinkingLevel, UserContent};
    use her_protocol::PermissionMode;
    use her_tools::{BashTool, LocalEnvironment, builtin_registry};

    for (targets, snapshot_first) in [(vec!["T6", "T7", "T8"], false), (vec!["T6", "T7", "T8"], true), (vec!["T7"], false)] {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        let user = home.config_file();
        std::fs::write(&user, "[browser]\nextension = true\n[browser.sites]\n\"https://recovery.test\" = \"act\"\n").unwrap();
        let config = her_core::config::load_layers(
            &her_core::config::LayerFiles {
                user: &user,
                project: None,
                user_project: None,
                project_root: None,
            },
            true,
            &[],
        )
        .unwrap()
        .config;
        let api = ScriptedApi::new();
        let mut registry = ModelRegistry::new(Catalog::default(), RegistryOptions::default());
        registry.register_api(api.clone());
        registry.insert_provider(ScriptedApi::provider());
        let pool = Arc::new(pool(dir.path(), targets.clone(), "No tab with given id 7."));
        let deps = KernelDeps {
            llm: Arc::new(registry),
            tools: Arc::new(builtin_registry(BashTool::default())),
            env: Arc::new(LocalEnvironment::new(dir.path().into())),
            store: Arc::new(JsonlStore::new(home.sessions_dir())),
            hooks: vec![],
            mcp_servers: Vec::new(),
            browser_pool: Some(pool.clone()),
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
            permission_mode: PermissionMode::Manual,
            cwd: dir.path().into(),
            home,
            config: Arc::new(config),
            project_root: None,
            trusted: true,
            interactive_approvals: false,
            bot: None,
        };
        let mut kernel = Kernel::start(options, deps).await.unwrap();
        pool.set_session_tabs([(kernel.session_id().into(), targets.len())].into());
        api.push(Script::tool_call("search", "tool_search", json!({"query": "browser"})));
        let list = json!({"name": "browser_tabs", "arguments": {"action": "list"}});
        let snapshot = json!({"name": "browser_snapshot", "arguments": {}});
        api.push(Script::tool_call("first", "call_tool", if snapshot_first { snapshot.clone() } else { list.clone() }));
        if targets.len() > 1 {
            api.push(Script::tool_call("restored", "call_tool", if snapshot_first { list } else { snapshot }));
        }
        api.push(Script::tool_call(
            "open",
            "call_tool",
            json!({"name": "browser_tabs", "arguments": {"action": "open"}}),
        ));
        api.push(Script::tool_call("new", "call_tool", json!({"name": "browser_snapshot", "arguments": {}})));
        api.push(Script::text("done"));
        let result = tokio::time::timeout(
            Duration::from_secs(10),
            kernel.run(vec![UserContent::Text {
                text: "recover my tabs".into(),
            }]),
        )
        .await
        .unwrap()
        .unwrap();
        assert!(matches!(result.end, TaskEnd::Completed { .. }));
        let requests = api.requests();
        let results: Vec<_> = requests
            .last()
            .unwrap()
            .messages
            .iter()
            .filter_map(|m| match m {
                Message::ToolResult(r) => Some(r.text_content()),
                _ => None,
            })
            .skip(1)
            .collect();
        assert_eq!(results.len(), if targets.len() > 1 { 4 } else { 3 });
        let listed = usize::from(snapshot_first);
        assert!(results[listed].contains(if targets.len() > 1 { "2 tabs" } else { "No tabs" }), "{results:?}");
        if targets.len() > 1 {
            assert!(results[1 - listed].contains("Kept page"), "{results:?}");
        }
        assert!(results[results.len() - 2].contains("Opened tab"), "{results:?}");
        assert!(results.last().unwrap().contains("Kept page"), "{results:?}");
        assert!(!results.iter().any(|r| r.contains("No tab with given id")), "{results:?}");
        kernel.shutdown().await;
    }
}

/// A host that drops the connection once on the first snapshot (simulating a transient extension
/// worker restart), then succeeds on the reconnect while keeping the session's tab.
struct DropOnceSnapshotHost {
    id: u64,
    dropped: Arc<std::sync::atomic::AtomicBool>,
}

impl EmbeddedHost for DropOnceSnapshotHost {
    fn id(&self) -> u64 {
        self.id
    }
    fn keeps_tabs(&self) -> bool {
        true
    }
    fn connect(&self) -> Result<Stream, BrowserError> {
        let (client, server) = tokio::io::duplex(65536);
        let (read, mut write) = tokio::io::split(server);
        let dropped = self.dropped.clone();
        tokio::spawn(async move {
            let mut read = BufReader::new(read);
            let mut attached = HashSet::new();
            let mut targets: Vec<String> = vec!["T10".into()];
            loop {
                let mut bytes = Vec::new();
                if read.read_until(0, &mut bytes).await.unwrap_or(0) == 0 {
                    break;
                }
                bytes.pop();
                let c: Value = serde_json::from_slice(&bytes).unwrap();
                let method = c["method"].as_str().unwrap();
                let target = c["params"]["targetId"].as_str().unwrap_or("");
                if method == "Accessibility.getFullAXTree"
                    && !dropped.swap(true, std::sync::atomic::Ordering::SeqCst)
                {
                    // Drop the stream mid-snapshot once; the tool should reconnect and retry.
                    break;
                }
                let mut reply = json!({"id": c["id"], "result": {}});
                match method {
                    "Target.getTargets" => {
                        reply["result"] = json!({"targetInfos": targets.iter().filter(|t| !attached.contains(*t)).map(|t| info(t)).collect::<Vec<_>>()})
                    }
                    "Target.createTarget" => {
                        if !targets.iter().any(|t| t == "T10") {
                            targets.push("T10".into());
                        }
                        reply["result"] = json!({"targetId": "T10"});
                    }
                    "Target.attachToTarget" => {
                        attached.insert(target.to_string());
                        let session = format!("S{target}");
                        let event = json!({"method": "Target.attachedToTarget", "params": {"sessionId": session, "targetInfo": info(target), "waitingForDebugger": false}});
                        let mut eb = serde_json::to_vec(&event).unwrap();
                        eb.push(0);
                        if write.write_all(&eb).await.is_err() {
                            break;
                        }
                        reply["result"] = json!({"sessionId": session});
                    }
                    "Target.getTargetInfo" => reply["result"] = json!({"targetInfo": info(target)}),
                    "Page.getFrameTree" => reply["result"] = json!({"frameTree": {"frame": frame(false)}}),
                    "Page.navigate" => reply["result"] = json!({"frameId": "frame", "loaderId": "document"}),
                    "Accessibility.getFullAXTree" => {
                        reply["result"] = json!({"nodes": [
                            {"nodeId": "1", "role": {"value": "RootWebArea"}, "childIds": ["2"]},
                            {"nodeId": "2", "role": {"value": "heading"}, "name": {"value": "Auto relaunched page"}},
                        ]});
                    }
                    _ => {}
                }
                let mut rb = serde_json::to_vec(&reply).unwrap();
                rb.push(0);
                if write.write_all(&rb).await.is_err() {
                    break;
                }
            }
        });
        let (read, write) = tokio::io::split(client);
        Ok((Box::new(read), Box::new(write)))
    }
}

#[tokio::test]
async fn kernel_auto_launches_closed_extension_browser_and_retries_transient_snapshot_drop() {
    use her_core::{HerHome, JsonlStore, Kernel, KernelDeps, StartOptions, TaskEnd};
    use her_llm::api::scripted::{Script, ScriptedApi};
    use her_llm::{Catalog, Message, ModelRegistry, RegistryOptions, ThinkingLevel, UserContent};
    use her_protocol::PermissionMode;
    use her_tools::{BashTool, LocalEnvironment, builtin_registry};

    let dir = tempfile::tempdir().unwrap();
    let home = HerHome::new(dir.path().join("home"));
    home.ensure().unwrap();
    let user = home.config_file();
    std::fs::write(&user, "[browser]\nextension = true\n[browser.sites]\n\"https://recovery.test\" = \"act\"\n").unwrap();
    let config = her_core::config::load_layers(
        &her_core::config::LayerFiles {
            user: &user,
            project: None,
            user_project: None,
            project_root: None,
        },
        true,
        &[],
    )
    .unwrap()
    .config;
    let api = ScriptedApi::new();
    let mut registry = ModelRegistry::new(Catalog::default(), RegistryOptions::default());
    registry.register_api(api.clone());
    registry.insert_provider(ScriptedApi::provider());

    // Simulate Chrome previously connected, then closed before the task begins.
    let pool = Arc::new(BrowserPool::new(dir.path().join("browser"), false));
    let dropped = Arc::new(std::sync::atomic::AtomicBool::new(false));
    pool.add_extension_named(
        Arc::new(DropOnceSnapshotHost { id: 1, dropped: dropped.clone() }),
        Some("inst-1".into()),
        Some("Google Chrome 131.0.0.1".into()),
    );
    pool.remove_extension(1);

    let launches = Arc::new(parking_lot::Mutex::new(Vec::<Option<String>>::new()));
    let launches_ref = launches.clone();
    let pool_ref = pool.clone();
    let dropped_ref = dropped.clone();
    pool.set_extension_launcher(Arc::new(move |browser| {
        launches_ref.lock().push(browser.map(str::to_string));
        let p = pool_ref.clone();
        let d = dropped_ref.clone();
        tokio::spawn(async move {
            tokio::time::sleep(Duration::from_millis(40)).await;
            p.add_extension_named(
                Arc::new(DropOnceSnapshotHost { id: 2, dropped: d }),
                Some("inst-1".into()),
                Some("Google Chrome 131.0.0.1".into()),
            );
        });
        true
    }));

    let deps = KernelDeps {
        llm: Arc::new(registry),
        tools: Arc::new(builtin_registry(BashTool::default())),
        env: Arc::new(LocalEnvironment::new(dir.path().into())),
        store: Arc::new(JsonlStore::new(home.sessions_dir())),
        hooks: vec![],
        mcp_servers: Vec::new(),
        browser_pool: Some(pool.clone()),
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
        permission_mode: PermissionMode::Manual,
        cwd: dir.path().into(),
        home,
        config: Arc::new(config),
        project_root: None,
        trusted: true,
        interactive_approvals: false,
        bot: None,
    };
    let mut kernel = Kernel::start(options, deps).await.unwrap();
    pool.set_session_tabs([(kernel.session_id().into(), 1)].into());

    // First call: Chrome is closed -> auto-launches Chrome -> adopts tab -> snapshot drops once mid-call -> transparently retries and succeeds!
    api.push(Script::tool_call("search", "tool_search", json!({"query": "browser"})));
    api.push(Script::tool_call("snap", "call_tool", json!({"name": "browser_snapshot", "arguments": {}})));
    api.push(Script::text("done"));
    let result = tokio::time::timeout(
        Duration::from_secs(10),
        kernel.run(vec![UserContent::Text { text: "read page".into() }]),
    )
    .await
    .unwrap()
    .unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert_eq!(*launches.lock(), vec![Some("Google Chrome".to_string())]);
    let last_result = api
        .requests()
        .last()
        .unwrap()
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::ToolResult(r) => Some(r.text_content()),
            _ => None,
        })
        .last()
        .unwrap();
    assert!(last_result.contains("Auto relaunched page"), "{last_result}");
    kernel.shutdown().await;
}
