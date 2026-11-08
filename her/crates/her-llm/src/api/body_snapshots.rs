//! Request-body snapshots for both wire protocols across the compat switches, so changes to how
//! bodies are built cannot silently change what providers receive. The expected bodies live in
//! `tests/fixtures/bodies/`; run with `HER_UPDATE_SNAPSHOTS=1` to rewrite them after an intended
//! change.

use serde_json::Value;
use serde_json::json;
use std::path::PathBuf;

use crate::model::MaxTokensField;
use crate::model::Modality;
use crate::model::Model;
use crate::model::SystemRole;
use crate::model::ThinkingFormat;
use crate::model::test_model;
use crate::types::AssistantContent;
use crate::types::AssistantMessage;
use crate::types::Context;
use crate::types::Message;
use crate::types::RequestOptions;
use crate::types::StopReason;
use crate::types::ThinkingLevel;
use crate::types::ToolCall;
use crate::types::ToolChoice;
use crate::types::ToolResultMessage;
use crate::types::ToolSpec;
use crate::types::Usage;
use crate::types::UserContent;
use crate::types::UserMessage;

fn text(text: &str) -> UserContent {
    UserContent::Text { text: text.into() }
}

fn image(mime_type: &str, data: &str) -> UserContent {
    UserContent::Image {
        mime_type: mime_type.into(),
        data: data.into(),
    }
}

fn call(id: &str, name: &str, arguments: Value) -> AssistantContent {
    AssistantContent::ToolCall(ToolCall {
        id: id.into(),
        name: name.into(),
        arguments: arguments.into(),
        arguments_error: None,
        provider_data: None,
    })
}

fn assistant(model: &Model, content: Vec<AssistantContent>) -> Message {
    Message::from(AssistantMessage {
        content,
        provider: model.provider.clone(),
        api: model.api.clone(),
        model: model.id.clone(),
        response_id: None,
        usage: Usage::default(),
        stop_reason: StopReason::ToolUse,
        error: None,
        ts: 0,
        duration_ms: None,
    })
}

fn result(call_id: &str, content: Vec<UserContent>, is_error: bool) -> Message {
    Message::from(ToolResultMessage {
        call_id: call_id.into(),
        tool_name: "read".into(),
        content,
        is_error,
        details: Some(json!({ "path": "a.txt" }).into()),
        ts: 0,
        duration_ms: None,
    })
}

fn user(content: Vec<UserContent>) -> Message {
    Message::from(UserMessage { content, ts: 0 })
}

/// Every message shape the adapters handle: text in parts, images, reasoning (plain and
/// redacted), several tool calls, error and empty results, results carrying images.
fn conversation(model: &Model) -> Context {
    let thinking = |text: &str, signature: &str, redacted: bool| AssistantContent::Thinking {
        text: text.into(),
        signature: Some(signature.into()),
        redacted,
    };
    Context {
        system: "You are a test.\nBe brief.".into(),
        messages: vec![
            user(vec![text("first"), text("second \"quoted\"")]),
            assistant(
                model,
                vec![
                    thinking("plan the reads", "reasoning_content", false),
                    AssistantContent::Text { text: "I will read".into() },
                    call("call_1", "read", json!({ "path": "a.txt" })),
                    call("call 2!", "bash", json!({ "command": "ls -la", "timeout": 5 })),
                ],
            ),
            result("call_1", vec![text("file a\n\tline 2")], false),
            result("call 2!", vec![text("listing"), image("image/png", "AAAA")], true),
            assistant(
                model,
                vec![
                    thinking("", "opaque-data", true),
                    AssistantContent::Text { text: "done".into() },
                ],
            ),
            user(vec![image("image/jpeg", "BBBB"), text("what is this?")]),
            assistant(model, vec![call("call_3", "read", json!({}))]),
            result("call_3", vec![], false),
            user(vec![text("thanks")]),
        ],
        tools: vec![
            ToolSpec {
                name: "read".into(),
                description: "Read a file.".into(),
                parameters: json!({ "type": "object", "properties": { "path": { "type": "string" } } }),
            },
            ToolSpec {
                name: "bash".into(),
                description: "Run a command.".into(),
                parameters: json!({ "type": "object", "properties": { "command": { "type": "string" } } }),
            },
        ],
    }
}

