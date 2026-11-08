use async_trait::async_trait;
use base64::Engine;
use her_llm::UserContent;
use schemars::JsonSchema;
use serde::Deserialize;
use serde_json::Value;
use serde_json::json;
use std::io;
use std::path::Path;
use std::path::PathBuf;

use crate::env::Environment;
use crate::env::FileContent;
use crate::env::LineWindow;
use crate::env::NotRegularFile;
use crate::env::TextWindow;
use crate::image::ImageFormat;
use crate::text;
use crate::tool::ToolCtx;
use crate::tool::ToolError;
use crate::tool::ToolOutput;
use crate::truncate::bytes_for_tokens;
use crate::truncate::format_size;
use crate::truncate::tokens_for_bytes;
use crate::typed::TypedTool;

/// Read first: enough for most text files, which then need no second read, and for telling
/// images and binary files from text.
const HEAD_BYTES: u64 = 64 * 1024;
/// Providers reject an image whose base64 data is over 5 MiB, and once one is in the history
/// every later request fails.
const MAX_IMAGE_BASE64: u64 = 5 * 1024 * 1024;
/// The largest image whose base64 data fits: 3.75 MiB.
const MAX_IMAGE_BYTES: u64 = MAX_IMAGE_BASE64 / 4 * 3;
/// Providers also reject an image with a side over this many pixels.
const MAX_IMAGE_SIDE: u32 = 8000;

pub struct ReadTool;

#[derive(Debug, Deserialize, JsonSchema)]
pub struct ReadArgs {
    /// Path to the file, relative to the working directory or absolute.
    pub path: String,
    /// Line number to start reading from (1-based). Defaults to 1.
    #[serde(default, deserialize_with = "crate::de::opt_usize")]
    pub offset: Option<usize>,
    /// Maximum number of lines to read.
    #[serde(default, deserialize_with = "crate::de::opt_usize")]
    pub limit: Option<usize>,
}

#[async_trait]
impl TypedTool for ReadTool {
    type Args = ReadArgs;
    const NAME: &'static str = "read";
    const DESCRIPTION: &'static str = "Read a file from the workspace. Text output is limited to 2000 lines or \
        10000 tokens; use offset and limit to read a large file in parts. Images (png, jpeg, gif, webp) are \
        returned as images when the model accepts them.";

    fn guidelines(&self) -> &[&'static str] {
        &["Use read to view files instead of running cat, head or tail in bash."]
    }

    fn describe(&self, args: &Value, env: &dyn Environment) -> crate::tool::ToolDescription {
        let line = args
            .get("offset")
            .and_then(|v| v.as_u64().or_else(|| v.as_str()?.trim().parse().ok()))
            .map(|n| n.max(1) as u32);
        crate::tool::ToolDescription::for_path(crate::tool::ToolCategory::Read, "read", args, env, line.or(Some(1)))
    }

    fn prepare_arguments(&self, mut args: Value) -> Value {
        // A common name for the same argument.
        if let Value::Object(map) = &mut args
            && !map.contains_key("path")
            && let Some(path) = map.remove("file_path")
        {
            map.insert("path".into(), path);
        }
        args
    }

    async fn run(&self, args: ReadArgs, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        let env = ctx.env.as_ref();
        let mut path = env.resolve(&args.path);
        let mut head = env.read_file(&path, HEAD_BYTES).await;
        let mut corrected = false;
        if let Err(err) = &head
            && err.kind() == io::ErrorKind::NotFound
        {
            let Some(found) = find_path_variant(env, &args.path).await else {
                return Err(ToolError::Failed(format!("File not found: {}", path.display())));
            };
            path = found;
            corrected = true;
            head = env.read_file(&path, HEAD_BYTES).await;
        }
        let display = path.display().to_string();
        let head = match head {
            Ok(head) => head,
            Err(err) => return Err(read_error(env, &path, &display, err).await),
        };
        if let Some(format) = ImageFormat::detect(&head.bytes) {
            return read_image(&ctx, &path, &display, format, head).await;
        }
        if text::is_binary(&head.bytes) {
            ctx.files.record(&path, head.stamp);
            return Err(ToolError::Failed(format!(
                "{display} is a binary file ({} bytes) and cannot be shown as text.",
                head.total_size
            )));
        }

        let limits = &ctx.limits;
        let window = LineWindow {
            offset: args.offset.unwrap_or(1).max(1) as u64,
            max_lines: args.limit.unwrap_or(limits.max_lines).clamp(1, limits.max_lines),
            max_bytes: bytes_for_tokens(limits.max_tokens),
            max_line_chars: limits.max_line_chars,
        };
        let text = if head.truncated {
            env.read_lines(&path, &window).await
        } else {
            // The whole file is at hand.
            text::window_in_memory(&head, &window)
        };
        let text = match text {
            Ok(text) => text,
            Err(err) => return Err(read_error(env, &path, &display, err).await),
        };
        ctx.files.record(&path, text.stamp);
        let mut output = show_lines(&text, &window, &display)?;
        if corrected && let Some(UserContent::Text { text }) = output.content.first_mut() {
            let gap = if text.ends_with('\n') { "\n" } else { "\n\n" };
            text.push_str(&format!("{gap}[Read {display}; the path given does not exist.]"));
        }
        Ok(output)
    }
}

