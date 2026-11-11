//! `her`: command-line front end of the kernel.

mod approval_prompt;
#[cfg(test)]
mod cli_shape;
mod cmd_browser;
mod cmd_chat;
mod cmd_config;
mod cmd_web_search;
mod cmd_check;
mod cmd_customize;
mod cmd_exec;
mod cmd_info;
mod cmd_install;
mod cmd_interactive;
mod cmd_login;
mod cmd_mcp;
mod cmd_serve;
mod render;
mod setup;
mod term;

use anyhow::Context as _;
use clap::Args;
use clap::CommandFactory;
use clap::FromArgMatches;
use clap::Parser;
use clap::Subcommand;
use her_llm::ThinkingLevel;
use her_protocol::PermissionMode;
use std::path::PathBuf;
use std::process::ExitCode;

/// The top-level help, by what the commands are for. `cli_shape` keeps it in step with them.
const TOP_LEVEL_HELP: &str = "\
{about-with-newline}
{usage-heading} {usage}

Run tasks:
  exec        Run a task without an interactive interface
  resume      Continue a session in the terminal
  sessions    List, rename and remove sessions

Models:
  login       Connect a model: a key, a subscription, a service
  logout      Remove a saved key, or sign out of ChatGPT
  models      List the models in the model menu

Tools and extensions:
  tools       List the tools a session here would have
  mcp         List, add and remove MCP servers
  skills      List the skills a session here would offer
  web-search  Show, set up and check web search
  browser     Set up the browser the agent uses
  install     Install a component: pdf, browser, computer
  uninstall   Remove a component

Setup:
  config      Read and change the configuration
  status      Show the effective configuration
  doctor      Check configuration, credentials and directories
  trust       Trust a project's own configuration
  upgrade     Upgrade her to the latest release
  completion  Print a shell completion script

For other programs:
  serve       Serve JSON-RPC on stdio or a Unix socket
  acp         Serve ACP on stdio, for editors such as Zed
  protocol    Describe the native protocol

Options:
{options}

Examples:
  her
  her \"fix the failing test\"
  her exec \"fix the failing test\"
  her exec --json \"summarize the changes\" | jq -r .final_output
  her exec --resume last \"now update the changelog\"
  her models
  her doctor

Run `her <command> --help` for a command's options.
";

#[derive(Parser)]
#[command(
    name = "her",
    version,
    about = "her: an AI agent for your terminal, your editor and your programs",
    help_template = TOP_LEVEL_HELP,
    override_usage = "her [\"PROMPT\"]\n       her <COMMAND>",
    disable_version_flag = true,
    // The last one wins, so that a wrapper or an alias can set a flag and the command line can
    // still change it. Clap applies this to every subcommand.
    args_override_self = true
)]
struct Cli {
    /// Override a configuration value, e.g. -c thinking=low (repeatable)
    #[arg(short = 'c', long = "config", global = true, value_name = "KEY=VALUE")]
    overrides: Vec<String>,

    /// Add the MCP servers of a file in the .mcp.json format (repeatable)
    #[arg(long = "mcp-config", global = true, value_name = "FILE")]
    mcp_config: Vec<PathBuf>,

    /// Print version
    #[arg(short = 'V', short_alias = 'v', long, action = clap::ArgAction::Version)]
    version: Option<bool>,

    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand)]
