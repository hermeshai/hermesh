//! Handoff-summary compaction. The model that did the work writes a handoff summary; the
//! context then continues from the user's recent messages, kept word for word, and that
//! summary.
//!
//! The summary request is the next normal request plus one appended user message, with the
//! same system prompt, tools and options, so the provider's prompt cache covers almost all of
//! it. Nothing is recorded until a summary is in hand: a failed compaction leaves the history
//! as it was.

use her_llm::AssistantMessage;
use her_llm::ErrorKind;
use her_llm::LlmError;
use her_llm::Message;
use her_llm::MessageAccumulator;
use her_llm::Model;
use her_llm::RequestOptions;
use her_llm::StopReason;
use her_llm::StreamEvent;
use her_llm::ToolChoice;
use her_llm::Usage;
use her_llm::UserContent;
use her_llm::UserMessage;
use her_llm::now_ms;
use her_protocol::CompactionReason;
use her_protocol::ErrorInfo;
use her_protocol::EventMsg;
use futures::StreamExt;
use std::sync::Arc;

use crate::budget;
use crate::context;
use crate::session::Session;
use crate::skills::kept;
use crate::store::CompactionEntry;
use crate::store::EntryKind;
use crate::task::TaskCtx;

/// Attempts at shrinking a summary request that is itself too large.
const MAX_TRIMS: u32 = 4;
/// A trimmed request aims at no more than this share of the hard limit…
const TRIM_TARGET_PERCENT: u64 = 85;
/// …and at most this share of the request that was rejected, since the configured window
/// may be wrong.
const TRIM_SHRINK_PERCENT: u64 = 80;
/// A user message is shortened to fit the remaining budget only if at least this much is left.
const MIN_PARTIAL_TOKENS: u64 = 200;
const IMAGE_OMITTED: &str = "[image omitted]";
const SUMMARY_CUT: &str = "(The summary was cut off at the output limit.)";

#[derive(Debug)]
pub(crate) enum CompactError {
    Interrupted,
    Failed(ErrorInfo),
}

impl CompactError {
    fn failed(kind: &str, message: impl Into<String>) -> Self {
        CompactError::Failed(ErrorInfo::new(kind, message))
    }
}

/// Whether the context holds anything beyond the last checkpoint that a summary could replace.
pub(crate) fn can_compact(session: &Session) -> bool {
    session.with_branch(|branch| {
        context::normalize(context::branch_messages(branch)).len() > context::checkpoint_len(branch)
    })
}

/// Compacts the current context. `summarizer` overrides the model that writes the summary
/// (used after switching to a model whose window cannot hold the history).
pub(crate) async fn compact(
    session: &Arc<Session>,
    task: &TaskCtx,
    reason: CompactionReason,
    summarizer: Option<Arc<Model>>,
) -> Result<(), CompactError> {
    session.emit(EventMsg::CompactionStarted { reason });
    let result = run(session, task, reason, summarizer).await;
    if let Err(err) = &result {
        let error = match err {
            CompactError::Interrupted => ErrorInfo::new("interrupted", "compaction was interrupted"),
            CompactError::Failed(error) => error.clone(),
        };
        tracing::warn!(session = %session.id, reason = reason.as_str(), error = %error.message, "compaction failed");
        session.emit(EventMsg::CompactionFailed { reason, error });
    }
    result
}

