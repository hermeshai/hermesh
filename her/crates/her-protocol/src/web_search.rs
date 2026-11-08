//! The 联网搜索 settings (docs/kernel/web-search-design.md §6.4). The order, the switches and the
//! master switch are ordinary configuration (`config/write`); a key goes through `set_key`, which
//! tries it first, and away with `credentials/delete`.

use schemars::JsonSchema;
use serde::Deserialize;
use serde::Serialize;

/// What a service needs before it can be used.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum WebSearchNeeds {
    /// It works as it is; a key only raises its limits.
    Nothing,
    Key,
    /// A connected model account, whose key it shares.
    Account,
}

/// Where a service's key comes from.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum WebSearchKeySource {
    /// Saved with `web_search/set_key`: it can be replaced and removed here.
    Saved,
    /// An environment variable, named in `key_env`.
    Env,
    /// The model account's.
    Account,
}

/// Why a search with a service failed.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum WebSearchErrorKind {
    AuthFailed,
    QuotaExhausted,
    RateLimited,
    Unreachable,
    BadResponse,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct WebSearchFailure {
    pub kind: WebSearchErrorKind,
    /// When, in milliseconds since the epoch.
    pub at_ms: u64,
}

/// One service, in the list's order.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct WebSearchSource {
    pub id: String,
    pub name: String,
    pub needs: WebSearchNeeds,
    /// It has what it needs.
    pub configured: bool,
    /// Its switch: searches skip it when off.
    pub enabled: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub key: Option<WebSearchKeySource>,
    /// The variable the key comes from.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub key_env: Option<String>,
    /// Where to get a key.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub key_url: Option<String>,
    /// Its last search, when that failed; a search that works clears it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub failure: Option<WebSearchFailure>,
}

/// The result of `web_search/status`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct WebSearchStatus {
    /// `web_search.enabled`.
    pub enabled: bool,
    /// The service searches use now: the first that is usable and whose last search did not fail.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub current: Option<String>,
    pub sources: Vec<WebSearchSource>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct WebSearchTestParams {
    pub source: String,
}

/// How one real search went.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct WebSearchTestResult {
    pub ok: bool,
    pub count: u32,
    pub elapsed_ms: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<WebSearchErrorKind>,
}

#[derive(Clone, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct WebSearchSetKeyParams {
    pub source: String,
    pub key: String,
}

/// Keys are never printed.
impl std::fmt::Debug for WebSearchSetKeyParams {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("WebSearchSetKeyParams").field("source", &self.source).finish_non_exhaustive()
    }
}

/// The result of `web_search/set_key`. A key the service refused is not saved; one that could not
/// be tried is, and the service then goes first in the list, switched on.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct WebSearchSetKeyResult {
    pub saved: bool,
    pub test: WebSearchTestResult,
}
