//! Bots (docs/kernel/bot-design.md §5): who each bot is, kept in `~/.her/bots/<id>/bot.json`,
//! its row as the list shows it, and the `bots/*` methods. A bot's conversation is a session;
//! its hub hands the events that change the row here instead of to the session list.

use base64::Engine as _;
use bytes::Bytes;
use her_core::BotIdentity;
use her_core::HerHome;
use her_core::SessionRef;
use her_core::SessionStore;
use her_core::StartOptions;
use her_core::Workspace;
use her_llm::ThinkingLevel;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::PermissionMode;
use her_protocol::api::SessionInfo;
use her_protocol::api::SessionState;
use her_protocol::api::method;
use her_protocol::bot::Avatar;
use her_protocol::bot::AvatarInput;
use her_protocol::bot::BotAvatarResult;
use her_protocol::bot::BotCreateParams;
use her_protocol::bot::BotResult;
use her_protocol::bot::BotSummary;
use her_protocol::bot::BotUpdateParams;
use her_protocol::bot::BotsChangedParams;
use her_protocol::bot::BotsResult;
use her_protocol::bot::LastMessage;
use her_protocol::bot::RoutineSummary;
use her_protocol::bot::Speaker;
use her_rpc::RpcError;
use parking_lot::Mutex;
use serde::Deserialize;
use serde::Serialize;
use std::collections::BTreeMap;
use std::path::Path;
use std::sync::Arc;

use crate::conn::ConnHandle;
use crate::errors;
use crate::hub::AttachRequest;
use crate::hub::HubCmd;
use crate::hub::HubHandle;
use crate::hub::SummaryBase;
use crate::list;
use crate::list::ListFeed;
use crate::mirror::SessionMirror;
use crate::server::ResumeOverrides;
use crate::server::Server;

/// The name every user's first bot has. One made under the earlier name, HerBot, keeps it.
const DEFAULT_NAME: &str = "Her";
/// Characters in a name, once trimmed.
const NAME_MAX: usize = 20;
/// Characters of a message kept for the list.
const TEXT_MAX: usize = 1000;
/// An uploaded avatar, after the client scaled it to 256 px.
const PNG_MAX: usize = 1 << 20;
const PNG_SIGNATURE: &[u8] = b"\x89PNG\r\n\x1a\n";

/// What `bot.json` holds.
#[derive(Clone, Debug, Serialize, Deserialize)]
struct BotFile {
    version: u32,
    id: String,
    name: String,
    avatar: Avatar,
    #[serde(default)]
    default: bool,
    #[serde(default)]
    pinned: bool,
    #[serde(default)]
    muted: bool,
    created_at: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    session_id: Option<String>,
    /// What its next conversation starts with: what `bots/create` was told, then what the last one
    /// had when it was cleared. While one runs, it keeps its own (`set_model`).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    model: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    thinking: Option<ThinkingLevel>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    permission_mode: Option<PermissionMode>,
    /// The list's row, kept for when the conversation is not loaded.
    #[serde(default)]
    summary: Kept,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
struct Kept {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    last: Option<LastMessage>,
    #[serde(default)]
    unread: u32,
}

struct Bot {
    file: BotFile,
    // From its conversation's events while it is loaded.
    typing: bool,
    agents_working: u32,
    waiting: Option<String>,
    failed: bool,
    /// Its routines as its row shows them: the routines' side says when they change.
    routines: Vec<RoutineSummary>,
}

impl Bot {
    fn new(file: BotFile) -> Self {
        Self {
            file,
            typing: false,
            agents_working: 0,
            waiting: None,
            failed: false,
            routines: Vec::new(),
        }
    }

