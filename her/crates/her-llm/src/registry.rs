//! The entry point for callers: resolves models, credentials and adapters, converts history,
//! retries, and prices the usage.

use futures::StreamExt;
use parking_lot::RwLock;
use std::collections::BTreeMap;
use std::collections::HashMap;
use std::sync::Arc;
use std::sync::OnceLock;
use std::time::Duration;
use tokio::time::Instant;

use crate::api::Timeouts;
use crate::api::WireApi;
use crate::api::WireRequest;
use crate::api::anthropic::AnthropicApi;
use crate::api::openai_chat::OpenAiChatApi;
use crate::catalog::Catalog;
use crate::catalog::ProviderConfig;
use crate::directory::ModelDirectory;
use crate::discovery::ListError;
use crate::discovery::ListSource;
use crate::discovery::Listed;
use crate::discovery::ModelList;
use crate::error::ErrorKind;
use crate::error::LlmError;
use crate::model::Model;
use crate::stream::LlmStream;
use crate::stream::MessageAccumulator;
use crate::stream::StreamEvent;
use crate::transform::transform_for;
use crate::types::Context;
use crate::types::RequestOptions;
use crate::usage::apply_cost;

fn build_http(options: &RegistryOptions) -> Result<reqwest::Client, LlmError> {
    let user_agent = options.user_agent.clone().unwrap_or_else(|| {
        format!(
            "her/{} ({}; {})",
            env!("CARGO_PKG_VERSION"),
            std::env::consts::OS,
            std::env::consts::ARCH
        )
    });
    let tls = crate::tls::client_config()
        .map_err(|e| LlmError::new(ErrorKind::Unknown, format!("failed to set up TLS: {e}")))?;
    reqwest::Client::builder()
        .use_preconfigured_tls(tls)
        .connect_timeout(options.timeouts.connect)
        // Idle connections hold read and write buffers; a quiet service drops them sooner
        // than the 90-second default.
        .pool_idle_timeout(Duration::from_secs(30))
        .user_agent(user_agent)
        .redirect(reqwest::redirect::Policy::none())
        .build()
        .map_err(|e| {
            LlmError::new(
                ErrorKind::Unknown,
                format!("failed to build HTTP client: {e}"),
            )
        })
}

#[derive(Clone, Debug)]
pub struct RetryPolicy {
    /// How long a request keeps retrying, counted from its first failure.
    pub window: Duration,
    /// At most this many retries; `None` leaves it to `window`.
    pub max_retries: Option<u32>,
    pub base_delay: Duration,
    pub max_delay: Duration,
}

impl Default for RetryPolicy {
    fn default() -> Self {
        Self {
            window: Duration::from_secs(600),
            max_retries: None,
            base_delay: Duration::from_millis(500),
            max_delay: Duration::from_secs(30),
        }
    }
}

impl RetryPolicy {
    /// Whether a request that has been retried `attempt` times, and first failed at `since`,
    /// may be retried again.
    fn allows(&self, attempt: u32, since: Instant) -> bool {
        self.max_retries.is_none_or(|max| attempt < max) && since.elapsed() < self.window
    }

    /// Delay before retry number `attempt` (1-based), with `remaining` of the window left: the
    /// last one ends with the window. `Err` carries a server-requested delay longer than that.
    pub fn delay(&self, attempt: u32, retry_after_ms: Option<u64>, remaining: Duration) -> Result<Duration, Duration> {
        if let Some(ms) = retry_after_ms {
            let requested = Duration::from_millis(ms);
            return if requested > remaining { Err(requested) } else { Ok(requested) };
        }
        let exp = self
            .base_delay
            .saturating_mul(2u32.saturating_pow(attempt.saturating_sub(1)));
        let capped = exp.min(self.max_delay);
        let jitter = 0.8 + rand::random::<f64>() * 0.4;
        Ok(capped.mul_f64(jitter).min(remaining))
    }
}

/// The error a request ends with: after retries, it says for how long they went on.
fn gave_up(mut error: LlmError, attempt: u32, since: Option<Instant>) -> LlmError {
    if let Some(since) = since.filter(|_| attempt > 0) {
        error.message = format!(
            "{} (gave up after {attempt} retries in {}s)",
            error.message,
            since.elapsed().as_secs()
        );
    }
    error
}

