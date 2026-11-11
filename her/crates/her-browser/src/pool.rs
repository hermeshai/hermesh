//! One browser process per profile, shared by the sessions of this process (browser-design
//! §6.1). The pool keeps only weak references: the browser closes when its last session lets
//! go, and the next `acquire` waits for that close to finish before starting another.

use parking_lot::Mutex;
use std::collections::HashMap;
use std::collections::HashSet;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::Weak;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use std::time::Duration;
use tokio::io::AsyncRead;
use tokio::io::AsyncWrite;
use tokio::sync::Notify;
use tokio::sync::watch;

use crate::BrowserError;
use crate::cdp::Conn;
use crate::launch::LaunchOptions;
use crate::launch::Launcher;
use crate::launch::ManagedLauncher;
use crate::launch::ProfileLock;
use crate::router::Router;

/// How long `Browser.close` may take before the process is killed.
const CLOSE_GRACE: Duration = Duration::from_secs(5);
/// How long `acquire` waits for the previous browser on a profile to exit.
const CLOSE_WAIT: Duration = Duration::from_secs(15);
/// How long a session waits for the extension to come back when no client can tell the user
/// that it waits: at most an alarm's period, 30 seconds, plus the time to reconnect.
const RECONNECT_WAIT: Duration = Duration::from_secs(35);
/// The file that keeps which extension the user chose, in the pool's root.
const IN_USE_FILE: &str = "extension-in-use";

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Headless {
    /// Without a window for `her exec` and where there is no display.
    #[default]
    Auto,
    On,
    Off,
}

/// Which browser a session gets (§8.3.3).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Backend {
    /// The desktop app's embedded browser while one is connected, else the managed one.
    #[default]
    Auto,
    Embedded,
    Managed,
    /// The user's own Chrome through the her extension (§8.4); only when configured.
    Extension,
}

/// The `[browser]` settings that decide which browser a session gets.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BrowserConfig {
    pub backend: Backend,
    pub executable: Option<PathBuf>,
    pub profile: String,
    pub headless: Headless,
}

impl Default for BrowserConfig {
    fn default() -> Self {
        Self {
            backend: Backend::Auto,
            executable: None,
            profile: "default".into(),
            headless: Headless::Auto,
        }
    }
}

/// Both ends of a CDP byte stream.
pub type Stream = (Box<dyn AsyncRead + Send + Unpin>, Box<dyn AsyncWrite + Send + Unpin>);

/// A desktop app that shows embedded browsers (§8.3.1), or the her extension in the user's
/// Chrome (§8.4). Each `connect` is a new browser: a CDP stream to the host, which acts as
/// the browser end.
pub trait EmbeddedHost: Send + Sync + 'static {
    /// Tells hosts apart: a host that replaced another gets its own browser.
    fn id(&self) -> u64;
    fn connect(&self) -> Result<Stream, BrowserError>;
    /// The tabs are the user's and outlive the agent's use of them (§8.4.7): a session that ends
    /// lets them go instead of closing them, and finds them again by its label.
    fn keeps_tabs(&self) -> bool {
        false
    }
}

/// How waiting for the extension ended.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ExtensionWait {
    Connected,
    /// The user chose not to use their browser this time.
    Declined,
    /// It is not there, and nobody can be told so.
    NoOne,
}

struct ExtensionLink {
    host: Arc<dyn EmbeddedHost>,
    instance: Option<String>,
}

const NO_HOST: &str = "No desktop app is connected to show the embedded browser. Open Hermesh, \
    or set browser.backend = \"managed\" in ~/.her/config.toml to use a separate browser window.";

/// Said in the first result of every session on the user's Chrome.
const EXTENSION_NOTE: &str = "This is the user's own browser: they can see the tabs you open, and their sign-ins \
    are real. Act as carefully as with their accounts.";

const NO_EXTENSION: &str = "The Hermesh extension is not connected, so the user's own browser cannot be used. \
    Ask the user to open their browser with the Hermesh extension, or to connect it again in Hermesh; \
    the built-in browser does not have their sign-ins.";

