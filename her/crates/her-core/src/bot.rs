//! Bots (bot-design): a bot is a name, an avatar and one session. Its session has one tool
//! of its own, `send_to_user`, the only way what it writes reaches the user: words, files and
//! images, a question with answers to tap (§3.12), or a secure card a tool prepared
//! (docs/her-secure-cards.md §2.1).

use async_trait::async_trait;
use her_llm::Message;
use her_protocol::AgentStatus;
use her_protocol::card::Card;
use her_tools::Environment;
use her_tools::Tool;
use her_tools::ToolCategory;
use her_tools::ToolCtx;
use her_tools::ToolDescription;
use her_tools::ToolError;
use her_tools::ToolOutput;
use her_tools::Typed;
use her_tools::TypedTool;
use her_tools::builtin::read::find_path_variant;
use her_tools::image::ImageFormat;
use schemars::JsonSchema;
use serde::Deserialize;
use serde_json::Value;
use serde_json::json;
use std::collections::HashSet;
use std::io;
use std::path::Path;
use std::sync::Arc;

use crate::cards::Cards;
use crate::files::MAX_IMAGE;
use crate::history::History;
use crate::store::EntryKind;
use her_protocol::MailKind;

/// The bot a session is started for: its id, and its name now.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BotIdentity {
    pub id: String,
    pub name: String,
}

pub const SEND_TO_USER: &str = "send_to_user";

/// The tools a bot's session gets on top of a session's. Its sub-agents have a `send_to_user` of
/// their own (agent-messaging-design §17).
pub(crate) fn tools() -> Vec<Arc<dyn Tool>> {
    vec![Arc::new(Typed::new(SendToUser)), crate::routine::tool()]
}

/// What a bot about to end its turn still owes its owner.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Owed {
    Nothing,
    /// Nothing reached them since their latest message (bot-design §3.11). The message may be an
    /// earlier task's, as when a failed task is retried.
    Reply,
    /// Nothing reached them since a routine's report, which they asked for, unless the bot's reply
    /// says it holds nothing to tell (routine-design §2.4, D-441).
    RoutineReport,
    /// Something came of its work since its last message: a sub-agent's report or message, or the
    /// results of its tools (bot-turn-design §6). It may have nothing worth telling.
    Delivery,
}

impl Owed {
    /// The reminder for what must not go unanswered.
    pub(crate) fn reminder(self) -> Option<EntryKind> {
        match self {
            Owed::Reply => Some(EntryKind::Unanswered { routine: false }),
            Owed::RoutineReport => Some(EntryKind::Unanswered { routine: true }),
            Owed::Nothing | Owed::Delivery => None,
        }
    }
}

/// Looks back from the end of the history to the last message that reached the owner.
pub(crate) fn owed(history: &History) -> Owed {
    let mut worked = false;
    let mut last_reply = None;
    for entry in history.branch_back() {
        match &entry.kind {
            EntryKind::Message {
                message: Message::ToolResult(result),
                ..
            } => {
                if result.tool_name == SEND_TO_USER && !result.is_error {
                    break;
                }
                // Starting an agent or telling it something gives the owner nothing to hear, and a
                // message the tool refused is nothing that came of the work.
                worked |= !matches!(result.tool_name.as_str(), "spawn_agent" | "send_message" | SEND_TO_USER);
            }
            EntryKind::Message {
                message: Message::User(_), ..
            } => return Owed::Reply,
            EntryKind::Message {
                message: Message::Assistant(reply),
                ..
            } if last_reply.is_none() => last_reply = Some(reply),
            EntryKind::Mail { mail, .. } if mail.kind == MailKind::Routine => {
                if !last_reply.is_some_and(|reply| crate::routine::quiet_note(&reply.text()).is_some()) {
                    return Owed::RoutineReport;
                }
            }
            // A stopped agent's report wakes nobody, and nobody waits for it.
            EntryKind::Mail { mail, .. } => worked |= mail.status != AgentStatus::Interrupted,
            EntryKind::AgentMessage { .. } | EntryKind::CardResolved(_) => worked = true,
            _ => {}
        }
    }
    // Its reply can say there is nothing to tell (routine-design D-441).
    let settled = last_reply.is_some_and(|reply| crate::routine::quiet_note(&reply.text()).is_some());
    if worked && !settled { Owed::Delivery } else { Owed::Nothing }
}

/// The words a call sends the owner: no blank ones, and not the phrase that is between the kernel
/// and the model, which the tool refuses (routine-design D-441).
pub fn words_of(args: &Value) -> Option<&str> {
    args.get("text")
        .and_then(Value::as_str)
        .filter(|text| !text.trim().is_empty() && crate::routine::quiet_note(text).is_none())
}