#[derive(Clone, Debug, Default)]
pub struct RegistryOptions {
    pub timeouts: Timeouts,
    pub retry: RetryPolicy,
    pub user_agent: Option<String>,
    /// `~/.her/models`: list and catalog caches, the menu selection. `None` keeps them in memory.
    pub models_dir: Option<std::path::PathBuf>,
}

/// What one list refresh found.
#[derive(Clone, Debug, PartialEq)]
pub struct ListRefresh {
    pub source: ListSource,
    pub models: usize,
    /// The list differs from the previous one.
    pub changed: bool,
}

#[derive(Debug, thiserror::Error)]
pub enum ResolveError {
    #[error("invalid model reference '{0}': expected provider/model-id")]
    BadReference(String),
    #[error("unknown provider '{provider}' (known: {known})")]
    UnknownProvider { provider: String, known: String },
}

pub struct ResolvedModel {
    pub model: Arc<Model>,
    /// The model is not in the catalog; default limits apply.
    pub ad_hoc: bool,
}

pub struct ModelRegistry {
    providers: BTreeMap<String, ProviderConfig>,
    warnings: Vec<String>,
    sources: RwLock<BTreeMap<String, Vec<ProviderConfig>>>,
    authorizer: OnceLock<Arc<dyn crate::connection::ConnectionAuthorizer>>,
    apis: HashMap<String, Arc<dyn WireApi>>,
    /// Built by the first request, so commands that never call a model skip HTTP and TLS setup.
    http: OnceLock<Result<reqwest::Client, LlmError>>,
    directory: ModelDirectory,
    options: RegistryOptions,
}

impl ModelRegistry {
    pub fn new(catalog: Catalog, options: RegistryOptions) -> Self {
        let mut registry = Self {
            providers: catalog.providers,
            warnings: catalog.warnings,
            sources: RwLock::new(BTreeMap::new()),
            authorizer: OnceLock::new(),
            apis: HashMap::new(),
            http: OnceLock::new(),
            directory: ModelDirectory::new(options.models_dir.clone()),
            options,
        };
        registry.register_api(Arc::new(OpenAiChatApi));
        registry.register_api(Arc::new(crate::api::openai_responses::OpenAiResponsesApi));
        registry.register_api(Arc::new(AnthropicApi));
        registry
    }

    /// What reading the catalog left to say: settings it did not know.
    pub fn warnings(&self) -> &[String] {
        &self.warnings
    }

    pub fn register_api(&mut self, api: Arc<dyn WireApi>) {
        self.apis.insert(api.id().to_string(), api);
    }

    pub fn insert_provider(&mut self, provider: ProviderConfig) {
        self.providers.insert(provider.id.clone(), provider);
    }

    /// An owned snapshot: callers never retain a lock across I/O.
    pub fn providers(&self) -> impl Iterator<Item = ProviderConfig> {
        let mut result: Vec<_> = self.providers.values().cloned().collect();
        result.extend(self.sources.read().values().flatten().cloned());
        result.into_iter()
    }

    /// How a provider's credentials stand, without copying its configuration.
    pub fn auth_status(&self, id: &str) -> Option<crate::AuthStatus> {
        if let Some(provider) = self.providers.get(id) {
            return Some(provider.auth.status(id));
        }
        self.sources.read().values().flatten().find(|p| p.id == id).map(|p| p.auth.status(id))
    }

    pub fn provider(&self, id: &str) -> Option<ProviderConfig> {
        self.providers.get(id).cloned().or_else(|| {
            self.sources
                .read()
                .values()
                .flatten()
                .find(|p| p.id == id)
                .cloned()
        })
    }

    /// Atomically replace just one external source, never built-in/user providers.
    pub fn replace_source(
        &self,
        source: &str,
        providers: Vec<ProviderConfig>,
    ) -> Result<(), String> {
        let mut sources = self.sources.write();
        let mut ids = std::collections::HashSet::new();
        for p in &providers {
            if !ids.insert(&p.id)
                || self.providers.contains_key(&p.id)
                || sources
                    .iter()
                    .any(|(s, ps)| s != source && ps.iter().any(|v| v.id == p.id))
            {
                return Err("provider ID is already owned".into());
            }
        }
        let replaced = sources.insert(source.to_string(), providers);
        for p in replaced.iter().flatten().chain(sources.get(source).into_iter().flatten()) {
            self.directory.forget(&p.id);
        }
        Ok(())
    }

