//! The 自定义 page's methods (docs/desktop/customize-design.md §6): configuration and
//! credentials, skills, MCP servers, and the notifications that follow changes. The work is
//! in her-core, shared with the command line; this is the protocol side.

use std::collections::HashMap;
use std::collections::HashSet;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;

use her_core::Workspace;
use her_core::config;
use her_core::config::LayerFiles;
use her_core::config_edit;
use her_core::config_edit::WriteError;
use her_core::mcp::views;
use her_core::skills::install;
use her_core::skills::install::InstallError;
use her_core::trash::Trash;
use her_protocol::McpServerState;
use her_protocol::api::Empty;
use her_protocol::api::SkillScope;
use her_protocol::api::error_code;
use her_protocol::api::method;
use her_protocol::customize::*;
use her_rpc::RpcError;
use parking_lot::Mutex;
use serde_json::json;

use crate::conn::ConnHandle;
use crate::conn::ConnId;
use crate::errors;
use crate::server::Server;
use crate::watch;

/// Who hears about which change, and what is being watched for them.
#[derive(Default)]
pub(crate) struct State {
    config: Mutex<HashMap<ConnId, ConnHandle>>,
    skills: Mutex<HashMap<ConnId, ConnHandle>>,
    mcp: Mutex<HashMap<ConnId, ConnHandle>>,
    /// Project directories each connection asked about.
    workspaces: Mutex<HashMap<ConnId, HashSet<PathBuf>>>,
    watcher: Mutex<Option<watch::Watcher>>,
    /// Files this server wrote and their version, so the watcher does not announce them again.
    written: Mutex<HashMap<PathBuf, String>>,
    /// One check of a server at a time.
    checks: Mutex<HashMap<String, Arc<tokio::sync::Mutex<()>>>>,
    /// Running probes, by the caller's `probe_id`.
    probes: Mutex<HashMap<String, tokio_util::sync::CancellationToken>>,
    /// Active authorization cancellation tokens, by server name.
    logins: Mutex<HashMap<String, tokio_util::sync::CancellationToken>>,
    /// The latest browser step survives a renderer reload, including initial URL creation.
    login_results: Mutex<HashMap<String, LoginAttempt>>,
    /// Sign-ins started with a `redirect_uri`, by login id, for `mcp/login_complete`.
    completions: Mutex<HashMap<String, her_mcp::oauth::Completion>>,
}

type LoginResponse = tokio::sync::watch::Receiver<Option<Result<McpLoginResult, String>>>;

#[derive(Clone)]
struct LoginAttempt {
    config: her_protocol::McpServerConfig,
    client: Option<McpOAuthClientKind>,
    redirect_uri: Option<String>,
    response: LoginResponse,
}

/// A `redirect_uri` on the client's device, as RFC 8252 allows: `http://127.0.0.1:<port>/…`,
/// `http://[::1]:<port>/…` or `http://localhost:<port>/…`. It is returned as parsed, so the
/// address the provider is given is the one that was checked.
pub(crate) fn loopback_redirect(uri: &str) -> Result<String, RpcError> {
    let refused = || RpcError::invalid_params(format!("redirect_uri must be http://127.0.0.1:<port>/…, http://[::1]:<port>/… or http://localhost:<port>/…, not {uri}"));
    let url = url::Url::parse(uri).map_err(|_| refused())?;
    let loopback = match url.host() {
        Some(url::Host::Ipv4(ip)) => ip == std::net::Ipv4Addr::LOCALHOST,
        Some(url::Host::Ipv6(ip)) => ip == std::net::Ipv6Addr::LOCALHOST,
        Some(url::Host::Domain(name)) => name == "localhost",
        None => false,
    };
    let plain = url.username().is_empty() && url.password().is_none() && url.fragment().is_none();
    if url.scheme() != "http" || !loopback || url.port().is_none_or(|port| port == 0) || !plain {
        return Err(refused());
    }
    Ok(url.into())
}

async fn login_result(mut pending: LoginResponse) -> Result<McpLoginResult, RpcError> {
    loop {
        if let Some(result) = pending.borrow_and_update().clone() {
            return result.map_err(|e| RpcError::with_kind(error_code::MCP_LOGIN_FAILED, "mcp_login_failed", e));
        }
        pending.changed().await.map_err(|_| RpcError::internal("登录流程已结束，请重试"))?;
    }
}

/// How long a sign-in waits for the browser to come back.
const LOGIN_WAIT: std::time::Duration = std::time::Duration::from_secs(300);

/// The user's own OAuth client of `server`, its secret expanded from the credential store.
fn own_client(server: &her_protocol::McpServerConfig) -> Result<Option<her_protocol::McpOAuthClient>, String> {
    let Some(own) = server.oauth.clone() else { return Ok(None) };
    let secret = match &own.client_secret {
        Some(value) => {
            let names = her_mcp::env::credential_names([value.as_str()]);
            let saved: HashMap<String, String> = names.into_iter().filter_map(|n| her_llm::credentials::get(&n).map(|v| (n, v))).collect();
            let lookup = |name: &str| match name.strip_prefix("credential:") {
                Some(c) => saved.get(c).cloned(),
                None => her_mcp::env::process_lookup(name),
            };
            Some(her_mcp::env::expand(value, &lookup)?)
        }
        None => None,
    };
    Ok(Some(her_protocol::McpOAuthClient { client_secret: secret, ..own }))
}

