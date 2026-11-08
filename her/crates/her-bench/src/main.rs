//! `her-load`: load test of `her serve --listen`.
//!
//! Starts a mock model gateway with realistic latency, runs the real `her` binary as a Unix
//! socket service, and drives many sessions at once over the native protocol. While it runs,
//! the service process is sampled every 10 ms (memory, CPU time, threads).
//!
//! Phases:
//! 1. solo: one session runs one task alone (the uncontended baseline);
//! 2. sequential: sessions created one after another on an idle service;
//! 3. rounds: create N sessions at once, run their tasks at once, close them all. Later
//!    rounds show whether memory comes back.
//!
//! `--scenario` picks a harder workload instead (see the `stress` module): long sessions,
//! sub-agent fan-out, huge outputs, many viewers, interrupts, and startup over a large data
//! directory.
//!
//! ```text
//! cargo build --release -p her-cli -p her-bench
//! target/release/her-load --sessions 200
//! target/release/her-load --scenario long --sessions 20 --tasks 40
//! ```

mod probe;
mod stress;

use clap::Parser;
use her_mock::MockOptions;
use her_mock::MockServer;
use her_rpc::Client;
use her_rpc::Incoming;
use her_rpc::IncomingRx;
use parking_lot::Mutex;
use probe::Probe;
use probe::Sample;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;
use std::io::Write;
use std::path::Path;
use std::path::PathBuf;
use std::process::Stdio;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use tokio::net::UnixStream;
use tokio::sync::mpsc;

const WAIT: Duration = Duration::from_secs(300);

#[derive(Parser, Clone)]
#[command(name = "her-load", about = "Load test of her serve: many concurrent sessions against a mock gateway")]
struct Args {
    /// The her binary to test
    #[arg(long, default_value = "target/release/her")]
    her: PathBuf,
    /// Sessions created and run at once in each round
    #[arg(long, default_value_t = 200)]
    sessions: usize,
    /// Connections the sessions share (default: one connection per session)
    #[arg(long)]
    conns: Option<usize>,
    /// Tasks each session runs in a round
    #[arg(long, default_value_t = 1)]
    tasks: usize,
    /// Rounds of create, run and close
    #[arg(long, default_value_t = 2)]
    rounds: usize,
    /// Sessions created one after another on the idle service
    #[arg(long, default_value_t = 20)]
    sequential: usize,
    /// Gateway time to first token, in milliseconds
    #[arg(long, default_value_t = 300)]
    ttft_ms: u64,
    /// Gateway pause between chunks, in milliseconds
    #[arg(long, default_value_t = 15)]
    chunk_ms: u64,
    /// Settings for the service, as with `her -c key=value`
    #[arg(short = 'c', long = "config")]
    overrides: Vec<String>,
    /// Also measure the live heap with `vmmap` at quiet points (macOS; pauses the service)
    #[arg(long)]
    heap: bool,
    /// Also measure churn: this many seconds of creating and closing sessions from 8 clients
    #[arg(long, default_value_t = 0.0)]
    churn: f64,
    /// Also measure bursts: this many times, create all sessions at once and close them
    #[arg(long, default_value_t = 0)]
    bursts: usize,
    /// Environment for the service, KEY=VALUE (e.g. MallocStackLogging=1)
    #[arg(long = "service-env")]
    service_env: Vec<String>,
    /// With --heap: also save `malloc_history -allByCount` at each quiet point to this directory
    /// (needs --service-env MallocStackLogging=1)
    #[arg(long)]
    inspect: Option<PathBuf>,
    /// After the rounds, wait this many seconds and sample memory again (idle timeouts)
    #[arg(long, default_value_t = 0.0)]
    linger: f64,
    /// Keep the service's data directory (logs, session files) and print where it is
    #[arg(long)]
    keep: bool,
    /// Also write the report as JSON to this file
    #[arg(long)]
    json: Option<PathBuf>,
    /// The workload: rounds (the default), long, fanout, bigout, viewers, interrupt, startup,
    /// churn or exec-churn
    #[arg(long, default_value = "rounds")]
    scenario: String,
    /// fanout: sub-agents each main session starts; viewers: viewers per session
    #[arg(long, default_value_t = 8)]
    agents: usize,
    /// bigout: megabytes of command output and file; startup: sessions on disk (×100)
    #[arg(long, default_value_t = 8)]
    size: usize,
    /// churn, exec-churn: tool turns in the one task of each session
    #[arg(long, default_value_t = 3000)]
    turns: usize,
}

fn main() -> std::io::Result<()> {
    let args = Args::parse();
    tokio::runtime::Builder::new_multi_thread()
        .worker_threads(8)
        .enable_all()
        .build()?
        .block_on(run(args))
}

