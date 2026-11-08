//! Models for clients: the merged directory, model lists asked from providers, the menu
//! selection, checked keys, and the background refresh of lists and the public catalog
//! (docs/kernel/model-catalog-design.md §3, §5, §6, §8).

use crate::server::Server;
use her_llm::AuthConfig;
use her_llm::AuthStatus;
use her_llm::ErrorKind;
use her_llm::ProviderConfig;
use her_llm::discovery::ListSource;
use her_llm::discovery::ModelList;
use her_protocol::api::AuthSetResult;
use her_protocol::api::ModelsListResult;
use her_protocol::api::ModelsRefreshParams;
use her_protocol::api::ModelsRefreshResult;
use her_protocol::api::error_code;
use her_rpc::RpcError;
use futures::StreamExt;
use parking_lot::Mutex;
use std::collections::BTreeSet;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::Ordering;
use std::time::Duration;

/// Lists and the catalog are asked again after a day.
const MAX_AGE_SECS: i64 = 24 * 60 * 60;
/// A failed list refresh is retried after this long.
const RETRY_AFTER_SECS: i64 = 10 * 60;
/// Local servers (no key) are asked again before a listing after this long.
const LOCAL_MAX_AGE_SECS: i64 = 60;
const LOCAL_WAIT: Duration = Duration::from_millis(300);
/// How often the background refresh wakes up to look.
const UPKEEP_TICK: Duration = Duration::from_secs(60);
const PARALLEL_REFRESHES: usize = 4;
/// How often the service looks whether another process changed the saved keys: one `stat`.
const KEY_WATCH_TICK: Duration = Duration::from_secs(2);

/// Background refresh state.
pub(crate) struct Upkeep {
    network: bool,
    started: AtomicBool,
    watching: AtomicBool,
    /// Providers being refreshed now.
    refreshing: Mutex<BTreeSet<String>>,
    /// Providers to refresh at the next wake-up (a new key, a failed check).
    stale: Mutex<BTreeSet<String>>,
    wake: tokio::sync::Notify,
}

impl Upkeep {
    pub(crate) fn new(network: bool) -> Self {
        Self {
            network,
            started: AtomicBool::new(false),
            watching: AtomicBool::new(false),
            refreshing: Mutex::new(BTreeSet::new()),
            stale: Mutex::new(BTreeSet::new()),
            wake: tokio::sync::Notify::new(),
        }
    }
}

/// The key a provider would use now, when it can be read without running a command.
fn current_key(provider: &ProviderConfig) -> Option<String> {
    let AuthConfig::ApiKey { env, value } = &provider.auth else {
        return None;
    };
    if let Some(v) = value.as_ref().filter(|v| !v.is_empty()) {
        return Some(v.clone());
    }
    for name in env {
        if let Ok(v) = std::env::var(name)
            && !v.trim().is_empty()
        {
            return Some(v.trim().to_string());
        }
    }
    her_llm::credentials::get(&provider.id)
}

fn age_over(time: Option<&str>, secs: i64) -> bool {
    time.and_then(her_llm::discovery::age_secs).is_none_or(|age| age > secs)
}

impl Server {
    /// Models, providers with where their keys and models come from, and the defaults of new
    /// sessions. May read saved keys (the keychain), so it blocks.
    pub fn list_models(&self) -> ModelsListResult {
        let _ = self.0.connections.chatgpt.sync();
        let llm = &self.0.llm;
        let refreshing = self.0.model_upkeep.refreshing.lock().clone();
        let result = her_core::models::list(llm, &self.0.defaults, &|id| refreshing.contains(id));
        for provider in llm.providers() {
            if !matches!(provider.auth.status(&provider.id), AuthStatus::Missing(_)) {
                self.note_key_change(&provider, llm.directory().list(&provider.id).as_ref());
            }
        }
        result
    }

    /// A key that differs from the one the list was fetched with (or a list never fetched)
    /// gets the provider refreshed in the background.
    fn note_key_change(&self, provider: &ProviderConfig, list: Option<&ModelList>) {
        let upkeep = &self.0.model_upkeep;
        if !upkeep.network
            || !upkeep.started.load(Ordering::Relaxed)
            || provider.listing.path.is_none()
            || !matches!(provider.auth, AuthConfig::ApiKey { .. })
        {
            return;
        }
        let Some(key) = current_key(provider) else { return };
        let fingerprint = her_llm::discovery::fingerprint(&key);
        if list.is_none_or(|l| l.credential.as_deref() != Some(fingerprint.as_str()))
            && upkeep.stale.lock().insert(provider.id.clone())
        {
            upkeep.wake.notify_one();
        }
    }

