//! One her session's use of a browser (browser-design §6.1): a lease on the shared browser,
//! the tabs it owns, its refs, and the operations of the tools. Every operation that acts
//! checks first that the page is the one the call was approved for (§7.4).

use async_trait::async_trait;
use base64::Engine;
use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;
use std::collections::HashSet;
use std::net::IpAddr;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use tokio_util::sync::CancellationToken;
use url::Url;

use crate::BrowserError;
use crate::cdp::CdpError;
use crate::input;
use crate::policy::origin_of;
use crate::pool::Browser;
use crate::router::Frame;
use crate::router::NavigationGuard;
use crate::router::OwnerId;
use crate::router::Tab;
use crate::router::TabNumbers;
use crate::router::str_of;
use crate::snapshot;
use crate::snapshot::Item;

/// Lines of diff in an action's result (D-188).
const DIFF_LINES: usize = 60;
/// Characters of a new page's snapshot in a result: about 4k tokens (D-188).
const NEW_PAGE_CHARS: usize = 16_000;
/// Characters of a snapshot before it is cut (§5.2).
const SNAPSHOT_CHARS: usize = 60_000;
/// Iframes expanded inside one another, at most.
const FRAME_DEPTH: usize = 3;
/// How long the network must stay quiet for `browser_wait` to call it idle.
const NETWORK_QUIET: Duration = Duration::from_millis(500);

tokio::task_local! {
    static BRIEF: bool;
}

/// Runs `f` with brief results: calls that change the page say what they did and where the
/// page is, without its snapshot or changes. For the steps of a batch before its last, whose
/// last step then reports every change since the batch began (§5.3).
pub async fn brief<F: std::future::Future>(f: F) -> F::Output {
    BRIEF.scope(true, f).await
}

fn is_brief() -> bool {
    BRIEF.try_with(|brief| *brief).unwrap_or(false)
}

pub struct SessionOptions {
    pub guard: Arc<dyn NavigationGuard>,
    /// Where this session's downloads go.
    pub downloads: PathBuf,
    pub navigation_timeout: Duration,
    pub action_timeout: Duration,
    /// The her session whose window shows the tabs, for an embedded browser (§8.3.4).
    pub label: Option<String>,
    /// Put before each tab's number and each ref, so that the tabs and refs of two browsers in
    /// one her session differ (connectors-entry-design §9.2): "" for the built-in browser.
    pub prefix: &'static str,
    /// How the session numbered the tabs it had before, if it had any.
    pub numbers: TabNumbers,
    /// Said in the first result, in place of what the browser says.
    pub note: Option<String>,
}

/// A page as approved: the tab, the frame, its document and origin. Execution runs only if
/// all four still hold (§7.4).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PageTarget {
    pub tab: String,
    pub frame: String,
    pub loader: String,
    pub origin: String,
}

/// An element a ref names, on its page.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ElementTarget {
    pub page: PageTarget,
    pub backend: i64,
    /// `button "Continue"`, for summaries.
    pub label: String,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Action {
    Click,
    DoubleClick,
    Hover,
    Fill(String),
    Type(String),
    Press(String),
    Select(Vec<String>),
    Check(bool),
    Scroll { dx: f64, dy: f64 },
    Upload(Vec<PathBuf>),
}

impl Action {
    pub fn verb(&self) -> &'static str {
        match self {
            Action::Click => "click",
            Action::DoubleClick => "double-click",
            Action::Hover => "hover",
            Action::Fill(_) => "fill",
            Action::Type(_) => "type into",
            Action::Press(_) => "press a key on",
            Action::Select(_) => "select in",
            Action::Check(true) => "check",
            Action::Check(false) => "uncheck",
            Action::Scroll { .. } => "scroll",
            Action::Upload(_) => "upload files to",
        }
    }
}

/// Where an action happens.
#[derive(Clone, Debug, PartialEq)]
pub enum Place {
    Element(ElementTarget),
    /// Viewport coordinates, in CSS pixels, of the tab's page.
    Point { page: PageTarget, x: f64, y: f64 },
    /// The page itself: keys to the focused element, scrolling the viewport.
    Page(PageTarget),
}

impl Place {
    fn page(&self) -> &PageTarget {
        match self {
            Place::Element(element) => &element.page,
            Place::Point { page, .. } | Place::Page(page) => page,
        }
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub enum SnapshotMode {
    #[default]
    Tree,
    Text,
}

#[derive(Clone, Debug, Default)]
pub struct SnapshotRequest {
    pub tab: Option<String>,
    pub mode: SnapshotMode,
    pub query: Option<String>,
    /// Only this element's subtree.
    pub root: Option<String>,
    /// An iframe's ref: snapshot its content.
    pub frame: Option<String>,
}

/// What a call reports: the page it ended on and the text for the model.
#[derive(Clone, Debug)]
pub struct Report {
    pub text: String,
    pub url: String,
    pub title: String,
    pub tab: String,
}

/// The document a snapshot was of, and its lines.
type LastSnapshot = (String, Vec<String>);

struct RefEntry {
    tab: String,
    frame: String,
    loader: String,
    backend: i64,
    label: String,
}

#[derive(Default)]
struct Refs {
    by_ref: HashMap<String, RefEntry>,
    by_node: HashMap<(String, String, String, i64), String>,
    next: u64,
}

/// The guard, plus the origin the model was just allowed to open: an approval for one
/// navigation covers that load, even where a page-made one would be refused. It covers a
/// private address only when the approval was for one; a name that turns private when it
/// loads is still stopped (DNS rebinding).
struct SessionGuard {
    inner: Arc<dyn NavigationGuard>,
    /// Origin → whether the approval covers a private address.
    approved: Mutex<HashMap<String, bool>>,
}

#[async_trait]
impl NavigationGuard for SessionGuard {
    async fn check_request(&self, url: &Url) -> Result<(), String> {
        if self.approved.lock().contains_key(&origin_of(url)) {
            return Ok(());
        }
        self.inner.check_request(url).await
    }

    fn check_address(&self, url: &Url, ip: IpAddr) -> Result<(), String> {
        let private_ok = self.approved.lock().get(&origin_of(url)).copied().unwrap_or(false);
        if private_ok && crate::policy::classify_ip(ip) == crate::policy::AddressClass::Private {
            return Ok(());
        }
        self.inner.check_address(url, ip)
    }
}

/// Verified login field inspected directly from the page DOM for a `site_login` card.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LoginFieldKind {
    Email,
    Username,
    Password,
    OneTimeCode,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct InspectedLoginField {
    pub backend: i64,
    pub tag: String,
    pub input_type: String,
    pub label: String,
    pub kind: LoginFieldKind,
    pub secret: bool,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SiteLoginInspection {
    pub tab: String,
    pub target_id: String,
    pub frame: String,
    pub loader: String,
    pub origin: String,
    pub url: String,
    pub host: String,
    pub title: Option<String>,
    pub fields: Vec<InspectedLoginField>,
}

/// Verified top-level target inspected from a local Chrome extension tab for isolated user takeover.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ChromeTakeoverInspection {
    pub tab: String,
    pub target_id: String,
    pub frame: String,
    pub loader: String,
    pub origin: String,
    pub host: String,
    pub url: String,
    pub title: Option<String>,
}

/// Live validity of a bound `site_login` target while its card waits for the owner.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LoginTargetStatus {
    Valid,
    Changed,
    Disconnected,
}

/// Active sensitive sign-in guard on a tab (pending card or filled unsubmitted fields).
struct SensitiveTab {
    frame: String,
    origin: String,
    backends: HashSet<i64>,
    pending_cards: HashSet<String>,
    filled: bool,
    discard_logs: bool,
}

pub struct BrowserSession {
    browser: Arc<Browser>,
    owner: OwnerId,
    guard: Arc<SessionGuard>,
    options: SessionOptions,
    current: Mutex<Option<String>>,
    refs: Mutex<Refs>,
    /// The last snapshot of each (tab, frame), with its document, for diffs.
    last: Mutex<HashMap<(String, String), LastSnapshot>>,
    /// Isolated worlds by (frame, document).
    worlds: Mutex<HashMap<(String, String), i64>>,
    /// Said once, in the first result.
    pending_note: Mutex<Option<String>>,
    /// Sensitive documents by tab and loader, including filled documents in browser history.
    sensitive: Mutex<HashMap<String, HashMap<String, SensitiveTab>>>,
    /// Filled tabs adopted on reconnect before their `main_frame` was seeded (`tab.id -> origin`);
    /// fail-closed until `main_frame` arrives and promotes to `sensitive` or proves a new origin.
    filled_unseeded: Mutex<HashMap<String, String>>,
}

impl BrowserSession {
    pub fn new(browser: Arc<Browser>, options: SessionOptions) -> Self {
        let guard = Arc::new(SessionGuard {
            inner: options.guard.clone(),
            approved: Mutex::new(HashMap::new()),
        });
        let owner = browser
            .router
            .add_owner(guard.clone(), options.downloads.clone(), options.label.clone(), options.prefix, &options.numbers);
        let pending_note = Mutex::new(options.note.clone().or_else(|| browser.note.clone()));
        let refs = Refs {
            next: options.numbers.next_ref,
            ..Refs::default()
        };
        Self {
            browser,
            owner,
            guard,
            options,
            current: Mutex::new(None),
            refs: Mutex::new(refs),
            last: Mutex::new(HashMap::new()),
            worlds: Mutex::new(HashMap::new()),
            pending_note,
            sensitive: Mutex::new(HashMap::new()),
            filled_unseeded: Mutex::new(HashMap::new()),
        }
    }

    /// Preserve the real pages (including form state and history) handed over by the UI.
    pub async fn adopt_pending(&self) -> Result<(), BrowserError> {
        for tab in self.browser.router.adopt_pending(self.owner).await? {
            self.wait_for(&tab, Duration::from_secs(5), |state| {
                state.main_frame.is_some()
            })
            .await;
            *self.current.lock() = Some(tab.id.clone());
        }
        // The tab that was current, when the session finds its tabs again.
        if let Some(current) = &self.options.numbers.current
            && self.tabs().iter().any(|t| &t.id == current)
        {
            *self.current.lock() = Some(current.clone());
        }
        Ok(())
    }

    pub fn is_dead(&self) -> bool {
        self.browser.is_dead()
    }

    /// Probes whether the underlying browser connection still responds; closes it on timeout.
    pub async fn check_alive(&self) -> bool {
        self.browser.check_alive(Duration::from_secs(2)).await
    }

    /// Ensures the tab is active in its window before input or screenshot commands that stall or
    /// drop events on inactive/minimized tabs.
    async fn activate(&self, tab: &Tab) {
        let _ = self
            .conn()
            .send_with_timeout(
                None,
                "Target.activateTarget",
                json!({ "targetId": tab.target_id }),
                Duration::from_secs(3),
            )
            .await;
    }

    /// The tabs are the user's and stay when the session lets them go.
    pub fn keeps_tabs(&self) -> bool {
        self.browser.router.keeps_tabs()
    }

    pub fn browser_kind(&self) -> crate::pool::BrowserKind {
        self.browser.kind()
    }

    /// True while any open tab in this session has a pending or filled-unsubmitted `site_login`
    /// document whose observation must remain blocked.
    pub fn has_active_sensitive(&self) -> bool {
        self.tabs().iter().any(|tab| self.sensitive_active(tab).is_some())
    }

    /// True while the open tab matching `target_id` still has an active `site_login` sensitive guard.
    pub fn has_active_sensitive_target(&self, target_id: &str) -> bool {
        self.tabs()
            .iter()
            .find(|tab| tab.target_id == target_id)
            .is_some_and(|tab| self.sensitive_active(tab).is_some())
    }

    /// True while the open tab matching `target_id` has a filled-unsubmitted `site_login` guard.
    pub fn has_filled_target(&self, target_id: &str) -> bool {
        let Some(tab) = self.tabs().into_iter().find(|t| t.target_id == target_id) else {
            return false;
        };
        if self.sensitive_active(&tab).is_none() {
            return false;
        }
        if self.filled_unseeded.lock().contains_key(&tab.id) {
            return true;
        }
        let sensitive = self.sensitive.lock();
        sensitive
            .get(&tab.id)
            .is_some_and(|docs| docs.values().any(|entry| entry.filled))
    }

    /// Re-installs a filled-document observation guard (for example if an extension session
    /// reconnects while a previously filled login page is still loaded).
    pub fn mark_filled_document(&self, tab: &str, frame: &str, loader: &str, origin: &str) {
        self.filled_unseeded.lock().remove(tab);
        let mut sensitive = self.sensitive.lock();
        let entry = sensitive
            .entry(tab.to_string())
            .or_default()
            .entry(loader.to_string())
            .or_insert_with(|| SensitiveTab {
                frame: frame.to_string(),
                origin: origin.to_string(),
                backends: HashSet::new(),
                pending_cards: HashSet::new(),
                filled: true,
                discard_logs: true,
            });
        entry.filled = true;
        entry.discard_logs = true;
    }

    /// Installs a fail-closed tab-level filled guard when a reconnect adopts a filled tab before
    /// its `main_frame` has been seeded; promotes to a document guard once `main_frame` arrives
    /// on the same origin, or clears once a different origin loads or the tab closes.
    pub fn mark_filled_unseeded(&self, tab: &str, origin: &str) {
        self.filled_unseeded
            .lock()
            .insert(tab.to_string(), origin.to_string());
    }

    pub fn tab_handle(&self, id: &str) -> Option<Arc<Tab>> {
        self.tabs().into_iter().find(|t| t.id == id)
    }

    /// Checks whether a bound `site_login` tab still points to the exact verified document.
    pub fn check_login_target(
        &self,
        tab: &Tab,
        frame_id: &str,
        loader: &str,
        origin: &str,
        expected_url: &str,
    ) -> LoginTargetStatus {
        if self.is_dead() || tab.connection_dropped() {
            return LoginTargetStatus::Disconnected;
        }
        let state = tab.state.lock();
        if state.closed.is_some() {
            return LoginTargetStatus::Disconnected;
        }
        let Some(main_id) = state.main_frame.as_deref() else {
            return LoginTargetStatus::Changed;
        };
        if main_id != frame_id {
            return LoginTargetStatus::Changed;
        }
        let Some(frame) = state.frames.get(frame_id) else {
            return LoginTargetStatus::Changed;
        };
        if frame.loader != loader
            || url_origin(&frame.url) != origin
            || frame.url != expected_url
            || !is_allowed_login_url(&frame.url)
        {
            return LoginTargetStatus::Changed;
        }
        LoginTargetStatus::Valid
    }

