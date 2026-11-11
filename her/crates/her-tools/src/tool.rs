//! The tool contract. Tools only declare what they are; approval, concurrency, truncation and
//! recording are the kernel's job.

use async_trait::async_trait;
use her_llm::ToolSpec;
use her_llm::UserContent;
use serde::Deserialize;
use serde::Serialize;
use serde_json::Value;
use std::any::Any;
use std::any::TypeId;
use std::collections::HashMap;
use std::fmt;
use std::path::PathBuf;
use std::sync::Arc;
use tokio_util::sync::CancellationToken;

use crate::env::Environment;
use crate::files::FileTracker;
use crate::files::PathLocks;
use crate::truncate::OutputLimits;

#[async_trait]
pub trait Tool: Send + Sync + 'static {
    /// Name, description and JSON Schema of the parameters.
    fn spec(&self) -> &ToolSpec;

    /// Read-only tools may run in parallel and skip approval by default; mutating tools run
    /// exclusively and go through the approval policy.
    fn kind(&self) -> ToolKind {
        ToolKind::ReadOnly
    }

    /// The kind of one call. Differs from [`Tool::kind`] only for tools that run another
    /// tool named in their arguments.
    fn kind_for(&self, args: &Value) -> ToolKind {
        let _ = args;
        self.kind()
    }

    /// Where the tool comes from. The kernel can judge what built-in tools do; it cannot for
    /// tools from outside, such as MCP servers.
    fn origin(&self) -> ToolOrigin {
        ToolOrigin::Builtin
    }

    /// Where one call's tool comes from. Differs from [`Tool::origin`] only for tools that run
    /// another tool named in their arguments.
    fn origin_for(&self, args: &Value) -> ToolOrigin {
        let _ = args;
        self.origin()
    }

    /// For a tool from outside: whether one call runs without asking in the modes that ask.
    fn runs_unasked(&self, args: &Value) -> bool {
        let _ = args;
        false
    }

    /// Lines added to the "tool guidelines" part of the system prompt.
    fn guidelines(&self) -> &[&'static str] {
        &[]
    }

    /// What a call does, for user interfaces. `args` may be invalid; describe what you can.
    fn describe(&self, args: &Value, env: &dyn Environment) -> ToolDescription {
        let _ = (args, env);
        ToolDescription::named(&self.spec().name)
    }

    /// Repairs well-known mistakes in the arguments, such as a list sent as a JSON string. The
    /// kernel calls it first; hooks, approval, events and `execute` see the result.
    fn prepare_arguments(&self, args: Value) -> Value {
        args
    }

    /// What the call will do on which site, judged from the session's current state
    /// (browser-design §7.1). The kernel computes it once, before the hooks; approval judges
    /// it, and `execute` gets the same list in [`ToolCtx::authorities`]. `Err` fails the call
    /// without asking anyone.
    async fn authorities(&self, args: &Value, ctx: &ToolCtx) -> Result<Vec<Authority>, ToolError> {
        let _ = (args, ctx);
        Ok(Vec::new())
    }

    /// Checks a call before the user is asked to approve it, so nobody approves a call that
    /// cannot succeed. Must not change anything.
    async fn check(&self, args: &Value, ctx: &ToolCtx) -> Result<(), ToolError> {
        let _ = (args, ctx);
        Ok(())
    }

    /// For a tool that a host outside the kernel runs: what one call would do, in the host's
    /// words. The approval policy asks for it only before it would ask the user about a call
    /// that changes something. `None` for every other tool, or when the host does not say.
    async fn approval_words(&self, args: &Value) -> Option<ApprovalWords> {
        let _ = args;
        None
    }

    async fn execute(&self, args: Value, ctx: ToolCtx) -> Result<ToolOutput, ToolError>;
}

/// What a tool's host says of one call, for the approval policy ([`Tool::approval_words`]).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ApprovalWords {
    /// What the call would do, for the user who approves it.
    pub summary: String,
    /// The words of the rule that covers the call, if one can: an allow rule with them lets it
    /// run unasked, and approving for the session keeps one.
    pub rule: Option<Vec<String>>,
}

/// Display category of a call; independent of [`ToolKind`], which only drives locking.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum ToolCategory {
    Read,
    Edit,
    Delete,
    Move,
    Search,
    Execute,
    Fetch,
    #[default]
    Other,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ToolDescription {
    pub category: ToolCategory,
    /// One line, e.g. the command or `read <path>`.
    pub title: String,
    /// Absolute paths the call reads or changes, with an optional 1-based line.
    pub locations: Vec<(PathBuf, Option<u32>)>,
}

impl ToolDescription {
    pub fn named(name: &str) -> Self {
        Self {
            title: name.to_string(),
            ..Self::default()
        }
    }

