//! Services added by their address (docs/kernel/custom-endpoint-design.md §9): `providers/add`,
//! `providers/update`, `providers/remove` and `models/configure`. The address is probed first and
//! a service is saved only when it can be used; models.toml is then read again, so the service is
//! there at once.

use crate::server::Server;
use her_core::custom_providers;
use her_core::custom_providers::ModelPatch;
use her_core::custom_providers::ServiceEntry;
use her_llm::AuthConfig;
use her_llm::catalog::KNOWN_APIS;
use her_llm::discovery::ListSource;
use her_llm::discovery::ModelList;
use her_llm::probe::ProbeError;
use her_llm::probe::ProbeInput;
use her_llm::probe::ProbeReport;
use her_protocol::api::EndpointProtocol;
use her_protocol::api::ModelsConfigureParams;
use her_protocol::api::ModelsConfigureResult;
use her_protocol::api::ProbeSummary;
use her_protocol::api::ProvidersAddParams;
use her_protocol::api::ProvidersAddResult;
use her_protocol::api::ProvidersUpdateParams;
use her_protocol::api::error_code;
use her_rpc::RpcError;

fn probe_error(error: ProbeError) -> RpcError {
    match &error {
        ProbeError::KeyRejected(message) => RpcError {
            code: error_code::KEY_REJECTED,
            message: message.clone(),
            data: Some(serde_json::json!({ "kind": "key_rejected" })),
        },
        _ => RpcError {
            code: error_code::ENDPOINT_UNUSABLE,
            message: error.to_string(),
            data: Some(serde_json::json!({ "kind": "endpoint_unusable", "reason": error.reason() })),
        },
    }
}

fn unusable(reason: &str, message: impl Into<String>) -> RpcError {
    RpcError {
        code: error_code::ENDPOINT_UNUSABLE,
        message: message.into(),
        data: Some(serde_json::json!({ "kind": "endpoint_unusable", "reason": reason })),
    }
}

/// What to tell the person about services of a known kind (§4.6).
fn note_for(host: &str) -> Option<&'static str> {
    if host.ends_with(".openai.azure.com") || host.ends_with(".services.ai.azure.com") || host.ends_with(".cognitiveservices.azure.com") {
        Some("azure_deployments")
    } else if host.starts_with("bedrock-runtime.") && host.ends_with(".amazonaws.com") {
        Some("bedrock_models")
    } else if host == "aiplatform.googleapis.com" || host.ends_with("-aiplatform.googleapis.com") {
        Some("vertex_token")
    } else if host.ends_with("portkey.ai") {
        Some("portkey_headers")
    } else {
        None
    }
}

fn blocking_error(e: tokio::task::JoinError) -> RpcError {
    RpcError::internal(e.to_string())
}

impl Server {
    /// The built-in provider at the same host, if any (§4.6).
    fn builtin_at(&self, host: &str) -> Option<String> {
        self.0
            .llm
            .providers()
            .filter(|p| !p.custom && !matches!(p.auth, AuthConfig::Managed))
            .find(|p| her_llm::probe::normalize(&p.base_url).is_ok_and(|a| a.host == host))
            .map(|p| p.id)
    }

    async fn probe_endpoint(&self, base_url: &str, api_key: Option<&str>, api: Option<&str>) -> Result<ProbeReport, RpcError> {
        if let Some(api) = api
            && !KNOWN_APIS.contains(&api)
        {
            return Err(RpcError::invalid_params(format!("unknown api '{api}' (expected {})", KNOWN_APIS.join(", "))));
        }
        let http = self.0.llm.http_client().map_err(|e| RpcError::internal(e.message))?;
        let input = ProbeInput { base_url: base_url.to_string(), api_key: api_key.map(str::to_string), api: api.map(str::to_string) };
        her_llm::probe::probe(&http, &input).await.map_err(probe_error)
    }

