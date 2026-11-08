//! Model connection management hosted by the existing kernel process.
//! No child service, heartbeat, or periodic token refresh. ChatGPT sign-in alone opens
//! a temporary loopback callback listener while browser authorization is pending.
mod adapter;
pub mod chatgpt;
pub mod platform;
mod qoder;
mod qwenwork;
use adapter::ProductAdapter;
use async_trait::async_trait;
use her_llm::connection::{Authorization, ConnectionAuthorizer, ConnectionRequest};
use her_llm::{AuthConfig, Compat, LlmError, Modality, ModelRegistry, ProviderConfig};
use her_protocol::api::{
    ConnectionConnectParams, ConnectionDiscovery, ConnectionModel, ConnectionState, ConnectionView,
    ConnectionsSnapshot,
};
use parking_lot::RwLock;
use platform::Environment;
#[cfg(test)]
use qwenwork::QwenWork;
use serde::{Deserialize, Serialize};
use std::{
    collections::BTreeMap,
    path::{Path, PathBuf},
    sync::{
        Arc, Weak,
        atomic::{AtomicU64, Ordering},
    },
};
use tokio::{
    io::AsyncWriteExt,
    sync::{Mutex, watch},
};
use tokio_util::sync::CancellationToken;

pub type Result<T> = std::result::Result<T, ConnectionError>;
mod diagnostic;
pub use diagnostic::ConnectionError;
pub(crate) fn valid_id(id: &str) -> bool {
    !id.is_empty()
        && id.len() <= 100
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
}

/// What an account's own catalog declares about a model. It speaks for this channel, so it comes
/// first; what it leaves out comes from the model's maker in the public catalog, by its id.
pub(crate) fn declared_model(m: &ConnectionModel) -> her_llm::catalog::ModelToml {
    her_llm::catalog::ModelToml {
        name: Some(m.name.clone()),
        names: m.names.clone(),
        context_window: m.context_window.map(|window| window.clamp(1024, 2_000_000)),
        max_output_tokens: m.max_output_tokens.map(|limit| limit.max(1)),
        reasoning: Some(m.reasoning),
        input: Some(if m.images {
            vec![Modality::Text, Modality::Image]
        } else {
            vec![Modality::Text]
        }),
        thinking: Some(m.thinking.clone()),
        default_thinking: Some(m.default_thinking),
        ..her_llm::catalog::ModelToml::new(&m.id)
    }
}

