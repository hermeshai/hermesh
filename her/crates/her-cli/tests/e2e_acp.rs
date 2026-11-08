//! End-to-end: ACP over `her acp`. Every message from the agent is checked against the
//! official schema types.

mod common;

use agent_client_protocol_schema::v1 as acp;
use common::TestEnv;
use common::next;
use her_rpc::Client;
use her_rpc::Incoming;
use her_rpc::IncomingRx;
use her_rpc::RpcError;
use serde::de::DeserializeOwned;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::time::Duration;

fn conforms<T: DeserializeOwned>(value: &Value, what: &str) -> T {
    serde_json::from_value(value.clone()).unwrap_or_else(|e| panic!("{what} does not match the ACP schema: {e}\n{value:#}"))
}

/// Checks a message from the agent against the schema.
fn check(incoming: &Incoming) {
    match incoming.method() {
        "session/update" => {
            conforms::<acp::SessionNotification>(incoming.params(), "session/update");
        }
        "session/request_permission" => {
            conforms::<acp::RequestPermissionRequest>(incoming.params(), "session/request_permission");
        }
        "$/cancel_request" => {
            conforms::<acp::CancelRequestNotification>(incoming.params(), "$/cancel_request");
        }
        other => panic!("unexpected message from the agent: {other}"),
    }
}

async fn init(client: &Client) -> Value {
    let result = client
        .request("initialize", json!({"protocolVersion": 1, "clientCapabilities": {
            "fs": {"readTextFile": true, "writeTextFile": true}, "terminal": true,
            "session": {"notices": {}}}, "clientInfo": {"name": "e2e", "version": "1"}}))
        .await
        .expect("initialize");
    conforms::<acp::InitializeResponse>(&result, "initialize result");
    result
}

/// Opens a session and waits for the commands announced after the reply.
async fn new_session(client: &Client, rx: &mut IncomingRx, cwd: &Path) -> (String, Value, Vec<Value>) {
    let result = client
        .request("session/new", json!({"cwd": cwd, "mcpServers": []}))
        .await
        .expect("session/new");
    conforms::<acp::NewSessionResponse>(&result, "session/new result");
    let session = result["sessionId"].as_str().unwrap().to_string();
    let (commands, _) = commands_after_open(rx, &session).await;
    (session, result, commands)
}

/// The messages up to the `available_commands_update` that follows opening a session, and
/// its commands.
async fn commands_after_open(rx: &mut IncomingRx, session: &str) -> (Vec<Value>, Vec<Value>) {
    let mut before = Vec::new();
    loop {
        let incoming = next(rx).await;
        check(&incoming);
        let params = incoming.params();
        let update = &params["update"];
        if params["sessionId"] == session && update["sessionUpdate"] == "available_commands_update" {
            return (update["availableCommands"].as_array().unwrap().clone(), before);
        }
        before.push(update.clone());
    }
}

#[derive(Default, Debug)]
struct Turn {
    updates: Vec<Value>,
    text: String,
    permissions: usize,
}

impl Turn {
    fn kinds(&self) -> Vec<&str> {
        self.updates.iter().filter_map(|u| u["sessionUpdate"].as_str()).collect()
    }
}

/// Sends a prompt and handles updates and permission requests until the response arrives.
async fn prompt(client: &Client, rx: &mut IncomingRx, session: &str, text: &str, option: &str) -> (Turn, Result<Value, RpcError>) {
    let request = {
        let client = client.clone();
        let params = json!({"sessionId": session, "prompt": [{"type": "text", "text": text}]});
        tokio::spawn(async move { client.request("session/prompt", params).await })
    };
    let mut turn = Turn::default();
    let mut request = Box::pin(request);
    let result = loop {
        tokio::select! {
            done = &mut request => break done.expect("prompt task"),
            incoming = rx.recv() => {
                let incoming = incoming.expect("connection closed");
                handle(client, &incoming, &mut turn, option);
            }
            _ = tokio::time::sleep(common::WAIT) => panic!("prompt timed out"),
        }
    };
    // Updates precede the response, so whatever is queued now belongs to this turn.
    while let Ok(incoming) = rx.try_recv() {
        handle(client, &incoming, &mut turn, option);
    }
    if let Ok(value) = &result {
        conforms::<acp::PromptResponse>(value, "session/prompt result");
    }
    (turn, result)
}

