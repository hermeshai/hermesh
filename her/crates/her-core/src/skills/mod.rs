//! Skills: directories with a `SKILL.md` that teach the model one kind of task. They are
//! found when a session starts, in the project's `.agents/skills` (and `.claude/skills`, off
//! by default), the user's `~/.her/skills` and configured paths, and listed in the system
//! prompt; the model loads one with read (docs/kernel/skill-design.md).

mod catalog;
mod frontmatter;
pub mod install;
pub(crate) mod kept;
pub mod system;

pub use catalog::Catalog;
pub use catalog::Reader;
pub use catalog::catalog;
pub use catalog::default_budget;
pub(crate) use catalog::section_in as catalog_in;
pub(crate) use catalog::escape;
pub(crate) use frontmatter::body;

pub use her_protocol::api::SkillEntry;
pub use her_protocol::api::SkillScope;
pub use her_protocol::api::SkillState;

use her_protocol::api::CatalogInfo;
use her_protocol::api::SkillsListResult;
use her_tools::env::normalize_lexically;
use serde::Deserialize;
use serde::Serialize;
use std::collections::HashMap;
use std::collections::HashSet;
use std::fs::Metadata;
use std::io;
use std::io::Read;
use std::path::Path;
use std::path::PathBuf;

use crate::config::SkillsSettings;
use crate::paths::HerHome;

pub const SKILL_FILE: &str = "SKILL.md";
/// Longest description kept, as the open specification allows.
pub const MAX_DESCRIPTION_CHARS: usize = 1024;
const MAX_NAME_CHARS: usize = 64;
/// How deep below a root the search goes, and how many directories it visits at most.
const MAX_DEPTH: usize = 6;
const MAX_DIRS: usize = 2000;
/// Only the header is read at start-up; it sits at the top of the file.
const MAX_HEADER_BYTES: u64 = 64 * 1024;
/// Threads that read skill files at once, and the fewest directories worth a thread each.
const PARALLEL_READERS: usize = 4;
const DIRS_PER_READER: usize = 8;

/// A skill of a session, as its system_prompt entry records it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SkillRef {
    pub name: String,
    pub path: PathBuf,
    pub scope: SkillScope,
    /// False when only the user may invoke it; then it is not in the catalog.
    pub model_invocable: bool,
    /// What to type after `/name`, from the header's `argument-hint`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub argument_hint: Option<String>,
}

impl From<&SkillEntry> for SkillRef {
    fn from(entry: &SkillEntry) -> Self {
        SkillRef {
            name: entry.name.clone(),
            path: entry.path.clone(),
            scope: entry.scope,
            model_invocable: entry.model_invocable,
            argument_hint: entry.argument_hint.clone(),
        }
    }
}

#[derive(Clone, Debug, Default)]
pub struct SkillSet {
    /// Every `SKILL.md` found, in priority order.
    pub entries: Vec<SkillEntry>,
    /// Problems the user should fix; a session reports them when it starts.
    pub warnings: Vec<String>,
    /// Other apps' folders that hold skills but were not read (`skills.other_apps` is off).
    pub not_read: Vec<PathBuf>,
}

impl SkillSet {
    pub fn active(&self) -> impl Iterator<Item = &SkillEntry> {
        self.entries.iter().filter(|e| e.state == SkillState::Active)
    }

    pub fn references(&self) -> Vec<SkillRef> {
        self.active().map(SkillRef::from).collect()
    }
}

/// What a session started in a directory finds, and the catalog it lists.
pub struct Scan {
    pub set: SkillSet,
    pub catalog: Catalog,
    pub enabled: bool,
}

impl Scan {
    /// `tools` are the session's tools: the catalog says how to read a skill with them.
    /// `context_window` sets the budget when the configuration does not.
    pub fn new(sources: &Sources<'_>, tools: &[String], context_window: u64) -> Self {
        let set = discover(sources);
        let budget = match sources.settings.max_catalog_tokens {
            0 => default_budget(context_window),
            tokens => tokens,
        };
        let active: Vec<&SkillEntry> = set.active().collect();
        let catalog = catalog(&active, Reader::for_tools(tools), budget);
        Self {
            set,
            catalog,
            enabled: sources.settings.enabled,
        }
    }

    /// Problems with the skills, then with the catalog's budget.
    pub fn warnings(&self) -> Vec<String> {
        self.set.warnings.iter().cloned().chain(self.catalog.warnings()).collect()
    }

    /// The report of `skills/list` and `her skills --json`; `all` keeps the skills that are
    /// not used (shadowed, disabled or broken). `commands` is what `/` would offer in a
    /// session started here.
    pub fn report(&self, all: bool) -> SkillsListResult {
        let catalog = &self.catalog;
        SkillsListResult {
            skills: self
                .set
                .entries
                .iter()
                .filter(|e| all || e.state == SkillState::Active)
                .cloned()
                .collect(),
            commands: crate::commands::available(&self.set.entries),
            catalog: Some(CatalogInfo {
                listed: catalog.listed,
                tokens: catalog.tokens,
                budget: catalog.budget,
                shortened_to: catalog.shortened_to,
                left_out: catalog.left_out.clone(),
            }),
            enabled: self.enabled,
            warnings: self.warnings(),
            not_read: self.set.not_read.clone(),
        }
    }
}

