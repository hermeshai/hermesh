//! Word documents held by the kernel (docs/desktop/word-design.md §4). An open .docx is an entry
//! like a deck: the same directory layout (with `source.docx`), log, recovery, save guard and
//! conflicts, on the deck worker's process, where a thread holds its `her_word::Word`.
//!
//! The deck worker writes the .docx itself (her-word `Word::save`, the engine's save ported to
//! Rust; word-native-save.md). The desktop app's serializer process, a [`DocxSerializer`] that
//! runs the engine's TypeScript save from the source, the Yrs state and the peer metadata, is
//! the fallback when the native save fails, and the reference in [`SaveEngine::Compare`]. The
//! kernel checks what was written and is the only one to replace the file.

use std::future::Future;
use std::path::{Path, PathBuf};
use std::pin::Pin;
use std::sync::Arc;

use parking_lot::Mutex;
use serde::de::DeserializeOwned;

use crate::deck::journal;
use crate::deck::{Changed, DeckEntry, DeckError, DeckEvent, DeckInfo, DeckStore, OpenedDeck, Request, Synced, WordChecked, WordEdited, WordQuery};

/// The author of the agent's revisions and comments.
pub const AUTHOR: &str = "Her";

/// Writes a .docx from its source, Yrs state and peer metadata (files), to `out`; the bytes
/// written, or why it could not.
pub trait DocxSerializer: Send + Sync {
    fn serialize<'a>(&'a self, source: &'a Path, state: &'a Path, metadata: &'a Path, out: &'a Path) -> Pin<Box<dyn Future<Output = Result<u64, String>> + Send + 'a>>;
}

/// Which writer saves Word documents (`HER_WORD_SAVE`: `native`, the default, `desktop` or
/// `compare`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SaveEngine {
    /// In the deck worker; the desktop's serializer only when that fails.
    Native,
    /// The desktop's serializer only (the kernel before the native save).
    Desktop,
    /// Both: the desktop's bytes are kept, and where the native ones differ it is logged.
    Compare,
}

impl SaveEngine {
    pub fn from_env() -> Self {
        match std::env::var("HER_WORD_SAVE").as_deref() {
            Ok("desktop") => SaveEngine::Desktop,
            Ok("compare") => SaveEngine::Compare,
            _ => SaveEngine::Native,
        }
    }
}

/// What listeners hear.
#[derive(Clone, Debug)]
pub enum WordEvent {
    /// After any edit, save or reload; `author` is the agent's name for its own edits.
    Changed { info: DeckInfo, author: Option<String> },
    /// An update merged from `session` (none: the kernel's own): every other replica should get it.
    Update { docx_id: String, session: Option<String>, update: Vec<u8>, revision: u64 },
}

type Listener = Box<dyn Fn(&WordEvent) + Send + Sync>;

pub struct WordStore {
    entries: DeckStore,
    serializer: Arc<dyn DocxSerializer>,
    engine: SaveEngine,
    listeners: Arc<Mutex<Vec<Listener>>>,
}

impl WordStore {
    /// A store keeping its documents in `root` (`$HER_HOME/docx`), on `decks`' worker process.
    pub fn new(root: PathBuf, decks: &DeckStore, serializer: Arc<dyn DocxSerializer>) -> Self {
        let entries = DeckStore::sharing(root, journal::WORD_SOURCE, decks);
        let listeners: Arc<Mutex<Vec<Listener>>> = Arc::default();
        let heard = listeners.clone();
        entries.on_event(move |event| {
            let event = match event {
                DeckEvent::Changed(info) => WordEvent::Changed { info: info.clone(), author: None },
                DeckEvent::Update { deck_id, session, update, revision } => {
                    WordEvent::Update { docx_id: deck_id.clone(), session: session.clone(), update: update.clone(), revision: *revision }
                }
            };
            for f in heard.lock().iter() {
                f(&event);
            }
        });
        Self { entries, serializer, engine: SaveEngine::from_env(), listeners }
    }

    /// The store saving with `engine` instead of what `HER_WORD_SAVE` says.
    pub fn with_engine(mut self, engine: SaveEngine) -> Self {
        self.engine = engine;
        self
    }

