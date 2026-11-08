//! Making a session log durable.
//!
//! On Apple platforms `fsync` moves a file's data and attributes to the drive, and
//! `F_FULLFSYNC` (what `sync_data` does there) also asks the drive to write everything it has
//! buffered to permanent storage. That drive flush takes milliseconds and drives run them one
//! after another, so sessions ending together queued behind each other's flushes. Now each
//! file is fsynced on its own, then one flush covers every file fsynced before it began.

use std::fs::File;
use std::io;

/// Returns once `file`'s data is on permanent storage, as `sync_data` does.
pub(crate) fn sync(file: &File) -> io::Result<()> {
    #[cfg(target_vendor = "apple")]
    {
        use std::os::fd::AsRawFd;
        use std::os::unix::fs::MetadataExt;
        let device = file.metadata()?.dev();
        DRIVES.sync(device, || fsync(file.as_raw_fd()), || file.sync_data())
    }
    #[cfg(not(target_vendor = "apple"))]
    {
        // fdatasync already flushes the drive's cache here.
        file.sync_data()
    }
}

#[cfg(target_vendor = "apple")]
fn fsync(fd: std::os::fd::RawFd) -> io::Result<()> {
    loop {
        // SAFETY: fsync on a descriptor the caller keeps open.
        if unsafe { libc::fsync(fd) } == 0 {
            return Ok(());
        }
        let err = io::Error::last_os_error();
        if err.kind() != io::ErrorKind::Interrupted {
            return Err(err);
        }
    }
}

#[cfg(target_vendor = "apple")]
static DRIVES: Drives = Drives::new();

#[cfg_attr(not(target_vendor = "apple"), allow(dead_code))]
/// Drive flushes, per device. Requests are numbered in the order their fsync finished; a flush
/// covers every request numbered up to the count when it began, and no later one.
struct Drives {
    devices: parking_lot::Mutex<Vec<(u64, Device)>>,
    flushed: parking_lot::Condvar,
}

#[cfg_attr(not(target_vendor = "apple"), allow(dead_code))]
#[derive(Default)]
struct Device {
    requested: u64,
    flushed: u64,
    flushing: bool,
    /// Requests whose flush failed, as `(first, last, error)`; failures are rare.
    failures: Vec<(u64, u64, io::ErrorKind, String)>,
}

#[cfg_attr(not(target_vendor = "apple"), allow(dead_code))]
impl Drives {
    const fn new() -> Self {
        Self {
            devices: parking_lot::const_mutex(Vec::new()),
            flushed: parking_lot::Condvar::new(),
        }
    }

    /// `own` makes the file's data reach the drive; `flush` does that and flushes the drive.
    fn sync(
        &self,
        device: u64,
        own: impl FnOnce() -> io::Result<()>,
        flush: impl FnOnce() -> io::Result<()>,
    ) -> io::Result<()> {
        own()?;
        let mut flush = Some(flush);
        let mut devices = self.devices.lock();
        let index = match devices.iter().position(|(d, _)| *d == device) {
            Some(index) => index,
            None => {
                devices.push((device, Device::default()));
                devices.len() - 1
            }
        };
        devices[index].1.requested += 1;
        let mine = devices[index].1.requested;
        loop {
            let state = &mut devices[index].1;
            if state.flushed >= mine {
                return match state.failures.iter().find(|(first, last, ..)| (*first..=*last).contains(&mine)) {
                    Some((.., kind, message)) => Err(io::Error::new(*kind, message.clone())),
                    None => Ok(()),
                };
            }
            if !state.flushing
                && let Some(flush) = flush.take()
            {
                state.flushing = true;
                let (first, last) = (state.flushed + 1, state.requested);
                drop(devices);
                let result = flush();
                devices = self.devices.lock();
                let state = &mut devices[index].1;
                state.flushing = false;
                state.flushed = last;
                if let Err(err) = &result {
                    if state.failures.len() == 16 {
                        state.failures.remove(0);
                    }
                    state.failures.push((first, last, err.kind(), err.to_string()));
                }
                self.flushed.notify_all();
                return result;
            }
            self.flushed.wait(&mut devices);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::time::{Duration, Instant};

    #[test]
    fn a_file_is_synced() {
        let dir = tempfile::tempdir().unwrap();
        let file = File::create(dir.path().join("log")).unwrap();
        sync(&file).unwrap();
    }

    #[test]
    fn concurrent_requests_share_flushes_and_each_waits_for_one_that_began_after_it() {
        let drives = Arc::new(Drives::new());
        let flushes = Arc::new(AtomicUsize::new(0));
        let started = Arc::new(parking_lot::Mutex::new(Vec::<Instant>::new()));
        let threads: Vec<_> = (0..32)
            .map(|i| {
                let (drives, flushes, started) = (drives.clone(), flushes.clone(), started.clone());
                std::thread::spawn(move || {
                    std::thread::sleep(Duration::from_micros((i % 8) as u64 * 300));
                    let own_done = parking_lot::Mutex::new(None);
                    let own = || {
                        *own_done.lock() = Some(Instant::now());
                        Ok(())
                    };
                    let flush = || {
                        started.lock().push(Instant::now());
                        flushes.fetch_add(1, Ordering::SeqCst);
                        std::thread::sleep(Duration::from_millis(3));
                        Ok(())
                    };
                    drives.sync(7, own, flush).unwrap();
                    let own_done = own_done.lock().unwrap();
                    (own_done, Instant::now())
                })
            })
            .collect();
        let requests: Vec<_> = threads.into_iter().map(|t| t.join().unwrap()).collect();
        let flushes = flushes.load(Ordering::SeqCst);
        assert!(flushes < 32, "32 requests should share flushes, got {flushes}");
        // Each request returned only after a flush that began once its own fsync was done.
        let started = started.lock();
        for (own_done, returned) in requests {
            assert!(
                started.iter().any(|s| *s >= own_done && *s <= returned),
                "a request returned without a flush that began after its fsync"
            );
        }
    }

    #[test]
    fn a_failed_flush_fails_exactly_the_requests_it_covered() {
        let drives = Drives::new();
        let err = drives.sync(1, || Ok(()), || Err(io::Error::other("disk gone"))).unwrap_err();
        assert_eq!(err.to_string(), "disk gone");
        drives.sync(1, || Ok(()), || Ok(())).unwrap();
        // Another device is unaffected, and an fsync error is the request's own.
        drives.sync(2, || Ok(()), || Ok(())).unwrap();
        assert!(drives.sync(1, || Err(io::Error::other("bad fd")), || Ok(())).is_err());
    }
}