async fn run(
    session: &Arc<Session>,
    task: &TaskCtx,
    reason: CompactionReason,
    summarizer: Option<Arc<Model>>,
) -> Result<(), CompactError> {
    let (current, thinking) = session.current_model();
    let model = summarizer.unwrap_or(current);
    let summarizer_limits = budget::limits(&model, thinking, &session.config.compaction);
    let before = session.budget();
    let ctx = session.build_context();
    let checkpoint = session.with_branch(context::checkpoint_len).min(ctx.messages.len());
    if ctx.messages.len() <= checkpoint {
        return Err(CompactError::failed("compaction", "there is nothing to compact"));
    }
    let mut input = Trimmable::new(ctx.messages.clone(), checkpoint);
    let mut tool_choice = ToolChoice::Auto;
    let mut usage = Usage::default();
    let mut trims = 0;
    let summary = loop {
        let mut request = ctx.clone();
        request.messages = input.messages();
        add_handoff_request(&mut request.messages);
        let options = RequestOptions {
            thinking,
            cache_key: Some(session.id.clone()),
            tool_choice,
            ..Default::default()
        };
        let reply = request_summary(session, task, &model, request, options).await?;
        if !reply.usage.is_empty() {
            usage.add(&reply.usage);
            session.add_usage(&model.reference(), &reply.usage);
            task.usage.lock().add(&model.reference(), &reply.usage);
        }
        match reply.stop_reason {
            StopReason::Aborted => return Err(CompactError::Interrupted),
            StopReason::Error => {
                let error = reply
                    .error
                    .as_deref()
                    .cloned()
                    .unwrap_or_else(|| LlmError::new(ErrorKind::Unknown, "the summary request failed"));
                if error.kind == ErrorKind::ContextOverflow && trims < MAX_TRIMS {
                    trims += 1;
                    let cap = summarizer_limits.hard * TRIM_TARGET_PERCENT / 100;
                    if input.trim(cap, session.fixed_tokens(), session.estimate_ratio()) {
                        continue;
                    }
                }
                return Err(CompactError::Failed(ErrorInfo::from(&error)));
            }
            StopReason::Stop | StopReason::ToolUse | StopReason::Length => {}
        }
        let text = reply.text();
        if text.trim().is_empty() {
            if tool_choice == ToolChoice::Auto {
                // Asked for a summary, the model called a tool instead. Insist; this request
                // may miss the cache, but it gets the summary.
                tool_choice = ToolChoice::None;
                continue;
            }
            return Err(CompactError::failed("compaction", "the model returned no summary"));
        }
        if reply.stop_reason == StopReason::Length {
            break format!("{}\n\n{SUMMARY_CUT}", text.trim());
        }
        break text.trim().to_string();
    };
    if input.dropped > 0 {
        session.warn(format!(
            "{} earlier message(s) did not fit in the summary request and are not covered by the summary",
            input.dropped
        ));
    }

    let limits = session.limits();
    let fixed = session.fixed_tokens();
    let ratio = session.estimate_ratio();
    let candidates = session.with_branch(retain_candidates);
    let (used, previous) = session.with_branch(|branch| {
        let previous = match context::last_compaction(branch).map(|i| &branch[i].kind) {
            Some(EntryKind::Compaction(checkpoint)) => checkpoint.skills.clone(),
            _ => Vec::new(),
        };
        let window = &branch[context::window_start(branch)..];
        (kept::used_files(window, session.env.as_ref()), previous)
    });
    let skills = session.state.lock().skills.clone();
    let rendered = kept::render(&kept::in_use(&used, &previous, &skills), &session.config.limits);
    let ts = now_ms();
    let mut retained_budget = budget::retained_budget(&session.config.compaction, &limits);
    let mut skill_budget = budget::skill_budget(&session.config.compaction, &limits);
    let (retained, kept_skills, tokens_after) = loop {
        let retained = select_retained(&candidates, retained_budget);
        let kept_skills = kept::select(&rendered, skill_budget);
        let mut messages: Vec<Message> = retained.iter().cloned().map(Message::from).collect();
        if !kept_skills.is_empty() {
            messages.push(Message::from(context::skills_message(&kept_skills, ts)));
        }
        messages.push(Message::from(context::summary_message(&summary, !retained.is_empty(), ts)));
        let after = budget::scale(fixed + context::estimate_tokens(&messages), ratio);
        if after < limits.soft || (retained_budget == 0 && skill_budget == 0) {
            break (retained, kept_skills, after);
        }
        retained_budget /= 2;
        skill_budget /= 2;
    };
    if tokens_after >= limits.hard {
        return Err(CompactError::failed(
            "context_overflow",
            format!(
                "the context does not fit even after compaction (about {tokens_after} tokens, the limit is {})",
                limits.hard
            ),
        ));
    }
    if tokens_after >= limits.soft {
        session.warn("the context is still large after compaction; consider a model with a larger window");
    }
    let seq = session.with_branch(|branch| {
        branch
            .iter()
            .filter(|e| matches!(e.kind, EntryKind::Compaction(_)))
            .count() as u32
    }) + 1;
    let retained_messages = retained.len() as u32;
    // Tool definitions found by searches are gone from the context with the old history.
    session.mcp_shown.lock().clear();
    let kept_names: Vec<String> = kept_skills.iter().map(|s| s.name.clone()).collect();
    session.record(EntryKind::Compaction(Box::new(CompactionEntry {
        seq,
        reason,
        summary: summary.clone(),
        retained,
        skills: kept_skills,
        model: model.reference(),
        usage,
        tokens_before: before.usage.tokens,
        tokens_after,
        dropped_messages: input.dropped,
    })));
    session.emit(EventMsg::CompactionCompleted {
        reason,
        seq,
        tokens_before: before.usage.tokens,
        tokens_after,
        retained_messages,
        kept_skills: kept_names,
        summary,
        context: session.budget().usage,
    });
    Ok(())
}

