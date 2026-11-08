//! Model lists and the public catalog against a local mock server
//! (docs/kernel/model-catalog-design.md §3, §5).

use her_llm::Catalog;
use her_llm::ErrorKind;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use her_llm::directory::IdSource;
use her_llm::discovery::ListSource;
use serde_json::json;
use wiremock::Mock;
use wiremock::MockServer;
use wiremock::ResponseTemplate;
use wiremock::matchers::body_json;
use wiremock::matchers::header;
use wiremock::matchers::method;
use wiremock::matchers::path;
use wiremock::matchers::query_param;

fn registry(server: &MockServer, dir: &std::path::Path) -> ModelRegistry {
    let catalog = Catalog::from_toml(&format!(
        r#"
        [providers.oai]
        api = "openai-chat"
        base_url = "{uri}/v1"
        api_key = "sk-configured"
        catalog_id = "deepseek"

        [[providers.oai.models]]
        id = "written"
        name = "Written model"

        [providers.alt]
        api = "anthropic-messages"
        base_url = "{uri}/alt"
        api_key = "sk-configured"
        list_from = "oai"

        [providers.ant]
        api = "anthropic-messages"
        base_url = "{uri}/ant"
        api_key = "sk-ant"

        [providers.none]
        api = "openai-chat"
        base_url = "{uri}/nolist/v1"
        auth = "none"

        [providers.local]
        api = "openai-chat"
        base_url = "{uri}/v1"
        auth = "none"
        list_details = "ollama"
        "#,
        uri = server.uri()
    ))
    .unwrap();
    ModelRegistry::new(catalog, RegistryOptions { models_dir: Some(dir.to_path_buf()), ..Default::default() })
}

#[tokio::test]
async fn an_openai_style_list_is_asked_with_the_key_and_remembered() {
    let server = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path("/v1/models"))
        .and(header("authorization", "Bearer sk-configured"))
        .respond_with(ResponseTemplate::new(200).set_body_json(json!({
            "object": "list",
            "data": [{"id": "deepseek-flash", "object": "model"}, {"id": "brand-new", "object": "model"}]
        })))
        .mount(&server)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let llm = registry(&server, dir.path());
    let refresh = llm.refresh_list("oai", None).await.unwrap();
    assert_eq!((refresh.source, refresh.models, refresh.changed), (ListSource::Provider, 2, true));
    let list = llm.directory().list("oai").unwrap();
    assert!(list.fetched_at.is_some() && list.error.is_none());
    assert_eq!(list.credential.as_deref(), Some(her_llm::discovery::fingerprint("sk-configured").as_str()));
    let text = std::fs::read_to_string(dir.path().join("lists/oai.json")).unwrap();
    assert!(!text.contains("sk-configured"), "the key never reaches the cache");

    let provider = llm.provider("oai").unwrap();
    let models = llm.directory().models(&provider);
    assert_eq!(models.source, IdSource::Provider);
    let ids: Vec<_> = models.models.iter().map(|m| m.model.id.as_str()).collect();
    assert_eq!(ids, ["deepseek-flash", "brand-new", "written"]);
    // The catalog fills in the listed model's details.
    let flash = llm.resolve("oai/deepseek-flash").unwrap();
    assert!(!flash.ad_hoc);
    assert_eq!(flash.model.context_window, 1_048_576);
    assert!(llm.resolve("oai/brand-new").is_ok());
    // A provider sharing the list reads it without asking.
    let alt = llm.provider("alt").unwrap();
    assert_eq!(llm.directory().models(&alt).models.len(), 2);
    assert_eq!(server.received_requests().await.unwrap().len(), 1);

    // A second refresh with the same answer changes nothing.
    assert!(!llm.refresh_list("oai", None).await.unwrap().changed);
}

#[tokio::test]
async fn anthropic_lists_page_with_their_own_headers() {
    let server = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path("/ant/v1/models"))
        .and(header("x-api-key", "sk-ant"))
        .and(header("anthropic-version", "2023-06-01"))
        .and(query_param("after_id", "m2"))
        .respond_with(ResponseTemplate::new(200).set_body_json(json!({
            "data": [{"id": "m3", "display_name": "Model Three"}], "has_more": false
        })))
        .mount(&server)
        .await;
    Mock::given(method("GET"))
        .and(path("/ant/v1/models"))
        .and(query_param("limit", "1000"))
        .respond_with(ResponseTemplate::new(200).set_body_json(json!({
            "data": [{"id": "m1"}, {"id": "m2"}], "has_more": true, "last_id": "m2"
        })))
        .mount(&server)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let llm = registry(&server, dir.path());
    assert_eq!(llm.refresh_list("ant", None).await.unwrap().models, 3);
    let provider = llm.provider("ant").unwrap();
    let models = llm.directory().models(&provider);
    assert_eq!(models.get("m3").unwrap().model.name, "Model Three");
}

