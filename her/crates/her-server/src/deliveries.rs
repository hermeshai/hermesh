//! The files a bot sent its owner (docs/her-deliverables.md §4.2, §4.3): a row in the database
//! for each file of each `send_to_user` call that sent files, written as the call completes.
//! The list of them (`deliveries/list`) reads the rows, not the conversation: a device holds
//! only the conversation's end, and a cleared conversation's files are still the owner's.
//! The bot's row says how many are new since the owner last looked (`deliveries/seen`), so
//! every device shows the same number.
//!
//! The database is the routines' to open (`Routines`): none in a her that is not the service
//! that stays running, where nothing is recorded and the list is empty.
//!
//! What a bot sent before there was a record of it is written once from its conversations'
//! logs (§4.7), before anything it sends from then on, and seen: nothing old shows as new.
//!
//! Where a file came from (§4.11): a routine's run, when the run wrote that content in the bot's
//! folder; otherwise what woke the bot to send it, followed back through the sub-agents it gave
//! the work to; otherwise the owner's asking in the conversation.

use std::path::Path;
use std::sync::Arc;

use her_llm::Message;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::MailKind;
use her_protocol::api::TranscriptItem;
use her_protocol::bot::DeliveredFile;
use her_protocol::bot::DeliveriesListParams;
use her_protocol::bot::DeliveriesListResult;
use her_protocol::bot::DeliveriesSeenParams;
use her_protocol::bot::DeliveriesSourcesParams;
use her_protocol::bot::DeliveriesSourcesResult;
use her_protocol::bot::Delivery;
use her_protocol::bot::DeliveryKind;
use her_protocol::bot::DeliverySource;
use her_protocol::bot::DeliverySourceCount;
use her_protocol::bot::DeliverySourceFilter;
use her_protocol::bot::DeliverySourceKind;
use her_rpc::RpcError;
use parking_lot::Mutex;
use rusqlite::Connection;
use rusqlite::OptionalExtension;
use rusqlite::params;
use serde_json::Value;
use tokio::sync::mpsc;

use her_core::JsonlStore;
use her_core::store::SessionStore;

use crate::bots::Registry;
use crate::mirror::SessionMirror;
use crate::routines::Routines;
use crate::server::Server;

/// The second step of the database (docs/her-deliverables.md §4.2). `seq` orders a bot's sends
/// and is what the owner's mark and a page's edge are told in; the unique key makes writing a
/// send twice harmless.
pub(crate) const SCHEMA_V2: &str = "
CREATE TABLE deliveries (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  bot_id      TEXT NOT NULL,
  session_id  TEXT NOT NULL,
  call_id     TEXT NOT NULL,
  idx         INTEGER NOT NULL,
  path        TEXT NOT NULL,
  name        TEXT NOT NULL,
  kind        TEXT NOT NULL,
  size        INTEGER NOT NULL,
  width       INTEGER,
  height      INTEGER,
  sha256      TEXT,
  snapshot    TEXT,
  text        TEXT,
  sent_at     TEXT NOT NULL,
  source_kind TEXT,
  source_id   TEXT,
  source_name TEXT,
  UNIQUE (session_id, call_id, idx)
) STRICT;
CREATE INDEX deliveries_by_bot ON deliveries(bot_id, seq DESC);
CREATE TABLE delivery_marks (
  bot_id     TEXT PRIMARY KEY,
  seen       INTEGER NOT NULL DEFAULT 0,
  backfilled INTEGER NOT NULL DEFAULT 0
) STRICT;
";

/// The third (§4.11): the run a file came from, and what each routine's run wrote in the bot's
/// folder, by content.
pub(crate) const SCHEMA_V3: &str = "
ALTER TABLE deliveries ADD COLUMN source_run TEXT;
CREATE TABLE run_files (
  run_id     TEXT NOT NULL,
  routine_id TEXT NOT NULL,
  bot_id     TEXT NOT NULL,
  name       TEXT NOT NULL,
  path       TEXT NOT NULL,
  sha256     TEXT NOT NULL,
  at         TEXT NOT NULL,
  PRIMARY KEY (run_id, path)
) STRICT;
CREATE INDEX run_files_by_content ON run_files(bot_id, sha256);
";

/// A page of the list without a `limit`, and the most one may ask for.
const PAGE: u32 = 50;
const PAGE_MAX: u32 = 200;

/// One `send_to_user` call that sent files.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Sent {
    bot: String,
    session: String,
    call: String,
    at: String,
    text: Option<String>,
    source: Option<DeliverySource>,
    files: Vec<SentFile>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
struct SentFile {
    path: String,
    name: String,
    kind: DeliveryKind,
    size: u64,
    width: Option<u32>,
    height: Option<u32>,
    sha256: Option<String>,
    snapshot: Option<String>,
}

/// The files a call's result says it sent (`details.files`, as `her_core::bot` writes them),
/// with the words that went with them. None for a result that sent none.
fn sent_of(bot: &str, session: &str, call: &str, details: &str, at: String, source: Option<DeliverySource>) -> Option<Sent> {
    let details: Value = serde_json::from_str(details).ok()?;
    let text = |v: &Value, key: &str| v.get(key).and_then(Value::as_str).filter(|s| !s.is_empty()).map(str::to_string);
    let pixels = |v: &Value, key: &str| v.get(key).and_then(Value::as_u64).and_then(|n| u32::try_from(n).ok());
    let files: Vec<SentFile> = details
        .get("files")?
        .as_array()?
        .iter()
        .filter_map(|f| {
            let path = text(f, "path")?;
            let name = std::path::Path::new(&path).file_name()?.to_str()?.to_string();
            let image = f.get("image").and_then(Value::as_bool) == Some(true);
            Some(SentFile {
                kind: DeliveryKind::of(&name, image),
                name,
                size: f.get("size").and_then(Value::as_u64).unwrap_or(0),
                width: pixels(f, "width"),
                height: pixels(f, "height"),
                sha256: text(f, "sha256"),
                snapshot: text(f, "snapshot"),
                path,
            })
        })
        .collect();
    (!files.is_empty()).then(|| Sent {
        bot: bot.to_string(),
        session: session.to_string(),
        call: call.to_string(),
        at,
        text: text(&details, "text"),
        source,
        files,
    })
}

/// What the bot's next message comes from: the latest of what it was told since the last task's
/// end. A routine's report, or news of a session, is the source; the owner's message is none;
/// a sub-agent's report is what the bot was doing when it last gave that agent work, so the
/// work a routine's report set off keeps its routine however many rounds it took.
fn source_of(transcript: &[TranscriptItem]) -> Option<DeliverySource> {
    for (at, item) in transcript.iter().enumerate().rev() {
        match item {
            TranscriptItem::TaskEnd { .. } => return None,
            TranscriptItem::Message {
                message: Message::User(_), mail, ..
            } => {
                let mail = mail.as_ref()?;
                return match (&mail.routine_id, mail.kind) {
                    (Some(routine), _) => Some(DeliverySource {
                        kind: DeliverySourceKind::Routine,
                        id: routine.clone(),
                        name: mail.from.name.clone(),
                        run: Some(mail.from.id.clone()),
                    }),
                    (None, MailKind::News) => Some(DeliverySource {
                        kind: DeliverySourceKind::Session,
                        id: mail.from.id.clone(),
                        name: mail.from.name.clone(),
                        run: None,
                    }),
                    (None, MailKind::Report) => match given(&transcript[..at], &mail.from.id) {
                        Some(given) => source_of(&transcript[..given]),
                        // Its start is not in what is here: what came before it, then.
                        None => continue,
                    },
                    // What an agent says while it works, or told the owner: not why the bot acts.
                    _ => continue,
                };
            }
            _ => {}
        }
    }
    None
}

