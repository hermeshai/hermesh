//! Project root detection, instruction files (AGENTS.md) and project trust.

use serde::Deserialize;
use serde::Serialize;
use std::io;
use std::path::Path;
use std::path::PathBuf;

use crate::paths::HerHome;

const MAX_INSTRUCTIONS_BYTES: usize = 32 * 1024;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct InstructionFile {
    pub path: PathBuf,
    /// From the user's data directory rather than the project.
    pub global: bool,
    pub content: String,
}

/// Nearest ancestor of `cwd` (inclusive) that contains one of `markers`.
pub fn find_project_root(cwd: &Path, markers: &[String]) -> Option<PathBuf> {
    cwd.ancestors()
        .find(|dir| markers.iter().any(|marker| dir.join(marker).exists()))
        .map(Path::to_path_buf)
}

/// Global instructions first, then one file per directory from the project root down to
/// `cwd`. The total is capped at 32 KB.
pub fn discover(
    home: &HerHome,
    cwd: &Path,
    project_root: Option<&Path>,
    names: &[String],
) -> (Vec<InstructionFile>, Vec<String>) {
    let mut files = Vec::new();
    let mut warnings = Vec::new();
    if let Ok(content) = std::fs::read_to_string(home.agents_md()) {
        files.push(InstructionFile {
            path: home.agents_md(),
            global: true,
            content,
        });
    }
    let dirs: Vec<PathBuf> = match project_root {
        Some(root) if cwd.starts_with(root) => {
            let mut chain: Vec<PathBuf> = cwd
                .ancestors()
                .take_while(|dir| dir.starts_with(root))
                .map(Path::to_path_buf)
                .collect();
            chain.reverse();
            chain
        }
        _ => vec![cwd.to_path_buf()],
    };
    for dir in dirs {
        for name in names {
            let path = dir.join(name);
            if let Ok(content) = std::fs::read_to_string(&path) {
                files.push(InstructionFile {
                    path,
                    global: false,
                    content,
                });
                break;
            }
        }
    }
    let mut budget = MAX_INSTRUCTIONS_BYTES;
    for file in &mut files {
        if file.content.len() > budget {
            let mut end = budget;
            while end > 0 && !file.content.is_char_boundary(end) {
                end -= 1;
            }
            file.content.truncate(end);
            file.content.push_str("\n[Instructions truncated: the 32 KB limit was reached.]");
            warnings.push(format!("instruction files exceed 32 KB; {} was truncated", file.path.display()));
            budget = 0;
        } else {
            budget -= file.content.len();
        }
    }
    files.retain(|f| !f.content.trim().is_empty());
    (files, warnings)
}

#[derive(Debug, Default, Serialize, Deserialize)]
struct TrustedFile {
    #[serde(default)]
    paths: Vec<PathBuf>,
    /// Asked and answered no: not asked again.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    declined: Vec<PathBuf>,
}

fn canonical(path: &Path) -> PathBuf {
    std::fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf())
}

pub fn is_trusted(home: &HerHome, project_dir: &Path) -> bool {
    let Ok(text) = std::fs::read_to_string(home.trusted_file()) else {
        return false;
    };
    let Ok(file) = toml::from_str::<TrustedFile>(&text) else {
        return false;
    };
    let target = canonical(project_dir);
    file.paths.iter().any(|p| canonical(p) == target)
}

/// Whether the person answered no when asked to trust this directory.
pub fn was_declined(home: &HerHome, project_dir: &Path) -> bool {
    let Some(file) = std::fs::read_to_string(home.trusted_file()).ok().and_then(|text| toml::from_str::<TrustedFile>(&text).ok()) else {
        return false;
    };
    let target = canonical(project_dir);
    file.declined.iter().any(|p| canonical(p) == target)
}

/// Remembers that the person does not want to trust this directory.
pub fn decline(home: &HerHome, project_dir: &Path) -> io::Result<()> {
    let path = home.trusted_file();
    let mut file: TrustedFile = std::fs::read_to_string(&path).ok().and_then(|text| toml::from_str(&text).ok()).unwrap_or_default();
    file.declined.push(canonical(project_dir));
    std::fs::create_dir_all(home.root())?;
    std::fs::write(&path, toml::to_string(&file).map_err(io::Error::other)?)
}

/// Stops trusting a directory. Returns false when it was not trusted.
pub fn untrust(home: &HerHome, project_dir: &Path) -> io::Result<bool> {
    let path = home.trusted_file();
    let Some(mut file) = std::fs::read_to_string(&path).ok().and_then(|text| toml::from_str::<TrustedFile>(&text).ok()) else {
        return Ok(false);
    };
    let target = canonical(project_dir);
    let before = file.paths.len();
    file.paths.retain(|p| canonical(p) != target);
    if file.paths.len() == before {
        return Ok(false);
    }
    std::fs::write(&path, toml::to_string(&file).map_err(io::Error::other)?)?;
    Ok(true)
}

/// Marks a directory as trusted. Returns false when it already was.
pub fn trust(home: &HerHome, project_dir: &Path) -> io::Result<bool> {
    let path = home.trusted_file();
    let mut file: TrustedFile = std::fs::read_to_string(&path)
        .ok()
        .and_then(|text| toml::from_str(&text).ok())
        .unwrap_or_default();
    let target = canonical(project_dir);
    if file.paths.iter().any(|p| canonical(p) == target) {
        return Ok(false);
    }
    file.declined.retain(|p| canonical(p) != target);
    file.paths.push(target);
    let text = toml::to_string(&file).map_err(io::Error::other)?;
    std::fs::create_dir_all(home.root())?;
    std::fs::write(&path, text)?;
    Ok(true)
}

/// Branch name of a git checkout, read from `.git/HEAD` without running git.
pub fn git_branch(project_root: &Path) -> Option<String> {
    let head = std::fs::read_to_string(project_root.join(".git").join("HEAD")).ok()?;
    head.trim().strip_prefix("ref: refs/heads/").map(str::to_string)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn discovers_files_from_root_to_cwd() {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path().join("repo");
        let sub = root.join("crates/app");
        std::fs::create_dir_all(&sub).unwrap();
        std::fs::create_dir_all(root.join(".git")).unwrap();
        std::fs::write(root.join("AGENTS.md"), "root rules").unwrap();
        std::fs::write(sub.join("AGENTS.md"), "app rules").unwrap();
        let home = HerHome::new(dir.path().join("home"));
        std::fs::create_dir_all(home.root()).unwrap();
        std::fs::write(home.agents_md(), "global rules").unwrap();

        let markers = vec![".git".to_string()];
        let project_root = find_project_root(&sub, &markers).unwrap();
        assert_eq!(project_root, root);
        let (files, warnings) = discover(&home, &sub, Some(&project_root), &["AGENTS.md".to_string()]);
        let contents: Vec<&str> = files.iter().map(|f| f.content.as_str()).collect();
        assert_eq!(contents, vec!["global rules", "root rules", "app rules"]);
        assert!(files[0].global);
        assert!(warnings.is_empty());
    }

    #[test]
    fn trust_round_trip() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        let project = dir.path().join("p");
        std::fs::create_dir_all(&project).unwrap();
        assert!(!is_trusted(&home, &project));
        assert!(trust(&home, &project).unwrap());
        assert!(!trust(&home, &project).unwrap());
        assert!(is_trusted(&home, &project));
    }
}
