//! Writing the user's own configuration files (docs/desktop/customize-design.md §5): dotted
//! key paths, edits that keep the rest of a hand-written file byte for byte, versions against
//! lost updates, a lock against other writers, the whole configuration checked before the file
//! is replaced, and which written keys a higher layer still sets. The service's `config/write`
//! and the command line's `her mcp add` and friends both come here.

use std::collections::BTreeMap;
use std::collections::BTreeSet;
use std::fs::File;
use std::io;
use std::io::Write as _;
use std::path::Path;
use std::path::PathBuf;
use std::time::Duration;
use std::time::Instant;

use her_protocol::customize::ConfigEdit;
use her_protocol::customize::ConfigLayer;
use her_protocol::customize::ConfigLayerInfo;
use her_protocol::customize::ConfigMerge;
use her_protocol::customize::ConfigOverride;
use her_protocol::customize::ConfigReadResult;
use her_protocol::customize::ConfigWriteResult;
use her_protocol::customize::ConfigWriteStatus;
use serde_json::Value as Json;
use toml_edit::DocumentMut;
use toml_edit::Item;
use toml_edit::TableLike;

use crate::config;
use crate::config::LayerFiles;
use crate::config::LoadedConfig;
use crate::paths::HerHome;

/// How long a writer waits for another one to finish.
const LOCK_WAIT: Duration = Duration::from_secs(2);

/// The first lines of a personal project file that her creates.
pub fn user_project_header(project_dir: &Path) -> String {
    format!(
        "# Personal settings for {}\n# (only you, only this project; written by her)\n\n",
        project_dir.display()
    )
}

#[derive(Debug, thiserror::Error)]
pub enum WriteError {
    #[error("the file changed after it was read")]
    VersionConflict { version: String },
    #[error("{}", .0.iter().map(|e| format!("{}: {}", e.key_path, e.message)).collect::<Vec<_>>().join("; "))]
    Invalid(Vec<FieldError>),
    #[error("the configuration file is locked by another writer")]
    Busy,
    #[error("cannot write {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: io::Error,
    },
}

/// A problem with one written key; `key_path` is empty when it cannot be pinned down.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct FieldError {
    pub key_path: String,
    pub message: String,
}

impl FieldError {
    fn new(key_path: &str, message: impl Into<String>) -> Self {
        Self { key_path: key_path.to_string(), message: message.into() }
    }
}

/// Where a write goes, and the other layers to check it against.
pub struct Target<'a> {
    pub layer: ConfigLayer,
    pub files: LayerFiles<'a>,
    pub trusted: bool,
    pub overrides: &'a [String],
    /// Written first when the write creates the file.
    pub header: Option<String>,
    /// Also write the project's own file: only the command line's `--project` asks for it;
    /// the page never writes a repository's files (design DD-46).
    pub allow_repo: bool,
}

impl Target<'_> {
    fn file(&self) -> Option<&Path> {
        match self.layer {
            ConfigLayer::User => Some(self.files.user),
            ConfigLayer::UserProject => self.files.user_project,
            ConfigLayer::Project if self.allow_repo => self.files.project,
            _ => None,
        }
    }
}

/// A finished write.
#[derive(Debug)]
pub struct Written {
    pub result: ConfigWriteResult,
    /// `${credential:…}` names the file referred to before the write and no longer does.
    pub dropped_credentials: Vec<String>,
}

/// The first 16 hex digits of the SHA-256 of a file's bytes, or "absent".
pub fn version(bytes: Option<&[u8]>) -> String {
    match bytes {
        None => "absent".to_string(),
        Some(bytes) => {
            let digest = ring::digest::digest(&ring::digest::SHA256, bytes);
            digest.as_ref()[..8].iter().map(|b| format!("{b:02x}")).collect()
        }
    }
}

pub fn file_version(path: &Path) -> String {
    version(std::fs::read(path).ok().as_deref())
}

