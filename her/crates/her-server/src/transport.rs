//! Transports: one connection on stdio, or a local service on a Unix socket.

use std::fs::File;
use std::io;
use std::os::fd::AsRawFd;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::AtomicUsize;
use std::sync::atomic::Ordering;
use std::time::Duration;
use tokio::net::UnixListener;

use crate::conn::Protocol;
use crate::conn::run_connection;
use crate::server::Server;

/// Serves one client on stdin and stdout. When stdin closes, every session is closed.
pub async fn serve_stdio(server: Server, protocol: Arc<dyn Protocol>) {
    let stopped = server.shutdown_token();
    tokio::select! {
        _ = run_connection(server.clone(), tokio::io::stdin(), tokio::io::stdout(), protocol) => {}
        _ = stopped.cancelled() => {}
    }
    server.close_all().await;
}

/// The default socket: `$HER_HOME/run/her.sock`.
pub fn default_socket_path(server: &Server) -> PathBuf {
    server.home().root().join("run").join("her.sock")
}

/// Socket paths are limited to the size of `sockaddr_un.sun_path`, terminator included.
const MAX_SOCKET_PATH: usize = 103;

pub struct UnixService {
    pub path: PathBuf,
    /// Exit after this long without connections or running tasks.
    pub idle_exit: Option<Duration>,
}

/// Listens on a private Unix socket until a client sends `server/shutdown`, the server is
/// asked to stop, or it stays idle for `idle_exit`. Only the owner's processes may connect.
pub async fn serve_unix(
    server: Server,
    service: UnixService,
    protocol: impl Fn() -> Arc<dyn Protocol> + Send + Sync + 'static,
) -> io::Result<()> {
    let path = service.path;
    if path.as_os_str().len() > MAX_SOCKET_PATH {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!(
                "socket path {} is longer than {MAX_SOCKET_PATH} bytes; pass a shorter one with --listen unix://PATH",
                path.display()
            ),
        ));
    }
    let dir = path.parent().unwrap_or(Path::new("."));
    std::fs::create_dir_all(dir)?;
    std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700))?;
    // One service per socket: the lock is held for the life of the process. The file is
    // opened without truncating it: until the lock is ours, it holds the running service's
    // record.
    let lock_path = path.with_extension("lock");
    let mut lock = std::fs::OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .open(&lock_path)?;
    match lock.try_lock() {
        Ok(()) => {}
        Err(std::fs::TryLockError::WouldBlock) => {
            return Err(io::Error::new(
                io::ErrorKind::AddrInUse,
                format!("another her service is already listening on {}", path.display()),
            ));
        }
        Err(std::fs::TryLockError::Error(err)) => return Err(err),
    }
    write_record(&mut lock, &path)?;
    // Holding the lock, any existing socket file is stale.
    match std::fs::remove_file(&path) {
        Ok(()) => {}
        Err(err) if err.kind() == io::ErrorKind::NotFound => {}
        Err(err) => return Err(err),
    }
    let listener = UnixListener::bind(&path)?;
    // The longest queue of connections waiting to be accepted that the system allows: 4096
    // by default on Linux, and on macOS 128 unless `kern.ipc.somaxconn` is raised. Clients
    // retry while it is full (protocol-design §8.2).
    // SAFETY: listening again on the socket just bound only changes the queue length.
    if unsafe { libc::listen(listener.as_raw_fd(), -1) } != 0 {
        tracing::debug!(err = %io::Error::last_os_error(), "kept the default accept queue");
    }
    std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600))?;
    tracing::info!(socket = %path.display(), "her service listening");
    // The record goes to disk once clients can connect: the sync takes about 4 ms on macOS,
    // and readers on this machine see the record without it.
    if let Ok(file) = lock.try_clone() {
        tokio::task::spawn_blocking(move || file.sync_data());
    }

    let protocol = Arc::new(protocol);
    let active = Arc::new(AtomicUsize::new(0));
    let stopped = server.shutdown_token();
    let uid = unsafe { libc::geteuid() };
    let mut idle_since = tokio::time::Instant::now();
    let mut tick = tokio::time::interval(Duration::from_millis(250));
    loop {
        tokio::select! {
            accepted = listener.accept() => {
                let (stream, _) = match accepted {
                    Ok(pair) => pair,
                    Err(err) => {
                        tracing::warn!(%err, "accept failed");
                        tokio::time::sleep(Duration::from_millis(50)).await;
                        continue;
                    }
                };
                let peer_pid = match stream.peer_cred() {
                    Ok(cred) if cred.uid() == uid => cred.pid().and_then(|pid| u32::try_from(pid).ok()),
                    Ok(cred) => {
                        tracing::warn!(uid = cred.uid(), "refused a connection from another user");
                        continue;
                    }
                    // The peer is already gone (a readiness probe, say).
                    Err(err) => {
                        tracing::debug!(%err, "dropped a connection whose peer could not be identified");
                        continue;
                    }
                };
                let server = server.clone();
                let protocol = (protocol)();
                let active = active.clone();
                active.fetch_add(1, Ordering::Relaxed);
                tokio::spawn(async move {
                    let (read, write) = stream.into_split();
                    crate::conn::run_peer_connection(server, read, write, protocol, peer_pid).await;
                    active.fetch_sub(1, Ordering::Relaxed);
                });
            }
            _ = stopped.cancelled() => break,
            _ = tick.tick(), if service.idle_exit.is_some() => {
                // An extension's host only waits for the service: it keeps nothing running.
                // Nor does the time of an enabled routine come if the service is gone.
                if active.load(Ordering::Relaxed) > server.extension_hosts() || server.busy() || server.has_active_routines() {
                    idle_since = tokio::time::Instant::now();
                } else if service.idle_exit.is_some_and(|limit| idle_since.elapsed() >= limit) {
                    tracing::info!("idle; stopping the service");
                    break;
                }
            }
        }
    }
    drop(listener);
    let _ = std::fs::remove_file(&path);
    server.close_all().await;
    drop(lock);
    let _ = std::fs::remove_file(&lock_path);
    Ok(())
}

/// What a client can learn about the running service without talking to it: written into the
/// lock file once the lock is held, one line of JSON. Clients that cannot complete a handshake
/// (an incompatible protocol, a hung process) find the process here.
#[derive(Debug, serde::Serialize, serde::Deserialize, PartialEq)]
pub struct ServiceRecord {
    pub pid: u32,
    pub version: String,
    pub protocol_version: u32,
    pub socket: PathBuf,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exe: Option<PathBuf>,
    /// RFC 3339.
    pub started_at: String,
}

fn write_record(lock: &mut File, socket: &Path) -> io::Result<()> {
    use std::io::Seek as _;
    use std::io::Write as _;
    let record = ServiceRecord {
        pid: std::process::id(),
        version: env!("CARGO_PKG_VERSION").to_string(),
        protocol_version: her_protocol::PROTOCOL_VERSION,
        socket: socket.to_path_buf(),
        exe: std::env::current_exe().ok(),
        started_at: chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Secs, true),
    };
    let mut line = serde_json::to_vec(&record).map_err(io::Error::other)?;
    line.push(b'\n');
    lock.set_len(0)?;
    lock.seek(io::SeekFrom::Start(0))?;
    lock.write_all(&line)
}
