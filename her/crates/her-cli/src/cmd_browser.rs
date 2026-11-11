//! The user's own Chrome (browser-design §8.4): `her browser native-host`, which Chrome
//! starts for the Hermesh extension, and `her browser extension install`, which tells
//! Chrome where that host is.
//!
//! The host relays between two framings. Chrome speaks Native Messaging on stdin and stdout:
//! a 4-byte length in native byte order, then that much JSON. The her service speaks the
//! native protocol on its Unix socket (`$HER_HOME/run/her.sock`, or the one `--socket` names,
//! as for the her that Hermesh runs on its own socket): the host calls `browser/host {kind: "extension"}` and
//! then carries CDP in `browser/cdp` notifications, as the desktop app does. To the extension
//! it sends `{stream, data}` (one CDP message of the browser numbered `stream`, as a JSON
//! object), or `{stream, part, more}` pieces of one that is over Chrome's 1 MB limit (parts of
//! the message's JSON text), and `{status}` when the service comes or goes; from it, it takes
//! `{stream, data}`, and first `{hello: {browser, extension_version, bridge: {min, max}}}`,
//! which it passes on so the app can say which browser is connected, and the service can pick
//! the bridge version both speak (D-219). Connected, it says `{status: "connected", bridge}`;
//! when they share none, `{status: "outdated", update: "extension" | "her", reason}`, and it
//! stays connected without a browser until one side is updated. It also carries `{control}`
//! both ways: what the extension and the service tell each other beyond CDP (§8.4.7).
//!
//! A host the service refuses runs another program than the service: an update replaced the app
//! while Chrome kept this process. It exits, and the extension starts the current one.
//! CDP messages pass through as JSON text: only the stream number is read.

use anyhow::Context as _;
use serde::Deserialize;
use serde_json::Value;
use serde_json::json;
use serde_json::value::RawValue;
use std::borrow::Cow;
use std::path::Path;
use std::path::PathBuf;
use std::time::Duration;
use tokio::io::AsyncBufReadExt;
use tokio::io::AsyncReadExt;
use tokio::io::AsyncWriteExt;
use tokio::sync::mpsc;

use crate::setup;

/// The Native Messaging host's name, as the manifest and the extension call it.
pub const HOST_NAME: &str = "com.hermesh.browser";
/// The Hermesh extension's ID, fixed by the `key` in its manifest.json.
pub const EXTENSION_ID: &str = "igeolfcjdhlfopmimahmhleloehmfkdc";
/// The extension's ID in the Chrome Web Store, once published, when the store gave it
/// another ID than [`EXTENSION_ID`] (it keeps that one only when the first upload carried
/// its key.pem). Every install lets both start the host.
pub const STORE_EXTENSION_ID: Option<&str> = None;
/// Chrome refuses messages from a host larger than this.
const CHROME_LIMIT: usize = 1 << 20;
/// Pieces of a larger message stay well under it, JSON escaping included.
const PIECE: usize = 256 << 10;
/// Messages from the extension: an accessibility tree or a screenshot can be large.
const MAX_INBOUND: usize = 64 << 20;
/// How often the host looks for the service while it is not running.
const RETRY: Duration = Duration::from_secs(2);
/// How long the host waits for the extension's hello before connecting without it. A worker that
/// has just started on a busy machine can take seconds, and a hello that comes late is lost.
const HELLO_WAIT: Duration = Duration::from_secs(30);
/// The host's exit code when the service refused it.
const REFUSED_EXIT: i32 = 3;

/// The service refused this host: it is not the service's own program.
#[derive(Debug)]
struct Refused(String);

