//! Bots (docs/kernel/bot-design.md §4): a bot is a name, an avatar and one conversation, a
//! session the server keeps for it. The `bots/*` methods manage bots; the conversation uses
//! the session methods once `bots/open` has attached it.

use her_llm::ThinkingLevel;
use schemars::JsonSchema;
use serde::Deserialize;
use serde::Serialize;

use crate::api::EventFilter;
use crate::api::TranscriptItem;

/// The characters an avatar can show, told apart by their eyes and drawn by the client. Each
/// alias is what the bots made before these eyes have in their `bot.json`.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum AvatarShape {
    #[serde(alias = "box")]
    Tall,
    #[serde(alias = "round")]
    Dot,
    #[serde(alias = "cloud")]
    Smile,
    #[serde(alias = "cat")]
    Wink,
    #[serde(alias = "ghost")]
    Lid,
    #[serde(alias = "dome")]
    Bar,
    #[serde(alias = "gem")]
    Side,
    #[serde(alias = "egg")]
    Odd,
}

/// The colours behind an avatar's character or initial, the same in both themes.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum AvatarHue {
    Slate,
    Sage,
    Clay,
    Ochre,
    Plum,
    Teal,
    Rose,
    Stone,
}

/// A bot's avatar.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Avatar {
    /// The Hermesh mark: the default bot's alone.
    Mark,
    /// A character on a colour.
    Char { shape: AvatarShape, hue: AvatarHue },
    /// The first character of the bot's name on a colour.
    Mono { hue: AvatarHue },
    /// An image uploaded for the bot, which `bots/avatar` gives; `rev` changes with each one.
    Image { rev: u32 },
}

/// An avatar given to `bots/create` and `bots/update`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum AvatarInput {
    Mark,
    Char { shape: AvatarShape, hue: AvatarHue },
    Mono { hue: AvatarHue },
    /// A square PNG, base64, which the client cropped and scaled (256 px).
    Image { png: String },
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum Speaker {
    User,
    Bot,
}

/// The last message of a bot's conversation, for its row in the list.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct LastMessage {
    pub from: Speaker,
    /// The start of its text as written (at most 1000 characters): the client takes the line
    /// to show from it, as it does for the conversation.
    pub text: String,
    /// The images it carried.
    #[serde(default, skip_serializing_if = "is_zero")]
    pub images: u32,
    /// A bot's message of files: the name of the first that is not an image.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub file: Option<String>,
    /// A bot's question: the user is to decide.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub question: bool,
    /// RFC 3339.
    pub ts: String,
}

fn is_zero(n: &u32) -> bool {
    *n == 0
}

fn is_zero64(n: &u64) -> bool {
    *n == 0
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotSummary {
    pub id: String,
    pub name: String,
    pub avatar: Avatar,
    /// The bot every user has: it cannot be deleted.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub default: bool,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub pinned: bool,
    /// Its conversation, once there is one.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub session_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub last: Option<LastMessage>,
    /// Messages and approval requests since the user last looked (`bots/read`).
    #[serde(default, skip_serializing_if = "is_zero")]
    pub unread: u32,
    /// It is answering: its conversation runs a task.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub typing: bool,
    /// Sub-agents at work.
    #[serde(default, skip_serializing_if = "is_zero")]
    pub agents_working: u32,
    /// The oldest approval it waits for: what the call does.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub waiting: Option<String>,
    /// Its last task failed, so no message may be coming. Only while its conversation is loaded.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub failed: bool,
    /// The owner asked not to be alerted about this bot, on any device.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub muted: bool,
    /// RFC 3339.
    pub created_at: String,
    /// Its routines, oldest first (docs/kernel/routine-design.md §8).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub routines: Vec<RoutineSummary>,
    /// Why routines cannot be used now: their database cannot be opened. `routines` is then empty.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub routines_error: Option<String>,
    /// The files it sent since the owner last looked at the list of them (`deliveries/seen`).
    #[serde(default, skip_serializing_if = "is_zero")]
    pub new_deliveries: u32,
    /// The latest send in that list, as `deliveries/list` numbers them: the list changed when
    /// this moves.
    #[serde(default, skip_serializing_if = "is_zero64")]
    pub deliveries_latest: u64,
}

/// Result of `bots/subscribe`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotsResult {
    pub bots: Vec<BotSummary>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotCreateParams {
    /// 1 to 20 characters once trimmed.
    pub name: String,
    pub avatar: AvatarInput,
    /// The model its conversation starts with; the configured one without it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    /// How hard its conversation starts out thinking; the configured level without it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub thinking: Option<ThinkingLevel>,
}

/// Result of `bots/create` and `bots/update`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotResult {
    pub bot: BotSummary,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotUpdateParams {
    pub bot_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub avatar: Option<AvatarInput>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub pinned: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub muted: Option<bool>,
}

/// Parameters of `bots/avatar`, `bots/read`, `bots/unread`, `bots/clear`, `bots/delete`,
/// `bots/memory` and `bots/memory_clear`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotIdParams {
    pub bot_id: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotAvatarResult {
    /// `data:image/png;base64,…`
    pub data: String,
}

/// What a bot keeps in its memory (docs/her-memory.md): its `MEMORY.md` as written, to show as
/// Markdown whatever its form, and the other files of the folder.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotMemoryResult {
    /// `MEMORY.md`, empty when nothing is kept.
    pub text: String,
    /// Where `MEMORY.md` is on the Her Server, for `files/read`.
    pub path: String,
    /// The other files of the memory folder, by name.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub pages: Vec<BotMemoryPage>,
}

