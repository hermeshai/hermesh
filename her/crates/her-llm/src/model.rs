//! Model metadata and per-provider compatibility flags.

use serde::Deserialize;
use serde::Serialize;
use std::collections::BTreeMap;

use crate::types::Currency;
use crate::types::ThinkingLevel;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Modality {
    Text,
    Image,
}

/// Prices per million tokens, in the provider's currency.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct ModelCost {
    #[serde(default)]
    pub input: f64,
    #[serde(default)]
    pub output: f64,
    #[serde(default)]
    pub cache_read: f64,
    #[serde(default)]
    pub cache_write: f64,
}

impl ModelCost {
    pub fn is_configured(&self) -> bool {
        self.input > 0.0 || self.output > 0.0
    }
}

/// Prices that apply once a request's input (cached parts included) exceeds `above` tokens.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct CostTier {
    pub above: u64,
    #[serde(flatten)]
    pub cost: ModelCost,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SystemRole {
    System,
    Developer,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum MaxTokensField {
    MaxCompletionTokens,
    MaxTokens,
}

/// How a channel of `openai-chat` sends what is not an effort: its thinking switch and budget
/// (docs/kernel/model-catalog-design.md §18.3). Efforts go out as `reasoning_effort` whatever the
/// format. Left unset, nothing else can be sent.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ThinkingFormat {
    /// Nothing about thinking is sent, not even efforts: the channel refuses it.
    None,
    /// `reasoning_effort: "none"` switches off.
    Openai,
    /// `thinking: {type}` is the switch.
    Deepseek,
    /// `enable_thinking` is the switch, `thinking_budget` the budget.
    Qwen,
    /// Same as `deepseek`.
    Zai,
    /// All of it under `reasoning`, which the aggregator translates for each model.
    Openrouter,
}

/// What a model's row in the public catalog says about controlling its thinking.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct CatalogThinking {
    /// It can be switched on and off.
    pub switch: bool,
    /// The named efforts it takes, as her levels, without `Off`.
    pub efforts: Vec<ThinkingLevel>,
    /// `none` is one of its efforts.
    pub off_effort: bool,
    /// A token budget can be set.
    pub budget: bool,
}

/// How a model's levels other than off and on go out.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ThinkingControl {
    /// As the provider's named efforts.
    #[default]
    Effort,
    /// As a token budget.
    Budget,
    /// The model takes either; each wire protocol knows which to use.
    EffortOrBudget,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AuthHeader {
    XApiKey,
    Bearer,
}

/// Differences between implementations of the same wire protocol, expressed as data.
/// Merge order: adapter defaults → provider → model (field by field).
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Compat {
    // openai-chat
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub system_role: Option<SystemRole>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_tokens_field: Option<MaxTokensField>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub stream_usage: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub thinking_format: Option<ThinkingFormat>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub replay_thinking: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_result_name: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_result_images: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub prompt_cache_key: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub strict_tools: Option<bool>,
    // anthropic-messages
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub auth_header: Option<AuthHeader>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cache_control: Option<bool>,
    // shared
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub context_overflow_patterns: Option<Vec<String>>,
}

impl Compat {
    /// Returns `self` with every field set in `over` replaced.
    pub fn merged(&self, over: &Compat) -> Compat {
        Compat {
            system_role: over.system_role.or(self.system_role),
            max_tokens_field: over.max_tokens_field.or(self.max_tokens_field),
            stream_usage: over.stream_usage.or(self.stream_usage),
            thinking_format: over.thinking_format.or(self.thinking_format),
            replay_thinking: over.replay_thinking.or(self.replay_thinking),
            tool_result_name: over.tool_result_name.or(self.tool_result_name),
            tool_result_images: over.tool_result_images.or(self.tool_result_images),
            prompt_cache_key: over.prompt_cache_key.or(self.prompt_cache_key),
            strict_tools: over.strict_tools.or(self.strict_tools),
            auth_header: over.auth_header.or(self.auth_header),
            cache_control: over.cache_control.or(self.cache_control),
            context_overflow_patterns: over
                .context_overflow_patterns
                .clone()
                .or_else(|| self.context_overflow_patterns.clone()),
        }
    }

    pub fn overflow_patterns(&self) -> Vec<String> {
        self.context_overflow_patterns.clone().unwrap_or_default()
    }
}

