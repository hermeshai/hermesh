//! Disposable projections for local session lists. The JSONL remains authoritative; opening
//! or resuming a session never uses this cache. A new process accepts only an exact file
//! identity/size/mtime/ctime match, and otherwise falls back to a full scan.

use super::Scanned;
use serde::{Deserialize, Serialize};
use std::fs::{File, Metadata, OpenOptions};
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::time::SystemTime;

const MAX_BYTES: u64 = 64 * 1024;
const VERSION: u32 = 1;

#[derive(PartialEq, Serialize, Deserialize)]
struct Stamp {
    len: u64,
    modified: SystemTime,
    device: u64,
    inode: u64,
    changed: (i64, i64),
}

impl Stamp {
    fn of(meta: &Metadata) -> Option<Self> {
        #[cfg(unix)]
        {
            use std::os::unix::fs::MetadataExt;
            Some(Self {
                len: meta.len(),
                modified: meta.modified().ok()?,
                device: meta.dev(),
                inode: meta.ino(),
                changed: (meta.ctime(), meta.ctime_nsec()),
            })
        }
        #[cfg(not(unix))]
        {
            let _ = meta;
            None
        }
    }
}

#[derive(Deserialize)]
struct Projection {
    version: u32,
    stamp: Stamp,
    scanned: Scanned,
}

#[derive(Serialize)]
struct ProjectionRef<'a> {
    version: u32,
    stamp: Stamp,
    scanned: &'a Scanned,
}

struct Bounded(Vec<u8>);

impl Write for Bounded {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        if self.0.len().saturating_add(bytes.len()) > MAX_BYTES as usize {
            return Err(std::io::Error::other("summary cache is too large"));
        }
        self.0.extend_from_slice(bytes);
        Ok(bytes.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

fn path(log: &Path) -> PathBuf {
    // Travels with the session's data directory when it is moved to trash.
    log.with_extension("").join("summary-cache.json")
}

pub(super) fn load(log: &Path, meta: &Metadata) -> Option<Scanned> {
    let stamp = Stamp::of(meta)?;
    let mut bytes = Vec::new();
    File::open(path(log)).ok()?.take(MAX_BYTES + 1).read_to_end(&mut bytes).ok()?;
    if bytes.len() as u64 > MAX_BYTES {
        return None;
    }
    let cached: Projection = serde_json::from_slice(&bytes).ok()?;
    (cached.version == VERSION
        && cached.stamp == stamp
        && cached.scanned.summary.location == log
        && cached.scanned.offset == stamp.len
        && cached.scanned.len == stamp.len
        && cached.scanned.inode == stamp.inode
        && cached.scanned.modified == Some(stamp.modified))
    .then_some(cached.scanned)
}

pub(super) fn save(log: &Path, scanned: &Scanned, before: &Metadata) {
    let Some(stamp) = Stamp::of(before) else { return };
    if scanned.offset != stamp.len || std::fs::metadata(log).ok().and_then(|m| Stamp::of(&m)).as_ref() != Some(&stamp) {
        return;
    }
    let projection = ProjectionRef {
        version: VERSION,
        stamp,
        scanned,
    };
    let mut bytes = Bounded(Vec::new());
    if serde_json::to_writer(&mut bytes, &projection).is_err() {
        return;
    }
    let target = path(log);
    let Some(dir) = target.parent() else { return };
    let mut builder = std::fs::DirBuilder::new();
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    if builder.create(dir).is_err() && !std::fs::symlink_metadata(dir).is_ok_and(|m| m.is_dir()) {
        return;
    }
    let temporary = dir.join(format!(".summary-{}-{:016x}.tmp", std::process::id(), rand::random::<u64>()));
    let result = (|| -> std::io::Result<()> {
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        super::private(&mut options).open(&temporary)?.write_all(&bytes.0)?;
        std::fs::rename(&temporary, target)
    })();
    if result.is_err() {
        let _ = std::fs::remove_file(temporary);
    }
}
