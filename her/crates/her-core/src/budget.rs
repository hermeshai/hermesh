//! How full the context window is: the usage the provider reported for the last response of
//! the current model, plus an estimate of everything recorded after it. The same calculation
//! runs inside a task and after a resume, since the session log keeps every response's usage.

use her_llm::AssistantMessage;
use her_llm::ErrorKind;
use her_llm::Message;
use her_llm::Model;
use her_llm::StopReason;
use her_llm::ThinkingLevel;
use her_protocol::ContextUsage;
use her_tools::OutputLimits;
use std::sync::Arc;

use crate::config::CompactionSettings;
use crate::context;
use crate::context_report::Fixed;
use crate::session::Session;
use crate::store::Entry;

/// Range of the estimate correction factor.
const RATIO_RANGE: (f64, f64) = (0.5, 2.0);
/// Requests smaller than this say little about the tokenizer.
const MIN_CALIBRATION_TOKENS: u64 = 200;
/// A tool result may fill at most this share of the usable window.
const TOOL_RESULT_SHARE: u64 = 10;
/// Retained user messages may fill at most this share of the usable window.
const RETAINED_SHARE: u64 = 15;
/// The instructions of skills kept through a compaction may fill at most this share.
const SKILL_SHARE: u64 = 10;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Limits {
    /// Compaction starts here.
    pub(crate) soft: u64,
    /// The window minus the room kept for the reply.
    pub(crate) hard: u64,
    pub(crate) window: u64,
}

pub(crate) fn limits(model: &Model, thinking: ThinkingLevel, settings: &CompactionSettings) -> Limits {
    let window = u64::from(model.context_window);
    let reserve = u64::from(model.output_reserve(thinking));
    // A misconfigured output limit must not leave no room at all.
    let hard = window.saturating_sub(reserve).max(window / 2);
    let mut soft = (hard as f64 * settings.threshold) as u64;
    if settings.token_limit > 0 {
        soft = soft.min(settings.token_limit);
    }
    Limits { soft, hard, window }
}

/// Token limits for tool results under these window limits.
pub(crate) fn tool_limits(base: &OutputLimits, limits: &Limits) -> OutputLimits {
    base.capped((limits.hard * TOOL_RESULT_SHARE / 100) as usize)
}

/// Token budget for the user messages kept word for word by a compaction.
pub(crate) fn retained_budget(settings: &CompactionSettings, limits: &Limits) -> u64 {
    settings.keep_user_tokens.min(limits.hard * RETAINED_SHARE / 100)
}

/// Token budget for the instructions of the skills in use that a compaction keeps.
pub(crate) fn skill_budget(settings: &CompactionSettings, limits: &Limits) -> u64 {
    settings.keep_skill_tokens.min(limits.hard * SKILL_SHARE / 100)
}

#[derive(Clone, Debug)]
pub(crate) struct Budget {
    pub(crate) usage: ContextUsage,
    /// Room the next turn may need: as much as the largest turn of this window added (a reply
    /// with its tool results), plus a handoff request in case it has to be summarized.
    pub(crate) next_turn: u64,
    /// The last request of this window failed as too large.
    pub(crate) full: bool,
    /// The model of the last response, when it is not the current one (`provider/model`).
    pub(crate) previous_model: Option<String>,
}

impl Budget {
    /// Compact before the next request: the context reached the soft limit, or it is past half
    /// the window and one more turn as large as the largest so far would leave no room for the
    /// summary request. Compacting a turn early keeps that request inside the window.
    pub(crate) fn needs_compaction(&self) -> bool {
        let u = &self.usage;
        u.tokens >= u.soft_limit || (u.tokens >= u.hard_limit / 2 && u.tokens + self.next_turn >= u.hard_limit)
    }
}

/// Inputs that do not come from the log.
pub(crate) struct Measure<'a> {
    pub(crate) model: &'a Model,
    pub(crate) limits: Limits,
    /// Estimate of the system prompt and the tool definitions.
    pub(crate) fixed: u64,
    /// Correction factor for estimates, from [`calibrate`].
    pub(crate) ratio: f64,
}