    fn summary(&self, routines_error: Option<&str>) -> BotSummary {
        let f = &self.file;
        BotSummary {
            id: f.id.clone(),
            name: f.name.clone(),
            avatar: f.avatar.clone(),
            default: f.default,
            pinned: f.pinned,
            session_id: f.session_id.clone(),
            last: f.summary.last.clone(),
            unread: f.summary.unread,
            typing: self.typing,
            agents_working: self.agents_working,
            waiting: self.waiting.clone(),
            failed: self.failed,
            muted: f.muted,
            created_at: f.created_at.clone(),
            routines: self.routines.clone(),
            routines_error: routines_error.map(str::to_string),
        }
    }
}

/// Every bot, and who follows the list.
pub(crate) struct Registry {
    home: HerHome,
    /// `None` until read from disk, on first use.
    bots: Mutex<Option<BTreeMap<String, Bot>>>,
    feed: ListFeed,
    /// One `bot.json` write at a time, each with the bot as it is then: the last write is the
    /// latest state.
    writing: Mutex<()>,
    /// One `bots/open` at a time, so a bot never gets two conversations.
    opening: tokio::sync::Mutex<()>,
    /// Why routines cannot be used here, which every row says.
    routines_error: Mutex<Option<String>>,
}

impl Registry {
    pub(crate) fn new(home: HerHome) -> Self {
        Self {
            home,
            bots: Mutex::new(None),
            feed: ListFeed::default(),
            writing: Mutex::new(()),
            opening: tokio::sync::Mutex::new(()),
            routines_error: Mutex::new(None),
        }
    }

    /// Runs `f` on every bot, read from disk the first time. Blocking I/O then.
    fn with<R>(&self, f: impl FnOnce(&mut BTreeMap<String, Bot>) -> R) -> R {
        let mut bots = self.bots.lock();
        let bots = bots.get_or_insert_with(|| read_all(&self.home));
        f(bots)
    }

    fn with_bot<R>(&self, id: &str, f: impl FnOnce(&mut Bot) -> R) -> Result<R, RpcError> {
        self.with(|bots| bots.get_mut(id).map(f)).ok_or_else(|| errors::bot_not_found(id))
    }

    /// Writes the bot's file as it is now. Blocking I/O.
    fn save(&self, id: &str) -> Result<(), RpcError> {
        let _writing = self.writing.lock();
        let Some(file) = self.with(|bots| bots.get(id).map(|b| b.file.clone())) else {
            return Ok(());
        };
        let dir = self.home.bot_dir(id);
        private_dir(&dir).map_err(|e| RpcError::internal(format!("cannot create {}: {e}", dir.display())))?;
        let text = serde_json::to_vec_pretty(&file).map_err(|e| RpcError::internal(e.to_string()))?;
        her_core::config_edit::replace_file(&dir.join("bot.json"), &text)
            .map_err(|e| RpcError::internal(format!("cannot write the bot: {e}")))
    }

    /// [`Self::save`] off the caller's thread, for the hub, which never blocks.
    fn save_later(self: &Arc<Self>, id: &str) {
        let (registry, id) = (self.clone(), id.to_string());
        tokio::task::spawn_blocking(move || {
            if let Err(err) = registry.save(&id) {
                tracing::warn!(bot = %id, "cannot save the bot: {}", err.message);
            }
        });
    }

    /// Every bot's row, or those that `keep` says.
    fn rows(&self, keep: impl Fn(&str) -> bool) -> Vec<BotSummary> {
        let error = self.routines_error.lock().clone();
        self.with(|bots| bots.values().filter(|bot| keep(&bot.file.id)).map(|bot| bot.summary(error.as_deref())).collect())
    }

    fn row(&self, id: &str) -> Result<BotSummary, RpcError> {
        self.rows(|bot| bot == id).pop().ok_or_else(|| errors::bot_not_found(id))
    }

    fn announce(&self, ids: &[&str], deleted: Vec<String>) {
        let bots = self.rows(|bot| ids.contains(&bot));
        self.feed.send(Bytes::from(her_rpc::notification(
            method::BOTS_CHANGED,
            &BotsChangedParams { bots, deleted },
        )));
    }

    /// The bot's name now, for a conversation being loaded.
    pub(crate) fn name(&self, id: &str) -> Option<String> {
        self.with(|bots| bots.get(id).map(|b| b.file.name.clone()))
    }

    /// Reads the list now, if it was not read yet. Blocking I/O.
    pub(crate) fn load(&self) {
        self.with(|_| ());
    }

    pub(crate) fn exists(&self, id: &str) -> bool {
        self.with(|bots| bots.contains_key(id))
    }

    /// The bot's routines changed; its row shows them.
    pub(crate) fn set_routines(&self, id: &str, routines: Vec<RoutineSummary>) {
        let changed = self.with(|bots| {
            bots.get_mut(id).is_some_and(|bot| {
                let changed = bot.routines != routines;
                bot.routines = routines;
                changed
            })
        });
        if changed {
            self.announce(&[id], Vec::new());
        }
    }

