//! Shared harness for the end-to-end and load tests: a mock model service, a private
//! HER_HOME pointing at it, a private HOME (so the developer's own skills stay out), and
//! helpers to drive the real `her` binary.

#![allow(dead_code)]

use her_mock::MockServer;
use her_rpc::Client;
use her_rpc::Incoming;
use her_rpc::IncomingRx;
use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;
use std::process::Stdio;
use std::sync::Arc;
use std::sync::OnceLock;
use std::time::Duration;
use std::time::Instant;
use std::time::SystemTime;
use std::time::UNIX_EPOCH;
use tempfile::TempDir;
use tokio::process::Child;
use tokio::process::Command;
use tokio::sync::mpsc;

pub const WAIT: Duration = Duration::from_secs(60);

pub struct TestEnv {
    pub mock: MockServer,
    pub home: TempDir,
    pub work: TempDir,
}

impl TestEnv {
    /// A home whose default model is the mock, with `permission_mode` and extra config lines.
    pub async fn new(permission_mode: &str, extra_config: &str) -> TestEnv {
        let mock = MockServer::start().await.expect("mock server");
        let home = tempfile::tempdir().expect("home");
        let work = tempfile::tempdir().expect("work");
        // Built-in providers a test gives a key to ask the mock, never the real service.
        let models = format!("{}\n[providers.deepseek]\nbase_url = \"{}\"\n", mock.models_toml(), mock.base_url());
        std::fs::write(home.path().join("models.toml"), models).unwrap();
        std::fs::write(
            home.path().join("config.toml"),
            format!(
                "model = \"mock/mock-1\"\npermission_mode = \"{permission_mode}\"\n{extra_config}\n[llm]\nmax_retries = 2\n"
            ),
        )
        .unwrap();
        std::fs::create_dir_all(home.path().join("user")).unwrap();
        TestEnv { mock, home, work }
    }

    pub fn work(&self) -> &Path {
        self.work.path()
    }

    /// The HOME that `her` sees: `~/.agents/skills` and the like live here.
    pub fn user_home(&self) -> PathBuf {
        self.home.path().join("user")
    }

    /// A fresh, canonical workspace directory under the test's work dir.
    pub fn workspace(&self, name: &str) -> PathBuf {
        let dir = self.work.path().join(name);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::canonicalize(dir).unwrap()
    }

    pub fn her(&self, args: &[&str]) -> Command {
        self.her_in(self.work.path(), args)
    }

    pub fn her_in(&self, cwd: &Path, args: &[&str]) -> Command {
        let mut cmd = Command::new(env!("CARGO_BIN_EXE_her"));
        cmd.args(args)
            .current_dir(cwd)
            .env("HER_HOME", self.home.path())
            .env("HOME", self.user_home())
            .env_remove("XDG_DATA_HOME")
            // Saved keys go to a file in the test's home, never the developer's keychain.
            .env("HER_CREDENTIAL_STORE", "file")
            // Sign-ins print their address; tests play the browser themselves.
            .env("HER_NO_BROWSER", "1")
            // What it says to people does not depend on the machine's locale.
            .env("HER_LANG", "en")
            // The public model catalog comes from the mock; the developer's keys stay out.
            .env("HER_MODEL_CATALOG_URL", self.mock.catalog_url())
            .env_remove("DEEPSEEK_API_KEY")
            .env_remove("OPENAI_API_KEY")
            .env_remove("ANTHROPIC_API_KEY")
            .env_remove("DASHSCOPE_API_KEY")
            .env_remove("MOONSHOT_API_KEY")
            .env_remove("ZHIPUAI_API_KEY")
            .env_remove("OPENROUTER_API_KEY")
            .env_remove("TAVILY_API_KEY")
            .env_remove("BRAVE_SEARCH_API_KEY")
            .env_remove("BOCHA_API_KEY")
            .env_remove("EXA_API_KEY")
            .env_remove("PARALLEL_API_KEY")
            .env_remove("BAILIAN_TOKEN_PLAN_API_KEY")
            .env_remove("TENCENT_TOKEN_PLAN_API_KEY")
            .env_remove("VOLCENGINE_AGENT_PLAN_API_KEY")
            .env_remove("VOLCENGINE_CODING_PLAN_API_KEY")
            .env_remove("ZAI_CODING_PLAN_API_KEY")
            .env_remove("ZHIPU_CODING_PLAN_API_KEY")
            .env_remove("HER_MODEL")
            .env_remove("HER_APPROVAL")
            .env_remove("HER_LOG")
            .kill_on_drop(true);
        cmd
    }