    /// `<verb> <path>` for a call whose `path` argument names one file.
    pub fn for_path(category: ToolCategory, verb: &str, args: &Value, env: &dyn Environment, line: Option<u32>) -> Self {
        match args.get("path").and_then(Value::as_str) {
            Some(raw) => Self {
                category,
                title: format!("{verb} {raw}"),
                locations: vec![(env.resolve(raw), line)],
            },
            None => Self {
                category,
                ..Self::named(verb)
            },
        }
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum ToolOrigin {
    /// Part of her, including kernel tools such as the agent tools.
    #[default]
    Builtin,
    /// Provided by an MCP server, or a kernel tool that runs MCP tools.
    Mcp,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ToolKind {
    ReadOnly,
    Mutating,
}

#[derive(Clone)]
pub struct ToolCtx {
    pub call_id: String,
    pub env: Arc<dyn Environment>,
    pub files: Arc<FileTracker>,
    /// Serializes changes to the same file across the sessions that share a workspace.
    pub locks: Arc<PathLocks>,
    pub limits: OutputLimits,
    pub progress: ProgressSink,
    pub cancel: CancellationToken,
    /// Whether the current model accepts image input.
    pub model_accepts_images: bool,
    /// Where the full output of a cut result is saved: the session's `tool-output` directory.
    pub output_dir: PathBuf,
    /// What the kernel's own tools keep for the calling session, such as its sub-agents or
    /// its browser. It lets one instance of such a tool serve every session. The tools here
    /// ignore it.
    pub session_state: Arc<SessionState>,
    /// What [`Tool::authorities`] returned when the call was approved; never recomputed, so
    /// the tool can check that the page is still the approved one (browser-design §7.4).
    pub authorities: Arc<[Authority]>,
}

/// Per-session values of the kernel's tools, one per type.
#[derive(Default)]
pub struct SessionState {
    slots: parking_lot::Mutex<HashMap<TypeId, Arc<dyn Any + Send + Sync>>>,
}

impl SessionState {
    pub fn get<T: Send + Sync + 'static>(&self) -> Option<Arc<T>> {
        let slot = self.slots.lock().get(&TypeId::of::<T>()).cloned()?;
        slot.downcast::<T>().ok()
    }

    pub fn get_or_init<T: Send + Sync + 'static>(&self, init: impl FnOnce() -> T) -> Arc<T> {
        let mut slots = self.slots.lock();
        let slot = slots
            .entry(TypeId::of::<T>())
            .or_insert_with(|| Arc::new(init()) as Arc<dyn Any + Send + Sync>)
            .clone();
        slot.downcast::<T>().expect("slots are keyed by their type")
    }

    pub fn insert<T: Send + Sync + 'static>(&self, value: Arc<T>) {
        self.slots.lock().insert(TypeId::of::<T>(), value);
    }

    pub fn remove<T: Send + Sync + 'static>(&self) -> Option<Arc<T>> {
        let slot = self.slots.lock().remove(&TypeId::of::<T>())?;
        slot.downcast::<T>().ok()
    }
}

impl fmt::Debug for SessionState {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "SessionState({} slots)", self.slots.lock().len())
    }
}

/// A page as a call was approved for: the tab, the frame and the document (browser-design
/// §7.1). Execution runs only on that page.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PageTarget {
    pub tab: String,
    pub frame: String,
    pub loader: String,
}

/// What one call does on which site. Origins are written as `https://example.com`. `own`: in
/// the user's own browser, with their real sign-ins, rather than the built-in one.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Authority {
    /// Opens a URL.
    Navigate { url: String, own: bool },
    /// Reads the page of an origin.
    Observe { origin: String },
    /// Acts on a page: clicks, types, selects. `action` says what, for the user.
    Act { origin: String, target: PageTarget, action: String, own: bool },
    /// Hands local files to a page.
    Upload { origin: String, paths: Vec<PathBuf>, target: PageTarget, own: bool },
    /// Runs a script on a page: it can read and change anything there.
    Script { origin: String, target: PageTarget, own: bool },
    /// Hands a page to the user for what only they can do: a sign-in, a CAPTCHA.
    UserStep { what: String, own: bool },
    /// Reads a desktop app's window. `app` is its bundle id, or its name when it has none.
    AppObserve { app: String },
    /// Acts on a desktop app. `action` says what, for the user.
    AppAct { app: String, action: String },
}

/// What a tool returns. `content` goes to the model, `details` to user interfaces and logs.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct ToolOutput {
    pub content: Vec<UserContent>,
    pub details: Option<Value>,
    pub is_error: bool,
    /// The task ends once this batch of calls is done, unless new input waits: the model said
    /// its last word (bot-design §3.3).
    pub ends_task: bool,
}

impl ToolOutput {
    pub fn text(text: impl Into<String>) -> Self {
        Self {
            content: vec![UserContent::Text { text: text.into() }],
            details: None,
            is_error: false,
            ends_task: false,
        }
    }

    pub fn error(text: impl Into<String>) -> Self {
        Self {
            is_error: true,
            ..Self::text(text)
        }
    }

    pub fn with_details(mut self, details: Value) -> Self {
        self.details = Some(details);
        self
    }

    pub fn ending_task(mut self) -> Self {
        self.ends_task = true;
        self
    }
}

#[derive(Debug, thiserror::Error)]
pub enum ToolError {
    #[error("invalid arguments: {0}")]
    InvalidArgs(String),
    #[error("{0}")]
    Failed(String),
    #[error("cancelled")]
    Cancelled,
    #[error("io error: {0}")]
    Io(#[from] std::io::Error),
}

type ProgressFn = dyn Fn(&str) + Send + Sync;

/// Live output of a running tool (for example command output), forwarded as events.
#[derive(Clone, Default)]
pub struct ProgressSink(Option<Arc<ProgressFn>>);

impl ProgressSink {
    pub fn new(f: impl Fn(&str) + Send + Sync + 'static) -> Self {
        Self(Some(Arc::new(f)))
    }

    pub fn noop() -> Self {
        Self(None)
    }

    pub fn emit(&self, text: &str) {
        if let Some(f) = &self.0
            && !text.is_empty()
        {
            f(text);
        }
    }
}

impl fmt::Debug for ProgressSink {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(if self.0.is_some() { "ProgressSink(active)" } else { "ProgressSink(noop)" })
    }
}