    /// Asks local servers (no key) again when their answer is older than a minute, waiting at
    /// most 300 ms: a stopped Ollama should not stay in the menu.
    pub async fn refresh_local_models(&self) {
        let llm = self.0.llm.clone();
        let local: Vec<String> = llm
            .providers()
            .filter(|p| matches!(p.auth, AuthConfig::None) && p.listing.path.is_some() && p.listing.from.is_none())
            .filter(|p| age_over(llm.directory().list(&p.id).as_ref().map(|l| l.checked_at.as_str()), LOCAL_MAX_AGE_SECS))
            .map(|p| p.id)
            .collect();
        if local.is_empty() {
            return;
        }
        let checks = local.iter().map(|id| tokio::time::timeout(LOCAL_WAIT, llm.refresh_list(id, None)));
        let results = futures::future::join_all(checks).await;
        if results.iter().any(|r| matches!(r, Ok(Ok(refresh)) if refresh.changed)) {
            self.models_changed();
        }
    }

    /// `models/refresh`: asks providers for their lists now; failures show in `listing.error`.
    pub async fn refresh_models(&self, params: ModelsRefreshParams) -> Result<ModelsRefreshResult, RpcError> {
        let llm = self.0.llm.clone();
        let targets: Vec<String> = match &params.provider {
            Some(id) => {
                let provider = llm
                    .provider(id)
                    .ok_or_else(|| RpcError::invalid_params(format!("unknown provider '{id}'")))?;
                if matches!(provider.auth, AuthConfig::Managed) {
                    return Err(RpcError::invalid_params(format!(
                        "provider '{id}' is an account connection; use connections/models"
                    )));
                }
                vec![provider.listing.from.clone().unwrap_or_else(|| id.clone())]
            }
            None => her_core::models::refreshable(&llm),
        };
        let mut changed = false;
        if params.catalog {
            match llm.refresh_catalog(&her_core::models::catalog_url()).await {
                Ok(c) => changed |= c,
                Err(e) => tracing::warn!("model catalog refresh failed: {}", e.message),
            }
        }
        changed |= self.refresh_lists(targets).await;
        if changed {
            self.models_changed();
        }
        let server = self.clone();
        let listing = tokio::task::spawn_blocking(move || server.list_models())
            .await
            .map_err(|e| RpcError::internal(e.to_string()))?;
        Ok(ModelsRefreshResult { providers: listing.providers })
    }

    /// Refreshes the given providers' lists, a few at a time. Returns whether any changed.
    async fn refresh_lists(&self, ids: Vec<String>) -> bool {
        let upkeep = &self.0.model_upkeep;
        let ids: Vec<String> = ids.into_iter().filter(|id| upkeep.refreshing.lock().insert(id.clone())).collect();
        if ids.is_empty() {
            return false;
        }
        self.models_changed();
        let llm = self.0.llm.clone();
        let results: Vec<(String, bool)> = futures::stream::iter(ids)
            .map(|id| {
                let llm = llm.clone();
                async move {
                    let changed = match llm.refresh_list(&id, None).await {
                        Ok(refresh) => refresh.changed,
                        Err(error) => {
                            tracing::info!(provider = %id, "model list refresh failed: {}", error.message);
                            true
                        }
                    };
                    (id, changed)
                }
            })
            .buffer_unordered(PARALLEL_REFRESHES)
            .collect()
            .await;
        let mut refreshing = upkeep.refreshing.lock();
        for (id, _) in &results {
            refreshing.remove(id);
        }
        drop(refreshing);
        // The `refreshing` flags changed either way.
        self.models_changed();
        results.iter().any(|(_, changed)| *changed)
    }

    /// `auth/set`: asks the provider for its list with the new key first. A refused key is not
    /// saved; a provider that cannot be asked does not stop the key from being saved.
    pub async fn set_key(&self, provider_id: &str, key: &str) -> Result<AuthSetResult, RpcError> {
        self.key_provider(provider_id)?;
        let llm = self.0.llm.clone();
        let provider = llm.provider(provider_id).ok_or_else(|| RpcError::invalid_params("unknown provider"))?;
        let target = provider.listing.from.clone().unwrap_or_else(|| provider_id.to_string());
        let asked = llm.provider(&target).is_some_and(|p| p.listing.path.is_some());
        let checked = if asked { Some(llm.refresh_list(&target, Some(key.to_string())).await) } else { None };
        let result = match checked {
            Some(Ok(refresh)) if refresh.source == ListSource::Provider => {
                AuthSetResult { verified: true, models: Some(refresh.models as u32) }
            }
            Some(Err(error)) if error.kind == ErrorKind::Auth => {
                return Err(RpcError {
                    code: error_code::KEY_REJECTED,
                    message: error.message,
                    data: Some(serde_json::json!({ "kind": "key_rejected" })),
                });
            }
            Some(Err(error)) => {
                tracing::info!(provider = %provider_id, "could not check the key: {}", error.message);
                let upkeep = &self.0.model_upkeep;
                upkeep.stale.lock().insert(target.clone());
                upkeep.wake.notify_one();
                AuthSetResult { verified: false, models: None }
            }
            _ => AuthSetResult { verified: false, models: None },
        };
        let (provider_id, key) = (provider_id.to_string(), key.to_string());
        tokio::task::spawn_blocking(move || her_llm::credentials::set(&provider_id, &key))
            .await
            .map_err(|e| RpcError::internal(e.to_string()))?
            .map_err(RpcError::internal)?;
        llm.directory().invalidate();
        self.models_changed();
        Ok(result)
    }

