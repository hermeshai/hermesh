//! End-to-end: a service added by its address through the real `her serve`
//! (docs/kernel/custom-endpoint-design.md §13.2). The mock stands in for a gateway that speaks
//! both protocols; nothing leaves the machine.

mod common;

use common::Demux;
use common::SchemaCheck;
use common::TestEnv;
use common::native_init;
use common::native_prompt;
use her_mock::GATEWAY_KEY;
use her_mock::Gateway;
use her_rpc::Client;
use serde_json::Value;
use serde_json::json;

fn provider(models: &Value, id: &str) -> Value {
    models["providers"].as_array().unwrap().iter().find(|p| p["id"] == id).cloned().unwrap_or(Value::Null)
}

fn model(models: &Value, id: &str) -> Value {
    models["models"].as_array().unwrap().iter().find(|m| m["id"] == id).cloned().unwrap_or(Value::Null)
}

/// One turn on `model` in a new session; what the mock was sent for it.
async fn turn(env: &TestEnv, client: &Client, demux: &Demux, model: &str) -> Value {
    let cwd = env.workspace(model.replace('/', "-").as_str());
    let created = client.request("session/new", json!({"cwd": cwd, "model": model})).await.unwrap();
    let session = created["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);
    let done = native_prompt(client, &mut events, &session, "hello", false).await;
    assert_eq!(done.end["type"], "task_completed", "{model}: {:?}", done.end);
    env.mock.stats().chat_controls().last().cloned().unwrap()
}

/// A kind of service added by its address: what the person types after its root, what the probe
/// must find there, and where each model's request must go.
struct Kind {
    gateway: Gateway,
    name: &'static str,
    typed: &'static str,
    /// Protocol, its prefix after the root, the header its key goes in (as models.toml says it).
    found: &'static [(&'static str, &'static str, &'static str)],
    /// Model, the path its request reaches, the header its key came in, whether over Messages.
    sends: &'static [(&'static str, &'static str, &'static str, bool)],
}

