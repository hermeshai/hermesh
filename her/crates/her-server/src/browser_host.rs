//! The desktop app's embedded browser (browser-design §8.3.1): a connection that called
//! `browser/host` carries CDP as `browser/cdp` notifications. Each browser the pool starts on
//! it gets an in-memory byte stream, `\0`-separated like `--remote-debugging-pipe`, so
//! her-browser runs on it as on a managed browser.

use her_browser::BrowserError;
use her_browser::EmbeddedHost;
use her_browser::Stream;
use her_protocol::api::method;
use parking_lot::Mutex;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use tokio::io::AsyncReadExt;
use tokio::io::AsyncWriteExt;
use tokio::sync::mpsc;

use crate::conn::ConnHandle;

/// Room in the stream between the connection and the browser, either way.
const STREAM_BUFFER: usize = 1 << 20;

pub(crate) struct HostLink {
    id: u64,
    conn: ConnHandle,
    /// The current browser's stream number and where the host's messages for it go;
    /// replaced by each `connect`, which ends the one before.
    inbound: Mutex<Option<(u64, mpsc::UnboundedSender<String>)>>,
    streams: AtomicU64,
    /// An extension that lets tabs go and finds them again (browser-design §8.4.7).
    keeps_tabs: bool,
}

impl HostLink {
    pub(crate) fn new(conn: ConnHandle, keeps_tabs: bool) -> Self {
        static NEXT: AtomicU64 = AtomicU64::new(1);
        Self {
            id: NEXT.fetch_add(1, Ordering::Relaxed),
            conn,
            inbound: Mutex::new(None),
            streams: AtomicU64::new(0),
            keeps_tabs,
        }
    }

    /// A `browser/cdp` notification from the host: a response or an event. One for a
    /// browser that has ended is dropped.
    pub(crate) fn receive(&self, stream: u64, data: String) {
        if let Some((current, tx)) = &*self.inbound.lock()
            && *current == stream
        {
            let _ = tx.send(data);
        }
    }

    /// The host left: the current browser's stream ends.
    pub(crate) fn close(&self) {
        self.inbound.lock().take();
    }
}

impl EmbeddedHost for HostLink {
    fn id(&self) -> u64 {
        self.id
    }

    fn keeps_tabs(&self) -> bool {
        self.keeps_tabs
    }

    fn connect(&self) -> Result<Stream, BrowserError> {
        if self.conn.queue.is_closed() {
            return Err(BrowserError::Launch("the desktop app has disconnected".into()));
        }
        let stream = self.streams.fetch_add(1, Ordering::Relaxed) + 1;
        let (ours, theirs) = tokio::io::duplex(STREAM_BUFFER);
        let (mut from_kernel, mut to_kernel) = tokio::io::split(theirs);
        // Kernel → host: each `\0`-ended message becomes a notification.
        let conn = self.conn.clone();
        tokio::spawn(async move {
            let mut pending = Vec::new();
            let mut chunk = vec![0u8; 64 << 10];
            loop {
                let n = match from_kernel.read(&mut chunk).await {
                    Ok(0) | Err(_) => break,
                    Ok(n) => n,
                };
                pending.extend_from_slice(&chunk[..n]);
                while let Some(end) = pending.iter().position(|b| *b == 0) {
                    let message: Vec<u8> = pending.drain(..=end).collect();
                    conn.send(cdp_notification(stream, &message[..end]));
                }
            }
        });
        // Host → kernel, in arrival order.
        let (tx, mut rx) = mpsc::unbounded_channel::<String>();
        tokio::spawn(async move {
            while let Some(data) = rx.recv().await {
                let mut bytes = data.into_bytes();
                bytes.push(0);
                if to_kernel.write_all(&bytes).await.is_err() {
                    break;
                }
            }
            // The browser's stream ends: its connection closes, and so the browser is dead.
            let _ = to_kernel.shutdown().await;
        });
        *self.inbound.lock() = Some((stream, tx));
        let (reader, writer) = tokio::io::split(ours);
        Ok((Box::new(reader), Box::new(writer)))
    }
}

/// The `browser/cdp` line for one of the kernel's CDP messages, with the message embedded as
/// is: her-browser wrote it as compact JSON (no newline), so it need not be parsed again.
fn cdp_notification(stream: u64, message: &[u8]) -> Vec<u8> {
    let head = format!("{{\"jsonrpc\":\"2.0\",\"method\":\"{}\",\"params\":{{\"stream\":{stream},\"data\":", method::BROWSER_CDP);
    let mut line = Vec::with_capacity(head.len() + message.len() + 3);
    line.extend_from_slice(head.as_bytes());
    line.extend_from_slice(message);
    line.extend_from_slice(b"}}\n");
    line
}

