//! Starting a managed browser: find a Chromium-family executable, run it on its own profile
//! with `--remote-debugging-pipe`, and hold a lock on the profile (browser-design §6.2, §8.2).

use async_trait::async_trait;
use std::fs::File;
use std::io::Read;
use std::io::Seek;
use std::io::Write;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::mpsc;

use crate::BrowserError;
use crate::cdp::CdpEvent;
use crate::cdp::Conn;

/// How long one CDP command may take before it counts as lost.
pub const COMMAND_TIMEOUT: Duration = Duration::from_secs(30);

#[derive(Clone, Debug)]
pub struct LaunchOptions {
    /// Configured executable; found automatically when `None`.
    pub executable: Option<PathBuf>,
    pub user_data_dir: PathBuf,
    pub headless: bool,
}

/// A started browser: the connection, its events, and the process when there is one.
pub struct Launched {
    pub conn: Arc<Conn>,
    pub events: mpsc::UnboundedReceiver<CdpEvent>,
    pub child: Option<tokio::process::Child>,
}

#[async_trait]
pub trait Launcher: Send + Sync + 'static {
    async fn launch(&self, options: &LaunchOptions) -> Result<Launched, BrowserError>;
}

/// Runs a Chromium-family browser installed on this machine.
pub struct ManagedLauncher;

#[async_trait]
impl Launcher for ManagedLauncher {
    async fn launch(&self, options: &LaunchOptions) -> Result<Launched, BrowserError> {
        let executable = match &options.executable {
            Some(path) if path.is_file() => path.clone(),
            Some(path) => {
                return Err(BrowserError::NotFound(format!(
                    "browser.executable {} does not exist",
                    path.display()
                )));
            }
            None => find_executable().ok_or_else(|| BrowserError::NotFound(NOT_FOUND.to_string()))?,
        };
        #[cfg(unix)]
        {
            spawn(&executable, options)
        }
        #[cfg(windows)]
        {
            spawn(&executable, options).await
        }
    }
}

const NOT_FOUND: &str = "No Chrome, Edge or Chromium was found on this machine. Install Google Chrome, \
    run `her browser install` to install a browser for her alone, or set browser.executable in \
    ~/.her/config.toml to the path of a Chromium-based browser.";

/// The Chrome for Testing platform of this machine, as its downloads name it (D-194).
pub fn testing_platform() -> Option<&'static str> {
    match (std::env::consts::OS, std::env::consts::ARCH) {
        ("macos", "aarch64") => Some("mac-arm64"),
        ("macos", "x86_64") => Some("mac-x64"),
        ("linux", "x86_64") => Some("linux64"),
        ("windows", "x86_64") => Some("win64"),
        ("windows", "x86") => Some("win32"),
        _ => None,
    }
}

/// Where the executable lies in an unpacked Chrome for Testing archive of `platform`.
pub fn testing_executable(platform: &str) -> Option<PathBuf> {
    let app = "Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing";
    Some(match platform {
        "mac-arm64" | "mac-x64" => PathBuf::from(format!("chrome-{platform}")).join(app),
        "linux64" => PathBuf::from("chrome-linux64/chrome"),
        "win64" | "win32" => PathBuf::from(format!("chrome-{platform}")).join("chrome.exe"),
        _ => return None,
    })
}

/// The browser `her browser install` put under `browser_root` (`~/.her/browser`), if any.
pub fn installed_executable(browser_root: &Path) -> Option<PathBuf> {
    let path = browser_root.join("chromium").join(testing_executable(testing_platform()?)?);
    path.is_file().then_some(path)
}

/// The first Chromium-family browser in the usual places.
pub fn find_executable() -> Option<PathBuf> {
    candidates().into_iter().find(|path| path.is_file())
}

/// Launches the user's own Chromium-family browser (in the background on macOS) so the Hermesh
/// extension can reconnect after the browser was closed.
pub fn launch_user_browser(browser_label: Option<&str>) -> bool {
    if std::env::var_os("HER_NO_BROWSER").is_some() {
        return false;
    }
    #[cfg(target_os = "macos")]
    {
        let app = macos_app_name(browser_label);
        return std::process::Command::new("/usr/bin/open")
            .args(["-g", "-a", &app])
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .spawn()
            .is_ok();
    }
    #[cfg(not(target_os = "macos"))]
    {
        let Some(exe) = find_executable_for(browser_label) else {
            return false;
        };
        let mut cmd = std::process::Command::new(exe);
        cmd.envs(crate::pool::session_display())
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null());
        cmd.spawn().is_ok()
    }
}