const KINDS: &[Kind] = &[
    // Chat Completions only: Claude goes there too.
    Kind {
        gateway: Gateway::ChatOnly,
        name: "gw-chat-only",
        typed: "",
        found: &[("openai-chat", "/v1", "bearer")],
        sends: &[("claude-sonnet-4-5", "/v1/chat/completions", "bearer", false)],
    },
    // Messages only, with Anthropic's list: a model that is not Claude goes to Messages as well.
    Kind {
        gateway: Gateway::AnthropicOnly,
        name: "gw-anthropic-only",
        typed: "",
        found: &[("anthropic-messages", "", "x_api_key")],
        sends: &[("house-model-7", "/v1/messages", "x-api-key", true)],
    },
    // Azure: the prefix typed in full, the key in `api-key`.
    Kind {
        gateway: Gateway::Azure,
        name: "gw-azure",
        typed: "/openai/v1",
        found: &[("openai-chat", "/openai/v1", "api_key")],
        sends: &[("mock-1", "/openai/v1/chat/completions", "api-key", false)],
    },
    // Messages under /anthropic, taking only a bearer token: each protocol keeps its own prefix
    // and header.
    Kind {
        gateway: Gateway::AnthropicPath,
        name: "gw-anthropic-path",
        typed: "/v1",
        found: &[("openai-chat", "/v1", "bearer"), ("anthropic-messages", "/anthropic", "bearer")],
        sends: &[("claude-sonnet-4-5", "/anthropic/v1/messages", "bearer", true), ("mock-1", "/v1/chat/completions", "bearer", false)],
    },
    // New API: a Claude model its list keeps to Chat Completions stays there.
    Kind {
        gateway: Gateway::NewApi,
        name: "gw-newapi",
        typed: "",
        found: &[("openai-chat", "/v1", "bearer"), ("anthropic-messages", "", "x_api_key")],
        sends: &[("claude-sonnet-4-5", "/v1/chat/completions", "bearer", false), ("claude-opus-4-1", "/v1/messages", "x-api-key", true)],
    },
    // The key before any path: found once the key is right.
    Kind {
        gateway: Gateway::KeyFirst,
        name: "gw-key-first",
        typed: "",
        found: &[("openai-chat", "/v1", "bearer")],
        sends: &[("mock-1", "/v1/chat/completions", "bearer", false)],
    },
];

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_service_added_by_its_address_is_probed_saved_and_used() {
    let env = TestEnv::new("manual", "[models]\nnetwork = false\n").await;
    let schema = SchemaCheck::new(&["ProvidersAddResult", "ModelsListResult", "ModelsConfigureResult"]);
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let demux = Demux::spawn(incoming);
    let base = format!("{}/gateway", env.mock.base_url());

    // An address nothing answers at changes nothing.
    let unusable = client.request("providers/add", json!({"base_url": "http://127.0.0.1:9"})).await.unwrap_err();
    assert_eq!(unusable.code, -32048);
    assert_eq!(unusable.data.as_ref().unwrap()["reason"], "unreachable");

    let added = client.request("providers/add", json!({"base_url": base, "api_key": "sk-gateway-test", "name": "Acme"})).await.unwrap();
    schema.check("ProvidersAddResult", &added);
    assert_eq!(added["provider"]["id"], "acme");
    let found: Vec<(&str, &str)> =
        added["probe"]["protocols"].as_array().unwrap().iter().map(|p| (p["api"].as_str().unwrap(), p["base_url"].as_str().unwrap())).collect();
    assert_eq!(found, [("openai-chat", format!("{base}/v1").as_str()), ("anthropic-messages", base.as_str())]);
    assert_eq!((&added["probe"]["models"], &added["probe"]["known"]), (&json!(3), &json!(1)));
    assert_eq!(added["provider"]["credential"], "stored");
    let toml = std::fs::read_to_string(env.home.path().join("models.toml")).unwrap();
    assert!(toml.contains("[providers.acme]") && !toml.contains("sk-gateway-test"), "{toml}");

    let models = client.request("models/list", json!({})).await.unwrap();
    schema.check("ModelsListResult", &models);
    assert_eq!(provider(&models, "acme")["custom"]["protocols"].as_array().unwrap().len(), 2);
    let claude = model(&models, "acme/claude-sonnet-4-5");
    assert_eq!((&claude["known"], &claude["shown"]), (&json!(true), &json!(true)));
    assert!(claude["canonical"]["name"].as_str().is_some_and(|n| n.contains("Sonnet")), "{claude}");
    let house = model(&models, "acme/house-model-7");
    assert_eq!((&house["known"], &house["shown"]), (&json!(false), &json!(true)), "unknown models fill a custom endpoint's menu");

    // Claude goes to Messages, with its cache breakpoints; the mock's model to Chat Completions.
    let ask = |model: &'static str| {
        let (client, demux, env) = (&client, &demux, &env);
        async move {
            let cwd = env.workspace(model.replace('/', "-").as_str());
            let created = client.request("session/new", json!({"cwd": cwd, "model": model})).await.unwrap();
            let session = created["session_id"].as_str().unwrap().to_string();
            let mut events = demux.subscribe(&session);
            let turn = native_prompt(client, &mut events, &session, "hello", false).await;
            assert_eq!(turn.end["type"], "task_completed", "{model}: {:?}", turn.end);
            env.mock.stats().chat_controls().last().cloned().unwrap()
        }
    };
    let sent = ask("acme/claude-sonnet-4-5").await;
    assert_eq!(sent["_route"], "messages");
    assert_eq!(sent["model"], "claude-sonnet-4-5", "the gateway's name for the model");
    assert!(sent["system"].to_string().contains("cache_control"), "{sent}");
    let sent = ask("acme/mock-1").await;
    assert!(sent.get("_route").is_none(), "{sent}");
    assert_eq!(sent["model"], "mock-1");

    // Saying what an unknown model is like.
    let configured = client.request("models/configure", json!({"model": "acme/house-model-7", "canonical": "GPT-4o"})).await.unwrap();
    schema.check("ModelsConfigureResult", &configured);
    assert_eq!(configured["model"]["canonical"]["id"], "openai/gpt-4o", "any name that finds it, saved as its id");
    assert_eq!((&configured["model"]["known"], &configured["model"]["images"]), (&json!(true), &json!(true)));
    let bad = client.request("models/configure", json!({"model": "acme/house-model-7", "canonical": "no-such-model-anywhere"})).await.unwrap_err();
    assert_eq!(bad.code, -32602);
    let cleared = client.request("models/configure", json!({"model": "acme/house-model-7", "clear": true})).await.unwrap();
    assert_eq!(cleared["model"]["known"], false);
    let builtin = client.request("models/configure", json!({"model": "deepseek/deepseek-flash", "context_window": 1000})).await.unwrap_err();
    assert_eq!(builtin.code, -32602, "built-in providers are not changed here");

    // A new name renames; the rest stays.
    let renamed = client.request("providers/update", json!({"id": "acme", "name": "Acme Corp"})).await.unwrap();
    assert_eq!(renamed["provider"]["name"], "Acme Corp");
    assert_eq!(renamed["provider"]["custom"]["protocols"].as_array().unwrap().len(), 2);

    let new_receiver = TestEnv::new("manual", "").await;
    let changed = client.request("providers/update", json!({"id": "acme", "base_url": new_receiver.mock.base_url()})).await.unwrap_err();
    assert_eq!(changed.code, -32602);
    assert!(changed.message.contains("API key"));
    assert_eq!(new_receiver.mock.stats().requests.load(std::sync::atomic::Ordering::Relaxed), 0);

    client.request("providers/remove", json!({"id": "acme"})).await.unwrap();
    let after = client.request("models/list", json!({})).await.unwrap();
    assert!(provider(&after, "acme").is_null());
    assert!(after["models"].as_array().unwrap().iter().all(|m| m["provider"] != "acme"));
    assert!(!std::fs::read_to_string(env.home.path().join("models.toml")).unwrap().contains("[providers.acme]"));
    let again = client.request("providers/remove", json!({"id": "acme"})).await.unwrap_err();
    assert_eq!(again.code, -32602);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_command_line_adds_a_service_the_same_way() {
    let env = TestEnv::new("manual", "[models]\nnetwork = false\n").await;
    let base = format!("{}/gateway", env.mock.base_url());
    let mut child = env
        .her(&["login", "acme", "--base-url", &base, "--key-stdin", "--model", "mock-1"])
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .spawn()
        .unwrap();
    {
        use tokio::io::AsyncWriteExt;
        let mut stdin = child.stdin.take().unwrap();
        stdin.write_all(b"sk-gateway-test").await.unwrap();
    }
    let out = child.wait_with_output().await.unwrap();
    assert!(out.status.success(), "{}\n{}", String::from_utf8_lossy(&out.stdout), String::from_utf8_lossy(&out.stderr));
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(stdout.contains("OpenAI Chat Completions") && stdout.contains("Anthropic Messages"), "{stdout}");
    assert!(stdout.contains("model set to acme/mock-1"), "{stdout}");
    let set = env.her(&["models", "set", "acme/house-model-7", "--like", "openai/gpt-4o", "--context", "64k"]).output().await.unwrap();
    assert!(set.status.success(), "{}", String::from_utf8_lossy(&set.stderr));
    assert!(String::from_utf8_lossy(&set.stdout).contains("like GPT-4o"), "{}", String::from_utf8_lossy(&set.stdout));
    let listed = env.her(&["models", "--provider", "acme", "--all"]).output().await.unwrap();
    let listed = String::from_utf8_lossy(&listed.stdout);
    let line = |id: &str| listed.lines().find(|l| l.trim_start().starts_with(id)).unwrap_or_default().to_string();
    assert!(line("acme/house-model-7").contains("like GPT-4o"), "{listed}");
    assert!(line("acme/claude-sonnet-4-5").contains("like Claude Sonnet 4.5"), "{listed}");
    let removed = env.her(&["logout", "acme", "--remove"]).output().await.unwrap();
    assert!(removed.status.success(), "{}", String::from_utf8_lossy(&removed.stderr));
    assert!(!std::fs::read_to_string(env.home.path().join("models.toml")).unwrap().contains("[providers.acme]"));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn each_kind_of_service_is_probed_saved_and_used() {
    let env = TestEnv::new("manual", "[models]\nnetwork = false\n").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let demux = Demux::spawn(incoming);
    for kind in KINDS {
        let root = env.mock.gateway(kind.gateway).await.unwrap();
        let base = format!("{root}{}", kind.typed);
        if kind.gateway == Gateway::KeyFirst {
            // Every path asks for the key first: no key, or a wrong one, says so.
            let needs = client.request("providers/add", json!({"base_url": base})).await.unwrap_err();
            assert_eq!((needs.code, needs.data.as_ref().unwrap()["reason"].as_str()), (-32048, Some("key_required")), "{needs:?}");
            let wrong = client.request("providers/add", json!({"base_url": base, "api_key": "sk-wrong"})).await.unwrap_err();
            assert_eq!(wrong.code, -32021, "{wrong:?}");
            assert!(wrong.message.contains("Authentication Fails"), "the service's words: {}", wrong.message);
        }
        let added = client
            .request("providers/add", json!({"base_url": base, "api_key": GATEWAY_KEY, "name": kind.name}))
            .await
            .unwrap_or_else(|e| panic!("{}: {e:?}", kind.name));
        assert_eq!(added["provider"]["id"], kind.name);
        let found: Vec<(String, String, String)> = added["probe"]["protocols"]
            .as_array()
            .unwrap()
            .iter()
            .map(|p| (p["api"].as_str().unwrap().to_string(), p["base_url"].as_str().unwrap().to_string(), p["auth_header"].as_str().unwrap_or("").to_string()))
            .collect();
        let expected: Vec<(String, String, String)> = kind.found.iter().map(|(api, prefix, header)| (api.to_string(), format!("{root}{prefix}"), header.to_string())).collect();
        assert_eq!(found, expected, "{}: {:#}", kind.name, added["probe"]);
        for (model, path, header, messages) in kind.sends {
            let sent = turn(&env, &client, &demux, &format!("{}/{model}", kind.name)).await;
            let what = format!("{} {model}: {sent:#}", kind.name);
            assert_eq!(sent["model"], *model, "the service's own name for the model: {what}");
            assert_eq!(sent["_path"], *path, "{what}");
            assert_eq!(sent["_auth"], *header, "{what}");
            assert_eq!(sent.get("_route").is_some(), *messages, "{what}");
            if *messages && model.starts_with("claude") {
                assert!(sent["system"].to_string().contains("cache_control"), "Claude keeps its cache breakpoints: {what}");
            }
        }
    }
}
