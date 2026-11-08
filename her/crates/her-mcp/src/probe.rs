//! Checking a server before it is added (mcp-design §13.6). Nothing is written. A remote
//! server is tried without credentials first; its 401 leads to its sign-in settings, so the
//! page can offer the right way to authenticate. A local command is started, greeted and asked
//! for its tools. Each step is reported as it starts and ends.

use her_protocol::McpRemoteTransport;
use her_protocol::McpServerConfig;
use her_protocol::customize::McpOAuthClientKind;
use her_protocol::customize::McpProbeAuth;
use her_protocol::customize::McpProbeKind;
use her_protocol::customize::McpProbeOAuth;
use her_protocol::customize::McpProbeResult;
use her_protocol::customize::McpProbeStep;
use her_protocol::customize::McpProbeStepId;
use her_protocol::customize::McpProbeStepState;
use her_protocol::customize::McpProbeSuggestion;
use her_protocol::customize::McpSuggestedAuth;
use her_protocol::customize::McpToolSummary;
use her_protocol::mcp::McpTransportKind;
use parking_lot::Mutex;
use serde_json::json;
use std::sync::Arc;
use std::time::Duration;
use tokio_util::sync::CancellationToken;

use crate::McpServer;
use crate::ServerContext;
use crate::oauth;
use crate::oauth::discovery;
use crate::server::DEFAULT_STARTUP_TIMEOUT;
use crate::server::Phase;
use crate::server::PhaseFn;
use crate::types::Implementation;

pub type ProgressFn = Arc<dyn Fn(&McpProbeStep) + Send + Sync>;

/// How long a probe waits for the logo before answering without it.
const ICON_WAIT: Duration = Duration::from_secs(2);

pub struct ProbeContext {
    pub server: ServerContext,
    /// her's client metadata document, to say which identity a sign-in would use.
    pub metadata_url: Option<String>,
    pub progress: ProgressFn,
    pub cancel: CancellationToken,
    /// Where the service's icon is kept; none: no icon is looked for.
    pub icons: Option<std::path::PathBuf>,
}

/// The steps so far, each reported when it changes.
struct Steps {
    list: Mutex<Vec<McpProbeStep>>,
    progress: ProgressFn,
}

impl Steps {
    fn set(&self, id: McpProbeStepId, state: McpProbeStepState, detail: Option<String>, http_status: Option<u16>) {
        let step = McpProbeStep { id, state, detail, http_status };
        {
            let mut list = self.list.lock();
            match list.iter_mut().find(|s| s.id == id) {
                Some(existing) => *existing = step.clone(),
                None => list.push(step.clone()),
            }
        }
        (self.progress)(&step);
    }

    fn take(&self) -> Vec<McpProbeStep> {
        self.list.lock().clone()
    }
}

fn summaries(tools: &[crate::ToolInfo]) -> Vec<McpToolSummary> {
    tools
        .iter()
        .map(|t| McpToolSummary {
            name: t.name.clone(),
            title: t.title.clone(),
            description: t.description.as_deref().map(|d| d.lines().next().unwrap_or(d).chars().take(300).collect()),
        })
        .collect()
}

pub async fn probe(config: McpServerConfig, ctx: ProbeContext) -> McpProbeResult {
    let steps = Arc::new(Steps { list: Mutex::new(Vec::new()), progress: ctx.progress.clone() });
    let kind = match config.transport() {
        Ok(McpTransportKind::Stdio) => McpProbeKind::Local,
        Ok(McpTransportKind::Http) => McpProbeKind::Remote,
        Err(err) => return failed(McpProbeKind::Local, Vec::new(), err),
    };
    let cancel = ctx.cancel.clone();
    let identity = crate::identity::identity(&config);
    let url = config.url.clone();
    let work = async {
        let info = Mutex::new(None::<Implementation>);
        let mut result = match kind {
            McpProbeKind::Local => local(config, &ctx, &steps, &info).await,
            McpProbeKind::Remote => remote(config, &ctx, &steps, &info).await,
        };
        // The logo, once the server answered: what it declares, else its website's. A slow
        // website does not hold up the check: the lookup goes on and keeps what it finds.
        if result.error.is_none()
            && let (Some(dir), Some(identity)) = (ctx.icons.clone(), identity.clone())
        {
            let (info, url) = (info.lock().clone(), url.clone());
            let lookup = tokio::spawn(async move { crate::icon::refresh(&dir, &identity, url.as_deref(), info.as_ref()).await });
            if let Result::Ok(Result::Ok(stored)) = tokio::time::timeout(ICON_WAIT, lookup).await {
                result.icon = stored.and_then(|s| s.data_uri());
            }
        }
        result
    };
    tokio::select! {
        result = work => result,
        _ = cancel.cancelled() => failed(kind, steps.take(), "cancelled".into()),
    }
}

