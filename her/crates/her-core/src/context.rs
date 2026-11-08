//! Turning the session branch into the message list sent to the model.

use her_llm::AssistantContent;
use her_llm::Message;
use her_llm::StopReason;
use her_llm::Timestamp;
use her_llm::ToolResultMessage;
use her_llm::ToolSpec;
use her_llm::UserContent;
use her_llm::UserMessage;
use her_protocol::SkillUse;
use std::borrow::Cow;
use std::sync::Arc;

use crate::history::History;
use crate::store::Entry;
use crate::store::EntryKind;
use crate::store::ExpandedSkill;
use crate::store::SkillText;

pub const NOT_EXECUTED: &str = "Tool call was not executed (interrupted).";

/// Appended to the context to ask the model for a handoff summary.
pub const HANDOFF_REQUEST: &str = include_str!("prompts/handoff.md");

const SUMMARY_INTRO: &str = "Earlier parts of this conversation were replaced by the handoff summary below, \
    written by the assistant that did the work.";
const SUMMARY_KEPT: &str = "The user's recent messages are kept above in their own words: the latest one \
    steers the work, and the goal in the summary stands unless that message clearly cancels or replaces it.";
const SUMMARY_GO_ON: &str = "Tool results from that work are no longer in context: read files again when you \
    need their current content. Continue from where the work stopped and do not redo finished steps.";
const SKILLS_KEPT: &str = "These skills were in use before the conversation was compacted.";

/// Index of the last compaction entry on the branch.
pub fn last_compaction(branch: &[&Entry]) -> Option<usize> {
    branch
        .iter()
        .rposition(|e| matches!(e.kind, EntryKind::Compaction { .. }))
}

/// Where the current context window starts: right after the last compaction.
pub fn window_start(branch: &[&Entry]) -> usize {
    last_compaction(branch).map_or(0, |i| i + 1)
}

/// The user message that gives the model a new date, as the system prompt's `<environment>`
/// gives the first.
pub fn date_message(date: &str, ts: Timestamp) -> UserMessage {
    let text = match chrono::NaiveDate::parse_from_str(date, "%Y-%m-%d") {
        Ok(day) => crate::prompt::date_text(day),
        Err(_) => date.to_string(),
    };
    UserMessage {
        content: vec![UserContent::Text {
            text: format!("<environment_update>\ndate: {text}\n</environment_update>"),
        }],
        ts,
    }
}

/// The user message that gives a bot its new name (bot-design §2.4).
pub fn bot_name_message(name: &str, ts: Timestamp) -> UserMessage {
    UserMessage {
        content: vec![UserContent::Text {
            text: format!("<environment_update>\nyour name: {name}\n</environment_update>"),
        }],
        ts,
    }
}

/// The user message that gives a bot the list of its routines (routine-design §4.4).
pub fn routines_message(text: &str, ts: Timestamp) -> UserMessage {
    UserMessage {
        content: vec![UserContent::Text {
            text: format!("<environment_update>\n{text}\n</environment_update>"),
        }],
        ts,
    }
}

/// The user message that reminds a bot of what came of its work (bot-turn-design §6).
pub fn undelivered_message(ts: Timestamp) -> UserMessage {
    UserMessage {
        content: vec![UserContent::Text {
            text: "<reply_reminder>\nWhat you wrote stays private and has not reached the user. If they are waiting \
                   for any of it, call send_to_user with it now; otherwise end your turn without writing anything.\n</reply_reminder>"
                .into(),
        }],
        ts,
    }
}

/// The user message right after each of a bot's owner's messages (bot-design §3.14).
pub fn reply_nudge_message(ts: Timestamp) -> UserMessage {
    UserMessage {
        content: vec![UserContent::Text {
            text: "<reminder>Reply with send_to_user, and write no text outside the call. Think after it is sent. Long work goes to an agent.</reminder>".into(),
        }],
        ts,
    }
}

/// The user message that reminds a bot its owner has had no reply (bot-design §3.11), or that a
/// routine's report has not reached them (routine-design §2.4).
pub fn unanswered_message(routine: bool, ts: Timestamp) -> UserMessage {
    let text = if routine {
        "<reply_reminder>\nThe routine report above has not reached the user: what you write stays private, and only \
         send_to_user reaches them. Pass on what it says now. If it holds nothing they asked to hear, send nothing and write \
         only NOTHING_TO_REPORT. If the user no longer wants these, pause the routine and tell them you did.\n</reply_reminder>"
    } else {
        "<reply_reminder>\nThe user has not received a reply to their last message: what you write \
         stays private, and only send_to_user reaches them. Call send_to_user with your reply now, and write no text \
         outside the call.\n</reply_reminder>"
    };
    UserMessage {
        content: vec![UserContent::Text { text: text.into() }],
        ts,
    }
}