/// The text result: the lines, then a note on how to go on when there is more.
fn show_lines(text: &TextWindow, window: &LineWindow, display: &str) -> Result<ToolOutput, ToolError> {
    let (start, total) = (window.offset, text.total_lines);
    if text.lines.is_empty() {
        if total == 0 && text.total_exact {
            return Ok(ToolOutput::text("(empty file)").with_details(json!({ "path": display, "total_lines": 0 })));
        }
        if text.total_exact {
            return Err(ToolError::Failed(format!("offset {start} is beyond the end of {display} ({total} lines).")));
        }
        // The lower bound counts a line cut off where reading stopped; a line at or before it
        // that shows nothing is that one, so the lines before it are what can be read.
        let readable = if start <= total { start - 1 } else { total };
        return Err(ToolError::Failed(format!(
            "Cannot reach line {start} of {display}: only its first {readable} lines can be read. Use bash to \
             read further."
        )));
    }
    let end = start + text.lines.len() as u64 - 1;
    let more = end < total || !text.total_exact;
    let mut out = String::with_capacity(text.lines.iter().map(|line| line.len() + 1).sum::<usize>() + 100);
    for line in &text.lines {
        out.push_str(line);
        out.push('\n');
    }
    if more {
        let of = if text.total_exact { total.to_string() } else { format!("at least {total}") };
        let why = if text.byte_limited {
            format!(" ({}-token limit)", tokens_for_bytes(window.max_bytes as u64))
        } else {
            String::new()
        };
        out.push_str(&format!("\n[Showing lines {start}-{end} of {of}{why}. Use offset={} to continue.]", end + 1));
    }
    Ok(ToolOutput::text(out).with_details(json!({
        "path": display,
        "start_line": start,
        "end_line": end,
        "total_lines": total,
        "truncated": more,
    })))
}

/// When `raw` names nothing, the first variant of it that exists, if any: a leading `@`
/// removed (from chat mentions); Unicode spaces made plain; a narrow no-break space before
/// `AM.` or `PM.` (macOS screenshot names); `’` for `'` (localized screenshot names). Single
/// fixes are tried first, in that order, then combinations.
pub async fn find_path_variant(env: &dyn Environment, raw: &str) -> Option<PathBuf> {
    const FIXES: [fn(&str) -> String; 4] = [without_at, plain_spaces, narrow_space_before_meridiem, curly_apostrophes];
    let mut sets: Vec<usize> = (1..1 << FIXES.len()).collect();
    sets.sort_by_key(|set| set.count_ones());
    let mut tried = vec![env.resolve(raw)];
    for set in sets {
        let mut candidate = raw.to_string();
        for (i, fix) in FIXES.iter().enumerate() {
            if set & (1 << i) != 0 {
                candidate = fix(&candidate);
            }
        }
        let path = env.resolve(&candidate);
        if tried.contains(&path) {
            continue;
        }
        if env.metadata(&path).await.is_ok() {
            return Some(path);
        }
        tried.push(path);
    }
    None
}

fn without_at(raw: &str) -> String {
    raw.strip_prefix('@').unwrap_or(raw).to_string()
}

fn plain_spaces(raw: &str) -> String {
    raw.replace(|c| matches!(c, '\u{00A0}' | '\u{2000}'..='\u{200A}' | '\u{202F}' | '\u{205F}' | '\u{3000}'), " ")
}

fn narrow_space_before_meridiem(raw: &str) -> String {
    raw.replace(" AM.", "\u{202F}AM.").replace(" PM.", "\u{202F}PM.")
}

fn curly_apostrophes(raw: &str) -> String {
    raw.replace('\'', "\u{2019}")
}