    /// Starts `her <args>` with piped stdio and a JSON-RPC client on it.
    pub fn spawn_rpc(&self, args: &[&str]) -> (Child, Client, IncomingRx) {
        let mut child = self
            .her(args)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .expect("spawn her");
        let stdout = child.stdout.take().unwrap();
        let stdin = child.stdin.take().unwrap();
        let (client, incoming) = Client::connect(stdout, stdin);
        (child, client, incoming)
    }
}

pub async fn next(rx: &mut IncomingRx) -> Incoming {
    tokio::time::timeout(WAIT, rx.recv())
        .await
        .expect("timed out waiting for a message")
        .expect("connection closed")
}

/// Waits for the child to exit and returns its exit code.
pub async fn exit_code(child: &mut Child) -> i32 {
    let status = tokio::time::timeout(WAIT, child.wait())
        .await
        .expect("timed out waiting for exit")
        .expect("wait");
    status.code().unwrap_or(-1)
}

/// The session a notification or request belongs to (native or ACP field name).
pub fn session_of(incoming: &Incoming) -> Option<String> {
    let params = incoming.params();
    params
        .get("session_id")
        .or_else(|| params.get("sessionId"))
        .and_then(Value::as_str)
        .map(str::to_string)
}

/// Routes incoming messages to per-session channels; messages for a session nobody has
/// subscribed to yet are kept until someone does.
#[derive(Clone)]
pub struct Demux {
    state: Arc<Mutex<DemuxState>>,
}

#[derive(Default)]
struct DemuxState {
    senders: HashMap<String, mpsc::UnboundedSender<Incoming>>,
    buffered: HashMap<String, Vec<Incoming>>,
}

impl Demux {
    pub fn spawn(mut rx: IncomingRx) -> Demux {
        let demux = Demux {
            state: Arc::new(Mutex::new(DemuxState::default())),
        };
        let state = demux.state.clone();
        tokio::spawn(async move {
            while let Some(incoming) = rx.recv().await {
                let Some(session) = session_of(&incoming) else { continue };
                let mut state = state.lock();
                match state.senders.get(&session) {
                    Some(tx) => {
                        let _ = tx.send(incoming);
                    }
                    None => state.buffered.entry(session).or_default().push(incoming),
                }
            }
            state.lock().senders.clear();
        });
        demux
    }

    pub fn subscribe(&self, session: &str) -> mpsc::UnboundedReceiver<Incoming> {
        let (tx, rx) = mpsc::unbounded_channel();
        let mut state = self.state.lock();
        for incoming in state.buffered.remove(session).unwrap_or_default() {
            let _ = tx.send(incoming);
        }
        state.senders.insert(session.to_string(), tx);
        rx
    }
}

pub async fn recv(rx: &mut mpsc::UnboundedReceiver<Incoming>) -> Incoming {
    tokio::time::timeout(WAIT, rx.recv())
        .await
        .expect("timed out waiting for a session message")
        .expect("connection closed")
}

/// Wall-clock microseconds of a monotonic instant (for comparing with the mock's stamps).
pub fn unix_us(at: Instant) -> f64 {
    static BASE: OnceLock<(Instant, f64)> = OnceLock::new();
    let (base_at, base_us) = *BASE.get_or_init(|| {
        (Instant::now(), SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_micros() as f64)
    });
    if at >= base_at {
        base_us + (at - base_at).as_micros() as f64
    } else {
        base_us - (base_at - at).as_micros() as f64
    }
}

/// Latencies of the `@<us>;` stamps in a delta that arrived at `received`.
pub fn stamp_latencies(text: &str, received: Instant, out: &mut Vec<f64>) {
    let at = unix_us(received);
    for stamp in text.split(';').filter_map(|s| s.strip_prefix('@')).filter_map(|s| s.parse::<f64>().ok()) {
        out.push(at - stamp);
    }
}

