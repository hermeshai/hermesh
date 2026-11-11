//! Paths that need extra care. Writing a protected path asks in every mode except
//! bypass-permissions; deleting a critical path asks in every mode.

use her_tools::env::normalize_lexically;
use std::io::Read;
use std::path::Component;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;

use crate::config::SkillsSettings;
use crate::hooks::SessionView;
use crate::paths::HerHome;

/// Directories that configure the repository, editors, build tools or credentials, or hold
/// code that tools run on their own. `.aws`, `.kube` and `.ssh` configuration can name
/// programs to run; `.agents` and `.claude` hold instructions, skills, scripts and hooks for
/// coding agents.
const PROTECTED_DIRS: &[&str] = &[
    ".git",
    ".her",
    ".vscode",
    ".idea",
    ".husky",
    ".cargo",
    ".devcontainer",
    ".yarn",
    ".mvn",
    ".aws",
    ".kube",
    ".ssh",
    ".agents",
    ".claude",
];

/// Files that run code or change how tools behave, wherever they are.
const PROTECTED_FILES: &[&str] = &[
    ".gitconfig",
    ".gitmodules",
    ".bashrc",
    ".bash_profile",
    ".bash_login",
    ".bash_aliases",
    ".bash_logout",
    ".zshrc",
    ".zprofile",
    ".zshenv",
    ".zlogin",
    ".zlogout",
    ".profile",
    ".envrc",
    ".npmrc",
    ".yarnrc",
    ".yarnrc.yml",
    ".pnp.cjs",
    ".pnp.loader.mjs",
    ".pnpmfile.cjs",
    "bunfig.toml",
    ".bunfig.toml",
    ".bazelrc",
    ".bazelversion",
    ".bazeliskrc",
    ".pre-commit-config.yaml",
    "lefthook.yml",
    "lefthook.yaml",
    ".lefthook.yml",
    ".lefthook.yaml",
    "gradle-wrapper.properties",
    "maven-wrapper.properties",
    ".devcontainer.json",
    ".ripgreprc",
    "pyrightconfig.json",
    ".mcp.json",
];

/// What makes writing `path` (absolute and normalized) sensitive, e.g. `.git` or `.bashrc`.
/// Inside the workspace only the part below the workspace root counts.
pub(crate) fn protected(path: &Path, view: &SessionView) -> Option<String> {
    let path = canonical_lossy(path);
    // A session's own folder in the data directory holds the user's files, not her's data;
    // the rules below still apply inside it.
    let own = view.own_folder.as_ref().is_some_and(|own| path.starts_with(own));
    if path.starts_with(&view.data_dir) && !own {
        return Some("her data directory".into());
    }
    if view.skill_roots.iter().any(|root| path.starts_with(root)) {
        return Some("skills directory".into());
    }
    let roots: Vec<PathBuf> = view.env.workspace_roots().iter().map(|root| canonical_lossy(root)).collect();
    if roots
        .iter()
        .filter_map(|root| git_pointer_target(root))
        .any(|git_dir| path.starts_with(git_dir))
    {
        return Some("git directory".into());
    }
    let relative = roots
        .iter()
        .find_map(|root| path.strip_prefix(root).ok().map(Path::to_path_buf))
        .unwrap_or(path);
    protected_name(&relative)
}

/// Whether `path` (absolute and normalized) is in a bot's memory, `bots/<id>/memory` in the data
/// directory, and whether that memory is this session's own.
pub(crate) fn memory(path: &Path, view: &SessionView) -> Option<bool> {
    let path = canonical_lossy(path);
    if view.memory_dir.as_ref().is_some_and(|own| path.starts_with(own)) {
        return Some(true);
    }
    let mut below = path.strip_prefix(view.data_dir.join("bots")).ok()?.components();
    below.next()?;
    (below.next()?.as_os_str() == crate::bot_memory::DIR).then_some(false)
}

/// The protected name among the components of `path`, judged by the names alone.
pub(crate) fn protected_name(path: &Path) -> Option<String> {
    let names: Vec<&str> = path
        .components()
        .filter_map(|c| match c {
            Component::Normal(name) => name.to_str(),
            _ => None,
        })
        .collect();
    // A protected directory itself, or anything inside it.
    if let Some(dir) = names.iter().find(|name| PROTECTED_DIRS.contains(name)) {
        return Some((*dir).to_string());
    }
    if names.windows(2).any(|pair| pair == [".config", "git"]) {
        return Some(".config/git".into());
    }
    let file = names.last()?;
    PROTECTED_FILES.contains(file).then(|| (*file).to_string())
}

