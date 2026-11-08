//! Bots over the native protocol (docs/kernel/bot-design.md §4–§5): the real `her serve`, the
//! mock model's bot scenarios, a private HER_HOME.

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
use std::path::PathBuf;
use tokio::sync::mpsc;

/// What the server sends, split: `bots/changed` params, and session events.
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

/// Events until one matches `stop`, inclusive.
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

/// The texts the bot sent the user among `events`, in order.
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

/// Every session log under `home`, as text.
fn logs(home: &Path) -> Vec<(PathBuf, String)> {
    fn walk(dir: &Path, out: &mut Vec<(PathBuf, String)>) {
        for entry in std::fs::read_dir(dir).into_iter().flatten().flatten() {
            let path = entry.path();
            if path.is_dir() {
                walk(&path, out);
            } else if path.extension().is_some_and(|e| e == "jsonl") {
                out.push((path.clone(), std::fs::read_to_string(&path).unwrap_or_default()));
            }
        }
    }
    let mut out = Vec::new();
    walk(&home.join("sessions"), &mut out);
    out
}

/// A 1 × 1 PNG.
const PNG: &str = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==";

#[tokio::test]
async fn a_bot_talks_in_messages_and_its_row_follows() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let schema = SchemaCheck::new(&["BotsResult", "BotResult", "BotsChangedParams", "SessionNewResult"]);

    // The default bot is there from the start.
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    schema.check("BotsResult", &listed);
    let bots = listed["bots"].as_array().unwrap();
    assert_eq!(bots.len(), 1);
    assert_eq!((bots[0]["name"].as_str(), bots[0]["default"].as_bool()), (Some("Her"), Some(true)));
    assert_eq!(bots[0]["avatar"], json!({"kind": "mark"}));

    let created = client
        .request("bots/create", json!({"name": "  研究员 ", "avatar": {"kind": "char", "shape": "wink", "hue": "teal"}}))
        .await
        .unwrap();
    schema.check("BotResult", &created);
    let bot = created["bot"]["id"].as_str().unwrap().to_string();
    assert_eq!(created["bot"]["name"], "研究员");
    until_row(&mut wire, &bot, |b| b["name"] == "研究员").await;

    let opened = client
        .request("bots/open", json!({"bot_id": bot, "events": {"exclude": ["text_delta", "thinking_delta", "tool_call_delta", "tool_progress"]}}))
        .await
        .unwrap();
    schema.check("SessionNewResult", &opened);
    let session = opened["session_id"].as_str().unwrap().to_string();
    let row = until_row(&mut wire, &bot, |b| b["session_id"].is_string()).await;
    assert_eq!(row["session_id"], session.as_str());

    say(&client, &session, "[[mock:send:你好，我是研究员。]] 你是谁").await;
    let events = until_event(&mut wire, ended).await;
    assert_eq!(sent(&events), ["你好，我是研究员。"]);
    assert!(events.iter().all(|e| e["type"] != "message_delta"), "no streaming for a bot");
    let row = until_row(&mut wire, &bot, |b| b["last"]["from"] == "bot").await;
    schema.check("BotsChangedParams", &json!({"bots": [row.clone()]}));
    assert_eq!(row["last"]["text"], "你好，我是研究员。");
    assert_eq!(row["unread"], 1);
    client.request("bots/read", json!({"bot_id": bot})).await.unwrap();
    until_row(&mut wire, &bot, |b| b.get("unread").is_none()).await;

    // Not a session of the session list.
    let list = client.request("session/list", json!({})).await.unwrap();
    assert!(list["sessions"].as_array().unwrap().iter().all(|s| s["session_id"] != session.as_str()));

    // Its prompt is a bot's, with its name, and it works in its folder.
    let (_, log) = logs(env.home.path()).into_iter().find(|(_, text)| text.contains(&session)).expect("the log");
    assert!(log.contains("You are 研究员, one of the user's bots in Hermesh."));
    let folder = env.home.path().join("bots").join(&bot).join("files");
    assert!(log.contains(&format!("\"cwd\":\"{}", std::fs::canonicalize(&folder).unwrap().display())));
}

#[tokio::test]
async fn messages_in_a_row_are_answered_in_order() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;
    for poem in ["春", "夏", "秋", "冬"] {
        say(&client, &session, &format!("[[mock:send:{poem}天的诗]] 写一首{poem}天的诗")).await;
    }
    let mut seen = Vec::new();
    while sent(&seen).len() < 4 {
        seen.extend(until_event(&mut wire, ended).await);
    }
    assert_eq!(sent(&seen), ["春天的诗", "夏天的诗", "秋天的诗", "冬天的诗"]);
}

