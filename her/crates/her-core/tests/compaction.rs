//! Context management end to end in the kernel: usage accounting, handoff-summary compaction
//! and write-time truncation of tool results, with a scripted model, real tools and a real
//! JSONL session log.

use her_core::Config;
use her_core::HerHome;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::KernelDeps;
use her_core::ResumeOptions;
use her_core::SessionRef;
use her_core::StartOptions;
use her_core::TaskEnd;
use her_core::TaskResult;
use her_core::context::HANDOFF_REQUEST;
use her_llm::Catalog;
use her_llm::ErrorKind;
use her_llm::Message;
use her_llm::ModelRegistry;
use her_llm::ProviderConfig;
use her_llm::RegistryOptions;
use her_llm::ThinkingLevel;
use her_llm::ToolChoice;
use her_llm::ToolSpec;
use her_llm::UserContent;
use her_llm::api::scripted::Script;
use her_llm::catalog::ModelToml;
use her_llm::api::scripted::ScriptedApi;
use her_protocol::PermissionMode;
use her_protocol::CompactionReason;
use her_protocol::ContextUsage;
use her_protocol::EventMsg;
use her_protocol::Op;
use her_tools::BashTool;
use her_tools::LocalEnvironment;
use her_tools::Tool;
use her_tools::ToolCtx;
use her_tools::ToolError;
use her_tools::ToolOutput;
use her_tools::ToolRegistry;
use her_tools::builtin_registry;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;
use tempfile::TempDir;

/// The default test model: window 20,000, replies up to 2,000. Hard limit 18,000, soft 16,200.
const SMALL: &str = "test/scripted";
const BIG: &str = "test/scripted-big";
const SOFT: u64 = 16_200;
const HARD: u64 = 18_000;

fn entry(id: &str, window: u32) -> ModelToml {
    ModelToml { name: Some(id.into()), context_window: Some(window), max_output_tokens: Some(2_000), ..ModelToml::new(id) }
}

fn provider() -> ProviderConfig {
    ScriptedApi::provider_with(vec![entry("scripted", 20_000), entry("scripted-big", 60_000)])
}

/// Returns a large output, to exercise the kernel's safety net for any tool.
struct BigTool {
    spec: ToolSpec,
}

impl BigTool {
    fn new() -> Self {
        Self {
            spec: ToolSpec {
                name: "big".into(),
                description: "Prints a lot.".into(),
                parameters: json!({"type": "object", "properties": {}}),
            },
        }
    }
}

#[async_trait::async_trait]
impl Tool for BigTool {
    fn spec(&self) -> &ToolSpec {
        &self.spec
    }

    async fn execute(&self, _args: Value, _ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        Ok(ToolOutput::text(big_text()))
    }
}

fn big_text() -> String {
    (1..=3000).map(|n| format!("record {n}\n")).collect()
}

fn deps(api: &Arc<ScriptedApi>, work: &Path, home: &HerHome) -> KernelDeps {
    let mut registry = ModelRegistry::new(Catalog::default(), RegistryOptions::default());
    registry.register_api(api.clone());
    registry.insert_provider(provider());
    let mut tools: ToolRegistry = builtin_registry(BashTool::default());
    tools.register(Arc::new(BigTool::new())).unwrap();
    KernelDeps {
        llm: Arc::new(registry),
        tools: Arc::new(tools),
        env: Arc::new(LocalEnvironment::new(work.to_path_buf())),
        store: Arc::new(JsonlStore::new(home.sessions_dir())),
        hooks: vec![],
        mcp_servers: Vec::new(),
        browser_pool: None,
        docs: None,
        decks: None,
        words: None,
        routines: None,
        connectors: None,
    }
}

struct Harness {
    kernel: Kernel,
    api: Arc<ScriptedApi>,
    work: PathBuf,
    home: HerHome,
    _dir: TempDir,
}

