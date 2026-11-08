//! The `docx/*` methods (word-design §4): WordStore holds each open .docx; an editor joins as a
//! Yrs replica and syncs through `docx/sync`, as with decks (crate::decks). Saving goes through
//! the desktop app's serializer (crate::docx_host).

use std::path::PathBuf;
use std::sync::Arc;

use base64::Engine as _;
use base64::engine::general_purpose::STANDARD as B64;
use her_docs::deck::DeckError;
use her_docs::word::WordEvent;
use her_protocol::api::{Empty, method};
use her_protocol::docx::{
    DocxChangedParams, DocxIdParams, DocxOpenParams, DocxOpenResult, DocxReviewAction, DocxReviewParams, DocxRevisionResult, DocxSaveParams, DocxSaveResult,
    DocxSaveStatus, DocxSyncNotice, DocxSyncParams, DocxSyncResult, DocxUndoParams,
};
use her_rpc::RpcError;

use crate::conn::{ConnHandle, ConnId};
use crate::decks::{decode, rpc_error};
use crate::server::Server;

impl Server {
    /// Routes WordStore's events: updates to every other replica, state to all.
    pub(crate) fn follow_word_changes(&self) {
        let weak = Arc::downgrade(&self.0);
        self.0.words.on_event(move |event| {
            let Some(inner) = weak.upgrade() else { return };
            let watchers = inner.docx_watchers.lock();
            match event {
                WordEvent::Update { docx_id, session, update, revision } => {
                    let Some(sessions) = watchers.get(docx_id) else { return };
                    let notice = DocxSyncNotice { docx_id: docx_id.clone(), update: B64.encode(update), revision: *revision };
                    let frame = her_rpc::notification(method::DOCX_SYNC, &notice);
                    for (s, conn) in sessions {
                        if session.as_deref() != Some(s.as_str()) {
                            conn.send(frame.clone());
                        }
                    }
                }
                WordEvent::Changed { info, author } => {
                    let Some(sessions) = watchers.get(&info.deck_id) else { return };
                    let notice = DocxChangedParams { docx_id: info.deck_id.clone(), revision: info.revision, dirty: info.dirty, author: author.clone(), conflict: info.conflict };
                    let frame = her_rpc::notification(method::DOCX_CHANGED, &notice);
                    for conn in sessions.values() {
                        conn.send(frame.clone());
                    }
                }
            }
        });
    }

    pub(crate) fn forget_docx_watcher(&self, conn: ConnId) {
        let gone = crate::decks::drop_conn(&self.0.docx_watchers, conn);
        let words = self.0.words.clone();
        tokio::spawn(async move {
            for (docx, session) in gone {
                let _ = words.close(&docx, &session).await;
            }
        });
    }

    pub(crate) async fn docx_open(&self, p: DocxOpenParams, conn: &ConnHandle) -> Result<DocxOpenResult, RpcError> {
        let scope = self.file_scope(&p.session_id).await?;
        let path: PathBuf = scope.env.resolve(&p.path);
        let refused = her_core::files::refused(&self.0.home, &path);
        if p.discard {
            self.0.words.discard_path(&path).await.map_err(rpc_error)?;
        }
        let opened = self.0.words.open(&path, &p.session_id, p.replica).await.map_err(rpc_error)?;
        let entry = opened.entry;
        self.0.docx_watchers.lock().entry(entry.deck_id.clone()).or_default().insert(p.session_id.clone(), conn.clone());
        let source = tokio::fs::read(entry.source()).await.map_err(|e| RpcError::internal(e.to_string()))?;
        let source_b64 = p.replica.then(|| B64.encode(&source));
        let font_families = tokio::task::spawn_blocking(move || her_core::pptx::docx_font_families_of(&source))
            .await
            .map_err(|e| RpcError::internal(e.to_string()))?
            .map_err(|e| RpcError::invalid_params(e.to_string()))?;
        let info = entry.info();
        Ok(DocxOpenResult {
            docx_id: info.deck_id,
            path: info.path.display().to_string(),
            source: source_b64,
            state: opened.state.map(|s| B64.encode(s)),
            hash: info.hash,
            writable: info.writable && !refused,
            dirty: info.dirty,
            revision: info.revision,
            font_families,
        })
    }