pub(crate) fn scale(tokens: u64, ratio: f64) -> u64 {
    (tokens as f64 * ratio).ceil() as u64
}

/// Measures the current context of `branch`.
#[cfg(test)]
pub(crate) fn measure(branch: &[&Entry], m: &Measure) -> Budget {
    measure_with(branch, m, |window, _, model| scan_turns(window, model))
}

/// [`measure`], with `turns` giving [`scan_turns`] of the window (the branch from its start
/// index on) for the model.
pub(crate) fn measure_with(branch: &[&Entry], m: &Measure, turns: impl FnOnce(&[&Entry], usize, &Model) -> Turns) -> Budget {
    let start = context::window_start(branch);
    let mut tail = 0u64;
    let mut baseline = None;
    let mut full = None;
    let mut previous_model = None;
    for entry in branch[start..].iter().rev() {
        let Some(message) = context::entry_message(&entry.kind, entry.ts) else {
            continue;
        };
        if let Message::Assistant(a) = &*message {
            full.get_or_insert(
                a.stop_reason == StopReason::Error && a.error.as_ref().is_some_and(|e| e.kind == ErrorKind::ContextOverflow),
            );
            match reported_by(a, m.model) {
                Reported::NotInContext => continue,
                Reported::No => {}
                Reported::ThisModel => {
                    baseline = Some(a.usage.context_tokens());
                    break;
                }
                Reported::OtherModel => {
                    previous_model = Some(format!("{}/{}", a.provider, a.model));
                    break;
                }
            }
        }
        tail += context::estimate_message(&message);
    }
    // What is added turn by turn may count differently from the whole request, which the
    // system prompt and the tool definitions dominate: this window's turns tell how.
    let turns = turns(&branch[start..], start, m.model);
    let ratio = turns.ratio.filter(|_| baseline.is_some()).unwrap_or(m.ratio);
    let (tokens, estimated) = match baseline {
        Some(reported) => {
            let estimated = scale(tail, ratio);
            (reported + estimated, estimated)
        }
        None => {
            // Nothing reported for this model yet: estimate the whole request.
            let messages = context::normalize(context::branch_messages(branch));
            let estimated = scale(m.fixed + context::estimate_tokens(&messages), m.ratio);
            (estimated, estimated)
        }
    };
    let handoff = context::estimate_text(context::HANDOFF_REQUEST) + context::MESSAGE_OVERHEAD;
    let next_turn = scale(turns.largest + handoff, ratio);
    let full = full.unwrap_or(false);
    let tokens = if full { tokens.max(m.limits.hard) } else { tokens };
    Budget {
        usage: ContextUsage {
            tokens,
            estimated,
            soft_limit: m.limits.soft,
            hard_limit: m.limits.hard,
            window: m.limits.window,
        },
        next_turn,
        full,
        previous_model,
    }
}

/// What a reply in the window says about the usage of its request.
enum Reported {
    /// A failed response: not part of the context.
    NotInContext,
    /// In the context, with no usage to go by (an interrupted reply, say).
    No,
    ThisModel,
    OtherModel,
}

fn reported_by(a: &AssistantMessage, model: &Model) -> Reported {
    if a.stop_reason == StopReason::Error {
        return Reported::NotInContext;
    }
    if !has_usage(a) {
        return Reported::No;
    }
    if a.provider == model.provider && a.api == model.api && a.model == model.id {
        Reported::ThisModel
    } else {
        Reported::OtherModel
    }
}

/// A finished response with the usage of its request.
fn has_usage(a: &AssistantMessage) -> bool {
    matches!(a.stop_reason, StopReason::Stop | StopReason::ToolUse | StopReason::Length) && !a.usage.is_empty()
}

/// What the turns of a window show.
#[derive(Clone, Copy, Debug, PartialEq)]
pub(crate) struct Turns {
    /// The most that one turn added: a reply with the tool results and messages that follow
    /// it. Turns differ (a long command output, then a short read), so the largest one is the
    /// safer guess for the next.
    pub(crate) largest: u64,
    /// How the provider counted what the turns added, over its estimate: the growth from the
    /// request of the first response of the model to the request of its last, over the
    /// estimate of everything from that first response up to the last. Weighted by size, so a
    /// short turn between long ones does not hide how the long ones count. `None` when there
    /// is too little to tell, or the ratio is out of range: then more than the messages
    /// changed (the system prompt, say).
    pub(crate) ratio: Option<f64>,
}

