//! End-to-end: MCP servers through the `her` binary, against the mock model and the test MCP
//! server (`her-mcp-server`): from the configuration and `--mcp-config`, and brought by native
//! and ACP clients.

mod common;

use agent_client_protocol_schema::v1 as acp;
use common::Demux;
use common::TestEnv;
use common::native_init;
use common::native_prompt;
use her_mock::mcp::vault_code;
use her_rpc::Client;
use her_rpc::Incoming;
use her_rpc::IncomingRx;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::path::PathBuf;
use std::sync::OnceLock;

/// Builds the test MCP server once, next to this test's own binary.
fn server_bin() -> PathBuf {
    static BIN: OnceLock<PathBuf> = OnceLock::new();
    BIN.get_or_init(|| {
        let exe = std::env::current_exe().unwrap();
        let profile_dir = exe.parent().unwrap().parent().unwrap().to_path_buf();
        let cargo = std::env::var("CARGO").unwrap_or_else(|_| "cargo".into());
        let mut build = std::process::Command::new(cargo);
        build.args(["build", "-q", "-p", "her-mock", "--bin", "her-mcp-server"]);
        if profile_dir.ends_with("release") {
            build.arg("--release");
        }
        assert!(build.status().expect("cargo build").success(), "building her-mcp-server failed");
        profile_dir.join("her-mcp-server")
    })
    .clone()
}

/// `[mcp_servers.<name>]` running the test server with `generated` extra tools.
fn server_toml(name: &str, generated: usize) -> String {
    format!(
        "[mcp_servers.{name}]\ncommand = {:?}\nargs = [\"--name\", \"{name}\", \"--tools\", \"{generated}\"]\n",
        server_bin().display().to_string()
    )
}

fn of_type<'a>(events: &'a [Value], kind: &str) -> Vec<&'a Value> {
    events.iter().filter(|e| e["type"] == kind).collect()
}

fn released(project: &str) -> String {
    format!("mcp done: Release code for {project}: {}", vault_code(project))
}

/// The entries of the session log at `file`.
fn log_entries(file: &Path) -> Vec<Value> {
    std::fs::read_to_string(file)
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect()
}