/// What a file a bot sent is, by its name; a picture by its contents (docs/her-deliverables.md §4.2).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum DeliveryKind {
    Doc,
    Sheet,
    Slide,
    Image,
    Video,
    Audio,
    Code,
    Other,
}

impl DeliveryKind {
    /// Pictures, video and audio: what the apps list apart from documents.
    pub fn is_media(self) -> bool {
        matches!(self, Self::Image | Self::Video | Self::Audio)
    }

    /// By the name's extension, without regard to case; `image`: its contents are a picture's.
    pub fn of(name: &str, image: bool) -> Self {
        if image {
            return Self::Image;
        }
        let ext = name.rsplit_once('.').map(|(_, ext)| ext.to_ascii_lowercase()).unwrap_or_default();
        match ext.as_str() {
            "doc" | "docx" | "pdf" | "md" | "markdown" | "txt" | "rtf" | "pages" | "odt" | "html" | "htm" => Self::Doc,
            "xlsx" | "xls" | "csv" | "tsv" | "numbers" | "ods" => Self::Sheet,
            "pptx" | "ppt" | "key" | "odp" => Self::Slide,
            "png" | "jpg" | "jpeg" | "gif" | "webp" | "heic" | "svg" | "bmp" | "tiff" => Self::Image,
            "mp4" | "mov" | "m4v" | "webm" | "avi" | "mkv" => Self::Video,
            "mp3" | "m4a" | "wav" | "aac" | "flac" | "ogg" | "opus" => Self::Audio,
            "rs" | "go" | "py" | "js" | "mjs" | "ts" | "tsx" | "jsx" | "java" | "kt" | "swift" | "c" | "h" | "cc" | "cpp" | "hpp" | "cs" | "rb" | "php"
            | "sh" | "bash" | "zsh" | "sql" | "json" | "yaml" | "yml" | "toml" | "xml" | "css" | "scss" | "lua" | "r" | "diff" | "patch" | "ipynb" => Self::Code,
            _ => Self::Other,
        }
    }

    pub fn as_str(self) -> &'static str {
        match self {
            Self::Doc => "doc",
            Self::Sheet => "sheet",
            Self::Slide => "slide",
            Self::Image => "image",
            Self::Video => "video",
            Self::Audio => "audio",
            Self::Code => "code",
            Self::Other => "other",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeliveriesListParams {
    pub bot_id: String,
    /// Files whose latest send is before this one: the `next` of the page before. The newest without it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub before: Option<u64>,
    /// How many files: 50 without it, at most 200.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub limit: Option<u32>,
    /// Only files with this in their name, in the bot's words or in what they came from, in
    /// any version, without regard to case.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub query: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub kind: Option<DeliveryKind>,
    /// true: only pictures, video and audio (DeliveryKind::is_media); false: only the rest, the
    /// documents. Both without it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub media: Option<bool>,
    /// Only files whose latest send came from this (`deliveries/sources`).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source: Option<DeliverySourceFilter>,
}

/// What a list of files keeps by where they came from.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum DeliverySourceFilter {
    /// What the owner asked for in the conversation: no source.
    Conversation,
    /// A routine's, by its id, whatever it was called.
    Routine { id: String },
    /// Coding sessions' of this name: a session goes when its work is done, its name stays.
    Session { name: String },
}

/// Result of `deliveries/list`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeliveriesListResult {
    /// Newest first, by their latest send.
    pub files: Vec<DeliveredFile>,
    /// The last send the owner saw (`deliveries/seen`): a later one is new.
    pub seen: u64,
    /// The `before` of the next page, when there is one.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub next: Option<u64>,
}

