//! Session logs as JSONL files: `sessions/YYYY/MM/DD/<timestamp>-<id>.jsonl`.

use parking_lot::Mutex;
use std::collections::HashMap;
use std::fs::File;
use std::fs::OpenOptions;
use std::io;
use std::io::BufRead;
use std::io::Read;
use std::io::Seek;
use std::io::SeekFrom;
use std::io::Write;
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
use crate::store::entry::EntryKind;
use crate::store::entry::SessionHeader;
use crate::store::writer::LineSink;
use crate::store::writer::SessionWriter;

#[path = "summary_cache.rs"]
mod summary_cache;

pub struct JsonlStore {
    dir: PathBuf,
    unwritten: Unwritten,
    scanned: Mutex<HashMap<PathBuf, Scanned>>,
    /// Logs shorter than this are read in full each time instead of kept in `scanned`.
    keep_from: u64,
}

/// What `list` learned from one log. Logs only grow, so the next call reads just the lines
/// appended since: a service listing long sessions again and again otherwise rereads every
/// log in full each time. Only long logs are kept: a short one costs little more to read
/// again than to keep (about 1 KiB each).
#[derive(Clone, serde::Serialize, serde::Deserialize)]
struct Scanned {
    /// The file as it was when read; another file or a change other than growth means a
    /// full read.
    len: u64,
    modified: Option<std::time::SystemTime>,
    inode: u64,
    /// The end of the last complete line read.
    offset: u64,
    /// A log session lists leave out: a sub-agent's or a bot's.
    #[serde(alias = "agent")]
    hidden: bool,
    /// The summary from the lines up to `offset`.
    summary: SessionSummary,
}

/// Sessions started by this store whose log file is not written yet, by the path it will
/// have. They are listed with the others.
type Unwritten = Arc<Mutex<HashMap<PathBuf, SessionHeader>>>;

impl JsonlStore {
    pub fn new(dir: impl Into<PathBuf>) -> Self {
        Self {
            dir: dir.into(),
            unwritten: Unwritten::default(),
            scanned: Mutex::default(),
            keep_from: 64 * 1024,
        }
    }

    fn all_files(&self) -> Vec<PathBuf> {
        let mut out = Vec::new();
        collect_jsonl(&self.dir, &mut out, 0);
        // Directories and file names start with the date and time, so this is chronological.
        out.sort();
        out
    }

    fn find(&self, reference: &SessionRef) -> Result<PathBuf, StoreError> {
        match reference {
            SessionRef::Path(path) => Ok(path.clone()),
            // Sub-agents' and bots' sessions are never "the last session".
            SessionRef::Last => self
                .all_files()
                .into_iter()
                .rev()
                .find(|path| read_header(path).is_some_and(|h| h.listed()))
                .ok_or_else(|| StoreError::NotFound("no sessions yet".into())),
            SessionRef::LastIn(dir) => self
                .all_files()
                .into_iter()
                .rev()
                .find(|path| read_header(path).is_some_and(|h| h.listed() && h.cwd.starts_with(dir)))
                .ok_or_else(|| StoreError::NotFound(format!("no session started in {} yet", dir.display()))),
            SessionRef::Id(id) => {
                let matches: Vec<PathBuf> = self
                    .all_files()
                    .into_iter()
                    .filter(|path| {
                        session_id_of(path).is_some_and(|sid| sid.starts_with(id.as_str()) || sid.ends_with(id.as_str()))
                    })
                    .collect();
                match matches.len() {
                    0 => Err(StoreError::NotFound(format!("no session matches '{id}'"))),
                    1 => Ok(matches.into_iter().next().unwrap_or_default()),
                    n => Err(StoreError::Ambiguous(format!("'{id}' matches {n} sessions; use more characters"))),
                }
            }
        }
    }
}

fn collect_jsonl(dir: &Path, out: &mut Vec<PathBuf>, depth: usize) {
    if depth > 4 {
        return;
    }
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        // The entry's type comes with the directory listing; asking the path costs a stat,
        // which only a symbolic link needs.
        let is_dir = match entry.file_type() {
            Ok(kind) if !kind.is_symlink() => kind.is_dir(),
            _ => path.is_dir(),
        };
        if is_dir {
            // Logs sit in YYYY/MM/DD; directories next to them hold the sessions' files.
            if depth < 3 {
                collect_jsonl(&path, out, depth + 1);
            }
        } else if path.extension().is_some_and(|e| e == "jsonl") {
            out.push(path);
        }
    }
}

/// Reads only the first line.
fn read_header(path: &Path) -> Option<SessionHeader> {
    let mut line = String::new();
    std::io::BufReader::new(File::open(path).ok()?).read_line(&mut line).ok()?;
    serde_json::from_str(&line).ok()
}

