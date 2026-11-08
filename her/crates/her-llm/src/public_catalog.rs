//! The public model catalog (models.dev), projected to what her uses: one file per provider,
//! a snapshot compiled in and a fresher copy the service keeps under `~/.her/models/catalog`
//! (docs/kernel/model-catalog-design.md §4–§5).

use serde::Deserialize;
use serde::Serialize;
use serde_json::Value;
use std::collections::BTreeMap;
use std::path::Path;
use std::time::Duration;

use crate::error::ErrorKind;
use crate::error::LlmError;
use crate::model::CostTier;
use crate::model::ModelCost;

pub const SOURCE_URL: &str = "https://models.dev/api.json";

/// The upstream file is a few megabytes; far past that it is not the catalog.
pub const MAX_BYTES: usize = 32 * 1024 * 1024;

const FETCH_TIMEOUT: Duration = Duration::from_secs(10);

/// Catalog ids with a compiled-in snapshot; the built-in providers' `catalog_id`s.
const EMBEDDED: &[(&str, &str)] = &[
    ("alibaba-cn", include_str!("../catalog/public/alibaba-cn.json")),
    ("alibaba-token-plan-cn", include_str!("../catalog/public/alibaba-token-plan-cn.json")),
    ("anthropic", include_str!("../catalog/public/anthropic.json")),
    ("deepseek", include_str!("../catalog/public/deepseek.json")),
    ("google", include_str!("../catalog/public/google.json")),
    ("minimax-cn", include_str!("../catalog/public/minimax-cn.json")),
    ("moonshotai-cn", include_str!("../catalog/public/moonshotai-cn.json")),
    ("openai", include_str!("../catalog/public/openai.json")),
    ("openrouter", include_str!("../catalog/public/openrouter.json")),
    ("tencent-token-plan", include_str!("../catalog/public/tencent-token-plan.json")),
    ("tencent-tokenhub", include_str!("../catalog/public/tencent-tokenhub.json")),
    ("volcengine-coding-plan", include_str!("../catalog/public/volcengine-coding-plan.json")),
    ("xai", include_str!("../catalog/public/xai.json")),
    ("xiaomi", include_str!("../catalog/public/xiaomi.json")),
    ("zai-coding-plan", include_str!("../catalog/public/zai-coding-plan.json")),
    ("zhipuai", include_str!("../catalog/public/zhipuai.json")),
    ("zhipuai-coding-plan", include_str!("../catalog/public/zhipuai-coding-plan.json")),
];

include!("../catalog/makers.rs");
include!(concat!(env!("OUT_DIR"), "/maker-ids.rs"));

/// Whether a maker's snapshot compiled into this build has the model a channel calls `id`.
pub fn compiled_maker_has(id: &str) -> bool {
    let known = |name: &str| MAKER_NAME_HASHES.binary_search(&model_name_hash(name)).is_ok();
    known(id) || known(bare_name(id))
}

/// One provider's models as the public catalog describes them.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct PublicProvider {
    pub provider: String,
    /// When this copy was taken from upstream (RFC 3339, UTC).
    pub fetched_at: String,
    pub models: Vec<PublicModel>,
}

impl PublicProvider {
    pub fn model(&self, id: &str) -> Option<&PublicModel> {
        self.models.iter().find(|m| m.id == id)
    }

    /// The row of a model as another channel calls it (see [`same_model`]).
    pub fn model_named(&self, id: &str) -> Option<&PublicModel> {
        self.models.iter().find(|m| same_model(&m.id, id))
    }
}

/// Whether a maker's row describes the model a channel calls `id`. Channels differ in case and in
/// a maker's prefix: `minimax-m3` is `MiniMax-M3`, `deepseek/deepseek-flash` is `deepseek-flash`.
pub fn same_model(row: &str, id: &str) -> bool {
    row.eq_ignore_ascii_case(id) || row.eq_ignore_ascii_case(bare_name(id))
}

