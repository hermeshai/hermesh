//! End-to-end: `her login`, `her logout` and `her models check` (standalone-cli-design §7).
//! The mock answers for DeepSeek's account and for a service of one's own.

mod common;

use common::TestEnv;
use serde_json::Value;
use std::process::Stdio;
use tokio::io::AsyncWriteExt as _;

/// A home with no model configured, and DeepSeek's address at the mock.
async fn bare() -> TestEnv {
    let env = TestEnv::new("manual", "").await;
    std::fs::write(env.home.path().join("config.toml"), "").unwrap();
    let base = env.mock.base_url();
    std::fs::write(
        env.home.path().join("models.toml"),
        format!("# keep this comment\n[providers.deepseek]\nbase_url = \"{base}\"\n\n[providers.ollama]\nbase_url = \"http://127.0.0.1:9/v1\"\n"),
    )
    .unwrap();
    env
}

/// Runs her with `input` on standard input, as a script would.
async fn piped(env: &TestEnv, args: &[&str], input: &str) -> (i32, String, String) {
    let mut child = env.her(args).stdin(Stdio::piped()).stdout(Stdio::piped()).stderr(Stdio::piped()).spawn().unwrap();
    let mut stdin = child.stdin.take().unwrap();
    stdin.write_all(input.as_bytes()).await.unwrap();
    drop(stdin);
    let out = child.wait_with_output().await.unwrap();
    (
        out.status.code().unwrap_or(-1),
        String::from_utf8_lossy(&out.stdout).into_owned(),
        String::from_utf8_lossy(&out.stderr).into_owned(),
    )
}

