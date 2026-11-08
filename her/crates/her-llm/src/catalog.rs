//! Provider and model catalog: built-in data merged with the user's models.toml.

use serde::Deserialize;
use std::collections::BTreeMap;
use std::path::Path;
use std::path::PathBuf;

use crate::auth::AuthConfig;
use crate::model::CatalogThinking;
use crate::model::Compat;
use crate::model::DEFAULT_CONTEXT_WINDOW;
use crate::model::Modality;
use crate::model::Model;
use crate::model::ModelCost;
use crate::model::ThinkingBudgets;
use crate::model::ThinkingControl;
use crate::model::ThinkingFormat;
use crate::types::ThinkingLevel;
use crate::types::Currency;

pub const BUILTIN_CATALOG: &str = include_str!("../catalog/builtin.toml");

fn builtin_file() -> Result<CatalogFile, CatalogError> {
    serde_json::from_str(include_str!(concat!(env!("OUT_DIR"), "/builtin-catalog.json")))
        .map_err(|error| CatalogError::Parse { origin: "builtin catalog".into(), message: error.to_string() })
}

#[derive(Debug, thiserror::Error)]
pub enum CatalogError {
    #[error("failed to read {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: std::io::Error,
    },
    #[error("invalid model catalog {origin}: {message}")]
    Parse { origin: String, message: String },
    #[error("provider '{provider}' in {origin}: {message}")]
    Invalid {
        origin: String,
        provider: String,
        message: String,
    },
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct CatalogFile {
    #[serde(default)]
    pub providers: BTreeMap<String, ProviderToml>,
}

#[derive(Debug, Clone, PartialEq, Deserialize)]
#[serde(untagged)]
pub enum StringOrList {
    One(String),
    Many(Vec<String>),
}

impl StringOrList {
    fn into_vec(self) -> Vec<String> {
        match self {
            StringOrList::One(s) => vec![s],
            StringOrList::Many(v) => v,
        }
    }
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct ProviderToml {
    pub name: Option<String>,
    pub api: Option<String>,
    pub base_url: Option<String>,
    pub api_key_env: Option<StringOrList>,
    pub api_key: Option<String>,
    pub api_key_command: Option<Vec<String>>,
    /// Only "none" is accepted.
    pub auth: Option<String>,
    pub headers: Option<BTreeMap<String, String>>,
    pub currency: Option<Currency>,
    pub compat: Option<Compat>,
    /// The provider's id in the public model catalog.
    pub catalog_id: Option<String>,
    /// The model list path, `true` for the wire protocol's default, `false` for none.
    pub list_models: Option<ListModelsToml>,
    /// Use another provider's model list instead of asking (same account, other endpoint).
    pub list_from: Option<String>,
    /// Extra per-model details the server offers ("ollama").
    pub list_details: Option<String>,
    pub models: Option<Vec<ModelToml>>,
    /// A subscription product, independent of its API-key authentication.
    pub plan: Option<PlanConfig>,
}

/// Product metadata. Endpoints and credentials still use the normal provider machinery.
#[derive(Debug, Clone, PartialEq, Deserialize)]
pub struct PlanConfig {
    pub key_url: String,
}

#[derive(Debug, Clone, PartialEq, Deserialize)]
#[serde(untagged)]
pub enum ListModelsToml {
    Enabled(bool),
    Path(String),
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct ModelToml {
    pub id: String,
    pub name: Option<String>,
    /// The name by language tag, from an account's own catalog; never written by hand.
    #[serde(skip)]
    pub names: BTreeMap<String, String>,
    pub api: Option<String>,
    pub base_url: Option<String>,
    pub context_window: Option<u32>,
    pub max_output_tokens: Option<u32>,
    pub reasoning: Option<bool>,
    /// The thinking levels it offers (docs/kernel/model-catalog-design.md §18).
    pub thinking: Option<Vec<ThinkingLevel>>,
    /// The value sent for a level when it is not the level's own name. Without `thinking`, the
    /// levels named here with a value are the ones offered (models.toml before `thinking`).
    pub thinking_levels: Option<BTreeMap<String, String>>,
    /// The level a session starts with on this model.
    pub default_thinking: Option<ThinkingLevel>,
    pub thinking_budgets: Option<ThinkingBudgets>,
    pub input: Option<Vec<Modality>>,
    pub cost: Option<ModelCost>,
    /// Currency of `cost`; defaults to the provider's.
    pub currency: Option<Currency>,
    pub compat: Option<Compat>,
    /// What the public catalog's row says about its thinking; never written by hand.
    #[serde(skip)]
    pub catalog_thinking: Option<CatalogThinking>,
}

impl ModelToml {
    /// A model known by its id alone.
    pub fn new(id: impl Into<String>) -> Self {
        Self { id: id.into(), ..Default::default() }
    }

