//! Can the model get back what was cut from a tool result? A scripted "reading" model uses
//! only what results tell it: the note's path, line range and offset, then read's own
//! "Use offset=N to continue" hints, or for a long line the byte range and the command in the
//! note. The test then puts the head, the pages read back and the tail together and compares
//! the result with the original output, byte for byte.

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
use her_llm::Catalog;
use her_llm::Context;
use her_llm::Message;
use her_llm::ModelRegistry;
use her_llm::ProviderConfig;
use her_llm::RegistryOptions;
use her_llm::ThinkingLevel;
use her_llm::ToolResultMessage;
use her_llm::ToolSpec;
use her_llm::UserContent;
use her_llm::api::scripted::Script;
use her_llm::catalog::ModelToml;
use her_llm::api::scripted::ScriptedApi;
use her_protocol::PermissionMode;
use her_protocol::EventMsg;
use her_tools::BashTool;
use her_tools::LocalEnvironment;
use her_tools::Tool;
use her_tools::ToolCtx;
use her_tools::ToolError;
use her_tools::ToolOutput;
use her_tools::builtin_registry;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use tempfile::TempDir;

/// Lines per read: small, so that reading back takes several pages.
const PAGE: u64 = 150;
/// Bytes per command when a long line is read back by position.
const CHUNK: u64 = 1000;

fn provider() -> ProviderConfig {
    // Hard limit 18,000: tool results get 7,200 bytes (bash: 1,440 head, 5,760 tail).
    ScriptedApi::provider_with(vec![ModelToml { context_window: Some(20_000), max_output_tokens: Some(2_000), ..ScriptedApi::entry() }])
}

/// Prints `big_text()`, so the kernel's safety net (not a tool's own limit) cuts it.
struct BigTool(ToolSpec);

#[async_trait::async_trait]
impl Tool for BigTool {
    fn spec(&self) -> &ToolSpec {
        &self.0
    }

    async fn execute(&self, _args: Value, _ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        Ok(ToolOutput::text(big_text()))
    }
}

fn big_text() -> String {
    (1..=3000).map(|n| format!("record {n}: {}\n", "·".repeat(n % 7))).collect()
}

fn deps(api: &Arc<ScriptedApi>, work: &Path, home: &HerHome) -> KernelDeps {
    let mut registry = ModelRegistry::new(Catalog::default(), RegistryOptions::default());
    registry.register_api(api.clone());
    registry.insert_provider(provider());
    let mut tools = builtin_registry(BashTool::default());
    tools
        .register(Arc::new(BigTool(ToolSpec {
            name: "big".into(),
            description: "Prints a lot.".into(),
            parameters: json!({"type": "object", "properties": {}}),
        })))
        .unwrap();
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
    }
}

struct Harness {
    kernel: Kernel,
    api: Arc<ScriptedApi>,
    work: PathBuf,
    home: HerHome,
    _dir: TempDir,
}

