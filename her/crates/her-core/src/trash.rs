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
    /// Windows' Recycle Bin, which the shell fills: it is no folder to move files into.
    #[cfg_attr(not(windows), allow(dead_code))]
    recycle_bin: bool,
}

impl Trash {
    /// The user's trash: `~/.Trash` on macOS, the Recycle Bin on Windows, the freedesktop trash
    /// elsewhere.
    pub fn system() -> io::Result<Self> {
        if cfg!(windows) {
            return Ok(Self { files: PathBuf::new(), info: None, recycle_bin: true });
        }
        let home = std::env::home_dir().ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "no home directory"))?;
        if cfg!(target_os = "macos") {
            return Ok(Self { files: home.join(".Trash"), info: None, recycle_bin: false });
        }
        let data = std::env::var_os("XDG_DATA_HOME").map(PathBuf::from).unwrap_or_else(|| home.join(".local/share"));
        let root = data.join("Trash");
        Ok(Self { files: root.join("files"), info: Some(root.join("info")), recycle_bin: false })
    }

    /// A trash in a directory of one's own (tests).
    pub fn at(dir: &Path) -> Self {
        Self { files: dir.join("files"), info: None, recycle_bin: false }
    }

    /// Moves `path` into the trash under a name not taken yet, and returns where it went (the
    /// path itself for the Recycle Bin, which names no place).
    pub fn put(&self, path: &Path) -> io::Result<PathBuf> {
        #[cfg(windows)]
        if self.recycle_bin {
            recycle(path)?;
            return Ok(path.to_path_buf());
        }
        std::fs::create_dir_all(&self.files)?;
        let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_else(|| "item".into());
        let mut target = self.files.join(&name);
        let mut n = 2;
        while target.exists() || target.symlink_metadata().is_ok() {
            target = self.files.join(format!("{name} {n}"));
            n += 1;
        }
        her_tools::files::rename(path, &target)?;
        if let Some(info) = &self.info {
            std::fs::create_dir_all(info)?;
            let file_name = target.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
            let date = chrono::Local::now().format("%Y-%m-%dT%H:%M:%S");
            let original = dunce::canonicalize(path.parent().unwrap_or(Path::new("/")))
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

/// Sends `path` to the Recycle Bin, as Explorer's Delete does, asking and showing nothing.
#[cfg(windows)]
fn recycle(path: &Path) -> io::Result<()> {
    use std::os::windows::ffi::OsStrExt;
    use windows_sys::Win32::UI::Shell::FO_DELETE;
    use windows_sys::Win32::UI::Shell::FOF_ALLOWUNDO;
    use windows_sys::Win32::UI::Shell::FOF_NOCONFIRMATION;
    use windows_sys::Win32::UI::Shell::FOF_NOERRORUI;
    use windows_sys::Win32::UI::Shell::FOF_SILENT;
    use windows_sys::Win32::UI::Shell::SHFILEOPSTRUCTW;
    use windows_sys::Win32::UI::Shell::SHFileOperationW;

    // The shell takes a list of full paths, each ended by a NUL and the list by one more.
    let from: Vec<u16> = std::path::absolute(path)?.as_os_str().encode_wide().chain([0, 0]).collect();
    // SAFETY: zeroes are a valid value of this plain C struct.
    let mut operation: SHFILEOPSTRUCTW = unsafe { std::mem::zeroed() };
    operation.wFunc = FO_DELETE;
    operation.pFrom = from.as_ptr();
    operation.fFlags = (FOF_ALLOWUNDO | FOF_NOCONFIRMATION | FOF_NOERRORUI | FOF_SILENT) as u16;
    // SAFETY: `operation` is filled in as the call takes it, and `from` outlives the call.
    let status = unsafe { SHFileOperationW(&mut operation) };
    if status != 0 || operation.fAnyOperationsAborted != 0 {
        return Err(io::Error::other(format!("the Recycle Bin did not take {} (code {status:#x})", path.display())));
    }
    Ok(())
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

/// The Recycle Bin takes a folder, which is then gone from where it was.
#[cfg(all(test, windows))]
mod windows_tests {
    use super::*;

    #[test]
    fn the_recycle_bin_takes_a_folder() {
        let dir = tempfile::tempdir().unwrap();
        let item = dir.path().join("lint 技能");
        std::fs::create_dir_all(&item).unwrap();
        std::fs::write(item.join("SKILL.md"), "x").unwrap();
        Trash::system().unwrap().put(&item).unwrap();
        assert!(!item.exists());
    }
}
