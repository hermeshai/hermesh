//! Layered configuration: defaults < user file < project file < the user's own file for the
//! project < command-line overrides.

use her_llm::ThinkingLevel;
use her_protocol::CommandRule;
use her_protocol::McpExposure;
use her_protocol::McpServerConfig;
use her_protocol::PermissionMode;
use her_protocol::customize::ConfigLayer;
use std::collections::BTreeMap;
use her_tools::OutputLimits;
use serde::Deserialize;
use std::io;
use std::path::Path;
use std::path::PathBuf;
use std::time::Duration;

#[derive(Debug, Clone, Default, Deserialize)]
pub struct ConfigFile {
    pub model: Option<String>,
    pub thinking: Option<ThinkingLevel>,
    #[serde(alias = "approval_mode")]
    pub permission_mode: Option<PermissionMode>,
    pub max_turns: Option<u32>,
    pub extra_writable_roots: Option<Vec<PathBuf>>,
    pub system_prompt_file: Option<PathBuf>,
    pub bot_prompt_file: Option<PathBuf>,
    pub instructions: Option<InstructionsFile>,
    pub tools: Option<ToolsFile>,
    pub compaction: Option<CompactionFile>,
    pub llm: Option<LlmFile>,
    pub rules: Option<Vec<CommandRule>>,
    pub server: Option<ServerFile>,
    pub acp: Option<AcpFile>,
    pub agents: Option<AgentsFile>,
    pub skills: Option<SkillsFile>,
    pub mcp: Option<McpFile>,
    pub browser: Option<BrowserFile>,
    pub desktop: Option<DesktopFile>,
    pub web_search: Option<WebSearchFile>,
    pub models: Option<ModelsFile>,
    pub ui: Option<UiFile>,
    pub history: Option<HistoryFile>,
    pub update: Option<UpdateFile>,
    /// `[mcp_servers.<name>]`, by name.
    pub mcp_servers: Option<BTreeMap<String, McpServerConfig>>,
}

/// `[ui]`: how the interactive mode shows things.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct UiFile {
    /// Write the model's reasoning into the scrollback, dimmed.
    pub show_thinking: Option<bool>,
}

/// `[history]`: what was typed in the interactive mode.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct HistoryFile {
    pub enabled: Option<bool>,
}

/// `[update]`
#[derive(Debug, Clone, Default, Deserialize)]
pub struct UpdateFile {
    /// Let the interactive mode look for a new release, at most once a day.
    pub check: Option<bool>,
}

/// `[mcp]`: settings for every MCP server.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct McpFile {
    pub enabled: Option<bool>,
    /// Read `.mcp.json` at the root of a trusted project.
    pub project_json: Option<bool>,
    /// How long the first request waits for servers whose tools go into the tool list.
    pub startup_wait_secs: Option<u64>,
    /// Default exposure of servers that do not set one.
    pub exposure: Option<McpExposure>,
    /// her's OAuth client metadata document; only for tests and self-built copies.
    pub client_metadata_url: Option<String>,
}

/// `[browser]`: the browser tools (docs/kernel/browser-design.md §10).
#[derive(Debug, Clone, Default, Deserialize)]
pub struct BrowserFile {
    /// Whether Hermesh controls browsers at all (browser-design §10.1).
    pub enabled: Option<bool>,
    /// The user's own browser through the Hermesh extension (§8.4): connected for the agent.
    pub extension: Option<bool>,
    /// Where pages open when a call names no browser: `builtin` or `yours`.
    pub default: Option<String>,
    /// Which built-in browser: `auto`, `embedded` or `managed` (§8.3.3). `extension`, from before
    /// the user's own browser had its own switch, reads as `extension = true`.
    pub backend: Option<String>,
    pub executable: Option<PathBuf>,
    pub profile: Option<String>,
    /// `auto`, `true` or `false`.
    pub headless: Option<toml::Value>,
    pub allowed_domains: Option<Vec<String>>,
    pub denied_domains: Option<Vec<String>>,
    pub script_enabled: Option<bool>,
    pub navigation_timeout_secs: Option<u64>,
    pub action_timeout_secs: Option<u64>,
    /// Origin → `deny`, `navigate` or `act`.
    pub sites: Option<BTreeMap<String, String>>,
}

/// Where a page opens when a call names no browser (`browser.default`).
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum DefaultBrowser {
    #[default]
    Builtin,
    Yours,
}

/// `[browser]` with its defaults applied.
#[derive(Debug, Clone, PartialEq)]
pub struct BrowserSettings {
    /// Whether Hermesh controls browsers (`browser.enabled`); `None` when not set: on where a
    /// desktop app shows browsers or the user's own browser is connected (browser-design §10.1).
    pub enabled: Option<bool>,
    /// The user's own browser, through the Hermesh extension (`browser.extension`).
    pub extension: bool,
    /// Where pages open when a call names no browser, while both are on.
    pub default: DefaultBrowser,
    /// The built-in browser's: its backend is never `Extension`.
    pub config: her_browser::BrowserConfig,
    pub policy: std::sync::Arc<her_browser::policy::SitePolicy>,
    pub navigation_timeout: Duration,
    pub action_timeout: Duration,
}

impl BrowserSettings {
    fn from_file(file: BrowserFile) -> Result<Self, ConfigError> {
        use her_browser::policy::SiteLevel;
        let headless = match &file.headless {
            None => her_browser::Headless::Auto,
            Some(toml::Value::Boolean(true)) => her_browser::Headless::On,
            Some(toml::Value::Boolean(false)) => her_browser::Headless::Off,
            Some(toml::Value::String(text)) => match text.as_str() {
                "auto" => her_browser::Headless::Auto,
                "true" => her_browser::Headless::On,
                "false" => her_browser::Headless::Off,
                _ => return Err(ConfigError::Invalid(format!("browser.headless must be auto, true or false, got \"{text}\""))),
            },
            Some(other) => return Err(ConfigError::Invalid(format!("browser.headless must be auto, true or false, got {other}"))),
        };
        let backend = match file.backend.as_deref() {
            None | Some("auto" | "extension") => her_browser::Backend::Auto,
            Some("embedded") => her_browser::Backend::Embedded,
            Some("managed") => her_browser::Backend::Managed,
            Some(other) => {
                return Err(ConfigError::Invalid(format!(
                    "browser.backend must be auto, embedded, managed or extension, got \"{other}\""
                )));
            }
        };
        let mut sites = BTreeMap::new();
        for (origin, level) in file.sites.unwrap_or_default() {
            // Every public site: navigate or act; refusing them all is `allowed_domains`' job.
            if origin == her_browser::policy::ANY_SITE {
                let level = SiteLevel::parse(&level).filter(|l| *l != SiteLevel::Deny).ok_or_else(|| {
                    ConfigError::Invalid(format!("browser.sites.\"*\" must be navigate or act, got \"{level}\""))
                })?;
                sites.insert(origin, level);
                continue;
            }
            let normalized = her_browser::policy::normalize_origin(&origin).ok_or_else(|| {
                ConfigError::Invalid(format!(
                    "browser.sites: \"{origin}\" is not an origin such as \"https://example.com\""
                ))
            })?;
            let level = SiteLevel::parse(&level).ok_or_else(|| {
                ConfigError::Invalid(format!("browser.sites.\"{origin}\" must be deny, navigate or act, got \"{level}\""))
            })?;
            sites.insert(normalized, level);
        }
        let profile = file.profile.unwrap_or_else(|| "default".into());
        if profile.is_empty() || profile.contains(['/', '\\']) || profile.starts_with('.') {
            return Err(ConfigError::Invalid(format!("browser.profile \"{profile}\" must be a plain name")));
        }
        let default = match file.default.as_deref() {
            None | Some("builtin") => DefaultBrowser::Builtin,
            Some("yours") => DefaultBrowser::Yours,
            Some(other) => return Err(ConfigError::Invalid(format!("browser.default must be builtin or yours, got \"{other}\""))),
        };
        Ok(Self {
            enabled: file.enabled,
            extension: file.extension.unwrap_or(file.backend.as_deref() == Some("extension")),
            default,
            config: her_browser::BrowserConfig {
                backend,
                executable: file.executable.filter(|p| !p.as_os_str().is_empty()),
                profile,
                headless,
            },
            policy: std::sync::Arc::new(her_browser::policy::SitePolicy {
                allowed_domains: file.allowed_domains.unwrap_or_default(),
                denied_domains: file.denied_domains.unwrap_or_default(),
                sites,
                // On: each script still asks before it runs (browser-design §7.6).
                script_enabled: file.script_enabled.unwrap_or(true),
            }),
            navigation_timeout: Duration::from_secs(file.navigation_timeout_secs.unwrap_or(30).clamp(1, 600)),
            action_timeout: Duration::from_secs(file.action_timeout_secs.unwrap_or(10).clamp(1, 600)),
        })
    }

    /// Whether Hermesh controls browsers, given whether a desktop app shows them.
    pub fn control_on(&self, embedded: bool) -> bool {
        self.enabled.unwrap_or(embedded || self.extension)
    }

    /// Whether the built-in browser is on: with browser control, where a desktop app shows it
    /// or the user asked for it in so many words (the command line starts a browser of its own).
    pub fn builtin_on(&self, embedded: bool) -> bool {
        self.control_on(embedded) && (embedded || self.enabled == Some(true))
    }

    /// Whether the user's own browser is on: with browser control, once connected.
    pub fn yours_on(&self, embedded: bool) -> bool {
        self.control_on(embedded) && self.extension
    }