/// Reminded and still silent, the task fails: the owner is told, not left waiting (bot-design
/// §3.11, D-321). What it only wrote stays private.
#[tokio::test]
async fn a_bot_that_never_sends_fails_so_the_owner_knows() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;
    say(&client, &session, "[[mock:mute]] 还在吗").await;
    let events = until_event(&mut wire, ended).await;
    let end = events.last().unwrap();
    assert_eq!(end["type"], "task_failed", "{end}");
    assert_eq!(end["error"]["kind"], "no_reply", "{end}");
    assert_eq!(end["error"]["retryable"], true, "{end}");
    assert!(sent(&events).is_empty());
    assert_eq!(events.iter().filter(|e| e["type"] == "turn_started").count(), 2);
    // Its row says so: no message is coming, and the owner's system notification comes from this.
    until_row(&mut wire, &bot, |b| b["failed"] == true).await;
    // Retrying asks again rather than saying there is nothing to continue.
    client.request("session/op", json!({"session_id": session, "op": {"type": "continue"}})).await.unwrap();
    let again = until_event(&mut wire, ended).await;
    assert_eq!(again.last().unwrap()["type"], "task_failed", "{again:?}");
}

/// A bot that answers in plain text is reminded and sends it; the reminder is the model's alone
/// (bot-design §3.11).
#[tokio::test]
async fn a_bot_that_forgets_to_send_is_reminded_and_the_user_sees_one_message() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;
    say(&client, &session, "[[mock:quiet:2。]] 1 加 1 等于几？").await;
    let events = until_event(&mut wire, ended).await;
    assert_eq!(sent(&events), ["2。"]);
    assert_eq!(events.iter().filter(|e| e["type"] == "turn_started").count(), 2);
    assert!(events.iter().all(|e| !e.to_string().contains("reply_reminder")));
    let row = until_row(&mut wire, &bot, |b| b["last"]["from"] == "bot").await;
    assert_eq!((row["last"]["text"].as_str(), row["unread"].as_u64()), (Some("2。"), Some(1)));
    // The conversation loads without it; the log keeps it.
    let opened = client.request("bots/open", json!({"bot_id": bot})).await.unwrap();
    let transcript = opened["snapshot"]["transcript"].to_string();
    assert!(transcript.contains("2。") && !transcript.contains("reply_reminder"), "{transcript}");
    let (_, log) = logs(env.home.path()).into_iter().find(|(_, text)| text.contains(&session)).expect("the log");
    assert_eq!(log.matches("\"type\":\"unanswered\"").count(), 1);
}

/// The first answer of a burst only written as plain text still goes out, ahead of the next one:
/// a reply to the next message must not count as answering it too (bot-design §3.11).
#[tokio::test]
async fn a_private_answer_goes_out_before_the_next_message_is_answered() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;
    say(&client, &session, "[[mock:quiet:春天的诗]] 写一首春天的诗").await;
    // The next message lands while this reply is written. Earlier, it joins this request and the
    // model reads both at once, which `her-core/tests/bot.rs` covers.
    let mut seen = until_event(&mut wire, |e| e["type"] == "turn_started").await;
    say(&client, &session, "[[mock:send:夏天的诗]] 写一首夏天的诗").await;
    while sent(&seen).len() < 2 {
        seen.extend(until_event(&mut wire, ended).await);
    }
    assert_eq!(sent(&seen), ["春天的诗", "夏天的诗"]);
    let (_, log) = logs(env.home.path()).into_iter().find(|(_, text)| text.contains(&session)).expect("the log");
    assert_eq!(log.matches("\"type\":\"unanswered\"").count(), 1);
}

/// A message that lands as a silent bot's task fails is answered, wherever exactly it lands: it
/// never goes back to the composer (bot-design §3.11). The moment cannot be aimed at from here, so
/// this walks across it; `her-core/tests/bot.rs` holds the task there.
#[tokio::test]
async fn a_message_that_lands_as_a_silent_bot_fails_is_still_answered() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    client.request("bots/subscribe", Value::Null).await.unwrap();
    for ms in 0..20 {
        let created = client.request("bots/create", json!({"name": format!("测试{ms}"), "avatar": {"kind": "mono", "hue": "sage"}})).await.unwrap();
        let session = open(&client, created["bot"]["id"].as_str().unwrap()).await;
        say(&client, &session, "[[mock:mute]] 还在吗").await;
        tokio::time::sleep(std::time::Duration::from_millis(ms)).await;
        say(&client, &session, "[[mock:send:夏天的诗]] 写一首夏天的诗").await;
        let mut seen = Vec::new();
        while sent(&seen).is_empty() {
            seen.extend(until_event(&mut wire, ended).await);
            let end = seen.last().unwrap();
            assert!(end.get("returned_input").and_then(Value::as_array).is_none_or(Vec::is_empty), "{ms} ms: {end}");
        }
        assert_eq!(sent(&seen), ["夏天的诗"], "{ms} ms");
    }
}

