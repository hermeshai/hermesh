//! The client against the test server: in-process over Streamable HTTP, and as a real child
//! process over stdio.

use her_mcp::CallOptions;
use her_mcp::McpError;
use her_mcp::McpServer;
use her_mcp::ServerContext;
use her_mcp::types::Content;
use her_mock::mcp::McpTestOptions;
use her_protocol::McpServerConfig;
use her_protocol::McpServerState;
use parking_lot::Mutex;
use serde_json::json;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::OnceLock;
use std::time::Duration;
use std::time::Instant;
use tokio_util::sync::CancellationToken;

/// Builds the test server binary once, next to this test's own binary.
fn server_bin() -> PathBuf {
    static BIN: OnceLock<PathBuf> = OnceLock::new();
    BIN.get_or_init(|| {
        let exe = std::env::current_exe().unwrap();
        let profile_dir = exe.parent().unwrap().parent().unwrap().to_path_buf();
        let cargo = std::env::var("CARGO").unwrap_or_else(|_| "cargo".into());
        let mut build = std::process::Command::new(cargo);
        build.args(["build", "-q", "-p", "her-mock", "--bin", "her-mcp-server"]);
        if profile_dir.ends_with("release") {
            build.arg("--release");
        }
        let status = build.status().expect("cargo build");
        assert!(status.success(), "building her-mcp-server failed");
        profile_dir.join("her-mcp-server")
    })
    .clone()
}

/// The same HTTP client her uses (rustls with the platform verifier).
fn http_client() -> reqwest::Client {
    let catalog = her_llm::Catalog::builtin().unwrap();
    her_llm::ModelRegistry::new(catalog, her_llm::RegistryOptions::default())
        .http_client()
        .unwrap()
}

fn context(dir: &Path) -> ServerContext {
    ServerContext {
        oauth: None,
        http: Some(http_client()),
        roots: vec![dir.to_path_buf()],
        cwd: dir.to_path_buf(),
        project_root: None,
        on_status: None,
    }
}

async fn ready(server: &Arc<McpServer>) {
    server.connect();
    server.wait_settled(Instant::now() + Duration::from_secs(20)).await;
    let status = server.status();
    assert_eq!(status.state, McpServerState::Ready, "{status:?}");
}

fn options() -> CallOptions {
    CallOptions {
        cancel: CancellationToken::new(),
        progress: None,
    }
}

fn text(result: &her_mcp::CallToolResult) -> String {
    result
        .content
        .iter()
        .filter_map(|c| match c {
            Content::Text(t) => Some(t.as_str()),
            _ => None,
        })
        .collect::<Vec<_>>()
        .join("\n")
}

