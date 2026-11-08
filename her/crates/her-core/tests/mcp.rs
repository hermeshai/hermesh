//! MCP in the kernel: a scripted model against the in-process MCP test server. Covers
//! deferred tools (catalog, tool_search, call_tool), direct tools, list changes, required
//! servers, sub-agents and resuming, and checks that the request prefix never changes while
//! the model searches and calls.

use her_core::Config;
use her_core::HerHome;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::KernelDeps;
use her_core::ResumeOptions;
use her_core::SessionRef;
use her_core::StartOptions;
use her_core::TaskEnd;
use her_llm::Catalog;
use her_llm::Context;
use her_llm::Message;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use her_llm::ThinkingLevel;
use her_llm::UserContent;
use her_llm::api::scripted::Script;
use her_llm::api::scripted::ScriptedApi;
use her_mock::mcp::McpHttpServer;
use her_mock::mcp::McpTestOptions;
use her_protocol::EventMsg;
use her_protocol::McpExposure;
use her_protocol::McpServerConfig;
use her_protocol::McpServerState;
use her_protocol::Op;
use her_protocol::PermissionMode;
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
use std::time::Duration;
use std::time::Instant;
use tempfile::TempDir;

struct Harness {
    kernel: Kernel,
    api: Arc<ScriptedApi>,
    home: HerHome,
    work: PathBuf,
    server: Option<McpHttpServer>,
    _dir: TempDir,
}

/// Scripts by session: the main session is `root`, a sub-agent is its name.
#[derive(Default)]
struct Lanes(Mutex<HashMap<String, VecDeque<Script>>>);

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
        .unwrap_or_else(|| "root".to_string())
}

fn deps(api: &Arc<ScriptedApi>, work: &Path, home: &HerHome, servers: Vec<McpServerConfig>) -> KernelDeps {
    let mut registry = ModelRegistry::new(Catalog::builtin().unwrap(), RegistryOptions::default());
    registry.register_api(api.clone());
    registry.insert_provider(ScriptedApi::provider());
    KernelDeps {
        llm: Arc::new(registry),
        tools: Arc::new(builtin_registry(BashTool::default())),
        env: Arc::new(LocalEnvironment::new(work.to_path_buf())),
        store: Arc::new(JsonlStore::new(home.sessions_dir())),
        hooks: vec![],
        mcp_servers: servers,
        browser_pool: None,
        docs: None,
        decks: None,
        words: None,
        routines: None,
    }
}

/// Most tests are about what the tools do, not whether they may: those that ask get `manual` by
/// setting it.
fn config_mode(config: &Config) -> PermissionMode {
    if config.permission_mode == PermissionMode::Manual { PermissionMode::BypassPermissions } else { config.permission_mode }
}

fn start_options(work: &Path, home: &HerHome, config: Config) -> StartOptions {
    StartOptions {
        model: "test/scripted".into(),
        thinking: ThinkingLevel::Off,
        // Without anyone to approve; the tools below that are not read-only would be refused.
        permission_mode: config_mode(&config),
        cwd: work.to_path_buf(),
        home: home.clone(),
        config: Arc::new(config),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot: None,
    }
}

async fn test_server(options: McpTestOptions) -> McpHttpServer {
    her_mock::mcp::serve_http("127.0.0.1:0", options).await.unwrap()
}

fn http_config(name: &str, server: &McpHttpServer) -> McpServerConfig {
    McpServerConfig {
        name: name.into(),
        url: Some(server.url()),
        description: Some("Test tools: echo, arithmetic, a vault".into()),
        ..Default::default()
    }
}

impl Harness {
    async fn start(configs: Vec<McpServerConfig>, server: Option<McpHttpServer>, scripts: Vec<Script>) -> Harness {
        Self::start_with(Config::default(), configs, server, |api| {
            for script in scripts {
                api.push(script);
            }
        })
        .await
    }

    async fn start_with(
        config: Config,
        configs: Vec<McpServerConfig>,
        server: Option<McpHttpServer>,
        script: impl FnOnce(&Arc<ScriptedApi>),
    ) -> Harness {
        let dir = tempfile::tempdir().unwrap();
        let work = dir.path().join("work");
        std::fs::create_dir_all(&work).unwrap();
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        let api = ScriptedApi::new();
        script(&api);
        let kernel = Kernel::start(start_options(&work, &home, config), deps(&api, &work, &home, configs))
            .await
            .unwrap();
        Harness {
            kernel,
            api,
            home,
            work,
            server,
            _dir: dir,
        }
    }

