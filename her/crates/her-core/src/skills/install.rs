//! Installing, removing, describing and switching skills for the 自定义 page and
//! `her skills add/remove/enable/disable` (docs/desktop/customize-design.md §9).

use std::fs::File;
use std::io;
use std::io::Read as _;
use std::path::Path;
use std::path::PathBuf;
use std::time::SystemTime;

use base64::Engine as _;
use her_protocol::api::SkillEntry;
use her_protocol::api::SkillScope;
use her_protocol::api::SkillState;
use her_protocol::customize::ConfigEdit;
use her_protocol::customize::ConfigLayer;
use her_protocol::customize::ConfigMerge;
use her_protocol::customize::SkillFileInfo;
use her_protocol::customize::SkillImage;
use her_protocol::customize::SkillSource;
use her_protocol::customize::SkillSourceKind;
use her_protocol::customize::SkillsFilesResult;
use her_protocol::customize::SkillsReadResult;
use her_protocol::customize::SkillsSetEnabledResult;
use her_tools::image::ImageFormat;

use super::SKILL_FILE;
use super::expand;
use super::is_path;
use super::read_entry;
use crate::config::LayerFiles;
use crate::config_edit;
use crate::paths::HerHome;
use crate::trash::Trash;

/// Where her records what it installed a skill from.
pub const SOURCE_FILE: &str = ".her-source.json";
const MAX_TOTAL_BYTES: u64 = 20 << 20;
const MAX_FILES: usize = 2000;
const MAX_DEPTH: usize = 8;
const MAX_SKILL_MD: usize = 256 << 10;
const MAX_LISTED: usize = 500;
const MAX_LISTED_DEPTH: usize = 6;

#[derive(Debug, thiserror::Error)]
pub enum InstallError {
    /// Not a usable skill (→ `skill_invalid`).
    #[error("{0}")]
    Invalid(String),
    /// A skill of that name is there (→ `skill_conflict`).
    #[error("a skill named {name} is already at {}", path.display())]
    Conflict { name: String, path: PathBuf },
    /// Not one of the user's own skills (→ `not_removable`).
    #[error("{0}")]
    NotRemovable(String),
    #[error("{context}: {source}")]
    Io {
        context: String,
        #[source]
        source: io::Error,
    },
}

fn io_err(context: impl Into<String>) -> impl FnOnce(io::Error) -> InstallError {
    let context = context.into();
    move |source| InstallError::Io { context, source }
}

/// `.her-source.json` of a skill directory, when there is one.
pub fn read_source(dir: &Path) -> Option<SkillSource> {
    let text = std::fs::read_to_string(dir.join(SOURCE_FILE)).ok()?;
    serde_json::from_str(&text).ok()
}

pub struct InstallRequest<'a> {
    /// A skill directory, a `SKILL.md`, or a `.zip`/`.skill` archive.
    pub from: &'a Path,
    /// Where the skill goes: `~/.her/skills` or `<project>/.her/skills`.
    pub root: &'a Path,
    pub scope: SkillScope,
    /// Absent: an upload.
    pub source: Option<SkillSource>,
    pub dry_run: bool,
    pub replace: bool,
    pub trash: &'a Trash,
}

#[derive(Debug)]
pub struct Installed {
    pub skill: SkillEntry,
    pub target: PathBuf,
    /// A dry run found a skill of the same name there.
    pub conflict: Option<PathBuf>,
}