pub struct BrowserPool {
    /// `~/.her/browser`.
    root: PathBuf,
    /// What `Headless::Auto` means in this process.
    auto_headless: bool,
    launcher: Arc<dyn Launcher>,
    /// The desktop app connected to this process, if any.
    host: Mutex<Option<Arc<dyn EmbeddedHost>>>,
    /// The her extensions connected, oldest first: one per browser and profile.
    extensions: Mutex<Vec<ExtensionLink>>,
    /// The one the user chose; none: the oldest connected.
    chosen: Mutex<Option<String>>,
    /// An extension connected at some point, so it may well come back.
    ever_connected: AtomicBool,
    /// A client follows the browsers' status, and can tell the user what is awaited.
    watched: AtomicBool,
    /// Sessions waiting for the extension, and how many calls of each.
    waiting: Mutex<HashMap<String, usize>>,
    /// Sessions whose wait the user called off.
    declined: Mutex<HashSet<String>>,
    /// Sessions the user took their browser back from.
    revoked: Mutex<HashSet<String>>,
    /// How many tabs each session has in the user's browser, as the extension last said.
    session_tabs: Mutex<HashMap<String, usize>>,
    /// Woken when any of the above changes.
    changed: Notify,
    /// Told when something the status shows changes.
    on_change: Mutex<Option<Arc<dyn Fn() + Send + Sync>>>,
    inner: Arc<PoolInner>,
}

#[derive(Default)]
struct PoolInner {
    browsers: Mutex<HashMap<String, Weak<Browser>>>,
    /// One start at a time per profile.
    starting: Mutex<HashMap<String, Arc<tokio::sync::Mutex<()>>>>,
    /// Set while a profile's previous browser is closing; true once it has.
    closing: Mutex<HashMap<String, watch::Receiver<bool>>>,
}

impl BrowserPool {
    /// `interactive`: a person may watch, so `Headless::Auto` shows a window when there is a
    /// display. `her exec` passes false.
    pub fn new(root: PathBuf, interactive: bool) -> Self {
        Self::with_launcher(root, interactive && has_display(), Arc::new(ManagedLauncher))
    }

    pub fn with_launcher(root: PathBuf, auto_headless_off: bool, launcher: Arc<dyn Launcher>) -> Self {
        let chosen = std::fs::read_to_string(root.join(IN_USE_FILE)).ok().map(|s| s.trim().to_string()).filter(|s| !s.is_empty());
        Self {
            root,
            auto_headless: !auto_headless_off,
            launcher,
            host: Mutex::new(None),
            extensions: Mutex::new(Vec::new()),
            chosen: Mutex::new(chosen),
            ever_connected: AtomicBool::new(false),
            watched: AtomicBool::new(false),
            waiting: Mutex::new(HashMap::new()),
            declined: Mutex::new(HashSet::new()),
            revoked: Mutex::new(HashSet::new()),
            session_tabs: Mutex::new(HashMap::new()),
            changed: Notify::new(),
            on_change: Mutex::new(None),
            inner: Arc::default(),
        }
    }

    /// The desktop app that shows embedded browsers from now on; `None` when it left. A
    /// browser on a host that left is dead (its stream ended), so its sessions start over.
    pub fn set_host(&self, host: Option<Arc<dyn EmbeddedHost>>) {
        *self.host.lock() = host;
    }

    /// Clears the host only if it is still `id`: a newer one may have replaced it.
    pub fn remove_host(&self, id: u64) {
        let mut host = self.host.lock();
        if host.as_ref().is_some_and(|h| h.id() == id) {
            *host = None;
        }
    }

    pub fn has_host(&self) -> bool {
        self.host.lock().is_some()
    }

