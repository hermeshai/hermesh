//! Keys saved for providers (`auth/set`), looked up after the configuration and the environment.
//!
//! On macOS they live in the login keychain, read and written through `/usr/bin/security`: the
//! items' access list then names that system tool rather than her, so a new her build reads
//! them without a keychain prompt, and the key reaches the tool on stdin, never in its
//! arguments. Elsewhere they are a private file next to the configuration. Nothing is installed
//! by default: only `her` itself chooses a store, so tests never touch a real keychain.

use parking_lot::Mutex;
use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::LazyLock;
use std::sync::OnceLock;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use std::time::SystemTime;

pub trait CredentialStore: Send + Sync {
    /// Where keys go, for messages ("keychain").
    fn name(&self) -> &'static str;
    fn get(&self, provider: &str) -> Result<Option<String>, String>;
    fn set(&self, provider: &str, key: &str) -> Result<(), String>;
    fn delete(&self, provider: &str) -> Result<(), String>;
}

static STORE: OnceLock<Box<dyn CredentialStore>> = OnceLock::new();
/// Provider → key (or its absence), read at most once per process.
static CACHE: LazyLock<Mutex<HashMap<String, Option<String>>>> = LazyLock::new(|| Mutex::new(HashMap::new()));

/// A file every `set` and `delete` touches, here or in another process: the saved keys changed.
static STAMP: OnceLock<PathBuf> = OnceLock::new();
/// The stamp's time when the cache was last good.
static SEEN: Mutex<Option<SystemTime>> = Mutex::new(None);
/// Counts the changes this process noticed from elsewhere.
static CHANGES: AtomicU64 = AtomicU64::new(0);

/// Chooses the store for this process; later calls are ignored.
pub fn install(store: Box<dyn CredentialStore>) {
    let _ = STORE.set(store);
}

/// Names the stamp file, so that keys saved by another process are seen here at once. A `stat`
/// costs about a microsecond (standalone-cli-design §11.3).
pub fn watch(stamp: PathBuf) {
    let time = stamp_time(&stamp);
    if STAMP.set(stamp).is_ok() {
        *SEEN.lock() = time;
    }
}

fn stamp_time(path: &std::path::Path) -> Option<SystemTime> {
    std::fs::metadata(path).and_then(|meta| meta.modified()).ok()
}

/// Forgets what was read when another process changed the keys since.
fn notice_changes() {
    let Some(stamp) = STAMP.get() else { return };
    let time = stamp_time(stamp);
    let mut seen = SEEN.lock();
    if *seen != time {
        *seen = time;
        CACHE.lock().clear();
        CHANGES.fetch_add(1, Ordering::Relaxed);
    }
}

/// How many times another process changed the keys, as far as this one looked. A service compares
/// it to know when to tell its clients.
pub fn changes_elsewhere() -> u64 {
    notice_changes();
    CHANGES.load(Ordering::Relaxed)
}

fn touch_stamp() {
    let Some(stamp) = STAMP.get() else { return };
    let now = SystemTime::now().duration_since(SystemTime::UNIX_EPOCH).map_or(0, |d| d.as_nanos());
    if std::fs::write(stamp, now.to_string()).is_ok() {
        // This process's own change is in its cache already.
        *SEEN.lock() = stamp_time(stamp);
    }
}

/// The store for this platform. `HER_CREDENTIAL_STORE=file` uses the file everywhere
/// (tests, machines without a keychain); `none` disables saved keys.
pub fn default_store(file: PathBuf) -> Option<Box<dyn CredentialStore>> {
    match std::env::var("HER_CREDENTIAL_STORE").as_deref() {
        Ok("none") => None,
        Ok("file") => Some(Box::new(FileStore { path: file })),
        _ if cfg!(target_os = "macos") => Some(Box::new(KeychainStore)),
        _ => Some(Box::new(FileStore { path: file })),
    }
}

pub fn store_name() -> Option<&'static str> {
    STORE.get().map(|s| s.name())
}

