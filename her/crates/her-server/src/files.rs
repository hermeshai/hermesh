//! The desktop's file panel (docs/desktop/file-panel-design.md §9): `files/list`, `files/read`
//! and `files/write` on a loaded session's files, through its kernel's environment and locks
//! (her_core::files), and `files/watch` with its `files/changed` notifications.

use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use her_core::files::FileScope;
use her_protocol::api::Empty;
use her_protocol::api::method;
use her_protocol::files::FilesChangedParams;
use her_protocol::files::FilesListParams;
use her_protocol::files::FilesListResult;
use her_protocol::files::FilesReadParams;
use her_protocol::files::FilesReadResult;
use her_protocol::files::FilesWatchParams;
use her_protocol::files::FilesWriteParams;
use her_protocol::files::FilesWriteResult;
use her_rpc::RpcError;
use parking_lot::Mutex;
use tokio::sync::oneshot;

use crate::conn::ConnHandle;
use crate::conn::ConnId;
use crate::errors;
use crate::hub::HubCmd;
use crate::server::Server;
use crate::watch;

/// How long changes are gathered before one notification goes out: short, as an editor waits on it.
const SETTLE: Duration = Duration::from_millis(300);

/// Who watches what (§9.5): each connection's paths per session, and the folders watched for
/// them, each once, counted.
#[derive(Default)]
pub(crate) struct Watches {
    watcher: Mutex<Option<watch::Watcher>>,
    /// Connection → session → what it watches there.
    subs: Mutex<HashMap<ConnId, HashMap<String, Sub>>>,
    /// Folder → how many asked-for paths need it.
    dirs: Mutex<HashMap<PathBuf, usize>>,
}

/// A connection's watch on a session: where to tell, and each path as asked → how it is watched.
type Sub = (ConnHandle, HashMap<PathBuf, Watched>);

/// A path as the system names it in its events (links followed: `/var` is `/private/var` on
/// macOS), and the folder watched for it.
#[derive(Clone, PartialEq, Eq)]
struct Watched {
    real: PathBuf,
    folder: PathBuf,
}

/// How `path` is watched: a folder itself, else the folder holding the file (an editor that
/// saves by renaming over the file would leave a watch on the file itself behind).
fn watched(path: &Path) -> Watched {
    let canonical = |p: &Path| std::fs::canonicalize(p).unwrap_or_else(|_| p.to_path_buf());
    let real = match (std::fs::canonicalize(path), path.parent(), path.file_name()) {
        (Ok(real), _, _) => real,
        (Err(_), Some(dir), Some(name)) => canonical(dir).join(name),
        _ => path.to_path_buf(),
    };
    let folder = if real.is_dir() { real.clone() } else { real.parent().map_or_else(|| real.clone(), Path::to_path_buf) };
    Watched { real, folder }
}

fn io_error(path: &Path, err: std::io::Error) -> RpcError {
    RpcError::invalid_params(format!("{}: {err}", path.display()))
}

fn join_error(err: tokio::task::JoinError) -> RpcError {
    RpcError::internal(err.to_string())
}

impl Server {
    pub(crate) async fn file_scope(&self, id: &str) -> Result<FileScope, RpcError> {
        let (tx, rx) = oneshot::channel();
        self.loaded(id)?.send(HubCmd::FileScope { reply: tx }).await?;
        rx.await.ok().flatten().ok_or_else(|| errors::session_closed(id))
    }

    pub async fn files_list(&self, params: FilesListParams) -> Result<FilesListResult, RpcError> {
        let scope = self.file_scope(&params.session_id).await?;
        let dir = scope.env.resolve(&params.path);
        tokio::task::spawn_blocking(move || her_core::files::list(&dir).map_err(|e| io_error(&dir, e))).await.map_err(join_error)?
    }

    pub async fn files_read(&self, params: FilesReadParams) -> Result<FilesReadResult, RpcError> {
        let scope = self.file_scope(&params.session_id).await?;
        let path = scope.env.resolve(&params.path);
        let home = self.0.home.clone();
        tokio::task::spawn_blocking(move || her_core::files::read(&home, &path).map_err(|e| io_error(&path, e))).await.map_err(join_error)?
    }

    pub async fn files_write(&self, params: FilesWriteParams) -> Result<FilesWriteResult, RpcError> {
        let scope = self.file_scope(&params.session_id).await?;
        let path = scope.env.resolve(&params.path);
        her_core::files::write(&scope, &self.0.home, &path, &params.text, params.base_hash.as_deref())
            .await
            .map_err(|e| io_error(&path, e))
    }