fn handle(client: &Client, incoming: &Incoming, turn: &mut Turn, option: &str) {
    check(incoming);
    match incoming {
        Incoming::Request { id, method, .. } if method == "session/request_permission" => {
            turn.permissions += 1;
            client.respond(id, json!({"outcome": {"outcome": "selected", "optionId": option}}));
        }
        Incoming::Notification { method, params, .. } if method == "session/update" => {
            let update = params["update"].clone();
            if update["sessionUpdate"] == "agent_message_chunk" {
                turn.text.push_str(update["content"]["text"].as_str().unwrap_or(""));
            }
            turn.updates.push(update);
        }
        _ => {}
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_prompt_at_the_size_limit_goes_through_and_one_over_it_gets_its_answer() {
    let env = TestEnv::new("manual", "[server]\nmax_frame_bytes = 65536\n").await;
    let (mut child, client, mut rx) = env.spawn_rpc(&["acp"]);
    init(&client).await;
    let cwd = env.workspace("acp");
    let (session, _, _) = new_session(&client, &mut rx, &cwd).await;

    let (turn, result) = prompt(&client, &mut rx, &session, &"x".repeat(65_000), "approve").await;
    assert_eq!(result.unwrap()["stopReason"], "end_turn");
    assert!(!turn.text.is_empty());

    let params = json!({"sessionId": session, "prompt": [{"type": "text", "text": "x".repeat(66_000)}]});
    let answer = tokio::time::timeout(common::WAIT, client.request("session/prompt", params));
    let err = answer.await.expect("no answer").unwrap_err();
    assert_eq!(err.code, her_rpc::code::INVALID_REQUEST);
    assert!(err.message.contains("65536"), "{}", err.message);

    // The session goes on.
    let (_, result) = prompt(&client, &mut rx, &session, "again", "approve").await;
    assert_eq!(result.unwrap()["stopReason"], "end_turn");
    drop(client);
    assert_eq!(common::exit_code(&mut child).await, 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn acp_runs_tools_with_permissions() {
    let env = TestEnv::new("manual", "").await;
    let (mut child, client, mut rx) = env.spawn_rpc(&["acp"]);
    let init = init(&client).await;
    assert_eq!(init["agentCapabilities"]["loadSession"], true);
    let cwd = env.workspace("acp");
    let (session, created, _) = new_session(&client, &mut rx, &cwd).await;
    let options = created["configOptions"].as_array().unwrap();
    assert_eq!(options.len(), 3);
    assert_eq!(options[0]["currentValue"], "mock/mock-1");

    let (turn, result) = prompt(&client, &mut rx, &session, "[[mock:files]] go", "approve_for_session").await;
    assert_eq!(result.unwrap()["stopReason"], "end_turn");
    assert_eq!(turn.permissions, 1);
    let calls: Vec<&Value> = turn.updates.iter().filter(|u| u["sessionUpdate"] == "tool_call").collect();
    assert_eq!(calls.len(), 3);
    assert_eq!(calls[0]["kind"], "edit");
    assert_eq!(calls[0]["locations"][0]["path"], json!(cwd.join("notes.txt")));
    let finished: Vec<&Value> = turn
        .updates
        .iter()
        .filter(|u| u["sessionUpdate"] == "tool_call_update" && u["status"] == "completed")
        .collect();
    assert_eq!(finished.len(), 3);
    assert_eq!(finished[0]["content"][0]["type"], "diff", "a new file shows as a diff");
    assert!(finished[0]["content"][0].get("oldText").is_none());
    assert_eq!(finished[2]["content"][0]["oldText"], "alpha");
    assert_eq!(finished[2]["content"][0]["newText"], "gamma");
    let usage = turn.updates.iter().find(|u| u["sessionUpdate"] == "usage_update").unwrap();
    assert_eq!(usage["size"], 200000);
    assert!(usage["cost"]["amount"].as_f64().unwrap() > 0.0);
    assert_eq!(turn.text, "files done");
    assert_eq!(std::fs::read_to_string(cwd.join("notes.txt")).unwrap(), "gamma\nbeta\n");

    // "Always" for a file change switched the session to accept-edits, where `touch` in the
    // workspace no longer asks. A new session (manual) checks a denied permission: the call
    // fails and the task still ends normally.
    let cwd = env.workspace("acp-deny");
    let (session, _, _) = new_session(&client, &mut rx, &cwd).await;
    let (turn, result) = prompt(&client, &mut rx, &session, "[[mock:bash:touch denied.txt]] go", "deny").await;
    assert_eq!(result.unwrap()["stopReason"], "end_turn");
    assert_eq!(turn.permissions, 1);
    assert!(turn.updates.iter().any(|u| u["sessionUpdate"] == "tool_call_update" && u["status"] == "failed"));
    assert!(!cwd.join("denied.txt").exists());

    drop(client);
    assert_eq!(common::exit_code(&mut child).await, 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn acp_asks_the_client_what_a_sub_agent_asks() {
    let env = TestEnv::new("manual", "").await;
    let (mut child, client, mut rx) = env.spawn_rpc(&["acp"]);
    init(&client).await;
    let cwd = env.workspace("acp");
    let (session, _, _) = new_session(&client, &mut rx, &cwd).await;

    // The prompt is answered once the sub-agent is done, so the question comes before that.
    let request = {
        let client = client.clone();
        let params = json!({"sessionId": session, "prompt": [{"type": "text", "text": "[[mock:delegate:touch made.txt]] run it"}]});
        tokio::spawn(async move { client.request("session/prompt", params).await })
    };
    let mut asked = None;
    let mut request = Box::pin(request);
    let result = loop {
        tokio::select! {
            done = &mut request => break done.expect("prompt task"),
            incoming = rx.recv() => {
                let incoming = incoming.expect("connection closed");
                check(&incoming);
                if let Incoming::Request { id, method, params, .. } = &incoming && method == "session/request_permission" {
                    assert!(asked.is_none(), "one question");
                    assert!(!cwd.join("made.txt").exists());
                    asked = Some(params.clone());
                    client.respond(id, json!({"outcome": {"outcome": "selected", "optionId": "approve"}}));
                }
            }
            _ = tokio::time::sleep(common::WAIT) => panic!("prompt timed out"),
        }
    };
    assert_eq!(result.unwrap()["stopReason"], "end_turn");
    let asked: Value = asked.expect("the client was asked");
    let tool = &asked["toolCall"];
    // Who asks shows, and its id is apart from the session's calls.
    assert!(tool["toolCallId"].as_str().unwrap().starts_with("agent-"), "{tool}");
    assert_eq!(tool["title"], "运行 touch made.txt: touch made.txt");
    assert!(cwd.join("made.txt").exists());
    drop(client);
    common::exit_code(&mut child).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn acp_settings_cancellation_and_busy_prompts() {
    let env = TestEnv::new("manual", "").await;
    let (mut child, client, mut rx) = env.spawn_rpc(&["acp"]);
    init(&client).await;
    let (session, _, _) = new_session(&client, &mut rx, &env.workspace("cancel")).await;

    let result = client
        .request("session/set_config_option", json!({"sessionId": session, "configId": "permission_mode", "value": "bypass-permissions"}))
        .await
        .unwrap();
    conforms::<acp::SetSessionConfigOptionResponse>(&result, "set_config_option result");
    assert_eq!(result["configOptions"][2]["id"], "permission_mode");
    assert_eq!(result["configOptions"][2]["currentValue"], "bypass-permissions");
    let update = next(&mut rx).await;
    check(&update);
    assert_eq!(update.params()["update"]["sessionUpdate"], "config_option_update");

    // bypass-permissions: no permission request for a command.
    let (turn, result) = prompt(&client, &mut rx, &session, "[[mock:bash:touch made.txt]] go", "deny").await;
    assert_eq!(result.unwrap()["stopReason"], "end_turn");
    assert_eq!(turn.permissions, 0);
    assert!(turn.kinds().contains(&"tool_call"));

    // Cancel a slow reply after its first chunk; a second prompt meanwhile is refused.
    let slow = {
        let client = client.clone();
        let params = json!({"sessionId": session, "prompt": [{"type": "text", "text": "[[mock:slow:100]] take your time"}]});
        tokio::spawn(async move { client.request("session/prompt", params).await })
    };
    loop {
        let incoming = next(&mut rx).await;
        check(&incoming);
        if incoming.params()["update"]["sessionUpdate"] == "agent_message_chunk" {
            break;
        }
    }
    let busy = client
        .request("session/prompt", json!({"sessionId": session, "prompt": [{"type": "text", "text": "hi"}]}))
        .await
        .unwrap_err();
    assert_eq!(busy.kind(), Some("prompt_in_progress"));
    client.notify("session/cancel", json!({"sessionId": session}));
    let result = tokio::time::timeout(Duration::from_secs(10), slow).await.unwrap().unwrap().unwrap();
    assert_eq!(result["stopReason"], "cancelled");
    while let Ok(incoming) = rx.try_recv() {
        check(&incoming);
    }

    // The session works again after the cancellation.
    let (turn, result) = prompt(&client, &mut rx, &session, "[[mock:echo]] again", "deny").await;
    assert_eq!(result.unwrap()["stopReason"], "end_turn");
    assert_eq!(turn.text, "echo: again");

    drop(client);
    assert_eq!(common::exit_code(&mut child).await, 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn acp_lists_loads_resumes_and_deletes_sessions() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (mut child, client, mut rx) = env.spawn_rpc(&["acp"]);
    init(&client).await;
    let cwd = env.workspace("history");
    let (session, _, _) = new_session(&client, &mut rx, &cwd).await;
    let (_, result) = prompt(&client, &mut rx, &session, "[[mock:bash:echo history]] remember this", "deny").await;
    assert_eq!(result.unwrap()["stopReason"], "end_turn");
    let result = client.request("session/close", json!({"sessionId": session})).await.unwrap();
    conforms::<acp::CloseSessionResponse>(&result, "session/close result");

    let list = client.request("session/list", json!({"cwd": cwd})).await.unwrap();
    conforms::<acp::ListSessionsResponse>(&list, "session/list result");
    assert_eq!(list["sessions"][0]["sessionId"], json!(session));
    assert_eq!(list["sessions"][0]["title"], "[[mock:bash:echo history]] remember this");

    // Load replays the conversation before answering.
    let loaded = client
        .request("session/load", json!({"sessionId": session, "cwd": cwd, "mcpServers": []}))
        .await
        .unwrap();
    conforms::<acp::LoadSessionResponse>(&loaded, "session/load result");
    // The replay came before the reply; the commands and live notices (the bypass-permissions
    // warning of the resumed session) follow it, in either order.
    let is_bypass = |n: &Value| n["sessionUpdate"] == "notice" && n["title"].as_str().unwrap_or("").contains("bypass-permissions");
    let (commands, before) = commands_after_open(&mut rx, &session).await;
    assert_eq!(commands[0]["name"], "compact");
    let (mut notices, replayed): (Vec<Value>, Vec<Value>) = before.into_iter().partition(|u| u["sessionUpdate"] == "notice");
    while !notices.iter().any(is_bypass) {
        let incoming = next(&mut rx).await;
        check(&incoming);
        notices.push(incoming.params()["update"].clone());
    }
    let kinds: Vec<&str> = replayed.iter().filter_map(|u| u["sessionUpdate"].as_str()).collect();
    assert_eq!(kinds.first(), Some(&"user_message_chunk"));
    assert!(kinds.contains(&"tool_call"));
    let call = replayed.iter().find(|u| u["sessionUpdate"] == "tool_call").unwrap();
    assert_eq!(call["status"], "completed");
    assert_eq!(call["kind"], "execute");
    assert_eq!(
        replayed.last().unwrap()["content"]["text"],
        "bash done",
        "the final reply is replayed last"
    );

    client.request("session/close", json!({"sessionId": session})).await.unwrap();
    let resumed = client
        .request("session/resume", json!({"sessionId": session, "cwd": cwd}))
        .await
        .unwrap();
    conforms::<acp::ResumeSessionResponse>(&resumed, "session/resume result");
    let (_, before) = commands_after_open(&mut rx, &session).await;
    while let Ok(incoming) = rx.try_recv() {
        assert_eq!(incoming.params()["update"]["sessionUpdate"], "notice", "resume does not replay");
    }
    assert!(before.iter().all(|u| u["sessionUpdate"] == "notice"), "resume does not replay: {before:?}");
    let (turn, result) = prompt(&client, &mut rx, &session, "[[mock:echo]] continued", "deny").await;
    assert_eq!(result.unwrap()["stopReason"], "end_turn");
    assert_eq!(turn.text, "echo: continued");

    let deleted = client.request("session/delete", json!({"sessionId": session})).await.unwrap();
    conforms::<acp::DeleteSessionResponse>(&deleted, "session/delete result");
    let list = client.request("session/list", json!({"cwd": cwd})).await.unwrap();
    assert!(list["sessions"].as_array().unwrap().is_empty());
    assert!(env.home.path().join("trash").join("sessions").read_dir().unwrap().next().is_some());

    drop(client);
    assert_eq!(common::exit_code(&mut child).await, 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn acp_announces_commands_and_runs_skills() {
    let env = TestEnv::new("manual", "").await;
    let skill = env.home.path().join("skills/gen-report");
    std::fs::create_dir_all(skill.join("scripts")).unwrap();
    std::fs::write(
        skill.join("SKILL.md"),
        "---\nname: gen-report\ndescription: Generate the weekly report.\nargument-hint: \"[week]\"\n---\n\
         Run `scripts/gen.sh` from the project directory.\n",
    )
    .unwrap();
    std::fs::write(skill.join("scripts/gen.sh"), "echo WEEKLY-REPORT > report.md\n").unwrap();
    let style = env.home.path().join("skills/commit-style");
    std::fs::create_dir_all(&style).unwrap();
    std::fs::write(style.join("SKILL.md"), "---\nname: commit-style\ndescription: Commit messages.\n---\nTag it.\n").unwrap();
    let (mut child, client, mut rx) = env.spawn_rpc(&["acp"]);
    init(&client).await;
    let cwd = env.workspace("commands");
    let (session, _, commands) = new_session(&client, &mut rx, &cwd).await;
    assert_eq!(
        commands,
        vec![
            json!({"name": "compact", "description": "Summarize the conversation so far to free up the context window"}),
            json!({"name": "context", "description": "Show what fills the context window"}),
            json!({"name": "skill:commit-style", "description": "Commit messages."}),
            json!({"name": "skill:gen-report", "description": "Generate the weekly report.", "input": {"hint": "[week]"}}),
            // Built into her (docs/desktop/customize-design.md §4.6).
            json!({"name": "skill:skill-creator", "description": "Create, improve, and evaluate skills using realistic test cases, baseline comparisons, and user feedback."}),
        ]
    );

    // `/skill:name` anywhere brings each skill's instructions with the message; `/compact` compacts.
    let typed = "请用 /skill:gen-report [[mock:invoked]] 整理本周进展，再按 /skill:commit-style 写提交信息";
    let (turn, result) = prompt(&client, &mut rx, &session, typed, "approve").await;
    assert_eq!(result.unwrap()["stopReason"], "end_turn");
    assert_eq!(turn.text, "invoked done");
    assert_eq!(turn.permissions, 1, "only the script asks");
    assert_eq!(std::fs::read_to_string(cwd.join("report.md")).unwrap(), "WEEKLY-REPORT\n");
    let (turn, _) = prompt(&client, &mut rx, &session, "[[mock:context]]", "deny").await;
    assert_eq!(turn.text, "skills in context: gen-report, commit-style");
    let (_, result) = prompt(&client, &mut rx, &session, "/compact", "deny").await;
    assert_eq!(result.unwrap()["stopReason"], "end_turn");

    // The replay shows what the user typed, not the instructions.
    client.request("session/close", json!({"sessionId": session})).await.unwrap();
    client
        .request("session/load", json!({"sessionId": session, "cwd": cwd, "mcpServers": []}))
        .await
        .unwrap();
    let (commands, replayed) = commands_after_open(&mut rx, &session).await;
    assert_eq!(commands.len(), 5);
    let user = replayed.iter().find(|u| u["sessionUpdate"] == "user_message_chunk").unwrap();
    assert_eq!(user["content"]["text"], typed);

    drop(client);
    assert_eq!(common::exit_code(&mut child).await, 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn acp_rejects_what_it_does_not_support() {
    let env = TestEnv::new("manual", "").await;
    let (mut child, client, mut rx) = env.spawn_rpc(&["acp"]);
    let err = client.request("session/list", json!({})).await.unwrap_err();
    assert_eq!(err.kind(), Some("not_initialized"));
    init(&client).await;
    let err = client.request("authenticate", json!({"methodId": "x"})).await.unwrap_err();
    assert_eq!(err.code, -32602, "no such way to sign in");
    let (session, _, _) = new_session(&client, &mut rx, &env.workspace("audio")).await;
    let err = client
        .request("session/prompt", json!({"sessionId": session, "prompt": [{"type": "audio", "data": "AAAA", "mimeType": "audio/wav"}]}))
        .await
        .unwrap_err();
    assert_eq!(err.kind(), Some("unsupported_content"));
    let err = client
        .request("session/prompt", json!({"sessionId": "missing", "prompt": [{"type": "text", "text": "hi"}]}))
        .await
        .unwrap_err();
    assert_eq!(err.kind(), Some("session_not_found"));
    drop(client);
    assert_eq!(common::exit_code(&mut child).await, 0);
}