#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
fn macos_app_name(browser_label: Option<&str>) -> String {
    let lower = browser_label.unwrap_or("").trim().to_ascii_lowercase();
    if lower.contains("canary") {
        return "Google Chrome Canary".into();
    }
    if lower.contains("beta") && lower.contains("chrome") {
        return "Google Chrome Beta".into();
    }
    if lower.contains("edge") {
        return "Microsoft Edge".into();
    }
    if lower.contains("brave") {
        return "Brave Browser".into();
    }
    if lower == "chromium" {
        return "Chromium".into();
    }
    if lower.contains("chrome") {
        return "Google Chrome".into();
    }
    if let Some(exe) = find_executable()
        && let Some(app) = exe.ancestors().find(|p| p.extension().is_some_and(|e| e == "app"))
        && let Some(stem) = app.file_stem().and_then(|s| s.to_str())
    {
        return stem.to_string();
    }
    "Google Chrome".into()
}

#[cfg(not(target_os = "macos"))]
fn find_executable_for(browser_label: Option<&str>) -> Option<PathBuf> {
    let lower = browser_label.unwrap_or("").trim().to_ascii_lowercase();
    let needle = if lower.contains("edge") {
        Some("edge")
    } else if lower.contains("brave") {
        Some("brave")
    } else if lower == "chromium" {
        Some("chromium")
    } else if lower.contains("chrome") {
        Some("chrome")
    } else {
        None
    };
    let all = candidates();
    if let Some(needle) = needle
        && let Some(found) = all
            .iter()
            .find(|p| p.is_file() && p.to_string_lossy().to_ascii_lowercase().contains(needle))
    {
        return Some(found.clone());
    }
    all.into_iter().find(|p| p.is_file())
}

fn candidates() -> Vec<PathBuf> {
    let mut out = Vec::new();
    if cfg!(target_os = "macos") {
        let apps = [
            "Google Chrome.app/Contents/MacOS/Google Chrome",
            "Chromium.app/Contents/MacOS/Chromium",
            "Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
            "Brave Browser.app/Contents/MacOS/Brave Browser",
            "Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
        ];
        let mut roots = vec![PathBuf::from("/Applications")];
        if let Some(home) = std::env::home_dir() {
            roots.push(home.join("Applications"));
        }
        for root in roots {
            out.extend(apps.iter().map(|app| root.join(app)));
        }
    } else if cfg!(windows) {
        for var in ["PROGRAMFILES", "PROGRAMFILES(X86)", "LOCALAPPDATA"] {
            if let Some(root) = std::env::var_os(var).map(PathBuf::from) {
                out.push(root.join(r"Google\Chrome\Application\chrome.exe"));
                out.push(root.join(r"Microsoft\Edge\Application\msedge.exe"));
                out.push(root.join(r"Chromium\Application\chrome.exe"));
            }
        }
    } else {
        let names = [
            "google-chrome",
            "google-chrome-stable",
            "chromium",
            "chromium-browser",
            "microsoft-edge",
            "microsoft-edge-stable",
            "brave-browser",
        ];
        if let Some(path) = std::env::var_os("PATH") {
            for dir in std::env::split_paths(&path) {
                out.extend(names.iter().map(|name| dir.join(name)));
            }
        }
        out.push(PathBuf::from("/snap/bin/chromium"));
    }
    out
}

/// Variables the browser keeps; everything else (API keys among them) stays out.
const KEPT_ENV: &[&str] = &[
    "HOME",
    "PATH",
    "USER",
    "LOGNAME",
    "LANG",
    "LANGUAGE",
    "LC_ALL",
    "LC_CTYPE",
    "TZ",
    "TMPDIR",
    "TMP",
    "TEMP",
    "DISPLAY",
    "WAYLAND_DISPLAY",
    "XAUTHORITY",
    "XDG_RUNTIME_DIR",
    "XDG_CONFIG_HOME",
    "XDG_CACHE_HOME",
    "DBUS_SESSION_BUS_ADDRESS",
    "FONTCONFIG_PATH",
    "SYSTEMROOT",
    "SystemRoot",
    "USERPROFILE",
    "LOCALAPPDATA",
    "APPDATA",
    "PROGRAMFILES",
];