// Build arguments only for the selected command; help still expands its own command.
#[command(defer = true)]
enum Command {
    /// The document worker `her serve` starts: PDFium, one request a line on stdin
    #[command(hide = true)]
    DocWorker,
    /// The presentation worker `her serve` starts: open decks, one request a line on stdin
    #[command(hide = true)]
    DeckWorker,
    /// Run a managed connector's internal tool adapter
    #[command(hide = true)]
    Connector {
        #[command(subcommand)]
        command: ConnectorCommand,
    },
    /// Chat with a model directly, without tools (checks the model setup)
    #[command(hide = true)]
    Chat {
        /// Message to send; without it an interactive chat starts ("-" reads stdin)
        prompt: Option<String>,
        /// Model as provider/model-id
        #[arg(short, long)]
        model: Option<String>,
        /// Thinking level: default (the provider decides), or one the model offers: off, on,
        /// minimal, low, medium, high, xhigh, max
        #[arg(long)]
        thinking: Option<ThinkingLevel>,
        /// System prompt
        #[arg(long)]
        system: Option<String>,
        /// Offer a sample get_time tool to check tool calling
        #[arg(long)]
        tool_test: bool,
    },
    /// The interactive mode: what `her` and `her "PROMPT"` come to
    #[command(hide = true)]
    Interactive {
        /// The first message
        prompt: Option<String>,
        /// Working directory (default: the current directory)
        #[arg(long)]
        cwd: Option<PathBuf>,
        #[command(flatten)]
        run: RunArgs,
    },
    /// Run a task without an interactive interface
    Exec {
        /// The task ("-" reads it from stdin)
        prompt: String,
        /// Working directory (default: the current directory)
        #[arg(long)]
        cwd: Option<PathBuf>,
        /// Continue a session: `last` (of this project) or a session id or a unique part of one
        #[arg(long, value_name = "last|ID", conflicts_with = "cwd")]
        resume: Option<String>,
        /// Do not keep a session log
        #[arg(long, conflicts_with = "resume")]
        no_save: bool,
        #[command(flatten)]
        args: ExecArgs,
    },
    /// Continue a session in the terminal
    Resume {
        /// `last` (of this project), a session id, or a unique part of one
        #[arg(default_value = "last", value_name = "last|ID")]
        session: String,
        /// The next message
        prompt: Option<String>,
        #[command(flatten)]
        run: RunArgs,
    },
    /// Connect a model: a key, a subscription, a service
    ///
    /// Without arguments it asks. In a script, pipe the key:
    /// printf %s "$KEY" | her login deepseek --key-stdin --model deepseek/deepseek-flash
    Login {
        /// A provider (`her models` lists them), `chatgpt`, or a name for your own service
        provider: Option<String>,
        /// Read the key from standard input
        #[arg(long)]
        key_stdin: bool,
        /// The model new sessions start on: writes `model` to your configuration
        #[arg(short, long)]
        model: Option<String>,
        /// The address of a compatible service of your own: writes it to models.toml
        #[arg(long, value_name = "URL")]
        base_url: Option<String>,
        /// What the service speaks (default: openai-chat)
        #[arg(long, value_name = "API", requires = "base_url")]
        api: Option<String>,
        /// The service takes no key
        #[arg(long, requires = "base_url")]
        no_key: bool,
    },
    /// Remove a saved key, or sign out of ChatGPT
    Logout {
        provider: String,
        /// For chatgpt: also forget this registration, so another account can sign in
        #[arg(long)]
        forget: bool,
    },
    /// List, rename and remove sessions
    ///
    /// Of this project; --all for every project.
    #[command(args_conflicts_with_subcommands = true)]
    Sessions {
        #[command(flatten)]
        list: SessionsArgs,
        #[command(subcommand)]
        action: Option<SessionsAction>,
    },
    /// List the models in the model menu
    ///
    /// By provider. `hide` and `unhide` change the menu.
    #[command(args_conflicts_with_subcommands = true)]
    Models {
        #[command(flatten)]
        list: ModelsArgs,
        #[command(subcommand)]
        action: Option<ModelsAction>,
    },
    /// List the tools a session here would have
    ///
    /// With a name, its description and parameters.
    Tools {
        /// One tool
        name: Option<String>,
        /// Every tool of a group that is off, one line each
        #[arg(long)]
        all: bool,
        /// Print JSON
        #[arg(long)]
        json: bool,
    },
    /// Read and change the configuration
    ///
    /// Each value comes from one layer: your file, this project's, or the command line.
    #[command(args_conflicts_with_subcommands = true)]
    Config {
        #[command(flatten)]
        list: ConfigListArgs,
        #[command(subcommand)]
        action: Option<ConfigAction>,
    },
    /// Show, set up and check web search
    #[command(name = "web-search", args_conflicts_with_subcommands = true)]
    WebSearch {
        #[command(subcommand)]
        action: WebSearchAction,
    },
    /// Show the effective configuration
    ///
    /// Model, provider, credentials and directories.
    Status {
        /// Print JSON
        #[arg(long)]
        json: bool,
    },
    /// Check configuration, credentials and directories
    Doctor {
        /// Print JSON
        #[arg(long)]
        json: bool,
        /// Warnings count as failures: for CI
        #[arg(long)]
        strict: bool,
        /// Also make one real request to the default model
        #[arg(long)]
        online: bool,
    },
    /// Install a component: pdf, browser, computer
    ///
    /// What most people do not need stays out of the program until it is asked for.
    Install {
        #[arg(value_parser = ["pdf", "browser", "computer"])]
        component: String,
    },
    /// Remove a component
    Uninstall {
        #[arg(value_parser = ["pdf", "browser", "computer"])]
        component: String,
    },
    /// Upgrade her to the latest release
    #[command(alias = "update")]
    Upgrade {
        /// Only say whether there is a newer release
        #[arg(long)]
        check: bool,
        /// This version, even an older one
        #[arg(long, value_name = "V")]
        version: Option<String>,
        /// With --check: print JSON
        #[arg(long, requires = "check")]
        json: bool,
    },
    /// Print a shell completion script
    ///
    /// bash: eval "$(her completion bash)". zsh: source <(her completion zsh).
    /// fish: her completion fish | source.
    Completion {
        #[arg(value_parser = ["bash", "zsh", "fish"])]
        shell: String,
    },
    /// List the skills a session here would offer
    ///
    /// `add`, `remove`, `enable` and `disable` change them.
    #[command(args_conflicts_with_subcommands = true)]
    Skills {
        #[command(flatten)]
        list: SkillsArgs,
        #[command(subcommand)]
        action: Option<SkillsAction>,
    },
    /// Set up the browser the agent uses
    Browser {
        #[command(subcommand)]
        command: BrowserCommand,
    },
    /// Trust a project's own configuration
    ///
    /// Its .her/config.toml may then change security settings.
    Trust {
        /// Project directory (default: the current directory)
        dir: Option<PathBuf>,
        /// Stop trusting it
        #[arg(long)]
        remove: bool,
    },
    /// List, add and remove MCP servers
    #[command(args_conflicts_with_subcommands = true)]
    Mcp {
        #[command(flatten)]
        list: McpListArgs,
        #[command(subcommand)]
        command: Option<McpCommand>,
    },
    /// Serve JSON-RPC on stdio or a Unix socket
    ///
    /// The native protocol, for programs that drive her.
    Serve {
        /// Serve one client on stdin and stdout (the default)
        #[arg(long, conflicts_with = "listen")]
        stdio: bool,
        /// Listen on a Unix socket: unix:// for $HER_HOME/run/her.sock, or unix://PATH
        #[arg(long, value_name = "URL")]
        listen: Option<String>,
        /// With --listen: exit after this many seconds without clients or running tasks
        #[arg(long, value_name = "SECS", requires = "listen")]
        idle_exit: Option<u64>,
    },
    /// Serve ACP on stdio, for editors such as Zed
    ///
    /// The Agent Client Protocol.
    Acp {
        /// Sign in at this terminal instead of serving: what an editor runs for you
        #[arg(long)]
        login: bool,
    },
    /// Describe the native protocol
    Protocol {
        #[command(subcommand)]
        command: ProtocolCommand,
    },
}

