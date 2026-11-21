//! Regression and upgrade-compatibility checks for removing `--require-ai-consent`.
//! Uses only synthetic configurations and fake local receivers; never uses customer addresses or credentials.

mod common;

use common::{Demux, TestEnv, native_init, native_prompt};
use serde_json::json;

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn her_serve_removes_require_ai_consent_flag_and_backend_gate() {
    // 1. Pre-existing legacy `ai-consent.json` and synthetic LAN HTTP custom model in `models.toml`.
    let env = TestEnv::new("bypass-permissions", "[models]\nnetwork = false\n").await;

    // `--require-ai-consent` is completely removed from CLI parsing (not defaulted).
    let output = env
        .her(&["serve", "--stdio", "--require-ai-consent"])
        .output()
        .await
        .expect("spawn her");
    assert!(!output.status.success(), "expected --require-ai-consent to be rejected");
    let stderr = String::from_utf8_lossy(&output.stderr);
    assert!(
        stderr.contains("require-ai-consent") && stderr.contains("unexpected argument"),
        "unexpected stderr: {stderr}"
    );

    let legacy_consent = env.home.path().join("ai-consent.json");
    let legacy_payload = r#"{"allowed":{"provider":"legacy","endpoint":"https://example.invalid/v1"}}"#;
    std::fs::write(&legacy_consent, legacy_payload).unwrap();

    let models_path = env.home.path().join("models.toml");
    let models = std::fs::read_to_string(&models_path).unwrap();
    let synthetic_lan = r#"
[providers.lan_gw]
name = "Synthetic LAN Gateway"
api = "openai-chat"
base_url = "http://192.168.100.10:18080/v1"
auth = "none"

[[providers.lan_gw.models]]
id = "lan-model"
name = "LAN Model"
"#;
    std::fs::write(&models_path, format!("{models}\n{synthetic_lan}")).unwrap();

    let (mut child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    for removed in ["ai_consent/status", "ai_consent/allow", "ai_consent/revoke"] {
        let err = client.request(removed, json!({})).await.unwrap_err();
        assert_eq!(err.code, her_rpc::code::METHOD_NOT_FOUND, "{removed}: {err:?}");
    }

    let demux = Demux::spawn(incoming);
    let bots = client.request("bots/subscribe", json!({})).await.unwrap();
    let bot = bots["bots"][0]["id"].as_str().unwrap();
    let opened = client.request("bots/open", json!({"bot_id": bot})).await.unwrap();
    let session = opened["session_id"].as_str().unwrap();
    let mut events = demux.subscribe(session);

    // 3. Normal prompt succeeds directly without any backend consent gate.
    let accepted = native_prompt(&client, &mut events, session, "fictional direct prompt", false).await;
    assert_eq!(accepted.end["type"], "task_completed");
    assert_eq!(env.mock.stats().chat_controls().len(), 1);

    // 4. Synthetic custom LAN HTTP model is listed and selectable without backend provider verification blocking it.
    let listed = client.request("models/list", json!({})).await.unwrap();
    assert!(
        listed["models"]
            .as_array()
            .unwrap()
            .iter()
            .any(|m| m["id"] == "lan_gw/lan-model"),
        "expected synthetic LAN HTTP model in models/list: {listed}"
    );
    client
        .request(
            "session/op",
            json!({"session_id": session, "op": {"type": "set_model", "model": "lan_gw/lan-model"}}),
        )
        .await
        .unwrap();
    // Submitting a prompt to the synthetic LAN HTTP model must NOT be rejected by any `ai_consent_required` gate.
    client
        .request(
            "session/op",
            json!({
                "session_id": session,
                "op": {"type": "prompt", "input": [{"type": "text", "text": "fictional lan http prompt"}]}
            }),
        )
        .await
        .unwrap();

    // 5. Legacy `ai-consent.json` is preserved untouched (not destructively deleted).
    assert_eq!(std::fs::read_to_string(&legacy_consent).unwrap(), legacy_payload);
    let _ = child.kill().await;
}
