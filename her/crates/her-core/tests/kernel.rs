//! End-to-end kernel tests: scripted model, real tools, real JSONL session log.

use her_core::Config;
use her_core::HerHome;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::KernelDeps;
use her_core::ResumeOptions;
use her_core::SessionRef;
use her_core::StartOptions;
use her_core::TaskEnd;
use her_llm::Catalog;
use her_llm::ErrorKind;
use her_llm::Message;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use her_llm::ThinkingLevel;
use her_llm::UserContent;
use her_llm::api::scripted::Script;
use her_llm::api::scripted::ScriptedApi;
use her_protocol::ApprovalDecision;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::Op;
use her_protocol::PermissionMode;
use her_protocol::PromptMode;
use her_protocol::QueuedInput;
use her_llm::ToolSpec;
use her_tools::BashTool;
use her_tools::LocalEnvironment;
use her_tools::Tool;
use her_tools::ToolCtx;
use her_tools::ToolError;
use her_tools::ToolKind;
use her_tools::ToolOutput;
use her_tools::builtin_registry;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::Mutex;
use std::time::Duration;
use std::time::Instant;
use tempfile::TempDir;

struct Harness {
    kernel: Kernel,
    api: Arc<ScriptedApi>,
    work: PathBuf,
    home: HerHome,
    _dir: TempDir,
}

fn deps(api: &Arc<ScriptedApi>, work: &Path, home: &HerHome) -> KernelDeps {
    let mut registry = ModelRegistry::new(Catalog::default(), RegistryOptions::default());
    registry.register_api(api.clone());
    registry.insert_provider(ScriptedApi::provider());
    KernelDeps {
        llm: Arc::new(registry),
        tools: Arc::new(builtin_registry(BashTool::default())),
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
        mesh: None,
    }
}

async fn harness(mode: PermissionMode, interactive: bool, scripts: Vec<Script>) -> Harness {
    harness_with(Config::default(), mode, interactive, scripts).await
}

async fn harness_with(config: Config, mode: PermissionMode, interactive: bool, scripts: Vec<Script>) -> Harness {
    start(config, mode, interactive, scripts, Vec::new()).await
}

/// A harness whose registry has `extra` tools after the built-in ones.
async fn start(
    config: Config,
    mode: PermissionMode,
    interactive: bool,
    scripts: Vec<Script>,
    extra: Vec<Arc<dyn Tool>>,
) -> Harness {
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
        model: "test/scripted".into(),
        thinking: ThinkingLevel::Off,
        permission_mode: mode,
        cwd: work.clone(),
        home: home.clone(),
        config: Arc::new(config),
        project_root: None,
        trusted: false,
        interactive_approvals: interactive,
        bot: None,
    };
    let mut deps = deps(&api, &work, &home);
    if !extra.is_empty() {
        let mut tools = builtin_registry(BashTool::default());
        for tool in extra {
            tools.register(tool).unwrap();
        }
        deps.tools = Arc::new(tools);
    }
    let kernel = Kernel::start(options, deps).await.unwrap();
    Harness {
        kernel,
        api,
        work,
        home,
        _dir: dir,
    }
}

/// Collects events until `stop` matches (inclusive). Fails after 10 seconds.
async fn until(kernel: &mut Kernel, stop: impl Fn(&EventMsg) -> bool) -> Vec<Event> {
    let mut events = Vec::new();
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        let remaining = deadline.saturating_duration_since(Instant::now());
        let event = tokio::time::timeout(remaining, kernel.next_event())
            .await
            .expect("timed out waiting for an event")
            .expect("session closed");
        let done = stop(&event.msg);
        events.push(event);
        if done {
            return events;
        }
    }
}

fn prompt(text: &str) -> Vec<UserContent> {
    vec![UserContent::Text { text: text.into() }]
}

fn tool_results(context: &her_llm::Context) -> Vec<String> {
    context
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::ToolResult(r) => Some(r.text_content()),
            _ => None,
        })
        .collect()
}

#[tokio::test]
async fn writes_a_file_then_answers() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![
            Script::tool_call("call_1", "write", json!({"path": "hello.txt", "content": "hi"})),
            Script::text("done").with_usage(100, 10),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("create hello.txt")).await.unwrap();
    assert_eq!(
        result.end,
        TaskEnd::Completed {
            last_message: Some("done".into())
        }
    );
    assert_eq!(std::fs::read_to_string(h.work.join("hello.txt")).unwrap(), "hi");

    let requests = h.api.requests();
    assert_eq!(requests.len(), 2);
    assert!(requests[0].system.contains("You are Her"));
    // The four built-in tools, then the sub-agent tools (on by default), then the two that reach the
    // deferred groups: the sub-agents' other two and web search, on by default.
    let names: Vec<&str> = requests[0].tools.iter().map(|t| t.name.as_str()).collect();
    assert_eq!(names, ["read", "write", "edit", "bash", "spawn_agent", "send_message", "tool_search", "call_tool"]);
    assert!(requests[0].system.contains("- agents: See how your agents stand, or stop them"));
    assert!(tool_results(&requests[1])[0].starts_with("Wrote 2 bytes"));

    // Events arrive in strict sequence order and the task is bracketed.
    let seqs: Vec<u64> = result.events.iter().map(|e| e.seq).collect();
    assert!(seqs.windows(2).all(|w| w[0] < w[1]));
    let kinds: Vec<&str> = result
        .events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::TaskStarted { .. } => Some("task_started"),
            EventMsg::TurnStarted { .. } => Some("turn_started"),
            EventMsg::ToolStarted { .. } => Some("tool_started"),
            EventMsg::ToolCompleted { .. } => Some("tool_completed"),
            EventMsg::TurnCompleted { .. } => Some("turn_completed"),
            EventMsg::TaskCompleted { .. } => Some("task_completed"),
            _ => None,
        })
        .collect();
    assert_eq!(
        kinds,
        vec![
            "task_started",
            "turn_started",
            "tool_started",
            "tool_completed",
            "turn_completed",
            "turn_started",
            "turn_completed",
            "task_completed"
        ]
    );
    let EventMsg::TaskCompleted { usage, .. } = &result.events.last().unwrap().msg else {
        panic!()
    };
    assert_eq!(usage.tokens().input, 100);
}

