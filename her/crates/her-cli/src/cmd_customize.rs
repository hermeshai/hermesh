//! `her skills add/remove/enable/disable` and `her mcp add/remove/enable/disable`: what the
//! desktop's 自定义 page does, from a terminal (docs/desktop/customize-design.md §7.4). Writes
//! go to the user's own files; `--project` writes the repository's, as the user asked.

use std::io::Read as _;
use std::path::Path;
use std::path::PathBuf;

use anyhow::Context as _;
use anyhow::bail;
use her_core::HerHome;
use her_core::Workspace;
use her_core::config::LayerFiles;
use her_core::config_edit;
use her_core::skills;
use her_core::skills::install;
use her_core::trash::Trash;
use her_protocol::McpServerState;
use her_protocol::api::SkillScope;
use her_protocol::customize::ConfigEdit;
use her_protocol::customize::ConfigLayer;
use her_protocol::customize::ConfigMerge;
use her_protocol::customize::ConfigWriteResult;
use serde_json::Value;
use serde_json::json;

use crate::setup;

/// `~/x` for paths in the user's home.
pub(crate) fn shorten(home: &HerHome, path: &Path) -> String {
    match home.user_home().and_then(|user| path.strip_prefix(user).ok()) {
        Some(rest) => format!("~/{}", rest.display()),
        None => path.display().to_string(),
    }
}

fn here(home: &HerHome) -> anyhow::Result<Workspace> {
    Ok(Workspace::open(home, &std::env::current_dir()?, &[])?)
}

// ---- Skills ----

pub fn skills_add(from: &Path, project: bool, replace: bool) -> anyhow::Result<u8> {
    let home = setup::home()?;
    let from = std::fs::canonicalize(from).with_context(|| format!("cannot read {}", from.display()))?;
    let (root, scope) = if project {
        let Some(project_root) = here(&home)?.project_root else {
            bail!("--project needs a project: run it inside a repository");
        };
        (project_root.join(".agents").join("skills"), SkillScope::Project)
    } else {
        (home.skills_dir(), SkillScope::User)
    };
    let trash = Trash::system()?;
    let request = install::InstallRequest { from: &from, root: &root, scope, source: None, dry_run: false, replace, trash: &trash };
    let installed = match install::install(&request) {
        Err(install::InstallError::Conflict { name, path }) => {
            bail!("a skill named {name} is already at {}; add --replace to put this one in its place", shorten(&home, &path))
        }
        result => result?,
    };
    println!("added {} to {}", installed.skill.name, shorten(&home, &installed.target));
    Ok(0)
}

/// The skills of a name here, the one sessions use first.
fn skills_named(home: &HerHome, name: &str) -> anyhow::Result<Vec<PathBuf>> {
    let ws = here(home)?;
    let sources = skills::Sources::new(home, &ws.cwd, ws.project_root.as_deref(), &ws.config.skills);
    Ok(skills::discover(&sources).entries.into_iter().filter(|e| e.name == name).map(|e| e.dir().to_path_buf()).collect())
}

/// A path names one skill; anything else is a name.
fn as_path(name_or_path: &str) -> Option<PathBuf> {
    (skills::is_path(name_or_path) || Path::new(name_or_path).exists()).then(|| PathBuf::from(name_or_path))
}

pub fn skills_remove(name_or_path: &str) -> anyhow::Result<u8> {
    let home = setup::home()?;
    let dir = match as_path(name_or_path) {
        Some(path) => path,
        None => {
            // The user's own copy of that name: the only kind her removes.
            let own = home.skills_dir();
            skills_named(&home, name_or_path)?
                .into_iter()
                .find(|dir| dir.starts_with(&own))
                .with_context(|| format!("you have no skill named {name_or_path} in {}", shorten(&home, &own)))?
        }
    };
    let trash = Trash::system()?;
    let gone = install::remove(&dir, &home.skills_dir(), &trash)?;
    println!("moved {} to {}", shorten(&home, &dir), shorten(&home, &gone));
    Ok(0)
}

