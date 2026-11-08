//! `her chat`: talk to a model through the model layer only (no kernel, no tools).

use futures::StreamExt;
use her_llm::AssistantMessage;
use her_llm::Context;
use her_llm::Message;
use her_llm::MessageAccumulator;
use her_llm::Model;
use her_llm::ModelRegistry;
use her_llm::RequestOptions;
use her_llm::StopReason;
use her_llm::StreamEvent;
use her_llm::ThinkingLevel;
use her_llm::ToolResultMessage;
use her_llm::ToolSpec;
use her_llm::UsageTotals;
use her_llm::UserContent;
use her_llm::UserMessage;
use her_llm::now_ms;
use serde_json::json;
use std::io::Write;
use std::sync::Arc;

use crate::render::Style;
use crate::render::usage_line;
use crate::setup;

pub struct ChatOptions {
    pub prompt: Option<String>,
    pub model: Option<String>,
    pub thinking: Option<ThinkingLevel>,
    pub system: Option<String>,
    pub tool_test: bool,
}

pub async fn chat(overrides: &[String], options: ChatOptions) -> anyhow::Result<u8> {
    let rt = setup::init(std::env::current_dir()?, overrides)?;
    let style = Style::detect();
    for warning in &rt.warnings {
        eprintln!("{}", style.yellow(&format!("warning: {warning}")));
    }
    let reference = setup::session_model(&rt, options.model.clone())?;
    setup::check_credentials(&rt, &reference)?;
    let resolved = rt.llm.resolve(&reference)?;
    if resolved.ad_hoc {
        eprintln!("{}", style.yellow(&format!("warning: '{reference}' is not in the catalog; default limits apply")));
    }
    let model = resolved.model;
    let thinking = options.thinking.unwrap_or(rt.config.thinking);
    eprintln!("{}", style.dim(&format!("her chat · {} · thinking {thinking}", model.reference())));

    let mut context = Context {
        system: options.system.clone().unwrap_or_default(),
        messages: Vec::new(),
        tools: if options.tool_test { vec![time_tool()] } else { Vec::new() },
    };
    let mut usage = UsageTotals::default();
    let one_shot = options.prompt.is_some();
    let mut pending = options.prompt.clone();
    let mut exit_code = 0;
    loop {
        let text = match pending.take() {
            Some(text) => setup::read_prompt(&text)?,
            None if one_shot => break,
            None => {
                eprint!("{}", style.cyan("you> "));
                let _ = std::io::stderr().flush();
                let mut line = String::new();
                if std::io::stdin().read_line(&mut line)? == 0 {
                    break;
                }
                let line = line.trim().to_string();
                if line == "/exit" || line == "/quit" {
                    break;
                }
                if line.is_empty() {
                    continue;
                }
                line
            }
        };
        context.messages.push(Message::from(UserMessage::text(text)));
        loop {
            let message = stream_once(&rt.llm, &model, &context, thinking, &style).await;
            usage.add(&model.reference(), &message.usage);
            if message.stop_reason == StopReason::Error {
                let error = message.error.clone().map(|e| e.to_string()).unwrap_or_default();
                eprintln!("{}", style.red(&format!("error: {error}")));
                context.messages.pop();
                exit_code = 1;
                break;
            }
            let calls: Vec<_> = message.tool_calls().into_iter().cloned().collect();
            context.messages.push(Message::from(message));
            if calls.is_empty() {
                break;
            }
            for call in calls {
                let now = chrono::Local::now().format("%Y-%m-%d %H:%M:%S %:z").to_string();
                eprintln!("{}", style.cyan(&format!("• tool call {}({}) → {now}", call.name, call.arguments)));
                context.messages.push(Message::from(ToolResultMessage {
                    call_id: call.id,
                    tool_name: call.name,
                    content: vec![UserContent::Text { text: now }],
                    is_error: false,
                    details: None,
                    ts: now_ms(),
                    duration_ms: None,
                }));
            }
        }
        if one_shot {
            break;
        }
    }
    eprintln!("{}", style.dim(&usage_line(&usage)));
    Ok(exit_code)
}

fn time_tool() -> ToolSpec {
    ToolSpec {
        name: "get_time".into(),
        description: "Get the current local date and time.".into(),
        parameters: json!({
            "type": "object",
            "properties": {
                "timezone": { "type": "string", "description": "IANA time zone, for example Asia/Shanghai" }
            },
            "additionalProperties": false
        }),
    }
}

async fn stream_once(
    llm: &ModelRegistry,
    model: &Arc<Model>,
    context: &Context,
    thinking: ThinkingLevel,
    style: &Style,
) -> AssistantMessage {
    let options = RequestOptions {
        thinking,
        ..Default::default()
    };
    let mut stream = llm.stream(model.clone(), context.clone(), options);
    let mut thinking_open = false;
    let mut text_open = false;
    while let Some(event) = stream.next().await {
        match event {
            StreamEvent::ThinkingDelta { text, .. } => {
                if !thinking_open {
                    eprint!("{}", style.dim("thinking: "));
                    thinking_open = true;
                }
                eprint!("{}", style.dim(&text));
            }
            StreamEvent::TextDelta { text, .. } => {
                if thinking_open {
                    eprintln!();
                    thinking_open = false;
                }
                print!("{text}");
                let _ = std::io::stdout().flush();
                text_open = true;
            }
            StreamEvent::Retrying { attempt, delay, error } => {
                // What was printed belongs to the failed attempt; the next one starts over.
                if std::mem::take(&mut thinking_open) {
                    eprintln!();
                }
                if std::mem::take(&mut text_open) {
                    println!();
                }
                eprintln!(
                    "{}",
                    style.yellow(&format!("retrying in {:.1}s (attempt {attempt}): {}", delay.as_secs_f64(), error.message))
                );
            }
            StreamEvent::Done(message) | StreamEvent::Failed(message) => {
                if thinking_open {
                    eprintln!();
                }
                if text_open {
                    println!();
                }
                return message;
            }
            StreamEvent::ToolCallStart { .. } | StreamEvent::ToolCallArgs { .. } | StreamEvent::ToolCallData { .. } => {}
        }
    }
    MessageAccumulator::for_model(model).fail(her_llm::LlmError::new(
        her_llm::ErrorKind::Protocol,
        "the stream ended unexpectedly",
    ))
}