/// The browser's arguments; `root`: her runs as root on Linux (a server's), where Chrome won't
/// start with its sandbox.
fn arguments(options: &LaunchOptions, root: bool) -> Vec<String> {
    let mut args = vec![
        "--remote-debugging-pipe".to_string(),
        format!("--user-data-dir={}", options.user_data_dir.display()),
        "--no-first-run".into(),
        "--no-default-browser-check".into(),
        "--disable-sync".into(),
        "--disable-background-networking".into(),
        "--disable-background-timer-throttling".into(),
        "--disable-backgrounding-occluded-windows".into(),
        "--disable-renderer-backgrounding".into(),
        "--disable-ipc-flooding-protection".into(),
        "--disable-component-update".into(),
        "--disable-default-apps".into(),
        "--disable-features=Translate,MediaRouter,OptimizationHints,CalculateNativeWinOcclusion,IntensiveWakeUpThrottling,BackForwardCache".into(),
        // The profile is her's own: no prompt for the system keychain.
        "--password-store=basic".into(),
        "--use-mock-keychain".into(),
    ];
    if options.headless {
        args.push("--headless=new".into());
    }
    if root {
        args.push("--no-sandbox".into());
    }
    args.push("about:blank".into());
    args
}

#[cfg(unix)]
fn spawn(executable: &Path, options: &LaunchOptions) -> Result<Launched, BrowserError> {
    use std::os::fd::AsRawFd;
    use std::os::fd::FromRawFd;
    use std::os::fd::OwnedFd;

    std::fs::create_dir_all(&options.user_data_dir).map_err(|e| {
        BrowserError::Launch(format!("cannot create {}: {e}", options.user_data_dir.display()))
    })?;
    // `to_browser`: the browser reads fd 3; `from_browser`: it writes fd 4.
    let pipe = || -> std::io::Result<(OwnedFd, OwnedFd)> {
        let mut fds = [0; 2];
        // SAFETY: `fds` has room for the two descriptors pipe(2) writes.
        if unsafe { libc::pipe(fds.as_mut_ptr()) } != 0 {
            return Err(std::io::Error::last_os_error());
        }
        // SAFETY: pipe(2) just returned these two descriptors, owned by nobody else.
        let (read, write) = unsafe { (OwnedFd::from_raw_fd(fds[0]), OwnedFd::from_raw_fd(fds[1])) };
        Ok((high(read)?, high(write)?))
    };
    let launch_error = |e: std::io::Error| BrowserError::Launch(format!("cannot create the browser pipe: {e}"));
    let (browser_reads, we_write) = pipe().map_err(launch_error)?;
    let (we_read, browser_writes) = pipe().map_err(launch_error)?;
    let child_read = browser_reads.as_raw_fd();
    let child_write = browser_writes.as_raw_fd();
    let mut command = tokio::process::Command::new(executable);
    command
        // SAFETY: geteuid(2) only reads the process's effective user ID.
        .args(arguments(options, cfg!(target_os = "linux") && unsafe { libc::geteuid() } == 0))
        .env_clear()
        .envs(KEPT_ENV.iter().filter_map(|name| std::env::var_os(name).map(|value| (name, value))))
        .envs(crate::pool::session_display())
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .kill_on_drop(true);
    // SAFETY: only async-signal-safe calls (dup2) between fork and exec. Both descriptors are
    // at 10 or above, so moving them to 3 and 4 cannot overwrite one with the other; dup2
    // clears close-on-exec on the copies, which the browser inherits.
    unsafe {
        command.pre_exec(move || {
            if libc::dup2(child_read, 3) < 0 || libc::dup2(child_write, 4) < 0 {
                return Err(std::io::Error::last_os_error());
            }
            Ok(())
        });
    }
    let child = command
        .spawn()
        .map_err(|e| BrowserError::Launch(format!("cannot start {}: {e}", executable.display())))?;
    drop(browser_reads);
    drop(browser_writes);
    let reader = tokio::net::unix::pipe::Receiver::from_owned_fd(nonblocking(we_read).map_err(launch_error)?)
        .map_err(launch_error)?;
    let writer = tokio::net::unix::pipe::Sender::from_owned_fd(nonblocking(we_write).map_err(launch_error)?)
        .map_err(launch_error)?;
    let (conn, events) = Conn::start(reader, writer, COMMAND_TIMEOUT);
    Ok(Launched {
        conn,
        events,
        child: Some(child),
    })
}