#[tokio::test]
async fn asks_for_approval_and_applies_decisions() {
    let mut h = harness(
        PermissionMode::Manual,
        true,
        vec![
            Script::tool_call("call_1", "bash", json!({"command": "touch approved.txt"})),
            Script::tool_call("call_2", "bash", json!({"command": "touch denied.txt"})),
            Script::text("ok"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("touch two files")).await.unwrap();

    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let EventMsg::ApprovalRequested { call_id, summary, proposed_rule, .. } = &events.last().unwrap().msg else {
        panic!()
    };
    assert_eq!(summary, "touch approved.txt");
    assert_eq!(proposed_rule.as_ref().unwrap().prefix, vec!["touch", "approved.txt"]);
    h.kernel
        .submit(Op::Approval {
            call_id: call_id.clone(),
            decision: ApprovalDecision::Approve,
        })
        .await
        .unwrap();

    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let EventMsg::ApprovalRequested { call_id, .. } = &events.last().unwrap().msg else {
        panic!()
    };
    h.kernel
        .submit(Op::Approval {
            call_id: call_id.clone(),
            decision: ApprovalDecision::Deny {
                feedback: Some("not this one".into()),
            },
        })
        .await
        .unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;

    assert!(h.work.join("approved.txt").exists());
    assert!(!h.work.join("denied.txt").exists());
    let last_request = h.api.requests().pop().unwrap();
    let results = tool_results(&last_request);
    assert!(results[0].starts_with("Exit code: 0"));
    assert_eq!(results[1], "The user denied this tool call. Feedback: not this one");
}

#[tokio::test]
async fn without_an_approver_ask_means_deny() {
    let mut h = harness(
        PermissionMode::Manual,
        false,
        vec![
            Script::tool_call("call_1", "bash", json!({"command": "touch x.txt"})),
            Script::text("understood"),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("touch x")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert!(!h.work.join("x.txt").exists());
    assert!(!result.events.iter().any(|e| matches!(e.msg, EventMsg::ApprovalRequested { .. })));
    let results = tool_results(&h.api.requests()[1]);
    assert!(results[0].contains("nobody can approve it"));
}

#[tokio::test]
async fn approving_an_edit_for_the_session_switches_to_accept_edits() {
    let mut h = harness(
        PermissionMode::Manual,
        true,
        vec![
            Script::tool_call("call_1", "write", json!({"path": "a.txt", "content": "a"})),
            Script::tool_call("call_2", "write", json!({"path": "b.txt", "content": "b"})),
            Script::tool_call("call_3", "bash", json!({"command": "mkdir -p out && touch out/c.txt"})),
            Script::text("ok"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("write files")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let EventMsg::ApprovalRequested {
        call_id,
        proposed_rule,
        proposed_mode,
        ..
    } = &events.last().unwrap().msg
    else {
        panic!()
    };
    assert_eq!(*proposed_rule, None);
    assert_eq!(*proposed_mode, Some(PermissionMode::AcceptEdits));
    h.kernel
        .submit(Op::Approval {
            call_id: call_id.clone(),
            decision: ApprovalDecision::ApproveForSession { grant: None, persist: false },
        })
        .await
        .unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert!(events.iter().any(|e| matches!(
        e.msg,
        EventMsg::SettingsChanged {
            permission_mode: PermissionMode::AcceptEdits,
            ..
        }
    )));
    // The later write and the file-system command run without asking.
    assert!(!events.iter().any(|e| matches!(e.msg, EventMsg::ApprovalRequested { .. })));
    assert!(h.work.join("b.txt").exists());
    assert!(h.work.join("out/c.txt").exists());
    let session_file = h.kernel.session_file().to_path_buf();
    h.kernel.shutdown().await;
    let log = std::fs::read_to_string(session_file).unwrap();
    assert!(log.contains(r#""permission_mode":"accept-edits""#));
}

#[tokio::test]
async fn bypass_warns_and_still_stops_critical_deletions() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![
            Script::tool_call("call_1", "bash", json!({"command": "rm -r .."})),
            Script::tool_call("call_2", "write", json!({"path": ".git/config", "content": "x"})),
            Script::text("stopped"),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("clean up")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert!(result.events.iter().any(|e| match &e.msg {
        EventMsg::Warning { message, code } => {
            message.contains("bypass-permissions") && *code == Some(her_protocol::WarningCode::BypassPermissions)
        }
        _ => false,
    }));
    assert!(h.work.exists());
    let results = tool_results(&h.api.requests()[1]);
    assert!(results[0].contains("nobody can approve it"), "{}", results[0]);
    // Protected paths are not protected in this mode.
    assert_eq!(std::fs::read_to_string(h.work.join(".git/config")).unwrap(), "x");
}

#[tokio::test]
async fn critical_deletions_are_found_behind_cd_and_scripts() {
    let commands = [
        "cd .. && rm -r work",
        "sh -c 'cd .. && rm -r work'",
        "trap 'rm -r ../work' EXIT; true",
        "find .. -exec rm -r {} +",
    ];
    let scripts = commands
        .iter()
        .enumerate()
        .map(|(i, command)| Script::tool_call(format!("call_{i}"), "bash", json!({ "command": command })))
        .chain([Script::text("stopped")])
        .collect();
    let mut h = harness(PermissionMode::BypassPermissions, false, scripts).await;
    std::fs::write(h.work.join("keep.txt"), "still here").unwrap();
    let result = h.kernel.run(prompt("clean everything")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert_eq!(std::fs::read_to_string(h.work.join("keep.txt")).unwrap(), "still here");
    let requests = h.api.requests();
    for (i, command) in commands.iter().enumerate() {
        let result = tool_results(&requests[i + 1]).pop().unwrap();
        assert!(result.contains("nobody can approve it"), "{command}: {result}");
        assert!(result.contains("Why it needs approval: Deleting"), "{command}: {result}");
    }
}

#[tokio::test]
async fn deny_rules_cannot_be_dodged() {
    let mut config = Config::default();
    config.rules.push(her_protocol::CommandRule {
        prefix: vec!["git".into(), "push".into()],
        decision: her_protocol::RuleDecision::Deny,
        reason: Some("pushing is off".into()),
    });
    let commands = ["/usr/bin/git push", "git -C . push", "bash -lc 'git push'", "env A=1 git push"];
    let scripts = commands
        .iter()
        .enumerate()
        .map(|(i, command)| Script::tool_call(format!("call_{i}"), "bash", json!({ "command": command })))
        .chain([Script::text("ok")])
        .collect();
    let mut h = harness_with(config, PermissionMode::BypassPermissions, false, scripts).await;
    h.kernel.run(prompt("push it")).await.unwrap();
    let requests = h.api.requests();
    for (i, command) in commands.iter().enumerate() {
        let result = tool_results(&requests[i + 1]).pop().unwrap();
        assert_eq!(
            result, "Tool call denied: The command is blocked by the rule `git push`: pushing is off",
            "{command}"
        );
    }
}

#[tokio::test]
async fn always_allow_is_not_offered_for_inline_code() {
    let mut h = harness(
        PermissionMode::Manual,
        true,
        vec![
            Script::tool_call("call_1", "bash", json!({"command": "python3 -c 'print(1)'"})),
            Script::tool_call("call_2", "bash", json!({"command": "cd sub && npm run build"})),
            Script::text("ok"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("run things")).await.unwrap();
    let mut offered = Vec::new();
    for _ in 0..2 {
        let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
        let EventMsg::ApprovalRequested { call_id, proposed_rule, .. } = &events.last().unwrap().msg else {
            panic!()
        };
        offered.push(proposed_rule.as_ref().map(|rule| rule.prefix.join(" ")));
        h.kernel
            .submit(Op::Approval {
                call_id: call_id.clone(),
                decision: ApprovalDecision::Deny { feedback: None },
            })
            .await
            .unwrap();
    }
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert_eq!(offered, vec![None, Some("npm run build".to_string())]);
}

#[tokio::test]
async fn a_session_log_is_created_once_the_session_does_something() {
    let mut h = harness(PermissionMode::Manual, false, vec![Script::text("hi")]).await;
    let file = h.kernel.session_file().to_path_buf();
    tokio::time::sleep(Duration::from_millis(50)).await;
    assert!(!file.exists(), "starting a session writes no file");
    let result = h.kernel.run(prompt("hello")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    let log = std::fs::read_to_string(&file).unwrap();
    let kinds: Vec<String> = log
        .lines()
        .map(|l| serde_json::from_str::<serde_json::Value>(l).unwrap()["type"].as_str().unwrap().to_string())
        .collect();
    assert_eq!(&kinds[..3], ["session", "system_prompt", "settings"]);
    assert!(kinds.iter().any(|k| k == "message"));

    // A session that does nothing still leaves a log when it closes.
    let idle = harness(PermissionMode::Manual, false, vec![]).await;
    let idle_file = idle.kernel.session_file().to_path_buf();
    idle.kernel.shutdown().await;
    let log = std::fs::read_to_string(&idle_file).unwrap();
    assert_eq!(log.lines().count(), 3, "{log}");
}

#[tokio::test]
async fn read_only_commands_and_reads_skip_approval() {
    let mut h = harness(
        PermissionMode::Manual,
        true,
        vec![
            Script::tool_call("call_1", "bash", json!({"command": "ls -a"}))
                .then_tool_call("call_2", "read", json!({"path": "notes.txt"})),
            Script::text("listed"),
        ],
    )
    .await;
    std::fs::write(h.work.join("notes.txt"), "remember the milk").unwrap();
    let result = h.kernel.run(prompt("look around")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert!(!result.events.iter().any(|e| matches!(e.msg, EventMsg::ApprovalRequested { .. })));
    let results = tool_results(&h.api.requests()[1]);
    assert!(results[0].contains("notes.txt"));
    assert!(results[1].contains("remember the milk"));
}

#[tokio::test]
async fn interrupting_a_running_tool_keeps_history_valid_and_resumes() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![Script::tool_call("call_1", "bash", json!({"command": "sleep 30"}))],
    )
    .await;
    let started = Instant::now();
    h.kernel.submit(Op::prompt("wait a while")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::ToolStarted { .. })).await;
    tokio::time::sleep(Duration::from_millis(200)).await;
    h.kernel.submit(Op::Interrupt).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(matches!(events.last().unwrap().msg, EventMsg::TaskInterrupted { .. }));
    assert!(started.elapsed() < Duration::from_secs(10));
    let cancelled = events.iter().any(|e| match &e.msg {
        EventMsg::ToolCompleted { result, .. } => result.text_content() == "Cancelled by user.",
        _ => false,
    });
    assert!(cancelled);
    let session_id = h.kernel.session_id().to_string();
    h.kernel.shutdown().await;

    // Resume from the JSONL log and continue the conversation.
    let api = ScriptedApi::new();
    api.push(Script::text("resumed"));
    let options = ResumeOptions {
        session: SessionRef::Id(her_core::short_id(&session_id).to_string()),
        model: None,
        thinking: None,
        permission_mode: None,
        home: h.home.clone(),
        config: Arc::new(Config::default()),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot_name: None,
    };
    let mut kernel = Kernel::resume(options, deps(&api, &h.work, &h.home)).await.unwrap();
    let started = until(&mut kernel, |m| matches!(m, EventMsg::SessionStarted { .. })).await;
    let EventMsg::SessionStarted { resumed, session_id: resumed_id, .. } = &started.last().unwrap().msg else {
        panic!()
    };
    assert!(*resumed);
    assert_eq!(resumed_id, &session_id);
    let result = kernel.run(prompt("are you there?")).await.unwrap();
    assert_eq!(
        result.end,
        TaskEnd::Completed {
            last_message: Some("resumed".into())
        }
    );
    let request = &api.requests()[0];
    let users: Vec<String> = request
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::User(u) => Some(u.text_content()),
            _ => None,
        })
        .collect();
    assert_eq!(users, vec!["wait a while", "are you there?"]);
    assert_eq!(tool_results(request), vec!["Cancelled by user."]);
    let task_ids: Vec<String> = result
        .events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::TaskStarted { task_id, .. } => Some(task_id.clone()),
            _ => None,
        })
        .collect();
    assert_eq!(task_ids, vec!["t2"], "task numbering continues after resume");
}

#[tokio::test]
async fn interrupting_while_streaming_keeps_partial_text() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![Script::text("partial answer").then_sleep(Duration::from_secs(30)).then_text("never")],
    )
    .await;
    h.kernel.submit(Op::prompt("talk")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::MessageDelta { .. })).await;
    h.kernel.submit(Op::Interrupt).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    let partial = events.iter().find_map(|e| match &e.msg {
        EventMsg::MessageCompleted {
            message: Message::Assistant(a),
            ..
        } => Some(a.clone()),
        _ => None,
    });
    let partial = partial.unwrap();
    assert_eq!(partial.text(), "partial answer");
    assert_eq!(partial.stop_reason, her_llm::StopReason::Aborted);
    assert!(matches!(events.last().unwrap().msg, EventMsg::TaskInterrupted { .. }));
}

#[tokio::test]
async fn steering_input_joins_at_the_next_turn() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![
            Script::tool_call("call_1", "bash", json!({"command": "sleep 1"})),
            Script::text("saw both"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("first")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::ToolStarted { .. })).await;
    h.kernel.submit(Op::prompt("also this")).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(matches!(events.last().unwrap().msg, EventMsg::TaskCompleted { .. }));
    let second = &h.api.requests()[1];
    let last = second.messages.last().unwrap();
    match last {
        Message::User(u) => assert_eq!(u.text_content(), "also this"),
        other => panic!("unexpected {other:?}"),
    }
    assert!(matches!(second.messages[second.messages.len() - 2], Message::ToolResult(_)));
}

#[tokio::test]
async fn model_failures_fail_the_task() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![Script::fail(ErrorKind::Auth, "bad key")],
    )
    .await;
    let result = h.kernel.run(prompt("hi")).await.unwrap();
    match result.end {
        TaskEnd::Failed(error) => {
            assert_eq!(error.kind, "auth");
            assert_eq!(error.message, "bad key");
        }
        other => panic!("unexpected {other:?}"),
    }
}

#[tokio::test]
async fn a_reply_cut_off_is_asked_again_and_only_the_new_one_stays() {
    use her_llm::api::scripted::ScriptStep;
    let cut_off = Script {
        steps: vec![ScriptStep::Text("half a re".into()), ScriptStep::Fail(ErrorKind::Network, "stream interrupted".into())],
        ..Default::default()
    };
    let mut h = harness(PermissionMode::BypassPermissions, false, vec![cut_off, Script::text("the whole reply")]).await;
    h.kernel.submit(Op::prompt("hi")).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(matches!(events.last().unwrap().msg, EventMsg::TaskCompleted { .. }));
    assert!(events.iter().any(|e| matches!(e.msg, EventMsg::StreamRetry { attempt: 1, .. })));
    let started: Vec<&String> = events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::MessageStarted { message_id, role: her_protocol::Role::Assistant } => Some(message_id),
            _ => None,
        })
        .collect();
    assert_eq!(started.len(), 2);
    assert_ne!(started[0], started[1]);
    let replies: Vec<String> = events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::MessageCompleted { message: Message::Assistant(a), .. } => Some(a.text()),
            _ => None,
        })
        .collect();
    assert_eq!(replies, ["the whole reply"]);
}