    /// How this session numbered its tabs and refs, for a later session to go on from.
    pub fn numbers(&self) -> TabNumbers {
        let (tabs, next_tab) = self.browser.router.numbers(self.owner);
        TabNumbers {
            tabs,
            next_tab,
            next_ref: self.refs.lock().next,
            current: self.current.lock().clone(),
        }
    }

    fn conn(&self) -> &crate::cdp::Conn {
        &self.browser.conn
    }

    /// The open tabs, in order.
    pub fn tabs(&self) -> Vec<Arc<Tab>> {
        self.browser
            .router
            .tabs(self.owner)
            .into_iter()
            .filter(|t| !t.is_closed())
            .collect()
    }

    fn tab(&self, id: &str) -> Result<Arc<Tab>, BrowserError> {
        let tabs = self.browser.router.tabs(self.owner);
        let tab = tabs
            .into_iter()
            .find(|t| t.id == id)
            .ok_or_else(|| BrowserError::StaleRef(format!("There is no tab {id}. List tabs with browser_tabs.")))?;
        if let Some(why) = tab.state.lock().closed.clone() {
            return Err(BrowserError::StaleRef(format!("Tab {id} is gone: {why}.")));
        }
        Ok(tab)
    }

    /// The tab a call means: the one named, else the current one.
    pub fn resolve_tab(&self, id: Option<&str>) -> Result<Arc<Tab>, BrowserError> {
        if let Some(id) = id {
            return self.tab(id);
        }
        let current = self.current.lock().clone();
        if let Some(current) = current
            && let Ok(tab) = self.tab(&current)
        {
            return Ok(tab);
        }
        // The current tab closed: the last open one takes over.
        let tab = self.tabs().pop().ok_or_else(|| {
            BrowserError::StaleRef("No page is open. Open one with browser_navigate.".into())
        })?;
        *self.current.lock() = Some(tab.id.clone());
        Ok(tab)
    }

    pub fn current_tab_id(&self) -> Option<String> {
        self.resolve_tab(None).ok().map(|t| t.id.clone())
    }

    pub fn select_tab(&self, id: &str) -> Result<Arc<Tab>, BrowserError> {
        let tab = self.tab(id)?;
        *self.current.lock() = Some(tab.id.clone());
        self.browser
            .conn
            .post(None, "Target.activateTarget", json!({ "targetId": tab.target_id }));
        Ok(tab)
    }

    pub async fn open_tab(&self) -> Result<Arc<Tab>, BrowserError> {
        let tab = self.browser.router.create_tab(self.owner).await?;
        self.wait_for(&tab, Duration::from_secs(5), |state| state.main_frame.is_some())
            .await;
        *self.current.lock() = Some(tab.id.clone());
        Ok(tab)
    }

    pub async fn close_tab(&self, id: &str) -> Result<(), BrowserError> {
        let tab = self.tab(id)?;
        self.conn()
            .send(None, "Target.closeTarget", json!({ "targetId": tab.target_id }))
            .await?;
        self.wait_for(&tab, Duration::from_secs(3), |state| state.closed.is_some())
            .await;
        if self.current.lock().as_deref() == Some(id) {
            *self.current.lock() = None;
        }
        Ok(())
    }

    /// The page of a tab's main frame, as an approval would name it.
    pub fn page(&self, tab: Option<&str>) -> Result<PageTarget, BrowserError> {
        let tab = self.resolve_tab(tab)?;
        let state = tab.state.lock();
        let frame_id = state
            .main_frame
            .clone()
            .ok_or_else(|| BrowserError::failed("the tab has no page yet"))?;
        let frame = state.frames.get(&frame_id).ok_or_else(|| BrowserError::failed("the tab has no page yet"))?;
        Ok(target_of(&tab.id, &frame_id, frame))
    }

    /// The element a ref names, if it is still there (D-185).
    pub async fn element(&self, reference: &str) -> Result<ElementTarget, BrowserError> {
        let (tab_id, frame_id, loader, backend, label) = {
            let refs = self.refs.lock();
            let entry = refs.by_ref.get(reference).ok_or_else(|| {
                let given = reference
                    .strip_prefix(self.options.prefix)
                    .and_then(|r| r.strip_prefix('e'))
                    .and_then(|n| n.parse::<u64>().ok())
                    .is_some_and(|n| n >= 1 && n <= refs.next);
                BrowserError::StaleRef(if given {
                    format!(
                        "Ref {reference} is from before the page navigated or its tab closed. Take a new snapshot."
                    )
                } else {
                    format!(
                        "Unknown ref {reference}. Refs come from the latest browser_snapshot or action result; take a snapshot."
                    )
                })
            })?;
            (entry.tab.clone(), entry.frame.clone(), entry.loader.clone(), entry.backend, entry.label.clone())
        };
        let tab = self.tab(&tab_id)?;
        let frame = {
            let state = tab.state.lock();
            match state.frames.get(&frame_id) {
                Some(frame) if frame.loader == loader => frame.clone(),
                Some(_) => {
                    return Err(BrowserError::StaleRef(format!(
                        "Ref {reference} is from before the page navigated (now {}). Take a new snapshot.",
                        state.url
                    )));
                }
                None => {
                    return Err(BrowserError::StaleRef(format!(
                        "Ref {reference} was in a frame that is gone. Take a new snapshot."
                    )));
                }
            }
        };
        let resolved = self
            .conn()
            .send(Some(&frame.session), "DOM.resolveNode", json!({ "backendNodeId": backend }))
            .await;
        if resolved.is_err() {
            return Err(BrowserError::StaleRef(format!(
                "The element of ref {reference} ({label}) is gone from the page. Take a new snapshot."
            )));
        }
        Ok(ElementTarget {
            page: target_of(&tab.id, &frame_id, &frame),
            backend,
            label,
        })
    }

    /// The innermost frame at a point of a tab's viewport (D-182).
    pub async fn frame_at(&self, tab: Option<&str>, x: f64, y: f64) -> Result<PageTarget, BrowserError> {
        let tab = self.resolve_tab(tab)?;
        let hit = self
            .conn()
            .send(
                Some(&tab.session),
                "DOM.getNodeForLocation",
                json!({ "x": x.round() as i64, "y": y.round() as i64, "includeUserAgentShadowDOM": true }),
            )
            .await
            .map_err(|_| BrowserError::failed(format!("Nothing is at ({x}, {y}) on the page.")))?;
        let mut frame_id = str_of(&hit, "frameId").to_string();
        // An out-of-process iframe shows up as its owner element in the parent document.
        if let Some(backend) = hit.get("backendNodeId").and_then(Value::as_i64)
            && let Ok(node) = self
                .conn()
                .send(Some(&tab.session), "DOM.describeNode", json!({ "backendNodeId": backend }))
                .await
            && let Some(child) = node["node"].get("frameId").and_then(Value::as_str)
            && tab.state.lock().frames.contains_key(child)
        {
            frame_id = child.to_string();
        }
        let state = tab.state.lock();
        let frame = state
            .frames
            .get(&frame_id)
            .ok_or_else(|| BrowserError::failed("cannot tell which frame is at that point; nothing was sent"))?;
        Ok(target_of(&tab.id, &frame_id, frame))
    }

    /// Fails unless the page is still the approved one (§7.4).
    fn verify(&self, target: &PageTarget) -> Result<(Arc<Tab>, Frame), BrowserError> {
        let tab = self.tab(&target.tab).map_err(|_| BrowserError::PageChanged {
            now: "the tab is closed".into(),
        })?;
        let state = tab.state.lock();
        match state.frames.get(&target.frame) {
            Some(frame)
                if frame.loader == target.loader && url_origin(&frame.url) == target.origin =>
            {
                let frame = frame.clone();
                drop(state);
                Ok((tab, frame))
            }
            _ => Err(BrowserError::PageChanged { now: state.url.clone() }),
        }
    }

    /// Registers a pending `site_login` card on `tab`, blocking model observation until the card
    /// is cancelled/expired without filling or the page safely navigates away.
    pub fn begin_sensitive_card(&self, card_id: &str, tab: &str, frame: &str, loader: &str, origin: &str, backends: &[i64]) {
        let mut sensitive = self.sensitive.lock();
        let entry = sensitive
            .entry(tab.to_string())
            .or_default()
            .entry(loader.to_string())
            .or_insert_with(|| SensitiveTab {
                frame: frame.to_string(),
                origin: origin.to_string(),
                backends: HashSet::new(),
                pending_cards: HashSet::new(),
                filled: false,
                discard_logs: false,
            });
        entry.backends.extend(backends.iter().copied());
        entry.pending_cards.insert(card_id.to_string());
    }

    /// Releases a card that ended without filling.
    pub fn end_sensitive_card(&self, card_id: &str) {
        self.sensitive.lock().retain(|_, documents| {
            documents.retain(|_, entry| {
                entry.pending_cards.remove(card_id);
                entry.filled || !entry.pending_cards.is_empty()
            });
            !documents.is_empty()
        });
    }

    fn sensitive_active(&self, tab: &Tab) -> Option<HashSet<i64>> {
        let mut sensitive = self.sensitive.lock();
        let mut unseeded = self.filled_unseeded.lock();
        if tab.is_closed() {
            sensitive.remove(&tab.id);
            unseeded.remove(&tab.id);
            return None;
        }
        if let Some(origin) = unseeded.get(&tab.id).cloned() {
            let mut state = tab.state.lock();
            let seeded = state.main_frame.as_ref().and_then(|fid| {
                state.frames.get(fid).map(|f| (fid.clone(), f.loader.clone(), f.url.clone()))
            });
            match seeded {
                None => {
                    return Some(HashSet::new());
                }
                Some((frame_id, loader, current_url)) if url_origin(&current_url) == origin => {
                    unseeded.remove(&tab.id);
                    let entry = sensitive
                        .entry(tab.id.clone())
                        .or_default()
                        .entry(loader)
                        .or_insert_with(|| SensitiveTab {
                            frame: frame_id,
                            origin,
                            backends: HashSet::new(),
                            pending_cards: HashSet::new(),
                            filled: true,
                            discard_logs: true,
                        });
                    entry.filled = true;
                    entry.discard_logs = true;
                    return Some(entry.backends.clone());
                }
                Some(_) => {
                    unseeded.remove(&tab.id);
                    state.console.clear();
                    state.notes.clear();
                }
            }
        }
        drop(unseeded);
        let documents = sensitive.get_mut(&tab.id)?;
        let mut state = tab.state.lock();
        let mut active = None;
        let mut discard_logs = false;
        for (loader, entry) in documents {
            if state.frames.get(&entry.frame).is_some_and(|f| &f.loader == loader && url_origin(&f.url) == entry.origin) {
                entry.discard_logs = entry.filled;
                active = Some(entry.backends.clone());
            } else {
                discard_logs |= std::mem::take(&mut entry.discard_logs);
            }
        }
        if discard_logs {
            state.console.clear();
            state.notes.clear();
        }
        active
    }

    /// Inspects `refs` directly in the bound browser DOM to prepare a `site_login` card.
    /// Refuses unless reliable target verification & isolation hold: live session, HTTPS or
    /// loopback origin, top-level same-origin document, no CAPTCHA/anti-bot challenge, and 1–3
    /// visible login inputs including at least one password or one-time code field.
    pub async fn inspect_site_login(&self, refs: &[String]) -> Result<SiteLoginInspection, BrowserError> {
        if self.is_dead() {
            return Err(CdpError::Closed.into());
        }
        if refs.is_empty() || refs.len() > 3 {
            return Err(BrowserError::failed(
                "ask_owner takes 1 to 3 login field refs (such as email/username, password, or one-time code).",
            ));
        }
        let mut seen = HashSet::new();
        let mut elements = Vec::with_capacity(refs.len());
        for reference in refs {
            if !seen.insert(reference.as_str()) {
                return Err(BrowserError::failed(format!("duplicate ref {reference} in ask_owner")));
            }
            elements.push(self.element(reference).await?);
        }
        let first_page = &elements[0].page;
        if elements.iter().any(|e| &e.page != first_page) {
            return Err(BrowserError::failed(
                "All sign-in fields must be on the same page and same-origin frame. Use browser_wait with user instead.",
            ));
        }
        let (tab, _) = self.verify(first_page)?;
        let _busy = tab.busy.lock().await;
        let (tab, frame) = self.verify(first_page)?;
        if self.sensitive_active(&tab).is_some() {
            return Err(BrowserError::failed("A sensitive sign-in step is already active on this page."));
        }
        let (main_id, main_frame) = self.main_frame(&tab)?;
        let top_origin = url_origin(&main_frame.url);
        if first_page.frame != main_id || top_origin != first_page.origin || first_page.origin == "null" {
            return Err(BrowserError::failed(
                "Secure sign-in cards are disabled for cross-origin or nested iframes. Use browser_wait with user so the owner can sign in directly.",
            ));
        }
        let parsed = Url::parse(&main_frame.url).map_err(|_| BrowserError::failed("cannot verify the page URL; use browser_wait with user"))?;
        if !is_allowed_login_url(&main_frame.url) {
            return Err(BrowserError::failed(format!(
                "Secure sign-in cards require HTTPS (found {}). Use browser_wait with user so the owner can sign in directly.",
                first_page.origin
            )));
        }
        let host = match (parsed.host_str(), parsed.port()) {
            (Some(h), Some(p)) => format!("{h}:{p}"),
            (Some(h), None) => h.to_string(),
            _ => first_page.origin.clone(),
        };
        self.check_no_challenge(&main_id, &frame).await?;
        let mut fields = Vec::with_capacity(elements.len());
        for element in &elements {
            let inspected = self.inspect_login_input(&tab, &main_id, &frame, element).await?;
            fields.push(inspected);
        }
        let has_secret = fields
            .iter()
            .any(|f| matches!(f.kind, LoginFieldKind::Password | LoginFieldKind::OneTimeCode));
        if !has_secret {
            return Err(BrowserError::failed(
                "Secure sign-in cards are only for login credentials (must include a password or one-time code field), not general form collection.",
            ));
        }
        // If any plain text field was accepted as username, require a password field alongside it.
        let has_password = fields.iter().any(|f| f.kind == LoginFieldKind::Password);
        if !has_password && fields.iter().any(|f| f.kind == LoginFieldKind::Username && f.input_type == "text") {
            return Err(BrowserError::failed(
                "A plain text field can only be included alongside a password field on a sign-in form.",
            ));
        }
        self.refresh(&tab).await;
        // Re-verify the exact bound document did not jump while inspecting fields.
        let (latest_main_id, latest_main_frame) = self.main_frame(&tab)?;
        if latest_main_id != main_id
            || latest_main_frame.loader != frame.loader
            || latest_main_frame.url != main_frame.url
            || url_origin(&latest_main_frame.url) != first_page.origin
        {
            return Err(BrowserError::PageChanged { now: latest_main_frame.url });
        }
        let raw_title = tab.state.lock().title.clone();
        let title = {
            let cleaned = snapshot::clean(&raw_title);
            let clipped: String = cleaned.chars().take(80).collect();
            (!clipped.is_empty()).then_some(clipped)
        };
        Ok(SiteLoginInspection {
            tab: tab.id.clone(),
            target_id: tab.target_id.clone(),
            frame: main_id,
            loader: frame.loader,
            origin: first_page.origin.clone(),
            url: main_frame.url,
            host,
            title,
            fields,
        })
    }

