use async_trait::async_trait;
use schemars::JsonSchema;
use serde::Deserialize;
use serde_json::Value;
use serde_json::json;
use std::io;
use std::path::Path;

use crate::env::FileMeta;
use crate::env::NotRegularFile;
use crate::env::TextEncoding;
use crate::text;
use crate::files::StaleReason;
use crate::tool::ToolCtx;
use crate::tool::ToolError;
use crate::tool::ToolKind;
use crate::tool::ToolOutput;
use crate::typed::TypedTool;

/// How much of a replaced file decides whether its byte order mark and line endings are kept.
const FORMAT_PROBE_BYTES: u64 = 64 * 1024;
const BOM: &str = "\u{feff}";

pub struct WriteTool;

#[derive(Debug, Deserialize, JsonSchema)]
pub struct WriteArgs {
    /// Path to the file, relative to the working directory or absolute.
    pub path: String,
    /// The complete file content.
    pub content: String,
}

#[async_trait]
impl TypedTool for WriteTool {
    type Args = WriteArgs;
    const NAME: &'static str = "write";
    const DESCRIPTION: &'static str = "Create a file, or replace an existing file completely. Parent directories \
        are created as needed. An existing file must be read in this session before it is overwritten.";

    fn kind(&self) -> ToolKind {
        ToolKind::Mutating
    }

    fn guidelines(&self) -> &[&'static str] {
        &["Use write to create a file or to replace one completely; use edit for partial changes."]
    }

    fn describe(&self, args: &serde_json::Value, env: &dyn crate::env::Environment) -> crate::tool::ToolDescription {
        crate::tool::ToolDescription::for_path(crate::tool::ToolCategory::Edit, "write", args, env, None)
    }

    fn prepare_arguments(&self, args: Value) -> Value {
        repair_arguments(args)
    }

    async fn check(&self, args: &WriteArgs, ctx: &ToolCtx) -> Result<(), ToolError> {
        validate(&ctx.env.resolve(&args.path), ctx).await.map(|_| ())
    }

    async fn run(&self, args: WriteArgs, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        let path = ctx.env.resolve(&args.path);
        let display = path.display().to_string();
        // One change to a file at a time, also across sessions that share it (sub-agents).
        let _lock = ctx.locks.lock(&path).await;
        let format = match validate(&path, &ctx).await? {
            Some(meta) => existing_format(&path, &meta, &ctx).await?,
            None => Format::default(),
        };
        let (bytes, kept) = format.apply(args.content);
        let outcome = ctx
            .env
            .write_file(&path, bytes)
            .await
            .map_err(|err| write_error(&display, &err))?;
        ctx.files.record(&path, outcome.stamp);
        let action = if outcome.created { "created" } else { "overwritten" };
        let kept = if kept.is_empty() { String::new() } else { format!(", {} kept", kept.join(" and ")) };
        let text = format!("Wrote {} bytes to {display} ({action}{kept}).", outcome.bytes);
        Ok(ToolOutput::text(text).with_details(json!({
            "path": display,
            "created": outcome.created,
            "bytes": outcome.bytes,
        })))
    }
}

/// Repairs shapes some models send: `file_path` for `path`, `contents` for `content`, and a
/// JSON file as a JSON value instead of its text.
fn repair_arguments(mut args: Value) -> Value {
    if let Some(map) = args.as_object_mut() {
        for (alias, name) in [("file_path", "path"), ("contents", "content")] {
            if !map.contains_key(name)
                && let Some(value) = map.remove(alias)
            {
                map.insert(name.to_string(), value);
            }
        }
        if let Some(content) = map.get_mut("content")
            && (content.is_object() || content.is_array())
            && let Ok(text) = serde_json::to_string_pretty(content)
        {
            *content = Value::String(text + "\n");
        }
    }
    args
}

