//! `data/export`: an archive of this her's own data for its owner to keep, a .zip of the home's
//! files in the places the home keeps them. What goes in is named here, place by place
//! (her_core::HerHome), so that nothing else the home holds, now or later, goes with it: not the
//! credential store (`credentials.toml`; on macOS the keychain, which is not in the home at all),
//! the tokens of model connections (`connections/`) and connectors (`connectors/`, `mcp/`), the
//! managed browser's profiles and cookies (`browser/`), caches, logs, what is staged for a moment,
//! or an earlier archive. Links are not followed, so none of those comes in through one either.

use std::fs::File;
use std::fs::Metadata;
use std::io;
use std::path::Path;
use std::path::PathBuf;
use std::time::Duration;
use std::time::SystemTime;

use her_core::HerHome;
use her_protocol::api::DataExportResult;
use her_rpc::RpcError;
use zip::ZipWriter;
use zip::write::SimpleFileOptions;

use crate::server::Server;

/// Where archives are written, in her's home.
const EXPORTS: &str = "exports";
/// An archive left this long is removed when the next is written: whoever asked for it did not.
const KEPT_FOR: Duration = Duration::from_secs(24 * 60 * 60);
/// The routines and their runs (crate::routines), copied as the database has them.
const DATABASE: &str = "her.db";
/// What the session list keeps beside a conversation's log (her-core store/summary_cache.rs).
const SUMMARY_CACHE: &str = "summary-cache.json";

/// The home's own data: the user layer's settings and instructions, the assistants with their
/// folders, the conversations, the folders of conversations without a project, and the skills the
/// user added.
fn places(home: &HerHome) -> [PathBuf; 7] {
    [home.config_file(), home.models_file(), home.agents_md(), home.bots_dir(), home.sessions_dir(), home.chats_dir(), home.skills_dir()]
}

/// What stays out of those places: the skills built into her, written again on first use, and the
/// session list's caches.
fn left_out(home: &HerHome, path: &Path) -> bool {
    path == home.system_skills_dir() || (path.starts_with(home.sessions_dir()) && path.file_name().is_some_and(|name| name == SUMMARY_CACHE))
}

impl Server {
    pub(crate) async fn data_export(&self) -> Result<DataExportResult, RpcError> {
        let home = self.0.home.clone();
        tokio::task::spawn_blocking(move || write(&home))
            .await
            .map_err(|e| RpcError::internal(e.to_string()))?
            .map_err(|e| RpcError::internal(format!("cannot write the archive: {e}")))
    }
}

/// Writes a new archive in `exports/`, and removes the ones left there a day.
fn write(home: &HerHome) -> io::Result<DataExportResult> {
    let dir = home.root().join(EXPORTS);
    std::fs::create_dir_all(&dir)?;
    remove_old(&dir);
    let (file, path) = create(&dir, &format!("her-export-{}", chrono::Local::now().format("%Y%m%d-%H%M%S")))?;
    match fill(file, home, &dir) {
        Ok(bytes) => Ok(DataExportResult { path, bytes }),
        Err(err) => {
            let _ = std::fs::remove_file(&path);
            Err(err)
        }
    }
}

fn remove_old(dir: &Path) {
    for entry in std::fs::read_dir(dir).into_iter().flatten().flatten() {
        let old = entry.metadata().and_then(|m| m.modified()).is_ok_and(|t| t.elapsed().is_ok_and(|age| age > KEPT_FOR));
        if old {
            let _ = std::fs::remove_file(entry.path());
        }
    }
}

/// A new file `<stem>.zip` in `dir`, only its owner's, with a number added while the name is taken.
fn create(dir: &Path, stem: &str) -> io::Result<(File, PathBuf)> {
    for n in 1.. {
        let path = dir.join(if n == 1 { format!("{stem}.zip") } else { format!("{stem}-{n}.zip") });
        let mut options = std::fs::OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        std::os::unix::fs::OpenOptionsExt::mode(&mut options, 0o600);
        match options.open(&path) {
            Err(err) if err.kind() == io::ErrorKind::AlreadyExists => continue,
            opened => return opened.map(|file| (file, path)),
        }
    }
    unreachable!("a name is found before the numbers run out")
}