#[cfg(test)]
mod tests {
    #![allow(clippy::print_stderr)]

    use her_browser::Backend;
    use her_browser::BrowserConfig;
    use her_browser::BrowserSession;
    use her_browser::Headless;
    use her_browser::SessionOptions;
    use her_browser::launch::LaunchOptions;
    use her_browser::launch::Launcher;
    use her_browser::launch::ManagedLauncher;
    use her_browser::router::OpenGuard;
    use her_core::HerHome;
    use serde_json::Value;
    use serde_json::json;
    use std::sync::Arc;
    use std::time::Duration;
    use tokio::io::AsyncBufReadExt;
    use tokio::io::AsyncWriteExt;
    use tokio::net::TcpListener;

    use crate::native::NativeProtocol;
    use crate::server::Server;
    use crate::server::ServerOptions;

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

    /// A stand-in for the desktop app: a client of the server that calls `browser/host`
    /// and relays `browser/cdp` to a real Chromium, leaving out `herSession` as the app's
    /// own browser end does, and noting the commands it got.
    #[tokio::test]
    async fn an_embedded_browser_runs_over_the_host_connection() {
        let Some(executable) = her_browser::launch::find_executable() else {
            eprintln!("no Chromium-family browser installed; skipping");
            return;
        };
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        let server = Server::new(ServerOptions {
            home,
            cwd: dir.path().to_path_buf(),
            overrides: Vec::new(),
            mcp_servers: Vec::new(),
            doc_worker: her_docs::Launcher::Thread,
        })
        .unwrap();
        let (client, ours) = tokio::io::duplex(8 << 20);
        let (server_reads, server_writes) = tokio::io::split(ours);
        let connection = tokio::spawn(crate::conn::run_connection(
            server.clone(),
            server_reads,
            server_writes,
            NativeProtocol::new(server.clone()),
        ));
        let (client_reads, mut client_writes) = tokio::io::split(client);
        let mut lines = tokio::io::BufReader::new(client_reads).lines();

        // Handshake, then become the host.
        let write = |value: Value| format!("{value}\n");
        client_writes
            .write_all(
                write(json!({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                    "params": {"protocol_version": her_protocol::PROTOCOL_VERSION, "client_info": {"name": "test"}}}))
                .as_bytes(),
            )
            .await
            .unwrap();
        let init: Value = serde_json::from_str(&lines.next_line().await.unwrap().unwrap()).unwrap();
        assert_eq!(init["result"]["capabilities"]["embedded_browser"], json!(true), "{init}");
        assert!(!server.0.browser_pool.has_host());
        client_writes
            .write_all(write(json!({"jsonrpc": "2.0", "id": 2, "method": "browser/host"})).as_bytes())
            .await
            .unwrap();
        let hosted: Value = serde_json::from_str(&lines.next_line().await.unwrap().unwrap()).unwrap();
        assert_eq!(hosted["id"], json!(2), "{hosted}");
        assert!(server.0.browser_pool.has_host());

        // The browser end: a real Chromium behind the relay.
        let launched = ManagedLauncher
            .launch(&LaunchOptions {
                executable: Some(executable),
                user_data_dir: dir.path().join("profile"),
                headless: true,
            })
            .await
            .unwrap();
        let chrome = launched.conn.clone();
        let _child = launched.child;
        let (out_tx, mut out_rx) = tokio::sync::mpsc::unbounded_channel::<Value>();
        let stream = Arc::new(std::sync::atomic::AtomicU64::new(0));
        let current = stream.clone();
        let writer = tokio::spawn(async move {
            while let Some(data) = out_rx.recv().await {
                let stream = current.load(std::sync::atomic::Ordering::Relaxed);
                let line = write(json!({"jsonrpc": "2.0", "method": "browser/cdp", "params": {"stream": stream, "data": data}}));
                if client_writes.write_all(line.as_bytes()).await.is_err() {
                    break;
                }
            }
        });
        let mut events = launched.events;
        let events_tx = out_tx.clone();
        tokio::spawn(async move {
            while let Some(event) = events.recv().await {
                let mut data = json!({"method": event.method, "params": event.params});
                if let Some(session) = event.session {
                    data["sessionId"] = json!(session);
                }
                let _ = events_tx.send(data);
            }
        });
        let seen: Arc<parking_lot::Mutex<Vec<Value>>> = Arc::default();
        let noted = seen.clone();
        let relay = tokio::spawn(async move {
            while let Ok(Some(line)) = lines.next_line().await {
                let message: Value = serde_json::from_str(&line).unwrap();
                if message["method"] != "browser/cdp" {
                    continue;
                }
                stream.store(message["params"]["stream"].as_u64().unwrap(), std::sync::atomic::Ordering::Relaxed);
                let mut command: Value = message["params"]["data"].clone();
                noted.lock().push(command.clone());
                if let Some(params) = command["params"].as_object_mut() {
                    params.remove("herSession");
                }
                let chrome = chrome.clone();
                let out = out_tx.clone();
                tokio::spawn(async move {
                    let session = command["sessionId"].as_str().map(str::to_string);
                    let method = command["method"].as_str().unwrap_or_default().to_string();
                    let reply = match chrome.send(session.as_deref(), &method, command["params"].clone()).await {
                        Ok(result) => json!({"id": command["id"], "result": result}),
                        Err(err) => json!({"id": command["id"], "error": {"code": -32000, "message": err.to_string()}}),
                    };
                    let _ = out.send(reply);
                });
            }
        });

        // The kernel side, as the browser tools use it.
        let pool = server.0.browser_pool.clone();
        let config = BrowserConfig {
            backend: Backend::Embedded,
            headless: Headless::On,
            ..BrowserConfig::default()
        };
        let browser = pool.acquire(&config).await.expect("the embedded browser starts");
        assert!(browser.is_embedded());
        let session = BrowserSession::new(
            browser,
            SessionOptions {
                guard: Arc::new(OpenGuard),
                downloads: dir.path().join("downloads"),
                navigation_timeout: Duration::from_secs(15),
                action_timeout: Duration::from_secs(10),
                label: Some("session-1".into()),
                prefix: "",
                numbers: Default::default(),
                note: None,
            },
        );
        let url = serve_page("<title>Hosted</title><h1>Hello from the host</h1><button>Go</button>").await;
        let report = session.navigate(None, &url::Url::parse(&url).unwrap(), false).await.unwrap();
        assert!(report.text.contains("Hello from the host") && report.text.contains("[ref="), "{}", report.text);
        let created = seen
            .lock()
            .iter()
            .find(|c| c["method"] == "Target.createTarget")
            .cloned()
            .expect("a tab was created");
        assert_eq!(created["params"]["herSession"], json!("session-1"), "{created}");

        // The host leaves (both ends of its connection close): the browser is dead, and the
        // embedded backend has nobody to ask.
        relay.abort();
        writer.abort();
        tokio::time::timeout(Duration::from_secs(5), connection)
            .await
            .expect("the server sees the host leave")
            .unwrap();
        assert!(!pool.has_host());
        let dead = async {
            while !session.is_dead() {
                tokio::time::sleep(Duration::from_millis(20)).await;
            }
        };
        tokio::time::timeout(Duration::from_secs(5), dead).await.expect("the browser is dead");
        let err = pool.acquire(&config).await.err().expect("no host");
        assert!(err.to_string().contains("No desktop app"), "{err}");
    }