/// The bot's name the current context last gave, when not the system prompt's: the newest
/// name entry since the last compaction.
pub fn name_in_context(history: &History) -> Option<&str> {
    for entry in history.branch_back() {
        match &entry.kind {
            EntryKind::BotName { name } => return Some(name),
            EntryKind::Compaction(_) => break,
            _ => {}
        }
    }
    None
}

/// The list of routines the current context last gave: the newest such entry since the last
/// compaction.
pub fn routines_in_context(history: &History) -> Option<&str> {
    for entry in history.branch_back() {
        match &entry.kind {
            EntryKind::Routines { text } => return Some(text),
            EntryKind::Compaction(_) => break,
            _ => {}
        }
    }
    None
}

/// The date the current context last gave the model: the newest date entry since the last
/// compaction, else the one in the system prompt.
pub fn date_in_context(history: &History, base_prompt: &str) -> Option<chrono::NaiveDate> {
    for entry in history.branch_back() {
        match &entry.kind {
            EntryKind::Date { date } => return chrono::NaiveDate::parse_from_str(date, "%Y-%m-%d").ok(),
            EntryKind::Compaction(_) => break,
            _ => {}
        }
    }
    crate::prompt::prompt_date(base_prompt)
}

/// The user message that carries a handoff summary in the new context.
pub fn summary_message(summary: &str, kept_user_messages: bool, ts: Timestamp) -> UserMessage {
    let kept = if kept_user_messages {
        format!(" {SUMMARY_KEPT}")
    } else {
        String::new()
    };
    UserMessage {
        content: vec![UserContent::Text {
            text: format!(
                "<conversation_summary>\n{SUMMARY_INTRO}{kept} {SUMMARY_GO_ON}\n\n{}\n</conversation_summary>",
                summary.trim()
            ),
        }],
        ts,
    }
}

/// The user message that carries the instructions of the skills in use through a compaction.
pub fn skills_message(skills: &[SkillText], ts: Timestamp) -> UserMessage {
    let blocks: Vec<&str> = skills.iter().map(|s| s.content.as_str()).collect();
    UserMessage {
        content: vec![UserContent::Text {
            text: format!("{SKILLS_KEPT}\n\n{}", blocks.join("\n\n")),
        }],
        ts,
    }
}

/// Messages of the current context: after a compaction, its retained user messages, the
/// skills in use and its summary, then the messages recorded after it.
pub fn branch_messages(branch: &[&Entry]) -> Vec<Message> {
    let mut out = Vec::new();
    let start = match last_compaction(branch) {
        Some(index) => {
            if let EntryKind::Compaction(checkpoint) = &branch[index].kind {
                out.extend(checkpoint.retained.iter().cloned().map(Message::from));
                if !checkpoint.skills.is_empty() {
                    out.push(Message::from(skills_message(&checkpoint.skills, branch[index].ts)));
                }
                out.push(Message::from(summary_message(
                    &checkpoint.summary,
                    !checkpoint.retained.is_empty(),
                    branch[index].ts,
                )));
            }
            index + 1
        }
        None => 0,
    };
    for entry in &branch[start..] {
        if let Some(message) = entry_message(&entry.kind, entry.ts) {
            out.push(message.into_owned());
        }
    }
    out
}

/// The message an entry adds to the context; `ts` is the entry's. A delivered report, a new
/// date, a bot's new name, its routines and its reply reminders are user messages; a user message that mentions
/// skills carries their instructions first (docs/kernel/skill-design.md §6.3.4).
pub fn entry_message(kind: &EntryKind, ts: Timestamp) -> Option<Cow<'_, Message>> {
    match kind {
        EntryKind::Message { message, skills, .. } if !skills.is_empty() => Some(Cow::Owned(with_skills(message, skills))),
        EntryKind::Message { message, .. } | EntryKind::Mail { message, .. } | EntryKind::AgentMessage { message, .. } | EntryKind::AgentToUser { message, .. } => {
            Some(Cow::Borrowed(message))
        }
        EntryKind::Date { date } => Some(Cow::Owned(Message::from(date_message(date, ts)))),
        EntryKind::BotName { name } => Some(Cow::Owned(Message::from(bot_name_message(name, ts)))),
        EntryKind::Routines { text } => Some(Cow::Owned(Message::from(routines_message(text, ts)))),
        EntryKind::Unanswered { routine } => Some(Cow::Owned(Message::from(unanswered_message(*routine, ts)))),
        EntryKind::Undelivered => Some(Cow::Owned(Message::from(undelivered_message(ts)))),
        EntryKind::ReplyNudge => Some(Cow::Owned(Message::from(reply_nudge_message(ts)))),
        _ => None,
    }
}