async fn session_file(env: &TestEnv) -> PathBuf {
    let output = env.her(&["sessions", "--all", "--json"]).output().await.unwrap();
    let sessions: Value = serde_json::from_slice(&output.stdout).unwrap();
    PathBuf::from(sessions[0]["file"].as_str().expect("a session log"))
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn exec_finds_and_runs_a_deferred_tool() {
    // Asking is covered below; this is about finding and running.
    let env = TestEnv::new("bypass-permissions", &server_toml("vault", 120)).await;
    let output = env.her(&["exec", "--output", "stream-json", "[[mock:mcp:vault:Apollo]] get the code"]).output().await.unwrap();
    let stderr = String::from_utf8_lossy(&output.stderr);
    assert_eq!(output.status.code(), Some(0), "stderr: {stderr}");
    let events: Vec<Value> = String::from_utf8_lossy(&output.stdout)
        .lines()
        .map(|l| serde_json::from_str(l).unwrap())
        .collect();
    let ready: Vec<&Value> = of_type(&events, "mcp_server_status").into_iter().filter(|e| e["state"] == "ready").collect();
    assert_eq!(ready.len(), 1, "{events:#?}");
    assert_eq!(ready[0]["server"], "vault");
    assert!(ready[0]["tools"].as_u64().unwrap() >= 120);
    let started = of_type(&events, "tool_started");
    let names: Vec<&str> = started.iter().filter_map(|e| e["name"].as_str()).collect();
    assert_eq!(names, vec!["tool_search", "call_tool"]);
    assert_eq!(started[1]["title"], "vault: vault_reveal_code");
    assert!(of_type(&events, "approval_requested").is_empty());
    let search = &of_type(&events, "tool_completed")[0]["result"]["content"][0]["text"];
    assert!(search.as_str().unwrap().contains("mcp__vault__vault_reveal_code"), "{search}");
    let completed = of_type(&events, "task_completed")[0];
    assert_eq!(completed["last_message"], json!(released("Apollo")));

    // One system prompt for the whole session: the 120+ tools stay out of the tool list.
    let entries = log_entries(&session_file(&env).await);
    let prompts: Vec<&Value> = entries.iter().filter(|e| e["type"] == "system_prompt").collect();
    assert_eq!(prompts.len(), 1);
    let tools: Vec<&str> = prompts[0]["tools"].as_array().unwrap().iter().filter_map(Value::as_str).collect();
    assert!(tools.contains(&"tool_search") && tools.contains(&"call_tool"), "{tools:?}");
    assert!(!tools.iter().any(|t| t.starts_with("mcp__")), "{tools:?}");
    let section = prompts[0]["mcp"].as_str().unwrap();
    assert!(section.contains("<deferred_tools>") && section.contains("- vault"), "{section}");

    // Text mode shows the server's status line.
    let output = env.her(&["exec", "[[mock:mcp:vault:Apollo]] again"]).output().await.unwrap();
    assert_eq!(output.status.code(), Some(0));
    let stderr = String::from_utf8_lossy(&output.stderr);
    assert!(stderr.contains("mcp: vault ready ("), "{stderr}");
    assert!(String::from_utf8_lossy(&output.stdout).contains(&released("Apollo")));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn mcp_commands_list_check_and_get_servers() {
    let config = format!(
        "{}[mcp_servers.broken]\ncommand = \"/nonexistent/mcp-server\"\n[mcp_servers.off]\ncommand = \"x\"\nenabled = false\n",
        server_toml("docs", 0)
    );
    let env = TestEnv::new("manual", &config).await;
    let file = env.work().join("ci-mcp.json");
    let json_config = json!({"mcpServers": {
        "vault": {"command": server_bin(), "args": ["--tools", "3"], "env": {"VAULT_TOKEN": "s3cret", "REF": "${HOME}"}},
        "events": {"type": "sse", "url": "http://127.0.0.1:9/sse"}
    }});
    std::fs::write(&file, json_config.to_string()).unwrap();
    let flag = file.display().to_string();

    let output = env.her(&["--mcp-config", &flag, "mcp", "list", "--json"]).output().await.unwrap();
    assert_eq!(output.status.code(), Some(0));
    let listed: Value = serde_json::from_slice(&output.stdout).unwrap();
    let rows = listed["servers"].as_array().unwrap();
    let names: Vec<&str> = rows.iter().filter_map(|r| r["name"].as_str()).collect();
    assert_eq!(names, vec!["broken", "docs", "off", "vault"]);
    assert_eq!(rows[2]["enabled"], false);
    assert_eq!(rows[1]["sources"], json!(["user config"]));
    assert_eq!(rows[3]["sources"], json!([format!("--mcp-config {flag}")]));
    assert_eq!(rows[3]["exposure"], "deferred");
    assert!(listed["warnings"].to_string().contains("SSE"), "{listed}");

    let output = env.her(&["--mcp-config", &flag, "mcp", "get", "vault", "--json"]).output().await.unwrap();
    assert_eq!(output.status.code(), Some(0));
    let shown: Value = serde_json::from_slice(&output.stdout).unwrap();
    assert_eq!(shown["env"], json!({"REF": "${HOME}", "VAULT_TOKEN": "***"}), "secret values are hidden");
    assert!(!String::from_utf8_lossy(&output.stdout).contains("s3cret"));

    let output = env.her(&["--mcp-config", &flag, "mcp", "list", "--check"]).output().await.unwrap();
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert_eq!(output.status.code(), Some(1), "a server failed: {stdout}");
    assert!(stdout.contains("✓ docs: "), "{stdout}");
    assert!(stdout.contains("mcp__vault__vault_reveal_code"), "{stdout}");
    assert!(stdout.contains("✗ broken: "), "{stdout}");
    assert!(!stdout.contains("off"), "disabled servers are not started: {stdout}");

    let output = env.her(&["--mcp-config", &flag, "status", "--json"]).output().await.unwrap();
    let status: Value = serde_json::from_slice(&output.stdout).unwrap();
    assert_eq!(status["mcp_servers"], json!(["broken", "docs", "vault"]));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn native_client_brings_a_direct_server() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (mut child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let demux = Demux::spawn(incoming);
    let cwd = env.workspace("native");
    let server = json!({"name": "vault", "command": server_bin(), "exposure": "direct"});
    let created = client
        .request("session/new", json!({"cwd": cwd, "mcp_servers": [server]}))
        .await
        .expect("session/new");
    let session = created["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);

    let turn = native_prompt(&client, &mut events, &session, "[[mock:mcp_direct:vault:Gemini]] go", false).await;
    assert_eq!(turn.end["type"], "task_completed", "{:#?}", turn.events);
    assert_eq!(turn.end["last_message"], json!(released("Gemini")));
    assert_eq!(turn.approvals, 0);
    let statuses: Vec<&str> = of_type(&turn.events, "mcp_server_status").iter().filter_map(|e| e["state"].as_str()).collect();
    assert_eq!(statuses, vec!["starting", "ready"]);
    let tool = of_type(&turn.events, "tool_started")[0];
    assert_eq!(tool["name"], "mcp__vault__vault_reveal_code");

    let snapshot = client
        .request("session/attach", json!({"session_id": session, "history_limit": 0}))
        .await
        .unwrap();
    assert_eq!(snapshot["snapshot"]["mcp_servers"][0]["state"], "ready", "{snapshot:#}");
    client.request("session/close", json!({"session_id": session})).await.unwrap();
    drop(client);
    common::exit_code(&mut child).await;
}

#[derive(Default)]
struct AcpTurn {
    reply: String,
    calls: usize,
    notices: Vec<String>,
}

async fn acp_prompt(client: &Client, rx: &mut IncomingRx, session: &str, text: &str) -> (AcpTurn, Value) {
    let request = {
        let client = client.clone();
        let params = json!({"sessionId": session, "prompt": [{"type": "text", "text": text}]});
        tokio::spawn(async move { client.request("session/prompt", params).await })
    };
    let mut request = Box::pin(request);
    let mut turn = AcpTurn::default();
    let mut handle = |incoming: Incoming| {
        if let Incoming::Notification { method, params, .. } = &incoming
            && method == "session/update"
        {
            serde_json::from_value::<acp::SessionNotification>(params.clone()).expect("ACP update");
            let update = &params["update"];
            match update["sessionUpdate"].as_str() {
                Some("agent_message_chunk") => turn.reply.push_str(update["content"]["text"].as_str().unwrap_or("")),
                Some("tool_call") => turn.calls += 1,
                Some("notice") => turn.notices.push(update["title"].as_str().unwrap_or("").to_string()),
                _ => {}
            }
        }
        assert!(!matches!(incoming, Incoming::Request { .. }), "no permission requests for MCP calls");
    };
    let result = loop {
        tokio::select! {
            done = &mut request => break done.expect("prompt task").expect("session/prompt"),
            incoming = rx.recv() => handle(incoming.expect("connection closed")),
            _ = tokio::time::sleep(common::WAIT) => panic!("prompt timed out"),
        }
    };
    while let Ok(incoming) = rx.try_recv() {
        handle(incoming);
    }
    (turn, result)
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn acp_client_brings_servers_for_new_and_loaded_sessions() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (mut child, client, mut rx) = env.spawn_rpc(&["acp"]);
    let init = client
        .request("initialize", json!({"protocolVersion": 1, "clientCapabilities": {"session": {"notices": {}}},
            "clientInfo": {"name": "e2e", "version": "1"}}))
        .await
        .unwrap();
    assert_eq!(init["agentCapabilities"]["mcpCapabilities"], json!({"http": true, "sse": false}));
    let cwd = env.workspace("acp");
    let servers = json!([
        {"name": "vault", "command": server_bin(), "args": ["--tools", "40"], "env": [{"name": "MODE", "value": "e2e"}]},
        {"type": "sse", "name": "events", "url": "http://127.0.0.1:9/sse", "headers": []}
    ]);
    let created = client
        .request("session/new", json!({"cwd": cwd, "mcpServers": servers}))
        .await
        .expect("session/new");
    let session = created["sessionId"].as_str().unwrap().to_string();
    let (turn, result) = acp_prompt(&client, &mut rx, &session, "[[mock:mcp:vault:Zed]] go").await;
    assert_eq!(result["stopReason"], "end_turn");
    assert_eq!(turn.reply, released("Zed"));
    assert_eq!(turn.calls, 2, "tool_search, then call_tool");
    // The SSE server was skipped, with a notice right after the session opened.
    assert!(
        turn.notices.iter().any(|n| n.contains("'events'") && n.contains("sse")),
        "{:?}",
        turn.notices
    );

    // A session loaded again gets the servers the client sends with the load.
    client.request("session/close", json!({"sessionId": session})).await.unwrap();
    client
        .request("session/load", json!({"sessionId": session, "cwd": cwd, "mcpServers": servers}))
        .await
        .expect("session/load");
    while rx.try_recv().is_ok() {}
    let (turn, result) = acp_prompt(&client, &mut rx, &session, "[[mock:mcp:vault:Helix]] again").await;
    assert_eq!(result["stopReason"], "end_turn");
    assert_eq!(turn.reply, released("Helix"));
    drop(client);
    common::exit_code(&mut child).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_tool_that_may_change_things_is_refused_when_nobody_can_approve() {
    let env = TestEnv::new("manual", &server_toml("vault", 120)).await;
    let output = env.her(&["exec", "--json", "[[mock:mcp:vault:Apollo]] get the code"]).output().await.unwrap();
    assert_eq!(output.status.code(), Some(3), "{}", String::from_utf8_lossy(&output.stdout));
    let result: Value = serde_json::from_slice(&output.stdout).unwrap();
    assert!(result.to_string().contains("mcp__vault__"), "{result}");
    // The user can trust the server, in their own file.
    let trusted = format!("{}approval = \"never\"\n", server_toml("vault", 120));
    let env = TestEnv::new("manual", &trusted).await;
    let output = env.her(&["exec", "--json", "[[mock:mcp:vault:Apollo]] get the code"]).output().await.unwrap();
    assert_eq!(output.status.code(), Some(0), "{}", String::from_utf8_lossy(&output.stdout));
}

/// `tools/list`: the built-in tools without a session; with one, what it really has.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_tools_of_a_session_include_its_servers() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (mut child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let demux = Demux::spawn(incoming);
    let builtin = client.request("tools/list", json!({})).await.expect("tools/list");
    let names: Vec<&str> = builtin["tools"].as_array().unwrap().iter().filter_map(|t| t["name"].as_str()).collect();
    assert!(names.contains(&"bash") && names.contains(&"browser_navigate"), "{names:?}");
    assert!(!names.iter().any(|name| name.starts_with("mcp__")));

    let cwd = env.workspace("listed");
    // Required: the first request waits for it, so that it is there to list.
    let server = json!({"name": "vault", "command": server_bin(), "required": true});
    let created = client.request("session/new", json!({"cwd": cwd, "mcp_servers": [server]})).await.expect("session/new");
    let session = created["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);
    let turn = native_prompt(&client, &mut events, &session, "say hi", false).await;
    assert_eq!(turn.end["type"], "task_completed");
    let listed = client.request("tools/list", json!({"session_id": session})).await.expect("tools/list");
    let tools = listed["tools"].as_array().unwrap();
    let vault = tools.iter().find(|t| t["name"] == "mcp__vault__vault_reveal_code").unwrap_or_else(|| panic!("{listed:#}"));
    assert_eq!((vault["group"].as_str(), vault["origin"].as_str(), vault["loaded"].as_bool()), (Some("mcp"), Some("mcp"), Some(false)));
    assert!(tools.iter().any(|t| t["name"] == "read" && t["loaded"] == true), "{listed:#}");
    assert!(!tools.iter().any(|t| t["name"] == "browser_navigate"), "only what it has: {listed:#}");
    drop(client);
    common::exit_code(&mut child).await;
}
