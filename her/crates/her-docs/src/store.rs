//! DocStore (design §5.2): one per service, one entry per file, shared by every session that
//! opened it. A document's directory is `<docs>/<doc_id>/`: `base.<ext>` (the file as opened or
//! last saved), `current.<ext>` (the working copy), `meta.json`, and for a workbook
//! `revisions.jsonl` (its edits, spreadsheet-design §6). Sessions only keep what they look at.
//!
//! PDFs are read only: opening a file that changed on disk takes the new contents. Workbooks
//! are edited in the working copy and saved back on request; a workbook with unsaved edits is
//! kept even when its file changes on disk, and saving over that change needs `force`.

use her_protocol::doc::Anchor;
use her_protocol::doc::Capability;
use her_protocol::doc::DocInfo;
use her_protocol::doc::DocViewing;
use her_protocol::doc::Impact;
use her_protocol::doc::Rect;
use her_protocol::doc::SheetOp;
use her_protocol::doc::SheetStructure;
use her_protocol::doc::TextBlock;
use parking_lot::Mutex;
use serde::Deserialize;
use serde::Serialize;
use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;

use crate::engine::Opened;
use crate::engine::Rendered;
use crate::engine::Request;
use crate::atomic;
use crate::kind::DocKind;
use crate::kind::Unopenable;
use crate::layout;
use crate::layout::PageChars;
use crate::sheet::Applied;
use crate::sheet::Cells;
use crate::sheet::Direction;
use crate::sheet::Found;
use crate::sheet::REFUSED;
use crate::sheet::SheetOpened;
use crate::worker::Launcher;
use crate::worker::Worker;
use crate::worker::WorkerError;

/// The largest file opened (design §11).
pub const MAX_FILE_BYTES: u64 = 1 << 30;
/// Images for the agent: never more than this on the longer side.
pub const MAX_RENDER_SIDE: u32 = 2000;

#[derive(Debug, thiserror::Error)]
pub enum DocError {
    #[error("{0}: no such file")]
    NotFound(String),
    #[error("{0} is not a document her opens: PDF, xlsx, xlsm, xls, csv or tsv")]
    Unsupported(String),
    #[error("{0} is larger than 1 GB")]
    TooLarge(String),
    #[error("the document is encrypted: the password is missing or wrong")]
    Password,
    #[error("{0}: cannot open this document: {1}")]
    Unreadable(String, String),
    #[error("no open document {0}: open it first")]
    NotOpen(String),
    #[error("no page {page}: the document has {pages}")]
    NoPage { page: u32, pages: u32 },
    /// A workbook edit that would risk the file (spreadsheet-design §5.3.3).
    #[error("{0}")]
    Refused(String),
    #[error("the document changed: it is at revision {now}, the edit was made against {base}")]
    Stale { base: u64, now: u64 },
    #[error("{0} changed on disk since it was opened; save again with force to write over it")]
    Conflict(String),
    #[error("{0}")]
    NotEditable(String),
    #[error("{0}")]
    Io(#[from] std::io::Error),
    #[error("{0}")]
    Worker(#[from] WorkerError),
}

/// A page's text: PDFium's characters and the blocks made of them.
pub struct PageText {
    pub chars: PageChars,
    pub blocks: Vec<TextBlock>,
}

/// A hit of a search, with a little text around it.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Hit {
    pub anchor: Anchor,
    pub block: Option<String>,
    pub context: String,
}

/// What a session's column shows of a document.
#[derive(Clone, Debug, Default)]
struct View {
    page: u32,
    selection: Option<Anchor>,
    token: String,
}

/// One edit of a workbook, as kept in `revisions.jsonl`.
#[derive(Clone, Debug, Serialize, Deserialize)]
struct Revision {
    ops: Vec<SheetOp>,
    undo: Vec<SheetOp>,
    /// Rows or columns inserted or deleted, instead of `ops`; undone by putting back
    /// `snapshot`, the working copy as it was before (a file in the document's directory).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    structure: Option<SheetStructure>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    snapshot: Option<String>,
    session: String,
    at_ms: u128,
}

/// A workbook's edits: the first `applied` are in the working copy, the rest can be redone;
/// `saved` is how many were applied when it was last saved, if that state is still reachable.
#[derive(Debug, Default)]
struct History {
    revisions: Vec<Revision>,
    applied: usize,
    saved: Option<usize>,
}

impl History {
    fn dirty(&self) -> bool {
        self.saved != Some(self.applied)
    }
}

/// What a save put into the file that it did not hold before: the cells the edits since
/// then leave set, as formulas or values, and the ranges they leave cleared.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Written {
    pub formulas: usize,
    pub values: usize,
    pub cleared: usize,
    /// Rows or columns inserted or deleted.
    pub reshaped: usize,
}