    pub fn remove_source(&self, source: &str) {
        for p in self.sources.write().remove(source).iter().flatten() {
            self.directory.forget(&p.id);
        }
    }

    pub fn install_authorizer(&self, resolver: Arc<dyn crate::connection::ConnectionAuthorizer>) {
        let _ = self.authorizer.set(resolver);
    }

    /// Shared network stack, initialized only on first actual connection/model request.
    pub fn http_client(&self) -> Result<reqwest::Client, LlmError> {
        self.http.get_or_init(|| build_http(&self.options)).clone()
    }

    /// Every provider's models, merged from their layers (docs/kernel/model-catalog-design.md §4).
    pub fn directory(&self) -> &ModelDirectory {
        &self.directory
    }

    /// A provider's model, merged from its layers.
    pub fn lookup(&self, provider: &ProviderConfig, id: &str) -> Option<Arc<Model>> {
        self.directory.model(provider, id)
    }

    /// Asks a provider for its model list and remembers the answer. With `key`, that key is used
    /// instead of the provider's credentials, and a failure leaves nothing behind (key checks).
    pub async fn refresh_list(&self, provider_id: &str, key: Option<String>) -> Result<ListRefresh, LlmError> {
        let provider = self
            .provider(provider_id)
            .ok_or_else(|| LlmError::new(ErrorKind::InvalidRequest, format!("unknown provider '{provider_id}'")))?;
        let checking = key.is_some();
        let credentials = match key {
            Some(key) => crate::Credentials::with_key(key),
            None => provider.auth.resolve(provider_id).await?,
        };
        let http = self.http_client()?;
        let fetched = crate::discovery::fetch(&http, &provider, &credentials).await;
        let lists = self.directory.lists_dir();
        let previous = self.directory.list(provider_id);
        let now = crate::discovery::now();
        let credential = credentials.api_key.as_deref().map(crate::discovery::fingerprint);
        let list = match fetched {
            Ok(Listed::Models(models)) => ModelList {
                version: 1,
                fetched_at: Some(now.clone()),
                checked_at: now,
                source: ListSource::Provider,
                credential,
                error: None,
                models,
            },
            Ok(Listed::Unsupported) => ModelList {
                version: 1,
                fetched_at: Some(now.clone()),
                checked_at: now,
                source: ListSource::Unsupported,
                credential,
                error: None,
                models: Vec::new(),
            },
            Err(error) if checking => return Err(error),
            Err(error) => {
                let mut kept = previous.clone().unwrap_or(ModelList {
                    version: 1,
                    fetched_at: None,
                    checked_at: now.clone(),
                    source: ListSource::Provider,
                    credential: None,
                    error: None,
                    models: Vec::new(),
                });
                kept.checked_at = now;
                kept.credential = credential;
                kept.error = Some(ListError { kind: error.kind.as_str().to_string(), message: error.message.clone() });
                if let Some(dir) = &lists {
                    crate::discovery::write_json(&dir.join(format!("{provider_id}.json")), &kept)?;
                }
                self.directory.invalidate();
                return Err(error);
            }
        };
        if let Some(dir) = &lists {
            crate::discovery::write_json(&dir.join(format!("{provider_id}.json")), &list)?;
        }
        self.directory.invalidate();
        Ok(ListRefresh {
            source: list.source,
            models: list.models.len(),
            changed: previous.is_none_or(|p| p.models != list.models || p.source != list.source),
        })
    }

    /// Refreshes the local copy of the public catalog for every provider that names one.
    /// Returns whether any provider's models changed.
    pub async fn refresh_catalog(&self, url: &str) -> Result<bool, LlmError> {
        let Some(dir) = self.directory.catalog_dir() else {
            return Ok(false);
        };
        let mut ids: Vec<String> = self.providers().filter_map(|p| p.listing.catalog_id).collect();
        ids.extend(crate::public_catalog::MAKERS.iter().map(|id| id.to_string()));
        ids.sort();
        ids.dedup();
        let http = self.http_client()?;
        let refs: Vec<&str> = ids.iter().map(String::as_str).collect();
        let changed = crate::public_catalog::refresh(&http, url, &dir, &refs).await?;
        self.directory.invalidate();
        Ok(changed)
    }

