//! A local server: a child process speaking newline-delimited JSON on stdin and stdout.
//!
//! The process gets its own process group, and signals always go to the whole group:
//! wrappers such as `npx` start the real server as a grandchild.

use async_trait::async_trait;
use her_rpc::Frame;
use her_rpc::FrameReader;
use parking_lot::Mutex;
use serde_json::Value;
use std::collections::BTreeMap;
use std::collections::VecDeque;
use std::path::PathBuf;
use std::process::Stdio;
use std::sync::Arc;
use std::time::Duration;
use tokio::io::AsyncBufReadExt;
use tokio::io::AsyncWriteExt;
use tokio::process::Command;
use tokio::sync::mpsc;
use tokio::sync::watch;

use super::Inbound;
use super::Transport;
use crate::McpError;

/// Longest line read from the server.
const MAX_LINE: usize = 16 << 20;
/// The end of stderr kept for error messages.
const STDERR_TAIL: usize = 8 << 10;

pub(crate) struct StdioSpec {
    pub(crate) server: String,
    pub(crate) program: String,
    pub(crate) args: Vec<String>,
    pub(crate) env: BTreeMap<String, String>,
    pub(crate) cwd: PathBuf,
}

pub(crate) struct StdioTransport {
    server: String,
    /// Dropping the sender closes the child's stdin.
    out: Mutex<Option<mpsc::UnboundedSender<Vec<u8>>>>,
    pid: Option<u32>,
    exited: watch::Receiver<bool>,
    stderr: Arc<Mutex<VecDeque<u8>>>,
}

/// Why spawning failed, and whether retrying could help.
pub(crate) struct SpawnError {
    pub(crate) message: String,
    pub(crate) permanent: bool,
}

pub(crate) fn spawn(spec: StdioSpec, inbound: mpsc::UnboundedSender<Inbound>) -> Result<Arc<StdioTransport>, SpawnError> {
    let mut command = Command::new(&spec.program);
    command
        .args(&spec.args)
        .env_clear()
        .envs(&spec.env)
        .current_dir(&spec.cwd)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    #[cfg(unix)]
    command.process_group(0);
    let mut child = command.spawn().map_err(|err| {
        let permanent = matches!(err.kind(), std::io::ErrorKind::NotFound | std::io::ErrorKind::PermissionDenied);
        let message = match err.kind() {
            std::io::ErrorKind::NotFound if !spec.cwd.is_dir() => {
                format!("working directory {} does not exist", spec.cwd.display())
            }
            std::io::ErrorKind::NotFound => format!("command not found: {}", spec.program),
            _ => format!("cannot start {}: {err}", spec.program),
        };
        SpawnError { message, permanent }
    })?;
    let pid = child.id();
    let stdin = child.stdin.take();
    let stdout = child.stdout.take();
    let stderr_pipe = child.stderr.take();
    let (out_tx, mut out_rx) = mpsc::unbounded_channel::<Vec<u8>>();
    if let Some(mut stdin) = stdin {
        tokio::spawn(async move {
            while let Some(mut line) = out_rx.recv().await {
                line.push(b'\n');
                if stdin.write_all(&line).await.is_err() || stdin.flush().await.is_err() {
                    break;
                }
            }
            // Dropping stdin here is the polite way to ask the server to exit.
        });
    }
    let stderr = Arc::new(Mutex::new(VecDeque::new()));
    if let Some(pipe) = stderr_pipe {
        let tail = stderr.clone();
        let server = spec.server.clone();
        // Always drained: a full pipe would block the server on its next write.
        tokio::spawn(async move {
            let mut lines = tokio::io::BufReader::new(pipe).lines();
            while let Ok(Some(line)) = lines.next_line().await {
                tracing::info!(target: "her_mcp::stderr", server = %server, "{line}");
                let mut tail = tail.lock();
                tail.extend(line.as_bytes());
                tail.push_back(b'\n');
                while tail.len() > STDERR_TAIL {
                    tail.pop_front();
                }
            }
        });
    }
    let (read_done_tx, read_done_rx) = tokio::sync::oneshot::channel::<()>();
    if let Some(stdout) = stdout {
        let inbound = inbound.clone();
        let server = spec.server.clone();
        tokio::spawn(async move {
            let mut frames = FrameReader::new(stdout, MAX_LINE);
            loop {
                match frames.next().await {
                    Ok(Some(Frame::Line(line))) => match serde_json::from_slice::<Value>(&line) {
                        Ok(value) => {
                            let _ = inbound.send(Inbound::Message(value));
                        }
                        Err(_) => tracing::debug!(server = %server, "ignored a stdout line that is not JSON"),
                    },
                    Ok(Some(Frame::Oversize { .. })) => tracing::warn!(server = %server, "ignored a stdout line over 16 MB"),
                    Ok(None) | Err(_) => break,
                }
            }
            let _ = read_done_tx.send(());
        });
    } else {
        let _ = read_done_tx.send(());
    }
    let (exited_tx, exited_rx) = watch::channel(false);
    let tail = stderr.clone();
    tokio::spawn(async move {
        let status = child.wait().await;
        // Messages written before the exit are delivered first.
        let _ = tokio::time::timeout(Duration::from_secs(1), read_done_rx).await;
        let _ = exited_tx.send(true);
        let status = match status {
            Ok(status) => describe_exit(status),
            Err(err) => format!("could not wait for the process: {err}"),
        };
        let _ = inbound.send(Inbound::Closed(with_stderr(status, &tail)));
    });
    Ok(Arc::new(StdioTransport {
        server: spec.server,
        out: Mutex::new(Some(out_tx)),
        pid,
        exited: exited_rx,
        stderr,
    }))
}