/// By path: that copy only. By name: every skill of the name (skill-design §4.4).
pub fn skills_switch(name_or_path: &str, enabled: bool) -> anyhow::Result<u8> {
    let home = setup::home()?;
    let verb = if enabled { "enabled" } else { "disabled" };
    let (what, result) = match as_path(name_or_path) {
        Some(path) => {
            let dir = std::fs::canonicalize(&path).with_context(|| format!("cannot read {name_or_path}"))?;
            (shorten(&home, &dir), install::set_enabled(&home, &dir, enabled)?)
        }
        None => {
            let dirs = skills_named(&home, name_or_path)?;
            (format!("every skill named {name_or_path}"), install::set_name_enabled(&home, name_or_path, &dirs, enabled)?)
        }
    };
    println!("{verb} {what} in {}", shorten(&home, &result.file));
    Ok(0)
}

// ---- MCP servers ----

fn edit(key_path: String, value: Value) -> ConfigEdit {
    ConfigEdit { key_path, value, merge: ConfigMerge::Replace }
}

/// Writes `edits` to one layer, checked against the workspace's other layers; then removes
/// the saved credentials nothing refers to any more.
pub(crate) fn write(home: &HerHome, ws: &Workspace, layer: ConfigLayer, edits: &[ConfigEdit]) -> anyhow::Result<ConfigWriteResult> {
    let dir = ws.project_root.clone().unwrap_or_else(|| ws.cwd.clone());
    let project = dir.join(".her").join("config.toml");
    let user = home.config_file();
    if layer == ConfigLayer::UserProject && ws.user_project_file.is_none() {
        bail!("--local needs a project: run it inside a repository");
    }
    if layer == ConfigLayer::Project && ws.project_root.is_none() {
        bail!("--project needs a project: run it inside a repository");
    }
    let target = config_edit::Target {
        layer,
        files: LayerFiles {
            user: &user,
            project: Some(&project),
            user_project: ws.user_project_file.as_deref(),
            project_root: Some(&dir),
        },
        trusted: ws.trusted,
        overrides: &[],
        header: (layer == ConfigLayer::UserProject).then(|| config_edit::user_project_header(&dir)),
        allow_repo: layer == ConfigLayer::Project,
    };
    let written = config_edit::write(&target, edits, None)?;
    for name in config_edit::unused_credentials(home, &written.dropped_credentials) {
        match her_llm::credentials::delete(&name) {
            Ok(()) => println!("deleted the saved credential {name}"),
            Err(err) => eprintln!("warning: cannot delete the saved credential {name}: {err}"),
        }
    }
    Ok(written.result)
}

/// What a layer above the written one still sets.
fn note_overrides(home: &HerHome, result: &ConfigWriteResult) {
    for over in &result.overridden_by {
        let file = over.file.as_deref().map(|f| shorten(home, f)).unwrap_or_else(|| over.layer.as_str().to_string());
        eprintln!("note: {file} also sets {}, and wins here", over.key_path);
    }
}

pub struct AddArgs {
    pub name: String,
    pub url: Option<String>,
    pub headers: Vec<String>,
    pub env: Vec<String>,
    pub secret_env: Vec<String>,
    pub token: bool,
    pub local: bool,
    pub project: bool,
    pub command: Vec<String>,
    /// Write it even when the check fails.
    pub force: bool,
    /// Do not sign in now, even when the server signs in with OAuth.
    pub no_login: bool,
}

fn pairs(items: &[String], flag: &str) -> anyhow::Result<Vec<(String, String)>> {
    items
        .iter()
        .map(|item| {
            let (key, value) = item.split_once('=').with_context(|| format!("{flag} {item}: use KEY=VALUE"))?;
            Ok((key.trim().to_string(), value.to_string()))
        })
        .collect()
}

/// Saves a secret in the keychain; the configuration gets the reference.
fn save_credential(name: &str, value: &str) -> anyhow::Result<String> {
    her_llm::credentials::set(name, value).map_err(|err| anyhow::anyhow!("cannot save {name}: {err}"))?;
    println!("saved {name} in the keychain");
    Ok(format!("${{credential:{name}}}"))
}

/// Prints a probe's steps as they happen.
fn print_step(step: &her_protocol::customize::McpProbeStep) {
    use her_protocol::customize::McpProbeStepState::*;
    let what = match step.id {
        her_protocol::customize::McpProbeStepId::Connect => "connecting",
        her_protocol::customize::McpProbeStepId::Resource => "looking up the sign-in settings",
        her_protocol::customize::McpProbeStepId::AuthServer => "checking the authorization server",
        her_protocol::customize::McpProbeStepId::Start => "starting the command",
        her_protocol::customize::McpProbeStepId::Handshake => "handshake",
        her_protocol::customize::McpProbeStepId::Tools => "listing the tools",
    };
    let detail = step.detail.as_deref().map(|d| format!(": {d}")).unwrap_or_default();
    let status = step.http_status.map(|s| format!(" (HTTP {s})")).unwrap_or_default();
    match step.state {
        Running => {}
        Ok => println!("  {what} … ok{status}{detail}"),
        Failed => println!("  {what} … failed{status}{detail}"),
        Skipped if step.detail.is_some() => println!("  {what} … skipped{detail}"),
        Skipped => {}
    }
}