async fn harness(config: Config) -> Harness {
    let dir = tempfile::tempdir().unwrap();
    let work = dir.path().join("work");
    std::fs::create_dir_all(&work).unwrap();
    let home = HerHome::new(dir.path().join("home"));
    home.ensure().unwrap();
    let api = ScriptedApi::new();
    api.respond_with(reading_model);
    let options = StartOptions {
        model: "test/scripted".into(),
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

fn prompt(text: &str) -> Vec<UserContent> {
    vec![UserContent::Text { text: text.into() }]
}

/// Runs a task; a model that loops fails the test instead of hanging it.
async fn run(kernel: &mut Kernel, text: &str) -> TaskResult {
    tokio::time::timeout(std::time::Duration::from_secs(60), kernel.run(prompt(text)))
        .await
        .expect("the task did not finish within 60 seconds")
        .unwrap()
}

fn text_of(message: &Message) -> String {
    match message {
        Message::User(u) => u.text_content(),
        Message::Assistant(a) => a.text(),
        Message::ToolResult(r) => r.text_content(),
    }
}

/// What a note says was cut.
#[derive(Clone, Debug, PartialEq)]
enum Cut {
    Lines { path: String, first: u64, last: u64 },
    Bytes { path: String, start: u64, end: u64 },
}

fn number_after(text: &str, marker: &str) -> Option<u64> {
    let at = text.find(marker)? + marker.len();
    text[at..].chars().take_while(char::is_ascii_digit).collect::<String>().parse().ok()
}

/// Reads the last note in `text`, the way a model would.
fn find_cut(text: &str) -> Option<Cut> {
    let lines = text.rfind("[Lines ");
    let bytes = text.rfind("[Bytes ");
    let at = lines.max(bytes)?;
    let note = &text[at..];
    let path_start = note.find("Full output")?;
    let path_start = path_start + note[path_start..].find(": ")? + 2;
    let path_end = path_start + note[path_start..].find(" (")?;
    let path = note[path_start..path_end].to_string();
    if Some(at) == lines {
        Some(Cut::Lines {
            path,
            first: number_after(note, "[Lines ")?,
            last: number_after(note, &format!("[Lines {}-", number_after(note, "[Lines ")?))?,
        })
    } else {
        Some(Cut::Bytes {
            path,
            start: number_after(note, "[Bytes ")?,
            end: number_after(note, &format!("[Bytes {}-", number_after(note, "[Bytes ")?))?,
        })
    }
}

const PROGRESS: &str = "Read back through ";

/// The scripted model: after a cut result, it reads the omitted part back page by page (or
/// chunk by chunk for a long line), then answers "DONE". It works out where it is from the
/// conversation alone: the latest note, the tool results since the user's last message, and
/// after a compaction its own summary, which carries the note and how far it got. Reading a
/// large part back can itself fill the window, so compactions happen in the middle of it.
fn reading_model(ctx: &Context) -> Option<Script> {
    let messages = &ctx.messages;
    let cut = messages.iter().rev().find_map(|m| find_cut(&text_of(m)))?;
    let users: Vec<usize> = (0..messages.len())
        .filter(|&i| matches!(messages[i], Message::User(_)))
        .collect();
    let handoff = messages.last().is_some_and(|m| text_of(m).contains("<handoff_request>"));
    // Progress counts from the user message that started this stretch of work (the task, or
    // the summary of an earlier compaction), not from one that only carries a handoff request.
    let anchor = users
        .iter()
        .rev()
        .copied()
        .find(|&i| !(handoff && i == messages.len() - 1));
    let since = anchor.map_or(0, |i| i + 1);
    let results: Vec<&ToolResultMessage> = messages[since..]
        .iter()
        .filter_map(|m| match m {
            Message::ToolResult(r) => Some(r.as_ref()),
            _ => None,
        })
        .collect();
    let resumed_at = anchor.and_then(|i| number_after(&text_of(&messages[i]), PROGRESS));
    let id = format!("call_r{}", messages.len());
    match &cut {
        Cut::Lines { path, first, last } => {
            let next = match results.iter().rev().find(|r| r.tool_name == "read") {
                Some(read) => number_after(&read.text_content(), "Use offset=").unwrap_or(u64::MAX),
                None => resumed_at.map_or(*first, |line| line + 1),
            };
            if handoff {
                let note = note_line(messages, &cut);
                return Some(Script::text(format!("Reading back cut output. {note}\n{PROGRESS}{}", next - 1)));
            }
            if next > *last {
                return Some(Script::text("DONE"));
            }
            Some(Script::tool_call(id, "read", json!({"path": path, "offset": next, "limit": PAGE})))
        }
        Cut::Bytes { path, start, end } => {
            let chunks = results
                .iter()
                .filter(|r| r.tool_name == "bash" && find_cut(&r.text_content()).is_none())
                .count() as u64;
            let from = resumed_at.map_or(*start, |byte| byte + 1) + chunks * CHUNK;
            if handoff {
                let note = note_line(messages, &cut);
                return Some(Script::text(format!("Reading back cut output. {note}\n{PROGRESS}{}", from - 1)));
            }
            if from > *end {
                return Some(Script::text("DONE"));
            }
            let to = (from + CHUNK - 1).min(*end);
            Some(Script::tool_call(id, "bash", json!({"command": format!("head -c {to} {path} | tail -c +{from}")})))
        }
    }
}

/// The note line that describes `cut`, copied into a summary.
fn note_line(messages: &[Message], cut: &Cut) -> String {
    messages
        .iter()
        .rev()
        .map(text_of)
        .flat_map(|text| text.lines().map(str::to_string).collect::<Vec<_>>())
        .find(|line| find_cut(line).as_ref() == Some(cut))
        .unwrap()
}

fn tool_results(result: &TaskResult) -> Vec<ToolResultMessage> {
    result
        .events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::ToolCompleted { result, .. } => Some(result.as_ref().clone()),
            _ => None,
        })
        .collect()
}

/// The command output inside a bash result.
fn bash_body(text: &str) -> &str {
    let at = text.find("Output:\n").expect(text) + "Output:\n".len();
    &text[at..]
}