#[derive(Subcommand)]
#[command(defer = true)]
enum ConnectorCommand {
    Serve { id: String, #[arg(long)] home: PathBuf },
}

#[derive(Subcommand)]
#[command(defer = true)]
enum BrowserCommand {
    /// Download Chrome for Testing into ~/.her/browser/chromium, for machines without Chrome,
    /// Edge or Chromium (used only when none is installed)
    Install,
    /// Let the agent use your own Chrome through the Hermesh extension
    Extension {
        #[command(subcommand)]
        command: ExtensionCommand,
    },
    /// The Native Messaging host that Chrome starts for the Hermesh extension
    #[command(hide = true)]
    NativeHost {
        /// The her service's socket, when it is not $HER_HOME/run/her.sock
        #[arg(long, value_name = "PATH")]
        socket: Option<PathBuf>,
        /// What Chrome passes: the extension's origin, and on Windows the parent window
        #[arg(trailing_var_arg = true, allow_hyphen_values = true)]
        args: Vec<String>,
    },
}

#[derive(Subcommand)]
#[command(defer = true)]
enum ExtensionCommand {
    /// Register her with Chrome, Chromium, Edge and Brave as the extension's host
    Install {
        /// Also allow this extension ID (a copy from a store or loaded from another folder)
        #[arg(long = "extension-id", value_name = "ID")]
        extension_ids: Vec<String>,
        /// Register for this browser user data directory only
        #[arg(long, value_name = "DIR")]
        user_data_dir: Option<PathBuf>,
        /// The socket of the her service the host connects to, when it is not
        /// $HER_HOME/run/her.sock (a service started with --listen unix://PATH)
        #[arg(long, value_name = "PATH")]
        socket: Option<PathBuf>,
        /// Also write the Hermesh extension to this folder, to add to Chrome with "Load unpacked"
        #[arg(long, value_name = "DIR")]
        folder: Option<PathBuf>,
    },
    /// Remove what install wrote
    Uninstall,
    /// Show where the host is registered
    Status {
        /// Print JSON
        #[arg(long)]
        json: bool,
    },
}

#[derive(Subcommand)]
#[command(defer = true)]
enum McpCommand {
    /// List the servers: name, transport, exposure and where each is configured
    List(McpListArgs),
    /// Show one server's settings, with secret values hidden
    Get {
        name: String,
        /// Print JSON
        #[arg(long)]
        json: bool,
    },
    /// Check a server, then add it to your configuration; one that signs in with OAuth is signed in first
    Add {
        name: String,
        /// The server's URL (streamable HTTP)
        #[arg(long, conflicts_with = "command")]
        url: Option<String>,
        /// An HTTP header, as KEY=VALUE (repeatable)
        #[arg(long = "header", value_name = "KEY=VALUE", requires = "url")]
        headers: Vec<String>,
        /// A variable for the command, as KEY=VALUE (repeatable)
        #[arg(long = "env", value_name = "KEY=VALUE")]
        env: Vec<String>,
        /// Keep this --env variable's value in the keychain; the configuration refers to it
        #[arg(long = "secret-env", value_name = "KEY")]
        secret_env: Vec<String>,
        /// Read a bearer token from standard input and keep it in the keychain
        #[arg(long, requires = "url")]
        token: bool,
        /// In your own configuration, ~/.her/config.toml (the default)
        #[arg(long, overrides_with_all = ["local", "project"])]
        global: bool,
        /// Only for you, only in this project (your personal project settings)
        #[arg(long, overrides_with_all = ["global", "project"])]
        local: bool,
        /// In the project's .her/config.toml, for everyone who uses the repository
        #[arg(long, overrides_with_all = ["global", "local"])]
        project: bool,
        /// Add it even when the check fails
        #[arg(long)]
        force: bool,
        /// Do not sign in now; sign in later with `her mcp login`
        #[arg(long)]
        no_login: bool,
        /// The command that runs the server, after --
        #[arg(last = true, value_name = "COMMAND")]
        command: Vec<String>,
    },
    /// Sign in to a server with OAuth in your browser
    Login { name: String },
    /// Sign out of a server: revoke and delete its tokens
    Logout { name: String },
    /// Remove a server from the file that defines it
    Remove {
        name: String,
        /// Also from the project's .her/config.toml
        #[arg(long)]
        project: bool,
    },
    /// Turn a server on again
    Enable {
        name: String,
        /// In your own configuration (the default)
        #[arg(long, overrides_with = "local")]
        global: bool,
        /// Only for you, only in this project
        #[arg(long, overrides_with = "global")]
        local: bool,
    },
    /// Turn a server off; a server of the repository is turned off for you alone
    Disable {
        name: String,
        /// In your own configuration (the default)
        #[arg(long, overrides_with = "local")]
        global: bool,
        /// Only for you, only in this project
        #[arg(long, overrides_with = "global")]
        local: bool,
    },
}

// What `her mcp` and `her mcp list` show. (A doc comment here would become their description.)
#[derive(Args, Clone)]
struct McpListArgs {
    /// Connect to every server and list its tools
    #[arg(long)]
    check: bool,
    /// Print JSON
    #[arg(long)]
    json: bool,
}

#[derive(Subcommand)]
#[command(defer = true)]
enum SessionsAction {
    /// List recent sessions of this project
    List(SessionsArgs),
    /// Move a session to the trash folder
    Remove {
        /// A session id, or a unique part of one
        session: String,
    },
    /// Give a session a title
    Rename {
        /// `last` (of this project), a session id, or a unique part of one
        session: String,
        title: String,
    },
}

// What `her config` and `her config list` show.
#[derive(Args, Clone)]
struct ConfigListArgs {
    /// Say which layer each value comes from
    #[arg(long)]
    show_origin: bool,
    /// Print JSON
    #[arg(long)]
    json: bool,
}

// The file a config command means.
#[derive(Args, Clone)]
struct ScopeArgs {
    /// ~/.her/config.toml
    #[arg(long, overrides_with_all = ["local", "project"])]
    global: bool,
    /// Only for you, only in this project
    #[arg(long, overrides_with_all = ["global", "project"])]
    local: bool,
    /// The project's .her/config.toml, for everyone who uses the repository
    #[arg(long, overrides_with_all = ["global", "local"])]
    project: bool,
}

impl ScopeArgs {
    fn scope(&self) -> Option<cmd_config::Scope> {
        match (self.global, self.local, self.project) {
            (true, _, _) => Some(cmd_config::Scope::Global),
            (_, true, _) => Some(cmd_config::Scope::Local),
            (_, _, true) => Some(cmd_config::Scope::Project),
            _ => None,
        }
    }
}

#[derive(Subcommand)]
#[command(defer = true)]
enum ConfigAction {
    /// List the values in effect
    List(ConfigListArgs),
    /// Print one value; exit 1 when it is not set
    Get {
        key: String,
        #[command(flatten)]
        scope: ScopeArgs,
        /// Say which layer it comes from
        #[arg(long)]
        show_origin: bool,
        /// Print JSON
        #[arg(long)]
        json: bool,
    },
    /// Save a value
    Set {
        key: String,
        value: String,
        #[command(flatten)]
        scope: ScopeArgs,
    },
    /// Remove a value from a file
    Unset {
        key: String,
        #[command(flatten)]
        scope: ScopeArgs,
    },
    /// Print where a configuration file is
    Path {
        #[command(flatten)]
        scope: ScopeArgs,
    },
}

#[derive(Subcommand)]
#[command(defer = true)]
enum WebSearchAction {
    /// Show each search service and which one searches use now
    Status {
        /// Print JSON
        #[arg(long)]
        json: bool,
    },
    /// Try a service's key from standard input, and keep it unless it is refused
    SetKey { service: String },
    /// Make one real search; exit 1 when it fails
    Check {
        /// A service (default: the one searches use now)
        service: Option<String>,
        /// Print JSON
        #[arg(long)]
        json: bool,
    },
}

// What `her sessions` and `her sessions list` show.
#[derive(Args, Clone)]
struct SessionsArgs {
    /// Sessions of every project
    #[arg(long)]
    all: bool,
    #[arg(long, default_value_t = 20)]
    limit: usize,
    /// Print JSON
    #[arg(long)]
    json: bool,
}

// What `her models` and `her models list` show.
#[derive(Args, Clone)]
pub(crate) struct ModelsArgs {
    /// Only this provider
    #[arg(long)]
    provider: Option<String>,
    /// Also the hidden models
    #[arg(long)]
    all: bool,
    /// Ask the providers and the public model catalog first
    #[arg(long)]
    refresh: bool,
    /// Print JSON (the `models/list` result)
    #[arg(long)]
    json: bool,
}

// What `her skills` and `her skills list` show.
#[derive(Args, Clone)]
struct SkillsArgs {
    /// Directory (default: the current directory)
    dir: Option<PathBuf>,
    /// Also list skills that are shadowed, disabled or failed to load
    #[arg(long)]
    all: bool,
    /// Print JSON
    #[arg(long)]
    json: bool,
}

#[derive(Subcommand)]
#[command(defer = true)]
enum SkillsAction {
    /// List the skills a session here would offer
    List(SkillsArgs),
    /// Copy a skill (a folder with SKILL.md, or a .zip or .skill archive) into ~/.her/skills
    Add {
        path: PathBuf,
        /// Into ~/.her/skills (the default)
        #[arg(long, overrides_with = "project")]
        global: bool,
        /// Into the project's .agents/skills instead
        #[arg(long, overrides_with = "global")]
        project: bool,
        /// Put it in place of a skill of the same name (the old one goes to the trash)
        #[arg(long)]
        replace: bool,
    },
    /// Move one of your own skills to the trash
    Remove {
        /// A name, or a path to the skill
        skill: String,
    },
    /// Turn a skill on again
    Enable {
        /// A name (every skill of the name), or a path (that copy)
        skill: String,
    },
    /// Turn a skill off in your configuration
    Disable {
        /// A name (every skill of the name), or a path (that copy)
        skill: String,
    },
}

#[derive(Subcommand)]
#[command(defer = true)]
pub(crate) enum ModelsAction {
    /// List the models in the model menu
    List(ModelsArgs),
    /// Try a model with real requests: text, tools, levels, images, cache, cancel
    Check {
        /// provider/model-id (default: the default model)
        model: Option<String>,
        /// Print JSON
        #[arg(long)]
        json: bool,
    },
    /// Put a model back in the model menu
    Unhide {
        /// provider/model-id
        model: String,
    },
    /// Take a model out of the model menu
    Hide {
        /// provider/model-id
        model: String,
    },
}

#[derive(Subcommand)]
#[command(defer = true)]
enum ProtocolCommand {
    /// Print the JSON Schema of every message, with the methods and error codes
    Schema,
}

// What every command that runs the agent takes (standalone-cli-design §4.3).
#[derive(Args, Clone, Default)]
pub struct RunArgs {
    /// Model as provider/model-id
    #[arg(short, long)]
    pub model: Option<String>,
    /// Thinking level: default (the provider decides), or one the model offers: off, on,
    /// minimal, low, medium, high, xhigh, max
    #[arg(long)]
    pub thinking: Option<ThinkingLevel>,
    /// Permission mode: manual (ask before changes), accept-edits (file changes in the
    /// workspace run without asking) or bypass-permissions (everything runs without asking)
    #[arg(long, alias = "approval", value_name = "MODE", value_parser = parse_permission_mode, overrides_with = "dangerously_skip_permissions")]
    pub permission_mode: Option<ModeArg>,
    /// Same as --permission-mode bypass-permissions: every action runs without asking,
    /// including changes to .git and shell profiles. Use only in an isolated environment
    #[arg(long, overrides_with = "permission_mode")]
    pub dangerously_skip_permissions: bool,
    /// Stop a task after this many turns
    #[arg(long)]
    pub max_turns: Option<u32>,
    /// Use only these built-in tools, e.g. --tools read,bash
    #[arg(long, value_delimiter = ',', value_name = "NAMES")]
    pub tools: Option<Vec<String>>,
    /// Another directory the task may write to (repeatable)
    #[arg(long = "add-dir", value_name = "DIR")]
    pub add_dir: Vec<PathBuf>,
}

// `her exec` on top of a run's own: what to print, and what comes from outside.
#[derive(Args, Clone)]
pub struct ExecArgs {
    #[command(flatten)]
    pub run: RunArgs,
    /// text: the reply as it streams; json: one result object at the end; stream-json: every event
    /// as JSON lines
    #[arg(long, value_enum, default_value_t = OutputFormat::Text, overrides_with = "json")]
    pub output: OutputFormat,
    /// Same as --output json
    #[arg(long, overrides_with = "output")]
    pub json: bool,
    /// Read operations (JSON lines) from stdin, e.g. approvals
    #[arg(long)]
    pub ops_stdin: bool,
    /// Attach an image: png, jpeg, gif or webp (repeatable)
    #[arg(long, value_name = "PATH")]
    pub image: Vec<PathBuf>,
    /// Write the last assistant message to this file
    #[arg(long, value_name = "FILE")]
    pub output_last_message: Option<PathBuf>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, clap::ValueEnum)]