#[tokio::test]
async fn bad_tool_calls_become_error_results() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![
            Script::tool_call("call_1", "nope", json!({})).then_tool_call("call_2", "read", json!({"path": 5})),
            Script::text("sorry"),
        ],
    )
    .await;
    let result = h.kernel.run(prompt("go")).await.unwrap();
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    let results = tool_results(&h.api.requests()[1]);
    assert_eq!(
        results[0],
        "Unknown tool 'nope'. Available tools: read, write, edit, bash, spawn_agent, send_message, tool_search, call_tool."
    );
    assert!(results[1].starts_with("invalid arguments:"), "{}", results[1]);
}

#[tokio::test]
async fn follow_up_runs_after_the_task_would_end() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![
            Script::text("one").then_sleep(Duration::from_millis(300)),
            Script::text("two"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("first")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::TurnStarted { .. })).await;
    h.kernel
        .submit(Op::Prompt {
            input: prompt("later"),
            mode: her_protocol::PromptMode::FollowUp,
            commands: true,
        })
        .await
        .unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    match &events.last().unwrap().msg {
        EventMsg::TaskCompleted { last_message, .. } => assert_eq!(last_message.as_deref(), Some("two")),
        other => panic!("unexpected {other:?}"),
    }
    assert_eq!(h.api.requests().len(), 2);
    // It was announced as waiting, and joined the history under the id it was announced with.
    let queued = first_queued(&events);
    assert_eq!(queued.mode, PromptMode::FollowUp);
    assert!(events.iter().any(|e| matches!(&e.msg,
        EventMsg::MessageCompleted { message_id, message: Message::User(u), .. } if *message_id == queued.id && u.text_content() == "later")));
}