async fn harness(model: &str, config: Config, scripts: Vec<Script>) -> Harness {
    let dir = tempfile::tempdir().unwrap();
    let work = dir.path().join("work");
    std::fs::create_dir_all(&work).unwrap();
    let home = HerHome::new(dir.path().join("home"));
    home.ensure().unwrap();
    let api = ScriptedApi::new();
    for script in scripts {
        api.push(script);
    }
    let options = StartOptions {
        model: model.into(),
        thinking: ThinkingLevel::Off,
        permission_mode: PermissionMode::BypassPermissions,
        cwd: work.clone(),
        home: home.clone(),
        config: Arc::new(config),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot: None,
    };
    let kernel = Kernel::start(options, deps(&api, &work, &home)).await.unwrap();
    Harness {
        kernel,
        api,
        work,
        home,
        _dir: dir,
    }
}

async fn resume(home: &HerHome, work: &Path, session_id: &str, api: &Arc<ScriptedApi>) -> Kernel {
    let options = ResumeOptions {
        session: SessionRef::Id(session_id.to_string()),
        model: None,
        thinking: None,
        permission_mode: None,
        home: home.clone(),
        config: Arc::new(Config::default()),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot_name: None,
    };
    Kernel::resume(options, deps(api, work, home)).await.unwrap()
}

fn prompt(text: &str) -> Vec<UserContent> {
    vec![UserContent::Text { text: text.into() }]
}

/// Collects events until a task ends.
async fn until_task_end(kernel: &mut Kernel) -> Vec<EventMsg> {
    let mut events = Vec::new();
    loop {
        let event = tokio::time::timeout(Duration::from_secs(20), kernel.next_event())
            .await
            .expect("timed out waiting for an event")
            .expect("session closed");
        let end = event.msg.is_task_end();
        events.push(event.msg);
        if end {
            return events;
        }
    }
}

fn messages(result: &TaskResult) -> Vec<&EventMsg> {
    result.events.iter().map(|e| &e.msg).collect()
}

fn text_of(message: &Message) -> String {
    match message {
        Message::User(u) => u.text_content(),
        Message::Assistant(a) => a.text(),
        Message::ToolResult(r) => r.text_content(),
    }
}

/// `user:text` / `assistant:text` / `tool:text`, shortened.
fn shape(context: &her_llm::Context) -> Vec<String> {
    context
        .messages
        .iter()
        .map(|m| {
            let role = match m {
                Message::User(_) => "user",
                Message::Assistant(_) => "assistant",
                Message::ToolResult(_) => "tool",
            };
            let text: String = text_of(m).chars().take(40).collect();
            format!("{role}:{text}")
        })
        .collect()
}

