//! The session list as clients follow it: connections that called `sessions/subscribe` get
//! `sessions/changed` whenever a session appears, changes state or goes away. Each change is
//! encoded once and the same bytes go to every subscriber.

use bytes::Bytes;
use her_llm::Timestamp;
use her_protocol::api::SessionSummary;
use her_protocol::api::SessionsChangedParams;
use her_protocol::api::method;
use parking_lot::Mutex;
use std::sync::Arc;

use crate::conn::ConnHandle;
use crate::conn::ConnId;
use crate::conn::Outgoing;

struct Watcher {
    conn: ConnHandle,
    /// Changes held back until the subscribe response is queued, so a client never sees a
    /// change before the list it applies to.
    pending: Option<Vec<Bytes>>,
}

#[derive(Default)]
pub struct ListFeed {
    watchers: Mutex<Vec<Watcher>>,
    /// Hears every change too (the extension's tab groups follow the sessions).
    observer: Mutex<Option<Arc<dyn Fn() + Send + Sync>>>,
}

impl ListFeed {
    pub fn observe(&self, f: Arc<dyn Fn() + Send + Sync>) {
        *self.observer.lock() = Some(f);
    }

    /// Starts collecting changes for `conn`; `start` sends them after the list.
    pub fn watch(&self, conn: &ConnHandle) {
        let mut watchers = self.watchers.lock();
        watchers.retain(|w| w.conn.id != conn.id);
        watchers.push(Watcher {
            conn: conn.clone(),
            pending: Some(Vec::new()),
        });
    }

    /// Queues the subscribe response, then the changes collected since `watch`.
    pub fn start(&self, conn: &ConnHandle, response: Vec<u8>) {
        let mut watchers = self.watchers.lock();
        conn.send(response);
        if let Some(watcher) = watchers.iter_mut().find(|w| w.conn.id == conn.id) {
            for frame in watcher.pending.take().unwrap_or_default() {
                conn.queue.push(Outgoing::Frame(frame));
            }
        }
    }

    pub fn cancel(&self, conn: ConnId) {
        self.watchers.lock().retain(|w| w.conn.id != conn);
    }

    pub fn publish(&self, sessions: Vec<SessionSummary>, deleted: Vec<String>) {
        let observer = self.observer.lock().clone();
        if let Some(observer) = observer {
            observer();
        }
        if self.watchers.lock().is_empty() {
            return;
        }
        self.send(Bytes::from(her_rpc::notification(
            method::SESSIONS_CHANGED,
            &SessionsChangedParams { sessions, deleted },
        )));
    }

    /// Sends a notification, encoded once, to every watcher. The bot list uses it too.
    pub fn send(&self, frame: Bytes) {
        let mut watchers = self.watchers.lock();
        watchers.retain(|w| !w.conn.queue.is_closed());
        for watcher in watchers.iter_mut() {
            match &mut watcher.pending {
                Some(pending) => pending.push(frame.clone()),
                None => watcher.conn.queue.push(Outgoing::Frame(frame.clone())),
            }
        }
    }
}

/// A protocol timestamp (milliseconds since the epoch) as RFC 3339, to the second.
pub fn rfc3339(ts: Timestamp) -> String {
    chrono::DateTime::from_timestamp_millis(ts)
        .unwrap_or_default()
        .to_rfc3339_opts(chrono::SecondsFormat::Secs, true)
}

/// Now, as RFC 3339 to the second.
pub fn now() -> String {
    chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Secs, true)
}