pub enum OutputFormat {
    Text,
    Json,
    StreamJson,
}

/// A `--permission-mode` value, with a notice when it uses a name from before the rename.
#[derive(Clone, Debug)]
pub struct ModeArg {
    pub mode: PermissionMode,
    pub notice: Option<String>,
}

fn parse_permission_mode(name: &str) -> Result<ModeArg, String> {
    PermissionMode::parse_with_notice(name).map(|(mode, notice)| ModeArg { mode, notice })
}

impl ExecArgs {
    pub fn format(&self) -> OutputFormat {
        if self.json { OutputFormat::Json } else { self.output }
    }
}

impl RunArgs {
    /// The mode chosen on the command line, if any.
    pub fn permission_mode(&self) -> Option<PermissionMode> {
        if self.dangerously_skip_permissions {
            return Some(PermissionMode::BypassPermissions);
        }
        self.permission_mode.as_ref().map(|arg| arg.mode)
    }

    /// Warnings about the arguments themselves.
    pub fn warnings(&self) -> impl Iterator<Item = String> + '_ {
        self.permission_mode.iter().filter_map(|arg| arg.notice.clone())
    }
}

async fn run(overrides: &[String], command: Command) -> anyhow::Result<u8> {
    match command {
        Command::Connector { command: ConnectorCommand::Serve { id, home } } => {
            if !home.is_absolute() { anyhow::bail!("connector home must be absolute"); }
            her_core::connectors::serve(&home, &id, tokio::io::stdin(), tokio::io::stdout()).await.map_err(anyhow::Error::msg)?;
            Ok(0)
        }
        Command::Chat {
            prompt,
            model,
            thinking,
            system,
            tool_test,
        } => {
            cmd_chat::chat(
                overrides,
                cmd_chat::ChatOptions {
                    prompt,
                    model,
                    thinking,
                    system,
                    tool_test,
                },
            )
            .await
        }
        Command::Exec {
            prompt,
            cwd,
            resume,
            no_save,
            args,
        } => match resume {
            Some(session) => cmd_exec::resume(overrides, &session, &prompt, &args).await,
            None => cmd_exec::exec(overrides, &prompt, cwd, !no_save, &args).await,
        },
        Command::Interactive { prompt, cwd, run } => {
            cmd_interactive::interactive(overrides, cmd_interactive::Start { prompt, cwd, resume: None, run }).await
        }
        Command::Resume { session, prompt, run } => {
            cmd_interactive::interactive(overrides, cmd_interactive::Start { prompt, cwd: None, resume: Some(session), run }).await
        }
        Command::Login { provider, key_stdin, model, base_url, api, no_key } => {
            cmd_login::login(overrides, cmd_login::LoginArgs { provider, key_stdin, model, base_url, api, no_key }).await
        }
        Command::Logout { provider, forget } => cmd_login::logout(overrides, &provider, forget).await,
        Command::Models { action: Some(ModelsAction::Check { model, json }), .. } => cmd_check::check(overrides, model, json).await,
        Command::Doctor { json, strict, online: true } => {
            let online = cmd_info::online_check(overrides).await;
            cmd_info::doctor(overrides, json, strict, Some(online))
        }
        Command::Install { component } => cmd_install::install(&component).await,
        Command::Upgrade { check, version, json } => cmd_install::upgrade(check, version, json).await,
        Command::WebSearch { action: WebSearchAction::SetKey { service } } => cmd_web_search::set_key(overrides, &service).await,
        Command::WebSearch { action: WebSearchAction::Check { service, json } } => cmd_web_search::check(overrides, service, json).await,
        Command::Serve { listen, idle_exit, .. } => cmd_serve::serve(overrides, listen, idle_exit).await,
        Command::Acp { login: true } => cmd_login::login(overrides, Default::default()).await,
        Command::Acp { login: false } => cmd_serve::acp(overrides).await,
        Command::Browser {
            command: BrowserCommand::Install,
        } => cmd_info::browser_install().await,
        Command::Browser {
            command: BrowserCommand::NativeHost { socket, .. },
        } => cmd_browser::native_host(socket).await,
        Command::Browser {
            command: BrowserCommand::Extension { command },
        } => match command {
            ExtensionCommand::Install { extension_ids, user_data_dir, socket, folder } => cmd_browser::install(&extension_ids, user_data_dir, socket, folder),
            ExtensionCommand::Uninstall => cmd_browser::uninstall(),
            ExtensionCommand::Status { json } => cmd_browser::status(json),
        },
        // Only `--check` gets here; a plain listing runs without the async runtime.
        Command::Mcp { list, command: None }
        | Command::Mcp { command: Some(McpCommand::List(list)), .. } => cmd_mcp::check(overrides, list.json).await,
        Command::Mcp {
            command: Some(McpCommand::Add { name, url, headers, env, secret_env, token, local, project, force, no_login, command, .. }),
            ..
        } => {
            cmd_customize::mcp_add(cmd_customize::AddArgs { name, url, headers, env, secret_env, token, local, project, command, force, no_login })
                .await
        }
        Command::Mcp {
            command: Some(McpCommand::Login { name }),
            ..
        } => cmd_customize::mcp_login(&name).await,
        Command::Mcp {
            command: Some(McpCommand::Remove { name, project }),
            ..
        } => cmd_customize::mcp_remove(&name, project).await,
        Command::Mcp {
            command: Some(McpCommand::Logout { name }),
            ..
        } => cmd_customize::mcp_logout(&name).await,
        Command::Sessions { .. }
        | Command::Models { .. }
        | Command::Status { .. }
        | Command::Tools { .. }
        | Command::Uninstall { .. }
        | Command::Config { .. }
        | Command::WebSearch { action: WebSearchAction::Status { .. } }
        | Command::Doctor { online: false, .. }
        | Command::Completion { .. }
        | Command::Skills { .. }
        | Command::Trust { .. }
        | Command::Mcp { .. }
        | Command::Protocol { .. }
        | Command::DocWorker
        | Command::DeckWorker => {
            unreachable!("local commands run without the async runtime")
        }
    }
}