    /// Saves a probed service: its key, its list, then its entry, and reads models.toml again.
    /// A failure puts back the key and the list that were there before (an update has both).
    async fn save_service(&self, entry: ServiceEntry, key: Option<String>, report: &ProbeReport) -> Result<(), RpcError> {
        let id = entry.id.clone();
        let fresh_key = key.is_some();
        let old_key = if fresh_key {
            let id = id.clone();
            tokio::task::spawn_blocking(move || her_llm::credentials::get(&id)).await.map_err(blocking_error)?
        } else {
            None
        };
        let lists = self.0.llm.directory().lists_dir();
        let list_path = lists.as_ref().filter(|_| report.list.is_some()).map(|dir| dir.join(format!("{id}.json")));
        let old_list = list_path.as_ref().and_then(|path| std::fs::read(path).ok());
        if let Some(key) = key.clone() {
            let id = id.clone();
            tokio::task::spawn_blocking(move || her_llm::credentials::set(&id, &key))
                .await
                .map_err(blocking_error)?
                .map_err(RpcError::internal)?;
        }
        if let (Some(path), Some(list)) = (&list_path, &report.list) {
            let now = her_llm::discovery::now();
            let saved = ModelList {
                version: 1,
                fetched_at: Some(now.clone()),
                checked_at: now,
                source: ListSource::Provider,
                credential: key.as_deref().map(her_llm::discovery::fingerprint),
                error: None,
                models: list.models.clone(),
            };
            her_llm::discovery::write_json(path, &saved).map_err(|e| RpcError::internal(e.message))?;
        }
        let server = self.clone();
        let written = tokio::task::spawn_blocking(move || {
            custom_providers::write_service(&server.0.home, &entry)?;
            her_core::runtime::reload_custom(&server.0.home, &server.0.llm)
        })
        .await
        .map_err(blocking_error)?;
        if let Err(message) = written {
            if fresh_key {
                let id = id.clone();
                let _ = tokio::task::spawn_blocking(move || match old_key {
                    Some(old) => her_llm::credentials::set(&id, &old),
                    None => her_llm::credentials::delete(&id),
                })
                .await;
            }
            if let Some(path) = &list_path {
                let _ = match &old_list {
                    Some(bytes) => std::fs::write(path, bytes),
                    None => std::fs::remove_file(path),
                };
            }
            return Err(RpcError::internal(message));
        }
        self.models_changed();
        Ok(())
    }

    /// The result of `providers/add` and `providers/update`: the service as `models/list` shows
    /// it, and what the probe found.
    async fn added(&self, id: &str, report: &ProbeReport, host: &str) -> Result<ProvidersAddResult, RpcError> {
        let server = self.clone();
        let listing = tokio::task::spawn_blocking(move || server.list_models()).await.map_err(blocking_error)?;
        let provider = listing
            .providers
            .into_iter()
            .find(|p| p.id == id)
            .ok_or_else(|| RpcError::internal(format!("{id} was saved but is not listed")))?;
        let mine: Vec<_> = listing.models.iter().filter(|m| m.provider == id).collect();
        let protocols = report
            .protocols
            .iter()
            .map(|p| EndpointProtocol {
                api: p.api.clone(),
                base_url: p.base_url.clone(),
                auth_header: p.auth_header.map(|h| custom_providers::header_name(h).to_string()),
            })
            .collect();
        Ok(ProvidersAddResult {
            provider,
            probe: ProbeSummary {
                base_url: report.base_url.clone(),
                protocols,
                unsure: report.unsure.clone(),
                models: mine.len() as u32,
                known: mine.iter().filter(|m| m.known).count() as u32,
                no_list: report.list.is_none(),
                builtin: self.builtin_at(host),
                insecure: report.insecure,
                note: note_for(host).map(str::to_string),
            },
        })
    }

    /// `providers/add`: probes the address and saves the service when it can be used (§9).
    pub async fn add_provider(&self, params: ProvidersAddParams) -> Result<ProvidersAddResult, RpcError> {
        let key = params.api_key.as_deref().map(str::trim).filter(|k| !k.is_empty()).map(str::to_string);
        let report = self.probe_endpoint(&params.base_url, key.as_deref(), params.api.as_deref()).await?;
        let models: Vec<String> = params.models.iter().map(|m| m.trim().to_string()).filter(|m| her_llm::discovery::valid_id(m)).collect();
        if report.list.is_none() && models.is_empty() {
            return Err(unusable("no_models", "the service gave no model list: name its models"));
        }
        let host = her_llm::probe::normalize(&params.base_url).map(|a| a.host).unwrap_or_default();
        let llm = self.0.llm.clone();
        let name = params.name.as_deref().map(str::trim).filter(|n| !n.is_empty()).map(str::to_string);
        let id = custom_providers::new_id(name.as_deref(), &host, |id| llm.provider(id).is_some() || id == "chatgpt");
        let entry = ServiceEntry::from_probe(&id, name.as_deref().unwrap_or(&host), &report, key.is_none(), &models)
            .ok_or_else(|| unusable("not_a_model_service", "no protocol to talk to it was found"))?;
        self.save_service(entry, key, &report).await?;
        self.added(&id, &report, &host).await
    }

    fn custom_provider(&self, id: &str) -> Result<her_llm::ProviderConfig, RpcError> {
        match self.0.llm.provider(id) {
            Some(p) if p.custom => Ok(p),
            Some(_) => Err(RpcError::invalid_params(format!("'{id}' is not a service added by its address"))),
            None => Err(RpcError::invalid_params(format!("unknown provider '{id}'"))),
        }
    }

