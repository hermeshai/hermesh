//! Per-directory set-up shared by the command line and the server: project root, trust,
//! layered configuration, the model registry and the dependencies of a kernel.

use her_llm::Catalog;
use her_llm::CatalogError;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use her_llm::RetryPolicy;
use her_llm::Timeouts;
use her_protocol::McpServerConfig;
use her_protocol::customize::ConfigLayer;
use her_tools::BashTool;
use her_tools::LocalEnvironment;
use her_tools::RegistryError;
use her_tools::ToolRegistry;
use her_tools::builtin_registry;
use parking_lot::Mutex;
use std::collections::BTreeMap;
use std::collections::HashMap;
use std::io;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::OnceLock;
use std::time::Duration;

use crate::config;
use crate::config::Config;
use crate::config::ConfigError;
use crate::instructions;
use crate::kernel::KernelDeps;
use crate::mcp;
use crate::paths::HerHome;
use crate::store::SessionStore;

#[derive(Debug, thiserror::Error)]
pub enum RuntimeError {
    #[error("working directory {path} not found: {source}")]
    Cwd {
        path: PathBuf,
        #[source]
        source: io::Error,
    },
    #[error(transparent)]
    Config(#[from] ConfigError),
    #[error(transparent)]
    Catalog(#[from] CatalogError),
    #[error(transparent)]
    Tools(#[from] RegistryError),
}

/// A working directory with its project, trust state and configuration resolved.
#[derive(Debug, Clone)]
pub struct Workspace {
    /// Canonical.
    pub cwd: PathBuf,
    pub project_root: Option<PathBuf>,
    pub trusted: bool,
    pub config: Config,
    pub warnings: Vec<String>,
    /// MCP servers from the configuration (and `.mcp.json` when enabled), validated.
    /// Disabled ones are kept, so they can be listed; sessions do not start them.
    pub mcp_servers: Vec<McpServerConfig>,
    /// Where each server is defined: configuration layers, or ".mcp.json".
    pub mcp_sources: BTreeMap<String, Vec<String>>,
    /// The user's own configuration for this project (`~/.her/projects/<key>/config.toml`),
    /// whether or not it exists; none for a session without a project.
    pub user_project_file: Option<PathBuf>,
    /// The layer that last set each configuration key.
    pub origins: BTreeMap<String, ConfigLayer>,
}

impl Workspace {
    /// `overrides` are `key=value` settings that beat both configuration files.
    pub fn open(home: &HerHome, cwd: &Path, overrides: &[String]) -> Result<Self, RuntimeError> {
        let cwd = std::fs::canonicalize(cwd).map_err(|source| RuntimeError::Cwd {
            path: cwd.to_path_buf(),
            source,
        })?;
        // A session without a project, or a bot's, has its own folder, whatever lies around it.
        let is_chat = home.has_no_project(&cwd);
        let project_root = if is_chat {
            None
        } else {
            instructions::find_project_root(&cwd, &[".git".to_string()])
        };
        let project_dir = project_root.clone().unwrap_or_else(|| cwd.clone());
        let trusted = instructions::is_trusted(home, &project_dir);
        let project_config = project_dir.join(".her").join("config.toml");
        let user_project_file = (!is_chat).then(|| home.user_project_config(&project_dir));
        let files = config::LayerFiles {
            user: &home.config_file(),
            project: Some(&project_config),
            user_project: user_project_file.as_deref(),
            project_root: Some(&project_dir),
        };
        let loaded = config::load_layers(&files, trusted, overrides)?;
        let mut warnings = loaded.warnings;
        let mut config = loaded.config;
        config.mcp.cache_dir = Some(home.mcp_cache_dir());
        config.mcp.her_home = Some(home.root().to_path_buf());
        let mut mcp_sources = loaded.mcp_sources;
        let origins = loaded.origins;
        let mcp_servers = if config.mcp.enabled {
            let project_json = project_mcp_json(&config, &project_dir, trusted, &mut warnings);
            // `.mcp.json` adds the servers the configuration does not define; the
            // configuration's settings for one of them still apply.
            for server in &project_json {
                let configured = config.mcp_servers.iter().find(|s| s.name == server.name);
                if configured.is_none_or(mcp::is_settings_only) {
                    mcp_sources.entry(server.name.clone()).or_default().insert(0, ".mcp.json".to_string());
                }
            }
            let (mut servers, problems) = mcp::merge_servers(&config.mcp_servers, project_json, &[]);
            warnings.extend(problems);
            for server in &mut servers {
                server.project_defined = mcp_sources
                    .get(&server.name)
                    .is_some_and(|layers| layers.iter().any(|layer| layer == ".mcp.json" || layer == "project config"));
            }
            servers
        } else {
            Vec::new()
        };
        Ok(Self {
            cwd,
            project_root,
            trusted,
            config,
            warnings,
            mcp_servers,
            mcp_sources,
            user_project_file,
            origins,
        })
    }

    /// The servers of a session: this workspace's, then `extra` (from the command line or a
    /// client), which replace servers of the same name.
    pub fn mcp_servers_with(&self, extra: &[McpServerConfig]) -> (Vec<McpServerConfig>, Vec<String>) {
        if !self.config.mcp.enabled {
            return (Vec::new(), Vec::new());
        }
        mcp::merge_servers(&self.mcp_servers, Vec::new(), extra)
    }

    /// Dependencies for a session in this workspace, with its MCP servers.
    pub fn kernel_deps(
        &self,
        llm: Arc<ModelRegistry>,
        store: Arc<dyn SessionStore>,
        extra_roots: &[PathBuf],
    ) -> Result<KernelDeps, RuntimeError> {
        let mut deps = kernel_deps(&self.config, &self.cwd, self.project_root.as_deref(), llm, store, extra_roots)?;
        deps.mcp_servers = self.mcp_servers.clone();
        Ok(deps)
    }
}

/// What an untrusted project at `project_dir` would bring if it were trusted, one line each, the
/// commands of its servers among them (standalone-cli-design §12.2). Nothing means nothing to ask.
pub fn what_trust_brings(project_dir: &Path) -> Vec<String> {
    let mut lines = Vec::new();
    let path = project_dir.join(".mcp.json");
    if let Some((servers, _)) = std::fs::read_to_string(&path).ok().and_then(|text| mcp::parse_mcp_json(&text, &path).ok())
        && !servers.is_empty()
    {
        lines.push(format!(".mcp.json: {} MCP server(s)", servers.len()));
        for server in &servers {
            let how = match (&server.command, &server.url) {
                (Some(command), _) => std::iter::once(command.clone()).chain(server.args.iter().cloned()).collect::<Vec<_>>().join(" "),
                (None, Some(url)) => url.clone(),
                (None, None) => "(no command)".to_string(),
            };
            lines.push(format!("  {}: {how}", server.name));
        }
    }
    let path = project_dir.join(".her").join("config.toml");
    if let Some(mut table) = std::fs::read_to_string(&path).ok().and_then(|text| toml::from_str::<toml::Table>(&text).ok()) {
        let items = config::strip_untrusted(&mut table);
        if !items.is_empty() {
            lines.push(format!(".her/config.toml: {}", items.join(", ")));
        }
    }
    lines
}

/// `.mcp.json` at the project root, when `mcp.project_json` is on and the project trusted.
fn project_mcp_json(config: &Config, project_dir: &Path, trusted: bool, warnings: &mut Vec<String>) -> Vec<McpServerConfig> {
    let path = project_dir.join(".mcp.json");
    if !config.mcp.project_json || !path.is_file() {
        return Vec::new();
    }
    if !trusted {
        warnings.push(format!(
            "ignored {} because the project is not trusted (run `her trust` to allow it)",
            path.display()
        ));
        return Vec::new();
    }
    match std::fs::read_to_string(&path).map_err(|e| e.to_string()).and_then(|text| mcp::parse_mcp_json(&text, &path)) {
        Ok((servers, problems)) => {
            warnings.extend(problems);
            servers
        }
        Err(err) => {
            warnings.push(format!("ignored {}: {err}", path.display()));
            Vec::new()
        }
    }
}

/// Built-in tools enabled by `config`, a local environment rooted at the project, and `store`.
pub fn kernel_deps(
    config: &Config,
    cwd: &Path,
    project_root: Option<&Path>,
    llm: Arc<ModelRegistry>,
    store: Arc<dyn SessionStore>,
    extra_roots: &[PathBuf],
) -> Result<KernelDeps, RuntimeError> {
    let tools = shared_tools(config)?;
    let mut roots = vec![project_root.unwrap_or(cwd).to_path_buf()];
    roots.extend(config.extra_writable_roots.iter().cloned());
    roots.extend(extra_roots.iter().cloned());
    let scrubbed = if config.pass_provider_keys {
        Vec::new()
    } else {
        llm.key_env_vars()
    };
    let mut env = LocalEnvironment::new(cwd.to_path_buf())
        .with_roots(roots)
        .with_scrubbed_env(scrubbed);
    if let Some(shell) = &config.bash_shell {
        env = env.with_shell(shell.clone());
    }
    Ok(KernelDeps {
        llm,
        tools,
        env: Arc::new(env),
        store,
        hooks: Vec::new(),
        mcp_servers: Vec::new(),
        browser_pool: None,
        docs: None,
        decks: None,
        words: None,
        routines: None,
        connectors: None,
    })
}

/// Key of a built-in tool set: the enabled tools and the bash timeouts.
type ToolsKey = (Vec<String>, Duration, Duration);

/// Built-in tools hold no session state, so sessions with the same settings share one
/// registry, including its tool definitions.
fn shared_tools(config: &Config) -> Result<Arc<ToolRegistry>, RuntimeError> {
    static CACHE: OnceLock<Mutex<HashMap<ToolsKey, Arc<ToolRegistry>>>> = OnceLock::new();
    let key = (config.enabled_tools.clone(), config.bash_default_timeout, config.bash_max_timeout);
    let cache = CACHE.get_or_init(Default::default);
    if let Some(tools) = cache.lock().get(&key) {
        return Ok(tools.clone());
    }
    let all = builtin_registry(BashTool {
        default_timeout: config.bash_default_timeout,
        max_timeout: config.bash_max_timeout,
    });
    let tools = Arc::new(all.subset(&config.enabled_tools)?);
    cache.lock().insert(key, tools.clone());
    Ok(tools)
}

/// The built-in catalog merged with `models.toml`, with the timeouts and retries of `config`.
pub fn model_registry(home: &HerHome, config: &Config) -> Result<ModelRegistry, RuntimeError> {
    let catalog = Catalog::load(Some(&home.models_file()))?;
    let options = RegistryOptions {
        timeouts: Timeouts {
            connect: config.llm_connect_timeout,
            first_byte: Duration::from_secs(60),
            idle: config.llm_idle_timeout,
        },
        retry: RetryPolicy {
            window: config.llm_retry_window,
            max_retries: config.llm_max_retries,
            ..Default::default()
        },
        user_agent: None,
        models_dir: Some(home.models_dir()),
    };
    Ok(ModelRegistry::new(catalog, options))
}