#[derive(Clone, Copy)]
pub(crate) enum Topic {
    Config,
    Skills,
    Mcp,
}

fn blocking_error(e: tokio::task::JoinError) -> RpcError {
    RpcError::internal(e.to_string())
}

fn write_error(err: WriteError) -> RpcError {
    match err {
        WriteError::VersionConflict { version } => RpcError {
            code: error_code::CONFIG_VERSION_CONFLICT,
            message: "the configuration changed since it was read; read it again".into(),
            data: Some(json!({"kind": "config_version_conflict", "version": version})),
        },
        WriteError::Invalid(errors) => RpcError {
            code: error_code::CONFIG_VALIDATION_ERROR,
            message: errors.iter().map(|e| e.message.clone()).collect::<Vec<_>>().join("; "),
            data: Some(json!({"kind": "config_validation_error", "errors": errors})),
        },
        WriteError::Busy => RpcError::with_kind(error_code::CONFIG_BUSY, "config_busy", "the configuration file is locked by another writer"),
        WriteError::Io { .. } => RpcError::internal(err.to_string()),
    }
}

fn install_error(err: InstallError) -> RpcError {
    match err {
        InstallError::Invalid(message) => RpcError::with_kind(error_code::SKILL_INVALID, "skill_invalid", message),
        InstallError::Conflict { ref path, .. } => RpcError {
            code: error_code::SKILL_CONFLICT,
            message: err.to_string(),
            data: Some(json!({"kind": "skill_conflict", "path": path})),
        },
        InstallError::NotRemovable(message) => RpcError::with_kind(error_code::NOT_REMOVABLE, "not_removable", message),
        InstallError::Io { .. } => RpcError::internal(err.to_string()),
    }
}

pub(crate) fn store_error(message: String) -> RpcError {
    RpcError::with_kind(error_code::CREDENTIAL_STORE_UNAVAILABLE, "credential_store_unavailable", message)
}

fn trash() -> Result<Trash, RpcError> {
    Trash::system().map_err(|e| RpcError::internal(format!("no trash: {e}")))
}

impl Server {
    pub async fn connector_install(&self, p: ConnectorInstallParams) -> Result<ConnectorInstallResult, RpcError> {
        her_core::connectors::manifest(&p.id).map_err(RpcError::invalid_params)?;
        let key = format!("connector-install:{}", p.id);
        let lock = self.customize().checks.lock().entry(key).or_default().clone();
        let _lock = lock.lock().await;
        let manifest = her_core::connectors::install(self.0.home.root(), &p.id).await.map_err(RpcError::internal)?;
        let executable = std::env::current_exe().map_err(|e| RpcError::internal(e.to_string()))?;
        Ok(ConnectorInstallResult { server: her_core::connectors::server_config(self.0.home.root(), &manifest, &executable), version: manifest.version })
    }

    async fn set_managed_enabled(&self, name: &str, enabled: bool) -> Result<(), RpcError> {
        let version = config_edit::file_version(&self.0.home.config_file());
        let ws = self.workspace(None)?;
        let configured = ws.mcp_servers.iter().find(|s| s.name == name).and_then(|s| her_core::connectors::for_server(self.0.home.root(), s));
        if configured.is_none() { return Err(RpcError::invalid_params("连接器配置已改变，请重新连接")); }
        self.config_write(ConfigWriteParams {
            layer: ConfigLayer::User, cwd: None, expected_version: Some(version),
            edits: vec![ConfigEdit { key_path: format!("mcp_servers.{name}.enabled"), value: json!(enabled), merge: ConfigMerge::Replace }],
        }).await?;
        Ok(())
    }

    pub async fn mcp_login_cancel(&self, p: McpLogoutParams) -> Result<Empty, RpcError> {
        if let Some(cancel) = self.customize().logins.lock().get(&p.name) { cancel.cancel(); }
        self.wait_for_login_end(&p.name).await?;
        Ok(Empty {})
    }

    async fn wait_for_login_end(&self, name: &str) -> Result<(), RpcError> {
        tokio::time::timeout(std::time::Duration::from_secs(35), async {
            while self.customize().logins.lock().contains_key(name) {
                tokio::time::sleep(std::time::Duration::from_millis(25)).await;
            }
        }).await.map_err(|_| RpcError::internal("登录仍在结束，请稍后重试"))
    }

    fn customize(&self) -> &State {
        &self.0.customize
    }

    /// The workspace of `cwd`; without one, that of a session without a project.
    pub(crate) fn workspace(&self, cwd: Option<&Path>) -> Result<Workspace, RpcError> {
        let dir = match cwd {
            Some(cwd) if !cwd.is_absolute() => return Err(RpcError::invalid_params("cwd must be an absolute path")),
            Some(cwd) => cwd.to_path_buf(),
            None => {
                let chats = self.0.home.chats_dir();
                std::fs::create_dir_all(&chats).map_err(|e| RpcError::internal(e.to_string()))?;
                chats
            }
        };
        Workspace::open(&self.0.home, &dir, &self.0.overrides).map_err(errors::from_runtime)
    }

    fn layer_files<'a>(&'a self, ws: &'a Workspace, project: &'a Path, user: &'a Path) -> LayerFiles<'a> {
        LayerFiles {
            user,
            project: Some(project),
            user_project: ws.user_project_file.as_deref(),
            project_root: ws.project_root.as_deref().or(Some(ws.cwd.as_path())),
        }
    }

