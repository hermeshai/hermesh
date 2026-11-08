//! Samples another process's memory, CPU time and thread count.

use parking_lot::Mutex;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::Ordering;
use std::time::Duration;
use std::time::Instant;

#[derive(Clone, Copy, Debug, Default)]
pub struct Sample {
    /// Seconds since the probe started.
    pub at: f64,
    /// Resident set size, bytes.
    pub rss: u64,
    /// Physical footprint (what Activity Monitor shows), bytes.
    pub footprint: u64,
    /// Highest physical footprint so far, bytes.
    pub peak_footprint: u64,
    /// User plus system CPU time, nanoseconds.
    pub cpu_ns: u64,
    /// Instructions retired (macOS; 0 elsewhere). Steadier than CPU time on a busy machine.
    pub instructions: u64,
    pub threads: u32,
    /// Open file descriptors.
    pub fds: u32,
}

#[cfg(target_os = "macos")]
#[allow(deprecated)] // mach_timebase_info: libc points to the mach2 crate
pub fn sample(pid: i32) -> Option<Sample> {
    use std::sync::OnceLock;
    static TIMEBASE: OnceLock<(u64, u64)> = OnceLock::new();
    let (numer, denom) = *TIMEBASE.get_or_init(|| {
        let mut info = libc::mach_timebase_info { numer: 0, denom: 0 };
        // SAFETY: fills a plain struct.
        unsafe {
            libc::mach_timebase_info(&mut info);
        }
        (u64::from(info.numer.max(1)), u64::from(info.denom.max(1)))
    });
    // SAFETY: both structs are plain data and sized as the calls expect.
    unsafe {
        let mut usage: libc::rusage_info_v4 = std::mem::zeroed();
        let rc = libc::proc_pid_rusage(
            pid,
            libc::RUSAGE_INFO_V4,
            &mut usage as *mut libc::rusage_info_v4 as *mut libc::rusage_info_t,
        );
        if rc != 0 {
            return None;
        }
        let mut task: libc::proc_taskinfo = std::mem::zeroed();
        let size = std::mem::size_of::<libc::proc_taskinfo>() as libc::c_int;
        let got = libc::proc_pidinfo(
            pid,
            libc::PROC_PIDTASKINFO,
            0,
            &mut task as *mut libc::proc_taskinfo as *mut libc::c_void,
            size,
        );
        let threads = if got == size {
            task.pti_threadnum as u32
        } else {
            0
        };
        // With no buffer, the size a list of the open descriptors needs: 8 bytes each.
        const PROC_PIDLISTFDS: libc::c_int = 1;
        let listed = libc::proc_pidinfo(pid, PROC_PIDLISTFDS, 0, std::ptr::null_mut(), 0);
        // The size-only query is an upper bound (the descriptor table capacity), not
        // the number of open files. Read the list to observe descriptors being closed.
        let fds = if listed > 0 {
            let mut entries = vec![0u64; (listed as usize).div_ceil(8)];
            let bytes =
                libc::proc_pidinfo(pid, PROC_PIDLISTFDS, 0, entries.as_mut_ptr().cast(), listed);
            bytes.max(0) as u32 / 8
        } else {
            0
        };
        Some(Sample {
            at: 0.0,
            rss: usage.ri_resident_size,
            footprint: usage.ri_phys_footprint,
            peak_footprint: usage.ri_lifetime_max_phys_footprint,
            cpu_ns: (usage.ri_user_time + usage.ri_system_time) * numer / denom,
            instructions: usage.ri_instructions,
            threads,
            fds,
        })
    }
}

