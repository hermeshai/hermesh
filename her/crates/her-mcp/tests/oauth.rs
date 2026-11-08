//! Remote servers that need more than a URL: the older HTTP+SSE transport, checking a server
//! before it is added, and signing in with OAuth (mcp-design §13.6–§13.8), against the test
//! server and its authorization server in her-mock.

use her_mcp::CallOptions;
use her_mcp::McpServer;
use her_mcp::ServerContext;
use her_mcp::oauth;
use her_mcp::oauth::LoginRequest;
use her_mcp::oauth::OAuthPaths;
use her_mcp::probe::ProbeContext;
use her_mock::mcp::McpTestOptions;
use her_mock::mcp_auth::McpTestAuth;
use her_mock::mcp_auth::OAuthOptions;
use her_protocol::McpOAuthClient;
use her_protocol::McpRemoteTransport;
use her_protocol::McpServerConfig;
use her_protocol::McpServerState;
use her_protocol::customize::McpOAuthClientKind;
use her_protocol::customize::McpProbeResult;
use her_protocol::customize::McpProbeStepId;
use her_protocol::customize::McpProbeStepState;
use her_protocol::customize::McpSuggestedAuth;
use parking_lot::Mutex;
use serde_json::json;
use std::collections::HashMap;
use std::path::Path;
use std::sync::Arc;
use std::sync::Once;
use std::time::Duration;
use std::time::Instant;
use tokio_util::sync::CancellationToken;

/// Tokens go to an in-memory store, shared by the tests of this file.
struct MemoryStore(Mutex<HashMap<String, String>>);

impl her_llm::credentials::CredentialStore for MemoryStore {
    fn name(&self) -> &'static str {
        "memory"
    }
    fn get(&self, provider: &str) -> Result<Option<String>, String> {
        Ok(self.0.lock().get(provider).cloned())
    }
    fn set(&self, provider: &str, key: &str) -> Result<(), String> {
        self.0.lock().insert(provider.into(), key.into());
        Ok(())
    }
    fn delete(&self, provider: &str) -> Result<(), String> {
        self.0.lock().remove(provider);
        Ok(())
    }
}

fn store() {
    static ONCE: Once = Once::new();
    ONCE.call_once(|| her_llm::credentials::install(Box::new(MemoryStore(Mutex::new(HashMap::new())))));
}

fn http_client() -> reqwest::Client {
    let catalog = her_llm::Catalog::builtin().unwrap();
    her_llm::ModelRegistry::new(catalog, her_llm::RegistryOptions::default()).http_client().unwrap()
}

fn context(dir: &Path, home: Option<&Path>) -> ServerContext {
    ServerContext {
        http: Some(http_client()),
        roots: vec![dir.to_path_buf()],
        cwd: dir.to_path_buf(),
        project_root: None,
        on_status: None,
        oauth: home.map(|h| OAuthPaths { her_home: h.to_path_buf() }),
    }
}

fn remote(name: &str, url: &str) -> McpServerConfig {
    McpServerConfig { name: name.into(), url: Some(url.into()), ..Default::default() }
}

async fn probe(config: McpServerConfig, dir: &Path, metadata_url: Option<&str>) -> (McpProbeResult, Vec<(McpProbeStepId, McpProbeStepState)>) {
    let seen = Arc::new(Mutex::new(Vec::new()));
    let sink = seen.clone();
    let ctx = ProbeContext {
        server: context(dir, None),
        metadata_url: metadata_url.map(String::from),
        progress: Arc::new(move |step| sink.lock().push((step.id, step.state))),
        cancel: CancellationToken::new(),
        icons: None,
    };
    let result = her_mcp::probe::probe(config, ctx).await;
    let seen = seen.lock().clone();
    (result, seen)
}

fn state(result: &McpProbeResult, id: McpProbeStepId) -> McpProbeStepState {
    result.steps.iter().find(|s| s.id == id).map(|s| s.state).unwrap_or_else(|| panic!("no step {id:?}: {result:#?}"))
}

async fn ready(server: &Arc<McpServer>) {
    server.connect();
    server.wait_settled(Instant::now() + Duration::from_secs(20)).await;
}

fn oauth_options(metadata_document: bool, registration: bool, ttl: u64) -> McpTestOptions {
    McpTestOptions {
        auth: McpTestAuth::OAuth(OAuthOptions { metadata_document, registration, token_ttl_secs: ttl }),
        ..Default::default()
    }
}