fn bare_name(id: &str) -> &str {
    id.rsplit_once('/').map_or(id, |(_, name)| name)
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct PublicModel {
    pub id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    /// The smaller of the context window and the input limit.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub context_window: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_output_tokens: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reasoning: Option<bool>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub reasoning_options: Vec<ReasoningOption>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub input: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub output: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_call: Option<bool>,
    /// Upstream `type`: "embedding", "image", … (absent for chat models).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub kind: Option<String>,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub deprecated: bool,
    /// `YYYY-MM-DD`
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub released: Option<String>,
    /// US dollars per million tokens.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cost: Option<ModelCost>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub cost_tiers: Vec<CostTier>,
}

/// Upstream kinds that are not chat models.
const NOT_CHAT: &[&str] = &["embedding", "image", "video", "speech", "transcription", "reranking", "realtime"];

impl PublicModel {
    /// Whether an agent can use it: a chat model with text out that does not refuse tools.
    pub fn is_chat(&self) -> bool {
        !self.kind.as_deref().is_some_and(|k| NOT_CHAT.contains(&k))
            && (self.output.is_empty() || self.output.iter().any(|o| o == "text"))
            && self.tool_call != Some(false)
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ReasoningOption {
    /// Reasoning can be switched on and off.
    Toggle,
    /// Named effort values, as the provider spells them.
    Effort { values: Vec<String> },
    /// A token budget.
    BudgetTokens {
        #[serde(default, skip_serializing_if = "Option::is_none")]
        min: Option<u32>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        max: Option<u32>,
    },
}

/// The upstream catalog, projected to some providers.
#[derive(Debug, Default)]
pub struct Projection {
    pub providers: BTreeMap<String, PublicProvider>,
    /// What stopped the others: one that does not project is not taken, the rest still are, so
    /// an unknown `catalog_id` in models.toml costs only its own provider.
    pub failed: Vec<String>,
}

/// Projects the upstream catalog to the providers in `ids`. Fails when it is not a catalog.
pub fn project(raw: &Value, ids: &[&str], fetched_at: &str) -> Result<Projection, String> {
    let root = raw.as_object().ok_or("the catalog is not a JSON object")?;
    let mut out = Projection::default();
    for id in ids {
        match project_provider(root, id, fetched_at) {
            Ok(provider) => {
                out.providers.insert(id.to_string(), provider);
            }
            Err(error) => out.failed.push(error),
        }
    }
    if out.providers.is_empty() && !out.failed.is_empty() {
        return Err(out.failed.join("; "));
    }
    Ok(out)
}

fn project_provider(root: &serde_json::Map<String, Value>, id: &str, fetched_at: &str) -> Result<PublicProvider, String> {
    let provider = root.get(id).ok_or_else(|| format!("provider '{id}' is missing"))?;
    let models = provider
        .get("models")
        .and_then(Value::as_object)
        .ok_or_else(|| format!("provider '{id}' has no models object"))?;
    let mut list = Vec::with_capacity(models.len());
    for (key, model) in models {
        list.push(project_model(key, model).map_err(|e| format!("{id}/{key}: {e}"))?);
    }
    list.sort_by(|a, b| a.id.cmp(&b.id));
    Ok(PublicProvider { provider: id.to_string(), fetched_at: fetched_at.to_string(), models: list })
}

fn project_model(key: &str, m: &Value) -> Result<PublicModel, String> {
    let obj = m.as_object().ok_or("not an object")?;
    let id = match obj.get("id") {
        Some(Value::String(id)) => id.clone(),
        None => key.to_string(),
        Some(_) => return Err("`id` is not a string".into()),
    };
    if !crate::discovery::valid_id(&id) {
        return Err(format!("invalid model id {id:?}"));
    }
    let limit = obj.get("limit");
    let context = count(limit, "context")?;
    let input_limit = count(limit, "input")?;
    let modalities = obj.get("modalities");
    let cost = obj.get("cost").filter(|c| !c.is_null());
    Ok(PublicModel {
        id,
        name: string(obj.get("name"))?,
        context_window: match (context, input_limit) {
            (Some(c), Some(i)) => Some(c.min(i)),
            (c, i) => c.or(i),
        },
        max_output_tokens: count(limit, "output")?,
        reasoning: boolean(obj.get("reasoning"))?,
        reasoning_options: reasoning_options(obj.get("reasoning_options"))?,
        input: strings(modalities.and_then(|v| v.get("input")))?,
        output: strings(modalities.and_then(|v| v.get("output")))?,
        tool_call: boolean(obj.get("tool_call"))?,
        kind: string(obj.get("type"))?,
        deprecated: string(obj.get("status"))?.as_deref() == Some("deprecated"),
        released: string(obj.get("release_date"))?,
        cost: cost.map(prices).transpose()?,
        cost_tiers: cost.map(tiers).transpose()?.unwrap_or_default(),
    })
}

fn string(v: Option<&Value>) -> Result<Option<String>, String> {
    match v {
        None | Some(Value::Null) => Ok(None),
        Some(Value::String(s)) => Ok(Some(s.clone())),
        Some(other) => Err(format!("expected a string, got {other}")),
    }
}

fn boolean(v: Option<&Value>) -> Result<Option<bool>, String> {
    match v {
        None | Some(Value::Null) => Ok(None),
        Some(Value::Bool(b)) => Ok(Some(*b)),
        Some(other) => Err(format!("expected a boolean, got {other}")),
    }
}

fn strings(v: Option<&Value>) -> Result<Vec<String>, String> {
    match v {
        None | Some(Value::Null) => Ok(Vec::new()),
        Some(Value::Array(items)) => items
            .iter()
            .map(|i| i.as_str().map(str::to_string).ok_or_else(|| format!("expected strings, got {i}")))
            .collect(),
        Some(other) => Err(format!("expected a list, got {other}")),
    }
}

/// A token count; zero means unknown.
fn count(parent: Option<&Value>, key: &str) -> Result<Option<u32>, String> {
    match parent.and_then(|p| p.get(key)) {
        None | Some(Value::Null) => Ok(None),
        Some(v) => {
            let n = v.as_f64().filter(|n| n.is_finite() && *n >= 0.0).ok_or_else(|| format!("bad {key}: {v}"))?;
            Ok((n >= 1.0).then(|| n.min(f64::from(u32::MAX)) as u32))
        }
    }
}

fn price(parent: &Value, key: &str) -> Result<f64, String> {
    match parent.get(key) {
        None | Some(Value::Null) => Ok(0.0),
        Some(v) => v.as_f64().filter(|n| n.is_finite() && *n >= 0.0).ok_or_else(|| format!("bad price {key}: {v}")),
    }
}

fn prices(v: &Value) -> Result<ModelCost, String> {
    if !v.is_object() {
        return Err(format!("bad cost: {v}"));
    }
    Ok(ModelCost {
        input: price(v, "input")?,
        output: price(v, "output")?,
        cache_read: price(v, "cache_read")?,
        cache_write: price(v, "cache_write")?,
    })
}

/// Long-context prices: `tiers` of type "context", and the older `context_over_200k`.
fn tiers(v: &Value) -> Result<Vec<CostTier>, String> {
    let mut out = Vec::new();
    if let Some(list) = v.get("tiers").and_then(Value::as_array) {
        for tier in list {
            let Some(spec) = tier.get("tier") else { continue };
            if spec.get("type").and_then(Value::as_str) != Some("context") {
                continue;
            }
            let Some(size) = spec.get("size").and_then(Value::as_u64) else { continue };
            out.push(CostTier { above: size, cost: prices(tier)? });
        }
    }
    if let Some(over) = v.get("context_over_200k").filter(|o| o.is_object())
        && !out.iter().any(|t| t.above == 200_000)
    {
        out.push(CostTier { above: 200_000, cost: prices(over)? });
    }
    out.sort_by_key(|t| t.above);
    Ok(out)
}

fn reasoning_options(v: Option<&Value>) -> Result<Vec<ReasoningOption>, String> {
    let Some(Value::Array(items)) = v else {
        return Ok(Vec::new());
    };
    let mut out = Vec::new();
    for item in items {
        match item.get("type").and_then(Value::as_str) {
            Some("toggle") => out.push(ReasoningOption::Toggle),
            Some("effort") => out.push(ReasoningOption::Effort { values: strings(item.get("values"))? }),
            Some("budget_tokens") => out.push(ReasoningOption::BudgetTokens {
                min: count(Some(item), "min")?,
                max: count(Some(item), "max")?,
            }),
            // Kinds her does not know are left out, not refused.
            _ => {}
        }
    }
    Ok(out)
}

/// One provider's file: a header line, then one model per line, so diffs stay readable.
pub fn to_file(provider: &PublicProvider) -> String {
    let mut out = format!(
        "{{\"provider\":{},\"fetched_at\":{},\"models\":[\n",
        serde_json::to_string(&provider.provider).unwrap_or_default(),
        serde_json::to_string(&provider.fetched_at).unwrap_or_default()
    );
    for (i, model) in provider.models.iter().enumerate() {
        out.push_str(&serde_json::to_string(model).unwrap_or_default());
        out.push_str(if i + 1 < provider.models.len() { ",\n" } else { "\n" });
    }
    out.push_str("]}\n");
    out
}

/// `fetched_at` from a file's first line, without parsing the rest.
fn header_time(text: &str) -> Option<&str> {
    let line = text.lines().next()?;
    let start = line.find("\"fetched_at\":\"")? + "\"fetched_at\":\"".len();
    let end = start + line[start..].find('"')?;
    Some(&line[start..end])
}

pub fn embedded(catalog_id: &str) -> Option<&'static str> {
    EMBEDDED.iter().find(|(id, _)| *id == catalog_id).map(|(_, text)| *text)
}

pub fn embedded_ids() -> impl Iterator<Item = &'static str> {
    EMBEDDED.iter().map(|(id, _)| *id)
}

/// The text of the newer of the local copy under `dir` and the compiled-in snapshot.
fn newer_text(dir: Option<&Path>, catalog_id: &str) -> Option<std::borrow::Cow<'static, str>> {
    let builtin = embedded(catalog_id);
    let local = dir.and_then(|d| std::fs::read_to_string(d.join(format!("{catalog_id}.json"))).ok());
    match (local, builtin) {
        (Some(local), Some(builtin)) if header_time(&local) > header_time(builtin) => Some(local.into()),
        (Some(local), None) => Some(local.into()),
        (_, Some(builtin)) => Some(builtin.into()),
        (None, None) => None,
    }
}