/// A log's header and entries; lines that cannot be read are skipped with a warning.
fn parse_log(path: &Path, text: &str, warnings: &mut Vec<String>) -> Result<(SessionHeader, Vec<Entry>), StoreError> {
    let mut lines = text.lines();
    let header_line = lines.next().ok_or_else(|| StoreError::Corrupt {
        path: path.to_path_buf(),
        message: "empty file".into(),
    })?;
    let header: SessionHeader = serde_json::from_str(header_line).map_err(|e| StoreError::Corrupt {
        path: path.to_path_buf(),
        message: format!("bad header: {e}"),
    })?;
    let mut entries = Vec::new();
    for (number, line) in lines.enumerate() {
        if line.trim().is_empty() {
            continue;
        }
        match serde_json::from_str::<Entry>(line) {
            Ok(entry) => entries.push(entry),
            Err(err) => warnings.push(format!("skipped unreadable line {}: {err}", number + 2)),
        }
    }
    Ok((header, entries))
}

/// `20261028T090000-<id>.jsonl` → `<id>`
fn session_id_of(path: &Path) -> Option<&str> {
    let stem = path.file_stem()?.to_str()?;
    stem.split_once('-').map(|(_, id)| id)
}

fn io_err(path: &Path) -> impl FnOnce(io::Error) -> StoreError + '_ {
    move |source| StoreError::Io {
        path: path.to_path_buf(),
        source,
    }
}

fn lock(file: &File, path: &Path) -> Result<(), StoreError> {
    match file.try_lock() {
        Ok(()) => Ok(()),
        Err(std::fs::TryLockError::WouldBlock) => Err(StoreError::Locked(path.to_path_buf())),
        Err(std::fs::TryLockError::Error(source)) => Err(StoreError::Io {
            path: path.to_path_buf(),
            source,
        }),
    }
}

#[cfg(unix)]
fn private(options: &mut OpenOptions) -> &mut OpenOptions {
    use std::os::unix::fs::OpenOptionsExt;
    options.mode(0o600)
}

#[cfg(not(unix))]
fn private(options: &mut OpenOptions) -> &mut OpenOptions {
    options
}

/// The log file. A new session's file is created by the writer on its first line, so that
/// starting a session does no file I/O of its own.
enum FileSink {
    Pending {
        path: PathBuf,
        header: String,
        unwritten: Unwritten,
    },
    Open(File),
}

impl Drop for FileSink {
    /// The file now exists, or never will: either way the store stops listing it as unwritten.
    fn drop(&mut self) {
        if let FileSink::Pending { path, unwritten, .. } = self {
            unwritten.lock().remove(path);
        }
    }
}

impl FileSink {
    fn file(&mut self) -> io::Result<&mut File> {
        if let FileSink::Pending { path, header, .. } = self {
            let file = create_log(path, header)?;
            *self = FileSink::Open(file);
        }
        match self {
            FileSink::Open(file) => Ok(file),
            FileSink::Pending { .. } => unreachable!("opened above"),
        }
    }
}

/// Creates a session log with its header line, locked against other writers.
fn create_log(path: &Path, header: &str) -> io::Result<File> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir)?;
    }
    let mut file = private(OpenOptions::new().create_new(true).append(true)).open(path)?;
    file.try_lock().map_err(|err| match err {
        std::fs::TryLockError::WouldBlock => {
            io::Error::new(io::ErrorKind::WouldBlock, format!("{} is locked by another process", path.display()))
        }
        std::fs::TryLockError::Error(err) => err,
    })?;
    file.write_all(format!("{header}\n").as_bytes())?;
    Ok(file)
}

impl LineSink for FileSink {
    fn write_line(&mut self, line: &str) -> io::Result<()> {
        let mut buf = Vec::with_capacity(line.len() + 1);
        buf.extend_from_slice(line.as_bytes());
        buf.push(b'\n');
        self.file()?.write_all(&buf)
    }

    fn write_lines(&mut self, lines: &[String]) -> io::Result<()> {
        let mut buf = Vec::with_capacity(lines.iter().map(|l| l.len() + 1).sum());
        for line in lines {
            buf.extend_from_slice(line.as_bytes());
            buf.push(b'\n');
        }
        self.file()?.write_all(&buf)
    }

    /// Also creates the file of a session that wrote nothing, so that every session has one.
    fn sync(&mut self) -> io::Result<()> {
        super::durable::sync(self.file()?)
    }

    fn is_new(&self) -> bool {
        matches!(self, FileSink::Pending { .. })
    }
}