/// Routes notifications to per-session channels, keeping those that arrive before anyone
/// subscribed.
#[derive(Clone)]
struct Demux(Arc<Mutex<DemuxState>>);

#[derive(Default)]
struct DemuxState {
    senders: HashMap<String, mpsc::UnboundedSender<Incoming>>,
    buffered: HashMap<String, Vec<Incoming>>,
}

impl Demux {
    fn spawn(mut rx: IncomingRx) -> Demux {
        let demux = Demux(Arc::default());
        let state = demux.0.clone();
        tokio::spawn(async move {
            while let Some(incoming) = rx.recv().await {
                let Some(session) = incoming.params().get("session_id").and_then(Value::as_str) else {
                    continue;
                };
                let mut state = state.lock();
                match state.senders.get(session) {
                    Some(tx) => {
                        let _ = tx.send(incoming);
                    }
                    None => state.buffered.entry(session.to_string()).or_default().push(incoming),
                }
            }
            state.lock().senders.clear();
        });
        demux
    }

    fn subscribe(&self, session: &str) -> mpsc::UnboundedReceiver<Incoming> {
        let (tx, rx) = mpsc::unbounded_channel();
        let mut state = self.0.lock();
        for incoming in state.buffered.remove(session).unwrap_or_default() {
            let _ = tx.send(incoming);
        }
        state.senders.insert(session.to_string(), tx);
        rx
    }

    fn forget(&self, session: &str) {
        let mut state = self.0.lock();
        state.senders.remove(session);
        state.buffered.remove(session);
    }
}

struct Conn {
    client: Client,
    demux: Demux,
}

/// Connections refused because the listen backlog was full (macOS caps it at 128); the
/// client waits briefly and tries again, as a real client would.
static REFUSED: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

async fn connect(socket: &Path) -> Conn {
    let mut attempts = 0;
    let stream = loop {
        match UnixStream::connect(socket).await {
            Ok(stream) => break stream,
            Err(err) if err.kind() == std::io::ErrorKind::ConnectionRefused && attempts < 200 => {
                attempts += 1;
                REFUSED.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                tokio::time::sleep(Duration::from_millis(2)).await;
            }
            Err(err) => panic!("connect to the service: {err}"),
        }
    };
    let (read, write) = stream.into_split();
    let (client, rx) = Client::connect(read, write);
    let result = client
        .request("initialize", json!({"protocol_version": 2, "client_info": {"name": "her-load"}}))
        .await
        .expect("initialize");
    assert_eq!(result["protocol_version"], 2);
    Conn {
        client,
        demux: Demux::spawn(rx),
    }
}

/// A private HER_HOME whose default model is the mock, and workspaces to run in.
struct Env {
    _dir: tempfile::TempDir,
    home: PathBuf,
    work: PathBuf,
    socket: PathBuf,
}

impl Env {
    fn new(mock: &MockServer) -> Env {
        Env::with_config(mock, "")
    }

    /// Like `new`, with more lines for config.toml.
    fn with_config(mock: &MockServer, extra: &str) -> Env {
        let dir = tempfile::Builder::new().prefix("her-load-").tempdir().expect("temp dir");
        let home = dir.path().join("home");
        let work = dir.path().join("work");
        std::fs::create_dir_all(&home).unwrap();
        std::fs::create_dir_all(&work).unwrap();
        std::fs::write(home.join("models.toml"), mock.models_toml()).unwrap();
        std::fs::write(
            home.join("config.toml"),
            format!(
                "model = \"mock/mock-1\"\npermission_mode = \"bypass-permissions\"\n\n\
                 [server]\nmax_sessions = 100000\nidle_session_secs = 0\n{extra}"
            ),
        )
        .unwrap();
        let socket = dir.path().join("s.sock");
        Env {
            _dir: dir,
            home,
            work,
            socket,
        }
    }

    /// A small project: a git checkout with an instruction file, 20 KB of source and a README.
    fn workspace(&self, name: &str) -> PathBuf {
        let dir = self.work.join(name);
        std::fs::create_dir_all(dir.join(".git")).unwrap();
        std::fs::create_dir_all(dir.join("src")).unwrap();
        std::fs::write(dir.join(".git/HEAD"), "ref: refs/heads/main\n").unwrap();
        std::fs::write(dir.join("AGENTS.md"), "Use 4-space indentation. Run the tests before finishing.\n").unwrap();
        let mut source = String::new();
        let mut n = 0;
        while source.len() < 20_000 {
            source.push_str(&format!(
                "/// Returns the {n}th value of the series, clamped to the limit.\npub fn value_{n}(input: u64, limit: u64) -> u64 {{\n    let scaled = input.saturating_mul({n} + 1);\n    scaled.min(limit)\n}}\n\n"
            ));
            n += 1;
        }
        std::fs::write(dir.join("src/lib.rs"), source).unwrap();
        std::fs::write(
            dir.join("README.md"),
            "# 示例项目\n\n这个项目演示数列计算。`value_N` 返回第 N 项，结果不超过上限。\n\n## Build\n\n    cargo build\n".repeat(12),
        )
        .unwrap();
        std::fs::canonicalize(dir).unwrap()
    }
}