    /// Inspects a local Chrome extension tab to prepare an isolated user-takeover `site_login`
    /// card. Never reads form values, passwords, or cookies; enforces top-level HTTPS (or loopback
    /// test fixture) origin and rejects cross-origin/nested iframe targets.
    pub async fn inspect_chrome_takeover(&self, tab: Option<&str>, refs: &[String]) -> Result<ChromeTakeoverInspection, BrowserError> {
        if !self.keeps_tabs() {
            return Err(BrowserError::failed("chrome takeover sign-in cards require the user's own Chrome extension"));
        }
        let (target_tab, first_page) = if !refs.is_empty() {
            let mut seen = HashSet::new();
            let mut elements = Vec::with_capacity(refs.len());
            for reference in refs {
                if !seen.insert(reference.as_str()) {
                    return Err(BrowserError::failed(format!("duplicate ref {reference} in ask_owner")));
                }
                elements.push(self.element(reference).await?);
            }
            let first_page = elements[0].page.clone();
            if elements.iter().any(|e| e.page != first_page) {
                return Err(BrowserError::failed(
                    "All refs in ask_owner must belong to the same page and top-level frame. Fall back to manual login.",
                ));
            }
            if let Some(requested_tab) = tab
                && requested_tab != first_page.tab
            {
                return Err(BrowserError::failed(format!(
                    "Tab {requested_tab} does not match ref tab {}. Fall back to manual login.",
                    first_page.tab
                )));
            }
            let (tab_arc, _) = self.verify(&first_page)?;
            let (main_id, main_frame) = self.main_frame(&tab_arc)?;
            let top_origin = url_origin(&main_frame.url);
            if first_page.frame != main_id || top_origin != first_page.origin {
                return Err(BrowserError::failed(
                    "Chrome sign-in cards are disabled for cross-origin or nested iframes. Fall back to manual login.",
                ));
            }
            (tab_arc, Some(first_page))
        } else {
            (self.resolve_tab(tab)?, None)
        };
        let _busy = target_tab.busy.lock().await;
        if self.has_active_sensitive() {
            return Err(BrowserError::failed("A sensitive sign-in step is already active in Chrome."));
        }
        self.refresh(&target_tab).await;
        if let Some(page) = &first_page {
            self.verify(page)?;
        }
        let (main_id, main_frame) = self.main_frame(&target_tab)?;
        let origin = url_origin(&main_frame.url);
        let parsed = Url::parse(&main_frame.url)
            .map_err(|_| BrowserError::failed("Cannot verify the Chrome page URL; fall back to manual login."))?;
        if !is_allowed_login_url(&main_frame.url) {
            return Err(BrowserError::failed(format!(
                "Chrome sign-in cards require an HTTPS origin (found {origin}). Fall back to manual login."
            )));
        }
        // Re-check tab-level URL reported by Target.getTargetInfo matches the main frame origin.
        let tab_url = target_tab.state.lock().url.clone();
        if !tab_url.is_empty() && url_origin(&tab_url) != origin {
            return Err(BrowserError::PageChanged { now: tab_url });
        }
        let host = match (parsed.host_str(), parsed.port()) {
            (Some(h), Some(p)) => format!("{h}:{p}"),
            (Some(h), None) => h.to_string(),
            _ => origin.clone(),
        };
        let raw_title = target_tab.state.lock().title.clone();
        let title = {
            let cleaned = snapshot::clean(&raw_title);
            let clipped: String = cleaned.chars().take(80).collect();
            (!clipped.is_empty()).then_some(clipped)
        };
        Ok(ChromeTakeoverInspection {
            tab: target_tab.id.clone(),
            target_id: target_tab.target_id.clone(),
            frame: main_id,
            loader: main_frame.loader,
            origin,
            host,
            url: main_frame.url,
            title,
        })
    }

    /// Explicitly detaches Chrome's debugger from every open tab owned by this extension session
    /// and awaits acknowledgement so sensitive sign-in starts only after reliable isolation holds.
    pub async fn release_all_tabs(&self) -> Result<(), BrowserError> {
        if !self.keeps_tabs() {
            return Err(BrowserError::failed("release_all_tabs requires an extension session"));
        }
        let open = self.tabs();
        for tab in open {
            if tab.is_closed() {
                continue;
            }
            self.conn()
                .send_with_timeout(
                    None,
                    "Target.detachFromTarget",
                    json!({ "targetId": tab.target_id, "herRelease": true }),
                    Duration::from_secs(5),
                )
                .await?;
        }
        Ok(())
    }

    async fn check_no_challenge(&self, frame_id: &str, frame: &Frame) -> Result<(), BrowserError> {
        let world = self.world(frame_id, frame).await?;
        let res = self
            .conn()
            .send(
                Some(&frame.session),
                "Runtime.evaluate",
                json!({
                    "expression": CHALLENGE_CHECK_SCRIPT,
                    "contextId": world,
                    "returnByValue": true,
                }),
            )
            .await?;
        if let Some(reason) = res["result"]["value"].as_str().filter(|s| !s.is_empty()) {
            return Err(BrowserError::failed(format!(
                "Secure sign-in cards are disabled on pages with {reason}. Use browser_wait with user so the owner can complete sign-in directly."
            )));
        }
        Ok(())
    }

    async fn inspect_login_input(
        &self,
        tab: &Tab,
        frame_id: &str,
        frame: &Frame,
        element: &ElementTarget,
    ) -> Result<InspectedLoginField, BrowserError> {
        // Ensure visible & not obscured by another element.
        let _ = self.clickable_point(tab, element).await?;
        let info = self
            .call_on(frame_id, frame, element.backend, INSPECT_LOGIN_INPUT_SCRIPT, vec![])
            .await?;
        if let Some(err) = info.get("error").and_then(Value::as_str) {
            return Err(BrowserError::failed(format!(
                "Field {} cannot be used on a sign-in card: {err}. Use browser_wait with user instead.",
                element.label
            )));
        }
        let tag = info["tag"].as_str().unwrap_or("").to_string();
        let input_type = info["type"].as_str().unwrap_or("text").to_string();
        let raw_label = info["label"].as_str().unwrap_or("");
        let kind = match info["kind"].as_str() {
            Some("password") => LoginFieldKind::Password,
            Some("one_time_code") => LoginFieldKind::OneTimeCode,
            Some("email") => LoginFieldKind::Email,
            Some("username") => LoginFieldKind::Username,
            _ => {
                return Err(BrowserError::failed(format!(
                    "Field {} is not a recognized sign-in field (email, username, password, or one-time code).",
                    element.label
                )));
            }
        };
        let secret = matches!(kind, LoginFieldKind::Password | LoginFieldKind::OneTimeCode);
        let fallback = match kind {
            LoginFieldKind::Email => "Email",
            LoginFieldKind::Username => "Username",
            LoginFieldKind::Password => "Password",
            LoginFieldKind::OneTimeCode => "Verification code",
        };
        let clean_label: String = snapshot::clean(raw_label)
            .chars()
            .filter(|c| !c.is_control())
            .take(60)
            .collect();
        let label = if clean_label.is_empty() { fallback.to_string() } else { clean_label };
        Ok(InspectedLoginField {
            backend: element.backend,
            tag,
            input_type,
            label,
            kind,
            secret,
        })
    }

    /// Atomically verifies the bound page and fields, fills each field without submitting, and
    /// keeps the tab's sensitive observation guard active until safe navigation recovery.
    pub async fn fill_site_login(
        &self,
        card_id: &str,
        tab_id: &str,
        frame_id: &str,
        loader: &str,
        origin: &str,
        expected_url: &str,
        fields: &[(i64, String, String)],
    ) -> Result<(), BrowserError> {
        if self.is_dead() {
            return Err(CdpError::Closed.into());
        }
        let page = PageTarget {
            tab: tab_id.to_string(),
            frame: frame_id.to_string(),
            loader: loader.to_string(),
            origin: origin.to_string(),
        };
        let (tab, _) = self.verify(&page)?;
        let _busy = tab.busy.lock().await;
        if self.is_dead() || tab.connection_dropped() {
            return Err(CdpError::Closed.into());
        }
        let (tab, frame) = self.verify(&page)?;
        let (main_id, main_frame) = self.main_frame(&tab)?;
        if main_id != frame_id
            || url_origin(&main_frame.url) != origin
            || main_frame.url != expected_url
            || !is_allowed_login_url(&main_frame.url)
        {
            return Err(BrowserError::PageChanged { now: main_frame.url });
        }
        self.activate(&tab).await;
        self.check_no_challenge(frame_id, &frame).await?;

        // Re-verify every bound element BEFORE typing a single character.
        for (backend, expected_type, _) in fields {
            let target = ElementTarget {
                page: page.clone(),
                backend: *backend,
                label: "sign-in field".into(),
            };
            let _ = self.clickable_point(&tab, &target).await.map_err(|_| BrowserError::PageChanged {
                now: main_frame.url.clone(),
            })?;
            let info = self
                .call_on(frame_id, &frame, *backend, INSPECT_LOGIN_INPUT_SCRIPT, vec![])
                .await
                .map_err(|_| BrowserError::PageChanged {
                    now: main_frame.url.clone(),
                })?;
            if info.get("error").is_some()
                || info["tag"].as_str() != Some("INPUT")
                || info["type"].as_str() != Some(expected_type.as_str())
            {
                return Err(BrowserError::PageChanged {
                    now: main_frame.url.clone(),
                });
            }
        }

        // Keep observation blocked even if the card ends during a write or the page handles
        // input before a later field can be filled.
        {
            let mut sensitive = self.sensitive.lock();
            let entry = sensitive
                .get_mut(&tab.id)
                .and_then(|documents| documents.get_mut(loader))
                .filter(|entry| entry.frame == frame_id && entry.origin == origin)
                .ok_or_else(|| BrowserError::failed("The sign-in card is no longer open."))?;
            if !entry.pending_cards.remove(card_id) {
                return Err(BrowserError::failed("The sign-in card is no longer open."));
            }
            entry.filled = true;
            entry.discard_logs = true;
        }

        // Write to the bound DOM node without relying on the renderer's current focus.
        let mut filled_backends = Vec::with_capacity(fields.len());
        for (backend, input_type, value) in fields {
            if self.verify(&page).is_err()
                || self.main_frame(&tab).map(|(_, f)| f.url != expected_url).unwrap_or(true)
            {
                self.clear_inputs(frame_id, &frame, &filled_backends).await;
                return Err(BrowserError::PageChanged { now: main_frame.url });
            }
            let result = self
                .call_on(
                    frame_id,
                    &frame,
                    *backend,
                    FILL_LOGIN_INPUT_SCRIPT,
                    vec![json!(origin), json!(expected_url), json!(input_type), json!(value)],
                )
                .await;
            if !matches!(result, Ok(Value::Bool(true))) {
                self.clear_inputs(frame_id, &frame, &filled_backends).await;
                return Err(BrowserError::PageChanged { now: main_frame.url });
            }
            filled_backends.push(*backend);
        }
        Ok(())
    }

    async fn clear_inputs(&self, frame_id: &str, frame: &Frame, backends: &[i64]) {
        for backend in backends {
            let _ = self
                .call_on(
                    frame_id,
                    frame,
                    *backend,
                    "function(){ if ('value' in this) { this.value = ''; this.dispatchEvent(new Event('input', {bubbles: true})); } }",
                    vec![],
                )
                .await;
        }
    }

    // ---- Navigation ----

    /// Opens `url` in the tab (a new one when none is open). `private_ok`: the approval was
    /// for a private network address.
    pub async fn navigate(&self, tab: Option<&str>, url: &Url, private_ok: bool) -> Result<Report, BrowserError> {
        let tab = match (tab, self.resolve_tab(None)) {
            (Some(id), _) => self.tab(id)?,
            (None, Ok(tab)) => tab,
            (None, Err(_)) => self.open_tab().await?,
        };
        *self.current.lock() = Some(tab.id.clone());
        let _busy = tab.busy.lock().await;
        let origin = origin_of(url);
        self.guard.approved.lock().insert(origin.clone(), private_ok);
        let result = self.navigate_locked(&tab, url).await;
        self.guard.approved.lock().remove(&origin);
        drop(_busy);
        result?;
        self.report(&tab, None, "Opened the page.").await
    }

    async fn navigate_locked(&self, tab: &Arc<Tab>, url: &Url) -> Result<(), BrowserError> {
        let notes_before = tab.state.lock().notes.len();
        let sent = self
            .conn()
            .send_with_timeout(
                Some(&tab.session),
                "Page.navigate",
                json!({ "url": url.as_str() }),
                self.options.navigation_timeout,
            )
            .await?;
        if let Some(error) = sent.get("errorText").and_then(Value::as_str).filter(|e| !e.is_empty()) {
            let blocked = tab.state.lock().notes.get(notes_before..).and_then(|notes| {
                notes.iter().find(|n| n.starts_with("Blocked loading")).cloned()
            });
            return Err(BrowserError::Failed(match blocked {
                Some(note) => note,
                None => format!("Could not open {url}: {error}"),
            }));
        }
        let Some(loader) = sent.get("loaderId").and_then(Value::as_str).map(str::to_string) else {
            // Same document: a fragment change.
            return Ok(());
        };
        self.wait_for(tab, self.options.navigation_timeout, |state| {
            state.main().is_some_and(|f| f.loader == loader) && !state.loading
        })
        .await;
        Ok(())
    }

