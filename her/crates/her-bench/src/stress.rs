//! Harder workloads than the rounds of `her-load`, picked with `--scenario`:
//!
//! | Scenario | Workload |
//! |---|---|
//! | `long` | `--sessions` sessions each run `--tasks` agent tasks in turn, so history grows past compaction; then all are closed, resumed at once, run one more task, and the service restarts over their logs |
//! | `fanout` | every session's task starts `--agents` sub-agents that each run an agent task; two waves |
//! | `bigout` | every session runs a command with `--size` MB of output, reads a `--size` MB file, gets a 1 MB reply, then a short prompt |
//! | `viewers` | `--agents` extra connections attach to every session and watch its tasks; one more client per session attaches and never reads |
//! | `interrupt` | `--tasks` times, every session starts a task (streaming, a long command, or an agent task) and interrupts it after a while |
//! | `startup` | `--size` × 100 sessions are written to disk, then the service restarts over them five times |
//! | `churn` | every session runs one task of `--turns` tool turns (reads, writes and edits of a file, appends, searches), through many compactions; then close, resume the first, restart |
//! | `exec-churn` | the same task in one `her exec` process |

use crate::Args;
use crate::Conn;
use crate::Dist;
use crate::Env;
use crate::TaskOutcome;
use crate::WAIT;
use crate::connect;
use crate::heap_at;
use crate::mem_json;
use crate::mem_line;
use crate::mib;
use crate::probe::Probe;
use crate::probe::Sample;
use crate::run_prompt;
use crate::settle;
use crate::start_service;
use crate::stop_service;
use her_mock::MockOptions;
use her_mock::MockServer;
use her_rpc::Incoming;
use serde_json::Value;
use serde_json::json;
use std::io::Write;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use tokio::io::AsyncWriteExt;
use tokio::net::UnixStream;
use tokio::sync::mpsc;

/// A session with its own connection and its event stream.
struct Slot {
    conn: Arc<Conn>,
    id: String,
    events: mpsc::UnboundedReceiver<Incoming>,
}

/// One service under test and the report being written.
struct Bench {
    args: Args,
    env: Arc<Env>,
    mock: MockServer,
    service: tokio::process::Child,
    pid: i32,
    probe: Probe,
    out: Vec<String>,
    report: serde_json::Map<String, Value>,
}

impl Bench {
    async fn start(args: Args, options: MockOptions, config: &str) -> std::io::Result<Bench> {
        let mock = MockServer::bind_with("127.0.0.1:0", options).await?;
        let env = Arc::new(Env::with_config(&mock, config));
        let (service, pid, ready_ms) = start_service(&args, &env).await?;
        let probe = Probe::start(pid, Duration::from_millis(10));
        let idle = settle(&probe).await;
        let mut bench = Bench {
            args,
            env,
            mock,
            service,
            pid,
            probe,
            out: Vec::new(),
            report: serde_json::Map::new(),
        };
        bench.out.push(format!(
            "her-load --scenario {}: gateway ttft {} ms, {} ms per chunk",
            bench.args.scenario,
            options.ttft.as_millis(),
            options.chunk_delay.as_millis()
        ));
        bench.out.push(format!(
            "  spawn → socket ready              {ready_ms:>9.2} ms"
        ));
        bench.line(mem_line("idle service", idle));
        bench.report.insert("ready_ms".into(), json!(ready_ms));
        bench.report.insert("idle".into(), mem_json(idle));
        Ok(bench)
    }

    fn line(&mut self, line: String) {
        let _ = writeln!(std::io::stdout(), "{line}");
        self.out.push(line);
    }

    fn put(&mut self, key: &str, value: Value) {
        self.report.insert(key.into(), value);
    }

    async fn heap(&mut self, label: &str) -> Value {
        let mut lines = Vec::new();
        let value = heap_at(&self.args, self.pid, label, &mut lines).await;
        for line in lines {
            self.line(line);
        }
        value
    }

    async fn mem(&mut self, label: &str) -> Sample {
        let sample = settle(&self.probe).await;
        self.line(mem_line(label, sample));
        sample
    }

    /// Restarts the service over the same data directory.
    async fn restart(&mut self) -> std::io::Result<f64> {
        let _ = stop_service(&self.env, &mut self.service).await?;
        self.probe.stop();
        let (service, pid, ready_ms) = start_service(&self.args, &self.env).await?;
        self.service = service;
        self.pid = pid;
        self.probe = Probe::start(pid, Duration::from_millis(10));
        Ok(ready_ms)
    }

    async fn finish(mut self) -> std::io::Result<()> {
        let peak = self.probe.now().peak_footprint;
        self.line(format!(
            "  lifetime peak footprint            {:>9.1} MiB",
            mib(peak)
        ));
        let (shutdown_ms, code) = stop_service(&self.env, &mut self.service).await?;
        self.probe.stop();
        self.line(format!(
            "  server/shutdown → exit            {shutdown_ms:>9.2} ms   exit code {code:?}"
        ));
        let stats = self.mock.stats();
        let served = format!(
            "  gateway served {} requests, {} chunks, {} errors",
            stats.requests.load(std::sync::atomic::Ordering::Relaxed),
            stats.chunks.load(std::sync::atomic::Ordering::Relaxed),
            stats
                .errors_served
                .load(std::sync::atomic::Ordering::Relaxed)
        );
        self.line(served);
        self.put("lifetime_peak_footprint_mib", json!(mib(peak)));
        self.put("shutdown_ms", json!(shutdown_ms));
        if self.args.keep {
            let _ = writeln!(std::io::stdout(), "data directory: {}", self.env.home.display());
            std::mem::forget(Arc::try_unwrap(self.env).ok().map(|env| env._dir));
        }
        if let Some(path) = &self.args.json {
            std::fs::write(
                path,
                serde_json::to_string_pretty(&Value::Object(self.report)).unwrap(),
            )?;
        }
        std::io::stdout().flush()
    }

    async fn open(&self, workspaces: &[PathBuf]) -> Vec<Slot> {
        let mut opening = Vec::new();
        for cwd in workspaces {
            let (socket, cwd) = (self.env.socket.clone(), cwd.clone());
            opening.push(tokio::spawn(async move {
                let conn = Arc::new(connect(&socket).await);
                let created = conn
                    .client
                    .request("session/new", json!({"cwd": cwd}))
                    .await
                    .expect("session/new");
                let id = created["session_id"].as_str().unwrap().to_string();
                let events = conn.demux.subscribe(&id);
                Slot { conn, id, events }
            }));
        }
        let mut slots = Vec::new();
        for task in opening {
            slots.push(task.await.unwrap());
        }
        slots
    }
}

