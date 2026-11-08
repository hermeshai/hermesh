//! Word documents on BetterOffice: the kernel's editing session (Yrs), which editor replicas
//! join; reading for the agent, atomic edit batches (as revisions by default), review, undo of
//! the kernel's own edits, and rendering of saved bytes.
//!
//! [`Word::save`] writes the `.docx` in-process with the fork's `docx-native-save`, the Rust
//! port of the engine's TypeScript `saveYrsDocx`: unchanged parts keep their source bytes. The
//! desktop's serializer process (the TypeScript save itself, from [`Word::source_bytes`],
//! [`Word::encode_state`] and [`Word::peer_metadata`]) remains as a fallback and a reference;
//! either way the kernel checks the bytes with [`Word::check_saved`] before replacing the file
//! (word-native-save.md).

mod compare;
mod ops;
mod read;
mod render;

use std::collections::BTreeSet;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use betteroffice_docx::fonts::FontError;
use docx_edit::session::{EditSessionCore, EngineResult, Host};
use docx_native_save::OpenedSource;
pub use docx_native_save::SaveOptions;
use serde_json::{Value, json};

pub use ops::{
    Align, Applied, Edge, EditBatch, EditMode, NewParagraph, ReviewAction, RowPosition, WordOp,
};
pub use read::{
    CellPosition, Comment, Found, ListItem, OBJECT, OutlineEntry, ParagraphView, Revision,
    RevisionKind,
};
pub use compare::package_difference;
pub use render::{page_count, render_page};

use read::Paragraph;

/// The engine build. Keep in step with the `docx-edit` rev in the workspace Cargo.toml.
pub const ENGINE: &str = "betteroffice aac46417";

/// The Yrs client id of the kernel's own edits; see [`Word::client_id`].
const CLIENT_ID: u64 = 1;
/// Where the comments of the opened package sit in the engine's open receipt.
const OPENED_COMMENTS: &str = "/envelope/document/package/document/comments";

#[derive(Debug, thiserror::Error)]
pub enum WordError {
    #[error("{0}")]
    Engine(String),
    /// A batch refused whole: `step` is the edit (from 0) that failed.
    #[error("edit {step}: {message}")]
    Step { step: usize, message: String },
    #[error("no paragraph {0}")]
    NoParagraph(String),
    #[error("no revision {0}")]
    NoRevision(String),
    #[error("update refused: {0}")]
    Update(String),
    #[error("no page {0}")]
    NoPage(u32),
    #[error("not a readable .docx: {0}")]
    Docx(#[from] betteroffice_docx::Error),
    #[error("fonts: {0}")]
    Fonts(#[from] FontError),
    /// The native save could not write the document.
    #[error("save: {0}")]
    Save(String),
}

pub type Result<T> = std::result::Result<T, WordError>;

/// What [`Word::check_saved`] found in serialized bytes.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SavedSummary {
    /// Body paragraphs outside tables.
    pub paragraphs: usize,
    pub tables: usize,
    pub sections: usize,
    pub pages: u32,
}

/// The number the editor knows revision `id` by (`scrollToChange`): the id itself when it is
/// a number, else the engine's hash of it into the safe-integer range (docx-edit's layout
/// `numeric_id` with no mapping, which is how the editor lays out).
pub fn revision_number(id: &str) -> u64 {
    if let Ok(n) = id.parse::<u64>() {
        return n;
    }
    let hash = id.bytes().fold(0xcbf2_9ce4_8422_2325_u64, |hash, byte| {
        (hash ^ u64::from(byte)).wrapping_mul(0x0100_0000_01b3)
    });
    hash & ((1 << 53) - 1)
}

/// One Word document's authoritative session.
///
/// Not `Send`: the engine session holds `Rc`/`RefCell` state, so a `Word` lives on the thread
/// that opened it (the deck worker's per-file thread, like `her_slide::Deck`).
pub struct Word {
    session: EditSessionCore,
    source: Vec<u8>,
    sha256: String,
    /// What the native save keeps of the opening, or why it could not (then only the desktop's
    /// serializer can write this document).
    opened: std::result::Result<OpenedSource, String>,
}

impl Word {
    pub fn open(bytes: Vec<u8>) -> Result<Self> {
        let session = new_session();
        let receipt = engine(session.open_docx(&bytes, true, None, None))?;
        let opened = parse(&receipt)?;
        seed_comments(&session, opened.pointer(OPENED_COMMENTS))?;
        drop(opened);
        let native = OpenedSource::after_open(bytes.clone(), &receipt).map_err(|e| e.to_string());
        // After seeding, so opening is not a step to undo.
        session.track_undo();
        let digest = ring::digest::digest(&ring::digest::SHA256, &bytes);
        let sha256 = digest.as_ref().iter().map(|b| format!("{b:02x}")).collect();
        Ok(Self {
            session,
            source: bytes,
            sha256,
            opened: native,
        })
    }