/// [`Turns`] of `window` for `model`.
#[cfg(test)]
fn scan_turns(window: &[&Entry], model: &Model) -> Turns {
    let mut scan = TurnScan::default();
    window.iter().for_each(|entry| scan.add(entry));
    scan.turns(model)
}

/// [`scan_turns`] as a fold that continues as the window grows. Measuring estimated the whole
/// window since the last compaction, at least twice per turn; now only new entries.
#[derive(Default)]
pub(crate) struct TurnScan {
    /// The walk of the history, and the position of the window's first entry, it is for.
    generation: u64,
    first: Option<usize>,
    scanned: usize,
    largest: u64,
    current: u64,
    counted: Option<Counted>,
}

/// The responses of one model in a row, from the first one with usage.
struct Counted {
    provider: String,
    api: String,
    model: String,
    /// The prompt tokens of the first response's request and of the last one's.
    first: u64,
    last: u64,
    /// Estimate of everything from the first response up to the last.
    estimated: u64,
    /// Estimate of what came since the last response, that reply included.
    pending: u64,
}

impl TurnScan {
    fn add(&mut self, entry: &Entry) {
        let Some(message) = context::entry_message(&entry.kind, entry.ts) else {
            return;
        };
        let estimate = match &*message {
            Message::Assistant(a) if a.stop_reason == StopReason::Error => return,
            Message::Assistant(a) => {
                let estimate = context::estimate_message(&message);
                self.largest = self.largest.max(self.current);
                self.current = estimate;
                if has_usage(a) {
                    self.count(a, estimate);
                    return;
                }
                estimate
            }
            other => {
                let estimate = context::estimate_message(other);
                self.current += estimate;
                estimate
            }
        };
        if let Some(counted) = &mut self.counted {
            counted.pending += estimate;
        }
    }

    /// A response with usage: it extends the run of its model, or starts a new run.
    fn count(&mut self, a: &AssistantMessage, estimate: u64) {
        let prompt = a.usage.prompt_tokens();
        match &mut self.counted {
            Some(c) if c.provider == a.provider && c.api == a.api && c.model == a.model => {
                c.estimated += c.pending;
                c.last = prompt;
                c.pending = estimate;
            }
            _ => {
                self.counted = Some(Counted {
                    provider: a.provider.clone(),
                    api: a.api.clone(),
                    model: a.model.clone(),
                    first: prompt,
                    last: prompt,
                    estimated: 0,
                    pending: estimate,
                });
            }
        }
    }

    fn turns(&self, model: &Model) -> Turns {
        let ratio = self.counted.as_ref().and_then(|c| {
            let same = c.provider == model.provider && c.api == model.api && c.model == model.id;
            if !same || c.estimated < MIN_CALIBRATION_TOKENS {
                return None;
            }
            let ratio = c.last.checked_sub(c.first)? as f64 / c.estimated as f64;
            (RATIO_RANGE.0..=RATIO_RANGE.1).contains(&ratio).then_some(ratio)
        });
        Turns {
            largest: self.largest.max(self.current),
            ratio,
        }
    }

    /// [`scan_turns`] of `window`, whose entries are at `positions` of the history found by
    /// walk `generation`: the fold goes on from where it stopped while the window is the same
    /// one, grown.
    fn scan(&mut self, window: &[&Entry], positions: &[usize], generation: u64, model: &Model) -> Turns {
        let same = self.generation == generation && self.first == positions.first().copied() && self.scanned <= window.len();
        if !same {
            *self = TurnScan {
                generation,
                first: positions.first().copied(),
                ..TurnScan::default()
            };
        }
        for entry in &window[self.scanned..] {
            self.add(entry);
        }
        self.scanned = window.len();
        self.turns(model)
    }
}

