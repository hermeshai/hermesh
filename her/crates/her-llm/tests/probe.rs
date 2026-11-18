//! The probe of docs/kernel/custom-endpoint-design.md §4 against the shapes services come in.

use her_llm::model::AuthHeader;
use her_llm::probe::ListShape;
use her_llm::probe::ProbeError;
use her_llm::probe::ProbeInput;
use her_llm::probe::probe;
use serde_json::json;
use wiremock::Mock;
use wiremock::MockServer;
use wiremock::ResponseTemplate;
use wiremock::matchers::any;
use wiremock::matchers::header;
use wiremock::matchers::method;
use wiremock::matchers::path;

/// The kernel's own client: it never follows redirects.
fn http() -> reqwest::Client {
    her_llm::ModelRegistry::new(her_llm::Catalog::default(), Default::default()).http_client().unwrap()
}

fn input(base: &str, key: Option<&str>) -> ProbeInput {
    ProbeInput { base_url: base.to_string(), api_key: key.map(str::to_string), api: None }
}

fn openai_error() -> ResponseTemplate {
    ResponseTemplate::new(400).set_body_json(json!({"error": {"message": "model is required", "type": "invalid_request_error"}}))
}

fn anthropic_error() -> ResponseTemplate {
    ResponseTemplate::new(400).set_body_json(json!({"type": "error", "error": {"type": "invalid_request_error", "message": "model: Field required"}}))
}

fn list() -> ResponseTemplate {
    ResponseTemplate::new(200).set_body_json(json!({"object": "list", "data": [
        {"id": "claude-sonnet-4-5", "object": "model"},
        {"id": "gpt-4o", "object": "model", "supported_endpoint_types": ["openai"]},
        {"id": "house-model", "object": "model", "context_length": 32768}
    ]}))
}

#[tokio::test]
async fn a_gateway_that_speaks_both() {
    let server = MockServer::start().await;
    Mock::given(method("GET")).and(path("/v1/models")).respond_with(list()).mount(&server).await;
    Mock::given(method("POST")).and(path("/v1/chat/completions")).respond_with(openai_error()).mount(&server).await;
    Mock::given(method("POST")).and(path("/v1/responses")).respond_with(openai_error()).mount(&server).await;
    Mock::given(method("POST")).and(path("/v1/messages")).respond_with(anthropic_error()).mount(&server).await;
    let report = probe(&http(), &input(&server.uri(), Some("sk-good"))).await.unwrap();
    let found: Vec<(&str, &str)> = report.protocols.iter().map(|p| (p.api.as_str(), p.base_url.as_str())).collect();
    let root = server.uri();
    let v1 = format!("{root}/v1");
    assert_eq!(found, [("openai-chat", v1.as_str()), ("anthropic-messages", root.as_str()), ("openai-responses", v1.as_str())]);
    assert_eq!(report.protocols[0].auth_header, Some(AuthHeader::Bearer));
    assert_eq!(report.protocols[1].auth_header, Some(AuthHeader::XApiKey));
    let list = report.list.unwrap();
    assert_eq!((list.shape, list.models.len()), (ListShape::Openai, 3));
    assert_eq!(list.models[1].protocols, ["openai-chat"]);
    assert!(report.unsure.is_empty() && !report.insecure, "local http is fine");
}

#[tokio::test]
async fn an_anthropic_only_gateway() {
    let server = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path("/v1/models"))
        .and(header("anthropic-version", "2023-06-01"))
        .respond_with(ResponseTemplate::new(200).set_body_json(json!({
            "data": [{"id": "claude-opus-5", "type": "model", "display_name": "Claude Opus 5", "max_input_tokens": 1000000}],
            "has_more": false, "first_id": "claude-opus-5", "last_id": "claude-opus-5"
        })))
        .mount(&server)
        .await;
    Mock::given(method("POST")).and(path("/v1/messages")).respond_with(anthropic_error()).mount(&server).await;
    let report = probe(&http(), &input(&server.uri(), Some("sk"))).await.unwrap();
    assert_eq!(report.protocols.len(), 1);
    assert_eq!(report.protocols[0].api, "anthropic-messages");
    let list = report.list.unwrap();
    assert_eq!(list.shape, ListShape::Anthropic);
    assert_eq!(list.models[0].context_window, Some(1_000_000));
}