    /// The Yrs client id of this document's own edits. Replicas take a random 53-bit id from 1
    /// up (`replicaClientId` in the desktop app), so they meet this one only by a 2^-53 chance;
    /// every run of the worker reuses 1, which is safe because Yrs continues a client's clock
    /// from the state it holds and the worker replays the whole log before it writes.
    pub fn client_id(&self) -> u64 {
        CLIENT_ID
    }

    /// The bytes it was opened from, the base the serializer writes against.
    pub fn source_bytes(&self) -> &[u8] {
        &self.source
    }

    /// SHA-256 of [`Word::source_bytes`], lowercase hex.
    pub fn source_sha256(&self) -> &str {
        &self.sha256
    }

    /// Full Yrs state for a replica joining this document.
    pub fn encode_state(&self) -> Vec<u8> {
        self.session.encode_state()
    }

    /// What the serializer needs beside [`Word::encode_state`]: media, header and footer aliases
    /// and note separators the state does not carry.
    pub fn peer_metadata(&self) -> Result<Vec<u8>> {
        engine(self.session.encode_peer_metadata())
    }

    pub fn state_vector(&self) -> Vec<u8> {
        self.session.encode_state_vector()
    }

    /// Changes the replica described by `state_vector` does not have yet.
    pub fn encode_diff(&self, state_vector: &[u8]) -> Result<Vec<u8>> {
        engine(self.session.encode_diff(state_vector))
    }

    /// Merges a replica's update. A malformed update is refused whole and changes nothing. It
    /// joins no undo history: [`Word::undo`] never takes back a replica's edit.
    pub fn apply_update(&self, update: &[u8]) -> Result<()> {
        self.session
            .apply_update(update)
            .map_err(|e| WordError::Update(e.to_string()))
    }

    /// Every body paragraph in reading order, table cells row by row where the table stands.
    pub fn outline(&self) -> Result<Vec<OutlineEntry>> {
        read::outline(&self.session)
    }

    /// `count` paragraphs in full from paragraph `from`.
    pub fn read(&self, from: &str, count: usize) -> Result<Vec<ParagraphView>> {
        read::read(&self.session, from, count)
    }

    /// Exact, case-sensitive matches, each within one paragraph, at most `limit`.
    pub fn find(&self, query: &str, limit: usize) -> Result<Vec<Found>> {
        read::find(&self.session, query, limit)
    }

    pub fn comments(&self) -> Result<Vec<Comment>> {
        read::comments(&self.session)
    }

    /// Pending tracked changes in document order.
    pub fn revisions(&self) -> Result<Vec<Revision>> {
        Ok(read::revisions(&self.session)?
            .into_iter()
            .map(Revision::from)
            .collect())
    }

