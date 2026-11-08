//! Load test: many concurrent sessions through every invocation path, scripted by the mock
//! model, with every reply checked. `HER_STRESS_SCALE` multiplies the load (default 1).

mod common;

use common::Demux;
use common::TestEnv;
use common::assert_increasing;
use common::native_init;
use common::native_prompt;
use common::percentile;
use her_rpc::Client;
use her_rpc::Incoming;
use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::io::Write as _;
use std::path::Path;
use std::sync::Arc;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use std::time::Duration;
use std::time::Instant;
use tokio::io::AsyncBufReadExt;
use tokio::io::AsyncWriteExt;
use tokio::net::UnixStream;

fn scale() -> usize {
    std::env::var("HER_STRESS_SCALE")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(1)
        .max(1)
}

#[derive(Default)]
struct Metrics {
    prompts: AtomicU64,
    events: AtomicU64,
    /// Prompt sent → task ended, in milliseconds.
    latency_ms: Mutex<Vec<f64>>,
    /// Mock wrote the chunk → client decoded the event, in microseconds.
    stream_us: Mutex<Vec<f64>>,
}

impl Metrics {
    fn record(&self, started: Instant, events: usize) {
        self.prompts.fetch_add(1, Ordering::Relaxed);
        self.events.fetch_add(events as u64, Ordering::Relaxed);
        self.latency_ms.lock().push(started.elapsed().as_secs_f64() * 1000.0);
    }

    fn stamps(&self, latencies: &[f64]) {
        self.stream_us.lock().extend_from_slice(latencies);
    }

    fn report(&self, phase: &str, wall: Duration) {
        let mut lat = self.latency_ms.lock().clone();
        lat.sort_by(f64::total_cmp);
        let mut stream = self.stream_us.lock().clone();
        stream.sort_by(f64::total_cmp);
        let prompts = self.prompts.load(Ordering::Relaxed);
        let events = self.events.load(Ordering::Relaxed);
        let secs = wall.as_secs_f64();
        let mut err = std::io::stderr().lock();
        let _ = writeln!(
            err,
            "[stress] {phase:<22} prompts {prompts:>5}  {:>7.1} prompts/s  events {events:>7}  {:>8.0} events/s  \
             latency p50 {:>6.1} ms p99 {:>6.1} ms{}",
            prompts as f64 / secs,
            events as f64 / secs,
            percentile(&lat, 0.5),
            percentile(&lat, 0.99),
            if stream.is_empty() {
                String::new()
            } else {
                format!(
                    "  stream p50 {:.0} µs p99 {:.0} µs ({} samples)",
                    percentile(&stream, 0.5),
                    percentile(&stream, 0.99),
                    stream.len()
                )
            }
        );
    }
}

/// A prompt and the reply text it must produce.
fn scenario(tag: &str, n: usize) -> (String, Option<String>) {
    match n % 7 {
        0 => (format!("[[mock:echo]] {tag}"), Some(format!("echo: {tag}"))),
        1 => (
            format!("[[mock:long:200]] {tag}"),
            Some((0..200).map(|i| format!("t{i} ")).collect()),
        ),
        2 => (format!("[[mock:think]] {tag}"), Some("Thought it through.".into())),
        3 => (format!("[[mock:stamp:40]] {tag}"), None),
        4 => (format!("[[mock:files]] {tag}"), Some("files done".into())),
        5 => (format!("[[mock:bash:echo {tag}]]"), Some("bash done".into())),
        _ => (format!("[[mock:parallel]] {tag}"), Some("parallel done".into())),
    }
}

fn check_text(expected: &Option<String>, text: &str, latencies: &[f64], metrics: &Metrics) {
    match expected {
        Some(expected) => assert_eq!(text, expected),
        None => {
            assert_eq!(text.matches('@').count(), 40, "every stamp arrives: {text}");
            metrics.stamps(latencies);
        }
    }
}

async fn wait_for_socket(path: &Path) {
    for _ in 0..400 {
        if UnixStream::connect(path).await.is_ok() {
            return;
        }
        tokio::time::sleep(Duration::from_millis(25)).await;
    }
    panic!("the service did not start");
}

