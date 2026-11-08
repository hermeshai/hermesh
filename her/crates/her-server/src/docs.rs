//! The `doc/*` methods (document-editor-design §6): documents belong to their files and are
//! shared by every session; a session only says what its column shows.

use her_docs::DocError;
use her_protocol::api::Empty;
use her_protocol::api::error_code;
use her_protocol::doc::DocApplyParams;
use her_protocol::doc::DocApplyResult;
use her_protocol::doc::DocCellsParams;
use her_protocol::doc::DocCellsResult;
use her_protocol::doc::DocIdParams;
use her_protocol::doc::DocInfo;
use her_protocol::doc::DocSaveParams;
use her_protocol::doc::DocLocateParams;
use her_protocol::doc::DocLocateResult;
use her_protocol::doc::DocOpenParams;
use her_protocol::doc::DocOpenResult;
use her_protocol::doc::DocResolveParams;
use her_protocol::doc::DocResolveResult;
use her_protocol::doc::DocViewParams;
use her_protocol::doc::ResolvedPart;
use her_rpc::RpcError;
use std::path::PathBuf;
use std::sync::Arc;

use crate::conn::ConnHandle;
use crate::conn::ConnId;
use crate::server::Server;

fn rpc_error(e: DocError) -> RpcError {
    match e {
        DocError::Password => RpcError::with_kind(error_code::DOC_PASSWORD, "doc_password", e.to_string()),
        DocError::NotFound(_) | DocError::Unsupported(_) | DocError::TooLarge(_) | DocError::Unreadable(..) => {
            RpcError::with_kind(error_code::DOC_UNAVAILABLE, "doc_unavailable", e.to_string())
        }
        DocError::NotOpen(_) | DocError::NoPage { .. } | DocError::NotEditable(_) => RpcError::invalid_params(e),
        DocError::Refused(_) => RpcError::with_kind(error_code::DOC_REFUSED, "doc_refused", e.to_string()),
        DocError::Stale { .. } => RpcError::with_kind(error_code::DOC_STALE, "doc_stale", e.to_string()),
        DocError::Conflict(_) => RpcError::with_kind(error_code::DOC_CONFLICT, "doc_conflict", e.to_string()),
        DocError::Io(_) | DocError::Worker(_) => RpcError::internal(e.to_string()),
    }
}

impl Server {
    /// A connection that opened a document hears `doc/changed` until it closes.
    pub(crate) fn watch_docs(&self, conn: &ConnHandle) {
        self.0.doc_watchers.lock().insert(conn.id, conn.clone());
    }

    pub(crate) fn forget_doc_watcher(&self, conn: ConnId) {
        self.0.doc_watchers.lock().remove(&conn);
    }

    /// Tells the watchers of every change to an open document.
    pub(crate) fn follow_doc_changes(&self) {
        let weak = Arc::downgrade(&self.0);
        self.0.docs.on_change(move |info| {
            let Some(inner) = weak.upgrade() else { return };
            let frame = her_rpc::notification(her_protocol::api::method::DOC_CHANGED, &her_protocol::doc::DocChangedParams { info: info.clone() });
            for conn in inner.doc_watchers.lock().values() {
                conn.send(frame.clone());
            }
        });
    }
    pub(crate) async fn doc_open(&self, p: DocOpenParams) -> Result<DocOpenResult, RpcError> {
        let path = match (&p.path, &p.doc_id) {
            (Some(path), _) => PathBuf::from(path),
            (None, Some(id)) => self.0.docs.path_of(id).map_err(rpc_error)?,
            (None, None) => return Err(RpcError::invalid_params("give path or doc_id")),
        };
        if !path.is_absolute() {
            return Err(RpcError::invalid_params("path must be absolute"));
        }
        let (doc, token) = self.0.docs.open(&path, p.password, &p.session_id).await.map_err(|e| {
            // Which file wants its password: one asked for by its id is shown by its path, to ask there.
            let password = matches!(e, DocError::Password);
            let mut e = rpc_error(e);
            if password && let Some(serde_json::Value::Object(data)) = e.data.as_mut() {
                data.insert("path".into(), path.display().to_string().into());
            }
            e
        })?;
        Ok(DocOpenResult { info: doc.info(), token, password: doc.password().map(str::to_string) })
    }