/// The error for a file that cannot be read.
async fn read_error(env: &dyn Environment, path: &Path, display: &str, err: io::Error) -> ToolError {
    if let Some(special) = NotRegularFile::of(&err) {
        return ToolError::Failed(format!("{display} is not a regular file ({}); it cannot be read.", special.kind));
    }
    if err.kind() == io::ErrorKind::IsADirectory || env.metadata(path).await.is_ok_and(|meta| meta.is_dir) {
        return ToolError::Failed(format!("{display} is a directory, not a file. Use bash (ls) to list it."));
    }
    if err.kind() == io::ErrorKind::NotFound {
        return ToolError::Failed(format!("File not found: {display}"));
    }
    ToolError::Failed(format!("Cannot read {display}: {err}"))
}

async fn read_image(
    ctx: &ToolCtx,
    path: &Path,
    display: &str,
    format: ImageFormat,
    head: FileContent,
) -> Result<ToolOutput, ToolError> {
    if !ctx.model_accepts_images {
        ctx.files.record(path, head.stamp);
        return Ok(ToolOutput::text(format!(
            "[Image file {display} ({} bytes). The current model does not accept images.]",
            head.total_size
        )));
    }
    let too_large = |size: u64| {
        ToolError::Failed(format!(
            "Image {display} is too large: {}, the limit is {:.2} MB ({} MB in base64). Make a smaller copy with bash \
             and read that instead.",
            format_size(size),
            MAX_IMAGE_BYTES as f64 / (1024.0 * 1024.0),
            MAX_IMAGE_BASE64 / (1024 * 1024),
        ))
    };
    if head.total_size > MAX_IMAGE_BYTES {
        return Err(too_large(head.total_size));
    }
    let file = if head.truncated {
        match ctx.env.read_file(path, MAX_IMAGE_BYTES).await {
            Ok(file) => file,
            Err(err) => return Err(read_error(ctx.env.as_ref(), path, display, err).await),
        }
    } else {
        head
    };
    ctx.files.record(path, file.stamp);
    if file.truncated {
        // It grew since.
        return Err(too_large(file.total_size));
    }
    let size = format.dimensions(&file.bytes);
    if let Some((width, height)) = size
        && width.max(height) > MAX_IMAGE_SIDE
    {
        return Err(ToolError::Failed(format!(
            "Image {display} is {width}x{height} pixels; the limit is {MAX_IMAGE_SIDE} pixels per side. Make a smaller \
             copy with bash and read that instead."
        )));
    }
    let pixels = size.map(|(width, height)| format!("{width}x{height}, ")).unwrap_or_default();
    let text = format!("Image: {display} ({}, {pixels}{})", format.name(), format_size(file.bytes.len() as u64));
    let data = base64::engine::general_purpose::STANDARD.encode(&file.bytes);
    Ok(ToolOutput {
        content: vec![
            UserContent::Text { text },
            UserContent::Image {
                mime_type: format.mime_type().to_string(),
                data,
            },
        ],
        details: Some(json!({
            "path": display,
            "image": format.mime_type(),
            "width": size.map(|s| s.0),
            "height": size.map(|s| s.1),
        })),
        is_error: false,
        ends_task: false,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::builtin::test_support::run;
    use crate::builtin::test_support::test_ctx;
    use crate::typed::Typed;

    async fn read(dir: &Path, args: Value) -> Result<ToolOutput, ToolError> {
        run(&ReadTool, args, test_ctx(dir)).await
    }

    /// A PNG of `size` bytes: signature and IHDR, then zeros.
    fn png(width: u32, height: u32, size: usize) -> Vec<u8> {
        let mut bytes = b"\x89PNG\r\n\x1a\n\0\0\0\x0dIHDR".to_vec();
        bytes.extend_from_slice(&width.to_be_bytes());
        bytes.extend_from_slice(&height.to_be_bytes());
        bytes.resize(size, 0);
        bytes
    }

    #[tokio::test]
    async fn reads_with_offset_and_limit() {
        let dir = tempfile::tempdir().unwrap();
        let body: String = (1..=10).map(|i| format!("line {i}\n")).collect();
        std::fs::write(dir.path().join("f.txt"), body).unwrap();
        let ctx = test_ctx(dir.path());
        let out = run(&ReadTool, json!({"path": "f.txt", "offset": 3, "limit": "2"}), ctx).await.unwrap();
        assert_eq!(out.content_text(), "line 3\nline 4\n\n[Showing lines 3-4 of 10. Use offset=5 to continue.]");
        let out = read(dir.path(), json!({"path": "f.txt", "offset": 9})).await.unwrap();
        assert_eq!(out.content_text(), "line 9\nline 10\n");
    }

    #[tokio::test]
    async fn reports_errors_clearly() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("bin"), [0u8, 1, 2, 3]).unwrap();
        std::fs::write(dir.path().join("short.txt"), "a\nb\n").unwrap();
        std::fs::write(dir.path().join("empty.txt"), "").unwrap();
        let ctx = test_ctx(dir.path());
        let missing = run(&ReadTool, json!({"path": "nope.txt"}), ctx.clone()).await.unwrap_err();
        assert_eq!(missing.to_string(), format!("File not found: {}/nope.txt", dir.path().display()));
        let binary = run(&ReadTool, json!({"path": "bin"}), ctx.clone()).await.unwrap_err();
        assert!(binary.to_string().contains("is a binary file (4 bytes)"));
        let beyond = run(&ReadTool, json!({"path": "short.txt", "offset": 9}), ctx.clone()).await.unwrap_err();
        assert!(beyond.to_string().contains("offset 9 is beyond the end of"), "{beyond}");
        assert!(beyond.to_string().ends_with("short.txt (2 lines)."));
        let dir_err = run(&ReadTool, json!({"path": "."}), ctx.clone()).await.unwrap_err();
        assert!(dir_err.to_string().contains("is a directory"));
        let empty = run(&ReadTool, json!({"path": "empty.txt"}), ctx).await.unwrap();
        assert_eq!(empty.content_text(), "(empty file)");
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn named_pipes_and_devices_fail_at_once() {
        use std::os::unix::ffi::OsStrExt;
        let dir = tempfile::tempdir().unwrap();
        let fifo = dir.path().join("pipe");
        let name = std::ffi::CString::new(fifo.as_os_str().as_bytes()).unwrap();
        assert_eq!(unsafe { libc::mkfifo(name.as_ptr(), 0o644) }, 0);
        // Nobody writes to the pipe: opening it for reading would wait forever.
        let err = tokio::time::timeout(std::time::Duration::from_secs(5), read(dir.path(), json!({"path": "pipe"})))
            .await
            .expect("does not wait for a writer")
            .unwrap_err();
        let expected = format!("{} is not a regular file (named pipe); it cannot be read.", fifo.display());
        assert_eq!(err.to_string(), expected);
        let err = read(dir.path(), json!({"path": "/dev/null"})).await.unwrap_err();
        assert_eq!(err.to_string(), "/dev/null is not a regular file (character device); it cannot be read.");
    }

    #[tokio::test]
    async fn caps_long_files() {
        let dir = tempfile::tempdir().unwrap();
        let body: String = (1..=3000).map(|i| format!("{i}\n")).collect();
        std::fs::write(dir.path().join("big.txt"), body).unwrap();
        let out = read(dir.path(), json!({"path": "big.txt"})).await.unwrap();
        let text = out.content_text();
        assert!(text.ends_with("1999\n2000\n\n[Showing lines 1-2000 of 3000. Use offset=2001 to continue.]"));
    }

    #[tokio::test]
    async fn the_token_budget_says_why_the_window_ended() {
        let dir = tempfile::tempdir().unwrap();
        // 3000 lines of 32 bytes: 10,000 tokens (40,000 bytes) end the window at line 1250.
        let body: String = (1..=3000).map(|i| format!("{i:031}\n")).collect();
        std::fs::write(dir.path().join("wide.txt"), body).unwrap();
        let out = read(dir.path(), json!({"path": "wide.txt"})).await.unwrap();
        let text = out.content_text();
        let note = "\n\n[Showing lines 1-1250 of 3000 (10000-token limit). Use offset=1251 to continue.]";
        assert!(text.ends_with(note), "{text}");
        assert_eq!(text.lines().filter(|l| l.len() == 31).count(), 1250);
        // A smaller limit ends it first.
        let out = read(dir.path(), json!({"path": "wide.txt", "offset": 2990, "limit": 5})).await.unwrap();
        assert!(out.content_text().ends_with("[Showing lines 2990-2994 of 3000. Use offset=2995 to continue.]"));
    }

    #[tokio::test]
    async fn long_lines_give_their_length() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("min.js"), format!("{}\nend\n", "é".repeat(81_234))).unwrap();
        let out = read(dir.path(), json!({"path": "min.js"})).await.unwrap();
        let text = out.content_text();
        assert_eq!(text, format!("{}… [line truncated: 2000 of 81234 characters]\nend\n", "é".repeat(2000)));
    }

    #[tokio::test]
    async fn every_line_break_ends_a_line_and_none_is_shown() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("crlf.txt"), "one\r\ntwo\r\n").unwrap();
        std::fs::write(dir.path().join("cr.txt"), "one\rtwo\r").unwrap();
        std::fs::write(dir.path().join("mixed.txt"), "one\r\ntwo\rthree\n\r\nfive").unwrap();
        for name in ["crlf.txt", "cr.txt"] {
            let out = read(dir.path(), json!({"path": name})).await.unwrap();
            assert_eq!(out.content_text(), "one\ntwo\n", "{name}");
        }
        let out = read(dir.path(), json!({"path": "mixed.txt", "limit": 4})).await.unwrap();
        assert_eq!(out.content_text(), "one\ntwo\nthree\n\n\n[Showing lines 1-4 of 5. Use offset=5 to continue.]");
    }

    #[tokio::test]
    async fn invalid_utf8_is_replaced() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("latin1.txt"), b"caf\xe9\nok\n").unwrap();
        let out = read(dir.path(), json!({"path": "latin1.txt"})).await.unwrap();
        assert_eq!(out.content_text(), "caf\u{FFFD}\nok\n");
    }

    #[tokio::test]
    async fn utf16_with_a_byte_order_mark_is_text() {
        let dir = tempfile::tempdir().unwrap();
        let text = "naïve\r\n日本\r\n";
        let le: Vec<u8> = [0xFF, 0xFE].into_iter().chain(text.encode_utf16().flat_map(u16::to_le_bytes)).collect();
        let be: Vec<u8> = [0xFE, 0xFF].into_iter().chain(text.encode_utf16().flat_map(u16::to_be_bytes)).collect();
        std::fs::write(dir.path().join("le.txt"), le).unwrap();
        std::fs::write(dir.path().join("be.txt"), be).unwrap();
        for name in ["le.txt", "be.txt"] {
            let out = read(dir.path(), json!({"path": name})).await.unwrap();
            assert_eq!(out.content_text(), "naïve\n日本\n", "{name}");
        }
    }

    #[tokio::test]
    async fn reads_anywhere_in_a_file_past_16_mib() {
        let dir = tempfile::tempdir().unwrap();
        let lines = 2_000_000;
        let body: String = (1..=lines).map(|i| format!("line {i}\n")).collect();
        assert!(body.len() > 20 << 20);
        std::fs::write(dir.path().join("big.log"), body).unwrap();
        let out = read(dir.path(), json!({"path": "big.log", "offset": 1_999_998})).await.unwrap();
        assert_eq!(out.content_text(), "line 1999998\nline 1999999\nline 2000000\n");
        let out = read(dir.path(), json!({"path": "big.log", "offset": 1_500_000, "limit": 2})).await.unwrap();
        assert_eq!(
            out.content_text(),
            "line 1500000\nline 1500001\n\n[Showing lines 1500000-1500001 of 2000000. Use offset=1500002 to continue.]"
        );
        let err = read(dir.path(), json!({"path": "big.log", "offset": 2_400_000})).await.unwrap_err();
        assert!(err.to_string().ends_with("big.log (2000000 lines)."), "{err}");
    }

    #[tokio::test]
    async fn records_the_version_it_read() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("f.txt");
        std::fs::write(&path, "v1\n").unwrap();
        let ctx = test_ctx(dir.path());
        run(&ReadTool, json!({"path": "f.txt"}), ctx.clone()).await.unwrap();
        let stamp = crate::files::FileStamp::of(&std::fs::metadata(&path).unwrap());
        assert!(ctx.files.verify(&path, stamp).is_ok());
    }

    #[tokio::test]
    async fn images_need_a_vision_model() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("a.png"), png(1280, 720, 345 * 1024)).unwrap();
        let mut ctx = test_ctx(dir.path());
        let out = run(&ReadTool, json!({"path": "a.png"}), ctx.clone()).await.unwrap();
        assert!(out.content_text().contains("does not accept images"));
        ctx.model_accepts_images = true;
        let out = run(&ReadTool, json!({"path": "a.png"}), ctx).await.unwrap();
        assert_eq!(out.content_text(), format!("Image: {}/a.png (PNG, 1280x720, 345.0 KB)", dir.path().display()));
        let UserContent::Image { mime_type, data } = &out.content[1] else { panic!("{out:?}") };
        assert_eq!(mime_type, "image/png");
        assert_eq!(base64::engine::general_purpose::STANDARD.decode(data).unwrap(), png(1280, 720, 345 * 1024));
    }

    #[tokio::test]
    async fn images_must_fit_the_provider_limits() {
        let dir = tempfile::tempdir().unwrap();
        let mut ctx = test_ctx(dir.path());
        ctx.model_accepts_images = true;
        // The base64 data of the largest image is exactly 5 MiB.
        std::fs::write(dir.path().join("max.png"), png(100, 100, MAX_IMAGE_BYTES as usize)).unwrap();
        let out = run(&ReadTool, json!({"path": "max.png"}), ctx.clone()).await.unwrap();
        let UserContent::Image { data, .. } = &out.content[1] else { panic!() };
        assert_eq!(data.len(), 5 * 1024 * 1024);
        std::fs::write(dir.path().join("big.png"), png(100, 100, MAX_IMAGE_BYTES as usize + 1)).unwrap();
        let err = run(&ReadTool, json!({"path": "big.png"}), ctx.clone()).await.unwrap_err();
        assert!(
            err.to_string().contains("big.png is too large: 3.8 MB, the limit is 3.75 MB (5 MB in base64)."),
            "{err}"
        );

        std::fs::write(dir.path().join("wide.png"), png(8001, 600, 100)).unwrap();
        let err = run(&ReadTool, json!({"path": "wide.png"}), ctx.clone()).await.unwrap_err();
        assert!(err.to_string().contains("is 8001x600 pixels; the limit is 8000 pixels per side."), "{err}");
        std::fs::write(dir.path().join("edge.png"), png(8000, 8000, 100)).unwrap();
        run(&ReadTool, json!({"path": "edge.png"}), ctx.clone()).await.unwrap();
        // Without a readable header only the size limit applies.
        std::fs::write(dir.path().join("odd.png"), b"\x89PNG\r\n\x1a\n\0\0\0\x0dCgBI").unwrap();
        let out = run(&ReadTool, json!({"path": "odd.png"}), ctx).await.unwrap();
        assert!(out.content_text().ends_with("odd.png (PNG, 16 B)"));
    }

    #[tokio::test]
    async fn finds_the_file_a_mistyped_path_means() {
        let dir = tempfile::tempdir().unwrap();
        let names = [
            ("notes.txt", "@notes.txt"),
            ("two words.txt", "two\u{00A0}words.txt"),
            ("Shot 2026-10-29 at 10.15.32\u{202F}PM.txt", "Shot 2026-10-29 at 10.15.32 PM.txt"),
            ("Capture d\u{2019}écran.txt", "Capture d'écran.txt"),
            ("Shot at 9.00.00\u{202F}AM.txt", "@Shot at 9.00.00 AM.txt"),
        ];
        for (name, typed) in names {
            std::fs::write(dir.path().join(name), name).unwrap();
            let out = read(dir.path(), json!({"path": typed})).await.unwrap();
            let real = dir.path().join(name);
            assert_eq!(
                out.content_text(),
                format!("{name}\n\n[Read {}; the path given does not exist.]", real.display()),
                "{typed}"
            );
        }
        let env = crate::local_env::LocalEnvironment::new(dir.path().to_path_buf());
        assert_eq!(find_path_variant(&env, "@nothing.txt").await, None);
        // A path that exists is never replaced.
        std::fs::write(dir.path().join("@notes.txt"), "mention").unwrap();
        let out = read(dir.path(), json!({"path": "@notes.txt"})).await.unwrap();
        assert_eq!(out.content_text(), "mention\n");
    }

    #[tokio::test]
    async fn accepts_file_urls() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("my file.txt"), "hello\n").unwrap();
        let url = format!("file://{}/my%20file.txt", dir.path().display());
        let out = read(dir.path(), json!({ "path": url })).await.unwrap();
        assert_eq!(out.content_text(), "hello\n");
    }

    #[tokio::test]
    async fn file_path_is_another_name_for_path() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("f.txt"), "content\n").unwrap();
        let tool = Typed::new(ReadTool);
        let args = crate::tool::Tool::prepare_arguments(&tool, json!({"file_path": "f.txt", "limit": 1}));
        assert_eq!(args, json!({"path": "f.txt", "limit": 1}));
        assert_eq!(read(dir.path(), args).await.unwrap().content_text(), "content\n");
        // With both, path wins and nothing else changes.
        let both = json!({"path": "a", "file_path": "b"});
        assert_eq!(crate::tool::Tool::prepare_arguments(&tool, both.clone()), both);
    }
}