/// The latest call that gave `agent` work, starting it or sending it a message: what the bot
/// was doing then is what that work came from.
fn given(transcript: &[TranscriptItem], agent: &str) -> Option<usize> {
    transcript.iter().rposition(|item| {
        let TranscriptItem::ToolResult { result, .. } = item else {
            return false;
        };
        matches!(result.tool_name.as_str(), "spawn_agent" | "send_message")
            && !result.is_error
            && result
                .details
                .as_ref()
                .and_then(|d| serde_json::from_str::<Value>(d.as_str()).ok())
                .is_some_and(|d| d.get("agent_id").and_then(Value::as_str) == Some(agent))
    })
}

/// The routine's run that wrote this content in the bot's folder, the latest.
fn run_that_wrote(conn: &Connection, bot: &str, sha256: &str) -> rusqlite::Result<Option<DeliverySource>> {
    conn.prepare_cached("SELECT routine_id, name, run_id FROM run_files WHERE bot_id = ?1 AND sha256 = ?2 ORDER BY at DESC, run_id DESC LIMIT 1")?
        .query_row(params![bot, sha256], |row| {
            Ok(DeliverySource {
                kind: DeliverySourceKind::Routine,
                id: row.get(0)?,
                name: row.get(1)?,
                run: Some(row.get(2)?),
            })
        })
        .optional()
}

fn source_kind_text(kind: DeliverySourceKind) -> &'static str {
    match kind {
        DeliverySourceKind::Routine => "routine",
        DeliverySourceKind::Session => "session",
    }
}

fn kind_of(text: &str) -> DeliveryKind {
    match text {
        "doc" => DeliveryKind::Doc,
        "sheet" => DeliveryKind::Sheet,
        "slide" => DeliveryKind::Slide,
        "image" => DeliveryKind::Image,
        "video" => DeliveryKind::Video,
        "audio" => DeliveryKind::Audio,
        "code" => DeliveryKind::Code,
        _ => DeliveryKind::Other,
    }
}

/// Writes a call's files, once however often it is asked. A file a routine's run wrote is the
/// run's, whatever woke the bot to send it.
fn record(conn: &Connection, sent: &Sent) -> rusqlite::Result<()> {
    let mut insert = conn.prepare_cached(
        "INSERT OR IGNORE INTO deliveries
           (bot_id, session_id, call_id, idx, path, name, kind, size, width, height, sha256, snapshot, text, sent_at, source_kind, source_id, source_name, source_run)
         VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18)",
    )?;
    for (idx, f) in sent.files.iter().enumerate() {
        let written = match &f.sha256 {
            Some(sha256) => run_that_wrote(conn, &sent.bot, sha256)?,
            None => None,
        };
        let source = written.as_ref().or(sent.source.as_ref());
        insert.execute(params![
            sent.bot,
            sent.session,
            sent.call,
            idx as i64,
            f.path,
            f.name,
            f.kind.as_str(),
            f.size as i64,
            f.width,
            f.height,
            f.sha256,
            f.snapshot,
            sent.text,
            sent.at,
            source.map(|s| source_kind_text(s.kind)),
            source.map(|s| s.id.as_str()),
            source.map(|s| s.name.as_str()),
            source.and_then(|s| s.run.as_deref()),
        ])?;
    }
    Ok(())
}

/// What a bot's row says: how many files have a send the owner has not seen, and the latest send.
fn counts(conn: &Connection, bot: &str) -> rusqlite::Result<(u32, u64)> {
    conn.query_row(
        "SELECT (SELECT COUNT(DISTINCT path) FROM deliveries
                  WHERE bot_id = ?1 AND seq > COALESCE((SELECT seen FROM delivery_marks WHERE bot_id = ?1), 0)),
                (SELECT COALESCE(MAX(seq), 0) FROM deliveries WHERE bot_id = ?1)",
        params![bot],
        |row| Ok((row.get::<_, i64>(0)? as u32, row.get::<_, i64>(1)? as u64)),
    )
}

/// The owner saw the list up to `seq`: the mark moves there, never back and never past the
/// latest send, so one made later is new whatever a client sent.
fn mark_seen(conn: &Connection, bot: &str, seq: u64) -> rusqlite::Result<()> {
    conn.execute(
        "INSERT INTO delivery_marks (bot_id, seen)
         VALUES (?1, MIN(?2, (SELECT COALESCE(MAX(seq), 0) FROM deliveries WHERE bot_id = ?1)))
         ON CONFLICT (bot_id) DO UPDATE SET seen = MAX(seen, excluded.seen)",
        params![bot, i64::try_from(seq).unwrap_or(i64::MAX)],
    )?;
    Ok(())
}

fn forget(conn: &Connection, bot: &str) -> rusqlite::Result<()> {
    conn.execute("DELETE FROM deliveries WHERE bot_id = ?1", params![bot])?;
    conn.execute("DELETE FROM run_files WHERE bot_id = ?1", params![bot])?;
    conn.execute("DELETE FROM delivery_marks WHERE bot_id = ?1", params![bot])?;
    Ok(())
}

/// `query` as a `LIKE` pattern that matches it anywhere, its own `%`, `_` and `\` as they are.
fn pattern(query: &str) -> String {
    let mut out = String::from("%");
    for c in query.chars() {
        if matches!(c, '%' | '_' | '\\') {
            out.push('\\');
        }
        out.push(c);
    }
    out.push('%');
    out
}

/// A file's sends, newest first, as the list shows them: each different content once, by its
/// latest send in a row of the same; sends that kept no copy cannot be shown as they were, so
/// they are left out, and a file whose latest send kept none is that send alone.
fn fold(sends: Vec<Delivery>) -> Vec<Delivery> {
    let mut versions: Vec<Delivery> = Vec::new();
    for send in sends {
        match versions.last() {
            None => versions.push(send),
            Some(newer) if newer.sha256.is_none() => break,
            Some(newer) => {
                if send.sha256.is_some() && send.sha256 != newer.sha256 {
                    versions.push(send);
                }
            }
        }
    }
    versions
}