    /// Whether it says anything about what the model can do, beyond its name and price.
    pub fn describes_model(&self) -> bool {
        self.context_window.is_some()
            || self.max_output_tokens.is_some()
            || self.reasoning.is_some()
            || self.thinking.is_some()
            || self.input.is_some()
    }
}

/// Where a provider's model list and model details come from (docs/kernel/model-catalog-design.md).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Listing {
    /// The provider's id in the public model catalog.
    pub catalog_id: Option<String>,
    /// Path of the model list under `base_url`; `None` when the provider is not asked.
    pub path: Option<String>,
    /// Another provider whose list this one shares.
    pub from: Option<String>,
    /// Extra details the server offers per model ("ollama").
    pub details: Option<String>,
    /// What the channel itself declares, in only the fields it sets: the models.toml and built-in
    /// entries, or an account connection's catalog.
    pub entries: Vec<ModelToml>,
}

impl Listing {
    /// Whether anything beyond the written entries describes this provider's models.
    pub fn is_dynamic(&self) -> bool {
        self.catalog_id.is_some() || self.path.is_some() || self.from.is_some()
    }
}

#[derive(Debug, Clone)]
pub struct ProviderConfig {
    pub id: String,
    pub name: String,
    /// The name by language tag, for an account connection's product; never written by hand.
    pub names: BTreeMap<String, String>,
    pub api: String,
    pub base_url: String,
    pub auth: AuthConfig,
    pub headers: BTreeMap<String, String>,
    pub currency: Currency,
    pub compat: Compat,
    pub listing: Listing,
    pub plan: Option<PlanConfig>,
}

impl ProviderConfig {
    /// What this channel declares about a model.
    pub fn entry(&self, id: &str) -> Option<&ModelToml> {
        self.listing.entries.iter().find(|e| e.id == id)
    }

    /// A channel that decides which models exist: anything else does not. A key's plan, or an
    /// account connection.
    pub fn fixed_membership(&self) -> bool {
        self.plan.is_some() || matches!(self.auth, AuthConfig::Managed)
    }

    /// A model nothing describes: default limits and no reasoning or images.
    pub fn ad_hoc_model(&self, id: &str) -> Model {
        self.build_model(None, &ModelToml::new(id))
    }

