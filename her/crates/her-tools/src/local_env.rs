//! The local execution environment: the user's own file system and processes.

use async_trait::async_trait;
use parking_lot::Mutex;
use std::collections::HashMap;
use std::collections::VecDeque;
use std::ffi::OsStr;
use std::io;
use std::io::Read;
use std::io::Write;
use std::path::Path;
use std::path::PathBuf;
use std::process::Stdio;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use tokio::io::AsyncRead;
use tokio::io::AsyncReadExt;
use tokio::sync::mpsc;

use crate::env::CaptureConfig;
use crate::env::CapturedOutput;
use crate::truncate::SavedOutput;
use crate::ansi;
use crate::ansi::AnsiStripper;
use crate::truncate::cap_text;
use crate::truncate::create_unique;
use crate::truncate::trim_to_lines;
use crate::env::DirEntry;
use crate::env::Environment;
use crate::env::ExecOutput;
use crate::env::ExecRequest;
use crate::env::FileContent;
use crate::env::FileMeta;
use crate::env::LineWindow;
use crate::env::NotRegularFile;
use crate::env::OsInfo;
use crate::env::TextWindow;
use crate::env::WriteOutcome;
use crate::files::FileStamp;
use crate::text;
use crate::text::LineIndex;

/// Output kept in memory before it is moved to the spill file.
const MEM_LIMIT: usize = 1024 * 1024;
/// How much of a file a read of lines scans only to count: the lines after the window and the
/// characters of a cut line. Past it the counts are lower bounds.
const COUNT_LIMIT: u64 = 256 * 1024 * 1024;
/// Line indexes kept, of the large files read last.
const LINE_INDEXES: usize = 8;

/// Line indexes by path, most recent first.
type LineIndexes = Mutex<Vec<(PathBuf, Arc<LineIndex>)>>;
/// After the process exits, how long to wait for children that still hold the pipes.
const EXIT_GRACE: Duration = Duration::from_millis(500);
/// Between SIGTERM and SIGKILL.
const KILL_GRACE: Duration = Duration::from_secs(2);

pub struct LocalEnvironment {
    cwd: PathBuf,
    roots: Vec<PathBuf>,
    shell: String,
    scrub_env: Vec<String>,
    extra_env: HashMap<String, String>,
    /// Line indexes of the large files read last, most recent first, so that paging through one
    /// does not scan it whole for every page.
    line_indexes: Arc<LineIndexes>,
}

impl LocalEnvironment {
    pub fn new(cwd: PathBuf) -> Self {
        let mut extra_env = HashMap::new();
        // Keep commands from blocking on pagers or credential prompts.
        extra_env.insert("PAGER".to_string(), "cat".to_string());
        extra_env.insert("GIT_PAGER".to_string(), "cat".to_string());
        extra_env.insert("GIT_TERMINAL_PROMPT".to_string(), "0".to_string());
        Self {
            roots: vec![cwd.clone()],
            cwd,
            shell: default_shell(),
            scrub_env: Vec::new(),
            extra_env,
            line_indexes: Arc::default(),
        }
    }

    pub fn with_roots(mut self, roots: Vec<PathBuf>) -> Self {
        if !roots.is_empty() {
            self.roots = roots;
        }
        self
    }

    pub fn with_shell(mut self, shell: impl Into<String>) -> Self {
        self.shell = shell.into();
        self
    }

    /// Variables removed from the environment of child processes (provider keys).
    pub fn with_scrubbed_env(mut self, vars: Vec<String>) -> Self {
        self.scrub_env = vars;
        self
    }

    pub fn with_env(mut self, key: impl Into<String>, value: impl Into<String>) -> Self {
        self.extra_env.insert(key.into(), value.into());
        self
    }
}

fn default_shell() -> String {
    if Path::new("/bin/bash").exists() {
        "/bin/bash".to_string()
    } else {
        "/bin/sh".to_string()
    }
}

#[async_trait]
impl Environment for LocalEnvironment {
    fn cwd(&self) -> &Path {
        &self.cwd
    }

    fn workspace_roots(&self) -> &[PathBuf] {
        &self.roots
    }

    fn os(&self) -> OsInfo {
        OsInfo {
            name: std::env::consts::OS.to_string(),
            arch: std::env::consts::ARCH.to_string(),
            shell: self.shell.clone(),
        }
    }

    async fn read_file(&self, path: &Path, max_bytes: u64) -> io::Result<FileContent> {
        let path = path.to_path_buf();
        tokio::task::spawn_blocking(move || {
            let (file, meta) = open_regular(&path)?;
            let mut bytes = Vec::with_capacity(meta.len().min(max_bytes) as usize);
            file.take(max_bytes).read_to_end(&mut bytes)?;
            Ok(FileContent {
                truncated: meta.len() > max_bytes,
                total_size: meta.len(),
                stamp: FileStamp::of(&meta),
                bytes,
            })
        })
        .await
        .map_err(io::Error::other)?
    }

    async fn read_lines(&self, path: &Path, window: &LineWindow) -> io::Result<TextWindow> {
        let path = path.to_path_buf();
        let window = window.clone();
        let indexes = self.line_indexes.clone();
        tokio::task::spawn_blocking(move || {
            let (mut file, meta) = open_regular(&path)?;
            let stamp = FileStamp::of(&meta);
            let index = {
                let mut indexes = indexes.lock();
                let at = indexes.iter().position(|(p, index)| *p == path && index.stamp() == stamp);
                at.map(|at| {
                    let entry = indexes.remove(at);
                    let index = entry.1.clone();
                    indexes.insert(0, entry);
                    index
                })
            };
            let (text, built) =
                text::read_window_indexed(&mut file, meta.len(), stamp, &window, COUNT_LIMIT, index.as_deref())?;
            if let Some(built) = built {
                let mut indexes = indexes.lock();
                indexes.retain(|(p, _)| *p != path);
                indexes.insert(0, (path, Arc::new(built)));
                indexes.truncate(LINE_INDEXES);
            }
            Ok(text)
        })
        .await
        .map_err(io::Error::other)?
    }

