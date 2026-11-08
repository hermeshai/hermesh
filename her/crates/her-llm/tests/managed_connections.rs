use async_trait::async_trait;
use her_llm::catalog::{Listing, ModelToml};
use her_llm::{
    connection::{Authorization, ConnectionAuthorizer, ConnectionRequest},
    *,
};
use futures::StreamExt;
use std::{
    collections::BTreeMap,
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
};
use tokio_util::sync::CancellationToken;
use wiremock::{
    Mock, MockServer, ResponseTemplate,
    matchers::{header, method, path},
};
struct Resolver {
    base: String,
    calls: Arc<AtomicUsize>,
    released: Arc<AtomicUsize>,
}
struct Lease {
    base: String,
    calls: Arc<AtomicUsize>,
    released: Arc<AtomicUsize>,
}
impl Drop for Lease {
    fn drop(&mut self) {
        self.released.fetch_add(1, Ordering::Relaxed);
    }
}
#[async_trait]
impl ConnectionAuthorizer for Resolver {
    async fn begin(
        &self,
        _: &str,
        _: &str,
        _: reqwest::Client,
    ) -> Result<Box<dyn ConnectionRequest>, LlmError> {
        Ok(Box::new(Lease {
            base: self.base.clone(),
            calls: self.calls.clone(),
            released: self.released.clone(),
        }))
    }
}
#[async_trait]
impl ConnectionRequest for Lease {
    async fn authorize(&self, rejected: bool) -> Result<Authorization, LlmError> {
        self.calls.fetch_add(1, Ordering::Relaxed);
        let mut credentials =
            Credentials::with_key(if rejected { "new-token" } else { "old-token" }.into());
        credentials.headers.insert(
            "X-Session-Id".into(),
            if rejected {
                "new-session"
            } else {
                "old-session"
            }
            .into(),
        );
        Ok(Authorization {
            base_url: self.base.clone(),
            credentials,
            transport: None,
        })
    }
    fn cancellation(&self) -> CancellationToken {
        CancellationToken::new()
    }
    fn failed(&self, e: &mut LlmError) {
        e.message = "sanitized upstream error".into();
    }
}
fn setup(server: &MockServer) -> (ModelRegistry, Arc<AtomicUsize>, Arc<AtomicUsize>) {
    let registry = ModelRegistry::new(
        Catalog::builtin().unwrap(),
        RegistryOptions {
            retry: RetryPolicy {
                max_retries: Some(0),
                ..Default::default()
            },
            ..Default::default()
        },
    );
    let p = ProviderConfig {
        plan: None,
        id: "local-test".into(),
        name: "Managed".into(),
        names: Default::default(),
        api: "openai-chat".into(),
        base_url: "https://unused.invalid/v1".into(),
        auth: AuthConfig::Managed,
        headers: BTreeMap::new(),
        currency: Default::default(),
        compat: Compat::default(),
        listing: Listing { entries: vec![ModelToml::new("flash")], ..Default::default() },
    };
    registry.replace_source("connections", vec![p]).unwrap();
    let calls = Arc::new(AtomicUsize::new(0));
    let released = Arc::new(AtomicUsize::new(0));
    registry.install_authorizer(Arc::new(Resolver {
        base: format!("{}/v1", server.uri()),
        calls: calls.clone(),
        released: released.clone(),
    }));
    (registry, calls, released)
}
fn context() -> Context {
    Context {
        system: "system from kernel".into(),
        messages: vec![Message::from(UserMessage::text("hello"))],
        tools: vec![],
    }
}
#[tokio::test]
async fn explicit_401_refreshes_once_and_streams_directly_with_matching_headers() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/v1/chat/completions"))
        .and(header("authorization", "Bearer old-token"))
        .and(header("X-Session-Id", "old-session"))
        .respond_with(ResponseTemplate::new(401).set_body_string("rejected"))
        .expect(1)
        .mount(&server)
        .await;
    Mock::given(method("POST")).and(path("/v1/chat/completions")).and(header("authorization","Bearer new-token")).and(header("X-Session-Id","new-session")).respond_with(ResponseTemplate::new(200).insert_header("content-type","text/event-stream").set_body_string("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"direct reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")).expect(1).mount(&server).await;
    let (registry, calls, released) = setup(&server);
    let model = registry.resolve("local-test/flash").unwrap().model;
    let events: Vec<_> = registry
        .stream(model, context(), RequestOptions::default())
        .collect()
        .await;
    assert!(matches!(events.last(),Some(StreamEvent::Done(m)) if m.text()=="direct reply"));
    assert_eq!(calls.load(Ordering::Relaxed), 2);
    assert_eq!(released.load(Ordering::Relaxed), 1);
    let requests = server.received_requests().await.unwrap();
    let body: serde_json::Value = serde_json::from_slice(&requests[1].body).unwrap();
    assert_eq!(body["messages"][0]["content"], "system from kernel");
}
#[tokio::test]
async fn second_401_is_terminal_and_cannot_loop_or_expose_upstream_diagnostics() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .respond_with(ResponseTemplate::new(401).set_body_string("private upstream contents"))
        .expect(2)
        .mount(&server)
        .await;
    let (registry, calls, released) = setup(&server);
    let model = registry.resolve("local-test/flash").unwrap().model;
    let events: Vec<_> = registry
        .stream(model, context(), RequestOptions::default())
        .collect()
        .await;
    assert!(
        matches!(events.last(),Some(StreamEvent::Failed(m)) if m.error.as_ref().unwrap().message=="sanitized upstream error")
    );
    assert_eq!(calls.load(Ordering::Relaxed), 2);
    assert_eq!(released.load(Ordering::Relaxed), 1);
}
/// Fails the first `failures` authorizations as a network error, as when the application's
/// token service cannot be reached.
struct Flaky {
    base: String,
    failures: Arc<AtomicUsize>,
    leases: Arc<AtomicUsize>,
}
struct FlakyLease {
    base: String,
    failures: Arc<AtomicUsize>,
}
#[async_trait]
impl ConnectionAuthorizer for Flaky {
    async fn begin(
        &self,
        _: &str,
        _: &str,
        _: reqwest::Client,
    ) -> Result<Box<dyn ConnectionRequest>, LlmError> {
        self.leases.fetch_add(1, Ordering::Relaxed);
        Ok(Box::new(FlakyLease {
            base: self.base.clone(),
            failures: self.failures.clone(),
        }))
    }
}
#[async_trait]
impl ConnectionRequest for FlakyLease {
    async fn authorize(&self, _: bool) -> Result<Authorization, LlmError> {
        let failures = self.failures.load(Ordering::Relaxed);
        if failures > 0 {
            self.failures.store(failures - 1, Ordering::Relaxed);
            return Err(LlmError::new(ErrorKind::Network, "token service unreachable"));
        }
        Ok(Authorization {
            base_url: self.base.clone(),
            credentials: Credentials::with_key("token".into()),
            transport: None,
        })
    }
    fn cancellation(&self) -> CancellationToken {
        CancellationToken::new()
    }
    fn failed(&self, _: &mut LlmError) {}
}
#[tokio::test]
async fn authorization_network_failures_are_retried_each_with_its_own_lease() {
    let server = MockServer::start().await;
    Mock::given(method("POST")).and(path("/v1/chat/completions")).respond_with(ResponseTemplate::new(200).insert_header("content-type","text/event-stream").set_body_string("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")).expect(1).mount(&server).await;
    let registry = ModelRegistry::new(
        Catalog::builtin().unwrap(),
        RegistryOptions {
            retry: RetryPolicy {
                base_delay: std::time::Duration::from_millis(1),
                max_delay: std::time::Duration::from_millis(5),
                ..Default::default()
            },
            ..Default::default()
        },
    );
    let p = ProviderConfig {
        plan: None,
        id: "local-test".into(),
        name: "Managed".into(),
        names: Default::default(),
        api: "openai-chat".into(),
        base_url: "https://unused.invalid/v1".into(),
        auth: AuthConfig::Managed,
        headers: BTreeMap::new(),
        currency: Default::default(),
        compat: Compat::default(),
        listing: Listing { entries: vec![ModelToml::new("flash")], ..Default::default() },
    };
    registry.replace_source("connections", vec![p]).unwrap();
    let leases = Arc::new(AtomicUsize::new(0));
    registry.install_authorizer(Arc::new(Flaky {
        base: format!("{}/v1", server.uri()),
        failures: Arc::new(AtomicUsize::new(2)),
        leases: leases.clone(),
    }));
    let model = registry.resolve("local-test/flash").unwrap().model;
    let events: Vec<_> = registry
        .stream(model, context(), RequestOptions::default())
        .collect()
        .await;
    let retries: Vec<_> = events
        .iter()
        .filter_map(|e| match e {
            StreamEvent::Retrying { error, .. } => Some(error.kind),
            _ => None,
        })
        .collect();
    assert_eq!(retries, vec![ErrorKind::Network, ErrorKind::Network]);
    assert!(matches!(events.last(), Some(StreamEvent::Done(m)) if m.text() == "reply"));
    assert_eq!(leases.load(Ordering::Relaxed), 3);
}
