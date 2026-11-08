//! What fills the context window: the measured size (see `budget`) split into parts.
//!
//! The parts before the messages (system prompt, skill catalog, tool definitions, MCP) are
//! estimated once and kept until MCP servers change them. The skill instructions in the
//! window are counted from it. The messages are the rest of the measured size, so that the
//! parts add up to it: the measured size holds the provider's own count, which the estimates
//! only approach. A report is kept until the history, the model or the fixed parts change, so
//! a client may ask as often as it likes.

use her_llm::AssistantContent;
use her_llm::Message;
use her_llm::ThinkingLevel;
use her_llm::ToolSpec;
use her_protocol::ContextPart;
use her_protocol::ContextPartKind;
use her_protocol::ContextReport;
use her_protocol::ContextUsage;
use std::collections::HashSet;
use std::sync::Arc;

use crate::budget;
use crate::context;
use crate::mcp;
use crate::session::Session;
use crate::skills;
use crate::store::Entry;
use crate::store::EntryKind;

/// Unscaled estimates of the parts of every request before the messages.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct Fixed {
    /// The system prompt without the skill catalog and the MCP section.
    pub(crate) system: u64,
    /// The skill catalog in the system prompt.
    pub(crate) catalog: u64,
    /// The MCP section of the system prompt and the MCP tools' definitions.
    pub(crate) mcp: u64,
    /// The other tools' definitions.
    pub(crate) tools: u64,
}

impl Fixed {
    /// `system_prompt` is what is sent: `base_prompt`, then `mcp_section`.
    pub(crate) fn of<'a>(system_prompt: &str, base_prompt: &str, mcp_section: Option<&str>, tools: impl IntoIterator<Item = &'a ToolSpec>) -> Fixed {
        let catalog = context::estimate_text(skills::catalog_in(base_prompt));
        let mcp_prompt = mcp_section.map_or(0, context::estimate_text);
        let mut fixed = Fixed {
            system: context::estimate_text(system_prompt).saturating_sub(catalog + mcp_prompt),
            catalog,
            mcp: mcp_prompt,
            tools: 0,
        };
        for tool in tools {
            let tokens = context::estimate_tool(tool);
            if is_mcp_tool(&tool.name) {
                fixed.mcp += tokens;
            } else {
                fixed.tools += tokens;
            }
        }
        fixed
    }

    /// The same as [`context::estimate_fixed`] of the system prompt and the tools.
    pub(crate) fn total(&self) -> u64 {
        self.system + self.catalog + self.mcp + self.tools
    }
}

/// An MCP server's tool, or one of the tools that reach deferred servers' tools.
fn is_mcp_tool(name: &str) -> bool {
    name.starts_with("mcp__") || name == mcp::tools::SEARCH_TOOL || name == mcp::tools::CALL_TOOL
}

/// Unscaled estimate of the skill instructions in the window: those the last compaction kept,
/// those sent with a message that mentions the skill, and SKILL.md files read by a tool call.
pub(crate) fn window_skills(branch: &[&Entry]) -> u64 {
    let start = context::window_start(branch);
    let text = |skills: &[crate::store::SkillText]| skills.iter().map(|s| context::estimate_text(&s.content)).sum::<u64>();
    let mut tokens = match start.checked_sub(1).map(|i| &branch[i].kind) {
        Some(EntryKind::Compaction(checkpoint)) => text(&checkpoint.skills),
        _ => 0,
    };
    let mut reads = HashSet::new();
    for entry in &branch[start..] {
        let EntryKind::Message { message, skills, .. } = &entry.kind else {
            continue;
        };
        tokens += text(skills);
        match message {
            Message::Assistant(assistant) => {
                for content in &assistant.content {
                    if let AssistantContent::ToolCall(call) = content
                        && !skills::kept::skill_files_read(call).is_empty()
                    {
                        reads.insert(call.id.as_str());
                    }
                }
            }
            Message::ToolResult(result) if !result.is_error && reads.contains(result.call_id.as_str()) => {
                tokens += context::estimate_user_content(&result.content);
            }
            _ => {}
        }
    }
    tokens
}

/// The report for a measured `usage`. Estimates are scaled by `ratio`; when they come to more
/// than the measured size, they are scaled down to it and the messages get nothing.
pub(crate) fn report(model: String, usage: ContextUsage, fixed: Fixed, skills: u64, ratio: f64, auto_compact: bool) -> ContextReport {
    let mut others = [
        (ContextPartKind::SystemPrompt, budget::scale(fixed.system, ratio)),
        (ContextPartKind::Tools, budget::scale(fixed.tools, ratio)),
        (ContextPartKind::Skills, budget::scale(fixed.catalog + skills, ratio)),
        (ContextPartKind::Mcp, budget::scale(fixed.mcp, ratio)),
    ];
    let estimated: u64 = others.iter().map(|(_, tokens)| tokens).sum();
    if estimated > usage.tokens {
        for (_, tokens) in &mut others {
            *tokens = (u128::from(*tokens) * u128::from(usage.tokens) / u128::from(estimated)) as u64;
        }
    }
    let messages = usage.tokens - others.iter().map(|(_, tokens)| tokens).sum::<u64>();
    let parts = std::iter::once((ContextPartKind::Messages, messages))
        .chain(others)
        .filter(|(kind, tokens)| *kind != ContextPartKind::Mcp || *tokens > 0)
        .map(|(kind, tokens)| ContextPart { kind, tokens })
        .collect();
    let limit = if auto_compact { usage.soft_limit } else { usage.hard_limit };
    ContextReport {
        model,
        usage,
        parts,
        free: limit.saturating_sub(usage.tokens),
        reserved: usage.window.saturating_sub(usage.tokens.max(limit)),
    }
}

