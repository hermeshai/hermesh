//! Subscription Responses behavior against a real HTTP/SSE boundary.
use her_llm::{
    Catalog, Context, ErrorKind, Message, ModelRegistry, RegistryOptions, RequestOptions, StopReason, StreamEvent,
    UserMessage,
};
use futures::StreamExt;
use serde_json::{Value, json};
use wiremock::{
    Mock, MockServer, ResponseTemplate,
    matchers::{header, method, path},
};

fn registry(server: &MockServer) -> ModelRegistry {
    ModelRegistry::new(
        Catalog::from_toml(&format!(
            r#"
[providers.plan]
api = "openai-responses"
base_url = "{}/v1"
api_key = "fixture-access"
[[providers.plan.models]]
id = "gpt-test"
"#,
            server.uri()
        ))
        .unwrap(),
        RegistryOptions::default(),
    )
}
fn context() -> Context {
    Context {
        system: "User's own instructions".into(),
        messages: vec![Message::from(UserMessage::text("hello"))],
        tools: vec![],
    }
}
async fn serve(server: &MockServer, events: Vec<Value>) {
    let sse: String = events.into_iter().map(|e| format!("data: {e}\n\n")).collect();
    Mock::given(method("POST"))
        .and(path("/v1/responses"))
        .and(header("authorization", "Bearer fixture-access"))
        .respond_with(
            ResponseTemplate::new(200)
                .insert_header("content-type", "text/event-stream")
                .set_body_string(sse),
        )
        .expect(1)
        .mount(server)
        .await;
}
#[tokio::test]
async fn a_completion_listing_no_items_keeps_what_was_streamed() {
    let server = MockServer::start().await;
    serve(&server, vec![
        json!({"type":"response.output_text.delta","output_index":0,"delta":"o"}),
        json!({"type":"response.output_text.delta","output_index":0,"delta":"k"}),
        json!({"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":2}}}),
    ]).await;
    let registry = registry(&server);
    let model = registry.resolve("plan/gpt-test").unwrap().model;
    let events: Vec<_> = registry.stream(model, context(), RequestOptions::default()).collect().await;
    let StreamEvent::Done(message) = events.last().unwrap() else {
        panic!("{events:?}")
    };
    assert_eq!(message.text(), "ok", "the reply stays in the history");
    assert_eq!(message.stop_reason, StopReason::Stop);
    assert_eq!(message.usage.output, 2);
}
#[tokio::test]
async fn completion_preserves_reasoning_tools_usage_and_stateless_replay() {
    let server = MockServer::start().await;
    let reasoning = json!({"type":"reasoning","id":"rs_1","encrypted_content":"opaque-ciphertext","summary":[{"type":"summary_text","text":"Checking"}]});
    let call = json!({"type":"function_call","id":"fc_1","namespace":"her","call_id":"call_1","name":"get_time","arguments":"{}","status":"completed"});
    serve(&server,vec![
        json!({"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"Checking"}),
        json!({"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"get_time"}}),
        json!({"type":"response.function_call_arguments.delta","output_index":1,"delta":"{}"}),
        json!({"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[reasoning.clone(),call],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":8}}}}),
    ]).await;
    let registry = registry(&server);
    let model = registry.resolve("plan/gpt-test").unwrap().model;
    let options = RequestOptions {
        temperature: Some(0.7),
        max_output_tokens: Some(10),
        extra_body: json!({"store":true,"previous_response_id":"bad"})
            .as_object()
            .unwrap()
            .clone(),
        ..Default::default()
    };
    let events: Vec<_> = registry.stream(model.clone(), context(), options).collect().await;
    let StreamEvent::Done(message) = events.last().unwrap() else {
        panic!("{events:?}")
    };
    assert_eq!(message.stop_reason, StopReason::ToolUse);
    assert_eq!(message.usage.input, 60);
    assert_eq!(message.usage.cache_read, 40);
    assert_eq!(message.usage.output, 20);
    assert_eq!(message.usage.reasoning, Some(8));
    let mut replay = context();
    replay.messages.push(Message::from(message.clone()));
    let body = her_llm::api::openai_responses::build_body(&model, &replay, &RequestOptions::default());
    assert_eq!(body["input"][1], reasoning);
    assert_eq!(body["input"][2]["namespace"], "her");
    assert_eq!(body["input"][2]["call_id"], "call_1");
    let requests = server.received_requests().await.unwrap();
    let body: Value = requests[0].body_json().unwrap();
    assert_eq!(body["instructions"], "User's own instructions");
    assert_eq!(body["store"], false);
    assert_eq!(body["stream"], true);
    for key in ["temperature", "max_output_tokens", "previous_response_id"] {
        assert!(body.get(key).is_none());
    }
}
/// The events of a request for "hello", to the end.
async fn run(server: &MockServer) -> Vec<StreamEvent> {
    let registry = registry(server);
    let model = registry.resolve("plan/gpt-test").unwrap().model;
    registry.stream(model, context(), RequestOptions::default()).collect().await
}

#[tokio::test]
async fn output_her_cannot_read_is_a_failure_that_names_it() {
    let server = MockServer::start().await;
    serve(
        &server,
        vec![json!({"type": "response.completed", "response": {"status": "completed", "id": "r",
            "output": [{"type": "new_kind", "text": "private"}, {"type": "message", "content": [{"type": "new_part", "x": "private"}]}],
            "usage": {"input_tokens": 9, "output_tokens": 46}}})],
    )
    .await;
    let events = run(&server).await;
    let Some(StreamEvent::Failed(message)) = events.last() else { panic!("{events:?}") };
    let error = message.error.as_ref().unwrap();
    assert_eq!(error.kind, ErrorKind::Protocol);
    assert!(error.message.contains("new_kind") && error.message.contains("message/new_part"), "{}", error.message);
    assert!(!error.message.contains("private"), "{}", error.message);
}

#[tokio::test]
async fn output_her_cannot_read_is_named_when_only_its_own_event_lists_it() {
    let server = MockServer::start().await;
    serve(
        &server,
        vec![
            json!({"type": "response.output_item.done", "output_index": 0, "item": {"type": "new_kind", "text": "private"}}),
            json!({"type": "response.completed", "response": {"status": "completed", "id": "r", "output": [],
                "usage": {"input_tokens": 9, "output_tokens": 46}}}),
        ],
    )
    .await;
    let events = run(&server).await;
    let Some(StreamEvent::Failed(message)) = events.last() else { panic!("{events:?}") };
    let error = message.error.as_ref().unwrap();
    assert!(error.message.contains("new_kind") && !error.message.contains("private"), "{}", error.message);
}

/// What the subscription route sent when a model chose to say nothing, captured 2026-11-05.
#[tokio::test]
async fn an_empty_answer_is_a_reply_not_a_failure() {
    let server = MockServer::start().await;
    let part = json!({"annotations": [], "logprobs": [], "text": "", "type": "output_text"});
    serve(
        &server,
        vec![
            json!({"type": "response.output_item.added", "output_index": 0, "item": {"type": "message", "role": "assistant",
                "phase": "final_answer", "status": "in_progress", "content": []}}),
            json!({"type": "response.content_part.added", "output_index": 0, "content_index": 0, "part": part}),
            json!({"type": "response.output_text.done", "output_index": 0, "content_index": 0, "text": ""}),
            json!({"type": "response.content_part.done", "output_index": 0, "content_index": 0, "part": part}),
            json!({"type": "response.output_item.done", "output_index": 0, "item": {"type": "message", "role": "assistant",
                "phase": "final_answer", "status": "completed", "content": [part]}}),
            json!({"type": "response.completed", "response": {"status": "completed", "id": "r", "output": [],
                "usage": {"input_tokens": 2778, "output_tokens": 4}}}),
        ],
    )
    .await;
    let events = run(&server).await;
    let Some(StreamEvent::Done(message)) = events.last() else { panic!("{events:?}") };
    assert!(message.content.is_empty() && message.error.is_none(), "{message:?}");
    assert_eq!(message.stop_reason, StopReason::Stop);
    assert_eq!(message.usage.output, 4);
}

#[tokio::test]
async fn an_empty_output_list_keeps_what_was_streamed() {
    let server = MockServer::start().await;
    serve(
        &server,
        vec![
            json!({"type": "response.output_text.delta", "output_index": 0, "delta": "你好"}),
            json!({"type": "response.completed", "response": {"status": "completed", "id": "r", "output": [],
                "usage": {"input_tokens": 9, "output_tokens": 2}}}),
        ],
    )
    .await;
    let events = run(&server).await;
    let Some(StreamEvent::Done(message)) = events.last() else { panic!("{events:?}") };
    assert_eq!(message.text(), "你好");
}

#[tokio::test]
async fn a_reply_with_only_reasoning_is_still_a_reply() {
    let server = MockServer::start().await;
    serve(
        &server,
        vec![json!({"type": "response.completed", "response": {"status": "completed", "id": "r",
            "output": [{"type": "reasoning", "id": "rs", "summary": []}],
            "usage": {"input_tokens": 9, "output_tokens": 5}}})],
    )
    .await;
    assert!(matches!(run(&server).await.last(), Some(StreamEvent::Done(_))));
}

#[tokio::test]
async fn truncated_stream_is_failure_even_after_text() {
    let server = MockServer::start().await;
    serve(
        &server,
        vec![json!({"type":"response.output_text.delta","output_index":0,"delta":"partial"})],
    )
    .await;
    let r = registry(&server);
    let events: Vec<_> = r
        .stream(
            r.resolve("plan/gpt-test").unwrap().model,
            context(),
            RequestOptions::default(),
        )
        .collect()
        .await;
    let StreamEvent::Failed(m) = events.last().unwrap() else {
        panic!("{events:?}")
    };
    assert_eq!(m.error.as_ref().unwrap().kind, ErrorKind::Protocol);
    assert_eq!(m.text(), "partial");
}
#[tokio::test]
async fn quota_error_after_text_is_not_retried_or_treated_as_success() {
    let server = MockServer::start().await;
    serve(&server,vec![json!({"type":"response.output_text.delta","output_index":0,"delta":"partial"}),json!({"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"limit"}}})]).await;
    let r = registry(&server);
    let events: Vec<_> = r
        .stream(
            r.resolve("plan/gpt-test").unwrap().model,
            context(),
            RequestOptions::default(),
        )
        .collect()
        .await;
    let StreamEvent::Failed(m) = events.last().unwrap() else {
        panic!("{events:?}")
    };
    assert_eq!(m.error.as_ref().unwrap().kind, ErrorKind::QuotaExceeded);
    assert!(
        !events
            .iter()
            .any(|e| matches!(e, StreamEvent::Done(_) | StreamEvent::Retrying { .. }))
    );
}
#[tokio::test]
async fn invalid_tool_arguments_never_execute() {
    let server = MockServer::start().await;
    serve(&server,vec![json!({"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"get_time","arguments":"{broken"}]}})]).await;
    let r = registry(&server);
    let events: Vec<_> = r
        .stream(
            r.resolve("plan/gpt-test").unwrap().model,
            context(),
            RequestOptions::default(),
        )
        .collect()
        .await;
    let StreamEvent::Failed(m) = events.last().unwrap() else {
        panic!("{events:?}")
    };
    assert!(m.tool_calls().is_empty());
}

#[tokio::test]
async fn images_and_tool_results_use_responses_input_parts() {
    let server = MockServer::start().await;
    let r = registry(&server);
    let model = r.resolve("plan/gpt-test").unwrap().model;
    let mut user = UserMessage::text("describe this image");
    user.content.push(her_llm::UserContent::Image {
        mime_type: "image/png".into(),
        data: "aGVsbG8=".into(),
    });
    let mut input = context();
    input.messages = vec![
        Message::from(user),
        Message::from(her_llm::ToolResultMessage {
            call_id: "call1".into(),
            tool_name: "read".into(),
            content: vec![her_llm::UserContent::Text { text: "result".into() }],
            is_error: false,
            details: None,
            ts: 0,
            duration_ms: None,
        }),
    ];
    let body = her_llm::api::openai_responses::build_body(&model, &input, &RequestOptions::default());
    assert_eq!(
        body["input"][0]["content"][1]["image_url"],
        "data:image/png;base64,aGVsbG8="
    );
    assert_eq!(body["input"][1]["type"], "function_call_output");
    assert_eq!(
        body["input"][1]["output"][0],
        json!({"type":"input_text","text":"result"})
    );
}