impl SessionStore for JsonlStore {
    fn create(&self, header: &SessionHeader) -> Result<CreatedSession, StoreError> {
        let now = chrono::Local::now();
        let dir = self.dir.join(now.format("%Y/%m/%d").to_string());
        let path = dir.join(format!("{}-{}.jsonl", now.format("%Y%m%dT%H%M%S"), header.id));
        let line = serde_json::to_string(header).map_err(|e| StoreError::Corrupt {
            path: path.clone(),
            message: e.to_string(),
        })?;
        self.unwritten.lock().insert(path.clone(), header.clone());
        let sink = FileSink::Pending {
            path: path.clone(),
            header: line,
            unwritten: self.unwritten.clone(),
        };
        let writer = SessionWriter::spawn(sink).map_err(io_err(&path))?;
        Ok(CreatedSession { location: path, writer })
    }

    fn header(&self, reference: &SessionRef) -> Result<(SessionHeader, PathBuf), StoreError> {
        let path = self.find(reference)?;
        let text = std::fs::read_to_string(&path).map_err(io_err(&path))?;
        let line = text.lines().next().unwrap_or_default();
        let header = serde_json::from_str(line).map_err(|e| StoreError::Corrupt {
            path: path.clone(),
            message: format!("bad header: {e}"),
        })?;
        Ok((header, path))
    }

    fn open(&self, reference: &SessionRef) -> Result<LoadedSession, StoreError> {
        let path = self.find(reference)?;
        let mut file = OpenOptions::new()
            .read(true)
            .append(true)
            .open(&path)
            .map_err(io_err(&path))?;
        lock(&file, &path)?;
        let mut text = String::new();
        file.read_to_string(&mut text).map_err(io_err(&path))?;
        let mut warnings = Vec::new();

        // A crash can leave a half-written last line; drop it so new lines start cleanly.
        if !text.is_empty() && !text.ends_with('\n') {
            let keep = text.rfind('\n').map(|i| i + 1).unwrap_or(0);
            file.set_len(keep as u64).map_err(io_err(&path))?;
            text.truncate(keep);
            warnings.push("dropped an incomplete last line from the session log".to_string());
        }

        let (header, entries) = parse_log(&path, &text, &mut warnings)?;
        let writer = SessionWriter::spawn(FileSink::Open(file)).map_err(io_err(&path))?;
        Ok(LoadedSession {
            header,
            entries,
            location: path,
            writer,
            warnings,
        })
    }

    fn read(&self, reference: &SessionRef) -> Result<(SessionHeader, Vec<Entry>), StoreError> {
        let path = self.find(reference)?;
        let mut text = std::fs::read_to_string(&path).map_err(io_err(&path))?;
        // A line its writer has not finished yet.
        text.truncate(text.rfind('\n').map_or(0, |i| i + 1));
        parse_log(&path, &text, &mut Vec::new())
    }

    fn list(&self, limit: usize, within: Option<&Path>) -> Result<Vec<SessionSummary>, StoreError> {
        // Taken before the directory is read: a log written in between is then found on disk.
        let unwritten = self.unwritten.lock().clone();
        let mut paths = self.all_files();
        // Their paths have the same layout, so they sort among the others by creation time.
        paths.extend(unwritten.keys().cloned());
        paths.sort();
        paths.dedup();
        let mut out = Vec::new();
        for path in paths.iter().rev().cloned() {
            if out.len() == limit {
                break;
            }
            if let Some(header) = unwritten.get(&path)
                && !path.exists()
            {
                if header.listed() && within.is_none_or(|dir| header.cwd.starts_with(dir)) {
                    out.push(SessionSummary {
                        id: header.id.clone(),
                        location: path,
                        created_at: header.created_at.clone(),
                        cwd: header.cwd.clone(),
                        first_message: None,
                        title: None,
                        model: None,
                        archived: false,
                        last_task: None,
                        last_task_at: None,
                    });
                }
                continue;
            }
            // Sub-agents' sessions are opened through their parent, bots' through their bot: not
            // listed.
            if let Some((false, summary)) = self.scan(&path)
                && within.is_none_or(|dir| summary.cwd.starts_with(dir))
            {
                out.push(summary);
            }
        }
        // Forget logs that are gone.
        let present: std::collections::HashSet<&PathBuf> = paths.iter().collect();
        self.scanned.lock().retain(|path, _| present.contains(path));
        Ok(out)
    }
}

impl JsonlStore {
    /// One session's summary, as `list` would give it.
    pub fn summary(&self, reference: &SessionRef) -> Result<SessionSummary, StoreError> {
        let path = self.find(reference)?;
        self.scan(&path).map(|(_, summary)| summary).ok_or_else(|| StoreError::Corrupt {
            path: path.clone(),
            message: "unreadable header".into(),
        })
    }

