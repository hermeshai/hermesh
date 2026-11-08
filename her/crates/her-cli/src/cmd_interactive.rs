//! The interactive mode (standalone-cli-design §5): `her`, `her "PROMPT"`, `her resume`.
//!
//! Three things drive it: keys from a thread of their own, the kernel's events, and a timer that
//! gathers changes into one redraw every 33 ms. Running input goes to the kernel's own queue.

use anyhow::Context as _;
use crossterm::event::Event as TermEvent;
use crossterm::event::KeyCode;
use crossterm::event::KeyEvent;
use crossterm::event::KeyEventKind;
use crossterm::event::KeyModifiers;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::ResumeOptions;
use her_core::SessionRef;
use her_core::SessionStore;
use her_core::StartOptions;
use her_core::instructions;
use her_core::release;
use her_llm::Message;
use her_llm::ThinkingLevel;
use her_llm::UsageTotals;
use her_llm::UserContent;
use her_protocol::ApprovalDecision;
use her_protocol::CommandRule;
use her_protocol::Delta;
use her_protocol::EventMsg;
use her_protocol::Op;
use her_protocol::PermissionMode;
use her_protocol::PromptMode;
use her_protocol::QueuedInput;
use her_protocol::SiteGrant;
use her_protocol::api::TranscriptItem;
use serde_json::Value;
use std::io::IsTerminal;
use std::io::Write;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::Ordering;
use std::time::Duration;
use std::time::Instant;
use tokio::sync::mpsc;

use crate::RunArgs;
use crate::approval_prompt;
use crate::approval_prompt::Answer;
use crate::approval_prompt::Question;
use crate::render;
use crate::render::Style;
use crate::setup;
use crate::term;
use crate::term::editor::Editor;
use crate::term::live;
use crate::term::live::Live;
use crate::term::markdown::Formatter;

const REDRAW: Duration = Duration::from_millis(33);
const SPINNER: [&str; 10] = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
/// Two presses of Ctrl-C within this quit.
const AGAIN: Duration = Duration::from_secs(2);
const COMMANDS: &[&str] = &[
    "/help", "/compact", "/context", "/model", "/thinking", "/permissions", "/new", "/resume", "/rewind", "/rename", "/status", "/tools", "/mcp",
    "/skills", "/login", "/quit", "/exit",
];

const HELP: &str = "\
Enter sends; while a task runs it joins at the next step. Tab, with text: send after the task.
Alt+Enter or Ctrl+J: new line. Up: history, or take back what waits. Ctrl+C: stop the task.
/model [ID]  /thinking [LEVEL]  /permissions [MODE]  /new  /resume [ID]  /rewind  /rename TEXT
/compact  /context  /status  /tools  /mcp  /skills  /login  /quit";

/// How the mode was started.
pub struct Start {
    pub prompt: Option<String>,
    pub cwd: Option<PathBuf>,
    pub resume: Option<String>,
    pub run: RunArgs,
}