fn run_async(overrides: &[String], command: Command) -> anyhow::Result<u8> {
    // A foreground command is driven by the calling thread. Its asynchronous tools and
    // sub-agents still make progress together; filesystem work uses the blocking pool.
    // The service runs many clients' sessions on a worker per available CPU. Other commands,
    // `her acp` among them, serve one client: four workers are plenty, and each idle worker
    // costs its stack and queues.
    let mut builder = match command {
        Command::Exec { .. } | Command::Chat { .. } | Command::Interactive { .. } | Command::Resume { .. } => {
            tokio::runtime::Builder::new_current_thread()
        }
        _ => {
            let limit = if matches!(command, Command::Serve { .. }) { 16 } else { 4 };
            let workers = std::thread::available_parallelism().map_or(4, |n| n.get().min(limit));
            let mut builder = tokio::runtime::Builder::new_multi_thread();
            builder.worker_threads(workers);
            builder
        }
    };
    if !matches!(command, Command::Serve { .. }) {
        // The I/O driver reserves this many events up front (48 bytes each on macOS). One
        // client's sockets and pipes never have a thousand ready at once.
        builder.max_io_events_per_tick(256);
    }
    let runtime = builder
        // Blocking threads (file tools, log writes) go away soon after a burst.
        .thread_keep_alive(std::time::Duration::from_secs(3))
        // Many sessions writing logs or files at once share these instead of each adding one.
        .max_blocking_threads(64)
        .enable_all()
        .build()
        .context("cannot start the async runtime")?;
    let result = runtime.block_on(run(overrides, command));
    // Background readers (stdin) must not keep the process alive.
    runtime.shutdown_timeout(std::time::Duration::from_millis(200));
    result
}

