//! The `mesh` group end to end: the real `her serve`, Her driven by the mock model's `mesh`
//! scenario, and this test as the mesh host on her connection. The host gives the group, says
//! what a write would do, runs the tools and brings news; a client answers the approval as the
//! owner's device does.

mod common;

use common::SchemaCheck;
use common::TestEnv;
use common::WAIT;
use common::native_init;
use her_rpc::Client;
use her_rpc::Incoming;
use her_rpc::IncomingRx;
use serde_json::Value;
use serde_json::json;
use tokio::sync::mpsc;

/// What the server sends, split: session events, and its requests to the mesh host, which are
/// answered as they come.
struct Wire {
    events: mpsc::UnboundedReceiver<Value>,
    /// Each `mesh/describe` and `mesh/call`: its method and parameters.
    asked: mpsc::UnboundedReceiver<(String, Value)>,
}

/// The host's side: a `start` is described by its workshop, which is also its rule, and every
/// call answers a document that names the tool.
fn route(mut incoming: IncomingRx, client: Client) -> Wire {
    let (events_tx, events) = mpsc::unbounded_channel();
    let (asked_tx, asked) = mpsc::unbounded_channel();
    tokio::spawn(async move {
        while let Some(message) = incoming.recv().await {
            match message {
                Incoming::Notification { method, params, .. } if method == "session/event" => {
                    let _ = events_tx.send(params["event"].clone());
                }
                Incoming::Request { id, method, params, .. } => {
                    let workshop = params["arguments"]["workshop"].as_str().unwrap_or_default();
                    match method.as_str() {
                        "mesh/describe" => client.respond(&id, json!({"summary": format!("Start work in {workshop}"), "rule": ["mesh", workshop]})),
                        "mesh/call" => client.respond(&id, json!({"output": json!({"ok": true, "tool": params["name"]}).to_string()})),
                        other => panic!("an unexpected request: {other}"),
                    }
                    let _ = asked_tx.send((method, params));
                }
                Incoming::Notification { .. } => {}
            }
        }
    });
    Wire { events, asked }
}

fn ended(event: &Value) -> bool {
    matches!(event["type"].as_str(), Some("task_completed" | "task_failed" | "task_interrupted"))
}

/// Events until one matches `stop`, inclusive.
async fn until(wire: &mut Wire, stop: impl Fn(&Value) -> bool) -> Vec<Value> {
    let mut seen = Vec::new();
    loop {
        let event = tokio::time::timeout(WAIT, wire.events.recv()).await.unwrap_or_else(|_| panic!("timed out waiting for an event, after: {seen:#?}")).expect("closed");
        let done = stop(&event);
        seen.push(event);
        if done {
            return seen;
        }
    }
}

/// What Her sent the owner among `events`.
fn sent(events: &[Value]) -> Vec<String> {
    events
        .iter()
        .filter(|e| e["type"] == "tool_started" && e["name"] == "send_to_user")
        .filter_map(|e| e["args"]["text"].as_str().map(str::to_string))
        .collect()
}

async fn asked(wire: &mut Wire) -> (String, Value) {
    tokio::time::timeout(WAIT, wire.asked.recv()).await.expect("timed out waiting for a request to the host").expect("closed")
}

async fn say(client: &Client, session: &str, text: &str) {
    let op = json!({"session_id": session, "op": {"type": "prompt", "input": [{"type": "text", "text": text}]}});
    client.request("session/op", op).await.expect("prompt accepted");
}