#[cfg(not(target_os = "macos"))]
pub fn sample(pid: i32) -> Option<Sample> {
    let status = std::fs::read_to_string(format!("/proc/{pid}/status")).ok()?;
    let field = |name: &str| {
        status
            .lines()
            .find_map(|l| l.strip_prefix(name))
            .and_then(|v| v.split_whitespace().next()?.parse::<u64>().ok())
    };
    let stat = std::fs::read_to_string(format!("/proc/{pid}/stat")).ok()?;
    let fields: Vec<&str> = stat.rsplit(')').next()?.split_whitespace().collect();
    let ticks = fields.get(11)?.parse::<u64>().ok()? + fields.get(12)?.parse::<u64>().ok()?;
    Some(Sample {
        at: 0.0,
        rss: field("VmRSS:")? * 1024,
        footprint: field("VmRSS:")? * 1024,
        peak_footprint: field("VmHWM:")? * 1024,
        // Linux configurations are not all 100 Hz.
        cpu_ns: ticks.saturating_mul(1_000_000_000)
            / unsafe { libc::sysconf(libc::_SC_CLK_TCK) }.max(1) as u64,
        instructions: 0,
        threads: field("Threads:")? as u32,
        fds: std::fs::read_dir(format!("/proc/{pid}/fd")).map_or(0, |d| d.count() as u32),
    })
}

/// Samples a process every few milliseconds on a thread of its own.
#[derive(Clone)]
pub struct Probe {
    pid: i32,
    started: Instant,
    samples: Arc<Mutex<Vec<Sample>>>,
    stop: Arc<AtomicBool>,
}

impl Probe {
    pub fn start(pid: i32, every: Duration) -> Probe {
        let probe = Probe {
            pid,
            started: Instant::now(),
            samples: Arc::new(Mutex::new(Vec::new())),
            stop: Arc::new(AtomicBool::new(false)),
        };
        let worker = probe.clone();
        std::thread::spawn(move || {
            while !worker.stop.load(Ordering::Relaxed) {
                worker.record();
                std::thread::sleep(every);
            }
        });
        probe
    }

    fn record(&self) -> Option<Sample> {
        let mut sample = sample(self.pid)?;
        sample.at = self.started.elapsed().as_secs_f64();
        self.samples.lock().push(sample);
        Some(sample)
    }

    /// A sample taken now.
    pub fn now(&self) -> Sample {
        self.record().unwrap_or_default()
    }

    pub fn elapsed(&self) -> f64 {
        self.started.elapsed().as_secs_f64()
    }

    /// Highest values between two points in time.
    pub fn peak(&self, from: f64, to: f64) -> Sample {
        let samples = self.samples.lock();
        let mut peak = Sample::default();
        for s in samples.iter().filter(|s| s.at >= from && s.at <= to) {
            peak.rss = peak.rss.max(s.rss);
            peak.footprint = peak.footprint.max(s.footprint);
            peak.threads = peak.threads.max(s.threads);
            peak.fds = peak.fds.max(s.fds);
        }
        peak
    }

    pub fn stop(&self) {
        self.stop.store(true, Ordering::Relaxed);
    }
}

/// Live heap of a process as `vmmap --summary` reports it (macOS): bytes allocated, and the
/// dirty memory of the malloc zones that hold them.
#[derive(Clone, Copy, Debug, Default)]
pub struct Heap {
    pub allocated: u64,
    pub dirty: u64,
    pub count: u64,
}

pub fn heap(pid: i32) -> Option<Heap> {
    let output = std::process::Command::new("vmmap")
        .arg("--summary")
        .arg(pid.to_string())
        .output()
        .ok()?;
    let text = String::from_utf8_lossy(&output.stdout);
    // The malloc zone table's total: TOTAL virtual resident dirty swapped count allocated frag %frag regions
    let row = text
        .lines()
        .filter(|l| l.starts_with("TOTAL") && !l.contains("minus"))
        .map(|l| l.split_whitespace().collect::<Vec<_>>())
        .find(|cols| cols.len() == 10)?;
    Some(Heap {
        dirty: size(row[3])?,
        count: row[5].parse().ok()?,
        allocated: size(row[6])?,
    })
}

/// `1088K`, `29.2M`, `2.1G` or plain bytes.
fn size(text: &str) -> Option<u64> {
    let (number, scale) = match text.chars().last()? {
        'K' => (&text[..text.len() - 1], 1024.0),
        'M' => (&text[..text.len() - 1], 1048576.0),
        'G' => (&text[..text.len() - 1], 1073741824.0),
        _ => (text, 1.0),
    };
    Some((number.parse::<f64>().ok()? * scale) as u64)
}