    // ---- Subscriptions ----

    pub(crate) fn subscribe(&self, topic: Topic, conn: &ConnHandle, cwd: Option<&Path>) {
        let state = self.customize();
        let map = match topic {
            Topic::Config => &state.config,
            Topic::Skills => &state.skills,
            Topic::Mcp => &state.mcp,
        };
        map.lock().insert(conn.id, conn.clone());
        let dir = cwd.map(|c| her_core::instructions::find_project_root(c, &[".git".to_string()]).unwrap_or_else(|| c.to_path_buf()));
        let added = dir.as_ref().is_some_and(|d| state.workspaces.lock().entry(conn.id).or_default().insert(d.clone()));
        self.ensure_watching(if added { dir.as_deref() } else { None });
    }

    pub(crate) fn forget_customize(&self, id: ConnId) {
        let state = self.customize();
        state.config.lock().remove(&id);
        state.skills.lock().remove(&id);
        state.mcp.lock().remove(&id);
        let gone = state.workspaces.lock().remove(&id).unwrap_or_default();
        let still: HashSet<PathBuf> = state.workspaces.lock().values().flatten().cloned().collect();
        if let Some(watcher) = state.watcher.lock().as_mut() {
            for dir in gone.difference(&still) {
                for (path, recursive) in workspace_paths(dir) {
                    watcher.unwatch(&path, recursive);
                }
            }
        }
    }

    fn notify(&self, topic: Topic, method: &str, params: serde_json::Value) {
        let state = self.customize();
        let map = match topic {
            Topic::Config => &state.config,
            Topic::Skills => &state.skills,
            Topic::Mcp => &state.mcp,
        };
        for conn in map.lock().values() {
            conn.send(her_rpc::notification(method, &params));
        }
    }

    fn config_changed(&self, file: &Path) {
        self.notify(Topic::Config, method::CONFIG_CHANGED, json!({ "file": file }));
        self.push_system_connectors();
    }

    /// Every open session's browser and desktop settings, from the configuration as it is now:
    /// each takes them up before its next request (connectors-entry-design §9.4). Sessions of
    /// one folder share one read of their files.
    fn push_system_connectors(&self) {
        let server = self.clone();
        tokio::spawn(async move {
            let mut by_folder: std::collections::BTreeMap<PathBuf, Vec<crate::hub::HubHandle>> = Default::default();
            for hub in server.hubs() {
                let cwd = hub.shared.summary.lock().cwd.clone();
                by_folder.entry(cwd).or_default().push(hub);
            }
            for (cwd, hubs) in by_folder {
                let (home, overrides) = (server.0.home.clone(), server.0.overrides.clone());
                let opened = tokio::task::spawn_blocking(move || her_core::Workspace::open(&home, &cwd, &overrides)).await;
                let Ok(Ok(workspace)) = opened else { continue };
                let settings = her_core::SystemSettings::of(&workspace.config);
                for hub in hubs {
                    let _ = hub.send(crate::hub::HubCmd::System(Box::new(settings.clone()))).await;
                }
            }
        });
    }

    fn skills_changed(&self) {
        self.notify(Topic::Skills, method::SKILLS_CHANGED, json!({}));
    }

    fn mcp_changed(&self, name: Option<&str>) {
        let params = match name {
            Some(name) => json!({ "name": name }),
            None => json!({}),
        };
        self.notify(Topic::Mcp, method::MCP_CHANGED, params);
    }

    /// Starts the watcher on first use, and adds a workspace's paths.
    fn ensure_watching(&self, workspace: Option<&Path>) {
        let state = self.customize();
        let mut guard = state.watcher.lock();
        if guard.is_none() {
            let home = &self.0.home;
            for dir in [home.skills_dir(), home.projects_dir(), home.mcp_cache_dir()] {
                let _ = std::fs::create_dir_all(dir);
            }
            match watch::Watcher::new() {
                Ok((mut watcher, mut rx)) => {
                    watcher.watch(home.root(), false);
                    watcher.watch(&home.skills_dir(), true);
                    watcher.watch(&home.projects_dir(), true);
                    watcher.watch(&home.mcp_cache_dir(), false);
                    if let Some(user) = home.user_home() {
                        watcher.watch(&user.join(".agents/skills"), true);
                        watcher.watch(&user.join(".claude/skills"), true);
                    }
                    *guard = Some(watcher);
                    let weak = Arc::downgrade(&self.0);
                    tokio::spawn(async move {
                        while let Some(paths) = watch::gather(&mut rx).await {
                            let Some(inner) = weak.upgrade() else { break };
                            Server(inner).announce(&paths);
                        }
                    });
                }
                Err(err) => tracing::warn!(%err, "cannot watch skills and configuration files"),
            }
        }
        if let (Some(watcher), Some(dir)) = (guard.as_mut(), workspace) {
            for (path, recursive) in workspace_paths(dir) {
                watcher.watch(&path, recursive);
            }
        }
    }