    /// Whether some browser is turned on in so many words: what a process without a desktop
    /// app needs a browser pool for.
    pub fn configured_on(&self) -> bool {
        self.enabled == Some(true) || (self.enabled != Some(false) && self.extension)
    }
}

/// `[desktop]`: the desktop app tools, through the agent-desktop CLI.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct DesktopFile {
    pub enabled: Option<bool>,
    /// The agent-desktop program; by default `~/.her/desktop/bin`, then `PATH`.
    pub executable: Option<PathBuf>,
    /// Bundle id (or name) → `deny`, `read` or `act`.
    pub apps: Option<BTreeMap<String, String>>,
}

/// What `[desktop.apps]` allows for an app without asking.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum AppLevel {
    Deny,
    Read,
    Act,
}

/// `[desktop]` with its defaults applied.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct DesktopSettings {
    /// Computer use. Not set: on while the desktop app is connected, as the built-in browser is
    /// (docs/desktop/computer-use-design.md); the user's own switch otherwise.
    pub enabled: Option<bool>,
    pub executable: Option<PathBuf>,
    /// Keyed by lowercased bundle id or name.
    pub apps: std::sync::Arc<BTreeMap<String, AppLevel>>,
}

impl DesktopSettings {
    fn from_file(file: DesktopFile) -> Result<Self, ConfigError> {
        let mut apps = BTreeMap::new();
        for (app, level) in file.apps.unwrap_or_default() {
            let level = match level.as_str() {
                "deny" => AppLevel::Deny,
                // Two levels now, allow and deny (computer-use-design §4); `read` and `act`
                // from before keep their meaning.
                "allow" | "act" => AppLevel::Act,
                "read" => AppLevel::Read,
                _ => {
                    return Err(ConfigError::Invalid(format!(
                        "desktop.apps.\"{app}\" must be allow or deny, got \"{level}\""
                    )));
                }
            };
            apps.insert(app.trim().to_lowercase(), level);
        }
        Ok(Self {
            enabled: file.enabled,
            executable: file.executable.filter(|p| !p.as_os_str().is_empty()),
            apps: std::sync::Arc::new(apps),
        })
    }

    /// Whether the computer use tools are on, `app` saying whether the desktop app is connected.
    pub fn on(&self, app: bool) -> bool {
        self.enabled.unwrap_or(app)
    }

    /// The configured level of `app`, by bundle id or name.
    pub fn app(&self, app: &str) -> Option<AppLevel> {
        self.apps.get(&app.trim().to_lowercase()).copied()
    }
}

/// `[web_search]` (docs/kernel/web-search-design.md §6.3).
#[derive(Debug, Clone, Default, Deserialize)]
pub struct WebSearchFile {
    pub enabled: Option<bool>,
    /// Service ids, the first tried first; the ones not named follow in the default order.
    pub order: Option<Vec<String>>,
    /// Services switched off.
    pub disabled: Option<Vec<String>>,
    /// Service id → address, instead of its own; only for tests and self-built copies.
    pub endpoints: Option<BTreeMap<String, String>>,
}

/// `[web_search]` with its defaults applied.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct WebSearchSettings {
    pub enabled: bool,
    pub order: Vec<String>,
    pub disabled: Vec<String>,
    pub endpoints: BTreeMap<String, String>,
}

impl Default for WebSearchSettings {
    fn default() -> Self {
        Self::from_file(WebSearchFile::default())
    }
}

impl WebSearchSettings {
    fn from_file(file: WebSearchFile) -> Self {
        Self {
            enabled: file.enabled.unwrap_or(true),
            order: file.order.unwrap_or_default(),
            disabled: file.disabled.unwrap_or_default(),
            endpoints: file.endpoints.unwrap_or_default(),
        }
    }

    pub fn is_disabled(&self, id: &str) -> bool {
        self.disabled.iter().any(|d| d == id)
    }
}

/// `[models]`: model lists and the public catalog (docs/kernel/model-catalog-design.md §11).
#[derive(Debug, Clone, Default, Deserialize)]
pub struct ModelsFile {
    /// `false`: no background refresh of the catalog and the lists.
    pub network: Option<bool>,
}

/// `[agents]`: sub-agents (`spawn_agent`, `send_message`).
#[derive(Debug, Clone, Default, Deserialize)]
pub struct AgentsFile {
    pub enabled: Option<bool>,
}

/// `[skills]` (docs/kernel/skill-design.md §10).
#[derive(Debug, Clone, Default, Deserialize)]
pub struct SkillsFile {
    pub enabled: Option<bool>,
    /// Removed in 2e (design D-176); still read so that the error can say what replaced it.
    pub dirs: Option<toml::Value>,
    pub other_apps: Option<bool>,
    pub paths: Option<Vec<String>>,
    pub opt_in: Option<Vec<String>>,
    pub disabled: Option<Vec<String>>,
    pub max_catalog_tokens: Option<u64>,
}

#[derive(Debug, Clone)]
pub struct SkillsSettings {
    /// Look for skills and list them in the system prompt.
    pub enabled: bool,
    /// Also read `~/.agents/skills` and `~/.claude/skills`, the folders other apps install
    /// into (design D-171).
    pub other_apps: bool,
    /// Further skill roots, skill directories or `SKILL.md` files, after the user layer.
    /// `~/` is the home directory; other relative paths are relative to the working
    /// directory, except in a project's own file, where they are relative to the project.
    pub paths: Vec<String>,
    /// Paths of skill directories or `SKILL.md` files that are off by default and turned on.
    pub opt_in: Vec<String>,
    /// Skill names, or paths of skill directories or `SKILL.md` files.
    pub disabled: Vec<String>,
    /// Catalog budget in tokens; 0 for 2% of the context window, at most 10,000.
    pub max_catalog_tokens: u64,
}

impl Default for SkillsSettings {
    fn default() -> Self {
        Self {
            enabled: true,
            other_apps: false,
            paths: Vec::new(),
            opt_in: Vec::new(),
            disabled: Vec::new(),
            max_catalog_tokens: 0,
        }
    }
}

/// `[server]`: the protocol service (`her serve`, `her acp`).
#[derive(Debug, Clone, Default, Deserialize)]
pub struct ServerFile {
    pub max_frame_bytes: Option<u64>,
    pub queue_hard_bytes: Option<u64>,
    pub max_sessions: Option<usize>,
    pub idle_session_secs: Option<u64>,
    pub history_limit: Option<u32>,
}

/// `[acp]`: the ACP projection.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct AcpFile {
    pub progress_interval_ms: Option<u64>,
    pub progress_tail_bytes: Option<usize>,
    pub delegate_fs: Option<bool>,
}

#[derive(Debug, Clone)]
pub struct ServerSettings {
    pub max_frame_bytes: usize,
    pub queue_hard_bytes: usize,
    pub max_sessions: usize,
    /// 0 keeps idle sessions loaded.
    pub idle_session_secs: u64,
    pub history_limit: u32,
}