#[tokio::test]
async fn a_service_that_checks_the_key_before_the_path() {
    let server = MockServer::start().await;
    let good = || header("authorization", "Bearer sk-good");
    Mock::given(method("GET")).and(path("/v1/models")).and(good()).respond_with(list()).with_priority(1).mount(&server).await;
    Mock::given(method("POST")).and(path("/v1/chat/completions")).and(good()).respond_with(openai_error()).with_priority(1).mount(&server).await;
    Mock::given(method("POST")).and(good()).respond_with(ResponseTemplate::new(404)).with_priority(2).mount(&server).await;
    Mock::given(any()).respond_with(ResponseTemplate::new(401).set_body_string("Authentication Fails (governor)")).with_priority(10).mount(&server).await;
    assert_eq!(probe(&http(), &input(&server.uri(), None)).await.unwrap_err(), ProbeError::KeyRequired);
    let rejected = probe(&http(), &input(&server.uri(), Some("sk-bad"))).await.unwrap_err();
    assert_eq!(rejected, ProbeError::KeyRejected("Authentication Fails (governor)".into()), "a plain-text answer's words are kept");
    // Refused under every header: what the usual one heard, not "no key provided" from the last.
    let usual = MockServer::start().await;
    Mock::given(header("authorization", "Bearer sk-old"))
        .respond_with(ResponseTemplate::new(401).set_body_json(json!({"error": {"message": "Invalid API-key provided."}})))
        .with_priority(1)
        .mount(&usual)
        .await;
    Mock::given(any()).respond_with(ResponseTemplate::new(401).set_body_json(json!({"error": {"message": "No API-key provided."}}))).with_priority(5).mount(&usual).await;
    let rejected = probe(&http(), &input(&usual.uri(), Some("sk-old"))).await.unwrap_err();
    assert!(matches!(&rejected, ProbeError::KeyRejected(m) if m.contains("Invalid API-key")), "{rejected:?}");
    let report = probe(&http(), &input(&server.uri(), Some("sk-good"))).await.unwrap();
    assert_eq!(report.protocols.iter().map(|p| p.api.as_str()).collect::<Vec<_>>(), ["openai-chat"]);
}

#[tokio::test]
async fn an_admin_page_on_every_path_is_no_interface() {
    let server = MockServer::start().await;
    let page = ResponseTemplate::new(200).insert_header("content-type", "text/html").set_body_string("<html>admin</html>");
    Mock::given(method("GET")).and(path("/v1/models")).respond_with(list()).with_priority(1).mount(&server).await;
    Mock::given(method("POST")).and(path("/v1/chat/completions")).respond_with(openai_error()).with_priority(1).mount(&server).await;
    Mock::given(method("POST")).and(path("/v1/her-probe-x")).respond_with(page.clone()).with_priority(1).mount(&server).await;
    Mock::given(method("GET")).respond_with(page.clone()).with_priority(5).mount(&server).await;
    let report = probe(&http(), &input(&format!("{}/", server.uri()), Some("k"))).await.unwrap();
    assert_eq!(report.protocols.iter().map(|p| p.api.as_str()).collect::<Vec<_>>(), ["openai-chat"]);
    assert_eq!(report.list.unwrap().base_url, format!("{}/v1", server.uri()));
}

#[tokio::test]
async fn the_header_that_works_is_kept() {
    let server = MockServer::start().await;
    let key = || header("api-key", "az-key");
    Mock::given(method("GET")).and(path("/openai/v1/models")).and(key()).respond_with(list()).with_priority(1).mount(&server).await;
    Mock::given(method("POST")).and(path("/openai/v1/chat/completions")).and(key()).respond_with(openai_error()).with_priority(1).mount(&server).await;
    Mock::given(method("GET")).and(path("/openai/v1/models")).respond_with(ResponseTemplate::new(401).set_body_json(json!({"error": {"message": "no key"}}))).with_priority(5).mount(&server).await;
    Mock::given(method("POST")).and(path("/openai/v1/chat/completions")).respond_with(ResponseTemplate::new(401).set_body_json(json!({"error": {"message": "no key"}}))).with_priority(5).mount(&server).await;
    let report = probe(&http(), &input(&format!("{}/openai/v1", server.uri()), Some("az-key"))).await.unwrap();
    assert_eq!(report.protocols[0].auth_header, Some(AuthHeader::ApiKey));
    assert_eq!(report.list.unwrap().auth_header, Some(AuthHeader::ApiKey));
}