    /// Why routines cannot be used here, which every row says from now on.
    pub(crate) fn set_routines_error(&self, why: Option<String>) {
        *self.routines_error.lock() = why;
        let ids: Vec<String> = self.with(|bots| bots.keys().cloned().collect());
        self.announce(&ids.iter().map(String::as_str).collect::<Vec<_>>(), Vec::new());
    }

    #[cfg(test)]
    pub(crate) fn routines_error(&self) -> Option<String> {
        self.routines_error.lock().clone()
    }

    #[cfg(test)]
    pub(crate) fn add_for_test(&self, id: &str) {
        let mut file = new_file("测试".into(), Avatar::Mark, false);
        file.id = id.to_string();
        self.with(|bots| bots.insert(id.to_string(), Bot::new(file)));
    }

    /// An event of a bot's loaded conversation: the row may change (bot-design §5.2).
    pub(crate) fn on_event(self: &Arc<Self>, id: &str, event: &Event, mirror: &SessionMirror) {
        let (changed, kept) = {
            let mut guard = self.bots.lock();
            let Some(bot) = guard.as_mut().and_then(|bots| bots.get_mut(id)) else {
                return;
            };
            let before = (bot.typing, bot.agents_working, bot.waiting.clone(), bot.failed);
            let mut kept = false;
            match &event.msg {
                // A message to the user is a bubble from the call's start, as the client shows it.
                EventMsg::ToolStarted { name, args, .. } if name == her_core::bot::SEND_TO_USER => {
                    if !her_core::bot::sends_files(args)
                        && let Some(text) = her_core::bot::words_of(args)
                    {
                        bot.file.summary.last = Some(last(Speaker::Bot, text, 0, None, event.ts));
                        bot.file.summary.unread += 1;
                        kept = true;
                    }
                }
                // Files count once they were found to be there (bot-design §3.12).
                EventMsg::ToolCompleted { result, .. } if result.tool_name == her_core::bot::SEND_TO_USER && !result.is_error => {
                    if let Some((text, images, file)) = result.details.as_ref().and_then(|d| her_core::bot::attachment_summary(d.as_str())) {
                        bot.file.summary.last = Some(last(Speaker::Bot, &text, images, file, event.ts));
                        bot.file.summary.unread += 1;
                        kept = true;
                    }
                }
                EventMsg::MessageCompleted {
                    message: her_llm::Message::User(user),
                    mail: None,
                    ..
                } => {
                    let images = user.content.iter().filter(|c| matches!(c, her_llm::UserContent::Image { .. })).count();
                    bot.file.summary.last = Some(last(Speaker::User, &user.text_content(), images as u32, None, event.ts));
                    kept = true;
                }
                // What a sub-agent told the owner is a message like the bot's own
                // (agent-messaging-design §17).
                EventMsg::MessageCompleted {
                    message: her_llm::Message::User(user),
                    mail: Some(mail),
                    ..
                } if mail.kind == her_protocol::MailKind::ToUser => {
                    bot.file.summary.last = Some(last(Speaker::Bot, her_core::bot::told_words(&user.text_content()), 0, None, event.ts));
                    bot.file.summary.unread += 1;
                    kept = true;
                }
                EventMsg::ApprovalRequested { .. } => {
                    bot.file.summary.unread += 1;
                    kept = true;
                }
                // What a sub-agent asks counts like the bot's own.
                EventMsg::AgentEvent { event, .. } if matches!(event.msg, EventMsg::ApprovalRequested { .. }) => {
                    bot.file.summary.unread += 1;
                    kept = true;
                }
                _ => {}
            }
            bot.typing = mirror.busy();
            bot.agents_working = mirror.agents_working();
            bot.waiting = mirror.first_approval().map(str::to_string);
            bot.failed = mirror.state() == SessionState::Failed;
            (kept || before != (bot.typing, bot.agents_working, bot.waiting.clone(), bot.failed), kept)
        };
        if kept {
            self.save_later(id);
        }
        if changed {
            self.announce(&[id], Vec::new());
        }
    }