    pub async fn history(&self, tab: Option<&str>, step: i64) -> Result<Report, BrowserError> {
        let tab = self.resolve_tab(tab)?;
        let _busy = tab.busy.lock().await;
        let history = self.conn().send(Some(&tab.session), "Page.getNavigationHistory", json!({})).await?;
        let index = history["currentIndex"].as_i64().unwrap_or(0) + step;
        let entry = history["entries"]
            .as_array()
            .and_then(|entries| usize::try_from(index).ok().and_then(|i| entries.get(i)))
            .ok_or_else(|| {
                BrowserError::failed(if step < 0 { "There is no page to go back to." } else { "There is no page to go forward to." })
            })?;
        let before = self.main_loader(&tab);
        let since = Instant::now();
        self.conn()
            .send(
                Some(&tab.session),
                "Page.navigateToHistoryEntry",
                json!({ "entryId": entry["id"] }),
            )
            .await?;
        self.settle(&tab, before, since).await;
        drop(_busy);
        self.report(&tab, None, if step < 0 { "Went back." } else { "Went forward." }).await
    }

    pub async fn reload(&self, tab: Option<&str>) -> Result<Report, BrowserError> {
        let tab = self.resolve_tab(tab)?;
        let _busy = tab.busy.lock().await;
        let before = self.main_loader(&tab);
        let since = Instant::now();
        self.conn().send(Some(&tab.session), "Page.reload", json!({})).await?;
        self.settle(&tab, before, since).await;
        drop(_busy);
        self.report(&tab, None, "Reloaded the page.").await
    }

    fn main_loader(&self, tab: &Tab) -> Option<String> {
        tab.state.lock().main().map(|f| f.loader.clone())
    }

    /// Waits for what an action set off: a navigation to load, requests to finish.
    /// `since`: when the action began; requests the page had open before do not count.
    async fn settle(&self, tab: &Arc<Tab>, loader_before: Option<String>, since: Instant) {
        tokio::time::sleep(Duration::from_millis(150)).await;
        let navigating = {
            let state = tab.state.lock();
            state.loading || state.main().map(|f| f.loader.clone()) != loader_before
        };
        if navigating {
            self.wait_for(tab, self.options.navigation_timeout, |state| !state.loading)
                .await;
        }
        self.wait_for(tab, Duration::from_millis(1500), |state| state.requests_done_since(since))
            .await;
        // A request can start a navigation after the first loading check. Its document
        // must finish even when it outlasts the shorter request-settling window.
        self.wait_for(tab, self.options.navigation_timeout, |state| !state.loading)
            .await;
    }

    /// Waits until `done` holds for the tab's state, or the time is up, or the tab closed.
    async fn wait_for(&self, tab: &Tab, timeout: Duration, done: impl Fn(&crate::router::TabState) -> bool) -> bool {
        let deadline = Instant::now() + timeout;
        loop {
            let changed = tab.changed.notified();
            {
                let state = tab.state.lock();
                if done(&state) {
                    return true;
                }
                if state.closed.is_some() {
                    return false;
                }
            }
            let left = deadline.saturating_duration_since(Instant::now());
            if left.is_zero() {
                return false;
            }
            // Notifications can be missed between the check and the wait; poll as well.
            let _ = tokio::time::timeout(left.min(Duration::from_millis(100)), changed).await;
        }
    }

    // ---- Snapshots ----

    pub async fn snapshot(&self, request: &SnapshotRequest) -> Result<Report, BrowserError> {
        let requested_tab = self.resolve_tab(request.tab.as_deref())?;
        let (tab, frame_override) = match &request.frame {
            Some(reference) => {
                let (iframe_tab, frame_id, frame) = self.iframe_content(reference).await?;
                if request.tab.is_some() && iframe_tab.id != requested_tab.id {
                    return Err(BrowserError::failed(format!(
                        "{reference} is in tab {}, not tab {}",
                        iframe_tab.id, requested_tab.id
                    )));
                }
                (iframe_tab, Some((frame_id, frame)))
            }
            None => (requested_tab.clone(), None),
        };
        let _busy = tab.busy.lock().await;
        if self.sensitive_active(&tab).is_some() || self.sensitive_active(&requested_tab).is_some() {
            return Err(BrowserError::failed(
                "Page observation is blocked during a sensitive sign-in step until the page navigates away or the step is cancelled.",
            ));
        }
        let (frame_id, frame) = match frame_override {
            Some(found) => found,
            None => self.main_frame(&tab)?,
        };
        let origin = url_origin(&frame.url);
        let body = match request.mode {
            SnapshotMode::Text => self.text(&tab, &frame_id, &frame).await?,
            SnapshotMode::Tree => {
                let root = match &request.root {
                    Some(reference) => {
                        let element = self.element(reference).await?;
                        if element.page.frame != frame_id {
                            return Err(BrowserError::failed(format!(
                                "{reference} is not in this frame; pass frame to look inside an iframe"
                            )));
                        }
                        Some(element.backend)
                    }
                    None => None,
                };
                let lines = self.tree(&tab, &frame_id, &frame, root, 0).await?;
                if root.is_none() {
                    self.last
                        .lock()
                        .insert((tab.id.clone(), frame_id.clone()), (frame.loader.clone(), lines.clone()));
                }
                let lines = match &request.query {
                    Some(query) => {
                        let found = snapshot::filter(&lines, query);
                        if found.is_empty() {
                            vec![format!("(nothing matches \"{query}\")")]
                        } else {
                            found
                        }
                    }
                    None => lines,
                };
                cut(&lines.join("\n"), SNAPSHOT_CHARS, "Use query, ref or mode \"text\" to read less at a time.")
            }
        };
        self.refresh(&tab).await;
        drop(_busy);
        let mut header = self.header(&tab);
        if request.frame.is_some() {
            header.push_str(&format!("\nFrame: {} (from {origin})", frame.url));
        }
        let text = format!("{header}\n{}", snapshot::wrap_page_content(&origin, &body));
        Ok(self.finish(&tab, text))
    }

    fn main_frame(&self, tab: &Tab) -> Result<(String, Frame), BrowserError> {
        let state = tab.state.lock();
        let id = state
            .main_frame
            .clone()
            .ok_or_else(|| BrowserError::failed("the tab has no page yet"))?;
        let frame = state
            .frames
            .get(&id)
            .cloned()
            .ok_or_else(|| BrowserError::failed("the tab has no page yet"))?;
        Ok((id, frame))
    }

    /// The frame inside the iframe that `reference` names, and the tab that owns it.
    async fn iframe_content(&self, reference: &str) -> Result<(Arc<Tab>, String, Frame), BrowserError> {
        let element = self.element(reference).await?;
        let tab = self.tab(&element.page.tab)?;
        let session = tab
            .state
            .lock()
            .frames
            .get(&element.page.frame)
            .map(|f| f.session.clone())
            .ok_or_else(|| BrowserError::StaleRef("The frame is gone. Take a new snapshot.".into()))?;
        let node = self
            .conn()
            .send(Some(&session), "DOM.describeNode", json!({ "backendNodeId": element.backend }))
            .await?;
        let child = node["node"]
            .get("frameId")
            .and_then(Value::as_str)
            .ok_or_else(|| BrowserError::failed(format!("{reference} is not an iframe")))?
            .to_string();
        let found = self
            .wait_for(&tab, Duration::from_secs(2), |state| state.frames.contains_key(&child))
            .await;
        let frame = tab.state.lock().frames.get(&child).cloned();
        match (found, frame) {
            (true, Some(frame)) => Ok((tab, child, frame)),
            _ => Err(BrowserError::failed(format!("the iframe {reference} has no document yet"))),
        }
    }

    /// The lines of a frame's accessibility tree, refs given out.
    async fn tree(
        &self,
        tab: &Tab,
        frame_id: &str,
        frame: &Frame,
        root: Option<i64>,
        nesting: usize,
    ) -> Result<Vec<String>, BrowserError> {
        let s = Some(frame.session.as_str());
        // One DOM snapshot covers every document in the frame's process, so the same-process
        // iframes expanded below use it too.
        let (tree, dom) = futures::join!(
            self.conn().send(s, "Accessibility.getFullAXTree", json!({ "frameId": frame_id })),
            self.conn().send(s, "DOMSnapshot.captureSnapshot", json!({ "computedStyles": snapshot::COMPUTED_STYLES })),
        );
        let tree = tree?;
        let attrs = dom.map(|d| snapshot::attrs_from_dom_snapshot(&d)).unwrap_or_default();
        self.tree_lines(tab, frame_id, frame, &tree, &attrs, root, nesting).await
    }

    #[allow(clippy::too_many_arguments)]
    async fn tree_lines(
        &self,
        tab: &Tab,
        frame_id: &str,
        frame: &Frame,
        tree: &Value,
        attrs: &HashMap<i64, snapshot::Attrs>,
        root: Option<i64>,
        nesting: usize,
    ) -> Result<Vec<String>, BrowserError> {
        self.prune(&tab.id, frame_id, &frame.loader);
        let nodes = tree["nodes"].as_array().map(Vec::as_slice).unwrap_or_default();
        let redacted = self.sensitive_active(tab).unwrap_or_default();
        let items = snapshot::build_with_redacted(nodes, attrs, root, &redacted);
        // The frames behind the iframes, looked up all at once.
        let iframes: Vec<i64> = items.iter().filter(|i| i.iframe).filter_map(|i| i.backend).collect();
        let found = futures::future::join_all(iframes.iter().map(|b| self.child_frame(tab, &frame.session, *b))).await;
        let mut children: HashMap<i64, (String, Frame)> = iframes
            .into_iter()
            .zip(found)
            .filter_map(|(backend, child)| child.map(|c| (backend, c)))
            .collect();
        let mut lines = Vec::with_capacity(items.len());
        for item in items {
            let reference = item
                .backend
                .filter(|_| item.interactive)
                .map(|backend| self.give_ref(&tab.id, frame_id, &frame.loader, backend, &item));
            if !item.iframe {
                lines.push(item.render(reference.as_deref()));
                continue;
            }
            // Same-origin iframes in the same process are shown inline; others as one line
            // whose content `frame` opens, labelled with its origin (§5.2, §7.7).
            let child = item.backend.and_then(|backend| children.remove(&backend));
            let mut item = item;
            match child {
                Some((child_id, child))
                    if child.session == frame.session
                        && url_origin(&child.url) == url_origin(&frame.url)
                        && nesting < FRAME_DEPTH =>
                {
                    lines.push(item.render(reference.as_deref()));
                    let s = Some(frame.session.as_str());
                    let tree = self
                        .conn()
                        .send(s, "Accessibility.getFullAXTree", json!({ "frameId": child_id }))
                        .await?;
                    let inner = Box::pin(self.tree_lines(tab, &child_id, &child, &tree, attrs, None, nesting + 1)).await?;
                    let indent = "  ".repeat(item.depth + 1);
                    lines.extend(inner.into_iter().map(|line| format!("{indent}{line}")));
                }
                Some((_, child)) => {
                    item.props.push(("origin".into(), url_origin(&child.url)));
                    lines.push(item.render(reference.as_deref()));
                }
                None => lines.push(item.render(reference.as_deref())),
            }
        }
        Ok(lines)
    }

    /// Forgets the refs and snapshots that can no longer resolve: those of this frame's
    /// earlier documents, and those of tabs that closed.
    fn prune(&self, tab: &str, frame: &str, loader: &str) {
        let open: Vec<String> = self.tabs().into_iter().map(|t| t.id.clone()).collect();
        let keep = |t: &str, f: &str, l: &str| open.iter().any(|o| o == t) && !(t == tab && f == frame && l != loader);
        let mut refs = self.refs.lock();
        refs.by_ref.retain(|_, e| keep(&e.tab, &e.frame, &e.loader));
        refs.by_node.retain(|(t, f, l, _), _| keep(t, f, l));
        drop(refs);
        self.last.lock().retain(|(t, f), (l, _)| keep(t, f, l));
    }

    /// A frame's tree now, kept as its last snapshot: brief results leave it for the next
    /// full one to diff against.
    async fn fresh_tree(&self, tab: &Tab, frame_id: &str, frame: &Frame) -> Vec<String> {
        let lines = self.tree(tab, frame_id, frame, None, 0).await.unwrap_or_default();
        self.last
            .lock()
            .insert((tab.id.clone(), frame_id.to_string()), (frame.loader.clone(), lines.clone()));
        lines
    }

    async fn child_frame(&self, tab: &Tab, session: &str, backend: i64) -> Option<(String, Frame)> {
        let node = self
            .conn()
            .send(Some(session), "DOM.describeNode", json!({ "backendNodeId": backend }))
            .await
            .ok()?;
        let id = node["node"].get("frameId")?.as_str()?.to_string();
        let frame = tab.state.lock().frames.get(&id).cloned()?;
        Some((id, frame))
    }

    fn give_ref(&self, tab: &str, frame: &str, loader: &str, backend: i64, item: &Item) -> String {
        let mut refs = self.refs.lock();
        let key = (tab.to_string(), frame.to_string(), loader.to_string(), backend);
        if let Some(existing) = refs.by_node.get(&key) {
            return existing.clone();
        }
        refs.next += 1;
        let reference = format!("{}e{}", self.options.prefix, refs.next);
        let label = if item.name.is_empty() {
            item.role.clone()
        } else {
            format!("{} \"{}\"", item.role, item.name)
        };
        refs.by_node.insert(key, reference.clone());
        refs.by_ref.insert(
            reference.clone(),
            RefEntry {
                tab: tab.to_string(),
                frame: frame.to_string(),
                loader: loader.to_string(),
                backend,
                label,
            },
        );
        reference
    }