impl Written {
    fn of<'a>(ops: impl Iterator<Item = &'a SheetOp>) -> Self {
        use her_sheet::cellref::{CellRef, Rect};
        // The last op on a cell wins; a clear takes back the sets inside it.
        let mut set: HashMap<(String, String), bool> = HashMap::new();
        let mut cleared: Vec<(String, String)> = Vec::new();
        for op in ops {
            match op {
                SheetOp::SetValue { sheet, cell, .. } => {
                    set.insert((sheet.clone(), cell.to_ascii_uppercase()), false);
                }
                SheetOp::SetFormula { sheet, cell, .. } => {
                    set.insert((sheet.clone(), cell.to_ascii_uppercase()), true);
                }
                SheetOp::Clear { sheet, range } => {
                    if let Some(r) = Rect::parse(range) {
                        set.retain(|(s, c), _| s != sheet || !CellRef::parse(c).is_some_and(|c| r.contains(c)));
                    }
                    cleared.push((sheet.clone(), range.to_ascii_uppercase()));
                }
            }
        }
        cleared.sort();
        cleared.dedup();
        let formulas = set.values().filter(|f| **f).count();
        Self { formulas, values: set.len() - formulas, cleared: cleared.len(), reshaped: 0 }
    }
}

pub struct Doc {
    pub id: String,
    pub path: PathBuf,
    dir: PathBuf,
    kind: DocKind,
    handle: u64,
    /// The password it was opened with, in memory only: the pages of every session need it.
    password: Option<String>,
    info: Mutex<DocInfo>,
    pages: Mutex<HashMap<u32, Arc<PageText>>>,
    /// The sessions that have it open, and what each shows.
    views: Mutex<HashMap<String, View>>,
    history: Mutex<History>,
    /// One edit at a time: revisions stay in order.
    editing: tokio::sync::Mutex<()>,
}

impl Doc {
    pub fn info(&self) -> DocInfo {
        self.info.lock().clone()
    }

    pub fn password(&self) -> Option<&str> {
        self.password.as_deref()
    }

    pub fn pages(&self) -> u32 {
        self.info.lock().pages.unwrap_or(0)
    }

    /// The working copy.
    pub fn current(&self) -> PathBuf {
        self.dir.join(format!("current.{}", self.kind.ext()))
    }

    pub fn is_sheet(&self) -> bool {
        self.kind.is_sheet()
    }

    fn dirty(&self) -> bool {
        self.is_sheet() && self.history.lock().dirty()
    }

    fn check_page(&self, page: u32) -> Result<(), DocError> {
        let pages = self.pages();
        if page == 0 || page > pages {
            return Err(DocError::NoPage { page, pages });
        }
        Ok(())
    }
}

#[derive(Serialize, Deserialize)]
struct Meta {
    path: PathBuf,
    size: u64,
    modified_ms: u128,
}

/// Told what a document is after it changed.
type Listener = Box<dyn Fn(&DocInfo) + Send + Sync>;

pub struct DocStore {
    listeners: Mutex<Vec<Listener>>,
    root: PathBuf,
    worker: Worker,
    docs: Mutex<HashMap<String, Arc<Doc>>>,
    /// One open at a time: two sessions opening the same file get the same entry.
    opening: tokio::sync::Mutex<()>,
}

/// A document's id: from its canonical path, the same in every session and every run.
pub fn doc_id(canonical: &Path) -> String {
    let digest = ring::digest::digest(&ring::digest::SHA256, canonical.as_os_str().as_encoded_bytes());
    digest.as_ref()[..8].iter().map(|b| format!("{b:02x}")).collect()
}

impl DocStore {
    /// A store keeping its documents in `root` (`$HER_HOME/docs`). Tokens of an earlier run are
    /// dropped: no page holds them any more.
    pub fn new(root: PathBuf, launcher: Launcher) -> Self {
        let _ = std::fs::remove_dir_all(root.join(".tokens"));
        Self { listeners: Mutex::default(), root, worker: Worker::new(launcher), docs: Mutex::default(), opening: tokio::sync::Mutex::new(()) }
    }

    /// Called after every change to a document, from any session or tool.
    pub fn on_change(&self, f: impl Fn(&DocInfo) + Send + Sync + 'static) {
        self.listeners.lock().push(Box::new(f));
    }

    fn changed(&self, doc: &Doc) {
        let info = doc.info();
        for f in self.listeners.lock().iter() {
            f(&info);
        }
    }

    pub fn root(&self) -> &Path {
        &self.root
    }

    pub fn get(&self, doc_id: &str) -> Result<Arc<Doc>, DocError> {
        self.docs.lock().get(doc_id).cloned().ok_or_else(|| DocError::NotOpen(doc_id.to_string()))
    }

    /// The open document of a file, if any.
    pub fn by_path(&self, path: &Path) -> Option<Arc<Doc>> {
        let canonical = std::fs::canonicalize(path).ok()?;
        self.docs.lock().get(&doc_id(&canonical)).cloned()
    }

