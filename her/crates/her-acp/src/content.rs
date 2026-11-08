//! Conversions: ACP prompt content to kernel input, and a stored transcript to the
//! `session/update` notifications that replay it for `session/load`.

use bytes::Bytes;
use her_llm::AssistantContent;
use her_llm::JsonText;
use her_llm::Message;
use her_llm::ToolResultMessage;
use her_llm::UserContent;
use her_protocol::api::TranscriptItem;
use her_rpc::RpcError;
use her_server::errors;
use serde_json::Value;
use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;

use crate::out;
use crate::out::Block;
use crate::out::Chunk;
use crate::out::LocationOut;
use crate::out::ToolCallOut;
use crate::out::ToolContent;
use crate::schema::ContentBlock;
use crate::schema::EmbeddedResource;

/// Kernel input for an ACP prompt.
pub fn prompt_input(blocks: &[ContentBlock]) -> Result<Vec<UserContent>, RpcError> {
    let mut input = Vec::with_capacity(blocks.len());
    for block in blocks {
        input.push(match block {
            ContentBlock::Text { text } => UserContent::Text { text: text.clone() },
            ContentBlock::Image { data, mime_type } => UserContent::Image {
                mime_type: mime_type.clone(),
                data: data.clone(),
            },
            ContentBlock::ResourceLink { uri } => UserContent::Text {
                text: match uri.strip_prefix("file://") {
                    Some(path) => format!("@{path}"),
                    None => uri.clone(),
                },
            },
            ContentBlock::Resource { resource } => match resource {
                EmbeddedResource::Text { uri, text } => UserContent::Text {
                    text: her_core::commands::resource_text(uri, text),
                },
                EmbeddedResource::Blob {} => return Err(errors::unsupported_content("binary resources are not supported")),
            },
            ContentBlock::Audio {} => return Err(errors::unsupported_content("audio input is not supported")),
            ContentBlock::Unknown => return Err(errors::unsupported_content("unsupported content block")),
        });
    }
    if input.is_empty() {
        return Err(RpcError::invalid_params("the prompt is empty"));
    }
    Ok(input)
}

/// What a tool call looks like when only its name and arguments are known (history).
pub struct CallView {
    pub kind: &'static str,
    pub title: String,
    pub path: Option<PathBuf>,
}

pub fn describe(name: &str, args: &Value, cwd: &Path) -> CallView {
    let path = args.get("path").and_then(Value::as_str);
    let resolved = path.map(|p| {
        let p = Path::new(p);
        if p.is_absolute() { p.to_path_buf() } else { cwd.join(p) }
    });
    let (kind, title) = match name {
        "read" => ("read", format!("read {}", path.unwrap_or(""))),
        "write" => ("edit", format!("write {}", path.unwrap_or(""))),
        "edit" => ("edit", format!("edit {}", path.unwrap_or(""))),
        "bash" => (
            "execute",
            args.get("command")
                .and_then(Value::as_str)
                .and_then(|c| c.lines().next())
                .unwrap_or("bash")
                .chars()
                .take(200)
                .collect(),
        ),
        other => ("other", other.to_string()),
    };
    CallView {
        kind,
        title,
        path: resolved,
    }
}

/// The content shown for a finished call: diffs for file changes, text for commands.
pub fn completion_content<'a>(
    name: &str,
    args: &'a Value,
    path: Option<&'a Path>,
    result: &'a ToolResultMessage,
    text: &'a str,
) -> Vec<ToolContent<'a>> {
    if result.is_error {
        return vec![ToolContent::text(text)];
    }
    match (name, path) {
        ("edit", Some(path)) => args
            .get("edits")
            .and_then(Value::as_array)
            .map(|edits| {
                edits
                    .iter()
                    .filter_map(|e| {
                        Some(ToolContent::Diff {
                            path,
                            old_text: Some(e.get("old_text")?.as_str()?),
                            new_text: e.get("new_text")?.as_str()?,
                        })
                    })
                    .collect()
            })
            .unwrap_or_default(),
        ("write", Some(path)) => {
            let created = result
                .details
                .as_ref()
                .map(JsonText::to_value)
                .and_then(|d| d.get("created").and_then(Value::as_bool))
                .unwrap_or(false);
            match args.get("content").and_then(Value::as_str) {
                Some(content) if created => vec![ToolContent::Diff {
                    path,
                    old_text: None,
                    new_text: content,
                }],
                _ => vec![ToolContent::text(text)],
            }
        }
        ("read", _) => Vec::new(),
        _ => vec![ToolContent::text(text)],
    }
}

/// `session/update` notifications that replay a transcript, oldest first.
pub fn replay(session_id: &str, cwd: &Path, items: &[TranscriptItem]) -> Vec<Bytes> {
    let results: HashMap<&str, &ToolResultMessage> = items
        .iter()
        .filter_map(|item| match item {
            TranscriptItem::ToolResult { call_id, result } => Some((call_id.as_str(), result.as_ref())),
            _ => None,
        })
        .collect();
    let mut out = Vec::new();
    for item in items {
        // A sub-agent's report is shown with its tool call, not as a user message.
        let TranscriptItem::Message {
            message_id,
            message,
            mail: None,
            ..
        } = item
        else {
            continue;
        };
        match message {
            Message::User(user) => {
                for content in &user.content {
                    out.push(out::update(
                        session_id,
                        Chunk {
                            session_update: "user_message_chunk",
                            content: Block::from_user(content),
                            message_id: Some(message_id),
                        },
                    ));
                }
            }
            Message::Assistant(assistant) => {
                for content in &assistant.content {
                    match content {
                        AssistantContent::Thinking { text, redacted, .. } if !redacted && !text.is_empty() => {
                            out.push(out::update(
                                session_id,
                                Chunk {
                                    session_update: "agent_thought_chunk",
                                    content: Block::Text { text },
                                    message_id: Some(message_id),
                                },
                            ));
                        }
                        AssistantContent::Text { text } if !text.is_empty() => {
                            out.push(out::update(
                                session_id,
                                Chunk {
                                    session_update: "agent_message_chunk",
                                    content: Block::Text { text },
                                    message_id: Some(message_id),
                                },
                            ));
                        }
                        AssistantContent::ToolCall(call) => {
                            let arguments = call.arguments.to_value();
                            let view = describe(&call.name, &arguments, cwd);
                            let result = results.get(call.id.as_str()).copied();
                            let text = result.map(ToolResultMessage::text_content).unwrap_or_default();
                            let content = match result {
                                Some(result) => {
                                    completion_content(&call.name, &arguments, view.path.as_deref(), result, &text)
                                }
                                None => Vec::new(),
                            };
                            let status = match result {
                                Some(r) if r.is_error => "failed",
                                Some(_) => "completed",
                                None => "failed",
                            };
                            out.push(out::update(
                                session_id,
                                ToolCallOut {
                                    session_update: "tool_call",
                                    tool_call_id: &call.id,
                                    title: &view.title,
                                    name: &call.name,
                                    kind: view.kind,
                                    status,
                                    locations: view
                                        .path
                                        .as_deref()
                                        .map(|path| vec![LocationOut { path, line: None }])
                                        .unwrap_or_default(),
                                    content,
                                    raw_input: &arguments,
                                    raw_output: result.and_then(|r| r.details.as_ref()),
                                },
                            ));
                        }
                        _ => {}
                    }
                }
            }
            Message::ToolResult(_) => {}
        }
    }
    out
}
