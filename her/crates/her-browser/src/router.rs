//! The shared layer of a browser: which session owns which target, and what each tab's
//! events say about it (browser-design §6.1). Events go to the tab of the target they belong
//! to; a session only ever sees the tabs it owns. The router never holds the `Browser`, so a
//! browser nobody uses still closes.

use async_trait::async_trait;
use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;
use std::collections::VecDeque;
use std::net::IpAddr;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use tokio::sync::Notify;
use tokio::sync::mpsc;
use url::Url;

use crate::BrowserError;
use crate::cdp::CdpEvent;
use crate::cdp::Conn;

/// Why a tab is out of reach when only the connection to the user's own browser went: the tab is
/// still open there (browser-design §8.4.7), and what a call in flight did is not known.
const CONNECTION_DROPPED: &str = "the connection to the browser dropped";

/// Console entries kept per tab.
const CONSOLE_KEPT: usize = 500;
/// Notes kept per tab until a result reports them.
const NOTES_KEPT: usize = 50;
/// Events waiting to be handled before the router warns.
const BACKLOG_WARN: usize = 10_000;
/// How long an attach waits for its `attachedToTarget`.
const ATTACH_WAIT: Duration = Duration::from_secs(2);
/// Closed tabs an owner remembers, so a call naming one hears why it is gone.
const CLOSED_KEPT: usize = 20;
/// A request open this long is a long poll or a stream, not part of loading the page.
const LONG_REQUEST: Duration = Duration::from_secs(5);

/// What the kernel says about navigations that the page makes on its own: the model did not
/// ask for them, so only refusals apply (§7.2), never a question.
#[async_trait]
pub trait NavigationGuard: Send + Sync + 'static {
    /// A document request (a link, a redirect, a form, a script). `Err` blocks it.
    async fn check_request(&self, url: &Url) -> Result<(), String>;
    /// The address a document actually came from, against DNS rebinding.
    fn check_address(&self, url: &Url, ip: IpAddr) -> Result<(), String>;
}

/// Allows everything; for tests of the plumbing.
pub struct OpenGuard;

#[async_trait]
impl NavigationGuard for OpenGuard {
    async fn check_request(&self, _url: &Url) -> Result<(), String> {
        Ok(())
    }
    fn check_address(&self, _url: &Url, _ip: IpAddr) -> Result<(), String> {
        Ok(())
    }
}

pub type OwnerId = u64;

