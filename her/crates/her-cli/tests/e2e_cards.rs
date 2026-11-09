//! Secure cards end to end (docs/her-secure-cards.md, step 1): the real `her serve`, Her driven by
//! the mock model's `connect` scenario, and the MCP test server as the connector. Her prepares a
//! card and shows it; a client answers it as the owner's device does, and signs in as the console
//! does; the card ends, Her hears how and passes it on.

mod common;

use common::SchemaCheck;
use common::TestEnv;
use common::WAIT;
use common::native_init;
use her_mock::mcp::McpTestOptions;
use her_mock::mcp_auth::McpTestAuth;
use her_mock::mcp_auth::OAuthOptions;
use her_rpc::Client;
use her_rpc::IncomingRx;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;
use tokio::io::AsyncReadExt;
use tokio::io::AsyncWriteExt;
use tokio::sync::mpsc;

/// What the server sends, split: `bots/changed` rows, and session events.
struct Wire {
    bots: mpsc::UnboundedReceiver<Value>,
    events: mpsc::UnboundedReceiver<Value>,
}

fn route(mut incoming: IncomingRx) -> Wire {
    let (bots_tx, bots) = mpsc::unbounded_channel();
    let (events_tx, events) = mpsc::unbounded_channel();
    tokio::spawn(async move {
        while let Some(message) = incoming.recv().await {
            match message.method() {
                "bots/changed" => {
                    for row in message.params()["bots"].as_array().into_iter().flatten() {
                        let _ = bots_tx.send(row.clone());
                    }
                }
                "session/event" => {
                    let _ = events_tx.send(message.params()["event"].clone());
                }
                _ => {}
            }
        }
    });
    Wire { bots, events }
}

/// Events until one matches `stop`, inclusive.
async fn until(wire: &mut Wire, stop: impl Fn(&Value) -> bool) -> Vec<Value> {
    let mut seen = Vec::new();
    loop {
        let event = tokio::time::timeout(WAIT, wire.events.recv()).await.expect("timed out waiting for an event").expect("closed");
        let done = stop(&event);
        seen.push(event);
        if done {
            return seen;
        }
    }
}

/// The first row that matches `want`.
async fn until_row(wire: &mut Wire, want: impl Fn(&Value) -> bool) -> Value {
    loop {
        let row = tokio::time::timeout(WAIT, wire.bots.recv()).await.expect("timed out waiting for bots/changed").expect("closed");
        if want(&row) {
            return row;
        }
    }
}

fn ended(event: &Value) -> bool {
    matches!(event["type"].as_str(), Some("task_completed" | "task_failed" | "task_interrupted"))
}

