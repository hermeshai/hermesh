//! Tracks which files were read in this session, so that `write` never overwrites changes the
//! model has not seen.

use parking_lot::Mutex;
use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;
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
        #[cfg_attr(not(unix), allow(unused_mut))]
        let mut stamp = Self {
            size: meta.len(),
            mtime: meta.modified().ok(),
            ..Self::default()
        };
        #[cfg(unix)]
        {
            use std::os::unix::fs::MetadataExt;
            use std::time::Duration;
            stamp.ctime = u64::try_from(meta.ctime())
                .ok()
                .map(|secs| SystemTime::UNIX_EPOCH + Duration::new(secs, meta.ctime_nsec() as u32));
            stamp.dev = meta.dev();
            stamp.ino = meta.ino();
        }
        stamp
    }

    /// The stamp of an open file. Windows' metadata has neither a change time nor an identity:
    /// there they are asked of the file's handle.
    pub fn of_file(file: &std::fs::File) -> std::io::Result<Self> {
        #[cfg_attr(not(windows), allow(unused_mut))]
        let mut stamp = Self::of(&file.metadata()?);
        #[cfg(windows)]
        {
            use std::os::windows::io::AsRawHandle;
            use std::time::Duration;
            use windows_sys::Win32::Foundation::HANDLE;
            use windows_sys::Win32::Storage::FileSystem::BY_HANDLE_FILE_INFORMATION;
            use windows_sys::Win32::Storage::FileSystem::FILE_BASIC_INFO;
            use windows_sys::Win32::Storage::FileSystem::FileBasicInfo;
            use windows_sys::Win32::Storage::FileSystem::GetFileInformationByHandle;
            use windows_sys::Win32::Storage::FileSystem::GetFileInformationByHandleEx;

            let handle = file.as_raw_handle() as HANDLE;
            // SAFETY: zeroes are a valid value of these plain C structs, which the calls fill
            // for a handle the caller keeps open.
            unsafe {
                let mut info: BY_HANDLE_FILE_INFORMATION = std::mem::zeroed();
                if GetFileInformationByHandle(handle, &mut info) == 0 {
                    return Err(std::io::Error::last_os_error());
                }
                stamp.dev = u64::from(info.dwVolumeSerialNumber);
                stamp.ino = u64::from(info.nFileIndexHigh) << 32 | u64::from(info.nFileIndexLow);
                let mut basic: FILE_BASIC_INFO = std::mem::zeroed();
                if GetFileInformationByHandleEx(handle, FileBasicInfo, (&raw mut basic).cast(), size_of::<FILE_BASIC_INFO>() as u32) == 0 {
                    return Err(std::io::Error::last_os_error());
                }
                // 100 ns since 1601, which nobody sets with a file's other times.
                let ticks = u64::try_from(basic.ChangeTime).unwrap_or_default();
                let since_1601 = Duration::new(ticks / 10_000_000, (ticks % 10_000_000) as u32 * 100);
                stamp.ctime = (SystemTime::UNIX_EPOCH + since_1601).checked_sub(Duration::from_secs(11_644_473_600));
            }
        }
        Ok(stamp)
    }

    /// The stamp of what is at `path`, as [`of_file`](Self::of_file) gives it for a file.
    pub fn of_path(path: &Path) -> std::io::Result<Self> {
        // A folder does not open as a file on Windows: its metadata is all there is.
        #[cfg(windows)]
        if let Ok(file) = std::fs::File::open(path) {
            return Self::of_file(&file);
        }
        Ok(Self::of(&std::fs::metadata(path)?))
    }
}

/// Takes the exclusive lock of a file that others read meanwhile (a session's log, a service's
/// record), without waiting.
#[cfg(not(windows))]
pub fn try_lock(file: &std::fs::File) -> Result<(), std::fs::TryLockError> {
    file.try_lock()
}

/// Windows keeps other handles from reading the bytes a lock covers: the lock is on one byte
/// far past the file's end. The handle must be one that may read or overwrite the file.
#[cfg(windows)]
pub fn try_lock(file: &std::fs::File) -> Result<(), std::fs::TryLockError> {
    use std::os::windows::io::AsRawHandle;
    use windows_sys::Win32::Foundation::ERROR_IO_PENDING;
    use windows_sys::Win32::Foundation::ERROR_LOCK_VIOLATION;
    use windows_sys::Win32::Foundation::HANDLE;
    use windows_sys::Win32::Storage::FileSystem::LOCKFILE_EXCLUSIVE_LOCK;
    use windows_sys::Win32::Storage::FileSystem::LOCKFILE_FAIL_IMMEDIATELY;
    use windows_sys::Win32::Storage::FileSystem::LockFileEx;
    use windows_sys::Win32::System::IO::OVERLAPPED;

    // SAFETY: zeroes are a valid value of this plain C struct.
    let mut at: OVERLAPPED = unsafe { std::mem::zeroed() };
    at.Anonymous.Anonymous.OffsetHigh = 0x7fff_ffff;
    // SAFETY: LockFileEx on a handle the caller keeps open; `at` names the byte and outlives
    // the call.
    if unsafe { LockFileEx(file.as_raw_handle() as HANDLE, LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &mut at) } != 0 {
        return Ok(());
    }
    let err = std::io::Error::last_os_error();
    match err.raw_os_error().map(|code| code as u32) {
        Some(ERROR_LOCK_VIOLATION | ERROR_IO_PENDING) => Err(std::fs::TryLockError::WouldBlock),
        _ => Err(std::fs::TryLockError::Error(err)),
    }
}

/// Renames a file or a folder. Windows refuses while any program has a file under it open,
/// and its indexer and virus scanner open what was just written, for a moment: there the
/// rename is tried again for two seconds.
pub fn rename(from: &Path, to: &Path) -> std::io::Result<()> {
    let mut tries = if cfg!(windows) { 40 } else { 0 };
    loop {
        match std::fs::rename(from, to) {
            Err(err) if tries > 0 && err.kind() == std::io::ErrorKind::PermissionDenied => {
                tries -= 1;
                std::thread::sleep(std::time::Duration::from_millis(50));
            }
            done => return done,
        }
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
    dunce::canonicalize(path).unwrap_or_else(|_| path.to_path_buf())
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
    use std::time::Duration;

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
        let before = FileStamp::of_path(&path).unwrap();
        // Same size, and the mtime is put back as `cp -p` would.
        std::thread::sleep(Duration::from_millis(20));
        std::fs::write(&path, "two").unwrap();
        let file = std::fs::File::options().write(true).open(&path).unwrap();
        file.set_modified(before.mtime.unwrap()).unwrap();
        let after = FileStamp::of_path(&path).unwrap();
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
