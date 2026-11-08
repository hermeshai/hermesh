//! Correctness-gated workload runner over the production CLI, native socket/stdio and ACP.
#![allow(dead_code)] // The process sampler is shared with her-load.
#[path = "probe.rs"]
mod probe;

use anyhow::{Context, Result, bail, ensure};
use clap::{Parser, ValueEnum};
use her_mock::mcp::{McpTestOptions, serve_http};
use her_mock::simulation::{Scenario, asset_digest, scenario, scenarios};
use her_mock::{MockOptions, MockServer};
use her_rpc::{Client, Incoming, IncomingRx};
use parking_lot::Mutex;
use serde::Serialize;
use serde_json::{Value, json};
use std::collections::HashMap;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::sync::Arc;
use std::time::{Duration, Instant};
use tokio::io::{AsyncBufReadExt, BufReader};
use tokio::net::UnixStream;
use tokio::process::{Child, Command};
use tokio::sync::{Semaphore, mpsc, oneshot};

#[derive(Clone, Copy, Debug, ValueEnum, Serialize, PartialEq)]
#[serde(rename_all = "kebab-case")]
enum Protocol {
    Native,
    Stdio,
    Acp,
    Cli,
    All,
}

#[derive(Clone, Parser)]
#[command(
    name = "her-sim",
    about = "Evidence-checked scenario simulation and kernel load test"
)]
struct Args {
    #[arg(long, default_value = "target/release/her")]
    her: PathBuf,
    #[arg(long, value_enum, default_value = "native")]
    protocol: Protocol,
    /// suite, macro, company, financial, coding, or long-horizon
    #[arg(long, default_value = "suite")]
    scenario: String,
    /// Sessions per scenario in each wave
    #[arg(long, default_value_t = 1)]
    sessions: usize,
    /// Maximum simultaneously active sessions / CLI processes
    #[arg(long, default_value_t = 16)]
    concurrency: usize,
    /// Repeated create/run/close waves on the same service
    #[arg(long, default_value_t = 1)]
    repeats: usize,
    /// Override number of conversation turns (0: the full asset, including 220 turns)
    #[arg(long, default_value_t = 0)]
    turns: usize,
    #[arg(long, default_value_t = 0)]
    ttft_ms: u64,
    #[arg(long, default_value_t = 0)]
    chunk_ms: u64,
    #[arg(long, default_value_t = 180)]
    timeout_secs: u64,
    /// Tool response latency, in milliseconds
    #[arg(long, default_value_t = 0)]
    tool_ms: u64,
    /// Deliberately corrupt one tool result; the correctness gate must fail
    #[arg(long)]
    corrupt_evidence: bool,
    /// Model context window; lower this to exercise compaction
    #[arg(long, default_value_t = 200000)]
    context_window: usize,
    #[arg(long, default_value = "target/simulation-results/report.json")]
    json: PathBuf,
    /// Compare with a report with exactly the same workload; mismatch fails
    #[arg(long)]
    compare: Option<PathBuf>,
    #[arg(long, default_value_t = 20.0)]
    max_regression_pct: f64,
    #[arg(long)]
    max_peak_mib: Option<f64>,
    #[arg(long)]
    max_task_p95_ms: Option<f64>,
    /// Quiet interval after closing each wave; allows allocator upkeep to run
    #[arg(long, default_value_t = 3500)]
    settle_ms: u64,
    #[arg(long)]
    keep: bool,
    /// Native protocol only: every session gets its own connection, as separate clients
    /// would. Latency then excludes queueing behind other sessions' events on one connection.
    #[arg(long)]
    connection_per_session: bool,
}

fn ms(at: Instant) -> f64 {
    at.elapsed().as_secs_f64() * 1000.0
}
fn mib(bytes: u64) -> f64 {
    bytes as f64 / 1048576.0
}
fn dist(mut v: Vec<f64>) -> Value {
    v.sort_by(f64::total_cmp);
    let p = |q: f64| {
        if v.is_empty() {
            Value::Null
        } else {
            json!(
                v[((v.len() as f64 * q).ceil() as usize)
                    .saturating_sub(1)
                    .min(v.len() - 1)]
            )
        }
    };
    json!({"n":v.len(),"p50":p(0.5),"p95":p(0.95),"p99":p(0.99),"max":v.last(),
        "mean": if v.is_empty() {None} else {Some(v.iter().sum::<f64>() / v.len() as f64)}})
}