    async fn write_file(&self, path: &Path, data: Vec<u8>) -> io::Result<WriteOutcome> {
        let path = path.to_path_buf();
        tokio::task::spawn_blocking(move || atomic_write(&path, &data))
            .await
            .map_err(io::Error::other)?
    }

    async fn check_write(&self, path: &Path) -> io::Result<()> {
        let path = path.to_path_buf();
        tokio::task::spawn_blocking(move || check_writable(&path))
            .await
            .map_err(io::Error::other)?
    }

    async fn metadata(&self, path: &Path) -> io::Result<FileMeta> {
        let path = path.to_path_buf();
        tokio::task::spawn_blocking(move || {
            let meta = std::fs::metadata(&path)?;
            Ok(FileMeta {
                is_dir: meta.is_dir(),
                is_file: meta.is_file(),
                size: meta.len(),
                readonly: meta.is_file() && is_read_only(&path),
                stamp: FileStamp::of(&meta),
            })
        })
        .await
        .map_err(io::Error::other)?
    }

    async fn list_dir(&self, path: &Path) -> io::Result<Vec<DirEntry>> {
        let mut entries = Vec::new();
        let mut reader = tokio::fs::read_dir(path).await?;
        while let Some(entry) = reader.next_entry().await? {
            let is_dir = entry.file_type().await.map(|t| t.is_dir()).unwrap_or(false);
            entries.push(DirEntry {
                name: entry.file_name().to_string_lossy().into_owned(),
                is_dir,
            });
        }
        entries.sort_by(|a, b| a.name.cmp(&b.name));
        Ok(entries)
    }

    async fn exec(&self, req: ExecRequest) -> io::Result<ExecOutput> {
        let started = Instant::now();
        let mut command = tokio::process::Command::new(&self.shell);
        command
            .arg("-c")
            .arg(&req.command)
            .current_dir(&req.cwd)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        for var in &self.scrub_env {
            command.env_remove(var);
        }
        command.envs(&self.extra_env).envs(&req.env);
        #[cfg(unix)]
        command.process_group(0);

        let mut child = command.spawn()?;
        let pid = child.id();
        let (tx, mut rx) = mpsc::channel::<Vec<u8>>(64);
        if let Some(stdout) = child.stdout.take() {
            tokio::spawn(pump(stdout, tx.clone()));
        }
        if let Some(stderr) = child.stderr.take() {
            tokio::spawn(pump(stderr, tx.clone()));
        }
        drop(tx);
        let mut waiter = tokio::spawn(async move { child.wait().await });

        let mut capture = Capture::new(&req.capture);
        let deadline = tokio::time::Instant::now() + req.timeout;
        let mut exit_status: Option<std::process::ExitStatus> = None;
        let mut pipes_open = true;
        let mut grace_deadline: Option<tokio::time::Instant> = None;
        let mut timed_out = false;
        let mut cancelled = false;

        loop {
            if exit_status.is_some() && !pipes_open {
                break;
            }
            let grace = grace_deadline;
            let grace_sleep = async move {
                match grace {
                    Some(at) => tokio::time::sleep_until(at).await,
                    None => std::future::pending().await,
                }
            };
            let stopping = timed_out || cancelled;
            tokio::select! {
                chunk = rx.recv(), if pipes_open => match chunk {
                    Some(bytes) => {
                        if let Some(progress) = &req.progress {
                            progress.emit(&clean_text(&bytes));
                        }
                        capture.push(&bytes)?;
                    }
                    None => pipes_open = false,
                },
                result = &mut waiter, if exit_status.is_none() => {
                    let status = result.map_err(io::Error::other)??;
                    exit_status = Some(status);
                    if pipes_open {
                        grace_deadline = Some(tokio::time::Instant::now() + EXIT_GRACE);
                    }
                }
                _ = grace_sleep => {
                    // The command exited but something it started still holds the output pipes.
                    signal_group(pid, libc::SIGKILL);
                    pipes_open = false;
                }
                _ = tokio::time::sleep_until(deadline), if !stopping && exit_status.is_none() => {
                    timed_out = true;
                    terminate_group(pid);
                }
                _ = req.cancel.cancelled(), if !stopping && exit_status.is_none() => {
                    cancelled = true;
                    terminate_group(pid);
                }
            }
        }

        let status = exit_status.expect("loop exits only after the process exited");
        #[cfg(unix)]
        let signal = std::os::unix::process::ExitStatusExt::signal(&status);
        #[cfg(not(unix))]
        let signal = None;
        Ok(ExecOutput {
            exit_code: status.code(),
            signal,
            timed_out,
            cancelled,
            duration: started.elapsed(),
            output: capture.finish()?,
        })
    }
}

/// Opens a regular file for reading. Anything else fails at once: opening a named pipe blocks
/// until a writer comes, and a device may never end.
fn open_regular(path: &Path) -> io::Result<(std::fs::File, std::fs::Metadata)> {
    check_regular(&std::fs::metadata(path)?)?;
    let mut options = std::fs::OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        // Should something else have taken the file's place since the check, opening it still
        // returns at once, and a terminal does not become our controlling terminal.
        options.custom_flags(libc::O_NONBLOCK | libc::O_NOCTTY);
    }
    let file = options.open(path)?;
    let meta = file.metadata()?;
    check_regular(&meta)?;
    Ok((file, meta))
}

