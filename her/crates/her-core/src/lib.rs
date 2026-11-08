//! Kernel runtime: sessions, the agent loop, approvals, context assembly and the session log.
//!
//! Front ends drive a [`Kernel`] with [`her_protocol::Op`]s and consume
//! [`her_protocol::Event`]s; the kernel never prints or reads the terminal.

mod agents;
pub mod approval;
pub mod bot;
mod browser;
pub mod browser_install;
mod budget;
pub mod commands;
mod compaction;
pub mod config;
pub mod config_edit;
pub mod connectors;
pub mod context;
pub mod context_report;
mod deck;
mod deferred;
mod desktop;
mod docs;
mod emitter;
pub mod files;
pub mod pptx;
pub mod history;
pub mod hooks;
pub mod instructions;
pub mod kernel;
pub mod lang;
pub mod mcp;
pub mod models;
pub mod paths;
pub mod prompt;
pub mod release;
mod protected;
pub mod routine;
pub mod rules;
pub mod runtime;
mod session;
mod site_policy;
pub mod skills;
pub mod system;
pub mod store;
pub mod trash;
pub mod web_search;
mod word;
mod task;
pub mod tool_list;
mod tool_exec;

pub use agents::RestoredAgent;
pub use bot::BotIdentity;
pub use config::Config;
pub use config::ConfigError;
pub use config::LoadedConfig;
pub use hooks::Gate;
pub use hooks::Hooks;
pub use hooks::SessionView;
pub use hooks::TurnDecision;
pub use kernel::Kernel;
pub use kernel::KernelClosed;
pub use kernel::KernelDeps;
pub use kernel::KernelError;
pub use kernel::ResumeOptions;
pub use kernel::StartOptions;
pub use kernel::TaskEnd;
pub use kernel::TaskResult;
pub use paths::HerHome;
pub use runtime::Workspace;
pub use session::MAX_TITLE_CHARS;
pub use system::SystemSettings;
pub use session::clean_title;
pub use store::JsonlStore;
pub use store::MemoryStore;
pub use store::SessionRef;
pub use store::SessionStore;
pub use store::short_id;

pub use her_browser::BrowserPool;

/// Opens an address in the person's browser, unless `HER_NO_BROWSER` says there is none. Returns
/// false when it could not.
pub fn open_in_browser(url: &str) -> bool {
    if std::env::var_os("HER_NO_BROWSER").is_some() {
        return true;
    }
    let opened = if cfg!(target_os = "macos") {
        std::process::Command::new("open").arg(url).status()
    } else if cfg!(windows) {
        std::process::Command::new("cmd").args(["/C", "start", "", url]).status()
    } else {
        std::process::Command::new("xdg-open").arg(url).status()
    };
    opened.is_ok()
}