    /// Only her's own native messaging host may lend the user's Chrome (D-198): a connection
    /// whose process is not known (or not her) is refused, and the pool has no extension.
    #[tokio::test]
    async fn an_unknown_peer_cannot_claim_to_be_the_extension() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        let server = Server::new(ServerOptions {
            home,
            cwd: dir.path().to_path_buf(),
            overrides: Vec::new(),
            mcp_servers: Vec::new(),
            doc_worker: her_docs::Launcher::Thread,
        })
        .unwrap();
        let (client, ours) = tokio::io::duplex(1 << 20);
        let (server_reads, server_writes) = tokio::io::split(ours);
        tokio::spawn(crate::conn::run_connection(server.clone(), server_reads, server_writes, NativeProtocol::new(server.clone())));
        let (client_reads, mut client_writes) = tokio::io::split(client);
        let mut lines = tokio::io::BufReader::new(client_reads).lines();
        for request in [
            json!({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                "params": {"protocol_version": her_protocol::PROTOCOL_VERSION, "client_info": {"name": "test"}}}),
            json!({"jsonrpc": "2.0", "id": 2, "method": "browser/host", "params": {"kind": "extension"}}),
        ] {
            client_writes.write_all(format!("{request}\n").as_bytes()).await.unwrap();
        }
        let _init = lines.next_line().await.unwrap().unwrap();
        let refused: Value = serde_json::from_str(&lines.next_line().await.unwrap().unwrap()).unwrap();
        assert_eq!(refused["id"], json!(2));
        assert!(refused["error"]["message"].as_str().unwrap().contains("not her's extension host"), "{refused}");
        assert!(!server.0.browser_pool.has_extension() && !server.0.browser_pool.has_host());
        let config = BrowserConfig {
            backend: Backend::Extension,
            ..BrowserConfig::default()
        };
        let err = server.0.browser_pool.acquire(&config).await.err().expect("no extension");
        assert!(err.to_string().contains("extension is not connected"), "{err}");
    }
}