/// Checks a skill and copies it into `root` (§9.1): read and check it as discovery would,
/// copy it beside the target, record where it came from, then rename it into place.
pub fn install(req: &InstallRequest<'_>) -> Result<Installed, InstallError> {
    std::fs::create_dir_all(req.root).map_err(io_err(format!("cannot create {}", req.root.display())))?;
    let mut unpacked = None;
    let result = (|| {
        let source_dir = match source_dir(req.from)? {
            Source::Dir(dir) => dir,
            Source::Archive(archive) => {
                let into = req.root.join(format!(".unpack-{}-{}", std::process::id(), nanos()));
                unpacked = Some(into.clone());
                unpack(&archive, &into)?;
                find_skill_dir(&into)?
            }
        };
        let skill = read_entry(source_dir.join(SKILL_FILE), req.scope);
        if skill.state == SkillState::Invalid {
            return Err(InstallError::Invalid(format!("not a usable skill: {}", skill.detail.as_deref().unwrap_or("the header is broken"))));
        }
        let files = collect(&source_dir)?;
        let folder = folder_name(&skill.name)
            .or_else(|| source_dir.file_name().map(|n| n.to_string_lossy().into_owned()).and_then(|n| folder_name(&n)))
            .ok_or_else(|| InstallError::Invalid(format!("'{}' cannot be a folder name", skill.name)))?;
        let target = req.root.join(&folder);
        let conflict = target.symlink_metadata().is_ok().then(|| target.clone());
        if req.dry_run {
            return Ok(Installed { skill, target, conflict });
        }
        if let Some(path) = &conflict
            && !req.replace
        {
            return Err(InstallError::Conflict { name: skill.name.clone(), path: path.clone() });
        }
        let staging = req.root.join(format!(".{folder}.tmp-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&staging);
        let copied = copy_into(&source_dir, &files, &staging).and_then(|sha256| {
            let source = req.source.clone().unwrap_or(SkillSource {
                kind: SkillSourceKind::Upload,
                id: None,
                version: None,
                sha256: None,
                installed_at: None,
            });
            let record = SkillSource {
                sha256: Some(sha256),
                installed_at: Some(chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Secs, true)),
                ..source
            };
            let json = serde_json::to_string_pretty(&record).unwrap_or_default() + "\n";
            std::fs::write(staging.join(SOURCE_FILE), json).map_err(io_err("cannot record the source"))
        });
        if let Err(err) = copied {
            let _ = std::fs::remove_dir_all(&staging);
            return Err(err);
        }
        if conflict.is_some()
            && let Err(err) = req.trash.put(&target)
        {
            let _ = std::fs::remove_dir_all(&staging);
            return Err(io_err(format!("cannot move {} to the trash", target.display()))(err));
        }
        std::fs::rename(&staging, &target).map_err(io_err(format!("cannot create {}", target.display())))?;
        let mut skill = read_entry(target.join(SKILL_FILE), req.scope);
        skill.removable = req.scope == SkillScope::User;
        skill.source = read_source(&target);
        Ok(Installed { skill, target, conflict: None })
    })();
    if let Some(dir) = unpacked {
        let _ = std::fs::remove_dir_all(dir);
    }
    result
}

enum Source {
    Dir(PathBuf),
    Archive(PathBuf),
}

fn source_dir(from: &Path) -> Result<Source, InstallError> {
    let meta = std::fs::metadata(from).map_err(io_err(format!("cannot read {}", from.display())))?;
    if meta.is_dir() {
        return Ok(Source::Dir(from.to_path_buf()));
    }
    let name = from.file_name().map(|n| n.to_string_lossy().to_ascii_lowercase()).unwrap_or_default();
    if name == "skill.md" {
        return Ok(Source::Dir(from.parent().unwrap_or(Path::new(".")).to_path_buf()));
    }
    if name.ends_with(".zip") || name.ends_with(".skill") {
        return Ok(Source::Archive(from.to_path_buf()));
    }
    Err(InstallError::Invalid("choose a skill folder, a .zip or .skill archive, or a SKILL.md".into()))
}

/// A skill name as a folder name: one path component, not hidden.
fn folder_name(name: &str) -> Option<String> {
    let ok = !name.is_empty()
        && name.len() <= 64
        && !name.starts_with('.')
        && name.chars().all(|c| c.is_alphanumeric() || matches!(c, '-' | '_' | '.'));
    ok.then(|| name.to_string())
}

fn nanos() -> u128 {
    SystemTime::now().duration_since(SystemTime::UNIX_EPOCH).map(|d| d.as_nanos()).unwrap_or(0)
}

/// Extracts a zip archive, refusing paths that leave it, links and anything over the limits.
fn unpack(archive: &Path, into: &Path) -> Result<(), InstallError> {
    let file = File::open(archive).map_err(io_err(format!("cannot read {}", archive.display())))?;
    let mut zip = zip::ZipArchive::new(file).map_err(|e| InstallError::Invalid(format!("not a zip archive: {e}")))?;
    if zip.len() > MAX_FILES {
        return Err(InstallError::Invalid(format!("the archive has more than {MAX_FILES} entries")));
    }
    std::fs::create_dir_all(into).map_err(io_err("cannot unpack the archive"))?;
    let mut total = 0u64;
    for i in 0..zip.len() {
        let mut entry = zip.by_index(i).map_err(|e| InstallError::Invalid(format!("cannot read the archive: {e}")))?;
        let Some(relative) = entry.enclosed_name() else {
            let name = entry.name().map(|n| n.into_owned()).unwrap_or_default();
            return Err(InstallError::Invalid(format!("the archive has an unsafe path: {name}")));
        };
        if relative.components().next().is_some_and(|c| c.as_os_str() == "__MACOSX") || relative.ends_with(".DS_Store") {
            continue;
        }
        if entry.is_symlink() {
            return Err(InstallError::Invalid(format!("the archive has a link: {}", relative.display())));
        }
        if relative.components().count() > MAX_DEPTH + 1 {
            return Err(InstallError::Invalid(format!("the archive is nested deeper than {MAX_DEPTH} levels")));
        }
        let out = into.join(&relative);
        if entry.is_dir() {
            std::fs::create_dir_all(&out).map_err(io_err("cannot unpack the archive"))?;
            continue;
        }
        if let Some(parent) = out.parent() {
            std::fs::create_dir_all(parent).map_err(io_err("cannot unpack the archive"))?;
        }
        let mut file = File::create(&out).map_err(io_err("cannot unpack the archive"))?;
        let left = MAX_TOTAL_BYTES.saturating_sub(total);
        let written = io::copy(&mut (&mut entry).take(left + 1), &mut file).map_err(io_err("cannot unpack the archive"))?;
        total += written;
        if total > MAX_TOTAL_BYTES {
            return Err(InstallError::Invalid(format!("the skill is larger than {} MB", MAX_TOTAL_BYTES >> 20)));
        }
        #[cfg(unix)]
        if let Some(mode) = entry.unix_mode() {
            use std::os::unix::fs::PermissionsExt;
            let _ = file.set_permissions(std::fs::Permissions::from_mode(mode & 0o755));
        }
    }
    Ok(())
}

/// The skill in an unpacked archive: at its top, or in its only top-level folder.
fn find_skill_dir(dir: &Path) -> Result<PathBuf, InstallError> {
    if dir.join(SKILL_FILE).is_file() {
        return Ok(dir.to_path_buf());
    }
    let folders: Vec<PathBuf> = std::fs::read_dir(dir)
        .map_err(io_err("cannot read the archive"))?
        .flatten()
        .map(|e| e.path())
        .filter(|p| p.is_dir() && !p.file_name().is_some_and(|n| n.to_string_lossy().starts_with('.')))
        .collect();
    match folders.as_slice() {
        [only] if only.join(SKILL_FILE).is_file() => Ok(only.clone()),
        _ => Err(InstallError::Invalid("the archive has no SKILL.md at its top or in its only folder".into())),
    }
}

/// One file of a skill: its path inside the skill, and where it is.
struct SkillFile {
    relative: PathBuf,
    path: PathBuf,
    dir: bool,
}

/// Every file of the skill, checked against the limits; links that point outside it refused.
fn collect(dir: &Path) -> Result<Vec<SkillFile>, InstallError> {
    let root = std::fs::canonicalize(dir).map_err(io_err(format!("cannot read {}", dir.display())))?;
    let mut out = Vec::new();
    let mut total = 0u64;
    let mut stack = vec![(PathBuf::new(), 0usize)];
    while let Some((relative, depth)) = stack.pop() {
        let here = dir.join(&relative);
        let mut entries: Vec<_> = std::fs::read_dir(&here)
            .map_err(io_err(format!("cannot read {}", here.display())))?
            .flatten()
            .collect();
        entries.sort_by_key(|e| e.file_name());
        for entry in entries {
            let name = entry.file_name();
            if name == ".DS_Store" || name == SOURCE_FILE || name == ".git" {
                continue;
            }
            let path = entry.path();
            let rel = relative.join(&name);
            let meta = std::fs::metadata(&path).map_err(io_err(format!("cannot read {}", path.display())))?;
            if entry.file_type().map(|t| t.is_symlink()).unwrap_or(false) {
                let real = std::fs::canonicalize(&path).map_err(io_err(format!("cannot follow {}", path.display())))?;
                if !real.starts_with(&root) {
                    return Err(InstallError::Invalid(format!("{} links outside the skill", rel.display())));
                }
            }
            if out.len() >= MAX_FILES {
                return Err(InstallError::Invalid(format!("the skill has more than {MAX_FILES} files")));
            }
            if meta.is_dir() {
                if depth + 1 > MAX_DEPTH {
                    return Err(InstallError::Invalid(format!("the skill is nested deeper than {MAX_DEPTH} levels")));
                }
                out.push(SkillFile { relative: rel.clone(), path, dir: true });
                stack.push((rel, depth + 1));
            } else if meta.is_file() {
                total += meta.len();
                if total > MAX_TOTAL_BYTES {
                    return Err(InstallError::Invalid(format!("the skill is larger than {} MB", MAX_TOTAL_BYTES >> 20)));
                }
                out.push(SkillFile { relative: rel, path, dir: false });
            }
        }
    }
    Ok(out)
}

/// Copies the files into `staging` and returns their SHA-256 (paths in order, then contents).
fn copy_into(source: &Path, files: &[SkillFile], staging: &Path) -> Result<String, InstallError> {
    std::fs::create_dir_all(staging).map_err(io_err(format!("cannot create {}", staging.display())))?;
    let mut digest = ring::digest::Context::new(&ring::digest::SHA256);
    let mut sorted: Vec<&SkillFile> = files.iter().collect();
    sorted.sort_by(|a, b| a.relative.cmp(&b.relative));
    for file in sorted {
        let to = staging.join(&file.relative);
        if file.dir {
            std::fs::create_dir_all(&to).map_err(io_err(format!("cannot create {}", to.display())))?;
            continue;
        }
        if let Some(parent) = to.parent() {
            std::fs::create_dir_all(parent).map_err(io_err(format!("cannot create {}", parent.display())))?;
        }
        std::fs::copy(&file.path, &to).map_err(io_err(format!("cannot copy {}", source.join(&file.relative).display())))?;
        let bytes = std::fs::read(&to).map_err(io_err(format!("cannot read {}", to.display())))?;
        digest.update(file.relative.to_string_lossy().as_bytes());
        digest.update(&[0]);
        digest.update(&bytes);
        digest.update(&[0]);
    }
    Ok(digest.finish().as_ref().iter().map(|b| format!("{b:02x}")).collect())
}

/// The skill directory of a `SKILL.md` or of the directory itself.
fn skill_dir(path: &Path) -> PathBuf {
    if path.file_name().is_some_and(|n| n.eq_ignore_ascii_case(SKILL_FILE)) {
        path.parent().unwrap_or(path).to_path_buf()
    } else {
        path.to_path_buf()
    }
}

/// Moves one of the user's own skills to the trash (§9.2). Built-in skills and skills
/// elsewhere (a project, another client's folder) are refused.
pub fn remove(path: &Path, user_root: &Path, trash: &Trash) -> Result<PathBuf, InstallError> {
    let dir = skill_dir(path);
    let not_removable = || InstallError::NotRemovable(format!("{} is not one of your own skills in {}", dir.display(), user_root.display()));
    let real = std::fs::canonicalize(&dir).map_err(|_| not_removable())?;
    let root = std::fs::canonicalize(user_root).map_err(|_| not_removable())?;
    let Ok(inside) = real.strip_prefix(&root) else { return Err(not_removable()) };
    let first = inside.components().next().map(|c| c.as_os_str().to_string_lossy().into_owned()).unwrap_or_default();
    if first.is_empty() || first.starts_with('.') || !real.join(SKILL_FILE).is_file() {
        return Err(not_removable());
    }
    trash.put(&real).map_err(io_err(format!("cannot move {} to the trash", real.display())))
}

/// `skills/files`: the `SKILL.md` and the list of files, for the detail page (§9.4).
pub fn files(path: &Path) -> Result<SkillsFilesResult, InstallError> {
    let dir = skill_dir(path);
    let mut text = Vec::new();
    File::open(dir.join(SKILL_FILE))
        .map_err(io_err(format!("cannot read {}", dir.join(SKILL_FILE).display())))?
        .take(MAX_SKILL_MD as u64 + 1)
        .read_to_end(&mut text)
        .map_err(io_err("cannot read SKILL.md"))?;
    let truncated = text.len() > MAX_SKILL_MD;
    text.truncate(MAX_SKILL_MD);
    let skill_md = String::from_utf8_lossy(&text).into_owned();
    let mut files = Vec::new();
    let mut stack = vec![(PathBuf::new(), 0usize)];
    'walk: while let Some((relative, depth)) = stack.pop() {
        let Ok(entries) = std::fs::read_dir(dir.join(&relative)) else { continue };
        let mut entries: Vec<_> = entries.flatten().collect();
        entries.sort_by_key(|e| e.file_name());
        for entry in entries {
            let name = entry.file_name().to_string_lossy().into_owned();
            if name.starts_with('.') {
                continue;
            }
            let Ok(meta) = entry.metadata() else { continue };
            let rel = relative.join(&name);
            files.push(SkillFileInfo {
                path: rel.to_string_lossy().replace(std::path::MAIN_SEPARATOR, "/"),
                size: if meta.is_dir() { 0 } else { meta.len() },
                dir: meta.is_dir(),
            });
            if files.len() >= MAX_LISTED {
                break 'walk;
            }
            if meta.is_dir() && depth + 1 < MAX_LISTED_DEPTH {
                stack.push((rel, depth + 1));
            }
        }
    }
    files.sort_by(|a, b| a.path.cmp(&b.path));
    Ok(SkillsFilesResult { skill_md, truncated, files })
}

