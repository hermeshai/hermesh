//! History conversion before a request, so that any history can be sent to any model.

use std::collections::HashMap;
use std::sync::Arc;

use crate::model::Model;
use crate::types::AssistantContent;
use crate::types::Context;
use crate::types::Message;
use crate::types::UserContent;

const IMAGE_OMITTED: &str = "(image omitted: the current model does not accept images)";

/// Messages are shared with the session history, so only the ones that change are copied.
pub fn transform_for(model: &Model, mut context: Context) -> Context {
    let images_ok = model.accepts_images();
    for message in &mut context.messages {
        match message {
            Message::Assistant(assistant) => {
                let same_model =
                    assistant.provider == model.provider && assistant.api == model.api && assistant.model == model.id;
                let keep = |c: &AssistantContent| match c {
                    // Signatures and encrypted reasoning only work with the model that produced
                    // them; plain reasoning text would be mistaken for part of the reply.
                    AssistantContent::Thinking { .. } => same_model,
                    AssistantContent::Text { text } => !text.is_empty(),
                    AssistantContent::ToolCall(_) => true,
                };
                if !assistant.content.iter().all(keep) {
                    Arc::make_mut(assistant).content.retain(keep);
                }
            }
            Message::User(user) if !images_ok && has_images(&user.content) => {
                replace_images(&mut Arc::make_mut(user).content)
            }
            Message::ToolResult(result) if !images_ok && has_images(&result.content) => {
                replace_images(&mut Arc::make_mut(result).content)
            }
            _ => {}
        }
    }
    context
        .messages
        .retain(|m| !matches!(m, Message::Assistant(a) if a.content.is_empty()));
    if model.api == "anthropic-messages" {
        normalize_tool_ids(&mut context);
    }
    context
}

fn has_images(content: &[UserContent]) -> bool {
    content.iter().any(|c| matches!(c, UserContent::Image { .. }))
}

fn replace_images(content: &mut [UserContent]) {
    for block in content.iter_mut() {
        if matches!(block, UserContent::Image { .. }) {
            *block = UserContent::Text {
                text: IMAGE_OMITTED.to_string(),
            };
        }
    }
}

fn valid_anthropic_id(id: &str) -> bool {
    !id.is_empty() && id.len() <= 64 && id.chars().all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '-')
}

/// Maps tool-call ids that the target protocol would reject to stable valid ones.
fn normalize_tool_ids(context: &mut Context) {
    let mut mapping: HashMap<String, String> = HashMap::new();
    for message in &mut context.messages {
        match message {
            Message::Assistant(assistant) => {
                let invalid = |c: &AssistantContent| matches!(c, AssistantContent::ToolCall(call) if !valid_anthropic_id(&call.id));
                if !assistant.content.iter().any(invalid) {
                    continue;
                }
                for content in &mut Arc::make_mut(assistant).content {
                    if let AssistantContent::ToolCall(call) = content
                        && !valid_anthropic_id(&call.id)
                    {
                        let new_id = format!("call_{:016x}", fnv1a(&call.id));
                        mapping.insert(call.id.clone(), new_id.clone());
                        call.id = new_id;
                    }
                }
            }
            Message::ToolResult(result) => {
                if let Some(new_id) = mapping.get(&result.call_id) {
                    Arc::make_mut(result).call_id = new_id.clone();
                }
            }
            Message::User(_) => {}
        }
    }
}

fn fnv1a(text: &str) -> u64 {
    let mut hash: u64 = 0xcbf2_9ce4_8422_2325;
    for byte in text.as_bytes() {
        hash ^= u64::from(*byte);
        hash = hash.wrapping_mul(0x0100_0000_01b3);
    }
    hash
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::test_model;
    use crate::types::AssistantMessage;
    use crate::types::StopReason;
    use crate::types::ToolCall;
    use crate::types::ToolResultMessage;
    use crate::types::Usage;

    fn assistant(provider: &str, model: &str, content: Vec<AssistantContent>) -> Message {
        Message::from(AssistantMessage {
            content,
            provider: provider.into(),
            api: "openai-chat".into(),
            model: model.into(),
            response_id: None,
            usage: Usage::default(),
            stop_reason: StopReason::Stop,
            error: None,
            ts: 0,
            duration_ms: None,
        })
    }

    #[test]
    fn drops_thinking_from_other_models() {
        let model = test_model("openai-chat");
        let thinking = AssistantContent::Thinking {
            text: "hmm".into(),
            signature: None,
            redacted: false,
        };
        let ctx = Context {
            system: String::new(),
            messages: vec![
                assistant("test", "test-model", vec![thinking.clone(), AssistantContent::Text { text: "a".into() }]),
                assistant("other", "x", vec![thinking.clone(), AssistantContent::Text { text: "b".into() }]),
                assistant("other", "x", vec![thinking]),
            ],
            tools: vec![],
        };
        let out = transform_for(&model, ctx);
        assert_eq!(out.messages.len(), 2, "the thinking-only message from another model disappears");
        let Message::Assistant(first) = &out.messages[0] else { panic!() };
        assert_eq!(first.content.len(), 2);
        let Message::Assistant(second) = &out.messages[1] else { panic!() };
        assert_eq!(second.content.len(), 1);
    }

    #[test]
    fn rewrites_invalid_ids_for_anthropic() {
        let model = test_model("anthropic-messages");
        let call = ToolCall {
            id: "call:1/abc".into(),
            name: "read".into(),
            arguments: serde_json::json!({}).into(),
            arguments_error: None,
            provider_data: None,
        };
        let ctx = Context {
            system: String::new(),
            messages: vec![
                assistant("other", "x", vec![AssistantContent::ToolCall(call)]),
                Message::from(ToolResultMessage {
                    call_id: "call:1/abc".into(),
                    tool_name: "read".into(),
                    content: vec![UserContent::Text { text: "ok".into() }],
                    is_error: false,
                    details: None,
                    ts: 0,
                    duration_ms: None,
                }),
            ],
            tools: vec![],
        };
        let out = transform_for(&model, ctx);
        let Message::Assistant(a) = &out.messages[0] else { panic!() };
        let new_id = a.tool_calls()[0].id.clone();
        assert!(valid_anthropic_id(&new_id));
        let Message::ToolResult(r) = &out.messages[1] else { panic!() };
        assert_eq!(r.call_id, new_id);
    }

    #[test]
    fn replaces_images_for_text_only_models() {
        let model = test_model("openai-chat");
        let ctx = Context {
            system: String::new(),
            messages: vec![Message::from(crate::types::UserMessage {
                content: vec![UserContent::Image {
                    mime_type: "image/png".into(),
                    data: "AAAA".into(),
                }],
                ts: 0,
            })],
            tools: vec![],
        };
        let out = transform_for(&model, ctx);
        let Message::User(u) = &out.messages[0] else { panic!() };
        assert_eq!(u.text_content(), IMAGE_OMITTED);
    }
}