    /// A her extension connected (§8.4). Several may be, one per browser and profile: the
    /// agent stays with the one it uses, so a later one never moves it to another account
    /// (§8.4.7).
    pub fn add_extension(&self, host: Arc<dyn EmbeddedHost>, instance: Option<String>) {
        self.extensions.lock().push(ExtensionLink { host, instance });
        self.ever_connected.store(true, Ordering::Relaxed);
        self.changed.notify_waiters();
    }

    pub fn remove_extension(&self, id: u64) {
        self.extensions.lock().retain(|link| link.host.id() != id);
    }

    /// The extension the agent uses: the one chosen, else the oldest connected. The one chosen
    /// being away moves nobody to another: the user's accounts differ.
    fn picked(&self) -> Option<Arc<dyn EmbeddedHost>> {
        let links = self.extensions.lock();
        match &*self.chosen.lock() {
            Some(chosen) => links.iter().find(|l| l.instance.as_ref() == Some(chosen)).map(|l| l.host.clone()),
            None => links.first().map(|l| l.host.clone()),
        }
    }

    /// An extension the agent can use is connected.
    pub fn has_extension(&self) -> bool {
        self.picked().is_some()
    }

    /// Which host the agent uses.
    pub fn extension_in_use(&self) -> Option<u64> {
        self.picked().map(|host| host.id())
    }

    /// The extension the user chose, from now on and after a restart.
    pub fn choose_extension(&self, instance: &str) {
        *self.chosen.lock() = Some(instance.to_string());
        let _ = std::fs::create_dir_all(&self.root);
        if let Err(err) = std::fs::write(self.root.join(IN_USE_FILE), instance) {
            tracing::warn!(%err, "cannot keep which extension is in use");
        }
        self.changed.notify_waiters();
    }

    /// `f` hears of what the status shows changing here: who waits.
    pub fn on_change(&self, f: Arc<dyn Fn() + Send + Sync>) {
        *self.on_change.lock() = Some(f);
    }

    fn fire(&self) {
        let f = self.on_change.lock().clone();
        if let Some(f) = f {
            f();
        }
    }

    /// Whether a client follows the status.
    pub fn set_watched(&self, watched: bool) {
        self.watched.store(watched, Ordering::Relaxed);
    }

    /// Waits for an extension the agent can use, for as long as a client can tell the user;
    /// else for as long as one may come back by itself.
    pub async fn wait_for_extension(&self, label: &str) -> ExtensionWait {
        if self.has_extension() {
            return ExtensionWait::Connected;
        }
        let _waiting = Waiting::new(self, label);
        let started = tokio::time::Instant::now();
        loop {
            let changed = self.changed.notified();
            if self.has_extension() {
                return ExtensionWait::Connected;
            }
            // For all of the session's calls that wait; the last one to end clears it.
            if self.declined.lock().contains(label) {
                return ExtensionWait::Declined;
            }
            if !self.watched.load(Ordering::Relaxed)
                && (!self.ever_connected.load(Ordering::Relaxed) || started.elapsed() >= RECONNECT_WAIT)
            {
                return ExtensionWait::NoOne;
            }
            let _ = tokio::time::timeout(Duration::from_millis(200), changed).await;
        }
    }

    /// The sessions waiting for the extension.
    pub fn waiting(&self) -> Vec<String> {
        let mut labels: Vec<String> = self.waiting.lock().keys().cloned().collect();
        labels.sort();
        labels
    }

    /// The user chose not to use their browser this time: the session's calls stop waiting.
    pub fn decline(&self, label: &str) {
        if self.waiting.lock().contains_key(label) {
            self.declined.lock().insert(label.to_string());
            self.changed.notify_waiters();
        }
    }

    /// The user took their browser back from these sessions (they closed Chrome's bar).
    pub fn revoke(&self, labels: &[String]) {
        self.revoked.lock().extend(labels.iter().cloned());
    }

    pub fn allow(&self, label: &str) {
        self.revoked.lock().remove(label);
    }

    pub fn is_revoked(&self, label: &str) -> bool {
        self.revoked.lock().contains(label)
    }