#[tokio::test]
async fn a_rejected_key_is_an_auth_error_and_leaves_nothing_behind() {
    let server = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path("/v1/models"))
        .respond_with(ResponseTemplate::new(401).set_body_json(json!({"error": {"message": "Authentication Fails, Your api key is invalid"}})))
        .mount(&server)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let llm = registry(&server, dir.path());
    let err = llm.refresh_list("oai", Some("sk-wrong".into())).await.unwrap_err();
    assert_eq!(err.kind, ErrorKind::Auth);
    assert!(err.message.contains("invalid"));
    assert!(!dir.path().join("lists/oai.json").exists());
}

#[tokio::test]
async fn a_failed_refresh_keeps_the_old_list_and_says_why() {
    let server = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path("/v1/models"))
        .respond_with(ResponseTemplate::new(200).set_body_json(json!({"data": [{"id": "deepseek-flash"}]})))
        .up_to_n_times(1)
        .mount(&server)
        .await;
    Mock::given(method("GET"))
        .and(path("/v1/models"))
        .respond_with(ResponseTemplate::new(503).set_body_string("busy"))
        .mount(&server)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let llm = registry(&server, dir.path());
    llm.refresh_list("oai", None).await.unwrap();
    let err = llm.refresh_list("oai", None).await.unwrap_err();
    assert_eq!(err.kind, ErrorKind::Overloaded);
    let list = llm.directory().list("oai").unwrap();
    assert_eq!(list.models.len(), 1, "the old answer stays");
    assert_eq!(list.error.as_ref().unwrap().kind, "overloaded");
}

#[tokio::test]
async fn providers_without_a_list_fall_back_to_what_is_written() {
    let server = MockServer::start().await;
    let dir = tempfile::tempdir().unwrap();
    let llm = registry(&server, dir.path());
    // Nothing mounted: 404.
    let refresh = llm.refresh_list("none", None).await.unwrap();
    assert_eq!(refresh.source, ListSource::Unsupported);
    // Without a list, the catalog's models and the written ones.
    let refresh = llm.refresh_list("oai", None).await.unwrap();
    assert_eq!(refresh.source, ListSource::Unsupported);
    let models = llm.directory().models(&llm.provider("oai").unwrap());
    assert_eq!(models.source, IdSource::Catalog);
    assert!(models.get("deepseek-flash").is_some() && models.get("written").is_some());
}

#[tokio::test]
async fn ollama_details_fill_the_window() {
    let server = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path("/v1/models"))
        .respond_with(ResponseTemplate::new(200).set_body_json(json!({"data": [{"id": "qwen3-coder:30b"}]})))
        .mount(&server)
        .await;
    Mock::given(method("POST"))
        .and(path("/api/show"))
        .and(body_json(json!({"model": "qwen3-coder:30b"})))
        .respond_with(ResponseTemplate::new(200).set_body_json(json!({
            "model_info": {"qwen3moe.context_length": 262144},
            "capabilities": ["completion", "tools"]
        })))
        .mount(&server)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let llm = registry(&server, dir.path());
    llm.refresh_list("local", None).await.unwrap();
    let model = llm.resolve("local/qwen3-coder:30b").unwrap().model;
    assert_eq!(model.context_window, 262_144);
    let models = llm.directory().models(&llm.provider("local").unwrap());
    assert!(models.get("qwen3-coder:30b").unwrap().meta.known);
}