/// Sum of live target processes at each sampling instant. Mock and driver memory are excluded.
#[derive(Default)]
struct MemoryState {
    pids: HashMap<i32, u64>,
    peak_rss: u64,
    peak_footprint: u64,
    cpu_ns: u64,
    max_threads: u32,
    max_fds: u32,
    samples: u64,
    driver_peak_rss: u64,
    driver_cpu_ns: u64,
    driver_initial_cpu_ns: Option<u64>,
}
#[derive(Clone, Default)]
struct Memory(Arc<Mutex<MemoryState>>);
impl Memory {
    fn add(&self, pid: u32) {
        self.0.lock().pids.insert(pid as i32, 0);
    }
    fn remove(&self, pid: u32) {
        self.sample();
        self.0.lock().pids.remove(&(pid as i32));
    }
    fn sample(&self) -> Value {
        let mut state = self.0.lock();
        let (mut rss, mut footprint, mut cpu, mut threads, mut fds) = (0, 0, 0, 0, 0);
        let mut measured = 0;
        for (pid, previous) in &mut state.pids {
            if let Some(s) = probe::sample(*pid) {
                measured += 1;
                rss += s.rss;
                footprint += s.footprint;
                threads += s.threads;
                fds += s.fds;
                cpu += s.cpu_ns.saturating_sub(*previous);
                *previous = s.cpu_ns;
            }
        }
        if let Some(driver) = probe::sample(std::process::id() as i32) {
            state.driver_peak_rss = state.driver_peak_rss.max(driver.rss);
            let initial = *state.driver_initial_cpu_ns.get_or_insert(driver.cpu_ns);
            state.driver_cpu_ns = driver.cpu_ns.saturating_sub(initial);
        }
        state.cpu_ns += cpu;
        state.peak_rss = state.peak_rss.max(rss);
        state.peak_footprint = state.peak_footprint.max(footprint);
        state.max_threads = state.max_threads.max(threads);
        state.max_fds = state.max_fds.max(fds);
        if measured > 0 {
            state.samples += 1;
        }
        json!({"rss_mib":mib(rss),"footprint_mib":mib(footprint),"threads":threads,"fds":fds,"live_processes":measured})
    }
    fn report(&self) -> Value {
        self.sample();
        let s = self.0.lock();
        json!({"peak_rss_mib":mib(s.peak_rss),"peak_footprint_mib":mib(s.peak_footprint),"sampled_cpu_ms":s.cpu_ns as f64 / 1e6,
            "driver_peak_rss_mib":mib(s.driver_peak_rss),"driver_cpu_ms":s.driver_cpu_ns as f64/1e6,"max_threads":s.max_threads,"max_fds":s.max_fds,"samples":s.samples,"sample_interval_ms":10})
    }
}

struct Env {
    dir: tempfile::TempDir,
    home: PathBuf,
    user: PathBuf,
    work: PathBuf,
    socket: PathBuf,
    catalog: String,
}
impl Env {
    fn new(mock: &MockServer, tools: &str, args: &Args) -> Result<Self> {
        let dir = tempfile::Builder::new().prefix("her-sim-").tempdir()?;
        let (home, user, work) = (
            dir.path().join("home"),
            dir.path().join("user"),
            dir.path().join("work"),
        );
        for p in [&home, &user, &work] {
            std::fs::create_dir_all(p)?;
        }
        std::fs::write(
            home.join("models.toml"),
            mock.models_toml().replace(
                "context_window = 200000",
                &format!("context_window = {}", args.context_window),
            ),
        )?;
        std::fs::write(
            home.join("config.toml"),
            format!(
                "model = \"mock/mock-1\"\npermission_mode = \"bypass-permissions\"\n\
            [server]\nmax_sessions = 100000\nidle_session_secs = 0\n\
            [mcp_servers.sim]\nurl = {tools:?}\nexposure = \"direct\"\nrequired = true\n"
            ),
        )?;
        let socket = dir.path().join("s.sock");
        Ok(Self {
            dir,
            home,
            user,
            work,
            socket,
            catalog: mock.catalog_url(),
        })
    }
    fn command(&self, args: &Args, cwd: &Path) -> Command {
        let mut c = Command::new(&args.her);
        c.current_dir(cwd)
            .env("HER_HOME", &self.home)
            .env("HOME", &self.user)
            .env("HER_CREDENTIAL_STORE", "file")
            .env("HER_NO_BROWSER", "1")
            .env("HER_MODEL_CATALOG_URL", &self.catalog)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .kill_on_drop(true);
        for key in [
            "HER_MODEL",
            "HER_PERMISSION_MODE",
            "HER_APPROVAL",
            "HER_LOG",
            "DEEPSEEK_API_KEY",
            "OPENAI_API_KEY",
            "ANTHROPIC_API_KEY",
            "DASHSCOPE_API_KEY",
            "MOONSHOT_API_KEY",
            "ZHIPUAI_API_KEY",
            "OPENROUTER_API_KEY",
        ] {
            c.env_remove(key);
        }
        c
    }
    fn workspace(&self, name: &str) -> Result<PathBuf> {
        let p = self.work.join(name);
        std::fs::create_dir_all(&p)?;
        Ok(std::fs::canonicalize(p)?)
    }
}

