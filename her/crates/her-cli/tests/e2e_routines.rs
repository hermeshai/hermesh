//! A bot's routines over the native protocol (docs/kernel/routine-design.md): the real
//! `her serve`, the mock model, a private HER_HOME.

mod common;

use common::SchemaCheck;
use common::TestEnv;
use common::WAIT;
use common::native_init;
use her_rpc::Client;
use her_rpc::IncomingRx;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::sync::atomic::Ordering;
use std::time::Duration;
use tokio::net::UnixStream;
use tokio::process::Child;
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
                    let _ = bots_tx.send(message.params().clone());
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

async fn until_event(wire: &mut Wire, stop: impl Fn(&Value) -> bool) -> Vec<Value> {
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

/// The first `bots/changed` row of `bot` that matches `want`.
async fn until_row(wire: &mut Wire, bot: &str, want: impl Fn(&Value) -> bool) -> Value {
    loop {
        let changed = tokio::time::timeout(WAIT, wire.bots.recv()).await.expect("timed out waiting for bots/changed").expect("closed");
        if let Some(row) = changed["bots"].as_array().into_iter().flatten().find(|b| b["id"] == bot && want(b)) {
            return row.clone();
        }
    }
}

fn sent(events: &[Value]) -> Vec<String> {
    events
        .iter()
        .filter(|e| e["type"] == "tool_started" && e["name"] == "send_to_user")
        .filter_map(|e| e["args"]["text"].as_str().map(str::to_string))
        .collect()
}

fn ended(e: &Value) -> bool {
    matches!(e["type"].as_str(), Some("task_completed" | "task_failed" | "task_interrupted"))
}

/// The default bot.
async fn the_bot(client: &Client) -> String {
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    listed["bots"][0]["id"].as_str().unwrap().to_string()
}

async fn open(client: &Client, bot: &str) -> String {
    let opened = client
        .request(
            "bots/open",
            json!({"bot_id": bot, "events": {"exclude": ["text_delta", "thinking_delta", "tool_call_delta", "tool_progress"]}}),
        )
        .await
        .expect("bots/open");
    opened["session_id"].as_str().unwrap().to_string()
}

async fn say(client: &Client, session: &str, text: &str) {
    let op = json!({"session_id": session, "op": {"type": "prompt", "input": [{"type": "text", "text": text}]}});
    client.request("session/op", op).await.expect("prompt accepted");
}

/// Has the bot make a routine, and returns its id.
async fn make(wire: &mut Wire, client: &Client, session: &str, bot: &str, name: &str, schedule: &str, run: &str) -> String {
    say(client, session, &format!("[[mock:routine:{name}|{schedule}|{run}]]")).await;
    let events = until_event(wire, ended).await;
    assert_eq!(events.last().unwrap()["type"], "task_completed", "{events:?}");
    let row = until_row(wire, bot, |b| b["routines"].as_array().is_some_and(|all| all.iter().any(|r| r["name"] == name))).await;
    let all = row["routines"].as_array().unwrap();
    all.iter().find(|r| r["name"] == name).unwrap()["id"].as_str().unwrap().to_string()
}

fn routine<'a>(row: &'a Value, id: &str) -> &'a Value {
    row["routines"].as_array().and_then(|all| all.iter().find(|r| r["id"] == id)).unwrap_or(&Value::Null)
}

fn kind(err: &her_rpc::RpcError) -> String {
    err.data.as_ref().and_then(|d| d.get("kind")).and_then(Value::as_str).unwrap_or_default().to_string()
}

/// Every session log under `home`, as text.
fn logs(home: &Path) -> Vec<String> {
    fn walk(dir: &Path, out: &mut Vec<String>) {
        for entry in std::fs::read_dir(dir).into_iter().flatten().flatten() {
            let path = entry.path();
            if path.is_dir() {
                walk(&path, out);
            } else if path.extension().is_some_and(|e| e == "jsonl") {
                out.push(std::fs::read_to_string(&path).unwrap_or_default());
            }
        }
    }
    let mut out = Vec::new();
    walk(&home.join("sessions"), &mut out);
    out
}