    /// The bot's conversation was unloaded: nothing runs.
    pub(crate) fn on_unloaded(&self, id: &str) {
        let changed = self.with(|bots| {
            bots.get_mut(id).is_some_and(|bot| {
                let was = bot.typing || bot.agents_working > 0 || bot.waiting.is_some() || bot.failed;
                (bot.typing, bot.agents_working, bot.waiting, bot.failed) = (false, 0, None, false);
                was
            })
        });
        if changed {
            self.announce(&[id], Vec::new());
        }
    }
}

fn last(from: Speaker, text: &str, images: u32, file: Option<String>, ts: her_llm::Timestamp) -> LastMessage {
    LastMessage {
        from,
        text: text.chars().take(TEXT_MAX).collect(),
        images,
        file,
        ts: list::rfc3339(ts),
    }
}

fn private_dir(dir: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(dir)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700))?;
    }
    Ok(())
}

/// Every `bots/<id>/bot.json` that reads; the others are skipped with a warning.
fn read_all(home: &HerHome) -> BTreeMap<String, Bot> {
    let mut bots = BTreeMap::new();
    let Ok(entries) = std::fs::read_dir(home.bots_dir()) else {
        return bots;
    };
    for entry in entries.flatten() {
        let path = entry.path().join("bot.json");
        if !path.is_file() {
            continue;
        }
        match std::fs::read(&path).map_err(|e| e.to_string()).and_then(|b| serde_json::from_slice::<BotFile>(&b).map_err(|e| e.to_string())) {
            Ok(file) if entry.file_name().to_str() == Some(file.id.as_str()) => {
                bots.insert(file.id.clone(), Bot::new(file));
            }
            Ok(_) => tracing::warn!(path = %path.display(), "skipping a bot whose id is not its folder's name"),
            Err(err) => tracing::warn!(path = %path.display(), "skipping a bot that cannot be read: {err}"),
        }
    }
    bots
}

/// A name as kept: trimmed, 1 to 20 characters.
fn clean_name(name: &str) -> Result<String, RpcError> {
    let name = name.trim();
    match name.chars().count() {
        0 => Err(errors::bot_invalid("the name is empty")),
        n if n > NAME_MAX => Err(errors::bot_invalid(format!("the name is longer than {NAME_MAX} characters"))),
        _ => Ok(name.to_string()),
    }
}

impl Server {
    fn bots(&self) -> &Arc<Registry> {
        &self.0.bots
    }

    /// `bots/subscribe`: every bot now, the default one made first when there is none, and
    /// `bots/changed` from then on. Blocking I/O.
    pub fn bots_subscribe(&self, conn: &ConnHandle, respond: impl FnOnce(Result<BotsResult, RpcError>) -> Vec<u8>) {
        let registry = self.bots();
        registry.feed.watch(conn);
        let result = self.make_default_bot().map(|()| BotsResult { bots: registry.rows(|_| true) });
        let failed = result.is_err();
        registry.feed.start(conn, respond(result));
        if failed {
            registry.feed.cancel(conn.id);
        }
    }

    pub fn bots_unsubscribe(&self, conn: &ConnHandle) {
        self.bots().feed.cancel(conn.id);
    }

    /// The default bot, when there is no bot at all (bot-design §2.1).
    fn make_default_bot(&self) -> Result<(), RpcError> {
        let registry = self.bots();
        let id = registry.with(|bots| {
            if !bots.is_empty() {
                return None;
            }
            let file = new_file(DEFAULT_NAME.into(), Avatar::Mark, true);
            let id = file.id.clone();
            bots.insert(id.clone(), Bot::new(file));
            Some(id)
        });
        match id {
            Some(id) => self.made(&id),
            None => Ok(()),
        }
    }

    /// Writes a new bot's folder and file. Blocking I/O.
    fn made(&self, id: &str) -> Result<(), RpcError> {
        let files = self.0.home.bot_files_dir(id);
        private_dir(&self.0.home.bot_dir(id))
            .and_then(|()| std::fs::create_dir_all(&files))
            .map_err(|e| RpcError::internal(format!("cannot create {}: {e}", files.display())))?;
        self.bots().save(id)
    }

    /// `bots/create`. Blocking I/O.
    pub fn bots_create(&self, params: BotCreateParams) -> Result<BotResult, RpcError> {
        let name = clean_name(&params.name)?;
        let file = new_file(name, Avatar::Mark, false);
        let id = file.id.clone();
        let avatar = self.avatar_of(&id, &params.avatar, false, None)?;
        let registry = self.bots();
        registry.with(|bots| bots.insert(id.clone(), Bot::new(BotFile { avatar, model: params.model, thinking: params.thinking, ..file })));
        if let Err(err) = self.made(&id) {
            registry.with(|bots| bots.remove(&id));
            return Err(err);
        }
        registry.announce(&[&id], Vec::new());
        Ok(BotResult { bot: registry.row(&id)? })
    }