/// Adds the handoff request to the end of the context. After a tool result or a user message it
/// joins that message instead of opening a new user turn: some providers render the whole
/// tool-call sequence differently once a new user turn follows it (DeepSeek in thinking mode
/// drops the earlier reasoning), which loses the cached prefix. Only the last message changes,
/// and it is new since the previous request anyway.
fn add_handoff_request(messages: &mut Vec<Message>) {
    let text = UserContent::Text {
        text: format!("\n{}", context::HANDOFF_REQUEST),
    };
    match messages.last_mut() {
        Some(Message::ToolResult(result)) => Arc::make_mut(result).content.push(text),
        Some(Message::User(user)) => Arc::make_mut(user).content.push(text),
        _ => messages.push(Message::from(UserMessage::text(context::HANDOFF_REQUEST))),
    }
}

/// Whether a message carries the handoff request.
#[cfg(test)]
fn has_handoff_request(message: &Message) -> bool {
    let content = match message {
        Message::User(u) => &u.content,
        Message::ToolResult(r) => &r.content,
        Message::Assistant(_) => return false,
    };
    content
        .iter()
        .any(|c| matches!(c, UserContent::Text { text } if text.contains("<handoff_request>")))
}

/// Streams the summary request without forwarding deltas. Cancelling the task stops it.
async fn request_summary(
    session: &Session,
    task: &TaskCtx,
    model: &Arc<Model>,
    request: her_llm::Context,
    options: RequestOptions,
) -> Result<AssistantMessage, CompactError> {
    let mut stream = session.llm.stream(model.clone(), request, options);
    let mut acc = MessageAccumulator::for_model(model);
    loop {
        tokio::select! {
            biased;
            _ = task.cancel.cancelled() => return Err(CompactError::Interrupted),
            event = stream.next() => match event {
                None => return Ok(acc.fail(LlmError::new(ErrorKind::Protocol, "the model stream ended unexpectedly"))),
                Some(StreamEvent::Done(message)) | Some(StreamEvent::Failed(message)) => return Ok(message),
                Some(StreamEvent::Retrying { attempt, delay, error }) => {
                    acc = MessageAccumulator::for_model(model);
                    session.emit(EventMsg::StreamRetry {
                        attempt,
                        delay_ms: delay.as_millis() as u64,
                        error: ErrorInfo::from(&error),
                    });
                }
                Some(event) => acc.apply(&event),
            },
        }
    }
}

/// The summary request's messages: the previous checkpoint (kept whole), then groups that can
/// be dropped from the oldest end when the request is too large. A group is a user message,
/// or an assistant message with its tool results.
struct Trimmable {
    checkpoint: Vec<Message>,
    groups: Vec<Vec<Message>>,
    dropped: u32,
}

impl Trimmable {
    fn new(mut messages: Vec<Message>, checkpoint: usize) -> Self {
        let rest = messages.split_off(checkpoint);
        let mut groups: Vec<Vec<Message>> = Vec::new();
        for message in rest {
            match (&message, groups.last_mut()) {
                (Message::ToolResult(_), Some(group)) => group.push(message),
                _ => groups.push(vec![message]),
            }
        }
        Self {
            checkpoint: messages,
            groups,
            dropped: 0,
        }
    }

    fn messages(&self) -> Vec<Message> {
        let mut out = self.checkpoint.clone();
        if self.dropped > 0 {
            out.push(Message::from(UserMessage::text(format!(
                "[{} earlier message(s) were left out here because the conversation did not fit in one request.]",
                self.dropped
            ))));
        }
        out.extend(self.groups.iter().flatten().cloned());
        out
    }