    /// Turns changed paths into notifications.
    fn announce(&self, paths: &[PathBuf]) {
        let home = &self.0.home;
        let user_home = home.user_home().map(Path::to_path_buf);
        let mut skills = false;
        let mut mcp = false;
        let mut configs: Vec<PathBuf> = Vec::new();
        for path in paths {
            if let Some(version) = self.customize().written.lock().remove(path)
                && version == config_edit::file_version(path)
            {
                continue;
            }
            let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
            let in_skills = path.starts_with(home.skills_dir())
                || user_home.as_ref().is_some_and(|u| path.starts_with(u.join(".agents/skills")) || path.starts_with(u.join(".claude/skills")))
                || path.components().any(|c| c.as_os_str() == "skills");
            if name == "config.toml" || name == ".mcp.json" {
                if !configs.contains(path) {
                    configs.push(path.clone());
                }
                mcp = true;
                skills = true;
            } else if path.starts_with(home.mcp_cache_dir()) {
                mcp = true;
            } else if in_skills {
                skills = true;
            }
        }
        for file in &configs {
            self.config_changed(file);
        }
        if skills {
            self.skills_changed();
        }
        if mcp {
            self.mcp_changed(None);
        }
    }

    // ---- Configuration and credentials ----

    pub async fn config_read(&self, params: ConfigReadParams, conn: &ConnHandle) -> Result<ConfigReadResult, RpcError> {
        self.subscribe(Topic::Config, conn, params.cwd.as_deref());
        let server = self.clone();
        tokio::task::spawn_blocking(move || {
            let ws = server.workspace(params.cwd.as_deref())?;
            let project = views::workspace_dir(&ws).join(".her").join("config.toml");
            let user = server.0.home.config_file();
            let files = server.layer_files(&ws, &project, &user);
            let loaded = config::load_layers(&files, ws.trusted, &server.0.overrides).map_err(|e| RpcError::invalid_params(e.to_string()))?;
            Ok(config_edit::read(&loaded, &files, params.layers))
        })
        .await
        .map_err(blocking_error)?
    }

    pub async fn config_write(&self, params: ConfigWriteParams) -> Result<ConfigWriteResult, RpcError> {
        if !params.layer.writable() {
            return Err(RpcError::with_kind(
                error_code::CONFIG_LAYER_READONLY,
                "config_layer_readonly",
                format!("her does not write the {} layer", params.layer.as_str()),
            ));
        }
        if params.layer == ConfigLayer::UserProject && params.cwd.is_none() {
            return Err(RpcError::invalid_params("cwd is required for the user_project layer"));
        }
        let touched = config_edit::touched_sections(&params.edits);
        let server = self.clone();
        let (written, retired) = tokio::task::spawn_blocking(move || -> Result<_, RpcError> {
            let ws = server.workspace(params.cwd.as_deref())?;
            if params.layer == ConfigLayer::UserProject && ws.user_project_file.is_none() {
                return Err(RpcError::invalid_params("a session without a project has no personal project layer"));
            }
            let dir = views::workspace_dir(&ws);
            let project = dir.join(".her").join("config.toml");
            let user = server.0.home.config_file();
            let target = config_edit::Target {
                layer: params.layer,
                files: server.layer_files(&ws, &project, &user),
                trusted: ws.trusted,
                overrides: &server.0.overrides,
                header: (params.layer == ConfigLayer::UserProject).then(|| config_edit::user_project_header(&dir)),
                allow_repo: false,
            };
            let written = config_edit::write(&target, &params.edits, params.expected_version.as_deref()).map_err(write_error)?;
            // Credentials only the removed or changed lines referred to go with them.
            let retired = config_edit::unused_credentials(&server.0.home, &written.dropped_credentials);
            for name in &retired {
                if let Err(err) = her_llm::credentials::delete(name) {
                    tracing::warn!(credential = name, %err, "cannot delete a credential no longer used");
                }
            }
            server.customize().written.lock().insert(written.result.file.clone(), written.result.version.clone());
            Ok((written, retired))
        })
        .await
        .map_err(blocking_error)??;
        self.config_changed(&written.result.file);
        if touched["mcp_servers"] || !retired.is_empty() {
            self.mcp_changed(None);
        }
        if touched["skills"] {
            self.skills_changed();
        }
        Ok(written.result)
    }

    pub async fn credentials_set(&self, params: CredentialsSetParams) -> Result<Empty, RpcError> {
        if !config_edit::valid_credential_name(&params.name) {
            return Err(RpcError::invalid_params(format!("'{}' is not a credential name her manages", params.name)));
        }
        if params.value.is_empty() {
            return Err(RpcError::invalid_params("the value is empty"));
        }
        tokio::task::spawn_blocking(move || her_llm::credentials::set(&params.name, &params.value))
            .await
            .map_err(blocking_error)?
            .map_err(store_error)?;
        self.mcp_changed(None);
        Ok(Empty {})
    }

    pub async fn credentials_delete(&self, params: CredentialsDeleteParams) -> Result<Empty, RpcError> {
        if !config_edit::valid_credential_name(&params.name) {
            return Err(RpcError::invalid_params(format!("'{}' is not a credential name her manages", params.name)));
        }
        tokio::task::spawn_blocking(move || her_llm::credentials::delete(&params.name))
            .await
            .map_err(blocking_error)?
            .map_err(store_error)?;
        self.mcp_changed(None);
        Ok(Empty {})
    }

    // ---- Skills ----

    pub(crate) fn watch_skills(&self, conn: &ConnHandle, cwd: Option<&Path>) {
        self.subscribe(Topic::Skills, conn, cwd);
    }