    /// Makes the batch's edits as one step of this document's undo history, or none of them.
    /// They run on a private copy of the session; only when every one succeeds does the copy's
    /// update land here (and go to replicas as [`Applied::update`]).
    pub fn apply(&self, batch: &EditBatch) -> Result<Applied> {
        let draft = self.draft()?;
        let base = self.state_vector();
        let before: BTreeSet<String> = read::revisions(&draft)?.into_iter().map(|r| r.id).collect();
        let step = Step {
            draft: &draft,
            author: &batch.author,
            date: now_iso(),
            suggest: batch.mode == EditMode::Suggest,
        };
        let mut touched = Vec::new();
        for (index, op) in batch.edits.iter().enumerate() {
            step.run(op, &mut touched)
                .map_err(|message| WordError::Step {
                    step: index,
                    message,
                })?;
        }
        let mut created = Vec::new();
        for revision in read::revisions(&draft)? {
            if !before.contains(&revision.id) && !created.contains(&revision.id) {
                created.push(revision.id);
            }
        }
        if draft.encode_state() == self.encode_state() {
            return Ok(Applied {
                paragraphs_touched: touched,
                ..Applied::default()
            });
        }
        let update = engine(draft.encode_diff(&base))?;
        // Its own step: never merged with an edit just before or after it.
        self.session.add_undo_boundary();
        let landed = self.session.apply_local_update(&update);
        self.session.add_undo_boundary();
        engine(landed)?;
        Ok(Applied {
            revision_ids_created: created,
            paragraphs_touched: touched,
            update,
        })
    }

    /// The ids of pending revisions by their [`revision_number`]s.
    pub fn revision_ids(&self, numbers: &[u64]) -> Result<Vec<String>> {
        let pending = read::revisions(&self.session)?;
        let mut ids: Vec<String> = Vec::new();
        for &n in numbers {
            let id = pending
                .iter()
                .find(|r| revision_number(&r.id) == n)
                .ok_or_else(|| WordError::NoRevision(n.to_string()))?;
            if !ids.contains(&id.id) {
                ids.push(id.id.clone());
            }
        }
        Ok(ids)
    }

    /// Accepts or rejects revisions (`None`: every pending one); returns the update.
    pub fn review(&self, action: ReviewAction, ids: Option<&[String]>) -> Result<Vec<u8>> {
        let pending: BTreeSet<String> = read::revisions(&self.session)?
            .into_iter()
            .map(|r| r.id)
            .collect();
        let ids: Vec<String> = match ids {
            Some(ids) => {
                if let Some(missing) = ids.iter().find(|id| !pending.contains(*id)) {
                    return Err(WordError::NoRevision(missing.clone()));
                }
                ids.to_vec()
            }
            None => pending.into_iter().collect(),
        };
        if ids.is_empty() {
            return Ok(Vec::new());
        }
        let request = match action {
            ReviewAction::Accept => json!({ "accept": ids }),
            ReviewAction::Reject => json!({ "reject": ids }),
        };
        let sv = self.state_vector();
        outcome(&engine(
            self.session.settle_revisions_json(&request.to_string()),
        )?)
        .map_err(WordError::Engine)?;
        // Settling may only delete, which leaves the state vector as it was: the diff, always.
        self.encode_diff(&sv)
    }

    /// Takes back this document's last own batch; replicas' edits are not in its history and
    /// stay. `None` when there is nothing to take back.
    pub fn undo(&self) -> Result<Option<Vec<u8>>> {
        if !self.session.can_undo() {
            return Ok(None);
        }
        let sv = self.state_vector();
        if !self.session.undo() {
            return Ok(None);
        }
        Ok(Some(self.encode_diff(&sv)?))
    }

    /// The `.docx` of the document as it is now, written in-process: what the desktop's
    /// serializer writes from the same state (`saveYrsDocx`). Only reads the session.
    pub fn save(&self) -> Result<Vec<u8>> {
        self.save_with(&SaveOptions::default())
    }

    /// [`Word::save`] with the writer's options; `record_saved_paragraph_ids` is always off,
    /// so saving is never a change to the document.
    pub fn save_with(&self, options: &SaveOptions) -> Result<Vec<u8>> {
        let opened = self.opened.as_ref().map_err(|e| WordError::Save(e.clone()))?;
        let options = SaveOptions {
            record_saved_paragraph_ids: false,
            ..options.clone()
        };
        docx_native_save::save_docx(&self.session, opened, &options)
            .map(|saved| saved.bytes)
            .map_err(|e| WordError::Save(e.to_string()))
    }

    /// Opens what the serializer wrote and says what is in it; bytes the engine cannot read or
    /// lay out are an error.
    pub fn check_saved(&self, bytes: &[u8]) -> Result<SavedSummary> {
        let document = render::open(bytes)?;
        let structure = document.structure();
        Ok(SavedSummary {
            paragraphs: structure.body_paragraphs,
            tables: structure.body_tables,
            sections: structure.sections,
            pages: render::count(&document)?,
        })
    }