    pub fn on_event(&self, f: impl Fn(&WordEvent) + Send + Sync + 'static) {
        self.listeners.lock().push(Box::new(f));
    }

    fn emit(&self, event: WordEvent) {
        for f in self.listeners.lock().iter() {
            f(&event);
        }
    }

    pub fn get(&self, docx_id: &str) -> Result<Arc<DeckEntry>, DeckError> {
        self.entries.get(docx_id)
    }

    pub fn by_path(&self, path: &Path) -> Option<Arc<DeckEntry>> {
        self.entries.by_path(path)
    }

    /// Opens a .docx for a session, or joins its entry; as [`DeckStore::open`].
    pub async fn open(&self, path: &Path, session: &str, replica: bool) -> Result<OpenedDeck, DeckError> {
        if !path.extension().and_then(|e| e.to_str()).is_some_and(|e| e.eq_ignore_ascii_case("docx")) {
            return Err(DeckError::Refused(format!("{} is not a .docx document", path.display())));
        }
        self.entries.open(path, session, replica).await
    }

    /// As [`DeckStore::sync`].
    pub async fn sync(&self, docx_id: &str, session: Option<&str>, update: &[u8], state_vector: Option<&[u8]>) -> Result<(u64, Option<Vec<u8>>), DeckError> {
        self.entries.sync(docx_id, session, update, state_vector).await
    }

    /// Makes the agent's batch as one step, or nothing (`Refused`, naming the step).
    pub async fn apply(&self, docx_id: &str, batch: &her_word::EditBatch) -> Result<WordEdited, DeckError> {
        let entry = self.get(docx_id)?;
        let author = batch.author.clone();
        let batch = serde_json::to_value(batch).map_err(|e| DeckError::Worker(e.to_string()))?;
        let edited: WordEdited = self.entries.worker.request(Request::WordApply { deck: docx_id.to_owned(), batch }).await?;
        self.changed(&entry, &edited.changed, Some(&author))?;
        Ok(edited)
    }

    /// Accepts or rejects revisions (`None`: every pending one). Returns the revision after it.
    pub async fn review(&self, docx_id: &str, action: her_word::ReviewAction, ids: Option<Vec<u64>>, author: Option<&str>) -> Result<u64, DeckError> {
        let entry = self.get(docx_id)?;
        let changed: Changed = self.entries.worker.request(Request::WordReview { deck: docx_id.to_owned(), action, ids }).await?;
        self.changed(&entry, &changed, author)?;
        Ok(changed.revision)
    }

    /// As [`DeckStore::undo`]: the agent's latest step only.
    pub async fn undo(&self, docx_id: &str, edit: u64) -> Result<u64, DeckError> {
        self.entries.undo(docx_id, edit).await
    }

    /// Reads the document as it is now; `T` is the her-word type `query` gives.
    pub async fn read<T: DeserializeOwned>(&self, docx_id: &str, query: WordQuery) -> Result<T, DeckError> {
        self.get(docx_id)?;
        Ok(self.entries.worker.request(Request::WordRead { deck: docx_id.to_owned(), query }).await?)
    }

    /// Saves to the file, or to `to`, with the save guard of decks: a staged file is written
    /// ([`SaveEngine`]), the engine checks it opens, then it replaces the target.
    pub async fn save(&self, docx_id: &str, to: Option<&Path>, force: bool) -> Result<DeckInfo, DeckError> {
        let entry = self.get(docx_id)?;
        self.entries
            .save_with(docx_id, to, force, "docx", |staged| async move {
                let checked = self.stage(&entry, &staged).await?;
                Ok((checked.0, checked.1.hash))
            })
            .await
    }

    /// The .docx as it would be saved now, without writing the file: for drawing pages.
    pub async fn current_bytes(&self, docx_id: &str) -> Result<Vec<u8>, DeckError> {
        let entry = self.get(docx_id)?;
        if !entry.info().dirty {
            return Ok(std::fs::read(&entry.path)?);
        }
        let peek = entry.dir.join(format!("peek-{}.docx", uuid::Uuid::new_v4()));
        let staged = self.stage(&entry, &peek).await;
        let bytes = staged.and_then(|_| Ok(std::fs::read(&peek)?));
        let _ = std::fs::remove_file(&peek);
        bytes
    }

