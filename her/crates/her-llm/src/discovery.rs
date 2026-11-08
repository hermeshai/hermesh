//! Asking a provider which models it offers, and remembering the answer under
//! `~/.her/models/lists` (docs/kernel/model-catalog-design.md §3).

use serde::Deserialize;
use serde::Serialize;
use serde_json::Value;
use std::path::Path;
use std::time::Duration;

use crate::auth::Credentials;
use crate::catalog::ProviderConfig;
use crate::error::ErrorKind;
use crate::error::LlmError;
use crate::model::AuthHeader;
use crate::model::ModelCost;

const LIST_TIMEOUT: Duration = Duration::from_secs(10);
const DETAILS_TIMEOUT: Duration = Duration::from_secs(2);
const MAX_BYTES: usize = 32 * 1024 * 1024;
pub const MAX_MODELS: usize = 5_000;
const ANTHROPIC_VERSION: &str = "2023-06-01";

/// A model id her accepts from outside: 1–256 bytes, printable, no whitespace.
pub fn valid_id(id: &str) -> bool {
    !id.is_empty() && id.len() <= 256 && !id.chars().any(|c| c.is_control() || c.is_whitespace())
}

/// One entry of a provider's list, with whatever details the list itself carries.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct ListedModel {
    pub id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub context_window: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_output_tokens: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reasoning: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub images: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_call: Option<bool>,
    /// US dollars per million tokens.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cost: Option<ModelCost>,
}

impl ListedModel {
    pub fn has_details(&self) -> bool {
        self.context_window.is_some() || self.max_output_tokens.is_some() || self.cost.is_some()
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ListSource {
    /// The provider's list.
    Provider,
    /// The provider has no list endpoint.
    Unsupported,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ListError {
    pub kind: String,
    pub message: String,
}

/// `lists/<provider>.json`
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct ModelList {
    pub version: u32,
    /// The last successful answer.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fetched_at: Option<String>,
    /// The last attempt, whatever its outcome.
    pub checked_at: String,
    pub source: ListSource,
    /// Fingerprint of the key the list was fetched with.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub credential: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<ListError>,
    #[serde(default)]
    pub models: Vec<ListedModel>,
}

pub enum Listed {
    Models(Vec<ListedModel>),
    /// 404 or 405: there is no list to ask for.
    Unsupported,
}

pub fn now() -> String {
    chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Secs, true)
}

/// Seconds since an RFC 3339 time, or `None` when it does not parse.
pub fn age_secs(time: &str) -> Option<i64> {
    let then = chrono::DateTime::parse_from_rfc3339(time).ok()?;
    Some((chrono::Utc::now() - then.with_timezone(&chrono::Utc)).num_seconds())
}

/// The first 8 bytes of the key's SHA-256: enough to notice a new key, useless for finding it.
pub fn fingerprint(key: &str) -> String {
    let digest = ring::digest::digest(&ring::digest::SHA256, key.as_bytes());
    digest.as_ref()[..8].iter().map(|b| format!("{b:02x}")).collect()
}

/// Writes through a temporary file and a rename, so readers never see half a file.
pub fn write_text(path: &Path, text: &str) -> Result<(), LlmError> {
    let io = |e: std::io::Error| LlmError::new(ErrorKind::Unknown, format!("failed to write {}: {e}", path.display()));
    if let Some(dir) = path.parent() {
        create_private_dir(dir).map_err(io)?;
    }
    let tmp = path.with_extension(format!("tmp-{}-{}", std::process::id(), rand::random::<u32>()));
    let result = std::fs::write(&tmp, text).and_then(|()| {
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o600))?;
        }
        std::fs::rename(&tmp, path)
    });
    if result.is_err() {
        let _ = std::fs::remove_file(&tmp);
    }
    result.map_err(io)
}

pub fn write_json(path: &Path, value: &impl Serialize) -> Result<(), LlmError> {
    let text = serde_json::to_string_pretty(value).map_err(|e| LlmError::new(ErrorKind::Unknown, e.to_string()))?;
    write_text(path, &text)
}

fn create_private_dir(dir: &Path) -> std::io::Result<()> {
    let mut builder = std::fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder.create(dir)
}

pub fn read_list(dir: &Path, provider: &str) -> Option<ModelList> {
    let bytes = std::fs::read(dir.join(format!("{provider}.json"))).ok()?;
    serde_json::from_slice(&bytes).ok()
}

/// Reads a response body, failing once it passes `max` bytes.
pub(crate) async fn read_limited(mut response: reqwest::Response, max: usize) -> Result<Vec<u8>, LlmError> {
    let too_big = || LlmError::new(ErrorKind::Protocol, format!("the response is larger than {} MiB", max / 1024 / 1024));
    if response.content_length().is_some_and(|n| n as usize > max) {
        return Err(too_big());
    }
    let mut body = Vec::new();
    while let Some(chunk) = response.chunk().await.map_err(|e| LlmError::from_reqwest(&e))? {
        if body.len() + chunk.len() > max {
            return Err(too_big());
        }
        body.extend_from_slice(&chunk);
    }
    Ok(body)
}