#[derive(Debug, Clone)]
pub struct AcpSettings {
    pub progress_interval: Duration,
    pub progress_tail_bytes: usize,
    pub delegate_fs: bool,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct InstructionsFile {
    pub files: Option<Vec<String>>,
    pub extra: Option<String>,
    pub project_root_markers: Option<Vec<String>>,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct ToolsFile {
    pub enabled: Option<Vec<String>>,
    pub bash: Option<BashFile>,
    pub limits: Option<LimitsFile>,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct BashFile {
    pub default_timeout_secs: Option<u64>,
    pub max_timeout_secs: Option<u64>,
    pub shell: Option<String>,
    pub pass_provider_keys: Option<bool>,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct LimitsFile {
    pub max_lines: Option<usize>,
    pub max_tokens: Option<usize>,
    /// The limit from before it was counted in tokens: loading turns it into `max_tokens`
    /// (`migrate_legacy`), so it is never set here.
    pub max_bytes: Option<usize>,
    pub max_saved_bytes: Option<u64>,
    pub max_parallel: Option<usize>,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct CompactionFile {
    pub enabled: Option<bool>,
    pub threshold: Option<f64>,
    pub token_limit: Option<u64>,
    pub keep_user_tokens: Option<u64>,
    pub keep_skill_tokens: Option<u64>,
}

#[derive(Debug, Clone, Default, Deserialize)]
pub struct LlmFile {
    pub max_retries: Option<u32>,
    pub retry_window_secs: Option<u64>,
    pub connect_timeout_secs: Option<u64>,
    pub idle_timeout_secs: Option<u64>,
}

#[derive(Debug, Clone)]
pub struct CompactionSettings {
    /// Compact automatically; manual compaction works either way.
    pub enabled: bool,
    /// Soft limit as a share of the usable window (the window minus the reply's room).
    pub threshold: f64,
    /// Absolute cap on the soft limit, 0 for none.
    pub token_limit: u64,
    /// Budget for the user messages kept word for word, before the 15% cap.
    pub keep_user_tokens: u64,
    /// Budget for the instructions of skills in use, before the 10% cap.
    pub keep_skill_tokens: u64,
}

#[derive(Debug, Clone)]
pub struct McpSettings {
    pub enabled: bool,
    pub project_json: bool,
    pub startup_wait: Duration,
    pub exposure: McpExposure,
    /// Where sessions record each server's last connection (`~/.her/mcp/cache`); set by
    /// `Workspace::open`, not by a configuration file.
    pub cache_dir: Option<PathBuf>,
    /// her's home, for OAuth's refresh lock and client registrations; set like `cache_dir`.
    pub her_home: Option<PathBuf>,
    /// The `client_id` URL of her's client metadata document (mcp-design §13.8.3).
    pub client_metadata_url: Option<String>,
}

/// Resolved configuration with every default applied.
#[derive(Debug, Clone)]
pub struct Config {
    /// The `model` the configuration names. Without one, `models::default_model` decides, and no
    /// model is assumed when nothing is set up.
    pub model: Option<String>,
    pub thinking: ThinkingLevel,
    pub permission_mode: PermissionMode,
    pub max_turns: u32,
    pub extra_writable_roots: Vec<PathBuf>,
    pub system_prompt_file: Option<PathBuf>,
    /// Replaces the base section of bots' system prompts (bot-design §2.3).
    pub bot_prompt_file: Option<PathBuf>,
    pub instruction_files: Vec<String>,
    pub instructions_extra: String,
    pub project_root_markers: Vec<String>,
    pub enabled_tools: Vec<String>,
    /// `ui.show_thinking`
    pub show_thinking: bool,
    /// `history.enabled`
    pub history_enabled: bool,
    /// `update.check`
    pub update_check: bool,
    pub bash_default_timeout: Duration,
    pub bash_max_timeout: Duration,
    pub bash_shell: Option<String>,
    pub pass_provider_keys: bool,
    pub limits: OutputLimits,
    pub max_parallel_tools: usize,
    pub compaction: CompactionSettings,
    /// At most this many retries of a model request; `None` leaves it to `llm_retry_window`.
    pub llm_max_retries: Option<u32>,
    /// How long a model request keeps retrying after it first failed.
    pub llm_retry_window: Duration,
    pub llm_connect_timeout: Duration,
    pub llm_idle_timeout: Duration,
    pub rules: Vec<CommandRule>,
    pub server: ServerSettings,
    pub acp: AcpSettings,
    /// Whether the main agent gets the sub-agent tools.
    pub agents_enabled: bool,
    pub skills: SkillsSettings,
    pub mcp: McpSettings,
    pub browser: BrowserSettings,
    pub desktop: DesktopSettings,
    pub web_search: WebSearchSettings,
    /// Refresh model lists and the public catalog in the background (`[models] network`).
    pub models_network: bool,
    /// The configured servers by name, names filled in; not yet validated.
    pub mcp_servers: Vec<McpServerConfig>,
}

impl Default for Config {
    fn default() -> Self {
        Self::from_file(ConfigFile::default()).expect("defaults are valid")
    }
}

impl Config {
    pub fn from_file(file: ConfigFile) -> Result<Self, ConfigError> {
        let instructions = file.instructions.unwrap_or_default();
        let tools = file.tools.unwrap_or_default();
        let bash = tools.bash.unwrap_or_default();
        let limits_file = tools.limits.unwrap_or_default();
        let compaction = file.compaction.unwrap_or_default();
        let llm = file.llm.unwrap_or_default();
        let server = file.server.unwrap_or_default();
        let acp = file.acp.unwrap_or_default();
        let skills_file = file.skills.unwrap_or_default();
        let mut skills = SkillsSettings::default();
        if skills_file.dirs.is_some() {
            return Err(ConfigError::Invalid(
                "skills.dirs was removed: project skills live in .agents/skills, and .claude/skills is listed \
                 off by default; set skills.other_apps for ~/.agents and ~/.claude"
                    .into(),
            ));
        }
        let opt_in = skills_file.opt_in.unwrap_or_default();
        if let Some(name) = opt_in.iter().find(|item| !crate::skills::is_path(item)) {
            return Err(ConfigError::Invalid(format!(
                "skills.opt_in takes paths of skills, got the name \"{name}\""
            )));
        }
        skills.enabled = skills_file.enabled.unwrap_or(skills.enabled);
        skills.other_apps = skills_file.other_apps.unwrap_or(skills.other_apps);
        skills.paths = skills_file.paths.unwrap_or_default();
        skills.opt_in = opt_in;
        skills.disabled = skills_file.disabled.unwrap_or_default();
        skills.max_catalog_tokens = skills_file.max_catalog_tokens.unwrap_or(0);
        let mcp = file.mcp.unwrap_or_default();
        let browser = BrowserSettings::from_file(file.browser.unwrap_or_default())?;
        let desktop = DesktopSettings::from_file(file.desktop.unwrap_or_default())?;
        let web_search = WebSearchSettings::from_file(file.web_search.unwrap_or_default());
        let threshold = compaction.threshold.unwrap_or(0.9);
        if !(0.1..=1.0).contains(&threshold) {
            return Err(ConfigError::Invalid(format!(
                "compaction.threshold must be between 0.1 and 1.0, got {threshold}"
            )));
        }
        let mut limits = OutputLimits::default();
        if let Some(v) = limits_file.max_lines {
            limits.max_lines = v.max(1);
        }
        if let Some(v) = limits_file.max_tokens {
            limits = limits.with_max_tokens(v);
        }
        if let Some(v) = limits_file.max_saved_bytes {
            limits.max_saved_bytes = v.max(1024);
        }
        let bash_max = Duration::from_secs(bash.max_timeout_secs.unwrap_or(600).max(1));
        Ok(Self {
            model: file.model,
            thinking: file.thinking.unwrap_or_default(),
            permission_mode: file.permission_mode.unwrap_or_default(),
            max_turns: file.max_turns.unwrap_or(0),
            extra_writable_roots: file.extra_writable_roots.unwrap_or_default(),
            system_prompt_file: file.system_prompt_file,
            bot_prompt_file: file.bot_prompt_file,
            instruction_files: instructions.files.unwrap_or_else(|| vec!["AGENTS.md".to_string()]),
            instructions_extra: instructions.extra.unwrap_or_default(),
            project_root_markers: instructions
                .project_root_markers
                .unwrap_or_else(|| vec![".git".to_string()]),
            show_thinking: file.ui.as_ref().and_then(|ui| ui.show_thinking).unwrap_or(false),
            history_enabled: file.history.as_ref().and_then(|history| history.enabled).unwrap_or(true),
            update_check: file.update.as_ref().and_then(|update| update.check).unwrap_or(true),
            enabled_tools: tools
                .enabled
                .unwrap_or_else(|| her_tools::DEFAULT_TOOLS.iter().map(|s| s.to_string()).collect()),
            bash_default_timeout: Duration::from_secs(bash.default_timeout_secs.unwrap_or(120).max(1)).min(bash_max),
            bash_max_timeout: bash_max,
            bash_shell: bash.shell,
            pass_provider_keys: bash.pass_provider_keys.unwrap_or(false),
            limits,
            max_parallel_tools: limits_file.max_parallel.unwrap_or(8).max(1),
            compaction: CompactionSettings {
                enabled: compaction.enabled.unwrap_or(true),
                threshold,
                token_limit: compaction.token_limit.unwrap_or(0),
                keep_user_tokens: compaction.keep_user_tokens.unwrap_or(20_000),
                keep_skill_tokens: compaction.keep_skill_tokens.unwrap_or(20_000),
            },
            llm_max_retries: llm.max_retries,
            llm_retry_window: Duration::from_secs(llm.retry_window_secs.unwrap_or(600)),
            llm_connect_timeout: Duration::from_secs(llm.connect_timeout_secs.unwrap_or(10).max(1)),
            llm_idle_timeout: Duration::from_secs(llm.idle_timeout_secs.unwrap_or(300).max(5)),
            rules: file.rules.unwrap_or_default(),
            server: ServerSettings {
                // What clients legitimately send, and what the figure rests on: protocol-design §4.4.
                max_frame_bytes: server.max_frame_bytes.unwrap_or(128 << 20).max(64 << 10) as usize,
                queue_hard_bytes: server.queue_hard_bytes.unwrap_or(16 << 20).max(64 << 10) as usize,
                max_sessions: server.max_sessions.unwrap_or(1000).max(1),
                idle_session_secs: server.idle_session_secs.unwrap_or(900),
                history_limit: server.history_limit.unwrap_or(100).clamp(1, 1000),
            },
            acp: AcpSettings {
                progress_interval: Duration::from_millis(acp.progress_interval_ms.unwrap_or(100)),
                progress_tail_bytes: acp.progress_tail_bytes.unwrap_or(16 << 10).max(256),
                delegate_fs: acp.delegate_fs.unwrap_or(false),
            },
            agents_enabled: file.agents.and_then(|a| a.enabled).unwrap_or(true),
            skills,
            mcp: McpSettings {
                enabled: mcp.enabled.unwrap_or(true),
                project_json: mcp.project_json.unwrap_or(false),
                startup_wait: Duration::from_secs(mcp.startup_wait_secs.unwrap_or(10)),
                exposure: mcp.exposure.unwrap_or_default(),
                cache_dir: None,
                her_home: None,
                client_metadata_url: mcp.client_metadata_url.filter(|u| !u.trim().is_empty()).or_else(|| crate::mcp::CLIENT_METADATA_URL.map(String::from)),
            },
            browser,
            desktop,
            web_search,
            models_network: file.models.and_then(|m| m.network).unwrap_or(true),
            mcp_servers: file
                .mcp_servers
                .unwrap_or_default()
                .into_iter()
                .map(|(name, mut server)| {
                    server.name = name;
                    server
                })
                .collect(),
        })
    }
}

#[derive(Debug, thiserror::Error)]
pub enum ConfigError {
    #[error("failed to read {path}: {source}")]
    Io {
        path: PathBuf,
        #[source]
        source: io::Error,
    },
    #[error("invalid config {path}: {message}")]
    Parse { path: String, message: String },
    #[error("invalid config: {0}")]
    Invalid(String),
}

pub struct LoadedConfig {
    pub config: Config,
    pub warnings: Vec<String>,
    pub sources: Vec<PathBuf>,
    /// The layers that define each MCP server, in order: "user config", "project config",
    /// "personal project config", "-c".
    pub mcp_sources: BTreeMap<String, Vec<String>>,
    /// The layer that last set each leaf, by dotted key path (`config/read`).
    pub origins: BTreeMap<String, ConfigLayer>,
    /// The merged table, before defaults were applied.
    pub merged: toml::Table,
}

/// The files of the layers. Missing files are skipped.
#[derive(Clone, Copy, Debug)]
pub struct LayerFiles<'a> {
    pub user: &'a Path,
    /// `<project>/.her/config.toml`
    pub project: Option<&'a Path>,
    /// `~/.her/projects/<key>/config.toml`: the user's own settings for the project, above
    /// the project's file (docs/desktop/customize-design.md §4.3).
    pub user_project: Option<&'a Path>,
    /// What relative skill paths in the two project files are relative to.
    pub project_root: Option<&'a Path>,
}

/// Loads and merges the configuration layers. Settings that loosen security are dropped
/// from an untrusted project's file.
pub fn load(
    user_file: &Path,
    project_file: Option<&Path>,
    project_trusted: bool,
    overrides: &[String],
) -> Result<LoadedConfig, ConfigError> {
    let project_root = project_file.and_then(Path::parent).and_then(Path::parent);
    let files = LayerFiles { user: user_file, project: project_file, user_project: None, project_root };
    load_layers(&files, project_trusted, overrides)
}

/// [`load`] with every layer, the user's own file for the project included.
pub fn load_layers(files: &LayerFiles<'_>, project_trusted: bool, overrides: &[String]) -> Result<LoadedConfig, ConfigError> {
    load_layers_with(files, project_trusted, overrides, None)
}

/// [`load_layers`] with one layer's file replaced by `substitute`: what the configuration
/// would be after a write (docs/desktop/customize-design.md §5.1). The table must already
/// have passed [`check_file`].
pub fn load_layers_with(
    files: &LayerFiles<'_>,
    project_trusted: bool,
    overrides: &[String],
    substitute: Option<(ConfigLayer, toml::Table)>,
) -> Result<LoadedConfig, ConfigError> {
    let mut substitute = substitute;
    let mut warnings = Vec::new();
    let mut layer_table = |layer: ConfigLayer, path: Option<&Path>| -> Result<Option<toml::Table>, ConfigError> {
        if let Some((replaced, _)) = &substitute
            && *replaced == layer
        {
            return Ok(substitute.take().map(|(_, table)| table));
        }
        match path {
            Some(path) if path.exists() => read_table(path).map(Some),
            _ => Ok(None),
        }
    };
    let mut merged = toml::Table::new();
    let mut sources = Vec::new();
    let mut mcp_sources = BTreeMap::new();
    let mut origins = BTreeMap::new();
    let user_file = files.user;
    if let Some(mut table) = layer_table(ConfigLayer::User, Some(user_file))? {
        migrate_legacy(&mut table, &user_file.display().to_string(), &mut warnings);
        note_mcp_servers(&table, "user config", &mut mcp_sources);
        note_origins(&table, "", ConfigLayer::User, &mut origins);
        merge_tables(&mut merged, table, "");
        sources.push(user_file.to_path_buf());
    }
    if let Some(project_file) = files.project
        && let Some(mut table) = layer_table(ConfigLayer::Project, Some(project_file))?
    {
        // Where models come from is the user's to say, trusted project or not.
        if table.remove("models").is_some() {
            warnings.push(format!("ignored [models] in {}: it can only be set in the user config", project_file.display()));
        }
        if !project_trusted {
            let removed = strip_untrusted(&mut table);
            if !removed.is_empty() {
                warnings.push(format!(
                    "ignored {} in {} because the project is not trusted (run `her trust` to allow them)",
                    removed.join(", "),
                    project_file.display()
                ));
            }
        }
        migrate_legacy(&mut table, &project_file.display().to_string(), &mut warnings);
        if let Some(project_root) = files.project_root {
            anchor_skill_paths(&mut table, project_root);
        }
        let loosened = tighten_browser(&mut table, &merged, project_trusted);
        if !loosened.is_empty() {
            warnings.push(format!(
                "ignored {} in {}: a project can only tighten the browser settings",
                loosened.join(", "),
                project_file.display()
            ));
        }
        let loosened = tighten_desktop(&mut table);
        if !loosened.is_empty() {
            warnings.push(format!(
                "ignored {} in {}: only your own config can turn on desktop control",
                loosened.join(", "),
                project_file.display()
            ));
        }
        let loosened = tighten_web_search(&mut table);
        if !loosened.is_empty() {
            warnings.push(format!(
                "ignored {} in {}: a project can only turn web search off",
                loosened.join(", "),
                project_file.display()
            ));
        }
        strip_mcp_approval(&mut table, project_file, &mut warnings);
        note_mcp_servers(&table, "project config", &mut mcp_sources);
        note_origins(&table, "", ConfigLayer::Project, &mut origins);
        merge_tables(&mut merged, table, "");
        sources.push(project_file.to_path_buf());
    }
    // The user's own file: trusted whatever the project, since only they write it.
    if let Some(user_project) = files.user_project
        && let Some(mut table) = layer_table(ConfigLayer::UserProject, Some(user_project))?
    {
        migrate_legacy(&mut table, &user_project.display().to_string(), &mut warnings);
        if let Some(project_root) = files.project_root {
            anchor_skill_paths(&mut table, project_root);
        }
        strip_mcp_approval(&mut table, user_project, &mut warnings);
        note_mcp_servers(&table, "personal project config", &mut mcp_sources);
        note_origins(&table, "", ConfigLayer::UserProject, &mut origins);
        merge_tables(&mut merged, table, "");
        sources.push(user_project.to_path_buf());
    }
    for item in overrides {
        let mut table = parse_override(item)?;
        migrate_legacy(&mut table, &format!("-c {item}"), &mut warnings);
        note_mcp_servers(&table, "-c", &mut mcp_sources);
        note_origins(&table, "", ConfigLayer::Flag, &mut origins);
        merge_tables(&mut merged, table, "");
    }
    let kept = merged.clone();
    let mut unknown = Vec::new();
    let file: ConfigFile = serde_ignored::deserialize(toml::Value::Table(merged), |path| unknown.push(dotted(&path))).map_err(|e: toml::de::Error| {
        // Only now is it worth going through the files, to name the one that holds the mistake.
        let layers = [Some(files.user), files.project, files.user_project];
        let culprit = layers.into_iter().flatten().find_map(|path| {
            let text = std::fs::read_to_string(path).ok()?;
            let err = unknown_keys(&text).err()?;
            Some((path.display().to_string(), err.to_string()))
        });
        let (path, message) = culprit.unwrap_or_else(|| ("merged configuration".to_string(), e.to_string()));
        ConfigError::Parse { path, message }
    })?;
    // Typed on this command line, not found in a file another version may have written.
    if let Some(key) = unknown.iter().find(|key| origins.get(*key) == Some(&ConfigLayer::Flag)) {
        return Err(ConfigError::Invalid(unknown_key_message(key)));
    }
    // Rare, so the files are read again for the line numbers only now.
    for key in &unknown {
        let file = match origins.get(key) {
            Some(ConfigLayer::User) => Some(files.user),
            Some(ConfigLayer::Project) => files.project,
            Some(ConfigLayer::UserProject) => files.user_project,
            _ => None,
        };
        let place = file.map_or(String::new(), |file| {
            let line = std::fs::read_to_string(file).map_or(String::new(), |text| line_of(&text, key));
            format!(" in {}{line}", file.display())
        });
        warnings.push(format!("ignored {}{place}", unknown_key_message(key)));
    }
    Ok(LoadedConfig {
        config: Config::from_file(file)?,
        warnings,
        sources,
        mcp_sources,
        origins,
        merged: kept,
    })
}

/// Records `layer` as the origin of every leaf of `table`; tables are walked, arrays and
/// values are leaves.
fn note_origins(table: &toml::Table, prefix: &str, layer: ConfigLayer, origins: &mut BTreeMap<String, ConfigLayer>) {
    for (key, value) in table {
        let path = join_key(prefix, key);
        match value {
            toml::Value::Table(inner) if !inner.is_empty() => note_origins(inner, &path, layer, origins),
            _ => {
                origins.insert(path, layer);
            }
        }
    }
}

/// `prefix.key`, the key quoted when it is not a bare TOML key.
pub fn join_key(prefix: &str, key: &str) -> String {
    let bare = !key.is_empty() && key.chars().all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '-');
    let key = if bare { key.to_string() } else { toml::Value::String(key.to_string()).to_string() };
    if prefix.is_empty() { key } else { format!("{prefix}.{key}") }
}

/// Records that `layer` defines (part of) each MCP server in `table`.
fn note_mcp_servers(table: &toml::Table, layer: &str, sources: &mut BTreeMap<String, Vec<String>>) {
    if let Some(toml::Value::Table(servers)) = table.get("mcp_servers") {
        for name in servers.keys() {
            let layers = sources.entry(name.clone()).or_default();
            if !layers.iter().any(|l| l == layer) {
                layers.push(layer.to_string());
            }
        }
    }
}

/// `tools.bash.x`; serde_ignored also has a step for each optional value.
fn dotted(path: &serde_ignored::Path) -> String {
    path.to_string().replace(".?", "")
}

/// The keys of `text` the schema does not know, by dotted path; an error for what it cannot read.
fn unknown_keys(text: &str) -> Result<Vec<String>, toml::de::Error> {
    let mut unknown = Vec::new();
    let deserializer = toml::Deserializer::parse(text)?;
    serde_ignored::deserialize::<_, _, ConfigFile>(deserializer, |path| unknown.push(dotted(&path)))?;
    Ok(unknown)
}

/// Checks one file's text against the schema and parses it. Unknown keys are errors: whoever writes
/// one through `config/write` or `her config set` made a mistake.
pub fn check_file(text: &str, path: &Path) -> Result<toml::Table, ConfigError> {
    let parse_error = |e: toml::de::Error| ConfigError::Parse {
        path: path.display().to_string(),
        message: e.to_string(),
    };
    let unknown = unknown_keys(text).map_err(parse_error)?;
    if let Some(key) = unknown.first() {
        return Err(ConfigError::Parse { path: path.display().to_string(), message: unknown_key_message(key) });
    }
    toml::from_str::<toml::Table>(text).map_err(parse_error)
}

/// Reads one layer's file. A key this version does not know is a warning, not a failure: a newer
/// her or Hermesh may have written it, and every command, `her doctor` included, should still
/// run. A value it cannot read stays an error, and so does anything unknown in `rules`: a safety
/// rule must not quietly go missing.
fn read_table(path: &Path) -> Result<toml::Table, ConfigError> {
    let text = std::fs::read_to_string(path).map_err(|source| ConfigError::Io {
        path: path.to_path_buf(),
        source,
    })?;
    let table = toml::from_str::<toml::Table>(&text).map_err(|e| ConfigError::Parse {
        path: path.display().to_string(),
        message: e.to_string(),
    })?;
    // One name would quietly replace the other once the old one is migrated.
    if table.contains_key("permission_mode") && table.contains_key("approval_mode") {
        return Err(ConfigError::Parse {
            path: path.display().to_string(),
            message: "permission_mode and approval_mode are the same setting: keep permission_mode".to_string(),
        });
    }
    Ok(table)
}

/// `:12` when the key's line can be found.
fn line_of(text: &str, key: &str) -> String {
    let last = key.rsplit('.').next().unwrap_or(key);
    let found = text.lines().position(|line| {
        let line = line.trim_start();
        line.strip_prefix(last).is_some_and(|rest| rest.trim_start().starts_with('='))
            || line.trim_start_matches('[').strip_prefix(key).is_some_and(|rest| rest.starts_with(']'))
    });
    found.map_or(String::new(), |index| format!(":{}", index + 1))
}

/// Names the fields of a struct: its generated `Deserialize` passes them to the deserializer.
struct FieldNames<'a>(&'a std::cell::Cell<&'static [&'static str]>);

impl<'de> serde::Deserializer<'de> for FieldNames<'_> {
    type Error = serde::de::value::Error;

