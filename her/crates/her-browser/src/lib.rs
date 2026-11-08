//! Browser control for her (docs/kernel/browser-design.md): a CDP client over a pipe, a
//! managed Chromium shared per profile, and per-session tabs with snapshots, refs, input and
//! the check that a call still runs on the page it was approved for. Independent of the
//! kernel; the tools and the approval policy live in her-core.

pub mod cdp;
pub mod input;
pub mod launch;
pub mod methods;
pub mod policy;
pub mod pool;
pub mod router;
pub mod session;
pub mod snapshot;

pub use cdp::CdpError;
pub use pool::Backend;
pub use pool::Browser;
pub use pool::BrowserConfig;
pub use pool::BrowserKind;
pub use pool::BrowserPool;
pub use pool::EmbeddedHost;
pub use pool::Headless;
pub use pool::Stream;
pub use router::NavigationGuard;
pub use session::BrowserSession;
pub use session::SessionOptions;

#[derive(Clone, Debug, thiserror::Error)]
pub enum BrowserError {
    #[error("{0}")]
    NotFound(String),
    #[error("cannot start the browser: {0}")]
    Launch(String),
    #[error(transparent)]
    Cdp(#[from] CdpError),
    /// The page is not the one the call was approved for; nothing was sent (§7.4).
    #[error(
        "The page changed after this call was approved (now {now}); nothing was sent. Take a snapshot and try again."
    )]
    PageChanged { now: String },
    /// A ref that no longer points at anything; the text says why.
    #[error("{0}")]
    StaleRef(String),
    #[error("{0}")]
    Failed(String),
}

impl BrowserError {
    pub fn failed(text: impl Into<String>) -> Self {
        Self::Failed(text.into())
    }
}