fn check_regular(meta: &std::fs::Metadata) -> io::Result<()> {
    let file_type = meta.file_type();
    if file_type.is_file() {
        return Ok(());
    }
    if file_type.is_dir() {
        return Err(io::Error::new(io::ErrorKind::IsADirectory, "path is a directory"));
    }
    #[cfg(unix)]
    let kind = {
        use std::os::unix::fs::FileTypeExt;
        if file_type.is_fifo() {
            "named pipe"
        } else if file_type.is_socket() {
            "socket"
        } else if file_type.is_char_device() {
            "character device"
        } else if file_type.is_block_device() {
            "block device"
        } else {
            "special file"
        }
    };
    #[cfg(not(unix))]
    let kind = "special file";
    Err(NotRegularFile::error(kind))
}

async fn pump(mut reader: impl AsyncRead + Unpin, tx: mpsc::Sender<Vec<u8>>) {
    let mut buf = vec![0u8; 8192];
    loop {
        match reader.read(&mut buf).await {
            Ok(0) | Err(_) => break,
            Ok(n) => {
                if tx.send(buf[..n].to_vec()).await.is_err() {
                    break;
                }
            }
        }
    }
}

fn signal_group(pid: Option<u32>, signal: libc::c_int) {
    #[cfg(unix)]
    if let Some(pid) = pid {
        // The child leads its own process group (process_group(0)), so this reaches
        // everything it started.
        unsafe {
            libc::killpg(pid as libc::pid_t, signal);
        }
    }
}

/// SIGTERM now, SIGKILL after a grace period.
fn terminate_group(pid: Option<u32>) {
    signal_group(pid, libc::SIGTERM);
    tokio::spawn(async move {
        tokio::time::sleep(KILL_GRACE).await;
        signal_group(pid, libc::SIGKILL);
    });
}

/// Output as text: escape sequences removed, tabs kept (see [`ansi`](crate::ansi)).
pub(crate) fn clean_text(bytes: &[u8]) -> String {
    String::from_utf8_lossy(&ansi::strip(bytes)).into_owned()
}

/// Bounded capture of command output: the head and tail stay in memory, everything goes to
/// the spill file once the output gets large. The spill file holds the same cleaned text the
/// model sees, so its line numbers and content match the note.
struct Capture {
    config: CaptureConfig,
    head: Vec<u8>,
    tail: VecDeque<u8>,
    /// The byte just before the tail is a newline, so the tail starts a line.
    tail_starts_line: bool,
    mem: Vec<u8>,
    spill: Option<(std::fs::File, PathBuf)>,
    stripper: AnsiStripper,
    /// Cleaned bytes produced for the spill file, including any beyond its cap.
    cleaned: u64,
    spilled: u64,
    capped: bool,
    total: u64,
    last: Option<u8>,
    lines: u64,
}

impl Capture {
    fn new(config: &CaptureConfig) -> Self {
        Self {
            config: config.clone(),
            head: Vec::new(),
            tail: VecDeque::new(),
            tail_starts_line: false,
            mem: Vec::new(),
            spill: None,
            stripper: AnsiStripper::new(),
            cleaned: 0,
            spilled: 0,
            capped: false,
            total: 0,
            last: None,
            lines: 0,
        }
    }

    fn push(&mut self, bytes: &[u8]) -> io::Result<()> {
        if bytes.is_empty() {
            return Ok(());
        }
        self.total += bytes.len() as u64;
        self.last = bytes.last().copied();
        self.lines += bytes.iter().filter(|b| **b == b'\n').count() as u64;
        if self.head.len() < self.config.head_bytes {
            let take = (self.config.head_bytes - self.head.len()).min(bytes.len());
            self.head.extend_from_slice(&bytes[..take]);
        }
        self.tail.extend(bytes.iter().copied());
        if self.tail.len() > self.config.tail_bytes {
            let excess = self.tail.len() - self.config.tail_bytes;
            self.tail_starts_line = self.tail[excess - 1] == b'\n';
            self.tail.drain(..excess);
        }
        if self.spill.is_some() {
            return self.write_spill(bytes);
        }
        self.mem.extend_from_slice(bytes);
        if self.mem.len() > MEM_LIMIT {
            let mem = std::mem::take(&mut self.mem);
            if self.open_spill()? {
                self.write_spill(&mem)?;
            }
        }
        Ok(())
    }

    /// Opens the spill file if one is configured.
    fn open_spill(&mut self) -> io::Result<bool> {
        let Some(path) = &self.config.spill_path else {
            return Ok(false);
        };
        self.spill = Some(create_unique(path)?);
        Ok(true)
    }

    fn write_spill(&mut self, bytes: &[u8]) -> io::Result<()> {
        let mut text = Vec::with_capacity(bytes.len());
        self.stripper.feed(bytes, &mut text);
        self.cleaned += text.len() as u64;
        let room = self.config.max_saved_bytes.saturating_sub(self.spilled);
        let n = (text.len() as u64).min(room) as usize;
        if n < text.len() {
            self.capped = true;
        }
        if let Some((file, _)) = &mut self.spill
            && n > 0
        {
            file.write_all(&text[..n])?;
            self.spilled += n as u64;
        }
        Ok(())
    }

