//! `data/export` over the native protocol: the real `her serve`, the mock model, and a private
//! HER_HOME that a conversation, a routine and a saved key have been through.

mod common;

use common::Demux;
use common::SchemaCheck;
use common::TestEnv;
use common::native_init;
use common::native_prompt;
use serde_json::Value;
use serde_json::json;
use std::collections::BTreeMap;
use std::io::Read;
use std::path::Path;
use std::path::PathBuf;

/// The archive's entries, by name, with their bytes.
fn entries(path: &Path) -> BTreeMap<String, Vec<u8>> {
    let mut archive = zip::ZipArchive::new(std::fs::File::open(path).unwrap()).unwrap();
    (0..archive.len())
        .map(|i| {
            let mut entry = archive.by_index(i).unwrap();
            let mut bytes = Vec::new();
            entry.read_to_end(&mut bytes).unwrap();
            (entry.name().unwrap().to_string(), bytes)
        })
        .collect()
}

/// What was said goes in, with the assistant, the routine she made (from the database the service
/// holds open) and the settings; the key saved in the credential store does not, nor the store.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn an_export_holds_the_conversation_and_the_routine_and_not_the_saved_key() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let demux = Demux::spawn(incoming);
    client.request("credentials/set", json!({"name": "mcp.tracker", "value": "tracker-token-in-the-store"})).await.unwrap();
    let listed = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = listed["bots"][0]["id"].as_str().unwrap().to_string();
    let opened = client.request("bots/open", json!({"bot_id": bot})).await.unwrap();
    let session = opened["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);
    for prompt in ["[[mock:send:Noted: the plan for Friday.]] Plan Friday.", "[[mock:routine:Stretch|0 8 * * *|say:Time to stretch.]]"] {
        let turn = native_prompt(&client, &mut events, &session, prompt, true).await;
        assert_eq!(turn.end["type"], "task_completed", "{:?}", turn.types());
    }

    let made = client.request("data/export", Value::Null).await.unwrap();
    SchemaCheck::new(&["DataExportResult"]).check("DataExportResult", &made);
    let path = PathBuf::from(made["path"].as_str().unwrap());
    assert_eq!(path.parent().unwrap(), env.home.path().join("exports"));
    assert_eq!(made["bytes"], std::fs::metadata(&path).unwrap().len());
    let got = entries(&path);

    let said = got.iter().any(|(name, bytes)| name.starts_with("sessions/") && name.ends_with(".jsonl") && String::from_utf8_lossy(bytes).contains("Noted: the plan for Friday."));
    assert!(said, "no conversation in {:?}", got.keys());
    assert!(got.contains_key(&format!("bots/{bot}/bot.json")), "{:?}", got.keys());
    assert!(got.contains_key("config.toml") && got.contains_key("models.toml"), "{:?}", got.keys());
    let database = &got["her.db"];
    assert!(database.starts_with(b"SQLite format 3\0"));
    assert!(String::from_utf8_lossy(database).contains("Stretch"), "the routine is not in the database's copy");

    assert!(env.home.path().join("credentials.toml").exists());
    assert!(!got.contains_key("credentials.toml"));
    for (name, bytes) in &got {
        assert!(!String::from_utf8_lossy(bytes).contains("tracker-token-in-the-store"), "{name} holds the saved key");
    }
}
