//! A bot's routines (docs/kernel/routine-design.md §3, §5): what they are and what their runs came
//! to, in `her.db`; the loop that starts them as they come due; and what the kernel's
//! `manage_routine` and the `bots/routine*` methods ask of them. When a schedule comes up is
//! `schedule.rs`'s business, and carrying out a run the kernel's.

use async_trait::async_trait;
use her_core::HerHome;
use her_core::JsonlStore;
use her_core::bot::SEND_TO_USER;
use her_core::routine::NewRoutine;
use her_core::routine::RecentRun;
use her_core::routine::RoutineChange;
use her_core::routine::RoutineDetail as ToolDetail;
use her_core::routine::RoutineHost;
use her_core::routine::RoutineLine;
use her_core::routine::RoutineRun;
use her_core::routine::RoutineSaved;
use her_core::routine::RunOutcome;
use her_llm::UsageTotals;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::bot::BotRoutineResult;
use her_protocol::bot::BotRoutineRunResult;
use her_protocol::bot::BotRoutineRunsResult;
use her_protocol::bot::RoutineDetail;
use her_protocol::bot::RoutineOutcome;
use her_protocol::bot::RoutineRunBrief;
use her_protocol::bot::RoutineRunInfo;
use her_protocol::bot::RoutineState;
use her_protocol::bot::RoutineSummary;
use her_rpc::RpcError;
use jiff::SignedDuration;
use jiff::Timestamp;
use jiff::tz::TimeZone;
use parking_lot::Mutex;
use rusqlite::Connection;
use rusqlite::OptionalExtension as _;
use rusqlite::Row;
use rusqlite::params;
use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::OnceLock;
use std::sync::Weak;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::AtomicUsize;
use std::sync::atomic::Ordering;
use std::time::Duration;
use tokio::sync::Notify;
use tokio::sync::oneshot;
use tokio_util::sync::CancellationToken;

use crate::bots::Registry;
use crate::db::Db;
use crate::errors;
use crate::hub::HubCmd;
use crate::hub::HubHandle;
use crate::schedule::Schedule;
use crate::schedule::moment_text;
use crate::schedule::runs_text;
use crate::server::Inner;
use crate::server::Server;

/// The first step of the database (routine-design §3.2, §3.3).
pub(crate) const SCHEMA_V1: &str = "
CREATE TABLE routines (
  id          TEXT PRIMARY KEY,
  bot_id      TEXT NOT NULL,
  name        TEXT NOT NULL,
  prompt      TEXT NOT NULL,
  schedule    TEXT NOT NULL,
  when_text   TEXT NOT NULL,
  enabled     INTEGER NOT NULL,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  last_slot   TEXT,
  failures    INTEGER NOT NULL DEFAULT 0,
  finished_at TEXT,
  deleted_at  TEXT
) STRICT;
CREATE INDEX routines_by_bot ON routines(bot_id) WHERE deleted_at IS NULL;
CREATE TABLE routine_runs (
  id         TEXT PRIMARY KEY,
  routine_id TEXT NOT NULL REFERENCES routines(id),
  bot_id     TEXT NOT NULL,
  scheduled  TEXT NOT NULL,
  started    TEXT,
  ended      TEXT,
  outcome    TEXT,
  note       TEXT,
  report     TEXT,
  delivered  TEXT,
  log        TEXT,
  usage      TEXT
) STRICT;
CREATE INDEX runs_by_routine ON routine_runs(routine_id, id DESC);
";

/// The longest the clock goes unread. Timers stand still while macOS sleeps, so waiting for the
/// next run alone would be late by however long the computer slept (routine-design D-421).
const CHECK_EVERY: Duration = Duration::from_secs(30);
/// How far past its time a run counts as late, which it is told.
const LATE_AFTER: SignedDuration = SignedDuration::from_mins(2);
/// Next runs shown for a routine.
const UPCOMING: usize = 3;
/// Runs `get` tells the model about, and how much of what each said.
const RECENT: u32 = 5;
const RECENT_CHARS: usize = 200;
/// Runs whose logs are kept per routine. The records stay. Only so that a routine that runs every
/// ten minutes does not leave fifty thousand files a year; no measurement says this is the number.
const LOGS_KEPT: i64 = 50;
/// A page of `bots/routine_runs`, when the client does not say.
const RUNS_PAGE: u32 = 20;
/// What a skipped run's record says.
const SKIPPED_NOTE: &str = "the run before it had not finished";

/// Why something cannot be done; in words for the model, which reads them as a tool's error.
#[derive(Clone, Debug, PartialEq, Eq)]
enum RoutineError {
    NotFound(String),
    Busy,
    Unavailable(String),
    /// The model can do it otherwise.
    Invalid(String),
}

impl RoutineError {
    fn for_model(self) -> String {
        match self {
            RoutineError::NotFound(id) => format!("No routine with id \"{id}\". Your routines are in the latest <environment_update>."),
            RoutineError::Busy => "It is running now.".to_string(),
            RoutineError::Unavailable(why) => format!("Routines cannot be used right now: {why}"),
            RoutineError::Invalid(why) => why,
        }
    }

    fn for_client(self) -> RpcError {
        match self {
            RoutineError::NotFound(id) => errors::routine_not_found(&id),
            RoutineError::Busy => errors::routine_busy(),
            RoutineError::Unavailable(why) => errors::routines_unavailable(why),
            RoutineError::Invalid(why) => RpcError::invalid_params(why),
        }
    }
}

impl From<crate::db::DbError> for RoutineError {
    fn from(error: crate::db::DbError) -> Self {
        RoutineError::Unavailable(error.to_string())
    }
}

/// A routine as the scheduler keeps it. The database is where it lasts; what it says here it has
/// already said there.
#[derive(Clone, Debug)]
struct Routine {
    id: String,
    bot: String,
    name: String,
    prompt: String,
    schedule: String,
    when: String,
    enabled: bool,
    created_at: String,
    /// Dealt with up to here: nothing at or before it comes due. Moved on to now when the
    /// schedule changes or the routine resumes, so that what passed meanwhile is not made up.
    last_slot: Timestamp,
    /// Failures in a row (routine-design D-423).
    failures: u32,
    finished: bool,
    /// A run under way: what stops it.
    running: Option<CancellationToken>,
    /// The latest run that started, and its id.
    last_run: Option<(String, RoutineRunBrief)>,
}

impl Routine {
    fn active(&self) -> bool {
        self.enabled && !self.finished
    }

    fn state(&self) -> RoutineState {
        match (self.finished, self.enabled) {
            (true, _) => RoutineState::Finished,
            (false, true) => RoutineState::Active,
            (false, false) => RoutineState::Paused,
        }
    }

    /// Its schedule. Saved ones always read; one written by another version might not.
    fn parsed(&self, now: Timestamp, local: &TimeZone) -> Option<Schedule> {
        match Schedule::parse(&self.schedule, now, local) {
            Ok(schedule) => Some(schedule),
            Err(err) => {
                tracing::warn!(routine = %self.id, "{err}");
                None
            }
        }
    }

    fn upcoming(&self, now: Timestamp, local: &TimeZone) -> Vec<Timestamp> {
        if !self.active() {
            return Vec::new();
        }
        self.parsed(now, local).map_or_else(Vec::new, |schedule| schedule.upcoming(now, UPCOMING, local))
    }

    fn summary(&self, now: Timestamp, local: &TimeZone) -> RoutineSummary {
        RoutineSummary {
            id: self.id.clone(),
            name: self.name.clone(),
            when: self.when.clone(),
            state: self.state(),
            running: self.running.is_some(),
            next_run: self.upcoming(now, local).first().copied().map(stamp),
            last_run: self.last_run.as_ref().map(|(_, brief)| brief.clone()),
        }
    }

    fn saved(&self, now: Timestamp, local: &TimeZone) -> RoutineSaved {
        let upcoming = self.upcoming(now, local);
        RoutineSaved {
            id: self.id.clone(),
            name: self.name.clone(),
            when: self.when.clone(),
            next_run: upcoming.first().copied().map(stamp),
            upcoming: runs_text(&upcoming, local),
        }
    }
}

/// How the routines stand with their database.
enum Store {
    /// This service does not keep routines: only one that stays up does.
    Off,
    /// No file yet, so no routines; the first one makes it.
    Empty,
    Open(Db),
    Failed(String),
}

struct State {
    store: Store,
    routines: Vec<Routine>,
    /// Reports not yet followed by a message to the user: the runs, by bot (routine-design §5.8).
    /// Empty most of the time, so that ordinary chat never touches the database.
    undelivered: HashMap<String, Vec<String>>,
    /// Held while the database is open: one service schedules from it.
    _lock: Option<std::fs::File>,
}

pub(crate) struct Routines {
    home: HerHome,
    bots: Arc<Registry>,
    server: OnceLock<Weak<Inner>>,
    /// This, for what starts from a shared reference.
    this: Weak<Routines>,
    /// Never held across an await.
    state: Mutex<State>,
    /// One change at a time, its database part included, so that the memory and the file agree.
    changing: Arc<tokio::sync::Mutex<()>>,
    /// Something the scheduler waits for changed.
    wake: Notify,
    /// What ends the scheduler; set by `start`, and it starts when there is a database.
    stop: OnceLock<CancellationToken>,
    closing: AtomicBool,
    /// Runs whose ends are not saved yet.
    open_runs: AtomicUsize,
    runs_saved: Notify,
}

