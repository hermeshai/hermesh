// Live diagnostics report progress on stderr.
#![allow(clippy::print_stderr)]
use super::*;
use her_llm::{
    AssistantContent, AssistantMessage, AuthConfig, Compat, Context, Credentials, Message,
    ProviderConfig, RequestOptions, StopReason, ThinkingLevel, ToolCall, ToolResultMessage,
    ToolSpec, UserContent, UserMessage,
};
use futures::StreamExt;
use std::collections::BTreeMap;
use wiremock::{
    Mock, MockServer, ResponseTemplate,
    matchers::{method, path},
};

fn entry() -> Value {
    json!({
        "key": "qfmodel", "display_name": "Qwen3.8-Flash", "enable": true, "format": "openai",
        "is_vl": true, "is_reasoning": true, "source": "system", "max_input_tokens": 200000,
        "minimal_version": "0.4.0", "prices": {"input": 1},
        "context_config": {"200K": {"is_default": true, "token_count": 200000}, "400K": {"token_count": 400000}, "1M": {"token_count": 1000000}},
        "thinking_config": {"enabled": {"efforts": {"low": {}, "high": {"is_default": true}}}},
    })
}

fn model(base: &str) -> Arc<her_llm::Model> {
    let provider = ProviderConfig {
        plan: None,
        id: "qoder".into(),
        name: "Qoder".into(),
        names: Default::default(),
        api: "openai-chat".into(),
        base_url: base.into(),
        auth: AuthConfig::Managed,
        headers: BTreeMap::new(),
        currency: Default::default(),
        compat: Compat::default(),
        listing: Default::default(),
    };
    let mut model = provider.ad_hoc_model("qfmodel");
    model.reasoning = true;
    model.context_window = 200_000;
    model.thinking = vec![ThinkingLevel::Low, ThinkingLevel::High];
    Arc::new(model)
}

fn request(base: &str, context: Context, options: RequestOptions) -> WireRequest {
    WireRequest {
        model: model(base),
        credentials: Credentials::with_key("job-token".into()),
        context: Arc::new(context),
        options,
        http: her_llm::ModelRegistry::new(
            her_llm::Catalog::builtin().unwrap(),
            Default::default(),
        )
        .http_client()
        .unwrap(),
        timeouts: Default::default(),
    }
}

fn tool() -> ToolSpec {
    ToolSpec {
        name: "read".into(),
        description: "Read a file".into(),
        parameters: json!({"type": "object", "properties": {"path": {"type": "string"}, "limit": {"type": "integer"}}}),
    }
}

fn assistant(content: Vec<AssistantContent>) -> Message {
    Message::Assistant(Arc::new(AssistantMessage {
        content,
        provider: "qoder".into(),
        api: "openai-chat".into(),
        model: "qfmodel".into(),
        response_id: None,
        usage: Default::default(),
        stop_reason: StopReason::ToolUse,
        error: None,
        ts: 1,
        duration_ms: None,
    }))
}

fn legacy(base: &str) -> Legacy {
    Legacy {
        base: base.into(),
        uid: "uid-1".into(),
        machine: "5c8f9a8e-0000-4000-8000-000000000000".into(),
        targets: Arc::new(parking_lot::RwLock::new(HashMap::from([(
            "qfmodel".into(),
            Target::from_entry(&entry()).unwrap(),
        )]))),
        queue: Arc::default(),
    }
}

fn sse(lines: &[Value]) -> String {
    let mut out = String::new();
    for body in lines {
        let body = if body.is_string() {
            body.as_str().unwrap().to_owned()
        } else {
            body.to_string()
        };
        out.push_str(&format!(
            "data:{}\n\n",
            json!({"statusCodeValue": 200, "body": body})
        ));
    }
    out
}

#[test]
fn targets_carry_only_the_entry_fields_the_app_sends() {
    let target = Target::from_entry(&entry()).unwrap();
    assert_eq!(target.tier_for(200_000), Some(200_000));
    assert_eq!(target.tier_for(250_000), Some(400_000));
    assert_eq!(target.tier_for(5_000_000), Some(1_000_000));
    assert_eq!(
        target.model_config(false),
        json!({"key": "qfmodel", "display_name": "Qwen3.8-Flash", "format": "openai", "is_vl": true,
               "is_reasoning": false, "api_key": "", "url": "", "source": "system", "max_input_tokens": 200000, "model": ""})
    );
    assert!(Target::from_entry(&json!({"key": "default"})).is_none());
    assert_eq!(
        Target::from_entry(&json!({"key": "x"}))
            .unwrap()
            .tier_for(100),
        None
    );
}

