//! The Hermesh extension's host on Windows (browser-design §8.4).
//!
//! A browser finds a host through the registry: `HKCU\Software\<browser>\NativeMessagingHosts\
//! com.hermesh.browser` names the host's manifest, for every profile of that browser. It
//! starts the manifest's program with its own arguments alone (the extension's origin, the
//! parent window) and none of our environment. So her itself is that program, and what the
//! launcher script holds elsewhere, her's home and the service's endpoint, is in the registry
//! too, under `HKCU\Software\Hermesh\Her\NativeHost`.

use anyhow::Context as _;
use serde_json::Value;
use std::ffi::OsString;
use std::io;
use std::path::Path;
use std::path::PathBuf;
use windows_sys::Win32::Foundation::ERROR_FILE_NOT_FOUND;
use windows_sys::Win32::Foundation::ERROR_SUCCESS;
use windows_sys::Win32::System::Registry::HKEY;
use windows_sys::Win32::System::Registry::HKEY_CURRENT_USER;
use windows_sys::Win32::System::Registry::KEY_WRITE;
use windows_sys::Win32::System::Registry::REG_OPTION_NON_VOLATILE;
use windows_sys::Win32::System::Registry::REG_SZ;
use windows_sys::Win32::System::Registry::RRF_RT_REG_SZ;
use windows_sys::Win32::System::Registry::RegCloseKey;
use windows_sys::Win32::System::Registry::RegCreateKeyExW;
use windows_sys::Win32::System::Registry::RegDeleteKeyW;
use windows_sys::Win32::System::Registry::RegDeleteKeyValueW;
use windows_sys::Win32::System::Registry::RegGetValueW;
use windows_sys::Win32::System::Registry::RegSetValueExW;

use crate::cmd_browser::HOST_NAME;
use crate::cmd_browser::allowed_origins;
use crate::cmd_browser::host_manifest;
use crate::cmd_browser::place_extension;
use crate::setup;

/// The keys Chromium-family browsers look their hosts up in, under `HKCU\Software`, by the
/// browsers that read each. Every channel of Chrome reads Chrome's, and so does Brave.
const BROWSERS: &[(&str, &str)] = &[("Google Chrome", r"Google\Chrome"), ("Chromium", "Chromium"), ("Microsoft Edge", r"Microsoft\Edge")];

/// Where the host's settings are, under `HKCU\Software`: `Home` and, when the service is not
/// the home's, `Socket`.
const SETTINGS: &str = r"Hermesh\Her\NativeHost";

/// A part of the current user's registry: `Software`, or a key of a test's own.
struct Hive(String);

impl Hive {
    fn user() -> Hive {
        Hive("Software".to_string())
    }

    fn path(&self, key: &str) -> Vec<u16> {
        wide(&format!(r"{}\{key}", self.0))
    }

    /// The string `name` of `key` (its default value when `None`), if there is one.
    fn get(&self, key: &str, name: Option<&str>) -> Option<String> {
        let name = name.map(wide);
        let name = name.as_ref().map_or(std::ptr::null(), |name| name.as_ptr());
        let mut buf = vec![0u16; 1024];
        loop {
            let mut bytes = (buf.len() * 2) as u32;
            // SAFETY: the strings end with NUL, and the buffer is as large as `bytes` says.
            let status = unsafe {
                RegGetValueW(HKEY_CURRENT_USER, self.path(key).as_ptr(), name, RRF_RT_REG_SZ, std::ptr::null_mut(), buf.as_mut_ptr().cast(), &mut bytes)
            };
            match status {
                ERROR_SUCCESS => {
                    let len = (bytes as usize / 2).min(buf.len());
                    let text = String::from_utf16_lossy(&buf[..len]);
                    return Some(text.trim_end_matches('\0').to_string());
                }
                // ERROR_MORE_DATA: `bytes` now says how much the value takes.
                234 if bytes as usize > buf.len() * 2 => buf.resize(bytes as usize / 2 + 1, 0),
                _ => return None,
            }
        }
    }