/// Opens `url` in the system browser, unless `HER_NO_BROWSER` is set.
pub(crate) fn open_browser(url: &str) {
    if !her_core::open_in_browser(url) {
        eprintln!("note: could not open a browser; open the address above yourself");
    }
}

/// Signs `name` in at `config`'s address, printing the address for the browser and waiting.
async fn sign_in(home: &HerHome, ws: &Workspace, name: &str, config: &her_protocol::McpServerConfig) -> anyhow::Result<()> {
    let url = config.url.clone().with_context(|| format!("{name} is not a remote server"))?;
    let own = match &config.oauth {
        Some(own) => {
            let secret = match &own.client_secret {
                Some(value) => {
                    let names = her_mcp::env::credential_names([value.as_str()]);
                    let saved: std::collections::HashMap<String, String> =
                        names.into_iter().filter_map(|n| her_llm::credentials::get(&n).map(|v| (n, v))).collect();
                    let lookup = |n: &str| match n.strip_prefix("credential:") {
                        Some(c) => saved.get(c).cloned(),
                        None => her_mcp::env::process_lookup(n),
                    };
                    Some(her_mcp::env::expand(value, &lookup).map_err(|e| anyhow::anyhow!(e))?)
                }
                None => None,
            };
            Some(her_protocol::McpOAuthClient { client_secret: secret, ..own.clone() })
        }
        None => None,
    };
    let http = her_core::runtime::model_registry(home, &ws.config)?.http_client()?;
    let pending = her_mcp::oauth::start_login(her_mcp::oauth::LoginRequest {
        server: name.to_string(),
        url,
        own,
        metadata_url: ws.config.mcp.client_metadata_url.clone(),
        paths: Some(her_mcp::oauth::OAuthPaths { her_home: home.root().to_path_buf() }),
        http,
        prefer: None,
    })
    .await
    .map_err(|e| anyhow::anyhow!("cannot sign in to {name}: {e}"))?;
    println!("sign in to {name} in your browser:");
    println!("{}", pending.authorize_url);
    open_browser(&pending.authorize_url);
    println!("waiting for the browser (up to 5 minutes)…");
    pending.wait(std::time::Duration::from_secs(300)).await.map_err(|e| anyhow::anyhow!("signing in to {name} failed: {e}"))?;
    println!("signed in to {name} (the token is in the {})", her_llm::credentials::store_name().unwrap_or("credential store"));
    Ok(())
}