#[derive(Debug, Default, Clone)]
struct TaskOutcome {
    ok: bool,
    end: String,
    /// Prompt sent → first message delta, milliseconds.
    first_delta_ms: f64,
    total_ms: f64,
    events: usize,
}

async fn run_task(conn: &Conn, events: &mut mpsc::UnboundedReceiver<Incoming>, session: &str, label: &str) -> TaskOutcome {
    let text = format!("[[mock:agent:{label}]] 请检查 {label} 的代码并写一份报告");
    run_prompt(conn, events, session, &text).await
}

/// Sends a prompt and waits for its task to end.
async fn run_prompt(conn: &Conn, events: &mut mpsc::UnboundedReceiver<Incoming>, session: &str, text: &str) -> TaskOutcome {
    let started = Instant::now();
    let op = json!({"session_id": session, "op": {"type": "prompt", "input": [{"type": "text", "text": text}]}});
    let mut outcome = TaskOutcome::default();
    if let Err(err) = conn.client.request("session/op", op).await {
        outcome.end = format!("prompt rejected: {err}");
        return outcome;
    }
    loop {
        let incoming = match tokio::time::timeout(WAIT, events.recv()).await {
            Ok(Some(incoming)) => incoming,
            Ok(None) => {
                outcome.end = "connection closed".into();
                break;
            }
            Err(_) => {
                outcome.end = "timed out".into();
                break;
            }
        };
        if incoming.method() != "session/event" {
            continue;
        }
        let event = &incoming.params()["event"];
        let kind = event["type"].as_str().unwrap_or("");
        outcome.events += 1;
        if kind == "message_delta" && outcome.first_delta_ms == 0.0 {
            outcome.first_delta_ms = started.elapsed().as_secs_f64() * 1e3;
        }
        if matches!(kind, "task_completed" | "task_failed" | "task_interrupted") {
            outcome.ok = kind == "task_completed";
            outcome.end = if outcome.ok {
                kind.to_string()
            } else {
                format!("{kind}: {}", event["error"]["message"].as_str().unwrap_or(""))
            };
            break;
        }
    }
    outcome.total_ms = started.elapsed().as_secs_f64() * 1e3;
    outcome
}

/// Percentiles and extremes of a list of milliseconds.
#[derive(Debug, Default, Clone, Copy)]
struct Dist {
    n: usize,
    p50: f64,
    p90: f64,
    p99: f64,
    max: f64,
    mean: f64,
}

impl Dist {
    fn of(values: &[f64]) -> Dist {
        let mut sorted = values.to_vec();
        sorted.sort_by(f64::total_cmp);
        let at = |p: f64| {
            if sorted.is_empty() {
                0.0
            } else {
                sorted[(((sorted.len() - 1) as f64) * p).round() as usize]
            }
        };
        Dist {
            n: sorted.len(),
            p50: at(0.5),
            p90: at(0.9),
            p99: at(0.99),
            max: sorted.last().copied().unwrap_or(0.0),
            mean: if sorted.is_empty() { 0.0 } else { sorted.iter().sum::<f64>() / sorted.len() as f64 },
        }
    }

    fn line(&self, label: &str) -> String {
        format!(
            "  {label:<34} n={:<5} p50 {:>9.2}  p90 {:>9.2}  p99 {:>9.2}  max {:>9.2}  mean {:>9.2} ms",
            self.n, self.p50, self.p90, self.p99, self.max, self.mean
        )
    }

    fn json(&self) -> Value {
        json!({"n": self.n, "p50": self.p50, "p90": self.p90, "p99": self.p99, "max": self.max, "mean": self.mean})
    }
}

fn mib(bytes: u64) -> f64 {
    bytes as f64 / 1048576.0
}

fn mem_line(label: &str, s: Sample) -> String {
    format!(
        "  {label:<34} footprint {:>8.1} MiB  rss {:>8.1} MiB  threads {:>4}  fds {:>5}",
        mib(s.footprint),
        mib(s.rss),
        s.threads,
        s.fds
    )
}