impl std::fmt::Display for Refused {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for Refused {}

// ---- The host ----

/// `her browser native-host`: runs until Chrome closes stdin, relaying to the service on
/// `socket` (else the home's). Nothing but messages may go to stdout.
pub async fn native_host(socket: Option<PathBuf>) -> anyhow::Result<u8> {
    let home = setup::home()?;
    let _guard = setup::init_logging(&home);
    let socket = socket.unwrap_or_else(|| home.root().join("run").join("her.sock"));
    tracing::info!(socket = %socket.display(), "extension host started");

    let (to_chrome, mut outbox) = mpsc::unbounded_channel::<String>();
    let writer = tokio::spawn(async move {
        let mut stdout = tokio::io::stdout();
        while let Some(message) = outbox.recv().await {
            for frame in frames(&message) {
                if stdout.write_all(&frame).await.is_err() || stdout.flush().await.is_err() {
                    return;
                }
            }
        }
    });

    // Messages from the extension go to the service connection of the moment.
    let (from_chrome, inbox) = mpsc::unbounded_channel::<String>();
    let reader = tokio::spawn(async move {
        let mut stdin = tokio::io::stdin();
        while let Ok(Some(message)) = read_message(&mut stdin).await {
            if from_chrome.send(message).is_err() {
                break;
            }
        }
    });

    let mut relay = tokio::spawn(relay(socket, to_chrome, inbox));
    tokio::select! {
        // Chrome closed the port (the extension stopped, or Chrome quit).
        _ = reader => relay.abort(),
        ended = &mut relay => {
            if let Ok(Some(reason)) = ended {
                tracing::info!(%reason, "extension host refused; exiting for the extension to start the current one");
                // A read of stdin cannot be cancelled, so the runtime would wait for it.
                drop(_guard);
                std::process::exit(REFUSED_EXIT);
            }
        }
    }
    writer.abort();
    tracing::info!("extension host stopped");
    Ok(0)
}

/// Connects to the service again and again, relaying while connected. Ends when Chrome closed the
/// port, or with why the service refused this host.
async fn relay(socket: PathBuf, to_chrome: mpsc::UnboundedSender<String>, mut inbox: mpsc::UnboundedReceiver<String>) -> Option<String> {
    // The extension says who it is as soon as it connects.
    let hello = match tokio::time::timeout(HELLO_WAIT, inbox.recv()).await {
        Ok(None) => return None,
        Ok(Some(message)) => serde_json::from_str::<Value>(&message)
            .ok()
            .and_then(|m| m.get("hello").cloned())
            .unwrap_or_default(),
        Err(_) => {
            tracing::warn!("the extension said no hello in time");
            Value::Null
        }
    };
    let mut said = String::new();
    loop {
        let status = match serve_once(&socket, &hello, &to_chrome, &mut inbox).await {
            Ok(()) => "Hermesh stopped".to_string(),
            Err(err) => {
                if let Some(refused) = err.downcast_ref::<Refused>() {
                    return Some(refused.0.clone());
                }
                format!("{err:#}")
            }
        };
        if status != said {
            tracing::info!(%status, "extension host waiting");
            let _ = to_chrome.send(json!({"status": "waiting", "reason": status}).to_string());
            said = status;
        }
        // While waiting, the extension's messages belong to no browser.
        let wait = tokio::time::sleep(RETRY);
        tokio::pin!(wait);
        loop {
            tokio::select! {
                _ = &mut wait => break,
                message = inbox.recv() => match message {
                    Some(message) => { host_action(&message); }
                    None => return None,
                },
            }
        }
    }
}

/// One connection to the service, until it ends.
async fn serve_once(
    socket: &Path,
    hello: &Value,
    to_chrome: &mpsc::UnboundedSender<String>,
    inbox: &mut mpsc::UnboundedReceiver<String>,
) -> anyhow::Result<()> {
    let stream = tokio::net::UnixStream::connect(socket)
        .await
        .map_err(|_| anyhow::anyhow!("Hermesh is not running"))?;
    let (read, mut write) = stream.into_split();
    let mut lines = tokio::io::BufReader::new(read).lines();
    let line = |value: Value| format!("{value}\n").into_bytes();
    write
        .write_all(&line(json!({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
            "protocol_version": her_protocol::PROTOCOL_VERSION,
            "client_info": {"name": "her-browser-extension", "version": env!("CARGO_PKG_VERSION")},
        }})))
        .await?;
    answer(&mut lines, 1).await.context("the Hermesh service did not start a session with the extension")?;
    write
        .write_all(&line(json!({"jsonrpc": "2.0", "id": 2, "method": "browser/host", "params": {
            "kind": "extension",
            "browser": hello["browser"].as_str(),
            "extension_version": hello["extension_version"].as_str(),
            "bridge": hello.get("bridge").filter(|b| b["min"].is_u64() && b["max"].is_u64()),
            "instance": hello["instance"].as_str(),
            "account": hello["account"].as_str(),
        }})))
        .await?;
    let result = answer(&mut lines, 2).await.context("the Hermesh service refused the extension")?;
    if let Some(update) = outdated(&result) {
        tracing::warn!(update, "the extension and her share no bridge version");
        let reason = match update {
            "extension" => "this extension is too old for Hermesh; it updates by itself, or update it on the extensions page",
            _ => "this extension is newer than Hermesh; update Hermesh",
        };
        let _ = to_chrome.send(json!({"status": "outdated", "update": update, "reason": reason}).to_string());
        // Held open, so the app can say so, until the service goes; the extension's messages
        // belong to no browser.
        loop {
            tokio::select! {
                next = lines.next_line() => if next?.is_none() { return Ok(()) },
                message = inbox.recv() => match message {
                    Some(message) => { host_action(&message); }
                    None => return Ok(()),
                },
            }
        }
    }
    tracing::info!(bridge = %result["bridge"], "extension host connected");
    let _ = to_chrome.send(json!({"status": "connected", "bridge": result["bridge"]}).to_string());
    loop {
        tokio::select! {
            next = lines.next_line() => {
                let Some(text) = next? else { return Ok(()) };
                let Ok(message) = serde_json::from_str::<Incoming>(&text) else { continue };
                match (message.method.as_deref(), message.params) {
                    (Some("browser/cdp"), Some(params)) => {
                        let _ = to_chrome.send(params.get().to_string());
                    }
                    (Some("browser/control"), Some(params)) => {
                        let _ = to_chrome.send(format!("{{\"control\":{}}}", params.get()));
                    }
                    _ => {}
                }
            }
            message = inbox.recv() => {
                let Some(message) = message else { return Ok(()) };
                if host_action(&message) {
                    continue;
                }
                if let Some(line) = cdp_line(&message).or_else(|| control_line(&message)) {
                    write.write_all(&line).await?;
                }
            }
        }
    }
}