/// Writes the archive into `file`; its size.
fn fill(file: File, home: &HerHome, dir: &Path) -> io::Result<u64> {
    let mut zip = ZipWriter::new(file);
    for place in places(home) {
        add(&mut zip, home, &place)?;
    }
    let database = home.root().join(DATABASE);
    if std::fs::symlink_metadata(&database).is_ok_and(|m| m.is_file()) {
        let copy = dir.join(format!(".{}.db", uuid::Uuid::new_v4()));
        let added = copy_database(&database, &copy).and_then(|()| add_file(&mut zip, DATABASE, &copy));
        let _ = std::fs::remove_file(&copy);
        added?;
    }
    Ok(zip.finish()?.metadata()?.len())
}

/// The routines' database as one moment has it, made by SQLite: the file alone may be halfway
/// through a write, its newest pages still in the log beside it.
fn copy_database(from: &Path, to: &Path) -> io::Result<()> {
    let copied = rusqlite::Connection::open_with_flags(from, rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY).and_then(|conn| {
        conn.busy_timeout(Duration::from_secs(5))?;
        conn.execute("VACUUM INTO ?1", [to.to_string_lossy()])
    });
    copied.map(drop).map_err(|e| io::Error::other(format!("cannot copy the routines' database: {e}")))
}

/// Adds `path`, a file or a folder with all it holds, under its place in the home. Links, and
/// anything but files and folders, are passed over; so is what went away meanwhile.
fn add(zip: &mut ZipWriter<File>, home: &HerHome, path: &Path) -> io::Result<()> {
    if left_out(home, path) {
        return Ok(());
    }
    let meta = match std::fs::symlink_metadata(path) {
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(()),
        meta => meta?,
    };
    if meta.is_dir() {
        let mut entries = std::fs::read_dir(path)?.collect::<io::Result<Vec<_>>>()?;
        entries.sort_by_key(|entry| entry.file_name());
        for entry in entries {
            add(zip, home, &entry.path())?;
        }
    } else if meta.is_file() {
        let name = path.strip_prefix(home.root()).unwrap_or(path).components().map(|c| c.as_os_str().to_string_lossy()).collect::<Vec<_>>().join("/");
        add_file(zip, &name, path)?;
    }
    Ok(())
}

fn add_file(zip: &mut ZipWriter<File>, name: &str, path: &Path) -> io::Result<()> {
    let mut file = match File::open(path) {
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(()),
        file => file?,
    };
    zip.start_file(name, options(&file.metadata()?))?;
    io::copy(&mut file, zip)?;
    Ok(())
}

/// A file's entry keeps its time and its permissions.
fn options(meta: &Metadata) -> SimpleFileOptions {
    let mut options = SimpleFileOptions::default()
        .compression_method(zip::CompressionMethod::Deflated)
        .large_file(meta.len() >= u64::from(u32::MAX));
    if let Ok(time) = meta.modified() {
        options = options.last_modified_time(zip_time(time));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        options = options.unix_permissions(meta.permissions().mode());
    }
    options
}