/// What the service spent on a stretch of work.
#[derive(Clone, Copy)]
struct Cost {
    cpu_ms: f64,
    /// Millions of instructions.
    minstr: f64,
}

impl Cost {
    fn between(a: Sample, b: Sample) -> Cost {
        Cost {
            cpu_ms: (b.cpu_ns - a.cpu_ns) as f64 / 1e6,
            minstr: (b.instructions - a.instructions) as f64 / 1e6,
        }
    }
}

/// Runs one prompt in every session at once. Returns the sessions, the outcomes in session
/// order, the wall time in seconds and what the service spent.
async fn wave(
    probe: &Probe,
    slots: Vec<Slot>,
    prompt: impl Fn(usize) -> String,
) -> (Vec<Slot>, Vec<TaskOutcome>, f64, Cost) {
    let before = probe.now();
    let started = Instant::now();
    let mut running = Vec::new();
    for (i, mut slot) in slots.into_iter().enumerate() {
        let text = prompt(i);
        running.push(tokio::spawn(async move {
            let outcome = run_prompt(&slot.conn, &mut slot.events, &slot.id, &text).await;
            (slot, outcome)
        }));
    }
    let mut slots = Vec::new();
    let mut outcomes = Vec::new();
    for task in running {
        let (slot, outcome) = task.await.unwrap();
        slots.push(slot);
        outcomes.push(outcome);
    }
    let wall = started.elapsed().as_secs_f64();
    tokio::time::sleep(Duration::from_millis(50)).await;
    (slots, outcomes, wall, Cost::between(before, probe.now()))
}

async fn close_all(slots: Vec<Slot>) -> Dist {
    let mut closing = Vec::new();
    for slot in slots {
        closing.push(tokio::spawn(async move {
            let at = Instant::now();
            slot.conn
                .client
                .request("session/close", json!({"session_id": slot.id}))
                .await
                .expect("close");
            at.elapsed().as_secs_f64() * 1e3
        }));
    }
    let mut ms = Vec::new();
    for task in closing {
        ms.push(task.await.unwrap());
    }
    Dist::of(&ms)
}

fn failures(outcomes: &[TaskOutcome]) -> Vec<String> {
    outcomes
        .iter()
        .filter(|o| !o.ok)
        .map(|o| o.end.clone())
        .collect()
}

fn wave_line(label: &str, outcomes: &[TaskOutcome], wall: f64, cost: Cost, per: usize) -> String {
    let failed = failures(outcomes);
    let first = Dist::of(
        &outcomes
            .iter()
            .map(|o| o.first_delta_ms)
            .collect::<Vec<_>>(),
    );
    let events: usize = outcomes.iter().map(|o| o.events).sum();
    let per = per.max(1) as f64;
    let mut line = format!(
        "  {label:<22} {:>7.2} s  {} failed  {:>7} events  CPU {:>6.2} ms  {:>7.1} Minstr each  first delta p50 {:>7.1} p99 {:>7.1} ms",
        wall,
        failed.len(),
        events,
        cost.cpu_ms / per,
        cost.minstr / per,
        first.p50,
        first.p99
    );
    if let Some(first) = failed.first() {
        line.push_str(&format!("  e.g. {first}"));
    }
    line
}

fn wave_json(outcomes: &[TaskOutcome], wall: f64, cost: Cost, per: usize) -> Value {
    json!({
        "wall_s": wall, "failed": failures(outcomes).len(), "cpu_ms": cost.cpu_ms, "cpu_ms_each": cost.cpu_ms / per.max(1) as f64,
        "minstr_each": cost.minstr / per.max(1) as f64,
        "events": outcomes.iter().map(|o| o.events).sum::<usize>(),
        "first_delta": Dist::of(&outcomes.iter().map(|o| o.first_delta_ms).collect::<Vec<_>>()).json(),
        "task": Dist::of(&outcomes.iter().map(|o| o.total_ms).collect::<Vec<_>>()).json(),
    })
}

/// Total bytes of the session logs under `dir`.
fn log_bytes(dir: &Path) -> (u64, usize) {
    let mut total = (0, 0);
    let Ok(entries) = std::fs::read_dir(dir) else {
        return total;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            let (bytes, files) = log_bytes(&path);
            total.0 += bytes;
            total.1 += files;
        } else if path.extension().is_some_and(|e| e == "jsonl") {
            total.0 += entry.metadata().map(|m| m.len()).unwrap_or(0);
            total.1 += 1;
        }
    }
    total
}

pub async fn run(args: Args) -> std::io::Result<()> {
    match args.scenario.as_str() {
        "long" => long(args).await,
        "fanout" => fanout(args).await,
        "bigout" => bigout(args).await,
        "viewers" => viewers(args).await,
        "interrupt" => interrupt(args).await,
        "startup" => startup(args).await,
        "churn" => churn(args).await,
        "exec-churn" => exec_churn(args).await,
        other => {
            let _ = writeln!(
                std::io::stderr(),
                "unknown scenario {other}: rounds, long, fanout, bigout, viewers, interrupt, startup, churn or exec-churn"
            );
            std::process::exit(2);
        }
    }
}

fn gateway(args: &Args) -> MockOptions {
    MockOptions {
        ttft: Duration::from_millis(args.ttft_ms),
        chunk_delay: Duration::from_millis(args.chunk_ms),
    }
}