/// The report as text for a terminal or a chat: the share used, then a line per part, then
/// the reserved and the free room.
pub fn text(report: &ContextReport) -> String {
    let usage = &report.usage;
    let share = |tokens: u64| match usage.window {
        0 => "—".to_string(),
        window => format!("{:.1}%", tokens as f64 * 100.0 / window as f64),
    };
    let mut out = format!(
        "Context · {}\n{} used · {} / {} tokens\n\n",
        report.model,
        share(usage.tokens),
        short_tokens(usage.tokens),
        short_tokens(usage.window)
    );
    let parts = report.parts.iter().map(|part| (part_name(part.kind), part.tokens));
    let rest = [("Reserved", report.reserved), ("Free", report.free)];
    for (name, tokens) in parts.chain(rest) {
        out.push_str(&format!("  {name:<14}{:>8}  {:>6}\n", short_tokens(tokens), share(tokens)));
    }
    if usage.estimated > 0 {
        out.push_str(&format!("\n{} of it estimated since the last reply\n", short_tokens(usage.estimated)));
    }
    out
}

fn part_name(kind: ContextPartKind) -> &'static str {
    match kind {
        ContextPartKind::Messages => "Messages",
        ContextPartKind::SystemPrompt => "System prompt",
        ContextPartKind::Tools => "Tools",
        ContextPartKind::Skills => "Skills",
        ContextPartKind::Mcp => "MCP",
    }
}

/// 950, 12.3k, 1M.
fn short_tokens(tokens: u64) -> String {
    let (value, unit) = match tokens {
        0..1_000 => return tokens.to_string(),
        1_000..1_000_000 => (tokens as f64 / 1e3, "k"),
        _ => (tokens as f64 / 1e6, "M"),
    };
    let text = format!("{value:.1}");
    format!("{}{unit}", text.strip_suffix(".0").unwrap_or(&text))
}

/// What a kept report was made from: the same again gives the same report.
#[derive(Clone, Copy, Debug, PartialEq)]
pub(crate) struct ReportKey {
    generation: u64,
    entries: usize,
    model: usize,
    thinking: ThinkingLevel,
    ratio: u64,
    fixed: Fixed,
}

