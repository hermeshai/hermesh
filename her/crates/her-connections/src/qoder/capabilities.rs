//! Qoder App catalog declarations, not assertions of upstream model capabilities.
//! Missing fields remain unknown; only the output limit uses the App's documented-in-code
//! 32,000 fallback (worker KT). Live observations are kept separately in the evidence report.
use her_llm::ThinkingLevel;
use her_protocol::api::ConnectionModel;
use serde_json::Value;

fn positive(value: &Value) -> Option<u32> {
    value
        .as_u64()
        .or_else(|| value.as_str()?.parse().ok())
        .and_then(|v| u32::try_from(v).ok())
        .filter(|v| *v > 0)
}

fn level(value: &str) -> Option<ThinkingLevel> {
    match value {
        "none" | "off" => Some(ThinkingLevel::Off),
        "low" => Some(ThinkingLevel::Low),
        "medium" => Some(ThinkingLevel::Medium),
        "high" => Some(ThinkingLevel::High),
        "xhigh" => Some(ThinkingLevel::Xhigh),
        "max" => Some(ThinkingLevel::Max),
        _ => None,
    }
}

pub(super) fn blocked_control(id: &str, level: ThinkingLevel) -> bool {
    // 2026-11-03: both gateways returned reasoning for none + enable_thinking=false,
    // also for the native disabled switch and a zero budget. Retain the declaration
    // in the evidence report, but do not advertise a working off switch.
    level == ThinkingLevel::Off && matches!(id, "dmodel" | "dfmodel")
}

pub(super) fn verified_temperature(id: &str, temperature: Option<f32>) -> bool {
    // App HTTP probes: both Kimi routes accept omitted/1.0. With a 4096 budget,
    // 0.25 and 0.5 return provider_error; 0.25 also fails with a 32000 budget.
    // Gate this adapter to verified values; other values remain unknown.
    !matches!(id, "kmodel" | "kmodel_latest") || temperature.is_none_or(|v| v == 1.0)
}

pub(super) fn model(m: &Value) -> Option<ConnectionModel> {
    let id = m["key"].as_str().filter(|id| crate::valid_id(id))?;
    let config = &m["thinking_config"];
    let efforts = m
        .get("efforts")
        .or_else(|| config.get("efforts"))
        .or_else(|| config.get("enabled")?.get("efforts"));
    let mut thinking = Vec::new();
    let mut default = ThinkingLevel::Default;
    if let Some(efforts) = efforts {
        let values: Vec<&str> = match efforts {
            Value::Object(values) => values.keys().map(String::as_str).collect(),
            Value::Array(values) => values.iter().filter_map(Value::as_str).collect(),
            Value::String(values) => values
                .split(|c: char| c == ',' || c.is_whitespace())
                .collect(),
            _ => Vec::new(),
        };
        for name in values {
            if let Some(l) = level(name) {
                if efforts[name]["is_default"] == true {
                    default = l;
                }
                if !thinking.contains(&l) {
                    thinking.push(l);
                }
            }
        }
    }
    if config
        .get("disabled")
        .is_some_and(|v| !v.is_null() && v != &Value::Bool(false))
        || m["supports_disabled"] == true
    {
        if !thinking.contains(&ThinkingLevel::Off) {
            thinking.push(ThinkingLevel::Off);
        }
        if config["disabled"]["is_default"] == true {
            default = ThinkingLevel::Off;
        }
    }
    if let Some(l) = m["default_effort"]
        .as_str()
        .and_then(level)
        .filter(|l| thinking.contains(l))
    {
        default = l;
    }
    thinking.retain(|l| !blocked_control(id, *l));
    if !thinking.contains(&default) {
        default = ThinkingLevel::Default;
    }
    thinking.sort_by_key(|l| ThinkingLevel::ALL.iter().position(|v| v == l));
    // Match the App's context selection: declared default tier wins over the legacy limit.
    // Do not silently choose the largest (possibly more expensive) available tier.
    let context_window = m["context_config"]
        .as_object()
        .and_then(|windows| {
            windows
                .values()
                .find(|v| v["is_default"] == true && positive(&v["token_count"]).is_some())
                .and_then(|v| positive(&v["token_count"]))
        })
        .or_else(|| positive(&m["default_context_window"]))
        .or_else(|| positive(&m["max_input_tokens"]));
    Some(ConnectionModel {
        id: id.into(),
        name: m["display_name"].as_str().unwrap_or(id).into(),
        names: Default::default(),
        context_window,
        max_output_tokens: Some(positive(&m["max_output_tokens"]).unwrap_or(32_000)),
        // Kimi's legacy flag is false even though its thinking_config declares levels.
        reasoning: m["is_reasoning"] == true || thinking.iter().any(|l| *l != ThinkingLevel::Off),
        images: m["is_vl"] == true,
        thinking,
        default_thinking: default,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn catalog_defaults_and_explicit_controls() {
        let entries: Vec<Value> =
            serde_json::from_str(include_str!("fixtures/catalog-2026-11-03.json")).unwrap();
        for entry in entries {
            let m = model(&entry).unwrap();
            assert_eq!(m.context_window, Some(200_000));
            assert_eq!(m.max_output_tokens, Some(32_000));
            assert!(m.images);
            if m.id.starts_with("kmodel") {
                assert!(m.reasoning);
                assert_eq!(m.default_thinking, ThinkingLevel::High);
            }
            assert_eq!(
                m.thinking.contains(&ThinkingLevel::Off),
                ["qmodel_38max", "qfmodel"].contains(&m.id.as_str())
            );
            assert!(!m.thinking.contains(&ThinkingLevel::Minimal));
        }
    }

    #[test]
    fn missing_or_future_capabilities_are_not_invented() {
        let m =
            model(&json!({"key":"future", "thinking_config":{"enabled":{"efforts":{"ultra":{}}}}}))
                .unwrap();
        assert!(!m.images && !m.reasoning && m.thinking.is_empty());
        assert_eq!(m.context_window, None);
        let m = model(&json!({"key":"a", "max_input_tokens":"8192", "max_output_tokens":"4096", "efforts":["low","high"], "default_effort":"max"})).unwrap();
        assert_eq!(m.max_output_tokens, Some(4096));
        assert_eq!(m.default_thinking, ThinkingLevel::Default);
    }
}
