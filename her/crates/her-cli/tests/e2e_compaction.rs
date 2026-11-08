//! End to end: the real `her` binary against a simulated model service (see `mock_model`),
//! with a small context window so that long tasks have to compact.

#![allow(clippy::print_stderr)]

mod mock_model;

use mock_model::Behavior;
use mock_model::FINISHED;
use mock_model::HANDOFF_TAG;
use mock_model::Kind;
use mock_model::MockModel;
use mock_model::Recorded;
use mock_model::STEP_MARKER;
use mock_model::SUMMARY_TITLE;
use serde_json::Value;
use std::path::Path;
use std::path::PathBuf;
use std::process::Command;

/// Configured window 16,000 with replies up to 2,000: hard limit 14,000, soft limit 12,600,
/// tool results up to 5,600 bytes.
const WINDOW: u64 = 16_000;
const SOFT: u64 = 12_600;
const HARD: u64 = 14_000;
const TASK: &str = "Run the steps of the job one at a time until all of them are complete.";

/// A directory removed when dropped.
struct TempDir(PathBuf);

impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!("her-e2e-{}-{}", std::process::id(), rand_suffix()));
        std::fs::create_dir_all(&path).unwrap();
        TempDir(path)
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

/// Unique within the process: tests run in parallel and the clock may not tell them apart.
fn rand_suffix() -> String {
    static NEXT: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
    let n = NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    format!("{nanos}-{n}")
}

struct Env {
    home: PathBuf,
    work: PathBuf,
    _dir: TempDir,
}

fn env(server: &MockModel) -> Env {
    let dir = TempDir::new();
    let home = dir.0.join("home");
    let work = dir.0.join("work");
    std::fs::create_dir_all(&home).unwrap();
    std::fs::create_dir_all(&work).unwrap();
    std::fs::write(
        home.join("models.toml"),
        format!(
            r#"
[providers.mock]
api = "openai-chat"
base_url = "{}"
api_key = "sk-mock"
compat = {{ max_tokens_field = "max_tokens" }}

[[providers.mock.models]]
id = "mock-model"
context_window = {WINDOW}
max_output_tokens = 2000
"#,
            server.base_url()
        ),
    )
    .unwrap();
    std::fs::write(home.join("config.toml"), "model = \"mock/mock-model\"\n").unwrap();
    Env {
        home,
        work,
        _dir: dir,
    }
}

/// Runs `her exec` with `args` and returns the exit code and the event stream.
fn her(env: &Env, args: &[&str]) -> (i32, Vec<Value>) {
    let output = Command::new(env!("CARGO_BIN_EXE_her"))
        .args(["exec", "--output", "stream-json", "--approval", "full-auto"])
        .args(args)
        .current_dir(&env.work)
        .env("HER_HOME", &env.home)
        // The developer's own skills would change the system prompt and every size measured here.
        .env("HOME", &env.home)
        .env_remove("HER_MODEL")
        .env_remove("HER_APPROVAL")
        .output()
        .expect("run her");
    let stdout = String::from_utf8_lossy(&output.stdout);
    let events = stdout
        .lines()
        .filter_map(|l| serde_json::from_str::<Value>(l).ok())
        .collect();
    let code = output.status.code().unwrap_or(-1);
    if code != 0 {
        eprintln!("her stderr:\n{}", String::from_utf8_lossy(&output.stderr));
    }
    (code, events)
}

fn of_type<'a>(events: &'a [Value], kind: &str) -> Vec<&'a Value> {
    events.iter().filter(|e| e["type"] == kind).collect()
}

fn last_message(events: &[Value]) -> String {
    of_type(events, "task_completed")
        .last()
        .and_then(|e| e["last_message"].as_str())
        .unwrap_or_default()
        .to_string()
}

fn session_file(events: &[Value]) -> PathBuf {
    PathBuf::from(of_type(events, "session_started")[0]["session_file"].as_str().unwrap())
}

fn log_entries(path: &Path, kind: &str) -> Vec<Value> {
    std::fs::read_to_string(path)
        .unwrap()
        .lines()
        .skip(1)
        .map(|l| serde_json::from_str::<Value>(l).unwrap())
        .filter(|v| v["type"] == kind)
        .collect()
}

fn text(message: &Value) -> String {
    mock_model::content_text(message)
}

/// Everything in a request except the messages: model, tools, limits, options.
fn settings(request: &Recorded) -> Value {
    let mut body = request.body.clone();
    body.as_object_mut().unwrap().remove("messages");
    body
}

