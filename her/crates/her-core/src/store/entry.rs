//! Session log records. The first line of a log is a header; every other line is an entry.

use her_llm::Message;
use her_llm::ThinkingLevel;
use her_llm::Timestamp;
use her_llm::Usage;
use her_llm::UserMessage;
use her_llm::UsageTotals;
use her_protocol::CompactionReason;
use her_protocol::CompletionReason;
use her_protocol::MailInfo;
use her_protocol::PermissionMode;
use her_protocol::ErrorInfo;
use serde::Deserialize;
use serde::Serialize;
use std::path::PathBuf;
use std::sync::Arc;

use crate::skills::SkillRef;

/// 2: compaction entries carry the retained user messages. Version 1 logs read unchanged.
pub const LOG_VERSION: u32 = 2;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum HeaderKind {
    Session,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct SessionHeader {
    #[serde(rename = "type")]
    pub kind: HeaderKind,
    pub version: u32,
    pub id: String,
    pub created_at: String,
    pub cwd: PathBuf,
    #[serde(alias = "fx_version")] // logs written before the rename
    pub her_version: String,
    /// Set for a sub-agent's session: who started it. Such sessions stay out of session lists.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub agent: Option<AgentOrigin>,
    /// Set for a bot's session (bot-design §2.2). Such sessions stay out of session lists too.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bot: Option<BotRef>,
}

/// The bot a session belongs to.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct BotRef {
    pub id: String,
    /// Its name when the session started, which the system prompt gives; later names come as
    /// `bot_name` entries.
    pub name: String,
}

/// Where a sub-agent's session comes from.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct AgentOrigin {
    pub root_id: String,
    pub parent_id: String,
    pub name: String,
    pub depth: u32,
    /// The `spawn_agent` call in the parent's session.
    pub call_id: String,
    /// The parent's task at that time.
    pub parent_task: String,
    /// A routine's run (routine-design §2.1): the routine. No `spawn_agent` call started it, and
    /// the parent's log has no mention of it unless it reports.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub routine: Option<String>,
}