    /// The model an entry describes, over `base` (lower layers) and then the defaults.
    pub fn build_model(&self, base: Option<&ModelToml>, entry: &ModelToml) -> Model {
        let mut m = base.cloned().unwrap_or_else(|| ModelToml { id: entry.id.clone(), ..Default::default() });
        let base_currency = m.currency;
        merge_model(&mut m, entry.clone());
        // A price and its currency come from the same layer.
        let currency = match (&entry.cost, entry.currency) {
            (_, Some(c)) => c,
            (Some(_), None) => self.currency,
            (None, None) => base_currency.unwrap_or(self.currency),
        };
        let compat = self.compat.merged(&m.compat.clone().unwrap_or_default());
        let api = m.api.clone().unwrap_or_else(|| self.api.clone());
        let reasoning = m.reasoning.unwrap_or(false);
        let row = m.catalog_thinking.as_ref();
        let thinking = offered_thinking(reasoning, &api, &compat, row, m.thinking.as_deref(), m.thinking_levels.as_ref());
        let default_thinking = m.default_thinking.filter(|l| thinking.contains(l)).unwrap_or_default();
        let thinking_control = match row {
            Some(row) if !row.efforts.is_empty() && row.budget => ThinkingControl::EffortOrBudget,
            Some(row) if !row.efforts.is_empty() => ThinkingControl::Effort,
            Some(row) if row.budget => ThinkingControl::Budget,
            _ if m.thinking_budgets.is_some() => ThinkingControl::Budget,
            _ => ThinkingControl::Effort,
        };
        let mut thinking_levels = m.thinking_levels.unwrap_or_default();
        if row.is_some_and(|row| row.off_effort) {
            thinking_levels.entry(ThinkingLevel::Off.as_str().to_string()).or_insert_with(|| "none".to_string());
        }
        Model {
            name: m.name.unwrap_or_else(|| m.id.clone()),
            provider: self.id.clone(),
            api,
            base_url: m.base_url.unwrap_or_else(|| self.base_url.clone()),
            context_window: m.context_window.unwrap_or(DEFAULT_CONTEXT_WINDOW),
            max_output_tokens: m.max_output_tokens,
            reasoning,
            thinking,
            default_thinking,
            thinking_levels,
            thinking_budgets: m.thinking_budgets.unwrap_or_default(),
            thinking_control,
            input: m.input.unwrap_or_else(|| vec![Modality::Text]),
            // Token rates from the public catalog are not subscription spending.
            cost: if self.plan.is_some() { ModelCost::default() } else { m.cost.unwrap_or_default() },
            cost_tiers: Vec::new(),
            currency,
            compat,
            headers: self.headers.clone(),
            id: m.id,
        }
    }
}

/// What a channel can send about thinking besides efforts.
#[derive(Clone, Copy)]
struct WireThinking {
    switch: bool,
    budget: bool,
}

fn wire_thinking(api: &str, format: Option<ThinkingFormat>) -> WireThinking {
    match (api, format) {
        ("anthropic-messages", _) => WireThinking { switch: true, budget: true },
        ("openai-chat", Some(ThinkingFormat::Deepseek | ThinkingFormat::Zai)) => WireThinking { switch: true, budget: false },
        ("openai-chat", Some(ThinkingFormat::Qwen | ThinkingFormat::Openrouter)) => WireThinking { switch: true, budget: true },
        _ => WireThinking { switch: false, budget: false },
    }
}

/// The levels of a model whose row says how it is controlled, on a channel that can send it
/// (docs/kernel/model-catalog-design.md §18.2): its named efforts, else budgets, else on and off.
/// "Off" only where it can really be switched off.
pub fn offered_by_row(row: &CatalogThinking, api: &str, format: Option<ThinkingFormat>) -> Vec<ThinkingLevel> {
    use ThinkingLevel::*;
    let wire = wire_thinking(api, format);
    let off = row.off_effort || (row.switch && wire.switch);
    if !row.efforts.is_empty() || row.off_effort {
        let mut levels = row.efforts.clone();
        levels.extend(off.then_some(Off));
        return levels;
    }
    if row.budget && wire.budget {
        let mut levels = vec![Low, Medium, High, Max];
        // A budget model of this protocol thinks only when asked to.
        levels.extend((off || api == "anthropic-messages").then_some(Off));
        return levels;
    }
    if off { vec![Off, On] } else { Vec::new() }
}

/// The levels of a model that thinks when nothing says how it is controlled: what the request
/// format can say.
fn assumed_levels(api: &str, format: Option<ThinkingFormat>) -> Vec<ThinkingLevel> {
    use ThinkingLevel::*;
    if api == "anthropic-messages" {
        return vec![Off, Low, Medium, High, Max];
    }
    match format {
        Some(ThinkingFormat::Openai) => vec![Low, Medium, High],
        Some(ThinkingFormat::Openrouter) => vec![Off, Low, Medium, High],
        Some(ThinkingFormat::Deepseek | ThinkingFormat::Qwen | ThinkingFormat::Zai) => vec![Off, On],
        _ => Vec::new(),
    }
}

/// The thinking levels a model offers: those its layers name, else what its catalog row and its
/// channel allow. A row that names no control means the model cannot be told, except through an
/// aggregator, which translates for each model.
fn offered_thinking(
    reasoning: bool,
    api: &str,
    compat: &Compat,
    row: Option<&CatalogThinking>,
    named: Option<&[ThinkingLevel]>,
    values: Option<&BTreeMap<String, String>>,
) -> Vec<ThinkingLevel> {
    use ThinkingLevel::*;
    let format = compat.thinking_format;
    if !reasoning || format == Some(ThinkingFormat::None) {
        return Vec::new();
    }
    let from_values = || -> Option<Vec<ThinkingLevel>> {
        let levels: Vec<ThinkingLevel> =
            values?.iter().filter(|(_, v)| !v.is_empty()).filter_map(|(k, _)| k.parse().ok()).collect();
        (!levels.is_empty()).then_some(levels)
    };
    let mut levels = named.map(<[ThinkingLevel]>::to_vec).or_else(from_values).unwrap_or_else(|| match row {
        Some(row) if *row != CatalogThinking::default() => offered_by_row(row, api, format),
        Some(_) if format != Some(ThinkingFormat::Openrouter) => Vec::new(),
        _ => assumed_levels(api, format),
    });
    levels.retain(|l| *l != Default);
    levels.sort();
    levels.dedup();
    levels
}

#[derive(Debug, Clone, Default)]
pub struct Catalog {
    pub providers: BTreeMap<String, ProviderConfig>,
    /// What reading the user's models.toml left to say.
    pub warnings: Vec<String>,
}

impl Catalog {
    pub fn builtin() -> Result<Self, CatalogError> {
        resolve(builtin_file()?, "builtin catalog")
    }

