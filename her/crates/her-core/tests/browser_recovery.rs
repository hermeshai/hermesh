//! Recovery through the real CDP transport, router and session, with an in-memory host.
//! No Chrome, native host registration or user profile is involved.

use her_browser::pool::Stream;
use her_browser::router::{OpenGuard, TabNumbers};
use her_browser::session::SnapshotRequest;
use her_browser::{Backend, BrowserConfig, BrowserError, BrowserPool, BrowserSession, CdpError, EmbeddedHost, SessionOptions};
use serde_json::{Value, json};
use std::collections::HashSet;
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};

struct Host {
    targets: Vec<&'static str>,
    error: &'static str,
}

fn info(id: &str) -> Value {
    json!({"targetId": id, "type": "page", "url": "https://recovery.test/", "title": "Kept page"})
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
        tokio::spawn(async move {
            let mut read = BufReader::new(read);
            let mut attached = HashSet::new();
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
                    "Target.getTargetInfo" => reply["result"] = json!({"targetInfo": info(target)}),
                    "Page.getFrameTree" => {
                        reply["result"] = json!({"frameTree": {"frame": {"id": "frame", "loaderId": "document", "url": "https://recovery.test/"}}})
                    }
                    "Accessibility.getFullAXTree" => {
                        reply["result"] = json!({"nodes": [{"nodeId": "1", "role": {"value": "RootWebArea"}, "name": {"value": "Kept page"}}]})
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
    pool.add_extension(Arc::new(Host { targets, error }), None);
    pool
}

fn config() -> BrowserConfig {
    BrowserConfig {
        backend: Backend::Extension,
        ..Default::default()
    }
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