    fn deserialize_any<V: serde::de::Visitor<'de>>(self, _visitor: V) -> Result<V::Value, Self::Error> {
        Err(serde::de::Error::custom("only asked for the fields"))
    }

    fn deserialize_struct<V: serde::de::Visitor<'de>>(
        self,
        _name: &'static str,
        fields: &'static [&'static str],
        _visitor: V,
    ) -> Result<V::Value, Self::Error> {
        self.0.set(fields);
        Err(serde::de::Error::custom("only asked for the fields"))
    }

    serde::forward_to_deserialize_any! {
        bool i8 i16 i32 i64 i128 u8 u16 u32 u64 u128 f32 f64 char str string bytes byte_buf option
        unit unit_struct newtype_struct seq tuple tuple_struct map enum identifier ignored_any
    }
}

fn fields_of<'de, T: Deserialize<'de>>() -> &'static [&'static str] {
    let fields = std::cell::Cell::new(&[][..]);
    let _ = T::deserialize(FieldNames(&fields));
    fields.get()
}

/// The keys a table of the configuration can hold, by the table's path.
fn known_keys(table: &str) -> &'static [&'static str] {
    match table {
        "" => fields_of::<ConfigFile>(),
        "mcp" => fields_of::<McpFile>(),
        "browser" => fields_of::<BrowserFile>(),
        "desktop" => fields_of::<DesktopFile>(),
        "web_search" => fields_of::<WebSearchFile>(),
        "models" => fields_of::<ModelsFile>(),
        "ui" => fields_of::<UiFile>(),
        "history" => fields_of::<HistoryFile>(),
        "update" => fields_of::<UpdateFile>(),
        "agents" => fields_of::<AgentsFile>(),
        "skills" => fields_of::<SkillsFile>(),
        "server" => fields_of::<ServerFile>(),
        "acp" => fields_of::<AcpFile>(),
        "instructions" => fields_of::<InstructionsFile>(),
        "tools" => fields_of::<ToolsFile>(),
        "tools.bash" => fields_of::<BashFile>(),
        "tools.limits" => fields_of::<LimitsFile>(),
        "compaction" => fields_of::<CompactionFile>(),
        "llm" => fields_of::<LlmFile>(),
        _ if table.starts_with("mcp_servers.") => fields_of::<McpServerConfig>(),
        _ => &[],
    }
}