/// The report of `skills/list` for a session: its skills (from `Kernel::skills`) described
/// from their files now, and the commands. Reads the files.
pub fn session_report(skills: &[SkillRef], enabled: bool) -> SkillsListResult {
    let skills = describe(skills);
    SkillsListResult {
        commands: crate::commands::available(&skills),
        skills,
        catalog: None,
        enabled,
        warnings: Vec::new(),
        not_read: Vec::new(),
    }
}

/// A session's skills described from their files now, for a `/` menu: what `/name` would
/// expand. The name and who may invoke it stay as the session started; a file that cannot be
/// read any more makes its skill invalid.
pub fn describe(skills: &[SkillRef]) -> Vec<SkillEntry> {
    skills
        .iter()
        .map(|skill| {
            let header = std::fs::File::open(&skill.path)
                .map_err(|e| format!("cannot read the file: {e}"))
                .and_then(read_header)
                .and_then(|text| frontmatter::parse(&text));
            SkillEntry {
                name: skill.name.clone(),
                model_invocable: skill.model_invocable,
                ..load(skill.path.clone(), header, skill.scope)
            }
        })
        .collect()
}

/// Where to look for skills.
pub struct Sources<'a> {
    pub cwd: &'a Path,
    pub project_root: Option<&'a Path>,
    /// `HER_HOME`: its `skills` directory is the user layer.
    pub her_home: &'a Path,
    /// The user's home directory: never a project directory, and where `~/.agents/skills`
    /// and `~/.claude/skills` are when `skills.other_apps` is on.
    pub user_home: Option<&'a Path>,
    pub settings: &'a SkillsSettings,
    /// Include the skills built into her, after every other root.
    pub system: bool,
}

impl<'a> Sources<'a> {
    pub fn new(
        home: &'a HerHome,
        cwd: &'a Path,
        project_root: Option<&'a Path>,
        settings: &'a SkillsSettings,
    ) -> Self {
        Self {
            cwd,
            project_root,
            her_home: home.root(),
            user_home: home.user_home(),
            settings,
            system: true,
        }
    }
}

/// Finds the skills of a session that would start in `sources.cwd`: the project layer from
/// the working directory up to the project root, then the user layer, then configured
/// paths. The first skill of a name that is on wins (docs/kernel/skill-design.md §4).
pub fn discover(sources: &Sources<'_>) -> SkillSet {
    let mut set = SkillSet::default();
    if !sources.settings.enabled {
        return set;
    }
    if sources.system {
        system::ensure(sources.her_home);
    }
    let disabled = PathsAndNames::new(&sources.settings.disabled, sources);
    let opt_in = PathsAndNames::new(&sources.settings.opt_in, sources);
    set.not_read = not_read(sources);
    let user_skills = sources.her_home.join("skills");
    let mut seen_roots = HashSet::new();
    let mut seen_files = HashSet::new();
    let mut winners: HashMap<String, PathBuf> = HashMap::new();
    for root in roots(sources) {
        let meta = match root.meta {
            Ok(meta) => meta,
            Err(err) if matches!(err.kind(), io::ErrorKind::NotFound | io::ErrorKind::NotADirectory) => continue,
            Err(err) => {
                set.warnings.push(format!("cannot read skills directory {}: {err}", root.path.display()));
                continue;
            }
        };
        if !seen_roots.insert(file_id(&meta, &root.path)) {
            continue;
        }
        for found in walk(&root.path, &meta, root.scope, &mut set.warnings) {
            // The same file reached through another root (a link, or cwd being the home).
            if !seen_files.insert(found.id) {
                continue;
            }
            let mut entry = found.entry;
            entry.default_enabled = root.default_enabled;
            // Installed by her in the user's own folder: the page may remove it, and it may
            // say where it came from.
            if entry.scope == SkillScope::User && entry.path.starts_with(&user_skills) {
                entry.removable = true;
                entry.source = install::read_source(entry.dir());
            }
            if entry.state == SkillState::Invalid {
                set.warnings.push(format!(
                    "skill at {} was not loaded: {}",
                    entry.path.display(),
                    entry.detail.as_deref().unwrap_or_default()
                ));
            } else if disabled.matches(&entry) || !(entry.default_enabled || opt_in.matches(&entry)) {
                // Off, so it does not shadow another skill of its name (design D-174).
                entry.state = SkillState::Disabled;
            } else if let Some(winner) = winners.get(&entry.name) {
                entry.state = SkillState::Shadowed;
                entry.detail = Some(winner.display().to_string());
            } else {
                winners.insert(entry.name.clone(), entry.path.clone());
            }
            set.entries.push(entry);
        }
    }
    set
}

/// Whether an entry of `skills.disabled` (or `skills.paths`) is a path rather than a name.
pub fn is_path(entry: &str) -> bool {
    entry.starts_with('~') || entry.contains('/') || entry.contains(std::path::MAIN_SEPARATOR)
}

