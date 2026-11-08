//! Execution environment: how tools reach files and processes. Local today; sandboxes,
//! containers and remote machines are further implementations.

use async_trait::async_trait;
use std::collections::HashMap;
use std::io;
use std::path::Component;
use std::path::Path;
use std::path::PathBuf;
use std::time::Duration;
use tokio_util::sync::CancellationToken;

use crate::files::FileStamp;
use crate::text;
use crate::tool::ProgressSink;
use crate::truncate::Omission;
use crate::truncate::SavedOutput;
use crate::truncate::join_cut;
use crate::truncate::omission_note;

#[async_trait]
pub trait Environment: Send + Sync + 'static {
    fn cwd(&self) -> &Path;
    fn workspace_roots(&self) -> &[PathBuf];
    fn os(&self) -> OsInfo;

    /// Resolves a path given by the model: a `file://` URL becomes its path, `~` is expanded,
    /// a relative path is joined to the working directory, `.` and `..` are removed lexically.
    fn resolve(&self, raw: &str) -> PathBuf {
        if let Some(path) = file_url_path(raw) {
            return normalize_lexically(&path);
        }
        let expanded = if raw == "~" {
            dirs::home_dir().unwrap_or_else(|| PathBuf::from(raw))
        } else if let Some(rest) = raw.strip_prefix("~/") {
            dirs::home_dir().map(|h| h.join(rest)).unwrap_or_else(|| PathBuf::from(raw))
        } else {
            PathBuf::from(raw)
        };
        let joined = if expanded.is_absolute() {
            expanded
        } else {
            self.cwd().join(expanded)
        };
        normalize_lexically(&joined)
    }

    /// The first `max_bytes` of a regular file. Anything else fails at once: a directory with
    /// `IsADirectory`, a named pipe, socket or device with [`NotRegularFile`], since reading
    /// one can block or never end.
    async fn read_file(&self, path: &Path, max_bytes: u64) -> io::Result<FileContent>;

    /// Lines of a text file: up to `window.max_lines` from line `window.offset` on, within
    /// `window.max_bytes`, long lines cut. `\n`, `\r\n` and a lone `\r` end a line and are
    /// not part of it. A UTF-16 byte-order mark makes the file UTF-16; anything else is read
    /// as UTF-8 with invalid bytes replaced and a UTF-8 byte-order mark dropped. Fails with
    /// `InvalidData` when the file is not text: a NUL byte in its first 8 KiB.
    ///
    /// This default loads the first 16 MiB with [`read_file`](Self::read_file): lines past
    /// them cannot be reached. Implementations should stream instead, keeping memory bounded
    /// by the window.
    async fn read_lines(&self, path: &Path, window: &LineWindow) -> io::Result<TextWindow> {
        let file = self.read_file(path, text::MAX_IN_MEMORY).await?;
        text::window_in_memory(&file, window)
    }

    /// Atomic write (temporary file, then rename); creates parent directories. Takes the data
    /// by value: a large file is not copied again on the way to the disk.
    async fn write_file(&self, path: &Path, data: Vec<u8>) -> io::Result<WriteOutcome>;
    /// Refuses, without writing anything, what `write_file` would refuse for reasons that can
    /// be seen beforehand. Used by checks before approval, so nobody approves a doomed write.
    async fn check_write(&self, path: &Path) -> io::Result<()> {
        let _ = path;
        Ok(())
    }
    async fn metadata(&self, path: &Path) -> io::Result<FileMeta>;
    async fn list_dir(&self, path: &Path) -> io::Result<Vec<DirEntry>>;
    async fn exec(&self, req: ExecRequest) -> io::Result<ExecOutput>;
}

pub fn normalize_lexically(path: &Path) -> PathBuf {
    let mut out = PathBuf::new();
    for component in path.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir => {
                out.pop();
            }
            other => out.push(other.as_os_str()),
        }
    }
    out
}

