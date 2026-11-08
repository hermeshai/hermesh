//! A deck's update log and snapshot (presentation-kernel-authority §7.1).
//!
//! `updates.log` holds one record per accepted update: `len u32 | crc32 u32 | revision u64 |
//! update`, little endian, the CRC over revision and update. Each record is synced before the
//! update is acknowledged. `snapshot.yupd` holds `revision u64 | full state` and replaces the
//! records up to its revision; records at or below it, left by a compaction cut short, are
//! skipped on replay. A record cut short or corrupted ends the log there.

use std::fs::File;
use std::fs::OpenOptions;
use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};

pub const SOURCE: &str = "source.pptx";
/// A Word document's source, in place of [`SOURCE`]: what the worker opens it as.
pub const WORD_SOURCE: &str = "source.docx";
pub const LOG: &str = "updates.log";
pub const SNAPSHOT: &str = "snapshot.yupd";
pub const ENGINE: &str = "engine.txt";

/// Compact once the log passes either.
const COMPACT_BYTES: u64 = 8 * 1024 * 1024;
const COMPACT_RECORDS: u64 = 2_000;
const HEADER: usize = 16;

/// What a directory held when it was opened.
#[derive(Default)]
pub struct Recovered {
    pub snapshot: Option<(u64, Vec<u8>)>,
    /// Records past the snapshot, in order.
    pub records: Vec<(u64, Vec<u8>)>,
    /// Bytes dropped from a torn or corrupt tail.
    pub dropped_bytes: u64,
    /// The engine that wrote them, when it is recorded.
    pub engine: Option<String>,
}

impl Recovered {
    pub fn revision(&self) -> u64 {
        self.records.last().map(|(r, _)| *r).or(self.snapshot.as_ref().map(|(r, _)| *r)).unwrap_or(0)
    }

    pub fn is_empty(&self) -> bool {
        self.snapshot.is_none() && self.records.is_empty()
    }
}

pub struct Journal {
    dir: PathBuf,
    log: File,
    bytes: u64,
    records: u64,
}

impl Journal {
    /// Opens the directory's log for appending, after reading what it holds.
    pub fn open(dir: &Path) -> io::Result<(Self, Recovered)> {
        let mut recovered = Recovered {
            snapshot: read_snapshot(&dir.join(SNAPSHOT))?,
            engine: std::fs::read_to_string(dir.join(ENGINE)).ok().map(|s| s.trim().to_owned()),
            ..Recovered::default()
        };
        let floor = recovered.snapshot.as_ref().map_or(0, |(r, _)| *r);
        let path = dir.join(LOG);
        let bytes = std::fs::read(&path).or_else(|e| if e.kind() == io::ErrorKind::NotFound { Ok(Vec::new()) } else { Err(e) })?;
        let (records, good) = parse(&bytes);
        recovered.dropped_bytes = (bytes.len() - good) as u64;
        let count = records.len() as u64;
        recovered.records = records.into_iter().filter(|(rev, _)| *rev > floor).collect();
        let log = OpenOptions::new().create(true).append(true).read(true).open(&path)?;
        if recovered.dropped_bytes > 0 {
            log.set_len(good as u64)?;
            log.sync_all()?;
        }
        Ok((Self { dir: dir.to_owned(), log, bytes: good as u64, records: count }, recovered))
    }

    /// Records the engine about to write here.
    pub fn stamp_engine(&self, engine: &str) -> io::Result<()> {
        crate::atomic::write(&self.dir.join(ENGINE), engine.as_bytes())
    }

    /// Appends a record and syncs it to disk.
    pub fn append(&mut self, revision: u64, update: &[u8]) -> io::Result<()> {
        let mut record = Vec::with_capacity(HEADER + update.len());
        record.extend_from_slice(&(update.len() as u32).to_le_bytes());
        record.extend_from_slice(&[0; 4]);
        record.extend_from_slice(&revision.to_le_bytes());
        record.extend_from_slice(update);
        let crc = crc32(&record[8..]);
        record[4..8].copy_from_slice(&crc.to_le_bytes());
        self.log.write_all(&record)?;
        self.log.sync_data()?;
        self.bytes += record.len() as u64;
        self.records += 1;
        Ok(())
    }

    pub fn wants_compaction(&self) -> bool {
        self.bytes > COMPACT_BYTES || self.records > COMPACT_RECORDS
    }

    /// Writes `state` as the snapshot at `revision`, then empties the log.
    pub fn compact(&mut self, revision: u64, state: &[u8]) -> io::Result<()> {
        let mut snapshot = Vec::with_capacity(8 + state.len());
        snapshot.extend_from_slice(&revision.to_le_bytes());
        snapshot.extend_from_slice(state);
        crate::atomic::write(&self.dir.join(SNAPSHOT), &snapshot)?;
        File::open(&self.dir)?.sync_all().ok();
        self.log.set_len(0)?;
        self.log.sync_all()?;
        self.bytes = 0;
        self.records = 0;
        Ok(())
    }