    /// Drops the oldest groups until the estimate is at most `cap` and at most 80% of the
    /// current size, and at least one group. Tool work goes first; user messages are small,
    /// state the goal and go only when nothing else is left. The newest group always stays.
    /// Returns whether anything was dropped.
    fn trim(&mut self, cap: u64, fixed: u64, ratio: f64) -> bool {
        let Some(newest) = self.groups.len().checked_sub(1) else {
            return false;
        };
        let estimate = |messages: &[Message]| budget::scale(context::estimate_tokens(messages), ratio);
        let mut size = budget::scale(fixed, ratio) + estimate(&self.messages());
        let target = cap.min(size * TRIM_SHRINK_PERCENT / 100);
        let is_user = |group: &Vec<Message>| matches!(group.first(), Some(Message::User(_)));
        let mut drop = vec![false; self.groups.len()];
        let mut any = false;
        for users in [false, true] {
            for (index, group) in self.groups[..newest].iter().enumerate() {
                if is_user(group) != users || (any && size <= target) {
                    continue;
                }
                drop[index] = true;
                any = true;
                size = size.saturating_sub(estimate(group));
            }
        }
        if !any {
            return false;
        }
        let groups = std::mem::take(&mut self.groups);
        for (group, dropped) in groups.into_iter().zip(drop) {
            if dropped {
                self.dropped += group.len() as u32;
            } else {
                self.groups.push(group);
            }
        }
        true
    }
}

/// User messages that a compaction may keep, oldest first: those the last compaction kept,
/// then every user message after it.
fn retain_candidates(branch: &[&crate::store::Entry]) -> Vec<UserMessage> {
    let mut out = Vec::new();
    if let Some(index) = context::last_compaction(branch)
        && let EntryKind::Compaction(checkpoint) = &branch[index].kind
    {
        out.extend(checkpoint.retained.iter().cloned());
    }
    for entry in &branch[context::window_start(branch)..] {
        // The user's own words: skill instructions are kept apart (docs/kernel/skill-design.md §7.2).
        if let EntryKind::Message {
            message: message @ Message::User(_),
            skill,
            ..
        } = &entry.kind
            && let Message::User(user) = context::written(message, skill.as_deref())
        {
            out.push(user.as_ref().clone());
        }
    }
    out
}

/// The newest user messages that fit in `budget` tokens, oldest first. Images become a
/// placeholder; the message that does not fit whole is shortened in the middle.
pub(crate) fn select_retained(candidates: &[UserMessage], budget: u64) -> Vec<UserMessage> {
    let mut out = Vec::new();
    let mut remaining = budget;
    for message in candidates.iter().rev() {
        let message = text_only(message);
        let tokens = context::estimate_user_content(&message.content);
        if tokens <= remaining {
            remaining -= tokens;
            out.push(message);
            continue;
        }
        if remaining >= MIN_PARTIAL_TOKENS {
            out.push(shorten(&message, remaining));
        }
        break;
    }
    out.reverse();
    out
}

fn text_only(message: &UserMessage) -> UserMessage {
    let content = message
        .content
        .iter()
        .map(|c| match c {
            UserContent::Text { text } => UserContent::Text { text: text.clone() },
            UserContent::Image { .. } => UserContent::Text {
                text: IMAGE_OMITTED.to_string(),
            },
        })
        .collect();
    UserMessage { content, ts: message.ts }
}

