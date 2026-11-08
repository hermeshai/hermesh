//! Remote servers end to end: checking before adding, signing in with OAuth, the older
//! HTTP+SSE transport (mcp-design §13.6–§13.8, customize-design §10.9), through the real
//! `her` process: the page's methods, the command line, and a session that uses the server.

mod common;

use common::TestEnv;
use her_mock::mcp::McpTestOptions;
use her_mock::mcp_auth::McpTestAuth;
use her_mock::mcp_auth::OAuthOptions;
use her_rpc::Incoming;
use her_rpc::IncomingRx;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::time::Duration;
use tokio::io::AsyncBufReadExt;

fn project(env: &TestEnv, name: &str) -> std::path::PathBuf {
    let dir = env.workspace(name);
    std::fs::create_dir_all(dir.join(".git")).unwrap();
    dir
}

fn user_config(env: &TestEnv) -> String {
    std::fs::read_to_string(env.home.path().join("config.toml")).unwrap()
}

fn credentials(env: &TestEnv) -> String {
    std::fs::read_to_string(env.home.path().join("credentials.toml")).unwrap_or_default()
}

async fn run(env: &TestEnv, cwd: &Path, args: &[&str]) -> (i32, String) {
    let output = env.her_in(cwd, args).output().await.unwrap();
    let text = format!("{}{}", String::from_utf8_lossy(&output.stdout), String::from_utf8_lossy(&output.stderr));
    (output.status.code().unwrap_or(-1), text)
}

/// A server whose sign-in offers dynamic registration but not her's metadata document, which
/// would otherwise come first (D-182), so these tests cover registering and reusing a client.
async fn oauth_server(registration: bool) -> her_mock::mcp::McpHttpServer {
    let auth = McpTestAuth::OAuth(OAuthOptions { metadata_document: false, registration, token_ttl_secs: 3600 });
    her_mock::mcp::serve_http("127.0.0.1:0", McpTestOptions { auth, ..Default::default() }).await.unwrap()
}

/// The next notification `method`, collecting the `mcp/probe_progress` ones met on the way.
async fn until(rx: &mut IncomingRx, method: &str, progress: &mut Vec<Value>) -> Value {
    let deadline = tokio::time::Instant::now() + Duration::from_secs(20);
    loop {
        let incoming = tokio::time::timeout_at(deadline, rx.recv()).await.unwrap_or_else(|_| panic!("no {method}")).expect("connection closed");
        if let Incoming::Notification { method: m, params, .. } = incoming {
            if m == "mcp/probe_progress" {
                progress.push(params.clone());
            }
            if m == method {
                return params;
            }
        }
    }
}