#[derive(Default)]
struct Routes {
    active: HashMap<String, mpsc::UnboundedSender<Incoming>>,
    pending: HashMap<String, Vec<Incoming>>,
}
struct Connection {
    client: Client,
    routes: Arc<Mutex<Routes>>,
    pump: tokio::task::JoinHandle<()>,
    barrier: mpsc::UnboundedSender<oneshot::Sender<()>>,
}
impl Drop for Connection {
    fn drop(&mut self) {
        self.pump.abort();
    }
}
impl Connection {
    fn new(client: Client, mut rx: IncomingRx) -> Self {
        let routes = Arc::new(Mutex::new(Routes::default()));
        let route = routes.clone();
        let (barrier, mut barriers) = mpsc::unbounded_channel::<oneshot::Sender<()>>();
        let pump = tokio::spawn(async move {
            let deliver = |incoming: Incoming| {
                let p = incoming.params();
                let Some(id) = p
                    .get("session_id")
                    .or_else(|| p.get("sessionId"))
                    .and_then(Value::as_str)
                    .map(str::to_string)
                else {
                    return;
                };
                let mut routes = route.lock();
                if let Some(tx) = routes.active.get(&id) {
                    let _ = tx.send(incoming);
                } else {
                    routes.pending.entry(id).or_default().push(incoming);
                }
            };
            loop {
                tokio::select! {
                    incoming = rx.recv() => match incoming {
                        Some(incoming) => deliver(incoming),
                        None => break,
                    },
                    Some(ack) = barriers.recv() => {
                        // The RPC reader enqueues preceding notifications before resolving a
                        // response. Drain that queued prefix before acknowledging the barrier.
                        // A bounded snapshot avoids waiting on unrelated sessions' future traffic.
                        for _ in 0..rx.len() {
                            if let Ok(incoming) = rx.try_recv() { deliver(incoming); }
                        }
                        let _ = ack.send(());
                    },
                }
            }
            route.lock().active.clear();
        });
        Self {
            client,
            routes,
            pump,
            barrier,
        }
    }
    async fn flush_notifications(&self) -> Result<()> {
        let (ack, wait) = oneshot::channel();
        self.barrier
            .send(ack)
            .map_err(|_| anyhow::anyhow!("notification pump closed"))?;
        wait.await.context("notification barrier closed")
    }
    fn subscribe(&self, id: &str) -> mpsc::UnboundedReceiver<Incoming> {
        let (tx, rx) = mpsc::unbounded_channel();
        let mut r = self.routes.lock();
        for msg in r.pending.remove(id).unwrap_or_default() {
            let _ = tx.send(msg);
        }
        r.active.insert(id.into(), tx);
        rx
    }
    fn forget(&self, id: &str) {
        let mut r = self.routes.lock();
        r.active.remove(id);
        r.pending.remove(id);
    }
}

async fn rpc(client: &Client, method: &str, params: Value, wait: Duration) -> Result<Value> {
    Ok(tokio::time::timeout(wait, client.request(method, params))
        .await
        .context("RPC deadline")??)
}

async fn connect_native(args: &Args, env: &Env) -> Result<Arc<Connection>> {
    // A full listen queue refuses at once; clients back off and retry (protocol-design §8.2).
    let deadline = Instant::now() + Duration::from_secs(args.timeout_secs);
    let mut backoff = Duration::from_millis(1);
    let stream = loop {
        match UnixStream::connect(&env.socket).await {
            Ok(stream) => break stream,
            Err(e) if e.kind() == std::io::ErrorKind::ConnectionRefused && Instant::now() < deadline => {
                tokio::time::sleep(backoff).await;
                backoff = (backoff * 2).min(Duration::from_millis(50));
            }
            Err(e) => return Err(e.into()),
        }
    };
    let (r, w) = stream.into_split();
    let (client, rx) = Client::connect(r, w);
    let init = json!({"protocol_version":2,"client_info":{"name":"her-sim"}});
    rpc(&client, "initialize", init, Duration::from_secs(args.timeout_secs)).await?;
    Ok(Arc::new(Connection::new(client, rx)))
}

async fn service(args: &Args, env: &Env, memory: &Memory) -> Result<(Child, Arc<Connection>, f64)> {
    let at = Instant::now();
    let mut c = env.command(args, &env.work);
    let mut child = match args.protocol {
        Protocol::Native => c
            .args([
                "serve",
                "--listen",
                &format!("unix://{}", env.socket.display()),
            ])
            .stdout(Stdio::null())
            .spawn()?,
        Protocol::Stdio => c.args(["serve", "--stdio"]).stdin(Stdio::piped()).spawn()?,
        Protocol::Acp => c.arg("acp").stdin(Stdio::piped()).spawn()?,
        _ => bail!("not a service protocol"),
    };
    memory.add(child.id().context("child PID")?);
    let (client, rx) = if args.protocol == Protocol::Native {
        let stream = loop {
            if let Ok(s) = UnixStream::connect(&env.socket).await {
                break s;
            }
            if let Some(status) = child.try_wait()? {
                bail!("service exited before ready: {status}");
            }
            ensure!(
                at.elapsed() < Duration::from_secs(args.timeout_secs),
                "service startup deadline"
            );
            tokio::time::sleep(Duration::from_millis(1)).await;
        };
        let (r, w) = stream.into_split();
        Client::connect(r, w)
    } else {
        Client::connect(child.stdout.take().unwrap(), child.stdin.take().unwrap())
    };
    let init = if args.protocol == Protocol::Acp {
        json!({"protocolVersion":1,"clientCapabilities":{}})
    } else {
        json!({"protocol_version":2,"client_info":{"name":"her-sim"}})
    };
    rpc(
        &client,
        "initialize",
        init,
        Duration::from_secs(args.timeout_secs),
    )
    .await?;
    Ok((child, Arc::new(Connection::new(client, rx)), ms(at)))
}