/// What the host does itself, without the service: opening the app, which may not be running.
/// True when `message` was that.
fn host_action(message: &str) -> bool {
    let Ok(value) = serde_json::from_str::<Value>(message) else {
        return false;
    };
    if value["control"]["kind"] != "open_app" {
        return false;
    }
    let exe = std::env::current_exe().ok();
    let app = exe.iter().flat_map(|exe| exe.ancestors()).find(|p| p.extension().is_some_and(|e| e == "app"));
    match app {
        Some(app) if cfg!(target_os = "macos") => {
            if let Err(err) = std::process::Command::new("/usr/bin/open").arg(app).spawn() {
                tracing::warn!(%err, "cannot open the app");
            }
        }
        _ => tracing::info!("no app to open from here"),
    }
    true
}

/// The `browser/extension` line for what the extension tells the service, if it is that.
fn control_line(message: &str) -> Option<Vec<u8>> {
    let value = serde_json::from_str::<Value>(message).ok()?;
    let control = value.get("control")?;
    Some(format!("{}\n", json!({"jsonrpc": "2.0", "method": "browser/extension", "params": control})).into_bytes())
}

/// Which side to update, when the service found no bridge version the extension speaks.
fn outdated(result: &Value) -> Option<&'static str> {
    match result["problem"].as_str()? {
        "update_extension" => Some("extension"),
        _ => Some("her"),
    }
}

/// A line from the service, with its params left as JSON text.
#[derive(Deserialize)]
struct Incoming<'a> {
    #[serde(borrow, default)]
    method: Option<Cow<'a, str>>,
    #[serde(borrow, default)]
    params: Option<&'a RawValue>,
}

/// `{stream, data}`, with the CDP message left as JSON text.
#[derive(Deserialize)]
struct Cdp<'a> {
    stream: u64,
    #[serde(borrow)]
    data: &'a RawValue,
}

/// The `browser/cdp` line for a message from the extension, if it is one: `{stream, data}`
/// with an object for data. It goes on as Chrome wrote it, unless it spans lines.
fn cdp_line(message: &str) -> Option<Vec<u8>> {
    let cdp = serde_json::from_str::<Cdp>(message).ok()?;
    if !cdp.data.get().starts_with('{') {
        return None;
    }
    let params: Cow<str> = if message.contains('\n') {
        Cow::Owned(serde_json::from_str::<Value>(message).ok()?.to_string())
    } else {
        Cow::Borrowed(message)
    };
    Some(format!("{{\"jsonrpc\":\"2.0\",\"method\":\"browser/cdp\",\"params\":{params}}}\n").into_bytes())
}