/// The first input a `queue_changed` among `events` announced.
fn first_queued(events: &[Event]) -> QueuedInput {
    events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::QueueChanged { queued } => queued.first().cloned(),
            _ => None,
        })
        .expect("queue_changed")
}

/// Sends `op` while the task runs, and waits until the queue holds `len` items.
async fn queue(h: &mut Harness, op: Op, len: usize) -> Vec<QueuedInput> {
    h.kernel.submit(op).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::QueueChanged { queued } if queued.len() == len)).await;
    match &events.last().unwrap().msg {
        EventMsg::QueueChanged { queued } => queued.clone(),
        _ => unreachable!(),
    }
}

fn input(text: &str, mode: PromptMode) -> Op {
    Op::Prompt {
        input: prompt(text),
        mode,
        commands: true,
    }
}

fn user_texts(context: &her_llm::Context) -> Vec<String> {
    context
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::User(u) => Some(u.text_content()),
            _ => None,
        })
        .collect()
}

#[tokio::test]
async fn queued_input_joins_under_its_id_steering_first() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![
            Script::tool_call("call_1", "bash", json!({"command": "sleep 1"})),
            Script::text("one"),
            Script::text("two"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("first")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::ToolStarted { .. })).await;
    queue(&mut h, input("later", PromptMode::FollowUp), 1).await;
    let both = queue(&mut h, input("now", PromptMode::Steer), 2).await;
    // In the order they join: steering input first.
    let modes: Vec<PromptMode> = both.iter().map(|q| q.mode).collect();
    assert_eq!(modes, [PromptMode::Steer, PromptMode::FollowUp]);
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(matches!(events.last().unwrap().msg, EventMsg::TaskCompleted { .. }));
    // Each leaves the queue, then joins under its id.
    for item in &both {
        let left = events.iter().position(|e| matches!(&e.msg, EventMsg::QueueChanged { queued } if !queued.iter().any(|q| q.id == item.id))).unwrap();
        let joined = events.iter().position(|e| matches!(&e.msg, EventMsg::MessageCompleted { message_id, .. } if *message_id == item.id)).unwrap();
        assert_eq!(joined, left + 1, "{}", item.message.text_content());
    }
    let requests = h.api.requests();
    assert_eq!(user_texts(&requests[1]), ["first", "now"]);
    assert_eq!(user_texts(&requests[2]), ["first", "now", "later"]);
}