#[derive(Default, Serialize)]
struct Turn {
    total_ms: f64,
    first_delta_ms: Option<f64>,
    process_ready_ms: Option<f64>,
    events: usize,
    tools: usize,
    tool_errors: usize,
    compactions: usize,
    #[serde(skip)]
    text: String,
    #[serde(skip)]
    end: String,
}
impl Turn {
    fn native(&mut self, e: &Value, at: Instant, last_seq: &mut u64) -> Result<()> {
        if let Some(seq) = e["seq"].as_u64() {
            ensure!(
                seq > *last_seq,
                "out-of-order sequence {seq} after {last_seq}"
            );
            *last_seq = seq;
        }
        self.events += 1;
        match e["type"].as_str().unwrap_or("") {
            "session_started" => {
                self.process_ready_ms.get_or_insert_with(|| ms(at));
            }
            "message_delta" => {
                self.first_delta_ms.get_or_insert_with(|| ms(at));
            }
            "tool_started" => self.tools += 1,
            "tool_completed" => {
                if e["result"]["is_error"] == true {
                    self.tool_errors += 1;
                }
            }
            "compaction_completed" => self.compactions += 1,
            "task_completed" => {
                self.text = e["last_message"].as_str().unwrap_or("").into();
                self.end = "completed".into();
            }
            "task_failed" | "task_interrupted" => self.end = e.to_string(),
            _ => {}
        }
        Ok(())
    }
    fn acp(&mut self, msg: Incoming, at: Instant) -> Result<()> {
        ensure!(
            !matches!(msg, Incoming::Request { .. }),
            "unexpected client request: {}",
            msg.method()
        );
        self.events += 1;
        let u = &msg.params()["update"];
        match u["sessionUpdate"].as_str().unwrap_or("") {
            "agent_message_chunk" => {
                self.first_delta_ms.get_or_insert_with(|| ms(at));
                self.text
                    .push_str(u["content"]["text"].as_str().unwrap_or(""));
            }
            "agent_thought_chunk" => {
                self.first_delta_ms.get_or_insert_with(|| ms(at));
            }
            "tool_call" => self.tools += 1,
            "tool_call_update" if u["status"] == "failed" => self.tool_errors += 1,
            _ => {}
        }
        Ok(())
    }
}

async fn rpc_turn(
    args: &Args,
    conn: &Connection,
    rx: &mut mpsc::UnboundedReceiver<Incoming>,
    id: &str,
    text: &str,
    seq: &mut u64,
) -> Result<Turn> {
    let at = Instant::now();
    let wait = Duration::from_secs(args.timeout_secs);
    tokio::time::timeout(wait,async {
        let mut turn = Turn::default();
        if args.protocol == Protocol::Acp {
            let request = conn.client.request("session/prompt",json!({"sessionId":id,"prompt":[{"type":"text","text":text}]}));
            tokio::pin!(request);
            loop { tokio::select! {
                result = &mut request => { ensure!(result?["stopReason"] == "end_turn","ACP did not finish normally"); break; }
                msg = rx.recv() => turn.acp(msg.context("ACP stream closed")?,at)?,
            }}
            conn.flush_notifications().await?;
            while let Ok(msg) = rx.try_recv() { turn.acp(msg,at)?; }
            turn.end = "completed".into();
        } else {
            rpc(&conn.client,"session/op",json!({"session_id":id,"op":{"type":"prompt","input":[{"type":"text","text":text}]}}),wait).await?;
            loop {
                let msg = rx.recv().await.context("event stream closed")?;
                if msg.method() == "session/event" { turn.native(&msg.params()["event"],at,seq)?; }
                if !turn.end.is_empty() { break; }
            }
        }
        turn.total_ms = ms(at); ensure!(turn.end == "completed","{}",turn.end); Ok(turn)
    }).await.context("task deadline")?
}

async fn cli_turn(
    args: &Args,
    env: &Env,
    memory: &Memory,
    cwd: &Path,
    session: &mut Option<String>,
    text: &str,
) -> Result<Turn> {
    let at = Instant::now();
    let mut c = env.command(args, cwd);
    match session.as_ref() {
        Some(id) => {
            c.args(["exec", "--resume", id, "--output", "stream-json", text]);
        }
        None => {
            c.args(["exec", "--output", "stream-json", text]);
        }
    }
    let mut child = c.spawn()?;
    let pid = child.id().unwrap();
    memory.add(pid);
    let result = tokio::time::timeout(Duration::from_secs(args.timeout_secs), async {
        let mut lines = BufReader::new(child.stdout.take().unwrap()).lines();
        let mut turn = Turn::default();
        let mut seq = 0;
        while let Some(line) = lines.next_line().await? {
            let event: Value =
                serde_json::from_str(&line).context("CLI emitted non-JSON stdout")?;
            if event["type"] == "session_started" {
                *session = event["session_id"].as_str().map(str::to_string);
            }
            turn.native(&event, at, &mut seq)?;
        }
        let status = child.wait().await?;
        ensure!(status.success(), "CLI failed: {status}; {}", turn.end);
        ensure!(turn.end == "completed", "CLI task failed: {}", turn.end);
        turn.total_ms = ms(at);
        Ok(turn)
    })
    .await
    .context("CLI deadline")
    .and_then(|v| v);
    memory.remove(pid);
    result
}