    /// The file a document id was opened from, as its directory remembers it.
    pub fn path_of(&self, doc_id: &str) -> Result<PathBuf, DocError> {
        if !doc_id.chars().all(|c| c.is_ascii_hexdigit()) || doc_id.is_empty() {
            return Err(DocError::NotOpen(doc_id.to_string()));
        }
        if let Ok(doc) = self.get(doc_id) {
            return Ok(doc.path.clone());
        }
        let text = std::fs::read_to_string(self.root.join(doc_id).join("meta.json")).map_err(|_| DocError::NotOpen(doc_id.to_string()))?;
        let meta: Meta = serde_json::from_str(&text).map_err(|_| DocError::NotOpen(doc_id.to_string()))?;
        Ok(meta.path)
    }

    /// Opens a file for a session, or joins the entry another session opened (§6 `doc/open`).
    /// Returns the document and the session's token for its bytes.
    pub async fn open(&self, path: &Path, password: Option<String>, session: &str) -> Result<(Arc<Doc>, String), DocError> {
        let _one = self.opening.lock().await;
        let canonical = std::fs::canonicalize(path).map_err(|_| DocError::NotFound(path.display().to_string()))?;
        let meta = std::fs::metadata(&canonical)?;
        if !meta.is_file() {
            return Err(DocError::NotFound(path.display().to_string()));
        }
        if meta.len() > MAX_FILE_BYTES {
            return Err(DocError::TooLarge(path.display().to_string()));
        }
        let id = doc_id(&canonical);
        let stamp = Meta { path: canonical.clone(), size: meta.len(), modified_ms: modified_ms(&meta) };
        let existing = self.docs.lock().get(&id).cloned();
        let doc = match existing {
            // Unsaved edits are not dropped because the file changed: saving says so (`Conflict`).
            Some(doc) if doc.dirty() || self.unchanged(&doc.dir, doc.kind, &stamp) => doc,
            stale => {
                if let Some(doc) = stale {
                    self.worker.close(doc.handle).await;
                }
                let doc = Arc::new(self.load(&canonical, &id, stamp, password).await?);
                self.docs.lock().insert(id.clone(), doc.clone());
                doc
            }
        };
        let token = self.attach(&doc, session)?;
        Ok((doc, token))
    }

    fn unchanged(&self, dir: &Path, kind: DocKind, stamp: &Meta) -> bool {
        let Ok(text) = std::fs::read_to_string(dir.join("meta.json")) else { return false };
        let Ok(meta) = serde_json::from_str::<Meta>(&text) else { return false };
        meta.size == stamp.size && meta.modified_ms == stamp.modified_ms && dir.join(format!("current.{}", kind.ext())).is_file()
    }