/// A run the scheduler has committed to.
#[derive(Clone, Debug)]
struct Fire {
    run: RoutineRun,
    bot: String,
    stop: CancellationToken,
    /// Started from the panel: it says nothing about when the routine comes due.
    manual: bool,
}

/// `2026-11-05T01:00:00Z`: how the database and the protocol write a moment.
fn stamp(at: Timestamp) -> String {
    at.round(jiff::Unit::Second).unwrap_or(at).to_string()
}

fn outcome_text(outcome: RoutineOutcome) -> &'static str {
    match outcome {
        RoutineOutcome::Reported => "reported",
        RoutineOutcome::Quiet => "quiet",
        RoutineOutcome::Failed => "failed",
        RoutineOutcome::Interrupted => "interrupted",
        RoutineOutcome::Skipped => "skipped",
    }
}

fn outcome_of(text: &str) -> Option<RoutineOutcome> {
    Some(match text {
        "reported" => RoutineOutcome::Reported,
        "quiet" => RoutineOutcome::Quiet,
        "failed" => RoutineOutcome::Failed,
        "interrupted" => RoutineOutcome::Interrupted,
        "skipped" => RoutineOutcome::Skipped,
        _ => return None,
    })
}

/// `r_` and six lowercase letters and digits, from the random end of a UUID.
fn new_routine_id() -> String {
    const DIGITS: &[u8] = b"abcdefghijklmnopqrstuvwxyz0123456789";
    let mut bits = uuid::Uuid::now_v7().as_u128();
    let mut id = String::from("r_");
    for _ in 0..6 {
        id.push(DIGITS[(bits % DIGITS.len() as u128) as usize] as char);
        bits /= DIGITS.len() as u128;
    }
    id
}

fn is_duplicate(error: &rusqlite::Error) -> bool {
    matches!(error, rusqlite::Error::SqliteFailure(e, _) if e.code == rusqlite::ErrorCode::ConstraintViolation)
}

// ---- SQL. Each function that writes more than one row is one transaction. ----

fn routine_from(row: &Row<'_>, now: Timestamp) -> rusqlite::Result<Routine> {
    let last_slot: Option<String> = row.get("last_slot")?;
    let updated_at: String = row.get("updated_at")?;
    let finished_at: Option<String> = row.get("finished_at")?;
    Ok(Routine {
        id: row.get("id")?,
        bot: row.get("bot_id")?,
        name: row.get("name")?,
        prompt: row.get("prompt")?,
        schedule: row.get("schedule")?,
        when: row.get("when_text")?,
        enabled: row.get("enabled")?,
        created_at: row.get("created_at")?,
        last_slot: last_slot.unwrap_or(updated_at).parse().unwrap_or(now),
        failures: row.get("failures")?,
        finished: finished_at.is_some(),
        running: None,
        last_run: None,
    })
}

fn read_routines(conn: &Connection, now: Timestamp) -> rusqlite::Result<Vec<Routine>> {
    let mut statement = conn.prepare("SELECT * FROM routines WHERE deleted_at IS NULL ORDER BY created_at, id")?;
    let mut routines = statement.query_map([], |row| routine_from(row, now))?.collect::<rusqlite::Result<Vec<_>>>()?;
    let mut last = conn.prepare(
        "SELECT id, started, outcome, delivered FROM routine_runs WHERE routine_id = ?1 AND started IS NOT NULL ORDER BY id DESC LIMIT 1",
    )?;
    for routine in &mut routines {
        routine.last_run = last
            .query_row([&routine.id], |row| {
                let outcome: Option<String> = row.get("outcome")?;
                Ok((
                    row.get("id")?,
                    RoutineRunBrief {
                        started: row.get("started")?,
                        outcome: outcome.as_deref().and_then(outcome_of),
                        delivered: row.get("delivered")?,
                    },
                ))
            })
            .optional()?;
    }
    Ok(routines)
}

/// What a crash left: runs that were open did not finish, and a one-off whose time was dealt with
/// has had its one go. Nothing runs again by itself (routine-design §5.2).
fn recover(conn: &mut Connection, now: Timestamp, local: &TimeZone) -> rusqlite::Result<Vec<Routine>> {
    let at = stamp(now);
    let tx = conn.transaction()?;
    tx.execute("UPDATE routine_runs SET outcome = 'interrupted', ended = ?1 WHERE outcome IS NULL", [&at])?;
    let mut routines = read_routines(&tx, now)?;
    for routine in &mut routines {
        let spent = matches!(Schedule::parse(&routine.schedule, now, local), Ok(Schedule::Once(moment)) if moment <= routine.last_slot);
        if spent && !routine.finished {
            routine.finished = true;
            routine.enabled = false;
            tx.execute("UPDATE routines SET finished_at = ?1, enabled = 0 WHERE id = ?2", params![at, routine.id])?;
        }
    }
    tx.commit()?;
    Ok(routines)
}

/// Saves a new routine under a free id, which it returns.
fn insert_routine(conn: &Connection, routine: &Routine, at: &str) -> rusqlite::Result<String> {
    loop {
        let id = new_routine_id();
        let inserted = conn.execute(
            "INSERT INTO routines (id, bot_id, name, prompt, schedule, when_text, enabled, created_at, updated_at, last_slot)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, 1, ?7, ?7, ?8)",
            params![id, routine.bot, routine.name, routine.prompt, routine.schedule, routine.when, at, stamp(routine.last_slot)],
        );
        match inserted {
            Ok(_) => return Ok(id),
            Err(err) if is_duplicate(&err) => {}
            Err(err) => return Err(err),
        }
    }
}

fn save_definition(conn: &Connection, routine: &Routine, at: &str) -> rusqlite::Result<()> {
    conn.execute(
        "UPDATE routines SET name = ?2, prompt = ?3, schedule = ?4, when_text = ?5, enabled = ?6, updated_at = ?7, last_slot = ?8,
         finished_at = CASE WHEN ?9 THEN COALESCE(finished_at, ?7) END WHERE id = ?1",
        params![
            routine.id,
            routine.name,
            routine.prompt,
            routine.schedule,
            routine.when,
            routine.enabled,
            at,
            stamp(routine.last_slot),
            routine.finished
        ],
    )?;
    Ok(())
}

/// A run begins: `slot` is dealt with and the record opens, together. A manual run moves nothing.
fn save_start(conn: &mut Connection, routine: &str, bot: &str, run: &str, slot: Timestamp, started: &str, manual: bool) -> rusqlite::Result<()> {
    let tx = conn.transaction()?;
    if !manual {
        tx.execute("UPDATE routines SET last_slot = ?2 WHERE id = ?1", params![routine, stamp(slot)])?;
    }
    tx.execute(
        "INSERT INTO routine_runs (id, routine_id, bot_id, scheduled, started) VALUES (?1, ?2, ?3, ?4, ?5)",
        params![run, routine, bot, stamp(slot), started],
    )?;
    tx.commit()
}

fn save_skip(conn: &mut Connection, routine: &str, bot: &str, run: &str, slot: Timestamp, at: &str) -> rusqlite::Result<()> {
    let tx = conn.transaction()?;
    tx.execute("UPDATE routines SET last_slot = ?2 WHERE id = ?1", params![routine, stamp(slot)])?;
    tx.execute(
        "INSERT INTO routine_runs (id, routine_id, bot_id, scheduled, ended, outcome, note) VALUES (?1, ?2, ?3, ?4, ?5, 'skipped', ?6)",
        params![run, routine, bot, stamp(slot), at, SKIPPED_NOTE],
    )?;
    tx.commit()
}

/// What a run came to, in one go with what it changes in its routine.
struct RunEnd {
    run: String,
    routine: String,
    at: String,
    outcome: RoutineOutcome,
    note: Option<String>,
    report: Option<String>,
    log: Option<String>,
    usage: Option<String>,
    failures: u32,
    finishes: bool,
}

fn save_end(conn: &mut Connection, end: &RunEnd) -> rusqlite::Result<()> {
    let tx = conn.transaction()?;
    tx.execute(
        "UPDATE routine_runs SET ended = ?2, outcome = ?3, note = ?4, report = ?5, log = ?6, usage = ?7 WHERE id = ?1",
        params![end.run, end.at, outcome_text(end.outcome), end.note, end.report, end.log, end.usage],
    )?;
    tx.execute("UPDATE routines SET failures = ?2 WHERE id = ?1", params![end.routine, end.failures])?;
    if end.finishes {
        tx.execute("UPDATE routines SET finished_at = ?2, enabled = 0 WHERE id = ?1", params![end.routine, end.at])?;
    }
    tx.commit()?;
    trim_logs(conn, &end.routine)
}

/// The logs past the newest ones go, with their data folders; the records stay.
fn trim_logs(conn: &Connection, routine: &str) -> rusqlite::Result<()> {
    let mut statement =
        conn.prepare_cached("SELECT id, log FROM routine_runs WHERE routine_id = ?1 AND log IS NOT NULL ORDER BY id DESC LIMIT -1 OFFSET ?2")?;
    let old = statement
        .query_map(params![routine, LOGS_KEPT], |row| Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?)))?
        .collect::<rusqlite::Result<Vec<_>>>()?;
    for (run, log) in old {
        let log = PathBuf::from(log);
        if let Err(err) = std::fs::remove_file(&log) {
            tracing::debug!(%err, path = %log.display(), "a run's log was already gone");
        }
        let (data, _) = HerHome::session_data_dir(&log, &run);
        let _ = std::fs::remove_dir_all(data);
        conn.execute("UPDATE routine_runs SET log = NULL WHERE id = ?1", [run])?;
    }
    Ok(())
}