/// Long sessions: history grows task after task, through compaction; then close, resume
/// everything at once, one more task, and a restart over the logs.
async fn long(args: Args) -> std::io::Result<()> {
    let sessions = args.sessions;
    let tasks = args.tasks.max(1);
    let mut b = Bench::start(args.clone(), gateway(&args), "").await?;
    let workspaces: Vec<PathBuf> = (0..sessions)
        .map(|i| b.env.workspace(&format!("ws{i}")))
        .collect();
    b.line(format!(
        "{sessions} sessions × {tasks} agent tasks, one after another in each session"
    ));
    let base = b.mem("before").await;
    let mut slots = b.open(&workspaces).await;
    let mut waves = Vec::new();
    for t in 0..tasks {
        let (back, outcomes, wall, cpu) = wave(&b.probe, slots, |i| {
            format!("[[mock:agent:s{i}t{t}]] 第 {t} 轮：检查 {i} 号模块并更新报告")
        })
        .await;
        slots = back;
        let now = b.probe.now();
        let line = format!(
            "{}  footprint {:>7.1} MiB",
            wave_line(&format!("task {}", t + 1), &outcomes, wall, cpu, sessions),
            mib(now.footprint)
        );
        b.line(line);
        let mut report = wave_json(&outcomes, wall, cpu, sessions);
        report["footprint_mib"] = json!(mib(now.footprint));
        waves.push(report);
    }
    b.put("waves", json!(waves));
    let (bytes, files) = log_bytes(&b.env.home);
    b.line(format!(
        "  session logs: {files} files, {:.1} MiB ({:.0} KiB per session)",
        mib(bytes),
        bytes as f64 / 1024.0 / sessions as f64
    ));
    let full = b.mem(&format!("{sessions} long sessions")).await;
    let heap_full = b.heap("long sessions").await;
    b.line(format!(
        "  per session: {:.0} KiB footprint",
        (full.footprint as f64 - base.footprint as f64) / 1024.0 / sessions as f64
    ));
    let ids: Vec<String> = slots.iter().map(|s| s.id.clone()).collect();
    let close = close_all(slots).await;
    b.line(close.line("session/close (long)"));
    let closed = b.mem("after close").await;
    let heap_closed = b.heap("after close").await;

    // Resume every session at once.
    let t0 = b.probe.elapsed();
    let started = Instant::now();
    let mut resuming = Vec::new();
    for id in &ids {
        let (socket, id) = (b.env.socket.clone(), id.clone());
        resuming.push(tokio::spawn(async move {
            let conn = Arc::new(connect(&socket).await);
            let at = Instant::now();
            let snapshot = conn
                .client
                .request(
                    "session/resume",
                    json!({"session_id": id, "history_limit": 50}),
                )
                .await
                .expect("session/resume");
            let ms = at.elapsed().as_secs_f64() * 1e3;
            let bytes = snapshot.to_string().len();
            let events = conn.demux.subscribe(&id);
            (Slot { conn, id, events }, ms, bytes)
        }));
    }
    let mut slots = Vec::new();
    let mut resume_ms = Vec::new();
    let mut snapshot_bytes = 0;
    for task in resuming {
        let (slot, ms, bytes) = task.await.unwrap();
        slots.push(slot);
        resume_ms.push(ms);
        snapshot_bytes += bytes;
    }
    let all_ms = started.elapsed().as_secs_f64() * 1e3;
    let resume_peak = b.probe.peak(t0, b.probe.elapsed());
    let resume = Dist::of(&resume_ms);
    b.line(format!(
        "  {sessions} × session/resume at once: all done in {all_ms:.1} ms, snapshots {:.0} KiB each",
        snapshot_bytes as f64 / 1024.0 / sessions as f64
    ));
    b.line(resume.line("session/resume"));
    b.line(mem_line("peak while resuming", resume_peak));
    let resumed = b.mem("resumed").await;
    let heap_resumed = b.heap("resumed").await;
    let (slots, outcomes, wall, cpu) = wave(&b.probe, slots, |i| {
        format!("[[mock:agent:s{i}r]] 恢复后再检查一次 {i} 号模块")
    })
    .await;
    let line = wave_line("task after resume", &outcomes, wall, cpu, sessions);
    b.line(line);
    let after_resume_task = wave_json(&outcomes, wall, cpu, sessions);
    close_all(slots).await;

    // Restart over the logs.
    let ready = b.restart().await?;
    b.line(format!(
        "restart over {files} session logs ({:.1} MiB)",
        mib(bytes)
    ));
    b.line(format!(
        "  spawn → socket ready              {ready:>9.2} ms"
    ));
    let conn = connect(&b.env.socket).await;
    let at = Instant::now();
    let list = conn
        .client
        .request("session/list", json!({}))
        .await
        .expect("session/list");
    let list_ms = at.elapsed().as_secs_f64() * 1e3;
    let listed = list["sessions"].as_array().map_or(0, Vec::len);
    b.line(format!(
        "  first session/list                {list_ms:>9.2} ms ({listed} sessions)"
    ));
    let at = Instant::now();
    conn.client
        .request(
            "session/resume",
            json!({"session_id": ids[0], "history_limit": 50}),
        )
        .await
        .expect("session/resume");
    let cold_ms = at.elapsed().as_secs_f64() * 1e3;
    b.line(format!(
        "  first session/resume              {cold_ms:>9.2} ms"
    ));
    drop(conn);
    b.mem("after restart").await;
    b.put("logs", json!({"files": files, "mib": mib(bytes)}));
    b.put(
        "mem",
        json!({"base": mem_json(base), "full": mem_json(full), "closed": mem_json(closed),
        "resume_peak": mem_json(resume_peak), "resumed": mem_json(resumed)}),
    );
    b.put(
        "heap",
        json!({"full": heap_full, "closed": heap_closed, "resumed": heap_resumed}),
    );
    b.put("close", close.json());
    b.put("resume", json!({"all_ms": all_ms, "each": resume.json(), "snapshot_kib": snapshot_bytes as f64 / 1024.0 / sessions as f64}));
    b.put("after_resume_task", after_resume_task);
    b.put(
        "restart",
        json!({"ready_ms": ready, "list_ms": list_ms, "listed": listed, "resume_ms": cold_ms}),
    );
    b.finish().await
}

/// Sub-agent fan-out: each session's task starts `--agents` sub-agents and waits for them.
async fn fanout(args: Args) -> std::io::Result<()> {
    let sessions = args.sessions;
    let agents = args.agents;
    let mut b = Bench::start(args.clone(), gateway(&args), "").await?;
    let workspaces: Vec<PathBuf> = (0..sessions)
        .map(|i| b.env.workspace(&format!("ws{i}")))
        .collect();
    b.line(format!(
        "{sessions} sessions, each task starts {agents} sub-agents ({} agent tasks per wave)",
        sessions * agents
    ));
    let base = b.mem("before").await;
    let mut slots = b.open(&workspaces).await;
    let idle = b.mem(&format!("{sessions} idle sessions")).await;
    let mut waves = Vec::new();
    for w in 0..2 {
        let t0 = b.probe.elapsed();
        let (back, outcomes, wall, cpu) = wave(&b.probe, slots, |i| {
            format!("[[mock:fanout:{agents}:w{w}s{i}]] 把检查工作分给 {agents} 个子智能体")
        })
        .await;
        slots = back;
        let peak = b.probe.peak(t0, b.probe.elapsed());
        let line = wave_line(
            &format!("wave {}", w + 1),
            &outcomes,
            wall,
            cpu,
            sessions * agents,
        );
        b.line(line);
        b.line(mem_line("peak while running", peak));
        let after = b.mem("after wave").await;
        let heap = b.heap(&format!("after wave {}", w + 1)).await;
        let mut report = wave_json(&outcomes, wall, cpu, sessions * agents);
        report["peak"] = mem_json(peak);
        report["after"] = mem_json(after);
        report["heap"] = heap;
        waves.push(report);
    }
    let close = close_all(slots).await;
    b.line(close.line("session/close (with agents)"));
    let closed = b.mem("after close").await;
    let heap_closed = b.heap("after close").await;
    let (bytes, files) = log_bytes(&b.env.home);
    b.line(format!(
        "  session logs: {files} files, {:.1} MiB",
        mib(bytes)
    ));
    b.put("waves", json!(waves));
    b.put(
        "mem",
        json!({"base": mem_json(base), "idle": mem_json(idle), "closed": mem_json(closed)}),
    );
    b.put("heap_closed", heap_closed);
    b.put("close", close.json());
    b.finish().await
}

