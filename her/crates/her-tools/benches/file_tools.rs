//! Latency and memory of read, write and edit on generated files.
//!
//!     cargo bench -p her-tools --bench file_tools [-- <name filter>]
//!
//! Every scenario runs the real tool through its public interface on a local file system.
//! Times are wall-clock per call (median and slowest of the runs); "peak heap" is the most
//! memory the call had allocated at once, measured by a counting allocator.

#![allow(clippy::print_stdout)]

use her_tools::FileTracker;
use her_tools::LocalEnvironment;
use her_tools::OutputLimits;
use her_tools::ProgressSink;
use her_tools::Tool;
use her_tools::ToolCtx;
use her_tools::ToolOutput;
use her_tools::Typed;
use her_tools::builtin::EditTool;
use her_tools::builtin::ReadTool;
use her_tools::builtin::WriteTool;
use serde_json::Value;
use serde_json::json;
use std::alloc::GlobalAlloc;
use std::alloc::Layout;
use std::alloc::System;
use std::path::Path;
use std::sync::Arc;
use std::sync::atomic::AtomicUsize;
use std::sync::atomic::Ordering;
use std::time::Duration;
use std::time::Instant;
use tokio_util::sync::CancellationToken;

struct Counting;

static CURRENT: AtomicUsize = AtomicUsize::new(0);
static PEAK: AtomicUsize = AtomicUsize::new(0);

fn grow(size: usize) {
    let now = CURRENT.fetch_add(size, Ordering::Relaxed) + size;
    PEAK.fetch_max(now, Ordering::Relaxed);
}

unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let ptr = unsafe { System.alloc(layout) };
        if !ptr.is_null() {
            grow(layout.size());
        }
        ptr
    }

    unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
        let ptr = unsafe { System.alloc_zeroed(layout) };
        if !ptr.is_null() {
            grow(layout.size());
        }
        ptr
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        unsafe { System.dealloc(ptr, layout) };
        CURRENT.fetch_sub(layout.size(), Ordering::Relaxed);
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        let new = unsafe { System.realloc(ptr, layout, new_size) };
        if !new.is_null() {
            CURRENT.fetch_sub(layout.size(), Ordering::Relaxed);
            grow(new_size);
        }
        new
    }
}

#[global_allocator]
static ALLOC: Counting = Counting;

/// A file to set up before each call, and the call.
struct Scenario {
    name: String,
    tool: &'static str,
    args: Value,
    /// Files written before every call (the call may change them).
    files: Vec<(String, Vec<u8>)>,
    /// Read these first in the same session, so write may replace them.
    read_first: Vec<String>,
    /// Make the same call once before the measured one, in the same session.
    warm: bool,
    images: bool,
}

impl Scenario {
    fn new(name: &str, tool: &'static str, args: Value) -> Self {
        Self {
            name: name.to_string(),
            tool,
            args,
            files: Vec::new(),
            read_first: Vec::new(),
            warm: false,
            images: false,
        }
    }

    fn file(mut self, path: &str, bytes: Vec<u8>) -> Self {
        self.files.push((path.to_string(), bytes));
        self
    }

    fn read_first(mut self, path: &str) -> Self {
        self.read_first.push(path.to_string());
        self
    }

    fn images(mut self) -> Self {
        self.images = true;
        self
    }

    fn warm(mut self) -> Self {
        self.warm = true;
        self
    }
}

struct Measured {
    median: Duration,
    slowest: Duration,
    runs: usize,
    peak_heap: usize,
    outcome: String,
}

fn ctx(dir: &Path, images: bool) -> ToolCtx {
    ToolCtx {
        call_id: "bench".into(),
        env: Arc::new(LocalEnvironment::new(dir.to_path_buf())),
        files: Arc::new(FileTracker::new()),
        locks: Arc::new(her_tools::files::PathLocks::new()),
        limits: OutputLimits::default(),
        progress: ProgressSink::noop(),
        cancel: CancellationToken::new(),
        model_accepts_images: images,
        output_dir: dir.join(".tool-output"),
        session_state: Default::default(),
            authorities: Default::default(),
    }
}

fn tool(name: &str) -> Box<dyn Tool> {
    match name {
        "read" => Box::new(Typed::new(ReadTool)),
        "write" => Box::new(Typed::new(WriteTool)),
        "edit" => Box::new(Typed::new(EditTool)),
        other => panic!("unknown tool {other}"),
    }
}

/// First line of the result, for the table.
fn outcome(result: &Result<ToolOutput, her_tools::ToolError>) -> String {
    let text = match result {
        Ok(out) => {
            let text: String = out
                .content
                .iter()
                .map(|c| match c {
                    her_llm::UserContent::Text { text } => text.as_str(),
                    _ => "[image]",
                })
                .collect::<Vec<_>>()
                .join(" ");
            let prefix = if out.is_error { "error: " } else { "" };
            format!("{prefix}{} bytes: {}", text.len(), text.lines().next().unwrap_or(""))
        }
        Err(err) => format!("error: {err}"),
    };
    text.chars().take(90).collect()
}