fn mem_json(s: Sample) -> Value {
    json!({"footprint_mib": mib(s.footprint), "rss_mib": mib(s.rss), "threads": s.threads, "fds": s.fds})
}

async fn settle(probe: &Probe) -> Sample {
    tokio::time::sleep(Duration::from_millis(300)).await;
    probe.now()
}

/// The live heap at a quiet point, when `--heap` is on.
async fn heap_at(args: &Args, pid: i32, label: &str, lines: &mut Vec<String>) -> Value {
    if !args.heap {
        return Value::Null;
    }
    if let Some(dir) = &args.inspect {
        std::fs::create_dir_all(dir).unwrap();
        let file = dir.join(format!("{}.txt", label.replace(' ', "-")));
        let output = std::process::Command::new("malloc_history")
            .arg(pid.to_string())
            .arg("-allByCount")
            .output()
            .expect("malloc_history");
        std::fs::write(&file, &output.stdout).unwrap();
        lines.push(format!("  saved malloc_history to {}", file.display()));
    }
    let Some(heap) = tokio::task::spawn_blocking(move || probe::heap(pid)).await.unwrap() else {
        return Value::Null;
    };
    lines.push(format!(
        "  heap: {label:<28} allocated {:>8.1} MiB in {:>8} blocks, malloc dirty {:>8.1} MiB",
        mib(heap.allocated),
        heap.count,
        mib(heap.dirty)
    ));
    json!({"allocated_mib": mib(heap.allocated), "blocks": heap.count, "dirty_mib": mib(heap.dirty)})
}

struct Round {
    report: Value,
    lines: Vec<String>,
}

/// Starts `her serve` on the environment's socket; returns it, its pid and the milliseconds
/// from spawn until the socket accepted a connection.
async fn start_service(args: &Args, env: &Env) -> std::io::Result<(tokio::process::Child, i32, f64)> {
    let _ = std::fs::remove_file(&env.socket);
    let spawned = Instant::now();
    let mut command = tokio::process::Command::new(&args.her);
    command
        .arg("serve")
        .arg("--listen")
        .arg(format!("unix://{}", env.socket.display()));
    for item in &args.overrides {
        command.arg("-c").arg(item);
    }
    for item in &args.service_env {
        if let Some((key, value)) = item.split_once('=') {
            command.env(key, value);
        }
    }
    let service = command
        .current_dir(&env.work)
        .env("HER_HOME", &env.home)
        .env_remove("HER_MODEL")
        .env_remove("HER_PERMISSION_MODE")
        .env_remove("HER_APPROVAL")
        .env_remove("HER_LOG")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::inherit())
        .kill_on_drop(true)
        .spawn()?;
    let pid = service.id().expect("service pid") as i32;
    while UnixStream::connect(&env.socket).await.is_err() {
        tokio::time::sleep(Duration::from_micros(500)).await;
        assert!(spawned.elapsed() < Duration::from_secs(10), "the service did not start");
    }
    let ready_ms = spawned.elapsed().as_secs_f64() * 1e3;
    Ok((service, pid, ready_ms))
}

/// Asks the service to shut down; returns the milliseconds until it exited and its exit code.
async fn stop_service(env: &Env, service: &mut tokio::process::Child) -> std::io::Result<(f64, Option<i32>)> {
    let started = Instant::now();
    let conn = connect(&env.socket).await;
    let _ = conn.client.request("server/shutdown", json!({})).await;
    let status = tokio::time::timeout(WAIT, service.wait()).await.expect("shutdown timed out")?;
    Ok((started.elapsed().as_secs_f64() * 1e3, status.code()))
}

