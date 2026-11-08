//! Moving files to the user's trash instead of deleting them, so a removed skill can be put
//! back (docs/desktop/customize-design.md §9.2).

use std::io;
use std::path::Path;
use std::path::PathBuf;

/// Where removed files go.
#[derive(Clone, Debug)]
pub struct Trash {
    files: PathBuf,
    /// The freedesktop layout keeps a `.trashinfo` file for each removed item.
    info: Option<PathBuf>,
}

impl Trash {
    /// The user's trash: `~/.Trash` on macOS, the freedesktop trash elsewhere on Unix.
    pub fn system() -> io::Result<Self> {
        let home = dirs::home_dir().ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "no home directory"))?;
        if cfg!(target_os = "macos") {
            return Ok(Self { files: home.join(".Trash"), info: None });
        }
        if cfg!(unix) {
            let data = std::env::var_os("XDG_DATA_HOME").map(PathBuf::from).unwrap_or_else(|| home.join(".local/share"));
            let root = data.join("Trash");
            return Ok(Self { files: root.join("files"), info: Some(root.join("info")) });
        }
        Err(io::Error::new(io::ErrorKind::Unsupported, "no trash on this system"))
    }

    /// A trash in a directory of one's own (tests).
    pub fn at(dir: &Path) -> Self {
        Self { files: dir.join("files"), info: None }
    }

    /// Moves `path` into the trash under a name not taken yet, and returns where it went.
    pub fn put(&self, path: &Path) -> io::Result<PathBuf> {
        std::fs::create_dir_all(&self.files)?;
        let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_else(|| "item".into());
        let mut target = self.files.join(&name);
        let mut n = 2;
        while target.exists() || target.symlink_metadata().is_ok() {
            target = self.files.join(format!("{name} {n}"));
            n += 1;
        }
        std::fs::rename(path, &target)?;
        if let Some(info) = &self.info {
            std::fs::create_dir_all(info)?;
            let file_name = target.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
            let date = chrono::Local::now().format("%Y-%m-%dT%H:%M:%S");
            let original = std::fs::canonicalize(path.parent().unwrap_or(Path::new("/")))
                .unwrap_or_else(|_| path.parent().unwrap_or(Path::new("/")).to_path_buf())
                .join(&name);
            std::fs::write(
                info.join(format!("{file_name}.trashinfo")),
                format!("[Trash Info]\nPath={}\nDeletionDate={date}\n", original.display()),
            )?;
        }
        Ok(target)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names_never_collide() {
        let dir = tempfile::tempdir().unwrap();
        let trash = Trash::at(&dir.path().join("trash"));
        for _ in 0..2 {
            let item = dir.path().join("lint");
            std::fs::create_dir_all(&item).unwrap();
            std::fs::write(item.join("SKILL.md"), "x").unwrap();
            trash.put(&item).unwrap();
            assert!(!item.exists());
        }
        assert!(dir.path().join("trash/files/lint/SKILL.md").exists());
        assert!(dir.path().join("trash/files/lint 2/SKILL.md").exists());
    }
}