#[tokio::test]
async fn a_sub_agent_works_in_the_background_and_the_bot_relays_its_report() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;
    say(&client, &session, "[[mock:relay:查资料]] 帮我查一下").await;
    let events = until_event(&mut wire, |e| e["type"] == "idle").await;
    let texts = sent(&events);
    assert_eq!(texts.len(), 2, "{texts:?}");
    assert_eq!(texts[0], "在查了。");
    assert!(texts[1].starts_with("查到了："), "{texts:?}");
    assert!(events.iter().any(|e| e["type"] == "agent_started" && e["description"] == "查资料"));
    // The row counted the sub-agent while it worked.
    let mut working = Vec::new();
    while let Ok(changed) = wire.bots.try_recv() {
        for row in changed["bots"].as_array().into_iter().flatten().filter(|b| b["id"] == bot.as_str()) {
            working.push(row["agents_working"].as_u64().unwrap_or(0));
        }
    }
    assert!(working.contains(&1), "{working:?}");
    assert_eq!(working.last(), Some(&0));
}

/// The whole timeline of a long job (bot-turn-design §3): the first message before anything else,
/// the work in the background, the owner's next messages answered while it runs, and its result
/// delivered when the report comes. No wait, no reminder.
#[tokio::test]
async fn a_long_job_goes_to_the_background_and_the_bot_keeps_talking() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;

    say(&client, &session, "[[mock:relay_slow:3]] 写一篇一万字的小作文").await;
    let started = std::time::Instant::now();
    let first = until_event(&mut wire, ended).await;
    assert_eq!(sent(&first), ["好，这个要一会儿，写好发你。"]);
    let tools: Vec<&str> = first.iter().filter(|e| e["type"] == "tool_started").filter_map(|e| e["name"].as_str()).collect();
    assert_eq!(tools, ["send_to_user", "spawn_agent"], "the message comes first");
    assert_eq!(first.last().unwrap()["type"], "task_completed", "the turn ends while the agent works");
    assert!(started.elapsed() < std::time::Duration::from_secs(3), "the turn did not wait for the agent");

    // The owner goes on talking: an answer each, while the work goes on.
    say(&client, &session, "刚才给你发啥了你知道么").await;
    let second = until_event(&mut wire, ended).await;
    assert_eq!(sent(&second), ["回复：刚才给你发啥了你知道么"]);
    assert!(started.elapsed() < std::time::Duration::from_secs(3), "it answered while the agent worked");
    assert!(!second.iter().any(|e| e["type"] == "agent_status" && e["status"] == "completed"));

    // Then the report wakes it, and the result reaches the owner.
    let rest = until_event(&mut wire, |e| e["type"] == "idle").await;
    let results = sent(&rest);
    assert_eq!(results.len(), 1, "{results:?}");
    assert!(results[0].starts_with("回复：<agent_report agent=\"writer\" status=\"completed\">"), "{results:?}");
    assert!(rest.iter().any(|e| e["type"] == "task_started" && e["trigger"] == "mail"));

    let (_, log) = logs(env.home.path()).into_iter().find(|(_, text)| text.contains(&session)).unwrap();
    assert_eq!(log.matches("\"type\":\"unanswered\"").count() + log.matches("\"type\":\"undelivered\"").count(), 0, "no reminder was needed");
}

/// A sub-agent speaks up on the way (agent-messaging-design §3.1): the bot is woken and tells the
/// owner, the work goes on, and its report still follows.
#[tokio::test]
async fn a_working_agent_speaks_up_and_the_bot_tells_the_owner() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;

    say(&client, &session, "[[mock:relay_note:2]] 把 sales.csv 按月汇总").await;
    let first = until_event(&mut wire, ended).await;
    assert_eq!(sent(&first), ["好，我先把数据汇总出来。"]);

    // The message wakes the bot, and the agent is still at work when the owner hears.
    let spoke = until_event(&mut wire, ended).await;
    let said = sent(&spoke);
    assert_eq!(said, ["回复：<agent_message from=\"sales\">\n3 月的数据整月缺失。\n</agent_message>"]);
    assert!(spoke.iter().any(|e| e["type"] == "task_started" && e["trigger"] == "mail"));
    let told = spoke.iter().find(|e| e["type"] == "message_completed" && e.get("mail").is_some()).unwrap();
    assert_eq!((told["mail"]["kind"].as_str(), told["mail"]["status"].as_str()), (Some("message"), Some("running")));
    assert!(!spoke.iter().any(|e| e["type"] == "agent_status" && e["status"] == "completed"), "{spoke:?}");
    until_row(&mut wire, &bot, |b| b["agents_working"] == 1).await;

    // Then its report, as ever.
    let rest = until_event(&mut wire, |e| e["type"] == "idle").await;
    let results = sent(&rest);
    assert_eq!(results.len(), 1, "{results:?}");
    assert!(results[0].starts_with("回复：<agent_report agent=\"sales\" status=\"completed\">"), "{results:?}");

    // The bot's own log, not its agent's, which names the bot's session too.
    let header = format!("\"id\":\"{session}\"");
    let (_, log) = logs(env.home.path()).into_iter().find(|(_, text)| text.lines().next().is_some_and(|l| l.contains(&header))).unwrap();
    assert_eq!(log.matches("\"type\":\"agent_message\"").count(), 1, "{log}");
    assert_eq!(log.matches("\"type\":\"unanswered\"").count() + log.matches("\"type\":\"undelivered\"").count(), 0, "no reminder was needed");
}