    /// A copy of the session to run a batch on: the same state, source and client.
    fn draft(&self) -> Result<EditSessionCore> {
        let draft = new_session();
        let metadata = engine(self.session.encode_peer_metadata())?;
        engine(draft.bootstrap_peer(&self.encode_state(), &metadata, Some(self.source.clone())))?;
        Ok(draft)
    }
}

/// One batch's edits on its draft.
struct Step<'a> {
    draft: &'a EditSessionCore,
    author: &'a str,
    date: String,
    suggest: bool,
}

impl Step<'_> {
    fn run(&self, op: &WordOp, touched: &mut Vec<String>) -> std::result::Result<(), String> {
        let mut touch = |id: &str| {
            if !touched.iter().any(|t| t == id) {
                touched.push(id.to_owned());
            }
        };
        match op {
            WordOp::ReplaceText {
                paragraph,
                find,
                replace,
            } => {
                let p = self.paragraph(paragraph)?;
                self.once(&p, find)?;
                let step = if replace.is_empty() {
                    json!({ "op": "deleteText", "target": search(&p, find) })
                } else {
                    json!({ "op": "replaceText", "target": search(&p, find), "text": replace })
                };
                self.edit(step)?;
                touch(paragraph);
            }
            WordOp::InsertText {
                paragraph,
                text,
                after,
                at,
            } => {
                let p = self.paragraph(paragraph)?;
                let (target, edge) = match (after, at) {
                    (Some(_), Some(_)) => return Err("give after or at, not both".into()),
                    (Some(find), None) => {
                        self.once(&p, find)?;
                        (search(&p, find), "end")
                    }
                    (None, at) => (
                        whole(&p),
                        if *at == Some(Edge::Start) {
                            "start"
                        } else {
                            "end"
                        },
                    ),
                };
                self.edit(
                    json!({ "op": "insertText", "target": target, "at": edge, "text": text }),
                )?;
                touch(paragraph);
            }
            WordOp::DeleteText { paragraph, find } => {
                let p = self.paragraph(paragraph)?;
                self.once(&p, find)?;
                self.edit(json!({ "op": "deleteText", "target": search(&p, find) }))?;
                touch(paragraph);
            }
            WordOp::FormatText {
                paragraph,
                find,
                bold,
                italic,
                underline,
                strike,
                color,
                highlight,
            } => {
                let p = self.paragraph(paragraph)?;
                let mut delta = serde_json::Map::new();
                for (key, flag) in [
                    ("bold", bold),
                    ("italic", italic),
                    ("underline", underline),
                    ("strike", strike),
                ] {
                    if let Some(flag) = flag {
                        delta.insert(key.into(), if *flag { json!(true) } else { Value::Null });
                    }
                }
                if let Some(color) = color {
                    delta.insert(
                        "color".into(),
                        json!({ "rgb": color.trim_start_matches('#').to_uppercase() }),
                    );
                }
                if let Some(highlight) = highlight {
                    delta.insert("highlight".into(), json!(highlight));
                }
                if delta.is_empty() {
                    return Err("format_text names no formatting".into());
                }
                let (start, end) = self.range(&p, find.as_deref())?;
                let (author, date) = self.stamp();
                let delta = Value::Object(delta).to_string();
                self.draft
                    .format_range(&p.story, &p.id, start, &p.id, end, &delta, author, date)
                    .map_err(|e| e.to_string())?;
                touch(paragraph);
            }
            WordOp::InsertParagraphs {
                after,
                before,
                paragraphs,
            } => {
                let (anchor, edge) = match (after, before) {
                    (Some(id), None) => (id, "end"),
                    (None, Some(id)) => (id, "start"),
                    _ => return Err("give exactly one of after and before".into()),
                };
                if paragraphs.is_empty() {
                    return Err("insert_paragraphs has no paragraphs".into());
                }
                let p = self.paragraph(anchor)?;
                let inputs: Vec<Value> = paragraphs
                    .iter()
                    .map(|n| match &n.style {
                        Some(style) => json!({ "text": n.text, "styleId": style }),
                        None => json!({ "text": n.text }),
                    })
                    .collect();
                let receipt = self.edit(json!({
                    "op": "insertParagraphs",
                    "target": { "story": p.story, "paraId": p.id },
                    "at": edge,
                    "paragraphs": inputs,
                }))?;
                for new in receipt["newParagraphs"].as_array().into_iter().flatten() {
                    touch(new["paraId"].as_str().unwrap_or_default());
                }
            }
            WordOp::DeleteParagraph { paragraph } => {
                let p = self.paragraph(paragraph)?;
                self.edit(json!({ "op": "deleteParagraphs", "story": p.story, "firstParaId": p.id, "lastParaId": p.id }))?;
                touch(paragraph);
            }
            WordOp::SetParagraphStyle { paragraph, style } => {
                let p = self.paragraph(paragraph)?;
                self.edit_untracked(json!({
                    "op": "setParagraphStyle",
                    "target": { "story": p.story, "paraId": p.id },
                    "styleId": style,
                }))?;
                touch(paragraph);
            }
            WordOp::SetAlignment { paragraph, align } => {
                let p = self.paragraph(paragraph)?;
                let (author, date) = self.stamp();
                let attrs = json!({ "alignment": align.ooxml() }).to_string();
                self.draft
                    .set_paragraph_attrs(&p.story, &p.id, 0, &p.id, 0, &attrs, author, date)
                    .map_err(|e| e.to_string())?;
                touch(paragraph);
            }
            WordOp::AddComment {
                paragraph,
                find,
                text,
            } => {
                let p = self.paragraph(paragraph)?;
                let (start, end) = self.range(&p, find.as_deref())?;
                let ranges = json!([{ "story": p.story, "startPara": p.id, "startOffset": start, "endPara": p.id, "endOffset": end }]);
                self.add_comment(&ranges, text)?;
                touch(paragraph);
            }
            WordOp::ReplyComment { comment, text } => {
                let root = self.root_comment(comment)?;
                let id = self.add_comment(&self.comment_ranges(&root)?, text)?;
                self.draft
                    .update_comment_metadata_json(&id, &json!({ "parentId": root }).to_string())
                    .map_err(|e| e.to_string())?;
            }
            WordOp::ResolveComment { comment } => {
                let root = self.root_comment(comment)?;
                self.draft
                    .update_comment_metadata_json(&root, &json!({ "done": true }).to_string())
                    .map_err(|e| e.to_string())?;
            }
            WordOp::InsertTableRow {
                row_of,
                position,
                cells,
            } => {
                let p = self.paragraph(row_of)?;
                let cell = p.cell.as_ref().ok_or_else(|| not_in_table(row_of))?;
                if cells.len() != cell.columns as usize {
                    return Err(format!(
                        "the row has {} cells, not {}",
                        cell.columns,
                        cells.len()
                    ));
                }
                let (author, date) = self.stamp();
                let at = json!({ "story": cell.parent, "tableIndex": cell.table_index, "row": cell.row, "column": cell.column });
                let receipt = self
                    .draft
                    .insert_row(
                        &at.to_string(),
                        *position == RowPosition::After,
                        author,
                        date,
                    )
                    .map_err(|e| e.to_string())?;
                let receipt = parse(&receipt).map_err(|e| e.to_string())?;
                let stories = receipt["createdStoryIds"]
                    .as_array()
                    .cloned()
                    .unwrap_or_default();
                let ids = receipt["newParaIds"]
                    .as_array()
                    .cloned()
                    .unwrap_or_default();
                if stories.len() != cells.len() || ids.len() != cells.len() {
                    return Err("a merged cell crosses this row; insert next to another row".into());
                }
                for ((story, id), text) in stories.iter().zip(&ids).zip(cells) {
                    let id = id.as_str().unwrap_or_default();
                    if !text.is_empty() {
                        let target = json!({ "kind": "paragraph", "story": story, "paraId": id });
                        self.edit(json!({ "op": "insertText", "target": target, "at": "end", "text": text }))?;
                    }
                    touch(id);
                }
            }
            WordOp::DeleteTableRow { row_of } => {
                let p = self.paragraph(row_of)?;
                let cell = p.cell.as_ref().ok_or_else(|| not_in_table(row_of))?;
                if cell.rows == 1 {
                    return Err("a table keeps at least one row".into());
                }
                let (author, date) = self.stamp();
                let at = json!({ "story": cell.parent, "tableIndex": cell.table_index, "row": cell.row, "column": cell.column });
                self.draft
                    .delete_row(
                        &json!({ "anchor": at, "head": at }).to_string(),
                        author,
                        date,
                    )
                    .map_err(|e| e.to_string())?;
                touch(row_of);
            }
            WordOp::SetCellText { cell_of, text } => {
                let p = self.paragraph(cell_of)?;
                if p.cell.is_none() {
                    return Err(not_in_table(cell_of));
                }
                let in_cell: Vec<Paragraph> = self
                    .paragraphs()?
                    .into_iter()
                    .filter(|q| q.story == p.story)
                    .collect();
                let first = &in_cell[0];
                let step = match (first.text.is_empty(), text.is_empty()) {
                    (true, true) => None,
                    (true, false) => Some(
                        json!({ "op": "insertText", "target": whole(first), "at": "end", "text": text }),
                    ),
                    (false, true) => Some(json!({ "op": "deleteText", "target": whole(first) })),
                    (false, false) => {
                        Some(json!({ "op": "replaceText", "target": whole(first), "text": text }))
                    }
                };
                if let Some(step) = step {
                    self.edit(step)?;
                }
                if let (Some(second), Some(last)) = (in_cell.get(1), in_cell.last()) {
                    self.edit(json!({ "op": "deleteParagraphs", "story": p.story, "firstParaId": second.id, "lastParaId": last.id }))?;
                }
                touch(&first.id);
            }
        }
        Ok(())
    }

    fn paragraphs(&self) -> std::result::Result<Vec<Paragraph>, String> {
        read::paragraphs(self.draft).map_err(|e| e.to_string())
    }

    fn paragraph(&self, id: &str) -> std::result::Result<Paragraph, String> {
        let mut found = self.paragraphs()?.into_iter().filter(|p| p.id == id);
        match (found.next(), found.next()) {
            (Some(p), None) => Ok(p),
            (Some(_), Some(_)) => Err(format!("paragraph id {id} is not unique in this document")),
            (None, _) => Err(format!("no paragraph {id}")),
        }
    }

    /// Refuses unless `find` occurs exactly once in the paragraph's text.
    fn once(&self, p: &Paragraph, find: &str) -> std::result::Result<usize, String> {
        if find.is_empty() {
            return Err("find is empty".into());
        }
        match read::matches(&p.text, find).as_slice() {
            [offset] => Ok(*offset),
            [] => Err(format!("{find:?} is not in paragraph {}", p.id)),
            all => Err(format!(
                "{find:?} occurs {} times in paragraph {}; quote more of the text so it occurs once",
                all.len(),
                p.id
            )),
        }
    }

    /// The match of `find` (the whole paragraph without one) in the session's own offsets,
    /// which count text a pending deletion hides.
    fn range(&self, p: &Paragraph, find: Option<&str>) -> std::result::Result<(u32, u32), String> {
        let (from, to) = match find {
            Some(find) => {
                let at = self.once(p, find)?;
                (at, at + find.encode_utf16().count())
            }
            None => (0, p.text.encode_utf16().count()),
        };
        let span = parse(
            &self
                .draft
                .locate_paragraph(&p.story, &p.id)
                .map_err(|e| e.to_string())?,
        )
        .map_err(|e| e.to_string())?;
        let (start, end) = (
            span["start"].as_u64().unwrap_or(0),
            span["end"].as_u64().unwrap_or(0),
        );
        let (mut raw, mut seen) = (0u64, 0usize);
        let (mut raw_from, mut raw_to) = (None, None);
        for segment in read::segments(self.draft, &p.story).map_err(|e| e.to_string())? {
            let units = match segment["text"].as_str() {
                Some(text) if segment["kind"] == "text" => text.encode_utf16().count() as u64,
                _ => 1,
            };
            if raw >= start && raw < end && segment["attributes"].get("del").is_none() {
                let shown = units as usize;
                if raw_from.is_none() && from < seen + shown {
                    raw_from = Some(raw - start + (from - seen) as u64);
                }
                if raw_to.is_none() && to <= seen + shown {
                    raw_to = Some(raw - start + (to - seen) as u64);
                }
                seen += shown;
            }
            raw += units;
        }
        let length = end - start;
        Ok((
            raw_from.unwrap_or(length) as u32,
            raw_to.unwrap_or(length) as u32,
        ))
    }

    /// The revision author and date in suggest mode.
    fn stamp(&self) -> (Option<String>, Option<String>) {
        if self.suggest {
            (Some(self.author.to_owned()), Some(self.date.clone()))
        } else {
            (None, None)
        }
    }

    /// One engine batch step, suggested in suggest mode; returns its receipt.
    fn edit(&self, mut step: Value) -> std::result::Result<Value, String> {
        if self.suggest {
            step["suggest"] = json!({ "author": self.author, "date": self.date });
        }
        self.edit_untracked(step)
    }

    fn edit_untracked(&self, step: Value) -> std::result::Result<Value, String> {
        let request = json!({ "expectVersion": self.draft.version(), "source": "agent", "history": "none", "steps": [step] });
        let result = self
            .draft
            .apply_edits_json(&request.to_string())
            .map_err(|e| e.to_string())?;
        let mut result = outcome(&result)?;
        Ok(result["receipts"][0].take())
    }

    fn add_comment(&self, ranges: &Value, text: &str) -> std::result::Result<String, String> {
        let body: Vec<Value> = text
            .split('\n')
            .map(|line| {
                let runs = if line.is_empty() { json!([]) } else { json!([{ "type": "run", "formatting": {}, "content": [{ "type": "text", "text": line }] }]) };
                json!({ "type": "paragraph", "formatting": {}, "content": runs })
            })
            .collect();
        let added = self
            .draft
            .add_comment(
                &ranges.to_string(),
                self.author,
                &self.date,
                &Value::Array(body).to_string(),
            )
            .map_err(|e| e.to_string())?;
        let added = parse(&added).map_err(|e| e.to_string())?;
        Ok(added["commentId"].as_str().unwrap_or_default().to_owned())
    }

    /// The thread's first comment.
    fn root_comment(&self, id: &str) -> std::result::Result<String, String> {
        let listed = parse(&self.draft.list_comments().map_err(|e| e.to_string())?)
            .map_err(|e| e.to_string())?;
        let comment = listed
            .as_array()
            .into_iter()
            .flatten()
            .find(|c| c["id"] == id)
            .ok_or_else(|| format!("no comment {id}"))?;
        Ok(comment["parentId"].as_str().unwrap_or(id).to_owned())
    }

    /// Where a comment is anchored, as `add_comment` ranges.
    fn comment_ranges(&self, id: &str) -> std::result::Result<Value, String> {
        let anchors = self
            .draft
            .resolve_comment(id)
            .map_err(|_| format!("comment {id} has no anchor in the text"))?;
        let anchors = parse(&anchors).map_err(|e| e.to_string())?;
        let paragraphs = self.paragraphs()?;
        let mut spans = Vec::new();
        for p in &paragraphs {
            let span = parse(
                &self
                    .draft
                    .locate_paragraph(&p.story, &p.id)
                    .map_err(|e| e.to_string())?,
            )
            .map_err(|e| e.to_string())?;
            spans.push((
                p,
                span["start"].as_u64().unwrap_or(0),
                span["end"].as_u64().unwrap_or(0),
            ));
        }
        let point = |story: &str, at: u64| {
            spans
                .iter()
                .find(|(p, start, end)| p.story == story && *start <= at && at <= *end)
                .map(|(p, start, _)| (p.id.clone(), at - start))
        };
        let mut ranges = Vec::new();
        for anchor in anchors.as_array().into_iter().flatten() {
            let story = anchor["story"].as_str().unwrap_or_default();
            let (Some(start), Some(end)) = (
                point(story, anchor["start"].as_u64().unwrap_or(0)),
                point(story, anchor["end"].as_u64().unwrap_or(0)),
            ) else {
                continue;
            };
            ranges.push(json!({ "story": story, "startPara": start.0, "startOffset": start.1, "endPara": end.0, "endOffset": end.1 }));
        }
        if ranges.is_empty() {
            return Err(format!("comment {id} has no anchor in the text"));
        }
        Ok(Value::Array(ranges))
    }
}