/// `~/x` against the user's home and other relative paths against `base`.
pub fn expand(path: &str, user_home: Option<&Path>, base: &Path) -> PathBuf {
    let path = match (path.strip_prefix('~'), user_home) {
        (Some(""), Some(home)) => home.to_path_buf(),
        (Some(rest), Some(home)) if rest.starts_with(['/', std::path::MAIN_SEPARATOR]) => {
            home.join(rest.trim_start_matches(['/', std::path::MAIN_SEPARATOR]))
        }
        _ => base.join(path),
    };
    normalize_lexically(&path)
}

/// The folders of other apps in a project and in the home directory, in priority order:
/// `.agents` is the open specification's shared place, `.claude` another app's (design §4.1).
const SHARED_DIR: &str = ".agents";
const OTHER_APP_DIR: &str = ".claude";

struct Root {
    path: PathBuf,
    scope: SkillScope,
    /// Whether its skills are on before the user turns them on or off (design D-173).
    default_enabled: bool,
    meta: io::Result<Metadata>,
}

impl Root {
    fn new(path: PathBuf, scope: SkillScope, default_enabled: bool) -> Self {
        let meta = std::fs::metadata(&path);
        Self {
            path,
            scope,
            default_enabled,
            meta,
        }
    }
}

fn roots(sources: &Sources<'_>) -> Vec<Root> {
    let mut user = vec![Root::new(sources.her_home.join("skills"), SkillScope::User, true)];
    if sources.settings.other_apps
        && let Some(home) = sources.user_home
    {
        for name in [SHARED_DIR, OTHER_APP_DIR] {
            user.push(Root::new(home.join(name).join("skills"), SkillScope::User, true));
        }
    }
    let user_ids: HashSet<_> = user
        .iter()
        .filter_map(|root| root.meta.as_ref().ok().map(|meta| file_id(meta, &root.path)))
        .collect();
    let mut roots = Vec::new();
    for dir in project_dirs(sources.cwd, sources.project_root, sources.user_home) {
        for (name, default_enabled) in [(SHARED_DIR, true), (OTHER_APP_DIR, false)] {
            let root = Root::new(dir.join(name).join("skills"), SkillScope::Project, default_enabled);
            // A user root met on the way up (a link to one) stays a user root.
            let is_user = root.meta.as_ref().is_ok_and(|meta| user_ids.contains(&file_id(meta, &root.path)));
            if !is_user {
                roots.push(root);
            }
        }
    }
    roots.extend(user);
    roots.extend(
        sources
            .settings
            .paths
            .iter()
            .map(|path| Root::new(expand(path, sources.user_home, sources.cwd), SkillScope::Extra, true)),
    );
    if sources.system {
        roots.push(Root::new(system::dir(sources.her_home), SkillScope::System, true));
    }
    roots
}

/// The working directory and its parents up to the project root, nearest first; only the
/// working directory when there is no project. Never the home directory: its `.agents` and
/// `.claude` are other apps' folders, read only with `skills.other_apps` (design D-172).
fn project_dirs(cwd: &Path, project_root: Option<&Path>, home: Option<&Path>) -> Vec<PathBuf> {
    let dirs: Vec<PathBuf> = match project_root {
        Some(root) if cwd.starts_with(root) => cwd
            .ancestors()
            .take_while(|dir| dir.starts_with(root))
            .map(Path::to_path_buf)
            .collect(),
        _ => vec![cwd.to_path_buf()],
    };
    let home = home.map(|home| std::fs::canonicalize(home).unwrap_or_else(|_| home.to_path_buf()));
    dirs.into_iter()
        .filter(|dir| home.as_ref().is_none_or(|home| std::fs::canonicalize(dir).as_ref().unwrap_or(dir) != home))
        .collect()
}

/// Whether the skill in `dir` is on by default, as its root says in [`discover`]: false only
/// inside a project's `.claude/skills` (not the home's, which `skills.other_apps` reads as on).
pub fn on_by_default(dir: &Path, user_home: Option<&Path>) -> bool {
    let home = user_home.map(|home| std::fs::canonicalize(home).unwrap_or_else(|_| home.to_path_buf()));
    !dir.ancestors().any(|root| {
        root.file_name().is_some_and(|name| name == "skills")
            && root.parent().is_some_and(|app| app.file_name().is_some_and(|name| name == OTHER_APP_DIR))
            && root.parent().and_then(Path::parent).is_some_and(|project| {
                home.as_ref().is_none_or(|home| std::fs::canonicalize(project).as_ref().unwrap_or(&project.to_path_buf()) != home)
            })
    })
}

/// Other apps' skill folders in the home directory that hold something but are not read,
/// for the note under `her skills` (design D-177). One directory read each.
fn not_read(sources: &Sources<'_>) -> Vec<PathBuf> {
    let Some(home) = sources.user_home.filter(|_| !sources.settings.other_apps) else {
        return Vec::new();
    };
    [SHARED_DIR, OTHER_APP_DIR]
        .into_iter()
        .map(|name| home.join(name).join("skills"))
        .filter(|root| {
            std::fs::read_dir(root).is_ok_and(|mut entries| {
                entries.any(|entry| {
                    entry.is_ok_and(|entry| {
                        !entry.file_name().to_string_lossy().starts_with('.')
                            && std::fs::metadata(entry.path()).is_ok_and(|meta| meta.is_dir())
                    })
                })
            })
        })
        .collect()
}