    /// The main text of a frame as simple Markdown; links carry refs.
    async fn text(&self, tab: &Tab, frame_id: &str, frame: &Frame) -> Result<String, BrowserError> {
        let world = self.world(frame_id, frame).await?;
        let s = Some(frame.session.as_str());
        let result = self
            .conn()
            .send(
                s,
                "Runtime.evaluate",
                json!({ "expression": TEXT_SCRIPT, "contextId": world, "returnByValue": false }),
            )
            .await?;
        let object = result["result"]["objectId"]
            .as_str()
            .ok_or_else(|| BrowserError::failed("could not read the page text"))?
            .to_string();
        let value = self
            .conn()
            .send(
                s,
                "Runtime.callFunctionOn",
                json!({ "objectId": object, "functionDeclaration": "function(){return this.text}", "returnByValue": true }),
            )
            .await?;
        let mut text = value["result"]["value"].as_str().unwrap_or("").to_string();
        let links = self
            .conn()
            .send(
                s,
                "Runtime.callFunctionOn",
                json!({ "objectId": object, "functionDeclaration": "function(){return this.links}", "returnByValue": false }),
            )
            .await?;
        if let Some(array) = links["result"]["objectId"].as_str() {
            let props = self
                .conn()
                .send(s, "Runtime.getProperties", json!({ "objectId": array, "ownProperties": true }))
                .await?;
            for prop in props["result"].as_array().into_iter().flatten() {
                let Ok(index) = str_of(prop, "name").parse::<usize>() else { continue };
                let Some(element) = prop["value"]["objectId"].as_str() else { continue };
                let Ok(node) = self.conn().send(s, "DOM.describeNode", json!({ "objectId": element })).await else {
                    continue;
                };
                let Some(backend) = node["node"]["backendNodeId"].as_i64() else { continue };
                let item = Item {
                    depth: 0,
                    role: "link".into(),
                    name: String::new(),
                    props: Vec::new(),
                    value: None,
                    backend: Some(backend),
                    interactive: true,
                    iframe: false,
                };
                let reference = self.give_ref(&tab.id, frame_id, &frame.loader, backend, &item);
                text = text.replace(&format!("\u{1}{index}\u{1}"), &format!(" [ref={reference}]"));
            }
        }
        let _ = self.conn().send(s, "Runtime.releaseObject", json!({ "objectId": object })).await;
        // Markers of links past the limit.
        let text: String = text.split('\u{1}').enumerate().filter(|(i, _)| i % 2 == 0).map(|(_, p)| p).collect();
        Ok(cut(text.trim(), SNAPSHOT_CHARS, "Use query or ref, or browser_snapshot in tree mode, for the rest."))
    }

    async fn world(&self, frame_id: &str, frame: &Frame) -> Result<i64, BrowserError> {
        let key = (frame_id.to_string(), frame.loader.clone());
        if let Some(id) = self.worlds.lock().get(&key) {
            return Ok(*id);
        }
        let created = self
            .conn()
            .send(
                Some(&frame.session),
                "Page.createIsolatedWorld",
                json!({ "frameId": frame_id, "worldName": "her" }),
            )
            .await?;
        let id = created["executionContextId"]
            .as_i64()
            .ok_or_else(|| BrowserError::failed("the frame has no document to run in"))?;
        let mut worlds = self.worlds.lock();
        if worlds.len() > 64 {
            worlds.clear();
        }
        worlds.insert(key, id);
        Ok(id)
    }

    /// Runs `function` on an element in her's isolated world; the page cannot interfere.
    async fn call_on(&self, frame_id: &str, frame: &Frame, backend: i64, function: &str, args: Vec<Value>) -> Result<Value, BrowserError> {
        let world = self.world(frame_id, frame).await?;
        let s = Some(frame.session.as_str());
        let resolved = self
            .conn()
            .send(s, "DOM.resolveNode", json!({ "backendNodeId": backend, "executionContextId": world }))
            .await
            .map_err(|_| BrowserError::StaleRef("The element is gone from the page. Take a new snapshot.".into()))?;
        let object = str_of(&resolved["object"], "objectId").to_string();
        let arguments: Vec<Value> = args.into_iter().map(|value| json!({ "value": value })).collect();
        let result = self
            .conn()
            .send(
                s,
                "Runtime.callFunctionOn",
                json!({
                    "objectId": object,
                    "functionDeclaration": function,
                    "arguments": arguments,
                    "returnByValue": true,
                    "awaitPromise": true,
                }),
            )
            .await;
        let _ = self.conn().send(s, "Runtime.releaseObject", json!({ "objectId": object })).await;
        let result = result?;
        if let Some(details) = result.get("exceptionDetails") {
            return Err(BrowserError::failed(format!(
                "the page refused: {}",
                details["exception"]["description"].as_str().unwrap_or("an exception")
            )));
        }
        Ok(result["result"].get("value").cloned().unwrap_or(Value::Null))
    }

    // ---- Actions ----

    /// Performs `action` at `place`, after checking that the page is the approved one, and
    /// reports what changed (D-188).
    pub async fn act(&self, action: &Action, place: &Place, cancel: &CancellationToken) -> Result<Report, BrowserError> {
        let (tab, _) = self.verify(place.page())?;
        let _busy = tab.busy.lock().await;
        let (tab, frame) = self.verify(place.page())?;
        if self.sensitive_active(&tab).is_some() {
            return Err(BrowserError::failed(
                "Cannot act on this page during sensitive sign-in. Use browser_wait with user so the owner can continue.",
            ));
        }
        self.activate(&tab).await;
        let frame_id = place.page().frame.clone();
        // The state to diff against: the last snapshot of this document, else one now.
        let cached = match self.last.lock().get(&(tab.id.clone(), frame_id.clone())) {
            Some((loader, lines)) if *loader == frame.loader => Some(lines.clone()),
            _ => None,
        };
        let before = match cached {
            Some(lines) => lines,
            None => self.fresh_tree(&tab, &frame_id, &frame).await,
        };
        let main_before = self.main_loader(&tab);
        let since = Instant::now();
        let guarded = may_open_file_chooser(action);
        if guarded {
            self.hold_file_chooser(&tab, true).await;
        }
        let done = tokio::time::timeout(self.options.action_timeout, self.perform(action, place, &tab, cancel)).await;
        if guarded && !matches!(done, Ok(Ok(_))) {
            self.hold_file_chooser(&tab, false).await;
        }
        let summary = match done {
            Ok(Ok(summary)) => summary,
            Ok(Err(err)) => return Err(err),
            Err(_) => {
                return Err(BrowserError::failed(match action {
                    Action::Type(_) | Action::Fill(_) => {
                        "The action timed out after part of the input was sent; retrying may repeat it. Take a snapshot first."
                    }
                    _ => "The action timed out; it may or may not have happened. Take a snapshot to see the page.",
                }));
            }
        };
        self.settle(&tab, main_before.clone(), since).await;
        if guarded {
            self.hold_file_chooser(&tab, false).await;
        }
        drop(_busy);
        let navigated = self.main_loader(&tab) != main_before;
        let changes = if navigated {
            None
        } else {
            Some((frame_id, frame, before))
        };
        self.report(&tab, changes, &summary).await
    }

    /// Runs a script in the page's own world, on the page the call was approved for (§7.6),
    /// and reports its value and what changed. The value is page content.
    pub async fn eval(&self, page: &PageTarget, expression: &str) -> Result<Report, BrowserError> {
        let (tab, _) = self.verify(page)?;
        let _busy = tab.busy.lock().await;
        let (tab, frame) = self.verify(page)?;
        if self.sensitive_active(&tab).is_some() || self.has_active_sensitive() {
            return Err(BrowserError::failed(
                "Running scripts is blocked during a sensitive sign-in step until the page navigates away or the step is cancelled.",
            ));
        }
        let before = match self.last.lock().get(&(tab.id.clone(), page.frame.clone())) {
            Some((loader, lines)) if *loader == frame.loader => Some(lines.clone()),
            _ => None,
        };
        let before = match before {
            Some(lines) => lines,
            None => self.fresh_tree(&tab, &page.frame, &frame).await,
        };
        let main_before = self.main_loader(&tab);
        let since = Instant::now();
        let timeout = self.options.action_timeout;
        self.hold_file_chooser(&tab, true).await;
        // No contextId: the main world of the session's top frame, which is the approved one.
        let sent = self
            .conn()
            .send_with_timeout(
                Some(&frame.session),
                "Runtime.evaluate",
                json!({
                    "expression": expression,
                    "returnByValue": true,
                    "awaitPromise": true,
                    "userGesture": true,
                    "timeout": timeout.as_millis() as u64,
                }),
                timeout + Duration::from_secs(1),
            )
            .await;
        self.hold_file_chooser(&tab, false).await;
        let result = match sent {
            Ok(result) => result,
            Err(CdpError::Timeout { .. }) => {
                return Err(BrowserError::failed(
                    "The script did not finish in time; it may have changed the page. Take a snapshot.",
                ));
            }
            Err(err) => return Err(err.into()),
        };
        let origin = url_origin(&frame.url);
        if let Some(details) = result.get("exceptionDetails") {
            let text = details["exception"]["description"]
                .as_str()
                .or_else(|| details["text"].as_str())
                .unwrap_or("an exception");
            return Err(BrowserError::Failed(format!(
                "The script threw; it may have changed the page before that. Take a snapshot.\n{}",
                snapshot::wrap_page_content(&origin, &cut(text, 4_000, ""))
            )));
        }
        let value = &result["result"];
        let shown = match value.get("value") {
            Some(v) => serde_json::to_string_pretty(v).unwrap_or_default(),
            None => value["description"].as_str().unwrap_or("undefined").to_string(),
        };
        let summary = format!(
            "Ran the script. Its value:\n{}",
            snapshot::wrap_page_content(&origin, &cut(&shown, 20_000, "The value was cut."))
        );
        self.settle(&tab, main_before.clone(), since).await;
        drop(_busy);
        let changes = if self.main_loader(&tab) != main_before {
            None
        } else {
            Some((page.frame.clone(), frame, before))
        };
        self.report(&tab, changes, &summary).await
    }

    /// While the agent acts, a file chooser the page opens goes no further than this tab: on
    /// the person's screen it would take over the window. Only around the agent's own actions,
    /// as the person clicking in the same tab expects the real one. On the way out, says if the
    /// page asked for one.
    async fn hold_file_chooser(&self, tab: &Tab, on: bool) {
        if on {
            tab.state.lock().file_chooser = None;
        }
        // Waited for: it must reach the page before the click does.
        let _ = self
            .conn()
            .send(Some(tab.session.as_str()), "Page.setInterceptFileChooserDialog", json!({ "enabled": on }))
            .await;
        if !on {
            let mut state = tab.state.lock();
            if state.file_chooser.take().is_some() {
                state.note("The page opened a file chooser, which was not shown. Use upload with the ref of the file input or its button.".into());
            }
        }
    }

    async fn perform(&self, action: &Action, place: &Place, tab: &Arc<Tab>, cancel: &CancellationToken) -> Result<String, BrowserError> {
        match (action, place) {
            (Action::Scroll { dx, dy }, Place::Page(_)) => {
                let (x, y) = self.viewport_center(tab).await?;
                self.wheel(tab, x, y, *dx, *dy).await?;
                Ok(format!("Scrolled the page by ({dx}, {dy})."))
            }
            (Action::Press(keys), Place::Page(_)) => {
                self.press(tab, keys, cancel).await?;
                Ok(format!("Pressed {keys}."))
            }
            (Action::Type(text), Place::Page(page)) => {
                self.verify(page)?;
                self.type_text(tab, text, cancel).await?;
                Ok(format!("Typed {} characters.", text.chars().count()))
            }
            (_, Place::Page(_)) => Err(BrowserError::failed(format!(
                "{} needs a ref or coordinates",
                action.verb()
            ))),
            (Action::Click | Action::DoubleClick | Action::Hover, Place::Point { page, x, y }) => {
                self.verify(page)?;
                self.mouse(tab, action, *x, *y).await?;
                Ok(format!("{} at ({x}, {y}).", capitalized(action.verb())))
            }
            (Action::Scroll { dx, dy }, Place::Point { page, x, y }) => {
                self.verify(page)?;
                self.wheel(tab, *x, *y, *dx, *dy).await?;
                Ok(format!("Scrolled at ({x}, {y})."))
            }
            (_, Place::Point { .. }) => Err(BrowserError::failed(format!(
                "{} needs a ref, not coordinates",
                action.verb()
            ))),
            (action, Place::Element(element)) => self.on_element(action, element, tab, cancel).await,
        }
    }

    async fn on_element(&self, action: &Action, element: &ElementTarget, tab: &Arc<Tab>, cancel: &CancellationToken) -> Result<String, BrowserError> {
        let (_, frame) = self.verify(&element.page)?;
        let frame_id = element.page.frame.as_str();
        let label = &element.label;
        let s = Some(frame.session.as_str());
        match action {
            Action::Click | Action::DoubleClick | Action::Hover => {
                if !matches!(action, Action::Hover) {
                    self.refuse_native_popup(frame_id, &frame, element).await?;
                }
                let (x, y) = self.clickable_point(tab, element).await?;
                self.verify(&element.page)?;
                self.mouse(tab, action, x, y).await?;
                Ok(format!("{} {label}.", past(action)))
            }
            Action::Check(want) => {
                let state = self
                    .call_on(frame_id, &frame, element.backend, CHECKED_SCRIPT, vec![])
                    .await?;
                if state.as_bool() == Some(*want) {
                    return Ok(format!("{label} was already {}.", if *want { "checked" } else { "unchecked" }));
                }
                let (x, y) = self.clickable_point(tab, element).await?;
                self.verify(&element.page)?;
                self.mouse(tab, &Action::Click, x, y).await?;
                Ok(format!("{} {label}.", if *want { "Checked" } else { "Unchecked" }))
            }
            Action::Fill(text) => {
                self.conn()
                    .send(s, "DOM.scrollIntoViewIfNeeded", json!({ "backendNodeId": element.backend }))
                    .await
                    .ok();
                let _ = self.conn().send(s, "DOM.focus", json!({ "backendNodeId": element.backend })).await;
                let prepared = self
                    .call_on(frame_id, &frame, element.backend, SELECT_ALL_SCRIPT, vec![])
                    .await?;
                if let Some(problem) = prepared.as_str().filter(|p| *p != "ok") {
                    return Err(BrowserError::failed(format!("cannot fill {label}: {problem}")));
                }
                self.verify(&element.page)?;
                if text.is_empty() {
                    self.key(tab, &input::key_for("Delete").expect("a named key"), 0).await?;
                } else {
                    self.conn()
                        .send(Some(&tab.session), "Input.insertText", json!({ "text": text }))
                        .await?;
                }
                Ok(format!("Filled {label}."))
            }
            Action::Type(text) => {
                let _ = self.conn().send(s, "DOM.focus", json!({ "backendNodeId": element.backend })).await;
                self.verify(&element.page)?;
                self.type_text(tab, text, cancel).await?;
                Ok(format!("Typed {} characters into {label}.", text.chars().count()))
            }
            Action::Press(keys) => {
                let _ = self.conn().send(s, "DOM.focus", json!({ "backendNodeId": element.backend })).await;
                self.verify(&element.page)?;
                self.press(tab, keys, cancel).await?;
                Ok(format!("Pressed {keys} on {label}."))
            }
            Action::Select(values) => {
                let result = self
                    .call_on(frame_id, &frame, element.backend, SELECT_SCRIPT, vec![json!(values)])
                    .await?;
                if let Some(error) = result.get("error").and_then(Value::as_str) {
                    return Err(BrowserError::failed(format!("cannot select in {label}: {error}")));
                }
                let chosen: Vec<String> = result["selected"]
                    .as_array()
                    .map(|a| a.iter().filter_map(Value::as_str).map(String::from).collect())
                    .unwrap_or_default();
                Ok(format!("Selected {} in {label}.", chosen.join(", ")))
            }
            Action::Scroll { dx, dy } => {
                self.conn()
                    .send(s, "DOM.scrollIntoViewIfNeeded", json!({ "backendNodeId": element.backend }))
                    .await?;
                if *dx != 0.0 || *dy != 0.0 {
                    let (x, y) = self.clickable_point(tab, element).await?;
                    self.wheel(tab, x, y, *dx, *dy).await?;
                }
                Ok(format!("Scrolled {label}."))
            }
            Action::Upload(paths) => {
                let files: Vec<String> = paths.iter().map(|p| p.display().to_string()).collect();
                let is_file_input = self
                    .call_on(frame_id, &frame, element.backend, FILE_INPUT_SCRIPT, vec![])
                    .await?
                    .as_bool()
                    .unwrap_or(false);
                if is_file_input {
                    self.conn()
                        .send(s, "DOM.setFileInputFiles", json!({ "files": files, "backendNodeId": element.backend }))
                        .await?;
                } else {
                    self.upload_through_chooser(tab, element, &files).await?;
                }
                Ok(format!("Uploaded {} file(s) to {label}.", files.len()))
            }
        }
    }