    /// Built-in catalog merged with `user_file` when it exists.
    pub fn load(user_file: Option<&Path>) -> Result<Self, CatalogError> {
        let mut file = builtin_file()?;
        let mut origin = "builtin catalog".to_string();
        let mut warnings = Vec::new();
        if let Some(path) = user_file
            && path.exists()
        {
            let text = std::fs::read_to_string(path).map_err(|source| CatalogError::Io {
                path: path.to_path_buf(),
                source,
            })?;
            let origin_name = path.display().to_string();
            let (user, unknown) = parse_with_unknown(&text, &origin_name)?;
            warnings.extend(unknown.into_iter().map(|key| format!("ignored unknown setting `{key}` in {origin_name}")));
            merge(&mut file, user);
            origin = origin_name;
        }
        let mut catalog = resolve(file, &origin)?;
        catalog.warnings = warnings;
        Ok(catalog)
    }

    pub fn from_toml(text: &str) -> Result<Self, CatalogError> {
        resolve(parse(text, "inline catalog")?, "inline catalog")
    }
}

/// A file strictly: a key nobody knows is a mistake here.
fn parse(text: &str, origin: &str) -> Result<CatalogFile, CatalogError> {
    let (file, unknown) = parse_with_unknown(text, origin)?;
    match unknown.first() {
        Some(key) => Err(CatalogError::Parse { origin: origin.to_string(), message: format!("unknown setting `{key}`") }),
        None => Ok(file),
    }
}

/// The user's file, with the keys this version does not know: a newer version may have written
/// them, and they should not stop every command (standalone-cli-design §11.2).
fn parse_with_unknown(text: &str, origin: &str) -> Result<(CatalogFile, Vec<String>), CatalogError> {
    let error = |e: toml::de::Error| CatalogError::Parse {
        origin: origin.to_string(),
        message: e.to_string(),
    };
    let mut unknown = Vec::new();
    let deserializer = toml::Deserializer::parse(text).map_err(error)?;
    let file = serde_ignored::deserialize(deserializer, |path| unknown.push(path.to_string().replace(".?", ""))).map_err(error)?;
    Ok((file, unknown))
}

fn merge(base: &mut CatalogFile, over: CatalogFile) {
    for (id, provider) in over.providers {
        match base.providers.get_mut(&id) {
            Some(existing) => merge_provider(existing, provider),
            None => {
                base.providers.insert(id, provider);
            }
        }
    }
}

fn merge_provider(base: &mut ProviderToml, over: ProviderToml) {
    if over.plan.is_some() {
        base.plan = over.plan;
    }
    if over.name.is_some() {
        base.name = over.name;
    }
    if over.api.is_some() {
        base.api = over.api;
    }
    if over.base_url.is_some() {
        base.base_url = over.base_url;
    }
    if over.api_key_env.is_some() || over.api_key.is_some() || over.api_key_command.is_some() || over.auth.is_some() {
        base.api_key_env = over.api_key_env;
        base.api_key = over.api_key;
        base.api_key_command = over.api_key_command;
        base.auth = over.auth;
    }
    if let Some(headers) = over.headers {
        base.headers.get_or_insert_with(BTreeMap::new).extend(headers);
    }
    if over.currency.is_some() {
        base.currency = over.currency;
    }
    if let Some(compat) = over.compat {
        base.compat = Some(base.compat.clone().unwrap_or_default().merged(&compat));
    }
    if over.catalog_id.is_some() {
        base.catalog_id = over.catalog_id;
    }
    if over.list_models.is_some() {
        base.list_models = over.list_models;
    }
    if over.list_from.is_some() {
        base.list_from = over.list_from;
    }
    if over.list_details.is_some() {
        base.list_details = over.list_details;
    }
    if let Some(models) = over.models {
        let list = base.models.get_or_insert_with(Vec::new);
        for model in models {
            match list.iter_mut().find(|m| m.id == model.id) {
                Some(existing) => merge_model(existing, model),
                None => list.push(model),
            }
        }
    }
}

/// A user's entry for a known model changes only the fields it sets: overriding the window must
/// not drop the model's reasoning support or thinking levels.
pub(crate) fn merge_model(base: &mut ModelToml, over: ModelToml) {
    macro_rules! take {
        ($($field:ident),*) => {
            $(if over.$field.is_some() {
                base.$field = over.$field;
            })*
        };
    }
    take!(name, api, base_url, context_window, max_output_tokens, reasoning, thinking, default_thinking, thinking_budgets, input, catalog_thinking);
    if over.cost.is_some() || over.currency.is_some() {
        base.cost = over.cost;
        base.currency = over.currency;
    }
    if let Some(levels) = over.thinking_levels {
        base.thinking_levels.get_or_insert_with(BTreeMap::new).extend(levels);
    }
    if let Some(compat) = over.compat {
        base.compat = Some(base.compat.clone().unwrap_or_default().merged(&compat));
    }
}

fn resolve(file: CatalogFile, origin: &str) -> Result<Catalog, CatalogError> {
    let mut providers = BTreeMap::new();
    for (id, p) in file.providers {
        let invalid = |message: &str| CatalogError::Invalid {
            origin: origin.to_string(),
            provider: id.clone(),
            message: message.to_string(),
        };
        let api = p.api.ok_or_else(|| invalid("missing `api`"))?;
        let base_url = p.base_url.ok_or_else(|| invalid("missing `base_url`"))?;
        let auth = match p.auth.as_deref() {
            Some("none") => AuthConfig::None,
            Some(other) => return Err(invalid(&format!("unknown auth '{other}' (only \"none\" is supported)"))),
            None => match p.api_key_command {
                Some(command) => AuthConfig::Command { command, ttl_secs: 300 },
                None => AuthConfig::ApiKey {
                    env: p.api_key_env.map(StringOrList::into_vec).unwrap_or_default(),
                    value: p.api_key,
                },
            },
        };
        let headers = p.headers.unwrap_or_default();
        let currency = p.currency.unwrap_or_default();
        let compat = p.compat.unwrap_or_default();
        let entries = p.models.unwrap_or_default();
        let default_path = match api.as_str() {
            "anthropic-messages" => Some("/v1/models"),
            "openai-chat" => Some("/models"),
            _ => None,
        };
        let path = match p.list_models {
            Some(ListModelsToml::Enabled(false)) => None,
            Some(ListModelsToml::Path(path)) => Some(path),
            Some(ListModelsToml::Enabled(true)) | None => default_path.map(str::to_string),
        };
        let config = ProviderConfig {
            plan: p.plan,
            name: p.name.unwrap_or_else(|| id.clone()),
            names: Default::default(),
            id: id.clone(),
            api,
            base_url,
            auth,
            headers,
            currency,
            compat,
            listing: Listing {
                catalog_id: p.catalog_id,
                path: if p.list_from.is_some() { None } else { path },
                from: p.list_from,
                details: p.list_details,
                entries,
            },
        };
        providers.insert(id, config);
    }
    Ok(Catalog { providers, warnings: Vec::new() })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::ThinkingFormat;

    #[test]
    fn builtin_catalog_parses() {
        assert_eq!(builtin_file().unwrap(), parse(BUILTIN_CATALOG, "builtin catalog").unwrap());
        let catalog = Catalog::builtin().expect("builtin catalog");
        let deepseek = &catalog.providers["deepseek"];
        assert_eq!(deepseek.api, "openai-chat");
        // Details come from the public catalog under the built-in corrections.
        let flash = crate::directory::ModelDirectory::new(None).model(deepseek, "deepseek-flash").expect("deepseek-flash");
        assert!(flash.reasoning);
        assert_eq!(flash.name, "DeepSeek-V4.1-Flash");
        assert_eq!(flash.compat.thinking_format, Some(ThinkingFormat::Deepseek));
        assert_eq!(flash.currency, Currency::Usd, "catalog prices are dollars");
        assert_eq!(catalog.providers["deepseek-anthropic"].listing.from.as_deref(), Some("deepseek"));
        assert_eq!(catalog.providers["deepseek-anthropic"].listing.path, None);
        assert_eq!(catalog.providers["ollama"].listing.path.as_deref(), Some("/models"));
        assert_eq!(catalog.providers["anthropic"].listing.path.as_deref(), Some("/v1/models"));
        // Every catalog id and every maker has a compiled-in snapshot.
        let ids = catalog.providers.values().filter_map(|p| p.listing.catalog_id.as_deref());
        for id in ids.chain(crate::public_catalog::MAKERS.iter().copied()) {
            assert!(crate::public_catalog::embedded(id).is_some(), "{id}");
        }
    }

    #[test]
    fn subscription_products_keep_routes_and_credentials_separate() {
        let catalog = Catalog::builtin().unwrap();
        let plans: Vec<_> = catalog.providers.values().filter(|p| p.plan.is_some()).collect();
        assert_eq!(plans.len(), 6);
        let mut envs = std::collections::BTreeSet::new();
        for p in &plans {
            assert!(p.auth.env_vars().iter().all(|env| envs.insert(env.clone())));
            assert!(p.base_url.contains("/plan/") || p.base_url.contains("/coding/") || p.base_url.contains("token-plan."));
            assert!(p.listing.from.is_none(), "a plan never borrows a pay-as-you-go list");
        }
        for p in catalog.providers.values().filter(|p| p.plan.is_none()) {
            assert!(p.auth.env_vars().iter().all(|env| !envs.contains(env)));
        }
        assert_eq!(catalog.providers["bailian-token-plan"].listing.path.as_deref(), Some("/models"));
        assert!(plans.iter().filter(|p| p.id != "bailian-token-plan").all(|p| p.listing.path.is_none()));
        let directory = crate::directory::ModelDirectory::new(None);
        let glm = directory.models(&catalog.providers["zhipu-coding-plan"]);
        assert_eq!(glm.models.len(), 2);
        assert!(glm.get("glm-5.3").unwrap().model.reasoning);
        assert!(glm.get("glm-5.3-flash").unwrap().model.input.contains(&Modality::Image));
        assert!(glm.models.iter().all(|m| !m.model.cost.is_configured()));
    }

    #[test]
    fn user_file_overrides_and_extends() {
        let mut base = parse(BUILTIN_CATALOG, "builtin").unwrap();
        let user = parse(
            r#"
            [providers.deepseek]
            base_url = "http://proxy.local/v1"

            [[providers.deepseek.models]]
            id = "deepseek-flash"
            context_window = 1000

            [providers.my-vllm]
            api = "openai-chat"
            base_url = "http://10.0.0.5:8000/v1"
            api_key_env = "MY_VLLM_KEY"
            compat = { max_tokens_field = "max_tokens" }

            [[providers.my-vllm.models]]
            id = "qwen3-coder"
            "#,
            "user",
        )
        .unwrap();
        merge(&mut base, user);
        let catalog = resolve(base, "test").unwrap();
        let flash = crate::directory::ModelDirectory::new(None)
            .model(&catalog.providers["deepseek"], "deepseek-flash")
            .unwrap();
        assert_eq!(flash.base_url, "http://proxy.local/v1");
        assert_eq!(flash.context_window, 1000);
        // Fields the user did not set keep the values of the layers below.
        assert!(flash.reasoning);
        assert_eq!(flash.name, "DeepSeek-V4.1-Flash");
        assert_eq!(flash.max_output_tokens, Some(393_216));
        assert_eq!(flash.thinking, [ThinkingLevel::Off, ThinkingLevel::Low, ThinkingLevel::High, ThinkingLevel::Max]);
        assert_eq!(flash.compat.thinking_format, Some(ThinkingFormat::Deepseek));
        let vllm = crate::directory::ModelDirectory::new(None).model(&catalog.providers["my-vllm"], "qwen3-coder").unwrap();
        assert_eq!(vllm.max_output_tokens, None, "not known: left to the provider");
        assert_eq!(
            catalog.providers["my-vllm"].auth,
            AuthConfig::ApiKey {
                env: vec!["MY_VLLM_KEY".into()],
                value: None
            }
        );
    }

    #[test]
    fn rejects_unknown_fields() {
        let err = parse("[providers.x]\napi = \"openai-chat\"\nbase_ur = \"typo\"\n", "user").unwrap_err();
        assert!(err.to_string().contains("base_ur"));
    }
}