/// A file a bot sent: the sends from one path are one file.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeliveredFile {
    /// Where it was on the Her Server when sent, absolute.
    pub path: String,
    pub name: String,
    pub kind: DeliveryKind,
    /// Newest first, each a different content: the same content sent again is its latest send.
    /// One entry when the latest send kept no copy, as nothing earlier can be shown.
    pub versions: Vec<Delivery>,
}

/// One send of a file.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct Delivery {
    /// Its number among the bot's sends, growing.
    pub seq: u64,
    /// The conversation it was sent in, and the `send_to_user` call that sent it: its message.
    pub session_id: String,
    pub call_id: String,
    pub size: u64,
    /// A picture's size in pixels, when known.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub width: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub height: Option<u32>,
    /// The copy kept of it as it was when sent, absolute, and its content's SHA-256: what to
    /// read. None when no copy was kept; it is then read from `path`, as it is now.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sha256: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub snapshot: Option<String>,
    /// The bot's words with it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub text: Option<String>,
    /// RFC 3339.
    pub sent_at: String,
    /// What it came from; none for what the owner asked for in the conversation.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source: Option<DeliverySource>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum DeliverySourceKind {
    /// A routine's report the bot was passing on.
    Routine,
    /// News of a coding session the bot was telling the owner of.
    Session,
}

/// The routine or the session a file came from, and its name as the bot heard it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeliverySource {
    pub kind: DeliverySourceKind,
    pub id: String,
    pub name: String,
    /// A routine's run it came from (`bots/routine_run`), when known.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub run: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeliveriesSourcesParams {
    pub bot_id: String,
}

/// Result of `deliveries/sources`: where a bot's files came from, the latest first.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeliveriesSourcesResult {
    pub sources: Vec<DeliverySourceCount>,
    /// The bot's routines that sent no file: not listed.
    pub silent: u32,
}

/// A source of a bot's files: those whose latest send came from it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeliverySourceCount {
    /// What `deliveries/list` takes to show them.
    pub filter: DeliverySourceFilter,
    /// What it is called now: a routine's current name, a session's, or none for the conversation.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    /// How many files, and how many of them are pictures, video or audio.
    pub files: u32,
    pub media: u32,
    /// Its latest send (`Delivery::seq`).
    pub latest: u64,
    /// A routine that was deleted: the files it sent stay.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub gone: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeliveriesSeenParams {
    pub bot_id: String,
    /// The highest `seq` the list showed. The mark never goes back.
    pub seq: u64,
}

/// A file of a bot's memory besides `MEMORY.md`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotMemoryPage {
    pub name: String,
    /// For `files/read`.
    pub path: String,
    /// RFC 3339.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub modified_at: Option<String>,
}

/// Parameters of `bots/open`, which answers as `session/new` does.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct BotOpenParams {
    pub bot_id: String,
    #[serde(default)]
    pub events: EventFilter,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub history_limit: Option<u32>,
}

/// Server notification to `bots/subscribe`rs: the bots that changed, and those deleted.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotsChangedParams {
    pub bots: Vec<BotSummary>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub deleted: Vec<String>,
}

// ---- Routines (docs/kernel/routine-design.md §8) ----

/// Where a routine stands.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum RoutineState {
    Active,
    Paused,
    /// A one-off that has run.
    Finished,
}

/// How a run of a routine ended.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum RoutineOutcome {
    /// It had something to say, which went to the bot.
    Reported,
    /// It found nothing to report.
    Quiet,
    Failed,
    /// Someone stopped it, or the service closed or crashed while it ran.
    Interrupted,
    /// The run before it had not finished.
    Skipped,
}