/// What one native prompt produced.
#[derive(Debug, Default)]
pub struct NativeTurn {
    pub events: Vec<Value>,
    /// Concatenated text deltas.
    pub text: String,
    pub end: Value,
    pub approvals: usize,
    /// Mock wrote the chunk → this client received the event, in microseconds.
    pub stamp_us: Vec<f64>,
}

impl NativeTurn {
    pub fn types(&self) -> Vec<&str> {
        self.events.iter().filter_map(|e| e["type"].as_str()).collect()
    }
}

/// Sends a prompt through `session/op` and collects that session's events until the task ends,
/// approving every request when `approve` is set.
pub async fn native_prompt(
    client: &Client,
    events: &mut mpsc::UnboundedReceiver<Incoming>,
    session: &str,
    text: &str,
    approve: bool,
) -> NativeTurn {
    let op = json!({"session_id": session, "op": {"type": "prompt", "input": [{"type": "text", "text": text}]}});
    client.request("session/op", op).await.expect("prompt accepted");
    let mut turn = NativeTurn::default();
    loop {
        let incoming = recv(events).await;
        if incoming.method() == "session/detached" {
            panic!("session {session} was detached: {}", incoming.params());
        }
        if incoming.method() != "session/event" {
            continue;
        }
        let event = incoming.params()["event"].clone();
        let kind = event["type"].as_str().unwrap_or("").to_string();
        if kind == "message_delta" && event["delta"]["type"] == "text" {
            let text = event["delta"]["text"].as_str().unwrap_or("");
            if let Incoming::Notification { received, .. } = &incoming {
                stamp_latencies(text, *received, &mut turn.stamp_us);
            }
            turn.text.push_str(text);
        }
        if kind == "approval_requested" && approve {
            turn.approvals += 1;
            let op = json!({"session_id": session, "op": {"type": "approval", "call_id": event["call_id"],
                "decision": {"type": "approve_for_session"}}});
            client.request("session/op", op).await.expect("approval accepted");
        }
        let end = matches!(kind.as_str(), "task_completed" | "task_failed" | "task_interrupted");
        turn.events.push(event.clone());
        if end {
            turn.end = event;
            return turn;
        }
    }
}

pub async fn native_init(client: &Client) {
    let result = client
        .request("initialize", json!({"protocol_version": 2, "client_info": {"name": "e2e"}}))
        .await
        .expect("initialize");
    assert_eq!(result["protocol_version"], 2);
}

/// Checks that sequence numbers only grow.
pub fn assert_increasing(events: &[Value]) {
    let seqs: Vec<u64> = events.iter().filter_map(|e| e["seq"].as_u64()).collect();
    assert!(
        seqs.windows(2).all(|w| w[0] < w[1]),
        "sequence numbers must strictly increase: {seqs:?}"
    );
}

/// Checks messages against the exported protocol schema (`her protocol schema`).
pub struct SchemaCheck {
    validators: HashMap<String, jsonschema::Validator>,
}

impl SchemaCheck {
    /// Validators for the named definitions.
    pub fn new(names: &[&str]) -> SchemaCheck {
        let doc = her_protocol::schema::document();
        let validators = names
            .iter()
            .map(|name| {
                let schema = json!({
                    "$schema": "http://json-schema.org/draft-07/schema#",
                    "$ref": format!("#/definitions/{name}"),
                    "definitions": doc["definitions"],
                });
                let validator = jsonschema::draft7::new(&schema).unwrap_or_else(|e| panic!("schema for {name}: {e}"));
                (name.to_string(), validator)
            })
            .collect();
        SchemaCheck { validators }
    }

    pub fn check(&self, name: &str, value: &Value) {
        let validator = self.validators.get(name).unwrap_or_else(|| panic!("no validator for {name}"));
        let errors: Vec<String> = validator.iter_errors(value).map(|e| format!("{e} at {}", e.instance_path())).collect();
        assert!(errors.is_empty(), "{name} does not match the protocol schema: {errors:?}\n{value:#}");
    }
}

pub fn percentile(sorted: &[f64], p: f64) -> f64 {
    if sorted.is_empty() {
        return 0.0;
    }
    let rank = ((sorted.len() - 1) as f64 * p).round() as usize;
    sorted[rank.min(sorted.len() - 1)]
}
