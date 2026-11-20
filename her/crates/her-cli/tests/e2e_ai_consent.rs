//! The real Her service and two loopback model receivers. No live credentials or providers.

mod common;

use common::{Demux, TestEnv, native_init, native_prompt, recv};
use her_rpc::Client;
use serde_json::{Value, json};

async fn status(client: &Client, model: &str) -> Value {
    client.request("ai_consent/status", json!({"model": model})).await.unwrap()
}

async fn allow(client: &Client, model: &str, status: &Value) -> Result<Value, her_rpc::RpcError> {
    client.request("ai_consent/allow", json!({
        "model": model, "recipient": status["recipient"], "revision": status["revision"],
    })).await
}

async fn refused_prompt(client: &Client, session: &str, text: &str) {
    let error = client.request("session/op", json!({
        "session_id": session,
        "op": {"type": "prompt", "input": [{"type": "text", "text": text}]},
    })).await.unwrap_err();
    assert_eq!(error.data.as_ref().unwrap()["kind"], "ai_consent_required");
    assert!(error.message.contains("AI sharing is blocked"), "{error:?}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn her_only_sends_to_the_currently_approved_recipient() {
    let env = TestEnv::new("bypass-permissions", "[models]\nnetwork = false\n").await;
    let other = TestEnv::new("manual", "").await;
    let models_path = env.home.path().join("models.toml");
    let models = std::fs::read_to_string(&models_path).unwrap();
    let second = other.mock.models_toml().replace("providers.mock", "providers.second");
    std::fs::write(&models_path, format!("{models}\n{second}")).unwrap();

    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio", "--require-ai-consent"]);
    native_init(&client).await;
    let demux = Demux::spawn(incoming);
    let bots = client.request("bots/subscribe", json!({})).await.unwrap();
    let bot = bots["bots"][0]["id"].as_str().unwrap();
    let opened = client.request("bots/open", json!({"bot_id": bot})).await.unwrap();
    let session = opened["session_id"].as_str().unwrap();
    let mut events = demux.subscribe(session);
    let first = "mock/mock-1";
    let second = "second/mock-1";
    let counts = || (env.mock.stats().chat_controls().len(), other.mock.stats().chat_controls().len());

    let offered = status(&client, first).await;
    assert_eq!(offered["recipient"]["provider"], "mock");
    assert!(offered["recipient"]["endpoint"].as_str().unwrap().starts_with(&env.mock.base_url()));
    assert!(offered["allowed"].is_null());
    refused_prompt(&client, session, "fictional declined draft").await;
    assert_eq!(counts(), (0, 0));

    allow(&client, first, &offered).await.unwrap();
    let accepted = native_prompt(&client, &mut events, session, "fictional allowed input", false).await;
    assert_eq!(accepted.end["type"], "task_completed");
    assert_eq!(counts(), (1, 0));
    assert!(!env.mock.stats().chat_controls()[0].to_string().contains("fictional declined draft"));

    let stale = status(&client, first).await;
    client.request("ai_consent/revoke", json!({})).await.unwrap();
    assert!(allow(&client, first, &stale).await.is_err());
    refused_prompt(&client, session, "fictional revoked input").await;
    // Compact goes straight to the inference gate with the existing history.
    client.request("session/op", json!({"session_id": session, "op": {"type": "compact"}})).await.unwrap();
    loop {
        let incoming = recv(&mut events).await;
        let event = incoming.params()["event"].clone();
        if event["type"] == "task_failed" {
            assert!(event.to_string().contains("AI sharing is blocked"), "{event}");
            break;
        }
        assert_ne!(event["type"], "task_completed");
    }
    assert_eq!(counts(), (1, 0));

    allow(&client, first, &status(&client, first).await).await.unwrap();
    client.request("session/op", json!({"session_id": session, "op": {"type": "set_model", "model": second}})).await.unwrap();
    refused_prompt(&client, session, "fictional switched input").await;
    assert_eq!(counts(), (1, 0));
    let offer = status(&client, second).await;
    assert!(offer["recipient"]["endpoint"].as_str().unwrap().starts_with(&other.mock.base_url()));
    assert!(offer["allowed"].is_null());
    assert!(allow(&client, second, &offered).await.is_err());
    allow(&client, second, &offer).await.unwrap();
    let accepted = native_prompt(&client, &mut events, session, "fictional second approval", false).await;
    assert_eq!(accepted.end["type"], "task_completed");
    assert_eq!(counts(), (1, 1));
    let received = other.mock.stats().chat_controls()[0].to_string();
    assert!(!received.contains("fictional revoked input") && !received.contains("fictional switched input"));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn her_persists_consent_across_restart_for_matching_recipient_only() {
    let env = TestEnv::new("bypass-permissions", "[models]\nnetwork = false\n").await;
    let other = TestEnv::new("manual", "").await;
    let models_path = env.home.path().join("models.toml");
    let models = std::fs::read_to_string(&models_path).unwrap();
    let second = other.mock.models_toml().replace("providers.mock", "providers.second");
    std::fs::write(&models_path, format!("{models}\n{second}")).unwrap();

    let first = "mock/mock-1";
    let second_model = "second/mock-1";
    let counts = || (env.mock.stats().chat_controls().len(), other.mock.stats().chat_controls().len());

    // First process: approve mock/mock-1 and send once.
    {
        let (mut child, client, incoming) = env.spawn_rpc(&["serve", "--stdio", "--require-ai-consent"]);
        native_init(&client).await;
        let demux = Demux::spawn(incoming);
        let bots = client.request("bots/subscribe", json!({})).await.unwrap();
        let bot = bots["bots"][0]["id"].as_str().unwrap();
        let opened = client.request("bots/open", json!({"bot_id": bot})).await.unwrap();
        let session = opened["session_id"].as_str().unwrap();
        let mut events = demux.subscribe(session);

        let offered = status(&client, first).await;
        allow(&client, first, &offered).await.unwrap();
        let accepted = native_prompt(&client, &mut events, session, "fictional before restart", false).await;
        assert_eq!(accepted.end["type"], "task_completed");
        assert_eq!(counts(), (1, 0));
        let _ = child.kill().await;
    }

    // Second process (restarted with same home): approval still applies to mock/mock-1,
    // does NOT apply to second/mock-1, and revocation persists across the next restart.
    {
        let (mut child, client, incoming) = env.spawn_rpc(&["serve", "--stdio", "--require-ai-consent"]);
        native_init(&client).await;
        let demux = Demux::spawn(incoming);
        let bots = client.request("bots/subscribe", json!({})).await.unwrap();
        let bot = bots["bots"][0]["id"].as_str().unwrap();
        let opened = client.request("bots/open", json!({"bot_id": bot})).await.unwrap();
        let session = opened["session_id"].as_str().unwrap();
        let mut events = demux.subscribe(session);

        let after_restart = status(&client, first).await;
        assert_eq!(after_restart["allowed"], after_restart["recipient"]);
        let accepted = native_prompt(&client, &mut events, session, "fictional after restart", false).await;
        assert_eq!(accepted.end["type"], "task_completed");
        assert_eq!(counts(), (2, 0));

        // Switching to a different recipient after restart must NOT reuse the persisted grant.
        client.request("session/op", json!({"session_id": session, "op": {"type": "set_model", "model": second_model}})).await.unwrap();
        refused_prompt(&client, session, "fictional mismatched recipient after restart").await;
        assert_eq!(counts(), (2, 0));

        // Explicit revoke starts with a fresh grant, separate from the model switch.
        let offer = status(&client, second_model).await;
        assert!(offer["allowed"].is_null());
        allow(&client, second_model, &offer).await.unwrap();
        let saved = env.home.path().join("ai-consent.json");
        assert!(saved.is_file());

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let permissions = std::fs::metadata(env.home.path()).unwrap().permissions();
            std::fs::set_permissions(env.home.path(), std::fs::Permissions::from_mode(0o500)).unwrap();
            // A privileged test process may still be able to write here.
            let failed = if tempfile::NamedTempFile::new_in(env.home.path()).is_err() {
                Some(client.request("ai_consent/revoke", json!({})).await)
            } else {
                None
            };
            std::fs::set_permissions(env.home.path(), permissions).unwrap();
            if let Some(result) = failed {
                let error = result.unwrap_err();
                assert!(error.message.contains("cannot remove saved AI sharing consent"), "{error:?}");
                assert!(saved.is_file());
                assert!(status(&client, second_model).await["allowed"].is_null());
                refused_prompt(&client, session, "fictional after failed disk revoke").await;
                assert_eq!(counts(), (2, 0));
            }
        }

        client.request("ai_consent/revoke", json!({})).await.unwrap();
        assert!(!saved.exists());
        let _ = child.kill().await;
    }

    // Third process: revoked consent stays revoked after restart.
    {
        let (mut child, client, incoming) = env.spawn_rpc(&["serve", "--stdio", "--require-ai-consent"]);
        native_init(&client).await;
        let _demux = Demux::spawn(incoming);
        let bots = client.request("bots/subscribe", json!({})).await.unwrap();
        let bot = bots["bots"][0]["id"].as_str().unwrap();
        let opened = client.request("bots/open", json!({"bot_id": bot})).await.unwrap();
        let session = opened["session_id"].as_str().unwrap();

        let revoked_status = status(&client, second_model).await;
        assert!(revoked_status["allowed"].is_null());
        client.request("session/op", json!({"session_id": session, "op": {"type": "set_model", "model": second_model}})).await.unwrap();
        refused_prompt(&client, session, "fictional after revoke restart").await;
        assert_eq!(counts(), (2, 0));
        let _ = child.kill().await;
    }
}
