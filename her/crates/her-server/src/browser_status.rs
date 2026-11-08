//! Which browsers are connected, for the clients that show it (the desktop app's settings):
//! `browser/status` answers and subscribes; `browser/changed` follows as hosts come and go.
//! `browser/site_icon` gives a site's icon for the lists of sites (browser-design §10.1).

use her_protocol::Op;
use her_protocol::api::BrowserAssignTabParams;
use her_protocol::api::BrowserCloseTabsParams;
use her_protocol::api::BrowserControl;
use her_protocol::api::BrowserRequest;
use her_protocol::api::BrowserRevealParams;
use her_protocol::api::BrowserSiteIconParams;
use her_protocol::api::BrowserSiteIconResult;
use her_protocol::api::BrowserStatus;
use her_protocol::api::ExtensionMessage;
use her_protocol::api::ExtensionSession;
use her_protocol::api::ExtensionStatus;
use her_protocol::api::YoursTab;
use her_protocol::api::method;
use her_rpc::RpcError;
use std::collections::BTreeMap;
use std::collections::HashMap;
use std::collections::HashSet;
use std::sync::Arc;

use crate::conn::ConnHandle;
use crate::conn::ConnId;
use crate::hub::HubCmd;
use crate::server::Server;

/// An extension connected, through its host's connection.
pub(crate) struct ExtensionEntry {
    host: u64,
    conn: ConnHandle,
    status: ExtensionStatus,
    /// Which tab is whose, as the extension says.
    tabs: BTreeMap<String, Vec<YoursTab>>,
    /// What it was told last, so that nothing is sent twice.
    told: BTreeMap<String, ExtensionSession>,
    told_in_use: Option<bool>,
    /// Tabs the user offered from it, not yet given to a session.
    offered: HashSet<String>,
}

fn not_connected() -> RpcError {
    RpcError::invalid_request("the Hermesh extension is not connected")
}

impl Server {
    pub(crate) fn browser_status(&self) -> BrowserStatus {
        let pool = &self.0.browser_pool;
        let in_use = pool.extension_in_use();
        let entries = self.0.extensions.lock();
        let mut all = Vec::with_capacity(entries.len());
        let mut tabs: BTreeMap<String, Vec<YoursTab>> = BTreeMap::new();
        for entry in entries.iter() {
            all.push(ExtensionStatus {
                in_use: Some(entry.host) == in_use,
                ..entry.status.clone()
            });
            for (session, session_tabs) in &entry.tabs {
                tabs.entry(session.clone()).or_default().extend(session_tabs.iter().cloned());
            }
        }
        // One that cannot be used is shown too, with its problem, so clients can say what to update.
        let extension = all.iter().find(|s| s.in_use).or_else(|| all.iter().find(|s| s.problem.is_some())).cloned();
        BrowserStatus {
            extensions: if all.len() > 1 || extension.is_none() { all } else { Vec::new() },
            extension,
            embedded: pool.has_host(),
            tabs,
            refused: self.0.host_refused.lock().clone(),
            revoked: pool.revoked(),
            waiting: pool.waiting(),
        }
    }

    pub(crate) fn watch_browser(&self, conn: &ConnHandle) {
        self.0.browser_watchers.lock().insert(conn.id, conn.clone());
        self.0.browser_pool.set_watched(true);
    }

    pub(crate) fn forget_browser_watcher(&self, conn: ConnId) {
        let mut watchers = self.0.browser_watchers.lock();
        watchers.remove(&conn);
        self.0.browser_pool.set_watched(!watchers.is_empty());
    }

    pub(crate) fn browser_changed(&self) {
        let status = self.browser_status();
        for conn in self.0.browser_watchers.lock().values() {
            conn.send(her_rpc::notification(method::BROWSER_CHANGED, &status));
        }
    }

    /// Hears what happens in the pool and the session list, for the status and the extensions.
    pub(crate) fn follow_browser_changes(&self) {
        let weak = Arc::downgrade(&self.0);
        self.0.browser_pool.on_change(Arc::new(move || {
            if let Some(inner) = weak.upgrade() {
                Server(inner).browser_changed();
            }
        }));
        let weak = Arc::downgrade(&self.0);
        self.0.feed.observe(Arc::new(move || {
            if let Some(inner) = weak.upgrade() {
                Server(inner).tell_extensions();
            }
        }));
    }