/// [`context::estimate_tokens`] of `messages`, reusing the estimates in `kept` for the
/// leading messages that are the same objects, and keeping the new ones.
fn estimate_reusing(kept: &mut Vec<(Message, u64)>, messages: &[Message]) -> u64 {
    let same = kept
        .iter()
        .zip(messages)
        .take_while(|((message, _), next)| same_message(message, next))
        .count();
    kept.truncate(same);
    for message in &messages[same..] {
        kept.push((message.clone(), context::estimate_message(message)));
    }
    kept.iter().map(|(_, tokens)| tokens).sum()
}

/// The same message object: the content behind it never changes.
fn same_message(a: &Message, b: &Message) -> bool {
    match (a, b) {
        (Message::User(a), Message::User(b)) => Arc::ptr_eq(a, b),
        (Message::Assistant(a), Message::Assistant(b)) => Arc::ptr_eq(a, b),
        (Message::ToolResult(a), Message::ToolResult(b)) => Arc::ptr_eq(a, b),
        _ => false,
    }
}

/// Correction factor for estimates: what the provider counted for a request over what was
/// estimated for it. `None` when the request was too small to tell.
pub(crate) fn calibrate(prompt_tokens: u64, estimated: u64) -> Option<f64> {
    if prompt_tokens == 0 || estimated < MIN_CALIBRATION_TOKENS {
        return None;
    }
    Some((prompt_tokens as f64 / estimated as f64).clamp(RATIO_RANGE.0, RATIO_RANGE.1))
}

impl Session {
    /// Runs `f` on the current branch, under the state lock.
    pub(crate) fn with_branch<T>(&self, f: impl FnOnce(&[&Entry]) -> T) -> T {
        let state = self.state.lock();
        let branch = state.history.branch();
        f(&branch)
    }

    /// Estimate of the system prompt and the tool definitions. They change only when MCP tools
    /// come or go, which clears the cached value.
    pub(crate) fn fixed_tokens(&self) -> u64 {
        self.fixed().total()
    }

    /// [`Session::fixed_tokens`] in its parts.
    pub(crate) fn fixed(&self) -> Fixed {
        if let Some(fixed) = *self.fixed_tokens.lock() {
            return fixed;
        }
        let (system, base, mcp) = {
            let state = self.state.lock();
            (state.system_prompt.clone(), state.base_prompt.clone(), state.mcp_section.clone())
        };
        let fixed = Fixed::of(&system, &base, mcp.as_deref(), self.tools().iter_specs());
        *self.fixed_tokens.lock() = Some(fixed);
        fixed
    }

    pub(crate) fn estimate_ratio(&self) -> f64 {
        self.state.lock().estimate_ratio
    }

    /// Window limits of the current model.
    pub(crate) fn limits(&self) -> Limits {
        let (model, thinking) = self.current_model();
        limits(&model, thinking, &self.config.compaction)
    }

    /// Tool result limits for the current model.
    pub(crate) fn tool_limits(&self) -> OutputLimits {
        tool_limits(&self.config.limits, &self.limits())
    }

    /// The current context size; see the module docs.
    pub(crate) fn budget(&self) -> Budget {
        let fixed = self.fixed_tokens();
        let mut guard = self.state.lock();
        let state = &mut *guard;
        let branch = state.history.branch();
        let (positions, generation) = state.history.branch_positions();
        let scan = &mut state.turn_scan;
        measure_with(
            &branch,
            &Measure {
                model: &state.model,
                limits: limits(&state.model, state.thinking, &self.config.compaction),
                fixed,
                ratio: state.estimate_ratio,
            },
            |window, start, model| scan.scan(window, &positions[start..], generation, model),
        )
    }

    /// Unscaled estimate of a request, to compare with the usage reported for it.
    ///
    /// A request repeats the messages of the one before and adds a few, so the estimates of
    /// the last request's messages are kept and reused while the messages are the same
    /// objects; estimating the whole context again took a tenth of each turn.
    pub(crate) fn estimate_request(&self, ctx: &her_llm::Context) -> u64 {
        self.fixed_tokens() + estimate_reusing(&mut self.request_estimates.lock(), &ctx.messages)
    }

