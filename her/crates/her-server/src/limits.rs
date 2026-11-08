//! Process limits for a service that holds many sessions.

use std::io;

/// The soft limit is raised no further than this. It is enough for thousands of sessions, and
/// the programs that tools start inherit it.
const OPEN_FILES_CEILING: libc::rlim_t = 65_536;

/// Raises the soft limit on open files towards the hard limit; never lowers it.
///
/// Each session holds its log file and, while it works, a connection to the model and the
/// pipes of the tools it runs; each client holds a connection. Programs started by launchd on
/// macOS, and shells by default, get a soft limit of 256, which runs out at around a hundred
/// busy sessions. Returns the soft limit before and after.
pub fn raise_open_file_limit() -> io::Result<(libc::rlim_t, libc::rlim_t)> {
    let mut limit = libc::rlimit {
        rlim_cur: 0,
        rlim_max: 0,
    };
    // SAFETY: fills a plain struct.
    if unsafe { libc::getrlimit(libc::RLIMIT_NOFILE, &mut limit) } != 0 {
        return Err(io::Error::last_os_error());
    }
    let before = limit.rlim_cur;
    let target = limit.rlim_max.min(OPEN_FILES_CEILING).min(per_process_cap());
    if target <= before {
        return Ok((before, before));
    }
    limit.rlim_cur = target;
    // SAFETY: only the soft limit changes, and it stays within the hard limit.
    if unsafe { libc::setrlimit(libc::RLIMIT_NOFILE, &limit) } != 0 {
        return Err(io::Error::last_os_error());
    }
    Ok((before, target))
}

/// macOS refuses a soft limit above `kern.maxfilesperproc`, even when the hard limit is
/// unlimited.
#[cfg(target_os = "macos")]
fn per_process_cap() -> libc::rlim_t {
    let mut value: libc::c_int = 0;
    let mut size = std::mem::size_of::<libc::c_int>();
    // SAFETY: reads one int into `value`, whose size is passed along.
    let rc = unsafe {
        libc::sysctlbyname(
            c"kern.maxfilesperproc".as_ptr(),
            (&mut value as *mut libc::c_int).cast(),
            &mut size,
            std::ptr::null_mut(),
            0,
        )
    };
    match libc::rlim_t::try_from(value) {
        Ok(cap) if rc == 0 && cap > 0 => cap,
        // OPEN_MAX, the documented ceiling.
        _ => 10_240,
    }
}

#[cfg(not(target_os = "macos"))]
fn per_process_cap() -> libc::rlim_t {
    libc::rlim_t::MAX
}