/// `config/read`: the merged configuration, the origin of each key, and the layers' files.
pub fn read(loaded: &LoadedConfig, files: &LayerFiles<'_>, with_layers: bool) -> ConfigReadResult {
    let mut layers = Vec::new();
    if with_layers {
        let mut add = |layer: ConfigLayer, path: &Path| {
            layers.push(ConfigLayerInfo {
                layer,
                file: path.to_path_buf(),
                version: file_version(path),
                writable: layer.writable(),
                exists: path.exists(),
            });
        };
        add(ConfigLayer::User, files.user);
        if let Some(project) = files.project {
            add(ConfigLayer::Project, project);
        }
        if let Some(user_project) = files.user_project {
            add(ConfigLayer::UserProject, user_project);
        }
    }
    ConfigReadResult {
        config: serde_json::to_value(&loaded.merged).unwrap_or(Json::Null),
        origins: loaded.origins.clone(),
        layers,
    }
}

/// Applies `edits` to the target file (§5.1): lock, version check, edits, checks of the file
/// and of the merged configuration, then an atomic replace.
pub fn write(target: &Target<'_>, edits: &[ConfigEdit], expected_version: Option<&str>) -> Result<Written, WriteError> {
    let Some(path) = target.file() else {
        return Err(WriteError::Invalid(vec![FieldError::new("", "only the user's own files can be written")]));
    };
    let io_error = |source| WriteError::Io { path: path.to_path_buf(), source };
    let dir = path.parent().unwrap_or(Path::new("."));
    // The user's own files are private; a repository's are the team's.
    let private = target.layer != ConfigLayer::Project;
    if private { create_private_dir(dir) } else { std::fs::create_dir_all(dir) }.map_err(io_error)?;
    let _lock = lock(path, target.files.user.parent().unwrap_or(Path::new(".")))?;

    let old = match std::fs::read(path) {
        Ok(bytes) => Some(bytes),
        Err(err) if err.kind() == io::ErrorKind::NotFound => None,
        Err(err) => return Err(io_error(err)),
    };
    let current = version(old.as_deref());
    if let Some(expected) = expected_version
        && expected != current
    {
        return Err(WriteError::VersionConflict { version: current });
    }
    let old_text = old.as_deref().map(String::from_utf8_lossy).map(|t| t.into_owned()).unwrap_or_default();
    let mut doc: DocumentMut = old_text
        .parse()
        .map_err(|e: toml_edit::TomlError| WriteError::Invalid(vec![FieldError::new("", format!("the file cannot be read: {e}"))]))?;
    for edit in edits {
        apply(&mut doc, edit).map_err(|message| WriteError::Invalid(vec![FieldError::new(&edit.key_path, message)]))?;
    }
    let mut new_text = doc.to_string();
    if old_text.trim().is_empty() {
        // A new or emptied file starts with its header, then its first table.
        new_text = format!("{}{}", target.header.as_deref().unwrap_or(""), new_text.trim_start_matches('\n'));
    }

    // The file alone, then everything together.
    let first_key = edits.first().map(|e| e.key_path.as_str()).unwrap_or("");
    let table = config::check_file(&new_text, path).map_err(|e| WriteError::Invalid(vec![FieldError::new(first_key, e.to_string())]))?;
    let loaded = config::load_layers_with(&target.files, target.trusted, target.overrides, Some((target.layer, table)))
        .map_err(|e| WriteError::Invalid(vec![FieldError::new(first_key, e.to_string())]))?;
    let problems = server_problems(&loaded, edits, target.files.user.parent().unwrap_or(Path::new(".")));
    if !problems.is_empty() {
        return Err(WriteError::Invalid(problems));
    }
    let overridden_by = overrides_of(&loaded, edits, target);

    replace(path, new_text.as_bytes(), private).map_err(io_error)?;
    let before = credential_refs(&old_text);
    let after = credential_refs(&new_text);
    Ok(Written {
        result: ConfigWriteResult {
            status: if overridden_by.is_empty() { ConfigWriteStatus::Ok } else { ConfigWriteStatus::Overridden },
            version: version(Some(new_text.as_bytes())),
            file: path.to_path_buf(),
            overridden_by,
        },
        dropped_credentials: before.difference(&after).cloned().collect(),
    })
}

