//! Tracks which files were read in this session, so that `write` never overwrites changes the
//! model has not seen.

use parking_lot::Mutex;
use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;
use std::time::Duration;
use std::time::SystemTime;

/// One version of a file. Any write, metadata change or file renamed over it gives a new stamp.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct FileStamp {
    pub size: u64,
    pub mtime: Option<SystemTime>,
    /// Status change time. Nobody can set it, so a change that keeps the mtime (`cp -p`,
    /// `rsync -t`, unpacking an archive) still shows.
    pub ctime: Option<SystemTime>,
    /// Device and inode: a file replaced by another one renamed over it shows.
    pub dev: u64,
    pub ino: u64,
}

impl FileStamp {
    pub fn of(meta: &std::fs::Metadata) -> Self {
        let mut stamp = Self {
            size: meta.len(),
            mtime: meta.modified().ok(),
            ..Self::default()
        };
        #[cfg(unix)]
        {
            use std::os::unix::fs::MetadataExt;
            stamp.ctime = u64::try_from(meta.ctime())
                .ok()
                .map(|secs| SystemTime::UNIX_EPOCH + Duration::new(secs, meta.ctime_nsec() as u32));
            stamp.dev = meta.dev();
            stamp.ino = meta.ino();
        }
        stamp
    }
}

#[derive(Debug, PartialEq, Eq)]
pub enum StaleReason {
    NotRead,
    Changed,
}

#[derive(Default, Debug)]
pub struct FileTracker {
    seen: Mutex<HashMap<PathBuf, FileStamp>>,
}

impl FileTracker {
    pub fn new() -> Self {
        Self::default()
    }

    /// Records the state of a file after it was read or written by a tool.
    pub fn record(&self, path: &Path, stamp: FileStamp) {
        self.seen.lock().insert(key(path), stamp);
    }

    /// Forgets every file, as a session that has just resumed knows none: the conversation went
    /// back and no longer holds what the model read.
    pub fn clear(&self) {
        self.seen.lock().clear();
    }

    /// Checks that `current` matches what this session last saw.
    pub fn verify(&self, path: &Path, current: FileStamp) -> Result<(), StaleReason> {
        match self.seen.lock().get(&key(path)) {
            None => Err(StaleReason::NotRead),
            Some(seen) if *seen != current => Err(StaleReason::Changed),
            Some(_) => Ok(()),
        }
    }
}

fn key(path: &Path) -> PathBuf {
    std::fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf())
}

/// One lock per file, shared by every session that works in the same files (a session and
/// its sub-agents), so that read-change-write sequences on one file never interleave.
#[derive(Default, Debug)]
pub struct PathLocks {
    locks: Mutex<HashMap<PathBuf, std::sync::Weak<tokio::sync::Mutex<()>>>>,
}

impl PathLocks {
    pub fn new() -> Self {
        Self::default()
    }

    /// Waits until no other holder changes `path`. The lock is released when the guard drops.
    pub async fn lock(&self, path: &Path) -> tokio::sync::OwnedMutexGuard<()> {
        let lock = {
            let mut locks = self.locks.lock();
            locks.retain(|_, lock| lock.strong_count() > 0);
            let key = key(path);
            match locks.get(&key).and_then(std::sync::Weak::upgrade) {
                Some(lock) => lock,
                None => {
                    let lock = std::sync::Arc::new(tokio::sync::Mutex::new(()));
                    locks.insert(key, std::sync::Arc::downgrade(&lock));
                    lock
                }
            }
        };
        lock.lock_owned().await
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detects_unread_and_changed_files() {
        let tracker = FileTracker::new();
        let path = Path::new("/tmp/her-tracker-test-file");
        let stamp = FileStamp {
            size: 3,
            mtime: Some(SystemTime::UNIX_EPOCH),
            ..FileStamp::default()
        };
        assert_eq!(tracker.verify(path, stamp), Err(StaleReason::NotRead));
        tracker.record(path, stamp);
        assert_eq!(tracker.verify(path, stamp), Ok(()));
        let changed = FileStamp {
            mtime: Some(SystemTime::UNIX_EPOCH + Duration::from_secs(1)),
            ..stamp
        };
        assert_eq!(tracker.verify(path, changed), Err(StaleReason::Changed));
        let replaced = FileStamp { ino: 7, ..stamp };
        assert_eq!(tracker.verify(path, replaced), Err(StaleReason::Changed));
    }

    #[test]
    fn a_kept_mtime_still_shows_as_a_change() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("f.txt");
        std::fs::write(&path, "one").unwrap();
        let before = FileStamp::of(&std::fs::metadata(&path).unwrap());
        // Same size, and the mtime is put back as `cp -p` would.
        std::thread::sleep(Duration::from_millis(20));
        std::fs::write(&path, "two").unwrap();
        let file = std::fs::File::options().write(true).open(&path).unwrap();
        file.set_modified(before.mtime.unwrap()).unwrap();
        let after = FileStamp::of(&std::fs::metadata(&path).unwrap());
        assert_eq!(after.size, before.size);
        assert_eq!(after.mtime, before.mtime);
        assert_ne!(after, before);
    }

    #[tokio::test]
    async fn path_locks_serialize_changes_to_one_file() {
        let locks = PathLocks::new();
        let a = Path::new("/tmp/her-locks-test-a");
        let held = locks.lock(a).await;
        let wait = Duration::from_millis(50);
        assert!(tokio::time::timeout(wait, locks.lock(a)).await.is_err(), "same file waits");
        let other = tokio::time::timeout(wait, locks.lock(Path::new("/tmp/her-locks-test-b"))).await;
        assert!(other.is_ok(), "other files do not wait");
        drop(held);
        assert!(tokio::time::timeout(wait, locks.lock(a)).await.is_ok());
    }
}
