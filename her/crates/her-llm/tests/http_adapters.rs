//! Adapters against a local mock server: headers, SSE parsing, error classification, retries.

use futures::StreamExt;
use her_llm::Catalog;
use her_llm::Context;
use her_llm::ErrorKind;
use her_llm::Message;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use her_llm::RequestOptions;
use her_llm::RetryPolicy;
use her_llm::StopReason;
use her_llm::StreamEvent;
use her_llm::UserMessage;
use std::time::Duration;
use wiremock::Mock;
use wiremock::MockServer;
use wiremock::ResponseTemplate;
use wiremock::matchers::header;
use wiremock::matchers::method;
use wiremock::matchers::path;

fn registry_for(server: &MockServer) -> ModelRegistry {
    let catalog = Catalog::from_toml(&format!(
        r#"
        [providers.mock]
        api = "openai-chat"
        base_url = "{uri}/v1"
        api_key = "sk-test-key"
        compat = {{ max_tokens_field = "max_tokens" }}

        [[providers.mock.models]]
        id = "mock-model"
        cost = {{ input = 1.0, output = 2.0 }}

        [providers.mock-anthropic]
        api = "anthropic-messages"
        base_url = "{uri}/anthropic"
        api_key = "sk-test-key"

        [[providers.mock-anthropic.models]]
        id = "mock-model"
        "#,
        uri = server.uri()
    ))
    .unwrap();
    let options = RegistryOptions {
        retry: RetryPolicy {
            base_delay: Duration::from_millis(1),
            max_delay: Duration::from_millis(5),
            ..Default::default()
        },
        ..Default::default()
    };
    ModelRegistry::new(catalog, options)
}

fn context() -> Context {
    Context {
        system: "be brief".into(),
        messages: vec![Message::from(UserMessage::text("hi"))],
        tools: vec![],
    }
}

fn sse(chunks: &[&str]) -> String {
    let mut body = String::new();
    for chunk in chunks {
        body.push_str("data: ");
        body.push_str(chunk);
        body.push_str("\n\n");
    }
    body
}

fn sse_response(body: String) -> ResponseTemplate {
    ResponseTemplate::new(200)
        .insert_header("content-type", "text/event-stream")
        .set_body_string(body)
}

async fn collect(registry: &ModelRegistry, reference: &str) -> Vec<StreamEvent> {
    let model = registry.resolve(reference).unwrap().model;
    registry
        .stream(model, context(), RequestOptions::default())
        .collect()
        .await
}

const OK_CHUNKS: &[&str] = &[
    r#"{"id":"r1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}"#,
    r#"{"id":"r1","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000000,"completion_tokens":500000}}"#,
    "[DONE]",
];

#[tokio::test]
async fn openai_chat_streams_and_prices_usage() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/v1/chat/completions"))
        .and(header("authorization", "Bearer sk-test-key"))
        .respond_with(sse_response(sse(OK_CHUNKS)))
        .expect(1)
        .mount(&server)
        .await;
    let registry = registry_for(&server);
    let events = collect(&registry, "mock/mock-model").await;
    let text: String = events
        .iter()
        .filter_map(|e| match e {
            StreamEvent::TextDelta { text, .. } => Some(text.as_str()),
            _ => None,
        })
        .collect();
    assert_eq!(text, "Hello");
    match events.last().unwrap() {
        StreamEvent::Done(msg) => {
            assert_eq!(msg.text(), "Hello");
            assert_eq!(msg.stop_reason, StopReason::Stop);
            assert_eq!(msg.usage.cost.total, 1.0 + 1.0);
        }
        other => panic!("unexpected {other:?}"),
    }
    let request = &server.received_requests().await.unwrap()[0];
    let body: serde_json::Value = serde_json::from_slice(&request.body).unwrap();
    assert_eq!(body["stream"], true);
    assert!(body.get("max_tokens").is_none(), "a limit nobody states is the provider's to apply");
    assert_eq!(body["messages"][0]["role"], "system");
}