/// Edited servers that would not start: a missing command or URL, a bad name.
fn server_problems(loaded: &LoadedConfig, edits: &[ConfigEdit], home: &Path) -> Vec<FieldError> {
    let mut problems = Vec::new();
    let mut seen = BTreeSet::new();
    for edit in edits {
        let Ok(segments) = parse_key_path(&edit.key_path) else { continue };
        if segments.first().map(String::as_str) != Some("mcp_servers") || segments.len() < 2 {
            continue;
        }
        let name = &segments[1];
        if !seen.insert(name.clone()) {
            continue;
        }
        let Some(server) = loaded.config.mcp_servers.iter().find(|s| &s.name == name) else { continue };
        if !server.is_enabled() {
            continue;
        }
        if !crate::connectors::authorized(home, server) {
            problems.push(FieldError::new(&config::join_key("mcp_servers", name), "连接器尚未完成授权，不能启用"));
        }
        if let Err(problem) = server.validate() {
            problems.push(FieldError::new(&config::join_key("mcp_servers", name), problem));
        }
    }
    problems
}

/// The written keys that a layer above the target still sets.
fn overrides_of(loaded: &LoadedConfig, edits: &[ConfigEdit], target: &Target<'_>) -> Vec<ConfigOverride> {
    let mut out: Vec<ConfigOverride> = Vec::new();
    for edit in edits {
        let Ok(segments) = parse_key_path(&edit.key_path) else { continue };
        let prefix = segments.iter().fold(String::new(), |acc, s| config::join_key(&acc, s));
        for (key, layer) in loaded.origins.range(prefix.clone()..) {
            if !(key == &prefix || key.starts_with(&format!("{prefix}."))) {
                break;
            }
            if *layer > target.layer && !out.iter().any(|o| o.key_path == *key) {
                out.push(ConfigOverride { key_path: key.clone(), layer: *layer, file: layer_file(&target.files, *layer) });
            }
        }
    }
    out
}

fn layer_file(files: &LayerFiles<'_>, layer: ConfigLayer) -> Option<PathBuf> {
    match layer {
        ConfigLayer::User => Some(files.user.to_path_buf()),
        ConfigLayer::Project => files.project.map(Path::to_path_buf),
        ConfigLayer::UserProject => files.user_project.map(Path::to_path_buf),
        ConfigLayer::ProjectJson | ConfigLayer::Flag => None,
    }
}

// ---- Key paths and edits ----

/// `a.b."c.d"` → ["a", "b", "c.d"].
pub fn parse_key_path(path: &str) -> Result<Vec<String>, String> {
    let mut segments = Vec::new();
    let mut chars = path.chars().peekable();
    loop {
        let mut segment = String::new();
        if chars.peek() == Some(&'"') {
            chars.next();
            loop {
                match chars.next() {
                    Some('"') => break,
                    Some('\\') => match chars.next() {
                        Some(c) => segment.push(c),
                        None => return Err(format!("unfinished quote in key path '{path}'")),
                    },
                    Some(c) => segment.push(c),
                    None => return Err(format!("unfinished quote in key path '{path}'")),
                }
            }
        } else {
            while let Some(&c) = chars.peek() {
                if c == '.' {
                    break;
                }
                if !(c.is_ascii_alphanumeric() || c == '_' || c == '-') {
                    return Err(format!("key path '{path}': quote the key containing '{c}'"));
                }
                segment.push(c);
                chars.next();
            }
            if segment.is_empty() {
                return Err(format!("empty key in key path '{path}'"));
            }
        }
        segments.push(segment);
        match chars.next() {
            None => return Ok(segments),
            Some('.') => continue,
            Some(c) => return Err(format!("key path '{path}': unexpected '{c}'")),
        }
    }
}