#[tokio::test]
async fn the_catalog_refreshes_with_etags_and_refuses_a_broken_one() {
    let server = MockServer::start().await;
    let mut upstream = json!({"deepseek": {"models": {
        "deepseek-flash": {"id": "deepseek-flash", "name": "Fresh Flash", "limit": {"context": 2000000, "output": 1000}}
    }}});
    Mock::given(method("GET"))
        .and(path("/api.json"))
        .and(header("if-none-match", "\"v1\""))
        .respond_with(ResponseTemplate::new(304))
        .mount(&server)
        .await;
    Mock::given(method("GET"))
        .and(path("/api.json"))
        .respond_with(ResponseTemplate::new(200).insert_header("etag", "\"v1\"").set_body_json(&upstream))
        .up_to_n_times(1)
        .mount(&server)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let llm = registry(&server, dir.path());
    let url = format!("{}/api.json", server.uri());
    assert!(llm.refresh_catalog(&url).await.unwrap());
    assert_eq!(llm.resolve("oai/deepseek-flash").unwrap().model.context_window, 2_000_000);
    // Not modified: nothing changes, the check is remembered.
    assert!(!llm.refresh_catalog(&url).await.unwrap());
    let state = her_llm::public_catalog::read_state(&dir.path().join("catalog"));
    assert_eq!(state.etag.as_deref(), Some("\"v1\""));
    assert!(state.checked_at.is_some());

    // A catalog that does not project is not taken.
    let broken = MockServer::start().await;
    upstream["deepseek"]["models"]["deepseek-flash"]["limit"]["context"] = json!("huge");
    Mock::given(method("GET")).respond_with(ResponseTemplate::new(200).set_body_json(&upstream)).mount(&broken).await;
    let err = llm.refresh_catalog(&format!("{}/api.json", broken.uri())).await.unwrap_err();
    assert_eq!(err.kind, ErrorKind::Protocol);
    assert_eq!(llm.resolve("oai/deepseek-flash").unwrap().model.context_window, 2_000_000);
}

#[tokio::test]
async fn a_model_the_provider_does_not_have_marks_its_list_for_refresh() {
    use her_llm::Context;
    use her_llm::Message;
    use her_llm::RequestOptions;
    use her_llm::StreamEvent;
    use her_llm::UserMessage;
    use futures::StreamExt;
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/v1/chat/completions"))
        .respond_with(ResponseTemplate::new(400).set_body_json(json!({"error": {"message": "Model Not Exist", "type": "invalid_request_error"}})))
        .mount(&server)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let llm = registry(&server, dir.path());
    let model = llm.resolve("oai/retired-model").unwrap().model;
    let context = Context { system: String::new(), messages: vec![Message::from(UserMessage::text("hi"))], tools: vec![] };
    let mut stream = llm.stream(model, context, RequestOptions::default());
    let mut failed = false;
    while let Some(event) = stream.next().await {
        failed |= matches!(event, StreamEvent::Failed(_));
    }
    assert!(failed);
    assert_eq!(llm.directory().take_missing(), std::collections::BTreeSet::from(["oai".to_string()]));
    assert!(llm.directory().take_missing().is_empty(), "taken once");
}

#[tokio::test]
async fn a_plan_request_uses_its_own_key_and_never_falls_back_to_the_regular_api() {
    use her_llm::{Context, Message, RequestOptions, StreamEvent, UserMessage};
    use futures::StreamExt;
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/plan/v3/chat/completions"))
        .and(header("authorization", "Bearer sk-plan-fixture"))
        .respond_with(ResponseTemplate::new(401).set_body_json(json!({"error": {"message": "plan expired"}})))
        .expect(1)
        .mount(&server)
        .await;
    let catalog = Catalog::from_toml(&format!(r#"
        [providers.plan]
        api = "openai-chat"
        base_url = "{uri}/plan/v3"
        api_key = "sk-plan-fixture"
        list_models = false
        plan = {{ key_url = "https://example.invalid/keys" }}
        [[providers.plan.models]]
        id = "shared-model"

        [providers.regular]
        api = "openai-chat"
        base_url = "{uri}/api/v3"
        api_key = "sk-payg-fixture"
        list_models = false
        [[providers.regular.models]]
        id = "shared-model"
        "#, uri = server.uri())).unwrap();
    let llm = ModelRegistry::new(catalog, RegistryOptions::default());
    let model = llm.resolve("plan/shared-model").unwrap().model;
    let context = Context { system: String::new(), messages: vec![Message::from(UserMessage::text("hi"))], tools: vec![] };
    let mut stream = llm.stream(model, context, RequestOptions::default());
    let mut failed = false;
    while let Some(event) = stream.next().await {
        failed |= matches!(event, StreamEvent::Failed(_));
    }
    assert!(failed);
    assert_eq!(server.received_requests().await.unwrap().len(), 1, "no request to the ordinary API");
}