    fn finish(mut self) -> io::Result<CapturedOutput> {
        let limit = (self.config.head_bytes + self.config.tail_bytes) as u64;
        let total_lines = self.lines + u64::from(self.last.is_some_and(|b| b != b'\n'));
        if self.total <= limit {
            return Ok(CapturedOutput {
                head: clean_text(&self.mem),
                tail: String::new(),
                total_bytes: self.total,
                total_lines,
                truncated: false,
                omission: None,
                saved: None,
            });
        }
        if self.spill.is_none() && !self.mem.is_empty() {
            let mem = std::mem::take(&mut self.mem);
            if self.open_spill()? {
                self.write_spill(&mem)?;
            }
        }
        let saved = match &mut self.spill {
            Some((file, path)) => {
                file.flush()?;
                Some(SavedOutput {
                    path: path.clone(),
                    capped: self.capped,
                })
            }
            None => None,
        };
        let tail: Vec<u8> = self.tail.into_iter().collect();
        let (head, tail, mut omission) = trim_to_lines(&self.head, &tail, self.tail_starts_line, self.total, total_lines);
        let (head, tail) = (clean_text(head), clean_text(tail));
        if saved.is_some() {
            // Positions in the saved (cleaned) text.
            let end = self.cleaned.saturating_sub(tail.len() as u64);
            omission.byte_range = (head.len() as u64 + 1, end);
            omission.bytes = end.saturating_sub(head.len() as u64);
        }
        Ok(CapturedOutput {
            head,
            tail,
            total_bytes: self.total,
            total_lines,
            truncated: true,
            omission: Some(omission),
            saved,
        })
    }
}

/// Replaces the file at `path` with `data`: the data goes to a new temporary file in the same
/// directory, reaches the disk ([`sync_before_rename`]), and the temporary file is renamed over
/// the old one, so readers, and the file after a crash, are the old or the new version, never a
/// mix.
///
/// - A symbolic link stays a link; the file at the end of it is replaced ([`write_target`]).
/// - A file with other hard links is detached from them, since the rename puts a new file under
///   this one name. This is deliberate: writing in place would change the file under all its
///   names, including names outside the workspace that the approval policy never checked.
/// - A file this process may not write is refused, although the rename would replace it with
///   write access to the directory alone.
fn atomic_write(path: &Path, data: &[u8]) -> io::Result<WriteOutcome> {
    let dir = path
        .parent()
        .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidInput, "path has no parent directory"))?;
    std::fs::create_dir_all(dir)?;
    let target = write_target(path)?;
    let existing = match std::fs::metadata(&target) {
        Ok(meta) => Some(meta),
        Err(err) if err.kind() == io::ErrorKind::NotFound => None,
        Err(err) => return Err(err),
    };
    if let Some(meta) = &existing {
        // A directory, or a named pipe or device the rename would replace with a regular file.
        check_regular(meta)?;
        if is_read_only(&target) {
            return Err(io::Error::new(
                io::ErrorKind::ReadOnlyFilesystem,
                format!("{} is read-only", path.display()),
            ));
        }
    }
    let (Some(target_dir), Some(name)) = (target.parent(), target.file_name()) else {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, "path does not name a file"));
    };
    let (mut file, tmp) = create_temp(target_dir, name, existing.is_some()).map_err(|err| {
        if err.kind() == io::ErrorKind::NotFound && target != path {
            let message = format!("the link points to {}, whose directory does not exist", target.display());
            io::Error::new(err.kind(), message)
        } else {
            err
        }
    })?;
    let result = (|| {
        file.write_all(data)?;
        if let Some(meta) = &existing {
            file.set_permissions(meta.permissions())?;
        }
        sync_before_rename(&file)?;
        std::fs::rename(&tmp, &target)
    })();
    if let Err(err) = result {
        let _ = std::fs::remove_file(&tmp);
        return Err(err);
    }
    // From the open file: the version written here, even if another one replaces it at once.
    let meta = file.metadata()?;
    Ok(WriteOutcome {
        created: existing.is_none(),
        bytes: data.len() as u64,
        stamp: FileStamp::of(&meta),
    })
}

/// What [`atomic_write`] would refuse, found without writing: a directory or special file, a
/// read-only file, a link whose target's directory does not exist, or a directory this process
/// may not create the temporary file in.
fn check_writable(path: &Path) -> io::Result<()> {
    let target = write_target(path)?;
    match std::fs::metadata(&target) {
        Ok(meta) => {
            check_regular(&meta)?;
            if is_read_only(&target) {
                return Err(io::Error::new(
                    io::ErrorKind::ReadOnlyFilesystem,
                    format!("{} is read-only", path.display()),
                ));
            }
        }
        Err(err) if err.kind() == io::ErrorKind::NotFound => {}
        Err(err) => return Err(err),
    }
    let Some(mut dir) = target.parent() else {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, "path does not name a file"));
    };
    // Missing directories of a plain path are created; a link's target only in an existing one.
    loop {
        match std::fs::metadata(dir) {
            Ok(meta) if meta.is_dir() => break,
            Ok(_) => {
                let message = format!("{} is not a directory", dir.display());
                return Err(io::Error::new(io::ErrorKind::NotADirectory, message));
            }
            Err(err) if err.kind() == io::ErrorKind::NotFound && target != path => {
                let message = format!("the link points to {}, whose directory does not exist", target.display());
                return Err(io::Error::new(err.kind(), message));
            }
            Err(err) if err.kind() == io::ErrorKind::NotFound => match dir.parent() {
                Some(parent) => dir = parent,
                None => return Err(err),
            },
            Err(err) => return Err(err),
        }
    }
    if is_read_only(dir) {
        let message = format!("files cannot be created in {}", dir.display());
        return Err(io::Error::new(io::ErrorKind::PermissionDenied, message));
    }
    Ok(())
}

