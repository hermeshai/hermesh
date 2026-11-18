//! Model names as gateways spell them, and the spellings the public catalog may use for the same
//! model (docs/kernel/custom-endpoint-design.md §6.3). These only produce candidates to look up;
//! a request always says the channel's own name.

include!("../catalog/name_rules.rs");

#[cfg(test)]
mod tests {
    use super::*;

    fn has(list: &[String], want: &str) -> bool {
        list.iter().any(|v| v == want)
    }

    #[test]
    fn gateway_spellings_reach_the_catalogs() {
        let cases = [
            ("us.anthropic.claude-opus-4-1-20250805-v1:0", "claude-opus-4-1-20250805"),
            ("openai.gpt-oss-20b-1:0", "gpt-oss-20b"),
            ("claude-sonnet-4-6@default", "claude-sonnet-4-6"),
            ("google/gemma-4-26b-a4b@q4_k_m", "gemma-4-26b-a4b"),
            ("accounts/fireworks/models/qwen3p8-max", "qwen3.8-max"),
            ("fireworks_ai/accounts/fireworks/models/deepseek-v3p1", "deepseek-v3.1"),
            ("qwen3:32b", "qwen3-32b"),
            ("llama3.2:latest", "llama3.2"),
            ("gpt-oss:20b", "gpt-oss-20b"),
            ("@cf/openai/gpt-oss-120b", "gpt-oss-120b"),
            ("openai/gpt-5.2:nitro:exacto", "openai/gpt-5.2"),
            ("Qwen/Qwen3-235B-A22B-Instruct-2507", "qwen3-235b-a22b-instruct-2507"),
            ("Qwen3-30B-A3B-Q4_K_M.gguf", "qwen3-30b-a3b"),
            ("nvidia/NVIDIA-Nemotron-3.5-Super-VL-120B-A12B-NVFP4", "nvidia-nemotron-3.5-super-vl-120b-a12b"),
            ("anthropic/claude-sonnet-4.5", "claude-sonnet-4-5"),
            ("claude-4.5-sonnet", "claude-sonnet-4-5"),
            ("claude-4-5-sonnet", "claude-sonnet-4-5"),
            ("claude-sonnet-4-5", "claude-sonnet-4.5"),
            ("deepseek/deepseek-v4-pro:free", "deepseek-v4-pro"),
        ];
        for (id, want) in cases {
            let v = variants(id);
            assert!(has(&v, want), "{id} should try {want}: {v:?}");
        }
    }

    #[test]
    fn dates_stay_dates() {
        assert!(!has(&variants("glm-4-20250414"), "glm-4.20250414"), "a dash before a date stays");
        assert!(has(&variants("gemini-2-5-flash"), "gemini-2.5-flash"));
    }

    #[test]
    fn a_name_is_tried_as_given_first() {
        assert_eq!(variants("kimi-k2-thinking")[0], "kimi-k2-thinking");
        assert!(!has(&variants("kimi-k2-thinking"), "kimi-k2"), "a suffix that is part of the name stays");
        assert!(has(&loose("kimi-k2-thinking"), "kimi-k2"), "dropping it is only a weak match");
    }

    #[test]
    fn squashed_names_meet() {
        assert_eq!(squash("claude-opus4-7"), squash("anthropic/claude-opus-4-7"));
        assert_eq!(squash("openai-gpt-61-terra"), squash("openai/gpt-6.1-terra"));
        assert_eq!(squash("us.meta.llama3-3-70b-instruct-v1:0"), squash("meta/llama-3.3-70b-instruct"));
        assert_ne!(squash("qwen3-32b"), squash("qwen3-235b"));
    }

    #[test]
    fn weak_spellings() {
        let cases = [
            ("openai/gpt-5.4-fast", "gpt-5.4"),
            ("claude-sonnet-4-6-think", "claude-sonnet-4-6"),
            ("gpt-4o-2024-08-06", "gpt-4o"),
            ("claude-sonnet-4-5-20250929", "claude-sonnet-4-5"),
            ("gemini-2.5-flash-preview-05-20", "gemini-2.5-flash-preview"),
            ("gemini-2.5-computer-use-preview-10-2025", "gemini-2.5-computer-use-preview"),
            ("seed-2-0-mini-260428", "seed-2-0-mini"),
            ("coding-glm-5.3", "glm-5.3"),
            ("qwen3-max@effort:high", "qwen3-max"),
            ("openai/gpt-oss-120b:groq", "openai/gpt-oss-120b"),
            ("Qwen3.6-35B-A3B-fp8-no-thinking", "qwen3.6-35b-a3b-fp8"),
            ("moonshotai/Kimi-K3-TEE", "kimi-k3"),
        ];
        for (id, want) in cases {
            let v = loose(id);
            assert!(has(&v, want), "{id} should weakly try {want}: {v:?}");
        }
        assert!(!has(&variants("qwen3-max@effort:high"), "qwen3-max"), "a setting after @ is only dropped weakly");
    }
}