/// The newer of the local copy under `dir` and the compiled-in snapshot; only that one is parsed.
/// A local copy that does not parse gives way to the snapshot.
pub fn load(dir: Option<&Path>, catalog_id: &str) -> Option<PublicProvider> {
    let text = newer_text(dir, catalog_id)?;
    serde_json::from_str(&text)
        .ok()
        .or_else(|| embedded(catalog_id).and_then(|t| serde_json::from_str(t).ok()))
}

/// One model, parsing only its line (files hold one model per line).
pub fn load_one(dir: Option<&Path>, catalog_id: &str, model_id: &str) -> Option<PublicModel> {
    let text = newer_text(dir, catalog_id)?;
    let id = serde_json::to_string(model_id).ok()?;
    let (more, last) = (format!("{{\"id\":{id},"), format!("{{\"id\":{id}}}"));
    text.lines()
        .skip(1)
        .find(|line| line.starts_with(&more) || line.trim_end_matches(',') == last)
        .and_then(|line| serde_json::from_str(line.trim_end_matches(',')).ok())
}

/// The row of a maker as a channel calls the model, parsing only its line.
pub fn load_named(dir: Option<&Path>, catalog_id: &str, model_id: &str) -> Option<PublicModel> {
    let text = newer_text(dir, catalog_id)?;
    text.lines()
        .skip(1)
        .find(|line| {
            line.strip_prefix("{\"id\":\"")
                .and_then(|rest| rest.split_once('"'))
                .is_some_and(|(row, _)| same_model(row, model_id))
        })
        .and_then(|line| serde_json::from_str(line.trim_end_matches(',')).ok())
}