    pub fn has_api(&self, id: &str) -> bool {
        self.apis.contains_key(id)
    }

    /// Environment variables holding provider keys (scrubbed from tool subprocesses).
    pub fn key_env_vars(&self) -> Vec<String> {
        let mut vars: Vec<String> = self
            .providers
            .values()
            .flat_map(|p| p.auth.env_vars().iter().cloned())
            .collect();
        vars.sort();
        vars.dedup();
        vars
    }

    /// Resolves `provider/model-id`. Unknown model ids of a known provider get default limits.
    pub fn resolve(&self, reference: &str) -> Result<ResolvedModel, ResolveError> {
        let (provider_id, model_id) = reference
            .split_once('/')
            .filter(|(p, m)| !p.is_empty() && !m.is_empty())
            .ok_or_else(|| ResolveError::BadReference(reference.to_string()))?;
        let provider = self
            .provider(provider_id)
            .ok_or_else(|| ResolveError::UnknownProvider {
                provider: provider_id.to_string(),
                known: self
                    .providers()
                    .map(|p| p.id)
                    .collect::<Vec<_>>()
                    .join(", "),
            })?;
        if matches!(provider.auth, crate::AuthConfig::Managed) && provider.entry(model_id).is_none() {
            return Err(ResolveError::BadReference(reference.to_string()));
        }
        Ok(match self.lookup(&provider, model_id) {
            Some(model) => ResolvedModel {
                model,
                ad_hoc: false,
            },
            None => ResolvedModel {
                model: Arc::new(provider.ad_hoc_model(model_id)),
                ad_hoc: true,
            },
        })
    }

    /// A model whose provider is gone, for a session that has nothing else to run on: it opens
    /// and its history can be read. A request with it fails, as no provider answers to it.
    pub fn unavailable(&self, reference: &str) -> Result<ResolvedModel, ResolveError> {
        let (provider, id) = reference
            .split_once('/')
            .filter(|(p, m)| !p.is_empty() && !m.is_empty())
            .ok_or_else(|| ResolveError::BadReference(reference.to_string()))?;
        let gone = ProviderConfig {
            id: provider.to_string(),
            name: provider.to_string(),
            names: Default::default(),
            api: "openai-chat".to_string(),
            base_url: String::new(),
            auth: crate::AuthConfig::None,
            headers: BTreeMap::new(),
            currency: Default::default(),
            compat: Default::default(),
            listing: Default::default(),
            plan: None,
        };
        Ok(ResolvedModel { model: Arc::new(gone.ad_hoc_model(id)), ad_hoc: true })
    }

