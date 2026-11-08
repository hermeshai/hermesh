//! End-to-end: with nothing set up, her assumes no model. Every way in says so and what to do,
//! and a provider connected supplies the default (standalone-cli-design.md §7.2).

mod common;

use common::Demux;
use common::TestEnv;
use common::native_init;
use common::native_prompt;
use her_protocol::api::error_code;
use serde_json::Value;
use serde_json::json;

/// A home nobody has set up: no `model`, and no provider but the built-in ones. DeepSeek's and
/// Anthropic's addresses still lead to the mock, should a test give them a key, and a local Ollama
/// on the developer's machine stays out of it.
async fn bare() -> TestEnv {
    let env = TestEnv::new("manual", "").await;
    std::fs::write(env.home.path().join("config.toml"), "").unwrap();
    let base = env.mock.base_url();
    std::fs::write(
        env.home.path().join("models.toml"),
        format!(
            "[providers.deepseek]\nbase_url = \"{base}\"\n[providers.anthropic]\nbase_url = \"{base}\"\n\
             [providers.ollama]\nbase_url = \"http://127.0.0.1:9/v1\"\n"
        ),
    )
    .unwrap();
    env
}

fn sessions_in(env: &TestEnv) -> usize {
    let found = std::fs::read_dir(env.home.path().join("sessions"));
    found.map_or(0, |dir| dir.flatten().count())
}

fn chats_in(env: &TestEnv) -> usize {
    let found = std::fs::read_dir(env.home.path().join("chats"));
    found.map_or(0, |dir| dir.flatten().count())
}

async fn output(env: &TestEnv, args: &[&str]) -> (i32, String, String) {
    let out = env.her(args).output().await.expect("run her");
    (
        out.status.code().unwrap_or(-1),
        String::from_utf8_lossy(&out.stdout).into_owned(),
        String::from_utf8_lossy(&out.stderr).into_owned(),
    )
}

