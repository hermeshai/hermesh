//! Cost calculation and usage aggregation.

use schemars::JsonSchema;
use serde::Deserialize;
use serde::Serialize;
use std::collections::BTreeMap;

use crate::model::Model;
use crate::types::Cost;
use crate::types::Currency;
use crate::types::Usage;

/// Fills `usage.cost` from the model's prices.
pub fn apply_cost(model: &Model, usage: &mut Usage) {
    let prompt = usage.input + usage.cache_read + usage.cache_write;
    let price = model
        .cost_tiers
        .iter()
        .rev()
        .find(|tier| prompt > tier.above)
        .map_or(&model.cost, |tier| &tier.cost);
    let part = |tokens: u64, per_million: f64| tokens as f64 * per_million / 1_000_000.0;
    let mut cost = Cost {
        currency: model.currency,
        input: part(usage.input, price.input),
        output: part(usage.output, price.output),
        cache_read: part(usage.cache_read, price.cache_read),
        cache_write: part(usage.cache_write, price.cache_write),
        total: 0.0,
    };
    cost.total = cost.input + cost.output + cost.cache_read + cost.cache_write;
    usage.cost = cost;
}

/// Usage summed per model (`provider/model`).
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct UsageTotals {
    #[serde(default)]
    pub per_model: BTreeMap<String, Usage>,
}

impl UsageTotals {
    pub fn add(&mut self, model_ref: &str, usage: &Usage) {
        self.per_model.entry(model_ref.to_string()).or_default().add(usage);
    }

    pub fn merge(&mut self, other: &UsageTotals) {
        for (model, usage) in &other.per_model {
            self.add(model, usage);
        }
    }

    /// Token counts summed over all models (cost fields are not meaningful across currencies).
    pub fn tokens(&self) -> Usage {
        let mut total = Usage::default();
        for usage in self.per_model.values() {
            total.input += usage.input;
            total.output += usage.output;
            total.cache_read += usage.cache_read;
            total.cache_write += usage.cache_write;
            if let Some(r) = usage.reasoning {
                total.reasoning = Some(total.reasoning.unwrap_or(0) + r);
            }
        }
        total
    }

    pub fn cost_by_currency(&self) -> BTreeMap<Currency, f64> {
        let mut out = BTreeMap::new();
        for usage in self.per_model.values() {
            if usage.cost.total > 0.0 {
                *out.entry(usage.cost.currency).or_insert(0.0) += usage.cost.total;
            }
        }
        out
    }

    /// True when some tokens were used by a model without configured prices.
    pub fn has_unpriced_usage(&self) -> bool {
        self.per_model.values().any(|u| !u.is_empty() && u.cost.total == 0.0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::ModelCost;
    use crate::model::test_model;

    #[test]
    fn long_inputs_use_the_tier_they_reach() {
        let mut model = test_model("openai-chat");
        model.cost = ModelCost { input: 1.0, output: 2.0, ..Default::default() };
        model.cost_tiers = vec![crate::model::CostTier { above: 200_000, cost: ModelCost { input: 2.0, output: 4.0, ..Default::default() } }];
        let mut short = Usage { input: 100_000, cache_read: 100_000, output: 1_000_000, ..Default::default() };
        apply_cost(&model, &mut short);
        assert_eq!(short.cost.output, 2.0, "200,000 is not above the tier");
        let mut long = Usage { input: 150_000, cache_read: 60_000, output: 1_000_000, ..Default::default() };
        apply_cost(&model, &mut long);
        assert_eq!(long.cost.output, 4.0);
    }

    #[test]
    fn computes_cost_per_million() {
        let mut model = test_model("openai-chat");
        model.cost = ModelCost {
            input: 2.0,
            output: 8.0,
            cache_read: 0.5,
            cache_write: 0.0,
        };
        model.currency = Currency::Cny;
        let mut usage = Usage {
            input: 1_000_000,
            output: 500_000,
            cache_read: 2_000_000,
            ..Default::default()
        };
        apply_cost(&model, &mut usage);
        assert_eq!(usage.cost.total, 2.0 + 4.0 + 1.0);
        assert_eq!(usage.cost.currency, Currency::Cny);

        let mut totals = UsageTotals::default();
        totals.add("test/m", &usage);
        totals.add("test/m", &usage);
        assert_eq!(totals.tokens().input, 2_000_000);
        assert_eq!(totals.cost_by_currency()[&Currency::Cny], 14.0);
        assert!(!totals.has_unpriced_usage());
    }
}