/// A `SKILL.md` that the walk found, loaded.
struct Found {
    id: FileId,
    entry: SkillEntry,
}

/// The skills under `root`, sorted by path. A directory with a `SKILL.md` is a skill and is
/// not searched further; hidden directories and `node_modules` are skipped. Every directory
/// costs one failed `open`; a skill costs an `open`, a `stat` and one `read`.
fn walk(root: &Path, meta: &Metadata, scope: SkillScope, warnings: &mut Vec<String>) -> Vec<Found> {
    if meta.is_file() {
        // A configured path may name a SKILL.md itself.
        return match root.file_name() {
            Some(name) if name == SKILL_FILE => open_skill(root, scope).into_iter().collect(),
            _ => Vec::new(),
        };
    }
    let mut found = Vec::new();
    let mut seen = HashSet::from([file_id(meta, root)]);
    let mut level = vec![root.to_path_buf()];
    let mut visited = 0usize;
    // Level by level, so that the skills of one level are read together.
    for depth in 0..=MAX_DEPTH {
        if level.is_empty() {
            break;
        }
        let full = visited + level.len() > MAX_DIRS;
        if full {
            level.truncate(MAX_DIRS - visited);
            warnings.push(format!(
                "stopped scanning {} after {MAX_DIRS} directories; some skills may be missing",
                root.display()
            ));
        }
        visited += level.len();
        let mut next = Vec::new();
        for (dir, skill) in level.iter().zip(open_skills(&level, scope)) {
            if let Some(skill) = skill {
                found.push(skill);
                continue;
            }
            if depth == MAX_DEPTH || full {
                continue;
            }
            let entries = match std::fs::read_dir(dir) {
                Ok(entries) => entries,
                Err(err) if depth == 0 => {
                    warnings.push(format!("cannot read skills directory {}: {err}", root.display()));
                    continue;
                }
                Err(_) => continue,
            };
            for entry in entries.flatten() {
                let name = entry.file_name();
                if name.to_str().is_none_or(|name| name.starts_with('.') || name == "node_modules") {
                    continue;
                }
                let Ok(kind) = entry.file_type() else { continue };
                // Links are followed; the identity check stops cycles.
                let id = if kind.is_dir() {
                    entry_id(&entry, meta)
                } else if kind.is_symlink() {
                    match std::fs::metadata(entry.path()) {
                        Ok(target) if target.is_dir() => Some(file_id(&target, &entry.path())),
                        _ => None,
                    }
                } else {
                    None
                };
                if let Some(id) = id
                    && seen.insert(id)
                {
                    next.push(entry.path());
                }
            }
        }
        level = next;
    }
    found.sort_by(|a, b| a.entry.path.cmp(&b.entry.path));
    found
}

/// The skill in each of `dirs`, if any. Many directories are read on a few threads: opening,
/// reading and parsing the files is what discovery spends its time on.
fn open_skills(dirs: &[PathBuf], scope: SkillScope) -> Vec<Option<Found>> {
    let probe = |dirs: &[PathBuf]| {
        dirs.iter()
            .map(|dir| open_skill(&dir.join(SKILL_FILE), scope))
            .collect::<Vec<_>>()
    };
    // Small levels cannot use two readers anyway. On Linux, available_parallelism reads
    // cgroup quotas up to the root; do not repeat those probes for every small directory.
    if dirs.len() < 2 * DIRS_PER_READER {
        return probe(dirs);
    }
    let threads = std::thread::available_parallelism()
        .map_or(1, usize::from)
        .min(PARALLEL_READERS)
        .min(dirs.len() / DIRS_PER_READER);
    if threads < 2 {
        return probe(dirs);
    }
    std::thread::scope(|scope| {
        let readers: Vec<_> = dirs
            .chunks(dirs.len().div_ceil(threads))
            .map(|part| scope.spawn(move || probe(part)))
            .collect();
        readers
            .into_iter()
            .zip(dirs.chunks(dirs.len().div_ceil(threads)))
            .flat_map(|(reader, part)| reader.join().unwrap_or_else(|_| probe(part)))
            .collect()
    })
}

/// The skill whose file is `path`, when it is one.
fn open_skill(path: &Path, scope: SkillScope) -> Option<Found> {
    let file = std::fs::File::open(path).ok()?;
    let meta = file.metadata().ok().filter(Metadata::is_file)?;
    let header = read_header(file).and_then(|text| frontmatter::parse(&text));
    Some(Found {
        id: file_id(&meta, path),
        entry: load(path.to_path_buf(), header, scope),
    })
}

/// Reads one `SKILL.md` as discovery would see it (used to check a skill before installing it).
pub(crate) fn read_entry(path: PathBuf, scope: SkillScope) -> SkillEntry {
    let header = std::fs::File::open(&path)
        .map_err(|e| format!("cannot read the file: {e}"))
        .and_then(read_header)
        .and_then(|text| frontmatter::parse(&text));
    load(path, header, scope)
}