async fn json_of(env: &TestEnv, args: &[&str]) -> Value {
    let (_, stdout, stderr) = output(env, args).await;
    serde_json::from_str(&stdout).unwrap_or_else(|e| panic!("{args:?}: {e}\n{stdout}\n{stderr}"))
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_command_line_says_how_to_set_a_model_up() {
    let env = bare().await;

    for args in [&["exec", "hello"][..], &["chat", "hello"][..]] {
        let (code, stdout, stderr) = output(&env, args).await;
        assert_eq!(code, 2, "{args:?}: {stderr}");
        assert_eq!(stdout, "", "{args:?}");
        assert!(stderr.contains("no model is set up"), "{stderr}");
        for way in ["her login", "ANTHROPIC_API_KEY", "DEEPSEEK_API_KEY", "her login chatgpt", "-m ollama/<model>", "config.toml"] {
            assert!(stderr.contains(way), "{way} is missing: {stderr}");
        }
        assert!(!stderr.contains("deepseek/"), "no model is named for the person: {stderr}");
    }
    assert_eq!(sessions_in(&env), 0, "no session was started");

    // A script that asked for JSON gets JSON, with the same advice.
    let (code, stdout, stderr) = output(&env, &["exec", "--json", "hello"]).await;
    assert_eq!(code, 2);
    assert_eq!(stderr, "");
    let failure: Value = serde_json::from_str(&stdout).unwrap_or_else(|e| panic!("{e}: {stdout}"));
    assert_eq!(failure["error"]["kind"], "no_model", "{failure:#}");
    assert_eq!(failure["error"]["message"], "no model is set up");
    assert!(failure["error"]["help"].as_str().unwrap().contains("ANTHROPIC_API_KEY"), "{failure:#}");
    assert_eq!(failure["exit_code"], 2);

    let status = json_of(&env, &["status", "--json"]).await;
    assert_eq!(status["model"]["ref"], Value::Null, "{status:#}");
    assert_eq!(status["model"]["error"], "no model is set up");
    let (_, text, _) = output(&env, &["status"]).await;
    assert!(text.contains("none is set up"), "{text}");
    assert!(!text.contains("deepseek"), "{text}");

    let (code, stdout, _) = output(&env, &["doctor", "--json"]).await;
    assert_eq!(code, 1);
    let doctor: Value = serde_json::from_str(&stdout).unwrap();
    let check = doctor["checks"].as_array().unwrap().iter().find(|c| c["name"] == "default model").unwrap();
    assert_eq!(check["ok"], false, "{check}");

    let (code, stdout, _) = output(&env, &["models"]).await;
    assert_eq!(code, 0);
    assert!(stdout.contains("To set up a model"), "{stdout}");
    assert!(!stdout.contains("(default)"), "{stdout}");
    let models = json_of(&env, &["models", "--json"]).await;
    assert_eq!(models["models"], json!([]), "nothing is offered before it is connected");
    assert!(models["defaults"].get("model").is_none(), "{models:#}");

    // A model named by hand is taken at its word: what is missing is its own key.
    let (code, _, stderr) = output(&env, &["exec", "hello", "-m", "anthropic/claude-sonnet-5-5", "--no-save"]).await;
    assert_eq!(code, 2, "a model that cannot run is a setup problem, not a failed task: {stderr}");
    assert!(stderr.contains("no credentials for anthropic/claude-sonnet-5-5"), "{stderr}");
    assert!(stderr.contains("ANTHROPIC_API_KEY"), "{stderr}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_configured_model_stays_the_default_without_a_key() {
    let env = bare().await;
    std::fs::write(env.home.path().join("config.toml"), "model = \"deepseek/deepseek-v4-pro\"\n").unwrap();
    let status = json_of(&env, &["status", "--json"]).await;
    assert_eq!(status["model"]["ref"], "deepseek/deepseek-v4-pro");
    assert_eq!(status["model"]["chosen"], "configured");
    let models = json_of(&env, &["models", "--json"]).await;
    assert_eq!(models["defaults"]["model"], "deepseek/deepseek-v4-pro");
    let listed = models["models"].as_array().unwrap();
    assert_eq!(listed.len(), 1, "{listed:#?}");
    assert_eq!((&listed[0]["id"], &listed[0]["available"]), (&json!("deepseek/deepseek-v4-pro"), &json!(false)));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_key_in_the_environment_makes_its_provider_the_default() {
    let env = bare().await;
    let out = env.her(&["status", "--json"]).env("ANTHROPIC_API_KEY", "sk-env").output().await.unwrap();
    let status: Value = serde_json::from_slice(&out.stdout).unwrap();
    assert_eq!(status["model"]["ref"], "anthropic/claude-sonnet-5-5", "{status:#}");
    assert_eq!(status["model"]["chosen"], "first usable provider");

    let out = env.her(&["doctor", "--json"]).env("ANTHROPIC_API_KEY", "sk-env").output().await.unwrap();
    assert_eq!(out.status.code(), Some(0), "{}", String::from_utf8_lossy(&out.stdout));

    let out = env.her(&["models"]).env("ANTHROPIC_API_KEY", "sk-env").output().await.unwrap();
    let text = String::from_utf8_lossy(&out.stdout);
    assert!(text.contains("anthropic/claude-sonnet-5-5 (default)"), "{text}");
    assert!(!text.contains("To set up a model"), "{text}");

    // Another provider's model is still one named by hand, and the configured one still wins.
    std::fs::write(env.home.path().join("config.toml"), "model = \"deepseek/deepseek-flash\"\n").unwrap();
    let out = env.her(&["status", "--json"]).env("ANTHROPIC_API_KEY", "sk-env").output().await.unwrap();
    let status: Value = serde_json::from_slice(&out.stdout).unwrap();
    assert_eq!(status["model"]["ref"], "deepseek/deepseek-flash", "{status:#}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_new_session_waits_for_a_model_and_the_first_key_is_enough() {
    let env = bare().await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let demux = Demux::spawn(incoming);

    let models = client.request("models/list", json!({})).await.unwrap();
    assert_eq!(models["models"], json!([]));
    assert!(models["defaults"].get("model").is_none(), "{models:#}");

    let cwd = env.workspace("project");
    let err = client.request("session/new", json!({"cwd": cwd})).await.unwrap_err();
    assert_eq!(err.code, error_code::NO_MODEL);
    assert_eq!(err.kind(), Some("no_model"));
    assert!(err.message.contains("no model is set up"), "{}", err.message);

    // A session without a project leaves no folder behind.
    let err = client.request("session/new", json!({})).await.unwrap_err();
    assert_eq!(err.kind(), Some("no_model"));
    assert_eq!(chats_in(&env), 0);

    // A bot has nothing to run on either; asked again later, it starts.
    let bots = client.request("bots/subscribe", Value::Null).await.unwrap();
    let bot = bots["bots"][0]["id"].as_str().unwrap().to_string();
    let err = client.request("bots/open", json!({"bot_id": bot})).await.unwrap_err();
    assert_eq!(err.kind(), Some("no_model"));
    assert_eq!(sessions_in(&env), 0);

    // A key saved now is the model, without a restart.
    client.request("auth/set", json!({"provider": "deepseek", "api_key": "sk-good-key"})).await.unwrap();
    let models = client.request("models/list", json!({})).await.unwrap();
    assert_eq!(models["defaults"]["model"], "deepseek/deepseek-flash", "{models:#}");
    let session = client.request("session/new", json!({"cwd": cwd})).await.unwrap();
    assert_eq!(session["snapshot"]["session"]["model"], "deepseek/deepseek-flash");
    let opened = client.request("bots/open", json!({"bot_id": bot})).await.unwrap();
    assert_eq!(opened["snapshot"]["session"]["model"], "deepseek/deepseek-flash");
    drop(demux);

    // Without the key, nothing is assumed again.
    client.request("auth/delete", json!({"provider": "deepseek"})).await.unwrap();
    let models = client.request("models/list", json!({})).await.unwrap();
    assert!(models["defaults"].get("model").is_none(), "{models:#}");
    let err = client.request("session/new", json!({"cwd": cwd})).await.unwrap_err();
    assert_eq!(err.kind(), Some("no_model"));
    // One named by hand is taken at its word.
    let named = client.request("session/new", json!({"cwd": cwd, "model": "deepseek/deepseek-flash"})).await.unwrap();
    assert_eq!(named["snapshot"]["session"]["model"], "deepseek/deepseek-flash");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn an_acp_client_hears_auth_required() {
    let env = bare().await;
    let (_child, client, _incoming) = env.spawn_rpc(&["acp"]);
    client
        .request("initialize", json!({"protocolVersion": 1, "clientCapabilities": {}, "clientInfo": {"name": "e2e", "version": "1"}}))
        .await
        .unwrap();
    let cwd = env.workspace("project");
    let err = client.request("session/new", json!({"cwd": cwd, "mcpServers": []})).await.unwrap_err();
    assert_eq!(err.code, -32000, "{err:?}");
    assert_eq!(err.kind(), Some("auth_required"));
    assert!(err.message.contains("no model is set up"), "{}", err.message);
    assert_eq!(sessions_in(&env), 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn an_acp_client_is_offered_ways_to_sign_in() {
    let env = bare().await;
    let (_child, client, _incoming) = env.spawn_rpc(&["acp"]);
    let init = |terminal: bool| json!({"protocolVersion": 1, "clientCapabilities": {"auth": {"terminal": terminal}}, "clientInfo": {"name": "e2e", "version": "1"}});
    let plain = client.request("initialize", init(false)).await.unwrap();
    let ids: Vec<&str> = plain["authMethods"].as_array().unwrap().iter().filter_map(|m| m["id"].as_str()).collect();
    assert_eq!(ids, ["her-chatgpt", "her-saved"]);
    let with_terminal = client.request("initialize", init(true)).await.unwrap();
    let terminal = &with_terminal["authMethods"][0];
    assert_eq!((terminal["id"].as_str(), terminal["type"].as_str()), (Some("her-terminal"), Some("terminal")));
    assert_eq!(terminal["args"], json!(["--login"]));

    // Nothing saved yet: the method says so.
    let err = client.request("authenticate", json!({"methodId": "her-saved"})).await.unwrap_err();
    assert_eq!((err.code, err.kind()), (-32000, Some("auth_required")), "{err:?}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn an_acp_client_hears_which_key_a_configured_model_lacks() {
    let env = bare().await;
    std::fs::write(env.home.path().join("config.toml"), "model = \"deepseek/deepseek-flash\"\n").unwrap();
    let (_child, client, _incoming) = env.spawn_rpc(&["acp"]);
    client.request("initialize", json!({"protocolVersion": 1, "clientCapabilities": {}, "clientInfo": {"name": "e2e", "version": "1"}})).await.unwrap();
    let cwd = env.workspace("project");
    let err = client.request("session/new", json!({"cwd": cwd, "mcpServers": []})).await.unwrap_err();
    assert_eq!((err.code, err.kind()), (-32000, Some("auth_required")), "{err:?}");
    assert!(err.message.contains("her login deepseek"), "{}", err.message);
    assert_eq!(sessions_in(&env), 0);
    let err = client.request("authenticate", json!({"methodId": "her-saved"})).await.unwrap_err();
    assert!(err.message.contains("deepseek"), "{}", err.message);
}

/// The only connection went away and nothing else is set up: the session's history can still be
/// read, the session says what happened, and a prompt fails instead of reaching some other model.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_session_whose_only_model_is_gone_still_opens() {
    let env = bare().await;
    let connection = "35e57942-0000-4000-8000-000000000001";
    std::fs::create_dir_all(env.home.path().join("connections")).unwrap();
    std::fs::write(
        env.home.path().join("connections/config.json"),
        json!({"schema": 1, "connections": [{
            "connection_id": connection, "adapter_id": "qwenwork", "identity_key": "account-a",
            "enabled": true, "idempotency_key": "e2e",
            "models": [{"id": "flash", "name": "Flash", "context_window": 1000000, "max_output_tokens": null}]
        }]})
        .to_string(),
    )
    .unwrap();
    let flash = format!("local-{connection}/flash");
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let demux = Demux::spawn(incoming);

    let models = client.request("models/list", json!({})).await.unwrap();
    assert_eq!(models["defaults"]["model"], json!(flash), "a connection is a connected provider: {models:#}");
    let cwd = env.workspace("gone");
    let used = client.request("session/new", json!({"cwd": cwd})).await.unwrap();
    assert_eq!(used["snapshot"]["session"]["model"], json!(flash));
    let used = used["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&used);
    client.request("session/close", json!({"session_id": used})).await.unwrap();
    while common::recv(&mut events).await.method() != "session/detached" {}

    client.request("connections/disconnect", json!({"connection_id": connection})).await.unwrap();
    let models = client.request("models/list", json!({})).await.unwrap();
    assert!(models["defaults"].get("model").is_none(), "{models:#}");

    let resumed = client.request("session/resume", json!({"session_id": used})).await.unwrap();
    assert_eq!(resumed["snapshot"]["session"]["model"], json!(flash), "the log's model is kept");
    let turn = native_prompt(&client, &mut events, &used, "hello", false).await;
    let warned = turn.events.iter().any(|e| e["type"] == "warning" && e["message"].as_str().is_some_and(|m| m.contains("connect a model")))
        || serde_json::to_string(&resumed).unwrap().contains("connect a model");
    assert!(warned, "the session says what happened: {:#?}", turn.events);
    assert_eq!(turn.end["type"], "task_failed", "{:#?}", turn.events);
}
