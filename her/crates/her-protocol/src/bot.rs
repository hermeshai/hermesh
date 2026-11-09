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

/// Parameters of `bots/avatar`, `bots/read`, `bots/unread`, `bots/clear` and `bots/delete`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotIdParams {
    pub bot_id: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct BotAvatarResult {
    /// `data:image/png;base64,…`
    pub data: String,
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