    pub(crate) async fn docx_sync(&self, p: DocxSyncParams) -> Result<DocxSyncResult, RpcError> {
        let update = p.update.as_deref().map(|u| decode("update", u)).transpose()?.unwrap_or_default();
        let sv = p.state_vector.as_deref().map(|sv| decode("state_vector", sv)).transpose()?;
        let (revision, missing) = self.0.words.sync(&p.docx_id, Some(&p.session_id), &update, sv.as_deref()).await.map_err(rpc_error)?;
        Ok(DocxSyncResult { revision, update: missing.map(|m| B64.encode(m)) })
    }

    /// `docx/save`: a conflict or a refusal is a status, not an error; no desktop app to write
    /// the bytes is an error.
    pub(crate) async fn docx_save(&self, p: DocxSaveParams) -> Result<DocxSaveResult, RpcError> {
        let entry = self.0.words.get(&p.docx_id).map_err(rpc_error)?;
        let to = match &p.path {
            Some(path) => Some(self.file_scope(&p.session_id).await?.env.resolve(path)),
            None => None,
        };
        let status = |status, hash: Option<String>, message: String| DocxSaveResult { status, hash, message: Some(message), revision: entry.info().revision };
        if her_core::files::refused(&self.0.home, to.as_deref().unwrap_or(&entry.path)) {
            return Ok(status(DocxSaveStatus::Refused, None, "her's own data is not written from here".into()));
        }
        match self.0.words.save(&p.docx_id, to.as_deref(), p.force).await {
            Ok(info) => {
                let hash = match &to {
                    Some(to) => her_core::files::hash(&tokio::fs::read(to).await.map_err(|e| RpcError::internal(e.to_string()))?),
                    None => info.hash,
                };
                Ok(DocxSaveResult { status: DocxSaveStatus::Saved, hash: Some(hash), message: None, revision: info.revision })
            }
            Err(e @ DeckError::Conflict(_)) => Ok(status(DocxSaveStatus::Conflict, Some("changed".into()), e.to_string())),
            Err(e @ DeckError::Deleted(_)) => Ok(status(DocxSaveStatus::Conflict, None, e.to_string())),
            Err(e @ DeckError::Refused(_)) => Ok(status(DocxSaveStatus::Refused, None, e.to_string())),
            Err(e) => Err(rpc_error(e)),
        }
    }

    /// `docx/undo`: takes back the agent's edit `edit`; refused with the reason when it cannot.
    pub(crate) async fn docx_undo(&self, p: DocxUndoParams) -> Result<DocxRevisionResult, RpcError> {
        let undone = match self.0.words.undo(&p.docx_id, p.edit).await {
            Err(DeckError::NotOpen(_)) => Err(DeckError::NotUndoable("the document was closed since".into())),
            other => other,
        };
        Ok(DocxRevisionResult { revision: undone.map_err(rpc_error)? })
    }

    /// `docx/review`: the person accepts or rejects tracked changes in the document.
    pub(crate) async fn docx_review(&self, p: DocxReviewParams) -> Result<DocxRevisionResult, RpcError> {
        let action = match p.action {
            DocxReviewAction::Accept => her_word::ReviewAction::Accept,
            DocxReviewAction::Reject => her_word::ReviewAction::Reject,
        };
        let revision = self.0.words.review(&p.docx_id, action, p.ids, None).await.map_err(rpc_error)?;
        Ok(DocxRevisionResult { revision })
    }

    pub(crate) async fn docx_close(&self, p: DocxIdParams) -> Result<Empty, RpcError> {
        if let Some(sessions) = self.0.docx_watchers.lock().get_mut(&p.docx_id) {
            sessions.remove(&p.session_id);
        }
        self.0.words.close(&p.docx_id, &p.session_id).await.map_err(rpc_error)?;
        Ok(Empty {})
    }
}