    fn set(&self, key: &str, name: Option<&str>, value: &str) -> io::Result<()> {
        let mut handle: HKEY = std::ptr::null_mut();
        // SAFETY: the path ends with NUL; the key's handle is written to `handle`.
        let status = unsafe {
            RegCreateKeyExW(
                HKEY_CURRENT_USER,
                self.path(key).as_ptr(),
                0,
                std::ptr::null(),
                REG_OPTION_NON_VOLATILE,
                KEY_WRITE,
                std::ptr::null(),
                &mut handle,
                std::ptr::null_mut(),
            )
        };
        if status != ERROR_SUCCESS {
            return Err(io::Error::from_raw_os_error(status as i32));
        }
        let name = name.map(wide);
        let name = name.as_ref().map_or(std::ptr::null(), |name| name.as_ptr());
        let value = wide(value);
        // SAFETY: the key is open; the value is a NUL-terminated string of the length given.
        let status = unsafe {
            let status = RegSetValueExW(handle, name, 0, REG_SZ, value.as_ptr().cast(), (value.len() * 2) as u32);
            RegCloseKey(handle);
            status
        };
        match status {
            ERROR_SUCCESS => Ok(()),
            status => Err(io::Error::from_raw_os_error(status as i32)),
        }
    }

    /// Removes `key`, which has no keys under it; whether it was there.
    fn remove(&self, key: &str) -> io::Result<bool> {
        // SAFETY: the path ends with NUL.
        match unsafe { RegDeleteKeyW(HKEY_CURRENT_USER, self.path(key).as_ptr()) } {
            ERROR_SUCCESS => Ok(true),
            ERROR_FILE_NOT_FOUND => Ok(false),
            status => Err(io::Error::from_raw_os_error(status as i32)),
        }
    }

    fn remove_value(&self, key: &str, name: &str) -> io::Result<()> {
        // SAFETY: both strings end with NUL.
        match unsafe { RegDeleteKeyValueW(HKEY_CURRENT_USER, self.path(key).as_ptr(), wide(name).as_ptr()) } {
            ERROR_SUCCESS | ERROR_FILE_NOT_FOUND => Ok(()),
            status => Err(io::Error::from_raw_os_error(status as i32)),
        }
    }
}

fn wide(text: &str) -> Vec<u16> {
    text.encode_utf16().chain([0]).collect()
}

fn host_key(browser: &str) -> String {
    format!(r"{browser}\NativeMessagingHosts\{HOST_NAME}")
}

/// The home's manifest of the host.
fn manifest_path(browser_dir: &Path) -> PathBuf {
    browser_dir.join(format!("{HOST_NAME}.json"))
}

/// The command line Chrome starts the host with, as `her browser native-host` takes it, and
/// the home the host was registered for. Anything else comes back as it is.
pub fn host_command(args: Vec<OsString>) -> (Vec<OsString>, Option<PathBuf>) {
    host_command_in(&Hive::user(), args)
}

fn host_command_in(hive: &Hive, args: Vec<OsString>) -> (Vec<OsString>, Option<PathBuf>) {
    if !args.get(1).is_some_and(|first| first.to_string_lossy().starts_with("chrome-extension://")) {
        return (args, None);
    }
    let mut command: Vec<OsString> = vec![args[0].clone(), "browser".into(), "native-host".into()];
    if let Some(socket) = hive.get(SETTINGS, Some("Socket")) {
        command.extend(["--socket".into(), socket.into()]);
    }
    command.extend(args.into_iter().skip(1));
    (command, hive.get(SETTINGS, Some("Home")).map(PathBuf::from))
}

/// Writes the home's manifest, which names `exe`, and has every browser find it; the host's
/// settings beside. The manifest's path.
fn register(hive: &Hive, home: &Path, browser_dir: &Path, exe: &Path, socket: Option<&Path>, origins: &[String]) -> anyhow::Result<PathBuf> {
    std::fs::create_dir_all(browser_dir)?;
    let manifest = manifest_path(browser_dir);
    std::fs::write(&manifest, serde_json::to_string_pretty(&host_manifest(exe, origins))? + "\n")?;
    hive.set(SETTINGS, Some("Home"), &home.to_string_lossy())?;
    match socket {
        Some(socket) => hive.set(SETTINGS, Some("Socket"), &socket.to_string_lossy())?,
        None => hive.remove_value(SETTINGS, "Socket")?,
    }
    for (_, browser) in BROWSERS {
        hive.set(&host_key(browser), None, &manifest.to_string_lossy())?;
    }
    Ok(manifest)
}

