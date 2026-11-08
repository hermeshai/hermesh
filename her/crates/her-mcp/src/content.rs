//! A `tools/call` result as the model gets it. Text and images pass through; anything the
//! model cannot take is saved in the session's `tool-output` directory and named by path.

use base64::Engine;
use her_llm::UserContent;
use her_tools::ToolCtx;
use her_tools::ToolOutput;
use serde_json::json;
use std::path::PathBuf;

use crate::types::CallToolResult;
use crate::types::Content;

/// Same limit as the read tool.
pub const MAX_IMAGE_BYTES: usize = 5 * 1024 * 1024;
const NO_MESSAGE: &str = "The MCP tool reported an error without a message.";

struct Saver<'a> {
    ctx: &'a ToolCtx,
    count: usize,
}

impl Saver<'_> {
    /// Writes decoded base64 data; `Err` explains why it could not be saved.
    fn save(&mut self, data: &str, mime: &str) -> Result<(PathBuf, usize), String> {
        let bytes = base64::engine::general_purpose::STANDARD
            .decode(data.trim())
            .map_err(|_| "the data is not valid base64".to_string())?;
        self.count += 1;
        let name = format!("{}-{}.{}", self.ctx.call_id, self.count, extension(mime));
        std::fs::create_dir_all(&self.ctx.output_dir).map_err(|e| e.to_string())?;
        let path = self.ctx.output_dir.join(name);
        std::fs::write(&path, &bytes).map_err(|e| e.to_string())?;
        Ok((path, bytes.len()))
    }

    fn note(&mut self, what: &str, data: &str, mime: &str, reason: Option<&str>) -> String {
        let reason = reason.map(|r| format!(" {r}")).unwrap_or_default();
        match self.save(data, mime) {
            Ok((path, size)) => format!("[{what} ({mime}, {}) saved to {}.{reason}]", human_size(size), path.display()),
            Err(err) => format!("[{what} ({mime}) omitted: {err}.{reason}]"),
        }
    }
}

pub fn to_output(result: CallToolResult, ctx: &ToolCtx, server: &str, tool: &str) -> ToolOutput {
    let mut saver = Saver { ctx, count: 0 };
    let mut content: Vec<UserContent> = Vec::new();
    let text = |content: &mut Vec<UserContent>, text: String| content.push(UserContent::Text { text });
    for item in result.content {
        match item {
            Content::Text(t) => text(&mut content, t),
            Content::Image { data, mime_type } => image(&mut content, &mut saver, ctx, data, mime_type, "Image"),
            Content::Audio { data, mime_type } => {
                let note = saver.note("Audio", &data, &mime_type, None);
                text(&mut content, note);
            }
            Content::ResourceLink { uri, name, mime_type } => {
                let name = name.map(|n| format!("{n} ")).unwrap_or_default();
                let mime = mime_type.map(|m| format!(" ({m})")).unwrap_or_default();
                text(&mut content, format!("[Resource: {name}{uri}{mime}]"));
            }
            Content::Resource {
                uri,
                mime_type,
                text: Some(body),
                ..
            } => {
                let _ = mime_type;
                text(&mut content, format!("Resource {uri}:\n{body}"));
            }
            Content::Resource {
                uri,
                mime_type,
                blob: Some(blob),
                ..
            } => {
                let mime = mime_type.unwrap_or_else(|| "application/octet-stream".into());
                if mime.starts_with("image/") {
                    image(&mut content, &mut saver, ctx, blob, mime, "Image resource");
                } else if is_textual(&mime) {
                    match base64::engine::general_purpose::STANDARD.decode(blob.trim()) {
                        Ok(bytes) => text(&mut content, format!("Resource {uri}:\n{}", String::from_utf8_lossy(&bytes))),
                        Err(_) => text(&mut content, format!("[Resource {uri} ({mime}) omitted: invalid base64.]")),
                    }
                } else {
                    let note = saver.note(&format!("Binary resource {uri}"), &blob, &mime, None);
                    text(&mut content, note);
                }
            }
            Content::Resource { uri, .. } => text(&mut content, format!("[Resource: {uri}]")),
            Content::Unknown(kind) => text(&mut content, format!("[Unsupported content type '{kind}' omitted.]")),
        }
    }
    if content.is_empty()
        && let Some(structured) = &result.structured_content
    {
        text(&mut content, structured.to_string());
    }
    let has_text = content
        .iter()
        .any(|c| matches!(c, UserContent::Text { text } if !text.trim().is_empty()));
    if result.is_error && !has_text {
        content.insert(0, UserContent::Text { text: NO_MESSAGE.into() });
    }
    if content.is_empty() {
        text(&mut content, "(no output)".into());
    }
    ToolOutput {
        content,
        details: Some(json!({ "server": server, "tool": tool })),
        is_error: result.is_error,
        ends_task: false,
    }
}

fn image(content: &mut Vec<UserContent>, saver: &mut Saver<'_>, ctx: &ToolCtx, data: String, mime: String, what: &str) {
    let approx_size = data.len() / 4 * 3;
    if !ctx.model_accepts_images {
        let note = saver.note(what, &data, &mime, Some("The current model does not accept images."));
        content.push(UserContent::Text { text: note });
    } else if approx_size > MAX_IMAGE_BYTES {
        let note = saver.note(what, &data, &mime, Some("It is too large to show (limit 5 MB)."));
        content.push(UserContent::Text { text: note });
    } else {
        content.push(UserContent::Image { mime_type: mime, data });
    }
}

