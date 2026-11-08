//! Stateless Responses transport for the restricted ChatGPT plan route.
use super::http;
use crate::stream::{LlmStream, MessageAccumulator, StreamEvent};
use crate::types::{
    AssistantContent, Context, Message, RequestOptions, StopReason, ThinkingLevel, ToolChoice, UserContent,
};
use crate::{ErrorKind, LlmError, WireApi, WireRequest};
use serde_json::{Value, json};
use std::collections::HashSet;

pub struct OpenAiResponsesApi;

fn content(parts: &[UserContent]) -> Vec<Value> {
    parts
        .iter()
        .map(|p| match p {
            UserContent::Text { text } => json!({"type":"input_text","text":text}),
            UserContent::Image { mime_type, data } => {
                json!({"type":"input_image","image_url":format!("data:{mime_type};base64,{data}")})
            }
        })
        .collect()
}

pub fn build_body(model: &crate::Model, context: &Context, options: &RequestOptions) -> Value {
    let mut input = Vec::new();
    // A call recorded without a name can never be sent (`name` must be non-empty): leave it out,
    // with its result, rather than fail every later turn of the conversation.
    let nameless: HashSet<&str> = context
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::Assistant(m) => Some(&m.content),
            _ => None,
        })
        .flatten()
        .filter_map(|p| match p {
            AssistantContent::ToolCall(t) if t.name.is_empty() => Some(t.id.as_str()),
            _ => None,
        })
        .collect();
    for message in &context.messages {
        match message {
            Message::User(m) => input.push(json!({"role":"user","content":content(&m.content)})),
            Message::ToolResult(m) if nameless.contains(m.call_id.as_str()) => {}
            Message::ToolResult(m) => {
                input.push(json!({"type":"function_call_output","call_id":m.call_id,"output":content(&m.content)}))
            }
            Message::Assistant(m) => {
                for part in &m.content {
                    match part {
                        AssistantContent::Text { text } => input.push(json!({
                            "role": "assistant", "content": [{"type": "output_text", "text": text}]
                        })),
                        AssistantContent::ToolCall(t) if t.name.is_empty() => {}
                        AssistantContent::ToolCall(t) => input.push(json!({
                            "type": "function_call", "call_id": t.id, "namespace": "her",
                            "name": t.name, "arguments": t.arguments.as_str()
                        })),
                        AssistantContent::Thinking { signature: Some(s), .. } => {
                            // The complete reasoning item, including its ID and encrypted content.
                            if let Ok(item) = serde_json::from_str::<Value>(s)
                                && item["type"] == "reasoning"
                            {
                                input.push(item);
                            }
                        }
                        _ => {}
                    }
                }
            }
        }
    }
    let mut body = json!({"model":model.id,"instructions":context.system,"input":input,"store":false,"stream":true});
    if !context.tools.is_empty() {
        let tools: Vec<Value> = context.tools.iter().map(|t| json!({"type":"function","name":t.name,"description":t.description,"parameters":t.parameters,"strict":false})).collect();
        body["tools"] = json!([{"type":"namespace","name":"her","description":"Tools executed by the her runtime","tools":tools}]);
        body["tool_choice"] = json!(if options.tool_choice == ToolChoice::None {
            "none"
        } else {
            "auto"
        });
    }
    if options.thinking != ThinkingLevel::Default && model.thinking.contains(&options.thinking) {
        let effort = if options.thinking == ThinkingLevel::Off {
            "none".to_string()
        } else {
            options.thinking.to_string()
        };
        body["reasoning"] = json!({"effort":effort});
    }
    if let Some(key) = &options.cache_key {
        body["prompt_cache_key"] = json!(key);
    }
    // In particular, never merge extra_body into the subscription route: it could re-enable
    // storage, hosted tools or unsupported sampling parameters.
    body
}