    /// The avatar to keep for `input`; an image is written to the bot's folder. Blocking I/O.
    fn avatar_of(&self, id: &str, input: &AvatarInput, default: bool, before: Option<&Avatar>) -> Result<Avatar, RpcError> {
        Ok(match input {
            AvatarInput::Mark if default => Avatar::Mark,
            AvatarInput::Mark => return Err(errors::bot_default("only the default bot has the mark")),
            AvatarInput::Char { shape, hue } => Avatar::Char { shape: *shape, hue: *hue },
            AvatarInput::Mono { hue } => Avatar::Mono { hue: *hue },
            AvatarInput::Image { png } => {
                let bytes = base64::engine::general_purpose::STANDARD
                    .decode(png.trim())
                    .map_err(|_| errors::bot_invalid("the image is not base64"))?;
                if !bytes.starts_with(PNG_SIGNATURE) {
                    return Err(errors::bot_invalid("the image is not a PNG"));
                }
                if bytes.len() > PNG_MAX {
                    return Err(errors::bot_invalid("the image is larger than 1 MB"));
                }
                let dir = self.0.home.bot_dir(id);
                private_dir(&dir).map_err(|e| RpcError::internal(e.to_string()))?;
                her_core::config_edit::replace_file(&dir.join("avatar.png"), &bytes)
                    .map_err(|e| RpcError::internal(format!("cannot write the avatar: {e}")))?;
                let rev = match before {
                    Some(Avatar::Image { rev }) => rev + 1,
                    _ => 1,
                };
                Avatar::Image { rev }
            }
        })
    }

    /// `bots/update`: a new name reaches a loaded conversation's model at its next turn.
    /// Blocking I/O.
    pub fn bots_update(&self, params: BotUpdateParams) -> Result<BotResult, RpcError> {
        let registry = self.bots();
        let name = params.name.as_deref().map(clean_name).transpose()?;
        let (default, before) = registry.with_bot(&params.bot_id, |b| (b.file.default, b.file.avatar.clone()))?;
        let avatar = match &params.avatar {
            Some(input) => Some(self.avatar_of(&params.bot_id, input, default, Some(&before))?),
            None => None,
        };
        let renamed = registry.with_bot(&params.bot_id, |bot| {
            if let Some(avatar) = avatar {
                bot.file.avatar = avatar;
            }
            if let Some(pinned) = params.pinned {
                bot.file.pinned = pinned;
            }
            if let Some(muted) = params.muted {
                bot.file.muted = muted;
            }
            match name {
                Some(name) if name != bot.file.name => {
                    bot.file.name = name.clone();
                    bot.file.session_id.clone().map(|session| (session, name))
                }
                _ => None,
            }
        })?;
        registry.save(&params.bot_id)?;
        if let Some((session, name)) = renamed
            && let Some(hub) = self.hub(&session)
        {
            let _ = hub.tx.try_send(Box::new(HubCmd::BotName { name }));
        }
        registry.announce(&[&params.bot_id], Vec::new());
        Ok(BotResult { bot: registry.row(&params.bot_id)? })
    }

    /// `bots/avatar`. Blocking I/O.
    pub fn bots_avatar(&self, id: &str) -> Result<BotAvatarResult, RpcError> {
        self.bots().with_bot(id, |_| ())?;
        let bytes = std::fs::read(self.0.home.bot_dir(id).join("avatar.png"))
            .map_err(|_| errors::bot_invalid("the bot has no uploaded avatar"))?;
        Ok(BotAvatarResult {
            data: format!("data:image/png;base64,{}", base64::engine::general_purpose::STANDARD.encode(bytes)),
        })
    }

    /// `bots/read` (`unread` 0) and `bots/unread` (`unread` 1). Blocking I/O.
    pub fn bots_set_unread(&self, id: &str, unread: u32) -> Result<(), RpcError> {
        let registry = self.bots();
        let changed = registry.with_bot(id, |bot| std::mem::replace(&mut bot.file.summary.unread, unread) != unread)?;
        if changed {
            registry.save(id)?;
            registry.announce(&[id], Vec::new());
        }
        Ok(())
    }