/// Said in the chat, kept in the file, run at its time, and told by the bot in its own message.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_routine_said_in_the_chat_runs_at_its_time_and_the_bot_tells_the_user() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    let init = client.request("initialize", json!({"protocol_version": 2, "client_info": {"name": "e2e"}})).await.unwrap();
    assert_eq!(init["capabilities"]["routines"], true);
    let mut wire = route(incoming);
    let schema = SchemaCheck::new(&["BotRoutineResult", "BotRoutineRunsResult", "BotRoutineRunResult", "BotsChangedParams"]);
    let bot = the_bot(&client).await;
    let session = open(&client, &bot).await;
    assert!(!env.home.path().join("her.db").exists(), "made only for the first routine");

    say(&client, &session, "[[mock:routine:报价|in 2s|say:跌到 290 了。]]").await;
    let events = until_event(&mut wire, ended).await;
    // The model has the times to tell the user; clients have the change for a line in the chat.
    let told = sent(&events);
    assert!(told[0].starts_with("转告：Saved routine \"报价\" (id r_"), "{told:?}");
    assert!(told[0].contains("Next runs in local time:"), "{told:?}");
    let change = events.iter().find(|e| e["type"] == "tool_completed" && e["result"]["tool_name"] == "manage_routine").expect("the tool's result");
    assert_eq!(change["result"]["details"]["action"], "create");
    assert_eq!(change["result"]["details"]["name"], "报价");
    assert!(env.home.path().join("her.db").exists());
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = std::fs::metadata(env.home.path().join("her.db")).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode, 0o600);
    }
    let id = change["result"]["details"]["routine_id"].as_str().unwrap().to_string();

    // At its time, with nobody asking: a task that the letter started, and no agent in sight.
    let events = until_event(&mut wire, ended).await;
    let told = sent(&events);
    assert_eq!(told, ["跌到 290 了。"], "only what the run found");
    assert!(events.iter().any(|e| e["type"] == "task_started" && e["trigger"] == "mail"), "{events:?}");
    assert!(events.iter().all(|e| !e["type"].as_str().unwrap_or("").starts_with("agent_")), "{events:?}");
    let mail = &events.iter().find(|e| e["type"] == "message_completed" && e["mail"].is_object()).expect("the letter")["mail"];
    assert_eq!(mail["kind"], "routine");
    assert_eq!(mail["routine_id"], id.as_str());
    assert_eq!(mail["from"]["name"], "报价");
    let run = mail["from"]["id"].as_str().unwrap().to_string();

    // The panel's view: a one-off is over, and the message is what followed its report.
    let row = until_row(&mut wire, &bot, |b| {
        let r = routine(b, &id);
        r["state"] == "finished" && r["last_run"]["delivered"].is_string()
    })
    .await;
    schema.check("BotsChangedParams", &json!({"bots": [row.clone()]}));
    assert_eq!(routine(&row, &id)["last_run"]["outcome"], "reported");
    assert!(routine(&row, &id).get("next_run").is_none());
    let detail = client.request("bots/routine", json!({"bot_id": bot, "routine_id": id})).await.unwrap();
    schema.check("BotRoutineResult", &detail);
    assert_eq!(detail["routine"]["prompt"], "[[mock:say:跌到 290 了。]]");
    assert!(detail["routine"]["schedule"].as_str().unwrap().contains('T'), "a one-off is kept as its moment: {detail}");
    let runs = client.request("bots/routine_runs", json!({"bot_id": bot, "routine_id": id})).await.unwrap();
    schema.check("BotRoutineRunsResult", &runs);
    assert_eq!(runs["runs"].as_array().unwrap().len(), 1);
    let first = &runs["runs"][0];
    assert_eq!(first["id"], run.as_str());
    assert_eq!(first["outcome"], "reported");
    assert_eq!(first["report"], "跌到 290 了。");
    assert_eq!(first["has_log"], true);
    assert!(first["usage"].is_object(), "{first}");
    assert!(first["delivered"].is_string(), "{first}");

    // Its steps come from its own log, without the brief it started from.
    let one = client.request("bots/routine_run", json!({"bot_id": bot, "routine_id": id, "run_id": run})).await.unwrap();
    schema.check("BotRoutineRunResult", &one);
    let steps = serde_json::to_string(&one["transcript"]).unwrap();
    assert!(steps.contains("跌到 290 了。"), "{steps}");
    assert!(!steps.contains("You are carrying out"), "{steps}");

    // Ids that are not there.
    let err = client.request("bots/routine", json!({"bot_id": bot, "routine_id": "r_nope"})).await.unwrap_err();
    assert_eq!(kind(&err), "routine_not_found");
    let err = client.request("bots/routine_run", json!({"bot_id": bot, "routine_id": id, "run_id": "run_nope"})).await.unwrap_err();
    assert_eq!(kind(&err), "routine_not_found");
    let err = client.request("bots/routine", json!({"bot_id": "nobody", "routine_id": id})).await.unwrap_err();
    assert_eq!(kind(&err), "bot_not_found");
}