    async fn load(&self, canonical: &Path, id: &str, stamp: Meta, password: Option<String>) -> Result<Doc, DocError> {
        let shown = || canonical.display().to_string();
        let mut head = [0u8; 1024];
        let n = std::io::Read::read(&mut std::fs::File::open(canonical)?, &mut head)?;
        let kind = DocKind::detect(&head[..n], canonical.extension().and_then(|e| e.to_str())).map_err(|e| match e {
            Unopenable::Unsupported => DocError::Unsupported(shown()),
            Unopenable::EncryptedWorkbook => DocError::Unreadable(shown(), "the workbook is encrypted: remove its password in Excel to open it here".into()),
        })?;
        let ext = kind.ext();
        let dir = self.root.join(id);
        std::fs::create_dir_all(&dir)?;
        let kept = self.unchanged(&dir, kind, &stamp);
        if !kept {
            atomic::copy(canonical, &dir.join(format!("base.{ext}")))?;
            atomic::copy(canonical, &dir.join(format!("current.{ext}")))?;
            atomic::write(&dir.join("meta.json"), &serde_json::to_vec_pretty(&stamp).map_err(std::io::Error::other)?)?;
            let _ = std::fs::remove_file(dir.join("revisions.jsonl"));
            for e in std::fs::read_dir(&dir).into_iter().flatten().flatten() {
                if e.file_name().to_string_lossy().starts_with("snapshot-") {
                    let _ = std::fs::remove_file(e.path());
                }
            }
        }
        let current = dir.join(format!("current.{ext}"));
        let read_only = kind.read_only() || std::fs::metadata(canonical).map(|m| m.permissions().readonly()).unwrap_or(true);
        let mut info = DocInfo {
            doc_id: id.to_string(),
            path: shown(),
            format: kind.format(),
            layout: kind.layout(),
            pages: None,
            outline: Vec::new(),
            capabilities: Vec::new(),
            revision: 0,
            dirty: false,
            save_in_place: !read_only,
            scanned: false,
            encrypted: false,
            sheets: Vec::new(),
        };
        let mut history = History::default();
        let (handle, password) = if kind.is_sheet() {
            let (handle, opened): (u64, SheetOpened) = self.worker.open_sheet(current, canonical.file_stem().map(|s| s.to_string_lossy().into_owned())).await.map_err(|e| match e {
                WorkerError::Failed(m) => DocError::Unreadable(shown(), m),
                e => DocError::Worker(e),
            })?;
            // A working copy kept from an earlier run keeps its edits, to undo or save.
            history.saved = Some(0);
            if kept && let Ok(text) = std::fs::read_to_string(dir.join("revisions.jsonl")) {
                history.revisions = text.lines().filter_map(|l| serde_json::from_str(l).ok()).collect();
                history.applied = history.revisions.len();
                let same = std::fs::read(dir.join(format!("base.{ext}"))).ok() == std::fs::read(dir.join(format!("current.{ext}"))).ok();
                history.saved = if same { Some(history.applied) } else { None };
            }
            info.capabilities = kind.sheet_capabilities();
            if opened.calc_unavailable.is_none() {
                info.capabilities.push(Capability::Calc);
            }
            info.revision = history.applied as u64;
            info.dirty = history.dirty();
            info.sheets = opened.sheets;
            (handle, None)
        } else {
            let (handle, opened): (u64, Opened) = self.worker.open(current, password.clone()).await.map_err(|e| match e {
                WorkerError::Failed(m) if m.starts_with("password") => DocError::Password,
                WorkerError::Failed(m) => DocError::Unreadable(shown(), m),
                e => DocError::Worker(e),
            })?;
            // A password that opened it means it is encrypted, whatever PDFium makes of its security
            // handler (it may not tell an AES-256 one's revision): pdf.js needs it to show the pages.
            let encrypted = opened.encrypted || password.is_some();
            info.capabilities = vec![Capability::View];
            if opened.has_text {
                info.capabilities.extend([Capability::Text, Capability::Search]);
            }
            if !opened.outline.is_empty() {
                info.capabilities.push(Capability::Outline);
            }
            info.pages = Some(opened.pages);
            info.scanned = !opened.has_text && opened.pages > 0;
            info.outline = opened.outline;
            info.encrypted = encrypted;
            (handle, if encrypted { password } else { None })
        };
        Ok(Doc {
            id: id.to_string(),
            path: canonical.to_path_buf(),
            dir,
            kind,
            handle,
            password,
            info: Mutex::new(info),
            pages: Mutex::default(),
            views: Mutex::default(),
            history: Mutex::new(history),
            editing: tokio::sync::Mutex::new(()),
        })
    }

    /// A session joins a document: its token for the bytes (`herdoc://<token>/current.pdf`),
    /// kept as `<docs>/.tokens/<token>` holding the document's id, where the app looks it up.
    fn attach(&self, doc: &Doc, session: &str) -> Result<String, DocError> {
        if let Some(view) = doc.views.lock().get(session)
            && self.root.join(".tokens").join(&view.token).is_file()
        {
            return Ok(view.token.clone());
        }
        let token = uuid::Uuid::new_v4().simple().to_string();
        let tokens = self.root.join(".tokens");
        std::fs::create_dir_all(&tokens)?;
        std::fs::write(tokens.join(&token), &doc.id)?;
        doc.views.lock().insert(session.to_string(), View { page: 1, selection: None, token: token.clone() });
        Ok(token)
    }

    /// A session stops showing a document. Its state stays on disk; with no session left the
    /// worker lets it go.
    pub async fn close(&self, doc_id: &str, session: &str) -> Result<(), DocError> {
        let doc = self.get(doc_id)?;
        let (view, left) = {
            let mut views = doc.views.lock();
            (views.remove(session), views.len())
        };
        if let Some(view) = view {
            let _ = std::fs::remove_file(self.root.join(".tokens").join(view.token));
        }
        if left == 0 {
            self.docs.lock().remove(doc_id);
            self.worker.close(doc.handle).await;
        }
        Ok(())
    }

    /// A session is gone: it shows nothing any more.
    pub async fn forget_session(&self, session: &str) {
        let ids: Vec<String> = self.docs.lock().values().filter(|d| d.views.lock().contains_key(session)).map(|d| d.id.clone()).collect();
        for id in ids {
            let _ = self.close(&id, session).await;
        }
    }

    pub fn set_view(&self, doc_id: &str, session: &str, page: u32, selection: Option<Anchor>) -> Result<(), DocError> {
        let doc = self.get(doc_id)?;
        doc.check_page(page)?;
        let mut views = doc.views.lock();
        let view = views.get_mut(session).ok_or_else(|| DocError::NotOpen(doc_id.to_string()))?;
        view.page = page;
        view.selection = selection;
        Ok(())
    }

    /// What a session's column shows, for the context block sent with its messages (§7.4).
    pub fn viewing(&self, session: &str) -> Vec<DocViewing> {
        let mut out: Vec<DocViewing> = self
            .docs
            .lock()
            .values()
            .filter_map(|d| d.views.lock().get(session).map(|v| DocViewing { doc_id: d.id.clone(), path: d.path.display().to_string(), page: v.page }))
            .collect();
        out.sort_by(|a, b| a.path.cmp(&b.path));
        out
    }

