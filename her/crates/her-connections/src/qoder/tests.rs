// Live diagnostics report progress on stderr.
#![allow(clippy::print_stderr)]
use super::transport::Http;
use super::*;
use parking_lot::Mutex;
use std::{
    collections::BTreeMap,
    sync::atomic::{AtomicBool, AtomicI64, Ordering},
};
use wiremock::{
    Mock, MockServer, ResponseTemplate,
    matchers::{body_json, method, path},
};
struct Env {
    now: AtomicI64,
    source_expires: AtomicI64,
    identity: Mutex<String>,
    store: Mutex<BTreeMap<String, String>>,
    fail_save: AtomicBool,
}
impl Env {
    fn new() -> Arc<Self> {
        Arc::new(Self {
            now: AtomicI64::new(1_000_000),
            source_expires: AtomicI64::new(100_000_000),
            identity: Mutex::new("account-a".into()),
            store: Mutex::new(BTreeMap::new()),
            fail_save: AtomicBool::new(false),
        })
    }
}
#[async_trait]
impl Environment for Env {
    async fn discover(&self) -> Result<her_protocol::api::ConnectionDiscovery> {
        unreachable!()
    }
    async fn login(&self) -> Result<Login> {
        Ok(Login {
            token: "source-access".into(),
            expires: self.source_expires.load(Ordering::Relaxed),
            identity: self.identity.lock().clone(),
            subject: Some("user-a".into()),
        })
    }
    async fn headers(&self) -> Result<BTreeMap<String, String>> {
        Ok(BTreeMap::new())
    }
    async fn get(&self, id: &str) -> Result<Option<String>> {
        Ok(self.store.lock().get(id).cloned())
    }
    async fn set(&self, id: &str, v: &str) -> Result<()> {
        if self.fail_save.load(Ordering::Relaxed) {
            return Err(ConnectionError::new(
                "connection_permission_required",
                "save denied",
            ));
        }
        self.store.lock().insert(id.into(), v.into());
        Ok(())
    }
    async fn delete(&self, id: &str) -> Result<()> {
        self.store.lock().remove(id);
        Ok(())
    }
    fn now(&self) -> i64 {
        self.now.load(Ordering::Relaxed)
    }
}
fn pair(token: &str, refresh: &str) -> Value {
    json!({"token":token,"refresh_token":refresh,"expires_in":86_400_000,"refresh_token_expires_in":172_800_000})
}
async fn setup() -> (MockServer, Arc<Env>, Qoder, reqwest::Client) {
    let server = MockServer::start().await;
    let env = Env::new();
    let mut q = Qoder::new("connection".into(), "account-a".into(), env.clone());
    q.openapi = server.uri();
    q.catalog_url = format!("{}/algo/api/v2/model/list?Encode=1", server.uri());
    (
        server,
        env,
        q,
        her_llm::ModelRegistry::new(
            her_llm::Catalog::builtin().unwrap(),
            her_llm::RegistryOptions::default(),
        )
        .http_client()
        .unwrap(),
    )
}
async fn issue(server: &MockServer) {
    Mock::given(method("POST"))
        .and(path("/api/v1/me/jobToken"))
        .and(body_json(json!({"clientId":CLIENT_ID})))
        .respond_with(ResponseTemplate::new(200).set_body_json(pair("job-a", "refresh-a")))
        .expect(1)
        .mount(server)
        .await;
}
#[tokio::test]
async fn issue_and_reload_use_independent_pair_without_account_refresh() {
    let (s, env, mut q, http) = setup().await;
    issue(&s).await;
    let a = q.authorize(&http, false).await.unwrap();
    assert_eq!(a.credentials.api_key.as_deref(), Some("job-a"));
    assert_eq!(q.saved.job.as_ref().unwrap().expires, 87_400_000);
    let mut other = Qoder::new("connection".into(), "account-a".into(), env);
    other.openapi = s.uri();
    other.authorize(&http, false).await.unwrap();
    assert_eq!(s.received_requests().await.unwrap().len(), 1);
}
#[tokio::test]
async fn refresh_rotates_entire_pair_even_when_source_access_expired() {
    let (s, env, mut q, http) = setup().await;
    issue(&s).await;
    q.authorize(&http, false).await.unwrap();
    env.source_expires.store(0, Ordering::Relaxed);
    Mock::given(path("/api/v1/jobToken/refresh"))
        .and(body_json(json!({"refresh_token":"refresh-a"})))
        .respond_with(ResponseTemplate::new(200).set_body_json(pair("job-b", "refresh-b")))
        .expect(1)
        .mount(&s)
        .await;
    q.authorize(&http, true).await.unwrap();
    let saved: Saved = serde_json::from_str(env.store.lock().get("connection").unwrap()).unwrap();
    assert_eq!(saved.job.unwrap().refresh_token, "refresh-b");
}
#[tokio::test]
async fn save_failure_retries_persistence_without_reusing_spent_refresh() {
    let (s, env, mut q, http) = setup().await;
    issue(&s).await;
    q.authorize(&http, false).await.unwrap();
    Mock::given(path("/api/v1/jobToken/refresh"))
        .respond_with(ResponseTemplate::new(200).set_body_json(pair("job-b", "refresh-b")))
        .expect(1)
        .mount(&s)
        .await;
    env.fail_save.store(true, Ordering::Relaxed);
    assert!(q.authorize(&http, true).await.is_err());
    env.fail_save.store(false, Ordering::Relaxed);
    let a = q.authorize(&http, false).await.unwrap();
    assert_eq!(a.credentials.api_key.as_deref(), Some("job-b"));
}
#[tokio::test]
async fn account_switch_deletes_only_own_record() {
    let (s, env, mut q, http) = setup().await;
    issue(&s).await;
    q.authorize(&http, false).await.unwrap();
    *env.identity.lock() = "account-b".into();
    assert_eq!(
        q.authorize(&http, false).await.err().unwrap().code,
        "identity_changed"
    );
    assert!(env.store.lock().is_empty());
}
#[tokio::test]
async fn listing_preserves_other_models_without_publishing_them() {
    let (s, _, mut q, http) = setup().await;
    issue(&s).await;
    Mock::given(path("/algo/api/v2/model/list")).respond_with(ResponseTemplate::new(200).set_body_json(json!({"app":[{"key":"auto","display_name":"Auto","enable":true,"max_input_tokens":200000},{"key":"qfmodel","enable":true},{"key":"disabled","enable":false}]}))).expect(1).mount(&s).await;
    let models = q.catalog(&http).await.unwrap();
    assert_eq!(
        models.iter().map(|m| m.id.as_str()).collect::<Vec<_>>(),
        ["auto", "qfmodel"]
    );
}
#[test]
fn source_rejects_unknown_schema_and_dates() {
    assert!(source_login(&json!({"schemaVersion":2})).is_err());
    assert!(parse_job(&json!({"token":"a","refresh_token":"b","expires_in":86400,"refresh_token_expires_in":0}),1_000_000).is_err());
}