#[tokio::test]
async fn legacy_sse_servers_connect_by_address_or_by_probing() {
    store();
    let dir = tempfile::tempdir().unwrap();
    let mock = her_mock::mcp::serve_http("127.0.0.1:0", McpTestOptions { legacy_sse: true, ..Default::default() }).await.unwrap();
    // An address ending in /sse picks the older transport by itself.
    let server = McpServer::new(remote("legacy", &mock.url()), context(dir.path(), None));
    ready(&server).await;
    assert_eq!(server.status().state, McpServerState::Ready, "{:?}", server.status());
    let options = CallOptions { cancel: CancellationToken::new(), progress: None };
    let result = server.call_tool("echo", json!({"text": "over sse"}), options).await.unwrap();
    assert_eq!(result.content.len(), 1, "{result:?}");
    server.shutdown().await;

    // Elsewhere, a POST refused with 405 leads the probe to the event stream.
    let other = mock.url().replace("/sse", "/stream");
    let (result, _) = probe(remote("legacy", &other), dir.path(), None).await;
    assert_eq!(result.error, None, "{result:#?}");
    assert_eq!(result.transport, Some(McpRemoteTransport::Sse));
    assert!(result.tools.iter().any(|t| t.name == "echo"));
    assert_eq!(result.suggested.auth, McpSuggestedAuth::None);
}

#[tokio::test]
async fn probing_reports_each_step_and_suggests_how_to_authenticate() {
    store();
    let dir = tempfile::tempdir().unwrap();
    let open = her_mock::mcp::serve_http("127.0.0.1:0", McpTestOptions::default()).await.unwrap();
    let (result, seen) = probe(remote("open", &open.url()), dir.path(), None).await;
    assert_eq!(result.error, None);
    assert_eq!(result.suggested.auth, McpSuggestedAuth::None);
    assert_eq!(result.transport, Some(McpRemoteTransport::StreamableHttp));
    assert!(result.tools.len() > 3);
    assert_eq!(seen.first(), Some(&(McpProbeStepId::Connect, McpProbeStepState::Running)));
    assert_eq!(state(&result, McpProbeStepId::Resource), McpProbeStepState::Skipped);

    let oauth = her_mock::mcp::serve_http("127.0.0.1:0", oauth_options(true, true, 3600)).await.unwrap();
    let (result, _) = probe(remote("o", &oauth.url()), dir.path(), Some("https://her.example/oauth/client.json")).await;
    assert_eq!(result.error, None, "{result:#?}");
    let found = result.auth.as_ref().and_then(|a| a.oauth.clone()).expect("oauth settings");
    assert!(found.pkce && found.metadata_document && found.registration, "{found:?}");
    assert_eq!(found.authorization_server, oauth.url().trim_end_matches("/mcp"));
    assert_eq!((result.suggested.auth, result.suggested.client), (McpSuggestedAuth::Oauth, Some(McpOAuthClientKind::MetadataDocument)));
    assert_eq!(result.steps.iter().find(|s| s.id == McpProbeStepId::Connect).unwrap().http_status, Some(401));
    // Without a published document, registration is next.
    let (result, _) = probe(remote("o", &oauth.url()), dir.path(), None).await;
    assert_eq!(result.suggested.client, Some(McpOAuthClientKind::Registration));

    let neither = her_mock::mcp::serve_http("127.0.0.1:0", oauth_options(false, false, 3600)).await.unwrap();
    let (result, _) = probe(remote("n", &neither.url()), dir.path(), Some("https://her.example/c.json")).await;
    assert_eq!(result.suggested.client, Some(McpOAuthClientKind::Own), "only the user's own client is left");

    let keyed = her_mock::mcp::serve_http("127.0.0.1:0", McpTestOptions { auth: McpTestAuth::Token("sk-right".into()), ..Default::default() }).await.unwrap();
    let (result, _) = probe(remote("k", &keyed.url()), dir.path(), None).await;
    assert_eq!(result.suggested.auth, McpSuggestedAuth::Token, "a 401 without OAuth settings wants a key: {result:#?}");
    assert_eq!(state(&result, McpProbeStepId::Resource), McpProbeStepState::Skipped);
    let mut with_key = remote("k", &keyed.url());
    with_key.headers.insert("Authorization".into(), "Bearer sk-wrong".into());
    let (result, _) = probe(with_key.clone(), dir.path(), None).await;
    assert!(result.error.as_deref().is_some_and(|e| e.contains("refused the token")), "{result:#?}");
    with_key.headers.insert("Authorization".into(), "Bearer sk-right".into());
    let (result, _) = probe(with_key, dir.path(), None).await;
    assert_eq!(result.error, None);
    assert!(!result.tools.is_empty());

    let (result, _) = probe(remote("gone", "http://127.0.0.1:9/mcp"), dir.path(), None).await;
    assert_eq!(state(&result, McpProbeStepId::Connect), McpProbeStepState::Failed);
    assert_eq!(state(&result, McpProbeStepId::AuthServer), McpProbeStepState::Skipped);

    // A local command: started, greeted, listed.
    let local = McpServerConfig { name: "l".into(), command: Some("/nonexistent/her-test-server".into()), ..Default::default() };
    let (result, _) = probe(local, dir.path(), None).await;
    assert_eq!(state(&result, McpProbeStepId::Start), McpProbeStepState::Failed);
    assert_eq!(state(&result, McpProbeStepId::Tools), McpProbeStepState::Skipped);
}