    /// A page's text, read once and kept.
    pub async fn page(&self, doc: &Doc, page: u32) -> Result<Arc<PageText>, DocError> {
        doc.check_page(page)?;
        if let Some(text) = doc.pages.lock().get(&page) {
            return Ok(text.clone());
        }
        let chars: PageChars = self.worker.request(Request::Chars { handle: doc.handle, page }).await?;
        let blocks = layout::blocks(page, &chars);
        let text = Arc::new(PageText { chars, blocks });
        doc.pages.lock().insert(page, text.clone());
        Ok(text)
    }

    /// Up to `limit` hits of `query`, in page order.
    pub async fn find(&self, doc: &Doc, query: &str, limit: usize) -> Result<Vec<Hit>, DocError> {
        let mut hits = Vec::new();
        for page in 1..=doc.pages() {
            let text = self.page(doc, page).await?;
            for (a, b) in layout::find(&text.chars, query) {
                let block = layout::block_at(&text.blocks, &text.chars, a);
                let found: String = text.chars.chars[a..=b].iter().map(|c| c.c).collect();
                hits.push(Hit {
                    anchor: Anchor::Text { page, quads: layout::span_rects(&text.chars, a, b), text: found.trim().to_string(), block: block.map(|b| b.id.clone()) },
                    block: block.map(|b| b.id.clone()),
                    context: block.map(|b| context(&b.text, query)).unwrap_or_default(),
                });
                if hits.len() >= limit {
                    return Ok(hits);
                }
            }
        }
        Ok(hits)
    }

    /// A citation's target: `p12.b3`, a block, as a text anchor over its lines; `p12`, the page.
    pub async fn locate(&self, doc: &Doc, target: &str) -> Result<Anchor, DocError> {
        let bad = || DocError::NotOpen(format!("{} has no {target}", doc.id));
        let rest = target.trim().strip_prefix('p').ok_or_else(bad)?;
        let (page, block) = match rest.split_once('.') {
            Some((p, b)) => (p.parse::<u32>().map_err(|_| bad())?, Some(b)),
            None => (rest.parse::<u32>().map_err(|_| bad())?, None),
        };
        doc.check_page(page)?;
        let Some(_) = block else { return Ok(Anchor::Page { page }) };
        let text = self.page(doc, page).await?;
        let found = text.blocks.iter().find(|b| b.id == target.trim()).ok_or_else(bad)?;
        let taken = layout::take(&text.chars, &[found.rect]);
        Ok(Anchor::Text { page, quads: taken.lines, text: found.text.clone(), block: Some(found.id.clone()) })
    }

    /// Reads the text at a selection or in a region of a page (`doc/resolve`, §9.4.1).
    pub async fn resolve(&self, doc: &Doc, page: u32, quads: &[Rect], rect: Option<Rect>) -> Result<(Anchor, String, Vec<Rect>), DocError> {
        let text = self.page(doc, page).await?;
        let rects: Vec<Rect> = rect.into_iter().chain(quads.iter().copied()).collect();
        let taken = layout::take(&text.chars, &rects);
        let block = taken.first.and_then(|i| layout::block_at(&text.blocks, &text.chars, i)).map(|b| b.id.clone());
        let anchor = match rect {
            Some(rect) if quads.is_empty() => Anchor::Region { page, rect },
            _ => Anchor::Text { page, quads: taken.lines, text: taken.text.clone(), block },
        };
        Ok((anchor, taken.text, taken.boxes))
    }

    /// A page, or the part of it an anchor covers, as PNG.
    pub async fn render(&self, doc: &Doc, anchor: &Anchor, dpi: u32) -> Result<Rendered, DocError> {
        let page = anchor.page();
        doc.check_page(page)?;
        let clip = match anchor {
            Anchor::Page { .. } => None,
            Anchor::Region { rect, .. } => Some(*rect),
            // The lines, with a margin around them.
            Anchor::Text { quads, .. } => quads.iter().copied().reduce(|a, b| a.union(&b)).map(|r| Rect { x0: r.x0 - 24.0, y0: r.y0 - 24.0, x1: r.x1 + 24.0, y1: r.y1 + 24.0 }),
        };
        Ok(self.worker.request(Request::Render { handle: doc.handle, page, clip, dpi, max_side: MAX_RENDER_SIDE }).await?)
    }
}

/// Workbooks (spreadsheet-design §6, §7).
impl DocStore {
    fn sheet(doc: &Doc) -> Result<(), DocError> {
        if doc.is_sheet() { Ok(()) } else { Err(DocError::NotEditable(format!("{} is not a workbook", doc.path.display()))) }
    }

    /// A range's cells, as the grid shows them.
    pub async fn cells(&self, doc: &Doc, sheet: &str, range: &str) -> Result<Cells, DocError> {
        Self::sheet(doc)?;
        self.worker.request(crate::engine::Request::SheetCells { handle: doc.handle, sheet: sheet.into(), range: range.into() }).await.map_err(worker_error)
    }