#[derive(Clone, Serialize, Deserialize)]
struct Record {
    connection_id: String,
    /// Empty in records written before it existed; they keep `local-<connection_id>`.
    #[serde(default)]
    provider_id: String,
    adapter_id: String,
    identity_key: String,
    enabled: bool,
    idempotency_key: String,
    #[serde(default)]
    models: Vec<ConnectionModel>,
    /// When `models` was last taken from the source (RFC 3339).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    models_fetched_at: Option<String>,
}
#[derive(Serialize, Deserialize)]
struct Config {
    schema: u32,
    connections: Vec<Record>,
}
struct Entry {
    record: RwLock<Record>,
    view: RwLock<ConnectionView>,
    adapter: Mutex<Box<dyn ProductAdapter>>,
    /// Requests running on it; disconnecting waits for none.
    requests: watch::Sender<usize>,
    /// Explicit revalidation invalidates diagnostics from older in-flight requests.
    generation: AtomicU64,
    cancel: RwLock<CancellationToken>,
}
type Changed = Arc<dyn Fn() + Send + Sync>;
pub struct Manager {
    home: PathBuf,
    registry: Weak<ModelRegistry>,
    environments: BTreeMap<String, Arc<dyn Environment>>,
    entries: RwLock<BTreeMap<String, Arc<Entry>>>,
    revision: AtomicU64,
    mutations: Mutex<()>,
    changed: RwLock<Option<Changed>>,
    myself: Weak<Manager>,
    load_error: Option<ConnectionError>,
    pub chatgpt: Arc<chatgpt::ChatGpt>,
}
impl Manager {
    /// Loads only non-secret configuration. No network, Keychain, timers, or spawned processes.
    pub fn new(home: PathBuf, registry: &Arc<ModelRegistry>) -> Arc<Self> {
        // A public build does not borrow other apps' sign-ins (standalone-cli-design §18.3).
        let environments = if cfg!(feature = "local-accounts") {
            BTreeMap::from([
                ("qwenwork".into(), platform::system(&home)),
                ("qoder".into(), platform::system_for(&home, "qoder")),
            ])
        } else {
            BTreeMap::new()
        };
        Self::with_environments(home, registry, environments)
    }
    pub fn with_environment(
        home: PathBuf,
        registry: &Arc<ModelRegistry>,
        environment: Arc<dyn Environment>,
    ) -> Arc<Self> {
        Self::with_environments(
            home,
            registry,
            BTreeMap::from([("qwenwork".into(), environment)]),
        )
    }
    pub fn with_environments(
        home: PathBuf,
        registry: &Arc<ModelRegistry>,
        environments: BTreeMap<String, Arc<dyn Environment>>,
    ) -> Arc<Self> {
        let loaded = read_config(&home);
        let (records, load_error) = match loaded {
            Ok(v) => (v, None),
            Err(e) => (vec![], Some(e)),
        };
        let chatgpt = chatgpt::ChatGpt::new(&home, registry);
        let result = Arc::new_cyclic(|myself| Self {
            home,
            registry: Arc::downgrade(registry),
            environments,
            entries: RwLock::new(BTreeMap::new()),
            revision: AtomicU64::new(1),
            mutations: Mutex::new(()),
            changed: RwLock::new(None),
            myself: myself.clone(),
            load_error,
            chatgpt,
        });
        for r in records {
            if result.environments.contains_key(&r.adapter_id) {
                result.add(r);
            }
        }
        registry.install_authorizer(result.clone());
        result.publish();
        let _ = result.chatgpt.sync();
        result
    }
    pub fn on_change(&self, callback: Changed) {
        let weak = self.myself.clone();
        let notify = callback.clone();
        self.chatgpt.on_change(Arc::new(move || {
            if let Some(manager) = weak.upgrade() {
                manager.revision.fetch_add(1, Ordering::AcqRel);
            }
            notify();
        }));
        *self.changed.write() = Some(callback);
    }
    fn check(&self) -> Result<()> {
        self.load_error.clone().map_or(Ok(()), Err)
    }
    pub fn snapshot(&self) -> Result<ConnectionsSnapshot> {
        self.check()?;
        let mut connections: Vec<_> = self
            .entries
            .read()
            .values()
            .map(|e| e.view.read().clone())
            .collect();
        if let Some(view) = self.chatgpt.sync()? {
            connections.push(view);
        }
        Ok(ConnectionsSnapshot {
            revision: self.revision.load(Ordering::Acquire),
            connections,
        })
    }
    pub async fn discover(&self) -> Result<Vec<ConnectionDiscovery>> {
        let mut discoveries = Vec::new();
        for (id, env) in &self.environments {
            discoveries.push(diagnostic::operation("discover", id, "", true, async {
                let discovery = env.discover().await?;
                tracing::info!(installed = discovery.installed, version = ?discovery.version, "connection_source_detected");
                Ok(discovery)
            }).await.unwrap_or_else(|error| ConnectionDiscovery {
                diagnostic: Some(error.diagnostic()),
                adapter_id: id.clone(),
                installed: false,
                version: None,
                login: "unavailable".into(),
                discovery_id: format!("{id}-standard"),
            }));
        }
        Ok(discoveries)
    }
    fn add(&self, r: Record) -> Arc<Entry> {
        let view = ConnectionView {
            connection_id: r.connection_id.clone(),
            provider_id: r.provider_id.clone(),
            adapter_id: r.adapter_id.clone(),
            display_name: adapter::name(&r.adapter_id).into(),
            state: if !r.enabled {
                ConnectionState::Disabled
            } else if r.models.is_empty() {
                ConnectionState::Unavailable
            } else {
                ConnectionState::Ready
            },
            reason_code: None,
            diagnostic: None,
            models: r.models.clone(),
            models_fetched_at: r.models_fetched_at.clone(),
        };
        let e = Arc::new(Entry {
            adapter: Mutex::new(adapter::create(
                &r.adapter_id,
                r.connection_id.clone(),
                r.identity_key.clone(),
                self.environments[&r.adapter_id].clone(),
            )),
            record: RwLock::new(r),
            view: RwLock::new(view),
            requests: watch::Sender::new(0),
            generation: AtomicU64::new(0),
            cancel: RwLock::new(CancellationToken::new()),
        });
        self.entries
            .write()
            .insert(e.record.read().connection_id.clone(), e.clone());
        e
    }
    fn entry(&self, id: &str) -> Result<Arc<Entry>> {
        self.entries
            .read()
            .get(id)
            .cloned()
            .ok_or_else(|| ConnectionError::new("not_found", "连接不存在"))
    }
    fn http(&self) -> Result<reqwest::Client> {
        self.registry
            .upgrade()
            .ok_or_else(ConnectionError::unavailable)?
            .http_client()
            .map_err(|_| ConnectionError::unavailable())
    }
    fn publish(&self) {
        let providers = self
            .entries
            .read()
            .values()
            .filter_map(|e| {
                let record = e.record.read();
                let view = e.view.read();
                if !record.enabled || !matches!(view.state, ConnectionState::Ready) {
                    return None;
                }
                let entries = view
                    .models
                    .iter()
                    .filter(|m| adapter::supported(&record.adapter_id, &m.id))
                    .map(declared_model)
                    .collect();
                Some(ProviderConfig {
                    plan: None,
                    id: record.provider_id.clone(),
                    name: view.display_name.clone(),
                    names: adapter::names(&record.adapter_id),
                    api: "openai-chat".into(),
                    base_url: adapter::base(&record.adapter_id).into(),
                    auth: AuthConfig::Managed,
                    headers: BTreeMap::new(),
                    currency: Default::default(),
                    compat: Compat {
                        thinking_format: adapter::thinking_format(&record.adapter_id),
                        ..Compat::default()
                    },
                    listing: her_llm::catalog::Listing {
                        entries,
                        ..Default::default()
                    },
                })
            })
            .collect();
        if let Some(registry) = self.registry.upgrade() {
            let _ = registry.replace_source("model-connections", providers);
        }
        self.revision.fetch_add(1, Ordering::AcqRel);
        if let Some(callback) = self.changed.read().clone() {
            callback();
        }
    }
    fn failed(&self, e: &Entry, error: &ConnectionError) {
        self.failed_for(e, error, None);
    }
    fn failed_for(&self, e: &Entry, error: &ConnectionError, generation: Option<u64>) {
        // A draining request must not overwrite the user's explicit disconnect.
        let record = e.record.read();
        if !record.enabled && matches!(e.view.read().state, ConnectionState::Disabled) {
            if error.code == "identity_changed" {
                e.cancel.read().cancel();
            }
            return;
        }
        {
            let mut view = e.view.write();
            if generation.is_some_and(|g| g != e.generation.load(Ordering::Acquire)) {
                tracing::info!(adapter = %record.adapter_id, connection_id = %record.connection_id,
                    generation, current_generation = e.generation.load(Ordering::Acquire), "connection_stale_failure_ignored");
                return;
            }
            view.state = match error.code.as_str() {
                "connection_login_required" | "identity_changed" => ConnectionState::NeedsLogin,
                "connection_permission_required" | "permission_cancelled" => {
                    ConnectionState::PermissionRequired
                }
                c if c.starts_with("unsupported") => ConnectionState::Unsupported,
                _ if error.retryable && record.enabled && !view.models.is_empty() => {
                    ConnectionState::Ready
                }
                _ => ConnectionState::Unavailable,
            };
            tracing::warn!(adapter = %record.adapter_id, connection_id = %record.connection_id,
                state = ?view.state, stage = %error.stage, code = %error.code, "connection_state_changed");
            view.reason_code = Some(error.code.clone());
            view.diagnostic = Some(error.diagnostic());
        }
        drop(record);
        if error.code == "identity_changed" {
            e.cancel.read().cancel();
        }
        self.publish();
    }
    async fn persist(&self) -> Result<()> {
        let connections = self
            .entries
            .read()
            .values()
            .map(|e| e.record.read().clone())
            .collect();
        let data = serde_json::to_vec(&Config {
            schema: 1,
            connections,
        })
        .map_err(|_| {
            ConnectionError::new(
                "connection_storage_failed",
                "无法保存连接配置，请检查本地目录权限和磁盘空间",
            )
            .at("credential_store")
        })?;
        let dir = self.home.join("connections");
        tokio::fs::create_dir_all(&dir).await.map_err(|_| {
            ConnectionError::new(
                "connection_storage_failed",
                "无法保存连接配置，请检查本地目录权限和磁盘空间",
            )
            .at("credential_store")
        })?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            tokio::fs::set_permissions(&dir, std::fs::Permissions::from_mode(0o700))
                .await
                .map_err(|_| {
                    ConnectionError::new(
                        "connection_storage_failed",
                        "无法保存连接配置，请检查本地目录权限和磁盘空间",
                    )
                    .at("credential_store")
                })?;
        }
        let temporary = dir.join("config.rust.tmp");
        let mut options = tokio::fs::OpenOptions::new();
        options.write(true).create(true).truncate(true);
        #[cfg(unix)]
        options.mode(0o600);
        let mut file = options.open(&temporary).await.map_err(|_| {
            ConnectionError::new(
                "connection_storage_failed",
                "无法保存连接配置，请检查本地目录权限和磁盘空间",
            )
            .at("credential_store")
        })?;
        file.write_all(&data).await.map_err(|_| {
            ConnectionError::new(
                "connection_storage_failed",
                "无法保存连接配置，请检查本地目录权限和磁盘空间",
            )
            .at("credential_store")
        })?;
        file.sync_all().await.map_err(|_| {
            ConnectionError::new(
                "connection_storage_failed",
                "无法保存连接配置，请检查本地目录权限和磁盘空间",
            )
            .at("credential_store")
        })?;
        tokio::fs::rename(&temporary, dir.join("config.json"))
            .await
            .map_err(|_| {
                ConnectionError::new(
                    "connection_storage_failed",
                    "无法保存连接配置，请检查本地目录权限和磁盘空间",
                )
                .at("credential_store")
            })
    }
    /// Asks ready connections whose model catalog is older than `max_age` for it again
    /// (model-catalog-design §14). Quiet: a failure keeps the old catalog and the connection's
    /// state. Returns whether any connection's models changed.
    pub async fn refresh_stale_catalogs(&self, max_age: std::time::Duration) -> bool {
        let due: Vec<Arc<Entry>> = self
            .entries
            .read()
            .values()
            .filter(|e| {
                let record = e.record.read();
                record.enabled
                    && matches!(e.view.read().state, ConnectionState::Ready)
                    && record
                        .models_fetched_at
                        .as_deref()
                        .and_then(her_llm::discovery::age_secs)
                        .is_none_or(|age| age >= max_age.as_secs() as i64)
            })
            .cloned()
            .collect();
        let mut changed = false;
        for e in due {
            let Ok(http) = self.http() else {
                return changed;
            };
            let (adapter_id, connection_id) = {
                let r = e.record.read();
                (r.adapter_id.clone(), r.connection_id.clone())
            };
            let fetched = diagnostic::operation(
                "refresh_catalog",
                &adapter_id,
                &connection_id,
                false,
                async { e.adapter.lock().await.catalog(&http).await },
            )
            .await;
            let Ok(models) = fetched else { continue };
            let same = serde_json::to_value(&e.record.read().models).ok()
                == serde_json::to_value(&models).ok();
            {
                let mut record = e.record.write();
                record.models_fetched_at = Some(her_llm::discovery::now());
                if !same {
                    record.models = models.clone();
                }
            }
            let mut view = e.view.write();
            view.models_fetched_at = e.record.read().models_fetched_at.clone();
            if !same {
                view.models = models;
                changed = true;
            }
        }
        if changed {
            let _ = self.persist().await;
            self.publish();
        }
        changed
    }

    async fn prepare(&self, e: &Arc<Entry>) -> Result<()> {
        {
            let mut view = e.view.write();
            e.generation.fetch_add(1, Ordering::AcqRel);
            view.state = ConnectionState::Connecting;
            view.reason_code = None;
            view.diagnostic = None;
        }
        self.publish();
        let result = async {
            let http = self.http()?;
            let mut adapter = e.adapter.lock().await;
            let models = diagnostic::stage("catalog", adapter.catalog(&http)).await?;
            tracing::info!(model_count = models.len(), "connection_catalog_ready");
            let _ = diagnostic::stage("authorization", adapter.authorize(&http, false)).await?;
            {
                let mut record = e.record.write();
                record.enabled = true;
                record.models = models.clone();
                record.models_fetched_at = Some(her_llm::discovery::now());
            }
            {
                let mut view = e.view.write();
                view.models = models;
                view.models_fetched_at = e.record.read().models_fetched_at.clone();
            }
            *e.cancel.write() = CancellationToken::new();
            diagnostic::stage("connection_storage", self.persist()).await?;
            e.view.write().state = ConnectionState::Ready;
            tracing::info!("connection_ready");
            self.publish();
            Ok(())
        }
        .await;
        if let Err(ref error) = result {
            self.failed(e, error);
        }
        result
    }
    pub async fn connect(&self, p: ConnectionConnectParams) -> Result<ConnectionsSnapshot> {
        let adapter = p.adapter_id.clone();
        diagnostic::operation("connect", &adapter, "", true, self.connect_inner(p)).await
    }
    async fn connect_inner(&self, p: ConnectionConnectParams) -> Result<ConnectionsSnapshot> {
        let _guard = self.mutations.lock().await;
        self.check()?;
        if !self.environments.contains_key(&p.adapter_id)
            || p.discovery_id != format!("{}-standard", p.adapter_id)
            || !valid_id(&p.idempotency_key)
        {
            return Err(ConnectionError::new("invalid_params", "连接参数无效"));
        }
        let existing = self
            .entries
            .read()
            .values()
            .find(|e| {
                let r = e.record.read();
                r.idempotency_key == p.idempotency_key && r.adapter_id == p.adapter_id
            })
            .cloned();
        if let Some(e) = existing {
            self.prepare(&e).await?;
            return self.snapshot();
        }
        let environment = &self.environments[&p.adapter_id];
        let discovery = diagnostic::stage("discovery", environment.discover()).await?;
        tracing::info!(installed = discovery.installed, version = ?discovery.version, "connection_source_detected");
        if !discovery.installed {
            return Err(
                ConnectionError::new("source_not_found", "未检测到来源应用").at("discovery")
            );
        }
        let _ = diagnostic::stage("app_metadata", environment.headers()).await?;
        let login = diagnostic::stage("source_login", environment.interactive_login()).await?;
        if self.entries.read().values().any(|e| {
            let r = e.record.read();
            r.adapter_id == p.adapter_id && r.identity_key == login.identity
        }) {
            return Err(ConnectionError::new("conflict", "这个账户已经连接"));
        }
        let e = self.add(Record {
            connection_id: uuid::Uuid::new_v4().to_string(),
            provider_id: provider_id(&p.adapter_id, &login.identity),
            adapter_id: p.adapter_id,
            identity_key: login.identity,
            enabled: true,
            idempotency_key: p.idempotency_key,
            models: vec![],
            models_fetched_at: None,
        });
        tracing::info!(connection_id = %e.record.read().connection_id, "connection_record_created");
        diagnostic::stage("connection_storage", self.persist()).await?;
        self.prepare(&e).await?;
        self.snapshot()
    }
    pub async fn retry(&self, id: &str) -> Result<ConnectionsSnapshot> {
        let adapter = self
            .entries
            .read()
            .get(id)
            .map(|e| e.record.read().adapter_id.clone())
            .unwrap_or_default();
        diagnostic::operation("retry", &adapter, id, true, self.retry_inner(id)).await
    }
    async fn retry_inner(&self, id: &str) -> Result<ConnectionsSnapshot> {
        if id == chatgpt::ID {
            self.chatgpt.refresh().await?;
            return self.snapshot();
        }
        let _guard = self.mutations.lock().await;
        self.check()?;
        let e = self.entry(id)?;
        let adapter_id = e.record.read().adapter_id.clone();
        {
            let _view = e.view.write();
            e.generation.fetch_add(1, Ordering::AcqRel);
        }
        if let Err(error) = diagnostic::stage(
            "source_login",
            self.environments[&adapter_id].interactive_login(),
        )
        .await
        {
            self.failed(&e, &error);
            return Err(error);
        }
        self.prepare(&e).await?;
        self.snapshot()
    }
    pub async fn disconnect(&self, id: &str) -> Result<ConnectionsSnapshot> {
        if id == chatgpt::ID {
            self.chatgpt.logout().await?;
            return self.snapshot();
        }
        let _guard = self.mutations.lock().await;
        self.check()?;
        let e = self.entry(id)?;
        e.record.write().enabled = false;
        e.view.write().state = ConnectionState::Disabled;
        self.publish();
        self.persist().await?;
        let _ = e
            .requests
            .subscribe()
            .wait_for(|running| *running == 0)
            .await;
        self.snapshot()
    }
    pub async fn remove(&self, id: &str) -> Result<ConnectionsSnapshot> {
        if id == chatgpt::ID {
            self.chatgpt.forget().await?;
            return self.snapshot();
        }
        let _guard = self.mutations.lock().await;
        self.check()?;
        let e = self.entry(id)?;
        if e.record.read().enabled || *e.requests.borrow() > 0 {
            return Err(ConnectionError::new(
                "conflict",
                "请先断开连接并等待任务结束",
            ));
        }
        let adapter_id = e.record.read().adapter_id.clone();
        self.environments[&adapter_id].delete(id).await?;
        self.entries.write().remove(id);
        self.persist().await?;
        self.publish();
        self.snapshot()
    }
}
fn read_config(home: &Path) -> Result<Vec<Record>> {
    let file = home.join("connections/config.json");
    let data = match std::fs::read(file) {
        Ok(d) => d,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(vec![]),
        Err(_) => {
            return Err(ConnectionError::new(
                "invalid_config",
                "无法读取模型连接配置",
            ));
        }
    };
    if data.len() > 1024 * 1024 {
        return Err(ConnectionError::new("invalid_config", "模型连接配置过大"));
    }
    let config: Config = serde_json::from_slice(&data)
        .map_err(|_| ConnectionError::new("invalid_config", "模型连接配置格式无效"))?;
    let mut ids = std::collections::HashSet::new();
    if config.schema != 1
        || config.connections.iter().any(|r| {
            uuid::Uuid::parse_str(&r.connection_id).is_err()
                || !ids.insert(r.connection_id.clone())
                || !["qwenwork", "qoder"].contains(&r.adapter_id.as_str())
                || r.models.iter().any(|m| !valid_id(&m.id))
        })
    {
        return Err(ConnectionError::new("invalid_config", "模型连接配置不兼容"));
    }
    let mut connections = config.connections;
    for r in &mut connections {
        if r.provider_id.is_empty() {
            r.provider_id = format!("local-{}", r.connection_id);
        }
    }
    let mut providers = std::collections::HashSet::new();
    if connections
        .iter()
        .any(|r| !valid_id(&r.provider_id) || !providers.insert(r.provider_id.clone()))
    {
        return Err(ConnectionError::new("invalid_config", "模型连接配置不兼容"));
    }
    Ok(connections)
}
/// Derived from the application and the account, which a connection never changes: removing a
/// connection and making it again publishes the same provider, so sessions that used its models
/// still find them.
fn provider_id(adapter_id: &str, identity: &str) -> String {
    let hash = platform::digest(format!("{adapter_id}\0{identity}").as_bytes());
    format!("local-{adapter_id}-{}", &hash[..12])
}
struct RequestLease {
    entry: Arc<Entry>,
    generation: u64,
    manager: Weak<Manager>,
    http: reqwest::Client,
    _running: Running,
    cancellation: CancellationToken,
}
/// Counts a request on its connection while it lives.
struct Running(Arc<Entry>);
impl Running {
    fn start(e: &Arc<Entry>) -> Self {
        e.requests.send_modify(|running| *running += 1);
        Self(e.clone())
    }
}
impl Drop for Running {
    fn drop(&mut self) {
        self.0.requests.send_modify(|running| *running -= 1);
    }
}
#[async_trait]
impl ConnectionAuthorizer for Manager {
    async fn begin(
        &self,
        provider: &str,
        model: &str,
        http: reqwest::Client,
    ) -> std::result::Result<Box<dyn ConnectionRequest>, LlmError> {
        if provider.starts_with("local-chatgpt-api-") {
            return self.chatgpt.begin(provider, model).await;
        }
        self.check().map_err(|e| e.llm())?;
        let e = self
            .entries
            .read()
            .values()
            .find(|e| e.record.read().provider_id == provider)
            .cloned()
            .ok_or_else(|| ConnectionError::new("not_found", "连接不存在").llm())?;
        // Counted before the checks, so that a disconnect either refuses it or waits for it.
        let running = Running::start(&e);
        if !e.record.read().enabled || !matches!(e.view.read().state, ConnectionState::Ready) {
            return Err(ConnectionError::new("connection_disabled", "连接当前不可用").llm());
        }
        if !adapter::supported(&e.record.read().adapter_id, model)
            || !e.view.read().models.iter().any(|m| m.id == model)
        {
            return Err(ConnectionError::new("unsupported_capability", "模型不在当前目录中").llm());
        }
        let cancellation = e.cancel.read().clone();
        Ok(Box::new(RequestLease {
            generation: e.generation.load(Ordering::Acquire),
            entry: e,
            manager: self.myself.clone(),
            http,
            _running: running,
            cancellation,
        }))
    }
}
#[async_trait]
impl ConnectionRequest for RequestLease {
    async fn authorize(&self, rejected: bool) -> std::result::Result<Authorization, LlmError> {
        let (adapter_id, connection_id) = {
            let r = self.entry.record.read();
            (r.adapter_id.clone(), r.connection_id.clone())
        };
        let result =
            diagnostic::operation("authorize", &adapter_id, &connection_id, false, async {
                if rejected {
                    tracing::info!("connection_reauthorize_after_rejection");
                }
                self.entry
                    .adapter
                    .lock()
                    .await
                    .authorize(&self.http, rejected)
                    .await
            })
            .await;
        if let Err(ref error) = result
            && let Some(manager) = self.manager.upgrade()
        {
            manager.failed_for(&self.entry, error, Some(self.generation));
        }
        if result.is_ok() {
            let recovered = {
                let mut view = self.entry.view.write();
                if self.generation == self.entry.generation.load(Ordering::Acquire)
                    && matches!(view.state, ConnectionState::Ready)
                    && view.diagnostic.as_ref().is_some_and(|d| {
                        d.retryable
                            && matches!(
                                d.stage.as_str(),
                                "authorization" | "refresh" | "connection"
                            )
                    })
                {
                    view.reason_code = None;
                    view.diagnostic = None;
                    true
                } else {
                    false
                }
            };
            if recovered && let Some(manager) = self.manager.upgrade() {
                tracing::info!(adapter = %adapter_id, connection_id = %connection_id, "connection_recovered");
                manager.publish();
            }
        }
        result.map_err(|e| e.llm())
    }
    fn cancellation(&self) -> CancellationToken {
        self.cancellation.clone()
    }
    fn failed(&self, error: &mut LlmError) {
        let adapter_id = self.entry.record.read().adapter_id.clone();
        if adapter::authentication_failure(&adapter_id, error) {
            if let Some(manager) = self.manager.upgrade() {
                let mut failure = ConnectionError::login().at("inference");
                failure.http_status = error.status;
                tracing::warn!(adapter = %adapter_id, connection_id = %self.entry.record.read().connection_id,
                    stage = "inference", http_status = ?failure.http_status, code = %failure.code, "connection_failed");
                manager.failed_for(&self.entry, &failure, Some(self.generation));
            }
            error.message = ConnectionError::login().into_message();
            return;
        }
        adapter::reclassify(&adapter_id, error);
        // The gateway's own words tell what went wrong; what may identify the account goes.
        error.message = adapter::scrub(&error.message);
        tracing::warn!(
            adapter = %adapter_id,
            connection_id = %self.entry.record.read().connection_id,
            stage = "inference",
            status = ?error.status,
            kind = %error.kind,
            code = ?error.provider_code,
            message = %error.message,
            "connection model request failed"
        );
    }
}

#[cfg(test)]
mod tests;