async fn status(env: &TestEnv) -> Value {
    let out = env.her(&["status", "--json"]).output().await.unwrap();
    serde_json::from_slice(&out.stdout).unwrap()
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_key_from_a_script_connects_the_provider() {
    let env = bare().await;
    assert_eq!(status(&env).await["model"]["ref"], Value::Null);

    // A key the provider refuses is not saved.
    let (code, stdout, stderr) = piped(&env, &["login", "deepseek", "--key-stdin"], her_mock::REJECTED_KEY).await;
    assert_eq!(code, 2, "{stderr}");
    assert!(stderr.contains("not accepted") && stderr.contains("nothing was saved"), "{stderr}");
    assert!(!stdout.contains("saved"), "{stdout}");
    assert_eq!(status(&env).await["model"]["ref"], Value::Null);
    let (code, stdout, _) = piped(&env, &["login", "deepseek", "--key-stdin", "--json"], her_mock::REJECTED_KEY).await;
    assert_eq!(code, 2, "--json is not a flag of login: {stdout}");

    // A good one is, and the provider becomes the default; nothing is written to the configuration.
    let (code, stdout, stderr) = piped(&env, &["login", "deepseek", "--key-stdin"], "sk-good-key\n").await;
    assert_eq!(code, 0, "{stderr}");
    assert!(stdout.contains("key accepted"), "{stdout}");
    assert_eq!(status(&env).await["model"]["ref"], "deepseek/deepseek-flash");
    assert_eq!(status(&env).await["model"]["chosen"], "first usable provider");
    assert_eq!(std::fs::read_to_string(env.home.path().join("config.toml")).unwrap(), "");

    // With --model it says which, and where.
    let (code, stdout, stderr) = piped(&env, &["login", "deepseek", "--key-stdin", "--model", "deepseek-v4-pro"], "sk-good-key").await;
    assert_eq!(code, 0, "{stderr}");
    assert!(stdout.contains("model set to deepseek/deepseek-v4-pro in "), "{stdout}");
    assert_eq!(status(&env).await["model"]["ref"], "deepseek/deepseek-v4-pro");
    assert_eq!(status(&env).await["model"]["chosen"], "configured");

    // The key is there for a task, without an environment variable.
    let (code, _, stderr) = piped(&env, &["exec", "say hi"], "").await;
    assert_eq!(code, 0, "{stderr}");

    // Logging out takes the key away.
    let (code, stdout, stderr) = piped(&env, &["logout", "deepseek"], "").await;
    assert_eq!(code, 0, "{stderr}");
    assert!(stdout.contains("removed the saved key"), "{stdout}");
    let (code, _, stderr) = piped(&env, &["exec", "say hi"], "").await;
    assert_eq!(code, 2, "{stderr}");
    assert!(stderr.contains("no credentials for deepseek/deepseek-v4-pro"), "{stderr}");
    assert!(stderr.contains("her login deepseek"), "{stderr}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn without_a_terminal_it_asks_for_nothing() {
    let env = bare().await;
    let (code, _, stderr) = piped(&env, &["login"], "").await;
    assert_eq!(code, 2);
    assert!(stderr.contains("her login deepseek --key-stdin"), "{stderr}");
    let (code, _, stderr) = piped(&env, &["login", "deepseek"], "").await;
    assert_eq!(code, 2);
    assert!(stderr.contains("--key-stdin"), "{stderr}");
    assert!(stderr.contains("never goes in an argument"), "{stderr}");
    let (code, _, stderr) = piped(&env, &["login", "no-such-provider", "--key-stdin"], "x").await;
    assert_eq!(code, 2);
    assert!(stderr.contains("unknown provider") && stderr.contains("--base-url"), "{stderr}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_service_of_ones_own() {
    let env = bare().await;
    let base = env.mock.base_url();
    let models = env.home.path().join("models.toml");
    let before = std::fs::read_to_string(&models).unwrap();

    // A refused key leaves models.toml as it was.
    let (code, _, stderr) = piped(&env, &["login", "gateway", "--base-url", &base, "--key-stdin"], her_mock::REJECTED_KEY).await;
    assert_eq!(code, 2, "{stderr}");
    assert_eq!(std::fs::read_to_string(&models).unwrap(), before);

    let (code, stdout, stderr) = piped(&env, &["login", "gateway", "--base-url", &base, "--key-stdin", "--model", "mock-1"], "sk-good-key").await;
    assert_eq!(code, 0, "{stderr}");
    assert!(stdout.contains("added gateway to "), "{stdout}");
    assert!(stdout.contains("model set to gateway/mock-1"), "{stdout}");
    let text = std::fs::read_to_string(&models).unwrap();
    assert!(text.contains("# keep this comment"), "{text}");
    assert!(text.contains("[providers.gateway]"), "{text}");
    let (code, stdout, stderr) = piped(&env, &["exec", "say hi"], "").await;
    assert_eq!(code, 0, "{stderr}");
    assert!(stdout.contains("Hello from mock."), "{stdout}");

    // One that takes no key.
    let (code, _, stderr) = piped(&env, &["login", "openly", "--base-url", &base, "--no-key", "--model", "mock-1"], "").await;
    assert_eq!(code, 0, "{stderr}");
    assert!(std::fs::read_to_string(&models).unwrap().contains("auth = \"none\""));

    // A built-in name is not taken over.
    let (code, _, stderr) = piped(&env, &["login", "openai", "--base-url", &base, "--key-stdin"], "x").await;
    assert_eq!(code, 2);
    assert!(stderr.contains("built-in"), "{stderr}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn models_check_tries_the_model() {
    let env = TestEnv::new("manual", "").await;
    let out = env.her(&["models", "check", "--json"]).output().await.unwrap();
    assert_eq!(out.status.code(), Some(0), "{}", String::from_utf8_lossy(&out.stderr));
    let report: Value = serde_json::from_slice(&out.stdout).unwrap();
    assert_eq!(report["model"], "mock/mock-1");
    assert_eq!(report["ok"], true, "{report:#}");
    let status = |name: &str| report["checks"].as_array().unwrap().iter().find(|c| c["name"] == name).unwrap()["status"].as_str().unwrap().to_string();
    assert_eq!(status("text"), "ok", "{report:#}");
    assert_eq!(status("tools"), "ok", "{report:#}");
    assert_eq!(status("parallel tools"), "ok", "{report:#}");
    assert_eq!(status("cancel"), "ok", "{report:#}");

    // Text for people, and a model that cannot run fails before any request.
    let out = env.her(&["models", "check"]).output().await.unwrap();
    assert!(String::from_utf8_lossy(&out.stdout).contains("parallel tools"));
    let out = env.her(&["models", "check", "deepseek/deepseek-flash"]).output().await.unwrap();
    assert_eq!(out.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&out.stderr).contains("no credentials"));

    // doctor --online makes one request.
    let before = env.mock.stats().requests.load(std::sync::atomic::Ordering::Relaxed);
    let out = env.her(&["doctor", "--online", "--json"]).output().await.unwrap();
    assert_eq!(out.status.code(), Some(0), "{}", String::from_utf8_lossy(&out.stdout));
    assert_eq!(env.mock.stats().requests.load(std::sync::atomic::Ordering::Relaxed), before + 1);
    let doctor: Value = serde_json::from_slice(&out.stdout).unwrap();
    assert!(doctor["checks"].as_array().unwrap().iter().any(|c| c["name"] == "model answers" && c["ok"] == true), "{doctor:#}");
}