/// What the service remembers between refreshes.
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
pub struct RefreshState {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub etag: Option<String>,
    /// Last time upstream was asked, whatever the answer.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub checked_at: Option<String>,
}

pub fn read_state(dir: &Path) -> RefreshState {
    std::fs::read(dir.join("state.json")).ok().and_then(|b| serde_json::from_slice(&b).ok()).unwrap_or_default()
}

/// Asks upstream for the catalog and replaces the local copies of `ids` when it changed.
/// Returns whether any provider's models changed. On any failure the old copies stay.
pub async fn refresh(http: &reqwest::Client, url: &str, dir: &Path, ids: &[&str]) -> Result<bool, LlmError> {
    let mut state = read_state(dir);
    let mut request = http.get(url).timeout(FETCH_TIMEOUT).header(reqwest::header::ACCEPT, "application/json");
    if let Some(etag) = &state.etag {
        request = request.header(reqwest::header::IF_NONE_MATCH, etag);
    }
    let response = request.send().await.map_err(|e| LlmError::from_reqwest(&e))?;
    let now = crate::discovery::now();
    if response.status() == reqwest::StatusCode::NOT_MODIFIED {
        state.checked_at = Some(now);
        crate::discovery::write_json(&dir.join("state.json"), &state)?;
        return Ok(false);
    }
    let status = response.status().as_u16();
    if !response.status().is_success() {
        return Err(LlmError::new(ErrorKind::Server, format!("the model catalog answered HTTP {status}")));
    }
    let etag = response.headers().get(reqwest::header::ETAG).and_then(|v| v.to_str().ok()).map(str::to_string);
    let body = crate::discovery::read_limited(response, MAX_BYTES).await?;
    let raw: Value = serde_json::from_slice(&body)
        .map_err(|e| LlmError::new(ErrorKind::Protocol, format!("the model catalog is not JSON: {e}")))?;
    let projected = project(&raw, ids, &now).map_err(|e| LlmError::new(ErrorKind::Protocol, e))?;
    for failure in &projected.failed {
        tracing::warn!(%failure, "a provider of the model catalog was not refreshed");
    }
    let mut changed = false;
    for (id, provider) in projected.providers {
        let current = load(Some(dir), &id);
        if current.as_ref().map(|c| &c.models) != Some(&provider.models) {
            changed = true;
        }
        crate::discovery::write_text(&dir.join(format!("{id}.json")), &to_file(&provider))?;
    }
    state.etag = etag;
    state.checked_at = Some(now);
    crate::discovery::write_json(&dir.join("state.json"), &state)?;
    Ok(changed)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn sample() -> Value {
        json!({
            "deepseek": {"models": {
                "deepseek-flash": {
                    "id": "deepseek-flash", "name": "DeepSeek V4.1 Flash",
                    "reasoning": true,
                    "reasoning_options": [{"type": "toggle"}, {"type": "effort", "values": ["low", "high", "max"]}, {"type": "mystery"}],
                    "tool_call": true,
                    "modalities": {"input": ["text", "image"], "output": ["text"]},
                    "limit": {"context": 1048576, "output": 393216},
                    "release_date": "2026-10-17",
                    "cost": {"input": 0.15, "output": 0.6, "cache_read": 0.003,
                             "tiers": [{"input": 0.3, "output": 1.2, "tier": {"type": "context", "size": 272000}}]}
                },
                "deepseek-embed": {"type": "embedding", "limit": {"context": 8192, "output": 0}},
                "old": {"status": "deprecated", "limit": {"context": 128000, "input": 100000, "output": 8192},
                        "cost": {"input": 1, "output": 2, "context_over_200k": {"input": 2, "output": 4}}}
            }},
            "other": {"models": {}}
        })
    }

    #[test]
    fn projects_the_fields_her_uses() {
        let out = project(&sample(), &["deepseek"], "2026-10-30T00:00:00Z").unwrap().providers;
        let p = &out["deepseek"];
        assert_eq!(p.models.iter().map(|m| m.id.as_str()).collect::<Vec<_>>(), ["deepseek-embed", "deepseek-flash", "old"]);
        let flash = p.model("deepseek-flash").unwrap();
        assert_eq!(flash.context_window, Some(1_048_576));
        assert_eq!(flash.input, ["text", "image"]);
        assert_eq!(
            flash.reasoning_options,
            [ReasoningOption::Toggle, ReasoningOption::Effort { values: vec!["low".into(), "high".into(), "max".into()] }]
        );
        assert_eq!(flash.cost.as_ref().unwrap().cache_read, 0.003);
        assert_eq!(flash.cost_tiers[0].above, 272_000);
        assert!(flash.is_chat());
        let embed = p.model("deepseek-embed").unwrap();
        assert!(!embed.is_chat());
        assert_eq!(embed.max_output_tokens, None);
        let old = p.model("old").unwrap();
        assert!(old.deprecated);
        assert_eq!(old.context_window, Some(100_000), "the input limit bounds the window");
        assert_eq!(old.cost_tiers, [CostTier { above: 200_000, cost: ModelCost { input: 2.0, output: 4.0, ..Default::default() } }]);
    }

    #[test]
    fn a_provider_that_does_not_project_costs_only_itself() {
        assert!(project(&json!([]), &["deepseek"], "t").is_err(), "not a catalog");
        assert!(project(&sample(), &["missing"], "t").unwrap_err().contains("missing"));
        let mut bad = sample();
        bad["deepseek"]["models"]["deepseek-flash"]["limit"]["context"] = json!("lots");
        assert!(project(&bad, &["deepseek"], "t").unwrap_err().contains("deepseek-flash"));
        let mut bad_id = sample();
        bad_id["deepseek"]["models"]["x"] = json!({"id": "has space"});
        assert!(project(&bad_id, &["deepseek"], "t").is_err());
        // Among others, it is reported and the rest are taken.
        let some = project(&sample(), &["deepseek", "missing", "other"], "t").unwrap();
        assert_eq!(some.providers.keys().collect::<Vec<_>>(), ["deepseek", "other"]);
        assert!(some.failed.len() == 1 && some.failed[0].contains("missing"));
    }

    #[test]
    fn a_maker_row_is_found_as_a_channel_calls_the_model() {
        for (row, id) in [("MiniMax-M3", "minimax-m3"), ("deepseek-flash", "deepseek/deepseek-flash"), ("glm-5.3", "z-ai/GLM-5.3"), ("auto", "auto")] {
            assert!(same_model(row, id), "{row} {id}");
        }
        assert!(!same_model("deepseek-flash", "deepseek"), "a prefix of the name is not the name");
        assert!(!same_model("glm-5.3", "glm-5.3-flash"));
        let line = load_named(None, "minimax-cn", "minimax-m3").expect("a line of the snapshot");
        assert_eq!(line.id, "MiniMax-M3");
        assert_eq!(load_named(None, "minimax-cn", "nope"), None);
        let parsed = load(None, "minimax-cn").unwrap();
        assert_eq!(parsed.model_named("minimax-m3").map(|m| m.id.as_str()), Some("MiniMax-M3"));
    }

    #[test]
    fn the_compiled_index_answers_as_the_rows_do() {
        for maker in MAKERS {
            for model in load(None, maker).unwrap().models {
                assert!(compiled_maker_has(&model.id), "{}", model.id);
            }
        }
        assert!(compiled_maker_has("deepseek/deepseek-flash") && compiled_maker_has("minimax-m3"));
        assert!(!compiled_maker_has("mock-1") && !compiled_maker_has("auto") && !compiled_maker_has("flash"));
    }

    #[test]
    fn files_round_trip_and_the_newer_copy_wins() {
        let out = project(&sample(), &["deepseek"], "2099-01-01T00:00:00Z").unwrap().providers;
        let text = to_file(&out["deepseek"]);
        assert_eq!(header_time(&text), Some("2099-01-01T00:00:00Z"));
        assert_eq!(serde_json::from_str::<PublicProvider>(&text).unwrap(), out["deepseek"]);
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("deepseek.json"), &text).unwrap();
        assert_eq!(load(Some(dir.path()), "deepseek").unwrap().models.len(), 3);
        // An older local copy loses to the compiled-in snapshot.
        let old = project(&sample(), &["deepseek"], "2000-01-01T00:00:00Z").unwrap().providers;
        std::fs::write(dir.path().join("deepseek.json"), to_file(&old["deepseek"])).unwrap();
        let loaded = load(Some(dir.path()), "deepseek").unwrap();
        assert_ne!(loaded.fetched_at, "2000-01-01T00:00:00Z");
    }

    #[test]
    fn one_model_is_read_from_its_line() {
        let out = project(&sample(), &["deepseek"], "2099-01-01T00:00:00Z").unwrap().providers;
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("deepseek.json"), to_file(&out["deepseek"])).unwrap();
        for model in &out["deepseek"].models {
            assert_eq!(load_one(Some(dir.path()), "deepseek", &model.id).as_ref(), Some(model));
        }
        assert_eq!(load_one(Some(dir.path()), "deepseek", "deepseek"), None, "a prefix is not a match");
        assert!(load_one(None, "deepseek", "deepseek-flash").is_some(), "the snapshot");
    }

    #[test]
    fn every_snapshot_parses() {
        for id in embedded_ids() {
            let p: PublicProvider = serde_json::from_str(embedded(id).unwrap()).unwrap();
            assert_eq!(p.provider, id);
            assert!(!p.models.is_empty(), "{id}");
            assert!(header_time(embedded(id).unwrap()).is_some());
        }
    }
}