/// Nothing to tell: the chat stays as it was, and the record says what it checked.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_run_with_nothing_to_tell_stays_out_of_the_chat() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let bot = the_bot(&client).await;
    let session = open(&client, &bot).await;
    let id = make(&mut wire, &client, &session, &bot, "价格", "0 8 * * *", "say:NOTHING_TO_REPORT 还是 412。").await;
    let before = env.mock.stats().requests.load(Ordering::Relaxed);

    client.request("bots/routine_run_now", json!({"bot_id": bot, "routine_id": id})).await.unwrap();
    let row = until_row(&mut wire, &bot, |b| routine(b, &id)["last_run"]["outcome"] == "quiet").await;
    assert!(routine(&row, &id).get("running").is_none(), "{row}");
    assert_eq!(routine(&row, &id)["state"], "active", "a test run does not change what is scheduled");
    assert_eq!(env.mock.stats().requests.load(Ordering::Relaxed), before + 1, "only the run asked the model");
    let runs = client.request("bots/routine_runs", json!({"bot_id": bot, "routine_id": id})).await.unwrap();
    assert_eq!(runs["runs"][0]["outcome"], "quiet");
    assert_eq!(runs["runs"][0]["note"], "还是 412。");
    assert!(runs["runs"][0].get("delivered").is_none());
    // The bot's own events show nothing: its next one is what comes next.
    say(&client, &session, "[[mock:send:在。]]").await;
    let events = until_event(&mut wire, ended).await;
    assert_eq!(events.iter().filter(|e| e["type"] == "task_started").count(), 1, "{events:?}");
    assert!(events.iter().all(|e| !e["mail"].is_object()), "{events:?}");
}

/// A run in progress shows in the row, cannot be started twice and can be stopped.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_run_under_way_shows_and_can_be_stopped() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let bot = the_bot(&client).await;
    let session = open(&client, &bot).await;
    let id = make(&mut wire, &client, &session, &bot, "慢", "0 8 * * *", "slow:600").await;

    client.request("bots/routine_run_now", json!({"bot_id": bot, "routine_id": id})).await.unwrap();
    let row = until_row(&mut wire, &bot, |b| routine(b, &id)["running"] == true).await;
    assert_eq!(routine(&row, &id).get("typing"), None);
    assert!(row.get("typing").is_none() && row.get("agents_working").is_none(), "the bot itself is not at work: {row}");
    let err = client.request("bots/routine_run_now", json!({"bot_id": bot, "routine_id": id})).await.unwrap_err();
    assert_eq!(kind(&err), "routine_busy");
    client.request("bots/routine_stop", json!({"bot_id": bot, "routine_id": id})).await.unwrap();
    let row = until_row(&mut wire, &bot, |b| routine(b, &id)["last_run"]["outcome"] == "interrupted").await;
    assert!(routine(&row, &id).get("running").is_none());
    let runs = client.request("bots/routine_runs", json!({"bot_id": bot, "routine_id": id})).await.unwrap();
    assert_eq!(runs["runs"][0]["outcome"], "interrupted");
}