/// A local command, or nothing when the text is to be sent. Only these exact words count, so that
/// `/etc/hosts is what?` goes to the model (§5.6).
#[derive(Debug, PartialEq, Eq)]
enum Slash<'a> {
    Help,
    Model(Option<&'a str>),
    Thinking(Option<&'a str>),
    Permissions(Option<&'a str>),
    New,
    Resume(Option<&'a str>),
    Rewind,
    Rename(&'a str),
    Status,
    Tools,
    Mcp,
    Skills,
    Login,
    Quit,
}

fn parse_slash(text: &str) -> Option<Slash<'_>> {
    let text = text.trim();
    let (word, rest) = text.split_once(char::is_whitespace).map_or((text, ""), |(w, r)| (w, r.trim()));
    let arg = (!rest.is_empty()).then_some(rest);
    let plain = |slash: Slash<'static>| rest.is_empty().then_some(slash);
    match word {
        "/help" => plain(Slash::Help),
        "/model" => Some(Slash::Model(arg)),
        "/thinking" => Some(Slash::Thinking(arg)),
        "/permissions" => Some(Slash::Permissions(arg)),
        "/new" => plain(Slash::New),
        "/resume" => Some(Slash::Resume(arg)),
        "/rewind" => plain(Slash::Rewind),
        "/rename" => Some(Slash::Rename(rest)),
        "/status" => plain(Slash::Status),
        "/tools" => plain(Slash::Tools),
        "/mcp" => plain(Slash::Mcp),
        "/skills" => plain(Slash::Skills),
        "/login" => plain(Slash::Login),
        "/quit" | "/exit" => plain(Slash::Quit),
        _ => None,
    }
}

/// One approval waiting for its answer.
struct Asking {
    agent_id: Option<String>,
    agent_label: Option<String>,
    call_id: String,
    name: String,
    args: Value,
    summary: String,
    reason: Option<String>,
    rule: Option<CommandRule>,
    mode: Option<PermissionMode>,
    grants: Vec<SiteGrant>,
    answer: Editor,
    /// `n` was answered: the words for the model come now.
    feedback: bool,
    hint: Option<&'static str>,
    /// What the question folded away, until `v`.
    folded: Vec<String>,
}

impl Asking {
    fn question(&self) -> Question<'_> {
        Question {
            agent: self.agent_label.as_deref(),
            name: &self.name,
            summary: &self.summary,
            reason: self.reason.as_deref(),
            rule: self.rule.as_ref(),
            mode: self.mode,
            grants: &self.grants,
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Phase {
    Replying,
    Thinking,
    Tool,
}

struct App {
    overrides: Vec<String>,
    style: Style,
    show_thinking: bool,
    kernel: Kernel,
    cwd: PathBuf,
    editor: Editor,
    live: Live,
    /// Finished lines not printed yet.
    finished: Vec<String>,
    /// The reply's line still being written.
    partial: String,
    markdown: Formatter,
    /// A task has been running since then.
    running: Option<Instant>,
    phase: Phase,
    progress: Vec<String>,
    queue: Vec<QueuedInput>,
    asking: Vec<Asking>,
    agents: Vec<(String, String)>,
    model: String,
    context_percent: Option<u64>,
    task_tokens: u64,
    usage: UsageTotals,
    /// The previous Ctrl-C, for the second one.
    last_interrupt: Option<Instant>,
    /// `/rewind`: the messages to pick from, then the one being replaced.
    choosing: Vec<(String, String)>,
    replacing: Option<String>,
    history_file: Option<PathBuf>,
    dirty: bool,
    frame: usize,
    quit: bool,
    keys_paused: Arc<AtomicBool>,
    raw: Option<term::Raw>,
    trusted: bool,
}

/// Some terminals report no size at all (a bare pty, some CI runners): the usual one then.
fn terminal_size() -> (usize, usize) {
    match crossterm::terminal::size() {
        Ok((columns, rows)) if columns > 0 && rows > 0 => (usize::from(columns), usize::from(rows)),
        _ => (80, 24),
    }
}

/// Keys, on a thread of their own. It lets go of stdin while something else reads it.
fn spawn_keys(paused: Arc<AtomicBool>) -> mpsc::Receiver<TermEvent> {
    let (tx, rx) = mpsc::channel(256);
    std::thread::spawn(move || {
        loop {
            if paused.load(Ordering::Acquire) {
                std::thread::sleep(Duration::from_millis(20));
                continue;
            }
            match crossterm::event::poll(Duration::from_millis(100)) {
                Ok(true) => match crossterm::event::read() {
                    Ok(event) => {
                        if tx.blocking_send(event).is_err() {
                            return;
                        }
                    }
                    Err(_) => return,
                },
                Ok(false) => {}
                Err(_) => return,
            }
        }
    });
    rx
}

pub async fn interactive(overrides: &[String], start: Start) -> anyhow::Result<u8> {
    if !std::io::stdin().is_terminal() || !std::io::stdout().is_terminal() || std::env::var_os("TERM").is_some_and(|term| term == "dumb") {
        match &start.resume {
            Some(session) => anyhow::bail!("the interactive mode needs a terminal; in a script, use: her exec --resume {session} \"PROMPT\""),
            None => anyhow::bail!("the interactive mode needs a terminal; in a script, use: her exec \"PROMPT\""),
        }
    }
    let Start { prompt, cwd, resume, run } = start;
    let all_overrides = crate::cmd_exec::with_run_overrides(overrides, &run);
    let found = match &resume {
        Some(session) => Some(find_session(session)?),
        None => None,
    };
    let cwd = match (&found, cwd) {
        (Some((header, _)), _) => header.clone(),
        (None, Some(cwd)) => cwd,
        (None, None) => std::env::current_dir()?,
    };
    ask_about_trust(&cwd)?;
    let mut rt = setup::init(cwd.clone(), &all_overrides)?;
    rt.warnings.extend(run.warnings());
    let resuming = found.is_some();
    let kernel = match found {
        Some((_, path)) => {
            let kernel = resume_kernel(&rt, &path, &run).await?;
            setup::check_credentials(&rt, &kernel.info().model)?;
            kernel
        }
        None => {
            // Nothing to run on yet: set a model up first (§7.3).
            let model = match setup::session_model(&rt, run.model.clone()) {
                Ok(model) if setup::check_credentials(&rt, &model).is_ok() => model,
                _ => {
                    eprintln!("{}", her_core::lang::say("No model is set up yet. Let's connect one.", "还没有可用的模型，先接一个。"));
                    crate::cmd_login::login(overrides, Default::default()).await?;
                    rt = setup::init(cwd.clone(), &all_overrides)?;
                    let model = setup::session_model(&rt, run.model.clone())?;
                    setup::check_credentials(&rt, &model)?;
                    model
                }
            };
            start_kernel(&rt, model, &run).await?
        }
    };
    let home = setup::home()?;
    let history_file = rt.config.history_enabled.then(|| home.root().join("history.jsonl"));
    let project = setup::workspace(&rt.cwd);
    let history = history_file.as_deref().map(|file| load_history(file, &project)).unwrap_or_default();
    let keys_paused = Arc::new(AtomicBool::new(false));
    let mut app = App {
        overrides: all_overrides,
        style: Style::detect(),
        show_thinking: rt.config.show_thinking,
        cwd: rt.cwd.clone(),
        model: kernel.info().model,
        kernel,
        editor: Editor::with_history(history),
        live: Live::default(),
        finished: Vec::new(),
        partial: String::new(),
        markdown: Formatter::default(),
        running: None,
        phase: Phase::Replying,
        progress: Vec::new(),
        queue: Vec::new(),
        asking: Vec::new(),
        agents: Vec::new(),
        context_percent: None,
        task_tokens: 0,
        usage: UsageTotals::default(),
        last_interrupt: None,
        choosing: Vec::new(),
        replacing: None,
        history_file,
        dirty: true,
        frame: 0,
        quit: false,
        keys_paused: keys_paused.clone(),
        raw: Some(term::Raw::enter().context("cannot set up the terminal")?),
        trusted: rt.trusted,
    };
    app.banner(&rt, &run);
    if resuming {
        app.print_recent();
    }
    for warning in &rt.warnings {
        let line = app.style.yellow(&format!("warning: {warning}"));
        app.finished.push(line);
    }
    let update = rt.config.update_check.then(|| tokio::spawn(update_notice(home.clone())));
    let code = app.run(prompt, keys_paused, update).await;
    app.leave().await;
    code
}

async fn start_kernel(rt: &setup::Runtime, model: String, run: &RunArgs) -> anyhow::Result<Kernel> {
    let deps = rt.kernel_deps(true, &run.add_dir)?;
    let options = StartOptions {
        model,
        thinking: run.thinking.unwrap_or(rt.config.thinking),
        permission_mode: run.permission_mode().unwrap_or(rt.config.permission_mode),
        cwd: rt.cwd.clone(),
        home: rt.home.clone(),
        config: Arc::new(rt.config.clone()),
        project_root: rt.project_root.clone(),
        trusted: rt.trusted,
        interactive_approvals: true,
        bot: None,
    };
    Ok(Kernel::start(options, deps).await?)
}

async fn resume_kernel(rt: &setup::Runtime, path: &Path, run: &RunArgs) -> anyhow::Result<Kernel> {
    let deps = rt.kernel_deps(true, &run.add_dir)?;
    let options = ResumeOptions {
        session: SessionRef::Path(path.to_path_buf()),
        model: run.model.clone(),
        thinking: run.thinking,
        permission_mode: run.permission_mode(),
        home: rt.home.clone(),
        config: Arc::new(rt.config.clone()),
        project_root: rt.project_root.clone(),
        trusted: rt.trusted,
        interactive_approvals: true,
        bot_name: None,
    };
    Ok(Kernel::resume(options, deps).await?)
}

/// `last` or an id: where the session ran, and its log.
fn find_session(session: &str) -> anyhow::Result<(PathBuf, PathBuf)> {
    let home = setup::home()?;
    let store = JsonlStore::new(home.sessions_dir());
    let (header, path) = match session {
        "last" => {
            let workspace = setup::workspace(&std::env::current_dir()?);
            store
                .header(&SessionRef::LastIn(workspace))
                .context("nothing to continue in this project (`her sessions --all` lists every project)")?
        }
        id => store.header(&SessionRef::Id(id.to_string()))?,
    };
    Ok((header.cwd, path))
}

/// Asks once, only when the project really brings something to trust (§12.2).
fn ask_about_trust(cwd: &Path) -> anyhow::Result<()> {
    let home = setup::home()?;
    let Ok(cwd) = std::fs::canonicalize(cwd) else { return Ok(()) };
    let root = instructions::find_project_root(&cwd, &[".git".to_string()]).unwrap_or(cwd);
    if instructions::is_trusted(&home, &root) || instructions::was_declined(&home, &root) {
        return Ok(());
    }
    let brings = her_core::runtime::what_trust_brings(&root);
    if brings.is_empty() {
        return Ok(());
    }
    let say = her_core::lang::say;
    println!("{}", say("This project brings its own configuration:", "这个项目带了配置："));
    for line in &brings {
        println!("  {line}");
    }
    print!("{} [y/N] ", say("Trust it? These programs would then run on your computer.", "信任它们吗？信任后这些程序会在你的电脑上运行。"));
    std::io::stdout().flush()?;
    let mut line = String::new();
    std::io::stdin().read_line(&mut line)?;
    if matches!(line.trim().to_lowercase().as_str(), "y" | "yes") {
        instructions::trust(&home, &root)?;
    } else {
        instructions::decline(&home, &root)?;
    }
    Ok(())
}

fn load_history(file: &Path, project: &Path) -> Vec<String> {
    let Ok(text) = std::fs::read_to_string(file) else { return Vec::new() };
    text.lines()
        .filter_map(|line| serde_json::from_str::<Value>(line).ok())
        .filter(|entry| entry.get("project").and_then(Value::as_str).is_some_and(|p| Path::new(p) == project))
        .filter_map(|entry| entry.get("text").and_then(Value::as_str).map(str::to_string))
        .collect()
}

fn save_history(file: &Path, project: &Path, text: &str) {
    use std::os::unix::fs::OpenOptionsExt;
    let line = serde_json::json!({ "project": project, "text": text }).to_string();
    let opened = std::fs::OpenOptions::new().create(true).append(true).mode(0o600).open(file);
    if let Ok(mut opened) = opened {
        let _ = writeln!(opened, "{line}");
    }
}

/// At most once a day; quiet when it fails. The result of the last look is shown at once, so the
/// line does not depend on the network this time.
async fn update_notice(home: her_core::paths::HerHome) -> Option<String> {
    let file = home.root().join("run").join("update-check.json");
    let saved: Option<Value> = std::fs::read(&file).ok().and_then(|bytes| serde_json::from_slice(&bytes).ok());
    let now = chrono::Utc::now().timestamp();
    let fresh = saved.as_ref().and_then(|s| s.get("checked_at")).and_then(Value::as_i64).is_some_and(|at| now - at < 86_400);
    let known = saved.as_ref().and_then(|s| s.get("latest")).and_then(Value::as_str).map(str::to_string);
    let latest = if fresh {
        known
    } else {
        let manifest = release::fetch_manifest(None).await.ok()?;
        let _ = std::fs::create_dir_all(file.parent()?);
        let _ = std::fs::write(&file, serde_json::json!({ "checked_at": now, "latest": manifest.version }).to_string());
        Some(manifest.version)
    };
    let latest = latest?;
    release::is_newer(&latest, release::VERSION).then(|| match her_core::lang::current() {
        her_core::lang::Lang::Zh => format!("her {latest} 可用，运行 her upgrade"),
        _ => format!("her {latest} is available: run her upgrade"),
    })
}

fn user_text(input: &[UserContent]) -> String {
    let mut text = String::new();
    for part in input {
        if let UserContent::Text { text: part } = part {
            text.push_str(part);
        }
    }
    text
}

impl App {
    fn banner(&mut self, rt: &setup::Runtime, run: &RunArgs) {
        let home = setup::home().ok();
        let shown = home.map_or_else(|| self.cwd.display().to_string(), |home| crate::cmd_customize::shorten(&home, &self.cwd));
        let mode = run.permission_mode().unwrap_or(rt.config.permission_mode);
        let line = format!("her {} · {} · {mode} · {shown}", release::VERSION, self.model);
        self.finished.push(self.style.dim(&line));
    }

    async fn run(
        &mut self,
        prompt: Option<String>,
        keys_paused: Arc<AtomicBool>,
        mut update: Option<tokio::task::JoinHandle<Option<String>>>,
    ) -> anyhow::Result<u8> {
        let mut keys = spawn_keys(keys_paused);
        let mut tick = tokio::time::interval(REDRAW);
        tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
        let mut term_signal = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())?;
        let mut hangup = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::hangup())?;
        if let Some(prompt) = prompt {
            self.send(&prompt, PromptMode::Steer).await;
        }
        while !self.quit {
            tokio::select! {
                event = self.kernel.next_event() => match event {
                    Some(event) => self.on_event(&event.msg),
                    None => {
                        self.quit = true;
                    }
                },
                key = keys.recv() => match key {
                    Some(key) => self.on_terminal(key).await?,
                    None => self.quit = true,
                },
                _ = tick.tick() => {
                    if let Some(handle) = &mut update
                        && handle.is_finished()
                    {
                        if let Ok(Some(notice)) = handle.await {
                            self.finished.push(self.style.dim(&notice));
                            self.dirty = true;
                        }
                        update = None;
                    }
                    if let Some(started) = self.running {
                        let frame = (started.elapsed().as_millis() / 100) as usize;
                        if frame != self.frame {
                            self.frame = frame;
                            self.dirty = true;
                        }
                    }
                    if self.dirty {
                        self.draw();
                    }
                },
                _ = term_signal.recv() => self.quit = true,
                _ = hangup.recv() => self.quit = true,
            }
        }
        Ok(0)
    }

    /// Finishes up: the terminal comes back, then what it took and how to continue.
    async fn leave(mut self) {
        self.draw();
        let _ = self.live.clear(&mut std::io::stdout());
        drop(self.raw.take());
        let id = self.kernel.session_id().to_string();
        let saved = self.kernel.session_file().is_file();
        let _ = self.kernel.shutdown().await;
        if self.usage.tokens().input + self.usage.tokens().output > 0 {
            eprintln!("{}", self.style.dim(&render::usage_line(&self.usage)));
        }
        if saved {
            let short = her_core::short_id(&id);
            eprintln!("{}", self.style.dim(&format!("session {short} · continue with: her resume {short}")));
        }
    }

    fn draw(&mut self) {
        self.dirty = false;
        let size = terminal_size();
        let mut lines = Vec::new();
        if !self.partial.is_empty() {
            lines.push(self.partial.clone());
        }
        for line in &self.progress {
            lines.push(self.style.dim(&format!("  {line}")));
        }
        if let Some(status) = self.status_line() {
            lines.push(status);
        }
        for queued in &self.queue {
            let text = render::truncate(&user_text(&queued.message.content), size.0.saturating_sub(24).max(16));
            let label = match queued.mode {
                PromptMode::Steer => her_core::lang::say("next step", "下一步送达"),
                PromptMode::FollowUp => her_core::lang::say("after this task", "任务结束后"),
            };
            lines.push(self.style.dim(&format!("↳ {label}: {text}")));
        }
        let cursor = match self.asking.first() {
            Some(asking) => {
                for line in approval_prompt::lines(&self.style, &asking.question()) {
                    lines.push(line);
                }
                if let Some(hint) = asking.hint {
                    lines.push(format!("  {hint}"));
                }
                let prefix = if asking.feedback { her_core::lang::say("feedback (optional): ", "给模型的说明（可不填）：") } else { "> " };
                let (_, column) = asking.answer.cursor_position();
                lines.push(format!("{prefix}{}", asking.answer.text()));
                (lines.len() - 1, live::width(prefix) + column)
            }
            None => {
                if self.replacing.is_some() {
                    lines.push(self.style.dim(her_core::lang::say(
                        "Sending replaces that message and what came after it. Ctrl+C keeps them.",
                        "发送后，这条消息和它之后的内容会被替换。Ctrl+C 取消。",
                    )));
                } else if !self.choosing.is_empty() {
                    lines.push(self.style.dim(her_core::lang::say("A number picks the message. Ctrl+C keeps them all.", "输入序号选择消息。Ctrl+C 取消。")));
                }
                let first = lines.len();
                let (line, column) = self.editor.cursor_position();
                for (index, text) in self.editor.lines().into_iter().enumerate() {
                    lines.push(format!("{}{text}", if index == 0 { "› " } else { "  " }));
                }
                (first + line, column + 2)
            }
        };
        let finished = std::mem::take(&mut self.finished);
        let _ = self.live.draw(&mut std::io::stdout(), &finished, &lines, cursor, size);
    }

    fn status_line(&self) -> Option<String> {
        let started = self.running?;
        let say = her_core::lang::say;
        let what = if !self.asking.is_empty() {
            say("waiting for you", "等你回答")
        } else {
            match self.phase {
                Phase::Replying => say("replying", "正在回复"),
                Phase::Thinking => say("thinking", "正在思考"),
                Phase::Tool => say("running a tool", "正在运行工具"),
            }
        };
        let mut line = format!("{} {what} · {}s", SPINNER[self.frame % SPINNER.len()], started.elapsed().as_secs());
        if self.task_tokens > 0 {
            line.push_str(&format!(" · {} tokens", self.task_tokens));
        }
        if let Some(percent) = self.context_percent {
            line.push_str(&match her_core::lang::current() {
                her_core::lang::Lang::Zh => format!(" · 上下文 {percent}%"),
                _ => format!(" · context {percent}%"),
            });
        }
        Some(self.style.dim(&line))
    }

    fn print(&mut self, line: impl Into<String>) {
        self.finished.push(line.into());
        self.dirty = true;
    }

    /// Takes the reply as it comes: a line that is whole goes into the scrollback.
    fn reply_text(&mut self, text: &str) {
        let mut rest = text;
        while let Some(end) = rest.find('\n') {
            self.partial.push_str(&rest[..end]);
            let line = std::mem::take(&mut self.partial);
            let formatted = self.markdown.line(&self.style, &line);
            self.finished.push(formatted);
            rest = &rest[end + 1..];
        }
        self.partial.push_str(rest);
        self.dirty = true;
    }

    fn end_reply(&mut self) {
        if !self.partial.is_empty() {
            let line = std::mem::take(&mut self.partial);
            let formatted = self.markdown.line(&self.style, &line);
            self.finished.push(formatted);
        }
        self.markdown = Formatter::default();
    }

    fn on_event(&mut self, msg: &EventMsg) {
        self.dirty = true;
        match msg {
            EventMsg::TaskStarted { .. } => {
                self.running = Some(Instant::now());
                self.task_tokens = 0;
                self.phase = Phase::Thinking;
            }
            EventMsg::SessionStarted { model, resumed, .. } => {
                self.model.clone_from(model);
                if *resumed {
                    self.print_lines(msg);
                }
            }
            EventMsg::SettingsChanged { model, .. } => {
                if self.model != *model {
                    self.model.clone_from(model);
                    self.print(self.style.dim(&format!("model: {model}")));
                }
            }
            EventMsg::MessageDelta { delta: Delta::Text { text, .. }, .. } => {
                self.phase = Phase::Replying;
                self.reply_text(text);
            }
            EventMsg::MessageDelta { delta: Delta::Thinking { text, .. }, .. } => {
                self.phase = Phase::Thinking;
                if self.show_thinking {
                    let lines: Vec<String> = text.lines().map(|line| self.style.dim(line)).collect();
                    self.finished.extend(lines);
                }
            }
            EventMsg::MessageCompleted { message: Message::Assistant(_), .. } => self.end_reply(),
            EventMsg::ToolStarted { .. } => {
                self.end_reply();
                self.phase = Phase::Tool;
                self.progress.clear();
                self.print_lines(msg);
            }
            EventMsg::ToolProgress { text, .. } => {
                for line in text.lines() {
                    self.progress.push(render::truncate(line, terminal_size().0.saturating_sub(4).max(16)));
                }
                let extra = self.progress.len().saturating_sub(3);
                self.progress.drain(..extra);
            }
            EventMsg::ToolCompleted { .. } => {
                self.progress.clear();
                self.print_lines(msg);
            }
            EventMsg::TurnCompleted { usage, .. } => {
                self.task_tokens += usage.input + usage.output + usage.cache_read + usage.cache_write;
                self.context_percent = self.kernel.context().map(|report| report.usage.tokens * 100 / report.usage.window.max(1));
            }
            EventMsg::QueueChanged { queued } => self.queue.clone_from(queued),
            EventMsg::ContextReported { report } => {
                self.end_reply();
                for line in her_core::context_report::text(report).lines() {
                    self.finished.push(line.to_string());
                }
            }
            EventMsg::ApprovalRequested { .. } => self.on_approval(None, msg),
            EventMsg::AgentEvent { agent_id, event } => match &event.msg {
                EventMsg::ApprovalRequested { .. } => self.on_approval(Some(agent_id.clone()), &event.msg),
                other => {
                    for line in render::status_lines(&self.style, other).into_iter().filter(|_| matches!(other, EventMsg::ApprovalRefused { .. })) {
                        self.finished.push(line);
                    }
                }
            },
            EventMsg::AgentStarted { agent_id, name, description, .. } => {
                let label = if description.is_empty() { name.clone() } else { description.clone() };
                self.agents.push((agent_id.clone(), label));
                self.print_lines(msg);
            }
            EventMsg::TaskCompleted { usage, .. } | EventMsg::TaskFailed { usage, .. } | EventMsg::TaskInterrupted { usage, .. } => {
                self.end_reply();
                self.usage.merge(usage);
                self.running = None;
                self.progress.clear();
                self.print_lines(msg);
            }
            EventMsg::Idle => self.running = None,
            EventMsg::SessionClosed => self.quit = true,
            other => {
                if !render::status_lines(&self.style, other).is_empty() {
                    self.end_reply();
                    self.print_lines(other);
                }
            }
        }
    }

    fn print_lines(&mut self, msg: &EventMsg) {
        let lines = render::status_lines(&self.style, msg);
        self.finished.extend(lines);
    }

    fn on_approval(&mut self, agent_id: Option<String>, msg: &EventMsg) {
        let EventMsg::ApprovalRequested { call_id, name, args, summary, reason, proposed_rule, proposed_mode, proposed_grants, .. } = msg else {
            return;
        };
        self.end_reply();
        let agent_label = agent_id.as_ref().and_then(|id| self.agents.iter().find(|(known, _)| known == id)).map(|(_, label)| label.clone());
        let details = approval_prompt::details(&self.style, name, args);
        // Most of the screen, and the question below it stays in view.
        let room = terminal_size().1.saturating_sub(10).clamp(4, approval_prompt::FOLD_LINES);
        let (shown, folded) = if details.len() > room { (details[..room].to_vec(), details[room..].to_vec()) } else { (details, Vec::new()) };
        self.finished.extend(shown);
        if !folded.is_empty() {
            self.finished.push(self.style.dim(&format!("  … {} more line(s): v shows them", folded.len())));
        }
        self.asking.push(Asking {
            agent_id,
            agent_label,
            call_id: call_id.clone(),
            name: name.clone(),
            args: args.clone(),
            summary: summary.clone(),
            reason: reason.clone(),
            rule: proposed_rule.clone(),
            mode: *proposed_mode,
            grants: proposed_grants.clone(),
            answer: Editor::default(),
            feedback: false,
            hint: None,
            folded,
        });
    }

    async fn decide(&mut self, decision: ApprovalDecision) {
        let asking = self.asking.remove(0);
        let word = match &decision {
            ApprovalDecision::Approve | ApprovalDecision::ApproveForSession { .. } => her_core::lang::say("allowed", "已允许"),
            ApprovalDecision::Deny { .. } => her_core::lang::say("denied", "已拒绝"),
            ApprovalDecision::Abort => her_core::lang::say("stopped", "已停止"),
        };
        let shown = format!("  {word}: {} {}", asking.name, render::tool_summary(&asking.name, &asking.args));
        self.print(self.style.dim(&shown));
        let op = match asking.agent_id {
            Some(agent) => Op::AgentApproval { agent, call_id: asking.call_id, decision },
            None => Op::Approval { call_id: asking.call_id, decision },
        };
        let _ = self.kernel.submit(op).await;
    }

    async fn on_terminal(&mut self, event: TermEvent) -> anyhow::Result<()> {
        self.dirty = true;
        match event {
            TermEvent::Key(key) if key.kind != KeyEventKind::Release => {
                if self.asking.is_empty() {
                    self.on_key(key).await?;
                } else {
                    self.on_approval_key(key).await;
                }
            }
            TermEvent::Paste(text) => {
                let text = text.replace("\r\n", "\n").replace('\r', "\n");
                match self.asking.first_mut() {
                    Some(asking) => asking.answer.insert(&text),
                    None => self.editor.insert(&text),
                }
            }
            _ => {}
        }
        Ok(())
    }

    async fn on_approval_key(&mut self, key: KeyEvent) {
        let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);
        let Some(asking) = self.asking.first_mut() else { return };
        match key.code {
            KeyCode::Enter => {
                let line = asking.answer.take();
                if asking.feedback {
                    let feedback = Some(line.trim().to_string()).filter(|text| !text.is_empty());
                    self.decide(ApprovalDecision::Deny { feedback }).await;
                    return;
                }
                asking.hint = None;
                match approval_prompt::answer(&asking.question(), &line) {
                    Answer::Decided(decision) => self.decide(decision).await,
                    Answer::NeedsFeedback => asking.feedback = true,
                    Answer::ShowAll => {
                        let folded = std::mem::take(&mut asking.folded);
                        self.finished.extend(folded);
                    }
                    Answer::Again(hint) => asking.hint = Some(hint),
                }
            }
            KeyCode::Char('c') if ctrl => self.decide(ApprovalDecision::Abort).await,
            KeyCode::Char(c) if !ctrl => asking.answer.insert(c.encode_utf8(&mut [0; 4])),
            KeyCode::Backspace => asking.answer.backspace(),
            KeyCode::Left => asking.answer.left(),
            KeyCode::Right => asking.answer.right(),
            _ => {}
        }
    }

    async fn on_key(&mut self, key: KeyEvent) -> anyhow::Result<()> {
        let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);
        let alt = key.modifiers.contains(KeyModifiers::ALT);
        let shift = key.modifiers.contains(KeyModifiers::SHIFT);
        match key.code {
            KeyCode::Enter if alt || shift => self.editor.insert("\n"),
            KeyCode::Char('j') if ctrl => self.editor.insert("\n"),
            KeyCode::Enter => self.on_enter().await?,
            KeyCode::Tab => self.on_tab().await,
            KeyCode::Up if self.editor.is_empty() && !self.queue.is_empty() => {
                if let Some(last) = self.queue.last().map(|q| (q.id.clone(), user_text(&q.message.content))) {
                    let _ = self.kernel.submit(Op::Withdraw { id: last.0 }).await;
                    self.editor.set(&last.1);
                }
            }
            KeyCode::Up => self.editor.up(),
            KeyCode::Down => self.editor.down(),
            KeyCode::Left if ctrl || alt => self.editor.word_left(),
            KeyCode::Right if ctrl || alt => self.editor.word_right(),
            KeyCode::Left => self.editor.left(),
            KeyCode::Right => self.editor.right(),
            KeyCode::Home => self.editor.home(),
            KeyCode::End => self.editor.end(),
            KeyCode::Backspace if alt => self.editor.delete_word(),
            KeyCode::Backspace => self.editor.backspace(),
            KeyCode::Delete => self.editor.delete(),
            KeyCode::Char('c') if ctrl => self.on_interrupt().await,
            KeyCode::Char('d') if ctrl => {
                if self.editor.is_empty() {
                    self.quit = true;
                } else {
                    self.editor.delete();
                }
            }
            KeyCode::Char('l') if ctrl => {
                let _ = std::io::stdout().write_all(b"\x1b[2J\x1b[H");
                self.live = Live::default();
            }
            KeyCode::Char('a') if ctrl => self.editor.home(),
            KeyCode::Char('e') if ctrl => self.editor.end(),
            KeyCode::Char('u') if ctrl => self.editor.kill_to_start(),
            KeyCode::Char('k') if ctrl => self.editor.kill_to_end(),
            KeyCode::Char('w') if ctrl => self.editor.delete_word(),
            KeyCode::Char('z') if ctrl => self.suspend(),
            KeyCode::Char(c) if !ctrl => self.editor.insert(c.encode_utf8(&mut [0; 4])),
            _ => {}
        }
        Ok(())
    }

    async fn on_interrupt(&mut self) {
        let again = self.last_interrupt.is_some_and(|at| at.elapsed() < AGAIN);
        if self.running.is_some() {
            if again {
                self.quit = true;
                return;
            }
            self.last_interrupt = Some(Instant::now());
            let _ = self.kernel.submit(Op::Interrupt).await;
            return;
        }
        if !self.choosing.is_empty() || self.replacing.is_some() {
            self.choosing.clear();
            self.replacing = None;
            self.editor.take();
            return;
        }
        if !self.editor.is_empty() {
            self.editor.take();
            return;
        }
        if again {
            self.quit = true;
            return;
        }
        self.last_interrupt = Some(Instant::now());
        self.print(self.style.dim(her_core::lang::say("Ctrl-C again to quit", "再按一次 Ctrl-C 退出")));
    }

    /// Ctrl-Z: the terminal comes back while stopped, and goes raw again on `fg`.
    fn suspend(&mut self) {
        let _ = self.live.clear(&mut std::io::stdout());
        drop(self.raw.take());
        // SAFETY: raises a signal on this process; it stops here until SIGCONT.
        unsafe { libc::raise(libc::SIGTSTP) };
        self.raw = term::Raw::enter().ok();
        self.dirty = true;
    }

    async fn on_enter(&mut self) -> anyhow::Result<()> {
        let text = self.editor.take();
        if text.trim().is_empty() {
            return Ok(());
        }
        if !self.choosing.is_empty() {
            let picked = text.trim().parse::<usize>().ok().and_then(|n| n.checked_sub(1)).and_then(|n| self.choosing.get(n).cloned());
            match picked {
                Some((id, words)) => {
                    self.choosing.clear();
                    self.replacing = Some(id);
                    self.editor.set(&words);
                }
                None => self.print(self.style.yellow(her_core::lang::say("no such number", "没有这个序号"))),
            }
            return Ok(());
        }
        if let Some(message_id) = self.replacing.take() {
            self.remember(&text);
            let input = vec![UserContent::Text { text: text.clone() }];
            let _ = self.kernel.submit(Op::Resend { message_id, input, commands: true }).await;
            self.print(format!("{} {text}", self.style.cyan("›")));
            return Ok(());
        }
        if let Some(slash) = parse_slash(&text) {
            return self.on_slash(slash).await;
        }
        self.send(&text, PromptMode::Steer).await;
        Ok(())
    }

    fn remember(&mut self, text: &str) {
        self.editor.remember(text);
        if let Some(file) = &self.history_file {
            save_history(file, &setup::workspace(&self.cwd), text);
        }
    }

    async fn send(&mut self, text: &str, mode: PromptMode) {
        self.remember(text);
        // While a task runs it shows in the queue until it joins.
        if self.running.is_none() {
            for (index, line) in text.lines().enumerate() {
                let marker = if index == 0 { self.style.cyan("›") } else { " ".to_string() };
                self.finished.push(format!("{marker} {line}"));
            }
            self.running = Some(Instant::now());
            self.phase = Phase::Thinking;
        }
        self.dirty = true;
        let input = vec![UserContent::Text { text: text.to_string() }];
        let _ = self.kernel.submit(Op::Prompt { input, mode, commands: true }).await;
    }

    async fn on_tab(&mut self) {
        if self.running.is_some() {
            if !self.editor.is_empty() {
                let text = self.editor.take();
                self.send(&text, PromptMode::FollowUp).await;
                return;
            }
            // The last that waits moves up one step.
            if let Some(last) = self.queue.last() {
                let op = match last.mode {
                    PromptMode::FollowUp => Op::Requeue { id: last.id.clone(), mode: PromptMode::Steer },
                    PromptMode::Steer => Op::SteerNow,
                };
                let _ = self.kernel.submit(op).await;
            }
            return;
        }
        self.complete();
    }

    /// Commands at the start of the text, otherwise paths: what all the matches share is filled
    /// in, and the matches are listed when they differ.
    fn complete(&mut self) {
        let word = self.editor.last_word().to_string();
        let matches: Vec<String> = if word.starts_with('/') && self.editor.text() == word {
            COMMANDS.iter().filter(|c| c.starts_with(&word)).map(|c| (*c).to_string()).collect()
        } else {
            complete_path(&self.cwd, &word)
        };
        let Some(first) = matches.first() else { return };
        let shared = matches.iter().fold(first.as_str(), |shared, m| {
            let common = shared.char_indices().zip(m.chars()).take_while(|((_, a), b)| a == b).last().map_or(0, |((i, a), _)| i + a.len_utf8());
            &shared[..common]
        });
        if shared.len() > word.len() {
            let rest = shared[word.len()..].to_string();
            self.editor.insert(&rest);
        } else if matches.len() > 1 {
            self.print(self.style.dim(&matches.join("  ")));
        }
    }

    /// Runs something that prints to the ordinary terminal or asks on it.
    async fn outside<F, Fut>(&mut self, work: F) -> anyhow::Result<()>
    where
        F: FnOnce(Vec<String>) -> Fut,
        Fut: std::future::Future<Output = anyhow::Result<u8>>,
    {
        self.draw();
        let _ = self.live.clear(&mut std::io::stdout());
        self.keys_paused.store(true, Ordering::Release);
        // The key thread looks at the flag at least this often.
        tokio::time::sleep(Duration::from_millis(120)).await;
        drop(self.raw.take());
        let result = work(self.overrides.clone()).await;
        if let Err(err) = result {
            eprintln!("error: {err:#}");
        }
        self.raw = Some(term::Raw::enter().context("cannot set up the terminal")?);
        self.keys_paused.store(false, Ordering::Release);
        self.dirty = true;
        Ok(())
    }

    async fn on_slash(&mut self, slash: Slash<'_>) -> anyhow::Result<()> {
        match slash {
            Slash::Help => {
                let lines: Vec<String> = HELP.lines().map(str::to_string).collect();
                self.finished.extend(lines);
            }
            Slash::Quit => self.quit = true,
            Slash::Model(None) => {
                self.outside(|overrides| async move { crate::cmd_info::models(&overrides, None, false, false, false, None) }).await?;
            }
            Slash::Model(Some(model)) => {
                let _ = self.kernel.submit(Op::SetModel { model: model.to_string(), thinking: None }).await;
            }
            Slash::Thinking(None) => {
                let current = self.kernel.info().thinking;
                self.print(format!("thinking: {current:?}").to_lowercase());
            }
            Slash::Thinking(Some(level)) => match level.parse::<ThinkingLevel>() {
                Ok(level) => {
                    let _ = self.kernel.submit(Op::SetModel { model: self.model.clone(), thinking: Some(level) }).await;
                    self.print(self.style.dim(&format!("thinking: {level:?}").to_lowercase()));
                }
                Err(err) => self.print(self.style.yellow(&err.to_string())),
            },
            Slash::Permissions(None) => {
                let current = self.kernel.info().permission_mode;
                self.print(format!("permissions: {current}"));
            }
            Slash::Permissions(Some(mode)) => match PermissionMode::parse_with_notice(mode) {
                Ok((mode, _)) => {
                    let _ = self.kernel.submit(Op::SetPermissionMode { mode }).await;
                    self.print(self.style.dim(&format!("permissions: {mode}")));
                }
                Err(err) => self.print(self.style.yellow(&err)),
            },
            Slash::Rename(title) => {
                if title.is_empty() {
                    self.print(self.style.yellow("/rename TEXT"));
                } else {
                    let _ = self.kernel.submit(Op::SetTitle { title: title.to_string() }).await;
                    self.print(self.style.dim(&format!("renamed: {title}")));
                }
            }
            Slash::Status => self.outside(|overrides| async move { crate::cmd_info::status(&overrides, false) }).await?,
            Slash::Tools => self.outside(|overrides| async move { crate::cmd_info::tools(&overrides, None, false, false) }).await?,
            Slash::Mcp => self.outside(|overrides| async move { crate::cmd_mcp::list(&overrides, false) }).await?,
            Slash::Skills => self.outside(|overrides| async move { crate::cmd_info::skills(&overrides, None, false, false) }).await?,
            Slash::Login => self.outside(|overrides| async move { crate::cmd_login::login(&overrides, Default::default()).await }).await?,
            Slash::Rewind => self.begin_rewind(),
            Slash::New | Slash::Resume(Some(_)) if self.running.is_some() => {
                self.print(self.style.yellow(her_core::lang::say("A task is running: Ctrl+C stops it first.", "任务还在运行，先按 Ctrl+C 停止。")));
            }
            Slash::New => self.switch_session(None).await?,
            Slash::Resume(Some(session)) => self.switch_session(Some(session.to_string())).await?,
            Slash::Resume(None) => self.list_sessions()?,
        }
        self.dirty = true;
        Ok(())
    }

    fn list_sessions(&mut self) -> anyhow::Result<()> {
        let home = setup::home()?;
        let store = JsonlStore::new(home.sessions_dir());
        let workspace = setup::workspace(&self.cwd);
        let sessions = store.list(20, Some(&workspace))?;
        if sessions.is_empty() {
            self.print(self.style.dim(her_core::lang::say("No sessions yet in this project.", "这个项目还没有会话。")));
        }
        for session in sessions {
            let title = session.title.clone().or(session.first_message.clone()).unwrap_or_default();
            let line = format!("{}  {}", her_core::short_id(&session.id), render::truncate(&title, 70));
            self.finished.push(line);
        }
        self.print(self.style.dim("/resume ID"));
        Ok(())
    }

    fn begin_rewind(&mut self) {
        let mut choices = Vec::new();
        for item in self.kernel.transcript() {
            if let TranscriptItem::Message { message_id, message: Message::User(user), mail: None, .. } = item {
                choices.push((message_id, user.text_content()));
            }
        }
        if choices.is_empty() {
            self.print(self.style.dim(her_core::lang::say("Nothing to go back to yet.", "还没有可以回退的消息。")));
            return;
        }
        for (index, (_, text)) in choices.iter().enumerate() {
            self.finished.push(format!("{:>3}  {}", index + 1, render::truncate(text, 70)));
        }
        self.choosing = choices;
    }

    /// Closes this session and opens another: a new one here, or one named by id.
    async fn switch_session(&mut self, session: Option<String>) -> anyhow::Result<()> {
        let run = RunArgs::default();
        let (rt, next) = match &session {
            Some(session) => {
                let (cwd, path) = match find_session(session) {
                    Ok(found) => found,
                    Err(err) => {
                        self.print(self.style.yellow(&format!("{err:#}")));
                        return Ok(());
                    }
                };
                let rt = setup::init(cwd, &self.overrides)?;
                let kernel = resume_kernel(&rt, &path, &run).await?;
                (rt, kernel)
            }
            None => {
                let rt = setup::init(self.cwd.clone(), &self.overrides)?;
                let model = setup::session_model(&rt, Some(self.model.clone()))?;
                let kernel = start_kernel(&rt, model, &run).await?;
                (rt, kernel)
            }
        };
        let old = std::mem::replace(&mut self.kernel, next);
        let _ = old.shutdown().await;
        self.cwd.clone_from(&rt.cwd);
        self.trusted = rt.trusted;
        self.queue.clear();
        self.asking.clear();
        self.agents.clear();
        self.context_percent = None;
        self.model = self.kernel.info().model;
        if session.is_some() {
            self.print_recent();
        } else {
            self.print(self.style.dim(&format!("new session · {}", self.model)));
        }
        Ok(())
    }

    /// The end of a resumed conversation, so that it reads on from here.
    fn print_recent(&mut self) {
        const RECENT: usize = 20;
        let items = self.kernel.transcript();
        let messages: Vec<&Message> = items
            .iter()
            .filter_map(|item| match item {
                TranscriptItem::Message { message, .. } => Some(message),
                _ => None,
            })
            .collect();
        let skip = messages.len().saturating_sub(RECENT);
        for message in &messages[skip..] {
            match message {
                Message::User(user) => {
                    let text = user.text_content();
                    let line = format!("{} {}", self.style.cyan("›"), render::truncate(&text, 200));
                    self.finished.push(line);
                }
                Message::Assistant(assistant) => {
                    let text = assistant.text();
                    for line in text.lines() {
                        let formatted = self.markdown.line(&self.style, line);
                        self.finished.push(formatted);
                    }
                    self.markdown = Formatter::default();
                }
                _ => {}
            }
        }
        self.dirty = true;
    }
}

fn complete_path(cwd: &Path, word: &str) -> Vec<String> {
    let (dir, prefix) = word.rsplit_once('/').map_or(("", word), |(dir, prefix)| (dir, prefix));
    if word.is_empty() || (!word.contains('/') && !word.starts_with('.')) {
        return Vec::new();
    }
    let base = if dir.is_empty() && word.starts_with('/') { PathBuf::from("/") } else { cwd.join(dir) };
    let Ok(entries) = std::fs::read_dir(&base) else { return Vec::new() };
    let mut found: Vec<String> = entries
        .flatten()
        .filter_map(|entry| {
            let name = entry.file_name().to_string_lossy().into_owned();
            if !name.starts_with(prefix) || (prefix.is_empty() && name.starts_with('.')) {
                return None;
            }
            let slash = if entry.path().is_dir() { "/" } else { "" };
            Some(if word.contains('/') { format!("{dir}/{name}{slash}") } else { format!("{name}{slash}") })
        })
        .collect();
    found.sort();
    found
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_the_exact_words_are_commands() {
        assert_eq!(parse_slash("/quit"), Some(Slash::Quit));
        assert_eq!(parse_slash("/exit"), Some(Slash::Quit));
        assert_eq!(parse_slash(" /model a/b "), Some(Slash::Model(Some("a/b"))));
        assert_eq!(parse_slash("/rename a good name"), Some(Slash::Rename("a good name")));
        assert_eq!(parse_slash("/etc/hosts is what?"), None);
        assert_eq!(parse_slash("/quit now"), None, "a sentence that starts with a command word");
        assert_eq!(parse_slash("/compact"), None, "the kernel's own");
        assert_eq!(parse_slash("hello"), None);
    }

    #[test]
    fn paths_complete_from_the_directory_they_name() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir(dir.path().join("src")).unwrap();
        std::fs::write(dir.path().join("src/lib.rs"), "").unwrap();
        std::fs::write(dir.path().join("src/list.rs"), "").unwrap();
        assert_eq!(complete_path(dir.path(), "src/li"), vec!["src/lib.rs", "src/list.rs"]);
        assert_eq!(complete_path(dir.path(), "src/lib"), vec!["src/lib.rs"]);
        assert_eq!(complete_path(dir.path(), "./s"), vec!["./src/"]);
        assert!(complete_path(dir.path(), "plainword").is_empty());
    }
}