/// Checks the header of a `SKILL.md` that the walk found. Problems that do not stop the
/// skill from working become notes.
fn load(path: PathBuf, header: Result<frontmatter::Header, String>, scope: SkillScope) -> SkillEntry {
    let dir_name = path
        .parent()
        .and_then(Path::file_name)
        .map(|name| name.to_string_lossy().into_owned())
        .unwrap_or_else(|| "skill".into());
    let mut entry = SkillEntry {
        name: dir_name.clone(),
        description: String::new(),
        path,
        scope,
        model_invocable: true,
        argument_hint: None,
        state: SkillState::Active,
        detail: None,
        notes: Vec::new(),
        source: None,
        removable: false,
        default_enabled: true,
    };
    let header = match header {
        Ok(header) => header,
        Err(reason) => {
            entry.state = SkillState::Invalid;
            entry.detail = Some(reason);
            return entry;
        }
    };
    if let Some(name) = header.name.as_deref().map(one_line).filter(|name| !name.is_empty()) {
        entry.name = name;
    }
    if !follows_naming_rules(&entry.name) {
        entry.notes.push(format!(
            "the name '{}' does not follow the naming rules: lowercase letters, digits and single hyphens, \
             at most {MAX_NAME_CHARS} characters",
            entry.name
        ));
    } else if entry.name != dir_name {
        entry.notes.push(format!("the name differs from the directory name '{dir_name}'"));
    }
    let description = header.description.as_deref().map(one_line).unwrap_or_default();
    if description.is_empty() {
        entry.state = SkillState::Invalid;
        entry.detail = Some("the header has no description".into());
        return entry;
    }
    entry.description = if description.chars().count() > MAX_DESCRIPTION_CHARS {
        entry.notes.push(format!("the description was cut to {MAX_DESCRIPTION_CHARS} characters"));
        description.chars().take(MAX_DESCRIPTION_CHARS).collect()
    } else {
        description
    };
    entry.model_invocable = !header.disable_model_invocation;
    entry.argument_hint = header.argument_hint.as_deref().map(one_line).filter(|hint| !hint.is_empty());
    if header.allowed_tools {
        entry.notes.push("allowed-tools is not supported and was ignored".into());
    }
    if !crate::commands::is_mentionable(&entry.name) {
        entry
            .notes
            .push("the user cannot mention it as /skill:name with this name; the model can still use it".into());
    }
    entry
}

/// The start of the file, until the header's closing line or [`MAX_HEADER_BYTES`]. One
/// read is usually enough.
fn read_header(mut file: std::fs::File) -> Result<String, String> {
    let mut bytes = Vec::new();
    let mut chunk = [0u8; 8192];
    while (bytes.len() as u64) < MAX_HEADER_BYTES {
        let n = file.read(&mut chunk).map_err(|e| format!("cannot read the file: {e}"))?;
        if n == 0 {
            break;
        }
        bytes.extend_from_slice(&chunk[..n]);
        if header_is_complete(&bytes) {
            break;
        }
    }
    bytes.truncate(MAX_HEADER_BYTES as usize);
    match String::from_utf8(bytes) {
        Ok(text) => Ok(text),
        // Cut in the middle of a character at the limit: keep what is whole.
        Err(err) if err.utf8_error().error_len().is_none() => {
            let valid = err.utf8_error().valid_up_to();
            let mut bytes = err.into_bytes();
            bytes.truncate(valid);
            Ok(String::from_utf8(bytes).unwrap_or_default())
        }
        Err(_) => Err("the file is not UTF-8 text".into()),
    }
}

/// Whether `bytes` hold a whole `---` line after the first line.
fn header_is_complete(bytes: &[u8]) -> bool {
    let mut lines = bytes.split_inclusive(|b| *b == b'\n');
    lines.next();
    lines.any(|line| line.ends_with(b"\n") && line.trim_ascii_end() == b"---")
}

fn one_line(text: &str) -> String {
    text.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// 1–64 lowercase letters, digits and hyphens, without a hyphen at either end or two in a row.
fn follows_naming_rules(name: &str) -> bool {
    !name.is_empty()
        && name.chars().count() <= MAX_NAME_CHARS
        && name.chars().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '-')
        && !name.starts_with('-')
        && !name.ends_with('-')
        && !name.contains("--")
}

/// `skills.disabled` or `skills.opt_in`: names, and paths of skill directories or
/// `SKILL.md` files (`opt_in` holds paths only; the configuration checks that).
struct PathsAndNames {
    names: HashSet<String>,
    paths: Vec<PathBuf>,
}

impl PathsAndNames {
    fn new(items: &[String], sources: &Sources<'_>) -> Self {
        let mut names = HashSet::new();
        let mut paths = Vec::new();
        for item in items {
            if is_path(item) {
                let path = expand(item, sources.user_home, sources.cwd);
                paths.push(std::fs::canonicalize(&path).unwrap_or(path));
            } else {
                names.insert(item.clone());
            }
        }
        Self { names, paths }
    }

    fn matches(&self, entry: &SkillEntry) -> bool {
        if self.names.contains(&entry.name) {
            return true;
        }
        if self.paths.is_empty() {
            return false;
        }
        let real = |path: &Path| std::fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf());
        let (file, dir) = (real(&entry.path), real(entry.dir()));
        self.paths.iter().any(|path| *path == file || *path == dir)
    }
}

/// Identity of a file or directory, so that the same one met twice is seen as one.
#[cfg(unix)]
type FileId = (u64, u64);