#[test]
fn the_body_is_the_apps_envelope_around_the_kernels_turns() {
    let call = ToolCall {
        id: "call_1".into(),
        name: "read".into(),
        arguments: her_llm::JsonText::from_value(&json!({"path": "a.txt"})),
        arguments_error: None,
        provider_data: None,
    };
    let context = Context {
        system: "her system".into(),
        messages: vec![
            Message::from(UserMessage::text("请读取 a.txt 并总结")),
            assistant(vec![
                AssistantContent::Thinking {
                    text: "need the file".into(),
                    signature: Some("reasoning_content".into()),
                    redacted: false,
                },
                AssistantContent::ToolCall(call),
            ]),
            Message::ToolResult(Arc::new(ToolResultMessage {
                call_id: "call_1".into(),
                tool_name: "read".into(),
                content: vec![UserContent::Text {
                    text: "file text".into(),
                }],
                is_error: false,
                details: None,
                ts: 2,
                duration_ms: None,
            })),
        ],
        tools: vec![tool()],
    };
    let options = RequestOptions {
        thinking: ThinkingLevel::Low,
        max_output_tokens: Some(4096),
        cache_key: Some("session-1".into()),
        ..Default::default()
    };
    let req = request("http://unused", context, options);
    let target = Target::from_entry(&entry()).unwrap();
    let ids = body::ids(&req);
    let built = body::build(&req, &target, &ids, "req-1");
    assert_eq!(built["request_id"], "req-1");
    assert_eq!(built["chat_record_id"], "req-1");
    assert_eq!(built["request_set_id"], ids.task);
    assert_eq!(built["session_id"], ids.session);
    assert_eq!(
        built["parameters"],
        json!({"enable_thinking": true, "max_tokens": 4096, "reasoning_effort": "low", "context_length": 200000})
    );
    assert_eq!(
        built["business"]["stage"], "processing",
        "a call of the turn was answered"
    );
    assert_eq!(
        built["business"]["name"], "请读取 a.txt ",
        "the prompt's first ten characters"
    );
    assert_eq!(built["model_config"]["is_reasoning"], true);
    assert_eq!(
        built["system"],
        json!([{"type": "text", "text": "her system"}])
    );
    let messages = built["messages"].as_array().unwrap();
    assert_eq!(
        messages[0],
        json!({"role": "system", "content": [{"type": "text", "text": "her system"}]})
    );
    assert_eq!(
        messages[1]["content"],
        json!([{"type": "text", "text": "请读取 a.txt 并总结"}])
    );
    assert_eq!(messages[1]["contents"], messages[1]["content"]);
    assert_eq!(messages[2]["reasoning_content"], "need the file");
    assert_eq!(messages[2]["tool_calls"][0]["function"]["name"], "read");
    assert_eq!(
        messages[3],
        json!({"role": "tool", "tool_call_id": "call_1", "content": "file text"})
    );
    assert!(
        !built.to_string().contains("cache_control"),
        "the last turn is a tool's"
    );
    assert_eq!(built["tools"][0]["function"]["name"], "read");
    // The same conversation keeps its ids; another prompt is another task of the session.
    assert_eq!(body::ids(&req).task, ids.task);
    let mut next = (*req.context).clone();
    next.messages
        .push(Message::from(UserMessage::text("再总结一次")));
    let next = request(
        "http://unused",
        next,
        RequestOptions {
            cache_key: Some("session-1".into()),
            ..Default::default()
        },
    );
    let next_ids = body::ids(&next);
    assert_eq!(next_ids.session, ids.session);
    assert_ne!(next_ids.task, ids.task);
    let first = body::build(&next, &target, &next_ids, "req-2");
    assert_eq!(first["business"]["stage"], "start");
    let last = first["messages"]
        .as_array()
        .unwrap()
        .last()
        .unwrap()
        .clone();
    assert_eq!(
        last["content"][0]["cache_control"],
        json!({"type": "ephemeral"})
    );
    assert_eq!(last["contents"], last["content"]);
}