/// Takes back what [`register`] did; the browsers it was registered for.
fn unregister(hive: &Hive) -> io::Result<Vec<&'static str>> {
    let mut removed = Vec::new();
    for (name, browser) in BROWSERS {
        if hive.remove(&host_key(browser))? {
            removed.push(*name);
        }
    }
    hive.remove(SETTINGS)?;
    Ok(removed)
}

/// What a browser's registration says about the host it would start, for this home.
fn state(hive: &Hive, manifest: &Path, home: &Path) -> String {
    let program = std::fs::read_to_string(manifest)
        .ok()
        .and_then(|text| serde_json::from_str::<Value>(&text).ok())
        .and_then(|value| value["path"].as_str().map(PathBuf::from));
    match program {
        None => "the manifest cannot be read; run `her browser extension install` again".to_string(),
        Some(program) if !program.exists() => format!("points at {}, which is gone; run `her browser extension install` again", program.display()),
        Some(_) => match hive.get(SETTINGS, Some("Home")).map(PathBuf::from) {
            Some(registered) if registered != home => format!("registered by another Hermesh home ({})", registered.display()),
            _ => "installed".to_string(),
        },
    }
}

/// The browsers the host is registered for, with their manifests.
fn registered(hive: &Hive) -> Vec<(&'static str, PathBuf)> {
    BROWSERS.iter().filter_map(|(name, browser)| Some((*name, PathBuf::from(hive.get(&host_key(browser), None)?)))).collect()
}

/// The program a manifest starts.
fn program_of(manifest: &Path) -> Option<PathBuf> {
    let value: Value = serde_json::from_str(&std::fs::read_to_string(manifest).ok()?).ok()?;
    value["path"].as_str().map(PathBuf::from)
}

/// `her browser extension install`: the manifest and the registry's entries for every
/// Chromium-family browser; with `folder`, the extension itself there, to load in Chrome.
/// A browser's registry entry serves all its profiles, so `user_data_dir` changes nothing.
pub fn install(extension_ids: &[String], _user_data_dir: Option<PathBuf>, socket: Option<PathBuf>, folder: Option<PathBuf>) -> anyhow::Result<u8> {
    let home = setup::home()?;
    // First: it is there to load even where no browser is installed yet.
    if let Some(folder) = &folder {
        place_extension(folder).with_context(|| format!("cannot write the extension to {}", folder.display()))?;
        println!("extension: {}", folder.display());
    }
    let exe = std::env::current_exe().context("cannot find the her program")?;
    // Chrome starts the host in a folder of its own.
    let socket = socket.map(std::path::absolute).transpose()?;
    let origins = allowed_origins(extension_ids)?;
    let manifest = register(&Hive::user(), home.root(), &home.browser_dir(), &exe, socket.as_deref(), &origins)?;
    for (name, _) in BROWSERS {
        println!("{name}: {}", manifest.display());
    }
    println!("host: {}", exe.display());
    if let Some(socket) = &socket {
        println!("service: {}", socket.display());
    }
    println!(
        "Next: load the Hermesh extension in Chrome, and set extension = true under [browser] in {}.",
        home.root().join("config.toml").display()
    );
    Ok(0)
}

/// `her browser extension uninstall`: removes the registry's entries and the manifest.
pub fn uninstall() -> anyhow::Result<u8> {
    let home = setup::home()?;
    for name in unregister(&Hive::user())? {
        println!("removed for {name}");
    }
    let manifest = manifest_path(&home.browser_dir());
    if manifest.exists() {
        std::fs::remove_file(&manifest)?;
        println!("removed {}", manifest.display());
    }
    Ok(0)
}