#[cfg(unix)]
fn file_id(meta: &Metadata, _path: &Path) -> FileId {
    use std::os::unix::fs::MetadataExt;
    (meta.dev(), meta.ino())
}

/// The identity of a directory met while reading its parent, without a `stat`: the entry's
/// inode on the root's device. Mount points inside a skill root are not told apart.
#[cfg(unix)]
fn entry_id(entry: &std::fs::DirEntry, root: &Metadata) -> Option<FileId> {
    use std::os::unix::fs::DirEntryExt;
    use std::os::unix::fs::MetadataExt;
    Some((root.dev(), entry.ino()))
}

#[cfg(not(unix))]
type FileId = PathBuf;

#[cfg(not(unix))]
fn file_id(_meta: &Metadata, path: &Path) -> FileId {
    std::fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf())
}

#[cfg(not(unix))]
fn entry_id(entry: &std::fs::DirEntry, _root: &Metadata) -> Option<FileId> {
    Some(std::fs::canonicalize(entry.path()).unwrap_or_else(|_| entry.path()))
}

#[cfg(test)]
mod tests {
    use super::*;

    struct Fixture {
        _dir: tempfile::TempDir,
        base: PathBuf,
    }

    impl Fixture {
        fn new() -> Self {
            let dir = tempfile::tempdir().unwrap();
            let base = std::fs::canonicalize(dir.path()).unwrap();
            Self { _dir: dir, base }
        }

        fn path(&self, rel: &str) -> PathBuf {
            self.base.join(rel)
        }

        /// Writes `<rel>/SKILL.md` with a header naming `name` (or none) and `description`.
        fn skill(&self, rel: &str, name: Option<&str>, description: &str) -> PathBuf {
            let dir = self.path(rel);
            std::fs::create_dir_all(&dir).unwrap();
            let name = name.map(|n| format!("name: {n}\n")).unwrap_or_default();
            let file = dir.join(SKILL_FILE);
            std::fs::write(&file, format!("---\n{name}description: {description}\n---\n# Body\n")).unwrap();
            file
        }

        fn discover(&self, cwd: &str, root: Option<&str>, settings: &SkillsSettings) -> SkillSet {
            let cwd = self.path(cwd);
            std::fs::create_dir_all(&cwd).unwrap();
            let root = root.map(|r| self.path(r));
            discover(&Sources {
                cwd: &cwd,
                project_root: root.as_deref(),
                her_home: &self.path("her-home"),
                user_home: Some(&self.path("home")),
                settings,
                system: false,
            })
        }
    }

    fn states(set: &SkillSet) -> Vec<(String, SkillScope, SkillState)> {
        set.entries.iter().map(|e| (e.name.clone(), e.scope, e.state)).collect()
    }

    #[test]
    fn nearest_first_across_the_three_layers() {
        let fx = Fixture::new();
        fx.skill("repo/packages/web/.agents/skills/release", None, "Release the web package.");
        fx.skill("repo/packages/web/.her/skills/old", None, "A project's .her is not read.");
        fx.skill("repo/.agents/skills/release", None, "Release everything.");
        fx.skill("repo/.agents/skills/commit", None, "Commit using the repo convention.");
        fx.skill("her-home/skills/commit", None, "My commit style.");
        fx.skill("home/.agents/skills/pdf-tools", None, "Extract text from PDFs.");
        fx.skill("home/.claude/skills/pdf-tools", None, "Another copy.");
        fx.skill("extra/lint", None, "Lint rules.");
        let settings = SkillsSettings {
            other_apps: true,
            paths: vec![fx.path("extra").display().to_string()],
            ..SkillsSettings::default()
        };
        let set = fx.discover("repo/packages/web", Some("repo"), &settings);
        use SkillScope::*;
        use SkillState::*;
        assert_eq!(
            states(&set),
            vec![
                ("release".into(), Project, Active),
                ("commit".into(), Project, Active),
                ("release".into(), Project, Shadowed),
                ("commit".into(), User, Shadowed),
                ("pdf-tools".into(), User, Active),
                ("pdf-tools".into(), User, Shadowed),
                ("lint".into(), Extra, Active),
            ]
        );
        assert_eq!(set.entries[2].detail.as_deref(), Some(set.entries[0].path.to_str().unwrap()));
        assert_eq!(set.entries[4].path, fx.path("home/.agents/skills/pdf-tools/SKILL.md"));
        let refs: Vec<String> = set.references().into_iter().map(|r| r.name).collect();
        assert_eq!(refs, vec!["release", "commit", "pdf-tools", "lint"]);
        assert!(set.warnings.is_empty(), "{:?}", set.warnings);
        assert!(set.not_read.is_empty(), "read, so not reported");

        // By default the user layer is her's own folder; the others are only reported.
        let set = fx.discover("repo/packages/web", Some("repo"), &SkillsSettings::default());
        assert!(set.entries.iter().all(|e| !e.path.starts_with(fx.path("home"))), "{:?}", states(&set));
        assert_eq!(set.not_read, vec![fx.path("home/.agents/skills"), fx.path("home/.claude/skills")]);
    }