/// Explicit opt-in: reads local Qoder login, creates a private her connection and sends a small request.
#[tokio::test]
#[ignore = "requires local Qoder login and an explicit live test"]
async fn live_qoder_connection() {
    use her_llm::{
        Catalog, Context, Message, ModelRegistry, RegistryOptions, RequestOptions, StreamEvent,
        UserMessage,
    };
    use futures::StreamExt;
    // An explicitly supplied home tests restore/inference of an existing connection,
    // without reconnecting or rewriting its public catalog/configuration.
    let existing_home = std::env::var_os("HER_QODER_TEST_HOME").map(std::path::PathBuf::from);
    let home = existing_home
        .clone()
        .unwrap_or_else(|| dirs::home_dir().unwrap().join(".her-qoder-e2e"));
    tokio::fs::create_dir_all(&home).await.unwrap();
    let env = crate::platform::system_for(&home, "qoder");
    let source = env.interactive_login().await.unwrap();
    assert!(source.subject.is_some());
    let llm = Arc::new(ModelRegistry::new(
        Catalog::builtin().unwrap(),
        RegistryOptions {
            retry: her_llm::RetryPolicy {
                max_retries: Some(0),
                ..Default::default()
            },
            ..Default::default()
        },
    ));
    let manager = crate::Manager::with_environments(
        home.clone(),
        &llm,
        BTreeMap::from([("qoder".into(), env)]),
    );
    let initial = manager.snapshot().unwrap();
    let snapshot = if existing_home.is_some() {
        assert!(
            !initial.connections.is_empty(),
            "expected an existing Qoder connection"
        );
        initial
    } else if let Some(c) = initial.connections.first() {
        manager.retry(&c.connection_id).await.unwrap()
    } else {
        manager
            .connect(her_protocol::api::ConnectionConnectParams {
                adapter_id: "qoder".into(),
                discovery_id: "qoder-standard".into(),
                idempotency_key: "live-qoder".into(),
            })
            .await
            .unwrap()
    };
    let c = &snapshot.connections[0];
    assert!(c.models.iter().any(|m| m.id == "auto"));
    let provider = llm.provider(&c.provider_id).unwrap();
    assert_eq!(
        provider.listing.entries.len(),
        c.models.iter().filter(|m| supports_model(&m.id)).count()
    );
    let model_id = std::env::var("HER_QODER_TEST_MODEL").unwrap_or_else(|_| "auto".into());
    let model = llm
        .lookup(&provider, &model_id)
        .expect("model must be published");
    // Cantus rejects some general chat intents; validate its actual coding use case.
    let text_prompt = if model_id == "cmodel" {
        "Implement add(a,b) in Python. Start with the comment # HER_QODER_RUST_OK."
    } else {
        "Reply with exactly HER_QODER_RUST_OK."
    };
    let context = Context {
        system: text_prompt.into(),
        messages: vec![Message::User(UserMessage::text(text_prompt).into())],
        tools: vec![],
    };
    let mut stream = llm.stream(model.clone(), context, RequestOptions::default());
    let mut success = false;
    while let Some(event) = stream.next().await {
        match event {
            StreamEvent::Done(message) => {
                assert!(
                    message.text().contains("HER_QODER_RUST_OK"),
                    "text={:?}, stop={:?}, usage={:?}",
                    message.text().chars().take(500).collect::<String>(),
                    message.stop_reason,
                    message.usage
                );
                eprintln!("Qoder live: text/system response passed");
                success = true;
            }
            StreamEvent::Failed(message) => panic!(
                "live inference failed: {:?}",
                message
                    .error
                    .as_ref()
                    .map(|e| (e.kind, &e.provider_code, &e.message))
            ),
            _ => {}
        }
    }
    assert!(success);

    // Use a synthetic tool/result only; never execute a local command during this probe.
    let mut context = Context {
        system: if model_id == "cmodel" {
            "Write Python code. Before implementing add(a,b), call her_probe with text=你好 to obtain the required source header. Include the returned header as a comment in the code."
        } else {
            "Call her_probe with text=你好. After its result, reply with exactly the result text."
        }.into(),
        messages: vec![UserMessage::text(if model_id == "cmodel" {
            "Implement add(a,b) in Python using the required header from her_probe."
        } else { "Run the probe now." }).into()],
        tools: vec![her_llm::ToolSpec {
            name: "her_probe".into(),
            description: "Returns a test marker.".into(),
            parameters: json!({"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}),
        }],
    };
    let mut stream = llm.stream(model.clone(), context.clone(), RequestOptions::default());
    let mut assistant = None;
    while let Some(event) = stream.next().await {
        match event {
            StreamEvent::Done(message) => assistant = Some(message),
            StreamEvent::Failed(message) => panic!("live tool call failed: {:?}", message.error),
            _ => {}
        }
    }
    let assistant = assistant.expect("tool response");
    let calls = assistant.tool_calls();
    assert_eq!(calls.len(), 1);
    let call = calls[0];
    assert_eq!(call.name, "her_probe");
    assert!(!call.id.is_empty());
    assert!(call.arguments_error.is_none());
    assert_eq!(call.arguments.to_value()["text"], "你好");
    eprintln!("Qoder live: tool name, ID and Unicode JSON arguments passed");
    let result = her_llm::ToolResultMessage {
        call_id: call.id.clone(),
        tool_name: call.name.clone(),
        content: vec![her_llm::UserContent::Text {
            text: "HER_TOOL_ROUNDTRIP_OK".into(),
        }],
        is_error: false,
        details: None,
        ts: Default::default(),
        duration_ms: None,
    };
    context.messages.push(assistant.into());
    context.messages.push(result.into());
    let mut stream = llm.stream(model.clone(), context, RequestOptions::default());
    let mut success = false;
    while let Some(event) = stream.next().await {
        match event {
            StreamEvent::Done(message) => {
                assert!(message.text().contains("HER_TOOL_ROUNDTRIP_OK"));
                eprintln!("Qoder live: tool result roundtrip passed");
                success = true;
            }
            StreamEvent::Failed(message) => panic!("live tool result failed: {:?}", message.error),
            _ => {}
        }
    }
    assert!(success);
}

#[tokio::test]
async fn offline_past_refresh_expiry_reissues_from_source() {
    let (s, env, mut q, http) = setup().await;
    issue(&s).await;
    q.authorize(&http, false).await.unwrap();
    s.reset().await;
    env.now.store(180_000_000, Ordering::Relaxed);
    env.source_expires.store(300_000_000, Ordering::Relaxed);
    Mock::given(path("/api/v1/me/jobToken"))
        .respond_with(ResponseTemplate::new(200).set_body_json(pair("reissued", "new-refresh")))
        .expect(1)
        .mount(&s)
        .await;
    assert_eq!(
        q.authorize(&http, false)
            .await
            .unwrap()
            .credentials
            .api_key
            .as_deref(),
        Some("reissued")
    );
    assert_eq!(s.received_requests().await.unwrap().len(), 1);
}
#[tokio::test]
async fn uncertain_refresh_never_retries_spent_token() {
    let (s, _, mut q, http) = setup().await;
    issue(&s).await;
    q.authorize(&http, false).await.unwrap();
    s.reset().await;
    Mock::given(path("/api/v1/jobToken/refresh"))
        .respond_with(ResponseTemplate::new(503))
        .expect(1)
        .mount(&s)
        .await;
    Mock::given(path("/api/v1/me/jobToken"))
        .respond_with(ResponseTemplate::new(200).set_body_json(pair("reissued", "new-refresh")))
        .expect(1)
        .mount(&s)
        .await;
    assert_eq!(
        q.authorize(&http, true)
            .await
            .unwrap()
            .credentials
            .api_key
            .as_deref(),
        Some("reissued")
    );
    q.authorize(&http, false).await.unwrap();
}
#[tokio::test]
async fn strict_transport_rejects_partial_and_malformed_tools_and_keeps_system() {
    use her_llm::{
        AuthConfig, Compat, Context, Message, ProviderConfig, RequestOptions, StreamEvent,
        UserMessage, WireApi, WireRequest,
    };
    use futures::StreamExt;
    let (s, _, _, http) = setup().await;
    let provider = ProviderConfig {
        plan: None,
        id: "local-test".into(),
        name: "test".into(),
        names: Default::default(),
        api: "openai-chat".into(),
        base_url: s.uri(),
        auth: AuthConfig::Managed,
        headers: BTreeMap::new(),
        currency: Default::default(),
        compat: Compat::default(),
        listing: Default::default(),
    };
    for response in [
        "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
        "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"function\":{\"name\":\"probe\",\"arguments\":\"{broken\"}}]},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
    ] {
        s.reset().await;
        Mock::given(path("/chat/completions"))
            .respond_with(
                ResponseTemplate::new(200)
                    .insert_header("content-type", "text/event-stream")
                    .set_body_string(response),
            )
            .mount(&s)
            .await;
        // Qoder's catalog always gives a limit (capabilities.rs).
        let mut model = provider.ad_hoc_model("auto");
        model.max_output_tokens = Some(32_000);
        let events: Vec<_> = Http
            .stream(WireRequest {
                model: Arc::new(model),
                credentials: Credentials::with_key("private".into()),
                context: Arc::new(Context {
                    system: "her system".into(),
                    messages: vec![Message::from(UserMessage::text("hi"))],
                    tools: vec![],
                }),
                options: RequestOptions {
                    extra_body: serde_json::from_value(
                        json!({"model":"injected","messages":[],"metadata":{"token":"bad"}}),
                    )
                    .unwrap(),
                    ..Default::default()
                },
                http: http.clone(),
                timeouts: Default::default(),
            })
            .collect()
            .await;
        assert!(
            matches!(events.last(),Some(StreamEvent::Failed(m)) if m.error.as_ref().unwrap().kind==her_llm::ErrorKind::Protocol)
        );
        let req = &s.received_requests().await.unwrap()[0];
        let body: Value = serde_json::from_slice(&req.body).unwrap();
        assert_eq!(body["model"], "auto");
        assert_eq!(body["messages"][0]["content"], "her system");
        assert_eq!(body["metadata"]["business"]["product"], "app");
        assert!(body["metadata"].get("token").is_none());
        assert!(body.get("max_completion_tokens").is_none());
        assert!(body["max_tokens"].as_u64().unwrap() > 0);
        let context = &body["metadata"]["context"];
        assert_eq!(context["client_type"], "10");
        assert_eq!(
            req.headers["x-request-id"],
            context["request_id"].as_str().unwrap()
        );
        assert_eq!(
            req.headers["x-session-id"],
            context["session_id"].as_str().unwrap()
        );
        assert_eq!(
            context["request_set_id"],
            body["metadata"]["business"]["id"]
        );
    }
}

#[tokio::test]
async fn transport_surfaces_qoder_error_events_and_preserves_partial_content() {
    use her_llm::{Context, RequestOptions, StreamEvent, WireApi, WireRequest};
    use futures::StreamExt;
    let (server, _, _, http) = setup().await;
    let provider = her_llm::ProviderConfig {
        plan: None,
        id: "test".into(),
        name: "test".into(),
        names: Default::default(),
        api: "openai-chat".into(),
        base_url: server.uri(),
        auth: her_llm::AuthConfig::Managed,
        headers: BTreeMap::new(),
        currency: Default::default(),
        compat: Default::default(),
        listing: Default::default(),
    };
    let model = Arc::new(provider.ad_hoc_model("auto"));
    for prefix in [
        "",
        "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
    ] {
        server.reset().await;
        Mock::given(path("/chat/completions"))
            .respond_with(ResponseTemplate::new(200).insert_header("content-type", "text/event-stream")
                .set_body_string(format!("{prefix}event: error\ndata: {{\"code\":\"provider_error\",\"message\":\"All backends failed\",\"request_id\":\"private-request-id\",\"param\":{{\"retries\":0}}}}\n\n")))
            .expect(1).mount(&server).await;
        let events: Vec<_> = Http
            .stream(WireRequest {
                model: model.clone(),
                credentials: Credentials::with_key("private".into()),
                context: Arc::new(Context::default()),
                options: RequestOptions::default(),
                http: http.clone(),
                timeouts: Default::default(),
            })
            .collect()
            .await;
        let Some(StreamEvent::Failed(message)) = events.last() else {
            panic!("expected failure")
        };
        let error = message.error.as_ref().unwrap();
        assert_eq!(error.kind, her_llm::ErrorKind::Server);
        assert_eq!(error.provider_code.as_deref(), Some("provider_error"));
        assert_eq!(error.message, "All backends failed");
        assert_eq!(
            message.text(),
            if prefix.is_empty() { "" } else { "partial" }
        );
        assert!(!events.iter().any(|e| matches!(e, StreamEvent::Done(_))));
    }
    assert!(
        model.compat.max_tokens_field.is_none(),
        "shared model must not change"
    );
    assert!(model.headers.is_empty());
}

#[tokio::test]
async fn catalog_only_models_cannot_be_invoked_through_ad_hoc_references() {
    use her_llm::connection::ConnectionAuthorizer;
    let dir = tempfile::tempdir().unwrap();
    let llm = Arc::new(her_llm::ModelRegistry::new(
        her_llm::Catalog::builtin().unwrap(),
        her_llm::RegistryOptions::default(),
    ));
    let id = uuid::Uuid::new_v4().to_string();
    let models = ["auto", "qfmodel", "future-unverified-model"]
        .into_iter()
        .map(|id| ConnectionModel {
            id: id.into(),
            name: id.into(),
            names: Default::default(),
            context_window: None,
            max_output_tokens: None,
            reasoning: false,
            images: false,
            thinking: Vec::new(),
            default_thinking: Default::default(),
        })
        .collect();
    let record = crate::Record {
        connection_id: id.clone(),
        // Written before provider IDs were kept: published as `local-<connection_id>`.
        provider_id: String::new(),
        adapter_id: "qoder".into(),
        identity_key: "account-a".into(),
        enabled: true,
        idempotency_key: "test".into(),
        models,
        models_fetched_at: None,
    };
    std::fs::create_dir_all(dir.path().join("connections")).unwrap();
    std::fs::write(
        dir.path().join("connections/config.json"),
        serde_json::to_vec(&crate::Config {
            schema: 1,
            connections: vec![record],
        })
        .unwrap(),
    )
    .unwrap();
    let manager = crate::Manager::with_environments(
        dir.path().into(),
        &llm,
        BTreeMap::from([("qoder".into(), Env::new() as Arc<dyn Environment>)]),
    );
    let provider = format!("local-{id}");
    let published = llm.provider(&provider).unwrap();
    assert_eq!(published.listing.entries.len(), 2);
    assert!(llm.lookup(&published, "qfmodel").is_some());
    assert!(
        manager
            .begin(&provider, "qfmodel", llm.http_client().unwrap())
            .await
            .is_ok()
    );
    assert_eq!(
        manager
            .begin(
                &provider,
                "future-unverified-model",
                llm.http_client().unwrap()
            )
            .await
            .err()
            .unwrap()
            .provider_code
            .as_deref(),
        Some("unsupported_capability")
    );
}

/// Exports only model capability declarations, never account or routing data.
#[tokio::test]
#[ignore = "requires local Qoder login and explicit live test"]
async fn live_qoder_catalog_capabilities() {
    let home =
        std::path::PathBuf::from(std::env::var_os("HER_QODER_TEST_HOME").expect("explicit home"));
    let env = crate::platform::system_for(&home, "qoder");
    let source = env.interactive_login().await.unwrap();
    let config: Value =
        serde_json::from_slice(&std::fs::read(home.join("connections/config.json")).unwrap())
            .unwrap();
    let c = config["connections"]
        .as_array()
        .unwrap()
        .iter()
        .find(|c| c["adapter_id"] == "qoder")
        .unwrap();
    let mut q = Qoder::new(
        c["connection_id"].as_str().unwrap().into(),
        source.identity,
        env,
    );
    let http =
        her_llm::ModelRegistry::new(her_llm::Catalog::builtin().unwrap(), Default::default())
            .http_client()
            .unwrap();
    let value = q.catalog_value(&http).await.unwrap();
    let fields = [
        "key",
        "display_name",
        "enable",
        "max_input_tokens",
        "max_output_tokens",
        "is_reasoning",
        "is_vl",
        "context_config",
        "thinking_config",
        "efforts",
        "supports_disabled",
        "default_effort",
        "available_context_windows",
        "default_context_window",
    ];
    let entries: Vec<Value> = value["app"]
        .as_array()
        .unwrap()
        .iter()
        .map(|m| {
            Value::Object(
                fields
                    .iter()
                    .filter_map(|k| m.get(*k).map(|v| (k.to_string(), v.clone())))
                    .collect(),
            )
        })
        .collect();
    let out = std::env::var_os("HER_QODER_CAPABILITIES_OUT").expect("explicit output");
    std::fs::write(out, serde_json::to_vec_pretty(&entries).unwrap()).unwrap();
    eprintln!("Exported {} model declarations", entries.len());
}

/// Reproducible capability observations through Manager -> registry -> Qoder transport.
/// Uses a temporary public catalog, existing credentials, synthetic inputs, no real tools.
#[tokio::test]
#[ignore = "sends billable live requests for every declared Qoder capability"]
async fn live_qoder_capability_matrix() {
    use base64::Engine;
    use her_llm::{
        AssistantContent, Catalog, Context, Message, ModelRegistry, RegistryOptions,
        RequestOptions, StreamEvent, ThinkingLevel, UserContent, UserMessage,
    };
    use futures::{StreamExt, stream};
    let home =
        std::path::PathBuf::from(std::env::var_os("HER_QODER_TEST_HOME").expect("explicit home"));
    let declarations: Vec<Value> = serde_json::from_slice(
        &std::fs::read(std::env::var_os("HER_QODER_CATALOG_IN").expect("fresh sanitized catalog"))
            .unwrap(),
    )
    .unwrap();
    let env = crate::platform::system_for(&home, "qoder");
    env.interactive_login().await.unwrap();
    let mut config: Value =
        serde_json::from_slice(&std::fs::read(home.join("connections/config.json")).unwrap())
            .unwrap();
    let records = config["connections"].as_array_mut().unwrap();
    records.retain(|c| c["adapter_id"] == "qoder");
    assert_eq!(records.len(), 1);
    records[0]["models"] = serde_json::to_value(
        declarations
            .iter()
            .filter(|m| m["enable"] == true)
            .filter_map(super::capabilities::model)
            .collect::<Vec<_>>(),
    )
    .unwrap();
    let temp = tempfile::tempdir().unwrap();
    std::fs::create_dir_all(temp.path().join("connections")).unwrap();
    std::fs::write(
        temp.path().join("connections/config.json"),
        serde_json::to_vec(&config).unwrap(),
    )
    .unwrap();
    let llm = Arc::new(ModelRegistry::new(
        Catalog::builtin().unwrap(),
        RegistryOptions {
            retry: her_llm::RetryPolicy {
                max_retries: Some(0),
                ..Default::default()
            },
            ..Default::default()
        },
    ));
    let manager = crate::Manager::with_environments(
        temp.path().into(),
        &llm,
        BTreeMap::from([("qoder".into(), env)]),
    );
    let snapshot = manager.snapshot().unwrap();
    let provider = llm.provider(&snapshot.connections[0].provider_id).unwrap();
    let filter = std::env::var("HER_QODER_TEST_MODEL").ok();
    let mode = std::env::var("HER_QODER_TEST_MODE").unwrap_or_default();
    let mut jobs = Vec::new();
    let published = llm.directory().models(&provider);
    for model in published.models.iter().map(|m| &m.model) {
        if filter.as_ref().is_some_and(|f| f != &model.id) {
            continue;
        }
        if mode == "parameters" {
            jobs.push((model.clone(), "parameters".into(), ThinkingLevel::Default));
            continue;
        }
        if mode.is_empty() {
            jobs.push((model.clone(), "parameters".into(), ThinkingLevel::Default));
        }
        if mode != "thinking" {
            jobs.push((model.clone(), "image".to_string(), ThinkingLevel::Default));
        }
        if mode != "image" {
            jobs.push((model.clone(), "thinking".into(), ThinkingLevel::Default));
            for level in &model.thinking {
                jobs.push((model.clone(), "thinking".into(), *level));
            }
        }
    }
    let llm_ref = &llm;
    let observations: Vec<Value> = stream::iter(jobs).map(|(model, kind, level)| async move {
        let mut user = UserMessage::text(if kind == "image" {
            "For a UI snapshot unit test, inspect both attached images. For each image list the four quadrant colors in row-major order (top-left, top-right, bottom-left, bottom-right). Return only the eight lowercase color names separated by commas, image 1 then image 2."
        } else {
            "Implement a Python function that returns 17 * 23 - (41 + 79). Include the computed integer in a comment. Keep the code short."
        });
        if kind == "image" {
            for png in [include_bytes!("fixtures/quadrants-1.png").as_slice(), include_bytes!("fixtures/quadrants-2.png").as_slice()] {
                user.content.push(UserContent::Image { mime_type: "image/png".into(), data: base64::engine::general_purpose::STANDARD.encode(png) });
            }
        }
        let context = Context { system: String::new(), messages: vec![Message::User(user.into())], tools: vec![] };
        let start = std::time::Instant::now();
        let outcome = tokio::time::timeout(Duration::from_secs(150), async {
            let mut events = llm_ref.stream(model.clone(), context, RequestOptions {
                thinking: level, max_output_tokens: (kind != "parameters").then_some(4096),
                temperature: (kind == "parameters").then_some(if matches!(model.id.as_str(), "kmodel" | "kmodel_latest") { 1.0 } else { 0.25 }), ..Default::default()
            });
            let mut result = json!({"status":"unknown", "reason":"no_terminal_event"});
            while let Some(event) = events.next().await {
                match event {
                    StreamEvent::Done(message) => {
                        let text = message.text();
                        let normalized: String = text.to_lowercase().chars().filter(char::is_ascii_alphabetic).collect();
                        let valid = if kind == "image" { normalized.contains("blueyellowredgreengreenredyellowblue") } else { text.contains("271") };
                        let reasoning_chars: usize = message.content.iter().filter_map(|c| match c {
                            AssistantContent::Thinking { text, .. } => Some(text.chars().count()), _ => None
                        }).sum();
                        result = json!({"status": if valid {"passed"} else {"unknown"},
                            "reason": if valid {"expected_answer"} else {"answer_mismatch"},
                            "reasoning_chars": reasoning_chars, "stop_reason": message.stop_reason,
                            "usage": message.usage});
                    }
                    StreamEvent::Failed(message) => {
                        result = json!({"status":"unknown", "reason":"request_failed",
                            "error_kind": message.error.as_ref().map(|e| format!("{:?}",e.kind)),
                            "provider_code": message.error.as_ref().and_then(|e| e.provider_code.as_ref()).map(|s| crate::adapter::scrub(s))});
                    }
                    _ => {}
                }
            }
            result
        }).await.unwrap_or_else(|_| json!({"status":"unknown", "reason":"timeout"}));
        let row = json!({"model":model.id, "probe":kind, "thinking":level,
            "requested_max_tokens": if kind == "parameters" { model.default_max_output() } else {Some(4096)},
            "requested_temperature": if kind == "parameters" { Some(if matches!(model.id.as_str(), "kmodel" | "kmodel_latest") {1.0} else {0.25}) } else {None},
            "elapsed_ms":start.elapsed().as_millis(), "observation":outcome});
        eprintln!("Qoder capability {} {} {}: {}", model.id, kind, level, outcome);
        row
    }).buffer_unordered(2).collect().await;
    let report = json!({"schema_version":1, "observed_at":chrono::Utc::now().to_rfc3339(),
        "qoder_app_version":"0.4.11", "declarations":declarations, "observations":observations,
        "interpretation": {"passed":"The synthetic answer matched; an effort probe proves request acceptance, not intensity semantics.",
            "unknown":"Missing declaration, untested behavior or inconclusive/failed probe; never inferred as unsupported.",
            "output_limit":"Missing in catalog; effective 32000 is Qoder worker KT fallback, not a measured maximum.",
            "audio_video":"Unknown; Her UserContent only represents text and images."}});
    std::fs::write(
        std::env::var_os("HER_QODER_CAPABILITIES_OUT").expect("explicit report path"),
        serde_json::to_vec_pretty(&report).unwrap(),
    )
    .unwrap();
}

#[tokio::test]
async fn capability_controls_preserve_images_and_only_caller_prompt_and_tools() {
    use her_llm::{
        AuthConfig, Compat, Context, ProviderConfig, RequestOptions, ThinkingLevel, UserContent,
        UserMessage, WireApi, WireRequest,
    };
    use futures::StreamExt;
    let (server, _, _, http) = setup().await;
    let provider = ProviderConfig {
        plan: None,
        id: "qoder-test".into(),
        name: "test".into(),
        names: Default::default(),
        api: "openai-chat".into(),
        base_url: server.uri(),
        auth: AuthConfig::Managed,
        headers: BTreeMap::new(),
        currency: Default::default(),
        compat: Compat {
            thinking_format: crate::adapter::thinking_format("qoder"),
            ..Default::default()
        },
        listing: Default::default(),
    };
    let mut model = provider.ad_hoc_model("qfmodel");
    model.reasoning = true;
    model.context_window = 200_000;
    model.thinking = vec![ThinkingLevel::Off, ThinkingLevel::Low, ThinkingLevel::Xhigh];
    model.input.push(her_llm::model::Modality::Image);
    let context = Arc::new(Context {
        system: "Caller system only".into(),
        messages: vec![
            UserMessage {
                content: vec![
                    UserContent::Text {
                        text: "Inspect this image".into(),
                    },
                    UserContent::Image {
                        mime_type: "image/png".into(),
                        data: "synthetic-base64".into(),
                    },
                ],
                ts: Default::default(),
            }
            .into(),
        ],
        tools: vec![her_llm::ToolSpec {
            name: "caller_tool".into(),
            description: "Caller tool".into(),
            parameters: json!({"type":"object"}),
        }],
    });
    for level in [
        ThinkingLevel::Default,
        ThinkingLevel::Off,
        ThinkingLevel::Low,
        ThinkingLevel::Xhigh,
        ThinkingLevel::High,
    ] {
        server.reset().await;
        Mock::given(path("/chat/completions"))
            .respond_with(ResponseTemplate::new(200).insert_header("content-type", "text/event-stream")
                .set_body_string("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
            .mount(&server).await;
        let _: Vec<_> = Http.stream(WireRequest {
            model: Arc::new(model.clone()), credentials: Credentials::with_key("test".into()),
            context: context.clone(), options: RequestOptions { thinking: level, temperature: Some(0.25), max_output_tokens: Some(123),
                extra_body: serde_json::from_value(json!({"reasoning_effort":"max", "enable_thinking":true, "context_length":1000000})).unwrap(), ..Default::default() },
            http: http.clone(), timeouts: Default::default(),
        }).collect().await;
        let requests = server.received_requests().await.unwrap();
        let body: Value = serde_json::from_slice(&requests[0].body).unwrap();
        assert_eq!(body["messages"].as_array().unwrap().len(), 2);
        assert_eq!(
            body["messages"][0],
            json!({"role":"system", "content":"Caller system only"})
        );
        assert_eq!(
            body["messages"][1]["content"][1]["image_url"]["url"],
            "data:image/png;base64,synthetic-base64"
        );
        assert_eq!(body["tools"].as_array().unwrap().len(), 1);
        assert_eq!(body["tools"][0]["function"]["name"], "caller_tool");
        assert_eq!(body["temperature"], 0.25);
        assert_eq!(body["max_tokens"], 123);
        assert_eq!(body["context_length"], 200_000);
        match level {
            ThinkingLevel::Off => {
                assert_eq!(body["reasoning_effort"], "none");
                assert_eq!(body["enable_thinking"], false);
            }
            ThinkingLevel::Low | ThinkingLevel::Xhigh => {
                assert_eq!(body["reasoning_effort"], level.as_str());
                assert_eq!(body["enable_thinking"], true);
            }
            _ => {
                assert!(body.get("reasoning_effort").is_none());
                assert!(body.get("enable_thinking").is_none());
            }
        }
    }
}

#[tokio::test]
async fn disproven_off_control_fails_before_network_even_with_stale_catalog() {
    use her_llm::{
        AuthConfig, Compat, Context, ProviderConfig, RequestOptions, StreamEvent, ThinkingLevel,
        WireApi, WireRequest,
    };
    use futures::StreamExt;
    let (server, _, _, http) = setup().await;
    let provider = ProviderConfig {
        plan: None,
        id: "test".into(),
        name: "test".into(),
        names: Default::default(),
        api: "openai-chat".into(),
        base_url: server.uri(),
        auth: AuthConfig::Managed,
        headers: BTreeMap::new(),
        currency: Default::default(),
        compat: Compat::default(),
        listing: Default::default(),
    };
    for id in ["dmodel", "dfmodel"] {
        let mut model = provider.ad_hoc_model(id);
        model.thinking = vec![ThinkingLevel::Off];
        let events: Vec<_> = Http
            .stream(WireRequest {
                model: Arc::new(model),
                credentials: Credentials::with_key("test".into()),
                context: Arc::new(Context {
                    system: String::new(),
                    messages: vec![],
                    tools: vec![],
                }),
                options: RequestOptions {
                    thinking: ThinkingLevel::Off,
                    ..Default::default()
                },
                http: http.clone(),
                timeouts: Default::default(),
            })
            .collect()
            .await;
        assert_eq!(events.len(), 1);
        assert!(
            matches!(&events[0], StreamEvent::Failed(message) if message.error.as_ref().unwrap().provider_code.as_deref() == Some("qoder_thinking_off_unverified"))
        );
    }
    assert!(server.received_requests().await.unwrap().is_empty());
}

#[tokio::test]
async fn kimi_temperature_constraint_is_explicit_and_preserves_verified_values() {
    use her_llm::{
        AuthConfig, Compat, Context, ProviderConfig, RequestOptions, StreamEvent, UserMessage,
        WireApi, WireRequest,
    };
    use futures::StreamExt;
    let (server, _, _, http) = setup().await;
    let provider = ProviderConfig {
        plan: None,
        id: "test".into(),
        name: "test".into(),
        names: Default::default(),
        api: "openai-chat".into(),
        base_url: server.uri(),
        auth: AuthConfig::Managed,
        headers: BTreeMap::new(),
        currency: Default::default(),
        compat: Compat::default(),
        listing: Default::default(),
    };
    for id in ["kmodel", "kmodel_latest"] {
        for temperature in [Some(0.25), Some(0.5), Some(1.0), None] {
            server.reset().await;
            Mock::given(path("/chat/completions"))
                .respond_with(ResponseTemplate::new(200).insert_header("content-type", "text/event-stream")
                    .set_body_string("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
                .mount(&server).await;
            let events: Vec<_> = Http
                .stream(WireRequest {
                    model: Arc::new(provider.ad_hoc_model(id)),
                    credentials: Credentials::with_key("test".into()),
                    context: Arc::new(Context {
                        system: String::new(),
                        messages: vec![UserMessage::text("hi").into()],
                        tools: vec![],
                    }),
                    options: RequestOptions {
                        temperature,
                        ..Default::default()
                    },
                    http: http.clone(),
                    timeouts: Default::default(),
                })
                .collect()
                .await;
            let requests = server.received_requests().await.unwrap();
            if matches!(temperature, Some(0.25 | 0.5)) {
                assert!(requests.is_empty());
                assert!(
                    matches!(events.last(), Some(StreamEvent::Failed(message)) if message.error.as_ref().unwrap().provider_code.as_deref() == Some("qoder_temperature_unverified"))
                );
            } else {
                assert!(matches!(events.last(), Some(StreamEvent::Done(_))));
                assert_eq!(requests.len(), 1);
                let body: Value = serde_json::from_slice(&requests[0].body).unwrap();
                match temperature {
                    Some(_) => assert_eq!(body["temperature"], 1.0),
                    None => assert!(body.get("temperature").is_none()),
                }
            }
        }
    }
}

#[tokio::test]
async fn authorization_errors_keep_stage_status_and_recovery_action() {
    for (status, code, retryable) in [
        (400, "request_rejected", false),
        (401, "connection_login_required", false),
        (403, "permission_denied", false),
        (429, "rate_limited", true),
        (503, "service_unavailable", true),
    ] {
        let (s, _, mut q, http) = setup().await;
        Mock::given(path("/api/v1/me/jobToken"))
            .respond_with(ResponseTemplate::new(status).set_body_string("secret-do-not-expose"))
            .mount(&s)
            .await;
        let e = q.authorize(&http, false).await.err().unwrap();
        assert_eq!(e.code, code);
        assert_eq!(e.stage, "authorization");
        assert_eq!(e.http_status, Some(status));
        assert_eq!(e.retryable, retryable);
        assert!(
            !serde_json::to_string(&e.diagnostic())
                .unwrap()
                .contains("secret-do-not-expose")
        );
    }
}

#[tokio::test]
async fn catalog_failure_is_distinct_from_authorization_and_auto_is_not_required() {
    let (s, _, mut q, http) = setup().await;
    issue(&s).await;
    q.authorize(&http, false).await.unwrap();
    for (body, expected) in [
        ("<html>error</html>", "response_not_json"),
        (r#"{"app":{}}"#, "response_invalid"),
        (r#"{"app":[]}"#, "catalog_empty"),
    ] {
        s.reset().await;
        Mock::given(path("/algo/api/v2/model/list"))
            .respond_with(ResponseTemplate::new(200).set_body_string(body))
            .mount(&s)
            .await;
        let e = q.catalog(&http).await.unwrap_err();
        assert_eq!(e.code, expected);
        assert_eq!(e.stage, "catalog");
    }
    s.reset().await;
    Mock::given(path("/algo/api/v2/model/list"))
        .respond_with(
            ResponseTemplate::new(200).set_body_json(json!({"app":[{"key":"fast","enable":true}]})),
        )
        .mount(&s)
        .await;
    assert_eq!(q.catalog(&http).await.unwrap()[0].id, "fast");
}

#[test]
fn job_and_source_diagnostics_identify_fields() {
    let e = parse_job(
        &json!({"expires_in":90_000,"refresh_token_expires_in":180_000,"token":"abc"}),
        0,
    )
    .err()
    .unwrap();
    assert_eq!(e.stage, "authorization");
    assert_eq!(e.field.as_deref(), Some("refresh_token"));
    let e = source_login(&json!({"schemaVersion":999})).err().unwrap();
    assert_eq!(e.code, "unsupported_login_schema");
    assert_eq!(e.field.as_deref(), Some("schemaVersion"));
}