#[tokio::test]
async fn retries_after_rate_limit() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/v1/chat/completions"))
        .respond_with(ResponseTemplate::new(429).insert_header("retry-after-ms", "5").set_body_string("slow down"))
        .up_to_n_times(1)
        .with_priority(1)
        .mount(&server)
        .await;
    Mock::given(method("POST"))
        .and(path("/v1/chat/completions"))
        .respond_with(sse_response(sse(OK_CHUNKS)))
        .with_priority(2)
        .mount(&server)
        .await;
    let registry = registry_for(&server);
    let events = collect(&registry, "mock/mock-model").await;
    match &events[0] {
        StreamEvent::Retrying { attempt, delay, error } => {
            assert_eq!(*attempt, 1);
            assert_eq!(*delay, Duration::from_millis(5));
            assert_eq!(error.kind, ErrorKind::RateLimited);
        }
        other => panic!("unexpected {other:?}"),
    }
    assert!(matches!(events.last().unwrap(), StreamEvent::Done(_)));
}

#[tokio::test]
async fn auth_errors_are_not_retried() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/v1/chat/completions"))
        .respond_with(
            ResponseTemplate::new(401).set_body_string(r#"{"error":{"message":"Authentication Fails","type":"authentication_error"}}"#),
        )
        .expect(1)
        .mount(&server)
        .await;
    let registry = registry_for(&server);
    let events = collect(&registry, "mock/mock-model").await;
    assert_eq!(events.len(), 1);
    match &events[0] {
        StreamEvent::Failed(msg) => {
            let error = msg.error.as_ref().unwrap();
            assert_eq!(error.kind, ErrorKind::Auth);
            assert_eq!(error.status, Some(401));
            assert_eq!(error.message, "Authentication Fails");
        }
        other => panic!("unexpected {other:?}"),
    }
}

#[tokio::test]
async fn truncated_stream_is_a_failure() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/v1/chat/completions"))
        .respond_with(sse_response(String::new()))
        .mount(&server)
        .await;
    let registry = registry_for(&server);
    let events = collect(&registry, "mock/mock-model").await;
    match events.last().unwrap() {
        StreamEvent::Failed(msg) => assert_eq!(msg.error.as_ref().unwrap().kind, ErrorKind::Protocol),
        other => panic!("unexpected {other:?}"),
    }
}

