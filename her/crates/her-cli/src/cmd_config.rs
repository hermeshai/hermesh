//! `her config`: the layered configuration, read and changed from the command line
//! (standalone-cli-design §4.2).

use anyhow::Context;
use anyhow::bail;
use her_core::config;
use her_core::config_edit;
use her_core::paths::HerHome;
use her_core::runtime::Workspace;
use her_protocol::customize::ConfigEdit;
use her_protocol::customize::ConfigLayer;
use her_protocol::customize::ConfigMerge;
use her_protocol::customize::ConfigReadResult;
use serde_json::Value;
use serde_json::json;
use std::path::PathBuf;

use crate::cmd_customize::shorten;
use crate::setup;

/// Settings that decide what the agent may do. A model given permission to run `her config` has
/// no business changing them; this is one more step, not a boundary (§4.2).
const PERMISSION_KEYS: &[&str] = &[
    "permission_mode",
    "approval_mode",
    "rules",
    "hooks",
    "mcp_servers",
    "mcp.project_json",
    "extra_writable_roots",
    "tools.enabled",
    "tools.bash",
    "browser.sites",
    "browser.script_enabled",
    "desktop",
    "agents.enabled",
    "system_prompt_file",
];

fn is_permission_key(key: &str) -> bool {
    PERMISSION_KEYS.iter().any(|known| key == *known || key.strip_prefix(known).is_some_and(|rest| rest.starts_with('.')))
}

/// Which file a command means; `None` is every layer for reading and the user's file for writing.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Scope {
    Global,
    Local,
    Project,
}

struct Here {
    home: HerHome,
    workspace: Workspace,
    overrides: Vec<String>,
}

impl Here {
    fn open(overrides: &[String]) -> anyhow::Result<Self> {
        let home = setup::home()?;
        let mut warnings = Vec::new();
        let overrides: Vec<String> = setup::env_overrides(&mut warnings)?.into_iter().chain(overrides.iter().cloned()).collect();
        let workspace = Workspace::open(&home, &std::env::current_dir()?, &overrides)?;
        Ok(Self { home, workspace, overrides })
    }

    fn project_dir(&self) -> PathBuf {
        self.workspace.project_root.clone().unwrap_or_else(|| self.workspace.cwd.clone())
    }

    fn read(&self) -> anyhow::Result<ConfigReadResult> {
        let user = self.home.config_file();
        let dir = self.project_dir();
        let project = dir.join(".her").join("config.toml");
        let files = config::LayerFiles {
            user: &user,
            project: Some(&project),
            user_project: self.workspace.user_project_file.as_deref(),
            project_root: Some(&dir),
        };
        let loaded = config::load_layers(&files, self.workspace.trusted, &self.overrides)?;
        Ok(config_edit::read(&loaded, &files, true))
    }

    fn file_of(&self, scope: Scope) -> anyhow::Result<PathBuf> {
        match scope {
            Scope::Global => Ok(self.home.config_file()),
            Scope::Local => self.workspace.user_project_file.clone().context("--local needs a project: run it inside a repository"),
            Scope::Project => match &self.workspace.project_root {
                Some(root) => Ok(root.join(".her").join("config.toml")),
                None => bail!("--project needs a project: run it inside a repository"),
            },
        }
    }
}

fn layer_of(scope: Option<Scope>) -> ConfigLayer {
    match scope {
        None | Some(Scope::Global) => ConfigLayer::User,
        Some(Scope::Local) => ConfigLayer::UserProject,
        Some(Scope::Project) => ConfigLayer::Project,
    }
}

/// `a.b = 1` for every leaf of a value. Tables are walked; arrays are values.
fn leaves(prefix: &str, value: &Value, out: &mut Vec<(String, Value)>) {
    match value {
        Value::Object(map) => {
            for (key, value) in map {
                let path = if prefix.is_empty() { key.clone() } else { format!("{prefix}.{key}") };
                leaves(&path, value, out);
            }
        }
        other => out.push((prefix.to_string(), other.clone())),
    }
}

fn walk<'a>(mut value: &'a Value, path: &[String]) -> Option<&'a Value> {
    for part in path {
        value = value.get(part)?;
    }
    Some(value)
}

/// A value as `her config get` prints it: a string without its quotes, so that a script can use it.
fn plain(value: &Value) -> String {
    match value {
        Value::String(text) => text.clone(),
        other => other.to_string(),
    }
}

pub fn list(overrides: &[String], show_origin: bool, as_json: bool) -> anyhow::Result<u8> {
    let here = Here::open(overrides)?;
    let read = here.read()?;
    if as_json {
        println!("{}", serde_json::to_string_pretty(&read)?);
        return Ok(0);
    }
    let mut rows = Vec::new();
    leaves("", &read.config, &mut rows);
    for (key, value) in rows {
        if show_origin {
            let origin = origin_of(&read, &key);
            println!("{origin:<14}{key} = {value}");
        } else {
            println!("{key} = {value}");
        }
    }
    Ok(0)
}

/// The layer that set `key` or, for a table, one of its leaves.
fn origin_of(read: &ConfigReadResult, key: &str) -> &'static str {
    let inside = format!("{key}.");
    read.origins
        .get(key)
        .or_else(|| read.origins.iter().find(|(path, _)| path.starts_with(&inside)).map(|(_, layer)| layer))
        .map_or("default", |layer| layer.as_str())
}