/// Makes the data of a new file reach the disk before the rename that puts it in place, so a
/// crash cannot leave a renamed file without its data. On macOS a write barrier orders the two at
/// a fraction of the cost of a full flush (F_FULLFSYNC, about 5 ms a write); a crash may then
/// lose the newest write, but never leaves a torn file. Where the barrier is not supported, the
/// full flush is used.
fn sync_before_rename(file: &std::fs::File) -> io::Result<()> {
    #[cfg(target_vendor = "apple")]
    {
        use std::os::fd::AsRawFd;
        // The file is open for the whole call.
        if unsafe { libc::fcntl(file.as_raw_fd(), libc::F_BARRIERFSYNC) } == 0 {
            return Ok(());
        }
    }
    file.sync_all()
}

/// Links followed to find the file a write replaces; a longer chain is treated as a loop.
const MAX_LINK_HOPS: usize = 40;
/// The part of the file name kept in a temporary file's name, within the 255-byte limit.
const TMP_NAME_BYTES: usize = 200;

/// The file a write to `path` replaces: `path` itself, or the end of the chain when it is a
/// symbolic link. A relative target counts from the directory holding its link, and `..` is left
/// for the system to resolve, since that directory may itself be reached through a link. A
/// dangling link gives its missing target, which the write creates, as a shell redirect does.
fn write_target(path: &Path) -> io::Result<PathBuf> {
    let mut target = path.to_path_buf();
    for _ in 0..MAX_LINK_HOPS {
        match std::fs::symlink_metadata(&target) {
            Ok(meta) if meta.file_type().is_symlink() => {
                let next = std::fs::read_link(&target)?;
                target = match target.parent() {
                    Some(dir) => dir.join(next),
                    None => next,
                };
            }
            Ok(_) => return Ok(target),
            Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(target),
            Err(err) => return Err(err),
        }
    }
    Err(io::Error::from_raw_os_error(libc::ELOOP))
}

/// Whether this process may not write `path`, as `access(2)` answers for `W_OK`: permission
/// bits, ACLs, the immutable flag and a read-only file system all count.
fn is_read_only(path: &Path) -> bool {
    #[cfg(unix)]
    {
        use std::os::unix::ffi::OsStrExt;
        let Ok(c_path) = std::ffi::CString::new(path.as_os_str().as_bytes()) else {
            return false;
        };
        // `c_path` is a valid C string that outlives the call.
        if unsafe { libc::access(c_path.as_ptr(), libc::W_OK) } == 0 {
            return false;
        }
        matches!(
            io::Error::last_os_error().raw_os_error(),
            Some(libc::EACCES | libc::EPERM | libc::EROFS)
        )
    }
    #[cfg(not(unix))]
    {
        std::fs::metadata(path).is_ok_and(|meta| meta.permissions().readonly())
    }
}

