//! Presentations held by the kernel (docs/desktop/presentation-kernel-authority.md).
//!
//! DeckStore keeps one entry per open file, shared by every session that opens it. The deck
//! itself lives in `her deck-worker`; its directory `<root>/<deck_id>/` holds:
//!
//! - `source.pptx`: the file as first opened. Replicas join from these bytes, and the update log
//!   and snapshot build on them, so they stay as they are across saves: a save does not restart
//!   the Yrs history, and an editor's undo history survives it.
//! - `updates.log`, `snapshot.yupd`, `engine.txt`: see [`journal`].
//! - `meta.json`: the file's path, the hash, size and time of the file as last read or written,
//!   and the revision that holds.

mod actor;
mod client;
pub mod journal;
mod wire;

use std::collections::{HashMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::Arc;

use base64::Engine as _;
use base64::engine::general_purpose::STANDARD as B64;
use parking_lot::Mutex;
use serde::{Deserialize, Serialize};

pub use actor::serve;
pub use client::{DeckLauncher, QUARANTINED};
use client::DeckWorker;
pub use wire::{Edited, Opened, StripEntry, Summary, WordChecked, WordEdited, WordQuery};
pub(crate) use wire::{Changed, Request, Synced};
use wire::{Bytes, Saved};

use her_protocol::deck::DeckDiagnostic;

use crate::atomic;
use crate::worker::WorkerError;

/// The engine build replicas must match (`DeckOpenResult.engine`).
pub const ENGINE: &str = her_slide::ENGINE;

/// Larger files are refused (PD-K3: up from the 20 MiB of `pptx/read`).
pub const MAX_DECK_BYTES: u64 = 200 * 1024 * 1024;

#[derive(Debug, thiserror::Error)]
pub enum DeckError {
    #[error("not found: {0}")]
    NotFound(String),
    #[error("too large: {0}")]
    TooLarge(String),
    #[error("not open: {0}")]
    NotOpen(String),
    #[error("refused: {0}")]
    Refused(String),
    /// The agent's edit cannot be taken back (a later one is in the deck, or it is gone from
    /// the deck's history); the deck is as it was.
    #[error("cannot undo: {0}")]
    NotUndoable(String),
    /// The file changed on disk since it was read: nothing was written.
    #[error("changed on disk: {0}")]
    Conflict(String),
    /// The file is gone from disk: nothing was written.
    #[error("deleted from disk: {0}")]
    Deleted(String),
    /// A malformed update or state vector; the deck is as it was.
    #[error("{0}")]
    Protocol(String),
    /// The desktop app's serializer could not write a .docx (or there is none).
    #[error("{0}")]
    Host(String),
    #[error("{0}")]
    Worker(String),
    #[error(transparent)]
    Io(#[from] std::io::Error),
}

impl From<WorkerError> for DeckError {
    fn from(e: WorkerError) -> Self {
        match e {
            WorkerError::Failed(m) if m.starts_with("protocol: ") => DeckError::Protocol(m),
            WorkerError::Failed(m) if m.starts_with(actor::NOT_UNDOABLE) => DeckError::NotUndoable(m[actor::NOT_UNDOABLE.len()..].to_owned()),
            WorkerError::Failed(m) if m.starts_with(actor::REFUSED) => DeckError::Refused(m[actor::REFUSED.len()..].to_owned()),
            e => DeckError::Worker(e.to_string()),
        }
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
struct Meta {
    path: PathBuf,
    hash: String,
    size: u64,
    modified_ms: u128,
    /// The revision the file on disk holds.
    saved_revision: u64,
}

/// What a deck's entry looks like now.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DeckInfo {
    pub deck_id: String,
    pub path: PathBuf,
    /// Of the file as last read or written.
    pub hash: String,
    pub revision: u64,
    pub dirty: bool,
    pub writable: bool,
    /// The file changed on disk under unsaved edits.
    pub conflict: bool,
}

/// One open file.
pub struct DeckEntry {
    pub deck_id: String,
    pub path: PathBuf,
    pub dir: PathBuf,
    pub writable: bool,
    /// `source.pptx`, or `source.docx` for a Word document.
    source: &'static str,
    sessions: Mutex<HashSet<String>>,
    meta: Mutex<Meta>,
    pub(crate) revision: Mutex<u64>,
    conflict: Mutex<bool>,
    /// One save at a time.
    saving: tokio::sync::Mutex<()>,
}

impl DeckEntry {
    pub fn info(&self) -> DeckInfo {
        let meta = self.meta.lock();
        let revision = *self.revision.lock();
        DeckInfo {
            deck_id: self.deck_id.clone(),
            path: self.path.clone(),
            hash: meta.hash.clone(),
            revision,
            dirty: revision != meta.saved_revision,
            writable: self.writable,
            conflict: *self.conflict.lock(),
        }
    }

    pub fn source(&self) -> PathBuf {
        self.dir.join(self.source)
    }
}

/// What listeners hear.
#[derive(Clone, Debug)]
pub enum DeckEvent {
    /// After any edit, save or reload.
    Changed(DeckInfo),
    /// An update merged from `session`: every other replica of the deck should get it.
    Update { deck_id: String, session: Option<String>, update: Vec<u8>, revision: u64 },
}

type Listener = Box<dyn Fn(&DeckEvent) + Send + Sync>;

/// The result of `open`.
pub struct OpenedDeck {
    pub entry: Arc<DeckEntry>,
    /// The whole Yrs state, for a replica.
    pub state: Option<Vec<u8>>,
    /// Unsaved edits of an earlier run came back from the log.
    pub recovered: bool,
    /// A log the engine could not replay safely was kept under this suffix.
    pub set_aside: Option<String>,
}

pub struct DeckStore {
    root: PathBuf,
    /// The name of an entry's source in its directory: what the worker opens it as.
    source: &'static str,
    pub(crate) worker: DeckWorker,
    decks: Mutex<HashMap<String, Arc<DeckEntry>>>,
    opening: tokio::sync::Mutex<()>,
    listeners: Mutex<Vec<Listener>>,
}

impl DeckStore {
    /// A store keeping its decks in `root` (`$HER_HOME/decks`).
    pub fn new(root: PathBuf, launcher: DeckLauncher) -> Self {
        Self::with_worker(root, journal::SOURCE, DeckWorker::new(launcher))
    }

    /// A store of another kind of document in `root`, on `decks`' worker: a Word document is an
    /// entry like a deck, with `source` its source's name (word-design §4).
    pub(crate) fn sharing(root: PathBuf, source: &'static str, decks: &DeckStore) -> Self {
        Self::with_worker(root, source, decks.worker.clone())
    }

    fn with_worker(root: PathBuf, source: &'static str, worker: DeckWorker) -> Self {
        Self { root, source, worker, decks: Mutex::default(), opening: tokio::sync::Mutex::new(()), listeners: Mutex::default() }
    }

    pub fn on_event(&self, f: impl Fn(&DeckEvent) + Send + Sync + 'static) {
        self.listeners.lock().push(Box::new(f));
    }

    pub(crate) fn emit(&self, event: DeckEvent) {
        for f in self.listeners.lock().iter() {
            f(&event);
        }
    }

    pub fn get(&self, deck_id: &str) -> Result<Arc<DeckEntry>, DeckError> {
        self.decks.lock().get(deck_id).cloned().ok_or_else(|| DeckError::NotOpen(deck_id.to_owned()))
    }

    /// The open entry of a file, if any.
    pub fn by_path(&self, path: &Path) -> Option<Arc<DeckEntry>> {
        let canonical = std::fs::canonicalize(path).ok()?;
        self.decks.lock().get(&crate::store::doc_id(&canonical)).cloned()
    }

    /// Opens a file for a session, or joins the entry another session opened. A directory left
    /// by an earlier run with unsaved edits is recovered: the edits come back, dirty; if the
    /// file changed on disk meanwhile, the entry is in conflict.
    pub async fn open(&self, path: &Path, session: &str, replica: bool) -> Result<OpenedDeck, DeckError> {
        let _one = self.opening.lock().await;
        let canonical = std::fs::canonicalize(path).map_err(|_| DeckError::NotFound(path.display().to_string()))?;
        let id = crate::store::doc_id(&canonical);
        let joined = self.decks.lock().get(&id).cloned();
        if let Some(entry) = joined {
            entry.sessions.lock().insert(session.to_owned());
            let state = if replica { Some(self.state(&entry).await?) } else { None };
            return Ok(OpenedDeck { entry, state, recovered: false, set_aside: None });
        }
        let (disk, bytes) = read_file(&canonical)?;
        let dir = self.root.join(&id);
        let mut meta = read_meta(&dir).filter(|m| m.path == canonical && dir.join(self.source).is_file());
        let mut conflict = false;
        if let Some(m) = &meta {
            let leftover = dir.join(journal::LOG).metadata().is_ok_and(|l| l.len() > 0) || dir.join(journal::SNAPSHOT).exists();
            let same = m.hash == disk.hash;
            match (leftover, same) {
                // Nothing to recover, or the file moved on: start over from the file.
                (false, _) => meta = None,
                (true, true) => {}
                (true, false) => conflict = true,
            }
        }
        if meta.is_none() {
            let _ = std::fs::remove_dir_all(&dir);
            std::fs::create_dir_all(&dir)?;
            atomic::write(&dir.join(self.source), &bytes)?;
            let fresh = Meta { path: canonical.clone(), ..disk.clone() };
            write_meta(&dir, &fresh)?;
            meta = Some(fresh);
        }
        let meta = meta.expect("set above");
        let opened = match self.worker.open(&id, dir.clone()).await {
            Ok(o) => o,
            Err(e) => return Err(e.into()),
        };
        let dirty = opened.revision != meta.saved_revision;
        if conflict && !dirty {
            // The file moved on and nothing here is unsaved: start over from the file.
            self.worker.close(&id).await;
            let _ = std::fs::remove_dir_all(&dir);
            drop(_one);
            return Box::pin(self.open(path, session, replica)).await;
        }
        let entry = Arc::new(DeckEntry {
            deck_id: id.clone(),
            path: canonical.clone(),
            dir,
            writable: is_writable(&canonical),
            source: self.source,
            sessions: Mutex::new(HashSet::from([session.to_owned()])),
            meta: Mutex::new(meta),
            revision: Mutex::new(opened.revision),
            conflict: Mutex::new(conflict),
            saving: tokio::sync::Mutex::new(()),
        });
        self.decks.lock().insert(id, entry.clone());
        let state = if replica { Some(self.state(&entry).await?) } else { None };
        Ok(OpenedDeck { entry, state, recovered: opened.recovered, set_aside: opened.set_aside })
    }

    pub(crate) async fn state(&self, entry: &DeckEntry) -> Result<Vec<u8>, DeckError> {
        let bytes: Bytes = self.worker.request(Request::State { deck: entry.deck_id.clone() }).await?;
        decode(&bytes.data)
    }

    /// Merges a replica's update (if any) and returns the revision, with what a replica at
    /// `state_vector` lacks when one is given.
    pub async fn sync(&self, deck_id: &str, session: Option<&str>, update: &[u8], state_vector: Option<&[u8]>) -> Result<(u64, Option<Vec<u8>>), DeckError> {
        let entry = self.get(deck_id)?;
        if !update.is_empty() {
            let synced: Synced = self.worker.request(Request::Sync { deck: deck_id.to_owned(), update: B64.encode(update) }).await?;
            *entry.revision.lock() = synced.revision;
            self.emit(DeckEvent::Update { deck_id: deck_id.to_owned(), session: session.map(str::to_owned), update: update.to_vec(), revision: synced.revision });
            self.emit(DeckEvent::Changed(entry.info()));
        }
        let missing = match state_vector {
            Some(sv) => {
                let bytes: Bytes = self.worker.request(Request::Diff { deck: deck_id.to_owned(), state_vector: B64.encode(sv) }).await?;
                Some(decode(&bytes.data)?)
            }
            None => None,
        };
        Ok((*entry.revision.lock(), missing))
    }

    /// Saves the deck to its file, or to `to`. In place, a file that changed on disk since it
    /// was read is not written over unless `force`; nothing unsaved writes nothing.
    pub async fn save(&self, deck_id: &str, to: Option<&Path>, force: bool) -> Result<DeckInfo, DeckError> {
        self.save_with(deck_id, to, force, "pptx", |staged| async move {
            let saved: Saved = self.worker.request(Request::Save { deck: deck_id.to_owned(), to: staged }).await?;
            Ok((saved.revision, saved.hash))
        })
        .await
    }

    /// The save guard and the write around `stage`, which writes the document to the path it is
    /// given and returns the revision and hash of what it wrote.
    pub(crate) async fn save_with<F, Fut>(&self, deck_id: &str, to: Option<&Path>, force: bool, extension: &str, stage: F) -> Result<DeckInfo, DeckError>
    where
        F: FnOnce(PathBuf) -> Fut,
        Fut: std::future::Future<Output = Result<(u64, String), DeckError>>,
    {
        let entry = self.get(deck_id)?;
        let _one = entry.saving.lock().await;
        let target = to.map(Path::to_path_buf).unwrap_or_else(|| entry.path.clone());
        if target.extension().and_then(|e| e.to_str()).is_none_or(|e| !e.eq_ignore_ascii_case(extension)) {
            return Err(DeckError::Refused(format!("save as .{extension}")));
        }
        let in_place = target == entry.path;
        if in_place {
            if !entry.writable {
                return Err(DeckError::Refused(format!("{} is read only", entry.path.display())));
            }
            let meta = entry.meta.lock().clone();
            if !force {
                match std::fs::metadata(&entry.path) {
                    Err(_) => return Err(DeckError::Deleted(entry.path.display().to_string())),
                    Ok(m) if m.len() != meta.size || modified_ms(&m) != meta.modified_ms => {
                        // Size and time first; the hash settles a touch that changed nothing.
                        let (disk, _) = read_file(&entry.path)?;
                        if disk.hash != meta.hash {
                            *entry.conflict.lock() = true;
                            self.emit(DeckEvent::Changed(entry.info()));
                            return Err(DeckError::Conflict(entry.path.display().to_string()));
                        }
                    }
                    Ok(_) => {}
                }
            }
            if !entry.info().dirty && !*entry.conflict.lock() {
                return Ok(entry.info());
            }
        }
        let staged = entry.dir.join(format!("saving.{extension}"));
        let staging = stage(staged.clone()).await;
        let (revision, hash) = match staging {
            Ok(saved) => saved,
            Err(e) => {
                let _ = std::fs::remove_file(&staged);
                return Err(e);
            }
        };
        atomic::copy(&staged, &target)?;
        let _ = std::fs::remove_file(&staged);
        if in_place {
            let m = std::fs::metadata(&target)?;
            let meta = Meta { path: entry.path.clone(), hash, size: m.len(), modified_ms: modified_ms(&m), saved_revision: revision };
            write_meta(&entry.dir, &meta)?;
            *entry.meta.lock() = meta;
            *entry.conflict.lock() = false;
            self.emit(DeckEvent::Changed(entry.info()));
        }
        Ok(entry.info())
    }

    /// Makes the agent's edits, the engine's `ProposalEdit`s (JSON), at once: one revision,
    /// sent to every replica, and one step the agent's [`DeckStore::undo`] takes back. A batch
    /// with a step that fails changes nothing (`Refused`, naming the step).
    pub async fn apply(&self, deck_id: &str, agent_id: &str, note: Option<String>, edits: serde_json::Value) -> Result<Edited, DeckError> {
        let entry = self.get(deck_id)?;
        let edited: Edited = self.worker.request(Request::Apply { deck: deck_id.to_owned(), agent_id: agent_id.to_owned(), note, edits }).await?;
        self.changed(&entry, &edited.changed)?;
        Ok(edited)
    }

    /// Takes back the agent's edit that made revision `edit`, when it is the agent's latest
    /// one in the deck; the person's edits stay. Returns the revision after it.
    pub async fn undo(&self, deck_id: &str, edit: u64) -> Result<u64, DeckError> {
        let entry = self.get(deck_id)?;
        let changed: Changed = self.worker.request(Request::Undo { deck: deck_id.to_owned(), edit }).await?;
        self.changed(&entry, &changed)?;
        Ok(changed.revision)
    }

    /// The deterministic checks of the deck now.
    pub async fn check(&self, deck_id: &str) -> Result<Vec<DeckDiagnostic>, DeckError> {
        self.get(deck_id)?;
        Ok(self.worker.request(Request::Check { deck: deck_id.to_owned() }).await?)
    }

    /// The engine's snapshot of the deck now, unsaved edits included: the ids edits address.
    pub async fn snapshot(&self, deck_id: &str) -> Result<her_slide::DeckSnapshot, DeckError> {
        self.get(deck_id)?;
        Ok(self.worker.request(Request::Snapshot { deck: deck_id.to_owned() }).await?)
    }

    /// After a change of the worker's own: its update to every replica, the state to all.
    pub(crate) fn changed(&self, entry: &DeckEntry, changed: &Changed) -> Result<(), DeckError> {
        if !changed.update.is_empty() {
            *entry.revision.lock() = changed.revision;
            self.emit(DeckEvent::Update { deck_id: entry.deck_id.clone(), session: None, update: decode(&changed.update)?, revision: changed.revision });
        }
        self.emit(DeckEvent::Changed(entry.info()));
        Ok(())
    }

    /// Slide lines and size of the deck as it is now, unsaved edits included.
    pub async fn summary(&self, deck_id: &str) -> Result<Summary, DeckError> {
        self.get(deck_id)?;
        Ok(self.worker.request(Request::Summary { deck: deck_id.to_owned() }).await?)
    }

    /// The package as it would be saved now, without writing the file: what tools read, so
    /// they see unsaved edits. `None` when no entry holds the file.
    pub async fn current_bytes(&self, path: &Path) -> Result<Option<Vec<u8>>, DeckError> {
        let Some(entry) = self.by_path(path) else { return Ok(None) };
        if !entry.info().dirty {
            // Nothing unsaved: the file holds it.
            return Ok(Some(std::fs::read(&entry.path)?));
        }
        let peek = entry.dir.join(format!("peek-{}.pptx", uuid::Uuid::new_v4()));
        let _: Saved = self.worker.request(Request::Save { deck: entry.deck_id.clone(), to: peek.clone() }).await?;
        let bytes = std::fs::read(&peek);
        let _ = std::fs::remove_file(&peek);
        Ok(Some(bytes?))
    }

    /// Drops a file's unsaved edits, open or left by an earlier run: the next open reads it anew.
    pub async fn discard_path(&self, path: &Path) -> Result<(), DeckError> {
        let canonical = std::fs::canonicalize(path).map_err(|_| DeckError::NotFound(path.display().to_string()))?;
        let id = crate::store::doc_id(&canonical);
        if self.decks.lock().contains_key(&id) {
            return self.discard(&id).await;
        }
        let _ = std::fs::remove_dir_all(self.root.join(&id));
        Ok(())
    }

    /// Drops the unsaved edits and the entry's directory; the next open reads the file anew.
    pub async fn discard(&self, deck_id: &str) -> Result<(), DeckError> {
        let entry = self.decks.lock().remove(deck_id).ok_or_else(|| DeckError::NotOpen(deck_id.to_owned()))?;
        self.worker.close(deck_id).await;
        let _ = std::fs::remove_dir_all(&entry.dir);
        Ok(())
    }

    /// A session lets go. When no session holds the entry it is closed; unsaved edits stay in
    /// its directory, to come back on the next open.
    pub async fn close(&self, deck_id: &str, session: &str) -> Result<(), DeckError> {
        let entry = self.get(deck_id)?;
        let last = {
            let mut sessions = entry.sessions.lock();
            sessions.remove(session);
            sessions.is_empty()
        };
        if last {
            self.decks.lock().remove(deck_id);
            self.worker.close(deck_id).await;
            if !entry.info().dirty {
                let _ = std::fs::remove_dir_all(&entry.dir);
            }
        }
        Ok(())
    }

    /// Every session's hold on its entries ends.
    pub async fn forget_session(&self, session: &str) {
        let held: Vec<String> = self.decks.lock().values().filter(|e| e.sessions.lock().contains(session)).map(|e| e.deck_id.clone()).collect();
        for id in held {
            let _ = self.close(&id, session).await;
        }
    }

    pub fn is_quarantined(&self, deck_id: &str) -> bool {
        self.worker.is_quarantined(deck_id)
    }

    /// Ends the worker process, as a crash would. For fault-injection tests.
    #[doc(hidden)]
    pub async fn kill_worker(&self) {
        self.worker.exit().await;
    }
}

pub(crate) fn decode(data: &str) -> Result<Vec<u8>, DeckError> {
    B64.decode(data).map_err(|e| DeckError::Worker(format!("bad reply: {e}")))
}

pub(crate) fn sha256_hex(bytes: &[u8]) -> String {
    ring::digest::digest(&ring::digest::SHA256, bytes).as_ref().iter().map(|b| format!("{b:02x}")).collect()
}

fn read_file(path: &Path) -> Result<(Meta, Vec<u8>), DeckError> {
    let m = std::fs::metadata(path).map_err(|_| DeckError::NotFound(path.display().to_string()))?;
    if !m.is_file() {
        return Err(DeckError::NotFound(path.display().to_string()));
    }
    if m.len() > MAX_DECK_BYTES {
        return Err(DeckError::TooLarge(path.display().to_string()));
    }
    let bytes = std::fs::read(path)?;
    let meta = Meta { path: path.to_owned(), hash: sha256_hex(&bytes), size: m.len(), modified_ms: modified_ms(&m), saved_revision: 0 };
    Ok((meta, bytes))
}

fn read_meta(dir: &Path) -> Option<Meta> {
    serde_json::from_slice(&std::fs::read(dir.join("meta.json")).ok()?).ok()
}

fn write_meta(dir: &Path, meta: &Meta) -> std::io::Result<()> {
    atomic::write(&dir.join("meta.json"), &serde_json::to_vec_pretty(meta).map_err(std::io::Error::other)?)
}

fn modified_ms(m: &std::fs::Metadata) -> u128 {
    m.modified().ok().and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok()).map_or(0, |d| d.as_millis())
}

fn is_writable(path: &Path) -> bool {
    std::fs::metadata(path).is_ok_and(|m| !m.permissions().readonly())
}
