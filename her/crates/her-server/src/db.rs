//! The service's database, `~/.her/her.db`, for records that change and get queried
//! (routine-design §3.1, §3.6). One connection on one thread; the rest of the service sends it
//! work over a channel, so nothing async ever waits on the disk. It knows no business: the SQL
//! lives with the feature, and here is only the order the schema grew in.

use rusqlite::Connection;
use std::path::Path;
use std::path::PathBuf;
use std::sync::mpsc;
use tokio::sync::oneshot;

/// Every step of the schema, in order; `PRAGMA user_version` says how many a file has had.
/// A step only adds: tables and nullable columns, never a new meaning for an old one.
const MIGRATIONS: &[&str] = &[crate::routines::SCHEMA_V1];

/// Why the database cannot be used.
#[derive(Clone, Debug, PartialEq, Eq, thiserror::Error)]
pub(crate) enum DbError {
    /// A later version wrote it: leave it alone. Running an older build meets this.
    #[error("the file was written by a newer version of her (schema {found}, this one knows {known})")]
    Newer { found: u32, known: u32 },
    #[error("{0}")]
    Failed(String),
    #[error("the database thread has stopped")]
    Gone,
}

impl From<rusqlite::Error> for DbError {
    fn from(error: rusqlite::Error) -> Self {
        DbError::Failed(error.to_string())
    }
}

type Job = Box<dyn FnOnce(&mut Connection) + Send>;

/// The way to the connection. Cloning shares it.
#[derive(Clone)]
pub(crate) struct Db {
    jobs: mpsc::Sender<Job>,
}

impl Db {
    /// Opens the file, creating it when it is missing, and brings its schema up to date.
    /// Blocking I/O: call it off the runtime.
    pub(crate) fn open(path: &Path) -> Result<Db, DbError> {
        let conn = connect(path)?;
        migrate(conn, path, MIGRATIONS).map(start)
    }

    /// Runs `work` on the connection and waits for it.
    pub(crate) async fn call<R: Send + 'static>(
        &self,
        work: impl FnOnce(&mut Connection) -> rusqlite::Result<R> + Send + 'static,
    ) -> Result<R, DbError> {
        let (reply, answer) = oneshot::channel();
        self.jobs
            .send(Box::new(move |conn| {
                let _ = reply.send(work(conn));
            }))
            .map_err(|_| DbError::Gone)?;
        answer.await.map_err(|_| DbError::Gone)?.map_err(DbError::from)
    }

    /// Runs `work` on the connection without waiting; a failure is logged. For the callers that
    /// must not wait, such as a hub.
    pub(crate) fn send(&self, what: &'static str, work: impl FnOnce(&mut Connection) -> rusqlite::Result<()> + Send + 'static) {
        let sent = self.jobs.send(Box::new(move |conn| {
            if let Err(err) = work(conn) {
                tracing::warn!("cannot write {what}: {err}");
            }
        }));
        if sent.is_err() {
            tracing::warn!("cannot write {what}: the database thread has stopped");
        }
    }
}

fn connect(path: &Path) -> Result<Connection, DbError> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir).map_err(|e| DbError::Failed(format!("cannot create {}: {e}", dir.display())))?;
    }
    // Private from the start: SQLite gives its -wal and -shm files the main file's mode.
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        std::fs::OpenOptions::new()
            .create(true)
            .append(true)
            .mode(0o600)
            .open(path)
            .map_err(|e| DbError::Failed(format!("cannot open {}: {e}", path.display())))?;
    }
    let conn = Connection::open(path)?;
    conn.busy_timeout(std::time::Duration::from_secs(5))?;
    // Written rarely, so written firmly: a commit that a power cut takes back would run a task
    // twice. macOS's plain fsync does not reach the disk; fullfsync does (elsewhere a no-op).
    conn.pragma_update(None, "journal_mode", "WAL")?;
    conn.pragma_update(None, "synchronous", "FULL")?;
    conn.pragma_update(None, "fullfsync", "ON")?;
    conn.pragma_update(None, "checkpoint_fullfsync", "ON")?;
    conn.pragma_update(None, "foreign_keys", "ON")?;
    Ok(conn)
}

fn migrate(mut conn: Connection, path: &Path, steps: &[&str]) -> Result<Connection, DbError> {
    let known = steps.len() as u32;
    let found: u32 = conn.pragma_query_value(None, "user_version", |row| row.get(0))?;
    if found > known {
        return Err(DbError::Newer { found, known });
    }
    if found == known {
        return Ok(conn);
    }
    // A file with data gets a copy before its schema changes.
    if found > 0 {
        let backup = backup_path(path, found);
        let _ = std::fs::remove_file(&backup);
        conn.execute("VACUUM INTO ?1", [backup.to_string_lossy()])?;
    }
    for (index, step) in steps.iter().enumerate().skip(found as usize) {
        let tx = conn.transaction()?;
        tx.execute_batch(step)?;
        tx.pragma_update(None, "user_version", index as u32 + 1)?;
        tx.commit()?;
    }
    Ok(conn)
}