fn failed(kind: McpProbeKind, steps: Vec<McpProbeStep>, error: String) -> McpProbeResult {
    McpProbeResult {
        kind,
        steps,
        transport: None,
        auth: None,
        suggested: McpProbeSuggestion { auth: McpSuggestedAuth::None, client: None },
        tools: Vec::new(),
        icon: None,
        error: Some(error),
    }
}

/// Connects as a session would, reporting the stages to `on_phase`. The connection is closed
/// before returning.
async fn connect(config: McpServerConfig, ctx: &ServerContext, on_phase: PhaseFn) -> Result<(Vec<crate::ToolInfo>, Option<Implementation>), String> {
    let timeout = config.startup_timeout_secs.map(Duration::from_secs).unwrap_or(DEFAULT_STARTUP_TIMEOUT);
    let server = McpServer::new(config, ServerContext { on_status: None, ..ctx.clone() });
    match tokio::time::timeout(timeout, server.clone().connect_once(timeout, Some(on_phase))).await {
        Ok(Ok(connected)) => {
            connected.client.close().await;
            Ok((connected.tools, connected.info))
        }
        Ok(Err((error, _))) => Err(error),
        Err(_) => Err(format!("no answer within {} s", timeout.as_secs())),
    }
}

async fn local(config: McpServerConfig, ctx: &ProbeContext, steps: &Arc<Steps>, info: &Mutex<Option<Implementation>>) -> McpProbeResult {
    use McpProbeStepId::*;
    use McpProbeStepState::*;
    steps.set(Start, Running, None, None);
    let seen = Arc::new(Mutex::new(None::<Phase>));
    let on_phase: PhaseFn = {
        let (steps, seen) = (steps.clone(), seen.clone());
        Arc::new(move |phase| {
            *seen.lock() = Some(phase);
            match phase {
                Phase::Started => {
                    steps.set(Start, Ok, None, None);
                    steps.set(Handshake, Running, None, None);
                }
                Phase::Initialized => {
                    steps.set(Handshake, Ok, None, None);
                    steps.set(Tools, Running, None, None);
                }
                Phase::Listed => {}
            }
        })
    };
    match connect(config, &ctx.server, on_phase).await {
        Result::Ok((tools, said)) => {
            *info.lock() = said;
            steps.set(Tools, Ok, Some(format!("{} tools", tools.len())), None);
            McpProbeResult {
                kind: McpProbeKind::Local,
                steps: steps.take(),
                transport: None,
                auth: None,
                suggested: McpProbeSuggestion { auth: McpSuggestedAuth::None, client: None },
                tools: summaries(&tools),
                icon: None,
                error: None,
            }
        }
        Err(error) => {
            let at = match *seen.lock() {
                None => Start,
                Some(Phase::Started) => Handshake,
                Some(_) => Tools,
            };
            steps.set(at, Failed, Some(error.clone()), None);
            for later in [Start, Handshake, Tools].into_iter().skip_while(|s| *s != at).skip(1) {
                steps.set(later, Skipped, None, None);
            }
            failed(McpProbeKind::Local, steps.take(), error)
        }
    }
}