/// Huge outputs: a command, a file and a model reply of several megabytes each.
async fn bigout(args: Args) -> std::io::Result<()> {
    let sessions = args.sessions;
    let mb = args.size.max(1);
    let mut b = Bench::start(args.clone(), gateway(&args), "").await?;
    let workspaces: Vec<PathBuf> = (0..sessions)
        .map(|i| b.env.workspace(&format!("ws{i}")))
        .collect();
    // One big file, hard-linked into every workspace.
    let master = b.env.work.join("big.log");
    {
        let mut file = std::io::BufWriter::new(std::fs::File::create(&master)?);
        let mut n = 0u64;
        while (n as usize) * 64 < mb << 20 {
            writeln!(
                file,
                "2026-10-29T03:00:{:02}Z INFO request {n:>10} served in {:>4} us ok",
                n % 60,
                n % 977
            )?;
            n += 1;
        }
    }
    for ws in &workspaces {
        std::fs::hard_link(&master, ws.join("big.log"))?;
    }
    let lines = (mb << 20) / 8;
    b.line(format!("{sessions} sessions: `seq 1 {lines}` (~{mb} MB), read a {mb} MB file, a 1 MB reply, a short prompt"));
    let base = b.mem("before").await;
    let mut slots = b.open(&workspaces).await;
    let prompts = [
        format!("[[mock:bash:seq 1 {lines}]] 打印一长串数字"),
        "[[mock:read:big.log]] 看看这个日志".to_string(),
        "[[mock:big:1024]] 写一份很长的说明".to_string(),
        "[[mock:echo]] 总结一下".to_string(),
    ];
    let mut waves = Vec::new();
    for (n, prompt) in prompts.iter().enumerate() {
        let t0 = b.probe.elapsed();
        let (back, outcomes, wall, cpu) = wave(&b.probe, slots, |_| prompt.clone()).await;
        slots = back;
        let peak = b.probe.peak(t0, b.probe.elapsed());
        let label = ["big command", "big file", "1 MB reply", "short prompt"][n];
        let line = format!(
            "{}  peak {:>7.1} MiB ({:>6.0} KiB/session over base)",
            wave_line(label, &outcomes, wall, cpu, sessions),
            mib(peak.footprint),
            (peak.footprint as f64 - base.footprint as f64) / 1024.0 / sessions as f64
        );
        b.line(line);
        let mut report = wave_json(&outcomes, wall, cpu, sessions);
        report["peak"] = mem_json(peak);
        waves.push(report);
    }
    let after = b.mem("after the four prompts").await;
    let heap = b.heap("after big outputs").await;
    let close = close_all(slots).await;
    b.line(close.line("session/close"));
    let closed = b.mem("after close").await;
    let heap_closed = b.heap("after close").await;
    let (bytes, files) = log_bytes(&b.env.home);
    b.line(format!(
        "  session logs: {files} files, {:.1} MiB",
        mib(bytes)
    ));
    b.put("waves", json!(waves));
    b.put(
        "mem",
        json!({"base": mem_json(base), "after": mem_json(after), "closed": mem_json(closed)}),
    );
    b.put("heap", json!({"after": heap, "closed": heap_closed}));
    b.finish().await
}

/// Waits for a viewer's task to end; returns when it saw the end and how many events.
async fn watch(events: &mut mpsc::UnboundedReceiver<Incoming>) -> (Option<Instant>, usize) {
    let mut count = 0;
    loop {
        let incoming = match tokio::time::timeout(WAIT, events.recv()).await {
            Ok(Some(incoming)) => incoming,
            _ => return (None, count),
        };
        if incoming.method() == "session/detached" {
            return (None, count);
        }
        if incoming.method() != "session/event" {
            continue;
        }
        count += 1;
        let kind = incoming.params()["event"]["type"].as_str().unwrap_or("");
        if matches!(kind, "task_completed" | "task_failed" | "task_interrupted") {
            return (Some(Instant::now()), count);
        }
    }
}

