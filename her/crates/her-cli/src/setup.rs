//! Shared start-up: data directory, logging, configuration, model registry, tools.

use anyhow::Context as _;
use her_core::Config;
use her_core::HerHome;
use her_core::JsonlStore;
use her_core::MemoryStore;
use her_core::KernelDeps;
use her_core::SessionStore;
use her_core::Workspace;
use her_core::instructions;
use her_core::runtime;
use her_llm::ModelRegistry;
use her_protocol::McpServerConfig;
use her_protocol::PermissionMode;
use std::collections::BTreeMap;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::OnceLock;
use tracing_appender::non_blocking::WorkerGuard;
use tracing_subscriber::EnvFilter;

pub struct Runtime {
    pub home: HerHome,
    pub config: Config,
    pub llm: Arc<ModelRegistry>,
    pub connections: Arc<her_connections::Manager>,
    pub cwd: PathBuf,
    pub project_root: Option<PathBuf>,
    pub trusted: bool,
    pub warnings: Vec<String>,
    /// Settings taken from HER_MODEL and HER_PERMISSION_MODE, as `key=value`.
    pub env_overrides: Vec<String>,
    /// MCP servers of a session here: the configured ones, then `--mcp-config`. Disabled ones
    /// are kept for listing; sessions do not start them.
    pub mcp_servers: Vec<McpServerConfig>,
    /// Where each MCP server is defined.
    pub mcp_sources: BTreeMap<String, Vec<String>>,
    _log_guard: Option<WorkerGuard>,
}

/// The servers of the `--mcp-config` files, read once at start-up.
#[derive(Default)]
pub struct McpConfigFiles {
    pub servers: Vec<McpServerConfig>,
    /// The file each server came from.
    pub sources: BTreeMap<String, String>,
    pub warnings: Vec<String>,
}

static MCP_CONFIG_FILES: OnceLock<McpConfigFiles> = OnceLock::new();

/// Reads the `--mcp-config` files (the `.mcp.json` format) in order; a later file replaces
/// servers of the same name.
pub fn load_mcp_config(paths: &[PathBuf]) -> anyhow::Result<()> {
    let mut files = McpConfigFiles::default();
    for path in paths {
        let text = std::fs::read_to_string(path).with_context(|| format!("cannot read --mcp-config {}", path.display()))?;
        let (servers, warnings) =
            her_core::mcp::parse_mcp_json(&text, path).map_err(|e| anyhow::anyhow!("--mcp-config {e}"))?;
        for server in servers {
            files.sources.insert(server.name.clone(), format!("--mcp-config {}", path.display()));
            files.servers.push(server);
        }
        files.warnings.extend(warnings);
    }
    let _ = MCP_CONFIG_FILES.set(files);
    Ok(())
}

pub fn mcp_config_files() -> &'static McpConfigFiles {
    MCP_CONFIG_FILES.get_or_init(McpConfigFiles::default)
}

pub fn home() -> anyhow::Result<HerHome> {
    let home = HerHome::from_env()?;
    home.ensure()
        .with_context(|| format!("cannot create {}", home.root().display()))?;
    // Saved keys come after the configuration and the environment (models.toml stays the way
    // to configure them; `auth/set` adds to it).
    if let Some(store) = her_llm::credentials::default_store(home.root().join("credentials.toml")) {
        her_llm::credentials::install(store);
        her_llm::credentials::watch(home.root().join("credentials.stamp"));
    }
    Ok(home)
}

/// Start-up of a command that calls a model or a server: it writes the log file.
pub fn init(cwd: PathBuf, overrides: &[String]) -> anyhow::Result<Runtime> {
    let home = home()?;
    let log_guard = init_logging(&home);
    load(home, log_guard, cwd, overrides)
}

/// Start-up of a command that only shows local information: no log file, and the result lasts
/// until the process exits. Nothing in it has to be cleaned up without a log file, and freeing
/// it on the way out would be most of what such a command does after printing.
pub fn init_local(cwd: PathBuf, overrides: &[String]) -> anyhow::Result<&'static Runtime> {
    let home = home()?;
    Ok(Box::leak(Box::new(load(home, None, cwd, overrides)?)))
}