    /// Learns the estimate correction from a response's reported usage.
    pub(crate) fn calibrate(&self, usage: &her_llm::Usage, estimated: u64) {
        if let Some(ratio) = calibrate(usage.prompt_tokens(), estimated) {
            self.state.lock().estimate_ratio = ratio;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::store::EntryKind;
    use her_llm::AssistantContent;
    use her_llm::AssistantMessage;
    use her_llm::LlmError;
    use her_llm::Usage;
    use her_llm::UserMessage;

    fn model(id: &str) -> Model {
        let mut model = her_llm::api::scripted::ScriptedApi::model();
        model.id = id.into();
        model.context_window = 10_000;
        model.max_output_tokens = Some(1_000);
        model
    }

    fn settings() -> CompactionSettings {
        CompactionSettings {
            enabled: true,
            threshold: 0.9,
            token_limit: 0,
            keep_user_tokens: 20_000,
            keep_skill_tokens: 20_000,
        }
    }

    fn entry(kind: EntryKind) -> Entry {
        Entry {
            id: rand::random::<u32>().to_string(),
            parent: None,
            ts: 0,
            kind,
        }
    }

    fn user(text: &str) -> Entry {
        entry(EntryKind::message(Message::from(UserMessage::text(text))))
    }

    fn reply(model: &str, stop: StopReason, input: u64, output: u64) -> Entry {
        entry(EntryKind::Message {
            message: Message::from(AssistantMessage {
                content: vec![AssistantContent::Text { text: "ok".into() }],
                provider: "test".into(),
                api: "scripted".into(),
                model: model.into(),
                response_id: None,
                usage: Usage {
                    input,
                    output,
                    ..Default::default()
                },
                stop_reason: stop,
                error: (stop == StopReason::Error).then(|| Box::new(LlmError::new(ErrorKind::ContextOverflow, "too long"))),
                ts: 0,
                duration_ms: None,
            }),
            skills: Vec::new(),
            skill: None,
        })
    }

    fn tool(text: &str) -> Entry {
        entry(EntryKind::Message {
            message: Message::from(her_llm::ToolResultMessage {
                call_id: "c".into(),
                tool_name: "bash".into(),
                content: vec![her_llm::UserContent::Text { text: text.into() }],
                is_error: false,
                details: None,
                ts: 0,
                duration_ms: None,
            }),
            skills: Vec::new(),
            skill: None,
        })
    }

    fn handoff() -> u64 {
        context::estimate_text(context::HANDOFF_REQUEST) + context::MESSAGE_OVERHEAD
    }

    fn measure_with(entries: &[Entry], model: &Model, ratio: f64) -> Budget {
        let branch: Vec<&Entry> = entries.iter().collect();
        measure(
            &branch,
            &Measure {
                model,
                limits: limits(model, ThinkingLevel::Off, &settings()),
                fixed: 100,
                ratio,
            },
        )
    }

    #[test]
    fn limits_leave_room_for_the_reply() {
        let l = limits(&model("m"), ThinkingLevel::Off, &settings());
        assert_eq!(l, Limits { soft: 8_100, hard: 9_000, window: 10_000 });
        let capped = CompactionSettings {
            token_limit: 5_000,
            ..settings()
        };
        assert_eq!(limits(&model("m"), ThinkingLevel::Off, &capped).soft, 5_000);
        assert_eq!(retained_budget(&settings(), &l), 1_350);
        assert_eq!(tool_limits(&OutputLimits::default(), &l).max_tokens, 900);
    }

    #[test]
    fn the_last_reported_usage_is_the_baseline() {
        let m = model("m");
        let entries = [user("hi"), reply("m", StopReason::ToolUse, 3_000, 100), user("12345678")];
        let budget = measure_with(&entries, &m, 1.0);
        // 3,100 reported + the user message after it (4 overhead + 2).
        assert_eq!(budget.usage.tokens, 3_106);
        assert_eq!(budget.usage.estimated, 6);
        assert!(!budget.full);
        assert_eq!(measure_with(&entries, &m, 2.0).usage.estimated, 12);
    }

    #[test]
    fn another_model_or_no_response_means_a_full_estimate() {
        let m = model("m");
        let entries = [user("hi"), reply("other", StopReason::Stop, 3_000, 100)];
        let budget = measure_with(&entries, &m, 1.0);
        assert_eq!(budget.previous_model.as_deref(), Some("test/other"));
        assert_eq!(budget.usage.tokens, budget.usage.estimated);
        assert!(budget.usage.tokens < 200, "{}", budget.usage.tokens);
        let fresh = measure_with(&[user("hi")], &m, 1.0);
        assert_eq!(fresh.usage.tokens, 100 + 4 + 1);
    }

    #[test]
    fn an_overflow_marks_the_window_full_until_a_response_succeeds() {
        let m = model("m");
        let mut entries = vec![user("hi"), reply("m", StopReason::Stop, 1_000, 10), user("again")];
        entries.push(reply("m", StopReason::Error, 0, 0));
        let budget = measure_with(&entries, &m, 1.0);
        assert!(budget.full);
        assert_eq!(budget.usage.tokens, 9_000);
        entries.push(reply("m", StopReason::Stop, 2_000, 10));
        assert!(!measure_with(&entries, &m, 1.0).full);
    }

    #[test]
    fn compacts_a_turn_early_when_the_next_turn_would_not_fit() {
        let budget = |tokens: u64, next_turn: u64| Budget {
            usage: ContextUsage {
                tokens,
                estimated: 0,
                soft_limit: 8_100,
                hard_limit: 9_000,
                window: 10_000,
            },
            next_turn,
            full: false,
            previous_model: None,
        };
        assert!(budget(8_100, 0).needs_compaction(), "the soft limit");
        assert!(!budget(7_000, 1_000).needs_compaction(), "the next turn fits");
        assert!(budget(7_000, 2_000).needs_compaction(), "the next turn would cross the hard limit");
        assert!(!budget(4_000, 6_000).needs_compaction(), "not before half the window");
    }

    #[test]
    fn the_next_turn_is_guessed_from_the_largest_turn() {
        let m = model("m");
        let entries = [
            user("hi"),
            reply("m", StopReason::ToolUse, 1_000, 10),
            tool(&"x".repeat(4_000)),
            // The provider counted that turn as estimated: 1,009 more.
            reply("m", StopReason::ToolUse, 2_009, 10),
            tool("short"),
        ];
        let budget = measure_with(&entries, &m, 1.0);
        // The big result's turn: the reply (4 + 1) and the result (4 + 1,000).
        assert_eq!(budget.next_turn, 5 + 1_004 + handoff());
        assert!(budget.usage.estimated < 20, "only the short result is estimated");
    }

    #[test]
    fn additions_are_scaled_as_this_window_counted_them() {
        let m = model("m");
        let entries = [
            user("hi"),
            reply("m", StopReason::ToolUse, 1_000, 10),
            tool(&"x".repeat(4_000)),
            // The turn estimated at 1,009 was counted as 1,211.
            reply("m", StopReason::ToolUse, 2_211, 10),
            tool(&"y".repeat(400)),
        ];
        let ratio = 1_211.0 / 1_009.0;
        // The whole-request ratio given is not the one used.
        let budget = measure_with(&entries, &m, 0.8);
        assert_eq!(budget.usage.estimated, scale(104, ratio));
        assert_eq!(budget.usage.tokens, 2_221 + scale(104, ratio));
        assert_eq!(budget.next_turn, scale(1_009 + handoff(), ratio));

        // A short turn after a long one: the window as a whole still says how the long ones count.
        let entries = [
            user("hi"),
            reply("m", StopReason::ToolUse, 1_000, 10),
            tool(&"x".repeat(4_000)),
            reply("m", StopReason::ToolUse, 2_211, 10),
            tool("short"),
            // The short turn, estimated at 11, counted as 13.
            reply("m", StopReason::ToolUse, 2_224, 10),
            tool(&"y".repeat(400)),
        ];
        let ratio = 1_224.0 / 1_020.0;
        assert_eq!(measure_with(&entries, &m, 0.8).usage.estimated, scale(104, ratio));
    }

    #[test]
    fn the_whole_request_ratio_stands_in_when_the_window_says_nothing() {
        let m = model("m");
        let after = |earlier: Entry, prompt: u64, between: Entry| {
            [user("hi"), earlier, between, reply("m", StopReason::ToolUse, prompt, 10), tool(&"y".repeat(400))]
        };
        let cases = [
            // The request grew by far more than its messages: the system prompt changed, say.
            ("out of range", after(reply("m", StopReason::ToolUse, 1_000, 10), 4_000, tool(&"x".repeat(4_000)))),
            ("too small to tell", after(reply("m", StopReason::ToolUse, 1_000, 10), 1_050, tool("short"))),
            // Its growth was counted by another model's tokenizer.
            ("another model", after(reply("other", StopReason::ToolUse, 1_000, 10), 2_211, tool(&"x".repeat(4_000)))),
        ];
        for (case, entries) in cases {
            assert_eq!(measure_with(&entries, &m, 1.5).usage.estimated, scale(104, 1.5), "{case}");
        }
        let first = [user("hi"), reply("m", StopReason::ToolUse, 1_000, 10), tool(&"y".repeat(400))];
        assert_eq!(measure_with(&first, &m, 1.5).usage.estimated, scale(104, 1.5), "the window's first response");
    }

    #[test]
    fn calibration_is_bounded() {
        assert_eq!(calibrate(1_500, 1_000), Some(1.5));
        assert_eq!(calibrate(10_000, 1_000), Some(2.0));
        assert_eq!(calibrate(100, 1_000), Some(0.5));
        assert_eq!(calibrate(500, 100), None);
    }

    #[test]
    fn reused_estimates_match_a_fresh_estimate() {
        let text = |t: &str| Message::from(UserMessage::text(t));
        let a = text("first question, in English");
        let b = text("第二个问题");
        let c = text("third");
        let mut kept = Vec::new();
        let fresh = |messages: &[Message]| context::estimate_tokens(messages);
        for messages in [
            vec![a.clone()],
            vec![a.clone(), b.clone()],
            vec![a.clone(), b.clone(), c.clone()],
            // A compaction: a new first message, then the same ones.
            vec![text("summary"), c.clone()],
            vec![text("summary"), c.clone(), a.clone()],
            // An equal message that is another object is estimated again.
            vec![text("summary")],
            Vec::new(),
        ] {
            assert_eq!(estimate_reusing(&mut kept, &messages), fresh(&messages));
            assert_eq!(kept.len(), messages.len());
        }
    }


    #[test]
    fn the_turn_scan_matches_a_scan_of_the_whole_window() {
        let m = model("m");
        let reply = |text: &str, input: u64| EntryKind::Message {
            message: Message::from(AssistantMessage {
                content: vec![AssistantContent::Text { text: text.into() }],
                provider: "test".into(),
                api: "scripted".into(),
                model: "m".into(),
                response_id: None,
                usage: Usage {
                    input,
                    output: 10,
                    ..Default::default()
                },
                stop_reason: StopReason::ToolUse,
                error: None,
                ts: 0,
                duration_ms: None,
            }),
            skills: Vec::new(),
            skill: None,
        };
        let user = |text: &str| EntryKind::Message {
            message: Message::from(UserMessage::text(text)),
            skills: Vec::new(),
            skill: None,
        };
        let mut history = crate::history::History::default();
        let mut scan = TurnScan::default();
        for i in 0..60 {
            let kind = match i % 5 {
                0 => reply(&"reply ".repeat(i), 1_000 + 40 * i as u64),
                4 if i % 20 == 4 => EntryKind::Compaction(Box::new(crate::store::CompactionEntry {
                    seq: 1,
                    reason: her_protocol::CompactionReason::Threshold,
                    summary: "s".into(),
                    retained: Vec::new(),
                    skills: Vec::new(),
                    model: "p/m".into(),
                    usage: Usage::default(),
                    tokens_before: 0,
                    tokens_after: 0,
                    dropped_messages: 0,
                })),
                _ => user(&"result text ".repeat(i % 7 + 1)),
            };
            history.append(history.new_id(), kind, i as i64);
            let branch = history.branch();
            let (positions, generation) = history.branch_positions();
            let start = context::window_start(&branch);
            let kept = scan.scan(&branch[start..], &positions[start..], generation, &m);
            assert_eq!(kept, scan_turns(&branch[start..], &m), "after entry {i}");
        }
    }

}