/// The card's `card_changed` to `state`; a state the card should not reach fails the test.
fn changed_to(id: &str, state: &'static str) -> impl Fn(&Value) -> bool {
    let id = id.to_string();
    move |e| {
        if e["type"] != "card_changed" || e["card_id"] != id.as_str() {
            return false;
        }
        let now = e["state"].as_str().unwrap_or_default();
        assert!(now == state || matches!(now, "open" | "working" | "sign_in"), "card {id} went {now}: {e}");
        now == state
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

struct Her {
    client: Client,
    wire: Wire,
    child: tokio::process::Child,
    bot: String,
    session: String,
}

async fn her(env: &TestEnv) -> Her {
    let (child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;
    Her { client, wire, child, bot, session }
}

async fn open(client: &Client, bot: &str) -> String {
    let opened = client
        .request("bots/open", json!({"bot_id": bot, "events": {"exclude": ["text_delta", "thinking_delta", "tool_call_delta", "tool_progress"]}}))
        .await
        .expect("bots/open");
    opened["session_id"].as_str().unwrap().to_string()
}

impl Her {
    async fn say(&self, text: &str) {
        let op = json!({"session_id": self.session, "op": {"type": "prompt", "input": [{"type": "text", "text": text}]}});
        self.client.request("session/op", op).await.expect("prompt accepted");
    }

    /// Asks Her for a connector; the card she shows and the words above it.
    async fn ask_for(&mut self, name: &str, url: &str) -> (Value, String) {
        self.say(&format!("[[mock:connect:{name}|{url}]] Connect it.")).await;
        let events = until(&mut self.wire, ended).await;
        let details = events
            .iter()
            .find(|e| e["type"] == "tool_completed" && e["result"]["tool_name"] == "send_to_user" && e["result"]["is_error"] == false)
            .map(|e| e["result"]["details"].clone())
            .unwrap_or_else(|| panic!("no card: {events:#?}"));
        (details["card"].clone(), details["text"].as_str().unwrap_or_default().to_string())
    }

    async fn answer(&self, card: &Value, answer: Value) -> Result<Value, her_rpc::RpcError> {
        self.client.request("cards/answer", json!({"session_id": self.session, "card_id": card["id"], "answer": answer})).await
    }

    /// The card's ops, each value the owner types filled in with `value`.
    fn filled(card: &Value, value: &str) -> Value {
        let text = card["ops"].to_string().replace("<value:f1>", value);
        serde_json::from_str(&text).unwrap()
    }

    /// Waits for the card to end in `state`, then for Her to pass on its note; returns the note.
    async fn passed_on(&mut self, card: &Value, state: &'static str) -> String {
        let events = until(&mut self.wire, changed_to(card["id"].as_str().unwrap(), state)).await;
        let note = events.last().unwrap()["note"].as_str().expect("her words on the card").to_string();
        let told = until(&mut self.wire, ended).await;
        assert_eq!(sent(&told), [format!("转告：{note}")]);
        note
    }

    /// Events until the task ends, each approval asked on the way given.
    async fn approving(&mut self) -> Vec<Value> {
        let mut seen = Vec::new();
        loop {
            let event = tokio::time::timeout(WAIT, self.wire.events.recv()).await.expect("timed out waiting for an event").expect("closed");
            if event["type"] == "approval_requested" {
                let op = json!({"session_id": self.session, "op": {"type": "approval", "call_id": event["call_id"], "decision": {"type": "approve"}}});
                self.client.request("session/op", op).await.expect("approval accepted");
            }
            let end = ended(&event);
            seen.push(event);
            if end {
                return seen;
            }
        }
    }

    /// Stops the service; what it wrote to stderr.
    async fn stop(mut self) -> String {
        drop(self.client);
        let mut stderr = String::new();
        if let Some(mut pipe) = self.child.stderr.take() {
            let _ = tokio::time::timeout(WAIT, pipe.read_to_string(&mut stderr)).await;
        }
        common::exit_code(&mut self.child).await;
        stderr
    }
}

async fn oauth_server() -> her_mock::mcp::McpHttpServer {
    let auth = McpTestAuth::OAuth(OAuthOptions { metadata_document: false, registration: true, token_ttl_secs: 3600 });
    her_mock::mcp::serve_http("127.0.0.1:0", McpTestOptions { auth, ..Default::default() }).await.unwrap()
}

fn user_config(env: &TestEnv) -> String {
    std::fs::read_to_string(env.home.path().join("config.toml")).unwrap()
}

/// Plays the client's device: the browser lands on its port with the redirect, which it answers
/// and reads.
async fn catch_redirect(device: tokio::net::TcpListener) -> HashMap<String, String> {
    let (mut stream, _) = device.accept().await.unwrap();
    let mut head = Vec::new();
    let mut chunk = [0u8; 4096];
    while !head.windows(4).any(|w| w == b"\r\n\r\n") {
        let n = stream.read(&mut chunk).await.unwrap();
        assert!(n > 0, "the browser hung up");
        head.extend_from_slice(&chunk[..n]);
    }
    stream.write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 6\r\nConnection: close\r\n\r\ncaught").await.unwrap();
    let head = String::from_utf8_lossy(&head);
    let target = head.lines().next().unwrap().split(' ').nth(1).unwrap();
    her_mock::mcp_auth::form(target.split_once('?').unwrap().1)
}

/// Signs in to `name` as the console does on a device: `mcp/login` with its own redirect, the
/// browser, then `mcp/login_complete` with what the redirect brought.
async fn sign_in(client: &Client, name: &str) {
    let device = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let redirect = format!("http://127.0.0.1:{}/callback", device.local_addr().unwrap().port());
    let login = client.request("mcp/login", json!({"name": name, "redirect_uri": redirect})).await.unwrap();
    let caught = tokio::spawn(catch_redirect(device));
    her_mock::mcp_auth::browse(login["authorize_url"].as_str().unwrap()).await.unwrap();
    let query = caught.await.unwrap();
    let complete = json!({"login_id": login["login_id"], "code": query["code"], "state": query["state"], "iss": query["iss"]});
    client.request("mcp/login_complete", complete).await.unwrap();
}

/// Has the test server's authorization server forget `token`.
async fn revoke(url: &str, token: &str) {
    let host = url.strip_prefix("http://").unwrap().split('/').next().unwrap().to_string();
    let body = format!("token={token}");
    let mut stream = tokio::net::TcpStream::connect(&host).await.unwrap();
    let request = format!("POST /revoke HTTP/1.1\r\nHost: {host}\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len());
    stream.write_all(request.as_bytes()).await.unwrap();
    let mut answer = [0u8; 12];
    stream.read_exact(&mut answer).await.unwrap();
    assert_eq!(&answer, b"HTTP/1.1 200");
}

/// Every file under `dir`, with its text.
fn files(dir: &Path) -> Vec<(PathBuf, String)> {
    let mut out = Vec::new();
    for entry in std::fs::read_dir(dir).into_iter().flatten().flatten() {
        let path = entry.path();
        if path.is_dir() {
            out.extend(files(&path));
        } else {
            out.push((path.clone(), String::from_utf8_lossy(&std::fs::read(&path).unwrap_or_default()).into_owned()));
        }
    }
    out
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn her_connects_an_oauth_connector_on_a_card() {
    let env = TestEnv::new("manual", "").await;
    let schema = SchemaCheck::new(&["Card", "CardsAnswerResult", "Event", "BotsChangedParams"]);
    let mock = oauth_server().await;
    let mut her = her(&env).await;

    // Asked by the owner, Her shows a card made from the address as her checked it.
    let (card, words) = her.ask_for("tracker", &mock.url()).await;
    schema.check("Card", &card);
    let id = card["id"].as_str().unwrap().to_string();
    assert!(id.starts_with("c_") && id.len() == 12, "{id}");
    assert_eq!((card["kind"].as_str(), card["title"].as_str(), card["asked_by"].as_str()), (Some("connect"), Some("Connect Tracker"), Some("owner")));
    assert_eq!((card["body"]["url"].as_str(), card["body"]["host"].as_str(), card["body"]["sign_in"].as_str()), (Some(mock.url().as_str()), Some("127.0.0.1"), Some("oauth")));
    assert_eq!(card["body"]["fields"], json!([]));
    assert!(!user_config(&env).contains("tracker"), "nothing is written before the owner answers");
    // Her's row says she waits for the owner, in her words.
    let row = until_row(&mut her.wire, |row| row["waiting"].is_string()).await;
    assert_eq!(row["waiting"], words.as_str());

    // Ops that are not the card's are refused, and the card waits on.
    let mut more = card["ops"].as_array().unwrap().clone();
    more.push(json!({"method": "config/write", "params": {"layer": "user", "edits": [{"key_path": "web_search.enabled", "value": false}]}}));
    let refused = her.answer(&card, json!({"decision": "approve", "ops": more})).await.unwrap_err();
    assert_eq!(refused.code, -32602, "{refused:?}");

    // Approved: her writes the connector as the console would, and waits for its sign-in.
    let approved = her.answer(&card, json!({"decision": "approve", "ops": card["ops"]})).await.unwrap();
    schema.check("CardsAnswerResult", &approved);
    assert!(matches!(approved["state"].as_str(), Some("working" | "sign_in")), "{approved}");
    let events = until(&mut her.wire, changed_to(&id, "sign_in")).await;
    schema.check("Event", events.last().unwrap());
    assert!(user_config(&env).contains("[mcp_servers.tracker]") && user_config(&env).contains("auth = \"oauth\""), "{}", user_config(&env));
    // Approving again changes nothing.
    let again = her.answer(&card, json!({"decision": "approve", "ops": card["ops"]})).await.unwrap();
    assert_eq!(again["state"], "sign_in");

    // The device signs in; the card is done once the connector lists its tools, and Her passes it on.
    sign_in(&her.client, "tracker").await;
    let note = her.passed_on(&card, "done").await;
    assert!(note.starts_with("Tracker connected: ") && note.contains("echo"), "{note}");
    until_row(&mut her.wire, |row| row.get("waiting").is_none()).await;
    let checked = her.client.request("mcp/check", json!({"name": "tracker"})).await.unwrap();
    assert_eq!((checked["server"]["status"]["state"].as_str(), checked["server"]["auth_state"].as_str()), (Some("ok"), Some("logged_in")), "{checked:#}");

    // Answered once.
    let late = her.answer(&card, json!({"decision": "decline"})).await.unwrap_err();
    assert_eq!(late.data.as_ref().and_then(|d| d["kind"].as_str()), Some("card_ended"), "{late:?}");

    // A device opening the conversation later draws the card ended; so does the log.
    let opened = her.client.request("bots/open", json!({"bot_id": her.bot})).await.unwrap();
    let resolved = opened["snapshot"]["transcript"]["items"].as_array().unwrap().iter().find(|i| i["type"] == "card_resolved").cloned();
    assert_eq!(resolved, Some(json!({"type": "card_resolved", "card_id": id, "outcome": "done", "note": note})));
    her.client.request("session/close", json!({"session_id": her.session})).await.unwrap();
    let opened = her.client.request("bots/open", json!({"bot_id": her.bot})).await.unwrap();
    her.session = opened["session_id"].as_str().unwrap().to_string();
    assert!(opened["snapshot"]["transcript"]["items"].as_array().unwrap().iter().any(|i| i["type"] == "card_resolved" && i["card_id"] == id.as_str()));

    // Loaded again, Her's conversation uses the connector.
    her.say("[[mock:mcp_env:tracker:HER_CARDS_TEST]] read it").await;
    let told = sent(&her.approving().await).join("\n");
    assert!(told.starts_with("mcp done: ") && !told.contains("MCP server"), "{told}");

    // Its sign-in gone, and the token the conversation holds refused, a call says how to have
    // the owner sign in again.
    let credentials = std::fs::read_to_string(env.home.path().join("credentials.toml")).unwrap();
    let access = credentials.split("at-").nth(1).map(|rest| format!("at-{}", rest.chars().take_while(char::is_ascii_digit).collect::<String>())).expect("an access token");
    her.client.request("mcp/logout", json!({"name": "tracker"})).await.unwrap();
    revoke(&mock.url(), &access).await;
    her.say("[[mock:mcp_env:tracker:HER_CARDS_TEST]] read it again").await;
    let told = sent(&her.approving().await).join("\n");
    assert!(told.contains("connector_connect {name: \"tracker\"}"), "{told}");
    her.stop().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_token_goes_through_the_card_and_nowhere_else() {
    const TOKEN: &str = "tok-7f3k9q2m8z";
    const WRONG: &str = "tok-wrong-4j8s";
    let env = TestEnv::new("manual", "").await;
    let mock = her_mock::mcp::serve_http("127.0.0.1:0", McpTestOptions { auth: McpTestAuth::Token(TOKEN.into()), ..Default::default() }).await.unwrap();
    let mut her = her(&env).await;

    // A server that wants a token and offers no OAuth: the card asks for it.
    let (card, _) = her.ask_for("docs", &mock.url()).await;
    assert_eq!(card["body"]["sign_in"], "none");
    assert_eq!(card["body"]["fields"], json!([{"id": "f1", "label": "Authorization", "secret": true}]));
    assert_eq!(card["ops"][0], json!({"method": "credentials/set", "params": {"name": "mcp.docs", "value": "<value:f1>"}}));

    // A value left empty is no answer.
    let empty = her.answer(&card, json!({"decision": "approve", "ops": Her::filled(&card, "")})).await.unwrap_err();
    assert_eq!(empty.code, -32602);
    // A token the server refuses: the card says why and waits for another, and Her hears nothing.
    let refused = her.answer(&card, json!({"decision": "approve", "ops": Her::filled(&card, WRONG)})).await.unwrap();
    assert_eq!(refused["state"], "retry", "{refused}");
    assert!(refused["note"].as_str().is_some_and(|n| n.contains("401")), "{refused}");
    until(&mut her.wire, changed_to(card["id"].as_str().unwrap(), "retry")).await;
    // The right one: done, with the connector's tools.
    let done = her.answer(&card, json!({"decision": "approve", "ops": Her::filled(&card, TOKEN)})).await.unwrap();
    assert_eq!(done["state"], "done", "{done}");
    let note = her.passed_on(&card, "done").await;
    assert!(note.starts_with("Docs connected: "), "{note}");
    let config = user_config(&env);
    assert!(config.contains("Bearer ${credential:mcp.docs}"), "{config}");

    // Neither token is in a session file, the configuration or a log: only in the credential store.
    let stderr = her.stop().await;
    for secret in [TOKEN, WRONG] {
        assert!(!stderr.contains(secret), "stderr has {secret}");
        for (path, text) in files(env.home.path()) {
            if path.file_name().is_some_and(|n| n == "credentials.toml") {
                continue;
            }
            assert!(!text.contains(secret), "{} has {secret}", path.display());
        }
    }
    let credentials = std::fs::read_to_string(env.home.path().join("credentials.toml")).unwrap();
    assert!(credentials.contains(TOKEN) && !credentials.contains(WRONG));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_declined_card_takes_back_what_it_wrote() {
    let env = TestEnv::new("manual", "").await;
    let mock = oauth_server().await;
    let mut her = her(&env).await;

    // Declined while it waits for the sign-in: the connector it added goes again.
    let (card, _) = her.ask_for("tracker", &mock.url()).await;
    her.answer(&card, json!({"decision": "approve", "ops": card["ops"]})).await.unwrap();
    until(&mut her.wire, changed_to(card["id"].as_str().unwrap(), "sign_in")).await;
    assert!(user_config(&env).contains("[mcp_servers.tracker]"));
    let declined = her.answer(&card, json!({"decision": "decline"})).await.unwrap();
    assert_eq!(declined["state"], "declined");
    let note = her.passed_on(&card, "declined").await;
    assert_eq!(note, "The owner declined.");
    assert!(!user_config(&env).contains("tracker"), "{}", user_config(&env));

    // Declined at once: nothing was written, nothing is taken back.
    let (card, _) = her.ask_for("other", &mock.url()).await;
    her.answer(&card, json!({"decision": "decline"})).await.unwrap();
    her.passed_on(&card, "declined").await;
    assert!(!user_config(&env).contains("other"));
    her.stop().await;
}