    pub(crate) async fn doc_close(&self, p: DocIdParams) -> Result<Empty, RpcError> {
        self.0.docs.close(&p.doc_id, &p.session_id).await.map_err(rpc_error)?;
        Ok(Empty {})
    }

    pub(crate) fn doc_view(&self, p: DocViewParams) -> Result<Empty, RpcError> {
        self.0.docs.set_view(&p.doc_id, &p.session_id, p.page, p.selection).map_err(rpc_error)?;
        Ok(Empty {})
    }

    pub(crate) async fn doc_resolve(&self, p: DocResolveParams) -> Result<DocResolveResult, RpcError> {
        if p.quads.is_empty() && p.rect.is_none() {
            return Err(RpcError::invalid_params("give quads or rect"));
        }
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let (anchor, mut text, boxes) = self.0.docs.resolve(&doc, p.page, &p.quads, p.rect).await.map_err(rpc_error)?;
        let mut more = Vec::new();
        let mut last = p.page;
        for part in &p.more {
            if part.page <= last || part.quads.is_empty() {
                return Err(RpcError::invalid_params("more: pages after `page`, in order, each with quads"));
            }
            last = part.page;
            let (anchor, part_text, boxes) = self.0.docs.resolve(&doc, part.page, &part.quads, None).await.map_err(rpc_error)?;
            if !part_text.is_empty() {
                text = her_docs::layout::join_texts(&[text, part_text]);
            }
            more.push(ResolvedPart { anchor, boxes });
        }
        Ok(DocResolveResult { anchor, text, boxes, more })
    }

    pub(crate) async fn doc_locate(&self, p: DocLocateParams) -> Result<DocLocateResult, RpcError> {
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let anchor = self.0.docs.locate(&doc, &p.target).await.map_err(rpc_error)?;
        Ok(DocLocateResult { anchor })
    }

    pub(crate) async fn doc_cells(&self, p: DocCellsParams) -> Result<DocCellsResult, RpcError> {
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let cells = self.0.docs.cells(&doc, &p.sheet, &p.range).await.map_err(rpc_error)?;
        Ok(DocCellsResult { cells: cells.cells, merges: cells.merges, rows: cells.rows, revision: doc.info().revision })
    }

    /// A person's edit in the grid (spreadsheet-design §7); the agent's go through `doc_edit`.
    pub(crate) async fn doc_apply(&self, p: DocApplyParams) -> Result<DocApplyResult, RpcError> {
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let (revision, impact, dirty) = self.0.docs.apply(&doc, &p.session_id, p.ops, p.base_revision, p.preview).await.map_err(rpc_error)?;
        Ok(DocApplyResult { revision, impact, dirty })
    }

    pub(crate) async fn doc_undo(&self, p: DocIdParams) -> Result<DocApplyResult, RpcError> {
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let (revision, impact, dirty) = self.0.docs.undo(&doc).await.map_err(rpc_error)?;
        Ok(DocApplyResult { revision, impact, dirty })
    }

    pub(crate) async fn doc_redo(&self, p: DocIdParams) -> Result<DocApplyResult, RpcError> {
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let (revision, impact, dirty) = self.0.docs.redo(&doc).await.map_err(rpc_error)?;
        Ok(DocApplyResult { revision, impact, dirty })
    }

    pub(crate) async fn doc_recalc(&self, p: her_protocol::doc::DocRecalcParams) -> Result<DocApplyResult, RpcError> {
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let (revision, impact, dirty) = self.0.docs.recalc(&doc, p.circular_only).await.map_err(rpc_error)?;
        Ok(DocApplyResult { revision, impact, dirty })
    }