async fn measure(scenario: &Scenario) -> Measured {
    let tool = tool(scenario.tool);
    let budget = Duration::from_secs(2);
    let mut times = Vec::new();
    let mut peak_heap = 0;
    let mut last = String::new();
    let started = Instant::now();
    while times.len() < 3 || (started.elapsed() < budget && times.len() < 50) {
        let dir = tempfile::tempdir().unwrap();
        for (path, bytes) in &scenario.files {
            let path = dir.path().join(path);
            std::fs::create_dir_all(path.parent().unwrap()).unwrap();
            std::fs::write(&path, bytes).unwrap();
        }
        let ctx = ctx(dir.path(), scenario.images);
        for path in &scenario.read_first {
            Typed::new(ReadTool)
                .execute(json!({"path": path, "limit": 1}), ctx.clone())
                .await
                .unwrap();
        }
        if scenario.warm {
            tool.execute(scenario.args.clone(), ctx.clone()).await.unwrap();
        }
        let args = scenario.args.clone();
        let base = CURRENT.load(Ordering::Relaxed);
        PEAK.store(base, Ordering::Relaxed);
        let start = Instant::now();
        let result = tool.execute(args, ctx).await;
        let elapsed = start.elapsed();
        peak_heap = peak_heap.max(PEAK.load(Ordering::Relaxed).saturating_sub(base));
        last = outcome(&result);
        drop(result);
        times.push(elapsed);
        // One slow call is enough to know it is slow.
        if elapsed > budget {
            break;
        }
    }
    times.sort();
    Measured {
        median: times[times.len() / 2],
        slowest: *times.last().unwrap(),
        runs: times.len(),
        peak_heap,
        outcome: last,
    }
}

fn code_line(i: usize) -> String {
    format!("    let value_{i} = compute(\"key {i}\", {i} * 7); // step {i}")
}

/// Plausible source code of about `size` bytes, and its line count.
fn code(size: usize, ending: &str) -> (Vec<u8>, usize) {
    let mut out = String::with_capacity(size + 128);
    let mut lines = 0;
    while out.len() < size {
        lines += 1;
        out.push_str(&code_line(lines));
        out.push_str(ending);
    }
    (out.into_bytes(), lines)
}

fn png(size: usize) -> Vec<u8> {
    let mut out = vec![0x89, b'P', b'N', b'G', 0x0D, 0x0A, 0x1A, 0x0A];
    out.extend_from_slice(&13u32.to_be_bytes());
    out.extend_from_slice(b"IHDR");
    out.extend_from_slice(&1920u32.to_be_bytes());
    out.extend_from_slice(&1080u32.to_be_bytes());
    out.extend_from_slice(&[8, 6, 0, 0, 0]);
    out.extend_from_slice(&[0, 0, 0, 0]);
    let mut x: u32 = 0x1234_5678;
    while out.len() < size {
        x ^= x << 13;
        x ^= x >> 17;
        x ^= x << 5;
        out.push(x as u8);
    }
    out
}