/// A user message with the instructions of the skills it mentions before the user's words,
/// one text item each.
fn with_skills(message: &Message, skills: &[SkillText]) -> Message {
    let Message::User(user) = message else {
        return message.clone();
    };
    let mut content: Vec<UserContent> = skills
        .iter()
        .map(|skill| UserContent::Text {
            text: skill.content.clone(),
        })
        .collect();
    content.extend(user.content.iter().cloned());
    Message::from(UserMessage { content, ts: user.ts })
}

/// The skills a message mentions, for clients: names and paths, without the instructions.
pub fn skill_uses(skills: &[SkillText], legacy: Option<&ExpandedSkill>) -> Vec<SkillUse> {
    let uses = skills.iter().map(|skill| SkillUse {
        name: skill.name.clone(),
        path: skill.path.clone(),
    });
    let legacy = legacy.map(|skill| SkillUse {
        name: skill.name.clone(),
        path: skill.path.clone(),
    });
    uses.chain(legacy).collect()
}

/// A user message as the user wrote it. Phase 2 logs hold `/name`'s instructions in place of
/// the first text, and what was typed beside them.
pub fn written(message: &Message, legacy: Option<&ExpandedSkill>) -> Message {
    match (message, legacy) {
        (Message::User(user), Some(skill)) => {
            let mut user = (**user).clone();
            if let Some(UserContent::Text { text }) = user.content.first_mut() {
                text.clone_from(&skill.input);
            }
            Message::from(user)
        }
        _ => message.clone(),
    }
}

/// How many leading messages of [`branch_messages`] come from the last compaction.
pub fn checkpoint_len(branch: &[&Entry]) -> usize {
    match last_compaction(branch).map(|i| &branch[i].kind) {
        Some(EntryKind::Compaction(checkpoint)) => {
            checkpoint.retained.len() + usize::from(!checkpoint.skills.is_empty()) + 1
        }
        _ => 0,
    }
}

/// Makes any history valid for a model request:
/// - failed assistant messages are dropped; aborted ones keep only their text;
/// - every tool call gets a result right after its assistant message, in call order;
/// - results without a matching call are dropped.
pub fn normalize(messages: Vec<Message>) -> Vec<Message> {
    let mut out = Vec::with_capacity(messages.len());
    let mut iter = messages.into_iter().peekable();
    while let Some(message) = iter.next() {
        match message {
            Message::User(_) => out.push(message),
            Message::ToolResult(_) => {} // orphan
            Message::Assistant(mut assistant) => {
                if assistant.stop_reason == StopReason::Error {
                    // Its results (if any) are orphans now; skip them too.
                    while matches!(iter.peek(), Some(Message::ToolResult(_))) {
                        iter.next();
                    }
                    continue;
                }
                let replayable = |c: &AssistantContent| matches!(c, AssistantContent::Text { text } if !text.is_empty());
                if assistant.stop_reason == StopReason::Aborted && !assistant.content.iter().all(replayable) {
                    // Messages are shared with the history; this copies only the one that changes.
                    Arc::make_mut(&mut assistant).content.retain(replayable);
                }
                let mut results: Vec<Arc<ToolResultMessage>> = Vec::new();
                while let Some(Message::ToolResult(_)) = iter.peek() {
                    if let Some(Message::ToolResult(result)) = iter.next() {
                        results.push(result);
                    }
                }
                if assistant.content.is_empty() {
                    continue;
                }
                out.push(Message::Assistant(assistant.clone()));
                for call in assistant.tool_calls() {
                    let result = match results.iter().position(|r| r.call_id == call.id) {
                        Some(index) => results.swap_remove(index),
                        None => Arc::new(ToolResultMessage {
                            call_id: call.id.clone(),
                            tool_name: call.name.clone(),
                            content: vec![UserContent::Text {
                                text: NOT_EXECUTED.to_string(),
                            }],
                            is_error: true,
                            details: None,
                            ts: 0,
                            duration_ms: None,
                        }),
                    };
                    out.push(Message::ToolResult(result));
                }
            }
        }
    }
    out
}

