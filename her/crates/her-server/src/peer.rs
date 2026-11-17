//! Who is on the other end of a connection: the extension host must be this very program
//! (browser-design D-198). Same-user processes can already connect; this check keeps any of
//! them from passing itself off as the user's Chrome.

use std::path::PathBuf;

/// Ok when `pid` runs the same executable as this process.
pub(crate) fn is_this_program(pid: Option<u32>) -> Result<(), String> {
    let pid = pid.ok_or("the system did not say which process connected")?;
    let theirs = executable_of(pid).ok_or_else(|| format!("cannot find the program of process {pid}"))?;
    let ours = std::env::current_exe().map_err(|e| format!("cannot find this program: {e}"))?;
    let same = |path: &PathBuf| dunce::canonicalize(path).unwrap_or_else(|_| path.clone());
    if same(&theirs) == same(&ours) {
        Ok(())
    } else {
        Err(format!("process {pid} runs {}, not {}", theirs.display(), ours.display()))
    }
}

#[cfg(target_os = "macos")]
fn executable_of(pid: u32) -> Option<PathBuf> {
    use std::os::unix::ffi::OsStringExt;
    let mut buf = vec![0u8; libc::PROC_PIDPATHINFO_MAXSIZE as usize];
    // SAFETY: the buffer is as large as the call may write.
    let n = unsafe { libc::proc_pidpath(pid as libc::c_int, buf.as_mut_ptr().cast(), buf.len() as u32) };
    if n <= 0 {
        return None;
    }
    buf.truncate(n as usize);
    Some(PathBuf::from(std::ffi::OsString::from_vec(buf)))
}

#[cfg(all(unix, not(target_os = "macos")))]
fn executable_of(pid: u32) -> Option<PathBuf> {
    std::fs::read_link(format!("/proc/{pid}/exe")).ok()
}

#[cfg(windows)]
fn executable_of(pid: u32) -> Option<PathBuf> {
    use std::os::windows::ffi::OsStringExt;
    use windows_sys::Win32::Foundation::CloseHandle;
    use windows_sys::Win32::System::Threading::OpenProcess;
    use windows_sys::Win32::System::Threading::PROCESS_QUERY_LIMITED_INFORMATION;
    use windows_sys::Win32::System::Threading::QueryFullProcessImageNameW;

    // SAFETY: opens the process to ask its image's name only.
    let process = unsafe { OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid) };
    if process.is_null() {
        return None;
    }
    let mut buf = vec![0u16; 32_768];
    let mut len = buf.len() as u32;
    // SAFETY: the buffer is as large as `len` says; the call writes how much it used.
    let ok = unsafe { QueryFullProcessImageNameW(process, 0, buf.as_mut_ptr(), &mut len) };
    // SAFETY: the handle opened above.
    unsafe {
        CloseHandle(process);
    }
    (ok != 0).then(|| PathBuf::from(std::ffi::OsString::from_wide(&buf[..len as usize])))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn this_process_is_this_program_and_others_are_not() {
        assert_eq!(is_this_program(Some(std::process::id())), Ok(()));
        assert!(is_this_program(None).is_err());
        let mut other = std::process::Command::new("sleep").arg("5").spawn().unwrap();
        let refused = is_this_program(Some(other.id()));
        let _ = other.kill();
        let _ = other.wait();
        assert!(refused.unwrap_err().contains("sleep"));
    }
}