impl SessionHeader {
    pub fn new(id: String, cwd: PathBuf) -> Self {
        Self {
            kind: HeaderKind::Session,
            version: LOG_VERSION,
            id,
            created_at: chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Secs, true),
            cwd,
            her_version: env!("CARGO_PKG_VERSION").to_string(),
            agent: None,
            bot: None,
        }
    }

    /// Whether session lists show the session: neither a sub-agent's nor a bot's.
    pub fn listed(&self) -> bool {
        self.agent.is_none() && self.bot.is_none()
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Entry {
    /// Unique within the session.
    pub id: String,
    pub parent: Option<String>,
    pub ts: Timestamp,
    #[serde(flatten)]
    pub kind: EntryKind,
}

/// Nearly every entry is a message, which is a pointer. The rare large kinds are boxed, so
/// that an entry takes 112 bytes rather than 256; a long session holds thousands.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum EntryKind {
    Message {
        /// For a user message, the user's own words, as written.
        message: Message,
        /// Skills the user mentioned with `/skill:name`: their instructions go before the
        /// message in the context (docs/kernel/skill-design.md §6.3.4).
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        skills: Vec<SkillText>,
        /// Written by phase 2 only: the `/name` whose instructions replaced the message's first
        /// text, which `message` holds as the model saw it.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        skill: Option<Box<ExpandedSkill>>,
    },
    /// The system prompt. `text` is the part fixed when the session started; `mcp` is the MCP
    /// part (server catalog and instructions), which comes after it and may change later.
    SystemPrompt(Box<SystemPromptEntry>),
    Settings {
        #[serde(default, skip_serializing_if = "Option::is_none")]
        model: Option<String>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        thinking: Option<ThinkingLevel>,
        #[serde(default, skip_serializing_if = "Option::is_none", alias = "approval_mode")]
        permission_mode: Option<PermissionMode>,
    },
    /// A checkpoint: the context continues from `retained` and `summary` instead of the
    /// messages before this entry, which stay in the log.
    Compaction(Box<CompactionEntry>),
    Task {
        task_id: String,
        status: TaskStatus,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        error: Option<Box<ErrorInfo>>,
        /// Why a completed task ended; absent in logs written before it was recorded.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        reason: Option<CompletionReason>,
        /// What the task used, at its end; absent before it was recorded.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        usage: Option<Box<UsageTotals>>,
    },
    /// This session started a sub-agent.
    AgentSpawned(Box<AgentSpawnedEntry>),
    /// A sub-agent's report, delivered: `message` is what the model sees.
    Mail {
        mail: Box<MailInfo>,
        message: Message,
    },
    /// What a sub-agent sent while it worked, delivered: `message` is what the model sees. An
    /// entry of its own rather than another kind of `Mail`: a version that doesn't know it keeps
    /// its place in the branch, where a `Mail` it cannot read would cut the branch there
    /// (agent-messaging-design §8).
    AgentMessage {
        mail: Box<MailInfo>,
        message: Message,
    },
    /// What a bot's sub-agent told the user: `message` is what the bot's model reads of it. An
    /// entry of its own for the same reason as `AgentMessage`
    /// (agent-messaging-design §17).
    AgentToUser {
        mail: Box<MailInfo>,
        message: Message,
    },
    /// Marks a rewind: the parent is the point the branch continues from.
    Branch {
        /// It went back before the first user message: the next one is the session's first.
        /// Session lists go by this, without following parents.
        #[serde(default, skip_serializing_if = "std::ops::Not::not")]
        before_first: bool,
    },
    Title {
        title: String,
    },
    /// The session was archived (`true`) or brought back (`false`); the last one counts.
    Archived {
        archived: bool,
    },
    /// The date changed since the context last gave it: the model is told the new one
    /// (technical-design §7.2).
    Date {
        /// `YYYY-MM-DD`
        date: String,
    },
    /// A bot's session: the bot's name changed since the context last gave it; the model is
    /// told the new one (bot-design §2.4).
    BotName {
        name: String,
    },
    /// A bot's session: its routines changed since the context last gave them; the model is told
    /// the new list (routine-design §4.4).
    Routines {
        text: String,
    },
    /// A bot's session: its task was about to end with the owner's message unanswered, and the
    /// model is reminded to reply, once per task (bot-design §3.11).
    Unanswered {
        /// What waits is a routine's report rather than the owner's message (routine-design §2.4).
        #[serde(default, skip_serializing_if = "std::ops::Not::not")]
        routine: bool,
    },
    /// A bot's session: its task was about to end with what came of its work unsent, and the
    /// model is reminded of it, once per task (bot-turn-design §6).
    Undelivered,
    /// A bot's session: follows each message from its owner. Models often answer in plain text
    /// whatever the system prompt says; a word right after the message mostly settles it
    /// (bot-design §3.14).
    ReplyNudge,
    /// A bot's session: how a secure card ended, which the model reads as `<card …>`
    /// (docs/her-secure-cards.md §3.4). Never a value the owner typed.
    CardResolved(Box<CardResolvedEntry>),
    /// A kind written by a later version. It keeps its place in the branch, so the entries
    /// after it still link up; nothing else reads it.
    #[serde(other, skip_serializing)]
    Unknown,
}

/// The data of an [`EntryKind::Compaction`].
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct CompactionEntry {
    /// The session's compaction count, from 1.
    pub seq: u32,
    pub reason: CompactionReason,
    /// The handoff summary as the model wrote it.
    pub summary: String,
    /// User messages kept word for word, oldest first; text only, possibly shortened.
    pub retained: Vec<UserMessage>,
    /// Skills in use whose instructions the context keeps, most recently used first.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub skills: Vec<SkillText>,
    /// The model that wrote the summary (`provider/model`).
    pub model: String,
    /// Usage of the summary request, counted in the session's totals.
    pub usage: Usage,
    pub tokens_before: u64,
    /// Estimated size of the new context.
    pub tokens_after: u64,
    /// Messages left out of the summary request because it did not fit.
    #[serde(default, skip_serializing_if = "is_zero")]
    pub dropped_messages: u32,
}

/// The data of an [`EntryKind::SystemPrompt`].
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct SystemPromptEntry {
    /// Shared with the session that uses it.
    pub text: Arc<str>,
    pub tools: Vec<String>,
    /// The session's skills, those the catalog in `text` lists and those only the user may
    /// invoke.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub skills: Vec<SkillRef>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub mcp: Option<String>,
    /// A bot's session: which `bot.md` the text comes from (bot-turn-design §8).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub template: Option<String>,
}

/// The data of an [`EntryKind::CardResolved`]. Its kind and outcome are kept as words, so that a
/// log with the kinds and outcomes of a later version still reads.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct CardResolvedEntry {
    pub card_id: String,
    pub kind: String,
    /// The state it ended in.
    pub outcome: String,
    /// her's words on how it went.
    pub note: String,
}