    pub async fn skills_install(&self, params: SkillsInstallParams) -> Result<SkillsInstallResult, RpcError> {
        let server = self.clone();
        let dry_run = params.dry_run;
        let result = tokio::task::spawn_blocking(move || -> Result<SkillsInstallResult, RpcError> {
            if !params.from.is_absolute() {
                return Err(RpcError::invalid_params("from must be an absolute path"));
            }
            let (root, scope) = match params.scope {
                SkillInstallScope::User => (server.0.home.skills_dir(), SkillScope::User),
                SkillInstallScope::Project => {
                    let ws = server.workspace(Some(params.cwd.as_deref().ok_or_else(|| RpcError::invalid_params("cwd is required for the project scope"))?))?;
                    (views::workspace_dir(&ws).join(".agents").join("skills"), SkillScope::Project)
                }
            };
            let trash = trash()?;
            let installed = install::install(&install::InstallRequest {
                from: &params.from,
                root: &root,
                scope,
                source: params.source,
                dry_run: params.dry_run,
                replace: params.replace,
                trash: &trash,
            })
            .map_err(install_error)?;
            Ok(SkillsInstallResult { skill: installed.skill, target: installed.target, conflict: installed.conflict })
        })
        .await
        .map_err(blocking_error)??;
        if !dry_run {
            self.skills_changed();
        }
        Ok(result)
    }

    pub async fn skills_remove(&self, params: SkillsRemoveParams) -> Result<Empty, RpcError> {
        let server = self.clone();
        tokio::task::spawn_blocking(move || -> Result<(), RpcError> {
            let trash = trash()?;
            install::remove(&params.path, &server.0.home.skills_dir(), &trash).map_err(install_error)?;
            Ok(())
        })
        .await
        .map_err(blocking_error)??;
        self.skills_changed();
        Ok(Empty {})
    }

    pub async fn skills_set_enabled(&self, params: SkillsSetEnabledParams) -> Result<SkillsSetEnabledResult, RpcError> {
        let server = self.clone();
        let result = tokio::task::spawn_blocking(move || {
            let result = install::set_enabled(&server.0.home, &params.path, params.enabled).map_err(write_error)?;
            server.customize().written.lock().insert(result.file.clone(), config_edit::file_version(&result.file));
            Ok::<_, RpcError>(result)
        })
        .await
        .map_err(blocking_error)??;
        self.config_changed(&result.file);
        self.skills_changed();
        Ok(result)
    }

    pub async fn skills_files(&self, params: SkillsFilesParams) -> Result<SkillsFilesResult, RpcError> {
        tokio::task::spawn_blocking(move || install::files(&params.path).map_err(install_error))
            .await
            .map_err(blocking_error)?
    }

    pub async fn skills_read(&self, params: SkillsReadParams) -> Result<SkillsReadResult, RpcError> {
        tokio::task::spawn_blocking(move || install::read(&params.path, &params.file).map_err(install_error))
            .await
            .map_err(blocking_error)?
    }

    // ---- MCP servers ----

    /// Open sessions' view of the workspace's servers.
    fn live_mcp(&self, dir: &Path) -> HashMap<String, views::Live> {
        let mut live: HashMap<String, views::Live> = HashMap::new();
        for hub in self.hubs() {
            if !hub.shared.summary.lock().cwd.starts_with(dir) {
                continue;
            }
            for (name, info) in hub.shared.mcp.lock().iter() {
                let entry = live.entry(name.clone()).or_insert(views::Live { state: info.state, tools: info.tools, error: info.error.clone(), sessions: 0 });
                if info.state == McpServerState::Ready {
                    entry.sessions += 1;
                    entry.state = McpServerState::Ready;
                    entry.tools = info.tools;
                    entry.error = None;
                } else if entry.state != McpServerState::Ready {
                    entry.state = info.state;
                    entry.error = info.error.clone();
                }
            }
        }
        live
    }

    pub async fn mcp_list(&self, params: McpListParams, conn: &ConnHandle) -> Result<McpListResult, RpcError> {
        self.subscribe(Topic::Mcp, conn, params.cwd.as_deref());
        let server = self.clone();
        tokio::task::spawn_blocking(move || {
            let ws = server.workspace(params.cwd.as_deref())?;
            let live = server.live_mcp(&views::workspace_dir(&ws));
            Ok(views::list(&ws, &server.0.home, &live))
        })
        .await
        .map_err(blocking_error)?
    }

    pub async fn mcp_tools(&self, params: McpServerParams) -> Result<McpToolsResult, RpcError> {
        let server = self.clone();
        tokio::task::spawn_blocking(move || {
            let ws = server.workspace(params.cwd.as_deref())?;
            let config = ws
                .mcp_servers
                .iter()
                .find(|s| s.name == params.name)
                .ok_or_else(|| RpcError::invalid_params(format!("no MCP server named {}", params.name)))?;
            let live = server.live_mcp(&views::workspace_dir(&ws));
            Ok(views::tools(&ws, &server.0.home, config, live.get(&params.name)))
        })
        .await
        .map_err(blocking_error)?
    }