fn apply(doc: &mut DocumentMut, edit: &ConfigEdit) -> Result<(), String> {
    let segments = parse_key_path(&edit.key_path)?;
    let (last, parents) = segments.split_last().expect("at least one segment");
    if edit.value.is_null() {
        remove_path(doc.as_table_mut(), &segments);
        return Ok(());
    }
    let (parent, inline) = parent_table(doc.as_table_mut(), parents)?;
    let depth = segments.len();
    match (edit.merge, &edit.value) {
        (ConfigMerge::Upsert, Json::Object(fields)) => {
            if parent.get(last).is_none() {
                parent.insert(last, new_table_item(depth, inline));
            }
            let item = parent.get_mut(last).expect("just inserted");
            let inline = item.is_inline_table();
            let table = item.as_table_like_mut().ok_or_else(|| format!("'{}' is not a table", edit.key_path))?;
            for (key, value) in fields {
                if value.is_null() {
                    table.remove(key);
                } else {
                    table.insert(key, to_item(value, depth + 1, inline)?);
                }
            }
        }
        (_, value) => {
            let keep_table = parent.get(last).is_some_and(Item::is_table);
            let item = if keep_table && value.is_object() { to_table(value, depth)? } else { to_item(value, depth, inline)? };
            parent.insert(last, item);
        }
    }
    Ok(())
}

/// The table holding the last segment, made where missing (implicit sections up to two
/// levels, inline tables below), and whether it is an inline table or inside one.
fn parent_table<'a>(root: &'a mut toml_edit::Table, parents: &[String]) -> Result<(&'a mut dyn TableLike, bool), String> {
    let mut current: &mut dyn TableLike = root;
    let mut inline = false;
    for (i, segment) in parents.iter().enumerate() {
        if current.get(segment).is_none() {
            let item = if inline || i >= 2 {
                Item::Value(toml_edit::Value::InlineTable(toml_edit::InlineTable::new()))
            } else {
                let mut table = toml_edit::Table::new();
                table.set_implicit(true);
                Item::Table(table)
            };
            current.insert(segment, item);
        }
        let item = current.get_mut(segment).expect("present");
        inline = inline || item.is_inline_table();
        current = item
            .as_table_like_mut()
            .ok_or_else(|| format!("'{}' is not a table", parents[..=i].join(".")))?;
    }
    Ok((current, inline))
}

/// A new table at `depth`: a section of its own (`[mcp_servers.github]`) up to two levels,
/// an inline table (`env = { … }`) below that.
fn new_table_item(depth: usize, inside_inline: bool) -> Item {
    if depth <= 2 && !inside_inline {
        Item::Table(toml_edit::Table::new())
    } else {
        Item::Value(toml_edit::Value::InlineTable(toml_edit::InlineTable::new()))
    }
}

fn to_table(value: &Json, depth: usize) -> Result<Item, String> {
    let Json::Object(fields) = value else { return to_item(value, depth, false) };
    let mut table = toml_edit::Table::new();
    // A blank line before a section that her adds to a file.
    table.decor_mut().set_prefix("\n");
    let mut fields: Vec<(&String, &Json)> = fields.iter().collect();
    fields.sort_by_key(|(key, _)| (field_rank(key), key.to_string()));
    for (key, value) in fields {
        if value.is_null() {
            continue;
        }
        table.insert(key, Item::Value(to_value(value)?));
    }
    Ok(Item::Table(table))
}

/// Fields in the order people write them: what the server is, then how to reach it.
fn field_rank(key: &str) -> usize {
    const ORDER: &[&str] = &["description", "command", "args", "env", "env_vars", "cwd", "url", "headers", "bearer_token_env_var", "enabled"];
    ORDER.iter().position(|k| *k == key).unwrap_or(ORDER.len())
}

fn to_item(value: &Json, depth: usize, inside_inline: bool) -> Result<Item, String> {
    if value.is_object() && depth <= 2 && !inside_inline {
        return to_table(value, depth);
    }
    Ok(Item::Value(to_value(value)?))
}

fn to_value(value: &Json) -> Result<toml_edit::Value, String> {
    Ok(match value {
        Json::Bool(b) => toml_edit::Value::from(*b),
        Json::Number(n) => match (n.as_i64(), n.as_f64()) {
            (Some(i), _) => toml_edit::Value::from(i),
            (None, Some(f)) => toml_edit::Value::from(f),
            _ => return Err(format!("number {n} does not fit")),
        },
        Json::String(s) => toml_edit::Value::from(s.as_str()),
        Json::Array(items) => {
            let mut array = toml_edit::Array::new();
            for item in items {
                array.push(to_value(item)?);
            }
            toml_edit::Value::Array(array)
        }
        Json::Object(fields) => {
            let mut table = toml_edit::InlineTable::new();
            for (key, value) in fields {
                if !value.is_null() {
                    table.insert(key, to_value(value)?);
                }
            }
            toml_edit::Value::InlineTable(table)
        }
        Json::Null => return Err("null can only remove a key".to_string()),
    })
}