    /// A log's summary and whether session lists leave it out, reading only what was appended
    /// since the last call. The same as reading the whole log, as long as logs only grow.
    fn scan(&self, path: &Path) -> Option<(bool, SessionSummary)> {
        let meta = std::fs::metadata(path).ok()?;
        let (len, modified, inode) = (meta.len(), meta.modified().ok(), inode(&meta));
        let cached = self.scanned.lock().get(path).cloned();
        let cached = cached.or_else(|| {
            if len < self.keep_from || !path.starts_with(&self.dir) { return None; }
            let cached = summary_cache::load(path, &meta)?;
            self.scanned.lock().insert(path.to_path_buf(), cached.clone());
            Some(cached)
        });
        let mut state = match cached.filter(|_| len >= self.keep_from) {
            Some(c) if c.inode == inode && c.len == len && c.modified == modified => return Some((c.hidden, c.summary)),
            Some(c) if c.inode == inode && c.len < len => Some(c),
            _ => None,
        };
        let mut file = File::open(path).ok()?;
        let mut bytes = Vec::new();
        if let Some(c) = &state {
            // The cached part must still end a line where it did.
            let mut before = [0u8; 1];
            let intact = c.offset > 0
                && file.seek(SeekFrom::Start(c.offset - 1)).is_ok()
                && file.read_exact(&mut before).is_ok()
                && before[0] == b'\n';
            if !intact {
                state = None;
                file.seek(SeekFrom::Start(0)).ok()?;
            }
        }
        file.read_to_end(&mut bytes).ok()?;
        // Invalid UTF-8 anywhere makes the log unreadable, as a full read would.
        let text = std::str::from_utf8(&bytes).ok()?;
        let complete = text.rfind('\n').map_or(0, |i| i + 1);
        let (done, tail) = text.split_at(complete);
        let mut c = match state {
            Some(c) => c,
            None => {
                let mut lines = text.lines();
                let header = serde_json::from_str::<SessionHeader>(lines.next()?).ok()?;
                let hidden = !header.listed();
                let header_len = text.find('\n').map_or(text.len(), |i| i + 1);
                let mut summary = SessionSummary {
                    id: header.id,
                    location: path.to_path_buf(),
                    created_at: header.created_at,
                    cwd: header.cwd,
                    first_message: None,
                    title: None,
                    model: None,
                    archived: false,
                    last_task: None,
                    last_task_at: None,
                };
                if header_len > complete {
                    // Only a header, without its line end: nothing to keep.
                    return Some((hidden, summary));
                }
                for line in done[header_len..].lines() {
                    summarize_line(&mut summary, line);
                }
                let scanned = Scanned {
                    len,
                    modified,
                    inode,
                    offset: complete as u64,
                    hidden,
                    summary,
                };
                return Some(self.keep(path, scanned, tail, &meta));
            }
        };
        for line in done.lines() {
            summarize_line(&mut c.summary, line);
        }
        c.offset += complete as u64;
        c.len = len;
        c.modified = modified;
        Some(self.keep(path, c, tail, &meta))
    }

    /// Caches what was read; a half-written last line counts for this answer only, and is
    /// read again next time.
    fn keep(&self, path: &Path, mut scanned: Scanned, tail: &str, meta: &std::fs::Metadata) -> (bool, SessionSummary) {
        if !tail.is_empty() {
            scanned.len = scanned.offset;
        }
        let hidden = scanned.hidden;
        let mut summary = scanned.summary.clone();
        if scanned.offset >= self.keep_from {
            // A process restarting can reuse an unchanged large log's summary. Partial
            // records and concurrent writes are never persisted as a complete projection.
            if tail.is_empty() && path.starts_with(&self.dir) {
                summary_cache::save(path, &scanned, meta);
            }
            self.scanned.lock().insert(path.to_path_buf(), scanned);
        } else {
            self.scanned.lock().remove(path);
        }
        for line in tail.lines() {
            summarize_line(&mut summary, line);
        }
        (hidden, summary)
    }

    /// Moves the log of a session no process has open, and its files, into `trash`. Returns the
    /// session's id; `Locked` while a process has it open.
    pub fn move_to_trash(&self, reference: &SessionRef, trash: &Path) -> Result<String, StoreError> {
        let path = self.find(reference)?;
        let (header, _) = self.header(reference)?;
        let file = File::open(&path).map_err(io_err(&path))?;
        lock(&file, &path)?;
        std::fs::create_dir_all(trash).map_err(io_err(trash))?;
        let name = path.file_name().map_or_else(|| PathBuf::from(format!("{}.jsonl", header.id)), PathBuf::from);
        // The session's files go with its log.
        let (data_dir, _) = crate::paths::HerHome::session_data_dir(&path, &header.id);
        std::fs::rename(&path, trash.join(&name)).map_err(io_err(&path))?;
        if data_dir.is_dir() {
            std::fs::rename(&data_dir, trash.join(name.with_extension(""))).map_err(io_err(&data_dir))?;
        }
        Ok(header.id)
    }