/// "unknown setting `tool`; did you mean `tools`?"
/// What is wrong with a key someone typed: nothing for one her knows, or under a table whose keys
/// are not fixed.
pub fn key_problem(key: &str) -> Option<String> {
    let (table, last) = key.rsplit_once('.').unwrap_or(("", key));
    let known = known_keys(table);
    (!known.is_empty() && !known.contains(&last)).then(|| unknown_key_message(key))
}

fn unknown_key_message(key: &str) -> String {
    let (table, last) = key.rsplit_once('.').unwrap_or(("", key));
    let closest = known_keys(table)
        .iter()
        .map(|known| (strsim::damerau_levenshtein(known, last), *known))
        .filter(|(distance, _)| *distance <= 2)
        .min();
    match closest {
        Some((_, known)) if table.is_empty() => format!("unknown setting `{key}` (did you mean `{known}`?)"),
        Some((_, known)) => format!("unknown setting `{key}` (did you mean `{table}.{known}`?)"),
        None => format!("unknown setting `{key}`"),
    }
}

/// Tables merge key by key and arrays replace each other, except the lists that add up:
/// `rules`, and `skills.disabled` and `skills.opt_in` so that a project's list cannot undo
/// the user's (design §4.4).
fn merge_tables(base: &mut toml::Table, over: toml::Table, prefix: &str) {
    for (key, value) in over {
        let path = join_key(prefix, &key);
        match (base.get_mut(&key), value) {
            (Some(toml::Value::Table(existing)), toml::Value::Table(incoming)) => merge_tables(existing, incoming, &path),
            (Some(toml::Value::Array(existing)), toml::Value::Array(incoming)) if path == "rules" => {
                existing.extend(incoming)
            }
            (Some(toml::Value::Array(existing)), toml::Value::Array(incoming)) if matches!(path.as_str(), "skills.disabled" | "skills.opt_in") => {
                for item in incoming {
                    if !existing.contains(&item) {
                        existing.push(item);
                    }
                }
            }
            (_, value) => {
                base.insert(key, value);
            }
        }
    }
}

/// Merges the tables of several layers in order, as [`load_layers`] does.
pub fn merge_all(tables: impl IntoIterator<Item = toml::Table>) -> toml::Table {
    let mut merged = toml::Table::new();
    for table in tables {
        merge_tables(&mut merged, table, "");
    }
    merged
}

/// Renames settings from before the permission modes were renamed, and the byte limit of tool
/// results from before it was counted in tokens, with a warning.
fn migrate_legacy(table: &mut toml::Table, source: &str, warnings: &mut Vec<String>) {
    if let Some(value) = table.remove("approval_mode") {
        warnings.push(format!("`approval_mode` is now called `permission_mode` ({source})"));
        table.entry("permission_mode").or_insert(value);
    }
    if let Some(toml::Value::String(name)) = table.get_mut("permission_mode")
        && let Some(mode) = PermissionMode::from_legacy(name)
    {
        warnings.push(format!("permission mode '{name}' is now called '{mode}' ({source})"));
        *name = mode.to_string();
    }
    if let Some(toml::Value::Table(tools)) = table.get_mut("tools")
        && let Some(toml::Value::Table(limits)) = tools.get_mut("limits")
        && let Some(value) = limits.remove("max_bytes")
    {
        warnings.push(format!(
            "`tools.limits.max_bytes` is now `tools.limits.max_tokens`, at 4 bytes per token ({source})"
        ));
        let value = match value {
            toml::Value::Integer(bytes) => toml::Value::Integer((bytes.max(0) as u64).div_ceil(4) as i64),
            other => other,
        };
        limits.entry("max_tokens").or_insert(value);
    }
}

/// Makes relative paths in a project's `skills.paths`, `skills.opt_in` and `skills.disabled` relative to the
/// project, before the layers are merged and their origin is lost.
fn anchor_skill_paths(table: &mut toml::Table, project_root: &Path) {
    let Some(toml::Value::Table(skills)) = table.get_mut("skills") else {
        return;
    };
    for key in ["paths", "opt_in", "disabled"] {
        let Some(toml::Value::Array(items)) = skills.get_mut(key) else {
            continue;
        };
        for item in items {
            if let toml::Value::String(path) = item
                && (key != "disabled" || crate::skills::is_path(path))
                && !path.starts_with('~')
                && Path::new(path.as_str()).is_relative()
            {
                *path = project_root.join(path.as_str()).display().to_string();
            }
        }
    }
}

