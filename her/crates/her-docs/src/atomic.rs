//! Files written beside their place, then renamed over it: a reader never finds one half written.

use std::io;
use std::path::Path;
use std::path::PathBuf;

/// The file a write goes to before it is renamed over `target`: hidden, in the same folder.
fn partial_of(target: &Path) -> PathBuf {
    let name = target.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
    target.with_file_name(format!(".{name}.her-partial"))
}

/// Writes `bytes` to `target`; an existing file's permissions carry over.
pub fn write(target: &Path, bytes: &[u8]) -> io::Result<()> {
    let partial = partial_of(target);
    std::fs::write(&partial, bytes)?;
    finish(&partial, target)
}

/// Copies `from` to `target`; an existing file's permissions carry over.
pub fn copy(from: &Path, target: &Path) -> io::Result<()> {
    let partial = partial_of(target);
    std::fs::copy(from, &partial)?;
    finish(&partial, target)
}

fn finish(partial: &Path, target: &Path) -> io::Result<()> {
    if let Ok(m) = std::fs::metadata(target) {
        let _ = std::fs::set_permissions(partial, m.permissions());
    }
    std::fs::rename(partial, target).inspect_err(|_| {
        let _ = std::fs::remove_file(partial);
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn writes_and_copies_leave_nothing_beside() {
        let dir = tempfile::tempdir().unwrap();
        let a = dir.path().join("a.csv");
        write(&a, b"one").unwrap();
        write(&a, b"two").unwrap();
        assert_eq!(std::fs::read(&a).unwrap(), b"two");
        let b = dir.path().join("b.csv");
        copy(&a, &b).unwrap();
        assert_eq!(std::fs::read(&b).unwrap(), b"two");
        let names: Vec<_> = std::fs::read_dir(dir.path()).unwrap().map(|e| e.unwrap().file_name()).collect();
        assert_eq!(names.len(), 2);
    }
}