    pub fn revoked(&self) -> Vec<String> {
        let mut labels: Vec<String> = self.revoked.lock().iter().cloned().collect();
        labels.sort();
        labels
    }

    /// How many tabs each session has in the user's browser, as the extension says.
    pub fn set_session_tabs(&self, counts: HashMap<String, usize>) {
        *self.session_tabs.lock() = counts;
    }

    pub fn tabs_of(&self, label: &str) -> usize {
        self.session_tabs.lock().get(label).copied().unwrap_or(0)
    }

    /// The browser for `config`: the embedded one or the managed one for `config.profile`,
    /// started when there is none.
    pub async fn acquire(&self, config: &BrowserConfig) -> Result<Arc<Browser>, BrowserError> {
        let host = match config.backend {
            Backend::Managed => None,
            Backend::Auto | Backend::Embedded => self.host.lock().clone(),
            Backend::Extension => self.picked(),
        };
        if host.is_none() {
            match config.backend {
                Backend::Embedded => return Err(BrowserError::Launch(NO_HOST.into())),
                Backend::Extension => return Err(BrowserError::Launch(NO_EXTENSION.into())),
                Backend::Auto | Backend::Managed => {}
            }
        }
        let extension = config.backend == Backend::Extension;
        let key = match &host {
            Some(host) if extension => format!("extension#{}", host.id()),
            Some(host) => format!("embedded#{}", host.id()),
            None => config.profile.clone(),
        };
        let gate = self.inner.starting.lock().entry(key.clone()).or_default().clone();
        let _started = gate.lock().await;
        let existing = self.inner.browsers.lock().get(&key).and_then(Weak::upgrade);
        if let Some(browser) = existing {
            if !browser.is_dead() {
                return Ok(browser);
            }
            // Crashed or closed by the user: release the profile now, not when the last
            // session lets go of it.
            browser.shut_down();
        }
        let closing = self.inner.closing.lock().get(&key).cloned();
        if let Some(mut done) = closing {
            let _ = tokio::time::timeout(CLOSE_WAIT, done.wait_for(|done| *done)).await;
        }
        let browser = match host {
            Some(host) => self.hosted(host.as_ref(), &key, extension).await?,
            None => self.launch(config).await?,
        };
        self.inner.browsers.lock().insert(key, Arc::downgrade(&browser));
        Ok(browser)
    }

    /// A browser in the desktop app or in the user's Chrome: no process or profile of ours,
    /// only the stream.
    async fn hosted(&self, host: &dyn EmbeddedHost, key: &str, extension: bool) -> Result<Arc<Browser>, BrowserError> {
        let (reader, writer) = host.connect()?;
        let (conn, events) = Conn::start(reader, writer, crate::launch::COMMAND_TIMEOUT);
        let downloads = self.root.join("downloads").join(format!("{}-{}", std::process::id(), key.replace('#', "-")));
        let router = Router::start(conn.clone(), events, downloads.clone(), true, host.keeps_tabs());
        router.init().await?;
        let (done_tx, _) = watch::channel(false);
        Ok(Arc::new(Browser {
            conn,
            router,
            note: extension.then(|| EXTENSION_NOTE.to_string()),
            kind: if extension { BrowserKind::Extension } else { BrowserKind::Embedded },
            close: Mutex::new(Some(Closing {
                child: None,
                lock: None,
                temporary: None,
                downloads,
                done: done_tx,
                pool: Arc::downgrade(&self.inner),
                key: key.to_string(),
            })),
            runtime: tokio::runtime::Handle::current(),
        }))
    }

    /// The browser the built-in browser starts where `browser.executable` names none: one
    /// installed on this machine, else her's own (D-194).
    pub fn program(&self) -> Option<PathBuf> {
        crate::launch::find_executable().or_else(|| crate::launch::installed_executable(&self.root))
    }

    /// Whether the built-in browser shows a window where `browser.headless` is auto.
    pub fn shows_window(&self) -> bool {
        !self.auto_headless
    }