    /// Moves the log and snapshot aside as `<name>.<tag>`: kept, no longer replayed.
    pub fn set_aside(dir: &Path, tag: &str) -> io::Result<()> {
        for name in [LOG, SNAPSHOT] {
            let from = dir.join(name);
            if from.exists() {
                std::fs::rename(&from, dir.join(format!("{name}.{tag}")))?;
            }
        }
        Ok(())
    }
}

fn read_snapshot(path: &Path) -> io::Result<Option<(u64, Vec<u8>)>> {
    let mut bytes = Vec::new();
    match File::open(path) {
        Ok(mut f) => f.read_to_end(&mut bytes)?,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(e),
    };
    if bytes.len() < 8 {
        return Err(io::Error::new(io::ErrorKind::InvalidData, "snapshot too short"));
    }
    let revision = u64::from_le_bytes(bytes[..8].try_into().expect("8 bytes"));
    Ok(Some((revision, bytes.split_off(8))))
}

/// The whole records at the start of `bytes`, and how many bytes they take.
fn parse(bytes: &[u8]) -> (Vec<(u64, Vec<u8>)>, usize) {
    let mut records = Vec::new();
    let mut at = 0;
    while bytes.len() - at >= HEADER {
        let len = u32::from_le_bytes(bytes[at..at + 4].try_into().expect("4 bytes")) as usize;
        let crc = u32::from_le_bytes(bytes[at + 4..at + 8].try_into().expect("4 bytes"));
        let end = at + HEADER + len;
        if end > bytes.len() || crc32(&bytes[at + 8..end]) != crc {
            break;
        }
        let revision = u64::from_le_bytes(bytes[at + 8..at + 16].try_into().expect("8 bytes"));
        records.push((revision, bytes[at + HEADER..end].to_vec()));
        at = end;
    }
    (records, at)
}

/// CRC-32 (IEEE), bitwise: records are small and few.
fn crc32(bytes: &[u8]) -> u32 {
    let mut crc = !0u32;
    for &b in bytes {
        crc ^= b as u32;
        for _ in 0..8 {
            crc = (crc >> 1) ^ (0xEDB8_8320 & (crc & 1).wrapping_neg());
        }
    }
    !crc
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn crc_matches_the_standard_check_value() {
        assert_eq!(crc32(b"123456789"), 0xCBF4_3926);
    }

    #[test]
    fn records_survive_reopening_and_a_torn_tail_is_cut() {
        let dir = tempfile::tempdir().unwrap();
        let (mut j, r) = Journal::open(dir.path()).unwrap();
        assert!(r.is_empty());
        j.append(1, b"one").unwrap();
        j.append(2, b"two").unwrap();
        drop(j);
        // Half a third record, as after power loss.
        let mut f = OpenOptions::new().append(true).open(dir.path().join(LOG)).unwrap();
        f.write_all(&[9, 0, 0, 0, 1, 2]).unwrap();
        drop(f);
        let (mut j, r) = Journal::open(dir.path()).unwrap();
        assert_eq!(r.records, vec![(1, b"one".to_vec()), (2, b"two".to_vec())]);
        assert_eq!(r.dropped_bytes, 6);
        j.append(3, b"three").unwrap();
        drop(j);
        let (_, r) = Journal::open(dir.path()).unwrap();
        assert_eq!(r.revision(), 3);
        assert_eq!(r.dropped_bytes, 0);
    }

    #[test]
    fn a_corrupt_record_ends_the_log() {
        let dir = tempfile::tempdir().unwrap();
        let (mut j, _) = Journal::open(dir.path()).unwrap();
        j.append(1, b"one").unwrap();
        j.append(2, b"two").unwrap();
        drop(j);
        let path = dir.path().join(LOG);
        let mut bytes = std::fs::read(&path).unwrap();
        let last = bytes.len() - 1;
        bytes[last] ^= 0xff;
        std::fs::write(&path, bytes).unwrap();
        let (_, r) = Journal::open(dir.path()).unwrap();
        assert_eq!(r.records, vec![(1, b"one".to_vec())]);
    }

    #[test]
    fn compaction_replaces_records_and_skips_leftovers() {
        let dir = tempfile::tempdir().unwrap();
        let (mut j, _) = Journal::open(dir.path()).unwrap();
        j.append(1, b"one").unwrap();
        j.append(2, b"two").unwrap();
        j.compact(2, b"state").unwrap();
        j.append(3, b"three").unwrap();
        drop(j);
        let (_, r) = Journal::open(dir.path()).unwrap();
        assert_eq!(r.snapshot, Some((2, b"state".to_vec())));
        assert_eq!(r.records, vec![(3, b"three".to_vec())]);

        // A compaction cut short after the snapshot, before the log was emptied.
        let dir = tempfile::tempdir().unwrap();
        let (mut j, _) = Journal::open(dir.path()).unwrap();
        j.append(1, b"one").unwrap();
        j.append(2, b"two").unwrap();
        std::fs::write(dir.path().join(SNAPSHOT), [&1u64.to_le_bytes()[..], b"s"].concat()).unwrap();
        drop(j);
        let (_, r) = Journal::open(dir.path()).unwrap();
        assert_eq!(r.records, vec![(2, b"two".to_vec())]);
        assert_eq!(r.revision(), 2);
    }
}