fn classify(mut error: LlmError) -> LlmError {
    match error.provider_code.as_deref() {
        Some("subscription_sharing_usage_limit_exceeded") => {
            error.kind = ErrorKind::QuotaExceeded;
            error.message = "ChatGPT plan usage limit reached. Manage usage in ChatGPT settings.".into();
        }
        Some(
            "subscription_sharing_user_not_eligible"
            | "chatpass_v2_scope_not_authorized"
            | "chatpass_v2_invalid_authorization_context",
        ) => {
            error.kind = ErrorKind::InvalidRequest;
            error.message = "This ChatGPT account or workspace has not authorized plan usage.".into();
        }
        Some("subscription_sharing_usage_unavailable" | "subscription_sharing_user_unavailable") => {
            error.kind = ErrorKind::Server;
            error.message = "ChatGPT plan usage is temporarily unavailable; retry later.".into();
        }
        Some("subscription_sharing_unsupported_capability" | "subscription_sharing_route_not_supported") => {
            error.kind = ErrorKind::InvalidRequest;
        }
        Some("subscription_sharing_invalid_user") => error.kind = ErrorKind::Auth,
        _ => {}
    }
    error
}
fn protocol(message: &str) -> LlmError {
    LlmError::new(ErrorKind::Protocol, message)
}
fn text(v: &Value, key: &str) -> String {
    v[key].as_str().unwrap_or_default().to_string()
}
fn apply(acc: &mut MessageAccumulator, event: StreamEvent) {
    acc.apply(&event);
}
fn item(acc: &mut MessageAccumulator, index: usize, v: &Value) {
    match v["type"].as_str() {
        Some("message") => {
            if let Some(parts) = v["content"].as_array() {
                for p in parts {
                    if p["type"] == "output_text" {
                        apply(
                            acc,
                            StreamEvent::TextDelta {
                                index,
                                text: text(p, "text"),
                            },
                        );
                    }
                    if p["type"] == "refusal" {
                        apply(
                            acc,
                            StreamEvent::TextDelta {
                                index,
                                text: text(p, "refusal"),
                            },
                        );
                    }
                }
            }
        }
        Some("reasoning") => {
            acc.ensure_thinking(index);
            if let Some(parts) = v["summary"].as_array() {
                for p in parts {
                    apply(
                        acc,
                        StreamEvent::ThinkingDelta {
                            index,
                            text: text(p, "text"),
                        },
                    );
                }
            }
            acc.append_thinking_signature(index, &v.to_string());
        }
        Some("function_call") => {
            apply(
                acc,
                StreamEvent::ToolCallStart {
                    index,
                    id: text(v, "call_id"),
                    name: text(v, "name"),
                },
            );
            apply(
                acc,
                StreamEvent::ToolCallArgs {
                    index,
                    json: text(v, "arguments"),
                },
            );
        }
        _ => {}
    }
}
/// What `item` passes over in a response's output, by type, none of its content: a new kind of
/// item or part must not vanish in silence.
fn unreadable(output: &[Value]) -> Vec<String> {
    let mut found = Vec::new();
    for v in output {
        let kind = v["type"].as_str().unwrap_or("?");
        match kind {
            "message" => {
                for p in v["content"].as_array().into_iter().flatten() {
                    let part = p["type"].as_str().unwrap_or("?");
                    if part != "output_text" && part != "refusal" {
                        found.push(format!("message/{part}"));
                    }
                }
            }
            "reasoning" | "function_call" => {}
            _ => found.push(kind.to_string()),
        }
    }
    found
}
impl WireApi for OpenAiResponsesApi {
    fn id(&self) -> &'static str {
        "openai-responses"
    }
    fn stream(&self, req: WireRequest) -> LlmStream {
        Box::pin(async_stream::stream! {
            let mut acc = MessageAccumulator::for_model(&req.model);
            let body = build_body(&req.model, &req.context, &req.options);
            let mut request = req.http.post(format!("{}/responses",req.model.base_url.trim_end_matches('/'))).json(&body).header("Accept","text/event-stream");
            if let Some(key) = &req.credentials.api_key { request = request.bearer_auth(key); }
            for (k,v) in req.model.headers.iter().chain(req.credentials.headers.iter()) { request = request.header(k,v); }
            let response = match http::send(request,&req.timeouts,&req.model.compat.overflow_patterns()).await {
                Ok(r) => r,
                Err(e) => { yield StreamEvent::Failed(acc.fail(classify(e))); return; }
            };
            let mut events = http::sse_events(response,req.timeouts.idle);
            let mut streamed = HashSet::new();
            let mut passed_over = Vec::new();
            loop {
                let data = match events.next().await {
                    Ok(Some(e)) => e.data,
                    Ok(None) => { yield StreamEvent::Failed(acc.fail(protocol("Responses stream ended without response.completed"))); return; }
                    Err(e) => { yield StreamEvent::Failed(acc.fail(e)); return; }
                };
                let v: Value = match serde_json::from_str(&data) {
                    Ok(v) => v,
                    Err(_) => { yield StreamEvent::Failed(acc.fail(protocol("Invalid Responses event"))); return; }
                };
                let index = v["output_index"].as_u64().unwrap_or_default() as usize;
                if index > 10000 { yield StreamEvent::Failed(acc.fail(protocol("Invalid Responses output index"))); return; }
                let event = match v["type"].as_str().unwrap_or_default() {
                    "response.output_text.delta" | "response.refusal.delta" => Some(StreamEvent::TextDelta { index, text:text(&v,"delta") }),
                    "response.reasoning_summary_text.delta" => Some(StreamEvent::ThinkingDelta { index, text:text(&v,"delta") }),
                    "response.output_item.added" if v["item"]["type"] == "function_call" => Some(StreamEvent::ToolCallStart { index, id:text(&v["item"],"call_id"), name:text(&v["item"],"name") }),
                    "response.function_call_arguments.delta" => Some(StreamEvent::ToolCallArgs { index, json:text(&v,"delta") }),
                    "response.output_item.done" => {
                        if v["item"]["type"] == "reasoning" { acc.append_thinking_signature(index, &v["item"].to_string()); }
                        else if !streamed.contains(&index) { item(&mut acc,index,&v["item"]); }
                        else if v["item"]["type"] == "function_call" {
                            // `output_item.added` may name the call blank; the finished item is whole.
                            acc.apply(&StreamEvent::ToolCallStart { index, id:text(&v["item"],"call_id"), name:text(&v["item"],"name") });
                        }
                        // The subscription route's `response.completed` lists no items: this is its
                        // only word on what the reply held.
                        passed_over.extend(unreadable(std::slice::from_ref(&v["item"])));
                        None
                    }
                    "response.completed" => {
                        let r = &v["response"];
                        if r["status"].as_str().is_some_and(|s| s != "completed") { yield StreamEvent::Failed(acc.fail(protocol("Response did not complete"))); return; }
                        // An empty list says nothing about what was streamed.
                        if let Some(output) = r["output"].as_array().filter(|o| !o.is_empty()) {
                            // Terminal items are authoritative (also capture non-streamed arguments
                            // and encrypted reasoning). Display deltas remain incremental. An answer
                            // that lists none has said it all in what was streamed.
                            acc = MessageAccumulator::for_model(&req.model);
                            for (i,v) in output.iter().enumerate() { item(&mut acc,i,v); }
                            passed_over = unreadable(output);
                        }
                        if !passed_over.is_empty() { tracing::warn!(model=%req.model.id, items=?passed_over, "passed over Responses output"); }
                        acc.response_id = r["id"].as_str().map(String::from);
                        let u = &r["usage"];
                        acc.usage.cache_read = u["input_tokens_details"]["cached_tokens"].as_u64().unwrap_or_default();
                        acc.usage.input = u["input_tokens"].as_u64().unwrap_or_default().saturating_sub(acc.usage.cache_read);
                        acc.usage.output = u["output_tokens"].as_u64().unwrap_or_default();
                        acc.usage.reasoning = u["output_tokens_details"]["reasoning_tokens"].as_u64();
                        let message = acc.finish(StopReason::Stop);
                        // Nothing here because her could not read it is a failure. An empty reply
                        // is not: the model chose to say nothing, as after handing work to a
                        // background agent, and the layers above judge whether that will do.
                        if message.content.is_empty() && !passed_over.is_empty() {
                            yield StreamEvent::Failed(acc.fail(protocol(&format!("The model's reply has nothing her can read: {}", passed_over.join(", "))))); return;
                        }
                        if message.tool_calls().iter().any(|t| t.id.is_empty() || t.name.is_empty() || t.arguments_error.is_some()) {
                            yield StreamEvent::Failed(acc.fail(protocol("Invalid Responses tool call"))); return;
                        }
                        yield StreamEvent::Done(message); return;
                    }
                    "response.failed" | "error" => {
                        let e = if v["response"]["error"].is_object() { &v["response"]["error"] } else if v["error"].is_object() { &v["error"] } else { &v };
                        yield StreamEvent::Failed(acc.fail(classify(LlmError::from_stream_error(e,&req.model.compat.overflow_patterns())))); return;
                    }
                    "response.incomplete" => { yield StreamEvent::Failed(acc.fail(protocol("Response incomplete"))); return; }
                    _ => None,
                };
                if let Some(e) = event { streamed.insert(index); acc.apply(&e); yield e; }
            }
        })
    }
}