    /// A real click on these opens a menu or panel of the browser's own, above every window on
    /// the person's screen, and no tool can answer it. So they are not clicked.
    async fn refuse_native_popup(&self, frame_id: &str, frame: &Frame, element: &ElementTarget) -> Result<(), BrowserError> {
        let kind = self.call_on(frame_id, frame, element.backend, NATIVE_POPUP_SCRIPT, vec![]).await?;
        let label = &element.label;
        match kind.as_str() {
            Some("select") => Err(BrowserError::failed(format!(
                "{label} is a dropdown, so it was not clicked: its menu would cover the user's screen. Use select with the option's value or label."
            ))),
            Some("color") => Err(BrowserError::failed(format!(
                "{label} is a color picker, so it was not clicked: its panel would cover the user's screen. Set its value with browser_eval."
            ))),
            _ => Ok(()),
        }
    }

    async fn upload_through_chooser(&self, tab: &Arc<Tab>, element: &ElementTarget, files: &[String]) -> Result<(), BrowserError> {
        let session = Some(tab.session.as_str());
        tab.state.lock().file_chooser = None;
        self.conn()
            .send(session, "Page.setInterceptFileChooserDialog", json!({ "enabled": true }))
            .await?;
        let result = async {
            let (x, y) = self.clickable_point(tab, element).await?;
            self.verify(&element.page)?;
            self.mouse(tab, &Action::Click, x, y).await?;
            let opened = self
                .wait_for(tab, Duration::from_secs(3), |state| state.file_chooser.is_some())
                .await;
            let chooser = tab.state.lock().file_chooser.take();
            match (opened, chooser) {
                (true, Some(backend)) => {
                    self.conn()
                        .send(session, "DOM.setFileInputFiles", json!({ "files": files, "backendNodeId": backend }))
                        .await?;
                    Ok(())
                }
                _ => Err(BrowserError::failed(format!(
                    "clicking {} opened no file chooser; use the ref of the file input",
                    element.label
                ))),
            }
        }
        .await;
        let _ = self
            .conn()
            .send(session, "Page.setInterceptFileChooserDialog", json!({ "enabled": false }))
            .await;
        result
    }

    /// The middle of the element in the tab's viewport, after scrolling it into view, if
    /// nothing covers it (D-167).
    async fn clickable_point(&self, tab: &Tab, element: &ElementTarget) -> Result<(f64, f64), BrowserError> {
        let (_, frame) = self.verify(&element.page)?;
        let s = Some(frame.session.as_str());
        let backend = element.backend;
        let _ = self
            .conn()
            .send(s, "DOM.scrollIntoViewIfNeeded", json!({ "backendNodeId": backend }))
            .await;
        // Scrolling is asynchronous and may set off a navigation.
        self.verify(&element.page)?;
        let quads = self
            .conn()
            .send(s, "DOM.getContentQuads", json!({ "backendNodeId": backend }))
            .await
            .map_err(|_| BrowserError::failed(format!("{} is not visible on the page", element.label)))?;
        let (x, y) = quads["quads"]
            .as_array()
            .into_iter()
            .flatten()
            .filter_map(quad_center)
            .next()
            .ok_or_else(|| BrowserError::failed(format!("{} is not visible on the page", element.label)))?;
        // What is on top at that point, in the element's own frame.
        let hit = self
            .conn()
            .send(
                s,
                "DOM.getNodeForLocation",
                json!({ "x": x.round() as i64, "y": y.round() as i64, "includeUserAgentShadowDOM": true, "ignorePointerEventsNone": true }),
            )
            .await;
        if let Ok(hit) = hit
            && let Some(top) = hit.get("backendNodeId").and_then(Value::as_i64)
            && top != backend
        {
            let inside = self.contains(&element.page.frame, &frame, backend, top).await?;
            if !inside {
                let what = self.describe(&frame, top).await;
                return Err(BrowserError::failed(format!(
                    "{} is covered by {what} at ({}, {}); nothing was clicked. Close or scroll past what covers it, then take a snapshot.",
                    element.label,
                    x.round(),
                    y.round()
                )));
            }
        }
        let (ox, oy) = self.frame_offset(tab, &element.page.frame).await?;
        Ok((x + ox, y + oy))
    }

    /// Where an out-of-process frame's viewport sits in the tab's: quads in such a frame are
    /// relative to its own viewport, while input goes to the tab.
    async fn frame_offset(&self, tab: &Tab, frame_id: &str) -> Result<(f64, f64), BrowserError> {
        let (mut x, mut y) = (0.0, 0.0);
        let mut current = frame_id.to_string();
        for _ in 0..8 {
            let (frame, parent) = {
                let state = tab.state.lock();
                let Some(frame) = state.frames.get(&current).cloned() else { break };
                let parent = frame.parent.as_ref().and_then(|p| state.frames.get(p).cloned().map(|f| (p.clone(), f)));
                (frame, parent)
            };
            let Some((parent_id, parent)) = parent else { break };
            if frame.session != parent.session {
                let owner = self
                    .conn()
                    .send(Some(&parent.session), "DOM.getFrameOwner", json!({ "frameId": current }))
                    .await?;
                let box_model = self
                    .conn()
                    .send(Some(&parent.session), "DOM.getBoxModel", json!({ "backendNodeId": owner["backendNodeId"] }))
                    .await?;
                if let Some(content) = box_model["model"]["content"].as_array() {
                    x += content.first().and_then(Value::as_f64).unwrap_or(0.0);
                    y += content.get(1).and_then(Value::as_f64).unwrap_or(0.0);
                }
            }
            current = parent_id;
        }
        Ok((x, y))
    }

    async fn contains(&self, frame_id: &str, frame: &Frame, outer: i64, inner: i64) -> Result<bool, BrowserError> {
        let world = self.world(frame_id, frame).await?;
        let s = Some(frame.session.as_str());
        let resolved = self
            .conn()
            .send(s, "DOM.resolveNode", json!({ "backendNodeId": inner, "executionContextId": world }))
            .await;
        let Ok(resolved) = resolved else {
            // In another frame: not ours.
            return Ok(false);
        };
        let inner_object = str_of(&resolved["object"], "objectId").to_string();
        let outer_resolved = self
            .conn()
            .send(s, "DOM.resolveNode", json!({ "backendNodeId": outer, "executionContextId": world }))
            .await?;
        let outer_object = str_of(&outer_resolved["object"], "objectId").to_string();
        let checked = self
            .conn()
            .send(
                s,
                "Runtime.callFunctionOn",
                json!({
                    "objectId": outer_object,
                    "functionDeclaration": "function(n){for(let x=n;x;x=x.parentNode||x.host){if(x===this)return true}return false}",
                    "arguments": [{ "objectId": inner_object }],
                    "returnByValue": true,
                }),
            )
            .await?;
        let _ = self.conn().send(s, "Runtime.releaseObject", json!({ "objectId": inner_object })).await;
        let _ = self.conn().send(s, "Runtime.releaseObject", json!({ "objectId": outer_object })).await;
        Ok(checked["result"]["value"].as_bool().unwrap_or(false))
    }

    /// `div#cookie-banner "Accept all cookies"`, for a covering element.
    async fn describe(&self, frame: &Frame, backend: i64) -> String {
        let node = self
            .conn()
            .send(Some(&frame.session), "DOM.describeNode", json!({ "backendNodeId": backend }))
            .await;
        let Ok(node) = node else {
            return "another element".into();
        };
        let node = &node["node"];
        let mut out = str_of(node, "localName").to_string();
        if let Some(attrs) = node["attributes"].as_array() {
            for pair in attrs.chunks(2) {
                if let [key, value] = pair {
                    match key.as_str() {
                        Some("id") => out.push_str(&format!("#{}", value.as_str().unwrap_or(""))),
                        Some("class") => {
                            for class in value.as_str().unwrap_or("").split_whitespace().take(2) {
                                out.push_str(&format!(".{class}"));
                            }
                        }
                        _ => {}
                    }
                }
            }
        }
        if out.is_empty() { "another element".into() } else { out }
    }

    async fn mouse(&self, tab: &Tab, action: &Action, x: f64, y: f64) -> Result<(), BrowserError> {
        let s = Some(tab.session.as_str());
        let event = |kind: &str, count: i64| {
            json!({ "type": kind, "x": x, "y": y, "button": if kind == "mouseMoved" { "none" } else { "left" }, "clickCount": count })
        };
        self.conn().send(s, "Input.dispatchMouseEvent", event("mouseMoved", 0)).await?;
        if matches!(action, Action::Hover) {
            return Ok(());
        }
        let clicks = if matches!(action, Action::DoubleClick) { 2 } else { 1 };
        for count in 1..=clicks {
            self.conn().send(s, "Input.dispatchMouseEvent", event("mousePressed", count)).await?;
            // Released even if the press reported an error: a button must not stay down.
            self.conn().send(s, "Input.dispatchMouseEvent", event("mouseReleased", count)).await?;
        }
        Ok(())
    }

    async fn wheel(&self, tab: &Tab, x: f64, y: f64, dx: f64, dy: f64) -> Result<(), BrowserError> {
        self.conn()
            .send(
                Some(&tab.session),
                "Input.dispatchMouseEvent",
                json!({ "type": "mouseWheel", "x": x, "y": y, "deltaX": dx, "deltaY": dy }),
            )
            .await?;
        Ok(())
    }

    async fn viewport_center(&self, tab: &Tab) -> Result<(f64, f64), BrowserError> {
        let metrics = self.conn().send(Some(&tab.session), "Page.getLayoutMetrics", json!({})).await?;
        let viewport = &metrics["cssLayoutViewport"];
        let w = viewport["clientWidth"].as_f64().unwrap_or(800.0);
        let h = viewport["clientHeight"].as_f64().unwrap_or(600.0);
        Ok((w / 2.0, h / 2.0))
    }

    async fn key(&self, tab: &Tab, key: &input::Key, modifiers: i64) -> Result<(), BrowserError> {
        let s = Some(tab.session.as_str());
        self.conn()
            .send(s, "Input.dispatchKeyEvent", input::key_event("keyDown", key, modifiers))
            .await?;
        self.conn()
            .send(s, "Input.dispatchKeyEvent", input::key_event("keyUp", key, modifiers))
            .await?;
        Ok(())
    }

    /// Key by key, for fields that listen to key events. Stops at once when cancelled, and
    /// says how much was sent (D-184).
    async fn type_text(&self, tab: &Tab, text: &str, cancel: &CancellationToken) -> Result<(), BrowserError> {
        let total = text.chars().count();
        for (sent, c) in text.chars().enumerate() {
            if cancel.is_cancelled() {
                return Err(partly_sent(sent, total));
            }
            let key = input::char_key(c);
            let result = if key.code.is_empty() {
                // Characters without a key of their own are inserted as text.
                self.conn()
                    .send(Some(&tab.session), "Input.insertText", json!({ "text": c.to_string() }))
                    .await
                    .map(|_| ())
                    .map_err(BrowserError::from)
            } else {
                self.key(tab, &key, 0).await
            };
            if let Err(err) = result {
                return Err(if sent == 0 { err } else { partly_sent(sent, total) });
            }
        }
        Ok(())
    }

    /// Presses keys separated by spaces, each a chord such as `Control+a`. Modifiers held
    /// are released whatever happens.
    async fn press(&self, tab: &Tab, keys: &str, cancel: &CancellationToken) -> Result<(), BrowserError> {
        let chords = keys
            .split_whitespace()
            .map(input::parse_chord)
            .collect::<Result<Vec<_>, _>>()
            .map_err(BrowserError::Failed)?;
        if chords.is_empty() {
            return Err(BrowserError::failed("no key to press"));
        }
        let s = Some(tab.session.as_str());
        for chord in chords {
            if cancel.is_cancelled() {
                return Err(BrowserError::failed("Cancelled before every key was pressed."));
            }
            let mut held = 0;
            let mut pressed = Vec::new();
            let mut result = Ok(());
            for modifier in &chord.modifiers {
                held |= input::modifier_bits(std::slice::from_ref(modifier));
                result = self
                    .conn()
                    .send(s, "Input.dispatchKeyEvent", input::key_event("keyDown", modifier, held))
                    .await
                    .map(|_| ());
                if result.is_err() {
                    break;
                }
                pressed.push(modifier.clone());
            }
            if result.is_ok() {
                result = self.key(tab, &chord.key, held).await.map_err(|_| CdpError::Closed);
            }
            for modifier in pressed.iter().rev() {
                held &= !input::modifier_bits(std::slice::from_ref(modifier));
                let _ = self
                    .conn()
                    .send(s, "Input.dispatchKeyEvent", input::key_event("keyUp", modifier, held))
                    .await;
            }
            result?;
        }
        Ok(())
    }

    // ---- Observing ----