/// Removes the key, then the tables that the removal left empty.
fn remove_path(table: &mut dyn TableLike, segments: &[String]) {
    let (first, rest) = segments.split_first().expect("non-empty");
    if rest.is_empty() {
        table.remove(first);
        return;
    }
    let mut emptied = false;
    if let Some(child) = table.get_mut(first).and_then(Item::as_table_like_mut) {
        remove_path(child, rest);
        emptied = child.is_empty();
    }
    if emptied {
        table.remove(first);
    }
}

// ---- Files ----

fn create_private_dir(dir: &Path) -> io::Result<()> {
    if dir.as_os_str().is_empty() || dir.exists() {
        return Ok(());
    }
    std::fs::create_dir_all(dir)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700))?;
    }
    Ok(())
}

/// An exclusive lock for writing `path`, waited for up to two seconds. Lock files are kept in
/// `run/locks` of her's own directory (`her_dir`), never beside a repository's file.
pub fn lock(path: &Path, her_dir: &Path) -> Result<File, WriteError> {
    let dir = her_dir.join("run").join("locks");
    create_private_dir(&dir).map_err(|source| WriteError::Io { path: dir.clone(), source })?;
    // The same file however it is spelled: its directory exists by now.
    let real = match (path.parent().and_then(|p| std::fs::canonicalize(p).ok()), path.file_name()) {
        (Some(parent), Some(name)) => parent.join(name),
        _ => path.to_path_buf(),
    };
    let digest = ring::digest::digest(&ring::digest::SHA256, real.as_os_str().as_encoded_bytes());
    let id: String = digest.as_ref()[..8].iter().map(|b| format!("{b:02x}")).collect();
    let lock_path = dir.join(format!("config-{id}.lock"));
    let file = File::options()
        .create(true)
        .truncate(false)
        .write(true)
        .open(&lock_path)
        .map_err(|source| WriteError::Io { path: lock_path.clone(), source })?;
    let start = Instant::now();
    loop {
        match file.try_lock() {
            Ok(()) => return Ok(file),
            Err(std::fs::TryLockError::WouldBlock) if start.elapsed() < LOCK_WAIT => std::thread::sleep(Duration::from_millis(20)),
            Err(std::fs::TryLockError::WouldBlock) => return Err(WriteError::Busy),
            Err(std::fs::TryLockError::Error(source)) => return Err(WriteError::Io { path: lock_path, source }),
        }
    }
}

fn sibling(path: &Path, suffix: &str) -> PathBuf {
    let mut name = path.file_name().map(|n| n.to_os_string()).unwrap_or_default();
    name.push(suffix);
    path.with_file_name(name)
}

/// Writes a temporary file next to `path`, then renames it over `path`; the file is private
/// (0600).
pub fn replace_file(path: &Path, bytes: &[u8]) -> io::Result<()> {
    replace(path, bytes, true)
}

/// [`replace_file`]; a file that is not `private` keeps its mode, or gets the default one.
fn replace(path: &Path, bytes: &[u8], private: bool) -> io::Result<()> {
    let tmp = sibling(path, &format!(".tmp-{}", std::process::id()));
    let result = (|| {
        let mut file = File::create(&tmp)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = if private { Some(0o600) } else { std::fs::metadata(path).ok().map(|m| m.permissions().mode() & 0o7777) };
            if let Some(mode) = mode {
                file.set_permissions(std::fs::Permissions::from_mode(mode))?;
            }
        }
        #[cfg(not(unix))]
        let _ = private;
        file.write_all(bytes)?;
        file.sync_all()?;
        std::fs::rename(&tmp, path)
    })();
    if result.is_err() {
        let _ = std::fs::remove_file(&tmp);
    }
    result
}

