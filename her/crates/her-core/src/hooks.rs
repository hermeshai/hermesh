//! Extension points of the agent loop. The approval policy is itself a hook.

use async_trait::async_trait;
use her_llm::AssistantMessage;
use her_llm::Context;
use her_llm::ToolCall;
use her_protocol::CommandRule;
use her_protocol::PermissionMode;
use her_protocol::SiteGrant;
use her_tools::Authority;
use her_tools::Environment;
use her_tools::Tool;
use her_tools::ToolOutput;
use std::path::PathBuf;
use std::sync::Arc;

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Gate {
    Allow,
    Deny(String),
    Ask {
        summary: String,
        reason: Option<String>,
        /// Command prefix that approving for the session allows from then on.
        proposed_rule: Option<CommandRule>,
        /// Mode that approving for the session switches to. Without this, a rule or a
        /// grant, an approval covers this call only.
        proposed_mode: Option<PermissionMode>,
        /// Site permissions that approving for the session may keep, one of them.
        proposed_grants: Vec<SiteGrant>,
        /// The call works in the user's own browser (connectors-entry-design §3.4).
        own_browser: bool,
    },
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TurnDecision {
    /// Normal scheduling: continue when there are tool results or queued input.
    Default,
    /// Run one more turn.
    Continue,
    /// End the task now.
    End,
}

/// Read-only snapshot of session state for hooks.
#[derive(Clone)]
pub struct SessionView {
    pub permission_mode: PermissionMode,
    pub config_rules: Vec<CommandRule>,
    pub session_rules: Vec<CommandRule>,
    pub trusted: bool,
    pub env: Arc<dyn Environment>,
    /// her's own data directory (canonical), which is protected like `.git`.
    pub data_dir: PathBuf,
    /// The session's own folder inside the data directory (canonical), which is not: a
    /// session's without a project, or a bot's (bot-design §2.5).
    pub own_folder: Option<PathBuf>,
    /// Configured skill paths (canonical). What is written there becomes instructions for
    /// later sessions, so they are protected like `.agents`.
    pub skill_roots: Arc<[PathBuf]>,
    /// A bot's main session: its memory (canonical), which it alone writes, without asking
    /// (docs/her-memory.md §6). `None` in every other session, the bot's sub-agents and its
    /// routines' runs too.
    pub memory_dir: Option<PathBuf>,
    /// Site permissions given for the session (the main session's, in a sub-agent).
    pub site_grants: Vec<SiteGrant>,
    /// The `[browser]` policy: domain lists and configured sites.
    pub browser: Arc<her_browser::policy::SitePolicy>,
    /// The `[desktop]` settings: which apps are allowed or denied without asking.
    pub desktop: crate::config::DesktopSettings,
}

#[async_trait]
pub trait Hooks: Send + Sync + 'static {
    /// Adjust the request context (inject reminders, trim). Must not fail.
    fn transform_context(&self, _ctx: &mut Context) {}

    /// Allow, deny, or ask before a tool runs. The first non-`Allow` answer wins.
    /// `authorities` is what the call does on which site (`Tool::authorities`).
    async fn before_tool(&self, _call: &ToolCall, _tool: &dyn Tool, _authorities: &[Authority], _view: &SessionView) -> Gate {
        Gate::Allow
    }

    /// Adjust a tool result.
    async fn after_tool(&self, _call: &ToolCall, output: ToolOutput) -> ToolOutput {
        output
    }

    /// Decide after each turn. The first non-`Default` answer wins.
    async fn finish_turn(&self, _message: &AssistantMessage) -> TurnDecision {
        TurnDecision::Default
    }
}