fn load(home: HerHome, log_guard: Option<WorkerGuard>, cwd: PathBuf, overrides: &[String]) -> anyhow::Result<Runtime> {
    // Environment variables beat the config files; `-c` beats them.
    let mut warnings = Vec::new();
    let env_overrides = env_overrides(&mut warnings)?;
    let all: Vec<String> = env_overrides.iter().chain(overrides).cloned().collect();
    let workspace = Workspace::open(&home, &cwd, &all)?;
    warnings.extend(workspace.warnings.iter().cloned());
    let files = mcp_config_files();
    warnings.extend(files.warnings.iter().cloned());
    let (mcp_servers, problems) = workspace.mcp_servers_with(&files.servers);
    warnings.extend(problems);
    let mut mcp_sources = workspace.mcp_sources;
    for (name, source) in &files.sources {
        mcp_sources.insert(name.clone(), vec![source.clone()]);
    }
    let llm = Arc::new(runtime::model_registry(&home, &workspace.config)?);
    warnings.extend(llm.warnings().iter().cloned());
    let connections = her_connections::Manager::new(home.root().to_path_buf(), &llm);
    tracing::info!(cwd = %workspace.cwd.display(), project_root = ?workspace.project_root, trusted = workspace.trusted, "her started");
    Ok(Runtime {
        home,
        config: workspace.config,
        llm,
        connections,
        cwd: workspace.cwd,
        project_root: workspace.project_root,
        trusted: workspace.trusted,
        warnings,
        env_overrides,
        mcp_servers,
        mcp_sources,
        _log_guard: log_guard,
    })
}

/// No model to run on, with what to do about it.
#[derive(Debug)]
pub struct NoModel {
    pub help: String,
}

impl std::fmt::Display for NoModel {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "no model is set up\n\n{}", self.help)
    }
}

impl std::error::Error for NoModel {}

/// The model of a new session: the one asked for, the configured one, or the one the first usable
/// provider starts on. No model is assumed: with none, the error says how to set one up.
pub fn session_model(rt: &Runtime, asked: Option<String>) -> anyhow::Result<String> {
    her_core::models::session_model(&rt.llm, asked, rt.config.model.as_deref())
        .map_err(|_| NoModel { help: no_model_help(rt) }.into())
}

/// A model whose provider has no credentials: nothing can run on it.
#[derive(Debug)]
pub struct NoCredentials {
    pub model: String,
    pub help: String,
}

impl std::fmt::Display for NoCredentials {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "no credentials for {}\n  {}", self.model, self.help)
    }
}

impl std::error::Error for NoCredentials {}

/// Fails before anything starts when the model cannot run: this is a mistake in the setup, not a
/// failed task, and it should not leave a session behind (standalone-cli-design §6.2).
pub fn check_credentials(rt: &Runtime, reference: &str) -> anyhow::Result<()> {
    let Ok(resolved) = rt.llm.resolve(reference) else {
        return Ok(()); // The kernel words a bad reference.
    };
    let Some(provider) = rt.llm.provider(&resolved.model.provider) else {
        return Ok(());
    };
    if !matches!(provider.auth.status(&provider.id), her_llm::AuthStatus::Missing(_)) {
        return Ok(());
    }
    let help = match provider.auth.env_vars().first() {
        Some(variable) => format!("run `her login {}`, or set {variable}", provider.id),
        None => format!("run `her login {}`", provider.id),
    };
    Err(NoCredentials { model: reference.to_string(), help }.into())
}

/// What to do while no model is set up, in the ways that work from a terminal.
pub fn no_model_help(rt: &Runtime) -> String {
    let mut keys: Vec<String> = rt
        .llm
        .providers()
        .filter(|p| p.plan.is_none() && matches!(p.auth, her_llm::AuthConfig::ApiKey { .. }))
        .flat_map(|p| p.auth.env_vars().to_vec())
        .collect();
    keys.sort();
    keys.dedup();
    format!(
        "To set up a model, run: her login\n\n\
         It also takes: her login chatgpt, her login deepseek --key-stdin, her login NAME --base-url URL\n\
         A key in the environment works too: {}\n\
         A local Ollama: pass -m ollama/<model>\n\n\
         `her models` lists every provider and what it needs. To choose the default model, set\n\
         model = \"provider/model-id\" in {}",
        keys.join(", "),
        rt.home.config_file().display(),
    )
}

