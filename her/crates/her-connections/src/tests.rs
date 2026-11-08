use super::*;
use her_llm::{Catalog, RegistryOptions, ThinkingLevel};
use her_protocol::api::ConnectionDiscovery;
use platform::{Environment, Login};
use serde_json::json;
use std::sync::atomic::{AtomicI64, AtomicUsize};
use wiremock::{
    Mock, MockServer, ResponseTemplate,
    matchers::{method, path},
};

struct Fake {
    calls: AtomicUsize,
    discovery_failure: RwLock<Option<ConnectionError>>,
    login_failure: RwLock<Option<ConnectionError>>,
    now: AtomicI64,
    identity: RwLock<String>,
    expires: AtomicI64,
    store: Mutex<BTreeMap<String, String>>,
}
impl Fake {
    fn new() -> Arc<Self> {
        Arc::new(Self {
            calls: AtomicUsize::new(0),
            discovery_failure: RwLock::new(None),
            login_failure: RwLock::new(None),
            now: AtomicI64::new(1_000_000),
            identity: RwLock::new("account-a".into()),
            expires: AtomicI64::new(9_000_000),
            store: Mutex::new(BTreeMap::new()),
        })
    }
}
#[async_trait]
impl Environment for Fake {
    fn now(&self) -> i64 {
        self.now.load(Ordering::Relaxed)
    }
    async fn discover(&self) -> Result<ConnectionDiscovery> {
        self.calls.fetch_add(1, Ordering::Relaxed);
        if let Some(error) = self.discovery_failure.read().clone() {
            return Err(error);
        }
        Ok(ConnectionDiscovery {
            diagnostic: None,
            adapter_id: "qwenwork".into(),
            installed: true,
            version: Some("1.4.0".into()),
            login: "unknown".into(),
            discovery_id: "qwenwork-standard".into(),
        })
    }
    async fn login(&self) -> Result<Login> {
        self.calls.fetch_add(1, Ordering::Relaxed);
        if let Some(error) = self.login_failure.read().clone() {
            return Err(error);
        }
        Ok(Login {
            subject: None,
            token: "account-secret".into(),
            identity: self.identity.read().clone(),
            expires: self.expires.load(Ordering::Relaxed),
        })
    }
    async fn headers(&self) -> Result<BTreeMap<String, String>> {
        self.calls.fetch_add(1, Ordering::Relaxed);
        Ok(BTreeMap::new())
    }
    async fn get(&self, id: &str) -> Result<Option<String>> {
        Ok(self.store.lock().await.get(id).cloned())
    }
    async fn set(&self, id: &str, value: &str) -> Result<()> {
        self.store.lock().await.insert(id.into(), value.into());
        Ok(())
    }
    async fn delete(&self, id: &str) -> Result<()> {
        self.store.lock().await.remove(id);
        Ok(())
    }
}
fn registry() -> Arc<ModelRegistry> {
    Arc::new(ModelRegistry::new(
        Catalog::builtin().unwrap(),
        RegistryOptions::default(),
    ))
}
fn client() -> reqwest::Client {
    registry().http_client().unwrap()
}
fn record(enabled: bool) -> Record {
    Record {
        connection_id: uuid::Uuid::new_v4().to_string(),
        // Written before provider IDs were kept: published as `local-<connection_id>`.
        provider_id: String::new(),
        adapter_id: "qwenwork".into(),
        identity_key: "account-a".into(),
        enabled,
        idempotency_key: "test".into(),
        models: vec![ConnectionModel {
            id: "flash".into(),
            name: "Flash".into(),
            names: Default::default(),
            context_window: Some(1000000),
            max_output_tokens: None,
            reasoning: true,
            images: true,
            thinking: Vec::new(),
            default_thinking: Default::default(),
        }],
        models_fetched_at: None,
    }
}
fn save_config(dir: &Path, r: &Record) {
    std::fs::create_dir_all(dir.join("connections")).unwrap();
    std::fs::write(
        dir.join("connections/config.json"),
        serde_json::to_vec(&Config {
            schema: 1,
            connections: vec![r.clone()],
        })
        .unwrap(),
    )
    .unwrap();
}
async fn save_session(env: &Fake, id: &str) {
    env.set(id,&json!({"identity":"account-a","session":{"token":"private-model-token","session_id":"private-session","base":"https://api9.qwenwork.cn/v1","expires":8_000_000,"ttl":7_200_000}}).to_string()).await.unwrap();
}