async fn run(args: Args) -> std::io::Result<()> {
    let mut args = args;
    args.her = std::fs::canonicalize(&args.her)?;
    if args.scenario != "rounds" {
        return stress::run(args).await;
    }
    let options = MockOptions {
        ttft: Duration::from_millis(args.ttft_ms),
        chunk_delay: Duration::from_millis(args.chunk_ms),
    };
    let mock = MockServer::bind_with("127.0.0.1:0", options).await?;
    let env = Arc::new(Env::new(&mock));
    let mut stdout = std::io::stdout();
    if args.keep {
        writeln!(stdout, "data directory: {}", env.home.display())?;
    }
    let sessions = args.sessions;
    let conns = args.conns.unwrap_or(sessions).clamp(1, sessions.max(1));
    writeln!(
        stdout,
        "her-load: {} sessions over {} connections, {} task(s) each, {} round(s); gateway ttft {} ms, {} ms per chunk",
        sessions, conns, args.tasks, args.rounds, args.ttft_ms, args.chunk_ms
    )?;
    // Workspaces are made up front so that file creation is not part of any measurement.
    let workspaces: Arc<Vec<PathBuf>> =
        Arc::new((0..sessions.max(args.sequential).max(1)).map(|i| env.workspace(&format!("ws{i}"))).collect());

    let (mut service, pid, ready_ms) = start_service(&args, &env).await?;
    let probe = Probe::start(pid, Duration::from_millis(10));
    let idle = settle(&probe).await;
    let mut heap_lines = Vec::new();
    let heap_idle = heap_at(&args, pid, "idle service", &mut heap_lines).await;
    let mut out = vec![
        "startup".to_string(),
        format!("  spawn → socket ready              {ready_ms:>9.2} ms"),
        mem_line("idle service", idle),
    ];
    out.extend(heap_lines);

    // 1. One session, one task, nothing else running.
    let solo_conn = connect(&env.socket).await;
    let created = solo_conn
        .client
        .request("session/new", json!({"cwd": workspaces[0]}))
        .await
        .expect("session/new");
    let solo_id = created["session_id"].as_str().unwrap().to_string();
    let mut solo_events = solo_conn.demux.subscribe(&solo_id);
    let solo = run_task(&solo_conn, &mut solo_events, &solo_id, "solo").await;
    assert!(solo.ok, "the solo task failed: {}", solo.end);
    solo_conn
        .client
        .request("session/close", json!({"session_id": solo_id}))
        .await
        .expect("close");
    out.push("solo task (uncontended)".into());
    out.push(format!(
        "  prompt → first delta {:>9.2} ms (gateway ttft {} ms)   task {:>9.2} ms   {} events",
        solo.first_delta_ms, args.ttft_ms, solo.total_ms, solo.events
    ));

    // 2. Sessions created one after another on an idle service.
    let mut sequential = Vec::new();
    for i in 0..args.sequential {
        let started = Instant::now();
        let created = solo_conn
            .client
            .request("session/new", json!({"cwd": workspaces[i]}))
            .await
            .expect("session/new");
        sequential.push(started.elapsed().as_secs_f64() * 1e3);
        let id = created["session_id"].as_str().unwrap();
        solo_conn.client.request("session/close", json!({"session_id": id})).await.expect("close");
        solo_conn.demux.forget(id);
    }
    let sequential = Dist::of(&sequential);
    out.push("sequential session/new (idle service)".into());
    out.push(sequential.line("session/new"));
    drop(solo_events);
    drop(solo_conn);
    let after_warmup = settle(&probe).await;
    out.push(mem_line("after warm-up", after_warmup));

    let mut churn = Value::Null;
    if args.churn > 0.0 {
        let (rate, dist, cpu_per) = run_churn(&env, &workspaces, &probe, args.churn).await;
        out.push(format!("churn: create + close for {:.0} s from 8 clients", args.churn));
        out.push(format!("  {rate:.0} sessions/s, server CPU {cpu_per:.3} ms per session"));
        out.push(dist.line("session/new under churn"));
        out.push(mem_line("after churn", settle(&probe).await));
        churn = json!({"rate": rate, "new": dist.json(), "cpu_ms_per_session": cpu_per});
    }

    let mut bursts = Value::Null;
    if args.bursts > 0 {
        let (dist, medians, cpu_per) = run_bursts(&env, &workspaces, &probe, sessions, conns, args.bursts).await;
        out.push(format!("bursts: {} × ({sessions} × session/new at once, then close)", args.bursts));
        out.push(dist.line("session/new in bursts"));
        let mut sorted = medians.clone();
        sorted.sort_by(f64::total_cmp);
        out.push(format!(
            "  per-burst p50: min {:.2}  median {:.2}  max {:.2} ms; server CPU {cpu_per:.3} ms per session",
            sorted[0],
            sorted[sorted.len() / 2],
            sorted[sorted.len() - 1]
        ));
        bursts = json!({"new": dist.json(), "burst_p50s": medians, "cpu_ms_per_session": cpu_per});
    }

    let mut rounds = Vec::new();
    for round in 0..args.rounds {
        let result = run_round(&args, &env, &workspaces, &probe, pid, conns, round, &solo).await;
        out.push(format!("round {} ({} sessions)", round + 1, sessions));
        out.extend(result.lines);
        rounds.push(result.report);
    }

    if args.linger > 0.0 {
        tokio::time::sleep(Duration::from_secs_f64(args.linger)).await;
        out.push(format!("after {:.0} s idle", args.linger));
        out.push(mem_line("service", probe.now()));
        let mut heap_lines = Vec::new();
        heap_at(&args, pid, "after idle", &mut heap_lines).await;
        out.extend(heap_lines);
    }
    let lifetime = probe.now();
    out.push(format!("  lifetime peak footprint            {:>9.1} MiB", mib(lifetime.peak_footprint)));
    let (shutdown_ms, code) = stop_service(&env, &mut service).await?;
    probe.stop();
    let stats = mock.stats();
    out.push("shutdown".into());
    out.push(format!(
        "  server/shutdown → exit            {shutdown_ms:>9.2} ms   exit code {code:?}"
    ));
    out.push(format!(
        "  gateway served {} requests, {} chunks, {} errors",
        stats.requests.load(std::sync::atomic::Ordering::Relaxed),
        stats.chunks.load(std::sync::atomic::Ordering::Relaxed),
        stats.errors_served.load(std::sync::atomic::Ordering::Relaxed)
    ));
    for line in &out {
        writeln!(stdout, "{line}")?;
    }
    if args.keep {
        // Leak the directory on purpose.
        std::mem::forget(Arc::try_unwrap(env).ok().map(|env| env._dir));
    }
    if let Some(path) = &args.json {
        let report = json!({
            "sessions": sessions, "conns": conns, "tasks": args.tasks, "ttft_ms": args.ttft_ms,
            "chunk_ms": args.chunk_ms, "ready_ms": ready_ms, "idle": mem_json(idle), "idle_heap": heap_idle,
            "solo": {"first_delta_ms": solo.first_delta_ms, "task_ms": solo.total_ms, "events": solo.events},
            "sequential_new": sequential.json(), "after_warmup": mem_json(after_warmup),
            "rounds": rounds, "churn": churn, "bursts": bursts, "shutdown_ms": shutdown_ms, "lifetime_peak_footprint_mib": mib(lifetime.peak_footprint),
        });
        std::fs::write(path, serde_json::to_string_pretty(&report).unwrap())?;
    }
    Ok(())
}