#[test]
fn replies_give_thinking_text_and_native_calls_in_order() {
    let mut reply = reply::Reply::new(&[tool()]);
    let mut events = Vec::new();
    for chunk in [
        json!({"choices": [{"delta": {"reasoning_content": "think"}}]}),
        json!({"choices": [{"delta": {"content": "Let me read."}}]}),
        // Arguments before the name are held until the call is named.
        json!({"choices": [{"delta": {"tool_calls": [{"index": 0, "id": "tc_1", "function": {"arguments": "{\"pa"}}]}}]}),
        json!({"choices": [{"delta": {"tool_calls": [{"index": 0, "function": {"name": "read", "arguments": "th\":\"a\"}"}}]}}]}),
        json!({"choices": [{"delta": {}, "finish_reason": "tool_calls"}]}),
        json!({"usage": {"prompt_tokens": 100, "completion_tokens": 20, "prompt_tokens_details": {"cached_tokens": 60}, "completion_tokens_details": {"reasoning_tokens": 5}}}),
    ] {
        let line = format!(
            "data:{}",
            json!({"statusCodeValue": 200, "body": chunk.to_string()})
        );
        let reply::Step::Events(more) = reply.line(&line) else {
            panic!()
        };
        events.extend(more);
    }
    assert!(matches!(
        reply.line(&format!("data:{}", json!({"body": "[DONE]"}))),
        reply::Step::Done
    ));
    let mut acc = MessageAccumulator::new("qoder", "openai-chat", "qfmodel");
    for event in &events {
        acc.apply(event);
    }
    reply::apply_usage(&mut acc, &reply);
    let message = acc.finish(reply.stop());
    assert_eq!(message.stop_reason, StopReason::ToolUse);
    assert!(
        matches!(&message.content[0], AssistantContent::Thinking { text, .. } if text == "think")
    );
    assert!(
        matches!(&message.content[1], AssistantContent::Text { text } if text == "Let me read.")
    );
    let calls = message.tool_calls();
    assert_eq!(
        (calls[0].id.as_str(), calls[0].name.as_str()),
        ("tc_1", "read")
    );
    assert_eq!(calls[0].arguments.to_value(), json!({"path": "a"}));
    assert_eq!(
        (
            message.usage.input,
            message.usage.cache_read,
            message.usage.output,
            message.usage.reasoning
        ),
        (40, 60, 20, Some(5))
    );
}

fn texts_and_calls(chunks: &[&str]) -> (String, Vec<(String, Value)>) {
    let mut reply = reply::Reply::new(&[tool()]);
    let mut events = Vec::new();
    for chunk in chunks {
        let body = json!({"choices": [{"delta": {"content": chunk}}]}).to_string();
        let reply::Step::Events(more) = reply.line(&format!(
            "data:{}",
            json!({"statusCodeValue": 200, "body": body})
        )) else {
            panic!()
        };
        events.extend(more);
    }
    events.extend(reply.flush());
    let mut acc = MessageAccumulator::new("qoder", "openai-chat", "qfmodel");
    for event in &events {
        acc.apply(event);
    }
    let message = acc.finish(reply.stop());
    let calls = message
        .tool_calls()
        .iter()
        .map(|c| (c.name.clone(), c.arguments.to_value()))
        .collect();
    (message.text(), calls)
}

#[test]
fn text_form_calls_of_declared_tools_become_calls() {
    let (text, calls) = texts_and_calls(&[
        "读一下。<tool",
        "_call>{\"name\": \"read\", \"arguments\": {\"path\": \"a.txt\"}}</tool_call>",
    ]);
    assert_eq!(text, "读一下。");
    assert_eq!(calls, vec![("read".to_owned(), json!({"path": "a.txt"}))]);
    let (_, calls) = texts_and_calls(&[
        "<tool_call>\n<function=read>\n<parameter=path>\nb.txt\n</parameter>\n<parameter=limit>\n20\n</parameter>\n</function>\n</tool_call>",
    ]);
    assert_eq!(
        calls,
        vec![("read".to_owned(), json!({"path": "b.txt", "limit": 20}))]
    );
    // An undeclared tool, a quoted opener and an unclosed one stay text.
    let (text, calls) =
        texts_and_calls(&["<tool_call>{\"name\": \"rm\", \"arguments\": {}}</tool_call>"]);
    assert!(calls.is_empty() && text.contains("\"rm\""));
    let (text, calls) = texts_and_calls(&[
        "例如 `<tool_call>{\"name\": \"read\", \"arguments\": {\"path\": \"x\"}}</tool_call>` 这样",
    ]);
    assert!(calls.is_empty() && text.contains("<tool_call>"));
    let (text, calls) = texts_and_calls(&[
        "```\n<tool_call>{\"name\": \"read\", \"arguments\": {}}</tool_call>\n```",
    ]);
    assert!(calls.is_empty() && text.contains("<tool_call>"));
    let (text, calls) = texts_and_calls(&["结尾 <tool_call>{\"name\""]);
    assert!(calls.is_empty() && text.ends_with("{\"name\""));
}