fn completed(events: &[&EventMsg]) -> Vec<(CompactionReason, u32, u64, u64, u32, String, ContextUsage)> {
    events
        .iter()
        .filter_map(|m| match m {
            EventMsg::CompactionCompleted {
                reason,
                seq,
                tokens_before,
                tokens_after,
                retained_messages,
                summary,
                context,
                ..
            } => Some((
                *reason,
                *seq,
                *tokens_before,
                *tokens_after,
                *retained_messages,
                summary.clone(),
                *context,
            )),
            _ => None,
        })
        .collect()
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

fn is_handoff(context: &her_llm::Context) -> bool {
    context
        .messages
        .last()
        .is_some_and(|m| text_of(m).ends_with(HANDOFF_REQUEST))
}

#[tokio::test]
async fn reaching_the_soft_limit_mid_task_compacts_and_reuses_the_request_prefix() {
    let mut h = harness(
        SMALL,
        Config::default(),
        vec![
            // The provider reports a context above the soft limit after this response.
            Script::tool_call("call_1", "bash", json!({"command": "echo one"})).with_usage(17_000, 50),
            Script::text("SUMMARY-1: ran echo one").with_cached_usage(17_100, 17_000, 30),
            Script::text("all done").with_usage(3_000, 20),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("start the job")).await.unwrap();
    assert_eq!(
        result.end,
        TaskEnd::Completed {
            last_message: Some("all done".into())
        }
    );

    let calls = h.api.calls();
    assert_eq!(calls.len(), 3);
    let (first, handoff, after) = (&calls[0], &calls[1], &calls[2]);
    // The summary request is the next normal request plus the handoff message: same system
    // prompt, tools, model and options, and the previous request's messages as its prefix.
    assert!(is_handoff(&handoff.context));
    assert_eq!(handoff.context.system, first.context.system);
    assert_eq!(handoff.context.tools, first.context.tools);
    assert_eq!(handoff.model, first.model);
    assert_eq!(handoff.options.tool_choice, ToolChoice::Auto);
    assert_eq!(handoff.options.cache_key, first.options.cache_key);
    assert_eq!(handoff.options.thinking, first.options.thinking);
    let n = first.context.messages.len();
    assert_eq!(&handoff.context.messages[..n], &first.context.messages[..]);
    // The handoff request joins the new tool result instead of opening a user turn.
    let handoff_shape = shape(&handoff.context);
    assert_eq!(handoff_shape.len(), n + 2);
    assert_eq!(handoff_shape[n], "assistant:");
    assert!(handoff_shape[n + 1].starts_with("tool:Exit code: 0"), "{}", handoff_shape[n + 1]);

    // The next request starts from the user's own words and the summary.
    let shape_after = shape(&after.context);
    assert_eq!(shape_after.len(), 2, "{shape_after:?}");
    assert_eq!(shape_after[0], "user:start the job");
    let summary = text_of(&after.context.messages[1]);
    assert!(summary.starts_with("<conversation_summary>\n"));
    assert!(summary.contains("SUMMARY-1: ran echo one"));
    assert_eq!(after.context.tools, first.context.tools);

    let events = messages(&result);
    let started = events
        .iter()
        .find_map(|m| match m {
            EventMsg::CompactionStarted { reason } => Some(*reason),
            _ => None,
        })
        .unwrap();
    assert_eq!(started, CompactionReason::Threshold);
    let done = completed(&events);
    assert_eq!(done.len(), 1);
    let (_, seq, before, after_tokens, retained, summary_text, context) = &done[0];
    assert_eq!((*seq, *retained), (1, 1));
    assert_eq!(summary_text, "SUMMARY-1: ran echo one");
    assert!(*before >= SOFT, "{before}");
    assert!(*after_tokens < SOFT, "{after_tokens}");
    // The turn after the compaction reports the same estimate the compaction recorded.
    let turn_contexts: Vec<ContextUsage> = events
        .iter()
        .filter_map(|m| match m {
            EventMsg::TurnStarted { context, .. } => *context,
            _ => None,
        })
        .collect();
    assert_eq!(turn_contexts.len(), 2, "the summary request is not a turn");
    assert_eq!(turn_contexts[1].tokens, *after_tokens);
    assert_eq!(context.tokens, *after_tokens);
    assert_eq!((context.soft_limit, context.hard_limit), (SOFT, HARD));

    // The compaction is one line in the log; the original messages stay there too.
    let file = h.kernel.session_file().to_path_buf();
    let entries = log_entries(&file, "compaction");
    assert_eq!(entries.len(), 1);
    let entry = &entries[0];
    assert_eq!(entry["seq"], 1);
    assert_eq!(entry["reason"], "threshold");
    assert_eq!(entry["summary"], "SUMMARY-1: ran echo one");
    assert_eq!(entry["retained"][0]["content"][0]["text"], "start the job");
    assert_eq!(entry["model"], SMALL);
    assert_eq!(entry["usage"]["cache_read"], 17_000);
    // user, assistant, tool result, then the reply after the compaction.
    assert_eq!(log_entries(&file, "message").len(), 4);
}

#[tokio::test]
async fn later_compactions_fold_the_summary_and_keep_recent_user_messages() {
    let mut h = harness(
        SMALL,
        Config::default(),
        vec![
            Script::text("ok 1").with_usage(17_000, 10),
            // Task 2 starts above the soft limit: compaction before its first request.
            Script::text("S1").with_usage(1_000, 10),
            Script::text("ok 2").with_usage(17_000, 10),
            Script::text("S2").with_usage(1_000, 10),
            Script::text("ok 3").with_usage(2_000, 10),
        ],
    )
    .await;
    h.kernel.run(prompt("first request")).await.unwrap();
    let second = h.kernel.run(prompt("second request")).await.unwrap();
    let third = h.kernel.run(prompt("third request")).await.unwrap();
    assert!(matches!(third.end, TaskEnd::Completed { .. }));
    assert_eq!(completed(&messages(&second))[0].1, 1);
    assert_eq!(completed(&messages(&third))[0].1, 2);

    let calls = h.api.calls();
    assert_eq!(calls.len(), 5);
    // The second summary request carries the first summary, so the model can fold it in.
    assert!(is_handoff(&calls[3].context));
    let second_handoff = shape(&calls[3].context);
    assert_eq!(second_handoff[0], "user:first request");
    assert_eq!(second_handoff[1], "user:second request");
    assert!(second_handoff[2].starts_with("user:<conversation_summary>"));
    assert_eq!(second_handoff[3], "assistant:ok 2");
    assert!(second_handoff[4].starts_with("user:third request\n"), "{}", second_handoff[4]);
    // Afterwards: every user message so far (they fit the budget) and only the newest summary.
    let last = &calls[4].context;
    let texts: Vec<String> = last.messages.iter().map(text_of).collect();
    assert_eq!(texts.len(), 4);
    assert_eq!(&texts[..3], ["first request", "second request", "third request"]);
    assert!(texts[3].contains("S2") && !texts[3].contains("S1"));
}

#[tokio::test]
async fn an_overflow_compacts_and_retries_once() {
    let mut h = harness(
        SMALL,
        Config::default(),
        vec![
            Script::fail(ErrorKind::ContextOverflow, "maximum context length exceeded"),
            Script::text("S").with_usage(1_000, 10),
            Script::text("recovered").with_usage(2_000, 10),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("go")).await.unwrap();
    assert_eq!(
        result.end,
        TaskEnd::Completed {
            last_message: Some("recovered".into())
        }
    );
    let events = messages(&result);
    assert_eq!(completed(&events)[0].0, CompactionReason::Overflow);
    let calls = h.api.calls();
    // The failed reply is not part of the summary request; the handoff request joins the
    // user's message.
    assert_eq!(calls[1].context.messages.len(), 1);
    assert!(shape(&calls[1].context)[0].starts_with("user:go\n"));
    assert!(is_handoff(&calls[1].context));
    assert_eq!(shape(&calls[2].context).len(), 2);
}

#[tokio::test]
async fn an_overflow_right_after_a_compaction_is_not_compacted_again() {
    let mut h = harness(
        SMALL,
        Config::default(),
        vec![
            Script::fail(ErrorKind::ContextOverflow, "maximum context length exceeded"),
            Script::text("S1").with_usage(1_000, 10),
            Script::fail(ErrorKind::ContextOverflow, "maximum context length exceeded"),
            Script::text("finally").with_usage(2_000, 10),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("go")).await.unwrap();
    match &result.end {
        TaskEnd::Failed(error) => {
            assert_eq!(error.kind, "context_overflow");
            assert!(error.retryable);
        }
        other => panic!("unexpected {other:?}"),
    }
    // One compaction only: compacting again before any response could not help.
    assert_eq!(completed(&messages(&result)).len(), 1);

    // Nothing is left to compact, so `continue` just sends the request again.
    h.kernel.submit(Op::Continue).await.unwrap();
    let events = until_task_end(&mut h.kernel).await;
    match events.last().unwrap() {
        EventMsg::TaskCompleted { last_message, .. } => assert_eq!(last_message.as_deref(), Some("finally")),
        other => panic!("unexpected {other:?}"),
    }
    assert!(!events.iter().any(|m| matches!(m, EventMsg::CompactionStarted { .. })));
}

#[tokio::test]
async fn after_an_overflow_whose_compaction_failed_the_next_request_compacts_first() {
    let mut h = harness(
        SMALL,
        Config::default(),
        vec![
            Script::fail(ErrorKind::ContextOverflow, "maximum context length exceeded"),
            Script::fail(ErrorKind::Auth, "bad key"),
            // After `continue`: the last request overflowed, so the window counts as full.
            Script::text("S1").with_usage(1_000, 10),
            Script::text("finally").with_usage(2_000, 10),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("go")).await.unwrap();
    assert!(matches!(&result.end, TaskEnd::Failed(error) if error.kind == "context_overflow" && error.retryable));
    assert!(completed(&messages(&result)).is_empty());

    h.kernel.submit(Op::Continue).await.unwrap();
    let events = until_task_end(&mut h.kernel).await;
    let refs: Vec<&EventMsg> = events.iter().collect();
    match events.last().unwrap() {
        EventMsg::TaskCompleted { last_message, .. } => assert_eq!(last_message.as_deref(), Some("finally")),
        other => panic!("unexpected {other:?}"),
    }
    let done = completed(&refs);
    assert_eq!(done.len(), 1);
    assert_eq!((done[0].0, done[0].1), (CompactionReason::Overflow, 1));
    let full = events
        .iter()
        .find_map(|m| match m {
            EventMsg::CompactionCompleted { tokens_before, .. } => Some(*tokens_before),
            _ => None,
        })
        .unwrap();
    assert_eq!(full, HARD, "a window that overflowed counts as full");
}

#[tokio::test]
async fn a_failed_compaction_below_the_hard_limit_continues_without_it() {
    let mut h = harness(
        SMALL,
        Config::default(),
        vec![
            Script::tool_call("call_1", "bash", json!({"command": "echo one"})).with_usage(17_000, 50),
            Script::fail(ErrorKind::Auth, "bad key"),
            Script::text("went on").with_usage(17_200, 10),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("start")).await.unwrap();
    assert_eq!(
        result.end,
        TaskEnd::Completed {
            last_message: Some("went on".into())
        }
    );
    let events = messages(&result);
    assert!(events.iter().any(|m| matches!(m, EventMsg::CompactionFailed { error, .. } if error.kind == "auth")));
    assert!(events.iter().any(|m| matches!(m, EventMsg::Warning { message, .. } if message.contains("compaction failed"))));
    // The history is unchanged: the request after the failure is the uncompacted one.
    let calls = h.api.calls();
    assert_eq!(shape(&calls[2].context).len(), 3);
    assert!(log_entries(h.kernel.session_file(), "compaction").is_empty());
}

#[tokio::test]
async fn a_failed_compaction_above_the_hard_limit_fails_the_task() {
    let mut h = harness(
        SMALL,
        Config::default(),
        vec![
            Script::tool_call("call_1", "bash", json!({"command": "echo one"})).with_usage(18_500, 50),
            Script::fail(ErrorKind::Auth, "bad key"),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("start")).await.unwrap();
    match &result.end {
        TaskEnd::Failed(error) => {
            assert_eq!(error.kind, "context_overflow");
            assert!(error.message.contains("compaction failed: bad key"), "{}", error.message);
        }
        other => panic!("unexpected {other:?}"),
    }
    assert!(log_entries(h.kernel.session_file(), "compaction").is_empty());
    assert_eq!(h.api.calls().len(), 2, "no request is sent that would overflow");
}

#[tokio::test]
async fn a_reply_without_text_is_asked_again_without_tools() {
    let mut h = harness(
        SMALL,
        Config::default(),
        vec![
            Script::text("ok").with_usage(17_000, 10),
            Script::tool_call("call_x", "bash", json!({"command": "ls"})),
            Script::text("S").with_usage(1_000, 10),
            Script::text("done").with_usage(2_000, 10),
        ],
    )
    .await;
    h.kernel.run(prompt("one")).await.unwrap();
    let result = h.kernel.run(prompt("two")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    let calls = h.api.calls();
    assert!(is_handoff(&calls[1].context) && is_handoff(&calls[2].context));
    assert_eq!(calls[1].options.tool_choice, ToolChoice::Auto);
    assert_eq!(calls[2].options.tool_choice, ToolChoice::None);
    assert_eq!(completed(&messages(&result))[0].5, "S");
    // The tool call in the summary reply was not executed.
    assert!(!messages(&result).iter().any(|m| matches!(m, EventMsg::ToolStarted { .. })));
}

#[tokio::test]
async fn resuming_continues_from_the_checkpoint_with_the_logged_usage() {
    let mut h = harness(
        SMALL,
        Config::default(),
        vec![
            Script::tool_call("call_1", "bash", json!({"command": "echo one"})).with_usage(17_000, 50),
            Script::text("SUMMARY").with_usage(1_000, 10),
            Script::text("all done").with_usage(3_000, 20),
        ],
    )
    .await;
    h.kernel.run(prompt("start")).await.unwrap();
    let before_resume = h.api.calls()[2].context.clone();
    let session_id = h.kernel.session_id().to_string();
    let Harness {
        kernel, work, home, _dir, ..
    } = h;
    kernel.shutdown().await;

    let api = ScriptedApi::new();
    api.push(Script::text("resumed").with_usage(3_100, 10));
    let mut kernel = resume(&home, &work, &session_id, &api).await;
    // The usage after the resume comes from the log: the last response's reported usage.
    let started = kernel.next_event().await.unwrap();
    let EventMsg::SessionStarted { context, resumed, .. } = &started.msg else {
        panic!("unexpected {started:?}")
    };
    assert!(*resumed);
    let context = context.unwrap();
    assert_eq!((context.tokens, context.estimated), (3_020, 0));

    let result = kernel.run(prompt("next")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    let request = &api.calls()[0].context;
    let mut expected = shape(&before_resume);
    expected.push("assistant:all done".into());
    expected.push("user:next".into());
    assert_eq!(shape(request), expected);
    assert_eq!(&request.messages[..2], &before_resume.messages[..2], "the checkpoint is byte-identical");
}

#[tokio::test]
async fn a_manual_compaction_runs_as_its_own_task() {
    let mut h = harness(
        SMALL,
        Config::default(),
        vec![
            Script::text("hi").with_usage(500, 10),
            Script::text("MANUAL SUMMARY").with_usage(600, 10),
            Script::text("after").with_usage(700, 10),
        ],
    )
    .await;
    h.kernel.run(prompt("hello")).await.unwrap();
    h.kernel.submit(Op::Compact).await.unwrap();
    let events = until_task_end(&mut h.kernel).await;
    let kinds: Vec<&str> = events
        .iter()
        .filter_map(|m| match m {
            EventMsg::TaskStarted { .. } => Some("task_started"),
            EventMsg::TurnStarted { .. } => Some("turn_started"),
            EventMsg::CompactionStarted { .. } => Some("compaction_started"),
            EventMsg::CompactionCompleted { .. } => Some("compaction_completed"),
            EventMsg::TaskCompleted { .. } => Some("task_completed"),
            _ => None,
        })
        .collect();
    assert_eq!(kinds, ["task_started", "compaction_started", "compaction_completed", "task_completed"]);
    let refs: Vec<&EventMsg> = events.iter().collect();
    assert_eq!(completed(&refs)[0].0, CompactionReason::Manual);

    h.kernel.run(prompt("and now")).await.unwrap();
    let shape_after = shape(&h.api.calls()[2].context);
    assert_eq!(shape_after.len(), 3);
    assert_eq!(shape_after[0], "user:hello");
    assert!(shape_after[1].starts_with("user:<conversation_summary>"));
    assert_eq!(shape_after[2], "user:and now");
}

#[tokio::test]
async fn without_a_session_log_saved_output_is_temporary() {
    let dir = tempfile::tempdir().unwrap();
    let work = dir.path().join("work");
    std::fs::create_dir_all(&work).unwrap();
    let home = HerHome::new(dir.path().join("home"));
    home.ensure().unwrap();
    let api = ScriptedApi::new();
    api.push(Script::tool_call("call_1", "bash", json!({"command": "seq 1 5000"})));
    api.push(Script::text("ok"));
    let mut deps = deps(&api, &work, &home);
    deps.store = Arc::new(her_core::MemoryStore::new());
    let options = StartOptions {
        model: SMALL.into(),
        thinking: ThinkingLevel::Off,
        permission_mode: PermissionMode::BypassPermissions,
        cwd: work.clone(),
        home: home.clone(),
        config: Arc::new(Config::default()),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot: None,
    };
    let mut kernel = Kernel::start(options, deps).await.unwrap();
    let result = kernel.run(prompt("print")).await.unwrap();
    let output = result
        .events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::ToolCompleted { result, .. } => Some(result.text_content()),
            _ => None,
        })
        .unwrap();
    let path = output.split("Full output: ").nth(1).unwrap().split(" (read it").next().unwrap();
    let path = PathBuf::from(path);
    assert!(path.starts_with(std::env::temp_dir()), "{}", path.display());
    assert!(path.exists());
    kernel.shutdown().await;
    assert!(!path.exists(), "removed with the session");
}

#[tokio::test]
async fn switching_to_a_smaller_window_compacts_with_the_previous_model() {
    // About 17,500 tokens of user text: fine for the big model, too much for the small one.
    let large = "x".repeat(70_000);
    let mut h = harness(
        BIG,
        Config::default(),
        vec![
            Script::text("read it").with_usage(18_000, 10),
            Script::text("SWITCH SUMMARY").with_usage(18_100, 10),
            Script::text("small model here").with_usage(4_000, 10),
        ],
    )
    .await;
    h.kernel.run(prompt(&large)).await.unwrap();
    h.kernel
        .submit(Op::SetModel {
            model: SMALL.into(),
            thinking: None,
        })
        .await
        .unwrap();
    let result = h.kernel.run(prompt("continue on the small model")).await.unwrap();
    assert_eq!(
        result.end,
        TaskEnd::Completed {
            last_message: Some("small model here".into())
        }
    );
    let events = messages(&result);
    assert_eq!(completed(&events)[0].0, CompactionReason::ModelSwitch);
    let calls = h.api.calls();
    assert!(is_handoff(&calls[1].context));
    assert_eq!(calls[1].model, BIG, "the previous model writes the summary");
    assert_eq!(calls[2].model, SMALL);
    // The large message is kept, shortened in the middle to the small model's budget.
    let kept = text_of(&calls[2].context.messages[0]);
    assert!(kept.contains("characters omitted]"), "{}", &kept[..80.min(kept.len())]);
    assert!(kept.len() < 12_000, "{}", kept.len());
    assert_eq!(text_of(&calls[2].context.messages[1]), "continue on the small model");
}

#[tokio::test]
async fn large_tool_output_is_cut_once_and_can_be_read_back() {
    let mut h = harness(
        SMALL,
        Config::default(),
        vec![
            Script::tool_call("call_1", "bash", json!({"command": "seq 1 5000"})),
            Script::tool_call("call_2", "big", json!({})),
            Script::text("seen"),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("print numbers")).await.unwrap();
    let outputs: Vec<String> = result
        .events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::ToolCompleted { result, .. } => Some(result.text_content()),
            _ => None,
        })
        .collect();
    let data_dir = h.kernel.session_file().with_extension("");
    let output_dir = data_dir.join("tool-output");

    // bash: head and tail on whole lines, a note with the file and the first hidden line.
    let bash = &outputs[0];
    let note = bash.lines().find(|l| l.starts_with("[Lines ")).expect(bash);
    let path = output_dir.join("call_1.txt");
    assert!(note.contains(&format!("Full output: {}", path.display())), "{note}");
    let offset: usize = note.split("offset=").nth(1).unwrap().trim_end_matches(")]").parse().unwrap();
    let full = std::fs::read_to_string(&path).unwrap();
    assert_eq!(full.lines().count(), 5000);
    let hidden = (offset).to_string();
    assert!(!bash.lines().any(|l| l == hidden), "line {offset} is the first omitted one");
    assert!(bash.lines().any(|l| l == (offset - 1).to_string()));
    // The window is small (hard limit 18,000), so results get 10% of it: 7,200 bytes.
    assert!(bash.len() < 7_200 + 400, "{}", bash.len());

    // Any other tool: the kernel's safety net saves the whole text and keeps both ends.
    let big = &outputs[1];
    let big_path = output_dir.join("call_2.txt");
    assert_eq!(std::fs::read_to_string(&big_path).unwrap(), big_text());
    assert!(big.starts_with("record 1\n") && big.trim_end().ends_with("record 3000"));
    assert!(big.contains(&format!("Full output: {}", big_path.display())));

    // The model reads the omitted part back with the offset from the note.
    h.api.push(Script::tool_call(
        "call_3",
        "read",
        json!({"path": path.display().to_string(), "offset": offset, "limit": 3}),
    ));
    h.api.push(Script::text("read back"));
    let result = h.kernel.run(prompt("show the hidden part")).await.unwrap();
    let read = result
        .events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::ToolCompleted { result, .. } => Some(result.text_content()),
            _ => None,
        })
        .unwrap();
    assert!(read.starts_with(&format!("{offset}\n{}\n{}\n", offset + 1, offset + 2)), "{read}");
}