/// Signs in to `url` as `name`, following the authorization server's redirect as a browser
/// would.
async fn sign_in(name: &str, url: &str, home: &Path, own: Option<McpOAuthClient>, metadata_url: Option<&str>) -> McpOAuthClientKind {
    let pending = oauth::start_login(LoginRequest {
        server: name.into(),
        url: url.into(),
        own,
        metadata_url: metadata_url.map(String::from),
        paths: Some(OAuthPaths { her_home: home.to_path_buf() }),
        http: http_client(),
        prefer: None,
        redirect_uri: None,
    })
    .await
    .unwrap();
    let kind = pending.client;
    let authorize = pending.authorize_url.clone();
    // A connection that says nothing, as a browser's speculative one, does not hold up the
    // redirect that comes after it.
    let redirect = reqwest::Url::parse(&authorize).unwrap().query_pairs().find(|(k, _)| k == "redirect_uri").unwrap().1.into_owned();
    let port = reqwest::Url::parse(&redirect).unwrap().port().unwrap();
    let _idle = tokio::net::TcpStream::connect(("127.0.0.1", port)).await.unwrap();
    let started = std::time::Instant::now();
    let (status, page) = her_mock::mcp_auth::browse(&authorize).await.unwrap();
    assert!(status.contains("200") && page.contains("Hermesh"), "{status}\n{page}");
    pending.wait(Duration::from_secs(10)).await.unwrap();
    assert!(started.elapsed() < Duration::from_secs(5), "the sign-in waited {:?}", started.elapsed());
    kind
}

#[tokio::test]
async fn signing_in_reuses_one_registration_refreshes_and_signs_out() {
    store();
    let dir = tempfile::tempdir().unwrap();
    let home = dir.path().join("her");
    let mock = her_mock::mcp::serve_http("127.0.0.1:0", oauth_options(false, true, 1)).await.unwrap();
    let mut config = remote("tracker", &mock.url());
    config.auth = Some(her_protocol::McpAuthMode::Oauth);

    // Before signing in, the server waits for someone to sign in instead of failing for good.
    let server = McpServer::new(config.clone(), context(dir.path(), Some(&home)));
    ready(&server).await;
    assert_eq!(server.status().state, McpServerState::NeedsLogin, "{:?}", server.status());
    assert!(server.status().error.unwrap().contains("her mcp login tracker"));

    assert_eq!(sign_in("tracker", &mock.url(), &home, None, None).await, McpOAuthClientKind::Registration);
    assert!(oauth::signed_in("tracker", &mock.url()));
    // The token lives a second: the call refreshes it first, and works.
    tokio::time::sleep(Duration::from_millis(1100)).await;
    let options = CallOptions { cancel: CancellationToken::new(), progress: None };
    let result = server.call_tool("echo", json!({"text": "signed in"}), options.clone()).await;
    assert!(result.is_ok(), "{result:?}");
    let (registrations, grants, refreshes, _) = mock.oauth_counts();
    assert_eq!((registrations, grants), (1, 1));
    assert!(refreshes >= 1, "refreshed before use");
    server.shutdown().await;

    // Signing in again reuses the registration: one client per authorization server.
    assert_eq!(sign_in("tracker", &mock.url(), &home, None, None).await, McpOAuthClientKind::Registration);
    assert_eq!(mock.oauth_counts().0, 1, "no second registration");
    let saved = std::fs::read_to_string(home.join("mcp/oauth-clients.json")).unwrap();
    assert!(saved.contains("client-"), "{saved}");

    oauth::logout("tracker", &http_client()).await.unwrap();
    assert!(!oauth::signed_in("tracker", &mock.url()));
    assert_eq!(mock.oauth_counts().3, 1, "the refresh token was revoked");
}

