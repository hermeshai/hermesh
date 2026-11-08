//! End-to-end: the user's own Chrome through the Hermesh extension (browser-design §8.4).
//! A real Chrome loads apps/chrome-extension, starts the real `her browser native-host`
//! through the manifest that `her browser extension install` wrote, and that host lends the
//! browser to a real `her serve`, whose session (driven by the mock model) opens a page and
//! clicks on it. Then the built-in browser is turned on while the session is open, and the
//! session uses both (connectors-entry-design §9.2, §9.4). Skipped when no Chromium-family
//! browser is installed. Another test stands in for the extension to check the bridge versions
//! (D-219) without a browser.

mod common;

use common::Demux;
use common::TestEnv;
use common::native_init;
use common::native_prompt;
use her_browser::launch::LaunchOptions;
use her_browser::launch::Launcher;
use her_browser::launch::ManagedLauncher;
use her_rpc::Client;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::time::Duration;
use tokio::io::AsyncWriteExt;
use tokio::net::TcpListener;
use tokio::net::UnixStream;

const EXTENSION_ID: &str = "igeolfcjdhlfopmimahmhleloehmfkdc";

async fn serve_page(body: &'static str) -> String {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    tokio::spawn(async move {
        while let Ok((mut stream, _)) = listener.accept().await {
            let mut buf = vec![0u8; 4096];
            let _ = tokio::io::AsyncReadExt::read(&mut stream, &mut buf).await;
            let response = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            );
            let _ = stream.write_all(response.as_bytes()).await;
        }
    });
    format!("http://127.0.0.1:{port}/")
}