    /// `bots/open`: attaches the bot's conversation, loading it, or starting it when there is
    /// none (bot-design §5.4).
    pub async fn bots_open(&self, id: &str, attach: AttachRequest) -> Result<(), RpcError> {
        self.open_bot(id, Some(attach)).await.map(|_| ())
    }

    /// The bot's conversation loaded for what the service itself does with it: nothing
    /// attaches, nothing is written, and it unloads again when nothing uses it (routine-design §5.5).
    pub(crate) async fn load_bot_session(&self, id: &str) -> Result<HubHandle, RpcError> {
        let session = self.open_bot(id, None).await?;
        self.hub(&session).ok_or_else(|| errors::session_closed(&session))
    }

    /// The bot's conversation if it is loaded.
    pub(crate) fn bot_hub(&self, id: &str) -> Option<HubHandle> {
        let session = self.bots().with_bot(id, |bot| bot.file.session_id.clone()).ok()??;
        self.hub(&session)
    }

    pub(crate) fn bot_exists(&self, id: &str) -> Result<(), RpcError> {
        if self.bots().exists(id) { Ok(()) } else { Err(errors::bot_not_found(id)) }
    }

    /// Loads or starts the bot's conversation; returns its session.
    async fn open_bot(&self, id: &str, attach: Option<AttachRequest>) -> Result<String, RpcError> {
        let registry = self.bots().clone();
        let _opening = registry.opening.lock().await;
        let (session, name, start) = {
            let (registry, id) = (registry.clone(), id.to_string());
            tokio::task::spawn_blocking(move || registry.with_bot(&id, |b| (b.file.session_id.clone(), b.file.name.clone(), (b.file.model.clone(), b.file.thinking, b.file.permission_mode))))
                .await
                .map_err(|e| RpcError::internal(e.to_string()))??
        };
        if let Some(session) = session {
            let on_disk = self.is_loaded(&session) || self.store().header(&SessionRef::Id(session.clone())).is_ok_and(|(h, _)| h.id == session);
            if on_disk {
                self.load_session(&session, ResumeOverrides::default(), attach).await?;
                return Ok(session);
            }
        }
        let session = self.start_bot_session(id, name, start, attach).await?;
        registry.with_bot(id, |bot| bot.file.session_id = Some(session.clone()))?;
        let saved = {
            let (registry, id) = (registry.clone(), id.to_string());
            tokio::task::spawn_blocking(move || registry.save(&id)).await.map_err(|e| RpcError::internal(e.to_string()))?
        };
        registry.announce(&[id], Vec::new());
        saved.map(|()| session)
    }

    /// Starts a bot's conversation in its folder.
    async fn start_bot_session(&self, id: &str, name: String, (model, thinking, permission_mode): (Option<String>, Option<ThinkingLevel>, Option<PermissionMode>), attach: Option<AttachRequest>) -> Result<String, RpcError> {
        self.make_room().await?;
        let folder = self.0.home.bot_files_dir(id);
        std::fs::create_dir_all(&folder).map_err(|e| RpcError::internal(format!("cannot create {}: {e}", folder.display())))?;
        let workspace = Workspace::open(&self.0.home, &folder, &self.0.overrides).map_err(errors::from_runtime)?;
        let model = self.session_model(model, workspace.config.model.clone()).await?;
        let store: Arc<dyn SessionStore> = self.store().clone();
        let mut deps = workspace.kernel_deps(self.0.llm.clone(), store, &[]).map_err(errors::from_runtime)?;
        deps.browser_pool = Some(self.0.browser_pool.clone());
        deps.docs = Some(self.0.docs.clone());
        deps.decks = Some(self.0.decks.clone());
        deps.words = Some(self.0.words.clone());
        deps.mcp_servers = self.session_mcp_servers(&workspace, &[]);
        deps.routines = Some(self.0.routines.clone());
        let options = StartOptions {
            model,
            thinking: thinking.unwrap_or(workspace.config.thinking),
            permission_mode: permission_mode.unwrap_or(workspace.config.permission_mode),
            cwd: workspace.cwd.clone(),
            home: self.0.home.clone(),
            project_root: None,
            trusted: workspace.trusted,
            config: Arc::new(workspace.config),
            interactive_approvals: true,
            bot: Some(BotIdentity { id: id.to_string(), name }),
        };
        let kernel = her_core::Kernel::start(options, deps).await.map_err(|e| errors::from_kernel(e, "new"))?;
        if let Some(text) = self.0.routines.listing_of(id) {
            kernel.set_routines(text);
        }
        let now = list::now();
        let base = SummaryBase {
            created_at: now.clone(),
            preview: None,
            updated_at: now,
            no_project: true,
        };
        Ok(self.register(kernel, Vec::new(), attach, base, Some(id.to_string())))
    }

