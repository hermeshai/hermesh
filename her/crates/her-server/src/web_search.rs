//! 联网搜索's methods (docs/kernel/web-search-design.md §6.4). The work is in her-core; the order
//! and the switches are ordinary configuration, so only a key's way in needs more than
//! `config/write`.

use her_core::config::WebSearchSettings;
use her_core::web_search;
use her_protocol::customize::ConfigEdit;
use her_protocol::customize::ConfigLayer;
use her_protocol::customize::ConfigWriteParams;
use her_protocol::web_search::WebSearchErrorKind;
use her_protocol::web_search::WebSearchNeeds;
use her_protocol::web_search::WebSearchSetKeyParams;
use her_protocol::web_search::WebSearchSetKeyResult;
use her_protocol::web_search::WebSearchStatus;
use her_protocol::web_search::WebSearchTestParams;
use her_protocol::web_search::WebSearchTestResult;
use her_rpc::RpcError;
use serde_json::json;
use tokio_util::sync::CancellationToken;

use crate::conn::ConnHandle;
use crate::customize::Topic;
use crate::server::Server;

impl Server {
    /// The settings and each service's state. Reading both can block.
    fn web_search_now(&self) -> Result<(WebSearchSettings, WebSearchStatus), RpcError> {
        let settings = self.workspace(None)?.config.web_search.clone();
        let status = web_search::status(&settings, &self.0.llm);
        Ok((settings, status))
    }

    /// Whoever asks hears of the configuration's changes, as with `config/read`: the order and the
    /// switches are in it, and another window may change them.
    pub async fn web_search_status(&self, conn: &ConnHandle) -> Result<WebSearchStatus, RpcError> {
        self.subscribe(Topic::Config, conn, None);
        let server = self.clone();
        let (_, status) = tokio::task::spawn_blocking(move || server.web_search_now()).await.map_err(|e| RpcError::internal(e.to_string()))??;
        Ok(status)
    }

    /// One real search with one service.
    pub async fn web_search_test(&self, params: WebSearchTestParams) -> Result<WebSearchTestResult, RpcError> {
        let server = self.clone();
        let (settings, _) = tokio::task::spawn_blocking(move || server.web_search_now()).await.map_err(|e| RpcError::internal(e.to_string()))??;
        web_search::test(&settings, &self.0.llm, &params.source, None, &CancellationToken::new()).await.map_err(RpcError::invalid_params)
    }

    /// Tries a key with a real search and keeps it unless the service refused it. A service that
    /// had no key goes first in the list: a key is added to be used. Its switch goes on.
    pub async fn web_search_set_key(&self, params: WebSearchSetKeyParams) -> Result<WebSearchSetKeyResult, RpcError> {
        let key = params.key.trim().to_string();
        if key.is_empty() {
            return Err(RpcError::invalid_params("the key is empty"));
        }
        let server = self.clone();
        let (settings, status) = tokio::task::spawn_blocking(move || server.web_search_now()).await.map_err(|e| RpcError::internal(e.to_string()))??;
        let source = status.sources.iter().find(|s| s.id == params.source).ok_or_else(|| RpcError::invalid_params(format!("no search service '{}'", params.source)))?;
        if source.needs == WebSearchNeeds::Account {
            return Err(RpcError::invalid_params(format!("{} uses its model account's key", source.name)));
        }
        let test = web_search::test(&settings, &self.0.llm, &source.id, Some(key.clone()), &CancellationToken::new()).await.map_err(RpcError::invalid_params)?;
        if test.error == Some(WebSearchErrorKind::AuthFailed) {
            return Ok(WebSearchSetKeyResult { saved: false, test });
        }
        let name = format!("{}{}", web_search::CREDENTIAL_PREFIX, source.id);
        tokio::task::spawn_blocking(move || her_llm::credentials::set(&name, &key))
            .await
            .map_err(|e| RpcError::internal(e.to_string()))?
            .map_err(crate::customize::store_error)?;
        let added = source.key.is_none();
        let mut edits = Vec::new();
        if added {
            let mut order: Vec<&str> = vec![source.id.as_str()];
            order.extend(status.sources.iter().map(|s| s.id.as_str()).filter(|id| *id != source.id));
            edits.push(ConfigEdit { key_path: "web_search.order".into(), value: json!(order), merge: Default::default() });
        }
        if settings.is_disabled(&source.id) {
            let disabled: Vec<&String> = settings.disabled.iter().filter(|id| **id != source.id).collect();
            edits.push(ConfigEdit { key_path: "web_search.disabled".into(), value: json!(disabled), merge: Default::default() });
        }
        if !edits.is_empty() {
            self.config_write(ConfigWriteParams { layer: ConfigLayer::User, cwd: None, edits, expected_version: None }).await?;
        }
        Ok(WebSearchSetKeyResult { saved: true, test })
    }
}