/// The path of a `file:` URL on this machine (no host or `localhost`), percent-decoded, its
/// query and fragment dropped. `None` for anything else.
pub fn file_url_path(raw: &str) -> Option<PathBuf> {
    if !raw.get(..5)?.eq_ignore_ascii_case("file:") {
        return None;
    }
    let rest = &raw[5..];
    let path = match rest.strip_prefix("//") {
        Some(rest) => rest.strip_prefix("localhost").unwrap_or(rest),
        None => rest,
    };
    if !path.starts_with('/') {
        return None;
    }
    let path = &path[..path.find(['?', '#']).unwrap_or(path.len())];
    Some(path_from_bytes(percent_decode(path)))
}

fn percent_decode(text: &str) -> Vec<u8> {
    let bytes = text.as_bytes();
    let hex = |at: usize| bytes.get(at).and_then(|b| (*b as char).to_digit(16));
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%'
            && let (Some(high), Some(low)) = (hex(i + 1), hex(i + 2))
        {
            out.push((high * 16 + low) as u8);
            i += 3;
        } else {
            out.push(bytes[i]);
            i += 1;
        }
    }
    out
}

#[cfg(unix)]
fn path_from_bytes(bytes: Vec<u8>) -> PathBuf {
    use std::os::unix::ffi::OsStringExt;
    PathBuf::from(std::ffi::OsString::from_vec(bytes))
}

#[cfg(not(unix))]
fn path_from_bytes(bytes: Vec<u8>) -> PathBuf {
    PathBuf::from(String::from_utf8_lossy(&bytes).into_owned())
}

/// The error inside the `io::Error` (kind `InvalidInput`) of a read that met something other
/// than a regular file or a directory.
#[derive(Debug, thiserror::Error)]
#[error("not a regular file ({kind})")]
pub struct NotRegularFile {
    /// What it is instead: "named pipe", "socket", "character device"...
    pub kind: &'static str,
}

impl NotRegularFile {
    pub fn error(kind: &'static str) -> io::Error {
        io::Error::new(io::ErrorKind::InvalidInput, NotRegularFile { kind })
    }

    /// The `NotRegularFile` an error stands for, if any.
    pub fn of(err: &io::Error) -> Option<&NotRegularFile> {
        err.get_ref()?.downcast_ref()
    }
}

/// Which lines [`Environment::read_lines`] returns.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LineWindow {
    /// The first line, 1-based.
    pub offset: u64,
    pub max_lines: usize,
    /// Bytes of the lines returned, counting a newline after each. The first line is
    /// returned even when it alone is larger.
    pub max_bytes: usize,
    /// Longer lines are cut and end with a note that gives their length.
    pub max_line_chars: usize,
}