/// The descriptor moved to 10 or above, close-on-exec.
#[cfg(unix)]
fn high(fd: std::os::fd::OwnedFd) -> std::io::Result<std::os::fd::OwnedFd> {
    use std::os::fd::AsRawFd;
    use std::os::fd::FromRawFd;
    // SAFETY: F_DUPFD_CLOEXEC on a valid descriptor returns a new one or -1.
    let moved = unsafe { libc::fcntl(fd.as_raw_fd(), libc::F_DUPFD_CLOEXEC, 10) };
    if moved < 0 {
        return Err(std::io::Error::last_os_error());
    }
    // SAFETY: fcntl just created `moved`; `fd` is closed when dropped here.
    Ok(unsafe { std::os::fd::OwnedFd::from_raw_fd(moved) })
}

#[cfg(unix)]
fn nonblocking(fd: std::os::fd::OwnedFd) -> std::io::Result<std::os::fd::OwnedFd> {
    use std::os::fd::AsRawFd;
    // SAFETY: plain fcntl calls on a descriptor we own.
    unsafe {
        let flags = libc::fcntl(fd.as_raw_fd(), libc::F_GETFL);
        if flags < 0 || libc::fcntl(fd.as_raw_fd(), libc::F_SETFL, flags | libc::O_NONBLOCK) < 0 {
            return Err(std::io::Error::last_os_error());
        }
    }
    Ok(fd)
}

/// On Windows the browser takes its two pipe ends as handles it inherits, named in
/// `--remote-debugging-io-pipes` (browser-design D-195). They are named pipes: her's ends are
/// overlapped, as tokio needs, and the browser's are plain.
#[cfg(windows)]
async fn spawn(executable: &Path, options: &LaunchOptions) -> Result<Launched, BrowserError> {
    use std::os::windows::io::AsRawHandle;
    use std::sync::atomic::AtomicU64;
    use std::sync::atomic::Ordering;
    use tokio::net::windows::named_pipe::ServerOptions;
    use windows_sys::Win32::Foundation::HANDLE;
    use windows_sys::Win32::Foundation::HANDLE_FLAG_INHERIT;
    use windows_sys::Win32::Foundation::SetHandleInformation;

    static LAUNCHES: AtomicU64 = AtomicU64::new(0);

    std::fs::create_dir_all(&options.user_data_dir).map_err(|e| {
        BrowserError::Launch(format!("cannot create {}: {e}", options.user_data_dir.display()))
    })?;
    let launch_error = |e: std::io::Error| BrowserError::Launch(format!("cannot create the browser pipe: {e}"));
    let launch = LAUNCHES.fetch_add(1, Ordering::Relaxed);
    let name = |end: &str| format!(r"\\.\pipe\her-browser-{}-{launch}-{end}", std::process::id());
    // One instance each, the first of its name: nobody else serves or joins these pipes.
    let pipe = |end: &str, inbound: bool| -> std::io::Result<_> {
        let name = name(end);
        let ours = ServerOptions::new()
            .access_inbound(inbound)
            .access_outbound(!inbound)
            .first_pipe_instance(true)
            .max_instances(1)
            .create(&name)?;
        let theirs = std::fs::OpenOptions::new().read(!inbound).write(inbound).open(&name)?;
        // SAFETY: the handle is open, owned by `theirs`.
        if unsafe { SetHandleInformation(theirs.as_raw_handle() as HANDLE, HANDLE_FLAG_INHERIT, HANDLE_FLAG_INHERIT) } == 0 {
            return Err(std::io::Error::last_os_error());
        }
        Ok((ours, theirs))
    };
    let (we_write, browser_reads) = pipe("in", false).map_err(launch_error)?;
    let (we_read, browser_writes) = pipe("out", true).map_err(launch_error)?;
    // The other ends are open already: this only tells tokio's pipes so.
    we_write.connect().await.map_err(launch_error)?;
    we_read.connect().await.map_err(launch_error)?;
    let mut args = arguments(options, false);
    args.insert(
        1,
        format!(
            "--remote-debugging-io-pipes={},{}",
            browser_reads.as_raw_handle() as usize,
            browser_writes.as_raw_handle() as usize
        ),
    );
    let child = tokio::process::Command::new(executable)
        .args(args)
        .env_clear()
        .envs(KEPT_ENV.iter().filter_map(|name| std::env::var_os(name).map(|value| (name, value))))
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .kill_on_drop(true)
        .spawn()
        .map_err(|e| BrowserError::Launch(format!("cannot start {}: {e}", executable.display())))?;
    drop(browser_reads);
    drop(browser_writes);
    let (conn, events) = Conn::start(we_read, we_write, COMMAND_TIMEOUT);
    Ok(Launched {
        conn,
        events,
        child: Some(child),
    })
}