/// Token budgets for budget-style reasoning (anthropic, qwen). Set in models.toml, they make
/// the levels of a model the catalog does not describe go out as budgets.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct ThinkingBudgets {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub minimal: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub low: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub medium: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub high: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub xhigh: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max: Option<u32>,
}

/// Runtime view of a model, with provider settings already merged in.
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct Model {
    pub id: String,
    pub name: String,
    pub provider: String,
    pub api: String,
    pub base_url: String,
    pub context_window: u32,
    /// `None`: not known. Requests leave the limit to the provider where they can.
    pub max_output_tokens: Option<u32>,
    pub reasoning: bool,
    /// The levels it offers, in `ThinkingLevel::ALL` order, never `Default`. Empty when it cannot
    /// be told how hard to think.
    pub thinking: Vec<ThinkingLevel>,
    /// The value sent for a level when it is not the level's own name. `off` here means the model
    /// is switched off with that effort value, not with the channel's switch.
    pub thinking_levels: BTreeMap<String, String>,
    /// The level a session starts with on this model (models.toml `default_thinking`).
    pub default_thinking: ThinkingLevel,
    pub thinking_budgets: ThinkingBudgets,
    pub thinking_control: ThinkingControl,
    pub input: Vec<Modality>,
    pub cost: ModelCost,
    /// Higher prices for long inputs, ascending by `above`.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub cost_tiers: Vec<CostTier>,
    pub currency: Currency,
    pub compat: Compat,
    pub headers: BTreeMap<String, String>,
}

pub const DEFAULT_CONTEXT_WINDOW: u32 = 128_000;
/// For protocols that require an output limit, when the model's is not known.
pub const REQUIRED_MAX_OUTPUT_TOKENS: u32 = 8_192;
/// The most output a request asks for by default. Also the room kept for a reply when the
/// model's limit is not known, since such a request sets no limit.
pub const REQUEST_MAX_OUTPUT_TOKENS: u32 = 32_000;

impl Model {
    /// `provider/model_id`
    pub fn reference(&self) -> String {
        format!("{}/{}", self.provider, self.id)
    }

    pub fn accepts_images(&self) -> bool {
        self.input.contains(&Modality::Image)
    }

    /// Whether `level` can be asked of this model; `Default` always can.
    pub fn offers(&self, level: ThinkingLevel) -> bool {
        level == ThinkingLevel::Default || self.thinking.contains(&level)
    }

    /// The level a request carries: `level` when this model offers it, else `Default`, which
    /// sends nothing and leaves it to the provider.
    pub fn resolve_thinking(&self, level: ThinkingLevel) -> ThinkingLevel {
        if self.offers(level) { level } else { ThinkingLevel::Default }
    }

    /// The value an effort level is sent as: its own name unless models.toml says otherwise.
    pub fn thinking_value(&self, level: ThinkingLevel) -> String {
        self.thinking_levels
            .get(level.as_str())
            .filter(|v| !v.is_empty())
            .cloned()
            .unwrap_or_else(|| level.as_str().to_string())
    }

    /// The effort value that switches this model off, when that is how it is done.
    pub fn off_effort(&self) -> Option<&str> {
        self.thinking_levels.get(ThinkingLevel::Off.as_str()).map(String::as_str).filter(|v| !v.is_empty())
    }

    /// Token budget for budget-style reasoning, capped so the reply itself keeps room. `On` and
    /// `Default` think at the medium budget.
    pub fn thinking_budget(&self, level: ThinkingLevel) -> u32 {
        let b = &self.thinking_budgets;
        let budget = match level {
            ThinkingLevel::Off => return 0,
            ThinkingLevel::Minimal => b.minimal.unwrap_or(1_024),
            ThinkingLevel::Low => b.low.unwrap_or(4_096),
            ThinkingLevel::Default | ThinkingLevel::On | ThinkingLevel::Medium => b.medium.unwrap_or(10_240),
            ThinkingLevel::High => b.high.unwrap_or(24_576),
            ThinkingLevel::Xhigh => b.xhigh.unwrap_or(28_672),
            ThinkingLevel::Max => b.max.unwrap_or(32_768),
        };
        match self.max_output_tokens {
            Some(limit) => budget.min(limit.saturating_sub(4_096).max(1_024)),
            None => budget,
        }
    }