    /// Removes a provider's saved key (`auth/delete`). Blocking I/O.
    pub fn delete_key(&self, provider: &str) -> Result<(), RpcError> {
        self.key_provider(provider)?;
        her_llm::credentials::delete(provider).map_err(RpcError::internal)?;
        self.0.llm.directory().invalidate();
        self.models_changed();
        Ok(())
    }

    fn key_provider(&self, id: &str) -> Result<(), RpcError> {
        match self.0.llm.provider(id) {
            Some(p) if matches!(p.auth, AuthConfig::ApiKey { .. }) => Ok(()),
            Some(_) => Err(RpcError::invalid_params(format!("provider '{id}' does not use an API key"))),
            None => Err(RpcError::invalid_params(format!("unknown provider '{id}'"))),
        }
    }

    /// `models/show`: puts a model in the menu or takes it out. Blocking I/O.
    pub fn show_model(&self, reference: &str, shown: bool) -> Result<(), RpcError> {
        her_core::models::set_shown(&self.0.llm, self.0.defaults.model.as_deref(), reference, shown)
            .map_err(RpcError::invalid_params)?;
        self.models_changed();
        Ok(())
    }

    /// Starts the background refresh of the public catalog and the model lists (`her serve`).
    /// Nothing happens with `[models] network = false`.
    pub fn start_model_upkeep(&self) {
        let upkeep = &self.0.model_upkeep;
        let Ok(runtime) = tokio::runtime::Handle::try_current() else {
            return;
        };
        if upkeep.watching.swap(true, Ordering::Relaxed) {
            return;
        }
        // A key saved by `her login` in a terminal reaches this service's clients.
        let weak = std::sync::Arc::downgrade(&self.0);
        let shutdown = self.shutdown_token();
        runtime.spawn(async move {
            let mut seen = her_llm::credentials::changes_elsewhere();
            loop {
                tokio::select! {
                    _ = shutdown.cancelled() => return,
                    _ = tokio::time::sleep(KEY_WATCH_TICK) => {}
                }
                let Some(inner) = weak.upgrade() else { return };
                let now = her_llm::credentials::changes_elsewhere();
                if now != seen {
                    seen = now;
                    Server(inner).models_changed();
                }
            }
        });
        if !upkeep.network || upkeep.started.swap(true, Ordering::Relaxed) {
            return;
        }
        let weak = std::sync::Arc::downgrade(&self.0);
        let shutdown = self.shutdown_token();
        runtime.spawn(async move {
            loop {
                let Some(inner) = weak.upgrade() else { return };
                let server = Server(inner);
                server.upkeep_once().await;
                let wake = async { server.0.model_upkeep.wake.notified().await };
                tokio::select! {
                    _ = shutdown.cancelled() => return,
                    _ = tokio::time::sleep(UPKEEP_TICK) => {}
                    _ = wake => {}
                }
            }
        });
    }

    /// One look: refresh the catalog and every list that is a day old, failed a while ago,
    /// was fetched with another key, or named a model that turned out not to exist.
    async fn upkeep_once(&self) {
        let llm = self.0.llm.clone();
        let mut changed = false;
        if let Some(dir) = llm.directory().catalog_dir()
            && age_over(her_llm::public_catalog::read_state(&dir).checked_at.as_deref(), MAX_AGE_SECS)
        {
            match llm.refresh_catalog(&her_core::models::catalog_url()).await {
                Ok(c) => changed |= c,
                Err(e) => tracing::info!("model catalog refresh failed: {}", e.message),
            }
            // A failure is not retried before tomorrow.
            let mut state = her_llm::public_catalog::read_state(&dir);
            if age_over(state.checked_at.as_deref(), MAX_AGE_SECS) {
                state.checked_at = Some(her_llm::discovery::now());
                let _ = her_llm::discovery::write_json(&dir.join("state.json"), &state);
            }
        }
        let mut stale = std::mem::take(&mut *self.0.model_upkeep.stale.lock());
        stale.extend(llm.directory().take_missing());
        let due: Vec<String> = llm
            .providers()
            .filter(|p| {
                p.listing.path.is_some()
                    && p.listing.from.is_none()
                    && matches!(p.auth, AuthConfig::ApiKey { .. } | AuthConfig::Command { .. })
                    && !matches!(p.auth.status(&p.id), AuthStatus::Missing(_))
            })
            .filter(|p| {
                if stale.contains(&p.id) {
                    return true;
                }
                match llm.directory().list(&p.id) {
                    None => true,
                    Some(list) if list.error.is_some() => age_over(Some(&list.checked_at), RETRY_AFTER_SECS),
                    Some(list) => age_over(Some(&list.checked_at), MAX_AGE_SECS),
                }
            })
            .map(|p| p.id)
            .collect();
        changed |= self.refresh_lists(due).await;
        // Account connections' catalogs, on the same schedule; they publish their own changes.
        self.0.connections.refresh_stale_catalogs(Duration::from_secs(MAX_AGE_SECS as u64)).await;
        if changed {
            self.models_changed();
        }
    }
}