/// Parses a command line that names no global option, or returns None for `Cli::parse` to do it.
///
/// When its parser is built, clap copies each global option into every subcommand: about 40 KiB
/// of fresh memory on each launch. A command line that names none has no use for the copies, and
/// a parser without them reads it the same way. The help and the errors come from the full
/// parser, because they list the global options.
fn parse_plain(args: &[std::ffi::OsString]) -> Option<Cli> {
    let plain = args.iter().skip(1).all(|arg| {
        let arg = arg.as_encoded_bytes();
        !(arg.starts_with(b"-c") || arg.starts_with(b"--config") || arg.starts_with(b"--mcp-config"))
            && ![&b"-h"[..], b"--help", b"help", b"-V", b"-v", b"--version"].contains(&arg)
    });
    if !plain {
        return None;
    }
    let mut matches = parser_without_global_options().try_get_matches_from(args).ok()?;
    Cli::from_arg_matches_mut(&mut matches).ok()
}

fn parser_without_global_options() -> clap::Command {
    Cli::command()
        .mut_arg("overrides", |arg| arg.global(false))
        .mut_arg("mcp_config", |arg| arg.global(false))
}

/// Whether a command line asks for JSON. Its failures are then JSON too, on stdout.
fn wants_json(args: &[std::ffi::OsString]) -> bool {
    let mut words = args.iter().skip(1).map(|arg| arg.to_string_lossy()).take_while(|word| word != "--").peekable();
    while let Some(word) = words.next() {
        if ["--json", "--output=json", "--output=stream-json"].contains(&word.as_ref()) {
            return true;
        }
        if word == "--output" && words.peek().is_some_and(|value| value == "json" || value == "stream-json") {
            return true;
        }
    }
    false
}

/// A failure as JSON: the fields of the result object of `her exec --json`.
fn print_failure(kind: &str, message: &str, help: Option<&str>) {
    let mut error = serde_json::json!({ "kind": kind, "message": message });
    if let Some(help) = help {
        error["help"] = help.into();
    }
    println!("{}", serde_json::json!({ "error": error, "exit_code": 2 }));
}

/// What to write instead of a spelling from another tool or from before a rename.
fn pointer(args: &[std::ffi::OsString]) -> Option<&'static str> {
    let words: Vec<_> = args.iter().skip(1).map(|arg| arg.to_string_lossy()).take_while(|word| word != "--").collect();
    let words: Vec<&str> = words.iter().map(|word| word.as_ref()).collect();
    let has = |names: &[&str]| words.iter().any(|word| names.contains(word));
    if has(&["--continue", "-r"]) || (words.first() != Some(&"exec") && has(&["--resume"])) || (words.first() == Some(&"resume") && has(&["-p", "--print"])) {
        return Some("To continue a session: her resume [last|ID], or in a script her exec --resume <last|ID> \"PROMPT\"");
    }
    if has(&["-p", "--print"]) {
        return Some("To run one task: her exec \"PROMPT\"");
    }
    if words.starts_with(&["models", "login"]) || words.starts_with(&["models", "logout"]) {
        return Some("Accounts have their own commands: her login chatgpt, her logout chatgpt");
    }
    // What the first word's rewrite let through (§4.5).
    if words.first() == Some(&"interactive") && words.iter().skip(1).filter(|word| !word.starts_with('-')).count() > 1 {
        return Some("A prompt goes in quotes: her \"fix the login test\"");
    }
    if words.starts_with(&["models", "show"]) {
        return Some("`her models unhide MODEL` puts a model back in the menu; `her models` lists them");
    }
    None
}

/// The commands `her` knows without building its parser; `cli_shape` keeps this in step with them.
const COMMAND_WORDS: &[&str] = &[
    "doc-worker", "deck-worker", "connector", "chat", "interactive", "exec", "resume", "login", "logout", "sessions", "models", "tools", "config", "web-search", "status",
    "doctor", "install", "uninstall", "upgrade", "update", "completion", "skills", "browser", "trust", "mcp", "serve", "acp", "protocol", "help",
];

/// How far a word may be from a command to count as a slip: one step for a short command, two for
/// a longer one, a swap of neighbours being one step.
fn close_command(word: &str) -> Option<&'static str> {
    COMMAND_WORDS
        .iter()
        .filter(|command| !["doc-worker", "deck-worker", "connector", "interactive", "chat"].contains(command))
        .map(|command| (strsim::osa_distance(word, command), *command))
        .filter(|(distance, command)| *distance <= if command.len() < 6 { 1 } else { 2 })
        .min()
        .map(|(_, command)| command)
}

/// What a command line that names no command comes to: `her` and `her "PROMPT"` are the
/// interactive mode (§4.5). Looks only at the words, so that other commands parse as before.
fn first_word(args: Vec<std::ffi::OsString>) -> Result<Vec<std::ffi::OsString>, String> {
    let word_at = |index: usize| args.get(index).map(|arg| arg.to_string_lossy());
    let mut index = 1;
    while let Some(word) = word_at(index) {
        if word == "-c" || word == "--config" || word == "--mcp-config" {
            index += 2;
        } else if word.starts_with("-c") && word.len() > 2 && !word.starts_with("--") || word.starts_with("--config=") || word.starts_with("--mcp-config=") {
            index += 1;
        } else {
            break;
        }
    }
    let interactive = |rest_from: usize, prompt: Option<&str>| {
        let mut out: Vec<std::ffi::OsString> = args[..rest_from].to_vec();
        out.insert(1, "interactive".into());
        match prompt {
            Some(prompt) => {
                out.push("--".into());
                out.push(prompt.into());
            }
            None => out.extend_from_slice(&args[rest_from..]),
        }
        out
    };
    let Some(word) = word_at(index) else { return Ok(interactive(index, None)) };
    let word = word.as_ref();
    if ["-h", "--help", "-V", "-v", "--version"].contains(&word) || COMMAND_WORDS.contains(&word) {
        return Ok(args.clone());
    }
    if word == "--" {
        let rest: Vec<String> = args[index + 1..].iter().map(|arg| arg.to_string_lossy().into_owned()).collect();
        return Ok(interactive(index, Some(&rest.join(" "))));
    }
    if word.starts_with('-') {
        return Ok(interactive(index, None));
    }
    if let Some(command) = close_command(word) {
        return Err(format!("error: no command {word}; did you mean `her {command}`?\n\nTo send it as a message: her -- {word}"));
    }
    Ok(interactive(index, None))
}

/// A command line clap did not take. Help and the version go out as usual; a mistake says what to
/// write instead.
fn usage_error(err: clap::Error, args: &[std::ffi::OsString]) -> ExitCode {
    if !err.use_stderr() {
        let _ = err.print();
        return ExitCode::SUCCESS;
    }
    let hint = pointer(args);
    if wants_json(args) {
        let text = err.render().to_string();
        print_failure("usage_error", text.trim().trim_start_matches("error: "), hint);
    } else {
        let _ = err.print();
        if let Some(hint) = hint {
            eprintln!("\n{hint}");
        }
    }
    ExitCode::from(2)
}