fn backup_path(path: &Path, version: u32) -> PathBuf {
    let mut name = path.as_os_str().to_owned();
    name.push(format!(".bak-v{version}"));
    PathBuf::from(name)
}

fn start(mut conn: Connection) -> Db {
    let (jobs, inbox) = mpsc::channel::<Job>();
    let thread = std::thread::Builder::new().name("her-db".into()).spawn(move || {
        while let Ok(job) = inbox.recv() {
            job(&mut conn);
        }
    });
    if let Err(err) = thread {
        tracing::warn!("cannot start the database thread: {err}");
    }
    Db { jobs }
}

#[cfg(test)]
mod tests {
    use super::*;

    const ONE: &str = "CREATE TABLE a(x INTEGER NOT NULL) STRICT;";
    const TWO: &str = "ALTER TABLE a ADD COLUMN y TEXT;";

    fn version(conn: &Connection) -> u32 {
        conn.pragma_query_value(None, "user_version", |row| row.get(0)).unwrap()
    }

    #[test]
    fn a_new_file_gets_every_step() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("t.db");
        let conn = migrate(connect(&path).unwrap(), &path, &[ONE, TWO]).unwrap();
        assert_eq!(version(&conn), 2);
        conn.execute("INSERT INTO a(x, y) VALUES (1, 'y')", []).unwrap();
        // Nothing to copy from a new file.
        assert!(!backup_path(&path, 0).exists());
    }

    #[test]
    fn an_older_file_is_copied_then_upgraded() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("t.db");
        let conn = migrate(connect(&path).unwrap(), &path, &[ONE]).unwrap();
        conn.execute("INSERT INTO a(x) VALUES (7)", []).unwrap();
        drop(conn);
        let conn = migrate(connect(&path).unwrap(), &path, &[ONE, TWO]).unwrap();
        assert_eq!(version(&conn), 2);
        let x: i64 = conn.query_row("SELECT x FROM a", [], |r| r.get(0)).unwrap();
        assert_eq!(x, 7);
        let copy = Connection::open(backup_path(&path, 1)).unwrap();
        assert_eq!(version(&copy), 1);
        let x: i64 = copy.query_row("SELECT x FROM a", [], |r| r.get(0)).unwrap();
        assert_eq!(x, 7);
    }

    #[test]
    fn a_failed_step_leaves_the_version_where_it_was() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("t.db");
        let conn = migrate(connect(&path).unwrap(), &path, &[ONE]).unwrap();
        drop(conn);
        let bad = "ALTER TABLE a ADD COLUMN z TEXT; THIS IS NOT SQL;";
        assert!(migrate(connect(&path).unwrap(), &path, &[ONE, bad]).is_err());
        let conn = connect(&path).unwrap();
        assert_eq!(version(&conn), 1);
        assert!(conn.prepare("SELECT z FROM a").is_err());
    }

    #[test]
    fn a_newer_file_is_left_alone() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("t.db");
        let conn = migrate(connect(&path).unwrap(), &path, &[ONE, TWO]).unwrap();
        drop(conn);
        let err = migrate(connect(&path).unwrap(), &path, &[ONE]).err().unwrap();
        assert_eq!(err, DbError::Newer { found: 2, known: 1 });
    }

    #[test]
    fn a_damaged_file_is_an_error_not_a_panic() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("t.db");
        std::fs::write(&path, b"this is not a database, though it is long enough to look at").unwrap();
        assert!(Db::open(&path).is_err());
        // It stays as it was.
        assert!(std::fs::read(&path).unwrap().starts_with(b"this is not"));
    }

    #[cfg(unix)]
    #[test]
    fn the_file_is_private() {
        use std::os::unix::fs::PermissionsExt;
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("t.db");
        let _conn = migrate(connect(&path).unwrap(), &path, &[ONE]).unwrap();
        let mode = std::fs::metadata(&path).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode, 0o600);
    }

    #[tokio::test]
    async fn work_runs_on_the_thread_and_answers() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("t.db");
        let db = start(migrate(connect(&path).unwrap(), &path, &[ONE]).unwrap());
        db.send("a row", |conn| conn.execute("INSERT INTO a(x) VALUES (3)", []).map(|_| ()));
        let x = db.call(|conn| conn.query_row("SELECT x FROM a", [], |r| r.get::<_, i64>(0))).await.unwrap();
        assert_eq!(x, 3);
        // A failure comes back as an error.
        let err = db.call(|conn| conn.execute("INSERT INTO nowhere VALUES (1)", [])).await.unwrap_err();
        assert!(matches!(err, DbError::Failed(_)));
    }
}