fn describe_exit(status: std::process::ExitStatus) -> String {
    #[cfg(unix)]
    {
        use std::os::unix::process::ExitStatusExt;
        if let Some(signal) = status.signal() {
            return format!("the server process was killed by signal {signal}");
        }
    }
    match status.code() {
        Some(code) => format!("the server process exited with status {code}"),
        None => "the server process exited".to_string(),
    }
}

/// `message`, plus the last lines of stderr when there are any.
fn with_stderr(message: String, tail: &Mutex<VecDeque<u8>>) -> String {
    let bytes: Vec<u8> = tail.lock().iter().copied().collect();
    let text = String::from_utf8_lossy(&bytes);
    let lines: Vec<&str> = text.lines().filter(|l| !l.trim().is_empty()).collect();
    if lines.is_empty() {
        return message;
    }
    let last: Vec<&str> = lines.iter().rev().take(5).rev().copied().collect();
    format!("{message}; stderr: {}", last.join(" | "))
}

impl StdioTransport {
    fn signal_group(&self, signal: i32) {
        #[cfg(unix)]
        if let Some(pid) = self.pid {
            // SAFETY: plain syscall; the group was created for this child (process_group(0)).
            unsafe {
                libc::killpg(pid as libc::pid_t, signal);
            }
        }
        #[cfg(not(unix))]
        let _ = signal;
    }

    async fn wait_exit(&self, within: Duration) -> bool {
        let mut exited = self.exited.clone();
        tokio::time::timeout(within, exited.wait_for(|done| *done)).await.is_ok()
    }

    /// The end of stderr, for errors that happen before the process exits.
    pub(crate) fn stderr_tail(&self, message: String) -> String {
        with_stderr(message, &self.stderr)
    }
}

#[async_trait]
impl Transport for StdioTransport {
    async fn send(&self, message: Value) -> Result<(), McpError> {
        let bytes = serde_json::to_vec(&message).map_err(|e| McpError::Protocol(e.to_string()))?;
        let out = self.out.lock().clone();
        match out {
            Some(out) if out.send(bytes).is_ok() => Ok(()),
            _ => Err(McpError::Closed("the server's stdin is closed".into())),
        }
    }

    async fn close(&self) {
        self.out.lock().take();
        if self.wait_exit(Duration::from_secs(2)).await {
            // The server exited; anything it started in its group goes too.
            self.signal_group(libc_sigterm());
            return;
        }
        self.signal_group(libc_sigterm());
        if !self.wait_exit(Duration::from_secs(1)).await {
            self.signal_group(libc_sigkill());
            let _ = self.wait_exit(Duration::from_secs(1)).await;
        }
        tracing::debug!(server = %self.server, "stopped the MCP server process");
    }
}

impl Drop for StdioTransport {
    fn drop(&mut self) {
        // her is going away without a clean shutdown: take the whole group down.
        if !*self.exited.borrow() {
            self.signal_group(libc_sigkill());
        }
    }
}

#[cfg(unix)]
fn libc_sigterm() -> i32 {
    libc::SIGTERM
}
#[cfg(unix)]
fn libc_sigkill() -> i32 {
    libc::SIGKILL
}
#[cfg(not(unix))]
fn libc_sigterm() -> i32 {
    15
}
#[cfg(not(unix))]
fn libc_sigkill() -> i32 {
    9
}