fn cases() -> Vec<(&'static str, Model, RequestOptions)> {
    let chat = test_model("openai-chat");

    let mut deepseek = test_model("openai-chat");
    deepseek.compat.max_tokens_field = Some(MaxTokensField::MaxTokens);
    deepseek.compat.thinking_format = Some(ThinkingFormat::Deepseek);
    deepseek.compat.replay_thinking = Some(true);
    deepseek.thinking_levels.insert("high".into(), "high".into());

    let mut variants = test_model("openai-chat");
    variants.input = vec![Modality::Text, Modality::Image];
    variants.compat.system_role = Some(SystemRole::Developer);
    variants.compat.tool_result_name = Some(true);
    variants.compat.tool_result_images = Some(true);
    variants.compat.strict_tools = Some(true);
    variants.compat.prompt_cache_key = Some(true);
    variants.compat.stream_usage = Some(false);
    variants.compat.thinking_format = Some(ThinkingFormat::Qwen);
    let mut extra = serde_json::Map::new();
    extra.insert("top_p".into(), json!(0.9));
    let tuned = RequestOptions {
        thinking: ThinkingLevel::Low,
        max_output_tokens: Some(1000),
        temperature: Some(0.5),
        tool_choice: ToolChoice::None,
        cache_key: Some("session-1".into()),
        extra_body: extra.clone(),
    };

    let mut openrouter = test_model("openai-chat");
    openrouter.compat.thinking_format = Some(ThinkingFormat::Openrouter);

    let anthropic = test_model("anthropic-messages");
    let mut anthropic_plain = test_model("anthropic-messages");
    anthropic_plain.compat.cache_control = Some(false);
    let thinking = RequestOptions {
        thinking: ThinkingLevel::Medium,
        tool_choice: ToolChoice::None,
        extra_body: extra,
        ..Default::default()
    };

    vec![
        ("chat_default", chat, RequestOptions::default()),
        (
            "chat_deepseek",
            deepseek,
            RequestOptions {
                thinking: ThinkingLevel::High,
                ..Default::default()
            },
        ),
        ("chat_variants", variants, tuned),
        ("chat_openrouter", openrouter, RequestOptions::default()),
        ("anthropic_default", anthropic, RequestOptions::default()),
        ("anthropic_thinking_no_cache", anthropic_plain, thinking),
    ]
}

/// The value with object keys in sorted order.
fn sorted(value: Value) -> Value {
    match value {
        Value::Object(map) => {
            let mut entries: Vec<(String, Value)> = map.into_iter().collect();
            entries.sort_by(|a, b| a.0.cmp(&b.0));
            Value::Object(entries.into_iter().map(|(k, v)| (k, sorted(v))).collect())
        }
        Value::Array(items) => Value::Array(items.into_iter().map(sorted).collect()),
        other => other,
    }
}

#[test]
fn request_bodies_match_snapshots() {
    let dir = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/bodies");
    let update = std::env::var_os("HER_UPDATE_SNAPSHOTS").is_some();
    let mut mismatches = Vec::new();
    for (name, model, options) in cases() {
        let context = conversation(&model);
        let bytes = if model.api == "anthropic-messages" {
            super::anthropic::build_body(&model, &context, &options)
        } else {
            super::openai_chat::build_body(&model, &context, &options)
        };
        let body: Value = serde_json::from_slice(&bytes).expect("the body is valid JSON");
        // Keys sorted, whatever order serde_json keeps maps in for this build.
        let pretty = serde_json::to_string_pretty(&sorted(body.clone())).unwrap() + "\n";
        let path = dir.join(format!("{name}.json"));
        if update {
            std::fs::create_dir_all(&dir).unwrap();
            std::fs::write(&path, &pretty).unwrap();
        } else {
            let saved: Option<Value> = std::fs::read_to_string(&path).ok().and_then(|t| serde_json::from_str(&t).ok());
            if saved.as_ref() != Some(&body) {
                mismatches.push(format!("{name}:\n{pretty}"));
            }
        }
    }
    assert!(mismatches.is_empty(), "request bodies changed:\n{}", mismatches.join("\n"));
}