impl Session {
    /// What fills the context window now; see the module docs.
    pub(crate) fn context_report(&self) -> ContextReport {
        let fixed = self.fixed();
        let key = {
            let state = self.state.lock();
            ReportKey {
                generation: state.history.branch_positions().1,
                entries: state.history.len(),
                model: Arc::as_ptr(&state.model) as usize,
                thinking: state.thinking,
                ratio: state.estimate_ratio.to_bits(),
                fixed,
            }
        };
        if let Some((kept, report)) = &*self.context_report.lock()
            && *kept == key
        {
            return report.clone();
        }
        let usage = self.budget().usage;
        let (model, skills, ratio) = {
            let state = self.state.lock();
            (state.model.reference(), window_skills(&state.history.branch()), state.estimate_ratio)
        };
        let report = report(model, usage, fixed, skills, ratio, self.config.compaction.enabled);
        *self.context_report.lock() = Some((key, report.clone()));
        report
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::store::SkillText;
    use her_llm::AssistantMessage;
    use her_llm::StopReason;
    use her_llm::ToolCall;
    use her_llm::ToolResultMessage;
    use her_llm::UserContent;
    use her_llm::UserMessage;
    use serde_json::json;
    use std::path::PathBuf;

    fn usage(tokens: u64) -> ContextUsage {
        ContextUsage {
            tokens,
            estimated: 0,
            soft_limit: 800,
            hard_limit: 900,
            window: 1000,
        }
    }

    fn fixed() -> Fixed {
        Fixed {
            system: 100,
            catalog: 20,
            mcp: 0,
            tools: 50,
        }
    }

    fn tokens(report: &ContextReport) -> Vec<(ContextPartKind, u64)> {
        report.parts.iter().map(|p| (p.kind, p.tokens)).collect()
    }

    #[test]
    fn parts_add_up_to_the_measured_size_and_fill_the_window_with_free_and_reserved() {
        let report = report("p/m".into(), usage(400), fixed(), 30, 1.0, true);
        assert_eq!(
            tokens(&report),
            [
                (ContextPartKind::Messages, 200),
                (ContextPartKind::SystemPrompt, 100),
                (ContextPartKind::Tools, 50),
                (ContextPartKind::Skills, 50),
            ],
            "no MCP part without MCP"
        );
        assert_eq!((report.free, report.reserved), (400, 200));
        let without_auto = super::report("p/m".into(), usage(400), fixed(), 30, 1.0, false);
        assert_eq!((without_auto.free, without_auto.reserved), (500, 100), "free up to the hard limit");
    }

    #[test]
    fn estimates_above_the_measured_size_shrink_to_it() {
        let report = report("p/m".into(), usage(85), fixed(), 0, 1.0, true);
        let sum: u64 = report.parts.iter().map(|p| p.tokens).sum();
        assert!(sum <= 85 && report.parts[0].tokens <= 1, "{report:?}");
        let full = super::report("p/m".into(), usage(950), fixed(), 0, 2.0, true);
        assert_eq!((full.free, full.reserved), (0, 50));
        assert_eq!(full.parts[2].tokens, 100, "scaled by the ratio");
    }

    #[test]
    fn reads_as_text() {
        let text = text(&report("p/m".into(), usage(400), fixed(), 30, 1.0, true));
        assert!(text.starts_with("Context · p/m\n40.0% used · 400 / 1k tokens\n"), "{text}");
        assert!(text.contains("  Messages           200   20.0%\n"), "{text}");
        assert!(text.contains("  Free               400   40.0%\n"), "{text}");
        assert_eq!(short_tokens(212_500), "212.5k");
        assert_eq!(short_tokens(1_000_000), "1M");
    }

    #[test]
    fn the_catalog_and_mcp_are_taken_out_of_the_system_prompt() {
        let base = "You are her.\n\n<available_skills>\nRead them.\n- a: does a\n</available_skills>\n\n<environment>\n</environment>";
        let section = "<mcp_instructions server=\"x\">\nuse x\n</mcp_instructions>";
        let system = crate::mcp::compose(base, Some(section));
        let tool = |name: &str| ToolSpec {
            name: name.into(),
            description: "d".into(),
            parameters: json!({"type": "object"}),
        };
        let fixed = Fixed::of(&system, base, Some(section), &[tool("read"), tool("mcp__x__go"), tool("tool_search")]);
        assert_eq!(fixed.catalog, context::estimate_text(skills::catalog_in(base)));
        assert!(fixed.catalog > 0 && fixed.system > 0);
        assert_eq!(fixed.tools, context::estimate_tool(&tool("read")));
        assert_eq!(
            fixed.total(),
            context::estimate_fixed(&system, &[tool("read"), tool("mcp__x__go"), tool("tool_search")]),
            "the parts add up to the estimate that measuring uses"
        );
    }

    #[test]
    fn skills_in_the_window_are_mentioned_ones_and_read_skill_files() {
        let entry = |kind: EntryKind| Entry {
            id: String::new(),
            parent: None,
            ts: 0,
            kind,
        };
        let text = |text: &str| vec![UserContent::Text { text: text.into() }];
        let call = |id: &str, name: &str, args: serde_json::Value| AssistantContent::ToolCall(ToolCall {
            id: id.into(),
            name: name.into(),
            arguments: args.into(),
            arguments_error: None,
            provider_data: None,
        });
        let result = |id: &str, body: &str, is_error: bool| {
            Message::from(ToolResultMessage {
                call_id: id.into(),
                tool_name: "read".into(),
                content: text(body),
                is_error,
                details: None,
                ts: 0,
                duration_ms: None,
            })
        };
        let skill = SkillText {
            name: "s".into(),
            path: PathBuf::from("/s/SKILL.md"),
            content: "x".repeat(40),
        };
        let entries = [
            entry(EntryKind::Message {
                message: Message::from(UserMessage { content: text("go"), ts: 0 }),
                skills: vec![skill],
                skill: None,
            }),
            entry(EntryKind::message(Message::from(AssistantMessage {
                content: vec![
                    call("1", "read", json!({"path": "/t/SKILL.md"})),
                    call("2", "read", json!({"path": "/t/notes.md"})),
                    call("3", "bash", json!({"command": "cat /u/SKILL.md"})),
                ],
                provider: "p".into(),
                api: "a".into(),
                model: "m".into(),
                response_id: None,
                usage: Default::default(),
                stop_reason: StopReason::ToolUse,
                error: None,
                ts: 0,
                duration_ms: None,
            }))),
            entry(EntryKind::message(result("1", &"y".repeat(80), false))),
            entry(EntryKind::message(result("2", &"z".repeat(400), false))),
            entry(EntryKind::message(result("3", "missing", true))),
        ];
        let branch: Vec<&Entry> = entries.iter().collect();
        assert_eq!(window_skills(&branch), 10 + 20, "the mention and the successful read of a SKILL.md");
    }
}