/// Role markers and similar framing per message.
pub const MESSAGE_OVERHEAD: u64 = 4;
pub const IMAGE_TOKENS: u64 = 1600;

/// Conservative token estimate of a text: 4 ASCII bytes or 1 other character per token.
pub fn estimate_text(text: &str) -> u64 {
    let mut tally = Tally::default();
    tally.add(text.as_bytes());
    tally.tokens()
}

/// ASCII bytes and other characters of UTF-8 text, counted by byte: every other character
/// starts with a byte of 0xC0 or more and continues with bytes below it. Requests estimate
/// the whole history each time, so this runs over every byte of it per request.
#[derive(Default)]
struct Tally {
    ascii: u64,
    other: u64,
}

impl Tally {
    fn add(&mut self, bytes: &[u8]) {
        // Counted per chunk in narrow integers, which the compiler vectorizes.
        for chunk in bytes.chunks(u8::MAX as usize) {
            let (mut ascii, mut other) = (0u8, 0u8);
            for &b in chunk {
                ascii += u8::from(b < 0x80);
                other += u8::from(b >= 0xC0);
            }
            self.ascii += u64::from(ascii);
            self.other += u64::from(other);
        }
    }

    fn tokens(&self) -> u64 {
        self.ascii.div_ceil(4) + self.other
    }
}


pub fn estimate_user_content(content: &[UserContent]) -> u64 {
    content
        .iter()
        .map(|c| match c {
            UserContent::Text { text } => estimate_text(text),
            UserContent::Image { .. } => IMAGE_TOKENS,
        })
        .sum()
}

pub fn estimate_message(message: &Message) -> u64 {
    MESSAGE_OVERHEAD
        + match message {
            Message::User(u) => estimate_user_content(&u.content),
            Message::ToolResult(r) => estimate_user_content(&r.content),
            Message::Assistant(a) => a
                .content
                .iter()
                .map(|c| match c {
                    AssistantContent::Text { text } | AssistantContent::Thinking { text, .. } => estimate_text(text),
                    AssistantContent::ToolCall(call) => estimate_text(call.arguments.as_str()) + 8,
                })
                .sum(),
        }
}

/// Estimate of a list of messages; see [`estimate_text`].
pub fn estimate_tokens(messages: &[Message]) -> u64 {
    messages.iter().map(estimate_message).sum()
}

/// Estimate of the part of every request that never changes: system prompt and tool definitions.
pub fn estimate_fixed(system: &str, tools: &[ToolSpec]) -> u64 {
    estimate_text(system) + tools.iter().map(estimate_tool).sum::<u64>()
}