/// The saved key of a provider, if a store is installed and has one.
pub fn get(provider: &str) -> Option<String> {
    let store = STORE.get()?;
    notice_changes();
    if let Some(cached) = CACHE.lock().get(provider) {
        return cached.clone();
    }
    let key = match store.get(provider) {
        Ok(key) => key.filter(|k| !k.trim().is_empty()),
        Err(err) => {
            tracing::warn!(provider, %err, "cannot read the saved key");
            None
        }
    };
    CACHE.lock().insert(provider.to_string(), key.clone());
    key
}

/// Drops the cached value of `provider`, so the next [`get`] reads the store again: another
/// process may have changed it.
pub fn forget(provider: &str) {
    CACHE.lock().remove(provider);
}

pub fn set(provider: &str, key: &str) -> Result<(), String> {
    check_provider(provider)?;
    let key = key.trim();
    if key.is_empty() {
        return Err("the key is empty".into());
    }
    if key.contains(['\n', '\r', '\0']) {
        return Err("the key must be one line".into());
    }
    let store = STORE.get().ok_or("no credential store is available")?;
    store.set(provider, key)?;
    CACHE.lock().insert(provider.to_string(), Some(key.to_string()));
    touch_stamp();
    Ok(())
}

pub fn delete(provider: &str) -> Result<(), String> {
    check_provider(provider)?;
    let store = STORE.get().ok_or("no credential store is available")?;
    store.delete(provider)?;
    CACHE.lock().insert(provider.to_string(), None);
    touch_stamp();
    Ok(())
}

fn check_provider(provider: &str) -> Result<(), String> {
    let ok = !provider.is_empty()
        && provider.len() <= 64
        && provider.chars().all(|c| c.is_ascii_alphanumeric() || matches!(c, '-' | '_' | '.'));
    if ok { Ok(()) } else { Err(format!("invalid provider id '{provider}'")) }
}

/// The macOS login keychain: generic passwords with service `her` and the provider as account.
pub struct KeychainStore;

const SECURITY: &str = "/usr/bin/security";
const SERVICE: &str = "her";
/// `security`'s exit status when the item does not exist.
const NOT_FOUND: i32 = 44;

impl CredentialStore for KeychainStore {
    fn name(&self) -> &'static str {
        "keychain"
    }

    fn get(&self, provider: &str) -> Result<Option<String>, String> {
        let out = std::process::Command::new(SECURITY)
            .args(["find-generic-password", "-s", SERVICE, "-a", provider, "-w"])
            .stdin(std::process::Stdio::null())
            .output()
            .map_err(|e| format!("cannot run {SECURITY}: {e}"))?;
        match out.status.code() {
            Some(0) => Ok(Some(String::from_utf8_lossy(&out.stdout).trim().to_string())),
            Some(NOT_FOUND) => Ok(None),
            _ => Err(String::from_utf8_lossy(&out.stderr).trim().to_string()),
        }
    }

    fn set(&self, provider: &str, key: &str) -> Result<(), String> {
        use std::io::Write as _;
        // `security -i` reads commands from stdin, so the key is not in any process's arguments.
        let quote = |s: &str| format!("\"{}\"", s.replace('\\', "\\\\").replace('"', "\\\""));
        let command = format!(
            "add-generic-password -U -s {SERVICE} -a {provider} -l {} -w {}\n",
            quote(&format!("her: {provider}")),
            quote(key)
        );
        let mut child = std::process::Command::new(SECURITY)
            .arg("-i")
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::piped())
            .spawn()
            .map_err(|e| format!("cannot run {SECURITY}: {e}"))?;
        if let Some(mut stdin) = child.stdin.take() {
            stdin.write_all(command.as_bytes()).map_err(|e| e.to_string())?;
        }
        let out = child.wait_with_output().map_err(|e| e.to_string())?;
        // Interactive mode exits 0 even when a command fails; the error is on stderr.
        let err = String::from_utf8_lossy(&out.stderr).trim().to_string();
        if !out.status.success() || !err.is_empty() {
            return Err(if err.is_empty() { format!("{SECURITY} exited with {}", out.status) } else { err });
        }
        Ok(())
    }

    fn delete(&self, provider: &str) -> Result<(), String> {
        let out = std::process::Command::new(SECURITY)
            .args(["delete-generic-password", "-s", SERVICE, "-a", provider])
            .stdin(std::process::Stdio::null())
            .output()
            .map_err(|e| format!("cannot run {SECURITY}: {e}"))?;
        match out.status.code() {
            Some(0 | NOT_FOUND) => Ok(()),
            _ => Err(String::from_utf8_lossy(&out.stderr).trim().to_string()),
        }
    }
}