/// Nobody has the conversation open: it is loaded for the run, and the row follows.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_routine_runs_in_a_conversation_that_is_not_loaded() {
    let env = TestEnv::new("bypass-permissions", "[server]\nidle_session_secs = 1\n").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let bot = the_bot(&client).await;
    let session = open(&client, &bot).await;
    let id = make(&mut wire, &client, &session, &bot, "简报", "0 8 * * *", "say:今天有三件事。").await;
    client.request("session/close", json!({"session_id": session})).await.unwrap();

    client.request("bots/routine_run_now", json!({"bot_id": bot, "routine_id": id})).await.unwrap();
    let row = until_row(&mut wire, &bot, |b| {
        b["last"]["from"] == "bot" && b["last"]["text"].as_str().is_some_and(|t| t.contains("今天有三件事。"))
    })
    .await;
    assert!(row["unread"].as_u64().unwrap() >= 1);
    until_row(&mut wire, &bot, |b| routine(b, &id)["last_run"]["delivered"].is_string()).await;

    // Reading it later shows what happened, in the one conversation.
    let opened = client
        .request("bots/open", json!({"bot_id": bot, "events": {"exclude": ["text_delta", "thinking_delta", "tool_call_delta", "tool_progress"]}}))
        .await
        .unwrap();
    assert_eq!(opened["session_id"], session.as_str());
}

/// Nothing keeps a conversation loaded for a routine but a run under way: it stays for that, and
/// goes when it has gone unused for its idle time (routine-design §5.5, §7.2).
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_conversation_stays_loaded_for_a_run_and_no_longer() {
    let env = TestEnv::new("bypass-permissions", "[server]\nidle_session_secs = 1\n").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let bot = the_bot(&client).await;
    let session = open(&client, &bot).await;
    let id = make(&mut wire, &client, &session, &bot, "慢", "0 8 * * *", "slow:300").await;
    client.request("session/detach", json!({"session_id": session})).await.unwrap();

    client.request("bots/routine_run_now", json!({"bot_id": bot, "routine_id": id})).await.unwrap();
    until_row(&mut wire, &bot, |b| routine(b, &id)["running"] == true).await;
    tokio::time::sleep(Duration::from_secs(3)).await;
    let runs = client.request("bots/routine_runs", json!({"bot_id": bot, "routine_id": id})).await.unwrap();
    assert!(runs["runs"][0].get("outcome").is_none(), "still running: {runs}");

    client.request("bots/routine_stop", json!({"bot_id": bot, "routine_id": id})).await.unwrap();
    until_row(&mut wire, &bot, |b| routine(b, &id)["last_run"]["outcome"] == "interrupted").await;
    tokio::time::sleep(Duration::from_secs(3)).await;
    let err = client.request("session/attach", json!({"session_id": session})).await.unwrap_err();
    assert_eq!(kind(&err), "session_not_loaded");
}