    async fn run(&mut self, prompt: &str) -> (TaskEnd, Vec<EventMsg>) {
        let result = tokio::time::timeout(
            Duration::from_secs(30),
            self.kernel.run(vec![UserContent::Text { text: prompt.into() }]),
        )
        .await
        .expect("the task timed out")
        .unwrap();
        (result.end, result.events.into_iter().map(|e| e.msg).collect())
    }
}

/// Going back takes the definitions a search showed out of the context, so the next search
/// gives them whole again, as after a compaction.
#[tokio::test]
async fn going_back_forgets_what_searches_showed() {
    let server = test_server(McpTestOptions::default()).await;
    let config = http_config("test", &server);
    let mut h = Harness::start(
        vec![config],
        Some(server),
        vec![
            Script::tool_call("t1", "tool_search", json!({ "query": "mcp__test__add" })),
            Script::text("found it"),
            Script::tool_call("t2", "tool_search", json!({ "query": "mcp__test__add" })),
            Script::text("found it again"),
        ],
    )
    .await;
    let (_, events) = h.run("find add").await;
    let sent = events
        .iter()
        .find_map(|m| match m {
            EventMsg::MessageCompleted {
                message_id,
                message: Message::User(_),
                ..
            } => Some(message_id.clone()),
            _ => None,
        })
        .unwrap();
    h.kernel
        .submit(Op::Resend {
            message_id: sent,
            input: vec![UserContent::Text { text: "find it again".into() }],
            commands: true,
        })
        .await
        .unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let requests = h.api.requests();
    let results = tool_results(requests.last().unwrap());
    assert_eq!(results.len(), 1, "{results:?}");
    assert!(results[0].contains("- mcp__test__add: Add two numbers.\n  {"), "{}", results[0]);
}

/// The kernel's events up to and including the first one `done` accepts.
async fn until(kernel: &mut Kernel, done: impl Fn(&EventMsg) -> bool) -> Vec<EventMsg> {
    let mut seen = Vec::new();
    loop {
        let event = tokio::time::timeout(Duration::from_secs(30), kernel.next_event())
            .await
            .expect("timed out waiting for an event")
            .expect("the kernel stopped");
        let stop = done(&event.msg);
        seen.push(event.msg);
        if stop {
            return seen;
        }
    }
}

async fn wait_for_log(log: &Path, needle: &str) {
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        let text = std::fs::read_to_string(log).unwrap_or_default();
        if text.contains(needle) {
            return;
        }
        assert!(Instant::now() < deadline, "'{needle}' not in the server log:\n{text}");
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
}

