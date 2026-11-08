//! Watching the files behind the 自定义 page (docs/desktop/customize-design.md §5.3): skill
//! folders and configuration files, wherever the change comes from (the command line, an
//! editor, the model). Events are gathered for two seconds and become one notification each.

use std::collections::HashSet;
use std::path::Path;
use std::path::PathBuf;
use std::time::Duration;

use notify::RecursiveMode;
use notify::Watcher as _;
use tokio::sync::mpsc;

/// How long events are gathered before one notification goes out.
pub const SETTLE: Duration = Duration::from_secs(2);

pub struct Watcher {
    inner: notify::RecommendedWatcher,
    watched: HashSet<(PathBuf, bool)>,
}

impl Watcher {
    /// Paths that changed arrive on the returned channel.
    pub fn new() -> notify::Result<(Self, mpsc::UnboundedReceiver<PathBuf>)> {
        let (tx, rx) = mpsc::unbounded_channel();
        let inner = notify::recommended_watcher(move |result: notify::Result<notify::Event>| {
            if let Ok(event) = result
                && !matches!(event.kind, notify::EventKind::Access(_))
            {
                for path in event.paths {
                    let _ = tx.send(path);
                }
            }
        })?;
        Ok((Self { inner, watched: HashSet::new() }, rx))
    }

    /// Watches `path` when it exists; a path watched already is left as it is.
    pub fn watch(&mut self, path: &Path, recursive: bool) {
        let key = (path.to_path_buf(), recursive);
        if self.watched.contains(&key) || !path.exists() {
            return;
        }
        let mode = if recursive { RecursiveMode::Recursive } else { RecursiveMode::NonRecursive };
        match self.inner.watch(path, mode) {
            Ok(()) => {
                self.watched.insert(key);
            }
            Err(err) => tracing::debug!(path = %path.display(), %err, "cannot watch"),
        }
    }

    pub fn unwatch(&mut self, path: &Path, recursive: bool) {
        if self.watched.remove(&(path.to_path_buf(), recursive)) {
            let _ = self.inner.unwatch(path);
        }
    }
}

/// Gathers paths for [`SETTLE`] after the first one.
pub async fn gather(rx: &mut mpsc::UnboundedReceiver<PathBuf>) -> Option<Vec<PathBuf>> {
    gather_for(rx, SETTLE).await
}

/// Gathers paths for `settle` after the first one.
pub async fn gather_for(rx: &mut mpsc::UnboundedReceiver<PathBuf>, settle: Duration) -> Option<Vec<PathBuf>> {
    let first = rx.recv().await?;
    let mut paths = vec![first];
    tokio::time::sleep(settle).await;
    while let Ok(path) = rx.try_recv() {
        paths.push(path);
    }
    paths.sort();
    paths.dedup();
    Some(paths)
}