/// Asks `provider` for its models with `credentials`. Only the provider's own `base_url` is asked.
pub async fn fetch(http: &reqwest::Client, provider: &ProviderConfig, credentials: &Credentials) -> Result<Listed, LlmError> {
    let Some(path) = provider.listing.path.as_deref() else {
        return Ok(Listed::Unsupported);
    };
    let base = provider.base_url.trim_end_matches('/');
    let anthropic = provider.api == "anthropic-messages";
    let mut models: Vec<ListedModel> = Vec::new();
    let mut after: Option<String> = None;
    loop {
        let mut url = format!("{base}{path}");
        if anthropic {
            url.push_str(if url.contains('?') { "&" } else { "?" });
            url.push_str("limit=1000");
            if let Some(after) = &after {
                url.push_str(&format!("&after_id={after}"));
            }
        }
        let mut request = http.get(&url).timeout(LIST_TIMEOUT).header(reqwest::header::ACCEPT, "application/json");
        if let Some(key) = &credentials.api_key {
            request = if anthropic && provider.compat.auth_header.unwrap_or(AuthHeader::XApiKey) == AuthHeader::XApiKey {
                request.header("x-api-key", key)
            } else {
                request.bearer_auth(key)
            };
        }
        if anthropic {
            request = request.header("anthropic-version", ANTHROPIC_VERSION);
        }
        for (name, value) in credentials.headers.iter().chain(&provider.headers) {
            request = request.header(name.as_str(), value.as_str());
        }
        let response = request.send().await.map_err(|e| LlmError::from_reqwest(&e))?;
        let status = response.status().as_u16();
        if status == 404 || status == 405 {
            return Ok(Listed::Unsupported);
        }
        if !response.status().is_success() {
            let body = response.text().await.unwrap_or_default();
            return Err(LlmError::from_http(status, &body, None, &[]));
        }
        let body = read_limited(response, MAX_BYTES).await?;
        let page: Value = serde_json::from_slice(&body)
            .map_err(|e| LlmError::new(ErrorKind::Protocol, format!("the model list is not JSON: {e}")))?;
        parse_page(&page, &mut models)?;
        after = if anthropic && page.get("has_more").and_then(Value::as_bool) == Some(true) {
            page.get("last_id").and_then(Value::as_str).map(str::to_string)
        } else {
            None
        };
        if after.is_none() {
            break;
        }
    }
    if provider.listing.details.as_deref() == Some("ollama") {
        ollama_details(http, base, &mut models).await;
    }
    Ok(Listed::Models(models))
}

/// Adds the entries of one page: `{"data": [...]}`, a bare array, or Ollama's `{"models": [...]}`.
fn parse_page(page: &Value, models: &mut Vec<ListedModel>) -> Result<(), LlmError> {
    let items = page
        .get("data")
        .or_else(|| page.get("models"))
        .or(Some(page))
        .and_then(Value::as_array)
        .ok_or_else(|| LlmError::new(ErrorKind::Protocol, "the model list has no entries array"))?;
    for item in items {
        // Gemini's OpenAI-compatible list says `models/gemini-3.8-flash`; requests take the bare id.
        let Some(id) = item.get("id").or_else(|| item.get("name")).and_then(Value::as_str).map(|id| id.strip_prefix("models/").unwrap_or(id)) else {
            continue;
        };
        if !valid_id(id) || models.iter().any(|m| m.id == id) {
            continue;
        }
        if models.len() == MAX_MODELS {
            return Err(LlmError::new(ErrorKind::Protocol, format!("the provider lists more than {MAX_MODELS} models")));
        }
        models.push(listed(id, item));
    }
    Ok(())
}