#[tokio::test]
async fn withdrawn_input_never_joins() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![Script::tool_call("call_1", "bash", json!({"command": "sleep 1"})), Script::text("done")],
    )
    .await;
    h.kernel.submit(Op::prompt("first")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::ToolStarted { .. })).await;
    let queued = queue(&mut h, input("drop me", PromptMode::Steer), 1).await;
    let id = queued[0].id.clone();
    h.kernel.submit(Op::Withdraw { id: id.clone() }).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::QueueChanged { queued } if queued.is_empty())).await;
    let withdrawn = events.iter().rev().nth(1).unwrap();
    assert!(matches!(&withdrawn.msg, EventMsg::InputWithdrawn { id: gone } if *gone == id));
    until(&mut h.kernel, |m| m.is_task_end()).await;
    assert_eq!(user_texts(&h.api.requests()[1]), ["first"]);

    // Once it is gone, or has joined, withdrawing it again says nothing.
    h.api.push(Script::text("again"));
    h.kernel.submit(Op::Withdraw { id }).await.unwrap();
    let result = h.kernel.run(prompt("next")).await.unwrap();
    assert!(!result.events.iter().any(|e| matches!(e.msg, EventMsg::InputWithdrawn { .. })));
}

#[tokio::test]
async fn a_follow_up_made_steering_joins_at_the_next_turn() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![Script::tool_call("call_1", "bash", json!({"command": "sleep 1"})), Script::text("done")],
    )
    .await;
    h.kernel.submit(Op::prompt("first")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::ToolStarted { .. })).await;
    let queued = queue(&mut h, input("soon", PromptMode::FollowUp), 1).await;
    h.kernel
        .submit(Op::Requeue {
            id: queued[0].id.clone(),
            mode: PromptMode::Steer,
        })
        .await
        .unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::QueueChanged { .. })).await;
    assert!(matches!(&events.last().unwrap().msg, EventMsg::QueueChanged { queued: now } if now[0].mode == PromptMode::Steer && now[0].id == queued[0].id));
    until(&mut h.kernel, |m| m.is_task_end()).await;
    // It went in with the tool's result, not after the task's last answer.
    let requests = h.api.requests();
    assert_eq!(requests.len(), 2);
    assert_eq!(user_texts(&requests[1]), ["first", "soon"]);
}

#[tokio::test]
async fn steer_now_ends_a_running_command_and_the_task_goes_on() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![Script::tool_call("call_1", "bash", json!({"command": "sleep 30"})), Script::text("changed course")],
    )
    .await;
    let started = Instant::now();
    h.kernel.submit(Op::prompt("build it")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::ToolRunning { .. })).await;
    queue(&mut h, input("stop, use the other way", PromptMode::Steer), 1).await;
    h.kernel.submit(Op::SteerNow).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(started.elapsed() < Duration::from_secs(10));
    match &events.last().unwrap().msg {
        EventMsg::TaskCompleted { last_message, .. } => assert_eq!(last_message.as_deref(), Some("changed course")),
        other => panic!("unexpected {other:?}"),
    }
    let requests = h.api.requests();
    assert_eq!(tool_results(&requests[1]), ["Cancelled by user."]);
    assert_eq!(user_texts(&requests[1]), ["build it", "stop, use the other way"]);
}