#[derive(Serialize)]
struct RunReport {
    scenario: String,
    run: String,
    completed_turns: usize,
    expected_turns: usize,
    session_new_ms: Option<f64>,
    close_ms: Option<f64>,
    turns: Vec<Turn>,
    error: Option<String>,
    audit: Value,
}

async fn run_session(
    args: &Args,
    env: &Env,
    memory: &Memory,
    conn: Option<&Connection>,
    case: &Scenario,
    run: &str,
) -> RunReport {
    let turns = if args.turns == 0 {
        case.turns
    } else {
        args.turns.min(case.turns)
    };
    let mut report = RunReport {
        scenario: case.id.clone(),
        run: run.into(),
        completed_turns: 0,
        expected_turns: turns,
        session_new_ms: None,
        close_ms: None,
        turns: vec![],
        error: None,
        audit: Value::Null,
    };
    let own = if args.connection_per_session {
        match connect_native(args, env).await {
            Ok(own) => Some(own),
            Err(error) => {
                report.error = Some(format!("connect: {error:#}"));
                return report;
            }
        }
    } else {
        None
    };
    let conn = own.as_deref().or(conn);
    let mut id: Option<String> = None;
    let task = async {
        let cwd = env.workspace(run)?;
        let mut seq = 0;
        let mut events = None;
        if let Some(conn) = conn {
            let at = Instant::now();
            let p = if args.protocol == Protocol::Acp {
                json!({"cwd":cwd,"mcpServers":[]})
            } else {
                json!({"cwd":cwd})
            };
            let result = rpc(
                &conn.client,
                "session/new",
                p,
                Duration::from_secs(args.timeout_secs),
            )
            .await?;
            id = Some(
                result[if args.protocol == Protocol::Acp {
                    "sessionId"
                } else {
                    "session_id"
                }]
                .as_str()
                .context("session id")?
                .into(),
            );
            events = Some(conn.subscribe(id.as_deref().unwrap()));
            report.session_new_ms = Some(ms(at));
        }
        for t in 0..turns {
            let prompt = case.prompt(run, t);
            let result = match conn {
                Some(conn) => {
                    rpc_turn(
                        args,
                        conn,
                        events.as_mut().unwrap(),
                        id.as_deref().unwrap(),
                        &prompt,
                        &mut seq,
                    )
                    .await?
                }
                None => cli_turn(args, env, memory, &cwd, &mut id, &prompt).await?,
            };
            ensure!(
                result.text == case.expected_reply(run, t),
                "final answer mismatch at turn {t}: {}",
                result.text
            );
            ensure!(
                result.tools == case.calls_per_turn(),
                "tool event count mismatch: {} != {}",
                result.tools,
                case.calls_per_turn()
            );
            let expected_errors = case
                .steps
                .iter()
                .flat_map(|s| &s.calls)
                .filter(|c| c.is_error)
                .count();
            ensure!(
                result.tool_errors == expected_errors,
                "tool error event count mismatch: {} != {expected_errors}",
                result.tool_errors
            );
            report.completed_turns += 1;
            report.turns.push(result);
        }
        Ok::<(), anyhow::Error>(())
    }
    .await;
    if let Err(error) = task {
        report.error = Some(format!("{error:#}"));
    }
    if let (Some(conn), Some(id)) = (conn, id.as_deref()) {
        let at = Instant::now();
        let p = if args.protocol == Protocol::Acp {
            json!({"sessionId":id})
        } else {
            json!({"session_id":id})
        };
        if let Err(e) = rpc(
            &conn.client,
            "session/close",
            p,
            Duration::from_secs(args.timeout_secs),
        )
        .await
        {
            report.error = Some(format!("close: {e:#}"));
        }
        report.close_ms = Some(ms(at));
        conn.forget(id);
    }
    report
}

