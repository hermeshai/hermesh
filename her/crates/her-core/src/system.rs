//! The system connectors of a main session and its sub-agents: the browsers and desktop control
//! (docs/desktop/connectors-entry-design.md §9). Their settings change when the configuration
//! does; each session takes them up before its next model request (§9.4, D-224).

use parking_lot::Mutex;
use std::sync::Arc;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;

use crate::config::BrowserSettings;
use crate::config::Config;
use crate::config::DesktopSettings;
use crate::config::WebSearchSettings;

/// `[browser]`, `[desktop]` and `[web_search]`, as one value.
#[derive(Clone, Debug, PartialEq)]
pub struct SystemSettings {
    pub browser: BrowserSettings,
    pub desktop: DesktopSettings,
    pub web_search: WebSearchSettings,
}

impl SystemSettings {
    pub fn of(config: &Config) -> Self {
        Self {
            browser: config.browser.clone(),
            desktop: config.desktop.clone(),
            web_search: config.web_search.clone(),
        }
    }
}

/// The current settings, shared by a main session and its sub-agents, with a generation that
/// goes up with every change.
pub(crate) struct SystemCell {
    settings: Mutex<Arc<SystemSettings>>,
    generation: AtomicU64,
}

impl SystemCell {
    pub(crate) fn new(config: &Config) -> Arc<Self> {
        Arc::new(Self {
            settings: Mutex::new(Arc::new(SystemSettings::of(config))),
            generation: AtomicU64::new(0),
        })
    }

    pub(crate) fn get(&self) -> Arc<SystemSettings> {
        self.settings.lock().clone()
    }

    pub(crate) fn generation(&self) -> u64 {
        self.generation.load(Ordering::Acquire)
    }

    /// Returns whether they changed.
    pub(crate) fn set(&self, settings: SystemSettings) -> bool {
        let mut current = self.settings.lock();
        if **current == settings {
            return false;
        }
        *current = Arc::new(settings);
        self.generation.fetch_add(1, Ordering::AcqRel);
        true
    }
}

impl crate::session::Session {
    /// At a turn boundary, before the request (with the MCP refresh): takes up the browsers' and
    /// computer use's settings when they changed, or the first time. Their deferred tools, the
    /// handles and the policy follow; a browser turned off closes this session's tabs in it; the
    /// catalog in the prompt says which groups there are. A change is recorded, and costs one
    /// prompt-cache miss (connectors-entry-design §9.4).
    pub(crate) fn prepare_system(&self) {
        let generation = self.system.generation();
        if self.system_applied.swap(generation, Ordering::AcqRel) == generation {
            return;
        }
        let settings = self.system.get();
        let embedded = self.browser_pool.as_ref().is_some_and(|p| p.has_host());
        if let Some(handle) = self.tool_state.get::<crate::browser::BrowserHandle>() {
            for which in handle.update(settings.browser.clone(), embedded) {
                crate::browser::forget(&self.tool_state, which);
            }
        }
        let desktop = self.tool_state.get::<crate::desktop::DesktopHandle>();
        match (desktop, settings.desktop.on(embedded)) {
            (Some(handle), true) if *handle.settings() == settings.desktop => {}
            (_, true) => self.tool_state.insert(Arc::new(crate::desktop::DesktopHandle::new(
                settings.desktop.clone(),
                self.root_id.clone(),
            ))),
            (Some(_), false) => drop(self.tool_state.remove::<crate::desktop::DesktopHandle>()),
            (None, false) => {}
        }
        let docs = self.tool_state.get::<crate::docs::DocsHandle>().is_some();
        let connectors = self.tool_state.get::<crate::connect::ConnectorLink>().is_some();
        *self.deferred.lock() = Arc::new(crate::deferred::Deferred::of(&settings, self.browser_pool.is_some(), embedded, docs, self.agent_role, connectors));
        *self.base_tools.lock() = crate::mcp::base_tools(&self.core_tools, self.mcp.as_ref(), &self.deferred, &self.mcp_shown);
        if self.refresh_tools(true) {
            self.record_system_prompt();
        }
    }
}