#[tokio::test]
async fn steer_now_ends_a_reply_being_written() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![
            Script::text("partial").then_sleep(Duration::from_secs(30)).then_text("never"),
            Script::text("after"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("talk")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::MessageDelta { .. })).await;
    queue(&mut h, input("shorter please", PromptMode::Steer), 1).await;
    h.kernel.submit(Op::SteerNow).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    let replies: Vec<(String, her_llm::StopReason)> = events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::MessageCompleted { message: Message::Assistant(a), .. } => Some((a.text(), a.stop_reason)),
            _ => None,
        })
        .collect();
    assert_eq!(replies[0], ("partial".to_string(), her_llm::StopReason::Aborted));
    assert!(matches!(&events.last().unwrap().msg, EventMsg::TaskCompleted { last_message, .. } if last_message.as_deref() == Some("after")));
    assert_eq!(user_texts(&h.api.requests()[1]), ["talk", "shorter please"]);
}

#[tokio::test]
async fn steer_now_without_steering_input_changes_nothing() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![Script::text("a").then_sleep(Duration::from_millis(400)).then_text("b")],
    )
    .await;
    h.kernel.submit(Op::prompt("talk")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::MessageDelta { .. })).await;
    queue(&mut h, input("after it", PromptMode::FollowUp), 1).await;
    h.api.push(Script::text("then this"));
    h.kernel.submit(Op::SteerNow).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    let replies: Vec<String> = events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::MessageCompleted { message: Message::Assistant(a), .. } => Some(a.text()),
            _ => None,
        })
        .collect();
    assert_eq!(replies, ["ab", "then this"]);
}

#[tokio::test]
async fn a_failed_task_hands_waiting_input_back() {
    let mut h = harness(
        PermissionMode::BypassPermissions,
        false,
        vec![
            Script::tool_call("call_1", "bash", json!({"command": "sleep 1"})),
            Script::fail(ErrorKind::Auth, "bad key"),
        ],
    )
    .await;
    h.kernel.submit(Op::prompt("first")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::ToolStarted { .. })).await;
    queue(&mut h, input("after it", PromptMode::FollowUp), 1).await;
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    match &events.last().unwrap().msg {
        EventMsg::TaskFailed { returned_input, .. } => {
            assert_eq!(returned_input.iter().map(|m| m.text_content()).collect::<Vec<_>>(), ["after it"]);
        }
        other => panic!("unexpected {other:?}"),
    }
    assert!(matches!(&events[events.len() - 2].msg, EventMsg::QueueChanged { queued } if queued.is_empty()));
    assert!(!events.iter().any(|e| matches!(e.msg, EventMsg::Warning { .. })));
}

/// Input sent in a burst, some of it as tasks end: one task at a time, each input once, in order.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_burst_of_input_goes_in_once_each_in_order() {
    let mut h = harness(PermissionMode::BypassPermissions, false, Vec::new()).await;
    h.api.respond_with(|_| Some(Script::text("ok")));
    let sent: Vec<String> = (0..40).map(|i| format!("p{i}")).collect();
    for (i, text) in sent.iter().enumerate() {
        h.kernel.submit(Op::prompt(text.clone())).await.unwrap();
        // Some land while the task before them is ending.
        if i % 3 == 0 {
            tokio::time::sleep(Duration::from_millis(1)).await;
        }
    }
    let mut running = 0;
    let mut joined = Vec::new();
    loop {
        let events = until(&mut h.kernel, |m| m.is_task_end() || matches!(m, EventMsg::Idle)).await;
        for event in &events {
            match &event.msg {
                EventMsg::TaskStarted { .. } => running += 1,
                msg if msg.is_task_end() => running -= 1,
                EventMsg::MessageCompleted { message: Message::User(u), .. } => joined.push(u.text_content()),
                _ => {}
            }
            assert!(running <= 1, "two tasks ran at once");
        }
        if matches!(events.last().unwrap().msg, EventMsg::Idle) && joined.len() == sent.len() {
            break;
        }
    }
    // Each went in once, in the order sent.
    assert_eq!(joined, sent);
}

/// A file-changing tool for kernel tests: it records the arguments it runs with, its check can
/// fail, and it can hang without looking at the cancellation token.
struct Probe {
    spec: ToolSpec,
    check_error: Option<&'static str>,
    hang: bool,
    ran: Arc<Mutex<Vec<Value>>>,
}

impl Probe {
    /// The tool, and the arguments of the calls it ran.
    fn build(check_error: Option<&'static str>, hang: bool) -> (Arc<dyn Tool>, Arc<Mutex<Vec<Value>>>) {
        let ran = Arc::new(Mutex::new(Vec::new()));
        let probe = Probe {
            spec: ToolSpec {
                name: "probe".into(),
                description: "Probes.".into(),
                parameters: json!({"type": "object", "properties": {}}),
            },
            check_error,
            hang,
            ran: ran.clone(),
        };
        (Arc::new(probe), ran)
    }
}

#[async_trait::async_trait]
impl Tool for Probe {
    fn spec(&self) -> &ToolSpec {
        &self.spec
    }

    fn kind(&self) -> ToolKind {
        ToolKind::Mutating
    }

    /// `items` sent as a JSON string, as some models do.
    fn prepare_arguments(&self, mut args: Value) -> Value {
        if let Some(items) = args.get("items").and_then(Value::as_str).and_then(|s| serde_json::from_str(s).ok()) {
            args["items"] = items;
        }
        args
    }

    async fn check(&self, _args: &Value, _ctx: &ToolCtx) -> Result<(), ToolError> {
        match self.check_error {
            Some(problem) => Err(ToolError::Failed(problem.into())),
            None => Ok(()),
        }
    }