/// Everything about a write that can be checked without changing anything, shared by `check`
/// and `run`. Returns the file being replaced, or `None` when the write creates one.
async fn validate(path: &Path, ctx: &ToolCtx) -> Result<Option<FileMeta>, ToolError> {
    let existing = validate_file(path, ctx).await?;
    ctx.env
        .check_write(path)
        .await
        .map_err(|err| write_error(&path.display().to_string(), &err))?;
    Ok(existing)
}

async fn validate_file(path: &Path, ctx: &ToolCtx) -> Result<Option<FileMeta>, ToolError> {
    let display = path.display();
    match ctx.env.metadata(path).await {
        Ok(meta) if meta.is_dir => Err(ToolError::Failed(format!("{display} is a directory."))),
        Ok(meta) if !meta.is_file => Err(ToolError::Failed(format!(
            "{display} is not a regular file; it cannot be written."
        ))),
        Ok(meta) if meta.readonly => Err(ToolError::Failed(format!("{display} is read-only."))),
        Ok(meta) => match ctx.files.verify(path, meta.stamp) {
            Ok(()) => Ok(Some(meta)),
            Err(StaleReason::NotRead) => Err(ToolError::Failed(format!(
                "{display} already exists and has not been read in this session. Read it first, then write it."
            ))),
            Err(StaleReason::Changed) => Err(changed(path)),
        },
        Err(err) if matches!(err.kind(), io::ErrorKind::NotFound | io::ErrorKind::NotADirectory) => {
            check_parents(path, ctx).await?;
            Ok(None)
        }
        Err(err) => Err(ToolError::Failed(format!("Cannot access {display}: {err}"))),
    }
}

/// The nearest existing ancestor of a new file must be a directory, or the missing directories
/// cannot be created.
async fn check_parents(path: &Path, ctx: &ToolCtx) -> Result<(), ToolError> {
    for dir in path.ancestors().skip(1) {
        match ctx.env.metadata(dir).await {
            Ok(meta) if meta.is_dir => return Ok(()),
            Ok(_) => {
                return Err(ToolError::Failed(format!(
                    "Cannot create {}: {} is not a directory.",
                    path.display(),
                    dir.display()
                )));
            }
            Err(err) if matches!(err.kind(), io::ErrorKind::NotFound | io::ErrorKind::NotADirectory) => {}
            Err(err) => return Err(ToolError::Failed(format!("Cannot access {}: {err}", dir.display()))),
        }
    }
    Ok(())
}

fn changed(path: &Path) -> ToolError {
    ToolError::Failed(format!(
        "{} changed since it was last read, possibly by another agent or the user. Read it again, then write it.",
        path.display()
    ))
}

fn write_error(display: &str, err: &io::Error) -> ToolError {
    if let Some(special) = NotRegularFile::of(err) {
        return ToolError::Failed(format!(
            "{display} is not a regular file ({}); it cannot be written.",
            special.kind
        ));
    }
    ToolError::Failed(match err.kind() {
        io::ErrorKind::ReadOnlyFilesystem => format!("{display} is read-only."),
        io::ErrorKind::IsADirectory => format!("{display} is a directory."),
        _ => format!("Cannot write {display}: {err}"),
    })
}

/// The format of the file being replaced, from its beginning.
async fn existing_format(path: &Path, meta: &FileMeta, ctx: &ToolCtx) -> Result<Format, ToolError> {
    match ctx.env.read_file(path, FORMAT_PROBE_BYTES).await {
        // Not the version just checked: it changed in between.
        Ok(head) if head.stamp != meta.stamp => Err(changed(path)),
        Ok(head) => Ok(Format::detect(&head.bytes, head.truncated)),
        // Unreadable: the content is written as given.
        Err(_) => Ok(Format::default()),
    }
}

/// What a replaced text file keeps when the new content lacks it: a UTF-16 encoding, a UTF-8
/// byte order mark and CRLF line endings. Writing LF content over a CRLF file would otherwise
/// change every line, and UTF-8 over a UTF-16 file every byte.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
struct Format {
    bom: bool,
    crlf: bool,
    /// The file is UTF-16 after a byte order mark.
    utf16: Option<TextEncoding>,
}