async fn run_protocol(args: Args) -> Result<Value> {
    let mock = MockServer::bind_with(
        "127.0.0.1:0",
        MockOptions {
            ttft: Duration::from_millis(args.ttft_ms),
            chunk_delay: Duration::from_millis(args.chunk_ms),
        },
    )
    .await?;
    let tools = serve_http(
        "127.0.0.1:0",
        McpTestOptions {
            simulation: true,
            simulation_delay_ms: args.tool_ms,
            ..Default::default()
        },
    )
    .await?;
    if args.corrupt_evidence {
        tools.simulation().corrupt_next_result();
    }
    let env = Arc::new(Env::new(&mock, &tools.url(), &args)?);
    let memory = Memory::default();
    let mem = memory.clone();
    let sampler = tokio::spawn(async move {
        loop {
            mem.sample();
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    });
    let mut child = None;
    let mut conn = None;
    let mut ready_ms = None;
    if args.protocol != Protocol::Cli {
        let (c, connection, ready) = service(&args, &env, &memory).await?;
        child = Some(c);
        conn = Some(connection);
        ready_ms = Some(ready);
    }
    let idle = memory.sample();
    let cases: Vec<&Scenario> = if args.scenario == "suite" {
        scenarios().iter().collect()
    } else {
        vec![scenario(&args.scenario).map_err(anyhow::Error::msg)?]
    };
    let permits = Arc::new(Semaphore::new(args.concurrency));
    let mut reports = Vec::new();
    let mut waves = Vec::new();
    let started = Instant::now();
    for wave in 0..args.repeats {
        let at = Instant::now();
        let mut jobs = tokio::task::JoinSet::new();
        for case in &cases {
            for i in 0..args.sessions {
                let (case, env, memory, conn, args, permits) = (
                    *case,
                    env.clone(),
                    memory.clone(),
                    conn.clone(),
                    args.clone(),
                    permits.clone(),
                );
                let run = format!("w{wave}-{}-{i}", case.id);
                jobs.spawn(async move {
                    let _permit = permits.acquire().await.unwrap();
                    run_session(&args, &env, &memory, conn.as_deref(), case, &run).await
                });
            }
        }
        while let Some(result) = jobs.join_next().await {
            let mut report = result.context("session worker panicked")?;
            report.audit = tools.simulation().finish(&report.scenario, &report.run);
            let expected_calls =
                scenario(&report.scenario).unwrap().calls_per_turn() * report.expected_turns;
            if report.audit["completed_turns"] != report.expected_turns
                || report.audit["tool_calls"] != expected_calls
                || report.audit["pending_step"] != 0
            {
                report
                    .error
                    .get_or_insert_with(|| format!("tool audit mismatch: {}", report.audit));
            }
            if report.error.is_none() {
                let case = scenario(&report.scenario).unwrap();
                if let Some(path) = case.summary["artifact"].as_str() {
                    if report.audit["artifacts"].get(path).is_none() {
                        report.error = Some("missing final artifact".into());
                    }
                } else if report.scenario == "long-horizon" {
                    let turns = report.expected_turns;
                    if report.audit["ledger_balance"] != turns * (turns + 1) / 2 {
                        report.error = Some("ledger final balance mismatch".into());
                    }
                }
            }
            if let Some(error) = &report.error {
                writeln!(std::io::stderr(), "{} failed: {error}", report.run)?;
            }
            reports.push(report);
        }
        let active_ms = ms(at);
        tokio::time::sleep(Duration::from_millis(args.settle_ms)).await;
        waves.push(json!({"wave":wave,"active_ms":active_ms,"elapsed_ms":ms(at),"after_close":memory.sample()}));
        writeln!(
            std::io::stdout(),
            "{:?}: wave {} complete ({} sessions)",
            args.protocol,
            wave + 1,
            args.sessions * cases.len()
        )?;
    }
    let elapsed_s = started.elapsed().as_secs_f64();
    let active_s = waves
        .iter()
        .map(|w| w["active_ms"].as_f64().unwrap())
        .sum::<f64>()
        / 1000.0;
    let turns: Vec<&Turn> = reports.iter().flat_map(|r| &r.turns).collect();
    let failures = reports.iter().filter(|r| r.error.is_some()).count();
    let mem = memory.report();
    let stats = mock.stats();
    let metrics = json!({"startup_ms":ready_ms,"elapsed_s":elapsed_s,"sessions":reports.len(),"failed_sessions":failures,"completed_turns":turns.len(),
        "active_s":active_s,"turns_per_second":turns.len() as f64/active_s,
        "cli_process_ready_ms":if args.protocol == Protocol::Cli {dist(turns.iter().filter_map(|r|r.process_ready_ms).collect())} else {Value::Null},"session_new_ms":dist(reports.iter().filter_map(|r|r.session_new_ms).collect()),
        "task_ms":dist(turns.iter().map(|r|r.total_ms).collect()),"first_delta_ms":dist(turns.iter().filter_map(|r|r.first_delta_ms).collect()),
        "close_ms":dist(reports.iter().filter_map(|r|r.close_ms).collect()),"events":turns.iter().map(|r|r.events).sum::<usize>(),
        "tool_calls":turns.iter().map(|r|r.tools).sum::<usize>(),"compactions":turns.iter().map(|r|r.compactions).sum::<usize>(),
        "memory":mem,"idle":idle,"waves":waves,"model_requests":stats.requests.load(std::sync::atomic::Ordering::Relaxed),
        "model_request_bytes":stats.request_bytes.load(std::sync::atomic::Ordering::Relaxed),
        "model_peak_request_bytes":stats.peak_request_bytes.load(std::sync::atomic::Ordering::Relaxed),
        "model_planning_wall_ms":stats.planning_ns.load(std::sync::atomic::Ordering::Relaxed) as f64/1e6,
        "model_errors":stats.errors_served.load(std::sync::atomic::Ordering::Relaxed),"model_contract_errors":stats.simulation_errors(),"tool_contract_violations":tools.simulation().violations()});
    if let Some(mut child) = child {
        let pid = child.id().unwrap();
        if args.protocol != Protocol::Acp {
            let _ = rpc(
                &conn.as_ref().unwrap().client,
                "server/shutdown",
                json!({}),
                Duration::from_secs(5),
            )
            .await;
        }
        drop(conn.take());
        if tokio::time::timeout(Duration::from_secs(5), child.wait())
            .await
            .is_err()
        {
            child.kill().await?;
            child.wait().await?;
        }
        memory.remove(pid);
    }
    sampler.abort();
    writeln!(
        std::io::stdout(),
        "{:?}: {} turns, {} failed sessions, p95 {} ms, peak {} MiB",
        args.protocol,
        turns.len(),
        failures,
        metrics["task_ms"]["p95"],
        metrics["memory"]["peak_footprint_mib"]
    )?;
    let result = json!({"protocol":args.protocol,"metrics":metrics,"runs":reports});
    if args.keep {
        writeln!(std::io::stdout(), "data: {}", env.dir.path().display())?;
        if let Ok(e) = Arc::try_unwrap(env) {
            let _ = e.dir.keep();
        }
    }
    Ok(result)
}

fn gates(args: &Args, report: &Value) -> Result<Vec<String>> {
    let mut failures = Vec::new();
    for r in report["protocols"].as_array().unwrap() {
        let m = &r["metrics"];
        if m["failed_sessions"] != 0 || m["tool_contract_violations"] != 0 || m["model_errors"] != 0
        {
            failures.push(format!("{}: correctness gate failed", r["protocol"]));
        }
        if m["memory"]["samples"] == 0 {
            failures.push(format!("{}: no process memory samples", r["protocol"]));
        }
        for (limit, value, name) in [
            (
                args.max_peak_mib,
                m["memory"]["peak_footprint_mib"].as_f64(),
                "peak memory",
            ),
            (
                args.max_task_p95_ms,
                m["task_ms"]["p95"].as_f64(),
                "task p95",
            ),
        ] {
            if let (Some(limit), Some(value)) = (limit, value)
                && value > limit
            {
                failures.push(format!("{}: {name} {value:.3} > {limit:.3}", r["protocol"]));
            }
        }
    }
    if let Some(path) = &args.compare {
        let baseline: Value = serde_json::from_slice(&std::fs::read(path)?)?;
        ensure!(
            baseline["harness_sha256"] == report["harness_sha256"],
            "baseline harness binary mismatch"
        );
        ensure!(
            baseline["passed"] == true && baseline["schema_version"] == report["schema_version"],
            "baseline is failed or incompatible"
        );
        ensure!(
            baseline["protocols"].as_array().map(Vec::len)
                == report["protocols"].as_array().map(Vec::len),
            "baseline protocol count mismatch"
        );
        ensure!(
            baseline["workload"] == report["workload"],
            "baseline workload/asset mismatch; comparison rejected"
        );
        for (before, after) in baseline["protocols"]
            .as_array()
            .context("baseline protocols")?
            .iter()
            .zip(report["protocols"].as_array().unwrap())
        {
            ensure!(
                before["protocol"] == after["protocol"],
                "baseline protocol mismatch"
            );
            ensure!(
                before["metrics"]["failed_sessions"] == 0,
                "baseline has failed sessions"
            );
            for path in [
                "/metrics/startup_ms",
                "/metrics/task_ms/p95",
                "/metrics/session_new_ms/p95",
                "/metrics/memory/peak_footprint_mib",
            ] {
                if let (Some(a), Some(b)) = (
                    before.pointer(path).and_then(Value::as_f64),
                    after.pointer(path).and_then(Value::as_f64),
                ) && a > 0.0
                    && b > a * (1.0 + args.max_regression_pct / 100.0)
                {
                    failures.push(format!(
                        "{} {path}: regression {:.1}% exceeds {:.1}%",
                        after["protocol"],
                        (b / a - 1.0) * 100.0,
                        args.max_regression_pct
                    ));
                }
            }
        }
    }
    Ok(failures)
}

#[tokio::main(flavor = "multi_thread", worker_threads = 8)]
async fn main() -> Result<()> {
    let mut args = Args::parse();
    args.her = std::fs::canonicalize(&args.her)?;
    ensure!(
        args.sessions > 0 && args.concurrency > 0 && args.repeats > 0 && args.timeout_secs > 0,
        "counts and timeout must be positive"
    );
    ensure!(
        args.context_window >= 16000,
        "context window must be at least 16000 tokens for tools and a checkpoint"
    );
    ensure!(
        args.max_regression_pct.is_finite() && args.max_regression_pct >= 0.0,
        "invalid regression limit"
    );
    if args.scenario != "suite" {
        scenario(&args.scenario).map_err(anyhow::Error::msg)?;
    }
    ensure!(
        !args.connection_per_session || args.protocol == Protocol::Native,
        "--connection-per-session needs --protocol native"
    );
    let protocols = if args.protocol == Protocol::All {
        vec![
            Protocol::Native,
            Protocol::Stdio,
            Protocol::Acp,
            Protocol::Cli,
        ]
    } else {
        vec![args.protocol]
    };
    let mut report = json!({"schema_version":1,"workload":{"asset_sha256":asset_digest(),"scenario":args.scenario,"sessions":args.sessions,"concurrency":args.concurrency,
        "repeats":args.repeats,"turns":args.turns,"settle_ms":args.settle_ms,"ttft_ms":args.ttft_ms,"chunk_ms":args.chunk_ms,"tool_ms":args.tool_ms,"corrupt_evidence":args.corrupt_evidence,"context_window":args.context_window,"connection_per_session":args.connection_per_session,"protocols":protocols,
        "os":std::env::consts::OS,"arch":std::env::consts::ARCH},"binary":args.her,
        "binary_sha256":her_mock::simulation::sha256(&std::fs::read(&args.her)?),
        "harness_sha256":her_mock::simulation::sha256(&std::fs::read(std::env::current_exe()?)?),
        "protocols":[]});
    let mut results = Vec::new();
    for protocol in protocols {
        let mut a = args.clone();
        a.protocol = protocol;
        results.push(run_protocol(a).await?);
    }
    report["protocols"] = json!(results);
    let failures = match gates(&args, &report) {
        Ok(f) => f,
        Err(e) => vec![format!("comparison failed: {e:#}")],
    };
    report["gate_failures"] = json!(failures);
    report["passed"] = json!(failures.is_empty());
    if let Some(p) = args.json.parent().filter(|p| !p.as_os_str().is_empty()) {
        std::fs::create_dir_all(p)?;
    }
    std::fs::write(&args.json, serde_json::to_vec_pretty(&report)?)?;
    writeln!(std::io::stdout(), "report: {}", args.json.display())?;
    ensure!(failures.is_empty(), "{}", failures.join("; "));
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    async fn acp_response_barrier_delivers_the_entire_notification_prefix() {
        use tokio::io::AsyncWriteExt;
        let (client_side, server_side) = tokio::io::duplex(1 << 20);
        let (r, w) = tokio::io::split(client_side);
        let (client, incoming) = Client::connect(r, w);
        let conn = Connection::new(client, incoming);
        let mut notifications = conn.subscribe("s");
        let server = tokio::spawn(async move {
            let (r, mut w) = tokio::io::split(server_side);
            let mut lines = BufReader::new(r).lines();
            let request: Value =
                serde_json::from_str(&lines.next_line().await.unwrap().unwrap()).unwrap();
            let mut frames = String::new();
            for i in 0..1000 {
                frames.push_str(&json!({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","n":i}}).to_string());
                frames.push('\n');
            }
            frames.push_str(
                &json!({"jsonrpc":"2.0","id":request["id"],"result":{"stopReason":"end_turn"}})
                    .to_string(),
            );
            frames.push('\n');
            w.write_all(frames.as_bytes()).await.unwrap();
            // Keep the transport alive until the client has crossed its barrier.
            lines.next_line().await.unwrap();
        });
        conn.client
            .request("session/prompt", json!({}))
            .await
            .unwrap();
        conn.flush_notifications().await.unwrap();
        for i in 0..1000 {
            assert_eq!(notifications.try_recv().unwrap().params()["n"], i);
        }
        assert!(notifications.try_recv().is_err());
        conn.client.notify("test/done", json!({}));
        server.await.unwrap();
    }

    #[test]
    fn percentile_reports_do_not_turn_missing_samples_into_zero_latency() {
        assert!(dist(vec![])["p95"].is_null());
        let d = dist((1..=100).rev().map(f64::from).collect());
        assert_eq!(d["p50"], 50.0);
        assert_eq!(d["p95"], 95.0);
        assert_eq!(d["p99"], 99.0);
    }
    #[test]
    fn correctness_and_resource_budgets_are_hard_gates() {
        let args = Args::try_parse_from(["her-sim", "--max-peak-mib", "20"]).unwrap();
        let mut report = json!({"protocols":[{"protocol":"native","metrics":{
            "failed_sessions":0,"tool_contract_violations":0,"model_errors":0,
            "memory":{"samples":1,"peak_footprint_mib":10},"task_ms":{"p95":1}}}]});
        assert!(gates(&args, &report).unwrap().is_empty());
        report["protocols"][0]["metrics"]["model_errors"] = json!(1);
        assert!(!gates(&args, &report).unwrap().is_empty());
        report["protocols"][0]["metrics"]["model_errors"] = json!(0);
        report["protocols"][0]["metrics"]["memory"]["peak_footprint_mib"] = json!(21);
        assert!(!gates(&args, &report).unwrap().is_empty());
    }
}