    pub async fn screenshot(&self, tab: Option<&str>, full_page: bool, element: Option<&ElementTarget>) -> Result<(Vec<u8>, Report), BrowserError> {
        let tab = match element {
            Some(element) => self.tab(&element.page.tab)?,
            None => self.resolve_tab(tab)?,
        };
        let _busy = tab.busy.lock().await;
        if self.sensitive_active(&tab).is_some() {
            return Err(BrowserError::failed(
                "Screenshots are blocked during a sensitive sign-in step until the page navigates away or the step is cancelled.",
            ));
        }
        self.activate(&tab).await;
        let s = Some(tab.session.as_str());
        let mut params = json!({ "format": "png" });
        if let Some(element) = element {
            let (x, y, w, h) = self.element_box(&tab, element).await?;
            params["clip"] = json!({ "x": x, "y": y, "width": w, "height": h, "scale": 1 });
            params["captureBeyondViewport"] = json!(true);
        } else if full_page {
            let metrics = self.conn().send(s, "Page.getLayoutMetrics", json!({})).await?;
            let size = &metrics["cssContentSize"];
            let w = size["width"].as_f64().unwrap_or(800.0).min(4000.0);
            let h = size["height"].as_f64().unwrap_or(600.0).min(16000.0);
            params["clip"] = json!({ "x": 0, "y": 0, "width": w, "height": h, "scale": 1 });
            params["captureBeyondViewport"] = json!(true);
        }
        let shot = self.conn().send(s, "Page.captureScreenshot", params).await?;
        let bytes = base64::engine::general_purpose::STANDARD
            .decode(str_of(&shot, "data"))
            .map_err(|_| BrowserError::failed("the screenshot could not be read"))?;
        self.refresh(&tab).await;
        drop(_busy);
        let header = self.header(&tab);
        Ok((bytes, self.finish(&tab, header)))
    }

    async fn element_box(&self, tab: &Tab, element: &ElementTarget) -> Result<(f64, f64, f64, f64), BrowserError> {
        let (_, frame) = self.verify(&element.page)?;
        let s = Some(frame.session.as_str());
        let _ = self
            .conn()
            .send(s, "DOM.scrollIntoViewIfNeeded", json!({ "backendNodeId": element.backend }))
            .await;
        let model = self
            .conn()
            .send(s, "DOM.getBoxModel", json!({ "backendNodeId": element.backend }))
            .await
            .map_err(|_| BrowserError::failed(format!("{} is not visible", element.label)))?;
        let border: Vec<f64> = model["model"]["border"]
            .as_array()
            .map(|a| a.iter().filter_map(Value::as_f64).collect())
            .unwrap_or_default();
        if border.len() < 8 {
            return Err(BrowserError::failed(format!("{} is not visible", element.label)));
        }
        let xs = [border[0], border[2], border[4], border[6]];
        let ys = [border[1], border[3], border[5], border[7]];
        let (x0, x1) = (xs.iter().cloned().fold(f64::MAX, f64::min), xs.iter().cloned().fold(f64::MIN, f64::max));
        let (y0, y1) = (ys.iter().cloned().fold(f64::MAX, f64::min), ys.iter().cloned().fold(f64::MIN, f64::max));
        let (ox, oy) = self.frame_offset(tab, &element.page.frame).await?;
        // The clip is in page coordinates: add the scroll position.
        let metrics = self.conn().send(Some(&tab.session), "Page.getLayoutMetrics", json!({})).await?;
        let sx = metrics["cssVisualViewport"]["pageX"].as_f64().unwrap_or(0.0);
        let sy = metrics["cssVisualViewport"]["pageY"].as_f64().unwrap_or(0.0);
        Ok((x0 + ox + sx, y0 + oy + sy, (x1 - x0).max(1.0), (y1 - y0).max(1.0)))
    }

    /// Waits for text to appear or go, the URL to contain something, the network to be
    /// quiet, or a fixed time.
    pub async fn wait(&self, tab: Option<&str>, condition: &WaitFor, timeout: Duration) -> Result<Report, BrowserError> {
        let tab = self.resolve_tab(tab)?;
        let deadline = Instant::now() + timeout;
        let met = loop {
            if matches!(condition, WaitFor::Text(_) | WaitFor::TextGone(_) | WaitFor::Url(_))
                && self.sensitive_active(&tab).is_some()
            {
                return Err(BrowserError::failed("Reading the page is blocked during sensitive sign-in."));
            }
            let met = match condition {
                WaitFor::Time => false,
                WaitFor::Url(part) => tab.state.lock().url.contains(part.as_str()),
                WaitFor::NetworkIdle => {
                    let state = tab.state.lock();
                    !state.loading && state.network_idle(NETWORK_QUIET)
                }
                WaitFor::Text(text) | WaitFor::TextGone(text) => {
                    let found = self.page_has_text(&tab, text).await.unwrap_or(false);
                    found == matches!(condition, WaitFor::Text(_))
                }
            };
            if met {
                break true;
            }
            if tab.connection_dropped() {
                return Err(CdpError::Closed.into());
            }
            if tab.is_closed() {
                return Err(BrowserError::StaleRef("The tab closed while waiting.".into()));
            }
            let left = deadline.saturating_duration_since(Instant::now());
            if left.is_zero() {
                break false;
            }
            tokio::time::sleep(left.min(Duration::from_millis(200))).await;
        };
        self.refresh(&tab).await;
        let header = self.header(&tab);
        let what = match condition {
            WaitFor::Time => format!("Waited {:.1} s.", timeout.as_secs_f64()),
            _ if met => "The condition was met.".to_string(),
            _ => format!("The condition was not met within {:.1} s.", timeout.as_secs_f64()),
        };
        Ok(self.finish(&tab, format!("{what}\n{header}")))
    }

    async fn page_has_text(&self, tab: &Tab, text: &str) -> Result<bool, BrowserError> {
        let (frame_id, frame) = self.main_frame(tab)?;
        let world = self.world(&frame_id, &frame).await?;
        let result = self
            .conn()
            .send(
                Some(&frame.session),
                "Runtime.callFunctionOn",
                json!({
                    "executionContextId": world,
                    "functionDeclaration": "function(t){return !!document.body && document.body.innerText.includes(t)}",
                    "arguments": [{ "value": text }],
                    "returnByValue": true,
                }),
            )
            .await?;
        Ok(result["result"]["value"].as_bool().unwrap_or(false))
    }

    /// Console entries since the last read, optionally only `level` and worse.
    pub fn console(&self, tab: Option<&str>, min_level: Option<&str>, from_start: bool) -> Result<Report, BrowserError> {
        let tab = self.resolve_tab(tab)?;
        if self.sensitive_active(&tab).is_some() {
            return Err(BrowserError::failed(
                "Reading the browser console is blocked during a sensitive sign-in step until the page navigates away or the step is cancelled.",
            ));
        }
        let rank = |level: &str| match level {
            "error" => 3,
            "warning" => 2,
            "info" => 1,
            _ => 0,
        };
        let min = min_level.map(rank).unwrap_or(0);
        let (entries, origin) = {
            let mut state = tab.state.lock();
            let from = if from_start { 0 } else { state.console_read };
            let entries: Vec<String> = state
                .console
                .iter()
                .filter(|e| e.seq > from && rank(&e.level) >= min)
                .map(|e| format!("[{}] {}", e.level, e.text))
                .collect();
            state.console_read = state.console_seq;
            state.errors_reported = state.console_seq;
            (entries, url_origin(&state.url))
        };
        let shown = entries.len().min(200);
        let skipped = entries.len() - shown;
        let mut body = entries[skipped..].join("\n");
        if skipped > 0 {
            body = format!("({skipped} earlier messages left out)\n{body}");
        }
        let header = self.header(&tab);
        let text = if entries.is_empty() {
            format!("{header}\nNo new console messages.")
        } else {
            format!("{header}\n{}", snapshot::wrap_page_content(&origin, &body))
        };
        Ok(self.finish(&tab, text))
    }

    // ---- Results ----

    /// The title and URL as the browser has them now; the events that carry them may lag.
    async fn refresh(&self, tab: &Tab) {
        if let Ok(info) = self
            .conn()
            .send(None, "Target.getTargetInfo", json!({ "targetId": tab.target_id }))
            .await
        {
            let info = &info["targetInfo"];
            let mut state = tab.state.lock();
            state.title = str_of(info, "title").to_string();
            let url = str_of(info, "url");
            if !url.is_empty() {
                state.url = url.to_string();
            }
        }
    }

    /// `Page: <title> — <url> (tab 1 of 2)`.
    pub fn header(&self, tab: &Tab) -> String {
        let (url, title) = self.page_metadata(tab);
        let count = self.tabs().len();
        let title = snapshot::clean(&title);
        let page = if title.is_empty() { url } else { format!("\"{}\" {url}", title.replace('"', "'")) };
        format!("Page: {page} (tab {} of {count})", tab.id)
    }

    pub fn page_metadata(&self, tab: &Tab) -> (String, String) {
        let sensitive = self.sensitive_active(tab).is_some();
        let state = tab.state.lock();
        if sensitive {
            return (url_origin(&state.url), String::new());
        }
        (state.url.clone(), state.title.clone())
    }

    /// The result of a call that may have changed the page: what was done, where things
    /// stand, what happened on the side, and the snapshot's changes.
    async fn report(&self, tab: &Arc<Tab>, same_document: Option<(String, Frame, Vec<String>)>, done: &str) -> Result<Report, BrowserError> {
        if tab.connection_dropped() {
            return Err(CdpError::Closed.into());
        }
        if tab.is_closed() {
            let why = tab.state.lock().closed.clone().unwrap_or_default();
            return Ok(Report {
                text: format!("{done}\nThe tab closed ({why})."),
                url: String::new(),
                title: String::new(),
                tab: tab.id.clone(),
            });
        }
        let _busy = tab.busy.lock().await;
        // Brief results leave the previous snapshot for the next full result to diff against.
        let body = if is_brief() {
            String::new()
        } else if same_document.is_some() {
            let before = self.main_loader(tab);
            let body = self.report_page(tab, same_document).await;
            // The renderer can commit during the snapshot, before its navigation events have
            // reached the router. Check the current loader before returning a same-document diff.
            let document = self.conn().send(Some(&tab.session), "Page.getFrameTree", json!({})).await;
            let committed = document.as_ref().ok().and_then(|tree| tree["frameTree"]["frame"]["loaderId"].as_str());
            if committed.is_some_and(|current| Some(current) != before.as_deref()) {
                self.wait_for(tab, self.options.navigation_timeout, |state| {
                    !state.loading && state.main().is_some_and(|frame| Some(frame.loader.as_str()) != before.as_deref())
                })
                .await;
                self.report_page(tab, None).await
            } else {
                body
            }
        } else {
            self.report_page(tab, None).await
        };
        self.refresh(tab).await;
        let text = format!("{done}\n{}{body}", self.header(tab));
        Ok(self.finish(tab, text))
    }

    async fn report_page(&self, tab: &Tab, same_document: Option<(String, Frame, Vec<String>)>) -> String {
        if self.sensitive_active(tab).is_some() {
            return "\nPage observation remains blocked while sensitive sign-in fields stay on this document.".into();
        }
        if let Some((frame_id, frame, before)) = same_document {
            let still = tab.state.lock().frames.get(&frame_id).is_some_and(|f| f.loader == frame.loader);
            if still {
                let after = self.tree(tab, &frame_id, &frame, None, 0).await.unwrap_or_default();
                let changes = snapshot::diff(&before, &after, DIFF_LINES);
                self.last
                    .lock()
                    .insert((tab.id.clone(), frame_id), (frame.loader.clone(), after));
                return match changes {
                    Some(changes) => format!("\nChanges:\n{}", snapshot::wrap_page_content(&url_origin(&frame.url), &changes)),
                    None => "\nNo visible change.".into(),
                };
            }
            if frame.parent.is_some() {
                return "\nThe frame navigated; take a snapshot to see it.".into();
            }
        }
        // A new document: the start of its snapshot.
        let Ok((frame_id, frame)) = self.main_frame(tab) else { return String::new() };
        let lines = self.tree(tab, &frame_id, &frame, None, 0).await.unwrap_or_default();
        self.last
            .lock()
            .insert((tab.id.clone(), frame_id), (frame.loader.clone(), lines.clone()));
        let body = cut(
            &lines.join("\n"),
            NEW_PAGE_CHARS,
            "Use browser_snapshot with query or mode \"text\" to read further.",
        );
        format!("\nSnapshot:\n{}", snapshot::wrap_page_content(&url_origin(&frame.url), &body))
    }

    /// Adds what happened on the side, then the page's details.
    fn finish(&self, tab: &Tab, mut text: String) -> Report {
        let (url, title) = self.page_metadata(tab);
        let (notes, errors) = if self.sensitive_active(tab).is_some() {
            (Vec::new(), 0)
        } else {
            let mut state = tab.state.lock();
            (std::mem::take(&mut state.notes), state.take_new_errors())
        };
        let mut side = Vec::new();
        if let Some(note) = self.pending_note.lock().take() {
            side.push(note);
        }
        // Notes quote the page (dialog text, URLs): they are page content too.
        if !notes.is_empty() {
            side.push(snapshot::wrap_page_content(&url_origin(&url), &notes.join("\n")));
        }
        if errors > 0 {
            side.push(format!(
                "{errors} new console error{} on the page; read {} with browser_console.",
                if errors == 1 { "" } else { "s" },
                if errors == 1 { "it" } else { "them" }
            ));
        }
        if !side.is_empty() {
            text.push_str("\n\n");
            text.push_str(&side.join("\n"));
        }
        Report {
            text,
            url,
            title,
            tab: tab.id.clone(),
        }
    }
}

impl Drop for BrowserSession {
    fn drop(&mut self) {
        // Closes this session's tabs; the lease on the browser goes with `self.browser`.
        self.browser.router.remove_owner(self.owner);
    }
}

/// What `browser_wait` waits for.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum WaitFor {
    Text(String),
    TextGone(String),
    Url(String),
    NetworkIdle,
    Time,
}

fn target_of(tab: &str, frame_id: &str, frame: &Frame) -> PageTarget {
    PageTarget {
        tab: tab.to_string(),
        frame: frame_id.to_string(),
        loader: frame.loader.clone(),
        origin: url_origin(&frame.url),
    }
}

/// Whether `raw` is a verified HTTPS URL (or loopback HTTP for local test fixtures).
pub fn is_allowed_login_url(raw: &str) -> bool {
    let Ok(parsed) = Url::parse(raw) else {
        return false;
    };
    let loopback = match parsed.host() {
        Some(url::Host::Ipv4(ip)) => ip.is_loopback(),
        Some(url::Host::Ipv6(ip)) => ip.is_loopback(),
        Some(url::Host::Domain(name)) => name.eq_ignore_ascii_case("localhost"),
        None => false,
    };
    (parsed.scheme() == "https" && parsed.host_str().is_some()) || (parsed.scheme() == "http" && loopback)
}