    /// An extension host that is not the service's own program. Said once per reason: the
    /// host exits and the extension tries again, so the same words would fill the log.
    pub(crate) fn host_refused(&self, reason: &str) {
        let mut refused = self.0.host_refused.lock();
        if refused.as_deref() != Some(reason) {
            tracing::warn!(%reason, "refused an extension host");
            *refused = Some(reason.to_string());
            drop(refused);
            self.browser_changed();
        }
    }

    pub(crate) fn extension_connected(&self, host: u64, conn: ConnHandle, status: ExtensionStatus) {
        *self.0.host_refused.lock() = None;
        self.0.extensions.lock().push(ExtensionEntry {
            host,
            conn,
            status,
            tabs: BTreeMap::new(),
            told: BTreeMap::new(),
            told_in_use: None,
            offered: HashSet::new(),
        });
    }

    pub(crate) fn extension_gone(&self, host: u64) {
        self.0.extensions.lock().retain(|entry| entry.host != host);
        self.count_session_tabs();
    }

    /// How many hosts of extensions are connected.
    pub(crate) fn extension_hosts(&self) -> usize {
        self.0.extensions.lock().len()
    }

    fn count_session_tabs(&self) {
        let mut counts: HashMap<String, usize> = HashMap::new();
        for entry in self.0.extensions.lock().iter() {
            for (session, tabs) in &entry.tabs {
                *counts.entry(session.clone()).or_default() += tabs.len();
            }
        }
        self.0.browser_pool.set_session_tabs(counts);
    }

    /// Something the extension said (`browser/extension`).
    pub(crate) fn extension_message(&self, host: u64, message: ExtensionMessage) {
        match message {
            ExtensionMessage::Tabs { sessions } => {
                if let Some(entry) = self.0.extensions.lock().iter_mut().find(|e| e.host == host) {
                    entry.tabs = sessions;
                }
                self.count_session_tabs();
                self.browser_changed();
                self.tell_extensions();
            }
            ExtensionMessage::Revoked { sessions } => {
                self.0.browser_pool.revoke(&sessions);
                self.browser_changed();
            }
            ExtensionMessage::OpenSession { session_id } => self.browser_request(&BrowserRequest::OpenSession { session_id }),
            ExtensionMessage::OfferTab { target_id, url, title } => {
                if let Some(entry) = self.0.extensions.lock().iter_mut().find(|e| e.host == host) {
                    entry.offered.insert(target_id.clone());
                }
                self.browser_request(&BrowserRequest::TabOffered { target_id, url, title });
            }
            ExtensionMessage::Done { session_id } => {
                let done = self.hubs().into_iter().find_map(|hub| {
                    (*hub.shared.session.id == *session_id).then(|| hub.shared.handoff.lock().as_ref().map(crate::hub::Handoff::done)).flatten()
                });
                if let Some(op) = done {
                    self.extension_op(session_id, op);
                }
            }
            ExtensionMessage::Stop { session_id } => self.extension_op(session_id, Op::Interrupt),
            ExtensionMessage::UseThis => {
                let instance = self.0.extensions.lock().iter().find(|e| e.host == host).and_then(|e| e.status.instance.clone());
                if let Some(instance) = instance {
                    self.use_extension(&instance);
                }
            }
        }
    }

    /// An operation the user asked for from the extension.
    fn extension_op(&self, session_id: String, op: Op) {
        let server = self.clone();
        tokio::spawn(async move {
            match server.loaded(&session_id) {
                Ok(hub) => {
                    if let Err(err) = hub.send(HubCmd::FromExtension { op }).await {
                        tracing::debug!(%err, "an operation from the extension");
                    }
                }
                Err(err) => tracing::debug!(%err, "an operation from the extension"),
            }
        });
    }

    fn browser_request(&self, request: &BrowserRequest) {
        for conn in self.0.browser_watchers.lock().values() {
            conn.send(her_rpc::notification(method::BROWSER_REQUEST, request));
        }
    }