/// The service goes while a run is under way, in good order or not: it is written off as
/// interrupted and not run again, and the one-off among them is over (routine-design §5.2).
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_run_cut_off_by_the_service_ending_is_not_run_again() {
    for crash in [false, true] {
        let env = TestEnv::new("bypass-permissions", "").await;
        let (bot, id, mut child) = {
            let (child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
            native_init(&client).await;
            let mut wire = route(incoming);
            let bot = the_bot(&client).await;
            let session = open(&client, &bot).await;
            let id = make(&mut wire, &client, &session, &bot, "一会儿", "in 1s", "slow:600").await;
            until_row(&mut wire, &bot, |b| routine(b, &id)["running"] == true).await;
            if !crash {
                client.request("server/shutdown", Value::Null).await.unwrap();
            }
            (bot, id, child)
        };
        if crash {
            child.kill().await.unwrap();
        }
        tokio::time::timeout(WAIT, child.wait()).await.expect("the service stops").unwrap();

        let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
        native_init(&client).await;
        let mut wire = route(incoming);
        let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
        let row = listed["bots"].as_array().unwrap().iter().find(|b| b["id"] == bot.as_str()).unwrap().clone();
        let row = until_row_or(&mut wire, &bot, &row, |b| routine(b, &id)["state"] == "finished").await;
        assert_eq!(routine(&row, &id)["last_run"]["outcome"], "interrupted", "crash {crash}: {row}");
        let runs = client.request("bots/routine_runs", json!({"bot_id": bot, "routine_id": id})).await.unwrap();
        assert_eq!(runs["runs"].as_array().unwrap().len(), 1, "crash {crash}: {runs}");
        assert_eq!(runs["runs"][0]["outcome"], "interrupted");
    }
}

/// The file is what lasts: the service starts again and has them all. What it was asked to do at
/// a time that passed while it was down, it does once.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn routines_outlive_the_service() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (id, one_off, bot, mut child) = {
        let (child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
        native_init(&client).await;
        let mut wire = route(incoming);
        let bot = the_bot(&client).await;
        let session = open(&client, &bot).await;
        let id = make(&mut wire, &client, &session, &bot, "每天", "0 8 * * *", "say:NOTHING_TO_REPORT").await;
        let one_off = make(&mut wire, &client, &session, &bot, "一会儿", "in 3s", "say:到点了。").await;
        client.request("server/shutdown", Value::Null).await.unwrap();
        (id, one_off, bot, child)
    };
    tokio::time::timeout(WAIT, child.wait()).await.expect("the service stops").unwrap();
    tokio::time::sleep(Duration::from_secs(3)).await;

    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let row = listed["bots"].as_array().unwrap().iter().find(|b| b["id"] == bot.as_str()).unwrap().clone();
    // The first look may not have run yet; it comes without being asked.
    let daily = until_row_or(&mut wire, &bot, &row, |b| routine(b, &id)["state"] == "active").await;
    assert!(routine(&daily, &id)["next_run"].is_string(), "{daily}");
    let done = until_row_or(&mut wire, &bot, &row, |b| routine(b, &one_off)["state"] == "finished").await;
    assert!(routine(&done, &one_off)["last_run"].get("outcome").is_some(), "{done}");
    let runs = client.request("bots/routine_runs", json!({"bot_id": bot, "routine_id": one_off})).await.unwrap();
    assert_eq!(runs["runs"].as_array().unwrap().len(), 1, "once: {runs}");
    assert!(runs["runs"][0]["late"].is_null() || runs["runs"][0]["late"] == false, "seconds are not late: {runs}");
}

/// `row` itself when it matches, else the next one that does.
async fn until_row_or(wire: &mut Wire, bot: &str, row: &Value, want: impl Fn(&Value) -> bool) -> Value {
    if want(row) {
        return row.clone();
    }
    until_row(wire, bot, want).await
}

/// A file that cannot be read costs the routines and nothing else, and is left as it is.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_damaged_database_costs_only_the_routines() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let path = env.home.path().join("her.db");
    let junk = b"this is not a database, though it is long enough to look at it".repeat(100);
    std::fs::write(&path, &junk).unwrap();
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let bot = the_bot(&client).await;
    let session = open(&client, &bot).await;

    let mut row = client.request("bots/subscribe", Value::Null).await.unwrap()["bots"][0].clone();
    if row.get("routines_error").is_none() {
        row = until_row(&mut wire, &bot, |b| b["routines_error"].is_string()).await;
    }
    assert!(row["routines_error"].as_str().unwrap().contains("not a database"), "{row}");

    say(&client, &session, "[[mock:send:在。]] 在吗").await;
    assert_eq!(sent(&until_event(&mut wire, ended).await), ["在。"]);
    say(&client, &session, "[[mock:routine:喝水|0 8 * * *|say:喝水。]]").await;
    let told = sent(&until_event(&mut wire, ended).await);
    assert!(told[0].contains("Routines cannot be used right now"), "{told:?}");
    let err = client.request("bots/routine_runs", json!({"bot_id": bot, "routine_id": "r_any"})).await.unwrap_err();
    assert_eq!(kind(&err), "routines_unavailable");
    assert_eq!(std::fs::read(&path).unwrap(), junk);
}