/// `skills/read`: one file of a skill, for the detail page's contents (§9.4). Only files
/// inside the skill's directory are read; a path out of it, or a link that leads out, is
/// refused. Text that is not UTF-8 comes back as `binary` with no text; an image (by its
/// signature) also with its bytes, up to the size a whole skill may have (§9.1).
pub fn read(path: &Path, file: &str) -> Result<SkillsReadResult, InstallError> {
    let dir = skill_dir(path);
    let outside = || InstallError::Invalid(format!("{file} is not a file of the skill in {}", dir.display()));
    let relative = Path::new(file);
    if relative.components().any(|c| !matches!(c, std::path::Component::Normal(_))) {
        return Err(outside());
    }
    let root = std::fs::canonicalize(&dir).map_err(io_err(format!("cannot read {}", dir.display())))?;
    let real = std::fs::canonicalize(root.join(relative)).map_err(io_err(format!("cannot read {file}")))?;
    if !real.starts_with(&root) || !real.is_file() {
        return Err(outside());
    }
    let mut bytes = Vec::new();
    File::open(&real)
        .map_err(io_err(format!("cannot read {file}")))?
        .take(MAX_SKILL_MD as u64 + 1)
        .read_to_end(&mut bytes)
        .map_err(io_err(format!("cannot read {file}")))?;
    if let Some(format) = ImageFormat::detect(&bytes) {
        let whole = std::fs::metadata(&real).is_ok_and(|m| m.len() <= MAX_TOTAL_BYTES);
        let image = if whole {
            let bytes = std::fs::read(&real).map_err(io_err(format!("cannot read {file}")))?;
            Some(SkillImage { mime_type: format.mime_type().to_owned(), data: base64::engine::general_purpose::STANDARD.encode(bytes) })
        } else {
            None
        };
        return Ok(SkillsReadResult { text: String::new(), truncated: false, binary: true, image });
    }
    let truncated = bytes.len() > MAX_SKILL_MD;
    bytes.truncate(MAX_SKILL_MD);
    // A cut can split a character: only what comes after the last whole one may be invalid.
    let text = match std::str::from_utf8(&bytes) {
        Ok(text) => Some(text),
        Err(e) if truncated && e.error_len().is_none() => std::str::from_utf8(&bytes[..e.valid_up_to()]).ok(),
        Err(_) => None,
    };
    Ok(match text.filter(|t| !t.contains('\0')) {
        Some(text) => SkillsReadResult { text: text.to_owned(), truncated, binary: false, image: None },
        None => SkillsReadResult { text: String::new(), truncated: false, binary: true, image: None },
    })
}