fn is_textual(mime: &str) -> bool {
    let mime = mime.split(';').next().unwrap_or(mime).trim();
    mime.starts_with("text/") || mime == "application/json" || mime == "application/xml" || mime.ends_with("+json") || mime.ends_with("+xml")
}

fn extension(mime: &str) -> &'static str {
    match mime.split(';').next().unwrap_or(mime).trim() {
        "image/png" => "png",
        "image/jpeg" => "jpg",
        "image/gif" => "gif",
        "image/webp" => "webp",
        "image/svg+xml" => "svg",
        "audio/wav" | "audio/x-wav" => "wav",
        "audio/mpeg" => "mp3",
        "audio/ogg" => "ogg",
        "application/pdf" => "pdf",
        "application/zip" => "zip",
        _ => "bin",
    }
}

fn human_size(bytes: usize) -> String {
    if bytes >= 1 << 20 {
        format!("{:.1} MB", bytes as f64 / f64::from(1 << 20))
    } else if bytes >= 1 << 10 {
        format!("{} KB", bytes.div_ceil(1 << 10))
    } else {
        format!("{bytes} B")
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_tools::FileTracker;
    use her_tools::LocalEnvironment;
    use her_tools::OutputLimits;
    use her_tools::PathLocks;
    use her_tools::ProgressSink;
    use std::sync::Arc;
    use tokio_util::sync::CancellationToken;

    fn ctx(dir: &std::path::Path, images: bool) -> ToolCtx {
        ToolCtx {
            call_id: "call_1".into(),
            env: Arc::new(LocalEnvironment::new(dir.to_path_buf())),
            files: Arc::new(FileTracker::new()),
            locks: Arc::new(PathLocks::new()),
            limits: OutputLimits::default(),
            progress: ProgressSink::noop(),
            cancel: CancellationToken::new(),
            model_accepts_images: images,
            output_dir: dir.join("tool-output"),
            session_state: Default::default(),
            authorities: Default::default(),
        }
    }

    fn texts(output: &ToolOutput) -> Vec<String> {
        output
            .content
            .iter()
            .filter_map(|c| match c {
                UserContent::Text { text } => Some(text.clone()),
                _ => None,
            })
            .collect()
    }

    #[test]
    fn converts_each_kind() {
        let dir = tempfile::tempdir().unwrap();
        let png = base64::engine::general_purpose::STANDARD.encode([0x89, b'P', b'N', b'G']);
        let result = CallToolResult {
            content: vec![
                Content::Text("hello".into()),
                Content::Image { data: png.clone(), mime_type: "image/png".into() },
                Content::Audio { data: png.clone(), mime_type: "audio/wav".into() },
                Content::ResourceLink { uri: "file:///x".into(), name: Some("x".into()), mime_type: None },
                Content::Resource {
                    uri: "mem://j".into(),
                    mime_type: Some("application/json".into()),
                    text: None,
                    blob: Some(base64::engine::general_purpose::STANDARD.encode("{\"a\":1}")),
                },
                Content::Unknown("hologram".into()),
            ],
            structured_content: None,
            is_error: false,
        };
        let out = to_output(result.clone(), &ctx(dir.path(), true), "s", "t");
        assert!(matches!(out.content[1], UserContent::Image { .. }));
        let t = texts(&out);
        assert_eq!(t[0], "hello");
        assert!(t[1].starts_with("[Audio (audio/wav, 4 B) saved to "), "{}", t[1]);
        assert_eq!(t[2], "[Resource: x file:///x]");
        assert_eq!(t[3], "Resource mem://j:\n{\"a\":1}");
        assert!(t[4].contains("hologram"));
        assert!(dir.path().join("tool-output/call_1-1.wav").exists());
        assert_eq!(out.details, Some(json!({"server": "s", "tool": "t"})));

        let no_images = to_output(result, &ctx(dir.path(), false), "s", "t");
        assert!(texts(&no_images)[1].contains("does not accept images"));
        assert!(!no_images.content.iter().any(|c| matches!(c, UserContent::Image { .. })));
    }

    #[test]
    fn structured_errors_and_empty_results() {
        let dir = tempfile::tempdir().unwrap();
        let structured = CallToolResult {
            content: vec![],
            structured_content: Some(json!({"n": 1})),
            is_error: false,
        };
        assert_eq!(texts(&to_output(structured, &ctx(dir.path(), true), "s", "t")), vec!["{\"n\":1}"]);
        let error = CallToolResult {
            is_error: true,
            ..Default::default()
        };
        let out = to_output(error, &ctx(dir.path(), true), "s", "t");
        assert!(out.is_error);
        assert_eq!(texts(&out), vec![NO_MESSAGE]);
        let empty = to_output(CallToolResult::default(), &ctx(dir.path(), true), "s", "t");
        assert_eq!(texts(&empty), vec!["(no output)"]);
    }
}