/// The data of an [`EntryKind::AgentSpawned`].
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct AgentSpawnedEntry {
    pub agent_id: String,
    pub name: String,
    /// What the user sees for it; agents from before the label have none.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub description: String,
    pub session_file: PathBuf,
    pub call_id: String,
}

impl EntryKind {
    /// A message entry that mentions no skill: a reply, a tool result, a plain user message.
    pub fn message(message: Message) -> Self {
        EntryKind::Message {
            message,
            skills: Vec::new(),
            skill: None,
        }
    }
}

/// A skill's instructions as the model receives them, its `<skill>` block, recorded so that the
/// context can be rebuilt from the log alone: for a mention (docs/kernel/skill-design.md §6.3.4) and
/// for a skill kept through a compaction (§7.2).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SkillText {
    pub name: String,
    pub path: PathBuf,
    pub content: String,
}

/// A phase 2 invocation: `input` is what the user typed, `/name` and its arguments.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ExpandedSkill {
    pub name: String,
    pub path: PathBuf,
    pub input: String,
}

fn is_zero(n: &u32) -> bool {
    *n == 0
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TaskStatus {
    Started,
    Completed,
    Failed,
    Interrupted,
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_llm::UserMessage;

    #[test]
    fn entries_serialize_flat() {
        let entry = Entry {
            id: "a1b2c3d4".into(),
            parent: None,
            ts: 5,
            kind: EntryKind::Message {
                message: Message::from(UserMessage {
                    content: vec![her_llm::UserContent::Text { text: "hi".into() }],
                    ts: 5,
                }),
                skills: Vec::new(),
                skill: None,
            },
        };
        let json = serde_json::to_string(&entry).unwrap();
        assert_eq!(
            json,
            r#"{"id":"a1b2c3d4","parent":null,"ts":5,"type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"}],"ts":5}}"#
        );
        let back: Entry = serde_json::from_str(&json).unwrap();
        assert_eq!(back, entry);
        let branch: Entry = serde_json::from_str(r#"{"id":"x","parent":"y","ts":1,"type":"branch"}"#).unwrap();
        assert_eq!(branch.kind, EntryKind::Branch { before_first: false });
    }

    #[test]
    fn logs_from_before_the_rename_still_load() {
        let settings: Entry =
            serde_json::from_str(r#"{"id":"x","parent":null,"ts":1,"type":"settings","approval_mode":"full-auto"}"#)
                .unwrap();
        assert_eq!(
            settings.kind,
            EntryKind::Settings {
                model: None,
                thinking: None,
                permission_mode: Some(PermissionMode::BypassPermissions),
            }
        );
        let json = serde_json::to_string(&settings).unwrap();
        assert!(json.ends_with(r#""type":"settings","permission_mode":"bypass-permissions"}"#), "{json}");
    }

    /// One line of each entry type with nested data, as logs hold them: read and written
    /// back unchanged.
    #[test]
    fn every_entry_type_reads_and_writes_back_unchanged() {
        let lines = [
            r#"{"id":"8984ef61","parent":"f459afc5","ts":1793224075025,"type":"task","task_id":"t1","status":"completed","reason":"done","usage":{"per_model":{"mock/mock-1":{"input":59949,"output":2443,"cache_read":0,"cache_write":0,"cost":{"currency":"USD","input":0.059949,"output":0.004886,"cache_read":0.0,"cache_write":0.0,"total":0.064835}}}}}"#,
            r#"{"id":"aad29609","parent":"a4947bc2","ts":1793224073137,"type":"task","task_id":"t2","status":"started"}"#,
            r#"{"id":"aad29610","parent":"aad29609","ts":1793224073138,"type":"task","task_id":"t2","status":"failed","error":{"kind":"network","message":"stream interrupted","status":502,"retryable":true}}"#,
            r#"{"id":"541a9253","parent":"2dbb9631","ts":1793224076428,"type":"compaction","seq":1,"reason":"threshold","summary":"摘要","retained":[{"content":[{"type":"text","text":"[[mock:agent:s0t0]] 第 0 轮：检查 0 号模块并更新报告"}],"ts":1793224074949}],"model":"mock/mock-1","usage":{"input":151983,"output":29,"cache_read":0,"cache_write":0,"cost":{"currency":"USD","input":0.151983,"output":0.000058,"cache_read":0.0,"cache_write":0.0,"total":0.152041}},"tokens_before":151667,"tokens_after":2417}"#,
            r#"{"id":"541a9254","parent":"541a9253","ts":1793224076429,"type":"compaction","seq":2,"reason":"overflow","summary":"s","retained":[],"model":"m/x","usage":{"input":1,"output":2,"cache_read":0,"cache_write":0,"cost":{"currency":"USD","input":0.0,"output":0.0,"cache_read":0.0,"cache_write":0.0,"total":0.0}},"tokens_before":9,"tokens_after":3,"dropped_messages":4}"#,
            r#"{"id":"2787b99f","parent":"ba255377","ts":1793224073141,"type":"agent_spawned","agent_id":"01a1847b-dfb5-729b-a322-ae11d3728a76","name":"a0","session_file":"/tmp/sessions/2026/10/29/20261029T054753-01a1847b.jsonl","call_id":"call_1"}"#,
            r#"{"id":"2787b9a0","parent":"2787b99f","ts":1793224073142,"type":"mail","mail":{"id":"m1","from":{"id":"01a1847b","name":"a0"},"kind":"report","status":"completed"},"message":{"role":"user","content":[{"type":"text","text":"report"}],"ts":3}}"#,
            r#"{"id":"2787b9a7","parent":"2787b9a0","ts":1793224073142,"type":"agent_message","mail":{"id":"m2","from":{"id":"01a1847b","name":"a0"},"kind":"message","status":"running"},"message":{"role":"user","content":[{"type":"text","text":"note"}],"ts":3}}"#,
            r#"{"id":"2787b9a1","parent":"2787b9a0","ts":1793224073143,"type":"system_prompt","text":"be brief","tools":["read","bash"]}"#,
            r#"{"id":"2787b9a3","parent":"2787b9a1","ts":1793224073145,"type":"system_prompt","text":"be brief","tools":["read"],"skills":[{"name":"review","path":"/p/.her/skills/review/SKILL.md","scope":"project","model_invocable":true,"argument_hint":"[file]"}],"mcp":"MCP servers: vault"}"#,
            r#"{"id":"2787b9a4","parent":"2787b9a3","ts":1793224073146,"type":"message","message":{"role":"user","content":[{"type":"text","text":"<skill>"}],"ts":3},"skill":{"name":"review","path":"/p/.her/skills/review/SKILL.md","input":"/review a.rs"}}"#,
            r#"{"id":"2787b9a5","parent":"2787b9a4","ts":1793224073147,"type":"compaction","seq":3,"reason":"threshold","summary":"s","retained":[],"skills":[{"name":"review","path":"/p/.her/skills/review/SKILL.md","content":"<skill>"}],"model":"m/x","usage":{"input":1,"output":2,"cache_read":0,"cache_write":0,"cost":{"currency":"USD","input":0.0,"output":0.0,"cache_read":0.0,"cache_write":0.0,"total":0.0}},"tokens_before":9,"tokens_after":3}"#,
            r#"{"id":"2787b9a2","parent":"2787b9a1","ts":1793224073144,"type":"settings","model":"m/x","thinking":"high","permission_mode":"manual"}"#,
            r#"{"id":"2787b9a6","parent":"2787b9a5","ts":1793224073148,"type":"date","date":"2026-11-03"}"#,
            r#"{"id":"2787b9a8","parent":"2787b9a6","ts":1793224073149,"type":"card_resolved","card_id":"c_k3j9x2m4qa","kind":"connect","outcome":"done","note":"Notion connected: 14 tools (notion_search, …)."}"#,
        ];
        for line in lines {
            let entry: Entry = serde_json::from_str(line).unwrap_or_else(|e| panic!("{e}: {line}"));
            assert_eq!(serde_json::to_string(&entry).unwrap(), line);
        }
    }

    #[test]
    fn kinds_of_later_versions_keep_their_place_and_are_never_written() {
        let line = r#"{"id":"b","parent":"a","ts":7,"type":"from_a_later_version","data":{"n":1}}"#;
        let entry: Entry = serde_json::from_str(line).unwrap();
        assert_eq!((entry.id.as_str(), entry.parent.as_deref(), entry.kind.clone()), ("b", Some("a"), EntryKind::Unknown));
        assert!(serde_json::to_string(&entry).is_err(), "its data is gone: writing it would lose it");
        // A known kind with broken data is still unreadable.
        assert!(serde_json::from_str::<Entry>(r#"{"id":"c","parent":"b","ts":8,"type":"date"}"#).is_err());
    }
}