/// A page of a bot's files, newest first by their latest send.
fn list(conn: &Connection, p: &DeliveriesListParams) -> rusqlite::Result<DeliveriesListResult> {
    let limit = p.limit.unwrap_or(PAGE).clamp(1, PAGE_MAX) as usize;
    let query = p.query.as_deref().map(str::trim).filter(|q| !q.is_empty()).map(pattern);
    let before = p.before.map(|seq| i64::try_from(seq).unwrap_or(i64::MAX));
    let (source_kind, source_key) = match &p.source {
        None => (None, None),
        Some(DeliverySourceFilter::Conversation) => (Some("conversation"), None),
        Some(DeliverySourceFilter::Routine { id }) => (Some("routine"), Some(id.as_str())),
        Some(DeliverySourceFilter::Session { name }) => (Some("session"), Some(name.as_str())),
    };
    // A file is its path; its name, kind and source are its latest send's (a bare column beside
    // MAX is that row's).
    let mut pages = conn.prepare_cached(
        r"SELECT path, name, kind, latest FROM (
            SELECT path, name, kind, source_kind, source_id, source_name, MAX(seq) AS latest FROM deliveries
             WHERE bot_id = ?1
               AND (?2 IS NULL OR path IN (
                     SELECT path FROM deliveries
                      WHERE bot_id = ?1 AND (name LIKE ?2 ESCAPE '\' OR text LIKE ?2 ESCAPE '\' OR source_name LIKE ?2 ESCAPE '\')))
             GROUP BY path)
           WHERE (?3 IS NULL OR latest < ?3) AND (?4 IS NULL OR kind = ?4)
             AND (?6 IS NULL OR (kind IN ('image', 'video', 'audio')) = ?6)
             AND (?7 IS NULL
                  OR (?7 = 'conversation' AND source_kind IS NULL)
                  OR (?7 = 'routine' AND source_kind = 'routine' AND source_id = ?8)
                  OR (?7 = 'session' AND source_kind = 'session' AND source_name = ?8))
           ORDER BY latest DESC LIMIT ?5",
    )?;
    let mut found: Vec<(String, String, String, i64)> = pages
        .query_map(
            params![p.bot_id, query, before, p.kind.map(DeliveryKind::as_str), (limit + 1) as i64, p.media, source_kind, source_key],
            |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?)),
        )?
        .collect::<rusqlite::Result<_>>()?;
    let more = found.len() > limit;
    found.truncate(limit);
    let next = if more { found.last().map(|(.., latest)| *latest as u64) } else { None };

    let mut sends = conn.prepare_cached(
        "SELECT seq, session_id, call_id, size, width, height, sha256, snapshot, text, sent_at, source_kind, source_id, source_name, source_run
           FROM deliveries WHERE bot_id = ?1 AND path = ?2 ORDER BY seq DESC",
    )?;
    let mut files = Vec::with_capacity(found.len());
    for (path, name, kind, _) in found {
        let versions = sends
            .query_map(params![p.bot_id, path], |row| {
                let source = match (row.get::<_, Option<String>>(10)?, row.get::<_, Option<String>>(11)?, row.get::<_, Option<String>>(12)?) {
                    (Some(kind), Some(id), Some(name)) => Some(DeliverySource {
                        kind: if kind == "routine" { DeliverySourceKind::Routine } else { DeliverySourceKind::Session },
                        id,
                        name,
                        run: row.get(13)?,
                    }),
                    _ => None,
                };
                Ok(Delivery {
                    seq: row.get::<_, i64>(0)? as u64,
                    session_id: row.get(1)?,
                    call_id: row.get(2)?,
                    size: row.get::<_, i64>(3)? as u64,
                    width: row.get(4)?,
                    height: row.get(5)?,
                    sha256: row.get(6)?,
                    snapshot: row.get(7)?,
                    text: row.get(8)?,
                    sent_at: row.get(9)?,
                    source,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        files.push(DeliveredFile {
            path,
            name,
            kind: kind_of(&kind),
            versions: fold(versions),
        });
    }
    let seen = conn.query_row("SELECT COALESCE((SELECT seen FROM delivery_marks WHERE bot_id = ?1), 0)", params![p.bot_id], |row| row.get::<_, i64>(0))?;
    Ok(DeliveriesListResult { files, seen: seen as u64, next })
}

/// Where a bot's files came from, by their latest sends, the latest first; a routine by the
/// name it has now, if it was not deleted.
fn sources(conn: &Connection, bot: &str) -> rusqlite::Result<DeliveriesSourcesResult> {
    let mut found = conn.prepare_cached(
        r"WITH files AS (
            SELECT source_kind, source_id, source_name, kind, MAX(seq) AS latest FROM deliveries
             WHERE bot_id = ?1 GROUP BY path)
          SELECT source_kind, CASE source_kind WHEN 'session' THEN source_name ELSE source_id END AS key,
                 source_name, COUNT(*), SUM(kind IN ('image', 'video', 'audio')), MAX(latest),
                 (SELECT name FROM routines r WHERE r.id = files.source_id AND r.deleted_at IS NULL)
            FROM files GROUP BY source_kind, key ORDER BY MAX(latest) DESC",
    )?;
    let sources = found
        .query_map(params![bot], |row| {
            let kind: Option<String> = row.get(0)?;
            let key: Option<String> = row.get(1)?;
            let heard: Option<String> = row.get(2)?;
            let now: Option<String> = row.get(6)?;
            let (filter, name, gone) = match (kind.as_deref(), key) {
                (Some("routine"), Some(id)) => (DeliverySourceFilter::Routine { id }, now.clone().or(heard), now.is_none()),
                (Some("session"), Some(name)) => (DeliverySourceFilter::Session { name: name.clone() }, Some(name), false),
                _ => (DeliverySourceFilter::Conversation, None, false),
            };
            Ok(DeliverySourceCount {
                filter,
                name,
                files: row.get::<_, i64>(3)? as u32,
                media: row.get::<_, i64>(4)? as u32,
                latest: row.get::<_, i64>(5)? as u64,
                gone,
            })
        })?
        .collect::<rusqlite::Result<Vec<_>>>()?;
    let silent = conn.query_row(
        "SELECT COUNT(*) FROM routines r WHERE r.bot_id = ?1 AND r.deleted_at IS NULL
           AND NOT EXISTS (SELECT 1 FROM deliveries d WHERE d.bot_id = ?1 AND d.source_kind = 'routine' AND d.source_id = r.id)",
        params![bot],
        |row| row.get::<_, i64>(0),
    )? as u32;
    Ok(DeliveriesSourcesResult { sources, silent })
}

/// A routine's run that ended, and what it wrote in its bot's folder.
#[derive(Clone, Debug)]
pub(crate) struct RunWrote {
    pub(crate) bot: String,
    pub(crate) routine: String,
    pub(crate) name: String,
    pub(crate) run: String,
    /// When it started, as `stamp` writes it.
    pub(crate) started: String,
    pub(crate) files: Vec<(std::path::PathBuf, String)>,
}

/// Keeps what the run wrote, and makes the files the bot sent with that content since the run
/// started the run's: the bot may have sent them before this is known, as the report reaches it
/// before the run's end is saved.
fn save_run_files(conn: &mut Connection, wrote: &RunWrote) -> rusqlite::Result<()> {
    let tx = conn.transaction()?;
    {
        let mut insert = tx.prepare_cached(
            "INSERT OR REPLACE INTO run_files (run_id, routine_id, bot_id, name, path, sha256, at) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
        )?;
        let mut claim = tx.prepare_cached(
            "UPDATE deliveries SET source_kind = 'routine', source_id = ?2, source_name = ?3, source_run = ?4
              WHERE bot_id = ?1 AND sha256 = ?5 AND sent_at >= ?6",
        )?;
        for (path, sha256) in &wrote.files {
            let path = path.to_string_lossy();
            insert.execute(params![wrote.run, wrote.routine, wrote.bot, wrote.name, path, sha256, wrote.started])?;
            claim.execute(params![wrote.bot, wrote.routine, wrote.name, wrote.run, sha256, wrote.started])?;
        }
    }
    tx.commit()
}

/// What `bots` sent in their conversations' logs that are still in `store` before `before` (ms),
/// oldest first: what was sent from then on comes through the events, after. A conversation
/// cleared or deleted is in the trash, not read: nothing promised its files would stay
/// (docs/her-deliverables.md §4.7). Blocking I/O.
fn sent_in_logs(store: &JsonlStore, bots: &[String], before: i64) -> Vec<Sent> {
    let mut out = Vec::new();
    for log in store.logs() {
        let Ok((header, _)) = store.header(&her_core::SessionRef::Path(log.clone())) else {
            continue;
        };
        let Some(bot) = header.bot.as_ref().filter(|b| header.agent.is_none() && bots.contains(&b.id)) else {
            continue;
        };
        let Some((_, transcript)) = her_core::routine::log_transcript(store, &log) else {
            continue;
        };
        for (i, item) in transcript.iter().enumerate() {
            let TranscriptItem::ToolResult { call_id, result } = item else {
                continue;
            };
            if result.tool_name != her_core::bot::SEND_TO_USER || result.is_error || result.ts >= before {
                continue;
            }
            let Some(details) = &result.details else {
                continue;
            };
            let at = stamp_ms(result.ts);
            if let Some(sent) = sent_of(&bot.id, &header.id, call_id, details.as_str(), at, source_of(&transcript[..i])) {
                out.push(sent);
            }
        }
    }
    out
}

fn stamp_ms(ms: i64) -> String {
    jiff::Timestamp::from_millisecond(ms).map(crate::routines::stamp).unwrap_or_default()
}

/// Whether the logs were read for `bot`.
fn backfilled(conn: &Connection, bot: &str) -> rusqlite::Result<bool> {
    Ok(conn.query_row("SELECT backfilled FROM delivery_marks WHERE bot_id = ?1", params![bot], |row| row.get::<_, i64>(0)).optional()?.unwrap_or(0) != 0)
}

/// Writes what was found in the logs of `bots` that were not read yet, all seen, and marks
/// them read; `skip`: the call being recorded now, which is new.
fn write_backfill(conn: &mut Connection, bots: &[String], sends: &[Sent], skip: Option<(&str, &str)>) -> rusqlite::Result<()> {
    let tx = conn.transaction()?;
    for bot in bots {
        if backfilled(&tx, bot)? {
            continue;
        }
        for sent in sends.iter().filter(|s| &s.bot == bot && skip != Some((s.session.as_str(), s.call.as_str()))) {
            record(&tx, sent)?;
        }
        tx.execute(
            "INSERT INTO delivery_marks (bot_id, seen, backfilled)
             VALUES (?1, (SELECT COALESCE(MAX(seq), 0) FROM deliveries WHERE bot_id = ?1), 1)
             ON CONFLICT (bot_id) DO UPDATE SET seen = MAX(seen, excluded.seen), backfilled = 1",
            params![bot],
        )?;
    }
    tx.commit()
}

/// What the writer does, in order.
enum Job {
    /// The database was opened at this moment (ms): what every bot sent before is read from the logs.
    Backfill(i64),
    /// A send, and when its event came (ms): what its bot sent before is read first.
    Send(Sent, i64),
}

/// The record of what the bots sent, and the way to it from their conversations' events.
pub(crate) struct Deliveries {
    routines: Arc<Routines>,
    bots: Arc<Registry>,
    /// The conversations' logs, read for what was sent before the record.
    logs: Arc<JsonlStore>,
    /// What to write, in the order it came: one task writes it all, so the sends' numbers follow
    /// the order they were made in, after what the logs held.
    queue: mpsc::UnboundedSender<Job>,
    /// The other end, until the first job starts that task.
    waiting: Mutex<Option<mpsc::UnboundedReceiver<Job>>>,
}

impl Deliveries {
    pub(crate) fn new(routines: Arc<Routines>, bots: Arc<Registry>, sessions: &Path) -> Arc<Self> {
        let (queue, waiting) = mpsc::unbounded_channel();
        Arc::new(Self {
            routines,
            bots,
            logs: Arc::new(JsonlStore::new(sessions)),
            queue,
            waiting: Mutex::new(Some(waiting)),
        })
    }

    /// An event of a bot's loaded conversation: a `send_to_user` call that sent files is
    /// recorded once it completed, when the files were found to be there (bot-design §3.12).
    pub(crate) fn on_event(self: &Arc<Self>, bot: &str, event: &Event, mirror: &SessionMirror) {
        let EventMsg::ToolCompleted { call_id, result } = &event.msg else {
            return;
        };
        if result.tool_name != her_core::bot::SEND_TO_USER || result.is_error {
            return;
        }
        let Some(details) = &result.details else {
            return;
        };
        let at = stamp_ms(event.ts);
        if let Some(sent) = sent_of(bot, &mirror.info().session_id, call_id, details.as_str(), at, source_of(mirror.transcript())) {
            self.push(Job::Send(sent, event.ts));
        }
    }

    fn push(self: &Arc<Self>, job: Job) {
        if let Some(waiting) = self.waiting.lock().take() {
            tokio::spawn(self.clone().writing(waiting));
        }
        let _ = self.queue.send(job);
    }

    async fn writing(self: Arc<Self>, mut jobs: mpsc::UnboundedReceiver<Job>) {
        while let Some(job) = jobs.recv().await {
            match job {
                Job::Backfill(before) => {
                    let Ok(Some(db)) = self.routines.database() else {
                        continue;
                    };
                    let bots = self.bots.ids();
                    if let Err(err) = self.backfill(&db, bots.clone(), before, None).await {
                        tracing::warn!("what was sent before was not read: {err}");
                    }
                    self.count(&db, &bots).await;
                }
                Job::Send(sent, before) => {
                    // The first send makes the database, as the first routine does.
                    let Some(db) = self.routines.database_for_new().await else {
                        continue;
                    };
                    let bot = sent.bot.clone();
                    let skip = Some((sent.session.clone(), sent.call.clone()));
                    if let Err(err) = self.backfill(&db, vec![bot.clone()], before, skip).await {
                        tracing::warn!(bot = %bot, "what was sent before was not read: {err}");
                    }
                    let written = db
                        .call(move |conn| {
                            record(conn, &sent)?;
                            counts(conn, &sent.bot)
                        })
                        .await;
                    match written {
                        Ok((new, latest)) => self.bots.set_deliveries(&bot, new, latest),
                        Err(err) => tracing::warn!(bot = %bot, "a sent file was not recorded: {err}"),
                    }
                }
            }
        }
    }

    /// Reads the logs of those of `bots` whose were not read yet, and writes what they sent.
    async fn backfill(&self, db: &crate::db::Db, bots: Vec<String>, before: i64, skip: Option<(String, String)>) -> Result<(), String> {
        let wanted = {
            let bots = bots.clone();
            db.call(move |conn| bots.into_iter().filter_map(|b| backfilled(conn, &b).map(|done| (!done).then_some(b)).transpose()).collect::<rusqlite::Result<Vec<_>>>())
                .await
                .map_err(|e| e.to_string())?
        };
        if wanted.is_empty() {
            return Ok(());
        }
        let logs = self.logs.clone();
        let found = {
            let wanted = wanted.clone();
            tokio::task::spawn_blocking(move || sent_in_logs(&logs, &wanted, before)).await.map_err(|e| e.to_string())?
        };
        db.call(move |conn| write_backfill(conn, &wanted, &found, skip.as_ref().map(|(s, c)| (s.as_str(), c.as_str()))))
            .await
            .map_err(|e| e.to_string())
    }

    /// Each of `bots`' rows says what is new.
    async fn count(&self, db: &crate::db::Db, bots: &[String]) {
        let bots = bots.to_vec();
        let counted = db.call(move |conn| bots.into_iter().map(|bot| counts(conn, &bot).map(|c| (bot, c))).collect::<rusqlite::Result<Vec<_>>>()).await;
        match counted {
            Ok(counted) => {
                for (bot, (new, latest)) in counted {
                    self.bots.set_deliveries(&bot, new, latest);
                }
            }
            Err(err) => tracing::warn!("the sent files were not counted: {err}"),
        }
    }

    /// The database was opened: what the bots sent before is read, and their rows say what
    /// is new.
    pub(crate) fn opened(self: &Arc<Self>) {
        self.push(Job::Backfill(jiff::Timestamp::now().as_millisecond()));
    }

    /// A routine's run ended: what it wrote in the bot's folder `dir` is known as its. That is a
    /// file changed there since `since` that its session's `log` names: the bot's conversation may
    /// be writing there meanwhile, and a run that never started wrote nothing.
    pub(crate) async fn run_ended(&self, dir: std::path::PathBuf, since: std::time::SystemTime, log: Option<std::path::PathBuf>, mut wrote: RunWrote) {
        let Some(log) = log else {
            return;
        };
        let found = tokio::task::spawn_blocking(move || {
            let said = std::fs::read_to_string(&log).unwrap_or_default();
            let mut files = her_core::bot::written_since(&dir, since);
            files.retain(|(path, _)| path.file_name().and_then(|n| n.to_str()).is_some_and(|name| said.contains(name)));
            files
        });
        match found.await {
            Ok(files) if !files.is_empty() => wrote.files = files,
            _ => return,
        }
        let Some(db) = self.routines.database_for_new().await else {
            return;
        };
        let run = wrote.run.clone();
        if let Err(err) = db.call(move |conn| save_run_files(conn, &wrote)).await {
            tracing::warn!(run = %run, "what a run wrote was not kept: {err}");
        }
    }

    /// A bot was deleted: what it sent goes from the list with it.
    pub(crate) async fn bot_deleted(&self, bot: &str) {
        let Ok(Some(db)) = self.routines.database() else {
            return;
        };
        let id = bot.to_string();
        if let Err(err) = db.call(move |conn| forget(conn, &id)).await {
            tracing::warn!(bot = %bot, "a deleted bot's sent files were not removed: {err}");
        }
    }
}

fn unavailable(why: impl std::fmt::Display) -> RpcError {
    RpcError::internal(format!("the record of sent files cannot be read: {why}"))
}

impl Server {
    /// `deliveries/list`: nothing where there is no database, as where nothing was sent.
    pub async fn deliveries_list(&self, p: DeliveriesListParams) -> Result<DeliveriesListResult, RpcError> {
        self.bot_exists(&p.bot_id)?;
        let Some(db) = self.0.routines.database().map_err(unavailable)? else {
            return Ok(DeliveriesListResult {
                files: Vec::new(),
                seen: 0,
                next: None,
            });
        };
        db.call(move |conn| list(conn, &p)).await.map_err(unavailable)
    }

    /// `deliveries/sources`: none where there is no database.
    pub async fn deliveries_sources(&self, p: DeliveriesSourcesParams) -> Result<DeliveriesSourcesResult, RpcError> {
        self.bot_exists(&p.bot_id)?;
        let Some(db) = self.0.routines.database().map_err(unavailable)? else {
            return Ok(DeliveriesSourcesResult { sources: Vec::new(), silent: 0 });
        };
        db.call(move |conn| sources(conn, &p.bot_id)).await.map_err(unavailable)
    }

    /// `deliveries/seen`: the bot's row says so to every device.
    pub async fn deliveries_seen(&self, p: DeliveriesSeenParams) -> Result<(), RpcError> {
        self.bot_exists(&p.bot_id)?;
        let Some(db) = self.0.routines.database().map_err(unavailable)? else {
            return Ok(());
        };
        let bot = p.bot_id.clone();
        let (new, latest) = db
            .call(move |conn| {
                mark_seen(conn, &p.bot_id, p.seq)?;
                counts(conn, &p.bot_id)
            })
            .await
            .map_err(unavailable)?;
        self.0.bots.set_deliveries(&bot, new, latest);
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::db::Db;
    use her_protocol::AgentRef;
    use her_protocol::AgentStatus;
    use her_protocol::MailInfo;
    use her_protocol::api::TaskOutcome;
    use serde_json::json;

    fn db(dir: &tempfile::TempDir) -> Db {
        Db::open(&dir.path().join("her.db")).unwrap()
    }

    /// A call of `bot` in the conversation s1 that sent these files: (path, the content's hash).
    fn sent(bot: &str, call: &str, text: &str, files: &[(&str, Option<&str>)]) -> Sent {
        let files: Vec<Value> = files
            .iter()
            .map(|(path, sha)| match sha {
                Some(sha) => json!({ "path": path, "size": 10, "sha256": sha, "snapshot": format!("/h/bots/{bot}/sent/{sha}/x") }),
                None => json!({ "path": path, "size": 10 }),
            })
            .collect();
        sent_of(bot, "s1", call, &json!({ "files": files, "text": text }).to_string(), format!("2026-11-16T10:00:0{}Z", call.len() % 10), None).unwrap()
    }

    async fn write(db: &Db, sends: Vec<Sent>) {
        db.call(move |conn| sends.iter().try_for_each(|s| record(conn, s))).await.unwrap();
    }

    fn page(bot: &str) -> DeliveriesListParams {
        DeliveriesListParams {
            bot_id: bot.into(),
            before: None,
            limit: None,
            query: None,
            kind: None,
            media: None,
            source: None,
        }
    }

    async fn listed(db: &Db, p: DeliveriesListParams) -> DeliveriesListResult {
        db.call(move |conn| list(conn, &p)).await.unwrap()
    }

    fn names(result: &DeliveriesListResult) -> Vec<&str> {
        result.files.iter().map(|f| f.name.as_str()).collect()
    }

    #[test]
    fn a_result_says_which_files_were_sent_and_what_each_is() {
        let details = json!({ "text": "两份。", "files": [
            { "path": "/h/bots/b1/files/图.png", "size": 2048, "image": true, "width": 960, "height": 600, "sha256": "aa", "snapshot": "/h/bots/b1/sent/aa/图.png" },
            { "path": "/Users/me/合同.PDF", "size": 9 },
        ] });
        let sent = sent_of("b1", "s1", "c1", &details.to_string(), "2026-11-16T10:00:00Z".into(), None).unwrap();
        assert_eq!((sent.text.as_deref(), sent.files.len()), (Some("两份。"), 2));
        let (image, file) = (&sent.files[0], &sent.files[1]);
        assert_eq!((image.name.as_str(), image.kind, image.width, image.height), ("图.png", DeliveryKind::Image, Some(960), Some(600)));
        assert_eq!((image.sha256.as_deref(), image.snapshot.as_deref()), (Some("aa"), Some("/h/bots/b1/sent/aa/图.png")));
        assert_eq!((file.name.as_str(), file.kind, file.size, file.sha256.as_deref()), ("合同.PDF", DeliveryKind::Doc, 9, None), "by its extension, whatever its case");
        let kinds: Vec<_> = ["clip.MP4", "voice.m4a", "photo.heic", "notes.txt", "data.zip"].iter().map(|n| DeliveryKind::of(n, false)).collect();
        assert_eq!(kinds, [DeliveryKind::Video, DeliveryKind::Audio, DeliveryKind::Image, DeliveryKind::Doc, DeliveryKind::Other]);
        assert_eq!(kinds.iter().map(|k| k.is_media()).collect::<Vec<_>>(), [true, true, true, false, false], "media is pictures, video and audio");
        assert_eq!(sent_of("b1", "s1", "c2", r#"{"card":{}}"#, String::new(), None), None, "a message of no files");
        assert_eq!(sent_of("b1", "s1", "c2", r#"{"files":[]}"#, String::new(), None), None);
    }

    #[test]
    fn a_file_comes_from_the_report_or_the_news_the_bot_is_passing_on() {
        let said = |id: &str, mail: Option<MailInfo>| TranscriptItem::Message {
            message_id: id.into(),
            message: Message::User(Arc::new(her_llm::UserMessage::text("…"))),
            skills: Vec::new(),
            mail: mail.map(Box::new),
        };
        let mail = |kind: MailKind, id: &str, name: &str, routine: Option<&str>| MailInfo {
            id: "m".into(),
            from: AgentRef { id: id.into(), name: name.into() },
            kind,
            status: AgentStatus::Completed,
            no_reply: false,
            routine_id: routine.map(str::to_string),
        };
        let ended = TranscriptItem::TaskEnd {
            task_id: "t".into(),
            outcome: TaskOutcome::Completed,
            reason: None,
            error: None,
            usage: None,
            ts: 0,
        };
        let report = said("u2", Some(mail(MailKind::Routine, "run_1", "每日晨报", Some("rtn_1"))));
        let news = said("u3", Some(mail(MailKind::News, "ses_9", "接口联调脚本", None)));
        let agent = said("u4", Some(mail(MailKind::Report, "a1", "调研", None)));

        assert_eq!(source_of(&[said("u1", None)]), None, "what the owner asked for");
        let routine = source_of(&[said("u1", None), ended.clone(), report.clone(), agent.clone()]).unwrap();
        assert_eq!((routine.kind, routine.id.as_str(), routine.name.as_str()), (DeliverySourceKind::Routine, "rtn_1", "每日晨报"), "a sub-agent's report is not one");
        let session = source_of(&[report.clone(), news]).unwrap();
        assert_eq!((session.kind, session.id.as_str(), session.name.as_str()), (DeliverySourceKind::Session, "ses_9", "接口联调脚本"), "the latest");
        assert_eq!(source_of(&[report.clone(), said("u5", None)]), None, "until the owner speaks");
        assert_eq!(source_of(&[report.clone(), ended.clone()]), None, "and until the task ends");
        assert_eq!(routine.run.as_deref(), Some("run_1"), "and the run is known");

        // The work a sub-agent did is for what the bot was doing when it gave the work.
        let gave = |tool: &str, agent: &str| TranscriptItem::ToolResult {
            call_id: "c".into(),
            result: Arc::new(her_llm::ToolResultMessage {
                call_id: "c".into(),
                tool_name: tool.into(),
                content: Vec::new(),
                is_error: false,
                details: Some(her_llm::JsonText::from_value(&json!({ "agent_id": agent, "background": true }))),
                ts: 0,
                duration_ms: None,
            }),
        };
        let owner = said("u9", None);
        let chain = [owner.clone(), ended.clone(), report.clone(), gave("spawn_agent", "a1"), ended.clone(), agent.clone()];
        assert_eq!(source_of(&chain).map(|s| s.id), Some("rtn_1".into()), "a routine's work, done by a sub-agent in a later task");
        let asked = [owner.clone(), gave("spawn_agent", "a1"), ended.clone(), report.clone(), ended.clone(), agent.clone()];
        assert_eq!(source_of(&asked), None, "the owner's, though a report came between");
        let again = [report.clone(), gave("spawn_agent", "a1"), ended.clone(), agent.clone(), ended.clone(), owner.clone(), gave("send_message", "a1"), ended.clone(), agent.clone()];
        assert_eq!(source_of(&again), None, "the work given last: the owner asked for a change");
        let other = [report.clone(), gave("spawn_agent", "a2"), agent.clone()];
        assert_eq!(source_of(&other).map(|s| s.id), Some("rtn_1".into()), "another agent's start is not its");
    }

    #[tokio::test]
    async fn sends_of_one_path_are_one_file_and_its_versions() {
        let dir = tempfile::tempdir().unwrap();
        let db = db(&dir);
        write(
            &db,
            vec![
                sent("b1", "c1", "初稿", &[("/f/报告.docx", Some("aa"))]),
                sent("b1", "c2", "图表", &[("/f/图.png", Some("p1")), ("/f/表.xlsx", Some("x1"))]),
                sent("b1", "c3", "加了定价", &[("/f/报告.docx", Some("bb"))]),
                sent("b1", "c4", "原样再发", &[("/f/报告.docx", Some("bb"))]),
                sent("other", "c1", "别人的", &[("/f/报告.docx", Some("zz"))]),
            ],
        )
        .await;
        // Written again, as when a conversation's events are read twice: nothing more.
        write(&db, vec![sent("b1", "c3", "加了定价", &[("/f/报告.docx", Some("bb"))])]).await;

        let result = listed(&db, page("b1")).await;
        assert_eq!(names(&result), ["报告.docx", "表.xlsx", "图.png"], "newest first by their latest send");
        assert_eq!((result.seen, result.next), (0, None));
        let report = &result.files[0];
        assert_eq!((report.path.as_str(), report.kind), ("/f/报告.docx", DeliveryKind::Doc));
        let versions: Vec<_> = report.versions.iter().map(|v| (v.call_id.as_str(), v.sha256.as_deref(), v.text.as_deref())).collect();
        assert_eq!(versions, [("c4", Some("bb"), Some("原样再发")), ("c1", Some("aa"), Some("初稿"))], "the same content again is its latest send");
        assert_eq!(report.versions[0].session_id, "s1");
        assert_eq!(result.files[1].kind, DeliveryKind::Sheet);
    }

    #[test]
    fn versions_are_the_contents_that_can_be_shown() {
        let send = |seq: u64, sha: Option<&str>| Delivery {
            seq,
            session_id: "s1".into(),
            call_id: format!("c{seq}"),
            size: 1,
            width: None,
            height: None,
            sha256: sha.map(str::to_string),
            snapshot: None,
            text: None,
            sent_at: String::new(),
            source: None,
        };
        let seqs = |sends: Vec<Delivery>| fold(sends).iter().map(|v| v.seq).collect::<Vec<_>>();
        assert_eq!(seqs(vec![send(5, Some("a")), send(4, Some("b")), send(3, Some("b")), send(2, Some("a")), send(1, None)]), [5, 4, 2], "changed back is a version too");
        assert_eq!(seqs(vec![send(3, None), send(2, Some("a")), send(1, Some("b"))]), [3], "the latest kept no copy: nothing earlier is offered");
        assert_eq!(seqs(vec![send(3, Some("a")), send(2, None), send(1, Some("b"))]), [3, 1], "one in between that kept none is left out");
    }

    #[tokio::test]
    async fn the_list_goes_by_pages_and_can_be_searched_and_filtered() {
        let dir = tempfile::tempdir().unwrap();
        let db = db(&dir);
        let mut report = sent("b1", "c5", "早上好", &[("/f/晨报.md", Some("m1"))]);
        report.source = Some(DeliverySource {
            kind: DeliverySourceKind::Routine,
            id: "rtn_1".into(),
            name: "每日晨报".into(),
            run: None,
        });
        write(
            &db,
            vec![
                sent("b1", "c1", "定价页截图", &[("/f/a.png", Some("p1"))]),
                sent("b1", "c2", "透视表", &[("/f/销售.xlsx", Some("x1"))]),
                sent("b1", "c3", "初稿", &[("/f/Q3_100%.docx", Some("d1"))]),
                sent("b1", "c4", "补了定价一节", &[("/f/Q3_100%.docx", Some("d2"))]),
                report,
            ],
        )
        .await;

        let first = listed(&db, DeliveriesListParams { limit: Some(2), ..page("b1") }).await;
        assert_eq!(names(&first), ["晨报.md", "Q3_100%.docx"]);
        let second = listed(&db, DeliveriesListParams { limit: Some(2), before: first.next, ..page("b1") }).await;
        assert_eq!(names(&second), ["销售.xlsx", "a.png"]);
        assert_eq!(second.next, None, "the last page");

        let find = |query: &str| listed(&db, DeliveriesListParams { query: Some(query.into()), ..page("b1") });
        assert_eq!(names(&find("q3").await), ["Q3_100%.docx"], "by name, whatever its case");
        assert_eq!(names(&find("初稿").await), ["Q3_100%.docx"], "by the words of any version");
        assert_eq!(find("初稿").await.files[0].versions.len(), 2, "and all of it is listed");
        assert_eq!(names(&find("每日").await), ["晨报.md"], "by what it came from");
        assert_eq!(names(&find("定价").await), ["Q3_100%.docx", "a.png"]);
        assert_eq!(names(&find("100%").await), ["Q3_100%.docx"], "a percent sign is one");
        assert_eq!(names(&find("3_1").await), ["Q3_100%.docx"]);
        assert!(find("3x1").await.files.is_empty(), "an underscore is no wildcard");
        assert!(find("%").await.files.len() == 1 && find("  ").await.files.len() == 4, "blank is no search");

        let media = |media: bool| listed(&db, DeliveriesListParams { media: Some(media), ..page("b1") });
        assert_eq!(names(&media(true).await), ["a.png"], "pictures, video and audio");
        assert_eq!(names(&media(false).await), ["晨报.md", "Q3_100%.docx", "销售.xlsx"], "the documents, the sheet among them");
        let kind = |kind: DeliveryKind| listed(&db, DeliveriesListParams { kind: Some(kind), ..page("b1") });
        assert_eq!(names(&kind(DeliveryKind::Image).await), ["a.png"]);
        assert_eq!(names(&kind(DeliveryKind::Doc).await), ["晨报.md", "Q3_100%.docx"]);
        assert!(kind(DeliveryKind::Slide).await.files.is_empty());
        let routine = &find("晨报").await.files[0].versions[0];
        assert_eq!(routine.source.as_ref().map(|s| (s.kind, s.id.as_str(), s.name.as_str())), Some((DeliverySourceKind::Routine, "rtn_1", "每日晨报")));
    }

    #[tokio::test]
    async fn what_was_sent_before_is_written_once_seen_and_older_than_what_is_sent_now() {
        let dir = tempfile::tempdir().unwrap();
        let db = db(&dir);
        let before = vec![sent("b1", "c1", "", &[("/f/a.md", Some("a1"))]), sent("b1", "c2", "", &[("/f/b.md", Some("b1"))]), sent("b2", "c9", "", &[("/f/z.md", Some("z1"))])];
        let now = sent("b1", "c2", "", &[("/f/b.md", Some("b1"))]);
        let backfill = |bots: Vec<String>, sends: Vec<Sent>, skip: Option<(&'static str, &'static str)>| {
            let db = db.clone();
            async move { db.call(move |conn| write_backfill(conn, &bots, &sends, skip)).await.unwrap() }
        };
        // b1's next send is c2, which its log holds already: it is written after the rest, and new.
        backfill(vec!["b1".into()], before.clone(), Some(("s1", "c2"))).await;
        write(&db, vec![now]).await;
        assert_eq!(db.call(|conn| counts(conn, "b1")).await.unwrap(), (1, 2));
        assert_eq!(names(&listed(&db, page("b1")).await), ["b.md", "a.md"]);
        assert!(listed(&db, page("b2")).await.files.is_empty(), "only the bots asked for");

        // Asked again, once read: nothing more.
        backfill(vec!["b1".into(), "b2".into()], before, None).await;
        assert_eq!(db.call(|conn| counts(conn, "b1")).await.unwrap(), (1, 2));
        assert_eq!(db.call(|conn| counts(conn, "b2")).await.unwrap(), (0, 3), "b2's, all seen");
    }

    #[tokio::test]
    async fn what_is_new_is_counted_by_file_until_the_owner_has_seen_it() {
        let dir = tempfile::tempdir().unwrap();
        let db = db(&dir);
        async fn count(db: &Db, bot: &'static str) -> (u32, u64) {
            db.call(move |conn| counts(conn, bot)).await.unwrap()
        }
        async fn see(db: &Db, seq: u64) {
            db.call(move |conn| mark_seen(conn, "b1", seq)).await.unwrap()
        }
        assert_eq!(count(&db, "b1").await, (0, 0));
        write(&db, vec![sent("b1", "c1", "", &[("/f/a.md", Some("a1")), ("/f/b.md", Some("b1"))]), sent("b1", "c2", "", &[("/f/a.md", Some("a2"))])]).await;
        assert_eq!(count(&db, "b1").await, (2, 3), "two files, one of them sent twice");

        see(&db, 3).await;
        assert_eq!(count(&db, "b1").await, (0, 3));
        assert_eq!(listed(&db, page("b1")).await.seen, 3);
        see(&db, 1).await;
        assert_eq!(count(&db, "b1").await, (0, 3), "the mark never goes back");
        see(&db, 999).await;
        write(&db, vec![sent("b1", "c3", "", &[("/f/c.md", Some("c1"))])]).await;
        assert_eq!(count(&db, "b1").await, (1, 4), "nor past the latest send: a later one is new");
        assert_eq!(count(&db, "other").await, (0, 0));

        db.call(|conn| forget(conn, "b1")).await.unwrap();
        assert_eq!(count(&db, "b1").await, (0, 0), "a deleted bot's are gone");
        assert!(listed(&db, page("b1")).await.files.is_empty());
    }

    #[tokio::test]
    async fn a_file_a_routine_run_wrote_is_the_runs_whenever_it_is_sent() {
        let dir = tempfile::tempdir().unwrap();
        let db = db(&dir);
        let ran = |run: &str, started: &str, files: &[(&str, &str)]| RunWrote {
            bot: "b1".into(),
            routine: "rtn_gh".into(),
            name: "GitHub趋势早报".into(),
            run: run.into(),
            started: started.into(),
            files: files.iter().map(|(p, sha)| (std::path::PathBuf::from(p), sha.to_string())).collect(),
        };
        let save = |wrote: RunWrote| {
            let db = db.clone();
            async move { db.call(move |conn| save_run_files(conn, &wrote)).await.unwrap() }
        };
        // Sent before the run's end was saved, as the report reaches the bot first.
        write(&db, vec![sent("b1", "c1", "早报", &[("/f/brief.md", Some("g1"))]), sent("b1", "c", "旧的", &[("/f/old.md", Some("o1"))])]).await;
        save(ran("run_1", "2026-11-16T10:00:02Z", &[("/f/brief.md", "g1"), ("/f/old.md", "o1")])).await;
        // And after: the owner asked for it again later in the conversation.
        save(ran("run_2", "2026-11-16T10:00:05Z", &[("/f/brief2.md", "g2")])).await;
        write(&db, vec![sent("b1", "c4444", "今天的", &[("/f/brief2.md", Some("g2")), ("/f/mine.md", Some("m1"))])]).await;

        let result = listed(&db, page("b1")).await;
        let source = |name: &str| {
            let file = result.files.iter().find(|f| f.name == name).unwrap();
            file.versions[0].source.as_ref().map(|s| (s.id.clone(), s.name.clone(), s.run.clone()))
        };
        assert_eq!(source("brief.md"), Some(("rtn_gh".into(), "GitHub趋势早报".into(), Some("run_1".into()))));
        assert_eq!(source("brief2.md"), Some(("rtn_gh".into(), "GitHub趋势早报".into(), Some("run_2".into()))));
        assert_eq!(source("old.md"), None, "the same content sent before the run started is not its");
        assert_eq!(source("mine.md"), None, "a file it did not write is not its, though sent with one");
    }

    #[tokio::test]
    async fn the_list_shows_one_sources_files_and_says_which_sources_there_are() {
        let dir = tempfile::tempdir().unwrap();
        let db = db(&dir);
        let from = |mut sent: Sent, kind: DeliverySourceKind, id: &str, name: &str| {
            sent.source = Some(DeliverySource { kind, id: id.into(), name: name.into(), run: None });
            sent
        };
        db.call(|conn| {
            for (id, name, deleted) in [("rtn_gh", "GitHub趋势早报", None), ("rtn_ph", "PH周报", Some("2026-11-16T00:00:00Z")), ("rtn_quiet", "补货提醒", None), ("rtn_x", "别人的", None)] {
                let bot = if id == "rtn_x" { "b2" } else { "b1" };
                conn.execute(
                    "INSERT INTO routines (id, bot_id, name, prompt, schedule, when_text, enabled, created_at, updated_at, deleted_at) VALUES (?1, ?2, ?3, '', 'daily 08:00', '', 1, '', '', ?4)",
                    params![id, bot, name, deleted],
                )?;
            }
            Ok::<_, rusqlite::Error>(())
        })
        .await
        .unwrap();
        write(
            &db,
            vec![
                from(sent("b1", "c1", "", &[("/f/ph.md", Some("p1"))]), DeliverySourceKind::Routine, "rtn_ph", "PH周报"),
                sent("b1", "c2", "", &[("/f/plan.pdf", Some("x1"))]),
                from(sent("b1", "c3", "", &[("/f/api.md", Some("a1"))]), DeliverySourceKind::Session, "ses_1", "Claude Code in repo"),
                from(sent("b1", "c4", "", &[("/f/api2.md", Some("a2"))]), DeliverySourceKind::Session, "ses_2", "Claude Code in repo"),
                from(sent("b1", "c5", "", &[("/f/gh.md", Some("g1")), ("/f/gh.png", Some("g2"))]), DeliverySourceKind::Routine, "rtn_gh", "旧名字"),
                // The plan again, now from the routine: a file is its latest send's.
                from(sent("b1", "c6", "", &[("/f/plan.pdf", Some("x2"))]), DeliverySourceKind::Routine, "rtn_gh", "旧名字"),
            ],
        )
        .await;

        let only = |source: DeliverySourceFilter| listed(&db, DeliveriesListParams { source: Some(source), ..page("b1") });
        assert_eq!(names(&only(DeliverySourceFilter::Routine { id: "rtn_gh".into() }).await), ["plan.pdf", "gh.png", "gh.md"]);
        assert_eq!(names(&only(DeliverySourceFilter::Conversation).await), Vec::<&str>::new(), "the plan is the routine's now");
        assert_eq!(names(&only(DeliverySourceFilter::Session { name: "Claude Code in repo".into() }).await), ["api2.md", "api.md"], "sessions by their name");
        let both = listed(&db, DeliveriesListParams { source: Some(DeliverySourceFilter::Routine { id: "rtn_gh".into() }), media: Some(true), ..page("b1") }).await;
        assert_eq!(names(&both), ["gh.png"], "with the other filters");

        let found = db.call(|conn| sources(conn, "b1")).await.unwrap();
        let rows: Vec<_> = found.sources.iter().map(|s| (s.filter.clone(), s.name.as_deref(), s.files, s.media, s.gone)).collect();
        assert_eq!(
            rows,
            [
                (DeliverySourceFilter::Routine { id: "rtn_gh".into() }, Some("GitHub趋势早报"), 3, 1, false),
                (DeliverySourceFilter::Session { name: "Claude Code in repo".into() }, Some("Claude Code in repo"), 2, 0, false),
                (DeliverySourceFilter::Routine { id: "rtn_ph".into() }, Some("PH周报"), 1, 0, true),
            ],
            "the latest first; a routine by its name now; a deleted one stays; the conversation has none now"
        );
        assert_eq!(found.silent, 1, "the routine that sent nothing; another bot's is not counted");
    }
}