/// Estimate of one tool definition.
pub fn estimate_tool(tool: &ToolSpec) -> u64 {
    estimate_text(&tool.name) + estimate_text(&tool.description) + estimate_text(&tool.parameters.to_string()) + 8
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_llm::AssistantMessage;
    use her_llm::LlmError;
    use her_llm::ToolCall;
    use her_llm::Usage;
    use serde_json::json;

    fn assistant(content: Vec<AssistantContent>, stop: StopReason) -> Message {
        Message::from(AssistantMessage {
            content,
            provider: "p".into(),
            api: "a".into(),
            model: "m".into(),
            response_id: None,
            usage: Usage::default(),
            stop_reason: stop,
            error: (stop == StopReason::Error).then(|| Box::new(LlmError::new(her_llm::ErrorKind::Network, "x"))),
            ts: 0,
            duration_ms: None,
        })
    }

    fn call(id: &str) -> AssistantContent {
        AssistantContent::ToolCall(ToolCall {
            id: id.into(),
            name: "bash".into(),
            arguments: json!({}).into(),
            arguments_error: None,
            provider_data: None,
        })
    }

    fn result(id: &str) -> Message {
        Message::from(ToolResultMessage {
            call_id: id.into(),
            tool_name: "bash".into(),
            content: vec![UserContent::Text { text: format!("r{id}") }],
            is_error: false,
            details: None,
            ts: 0,
            duration_ms: None,
        })
    }

    #[test]
    fn repairs_tool_call_pairs() {
        let messages = vec![
            Message::from(UserMessage::text("go")),
            assistant(vec![call("a"), call("b")], StopReason::ToolUse),
            result("b"),
            result("zzz"),
            assistant(vec![AssistantContent::Text { text: "partial".into() }], StopReason::Error),
            Message::from(UserMessage::text("again")),
            assistant(
                vec![AssistantContent::Text { text: "half".into() }, call("c")],
                StopReason::Aborted,
            ),
        ];
        let out = normalize(messages);
        let kinds: Vec<String> = out
            .iter()
            .map(|m| match m {
                Message::User(u) => format!("user:{}", u.text_content()),
                Message::Assistant(a) => format!("assistant:{}:{}", a.text(), a.tool_calls().len()),
                Message::ToolResult(r) => format!("result:{}:{}", r.call_id, r.is_error),
            })
            .collect();
        assert_eq!(
            kinds,
            vec![
                "user:go",
                "assistant::2",
                "result:a:true",
                "result:b:false",
                "user:again",
                "assistant:half:0",
            ]
        );
    }

    #[test]
    fn estimates_cjk_conservatively() {
        let messages = vec![Message::from(UserMessage::text("abcd你好"))];
        assert_eq!(estimate_tokens(&messages), MESSAGE_OVERHEAD + 1 + 2);
        assert_eq!(estimate_text("abcde"), 2);
    }

    #[test]
    fn estimates_count_characters_like_chars() {
        let by_chars = |text: &str| {
            let ascii = text.chars().filter(char::is_ascii).count() as u64;
            ascii.div_ceil(4) + text.chars().count() as u64 - ascii
        };
        let mixed = "状态：TODO — naïve café 🚀 \u{10FFFF} done\n".repeat(40);
        for text in ["", "a", "é", "🚀", "你好abc", mixed.as_str()] {
            assert_eq!(estimate_text(text), by_chars(text), "{text}");
        }
        // Tool call arguments are estimated as the text they travel as.
        let args = json!({"path": "notes/报告.md", "content": mixed, "n": [1, 2.5, null, true]});
        assert_eq!(estimate_text(her_llm::JsonText::from_value(&args).as_str()), by_chars(&args.to_string()));
    }

    fn entry(id: &str, kind: EntryKind) -> Entry {
        Entry {
            id: id.into(),
            parent: None,
            ts: 7,
            kind,
        }
    }

    fn user_entry(id: &str, text: &str) -> Entry {
        entry(
            id,
            EntryKind::message(Message::from(UserMessage::text(text))),
        )
    }

    fn compaction(retained: Vec<UserMessage>, skills: Vec<SkillText>) -> EntryKind {
        EntryKind::Compaction(Box::new(crate::store::CompactionEntry {
            seq: 1,
            reason: her_protocol::CompactionReason::Threshold,
            summary: "  the summary  ".into(),
            retained,
            skills,
            model: "test/scripted".into(),
            usage: her_llm::Usage::default(),
            tokens_before: 100,
            tokens_after: 10,
            dropped_messages: 0,
        }))
    }

    fn user_texts(branch: &[&Entry]) -> Vec<String> {
        branch_messages(branch)
            .iter()
            .map(|m| match m {
                Message::User(u) => u.text_content(),
                other => panic!("unexpected {other:?}"),
            })
            .collect()
    }

    #[test]
    fn the_date_given_is_the_newest_since_the_last_compaction() {
        let prompt = "You are Her.\n\n<environment>\ndate: 2026-10-30 (Friday)\n</environment>\n";
        let started = chrono::NaiveDate::from_ymd_opt(2026, 10, 30);
        let november = |day| chrono::NaiveDate::from_ymd_opt(2026, 11, day);
        let mut history = History::default();
        history.append("1".into(), EntryKind::message(Message::from(UserMessage::text("hi"))), 1);
        assert_eq!(date_in_context(&history, prompt), started);
        history.append("2".into(), EntryKind::Date { date: "2026-11-02".into() }, 2);
        assert_eq!(date_in_context(&history, prompt), november(2));
        // The compaction replaces that update: the system prompt's date is what is left.
        history.append("3".into(), compaction(Vec::new(), Vec::new()), 3);
        assert_eq!(date_in_context(&history, prompt), started);
        history.append("4".into(), EntryKind::Date { date: "2026-11-03".into() }, 4);
        assert_eq!(date_in_context(&history, prompt), november(3));
        let texts = user_texts(&history.branch());
        assert_eq!(texts.len(), 2, "the summary, then the date");
        assert_eq!(texts[1], "<environment_update>\ndate: 2026-11-03 (Tuesday)\n</environment_update>");
    }

    #[test]
    fn a_compaction_replaces_everything_before_it() {
        let entries = [
            user_entry("1", "old request"),
            entry("2", assistant_entry_kind("old reply")),
            entry("3", compaction(vec![UserMessage::text("old request")], Vec::new())),
            user_entry("4", "new request"),
        ];
        let branch: Vec<&Entry> = entries.iter().collect();
        let texts = user_texts(&branch);
        assert_eq!(texts.len(), 3);
        assert_eq!(texts[0], "old request");
        assert!(texts[1].starts_with("<conversation_summary>\nEarlier parts"));
        assert!(texts[1].contains("kept above in their own words"));
        assert!(texts[1].ends_with("\n\nthe summary\n</conversation_summary>"));
        assert_eq!(texts[2], "new request");
        assert_eq!(checkpoint_len(&branch), 2);
        assert_eq!(window_start(&branch), 3);
        let without = summary_message("s", false, 0).text_content();
        assert!(!without.contains("kept above"));
    }

    fn parts(message: &Message) -> Vec<String> {
        let Message::User(user) = message else { panic!("a user message") };
        user.content
            .iter()
            .filter_map(|c| match c {
                UserContent::Text { text } => Some(text.clone()),
                _ => None,
            })
            .collect()
    }

    #[test]
    fn mentioned_skills_come_first_and_phase_2_logs_read_as_written() {
        let skill = SkillText {
            name: "a".into(),
            path: "/s/a/SKILL.md".into(),
            content: "<skill name=\"a\">\nA.\n</skill>".into(),
        };
        let words = Message::from(UserMessage::text("use /skill:a now"));
        let mentioned = EntryKind::Message {
            message: words.clone(),
            skills: vec![skill.clone()],
            skill: None,
        };
        assert_eq!(parts(&entry_message(&mentioned, 0).unwrap()), ["<skill name=\"a\">\nA.\n</skill>", "use /skill:a now"]);
        assert!(matches!(entry_message(&EntryKind::message(words), 0), Some(Cow::Borrowed(_))), "nothing copied");
        let uses = skill_uses(std::slice::from_ref(&skill), None);
        assert_eq!(
            uses,
            [SkillUse {
                name: "a".into(),
                path: "/s/a/SKILL.md".into(),
            }]
        );

        // Phase 2 wrote the instructions into the message: the model sees them as before, the
        // user sees what was typed.
        let line = r#"{"id":"e1","parent":null,"ts":1,"type":"message","message":{"role":"user","content":[{"type":"text","text":"<skill name=\"d\">D.</skill>\n\nnow"}],"ts":1},"skill":{"name":"d","path":"/s/d/SKILL.md","input":"/d now"}}"#;
        let entry: Entry = serde_json::from_str(line).unwrap();
        let EntryKind::Message { message, skills, skill } = &entry.kind else { panic!("a message") };
        assert_eq!(parts(&entry_message(&entry.kind, entry.ts).unwrap()), ["<skill name=\"d\">D.</skill>\n\nnow"]);
        assert_eq!(parts(&written(message, skill.as_deref())), ["/d now"]);
        assert_eq!(skill_uses(skills, skill.as_deref())[0].name, "d");
    }

    #[test]
    fn kept_skills_sit_between_the_kept_messages_and_the_summary() {
        let kept = |name: &str| SkillText {
            name: name.into(),
            path: format!("/s/{name}/SKILL.md").into(),
            content: format!("<skill name=\"{name}\">\nSteps.\n</skill>"),
        };
        let entries = [
            user_entry("1", "old request"),
            entry("2", compaction(vec![UserMessage::text("old request")], vec![kept("a"), kept("b")])),
            user_entry("3", "new request"),
        ];
        let branch: Vec<&Entry> = entries.iter().collect();
        let texts = user_texts(&branch);
        assert_eq!(texts.len(), 4);
        assert_eq!(texts[0], "old request");
        assert_eq!(
            texts[1],
            format!("{SKILLS_KEPT}\n\n<skill name=\"a\">\nSteps.\n</skill>\n\n<skill name=\"b\">\nSteps.\n</skill>")
        );
        assert!(texts[2].starts_with("<conversation_summary>"));
        assert_eq!(texts[3], "new request");
        assert_eq!(checkpoint_len(&branch), 3);
    }

    fn assistant_entry_kind(text: &str) -> EntryKind {
        EntryKind::message(assistant(vec![AssistantContent::Text { text: text.into() }], StopReason::Stop))
    }
}