    /// `mcp/probe` (mcp-design §13.6): checks a server that is not added yet. Each step goes
    /// to the calling connection as `mcp/probe_progress`.
    pub async fn mcp_probe(&self, params: McpProbeParams, conn: &ConnHandle) -> Result<McpProbeResult, RpcError> {
        let mut config = params.server.clone();
        config.enabled = None;
        config.validate().map_err(RpcError::invalid_params)?;
        let server = self.clone();
        let cwd = params.cwd.clone();
        let ws = tokio::task::spawn_blocking(move || server.workspace(cwd.as_deref())).await.map_err(blocking_error)??;
        let dir = views::workspace_dir(&ws);
        let cancel = tokio_util::sync::CancellationToken::new();
        if let Some(old) = self.customize().probes.lock().insert(params.probe_id.clone(), cancel.clone()) {
            old.cancel();
        }
        let (conn, probe_id) = (conn.clone(), params.probe_id.clone());
        let progress: her_mcp::probe::ProgressFn = Arc::new(move |step: &McpProbeStep| {
            let params = McpProbeProgressParams { probe_id: probe_id.clone(), step: step.clone() };
            conn.send(her_rpc::notification(method::MCP_PROBE_PROGRESS, &params));
        });
        let ctx = her_mcp::probe::ProbeContext {
            server: her_mcp::ServerContext {
                http: self.0.llm.http_client().ok(),
                roots: vec![dir.clone()],
                cwd: ws.cwd.clone(),
                project_root: ws.project_root.clone(),
                on_status: None,
                oauth: Some(her_mcp::oauth::OAuthPaths { her_home: self.0.home.root().to_path_buf() }),
            },
            metadata_url: ws.config.mcp.client_metadata_url.clone(),
            progress,
            cancel,
            icons: Some(self.0.home.mcp_icons_dir()),
        };
        let result = her_mcp::probe::probe(config, ctx).await;
        self.customize().probes.lock().remove(&params.probe_id);
        Ok(result)
    }

    pub async fn mcp_probe_cancel(&self, params: McpProbeCancelParams) -> Result<Empty, RpcError> {
        if let Some(cancel) = self.customize().probes.lock().remove(&params.probe_id) {
            cancel.cancel();
        }
        Ok(Empty {})
    }