    /// What the agent reads first (spreadsheet-design §9.1).
    pub async fn summary(&self, doc: &Doc) -> Result<String, DocError> {
        Self::sheet(doc)?;
        self.worker.request(Request::SheetSummary { handle: doc.handle }).await.map_err(worker_error)
    }

    pub async fn find_cells(&self, doc: &Doc, query: &str, limit: usize) -> Result<Vec<Found>, DocError> {
        Self::sheet(doc)?;
        self.worker.request(Request::SheetFind { handle: doc.handle, query: query.into(), limit }).await.map_err(worker_error)
    }

    pub async fn trace(&self, doc: &Doc, sheet: &str, cell: &str, direction: Direction, depth: u32) -> Result<Vec<Found>, DocError> {
        Self::sheet(doc)?;
        self.worker
            .request(Request::SheetTrace { handle: doc.handle, sheet: sheet.into(), cell: cell.into(), direction, depth })
            .await
            .map_err(worker_error)
    }

    /// The edits that fill `range` down or right (Excel's ⌘D, ⌘R), to apply as one revision.
    pub async fn fill_ops(&self, doc: &Doc, sheet: &str, range: &str, direction: crate::sheet::FillDirection) -> Result<Vec<SheetOp>, DocError> {
        Self::sheet(doc)?;
        self.worker
            .request(Request::SheetFill { handle: doc.handle, sheet: sheet.into(), range: range.into(), direction })
            .await
            .map_err(worker_error)
    }

    /// The edits the fill handle makes dragged from `source` to `target`, to apply as one revision.
    pub async fn autofill_ops(&self, doc: &Doc, sheet: &str, source: &str, target: &str, switch: bool) -> Result<Vec<SheetOp>, DocError> {
        Self::sheet(doc)?;
        self.worker
            .request(Request::SheetAutofill { handle: doc.handle, sheet: sheet.into(), source: source.into(), target: target.into(), switch })
            .await
            .map_err(worker_error)
    }

    async fn send(&self, doc: &Doc, ops: Vec<SheetOp>, preview: bool) -> Result<Applied, DocError> {
        self.worker.request(Request::SheetApply { handle: doc.handle, ops, preview }).await.map_err(worker_error)
    }

    fn record(&self, doc: &Doc) -> Result<(u64, bool), DocError> {
        let h = doc.history.lock();
        let mut lines = String::new();
        for r in &h.revisions[..h.applied] {
            lines.push_str(&serde_json::to_string(r).map_err(std::io::Error::other)?);
            lines.push('\n');
        }
        // The redo tail is kept in memory only: a restart starts from what is applied.
        atomic::write(&doc.dir.join("revisions.jsonl"), lines.as_bytes())?;
        let mut info = doc.info.lock();
        info.revision += 1;
        info.dirty = h.dirty();
        Ok((info.revision, info.dirty))
    }

    /// Edits a workbook (`preview`: only works out the impact). One revision per call.
    pub async fn apply(&self, doc: &Doc, session: &str, ops: Vec<SheetOp>, base: Option<u64>, preview: bool) -> Result<(u64, Impact, bool), DocError> {
        Self::sheet(doc)?;
        let _one = doc.editing.lock().await;
        let now = doc.info.lock().revision;
        if let Some(base) = base
            && base != now
        {
            return Err(DocError::Stale { base, now });
        }
        let applied = self.send(doc, ops.clone(), preview).await?;
        if preview {
            return Ok((now, applied.impact, doc.dirty()));
        }
        {
            let mut h = doc.history.lock();
            Self::drop_redo(doc, &mut h);
            h.revisions.push(Revision { ops, undo: applied.undo, structure: None, snapshot: None, session: session.to_string(), at_ms: now_ms() });
            h.applied += 1;
        }
        let (revision, dirty) = self.record(doc)?;
        self.changed(doc);
        Ok((revision, applied.impact, dirty))
    }

    /// Takes back the last edit, whoever made it, in whichever session.
    pub async fn undo(&self, doc: &Doc) -> Result<(u64, Impact, bool), DocError> {
        self.step(doc, Step::Undo).await
    }

    pub async fn redo(&self, doc: &Doc) -> Result<(u64, Impact, bool), DocError> {
        self.step(doc, Step::Redo).await
    }