/// A sub-agent tells the owner itself (agent-messaging-design §17): it shows in the bot's row and
/// counts as unread, the bot is not woken, and it is still there when the conversation is read back.
#[tokio::test]
async fn a_working_agent_tells_the_owner_and_the_bot_sleeps_on() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;
    const TOLD: &str = "3 月的数据整月缺失，我先按其余 11 个月做。";

    say(&client, &session, "[[mock:relay_told:2]] 把 sales.csv 按月汇总").await;
    let first = until_event(&mut wire, ended).await;
    assert_eq!(sent(&first), ["好，我先把数据汇总出来。"]);

    let is_told = |e: &Value| e["type"] == "message_completed" && e["mail"]["kind"] == "to_user";
    let spoke = until_event(&mut wire, is_told).await;
    assert!(!spoke.iter().any(|e| e["type"] == "task_started"), "the bot sleeps on: {spoke:?}");
    let told = spoke.last().unwrap();
    assert_eq!(told["message"]["content"][0]["text"], format!("<sent_to_user from=\"sales\">\n{TOLD}\n</sent_to_user>"));
    assert_eq!((told["mail"]["from"]["name"].as_str(), told["mail"]["status"].as_str()), (Some("sales"), Some("running")));
    let row = until_row(&mut wire, &bot, |b| b["last"]["text"] == TOLD).await;
    assert_eq!((row["last"]["from"].as_str(), row["unread"].as_u64(), row["agents_working"].as_u64()), (Some("bot"), Some(2), Some(1)));

    // Then its report wakes the bot, which has read what the owner was told.
    let rest = until_event(&mut wire, |e| e["type"] == "idle").await;
    let results = sent(&rest);
    assert_eq!(results.len(), 1, "{results:?}");
    assert!(results[0].starts_with("回复：<agent_report agent=\"sales\" status=\"completed\">"), "{results:?}");
    let woken: Vec<&Value> = rest.iter().filter(|e| e["type"] == "task_started").collect();
    assert_eq!(woken.len(), 1, "{woken:?}");

    // The bot's own log, written once.
    let header = format!("\"id\":\"{session}\"");
    let (_, log) = logs(env.home.path()).into_iter().find(|(_, text)| text.lines().next().is_some_and(|l| l.contains(&header))).unwrap();
    assert_eq!(log.matches("\"type\":\"agent_to_user\"").count(), 1, "{log}");
    assert_eq!(log.matches("\"type\":\"unanswered\"").count() + log.matches("\"type\":\"undelivered\"").count(), 0, "no reminder was needed");
}

/// The owner asks how it is going and then drops it: the bot finds the deferred tools, says what
/// they show, and the stopped work stays quiet (bot-turn-design §5.3).
#[tokio::test]
async fn the_bot_checks_on_the_work_and_stops_it() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;

    say(&client, &session, "[[mock:relay_slow:60]] 写一篇一万字的小作文").await;
    until_event(&mut wire, ended).await;
    until_row(&mut wire, &bot, |b| b["agents_working"] == 1).await;

    say(&client, &session, "[[mock:check]] 写到哪了").await;
    let checked = until_event(&mut wire, ended).await;
    let said = sent(&checked);
    assert_eq!(said.len(), 1, "{said:?}");
    assert!(said[0].starts_with("writer (写长文): running for"), "{said:?}");
    assert!(said[0].contains("recent steps: sleep 60"), "{said:?}");

    say(&client, &session, "[[mock:stop_all]] 算了不用了").await;
    let stopped = until_event(&mut wire, ended).await;
    assert_eq!(sent(&stopped), ["Stopped: writer."]);
    let tools: Vec<&str> = stopped.iter().filter(|e| e["type"] == "tool_started").filter_map(|e| e["name"].as_str()).collect();
    assert_eq!(tools[..2], ["tool_search", "call_tool"], "stopped before saying so: {tools:?}");
    assert!(stopped.iter().any(|e| e["type"] == "agent_status" && e["status"] == "interrupted"));
    until_row(&mut wire, &bot, |b| b.get("agents_working").is_none_or(|n| n == 0)).await;

    // Its report does not wake the bot: nothing more comes.
    let quiet = tokio::time::timeout(std::time::Duration::from_secs(1), until_event(&mut wire, |e| e["type"] == "task_started")).await;
    assert!(quiet.is_err(), "a stopped agent's report wakes no one");

    // The next message brings it along, and the bot talks as ever.
    say(&client, &session, "[[mock:send:在。]] 还在吗").await;
    let next = until_event(&mut wire, ended).await;
    assert_eq!(sent(&next), ["在。"]);
    let report = |e: &Value| e["type"] == "message_completed" && e["mail"]["status"] == "interrupted";
    assert!(stopped.iter().chain(&next).any(report), "{stopped:?} {next:?}");
}