/// A time as a zip entry keeps it: local, to two seconds, from 1980 on.
fn zip_time(time: SystemTime) -> zip::DateTime {
    use chrono::Datelike;
    use chrono::Timelike;
    let t = chrono::DateTime::<chrono::Local>::from(time);
    let year = u16::try_from(t.year()).unwrap_or(0);
    zip::DateTime::from_date_and_time(year, t.month() as u8, t.day() as u8, t.hour() as u8, t.minute() as u8, t.second() as u8).unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::BTreeMap;
    use std::io::Read;

    /// Every entry of the archive at `path`, by name, with its bytes.
    fn entries(path: &Path) -> BTreeMap<String, Vec<u8>> {
        let mut archive = zip::ZipArchive::new(File::open(path).unwrap()).unwrap();
        (0..archive.len())
            .map(|i| {
                let mut entry = archive.by_index(i).unwrap();
                let mut bytes = Vec::new();
                entry.read_to_end(&mut bytes).unwrap();
                (entry.name().unwrap().to_string(), bytes)
            })
            .collect()
    }

    fn put(home: &HerHome, path: &str, text: &str) {
        let path = home.root().join(path);
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(path, text).unwrap();
    }

    /// The secrets the home below keeps, each where her keeps such a thing.
    const SECRETS: &[&str] = &["model-key-in-the-store", "chatgpt-refresh-token", "oauth-client-secret", "connector-token", "cookie-value"];

    /// A home as a Her Server's looks after a while: assistants, conversations, routines,
    /// settings, skills; and the store, sign-ins, browser, caches, logs, staged files and trash,
    /// which stay out. The routines' database is open, as the service keeps it, its last write
    /// still in the log beside it.
    fn lived_in_home(dir: &Path) -> (HerHome, rusqlite::Connection) {
        let home = HerHome::new(dir.join("home"));
        home.ensure().unwrap();
        put(&home, "config.toml", "model = \"mock/mock-1\"\n[mcp_servers.tracker]\nurl = \"https://tracker.test/mcp\"\nheaders = { Authorization = \"Bearer ${credential:mcp.tracker}\" }\n");
        put(&home, "models.toml", "[providers.work]\nbase_url = \"https://models.test/v1\"\n");
        put(&home, "AGENTS.md", "Answer briefly.\n");
        put(&home, "bots/b1/bot.json", "{\"id\":\"b1\",\"name\":\"Her\"}");
        put(&home, "bots/b1/avatar.png", "png");
        put(&home, "bots/b1/files/report.md", "# The report Her wrote\n");
        put(&home, "sessions/2026/11/08/20261108T090000-s1.jsonl", "{\"text\":\"what was said\"}\n");
        put(&home, "sessions/2026/11/08/20261108T090000-s1/tool-output.txt", "a long result");
        put(&home, &format!("sessions/2026/11/08/20261108T090000-s1/{SUMMARY_CACHE}"), "{}");
        put(&home, "chats/c1/notes.txt", "made without a project");
        put(&home, "skills/weekly/SKILL.md", "---\nname: weekly\n---\n");
        put(&home, "skills/.system/builtin/SKILL.md", "---\nname: builtin\n---\n");
        let db = rusqlite::Connection::open(home.root().join(DATABASE)).unwrap();
        db.pragma_update(None, "journal_mode", "WAL").unwrap();
        db.execute_batch("CREATE TABLE routines (name TEXT); INSERT INTO routines VALUES ('Stretch');").unwrap();
        // Left out.
        put(&home, "credentials.toml", "[keys]\nmock = \"model-key-in-the-store\"\n");
        put(&home, "credentials.stamp", "1");
        put(&home, "connections/config.json", "{}");
        put(&home, "connections/chatgpt/auth.json", "{\"refresh_token\":\"chatgpt-refresh-token\"}");
        put(&home, "mcp/oauth-clients.json", "{\"client_secret\":\"oauth-client-secret\"}");
        put(&home, "mcp/cache/tracker.json", "{}");
        put(&home, "mcp/icons/tracker.png", "png");
        put(&home, "connectors/feishu/token.json", "{\"token\":\"connector-token\"}");
        put(&home, "browser/profiles/default/Cookies", "cookie-value");
        put(&home, "logs/her.log", "a log line");
        put(&home, "models/catalog.json", "{}");
        put(&home, "run/locks/tracker.lock", "");
        put(&home, "uploads/console/0a-skill.zip", "PK");
        put(&home, "trash/sessions/20261101T000000-old.jsonl", "{}");
        put(&home, "exports/her-export-20261107-120000.zip", "PK");
        put(&home, "projects/site-1a2b3c4d/config.toml", "model = \"x\"\n");
        put(&home, "trusted.toml", "");
        put(&home, "docs/doc.json", "{}");
        put(&home, "her.db.lock", "");
        // A link in an assistant's folder to the store, and one to a folder outside: not followed.
        #[cfg(unix)]
        {
            std::os::unix::fs::symlink(home.root().join("credentials.toml"), home.bot_files_dir("b1").join("keys.toml")).unwrap();
            std::os::unix::fs::symlink(home.root().join("connections"), home.bot_files_dir("b1").join("connections")).unwrap();
        }
        (home, db)
    }

    #[test]
    fn the_archive_holds_the_homes_own_data_and_no_secret() {
        let dir = tempfile::tempdir().unwrap();
        let (home, _db) = lived_in_home(dir.path());
        assert!(home.root().join("her.db-wal").exists());
        let made = write(&home).unwrap();
        assert_eq!(made.path.parent().unwrap(), home.root().join(EXPORTS));
        assert!(made.path.file_name().unwrap().to_string_lossy().starts_with("her-export-"), "{}", made.path.display());
        assert_eq!(made.bytes, std::fs::metadata(&made.path).unwrap().len());

        let got = entries(&made.path);
        let names: Vec<&str> = got.keys().map(String::as_str).collect();
        assert_eq!(
            names,
            [
                "AGENTS.md",
                "bots/b1/avatar.png",
                "bots/b1/bot.json",
                "bots/b1/files/report.md",
                "chats/c1/notes.txt",
                "config.toml",
                "her.db",
                "models.toml",
                "sessions/2026/11/08/20261108T090000-s1.jsonl",
                "sessions/2026/11/08/20261108T090000-s1/tool-output.txt",
                "skills/weekly/SKILL.md",
            ]
        );
        assert_eq!(got["sessions/2026/11/08/20261108T090000-s1.jsonl"], b"{\"text\":\"what was said\"}\n");
        assert!(String::from_utf8_lossy(&got["config.toml"]).contains("${credential:mcp.tracker}"));
        for (name, bytes) in &got {
            let text = String::from_utf8_lossy(bytes);
            for secret in SECRETS {
                assert!(!text.contains(secret), "{name} holds {secret}");
            }
        }

        // The database is a copy SQLite made, with what was committed, the log's pages included.
        let copy = dir.path().join("copy.db");
        std::fs::write(&copy, &got["her.db"]).unwrap();
        let name: String = rusqlite::Connection::open(&copy).unwrap().query_row("SELECT name FROM routines", [], |row| row.get(0)).unwrap();
        assert_eq!(name, "Stretch");
        let left: Vec<String> = std::fs::read_dir(home.root().join(EXPORTS)).unwrap().map(|e| e.unwrap().file_name().to_string_lossy().into_owned()).collect();
        assert!(left.iter().all(|name| !name.ends_with(".db")), "the database's copy stays: {left:?}");

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(std::fs::metadata(&made.path).unwrap().permissions().mode() & 0o777, 0o600);
        }
    }

    #[test]
    fn an_archive_left_a_day_goes_when_the_next_is_written() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        let exports = home.root().join(EXPORTS);
        std::fs::create_dir_all(&exports).unwrap();
        let (old, fresh) = (exports.join("her-export-old.zip"), exports.join("her-export-fresh.zip"));
        for path in [&old, &fresh] {
            std::fs::write(path, "PK").unwrap();
        }
        let day_ago = SystemTime::now() - KEPT_FOR - Duration::from_secs(60);
        File::options().write(true).open(&old).unwrap().set_modified(day_ago).unwrap();

        let first = write(&home).unwrap();
        let second = write(&home).unwrap();
        assert!(!old.exists());
        assert!(fresh.exists() && first.path.exists() && second.path.exists());
        assert_ne!(first.path, second.path, "two in the same second get names of their own");
        // An empty home makes an archive with nothing in it.
        assert!(entries(&second.path).is_empty());
    }
}