fn search(p: &Paragraph, find: &str) -> Value {
    json!({ "kind": "search", "text": find, "within": { "kind": "paragraph", "story": p.story, "paraId": p.id }, "view": "accepted" })
}

fn whole(p: &Paragraph) -> Value {
    json!({ "kind": "paragraph", "story": p.story, "paraId": p.id })
}

fn not_in_table(id: &str) -> String {
    format!("paragraph {id} is not in a table")
}

/// Seeds the opened package's comments into the session as the editor does
/// (`seedDocxCommentMetadata`): those anchored in the text, and replies.
fn seed_comments(session: &EditSessionCore, comments: Option<&Value>) -> Result<()> {
    let listed = parse(&engine(session.list_comments())?)?;
    let anchored: Vec<&str> = listed
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(|c| c["id"].as_str())
        .collect();
    for comment in comments.and_then(Value::as_array).into_iter().flatten() {
        let id = match &comment["id"] {
            Value::Number(n) => n
                .as_f64()
                .map(|f| (f as i64).to_string())
                .unwrap_or_default(),
            Value::String(s) => s.clone(),
            _ => continue,
        };
        let parent = &comment["parentId"];
        if !anchored.contains(&id.as_str()) && parent.is_null() {
            continue;
        }
        let parent = match parent {
            Value::Number(n) => json!(n.as_f64().map(|f| (f as i64).to_string())),
            Value::String(s) => json!(s),
            _ => Value::Null,
        };
        let body = if comment["blockContent"].is_null() {
            &comment["content"]
        } else {
            &comment["blockContent"]
        };
        let patch = json!({
            "author": comment["author"].as_str().unwrap_or_default(),
            "date": comment["date"].as_str().unwrap_or_default(),
            "done": comment["done"].as_bool().unwrap_or(false),
            "parentId": parent,
            "body": body,
        });
        engine(session.seed_comment_metadata_json(&id, &patch.to_string()))?;
    }
    Ok(())
}