/// Shortens a message to about `tokens`, keeping its beginning and end.
fn shorten(message: &UserMessage, tokens: u64) -> UserMessage {
    let text = message.text_content();
    let total = context::estimate_text(&text).max(1);
    let chars: Vec<char> = text.chars().collect();
    // Leave a little room for the marker.
    let keep = ((chars.len() as u128 * tokens.saturating_sub(16) as u128 / total as u128) as usize).min(chars.len());
    let head = keep / 2;
    let tail = keep - head;
    let omitted = chars.len() - keep;
    let shortened = format!(
        "{}\n…[{omitted} characters omitted]…\n{}",
        chars[..head].iter().collect::<String>(),
        chars[chars.len() - tail..].iter().collect::<String>()
    );
    UserMessage {
        content: vec![UserContent::Text { text: shortened }],
        ts: message.ts,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn user(text: &str) -> UserMessage {
        UserMessage::text(text)
    }

    #[test]
    fn keeps_the_newest_messages_that_fit() {
        let candidates = vec![user(&"a".repeat(400)), user(&"b".repeat(400)), user(&"c".repeat(400))];
        // 100 tokens each.
        let kept = select_retained(&candidates, 250);
        assert_eq!(kept.len(), 2);
        assert!(kept[0].text_content().starts_with('b'));
        assert!(kept[1].text_content().starts_with('c'));
        assert!(select_retained(&candidates, 0).is_empty());
    }

    #[test]
    fn shortens_the_message_that_does_not_fit() {
        let long = format!("{}{}", "x".repeat(4000), "y".repeat(4000));
        let kept = select_retained(&[user(&long), user("latest")], 500);
        assert_eq!(kept.len(), 2);
        let text = kept[0].text_content();
        assert!(text.starts_with("xxx") && text.ends_with("yyy"));
        assert!(text.contains("characters omitted]"));
        assert!(context::estimate_text(&text) <= 500, "{}", context::estimate_text(&text));
        assert_eq!(kept[1].text_content(), "latest");
    }

    #[test]
    fn the_handoff_request_joins_the_last_message_unless_it_is_a_reply() {
        use her_llm::ToolResultMessage;
        let result = Message::from(ToolResultMessage {
            call_id: "c".into(),
            tool_name: "bash".into(),
            content: vec![UserContent::Text { text: "out".into() }],
            is_error: false,
            details: None,
            ts: 0,
            duration_ms: None,
        });
        let history = vec![Message::from(user("go")), result];
        let mut request = history.clone();
        add_handoff_request(&mut request);
        assert_eq!(request.len(), 2, "no new user turn after a tool result");
        assert!(has_handoff_request(&request[1]));
        assert!(!has_handoff_request(&history[1]), "the history itself is untouched");

        let mut after_user = vec![Message::from(user("new task"))];
        add_handoff_request(&mut after_user);
        assert_eq!(after_user.len(), 1);
        assert!(has_handoff_request(&after_user[0]));

        let reply = Message::from(AssistantMessage {
            content: vec![her_llm::AssistantContent::Text { text: "done".into() }],
            provider: "p".into(),
            api: "a".into(),
            model: "m".into(),
            response_id: None,
            usage: Usage::default(),
            stop_reason: StopReason::Stop,
            error: None,
            ts: 0,
            duration_ms: None,
        });
        let mut after_reply = vec![reply];
        add_handoff_request(&mut after_reply);
        assert_eq!(after_reply.len(), 2);
        assert!(has_handoff_request(&after_reply[1]));
    }

    #[test]
    fn images_become_placeholders() {
        let message = UserMessage {
            content: vec![
                UserContent::Text { text: "look".into() },
                UserContent::Image {
                    mime_type: "image/png".into(),
                    data: "AAAA".into(),
                },
            ],
            ts: 1,
        };
        let kept = select_retained(&[message], 1_000);
        assert_eq!(kept[0].text_content(), format!("look\n{IMAGE_OMITTED}"));
    }

    #[test]
    fn trimming_drops_whole_groups_and_keeps_the_newest() {
        use her_llm::AssistantContent;
        use her_llm::ToolCall;
        use her_llm::ToolResultMessage;
        let call = |id: &str| {
            Message::from(AssistantMessage {
                content: vec![AssistantContent::ToolCall(ToolCall {
                    id: id.into(),
                    name: "bash".into(),
                    arguments: serde_json::json!({}).into(),
                    arguments_error: None,
                    provider_data: None,
                })],
                provider: "p".into(),
                api: "a".into(),
                model: "m".into(),
                response_id: None,
                usage: Usage::default(),
                stop_reason: StopReason::ToolUse,
                error: None,
                ts: 0,
                duration_ms: None,
            })
        };
        let result = |id: &str| {
            Message::from(ToolResultMessage {
                call_id: id.into(),
                tool_name: "bash".into(),
                content: vec![UserContent::Text { text: "r".repeat(4000) }],
                is_error: false,
                details: None,
                ts: 0,
                duration_ms: None,
            })
        };
        let messages = vec![
            Message::from(user("kept")),
            Message::from(user("summary")),
            Message::from(user("task")),
            call("1"),
            result("1"),
            call("2"),
            result("2"),
        ];
        let mut input = Trimmable::new(messages, 2);
        assert_eq!(input.groups.len(), 3);
        assert!(input.trim(1_100, 0, 1.0));
        let out = input.messages();
        // The checkpoint stays, a note marks the gap, the user's message stays, the newest
        // group is whole.
        let shape: Vec<&str> = out
            .iter()
            .map(|m| match m {
                Message::User(_) => "user",
                Message::Assistant(_) => "assistant",
                Message::ToolResult(_) => "tool",
            })
            .collect();
        assert_eq!(shape, ["user", "user", "user", "user", "assistant", "tool"]);
        assert!(matches!(&out[2], Message::User(u) if u.text_content().contains("2 earlier message(s)")));
        assert!(matches!(&out[3], Message::User(u) if u.text_content() == "task"));
        assert_eq!(input.dropped, 2);
        // Only the user message is left to drop, then nothing.
        assert!(input.trim(u64::MAX, 0, 1.0));
        assert_eq!(input.dropped, 3);
        assert!(!input.trim(0, 0, 1.0), "the newest group is never dropped");
    }
}