/// How a session numbered the tabs and refs it let go (§8.4.7), so that a tab keeps its number when
/// the session finds it again, and an old ref never names a new element.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct TabNumbers {
    /// Target id → tab number.
    pub tabs: HashMap<String, String>,
    pub next_tab: u32,
    pub next_ref: u64,
    pub current: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Frame {
    pub parent: Option<String>,
    pub loader: String,
    pub url: String,
    /// As the page reports it: `https://example.com`, `null`, `file://`.
    pub origin: String,
    /// The CDP session the frame lives in: the tab's, or an out-of-process iframe's.
    pub session: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ConsoleEntry {
    pub seq: u64,
    pub level: String,
    pub text: String,
}

#[derive(Default)]
pub struct TabState {
    pub url: String,
    pub title: String,
    pub main_frame: Option<String>,
    pub frames: HashMap<String, Frame>,
    pub console: VecDeque<ConsoleEntry>,
    pub console_seq: u64,
    /// Where `browser_console` continues from.
    pub console_read: u64,
    /// Errors up to here were already counted in a result.
    pub errors_reported: u64,
    /// Things to say in the next result: dialogs, downloads, new tabs, blocked loads.
    pub notes: Vec<String>,
    /// Load events of the main frame so far.
    pub loads: u64,
    pub loading: bool,
    /// Requests under way, by request id.
    pub inflight: HashMap<String, Request>,
    /// When a request last started or ended.
    pub network_at: Option<Instant>,
    pub file_chooser: Option<i64>,
    pub closed: Option<String>,
}

impl TabState {
    pub fn note(&mut self, text: String) {
        if self.notes.len() < NOTES_KEPT {
            self.notes.push(text);
        }
    }

    fn console(&mut self, level: &str, text: String) {
        self.console_seq += 1;
        let seq = self.console_seq;
        self.console.push_back(ConsoleEntry {
            seq,
            level: level.to_string(),
            text,
        });
        while self.console.len() > CONSOLE_KEPT {
            self.console.pop_front();
        }
    }

    /// Errors logged since the last result counted them.
    pub fn take_new_errors(&mut self) -> usize {
        let new = self
            .console
            .iter()
            .filter(|e| e.seq > self.errors_reported && e.level == "error")
            .count();
        self.errors_reported = self.console_seq;
        new
    }

    pub fn main(&self) -> Option<&Frame> {
        self.main_frame.as_ref().and_then(|id| self.frames.get(id))
    }

    /// No request that loads the page is under way, and none started or ended in the last
    /// `quiet`. Streams (EventSource, WebSocket, media) and requests open longer than
    /// [`LONG_REQUEST`] do not count, so a page that keeps a connection open still settles.
    pub fn network_idle(&self, quiet: Duration) -> bool {
        let busy = self.inflight.values().any(|r| !r.stream && r.started.elapsed() < LONG_REQUEST);
        !busy && self.network_at.is_none_or(|at| at.elapsed() >= quiet)
    }

    /// No request that started at or after `since` is still under way: what an action set
    /// off has finished, whatever the page had open before.
    pub fn requests_done_since(&self, since: Instant) -> bool {
        !self.inflight.values().any(|r| !r.stream && r.started >= since)
    }

    fn request_ended(&mut self, id: &str) {
        if self.inflight.remove(id).is_some() {
            self.network_at = Some(Instant::now());
        }
    }
}

/// A request under way.
#[derive(Clone, Debug)]
pub struct Request {
    /// The document it belongs to.
    pub loader: String,
    pub started: Instant,
    /// Stays open by design: EventSource, WebSocket, media.
    pub stream: bool,
}

/// A page target owned by one session.
pub struct Tab {
    /// Short, per session: "1", "2", …
    pub id: String,
    pub target_id: String,
    pub session: String,
    pub state: Mutex<TabState>,
    /// Woken on every event of the tab.
    pub changed: Notify,
    /// One operation at a time on a tab (§6.5).
    pub busy: tokio::sync::Mutex<()>,
}

impl Tab {
    pub fn is_closed(&self) -> bool {
        self.state.lock().closed.is_some()
    }

    pub fn connection_dropped(&self) -> bool {
        self.state.lock().closed.as_deref() == Some(CONNECTION_DROPPED)
    }
}

struct Owner {
    guard: Arc<dyn NavigationGuard>,
    downloads: PathBuf,
    /// The her session whose window shows the tabs, for an embedded browser (§8.3.4).
    label: Option<String>,
    tabs: Vec<Arc<Tab>>,
    /// Before each tab's number.
    tab_prefix: &'static str,
    next_tab: u32,
    /// The numbers of tabs this session had before, for when it finds them again.
    known: HashMap<String, String>,
}

#[derive(Clone)]
struct Target {
    owner: OwnerId,
    tab: Arc<Tab>,
    page: bool,
}

#[derive(Default)]
struct State {
    owners: HashMap<OwnerId, Owner>,
    next_owner: OwnerId,
    /// By CDP session.
    sessions: HashMap<String, Target>,
    /// Target id → session, for the targets attached.
    attached: HashMap<String, String>,
    /// Targets an owner is attaching to; their `attachedToTarget` may beat the response.
    claims: HashMap<String, OwnerId>,
    downloads: HashMap<String, Download>,
}

struct Download {
    owner: OwnerId,
    tab: Arc<Tab>,
    name: String,
    url: String,
}

pub struct Router {
    pub(crate) conn: Arc<Conn>,
    state: Mutex<State>,
    /// Where the browser saves downloads before they move to their session's directory.
    incoming: PathBuf,
    /// The desktop app's embedded browser (§8.3), which places tabs by `herSession`.
    embedded: bool,
    /// An owner that goes lets its tabs go instead of closing them (§8.4.7).
    keeps_tabs: bool,
    /// Woken whenever a target is registered.
    registered: Notify,
}

impl Router {
    pub(crate) fn start(conn: Arc<Conn>, events: mpsc::UnboundedReceiver<CdpEvent>, incoming: PathBuf, embedded: bool, keeps_tabs: bool) -> Arc<Router> {
        let router = Arc::new(Router {
            conn,
            state: Mutex::new(State::default()),
            incoming,
            embedded,
            keeps_tabs,
            registered: Notify::new(),
        });
        tokio::spawn(route(Arc::downgrade(&router), events));
        router
    }

    /// Browser-wide settings, once after launch.
    pub(crate) async fn init(&self) -> Result<(), BrowserError> {
        self.conn
            .send(None, "Target.setDiscoverTargets", json!({ "discover": true }))
            .await?;
        let _ = std::fs::create_dir_all(&self.incoming);
        // Older browsers lack the events; downloads then just go to the directory.
        let _ = self
            .conn
            .send(
                None,
                "Browser.setDownloadBehavior",
                json!({ "behavior": "allowAndName", "downloadPath": self.incoming, "eventsEnabled": true }),
            )
            .await;
        Ok(())
    }

    pub fn keeps_tabs(&self) -> bool {
        self.keeps_tabs
    }

    /// How `owner` numbered its open tabs.
    pub fn numbers(&self, owner: OwnerId) -> (HashMap<String, String>, u32) {
        let state = self.state.lock();
        let Some(owner) = state.owners.get(&owner) else {
            return (HashMap::new(), 1);
        };
        let tabs = owner.tabs.iter().filter(|t| !t.is_closed()).map(|t| (t.target_id.clone(), t.id.clone()));
        (owner.known.clone().into_iter().chain(tabs).collect(), owner.next_tab)
    }

    pub fn add_owner(
        &self,
        guard: Arc<dyn NavigationGuard>,
        downloads: PathBuf,
        label: Option<String>,
        tab_prefix: &'static str,
        numbers: &TabNumbers,
    ) -> OwnerId {
        let mut state = self.state.lock();
        state.next_owner += 1;
        let id = state.next_owner;
        state.owners.insert(
            id,
            Owner {
                guard,
                downloads,
                label,
                tabs: Vec::new(),
                tab_prefix,
                next_tab: numbers.next_tab.max(1),
                known: numbers.tabs.clone(),
            },
        );
        id
    }

    /// Forgets an owner and closes its tabs; where the tabs are the user's, lets them go.
    pub fn remove_owner(&self, owner: OwnerId) {
        let removed = self.state.lock().owners.remove(&owner);
        if let Some(removed) = removed {
            for tab in removed.tabs.iter().filter(|t| !t.is_closed()) {
                if self.keeps_tabs {
                    self.conn
                        .post(None, "Target.detachFromTarget", json!({ "targetId": tab.target_id, "herRelease": true }));
                } else {
                    self.conn
                        .post(None, "Target.closeTarget", json!({ "targetId": tab.target_id }));
                }
            }
        }
    }

    pub fn tabs(&self, owner: OwnerId) -> Vec<Arc<Tab>> {
        let state = self.state.lock();
        state.owners.get(&owner).map(|o| o.tabs.clone()).unwrap_or_default()
    }

    fn guard(&self, owner: OwnerId) -> Option<Arc<dyn NavigationGuard>> {
        self.state.lock().owners.get(&owner).map(|o| o.guard.clone())
    }

    /// Attach only user-opened pages explicitly assigned to this embedded session.
    pub async fn adopt_pending(&self, owner: OwnerId) -> Result<Vec<Arc<Tab>>, BrowserError> {
        if !self.embedded {
            return Ok(Vec::new());
        }
        let label = self
            .state
            .lock()
            .owners
            .get(&owner)
            .and_then(|o| o.label.clone());
        let Some(label) = label else {
            return Ok(Vec::new());
        };
        let result = self
            .conn
            .send(None, "Target.getTargets", json!({ "herSession": label }))
            .await?;
        let mut tabs = Vec::new();
        if let Some(infos) = result.get("targetInfos").and_then(Value::as_array) {
            for info in infos {
                let id = str_of(info, "targetId");
                if !id.is_empty() {
                    tabs.push(self.attach(owner, id, None).await?);
                }
            }
        }
        Ok(tabs)
    }

    /// Opens a blank tab for `owner`.
    pub async fn create_tab(&self, owner: OwnerId) -> Result<Arc<Tab>, BrowserError> {
        let mut params = json!({ "url": "about:blank" });
        if self.embedded
            && let Some(label) = self.state.lock().owners.get(&owner).and_then(|o| o.label.clone())
        {
            params["herSession"] = Value::String(label);
        }
        let created = self.conn.send(None, "Target.createTarget", params).await?;
        let target_id = str_of(&created, "targetId").to_string();
        self.attach(owner, &target_id, None).await
    }

    /// Attaches to a page target and sets it up as a tab of `owner`.
    async fn attach(&self, owner: OwnerId, target_id: &str, opener: Option<&str>) -> Result<Arc<Tab>, BrowserError> {
        self.state.lock().claims.insert(target_id.to_string(), owner);
        let attached = self
            .conn
            .send(None, "Target.attachToTarget", json!({ "targetId": target_id, "flatten": true }))
            .await;
        // The response and the `attachedToTarget` event race; the event registers the tab.
        let mut tab = None;
        if let Ok(attached) = &attached {
            let session = str_of(attached, "sessionId");
            let deadline = tokio::time::Instant::now() + ATTACH_WAIT;
            loop {
                let registered = self.registered.notified();
                tab = self.tab_of_session(session);
                if tab.is_some() {
                    break;
                }
                if tokio::time::timeout_at(deadline, registered).await.is_err() {
                    break;
                }
            }
        }
        self.state.lock().claims.remove(target_id);
        attached?;
        let tab = tab.ok_or_else(|| BrowserError::Failed("the new tab went away".into()))?;
        if let Some(opener) = opener {
            tab.state.lock().note(format!("Opened by tab {opener}."));
        }
        Ok(tab)
    }

    fn tab_of_session(&self, session: &str) -> Option<Arc<Tab>> {
        self.state.lock().sessions.get(session).map(|t| t.tab.clone())
    }

    /// Registers an attached target. Pages become tabs of `owner`; frames join their tab.
    fn register(&self, owner: OwnerId, session: &str, target_id: &str, parent: Option<Arc<Tab>>) -> Option<Arc<Tab>> {
        let mut state = self.state.lock();
        let state = &mut *state;
        let owner_entry = state.owners.get_mut(&owner)?;
        let (tab, page) = match parent {
            Some(tab) => (tab, false),
            None => {
                let id = owner_entry.known.remove(target_id).unwrap_or_else(|| {
                    let id = format!("{}{}", owner_entry.tab_prefix, owner_entry.next_tab);
                    owner_entry.next_tab += 1;
                    id
                });
                let tab = Arc::new(Tab {
                    id,
                    target_id: target_id.to_string(),
                    session: session.to_string(),
                    state: Mutex::new(TabState::default()),
                    changed: Notify::new(),
                    busy: tokio::sync::Mutex::new(()),
                });
                owner_entry.tabs.push(tab.clone());
                (tab, true)
            }
        };
        state.sessions.insert(
            session.to_string(),
            Target {
                owner,
                tab: tab.clone(),
                page,
            },
        );
        state.attached.insert(target_id.to_string(), session.to_string());
        self.registered.notify_waiters();
        Some(tab)
    }

    /// Turns on what a tab or frame session reports, and seeds its frames.
    async fn set_up(&self, session: &str, tab: &Arc<Tab>) {
        let s = Some(session);
        let calls: [(&str, Value); 6] = [
            ("Page.enable", json!({})),
            ("Runtime.enable", json!({})),
            ("Network.enable", json!({})),
            ("Log.enable", json!({})),
            (
                "Fetch.enable",
                json!({ "patterns": [{ "resourceType": "Document", "requestStage": "Request" }] }),
            ),
            (
                "Target.setAutoAttach",
                json!({ "autoAttach": true, "waitForDebuggerOnStart": false, "flatten": true }),
            ),
        ];
        let sent = calls.map(|(method, params)| self.conn.send(s, method, params));
        for result in futures::future::join_all(sent).await {
            if let Err(err) = result {
                tracing::debug!(%err, "setting up a browser target");
            }
        }
        if let Ok(tree) = self.conn.send(s, "Page.getFrameTree", json!({})).await {
            let mut state = tab.state.lock();
            seed_frames(&mut state, &tree["frameTree"], session);
        }
        let _ = self.conn.send(s, "Runtime.runIfWaitingForDebugger", json!({})).await;
        tab.changed.notify_waiters();
    }

    fn handle(self: &Arc<Self>, event: CdpEvent) {
        match event.session.as_deref() {
            None => self.browser_event(event),
            Some(session) => {
                let target = self.state.lock().sessions.get(session).cloned();
                match (event.method.as_str(), target) {
                    // Children of a tab or a frame: iframes, popups, workers.
                    ("Target.attachedToTarget", _) => self.attached(Some(session), &event.params),
                    ("Target.detachedFromTarget", _) => {
                        if let Some(target_id) = event.params.get("targetId").and_then(Value::as_str) {
                            self.gone(target_id, detached_why(&event.params));
                        }
                    }
                    (_, Some(target)) => {
                        self.target_event(&target, session, &event);
                        target.tab.changed.notify_waiters();
                    }
                    (_, None) => {}
                }
            }
        }
    }

    fn browser_event(self: &Arc<Self>, event: CdpEvent) {
        let params = &event.params;
        match event.method.as_str() {
            "Target.attachedToTarget" => self.attached(None, params),
            "Target.targetCreated" => {
                let info = &params["targetInfo"];
                let Some(opener) = info.get("openerId").and_then(Value::as_str) else {
                    return;
                };
                if str_of(info, "type") != "page" {
                    return;
                }
                let found = {
                    let state = self.state.lock();
                    state
                        .attached
                        .get(opener)
                        .and_then(|s| state.sessions.get(s))
                        .map(|t| (t.owner, t.tab.id.clone()))
                };
                let Some((owner, opener_tab)) = found else {
                    return;
                };
                let target_id = str_of(info, "targetId").to_string();
                let by_person = str_of(info, "herOpenedBy") == "person";
                let router = self.clone();
                tokio::spawn(async move {
                    // The opener's own auto-attach may take the popup first.
                    tokio::time::sleep(Duration::from_millis(30)).await;
                    if router.state.lock().attached.contains_key(&target_id) {
                        return;
                    }
                    match router.attach(owner, &target_id, Some(&opener_tab)).await {
                        Ok(tab) => router.announce(owner, &opener_tab, &tab, by_person),
                        Err(err) => tracing::debug!(%err, "attaching to a new tab"),
                    }
                });
            }
            "Target.targetInfoChanged" => {
                let info = &params["targetInfo"];
                if let Some(tab) = self.tab_by_target(str_of(info, "targetId")) {
                    let mut state = tab.state.lock();
                    state.url = str_of(info, "url").to_string();
                    state.title = str_of(info, "title").to_string();
                    drop(state);
                    tab.changed.notify_waiters();
                }
            }
            "Target.targetDestroyed" => self.gone(str_of(params, "targetId"), "the tab was closed"),
            "Target.targetCrashed" => self.gone(str_of(params, "targetId"), "the tab crashed"),
            "Target.detachedFromTarget" => {
                if let Some(target_id) = params.get("targetId").and_then(Value::as_str) {
                    self.gone(target_id, detached_why(params));
                }
            }
            "Browser.downloadWillBegin" => self.download_began(params),
            "Browser.downloadProgress" => self.download_progress(params),
            _ => {}
        }
    }

    fn tab_by_target(&self, target_id: &str) -> Option<Arc<Tab>> {
        let state = self.state.lock();
        let session = state.attached.get(target_id)?;
        state.sessions.get(session).filter(|t| t.page).map(|t| t.tab.clone())
    }

    fn gone(&self, target_id: &str, why: &str) {
        let removed = {
            let mut state = self.state.lock();
            let Some(session) = state.attached.remove(target_id) else {
                return;
            };
            state.sessions.remove(&session).map(|target| (session, target))
        };
        let Some((session, target)) = removed else {
            return;
        };
        {
            let mut state = target.tab.state.lock();
            if target.page {
                state.closed.get_or_insert_with(|| why.to_string());
                // Only the reason is kept; what the page logged and loaded goes.
                state.frames.clear();
                state.console.clear();
                state.inflight.clear();
                state.notes.clear();
            } else {
                state.frames.retain(|_, frame| frame.session != session);
            }
        }
        if target.page {
            self.forget_closed(target.owner, &target.tab);
        }
        target.tab.changed.notify_waiters();
    }

    /// After a tab closed: its frames' sessions go, and the owner remembers only the latest
    /// [`CLOSED_KEPT`] closed tabs.
    fn forget_closed(&self, owner: OwnerId, tab: &Arc<Tab>) {
        let mut state = self.state.lock();
        let state = &mut *state;
        let frames: Vec<String> = state
            .sessions
            .iter()
            .filter(|(_, t)| Arc::ptr_eq(&t.tab, tab))
            .map(|(s, _)| s.clone())
            .collect();
        for session in &frames {
            state.sessions.remove(session);
        }
        state.attached.retain(|_, session| !frames.contains(session));
        if let Some(owner) = state.owners.get_mut(&owner) {
            let closed = owner.tabs.iter().filter(|t| t.is_closed()).count();
            let mut extra = closed.saturating_sub(CLOSED_KEPT);
            owner.tabs.retain(|t| {
                if extra > 0 && t.is_closed() {
                    extra -= 1;
                    return false;
                }
                true
            });
        }
    }

    /// `attachedToTarget`: an explicit attach (`parent` none), or an auto-attached child of a
    /// tab or frame session.
    fn attached(self: &Arc<Self>, parent: Option<&str>, params: &Value) {
        let session = str_of(params, "sessionId").to_string();
        let info = &params["targetInfo"];
        let target_id = str_of(info, "targetId").to_string();
        let kind = str_of(info, "type");
        let (owner, parent_tab, duplicate) = {
            let state = self.state.lock();
            let duplicate = state.attached.contains_key(&target_id);
            match parent {
                None => (state.claims.get(&target_id).copied(), None, duplicate),
                Some(parent) => match state.sessions.get(parent) {
                    Some(target) => (Some(target.owner), Some(target.tab.clone()), duplicate),
                    None => (None, None, duplicate),
                },
            }
        };
        let keep = matches!(kind, "page" | "iframe") && owner.is_some() && !duplicate;
        if !keep {
            // Workers and the like, targets nobody owns, a second attach to the same target.
            self.conn
                .post(None, "Target.detachFromTarget", json!({ "sessionId": session }));
            return;
        }
        let owner = owner.expect("checked above");
        // A page that a page opened (a popup): its own tab, owned like its opener.
        let parent = if kind == "page" { None } else { parent_tab.clone() };
        let Some(tab) = self.register(owner, &session, &target_id, parent) else {
            self.conn
                .post(None, "Target.detachFromTarget", json!({ "sessionId": session }));
            return;
        };
        // A page already open when the session found it has its title by now.
        if kind == "page" {
            tab.state.lock().title = str_of(info, "title").to_string();
        }
        if kind == "page"
            && let Some(opener) = &parent_tab
        {
            self.announce(owner, &opener.id, &tab, false);
        }
        let router = self.clone();
        tokio::spawn(async move { router.set_up(&session, &tab).await });
    }

    /// Tells the opener's tab about a tab it opened.
    /// `by_person`: the person opened it in the desktop app's panel (§8.3.4), not the page.
    fn announce(&self, owner: OwnerId, opener: &str, tab: &Arc<Tab>, by_person: bool) {
        let opener_tab = self.tabs(owner).into_iter().find(|t| t.id == opener);
        if let Some(opener_tab) = opener_tab {
            let who = if by_person { "The user" } else { "The page" };
            opener_tab
                .state
                .lock()
                .note(format!("{who} opened a new tab: tab {} (select it with browser_tabs).", tab.id));
        }
    }

    fn target_event(self: &Arc<Self>, target: &Target, session: &str, event: &CdpEvent) {
        let params = &event.params;
        let tab = &target.tab;
        match event.method.as_str() {
            "Page.frameNavigated" => {
                let frame = &params["frame"];
                let id = str_of(frame, "id").to_string();
                let parent = frame.get("parentId").and_then(Value::as_str).map(str::to_string);
                let url = format!("{}{}", str_of(frame, "url"), str_of(frame, "urlFragment"));
                let mut state = tab.state.lock();
                if target.page && parent.is_none() {
                    state.main_frame = Some(id.clone());
                    state.url = url.clone();
                    // A new document: the old frames' children report their own detach.
                    state.loading = true;
                    // The old document's requests may never report their end.
                    let loader = str_of(frame, "loaderId");
                    state.inflight.retain(|_, r| r.loader == loader);
                }
                state.frames.insert(
                    id,
                    Frame {
                        parent,
                        loader: str_of(frame, "loaderId").to_string(),
                        url: url.clone(),
                        origin: str_of(frame, "securityOrigin").to_string(),
                        session: session.to_string(),
                    },
                );
                drop(state);
                if url.starts_with("file:") {
                    self.check_file_navigation(target, session, url);
                }
            }
            "Page.navigatedWithinDocument" => {
                let id = str_of(params, "frameId");
                let url = str_of(params, "url").to_string();
                let mut state = tab.state.lock();
                if state.main_frame.as_deref() == Some(id) {
                    state.url = url.clone();
                }
                if let Some(frame) = state.frames.get_mut(id) {
                    frame.url = url;
                }
            }
            "Page.frameDetached" => {
                let id = str_of(params, "frameId");
                let mut state = tab.state.lock();
                if state.frames.get(id).is_some_and(|f| f.session == session) {
                    state.frames.remove(id);
                }
            }
            "Page.frameStartedLoading" => {
                let mut state = tab.state.lock();
                if target.page && state.main_frame.as_deref() == Some(str_of(params, "frameId")) {
                    state.loading = true;
                }
            }
            "Page.frameStoppedLoading" => {
                let mut state = tab.state.lock();
                if target.page && state.main_frame.as_deref() == Some(str_of(params, "frameId")) {
                    state.loading = false;
                }
            }
            "Page.loadEventFired" if target.page => {
                let mut state = tab.state.lock();
                state.loads += 1;
                state.loading = false;
            }
            "Page.javascriptDialogOpening" => {
                let kind = str_of(params, "type").to_string();
                // Alerts and leaving the page go ahead; questions are answered "no" (D-187).
                let accept = matches!(kind.as_str(), "alert" | "beforeunload");
                self.conn
                    .post(Some(session), "Page.handleJavaScriptDialog", json!({ "accept": accept }));
                let message = str_of(params, "message");
                tab.state.lock().note(format!(
                    "A {kind} dialog was shown and {}: \"{message}\"",
                    if accept { "accepted" } else { "dismissed" }
                ));
            }
            "Page.fileChooserOpened" => {
                tab.state.lock().file_chooser = params.get("backendNodeId").and_then(Value::as_i64);
            }
            "Runtime.consoleAPICalled" => {
                let level = match str_of(params, "type") {
                    "error" | "assert" => "error",
                    "warning" => "warning",
                    "debug" => "debug",
                    _ => "info",
                };
                let text = params["args"]
                    .as_array()
                    .map(|args| args.iter().map(remote_text).collect::<Vec<_>>().join(" "))
                    .unwrap_or_default();
                tab.state.lock().console(level, text);
            }
            "Runtime.exceptionThrown" => {
                let details = &params["exceptionDetails"];
                let text = details["exception"]
                    .get("description")
                    .and_then(Value::as_str)
                    .unwrap_or_else(|| str_of(details, "text"))
                    .to_string();
                tab.state.lock().console("error", format!("Uncaught {text}"));
            }
            "Log.entryAdded" => {
                let entry = &params["entry"];
                let level = match str_of(entry, "level") {
                    "error" => "error",
                    "warning" => "warning",
                    "verbose" => "debug",
                    _ => "info",
                };
                // Network errors arrive from the Network domain with their URL.
                if str_of(entry, "source") != "network" {
                    tab.state.lock().console(level, str_of(entry, "text").to_string());
                }
            }
            "Network.requestWillBeSent" => {
                let now = Instant::now();
                let request = Request {
                    loader: str_of(params, "loaderId").to_string(),
                    started: now,
                    stream: matches!(str_of(params, "type"), "EventSource" | "WebSocket" | "Media"),
                };
                let mut state = tab.state.lock();
                state.inflight.insert(str_of(params, "requestId").to_string(), request);
                state.network_at = Some(now);
            }
            "Network.loadingFinished" => {
                tab.state.lock().request_ended(str_of(params, "requestId"));
            }
            "Network.loadingFailed" => {
                let mut state = tab.state.lock();
                state.request_ended(str_of(params, "requestId"));
                let canceled = params.get("canceled").and_then(Value::as_bool).unwrap_or(false);
                if !canceled {
                    let text = format!("{} request failed: {}", str_of(params, "type"), str_of(params, "errorText"));
                    state.console("error", text);
                }
            }
            "Network.responseReceived" => {
                let response = &params["response"];
                let status = response.get("status").and_then(Value::as_i64).unwrap_or(0);
                let url = str_of(response, "url");
                if status >= 400 {
                    tab.state.lock().console("error", format!("HTTP {status} for {url}"));
                }
                if str_of(params, "type") == "Document"
                    && let Some(ip) = remote_ip(response)
                    && let Ok(parsed) = Url::parse(url)
                    && let Some(guard) = self.guard(target.owner)
                    && let Err(reason) = guard.check_address(&parsed, ip)
                {
                    self.conn.post(Some(session), "Page.stopLoading", json!({}));
                    self.conn
                        .post(Some(session), "Page.navigate", json!({ "url": "about:blank" }));
                    tab.state.lock().note(format!("Stopped loading {url}: {reason}"));
                }
            }
            "Fetch.requestPaused" => {
                let request_id = str_of(params, "requestId").to_string();
                let url = str_of(&params["request"], "url").to_string();
                let guard = self.guard(target.owner);
                let conn = self.conn.clone();
                let session = session.to_string();
                let tab = tab.clone();
                tokio::spawn(async move {
                    let verdict = match (Url::parse(&url), guard) {
                        (Ok(parsed), Some(guard)) => guard.check_request(&parsed).await,
                        (Err(_), _) => Err("not a valid URL".to_string()),
                        (_, None) => Err("the session has ended".to_string()),
                    };
                    let s = Some(session.as_str());
                    match verdict {
                        Ok(()) => {
                            let _ = conn.send(s, "Fetch.continueRequest", json!({ "requestId": request_id })).await;
                        }
                        Err(reason) => {
                            tab.state.lock().note(format!("Blocked loading {url}: {reason}"));
                            let _ = conn
                                .send(
                                    s,
                                    "Fetch.failRequest",
                                    // Aborted, not blocked: the page stays as it was instead of
                                    // showing an error page.
                                    json!({ "requestId": request_id, "errorReason": "Aborted" }),
                                )
                                .await;
                            tab.changed.notify_waiters();
                        }
                    }
                });
            }
            _ => {}
        }
    }

    /// A frame that went to a local file on its own: checked after the fact, as file loads
    /// do not pass through request interception.
    fn check_file_navigation(self: &Arc<Self>, target: &Target, session: &str, url: String) {
        let Some(guard) = self.guard(target.owner) else {
            return;
        };
        let conn = self.conn.clone();
        let session = session.to_string();
        let tab = target.tab.clone();
        tokio::spawn(async move {
            let Ok(parsed) = Url::parse(&url) else {
                return;
            };
            if let Err(reason) = guard.check_request(&parsed).await {
                let _ = conn
                    .send(Some(&session), "Page.navigate", json!({ "url": "about:blank" }))
                    .await;
                tab.state.lock().note(format!("Left {url}: {reason}"));
                tab.changed.notify_waiters();
            }
        });
    }

    fn download_began(&self, params: &Value) {
        let frame = str_of(params, "frameId");
        let mut state = self.state.lock();
        let found = state.owners.iter().find_map(|(id, owner)| {
            owner
                .tabs
                .iter()
                .find(|t| t.state.lock().frames.contains_key(frame))
                .map(|t| (*id, t.clone()))
        });
        let Some((owner, tab)) = found else {
            return;
        };
        let name = str_of(params, "suggestedFilename").to_string();
        tab.state.lock().note(format!("A download started: {name}"));
        state.downloads.insert(
            str_of(params, "guid").to_string(),
            Download {
                owner,
                tab,
                name,
                url: str_of(params, "url").to_string(),
            },
        );
    }

    fn download_progress(&self, params: &Value) {
        let state_name = str_of(params, "state");
        if state_name == "inProgress" {
            return;
        }
        let guid = str_of(params, "guid");
        let (download, dir) = {
            let mut state = self.state.lock();
            let Some(download) = state.downloads.remove(guid) else {
                return;
            };
            let dir = state.owners.get(&download.owner).map(|o| o.downloads.clone());
            (download, dir)
        };
        let note = match (state_name, dir) {
            ("completed", Some(dir)) => match move_download(&self.incoming.join(guid), &dir, &download.name) {
                Ok(path) => format!("Downloaded {} to {}", download.url, path.display()),
                Err(err) => format!("Downloaded {} but could not save it: {err}", download.url),
            },
            ("completed", None) => return,
            _ => format!("The download of {} was canceled.", download.name),
        };
        download.tab.state.lock().note(note);
        download.tab.changed.notify_waiters();
    }
}

fn move_download(from: &Path, dir: &Path, name: &str) -> std::io::Result<PathBuf> {
    std::fs::create_dir_all(dir)?;
    let name = Path::new(name)
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .filter(|n| !n.is_empty())
        .unwrap_or_else(|| "download".into());
    let mut path = dir.join(&name);
    let mut n = 1;
    while path.exists() {
        let (stem, ext) = match name.rsplit_once('.') {
            Some((stem, ext)) if !stem.is_empty() => (stem.to_string(), format!(".{ext}")),
            _ => (name.clone(), String::new()),
        };
        path = dir.join(format!("{stem} ({n}){ext}"));
        n += 1;
    }
    if std::fs::rename(from, &path).is_err() {
        std::fs::copy(from, &path)?;
        let _ = std::fs::remove_file(from);
    }
    Ok(path)
}

fn seed_frames(state: &mut TabState, tree: &Value, session: &str) {
    let frame = &tree["frame"];
    let id = str_of(frame, "id").to_string();
    if !id.is_empty() {
        let parent = frame.get("parentId").and_then(Value::as_str).map(str::to_string);
        if parent.is_none() && state.main_frame.is_none() {
            state.main_frame = Some(id.clone());
            state.url = str_of(frame, "url").to_string();
        }
        state.frames.entry(id).or_insert_with(|| Frame {
            parent,
            loader: str_of(frame, "loaderId").to_string(),
            url: str_of(frame, "url").to_string(),
            origin: str_of(frame, "securityOrigin").to_string(),
            session: session.to_string(),
        });
    }
    if let Some(children) = tree.get("childFrames").and_then(Value::as_array) {
        for child in children {
            seed_frames(state, child, session);
        }
    }
}

fn remote_ip(response: &Value) -> Option<IpAddr> {
    let text = response.get("remoteIPAddress")?.as_str()?;
    text.trim_start_matches('[').trim_end_matches(']').parse().ok()
}

/// A console argument as text.
fn remote_text(arg: &Value) -> String {
    match arg.get("value") {
        Some(Value::String(text)) => text.clone(),
        Some(value) if !value.is_null() => value.to_string(),
        _ => arg
            .get("description")
            .or_else(|| arg.get("unserializableValue"))
            .and_then(Value::as_str)
            .unwrap_or_else(|| str_of(arg, "type"))
            .to_string(),
    }
}

/// Why a target detached: the browser end may say (the user took control back, §8.4.2).
fn detached_why(params: &Value) -> &str {
    params.get("reason").and_then(Value::as_str).unwrap_or("the tab was closed")
}

pub(crate) fn str_of<'a>(value: &'a Value, key: &str) -> &'a str {
    value.get(key).and_then(Value::as_str).unwrap_or("")
}

async fn route(router: std::sync::Weak<Router>, mut events: mpsc::UnboundedReceiver<CdpEvent>) {
    // The queue is unbounded so that the reader never stalls the browser; say so if it grows.
    let mut warn_at = BACKLOG_WARN;
    while let Some(event) = events.recv().await {
        let Some(router) = router.upgrade() else {
            break;
        };
        router.handle(event);
        let backlog = events.len();
        if backlog >= warn_at {
            tracing::warn!(backlog, "CDP events are piling up faster than they are handled");
            warn_at = backlog * 2;
        } else if backlog < BACKLOG_WARN / 2 {
            warn_at = BACKLOG_WARN;
        }
    }
    // The browser is gone, or the connection to one that keeps its tabs: every tab says so.
    if let Some(router) = router.upgrade() {
        let why = if router.keeps_tabs { CONNECTION_DROPPED } else { "the browser closed" };
        let tabs: Vec<Arc<Tab>> = router.state.lock().owners.values().flat_map(|o| o.tabs.clone()).collect();
        for tab in tabs {
            tab.state.lock().closed.get_or_insert_with(|| why.to_string());
            tab.changed.notify_waiters();
        }
    }
}