/// The git directory that a `.git` file in `root` points to with `gitdir: PATH`, as
/// worktrees, submodules and `git init --separate-git-dir` create.
fn git_pointer_target(root: &Path) -> Option<PathBuf> {
    let pointer = root.join(".git");
    if !pointer.is_file() {
        return None;
    }
    let mut text = String::new();
    std::fs::File::open(&pointer).ok()?.take(4096).read_to_string(&mut text).ok()?;
    let target = text.lines().find_map(|line| line.strip_prefix("gitdir:"))?.trim();
    Some(canonical_lossy(&normalize_lexically(&root.join(target))))
}

/// Why deleting `path` (absolute and normalized) would be a disaster: it is the file system
/// root, a top-level directory, the home directory, or the workspace, the working directory
/// or one of their ancestors.
pub(crate) fn critical(path: &Path, view: &SessionView) -> Option<&'static str> {
    let forms = [path.to_path_buf(), canonical_lossy(path)];
    let home = dirs::home_dir();
    let anchors: Vec<PathBuf> = view
        .env
        .workspace_roots()
        .iter()
        .map(PathBuf::as_path)
        .chain([view.env.cwd()])
        .flat_map(|dir| [dir.to_path_buf(), canonical_lossy(dir)])
        .collect();
    for form in &forms {
        let depth = form.components().filter(|c| matches!(c, Component::Normal(_))).count();
        if depth == 0 {
            return Some("the file system root");
        }
        if depth == 1 {
            return Some("a top-level directory");
        }
        if home.as_ref().is_some_and(|home| form == home || canonical_lossy(home) == *form) {
            return Some("the home directory");
        }
        if anchors.iter().any(|anchor| anchor.starts_with(form)) {
            return Some("the workspace or a directory that contains it");
        }
    }
    None
}

/// What skills add to the protected locations, for [`SessionView::skill_roots`]: the
/// configured paths. The other skill folders are inside `.agents`, `.claude` and the data
/// directory, which are protected already.
pub(crate) fn skill_locations(skills: &SkillsSettings, home: &HerHome, cwd: &Path) -> Arc<[PathBuf]> {
    if !skills.enabled {
        return Arc::from([]);
    }
    skills
        .paths
        .iter()
        .map(|path| canonical_lossy(&crate::skills::expand(path, home.user_home(), cwd)))
        .collect()
}

/// [`canonical_lossy`] of her's data directory, worked out once per directory.
pub(crate) fn canonical_data_dir(root: &Path) -> PathBuf {
    static CACHE: std::sync::OnceLock<parking_lot::Mutex<std::collections::HashMap<PathBuf, PathBuf>>> =
        std::sync::OnceLock::new();
    let cache = CACHE.get_or_init(Default::default);
    if let Some(dir) = cache.lock().get(root) {
        return dir.clone();
    }
    let dir = canonical_lossy(root);
    cache.lock().insert(root.to_path_buf(), dir.clone());
    dir
}

/// Links followed through dangling targets; a longer chain is treated as a loop.
const MAX_LINK_HOPS: usize = 40;

/// Canonical form of the deepest existing ancestor, with the remaining components appended. A
/// dangling symbolic link on the way is followed: writing through it creates its target, so the
/// target is the path that gets changed.
pub(crate) fn canonical_lossy(path: &Path) -> PathBuf {
    canonical_lossy_from(path, 0)
}

