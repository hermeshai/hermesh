//! A command's process tree: the process her starts and everything that process starts,
//! stopped together.
//!
//! On Unix the child leads a process group of its own, which signals reach. On Windows the
//! child joins a job object before it runs, so nothing it starts ever runs outside the job,
//! and ending the job ends them all.

use tokio::process::Child;
use tokio::process::Command;

/// The tree of one child. Dropping it stops nothing: what the command left running (an app
/// it opened, say) stays, as the child's own `kill_on_drop` decides for the child.
pub struct ProcessTree {
    #[cfg(unix)]
    pid: Option<u32>,
    #[cfg(windows)]
    job: job::Job,
}

impl ProcessTree {
    /// Spawns `command` as the first process of a new tree.
    pub fn spawn(command: &mut Command) -> std::io::Result<(Child, ProcessTree)> {
        #[cfg(unix)]
        {
            command.process_group(0);
            let child = command.spawn()?;
            let pid = child.id();
            Ok((child, ProcessTree { pid }))
        }
        #[cfg(windows)]
        {
            // A console no window shows: her may have none to share (started by hermeshd).
            command.creation_flags(job::CREATE_SUSPENDED | job::CREATE_NO_WINDOW);
            let mut child = command.spawn()?;
            match job::Job::holding(&child) {
                Ok(job) => Ok((child, ProcessTree { job })),
                Err(err) => {
                    // Still suspended, or outside a job: it must not run on.
                    let _ = child.start_kill();
                    Err(err)
                }
            }
        }
    }

    /// Asks the tree to stop: SIGTERM on Unix. Windows has no such request for a process
    /// without a window, so the tree ends at once.
    pub fn terminate(&self) {
        #[cfg(unix)]
        self.signal(libc::SIGTERM);
        #[cfg(windows)]
        self.kill();
    }

    /// Ends the tree now.
    pub fn kill(&self) {
        #[cfg(unix)]
        self.signal(libc::SIGKILL);
        #[cfg(windows)]
        self.job.terminate();
    }

    #[cfg(unix)]
    fn signal(&self, signal: libc::c_int) {
        if let Some(pid) = self.pid {
            // SAFETY: plain syscall; the child leads its own process group (process_group(0)),
            // so this reaches everything it started.
            unsafe {
                libc::killpg(pid as libc::pid_t, signal);
            }
        }
    }
}

/// The program a command names, as the system starts it. Windows finds only `.exe` files by
/// a bare name, and many tools there are scripts (`npx.cmd`): the name is looked up in `path`
/// (the child's `PATH`; her's own when `None`) with each extension of `PATHEXT`. Elsewhere,
/// and when nothing is found, the name as given.
pub fn program(name: &str, path: Option<&str>) -> std::ffi::OsString {
    #[cfg(windows)]
    if std::path::Path::new(name).extension().is_none() && !name.contains(['/', '\\']) {
        let path = path.map(std::ffi::OsString::from).or_else(|| std::env::var_os("PATH")).unwrap_or_default();
        let extensions = std::env::var("PATHEXT").unwrap_or_else(|_| ".COM;.EXE;.BAT;.CMD".to_string());
        for dir in std::env::split_paths(&path) {
            for extension in extensions.split(';').filter(|e| !e.is_empty()) {
                let file = dir.join(format!("{name}{extension}"));
                if file.is_file() {
                    return file.into_os_string();
                }
            }
        }
    }
    let _ = path;
    name.into()
}

#[cfg(windows)]
mod job {
    use std::io;
    use tokio::process::Child;
    use windows_sys::Win32::Foundation::CloseHandle;
    use windows_sys::Win32::Foundation::HANDLE;
    use windows_sys::Win32::System::JobObjects::AssignProcessToJobObject;
    use windows_sys::Win32::System::JobObjects::CreateJobObjectW;
    use windows_sys::Win32::System::JobObjects::TerminateJobObject;

    pub(super) use windows_sys::Win32::System::Threading::CREATE_NO_WINDOW;
    pub(super) use windows_sys::Win32::System::Threading::CREATE_SUSPENDED;

    #[link(name = "ntdll")]
    unsafe extern "system" {
        fn NtResumeProcess(process: HANDLE) -> i32;
    }

    /// A job object; closing its handle leaves its processes running.
    pub(super) struct Job(HANDLE);

    // SAFETY: a job handle may be used from any thread.
    unsafe impl Send for Job {}
    // SAFETY: as above; the calls made on it are thread-safe.
    unsafe impl Sync for Job {}

    impl Job {
        /// A new job holding `child`, which was started suspended and runs from here on.
        pub(super) fn holding(child: &Child) -> io::Result<Job> {
            let process = child
                .raw_handle()
                .ok_or_else(|| io::Error::other("the process has already exited"))?;
            // SAFETY: no name and default security; the handle is checked below.
            let handle = unsafe { CreateJobObjectW(std::ptr::null(), std::ptr::null()) };
            if handle.is_null() {
                return Err(io::Error::last_os_error());
            }
            let job = Job(handle);
            // SAFETY: both handles are open: the job is ours and the child holds its process's.
            if unsafe { AssignProcessToJobObject(job.0, process as HANDLE) } == 0 {
                return Err(io::Error::last_os_error());
            }
            // SAFETY: the child's own handle, which has every access right.
            let status = unsafe { NtResumeProcess(process as HANDLE) };
            if status != 0 {
                return Err(io::Error::other(format!("resuming the process: NTSTATUS {status:#x}")));
            }
            Ok(job)
        }

        pub(super) fn terminate(&self) {
            // SAFETY: the job's handle is open until drop.
            unsafe {
                TerminateJobObject(self.0, 1);
            }
        }
    }

    impl Drop for Job {
        fn drop(&mut self) {
            // SAFETY: closing the handle this value owns.
            unsafe {
                CloseHandle(self.0);
            }
        }
    }
}
