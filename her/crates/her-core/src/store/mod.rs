//! Session persistence: an append-only log per session, where entries form a tree through
//! their `parent` field.

mod durable;
pub mod entry;
pub mod jsonl;
pub mod memory;
pub mod writer;

use her_llm::Timestamp;
use std::io;
use std::path::Path;
use std::path::PathBuf;

pub use entry::AgentOrigin;
pub use entry::AgentSpawnedEntry;
pub use entry::BotRef;
pub use entry::CardResolvedEntry;
pub use entry::CompactionEntry;
pub use entry::SystemPromptEntry;
pub use entry::Entry;
pub use entry::EntryKind;
pub use entry::ExpandedSkill;
pub use entry::SkillText;
pub use entry::SessionHeader;
pub use entry::TaskStatus;
pub use jsonl::JsonlStore;
pub use memory::MemoryStore;
pub use writer::SessionWriter;
pub use writer::WriterClosed;

#[derive(Debug, thiserror::Error)]
pub enum StoreError {
    #[error("{path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: io::Error,
    },
    #[error("session not found: {0}")]
    NotFound(String),
    #[error("{0}")]
    Ambiguous(String),
    #[error("this session is open in another her or Hermesh ({0})")]
    Locked(PathBuf),
    #[error("corrupt session log {path}: {message}")]
    Corrupt { path: PathBuf, message: String },
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum SessionRef {
    /// The most recent session.
    Last,
    /// The most recent session started in this directory or below it (a project).
    LastIn(PathBuf),
    /// A session id, or a unique prefix or suffix of one (the short id is a suffix).
    Id(String),
    Path(PathBuf),
}

/// Display form of a session id: its last 8 characters. Session ids are UUID v7, whose
/// leading characters are a timestamp and repeat for sessions started close together.
pub fn short_id(id: &str) -> &str {
    let start = id.len().saturating_sub(8);
    id.get(start..).unwrap_or(id)
}

pub struct CreatedSession {
    pub location: PathBuf,
    pub writer: SessionWriter,
}

pub struct LoadedSession {
    pub header: SessionHeader,
    pub entries: Vec<Entry>,
    pub location: PathBuf,
    pub writer: SessionWriter,
    pub warnings: Vec<String>,
}

#[derive(Clone, Debug, serde::Serialize, serde::Deserialize)]
pub struct SessionSummary {
    pub id: String,
    pub location: PathBuf,
    pub created_at: String,
    pub cwd: PathBuf,
    pub first_message: Option<String>,
    pub title: Option<String>,
    /// The model of the last settings entry.
    pub model: Option<String>,
    pub archived: bool,
    /// The status of the last task entry.
    pub last_task: Option<TaskStatus>,
    /// When the last task entry was written (a task starting or ending): when the session last
    /// did something. Opening, renaming or archiving it does not count.
    pub last_task_at: Option<Timestamp>,
}

pub trait SessionStore: Send + Sync + 'static {
    fn create(&self, header: &SessionHeader) -> Result<CreatedSession, StoreError>;
    /// Reads only the header, without taking the writer lock.
    fn header(&self, reference: &SessionRef) -> Result<(SessionHeader, PathBuf), StoreError>;
    fn open(&self, reference: &SessionRef) -> Result<LoadedSession, StoreError>;
    /// Reads the whole log without taking the writer lock, to look at a session that is not
    /// opened here or that another holder may be writing.
    fn read(&self, reference: &SessionRef) -> Result<(SessionHeader, Vec<Entry>), StoreError>;
    /// Most recent first; with `within`, only sessions started in that directory or below.
    /// Sub-agents' and bots' sessions are left out; sessions started by this store whose log
    /// is not written yet are included.
    fn list(&self, limit: usize, within: Option<&Path>) -> Result<Vec<SessionSummary>, StoreError>;
}
