//! Each MCP server's last connection: the result and its tool list
//! (docs/desktop/customize-design.md §4.8). Sessions write it when a server becomes ready or
//! fails, and so does `mcp/check`; the 自定义 page reads it, so showing a server's state never
//! starts the server.

use std::collections::HashMap;
use std::io;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Mutex;
use std::sync::OnceLock;
use std::time::Duration;
use std::time::Instant;

use her_protocol::McpServerConfig;
use her_protocol::customize::McpToolSummary;
use serde::Deserialize;
use serde::Serialize;

const MAX_DESCRIPTION: usize = 300;
const MAX_FILE_BYTES: usize = 1 << 20;
const MAX_FILES: usize = 200;
/// A session writes the same server's entry at most this often.
const MIN_INTERVAL: Duration = Duration::from_secs(60);

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct CacheEntry {
    pub version: u32,
    pub server: String,
    pub fingerprint: String,
    /// "ok" or "failed"
    pub result: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    pub checked_at: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub elapsed_ms: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub instructions: Option<String>,
    #[serde(default)]
    pub tools: Vec<McpToolSummary>,
}

impl CacheEntry {
    pub fn ok(&self) -> bool {
        self.result == "ok"
    }
}

/// The configuration as far as it decides the tools: how to reach the server and what it is
/// given. Credentials take part only by name. A local server's workspace takes part too,
/// since the same command can offer different tools in another project.
pub fn fingerprint(config: &McpServerConfig, workspace: &Path) -> String {
    let mut context = ring::digest::Context::new(&ring::digest::SHA256);
    let mut add = |label: &str, value: &str| {
        context.update(label.as_bytes());
        context.update(&[0]);
        context.update(value.as_bytes());
        context.update(&[0]);
    };
    add("name", &config.name);
    if let Some(command) = &config.command {
        add("command", command);
        for arg in &config.args {
            add("arg", arg);
        }
        for (key, value) in &config.env {
            add("env", &format!("{key}={value}"));
        }
        for var in &config.env_vars {
            add("env_var", var);
        }
        if let Some(cwd) = &config.cwd {
            add("cwd", &cwd.to_string_lossy());
        }
        add("workspace", &workspace.to_string_lossy());
    }
    if let Some(url) = &config.url {
        add("url", url);
        for (key, value) in &config.headers {
            add("header", &format!("{key}={value}"));
        }
        if let Some(var) = &config.bearer_token_env_var {
            add("bearer", var);
        }
    }
    context.finish().as_ref()[..8].iter().map(|b| format!("{b:02x}")).collect()
}

pub fn path(dir: &Path, name: &str, fingerprint: &str) -> PathBuf {
    dir.join(format!("{name}-{}.json", &fingerprint[..fingerprint.len().min(8)]))
}

/// The entry for this configuration, when one was written for it.
pub fn read(dir: &Path, config: &McpServerConfig, workspace: &Path) -> Option<CacheEntry> {
    let fingerprint = fingerprint(config, workspace);
    let text = std::fs::read_to_string(path(dir, &config.name, &fingerprint)).ok()?;
    let entry: CacheEntry = serde_json::from_str(&text).ok()?;
    (entry.fingerprint == fingerprint).then_some(entry)
}

/// What a connection found, to be written.
pub struct Outcome<'a> {
    pub ok: bool,
    pub error: Option<String>,
    pub elapsed_ms: Option<u64>,
    pub instructions: Option<String>,
    pub tools: Vec<(&'a str, Option<&'a str>, Option<&'a str>)>,
}

/// Writes the entry. `throttle`: skip it when the same entry was written by this process in
/// the last minute (sessions); `mcp/check` always writes.
pub fn write(dir: &Path, config: &McpServerConfig, workspace: &Path, outcome: Outcome<'_>, throttle: bool) -> io::Result<()> {
    let fingerprint = fingerprint(config, workspace);
    let target = path(dir, &config.name, &fingerprint);
    if throttle {
        static LAST: OnceLock<Mutex<HashMap<PathBuf, Instant>>> = OnceLock::new();
        let mut last = LAST.get_or_init(Default::default).lock().unwrap_or_else(|e| e.into_inner());
        if last.get(&target).is_some_and(|at| at.elapsed() < MIN_INTERVAL) {
            return Ok(());
        }
        last.insert(target.clone(), Instant::now());
    }
    let entry = CacheEntry {
        version: 1,
        server: config.name.clone(),
        fingerprint,
        result: if outcome.ok { "ok" } else { "failed" }.to_string(),
        error: outcome.error,
        checked_at: chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Secs, true),
        elapsed_ms: outcome.elapsed_ms,
        instructions: outcome.instructions.map(|i| super::cap(&i, 4096)),
        tools: outcome
            .tools
            .into_iter()
            .map(|(name, title, description)| McpToolSummary {
                name: name.to_string(),
                title: title.map(str::to_string),
                description: description.map(|d| super::cap(d.lines().next().unwrap_or(d), MAX_DESCRIPTION)),
            })
            .collect(),
    };
    let mut bytes = serde_json::to_vec_pretty(&entry).map_err(io::Error::other)?;
    if bytes.len() > MAX_FILE_BYTES {
        let mut smaller = entry;
        smaller.tools.iter_mut().for_each(|t| t.description = None);
        bytes = serde_json::to_vec(&smaller).map_err(io::Error::other)?;
    }
    std::fs::create_dir_all(dir)?;
    crate::config_edit::replace_file(&target, &bytes)?;
    prune(dir);
    Ok(())
}

/// Keeps at most [`MAX_FILES`] entries, dropping the oldest.
fn prune(dir: &Path) {
    let Ok(entries) = std::fs::read_dir(dir) else { return };
    let mut files: Vec<(std::time::SystemTime, PathBuf)> = entries
        .flatten()
        .filter(|e| e.path().extension().is_some_and(|x| x == "json"))
        .filter_map(|e| Some((e.metadata().ok()?.modified().ok()?, e.path())))
        .collect();
    if files.len() <= MAX_FILES {
        return;
    }
    files.sort();
    for (_, path) in files.iter().take(files.len() - MAX_FILES) {
        let _ = std::fs::remove_file(path);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn server(command: &str) -> McpServerConfig {
        McpServerConfig { name: "pg".into(), command: Some(command.into()), ..Default::default() }
    }

    #[test]
    fn entries_match_only_the_same_configuration() {
        let dir = tempfile::tempdir().unwrap();
        let config = server("pg-mcp");
        let outcome = Outcome {
            ok: true,
            error: None,
            elapsed_ms: Some(12),
            instructions: None,
            tools: vec![("query", Some("Query"), Some("Runs a read-only query.\nMore."))],
        };
        write(dir.path(), &config, Path::new("/work/a"), outcome, false).unwrap();
        let entry = read(dir.path(), &config, Path::new("/work/a")).unwrap();
        assert!(entry.ok());
        assert_eq!(entry.tools[0].description.as_deref(), Some("Runs a read-only query."));
        assert!(read(dir.path(), &config, Path::new("/work/b")).is_none(), "another workspace");
        assert!(read(dir.path(), &server("pg-mcp2"), Path::new("/work/a")).is_none(), "another command");
        // Turning it off and on, or a new description, keeps the entry.
        let mut same = config.clone();
        same.enabled = Some(false);
        same.description = Some("x".into());
        assert!(read(dir.path(), &same, Path::new("/work/a")).is_some());
    }
}