/// Waits for the response to request `id`; an error response is an error.
async fn answer<R: tokio::io::AsyncBufRead + Unpin>(lines: &mut tokio::io::Lines<R>, id: u64) -> anyhow::Result<Value> {
    while let Some(text) = lines.next_line().await? {
        let Ok(message) = serde_json::from_str::<Value>(&text) else { continue };
        if message["id"] != json!(id) {
            continue;
        }
        if let Some(error) = message.get("error") {
            let text = error["message"].as_str().unwrap_or("error");
            if error["data"]["kind"] == her_protocol::api::HOST_REFUSED {
                return Err(Refused(text.to_string()).into());
            }
            anyhow::bail!("{text}");
        }
        return Ok(message["result"].clone());
    }
    anyhow::bail!("the connection closed")
}

/// The frames for one message (JSON text): one, or pieces when it is over Chrome's limit.
fn frames(message: &str) -> Vec<Vec<u8>> {
    if message.len() <= CHROME_LIMIT - 4 {
        return vec![frame(message.as_bytes())];
    }
    let Ok(Cdp { stream, data }) = serde_json::from_str::<Cdp>(message) else {
        tracing::warn!(bytes = message.len(), "dropped a message too large for Chrome");
        return Vec::new();
    };
    let mut out = Vec::new();
    let mut rest = data.get();
    while !rest.is_empty() {
        let mut end = rest.len().min(PIECE);
        while !rest.is_char_boundary(end) {
            end -= 1;
        }
        let (part, tail) = rest.split_at(end);
        rest = tail;
        out.push(frame(json!({"stream": stream, "part": part, "more": !rest.is_empty()}).to_string().as_bytes()));
    }
    out
}

fn frame(json: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(json.len() + 4);
    out.extend_from_slice(&(json.len() as u32).to_ne_bytes());
    out.extend_from_slice(json);
    out
}

/// One message from Chrome, as JSON text; `None` at the end of input.
async fn read_message<R: tokio::io::AsyncRead + Unpin>(input: &mut R) -> anyhow::Result<Option<String>> {
    let mut length = [0u8; 4];
    match input.read_exact(&mut length).await {
        Ok(_) => {}
        Err(err) if err.kind() == std::io::ErrorKind::UnexpectedEof => return Ok(None),
        Err(err) => return Err(err.into()),
    }
    let length = u32::from_ne_bytes(length) as usize;
    anyhow::ensure!(length <= MAX_INBOUND, "a message of {length} bytes is too large");
    let mut body = vec![0u8; length];
    input.read_exact(&mut body).await?;
    Ok(Some(String::from_utf8(body)?))
}

// ---- Installing ----

/// Where each Chromium-family browser looks for the user's Native Messaging hosts.
fn browser_dirs() -> Vec<(&'static str, PathBuf)> {
    let Some(home) = std::env::var_os("HOME").map(PathBuf::from) else {
        return Vec::new();
    };
    let list: &[(&str, &str)] = if cfg!(target_os = "macos") {
        &[
            ("Google Chrome", "Library/Application Support/Google/Chrome"),
            ("Google Chrome Beta", "Library/Application Support/Google/Chrome Beta"),
            ("Google Chrome Canary", "Library/Application Support/Google/Chrome Canary"),
            ("Chrome for Testing", "Library/Application Support/Google/Chrome for Testing"),
            ("Chromium", "Library/Application Support/Chromium"),
            ("Microsoft Edge", "Library/Application Support/Microsoft Edge"),
            ("Brave", "Library/Application Support/BraveSoftware/Brave-Browser"),
        ]
    } else {
        &[
            ("Google Chrome", ".config/google-chrome"),
            ("Google Chrome Beta", ".config/google-chrome-beta"),
            ("Chromium", ".config/chromium"),
            ("Microsoft Edge", ".config/microsoft-edge"),
            ("Brave", ".config/BraveSoftware/Brave-Browser"),
        ]
    };
    list.iter().map(|(name, dir)| (*name, home.join(dir))).collect()
}