/// A project's `[browser]` may only tighten the user's (browser-design D-178): its domain
/// allow list narrows the user's, its deny list adds to it, and of its sites only `deny`
/// counts. It never picks the program that runs, nor turns scripts or the user's own browser
/// on; an untrusted project cannot turn the built-in browser on either. Returns what was
/// dropped.
fn tighten_browser(table: &mut toml::Table, user: &toml::Table, trusted: bool) -> Vec<String> {
    let Some(toml::Value::Table(browser)) = table.get_mut("browser") else {
        return Vec::new();
    };
    let user_browser = user.get("browser").and_then(toml::Value::as_table);
    let user_list = |key: &str| -> Vec<toml::Value> {
        user_browser
            .and_then(|b| b.get(key))
            .and_then(toml::Value::as_array)
            .cloned()
            .unwrap_or_default()
    };
    let mut dropped = Vec::new();
    for key in ["backend", "executable", "profile", "default"] {
        if browser.remove(key).is_some() {
            dropped.push(format!("browser.{key}"));
        }
    }
    if browser.get("script_enabled").and_then(toml::Value::as_bool) == Some(true) {
        browser.remove("script_enabled");
        dropped.push("browser.script_enabled".into());
    }
    if !trusted && browser.get("enabled").and_then(toml::Value::as_bool) == Some(true) {
        browser.remove("enabled");
        dropped.push("browser.enabled".into());
    }
    // The user's own browser has their real accounts: only the user turns it on.
    if browser.get("extension").and_then(toml::Value::as_bool) == Some(true) {
        browser.remove("extension");
        dropped.push("browser.extension".into());
    }
    if let Some(toml::Value::Array(allowed)) = browser.get_mut("allowed_domains") {
        let user_allowed = user_list("allowed_domains");
        if !user_allowed.is_empty() {
            let before = allowed.len();
            allowed.retain(|domain| user_allowed.contains(domain));
            if allowed.is_empty() && before > 0 {
                // Nothing in common: allow nothing rather than everything.
                allowed.push(toml::Value::String("invalid.".into()));
            }
        }
    }
    if let Some(toml::Value::Array(denied)) = browser.get_mut("denied_domains") {
        for domain in user_list("denied_domains") {
            if !denied.contains(&domain) {
                denied.push(domain);
            }
        }
    }
    if let Some(toml::Value::Table(sites)) = browser.get_mut("sites") {
        let before = sites.len();
        sites.retain(|_, level| level.as_str() == Some("deny"));
        if sites.len() != before {
            dropped.push("browser.sites other than deny".into());
        }
    }
    dropped
}

/// A project's `[desktop]` may only turn the tools off and deny apps: operating the user's apps is the
/// user's own choice, trusted project or not. Returns what was dropped.
fn tighten_desktop(table: &mut toml::Table) -> Vec<String> {
    let Some(toml::Value::Table(desktop)) = table.get_mut("desktop") else {
        return Vec::new();
    };
    let mut dropped = Vec::new();
    if desktop.remove("executable").is_some() {
        dropped.push("desktop.executable".into());
    }
    if desktop.get("enabled").and_then(toml::Value::as_bool) == Some(true) {
        desktop.remove("enabled");
        dropped.push("desktop.enabled".into());
    }
    if let Some(toml::Value::Table(apps)) = desktop.get_mut("apps") {
        let before = apps.len();
        apps.retain(|_, level| level.as_str() == Some("deny"));
        if apps.len() != before {
            dropped.push("desktop.apps other than deny".into());
        }
    }
    dropped
}

/// A project's `[web_search]` may only turn searching off: where the user's searches go, and with
/// which of their keys, is their own choice, trusted project or not. Returns what was dropped.
fn tighten_web_search(table: &mut toml::Table) -> Vec<String> {
    let Some(toml::Value::Table(search)) = table.get_mut("web_search") else {
        return Vec::new();
    };
    let mut dropped: Vec<String> = ["order", "disabled", "endpoints"]
        .into_iter()
        .filter(|key| search.remove(*key).is_some())
        .map(|key| format!("web_search.{key}"))
        .collect();
    if search.get("enabled").and_then(toml::Value::as_bool) == Some(true) {
        search.remove("enabled");
        dropped.push("web_search.enabled".into());
    }
    dropped
}

/// Trusting a server for good is for the user's own file: a repository could otherwise switch off
/// the questions about the tools it brings.
fn strip_mcp_approval(table: &mut toml::Table, file: &Path, warnings: &mut Vec<String>) {
    let Some(toml::Value::Table(servers)) = table.get_mut("mcp_servers") else { return };
    for (name, server) in servers.iter_mut() {
        if let toml::Value::Table(server) = server
            && server.remove("approval").is_some()
        {
            warnings.push(format!(
                "ignored mcp_servers.{name}.approval in {}: it counts only in ~/.her/config.toml",
                file.display()
            ));
        }
    }
}

pub(crate) fn strip_untrusted(table: &mut toml::Table) -> Vec<String> {
    let mut removed = Vec::new();
    for key in ["permission_mode", "approval_mode", "extra_writable_roots", "system_prompt_file", "bot_prompt_file"] {
        if table.remove(key).is_some() {
            removed.push(key.to_string());
        }
    }
    if let Some(toml::Value::Table(tools)) = table.get_mut("tools")
        && tools.remove("bash").is_some()
    {
        removed.push("tools.bash".to_string());
    }
    // A project may turn sub-agents off, not on.
    if let Some(toml::Value::Table(agents)) = table.get_mut("agents")
        && agents.get("enabled").and_then(toml::Value::as_bool) == Some(true)
    {
        agents.remove("enabled");
        removed.push("agents.enabled".to_string());
    }
    // A server in the project runs code on this machine as soon as a session starts.
    if let Some(toml::Value::Table(servers)) = table.remove("mcp_servers") {
        let names: Vec<String> = servers.keys().cloned().collect();
        removed.push(format!("mcp_servers ({})", names.join(", ")));
    }
    if let Some(toml::Value::Table(mcp)) = table.get_mut("mcp")
        && mcp.remove("project_json").is_some()
    {
        removed.push("mcp.project_json".to_string());
    }
    if let Some(toml::Value::Array(rules)) = table.get_mut("rules") {
        let before = rules.len();
        rules.retain(|rule| rule.get("decision").and_then(toml::Value::as_str) != Some("allow"));
        if rules.len() != before {
            removed.push("allow rules".to_string());
        }
    }
    removed
}