fn scenarios() -> Vec<Scenario> {
    const MB: usize = 1024 * 1024;
    let (small, _) = code(5 * 1024, "\n");
    let (mb10, mb10_lines) = code(10 * MB, "\n");
    let (mb100, mb100_lines) = code(100 * MB, "\n");
    let (crlf10, _) = code(10 * MB, "\r\n");
    let one_line: Vec<u8> = {
        let mut line = "x".repeat(50 * MB);
        line.push('\n');
        line.into_bytes()
    };
    let (mb1, mb1_lines) = code(MB, "\n");
    let (mb5, mb5_lines) = code(5 * MB, "\n");
    let (mb16, mb16_lines) = code(15 * MB, "\n");
    let content_4k = String::from_utf8(code(4 * 1024, "\n").0).unwrap();
    let content_1m = String::from_utf8(mb1.clone()).unwrap();
    let content_16m = String::from_utf8(mb16.clone()).unwrap();
    let line = code_line;
    // The same file with trailing spaces on every line, for a loose match.
    let mb5_trailing: Vec<u8> = String::from_utf8(mb5.clone()).unwrap().replace('\n', "  \n").into_bytes();
    let hundred_edits: Vec<Value> = (1..=100)
        .map(|k| {
            let i = k * (mb1_lines / 101);
            json!({"old_text": line(i), "new_text": format!("{} // changed", line(i))})
        })
        .collect();
    let many_edits: Vec<Value> = (1..=2000)
        .map(|k| {
            let i = k * (mb5_lines / 2001);
            json!({"old_text": line(i), "new_text": format!("{} // changed", line(i))})
        })
        .collect();

    vec![
        Scenario::new("read small file (5 KB)", "read", json!({"path": "small.rs"})).file("small.rs", small.clone()),
        Scenario::new("read 2000 lines, start of 10 MB", "read", json!({"path": "f.log"})).file("f.log", mb10.clone()),
        Scenario::new(
            "read 2000 lines, middle of 10 MB",
            "read",
            json!({"path": "f.log", "offset": mb10_lines / 2}),
        )
        .file("f.log", mb10.clone()),
        Scenario::new(
            "read 2000 lines, end of 10 MB",
            "read",
            json!({"path": "f.log", "offset": mb10_lines - 1999}),
        )
        .file("f.log", mb10),
        Scenario::new("read 2000 lines, start of 100 MB", "read", json!({"path": "f.log"})).file("f.log", mb100.clone()),
        Scenario::new(
            "read 2000 lines, end of 100 MB",
            "read",
            json!({"path": "f.log", "offset": mb100_lines - 1999}),
        )
        .file("f.log", mb100.clone()),
        Scenario::new(
            "read 2000 lines, end of 100 MB, read before",
            "read",
            json!({"path": "f.log", "offset": mb100_lines - 1999}),
        )
        .file("f.log", mb100)
        .warm(),
        Scenario::new("read 2000 lines, start of 10 MB CRLF", "read", json!({"path": "f.log"})).file("f.log", crlf10),
        Scenario::new("read one 50 MB line", "read", json!({"path": "min.js"})).file("min.js", one_line),
        Scenario::new("read 3 MB PNG", "read", json!({"path": "shot.png"}))
            .file("shot.png", png(3 * MB))
            .images(),
        Scenario::new("write new 4 KB", "write", json!({"path": "out/new.rs", "content": content_4k})),
        Scenario::new("write new 1 MB", "write", json!({"path": "out/new.rs", "content": content_1m.clone()})),
        Scenario::new("write new 15 MB", "write", json!({"path": "out/new.rs", "content": content_16m})),
        Scenario::new("overwrite 1 MB (read first)", "write", json!({"path": "f.rs", "content": content_1m}))
            .file("f.rs", mb1.clone())
            .read_first("f.rs"),
        Scenario::new(
            "edit 1 line of small file",
            "edit",
            json!({"path": "small.rs", "edits": [{"old_text": line(30), "new_text": "    // gone"}]}),
        )
        .file("small.rs", small),
        Scenario::new(
            "edit last line of 1 MB",
            "edit",
            json!({"path": "f.rs", "edits": [{"old_text": line(mb1_lines), "new_text": "    // gone"}]}),
        )
        .file("f.rs", mb1.clone()),
        Scenario::new(
            "edit last line of 15 MB",
            "edit",
            json!({"path": "f.rs", "edits": [{"old_text": line(mb16_lines), "new_text": "    // gone"}]}),
        )
        .file("f.rs", mb16.clone()),
        Scenario::new("100 edits in 1 MB", "edit", json!({"path": "f.rs", "edits": hundred_edits})).file("f.rs", mb1),
        Scenario::new("2000 edits in 5 MB", "edit", json!({"path": "f.rs", "edits": many_edits})).file("f.rs", mb5),
        Scenario::new(
            "edit not found in 15 MB",
            "edit",
            json!({"path": "f.rs", "edits": [{"old_text": "    let nothing = here();\n    // at all", "new_text": "x"}]}),
        )
        .file("f.rs", mb16),
        Scenario::new(
            "edit, trailing spaces differ, 5 MB",
            "edit",
            json!({"path": "f.rs", "edits": [{"old_text": format!("{}\n{}", line(mb5_lines / 2), line(mb5_lines / 2 + 1)), "new_text": "    // gone"}]}),
        )
        .file("f.rs", mb5_trailing),
    ]
}

fn kb(bytes: usize) -> String {
    if bytes >= 10 * 1024 * 1024 {
        format!("{:.0} MB", bytes as f64 / 1048576.0)
    } else if bytes >= 1024 * 1024 {
        format!("{:.1} MB", bytes as f64 / 1048576.0)
    } else {
        format!("{:.0} KB", bytes as f64 / 1024.0)
    }
}

fn ms(d: Duration) -> String {
    let ms = d.as_secs_f64() * 1000.0;
    if ms < 10.0 { format!("{ms:.2} ms") } else { format!("{ms:.0} ms") }
}

fn main() {
    let filter: Option<String> = std::env::args().skip(1).find(|a| !a.starts_with('-'));
    let runtime = tokio::runtime::Builder::new_multi_thread().worker_threads(2).enable_all().build().unwrap();
    println!("| scenario | median | slowest | runs | peak heap | result |");
    println!("|---|---|---|---|---|---|");
    for scenario in scenarios() {
        if filter.as_ref().is_some_and(|f| !scenario.name.contains(f.as_str())) {
            continue;
        }
        let m = runtime.block_on(measure(&scenario));
        println!(
            "| {} | {} | {} | {} | {} | {} |",
            scenario.name,
            ms(m.median),
            ms(m.slowest),
            m.runs,
            kb(m.peak_heap),
            m.outcome.replace('|', "\\|")
        );
    }
}