#[allow(clippy::too_many_arguments)]
async fn run_round(
    args: &Args,
    env: &Arc<Env>,
    workspaces: &Arc<Vec<PathBuf>>,
    probe: &Probe,
    pid: i32,
    conns: usize,
    round: usize,
    solo: &TaskOutcome,
) -> Round {
    let sessions = args.sessions;
    let mut lines = Vec::new();
    let before = probe.now();

    // Connections.
    let started = Instant::now();
    let mut connecting = Vec::new();
    for _ in 0..conns {
        let socket = env.socket.clone();
        connecting.push(tokio::spawn(async move {
            let at = Instant::now();
            let conn = connect(&socket).await;
            (conn, at.elapsed().as_secs_f64() * 1e3)
        }));
    }
    let mut pool = Vec::new();
    let mut connect_ms = Vec::new();
    for task in connecting {
        let (conn, ms) = task.await.unwrap();
        pool.push(Arc::new(conn));
        connect_ms.push(ms);
    }
    let connect_all_ms = started.elapsed().as_secs_f64() * 1e3;
    let connected = settle(probe).await;

    // All sessions at once.
    let t_create = probe.elapsed();
    let cpu_before_create = probe.now().cpu_ns;
    let burst = Instant::now();
    let mut creating = Vec::new();
    for i in 0..sessions {
        let conn = pool[i % conns].clone();
        let cwd = workspaces[i].clone();
        creating.push(tokio::spawn(async move {
            let at = Instant::now();
            let created = conn.client.request("session/new", json!({"cwd": cwd})).await;
            let ms = at.elapsed().as_secs_f64() * 1e3;
            let id = created.expect("session/new")["session_id"].as_str().unwrap().to_string();
            let events = conn.demux.subscribe(&id);
            (id, events, ms)
        }));
    }
    let mut live = Vec::new();
    let mut create_ms = Vec::new();
    for (i, task) in creating.into_iter().enumerate() {
        let (id, events, ms) = task.await.unwrap();
        live.push((pool[i % conns].clone(), id, events));
        create_ms.push(ms);
    }
    let burst_ms = burst.elapsed().as_secs_f64() * 1e3;
    let cpu_create = probe.now().cpu_ns - cpu_before_create;
    let create_peak = probe.peak(t_create, probe.elapsed());
    let idle_sessions = settle(probe).await;
    let mut heap_lines = Vec::new();
    let heap_idle = heap_at(args, pid, &format!("{sessions} idle sessions"), &mut heap_lines).await;

    // All tasks at once.
    let t_load = probe.elapsed();
    let cpu_before_load = idle_sessions.cpu_ns;
    let load = Instant::now();
    let mut running = Vec::new();
    for (i, (conn, id, mut events)) in live.into_iter().enumerate() {
        let tasks = args.tasks;
        running.push(tokio::spawn(async move {
            let mut outcomes = Vec::new();
            for t in 0..tasks {
                outcomes.push(run_task(&conn, &mut events, &id, &format!("r{round}s{i}t{t}")).await);
            }
            (conn, id, outcomes)
        }));
    }
    let mut outcomes = Vec::new();
    let mut open = Vec::new();
    for task in running {
        let (conn, id, done) = task.await.unwrap();
        outcomes.extend(done);
        open.push((conn, id));
    }
    let load_s = load.elapsed().as_secs_f64();
    let load_peak = probe.peak(t_load, probe.elapsed());
    let after_tasks = settle(probe).await;
    let cpu_load = after_tasks.cpu_ns - cpu_before_load;
    let minstr_per_task =
        (after_tasks.instructions - idle_sessions.instructions) as f64 / 1e6 / outcomes.len().max(1) as f64;
    let heap_tasks = heap_at(args, pid, "after tasks", &mut heap_lines).await;

    // Close everything.
    let closing_at = Instant::now();
    let mut closing = Vec::new();
    for (conn, id) in open {
        closing.push(tokio::spawn(async move {
            let at = Instant::now();
            conn.client.request("session/close", json!({"session_id": id})).await.expect("close");
            conn.demux.forget(&id);
            at.elapsed().as_secs_f64() * 1e3
        }));
    }
    let mut close_ms = Vec::new();
    for task in closing {
        close_ms.push(task.await.unwrap());
    }
    let close_all_ms = closing_at.elapsed().as_secs_f64() * 1e3;
    let closed = settle(probe).await;
    drop(pool);
    tokio::time::sleep(Duration::from_secs(3)).await;
    let released = probe.now();
    let heap_released = heap_at(args, pid, "after close", &mut heap_lines).await;

    let failures: Vec<&TaskOutcome> = outcomes.iter().filter(|o| !o.ok).collect();
    let first = Dist::of(&outcomes.iter().filter(|o| o.ok).map(|o| o.first_delta_ms).collect::<Vec<_>>());
    let total = Dist::of(&outcomes.iter().filter(|o| o.ok).map(|o| o.total_ms).collect::<Vec<_>>());
    let events: usize = outcomes.iter().map(|o| o.events).sum();
    let create = Dist::of(&create_ms);
    let connect = Dist::of(&connect_ms);
    let close = Dist::of(&close_ms);
    let per_session = |a: Sample, b: Sample| (b.footprint as f64 - a.footprint as f64) / sessions as f64 / 1024.0;

    let refused = REFUSED.swap(0, std::sync::atomic::Ordering::Relaxed);
    lines.push(format!("  {conns} connections in {connect_all_ms:.1} ms ({refused} refused by a full backlog and retried)"));
    lines.push(connect.line("connect + initialize"));
    lines.push(format!(
        "  {sessions} × session/new at once: all done in {burst_ms:.1} ms ({:.0} sessions/s, server CPU {:.1} ms)",
        sessions as f64 / (burst_ms / 1e3),
        cpu_create as f64 / 1e6
    ));
    lines.push(create.line("session/new"));
    lines.push(format!(
        "  {} tasks at once: {:.2} s, {} failed, {} events ({:.0} events/s), server CPU {:.1} ms ({:.2} ms, {minstr_per_task:.1} Minstr per task)",
        outcomes.len(),
        load_s,
        failures.len(),
        events,
        events as f64 / load_s,
        cpu_load as f64 / 1e6,
        cpu_load as f64 / 1e6 / outcomes.len().max(1) as f64
    ));
    lines.push(first.line("prompt → first delta"));
    lines.push(total.line("task duration"));
    lines.push(format!(
        "  task slowdown vs solo              p50 ×{:.3}  p99 ×{:.3}",
        total.p50 / solo.total_ms,
        total.p99 / solo.total_ms
    ));
    lines.push(close.line("session/close"));
    lines.push(format!("  close all                          {close_all_ms:>9.2} ms"));
    lines.push(mem_line("before round", before));
    lines.push(mem_line("connections open", connected));
    lines.push(mem_line("peak while creating", create_peak));
    lines.push(mem_line(&format!("{sessions} idle sessions"), idle_sessions));
    lines.push(mem_line("peak while running", load_peak));
    lines.push(mem_line("after tasks (history kept)", after_tasks));
    lines.push(mem_line("after close", closed));
    lines.push(mem_line("after disconnect + 3 s", released));
    lines.push(format!(
        "  per session: {:.1} KiB idle, {:.1} KiB with one task of history, {:.1} KiB at peak",
        per_session(connected, idle_sessions),
        per_session(connected, after_tasks),
        per_session(connected, load_peak)
    ));
    lines.extend(heap_lines);
    for failure in failures.iter().take(5) {
        lines.push(format!("  failure: {}", failure.end));
    }
    let report = json!({
        "connect": connect.json(), "connect_all_ms": connect_all_ms,
        "new": create.json(), "burst_ms": burst_ms, "cpu_create_ms": cpu_create as f64 / 1e6,
        "tasks": outcomes.len(), "failed": failures.len(), "load_s": load_s, "events": events,
        "first_delta": first.json(), "task": total.json(), "cpu_load_ms": cpu_load as f64 / 1e6, "minstr_per_task": minstr_per_task,
        "close": close.json(), "close_all_ms": close_all_ms,
        "mem": {"before": mem_json(before), "connected": mem_json(connected), "create_peak": mem_json(create_peak),
            "idle_sessions": mem_json(idle_sessions), "load_peak": mem_json(load_peak),
            "after_tasks": mem_json(after_tasks), "closed": mem_json(closed), "released": mem_json(released)},
        "heap": {"idle_sessions": heap_idle, "after_tasks": heap_tasks, "after_close": heap_released},
    });
    Round { report, lines }
}