/// How many characters of text fit in a line about it.
fn start_of(text: &str, chars: usize) -> String {
    let line = text.split_whitespace().collect::<Vec<_>>().join(" ");
    match line.char_indices().nth(chars) {
        Some((end, _)) => format!("{}…", &line[..end]),
        None => line,
    }
}

const RUN_COLUMNS: &str = "id, scheduled, started, ended, outcome, note, report, delivered, log, usage";

fn run_from(row: &Row<'_>) -> rusqlite::Result<RoutineRunInfo> {
    let scheduled: String = row.get("scheduled")?;
    let started: Option<String> = row.get("started")?;
    let outcome: Option<String> = row.get("outcome")?;
    let log: Option<String> = row.get("log")?;
    let usage: Option<String> = row.get("usage")?;
    let late = match (scheduled.parse::<Timestamp>(), started.as_deref().map(str::parse::<Timestamp>)) {
        (Ok(due), Some(Ok(began))) => began.duration_since(due) > LATE_AFTER,
        _ => false,
    };
    Ok(RoutineRunInfo {
        id: row.get("id")?,
        scheduled,
        started,
        ended: row.get("ended")?,
        outcome: outcome.as_deref().and_then(outcome_of),
        note: row.get("note")?,
        report: row.get("report")?,
        delivered: row.get("delivered")?,
        late,
        has_log: log.is_some(),
        usage: usage.as_deref().and_then(|text| serde_json::from_str::<UsageTotals>(text).ok()),
    })
}

/// Runs of a routine, newest first.
fn read_runs(conn: &Connection, routine: &str, before: Option<&str>, limit: u32) -> rusqlite::Result<Vec<RoutineRunInfo>> {
    let sql = format!(
        "SELECT {RUN_COLUMNS} FROM routine_runs WHERE routine_id = ?1 AND (?2 IS NULL OR id < ?2) ORDER BY id DESC LIMIT ?3"
    );
    let mut statement = conn.prepare_cached(&sql)?;
    statement.query_map(params![routine, before, limit], run_from)?.collect()
}

// ---- The scheduler. ----

impl Routines {
    pub(crate) fn new(home: HerHome, bots: Arc<Registry>) -> Arc<Self> {
        Arc::new_cyclic(|this| Self {
            home,
            bots,
            server: OnceLock::new(),
            this: this.clone(),
            state: Mutex::new(State {
                store: Store::Off,
                routines: Vec::new(),
                undelivered: HashMap::new(),
                _lock: None,
            }),
            changing: Arc::default(),
            wake: Notify::new(),
            stop: OnceLock::new(),
            closing: AtomicBool::new(false),
            open_runs: AtomicUsize::new(0),
            runs_saved: Notify::new(),
        })
    }

    pub(crate) fn bind(&self, server: Weak<Inner>) {
        let _ = self.server.set(server);
    }

    fn server(&self) -> Option<Server> {
        self.server.get().and_then(Weak::upgrade).map(Server)
    }

    fn db_path(&self) -> PathBuf {
        self.home.root().join("her.db")
    }