/// A TOML file of `provider = "key"` lines, readable only by its owner.
pub struct FileStore {
    pub path: PathBuf,
}

impl FileStore {
    fn read(&self) -> Result<toml::Table, String> {
        match std::fs::read_to_string(&self.path) {
            Ok(text) => text.parse::<toml::Table>().map_err(|e| format!("{}: {e}", self.path.display())),
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(toml::Table::new()),
            Err(err) => Err(format!("{}: {err}", self.path.display())),
        }
    }

    fn write(&self, table: &toml::Table) -> Result<(), String> {
        use std::io::Write as _;
        let text = toml::to_string(table).map_err(|e| e.to_string())?;
        if let Some(dir) = self.path.parent() {
            std::fs::create_dir_all(dir).map_err(|e| e.to_string())?;
        }
        let tmp = self.path.with_extension("tmp");
        let mut options = std::fs::OpenOptions::new();
        options.write(true).create(true).truncate(true);
        #[cfg(unix)]
        std::os::unix::fs::OpenOptionsExt::mode(&mut options, 0o600);
        let mut file = options.open(&tmp).map_err(|e| format!("{}: {e}", tmp.display()))?;
        file.write_all(text.as_bytes()).map_err(|e| e.to_string())?;
        file.sync_all().map_err(|e| e.to_string())?;
        std::fs::rename(&tmp, &self.path).map_err(|e| e.to_string())
    }
}

impl CredentialStore for FileStore {
    fn name(&self) -> &'static str {
        "credentials file"
    }

    fn get(&self, provider: &str) -> Result<Option<String>, String> {
        Ok(self.read()?.get(provider).and_then(|v| v.as_str()).map(str::to_string))
    }

    fn set(&self, provider: &str, key: &str) -> Result<(), String> {
        let mut table = self.read()?;
        table.insert(provider.to_string(), toml::Value::String(key.to_string()));
        self.write(&table)
    }

    fn delete(&self, provider: &str) -> Result<(), String> {
        let mut table = self.read()?;
        if table.remove(provider).is_some() {
            self.write(&table)?;
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_file_store_keeps_keys_private_and_removes_them() {
        let dir = tempfile_dir();
        let store = FileStore {
            path: dir.join("credentials.toml"),
        };
        assert_eq!(store.get("deepseek").unwrap(), None);
        store.set("deepseek", "sk-1").unwrap();
        store.set("anthropic", "sk-2").unwrap();
        assert_eq!(store.get("deepseek").unwrap().as_deref(), Some("sk-1"));
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&store.path).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o600);
        }
        store.delete("deepseek").unwrap();
        assert_eq!(store.get("deepseek").unwrap(), None);
        assert_eq!(store.get("anthropic").unwrap().as_deref(), Some("sk-2"));
        let _ = std::fs::remove_dir_all(dir);
    }

    #[test]
    fn provider_ids_are_checked_before_reaching_a_store() {
        assert!(check_provider("deepseek").is_ok());
        assert!(check_provider("my.vllm-1").is_ok());
        assert!(check_provider("a b").is_err());
        assert!(check_provider("x\" -w y").is_err());
        assert!(check_provider("").is_err());
    }

    fn tempfile_dir() -> PathBuf {
        let dir = std::env::temp_dir().join(format!("her-credentials-{}", rand::random::<u64>()));
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }
}