/// Creates and closes sessions from 8 clients for `seconds`. Returns sessions per second, the
/// session/new latency, and service CPU milliseconds per session.
async fn run_churn(env: &Arc<Env>, workspaces: &Arc<Vec<PathBuf>>, probe: &Probe, seconds: f64) -> (f64, Dist, f64) {
    let cpu_before = probe.now().cpu_ns;
    let started = Instant::now();
    let deadline = started + Duration::from_secs_f64(seconds);
    let mut workers = Vec::new();
    for w in 0..8 {
        let (socket, workspaces) = (env.socket.clone(), workspaces.clone());
        workers.push(tokio::spawn(async move {
            let conn = connect(&socket).await;
            let mut latencies = Vec::new();
            let mut i = w;
            while Instant::now() < deadline {
                let at = Instant::now();
                let created = conn
                    .client
                    .request("session/new", json!({"cwd": workspaces[i % workspaces.len()]}))
                    .await
                    .expect("session/new");
                latencies.push(at.elapsed().as_secs_f64() * 1e3);
                let id = created["session_id"].as_str().unwrap().to_string();
                conn.client.request("session/close", json!({"session_id": id})).await.expect("close");
                conn.demux.forget(&id);
                i += 8;
            }
            latencies
        }));
    }
    let mut all = Vec::new();
    for worker in workers {
        all.extend(worker.await.unwrap());
    }
    let elapsed = started.elapsed().as_secs_f64();
    let cpu = (probe.now().cpu_ns - cpu_before) as f64 / 1e6;
    (all.len() as f64 / elapsed, Dist::of(&all), cpu / all.len().max(1) as f64)
}

