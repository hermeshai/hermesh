//! `her models check`: real requests to one model, to see what works before relying on it
//! (standalone-cli-design §7.6). Each check costs a few dozen tokens.

use her_llm::AssistantMessage;
use her_llm::Context;
use her_llm::Message;
use her_llm::Model;
use her_llm::ModelRegistry;
use her_llm::RequestOptions;
use her_llm::StopReason;
use her_llm::StreamEvent;
use her_llm::ThinkingLevel;
use her_llm::ToolResultMessage;
use her_llm::ToolSpec;
use her_llm::UserContent;
use her_llm::UserMessage;
use futures::StreamExt as _;
use serde_json::Value;
use serde_json::json;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;

use crate::render::Style;
use crate::setup;

/// A 1×1 PNG.
const TINY_PNG: &str = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg==";

#[derive(Clone, Copy, PartialEq)]
enum Status {
    Ok,
    Failed,
    /// Worth knowing, but not a failure.
    Note,
    Skipped,
}

struct Check {
    name: String,
    status: Status,
    detail: String,
    elapsed: Duration,
}

fn echo_tool() -> ToolSpec {
    ToolSpec {
        name: "check_echo".into(),
        description: "Returns its text.".into(),
        parameters: json!({
            "type": "object",
            "properties": { "text": { "type": "string" } },
            "required": ["text"],
            "additionalProperties": false
        }),
    }
}

async fn ask(llm: &ModelRegistry, model: &Arc<Model>, context: &Context, thinking: ThinkingLevel) -> AssistantMessage {
    let options = RequestOptions { thinking, max_output_tokens: Some(2000), ..Default::default() };
    let mut stream = llm.stream(model.clone(), context.clone(), options);
    let mut last = None;
    while let Some(event) = stream.next().await {
        match event {
            StreamEvent::Done(message) | StreamEvent::Failed(message) => last = Some(message),
            _ => {}
        }
    }
    last.expect("a stream ends with done or failed")
}

fn failure_of(message: &AssistantMessage) -> Option<String> {
    (message.stop_reason == StopReason::Error)
        .then(|| message.error.as_ref().map_or_else(|| "failed".to_string(), |e| e.message.chars().take(200).collect()))
}

fn user_context(text: &str, tools: Vec<ToolSpec>) -> Context {
    Context { system: String::new(), messages: vec![Message::from(UserMessage::text(text))], tools }
}

fn tool_result(call: &her_llm::ToolCall, text: &str) -> Message {
    Message::from(ToolResultMessage {
        call_id: call.id.clone(),
        tool_name: call.name.clone(),
        content: vec![UserContent::Text { text: text.to_string() }],
        is_error: false,
        details: None,
        ts: 0,
        duration_ms: None,
    })
}

async fn timed<F: Future<Output = (Status, String)>>(name: &str, check: F) -> Check {
    let started = Instant::now();
    let (status, detail) = check.await;
    Check { name: name.to_string(), status, detail, elapsed: started.elapsed() }
}

async fn check_text(llm: &ModelRegistry, model: &Arc<Model>) -> (Status, String) {
    let message = ask(llm, model, &user_context("Reply with the single word OK.", Vec::new()), ThinkingLevel::Default).await;
    if let Some(failure) = failure_of(&message) {
        return (Status::Failed, failure);
    }
    if message.text().trim().is_empty() {
        return (Status::Failed, "an empty reply".into());
    }
    if message.usage.input == 0 && message.usage.output == 0 {
        return (Status::Note, "a reply, without usage numbers".into());
    }
    (Status::Ok, format!("{} tokens in, {} out", message.usage.input, message.usage.output))
}

async fn check_tools(llm: &ModelRegistry, model: &Arc<Model>) -> (Status, String) {
    let mut context = user_context("Call check_echo with the text \"ping\". After its result, reply with that text.", vec![echo_tool()]);
    let first = ask(llm, model, &context, ThinkingLevel::Default).await;
    if let Some(failure) = failure_of(&first) {
        return (Status::Failed, failure);
    }
    let calls: Vec<_> = first.tool_calls().into_iter().cloned().collect();
    let Some(call) = calls.first().cloned() else {
        return (Status::Failed, "the model did not call the tool".into());
    };
    if let Some(problem) = &call.arguments_error {
        return (Status::Failed, problem.clone());
    }
    context.messages.push(Message::from(first));
    for call in &calls {
        context.messages.push(tool_result(call, "ping"));
    }
    let second = ask(llm, model, &context, ThinkingLevel::Default).await;
    match failure_of(&second) {
        Some(failure) => (Status::Failed, format!("after the result: {failure}")),
        None => (Status::Ok, "called it, went on with the result".into()),
    }
}

async fn check_parallel(llm: &ModelRegistry, model: &Arc<Model>) -> (Status, String) {
    let mut context = user_context("Call check_echo twice at once: once with the text \"a\" and once with \"b\". Then say done.", vec![echo_tool()]);
    let first = ask(llm, model, &context, ThinkingLevel::Default).await;
    if let Some(failure) = failure_of(&first) {
        return (Status::Failed, failure);
    }
    let calls: Vec<_> = first.tool_calls().into_iter().cloned().collect();
    if calls.len() < 2 {
        return (Status::Note, format!("the model made {} call(s) at once", calls.len()));
    }
    context.messages.push(Message::from(first));
    for call in &calls {
        context.messages.push(tool_result(call, "ok"));
    }
    let second = ask(llm, model, &context, ThinkingLevel::Default).await;
    match failure_of(&second) {
        Some(failure) => (Status::Failed, format!("after the results: {failure}")),
        None => (Status::Ok, format!("{} calls, results matched by id", calls.len())),
    }
}