fn main() -> ExitCode {
    // When the reader goes away (`her sessions | head`), stop quietly like other Unix tools
    // instead of panicking on the failed write.
    #[cfg(unix)]
    // SAFETY: restoring the default disposition of SIGPIPE before any thread is started.
    unsafe {
        libc::signal(libc::SIGPIPE, libc::SIG_DFL);
    }
    let args = match first_word(std::env::args_os().collect()) {
        Ok(args) => args,
        Err(message) => {
            eprintln!("{message}");
            return ExitCode::from(2);
        }
    };
    let parsed = parse_plain(&args).map_or_else(|| Cli::try_parse_from(&args), Ok);
    let Cli {
        overrides,
        mcp_config,
        command,
        ..
    } = match parsed {
        Ok(cli) => cli,
        Err(err) => return usage_error(err, &args),
    };
    // A server on a socket outlives its clients: one that goes away while her writes to it must
    // cost that connection an error, not the process. Linux raises SIGPIPE for a socket; macOS's
    // sockets don't, which hid it.
    #[cfg(unix)]
    if matches!(command, Command::Serve { listen: Some(_), .. }) {
        // SAFETY: still before any thread is started.
        unsafe {
            libc::signal(libc::SIGPIPE, libc::SIG_IGN);
        }
    }
    let result = setup::load_mcp_config(&mcp_config).and_then(|()| match command {
        // Local commands skip the async runtime: starting it costs more than they do.
        Command::Sessions { list, action: None } | Command::Sessions { action: Some(SessionsAction::List(list)), .. } => {
            cmd_info::sessions(list.all, list.limit, list.json)
        }
        Command::Sessions { action: Some(SessionsAction::Remove { session }), .. } => cmd_info::sessions_remove(&session),
        Command::Sessions { action: Some(SessionsAction::Rename { session, title }), .. } => cmd_info::sessions_rename(&session, &title),
        Command::Uninstall { component } => cmd_install::uninstall(&component),
        Command::Tools { name, all, json } => cmd_info::tools(&overrides, name.as_deref(), all, json),
        Command::Config { list, action: None } | Command::Config { action: Some(ConfigAction::List(list)), .. } => {
            cmd_config::list(&overrides, list.show_origin, list.json)
        }
        Command::Config { action: Some(action), .. } => match action {
            ConfigAction::List(_) => unreachable!("handled above"),
            ConfigAction::Get { key, scope, show_origin, json } => cmd_config::get(&overrides, &key, scope.scope(), show_origin, json),
            ConfigAction::Set { key, value, scope } => cmd_config::set(&overrides, &key, &value, scope.scope()),
            ConfigAction::Unset { key, scope } => cmd_config::unset(&overrides, &key, scope.scope()),
            ConfigAction::Path { scope } => cmd_config::path(scope.scope()),
        },
        Command::WebSearch { action: WebSearchAction::Status { json } } => cmd_web_search::status(&overrides, json),
        Command::Models { list, action: None } | Command::Models { action: Some(ModelsAction::List(list)), .. } => {
            cmd_info::models(&overrides, list.provider.as_deref(), list.all, list.refresh, list.json, None)
        }
        Command::Models { action: Some(ModelsAction::Check { .. }), .. } => run_async(&overrides, command),
        Command::Models { action, .. } => cmd_info::models(&overrides, None, false, false, false, action),
        Command::Status { json } => cmd_info::status(&overrides, json),
        Command::Doctor { json, strict, online: false } => cmd_info::doctor(&overrides, json, strict, None),
        Command::Completion { shell } => {
            let shell = match shell.as_str() {
                "bash" => clap_complete::Shell::Bash,
                "zsh" => clap_complete::Shell::Zsh,
                _ => clap_complete::Shell::Fish,
            };
            clap_complete::generate(shell, &mut Cli::command(), "her", &mut std::io::stdout());
            Ok(0)
        }
        Command::Skills { list, action: None } | Command::Skills { action: Some(SkillsAction::List(list)), .. } => {
            cmd_info::skills(&overrides, list.dir, list.all, list.json)
        }
        Command::Skills { action: Some(action), .. } => match action {
            SkillsAction::List(_) => unreachable!("handled above"),
            SkillsAction::Add { path, project, replace, .. } => cmd_customize::skills_add(&path, project, replace),
            SkillsAction::Remove { skill } => cmd_customize::skills_remove(&skill),
            SkillsAction::Enable { skill } => cmd_customize::skills_switch(&skill, true),
            SkillsAction::Disable { skill } => cmd_customize::skills_switch(&skill, false),
        },
        Command::Trust { dir, remove } => cmd_info::trust(dir, remove),
        Command::Mcp { list, command: None } | Command::Mcp { command: Some(McpCommand::List(list)), .. } if !list.check => {
            cmd_mcp::list(&overrides, list.json)
        }
        Command::Mcp {
            command: Some(McpCommand::Get { name, json }),
            ..
        } => cmd_mcp::get(&overrides, &name, json),
        Command::Mcp {
            command: Some(McpCommand::Enable { name, local, .. }),
            ..
        } => cmd_customize::mcp_switch(&name, true, local),
        Command::Mcp {
            command: Some(McpCommand::Disable { name, local, .. }),
            ..
        } => cmd_customize::mcp_switch(&name, false, local),
        Command::Protocol {
            command: ProtocolCommand::Schema,
        } => cmd_serve::schema(),
        Command::DocWorker => {
            her_docs::engine::serve(std::io::stdin().lock(), std::io::stdout().lock())?;
            Ok(0)
        }
        Command::DeckWorker => {
            her_docs::deck::serve(std::io::stdin().lock(), std::io::stdout())?;
            Ok(0)
        }
        command => run_async(&overrides, command),
    });
    match result {
        Ok(code) => ExitCode::from(code),
        Err(err) => {
            if wants_json(&args) {
                if let Some(no_model) = err.downcast_ref::<setup::NoModel>() {
                    print_failure("no_model", "no model is set up", Some(&no_model.help));
                } else if let Some(rejected) = err.downcast_ref::<cmd_login::KeyRejected>() {
                    print_failure("key_rejected", &rejected.0, None);
                } else if let Some(missing) = err.downcast_ref::<setup::NoCredentials>() {
                    print_failure("no_credentials", &format!("no credentials for {}", missing.model), Some(&missing.help));
                } else {
                    print_failure("error", &format!("{err:#}"), None);
                }
            } else {
                eprintln!("error: {err:#}");
            }
            ExitCode::from(2)
        }
    }
}

