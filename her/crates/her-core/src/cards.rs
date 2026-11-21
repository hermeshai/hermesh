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

impl SiteLoginBinding {
    pub fn url(&self) -> &str {
        match self {
            SiteLoginBinding::Fill(b) => &b.url,
            SiteLoginBinding::ChromeTakeover(b) => &b.url,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SiteLoginFillBinding {
    pub own_browser: bool,
    pub target_id: String,
    pub tab: String,
    pub frame: String,
    pub loader: String,
    pub origin: String,
    pub url: String,
    pub browser_label: Option<String>,
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
type ShowCheck = Arc<dyn Fn() -> Result<(), String> + Send + Sync>;

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
    before_show: Option<ShowCheck>,
}

fn target_key(card: &Card) -> Option<String> {
    if let Some(body) = card.connect_body() {
        Some(body.name.clone())
    } else {
        card.site_login_body().map(|body| match &body.browser {
            Some(browser) => format!("{browser}:{}", body.origin),
            None => body.origin.clone(),
        })
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
        self.prepare_with_binding(card, None, None, None);
    }

    pub(crate) fn prepare_with_binding(
        &self,
        card: Card,
        binding: Option<SiteLoginBinding>,
        on_end: Option<EndHook>,
        before_show: Option<ShowCheck>,
    ) {
        self.kept.lock().push(Kept { card, state: None, binding, on_end, before_show });
    }

    /// `send_to_user` shows a card this session prepared: it is open from now on, and an open one
    /// for the same target is replaced. Errors are one sentence for the model.
    pub(crate) fn show(self: &Arc<Self>, id: &str) -> Result<Card, String> {
        let before_show = {
            let kept = self.kept.lock();
            let Some(shown) = kept.iter().find(|kept| kept.card.id == id) else {
                return Err(format!("no card {id} was prepared in this task: prepare one with the tool that makes it"));
            };
            if shown.state.is_some() {
                return Err(format!("card {id} was shown already"));
            }
            if time_left(&shown.card).is_zero() {
                return Err(format!("card {id} has expired: prepare another"));
            }
            shown.before_show.clone()
        };
        if let Some(check) = before_show
            && let Err(reason) = check()
        {
            let hook = {
                let mut kept = self.kept.lock();
                if let Some(pos) = kept.iter().position(|k| k.card.id == id && k.state.is_none()) {
                    kept.remove(pos).on_end
                } else {
                    None
                }
            };
            if let Some(hook) = hook {
                hook(id, CardState::Failed);
            }
            return Err(reason);
        }
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
            shown.before_show = None;
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
        self.kept.lock().push(Kept { card, state: Some(CardState::Open), binding: None, on_end: None, before_show: None });
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

    /// Fails every open `site_login` card matching `own_browser` (or all when `None`), releasing its
    /// pending sensitive guard (and active takeover pause for `chrome_takeover` cards).
    pub fn invalidate_open_site_login(&self, own_browser: Option<bool>, reason: &str) {
        self.invalidate_open_site_login_where(
            |b| match b {
                SiteLoginBinding::Fill(fill) => own_browser.is_none_or(|own| fill.own_browser == own),
                SiteLoginBinding::ChromeTakeover(_) => own_browser != Some(false),
            },
            reason,
        );
    }

    /// Fails every open `site_login` card whose binding satisfies `predicate`.
    pub fn invalidate_open_site_login_where<F>(&self, mut predicate: F, reason: &str)
    where
        F: FnMut(&SiteLoginBinding) -> bool,
    {
        let ids: Vec<(String, bool)> = self
            .kept
            .lock()
            .iter()
            .filter_map(|kept| {
                if kept.card.kind != CardKind::SiteLogin {
                    return None;
                }
                let binding = kept.binding.as_ref()?;
                let is_takeover = matches!(binding, SiteLoginBinding::ChromeTakeover(_));
                let waiting = if is_takeover {
                    matches!(kept.state, Some(CardState::Open | CardState::SignIn))
                } else {
                    kept.state == Some(CardState::Open)
                };
                if waiting && predicate(binding) {
                    Some((kept.card.id.clone(), is_takeover))
                } else {
                    None
                }
            })
            .collect();
        for (id, is_takeover) in ids {
            let from: &[CardState] = if is_takeover {
                &[CardState::Open, CardState::SignIn]
            } else {
                &[CardState::Open]
            };
            let _ = self.set(&id, from, CardState::Failed, Some(reason.to_string()));
        }
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
        let target = self
            .kept
            .lock()
            .iter()
            .find(|kept| kept.card.id == id && time_left(&kept.card).is_zero())
            .map(|kept| {
                (
                    kept.card.kind,
                    kept.card.site_login_body().map(|b| b.mode),
                )
            });
        let Some((kind, mode)) = target else {
            return false;
        };
        let waiting: &[CardState] = match (kind, mode) {
            (CardKind::SiteLogin, Some(her_protocol::card::SiteLoginMode::ChromeTakeover)) => {
                &[CardState::Open, CardState::SignIn]
            }
            (CardKind::SiteLogin, _) => &[CardState::Open],
            _ => &[CardState::Open, CardState::Working, CardState::SignIn, CardState::Retry],
        };
        self.set(id, waiting, CardState::Expired, Some("The card expired before it was answered.".into())).is_ok()
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

impl Drop for Cards {
    fn drop(&mut self) {
        for item in self.kept.get_mut().drain(..) {
            if let Some(hook) = item.on_end {
                hook(&item.card.id, CardState::Expired);
            }
        }
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

#[cfg(test)]
mod tests {
    use super::*;
    use her_protocol::card::{AskedBy, CardBody, SiteLoginBody, SiteLoginField, SiteLoginFieldKind, SiteLoginMode};
    use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

    fn sample_site_login(id: &str, expires_at: &str) -> Card {
        Card {
            id: id.into(),
            kind: CardKind::SiteLogin,
            title: "Sign in to example.test".into(),
            asked_by: AskedBy::Owner,
            expires_at: expires_at.into(),
            ops: Vec::new(),
            body: CardBody::SiteLogin(SiteLoginBody {
                mode: SiteLoginMode::Fill,
                origin: "https://example.test".into(),
                host: "example.test".into(),
                title: None,
                browser: Some("Google Chrome".into()),
                tab: Some("y1".into()),
                fields: vec![SiteLoginField {
                    id: "f1".into(),
                    label: "Password".into(),
                    kind: SiteLoginFieldKind::Password,
                    secret: true,
                }],
            }),
        }
    }

    #[test]
    fn dropping_cards_runs_pending_end_hooks_once() {
        let (ev_tx, _ev_rx) = mpsc::unbounded_channel();
        let (tx, _rx) = mpsc::unbounded_channel();
        let cards = Cards::new(Arc::new(Emitter::new(ev_tx)), tx);
        let pending_calls = Arc::new(AtomicUsize::new(0));
        let ended_calls = Arc::new(AtomicUsize::new(0));

        let make_card = |id: &str| Card {
            id: id.into(),
            kind: CardKind::SiteLogin,
            title: "Sign in".into(),
            asked_by: AskedBy::Owner,
            expires_at: "2099-01-01T00:00:00Z".into(),
            ops: Vec::new(),
            body: CardBody::SiteLogin(SiteLoginBody {
                mode: SiteLoginMode::ChromeTakeover,
                origin: "https://example.com".into(),
                host: "example.com".into(),
                title: None,
                tab: Some("y1".into()),
                browser: None,
                fields: Vec::new(),
            }),
        };

        let p = pending_calls.clone();
        cards.prepare_with_binding(
            make_card("c_pending"),
            None,
            Some(Arc::new(move |id, state| {
                assert_eq!(id, "c_pending");
                assert_eq!(state, CardState::Expired);
                p.fetch_add(1, Ordering::SeqCst);
            })),
            None,
        );

        let e = ended_calls.clone();
        cards.prepare_with_binding(
            make_card("c_ended"),
            None,
            Some(Arc::new(move |id, state| {
                assert_eq!(id, "c_ended");
                assert_eq!(state, CardState::Declined);
                e.fetch_add(1, Ordering::SeqCst);
            })),
            None,
        );
        // Simulate an already-ended card whose hook ran via Cards::set().
        let mut kept = cards.kept.lock();
        if let Some(item) = kept.iter_mut().find(|k| k.card.id == "c_ended") {
            item.state = Some(CardState::Open);
        }
        drop(kept);
        cards.set("c_ended", &[CardState::Open], CardState::Declined, None).unwrap();
        assert_eq!(ended_calls.load(Ordering::SeqCst), 1);

        drop(cards);
        assert_eq!(pending_calls.load(Ordering::SeqCst), 1, "Drop must run pending on_end hooks");
        assert_eq!(ended_calls.load(Ordering::SeqCst), 1, "Drop must not double-run ended hooks");
    }

    #[tokio::test]
    async fn before_show_rejection_runs_cleanup_and_working_site_login_does_not_expire() {
        let (event_tx, _event_rx) = tokio::sync::mpsc::unbounded_channel();
        let (internal_tx, _internal_rx) = tokio::sync::mpsc::unbounded_channel();
        let cards = Arc::new(Cards::new(Arc::new(Emitter::new(event_tx)), internal_tx));

        let cleaned = Arc::new(AtomicBool::new(false));
        let cleaned_hook = cleaned.clone();
        let future = (chrono::Utc::now() + chrono::TimeDelta::minutes(5)).to_rfc3339();
        cards.prepare_with_binding(
            sample_site_login("c_stale", &future),
            None,
            Some(Arc::new(move |_, _| cleaned_hook.store(true, Ordering::SeqCst))),
            Some(Arc::new(|| Err("The page changed before the sign-in card could be shown.".into()))),
        );
        let err = cards.show("c_stale").unwrap_err();
        assert!(err.contains("changed"), "{err}");
        assert!(cleaned.load(Ordering::SeqCst), "on_end cleanup must run when before_show fails");
        assert!(!cards.has("c_stale"));

        // A SiteLogin fill card in Working state must not be expired mid-fill.
        cards.prepare_with_binding(sample_site_login("c_work", &future), None, None, None);
        cards.show("c_work").unwrap();
        cards.set("c_work", &[CardState::Open], CardState::Working, None).unwrap();
        // Force expires_at into the past to simulate timer firing during fill.
        cards.kept.lock().iter_mut().find(|k| k.card.id == "c_work").unwrap().card.expires_at =
            "2020-01-01T00:00:00Z".into();
        assert!(!cards.expire_if_due("c_work"), "Working SiteLogin card must not expire");
        assert_eq!(cards.get("c_work").map(|(_, s)| s), Some(CardState::Working));
    }
}