    #[test]
    fn claude_skills_are_off_until_opted_in_and_do_not_shadow() {
        let fx = Fixture::new();
        fx.skill("work/.agents/skills/x", None, "From agents.");
        fx.skill("work/.claude/skills/x", None, "From claude.");
        let deploy = fx.skill("work/.claude/skills/deploy", None, "The repo's deploy.");
        fx.skill("her-home/skills/deploy", None, "My deploy.");
        let set = fx.discover("work", None, &SkillsSettings::default());
        use SkillScope::*;
        use SkillState::*;
        assert_eq!(
            states(&set),
            vec![
                ("x".into(), Project, Active),
                ("deploy".into(), Project, Disabled),
                ("x".into(), Project, Disabled),
                ("deploy".into(), User, Active),
            ],
            "an off skill shadows nothing"
        );
        assert!(set.entries[0].default_enabled && !set.entries[1].default_enabled && set.entries[3].default_enabled);

        let opted = SkillsSettings {
            opt_in: vec![deploy.parent().unwrap().display().to_string()],
            ..SkillsSettings::default()
        };
        let set = fx.discover("work", None, &opted);
        assert_eq!(set.entries[1].state, Active, "turned on, the nearer copy wins");
        assert_eq!(set.entries[3].state, Shadowed);

        let both = SkillsSettings {
            disabled: vec![deploy.display().to_string()],
            ..opted
        };
        let set = fx.discover("work", None, &both);
        assert_eq!(set.entries[1].state, Disabled, "disabled wins over opt_in");
        assert_eq!(set.entries[3].state, Active);
    }

    #[test]
    fn without_a_project_only_the_working_directory_counts() {
        let fx = Fixture::new();
        fx.skill("work/.agents/skills/here", None, "Here.");
        fx.skill(".agents/skills/above", None, "Above the working directory.");
        let set = fx.discover("work", None, &SkillsSettings::default());
        assert_eq!(states(&set), vec![("here".into(), SkillScope::Project, SkillState::Active)]);
    }

    #[test]
    fn walking_stops_at_a_skill_and_skips_hidden_and_dependency_folders() {
        let fx = Fixture::new();
        let root = "work/.agents/skills";
        fx.skill(&format!("{root}/creator"), None, "Make skills.");
        // A template inside a skill is not another skill.
        fx.skill(&format!("{root}/creator/assets/template"), Some("template"), "A template.");
        fx.skill(&format!("{root}/group/nested"), None, "Grouped one level down.");
        fx.skill(&format!("{root}/.hidden/secret"), None, "Hidden.");
        fx.skill(&format!("{root}/node_modules/dep"), None, "A dependency.");
        fx.skill(&format!("{root}/a/b/c/d/e/f/too-deep"), None, "Seven levels down.");
        fx.skill(&format!("{root}/a/b/c/d/e/deep"), None, "Six levels down.");
        std::fs::write(fx.path(&format!("{root}/README.md")), "not a skill").unwrap();
        let set = fx.discover("work", None, &SkillsSettings::default());
        let names: Vec<&str> = set.entries.iter().map(|e| e.name.as_str()).collect();
        assert_eq!(names, vec!["deep", "creator", "nested"], "sorted by path");
    }

    #[cfg(unix)]
    #[test]
    fn links_are_followed_once_and_cycles_stop() {
        let fx = Fixture::new();
        let real = fx.skill("store/shared", None, "Installed by a link.");
        let root = fx.path("home/.agents/skills");
        std::fs::create_dir_all(&root).unwrap();
        std::os::unix::fs::symlink(fx.path("store/shared"), root.join("shared")).unwrap();
        std::os::unix::fs::symlink(&root, root.join("loop")).unwrap();
        // The whole root linked from another client's directory: the same files, seen once.
        std::fs::create_dir_all(fx.path("home/.claude")).unwrap();
        std::os::unix::fs::symlink(&root, fx.path("home/.claude/skills")).unwrap();
        let other_apps = SkillsSettings {
            other_apps: true,
            ..SkillsSettings::default()
        };
        let set = fx.discover("work", None, &other_apps);
        assert_eq!(set.entries.len(), 1, "{:?}", states(&set));
        assert_eq!(set.entries[0].path, root.join("shared/SKILL.md"), "the path as found, not resolved");
        assert_eq!(std::fs::canonicalize(&set.entries[0].path).unwrap(), real);
    }

    #[test]
    fn the_home_is_never_a_project_directory() {
        let fx = Fixture::new();
        fx.skill("home/.agents/skills/mine", None, "Mine.");
        fx.skill("home/code/.agents/skills/here", None, "Here.");
        // The home as the working directory, and the home as a project (a dotfiles repository).
        let set = fx.discover("home", None, &SkillsSettings::default());
        assert!(set.entries.is_empty(), "{:?}", states(&set));
        let set = fx.discover("home/code", Some("home"), &SkillsSettings::default());
        assert_eq!(states(&set), vec![("here".into(), SkillScope::Project, SkillState::Active)]);
        // With other apps read, the home's folder stays in the user layer.
        let other_apps = SkillsSettings {
            other_apps: true,
            ..SkillsSettings::default()
        };
        let set = fx.discover("home", None, &other_apps);
        assert_eq!(states(&set), vec![("mine".into(), SkillScope::User, SkillState::Active)]);
    }