/// The policy's origin of a frame's URL: `null` for `about:blank`.
pub fn url_origin(url: &str) -> String {
    Url::parse(url).map(|u| origin_of(&u)).unwrap_or_else(|_| "null".into())
}

fn quad_center(quad: &Value) -> Option<(f64, f64)> {
    let p: Vec<f64> = quad.as_array()?.iter().filter_map(Value::as_f64).collect();
    if p.len() < 8 {
        return None;
    }
    let area = ((p[0] * p[3] - p[2] * p[1]) + (p[2] * p[5] - p[4] * p[3]) + (p[4] * p[7] - p[6] * p[5]) + (p[6] * p[1] - p[0] * p[7])).abs() / 2.0;
    if area < 1.0 {
        return None;
    }
    Some(((p[0] + p[2] + p[4] + p[6]) / 4.0, (p[1] + p[3] + p[5] + p[7]) / 4.0))
}

fn cut(text: &str, max: usize, hint: &str) -> String {
    if text.len() <= max {
        return text.to_string();
    }
    let mut end = max;
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    let end = text[..end].rfind('\n').unwrap_or(end);
    format!("{}\n… (cut at {} of {} characters. {hint})", &text[..end], end, text.len())
}

fn partly_sent(sent: usize, total: usize) -> BrowserError {
    BrowserError::failed(format!(
        "Stopped after {sent} of {total} characters were typed; retrying may repeat input. Take a snapshot first."
    ))
}

fn capitalized(text: &str) -> String {
    let mut chars = text.chars();
    match chars.next() {
        Some(first) => first.to_uppercase().chain(chars).collect(),
        None => String::new(),
    }
}

fn past(action: &Action) -> &'static str {
    match action {
        Action::Click => "Clicked",
        Action::DoubleClick => "Double-clicked",
        Action::Hover => "Hovered over",
        _ => "Acted on",
    }
}

/// What can make a page open a file chooser: a click, or Enter or Space on a focused control.
fn may_open_file_chooser(action: &Action) -> bool {
    matches!(action, Action::Click | Action::DoubleClick | Action::Check(_) | Action::Press(_))
}

const CHECKED_SCRIPT: &str = "function(){ if ('checked' in this) return this.checked; const a = this.getAttribute('aria-checked'); return a === null ? null : a === 'true'; }";

const SELECT_ALL_SCRIPT: &str = r#"function(){
  if (this.isContentEditable) {
    this.focus();
    const range = document.createRange(); range.selectNodeContents(this);
    const sel = getSelection(); sel.removeAllRanges(); sel.addRange(range);
    return 'ok';
  }
  if (!('value' in this) || this.tagName === 'SELECT' || this.tagName === 'BUTTON') return 'it is not a text field';
  if (this.disabled) return 'it is disabled';
  if (this.readOnly) return 'it is read-only';
  this.focus();
  try { this.select(); } catch (e) { this.value = ''; }
  return 'ok';
}"#;

const FILL_LOGIN_INPUT_SCRIPT: &str = r#"function(origin, expectedUrl, type, value) {
  if (!this.isConnected || this.ownerDocument !== document || location.origin !== origin ||
      location.href !== expectedUrl ||
      this.tagName !== 'INPUT' || (this.getAttribute('type') || 'text').toLowerCase() !== type ||
      this.disabled || this.readOnly) return false;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(this, value);
  this.dispatchEvent(new InputEvent('input', {bubbles: true, composed: true}));
  return true;
}"#;

const SELECT_SCRIPT: &str = r#"function(values){
  if (this.tagName !== 'SELECT') return {error: 'it is not a <select>; click the option instead'};
  const options = [...this.options];
  const chosen = [];
  for (const v of values) {
    const o = options.find(o => o.value === v) || options.find(o => o.label.trim() === v) || options.find(o => o.textContent.trim() === v);
    if (!o) return {error: 'no option ' + JSON.stringify(v) + '; the options are ' + options.slice(0, 30).map(o => JSON.stringify(o.label.trim())).join(', ')};
    chosen.push(o);
  }
  if (!this.multiple && chosen.length > 1) return {error: 'only one option can be selected'};
  for (const o of options) o.selected = chosen.includes(o);
  this.dispatchEvent(new Event('input', {bubbles: true}));
  this.dispatchEvent(new Event('change', {bubbles: true}));
  return {selected: chosen.map(o => o.label.trim())};
}"#;

const NATIVE_POPUP_SCRIPT: &str = "function(){ if (this.tagName === 'SELECT' && !this.multiple && this.size <= 1) return 'select'; if (this.tagName === 'INPUT' && this.type === 'color') return 'color'; return null; }";

const FILE_INPUT_SCRIPT: &str = "function(){ return this.tagName === 'INPUT' && this.type === 'file'; }";

const CHALLENGE_CHECK_SCRIPT: &str = r#"(() => {
  const selectors = [
    '.g-recaptcha', '#recaptcha', '.h-captcha', '.cf-turnstile',
    '[data-sitekey]', '[data-hcaptcha-sitekey]', '[data-turnstile-sitekey]',
    'iframe[src*="recaptcha"]', 'iframe[src*="hcaptcha"]', 'iframe[src*="turnstile"]',
    'iframe[src*="arkoselabs"]', 'iframe[src*="challenges.cloudflare"]'
  ];
  for (const sel of selectors) {
    if (document.querySelector(sel)) return 'a CAPTCHA or anti-bot challenge';
  }
  return '';
})()"#;

const INSPECT_LOGIN_INPUT_SCRIPT: &str = r#"function(){
  if (!this || this.tagName !== 'INPUT') return {error: 'it is not an <input> element'};
  if (this.disabled) return {error: 'it is disabled'};
  if (this.readOnly) return {error: 'it is read-only'};
  const checkAction = (raw) => {
    try {
      const resolved = new URL(raw, document.baseURI);
      const loopback = resolved.hostname === 'localhost' || resolved.hostname === '127.0.0.1' || resolved.hostname === '[::1]';
      const schemeOk = resolved.protocol === 'https:' || (location.protocol === 'http:' && resolved.protocol === 'http:' && loopback);
      if (!schemeOk) return 'form action is not HTTPS';
      if (resolved.origin !== location.origin) return 'form action points to a different origin';
      return null;
    } catch (_) {
      return 'form action is invalid';
    }
  };
  if (this.form) {
    const rawAction = (this.form.getAttribute('action') || '').trim();
    const formErr = checkAction(rawAction || document.baseURI);
    if (formErr) return {error: formErr};
    const controls = [...this.form.querySelectorAll('[formaction]')];
    const formId = (this.form.getAttribute('id') || '').trim();
    if (formId) {
      for (const el of document.querySelectorAll('[form][formaction]')) {
        if (el.getAttribute('form') === formId) controls.push(el);
      }
    }
    for (const ctrl of controls) {
      const fa = (ctrl.getAttribute('formaction') || '').trim();
      const ctrlErr = checkAction(fa || document.baseURI);
      if (ctrlErr) return {error: ctrlErr};
    }
  }
  const style = getComputedStyle(this);
  if (style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') return {error: 'it is not visible'};
  const rect = this.getBoundingClientRect();
  if (rect.width <= 0 || rect.height <= 0) return {error: 'it has no visible box'};
  const type = (this.getAttribute('type') || 'text').toLowerCase();
  const blocked = new Set(['hidden','file','submit','button','reset','image','checkbox','radio','range','color','date','datetime-local','month','week','time']);
  if (blocked.has(type)) return {error: 'input type ' + type + ' is not allowed'};
  const ac = (this.getAttribute('autocomplete') || '').toLowerCase().split(/\s+/);
  const name = (this.getAttribute('name') || '').toLowerCase();
  const id = (this.getAttribute('id') || '').toLowerCase();
  const aria = (this.getAttribute('aria-label') || '').trim();
  const placeholder = (this.getAttribute('placeholder') || '').trim();
  let labelText = '';
  if (this.labels && this.labels.length > 0) {
    labelText = [...this.labels].map(l => (l.textContent || '').trim()).filter(Boolean).join(' ');
  }
  const label = aria || labelText || placeholder || '';
  const hint = (name + ' ' + id + ' ' + label).toLowerCase();
  let kind = null;
  if (type === 'password' || ac.includes('current-password') || ac.includes('new-password')) {
    kind = 'password';
  } else if (ac.includes('one-time-code') || /\b(otp|totp|2fa|mfa|passcode|verification.?code|auth.?code|one.?time)\b/.test(hint)) {
    kind = 'one_time_code';
  } else if (type === 'email' || ac.includes('email')) {
    kind = 'email';
  } else if ((type === 'text' || type === 'tel') && (ac.includes('username') || ac.includes('tel') || /\b(user|username|login|account|email|phone|mobile|identifier)\b/.test(hint) || (this.form && this.form.querySelector('input[type="password"]')))) {
    kind = 'username';
  }
  if (!kind) return {error: 'field is not a recognized sign-in input'};
  return {tag: 'INPUT', type, kind, label};
}"#;

/// The main text of the document as Markdown. Links become `[text](url)` followed by a marker
/// `\x01<index>\x01` that the session turns into the link's ref.
const TEXT_SCRIPT: &str = r#"(() => {
  const SKIP = new Set(['SCRIPT','STYLE','NOSCRIPT','TEMPLATE','SVG','CANVAS','IFRAME','NAV','FOOTER','HEADER','ASIDE','FORM','BUTTON','SELECT','TEXTAREA','INPUT']);
  const visible = el => { const s = getComputedStyle(el); return s.display !== 'none' && s.visibility !== 'hidden'; };
  const sensitive = el => el.tagName === 'INPUT' && (el.type === 'password' || el.type === 'hidden');
  let root = document.querySelector('main, article, [role=main]');
  if (!root) {
    let best = document.body, bestLen = 0;
    for (const el of document.querySelectorAll('div, section')) {
      const len = (el.innerText || '').length;
      if (len > bestLen * 1.0 && el.querySelectorAll('p').length >= 2 && len > bestLen) { best = el; bestLen = len; }
    }
    root = bestLen > 500 ? best : document.body;
  }
  const links = [];
  const out = [];
  const inline = node => {
    if (node.nodeType === 3) return node.textContent.replace(/\s+/g, ' ');
    if (node.nodeType !== 1 || !visible(node) || sensitive(node)) return '';
    if (SKIP.has(node.tagName) && node.tagName !== 'BUTTON') return '';
    const inner = [...node.childNodes].map(inline).join('');
    if (node.tagName === 'A' && node.href && inner.trim()) {
      if (links.length >= 300) return '[' + inner.trim() + '](' + node.href + ')';
      links.push(node);
      return '[' + inner.trim() + '](' + node.href + ')\u0001' + (links.length - 1) + '\u0001';
    }
    if (node.tagName === 'STRONG' || node.tagName === 'B') return inner.trim() ? '**' + inner.trim() + '** ' : '';
    if (node.tagName === 'CODE') return '`' + inner + '`';
    if (node.tagName === 'BR') return '\n';
    return inner;
  };
  const block = (node, depth) => {
    if (node.nodeType === 3) { const t = node.textContent.trim(); if (t) out.push(t); return; }
    if (node.nodeType !== 1 || !visible(node) || SKIP.has(node.tagName) && node !== root) return;
    const tag = node.tagName;
    if (/^H[1-6]$/.test(tag)) { out.push('\n' + '#'.repeat(+tag[1]) + ' ' + inline(node).trim()); return; }
    if (tag === 'P' || tag === 'BLOCKQUOTE' || tag === 'FIGCAPTION' || tag === 'DT' || tag === 'DD') { const t = inline(node).trim(); if (t) out.push('\n' + (tag === 'BLOCKQUOTE' ? '> ' : '') + t); return; }
    if (tag === 'PRE') { out.push('\n```\n' + node.innerText + '\n```'); return; }
    if (tag === 'LI') { out.push('  '.repeat(depth) + '- ' + inline(node).trim()); return; }
    if (tag === 'UL' || tag === 'OL') { out.push(''); for (const c of node.children) block(c, depth + (node.parentElement && node.parentElement.closest('li') ? 1 : 0)); return; }
    if (tag === 'TABLE') {
      out.push('');
      [...node.rows].slice(0, 100).forEach((row, i) => {
        out.push('| ' + [...row.cells].map(c => inline(c).trim().replace(/\|/g, '\\|')).join(' | ') + ' |');
        if (i === 0) out.push('|' + [...row.cells].map(() => ' --- ').join('|') + '|');
      });
      return;
    }
    if (tag === 'A') { const t = inline(node).trim(); if (t) out.push(t); return; }
    const blockChildren = [...node.childNodes].some(c => c.nodeType === 1 && /^(DIV|P|H[1-6]|UL|OL|TABLE|PRE|SECTION|ARTICLE|BLOCKQUOTE|LI|DL|FIGURE|MAIN)$/.test(c.tagName));
    if (!blockChildren) { const t = inline(node).trim(); if (t) out.push(t); return; }
    for (const c of node.childNodes) block(c, depth);
  };
  block(root, 0);
  const text = (document.title ? '# ' + document.title + '\n' : '') + out.join('\n').replace(/\n{3,}/g, '\n\n');
  return { text, links };
})()"#;

#[cfg(test)]
mod tests {
    use super::is_allowed_login_url;

    #[test]
    fn allowed_login_urls_require_https_or_loopback_fixture() {
        assert!(is_allowed_login_url("https://example.com/login"));
        assert!(is_allowed_login_url("https://accounts.example.org:8443/signin"));
        assert!(is_allowed_login_url("http://127.0.0.1:41234/login"));
        assert!(is_allowed_login_url("http://localhost:8080/login"));
        assert!(is_allowed_login_url("http://[::1]:8080/login"));

        assert!(!is_allowed_login_url("http://example.com/login"));
        assert!(!is_allowed_login_url("http://192.168.1.50/login"));
        assert!(!is_allowed_login_url("file:///tmp/login.html"));
        assert!(!is_allowed_login_url("chrome://settings"));
        assert!(!is_allowed_login_url("chrome-extension://abc/popup.html"));
        assert!(!is_allowed_login_url("about:blank"));
        assert!(!is_allowed_login_url("javascript:alert(1)"));
        assert!(!is_allowed_login_url("data:text/html,<input type=password>"));
        assert!(!is_allowed_login_url("not a url"));
    }
}