/// `skills/set_enabled` (§9.3, skill-design D-175). A skill that is on by default is turned
/// off by adding its directory to the user's `skills.disabled`; one that is off by default
/// (a project's `.claude/skills`) is turned on by adding it to `skills.opt_in`. Turning
/// either back takes out every entry that names it, by path, and by name when enabling; the
/// other list loses its entries for the skill too, so the two never contradict each other.
pub fn set_enabled(home: &HerHome, skill: &Path, enabled: bool) -> Result<SkillsSetEnabledResult, config_edit::WriteError> {
    let user = home.config_file();
    let dir = skill_dir(skill);
    let name = read_entry(dir.join(SKILL_FILE), SkillScope::User).name;
    let names_it = names_dirs(home, std::slice::from_ref(&dir));
    let disabled = user_list(&user, "disabled");
    let opt_in = user_list(&user, "opt_in");
    let mut removed_names = Vec::new();
    let mut next_disabled: Vec<String> = disabled
        .iter()
        .filter(|item| {
            if enabled && **item == name {
                removed_names.push(name.clone());
                return false;
            }
            !names_it(item)
        })
        .cloned()
        .collect();
    let mut next_opt_in: Vec<String> = opt_in.iter().filter(|item| !names_it(item)).cloned().collect();
    let own = dir.display().to_string();
    match (super::on_by_default(&dir, home.user_home()), enabled) {
        (true, false) => next_disabled.push(disabled.iter().find(|item| names_it(item)).cloned().unwrap_or(own)),
        (false, true) => next_opt_in.push(opt_in.iter().find(|item| names_it(item)).cloned().unwrap_or(own)),
        _ => {}
    }
    write_lists(&user, &[("disabled", &disabled, next_disabled), ("opt_in", &opt_in, next_opt_in)])?;
    Ok(SkillsSetEnabledResult { file: user, removed_names })
}