    /// `mcp/login` (mcp-design §13.8): starts a sign-in and returns the address for the
    /// browser; `mcp/changed { name, login_id }` says how it ended.
    pub async fn mcp_login(&self, params: McpLoginParams) -> Result<McpLoginResult, RpcError> {
        let redirect_uri = params.redirect_uri.as_deref().map(loopback_redirect).transpose()?;
        let server = self.clone();
        let cwd = params.cwd.clone();
        let ws = tokio::task::spawn_blocking(move || server.workspace(cwd.as_deref())).await.map_err(blocking_error)??;
        let config = match params.server.clone() {
            Some(config) => config,
            None => views::for_check(&ws, &params.name).ok_or_else(|| RpcError::invalid_params(format!("no MCP server named {}", params.name)))?,
        };
        if config.name != params.name { return Err(RpcError::invalid_params("连接器名称不匹配")); }
        let managed = if config.connector.is_some() {
            if redirect_uri.is_some() {
                return Err(RpcError::invalid_params(format!("{} signs in through its own program on this machine, which takes no redirect_uri", params.name)));
            }
            Some(her_core::connectors::for_server(self.0.home.root(), &config).ok_or_else(|| RpcError::invalid_params("托管连接器配置已改变，请重新安装"))?)
        } else {
            if config.url.is_none() { return Err(RpcError::invalid_params(format!("{} is not a remote server", params.name))); }
            None
        };
        // Both curated CLIs and ordinary MCP OAuth flows belong to the service. A lost
        // request or renderer reload can resume the same flow, including discovery.
        let key = format!("connector-login:{}", params.name);
        let lock = self.customize().checks.lock().entry(key).or_default().clone();
        let startup = lock.lock().await;
        let cancelled = self.customize().logins.lock().get(&params.name).is_some_and(|t| t.is_cancelled());
        if cancelled { self.wait_for_login_end(&params.name).await?; }
        let existing = self.customize().login_results.lock().get(&params.name).cloned();
        if let Some(existing) = existing {
            if existing.config == config && existing.client == params.client && existing.redirect_uri == redirect_uri {
                drop(startup);
                return login_result(existing.response).await;
            }
            // An edited URL/client must never resume another resource's authorization.
            if let Some(token) = self.customize().logins.lock().get(&params.name) { token.cancel(); }
            self.wait_for_login_end(&params.name).await?;
        }
        let token = tokio_util::sync::CancellationToken::new();
        let (progress, response) = tokio::sync::watch::channel(None);
        self.customize().logins.lock().insert(params.name.clone(), token.clone());
        self.customize().login_results.lock().insert(params.name.clone(), LoginAttempt { config: config.clone(), client: params.client, redirect_uri: redirect_uri.clone(), response: response.clone() });
        let server = self.clone();
        let name = params.name.clone();
        tokio::spawn(async move {
            let login_id = uuid::Uuid::new_v4().to_string();
            let outcome = async {
                if let Some(m) = managed {
                    let mut pending = her_core::connectors::login(server.0.home.root().to_path_buf(), m, token.clone()).await?;
                    let mut result = McpLoginResult { authorize_url: pending.url.clone(), login_id: login_id.clone(), client: McpOAuthClientKind::Managed };
                    progress.send_replace(Some(Ok(result.clone())));
                    let mut updates = pending.take_updates();
                    let wait = pending.wait();
                    tokio::pin!(wait);
                    let mut updating = true;
                    loop {
                        tokio::select! {
                            done = &mut wait => { done?; break; },
                            url = updates.recv(), if updating => match url {
                                Some(url) => {
                                    result.authorize_url = url.clone();
                                    progress.send_replace(Some(Ok(result.clone())));
                                    server.notify(Topic::Mcp, method::MCP_CHANGED, json!({
                                        "name": name, "login_id": login_id, "authorize_url": url,
                                    }));
                                },
                                None => updating = false,
                            },
                        }
                    }
                    let checked = server.mcp_check(McpServerParams { name: name.clone(), cwd: params.cwd }).await.map_err(|e| e.message)?;
                    if checked.server.status.state != McpStatusState::Ok || checked.server.auth_state != Some(McpAuthState::LoggedIn) {
                        return Err("授权后的连接检查未通过，连接器保持停用".into());
                    }
                    if token.is_cancelled() { return Err("已取消登录".into()); }
                    server.set_managed_enabled(&name, true).await.map_err(|e| e.message)
                } else {
                    let url = config.url.clone().ok_or("连接器地址缺失")?;
                    let own = tokio::task::spawn_blocking(move || own_client(&config)).await.map_err(|e| e.to_string())??;
                    let request = her_mcp::oauth::LoginRequest {
                        server: name.clone(), url,
                        own, metadata_url: ws.config.mcp.client_metadata_url.clone(),
                        paths: Some(her_mcp::oauth::OAuthPaths { her_home: server.0.home.root().to_path_buf() }),
                        http: server.0.llm.http_client().map_err(|e| e.to_string())?, prefer: params.client,
                        redirect_uri,
                    };
                    let pending = tokio::select! {
                        _ = token.cancelled() => return Err("已取消登录".into()),
                        result = tokio::time::timeout(std::time::Duration::from_secs(60), her_mcp::oauth::start_login(request)) => result.map_err(|_| "获取授权链接超时，请重试")??,
                    };
                    if let Some(completion) = pending.completion() {
                        server.customize().completions.lock().insert(login_id.clone(), completion);
                    }
                    progress.send_replace(Some(Ok(McpLoginResult { authorize_url: pending.authorize_url.clone(), login_id: login_id.clone(), client: pending.client })));
                    let cancel_provider = pending.canceller();
                    let wait = pending.wait(LOGIN_WAIT);
                    tokio::pin!(wait);
                    tokio::select! {
                        result = &mut wait => result,
                        _ = token.cancelled() => {
                            cancel_provider.cancel();
                            let _ = wait.await; // wait for the callback listener/token exchange to finish
                            Err("已取消登录".into())
                        },
                    }
                }
            }.await;
            if progress.borrow().is_none() {
                progress.send_replace(Some(Err(outcome.clone().err().unwrap_or_else(|| "登录流程已结束".into()))));
            }
            {
                let mut results = server.customize().login_results.lock();
                server.customize().logins.lock().remove(&name);
                server.customize().completions.lock().remove(&login_id);
                results.remove(&name);
            }
            let mut changed = json!({ "name": name, "login_id": login_id });
            if let Err(error) = outcome { changed["login_error"] = json!(error); }
            server.notify(Topic::Mcp, method::MCP_CHANGED, changed);
        });
        drop(startup);
        login_result(response).await
    }

    /// `mcp/login_complete`: finishes a sign-in started with a `redirect_uri`, with what its
    /// redirect carried, once the token is saved. `mcp/changed` follows, as for any sign-in.
    pub async fn mcp_login_complete(&self, p: McpLoginCompleteParams) -> Result<Empty, RpcError> {
        let completion = self.customize().completions.lock().get(&p.login_id).cloned();
        let completion = completion.ok_or_else(|| RpcError::invalid_params(format!("no sign-in started with a redirect_uri is waiting as {}", p.login_id)))?;
        if !completion.matches(&p.state) {
            return Err(RpcError::invalid_params("the state does not belong to this sign-in"));
        }
        completion.complete(p.code, p.state, p.iss).await.map_err(|e| RpcError::with_kind(error_code::MCP_LOGIN_FAILED, "mcp_login_failed", e))?;
        Ok(Empty {})
    }

    /// `mcp/logout`: revokes and deletes a server's tokens, and ends a sign-in in progress.
    pub async fn mcp_logout(&self, params: McpLogoutParams) -> Result<Empty, RpcError> {
        if let Some(cancel) = self.customize().logins.lock().get(&params.name) {
            cancel.cancel();
        }
        // Wait for the provider process to exit before removing its tokens, so an in-flight
        // callback cannot recreate credentials after logout.
        self.wait_for_login_end(&params.name).await?;
        let ws = self.workspace(None)?;
        if let Some(config) = ws.mcp_servers.iter().find(|s| s.name == params.name && s.connector.is_some()) {
            let m = her_core::connectors::for_server(self.0.home.root(), config).ok_or_else(|| RpcError::invalid_params("托管连接器配置无效"))?;
            self.set_managed_enabled(&params.name, false).await?;
            her_core::connectors::logout(self.0.home.root(), &m).await.map_err(RpcError::internal)?;
            self.mcp_changed(Some(&params.name));
            return Ok(Empty {});
        }
        let http = self.0.llm.http_client().map_err(|e| RpcError::internal(e.to_string()))?;
        her_mcp::oauth::logout(&params.name, &http).await.map_err(store_error)?;
        self.mcp_changed(Some(&params.name));
        Ok(Empty {})
    }