fn manifest_path(user_data_dir: &Path) -> PathBuf {
    user_data_dir.join("NativeMessagingHosts").join(format!("{HOST_NAME}.json"))
}

/// The script Chrome runs: Chrome passes no environment of ours and allows no arguments in
/// the manifest, so the script holds both, and the service's socket when it is not the home's.
fn launcher_script(exe: &Path, home: &Path, socket: Option<&Path>) -> String {
    let quote = |path: &Path| format!("'{}'", path.display().to_string().replace('\'', r"'\''"));
    let socket = socket.map(|s| format!(" --socket {}", quote(s))).unwrap_or_default();
    format!(
        "#!/bin/sh\n# Written by `her browser extension install`: Chrome runs this for the Hermesh extension.\n\
         HER_HOME={} exec {} browser native-host{socket} \"$@\"\n",
        quote(home),
        quote(exe)
    )
}

macro_rules! extension_file {
    ($path:literal) => {
        ($path, include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/../../apps/chrome-extension/", $path)) as &[u8])
    };
}

/// The Hermesh extension as Chrome loads it (apps/chrome-extension, without its tests and
/// types): her carries the one that speaks its bridge versions, for where no app brings it.
/// The manifest keeps its `key`, so loaded from a folder it has [`EXTENSION_ID`].
const EXTENSION_FILES: &[(&str, &[u8])] = &[
    extension_file!("manifest.json"),
    extension_file!("src/background.js"),
    extension_file!("src/browser.js"),
    extension_file!("src/methods.js"),
    extension_file!("src/panel.js"),
    extension_file!("src/popup.css"),
    extension_file!("src/popup.html"),
    extension_file!("src/popup.js"),
    extension_file!("src/relay.js"),
    extension_file!("icons/16.png"),
    extension_file!("icons/32.png"),
    extension_file!("icons/48.png"),
    extension_file!("icons/128.png"),
    extension_file!("_locales/en/messages.json"),
    extension_file!("_locales/zh_CN/messages.json"),
];

/// Makes `folder` the extension, unless it is that already: written beside it first, so
/// Chrome never finds half of it.
fn place_extension(folder: &Path) -> anyhow::Result<()> {
    if EXTENSION_FILES.iter().all(|(name, data)| std::fs::read(folder.join(name)).is_ok_and(|have| have == *data)) {
        return Ok(());
    }
    let mut draft = folder.as_os_str().to_owned();
    draft.push(".new");
    let draft = PathBuf::from(draft);
    if draft.exists() {
        std::fs::remove_dir_all(&draft)?;
    }
    for (name, data) in EXTENSION_FILES {
        let path = draft.join(name);
        std::fs::create_dir_all(path.parent().unwrap_or(&draft))?;
        std::fs::write(&path, data)?;
    }
    if folder.exists() {
        std::fs::remove_dir_all(folder)?;
    }
    std::fs::rename(&draft, folder)?;
    Ok(())
}

/// `her browser extension install`: the launcher script, and a manifest for every
/// Chromium-family browser on this machine (or for one user data directory); with `folder`,
/// the extension itself there, to load in Chrome.
pub fn install(extension_ids: &[String], user_data_dir: Option<PathBuf>, socket: Option<PathBuf>, folder: Option<PathBuf>) -> anyhow::Result<u8> {
    use std::os::unix::fs::PermissionsExt as _;
    let home = setup::home()?;
    // First: it is there to load even where no browser is installed yet.
    if let Some(folder) = &folder {
        place_extension(folder).with_context(|| format!("cannot write the extension to {}", folder.display()))?;
        println!("extension: {}", folder.display());
    }
    let exe = std::env::current_exe().context("cannot find the her program")?;
    let exe = std::fs::canonicalize(&exe).unwrap_or(exe);
    // Chrome starts the host in a folder of its own.
    let socket = socket.map(std::path::absolute).transpose()?;
    let launcher = home.browser_dir().join("native-host");
    std::fs::create_dir_all(home.browser_dir())?;
    std::fs::write(&launcher, launcher_script(&exe, home.root(), socket.as_deref()))?;
    std::fs::set_permissions(&launcher, std::fs::Permissions::from_mode(0o755))?;

    let mut ids: Vec<&str> = vec![EXTENSION_ID];
    ids.extend(STORE_EXTENSION_ID);
    ids.extend(extension_ids.iter().map(String::as_str));
    for id in &ids {
        anyhow::ensure!(
            id.len() == 32 && id.bytes().all(|b| (b'a'..=b'p').contains(&b)),
            "\"{id}\" is not an extension ID (32 letters a to p)"
        );
    }
    let manifest = json!({
        "name": HOST_NAME,
        "description": "Hermesh: lets the agent use the tabs it opens in this browser",
        "path": launcher,
        "type": "stdio",
        "allowed_origins": ids.iter().map(|id| format!("chrome-extension://{id}/")).collect::<Vec<_>>(),
    });
    let targets: Vec<(String, PathBuf)> = match user_data_dir {
        Some(dir) => vec![(dir.display().to_string(), dir)],
        // Only browsers that are installed: their directory exists.
        None => browser_dirs().into_iter().filter(|(_, dir)| dir.is_dir()).map(|(n, d)| (n.to_string(), d)).collect(),
    };
    if targets.is_empty() {
        anyhow::bail!("no Chrome, Chromium, Edge or Brave found for this user; start one once, then run this again");
    }
    for (name, dir) in &targets {
        let path = manifest_path(dir);
        std::fs::create_dir_all(path.parent().unwrap_or(dir))?;
        std::fs::write(&path, serde_json::to_string_pretty(&manifest)? + "\n")?;
        println!("{name}: {}", path.display());
    }
    println!("host: {}", launcher.display());
    if let Some(socket) = &socket {
        println!("service: {}", socket.display());
    }
    println!(
        "Next: load the Hermesh extension in Chrome, and set extension = true under [browser] in {}.",
        home.root().join("config.toml").display()
    );
    Ok(0)
}

/// `her browser extension uninstall`: removes the manifests and the script.
pub fn uninstall() -> anyhow::Result<u8> {
    let home = setup::home()?;
    for (name, dir) in browser_dirs() {
        let path = manifest_path(&dir);
        if path.exists() {
            std::fs::remove_file(&path)?;
            println!("removed for {name}: {}", path.display());
        }
    }
    let launcher = home.browser_dir().join("native-host");
    if launcher.exists() {
        std::fs::remove_file(&launcher)?;
        println!("removed {}", launcher.display());
    }
    Ok(0)
}

/// `her browser extension status`: where the host is installed, and whether what each
/// browser would start is there and is this home's.
pub fn status(as_json: bool) -> anyhow::Result<u8> {
    let home = setup::home()?;
    let launcher = home.browser_dir().join("native-host");
    if as_json {
        let browsers: Vec<Value> = browser_dirs()
            .into_iter()
            .map(|(name, dir)| (name, manifest_path(&dir)))
            .filter(|(_, path)| path.exists())
            .map(|(name, path)| serde_json::json!({ "name": name, "state": manifest_state(&path, &launcher), "manifest": path }))
            .collect();
        let program = launcher_program(&launcher);
        println!("{}", serde_json::json!({ "browsers": browsers, "host": launcher.exists().then_some(&launcher), "program": program }));
        return Ok(0);
    }
    let mut any = false;
    for (name, dir) in browser_dirs() {
        let path = manifest_path(&dir);
        if path.exists() {
            println!("{name}: {} ({})", manifest_state(&path, &launcher), path.display());
            any = true;
        }
    }
    if !any {
        println!("not installed; run `her browser extension install`");
    } else if !launcher.exists() {
        println!("the host script {} is missing; run `her browser extension install` again", launcher.display());
    } else if let Some(program) = launcher_program(&launcher) {
        let state = if !program.exists() { " (missing; run `her browser extension install` again)" } else { "" };
        println!("host: {} starts {}{state}", launcher.display(), program.display());
    }
    Ok(0)
}

/// What a browser's manifest says about the host it would start.
fn manifest_state(manifest: &Path, launcher: &Path) -> String {
    let path = std::fs::read_to_string(manifest)
        .ok()
        .and_then(|text| serde_json::from_str::<Value>(&text).ok())
        .and_then(|value| value["path"].as_str().map(PathBuf::from));
    match path {
        None => "the manifest cannot be read; run `her browser extension install` again".to_string(),
        Some(path) if !path.exists() => format!("points at {}, which is gone; run `her browser extension install` again", path.display()),
        Some(path) if path != launcher => format!("registered by another Hermesh home ({})", path.display()),
        Some(_) => "installed".to_string(),
    }
}

/// The program a launcher script starts.
fn launcher_program(launcher: &Path) -> Option<PathBuf> {
    let script = std::fs::read_to_string(launcher).ok()?;
    let start = script.find(" exec '")? + " exec '".len();
    let end = script[start..].find("' browser native-host")?;
    Some(PathBuf::from(script[start..start + end].replace(r"'\''", "'")))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn her_carries_every_file_chrome_loads() {
        fn walk(dir: &Path, root: &Path, out: &mut Vec<String>) {
            for entry in std::fs::read_dir(dir).unwrap() {
                let path = entry.unwrap().path();
                if path.is_dir() {
                    walk(&path, root, out);
                } else {
                    out.push(path.strip_prefix(root).unwrap().to_string_lossy().into_owned());
                }
            }
        }
        let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../apps/chrome-extension");
        let mut loads = vec!["manifest.json".to_string()];
        for part in ["src", "icons", "_locales"] {
            walk(&root.join(part), &root, &mut loads);
        }
        loads.retain(|f| !f.ends_with(".test.js") && !f.ends_with(".d.ts") && !f.ends_with(".DS_Store"));
        loads.sort();
        let mut carried: Vec<&str> = EXTENSION_FILES.iter().map(|(name, _)| *name).collect();
        carried.sort_unstable();
        assert_eq!(carried, loads, "list every file of apps/chrome-extension that Chrome loads in EXTENSION_FILES");
    }

    #[test]
    fn the_extension_is_written_once_and_replaced_whole() {
        let dir = tempfile::tempdir().unwrap();
        let folder = dir.path().join("share").join("chrome-extension");
        place_extension(&folder).unwrap();
        for (name, data) in EXTENSION_FILES {
            assert_eq!(std::fs::read(folder.join(name)).unwrap(), *data, "{name}");
        }

        // The same: left alone, with what Chrome wrote beside the files.
        std::fs::write(folder.join("mark"), "kept").unwrap();
        place_extension(&folder).unwrap();
        assert!(folder.join("mark").exists(), "a folder that was the same was written again");

        // An older one: replaced whole, and nothing is left beside it.
        std::fs::write(folder.join("src/background.js"), "old").unwrap();
        place_extension(&folder).unwrap();
        assert_eq!(std::fs::read(folder.join("src/background.js")).unwrap(), EXTENSION_FILES[1].1);
        assert!(!folder.join("mark").exists(), "the old folder's files are left");
        assert!(!dir.path().join("share").join("chrome-extension.new").exists(), "the draft is left");
    }

    #[test]
    fn large_messages_go_in_pieces_under_chromes_limit() {
        let small = json!({"stream": 1, "data": {"id": 1}}).to_string();
        assert_eq!(frames(&small).len(), 1);
        // Escaping the parts makes them larger than the text.
        let data = json!({"id": 2, "result": {"text": "é\"".repeat(400_000)}}).to_string();
        let big = format!("{{\"stream\":3,\"data\":{data}}}");
        let pieces = frames(&big);
        assert!(pieces.len() > 1);
        let mut joined = String::new();
        for (i, frame) in pieces.iter().enumerate() {
            let length = u32::from_ne_bytes(frame[..4].try_into().unwrap()) as usize;
            assert_eq!(length, frame.len() - 4);
            assert!(length < CHROME_LIMIT, "piece {i} is {length} bytes");
            let piece: Value = serde_json::from_slice(&frame[4..]).unwrap();
            assert_eq!(piece["stream"], 3);
            assert_eq!(piece["more"], i + 1 < pieces.len());
            joined.push_str(piece["part"].as_str().unwrap());
        }
        assert_eq!(joined, data);
    }

    #[tokio::test]
    async fn messages_from_chrome_are_read_in_order() {
        let mut input = Vec::new();
        input.extend(frame(b"{\"stream\":1,\"data\":{\"id\":1}}"));
        input.extend(frame(b"{\"stream\":2,\"data\":{\"id\":2}}"));
        let mut reader = &input[..];
        assert_eq!(read_message(&mut reader).await.unwrap().unwrap(), "{\"stream\":1,\"data\":{\"id\":1}}");
        assert_eq!(read_message(&mut reader).await.unwrap().unwrap(), "{\"stream\":2,\"data\":{\"id\":2}}");
        assert!(read_message(&mut reader).await.unwrap().is_none());
    }

    #[test]
    fn messages_from_chrome_go_to_the_service_as_they_are() {
        let message = "{\"stream\":4,\"data\":{\"id\":7,\"result\":{}}}";
        let line = String::from_utf8(cdp_line(message).unwrap()).unwrap();
        assert_eq!(line, format!("{{\"jsonrpc\":\"2.0\",\"method\":\"browser/cdp\",\"params\":{message}}}\n"));
        let spread = "{\"stream\":4,\n\"data\":{\"id\":7}}";
        let line = String::from_utf8(cdp_line(spread).unwrap()).unwrap();
        assert_eq!(line.matches('\n').count(), 1, "one line: {line}");
        assert!(cdp_line("{\"stream\":4,\"data\":\"text\"}").is_none());
        assert!(cdp_line("{\"hello\":{}}").is_none());
    }

    #[test]
    fn control_goes_to_the_service_and_open_app_stays_here() {
        let line = String::from_utf8(control_line(r#"{"control":{"kind":"stop","session_id":"s"}}"#).unwrap()).unwrap();
        let value: Value = serde_json::from_str(&line).unwrap();
        assert_eq!(value["method"], "browser/extension");
        assert_eq!(value["params"], json!({"kind": "stop", "session_id": "s"}));
        assert!(control_line(r#"{"stream":1,"data":{}}"#).is_none());
        assert!(!host_action(r#"{"control":{"kind":"stop","session_id":"s"}}"#));
        assert!(!host_action("not json"));
    }

    #[test]
    fn status_says_what_a_manifest_points_at() {
        let dir = tempfile::tempdir().unwrap();
        let launcher = dir.path().join("native-host");
        std::fs::write(&launcher, launcher_script(Path::new("/Apps/Her Work/her"), dir.path(), None)).unwrap();
        assert_eq!(launcher_program(&launcher), Some(PathBuf::from("/Apps/Her Work/her")));
        std::fs::write(&launcher, launcher_script(Path::new("/Apps/Her Work/her"), dir.path(), Some(Path::new("/s/her.sock")))).unwrap();
        assert_eq!(launcher_program(&launcher), Some(PathBuf::from("/Apps/Her Work/her")));
        let manifest = dir.path().join("m.json");
        let write = |path: &Path| std::fs::write(&manifest, json!({"path": path}).to_string()).unwrap();
        write(&launcher);
        assert_eq!(manifest_state(&manifest, &launcher), "installed");
        write(&dir.path().join("gone"));
        assert!(manifest_state(&manifest, &launcher).contains("which is gone"));
        let other = dir.path().join("other");
        std::fs::write(&other, "").unwrap();
        write(&other);
        assert!(manifest_state(&manifest, &launcher).contains("another Hermesh home"));
    }

    #[test]
    fn the_launcher_quotes_its_paths() {
        let script = launcher_script(Path::new("/Apps/Her Work/her"), Path::new("/Users/o'brien/.her"), None);
        assert!(script.contains("HER_HOME='/Users/o'\\''brien/.her' exec '/Apps/Her Work/her' browser native-host \"$@\""), "{script}");
        // Hermesh's her listens in its own folder, which has a space on a Mac.
        let socket = Path::new("/Users/o'brien/Library/Application Support/Hermesh/machine/her/her.sock");
        let script = launcher_script(Path::new("/Apps/Hermesh.app/Contents/Helpers/her"), Path::new("/Users/o'brien/.her"), Some(socket));
        assert!(
            script.contains(
                "exec '/Apps/Hermesh.app/Contents/Helpers/her' browser native-host --socket '/Users/o'\\''brien/Library/Application Support/Hermesh/machine/her/her.sock' \"$@\""
            ),
            "{script}"
        );
    }
}