    /// Opens the database with its lock, so that a second service on this folder cannot start
    /// the same routines. Blocking I/O.
    fn open(path: PathBuf) -> Result<(Db, std::fs::File), String> {
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).map_err(|e| format!("cannot create {}: {e}", dir.display()))?;
        }
        let mut lock_path = path.clone().into_os_string();
        lock_path.push(".lock");
        let lock = std::fs::OpenOptions::new()
            .create(true)
            .truncate(false)
            .write(true)
            .open(&lock_path)
            .map_err(|e| format!("cannot open its lock file: {e}"))?;
        match lock.try_lock() {
            Ok(()) => {}
            Err(std::fs::TryLockError::WouldBlock) => return Err("another her service on this data folder keeps the routines".to_string()),
            Err(std::fs::TryLockError::Error(err)) => return Err(format!("cannot lock it: {err}")),
        }
        let db = Db::open(&path).map_err(|e| e.to_string())?;
        Ok((db, lock))
    }

    /// Starts keeping routines, in the service that stays up. Without a database there are none,
    /// and a service that never has any does nothing for them: not a task, not a thread. With one
    /// it is read off the caller's way, as nothing waits for it to start serving.
    pub(crate) fn start(self: &Arc<Self>, stop: CancellationToken) {
        let _ = self.stop.set(stop);
        if !self.db_path().exists() {
            self.state.lock().store = Store::Empty;
            return;
        }
        let Ok(changing) = self.changing.clone().try_lock_owned() else {
            return;
        };
        let routines = self.clone();
        tokio::spawn(async move {
            routines.load().await;
            drop(changing);
            routines.schedule().await;
        });
    }

    /// Reads a database that is there.
    async fn load(&self) {
        let path = self.db_path();
        let bots = self.bots.clone();
        let opened = tokio::task::spawn_blocking(move || {
            // The list reads from disk the first time; from now on the scheduler can ask
            // without waiting.
            bots.load();
            Routines::open(path)
        })
        .await;
        let (db, lock) = match opened.map_err(|err| err.to_string()).and_then(|opened| opened) {
            Ok(opened) => opened,
            Err(why) => return self.failed(why),
        };
        let now = Timestamp::now();
        let local = TimeZone::system();
        match db.call(move |conn| recover(conn, now, &local)).await {
            Ok(routines) => {
                for routine in routines.iter().filter(|r| !self.bots.exists(&r.bot)) {
                    tracing::warn!(routine = %routine.id, bot = %routine.bot, "a routine of a bot that is not there will not run");
                }
                let mut bots: Vec<String> = routines.iter().map(|r| r.bot.clone()).collect();
                bots.sort_unstable();
                bots.dedup();
                {
                    let mut state = self.state.lock();
                    state.store = Store::Open(db);
                    state._lock = Some(lock);
                    state.routines = routines;
                }
                for bot in bots {
                    self.show(&bot);
                }
            }
            Err(err) => self.failed(err.to_string()),
        }
    }

    /// The database cannot be used: everything else goes on, and clients say why (routine-design §3.6).
    fn failed(&self, why: String) {
        tracing::warn!("routines are not available: {why}");
        self.state.lock().store = Store::Failed(why.clone());
        self.bots.set_routines_error(Some(why));
    }

    fn db(&self) -> Result<Db, RoutineError> {
        match &self.state.lock().store {
            Store::Open(db) => Ok(db.clone()),
            Store::Off => Err(RoutineError::Unavailable("this her process is not the service that stays running".into())),
            Store::Empty => Err(RoutineError::Unavailable("there is no database yet".into())),
            Store::Failed(why) => Err(RoutineError::Unavailable(why.clone())),
        }
    }

    /// The database, made now if this is the first routine.
    async fn db_for_new(&self) -> Result<Db, RoutineError> {
        if !matches!(self.state.lock().store, Store::Empty) {
            return self.db();
        }
        let path = self.db_path();
        let opened = tokio::task::spawn_blocking(move || Routines::open(path))
            .await
            .map_err(|e| e.to_string())
            .and_then(|opened| opened);
        match opened {
            Ok((db, lock)) => {
                {
                    let mut state = self.state.lock();
                    state.store = Store::Open(db.clone());
                    state._lock = Some(lock);
                }
                // The first routine: from now on there is something to schedule.
                if let Some(routines) = self.this.upgrade() {
                    tokio::spawn(routines.schedule());
                }
                Ok(db)
            }
            Err(why) => {
                self.failed(why.clone());
                Err(RoutineError::Unavailable(why))
            }
        }
    }

    async fn schedule(self: Arc<Self>) {
        // Only the service that stays up keeps time.
        let Some(stop) = self.stop.get().cloned() else {
            return;
        };
        loop {
            let now = Timestamp::now();
            let (fires, next) = self.check(now).await;
            for fire in fires {
                self.launch(fire);
            }
            let wait = next.map(|at| {
                let until = at.duration_since(now).unsigned_abs();
                until.min(CHECK_EVERY)
            });
            tokio::select! {
                _ = stop.cancelled() => return,
                _ = self.wake.notified() => {}
                _ = async {
                    match wait {
                        Some(wait) => tokio::time::sleep(wait).await,
                        None => std::future::pending().await,
                    }
                } => {}
            }
            if self.closing.load(Ordering::Relaxed) {
                return;
            }
        }
    }

    /// One look at the clock: commits to what has come due, and says when to look next (routine-design §5.2).
    async fn check(&self, now: Timestamp) -> (Vec<Fire>, Option<Timestamp>) {
        let local = TimeZone::system();
        let due: Vec<(String, String, Timestamp)> = {
            let state = self.state.lock();
            state
                .routines
                .iter()
                .filter(|routine| routine.active())
                .filter_map(|routine| {
                    let slot = routine.parsed(now, &local)?.due(routine.last_slot, now, &local)?;
                    Some((routine.id.clone(), routine.bot.clone(), slot))
                })
                .collect()
        };
        let mut fires = Vec::new();
        for (id, bot, slot) in due {
            if !self.bots.exists(&bot) {
                continue;
            }
            match self.fire(&id, slot, now, &local).await {
                Ok(fire) => fires.extend(fire),
                // Nothing runs on what was not written down: the next look tries again.
                Err(err) => tracing::warn!(routine = %id, "cannot save a run about to start: {err:?}"),
            }
        }
        let state = self.state.lock();
        let next = state
            .routines
            .iter()
            .filter(|routine| routine.active())
            .filter_map(|routine| routine.parsed(now, &local)?.next_after(now, &local))
            .min();
        (fires, next)
    }

    /// Writes down that `slot` has come, then says what to run: nothing when the last run still
    /// goes on, which is recorded instead (routine-design §5.4).
    async fn fire(&self, id: &str, slot: Timestamp, now: Timestamp, local: &TimeZone) -> Result<Option<Fire>, RoutineError> {
        let _changing = self.changing.lock().await;
        let db = self.db()?;
        let Some(routine) = self.find_any(id) else {
            return Ok(None);
        };
        if !routine.active() {
            return Ok(None);
        }
        if routine.running.is_some() {
            // A one-off gets its one go, after the test run from the panel.
            if routine.parsed(now, local).is_some_and(|schedule| schedule.is_once()) {
                return Ok(None);
            }
            let run = format!("run_{}", uuid::Uuid::now_v7());
            let (routine_id, bot, at) = (routine.id.clone(), routine.bot.clone(), stamp(now));
            db.call(move |conn| save_skip(conn, &routine_id, &bot, &run, slot, &at)).await?;
            self.update(id, |routine| routine.last_slot = slot);
            self.show(&routine.bot);
            return Ok(None);
        }
        self.begin(db, routine, slot, now, local, false).await.map(Some)
    }

    /// Opens a run's record and marks the routine busy. The caller holds `changing`.
    async fn begin(&self, db: Db, routine: Routine, slot: Timestamp, now: Timestamp, local: &TimeZone, manual: bool) -> Result<Fire, RoutineError> {
        let run_id = format!("run_{}", uuid::Uuid::now_v7());
        let started = stamp(now);
        {
            let (routine_id, bot, run, started) = (routine.id.clone(), routine.bot.clone(), run_id.clone(), started.clone());
            db.call(move |conn| save_start(conn, &routine_id, &bot, &run, slot, &started, manual)).await?;
        }
        let stop = CancellationToken::new();
        self.update(&routine.id, |routine| {
            if !manual {
                routine.last_slot = slot;
            }
            routine.running = Some(stop.clone());
            routine.last_run = Some((run_id.clone(), RoutineRunBrief { started: started.clone(), outcome: None, delivered: None }));
        });
        self.show(&routine.bot);
        Ok(Fire {
            run: RoutineRun {
                id: run_id,
                routine_id: routine.id,
                name: routine.name,
                prompt: routine.prompt,
                scheduled: moment_text(slot, local),
                now: moment_text(now, local),
                late: now.duration_since(slot) > LATE_AFTER,
                report_failure: routine.failures == 0,
            },
            bot: routine.bot,
            stop,
            manual,
        })
    }

    /// Carries a run out in the background and saves what it came to.
    fn launch(self: &Arc<Self>, fire: Fire) {
        let Some(server) = self.server() else {
            return;
        };
        self.open_runs.fetch_add(1, Ordering::AcqRel);
        let routines = self.clone();
        tokio::spawn(async move {
            let (ended, told) = server.carry_out_routine_run(&fire).await;
            routines.finish(&fire, ended, told, Timestamp::now()).await;
            routines.open_runs.fetch_sub(1, Ordering::AcqRel);
            routines.runs_saved.notify_waiters();
        });
    }

    /// Saves what a run came to, and what that changes in its routine. `told`: the bot heard if
    /// it failed, so the next failure need not be told (routine-design D-423).
    async fn finish(&self, fire: &Fire, ended: RunOutcome, told: bool, now: Timestamp) {
        let routine_id = fire.run.routine_id.clone();
        let reported = ended.outcome == RoutineOutcome::Reported;
        if reported {
            self.state.lock().undelivered.entry(fire.bot.clone()).or_default().push(fire.run.id.clone());
        }
        let _changing = self.changing.lock().await;
        let local = TimeZone::system();
        let routine = self.find_any(&routine_id);
        let once = routine.as_ref().and_then(|routine| routine.parsed(now, &local)).is_some_and(|schedule| schedule.is_once());
        let failures = routine.as_ref().map_or(0, |routine| match ended.outcome {
            RoutineOutcome::Reported | RoutineOutcome::Quiet => 0,
            RoutineOutcome::Failed if told => routine.failures + 1,
            _ => routine.failures,
        });
        let end = RunEnd {
            run: fire.run.id.clone(),
            routine: routine_id.clone(),
            at: stamp(now),
            outcome: ended.outcome,
            note: ended.note,
            report: ended.report,
            log: ended.log.map(|log| log.to_string_lossy().into_owned()),
            usage: serde_json::to_string(&ended.usage).ok(),
            failures,
            finishes: once && !fire.manual,
        };
        let finishes = end.finishes;
        match self.db() {
            Ok(db) => {
                if let Err(err) = db.call(move |conn| save_end(conn, &end)).await {
                    tracing::warn!(routine = %routine_id, "cannot save how a run ended: {err}");
                }
            }
            Err(err) => tracing::warn!(routine = %routine_id, "cannot save how a run ended: {err:?}"),
        }
        self.update(&routine_id, |routine| {
            routine.failures = failures;
            routine.running = None;
            if let Some((run, brief)) = &mut routine.last_run
                && *run == fire.run.id
            {
                brief.outcome = Some(ended.outcome);
            }
            if finishes {
                routine.finished = true;
                routine.enabled = false;
            }
        });
        self.show(&fire.bot);
        if finishes {
            self.tell_model(&fire.bot).await;
        }
        self.wake.notify_one();
    }

    /// Stops what is running and stops starting more; waits a little for their ends to be saved.
    pub(crate) async fn close(&self) {
        self.closing.store(true, Ordering::Relaxed);
        self.wake.notify_one();
        let deadline = tokio::time::Instant::now() + Duration::from_secs(2);
        loop {
            let saved = self.runs_saved.notified();
            if self.open_runs.load(Ordering::Acquire) == 0 {
                return;
            }
            if tokio::time::timeout_at(deadline, saved).await.is_err() {
                return;
            }
        }
    }

    fn find_any(&self, id: &str) -> Option<Routine> {
        self.state.lock().routines.iter().find(|routine| routine.id == id).cloned()
    }

    /// A routine of `bot` by its id. Without a database there is no telling: that is what to say.
    fn find(&self, bot: &str, id: &str) -> Result<Routine, RoutineError> {
        if let Some(routine) = self.find_any(id).filter(|routine| routine.bot == bot) {
            return Ok(routine);
        }
        match self.db() {
            Err(RoutineError::Unavailable(why)) if !matches!(self.state.lock().store, Store::Empty) => Err(RoutineError::Unavailable(why)),
            _ => Err(RoutineError::NotFound(id.to_string())),
        }
    }

    fn update(&self, id: &str, change: impl FnOnce(&mut Routine)) {
        if let Some(routine) = self.state.lock().routines.iter_mut().find(|routine| routine.id == id) {
            change(routine);
        }
    }

    /// Whether the service has to stay up for what is scheduled (routine-design §7.1).
    pub(crate) fn any_active(&self) -> bool {
        self.state.lock().routines.iter().any(Routine::active)
    }

    /// What the bot's row shows of its routines changed.
    fn show(&self, bot: &str) {
        let now = Timestamp::now();
        let local = TimeZone::system();
        let summaries: Vec<RoutineSummary> = {
            let state = self.state.lock();
            state.routines.iter().filter(|routine| routine.bot == bot).map(|routine| routine.summary(now, &local)).collect()
        };
        self.bots.set_routines(bot, summaries);
    }

    /// What the bot's model is to know of its routines; `None` when routines are not in use here.
    pub(crate) fn listing_of(&self, bot: &str) -> Option<String> {
        let state = self.state.lock();
        if !matches!(state.store, Store::Open(_) | Store::Empty) {
            return None;
        }
        let lines: Vec<RoutineLine> = state
            .routines
            .iter()
            .filter(|routine| routine.bot == bot && !routine.finished)
            .map(|routine| RoutineLine {
                id: routine.id.clone(),
                name: routine.name.clone(),
                when: routine.when.clone(),
                paused: !routine.enabled,
            })
            .collect();
        Some(her_core::routine::listing(&lines))
    }

    /// The bot's loaded conversation hears the list as it is now.
    async fn tell_model(&self, bot: &str) {
        let Some(text) = self.listing_of(bot) else {
            return;
        };
        if let Some(server) = self.server()
            && let Some(hub) = server.bot_hub(bot)
        {
            let _ = hub.send(HubCmd::Routines { text }).await;
        }
    }

    /// An event of a bot's conversation: its message to the user follows the reports that
    /// waited for one. Only "the bot said something": what it said may be about something else
    /// (routine-design §5.8).
    pub(crate) fn on_event(&self, bot: &str, event: &Event) {
        let EventMsg::ToolStarted { name, args, .. } = &event.msg else {
            return;
        };
        if name != SEND_TO_USER || her_core::bot::words_of(args).is_none() {
            return;
        }
        let at = stamp(Timestamp::now());
        let (runs, db) = {
            let mut state = self.state.lock();
            let Some(runs) = state.undelivered.remove(bot) else {
                return;
            };
            for routine in state.routines.iter_mut().filter(|routine| routine.bot == bot) {
                if let Some((run, brief)) = &mut routine.last_run
                    && runs.contains(run)
                {
                    brief.delivered = Some(at.clone());
                }
            }
            let db = match &state.store {
                Store::Open(db) => Some(db.clone()),
                _ => None,
            };
            (runs, db)
        };
        if let Some(db) = db {
            let when = at.clone();
            db.send("a report's delivery", move |conn| {
                let tx = conn.transaction()?;
                for run in &runs {
                    tx.execute("UPDATE routine_runs SET delivered = ?2 WHERE id = ?1", params![run, when])?;
                }
                tx.commit()
            });
        }
        self.show(bot);
    }

    /// The bot is gone, and its routines with it. Their records stay.
    pub(crate) async fn bot_deleted(&self, bot: &str) {
        let _changing = self.changing.lock().await;
        let Ok(db) = self.db() else {
            return;
        };
        let (id, at) = (bot.to_string(), stamp(Timestamp::now()));
        let deleted = db.call(move |conn| conn.execute("UPDATE routines SET deleted_at = ?2 WHERE bot_id = ?1 AND deleted_at IS NULL", params![id, at])).await;
        if let Err(err) = deleted {
            tracing::warn!(%bot, "cannot delete the bot's routines: {err}");
        }
        let mut state = self.state.lock();
        for routine in state.routines.iter().filter(|routine| routine.bot == bot) {
            if let Some(stop) = &routine.running {
                stop.cancel();
            }
        }
        state.routines.retain(|routine| routine.bot != bot);
        state.undelivered.remove(bot);
    }

    // ---- What the tool and the panel ask for. ----

    async fn create_at(&self, bot: &str, new: NewRoutine, now: Timestamp) -> Result<RoutineSaved, RoutineError> {
        let _changing = self.changing.lock().await;
        let local = TimeZone::system();
        let schedule = Schedule::parse_new(&new.schedule, now, &local).map_err(|err| RoutineError::Invalid(err.to_string()))?;
        let db = self.db_for_new().await?;
        let mut routine = Routine {
            id: String::new(),
            bot: bot.to_string(),
            name: new.name,
            prompt: new.prompt,
            schedule: schedule.stored(&local),
            when: new.when,
            enabled: true,
            created_at: stamp(now),
            last_slot: now,
            failures: 0,
            finished: false,
            running: None,
            last_run: None,
        };
        let row = routine.clone();
        routine.id = db.call(move |conn| insert_routine(conn, &row, &stamp(now))).await?;
        let saved = routine.saved(now, &local);
        self.state.lock().routines.push(routine);
        self.changed(bot).await;
        Ok(saved)
    }

    async fn update_at(&self, bot: &str, id: &str, change: RoutineChange, now: Timestamp) -> Result<RoutineSaved, RoutineError> {
        let _changing = self.changing.lock().await;
        let local = TimeZone::system();
        let mut routine = self.find(bot, id)?;
        if let Some(text) = &change.schedule {
            let schedule = Schedule::parse_new(text, now, &local).map_err(|err| RoutineError::Invalid(err.to_string()))?;
            routine.schedule = schedule.stored(&local);
            routine.last_slot = now;
            // A new time for a one-off that has run: it goes again.
            if routine.finished {
                routine.finished = false;
                routine.enabled = true;
            }
        }
        if let Some(name) = change.name {
            routine.name = name;
        }
        if let Some(prompt) = change.prompt {
            routine.prompt = prompt;
        }
        if let Some(when) = change.when {
            routine.when = when;
        }
        self.save(routine, now, &local, bot).await
    }

    /// Writes a definition, then takes it over; what only the scheduler changes stays.
    async fn save(&self, routine: Routine, now: Timestamp, local: &TimeZone, bot: &str) -> Result<RoutineSaved, RoutineError> {
        let db = self.db()?;
        let row = routine.clone();
        db.call(move |conn| save_definition(conn, &row, &stamp(now))).await?;
        let saved = routine.saved(now, local);
        let id = routine.id.clone();
        self.update(&id, |kept| {
            let (running, last_run, failures) = (kept.running.take(), kept.last_run.take(), kept.failures);
            *kept = Routine { running, last_run, failures, ..routine };
        });
        self.changed(bot).await;
        Ok(saved)
    }

    /// Clients and the model hear of a change, and the scheduler looks again.
    async fn changed(&self, bot: &str) {
        self.show(bot);
        self.tell_model(bot).await;
        self.wake.notify_one();
    }

    async fn set_enabled_at(&self, bot: &str, id: &str, enabled: bool, now: Timestamp) -> Result<RoutineSaved, RoutineError> {
        let _changing = self.changing.lock().await;
        let local = TimeZone::system();
        let mut routine = self.find(bot, id)?;
        if routine.enabled == enabled && !routine.finished {
            return Ok(routine.saved(now, &local));
        }
        if enabled {
            if routine.finished {
                return Err(RoutineError::Invalid("This one-time routine has already run. Give it a new time with update to run it again.".into()));
            }
            // What passed while it was paused is not made up.
            routine.last_slot = now;
            if routine.upcoming_after_resume(now, &local).is_none() {
                return Err(RoutineError::Invalid(format!(
                    "Its time passed while it was paused: it is now {}. Give it a new time with update.",
                    moment_text(now, &local)
                )));
            }
        }
        routine.enabled = enabled;
        self.save(routine, now, &local, bot).await
    }

    async fn delete_at(&self, bot: &str, id: &str, now: Timestamp) -> Result<RoutineSaved, RoutineError> {
        let _changing = self.changing.lock().await;
        let local = TimeZone::system();
        let routine = self.find(bot, id)?;
        let db = self.db()?;
        let (row, at) = (routine.id.clone(), stamp(now));
        db.call(move |conn| conn.execute("UPDATE routines SET deleted_at = ?2 WHERE id = ?1", params![row, at])).await?;
        if let Some(stop) = &routine.running {
            stop.cancel();
        }
        self.state.lock().routines.retain(|kept| kept.id != routine.id);
        self.changed(bot).await;
        // A deleted routine shows no next run.
        Ok(RoutineSaved { next_run: None, upcoming: String::new(), ..routine.saved(now, &local) })
    }

    async fn detail_at(&self, bot: &str, id: &str, now: Timestamp) -> Result<ToolDetail, RoutineError> {
        let local = TimeZone::system();
        let routine = self.find(bot, id)?;
        let db = self.db()?;
        let row = routine.id.clone();
        let runs = db.call(move |conn| read_runs(conn, &row, None, RECENT)).await?;
        Ok(ToolDetail {
            saved: routine.saved(now, &local),
            state: routine.state(),
            prompt: routine.prompt,
            schedule: routine.schedule,
            recent: runs
                .into_iter()
                .map(|run| {
                    let at = run.started.as_deref().unwrap_or(&run.scheduled).parse().map_or_else(|_| run.scheduled.clone(), |at| moment_text(at, &local));
                    let said = run.note.as_deref().or(run.report.as_deref()).map(|text| start_of(text, RECENT_CHARS));
                    RecentRun { at, outcome: run.outcome, text: said }
                })
                .collect(),
        })
    }

    /// Runs a routine now, as it would run at its time (routine-design §5.7). It does the real thing.
    async fn run_now_at(self: &Arc<Self>, bot: &str, id: &str, now: Timestamp) -> Result<(), RoutineError> {
        let fire = {
            let _changing = self.changing.lock().await;
            let local = TimeZone::system();
            let routine = self.find(bot, id)?;
            if routine.running.is_some() {
                return Err(RoutineError::Busy);
            }
            let db = self.db()?;
            self.begin(db, routine, now, now, &local, true).await?
        };
        self.launch(fire);
        Ok(())
    }

    fn stop_run(&self, bot: &str, id: &str) -> Result<(), RoutineError> {
        if let Some(stop) = self.find(bot, id)?.running {
            stop.cancel();
        }
        Ok(())
    }
}