// ---- Credentials ----

/// The names in `${credential:<name>}` references.
pub fn credential_refs(text: &str) -> BTreeSet<String> {
    let mut names = BTreeSet::new();
    let mut rest = text;
    while let Some(start) = rest.find("${credential:") {
        let after = &rest[start + "${credential:".len()..];
        let Some(end) = after.find('}') else { break };
        names.insert(after[..end].to_string());
        rest = &after[end + 1..];
    }
    names
}

/// A name `credentials/set` takes: `mcp.`, `mcp-oauth.` or `web-search.`, then 1-96 letters,
/// digits, `.`, `_` or `-`.
pub fn valid_credential_name(name: &str) -> bool {
    let rest = name.strip_prefix("mcp.").or_else(|| name.strip_prefix("mcp-oauth.")).or_else(|| name.strip_prefix(crate::web_search::CREDENTIAL_PREFIX));
    rest.is_some_and(|rest| {
        !rest.is_empty() && rest.len() <= 96 && rest.chars().all(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | '-'))
    })
}

/// Of `names`, those that no file of the user's still refers to: the user's configuration
/// and every personal project file. Files in repositories are not the user's to count.
pub fn unused_credentials(home: &HerHome, names: &[String]) -> Vec<String> {
    if names.is_empty() {
        return Vec::new();
    }
    let mut used = BTreeSet::new();
    let mut scan = |path: &Path| {
        if let Ok(text) = std::fs::read_to_string(path) {
            used.extend(credential_refs(&text));
        }
    };
    scan(&home.config_file());
    if let Ok(entries) = std::fs::read_dir(home.projects_dir()) {
        for entry in entries.flatten() {
            scan(&entry.path().join("config.toml"));
        }
    }
    names.iter().filter(|n| !used.contains(*n)).cloned().collect()
}

/// The credentials a server's configuration refers to, in order.
pub fn server_credentials(server: &her_protocol::McpServerConfig) -> Vec<String> {
    let mut text = String::new();
    for value in server.env.values().chain(server.headers.values()).chain(server.args.iter()) {
        text.push_str(value);
    }
    for value in [&server.command, &server.url].into_iter().flatten() {
        text.push_str(value);
    }
    credential_refs(&text).into_iter().collect()
}