    /// `providers/update`: a new name only renames; a new address, key or protocol is probed
    /// first, and nothing changes when the service cannot be used (§9).
    pub async fn update_provider(&self, params: ProvidersUpdateParams) -> Result<ProvidersAddResult, RpcError> {
        let provider = self.custom_provider(&params.id)?;
        let name = params.name.as_deref().map(str::trim).filter(|n| !n.is_empty()).map(str::to_string);
        let key = params.api_key.as_deref().map(str::trim).filter(|k| !k.is_empty()).map(str::to_string);
        let reprobe = params.base_url.is_some() || key.is_some() || params.api.is_some();
        let base_url = params.base_url.clone().unwrap_or_else(|| provider.base_url.clone());
        let host = her_llm::probe::normalize(&base_url).map(|a| a.host).unwrap_or_default();
        if !reprobe {
            if let Some(name) = name {
                let (server, id) = (self.clone(), params.id.clone());
                tokio::task::spawn_blocking(move || {
                    custom_providers::rename_service(&server.0.home, &id, &name)?;
                    her_core::runtime::reload_custom(&server.0.home, &server.0.llm)
                })
                .await
                .map_err(blocking_error)?
                .map_err(RpcError::internal)?;
                self.models_changed();
            }
            let report = ProbeReport { base_url: base_url.clone(), ..Default::default() };
            return self.added(&params.id, &report, &host).await;
        }
        let stored = match (&key, &provider.auth) {
            (Some(_), _) | (None, AuthConfig::None) => None,
            (None, _) => {
                let id = params.id.clone();
                tokio::task::spawn_blocking(move || her_llm::credentials::get(&id)).await.map_err(blocking_error)?
            }
        };
        let probe_key = key.clone().or(stored);
        let report = self.probe_endpoint(&base_url, probe_key.as_deref(), params.api.as_deref()).await?;
        let entry = ServiceEntry::from_probe(&params.id, name.as_deref().unwrap_or(&provider.name), &report, probe_key.is_none(), &[])
            .ok_or_else(|| unusable("not_a_model_service", "no protocol to talk to it was found"))?;
        let entry = ServiceEntry { list: report.list.is_some() || provider.listing.path.is_some(), ..entry };
        self.save_service(entry, key, &report).await?;
        self.added(&params.id, &report, &host).await
    }

    /// `providers/remove`: the service's entry, its saved key and its list go. Sessions on it
    /// still open (standalone-cli-design D-536). Blocking I/O.
    pub fn remove_provider(&self, id: &str) -> Result<(), RpcError> {
        self.custom_provider(id)?;
        custom_providers::remove_service(&self.0.home, id).map_err(RpcError::internal)?;
        let _ = her_llm::credentials::delete(id);
        if let Some(dir) = self.0.llm.directory().lists_dir() {
            let _ = std::fs::remove_file(dir.join(format!("{id}.json")));
        }
        her_core::runtime::reload_custom(&self.0.home, &self.0.llm).map_err(RpcError::internal)?;
        self.models_changed();
        Ok(())
    }

    /// `models/catalog`: canonical models by name (§7.3). Blocking: reads the catalog.
    pub fn catalog_models(&self, params: her_protocol::api::ModelsCatalogParams) -> her_protocol::api::ModelsCatalogResult {
        let limit = params.limit.unwrap_or(8).clamp(1, 50) as usize;
        let models = self
            .0
            .llm
            .directory()
            .search_canonical(&params.query, limit)
            .into_iter()
            .map(|m| her_protocol::api::CatalogModel {
                name: m.name.clone().unwrap_or_else(|| m.id.clone()),
                context_window: m.context_window.map(u64::from),
                images: m.input.iter().any(|i| i == "image"),
                reasoning: m.reasoning.unwrap_or(false),
                id: m.id,
            })
            .collect();
        her_protocol::api::ModelsCatalogResult { models }
    }

    /// `models/configure`: what a model of a custom endpoint is like (§7.3). Blocking I/O.
    pub fn configure_model(&self, params: ModelsConfigureParams) -> Result<ModelsConfigureResult, RpcError> {
        let (provider_id, model_id) = params
            .model
            .split_once('/')
            .filter(|(p, m)| !p.is_empty() && !m.is_empty())
            .ok_or_else(|| RpcError::invalid_params(format!("expected provider/model-id, got '{}'", params.model)))?;
        self.custom_provider(provider_id)?;
        let canonical = match &params.canonical {
            Some(Some(named)) => {
                let row = self
                    .0
                    .llm
                    .directory()
                    .canonical_for_config(named)
                    .ok_or_else(|| RpcError::invalid_params(format!("no model in the catalog is called '{named}'")))?;
                Some(Some(row.id.clone()))
            }
            other => other.clone(),
        };
        let patch = ModelPatch {
            canonical,
            context_window: params.context_window,
            images: params.images,
            reasoning: params.reasoning,
            clear: params.clear,
        };
        custom_providers::configure_model(&self.0.home, provider_id, model_id, &patch).map_err(RpcError::internal)?;
        her_core::runtime::reload_custom(&self.0.home, &self.0.llm).map_err(RpcError::internal)?;
        self.models_changed();
        let model = self
            .list_models()
            .models
            .into_iter()
            .find(|m| m.id == params.model)
            .ok_or_else(|| RpcError::invalid_params(format!("'{}' is not among the service's models", params.model)))?;
        Ok(ModelsConfigureResult { model })
    }
}