#[test]
fn failures_say_what_qoder_meant() {
    let queued = reply::failure(
        403,
        r#"{"code":"403","message":"{\"code\":\"10605\",\"message\":\"{\\\"isQueued\\\":true,\\\"modelKey\\\":\\\"qfmodel\\\",\\\"queueType\\\":\\\"p3\\\",\\\"retryAfterSeconds\\\":12,\\\"serviceAvailable\\\":false}\"}"}"#,
    );
    assert_eq!(
        (
            queued.kind,
            queued.provider_code.as_deref(),
            queued.retry_after_ms
        ),
        (ErrorKind::Overloaded, Some("qoder_queued"), Some(12_000))
    );
    let big = reply::failure(
        413,
        r#"{"message":"Request body size exceeds maximum allowed 52428800 bytes"}"#,
    );
    assert_eq!(
        (big.kind, big.provider_code.as_deref()),
        (ErrorKind::ContextOverflow, Some("request_too_large"))
    );
    assert_eq!(
        reply::failure(400, r#"{"code":"430","message":"prompt is too long"}"#).kind,
        ErrorKind::ContextOverflow
    );
    assert_eq!(
        reply::failure(429, r#"{"code":"113","message":"User quota exhausted"}"#).kind,
        ErrorKind::QuotaExceeded
    );
    assert_eq!(
        reply::failure(400, r#"{"message":"Error in upstream response"}"#).kind,
        ErrorKind::Server
    );
    let lapsed = reply::failure(403, r#"{"code":"105","message":"Login expired"}"#);
    assert_eq!((lapsed.kind, lapsed.status), (ErrorKind::Auth, Some(401)));
    assert_eq!(
        reply::failure(403, r#"{"code":"101","message":"signature invalid"}"#).kind,
        ErrorKind::Protocol
    );
    let state = queue::state_of(r#"{"code":"403","message":"{\"code\":\"10605\",\"message\":\"{\\\"isQueued\\\":true,\\\"queueType\\\":\\\"p3\\\",\\\"retryAfterSeconds\\\":30}\"}"}"#).unwrap();
    assert_eq!(
        state,
        queue::State {
            queued: true,
            queue_type: Some("p3".into()),
            retry_after: Some(30.0)
        }
    );
    assert!(
        !queue::state_of(r#"{"isQueued":false,"retryAfterSeconds":2,"serviceAvailable":true}"#)
            .unwrap()
            .queued
    );
}

#[tokio::test]
async fn a_chat_is_signed_encoded_and_read_back() {
    let server = MockServer::start().await;
    let reply = sse(&[
        json!({"choices": [{"delta": {"content": "在线"}}]}),
        json!({"choices": [{"delta": {}, "finish_reason": "stop"}]}),
        json!("[DONE]"),
    ]);
    Mock::given(method("POST"))
        .and(path("/algo/api/v2/service/pro/sse/agent_chat_generation"))
        .respond_with(
            ResponseTemplate::new(200)
                .set_body_string(reply)
                .insert_header("content-type", "text/event-stream"),
        )
        .expect(1)
        .mount(&server)
        .await;
    let context = Context {
        system: "her".into(),
        messages: vec![Message::from(UserMessage::text("在吗"))],
        tools: vec![],
    };
    let events: Vec<_> = legacy(&server.uri())
        .stream(request(&server.uri(), context, RequestOptions::default()))
        .collect()
        .await;
    let Some(StreamEvent::Done(message)) = events.last() else {
        panic!("{events:?}")
    };
    assert_eq!(message.text(), "在线");
    let sent = &server.received_requests().await.unwrap()[0];
    assert_eq!(
        sent.url.query(),
        Some("FetchKeys=llm_model_result&AgentId=agent_common&Encode=1")
    );
    let header = |name: &str| {
        sent.headers
            .get(name)
            .map(|v| v.to_str().unwrap().to_owned())
    };
    assert!(header("authorization").unwrap().starts_with("Bearer COSY."));
    assert_eq!(header("x-model-key").as_deref(), Some("qfmodel"));
    assert_eq!(header("accept").as_deref(), Some("text/event-stream"));
    assert!(header("cosy-clientip").is_none() && header("cosy-machinetoken").is_none());
    let plain: Value =
        serde_json::from_str(&codec::decode(std::str::from_utf8(&sent.body).unwrap()).unwrap())
            .unwrap();
    assert_eq!(plain["model_config"]["key"], "qfmodel");
    assert_eq!(plain["messages"][1]["content"][0]["text"], "在吗");
}

#[tokio::test]
async fn a_queued_task_waits_for_its_turn_then_releases_its_lease() {
    let server = MockServer::start().await;
    let queued = r#"{"code":"403","message":"{\"code\":\"10605\",\"message\":\"{\\\"isQueued\\\":true,\\\"modelKey\\\":\\\"qfmodel\\\",\\\"queueType\\\":\\\"p3\\\",\\\"retryAfterSeconds\\\":1,\\\"serviceAvailable\\\":false}\"}"}"#;
    Mock::given(method("POST"))
        .and(path("/algo/api/v2/service/pro/sse/agent_chat_generation"))
        .respond_with(ResponseTemplate::new(403).set_body_string(queued))
        .up_to_n_times(1)
        .mount(&server)
        .await;
    Mock::given(method("POST"))
        .and(path("/algo/api/v2/service/pro/sse/agent_chat_generation"))
        .respond_with(ResponseTemplate::new(200).set_body_string(sse(&[
            json!({"choices": [{"delta": {"content": "OK"}, "finish_reason": "stop"}]}),
            json!("[DONE]"),
        ])))
        .mount(&server)
        .await;
    // Still queued when first asked; ready when asked again.
    Mock::given(method("GET"))
        .and(path("/algo/api/v2/service/ask/queue/status"))
        .respond_with(ResponseTemplate::new(200).set_body_json(
            json!({"isQueued": true, "retryAfterSeconds": 1, "serviceAvailable": false}),
        ))
        .up_to_n_times(1)
        .mount(&server)
        .await;
    Mock::given(method("GET"))
        .and(path("/algo/api/v2/service/ask/queue/status"))
        .respond_with(ResponseTemplate::new(200).set_body_json(
            json!({"isQueued": false, "retryAfterSeconds": 0.5, "serviceAvailable": true}),
        ))
        .mount(&server)
        .await;
    Mock::given(method("POST"))
        .and(path("/algo/api/v2/service/ask/finish"))
        .respond_with(ResponseTemplate::new(200).set_body_json(json!({"success": true})))
        .expect(1)
        .mount(&server)
        .await;
    let route = legacy(&server.uri());
    let context = Context {
        system: String::new(),
        messages: vec![Message::from(UserMessage::text("hi"))],
        tools: vec![],
    };
    let options = RequestOptions {
        cache_key: Some("s".into()),
        ..Default::default()
    };
    // The first attempt ends queued, with the queue's delay for the kernel's retry.
    let first: Vec<_> = route
        .stream(request(&server.uri(), context.clone(), options.clone()))
        .collect()
        .await;
    let Some(StreamEvent::Failed(failed)) = first.last() else {
        panic!("{first:?}")
    };
    let error = failed.error.as_ref().unwrap();
    assert_eq!(
        (error.kind, error.retry_after_ms),
        (ErrorKind::Overloaded, Some(1_000))
    );
    // The retry asks the queue, which is ready, and goes through under the same task.
    let second: Vec<_> = route
        .stream(request(&server.uri(), context, options))
        .collect()
        .await;
    assert!(
        matches!(second.last(), Some(StreamEvent::Done(m)) if m.text() == "OK"),
        "{second:?}"
    );
    let requests = server.received_requests().await.unwrap();
    let tasks: Vec<String> = requests
        .iter()
        .filter(|r| r.url.path().ends_with("agent_chat_generation"))
        .map(|r| {
            serde_json::from_str::<Value>(
                &codec::decode(std::str::from_utf8(&r.body).unwrap()).unwrap(),
            )
            .unwrap()["request_set_id"]
                .as_str()
                .unwrap()
                .to_owned()
        })
        .collect();
    assert_eq!(tasks.len(), 2);
    assert_eq!(tasks[0], tasks[1]);
    let status = requests
        .iter()
        .find(|r| r.url.path().ends_with("queue/status"))
        .unwrap();
    assert!(status.url.query().unwrap().contains("queueType=p3"));
    let finish = requests
        .iter()
        .find(|r| r.url.path().ends_with("ask/finish"))
        .unwrap();
    let released: Value =
        serde_json::from_str(&codec::decode(std::str::from_utf8(&finish.body).unwrap()).unwrap())
            .unwrap();
    let payload: Value = serde_json::from_str(released["payload"].as_str().unwrap()).unwrap();
    assert_eq!(
        (
            payload["request_set_id"].as_str(),
            payload["model_key"].as_str()
        ),
        (Some(tasks[0].as_str()), Some("qfmodel"))
    );
}

#[tokio::test]
async fn an_empty_catalog_is_read_once_before_the_first_legacy_chat() {
    let server = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path("/algo/api/v2/model/list"))
        .respond_with(
            ResponseTemplate::new(200)
                .set_body_json(json!({"app": [entry(), {"key": "off", "enable": false}]})),
        )
        .expect(1)
        .mount(&server)
        .await;
    Mock::given(method("POST"))
        .and(path("/algo/api/v2/service/pro/sse/agent_chat_generation"))
        .respond_with(ResponseTemplate::new(200).set_body_string(sse(&[
            json!({"choices": [{"delta": {"content": "OK"}, "finish_reason": "stop"}]}),
            json!("[DONE]"),
        ])))
        .expect(2)
        .mount(&server)
        .await;
    let mut route = legacy(&server.uri());
    route.targets = Arc::default();
    let context = Context {
        system: String::new(),
        messages: vec![Message::from(UserMessage::text("hi"))],
        tools: vec![],
    };
    for _ in 0..2 {
        let events: Vec<_> = route
            .stream(request(
                &server.uri(),
                context.clone(),
                RequestOptions::default(),
            ))
            .collect()
            .await;
        assert!(
            matches!(events.last(), Some(StreamEvent::Done(m)) if m.text() == "OK"),
            "{events:?}"
        );
    }
    assert_eq!(
        route.targets.read().keys().collect::<Vec<_>>(),
        vec!["qfmodel"]
    );
}

#[test]
fn a_sessions_next_task_ends_its_earlier_ones() {
    let queue = Queue::default();
    queue.enter("task-1", "qfmodel", Some("p3".into()), "req-1", "session-1");
    queue.admitted("task-1");
    queue.enter("other", "qfmodel", None, "req-2", "session-2");
    // The same task carries on: nothing is over.
    assert!(queue.over("session-1", "task-1").is_empty());
    let over = queue.over("session-1", "task-2");
    assert_eq!(
        over.iter().map(|p| p.task.as_str()).collect::<Vec<_>>(),
        vec!["task-1"]
    );
    assert!(queue.place("task-1").is_none() && queue.place("other").is_some());
    // A turn that ends releases only a lease it holds.
    assert!(queue.end("other").is_none(), "still waiting");
    queue.admitted("other");
    assert_eq!(queue.end("other").map(|p| p.task), Some("other".into()));
}

/// The legacy route through Manager -> registry -> route, with an existing connection
/// (`HER_QODER_TEST_HOME`): a prompt the gateway would refuse for its size is answered, and a
/// tool call and its result go through. The registry's own retry waits out Qoder's queue, so a
/// run can take minutes.
#[tokio::test]
#[ignore = "sends billable live requests and may wait in Qoder's queue"]
async fn live_qoder_legacy() {
    use her_llm::{Catalog, ModelRegistry, RegistryOptions};
    let home =
        std::path::PathBuf::from(std::env::var_os("HER_QODER_TEST_HOME").expect("existing home"));
    let env = crate::platform::system_for(&home, "qoder");
    env.interactive_login().await.unwrap();
    let llm = Arc::new(ModelRegistry::new(
        Catalog::builtin().unwrap(),
        RegistryOptions::default(),
    ));
    let manager =
        crate::Manager::with_environments(home, &llm, BTreeMap::from([("qoder".into(), env)]));
    let snapshot = manager.snapshot().unwrap();
    let c = snapshot
        .connections
        .iter()
        .find(|c| c.adapter_id == "qoder")
        .expect("a Qoder connection");
    let provider = llm.provider(&c.provider_id).unwrap();
    let model = llm
        .lookup(
            &provider,
            &std::env::var("HER_QODER_TEST_MODEL").unwrap_or_else(|_| "qfmodel".into()),
        )
        .unwrap();
    // Past the gateway's body limit: about 300 KB of numbered lines.
    let padding: String = (0..6000)
        .map(|n| format!("Line {n}: reference material that the answer does not need.\n"))
        .collect();
    assert!(padding.len() > super::super::route::HTTP_BODY_LIMIT);
    let ask = |context: Context| {
        let llm = llm.clone();
        let model = model.clone();
        async move {
            let mut stream = llm.stream(model, context, RequestOptions::default());
            let mut last = None;
            while let Some(event) = stream.next().await {
                match event {
                    StreamEvent::Retrying { attempt, error, .. } => {
                        eprintln!("retry {attempt}: {}", error.message)
                    }
                    StreamEvent::Done(message) => last = Some(message),
                    StreamEvent::Failed(message) => {
                        panic!("legacy request failed: {:?}", message.error)
                    }
                    _ => {}
                }
            }
            last.expect("a reply")
        }
    };
    let started = std::time::Instant::now();
    let reply = ask(Context {
        system: String::new(),
        messages: vec![Message::from(UserMessage::text(format!(
            "{padding}\nReply with exactly HER_LEGACY_OK."
        )))],
        tools: vec![],
    })
    .await;
    assert!(
        reply.text().contains("HER_LEGACY_OK"),
        "{:?}",
        reply.text()
    );
    assert!(
        reply.usage.input + reply.usage.cache_read > 60_000,
        "{:?}",
        reply.usage
    );
    eprintln!(
        "legacy large prompt: {:?}, {:?}",
        started.elapsed(),
        reply.usage
    );

    let tools = vec![ToolSpec {
        name: "her_probe".into(),
        description: "Returns a test marker.".into(),
        parameters: json!({"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}),
    }];
    let mut context = Context {
        system:
            "Call her_probe with text=你好. After its result, reply with exactly the result text."
                .into(),
        messages: vec![Message::from(UserMessage::text(format!(
            "{padding}\nRun the probe now."
        )))],
        tools,
    };
    let started = std::time::Instant::now();
    let call_reply = ask(context.clone()).await;
    let calls = call_reply.tool_calls();
    assert_eq!(calls.len(), 1, "{:?}", call_reply.content);
    assert_eq!(
        (
            calls[0].name.as_str(),
            calls[0].arguments.to_value()["text"].as_str()
        ),
        ("her_probe", Some("你好"))
    );
    let result = ToolResultMessage {
        call_id: calls[0].id.clone(),
        tool_name: calls[0].name.clone(),
        content: vec![UserContent::Text {
            text: "HER_TOOL_ROUNDTRIP_OK".into(),
        }],
        is_error: false,
        details: None,
        ts: Default::default(),
        duration_ms: None,
    };
    context.messages.push(call_reply.clone().into());
    context.messages.push(result.into());
    let answer = ask(context).await;
    assert!(
        answer.text().contains("HER_TOOL_ROUNDTRIP_OK"),
        "{:?}",
        answer.text()
    );
    eprintln!("legacy tool round trip: {:?}", started.elapsed());
}

// ---- The route over both: legacy first, the gateway only where legacy cannot serve.

use super::super::route::{Degraded, Route};

fn openai_sse(text: &str) -> String {
    format!(
        "data: {}\n\ndata: {}\n\ndata: [DONE]\n\n",
        json!({"choices": [{"index": 0, "delta": {"content": text}}]}),
        json!({"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]})
    )
}

async fn ask_route(route: &Route, base: &str, model_id: &str, text: &str) -> Vec<StreamEvent> {
    let mut req = request(
        base,
        Context {
            system: String::new(),
            messages: vec![Message::from(UserMessage::text(text))],
            tools: vec![],
        },
        RequestOptions::default(),
    );
    let mut model = (*req.model).clone();
    model.id = model_id.into();
    req.model = Arc::new(model);
    route.stream(req).collect().await
}

fn answered(events: &[StreamEvent]) -> Option<String> {
    match events.last() {
        Some(StreamEvent::Done(message)) => Some(message.text()),
        _ => None,
    }
}

async fn calls(server: &MockServer, path_suffix: &str) -> usize {
    server
        .received_requests()
        .await
        .unwrap()
        .iter()
        .filter(|r| r.url.path().ends_with(path_suffix))
        .count()
}

#[tokio::test]
async fn every_request_goes_to_legacy_and_a_queue_is_waited_there() {
    let server = MockServer::start().await;
    let queued = r#"{"code":"403","message":"{\"code\":\"10605\",\"message\":\"{\\\"isQueued\\\":true,\\\"retryAfterSeconds\\\":30,\\\"serviceAvailable\\\":false}\"}"}"#;
    Mock::given(method("POST"))
        .and(path("/algo/api/v2/service/pro/sse/agent_chat_generation"))
        .respond_with(ResponseTemplate::new(200).set_body_string(sse(&[
            json!({"choices": [{"delta": {"content": "legacy"}, "finish_reason": "stop"}]}),
            json!("[DONE]"),
        ])))
        .up_to_n_times(1)
        .mount(&server)
        .await;
    Mock::given(method("POST"))
        .and(path("/algo/api/v2/service/pro/sse/agent_chat_generation"))
        .respond_with(ResponseTemplate::new(403).set_body_string(queued))
        .mount(&server)
        .await;
    Mock::given(method("GET"))
        .and(path("/algo/api/v2/service/ask/queue/status"))
        .respond_with(
            ResponseTemplate::new(200)
                .set_body_json(json!({"isQueued": true, "retryAfterSeconds": 30})),
        )
        .mount(&server)
        .await;
    let route = Route {
        legacy: legacy(&server.uri()),
        degraded: Arc::default(),
    };
    assert_eq!(
        answered(&ask_route(&route, &server.uri(), "qfmodel", "a").await).as_deref(),
        Some("legacy")
    );
    // A queue is no reason to leave: the attempt ends overloaded, for the kernel to retry.
    let events = ask_route(&route, &server.uri(), "qfmodel", "b").await;
    let Some(StreamEvent::Failed(failed)) = events.last() else {
        panic!("{events:?}")
    };
    assert_eq!(failed.error.as_ref().unwrap().kind, ErrorKind::Overloaded);
    assert_eq!(
        calls(&server, "/chat/completions").await,
        0,
        "the gateway is never asked"
    );
}

#[tokio::test]
async fn legacy_that_cannot_serve_hands_this_and_later_requests_to_the_gateway() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/algo/api/v2/service/pro/sse/agent_chat_generation"))
        .respond_with(ResponseTemplate::new(404).set_body_string("not found"))
        .mount(&server)
        .await;
    Mock::given(method("POST"))
        .and(path("/chat/completions"))
        .respond_with(ResponseTemplate::new(200).set_body_string(openai_sse("gateway")))
        .mount(&server)
        .await;
    let degraded: Arc<Degraded> = Arc::default();
    let route = Route {
        legacy: legacy(&server.uri()),
        degraded: degraded.clone(),
    };
    assert_eq!(
        answered(&ask_route(&route, &server.uri(), "qfmodel", "a").await).as_deref(),
        Some("gateway")
    );
    assert_eq!(
        answered(&ask_route(&route, &server.uri(), "qfmodel", "b").await).as_deref(),
        Some("gateway")
    );
    assert_eq!(
        calls(&server, "agent_chat_generation").await,
        1,
        "legacy is not tried again"
    );
    // A fresh catalog tries legacy again.
    degraded.reset();
    assert_eq!(
        answered(&ask_route(&route, &server.uri(), "qfmodel", "c").await).as_deref(),
        Some("gateway")
    );
    assert_eq!(calls(&server, "agent_chat_generation").await, 2);
}

#[tokio::test]
async fn a_model_legacy_does_not_list_goes_to_the_gateway_alone() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/algo/api/v2/service/pro/sse/agent_chat_generation"))
        .respond_with(ResponseTemplate::new(200).set_body_string(sse(&[
            json!({"choices": [{"delta": {"content": "legacy"}, "finish_reason": "stop"}]}),
            json!("[DONE]"),
        ])))
        .mount(&server)
        .await;
    Mock::given(method("POST"))
        .and(path("/chat/completions"))
        .respond_with(ResponseTemplate::new(200).set_body_string(openai_sse("gateway")))
        .mount(&server)
        .await;
    let route = Route {
        legacy: legacy(&server.uri()),
        degraded: Arc::default(),
    };
    assert_eq!(
        answered(&ask_route(&route, &server.uri(), "unlisted", "a").await).as_deref(),
        Some("gateway")
    );
    assert_eq!(
        answered(&ask_route(&route, &server.uri(), "qfmodel", "b").await).as_deref(),
        Some("legacy")
    );
    assert_eq!(
        answered(&ask_route(&route, &server.uri(), "unlisted", "c").await).as_deref(),
        Some("gateway")
    );
    assert_eq!(calls(&server, "agent_chat_generation").await, 1);
}

#[tokio::test]
async fn an_unreadable_catalog_is_retried_on_legacy() {
    let server = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path("/algo/api/v2/model/list"))
        .respond_with(ResponseTemplate::new(503))
        .mount(&server)
        .await;
    let mut legacy = legacy(&server.uri());
    legacy.targets = Arc::default();
    let route = Route {
        legacy,
        degraded: Arc::default(),
    };
    let events = ask_route(&route, &server.uri(), "qfmodel", "a").await;
    let Some(StreamEvent::Failed(failed)) = events.last() else {
        panic!("{events:?}")
    };
    assert!(failed.error.as_ref().unwrap().kind.is_retryable());
    assert_eq!(calls(&server, "/chat/completions").await, 0);
}

#[tokio::test]
async fn on_the_gateway_a_server_error_of_an_oversized_body_is_its_size() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/algo/api/v2/service/pro/sse/agent_chat_generation"))
        .respond_with(ResponseTemplate::new(404))
        .mount(&server)
        .await;
    Mock::given(method("POST"))
        .and(path("/chat/completions"))
        .respond_with(
            ResponseTemplate::new(500)
                .set_body_json(json!({"error": {"message": "internal server error"}})),
        )
        .mount(&server)
        .await;
    let route = Route {
        legacy: legacy(&server.uri()),
        degraded: Arc::default(),
    };
    let small = ask_route(&route, &server.uri(), "qfmodel", "small").await;
    let Some(StreamEvent::Failed(failed)) = small.last() else {
        panic!("{small:?}")
    };
    assert_eq!(failed.error.as_ref().unwrap().kind, ErrorKind::Server);
    let big = ask_route(&route, &server.uri(), "qfmodel", &"字".repeat(100_000)).await;
    let Some(StreamEvent::Failed(failed)) = big.last() else {
        panic!("{big:?}")
    };
    let error = failed.error.as_ref().unwrap();
    assert_eq!(
        (error.kind, error.provider_code.as_deref()),
        (ErrorKind::ContextOverflow, Some("request_too_large"))
    );
}