    /// Appends entries to the log of a session no process has open: after its last entry,
    /// under the log's lock. Returns `Locked` while a process has it open.
    pub fn append(&self, reference: &SessionRef, kinds: Vec<EntryKind>) -> Result<(), StoreError> {
        let path = self.find(reference)?;
        let mut file = OpenOptions::new()
            .read(true)
            .append(true)
            .open(&path)
            .map_err(io_err(&path))?;
        lock(&file, &path)?;
        let mut text = String::new();
        file.read_to_string(&mut text).map_err(io_err(&path))?;
        // A crash can leave a half-written last line; start on a line of our own.
        let mut out = if !text.is_empty() && !text.ends_with('\n') { "\n".to_string() } else { String::new() };
        let mut parent = text
            .lines()
            .rev()
            .skip_while(|line| line.trim().is_empty())
            .skip(usize::from(!text.ends_with('\n')))
            .find_map(|line| serde_json::from_str::<Entry>(line).ok())
            .map(|entry| entry.id);
        for kind in kinds {
            let id = format!("{:08x}", rand::random::<u32>());
            let entry = Entry {
                id: id.clone(),
                parent: parent.replace(id),
                ts: now_ms(),
                kind,
            };
            let line = serde_json::to_string(&entry).map_err(|e| StoreError::Corrupt {
                path: path.clone(),
                message: e.to_string(),
            })?;
            out.push_str(&line);
            out.push('\n');
        }
        file.write_all(out.as_bytes()).map_err(io_err(&path))?;
        super::durable::sync(&file).map_err(io_err(&path))
    }
}

fn now_ms() -> her_llm::Timestamp {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as her_llm::Timestamp)
        .unwrap_or_default()
}

