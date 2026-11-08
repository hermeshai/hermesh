//! Shared by the corpus tests: sessions that drive the real tools, a generator of complex files
//! (`corpus`) and simple reference models of what read, write and edit do to them (`model`).

#![allow(dead_code)]

pub mod corpus;
pub mod model;

use her_llm::UserContent;
use her_tools::FileStamp;
use her_tools::FileTracker;
use her_tools::LocalEnvironment;
use her_tools::OutputLimits;
use her_tools::ProgressSink;
use her_tools::Tool;
use her_tools::ToolCtx;
use her_tools::ToolError;
use her_tools::ToolOutput;
use her_tools::Typed;
use her_tools::builtin::EditTool;
use her_tools::builtin::ReadTool;
use her_tools::builtin::WriteTool;
use serde_json::Value;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use tempfile::TempDir;
use tokio_util::sync::CancellationToken;

/// The seed of a test: `base` unless `HER_CORPUS_SEED` gives another, to explore.
pub fn seed(base: u64) -> u64 {
    std::env::var("HER_CORPUS_SEED")
        .ok()
        .and_then(|s| s.trim().parse::<u64>().ok())
        .map_or(base, |s| s ^ base)
}

/// A working directory and a session of the tools in it: one environment (with its line
/// indexes) and one file tracker, as a kernel session has.
#[derive(Clone)]
pub struct Session {
    dir: Arc<TempDir>,
    pub ctx: ToolCtx,
}

impl Default for Session {
    fn default() -> Self {
        Self::new()
    }
}

impl Session {
    pub fn new() -> Self {
        let dir = Arc::new(tempfile::tempdir().expect("temporary directory"));
        let ctx = ctx(dir.path());
        Self { dir, ctx }
    }

    /// Another session in the same directory: its own environment and tracker.
    pub fn other(&self) -> Self {
        Self {
            dir: self.dir.clone(),
            ctx: ctx(self.dir.path()),
        }
    }

    /// The same session with a fresh environment: no line index of any file.
    pub fn fresh_env(&self) -> Self {
        let mut ctx = self.ctx.clone();
        ctx.env = Arc::new(LocalEnvironment::new(self.dir.path().to_path_buf()));
        Self {
            dir: self.dir.clone(),
            ctx,
        }
    }

    pub fn root(&self) -> &Path {
        self.dir.path()
    }

    pub fn path(&self, name: &str) -> PathBuf {
        self.dir.path().join(name)
    }

    /// How the tools name `name` in their results.
    pub fn display(&self, name: &str) -> String {
        self.path(name).display().to_string()
    }

    /// Writes a file directly, as the user or another program would.
    pub fn put(&self, name: &str, bytes: &[u8]) -> PathBuf {
        let path = self.path(name);
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent).unwrap();
        }
        std::fs::write(&path, bytes).unwrap();
        path
    }

    pub fn bytes(&self, name: &str) -> Vec<u8> {
        std::fs::read(self.path(name)).unwrap()
    }

    pub async fn read(&self, args: Value) -> Result<ToolOutput, ToolError> {
        call(&Typed::new(ReadTool), args, &self.ctx).await
    }

    pub async fn write(&self, args: Value) -> Result<ToolOutput, ToolError> {
        call(&Typed::new(WriteTool), args, &self.ctx).await
    }

    pub async fn edit(&self, args: Value) -> Result<ToolOutput, ToolError> {
        call(&Typed::new(EditTool), args, &self.ctx).await
    }

    /// read, expected to succeed with text: the text.
    pub async fn read_text(&self, args: Value) -> String {
        let shown = args.to_string();
        match self.read(args).await {
            Ok(out) => text(&out).to_string(),
            Err(err) => panic!("read {shown}: {err}"),
        }
    }

    /// Records `name` as read in this session, as read would, without depending on how read
    /// shows it.
    pub fn mark_read(&self, name: &str) {
        let path = self.path(name);
        self.ctx.files.record(&path, stamp(&path));
    }
}

fn ctx(dir: &Path) -> ToolCtx {
    ToolCtx {
        call_id: "corpus".into(),
        env: Arc::new(LocalEnvironment::new(dir.to_path_buf())),
        files: Arc::new(FileTracker::new()),
        locks: Arc::new(her_tools::files::PathLocks::new()),
        limits: OutputLimits::default(),
        progress: ProgressSink::noop(),
        cancel: CancellationToken::new(),
        model_accepts_images: false,
        output_dir: dir.join(".her-output"),
        session_state: Default::default(),
            authorities: Default::default(),
    }
}

/// A call as the kernel makes it: arguments repaired, then a mutating tool checked before it
/// runs. A refusal by `check` must be the refusal of the run, and `check` must leave the file
/// alone.
pub async fn call(tool: &dyn Tool, args: Value, ctx: &ToolCtx) -> Result<ToolOutput, ToolError> {
    let args = tool.prepare_arguments(args);
    if tool.kind() == her_tools::ToolKind::Mutating {
        let target = args.get("path").and_then(Value::as_str).map(|p| ctx.env.resolve(p));
        let before = target.as_deref().map(state);
        let checked = tool.check(&args, ctx).await;
        if let Some(target) = &target {
            assert_eq!(Some(state(target)), before, "check changed {}", target.display());
        }
        let result = tool.execute(args.clone(), ctx.clone()).await;
        if let Err(refused) = &checked {
            match &result {
                Ok(out) => panic!("check refused {args} ({refused}) but the run succeeded: {}", text(out)),
                Err(err) => assert_eq!(refused.to_string(), err.to_string(), "check and run of {args}"),
            }
        }
        return result;
    }
    tool.execute(args, ctx.clone()).await
}