    /// The grid's 追踪引用 / 追踪从属 (spreadsheet-design §8.7).
    pub(crate) async fn doc_trace(&self, p: her_protocol::doc::DocTraceParams) -> Result<her_protocol::doc::DocFoundResult, RpcError> {
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let cells = self.0.docs.trace(&doc, &p.sheet, &p.cell, p.direction, p.depth).await.map_err(rpc_error)?;
        Ok(her_protocol::doc::DocFoundResult { more: cells.len() >= 500, cells })
    }

    /// The grid's ⌘F.
    pub(crate) async fn doc_find(&self, p: her_protocol::doc::DocFindParams) -> Result<her_protocol::doc::DocFoundResult, RpcError> {
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let limit = p.limit.unwrap_or(200).clamp(1, 1000) as usize;
        // One more than shown says whether there are others.
        let mut cells = self.0.docs.find_cells(&doc, &p.query, limit + 1).await.map_err(rpc_error)?;
        let more = cells.len() > limit;
        cells.truncate(limit);
        Ok(her_protocol::doc::DocFoundResult { cells, more })
    }

    /// The grid's ⌘D / ⌘R: worked out by the worker, applied as one edit.
    pub(crate) async fn doc_fill(&self, p: her_protocol::doc::DocFillParams) -> Result<DocApplyResult, RpcError> {
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let ops = self.0.docs.fill_ops(&doc, &p.sheet, &p.range, p.direction).await.map_err(rpc_error)?;
        let (revision, impact, dirty) = self.0.docs.apply(&doc, &p.session_id, ops, p.base_revision, false).await.map_err(rpc_error)?;
        Ok(DocApplyResult { revision, impact, dirty })
    }

    /// The grid's 插入 / 删除 of rows and columns: one edit, undone by putting the copy back.
    pub(crate) async fn doc_structure(&self, p: her_protocol::doc::DocStructureParams) -> Result<DocApplyResult, RpcError> {
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let (revision, impact, dirty) = self.0.docs.structure(&doc, &p.session_id, p.change, p.base_revision).await.map_err(rpc_error)?;
        Ok(DocApplyResult { revision, impact, dirty })
    }

    /// The grid's 排序: worked out by the worker, applied as one edit.
    pub(crate) async fn doc_sort(&self, p: her_protocol::doc::DocSortParams) -> Result<DocApplyResult, RpcError> {
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let ops = self.0.docs.sort_ops(&doc, &p.sheet, &p.range, &p.by, p.descending, p.header).await.map_err(rpc_error)?;
        let (revision, impact, dirty) = self.0.docs.apply(&doc, &p.session_id, ops, p.base_revision, false).await.map_err(rpc_error)?;
        Ok(DocApplyResult { revision, impact, dirty })
    }

    /// The grid's fill handle: the series worked out by the worker, applied as one edit.
    pub(crate) async fn doc_autofill(&self, p: her_protocol::doc::DocAutofillParams) -> Result<DocApplyResult, RpcError> {
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let ops = self.0.docs.autofill_ops(&doc, &p.sheet, &p.source, &p.target, p.switch).await.map_err(rpc_error)?;
        let (revision, impact, dirty) = self.0.docs.apply(&doc, &p.session_id, ops, p.base_revision, p.preview).await.map_err(rpc_error)?;
        Ok(DocApplyResult { revision, impact, dirty })
    }

    pub(crate) async fn doc_save(&self, p: DocSaveParams) -> Result<DocInfo, RpcError> {
        let doc = self.0.docs.get(&p.doc_id).map_err(rpc_error)?;
        let to = match &p.path {
            Some(path) if !std::path::Path::new(path).is_absolute() => return Err(RpcError::invalid_params("path must be absolute")),
            Some(path) => Some(PathBuf::from(path)),
            None => None,
        };
        self.0.docs.save(&doc, to.as_deref(), p.force).await.map(|(info, _)| info).map_err(rpc_error)
    }

    /// A closed session's columns show nothing any more.
    pub(crate) async fn docs_forget(&self, session: &str) {
        self.0.docs.forget_session(session).await;
        // Decks the session's agent opened (deck_edit) close with it; unsaved edits stay.
        self.0.decks.forget_session(session).await;
        self.0.words.forget_session(session).await;
    }
}
