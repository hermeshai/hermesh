//! Secure cards in a session (docs/her-secure-cards.md): those its tools prepared, those
//! `send_to_user` showed, and where each stands. The server answers them (`cards/answer`) through
//! [`Cards`]. Each change reaches the session's clients as `card_changed`; how a card ended
//! reaches the model as a `CardResolved` entry, through the inbox, as a report does.

use her_llm::Message;
use her_protocol::EventMsg;
use her_protocol::TaskTrigger;
use her_protocol::card::AskedBy;
use her_protocol::card::Card;
use her_protocol::card::CardKind;
use her_protocol::card::CardState;
use parking_lot::Mutex;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::mpsc;

use crate::emitter::Emitter;
use crate::history::History;
use crate::session::Internal;
use crate::store::CardResolvedEntry;
use crate::store::EntryKind;

/// How long a `connect` card waits for the owner.
pub(crate) const LIFE_HOURS: i64 = 24;

/// How long a `site_login` card waits for the owner (5 minutes).
pub(crate) const SITE_LOGIN_LIFE_SECS: i64 = 300;

/// Server-only target binding for a `site_login` card (never sent to the model, never holds values).
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum SiteLoginBinding {
    Fill(SiteLoginFillBinding),
    ChromeTakeover(ChromeTakeoverBinding),
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SiteLoginFillBinding {
    pub tab: String,
    pub frame: String,
    pub loader: String,
    pub origin: String,
    pub fields: Vec<SiteLoginFieldBinding>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ChromeTakeoverBinding {
    /// Session tab id (e.g. `y1`).
    pub tab: String,
    /// Extension target id (e.g. `T42`).
    pub target_id: String,
    /// Verified top-level origin (`https://...` or loopback fixture).
    pub origin: String,
    /// Exact URL at the moment the card was prepared.
    pub url: String,
    /// Bound extension host id in `BrowserPool` / `Server`.
    pub extension_host: u64,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SiteLoginFieldBinding {
    pub id: String,
    pub backend: i64,
    pub tag: String,
    pub input_type: String,
    pub secret: bool,
}

type EndHook = Arc<dyn Fn(&str, CardState) + Send + Sync>;

/// A session's cards.
pub struct Cards {
    kept: Mutex<Vec<Kept>>,
    /// What began the running task: whoever asked for the cards it prepares.
    asked_by: Mutex<AskedBy>,
    emitter: Arc<Emitter>,
    internal: mpsc::UnboundedSender<Box<Internal>>,
}

struct Kept {
    card: Card,
    /// `None` while it is prepared and not shown.
    state: Option<CardState>,
    binding: Option<SiteLoginBinding>,
    on_end: Option<EndHook>,
}

fn target_key(card: &Card) -> Option<String> {
    if let Some(body) = card.connect_body() {
        Some(body.name.clone())
    } else {
        card.site_login_body().map(|body| body.origin.clone())
    }
}

impl Cards {
    pub(crate) fn new(emitter: Arc<Emitter>, internal: mpsc::UnboundedSender<Box<Internal>>) -> Self {
        Self { kept: Mutex::new(Vec::new()), asked_by: Mutex::new(AskedBy::Owner), emitter, internal }
    }

    pub(crate) fn asked_by(&self) -> AskedBy {
        *self.asked_by.lock()
    }

    /// A task starts: the owner asked for what it does only when their message began it.
    pub(crate) fn task_started(&self, trigger: TaskTrigger) {
        *self.asked_by.lock() = if trigger == TaskTrigger::User { AskedBy::Owner } else { AskedBy::Her };
    }

    /// A task ended: what it prepared and never showed goes.
    pub(crate) fn task_ended(&self) {
        let dropped: Vec<(String, Option<EndHook>)> = {
            let mut kept = self.kept.lock();
            let mut dropped = Vec::new();
            kept.retain(|item| {
                if item.state.is_some() {
                    true
                } else {
                    dropped.push((item.card.id.clone(), item.on_end.clone()));
                    false
                }
            });
            dropped
        };
        for (id, hook) in dropped {
            if let Some(hook) = hook {
                hook(&id, CardState::Expired);
            }
        }
    }

    pub(crate) fn prepare(&self, card: Card) {
        self.prepare_with_binding(card, None, None);
    }

    pub(crate) fn prepare_with_binding(&self, card: Card, binding: Option<SiteLoginBinding>, on_end: Option<EndHook>) {
        self.kept.lock().push(Kept { card, state: None, binding, on_end });
    }

    /// `send_to_user` shows a card this session prepared: it is open from now on, and an open one
    /// for the same target is replaced. Errors are one sentence for the model.
    pub(crate) fn show(self: &Arc<Self>, id: &str) -> Result<Card, String> {
        let (card, older) = {
            let mut kept = self.kept.lock();
            let Some(shown) = kept.iter_mut().find(|kept| kept.card.id == id) else {
                return Err(format!("no card {id} was prepared in this task: prepare one with the tool that makes it"));
            };
            if shown.state.is_some() {
                return Err(format!("card {id} was shown already"));
            }
            if time_left(&shown.card).is_zero() {
                return Err(format!("card {id} has expired: prepare another"));
            }
            shown.state = Some(CardState::Open);
            let card = shown.card.clone();
            let key = target_key(&card);
            self.emitter.emit(EventMsg::CardChanged { card_id: card.id.clone(), state: CardState::Open, note: None });
            let older: Vec<String> = kept
                .iter()
                .filter(|other| other.state == Some(CardState::Open) && other.card.id != card.id && other.card.kind == card.kind && target_key(&other.card) == key)
                .map(|other| other.card.id.clone())
                .collect();
            (card, older)
        };
        for old in older {
            let _ = self.set(&old, &[CardState::Open], CardState::Replaced, Some("A newer card took its place.".into()));
        }
        self.expire_later(&card);
        Ok(card)
    }

    /// A card shown before the session was loaded, whose end the history does not have: open
    /// again. `site_login` cards are bound to a live browser document and are never reopened.
    pub(crate) fn take_back(self: &Arc<Self>, card: Card) {
        if card.kind != CardKind::Connect {
            return;
        }
        self.expire_later(&card);
        self.kept.lock().push(Kept { card, state: Some(CardState::Open), binding: None, on_end: None });
    }

    pub(crate) fn has(&self, id: &str) -> bool {
        self.kept.lock().iter().any(|kept| kept.card.id == id)
    }

    /// A shown card and where it stands.
    pub fn get(&self, id: &str) -> Option<(Card, CardState)> {
        self.kept.lock().iter().find(|kept| kept.card.id == id).and_then(|kept| Some((kept.card.clone(), kept.state?)))
    }

    /// The server-only `site_login` target binding for a shown card.
    pub fn binding(&self, id: &str) -> Option<SiteLoginBinding> {
        self.kept.lock().iter().find(|kept| kept.card.id == id && kept.state.is_some()).and_then(|kept| kept.binding.clone())
    }

    /// Active (`open` or `sign_in`) local Chrome takeover cards for real-time invalidation.
    pub fn active_chrome_takeovers(&self) -> Vec<(String, CardState, ChromeTakeoverBinding)> {
        self.kept
            .lock()
            .iter()
            .filter_map(|kept| {
                let state = kept.state?;
                if !matches!(state, CardState::Open | CardState::SignIn) {
                    return None;
                }
                match &kept.binding {
                    Some(SiteLoginBinding::ChromeTakeover(binding)) => {
                        Some((kept.card.id.clone(), state, binding.clone()))
                    }
                    _ => None,
                }
            })
            .collect()
    }

    /// Moves a shown card from one of `from` to `to`, with her's words, and returns the state it
    /// left. `Err` holds where it stands when that is none of `from`, or `None` for no such card.
    /// An end is told to the model, the note with it.
    pub fn set(&self, id: &str, from: &[CardState], to: CardState, note: Option<String>) -> Result<CardState, Option<CardState>> {
        let (left, hook) = {
            let mut kept = self.kept.lock();
            let Some(card) = kept.iter_mut().find(|kept| kept.card.id == id) else {
                return Err(None);
            };
            let left = card.state.ok_or(None)?;
            if !from.contains(&left) {
                return Err(Some(left));
            }
            card.state = Some(to);
            // Under the lock: the events go out in the order of the changes.
            self.emitter.emit(EventMsg::CardChanged { card_id: id.to_string(), state: to, note: note.clone() });
            let hook = if to.is_ended() {
                let entry = CardResolvedEntry {
                    card_id: id.to_string(),
                    kind: card.card.kind.as_str().to_string(),
                    outcome: to.as_str().to_string(),
                    note: note.unwrap_or_else(|| to.as_str().to_string()),
                };
                let _ = self.internal.send(Box::new(Internal::Card(Box::new(entry))));
                card.on_end.take()
            } else {
                None
            };
            (left, hook)
        };
        if let Some(hook) = hook {
            hook(id, to);
        }
        Ok(left)
    }

    /// Ends a card past its time, unless it ended already. True when it is expired now.
    pub fn expire_if_due(&self, id: &str) -> bool {
        let due = self.kept.lock().iter().any(|kept| kept.card.id == id && time_left(&kept.card).is_zero());
        let waiting = [CardState::Open, CardState::Working, CardState::SignIn, CardState::Retry];
        due && self.set(id, &waiting, CardState::Expired, Some("The card expired before it was answered.".into())).is_ok()
    }

    fn expire_later(self: &Arc<Self>, card: &Card) {
        let (cards, id, left) = (Arc::downgrade(self), card.id.clone(), time_left(card));
        tokio::spawn(async move {
            tokio::time::sleep(left).await;
            if let Some(cards) = cards.upgrade() {
                cards.expire_if_due(&id);
            }
        });
    }
}

/// How long the card has before it expires; zero once it has, or when its time cannot be read.
fn time_left(card: &Card) -> Duration {
    chrono::DateTime::parse_from_rfc3339(&card.expires_at)
        .ok()
        .and_then(|at| (at.with_timezone(&chrono::Utc) - chrono::Utc::now()).to_std().ok())
        .unwrap_or_default()
}

/// The card of that id that `send_to_user` showed on the current branch, if no end of it follows.
/// Only `connect` cards are recoverable across restarts; `site_login` cards bind to live browser documents.
pub(crate) fn unresolved(history: &History, id: &str) -> Option<Card> {
    let mut found = None;
    for entry in history.branch() {
        match &entry.kind {
            EntryKind::Message { message: Message::ToolResult(result), .. } if result.tool_name == crate::bot::SEND_TO_USER && !result.is_error => {
                if let Some((card, _)) = result.details.as_ref().and_then(|details| crate::bot::shown_card(details.as_str())).filter(|(card, _)| card.id == id && card.kind == CardKind::Connect) {
                    found = Some(card);
                }
            }
            EntryKind::CardResolved(resolved) if resolved.card_id == id => found = None,
            _ => {}
        }
    }
    found
}