    #[test]
    fn disabling_by_name_or_by_path() {
        let fx = Fixture::new();
        fx.skill("work/.agents/skills/deploy", None, "The repo's deploy.");
        fx.skill("her-home/skills/deploy", None, "My deploy.");
        fx.skill("home/team/other", None, "Other.");
        let by_name = SkillsSettings {
            paths: vec!["~/team".into()],
            disabled: vec!["deploy".into()],
            ..SkillsSettings::default()
        };
        let set = fx.discover("work", None, &by_name);
        assert_eq!(
            set.entries.iter().map(|e| e.state).collect::<Vec<_>>(),
            vec![SkillState::Disabled, SkillState::Disabled, SkillState::Active],
            "no shadowed copy takes over"
        );
        let by_path = SkillsSettings {
            paths: vec!["~/team".into()],
            disabled: vec![fx.path("work/.agents/skills/deploy").display().to_string()],
            ..SkillsSettings::default()
        };
        let set = fx.discover("work", None, &by_path);
        assert_eq!(set.entries[0].state, SkillState::Disabled);
        assert_eq!(set.entries[1].state, SkillState::Active, "the next one takes over");
        assert_eq!(set.entries[1].description, "My deploy.");
        let by_home_path = SkillsSettings {
            paths: vec!["~/team".into()],
            disabled: vec!["~/team/other/SKILL.md".into()],
            ..SkillsSettings::default()
        };
        let set = fx.discover("work", None, &by_home_path);
        assert_eq!(set.entries.last().unwrap().state, SkillState::Disabled);
    }

    #[test]
    fn lenient_checks_and_broken_skills() {
        let fx = Fixture::new();
        let root = "work/.agents/skills";
        fx.skill(&format!("{root}/Odd_Name"), None, "Directory name used as the name.");
        fx.skill(&format!("{root}/renamed"), Some("other-name"), "Named in the header.");
        fx.skill(&format!("{root}/empty"), None, "''");
        std::fs::create_dir_all(fx.path(&format!("{root}/plain"))).unwrap();
        std::fs::write(fx.path(&format!("{root}/plain/SKILL.md")), "# Just markdown\n").unwrap();
        std::fs::create_dir_all(fx.path(&format!("{root}/long"))).unwrap();
        std::fs::write(
            fx.path(&format!("{root}/long/SKILL.md")),
            format!(
                "---\ndescription: >-\n  {}\n  tail\nallowed-tools: Read\ndisable-model-invocation: true\n---\n",
                "x".repeat(1100)
            ),
        )
        .unwrap();
        let set = fx.discover("work", None, &SkillsSettings::default());
        let by_name = |name: &str| set.entries.iter().find(|e| e.name == name).unwrap();
        assert_eq!(by_name("Odd_Name").state, SkillState::Active);
        assert!(by_name("Odd_Name").notes[0].contains("naming rules"));
        assert_eq!(by_name("other-name").notes, vec!["the name differs from the directory name 'renamed'"]);
        assert_eq!(by_name("empty").state, SkillState::Invalid);
        assert_eq!(by_name("empty").detail.as_deref(), Some("the header has no description"));
        assert_eq!(by_name("plain").state, SkillState::Invalid);
        let long = by_name("long");
        assert_eq!(long.description.chars().count(), MAX_DESCRIPTION_CHARS);
        assert!(!long.model_invocable);
        assert_eq!(long.notes.len(), 2, "{:?}", long.notes);
        assert_eq!(set.warnings.len(), 2, "one per skill that did not load: {:?}", set.warnings);
        assert!(set.warnings.iter().all(|w| w.contains("was not loaded")));
    }

    #[test]
    fn configured_paths_may_name_one_skill_and_use_the_home() {
        let fx = Fixture::new();
        fx.skill("home/team/one", None, "A single skill directory.");
        let file = fx.skill("elsewhere/two", None, "A SKILL.md file.");
        let settings = SkillsSettings {
            paths: vec!["~/team/one".into(), file.display().to_string(), "~/missing".into()],
            ..SkillsSettings::default()
        };
        let set = fx.discover("work", None, &settings);
        assert_eq!(states(&set).len(), 2);
        assert!(set.entries.iter().all(|e| e.scope == SkillScope::Extra && e.state == SkillState::Active));
        assert!(set.warnings.is_empty(), "missing roots are skipped silently");
    }

    #[test]
    fn turned_off_finds_nothing() {
        let fx = Fixture::new();
        fx.skill("work/.agents/skills/x", None, "X.");
        let off = SkillsSettings {
            enabled: false,
            ..SkillsSettings::default()
        };
        assert!(fx.discover("work", None, &off).entries.is_empty());
    }

    #[test]
    fn expanding_paths() {
        let home = Path::new("/home/me");
        let base = Path::new("/work");
        assert_eq!(expand("~/skills", Some(home), base), PathBuf::from("/home/me/skills"));
        assert_eq!(expand("~", Some(home), base), PathBuf::from("/home/me"));
        assert_eq!(expand("team/../skills", Some(home), base), PathBuf::from("/work/skills"));
        assert_eq!(expand("/abs", Some(home), base), PathBuf::from("/abs"));
        assert!(is_path("~/x") && is_path("a/b") && !is_path("pdf-tools"));
    }
}