pub async fn mcp_add(args: AddArgs) -> anyhow::Result<u8> {
    let AddArgs { name, url, headers, env, secret_env, token, local: personal, project, command, force, no_login } = args;
    if !her_protocol::mcp::valid_server_name(&name) {
        bail!("a server name is 1-32 letters, digits, '_' or '-'");
    }
    let home = setup::home()?;
    let ws = here(&home)?;
    if ws.mcp_servers.iter().any(|s| s.name == name) {
        bail!("there is a server named {name} here already; remove it first, or choose another name");
    }
    let mut server = serde_json::Map::new();
    match (&url, command.split_first()) {
        (Some(_), Some(_)) => bail!("give --url or a command after --, not both"),
        (None, None) => bail!("give --url URL, or the command after --"),
        (Some(url), None) => {
            server.insert("url".into(), json!(url));
        }
        (None, Some((program, args))) => {
            server.insert("command".into(), json!(program));
            if !args.is_empty() {
                server.insert("args".into(), json!(args));
            }
        }
    }
    let env = pairs(&env, "--env")?;
    if let Some(missing) = secret_env.iter().find(|key| !env.iter().any(|(k, _)| k == *key)) {
        bail!("--secret-env {missing} needs --env {missing}=VALUE");
    }
    if token && url.is_none() {
        bail!("--token is for a server at a URL; give a command's secret with --env KEY=VALUE --secret-env KEY");
    }
    let mut env_table = serde_json::Map::new();
    for (key, value) in env {
        let value = if secret_env.contains(&key) { save_credential(&format!("mcp.{name}.{key}"), &value)? } else { value };
        env_table.insert(key, json!(value));
    }
    let mut header_table = serde_json::Map::new();
    for (key, value) in pairs(&headers, "--header")? {
        header_table.insert(key, json!(value));
    }
    if token {
        let mut value = String::new();
        std::io::stdin().read_to_string(&mut value)?;
        let value = value.trim();
        if value.is_empty() {
            bail!("--token reads the token from standard input, which was empty");
        }
        let reference = save_credential(&format!("mcp.{name}"), value)?;
        header_table.insert("Authorization".into(), json!(format!("Bearer {reference}")));
    }
    if !env_table.is_empty() {
        server.insert("env".into(), Value::Object(env_table));
    }
    if !header_table.is_empty() {
        server.insert("headers".into(), Value::Object(header_table));
    }
    let saved: Vec<String> = her_mcp::env::credential_names(
        server.values().flat_map(|v| match v {
            Value::String(s) => vec![s.clone()],
            Value::Object(map) => map.values().filter_map(|v| v.as_str().map(String::from)).collect(),
            _ => Vec::new(),
        }).collect::<Vec<_>>().iter().map(String::as_str),
    );
    let forget_saved = || {
        for credential in &saved {
            let _ = her_llm::credentials::delete(credential);
        }
    };

    // Check before writing (mcp-design §13.6): nothing is written when it fails, unless --force.
    let mut config: her_protocol::McpServerConfig =
        serde_json::from_value(Value::Object(server.clone())).context("the server's settings")?;
    config.name = name.clone();
    // One service, one connector (mcp-design §13.10): a second copy only doubles its tools.
    let identity = her_mcp::identity::identity(&config);
    if let Some(same) = ws.mcp_servers.iter().find(|s| s.name != name && her_mcp::identity::identity(s) == identity) {
        if !force {
            forget_saved();
            eprintln!("not added: {} already reaches this server", same.name);
            eprintln!("nothing was written; add --force to add it again anyway");
            return Ok(1);
        }
        eprintln!("note: {} already reaches this server; adding it again", same.name);
    }
    println!("checking {name}…");
    let http = match config.url {
        Some(_) => her_core::runtime::model_registry(&home, &ws.config)?.http_client().ok(),
        None => None,
    };
    let dir = her_core::mcp::views::workspace_dir(&ws);
    let probe = her_mcp::probe::probe(
        config.clone(),
        her_mcp::probe::ProbeContext {
            server: her_mcp::ServerContext {
                http,
                roots: vec![dir.clone()],
                cwd: ws.cwd.clone(),
                project_root: ws.project_root.clone(),
                on_status: None,
                oauth: Some(her_mcp::oauth::OAuthPaths { her_home: home.root().to_path_buf() }),
            },
            metadata_url: ws.config.mcp.client_metadata_url.clone(),
            progress: std::sync::Arc::new(print_step),
            cancel: Default::default(),
            icons: Some(home.mcp_icons_dir()),
        },
    )
    .await;
    if let Some(error) = &probe.error {
        if !force {
            forget_saved();
            eprintln!("not added: {error}");
            eprintln!("nothing was written; add --force to add it anyway");
            return Ok(1);
        }
        eprintln!("note: the check failed ({error}); adding it anyway");
    }
    if probe.transport == Some(her_protocol::McpRemoteTransport::Sse) && config.remote_transport() != her_protocol::McpRemoteTransport::Sse {
        server.insert("transport".into(), json!("sse"));
        config.transport = Some(her_protocol::McpRemoteTransport::Sse);
    }
    let oauth = probe.error.is_none() && probe.suggested.auth == her_protocol::customize::McpSuggestedAuth::Oauth && header_table_is_empty(&server);
    if oauth {
        server.insert("auth".into(), json!("oauth"));
        config.auth = Some(her_protocol::McpAuthMode::Oauth);
        if no_login {
            println!("{name} signs in with OAuth; sign in later with `her mcp login {name}`");
        } else if let Err(err) = sign_in(&home, &ws, &name, &config).await {
            if !force {
                forget_saved();
                eprintln!("not added: {err}");
                return Ok(1);
            }
            eprintln!("note: {err}; adding it anyway");
        }
    } else if probe.suggested.auth == her_protocol::customize::McpSuggestedAuth::Token && header_table_is_empty(&server) {
        eprintln!("note: {name} wants a token or API key; add it with --token, or --header Authorization=…");
    }
    let layer = match (personal, project) {
        (_, true) => ConfigLayer::Project,
        (true, false) => ConfigLayer::UserProject,
        (false, false) => ConfigLayer::User,
    };
    let written = write(&home, &ws, layer, &[edit(format!("mcp_servers.{name}"), Value::Object(server))])?;
    println!("added {name} to {}", shorten(&home, &written.file));
    note_overrides(&home, &written);
    if layer == ConfigLayer::Project && !ws.trusted {
        eprintln!("note: this project is not trusted, so sessions skip its servers; `her trust` allows them");
    }

    // Connect once, as the page does after adding; the result goes to the cache it reads.
    let ws = here(&home)?;
    let Some(config) = her_core::mcp::views::for_check(&ws, &name) else { return Ok(0) };
    let http = match config.url {
        Some(_) => her_core::runtime::model_registry(&home, &ws.config)?.http_client().ok(),
        None => None,
    };
    let dir = her_core::mcp::views::workspace_dir(&ws);
    let (identity, url) = (her_mcp::identity::identity(&config), config.url.clone());
    let checks =
        her_core::mcp::check_servers(vec![config], http, vec![dir], ws.cwd.clone(), ws.project_root.clone(), Some(home.mcp_cache_dir()), Some(home.root().to_path_buf()))
            .await;
    match checks.first() {
        Some(check) if check.state == McpServerState::Ready => {
            println!("connected: {} tools ({} ms)", check.summaries.len(), check.elapsed_ms.unwrap_or(0));
            if let Some(identity) = &identity {
                her_mcp::icon::refresh(&home.mcp_icons_dir(), identity, url.as_deref(), check.info.as_ref()).await;
            }
        }
        Some(check) => println!("not connected: {}", check.error.as_deref().unwrap_or("no reason given")),
        None => {}
    }
    Ok(0)
}