#[test]
fn a_long_task_compacts_repeatedly_reuses_the_cache_and_resumes() {
    let server = MockModel::start(Behavior {
        steps: 24,
        lines_per_step: 2000,
        read_back_after: Some(2),
        window: None,
    });
    let env = env(&server);
    let (code, events) = her(&env, &[TASK]);
    assert_eq!(code, 0);
    assert_eq!(last_message(&events), format!("{FINISHED}: 24"));

    // Several compactions near the limit: at the soft limit, or one turn early when the next
    // turn would not fit.
    let compactions = of_type(&events, "compaction_completed");
    assert!(compactions.len() >= 2, "{} compaction(s)", compactions.len());
    for (i, c) in compactions.iter().enumerate() {
        assert_eq!(c["reason"], "threshold");
        assert_eq!(c["compaction_seq"], i as u64 + 1);
        assert!(c["seq"].as_u64().unwrap() > i as u64 + 1);
        let before = c["tokens_before"].as_u64().unwrap();
        assert!((HARD / 2..HARD).contains(&before), "{before}");
        assert!(c["tokens_after"].as_u64().unwrap() < SOFT / 2);
        assert!(c["summary"].as_str().unwrap().starts_with(SUMMARY_TITLE));
    }

    let requests = server.requests();
    let handoffs: Vec<usize> = (0..requests.len()).filter(|&i| requests[i].kind == Kind::Handoff).collect();
    assert_eq!(handoffs.len(), compactions.len());
    for &h in &handoffs {
        let handoff = &requests[h];
        let previous = &requests[h - 1];
        assert_eq!(previous.kind, Kind::Turn);
        // Same model, tools and options, and the previous request's messages as its prefix:
        // the request reuses the provider's cache instead of starting over.
        assert_eq!(settings(handoff), settings(previous));
        let shared = previous.messages().len();
        assert_eq!(&handoff.messages()[..shared], previous.messages());
        let added: Vec<&str> = handoff.messages()[shared..]
            .iter()
            .map(|m| m["role"].as_str().unwrap())
            .collect();
        assert_eq!(added, ["assistant", "tool"], "the handoff request joins the tool result");
        assert!(text(handoff.messages().last().unwrap()).contains(HANDOFF_TAG));
        let hit = handoff.cached_tokens as f64 / handoff.prompt_tokens as f64;
        assert!(hit > 0.8, "cache hit {hit:.2} for the summary request");
        // Compacting in time keeps the summary request itself inside the window.
        let reply_room = handoff.body["max_tokens"].as_u64().unwrap();
        assert!(handoff.prompt_tokens + reply_room <= WINDOW, "{}", handoff.prompt_tokens);

        // The next request starts from the system prompt, the task in the user's words and
        // the summary; the summary carries the progress.
        let after = &requests[h + 1];
        assert_eq!(after.kind, Kind::Turn);
        let messages = after.messages();
        assert_eq!(messages[0]["role"], "system");
        assert_eq!(messages[0], previous.messages()[0], "the system prompt never changes");
        assert_eq!(text(&messages[1]), TASK);
        let summary = text(&messages[2]);
        assert!(summary.starts_with("<conversation_summary>"));
        assert!(summary.contains(SUMMARY_TITLE) && summary.contains(&format!("{STEP_MARKER}1")));
        assert_eq!(messages.len(), 3);
    }

    // Between compactions every request extends the previous one.
    for pair in requests.windows(2) {
        let (a, b) = (&pair[0], &pair[1]);
        if a.kind == Kind::Turn && b.kind == Kind::Turn && b.messages().len() > 3 {
            assert_eq!(&b.messages()[..a.messages().len()], a.messages(), "append-only between compactions");
        }
    }

    // The model read the cut part of an output back from the saved file.
    // The model read the whole cut part back, page by page, and the head, the pages and the
    // tail put together are exactly what the command printed.
    let read_back = server.read_back().expect("the agent read an output back");
    assert!(read_back.done && read_back.verified, "{read_back:?}");
    assert!(read_back.pages >= 2, "{} page(s)", read_back.pages);

    // The session log: one line per compaction, with the summary request's usage.
    let log = session_file(&events);
    let entries = log_entries(&log, "compaction");
    assert_eq!(entries.len(), compactions.len());
    for entry in &entries {
        assert_eq!(entry["model"], "mock/mock-model");
        assert!(entry["usage"]["cache_read"].as_u64().unwrap() > 0);
        assert_eq!(entry["retained"][0]["content"][0]["text"], TASK);
    }
    // Every step's output was cut and saved next to the log.
    let saved: Vec<PathBuf> = std::fs::read_dir(log.with_extension("").join("tool-output"))
        .unwrap()
        .map(|e| e.unwrap().path())
        .collect();
    assert!(saved.len() >= 24, "{} saved outputs", saved.len());
    for path in &saved {
        let content = std::fs::read_to_string(path).unwrap();
        assert!(content.starts_with(STEP_MARKER), "{}", path.display());
        assert_eq!(content.lines().count(), 2001);
    }

    // Resume: the context continues from the last checkpoint, and the context size starts
    // from the usage in the log.
    let last_reply = requests.iter().rev().find(|r| r.kind == Kind::Turn).unwrap();
    let session_id = of_type(&events, "session_started")[0]["session_id"].as_str().unwrap().to_string();
    let (code, resumed) = her(&env, &["--resume", &session_id, "Is everything done?"]);
    assert_eq!(code, 0);
    assert_eq!(last_message(&resumed), format!("{FINISHED}: 24"));
    let turn = of_type(&resumed, "turn_started")[0];
    let context = &turn["context"];
    let reported = context["tokens"].as_u64().unwrap() - context["estimated"].as_u64().unwrap();
    assert_eq!(reported, last_reply.prompt_tokens + last_reply.completion_tokens);
    let requests_after = server.requests();
    let first = &requests_after[requests.len()];
    let messages = first.messages();
    assert_eq!(text(&messages[1]), TASK);
    assert!(text(&messages[2]).contains(&format!("{SUMMARY_TITLE}{}", compactions.len())));
    assert_eq!(text(messages.last().unwrap()), "Is everything done?");
    assert_eq!(
        &messages[..last_reply.messages().len()],
        last_reply.messages(),
        "the resumed request extends the last one before the resume"
    );

    // `/context` from the terminal: the report, without a request to the model. Its size is the
    // provider's count of the last reply plus an estimate of what came after, as a turn checks
    // it, and the parts add up to it.
    let requests = server.requests();
    let last_reply = requests.iter().rev().find(|r| r.kind == Kind::Turn).unwrap();
    let tasks = log_entries(&log, "task").len();
    assert!(tasks > 0);
    let (code, asked) = her(&env, &["--resume", &session_id, "/context"]);
    assert_eq!(code, 0);
    assert_eq!(server.requests().len(), requests.len(), "no request to the model");
    assert_eq!(log_entries(&log, "task").len(), tasks, "a report leaves no task in the log");
    let report = &of_type(&asked, "context_reported")[0]["report"];
    assert_eq!(report["model"], "mock/mock-model");
    assert_eq!(report["usage"]["window"], WINDOW);
    let tokens = report["usage"]["tokens"].as_u64().unwrap();
    let reported = tokens - report["usage"]["estimated"].as_u64().unwrap();
    assert_eq!(reported, last_reply.prompt_tokens + last_reply.completion_tokens);
    let parts = report["parts"].as_array().unwrap();
    assert_eq!(parts[0]["kind"], "messages");
    assert!(parts[0]["tokens"].as_u64().unwrap() > 0);
    assert_eq!(parts.iter().map(|p| p["tokens"].as_u64().unwrap()).sum::<u64>(), tokens);
    // As text, for a person.
    let output = Command::new(env!("CARGO_BIN_EXE_her"))
        .args(["exec", "--resume", &session_id, "/context"])
        .current_dir(&env.work)
        .env("HER_HOME", &env.home)
        .env("HOME", &env.home)
        .env_remove("HER_MODEL")
        .env_remove("HER_APPROVAL")
        .output()
        .expect("run her");
    let text = String::from_utf8_lossy(&output.stdout);
    assert!(text.starts_with("Context · mock/mock-model\n"), "{text}");
    assert!(text.contains("  Messages ") && text.contains("  Free "), "{text}");
}

