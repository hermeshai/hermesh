//! The `deck/*` methods held by the kernel (presentation-kernel-authority §4–§6): DeckStore holds
//! each open file; an editor joins as a Yrs replica and syncs through `deck/sync`.

use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::Arc;

use base64::Engine as _;
use base64::engine::general_purpose::STANDARD as B64;
use her_docs::deck::{DeckError, DeckEvent, DeckInfo};
use her_protocol::api::{Empty, error_code, method};
use her_protocol::deck::{
    DECK_CONTRACT_VERSION, DeckChangedParams, DeckIdParams, DeckOpenParams, DeckOpenResult, DeckOrigin, DeckSaveParams, DeckSaveResult, DeckSummary,
    DeckSyncNotice, DeckSyncParams, DeckSyncResult, DeckUndoParams, DeckUndoResult, SlideSize, ThemeInfo,
};
use her_rpc::RpcError;

use crate::conn::{ConnHandle, ConnId};
use crate::server::Server;

/// Who opened which deck, to route notices: by deck, each session with its connection.
pub(crate) type DeckWatchers = parking_lot::Mutex<HashMap<String, HashMap<String, ConnHandle>>>;

pub(crate) fn rpc_error(e: DeckError) -> RpcError {
    match e {
        DeckError::NotFound(_) | DeckError::TooLarge(_) => RpcError::with_kind(error_code::DOC_UNAVAILABLE, "doc_unavailable", e.to_string()),
        DeckError::NotOpen(_) | DeckError::Protocol(_) => RpcError::invalid_params(e),
        DeckError::Refused(_) | DeckError::NotUndoable(_) => RpcError::with_kind(error_code::DOC_REFUSED, "doc_refused", e.to_string()),
        DeckError::Conflict(_) | DeckError::Deleted(_) => RpcError::with_kind(error_code::DOC_CONFLICT, "doc_conflict", e.to_string()),
        DeckError::Host(_) => RpcError::with_kind(error_code::DOC_UNAVAILABLE, "doc_unavailable", e.to_string()),
        DeckError::Worker(_) | DeckError::Io(_) => RpcError::internal(e.to_string()),
    }
}

pub(crate) fn decode(field: &str, data: &str) -> Result<Vec<u8>, RpcError> {
    B64.decode(data).map_err(|e| RpcError::invalid_params(format!("{field}: {e}")))
}

fn changed(info: &DeckInfo, origin: DeckOrigin) -> DeckChangedParams {
    DeckChangedParams {
        deck_id: info.deck_id.clone(),
        version: info.revision.to_string(),
        dirty: info.dirty,
        hash: Some(info.hash.clone()),
        origin,
        slide_ids: Vec::new(),
        conflict: info.conflict,
    }
}

/// Takes a closed connection out of `watchers`: the (entry, session) pairs it held.
pub(crate) fn drop_conn(watchers: &DeckWatchers, conn: ConnId) -> Vec<(String, String)> {
    let mut watchers = watchers.lock();
    let mut gone = Vec::new();
    for (deck, sessions) in watchers.iter_mut() {
        sessions.retain(|session, c| {
            let keep = c.id != conn;
            if !keep {
                gone.push((deck.clone(), session.clone()));
            }
            keep
        });
    }
    watchers.retain(|_, s| !s.is_empty());
    gone
}

impl Server {
    /// Routes DeckStore's events: updates to every other replica of the deck, state to all.
    pub(crate) fn follow_deck_changes(&self) {
        let weak = Arc::downgrade(&self.0);
        self.0.decks.on_event(move |event| {
            let Some(inner) = weak.upgrade() else { return };
            let watchers = inner.deck_watchers.lock();
            match event {
                DeckEvent::Update { deck_id, session, update, revision } => {
                    let Some(sessions) = watchers.get(deck_id) else { return };
                    let notice = DeckSyncNotice {
                        deck_id: deck_id.clone(),
                        update: B64.encode(update),
                        version: revision.to_string(),
                        origin: if session.is_some() { DeckOrigin::Human } else { DeckOrigin::System },
                        proposal_id: None,
                    };
                    let frame = her_rpc::notification(method::DECK_SYNC, &notice);
                    for (s, conn) in sessions {
                        if session.as_deref() != Some(s.as_str()) {
                            conn.send(frame.clone());
                        }
                    }
                }
                DeckEvent::Changed(info) => {
                    let Some(sessions) = watchers.get(&info.deck_id) else { return };
                    let frame = her_rpc::notification(method::DECK_CHANGED, &changed(info, DeckOrigin::Human));
                    for conn in sessions.values() {
                        conn.send(frame.clone());
                    }
                }
            }
        });
    }

    pub(crate) fn forget_deck_watcher(&self, conn: ConnId) {
        let gone = drop_conn(&self.0.deck_watchers, conn);
        let decks = self.0.decks.clone();
        tokio::spawn(async move {
            for (deck, session) in gone {
                let _ = decks.close(&deck, &session).await;
            }
        });
    }