impl Routine {
    /// Whether it would still run once resumed.
    fn upcoming_after_resume(&self, now: Timestamp, local: &TimeZone) -> Option<Timestamp> {
        self.parsed(now, local)?.next_after(now, local)
    }
}

#[async_trait]
impl RoutineHost for Routines {
    async fn create(&self, bot: &str, new: NewRoutine) -> Result<RoutineSaved, String> {
        self.create_at(bot, new, Timestamp::now()).await.map_err(RoutineError::for_model)
    }

    async fn get(&self, bot: &str, id: &str) -> Result<ToolDetail, String> {
        self.detail_at(bot, id, Timestamp::now()).await.map_err(RoutineError::for_model)
    }

    async fn update(&self, bot: &str, id: &str, change: RoutineChange) -> Result<RoutineSaved, String> {
        self.update_at(bot, id, change, Timestamp::now()).await.map_err(RoutineError::for_model)
    }

    async fn set_enabled(&self, bot: &str, id: &str, enabled: bool) -> Result<RoutineSaved, String> {
        self.set_enabled_at(bot, id, enabled, Timestamp::now()).await.map_err(RoutineError::for_model)
    }

    async fn delete(&self, bot: &str, id: &str) -> Result<RoutineSaved, String> {
        self.delete_at(bot, id, Timestamp::now()).await.map_err(RoutineError::for_model)
    }
}

// ---- The service's side. ----