    /// Output limit of a request that does not set one; `None` leaves it to the provider.
    pub fn default_max_output(&self) -> Option<u32> {
        self.max_output_tokens.map(|limit| limit.min(REQUEST_MAX_OUTPUT_TOKENS))
    }

    /// The output limit of a request that thinks within `budget`: room for the reply after it.
    pub fn budget_max_tokens(&self, budget: u32) -> u32 {
        let wanted = budget + 16_384;
        self.max_output_tokens.map_or(wanted, |limit| limit.min(wanted).max(budget + 1))
    }

    /// The most output tokens a request at this thinking level asks for (the larger of the
    /// adapters' limits). The prompt has to leave this much of the context window free.
    pub fn output_reserve(&self, thinking: ThinkingLevel) -> u32 {
        let mut reserve = self.default_max_output().unwrap_or(REQUEST_MAX_OUTPUT_TOKENS);
        let thinking = self.resolve_thinking(thinking);
        if self.reasoning && thinking != ThinkingLevel::Off {
            reserve = reserve.max(self.budget_max_tokens(self.thinking_budget(thinking)));
        }
        reserve
    }
}

#[cfg(test)]
pub(crate) fn test_model(api: &str) -> Model {
    Model {
        id: "test-model".into(),
        name: "Test".into(),
        provider: "test".into(),
        api: api.into(),
        base_url: "http://localhost".into(),
        context_window: DEFAULT_CONTEXT_WINDOW,
        max_output_tokens: Some(64_000),
        reasoning: true,
        thinking: ThinkingLevel::EFFORTS.to_vec(),
        thinking_levels: BTreeMap::new(),
        default_thinking: ThinkingLevel::Default,
        thinking_budgets: ThinkingBudgets::default(),
        thinking_control: ThinkingControl::Effort,
        input: vec![Modality::Text],
        cost: ModelCost::default(),
        cost_tiers: Vec::new(),
        currency: Currency::Usd,
        compat: Compat::default(),
        headers: BTreeMap::new(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_offered_levels_reach_a_request() {
        let mut model = test_model("openai-chat");
        model.thinking = vec![ThinkingLevel::Off, ThinkingLevel::Low, ThinkingLevel::High];
        model.thinking_levels.insert("high".into(), "hard".into());
        assert_eq!(model.resolve_thinking(ThinkingLevel::High), ThinkingLevel::High);
        assert_eq!(model.resolve_thinking(ThinkingLevel::Medium), ThinkingLevel::Default, "never a level it lacks");
        assert_eq!(model.resolve_thinking(ThinkingLevel::Default), ThinkingLevel::Default);
        assert_eq!(model.thinking_value(ThinkingLevel::High), "hard");
        assert_eq!(model.thinking_value(ThinkingLevel::Low), "low");
    }

    #[test]
    fn budget_is_capped_by_output_limit() {
        let mut model = test_model("anthropic-messages");
        model.max_output_tokens = Some(16_000);
        assert_eq!(model.thinking_budget(ThinkingLevel::Max), 11_904);
        assert_eq!(model.thinking_budget(ThinkingLevel::Low), 4_096);
    }

    #[test]
    fn an_unknown_output_limit_is_left_to_the_provider() {
        let mut model = test_model("openai-chat");
        model.max_output_tokens = None;
        assert_eq!(model.default_max_output(), None);
        assert_eq!(model.thinking_budget(ThinkingLevel::Max), 32_768, "nothing to cap it by");
        assert_eq!(model.output_reserve(ThinkingLevel::Off), REQUEST_MAX_OUTPUT_TOKENS, "room for a long reply");
        assert_eq!(model.output_reserve(ThinkingLevel::Max), 32_768 + 16_384);
    }

    #[test]
    fn compat_merge_prefers_override() {
        let base = Compat {
            stream_usage: Some(true),
            thinking_format: Some(ThinkingFormat::Openai),
            ..Default::default()
        };
        let over = Compat {
            thinking_format: Some(ThinkingFormat::Deepseek),
            ..Default::default()
        };
        let merged = base.merged(&over);
        assert_eq!(merged.stream_usage, Some(true));
        assert_eq!(merged.thinking_format, Some(ThinkingFormat::Deepseek));
    }
}