    /// One step back or forward through the history: its ops sent (or, for rows or columns
    /// inserted or deleted, the change made again or the copy before it put back), and the
    /// history moved.
    async fn step(&self, doc: &Doc, step: Step) -> Result<(u64, Impact, bool), DocError> {
        Self::sheet(doc)?;
        let _one = doc.editing.lock().await;
        let revision = {
            let h = doc.history.lock();
            let found = match step {
                Step::Undo => h.applied.checked_sub(1).map(|i| h.revisions[i].clone()),
                Step::Redo => h.revisions.get(h.applied).cloned(),
            };
            found.ok_or_else(|| DocError::NotEditable(format!("nothing to {}", step.name())))?
        };
        let impact = match (&revision.structure, &revision.snapshot, step) {
            (Some(_), Some(snapshot), Step::Undo) => {
                atomic::copy(&doc.dir.join(snapshot), &doc.current())?;
                let opened: SheetOpened = self.worker.request(Request::SheetReload { handle: doc.handle }).await.map_err(worker_error)?;
                doc.info.lock().sheets = opened.sheets;
                Impact { converged: true, ..Default::default() }
            }
            (Some(change), _, Step::Redo) => self.reshape(doc, change).await?,
            _ => {
                let ops = match step {
                    Step::Undo => revision.undo,
                    Step::Redo => revision.ops,
                };
                self.send(doc, ops, false).await?.impact
            }
        };
        {
            let mut h = doc.history.lock();
            match step {
                Step::Undo => h.applied -= 1,
                Step::Redo => h.applied += 1,
            }
        }
        let (revision, dirty) = self.record(doc)?;
        self.changed(doc);
        Ok((revision, impact, dirty))
    }

    /// The edits that can no longer be redone, once a new one is made: gone, with their copies.
    fn drop_redo(doc: &Doc, h: &mut History) {
        let n = h.applied;
        for r in h.revisions.drain(n..) {
            if let Some(snapshot) = r.snapshot {
                let _ = std::fs::remove_file(doc.dir.join(snapshot));
            }
        }
        if h.saved.is_some_and(|s| s > n) {
            h.saved = None;
        }
    }

    /// Rows or columns inserted or deleted in the working copy; the sheets' shapes kept.
    async fn reshape(&self, doc: &Doc, change: &SheetStructure) -> Result<Impact, DocError> {
        let opened: SheetOpened = self.worker.request(Request::SheetStructure { handle: doc.handle, change: change.clone() }).await.map_err(worker_error)?;
        doc.info.lock().sheets = opened.sheets;
        Ok(Impact { converged: true, ..Default::default() })
    }

    /// Inserts or deletes rows or columns (Excel's 插入 / 删除) as one revision. The working
    /// copy before it is kept, so that undoing it gives back exactly what was deleted.
    pub async fn structure(&self, doc: &Doc, session: &str, change: SheetStructure, base: Option<u64>) -> Result<(u64, Impact, bool), DocError> {
        Self::sheet(doc)?;
        if doc.kind.read_only() {
            return Err(DocError::Refused("a legacy .xls is read only here: save it as .xlsx in your spreadsheet app".into()));
        }
        let _one = doc.editing.lock().await;
        let now = doc.info.lock().revision;
        if let Some(base) = base
            && base != now
        {
            return Err(DocError::Stale { base, now });
        }
        let snapshot = format!("snapshot-{}.{}", now_ms(), doc.kind.ext());
        atomic::copy(&doc.current(), &doc.dir.join(&snapshot))?;
        let impact = match self.reshape(doc, &change).await {
            Ok(impact) => impact,
            Err(e) => {
                let _ = std::fs::remove_file(doc.dir.join(&snapshot));
                return Err(e);
            }
        };
        {
            let mut h = doc.history.lock();
            Self::drop_redo(doc, &mut h);
            h.revisions.push(Revision { ops: Vec::new(), undo: Vec::new(), structure: Some(change), snapshot: Some(snapshot), session: session.to_string(), at_ms: now_ms() });
            h.applied += 1;
        }
        let (revision, dirty) = self.record(doc)?;
        self.changed(doc);
        Ok((revision, impact, dirty))
    }

    /// The edits that sort a range, to apply as one revision.
    pub async fn sort_ops(&self, doc: &Doc, sheet: &str, range: &str, by: &str, descending: bool, header: bool) -> Result<Vec<SheetOp>, DocError> {
        Self::sheet(doc)?;
        self.worker
            .request(Request::SheetSort { handle: doc.handle, sheet: sheet.into(), range: range.into(), by: by.into(), descending, header })
            .await
            .map_err(worker_error)
    }

    /// Reports discrepancies without changing the working copy, history or trust policy.
    pub async fn recalc(&self, doc: &Doc, circular_only: bool) -> Result<(u64, Impact, bool), DocError> {
        Self::sheet(doc)?;
        let _one = doc.editing.lock().await;
        let impact = self.worker.request(Request::SheetRecalc { handle: doc.handle, circular_only }).await.map_err(worker_error)?;
        Ok((doc.info().revision, impact, doc.dirty()))
    }