    pub async fn mcp_check(&self, params: McpServerParams) -> Result<McpCheckResult, RpcError> {
        let server = self.clone();
        let cwd = params.cwd.clone();
        let ws = tokio::task::spawn_blocking(move || server.workspace(cwd.as_deref())).await.map_err(blocking_error)??;
        let config = views::for_check(&ws, &params.name).ok_or_else(|| RpcError::invalid_params(format!("no MCP server named {}", params.name)))?;
        let dir = views::workspace_dir(&ws);
        let key = format!("{}\u{0}{}", dir.display(), params.name);
        let lock = self.customize().checks.lock().entry(key).or_default().clone();
        let _one_at_a_time = lock.lock().await;
        if let Some(m) = her_core::connectors::for_server(self.0.home.root(), &config) {
            her_core::connectors::status(self.0.home.root(), &m).await.map_err(RpcError::internal)?;
        }
        let http = config.url.is_some().then(|| self.0.llm.http_client().ok()).flatten();
        let (identity, url) = (her_mcp::identity::identity(&config), config.url.clone());
        let checks = her_core::mcp::check_servers(
            vec![config],
            http,
            vec![dir.clone()],
            ws.cwd.clone(),
            ws.project_root.clone(),
            Some(self.0.home.mcp_cache_dir()),
            Some(self.0.home.root().to_path_buf()),
        )
        .await;
        let check = checks.into_iter().next();
        // The handshake may declare a logo the website's does not match (mcp-design §13.9). It
        // is looked for after answering; the page hears of a new one through mcp/changed.
        if let (Some(identity), Some(check)) = (identity, &check)
            && check.state == her_protocol::McpServerState::Ready
        {
            let (server, name, info, dir) = (self.clone(), params.name.clone(), check.info.clone(), self.0.home.mcp_icons_dir());
            tokio::spawn(async move {
                let before = her_mcp::icon::load(&dir, &identity).and_then(|s| s.data_uri());
                let after = her_mcp::icon::refresh(&dir, &identity, url.as_deref(), info.as_ref()).await.and_then(|s| s.data_uri());
                if after != before {
                    server.mcp_changed(Some(&name));
                }
            });
        }
        let tools = check.map(|c| c.summaries).unwrap_or_default();
        let server = self.clone();
        let name = params.name.clone();
        let view = tokio::task::spawn_blocking(move || {
            let original = ws.mcp_servers.iter().find(|s| s.name == name).cloned().unwrap_or_default();
            let live = server.live_mcp(&views::workspace_dir(&ws));
            views::view(&ws, &server.0.home, &original, live.get(&name))
        })
        .await
        .map_err(blocking_error)?;
        self.mcp_changed(Some(&params.name));
        Ok(McpCheckResult { server: view, tools })
    }
}

/// What to watch in a project for the page: its `.her` (configuration), its skill folders
/// (`.agents/skills`, and `.claude/skills`, off by default), and `.mcp.json` at its root.
fn workspace_paths(dir: &Path) -> Vec<(PathBuf, bool)> {
    vec![
        (dir.to_path_buf(), false),
        (dir.join(".her"), true),
        (dir.join(".agents").join("skills"), true),
        (dir.join(".claude").join("skills"), true),
    ]
}

#[cfg(test)]
mod tests {
    use super::loopback_redirect;

    #[test]
    fn a_redirect_must_be_a_loopback_address_with_a_port() {
        for (given, kept) in [
            ("http://127.0.0.1:53682/callback", "http://127.0.0.1:53682/callback"),
            ("http://[::1]:8080/cb?from=app", "http://[::1]:8080/cb?from=app"),
            ("http://localhost:1455/auth/callback", "http://localhost:1455/auth/callback"),
            ("http://LOCALHOST:1455", "http://localhost:1455/"),
            // Spelled oddly, it is sent as checked.
            ("http://127.1:9000/cb", "http://127.0.0.1:9000/cb"),
            ("http://127.0.0.1:9000\\@evil.example/", "http://127.0.0.1:9000/@evil.example/"),
        ] {
            assert_eq!(loopback_redirect(given).as_deref(), Ok(kept), "{given}");
        }
        for refused in [
            "https://127.0.0.1:53682/callback",
            "https://evil.example/callback",
            "http://evil.example:8080/callback",
            "http://192.168.1.20:8080/callback",
            "http://127.0.0.2:8080/callback",
            "http://localhost.evil.example:8080/callback",
            "http://127.0.0.1.evil.example:8080/",
            "http://127.0.0.1/callback",
            "http://127.0.0.1:80/callback",
            "http://127.0.0.1:0/callback",
            "http://user@127.0.0.1:8080/callback",
            "http://evil.example@127.0.0.1:8080/",
            "http://127.0.0.1:8080@evil.example/",
            "http://127.0.0.1:8080/callback#fragment",
            "app://callback",
            "127.0.0.1:8080/callback",
            "",
        ] {
            let error = loopback_redirect(refused).expect_err(refused);
            assert_eq!(error.code, her_rpc::code::INVALID_PARAMS, "{refused}");
        }
    }
}