async fn unix_client(path: &Path) -> (Client, her_rpc::IncomingRx) {
    let stream = UnixStream::connect(path).await.unwrap();
    let (read, write) = stream.into_split();
    let (client, rx) = Client::connect(read, write);
    native_init(&client).await;
    (client, rx)
}

/// Native protocol, Unix socket: one connection per session, half of them asking approvals.
async fn phase_unix(env: Arc<TestEnv>, metrics: Arc<Metrics>) {
    let socket = env.home.path().join("run").join("stress.sock");
    let url = format!("unix://{}", socket.display());
    let mut service = env.her(&["serve", "--listen", &url]).spawn().unwrap();
    wait_for_socket(&socket).await;
    let clients = 16 * scale();
    let prompts = 6;
    let mut tasks = Vec::new();
    for c in 0..clients {
        let (env, metrics, socket) = (env.clone(), metrics.clone(), socket.clone());
        tasks.push(tokio::spawn(async move {
            let (client, rx) = unix_client(&socket).await;
            let demux = Demux::spawn(rx);
            let cwd = env.workspace(&format!("unix-{c}"));
            let mode = if c % 2 == 0 { "bypass-permissions" } else { "manual" };
            let created = client
                .request("session/new", json!({"cwd": cwd, "permission_mode": mode}))
                .await
                .unwrap();
            let session = created["session_id"].as_str().unwrap().to_string();
            let mut events = demux.subscribe(&session);
            let mut all = Vec::new();
            for i in 0..prompts {
                let (text, expected) = scenario(&format!("u{c}-{i}"), c + i);
                let started = Instant::now();
                let turn = native_prompt(&client, &mut events, &session, &text, true).await;
                assert_eq!(turn.end["type"], "task_completed", "{:?}", turn.end);
                check_text(&expected, &turn.text, &turn.stamp_us, &metrics);
                metrics.record(started, turn.events.len());
                all.extend(turn.events);
            }
            assert_increasing(&all);
            client.request("session/close", json!({"session_id": session})).await.unwrap();
        }));
    }
    for task in tasks {
        task.await.expect("a unix client failed");
    }
    let shutdown = unix_client(&socket).await.0;
    shutdown.request("server/shutdown", json!({})).await.unwrap();
    assert_eq!(common::exit_code(&mut service).await, 0);
}