/// `her skills enable/disable <name>`: every skill of the name (skill-design §4.4).
/// Enabling also takes out the entries that name one of `dirs`, the skills of the name, by
/// path.
pub fn set_name_enabled(
    home: &HerHome,
    name: &str,
    dirs: &[PathBuf],
    enabled: bool,
) -> Result<SkillsSetEnabledResult, config_edit::WriteError> {
    let user = home.config_file();
    let dirs: Vec<PathBuf> = dirs.iter().map(|d| skill_dir(d)).collect();
    let names_it = names_dirs(home, &dirs);
    let current = user_list(&user, "disabled");
    let next: Vec<String> = if enabled {
        current.iter().filter(|item| *item != name && !names_it(item)).cloned().collect()
    } else if current.iter().any(|item| item == name) {
        current.clone()
    } else {
        let mut next = current.clone();
        next.push(name.to_string());
        next
    };
    write_lists(&user, &[("disabled", &current, next)])?;
    Ok(SkillsSetEnabledResult { file: user, removed_names: Vec::new() })
}

/// Whether an entry of `skills.disabled` or `skills.opt_in` names one of `dirs` (or its
/// `SKILL.md`) by path.
fn names_dirs<'a>(home: &'a HerHome, dirs: &[PathBuf]) -> impl Fn(&str) -> bool + 'a {
    let real = |p: &Path| std::fs::canonicalize(p).unwrap_or_else(|_| p.to_path_buf());
    let known: Vec<PathBuf> = dirs.iter().flat_map(|d| [real(d), real(&d.join(SKILL_FILE))]).collect();
    let base = dirs.first().cloned().unwrap_or_default();
    move |item: &str| is_path(item) && known.contains(&real(&expand(item, home.user_home(), &base)))
}