    async fn launch(&self, config: &BrowserConfig) -> Result<Arc<Browser>, BrowserError> {
        if config.profile.is_empty() || config.profile.contains(['/', '\\']) || config.profile.starts_with('.') {
            return Err(BrowserError::Launch(format!("invalid browser.profile \"{}\"", config.profile)));
        }
        let profile = self.root.join("profiles").join(&config.profile);
        let (user_data_dir, lock, temporary, note) = match ProfileLock::acquire(&profile) {
            Ok(lock) => (profile, Some(lock), false, None),
            Err(holder) => {
                static NEXT: AtomicU64 = AtomicU64::new(0);
                let dir = self
                    .root
                    .join(format!("tmp-{}-{}", std::process::id(), NEXT.fetch_add(1, Ordering::Relaxed)));
                let holder = holder.map_or("another process".to_string(), |pid| format!("process {pid}"));
                let note = format!(
                    "The browser profile \"{}\" is in use by {holder}, so this browser uses a temporary profile \
                     without saved logins. Tell the user if a login is missing; a different browser.profile avoids this.",
                    config.profile
                );
                (dir, None, true, Some(note))
            }
        };
        let headless = match config.headless {
            Headless::On => true,
            Headless::Off => false,
            Headless::Auto => self.auto_headless,
        };
        // The configured one, else one installed on the machine, else her's own (D-194).
        let executable = config.executable.clone().or_else(|| {
            crate::launch::find_executable()
                .is_none()
                .then(|| crate::launch::installed_executable(&self.root))
                .flatten()
        });
        let options = LaunchOptions {
            executable,
            user_data_dir: user_data_dir.clone(),
            headless,
        };
        let launched = match self.launcher.launch(&options).await {
            Ok(launched) => launched,
            Err(err) => {
                if temporary {
                    let dir = user_data_dir.clone();
                    let _ = tokio::task::spawn_blocking(move || std::fs::remove_dir_all(dir)).await;
                }
                return Err(err);
            }
        };
        let downloads = self.root.join("downloads").join(format!("{}-{}", std::process::id(), short_key(&user_data_dir)));
        let router = Router::start(launched.conn.clone(), launched.events, downloads.clone(), false, false);
        router.init().await?;
        let (done_tx, _) = watch::channel(false);
        Ok(Arc::new(Browser {
            conn: launched.conn,
            router,
            note,
            kind: BrowserKind::Managed,
            close: Mutex::new(Some(Closing {
                child: launched.child,
                lock,
                temporary: temporary.then_some(user_data_dir),
                downloads,
                done: done_tx,
                pool: Arc::downgrade(&self.inner),
                key: config.profile.clone(),
            })),
            runtime: tokio::runtime::Handle::current(),
        }))
    }
}

/// A session's call waiting for the extension, while it does.
struct Waiting<'a> {
    pool: &'a BrowserPool,
    label: &'a str,
}

impl<'a> Waiting<'a> {
    fn new(pool: &'a BrowserPool, label: &'a str) -> Self {
        *pool.waiting.lock().entry(label.to_string()).or_default() += 1;
        pool.fire();
        Self { pool, label }
    }
}

impl Drop for Waiting<'_> {
    fn drop(&mut self) {
        {
            let mut waiting = self.pool.waiting.lock();
            if let Some(calls) = waiting.get_mut(self.label) {
                *calls -= 1;
                if *calls == 0 {
                    waiting.remove(self.label);
                    self.pool.declined.lock().remove(self.label);
                }
            }
        }
        self.pool.fire();
    }
}

fn short_key(path: &std::path::Path) -> String {
    use std::hash::Hash;
    use std::hash::Hasher;
    let mut hasher = std::collections::hash_map::DefaultHasher::new();
    path.hash(&mut hasher);
    format!("{:x}", hasher.finish() & 0xffff_ffff)
}