#[tokio::test]
async fn approvals_come_after_the_message_that_asks_and_count_as_unread() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;
    say(&client, &session, "[[mock:confirm:touch confirmed.txt]] 建个文件").await;
    let events = until_event(&mut wire, |e| e["type"] == "approval_requested").await;
    // The message that asks shows before the approval: the call's start, not its result.
    assert_eq!(sent(&events), ["要执行命令，确认一下。"]);
    let call = events.last().unwrap()["call_id"].clone();
    let row = until_row(&mut wire, &bot, |b| b["waiting"].is_string()).await;
    assert_eq!(row["waiting"], "touch confirmed.txt");
    assert_eq!(row["unread"], 2, "the message and the approval");
    let op = json!({"session_id": session, "op": {"type": "approval", "call_id": call, "decision": {"type": "approve"}}});
    client.request("session/op", op).await.unwrap();
    let events = until_event(&mut wire, ended).await;
    assert!(sent(&events)[0].starts_with("做完了"));
    assert!(env.home.path().join("bots").join(&bot).join("files").join("confirmed.txt").exists());
    until_row(&mut wire, &bot, |b| b.get("waiting").is_none() && b.get("typing").is_none()).await;
}

#[tokio::test]
async fn what_a_sub_agent_asks_waits_on_the_row_and_the_bot_keeps_talking() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;
    say(&client, &session, "[[mock:relay_run:touch made.txt]] 建个文件").await;
    let asked = |e: &Value| e["type"] == "agent_event" && e["event"]["type"] == "approval_requested";
    let events = until_event(&mut wire, asked).await;
    assert_eq!(sent(&events), ["交给子智能体了。"]);
    let ask = events.last().unwrap().clone();

    let row = until_row(&mut wire, &bot, |b| b["waiting"].is_string()).await;
    assert_eq!(row["waiting"], "touch made.txt");
    assert_eq!(row["agents_working"], 1);
    assert_eq!(row["unread"], 2, "the message and the approval");

    // The question does not hold the bot up.
    say(&client, &session, "还在吗").await;
    until_event(&mut wire, |e| e["type"] == "tool_started" && e["args"]["text"] == "做完了：还在吗").await;
    let files = env.home.path().join("bots").join(&bot).join("files");
    assert!(!files.join("made.txt").exists());

    let op = json!({"session_id": session, "op": {"type": "agent_approval", "agent": ask["agent_id"], "call_id": ask["event"]["call_id"], "decision": {"type": "approve"}}});
    client.request("session/op", op).await.unwrap();
    let events = until_event(&mut wire, |e| e["type"] == "idle").await;
    assert!(files.join("made.txt").exists());
    assert!(sent(&events).iter().any(|t| t.contains("<agent_report")), "{events:?}");
    until_row(&mut wire, &bot, |b| b.get("waiting").is_none() && b.get("agents_working").is_none()).await;
}