impl Format {
    /// From the beginning of a file, `truncated` when it goes on. Other encodings are left
    /// alone; line endings are CRLF when there is at least one CRLF and no bare LF.
    fn detect(head: &[u8], truncated: bool) -> Self {
        if let Some(encoding) = text::utf16_bom(head) {
            let decoded = text::decode_utf16(&head[2..], encoding);
            return Self {
                bom: false,
                crlf: crlf_only(decoded.as_bytes()),
                utf16: Some(encoding),
            };
        }
        match std::str::from_utf8(head) {
            Ok(_) => {}
            // The cut may split the last character.
            Err(err) if truncated && err.error_len().is_none() => {}
            Err(_) => return Self::default(),
        }
        Self {
            bom: head.starts_with(BOM.as_bytes()),
            crlf: crlf_only(head),
            utf16: None,
        }
    }

    /// The bytes to write and what was kept. Content with CR of its own is never converted.
    fn apply(self, mut content: String) -> (Vec<u8>, Vec<&'static str>) {
        let mut kept = Vec::new();
        if let Some(encoding) = self.utf16 {
            // UTF-16 is written with its own byte order mark.
            if content.starts_with(BOM) {
                content.drain(..BOM.len());
            }
            kept.push(if encoding == TextEncoding::Utf16Be { "UTF-16BE encoding" } else { "UTF-16LE encoding" });
        } else if self.bom && !content.starts_with(BOM) {
            content.insert_str(0, BOM);
            kept.push("byte order mark");
        }
        if self.crlf && content.contains('\n') && !content.contains('\r') {
            content = content.replace('\n', "\r\n");
            kept.push("CRLF line endings");
        }
        (text::encode(content, self.utf16.unwrap_or(TextEncoding::Utf8)), kept)
    }
}