impl Server {
    fn routines(&self) -> &Arc<Routines> {
        &self.0.routines
    }

    /// Starts keeping routines: `her serve`, not `her acp`. Nothing waits for it.
    pub fn start_routines(&self) {
        self.routines().start(self.shutdown_token());
    }

    /// Whether the service must stay up for what is scheduled.
    pub fn has_active_routines(&self) -> bool {
        self.routines().any_active()
    }

    /// Starts a run in the bot's conversation, loaded for it if need be. A conversation unloading
    /// just now cannot take it, and the next attempt loads it again.
    async fn start_routine_run(&self, bot: &str, run: RoutineRun) -> Result<(HubHandle, oneshot::Receiver<RunOutcome>), String> {
        for _ in 0..2 {
            let hub = self.load_bot_session(bot).await.map_err(|err| err.message)?;
            let (reply, answer) = oneshot::channel();
            if hub.send(HubCmd::StartRun { run: run.clone(), reply }).await.is_ok()
                && let Ok(Some(started)) = answer.await
            {
                return started.map(|outcome| (hub, outcome));
            }
            hub.shared.wait_closed().await;
        }
        Err("the bot's conversation kept closing".to_string())
    }

    /// Carries a run out to its end. With it, whether the bot was told if it failed.
    async fn carry_out_routine_run(&self, fire: &Fire) -> (RunOutcome, bool) {
        let unstarted = |outcome, report| RunOutcome {
            outcome,
            note: None,
            report,
            log: None,
            usage: UsageTotals::default(),
        };
        let (hub, mut outcome) = match self.start_routine_run(&fire.bot, fire.run.clone()).await {
            Ok(started) => started,
            Err(_) if self.is_closing() => return (unstarted(RoutineOutcome::Interrupted, None), false),
            // With no conversation, the bot cannot be told (routine-design §5.5).
            Err(why) => return (unstarted(RoutineOutcome::Failed, Some(why)), false),
        };
        let ended = tokio::select! {
            ended = &mut outcome => ended,
            _ = fire.stop.cancelled() => {
                let _ = hub.send(HubCmd::StopRun { run_id: fire.run.id.clone() }).await;
                (&mut outcome).await
            }
        };
        let _ = hub.send(HubCmd::RunEnded).await;
        match ended {
            Ok(ended) => (ended, fire.run.report_failure),
            Err(_) => (unstarted(RoutineOutcome::Interrupted, None), false),
        }
    }

    pub async fn bots_routine(&self, bot: &str, id: &str) -> Result<BotRoutineResult, RpcError> {
        self.bot_exists(bot)?;
        let routine = self.routines().find(bot, id).map_err(RoutineError::for_client)?;
        let now = Timestamp::now();
        let local = TimeZone::system();
        Ok(BotRoutineResult {
            routine: RoutineDetail {
                summary: routine.summary(now, &local),
                upcoming: routine.upcoming(now, &local).into_iter().map(stamp).collect(),
                prompt: routine.prompt,
                schedule: routine.schedule,
                created_at: routine.created_at,
            },
        })
    }

    pub async fn bots_routine_runs(&self, bot: &str, id: &str, limit: Option<u32>, before: Option<String>) -> Result<BotRoutineRunsResult, RpcError> {
        self.bot_exists(bot)?;
        let routines = self.routines();
        let routine = routines.find(bot, id).map_err(RoutineError::for_client)?;
        let db = routines.db().map_err(RoutineError::for_client)?;
        let limit = limit.unwrap_or(RUNS_PAGE);
        let runs = db
            .call(move |conn| read_runs(conn, &routine.id, before.as_deref(), limit))
            .await
            .map_err(|err| errors::routines_unavailable(err.to_string()))?;
        Ok(BotRoutineRunsResult { runs })
    }

    pub async fn bots_routine_run(&self, bot: &str, id: &str, run: String) -> Result<BotRoutineRunResult, RpcError> {
        self.bot_exists(bot)?;
        let routines = self.routines();
        let routine = routines.find(bot, id).map_err(RoutineError::for_client)?;
        let db = routines.db().map_err(RoutineError::for_client)?;
        let found = {
            let run = run.clone();
            db.call(move |conn| {
                let sql = format!("SELECT {RUN_COLUMNS} FROM routine_runs WHERE id = ?1 AND routine_id = ?2");
                conn.query_row(&sql, params![run, routine.id], |row| Ok((run_from(row)?, row.get::<_, Option<String>>("log")?))).optional()
            })
            .await
            .map_err(|err| errors::routines_unavailable(err.to_string()))?
        };
        let Some((info, log)) = found else {
            return Err(errors::routine_not_found(&run));
        };
        let home = self.0.home.clone();
        let transcript = match log {
            Some(log) => tokio::task::spawn_blocking(move || {
                let store = JsonlStore::new(home.sessions_dir());
                her_core::routine::run_transcript(&store, std::path::Path::new(&log))
            })
            .await
            .map_err(|err| RpcError::internal(err.to_string()))?,
            None => Vec::new(),
        };
        Ok(BotRoutineRunResult { run: info, transcript })
    }

    pub async fn bots_routine_set_enabled(&self, bot: &str, id: &str, enabled: bool) -> Result<(), RpcError> {
        self.bot_exists(bot)?;
        self.routines().set_enabled_at(bot, id, enabled, Timestamp::now()).await.map(|_| ()).map_err(RoutineError::for_client)
    }

    pub async fn bots_routine_run_now(&self, bot: &str, id: &str) -> Result<(), RpcError> {
        self.bot_exists(bot)?;
        self.routines().run_now_at(bot, id, Timestamp::now()).await.map_err(RoutineError::for_client)
    }

    pub async fn bots_routine_stop(&self, bot: &str, id: &str) -> Result<(), RpcError> {
        self.bot_exists(bot)?;
        self.routines().stop_run(bot, id).map_err(RoutineError::for_client)
    }

    pub async fn bots_routine_delete(&self, bot: &str, id: &str) -> Result<(), RpcError> {
        self.bot_exists(bot)?;
        self.routines().delete_at(bot, id, Timestamp::now()).await.map(|_| ()).map_err(RoutineError::for_client)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const LOCAL: &str = "2026-11-05T00:00:00Z";

    fn at(text: &str) -> Timestamp {
        text.parse().unwrap()
    }

    /// A service with a database, one bot and no way to run anything: what comes due is
    /// committed to, and the test says how it ends.
    async fn routines(dir: &tempfile::TempDir) -> Arc<Routines> {
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        let bots = Arc::new(Registry::new(home.clone()));
        bots.add_for_test("b1");
        let routines = Routines::new(home, bots);
        if routines.db_path().exists() {
            routines.load().await;
        } else {
            routines.state.lock().store = Store::Empty;
        }
        routines
    }

    fn new(name: &str, schedule: &str) -> NewRoutine {
        NewRoutine {
            name: name.into(),
            prompt: "Do it.".into(),
            schedule: schedule.into(),
            when: "说好的时间".into(),
        }
    }

    fn ended(outcome: RoutineOutcome) -> RunOutcome {
        RunOutcome {
            outcome,
            note: None,
            report: None,
            log: None,
            usage: UsageTotals::default(),
        }
    }

    /// Row counts by outcome.
    async fn count(routines: &Routines, outcome: &str) -> i64 {
        let outcome = outcome.to_string();
        routines
            .db()
            .unwrap()
            .call(move |conn| conn.query_row("SELECT COUNT(*) FROM routine_runs WHERE outcome = ?1", [outcome], |row| row.get(0)))
            .await
            .unwrap()
    }

    async fn check(routines: &Routines, now: &str) -> Vec<Fire> {
        routines.check(at(now)).await.0
    }

    #[tokio::test]
    async fn no_file_is_made_until_the_first_routine() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        assert!(!routines.db_path().exists());
        assert_eq!(routines.listing_of("b1").as_deref(), Some("your routines: none"));
        assert!(!routines.any_active());
        let saved = routines.create_at("b1", new("喝水", "0 8 * * *"), at(LOCAL)).await.unwrap();
        assert!(routines.db_path().exists());
        assert!(saved.id.starts_with("r_") && saved.id.len() == 8, "{}", saved.id);
        assert!(routines.any_active());
    }