#[tokio::test]
async fn anthropic_streams_with_api_key_header() {
    let server = MockServer::start().await;
    let body = [
        ("message_start", r#"{"type":"message_start","message":{"id":"m1","usage":{"input_tokens":12,"output_tokens":0}}}"#),
        ("content_block_start", r#"{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}"#),
        ("content_block_delta", r#"{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi there"}}"#),
        ("content_block_stop", r#"{"type":"content_block_stop","index":0}"#),
        ("message_delta", r#"{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}"#),
        ("message_stop", r#"{"type":"message_stop"}"#),
    ]
    .iter()
    .map(|(name, data)| format!("event: {name}\ndata: {data}\n\n"))
    .collect::<String>();
    Mock::given(method("POST"))
        .and(path("/anthropic/v1/messages"))
        .and(header("x-api-key", "sk-test-key"))
        .and(header("anthropic-version", "2023-06-01"))
        .respond_with(sse_response(body))
        .expect(1)
        .mount(&server)
        .await;
    let registry = registry_for(&server);
    let events = collect(&registry, "mock-anthropic/mock-model").await;
    match events.last().unwrap() {
        StreamEvent::Done(msg) => {
            assert_eq!(msg.text(), "Hi there");
            assert_eq!(msg.usage.input, 12);
            assert_eq!(msg.usage.output, 3);
            assert_eq!(msg.response_id.as_deref(), Some("m1"));
        }
        other => panic!("unexpected {other:?}"),
    }
}

/// Serves one streaming response per connection: `chunks` SSE events `gap` apart, then
/// `[DONE]`, or, with `stall`, nothing more after them.
async fn paced_server(chunks: usize, gap: Duration, stall: bool) -> String {
    use tokio::io::AsyncReadExt;
    use tokio::io::AsyncWriteExt;
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        while let Ok((mut stream, _)) = listener.accept().await {
            tokio::spawn(async move {
                let mut request = Vec::new();
                let mut buf = [0u8; 4096];
                // Headers, then the body by its length.
                loop {
                    let n = stream.read(&mut buf).await.unwrap_or(0);
                    if n == 0 {
                        return;
                    }
                    request.extend_from_slice(&buf[..n]);
                    let text = String::from_utf8_lossy(&request).to_string();
                    if let Some(end) = text.find("\r\n\r\n") {
                        let length = text[..end]
                            .lines()
                            .find_map(|l| l.to_ascii_lowercase().strip_prefix("content-length:").map(|v| v.trim().to_string()))
                            .and_then(|v| v.parse::<usize>().ok())
                            .unwrap_or(0);
                        if request.len() >= end + 4 + length {
                            break;
                        }
                    }
                }
                let head = "HTTP/1.1 200 OK\r\ncontent-type: text/event-stream\r\ntransfer-encoding: chunked\r\n\r\n";
                stream.write_all(head.as_bytes()).await.unwrap();
                let send = |data: String| {
                    let event = format!("data: {data}\n\n");
                    format!("{:x}\r\n{event}\r\n", event.len())
                };
                for i in 0..chunks {
                    let chunk = format!(r#"{{"id":"r1","choices":[{{"index":0,"delta":{{"content":"c{i} "}},"finish_reason":null}}]}}"#);
                    if stream.write_all(send(chunk).as_bytes()).await.is_err() {
                        return;
                    }
                    tokio::time::sleep(gap).await;
                }
                if stall {
                    tokio::time::sleep(Duration::from_secs(60)).await;
                    return;
                }
                let last = r#"{"id":"r1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}"#.to_string();
                let _ = stream.write_all(send(last).as_bytes()).await;
                let _ = stream.write_all(send("[DONE]".into()).as_bytes()).await;
                let _ = stream.write_all(b"0\r\n\r\n").await;
            });
        }
    });
    format!("http://{addr}")
}

fn paced_registry(base: &str, idle: Duration) -> ModelRegistry {
    let catalog = Catalog::from_toml(&format!(
        r#"
        [providers.mock]
        api = "openai-chat"
        base_url = "{base}/v1"
        api_key = "sk-test-key"

        [[providers.mock.models]]
        id = "mock-model"
        "#
    ))
    .unwrap();
    let options = RegistryOptions {
        timeouts: her_llm::Timeouts {
            idle,
            ..Default::default()
        },
        retry: RetryPolicy {
            max_retries: Some(0),
            ..Default::default()
        },
        ..Default::default()
    };
    ModelRegistry::new(catalog, options)
}

#[tokio::test]
async fn idle_timeout_spans_each_gap_not_the_whole_stream() {
    // Twenty events 75 ms apart: the stream takes longer than the idle timeout, but no
    // single gap comes close.
    let base = paced_server(20, Duration::from_millis(75), false).await;
    let registry = paced_registry(&base, Duration::from_secs(1));
    let events = collect(&registry, "mock/mock-model").await;
    let expected: String = (0..20).map(|i| format!("c{i} ")).collect();
    match events.last().unwrap() {
        StreamEvent::Done(msg) => assert_eq!(msg.text(), expected),
        other => panic!("unexpected {other:?}"),
    }
}

#[tokio::test]
async fn idle_timeout_fails_a_stalled_stream() {
    let base = paced_server(3, Duration::from_millis(20), true).await;
    let registry = paced_registry(&base, Duration::from_millis(300));
    let started = std::time::Instant::now();
    let events = collect(&registry, "mock/mock-model").await;
    let elapsed = started.elapsed();
    match events.last().unwrap() {
        StreamEvent::Failed(msg) => assert_eq!(msg.error.as_ref().unwrap().kind, ErrorKind::Timeout),
        other => panic!("unexpected {other:?}"),
    }
    // The last event arrived about 60 ms in; the timeout counts from there.
    assert!(elapsed >= Duration::from_millis(300), "{elapsed:?}");
    assert!(elapsed < Duration::from_secs(3), "{elapsed:?}");
}
