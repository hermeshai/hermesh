//! What `docs/cli/privacy.md` says about the network holds: with every request sent through a proxy
//! that writes down where it goes, nothing else shows up.

mod common;

use common::TestEnv;
use serde_json::json;
use std::io::Read;
use std::io::Write;
use std::net::TcpListener;
use std::sync::Arc;
use std::sync::Mutex;
use std::time::Duration;

/// Writes down where each request wants to go, and turns it away.
fn recording_proxy() -> (String, Arc<Mutex<Vec<String>>>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = format!("http://{}", listener.local_addr().unwrap());
    let seen: Arc<Mutex<Vec<String>>> = Default::default();
    let written = seen.clone();
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let mut stream = stream.unwrap();
            let mut buffer = [0; 1024];
            let read = stream.read(&mut buffer).unwrap_or(0);
            let text = String::from_utf8_lossy(&buffer[..read]);
            if let Some(target) = text.split_whitespace().nth(1) {
                written.lock().unwrap().push(target.to_string());
            }
            let _ = stream.write_all(b"HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n");
        }
    });
    (address, seen)
}

fn through(env: &TestEnv, proxy: &str, args: &[&str]) -> tokio::process::Command {
    let mut command = env.her(args);
    command.env("HTTPS_PROXY", proxy).env("HTTP_PROXY", proxy).env("ALL_PROXY", proxy).env("NO_PROXY", "127.0.0.1");
    command
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn scripts_and_looking_around_go_nowhere_but_the_model() {
    let env = TestEnv::new("manual", "").await;
    let (proxy, seen) = recording_proxy();
    for args in [
        &["exec", "say hi"][..],
        &["status"],
        &["doctor"],
        &["tools"],
        &["config"],
        &["models"],
        &["sessions"],
        &["web-search", "status"],
        &["mcp"],
        &["skills"],
    ] {
        let out = through(&env, &proxy, args).output().await.unwrap();
        assert!(out.status.success(), "{args:?}: {}", String::from_utf8_lossy(&out.stderr));
    }
    assert_eq!(*seen.lock().unwrap(), Vec::<String>::new());
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn an_editor_session_looks_for_no_updates() {
    let env = TestEnv::new("manual", "").await;
    let (proxy, seen) = recording_proxy();
    let mut command = through(&env, &proxy, &["acp"]);
    command.stdin(std::process::Stdio::piped()).stdout(std::process::Stdio::piped()).stderr(std::process::Stdio::null());
    let mut child = command.spawn().unwrap();
    let mut stdin = child.stdin.take().unwrap();
    let init = json!({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": 1, "clientCapabilities": {}, "clientInfo": {"name": "e2e", "version": "1"}}});
    tokio::io::AsyncWriteExt::write_all(&mut stdin, format!("{init}\n").as_bytes()).await.unwrap();
    tokio::time::sleep(Duration::from_secs(2)).await;
    drop(stdin);
    let _ = child.wait().await;
    assert_eq!(*seen.lock().unwrap(), Vec::<String>::new());
}

/// The resident service refreshes model lists in the background: the public catalog and the
/// providers it has keys for, nothing else.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_service_goes_only_where_the_list_says() {
    let env = TestEnv::new("manual", "").await;
    let (proxy, seen) = recording_proxy();
    let mut command = through(&env, &proxy, &["serve", "--stdio"]);
    command.stdin(std::process::Stdio::piped()).stdout(std::process::Stdio::null()).stderr(std::process::Stdio::null());
    let mut child = command.spawn().unwrap();
    tokio::time::sleep(Duration::from_secs(3)).await;
    drop(child.stdin.take());
    let _ = child.wait().await;
    for target in seen.lock().unwrap().iter() {
        assert!(target.starts_with("models.dev"), "{target} is not on the list");
    }
}