#[tokio::test]
async fn a_gateway_that_answers_every_path_alike() {
    let server = MockServer::start().await;
    Mock::given(method("GET")).and(path("/v1/models")).respond_with(list()).with_priority(1).mount(&server).await;
    Mock::given(method("POST")).respond_with(ResponseTemplate::new(400).set_body_json(json!({"error": {"message": "x-portkey-provider is required"}}))).with_priority(5).mount(&server).await;
    let report = probe(&http(), &input(&server.uri(), Some("k"))).await.unwrap();
    assert_eq!(report.protocols.iter().map(|p| p.api.as_str()).collect::<Vec<_>>(), ["openai-chat"], "assumed from the list");
    assert!(report.unsure.contains(&"anthropic-messages".to_string()));
}

#[tokio::test]
async fn nothing_listening() {
    let error = probe(&http(), &input("http://127.0.0.1:9", None)).await.unwrap_err();
    assert!(matches!(error, ProbeError::Unreachable(_)), "{error:?}");
    let error = probe(&http(), &input("not a url at all", None)).await.unwrap_err();
    assert!(matches!(error, ProbeError::InvalidUrl(_) | ProbeError::Unreachable(_)), "{error:?}");
}

#[tokio::test]
async fn a_protocol_can_be_named() {
    let server = MockServer::start().await;
    Mock::given(method("GET")).and(path("/v1/models")).respond_with(list()).mount(&server).await;
    let report = probe(&http(), &ProbeInput { api: Some("anthropic-messages".into()), ..input(&server.uri(), Some("k")) }).await.unwrap();
    assert_eq!(report.protocols.len(), 1);
    assert_eq!((report.protocols[0].api.as_str(), report.protocols[0].base_url.as_str()), ("anthropic-messages", server.uri().as_str()));
}

#[tokio::test]
async fn a_public_list_and_a_key_for_the_rest() {
    // OpenRouter's shape: anyone may list the models; asking anything else wants a key.
    let server = MockServer::start().await;
    let good = || header("authorization", "Bearer sk-good");
    Mock::given(method("GET")).and(path("/api/v1/models")).respond_with(list()).mount(&server).await;
    Mock::given(method("POST")).and(path("/api/v1/chat/completions")).and(good()).respond_with(openai_error()).with_priority(1).mount(&server).await;
    Mock::given(method("POST"))
        .and(path("/api/v1/messages"))
        .respond_with(ResponseTemplate::new(401).set_body_json(json!({"type": "error", "error": {"type": "authentication_error", "message": "No cookie auth credentials found"}})))
        .with_priority(5)
        .mount(&server)
        .await;
    Mock::given(method("POST"))
        .and(path("/api/v1/chat/completions"))
        .respond_with(ResponseTemplate::new(401).set_body_json(json!({"error": {"message": "No cookie auth credentials found", "code": 401}})))
        .with_priority(5)
        .mount(&server)
        .await;
    Mock::given(method("POST")).respond_with(ResponseTemplate::new(404).set_body_json(json!({"error": {"message": "Not Found", "code": 404}}))).with_priority(10).mount(&server).await;
    let base = format!("{}/api/v1", server.uri());
    assert_eq!(probe(&http(), &input(&base, None)).await.unwrap_err(), ProbeError::KeyRequired);
    let rejected = probe(&http(), &input(&base, Some("sk-wrong"))).await.unwrap_err();
    assert!(matches!(&rejected, ProbeError::KeyRejected(message) if message.contains("No cookie auth credentials")), "{rejected:?}");
    let report = probe(&http(), &input(&base, Some("sk-good"))).await.unwrap();
    assert_eq!(report.protocols.iter().map(|p| p.api.as_str()).collect::<Vec<_>>(), ["openai-chat"]);
}

#[tokio::test]
async fn a_public_list_and_a_key_before_any_path() {
    // Anyone may list; every POST, even to a path that is not there, wants a key first.
    let server = MockServer::start().await;
    Mock::given(method("GET")).and(path("/v1/models")).respond_with(list()).mount(&server).await;
    Mock::given(method("POST")).respond_with(ResponseTemplate::new(401).set_body_json(json!({"error": {"message": "missing api key"}}))).mount(&server).await;
    assert_eq!(probe(&http(), &input(&server.uri(), None)).await.unwrap_err(), ProbeError::KeyRequired, "not assumed from the list");
    let rejected = probe(&http(), &input(&server.uri(), Some("sk-wrong"))).await.unwrap_err();
    assert!(matches!(rejected, ProbeError::KeyRejected(_)), "{rejected:?}");
}