    #[tokio::test]
    async fn what_cannot_run_is_refused_in_words_the_model_can_use() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        let err = routines.create_at("b1", new("过去", "2026-01-01 08:00"), at(LOCAL)).await.unwrap_err();
        assert!(err.for_model().starts_with("That time has already passed: it is now"));
        let err = routines.create_at("b1", new("二月", "0 8 30 2 *"), at(LOCAL)).await.unwrap_err();
        assert_eq!(err.for_model(), "This schedule never runs.");
        // Nothing was made for what was refused.
        assert!(routines.state.lock().routines.is_empty());
    }

    #[tokio::test]
    async fn a_run_starts_when_it_comes_due_and_only_then() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        routines.create_at("b1", new("每分钟", "* * * * *"), at("2026-11-05T00:00:30Z")).await.unwrap();
        assert!(check(&routines, "2026-11-05T00:00:45Z").await.is_empty());
        let fires = check(&routines, "2026-11-05T00:01:00Z").await;
        assert_eq!(fires.len(), 1);
        assert!(!fires[0].run.late);
        assert_eq!(fires[0].run.prompt, "Do it.");
        // Committed before it runs: the record is open, and the same slot does not start twice.
        let open: i64 = routines
            .db()
            .unwrap()
            .call(|conn| conn.query_row("SELECT COUNT(*) FROM routine_runs WHERE outcome IS NULL", [], |row| row.get(0)))
            .await
            .unwrap();
        assert_eq!(open, 1);
        routines.finish(&fires[0], ended(RoutineOutcome::Quiet), true, at("2026-11-05T00:01:10Z")).await;
        assert!(check(&routines, "2026-11-05T00:01:20Z").await.is_empty());
    }

    /// Nothing runs on what was not written down, and the time is not lost: the next look tries
    /// again (routine-design §5.2).
    #[tokio::test]
    async fn a_run_that_cannot_be_written_down_does_not_start() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        let saved = routines.create_at("b1", new("每小时", "0 * * * *"), at("2026-11-05T00:30:00Z")).await.unwrap();
        let db = routines.db().unwrap();
        db.call(|conn| conn.execute_batch("ALTER TABLE routine_runs RENAME TO hidden")).await.unwrap();
        assert!(check(&routines, "2026-11-05T01:00:00Z").await.is_empty());
        let routine = routines.find("b1", &saved.id).unwrap();
        assert!(routine.running.is_none());
        assert_eq!(routine.last_slot, at("2026-11-05T00:30:00Z"), "the time stays to deal with");
        // The whole of it failed, the time in the file too.
        let slot: String = db.call(|conn| conn.query_row("SELECT last_slot FROM routines", [], |row| row.get(0))).await.unwrap();
        assert_eq!(slot, "2026-11-05T00:30:00Z");
        db.call(|conn| conn.execute_batch("ALTER TABLE hidden RENAME TO routine_runs")).await.unwrap();
        assert_eq!(check(&routines, "2026-11-05T01:00:10Z").await.len(), 1);
    }

    /// Asleep overnight: one run, for the latest time, told that it is late (routine-design §5.3).
    #[tokio::test]
    async fn only_the_latest_of_what_was_missed_runs() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        routines.create_at("b1", new("每小时", "0 * * * *"), at("2026-11-05T00:30:00Z")).await.unwrap();
        let fires = check(&routines, "2026-11-05T08:10:00Z").await;
        assert_eq!(fires.len(), 1);
        assert!(fires[0].run.late);
        let state = routines.state.lock();
        assert_eq!(state.routines[0].last_slot, at("2026-11-05T08:00:00Z"));
    }

    #[tokio::test]
    async fn a_missed_one_off_still_runs_then_finishes() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        let saved = routines.create_at("b1", new("提醒", "in 20m"), at("2026-11-05T00:00:00Z")).await.unwrap();
        assert_eq!(saved.upcoming.matches(':').count(), 1, "one run: {}", saved.upcoming);
        let fires = check(&routines, "2026-11-05T09:00:00Z").await;
        assert_eq!(fires.len(), 1);
        assert!(fires[0].run.late);
        routines.finish(&fires[0], ended(RoutineOutcome::Reported), true, at("2026-11-05T09:01:00Z")).await;
        let routine = routines.find("b1", &saved.id).unwrap();
        assert_eq!(routine.state(), RoutineState::Finished);
        assert!(routine.upcoming(at("2026-11-05T09:02:00Z"), &TimeZone::UTC).is_empty());
        // It left the model's list, and the service need not stay up for it.
        assert_eq!(routines.listing_of("b1").as_deref(), Some("your routines: none"));
        assert!(!routines.any_active());
        assert!(check(&routines, "2026-11-05T10:00:00Z").await.is_empty());
        // A new time brings it back; resuming as it is does not.
        let err = routines.set_enabled_at("b1", &saved.id, true, at("2026-11-05T10:00:00Z")).await.unwrap_err();
        assert!(err.clone().for_model().contains("already run"), "{err:?}");
        let change = RoutineChange { schedule: Some("in 1h".into()), when: Some("一小时后".into()), ..Default::default() };
        routines.update_at("b1", &saved.id, change, at("2026-11-05T10:00:00Z")).await.unwrap();
        assert_eq!(routines.find("b1", &saved.id).unwrap().state(), RoutineState::Active);
        assert_eq!(check(&routines, "2026-11-05T11:00:00Z").await.len(), 1);
    }

    /// The last run still goes on: this one is skipped and written down (routine-design §5.4).
    #[tokio::test]
    async fn a_run_due_while_the_last_goes_on_is_skipped() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        let saved = routines.create_at("b1", new("每小时", "0 * * * *"), at("2026-11-05T00:30:00Z")).await.unwrap();
        let fires = check(&routines, "2026-11-05T01:00:00Z").await;
        assert_eq!(fires.len(), 1);
        assert!(check(&routines, "2026-11-05T02:00:00Z").await.is_empty());
        assert_eq!(count(&routines, "skipped").await, 1);
        let runs = routines.detail_at("b1", &saved.id, at("2026-11-05T02:00:01Z")).await.unwrap().recent;
        assert_eq!(runs.len(), 2);
        assert_eq!(runs[0].outcome, Some(RoutineOutcome::Skipped));
        assert_eq!(runs[1].outcome, None, "the first is still running");
        routines.finish(&fires[0], ended(RoutineOutcome::Quiet), true, at("2026-11-05T02:10:00Z")).await;
        // The row shows the run that ran, not the one skipped.
        let routine = routines.find("b1", &saved.id).unwrap();
        assert_eq!(routine.last_run.unwrap().1.outcome, Some(RoutineOutcome::Quiet));
        assert_eq!(check(&routines, "2026-11-05T03:00:00Z").await.len(), 1);
    }

    #[tokio::test]
    async fn paused_it_does_not_run_and_what_passed_is_not_made_up() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        let saved = routines.create_at("b1", new("每小时", "0 * * * *"), at("2026-11-05T00:30:00Z")).await.unwrap();
        routines.set_enabled_at("b1", &saved.id, false, at("2026-11-05T00:40:00Z")).await.unwrap();
        assert!(check(&routines, "2026-11-05T05:00:00Z").await.is_empty());
        assert!(routines.listing_of("b1").unwrap().ends_with("(paused)"));
        assert!(!routines.any_active());
        routines.set_enabled_at("b1", &saved.id, true, at("2026-11-05T05:30:00Z")).await.unwrap();
        assert!(check(&routines, "2026-11-05T05:40:00Z").await.is_empty());
        assert_eq!(check(&routines, "2026-11-05T06:00:00Z").await.len(), 1);
    }

    #[tokio::test]
    async fn a_new_time_does_not_make_up_what_passed() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        let saved = routines.create_at("b1", new("每小时", "0 * * * *"), at("2026-11-05T00:30:00Z")).await.unwrap();
        let change = RoutineChange { schedule: Some("30 * * * *".into()), when: Some("每小时半".into()), ..Default::default() };
        routines.update_at("b1", &saved.id, change, at("2026-11-05T05:40:00Z")).await.unwrap();
        assert!(check(&routines, "2026-11-05T05:50:00Z").await.is_empty());
        assert_eq!(check(&routines, "2026-11-05T06:30:00Z").await.len(), 1);
    }

    /// An edit of the words leaves when it next runs alone.
    #[tokio::test]
    async fn changing_the_words_keeps_what_the_scheduler_knows() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        let saved = routines.create_at("b1", new("每小时", "0 * * * *"), at("2026-11-05T00:30:00Z")).await.unwrap();
        let fires = check(&routines, "2026-11-05T01:00:00Z").await;
        let change = RoutineChange { prompt: Some("Do it kindly.".into()), ..Default::default() };
        routines.update_at("b1", &saved.id, change, at("2026-11-05T01:00:30Z")).await.unwrap();
        let routine = routines.find("b1", &saved.id).unwrap();
        assert_eq!(routine.prompt, "Do it kindly.");
        assert!(routine.running.is_some(), "still running");
        assert_eq!(routine.last_slot, at("2026-11-05T01:00:00Z"));
        routines.finish(&fires[0], ended(RoutineOutcome::Failed), true, at("2026-11-05T01:01:00Z")).await;
        assert_eq!(routines.find("b1", &saved.id).unwrap().failures, 1);
    }

    /// The first failure is told; those after it are not, until one works (routine-design D-423).
    #[tokio::test]
    async fn only_the_first_failure_of_a_streak_is_told() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        routines.create_at("b1", new("每小时", "0 * * * *"), at("2026-11-05T00:30:00Z")).await.unwrap();
        let mut told = Vec::new();
        for (hour, outcome) in [(1, RoutineOutcome::Failed), (2, RoutineOutcome::Failed), (3, RoutineOutcome::Quiet), (4, RoutineOutcome::Failed)] {
            let now = format!("2026-11-05T{hour:02}:00:00Z");
            let fires = check(&routines, &now).await;
            told.push(fires[0].run.report_failure);
            routines.finish(&fires[0], ended(outcome), fires[0].run.report_failure, at(&now)).await;
        }
        // The run that ended the streak would not have told either; the next failure is a new one.
        assert_eq!(told, [true, false, false, true]);
    }

    /// A failure the bot never heard of does not use up the telling: the conversation could not
    /// be loaded, so the next failure is still the first it hears.
    #[tokio::test]
    async fn a_failure_nobody_was_told_of_does_not_count() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        routines.create_at("b1", new("每小时", "0 * * * *"), at("2026-11-05T00:30:00Z")).await.unwrap();
        let fires = check(&routines, "2026-11-05T01:00:00Z").await;
        routines.finish(&fires[0], ended(RoutineOutcome::Failed), false, at("2026-11-05T01:00:01Z")).await;
        let fires = check(&routines, "2026-11-05T02:00:00Z").await;
        assert!(fires[0].run.report_failure);
    }

    /// Restarted: the loop goes on from where it was, what was running is written off, and a
    /// one-off that was cut off is not run again (routine-design §5.2, §5.6).
    #[tokio::test]
    async fn a_restart_goes_on_from_the_file() {
        let dir = tempfile::tempdir().unwrap();
        let first = routines(&dir).await;
        let hourly = first.create_at("b1", new("每小时", "0 * * * *"), at("2026-11-05T00:30:00Z")).await.unwrap();
        let once = first.create_at("b1", new("提醒", "2026-11-05T01:00:00Z"), at("2026-11-05T00:30:00Z")).await.unwrap();
        assert_eq!(check(&first, "2026-11-05T01:00:00Z").await.len(), 2);
        // The process is gone with both runs open.
        {
            let mut state = first.state.lock();
            state.store = Store::Off;
            state._lock = None;
        }
        drop(first);
        tokio::time::sleep(Duration::from_millis(50)).await;

        let second = routines(&dir).await;
        assert_eq!(count(&second, "interrupted").await, 2);
        let routine = second.find("b1", &hourly.id).unwrap();
        assert_eq!(routine.state(), RoutineState::Active);
        assert_eq!(routine.last_run.unwrap().1.outcome, Some(RoutineOutcome::Interrupted));
        assert_eq!(second.find("b1", &once.id).unwrap().state(), RoutineState::Finished);
        // What was missed meanwhile runs once, right away.
        let fires = check(&second, "2026-11-05T09:10:00Z").await;
        assert_eq!(fires.len(), 1);
        assert_eq!(fires[0].run.routine_id, hourly.id);
    }

    /// When the file cannot be used, routines say so and nothing else changes.
    #[tokio::test]
    async fn a_file_from_a_newer_version_is_left_alone() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        let path = home.root().join("her.db");
        {
            let conn = Connection::open(&path).unwrap();
            conn.pragma_update(None, "user_version", 999).unwrap();
        }
        let routines = routines(&dir).await;
        let err = routines.create_at("b1", new("喝水", "0 8 * * *"), at(LOCAL)).await.unwrap_err();
        assert!(matches!(&err, RoutineError::Unavailable(why) if why.contains("newer version")), "{err:?}");
        assert_eq!(routines.listing_of("b1"), None);
        assert!(routines.bots.routines_error().is_some_and(|why| why.contains("newer version")));
        let version: u32 = Connection::open(&path).unwrap().pragma_query_value(None, "user_version", |row| row.get(0)).unwrap();
        assert_eq!(version, 999);
    }

    #[tokio::test]
    async fn one_service_keeps_the_routines_of_a_folder() {
        let dir = tempfile::tempdir().unwrap();
        let first = routines(&dir).await;
        first.create_at("b1", new("喝水", "0 8 * * *"), at(LOCAL)).await.unwrap();
        let second = routines(&dir).await;
        let err = second.create_at("b1", new("喝水", "0 8 * * *"), at(LOCAL)).await.unwrap_err();
        assert!(matches!(&err, RoutineError::Unavailable(why) if why.contains("another her service")), "{err:?}");
    }

    #[tokio::test]
    async fn deleting_keeps_the_records_and_ends_the_schedule() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        let saved = routines.create_at("b1", new("每小时", "0 * * * *"), at("2026-11-05T00:30:00Z")).await.unwrap();
        let fires = check(&routines, "2026-11-05T01:00:00Z").await;
        let gone = routines.delete_at("b1", &saved.id, at("2026-11-05T01:00:30Z")).await.unwrap();
        assert_eq!(gone.name, "每小时");
        assert!(fires[0].stop.is_cancelled(), "what it was doing stops");
        assert!(routines.find("b1", &saved.id).is_err());
        assert_eq!(routines.listing_of("b1").as_deref(), Some("your routines: none"));
        assert!(check(&routines, "2026-11-05T05:00:00Z").await.is_empty());
        // Its run still gets its ending; the record stays.
        routines.finish(&fires[0], ended(RoutineOutcome::Interrupted), false, at("2026-11-05T01:00:31Z")).await;
        assert_eq!(count(&routines, "interrupted").await, 1);
    }

    #[tokio::test]
    async fn another_bots_routine_is_not_found() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        let saved = routines.create_at("b1", new("喝水", "0 8 * * *"), at(LOCAL)).await.unwrap();
        let err = routines.update_at("b2", &saved.id, RoutineChange::default(), at(LOCAL)).await.unwrap_err();
        assert_eq!(err, RoutineError::NotFound(saved.id.clone()));
        assert!(err.for_model().contains("latest <environment_update>"));
    }

    #[tokio::test]
    async fn a_bots_routines_go_with_it() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        routines.create_at("b1", new("喝水", "0 8 * * *"), at(LOCAL)).await.unwrap();
        let fires = routines.create_at("b1", new("每小时", "0 * * * *"), at(LOCAL)).await.unwrap();
        let _ = fires;
        routines.bot_deleted("b1").await;
        assert!(routines.state.lock().routines.is_empty());
        assert!(!routines.any_active());
        let left: i64 = routines
            .db()
            .unwrap()
            .call(|conn| conn.query_row("SELECT COUNT(*) FROM routines WHERE deleted_at IS NULL", [], |row| row.get(0)))
            .await
            .unwrap();
        assert_eq!(left, 0);
    }

    /// Only a report waits for delivery, only the bot's next message settles it, and ordinary
    /// chat touches nothing.
    #[tokio::test]
    async fn a_report_is_delivered_by_the_next_message_to_the_user() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        let saved = routines.create_at("b1", new("每小时", "0 * * * *"), at("2026-11-05T00:30:00Z")).await.unwrap();
        let sent = |text: &str| {
            Event {
                seq: 0,
                ts: 0,
                msg: EventMsg::ToolStarted {
                    call_id: "c".into(),
                    name: SEND_TO_USER.into(),
                    args: serde_json::json!({ "text": text }),
                    category: Default::default(),
                    title: String::new(),
                    locations: Vec::new(),
                },
            }
        };
        routines.on_event("b1", &sent("随便聊聊"));
        let fires = check(&routines, "2026-11-05T01:00:00Z").await;
        routines.finish(&fires[0], ended(RoutineOutcome::Reported), true, at("2026-11-05T01:00:10Z")).await;
        assert!(routines.find("b1", &saved.id).unwrap().last_run.unwrap().1.delivered.is_none());
        // An empty message is an error, and reaches nobody; so is the phrase.
        routines.on_event("b1", &sent("  "));
        routines.on_event("b1", &sent("NOTHING_TO_REPORT"));
        assert!(routines.find("b1", &saved.id).unwrap().last_run.unwrap().1.delivered.is_none());
        routines.on_event("b1", &sent("跌到 290 了。"));
        assert!(routines.find("b1", &saved.id).unwrap().last_run.unwrap().1.delivered.is_some());
        let delivered: i64 = routines
            .db()
            .unwrap()
            .call(|conn| conn.query_row("SELECT COUNT(*) FROM routine_runs WHERE delivered IS NOT NULL", [], |row| row.get(0)))
            .await
            .unwrap();
        assert_eq!(delivered, 1);
        assert!(routines.state.lock().undelivered.is_empty());
    }

    #[tokio::test]
    async fn only_the_newest_logs_stay() {
        let dir = tempfile::tempdir().unwrap();
        let routines = routines(&dir).await;
        routines.create_at("b1", new("每分钟", "* * * * *"), at("2026-11-05T00:00:30Z")).await.unwrap();
        let logs = dir.path().join("logs");
        std::fs::create_dir_all(&logs).unwrap();
        let mut now = at("2026-11-05T00:01:00Z");
        for i in 0..(LOGS_KEPT + 2) {
            let log = logs.join(format!("{i}.jsonl"));
            std::fs::write(&log, b"{}").unwrap();
            std::fs::create_dir_all(log.with_extension("")).unwrap();
            let fires = routines.check(now).await.0;
            let done = RunOutcome { log: Some(log), ..ended(RoutineOutcome::Quiet) };
            routines.finish(&fires[0], done, true, now).await;
            now = now.checked_add(SignedDuration::from_mins(1)).unwrap();
        }
        // The two oldest went, with their folders.
        assert!(!logs.join("0.jsonl").exists() && !logs.join("0").exists());
        assert!(!logs.join("1.jsonl").exists());
        assert!(logs.join("2.jsonl").exists() && logs.join("2").exists());
        let (with_log, all): (i64, i64) = routines
            .db()
            .unwrap()
            .call(|conn| {
                conn.query_row("SELECT COUNT(log), COUNT(*) FROM routine_runs", [], |row| Ok((row.get(0)?, row.get(1)?)))
            })
            .await
            .unwrap();
        assert_eq!((with_log, all), (LOGS_KEPT, LOGS_KEPT + 2), "the records stay");
    }

    #[test]
    fn ids_are_short_and_typeable() {
        let id = new_routine_id();
        assert_eq!(id.len(), 8);
        assert!(id[2..].chars().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit()), "{id}");
        assert_ne!(new_routine_id(), new_routine_id());
    }
}