/// A window of a text file.
#[derive(Clone, Debug)]
pub struct TextWindow {
    /// The lines from `offset` on, without line breaks; empty when `offset` is past the end.
    pub lines: Vec<String>,
    /// Lines in the file. Only a lower bound unless `total_exact`: counting stopped early in
    /// a very large file.
    pub total_lines: u64,
    pub total_exact: bool,
    /// The byte budget, not the line count, ended the window.
    pub byte_limited: bool,
    pub encoding: TextEncoding,
    pub total_size: u64,
    /// The version that was read.
    pub stamp: FileStamp,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TextEncoding {
    Utf8,
    Utf16Le,
    Utf16Be,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct OsInfo {
    pub name: String,
    pub arch: String,
    pub shell: String,
}

#[derive(Clone, Debug)]
pub struct FileContent {
    pub bytes: Vec<u8>,
    /// The file is larger than `max_bytes`.
    pub truncated: bool,
    pub total_size: u64,
    /// The version that was read.
    pub stamp: FileStamp,
}

#[derive(Clone, Debug)]
pub struct WriteOutcome {
    pub created: bool,
    pub bytes: u64,
    /// The version that was written.
    pub stamp: FileStamp,
}

#[derive(Clone, Debug)]
pub struct FileMeta {
    pub is_dir: bool,
    pub is_file: bool,
    pub size: u64,
    /// A regular file this process may not write: permissions, flags or a read-only file system.
    pub readonly: bool,
    pub stamp: FileStamp,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DirEntry {
    pub name: String,
    pub is_dir: bool,
}

#[derive(Clone, Debug)]
pub struct CaptureConfig {
    pub head_bytes: usize,
    pub tail_bytes: usize,
    /// Where the full output goes when it does not fit in head + tail. An existing file is
    /// never overwritten; a suffixed name is used instead.
    pub spill_path: Option<PathBuf>,
    /// Size cap of the spill file.
    pub max_saved_bytes: u64,
}

impl Default for CaptureConfig {
    fn default() -> Self {
        Self {
            head_bytes: 10 * 1024,
            tail_bytes: 40 * 1024,
            spill_path: None,
            max_saved_bytes: 64 * 1024 * 1024,
        }
    }
}

#[derive(Clone, Debug)]
pub struct ExecRequest {
    pub command: String,
    pub cwd: PathBuf,
    /// Added on top of the inherited environment.
    pub env: HashMap<String, String>,
    pub timeout: Duration,
    pub capture: CaptureConfig,
    pub progress: Option<ProgressSink>,
    pub cancel: CancellationToken,
}

#[derive(Clone, Debug)]
pub struct ExecOutput {
    /// `None` when the process was killed by a signal.
    pub exit_code: Option<i32>,
    pub signal: Option<i32>,
    pub timed_out: bool,
    pub cancelled: bool,
    pub duration: Duration,
    pub output: CapturedOutput,
}

/// Merged stdout and stderr, ANSI escapes removed.
#[derive(Clone, Debug, Default)]
pub struct CapturedOutput {
    /// The whole output when it fits, otherwise the beginning.
    pub head: String,
    /// The end of the output; empty when nothing was truncated.
    pub tail: String,
    pub total_bytes: u64,
    pub total_lines: u64,
    pub truncated: bool,
    /// What was cut, when `truncated`.
    pub omission: Option<Omission>,
    /// The full output, when it was cut and could be saved.
    pub saved: Option<SavedOutput>,
}

impl CapturedOutput {
    /// Text for the model: head, a note about the omitted part and tail.
    pub fn text(&self) -> String {
        match (&self.omission, self.truncated) {
            (Some(omission), true) => join_cut(&self.head, &omission_note(omission, self.saved.as_ref()), &self.tail),
            _ => self.head.clone(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::local_env::LocalEnvironment;

    #[test]
    fn file_urls_become_paths() {
        let path = |raw: &str| file_url_path(raw).map(|p| p.to_string_lossy().into_owned());
        assert_eq!(path("file:///Users/me/My%20Notes.md").as_deref(), Some("/Users/me/My Notes.md"));
        assert_eq!(path("file://localhost/tmp/a.txt").as_deref(), Some("/tmp/a.txt"));
        assert_eq!(path("FILE:///tmp/a.txt").as_deref(), Some("/tmp/a.txt"));
        assert_eq!(path("file:/tmp/a.txt").as_deref(), Some("/tmp/a.txt"));
        assert_eq!(path("file:///tmp/%E6%97%A5%E6%9C%AC.txt").as_deref(), Some("/tmp/日本.txt"));
        assert_eq!(path("file:///src/main.rs#L10").as_deref(), Some("/src/main.rs"));
        // Escapes that are not two hex digits stay as they are.
        assert_eq!(path("file:///a%2/b%+5%zz%").as_deref(), Some("/a%2/b%+5%zz%"));
        assert_eq!(path("file://server/share/a.txt"), None);
        assert_eq!(path("file:a.txt"), None);
        assert_eq!(path("src/file:///x"), None);
        assert_eq!(path("/tmp/a%20b"), None);
    }

    #[test]
    fn resolve_accepts_file_urls() {
        let env = LocalEnvironment::new(PathBuf::from("/work/project"));
        assert_eq!(env.resolve("file:///tmp/a/../b%20c.txt"), PathBuf::from("/tmp/b c.txt"));
        // Elsewhere a percent sign is just a character.
        assert_eq!(env.resolve("a%20b.txt"), PathBuf::from("/work/project/a%20b.txt"));
    }
}