/// What a file is: its bytes and stamp, `None` when it does not exist.
fn state(path: &Path) -> Option<(Vec<u8>, FileStamp)> {
    let meta = std::fs::metadata(path).ok()?;
    if !meta.is_file() {
        return None;
    }
    Some((std::fs::read(path).ok()?, FileStamp::of(&meta)))
}

/// The stamp of a file, as the environment computes it.
pub fn stamp(path: &Path) -> FileStamp {
    FileStamp::of(&std::fs::metadata(path).unwrap())
}

/// The text of a result that holds only text.
pub fn text(out: &ToolOutput) -> &str {
    match out.content.as_slice() {
        [UserContent::Text { text }] => text,
        other => panic!("expected one text part, got {} parts", other.len()),
    }
}

/// The error text of a call that must fail.
pub fn error(result: Result<ToolOutput, ToolError>) -> String {
    match result {
        Ok(out) => panic!("expected an error, got: {}", excerpt(text(&out))),
        Err(err) => err.to_string(),
    }
}

/// At most the first 400 bytes of `text`, for messages.
pub fn excerpt(text: &str) -> String {
    let mut end = text.len().min(400);
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    if end < text.len() { format!("{:?}…", &text[..end]) } else { format!("{text:?}") }
}

/// Asserts that `got` equals `expected`; on a difference, shows where it starts instead of
/// both texts, which can be megabytes.
#[track_caller]
pub fn assert_same(got: &[u8], expected: &[u8], context: &str) {
    if got == expected {
        return;
    }
    let at = got.iter().zip(expected).take_while(|(a, b)| a == b).count();
    let line = expected[..at].iter().filter(|b| **b == b'\n').count() + 1;
    let from = at.saturating_sub(80);
    let show = |bytes: &[u8]| String::from_utf8_lossy(&bytes[from.min(bytes.len())..(at + 80).min(bytes.len())]).into_owned();
    panic!(
        "{context}: differs at byte {at} (line {line}); lengths {} and {} (got, expected)\n  got:      {:?}\n  expected: {:?}",
        got.len(),
        expected.len(),
        show(got),
        show(expected),
    );
}

/// Temporary files a failed or interrupted write could leave in `dir`.
pub fn leftovers(dir: &Path) -> Vec<String> {
    let mut found = Vec::new();
    let mut pending = vec![dir.to_path_buf()];
    while let Some(next) = pending.pop() {
        let Ok(entries) = std::fs::read_dir(&next) else { continue };
        for entry in entries.flatten() {
            let path = entry.path();
            if entry.file_type().is_ok_and(|t| t.is_dir()) {
                pending.push(path);
            } else if entry.file_name().to_string_lossy().contains(".her-tmp-") {
                found.push(path.display().to_string());
            }
        }
    }
    found
}

/// Every path under `dir` with the bytes of its files (a link as its target): equal snapshots
/// mean nothing changed.
pub fn snapshot(dir: &Path) -> Vec<(PathBuf, Option<Vec<u8>>)> {
    let mut out = Vec::new();
    let mut pending = vec![dir.to_path_buf()];
    while let Some(next) = pending.pop() {
        for entry in std::fs::read_dir(&next).unwrap() {
            let path = entry.unwrap().path();
            let meta = std::fs::symlink_metadata(&path).unwrap();
            if meta.file_type().is_symlink() {
                out.push((path.clone(), Some(std::fs::read_link(&path).unwrap().into_os_string().into_encoded_bytes())));
            } else if meta.is_dir() {
                pending.push(path.clone());
                out.push((path, None));
            } else if meta.is_file() {
                let bytes = std::fs::read(&path).unwrap();
                out.push((path, Some(bytes)));
            } else {
                out.push((path, None));
            }
        }
    }
    out.sort();
    out
}

/// The `Use offset=N` of a read result, if it has one. Another note may follow it.
pub fn next_offset(text: &str) -> Option<u64> {
    let note = &text[text.rfind("\n\n[Showing lines ")?..];
    let at = note.find(". Use offset=")? + ". Use offset=".len();
    let end = at + note[at..].find(" to continue.]")?;
    note[at..end].parse().ok()
}

/// Whether this process runs as root, which may write anything.
pub fn is_root() -> bool {
    unsafe { libc::geteuid() == 0 }
}

/// Writes `bytes` at `at` without truncating the file, then puts its modification time back,
/// as a program that keeps times would.
pub fn patch_in_place(path: &Path, at: u64, bytes: &[u8]) {
    use std::io::Seek;
    use std::io::Write;
    let mtime = std::fs::metadata(path).unwrap().modified().unwrap();
    let mut file = std::fs::OpenOptions::new().write(true).open(path).unwrap();
    file.seek(std::io::SeekFrom::Start(at)).unwrap();
    file.write_all(bytes).unwrap();
    file.set_modified(mtime).unwrap();
}