    /// `bots/clear`: the conversation goes to the trash; the next `bots/open` starts another, with
    /// the settings this one has, which the bot's settings show as the bot's.
    pub async fn bots_clear(&self, id: &str) -> Result<(), RpcError> {
        let registry = self.bots().clone();
        let current = registry.with_bot(id, |bot| bot.file.session_id.clone())?;
        let settings = match &current {
            Some(session) => self.conversation_settings(session).await,
            None => None,
        };
        let session = registry.with_bot(id, |bot| {
            if let Some(info) = settings {
                bot.file.model = Some(info.model);
                bot.file.thinking = Some(info.thinking);
                bot.file.permission_mode = Some(info.permission_mode);
            }
            bot.file.summary = Kept::default();
            bot.file.session_id.take()
        })?;
        self.drop_conversation(session).await?;
        let saved = {
            let (registry, id) = (registry.clone(), id.to_string());
            tokio::task::spawn_blocking(move || registry.save(&id)).await.map_err(|e| RpcError::internal(e.to_string()))?
        };
        registry.announce(&[id], Vec::new());
        saved
    }

    /// `bots/delete`: the bot's folder goes to `trash/bots/`, its conversation to the trash.
    pub async fn bots_delete(&self, id: &str) -> Result<(), RpcError> {
        let registry = self.bots().clone();
        if registry.with_bot(id, |bot| bot.file.default)? {
            return Err(errors::bot_default("the default bot cannot be deleted"));
        }
        let session = registry.with_bot(id, |bot| bot.file.session_id.take())?;
        self.0.routines.bot_deleted(id).await;
        self.drop_conversation(session).await?;
        let (home, dir_id) = (self.0.home.clone(), id.to_string());
        tokio::task::spawn_blocking(move || {
            // Ids are never reused, so its name there is free.
            let trash = home.root().join("trash").join("bots");
            std::fs::create_dir_all(&trash)?;
            std::fs::rename(home.bot_dir(&dir_id), trash.join(&dir_id))
        })
        .await
        .map_err(|e| RpcError::internal(e.to_string()))?
        .map_err(|e| RpcError::internal(format!("cannot move the bot to the trash: {e}")))?;
        registry.with(|bots| bots.remove(id));
        registry.announce(&[], vec![id.to_string()]);
        Ok(())
    }

    /// A conversation's settings, loading it if need be. A log that will not load is no reason to
    /// keep it: clearing goes on, and the next conversation starts on the defaults.
    async fn conversation_settings(&self, session: &str) -> Option<SessionInfo> {
        self.load_session(session, ResumeOverrides::default(), None).await.ok()?;
        self.info(session).await.ok()
    }

    /// Closes a bot's conversation, waiting for its log, and moves the log to the trash.
    async fn drop_conversation(&self, session: Option<String>) -> Result<(), RpcError> {
        let Some(session) = session else {
            return Ok(());
        };
        if self.is_loaded(&session) {
            self.close_session(&session).await?;
        }
        let server = self.clone();
        tokio::task::spawn_blocking(move || match server.trash_log(&session) {
            Err(err) if err.data.as_ref().and_then(|d| d.get("kind")).and_then(|k| k.as_str()) == Some("session_not_found") => Ok(()),
            other => other,
        })
        .await
        .map_err(|e| RpcError::internal(e.to_string()))?
    }
}

fn new_file(name: String, avatar: Avatar, default: bool) -> BotFile {
    BotFile {
        version: 1,
        id: uuid::Uuid::now_v7().to_string(),
        name,
        avatar,
        default,
        pinned: false,
        muted: false,
        created_at: list::now(),
        session_id: None,
        model: None,
        thinking: None,
        permission_mode: None,
        summary: Kept::default(),
    }
}