    pub async fn pptx_read(&self, params: her_protocol::pptx::PptxReadParams, conn: &ConnHandle) -> Result<her_protocol::pptx::PptxReadResult, RpcError> {
        let scope = self.file_scope(&params.session_id).await?;
        let path = scope.env.resolve(&params.path);
        let home = self.0.home.clone();
        let conn = conn.clone();
        tokio::task::spawn_blocking(move || {
            her_core::pptx::read_with_progress(&home, &path, |stage, completed, total| {
                if let Some(progress_id) = &params.progress_id {
                    conn.send(her_rpc::notification(method::PPTX_READ_PROGRESS, &her_protocol::pptx::PptxReadProgress {
                        session_id: params.session_id.clone(), progress_id: progress_id.clone(), stage, completed, total,
                    }));
                }
            }).map_err(|e| io_error(&path, e))
        }).await.map_err(join_error)?
    }

    pub async fn pptx_write(&self, params: her_protocol::pptx::PptxWriteParams) -> Result<FilesWriteResult, RpcError> {
        let scope = self.file_scope(&params.session_id).await?;
        let path = scope.env.resolve(&params.path);
        her_core::pptx::write(&scope, &self.0.home, &path, &params.data, &params.base_hash).await.map_err(|e| io_error(&path, e))
    }

    pub async fn pptx_metafile(&self, params: her_protocol::pptx::PptxMetafileParams) -> Result<her_protocol::pptx::PptxMetafileResult, RpcError> {
        self.file_scope(&params.session_id).await?;
        tokio::task::spawn_blocking(move || her_core::pptx::metafile(&params.data)
            .map_err(|e| RpcError::invalid_params(e.to_string()))).await.map_err(join_error)?
    }

    /// Replaces what `conn` watches for the session; the folders no one needs any more are let go.
    pub async fn files_watch(&self, params: FilesWatchParams, conn: &ConnHandle) -> Result<Empty, RpcError> {
        let scope = self.file_scope(&params.session_id).await?;
        let paths: HashMap<PathBuf, Watched> = params
            .paths
            .iter()
            .map(|p| {
                let path = scope.env.resolve(p);
                let how = watched(&path);
                (path, how)
            })
            .collect();
        let state = &self.0.files;
        let before = {
            let mut subs = state.subs.lock();
            let sessions = subs.entry(conn.id).or_default();
            let before = sessions.remove(&params.session_id).map(|(_, p)| p).unwrap_or_default();
            if !paths.is_empty() {
                sessions.insert(params.session_id.clone(), (conn.clone(), paths.clone()));
            }
            before
        };
        let folders = |m: &HashMap<PathBuf, Watched>| m.values().map(|w| w.folder.clone()).collect::<Vec<_>>();
        self.count_folders(&folders(&before), &folders(&paths));
        Ok(Empty {})
    }

    /// The connection is gone: what it watched is let go.
    pub(crate) fn forget_files(&self, id: ConnId) {
        let gone = self.0.files.subs.lock().remove(&id).unwrap_or_default();
        for (_, paths) in gone.into_values() {
            self.count_folders(&paths.values().map(|w| w.folder.clone()).collect::<Vec<_>>(), &[]);
        }
    }

    /// Counts the folders `now` needs in and those `before` needed out, watching a folder while
    /// its count is above zero.
    fn count_folders(&self, before: &[PathBuf], now: &[PathBuf]) {
        let state = &self.0.files;
        let mut watcher = state.watcher.lock();
        if watcher.is_none() && !now.is_empty() {
            match watch::Watcher::new() {
                Ok((w, mut rx)) => {
                    *watcher = Some(w);
                    let weak = Arc::downgrade(&self.0);
                    tokio::spawn(async move {
                        while let Some(paths) = watch::gather_for(&mut rx, SETTLE).await {
                            let Some(inner) = weak.upgrade() else { break };
                            Server(inner).files_changed(&paths);
                        }
                    });
                }
                Err(err) => tracing::warn!(%err, "cannot watch the file panel's files"),
            }
        }
        let mut dirs = state.dirs.lock();
        for dir in now {
            let count = dirs.entry(dir.clone()).or_default();
            *count += 1;
            if *count == 1
                && let Some(w) = watcher.as_mut()
            {
                w.watch(dir, false);
            }
        }
        for dir in before {
            if let Some(count) = dirs.get_mut(dir) {
                *count -= 1;
                if *count == 0 {
                    dirs.remove(dir);
                    if let Some(w) = watcher.as_mut() {
                        w.unwatch(dir, false);
                    }
                }
            }
        }
    }

    /// Tells each watcher which of its paths changed: a file changed, or a folder's entries did.
    fn files_changed(&self, changed: &[PathBuf]) {
        let subs = self.0.files.subs.lock();
        for sessions in subs.values() {
            for (session_id, (conn, paths)) in sessions {
                // The file itself, or (a folder) one of its entries.
                let hit: Vec<PathBuf> = paths
                    .iter()
                    .filter(|(_, w)| changed.iter().any(|c| *c == w.real || c.parent() == Some(w.real.as_path())))
                    .map(|(p, _)| p.clone())
                    .collect();
                if !hit.is_empty() {
                    let params = FilesChangedParams { session_id: session_id.clone(), paths: hit };
                    conn.send(her_rpc::notification(method::FILES_CHANGED, &params));
                }
            }
        }
    }
}