/// HER_MODEL and HER_PERMISSION_MODE as `key=value` overrides. HER_APPROVAL is the old
/// name of HER_PERMISSION_MODE.
pub fn env_overrides(warnings: &mut Vec<String>) -> anyhow::Result<Vec<String>> {
    let var = |name: &str| {
        let value = std::env::var(name).ok()?;
        let value = value.trim();
        (!value.is_empty()).then(|| value.to_string())
    };
    let mut overrides = Vec::new();
    if let Some(model) = var("HER_MODEL") {
        overrides.push(format!("model={model}"));
    }
    let mode = match (var("HER_PERMISSION_MODE"), var("HER_APPROVAL")) {
        (Some(mode), _) => Some(("HER_PERMISSION_MODE", mode)),
        (None, Some(mode)) => {
            warnings.push("HER_APPROVAL is now called HER_PERMISSION_MODE".to_string());
            Some(("HER_APPROVAL", mode))
        }
        (None, None) => None,
    };
    if let Some((name, value)) = mode {
        let (mode, notice) = PermissionMode::parse_with_notice(&value).map_err(|err| anyhow::anyhow!("{name}: {err}"))?;
        warnings.extend(notice.map(|notice| format!("{name}: {notice}")));
        overrides.push(format!("permission_mode={mode}"));
    }
    Ok(overrides)
}

/// The project that `dir` belongs to (its git checkout), or `dir` itself. Sessions are
/// listed and continued per workspace.
pub fn workspace(dir: &Path) -> PathBuf {
    let dir = std::fs::canonicalize(dir).unwrap_or_else(|_| dir.to_path_buf());
    instructions::find_project_root(&dir, &[".git".to_string()]).unwrap_or(dir)
}

impl Runtime {
    /// `save: false` keeps the session log in memory only.
    pub fn kernel_deps(&self, save: bool, extra_roots: &[PathBuf]) -> anyhow::Result<KernelDeps> {
        let store: Arc<dyn SessionStore> = if save {
            Arc::new(JsonlStore::new(self.home.sessions_dir()))
        } else {
            Arc::new(MemoryStore::new())
        };
        let mut deps = runtime::kernel_deps(&self.config, &self.cwd, self.project_root.as_deref(), self.llm.clone(), store, extra_roots)?;
        deps.mcp_servers = self.mcp_servers.clone();
        // `her exec` runs one session: its own pool, without a window unless configured.
        if self.config.browser.configured_on() {
            deps.browser_pool = Some(Arc::new(her_core::BrowserPool::new(self.home.browser_dir(), false)));
        }
        deps.docs = Some(Arc::new(her_docs::DocStore::new(self.home.root().join("docs"), her_docs::Launcher::this_program())));
        Ok(deps)
    }
}

/// Logs go to `~/.her/logs/her.YYYY-MM-DD.log`, never to the terminal. Filter with `HER_LOG`.
pub fn init_logging(home: &HerHome) -> Option<WorkerGuard> {
    std::fs::create_dir_all(home.logs_dir()).ok()?;
    let appender = tracing_appender::rolling::Builder::new()
        .rotation(tracing_appender::rolling::Rotation::DAILY)
        .filename_prefix("her")
        .filename_suffix("log")
        .max_log_files(7)
        .build(home.logs_dir())
        .ok()?;
    // Every buffered line is a slot reserved at start-up (4096 took 144 KiB, a quarter of an
    // idle process's heap). In a week of real use the busiest second logged 38 lines, and the
    // writer thread drains the queue as lines arrive.
    let (writer, guard) = tracing_appender::non_blocking::NonBlockingBuilder::default()
        .buffered_lines_limit(256)
        .finish(appender);
    let fmt = tracing_subscriber::fmt().with_writer(writer).with_ansi(false);
    // Parsing filter directives costs more than the rest of this; the default needs none.
    let installed = match std::env::var_os("HER_LOG") {
        Some(_) => fmt
            .with_env_filter(EnvFilter::try_from_env("HER_LOG").unwrap_or_else(|_| EnvFilter::new("info")))
            .try_init(),
        None => fmt.with_max_level(tracing::Level::INFO).try_init(),
    };
    installed.ok()?;
    Some(guard)
}

/// The prompt argument; "-" reads it from stdin.
pub fn read_prompt(arg: &str) -> anyhow::Result<String> {
    let text = if arg == "-" {
        std::io::read_to_string(std::io::stdin()).context("cannot read the prompt from stdin")?
    } else {
        arg.to_string()
    };
    let text = text.trim().to_string();
    anyhow::ensure!(!text.is_empty(), "the prompt is empty");
    Ok(text)
}