fn header_table_is_empty(server: &serde_json::Map<String, Value>) -> bool {
    server.get("headers").and_then(Value::as_object).is_none_or(|h| h.is_empty())
}

/// `her mcp login <name>`: signs in to a configured server; one that did not sign in with
/// OAuth yet is set to (`auth = "oauth"`) in the file that defines it, when her may write it.
pub async fn mcp_login(name: &str) -> anyhow::Result<u8> {
    let home = setup::home()?;
    let ws = here(&home)?;
    let config = her_core::mcp::views::for_check(&ws, name).with_context(|| format!("no MCP server named {name} here"))?;
    if config.connector.is_some() {
        let m = her_core::connectors::for_server(home.root(), &config).context("managed connector configuration changed; reinstall it from Connectors")?;
        let mut pending = her_core::connectors::login(home.root().to_path_buf(), m, Default::default()).await.map_err(anyhow::Error::msg)?;
        println!("{}", pending.url);
        open_browser(&pending.url);
        let mut updates = pending.take_updates();
        let wait = pending.wait();
        tokio::pin!(wait);
        let mut updating = true;
        loop {
            tokio::select! {
                result = &mut wait => { result.map_err(anyhow::Error::msg)?; break; },
                _ = tokio::signal::ctrl_c() => bail!("login cancelled"),
                url = updates.recv(), if updating => match url {
                    Some(url) => { println!("{url}"); open_browser(&url); },
                    None => updating = false,
                },
            }
        }
        mcp_switch(name, true, false)?;
        println!("signed in to {name}");
        return Ok(0);
    }
    sign_in(&home, &ws, name, &config).await?;
    if !config.uses_oauth() {
        match defined_in(&home, &ws, name) {
            Some(layer) if layer.writable() => {
                let written = write(&home, &ws, layer, &[edit(format!("mcp_servers.{name}.auth"), json!("oauth"))])?;
                println!("set {name} to sign in with OAuth in {}", shorten(&home, &written.file));
            }
            _ => eprintln!("note: set auth = \"oauth\" for {name} where it is defined, so sessions use the sign-in"),
        }
    }
    Ok(0)
}