/// Which edits touch MCP servers and skills, for the notifications that follow a write.
pub fn touched_sections(edits: &[ConfigEdit]) -> BTreeMap<&'static str, bool> {
    let mut out = BTreeMap::from([("mcp_servers", false), ("skills", false)]);
    for edit in edits {
        let first = parse_key_path(&edit.key_path).ok().and_then(|s| s.into_iter().next()).unwrap_or_default();
        match first.as_str() {
            "mcp_servers" | "mcp" => {
                out.insert("mcp_servers", true);
            }
            "skills" => {
                out.insert("skills", true);
            }
            _ => {}
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn edit(key_path: &str, value: Json) -> ConfigEdit {
        ConfigEdit { key_path: key_path.into(), value, merge: ConfigMerge::Replace }
    }

    fn target<'a>(user: &'a Path, project: Option<&'a Path>, user_project: Option<&'a Path>, layer: ConfigLayer) -> Target<'a> {
        Target {
            layer,
            files: LayerFiles { user, project, user_project, project_root: None },
            trusted: true,
            overrides: &[],
            header: None,
            allow_repo: false,
        }
    }

    #[test]
    fn key_paths() {
        assert_eq!(parse_key_path("mcp_servers.github.enabled").unwrap(), vec!["mcp_servers", "github", "enabled"]);
        assert_eq!(parse_key_path(r#"mcp_servers.x.headers."X-Team.Id""#).unwrap(), vec!["mcp_servers", "x", "headers", "X-Team.Id"]);
        assert!(parse_key_path("a..b").is_err());
        assert!(parse_key_path("a.b c").is_err());
        assert!(parse_key_path("a.\"b").is_err());
    }

    #[test]
    fn edits_keep_the_rest_of_the_file_byte_for_byte() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("config.toml");
        let original = "# my settings\nmodel = \"deepseek/deepseek-flash\"   # fast\n\n[mcp_servers.docs]\nurl = \"https://docs.example.com/mcp\" # keep\n";
        std::fs::write(&user, original).unwrap();
        let t = target(&user, None, None, ConfigLayer::User);
        let written = write(
            &t,
            &[
                edit("mcp_servers.github", json!({"url": "https://api.githubcopilot.com/mcp/", "headers": {"Authorization": "Bearer ${credential:mcp.github}"}})),
                edit("mcp_servers.docs.enabled", json!(false)),
            ],
            Some(&version(Some(original.as_bytes()))),
        )
        .unwrap();
        let text = std::fs::read_to_string(&user).unwrap();
        assert!(text.starts_with("# my settings\nmodel = \"deepseek/deepseek-flash\"   # fast\n"), "{text}");
        assert!(text.contains("url = \"https://docs.example.com/mcp\" # keep\nenabled = false\n"), "{text}");
        assert!(text.contains("[mcp_servers.github]\nurl = \"https://api.githubcopilot.com/mcp/\"\nheaders = { Authorization = \"Bearer ${credential:mcp.github}\" }\n"), "{text}");
        assert_eq!(written.result.status, ConfigWriteStatus::Ok);
        assert_eq!(written.result.version, file_version(&user));

        // Removing the server drops its credential and the emptied table.
        let written = write(&t, &[edit("mcp_servers.github", Json::Null)], None).unwrap();
        assert_eq!(written.dropped_credentials, vec!["mcp.github"]);
        let text = std::fs::read_to_string(&user).unwrap();
        assert!(!text.contains("github"), "{text}");
        assert!(text.contains("[mcp_servers.docs]"), "{text}");
    }

    #[test]
    fn versions_conflicts_and_validation() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("config.toml");
        let t = target(&user, None, None, ConfigLayer::User);
        // A file that does not exist yet has the version "absent".
        write(&t, &[edit("mcp_servers.a.command", json!("a-mcp"))], Some("absent")).unwrap();
        let err = write(&t, &[edit("mcp_servers.b.command", json!("b-mcp"))], Some("absent")).unwrap_err();
        assert!(matches!(err, WriteError::VersionConflict { ref version } if *version == file_version(&user)), "{err}");

        // Unknown keys, and servers that could not start, are refused and the file stays.
        let before = std::fs::read_to_string(&user).unwrap();
        let err = write(&t, &[edit("mcp_servers.a.comand", json!("x"))], None).unwrap_err();
        assert!(matches!(err, WriteError::Invalid(_)) && err.to_string().contains("comand"), "{err}");
        let err = write(&t, &[edit("mcp_servers.c.enabled", json!(true))], None).unwrap_err();
        let WriteError::Invalid(problems) = err else { panic!("invalid") };
        assert_eq!(problems[0].key_path, "mcp_servers.c");
        let err = write(&t, &[edit("mcp_servers.bad name", json!({"command": "x"}))], None).unwrap_err();
        assert!(matches!(err, WriteError::Invalid(_)), "{err}");
        assert_eq!(std::fs::read_to_string(&user).unwrap(), before);
    }

    #[test]
    fn overrides_report_the_higher_layer() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("config.toml");
        let project = dir.path().join("project.toml");
        let mine = dir.path().join("mine.toml");
        std::fs::write(&project, "[mcp_servers.db]\ncommand = \"db-mcp\"\n").unwrap();
        std::fs::write(&mine, "[mcp_servers.gh]\nenabled = true\n").unwrap();
        // Turning gh off for every project: this project's personal file turns it back on.
        let t = target(&user, Some(&project), Some(&mine), ConfigLayer::User);
        std::fs::write(&user, "[mcp_servers.gh]\ncommand = \"gh-mcp\"\n").unwrap();
        let written = write(&t, &[edit("mcp_servers.gh.enabled", json!(false))], None).unwrap();
        assert_eq!(written.result.status, ConfigWriteStatus::Overridden);
        assert_eq!(written.result.overridden_by[0].layer, ConfigLayer::UserProject);
        assert_eq!(written.result.overridden_by[0].key_path, "mcp_servers.gh.enabled");

        // Turning the project's server off for oneself: the personal layer is the highest.
        let t = target(&user, Some(&project), Some(&mine), ConfigLayer::UserProject);
        let written = write(&t, &[edit("mcp_servers.db.enabled", json!(false))], None).unwrap();
        assert_eq!(written.result.status, ConfigWriteStatus::Ok);
        let loaded = config::load_layers(&t.files, true, &[]).unwrap();
        let db = loaded.config.mcp_servers.iter().find(|s| s.name == "db").unwrap();
        assert!(!db.is_enabled());
        // On again: the key goes away, and the table with it.
        write(&t, &[edit("mcp_servers.db.enabled", Json::Null)], None).unwrap();
        let text = std::fs::read_to_string(&mine).unwrap();
        assert!(!text.contains("db"), "{text}");
    }

    #[test]
    fn upsert_keeps_other_fields_and_headers_go_on_new_files() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("config.toml");
        let mine = dir.path().join("projects/x-1/config.toml");
        let mut t = target(&user, None, Some(&mine), ConfigLayer::UserProject);
        t.header = Some(user_project_header(Path::new("/work/x")));
        write(&t, &[edit("mcp_servers.pg", json!({"command": "uvx", "args": ["mcp-server-postgres"], "env": {"DATABASE_URL": "${credential:mcp.pg.DATABASE_URL}"}}))], None).unwrap();
        let upsert = ConfigEdit { key_path: "mcp_servers.pg".into(), value: json!({"args": ["pg-mcp", "--ro"]}), merge: ConfigMerge::Upsert };
        write(&t, &[upsert], None).unwrap();
        let text = std::fs::read_to_string(&mine).unwrap();
        assert!(text.starts_with("# Personal settings for /work/x\n"), "{text}");
        assert!(text.contains("command = \"uvx\""), "{text}");
        assert!(text.contains("args = [\"pg-mcp\", \"--ro\"]"), "{text}");
        assert!(text.contains("DATABASE_URL = \"${credential:mcp.pg.DATABASE_URL}\""), "{text}");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&mine).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o600);
        }
    }

    #[test]
    fn readonly_layers_are_refused_and_credentials_counted() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("config.toml");
        let t = target(&user, None, None, ConfigLayer::Project);
        assert!(matches!(write(&t, &[edit("model", json!("x"))], None), Err(WriteError::Invalid(_))));
        assert!(valid_credential_name("mcp.github"));
        assert!(valid_credential_name("mcp.pg.DATABASE_URL"));
        assert!(valid_credential_name("web-search.tavily"));
        assert!(!valid_credential_name("deepseek"));
        assert!(!valid_credential_name("mcp."));
        assert!(!valid_credential_name("mcp.a b"));

        let home = HerHome::new(dir.path());
        std::fs::write(home.config_file(), "[mcp_servers.a]\nurl = \"https://a\"\nheaders = { Authorization = \"Bearer ${credential:mcp.a}\" }\n").unwrap();
        std::fs::create_dir_all(home.projects_dir().join("p-1")).unwrap();
        std::fs::write(home.projects_dir().join("p-1/config.toml"), "[mcp_servers.b]\ncommand = \"b\"\nenv = { T = \"${credential:mcp.b.T}\" }\n").unwrap();
        let unused = unused_credentials(&home, &["mcp.a".into(), "mcp.b.T".into(), "mcp.c".into()]);
        assert_eq!(unused, vec!["mcp.c"]);
    }

    #[test]
    fn concurrent_writers_lose_nothing() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("config.toml");
        let handles: Vec<_> = (0..8)
            .map(|i| {
                let user = user.clone();
                std::thread::spawn(move || {
                    let t = target(&user, None, None, ConfigLayer::User);
                    write(&t, &[edit(&format!("mcp_servers.s{i}.command"), json!(format!("s{i}")))], None).unwrap();
                })
            })
            .collect();
        for h in handles {
            h.join().unwrap();
        }
        let text = std::fs::read_to_string(&user).unwrap();
        for i in 0..8 {
            assert!(text.contains(&format!("[mcp_servers.s{i}]")), "{text}");
        }
    }
}