/// One session alone on the Unix socket: the streaming latency floor without contention.
async fn phase_probe(env: Arc<TestEnv>, metrics: Arc<Metrics>) {
    let socket = env.home.path().join("run").join("probe.sock");
    let url = format!("unix://{}", socket.display());
    let mut service = env.her(&["serve", "--listen", &url]).spawn().unwrap();
    wait_for_socket(&socket).await;
    let (client, rx) = unix_client(&socket).await;
    let demux = Demux::spawn(rx);
    let created = client
        .request("session/new", json!({"cwd": env.workspace("probe")}))
        .await
        .unwrap();
    let session = created["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);
    for i in 0..5 {
        let started = Instant::now();
        let turn = native_prompt(&client, &mut events, &session, &format!("[[mock:stamp:200:2]] probe {i}"), false).await;
        assert_eq!(turn.end["type"], "task_completed");
        assert_eq!(turn.text.matches('@').count(), 200);
        metrics.stamps(&turn.stamp_us);
        metrics.record(started, turn.events.len());
    }
    client.request("server/shutdown", json!({})).await.unwrap();
    assert_eq!(common::exit_code(&mut service).await, 0);
}

/// Native protocol, stdio: one process, many sessions multiplexed on one connection.
async fn phase_stdio(env: Arc<TestEnv>, metrics: Arc<Metrics>) {
    let (mut child, client, rx) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let demux = Demux::spawn(rx);
    let sessions = 24 * scale();
    let mut tasks = Vec::new();
    for s in 0..sessions {
        let (env, metrics, client, demux) = (env.clone(), metrics.clone(), client.clone(), demux.clone());
        tasks.push(tokio::spawn(async move {
            let cwd = env.workspace(&format!("stdio-{s}"));
            let created = client.request("session/new", json!({"cwd": cwd})).await.unwrap();
            let session = created["session_id"].as_str().unwrap().to_string();
            let mut events = demux.subscribe(&session);
            for i in 0..4 {
                let (text, expected) = scenario(&format!("s{s}-{i}"), s * 3 + i);
                let started = Instant::now();
                let turn = native_prompt(&client, &mut events, &session, &text, true).await;
                assert_eq!(turn.end["type"], "task_completed", "{:?}", turn.end);
                check_text(&expected, &turn.text, &turn.stamp_us, &metrics);
                metrics.record(started, turn.events.len());
            }
        }));
    }
    for task in tasks {
        task.await.expect("a stdio session failed");
    }
    drop((client, demux));
    assert_eq!(common::exit_code(&mut child).await, 0);
}

/// ACP over stdio: several editor processes at once, sessions one after another in each.
async fn phase_acp(env: Arc<TestEnv>, metrics: Arc<Metrics>) {
    let processes = 6 * scale();
    let mut tasks = Vec::new();
    for p in 0..processes {
        let (env, metrics) = (env.clone(), metrics.clone());
        tasks.push(tokio::spawn(async move {
            let (mut child, client, mut rx) = env.spawn_rpc(&["acp"]);
            client
                .request("initialize", json!({"protocolVersion": 1, "clientCapabilities": {}}))
                .await
                .unwrap();
            for s in 0..2 {
                let cwd = env.workspace(&format!("acp-{p}-{s}"));
                let created = client
                    .request("session/new", json!({"cwd": cwd, "mcpServers": []}))
                    .await
                    .unwrap();
                let session = created["sessionId"].as_str().unwrap().to_string();
                for i in 0..4 {
                    let (text, expected) = scenario(&format!("a{p}-{s}-{i}"), p + s + i);
                    let started = Instant::now();
                    let request = {
                        let client = client.clone();
                        let params = json!({"sessionId": session, "prompt": [{"type": "text", "text": text}]});
                        tokio::spawn(async move { client.request("session/prompt", params).await })
                    };
                    let result = request.await.unwrap().expect("prompt");
                    assert_eq!(result["stopReason"], "end_turn");
                    let mut reply = String::new();
                    let mut events = 0;
                    let mut latencies = Vec::new();
                    while let Ok(incoming) = rx.try_recv() {
                        events += 1;
                        if let Incoming::Request { id, .. } = &incoming {
                            client.respond(id, json!({"outcome": {"outcome": "selected", "optionId": "approve"}}));
                        }
                        let update = &incoming.params()["update"];
                        if update["sessionUpdate"] == "agent_message_chunk" {
                            let text = update["content"]["text"].as_str().unwrap_or("");
                            if let Incoming::Notification { received, .. } = &incoming {
                                common::stamp_latencies(text, *received, &mut latencies);
                            }
                            reply.push_str(text);
                        }
                    }
                    check_text(&expected, &reply, &latencies, &metrics);
                    metrics.record(started, events);
                }
                client.request("session/close", json!({"sessionId": session})).await.unwrap();
            }
            drop(client);
            assert_eq!(common::exit_code(&mut child).await, 0);
        }));
    }
    for task in tasks {
        task.await.expect("an ACP client failed");
    }
}

/// `her exec --output stream-json`: many short-lived processes in parallel.
async fn phase_exec(env: Arc<TestEnv>, metrics: Arc<Metrics>) {
    let runs = 12 * scale();
    let mut tasks = Vec::new();
    for r in 0..runs {
        let (env, metrics) = (env.clone(), metrics.clone());
        tasks.push(tokio::spawn(async move {
            let cwd = env.workspace(&format!("exec-{r}"));
            let (text, expected) = scenario(&format!("e{r}"), r);
            let started = Instant::now();
            let output = env.her_in(&cwd, &["exec", "--output", "stream-json", &text]).output().await.unwrap();
            assert_eq!(output.status.code(), Some(0), "{}", String::from_utf8_lossy(&output.stderr));
            let events: Vec<Value> = String::from_utf8_lossy(&output.stdout)
                .lines()
                .map(|l| serde_json::from_str(l).unwrap())
                .collect();
            assert_increasing(&events);
            let reply: String = events
                .iter()
                .filter(|e| e["type"] == "message_delta" && e["delta"]["type"] == "text")
                .map(|e| e["delta"]["text"].as_str().unwrap().to_string())
                .collect::<Vec<_>>()
                .concat();
            // Lines are read after the process exits, so no stamp latencies here.
            check_text(&expected, &reply, &[], &metrics);
            metrics.record(started, events.len());
        }));
    }
    for task in tasks {
        task.await.expect("an exec run failed");
    }
}

/// A connection that stops reading is detached; a connection that reads slowly gets merged
/// deltas with the same text; the session and the other connections are unaffected.
async fn phase_slow_consumer(env: Arc<TestEnv>) {
    let socket = env.home.path().join("run").join("slow.sock");
    let url = format!("unix://{}", socket.display());
    let mut service = env
        .her(&["-c", "server.queue_hard_bytes=65536", "serve", "--listen", &url])
        .spawn()
        .unwrap();
    wait_for_socket(&socket).await;
    let (owner, rx) = unix_client(&socket).await;
    let demux = Demux::spawn(rx);
    let cwd = env.workspace("slow");
    let created = owner.request("session/new", json!({"cwd": cwd})).await.unwrap();
    let session = created["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);

    // The stalled reader: attaches, then reads nothing for a while.
    let mut stalled = UnixStream::connect(&socket).await.unwrap();
    let attach = |id: u32| {
        format!(
            "{}\n{}\n",
            json!({"jsonrpc": "2.0", "id": id, "method": "initialize", "params": {"protocol_version": 2}}),
            json!({"jsonrpc": "2.0", "id": id + 1, "method": "session/attach", "params": {"session_id": session}})
        )
    };
    stalled.write_all(attach(1).as_bytes()).await.unwrap();
    // The slow reader: reads a little at a time, so its queue backs up and deltas merge.
    let mut slow = UnixStream::connect(&socket).await.unwrap();
    slow.write_all(attach(1).as_bytes()).await.unwrap();
    tokio::time::sleep(Duration::from_millis(200)).await;
    let slow_task = tokio::spawn(async move {
        let mut lines = tokio::io::BufReader::with_capacity(4096, slow).lines();
        let mut text = String::new();
        let mut deltas = 0usize;
        while let Some(line) = lines.next_line().await.unwrap() {
            let message: Value = serde_json::from_str(&line).unwrap();
            let event = &message["params"]["event"];
            if event["type"] == "message_delta" && event["delta"]["type"] == "text" {
                deltas += 1;
                text.push_str(event["delta"]["text"].as_str().unwrap());
                if deltas.is_multiple_of(256) {
                    tokio::time::sleep(Duration::from_millis(1)).await;
                }
            }
            if event["type"] == "task_completed" {
                return (text, deltas);
            }
        }
        panic!("the slow reader lost its connection");
    });

    let expected: String = "0123456789abcdef".repeat(64 * 512);
    let started = Instant::now();
    let turn = native_prompt(&owner, &mut events, &session, "[[mock:big:512]] flood", false).await;
    let owner_time = started.elapsed();
    assert_eq!(turn.end["type"], "task_completed");
    assert_eq!(turn.text, expected, "the reading client gets everything");
    let generated = turn.events.iter().filter(|e| e["type"] == "message_delta").count();

    let (slow_text, slow_deltas) = tokio::time::timeout(Duration::from_secs(60), slow_task).await.unwrap().unwrap();
    assert_eq!(slow_text, expected, "merged deltas keep the text intact");

    // Now drain the stalled connection: it must have been detached for falling behind.
    let mut lines = tokio::io::BufReader::new(stalled).lines();
    let mut detached = None;
    while let Ok(Some(line)) = tokio::time::timeout(Duration::from_secs(5), lines.next_line()).await.unwrap() {
        let message: Value = serde_json::from_str(&line).unwrap();
        if message["method"] == "session/detached" {
            detached = Some(message["params"]["reason"].clone());
            break;
        }
    }
    assert_eq!(detached, Some(json!("overflow")), "the stalled connection is detached");
    common::SchemaCheck::new(&["SessionDetachedParams"])
        .check("SessionDetachedParams", &json!({"session_id": session, "reason": "overflow", "last_seq": 1}));
    let mut err = std::io::stderr().lock();
    let _ = writeln!(
        err,
        "[stress] slow consumer           owner finished 512 KiB in {:.0} ms ({generated} deltas); \
         slow reader got {slow_deltas} merged deltas; stalled reader detached",
        owner_time.as_secs_f64() * 1000.0
    );
    drop(err);

    owner.request("server/shutdown", json!({})).await.unwrap();
    assert_eq!(common::exit_code(&mut service).await, 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 8)]
async fn stress_every_path_under_load() {
    let env = Arc::new(TestEnv::new("bypass-permissions", "[tools.limits]\nmax_parallel = 4\n[server]\nmax_sessions = 4096\n").await);
    let mut err = std::io::stderr().lock();
    let _ = writeln!(err, "[stress] scale {}", scale());
    drop(err);
    let total = Instant::now();

    type Phase = fn(Arc<TestEnv>, Arc<Metrics>) -> std::pin::Pin<Box<dyn std::future::Future<Output = ()> + Send>>;
    let phases: [(&str, Phase); 5] = [
        ("latency probe (1 sess)", |e, m| Box::pin(phase_probe(e, m))),
        ("native / unix socket", |e, m| Box::pin(phase_unix(e, m))),
        ("native / stdio mux", |e, m| Box::pin(phase_stdio(e, m))),
        ("acp / stdio", |e, m| Box::pin(phase_acp(e, m))),
        ("exec stream-json", |e, m| Box::pin(phase_exec(e, m))),
    ];
    // HER_STRESS_ONLY=slow runs just the slow-consumer phase (for debugging).
    if std::env::var("HER_STRESS_ONLY").as_deref() == Ok("slow") {
        phase_slow_consumer(env.clone()).await;
        return;
    }
    let all = Arc::new(Metrics::default());
    for (name, phase) in phases {
        let metrics = Arc::new(Metrics::default());
        let started = Instant::now();
        phase(env.clone(), metrics.clone()).await;
        metrics.report(name, started.elapsed());
        all.prompts.fetch_add(metrics.prompts.load(Ordering::Relaxed), Ordering::Relaxed);
        all.events.fetch_add(metrics.events.load(Ordering::Relaxed), Ordering::Relaxed);
        all.latency_ms.lock().extend(metrics.latency_ms.lock().iter());
        all.stream_us.lock().extend(metrics.stream_us.lock().iter());
    }

    // All four paths at the same time.
    let concurrent = Arc::new(Metrics::default());
    let started = Instant::now();
    let (a, b, c, d) = tokio::join!(
        tokio::spawn(phase_unix(env.clone(), concurrent.clone())),
        tokio::spawn(phase_stdio(env.clone(), concurrent.clone())),
        tokio::spawn(phase_acp(env.clone(), concurrent.clone())),
        tokio::spawn(phase_exec(env.clone(), concurrent.clone())),
    );
    a.unwrap();
    b.unwrap();
    c.unwrap();
    d.unwrap();
    concurrent.report("all paths at once", started.elapsed());

    phase_slow_consumer(env.clone()).await;

    all.prompts.fetch_add(concurrent.prompts.load(Ordering::Relaxed), Ordering::Relaxed);
    all.events.fetch_add(concurrent.events.load(Ordering::Relaxed), Ordering::Relaxed);
    all.latency_ms.lock().extend(concurrent.latency_ms.lock().iter());
    all.stream_us.lock().extend(concurrent.stream_us.lock().iter());
    all.report("total", total.elapsed());
    let mut err = std::io::stderr().lock();
    let _ = writeln!(
        err,
        "[stress] mock served {} requests, {} chunks, {} injected errors",
        env.mock.stats().requests.load(Ordering::Relaxed),
        env.mock.stats().chunks.load(Ordering::Relaxed),
        env.mock.stats().errors_served.load(Ordering::Relaxed)
    );
}