#[tokio::test]
async fn the_metadata_document_and_the_users_own_client() {
    store();
    let dir = tempfile::tempdir().unwrap();
    let home = dir.path().join("her");
    let mock = her_mock::mcp::serve_http("127.0.0.1:0", oauth_options(true, true, 3600)).await.unwrap();
    let document = "https://her.example/oauth/client.json";
    assert_eq!(sign_in("docs", &mock.url(), &home, None, Some(document)).await, McpOAuthClientKind::MetadataDocument);
    assert_eq!(mock.last_client().as_deref(), Some(document), "the document's address is the client_id");
    assert_eq!(mock.oauth_counts().0, 0, "nothing registered");

    // The user's own client goes first, and is refused here because the server never heard of it.
    let own = McpOAuthClient { client_id: "my-own-app".into(), client_secret: None, scopes: vec!["read".into()] };
    let pending = oauth::start_login(LoginRequest {
        server: "mine".into(),
        url: mock.url(),
        own: Some(own),
        metadata_url: Some(document.into()),
        paths: Some(OAuthPaths { her_home: home.clone() }),
        http: http_client(),
        prefer: None,
        redirect_uri: None,
    })
    .await
    .unwrap();
    assert_eq!(pending.client, McpOAuthClientKind::Own);
    assert!(pending.authorize_url.contains("client_id=my-own-app") && pending.authorize_url.contains("scope=read"), "{}", pending.authorize_url);

    // A server that offers neither: the sign-in says to bring a client or a token.
    let neither = her_mock::mcp::serve_http("127.0.0.1:0", oauth_options(false, false, 3600)).await.unwrap();
    let err = oauth::start_login(LoginRequest {
        server: "n".into(),
        url: neither.url(),
        own: None,
        metadata_url: None,
        paths: None,
        http: http_client(),
        prefer: None,
        redirect_uri: None,
    })
    .await
    .err()
    .unwrap();
    assert!(err.contains("oauth.client_id"), "{err}");
}

/// A sign-in whose browser is on the client's device (her-server.md §5.4): nothing listens
/// here, and the client hands back what the redirect carried.
#[tokio::test]
async fn a_sign_in_for_another_device_is_completed_with_what_its_redirect_carried() {
    store();
    let dir = tempfile::tempdir().unwrap();
    let home = dir.path().join("her");
    let mock = her_mock::mcp::serve_http("127.0.0.1:0", oauth_options(false, true, 3600)).await.unwrap();
    // Port 9 (discard): nothing answers there, so following the redirect would fail.
    let redirect = "http://127.0.0.1:9/callback";
    let start = |server: &str| {
        oauth::start_login(LoginRequest {
            server: server.into(),
            url: mock.url(),
            own: None,
            metadata_url: None,
            paths: Some(OAuthPaths { her_home: home.clone() }),
            http: http_client(),
            prefer: None,
            redirect_uri: Some(redirect.into()),
        })
    };
    // Where the authorization server sends the browser, read without following it: her's client
    // follows no redirect.
    let browser = http_client();
    let redirected = |authorize: String| {
        let browser = browser.clone();
        async move {
            let response = browser.get(&authorize).send().await.unwrap();
            let location = response.headers()["location"].to_str().unwrap().to_string();
            assert!(location.starts_with(&format!("{redirect}?")), "{location}");
            let query: HashMap<String, String> = reqwest::Url::parse(&location).unwrap().query_pairs().into_owned().collect();
            query
        }
    };

    let pending = start("far").await.unwrap();
    assert_eq!(pending.client, McpOAuthClientKind::Registration);
    let completion = pending.completion().expect("a sign-in for another device");
    let query = redirected(pending.authorize_url.clone()).await;
    // A state that is not this sign-in's is refused, and the sign-in waits on.
    assert!(!completion.matches("wrong"));
    let refused = completion.complete(query["code"].clone(), "wrong".into(), query.get("iss").cloned()).await;
    assert!(refused.is_err_and(|e| e.contains("state")));
    assert!(completion.matches(&query["state"]));
    completion.complete(query["code"].clone(), query["state"].clone(), query.get("iss").cloned()).await.unwrap();
    pending.wait(Duration::from_secs(10)).await.unwrap();
    assert!(oauth::signed_in("far", &mock.url()));
    let saved = std::fs::read_to_string(home.join("mcp/oauth-clients.json")).unwrap();
    assert!(saved.contains(redirect), "the client is registered for the device's address: {saved}");
    // Once it has ended, nothing more is taken.
    assert!(completion.complete(query["code"].clone(), query["state"].clone(), None).await.is_err());

    // The server says it names itself in the redirect (RFC 9207): without its name, the code may
    // be another server's, and the sign-in fails.
    let pending = start("unnamed").await.unwrap();
    let completion = pending.completion().unwrap();
    let query = redirected(pending.authorize_url.clone()).await;
    let failed = completion.complete(query["code"].clone(), query["state"].clone(), None).await;
    assert!(failed.is_err_and(|e| e.contains("iss")));
    assert!(pending.wait(Duration::from_secs(10)).await.is_err());
    assert!(!oauth::signed_in("unnamed", &mock.url()));
    assert_eq!(mock.oauth_counts().0, 1, "the registration for the device's address is reused");
}
