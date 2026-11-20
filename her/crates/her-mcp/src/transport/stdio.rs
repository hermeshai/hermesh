//! A local server: a child process speaking newline-delimited JSON on stdin and stdout.
//!
//! The process leads a tree of its own (`her_tools::proctree`), stopped as a whole: wrappers
//! such as `npx` start the real server as a grandchild.

use async_trait::async_trait;
use her_rpc::Frame;
use her_rpc::FrameReader;
use her_tools::proctree::ProcessTree;
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
/// Longest non-JSON stdout snippet kept for diagnostics.
const STDOUT_NOISE_LIMIT: usize = 120;

pub(crate) struct StdioSpec {
    pub(crate) server: String,
    pub(crate) program: String,
    pub(crate) args: Vec<String>,
    pub(crate) env: BTreeMap<String, String>,
    pub(crate) cwd: PathBuf,
    /// Secret values expanded from credentials, redacted from logs and error diagnostics.
    pub(crate) secrets: Vec<String>,
}

pub(crate) struct StdioTransport {
    server: String,
    /// Dropping the sender closes the child's stdin.
    out: Mutex<Option<mpsc::UnboundedSender<Vec<u8>>>>,
    tree: ProcessTree,
    exited: watch::Receiver<bool>,
    stderr: Arc<Mutex<VecDeque<u8>>>,
    stdout_noise: Arc<Mutex<Option<String>>>,
    secrets: Arc<Vec<String>>,
}

/// Why spawning failed, and whether retrying could help.
pub(crate) struct SpawnError {
    pub(crate) message: String,
    pub(crate) permanent: bool,
}

#[cfg(any(windows, test))]
fn is_windows_batch(path: &str) -> bool {
    let lower = path.to_ascii_lowercase();
    lower.ends_with(".cmd") || lower.ends_with(".bat")
}

fn validate_program(program: &str) -> Result<(), SpawnError> {
    let trimmed = program.trim();
    if trimmed.is_empty() || trimmed != program {
        return Err(SpawnError {
            message: "command must be an executable name or path without leading/trailing whitespace".into(),
            permanent: true,
        });
    }
    if program.contains("${") {
        return Err(SpawnError {
            message: "command cannot contain variable or credential references".into(),
            permanent: true,
        });
    }
    if program.chars().any(|c| c == '\0' || c == '\r' || c == '\n') {
        return Err(SpawnError {
            message: "command contains invalid control characters".into(),
            permanent: true,
        });
    }
    Ok(())
}

/// Replaces expanded secret values in logs and diagnostics so secrets never leak.
fn redact(text: &str, secrets: &[String]) -> String {
    let mut out = text.to_string();
    for secret in secrets {
        if !secret.is_empty() && out.contains(secret.as_str()) {
            out = out.replace(secret.as_str(), "[REDACTED]");
        }
    }
    out
}