/// Many viewers per session, and one client per session that never reads.
async fn viewers(args: Args) -> std::io::Result<()> {
    let sessions = args.sessions;
    let per = args.agents;
    let mut b = Bench::start(args.clone(), gateway(&args), "").await?;
    let workspaces: Vec<PathBuf> = (0..sessions)
        .map(|i| b.env.workspace(&format!("ws{i}")))
        .collect();
    b.line(format!(
        "{sessions} sessions, {per} viewers each ({} viewer connections) and one stuck client each",
        sessions * per
    ));
    let mut slots = b.open(&workspaces).await;
    // Alone first, for comparison.
    let (back, outcomes, wall, cpu) = wave(&b.probe, slots, |i| {
        format!("[[mock:agent:solo{i}]] 检查 {i} 号模块")
    })
    .await;
    slots = back;
    let line = wave_line("agent, no viewers", &outcomes, wall, cpu, sessions);
    b.line(line);
    let alone = wave_json(&outcomes, wall, cpu, sessions);
    let base = b.mem("before viewers").await;

    let mut viewers = Vec::new();
    for slot in &slots {
        for _ in 0..per {
            let conn = connect(&b.env.socket).await;
            conn.client
                .request(
                    "session/attach",
                    json!({"session_id": slot.id, "history_limit": 20}),
                )
                .await
                .expect("session/attach");
            let events = conn.demux.subscribe(&slot.id);
            viewers.push((conn, events));
        }
    }
    let mut stuck = Vec::new();
    for slot in &slots {
        let mut stream = UnixStream::connect(&b.env.socket).await?;
        let hello = format!(
            "{}\n{}\n",
            json!({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocol_version": 2}}),
            json!({"jsonrpc": "2.0", "id": 2, "method": "session/attach", "params": {"session_id": slot.id}})
        );
        stream.write_all(hello.as_bytes()).await?;
        stuck.push(stream);
    }
    let attached = b.mem("viewers attached").await;
    b.line(format!(
        "  per viewer connection: {:.1} KiB",
        (attached.footprint as f64 - base.footprint as f64)
            / 1024.0
            / (sessions * (per + 1)) as f64
    ));

    let mut waves = Vec::new();
    for (n, label) in ["agent, watched", "1 MB reply, watched"].iter().enumerate() {
        let mut watching = Vec::new();
        for (conn, mut events) in viewers.drain(..) {
            watching.push(tokio::spawn(async move {
                let seen = watch(&mut events).await;
                (conn, events, seen)
            }));
        }
        let t0 = b.probe.elapsed();
        let started = Instant::now();
        let (back, outcomes, wall, cpu) = wave(&b.probe, slots, |i| {
            if n == 0 {
                format!("[[mock:agent:v{i}]] 检查 {i} 号模块")
            } else {
                "[[mock:big:1024]] 写一份很长的说明".to_string()
            }
        })
        .await;
        slots = back;
        let owner_end = started + Duration::from_secs_f64(wall);
        let peak = b.probe.peak(t0, b.probe.elapsed());
        let mut lags = Vec::new();
        let mut missing = 0;
        let mut short = 0;
        let owner_events = outcomes.iter().map(|o| o.events).max().unwrap_or(0);
        for task in watching {
            let (conn, events, (end, count)) = task.await.unwrap();
            match end {
                Some(end) => {
                    lags.push(end.saturating_duration_since(owner_end).as_secs_f64() * 1e3)
                }
                None => missing += 1,
            }
            if count + 5 < owner_events {
                short += 1;
            }
            viewers.push((conn, events));
        }
        let line = wave_line(label, &outcomes, wall, cpu, sessions);
        b.line(line);
        b.line(format!(
            "  viewers: {missing} missed the end, {short} saw fewer events than the owner"
        ));
        let lag = Dist::of(&lags);
        b.line(lag.line("viewer end after last owner end"));
        b.line(mem_line("peak while running", peak));
        let mut report = wave_json(&outcomes, wall, cpu, sessions);
        report["peak"] = mem_json(peak);
        report["viewer_missing"] = json!(missing);
        report["viewer_lag"] = lag.json();
        waves.push(report);
        b.mem("after wave").await;
    }
    let heap = b.heap("with viewers").await;
    drop(stuck);
    drop(viewers);
    let close = close_all(slots).await;
    b.line(close.line("session/close"));
    let closed = b.mem("after close").await;
    b.put("alone", alone);
    b.put("waves", json!(waves));
    b.put(
        "mem",
        json!({"base": mem_json(base), "attached": mem_json(attached), "closed": mem_json(closed)}),
    );
    b.put("heap", heap);
    b.finish().await
}

/// Starts tasks and interrupts them: while streaming, while a command runs, during an agent task.
async fn interrupt(args: Args) -> std::io::Result<()> {
    let sessions = args.sessions;
    let rounds = args.tasks.max(1);
    let mut b = Bench::start(args.clone(), gateway(&args), "").await?;
    let workspaces: Vec<PathBuf> = (0..sessions)
        .map(|i| b.env.workspace(&format!("ws{i}")))
        .collect();
    b.line(format!("{sessions} sessions × {rounds} interrupted tasks"));
    let base = b.mem("before").await;
    let slots = b.open(&workspaces).await;
    let heap_before = b.heap("idle sessions").await;
    let before = b.probe.now();
    let started = Instant::now();
    let mut running = Vec::new();
    for (i, mut slot) in slots.into_iter().enumerate() {
        running.push(tokio::spawn(async move {
            let mut latencies = Vec::new();
            let mut ends = Vec::new();
            for r in 0..rounds {
                let text = match (i + r) % 3 {
                    0 => "[[mock:slow:200]] 慢慢写".to_string(),
                    1 => "[[mock:bash:sleep 30]] 等一会儿".to_string(),
                    _ => format!("[[mock:agent:i{i}r{r}]] 检查 {i} 号模块"),
                };
                let op = json!({"session_id": slot.id, "op": {"type": "prompt", "input": [{"type": "text", "text": text}]}});
                slot.conn.client.request("session/op", op).await.expect("prompt");
                // 0.4–2.0 s, varying by session and round.
                let wait = 400 + ((i * 7919 + r * 104_729) % 1600) as u64;
                tokio::time::sleep(Duration::from_millis(wait)).await;
                let at = Instant::now();
                let op = json!({"session_id": slot.id, "op": {"type": "interrupt"}});
                slot.conn.client.request("session/op", op).await.expect("interrupt");
                let end = loop {
                    match tokio::time::timeout(WAIT, slot.events.recv()).await {
                        Ok(Some(incoming)) if incoming.method() == "session/event" => {
                            let kind = incoming.params()["event"]["type"].as_str().unwrap_or("").to_string();
                            if matches!(kind.as_str(), "task_completed" | "task_failed" | "task_interrupted") {
                                break kind;
                            }
                        }
                        Ok(Some(_)) => {}
                        _ => break "timed out".to_string(),
                    }
                };
                latencies.push(at.elapsed().as_secs_f64() * 1e3);
                ends.push(end);
            }
            (slot, latencies, ends)
        }));
    }
    let mut slots = Vec::new();
    let mut latencies = Vec::new();
    let mut interrupted = 0;
    let mut other = Vec::new();
    for task in running {
        let (slot, lat, ends) = task.await.unwrap();
        slots.push(slot);
        latencies.extend(lat);
        for end in ends {
            if end == "task_interrupted" {
                interrupted += 1;
            } else {
                other.push(end);
            }
        }
    }
    let wall = started.elapsed().as_secs_f64();
    let cpu_ms = Cost::between(before, b.probe.now()).cpu_ms;
    let lat = Dist::of(&latencies);
    b.line(format!(
        "  {} tasks in {wall:.1} s: {interrupted} interrupted, {} ended otherwise {:?}; CPU {cpu_ms:.0} ms",
        latencies.len(),
        other.len(),
        other.iter().take(3).collect::<Vec<_>>()
    ));
    b.line(lat.line("interrupt → task end"));
    // Every session still works.
    let (slots, outcomes, wall, cpu) = wave(&b.probe, slots, |i| {
        format!("[[mock:agent:after{i}]] 再检查一次")
    })
    .await;
    let line = wave_line("task after interrupts", &outcomes, wall, cpu, sessions);
    b.line(line);
    let after = b.mem("after").await;
    let leftover = std::process::Command::new("pgrep")
        .args(["-f", "sleep 30"])
        .output()?;
    let leftover = String::from_utf8_lossy(&leftover.stdout).lines().count();
    b.line(format!("  `sleep 30` processes left: {leftover}"));
    let heap_after = b.heap("after interrupts").await;
    close_all(slots).await;
    let closed = b.mem("after close").await;
    b.put("interrupt", json!({"tasks": latencies.len(), "interrupted": interrupted, "other": other, "latency": lat.json(), "cpu_ms": cpu_ms}));
    b.put("after_task", wave_json(&outcomes, wall, cpu, sessions));
    b.put("leftover_processes", json!(leftover));
    b.put(
        "mem",
        json!({"base": mem_json(base), "after": mem_json(after), "closed": mem_json(closed)}),
    );
    b.put("heap", json!({"before": heap_before, "after": heap_after}));
    b.finish().await
}

/// A large data directory: many sessions on disk, then restarts over them.
async fn startup(args: Args) -> std::io::Result<()> {
    let total = args.size.max(1) * 100;
    let options = MockOptions {
        ttft: Duration::ZERO,
        chunk_delay: Duration::ZERO,
    };
    let mut b = Bench::start(args.clone(), options, "").await?;
    let workspaces: Arc<Vec<PathBuf>> = Arc::new(
        (0..50)
            .map(|i| b.env.workspace(&format!("ws{i}")))
            .collect(),
    );
    b.line(format!(
        "writing {total} sessions (three tasks each) from 50 clients"
    ));
    let started = Instant::now();
    let mut writers = Vec::new();
    for w in 0..50 {
        let (socket, workspaces) = (b.env.socket.clone(), workspaces.clone());
        writers.push(tokio::spawn(async move {
            let conn = connect(&socket).await;
            let mut n = w;
            while n < total {
                let created = conn
                    .client
                    .request(
                        "session/new",
                        json!({"cwd": workspaces[n % workspaces.len()]}),
                    )
                    .await
                    .expect("session/new");
                let id = created["session_id"].as_str().unwrap().to_string();
                let mut events = conn.demux.subscribe(&id);
                for t in 0..3 {
                    let text = if t == 1 {
                        format!("[[mock:files]] 第 {n} 个会话")
                    } else {
                        format!("[[mock:echo]] 会话 {n} 第 {t} 次")
                    };
                    let outcome = run_prompt(&conn, &mut events, &id, &text).await;
                    assert!(outcome.ok, "{}", outcome.end);
                }
                conn.client
                    .request("session/close", json!({"session_id": id}))
                    .await
                    .expect("close");
                conn.demux.forget(&id);
                n += 50;
            }
        }));
    }
    for writer in writers {
        writer.await.unwrap();
    }
    let (bytes, files) = log_bytes(&b.env.home);
    b.line(format!(
        "  {:.1} s; {files} logs, {:.1} MiB",
        started.elapsed().as_secs_f64(),
        mib(bytes)
    ));
    let mut restarts = Vec::new();
    for _ in 0..5 {
        let ready = b.restart().await?;
        let conn = connect(&b.env.socket).await;
        let at = Instant::now();
        let mut listed = 0;
        let mut pages = 0;
        let mut cursor = Value::Null;
        loop {
            let mut params = json!({"limit": 200});
            if !cursor.is_null() {
                params["cursor"] = cursor.clone();
            }
            let page = conn
                .client
                .request("session/list", params)
                .await
                .expect("session/list");
            listed += page["sessions"].as_array().map_or(0, Vec::len);
            pages += 1;
            cursor = page.get("next_cursor").cloned().unwrap_or(Value::Null);
            if cursor.is_null() {
                break;
            }
        }
        let list_ms = at.elapsed().as_secs_f64() * 1e3;
        let at = Instant::now();
        let first = conn
            .client
            .request("session/list", json!({"limit": 50}))
            .await
            .expect("session/list");
        let page_ms = at.elapsed().as_secs_f64() * 1e3;
        let at = Instant::now();
        conn.client
            .request("sessions/subscribe", json!({}))
            .await
            .expect("sessions/subscribe");
        let subscribe_ms = at.elapsed().as_secs_f64() * 1e3;
        let id = first["sessions"][0]["session_id"]
            .as_str()
            .unwrap_or_default()
            .to_string();
        let at = Instant::now();
        conn.client
            .request(
                "session/resume",
                json!({"session_id": id, "history_limit": 50}),
            )
            .await
            .expect("resume");
        let resume_ms = at.elapsed().as_secs_f64() * 1e3;
        let sample = settle(&b.probe).await;
        if restarts.is_empty() {
            b.heap("after listing").await;
        }
        b.line(format!(
            "  restart: ready {ready:>6.2} ms  list all ({listed} in {pages} pages) {list_ms:>7.2} ms  list 50 {page_ms:>6.2} ms  subscribe {subscribe_ms:>7.2} ms  resume {resume_ms:>6.2} ms  footprint {:>6.1} MiB",
            mib(sample.footprint)
        ));
        restarts.push(json!({"ready_ms": ready, "list_all_ms": list_ms, "listed": listed, "list_50_ms": page_ms,
            "subscribe_ms": subscribe_ms, "resume_ms": resume_ms, "footprint_mib": mib(sample.footprint)}));
        drop(conn);
    }
    b.put("sessions", json!(total));
    b.put("logs", json!({"files": files, "mib": mib(bytes)}));
    b.put("restarts", json!(restarts));
    b.finish().await
}

/// A project for a churn task: the usual one, with the files the task works on.
fn churn_workspace(env: &Env, name: &str) -> PathBuf {
    let dir = env.workspace(name);
    std::fs::create_dir_all(dir.join("notes")).unwrap();
    std::fs::write(dir.join("notes/state.txt"), "# state\nTODO-init\n").unwrap();
    std::fs::write(dir.join("notes/log.txt"), "entry 0 at step 0\n").unwrap();
    dir
}

fn churn_prompt(turns: usize, seed: usize) -> String {
    format!("[[mock:churn:{turns}:{seed}]] 反复读写 notes/state.txt，记日志，偶尔搜索，直到做完")
}

/// What all churn tasks did so far.
#[derive(Default)]
struct ChurnCounts {
    turns: std::sync::atomic::AtomicUsize,
    compactions: std::sync::atomic::AtomicUsize,
    tool_errors: std::sync::atomic::AtomicUsize,
    errors: parking_lot::Mutex<Vec<String>>,
}

/// Runs one churn task to its end, counting turns, compactions and failed tool calls.
async fn churn_task(slot: &mut Slot, prompt: &str, counts: &ChurnCounts) -> TaskOutcome {
    use std::sync::atomic::Ordering::Relaxed;
    let started = Instant::now();
    let op = json!({"session_id": slot.id, "op": {"type": "prompt", "input": [{"type": "text", "text": prompt}]}});
    let mut outcome = TaskOutcome::default();
    if let Err(err) = slot.conn.client.request("session/op", op).await {
        outcome.end = format!("prompt rejected: {err}");
        return outcome;
    }
    loop {
        let incoming = match tokio::time::timeout(WAIT, slot.events.recv()).await {
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
        outcome.events += 1;
        match event["type"].as_str().unwrap_or("") {
            "turn_completed" => {
                counts.turns.fetch_add(1, Relaxed);
            }
            "compaction_completed" => {
                counts.compactions.fetch_add(1, Relaxed);
            }
            "tool_completed" if event["result"]["is_error"] == true => {
                counts.tool_errors.fetch_add(1, Relaxed);
                let mut errors = counts.errors.lock();
                if errors.len() < 5 {
                    errors.push(event["result"]["content"][0]["text"].as_str().unwrap_or("").chars().take(160).collect());
                }
            }
            kind @ ("task_completed" | "task_failed" | "task_interrupted") => {
                outcome.ok = kind == "task_completed";
                outcome.end = if outcome.ok {
                    kind.to_string()
                } else {
                    format!("{kind}: {}", event["error"]["message"].as_str().unwrap_or(""))
                };
                break;
            }
            _ => {}
        }
    }
    outcome.total_ms = started.elapsed().as_secs_f64() * 1e3;
    outcome
}

/// Samples taken while turns go by: (seconds, turns so far, the process).
type Timeline = Vec<(f64, usize, Sample)>;

/// The cost of each tenth of the turns: time, instructions and memory as the history grows.
fn churn_segments(timeline: &Timeline, total: usize) -> (Vec<String>, Vec<Value>) {
    let mut lines = Vec::new();
    let mut report = Vec::new();
    let at = |turns: usize| timeline.iter().find(|(_, t, _)| *t >= turns).copied();
    let parts = 10.min(total.max(1));
    for i in 0..parts {
        let (from, to) = (total * i / parts, total * (i + 1) / parts);
        let (Some(a), Some(b)) = (at(from), at(to)) else {
            continue;
        };
        if b.1 == a.1 {
            continue;
        }
        let turns = (b.1 - a.1).max(1) as f64;
        let ms = (b.0 - a.0) * 1e3 / turns;
        let kinstr = (b.2.instructions - a.2.instructions) as f64 / 1e3 / turns;
        let cpu_us = (b.2.cpu_ns - a.2.cpu_ns) as f64 / 1e3 / turns;
        lines.push(format!(
            "  turns {:>6}–{:<6} {ms:>7.3} ms/turn  CPU {cpu_us:>7.0} µs/turn  {kinstr:>8.0} k instr/turn  footprint {:>7.1} MiB  threads {:>3}  fds {:>4}",
            a.1,
            b.1,
            mib(b.2.footprint),
            b.2.threads,
            b.2.fds
        ));
        report.push(json!({"from": a.1, "to": b.1, "ms_per_turn": ms, "cpu_us_per_turn": cpu_us, "kinstr_per_turn": kinstr,
            "footprint_mib": mib(b.2.footprint), "threads": b.2.threads, "fds": b.2.fds}));
    }
    (lines, report)
}

/// Body sizes the gateway received, in KiB: mean, largest, last.
fn churn_bodies(mock: &MockServer) -> (f64, f64, f64, usize) {
    let bodies = mock.stats().churn_bodies();
    let kib = |b: u32| f64::from(b) / 1024.0;
    let mean = bodies.iter().map(|b| f64::from(*b)).sum::<f64>() / bodies.len().max(1) as f64 / 1024.0;
    let max = bodies.iter().copied().max().map_or(0.0, kib);
    let last = bodies.last().copied().map_or(0.0, kib);
    (mean, max, last, bodies.len())
}

/// Many turns in one task per session: does the cost of a turn, and memory, stay flat as the
/// history grows and is compacted again and again?
async fn churn(args: Args) -> std::io::Result<()> {
    use std::sync::atomic::Ordering::Relaxed;
    let sessions = args.sessions;
    let turns = args.turns;
    let mut b = Bench::start(args.clone(), gateway(&args), "").await?;
    let workspaces: Vec<PathBuf> = (0..sessions).map(|i| churn_workspace(&b.env, &format!("ws{i}"))).collect();
    b.line(format!("{sessions} session(s), one task of {turns} tool turns each"));
    let base = b.mem("before").await;
    let mut slots = b.open(&workspaces).await;
    let counts = Arc::new(ChurnCounts::default());
    let started = Instant::now();
    let before = b.probe.now();
    let mut running = Vec::new();
    for (i, mut slot) in slots.drain(..).enumerate() {
        let counts = counts.clone();
        running.push(tokio::spawn(async move {
            let outcome = churn_task(&mut slot, &churn_prompt(turns, i + 1), &counts).await;
            (slot, outcome)
        }));
    }
    let mut timeline: Timeline = vec![(0.0, 0, before)];
    while !running.iter().all(|t| t.is_finished()) {
        tokio::time::sleep(Duration::from_millis(100)).await;
        timeline.push((started.elapsed().as_secs_f64(), counts.turns.load(Relaxed), b.probe.now()));
    }
    let mut outcomes = Vec::new();
    for task in running {
        let (slot, outcome) = task.await.unwrap();
        slots.push(slot);
        outcomes.push(outcome);
    }
    let wall = started.elapsed().as_secs_f64();
    let after = b.probe.now();
    timeline.push((wall, counts.turns.load(Relaxed), after));
    let total_turns = counts.turns.load(Relaxed);
    let peak = b.probe.peak(0.0, b.probe.elapsed());
    let cost = Cost::between(before, after);
    let per = total_turns.max(1) as f64;
    b.line(format!(
        "  {total_turns} turns in {wall:.1} s ({:.0} turns/s), {} failed, {} compactions, {} tool errors; per turn: {:.3} ms wall, CPU {:.0} µs, {:.0} k instructions",
        per / wall,
        failures(&outcomes).len(),
        counts.compactions.load(Relaxed),
        counts.tool_errors.load(Relaxed),
        wall * 1e3 / per * sessions as f64,
        cost.cpu_ms * 1e3 / per,
        cost.minstr * 1e3 / per,
    ));
    for error in counts.errors.lock().iter() {
        b.line(format!("  tool error: {error}"));
    }
    for failure in failures(&outcomes).iter().take(3) {
        b.line(format!("  failure: {failure}"));
    }
    let (lines, segments) = churn_segments(&timeline, total_turns);
    for line in lines {
        b.line(line);
    }
    let (mean, max, last, requests) = churn_bodies(&b.mock);
    b.line(format!(
        "  gateway: {requests} requests, body mean {mean:.0} KiB, largest {max:.0} KiB, last {last:.0} KiB; {} handoffs",
        b.mock.stats().churn_handoffs.load(Relaxed)
    ));
    b.line(mem_line("peak while running", peak));
    let done = b.mem("after the tasks").await;
    b.line(format!(
        "  per session: {:.0} KiB footprint over the start",
        (done.footprint as f64 - base.footprint as f64) / 1024.0 / sessions.max(1) as f64
    ));
    let heap_done = b.heap("after the tasks").await;
    let (bytes, files) = log_bytes(&b.env.home);
    b.line(format!("  session logs: {files} files, {:.1} MiB", mib(bytes)));
    let first = slots[0].id.clone();
    let close = close_all(slots).await;
    b.line(close.line("session/close"));
    let closed = b.mem("after close").await;
    let heap_closed = b.heap("after close").await;

    // The longest log, opened again.
    let conn = connect(&b.env.socket).await;
    let at = Instant::now();
    let snapshot = conn
        .client
        .request("session/resume", json!({"session_id": first, "history_limit": 100}))
        .await
        .expect("session/resume");
    let resume_ms = at.elapsed().as_secs_f64() * 1e3;
    let resumed = b.mem("one session resumed").await;
    b.line(format!(
        "  session/resume of a {turns}-turn session: {resume_ms:.1} ms, snapshot {:.0} KiB, {:.0} KiB footprint",
        snapshot.to_string().len() as f64 / 1024.0,
        (resumed.footprint as f64 - closed.footprint as f64) / 1024.0
    ));
    let heap_resumed = b.heap("one session resumed").await;
    let _ = conn.client.request("session/close", json!({"session_id": first})).await;
    drop(conn);
    let ready = b.restart().await?;
    let conn = connect(&b.env.socket).await;
    let at = Instant::now();
    conn.client.request("session/list", json!({})).await.expect("session/list");
    let list_ms = at.elapsed().as_secs_f64() * 1e3;
    b.line(format!("  restart: ready {ready:.2} ms, first session/list {list_ms:.2} ms"));
    drop(conn);
    b.put("turns", json!(total_turns));
    b.put("wall_s", json!(wall));
    b.put("compactions", json!(counts.compactions.load(Relaxed)));
    b.put("tool_errors", json!(counts.tool_errors.load(Relaxed)));
    b.put("per_turn", json!({"cpu_us": cost.cpu_ms * 1e3 / per, "kinstr": cost.minstr * 1e3 / per}));
    b.put("segments", json!(segments));
    b.put("bodies_kib", json!({"mean": mean, "max": max, "last": last, "requests": requests}));
    b.put("mem", json!({"base": mem_json(base), "peak": mem_json(peak), "done": mem_json(done), "closed": mem_json(closed), "resumed": mem_json(resumed)}));
    b.put("heap", json!({"done": heap_done, "closed": heap_closed, "resumed": heap_resumed}));
    b.put("logs", json!({"files": files, "mib": mib(bytes)}));
    b.put("resume_ms", json!(resume_ms));
    b.put("restart", json!({"ready_ms": ready, "list_ms": list_ms}));
    b.finish().await
}

/// The churn task in one `her exec` process: start-up, then memory and the cost of a turn
/// over thousands of turns, without a service.
async fn exec_churn(args: Args) -> std::io::Result<()> {
    let turns = args.turns;
    let mock = MockServer::bind_with("127.0.0.1:0", gateway(&args)).await?;
    let env = Env::new(&mock);
    let ws = churn_workspace(&env, "ws0");
    let mut out = vec![format!("her-load --scenario exec-churn: one her exec task of {turns} tool turns")];
    let started = Instant::now();
    let mut child = tokio::process::Command::new(&args.her)
        .arg("exec")
        .arg("--cwd")
        .arg(&ws)
        .arg("--output")
        .arg("json")
        .arg(churn_prompt(turns, 1))
        .current_dir(&ws)
        .env("HER_HOME", &env.home)
        .env_remove("HER_MODEL")
        .env_remove("HER_PERMISSION_MODE")
        .env_remove("HER_LOG")
        .envs(args.service_env.iter().filter_map(|item| item.split_once('=')))
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::inherit())
        .kill_on_drop(true)
        .spawn()?;
    let pid = child.id().expect("pid") as i32;
    let probe = Probe::start(pid, Duration::from_millis(10));
    let stats = mock.stats();
    let requests = || stats.requests.load(std::sync::atomic::Ordering::Relaxed) as usize;
    // Start-up: spawn until the first request reaches the gateway.
    while requests() == 0 && started.elapsed() < Duration::from_secs(10) {
        tokio::time::sleep(Duration::from_micros(200)).await;
    }
    let first_ms = started.elapsed().as_secs_f64() * 1e3;
    let mut timeline: Timeline = vec![(0.0, 0, probe.now())];
    let mut stdout = child.stdout.take().expect("stdout");
    let reader = tokio::spawn(async move {
        let mut text = String::new();
        let _ = tokio::io::AsyncReadExt::read_to_string(&mut stdout, &mut text).await;
        text
    });
    let status = loop {
        if let Some(status) = child.try_wait()? {
            break status;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
        timeline.push((started.elapsed().as_secs_f64(), requests(), probe.now()));
    };
    let wall = started.elapsed().as_secs_f64();
    probe.stop();
    // The process is gone: its last sample is the last one taken while it ran.
    let last = timeline.iter().rev().map(|(_, _, s)| *s).find(|s| s.cpu_ns > 0).unwrap_or_default();
    let result: Value = serde_json::from_str(reader.await.unwrap_or_default().trim()).unwrap_or(Value::Null);
    let total = requests();
    let first = timeline[0].2;
    out.push(format!("  spawn → first model request          {first_ms:>9.2} ms"));
    out.push(format!(
        "  {total} requests in {wall:.1} s ({:.0} per s), exit {:?}, result {}",
        total as f64 / wall,
        status.code(),
        result.get("status").or(result.get("outcome")).or(result.get("result")).map_or("?".to_string(), Value::to_string)
    ));
    let (lines, segments) = churn_segments(&timeline, total);
    out.extend(lines);
    let (mean, max, last_body, _) = churn_bodies(&mock);
    out.push(format!(
        "  gateway: body mean {mean:.0} KiB, largest {max:.0} KiB, last {last_body:.0} KiB; {} handoffs",
        stats.churn_handoffs.load(std::sync::atomic::Ordering::Relaxed)
    ));
    out.push(format!(
        "  peak footprint {:.1} MiB, CPU {:.0} ms ({:.0} µs per request), {:.0} M instructions",
        mib(last.peak_footprint),
        (last.cpu_ns - first.cpu_ns) as f64 / 1e6,
        (last.cpu_ns - first.cpu_ns) as f64 / 1e3 / total.max(1) as f64,
        (last.instructions - first.instructions) as f64 / 1e6
    ));
    for line in &out {
        let _ = writeln!(std::io::stdout(), "{line}");
    }
    if let Some(path) = &args.json {
        let report = json!({"turns": turns, "requests": total, "first_request_ms": first_ms, "wall_s": wall,
            "peak_footprint_mib": mib(last.peak_footprint), "segments": segments,
            "bodies_kib": {"mean": mean, "max": max, "last": last_body}});
        std::fs::write(path, serde_json::to_string_pretty(&report).unwrap())?;
    }
    Ok(())
}