/// The words a sub-agent told the owner, from what the bot's model reads of them
/// (agent-messaging-design §17).
pub fn told_words(text: &str) -> &str {
    text.strip_prefix("<sent_to_user")
        .and_then(|rest| rest.split_once(">\n"))
        .and_then(|(_, rest)| rest.strip_suffix("\n</sent_to_user>"))
        .unwrap_or(text)
}

/// Whether a call sends files: by its type, or without one by what it carries (bot-design §3.12).
pub fn sends_files(args: &Value) -> bool {
    match args.get("type").and_then(Value::as_str) {
        Some(kind) => kind == "attachment",
        None => args.get("files").and_then(Value::as_array).is_some_and(|files| !files.is_empty()),
    }
}

/// The card a message showed and the words above it, from its result's details. None for any
/// other message.
pub fn shown_card(details: &str) -> Option<(Card, Option<String>)> {
    let mut details: Value = serde_json::from_str(details).ok()?;
    let card = serde_json::from_value(details.get_mut("card")?.take()).ok()?;
    Some((card, details.get("text").and_then(Value::as_str).map(str::to_string)))
}

/// What a message of files carried, from its result's details (bot-design §3.12): its words, how
/// many images, and the name of its first other file. None for any other message.
pub fn attachment_summary(details: &str) -> Option<(String, u32, Option<String>)> {
    let details: Value = serde_json::from_str(details).ok()?;
    let files = details.get("files")?.as_array()?;
    let text = details.get("text").and_then(Value::as_str).unwrap_or_default().to_string();
    let images = files.iter().filter(|f| f.get("image").is_some()).count();
    let file = files
        .iter()
        .find(|f| f.get("image").is_none())
        .and_then(|f| Path::new(f.get("path")?.as_str()?).file_name()?.to_str())
        .map(str::to_string);
    Some((text, images as u32, file))
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize, JsonSchema)]
#[serde(rename_all = "lowercase")]
enum MessageType {
    Text,
    Attachment,
    Question,
    Card,
}

#[derive(Debug, Deserialize, JsonSchema)]
struct FileArg {
    /// A file on this computer, absolute or relative to your folder. Save anything from the web to a file first.
    path: String,
}

#[derive(Debug, Deserialize, JsonSchema)]
struct OptionArg {
    /// The answer.
    label: String,
}

#[derive(Debug, Deserialize, JsonSchema)]
pub(crate) struct SendArgs {
    /// What this message is. text (the default): words, rendered as Markdown. attachment: one or more files or images, with the message that goes with them, however long. question: a question, when you need the user to decide; it ends your turn and their answer comes back as their next message; send any background first as a text message. card: a card a tool prepared for the user to answer, with your words above it; it ends your turn, and you hear how it ends.
    #[serde(default, rename = "type")]
    kind: Option<MessageType>,
    /// The message. For attachment, the message shown above the files. For question, the question in one sentence.
    #[serde(default)]
    text: Option<String>,
    /// For attachment, the files to hand over, in order.
    #[serde(default)]
    files: Option<Vec<FileArg>>,
    /// For question, answers the user can tap, each worded the way they would say it. Leave it out when the answers cannot be listed.
    #[serde(default)]
    options: Option<Vec<OptionArg>>,
    /// For card, the card's id, as the tool that prepared it gave.
    #[serde(default)]
    card: Option<String>,
    /// True on your last message, once your work for now is done: the turn ends right after it. Leave it out for a first reply or an update while you keep working.
    #[serde(default)]
    end_turn: bool,
}

/// What the model hears of a card it showed: the owner's answer comes later, as a `<card>` message.
const CARD_SHOWN: &str = "Shown to the owner. You will hear how it ends; until then, don't say it is done.";

/// Enough of a file for an image's size in its header.
const IMAGE_HEAD: u64 = 256 * 1024;

fn invalid(message: impl Into<String>) -> ToolError {
    ToolError::InvalidArgs(message.into())
}

/// A file's entry in the result's details, for user interfaces: where it is, how large, and for
/// an image the panel can show (by its contents, like the file panel) its size. Or why it cannot
/// be sent. Looked at when the call runs: the model may make the file in the same step.
async fn sent_file(env: &dyn Environment, raw: &str) -> Result<Value, String> {
    if raw.starts_with("http://") || raw.starts_with("https://") {
        return Err(format!("{raw}: a web address, save it to a file first"));
    }
    let mut path = env.resolve(raw);
    let mut meta = env.metadata(&path).await;
    if matches!(&meta, Err(err) if err.kind() == io::ErrorKind::NotFound)
        && let Some(found) = find_path_variant(env, raw).await
    {
        meta = env.metadata(&found).await;
        path = found;
    }
    let meta = match meta {
        Ok(meta) => meta,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Err(format!("{raw}: no such file")),
        Err(err) => return Err(format!("{raw}: {err}")),
    };
    if !meta.is_file {
        return Err(format!("{raw}: not a file"));
    }
    let mut file = json!({ "path": path, "size": meta.size });
    if meta.size <= MAX_IMAGE
        && let Ok(head) = env.read_file(&path, IMAGE_HEAD).await
        && let Some(format) = ImageFormat::detect(&head.bytes)
    {
        file["image"] = json!(true);
        if let Some((width, height)) = format.dimensions(&head.bytes) {
            file["width"] = json!(width);
            file["height"] = json!(height);
        }
    }
    Ok(file)
}