/// The address and the configured headers, expanded as a connection would.
fn target(config: &McpServerConfig) -> Result<(String, Vec<(String, String)>), String> {
    let names = crate::env::credential_names(config.url.iter().map(String::as_str).chain(config.headers.values().map(String::as_str)));
    let credentials: std::collections::HashMap<String, String> = names.into_iter().filter_map(|n| her_llm::credentials::get(&n).map(|v| (n, v))).collect();
    let lookup = |name: &str| match name.strip_prefix("credential:") {
        Some(c) => credentials.get(c).cloned(),
        None => crate::env::process_lookup(name),
    };
    let url = crate::env::expand(config.url.as_deref().unwrap_or_default(), &lookup)?;
    let mut headers = Vec::new();
    for (name, value) in &config.headers {
        headers.push((name.clone(), crate::env::expand(value, &lookup)?));
    }
    if let Some(var) = &config.bearer_token_env_var {
        let token = lookup(var).filter(|t| !t.is_empty()).ok_or_else(|| format!("environment variable {var} (bearer_token_env_var) is not set"))?;
        headers.push(("Authorization".into(), format!("Bearer {token}")));
    }
    Ok((url, headers))
}

/// The first answer without OAuth: its status and challenge, or why there was none.
async fn knock(http: &reqwest::Client, url: &str, headers: &[(String, String)], transport: McpRemoteTransport) -> Result<(u16, Option<discovery::Challenge>), String> {
    let mut request = match transport {
        McpRemoteTransport::StreamableHttp => {
            let body = json!({"jsonrpc": "2.0", "id": 0, "method": "initialize", "params": {
                "protocolVersion": crate::types::LATEST_PROTOCOL_VERSION, "capabilities": {}, "clientInfo": {"name": "her", "version": env!("CARGO_PKG_VERSION")}}});
            http.post(url).header("accept", "application/json, text/event-stream").json(&body)
        }
        McpRemoteTransport::Sse => http.get(url).header("accept", "text/event-stream"),
    };
    for (name, value) in headers {
        request = request.header(name.as_str(), value.as_str());
    }
    let response = request.timeout(Duration::from_secs(15)).send().await.map_err(|e| describe_request_error(&e))?;
    let status = response.status().as_u16();
    let challenge = response.headers().get("www-authenticate").and_then(|v| v.to_str().ok()).map(discovery::parse_challenge);
    Ok((status, challenge))
}

fn describe_request_error(err: &reqwest::Error) -> String {
    let mut text = err.to_string();
    let mut source = std::error::Error::source(err);
    while let Some(inner) = source {
        text = format!("{text}: {inner}");
        source = inner.source();
    }
    text
}