/// Creates the temporary file for a write to `name` in `dir`, under a random name.
/// `create_new` (O_EXCL) never opens, follows or truncates anything already there.
fn create_temp(dir: &Path, name: &OsStr, replacing: bool) -> io::Result<(std::fs::File, PathBuf)> {
    let (name, _) = cap_text(&name.to_string_lossy(), TMP_NAME_BYTES);
    let mut options = std::fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        // A replacement is private until the old file's permissions are copied to it; a new
        // file gets the usual mode, less the umask.
        options.mode(if replacing { 0o600 } else { 0o666 });
    }
    for _ in 0..100 {
        let tmp = dir.join(format!(".{name}.her-tmp-{:08x}", rand::random::<u32>()));
        match options.open(&tmp) {
            Ok(file) => return Ok((file, tmp)),
            Err(err) if err.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(err) => return Err(err),
        }
    }
    Err(io::Error::new(io::ErrorKind::AlreadyExists, "no free temporary file name"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;
    use std::os::unix::fs::symlink;
    use tokio_util::sync::CancellationToken;

    fn request(command: &str, cwd: &Path) -> ExecRequest {
        ExecRequest {
            command: command.to_string(),
            cwd: cwd.to_path_buf(),
            env: HashMap::new(),
            timeout: Duration::from_secs(10),
            capture: CaptureConfig::default(),
            progress: None,
            cancel: CancellationToken::new(),
        }
    }

    #[tokio::test]
    async fn runs_commands_and_merges_output() {
        let dir = tempfile::tempdir().unwrap();
        let env = LocalEnvironment::new(dir.path().to_path_buf());
        let out = env.exec(request("echo out; echo err >&2; exit 3", dir.path())).await.unwrap();
        assert_eq!(out.exit_code, Some(3));
        assert!(out.output.head.contains("out"));
        assert!(out.output.head.contains("err"));
        assert!(!out.output.truncated);
    }

    #[tokio::test]
    async fn times_out_and_kills_the_group() {
        let dir = tempfile::tempdir().unwrap();
        let env = LocalEnvironment::new(dir.path().to_path_buf());
        let mut req = request("sleep 30 & sleep 30; echo never", dir.path());
        req.timeout = Duration::from_millis(300);
        let started = Instant::now();
        let out = env.exec(req).await.unwrap();
        assert!(out.timed_out);
        assert!(started.elapsed() < Duration::from_secs(5));
        assert!(!out.output.head.contains("never"));
    }

    #[tokio::test]
    async fn cancellation_stops_the_command() {
        let dir = tempfile::tempdir().unwrap();
        let env = LocalEnvironment::new(dir.path().to_path_buf());
        let req = request("sleep 30", dir.path());
        let cancel = req.cancel.clone();
        tokio::spawn(async move {
            tokio::time::sleep(Duration::from_millis(200)).await;
            cancel.cancel();
        });
        let out = env.exec(req).await.unwrap();
        assert!(out.cancelled);
    }

    #[tokio::test]
    async fn large_output_keeps_head_and_tail_and_spills() {
        let dir = tempfile::tempdir().unwrap();
        let env = LocalEnvironment::new(dir.path().to_path_buf());
        let mut req = request("seq 1 200000", dir.path());
        let spill = dir.path().join("out.log");
        req.capture.spill_path = Some(spill.clone());
        let out = env.exec(req).await.unwrap();
        assert!(out.output.truncated);
        assert!(out.output.head.starts_with("1\n2\n"));
        assert!(out.output.head.ends_with('\n'), "the head ends on a whole line");
        assert!(out.output.tail.trim_end().ends_with("200000"));
        assert_eq!(out.output.total_lines, 200000);
        let full = std::fs::read_to_string(&spill).unwrap();
        assert_eq!(full.lines().count(), 200000);
        // The note names the omitted lines exactly: the last head line and the first tail
        // line are their neighbours.
        let (first, last) = out.output.omission.unwrap().lines.unwrap();
        assert_eq!(out.output.head.lines().last().unwrap(), (first - 1).to_string());
        assert_eq!(out.output.tail.lines().next().unwrap(), (last + 1).to_string());
        let text = out.output.text();
        assert!(text.contains(&format!("[Lines {first}-{last} of 200000 omitted")), "{text}");
        assert!(text.contains(&format!("out.log (read it with offset={first})")));

        // A second command with the same file name does not overwrite the first file.
        let mut again = request("seq 1 100000", dir.path());
        again.capture.spill_path = Some(spill.clone());
        again.capture.max_saved_bytes = 1000;
        let out = env.exec(again).await.unwrap();
        let saved = out.output.saved.unwrap();
        assert_eq!(saved.path, dir.path().join("out-1.log"));
        assert!(saved.capped);
        assert_eq!(std::fs::metadata(&saved.path).unwrap().len(), 1000);
        assert_eq!(std::fs::read_to_string(&spill).unwrap().lines().count(), 200000);
    }

    #[tokio::test]
    async fn the_saved_output_is_the_text_the_model_sees() {
        let dir = tempfile::tempdir().unwrap();
        let env = LocalEnvironment::new(dir.path().to_path_buf());
        // Colours and tabs on every line, enough lines to be cut.
        let mut req = request(
            "for i in $(seq 1 3000); do printf '\\033[32mok\\033[0m\\t%d\\r\\n' $i; done",
            dir.path(),
        );
        req.capture.spill_path = Some(dir.path().join("out.txt"));
        let out = env.exec(req).await.unwrap();
        let saved = std::fs::read_to_string(out.output.saved.unwrap().path).unwrap();
        let expected: String = (1..=3000).map(|i| format!("ok\t{i}\n")).collect();
        assert_eq!(saved, expected, "escapes and CRs removed, tabs kept");
        assert!(out.output.head.starts_with("ok\t1\nok\t2\n"));
        let (first, last) = out.output.omission.unwrap().lines.unwrap();
        let lines: Vec<&str> = expected.lines().collect();
        assert_eq!(out.output.head.lines().last().unwrap(), lines[first as usize - 2]);
        assert_eq!(out.output.tail.lines().next().unwrap(), lines[last as usize]);
    }

    #[tokio::test]
    async fn scrubs_selected_variables() {
        let dir = tempfile::tempdir().unwrap();
        let env = LocalEnvironment::new(dir.path().to_path_buf())
            .with_scrubbed_env(vec!["HOME".into()])
            .with_env("HER_MARK", "1");
        let out = env
            .exec(request("echo \"home=${HOME:-unset} mark=$HER_MARK pager=$PAGER\"", dir.path()))
            .await
            .unwrap();
        assert_eq!(out.output.head.trim(), "home=unset mark=1 pager=cat");
    }

    #[tokio::test]
    async fn atomic_write_creates_parents_and_reports_creation() {
        let dir = tempfile::tempdir().unwrap();
        let env = LocalEnvironment::new(dir.path().to_path_buf());
        let path = dir.path().join("a/b/c.txt");
        let first = env.write_file(&path, b"one".to_vec()).await.unwrap();
        assert!(first.created);
        let second = env.write_file(&path, b"two".to_vec()).await.unwrap();
        assert!(!second.created);
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "two");
        let leftovers: Vec<_> = std::fs::read_dir(dir.path().join("a/b")).unwrap().collect();
        assert_eq!(leftovers.len(), 1, "no temporary files left behind");
    }

    /// Temporary files left in `dir`.
    fn leftovers(dir: &Path) -> Vec<String> {
        std::fs::read_dir(dir)
            .unwrap()
            .map(|entry| entry.unwrap().file_name().to_string_lossy().into_owned())
            .filter(|name| name.contains(".her-tmp-"))
            .collect()
    }

    fn is_link(path: &Path) -> bool {
        std::fs::symlink_metadata(path).unwrap().file_type().is_symlink()
    }

    #[tokio::test]
    async fn atomic_write_replaces_what_a_symlink_points_to() {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path();
        let env = LocalEnvironment::new(root.to_path_buf());
        std::fs::create_dir_all(root.join("a")).unwrap();
        std::fs::create_dir_all(root.join("b")).unwrap();
        let target = root.join("b/target.txt");
        std::fs::write(&target, "v0").unwrap();
        // Relative through `..`, absolute, and a chain of two.
        symlink("../b/target.txt", root.join("a/relative")).unwrap();
        symlink(&target, root.join("absolute")).unwrap();
        symlink("a/relative", root.join("chain")).unwrap();
        for (i, name) in ["a/relative", "absolute", "chain"].into_iter().enumerate() {
            let link = root.join(name);
            let points_to = std::fs::read_link(&link).unwrap();
            let data = format!("v{}", i + 1);
            let outcome = env.write_file(&link, data.clone().into_bytes()).await.unwrap();
            assert!(!outcome.created);
            assert!(is_link(&link), "{name} is still a link");
            assert_eq!(std::fs::read_link(&link).unwrap(), points_to);
            assert_eq!(std::fs::read_to_string(&target).unwrap(), data);
            // The stamp is that of the file reached through the link, as later checks see it.
            assert_eq!(outcome.stamp, FileStamp::of(&std::fs::metadata(&link).unwrap()));
        }
        assert!(leftovers(&root.join("b")).is_empty());

        // `..` in a link counts from where the link really is: `alias/link` is `real/inner/link`,
        // so `../file.txt` is `real/file.txt`, not `file.txt` next to `alias`.
        std::fs::create_dir_all(root.join("real/inner")).unwrap();
        std::fs::write(root.join("real/file.txt"), "old").unwrap();
        symlink("real/inner", root.join("alias")).unwrap();
        symlink("../file.txt", root.join("real/inner/link")).unwrap();
        env.write_file(&root.join("alias/link"), b"new".to_vec()).await.unwrap();
        assert_eq!(std::fs::read_to_string(root.join("real/file.txt")).unwrap(), "new");
        assert!(!root.join("file.txt").exists());
        assert!(is_link(&root.join("real/inner/link")));
    }

    #[tokio::test]
    async fn atomic_write_through_dangling_links_and_loops() {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path();
        let env = LocalEnvironment::new(root.to_path_buf());
        std::fs::create_dir(root.join("docs")).unwrap();
        symlink("docs/new.md", root.join("dangling")).unwrap();
        let outcome = env.write_file(&root.join("dangling"), b"hello".to_vec()).await.unwrap();
        assert!(outcome.created);
        assert!(is_link(&root.join("dangling")));
        assert_eq!(std::fs::read_to_string(root.join("docs/new.md")).unwrap(), "hello");

        // As with a shell redirect, the directory of the target is not created.
        symlink("missing/new.md", root.join("nowhere")).unwrap();
        let err = env.write_file(&root.join("nowhere"), b"x".to_vec()).await.unwrap_err();
        assert!(err.to_string().contains("whose directory does not exist"), "{err}");
        assert!(!root.join("missing").exists());
        assert!(is_link(&root.join("nowhere")));

        symlink("loop-b", root.join("loop-a")).unwrap();
        symlink("loop-a", root.join("loop-b")).unwrap();
        let err = env.write_file(&root.join("loop-a"), b"x".to_vec()).await.unwrap_err();
        assert_eq!(err.raw_os_error(), Some(libc::ELOOP));
        assert!(is_link(&root.join("loop-a")) && is_link(&root.join("loop-b")));
        assert!(leftovers(root).is_empty());
    }

    #[tokio::test]
    async fn atomic_write_handles_names_near_the_length_limit() {
        let dir = tempfile::tempdir().unwrap();
        let env = LocalEnvironment::new(dir.path().to_path_buf());
        // 250 bytes: the temporary file's name must not grow past 255.
        let path = dir.path().join("é".repeat(125));
        env.write_file(&path, b"one".to_vec()).await.unwrap();
        env.write_file(&path, b"two".to_vec()).await.unwrap();
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "two");
        assert!(leftovers(dir.path()).is_empty());
    }

    #[tokio::test]
    async fn a_failed_write_leaves_no_temporary_file() {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path();
        let env = LocalEnvironment::new(root.to_path_buf());
        // The temporary file is created next to `missing`, then the rename to `missing/` fails.
        symlink("missing/", root.join("link")).unwrap();
        env.write_file(&root.join("link"), b"x".to_vec()).await.unwrap_err();
        assert!(leftovers(root).is_empty());
        assert!(!root.join("missing").exists());
        assert!(is_link(&root.join("link")));
    }

    #[tokio::test]
    async fn atomic_write_detaches_hard_links() {
        use std::os::unix::fs::MetadataExt;
        let dir = tempfile::tempdir().unwrap();
        let env = LocalEnvironment::new(dir.path().to_path_buf());
        let path = dir.path().join("inside.txt");
        // Another name of the same file, which could be outside the workspace.
        let other = dir.path().join("other.txt");
        std::fs::write(&path, "shared").unwrap();
        std::fs::hard_link(&path, &other).unwrap();
        env.write_file(&path, b"new".to_vec()).await.unwrap();
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "new");
        assert_eq!(std::fs::read_to_string(&other).unwrap(), "shared", "the other name keeps the old file");
        assert_eq!(std::fs::metadata(&path).unwrap().nlink(), 1);
        assert_eq!(std::fs::metadata(&other).unwrap().nlink(), 1);
    }

    #[tokio::test]
    async fn atomic_write_refuses_read_only_files() {
        // Root may write anything.
        if unsafe { libc::geteuid() } == 0 {
            return;
        }
        let dir = tempfile::tempdir().unwrap();
        let env = LocalEnvironment::new(dir.path().to_path_buf());
        let path = dir.path().join("locked.txt");
        std::fs::write(&path, "keep").unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o444)).unwrap();
        symlink("locked.txt", dir.path().join("link")).unwrap();
        for name in ["locked.txt", "link"] {
            let err = env.write_file(&dir.path().join(name), b"x".to_vec()).await.unwrap_err();
            assert_eq!(err.kind(), io::ErrorKind::ReadOnlyFilesystem);
            assert_eq!(err.to_string(), format!("{} is read-only", dir.path().join(name).display()));
        }
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "keep");
        assert!(is_link(&dir.path().join("link")));
        assert!(leftovers(dir.path()).is_empty());
    }

    #[tokio::test]
    async fn atomic_write_keeps_permissions() {
        let dir = tempfile::tempdir().unwrap();
        let env = LocalEnvironment::new(dir.path().to_path_buf());
        let mode = |path: &Path| std::fs::metadata(path).unwrap().permissions().mode() & 0o7777;
        for wanted in [0o755, 0o640, 0o600] {
            let path = dir.path().join(format!("f{wanted:o}"));
            std::fs::write(&path, "old").unwrap();
            std::fs::set_permissions(&path, std::fs::Permissions::from_mode(wanted)).unwrap();
            env.write_file(&path, b"new".to_vec()).await.unwrap();
            assert_eq!(mode(&path), wanted);
        }
        // A new file gets the mode of any file created here.
        let reference = dir.path().join("reference");
        std::fs::write(&reference, "").unwrap();
        env.write_file(&dir.path().join("new"), b"x".to_vec()).await.unwrap();
        assert_eq!(mode(&dir.path().join("new")), mode(&reference));
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn reads_only_regular_files() {
        use std::os::unix::ffi::OsStrExt;
        let dir = tempfile::tempdir().unwrap();
        let env = LocalEnvironment::new(dir.path().to_path_buf());
        let fifo = dir.path().join("pipe");
        let name = std::ffi::CString::new(fifo.as_os_str().as_bytes()).unwrap();
        assert_eq!(unsafe { libc::mkfifo(name.as_ptr(), 0o644) }, 0);
        let window = LineWindow {
            offset: 1,
            max_lines: 10,
            max_bytes: 1000,
            max_line_chars: 100,
        };
        let limit = Duration::from_secs(5);
        let err = tokio::time::timeout(limit, env.read_file(&fifo, 100)).await.unwrap().unwrap_err();
        assert_eq!(NotRegularFile::of(&err).unwrap().kind, "named pipe");
        let err = tokio::time::timeout(limit, env.read_lines(&fifo, &window)).await.unwrap().unwrap_err();
        assert_eq!(err.to_string(), "not a regular file (named pipe)");
        let err = env.read_file(Path::new("/dev/null"), 100).await.unwrap_err();
        assert_eq!(NotRegularFile::of(&err).unwrap().kind, "character device");
        let err = env.read_lines(dir.path(), &window).await.unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::IsADirectory);
        assert!(NotRegularFile::of(&err).is_none());

        let file = dir.path().join("f.txt");
        std::fs::write(&file, "a\r\nb\n").unwrap();
        let text = env.read_lines(&file, &window).await.unwrap();
        assert_eq!((text.lines, text.total_lines, text.total_exact), (vec!["a".into(), "b".into()], 2, true));
        assert_eq!(text.total_size, 5);
        assert_eq!(text.stamp, FileStamp::of(&std::fs::metadata(&file).unwrap()));
    }

    #[tokio::test]
    async fn pages_of_a_large_file_come_from_its_line_index() {
        let dir = tempfile::tempdir().unwrap();
        let env = LocalEnvironment::new(dir.path().to_path_buf());
        let path = dir.path().join("big.log");
        let text: String = (1..=400_000).map(|i| format!("line {i}\n")).collect();
        std::fs::write(&path, &text).unwrap();
        let page = |offset| LineWindow {
            offset,
            max_lines: 2,
            max_bytes: 1 << 20,
            max_line_chars: 100,
        };
        let first = env.read_lines(&path, &page(1)).await.unwrap();
        assert_eq!((first.lines, first.total_lines), (vec!["line 1".into(), "line 2".into()], 400_000));
        assert_eq!(env.line_indexes.lock().len(), 1);
        let deep = env.read_lines(&path, &page(399_999)).await.unwrap();
        assert_eq!((deep.lines, deep.total_lines), (vec!["line 399999".into(), "line 400000".into()], 400_000));
        // Another version of the file is scanned again and replaces the index.
        std::fs::write(&path, format!("first\n{text}")).unwrap();
        let changed = env.read_lines(&path, &page(399_999)).await.unwrap();
        assert_eq!((changed.lines, changed.total_lines), (vec!["line 399998".into(), "line 399999".into()], 400_001));
        let indexes = env.line_indexes.lock();
        assert_eq!(indexes.len(), 1);
        assert_eq!(indexes[0].1.stamp(), FileStamp::of(&std::fs::metadata(&path).unwrap()));
    }

    #[test]
    fn resolves_relative_and_parent_paths() {
        let env = LocalEnvironment::new(PathBuf::from("/work/project"));
        assert_eq!(env.resolve("src/main.rs"), PathBuf::from("/work/project/src/main.rs"));
        assert_eq!(env.resolve("../other/x"), PathBuf::from("/work/other/x"));
        assert_eq!(env.resolve("/etc/hosts"), PathBuf::from("/etc/hosts"));
        assert_eq!(env.resolve("./a/./b"), PathBuf::from("/work/project/a/b"));
    }
}