/// Exit 1 when the key has no value, as `git config` does: the key itself may be fine.
pub fn get(overrides: &[String], key: &str, scope: Option<Scope>, show_origin: bool, as_json: bool) -> anyhow::Result<u8> {
    let path = config_edit::parse_key_path(key).map_err(anyhow::Error::msg)?;
    check_known(key)?;
    let here = Here::open(overrides)?;
    let read = here.read()?;
    let (value, origin) = match scope {
        None => (walk(&read.config, &path).cloned(), walk(&read.config, &path).map(|_| origin_of(&read, key))),
        Some(scope) => {
            let file = here.file_of(scope)?;
            let table: Value = match std::fs::read_to_string(&file) {
                Ok(text) => serde_json::to_value(toml::from_str::<toml::Table>(&text).with_context(|| format!("cannot read {}", file.display()))?)?,
                Err(_) => json!({}),
            };
            let value = walk(&table, &path).cloned();
            let origin = value.as_ref().map(|_| layer_of(Some(scope)).as_str());
            (value, origin)
        }
    };
    if as_json {
        println!("{}", json!({ "key": key, "value": value, "origin": origin }));
        return Ok(u8::from(value.is_none()));
    }
    let Some(value) = value else { return Ok(1) };
    if show_origin {
        println!("{:<14}{}", origin.unwrap_or("default"), plain(&value));
    } else {
        println!("{}", plain(&value));
    }
    Ok(0)
}

/// `set`, `unset` and `get` on a key her does not know is a mistake worth a word; a file with
/// such a key is only warned about (§11.2).
fn check_known(key: &str) -> anyhow::Result<()> {
    match config::key_problem(key) {
        Some(message) => bail!("{message}"),
        None => Ok(()),
    }
}

fn refuse_for_agents(key: &str) -> anyhow::Result<()> {
    if std::env::var_os("AI_AGENT").is_some() && is_permission_key(key) {
        bail!("{key} decides what the agent may do: run this in a terminal yourself (refused because AI_AGENT is set)");
    }
    Ok(())
}

/// What a value on the command line means: TOML when it reads as TOML, otherwise a string.
fn parse_value(text: &str) -> anyhow::Result<Value> {
    let parsed = toml::from_str::<toml::Table>(&format!("v = {text}")).ok().and_then(|mut table| table.remove("v"));
    match parsed {
        Some(value) => Ok(serde_json::to_value(value)?),
        None => Ok(Value::String(text.to_string())),
    }
}

fn write(here: &Here, scope: Option<Scope>, key: &str, value: Value) -> anyhow::Result<her_protocol::customize::ConfigWriteResult> {
    let layer = layer_of(scope);
    if let Some(scope) = scope {
        here.file_of(scope)?;
    }
    let edits = [ConfigEdit { key_path: key.to_string(), value, merge: ConfigMerge::Replace }];
    crate::cmd_customize::write(&here.home, &here.workspace, layer, &edits)
}

pub fn set(overrides: &[String], key: &str, value: &str, scope: Option<Scope>) -> anyhow::Result<u8> {
    config_edit::parse_key_path(key).map_err(anyhow::Error::msg)?;
    check_known(key)?;
    refuse_for_agents(key)?;
    let here = Here::open(overrides)?;
    let written = write(&here, scope, key, parse_value(value)?)?;
    println!("{key} set to {value} in {}", shorten(&here.home, &written.file));
    for over in &written.overridden_by {
        eprintln!("note: {} still wins over it: {}", over.layer.as_str(), over.key_path);
    }
    Ok(0)
}

pub fn unset(overrides: &[String], key: &str, scope: Option<Scope>) -> anyhow::Result<u8> {
    config_edit::parse_key_path(key).map_err(anyhow::Error::msg)?;
    check_known(key)?;
    refuse_for_agents(key)?;
    let here = Here::open(overrides)?;
    let written = write(&here, scope, key, Value::Null)?;
    println!("{key} unset in {}", shorten(&here.home, &written.file));
    Ok(0)
}

pub fn path(scope: Option<Scope>) -> anyhow::Result<u8> {
    let here = Here::open(&[])?;
    println!("{}", here.file_of(scope.unwrap_or(Scope::Global))?.display());
    Ok(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn values_read_as_toml_or_as_text() {
        assert_eq!(parse_value("3").unwrap(), json!(3));
        assert_eq!(parse_value("true").unwrap(), json!(true));
        assert_eq!(parse_value("[\"read\"]").unwrap(), json!(["read"]));
        assert_eq!(parse_value("low").unwrap(), json!("low"));
        assert_eq!(parse_value("provider/model").unwrap(), json!("provider/model"));
    }

    #[test]
    fn permission_keys_cover_what_is_inside_them() {
        assert!(is_permission_key("permission_mode"));
        assert!(is_permission_key("mcp_servers.mail.approval"));
        assert!(is_permission_key("tools.bash.pass_provider_keys"));
        assert!(!is_permission_key("thinking"));
        assert!(!is_permission_key("rules_extra"));
    }
}