/// A list of the user's own `[skills]`: `disabled` or `opt_in`.
fn user_list(user: &Path, key: &str) -> Vec<String> {
    std::fs::read_to_string(user)
        .ok()
        .and_then(|text| text.parse::<toml::Table>().ok())
        .and_then(|table| table.get("skills")?.get(key)?.as_array().cloned())
        .unwrap_or_default()
        .into_iter()
        .filter_map(|v| v.as_str().map(str::to_string))
        .collect()
}

/// Writes the `[skills]` lists that changed, `(key, current, next)`, in one edit of the user's file.
fn write_lists(user: &Path, lists: &[(&str, &[String], Vec<String>)]) -> Result<(), config_edit::WriteError> {
    let edits: Vec<ConfigEdit> = lists
        .iter()
        .filter(|(_, current, next)| next != current)
        .map(|(key, _, next)| ConfigEdit {
            key_path: format!("skills.{key}"),
            value: if next.is_empty() { serde_json::Value::Null } else { serde_json::json!(next) },
            merge: ConfigMerge::Replace,
        })
        .collect();
    if edits.is_empty() {
        return Ok(());
    }
    let target = config_edit::Target {
        layer: ConfigLayer::User,
        files: LayerFiles { user, project: None, user_project: None, project_root: None },
        trusted: true,
        overrides: &[],
        header: None,
        allow_repo: false,
    };
    config_edit::write(&target, &edits, None)?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write as _;

    fn skill(dir: &Path, name: &str, description: &str) -> PathBuf {
        let at = dir.join(name);
        std::fs::create_dir_all(at.join("scripts")).unwrap();
        std::fs::write(at.join(SKILL_FILE), format!("---\nname: {name}\ndescription: {description}\n---\n\n# {name}\n")).unwrap();
        std::fs::write(at.join("scripts/run.sh"), "echo hi\n").unwrap();
        at
    }

    fn request<'a>(from: &'a Path, root: &'a Path, trash: &'a Trash) -> InstallRequest<'a> {
        InstallRequest { from, root, scope: SkillScope::User, source: None, dry_run: false, replace: false, trash }
    }

    #[test]
    fn installs_a_folder_records_its_source_and_refuses_a_second_copy() {
        let dir = tempfile::tempdir().unwrap();
        let src = skill(dir.path(), "csv-cleaner", "Cleans CSV files.");
        let root = dir.path().join("home/skills");
        let trash = Trash::at(&dir.path().join("trash"));

        let dry = install(&InstallRequest { dry_run: true, ..request(&src, &root, &trash) }).unwrap();
        assert_eq!(dry.skill.name, "csv-cleaner");
        assert_eq!(dry.target, root.join("csv-cleaner"));
        assert!(dry.conflict.is_none() && !dry.target.exists());

        let done = install(&request(&src.join(SKILL_FILE), &root, &trash)).unwrap();
        assert!(done.target.join("scripts/run.sh").is_file());
        assert!(done.skill.removable);
        let source = done.skill.source.unwrap();
        assert_eq!(source.kind, SkillSourceKind::Upload);
        assert_eq!(source.sha256.unwrap().len(), 64);

        let dry = install(&InstallRequest { dry_run: true, ..request(&src, &root, &trash) }).unwrap();
        assert_eq!(dry.conflict.as_deref(), Some(root.join("csv-cleaner").as_path()));
        assert!(matches!(install(&request(&src, &root, &trash)), Err(InstallError::Conflict { .. })));
        let replaced = install(&InstallRequest { replace: true, ..request(&src, &root, &trash) }).unwrap();
        assert!(replaced.target.is_dir());
        assert!(dir.path().join("trash/files/csv-cleaner/SKILL.md").is_file(), "the old copy is in the trash");
        // Nothing is left behind.
        let leftovers: Vec<_> = std::fs::read_dir(&root).unwrap().flatten().map(|e| e.file_name()).collect();
        assert_eq!(leftovers, vec!["csv-cleaner"]);
    }

    #[test]
    fn refuses_broken_skills_and_links_out() {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path().join("skills");
        let trash = Trash::at(&dir.path().join("trash"));
        let broken = dir.path().join("broken");
        std::fs::create_dir_all(&broken).unwrap();
        std::fs::write(broken.join(SKILL_FILE), "---\nname: broken\n---\n").unwrap();
        assert!(matches!(install(&request(&broken, &root, &trash)), Err(InstallError::Invalid(_))));

        let src = skill(dir.path(), "linky", "Has a link.");
        std::fs::write(dir.path().join("secret.txt"), "s").unwrap();
        #[cfg(unix)]
        std::os::unix::fs::symlink(dir.path().join("secret.txt"), src.join("secret.txt")).unwrap();
        #[cfg(unix)]
        assert!(matches!(install(&request(&src, &root, &trash)), Err(InstallError::Invalid(m)) if m.contains("outside")));

        let text = dir.path().join("notes.txt");
        std::fs::write(&text, "x").unwrap();
        assert!(matches!(install(&request(&text, &root, &trash)), Err(InstallError::Invalid(_))));
    }

    fn zip_of(path: &Path, entries: &[(&str, &str)]) {
        let mut zip = zip::ZipWriter::new(File::create(path).unwrap());
        for (name, content) in entries {
            zip.start_file(*name, zip::write::SimpleFileOptions::default()).unwrap();
            zip.write_all(content.as_bytes()).unwrap();
        }
        zip.finish().unwrap();
    }

    #[test]
    fn installs_archives_and_refuses_paths_that_leave_them() {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path().join("skills");
        let trash = Trash::at(&dir.path().join("trash"));
        let good = dir.path().join("report.skill");
        zip_of(&good, &[("weekly-report/SKILL.md", "---\nname: weekly-report\ndescription: Weekly reports.\n---\n"), ("weekly-report/references/format.md", "x")]);
        let done = install(&request(&good, &root, &trash)).unwrap();
        assert!(done.target.join("references/format.md").is_file());

        let evil = dir.path().join("evil.zip");
        zip_of(&evil, &[("SKILL.md", "---\nname: evil\ndescription: E.\n---\n"), ("../escape.txt", "x")]);
        assert!(matches!(install(&request(&evil, &root, &trash)), Err(InstallError::Invalid(m)) if m.contains("unsafe")));
        assert!(!dir.path().join("escape.txt").exists());
        let leftovers: Vec<_> = std::fs::read_dir(&root).unwrap().flatten().map(|e| e.file_name()).collect();
        assert_eq!(leftovers, vec!["weekly-report"], "the unpacked copy is gone");
    }

    #[test]
    fn removes_only_own_skills_and_lists_files() {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path().join("skills");
        let trash = Trash::at(&dir.path().join("trash"));
        let mine = skill(&root, "mine", "Mine.");
        let builtin = skill(&root.join(".system"), "builtin-example", "Built in.");
        let elsewhere = skill(&dir.path().join("repo/.her/skills"), "repo-skill", "Theirs.");

        let listed = files(&mine.join(SKILL_FILE)).unwrap();
        assert!(listed.skill_md.contains("name: mine") && !listed.truncated);
        let paths: Vec<&str> = listed.files.iter().map(|f| f.path.as_str()).collect();
        assert_eq!(paths, vec!["SKILL.md", "scripts", "scripts/run.sh"]);

        assert!(matches!(remove(&builtin, &root, &trash), Err(InstallError::NotRemovable(_))));
        assert!(matches!(remove(&elsewhere, &root, &trash), Err(InstallError::NotRemovable(_))));
        remove(&mine.join(SKILL_FILE), &root, &trash).unwrap();
        assert!(!mine.exists());
    }

    #[test]
    fn reads_files_inside_the_skill_only() {
        let dir = tempfile::tempdir().unwrap();
        let at = skill(dir.path(), "reader", "Reads.");
        let run = read(&at, "scripts/run.sh").unwrap();
        assert_eq!((run.text.as_str(), run.truncated, run.binary), ("echo hi\n", false, false));
        assert!(read(&at.join(SKILL_FILE), "SKILL.md").unwrap().text.contains("name: reader"));

        std::fs::write(at.join("logo.png"), b"\x89PNG\r\n\x1a\n\0\0\0\x0dIHDR").unwrap();
        let png = read(&at, "logo.png").unwrap();
        assert!(png.binary && png.text.is_empty());
        let image = png.image.unwrap();
        assert_eq!(image.mime_type, "image/png");
        assert_eq!(base64::engine::general_purpose::STANDARD.decode(image.data).unwrap(), b"\x89PNG\r\n\x1a\n\0\0\0\x0dIHDR");
        std::fs::write(at.join("data.bin"), [0, 0xff, 0xfe]).unwrap();
        let bin = read(&at, "data.bin").unwrap();
        assert!(bin.binary && bin.image.is_none());

        // A cut in the middle of a character is still text.
        std::fs::write(at.join("long.md"), "中".repeat(MAX_SKILL_MD)).unwrap();
        let long = read(&at, "long.md").unwrap();
        assert!(long.truncated && !long.binary && long.text.len() <= MAX_SKILL_MD);

        std::fs::write(dir.path().join("secret.txt"), "s").unwrap();
        for out in ["../secret.txt", "/etc/hosts", "scripts"] {
            assert!(matches!(read(&at, out), Err(InstallError::Invalid(_))), "{out}");
        }
        #[cfg(unix)]
        {
            std::os::unix::fs::symlink(dir.path().join("secret.txt"), at.join("secret.txt")).unwrap();
            assert!(matches!(read(&at, "secret.txt"), Err(InstallError::Invalid(_))));
        }
    }

    #[test]
    fn switching_off_and_on_edits_the_user_config() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        std::fs::create_dir_all(home.root()).unwrap();
        std::fs::write(home.config_file(), "# keep me\n[skills]\ndisabled = [\"lint\"]\n").unwrap();
        let repo_skill = skill(&dir.path().join("repo/.agents/skills"), "lint", "Lint.");
        set_enabled(&home, &repo_skill.join(SKILL_FILE), false).unwrap();
        let text = std::fs::read_to_string(home.config_file()).unwrap();
        assert!(text.starts_with("# keep me\n"), "{text}");
        assert!(text.contains(&repo_skill.display().to_string()), "{text}");
        // Twice is the same as once.
        set_enabled(&home, &repo_skill, false).unwrap();
        assert_eq!(std::fs::read_to_string(home.config_file()).unwrap(), text);
        let result = set_enabled(&home, &repo_skill, true).unwrap();
        assert_eq!(result.removed_names, vec!["lint"]);
        let text = std::fs::read_to_string(home.config_file()).unwrap();
        assert!(!text.contains("disabled"), "{text}");
    }

    #[test]
    fn switching_a_skill_that_is_off_by_default_edits_opt_in() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        std::fs::create_dir_all(home.root()).unwrap();
        let theirs = skill(&dir.path().join("repo/.claude/skills"), "deploy", "Deploy.");
        // A stale entry for it in the other list goes when it is turned on.
        std::fs::write(home.config_file(), format!("[skills]\ndisabled = [{:?}]\n", theirs.display().to_string())).unwrap();
        let result = set_enabled(&home, &theirs.join(SKILL_FILE), true).unwrap();
        assert!(result.removed_names.is_empty());
        let config = std::fs::read_to_string(home.config_file()).unwrap();
        let table: toml::Table = config.parse().unwrap();
        assert_eq!(table["skills"]["opt_in"].as_array().unwrap(), &vec![toml::Value::from(theirs.display().to_string())]);
        assert!(table["skills"].get("disabled").is_none(), "{config}");
        // Twice is the same as once; turning it off takes it out again.
        set_enabled(&home, &theirs, true).unwrap();
        assert_eq!(std::fs::read_to_string(home.config_file()).unwrap(), config);
        set_enabled(&home, &theirs, false).unwrap();
        let config = std::fs::read_to_string(home.config_file()).unwrap();
        assert!(!config.contains("opt_in") && !config.contains("disabled"), "{config}");
        assert!(super::super::on_by_default(&dir.path().join("repo/.agents/skills/x"), None));
        assert!(!super::super::on_by_default(&theirs, None));
        assert!(super::super::on_by_default(&theirs, Some(&dir.path().join("repo"))), "the home's .claude is the user's");
    }

    #[test]
    fn switching_by_name_covers_every_copy() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        std::fs::create_dir_all(home.root()).unwrap();
        let copy = skill(&dir.path().join("repo/.agents/skills"), "lint", "Lint.");
        set_enabled(&home, &copy, false).unwrap();
        set_name_enabled(&home, "lint", std::slice::from_ref(&copy), false).unwrap();
        let text = std::fs::read_to_string(home.config_file()).unwrap();
        assert!(text.contains("\"lint\"") && text.contains(&copy.display().to_string()), "{text}");
        // Enabling by name takes out the name and the path of a copy.
        set_name_enabled(&home, "lint", &[copy.join(SKILL_FILE)], true).unwrap();
        let text = std::fs::read_to_string(home.config_file()).unwrap();
        assert!(!text.contains("disabled"), "{text}");
    }
}