/// The details some lists carry (OpenRouter: window, output limit, prices, modalities, tools).
fn listed(id: &str, item: &Value) -> ListedModel {
    let u32_of = |v: Option<&Value>| v.and_then(Value::as_u64).filter(|n| *n > 0).map(|n| n.min(u64::from(u32::MAX)) as u32);
    let per_million = |key: &str| {
        item.get("pricing")
            .and_then(|p| p.get(key))
            .and_then(|v| v.as_str().and_then(|s| s.parse::<f64>().ok()).or_else(|| v.as_f64()))
            .filter(|n| n.is_finite() && *n >= 0.0)
            .map(|n| n * 1_000_000.0)
    };
    let cost = per_million("prompt").map(|input| ModelCost {
        input,
        output: per_million("completion").unwrap_or(0.0),
        cache_read: per_million("input_cache_read").unwrap_or(0.0),
        cache_write: per_million("input_cache_write").unwrap_or(0.0),
    });
    let modalities = item.get("architecture").and_then(|a| a.get("input_modalities")).and_then(Value::as_array);
    let parameters = item.get("supported_parameters").and_then(Value::as_array);
    let name = item
        .get("display_name")
        .or_else(|| item.get("name").filter(|_| item.get("id").is_some()))
        .and_then(Value::as_str)
        .filter(|n| !n.is_empty() && n.len() <= 200)
        .map(str::to_string);
    ListedModel {
        id: id.to_string(),
        name,
        context_window: u32_of(item.get("context_length")),
        max_output_tokens: u32_of(item.get("top_provider").and_then(|t| t.get("max_completion_tokens"))),
        reasoning: parameters.map(|p| p.iter().any(|v| v == "reasoning")),
        images: modalities.map(|m| m.iter().any(|v| v == "image")),
        tool_call: parameters.map(|p| p.iter().any(|v| v == "tools")),
        cost,
    }
}

/// Ollama tells each local model's window and abilities through `/api/show`.
async fn ollama_details(http: &reqwest::Client, base: &str, models: &mut [ListedModel]) {
    let root = base.strip_suffix("/v1").unwrap_or(base);
    for model in models.iter_mut() {
        let response = http
            .post(format!("{root}/api/show"))
            .timeout(DETAILS_TIMEOUT)
            .json(&serde_json::json!({ "model": model.id }))
            .send()
            .await;
        let Ok(response) = response else { continue };
        let Ok(info) = response.json::<Value>().await else { continue };
        if let Some(window) = info
            .get("model_info")
            .and_then(Value::as_object)
            .and_then(|m| m.iter().find(|(k, _)| k.ends_with(".context_length")).map(|(_, v)| v))
            .and_then(Value::as_u64)
        {
            model.context_window = Some(window.min(u64::from(u32::MAX)) as u32);
        }
        if let Some(caps) = info.get("capabilities").and_then(Value::as_array) {
            model.tool_call = Some(caps.iter().any(|c| c == "tools"));
            model.images = Some(caps.iter().any(|c| c == "vision"));
            model.reasoning = Some(caps.iter().any(|c| c == "thinking"));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn ids_are_checked() {
        assert!(valid_id("deepseek-flash"));
        assert!(valid_id("anthropic/claude-sonnet-5.5:beta"));
        assert!(!valid_id(""));
        assert!(!valid_id("has space"));
        assert!(!valid_id("bell\u{7}"));
        assert!(!valid_id(&"x".repeat(257)));
    }

    #[test]
    fn pages_of_every_shape() {
        let mut models = Vec::new();
        parse_page(&json!({"data": [{"id": "a"}, {"id": "a"}, {"id": "bad id"}, {"object": "x"}]}), &mut models).unwrap();
        parse_page(&json!([{"id": "b"}]), &mut models).unwrap();
        parse_page(&json!({"models": [{"name": "llama3.2:latest"}]}), &mut models).unwrap();
        assert_eq!(models.iter().map(|m| m.id.as_str()).collect::<Vec<_>>(), ["a", "b", "llama3.2:latest"]);
        assert!(parse_page(&json!({"error": "x"}), &mut models).is_err());
    }

    #[test]
    fn list_details_are_read() {
        let item = json!({
            "id": "qwen/qwen3.8-max", "name": "Qwen: Qwen3.8 Max", "context_length": 262144,
            "top_provider": {"max_completion_tokens": 65536},
            "pricing": {"prompt": "0.0000012", "completion": "0.000006"},
            "architecture": {"input_modalities": ["text", "image"]},
            "supported_parameters": ["tools", "reasoning"]
        });
        let m = listed("qwen/qwen3.8-max", &item);
        assert_eq!(m.name.as_deref(), Some("Qwen: Qwen3.8 Max"));
        assert_eq!(m.context_window, Some(262_144));
        assert_eq!(m.max_output_tokens, Some(65_536));
        assert!((m.cost.unwrap().input - 1.2).abs() < 1e-9);
        assert_eq!((m.images, m.tool_call, m.reasoning), (Some(true), Some(true), Some(true)));
        let bare = listed("x", &json!({"id": "x", "object": "model"}));
        assert!(!bare.has_details());
    }

    #[test]
    fn too_many_models_fail() {
        let items: Vec<Value> = (0..=MAX_MODELS).map(|i| json!({"id": format!("m{i}")})).collect();
        assert!(parse_page(&json!({"data": items}), &mut Vec::new()).is_err());
    }

    #[test]
    fn fingerprints_are_short_and_stable() {
        assert_eq!(fingerprint("sk-abc"), fingerprint("sk-abc"));
        assert_ne!(fingerprint("sk-abc"), fingerprint("sk-abd"));
        assert_eq!(fingerprint("sk-abc").len(), 16);
    }
}