    pub(crate) async fn deck_open(&self, p: DeckOpenParams, conn: &ConnHandle) -> Result<DeckOpenResult, RpcError> {
        let scope = self.file_scope(&p.session_id).await?;
        let path: PathBuf = scope.env.resolve(&p.path);
        let refused = her_core::files::refused(&self.0.home, &path);
        if p.discard {
            self.0.decks.discard_path(&path).await.map_err(rpc_error)?;
        }
        let opened = self.0.decks.open(&path, &p.session_id, p.replica).await.map_err(rpc_error)?;
        let entry = opened.entry;
        self.0.deck_watchers.lock().entry(entry.deck_id.clone()).or_default().insert(p.session_id.clone(), conn.clone());
        let source = tokio::fs::read(entry.source()).await.map_err(|e| RpcError::internal(e.to_string()))?;
        let source_b64 = p.replica.then(|| B64.encode(&source));
        let font_families = tokio::task::spawn_blocking(move || her_core::pptx::font_families_of(&source))
            .await
            .map_err(|e| RpcError::internal(e.to_string()))?
            .map_err(|e| RpcError::invalid_params(e.to_string()))?;
        let summary = self.0.decks.summary(&entry.deck_id).await.map_err(rpc_error)?;
        let info = entry.info();
        let ratio = match summary.width_pt / summary.height_pt {
            r if (r - 16.0 / 9.0).abs() < 0.01 => "16:9",
            r if (r - 4.0 / 3.0).abs() < 0.01 => "4:3",
            _ => "custom",
        };
        let version = info.revision.to_string();
        Ok(DeckOpenResult {
            deck_id: info.deck_id.clone(),
            path: info.path.display().to_string(),
            hash: info.hash.clone(),
            writable: info.writable && !refused,
            version: version.clone(),
            dirty: info.dirty,
            contract_version: DECK_CONTRACT_VERSION,
            engine: her_docs::deck::ENGINE.to_owned(),
            state: opened.state.map(|s| B64.encode(s)),
            source: source_b64,
            font_families,
            // Edits come from replicas only: `deck/edit` is not carried out here yet.
            supported_ops: Vec::new(),
            summary: DeckSummary {
                deck_id: info.deck_id,
                path: info.path.display().to_string(),
                version,
                slide_count: summary.slides.len() as u32,
                size: SlideSize { width_pt: summary.width_pt, height_pt: summary.height_pt, ratio: ratio.into() },
                // Layouts and theme come with the deck tools (presentation-ai-design §7).
                layouts: Vec::new(),
                theme: ThemeInfo::default(),
                sections: Vec::new(),
                slides: summary.slides,
                truncated: false,
                stale_links: None,
                pending_proposals: None,
            },
        })
    }

    pub(crate) async fn deck_sync(&self, p: DeckSyncParams) -> Result<DeckSyncResult, RpcError> {
        let update = if p.update.is_empty() { Vec::new() } else { decode("update", &p.update)? };
        let sv = p.state_vector.as_deref().map(|sv| decode("state_vector", sv)).transpose()?;
        let (revision, missing) = self.0.decks.sync(&p.deck_id, Some(&p.session_id), &update, sv.as_deref()).await.map_err(rpc_error)?;
        Ok(DeckSyncResult { version: revision.to_string(), update: missing.map(|m| B64.encode(m)) })
    }

    pub(crate) async fn deck_save(&self, p: DeckSaveParams) -> Result<DeckSaveResult, RpcError> {
        let entry = self.0.decks.get(&p.deck_id).map_err(rpc_error)?;
        let to = match &p.path {
            Some(path) => {
                let scope = self.file_scope(&p.session_id).await?;
                let to = scope.env.resolve(path);
                if her_core::files::refused(&self.0.home, &to) {
                    return Err(rpc_error(DeckError::Refused("her's own data is not written from here".into())));
                }
                Some(to)
            }
            None => {
                if her_core::files::refused(&self.0.home, &entry.path) {
                    return Err(rpc_error(DeckError::Refused("her's own data is not written from here".into())));
                }
                None
            }
        };
        let info = self.0.decks.save(&p.deck_id, to.as_deref(), p.force).await.map_err(rpc_error)?;
        let path = to.unwrap_or(info.path);
        let hash = match &p.path {
            Some(_) => her_core::files::hash(&tokio::fs::read(&path).await.map_err(|e| RpcError::internal(e.to_string()))?),
            None => info.hash,
        };
        Ok(DeckSaveResult { path: path.display().to_string(), hash, version: info.revision.to_string() })
    }

    /// `deck/undo`: takes back the agent's edit `edit` (presentation-deck-edit §5); the update
    /// goes to every replica as `deck/sync`. A deck no longer open has no history left to undo.
    pub(crate) async fn deck_undo(&self, p: DeckUndoParams) -> Result<DeckUndoResult, RpcError> {
        let undone = match self.0.decks.undo(&p.deck_id, p.edit).await {
            Err(DeckError::NotOpen(_)) => Err(DeckError::NotUndoable("the presentation was closed since".into())),
            other => other,
        };
        Ok(DeckUndoResult { revision: undone.map_err(rpc_error)? })
    }

    pub(crate) async fn deck_close(&self, p: DeckIdParams) -> Result<Empty, RpcError> {
        if let Some(sessions) = self.0.deck_watchers.lock().get_mut(&p.deck_id) {
            sessions.remove(&p.session_id);
        }
        self.0.decks.close(&p.deck_id, &p.session_id).await.map_err(rpc_error)?;
        Ok(Empty {})
    }
}