/// `her browser extension status`: which browsers find the host, and whether what each would
/// start is there and is this home's.
pub fn status(as_json: bool) -> anyhow::Result<u8> {
    let home = setup::home()?;
    let hive = Hive::user();
    let found = registered(&hive);
    let program = found.first().and_then(|(_, manifest)| program_of(manifest));
    if as_json {
        let browsers: Vec<Value> = found
            .iter()
            .map(|(name, manifest)| serde_json::json!({ "name": name, "state": state(&hive, manifest, home.root()), "manifest": manifest }))
            .collect();
        let host = program.as_ref().filter(|program| program.exists());
        println!("{}", serde_json::json!({ "browsers": browsers, "host": host, "program": program }));
        return Ok(0);
    }
    for (name, manifest) in &found {
        println!("{name}: {} ({})", state(&hive, manifest, home.root()), manifest.display());
    }
    match program {
        _ if found.is_empty() => println!("not installed; run `her browser extension install`"),
        Some(program) => {
            let state = if !program.exists() { " (missing; run `her browser extension install` again)" } else { "" };
            println!("host: {}{state}", program.display());
        }
        None => {}
    }
    Ok(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A key of this test's own, removed with what the test put under it.
    struct TestHive(Hive);

    const TESTS: &str = r"Software\Hermesh\Tests";

    impl TestHive {
        fn new(name: &str) -> TestHive {
            TestHive(Hive(format!(r"{TESTS}\{name}-{}", std::process::id())))
        }
    }

    impl Drop for TestHive {
        fn drop(&mut self) {
            let _ = unregister(&self.0);
            // A key goes only once nothing is under it: the deepest first.
            let keys = [
                r"Google\Chrome\NativeMessagingHosts",
                r"Google\Chrome",
                "Google",
                r"Chromium\NativeMessagingHosts",
                "Chromium",
                r"Microsoft\Edge\NativeMessagingHosts",
                r"Microsoft\Edge",
                "Microsoft",
                r"Hermesh\Her",
                "Hermesh",
            ];
            for key in keys {
                let _ = self.0.remove(key);
            }
            let name = self.0.0.rsplit('\\').next().unwrap_or_default().to_string();
            let _ = Hive(TESTS.to_string()).remove(&name);
        }
    }

    /// Registered, every browser finds the manifest, which starts this program; Chrome's
    /// command line becomes the host's, for the registered home and socket; unregistered,
    /// nothing is left.
    #[test]
    fn browsers_find_the_host_and_the_host_its_home() {
        let hive = TestHive::new("host");
        let hive = &hive.0;
        let dir = tempfile::tempdir().unwrap();
        let home = dir.path().join("家 home");
        let exe = std::env::current_exe().unwrap();
        let origins = allowed_origins(&[]).unwrap();
        assert!(registered(hive).is_empty());

        let socket = dir.path().join("her.sock");
        let manifest = register(hive, &home, &home.join("browser"), &exe, Some(&socket), &origins).unwrap();
        let found = registered(hive);
        assert_eq!(found.len(), BROWSERS.len());
        assert!(found.iter().all(|(_, path)| *path == manifest), "{found:?}");
        assert_eq!(program_of(&manifest), Some(exe.clone()));
        assert_eq!(state(hive, &manifest, &home), "installed");
        assert!(state(hive, &manifest, &dir.path().join("other")).contains("another Hermesh home"));

        let chrome = |args: &[&str]| args.iter().map(OsString::from).collect::<Vec<_>>();
        let (command, registered_home) = host_command_in(hive, chrome(&["her.exe", "chrome-extension://abc/", "--parent-window=0"]));
        let socket_text = socket.to_string_lossy().into_owned();
        assert_eq!(command, chrome(&["her.exe", "browser", "native-host", "--socket", &socket_text, "chrome-extension://abc/", "--parent-window=0"]));
        assert_eq!(registered_home, Some(home.clone()));
        // Any other command line is her's own.
        let (command, registered_home) = host_command_in(hive, chrome(&["her.exe", "sessions"]));
        assert_eq!((command, registered_home), (chrome(&["her.exe", "sessions"]), None));

        // Registered again without a socket: the home's own service.
        register(hive, &home, &home.join("browser"), &exe, None, &origins).unwrap();
        let (command, _) = host_command_in(hive, chrome(&["her.exe", "chrome-extension://abc/"]));
        assert_eq!(command, chrome(&["her.exe", "browser", "native-host", "chrome-extension://abc/"]));

        assert_eq!(unregister(hive).unwrap().len(), BROWSERS.len());
        assert!(registered(hive).is_empty());
        assert_eq!(hive.get(SETTINGS, Some("Home")), None);
    }
}