/// Creates `sessions` sessions at once over `conns` connections and closes them, `count`
/// times. Returns the session/new latencies, each burst's median, and service CPU
/// milliseconds per session.
async fn run_bursts(
    env: &Arc<Env>,
    workspaces: &Arc<Vec<PathBuf>>,
    probe: &Probe,
    sessions: usize,
    conns: usize,
    count: usize,
) -> (Dist, Vec<f64>, f64) {
    let mut pool = Vec::new();
    for _ in 0..conns {
        pool.push(Arc::new(connect(&env.socket).await));
    }
    let cpu_before = probe.now().cpu_ns;
    let mut all = Vec::new();
    let mut medians = Vec::new();
    for _ in 0..count {
        let mut creating = Vec::new();
        for i in 0..sessions {
            let conn = pool[i % conns].clone();
            let cwd = workspaces[i].clone();
            creating.push(tokio::spawn(async move {
                let at = Instant::now();
                let created = conn.client.request("session/new", json!({"cwd": cwd})).await.expect("session/new");
                let ms = at.elapsed().as_secs_f64() * 1e3;
                (conn, created["session_id"].as_str().unwrap().to_string(), ms)
            }));
        }
        let mut open = Vec::new();
        let mut latencies = Vec::new();
        for task in creating {
            let (conn, id, ms) = task.await.unwrap();
            latencies.push(ms);
            open.push((conn, id));
        }
        medians.push(Dist::of(&latencies).p50);
        all.extend(latencies);
        let mut closing = Vec::new();
        for (conn, id) in open {
            closing.push(tokio::spawn(async move {
                conn.client.request("session/close", json!({"session_id": id})).await.expect("close");
                conn.demux.forget(&id);
            }));
        }
        for task in closing {
            task.await.unwrap();
        }
    }
    let cpu = (probe.now().cpu_ns - cpu_before) as f64 / 1e6;
    (Dist::of(&all), medians, cpu / (sessions * count) as f64)
}