#[tokio::test]
async fn unused_and_disabled_connections_do_no_platform_or_network_work() {
    let dir = tempfile::tempdir().unwrap();
    let env = Fake::new();
    let llm = registry();
    let m = Manager::with_environment(dir.path().into(), &llm, env.clone());
    assert!(m.snapshot().unwrap().connections.is_empty());
    assert_eq!(env.calls.load(Ordering::Relaxed), 0);
    let r = record(false);
    save_config(dir.path(), &r);
    let llm = registry();
    let m = Manager::with_environment(dir.path().into(), &llm, env.clone());
    assert!(matches!(
        m.snapshot().unwrap().connections[0].state,
        ConnectionState::Disabled
    ));
    assert!(
        llm.resolve(&format!("local-{}/flash", r.connection_id))
            .is_err()
    );
    assert_eq!(env.calls.load(Ordering::Relaxed), 0);
}
#[tokio::test]
async fn existing_connection_restores_without_secrets_in_catalog_and_disconnect_drains() {
    let dir = tempfile::tempdir().unwrap();
    let r = record(true);
    save_config(dir.path(), &r);
    let env = Fake::new();
    save_session(&env, &r.connection_id).await;
    let llm = registry();
    let m = Manager::with_environment(dir.path().into(), &llm, env.clone());
    let provider = format!("local-{}", r.connection_id);
    let flash = llm.resolve(&format!("{provider}/flash")).unwrap().model;
    assert!(flash.accepts_images());
    assert_eq!(flash.context_window, 1_000_000, "the account's catalog");
    // Not stated by anyone: left to the gateway, never a guess that cuts replies short.
    assert_eq!(flash.max_output_tokens, None);
    let body: serde_json::Value = serde_json::from_slice(&her_llm::api::openai_chat::build_body(
        &flash,
        &her_llm::Context::default(),
        &her_llm::RequestOptions::default(),
    ))
    .unwrap();
    assert!(
        body.get("max_tokens").is_none() && body.get("max_completion_tokens").is_none(),
        "{body}"
    );
    assert!(llm.resolve(&format!("{provider}/invented")).is_err());
    assert_eq!(env.calls.load(Ordering::Relaxed), 0);
    let lease = m.begin(&provider, "flash", client()).await.unwrap();
    let auth = lease.authorize(false).await.unwrap();
    assert_eq!(
        auth.credentials.api_key.as_deref(),
        Some("private-model-token")
    );
    assert_eq!(auth.credentials.headers["X-Session-Id"], "private-session");
    assert!(
        !serde_json::to_string(&m.snapshot().unwrap())
            .unwrap()
            .contains("private")
    );
    assert!(!format!("{:?}", auth.credentials).contains("private-session"));
    // Requests run side by side; the disconnect below waits for both.
    let other = m.begin(&provider, "flash", client()).await.unwrap();
    let manager = m.clone();
    let id = r.connection_id.clone();
    let disconnect = tokio::spawn(async move { manager.disconnect(&id).await });
    for _ in 0..50 {
        if matches!(
            m.snapshot().unwrap().connections[0].state,
            ConnectionState::Disabled
        ) {
            break;
        }
        tokio::task::yield_now().await;
    }
    assert!(!disconnect.is_finished());
    assert!(llm.resolve(&format!("{provider}/flash")).is_err());
    m.failed(
        &m.entry(&r.connection_id).unwrap(),
        &ConnectionError::login(),
    );
    assert!(matches!(
        m.snapshot().unwrap().connections[0].state,
        ConnectionState::Disabled
    ));
    drop(lease);
    for _ in 0..50 {
        tokio::task::yield_now().await;
    }
    assert!(!disconnect.is_finished());
    drop(other);
    disconnect.await.unwrap().unwrap();
    env.expires.store(0, Ordering::Relaxed);
    assert!(m.retry(&r.connection_id).await.is_err());
    assert!(matches!(
        m.snapshot().unwrap().connections[0].state,
        ConnectionState::NeedsLogin
    ));
    m.disconnect(&r.connection_id).await.unwrap();
    m.remove(&r.connection_id).await.unwrap();
    assert!(env.get(&r.connection_id).await.unwrap().is_none());
}
#[tokio::test]
async fn connecting_the_same_account_again_keeps_the_provider() {
    let dir = tempfile::tempdir().unwrap();
    let env = Fake::new();
    // Expired: the connection is saved, then its catalog fails before any request.
    env.expires.store(0, Ordering::Relaxed);
    let llm = registry();
    let m = Manager::with_environment(dir.path().into(), &llm, env.clone());
    let connect = |key: &str| {
        m.connect(ConnectionConnectParams {
            adapter_id: "qwenwork".into(),
            discovery_id: "qwenwork-standard".into(),
            idempotency_key: key.into(),
        })
    };
    assert!(connect("first").await.is_err());
    let first = m.snapshot().unwrap().connections.remove(0);
    assert_eq!(first.provider_id, provider_id("qwenwork", "account-a"));
    m.disconnect(&first.connection_id).await.unwrap();
    m.remove(&first.connection_id).await.unwrap();
    assert!(connect("second").await.is_err());
    let second = m.snapshot().unwrap().connections.remove(0);
    assert_ne!(second.connection_id, first.connection_id);
    assert_eq!(second.provider_id, first.provider_id);
    // Kept across a restart; another account or application gets its own.
    let m = Manager::with_environment(dir.path().into(), &registry(), env);
    assert_eq!(
        m.snapshot().unwrap().connections[0].provider_id,
        first.provider_id
    );
    assert_ne!(provider_id("qwenwork", "account-b"), first.provider_id);
    assert_ne!(provider_id("qoder", "account-a"), first.provider_id);
}
#[tokio::test]
async fn connection_publishes_and_authorizes_under_its_provider() {
    let dir = tempfile::tempdir().unwrap();
    let mut r = record(true);
    r.provider_id = provider_id("qwenwork", "account-a");
    save_config(dir.path(), &r);
    let env = Fake::new();
    save_session(&env, &r.connection_id).await;
    let llm = registry();
    let m = Manager::with_environment(dir.path().into(), &llm, env);
    assert!(llm.resolve(&format!("{}/flash", r.provider_id)).is_ok());
    assert!(
        llm.resolve(&format!("local-{}/flash", r.connection_id))
            .is_err()
    );
    assert!(m.begin(&r.provider_id, "flash", client()).await.is_ok());
}
#[tokio::test]
async fn identity_switch_cancels_connection_and_removes_only_its_session() {
    let dir = tempfile::tempdir().unwrap();
    let r = record(true);
    save_config(dir.path(), &r);
    let env = Fake::new();
    save_session(&env, &r.connection_id).await;
    let llm = registry();
    let m = Manager::with_environment(dir.path().into(), &llm, env.clone());
    let lease = m
        .begin(&format!("local-{}", r.connection_id), "flash", client())
        .await
        .unwrap();
    *env.identity.write() = "account-b".into();
    assert!(lease.authorize(false).await.is_err());
    assert!(lease.cancellation().is_cancelled());
    assert!(matches!(
        m.snapshot().unwrap().connections[0].state,
        ConnectionState::NeedsLogin
    ));
    assert!(env.get(&r.connection_id).await.unwrap().is_none());
}
#[tokio::test]
async fn parallel_refreshes_exchange_once_and_keep_session_pairing() {
    let server = MockServer::start().await;
    let env = Fake::new();
    Mock::given(method("POST"))
        .and(path("/v1/access-tokens/exchange"))
        .respond_with(
            ResponseTemplate::new(200)
                .set_body_json(json!({"access_token":"new-token","expires_at":1_120_000})),
        )
        .expect(1)
        .mount(&server)
        .await;
    let mut adapter = QwenWork::new("test".into(), "account-a".into(), env);
    adapter.session = Some(qwenwork::Session {
        token: "old-token".into(),
        session_id: "own-session".into(),
        base: format!("{}/v1", server.uri()),
        expires: 1_040_000,
        ttl: 120_000,
    });
    let shared = Arc::new(Mutex::new(adapter));
    let futures = (0..20).map(|_| {
        let a = shared.clone();
        async move { a.lock().await.authorize(&client(), false).await.unwrap() }
    });
    for a in futures::future::join_all(futures).await {
        assert_eq!(a.credentials.api_key.as_deref(), Some("new-token"));
        assert_eq!(a.credentials.headers["X-Session-Id"], "own-session");
    }
}
#[tokio::test]
async fn transient_refresh_backoff_and_expired_account_fail_without_refreshing_source() {
    let server = MockServer::start().await;
    let env = Fake::new();
    Mock::given(method("POST"))
        .and(path("/v1/access-tokens/exchange"))
        .respond_with(ResponseTemplate::new(503))
        .expect(1)
        .mount(&server)
        .await;
    let mut a = QwenWork::new("test".into(), "account-a".into(), env.clone());
    a.session = Some(qwenwork::Session {
        token: "old".into(),
        session_id: "own".into(),
        base: format!("{}/v1", server.uri()),
        expires: 1_040_000,
        ttl: 120_000,
    });
    assert_eq!(
        a.authorize(&client(), false)
            .await
            .unwrap()
            .credentials
            .api_key
            .as_deref(),
        Some("old")
    );
    a.authorize(&client(), false).await.unwrap();
    env.now.store(1_200_000, Ordering::Relaxed);
    env.expires.store(0, Ordering::Relaxed);
    assert_eq!(
        a.authorize(&client(), false).await.err().unwrap().code,
        "connection_login_required"
    );
}
#[tokio::test]
async fn expired_session_mints_an_independent_session_with_millisecond_expiry() {
    let server = MockServer::start().await;
    let env = Fake::new();
    Mock::given(method("POST")).and(path("/api/desktop-sessions")).respond_with(|req:&wiremock::Request|{
        let body:serde_json::Value=serde_json::from_slice(&req.body).unwrap();assert_ne!(body["session_id"],"old-session");
        ResponseTemplate::new(200).set_body_json(json!({"session_id":body["session_id"],"access_token":"minted","expires_at_ms":8_200_000,"ttl_ms":7_200_000,"base_url":"https://api9.qwenwork.cn"}))
    }).expect(1).mount(&server).await;
    let mut a = QwenWork::new("test".into(), "account-a".into(), env);
    a.gateway = server.uri();
    let auth = a.authorize(&client(), false).await.unwrap();
    assert_eq!(auth.credentials.api_key.as_deref(), Some("minted"));
    assert_eq!(auth.base_url, "https://api9.qwenwork.cn/v1");
}
#[test]
fn source_origin_allowlist_rejects_credentials_paths_and_non_https() {
    for url in [
        "http://api9.qwenwork.cn",
        "https://secret@api9.qwenwork.cn",
        "https://api9.qwenwork.cn/other",
        "https://attacker.test",
    ] {
        assert!(qwenwork::checked_base(url).is_err());
    }
    assert_eq!(
        qwenwork::checked_base("https://api9.qwenwork.cn").unwrap(),
        "https://api9.qwenwork.cn/v1"
    );
}
#[test]
fn chrome_v10_login_decrypts_and_preserves_existing_identity_hash() {
    use cbc::cipher::{BlockEncryptMut, KeyIvInit, block_padding::Pkcs7};
    use std::num::NonZeroU32;
    let plain = json!({"schemaVersion":2,"token":"secret-account","user":{"id":"user-1"},"expiresAt":"2030-01-01T00:00:00Z"});
    let mut key = [0u8; 16];
    ring::pbkdf2::derive(
        ring::pbkdf2::PBKDF2_HMAC_SHA1,
        NonZeroU32::new(1003).unwrap(),
        b"saltysalt",
        b"password",
        &mut key,
    );
    let mut sealed = b"v10".to_vec();
    sealed.extend(
        cbc::Encryptor::<aes::Aes128>::new_from_slices(&key, &[32; 16])
            .unwrap()
            .encrypt_padded_vec_mut::<Pkcs7>(plain.to_string().as_bytes()),
    );
    let login = platform::decrypt_login(&sealed, b"password").unwrap();
    assert_eq!(login.token, "secret-account");
    assert_eq!(
        login.identity,
        platform::digest(br#"["user-1","","standard"]"#)
    );
    assert_eq!(
        platform::decrypt_login(&sealed, b"incorrect")
            .err()
            .unwrap()
            .code,
        "credential_decrypt_failed"
    );
}

#[tokio::test]
async fn transient_failure_keeps_route_and_success_clears_diagnostic() {
    let dir = tempfile::tempdir().unwrap();
    let env = Fake::new();
    let llm = registry();
    let r = record(true);
    save_config(dir.path(), &r);
    save_session(&env, &r.connection_id).await;
    let manager = Manager::with_environment(dir.path().into(), &llm, env);
    let entry = manager.entry(&r.connection_id).unwrap();
    manager.failed(&entry, &ConnectionError::unavailable());
    assert!(
        llm.provider(&format!("local-{}", r.connection_id))
            .is_some()
    );
    assert_eq!(
        entry.view.read().reason_code.as_deref(),
        Some("connection_unavailable")
    );
    let lease = manager
        .begin(&format!("local-{}", r.connection_id), "flash", client())
        .await
        .unwrap();
    lease.authorize(false).await.unwrap();
    assert!(entry.view.read().reason_code.is_none());
}
#[tokio::test]
async fn catalogs_are_asked_again_only_when_old_and_failures_change_nothing() {
    let dir = tempfile::tempdir().unwrap();
    let mut r = record(true);
    r.models_fetched_at = Some(her_llm::discovery::now());
    save_config(dir.path(), &r);
    let env = Fake::new();
    save_session(&env, &r.connection_id).await;
    let llm = registry();
    let m = Manager::with_environment(dir.path().into(), &llm, env.clone());
    let day = std::time::Duration::from_secs(24 * 60 * 60);
    // Fresh: not asked.
    assert!(!m.refresh_stale_catalogs(day).await);
    assert_eq!(env.calls.load(Ordering::Relaxed), 0);
    // Old, and the source's login has expired: asked, and the failure leaves all as it was.
    env.expires.store(0, Ordering::Relaxed);
    assert!(!m.refresh_stale_catalogs(std::time::Duration::ZERO).await);
    assert!(env.calls.load(Ordering::Relaxed) > 0);
    let provider = format!("local-{}", r.connection_id);
    assert!(llm.resolve(&format!("{provider}/flash")).is_ok());
    assert!(matches!(
        m.snapshot().unwrap().connections[0].state,
        ConnectionState::Ready
    ));
}
#[test]
fn qwenwork_thinking_follows_the_application() {
    use ThinkingLevel::*;
    let all = vec![Off, Low, Medium, High, Xhigh, Max];
    let always = vec![Low, Medium, High, Xhigh, Max];
    let of = |id: &str, entry: serde_json::Value| crate::qwenwork::thinking_of(id, &entry);
    assert_eq!(
        of("pro", json!({"is_reasoning": true})),
        (all.clone(), High)
    );
    assert_eq!(
        of("qwen3.8-max-preview", json!({"is_reasoning": true})),
        (all.clone(), Xhigh)
    );
    // An opaque key: the display name says which model it is.
    assert_eq!(
        of(
            "mode-5d41402a",
            json!({"is_reasoning": true, "display_name": "Qwen3.8-Max"})
        ),
        (all.clone(), Xhigh)
    );
    assert_eq!(
        of(
            "mode-7d793037",
            json!({"is_reasoning": true, "display_name": "Qwen3.7-Max"})
        ),
        (vec![Off, On], On)
    );
    // The name shown may be the Chinese one while the English one says which model it is.
    assert_eq!(
        of(
            "mode-7d793037",
            json!({"is_reasoning": true, "i18n": {"display_name": {"zh": "千问旗舰", "en": "Qwen3.7-Max"}}})
        ),
        (vec![Off, On], On)
    );
    // A thinking_config means the model keeps thinking: no "off".
    assert_eq!(
        of(
            "pro",
            json!({"is_reasoning": true, "thinking_config": {"effort": "high"}})
        ),
        (always.clone(), High)
    );
    assert_eq!(
        of(
            "flash",
            json!({"thinking_config": {"enabled": true, "effort": "medium"}})
        ),
        (always.clone(), Medium)
    );
    assert_eq!(
        of(
            "flash",
            json!({"is_reasoning": true, "thinking_config": {"effort": "turbo"}})
        ),
        (always, High),
        "an unknown effort is ignored"
    );
    assert_eq!(
        of(
            "flash",
            json!({"is_reasoning": true, "thinking_config": null})
        ),
        (all, High)
    );
    assert_eq!(
        of("qwen3.7-plus", json!({"is_reasoning": true})),
        (vec![Off, On], On)
    );
    assert_eq!(
        of("plain", json!({"is_reasoning": false})),
        (vec![], Default)
    );
}

#[test]
fn qwenwork_429_quota_words_are_waited_out_unless_they_speak_of_arrears() {
    use her_llm::{ErrorKind, LlmError};
    let none: Vec<String> = Vec::new();
    let kind = |adapter: &str, status: u16, body: &str| {
        let mut error = LlmError::from_http(status, body, None, &none);
        crate::adapter::reclassify(adapter, &mut error);
        error.kind
    };
    let openai = r#"{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","code":"insufficient_quota"}}"#;
    assert_eq!(kind("qwenwork", 429, openai), ErrorKind::RateLimited);
    assert_eq!(
        kind(
            "qwenwork",
            429,
            r#"{"error":{"message":"账户已欠费，请充值"}}"#
        ),
        ErrorKind::QuotaExceeded
    );
    assert_eq!(
        kind("qwenwork", 402, openai),
        ErrorKind::QuotaExceeded,
        "only a 429 is a moment's limit"
    );
    assert_eq!(kind("qoder", 429, openai), ErrorKind::QuotaExceeded);
}

#[test]
fn gateway_words_lose_what_may_identify_the_account() {
    let scrub = crate::adapter::scrub;
    assert_eq!(
        scrub(
            "session 7f3a9c2e-41b8-4d0e-9a77-1c2d3e4f5a6b over limit, request_id=req_0a1b2c3d4e5f6a7b8c9d"
        ),
        "session … over limit, …"
    );
    assert_eq!(
        scrub("You exceeded your current quota (insufficient_quota)."),
        "You exceeded your current quota (insufficient_quota)."
    );
    assert_eq!(scrub("请求过多，请稍后再试"), "请求过多，请稍后再试");
    assert_eq!(scrub(&"字".repeat(600)).chars().count(), 501);
}

#[tokio::test]
async fn qwen_catalog_names_models_as_the_application_does_and_requests_by_key() {
    let server = MockServer::start().await;
    let mut q = QwenWork::new("test".into(), "account-a".into(), Fake::new());
    q.gateway = server.uri();
    let entry = |key: &str, fields: serde_json::Value| {
        let mut entry = json!({"key": key, "enable": true, "format": "openai/chat-completions"});
        entry
            .as_object_mut()
            .unwrap()
            .extend(fields.as_object().unwrap().clone());
        entry
    };
    let body = json!({"catalog": [
        entry("mode-a", json!({"displayName": "First", "display_name": "Second", "name": "Third"})),
        entry("mode-b", json!({"display_name": "Second", "name": "Third"})),
        entry("mode-c", json!({"name": "Third"})),
        entry("mode-d", json!({"name": "Own", "server_model": {"display_name": "From server"}})),
        entry("mode-e", json!({"serverModel": {"name": "From server"}})),
        entry("mode-f", json!({"display_name": "  "})),
        entry("flash", json!({})),
        // As the real catalog has it (2026-11-06): only names by language.
        entry("mode-g", json!({"name": "Plain", "i18n": {"display_name": {"en": "Standard", "zh": "标准"}}})),
        entry("mode-h", json!({"i18n": {"displayName": {"en": "Pro"}}})),
        entry("mode-i", json!({"server_model": {"i18n": {"display_name": {"zh": "高级"}}}})),
        entry("mode-j", json!({"i18n": {"display_name": {"zh-CN": "标准版"}}})),
        entry("mode-k", json!({"name": "Plain", "i18n": {"description": {"zh": "说明"}}})),
    ]});
    Mock::given(path("/api/desktop/chat-modes"))
        .respond_with(ResponseTemplate::new(200).set_body_json(body))
        .mount(&server)
        .await;
    let models = q.catalog(&client()).await.unwrap();
    let got: Vec<_> = models
        .iter()
        .map(|m| (m.id.as_str(), m.name.as_str()))
        .collect();
    assert_eq!(
        got,
        [
            ("mode-a", "First"),
            ("mode-b", "Second"),
            ("mode-c", "Third"),
            ("mode-d", "From server"),
            ("mode-e", "From server"),
            ("mode-f", "mode-f"),
            ("flash", "flash"),
            ("mode-g", "标准"),
            ("mode-h", "Pro"),
            ("mode-i", "高级"),
            ("mode-j", "标准版"),
            ("mode-k", "Plain"),
        ]
    );
    // Every language goes along, for clients that have an interface language to pick by.
    let names = |id: &str| &models.iter().find(|m| m.id == id).unwrap().names;
    assert_eq!(names("mode-g")["en"], "Standard");
    assert_eq!(names("mode-g")["zh"], "标准");
    assert_eq!(names("mode-i")["zh"], "高级");
    assert!(names("mode-a").is_empty());
    assert!(names("mode-k").is_empty());
}

#[tokio::test]
async fn qwen_catalog_reports_actual_http_and_schema_failures_without_secrets() {
    let server = MockServer::start().await;
    let env = Fake::new();
    let mut q = QwenWork::new("test".into(), "account-a".into(), env);
    q.gateway = server.uri();
    for (status, code, retryable) in [
        (400, "request_rejected", false),
        (401, "connection_login_required", false),
        (403, "permission_denied", false),
        (429, "rate_limited", true),
        (503, "service_unavailable", true),
        (302, "redirect_rejected", false),
    ] {
        server.reset().await;
        Mock::given(path("/api/desktop/chat-modes"))
            .respond_with(
                ResponseTemplate::new(status).set_body_string("secret-token-do-not-expose"),
            )
            .mount(&server)
            .await;
        let e = q.catalog(&client()).await.unwrap_err();
        assert_eq!(e.code, code);
        assert_eq!(e.stage, "catalog");
        assert_eq!(e.http_status, Some(status));
        assert_eq!(e.retryable, retryable);
        assert!(
            !serde_json::to_string(&e.diagnostic())
                .unwrap()
                .contains("secret-token")
        );
    }
    for (body, code, field) in [
        (
            "<html>proxy or service error</html>",
            "response_not_json",
            None,
        ),
        (r#"{"catalog":{}}"#, "response_invalid", Some("catalog")),
        (r#"{"catalog":[]}"#, "catalog_empty", None),
        (
            r#"{"catalog":[{"enable":true,"key":"future","format":"new-protocol"}]}"#,
            "unsupported_model_protocol",
            None,
        ),
    ] {
        server.reset().await;
        Mock::given(path("/api/desktop/chat-modes"))
            .respond_with(ResponseTemplate::new(200).set_body_string(body))
            .mount(&server)
            .await;
        let e = q.catalog(&client()).await.unwrap_err();
        assert_eq!(e.code, code);
        assert_eq!(e.stage, "catalog");
        assert_eq!(e.field.as_deref(), field);
    }
}

#[tokio::test]
async fn qwen_authorization_identifies_invalid_field_and_preserves_forbidden() {
    let server = MockServer::start().await;
    let mut q = QwenWork::new("test".into(), "account-a".into(), Fake::new());
    q.gateway = server.uri();
    Mock::given(path("/api/desktop-sessions"))
        .respond_with(ResponseTemplate::new(200).set_body_json(json!({"expires_at_ms":9_000_000})))
        .mount(&server)
        .await;
    let e = q.authorize(&client(), false).await.err().unwrap();
    assert_eq!(e.stage, "authorization");
    assert_eq!(e.field.as_deref(), Some("access_token"));
    server.reset().await;
    Mock::given(path("/api/desktop-sessions"))
        .respond_with(ResponseTemplate::new(403))
        .mount(&server)
        .await;
    let e = q.authorize(&client(), false).await.err().unwrap();
    assert_eq!(e.code, "permission_denied");
    assert_eq!(e.action, "check_account");
}

#[tokio::test]
async fn structured_diagnostics_survive_snapshots_and_clear_on_recovery() {
    let dir = tempfile::tempdir().unwrap();
    let env = Fake::new();
    let llm = registry();
    let r = record(true);
    save_config(dir.path(), &r);
    save_session(&env, &r.connection_id).await;
    let m = Manager::with_environment(dir.path().into(), &llm, env);
    let entry = m.entry(&r.connection_id).unwrap();
    let e = ConnectionError::new("network_timeout", "timeout").at("authorization");
    m.failed(&entry, &e);
    let snapshot = m.snapshot().unwrap();
    assert!(matches!(
        snapshot.connections[0].state,
        ConnectionState::Ready
    ));
    let d = snapshot.connections[0].diagnostic.as_ref().unwrap();
    assert_eq!(d.code, "network_timeout");
    assert_eq!(d.stage, "authorization");
    assert!(d.retryable);
    let lease = m
        .begin(&format!("local-{}", r.connection_id), "flash", client())
        .await
        .unwrap();
    lease.authorize(false).await.unwrap();
    assert!(m.snapshot().unwrap().connections[0].diagnostic.is_none());
    // Authorization success cannot clear an unrelated catalog failure.
    m.failed(&entry, &e.clone().at("catalog"));
    lease.authorize(false).await.unwrap();
    assert_eq!(
        m.snapshot().unwrap().connections[0]
            .diagnostic
            .as_ref()
            .unwrap()
            .stage,
        "catalog"
    );
    drop(lease);
    m.disconnect(&r.connection_id).await.unwrap();
    m.failed(&entry, &e);
    assert!(matches!(
        m.snapshot().unwrap().connections[0].state,
        ConnectionState::Disabled
    ));
}

#[test]
fn forbidden_is_not_login_expiry_for_either_adapter() {
    for adapter in ["qwenwork", "qoder"] {
        let mut e = her_llm::LlmError::new(her_llm::ErrorKind::Auth, "forbidden");
        e.status = Some(403);
        assert!(!adapter::authentication_failure(adapter, &e));
        e.status = Some(401);
        assert!(adapter::authentication_failure(adapter, &e));
    }
}

#[tokio::test]
async fn discovery_and_retry_preflight_preserve_diagnostics() {
    let dir = tempfile::tempdir().unwrap();
    let env = Fake::new();
    let llm = registry();
    let r = record(true);
    save_config(dir.path(), &r);
    let m = Manager::with_environment(dir.path().into(), &llm, env.clone());
    *env.discovery_failure.write() =
        Some(ConnectionError::new("source_unreadable", "cannot read app").at("discovery"));
    let discoveries = m.discover().await.unwrap();
    assert_eq!(
        discoveries[0].diagnostic.as_ref().unwrap().code,
        "source_unreadable"
    );
    assert_eq!(discoveries[0].login, "unavailable");
    *env.login_failure.write() =
        Some(ConnectionError::new("permission_cancelled", "cancelled").at("keychain"));
    assert_eq!(
        m.retry(&r.connection_id).await.unwrap_err().code,
        "permission_cancelled"
    );
    let snapshot = m.snapshot().unwrap();
    assert!(matches!(
        snapshot.connections[0].state,
        ConnectionState::PermissionRequired
    ));
    assert_eq!(
        snapshot.connections[0].diagnostic.as_ref().unwrap().stage,
        "keychain"
    );
}

#[tokio::test]
async fn earlier_request_failure_does_not_overwrite_a_new_validation() {
    let dir = tempfile::tempdir().unwrap();
    let llm = registry();
    let r = record(true);
    save_config(dir.path(), &r);
    let m = Manager::with_environment(dir.path().into(), &llm, Fake::new());
    let entry = m.entry(&r.connection_id).unwrap();
    let lease = m
        .begin(&format!("local-{}", r.connection_id), "flash", client())
        .await
        .unwrap();
    // A completed explicit check advances this generation while the old inference is in flight.
    entry.generation.fetch_add(1, Ordering::AcqRel);
    let mut failure = her_llm::LlmError::new(her_llm::ErrorKind::Auth, "old request rejected");
    failure.status = Some(401);
    lease.failed(&mut failure);
    let snapshot = m.snapshot().unwrap();
    assert!(matches!(
        snapshot.connections[0].state,
        ConnectionState::Ready
    ));
    assert!(snapshot.connections[0].diagnostic.is_none());
}