    async fn execute(&self, args: Value, _ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        self.ran.lock().unwrap().push(args);
        if self.hang {
            std::future::pending::<()>().await;
        }
        Ok(ToolOutput::text("probed"))
    }
}

#[tokio::test]
async fn calls_that_cannot_succeed_are_not_put_to_the_user() {
    let (probe, ran) = Probe::build(Some("The file is gone."), false);
    let scripts = vec![Script::tool_call("call_1", "probe", json!({})), Script::text("ok")];
    let mut h = start(Config::default(), PermissionMode::Manual, true, scripts, vec![probe]).await;
    h.kernel.submit(Op::prompt("probe it")).await.unwrap();
    let events = until(&mut h.kernel, |m| {
        matches!(m, EventMsg::ApprovalRequested { .. }) || m.is_task_end()
    })
    .await;
    assert!(matches!(events.last().unwrap().msg, EventMsg::TaskCompleted { .. }));
    assert!(ran.lock().unwrap().is_empty());
    assert_eq!(tool_results(&h.api.requests()[1]), vec!["The file is gone."]);
}

#[tokio::test]
async fn no_checks_after_a_call_that_runs_first() {
    // The write and the read run before the probe, which is therefore not judged on the state
    // before them.
    let (probe, ran) = Probe::build(Some("The file is gone."), false);
    let scripts = vec![
        Script::tool_call("call_1", "write", json!({"path": "a.txt", "content": "a"}))
            .then_tool_call("call_2", "probe", json!({}))
            .then_tool_call("call_3", "read", json!({"path": "a.txt"}))
            .then_tool_call("call_4", "probe", json!({})),
        Script::text("ok"),
    ];
    let mut h = start(Config::default(), PermissionMode::Manual, true, scripts, vec![probe]).await;
    h.kernel.submit(Op::prompt("go")).await.unwrap();
    for _ in 0..3 {
        let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
        let EventMsg::ApprovalRequested { call_id, .. } = &events.last().unwrap().msg else {
            panic!()
        };
        h.kernel
            .submit(Op::Approval {
                call_id: call_id.clone(),
                decision: ApprovalDecision::Approve,
            })
            .await
            .unwrap();
    }
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert!(h.work.join("a.txt").exists());
    assert_eq!(ran.lock().unwrap().len(), 2);
}

#[tokio::test]
async fn arguments_are_repaired_before_anything_sees_them() {
    let (probe, ran) = Probe::build(None, false);
    let scripts = vec![Script::tool_call("call_1", "probe", json!({"items": "[1, 2]"})), Script::text("ok")];
    let mut h = start(Config::default(), PermissionMode::BypassPermissions, false, scripts, vec![probe]).await;
    let result = h.kernel.run(prompt("probe it")).await.unwrap();
    let started = result
        .events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::ToolStarted { args, .. } => Some(args.clone()),
            _ => None,
        })
        .unwrap();
    assert_eq!(started, json!({"items": [1, 2]}));
    assert_eq!(*ran.lock().unwrap(), vec![json!({"items": [1, 2]})]);
}

#[tokio::test]
async fn a_tool_that_ignores_cancellation_cannot_hold_the_session() {
    let (probe, ran) = Probe::build(None, true);
    let scripts = vec![Script::tool_call("call_1", "probe", json!({})), Script::text("unused")];
    let mut h = start(Config::default(), PermissionMode::BypassPermissions, false, scripts, vec![probe]).await;
    h.kernel.submit(Op::prompt("hang")).await.unwrap();
    until(&mut h.kernel, |m| matches!(m, EventMsg::ToolRunning { .. })).await;
    assert_eq!(ran.lock().unwrap().len(), 1);
    let interrupted = Instant::now();
    h.kernel.submit(Op::Interrupt).await.unwrap();
    let events = until(&mut h.kernel, |m| m.is_task_end()).await;
    assert!(matches!(events.last().unwrap().msg, EventMsg::TaskInterrupted { .. }));
    assert!(interrupted.elapsed() < Duration::from_secs(6), "{:?}", interrupted.elapsed());
    assert!(events.iter().any(|e| match &e.msg {
        EventMsg::ToolCompleted { result, .. } => result.text_content() == "Cancelled by user.",
        _ => false,
    }));
}

#[tokio::test]
async fn a_session_whose_provider_was_removed_resumes_on_the_configured_model() {
    let mut h = harness(PermissionMode::BypassPermissions, false, vec![Script::text("hi")]).await;
    h.kernel.run(prompt("hello")).await.unwrap();
    let session_id = h.kernel.session_id().to_string();
    h.kernel.shutdown().await;

    // The provider it used ("test", as a removed connection's) is gone; another one is configured.
    let api = ScriptedApi::new();
    api.push(Script::text("still here"));
    let mut deps = deps(&api, &h.work, &h.home);
    let mut registry = ModelRegistry::new(Catalog::default(), RegistryOptions::default());
    registry.register_api(api.clone());
    let mut other = ScriptedApi::provider();
    other.id = "other".into();
    registry.insert_provider(other);
    deps.llm = Arc::new(registry);
    let config = Config { model: Some("other/scripted".into()), ..Config::default() };
    let options = ResumeOptions {
        session: SessionRef::Id(her_core::short_id(&session_id).to_string()),
        model: None,
        thinking: None,
        permission_mode: None,
        home: h.home.clone(),
        config: Arc::new(config),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot_name: None,
    };
    let mut kernel = Kernel::resume(options, deps).await.unwrap();
    let events = until(&mut kernel, |m| matches!(m, EventMsg::SessionStarted { .. })).await;
    let EventMsg::SessionStarted { model, resumed, .. } = &events.last().unwrap().msg else { panic!() };
    assert!(*resumed);
    assert_eq!(model, "other/scripted");
    // Said among the session's warnings (bypass mode's comes first).
    until(&mut kernel, |m| {
        matches!(m, EventMsg::Warning { message, .. } if message.contains("'test/scripted' is not available"))
    })
    .await;
    // The conversation carries on with the configured model.
    let result = kernel.run(prompt("still there?")).await.unwrap();
    assert_eq!(result.end, TaskEnd::Completed { last_message: Some("still here".into()) });
}