/// `her mcp logout <name>`: revokes and deletes the server's tokens.
pub async fn mcp_logout(name: &str) -> anyhow::Result<u8> {
    let home = setup::home()?;
    let ws = here(&home)?;
    if let Some(config) = her_core::mcp::views::for_check(&ws, name).filter(|s| s.connector.is_some()) {
        let m = her_core::connectors::for_server(home.root(), &config).context("invalid managed connector configuration")?;
        mcp_switch(name, false, false)?;
        her_core::connectors::logout(home.root(), &m).await.map_err(anyhow::Error::msg)?;
        println!("signed out of {name}");
        return Ok(0);
    }
    let http = her_core::runtime::model_registry(&home, &ws.config)?.http_client()?;
    her_mcp::oauth::logout(name, &http).await.map_err(|e| anyhow::anyhow!(e))?;
    println!("signed out of {name}");
    Ok(0)
}

/// The layer that says how to reach the server here, an untrusted project's included.
fn defined_in(home: &HerHome, ws: &Workspace, name: &str) -> Option<ConfigLayer> {
    let listed = her_core::mcp::views::list(ws, home, &Default::default());
    listed.servers.into_iter().find(|s| s.name == name).map(|s| s.layer)
}

pub async fn mcp_remove(name: &str, project: bool) -> anyhow::Result<u8> {
    let home = setup::home()?;
    let ws = here(&home)?;
    let layer = match defined_in(&home, &ws, name) {
        Some(ConfigLayer::Project) if project => ConfigLayer::Project,
        Some(ConfigLayer::Project) => bail!(
            "{name} is in the project's .her/config.toml; add --project to remove it there, \
             or turn it off for you with `her mcp disable {name} --local`"
        ),
        Some(ConfigLayer::ProjectJson) => {
            bail!("{name} is in the project's .mcp.json; edit that file, or turn it off for you with `her mcp disable {name} --local`")
        }
        Some(layer) if layer.writable() => layer,
        _ => bail!("no server named {name} in a configuration file here"),
    };
    if let Some(config) = her_core::mcp::views::for_check(&ws, name).filter(|s| s.connector.is_some()) {
        let m = her_core::connectors::for_server(home.root(), &config).context("invalid managed connector configuration")?;
        her_core::connectors::logout(home.root(), &m).await.map_err(anyhow::Error::msg)?;
    }
    let key = her_core::config::join_key("mcp_servers", name);
    let written = write(&home, &ws, layer, &[edit(key.clone(), Value::Null)])?;
    println!("removed {name} from {}", shorten(&home, &written.file));
    // What the personal project file still says about it goes too.
    let ws = here(&home)?;
    let prefix = format!("{key}.");
    let leftover = ws.origins.iter().any(|(k, l)| *l == ConfigLayer::UserProject && k.starts_with(&prefix));
    if layer != ConfigLayer::UserProject && leftover {
        write(&home, &ws, ConfigLayer::UserProject, &[edit(key, Value::Null)])?;
    }
    Ok(0)
}

/// Enabled is the default: enabling takes the setting out, and writes `enabled = true` only
/// when another file still turns the server off.
pub fn mcp_switch(name: &str, enabled: bool, personal: bool) -> anyhow::Result<u8> {
    let home = setup::home()?;
    let ws = here(&home)?;
    let Some(defined) = defined_in(&home, &ws, name) else { bail!("no server named {name} here") };
    // A server of the repository is switched for the user alone.
    let layer = if personal || !defined.writable() { ConfigLayer::UserProject } else { defined };
    let key = her_core::config::join_key(&her_core::config::join_key("mcp_servers", name), "enabled");
    let is_enabled = |ws: &Workspace| ws.mcp_servers.iter().any(|s| s.name == name && s.is_enabled());
    let mut file = write(&home, &ws, layer, &[edit(key.clone(), if enabled { Value::Null } else { json!(false) })])?.file;
    let mut ws = here(&home)?;
    if is_enabled(&ws) != enabled
        && !personal
        && let Some(origin) = ws.origins.get(&key).copied()
        && origin.writable()
        && origin != layer
    {
        // The personal project file sets it too: take it out there.
        file = write(&home, &ws, origin, &[edit(key.clone(), Value::Null)])?.file;
        ws = here(&home)?;
    }
    if enabled && !is_enabled(&ws) {
        file = write(&home, &ws, layer, &[edit(key.clone(), json!(true))])?.file;
        ws = here(&home)?;
    }
    println!("{} {name} in {}", if enabled { "enabled" } else { "disabled" }, shorten(&home, &file));
    if is_enabled(&ws) != enabled {
        let by = ws.origins.get(&key).map(|l| l.as_str()).unwrap_or("another");
        eprintln!("note: the {by} configuration still turns it {}", if enabled { "off" } else { "on" });
    }
    Ok(0)
}