fn parse_override(item: &str) -> Result<toml::Table, ConfigError> {
    let (key, value) = item
        .split_once('=')
        .ok_or_else(|| ConfigError::Invalid(format!("override '{item}' must look like key=value")))?;
    let (key, value) = (key.trim(), value.trim());
    toml::from_str::<toml::Table>(&format!("{key} = {value}"))
        .or_else(|_| {
            let quoted = toml::Value::String(value.to_string()).to_string();
            toml::from_str::<toml::Table>(&format!("{key} = {quoted}"))
        })
        .map_err(|e| ConfigError::Invalid(format!("override '{item}': {e}")))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn defaults_are_sensible() {
        let config = Config::default();
        assert_eq!(config.model, None, "no model is assumed");
        assert_eq!(config.permission_mode, PermissionMode::Manual);
        assert_eq!(config.enabled_tools, vec!["read", "write", "edit", "bash"]);
        assert_eq!(config.bash_default_timeout, Duration::from_secs(120));
    }

    #[test]
    fn layers_merge_and_untrusted_projects_are_restricted() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("user.toml");
        let project = dir.path().join("project.toml");
        std::fs::write(
            &user,
            "model = \"deepseek/deepseek-v4-pro\"\n[[rules]]\nprefix = [\"git\", \"push\"]\ndecision = \"ask\"\n",
        )
        .unwrap();
        std::fs::write(
            &project,
            "thinking = \"low\"\napproval_mode = \"full-auto\"\n[tools.bash]\nshell = \"/bin/evil\"\n\
             [[rules]]\nprefix = [\"rm\"]\ndecision = \"allow\"\n[[rules]]\nprefix = [\"curl\"]\ndecision = \"deny\"\n",
        )
        .unwrap();
        let loaded = load(&user, Some(&project), false, &["max_turns=7".into(), "tools.enabled=[\"read\"]".into()]).unwrap();
        let config = loaded.config;
        assert_eq!(config.model.as_deref(), Some("deepseek/deepseek-v4-pro"));
        assert_eq!(config.thinking, ThinkingLevel::Low);
        assert_eq!(config.permission_mode, PermissionMode::Manual, "untrusted project cannot loosen approvals");
        assert_eq!(config.bash_shell, None);
        assert_eq!(config.max_turns, 7);
        assert_eq!(config.enabled_tools, vec!["read"]);
        let prefixes: Vec<String> = config.rules.iter().map(|r| r.prefix.join(" ")).collect();
        assert_eq!(prefixes, vec!["git push", "curl"]);
        assert_eq!(loaded.warnings.len(), 1);

        let trusted = load(&user, Some(&project), true, &[]).unwrap();
        assert_eq!(trusted.config.permission_mode, PermissionMode::BypassPermissions);
        assert_eq!(
            trusted.warnings,
            vec![
                format!("`approval_mode` is now called `permission_mode` ({})", project.display()),
                format!(
                    "permission mode 'full-auto' is now called 'bypass-permissions' ({})",
                    project.display()
                ),
            ]
        );
    }

    #[test]
    fn tool_result_limits_count_tokens() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("user.toml");
        std::fs::write(&user, "[tools.limits]\nmax_tokens = 4000\n").unwrap();
        let loaded = load(&user, None, false, &[]).unwrap();
        let limits = &loaded.config.limits;
        assert_eq!((limits.max_tokens, limits.head_tokens, limits.tail_tokens), (4_000, 800, 3_200));
        assert!(loaded.warnings.is_empty());

        // The byte limit from before is read as tokens, with a warning.
        std::fs::write(&user, "[tools.limits]\nmax_bytes = 51201\n").unwrap();
        let legacy = load(&user, None, false, &[]).unwrap();
        assert_eq!(legacy.config.limits.max_tokens, 12_801);
        assert_eq!(
            legacy.warnings,
            vec![format!(
                "`tools.limits.max_bytes` is now `tools.limits.max_tokens`, at 4 bytes per token ({})",
                user.display()
            )]
        );
        let flag = load(&dir.path().join("none.toml"), None, false, &["tools.limits.max_bytes=8000".into()]).unwrap();
        assert_eq!(flag.config.limits.max_tokens, 2_000);
    }

    #[test]
    fn untrusted_projects_cannot_add_mcp_servers() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("user.toml");
        let project = dir.path().join("project.toml");
        std::fs::write(&user, "[mcp_servers.github]\ncommand = \"gh-mcp\"\n").unwrap();
        std::fs::write(
            &project,
            "[mcp]\nproject_json = true\n[mcp_servers.evil]\ncommand = \"./run-me\"\n[mcp_servers.github]\nenabled = false\n",
        )
        .unwrap();
        let overrides = ["mcp_servers.docs.url=\"https://mcp.example.com/mcp\"".to_string()];
        let loaded = load(&user, Some(&project), false, &overrides).unwrap();
        let names: Vec<&str> = loaded.config.mcp_servers.iter().map(|s| s.name.as_str()).collect();
        assert_eq!(names, vec!["docs", "github"]);
        assert!(loaded.config.mcp_servers.iter().all(|s| s.is_enabled()));
        assert!(!loaded.config.mcp.project_json);
        let warning = &loaded.warnings[0];
        assert!(warning.contains("mcp_servers (evil, github)") && warning.contains("mcp.project_json"), "{warning}");
        assert_eq!(loaded.mcp_sources["github"], vec!["user config"]);
        assert_eq!(loaded.mcp_sources["docs"], vec!["-c"]);

        let trusted = load(&user, Some(&project), true, &[]).unwrap();
        let github = trusted.config.mcp_servers.iter().find(|s| s.name == "github").unwrap();
        assert!(!github.is_enabled(), "a trusted project can turn a server off");
        assert_eq!(trusted.mcp_sources["github"], vec!["user config", "project config"]);
        assert!(trusted.config.mcp.project_json);
    }

    #[test]
    fn a_project_can_only_turn_web_search_off() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("user.toml");
        let project = dir.path().join("project.toml");
        std::fs::write(&user, "[web_search]\norder = [\"tavily\"]\ndisabled = [\"exa\"]\n").unwrap();
        // A trusted project still cannot send the user's searches and keys somewhere else.
        std::fs::write(
            &project,
            "[web_search]\nenabled = true\norder = [\"exa\"]\ndisabled = [\"tavily\"]\n[web_search.endpoints]\ntavily = \"https://evil.test\"\n",
        )
        .unwrap();
        let loaded = load(&user, Some(&project), true, &[]).unwrap();
        assert_eq!(loaded.config.web_search.order, ["tavily"]);
        assert_eq!(loaded.config.web_search.disabled, ["exa"]);
        assert!(loaded.config.web_search.endpoints.is_empty());
        let warning = loaded.warnings.iter().find(|w| w.contains("web search")).unwrap();
        assert!(warning.contains("web_search.endpoints") && warning.contains("web_search.enabled"), "{warning}");

        std::fs::write(&user, "[web_search]\nenabled = true\n").unwrap();
        std::fs::write(&project, "[web_search]\nenabled = false\n").unwrap();
        assert!(!load(&user, Some(&project), false, &[]).unwrap().config.web_search.enabled);
    }

    #[test]
    fn browser_settings_and_projects_only_tighten_them() {
        use her_browser::policy::SiteLevel;
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("user.toml");
        let project = dir.path().join("project.toml");
        std::fs::write(
            &user,
            "[browser]\nenabled = true\nheadless = true\nallowed_domains = [\"github.com\", \"example.com\"]\n\
             denied_domains = [\"evil.test\"]\nbackend = \"managed\"\n[browser.sites]\n\"https://GitHub.com/\" = \"act\"\n\"http://192.168.1.10:8080\" = \"act\"\n\"*\" = \"navigate\"\n",
        )
        .unwrap();
        std::fs::write(
            &project,
            "[browser]\nexecutable = \"/tmp/evil\"\nbackend = \"embedded\"\nallowed_domains = [\"github.com\", \"other.com\"]\n\
             denied_domains = [\"ads.test\"]\n[browser.sites]\n\"https://github.com\" = \"deny\"\n\"https://bank.test\" = \"act\"\n\"*\" = \"act\"\n",
        )
        .unwrap();
        let user_only = load(&user, None, true, &[]).unwrap().config.browser;
        assert_eq!(user_only.enabled, Some(true));
        assert!(user_only.policy.script_enabled, "scripts are on unless turned off");
        assert!(!user_only.extension);
        assert_eq!(user_only.config.headless, her_browser::Headless::On);
        assert_eq!(user_only.policy.site("https://github.com"), Some(SiteLevel::Act), "origins are normalized");
        assert_eq!(user_only.policy.any_site(), Some(SiteLevel::Navigate), "every site");
        assert_eq!(user_only.navigation_timeout, Duration::from_secs(30));
        assert_eq!(user_only.config.backend, her_browser::Backend::Managed);

        let loaded = load(&user, Some(&project), true, &[]).unwrap();
        let browser = loaded.config.browser;
        assert_eq!(browser.config.executable, None, "a project never picks the program");
        assert_eq!(browser.config.backend, her_browser::Backend::Managed, "nor the backend");
        assert!(!browser.extension);
        assert_eq!(browser.policy.allowed_domains, vec!["github.com"], "allow lists intersect");
        assert_eq!(browser.policy.denied_domains, vec!["ads.test", "evil.test"], "deny lists add up");
        assert_eq!(browser.policy.site("https://github.com"), Some(SiteLevel::Deny));
        assert_eq!(browser.policy.site("https://bank.test"), None, "a project cannot allow a site");
        assert_eq!(browser.policy.any_site(), Some(SiteLevel::Navigate), "nor every site");
        assert_eq!(browser.policy.site("http://192.168.1.10:8080"), Some(SiteLevel::Act));
        assert!(loaded.warnings.iter().any(|w| w.contains("browser.executable") && w.contains("only tighten")), "{:?}", loaded.warnings);

        std::fs::write(&project, "[browser]\nenabled = true\n").unwrap();
        std::fs::write(&user, "").unwrap();
        let untrusted = load(&user, Some(&project), false, &[]).unwrap().config.browser;
        assert_eq!(untrusted.enabled, None, "untrusted projects cannot turn it on");
        assert!(!untrusted.builtin_on(false) && untrusted.builtin_on(true), "not set: on where a desktop app shows it");
        assert_eq!(load(&user, Some(&project), true, &[]).unwrap().config.browser.enabled, Some(true));
        let bad = load(&user, None, true, &["browser.sites.\"github.com\"=\"act\"".into()]);
        assert!(bad.is_err());
        assert!(load(&user, None, true, &["browser.sites.\"*\"=\"deny\"".into()]).is_err(), "every site is navigate or act");
    }

    /// The user's own browser has a switch of its own; the old `backend = "extension"` reads as
    /// it, and no project turns it on (connectors-entry-design §9.1, D-225).
    #[test]
    fn the_users_own_browser_is_the_users_choice() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("user.toml");
        let project = dir.path().join("project.toml");
        std::fs::write(&user, "[browser]\nextension = true\n").unwrap();
        let browser = load(&user, None, true, &[]).unwrap().config.browser;
        assert!(browser.extension);
        assert_eq!(browser.enabled, None);
        assert!(browser.configured_on());
        assert!(browser.yours_on(false) && !browser.builtin_on(false), "connected: the command line has it alone");
        assert!(browser.yours_on(true) && browser.builtin_on(true), "the desktop app has both");
        assert_eq!(browser.default, crate::config::DefaultBrowser::Builtin);

        std::fs::write(&user, "[browser]\nextension = true\nenabled = false\ndefault = \"yours\"\n").unwrap();
        let off = load(&user, None, true, &[]).unwrap().config.browser;
        assert!(!off.yours_on(true) && !off.builtin_on(true), "browser control off: neither");
        assert!(!off.configured_on());
        assert_eq!(off.default, crate::config::DefaultBrowser::Yours);
        assert!(load(&user, None, true, &["browser.default=\"chrome\"".into()]).is_err());

        std::fs::write(&user, "[browser]\nenabled = true\nbackend = \"extension\"\n").unwrap();
        let old = load(&user, None, true, &[]).unwrap().config.browser;
        assert!(old.extension, "the old way reads as the switch");
        assert_eq!(old.config.backend, her_browser::Backend::Auto, "the built-in browser keeps its own backend");

        std::fs::write(&user, "").unwrap();
        std::fs::write(&project, "[browser]\nextension = true\n").unwrap();
        let loaded = load(&user, Some(&project), true, &[]).unwrap();
        assert!(!loaded.config.browser.extension, "not even a trusted project turns it on");
        assert!(loaded.warnings.iter().any(|w| w.contains("browser.extension")), "{:?}", loaded.warnings);
        assert!(!loaded.config.browser.configured_on());
    }

    #[test]
    fn desktop_is_the_users_choice() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("config.toml");
        let project = dir.path().join("project.toml");
        std::fs::write(&user, "").unwrap();
        let unset = load(&user, None, true, &[]).unwrap().config.desktop;
        assert!(unset.on(true) && !unset.on(false), "on with the desktop app, off without it");

        std::fs::write(&project, "[desktop]\nenabled = true\nexecutable = \"/tmp/evil\"\n").unwrap();
        let loaded = load(&user, Some(&project), true, &[]).unwrap();
        assert_eq!(loaded.config.desktop.enabled, None, "not even a trusted project turns it on");
        assert_eq!(loaded.config.desktop.executable, None);
        assert!(loaded.warnings.iter().any(|w| w.contains("desktop.enabled")), "{:?}", loaded.warnings);

        std::fs::write(&user, "[desktop]\nenabled = true\nexecutable = \"/opt/ad\"\n").unwrap();
        std::fs::write(&project, "[desktop]\nenabled = false\n").unwrap();
        assert_eq!(load(&user, None, false, &[]).unwrap().config.desktop.enabled, Some(true));
        assert_eq!(load(&user, None, false, &[]).unwrap().config.desktop.executable, Some(PathBuf::from("/opt/ad")));
        assert!(!load(&user, Some(&project), true, &[]).unwrap().config.desktop.on(true), "a project may turn it off");

        std::fs::write(&user, "[desktop.apps]\n\"com.apple.TextEdit\" = \"act\"\n\"com.tencent.xinWeChat\" = \"read\"\n").unwrap();
        std::fs::write(&project, "[desktop.apps]\n\"com.tencent.xinWeChat\" = \"deny\"\n\"com.evil\" = \"act\"\n").unwrap();
        let desktop = load(&user, None, true, &[]).unwrap().config.desktop;
        assert_eq!(desktop.app("COM.APPLE.TEXTEDIT"), Some(AppLevel::Act), "keys ignore case");
        let desktop = load(&user, Some(&project), true, &[]).unwrap().config.desktop;
        assert_eq!(desktop.app("com.tencent.xinWeChat"), Some(AppLevel::Deny), "a project may deny an app");
        assert_eq!(desktop.app("com.evil"), None, "but not allow one");
        assert!(load(&user, None, true, &["desktop.apps.x=\"all\"".into()]).is_err());
    }

    #[test]
    fn permission_mode_names() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("config.toml");
        std::fs::write(&user, "permission_mode = \"accept-edits\"\n").unwrap();
        let loaded = load(&user, None, false, &[]).unwrap();
        assert_eq!(loaded.config.permission_mode, PermissionMode::AcceptEdits);
        assert!(loaded.warnings.is_empty());

        let legacy = load(&user, None, false, &["approval_mode=ask".into()]).unwrap();
        assert_eq!(legacy.config.permission_mode, PermissionMode::Manual);
        assert_eq!(legacy.warnings.len(), 2, "{:?}", legacy.warnings);

        std::fs::write(&user, "permission_mode = \"plan\"\n").unwrap();
        let err = load(&user, None, false, &[]).err().unwrap().to_string();
        assert!(err.contains("not available yet") && err.contains("config.toml"), "{err}");
        std::fs::write(&user, "permission_mode = \"manual\"\napproval_mode = \"ask\"\n").unwrap();
        assert!(load(&user, None, false, &[]).is_err(), "both names at once");
    }

    #[test]
    fn unknown_keys_are_warnings_with_location() {
        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("config.toml");
        std::fs::write(&user, "modle = \"x\"\n").unwrap();
        let text = load(&user, None, false, &[]).unwrap().warnings.join("\n");
        assert!(text.contains("modle"), "{text}");
        assert!(text.contains("config.toml:1"), "{text}");
    }

    #[test]
    fn skills_settings_and_project_relative_paths() {
        let defaults = Config::default().skills;
        assert!(defaults.enabled);
        assert!(!defaults.other_apps);
        assert_eq!(defaults.max_catalog_tokens, 0);

        let dir = tempfile::tempdir().unwrap();
        let user = dir.path().join("config.toml");
        std::fs::write(&user, "[skills]\npaths = [\"~/team\", \"rel\"]\nmax_catalog_tokens = 3000\n").unwrap();
        let project_root = dir.path().join("repo");
        std::fs::create_dir_all(project_root.join(".her")).unwrap();
        let project = project_root.join(".her/config.toml");
        std::fs::write(
            &project,
            "[skills]\npaths = [\"tools/skills\", \"/abs\", \"~/x\"]\ndisabled = [\"deploy\", \"vendor/skills/old\"]\n\
             opt_in = [\".claude/skills/deploy\"]\n",
        )
        .unwrap();
        let skills = load(&user, Some(&project), false, &[]).unwrap().config.skills;
        // Arrays replace each other; the project's relative paths now name the project.
        assert_eq!(
            skills.paths,
            vec![project_root.join("tools/skills").display().to_string(), "/abs".into(), "~/x".into()]
        );
        assert_eq!(
            skills.disabled,
            vec!["deploy".to_string(), project_root.join("vendor/skills/old").display().to_string()]
        );
        assert_eq!(skills.opt_in, vec![project_root.join(".claude/skills/deploy").display().to_string()]);
        assert_eq!(skills.max_catalog_tokens, 3000);
        let user_only = load(&user, None, false, &["skills.enabled=false".into()]).unwrap().config.skills;
        assert_eq!(user_only.paths, vec!["~/team", "rel"], "left for the working directory");
        assert!(!user_only.enabled);

        std::fs::write(&user, "[skills]\ndirs = [\".agents\"]\n").unwrap();
        let err = load(&user, None, false, &[]).err().unwrap().to_string();
        assert!(err.contains("skills.dirs was removed") && err.contains("other_apps"), "{err}");
        std::fs::write(&user, "[skills]\nopt_in = [\"deploy\"]\n").unwrap();
        let err = load(&user, None, false, &[]).err().unwrap().to_string();
        assert!(err.contains("skills.opt_in takes paths"), "{err}");

        // Both lists add up across layers, so a project cannot undo the user's choices.
        std::fs::write(&user, "[skills]\nother_apps = true\nopt_in = [\"~/a/SKILL.md\"]\ndisabled = [\"x\"]\n").unwrap();
        let skills = load(&user, Some(&project), false, &[]).unwrap().config.skills;
        assert!(skills.other_apps);
        assert_eq!(skills.opt_in.len(), 2, "{:?}", skills.opt_in);
        assert_eq!(skills.disabled.len(), 3, "{:?}", skills.disabled);
    }

    #[test]
    fn string_overrides_need_no_quotes() {
        let loaded = load(Path::new("/nonexistent"), None, false, &["model=deepseek/deepseek-flash".into()]).unwrap();
        assert_eq!(loaded.config.model.as_deref(), Some("deepseek/deepseek-flash"));
    }

    fn with_user_file(text: &str) -> (tempfile::TempDir, std::path::PathBuf) {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("config.toml");
        std::fs::write(&path, text).unwrap();
        (dir, path)
    }

    #[test]
    fn a_key_this_version_does_not_know_is_a_warning() {
        let (_dir, path) = with_user_file("thinking = \"low\"\nthinkng = \"high\"\n[future]\nx = 1\n[tools.bash]\ndefault_timeout_sec = 5\n");
        let loaded = load(&path, None, false, &[]).unwrap();
        assert_eq!(loaded.config.thinking, ThinkingLevel::Low);
        let warnings = loaded.warnings.join("\n");
        assert!(warnings.contains("unknown setting `thinkng` (did you mean `thinking`?)"), "{warnings}");
        assert!(warnings.contains("config.toml:2"), "{warnings}");
        assert!(warnings.contains("unknown setting `future`"), "{warnings}");
        assert!(warnings.contains("did you mean `tools.bash.default_timeout_secs`"), "{warnings}");
    }

    #[test]
    fn what_stays_an_error() {
        // A value this version cannot read: it must not guess.
        let (_dir, path) = with_user_file("permission_mode = \"plan\"\n");
        assert!(load(&path, None, false, &[]).is_err());
        // Anything unknown in a safety rule.
        let (_dir, path) = with_user_file("[[rules]]\nprefix = [\"git\"]\ndecision = \"ask\"\nonly_on = \"fridays\"\n");
        assert!(load(&path, None, false, &[]).is_err());
        // A key typed on the command line.
        let error = load(Path::new("/nonexistent"), None, false, &["thinkng=low".into()]).err().expect("rejected");
        assert!(error.to_string().contains("did you mean `thinking`"), "{error}");
        // Writing one through config/write.
        let error = check_file("thinkng = \"low\"\n", Path::new("c.toml")).unwrap_err();
        assert!(error.to_string().contains("did you mean `thinking`"), "{error}");
    }

}