/// The session's log under `dir`, found by the session id in its file name.
fn find_log(dir: &Path, short: &str) -> Option<PathBuf> {
    for entry in std::fs::read_dir(dir).ok()?.flatten() {
        let path = entry.path();
        if path.is_dir() {
            if let Some(found) = find_log(&path, short) {
                return Some(found);
            }
        } else if path.file_name().and_then(|n| n.to_str()).is_some_and(|n| n.contains(short)) {
            return Some(path);
        }
    }
    None
}

/// Rewrites the date in the session's system prompt, as if the session had started on `day`.
fn backdate(home: &HerHome, session_id: &str, day: chrono::NaiveDate) {
    let log = find_log(&home.sessions_dir(), her_core::short_id(session_id)).expect("the session's log");
    let text = std::fs::read_to_string(&log).unwrap();
    let written = text
        .lines()
        .filter_map(|line| serde_json::from_str::<Value>(line).ok())
        .find(|entry| entry["type"] == "system_prompt")
        .and_then(|entry| her_core::prompt::prompt_date(entry["text"].as_str()?))
        .expect("a dated system prompt");
    let (from, to) = (her_core::prompt::date_text(written), her_core::prompt::date_text(day));
    std::fs::write(&log, text.replace(&from, &to)).unwrap();
}

async fn resumed(home: &HerHome, work: &Path, session_id: &str, api: &Arc<ScriptedApi>) -> Kernel {
    let options = ResumeOptions {
        session: SessionRef::Id(her_core::short_id(session_id).to_string()),
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

fn date_update(day: chrono::NaiveDate) -> String {
    format!("<environment_update>\ndate: {}\n</environment_update>", her_core::prompt::date_text(day))
}

#[tokio::test]
async fn a_session_continued_on_a_later_day_is_told_the_date_once() {
    let mut h = harness(PermissionMode::BypassPermissions, false, vec![Script::text("first")]).await;
    h.kernel.run(prompt("hello")).await.unwrap();
    // Started today: the system prompt has the date, with its weekday, and nothing is added.
    let first = &h.api.requests()[0];
    let started = her_core::prompt::prompt_date(&first.system).expect("a date in the environment");
    assert!(first.system.contains(&format!("\ndate: {}\n", her_core::prompt::date_text(started))));
    assert_eq!(user_texts(first), ["hello"]);
    let session_id = h.kernel.session_id().to_string();
    h.kernel.shutdown().await;

    // As if it had started on New Year's Day: continued today, the model is told the date
    // before the new message, and only once.
    backdate(&h.home, &session_id, chrono::NaiveDate::from_ymd_opt(2026, 1, 1).unwrap());
    let api = ScriptedApi::new();
    api.push(Script::text("second"));
    api.push(Script::text("third"));
    let mut kernel = resumed(&h.home, &h.work, &session_id, &api).await;
    kernel.run(prompt("next")).await.unwrap();
    kernel.run(prompt("again")).await.unwrap();
    let today = date_update(chrono::Local::now().date_naive());
    let requests = api.requests();
    assert!(requests[0].system.contains("\ndate: 2026-01-01 (Thursday)\n"), "the system prompt stays as it was");
    assert_eq!(user_texts(&requests[0]), ["hello", today.as_str(), "next"]);
    assert_eq!(user_texts(&requests[1]), ["hello", today.as_str(), "next", "again"]);
    // Only the model sees it.
    assert!(!format!("{:?}", kernel.transcript()).contains("environment_update"));
    kernel.shutdown().await;

    // The log keeps it: resumed again, it is not repeated.
    let api = ScriptedApi::new();
    api.push(Script::text("fourth"));
    let mut kernel = resumed(&h.home, &h.work, &session_id, &api).await;
    kernel.run(prompt("once more")).await.unwrap();
    assert_eq!(user_texts(&api.requests()[0]), ["hello", today.as_str(), "next", "again", "once more"]);
}

#[tokio::test]
async fn a_sub_agent_of_an_older_session_is_told_the_date_first() {
    let mut h = harness(PermissionMode::BypassPermissions, false, vec![Script::text("first")]).await;
    h.kernel.run(prompt("hello")).await.unwrap();
    let session_id = h.kernel.session_id().to_string();
    h.kernel.shutdown().await;
    backdate(&h.home, &session_id, chrono::NaiveDate::from_ymd_opt(2026, 1, 1).unwrap());

    // The sub-agent shares the parent's system prompt, from New Year's Day.
    let api = ScriptedApi::new();
    api.push(Script::tool_call(
        "call_1",
        "spawn_agent",
        json!({"description": "look around", "task": "look around", "name": "scout", "wait": true}),
    ));
    api.push(Script::text("nothing here"));
    api.push(Script::text("done"));
    let mut kernel = resumed(&h.home, &h.work, &session_id, &api).await;
    let result = kernel.run(prompt("send a scout")).await.unwrap();
    assert_eq!(result.end, TaskEnd::Completed { last_message: Some("done".into()) });
    let child = &api.requests()[1];
    assert!(child.system.contains("\ndate: 2026-01-01 (Thursday)\n"));
    let texts = user_texts(child);
    assert_eq!(texts.len(), 2, "{texts:?}");
    assert_eq!(texts[0], date_update(chrono::Local::now().date_naive()));
    assert!(texts[1].starts_with("You are a sub-agent named \"scout\""), "then its task");
}