struct SendToUser;

#[async_trait]
impl TypedTool for SendToUser {
    type Args = SendArgs;
    const NAME: &'static str = SEND_TO_USER;
    const DESCRIPTION: &'static str = "Send a message to the user. It is the only way they see anything from you: each call \
        shows up as one message, all at once.";

    fn describe(&self, args: &Value, _env: &dyn Environment) -> ToolDescription {
        let first = args
            .get("text")
            .and_then(Value::as_str)
            .and_then(|text| text.lines().map(str::trim).find(|line| !line.is_empty()))
            .map(str::to_string);
        let files = || {
            let names: Vec<&str> = args
                .get("files")?
                .as_array()?
                .iter()
                .filter_map(|f| Path::new(f.get("path")?.as_str()?).file_name()?.to_str())
                .collect();
            (!names.is_empty()).then(|| names.join(", "))
        };
        ToolDescription {
            category: ToolCategory::Other,
            title: match first.or_else(files) {
                Some(line) => line.chars().take(80).collect(),
                None => SEND_TO_USER.to_string(),
            },
            locations: Vec::new(),
        }
    }

    async fn run(&self, args: SendArgs, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        // An empty value is no value: some models fill in every optional parameter.
        let text = args.text.as_deref().filter(|text| !text.trim().is_empty());
        // Between the kernel and the model: the owner never reads it (routine-design D-441).
        if text.is_some_and(|text| crate::routine::quiet_note(text).is_some()) {
            return Err(invalid("NOTHING_TO_REPORT is not a message for the user: send nothing and write it as plain text"));
        }
        let files = args.files.unwrap_or_default();
        let options = args.options.unwrap_or_default();
        let card = args.card.as_deref().map(str::trim).filter(|id| !id.is_empty());
        let sent = ToolOutput::text("Sent.");
        // Without a type, what it carries says what it is.
        let kind = args.kind.unwrap_or(if !files.is_empty() {
            MessageType::Attachment
        } else if !options.is_empty() {
            MessageType::Question
        } else if card.is_some() {
            MessageType::Card
        } else {
            MessageType::Text
        });
        if kind != MessageType::Attachment && !files.is_empty() {
            return Err(invalid("files goes with type attachment"));
        }
        if kind != MessageType::Question && !options.is_empty() {
            return Err(invalid("options goes with type question"));
        }
        if kind != MessageType::Card && card.is_some() {
            return Err(invalid("card goes with type card"));
        }
        let output = match kind {
            MessageType::Text => {
                if text.is_none() {
                    return Err(invalid("text must not be empty"));
                }
                sent
            }
            MessageType::Attachment => {
                if files.is_empty() {
                    return Err(invalid("give at least one file"));
                }
                let mut details = Vec::with_capacity(files.len());
                let mut wrong = Vec::new();
                for file in &files {
                    match sent_file(ctx.env.as_ref(), &file.path).await {
                        Ok(file) => details.push(file),
                        Err(why) => wrong.push(why),
                    }
                }
                if !wrong.is_empty() {
                    return Err(ToolError::Failed(wrong.join("; ")));
                }
                let mut said = json!({ "files": details });
                if let Some(text) = text {
                    said["text"] = json!(text);
                }
                sent.with_details(said)
            }
            MessageType::Question => {
                if text.is_none() {
                    return Err(invalid("text must not be empty"));
                }
                let mut seen = HashSet::new();
                for option in &options {
                    let label = option.label.trim();
                    if label.is_empty() {
                        return Err(invalid("an option's label must not be empty"));
                    }
                    if !seen.insert(label) {
                        return Err(invalid(format!("two options have the same label: {label}")));
                    }
                }
                return Ok(sent.ending_task());
            }
            MessageType::Card => {
                let id = card.ok_or_else(|| invalid("card is required with type card: the id the tool that prepared it gave"))?;
                let cards = ctx
                    .session_state
                    .get::<Cards>()
                    .ok_or_else(|| invalid(format!("no card {id} was prepared in this task: prepare one with the tool that makes it")))?;
                let card = cards.show(id).map_err(invalid)?;
                let mut said = json!({ "card": card });
                if let Some(text) = text {
                    said["text"] = json!(text);
                }
                return Ok(ToolOutput::text(CARD_SHOWN).with_details(said).ending_task());
            }
        };
        Ok(if args.end_turn { output.ending_task() } else { output })
    }
}