fn new_session() -> EditSessionCore {
    static ENTROPY: AtomicU64 = AtomicU64::new(0);
    let seed = now_ms() ^ 0x9e37_79b9_7f4a_7c15;
    let host = Host::new(
        Arc::new(now_ms),
        Arc::new(move || {
            seed.wrapping_add(ENTROPY.fetch_add(0x9e37_79b9_7f4a_7c15, Ordering::Relaxed))
        }),
        Arc::new(|| now_ms() as f64),
    );
    EditSessionCore::new(CLIENT_ID, host)
}

fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

/// Now as Word writes revision dates: `2026-11-07T01:02:03Z`.
fn now_iso() -> String {
    let secs = (now_ms() / 1000) as i64;
    let (days, rest) = (secs.div_euclid(86_400), secs.rem_euclid(86_400));
    // Civil date from days since 1970-01-01 (Howard Hinnant's algorithm).
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}Z",
        rest / 3600,
        rest % 3600 / 60,
        rest % 60
    )
}

fn engine<T>(result: EngineResult<T>) -> Result<T> {
    result.map_err(|e| WordError::Engine(e.to_string()))
}

fn parse(json: &str) -> Result<Value> {
    serde_json::from_str(json).map_err(|e| WordError::Engine(e.to_string()))
}

/// An engine outcome: `{"ok":true,…}`, or a refusal's message.
fn outcome(json: &str) -> std::result::Result<Value, String> {
    let value: Value = serde_json::from_str(json).map_err(|e| e.to_string())?;
    if value["ok"] == true {
        return Ok(value);
    }
    Err(value["failure"]["message"]
        .as_str()
        .unwrap_or("refused")
        .to_owned())
}