    /// Writes the document now to `out` and checks it: the revision written.
    async fn stage(&self, entry: &DeckEntry, out: &Path) -> Result<(u64, WordChecked), DeckError> {
        let revision = match self.engine {
            SaveEngine::Native => match self.write_native(entry, out).await {
                Ok(revision) => revision,
                Err(native) => {
                    tracing::warn!(docx = %entry.deck_id, error = %native, "native Word save failed, the desktop app writes it");
                    self.write_desktop(entry, out).await.map_err(|desktop| match desktop {
                        DeckError::Host(host) => DeckError::Host(format!("{native}; {host}")),
                        other => other,
                    })?
                }
            },
            SaveEngine::Desktop => self.write_desktop(entry, out).await?,
            SaveEngine::Compare => {
                let revision = self.write_desktop(entry, out).await?;
                let native = entry.dir.join(format!("native-{}.docx", uuid::Uuid::new_v4()));
                let compared = self.write_native(entry, &native).await.and_then(|_| {
                    let theirs = std::fs::read(out)?;
                    let ours = std::fs::read(&native)?;
                    Ok(her_word::package_difference(&theirs, &ours))
                });
                let _ = std::fs::remove_file(&native);
                match compared {
                    Ok(None) => tracing::info!(docx = %entry.deck_id, revision, "native Word save matches the desktop's"),
                    Ok(Some(difference)) => tracing::warn!(docx = %entry.deck_id, revision, %difference, "native Word save differs from the desktop's"),
                    Err(e) => tracing::warn!(docx = %entry.deck_id, revision, error = %e, "native Word save failed"),
                }
                revision
            }
        };
        let checked: WordChecked = self.entries.worker.request(Request::WordCheck { deck: entry.deck_id.clone(), path: out.to_owned() }).await?;
        Ok((revision, checked))
    }

    /// The deck worker writes the document to `out`: the revision written.
    async fn write_native(&self, entry: &DeckEntry, out: &Path) -> Result<u64, DeckError> {
        let synced: Synced = self.entries.worker.request(Request::WordWrite { deck: entry.deck_id.clone(), path: out.to_owned() }).await?;
        Ok(synced.revision)
    }

    /// The desktop's serializer writes the document to `out`: the revision written.
    async fn write_desktop(&self, entry: &DeckEntry, out: &Path) -> Result<u64, DeckError> {
        let tag = uuid::Uuid::new_v4();
        let state = entry.dir.join(format!("state-{tag}.yupd"));
        let metadata = entry.dir.join(format!("metadata-{tag}.bin"));
        let result = async {
            let synced: Synced = self.entries.worker.request(Request::WordState { deck: entry.deck_id.clone(), state: state.clone(), metadata: metadata.clone() }).await?;
            self.serializer.serialize(&entry.source(), &state, &metadata, out).await.map_err(DeckError::Host)?;
            Ok(synced.revision)
        }
        .await;
        let _ = std::fs::remove_file(&state);
        let _ = std::fs::remove_file(&metadata);
        result
    }

    fn changed(&self, entry: &DeckEntry, changed: &Changed, author: Option<&str>) -> Result<(), DeckError> {
        if !changed.update.is_empty() {
            *entry.revision.lock() = changed.revision;
            let update = crate::deck::decode(&changed.update)?;
            self.emit(WordEvent::Update { docx_id: entry.deck_id.clone(), session: None, update, revision: changed.revision });
        }
        self.emit(WordEvent::Changed { info: entry.info(), author: author.map(str::to_owned) });
        Ok(())
    }

    pub async fn discard_path(&self, path: &Path) -> Result<(), DeckError> {
        self.entries.discard_path(path).await
    }

    pub async fn close(&self, docx_id: &str, session: &str) -> Result<(), DeckError> {
        self.entries.close(docx_id, session).await
    }

    pub async fn forget_session(&self, session: &str) {
        self.entries.forget_session(session).await
    }

    pub fn is_quarantined(&self, docx_id: &str) -> bool {
        self.entries.is_quarantined(docx_id)
    }

    /// Ends the worker process, as a crash would. For fault-injection tests.
    #[doc(hidden)]
    pub async fn kill_worker(&self) {
        self.entries.kill_worker().await;
    }
}