#[tokio::test]
async fn a_new_bot_starts_on_the_model_it_was_made_with() {
    let env = TestEnv::new("manual", "").await;
    let (child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    drop(incoming);
    let created = client
        .request("bots/create", json!({"name": "研究员", "avatar": {"kind": "mono", "hue": "teal"}, "model": "deepseek/deepseek-flash", "thinking": "low"}))
        .await
        .unwrap();
    let chosen = created["bot"]["id"].as_str().unwrap().to_string();
    let created = client.request("bots/create", json!({"name": "没选过", "avatar": {"kind": "mono", "hue": "sage"}})).await.unwrap();
    let plain = created["bot"]["id"].as_str().unwrap().to_string();
    drop((client, child));

    // Not opened yet when her restarts: the choice is in its file.
    let (_child, client, _incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let opened = client.request("bots/open", json!({"bot_id": chosen})).await.unwrap();
    assert_eq!(opened["snapshot"]["session"]["model"], "deepseek/deepseek-flash");
    assert_eq!(opened["snapshot"]["session"]["thinking"], "low");
    let opened = client.request("bots/open", json!({"bot_id": plain})).await.unwrap();
    assert_eq!(opened["snapshot"]["session"]["model"], "mock/mock-1", "without a choice, the configured model");
}

/// The panel shows them as the bot's: clearing leaves them, whether its conversation is loaded or
/// not, and a log that will not load does not stop it.
#[tokio::test]
async fn clearing_leaves_the_model_and_the_permission_mode() {
    let env = TestEnv::new("manual", "").await;
    let settings = async |client: &Client, bot: &str| {
        let opened = client.request("bots/open", json!({"bot_id": bot})).await.unwrap();
        let info = &opened["snapshot"]["session"];
        (opened["session_id"].as_str().unwrap().to_string(), info["model"].clone(), info["thinking"].clone(), info["permission_mode"].clone())
    };
    let chosen = (json!("deepseek/deepseek-flash"), json!("low"), json!("accept-edits"));
    let (child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    drop(incoming);
    let created = client.request("bots/create", json!({"name": "研究员", "avatar": {"kind": "mono", "hue": "teal"}})).await.unwrap();
    let bot = created["bot"]["id"].as_str().unwrap().to_string();
    let (first, ..) = settings(&client, &bot).await;
    client.request("session/op", json!({"session_id": first, "op": {"type": "set_model", "model": chosen.0, "thinking": chosen.1}})).await.unwrap();
    client.request("session/op", json!({"session_id": first, "op": {"type": "set_permission_mode", "mode": chosen.2}})).await.unwrap();
    tokio::time::timeout(WAIT, async {
        while settings(&client, &bot).await.3 != chosen.2 {
            tokio::time::sleep(std::time::Duration::from_millis(20)).await;
        }
    })
    .await
    .expect("the settings took");

    // Loaded.
    client.request("bots/clear", json!({"bot_id": bot})).await.unwrap();
    let (second, model, thinking, mode) = settings(&client, &bot).await;
    assert_ne!(second, first);
    assert_eq!((model, thinking, mode), chosen.clone());
    drop((client, child));

    // Not loaded, and read back from its file after another restart.
    let changed = json!("bypass-permissions");
    let (child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    client.request("bots/subscribe", Value::Null).await.unwrap();
    // A conversation nobody has spoken in has no log yet: this one starts from the file too.
    let (second, ..) = settings(&client, &bot).await;
    say(&client, &second, "[[mock:send:在。]] 在吗").await;
    until_event(&mut wire, ended).await;
    client.request("session/op", json!({"session_id": second, "op": {"type": "set_permission_mode", "mode": changed}})).await.unwrap();
    // The test kills her: it has to be in the log first.
    tokio::time::timeout(WAIT, async {
        while !logs(env.home.path()).iter().any(|(_, text)| text.contains(&second) && text.contains("bypass-permissions")) {
            tokio::time::sleep(std::time::Duration::from_millis(20)).await;
        }
    })
    .await
    .expect("the setting reached its log");
    drop((client, child, wire));
    let (child, client, _incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    client.request("bots/subscribe", Value::Null).await.unwrap();
    client.request("bots/clear", json!({"bot_id": bot})).await.unwrap();
    drop((client, child));
    let (child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    client.request("bots/subscribe", Value::Null).await.unwrap();
    let (third, model, thinking, mode) = settings(&client, &bot).await;
    assert_ne!(third, second);
    assert_eq!((model, thinking, mode), (chosen.0.clone(), chosen.1.clone(), changed));

    // A log that will not load: clearing goes on, a new conversation starts.
    say(&client, &third, "[[mock:send:在。]] 在吗").await;
    until_event(&mut wire, ended).await;
    drop((client, child, wire));
    let (log, text) = logs(env.home.path()).into_iter().find(|(_, text)| text.contains(&third)).expect("its log");
    let header = text.lines().next().unwrap();
    std::fs::write(&log, format!("{header}\nnot an entry\n{{\"neither\":1}}\n")).unwrap();
    let (_child, client, _incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    client.request("bots/subscribe", Value::Null).await.unwrap();
    client.request("bots/clear", json!({"bot_id": bot})).await.unwrap();
    let (fourth, ..) = settings(&client, &bot).await;
    assert_ne!(fourth, third);
}

#[tokio::test]
async fn renaming_avatars_clearing_and_deleting() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    client.request("bots/subscribe", Value::Null).await.unwrap();
    let created = client.request("bots/create", json!({"name": "写作助手", "avatar": {"kind": "mono", "hue": "clay"}})).await.unwrap();
    let bot = created["bot"]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;
    say(&client, &session, "[[mock:send:在。]] 在吗").await;
    until_event(&mut wire, ended).await;

    // A new name reaches the model at its next turn.
    client.request("bots/update", json!({"bot_id": bot, "name": "编辑", "pinned": true})).await.unwrap();
    let row = until_row(&mut wire, &bot, |b| b["name"] == "编辑").await;
    assert_eq!(row["pinned"], true);
    assert!(row.get("muted").is_none(), "a bot alerts by default: {row}");
    let muted = client.request("bots/update", json!({"bot_id": bot, "muted": true})).await.unwrap();
    assert_eq!(muted["bot"]["muted"], true);
    until_row(&mut wire, &bot, |b| b["muted"] == true).await;
    say(&client, &session, "[[mock:send:我是编辑。]] 你叫什么").await;
    until_event(&mut wire, ended).await;
    let (_, log) = logs(env.home.path()).into_iter().find(|(_, text)| text.contains(&session)).unwrap();
    assert!(log.contains(r#""type":"bot_name","name":"编辑""#), "{log}");

    // Avatars.
    let updated = client.request("bots/update", json!({"bot_id": bot, "avatar": {"kind": "image", "png": PNG}})).await.unwrap();
    assert_eq!(updated["bot"]["avatar"], json!({"kind": "image", "rev": 1}));
    let avatar = client.request("bots/avatar", json!({"bot_id": bot})).await.unwrap();
    assert!(avatar["data"].as_str().unwrap().starts_with("data:image/png;base64,iVBOR"));
    let refused = |e: her_rpc::RpcError| e.data.and_then(|d| d["kind"].as_str().map(str::to_string)).unwrap_or_default();
    let not_png = client.request("bots/update", json!({"bot_id": bot, "avatar": {"kind": "image", "png": "aGVsbG8="}})).await;
    assert_eq!(refused(not_png.unwrap_err()), "bot_invalid");
    let mark = client.request("bots/update", json!({"bot_id": bot, "avatar": {"kind": "mark"}})).await;
    assert_eq!(refused(mark.unwrap_err()), "bot_default");
    let long = client.request("bots/create", json!({"name": "一二三四五六七八九十一二三四五六七八九十一", "avatar": {"kind": "mono", "hue": "sage"}})).await;
    assert_eq!(refused(long.unwrap_err()), "bot_invalid");

    // Clearing: the conversation goes to the trash, the next one is new.
    client.request("bots/clear", json!({"bot_id": bot})).await.unwrap();
    until_row(&mut wire, &bot, |b| b.get("session_id").is_none() && b.get("last").is_none()).await;
    assert!(logs(env.home.path()).iter().all(|(_, text)| !text.contains(&session)), "the log left");
    let trashed = std::fs::read_dir(env.home.path().join("trash").join("sessions")).unwrap().count();
    assert!(trashed >= 1);
    let again = open(&client, &bot).await;
    assert_ne!(again, session);

    // Deleting: never the default bot.
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let default = listed["bots"].as_array().unwrap().iter().find(|b| b["default"] == true).unwrap()["id"].as_str().unwrap().to_string();
    let refused_default = client.request("bots/delete", json!({"bot_id": default})).await;
    assert_eq!(refused(refused_default.unwrap_err()), "bot_default");
    client.request("bots/delete", json!({"bot_id": bot})).await.unwrap();
    loop {
        let changed = tokio::time::timeout(WAIT, wire.bots.recv()).await.unwrap().unwrap();
        if changed["deleted"].as_array().is_some_and(|d| d.iter().any(|id| id == bot.as_str())) {
            break;
        }
    }
    assert!(!env.home.path().join("bots").join(&bot).exists());
    assert!(env.home.path().join("trash").join("bots").join(&bot).exists());
    let missing = client.request("bots/open", json!({"bot_id": bot})).await;
    assert_eq!(refused(missing.unwrap_err()), "bot_not_found");
}

#[tokio::test]
async fn bots_and_their_rows_survive_a_restart() {
    let env = TestEnv::new("manual", "").await;
    let bot;
    {
        let (mut child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
        native_init(&client).await;
        let mut wire = route(incoming);
        let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
        bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
        let session = open(&client, &bot).await;
        say(&client, &session, "[[mock:send:记下了。]] 记一下").await;
        until_event(&mut wire, ended).await;
        until_row(&mut wire, &bot, |b| b["unread"] == 1).await;
        client.request("bots/update", json!({"bot_id": bot, "muted": true})).await.unwrap();
        client.request("server/shutdown", json!({})).await.unwrap();
        common::exit_code(&mut child).await;
    }
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let _wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bots = listed["bots"].as_array().unwrap();
    assert_eq!(bots.len(), 1, "the default bot is made once");
    assert_eq!(bots[0]["id"], bot.as_str());
    assert_eq!(bots[0]["last"]["text"], "记下了。");
    assert_eq!(bots[0]["unread"], 1);
    assert_eq!(bots[0]["muted"], true);
    // Its conversation loads again, with what was said.
    let opened = client.request("bots/open", json!({"bot_id": bot})).await.unwrap();
    let transcript = opened["snapshot"]["transcript"].to_string();
    assert!(transcript.contains("记下了。"));
    assert!(!transcript.contains("<reminder>"), "what it is told is not shown: {transcript}");
}

/// A bot made before the characters were told apart by their eyes: its `bot.json` keeps the old name.
#[tokio::test]
async fn a_bot_saved_with_an_old_character_stays_in_the_list() {
    let env = TestEnv::new("manual", "").await;
    let id = "0193f0aa-0000-7000-8000-000000000001";
    let dir = env.home.path().join("bots").join(id);
    std::fs::create_dir_all(&dir).unwrap();
    let saved = json!({"version": 1, "id": id, "name": "老朋友", "avatar": {"kind": "char", "shape": "ghost", "hue": "teal"}, "created_at": "2026-11-03T14:05:00Z"});
    std::fs::write(dir.join("bot.json"), saved.to_string()).unwrap();

    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let _wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let row = listed["bots"].as_array().unwrap().iter().find(|b| b["id"] == id).expect("the old bot is still listed");
    assert_eq!(row["name"], "老朋友");
    assert_eq!(row["avatar"], json!({"kind": "char", "shape": "lid", "hue": "teal"}));
}

/// The default bot was named HerBot before it was named Her: one saved then keeps its name, and
/// no second default bot is made.
#[tokio::test]
async fn a_default_bot_saved_as_herbot_keeps_its_name() {
    let env = TestEnv::new("manual", "").await;
    let id = "0193f0aa-0000-7000-8000-000000000002";
    let dir = env.home.path().join("bots").join(id);
    std::fs::create_dir_all(&dir).unwrap();
    let saved = json!({"version": 1, "id": id, "name": "HerBot", "avatar": {"kind": "mark"}, "default": true, "created_at": "2026-11-03T14:05:00Z"});
    std::fs::write(dir.join("bot.json"), saved.to_string()).unwrap();

    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let _wire = route(incoming);
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bots = listed["bots"].as_array().unwrap();
    assert_eq!(bots.len(), 1, "{bots:#?}");
    assert_eq!((bots[0]["id"].as_str(), bots[0]["name"].as_str(), bots[0]["default"].as_bool()), (Some(id), Some("HerBot"), Some(true)));
}

/// The head of a PNG: enough to tell what it is and how large.
fn png_head(width: u32, height: u32) -> Vec<u8> {
    let mut bytes = b"\x89PNG\r\n\x1a\n\0\0\0\rIHDR".to_vec();
    bytes.extend(width.to_be_bytes());
    bytes.extend(height.to_be_bytes());
    bytes.extend([8, 6, 0, 0, 0, 0, 0, 0, 0]);
    bytes
}

#[tokio::test]
async fn files_and_questions_show_in_the_bots_row_and_count_as_unread() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let mut wire = route(incoming);
    let schema = SchemaCheck::new(&["BotsChangedParams"]);
    client.request("bots/subscribe", Value::Null).await.unwrap();
    let created = client.request("bots/create", json!({"name": "\u{7814}\u{7a76}\u{5458}", "avatar": {"kind": "mono", "hue": "teal"}})).await.unwrap();
    let bot = created["bot"]["id"].as_str().unwrap().to_string();
    let session = open(&client, &bot).await;
    let folder = env.home.path().join("bots").join(&bot).join("files");
    std::fs::write(folder.join("chart.png"), png_head(960, 600)).unwrap();
    std::fs::write(folder.join("report.pdf"), b"%PDF").unwrap();

    // Files: the row says what was sent; the result says what each one is.
    say(&client, &session, "[[mock:attach:chart.png,report.pdf]] send them").await;
    let events = until_event(&mut wire, ended).await;
    let done = events.iter().find(|e| e["type"] == "tool_completed" && e["result"]["tool_name"] == "send_to_user").expect("the result");
    assert_eq!(done["result"]["is_error"], false);
    let files = done["result"]["details"]["files"].as_array().expect("the files");
    assert_eq!(files[0]["image"], true);
    assert_eq!((files[0]["width"].as_u64(), files[0]["height"].as_u64()), (Some(960), Some(600)));
    assert!(files[1].get("image").is_none());
    let row = until_row(&mut wire, &bot, |b| b["last"]["from"] == "bot").await;
    schema.check("BotsChangedParams", &json!({"bots": [row.clone()]}));
    assert_eq!(row["last"]["text"], "");
    assert_eq!(row["last"]["images"], 1);
    assert_eq!(row["last"]["file"], "report.pdf");
    assert_eq!(row["unread"], 1);

    // A question: its words, and the task ends with no reminder.
    say(&client, &session, "[[mock:ask:send to whom?|finance|everyone]] then?").await;
    let events = until_event(&mut wire, ended).await;
    assert_eq!(sent(&events), ["send to whom?"]);
    let row = until_row(&mut wire, &bot, |b| b["last"]["text"] == "send to whom?").await;
    assert!(row["last"].get("file").is_none());
    assert_eq!(row["unread"], 2);

    // A wrong path: nothing to see, nothing unread.
    say(&client, &session, "[[mock:attach:missing.pdf]] again").await;
    let events = until_event(&mut wire, |e| e["type"] == "tool_completed").await;
    assert_eq!(events.last().unwrap()["result"]["is_error"], true);
    let list = client.request("bots/subscribe", Value::Null).await.unwrap();
    let row = list["bots"].as_array().unwrap().iter().find(|b| b["id"] == bot.as_str()).unwrap().clone();
    assert_eq!(row["unread"], 2);
}