#[tokio::test]
async fn her_uses_the_hosts_tools_asks_about_its_writes_and_is_woken_by_its_news() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming, client.clone());
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let opened = client
        .request("bots/open", json!({"bot_id": bot, "events": {"exclude": ["text_delta", "thinking_delta", "tool_call_delta", "tool_progress"]}}))
        .await
        .expect("bots/open");
    let session = opened["session_id"].as_str().unwrap().to_string();
    let start = r#"[[mock:mesh:start:{"workshop":"shed","task":"a shelf: oak"}]]"#;

    // Without a host there is no such tool.
    say(&client, &session, start).await;
    let events = until(&mut wire, ended).await;
    assert_eq!(sent(&events), ["mesh failed: Unknown tool 'start'. Use tool_search to find tools."]);

    // The host gives the group: the conversation that is already running has it.
    let group = json!({
        "summary": "Work in the user's other workshops",
        "guidelines": ["Never wait for a workshop."],
        "tools": [
            {"name": "start", "description": "Start work in a workshop.", "parameters": {"type": "object", "properties": {"workshop": {"type": "string"}}}, "writes": true},
            {"name": "look", "description": "See what a workshop holds.", "parameters": {"type": "object"}, "writes": false},
        ],
    });
    assert_eq!(client.request("mesh/host", group).await.unwrap(), json!({}));
    say(&client, &session, start).await;
    let events = until(&mut wire, |e| e["type"] == "approval_requested").await;
    let found = events.iter().find(|e| e["type"] == "tool_completed" && e["result"]["tool_name"] == "tool_search").expect("the search");
    let found = found["result"]["content"][0]["text"].as_str().unwrap();
    assert!(found.starts_with("Group mesh: Work in the user's other workshops.\n  Never wait for a workshop.\n- start: Start work in a workshop.\n"), "{found}");
    // The owner is asked in the host's words, and approves for the session.
    let ask = events.last().unwrap();
    assert_eq!(ask["summary"], "Start work in shed");
    assert_eq!(ask["proposed_rule"], json!({"prefix": ["mesh", "shed"], "decision": "allow"}));
    let schema = SchemaCheck::new(&["MeshDescribeParams", "MeshCallParams"]);
    let (method, described) = asked(&mut wire).await;
    assert_eq!(method, "mesh/describe");
    schema.check("MeshDescribeParams", &described);
    let arguments = json!({"workshop": "shed", "task": "a shelf: oak"});
    assert_eq!(described, json!({"bot": bot, "name": "start", "arguments": arguments}));
    let op = json!({"session_id": session, "op": {"type": "approval", "call_id": ask["call_id"], "decision": {"type": "approve_for_session"}}});
    client.request("session/op", op).await.expect("approval accepted");
    let events = until(&mut wire, ended).await;
    let (method, called) = asked(&mut wire).await;
    assert_eq!(method, "mesh/call");
    schema.check("MeshCallParams", &called);
    assert_eq!(called, json!({"bot": bot, "call_id": ask["call_id"], "name": "start", "arguments": arguments}));
    assert_eq!(sent(&events), [r#"mesh done: {"ok":true,"tool":"start"}"#]);

    // The rule is kept: the same workshop runs unasked, described first; a tool that only reads
    // is neither asked about nor described.
    say(&client, &session, r#"[[mock:mesh:start:{"workshop":"shed"}]]"#).await;
    let events = until(&mut wire, ended).await;
    assert!(!events.iter().any(|e| e["type"] == "approval_requested"));
    assert_eq!((asked(&mut wire).await.0.as_str(), asked(&mut wire).await.0.as_str()), ("mesh/describe", "mesh/call"));
    say(&client, &session, "[[mock:mesh:look]]").await;
    let events = until(&mut wire, ended).await;
    assert!(!events.iter().any(|e| e["type"] == "approval_requested"));
    assert_eq!(asked(&mut wire).await, ("mesh/call".to_string(), json!({"bot": bot, "call_id": called_id(&events), "name": "look", "arguments": {}})));
    assert_eq!(sent(&events), [r#"mesh done: {"ok":true,"tool":"look"}"#]);

    // News wakes her, and its sender hears once she has it.
    let news = "<news id=\"n1\">The shelf is built.</news>";
    let from = json!({"id": "shed", "name": "Shed"});
    assert_eq!(client.request("mesh/news", json!({"bot": bot, "id": "n1", "from": from, "text": news})).await.unwrap(), json!({}));
    let events = until(&mut wire, ended).await;
    let woke = events.iter().find(|e| e["type"] == "task_started").expect("a task");
    assert_eq!(woke["trigger"], "mail");
    let mail = events.iter().find(|e| e["type"] == "message_completed" && !e["mail"].is_null()).expect("the news as mail");
    assert_eq!(mail["mail"], json!({"id": "n1", "from": from, "kind": "news", "status": "completed"}));
    assert_eq!(sent(&events), [format!("回复：{news}")]);
    // A directive in the news chooses the mock's reply, as one in a message does.
    let news = json!({"bot": bot, "id": "n2", "from": from, "text": "<news id=\"n2\">[[mock:send:The door is hung.]]</news>"});
    client.request("mesh/news", news).await.unwrap();
    assert_eq!(sent(&until(&mut wire, ended).await), ["The door is hung."]);

    // News for nobody is an error.
    let err = client.request("mesh/news", json!({"bot": "nobody", "id": "n3", "from": from, "text": "t"})).await.unwrap_err();
    assert_eq!(err.kind(), Some("bot_not_found"), "{err:?}");

    // The host takes its tools back: the group goes.
    client.request("mesh/host", json!({"summary": "", "guidelines": [], "tools": []})).await.unwrap();
    say(&client, &session, "[[mock:mesh:look]]").await;
    assert_eq!(sent(&until(&mut wire, ended).await), ["mesh failed: Unknown tool 'look'. Use tool_search to find tools."]);
}

/// The id of the `call_tool` call among `events`.
fn called_id(events: &[Value]) -> Value {
    events.iter().find(|e| e["type"] == "tool_started" && e["name"] == "call_tool").expect("a call")["call_id"].clone()
}