/// The entry type of a log line: the first `"type"` field, which is the entry's own (the
/// header and entries serialize it before any nested object).
fn entry_type(line: &str) -> Option<&str> {
    let start = line.find(r#""type":""#)? + r#""type":""#.len();
    let rest = &line[start..];
    Some(&rest[..rest.find('"')?])
}

/// Adds one log line to a session's summary. Only the few entry types a list needs are
/// parsed: most lines of a long session are model output and tool results, and parsing them
/// dominated the listing.
fn summarize_line(summary: &mut SessionSummary, line: &str) {
    let wanted = match entry_type(line) {
        Some("message") => summary.first_message.is_none() && line.contains(r#""role":"user""#),
        Some("title" | "settings" | "task" | "archived" | "branch") => true,
        _ => false,
    };
    if !wanted {
        return;
    }
    let Ok(entry) = serde_json::from_str::<Entry>(line) else {
        return;
    };
    match entry.kind {
        // As the user wrote it: phase 2 logs hold a skill's instructions in the message.
        EntryKind::Message {
            message: her_llm::Message::User(user),
            skill,
            ..
        } => summary.first_message = Some(skill.map_or_else(|| user.text_content(), |skill| skill.input)),
        EntryKind::Title { title } => summary.title = Some(title),
        EntryKind::Settings { model: Some(model), .. } => summary.model = Some(model),
        EntryKind::Task { status, .. } => {
            summary.last_task = Some(status);
            summary.last_task_at = Some(entry.ts);
        }
        EntryKind::Archived { archived } => summary.archived = archived,
        // The session went back before its first message: the next one is the first.
        EntryKind::Branch { before_first: true } => summary.first_message = None,
        _ => {}
    }
}

#[cfg(unix)]
fn inode(meta: &std::fs::Metadata) -> u64 {
    std::os::unix::fs::MetadataExt::ino(meta)
}

#[cfg(not(unix))]
fn inode(_meta: &std::fs::Metadata) -> u64 {
    0
}

#[cfg(test)]
mod tests {
    use super::*;

    const OLDER: &str = "0193f0c2-7c1e-7d3a-9b1f-4c2d8e6a5b10";
    const NEWER: &str = "0193f0c2-7c1e-7d3a-9b1f-4c2d8e6a5b11";

    fn ids(store: &JsonlStore, limit: usize, within: Option<&Path>) -> Vec<String> {
        store.list(limit, within).unwrap().into_iter().map(|s| s.id).collect()
    }

    #[tokio::test]
    async fn sessions_are_listed_before_their_log_is_written() {
        let dir = tempfile::tempdir().unwrap();
        let store = JsonlStore::new(dir.path().join("sessions"));
        let work = dir.path().join("work");
        let older = store.create(&SessionHeader::new(OLDER.into(), work.clone())).unwrap();
        older.writer.sync().await.unwrap();
        assert!(older.location.exists());
        let newer = store.create(&SessionHeader::new(NEWER.into(), work.join("sub"))).unwrap();
        assert!(!newer.location.exists());

        let listed = store.list(10, None).unwrap();
        assert_eq!(listed.iter().map(|s| s.id.as_str()).collect::<Vec<_>>(), [NEWER, OLDER]);
        assert_eq!(listed[0].location, newer.location);
        assert_eq!(listed[0].cwd, work.join("sub"));
        assert!(listed[0].first_message.is_none());
        assert_eq!(ids(&store, 1, None), [NEWER]);
        assert_eq!(ids(&store, 10, Some(&work.join("sub"))), [NEWER]);
        assert!(ids(&store, 10, Some(&dir.path().join("elsewhere"))).is_empty());

        // Closing writes the log; it is then listed once, from disk.
        let closed = newer.writer.closed();
        drop(newer.writer);
        closed.wait().await;
        assert!(newer.location.exists());
        assert!(store.unwritten.lock().is_empty());
        assert_eq!(ids(&store, 10, None), [NEWER, OLDER]);
    }

    /// A bot's session is opened through its bot: never listed, never "the last session",
    /// written or not (bot-design §2.2).
    #[tokio::test]
    async fn bot_sessions_stay_out_of_lists() {
        let dir = tempfile::tempdir().unwrap();
        let store = JsonlStore::new(dir.path().join("sessions"));
        let work = dir.path().join("work");
        let plain = store.create(&SessionHeader::new(OLDER.into(), work.clone())).unwrap();
        plain.writer.sync().await.unwrap();
        let mut header = SessionHeader::new(NEWER.into(), work.clone());
        header.bot = Some(super::super::BotRef {
            id: "b1".into(),
            name: "HerBot".into(),
        });
        let bot = store.create(&header).unwrap();
        assert_eq!(ids(&store, 10, None), [OLDER], "not written yet");
        bot.writer.sync().await.unwrap();
        for store in [&store, &JsonlStore::new(dir.path().join("sessions"))] {
            assert_eq!(ids(store, 10, None), [OLDER]);
            assert_eq!(ids(store, 10, Some(&work)), [OLDER]);
            assert_eq!(store.header(&SessionRef::Last).unwrap().0.id, OLDER);
            assert_eq!(store.header(&SessionRef::LastIn(work.clone())).unwrap().0.id, OLDER);
            // Still found by its id.
            assert_eq!(store.header(&SessionRef::Id(NEWER.into())).unwrap().0.bot, header.bot);
        }
    }

    #[test]
    fn summaries_follow_appends_like_a_fresh_read() {
        use crate::store::entry::TaskStatus;
        let dir = tempfile::tempdir().unwrap();
        let sessions = dir.path().join("sessions");
        let day = sessions.join("2026/10/29");
        std::fs::create_dir_all(&day).unwrap();
        let path = day.join(format!("20261029T000000-{OLDER}.jsonl"));
        let header = serde_json::to_string(&SessionHeader::new(OLDER.into(), dir.path().join("work"))).unwrap();
        let line = |kind: EntryKind| {
            let entry = Entry {
                id: format!("{:08x}", rand::random::<u32>()),
                parent: None,
                ts: 7,
                kind,
            };
            serde_json::to_string(&entry).unwrap() + "\n"
        };
        let task = |status| EntryKind::Task {
            task_id: "t1".into(),
            status,
            error: None,
            reason: None,
            usage: None,
        };
        let mut store = JsonlStore::new(&sessions);
        // Keep every log, however short.
        store.keep_from = 0;
        let check = |store: &JsonlStore| {
            let fresh = JsonlStore::new(&sessions);
            let reference = SessionRef::Path(path.clone());
            let (kept, read) = (store.summary(&reference).unwrap(), fresh.summary(&reference).unwrap());
            assert_eq!(format!("{kept:?}"), format!("{read:?}"));
            assert_eq!(format!("{:?}", store.list(10, None).unwrap()), format!("{:?}", fresh.list(10, None).unwrap()));
            kept
        };
        let append = |text: &str| {
            OpenOptions::new().append(true).open(&path).unwrap().write_all(text.as_bytes()).unwrap();
        };

        std::fs::write(&path, format!("{header}\n")).unwrap();
        assert!(check(&store).first_message.is_none());
        append(&line(EntryKind::message(her_llm::UserMessage::text("first question").into())));
        append(&line(task(TaskStatus::Started)));
        assert_eq!(check(&store).first_message.as_deref(), Some("first question"));
        append(&line(EntryKind::Title { title: "Named".into() }));
        append(&line(EntryKind::message(her_llm::UserMessage::text("second question").into())));
        let summary = check(&store);
        assert_eq!((summary.title.as_deref(), summary.last_task), (Some("Named"), Some(TaskStatus::Started)));

        // A line being written: the end of the task, in two parts.
        let end = line(task(TaskStatus::Completed));
        let (first, rest) = end.split_at(end.len() / 2);
        append(first);
        assert_eq!(check(&store).last_task, Some(TaskStatus::Started));
        append(rest);
        assert_eq!(check(&store).last_task, Some(TaskStatus::Completed));
        // The whole last line, without its line end.
        append(line(EntryKind::Archived { archived: true }).trim_end());
        assert!(check(&store).archived);
        append("\n");
        append(&line(EntryKind::Title { title: "Renamed".into() }));
        assert_eq!(check(&store).title.as_deref(), Some("Renamed"));

        // Another file at the same path is read from the start.
        std::fs::remove_file(&path).unwrap();
        std::fs::write(&path, format!("{header}\n{}", line(EntryKind::Title { title: "Other".into() }))).unwrap();
        let summary = check(&store);
        assert_eq!((summary.title.as_deref(), summary.last_task, summary.archived), (Some("Other"), None, false));

        assert!(store.scanned.lock().contains_key(&path));

        // A deleted log is no longer listed or kept.
        std::fs::remove_file(&path).unwrap();
        assert!(store.list(10, None).unwrap().is_empty());
        assert!(store.scanned.lock().is_empty());

        // Short logs are read each time, not kept.
        std::fs::write(&path, format!("{header}\n{}", line(EntryKind::Title { title: "Short".into() }))).unwrap();
        let store = JsonlStore::new(&sessions);
        assert_eq!(store.list(10, None).unwrap()[0].title.as_deref(), Some("Short"));
        assert!(store.scanned.lock().is_empty());
    }

    /// Going back before the first message: the list shows the message that took its place, on
    /// a full read and on one that goes on from a kept summary. Going back to a later message
    /// leaves the first one alone.
    #[test]
    fn the_first_message_follows_a_rewind_before_it() {
        let dir = tempfile::tempdir().unwrap();
        let log = dir.path().join(format!("20261105T000000-{OLDER}.jsonl"));
        let header = serde_json::to_string(&SessionHeader::new(OLDER.into(), dir.path().into())).unwrap();
        let line = |kind: EntryKind| {
            let entry = Entry { id: format!("{:08x}", rand::random::<u32>()), parent: None, ts: 1, kind };
            serde_json::to_string(&entry).unwrap() + "\n"
        };
        let message = |text: &str| line(EntryKind::message(her_llm::UserMessage::text(text).into()));
        let append = |text: &str| OpenOptions::new().append(true).open(&log).unwrap().write_all(text.as_bytes()).unwrap();
        let mut kept = JsonlStore::new(dir.path());
        kept.keep_from = 0;
        let first = || {
            let reference = SessionRef::Path(log.clone());
            let fresh = JsonlStore::new(dir.path()).summary(&reference).unwrap().first_message;
            assert_eq!(kept.summary(&reference).unwrap().first_message, fresh, "kept and fresh agree");
            fresh
        };
        std::fs::write(&log, format!("{header}\n{}", message("the old first"))).unwrap();
        assert_eq!(first().as_deref(), Some("the old first"));
        append(&message("a second one"));
        append(&line(EntryKind::Branch { before_first: false }));
        assert_eq!(first().as_deref(), Some("the old first"), "back to a later message");
        append(&line(EntryKind::Branch { before_first: true }));
        assert_eq!(first(), None, "nothing has taken its place yet");
        append(&message("the new first"));
        assert_eq!(first().as_deref(), Some("the new first"));
        append(&message("then another"));
        assert_eq!(first().as_deref(), Some("the new first"));
    }

    #[test]
    #[cfg(unix)]
    fn large_summary_survives_process_restart_and_invalidates_on_every_file_change() {
        let dir = tempfile::tempdir().unwrap();
        let log = dir.path().join("20261103T000000-cached.jsonl");
        let header = serde_json::to_string(&SessionHeader::new("cached".into(), dir.path().into())).unwrap();
        let title = |value: &str| format!("{{\"id\":\"t\",\"parent\":null,\"ts\":0,\"type\":\"title\",\"title\":\"{value}\"}}\n");
        let text = |value: &str| format!("{header}\n{}\n{}", " ".repeat(128 * 1024), title(value));
        let summary = || JsonlStore::new(dir.path()).summary(&SessionRef::Path(log.clone())).unwrap();
        let cached = || summary_cache::load(&log, &std::fs::metadata(&log).unwrap());
        std::fs::write(&log, text("first!")).unwrap();
        assert_eq!(summary().title.as_deref(), Some("first!"));
        assert!(cached().is_some());
        assert_eq!(summary().title.as_deref(), Some("first!"));

        OpenOptions::new().append(true).open(&log).unwrap().write_all(title("second").as_bytes()).unwrap();
        assert!(cached().is_none());
        assert_eq!(summary().title.as_deref(), Some("second"));
        let old = std::fs::read_to_string(&log).unwrap();
        std::fs::write(&log, old.replace("second", "edited")).unwrap();
        assert!(cached().is_none(), "same-size edits invalidate the disk projection");
        assert_eq!(summary().title.as_deref(), Some("edited"));

        let replacement = dir.path().join("replacement");
        std::fs::write(&replacement, text("newone")).unwrap();
        std::fs::rename(replacement, &log).unwrap();
        assert!(cached().is_none());
        assert_eq!(summary().title.as_deref(), Some("newone"));
        std::fs::write(&log, format!("{header}\n{}", title("short!"))).unwrap();
        assert!(cached().is_none());
        assert_eq!(summary().title.as_deref(), Some("short!"));
    }

    #[test]
    #[cfg(unix)]
    fn summary_projection_is_optional_bounded_private_and_never_hides_a_partial_tail() {
        let dir = tempfile::tempdir().unwrap();
        let log = dir.path().join("20261103T000000-cached.jsonl");
        let header = serde_json::to_string(&SessionHeader::new("cached".into(), dir.path().into())).unwrap();
        let body = format!("{header}\n{}\n", " ".repeat(128 * 1024));
        let summary = || JsonlStore::new(dir.path()).summary(&SessionRef::Path(log.clone()));
        let cache = log.with_extension("").join("summary-cache.json");
        std::fs::write(&log, &body).unwrap();
        summary().unwrap();
        assert!(cache.is_file());
        let mut future: serde_json::Value = serde_json::from_slice(&std::fs::read(&cache).unwrap()).unwrap();
        future["version"] = serde_json::json!(999);
        std::fs::write(&cache, serde_json::to_vec(&future).unwrap()).unwrap();
        assert!(summary_cache::load(&log, &std::fs::metadata(&log).unwrap()).is_none());
        summary().unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(std::fs::metadata(&cache).unwrap().permissions().mode() & 0o777, 0o600);
        }
        for invalid in ["broken".to_string(), " ".repeat(65537)] {
            std::fs::write(&cache, invalid).unwrap();
            assert!(summary_cache::load(&log, &std::fs::metadata(&log).unwrap()).is_none());
            assert!(summary().unwrap().title.is_none());
        }
        let tail = "{\"id\":\"t\",\"parent\":null,\"ts\":0,\"type\":\"title\",\"title\":\"tail\"}";
        std::fs::write(&log, format!("{body}{tail}")).unwrap();
        assert_eq!(summary().unwrap().title.as_deref(), Some("tail"));
        assert!(summary_cache::load(&log, &std::fs::metadata(&log).unwrap()).is_none());
        OpenOptions::new().append(true).open(&log).unwrap().write_all(b"\n").unwrap();
        assert_eq!(summary().unwrap().title.as_deref(), Some("tail"));
        assert!(summary_cache::load(&log, &std::fs::metadata(&log).unwrap()).is_some());
        OpenOptions::new().append(true).open(&log).unwrap().write_all(&[0xff]).unwrap();
        assert!(summary().is_err(), "invalid UTF-8 must still make the log unreadable");
        std::fs::remove_dir_all(log.with_extension("")).unwrap();
        std::fs::write(log.with_extension(""), "occupied").unwrap();
        std::fs::write(&log, &body).unwrap();
        assert!(summary().is_ok(), "cache write failures must not fail listing");
    }

    #[test]
    #[cfg(unix)]
    fn oversized_first_messages_are_listed_without_an_unbounded_disk_projection() {
        let dir = tempfile::tempdir().unwrap();
        let log = dir.path().join("20261103T000000-large.jsonl");
        let header = serde_json::to_string(&SessionHeader::new("large".into(), dir.path().into())).unwrap();
        let text = "x".repeat(128 * 1024);
        let entry = Entry { id: "q".into(), parent: None, ts: 0,
            kind: EntryKind::message(her_llm::UserMessage::text(&text).into()) };
        std::fs::write(&log, format!("{header}\n{}\n", serde_json::to_string(&entry).unwrap())).unwrap();
        let summary = JsonlStore::new(dir.path()).summary(&SessionRef::Path(log.clone())).unwrap();
        assert_eq!(summary.first_message.as_deref(), Some(text.as_str()));
        assert!(!log.with_extension("").join("summary-cache.json").exists());
    }

}