/// Line breaks are CRLF: at least one, and no bare LF.
fn crlf_only(text: &[u8]) -> bool {
    let bare_lf = text.first() == Some(&b'\n') || text.windows(2).any(|w| w[1] == b'\n' && w[0] != b'\r');
    !bare_lf && text.windows(2).any(|w| w == b"\r\n")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::builtin::read::ReadTool;
    use crate::builtin::test_support::run;
    use crate::builtin::test_support::test_ctx;
    use crate::files::FileStamp;
    use crate::tool::Tool;
    use crate::typed::Typed;
    use std::os::unix::fs::PermissionsExt;
    use std::path::PathBuf;

    async fn check(args: Value, ctx: &ToolCtx) -> Result<(), ToolError> {
        Typed::new(WriteTool).check(&args, ctx).await
    }

    /// Records the file as read, as `read` would, without depending on how `read` shows it.
    fn mark_read(ctx: &ToolCtx, path: &Path) {
        ctx.files.record(path, FileStamp::of(&std::fs::metadata(path).unwrap()));
    }

    /// Every path under `dir`, with the bytes of files: equal snapshots mean nothing changed.
    fn snapshot(dir: &Path) -> Vec<(PathBuf, Option<Vec<u8>>)> {
        let mut out = Vec::new();
        let mut pending = vec![dir.to_path_buf()];
        while let Some(next) = pending.pop() {
            for entry in std::fs::read_dir(&next).unwrap() {
                let path = entry.unwrap().path();
                if path.is_dir() {
                    pending.push(path.clone());
                    out.push((path, None));
                } else {
                    let bytes = std::fs::read(&path).unwrap();
                    out.push((path, Some(bytes)));
                }
            }
        }
        out.sort();
        out
    }

    fn is_root() -> bool {
        unsafe { libc::geteuid() == 0 }
    }

    #[tokio::test]
    async fn creates_files_and_protects_unread_ones() {
        let dir = tempfile::tempdir().unwrap();
        let ctx = test_ctx(dir.path());
        let out = run(&WriteTool, json!({"path": "src/new.txt", "content": "hello"}), ctx.clone())
            .await
            .unwrap();
        assert_eq!(out.content_text(), format!("Wrote 5 bytes to {}/src/new.txt (created).", dir.path().display()));
        // Its own file can be overwritten without reading it first.
        run(&WriteTool, json!({"path": "src/new.txt", "content": "again"}), ctx.clone())
            .await
            .unwrap();

        std::fs::write(dir.path().join("existing.txt"), "user data").unwrap();
        let err = run(&WriteTool, json!({"path": "existing.txt", "content": "x"}), ctx.clone())
            .await
            .unwrap_err();
        assert!(err.to_string().contains("has not been read"));
        run(&ReadTool, json!({"path": "existing.txt"}), ctx.clone()).await.unwrap();
        run(&WriteTool, json!({"path": "existing.txt", "content": "x"}), ctx.clone())
            .await
            .unwrap();
        assert_eq!(std::fs::read_to_string(dir.path().join("existing.txt")).unwrap(), "x");
    }

    #[tokio::test]
    async fn detects_external_changes() {
        let dir = tempfile::tempdir().unwrap();
        let ctx = test_ctx(dir.path());
        let path = dir.path().join("f.txt");
        std::fs::write(&path, "v1").unwrap();
        run(&ReadTool, json!({"path": "f.txt"}), ctx.clone()).await.unwrap();
        std::fs::write(&path, "version two").unwrap();
        let err = run(&WriteTool, json!({"path": "f.txt", "content": "mine"}), ctx).await.unwrap_err();
        assert!(err.to_string().contains("changed since it was last read"));
    }

    #[tokio::test]
    async fn check_refuses_what_run_would_refuse_without_touching_the_disk() {
        let dir = tempfile::tempdir().unwrap();
        let ctx = test_ctx(dir.path());
        std::fs::create_dir(dir.path().join("sub")).unwrap();
        std::fs::write(dir.path().join("unread.txt"), "user data").unwrap();
        let seen = dir.path().join("seen.txt");
        std::fs::write(&seen, "v1").unwrap();
        mark_read(&ctx, &seen);
        std::fs::write(&seen, "version two").unwrap();
        let before = snapshot(dir.path());

        let root = dir.path().display();
        let cases = [
            ("sub", format!("{root}/sub is a directory.")),
            (
                "unread.txt/new/x.md",
                format!("Cannot create {root}/unread.txt/new/x.md: {root}/unread.txt is not a directory."),
            ),
            (
                "unread.txt",
                format!("{root}/unread.txt already exists and has not been read in this session."),
            ),
            (
                "seen.txt",
                format!("{root}/seen.txt changed since it was last read, possibly by another agent or the user."),
            ),
        ];
        for (path, expected) in cases {
            let args = json!({"path": path, "content": "x"});
            let err = check(args.clone(), &ctx).await.unwrap_err().to_string();
            assert!(err.starts_with(&expected), "check {path}: {err}");
            let err = run(&WriteTool, args, ctx.clone()).await.unwrap_err().to_string();
            assert!(err.starts_with(&expected), "run {path}: {err}");
        }
        // A new file in new directories passes; checking it creates nothing.
        check(json!({"path": "new/dir/x.md", "content": "x"}), &ctx).await.unwrap();
        assert_eq!(snapshot(dir.path()), before);
    }

    #[tokio::test]
    async fn refuses_read_only_files() {
        if is_root() {
            return;
        }
        let dir = tempfile::tempdir().unwrap();
        let ctx = test_ctx(dir.path());
        let path = dir.path().join("locked.txt");
        std::fs::write(&path, "keep me").unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o444)).unwrap();
        mark_read(&ctx, &path);
        let before = snapshot(dir.path());
        let args = json!({"path": "locked.txt", "content": "x"});
        let err = check(args.clone(), &ctx).await.unwrap_err();
        assert_eq!(err.to_string(), format!("{} is read-only.", path.display()));
        let err = run(&WriteTool, args, ctx).await.unwrap_err();
        assert_eq!(err.to_string(), format!("{} is read-only.", path.display()));
        assert_eq!(snapshot(dir.path()), before, "file kept and no temporary file left");
    }

    #[tokio::test]
    async fn never_replaces_a_named_pipe() {
        use std::os::unix::ffi::OsStrExt;
        use std::os::unix::fs::FileTypeExt;
        let dir = tempfile::tempdir().unwrap();
        let ctx = test_ctx(dir.path());
        let fifo = dir.path().join("pipe");
        let name = std::ffi::CString::new(fifo.as_os_str().as_bytes()).unwrap();
        assert_eq!(unsafe { libc::mkfifo(name.as_ptr(), 0o644) }, 0);
        let expected = format!("{} is not a regular file", fifo.display());
        let args = json!({"path": "pipe", "content": "x"});
        assert!(check(args.clone(), &ctx).await.unwrap_err().to_string().starts_with(&expected));
        assert!(run(&WriteTool, args, ctx.clone()).await.unwrap_err().to_string().starts_with(&expected));
        // Below the tool, too: the environment refuses to rename over it.
        let err = ctx.env.write_file(&fifo, b"x".to_vec()).await.unwrap_err();
        assert_eq!(NotRegularFile::of(&err).unwrap().kind, "named pipe");
        assert!(std::fs::symlink_metadata(&fifo).unwrap().file_type().is_fifo());
    }

    #[tokio::test]
    async fn writes_through_symlinks() {
        let dir = tempfile::tempdir().unwrap();
        let ctx = test_ctx(dir.path());
        let link = dir.path().join("AGENTS.md");
        let target = dir.path().join("docs/guide.md");
        std::fs::create_dir(dir.path().join("docs")).unwrap();
        std::fs::write(&target, "old").unwrap();
        std::os::unix::fs::symlink("docs/guide.md", &link).unwrap();

        // The link's target is what must have been read.
        let err = run(&WriteTool, json!({"path": "AGENTS.md", "content": "new"}), ctx.clone())
            .await
            .unwrap_err();
        assert!(err.to_string().contains("has not been read"));
        run(&ReadTool, json!({"path": "AGENTS.md"}), ctx.clone()).await.unwrap();
        let out = run(&WriteTool, json!({"path": "AGENTS.md", "content": "new"}), ctx.clone())
            .await
            .unwrap();
        assert_eq!(out.content_text(), format!("Wrote 3 bytes to {} (overwritten).", link.display()));
        assert!(std::fs::symlink_metadata(&link).unwrap().file_type().is_symlink());
        assert_eq!(std::fs::read_to_string(&target).unwrap(), "new");
        // The write recorded the version it made, under either name.
        run(&WriteTool, json!({"path": "docs/guide.md", "content": "newer"}), ctx.clone())
            .await
            .unwrap();
        run(&WriteTool, json!({"path": "AGENTS.md", "content": "newest"}), ctx.clone())
            .await
            .unwrap();
        assert_eq!(std::fs::read_to_string(&link).unwrap(), "newest");

        // A dangling link: its target is created, as a shell redirect would.
        std::os::unix::fs::symlink("docs/notes.md", dir.path().join("NOTES.md")).unwrap();
        let out = run(&WriteTool, json!({"path": "NOTES.md", "content": "hi"}), ctx).await.unwrap();
        assert!(out.content_text().ends_with("(created)."));
        assert!(std::fs::symlink_metadata(dir.path().join("NOTES.md")).unwrap().file_type().is_symlink());
        assert_eq!(std::fs::read_to_string(dir.path().join("docs/notes.md")).unwrap(), "hi");
    }

    /// Overwrites `f.txt` holding `old` with `new`; returns the result text and the file.
    async fn overwrite(old: &[u8], new: &str) -> (String, Vec<u8>) {
        let dir = tempfile::tempdir().unwrap();
        let ctx = test_ctx(dir.path());
        let path = dir.path().join("f.txt");
        std::fs::write(&path, old).unwrap();
        mark_read(&ctx, &path);
        let out = run(&WriteTool, json!({"path": "f.txt", "content": new}), ctx).await.unwrap();
        let text = out.content_text().replace(&format!("{}/", dir.path().display()), "");
        (text, std::fs::read(&path).unwrap())
    }

    #[tokio::test]
    async fn keeps_the_byte_order_mark_and_crlf_line_endings() {
        let (text, file) = overwrite("\u{feff}one\r\ntwo\r\n".as_bytes(), "x\ny\n").await;
        assert_eq!(file, "\u{feff}x\r\ny\r\n".as_bytes());
        assert_eq!(text, "Wrote 9 bytes to f.txt (overwritten, byte order mark and CRLF line endings kept).");
        let (text, file) = overwrite(b"a\r\nb\r\n", "a\nb\nc").await;
        assert_eq!(file, b"a\r\nb\r\nc");
        assert_eq!(text, "Wrote 7 bytes to f.txt (overwritten, CRLF line endings kept).");
        let (text, file) = overwrite("\u{feff}a\n".as_bytes(), "b\n").await;
        assert_eq!(file, "\u{feff}b\n".as_bytes());
        assert_eq!(text, "Wrote 5 bytes to f.txt (overwritten, byte order mark kept).");
        // The new content already has them.
        let (text, file) = overwrite("\u{feff}a\r\n".as_bytes(), "\u{feff}b\r\n").await;
        assert_eq!(file, "\u{feff}b\r\n".as_bytes());
        assert_eq!(text, "Wrote 6 bytes to f.txt (overwritten).");
    }

    #[tokio::test]
    async fn leaves_mixed_and_other_encodings_alone() {
        let cases: [(&[u8], &str); 5] = [
            // Mixed line endings in the file, or in the new content.
            (b"a\r\nb\nc\r\n", "x\ny\n"),
            (b"a\r\nb\r\n", "x\r\ny\n"),
            // A lone CR in the new content.
            (b"a\r\nb\r\n", "x\ry\n"),
            // Not UTF-8: Latin-1, and a byte order mark followed by invalid bytes.
            (b"caf\xe9\r\nbar\r\n", "x\ny\n"),
            (b"\xef\xbb\xbf\xff\r\n", "x\ny\n"),
        ];
        for (old, new) in cases {
            let (text, file) = overwrite(old, new).await;
            assert_eq!(file, new.as_bytes(), "{old:?}");
            assert_eq!(text, format!("Wrote {} bytes to f.txt (overwritten).", new.len()));
        }
        // A new file is written as given.
        let dir = tempfile::tempdir().unwrap();
        run(&WriteTool, json!({"path": "new.txt", "content": "a\nb\n"}), test_ctx(dir.path()))
            .await
            .unwrap();
        assert_eq!(std::fs::read(dir.path().join("new.txt")).unwrap(), b"a\nb\n");
    }

    #[tokio::test]
    async fn line_endings_are_decided_from_the_first_64_kib() {
        // CRLF up to the limit, a character split by it, bare LF only beyond it.
        let mut old = "line\r\n".repeat(12_000).into_bytes();
        old.truncate(FORMAT_PROBE_BYTES as usize - 1);
        old.extend_from_slice("é and more\nbare\n".as_bytes());
        let (text, file) = overwrite(&old, "x\ny\n").await;
        assert_eq!(file, b"x\r\ny\r\n");
        assert!(text.ends_with("(overwritten, CRLF line endings kept)."), "{text}");
    }

    #[tokio::test]
    async fn keeps_a_utf16_encoding() {
        let le = |text: &str| -> Vec<u8> {
            [0xFF, 0xFE].into_iter().chain(text.encode_utf16().flat_map(u16::to_le_bytes)).collect()
        };
        let be = |text: &str| -> Vec<u8> {
            [0xFE, 0xFF].into_iter().chain(text.encode_utf16().flat_map(u16::to_be_bytes)).collect()
        };
        let (text, file) = overwrite(&le("\"key\" = \"Wert\";\r\n"), "\"key\" = \"Grüße\";\n").await;
        assert_eq!(file, le("\"key\" = \"Grüße\";\r\n"));
        assert!(text.ends_with("(overwritten, UTF-16LE encoding and CRLF line endings kept)."), "{text}");
        // A byte order mark in the content is not doubled.
        let (_, file) = overwrite(&be("old\n"), "\u{feff}new\n").await;
        assert_eq!(file, be("new\n"));
    }

    #[test]
    fn detects_the_format() {
        let detect = |bytes: &[u8]| Format::detect(bytes, false);
        let crlf = Format {
            crlf: true,
            ..Format::default()
        };
        assert_eq!(detect(b"a\r\nb\r\n"), crlf);
        assert_eq!(detect(b"a\r\nb\n"), Format::default());
        assert_eq!(detect(b"\na\r\n"), Format::default());
        assert_eq!(detect(b"a\rb\r\n"), crlf);
        assert_eq!(detect(b"no line break"), Format::default());
        let bom = Format {
            bom: true,
            ..Format::default()
        };
        assert_eq!(detect("\u{feff}x".as_bytes()), bom);
        // UTF-16 after its byte order mark, with the line endings of the decoded text.
        let utf16 = |text: &str| -> Vec<u8> {
            [0xFF, 0xFE].into_iter().chain(text.encode_utf16().flat_map(u16::to_le_bytes)).collect()
        };
        let utf16_crlf = Format {
            crlf: true,
            utf16: Some(TextEncoding::Utf16Le),
            ..Format::default()
        };
        assert_eq!(detect(&utf16("a\r\nb\r\n")), utf16_crlf);
        assert_eq!(detect(&utf16("a\nb")).utf16, Some(TextEncoding::Utf16Le));
        assert_eq!(detect(b"\xe9\r\n"), Format::default());
        // A character cut at the end counts as text only when the file goes on.
        let cut = &"a\r\né".as_bytes()[..4];
        assert_eq!(Format::detect(cut, true), crlf);
        assert_eq!(Format::detect(cut, false), Format::default());
    }

    #[test]
    fn repairs_common_argument_mistakes() {
        let prepare = |args: Value| Typed::new(WriteTool).prepare_arguments(args);
        assert_eq!(
            prepare(json!({"file_path": "a.txt", "contents": "x"})),
            json!({"path": "a.txt", "content": "x"})
        );
        // The documented names win.
        let both = json!({"path": "a", "file_path": "b", "content": "x", "contents": "y"});
        assert_eq!(prepare(both.clone()), both);
        // A JSON file sent as a JSON value.
        assert_eq!(
            prepare(json!({"path": "a.json", "content": {"name": "x", "tags": [1, 2]}})),
            json!({"path": "a.json", "content": "{\n  \"name\": \"x\",\n  \"tags\": [\n    1,\n    2\n  ]\n}\n"})
        );
        assert_eq!(
            prepare(json!({"file_path": "a.json", "contents": [true]})),
            json!({"path": "a.json", "content": "[\n  true\n]\n"})
        );
        // Anything else is left alone.
        for args in [
            json!(null),
            json!("text"),
            json!([{"path": "a"}]),
            json!({}),
            json!({"path": "a", "content": 5}),
            json!({"path": "a", "content": null}),
        ] {
            assert_eq!(prepare(args.clone()), args);
        }
    }

    #[tokio::test]
    async fn writes_repaired_json_content() {
        let dir = tempfile::tempdir().unwrap();
        let args = WriteTool.prepare_arguments(json!({"file_path": "p.json", "content": {"a": 1}}));
        run(&WriteTool, args, test_ctx(dir.path())).await.unwrap();
        assert_eq!(std::fs::read_to_string(dir.path().join("p.json")).unwrap(), "{\n  \"a\": 1\n}\n");
    }
}