/// A session in `cwd` asks the mock model to call the server's `env` tool; its last message.
async fn use_in_exec(env: &TestEnv, cwd: &Path, server: &str) -> String {
    let prompt = format!("[[mock:mcp_env:{server}:HER_NO_BROWSER]] read it");
    let output = env.her_in(cwd, &["exec", "--output", "stream-json", &prompt]).output().await.unwrap();
    let events: Vec<Value> = String::from_utf8_lossy(&output.stdout).lines().filter_map(|l| serde_json::from_str(l).ok()).collect();
    events
        .iter()
        .find(|e| e["type"] == "task_completed")
        .and_then(|e| e["last_message"].as_str())
        .unwrap_or_else(|| panic!("no task_completed: {events:#?}\n{}", String::from_utf8_lossy(&output.stderr)))
        .to_string()
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_page_checks_signs_in_then_adds_an_oauth_server() {
    let env = TestEnv::new("manual", "").await;
    let schema = common::SchemaCheck::new(&["McpProbeResult", "McpLoginResult", "McpCheckResult", "McpListResult"]);
    let work = project(&env, "repo");
    let mock = oauth_server(true).await;
    let (mut child, client, mut rx) = env.spawn_rpc(&["serve", "--stdio"]);
    common::native_init(&client).await;
    client.request("mcp/list", json!({"cwd": work})).await.unwrap();

    // The check finds the sign-in settings and proposes OAuth with a registration, step by step.
    let server = json!({"name": "tracker", "url": mock.url()});
    let probed = client.request("mcp/probe", json!({"probe_id": "p1", "server": server, "cwd": work})).await.unwrap();
    schema.check("McpProbeResult", &probed);
    assert_eq!(probed.get("error"), None, "{probed:#}");
    assert_eq!((probed["suggested"]["auth"].as_str(), probed["suggested"]["client"].as_str()), (Some("oauth"), Some("registration")));
    assert_eq!(probed["auth"]["oauth"]["registration"], true);
    let steps: Vec<&str> = probed["steps"].as_array().unwrap().iter().map(|s| s["id"].as_str().unwrap()).collect();
    assert_eq!(steps, ["connect", "resource", "auth_server"]);
    assert!(user_config(&env).find("tracker").is_none(), "the check writes nothing");

    // Sign in before anything is written; the browser comes back to her.
    let login = client
        .request("mcp/login", json!({"name": "tracker", "cwd": work, "server": {"name": "tracker", "url": mock.url(), "auth": "oauth"}}))
        .await
        .unwrap();
    schema.check("McpLoginResult", &login);
    assert_eq!(login["client"], "registration");
    let (status, page) = her_mock::mcp_auth::browse(login["authorize_url"].as_str().unwrap()).await.unwrap();
    assert!(status.contains("200") && page.contains("Hermesh"), "{status}");
    let mut progress = Vec::new();
    let changed = until(&mut rx, "mcp/changed", &mut progress).await;
    assert_eq!((changed["login_id"].clone(), changed.get("login_error")), (login["login_id"].clone(), None), "{changed:#}");
    assert!(credentials(&env).contains("mcp-oauth.tracker"), "the token is in the credential store");
    assert!(!user_config(&env).contains("access_token") && !user_config(&env).contains("at-"), "never in the configuration");

    // Then it is added, and checking it connects with the token.
    let edit = json!({"key_path": "mcp_servers.tracker", "value": {"url": mock.url(), "auth": "oauth"}});
    client.request("config/write", json!({"layer": "user", "cwd": work, "edits": [edit]})).await.unwrap();
    let checked = client.request("mcp/check", json!({"name": "tracker", "cwd": work})).await.unwrap();
    schema.check("McpCheckResult", &checked);
    assert_eq!(checked["server"]["status"]["state"], "ok", "{checked:#}");
    assert_eq!((checked["server"]["auth"].as_str(), checked["server"]["auth_state"].as_str()), (Some("oauth"), Some("logged_in")));
    assert!(checked["tools"].as_array().unwrap().len() > 3);
    let (registrations, grants, _, _) = mock.oauth_counts();
    assert_eq!((registrations, grants), (1, 1));

    // A session uses it.
    assert!(use_in_exec(&env, &work, "tracker").await.starts_with("mcp done"), "the session called the signed-in server");

    // Signing out revokes, and the page says someone has to sign in.
    client.request("mcp/logout", json!({"name": "tracker"})).await.unwrap();
    assert_eq!(mock.oauth_counts().3, 1);
    let listed = client.request("mcp/list", json!({"cwd": work})).await.unwrap();
    schema.check("McpListResult", &listed);
    let tracker = listed["servers"].as_array().unwrap().iter().find(|s| s["name"] == "tracker").unwrap().clone();
    assert_eq!((tracker["status"]["state"].as_str(), tracker["auth_state"].as_str()), (Some("needs_login"), Some("needs_login")), "{tracker:#}");

    // A probe can be cancelled while it runs.
    let slow = json!({"name": "slow", "command": "sh", "args": ["-c", "sleep 30"]});
    let probe = client.request("mcp/probe", json!({"probe_id": "p2", "server": slow, "cwd": work}));
    let cancel = async {
        tokio::time::sleep(Duration::from_millis(500)).await;
        client.request("mcp/probe_cancel", json!({"probe_id": "p2"})).await.unwrap();
    };
    let (probed, ()) = tokio::join!(probe, cancel);
    assert_eq!(probed.unwrap()["error"], "cancelled");
    drop(client);
    common::exit_code(&mut child).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_command_line_checks_before_adding_and_signs_in() {
    let env = TestEnv::new("manual", "").await;
    let work = project(&env, "repo");

    // Nothing is written for a server that does not answer, unless asked to.
    let (code, out) = run(&env, &work, &["mcp", "add", "gone", "--url", "http://127.0.0.1:9/mcp"]).await;
    assert_eq!(code, 1, "{out}");
    assert!(out.contains("nothing was written"), "{out}");
    assert!(!user_config(&env).contains("gone"));
    let (code, out) = run(&env, &work, &["mcp", "add", "gone", "--url", "http://127.0.0.1:9/mcp", "--force"]).await;
    assert_eq!(code, 0, "{out}");
    assert!(user_config(&env).contains("[mcp_servers.gone]"));

    // An OAuth server: the command prints the address, waits for the browser, then writes.
    let mock = oauth_server(true).await;
    let mut child = env
        .her_in(&work, &["mcp", "add", "tracker", "--url", &mock.url()])
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .spawn()
        .unwrap();
    let mut lines = tokio::io::BufReader::new(child.stdout.take().unwrap()).lines();
    let mut printed = Vec::new();
    let address = loop {
        let line = tokio::time::timeout(Duration::from_secs(20), lines.next_line()).await.unwrap().unwrap().expect("an address");
        printed.push(line.clone());
        if line.starts_with("http://") {
            break line;
        }
    };
    assert!(printed.iter().any(|l| l.contains("connecting … ok (HTTP 401)")), "{printed:#?}");
    her_mock::mcp_auth::browse(&address).await.unwrap();
    let status = tokio::time::timeout(Duration::from_secs(20), child.wait()).await.unwrap().unwrap();
    while let Ok(Some(line)) = lines.next_line().await {
        printed.push(line);
    }
    assert!(status.success(), "{printed:#?}");
    assert!(printed.iter().any(|l| l.starts_with("signed in to tracker")), "{printed:#?}");
    let config = user_config(&env);
    assert!(config.contains("[mcp_servers.tracker]") && config.contains("auth = \"oauth\""), "{config}");
    assert!(use_in_exec(&env, &work, "tracker").await.starts_with("mcp done"));

    // `her mcp login` signs in again with the same registration; logout revokes.
    let mut child = env.her_in(&work, &["mcp", "login", "tracker"]).stdout(std::process::Stdio::piped()).spawn().unwrap();
    let mut lines = tokio::io::BufReader::new(child.stdout.take().unwrap()).lines();
    let address = loop {
        let line = tokio::time::timeout(Duration::from_secs(20), lines.next_line()).await.unwrap().unwrap().expect("an address");
        if line.starts_with("http://") {
            break line;
        }
    };
    her_mock::mcp_auth::browse(&address).await.unwrap();
    assert!(tokio::time::timeout(Duration::from_secs(20), child.wait()).await.unwrap().unwrap().success());
    assert_eq!(mock.oauth_counts().0, 1, "one registration for both sign-ins");
    let (code, out) = run(&env, &work, &["mcp", "logout", "tracker"]).await;
    assert_eq!(code, 0, "{out}");
    assert!(!credentials(&env).contains("mcp-oauth.tracker"));

    // The older transport, found by the check at an address that does not say so.
    let legacy = her_mock::mcp::serve_http("127.0.0.1:0", McpTestOptions { legacy_sse: true, ..Default::default() }).await.unwrap();
    let address = legacy.url().replace("/sse", "/stream");
    let (code, out) = run(&env, &work, &["mcp", "add", "old", "--url", &address]).await;
    assert_eq!(code, 0, "{out}");
    assert!(user_config(&env).contains("transport = \"sse\""), "{}", user_config(&env));
    assert!(use_in_exec(&env, &work, "old").await.starts_with("mcp done"));
}

#[tokio::test]
async fn one_service_is_added_once_and_keeps_its_logo() {
    let env = TestEnv::new("manual", "").await;
    let work = project(&env, "work");
    let docs = her_mock::mcp::serve_http("127.0.0.1:0", McpTestOptions::default()).await.unwrap();
    let (code, out) = run(&env, &work, &["mcp", "add", "docs", "--url", &docs.url()]).await;
    assert_eq!(code, 0, "{out}");
    // The logo the server declares in its handshake is kept for the list (mcp-design §13.9).
    let icons: Vec<String> = std::fs::read_dir(env.home.path().join("mcp/icons"))
        .unwrap()
        .map(|e| std::fs::read_to_string(e.unwrap().path()).unwrap())
        .collect();
    assert!(icons.iter().any(|i| i.contains("\"declared\":true") && i.contains("\"mime\":\"image/png\"")), "{icons:?}");

    // The same address under another name and spelling is the same service (§13.10).
    let again = format!("{}/#top", docs.url().replace("http://", "HTTP://"));
    let (code, out) = run(&env, &work, &["mcp", "add", "docs-2", "--url", &again]).await;
    assert_eq!(code, 1, "{out}");
    assert!(out.contains("docs already reaches this server"), "{out}");
    assert!(!user_config(&env).contains("docs-2"));
    let (code, out) = run(&env, &work, &["mcp", "add", "docs-2", "--url", &again, "--force"]).await;
    assert_eq!(code, 0, "{out}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn oauth_login_resumes_cancels_and_replaces_only_a_changed_resource() {
    let env = TestEnv::new("manual", "").await;
    let first = oauth_server(true).await;
    let second = oauth_server(true).await;
    let (mut child, client, mut rx) = env.spawn_rpc(&["serve", "--stdio"]);
    common::native_init(&client).await;
    client.request("mcp/list", json!({})).await.unwrap();
    let params = |url: String| json!({"name":"resume", "server":{"name":"resume", "url":url, "auth":"oauth"}});
    let (a, b) = tokio::join!(client.request("mcp/login", params(first.url())), client.request("mcp/login", params(first.url())));
    let a = a.unwrap();
    assert_eq!(a, b.unwrap(), "concurrent clicks resume one authorization URL and login ID");
    assert_eq!(first.oauth_counts().0, 1);
    let resumed = client.request("mcp/login", params(first.url())).await.unwrap();
    assert_eq!(a, resumed, "a new page/request can reattach without replacing the listener");
    client.request("mcp/login_cancel", json!({"name":"resume"})).await.unwrap();
    let cancelled = until(&mut rx, "mcp/changed", &mut Vec::new()).await;
    assert_eq!(cancelled["login_id"], a["login_id"]);
    assert!(cancelled["login_error"].is_string());
    let retry = client.request("mcp/login", params(first.url())).await.unwrap();
    assert_ne!(retry["login_id"], a["login_id"]);
    // A reused discovery name with a different server cannot reuse the old authorization.
    let replacement = client.request("mcp/login", params(second.url())).await.unwrap();
    assert_ne!(replacement["login_id"], retry["login_id"]);
    let old_ended = until(&mut rx, "mcp/changed", &mut Vec::new()).await;
    assert_eq!(old_ended["login_id"], retry["login_id"]);
    assert_eq!(client.request("mcp/login", params(second.url())).await.unwrap(), replacement, "the old completion must not erase the new login");
    let (status, _) = her_mock::mcp_auth::browse(replacement["authorize_url"].as_str().unwrap()).await.unwrap();
    assert!(status.contains("200"));
    let completed = until(&mut rx, "mcp/changed", &mut Vec::new()).await;
    assert_eq!(completed["login_id"], replacement["login_id"]);
    assert!(completed.get("login_error").is_none());
    assert_eq!(first.oauth_counts().1, 0);
    assert_eq!(second.oauth_counts().1, 1);
    client.request("mcp/logout", json!({"name":"resume"})).await.unwrap();
    assert!(!credentials(&env).contains("mcp-oauth.resume"));
    drop(client);
    common::exit_code(&mut child).await;
}