async fn remote(mut config: McpServerConfig, ctx: &ProbeContext, steps: &Arc<Steps>, info: &Mutex<Option<Implementation>>) -> McpProbeResult {
    use McpProbeStepId::*;
    use McpProbeStepState::*;
    let fail_at = |id: McpProbeStepId, error: String, status: Option<u16>| {
        steps.set(id, Failed, Some(error.clone()), status);
        for later in [Connect, Resource, AuthServer].into_iter().skip_while(|s| *s != id).skip(1) {
            steps.set(later, Skipped, None, None);
        }
        failed(McpProbeKind::Remote, steps.take(), error)
    };
    steps.set(Connect, Running, None, None);
    let Some(http) = ctx.server.http.clone() else {
        return fail_at(Connect, "remote MCP servers need an HTTP client, which is not available here".into(), None);
    };
    let (url, headers) = match target(&config) {
        Result::Ok(target) => target,
        Err(error) => return fail_at(Connect, error, None),
    };
    let has_credentials = !headers.is_empty() && headers.iter().any(|(n, _)| n.eq_ignore_ascii_case("authorization")) || config.bearer_token_env_var.is_some();
    let mut transport = config.remote_transport();
    let mut answer = knock(&http, &url, &headers, transport).await;
    // The older transport refuses a POST: ask its event stream instead.
    if transport == McpRemoteTransport::StreamableHttp && matches!(answer, Result::Ok((400 | 404 | 405, None))) {
        let sse = knock(&http, &url, &headers, McpRemoteTransport::Sse).await;
        if matches!(sse, Result::Ok((200 | 401, _))) {
            transport = McpRemoteTransport::Sse;
            answer = sse;
        }
    }
    config.transport = Some(transport);
    let (status, challenge) = match answer {
        Result::Ok(answer) => answer,
        Err(error) => return fail_at(Connect, error, None),
    };
    let ok_result = |tools: Vec<crate::ToolInfo>, auth: Option<McpProbeAuth>, suggested: McpSuggestedAuth| McpProbeResult {
        kind: McpProbeKind::Remote,
        steps: steps.take(),
        transport: Some(transport),
        auth,
        suggested: McpProbeSuggestion { auth: suggested, client: None },
        tools: summaries(&tools),
        icon: None,
        error: None,
    };
    if (200..300).contains(&status) {
        let tools = match connect(config, &ctx.server, Arc::new(|_| {})).await {
            Result::Ok((tools, said)) => {
                *info.lock() = said;
                tools
            }
            Err(error) => return fail_at(Connect, error, Some(status)),
        };
        steps.set(Connect, Ok, Some(format!("connected; {} tools", tools.len())), Some(status));
        steps.set(Resource, Skipped, Some("no sign-in needed".into()), None);
        steps.set(AuthServer, Skipped, Some("no sign-in needed".into()), None);
        let suggested = if has_credentials { McpSuggestedAuth::Token } else { McpSuggestedAuth::None };
        return ok_result(tools, Some(McpProbeAuth { required: has_credentials, oauth: None }), suggested);
    }
    if status != 401 {
        return fail_at(Connect, format!("the server answered HTTP {status}"), Some(status));
    }
    if has_credentials {
        return fail_at(Connect, "the server refused the token (HTTP 401)".into(), Some(status));
    }
    steps.set(Connect, Ok, Some("the server requires authorization".into()), Some(status));
    steps.set(Resource, Running, None, None);
    let hint = challenge.as_ref().and_then(|c| c.resource_metadata.clone());
    let resource = match discovery::resource_metadata(&http, &url, hint.as_deref()).await {
        Result::Ok(resource) => resource,
        Err(_) => {
            steps.set(Resource, Skipped, Some("no OAuth settings published; it probably takes an API key".into()), None);
            steps.set(AuthServer, Skipped, None, None);
            return ok_result(Vec::new(), Some(McpProbeAuth { required: true, oauth: None }), McpSuggestedAuth::Token);
        }
    };
    let issuer = resource.authorization_servers[0].clone();
    steps.set(Resource, Ok, Some(format!("authorization server {issuer}")), None);
    steps.set(AuthServer, Running, None, None);
    let meta = match discovery::auth_server_metadata(&http, &issuer).await {
        Result::Ok(meta) => meta,
        Err(error) => return fail_at(AuthServer, error, None),
    };
    let client = oauth::choose_kind(config.oauth.is_some(), &meta, ctx.metadata_url.as_deref(), None);
    let mut found = Vec::new();
    found.push(if meta.supports_s256() { "PKCE" } else { "no PKCE (S256)" });
    if meta.client_id_metadata_document_supported {
        found.push("client metadata documents");
    }
    if meta.registration_endpoint.is_some() {
        found.push("dynamic registration");
    }
    steps.set(AuthServer, Ok, Some(found.join(", ")), None);
    let mut scopes = challenge.and_then(|c| c.scope).map(|s| s.split_whitespace().map(String::from).collect::<Vec<_>>()).unwrap_or_default();
    if scopes.is_empty() {
        scopes = resource.scopes_supported.clone();
    }
    let oauth = McpProbeOAuth {
        authorization_server: meta.issuer.clone(),
        scopes,
        pkce: meta.supports_s256(),
        metadata_document: meta.client_id_metadata_document_supported,
        registration: meta.registration_endpoint.is_some(),
    };
    let (suggested, client) = if meta.supports_s256() {
        (McpSuggestedAuth::Oauth, Some(client.unwrap_or(McpOAuthClientKind::Own)))
    } else {
        (McpSuggestedAuth::Token, None)
    };
    McpProbeResult {
        kind: McpProbeKind::Remote,
        steps: steps.take(),
        transport: Some(transport),
        auth: Some(McpProbeAuth { required: true, oauth: Some(oauth) }),
        suggested: McpProbeSuggestion { auth: suggested, client },
        tools: Vec::new(),
        icon: None,
        error: None,
    }
}