/// Whether a window can be shown: a desktop session, not a remote shell.
pub fn has_display() -> bool {
    if std::env::var_os("SSH_CONNECTION").is_some() || std::env::var_os("SSH_TTY").is_some() {
        return false;
    }
    if cfg!(any(target_os = "macos", windows)) {
        return true;
    }
    std::env::var_os("DISPLAY").is_some() || std::env::var_os("WAYLAND_DISPLAY").is_some()
}

/// Where a running browser is.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BrowserKind {
    /// Started by her, with a profile of its own.
    Managed,
    /// In the desktop app's window.
    Embedded,
    /// The user's own Chrome, through the her extension.
    Extension,
}

/// A running browser shared by the sessions that use its profile.
pub struct Browser {
    pub(crate) conn: Arc<Conn>,
    pub(crate) router: Arc<Router>,
    /// Said in the first result of every session: the profile was taken.
    pub(crate) note: Option<String>,
    kind: BrowserKind,
    close: Mutex<Option<Closing>>,
    runtime: tokio::runtime::Handle,
}

/// What closing a browser releases.
struct Closing {
    child: Option<tokio::process::Child>,
    lock: Option<ProfileLock>,
    temporary: Option<PathBuf>,
    downloads: PathBuf,
    done: watch::Sender<bool>,
    pool: Weak<PoolInner>,
    key: String,
}

impl Browser {
    /// The process exited or the connection broke: every holder starts over.
    pub fn is_dead(&self) -> bool {
        self.conn.is_closed()
    }

    pub fn note(&self) -> Option<&str> {
        self.note.as_deref()
    }

    pub fn is_embedded(&self) -> bool {
        self.kind == BrowserKind::Embedded
    }

    pub fn kind(&self) -> BrowserKind {
        self.kind
    }
}

impl Drop for Browser {
    fn drop(&mut self) {
        self.shut_down();
    }
}

impl Browser {
    /// Starts closing the browser in the background; the pool waits for it on this profile.
    fn shut_down(&self) {
        let Some(closing) = self.close.lock().take() else {
            return;
        };
        if let Some(pool) = closing.pool.upgrade() {
            pool.closing.lock().insert(closing.key.clone(), closing.done.subscribe());
        }
        let conn = self.conn.clone();
        self.runtime.spawn(close(conn, closing));
    }
}

async fn close(conn: Arc<Conn>, mut closing: Closing) {
    if !conn.is_closed() {
        let _ = conn
            .send_with_timeout(None, "Browser.close", serde_json::json!({}), Duration::from_secs(2))
            .await;
    }
    if let Some(child) = &mut closing.child
        && tokio::time::timeout(CLOSE_GRACE, child.wait()).await.is_err()
    {
        let _ = child.kill().await;
    }
    drop(closing.lock.take());
    // A profile can be large: removing it must not hold up a runtime thread.
    let dirs: Vec<PathBuf> = closing.temporary.take().into_iter().chain([closing.downloads.clone()]).collect();
    let _ = tokio::task::spawn_blocking(move || {
        for dir in dirs {
            let _ = std::fs::remove_dir_all(dir);
        }
    })
    .await;
    let _ = closing.done.send(true);
    // Nothing more to wait for on this key: forget it, unless a newer browser took it over.
    if let Some(pool) = closing.pool.upgrade() {
        let mut waiting = pool.closing.lock();
        if waiting.get(&closing.key).is_some_and(|rx| rx.same_channel(&closing.done.subscribe())) {
            waiting.remove(&closing.key);
        }
        drop(waiting);
        let mut browsers = pool.browsers.lock();
        if browsers.get(&closing.key).is_some_and(|b| b.strong_count() == 0) {
            browsers.remove(&closing.key);
        }
        drop(browsers);
        let mut starting = pool.starting.lock();
        if starting.get(&closing.key).is_some_and(|gate| Arc::strong_count(gate) == 1) {
            starting.remove(&closing.key);
        }
    }
}