async fn wait_for(what: &str, mut check: impl FnMut() -> bool) {
    for _ in 0..300 {
        if check() {
            return;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    panic!("timed out waiting for {what}");
}

fn logs(home: &Path) -> String {
    let mut text = String::new();
    for entry in std::fs::read_dir(home.join("logs")).into_iter().flatten().flatten() {
        text.push_str(&std::fs::read_to_string(entry.path()).unwrap_or_default());
    }
    text
}

/// The URLs of the browser's pages, as Chrome itself lists them.
async fn pages(chrome: &her_browser::cdp::Conn) -> Vec<String> {
    let targets = chrome.send(None, "Target.getTargets", json!({})).await.unwrap();
    targets["targetInfos"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|t| t["type"] == "page")
        .map(|t| t["url"].as_str().unwrap_or_default().to_string())
        .collect()
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_session_drives_the_users_chrome_through_the_extension() {
    let Some(chrome_exe) = her_browser::launch::find_executable() else {
        eprintln!("no Chromium-family browser installed; skipping");
        return;
    };
    // Only the user's own browser: there is no desktop app, so the built-in one stays off.
    let env = TestEnv::new("bypass-permissions", "[browser]\nextension = true\n").await;
    let profile = env.work().join("chrome-profile");
    let socket = env.home.path().join("run").join("her.sock");

    // The service, as the desktop app starts it.
    let mut service = env
        .her(&["serve", "--listen", &format!("unix://{}", socket.display())])
        .spawn()
        .unwrap();
    for _ in 0..1200 {
        if UnixStream::connect(&socket).await.is_ok() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(25)).await;
    }
    let stream = UnixStream::connect(&socket).await.expect("the service listens");
    let (read, write) = stream.into_split();
    let (client, incoming) = Client::connect(read, write);
    native_init(&client).await;
    let demux = Demux::spawn(incoming);

    // Without the extension, the browser tools say how to get it.
    let cwd = env.workspace("ext");
    let new = client.request("session/new", json!({"cwd": cwd})).await.unwrap();
    let session = new["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);
    let missing = native_prompt(&client, &mut events, &session, "[[mock:browser:http://127.0.0.1:9/]] open it", true).await;
    let results = serde_json::to_string(&missing.events).unwrap();
    assert!(results.contains("extension is not connected"), "{results}");

    // Register the host for this profile, the way a user does for their Chrome.
    let installed = env
        .her(&["browser", "extension", "install", "--user-data-dir", profile.to_str().unwrap()])
        .output()
        .await
        .unwrap();
    assert!(installed.status.success(), "{}", String::from_utf8_lossy(&installed.stderr));
    let manifest: Value =
        serde_json::from_str(&std::fs::read_to_string(profile.join("NativeMessagingHosts/com.hermesh.browser.json")).unwrap())
            .unwrap();
    assert_eq!(manifest["allowed_origins"], json!([format!("chrome-extension://{EXTENSION_ID}/")]));

    // Chrome, with the extension loaded (branded Chrome only lets CDP load one, behind a flag).
    let wrapper = env.work().join("chrome.sh");
    std::fs::write(
        &wrapper,
        format!("#!/bin/sh\nexec '{}' --enable-unsafe-extension-debugging \"$@\"\n", chrome_exe.display()),
    )
    .unwrap();
    std::fs::set_permissions(&wrapper, std::os::unix::fs::PermissionsExt::from_mode(0o755)).unwrap();
    let launched = ManagedLauncher
        .launch(&LaunchOptions {
            executable: Some(wrapper),
            user_data_dir: profile.clone(),
            headless: true,
        })
        .await
        .expect("Chrome starts");
    let chrome = launched.conn.clone();
    let _child = launched.child;
    let extension = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../apps/chrome-extension");
    let loaded = chrome
        .send(None, "Extensions.loadUnpacked", json!({"path": std::fs::canonicalize(extension).unwrap()}))
        .await
        .expect("the extension loads");
    assert_eq!(loaded["id"], EXTENSION_ID, "the key in manifest.json fixes the ID");

    // The extension starts the host, and the host reaches the service.
    let home = env.home.path().to_path_buf();
    wait_for("the extension host to connect", || logs(&home).contains("extension host connected")).await;

    // A tab the user had open stays out of the agent's reach.
    let mine = serve_page("<title>Mine</title><p>the user's own page</p>").await;
    chrome.send(None, "Target.createTarget", json!({"url": mine})).await.unwrap();

    // The session opens a page and clicks its button, in the user's Chrome.
    let page = serve_page(
        "<title>Lent</title><h1>In the user's Chrome</h1>\
         <button onclick=\"document.body.insertAdjacentHTML('beforeend','<p>clicked it</p>')\">Go</button>",
    )
    .await;
    let turn = native_prompt(&client, &mut events, &session, &format!("[[mock:browser:{page}]] open it"), true).await;
    assert_eq!(turn.end["type"], "task_completed", "{:?}", turn.events);
    assert!(turn.text.contains("clicked it"), "{}", turn.text);
    let opened = turn
        .events
        .iter()
        .find(|e| e["type"] == "tool_completed" && e["result"].to_string().contains("In the user's Chrome"))
        .unwrap_or_else(|| panic!("the navigation's result: {:?}", turn.events));
    let opened = opened["result"].to_string();
    assert!(opened.contains("the user's own browser"), "the model is told whose browser it is: {opened}");
    assert!(!opened.contains("the user's own page"), "{opened}");
    let urls = pages(&chrome).await;
    assert!(urls.contains(&page) && urls.contains(&mine), "{urls:?}");

    // The built-in browser, turned on while the session is open, is there from its next request:
    // a page opens in it, another in the user's Chrome, and the tabs of both are listed.
    let written = client
        .request(
            "config/write",
            json!({"layer": "user", "edits": [
                {"key_path": "browser.enabled", "value": true},
                {"key_path": "browser.headless", "value": true},
                {"key_path": "browser.executable", "value": chrome_exe.display().to_string()},
            ]}),
        )
        .await
        .expect("the configuration is written");
    assert_eq!(written["status"], "ok", "{written}");
    let builtin_page = serve_page("<title>Built in</title><h1>In the built-in browser</h1>").await;
    let yours_page = serve_page("<title>Yours</title><h1>Again in the user's Chrome</h1>").await;
    let mut both = None;
    // The service hands the new settings to the session a moment after the write: until then
    // the session has the user's browser alone, and the tabs say so.
    for _ in 0..50 {
        let turn = native_prompt(&client, &mut events, &session, &format!("[[mock:browsers:{builtin_page} {yours_page}]] both"), true).await;
        if turn.text.contains("in the built-in browser") {
            both = Some(turn);
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    let both = both.expect("the session takes up the new settings");
    assert_eq!(both.end["type"], "task_completed", "{:?}", both.events);
    let results: Vec<String> = both
        .events
        .iter()
        .filter(|e| e["type"] == "tool_completed")
        .map(|e| e["result"].to_string())
        .collect();
    assert!(results[0].contains("In the built-in browser"), "{results:?}");
    assert!(results[1].contains("Again in the user's Chrome"), "{results:?}");
    assert!(both.text.contains("in the built-in browser") && both.text.contains("in the user's own browser"), "{}", both.text);
    assert!(both.text.contains("tab y"), "the user's tabs are told apart: {}", both.text);
    let urls = pages(&chrome).await;
    assert!(urls.contains(&yours_page) && !urls.contains(&builtin_page), "the built-in page is not in the user's Chrome: {urls:?}");

    // The user's own browser made the default: a page opened without naming a browser goes there.
    let written = client
        .request("config/write", json!({"layer": "user", "edits": [{"key_path": "browser.default", "value": "yours"}]}))
        .await
        .expect("the default is written");
    assert_eq!(written["status"], "ok", "{written}");
    let default_page = serve_page("<title>Default</title><h1>Where the default sends it</h1>").await;
    let mut opened = false;
    for _ in 0..50 {
        let turn = native_prompt(&client, &mut events, &session, &format!("[[mock:default_browser:{default_page}]] default"), true).await;
        assert_eq!(turn.end["type"], "task_completed", "{:?}", turn.events);
        if pages(&chrome).await.contains(&default_page) {
            opened = true;
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert!(opened, "the page did not open in the user's Chrome: {:?}", pages(&chrome).await);

    // Closing the session lets the agent's tabs go: they are the user's, and stay open (§8.4.7).
    client.request("session/close", json!({"session_id": session})).await.unwrap();
    tokio::time::sleep(Duration::from_secs(1)).await;
    let urls = pages(&chrome).await;
    assert!(urls.contains(&default_page) && urls.contains(&mine), "{urls:?}");

    client.request("server/shutdown", json!({})).await.unwrap();
    let _ = tokio::time::timeout(Duration::from_secs(20), service.wait()).await;
}

/// One Native Messaging frame, as Chrome writes it.
fn frame(message: &Value) -> Vec<u8> {
    let json = message.to_string();
    let mut out = (json.len() as u32).to_ne_bytes().to_vec();
    out.extend_from_slice(json.as_bytes());
    out
}

/// The host's next message with a `status`, skipping CDP.
async fn next_status(out: &mut tokio::process::ChildStdout) -> Value {
    use tokio::io::AsyncReadExt as _;
    loop {
        let mut length = [0u8; 4];
        tokio::time::timeout(Duration::from_secs(20), out.read_exact(&mut length)).await.expect("the host answers").unwrap();
        let mut body = vec![0u8; u32::from_ne_bytes(length) as usize];
        out.read_exact(&mut body).await.unwrap();
        let message: Value = serde_json::from_slice(&body).unwrap();
        if message.get("status").is_some() {
            return message;
        }
    }
}

/// Stands in for the extension: starts the real host, says hello, and returns its first status.
async fn hello(env: &TestEnv, hello: Value) -> (tokio::process::Child, Value) {
    let mut host = env
        .her(&["browser", "native-host"])
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .spawn()
        .unwrap();
    host.stdin.as_mut().unwrap().write_all(&frame(&json!({"hello": hello}))).await.unwrap();
    let status = next_status(host.stdout.as_mut().unwrap()).await;
    (host, status)
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_extension_and_her_agree_on_a_bridge_version_or_say_which_to_update() {
    let env = TestEnv::new("manual", "").await;
    let socket = env.home.path().join("run").join("her.sock");
    let mut service = env
        .her(&["serve", "--listen", &format!("unix://{}", socket.display())])
        .spawn()
        .unwrap();
    for _ in 0..1200 {
        if UnixStream::connect(&socket).await.is_ok() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(25)).await;
    }
    let stream = UnixStream::connect(&socket).await.expect("the service listens");
    let (read, write) = stream.into_split();
    let (client, incoming) = Client::connect(read, write);
    native_init(&client).await;
    let _demux = Demux::spawn(incoming);
    let status = || async { client.request("browser/status", json!({})).await.unwrap() };

    // An extension newer than this her: held, not used, and the app is told to update.
    let future = her_browser::methods::BRIDGE + 1;
    let (mut host, said) =
        hello(&env, json!({"browser": "Google Chrome 999", "extension_version": "9.0.0", "bridge": {"min": future, "max": future}}))
            .await;
    assert_eq!(said["status"], "outdated", "{said}");
    assert_eq!(said["update"], "her", "{said}");
    let now = status().await;
    assert_eq!(now["extension"]["problem"], "update_her", "{now}");
    assert_eq!(now["extension"]["extension_version"], "9.0.0", "{now}");
    host.kill().await.unwrap();
    let _ = host.wait().await;
    let mut gone = Value::Null;
    for _ in 0..100 {
        gone = status().await;
        if gone.get("extension").is_none() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    assert!(gone.get("extension").is_none(), "its status goes with it: {gone}");

    // An extension from before bridge versions speaks version 1, which this her still drives.
    let (mut host, said) = hello(&env, json!({"browser": "Google Chrome 141", "extension_version": "0.1.0"})).await;
    assert_eq!(said, json!({"status": "connected", "bridge": 1}), "{said}");
    let now = status().await;
    assert!(now["extension"].get("problem").is_none(), "{now}");
    host.kill().await.unwrap();

    client.request("server/shutdown", json!({})).await.unwrap();
    let _ = tokio::time::timeout(Duration::from_secs(20), service.wait()).await;
}

// ---- The tabs' lifetime and the connection's (browser-design §8.4.7) ----

/// A service on `socket`, and a client that follows the browsers' status.
async fn serve(env: &TestEnv, socket: &Path, program: Option<&Path>) -> (tokio::process::Child, Client, Demux) {
    let (service, client, demux) = serve_unwatched(env, socket, program).await;
    client.request("browser/status", json!({})).await.unwrap();
    (service, client, demux)
}

/// The same, with a client that has not asked about the browsers.
async fn serve_unwatched(env: &TestEnv, socket: &Path, program: Option<&Path>) -> (tokio::process::Child, Client, Demux) {
    let listen = format!("unix://{}", socket.display());
    let mut command = with_program(env, program, &["serve", "--listen", &listen]);
    let service = command.spawn().unwrap();
    for _ in 0..1200 {
        if UnixStream::connect(socket).await.is_ok() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(25)).await;
    }
    let stream = UnixStream::connect(socket).await.expect("the service listens");
    let (read, write) = stream.into_split();
    let (client, incoming) = Client::connect(read, write);
    native_init(&client).await;
    (service, client, Demux::spawn(incoming))
}

/// `her args` from another copy of the program, as an update leaves one, or the usual one.
fn with_program(env: &TestEnv, program: Option<&Path>, args: &[&str]) -> tokio::process::Command {
    let usual = env.her(args);
    let Some(program) = program else { return usual };
    let mut command = tokio::process::Command::new(program);
    command.args(args);
    for (key, value) in usual.as_std().get_envs() {
        match value {
            Some(value) => command.env(key, value),
            None => command.env_remove(key),
        };
    }
    command.current_dir(env.work());
    command.kill_on_drop(true);
    command
}

/// A real Chrome on `profile` with the extension loaded from the repository.
async fn chrome_with_extension(env: &TestEnv, profile: &Path, chrome_exe: &Path) -> (std::sync::Arc<her_browser::cdp::Conn>, tokio::process::Child) {
    chrome_with_folder(env, profile, chrome_exe, &Path::new(env!("CARGO_MANIFEST_DIR")).join("../../apps/chrome-extension")).await
}

/// The same with the extension loaded from `folder`.
async fn chrome_with_folder(env: &TestEnv, profile: &Path, chrome_exe: &Path, folder: &Path) -> (std::sync::Arc<her_browser::cdp::Conn>, tokio::process::Child) {
    let wrapper = env.work().join("chrome-wrapped.sh");
    std::fs::write(&wrapper, format!("#!/bin/sh\nexec '{}' --enable-unsafe-extension-debugging \"$@\"\n", chrome_exe.display())).unwrap();
    std::fs::set_permissions(&wrapper, std::os::unix::fs::PermissionsExt::from_mode(0o755)).unwrap();
    let launched = ManagedLauncher
        .launch(&LaunchOptions {
            executable: Some(wrapper),
            user_data_dir: profile.to_path_buf(),
            headless: true,
        })
        .await
        .expect("Chrome starts");
    let loaded = launched
        .conn
        .send(None, "Extensions.loadUnpacked", json!({"path": std::fs::canonicalize(folder).unwrap()}))
        .await
        .expect("the extension loads");
    assert_eq!(loaded["id"], EXTENSION_ID);
    (launched.conn.clone(), launched.child.expect("a child process"))
}

/// Runs `expression` in a target of the extension: its worker, or a page of it.
async fn evaluate_in(chrome: &her_browser::cdp::Conn, target: &str, expression: &str) -> Value {
    let attached = chrome.send(None, "Target.attachToTarget", json!({"targetId": target, "flatten": true})).await.unwrap();
    let session = attached["sessionId"].as_str().unwrap().to_string();
    let result = chrome
        .send(Some(&session), "Runtime.evaluate", json!({"expression": expression, "awaitPromise": true, "returnByValue": true}))
        .await
        .unwrap();
    let _ = chrome.send(None, "Target.detachFromTarget", json!({"sessionId": session})).await;
    assert!(result.get("exceptionDetails").is_none(), "{expression}: {result}");
    result["result"]["value"].clone()
}

async fn worker(chrome: &her_browser::cdp::Conn, expression: &str) -> Value {
    // Right after the first attach to a worker, an evaluation can run before `chrome` is there.
    for _ in 0..100 {
        if worker_now(chrome, "typeof chrome").await == Some(json!("object")) {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    let mut target = None;
    for _ in 0..100 {
        let targets = chrome.send(None, "Target.getTargets", json!({})).await.unwrap();
        target = targets["targetInfos"].as_array().unwrap().iter().find(|t| t["type"] == "service_worker" && t["url"].as_str().unwrap_or_default().contains(EXTENSION_ID)).cloned();
        if target.is_some() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    evaluate_in(chrome, target.expect("the extension's worker")["targetId"].as_str().unwrap(), expression).await
}

/// Runs `expression` in a target that may be coming or going: nothing until it answers.
async fn evaluate_now(chrome: &her_browser::cdp::Conn, target: &Value, expression: &str) -> Option<Value> {
    let attached = chrome.send(None, "Target.attachToTarget", json!({"targetId": target, "flatten": true})).await.ok()?;
    let session = attached["sessionId"].as_str()?.to_string();
    let result = chrome.send(Some(&session), "Runtime.evaluate", json!({"expression": expression, "awaitPromise": true, "returnByValue": true})).await.ok();
    let _ = chrome.send(None, "Target.detachFromTarget", json!({"sessionId": session})).await;
    let result = result?;
    if result.get("exceptionDetails").is_some() {
        return None;
    }
    Some(result["result"]["value"].clone())
}

/// The same in the worker, which may be restarting.
async fn worker_now(chrome: &her_browser::cdp::Conn, expression: &str) -> Option<Value> {
    let targets = chrome.send(None, "Target.getTargets", json!({})).await.ok()?;
    let target = targets["targetInfos"].as_array()?.iter().find(|t| t["type"] == "service_worker" && t["url"].as_str().unwrap_or_default().contains(EXTENSION_ID))?.clone();
    evaluate_now(chrome, &target["targetId"], expression).await
}

/// Developer mode on, as loading an extension from a folder has it for a user: without it, Chrome
/// disables an extension from a folder when it reloads.
async fn developer_mode(chrome: &her_browser::cdp::Conn) {
    let page = chrome.send(None, "Target.createTarget", json!({"url": "chrome://extensions", "background": true})).await.unwrap();
    let mut on = None;
    for _ in 0..100 {
        on = evaluate_now(chrome, &page["targetId"], "chrome.developerPrivate.updateProfileConfiguration({inDeveloperMode: true}).then(() => chrome.developerPrivate.getProfileConfiguration()).then(c => c.inDeveloperMode)").await;
        if on == Some(json!(true)) {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert_eq!(on, Some(json!(true)), "developer mode");
    let _ = chrome.send(None, "Target.closeTarget", json!({"targetId": page["targetId"]})).await;
}

/// A copy of the extension's files, as the app keeps them in its own folder.
fn copy_extension(env: &TestEnv, name: &str) -> std::path::PathBuf {
    fn copy(from: &Path, to: &Path) {
        std::fs::create_dir_all(to).unwrap();
        for entry in std::fs::read_dir(from).unwrap() {
            let entry = entry.unwrap();
            let target = to.join(entry.file_name());
            if entry.file_type().unwrap().is_dir() {
                copy(&entry.path(), &target);
            } else {
                std::fs::copy(entry.path(), &target).unwrap();
            }
        }
    }
    let from = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../apps/chrome-extension");
    let folder = env.work().join(name);
    std::fs::create_dir_all(&folder).unwrap();
    std::fs::copy(from.join("manifest.json"), folder.join("manifest.json")).unwrap();
    for part in ["_locales", "icons", "src"] {
        copy(&from.join(part), &folder.join(part));
    }
    folder
}

/// Sends a prompt without waiting for its turn.
async fn start_prompt(client: &Client, session: &str, text: &str) {
    let op = json!({"session_id": session, "op": {"type": "prompt", "input": [{"type": "text", "text": text}]}});
    client.request("session/op", op).await.unwrap();
}

/// What the model said until the task ended.
async fn said_until_done(events: &mut tokio::sync::mpsc::UnboundedReceiver<her_rpc::Incoming>) -> String {
    let mut said = String::new();
    loop {
        let incoming = common::recv(events).await;
        let event = incoming.params()["event"].clone();
        if event["type"] == "message_delta" {
            said.push_str(event["delta"]["text"].as_str().unwrap_or(""));
        }
        if event["type"] == "task_completed" {
            return said;
        }
        assert_ne!(event["type"], "task_failed", "{event}");
    }
}

/// Waits until the browsers' status passes `check`, and returns it.
async fn status_until(client: &Client, what: &str, check: impl Fn(&Value) -> bool) -> Value {
    let mut now = Value::Null;
    for _ in 0..300 {
        now = client.request("browser/status", json!({})).await.unwrap();
        if check(&now) {
            return now;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    panic!("timed out waiting for {what}: {now}");
}

fn tool_results(turn: &common::NativeTurn) -> String {
    turn.events.iter().filter(|e| e["type"] == "tool_completed").map(|e| e["result"].to_string()).collect::<Vec<_>>().join("\n")
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_agent_lets_go_of_its_tabs_and_finds_them_again() {
    let Some(chrome_exe) = her_browser::launch::find_executable() else {
        eprintln!("no Chromium-family browser installed; skipping");
        return;
    };
    let env = TestEnv::new("bypass-permissions", "[browser]\nextension = true\n").await;
    let socket = env.home.path().join("run").join("her.sock");
    let profile = env.work().join("chrome-profile");
    let (mut service, client, demux) = serve(&env, &socket, None).await;
    let installed = env.her(&["browser", "extension", "install", "--user-data-dir", profile.to_str().unwrap()]).output().await.unwrap();
    assert!(installed.status.success(), "{}", String::from_utf8_lossy(&installed.stderr));
    let (chrome, chrome_process) = chrome_with_extension(&env, &profile, &chrome_exe).await;
    let now = status_until(&client, "the extension", |s| s["extension"]["in_use"] == true).await;
    assert!(now["extension"]["browser"].as_str().unwrap().contains("Chrome"), "{now}");
    assert!(now["extension"]["instance"].is_string(), "{now}");

    let cwd = env.workspace("ext");
    let new = client.request("session/new", json!({"cwd": cwd})).await.unwrap();
    let session = new["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);
    let page = serve_page("<title>Kept</title><h1>Kept in the user's Chrome</h1>").await;
    let attached_now = |chrome: std::sync::Arc<her_browser::cdp::Conn>, url: String| async move {
        let targets = chrome.send(None, "Target.getTargets", json!({})).await.unwrap();
        targets["targetInfos"].as_array().unwrap().iter().find(|t| t["url"] == url.as_str()).map(|t| t["attached"] == true)
    };

    // A task opens a page. When it ends, the tab stays, with the session, and the debugger lets
    // go, so Chrome's bar goes.
    let turn = native_prompt(&client, &mut events, &session, &format!("[[mock:yours:{page}]] open"), true).await;
    assert_eq!(turn.end["type"], "task_completed", "{:?}", turn.events);
    assert!(turn.text.contains("Kept in the user's Chrome"), "{}", turn.text);
    let now = status_until(&client, "the tab, let go", |s| s["tabs"][&session][0]["url"] == page.as_str() && s["tabs"][&session][0].get("attached").is_none()).await;
    assert_eq!(now["tabs"][&session][0]["title"], "Kept", "{now}");
    for _ in 0..50 {
        if attached_now(chrome.clone(), page.clone()).await == Some(false) {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert_eq!(attached_now(chrome.clone(), page.clone()).await, Some(false), "the debugger let go");

    // The next task finds it, under the same number, and is told that refs are gone.
    let turn = native_prompt(&client, &mut events, &session, "[[mock:yours_read:y1]] read", true).await;
    let results = tool_results(&turn);
    assert!(results.contains("Kept in the user's Chrome"), "{results}");
    assert!(results.contains("still open"), "{results}");

    // The host dies under the session: the extension starts another by itself, and the session
    // goes on with its tab.
    let hosts = std::process::Command::new("/usr/bin/pgrep").args(["-P", &chrome_process.id().unwrap().to_string(), "-f", "native-host"]).output().unwrap();
    let host = String::from_utf8_lossy(&hosts.stdout).lines().next().expect("the extension's host runs").to_string();
    let died = std::time::Instant::now();
    std::process::Command::new("/bin/kill").args(["-9", &host]).status().unwrap();
    status_until(&client, "the extension gone", |s| s.get("extension").is_none()).await;
    status_until(&client, "the extension back", |s| s["extension"]["in_use"] == true && s["tabs"][&session][0]["url"] == page.as_str()).await;
    eprintln!("the extension was back {} ms after its host died", died.elapsed().as_millis());
    assert!(died.elapsed() < Duration::from_secs(10), "{:?}", died.elapsed());
    let turn = native_prompt(&client, &mut events, &session, "[[mock:yours_read:y1]] read", true).await;
    assert!(tool_results(&turn).contains("Kept in the user's Chrome"), "{}", tool_results(&turn));

    // The app asks to see the tab: it comes to the front in its window.
    client.request("browser/reveal", json!({"session_id": session})).await.unwrap();
    let active = format!("chrome.tabs.query({{url: {}}}).then(t => t[0].active)", json!(page));
    let mut in_front = Value::Null;
    for _ in 0..50 {
        in_front = worker(&chrome, &active).await;
        if in_front == true {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert_eq!(in_front, true);

    // The user closes Chrome's debugging bar: the agent keeps its hands off until allowed again.
    let tab = worker(&chrome, &format!("chrome.tabs.query({{url: {}}}).then(t => t[0].id)", json!(page))).await;
    worker(&chrome, &format!("chrome.debugger.onDetach.dispatch({{tabId: {tab}}}, 'canceled_by_user')")).await;
    status_until(&client, "the session revoked", |s| s["revoked"][0] == session.as_str()).await;
    let turn = native_prompt(&client, &mut events, &session, "[[mock:yours_read:y1]] read", true).await;
    assert!(tool_results(&turn).contains("took back control"), "{}", tool_results(&turn));
    client.request("browser/allow", json!({"session_id": session})).await.unwrap();
    let turn = native_prompt(&client, &mut events, &session, "[[mock:yours_read:y1]] read", true).await;
    assert!(tool_results(&turn).contains("Kept in the user's Chrome"), "{}", tool_results(&turn));

    // The extension's page: a task hands the page to the user, who says they are done there.
    let origin = format!("chrome-extension://{EXTENSION_ID}");
    let opened = chrome.send(None, "Target.createTarget", json!({"url": format!("{origin}/src/popup.html"), "background": true})).await.unwrap();
    let popup = opened["targetId"].as_str().unwrap().to_string();
    // A new target starts on a blank document: its page comes next.
    tokio::time::sleep(Duration::from_secs(1)).await;
    let op = json!({"session_id": session, "op": {"type": "prompt", "input": [{"type": "text", "text": "[[mock:handoff:sign in]] your turn"}]}});
    client.request("session/op", op).await.unwrap();
    loop {
        let incoming = common::recv(&mut events).await;
        let event = incoming.params()["event"].clone();
        if event["type"] == "approval_requested" {
            assert_eq!(event["own_browser"], true, "{event}");
            assert_eq!(event["summary"], "sign in", "{event}");
            assert_eq!(event["name"], "call_tool", "as a model runs the browser's tools: {event}");
            break;
        }
    }
    let waits = status_until(&client, "the session told to the extension", |_| true).await;
    assert!(waits["tabs"][&session].is_array(), "{waits}");
    let mut state = Value::Null;
    for _ in 0..50 {
        state = evaluate_in(&chrome, &popup, "chrome.runtime.sendMessage({get: 'state'})").await;
        if state["sessions"][0]["handoff"] == "sign in" {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert_eq!(state["sessions"][0]["handoff"], "sign in", "{state}");
    assert_eq!(state["sessions"][0]["state"], "awaiting_approval", "{state}");
    assert_eq!(state["host"], "connected", "{state}");
    // The page was opened before the task asked: it says so by itself, and its button does it.
    let mut shown = Value::Null;
    for _ in 0..50 {
        shown = evaluate_in(
            &chrome,
            &popup,
            "({head: document.querySelector('.head').textContent === chrome.i18n.getMessage('waitingOne'), what: document.querySelector('.line.whole')?.textContent, done: document.querySelector('button.primary')?.textContent === chrome.i18n.getMessage('done')})",
        )
        .await;
        if shown == json!({"head": true, "what": "sign in", "done": true}) {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert_eq!(shown, json!({"head": true, "what": "sign in", "done": true}));
    evaluate_in(&chrome, &popup, "document.querySelector('button.primary').click()").await;
    let mut said = String::new();
    loop {
        let incoming = common::recv(&mut events).await;
        let event = incoming.params()["event"].clone();
        if event["type"] == "message_delta" {
            said.push_str(event["delta"]["text"].as_str().unwrap_or(""));
        }
        if event["type"] == "task_completed" {
            break;
        }
        assert_ne!(event["type"], "task_failed", "{event}");
    }
    assert!(said.contains("handoff done") && said.contains("The user says they are done") && said.contains("Kept in the user's Chrome"), "{said}");

    // Another connection follows: the user offers a tab of their own from the extension, and the
    // app gives it to the session.
    let other = UnixStream::connect(&socket).await.unwrap();
    let (read, write) = other.into_split();
    let (app, mut requests) = Client::connect(read, write);
    native_init(&app).await;
    app.request("browser/status", json!({})).await.unwrap();
    let mine = serve_page("<title>Mine</title><h1>A page of the user's own</h1>").await;
    let created = chrome.send(None, "Target.createTarget", json!({"url": mine})).await.unwrap();
    chrome.send(None, "Target.activateTarget", json!({"targetId": created["targetId"]})).await.unwrap();
    tokio::time::sleep(Duration::from_millis(500)).await;
    evaluate_in(&chrome, &popup, "chrome.runtime.sendMessage({do: 'offer'})").await;
    let offered = loop {
        let incoming = common::next(&mut requests).await;
        if incoming.method() == "browser/request" {
            break incoming.params().clone();
        }
    };
    assert_eq!(offered["kind"], "tab_offered", "{offered}");
    assert_eq!(offered["title"], "Mine", "{offered}");
    client.request("browser/assign_tab", json!({"target_id": offered["target_id"], "session_id": session})).await.unwrap();
    status_until(&client, "the offered tab", |s| s["tabs"][&session].as_array().is_some_and(|t| t.iter().any(|t| t["title"] == "Mine"))).await;
    let turn = native_prompt(&client, &mut events, &session, "[[mock:yours_tabs:]] list", true).await;
    let listed = tool_results(&turn);
    assert!(listed.contains("Mine") && listed.contains("Kept"), "{listed}");
    assert!(listed.contains("tab y1") && listed.contains("tab y2"), "both, numbered: {listed}");

    // Back to the conversation, from the extension.
    evaluate_in(&chrome, &popup, &format!("chrome.runtime.sendMessage({{do: 'back', session: {}}})", json!(session))).await;
    let back = loop {
        let incoming = common::next(&mut requests).await;
        if incoming.method() == "browser/request" {
            break incoming.params().clone();
        }
    };
    assert_eq!(back, json!({"kind": "open_session", "session_id": session}));

    // The app closes them all.
    client.request("browser/close_tabs", json!({"session_id": session})).await.unwrap();
    status_until(&client, "no tabs", |s| s["tabs"].get(&session).is_none()).await;
    let urls = pages(&chrome).await;
    assert!(!urls.contains(&page) && !urls.contains(&mine), "{urls:?}");

    // The session is deleted: its tabs stay open, and are the user's now, out of its group.
    native_prompt(&client, &mut events, &session, &format!("[[mock:yours:{page}]] open"), true).await;
    status_until(&client, "the tab, let go", |s| s["tabs"][&session][0]["url"] == page.as_str() && s["tabs"][&session][0].get("attached").is_none()).await;
    let group = format!("chrome.tabs.query({{url: {}}}).then(t => t[0].groupId)", json!(page));
    assert_ne!(worker(&chrome, &group).await, -1, "in a group while the session has it");
    client.request("session/delete", json!({"session_id": session})).await.unwrap();
    status_until(&client, "no tabs", |s| s["tabs"].get(&session).is_none()).await;
    let mut grouped = Value::Null;
    for _ in 0..50 {
        grouped = worker(&chrome, &group).await;
        if grouped == -1 {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert_eq!(grouped, -1, "out of the group");
    assert!(pages(&chrome).await.contains(&page), "the tab stays");

    client.request("server/shutdown", json!({})).await.unwrap();
    let _ = tokio::time::timeout(Duration::from_secs(20), service.wait()).await;
}

/// What the extension's page says, read until it says `title`; and what its one button is.
async fn page_says(chrome: &her_browser::cdp::Conn, popup: &str, title: &str, within: Duration) -> Value {
    let expression = format!(
        "({{head: document.querySelector('.head')?.textContent === chrome.i18n.getMessage({title:?}), button: document.querySelector('button.primary')?.textContent ?? null}})"
    );
    let deadline = std::time::Instant::now() + within;
    loop {
        let shown = evaluate_in(chrome, popup, &expression).await;
        if shown["head"] == true {
            return shown["button"].clone();
        }
        assert!(std::time::Instant::now() < deadline, "the page never said {title}: {shown}");
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
}

/// The toolbar's "!" is only worth it if the page explains it: a host that cannot start is quiet at
/// first, then asks for the user with one button, and that button brings the connection back.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_extensions_page_says_what_is_wrong_and_what_to_do() {
    let Some(chrome_exe) = her_browser::launch::find_executable() else {
        eprintln!("no Chromium-family browser installed; skipping");
        return;
    };
    let env = TestEnv::new("bypass-permissions", "[browser]\nextension = true\n").await;
    let profile = env.work().join("chrome-profile");
    let installed = env
        .her(&["browser", "extension", "install", "--user-data-dir", profile.to_str().unwrap()])
        .output()
        .await
        .unwrap();
    assert!(installed.status.success(), "{}", String::from_utf8_lossy(&installed.stderr));
    let manifest: Value =
        serde_json::from_str(&std::fs::read_to_string(profile.join("NativeMessagingHosts/com.hermesh.browser.json")).unwrap()).unwrap();
    let script = std::path::PathBuf::from(manifest["path"].as_str().unwrap());
    let working = std::fs::read(&script).unwrap();

    // No service runs: the host starts and says Hermesh is not open, which is not a problem.
    let (chrome, chrome_process) = chrome_with_extension(&env, &profile, &chrome_exe).await;
    let opened = chrome.send(None, "Target.createTarget", json!({"url": format!("chrome-extension://{EXTENSION_ID}/src/popup.html"), "background": true})).await.unwrap();
    let popup = opened["targetId"].as_str().unwrap().to_string();
    tokio::time::sleep(Duration::from_secs(1)).await;
    let button = page_says(&chrome, &popup, "statusNotOpen", Duration::from_secs(20)).await;
    assert_eq!(evaluate_in(&chrome, &popup, "chrome.i18n.getMessage('openApp')").await, button);
    assert_eq!(worker(&chrome, "chrome.action.getBadgeText({})").await, "", "nothing on the icon");

    // The host cannot start any more, and the one running dies.
    std::fs::write(&script, "#!/bin/sh\nexit 1\n").unwrap();
    let hosts = std::process::Command::new("/usr/bin/pgrep").args(["-P", &chrome_process.id().unwrap().to_string(), "-f", "native-host"]).output().unwrap();
    let host = String::from_utf8_lossy(&hosts.stdout).lines().next().expect("the extension's host runs").to_string();
    std::process::Command::new("/bin/kill").args(["-9", &host]).status().unwrap();

    // Quiet while reconnecting would still be normal.
    assert_eq!(page_says(&chrome, &popup, "statusConnecting", Duration::from_secs(5)).await, Value::Null);
    assert_eq!(worker(&chrome, "chrome.action.getBadgeText({})").await, "", "too early for the icon");

    // Past that, it asks: on the icon, and on the page with one button.
    let button = page_says(&chrome, &popup, "statusStuck", Duration::from_secs(15)).await;
    assert_eq!(evaluate_in(&chrome, &popup, "chrome.i18n.getMessage('retry')").await, button);
    assert_eq!(worker(&chrome, "chrome.action.getBadgeText({})").await, "!");

    // Repaired, and tried again: back to normal, with the icon clear.
    std::fs::write(&script, &working).unwrap();
    evaluate_in(&chrome, &popup, "document.querySelector('button.primary').click()").await;
    page_says(&chrome, &popup, "statusNotOpen", Duration::from_secs(20)).await;
    assert_eq!(worker(&chrome, "chrome.action.getBadgeText({})").await, "", "the icon clears");
}

/// The bug behind "the extension is not connected": the app was replaced while Chrome kept a host
/// of the old program, which the service of the new one refuses. The old host exits, the extension
/// starts the current one, and nobody has to do anything.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn replacing_the_program_restarts_the_host_by_itself() {
    let Some(chrome_exe) = her_browser::launch::find_executable() else {
        eprintln!("no Chromium-family browser installed; skipping");
        return;
    };
    let env = TestEnv::new("bypass-permissions", "[browser]\nextension = true\n").await;
    let socket = env.home.path().join("run").join("her.sock");
    let profile = env.work().join("chrome-profile");
    let usual = env.her(&[]).as_std().get_program().to_owned();
    let copy = |name: &str| {
        let dir = env.work().join(name);
        std::fs::create_dir_all(&dir).unwrap();
        let program = dir.join("her");
        std::fs::copy(&usual, &program).unwrap();
        program
    };
    let (old, new) = (copy("old"), copy("new"));
    let (mut service, client, demux) = serve(&env, &socket, Some(&old)).await;
    let install = |program: std::path::PathBuf| {
        let args = ["browser", "extension", "install", "--user-data-dir", profile.to_str().unwrap()];
        let mut command = with_program(&env, Some(&program), &args);
        async move {
            let installed = command.output().await.unwrap();
            assert!(installed.status.success(), "{}", String::from_utf8_lossy(&installed.stderr));
        }
    };
    install(old.clone()).await;
    let (chrome, _chrome_process) = chrome_with_extension(&env, &profile, &chrome_exe).await;
    status_until(&client, "the extension", |s| s["extension"]["in_use"] == true).await;
    let cwd = env.workspace("ext");
    let created = client.request("session/new", json!({"cwd": cwd})).await.unwrap();
    let session = created["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);
    let page = serve_page("<title>Through it</title><h1>Still here after the update</h1>").await;
    let turn = native_prompt(&client, &mut events, &session, &format!("[[mock:yours:{page}]] open"), true).await;
    assert!(turn.text.contains("Still here after the update"), "{}", turn.text);

    // The update: the old service stops, the launcher points at the new program, and the new
    // program's service starts. Chrome keeps the old host running meanwhile.
    client.request("server/shutdown", json!({})).await.unwrap();
    let _ = tokio::time::timeout(Duration::from_secs(20), service.wait()).await;
    install(new.clone()).await;
    let updated = std::time::Instant::now();
    let (mut service, client, demux) = serve(&env, &socket, Some(&new)).await;

    // Nobody touches anything: it reconnects, and through the new program.
    let now = status_until(&client, "the extension back", |s| s["extension"]["in_use"] == true && s["tabs"][&session][0]["url"] == page.as_str()).await;
    eprintln!("the extension was back {} ms after the update", updated.elapsed().as_millis());
    assert!(updated.elapsed() < Duration::from_secs(20), "{:?}", updated.elapsed());
    assert!(now.get("refused").is_none(), "cleared once a host connects: {now}");
    let log = logs(env.home.path());
    assert_eq!(log.matches("refused an extension host").count(), 1, "the old host was refused, once: {log}");
    assert!(log.contains("exiting for the extension to start the current one"), "{log}");

    // The session comes back and finds its tab.
    client.request("session/resume", json!({"session_id": session})).await.unwrap();
    let mut events = demux.subscribe(&session);
    let turn = native_prompt(&client, &mut events, &session, "[[mock:yours_read:y1]] read", true).await;
    assert!(tool_results(&turn).contains("Still here after the update"), "{}", tool_results(&turn));
    assert!(pages(&chrome).await.contains(&page));

    client.request("server/shutdown", json!({})).await.unwrap();
    let _ = tokio::time::timeout(Duration::from_secs(20), service.wait()).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_refused_host_exits_and_several_extensions_stay_apart() {
    let env = TestEnv::new("manual", "").await;
    let socket = env.home.path().join("run").join("her.sock");
    let (mut service, client, _demux) = serve(&env, &socket, None).await;
    let hello_as = |instance: &str, account: &str| json!({"browser": "Google Chrome 141", "extension_version": "0.2.0", "bridge": {"min": 1, "max": 2}, "instance": instance, "account": account});

    // A host of another program: refused, it exits, and the status says why until one connects.
    let usual = env.her(&[]).as_std().get_program().to_owned();
    let dir = env.work().join("old");
    std::fs::create_dir_all(&dir).unwrap();
    let old = dir.join("her");
    std::fs::copy(&usual, &old).unwrap();
    let mut stale = with_program(&env, Some(&old), &["browser", "native-host"])
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .spawn()
        .unwrap();
    stale.stdin.as_mut().unwrap().write_all(&frame(&json!({"hello": hello_as("stale", "")}))).await.unwrap();
    // Not `wait`, which closes stdin: the host must exit by itself, with Chrome still attached.
    let mut code = None;
    for _ in 0..1200 {
        code = stale.try_wait().unwrap();
        if code.is_some() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert_eq!(code.expect("the host exits").code(), Some(3), "the code of a refused host");
    let now = status_until(&client, "the reason", |s| s["refused"].is_string()).await;
    assert!(now["refused"].as_str().unwrap().contains("not her's extension host"), "{now}");

    // Two browsers or profiles: the first stays in use, the second never moves the agent.
    let (mut first, said) = hello(&env, hello_as("first", "me@work.example")).await;
    assert_eq!(said["status"], "connected", "{said}");
    let now = status_until(&client, "one extension", |s| s["extension"]["instance"] == "first").await;
    assert!(now.get("refused").is_none(), "{now}");
    assert_eq!(now["extension"]["account"], "me@work.example", "{now}");
    let (mut second, said) = hello(&env, hello_as("second", "me@home.example")).await;
    assert_eq!(said["status"], "connected", "{said}");
    let now = status_until(&client, "two extensions", |s| s["extensions"].as_array().is_some_and(|e| e.len() == 2)).await;
    assert_eq!(now["extension"]["instance"], "first", "{now}");
    assert_eq!(now["extensions"][1]["instance"], "second", "{now}");
    assert!(now["extensions"][1].get("in_use").is_none(), "{now}");

    // The user chooses the second, and it is kept. While it is away, the agent does not fall back
    // to the other account: nothing is in use, and the first is still listed.
    client.request("browser/use_extension", json!({"instance": "second"})).await.unwrap();
    let now = status_until(&client, "the second in use", |s| s["extension"]["instance"] == "second").await;
    assert_eq!(now["extensions"][1]["in_use"], true, "{now}");
    second.kill().await.unwrap();
    let _ = second.wait().await;
    let now = status_until(&client, "the second gone", |s| s.get("extension").is_none()).await;
    assert_eq!(now["extensions"][0]["instance"], "first", "{now}");
    assert!(env.home.path().join("browser/extension-in-use").exists());
    client.request("browser/use_extension", json!({"instance": "first"})).await.unwrap();
    status_until(&client, "the first in use", |s| s["extension"]["instance"] == "first").await;
    first.kill().await.unwrap();
    let _ = first.wait().await;

    client.request("server/shutdown", json!({})).await.unwrap();
    let _ = tokio::time::timeout(Duration::from_secs(20), service.wait()).await;
}

/// A host only waits for the service: it does not keep it running after the app quit.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn an_extension_host_does_not_keep_the_service_running() {
    let env = TestEnv::new("manual", "").await;
    let socket = env.home.path().join("run").join("her.sock");
    let mut service = env.her(&["serve", "--listen", &format!("unix://{}", socket.display()), "--idle-exit", "1"]).spawn().unwrap();
    for _ in 0..1200 {
        if UnixStream::connect(&socket).await.is_ok() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(25)).await;
    }
    let (mut host, said) = hello(&env, json!({"browser": "Google Chrome 141", "extension_version": "0.2.0", "bridge": {"min": 1, "max": 2}})).await;
    assert_eq!(said["status"], "connected", "{said}");
    let exited = tokio::time::timeout(Duration::from_secs(30), service.wait()).await;
    assert!(exited.is_ok(), "the service stays up for a host");
    // The host says so, and waits for the next one.
    let said = next_status(host.stdout.as_mut().unwrap()).await;
    assert_eq!(said["status"], "waiting", "{said}");
    host.kill().await.unwrap();
}

/// The service stops under a connected browser, without warning: the host waits for the next one,
/// and the session finds its tab there.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_service_stopping_leaves_the_tabs_with_their_session() {
    let Some(chrome_exe) = her_browser::launch::find_executable() else {
        eprintln!("no Chromium-family browser installed; skipping");
        return;
    };
    let env = TestEnv::new("bypass-permissions", "[browser]\nextension = true\n").await;
    let socket = env.home.path().join("run").join("her.sock");
    let profile = env.work().join("chrome-profile");
    let (mut service, client, demux) = serve(&env, &socket, None).await;
    let installed = env.her(&["browser", "extension", "install", "--user-data-dir", profile.to_str().unwrap()]).output().await.unwrap();
    assert!(installed.status.success(), "{}", String::from_utf8_lossy(&installed.stderr));
    let (chrome, _chrome_process) = chrome_with_extension(&env, &profile, &chrome_exe).await;
    status_until(&client, "the extension", |s| s["extension"]["in_use"] == true).await;
    let cwd = env.workspace("ext");
    let created = client.request("session/new", json!({"cwd": cwd})).await.unwrap();
    let session = created["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);
    let page = serve_page("<title>Through it</title><h1>Still here after the restart</h1>").await;
    native_prompt(&client, &mut events, &session, &format!("[[mock:yours:{page}]] open"), true).await;

    service.kill().await.unwrap();
    let _ = service.wait().await;
    let stopped = std::time::Instant::now();
    let (mut service, client, demux) = serve(&env, &socket, None).await;
    status_until(&client, "the extension back", |s| s["extension"]["in_use"] == true && s["tabs"][&session][0]["url"] == page.as_str()).await;
    eprintln!("the extension was back {} ms after the service stopped", stopped.elapsed().as_millis());
    assert!(stopped.elapsed() < Duration::from_secs(20), "{:?}", stopped.elapsed());

    client.request("session/resume", json!({"session_id": session})).await.unwrap();
    let mut events = demux.subscribe(&session);
    let turn = native_prompt(&client, &mut events, &session, "[[mock:yours_read:y1]] read", true).await;
    let results = tool_results(&turn);
    assert!(results.contains("Still here after the restart") && results.contains("still open"), "{results}");
    assert!(pages(&chrome).await.contains(&page));

    client.request("server/shutdown", json!({})).await.unwrap();
    let _ = tokio::time::timeout(Duration::from_secs(20), service.wait()).await;
}

/// The connection drops during a call: what it did is not known, so it is not repeated by itself,
/// and the agent hears that.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_connection_lost_during_a_call_says_it_may_have_happened() {
    let Some(chrome_exe) = her_browser::launch::find_executable() else {
        eprintln!("no Chromium-family browser installed; skipping");
        return;
    };
    let env = TestEnv::new("bypass-permissions", "[browser]\nextension = true\n").await;
    let socket = env.home.path().join("run").join("her.sock");
    let profile = env.work().join("chrome-profile");
    let (mut service, client, demux) = serve(&env, &socket, None).await;
    let installed = env.her(&["browser", "extension", "install", "--user-data-dir", profile.to_str().unwrap()]).output().await.unwrap();
    assert!(installed.status.success(), "{}", String::from_utf8_lossy(&installed.stderr));
    let (chrome, chrome_process) = chrome_with_extension(&env, &profile, &chrome_exe).await;
    status_until(&client, "the extension", |s| s["extension"]["in_use"] == true).await;
    let cwd = env.workspace("ext");
    let created = client.request("session/new", json!({"cwd": cwd})).await.unwrap();
    let session = created["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);

    // A page that never finishes loading keeps the call open.
    let busy = serve_page("<title>Busy</title><script>for(;;);</script>").await;
    start_prompt(&client, &session, &format!("[[mock:yours:{busy}]] open")).await;
    for _ in 0..200 {
        if pages(&chrome).await.contains(&busy) {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    tokio::time::sleep(Duration::from_millis(500)).await;
    let hosts = std::process::Command::new("/usr/bin/pgrep").args(["-P", &chrome_process.id().unwrap().to_string(), "-f", "native-host"]).output().unwrap();
    let host = String::from_utf8_lossy(&hosts.stdout).lines().next().expect("the extension's host runs").to_string();
    std::process::Command::new("/bin/kill").args(["-9", &host]).status().unwrap();
    let said = said_until_done(&mut events).await;
    assert!(said.contains("may or may not have taken effect"), "{said}");

    client.request("server/shutdown", json!({})).await.unwrap();
    let _ = tokio::time::timeout(Duration::from_secs(20), service.wait()).await;
}

/// A call for the user's browser while it is not connected: it waits while a client can tell the
/// user, who can say not this time; with nobody to tell, and no browser ever seen, it says so.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_task_waits_for_the_users_browser_while_someone_can_tell_them() {
    let env = TestEnv::new("bypass-permissions", "[browser]\nextension = true\n").await;
    let socket = env.home.path().join("run").join("her.sock");
    let (mut service, client, demux) = serve_unwatched(&env, &socket, None).await;
    let cwd = env.workspace("ext");
    let created = client.request("session/new", json!({"cwd": cwd})).await.unwrap();
    let session = created["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);
    let turn = native_prompt(&client, &mut events, &session, "[[mock:yours:http://127.0.0.1:9/]] open", true).await;
    assert!(tool_results(&turn).contains("is not connected"), "{}", tool_results(&turn));

    // Now the client follows the browsers: the same call waits, and the user is the one to end it.
    client.request("browser/status", json!({})).await.unwrap();
    start_prompt(&client, &session, "[[mock:yours:http://127.0.0.1:9/]] open").await;
    status_until(&client, "the session waiting", |s| s["waiting"][0] == session.as_str()).await;
    tokio::time::sleep(Duration::from_secs(1)).await;
    let now = client.request("browser/status", json!({})).await.unwrap();
    assert_eq!(now["waiting"][0], session.as_str(), "it goes on waiting: {now}");
    client.request("browser/cancel_wait", json!({"session_id": session})).await.unwrap();
    let said = said_until_done(&mut events).await;
    assert!(said.contains("chose not to use"), "{said}");
    status_until(&client, "nothing waiting", |s| s.get("waiting").is_none()).await;

    client.request("server/shutdown", json!({})).await.unwrap();
    let _ = tokio::time::timeout(Duration::from_secs(20), service.wait()).await;
}

/// The app updates the extension's folder, and Chrome runs the old code until told: the extension
/// sees the new build and reloads, not while the agent uses a tab, and the tabs stay their session's.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_new_build_in_the_folder_reloads_the_extension_once_nothing_is_in_use() {
    let Some(chrome_exe) = her_browser::launch::find_executable() else {
        eprintln!("no Chromium-family browser installed; skipping");
        return;
    };
    let env = TestEnv::new("bypass-permissions", "[browser]\nextension = true\n").await;
    let socket = env.home.path().join("run").join("her.sock");
    let profile = env.work().join("chrome-profile");
    let folder = copy_extension(&env, "extension");
    std::fs::write(folder.join("build.json"), r#"{"build":"one"}"#).unwrap();
    let (mut service, client, demux) = serve(&env, &socket, None).await;
    let installed = env.her(&["browser", "extension", "install", "--user-data-dir", profile.to_str().unwrap()]).output().await.unwrap();
    assert!(installed.status.success(), "{}", String::from_utf8_lossy(&installed.stderr));
    let (chrome, _chrome_process) = chrome_with_folder(&env, &profile, &chrome_exe, &folder).await;
    developer_mode(&chrome).await;
    status_until(&client, "the extension", |s| s["extension"]["in_use"] == true).await;
    let cwd = env.workspace("ext");
    let created = client.request("session/new", json!({"cwd": cwd})).await.unwrap();
    let session = created["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);
    let page = serve_page("<title>Kept</title><h1>Kept across the update</h1>").await;
    native_prompt(&client, &mut events, &session, &format!("[[mock:yours:{page}]] open"), true).await;
    status_until(&client, "the tab, let go", |s| s["tabs"][&session][0].get("attached").is_none() && s["tabs"][&session][0]["url"] == page.as_str()).await;
    // Marks this worker; a new one does not have it.
    worker(&chrome, "globalThis.herBefore = true").await;

    // A task uses the tab: the new build waits.
    start_prompt(&client, &session, "[[mock:yours_hold:6000]] hold").await;
    status_until(&client, "the tab in use", |s| s["tabs"][&session][0]["attached"] == true).await;
    // Replaced whole, as the app does it.
    let fresh = copy_extension(&env, "extension.new");
    std::fs::write(fresh.join("build.json"), r#"{"build":"two"}"#).unwrap();
    std::fs::rename(&folder, env.work().join("extension.old")).unwrap();
    std::fs::rename(&fresh, &folder).unwrap();
    worker(&chrome, "chrome.alarms.onAlarm.dispatch({name: 'her-connect'})").await;
    tokio::time::sleep(Duration::from_millis(1500)).await;
    assert_eq!(worker(&chrome, "globalThis.herBefore === true").await, true, "reloaded with a tab in use");
    assert!(said_until_done(&mut events).await.contains("held"));

    // The task ended, and the extension's next look takes the update, by itself.
    let waited = std::time::Instant::now();
    let mut reloaded = false;
    // A slow runner can take well over the usual alarm period before the next look.
    while waited.elapsed() < Duration::from_secs(90) {
        if worker_now(&chrome, "globalThis.herBefore === undefined").await == Some(json!(true)) {
            reloaded = true;
            break;
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    assert!(reloaded, "the worker never reloaded");
    eprintln!("reloaded {} ms after the task ended", waited.elapsed().as_millis());
    status_until(&client, "the extension back, with the tab", |s| s["extension"]["in_use"] == true && s["tabs"][&session][0]["url"] == page.as_str()).await;
    assert_eq!(worker(&chrome, "chrome.storage.local.get('herCarried').then(v => v.herCarried === undefined)").await, true);
    let turn = native_prompt(&client, &mut events, &session, "[[mock:yours_read:y1]] read", true).await;
    assert!(tool_results(&turn).contains("Kept across the update"), "{}", tool_results(&turn));

    client.request("server/shutdown", json!({})).await.unwrap();
    let _ = tokio::time::timeout(Duration::from_secs(20), service.wait()).await;
}

/// Chrome restarts a stopped worker from the new files, under the manifest it loaded before: no
/// storage, and no identity. It still works, and reloads into the new manifest.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn new_code_under_an_older_manifest_reloads_into_the_new_one() {
    let Some(chrome_exe) = her_browser::launch::find_executable() else {
        eprintln!("no Chromium-family browser installed; skipping");
        return;
    };
    let env = TestEnv::new("bypass-permissions", "[browser]\nextension = true\n").await;
    let socket = env.home.path().join("run").join("her.sock");
    let profile = env.work().join("chrome-profile");
    let folder = copy_extension(&env, "extension");
    let current = std::fs::read_to_string(folder.join("manifest.json")).unwrap();
    let mut older: Value = serde_json::from_str(&current).unwrap();
    older["version"] = json!("0.1.0");
    older["permissions"] = json!(["debugger", "nativeMessaging", "tabs", "tabGroups", "alarms"]);
    older.as_object_mut().unwrap().remove("default_locale");
    older["action"].as_object_mut().unwrap().remove("default_popup");
    std::fs::write(folder.join("manifest.json"), older.to_string()).unwrap();
    let locales = env.work().join("locales-later");
    std::fs::rename(folder.join("_locales"), &locales).unwrap();

    let (mut service, client, _demux) = serve(&env, &socket, None).await;
    let installed = env.her(&["browser", "extension", "install", "--user-data-dir", profile.to_str().unwrap()]).output().await.unwrap();
    assert!(installed.status.success(), "{}", String::from_utf8_lossy(&installed.stderr));
    let (chrome, _chrome_process) = chrome_with_folder(&env, &profile, &chrome_exe, &folder).await;
    developer_mode(&chrome).await;
    let now = status_until(&client, "the extension", |s| s["extension"]["in_use"] == true).await;
    assert_eq!(now["extension"]["extension_version"], "0.1.0", "{now}");
    assert_eq!(worker(&chrome, "typeof chrome.storage").await, "undefined");

    // The update arrives: the next look finds it, without storage to carry anything in.
    std::fs::rename(&locales, folder.join("_locales")).unwrap();
    std::fs::write(folder.join("manifest.json"), &current).unwrap();
    worker(&chrome, "chrome.alarms.onAlarm.dispatch({name: 'her-connect'})").await;
    let version = serde_json::from_str::<Value>(&current).unwrap()["version"].clone();
    let now = status_until(&client, "the new version", |s| s["extension"]["extension_version"] == version && s["extension"]["in_use"] == true).await;
    assert!(now["extension"]["instance"].is_string(), "{now}");
    let mut ready = None;
    for _ in 0..50 {
        ready = worker_now(&chrome, "typeof chrome.storage").await;
        if ready == Some(json!("object")) {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert_eq!(ready, Some(json!("object")));

    client.request("server/shutdown", json!({})).await.unwrap();
    let _ = tokio::time::timeout(Duration::from_secs(20), service.wait()).await;
}

/// A sub-agent works for the conversation: its tabs are the conversation's in the user's browser,
/// stay when it ends, and the conversation finds them.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_sub_agents_tabs_stay_with_the_conversation() {
    let Some(chrome_exe) = her_browser::launch::find_executable() else {
        eprintln!("no Chromium-family browser installed; skipping");
        return;
    };
    let env = TestEnv::new("bypass-permissions", "[browser]\nextension = true\n").await;
    let socket = env.home.path().join("run").join("her.sock");
    let profile = env.work().join("chrome-profile");
    let (mut service, client, demux) = serve(&env, &socket, None).await;
    let installed = env.her(&["browser", "extension", "install", "--user-data-dir", profile.to_str().unwrap()]).output().await.unwrap();
    assert!(installed.status.success(), "{}", String::from_utf8_lossy(&installed.stderr));
    let (chrome, _chrome_process) = chrome_with_extension(&env, &profile, &chrome_exe).await;
    status_until(&client, "the extension", |s| s["extension"]["in_use"] == true).await;
    let cwd = env.workspace("ext");
    let created = client.request("session/new", json!({"cwd": cwd})).await.unwrap();
    let session = created["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);
    let page = serve_page("<title>From the agent</title><h1>Opened by a sub-agent</h1>").await;

    let turn = native_prompt(&client, &mut events, &session, &format!("[[mock:yours_agent:{page}]] delegate"), true).await;
    assert!(turn.text.contains("Opened by a sub-agent"), "{}", turn.text);
    let now = status_until(&client, "the tab, let go, under the conversation", |s| s["tabs"][&session][0]["url"] == page.as_str() && s["tabs"][&session][0].get("attached").is_none()).await;
    assert_eq!(now["tabs"].as_object().unwrap().len(), 1, "{now}");
    assert!(pages(&chrome).await.contains(&page));

    let turn = native_prompt(&client, &mut events, &session, "[[mock:yours_tabs:]] list", true).await;
    assert!(tool_results(&turn).contains("From the agent"), "{}", tool_results(&turn));

    client.request("server/shutdown", json!({})).await.unwrap();
    let _ = tokio::time::timeout(Duration::from_secs(20), service.wait()).await;
}