/// Puts a cut result back together: its head, what was read back, its tail.
fn reassemble(body: &str, cut: &Cut, reads: &[String]) -> String {
    let note_at = body.find("[Lines ").or_else(|| body.find("[Bytes ")).expect(body);
    let note_end = note_at + body[note_at..].find("]\n").unwrap() + 2;
    let tail = &body[note_end..];
    match cut {
        Cut::Lines { first, last, .. } => {
            let mut lines: Vec<&str> = Vec::new();
            for page in reads {
                let content = page.split("\n\n[Showing lines").next().unwrap();
                lines.extend(content.lines());
            }
            let wanted = (last - first + 1) as usize;
            assert!(lines.len() >= wanted, "read back {} of {wanted} lines", lines.len());
            let middle: String = lines[..wanted].iter().map(|l| format!("{l}\n")).collect();
            format!("{}{middle}{tail}", &body[..note_at])
        }
        Cut::Bytes { start, .. } => {
            // The head is the first start-1 bytes; a newline was added before the note.
            let head = &body[..*start as usize - 1];
            let middle: String = reads
                .iter()
                .map(|r| {
                    let chunk = bash_body(r);
                    if chunk == "(no output)" { "" } else { chunk }.to_string()
                })
                .collect();
            format!("{head}{middle}{tail}")
        }
    }
}

/// Runs a command that prints `expected`, lets the model read the cut part back, and checks
/// that nothing was lost or changed.
/// Runs a command that prints `expected`, lets the model read the cut part back, and checks
/// that nothing was lost or changed. Returns the cut and how many compactions happened while
/// reading (a large cut part does not fit in the window at once).
async fn run_and_read_back(command: &str, expected: &str) -> (Harness, Cut, usize) {
    let mut h = harness(Config::default()).await;
    h.api.push(Script::tool_call("call_1", "bash", json!({"command": command})));
    let result = run(&mut h.kernel, "print it, then read what was cut").await;
    assert_eq!(
        result.end,
        TaskEnd::Completed {
            last_message: Some("DONE".into())
        }
    );
    let results = tool_results(&result);
    let produced = results[0].text_content();
    let body = bash_body(&produced);
    let cut = find_cut(body).expect(body);
    let reads: Vec<String> = results[1..].iter().map(ToolResultMessage::text_content).collect();
    assert!(reads.len() >= 2, "{} read(s)", reads.len());
    assert_eq!(reassemble(body, &cut, &reads), expected);
    (h, cut, compactions(&result))
}

fn compactions(result: &TaskResult) -> usize {
    result
        .events
        .iter()
        .filter(|e| matches!(e.msg, EventMsg::CompactionCompleted { .. }))
        .count()
}

#[tokio::test]
async fn numbered_lines_with_tabs_come_back_exactly() {
    let expected: String = (1..=3000u64).map(|i| format!("line {i}\tsquare={}\n", i * i)).collect();
    let (h, cut, compacted) = run_and_read_back(
        "for i in $(seq 1 3000); do printf 'line %d\\tsquare=%d\\n' $i $((i*i)); done",
        &expected,
    )
    .await;
    let Cut::Lines { path, .. } = cut else { panic!("{cut:?}") };
    assert_eq!(std::fs::read_to_string(path).unwrap(), expected);
    assert!(h.api.calls().len() > 10, "read back over several pages");
    // The cut part (about 17,000 tokens) does not fit next to everything else: the model
    // compacts while reading and carries on from where its summary says it stopped.
    assert!(compacted >= 1, "{compacted} compaction(s)");
}

#[tokio::test]
async fn chinese_text_comes_back_exactly() {
    let expected: String = (1..=1500)
        .map(|i| format!("第 {i} 行：上下文压缩测试，中文内容需要逐字节一致\n"))
        .collect();
    let (_, _, compacted) = run_and_read_back(
        "for i in $(seq 1 1500); do echo \"第 $i 行：上下文压缩测试，中文内容需要逐字节一致\"; done",
        &expected,
    )
    .await;
    assert!(compacted >= 2, "{compacted} compaction(s)");
}

#[tokio::test]
async fn coloured_output_comes_back_as_the_same_clean_text() {
    let expected: String = (1..=2500).map(|i| format!("warn {i}\n")).collect();
    run_and_read_back(
        "for i in $(seq 1 2500); do printf '\\033[33mwarn\\033[0m %d\\r\\n' $i; done",
        &expected,
    )
    .await;
}

#[tokio::test]
async fn a_long_line_comes_back_by_byte_range() {
    let mut expected: String = (1..=6000).map(|n| format!("{n} ")).collect();
    expected.push('\n');
    let (_, cut, _) = run_and_read_back("seq 1 6000 | tr '\\n' ' '; echo", &expected).await;
    assert!(matches!(cut, Cut::Bytes { .. }), "{cut:?}");
}

#[tokio::test]
async fn output_cut_by_the_safety_net_comes_back_exactly() {
    let mut h = harness(Config::default()).await;
    h.api.push(Script::tool_call("call_1", "big", json!({})));
    let result = run(&mut h.kernel, "print it, then read what was cut").await;
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    let results = tool_results(&result);
    let produced = results[0].text_content();
    let cut = find_cut(&produced).expect(&produced);
    let reads: Vec<String> = results[1..].iter().map(ToolResultMessage::text_content).collect();
    assert_eq!(reassemble(&produced, &cut, &reads), big_text());
}