fn canonical_lossy_from(path: &Path, hops: usize) -> PathBuf {
    let mut existing = path;
    let mut rest = Vec::new();
    loop {
        if let Ok(canonical) = std::fs::canonicalize(existing) {
            let mut out = canonical;
            for part in rest.iter().rev() {
                out.push(part);
            }
            return out;
        }
        if hops < MAX_LINK_HOPS
            && std::fs::symlink_metadata(existing).is_ok_and(|meta| meta.file_type().is_symlink())
            && let Ok(target) = std::fs::read_link(existing)
        {
            // Relative to the directory holding the link; `..` is left to `canonicalize`.
            let mut next = existing.parent().map_or_else(|| target.clone(), |dir| dir.join(&target));
            for part in rest.iter().rev() {
                next.push(part);
            }
            return canonical_lossy_from(&next, hops + 1);
        }
        match (existing.parent(), existing.file_name()) {
            (Some(parent), Some(name)) => {
                rest.push(name.to_os_string());
                existing = parent;
            }
            _ => return path.to_path_buf(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::hooks::SessionView;
    use her_protocol::PermissionMode;
    use her_tools::LocalEnvironment;
    use std::sync::Arc;

    fn view_for(workspace: &Path, data_dir: &Path) -> SessionView {
        SessionView {
            permission_mode: PermissionMode::Manual,
            config_rules: Vec::new(),
            session_rules: Vec::new(),
            trusted: false,
            env: Arc::new(LocalEnvironment::new(workspace.to_path_buf())),
            data_dir: canonical_lossy(data_dir),
            own_folder: None,
            skill_roots: Arc::from([]),
            memory_dir: None,
            site_grants: Vec::new(),
            browser: Arc::default(),
            desktop: Default::default(),
        }
    }

    #[test]
    fn protected_names_anywhere() {
        let dir = tempfile::tempdir().unwrap();
        let workspace = dir.path().join("proj");
        std::fs::create_dir(&workspace).unwrap();
        let view = view_for(&workspace, &dir.path().join("her-home"));
        let check = |rel: &str| protected(&workspace.join(rel), &view);
        assert_eq!(check(".git/hooks/pre-commit").as_deref(), Some(".git"));
        assert_eq!(check(".git").as_deref(), Some(".git"));
        assert_eq!(check("vendor/lib/.git/config").as_deref(), Some(".git"));
        assert_eq!(check(".vscode/settings.json").as_deref(), Some(".vscode"));
        assert_eq!(check(".config/git/config").as_deref(), Some(".config/git"));
        assert_eq!(check("web/.npmrc").as_deref(), Some(".npmrc"));
        assert_eq!(check(".envrc").as_deref(), Some(".envrc"));
        assert_eq!(check(".aws/config").as_deref(), Some(".aws"));
        assert_eq!(check(".ssh/authorized_keys").as_deref(), Some(".ssh"));
        assert_eq!(check(".kube/config").as_deref(), Some(".kube"));
        assert_eq!(check(".agents/skills/x/SKILL.md").as_deref(), Some(".agents"));
        assert_eq!(check(".claude/skills/x/SKILL.md").as_deref(), Some(".claude"));
        assert_eq!(check(".claude/settings.json").as_deref(), Some(".claude"));
        assert_eq!(check("src/main.rs"), None);
        assert_eq!(check(".gitignore"), None);
        assert_eq!(check(".config/nvim/init.lua"), None);
        assert_eq!(check("docs/aws/config"), None);
        assert_eq!(
            protected(&dir.path().join("her-home/config.toml"), &view).as_deref(),
            Some("her data directory")
        );
        // Only the part below the workspace root counts.
        let nested = dir.path().join(".cargo").join("proj");
        std::fs::create_dir_all(&nested).unwrap();
        let inside_cargo = view_for(&nested, &dir.path().join("her-home"));
        assert_eq!(protected(&nested.join("src/lib.rs"), &inside_cargo), None);
    }

    /// A session without a project, or a bot's, works in its own folder in the data directory:
    /// writing there is not writing her's data (bot-design §2.5).
    #[test]
    fn a_sessions_own_folder_in_the_data_directory_is_writable() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(canonical_lossy(&dir.path().join("her-home")));
        let bot = home.bot_files_dir("b1");
        let chat = home.new_chat_dir().unwrap();
        std::fs::create_dir_all(&bot).unwrap();
        for own in [&bot, &chat] {
            let own = canonical_lossy(own);
            let mut view = view_for(&own, home.root());
            view.own_folder = home.private_folder(&own);
            assert_eq!(protected(&own.join("report.xlsx"), &view), None);
            assert_eq!(protected(&own.join("notes/a.md"), &view), None);
            // Names stay protected inside it.
            assert_eq!(protected(&own.join(".git/config"), &view).as_deref(), Some(".git"));
            assert_eq!(protected(&own.join(".bashrc"), &view).as_deref(), Some(".bashrc"));
            // The rest of the data directory does too.
            for other in [home.config_file(), home.bot_dir("b1").join("bot.json"), home.bot_files_dir("b2").join("x")] {
                assert_eq!(protected(&other, &view).as_deref(), Some("her data directory"), "{}", other.display());
            }
        }
        // Without its own folder, a session in the data directory protects all of it.
        let own = canonical_lossy(&bot);
        let view = view_for(&own, home.root());
        assert_eq!(protected(&own.join("report.xlsx"), &view).as_deref(), Some("her data directory"));
    }

    #[test]
    fn git_pointer_files_protect_their_target() {
        let dir = tempfile::tempdir().unwrap();
        let workspace = dir.path().join("proj");
        std::fs::create_dir_all(workspace.join("store/hooks")).unwrap();
        std::fs::write(workspace.join(".git"), "gitdir: store\n").unwrap();
        let view = view_for(&workspace, &dir.path().join("her-home"));
        assert_eq!(protected(&workspace.join("store/hooks/pre-commit"), &view).as_deref(), Some("git directory"));
        assert_eq!(protected(&workspace.join(".git"), &view).as_deref(), Some(".git"));
        assert_eq!(protected(&workspace.join("src/lib.rs"), &view), None);

        let elsewhere = dir.path().join("elsewhere");
        std::fs::write(workspace.join(".git"), format!("gitdir: {}\n", elsewhere.display())).unwrap();
        assert_eq!(protected(&elsewhere.join("config"), &view).as_deref(), Some("git directory"));
        assert_eq!(protected(&workspace.join("store/hooks/pre-commit"), &view), None);
    }

    #[test]
    fn names_alone() {
        assert_eq!(protected_name(Path::new("a/.git/config")).as_deref(), Some(".git"));
        assert_eq!(protected_name(Path::new(".bashrc")).as_deref(), Some(".bashrc"));
        assert_eq!(protected_name(Path::new("../x/.npmrc")).as_deref(), Some(".npmrc"));
        assert_eq!(protected_name(Path::new("src/main.rs")), None);
        assert_eq!(protected_name(Path::new("")), None);
    }

    #[test]
    fn skill_locations_are_protected() {
        let dir = tempfile::tempdir().unwrap();
        let workspace = dir.path().join("proj");
        std::fs::create_dir_all(&workspace).unwrap();
        let shared = dir.path().join("shared-skills");
        std::fs::create_dir_all(&shared).unwrap();
        let settings = SkillsSettings {
            paths: vec![shared.display().to_string()],
            ..SkillsSettings::default()
        };
        let home = HerHome::new(dir.path().join("her-home"));
        let skill_roots = skill_locations(&settings, &home, &workspace);
        let view = SessionView {
            skill_roots,
            ..view_for(&workspace, &dir.path().join("her-home"))
        };
        assert_eq!(protected(&shared.join("x/SKILL.md"), &view).as_deref(), Some("skills directory"));
        assert_eq!(protected(&workspace.join(".claude/skills/x/SKILL.md"), &view).as_deref(), Some(".claude"));
        assert_eq!(protected(&workspace.join("src/lib.rs"), &view), None);
        let off = SkillsSettings {
            enabled: false,
            ..settings
        };
        assert!(skill_locations(&off, &home, &workspace).is_empty());
    }

    #[test]
    fn critical_paths() {
        let dir = tempfile::tempdir().unwrap();
        let workspace = dir.path().join("a").join("proj");
        std::fs::create_dir_all(workspace.join("build")).unwrap();
        let view = view_for(&workspace, &dir.path().join("her-home"));
        assert_eq!(critical(Path::new("/"), &view), Some("the file system root"));
        assert_eq!(critical(Path::new("/usr"), &view), Some("a top-level directory"));
        if let Some(home) = dirs::home_dir() {
            assert_eq!(critical(&home, &view), Some("the home directory"));
        }
        assert!(critical(&workspace, &view).is_some());
        assert!(critical(&dir.path().join("a"), &view).is_some());
        assert!(critical(dir.path(), &view).is_some());
        assert_eq!(critical(&workspace.join("build"), &view), None);
        assert_eq!(critical(&workspace.join("missing/x"), &view), None);
        assert_eq!(critical(Path::new("/usr/local/lib/x"), &view), None);
    }
}
