//! Qoder global desktop protocol. Independent JobTokens; source login is read-only.
mod capabilities;
mod cosy;
mod legacy;
mod route;
mod sse;
#[cfg(test)]
mod tests;
mod transport;
use crate::{
    ConnectionError, Result,
    adapter::ProductAdapter,
    platform::{Environment, Login, digest},
};
use async_trait::async_trait;
use her_llm::{Credentials, connection::Authorization};
use her_protocol::api::ConnectionModel;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{sync::Arc, time::Duration};
const CLIENT_ID: &str = "732aef47-9cf2-46a2-95fe-4cebb5d0d1fa";
const OPENAPI: &str = "https://openapi.qoder.sh";
const CATALOG: &str = "https://api3.qoder.sh/algo/api/v2/model/list?Encode=1";
const MODEL_BASE: &str = "https://api2-v2.qoder.sh/model/v1";

// Models whose App HTTP route is verified with text and a complete tool roundtrip.
// Unknown catalog entries stay visible but cannot bypass this check via ad-hoc IDs.
pub(crate) const SUPPORTED: &[&str] = &[
    "auto",
    "ultimate",
    "fast",
    "efficient",
    "smodel",
    "cmodel",
    "qmodel_38max",
    "qfmodel",
    "gmodel",
    "gfmodel",
    "kmodel_latest",
    "kmodel",
    "dmodel",
    "dfmodel",
    "mmodel",
];
pub(crate) fn supports_model(model: &str) -> bool {
    SUPPORTED.contains(&model)
}
#[derive(Clone, Serialize, Deserialize)]
struct Job {
    token: String,
    refresh_token: String,
    expires: i64,
    refresh_expires: i64,
    ttl: i64,
}
#[derive(Serialize, Deserialize)]
struct Saved {
    identity: String,
    machine_id: String,
    job: Option<Job>,
}
pub(crate) struct Qoder {
    id: String,
    identity: String,
    environment: Arc<dyn Environment>,
    saved: Saved,
    loaded: bool,
    dirty: bool,
    retry_at: i64,
    last_failure: Option<ConnectionError>,
    refresh_uncertain: bool,
    /// The account's user id, from the source login: the legacy route signs with it.
    subject: Option<String>,
    /// The catalog's entries by model, for the legacy route's requests.
    targets: legacy::Targets,
    /// The account's legacy queue places and leases, across requests.
    queue: Arc<legacy::Queue>,
    /// Where the legacy chat could not serve; cleared when the catalog is read again.
    degraded: Arc<route::Degraded>,
    #[cfg(test)]
    openapi: String,
    #[cfg(test)]
    catalog_url: String,
}
pub(crate) fn source_login(v: &Value) -> Result<Login> {
    if v["schemaVersion"] != 1 {
        return Err(
            ConnectionError::new("unsupported_login_schema", "Qoder 登录文件格式暂不支持")
                .at("source_login")
                .field("schemaVersion"),
        );
    }
    if v["openApiBase"].as_str().is_some_and(|s| !s.is_empty()) {
        return Err(ConnectionError::new(
            "unsupported_origin",
            "当前适配器暂不支持自定义 Qoder 认证地址",
        )
        .at("source_login")
        .field("openApiBase"));
    }
    if let Some(context) = v["authenticationContext"].as_str() {
        let context: Value = serde_json::from_str(context)
            .map_err(|_| ConnectionError::format("source_login", "authenticationContext"))?;
        if context[1] != OPENAPI || context[2] != CLIENT_ID {
            return Err(ConnectionError::new(
                "unsupported_auth_context",
                "Qoder 认证服务或客户端标识不受当前适配器支持",
            )
            .at("source_login")
            .field("authenticationContext"));
        }
    }
    let uid = v["user"]["id"]
        .as_str()
        .filter(|s| !s.is_empty())
        .map(str::to_owned)
        .or_else(|| v["user"]["id"].as_u64().map(|n| n.to_string()))
        .ok_or_else(|| ConnectionError::format("source_login", "user.id"))?;
    let token = v["token"]
        .as_str()
        .filter(|s| !s.is_empty())
        .ok_or_else(|| ConnectionError::format("source_login", "token"))?
        .to_owned();
    let expires = date(&v["expiresAt"])
        .ok_or_else(|| ConnectionError::format("source_login", "expiresAt"))?;
    Ok(Login {
        identity: digest(json!(["qoder-global", uid]).to_string().as_bytes()),
        token,
        expires,
        subject: Some(uid),
    })
}
fn date(v: &Value) -> Option<i64> {
    chrono::DateTime::parse_from_rfc3339(v.as_str()?)
        .ok()
        .map(|v| v.timestamp_millis())
}
fn parse_job(v: &Value, now: i64) -> Result<Job> {
    let string = |key: &str| {
        v[key]
            .as_str()
            .filter(|s| !s.is_empty() && s.len() <= 32_768)
            .map(str::to_owned)
            .ok_or_else(|| ConnectionError::format("authorization", key))
    };
    let expires = date(&v["expires_at"])
        .or_else(|| now.checked_add(v["expires_in"].as_i64()?))
        .filter(|e| *e > now + 30_000)
        .ok_or_else(|| ConnectionError::format("authorization", "expires_at / expires_in"))?;
    let refresh_expires = date(&v["refresh_token_expires_at"])
        .or_else(|| now.checked_add(v["refresh_token_expires_in"].as_i64()?))
        .filter(|e| *e > now)
        .ok_or_else(|| {
            ConnectionError::format(
                "authorization",
                "refresh_token_expires_at / refresh_token_expires_in",
            )
        })?;
    Ok(Job {
        token: string("token")?,
        refresh_token: string("refresh_token")?,
        expires,
        refresh_expires,
        ttl: expires - now,
    })
}
impl Qoder {
    pub fn new(id: String, identity: String, environment: Arc<dyn Environment>) -> Self {
        Self {
            id,
            saved: Saved {
                identity: identity.clone(),
                machine_id: uuid::Uuid::new_v4().to_string(),
                job: None,
            },
            identity,
            environment,
            loaded: false,
            dirty: false,
            retry_at: 0,
            last_failure: None,
            refresh_uncertain: false,
            subject: None,
            targets: Arc::default(),
            queue: Arc::default(),
            degraded: Arc::default(),
            #[cfg(test)]
            openapi: OPENAPI.into(),
            #[cfg(test)]
            catalog_url: CATALOG.into(),
        }
    }
    fn openapi(&self) -> &str {
        #[cfg(test)]
        {
            &self.openapi
        }
        #[cfg(not(test))]
        {
            OPENAPI
        }
    }
    fn catalog_url(&self) -> &str {
        #[cfg(test)]
        {
            &self.catalog_url
        }
        #[cfg(not(test))]
        {
            CATALOG
        }
    }
    async fn source(&mut self) -> Result<Login> {
        self.environment.headers().await?;
        let login = self.environment.login().await?;
        if login.identity != self.identity {
            self.saved.job = None;
            self.environment.delete(&self.id).await?;
            return Err(ConnectionError::new(
                "identity_changed",
                "Qoder 账户已切换，请移除旧连接后重新连接",
            ));
        }
        Ok(login)
    }
    async fn save(&mut self) -> Result<()> {
        let value = zeroize::Zeroizing::new(serde_json::to_string(&self.saved).map_err(|_| {
            ConnectionError::new("saved_session_invalid", "无法编码 Qoder 授权数据")
                .at("credential_store")
        })?);
        self.environment.set(&self.id, &value).await?;
        self.dirty = false;
        Ok(())
    }
    async fn resolve(&mut self, http: &reqwest::Client, rejected: bool) -> Result<Job> {
        let login = self.source().await?;
        self.subject = login.subject.clone();
        let now = self.environment.now();
        if !self.loaded {
            if let Some(value) = self.environment.get(&self.id).await? {
                let saved: Saved = serde_json::from_str(&value).map_err(|_| {
                    ConnectionError::new(
                        "saved_session_invalid",
                        "已保存的 Qoder 授权数据格式无效，请移除连接后重新连接",
                    )
                    .at("credential_store")
                })?;
                if saved.identity != self.identity
                    || uuid::Uuid::parse_str(&saved.machine_id).is_err()
                {
                    return Err(ConnectionError::new(
                        "saved_session_invalid",
                        "已保存的 Qoder 授权身份或设备标识无效，请移除连接后重新连接",
                    )
                    .at("credential_store"));
                }
                self.saved = saved;
            }
            self.loaded = true;
        }
        if self.dirty {
            self.save().await?;
            self.refresh_uncertain = false;
        }
        if let Some(job) = self.saved.job.clone() {
            let margin = 300_000.min(job.ttl / 2).max(30_000);
            if !rejected && job.expires > now + margin {
                return Ok(job);
            }
            if now < self.retry_at {
                if !rejected && job.expires > now + 60_000 {
                    return Ok(job);
                }
                return Err(self
                    .last_failure
                    .clone()
                    .unwrap_or_else(|| ConnectionError::unavailable().at("authorization")));
            }
            if !self.refresh_uncertain && job.refresh_expires > now + 30_000 {
                let result = json_request(
                    http,
                    &format!("{}/api/v1/jobToken/refresh", self.openapi()),
                    None,
                    &json!({"refresh_token":job.refresh_token}),
                    "refresh",
                )
                .await;
                match result {
                    Ok(v) => {
                        // If parsing or persistence fails, the previous refresh may be spent.
                        self.refresh_uncertain = true;
                        let next = parse_job(&v, now).map_err(|e| e.at("refresh"))?;
                        self.saved.job = Some(next.clone());
                        self.dirty = true;
                        self.save().await?;
                        self.refresh_uncertain = false;
                        self.retry_at = 0;
                        return Ok(next);
                    }
                    Err(e) if e.code == "auth_rejected" => {
                        tracing::info!("connection_refresh_rejected_reissue_from_source");
                        self.refresh_uncertain = true;
                    }
                    Err(e) if !e.retryable => return Err(e),
                    Err(_) => {
                        tracing::info!("connection_refresh_uncertain_reissue_from_source");
                        // The refresh may have rotated despite a network/server failure.
                        // Recover through the independent source grant, never replay it.
                        self.refresh_uncertain = true;
                    }
                }
            }
        }
        if login.expires <= now {
            if !rejected
                && let Some(job) = self.saved.job.clone()
                && job.expires > now + 60_000
            {
                self.retry_at = now + 30_000;
                tracing::info!(
                    stage = "authorization",
                    reason = "source_expired",
                    backoff_ms = 30_000,
                    "connection_using_valid_cached_authorization"
                );
                return Ok(job);
            }
            return Err(ConnectionError::login());
        }
        let issued = json_request(
            http,
            &format!("{}/api/v1/me/jobToken", self.openapi()),
            Some(&login.token),
            &json!({"clientId":CLIENT_ID}),
            "authorization",
        )
        .await;
        let value = match issued {
            Ok(v) => v,
            Err(e) => {
                self.retry_at = now + 30_000;
                self.last_failure = Some(e.clone().source_rejected());
                if e.retryable
                    && !rejected
                    && let Some(job) = self.saved.job.clone()
                    && job.expires > now + 60_000
                {
                    tracing::info!(
                        stage = "authorization",
                        reason = "transient_issue_failure",
                        backoff_ms = 30_000,
                        "connection_using_valid_cached_authorization"
                    );
                    return Ok(job);
                }
                return Err(e.source_rejected());
            }
        };
        let job = parse_job(&value, now)?;
        self.saved.job = Some(job.clone());
        self.dirty = true;
        self.save().await?;
        self.refresh_uncertain = false;
        self.retry_at = 0;
        self.last_failure = None;
        Ok(job)
    }
}
#[async_trait]
impl ProductAdapter for Qoder {
    async fn authorize(&mut self, http: &reqwest::Client, rejected: bool) -> Result<Authorization> {
        let job = self.resolve(http, rejected).await?;
        Ok(Authorization {
            base_url: MODEL_BASE.into(),
            credentials: Credentials::with_key(job.token),
            transport: Some(Arc::new(route::Route {
                degraded: self.degraded.clone(),
                legacy: legacy::Legacy {
                    base: legacy::BASE.into(),
                    uid: self.subject.clone().unwrap_or_default(),
                    machine: self.saved.machine_id.clone(),
                    targets: self.targets.clone(),
                    queue: self.queue.clone(),
                },
            })),
        })
    }
    async fn catalog(&mut self, http: &reqwest::Client) -> Result<Vec<ConnectionModel>> {
        let value = self.catalog_value(http).await?;
        self.remember_targets(&value);
        let entries = value["app"]
            .as_array()
            .ok_or_else(|| ConnectionError::format("catalog", "app"))?;
        for (index, entry) in entries.iter().enumerate() {
            if entry["enable"] == true && !entry["key"].as_str().is_some_and(crate::valid_id) {
                return Err(ConnectionError::format(
                    "catalog",
                    &format!("app[{index}].key"),
                ));
            }
        }
        let mut ids = std::collections::HashSet::new();
        let models: Vec<_> = entries
            .iter()
            .filter(|m| m["enable"] == true)
            .filter_map(|m| {
                let id = m["key"].as_str()?;
                if !crate::valid_id(id) || !ids.insert(id.to_owned()) {
                    return None;
                }
                capabilities::model(m)
            })
            .collect();
        if !models.iter().any(|m| supports_model(&m.id)) {
            return Err(if entries.iter().all(|m| m["enable"] != true) {
                ConnectionError::new("catalog_empty", "Qoder 账户当前没有启用的模型").at("catalog")
            } else {
                ConnectionError::new(
                    "unsupported_model_protocol",
                    "Qoder 目录中没有当前适配器支持的模型",
                )
                .at("catalog")
            });
        }
        Ok(models)
    }
}
impl Qoder {
    fn remember_targets(&mut self, catalog: &Value) {
        let targets = legacy::targets_of(catalog);
        if !targets.is_empty() {
            *self.targets.write() = targets;
            self.degraded.reset();
        }
    }
    async fn catalog_value(&mut self, http: &reqwest::Client) -> Result<Value> {
        let job = self.resolve(http, false).await?;
        let login = self.source().await?;
        let headers = cosy::headers(
            self.catalog_url(),
            login
                .subject
                .as_deref()
                .ok_or_else(|| ConnectionError::format("source_login", "user.id"))?,
            &job.token,
            &self.saved.machine_id,
        )
        .map_err(|_| {
            ConnectionError::new("catalog_request_invalid", "无法构造 Qoder 模型目录授权请求")
                .at("catalog")
        })?;
        let request = http
            .get(self.catalog_url())
            .headers(headers)
            .timeout(Duration::from_secs(30));
        crate::diagnostic::request_json(request, "catalog").await
    }
}

async fn json_request(
    http: &reqwest::Client,
    url: &str,
    token: Option<&str>,
    body: &Value,
    stage: &'static str,
) -> Result<Value> {
    let mut request = http.post(url).json(body).timeout(Duration::from_secs(30));
    if let Some(token) = token {
        request = request.bearer_auth(token);
    }
    crate::diagnostic::request_json(request, stage).await
}