#[tokio::test]
async fn reused_call_ids_keep_separate_files() {
    let mut h = harness(Config::default()).await;
    // Some providers number calls per response, so the same id comes back every turn.
    h.api.push(Script::tool_call("call_0", "bash", json!({"command": "seq 1 3000"})));
    h.api.push(Script::tool_call("call_0", "bash", json!({"command": "seq 5001 8000"})));
    h.api.push(Script::text("two outputs"));
    let result = run(&mut h.kernel, "print twice").await;
    let results = tool_results(&result);
    let cuts: Vec<Cut> = results.iter().map(|r| find_cut(&r.text_content()).unwrap()).collect();
    let paths: Vec<String> = cuts
        .iter()
        .map(|c| match c {
            Cut::Lines { path, .. } => path.clone(),
            other => panic!("{other:?}"),
        })
        .collect();
    assert!(paths[0].ends_with("/call_0.txt") && paths[1].ends_with("/call_0-1.txt"), "{paths:?}");
    let first: String = (1..=3000).map(|n| format!("{n}\n")).collect();
    let second: String = (5001..=8000).map(|n| format!("{n}\n")).collect();
    assert_eq!(std::fs::read_to_string(&paths[0]).unwrap(), first);
    assert_eq!(std::fs::read_to_string(&paths[1]).unwrap(), second);
}

#[tokio::test]
async fn saved_output_is_still_readable_after_a_compaction_and_a_resume() {
    let expected: String = (1..=3000).map(|n| format!("{n}\n")).collect();
    let mut h = harness(Config::default()).await;
    // Task 1: the output is cut; the reply reports a context above the soft limit.
    h.api.push(Script::tool_call("call_1", "bash", json!({"command": "seq 1 3000"})));
    h.api.push(Script::text("printed").with_usage(17_000, 10));
    let first = run(&mut h.kernel, "print the numbers").await;
    let produced = tool_results(&first)[0].text_content();
    let body = bash_body(&produced).to_string();
    let note = body.lines().find(|l| l.starts_with("[Lines ")).unwrap().to_string();
    let cut = find_cut(&note).unwrap();

    // Task 2 compacts first. The tool result is gone from the context; only the summary
    // remembers where the output is, and that is all the model has to go on.
    h.api.push(Script::text(format!(
        "Printed the numbers 1 to 3000; part of the output was cut: {note}"
    )));
    let second = run(&mut h.kernel, "now read what was cut").await;
    assert!(second.events.iter().any(|e| matches!(e.msg, EventMsg::CompactionCompleted { .. })));
    // The first request after the compaction: the cut result is no longer in the context.
    let calls = h.api.calls();
    let compacted = calls
        .iter()
        .position(|c| c.context.messages.last().is_some_and(|m| text_of(m).contains("<handoff_request>")))
        .unwrap();
    assert!(!calls[compacted + 1].context.messages.iter().any(|m| text_of(m).contains("Exit code:")));
    let reads: Vec<String> = tool_results(&second).iter().map(ToolResultMessage::text_content).collect();
    assert_eq!(reassemble(&body, &cut, &reads), expected);

    // Task 3, in a new process: the file is where the note said.
    let session_id = h.kernel.session_id().to_string();
    let Harness {
        kernel, work, home, _dir, ..
    } = h;
    kernel.shutdown().await;
    let api = ScriptedApi::new();
    api.respond_with(reading_model);
    let options = ResumeOptions {
        session: SessionRef::Id(session_id),
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
    let mut kernel = Kernel::resume(options, deps(&api, &work, &home)).await.unwrap();
    let third = run(&mut kernel, "read it once more").await;
    assert!(matches!(third.end, TaskEnd::Completed { .. }));
    let reads: Vec<String> = tool_results(&third).iter().map(ToolResultMessage::text_content).collect();
    assert_eq!(reassemble(&body, &cut, &reads), expected);
}

#[tokio::test]
async fn a_capped_file_says_it_holds_only_the_first_part() {
    let mut config = Config::default();
    config.limits.max_saved_bytes = 4096;
    let mut h = harness(config).await;
    h.api.push(Script::tool_call("call_1", "bash", json!({"command": "seq 1 5000"})));
    h.api.push(Script::text("ok"));
    let result = run(&mut h.kernel, "print").await;
    let produced = tool_results(&result)[0].text_content();
    assert!(produced.contains("Full output (first part only): "), "{produced}");
    let Some(Cut::Lines { path, .. }) = find_cut(&produced) else { panic!("{produced}") };
    let saved = std::fs::read_to_string(path).unwrap();
    assert_eq!(saved.len(), 4096);
    let full: String = (1..=5000).map(|n| format!("{n}\n")).collect();
    assert!(full.starts_with(&saved));
}