pub(crate) fn spawn(spec: StdioSpec, inbound: mpsc::UnboundedSender<Inbound>) -> Result<Arc<StdioTransport>, SpawnError> {
    validate_program(&spec.program)?;
    // Windows' names have no case: its `PATH` is `Path`.
    let path = spec.env.iter().find(|(name, _)| name.eq_ignore_ascii_case("PATH")).map(|(_, value)| value.as_str());
    let resolved = her_tools::proctree::program(&spec.program, path);
    #[cfg(windows)]
    if is_windows_batch(&resolved.to_string_lossy()) {
        return Err(SpawnError {
            message: format!(
                "refused to run Windows batch script {}: stdio MCP servers must launch an executable directly without cmd.exe",
                spec.program
            ),
            permanent: true,
        });
    }
    let mut command = Command::new(resolved);
    command
        .args(&spec.args)
        .env_clear()
        .envs(&spec.env)
        .current_dir(&spec.cwd)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    let (mut child, tree) = ProcessTree::spawn(&mut command).map_err(|err| {
        let permanent = matches!(err.kind(), std::io::ErrorKind::NotFound | std::io::ErrorKind::PermissionDenied);
        let message = match err.kind() {
            std::io::ErrorKind::NotFound if !spec.cwd.is_dir() => {
                format!("working directory {} does not exist", spec.cwd.display())
            }
            std::io::ErrorKind::NotFound => {
                format!("command not found on this machine: {} (check PATH or use an absolute path)", spec.program)
            }
            std::io::ErrorKind::PermissionDenied => {
                format!("permission denied starting {} (check execute permissions)", spec.program)
            }
            _ => format!("cannot start {}: {err}", spec.program),
        };
        SpawnError { message, permanent }
    })?;
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
    let secrets = Arc::new(spec.secrets);
    let stderr = Arc::new(Mutex::new(VecDeque::new()));
    if let Some(pipe) = stderr_pipe {
        let tail = stderr.clone();
        let server = spec.server.clone();
        let secrets = secrets.clone();
        // Always drained: a full pipe would block the server on its next write.
        tokio::spawn(async move {
            let mut lines = tokio::io::BufReader::new(pipe).lines();
            while let Ok(Some(line)) = lines.next_line().await {
                let clean = redact(&line, &secrets);
                tracing::info!(target: "her_mcp::stderr", server = %server, "{clean}");
                let mut tail = tail.lock();
                tail.extend(clean.as_bytes());
                tail.push_back(b'\n');
                while tail.len() > STDERR_TAIL {
                    tail.pop_front();
                }
            }
        });
    }
    let stdout_noise: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));
    let (read_done_tx, read_done_rx) = tokio::sync::oneshot::channel::<()>();
    if let Some(stdout) = stdout {
        let inbound = inbound.clone();
        let server = spec.server.clone();
        let noise = stdout_noise.clone();
        let secrets = secrets.clone();
        tokio::spawn(async move {
            let mut frames = FrameReader::new(stdout, MAX_LINE);
            loop {
                match frames.next().await {
                    Ok(Some(Frame::Line(line))) => match serde_json::from_slice::<Value>(&line) {
                        Ok(value) => {
                            let _ = inbound.send(Inbound::Message(value));
                        }
                        Err(_) => {
                            tracing::debug!(server = %server, "ignored a stdout line that is not JSON");
                            let text = String::from_utf8_lossy(&line);
                            if !text.trim().is_empty() {
                                let mut slot = noise.lock();
                                if slot.is_none() {
                                    let clean = redact(&text, &secrets);
                                    let sample: String = clean.trim().chars().take(STDOUT_NOISE_LIMIT).collect();
                                    *slot = Some(sample);
                                }
                            }
                        }
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
    let noise = stdout_noise.clone();
    tokio::spawn(async move {
        let status = child.wait().await;
        // Messages written before the exit are delivered first.
        let _ = tokio::time::timeout(Duration::from_secs(1), read_done_rx).await;
        let _ = exited_tx.send(true);
        let status = match status {
            Ok(status) => describe_exit(status),
            Err(err) => format!("could not wait for the process: {err}"),
        };
        let _ = inbound.send(Inbound::Closed(with_diagnostics(status, &tail, &noise)));
    });
    Ok(Arc::new(StdioTransport {
        server: spec.server,
        out: Mutex::new(Some(out_tx)),
        tree,
        exited: exited_rx,
        stderr,
        stdout_noise,
        secrets,
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

/// `message`, plus the last lines of stderr and any non-JSON stdout pollution hint.
fn with_diagnostics(message: String, tail: &Mutex<VecDeque<u8>>, stdout_noise: &Mutex<Option<String>>) -> String {
    let mut out = message;
    let bytes: Vec<u8> = tail.lock().iter().copied().collect();
    let text = String::from_utf8_lossy(&bytes);
    let lines: Vec<&str> = text.lines().filter(|l| !l.trim().is_empty()).collect();
    if !lines.is_empty() {
        let last: Vec<&str> = lines.iter().rev().take(5).rev().copied().collect();
        out = format!("{out}; stderr: {}", last.join(" | "));
    }
    if let Some(sample) = stdout_noise.lock().as_ref() {
        out = format!("{out}; stdout emitted non-JSON output ({sample:?}): MCP stdio servers must write logs to stderr");
    }
    out
}

impl StdioTransport {
    async fn wait_exit(&self, within: Duration) -> bool {
        let mut exited = self.exited.clone();
        tokio::time::timeout(within, exited.wait_for(|done| *done)).await.is_ok()
    }

    /// Diagnostics for errors that happen before the process exits.
    pub(crate) fn diagnostic(&self, message: String) -> String {
        with_diagnostics(message, &self.stderr, &self.stdout_noise)
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

    fn redact(&self, message: &str) -> String {
        redact(message, &self.secrets)
    }

    async fn close(&self) {
        self.out.lock().take();
        if self.wait_exit(Duration::from_secs(2)).await {
            // The server exited; anything it started goes too.
            self.tree.terminate();
            return;
        }
        self.tree.terminate();
        if !self.wait_exit(Duration::from_secs(1)).await {
            self.tree.kill();
            let _ = self.wait_exit(Duration::from_secs(1)).await;
        }
        tracing::debug!(server = %self.server, "stopped the MCP server process");
    }
}

impl Drop for StdioTransport {
    fn drop(&mut self) {
        // her is going away without a clean shutdown: take the whole tree down.
        if !*self.exited.borrow() {
            self.tree.kill();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn program_validation_rejects_empty_whitespace_control_and_interpolation() {
        assert!(validate_program("node").is_ok());
        assert!(validate_program("/opt/homebrew/bin/uvx").is_ok());
        assert!(validate_program("").is_err());
        assert!(validate_program("  node").is_err());
        assert!(validate_program("node\n").is_err());
        assert!(validate_program("${HOME}/bin/mcp").is_err());
        assert!(validate_program("${credential:mcp.x.CMD}").is_err());
        assert!(is_windows_batch(r"C:\Program Files\nodejs\npx.CMD"));
        assert!(is_windows_batch("run.bat"));
        assert!(!is_windows_batch("/usr/bin/node"));
    }

    #[test]
    fn redacts_expanded_secrets_from_diagnostics() {
        let secrets = vec!["ghp_super_secret_token_123".to_string(), "tok".to_string()];
        let out = redact("auth failed for ghp_super_secret_token_123 with tok", &secrets);
        assert!(!out.contains("ghp_super_secret_token_123"), "{out}");
        assert!(out.contains("[REDACTED]"), "{out}");
        assert!(!out.contains("tok"), "{out}");
    }
}
