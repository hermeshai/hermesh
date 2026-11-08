//! In-memory session store for tests.

use parking_lot::Mutex;
use std::collections::HashMap;
use std::io;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;

use crate::store::CreatedSession;
use crate::store::LoadedSession;
use crate::store::SessionRef;
use crate::store::SessionStore;
use crate::store::SessionSummary;
use crate::store::StoreError;
use crate::store::entry::Entry;
use crate::store::entry::SessionHeader;
use crate::store::writer::LineSink;
use crate::store::writer::SessionWriter;

type Logs = Arc<Mutex<HashMap<String, (SessionHeader, Vec<String>)>>>;

#[derive(Clone, Default)]
pub struct MemoryStore {
    logs: Logs,
}

impl MemoryStore {
    pub fn new() -> Self {
        Self::default()
    }

    /// Entries written so far for a session.
    pub fn entries(&self, id: &str) -> Vec<Entry> {
        self.logs
            .lock()
            .get(id)
            .map(|(_, lines)| lines.iter().filter_map(|l| serde_json::from_str(l).ok()).collect())
            .unwrap_or_default()
    }
}

struct MemorySink {
    logs: Logs,
    id: String,
}

impl LineSink for MemorySink {
    fn write_line(&mut self, line: &str) -> io::Result<()> {
        if let Some((_, lines)) = self.logs.lock().get_mut(&self.id) {
            lines.push(line.to_string());
        }
        Ok(())
    }

    fn sync(&mut self) -> io::Result<()> {
        Ok(())
    }
}

impl SessionStore for MemoryStore {
    fn create(&self, header: &SessionHeader) -> Result<CreatedSession, StoreError> {
        self.logs.lock().insert(header.id.clone(), (header.clone(), Vec::new()));
        let writer = SessionWriter::spawn(MemorySink {
            logs: self.logs.clone(),
            id: header.id.clone(),
        })
        .map_err(|source| StoreError::Io {
            path: PathBuf::from("memory"),
            source,
        })?;
        Ok(CreatedSession {
            location: PathBuf::from(format!("memory://{}", header.id)),
            writer,
        })
    }

    fn header(&self, reference: &SessionRef) -> Result<(SessionHeader, PathBuf), StoreError> {
        let SessionRef::Id(id) = reference else {
            return Err(StoreError::NotFound("memory store only opens sessions by id".into()));
        };
        self.logs
            .lock()
            .get(id)
            .map(|(h, _)| (h.clone(), PathBuf::from(format!("memory://{id}"))))
            .ok_or_else(|| StoreError::NotFound(id.clone()))
    }

    fn open(&self, reference: &SessionRef) -> Result<LoadedSession, StoreError> {
        let SessionRef::Id(id) = reference else {
            return Err(StoreError::NotFound("memory store only opens sessions by id".into()));
        };
        let header = self
            .logs
            .lock()
            .get(id)
            .map(|(h, _)| h.clone())
            .ok_or_else(|| StoreError::NotFound(id.clone()))?;
        let entries = self.entries(id);
        let writer = SessionWriter::spawn(MemorySink {
            logs: self.logs.clone(),
            id: id.clone(),
        })
        .map_err(|source| StoreError::Io {
            path: PathBuf::from("memory"),
            source,
        })?;
        Ok(LoadedSession {
            header,
            entries,
            location: PathBuf::from(format!("memory://{id}")),
            writer,
            warnings: Vec::new(),
        })
    }

    fn read(&self, reference: &SessionRef) -> Result<(SessionHeader, Vec<Entry>), StoreError> {
        let (header, _) = self.header(reference)?;
        let entries = self.entries(&header.id);
        Ok((header, entries))
    }

    fn list(&self, limit: usize, within: Option<&Path>) -> Result<Vec<SessionSummary>, StoreError> {
        Ok(self
            .logs
            .lock()
            .values()
            .filter(|(header, _)| header.listed() && within.is_none_or(|dir| header.cwd.starts_with(dir)))
            .take(limit)
            .map(|(header, _)| SessionSummary {
                id: header.id.clone(),
                location: PathBuf::from(format!("memory://{}", header.id)),
                created_at: header.created_at.clone(),
                cwd: header.cwd.clone(),
                first_message: None,
                title: None,
                model: None,
                archived: false,
                last_task: None,
                last_task_at: None,
            })
            .collect())
    }
}