/// A routine's latest run, for its row.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct RoutineRunBrief {
    /// RFC 3339: when it started, or when it was due if it was skipped.
    pub started: String,
    /// `None` while it runs.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub outcome: Option<RoutineOutcome>,
    /// RFC 3339: when the bot next messaged the user, after a report.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub delivered: Option<String>,
}

/// A routine as a list shows it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct RoutineSummary {
    pub id: String,
    pub name: String,
    /// The schedule in the user's words, as the bot wrote it.
    pub when: String,
    pub state: RoutineState,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub running: bool,
    /// RFC 3339. None when it is paused or finished.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub next_run: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub last_run: Option<RoutineRunBrief>,
}

/// All about a routine.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct RoutineDetail {
    pub summary: RoutineSummary,
    /// What each run is told to do.
    pub prompt: String,
    /// The schedule as written.
    pub schedule: String,
    /// The next runs, RFC 3339. Empty when it is paused or finished.
    pub upcoming: Vec<String>,
    /// RFC 3339.
    pub created_at: String,
}

/// One run of a routine.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct RoutineRunInfo {
    pub id: String,
    /// RFC 3339: when it was due.
    pub scheduled: String,
    /// RFC 3339. None when it was skipped.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub started: Option<String>,
    /// RFC 3339. None while it runs.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ended: Option<String>,
    /// `None` while it runs.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub outcome: Option<RoutineOutcome>,
    /// What a quiet run says it checked; why a run was skipped.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
    /// What it reported, or the error it failed with.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub report: Option<String>,
    /// RFC 3339: when the bot next messaged the user, after a report.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub delivered: Option<String>,
    /// It started well after it was due: the computer was off or asleep.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub late: bool,
    /// Its steps are still kept: `bots/routine_run` returns them.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub has_log: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub usage: Option<her_llm::UsageTotals>,
}

/// Parameters of `bots/routine`, `bots/routine_run_now`, `bots/routine_stop` and `bots/routine_delete`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotRoutineParams {
    pub bot_id: String,
    pub routine_id: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotRoutineResult {
    pub routine: RoutineDetail,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotRoutineRunsParams {
    pub bot_id: String,
    pub routine_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub limit: Option<u32>,
    /// A run's id: only runs older than it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub before: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct BotRoutineRunsResult {
    /// Newest first.
    pub runs: Vec<RoutineRunInfo>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotRoutineRunParams {
    pub bot_id: String,
    pub routine_id: String,
    pub run_id: String,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct BotRoutineRunResult {
    pub run: RoutineRunInfo,
    /// The name of the routine it is a run of.
    pub routine_name: String,
    /// The routine has been deleted since. Its runs are kept, and still read here.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub routine_deleted: bool,
    /// Its steps. Empty once its log has been cleaned up, or when it never started.
    pub transcript: Vec<TranscriptItem>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotRoutineSetEnabledParams {
    pub bot_id: String,
    pub routine_id: String,
    pub enabled: bool,
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A `bot.json` that cannot be read is skipped, and its bot gone from the list, so a bot made
    /// before these eyes must keep loading; it is written back under the new name.
    #[test]
    fn a_character_made_before_the_eyes_still_loads() {
        for (old, shape, new) in [
            ("box", AvatarShape::Tall, "tall"),
            ("round", AvatarShape::Dot, "dot"),
            ("cloud", AvatarShape::Smile, "smile"),
            ("cat", AvatarShape::Wink, "wink"),
            ("ghost", AvatarShape::Lid, "lid"),
            ("dome", AvatarShape::Bar, "bar"),
            ("gem", AvatarShape::Side, "side"),
            ("egg", AvatarShape::Odd, "odd"),
        ] {
            let saved = serde_json::json!({"kind": "char", "shape": old, "hue": "teal"});
            let avatar: Avatar = serde_json::from_value(saved).unwrap();
            assert_eq!(avatar, Avatar::Char { shape, hue: AvatarHue::Teal });
            assert_eq!(serde_json::to_value(&avatar).unwrap(), serde_json::json!({"kind": "char", "shape": new, "hue": "teal"}));
        }
    }
}