/// A profile held by this process: an advisory lock that the system releases when the
/// process ends, however it ends, plus the holder's pid for the message to others.
pub struct ProfileLock {
    _file: File,
}

impl ProfileLock {
    /// Takes the lock beside `profile`, or says which process holds it.
    pub fn acquire(profile: &Path) -> Result<ProfileLock, Option<u32>> {
        let path = lock_path(profile);
        if let Some(parent) = path.parent() {
            let _ = std::fs::create_dir_all(parent);
        }
        let mut file = std::fs::OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(false)
            .open(&path)
            .map_err(|_| None)?;
        if !try_lock(&file) {
            let mut text = String::new();
            let _ = file.read_to_string(&mut text);
            return Err(text.trim().parse().ok());
        }
        let _ = file.set_len(0);
        let _ = file.rewind();
        let _ = write!(file, "{}", std::process::id());
        let _ = file.flush();
        Ok(ProfileLock { _file: file })
    }
}

fn lock_path(profile: &Path) -> PathBuf {
    let name = profile.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
    profile.with_file_name(format!("{name}.lock"))
}

#[cfg(unix)]
fn try_lock(file: &File) -> bool {
    use std::os::fd::AsRawFd;
    // SAFETY: flock on a descriptor we own.
    unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) == 0 }
}

/// Windows keeps other processes from reading a locked range, and the holder's pid in the
/// file is for them to read: the lock is on one byte far past the file's end.
#[cfg(windows)]
fn try_lock(file: &File) -> bool {
    use std::os::windows::io::AsRawHandle;
    use windows_sys::Win32::Foundation::HANDLE;
    use windows_sys::Win32::Storage::FileSystem::LOCKFILE_EXCLUSIVE_LOCK;
    use windows_sys::Win32::Storage::FileSystem::LOCKFILE_FAIL_IMMEDIATELY;
    use windows_sys::Win32::Storage::FileSystem::LockFileEx;
    use windows_sys::Win32::System::IO::OVERLAPPED;

    // SAFETY: zeroes are a valid value of this plain C struct.
    let mut at: OVERLAPPED = unsafe { std::mem::zeroed() };
    at.Anonymous.Anonymous.OffsetHigh = 1;
    // SAFETY: LockFileEx on a handle we own; `at` names the byte and outlives the call.
    unsafe {
        LockFileEx(file.as_raw_handle() as HANDLE, LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &mut at) != 0
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_profile_is_locked_once_per_process_lifetime() {
        let dir = tempfile::tempdir().unwrap();
        let profile = dir.path().join("profiles").join("default");
        let lock = ProfileLock::acquire(&profile).expect("first lock");
        // flock locks belong to the open file, so a second open in this process conflicts too.
        assert_eq!(ProfileLock::acquire(&profile).err(), Some(Some(std::process::id())));
        drop(lock);
        assert!(ProfileLock::acquire(&profile).is_ok());
    }

    #[test]
    fn headless_and_profile_arguments() {
        let options = LaunchOptions {
            executable: None,
            user_data_dir: PathBuf::from("/tmp/p"),
            headless: true,
        };
        let args = arguments(&options, false);
        assert!(args.contains(&"--remote-debugging-pipe".to_string()));
        assert!(args.contains(&"--user-data-dir=/tmp/p".to_string()));
        assert!(args.contains(&"--headless=new".to_string()));
        assert!(!args.iter().any(|a| a.starts_with("--remote-debugging-port")));
        assert!(!args.contains(&"--no-sandbox".to_string()));
        // As root, without the sandbox Chrome refuses to start in.
        assert!(arguments(&options, true).contains(&"--no-sandbox".to_string()));
        assert!(args.contains(&"--disable-background-timer-throttling".to_string()));
        assert!(args.contains(&"--disable-backgrounding-occluded-windows".to_string()));
        assert!(args.contains(&"--disable-renderer-backgrounding".to_string()));
    }

    #[test]
    fn macos_app_name_maps_known_browsers() {
        assert_eq!(macos_app_name(Some("Google Chrome")), "Google Chrome");
        assert_eq!(macos_app_name(Some("Chrome")), "Google Chrome");
        assert_eq!(macos_app_name(Some("Microsoft Edge")), "Microsoft Edge");
        assert_eq!(macos_app_name(Some("Brave")), "Brave Browser");
        assert_eq!(macos_app_name(Some("Chromium")), "Chromium");
        assert_eq!(macos_app_name(Some("Google Chrome Canary")), "Google Chrome Canary");
    }
}