async fn check_levels(llm: &ModelRegistry, model: &Arc<Model>) -> (Status, String) {
    if model.thinking.is_empty() {
        return (Status::Skipped, "this model has no levels".into());
    }
    let mut failed = Vec::new();
    for level in &model.thinking {
        let message = ask(llm, model, &user_context("Reply with the single word OK.", Vec::new()), *level).await;
        if let Some(failure) = failure_of(&message) {
            failed.push(format!("{level}: {failure}"));
        }
    }
    if failed.is_empty() {
        let names: Vec<String> = model.thinking.iter().map(ToString::to_string).collect();
        (Status::Ok, names.join(", "))
    } else {
        (Status::Failed, failed.join("; "))
    }
}

async fn check_image(llm: &ModelRegistry, model: &Arc<Model>) -> (Status, String) {
    if !model.accepts_images() {
        return (Status::Skipped, "the catalog says this model takes no images".into());
    }
    let user = UserMessage {
        content: vec![
            UserContent::Text { text: "This is a test image. Reply with the single word OK.".into() },
            UserContent::Image { mime_type: "image/png".into(), data: TINY_PNG.into() },
        ],
        ..UserMessage::text("")
    };
    let context = Context { system: String::new(), messages: vec![Message::from(user)], tools: Vec::new() };
    let message = ask(llm, model, &context, ThinkingLevel::Default).await;
    match failure_of(&message) {
        Some(failure) => (Status::Failed, failure),
        None => (Status::Ok, "answered".into()),
    }
}

async fn check_cache(llm: &ModelRegistry, model: &Arc<Model>) -> (Status, String) {
    let padding = "Context that a cache would keep. ".repeat(200);
    let context = Context {
        system: padding,
        messages: vec![Message::from(UserMessage::text("Reply with the single word OK."))],
        tools: Vec::new(),
    };
    let first = ask(llm, model, &context, ThinkingLevel::Default).await;
    if let Some(failure) = failure_of(&first) {
        return (Status::Failed, failure);
    }
    let second = ask(llm, model, &context, ThinkingLevel::Default).await;
    if let Some(failure) = failure_of(&second) {
        return (Status::Failed, failure);
    }
    if second.usage.cache_read > 0 {
        (Status::Ok, format!("{} tokens from the cache", second.usage.cache_read))
    } else {
        (Status::Note, "no cache hit on the second request".into())
    }
}

async fn check_cancel(llm: &ModelRegistry, model: &Arc<Model>) -> (Status, String) {
    let context = user_context("Count slowly from 1 to 200, one number a line.", Vec::new());
    let options = RequestOptions { max_output_tokens: Some(2000), ..Default::default() };
    let mut stream = llm.stream(model.clone(), context, options);
    let waited = tokio::time::timeout(Duration::from_secs(60), async {
        while let Some(event) = stream.next().await {
            match event {
                event if event.is_content() => return Ok(()),
                StreamEvent::Failed(message) => return Err(failure_of(&message).unwrap_or_default()),
                StreamEvent::Done(_) => return Ok(()),
                _ => {}
            }
        }
        Ok(())
    })
    .await;
    let started = Instant::now();
    drop(stream);
    match waited {
        Err(_) => (Status::Failed, "no output within a minute".into()),
        Ok(Err(failure)) => (Status::Failed, failure),
        Ok(Ok(())) => (Status::Ok, format!("stopped in {} ms", started.elapsed().as_millis())),
    }
}

/// One real request: whether the model answers at all. For `her doctor --online`.
pub async fn answers(llm: &ModelRegistry, model: &Arc<Model>) -> Result<String, String> {
    match check_text(llm, model).await {
        (Status::Failed, detail) => Err(detail),
        (_, detail) => Ok(detail),
    }
}

pub async fn check(overrides: &[String], model: Option<String>, as_json: bool) -> anyhow::Result<u8> {
    let rt = setup::init(std::env::current_dir()?, overrides)?;
    let reference = setup::session_model(&rt, model)?;
    setup::check_credentials(&rt, &reference)?;
    let resolved = rt.llm.resolve(&reference)?;
    let model = resolved.model;
    let llm = &rt.llm;
    let checks = vec![
        timed("text", check_text(llm, &model)).await,
        timed("tools", check_tools(llm, &model)).await,
        timed("parallel tools", check_parallel(llm, &model)).await,
        timed("thinking levels", check_levels(llm, &model)).await,
        timed("images", check_image(llm, &model)).await,
        timed("cache", check_cache(llm, &model)).await,
        timed("cancel", check_cancel(llm, &model)).await,
    ];
    let ok = !checks.iter().any(|check| check.status == Status::Failed);
    if as_json {
        let rows: Vec<Value> = checks
            .iter()
            .map(|check| {
                let status = match check.status {
                    Status::Ok => "ok",
                    Status::Failed => "failed",
                    Status::Note => "note",
                    Status::Skipped => "skipped",
                };
                json!({ "name": check.name, "status": status, "detail": check.detail, "duration_ms": check.elapsed.as_millis() as u64 })
            })
            .collect();
        println!("{}", json!({ "model": reference, "ok": ok, "checks": rows }));
    } else {
        let style = Style::detect();
        println!("{reference}");
        for check in &checks {
            let mark = match check.status {
                Status::Ok => style.green("✓"),
                Status::Failed => style.red("✗"),
                Status::Note => style.yellow("!"),
                Status::Skipped => style.dim("-"),
            };
            println!("{mark} {:<16} {}  {}", check.name, check.detail, style.dim(&format!("{:.1}s", check.elapsed.as_secs_f32())));
        }
    }
    Ok(if ok { 0 } else { 1 })
}