#[test]
fn an_overflow_reported_by_the_provider_is_compacted_and_retried() {
    // The provider's real window (9,000) is smaller than the configured one, so the soft
    // limit is never reached; the provider rejects requests instead.
    let server = MockModel::start(Behavior {
        steps: 12,
        lines_per_step: 2000,
        read_back_after: None,
        window: Some(9_000),
    });
    let env = env(&server);
    let (code, events) = her(&env, &[TASK]);
    assert_eq!(code, 0);
    assert_eq!(last_message(&events), format!("{FINISHED}: 12"));
    let compactions = of_type(&events, "compaction_completed");
    assert!(!compactions.is_empty());
    assert!(compactions.iter().all(|c| c["reason"] == "overflow"));

    let requests = server.requests();
    let rejected = requests.iter().filter(|r| r.kind == Kind::Rejected).count();
    assert!(rejected >= compactions.len(), "{rejected} rejected request(s)");
    // Each overflow is followed by a summary request; when that is too large as well, it is
    // trimmed until it fits, and the summary still lets the work finish.
    for (i, request) in requests.iter().enumerate() {
        if request.kind == Kind::Rejected && requests[i - 1].kind != Kind::Rejected {
            assert!(
                requests[i + 1..]
                    .iter()
                    .take_while(|r| r.kind != Kind::Turn)
                    .any(|r| r.kind == Kind::Handoff),
                "no summary request after the overflow at {i}"
            );
        }
    }
    let log = session_file(&events);
    assert_eq!(log_entries(&log, "compaction").len(), compactions.len());
}