#[cfg(test)]
mod parsing_tests {
    use super::*;
    use clap::error::ErrorKind;

    fn os(args: &[&str]) -> Vec<std::ffi::OsString> {
        args.iter().map(Into::into).collect()
    }

    /// What a parser made of a command line, level by level, apart from the global options
    /// (the full parser leaves its copies of them behind).
    fn understood(mut matches: &clap::ArgMatches) -> String {
        let mut out = String::new();
        loop {
            let mut ids: Vec<_> = matches.ids().map(|id| id.as_str()).filter(|id| !["overrides", "mcp_config"].contains(id)).collect();
            ids.sort_unstable();
            for id in ids {
                out.push_str(&format!("{id}={:?} ", matches.get_raw(id).map(Iterator::collect::<Vec<_>>)));
            }
            let Some((name, sub)) = matches.subcommand() else { return out };
            out.push_str(&format!("[{name}] "));
            matches = sub;
        }
    }

    #[test]
    fn a_command_line_without_global_options_parses_the_same_without_them() {
        let lines: &[&[&str]] = &[
            &["her", "status"],
            &["her", "status", "--json"],
            &["her", "doctor", "--json"],
            &["her", "sessions", "--all", "--limit", "3", "--json"],
            &["her", "models", "--provider", "deepseek", "--json"],
            &["her", "models", "unhide", "deepseek/deepseek-flash"],
            &["her", "models", "list", "--json"],
            &["her", "sessions", "list", "--all"],
            &["her", "skills", "list", "--json"],
            &["her", "mcp"],
            &["her", "mcp", "--json"],
            &["her", "skills", "--all"],
            &["her", "skills", "disable", "pdf"],
            &["her", "trust", "/tmp/project"],
            &["her", "exec", "hello"],
            &["her", "exec", "hello", "-m", "mock/bench", "--permission-mode", "manual", "--no-save"],
            &["her", "exec", "--resume", "last", "next"],
            &["her", "exec", "hello", "-m", "a/b", "-m", "c/d", "--json", "--output", "text"],
            &["her", "chat", "hello", "--thinking", "low"],
            &["her", "serve", "--listen", "unix://", "--idle-exit", "30"],
            &["her", "acp"],
            &["her", "doc-worker"],
            &["her", "deck-worker"],
            &["her", "mcp", "list", "--json"],
            &["her", "mcp", "add", "tools", "--", "npx", "--yes", "tools"],
            &["her", "browser", "native-host", "chrome-extension://abc/", "--parent-window=0"],
            &["her", "browser", "native-host", "--socket", "/tmp/her.sock", "chrome-extension://abc/"],
        ];
        for line in lines {
            assert!(parse_plain(&os(line)).is_some(), "{line:?}");
            let full = Cli::command().try_get_matches_from(*line).unwrap();
            let plain = parser_without_global_options().try_get_matches_from(*line).unwrap();
            assert_eq!(understood(&plain), understood(&full), "{line:?}");
        }
    }

    #[test]
    fn everything_else_goes_to_the_full_parser() {
        let lines: &[&[&str]] = &[
            // Global options, wherever they stand and however they are spelled.
            &["her", "-c", "thinking=low", "status"],
            &["her", "status", "-c", "thinking=low"],
            &["her", "status", "-cthinking=low"],
            &["her", "status", "--config=thinking=low"],
            &["her", "exec", "hello", "--mcp-config", "tools.json"],
            // A word that only looks like one: the full parser sorts it out, as it always did.
            &["her", "mcp", "add", "tools", "--", "sh", "-c", "serve"],
            // Help and version.
            &["her", "--help"],
            &["her", "-h"],
            &["her", "status", "--help"],
            &["her", "help", "status"],
            &["her", "--version"],
            &["her", "-V"],
            &["her", "-v"],
            // Mistakes: their messages list the global options.
            &["her"],
            &["her", "no-such-command"],
            &["her", "status", "--no-such-option"],
            &["her", "exec"],
            &["her", "exec", "hello", "--no-save", "--resume", "last"],
        ];
        for line in lines {
            assert!(parse_plain(&os(line)).is_none(), "{line:?}");
        }
    }

    #[test]
    fn help_expands_every_deferred_command() {
        fn visit(mut command: clap::Command, path: Vec<String>) {
            command.build();
            let mut args = path.clone();
            args.push("--help".into());
            let error = Cli::try_parse_from(args).err().expect("help exits the parser");
            assert_eq!(error.kind(), ErrorKind::DisplayHelp, "{path:?}: {error}");
            assert!(error.to_string().contains("Usage:"), "{path:?}: {error}");
            for child in command.get_subcommands().filter(|child| child.get_name() != "help") {
                let mut path = path.clone();
                path.push(child.get_name().into());
                visit(child.clone(), path);
            }
        }
        visit(Cli::command(), vec!["her".into()]);
    }

    #[test]
    fn deferred_arguments_keep_global_options_and_conflicts() {
        let cli = Cli::try_parse_from([
            "her", "-c", "thinking=low", "exec", "hello", "-m", "mock/bench",
            "--mcp-config", "tools.json", "--permission-mode", "manual",
        ]).unwrap();
        assert_eq!(cli.overrides, ["thinking=low"]);
        assert_eq!(cli.mcp_config, [PathBuf::from("tools.json")]);
        assert!(matches!(cli.command, Command::Exec { args, .. } if args.run.model.as_deref() == Some("mock/bench")));
        let error = Cli::try_parse_from(["her", "exec", "hello", "--no-save", "--resume", "last"])
            .err().expect("conflicting arguments");
        assert_eq!(error.kind(), ErrorKind::ArgumentConflict);
        let cli = Cli::try_parse_from(["her", "mcp", "get", "example", "-c", "thinking=low"]).unwrap();
        assert_eq!(cli.overrides, ["thinking=low"]);
        assert!(matches!(cli.command, Command::Mcp { command: Some(McpCommand::Get { name, .. }), .. } if name == "example"));
    }

    #[test]
    fn the_host_takes_its_socket_and_what_chrome_passes_after_it() {
        // The launcher's line, then what Chrome adds (on Windows also the parent window).
        let cli = Cli::try_parse_from(["her", "browser", "native-host", "--socket", "/s/her.sock", "chrome-extension://abc/", "--parent-window=0"]).unwrap();
        let Command::Browser { command: BrowserCommand::NativeHost { socket, args } } = cli.command else { panic!("not the host") };
        assert_eq!(socket, Some(PathBuf::from("/s/her.sock")));
        assert_eq!(args, ["chrome-extension://abc/", "--parent-window=0"]);
    }
}