#[tokio::test]
async fn http_server_calls_progress_cancel_roots_and_list_changes() {
    let dir = tempfile::tempdir().unwrap();
    let log = dir.path().join("server.log");
    let http = her_mock::mcp::serve_http(
        "127.0.0.1:0",
        McpTestOptions {
            log: Some(log.clone()),
            page_size: 4,
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let config = McpServerConfig {
        name: "web".into(),
        url: Some(http.url()),
        ..Default::default()
    };
    let server = McpServer::new(config, context(dir.path()));
    ready(&server).await;
    let names: Vec<String> = server.tools().iter().map(|t| t.name.clone()).collect();
    assert!(names.contains(&"echo".to_string()) && names.contains(&"vault_reveal_code".to_string()), "{names:?}");
    assert!(!names.contains(&"ui_panel".to_string()), "tools only for user interfaces are filtered");
    assert_eq!(server.instructions().as_deref(), Some("Test server for her. Use echo to repeat text."));

    let echo = server.call_tool("echo", json!({"text": "hi"}), options()).await.unwrap();
    assert_eq!(text(&echo), "echo: hi");
    let add = server.call_tool("add", json!({"a": 2, "b": 3}), options()).await.unwrap();
    assert_eq!(add.structured_content, Some(json!({"sum": 5.0})));
    let failed = server.call_tool("fail", json!({"message": "nope"}), options()).await.unwrap();
    assert!(failed.is_error);
    let unknown = server.call_tool("missing", json!({}), options()).await.unwrap_err();
    assert!(matches!(unknown, McpError::UnknownTool(_)), "{unknown:?}");

    // Progress arrives over the SSE response.
    let seen = Arc::new(Mutex::new(Vec::<String>::new()));
    let sink = seen.clone();
    let progress = CallOptions {
        cancel: CancellationToken::new(),
        progress: Some(Arc::new(move |t: &str| sink.lock().push(t.to_string()))),
    };
    let slow = server.call_tool("slow", json!({"seconds": 0.6}), progress).await.unwrap();
    assert_eq!(text(&slow), "slept 0.6 s");
    assert!(seen.lock().iter().any(|t| t.contains("step 1 of 3")), "{:?}", seen.lock());

    // Cancelling tells the server.
    let cancel = CancellationToken::new();
    let trigger = cancel.clone();
    tokio::spawn(async move {
        tokio::time::sleep(Duration::from_millis(300)).await;
        trigger.cancel();
    });
    let cancelled = server
        .call_tool("slow", json!({"seconds": 10}), CallOptions { cancel, progress: None })
        .await
        .unwrap_err();
    assert!(matches!(cancelled, McpError::Cancelled));
    wait_for_log(&log, "cancelled").await;

    // The server asks for roots; the client answers with the workspace roots.
    let roots = server.call_tool("roots", json!({}), options()).await.unwrap();
    let dir_name = dir.path().file_name().unwrap().to_string_lossy().into_owned();
    assert!(text(&roots).starts_with("file://") && text(&roots).contains(&dir_name), "{}", text(&roots));

    // tools/list_changed: the tool list is fetched again.
    let generation = server.generation();
    server.call_tool("add_tool", json!({}), options()).await.unwrap();
    let deadline = Instant::now() + Duration::from_secs(10);
    while server.generation() == generation && Instant::now() < deadline {
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    assert!(server.offers("extra_echo"), "the new tool shows up");

    server.shutdown().await;
    assert_eq!(server.status().state, McpServerState::Closed);
    wait_for_log(&log, "session deleted").await;
}

async fn wait_for_log(log: &Path, needle: &str) {
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        let text = std::fs::read_to_string(log).unwrap_or_default();
        if text.contains(needle) {
            return;
        }
        assert!(Instant::now() < deadline, "'{needle}' not in the server log:\n{text}");
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
}

fn stdio_config(name: &str, args: &[&str]) -> McpServerConfig {
    McpServerConfig {
        name: name.into(),
        command: Some(server_bin().to_string_lossy().into_owned()),
        args: args.iter().map(|a| a.to_string()).collect(),
        ..Default::default()
    }
}

#[tokio::test]
async fn stdio_server_environment_timeouts_crash_and_cleanup() {
    let dir = tempfile::tempdir().unwrap();
    let log = dir.path().join("stdio.log");
    let log_arg = log.to_string_lossy().into_owned();
    let mut config = stdio_config("local", &["--log", &log_arg, "--stderr-lines", "5000"]);
    config.env_vars = vec!["CARGO_PKG_NAME".into()];
    config.env.insert("GREETING".into(), "hello ${CARGO_PKG_NAME}".into());
    config.tool_timeout_secs = Some(1);
    let server = McpServer::new(config, context(dir.path()));
    ready(&server).await;

    // Only the base set and what the configuration names reach the process.
    let env = |name: &'static str| {
        let server = server.clone();
        async move { text(&server.call_tool("env", json!({"name": name}), options()).await.unwrap()) }
    };
    assert_ne!(env("PATH").await, "(unset)");
    assert_eq!(env("CARGO_PKG_NAME").await, "her-mcp");
    assert_eq!(env("GREETING").await, "hello her-mcp");
    assert_eq!(env("CARGO_MANIFEST_DIR").await, "(unset)", "not passed through");

    // No progress within the timeout: the call ends and the server hears about it.
    let started = Instant::now();
    let timed_out = server.call_tool("slow", json!({"seconds": 5}), options()).await.unwrap_err();
    assert!(matches!(timed_out, McpError::Timeout(1)), "{timed_out:?}");
    assert!(started.elapsed() < Duration::from_secs(3));
    wait_for_log(&log, "cancelled").await;
    // With progress the same call outlives the timeout.
    let progress = CallOptions {
        cancel: CancellationToken::new(),
        progress: Some(Arc::new(|_: &str| {})),
    };
    let slow = server.call_tool("slow", json!({"seconds": 1.6}), progress).await.unwrap();
    assert_eq!(text(&slow), "slept 1.6 s");

    // A crash fails the server; the next call reconnects once.
    let _ = server.call_tool("crash", json!({}), options()).await;
    let deadline = Instant::now() + Duration::from_secs(10);
    while server.status().state != McpServerState::Failed && Instant::now() < deadline {
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    assert_eq!(server.status().state, McpServerState::Failed);
    let echo = server.call_tool("echo", json!({"text": "again"}), options()).await.unwrap();
    assert_eq!(text(&echo), "echo: again");

    // Shutting down takes the whole process group, grandchildren included.
    let pid: i32 = text(&server.call_tool("spawn_sleeper", json!({}), options()).await.unwrap())
        .parse()
        .unwrap();
    assert!(alive(pid));
    server.shutdown().await;
    let deadline = Instant::now() + Duration::from_secs(5);
    while alive(pid) && Instant::now() < deadline {
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    assert!(!alive(pid), "the grandchild survived the shutdown");
}

fn alive(pid: i32) -> bool {
    // SAFETY: signal 0 only checks whether the process exists.
    unsafe { libc::kill(pid, 0) == 0 }
}

#[tokio::test]
async fn configuration_errors_are_permanent_and_explained() {
    let dir = tempfile::tempdir().unwrap();
    let missing = McpServerConfig {
        name: "nope".into(),
        command: Some("definitely-not-a-command-her".into()),
        ..Default::default()
    };
    let server = McpServer::new(missing, context(dir.path()));
    server.connect();
    server.wait_settled(Instant::now() + Duration::from_secs(10)).await;
    let status = server.status();
    assert_eq!(status.state, McpServerState::Failed);
    assert!(status.error.unwrap().contains("command not found"));
    let err = server.call_tool("echo", json!({}), options()).await.unwrap_err();
    assert!(matches!(err, McpError::Unavailable(_)));

    let unset = McpServerConfig {
        name: "unset".into(),
        url: Some("http://127.0.0.1:1/mcp".into()),
        bearer_token_env_var: Some("HER_TEST_NO_SUCH_TOKEN".into()),
        ..Default::default()
    };
    let server = McpServer::new(unset, context(dir.path()));
    server.connect();
    server.wait_settled(Instant::now() + Duration::from_secs(10)).await;
    assert!(server.status().error.unwrap().contains("HER_TEST_NO_SUCH_TOKEN"));
}