    /// Tells each extension what changed about the sessions with tabs there, and whether the
    /// agent uses it.
    pub(crate) fn tell_extensions(&self) {
        if self.0.extensions.lock().is_empty() {
            return;
        }
        let hubs = self.hubs();
        let in_use = self.0.browser_pool.extension_in_use();
        for entry in self.0.extensions.lock().iter_mut() {
            let mut sessions = entry.told.clone();
            sessions.retain(|id, _| entry.tabs.contains_key(id));
            for id in entry.tabs.keys() {
                let Some(hub) = hubs.iter().find(|h| *h.shared.session.id == **id) else {
                    continue;
                };
                let summary = hub.shared.summary.lock();
                let title = summary.title.clone().or_else(|| summary.preview.clone()).unwrap_or_default();
                sessions.insert(
                    id.clone(),
                    ExtensionSession {
                        title: title.lines().next().unwrap_or_default().to_string(),
                        state: summary.state,
                        handoff: hub.shared.handoff.lock().as_ref().map(|h| h.what.clone()),
                    },
                );
            }
            if sessions != entry.told {
                send_control(&entry.conn, &BrowserControl::Sessions { sessions: sessions.clone() });
                entry.told = sessions;
            }
            let used = Some(entry.host) == in_use;
            if entry.told_in_use != Some(used) {
                send_control(&entry.conn, &BrowserControl::InUse { in_use: used });
                entry.told_in_use = Some(used);
            }
        }
    }

    /// A session was deleted: its tabs in the user's browser are theirs now.
    pub(crate) fn forget_browser_session(&self, id: &str) {
        self.0.browser_pool.allow(id);
        for entry in self.0.extensions.lock().iter() {
            if entry.tabs.contains_key(id) {
                send_control(&entry.conn, &BrowserControl::Forget { session_id: id.to_string() });
            }
        }
    }

    pub(crate) fn reveal_tab(&self, params: BrowserRevealParams) -> Result<(), RpcError> {
        let in_use = self.0.browser_pool.extension_in_use();
        let entries = self.0.extensions.lock();
        // The extension that has the session's tabs, else the one in use.
        let entry = entries
            .iter()
            .find(|e| params.session_id.as_ref().is_some_and(|id| e.tabs.contains_key(id)))
            .or_else(|| entries.iter().find(|e| Some(e.host) == in_use))
            .ok_or_else(not_connected)?;
        send_control(&entry.conn, &BrowserControl::Reveal {
            session_id: params.session_id,
            target_id: params.target_id,
        });
        Ok(())
    }

    pub(crate) fn close_tabs(&self, params: BrowserCloseTabsParams) -> Result<(), RpcError> {
        let entries = self.0.extensions.lock();
        let entry = entries.iter().find(|e| e.tabs.contains_key(&params.session_id)).ok_or_else(not_connected)?;
        send_control(&entry.conn, &BrowserControl::CloseTabs {
            session_id: params.session_id,
            target_id: params.target_id,
        });
        Ok(())
    }

    pub(crate) fn assign_tab(&self, params: BrowserAssignTabParams) -> Result<(), RpcError> {
        let mut entries = self.0.extensions.lock();
        let entry = entries.iter_mut().find(|e| e.offered.contains(&params.target_id)).ok_or_else(not_connected)?;
        entry.offered.remove(&params.target_id);
        send_control(&entry.conn, &BrowserControl::AssignTab {
            target_id: params.target_id,
            session_id: params.session_id,
        });
        Ok(())
    }

    /// The extension the agent uses from now on.
    pub(crate) fn use_extension(&self, instance: &str) {
        self.0.browser_pool.choose_extension(instance);
        self.browser_changed();
        self.tell_extensions();
    }

    /// A site's icon, found the way a browser finds one and kept in `~/.her/browser/icons`
    /// (`icon::site`): asked of the site when it is first shown; after that the kept one is given
    /// at once, and once it is a week old it is looked for again in the background. A site with
    /// none is kept too, so it is not asked each time; when no site answers (offline), nothing
    /// is kept and the next showing asks again. Only https sites on the internet have one to
    /// look for (`localhost` and IP addresses are left out). `cached` gives only what is kept.
    pub(crate) async fn site_icon(&self, params: BrowserSiteIconParams) -> BrowserSiteIconResult {
        let origin = params.origin.trim_end_matches('/');
        // A local page (http, a file) has no site to ask.
        if !origin.starts_with("https://") {
            return BrowserSiteIconResult::default();
        }
        let dir = self.0.home.site_icons_dir();
        let stored = if params.cached { her_mcp::icon::load(&dir, origin) } else { her_mcp::icon::site(&dir, origin).await };
        BrowserSiteIconResult { icon: stored.and_then(|s| s.data_uri()) }
    }
}

fn send_control(conn: &ConnHandle, control: &BrowserControl) {
    conn.send(her_rpc::notification(method::BROWSER_CONTROL, control));
}