    /// Writes the working copy to its file, or to `to` (save as). Over a file that changed on
    /// disk since it was opened only with `force`.
    pub async fn save(&self, doc: &Doc, to: Option<&Path>, force: bool) -> Result<(DocInfo, Written), DocError> {
        Self::sheet(doc)?;
        if doc.kind.read_only() {
            return Err(DocError::Refused("a legacy .xls is read only here: save it as .xlsx in your spreadsheet app".into()));
        }
        let _one = doc.editing.lock().await;
        let target = to.map(Path::to_path_buf).unwrap_or_else(|| doc.path.clone());
        let ext = doc.kind.ext();
        if target.extension().and_then(|e| e.to_str()).is_none_or(|e| !e.eq_ignore_ascii_case(ext)) {
            return Err(DocError::Refused(format!("save as .{ext}; converting spreadsheet formats is not supported")));
        }
        let in_place = target == doc.path;
        if in_place && !force {
            let text = std::fs::read_to_string(doc.dir.join("meta.json"))?;
            let meta: Meta = serde_json::from_str(&text).map_err(std::io::Error::other)?;
            let disk = std::fs::metadata(&doc.path);
            let same = disk.as_ref().is_ok_and(|d| d.len() == meta.size && modified_ms(d) == meta.modified_ms);
            if !same {
                return Err(DocError::Conflict(doc.path.display().to_string()));
            }
        }
        // In place, the file already holds the state last saved, when that is still in the
        // history; anywhere else (or if not), it holds the file as opened.
        let written = {
            let h = doc.history.lock();
            let from = if in_place { h.saved.filter(|&n| n <= h.applied).unwrap_or(0) } else { 0 };
            let since = &h.revisions[from..h.applied];
            Written { reshaped: since.iter().filter(|r| r.structure.is_some()).count(), ..Written::of(since.iter().flat_map(|r| r.ops.iter())) }
        };
        // Never half written; the original's permissions carry over to the new file.
        atomic::copy(&doc.current(), &target)?;
        if in_place {
            atomic::copy(&doc.current(), &doc.dir.join(format!("base.{ext}")))?;
            let m = std::fs::metadata(&doc.path)?;
            let stamp = Meta { path: doc.path.clone(), size: m.len(), modified_ms: modified_ms(&m) };
            atomic::write(&doc.dir.join("meta.json"), &serde_json::to_vec_pretty(&stamp).map_err(std::io::Error::other)?)?;
            let mut h = doc.history.lock();
            h.saved = Some(h.applied);
            doc.info.lock().dirty = false;
        }
        self.changed(doc);
        Ok((doc.info(), written))
    }
}

#[derive(Clone, Copy)]
enum Step {
    Undo,
    Redo,
}

impl Step {
    fn name(self) -> &'static str {
        match self {
            Step::Undo => "undo",
            Step::Redo => "redo",
        }
    }
}

fn worker_error(e: WorkerError) -> DocError {
    match e {
        WorkerError::Failed(m) if m.starts_with(REFUSED) => DocError::Refused(m[REFUSED.len()..].to_string()),
        WorkerError::Failed(m) => DocError::NotEditable(m),
        e => DocError::Worker(e),
    }
}

fn now_ms() -> u128 {
    std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_millis()).unwrap_or(0)
}

fn modified_ms(meta: &std::fs::Metadata) -> u128 {
    meta.modified().ok().and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok()).map(|d| d.as_millis()).unwrap_or(0)
}

/// About 80 characters of `text` around the first loose match of `query`.
fn context(text: &str, query: &str) -> String {
    let chars: Vec<char> = text.chars().collect();
    let folded = layout::fold(chars.iter().copied().enumerate());
    let needle: Vec<char> = layout::fold(query.chars().enumerate()).into_iter().map(|(_, c)| c).collect();
    if needle.is_empty() {
        return String::new();
    }
    let at = (0..folded.len().saturating_sub(needle.len() - 1).max(1))
        .find(|&i| folded.len() >= i + needle.len() && folded[i..i + needle.len()].iter().map(|(_, c)| *c).eq(needle.iter().copied()))
        .map(|i| folded[i].0)
        .unwrap_or(0);
    let start = at.saturating_sub(40);
    let end = (at + 40 + query.chars().count()).min(chars.len());
    let mut out: String = chars[start..end].iter().collect();
    if start > 0 {
        out.insert(0, '…');
    }
    if end < chars.len() {
        out.push('…');
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_protocol::doc::CellInput;

    #[test]
    fn written_counts_what_the_edits_leave() {
        let value = |c: &str| SheetOp::SetValue { sheet: "S".into(), cell: c.into(), value: CellInput::Number(1.0) };
        let formula = |c: &str| SheetOp::SetFormula { sheet: "S".into(), cell: c.into(), formula: "=A1".into() };
        let ops = [
            value("B1"),
            formula("b1"), // the same cell, now a formula
            formula("C1"),
            value("D5"),
            SheetOp::Clear { sheet: "S".into(), range: "D1:D9".into() }, // takes D5 back
            value("E1"),
        ];
        assert_eq!(Written::of(ops.iter()), Written { formulas: 2, values: 1, cleared: 1, reshaped: 0 });
        assert_eq!(Written::of([].iter()), Written::default());
    }
}