/// From the panel: off and on, gone; the model hears each as a new list.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_panel_changes_a_routine_and_the_model_hears_of_it() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let bot = the_bot(&client).await;
    let session = open(&client, &bot).await;
    let id = make(&mut wire, &client, &session, &bot, "喝水", "0 8 * * *", "say:喝水。").await;

    client.request("bots/routine_set_enabled", json!({"bot_id": bot, "routine_id": id, "enabled": false})).await.unwrap();
    let row = until_row(&mut wire, &bot, |b| routine(b, &id)["state"] == "paused").await;
    assert!(routine(&row, &id).get("next_run").is_none());
    say(&client, &session, "[[mock:send:一。]]").await;
    until_event(&mut wire, ended).await;
    let told = |text: &str| logs(env.home.path()).iter().any(|log| log.contains(text));
    assert!(told(&format!("- {id} 喝水: 说好的时间 (paused)")), "the model was told it is paused");

    client.request("bots/routine_set_enabled", json!({"bot_id": bot, "routine_id": id, "enabled": true})).await.unwrap();
    let row = until_row(&mut wire, &bot, |b| routine(b, &id)["state"] == "active").await;
    assert!(routine(&row, &id)["next_run"].is_string());

    client.request("bots/routine_delete", json!({"bot_id": bot, "routine_id": id})).await.unwrap();
    until_row(&mut wire, &bot, |b| b.get("routines").is_none()).await;
    say(&client, &session, "[[mock:send:二。]]").await;
    until_event(&mut wire, ended).await;
    assert!(told("your routines: none"));
    let err = client.request("bots/routine", json!({"bot_id": bot, "routine_id": id})).await.unwrap_err();
    assert_eq!(kind(&err), "routine_not_found");
}

/// A new conversation keeps its bot's routines; a deleted bot takes them along.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn routines_belong_to_the_bot_not_the_conversation() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let _ = the_bot(&client).await;
    let created = client.request("bots/create", json!({"name": "研究员", "avatar": {"kind": "mono", "hue": "teal"}})).await.unwrap();
    let bot = created["bot"]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;
    let id = make(&mut wire, &client, &session, &bot, "喝水", "0 8 * * *", "say:喝水。").await;

    client.request("bots/clear", json!({"bot_id": bot})).await.unwrap();
    let session = open(&client, &bot).await;
    say(&client, &session, "[[mock:send:在。]]").await;
    until_event(&mut wire, ended).await;
    assert!(logs(env.home.path()).iter().any(|log| log.contains(&session) && log.contains(&format!("- {id} 喝水: 说好的时间"))), "told in the new conversation");
    client.request("bots/routine", json!({"bot_id": bot, "routine_id": id})).await.unwrap();

    client.request("bots/delete", json!({"bot_id": bot})).await.unwrap();
    let err = client.request("bots/routine", json!({"bot_id": bot, "routine_id": id})).await.unwrap_err();
    assert_eq!(kind(&err), "bot_not_found");
}

async fn connect(path: &Path) -> (Client, IncomingRx) {
    let stream = UnixStream::connect(path).await.expect("connect");
    let (read, write) = stream.into_split();
    let (client, incoming) = Client::connect(read, write);
    native_init(&client).await;
    (client, incoming)
}

async fn wait_for_socket(path: &Path) {
    for _ in 0..200 {
        if UnixStream::connect(path).await.is_ok() {
            return;
        }
        tokio::time::sleep(Duration::from_millis(25)).await;
    }
    panic!("the service did not start");
}

/// Whether it is still running after `wait`.
async fn still_up(child: &mut Child, wait: Duration) -> bool {
    tokio::time::timeout(wait, child.wait()).await.is_err()
}

/// With nobody connected, the service leaves after its idle time, unless a routine needs it
/// (routine-design §7.1).
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_service_stays_up_for_an_enabled_routine() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let socket = env.home.path().join("run").join("t.sock");
    let url = format!("unix://{}", socket.display());
    let mut service = env.her(&["serve", "--listen", &url, "--idle-exit", "1"]).spawn().unwrap();
    wait_for_socket(&socket).await;
    let (client, incoming) = connect(&socket).await;
    let mut wire = route(incoming);
    let bot = the_bot(&client).await;
    let session = open(&client, &bot).await;
    let id = make(&mut wire, &client, &session, &bot, "喝水", "0 8 * * *", "say:喝水。").await;
    drop(client);
    assert!(still_up(&mut service, Duration::from_secs(3)).await, "an enabled routine keeps it up");

    let (client, _incoming) = connect(&socket).await;
    client.request("bots/routine_set_enabled", json!({"bot_id": bot, "routine_id": id, "enabled": false})).await.unwrap();
    drop(client);
    let code = tokio::time::timeout(Duration::from_secs(10), service.wait()).await.expect("it leaves when nothing needs it").unwrap();
    assert_eq!(code.code(), Some(0));
}