fn tool_names(context: &Context) -> Vec<String> {
    context.tools.iter().map(|t| t.name.clone()).collect()
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

/// The `system_prompt` entries of the session log, as JSON.
fn prompt_entries(home: &HerHome) -> Vec<Value> {
    let mut out = Vec::new();
    for entry in walk(&home.sessions_dir()) {
        if entry.extension().is_some_and(|e| e == "jsonl") {
            for line in std::fs::read_to_string(&entry).unwrap().lines() {
                let value: Value = serde_json::from_str(line).unwrap();
                if value["type"] == "system_prompt" {
                    out.push(value);
                }
            }
        }
    }
    out
}

fn walk(dir: &Path) -> Vec<PathBuf> {
    let mut out = Vec::new();
    if let Ok(entries) = std::fs::read_dir(dir) {
        for entry in entries.flatten() {
            let path = entry.path();
            if path.is_dir() {
                out.extend(walk(&path));
            } else {
                out.push(path);
            }
        }
    }
    out
}

#[tokio::test]
async fn deferred_tools_are_searched_and_called_without_changing_the_request() {
    let server = test_server(McpTestOptions {
        generated: 60,
        ..Default::default()
    })
    .await;
    let config = http_config("test", &server);
    let mut h = Harness::start(
        vec![config],
        Some(server),
        vec![
            Script::tool_call("t1", "tool_search", json!({ "query": "repeat the text back" })),
            Script::tool_call("t2", "call_tool", json!({ "name": "mcp__test__echo", "arguments": { "text": "hi" } })),
            Script::text("done"),
        ],
    )
    .await;
    let (end, events) = h.run("say hi through the echo tool").await;
    assert_eq!(end, TaskEnd::Completed { last_message: Some("done".into()) });

    let requests = h.api.requests();
    assert_eq!(requests.len(), 3);
    // The prefix (system prompt and tool list) is the same for every request.
    for request in &requests[1..] {
        assert_eq!(request.system, requests[0].system);
        assert_eq!(request.tools, requests[0].tools);
    }
    let names = tool_names(&requests[0]);
    assert!(names.contains(&"tool_search".to_string()) && names.contains(&"call_tool".to_string()), "{names:?}");
    assert!(!names.iter().any(|n| n.starts_with("mcp__")), "deferred tools stay out of the list: {names:?}");
    assert!(requests[0].system.contains("<deferred_tools>\n"));
    assert!(requests[0].system.contains("- test: Test tools: echo, arithmetic, a vault"));

    let search = &tool_results(&requests[1])[0];
    assert!(search.starts_with("Server test: Test server for her."), "{search}");
    assert!(search.contains("- mcp__test__echo: Repeat the given text back."), "{search}");
    assert!(search.contains("\"Text to repeat\""), "the schema comes with it: {search}");
    assert_eq!(tool_results(&requests[2])[1], "echo: hi");

    assert!(events.iter().all(|e| !matches!(e, EventMsg::ApprovalRequested { .. })), "a tool that says it only reads is not asked about");
    let started = events
        .iter()
        .find_map(|e| match e {
            EventMsg::ToolStarted { name, title, .. } if name == "call_tool" => Some(title.clone()),
            _ => None,
        })
        .unwrap();
    assert_eq!(started, "test: Echo", "interfaces show the tool that runs");
    // The log keeps the fixed prompt and the MCP part apart.
    let entries = prompt_entries(&h.home);
    assert_eq!(entries.len(), 1, "nothing changed, so one entry: {entries:?}");
    assert!(entries[0]["mcp"].as_str().unwrap().starts_with("<deferred_tools>"));
    assert!(!entries[0]["text"].as_str().unwrap().contains("<deferred_tools>"));
    let _ = h.kernel.shutdown().await;
}

#[tokio::test]
async fn repeated_searches_do_not_repeat_definitions() {
    let server = test_server(McpTestOptions::default()).await;
    let config = http_config("test", &server);
    let mut h = Harness::start(
        vec![config],
        Some(server),
        vec![
            Script::tool_call("t1", "tool_search", json!({ "query": "mcp__test__add" })),
            Script::tool_call("t2", "tool_search", json!({ "query": "add two numbers" })),
            Script::tool_call("t3", "tool_search", json!({ "server": "test" })),
            Script::text("ok"),
        ],
    )
    .await;
    let (end, _) = h.run("find add").await;
    assert!(matches!(end, TaskEnd::Completed { .. }));
    let results = tool_results(&h.api.requests()[3]);
    assert!(results[0].contains("- mcp__test__add: Add two numbers.\n  {"), "{}", results[0]);
    assert!(results[1].contains("- mcp__test__add: Add two numbers. (definition shown earlier)"), "{}", results[1]);
    assert!(results[1].starts_with("Server test:\n"), "instructions only once: {}", results[1]);
    // Listing a server gives names without schemas.
    assert!(results[2].contains("- mcp__test__vault_reveal_code: Reveal the secret release code"), "{}", results[2]);
    assert!(!results[2].contains("\"properties\""), "{}", results[2]);
    assert!(!results[2].contains("ui_panel") && !results[2].contains("bad_schema"), "{}", results[2]);
}

#[tokio::test]
async fn call_tool_takes_arguments_sent_as_a_json_string() {
    let server = test_server(McpTestOptions::default()).await;
    let config = http_config("test", &server);
    let mut h = Harness::start(
        vec![config],
        Some(server),
        vec![
            Script::tool_call("t1", "call_tool", json!({ "name": "mcp__test__add", "arguments": "{\"a\": 2, \"b\": 40}" })),
            Script::text("ok"),
        ],
    )
    .await;
    let (end, _) = h.run("add them").await;
    assert!(matches!(end, TaskEnd::Completed { .. }));
    assert_eq!(tool_results(&h.api.requests()[1]), vec!["42".to_string()]);
}

#[tokio::test]
async fn listing_a_large_server_gives_every_name() {
    let server = test_server(McpTestOptions {
        generated: 300,
        ..McpTestOptions::default()
    })
    .await;
    let config = http_config("big", &server);
    let mut h = Harness::start(
        vec![config],
        Some(server),
        vec![
            Script::tool_call("t1", "tool_search", json!({ "server": "big" })),
            Script::text("ok"),
        ],
    )
    .await;
    let (end, _) = h.run("what can big do").await;
    assert!(matches!(end, TaskEnd::Completed { .. }));
    let listing = &tool_results(&h.api.requests()[1])[0];
    assert!(listing.contains("313 tools; the full name of each is mcp__big__<name>:"), "{listing}");
    let names = listing.lines().find(|l| l.starts_with("add, ")).expect("the names line");
    assert_eq!(names.split(", ").count(), 313);
    assert!(names.contains("vault_reveal_code") && names.contains("weather_list"), "{names}");
    assert!(!listing.contains("Reveal the secret"), "names only: {listing}");
    assert!(listing.len() < 6 * 1024, "{} bytes", listing.len());
}

#[tokio::test]
async fn interrupting_a_slow_call_cancels_it_on_the_server() {
    let dir = tempfile::tempdir().unwrap();
    let log = dir.path().join("server.log");
    let server = test_server(McpTestOptions {
        log: Some(log.clone()),
        ..McpTestOptions::default()
    })
    .await;
    let config = http_config("test", &server);
    let mut h = Harness::start(
        vec![config],
        Some(server),
        vec![
            Script::tool_call("t1", "call_tool", json!({ "name": "mcp__test__slow", "arguments": { "seconds": 20 } })),
            Script::text("still here"),
        ],
    )
    .await;
    let started = Instant::now();
    h.kernel.submit(Op::prompt("wait for it")).await.unwrap();
    // The server's progress reaches the session before the interrupt.
    until(&mut h.kernel, |m| matches!(m, EventMsg::ToolProgress { .. })).await;
    h.kernel.submit(Op::Interrupt).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(matches!(events.last().unwrap(), EventMsg::TaskInterrupted { .. }), "{events:?}");
    assert!(started.elapsed() < Duration::from_secs(10));
    wait_for_log(&log, "cancelled").await;
    // The session goes on.
    let (end, _) = h.run("go on").await;
    assert!(matches!(end, TaskEnd::Completed { .. }));
}

#[tokio::test]
async fn direct_servers_put_their_tools_in_the_list() {
    let server = test_server(McpTestOptions::default()).await;
    let mut config = http_config("tools", &server);
    config.exposure = Some(McpExposure::Direct);
    // No deferred group at all: web search, on by default, is one.
    let no_search = Config { web_search: her_core::config::WebSearchSettings { enabled: false, ..Default::default() }, ..Config::default() };
    let mut h = Harness::start_with(no_search, vec![config], Some(server), |api| {
        api.push(Script::tool_call("t1", "mcp__tools__echo", json!({ "text": "direct" })));
        api.push(Script::text("ok"));
    })
    .await;
    let (end, events) = h.run("echo directly").await;
    assert!(matches!(end, TaskEnd::Completed { .. }));
    let requests = h.api.requests();
    let names = tool_names(&requests[0]);
    assert!(names.contains(&"mcp__tools__echo".to_string()), "{names:?}");
    assert!(!names.iter().any(|n| n.contains("bad_schema") || n.contains("ui_panel")), "{names:?}");
    assert!(requests[0].system.contains("<mcp_instructions server=\"tools\">\nTest server for her."));
    assert!(!requests[0].system.contains("- tools"), "a direct server is not in the catalog");
    assert_eq!(tool_results(&requests[1])[0], "echo: direct");
    assert!(events.iter().any(|e| matches!(e, EventMsg::Warning { message, .. } if message.contains("bad_schema"))));
    // Start without tools, then the first request has them: one more entry, before any request.
    let entries = prompt_entries(&h.home);
    assert_eq!(entries.len(), 2);
    assert!(entries[1]["tools"].as_array().unwrap().iter().any(|t| t == "mcp__tools__echo"));
}

#[tokio::test]
async fn direct_tool_changes_apply_between_requests() {
    let server = test_server(McpTestOptions::default()).await;
    let mut config = http_config("live", &server);
    config.exposure = Some(McpExposure::Direct);
    let mut h = Harness::start(
        vec![config],
        Some(server),
        vec![
            Script::tool_call("t1", "mcp__live__add_tool", json!({})),
            Script::text("added"),
            Script::tool_call("t2", "mcp__live__extra_echo", json!({ "text": "new" })),
            Script::text("used it"),
        ],
    )
    .await;
    let (end, _) = h.run("add a tool").await;
    assert!(matches!(end, TaskEnd::Completed { .. }));
    tokio::time::sleep(Duration::from_millis(500)).await;
    let (end, _) = h.run("use the new tool").await;
    assert!(matches!(end, TaskEnd::Completed { .. }));
    let requests = h.api.requests();
    assert!(!tool_names(&requests[0]).contains(&"mcp__live__extra_echo".to_string()));
    assert!(tool_names(&requests[2]).contains(&"mcp__live__extra_echo".to_string()));
    assert_eq!(tool_results(&requests[3]).last().unwrap(), "echo: new");
    assert_eq!(prompt_entries(&h.home).len(), 3, "the change is recorded");
}

#[tokio::test]
async fn a_required_server_that_cannot_start_fails_the_task() {
    let config = McpServerConfig {
        name: "needed".into(),
        url: Some("http://127.0.0.1:1/mcp".into()),
        required: true,
        startup_timeout_secs: Some(2),
        ..Default::default()
    };
    let mut h = Harness::start(vec![config], None, vec![Script::text("never")]).await;
    let (end, events) = h.run("anything").await;
    match end {
        TaskEnd::Failed(error) => {
            assert_eq!(error.kind, "mcp_unavailable");
            assert!(error.message.contains("'needed'"), "{}", error.message);
        }
        other => panic!("expected a failure, got {other:?}"),
    }
    assert_eq!(h.api.requests().len(), 0, "no request without the required server");
    assert!(events.iter().any(|e| matches!(e, EventMsg::McpServerStatus { state: McpServerState::Failed, .. })));
}

#[tokio::test]
async fn sub_agents_share_the_servers_and_get_the_search_tools() {
    let server = test_server(McpTestOptions::default()).await;
    let config = http_config("test", &server);
    let lanes = Arc::new(Lanes::default());
    let push = |lane: &str, script: Script| lanes.0.lock().entry(lane.to_string()).or_default().push_back(script);
    push("root", Script::tool_call("r1", "spawn_agent", json!({ "description": "say hello", "task": "echo hello", "name": "worker", "wait": true })));
    push("worker", Script::tool_call("h1", "call_tool", json!({ "name": "mcp__test__echo", "arguments": { "text": "from the worker" } })));
    // The way to deferred tools does not lead to the ones only the main session has.
    push("worker", Script::tool_call("h2", "tool_search", json!({ "query": "stop_agents agents" })).then_tool_call("h3", "call_tool", json!({ "name": "stop_agents", "arguments": { "all": true } })));
    push("worker", Script::text("worker done"));
    push("root", Script::text("root done"));
    let routed = lanes.clone();
    let mut h = Harness::start_with(Config::default(), vec![config], Some(server), move |api| {
        api.respond_with(move |context| routed.0.lock().get_mut(&lane_of(context))?.pop_front());
    })
    .await;
    let (end, _) = h.run("delegate").await;
    assert_eq!(end, TaskEnd::Completed { last_message: Some("root done".into()) });
    let calls = h.api.calls();
    let worker: Vec<&Context> = calls.iter().map(|c| &c.context).filter(|c| lane_of(c) == "worker").collect();
    let names = tool_names(worker[0]);
    assert!(names.contains(&"call_tool".to_string()) && names.contains(&"tool_search".to_string()), "{names:?}");
    assert!(!names.contains(&"spawn_agent".to_string()), "{names:?}");
    assert!(worker[0].system.contains("<deferred_tools>"));
    assert!(worker[0].system.contains("- agents: See the other agents in this session"), "{}", worker[0].system);
    assert_eq!(tool_results(worker[1])[0], "echo: from the worker");
    let refused = tool_results(worker[2]);
    let (found, stopped) = (&refused[refused.len() - 2], &refused[refused.len() - 1]);
    assert!(!found.contains("Stop running agents"), "{found}");
    assert!(stopped.starts_with("Unknown tool 'stop_agents'"), "{stopped}");
    assert_eq!(h.server.as_ref().unwrap().calls(), 1, "one call on the shared connection");
}

#[tokio::test]
async fn resumed_sessions_reconnect_and_keep_one_prompt_entry() {
    let server = test_server(McpTestOptions::default()).await;
    let config = http_config("test", &server);
    let mut h = Harness::start(vec![config.clone()], Some(server), vec![Script::text("first")]).await;
    let (end, _) = h.run("hello").await;
    assert!(matches!(end, TaskEnd::Completed { .. }));
    let session = h.kernel.session_file().to_path_buf();
    let _ = h.kernel.shutdown().await;

    let api = ScriptedApi::new();
    api.push(Script::tool_call("t1", "call_tool", json!({ "name": "mcp__test__add", "arguments": { "a": 1, "b": 2 } })));
    api.push(Script::text("second"));
    let options = ResumeOptions {
        session: SessionRef::Path(session),
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
    let mut kernel = Kernel::resume(options, deps(&api, &h.work, &h.home, vec![config])).await.unwrap();
    let result = kernel.run(vec![UserContent::Text { text: "add".into() }]).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert_eq!(tool_results(&api.requests()[1]).last().unwrap(), "3");
    assert_eq!(prompt_entries(&h.home).len(), 1, "same prompt and tools: no new entry on resume");
    let _ = kernel.shutdown().await;
}

/// A run under `mode`: the model calls `tool` through `call_tool`. Returns whether it ran.
async fn runs_without_a_person(mode: PermissionMode, configure: impl FnOnce(&mut McpServerConfig), tool: &str, args: Value, rules: Vec<her_protocol::CommandRule>) -> (bool, Vec<EventMsg>) {
    let server = test_server(McpTestOptions::default()).await;
    let mut config = http_config("test", &server);
    configure(&mut config);
    let mut h = Harness::start_with(
        Config { permission_mode: mode, rules, ..Config::default() },
        vec![config],
        Some(server),
        |api| {
            api.push(Script::tool_call("t0", "tool_search", json!({ "query": "repeat" })));
            api.push(Script::tool_call("t1", "call_tool", json!({ "name": tool, "arguments": args })));
            api.push(Script::text("done"));
        },
    )
    .await;
    let (_, events) = h.run("go").await;
    let refused = events.iter().any(|e| matches!(e, EventMsg::ApprovalRefused { .. }));
    (!refused, events)
}

/// standalone-cli-design §8.5.
#[tokio::test]
async fn tools_from_servers_are_asked_about_unless_they_only_read_or_the_user_trusts_them() {
    let add = json!({ "a": 1, "b": 2 });
    let echo = json!({ "text": "hi" });
    let manual = PermissionMode::AcceptEdits;
    // Nobody can approve here, so asking is refusing.
    assert!(!runs_without_a_person(manual, |_| {}, "mcp__test__add", add.clone(), vec![]).await.0, "a tool that may change things is asked about");
    assert!(runs_without_a_person(manual, |_| {}, "mcp__test__echo", echo.clone(), vec![]).await.0, "one that says it only reads is not");
    assert!(
        !runs_without_a_person(manual, |c| c.project_defined = true, "mcp__test__echo", echo.clone(), vec![]).await.0,
        "a project's server is not taken at its word"
    );
    assert!(
        runs_without_a_person(manual, |c| c.approval = Some(her_protocol::McpApproval::Never), "mcp__test__add", add.clone(), vec![]).await.0,
        "the user trusts this server"
    );
    assert!(runs_without_a_person(PermissionMode::BypassPermissions, |_| {}, "mcp__test__add", add.clone(), vec![]).await.0, "every action runs");
    let allow = her_protocol::CommandRule { prefix: vec!["mcp__test__add".into()], decision: her_protocol::RuleDecision::Allow, reason: None };
    assert!(runs_without_a_person(manual, |_| {}, "mcp__test__add", add.clone(), vec![allow]).await.0, "a rule names the tool");
    let deny = her_protocol::CommandRule { prefix: vec!["mcp__test__echo".into()], decision: her_protocol::RuleDecision::Deny, reason: None };
    let (_, events) = runs_without_a_person(PermissionMode::BypassPermissions, |_| {}, "mcp__test__echo", echo, vec![deny]).await;
    assert!(
        format!("{events:?}").contains("blocked by the rule"),
        "a rule that forbids it holds in every mode: {events:?}"
    );
}