    /// Streams one response. Never fails synchronously; see the contract on [`StreamEvent`].
    pub fn stream(
        &self,
        model: Arc<Model>,
        context: Context,
        options: RequestOptions,
    ) -> LlmStream {
        // Capture the current route and its credential together, even for a running
        // conversation whose original Model predates a sidecar restart.
        let provider = self.provider(&model.provider);
        let model = provider
            .as_ref()
            .and_then(|p| self.lookup(p, &model.id))
            .unwrap_or(model);
        let api = self.apis.get(&model.api).cloned();
        let auth = provider.map(|p| p.auth);
        let authorizer = self.authorizer.get().cloned();
        let http = self.http.get_or_init(|| build_http(&self.options)).clone();
        let registry_options = self.options.clone();
        let missing = self.directory.missing_handle();
        Box::pin(async_stream::stream! {
            let base = MessageAccumulator::for_model(&model);
            let http = match http {
                Ok(http) => http,
                Err(err) => {
                    yield StreamEvent::Failed(base.fail(err));
                    return;
                }
            };
            let Some(api) = api else {
                yield StreamEvent::Failed(base.fail(LlmError::new(
                    ErrorKind::InvalidRequest,
                    format!("no adapter for wire protocol '{}'", model.api),
                )));
                return;
            };
            let Some(auth) = auth else {
                yield StreamEvent::Failed(base.fail(LlmError::new(
                    ErrorKind::InvalidRequest,
                    format!("unknown provider '{}'", model.provider),
                )));
                return;
            };
            let authorizer = if matches!(auth, crate::AuthConfig::Managed) {
                let Some(authorizer) = authorizer else {
                    yield StreamEvent::Failed(base.fail(LlmError::new(ErrorKind::Auth, "connection manager unavailable")));
                    return;
                };
                Some(authorizer)
            } else { None };
            let credentials = if authorizer.is_some() { crate::Credentials::default() } else {
                match auth.resolve(&model.provider).await {
                    Ok(credentials) => credentials,
                    Err(error) => { yield StreamEvent::Failed(base.fail(error)); return; }
                }
            };
            let retry = &registry_options.retry;
            // Held while an attempt runs, not while it waits to be retried: a connection
            // disconnected meanwhile need not wait, and refuses the next attempt.
            let mut managed: Option<Box<dyn crate::connection::ConnectionRequest>> = None;
            let mut auth_retried = false;
            let mut refresh_auth = false;
            let context = Arc::new(transform_for(&model, context));
            let mut attempt: u32 = 0;
            // The retry window counts from the first failure.
            let mut first_failure: Option<Instant> = None;
            'requests: loop {
                let error = 'attempt: {
                    if let Some(authorizer) = &authorizer && managed.is_none() {
                        match authorizer.begin(&model.provider, &model.id, http.clone()).await {
                            Ok(request) => managed = Some(request),
                            Err(error) => { yield StreamEvent::Failed(base.fail(gave_up(error, attempt, first_failure))); return; }
                        }
                    }
                    let cancellation = managed.as_ref().map(|m| m.cancellation()).unwrap_or_default();
                    let (request_model, request_credentials, request_api) = if let Some(managed) = &managed {
                        let authorization = tokio::select! {
                            _ = cancellation.cancelled() => Err(LlmError::new(ErrorKind::Auth, "connection cancelled")),
                            value = managed.authorize(refresh_auth) => value,
                        };
                        refresh_auth = false;
                        match authorization {
                            Ok(a) => { let mut current = (*model).clone(); current.base_url = a.base_url; (Arc::new(current), a.credentials, a.transport.unwrap_or_else(|| api.clone())) }
                            Err(error) => {
                                if error.kind.is_retryable() && retry.allows(attempt, *first_failure.get_or_insert_with(Instant::now)) {
                                    break 'attempt error;
                                }
                                yield StreamEvent::Failed(base.fail(gave_up(error, attempt, first_failure)));
                                return;
                            }
                        }
                    } else { (model.clone(), credentials.clone(), api.clone()) };
                    let request = WireRequest {
                        model: request_model,
                        credentials: request_credentials,
                        context: context.clone(),
                        options: options.clone(),
                        http: http.clone(),
                        timeouts: registry_options.timeouts.clone(),
                    };
                    let mut inner = request_api.stream(request);
                    let mut produced = false;
                    loop {
                        let event = tokio::select! {
                            _ = cancellation.cancelled() => {
                                yield StreamEvent::Failed(base.fail(LlmError::new(ErrorKind::Auth, "connection cancelled")));
                                return;
                            }
                            event = inner.next() => event,
                        };
                        let Some(event) = event else {
                            yield StreamEvent::Failed(base.fail(LlmError::new(
                                ErrorKind::Protocol,
                                "the adapter stream ended without a terminal event",
                            )));
                            return;
                        };

                        match event {
                            StreamEvent::Done(mut message) => {
                                apply_cost(&model, &mut message.usage);
                                yield StreamEvent::Done(message);
                                return;
                            }
                            StreamEvent::Failed(mut message) => {
                                if let Some(managed) = &managed {
                                    if !produced && !auth_retried && message.error.as_ref().is_some_and(|e| e.status == Some(401) && e.kind == ErrorKind::Auth) {
                                        auth_retried = true; refresh_auth = true;
                                        continue 'requests;
                                    }
                                    if let Some(error) = message.error.as_mut() { managed.failed(error); }
                                }
                                // Also after content: the consumer drops this attempt's deltas
                                // when it sees `Retrying`.
                                if message.error.as_ref().is_some_and(|e| e.kind.is_retryable())
                                    && retry.allows(attempt, *first_failure.get_or_insert_with(Instant::now))
                                    && let Some(error) = message.error.take()
                                {
                                    break 'attempt *error;
                                }
                                if message.error.as_ref().is_some_and(|e| e.is_missing_model()) {
                                    missing.lock().insert(model.provider.clone());
                                }
                                message.error = message.error.take().map(|e| Box::new(gave_up(*e, attempt, first_failure)));
                                apply_cost(&model, &mut message.usage);
                                yield StreamEvent::Failed(message);
                                return;
                            }
                            StreamEvent::Retrying { .. } => {}
                            other => {
                                produced |= other.is_content();
                                yield other;
                            }
                        }
                    }
                };
                managed = None;
                let since = first_failure.unwrap_or_else(Instant::now);
                match retry.delay(attempt + 1, error.retry_after_ms, retry.window.saturating_sub(since.elapsed())) {
                    Ok(delay) => {
                        attempt += 1;
                        tracing::info!(attempt, ?delay, kind = %error.kind, "retrying model request");
                        yield StreamEvent::Retrying { attempt, delay, error };
                        tokio::time::sleep(delay).await;
                    }
                    Err(requested) => {
                        let mut error = error;
                        error.message = format!(
                            "{} (the provider asked to retry after {}s)",
                            error.message,
                            requested.as_secs()
                        );
                        yield StreamEvent::Failed(base.fail(gave_up(error, attempt, first_failure)));
                        return;
                    }
                }
            }
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::api::scripted::Script;
    use crate::api::scripted::ScriptedApi;
    use crate::types::Message;
    use crate::types::StopReason;
    use crate::types::UserMessage;

    fn registry(api: Arc<ScriptedApi>) -> ModelRegistry {
        let options = RegistryOptions {
            retry: RetryPolicy {
                base_delay: Duration::from_millis(1),
                max_delay: Duration::from_millis(5),
                ..Default::default()
            },
            ..Default::default()
        };
        let mut registry = ModelRegistry::new(Catalog::default(), options);
        registry.register_api(api);
        registry.insert_provider(ScriptedApi::provider());
        registry
    }

    fn context() -> Context {
        Context {
            system: String::new(),
            messages: vec![Message::from(UserMessage::text("hi"))],
            tools: vec![],
        }
    }

    #[tokio::test]
    async fn retries_before_content_then_succeeds() {
        let api = ScriptedApi::new();
        api.push(Script::fail(ErrorKind::RateLimited, "slow down"));
        api.push(Script::text("hello"));
        let registry = registry(api.clone());
        let model = registry.resolve("test/scripted").unwrap().model;
        let events: Vec<StreamEvent> = registry
            .stream(model, context(), RequestOptions::default())
            .collect()
            .await;
        assert!(matches!(
            events[0],
            StreamEvent::Retrying { attempt: 1, .. }
        ));
        match events.last().unwrap() {
            StreamEvent::Done(msg) => {
                assert_eq!(msg.text(), "hello");
                assert_eq!(msg.stop_reason, StopReason::Stop);
            }
            other => panic!("unexpected {other:?}"),
        }
        assert_eq!(api.requests().len(), 2);
    }

    #[tokio::test]
    async fn does_not_retry_non_retryable_errors() {
        let api = ScriptedApi::new();
        api.push(Script::fail(ErrorKind::Auth, "bad key"));
        let registry = registry(api.clone());
        let model = registry.resolve("test/scripted").unwrap().model;
        let events: Vec<StreamEvent> = registry
            .stream(model, context(), RequestOptions::default())
            .collect()
            .await;
        assert_eq!(events.len(), 1);
        match &events[0] {
            StreamEvent::Failed(msg) => {
                assert_eq!(msg.error.as_ref().unwrap().kind, ErrorKind::Auth)
            }
            other => panic!("unexpected {other:?}"),
        }
    }

    #[tokio::test]
    async fn retries_after_content_and_the_new_attempt_replaces_it() {
        let script = Script {
            steps: vec![
                crate::api::scripted::ScriptStep::Text("partial".into()),
                crate::api::scripted::ScriptStep::Fail(ErrorKind::Network, "reset".into()),
            ],
            ..Default::default()
        };
        let api = ScriptedApi::new();
        api.push(script);
        api.push(Script::text("whole"));
        let registry = registry(api.clone());
        let model = registry.resolve("test/scripted").unwrap().model;
        let events: Vec<StreamEvent> = registry
            .stream(model, context(), RequestOptions::default())
            .collect()
            .await;
        assert!(matches!(&events[0], StreamEvent::TextDelta { text, .. } if text == "partial"));
        assert!(matches!(events[1], StreamEvent::Retrying { attempt: 1, .. }));
        match events.last().unwrap() {
            StreamEvent::Done(msg) => assert_eq!(msg.text(), "whole"),
            other => panic!("unexpected {other:?}"),
        }
        assert_eq!(api.requests().len(), 2);
    }

    fn failing(api: &ScriptedApi, times: usize) {
        for _ in 0..times {
            api.push(Script::fail(ErrorKind::Network, "unreachable"));
        }
    }

    #[tokio::test]
    async fn retries_until_the_window_ends() {
        let api = ScriptedApi::new();
        failing(&api, 1000);
        let mut registry = ModelRegistry::new(
            Catalog::default(),
            RegistryOptions {
                retry: RetryPolicy {
                    window: Duration::from_millis(40),
                    base_delay: Duration::from_millis(1),
                    max_delay: Duration::from_millis(5),
                    ..Default::default()
                },
                ..Default::default()
            },
        );
        registry.register_api(api.clone());
        registry.insert_provider(ScriptedApi::provider());
        let model = registry.resolve("test/scripted").unwrap().model;
        let started = std::time::Instant::now();
        let events: Vec<StreamEvent> = registry
            .stream(model, context(), RequestOptions::default())
            .collect()
            .await;
        assert!(started.elapsed() >= Duration::from_millis(40));
        match events.last().unwrap() {
            StreamEvent::Failed(msg) => {
                let error = msg.error.as_ref().unwrap();
                assert_eq!(error.kind, ErrorKind::Network);
                assert!(error.message.contains("gave up after"), "{}", error.message);
            }
            other => panic!("unexpected {other:?}"),
        }
        assert!(api.requests().len() > 2);
    }

    #[tokio::test]
    async fn max_retries_caps_the_count() {
        let api = ScriptedApi::new();
        failing(&api, 10);
        let mut registry = ModelRegistry::new(
            Catalog::default(),
            RegistryOptions {
                retry: RetryPolicy {
                    max_retries: Some(2),
                    base_delay: Duration::from_millis(1),
                    max_delay: Duration::from_millis(5),
                    ..Default::default()
                },
                ..Default::default()
            },
        );
        registry.register_api(api.clone());
        registry.insert_provider(ScriptedApi::provider());
        let model = registry.resolve("test/scripted").unwrap().model;
        let events: Vec<StreamEvent> = registry
            .stream(model, context(), RequestOptions::default())
            .collect()
            .await;
        match events.last().unwrap() {
            StreamEvent::Failed(msg) => {
                assert!(msg.error.as_ref().unwrap().message.contains("gave up after 2 retries"))
            }
            other => panic!("unexpected {other:?}"),
        }
        assert_eq!(api.requests().len(), 3);
    }

    #[test]
    fn delays_back_off_and_end_with_the_window() {
        let policy = RetryPolicy::default();
        let first = policy.delay(1, None, Duration::from_secs(600)).unwrap();
        assert!(first >= Duration::from_millis(400) && first <= Duration::from_millis(600));
        let late = policy.delay(20, None, Duration::from_secs(600)).unwrap();
        assert!(late >= Duration::from_secs(24) && late <= Duration::from_secs(36));
        assert_eq!(policy.delay(20, None, Duration::from_secs(2)).unwrap(), Duration::from_secs(2));
        assert_eq!(policy.delay(1, Some(90_000), Duration::from_secs(600)), Ok(Duration::from_secs(90)));
        assert_eq!(policy.delay(1, Some(90_000), Duration::from_secs(60)), Err(Duration::from_secs(90)));
    }

    #[test]
    fn resolves_known_and_ad_hoc_models() {
        let registry = ModelRegistry::new(Catalog::builtin().unwrap(), RegistryOptions::default());
        let known = registry.resolve("deepseek/deepseek-flash").unwrap();
        assert!(!known.ad_hoc);
        let ad_hoc = registry.resolve("openrouter/anthropic/claude-x").unwrap();
        assert!(ad_hoc.ad_hoc);
        assert_eq!(ad_hoc.model.id, "anthropic/claude-x");
        assert!(matches!(
            registry.resolve("nope/x"),
            Err(ResolveError::UnknownProvider { .. })
        ));
        assert!(matches!(
            registry.resolve("deepseek"),
            Err(ResolveError::BadReference(_))
        ));
        assert!(
            registry
                .key_env_vars()
                .contains(&"DEEPSEEK_API_KEY".to_string())
        );
    }
}
