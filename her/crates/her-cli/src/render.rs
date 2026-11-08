//! Terminal rendering: the reply goes to stdout, everything else to stderr.

use her_llm::Message;
use her_llm::UsageTotals;
use her_protocol::AgentStatus;
use her_protocol::Delta;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::McpServerState;
use her_protocol::WarningCode;
use serde_json::Value;
use std::io::IsTerminal;
use std::io::Write;

pub struct Style {
    color: bool,
}

/// `NO_COLOR` counts when it is not empty (no-color.org); a dumb terminal has no colors at all.
fn color_allowed(no_color: Option<std::ffi::OsString>, term: Option<std::ffi::OsString>) -> bool {
    no_color.is_none_or(|value| value.is_empty()) && term.is_none_or(|value| value != "dumb")
}

impl Style {
    #[cfg(test)]
    pub fn plain() -> Self {
        Self { color: false }
    }

    pub fn detect() -> Self {
        Self {
            color: std::io::stderr().is_terminal() && color_allowed(std::env::var_os("NO_COLOR"), std::env::var_os("TERM")),
        }
    }

    fn paint(&self, code: &str, text: &str) -> String {
        if self.color {
            format!("\x1b[{code}m{text}\x1b[0m")
        } else {
            text.to_string()
        }
    }

    pub fn dim(&self, text: &str) -> String {
        self.paint("2", text)
    }

    pub fn red(&self, text: &str) -> String {
        self.paint("31", text)
    }

    pub fn green(&self, text: &str) -> String {
        self.paint("32", text)
    }

    pub fn yellow(&self, text: &str) -> String {
        self.paint("33", text)
    }

    pub fn cyan(&self, text: &str) -> String {
        self.paint("36", text)
    }

    pub fn bold(&self, text: &str) -> String {
        self.paint("1", text)
    }
}

pub struct Renderer {
    json: bool,
    style: Style,
    line_open: bool,
    thinking_open: bool,
    /// Status lines that arrived in the middle of a line of the reply or of thinking.
    held: Vec<String>,
}

impl Renderer {
    pub fn new(json: bool) -> Self {
        Self {
            json,
            style: Style::detect(),
            line_open: false,
            thinking_open: false,
            held: Vec::new(),
        }
    }

    /// A line on stderr that arrives on its own (not caused by the reply), held until the
    /// current line ends so it does not split the reply.
    fn status_line(&mut self, line: String) {
        if self.line_open || self.thinking_open {
            self.held.push(line);
        } else {
            eprintln!("{line}");
        }
    }

    fn release_held(&mut self) {
        for line in std::mem::take(&mut self.held) {
            eprintln!("{line}");
        }
    }

    pub fn style(&self) -> &Style {
        &self.style
    }

    pub fn render(&mut self, event: &Event) {
        if self.json {
            if let Ok(line) = serde_json::to_string(event) {
                println!("{line}");
            }
            return;
        }
        match &event.msg {
            EventMsg::MessageDelta {
                delta: Delta::Text { text, .. },
                ..
            } => {
                self.end_thinking();
                print!("{text}");
                let _ = std::io::stdout().flush();
                self.line_open = !text.ends_with('\n');
            }
            EventMsg::MessageDelta {
                delta: Delta::Thinking { text, .. },
                ..
            } => {
                if !self.thinking_open {
                    self.end_line();
                    eprint!("{}", self.style.dim("thinking: "));
                    self.thinking_open = true;
                }
                eprint!("{}", self.style.dim(text));
            }
            EventMsg::MessageCompleted {
                message: Message::Assistant(_),
                ..
            } => {
                self.end_thinking();
                self.end_line();
            }
            EventMsg::ContextReported { report } => {
                self.end_thinking();
                self.end_line();
                print!("{}", her_core::context_report::text(report));
            }
            msg => {
                let lines = status_lines(&self.style, msg);
                if lines.is_empty() {
                    return;
                }
                // These arrive on their own, not because of the reply: they wait for its line to end.
                if matches!(msg, EventMsg::ApprovalRefused { .. } | EventMsg::McpServerStatus { .. }) {
                    for line in lines {
                        self.status_line(line);
                    }
                } else {
                    self.end_thinking();
                    self.end_line();
                    for line in lines {
                        eprintln!("{line}");
                    }
                }
            }
        }
    }

    fn end_thinking(&mut self) {
        if self.thinking_open {
            eprintln!();
            self.thinking_open = false;
        }
        if !self.line_open {
            self.release_held();
        }
    }

    fn end_line(&mut self) {
        if self.line_open {
            println!();
            self.line_open = false;
        }
        if !self.thinking_open {
            self.release_held();
        }
    }
}

/// What an event adds to the output apart from the reply itself: the same words in `her exec` and
/// in the interactive mode.
pub fn status_lines(style: &Style, msg: &EventMsg) -> Vec<String> {
    match msg {
        EventMsg::SessionStarted { session_id, model, resumed, .. } => {
            let action = if *resumed { "resumed" } else { "session" };
            let short = her_core::short_id(session_id);
            vec![style.dim(&format!("her · {model} · {action} {short}"))]
        }
        EventMsg::MessageCompleted { message: Message::User(_), skills, .. } if !skills.is_empty() => {
            let names: Vec<&str> = skills.iter().map(|skill| skill.name.as_str()).collect();
            vec![style.dim(&format!("using skill(s): {}", names.join(", ")))]
        }
        EventMsg::ToolStarted { name, args, .. } => {
            vec![format!("{} {}", style.cyan(&format!("• {name}")), tool_summary(name, args))]
        }
        EventMsg::ToolCompleted { result, .. } => {
            let line = result_line(&result.tool_name, &result.text_content(), result.duration_ms);
            if result.is_error {
                vec![format!("  {}", style.red(&format!("✗ {line}")))]
            } else {
                vec![format!("  {}", style.green(&format!("✓ {line}")))]
            }
        }
        EventMsg::StreamRetry { attempt, delay_ms, error } => vec![style.yellow(&format!(
            "retrying in {:.1}s (attempt {attempt}): {}",
            *delay_ms as f64 / 1000.0,
            error.message
        ))],
        // Shown by the server status lines already.
        EventMsg::Warning { code: Some(WarningCode::McpServersNotReady), .. } => Vec::new(),
        EventMsg::ApprovalRefused { name, summary, .. } => {
            vec![style.yellow(&format!("refused {name}: {summary} (nothing here can approve it)"))]
        }
        EventMsg::Warning { message, .. } => vec![style.yellow(&format!("warning: {message}"))],
        EventMsg::McpServerStatus { server, state, tools, error, elapsed_ms } => {
            let elapsed = elapsed_ms.map(|ms| format!(", {ms} ms")).unwrap_or_default();
            match state {
                McpServerState::Ready => vec![style.dim(&format!("mcp: {server} ready ({tools} tools{elapsed})"))],
                McpServerState::Failed | McpServerState::NeedsLogin => {
                    let error = error.as_deref().unwrap_or("not connected");
                    vec![style.yellow(&format!("mcp: {server} failed: {error}"))]
                }
                McpServerState::Starting | McpServerState::Closed => Vec::new(),
            }
        }
        EventMsg::CompactionStarted { reason } => vec![style.dim(&format!("compacting the context ({})…", reason.as_str()))],
        EventMsg::CompactionCompleted { seq, tokens_before, tokens_after, retained_messages, kept_skills, summary, .. } => {
            let skills = if kept_skills.is_empty() {
                String::new()
            } else {
                format!(" · kept skill(s) {}", kept_skills.join(", "))
            };
            vec![style.dim(&format!(
                "context compacted (#{seq}): {} → {} tokens · kept {retained_messages} user message(s){skills} · summary {} chars",
                group_digits(*tokens_before),
                group_digits(*tokens_after),
                group_digits(summary.chars().count() as u64)
            ))]
        }
        EventMsg::CompactionFailed { error, .. } => vec![style.yellow(&format!("compaction failed: {}", error.message))],
        EventMsg::TaskFailed { error, usage, .. } => vec![
            style.red(&format!("error ({}): {}", error.kind, error.message)),
            style.dim(&usage_line(usage)),
        ],
        EventMsg::TaskInterrupted { usage, .. } => vec![style.yellow("interrupted"), style.dim(&usage_line(usage))],
        EventMsg::TaskCompleted { usage, .. } => vec![style.dim(&usage_line(usage))],
        EventMsg::AgentStarted { name, description, .. } => {
            let label = if description.is_empty() { String::new() } else { format!(": {description}") };
            vec![style.dim(&format!("· agent {name} started{label}"))]
        }
        EventMsg::AgentStatus { name, status, .. } if *status != AgentStatus::Running => {
            vec![style.dim(&format!("· agent {name} {}", status.as_str()))]
        }
        _ => Vec::new(),
    }
}

pub fn truncate(text: &str, max: usize) -> String {
    let single: String = text.lines().next().unwrap_or("").to_string();
    let cut = single.chars().count() > max || text.lines().count() > 1;
    let mut out: String = single.chars().take(max).collect();
    if cut {
        out.push('…');
    }
    out
}

pub fn tool_summary(name: &str, args: &Value) -> String {
    let field = |key: &str| args.get(key).and_then(Value::as_str).unwrap_or("").to_string();
    match name {
        "bash" => truncate(&field("command"), 120),
        "read" | "write" => field("path"),
        "edit" => {
            let count = args.get("edits").and_then(Value::as_array).map_or(0, Vec::len);
            format!("{} ({count} change(s))", field("path"))
        }
        // Its label says what it is for; calls from before it had one show the start of the task.
        "spawn_agent" => {
            let about = if field("description").is_empty() { field("task") } else { field("description") };
            truncate(format!("{} {about}", field("name")).trim_start(), 120)
        }
        "tool_search" => truncate(format!("{} {}", field("server"), field("query")).trim(), 120),
        "call_tool" => {
            let arguments = args.get("arguments").map(Value::to_string).unwrap_or_default();
            truncate(&format!("{} {arguments}", field("name")), 120)
        }
        "send_message" => truncate(&format!("{}: {}", field("to"), field("message")), 120),
        _ => truncate(&args.to_string(), 120),
    }
}

fn result_line(tool: &str, text: &str, duration_ms: Option<u64>) -> String {
    if tool == "bash"
        && let Some(code) = text.lines().find_map(|l| l.strip_prefix("Exit code: "))
    {
        let seconds = duration_ms.unwrap_or(0) as f64 / 1000.0;
        let timed_out = text.starts_with("Command timed out");
        return format!(
            "exit {code} · {seconds:.1}s{}",
            if timed_out { " · timed out" } else { "" }
        );
    }
    truncate(text, 100)
}

fn group_digits(n: u64) -> String {
    let digits = n.to_string();
    let mut out = String::new();
    for (i, c) in digits.chars().enumerate() {
        if i > 0 && (digits.len() - i).is_multiple_of(3) {
            out.push(',');
        }
        out.push(c);
    }
    out
}

pub fn usage_line(usage: &UsageTotals) -> String {
    let tokens = usage.tokens();
    let input = tokens.input + tokens.cache_read + tokens.cache_write;
    let mut line = format!(
        "tokens: {} in ({} cached) · {} out",
        group_digits(input),
        group_digits(tokens.cache_read),
        group_digits(tokens.output)
    );
    let costs = usage.cost_by_currency();
    if !costs.is_empty() {
        let parts: Vec<String> = costs
            .iter()
            .map(|(currency, amount)| format!("{}{amount:.4}", currency.symbol()))
            .collect();
        line.push_str(&format!(" · cost {}", parts.join(" + ")));
    }
    if usage.has_unpriced_usage() {
        line.push_str(" · cost unknown (no price configured)");
    }
    line
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn formats_numbers_and_results() {
        assert_eq!(group_digits(1234567), "1,234,567");
        assert_eq!(group_digits(12), "12");
        assert_eq!(result_line("bash", "Exit code: 2\nDuration: 0.1s\nOutput:\nx", Some(1500)), "exit 2 · 1.5s");
        assert_eq!(truncate("one\ntwo", 10), "one…");
    }
}

#[cfg(test)]
mod color_tests {
    use super::color_allowed;

    #[test]
    fn no_color_and_a_dumb_terminal_turn_colors_off() {
        let some = |text: &str| Some(std::ffi::OsString::from(text));
        assert!(color_allowed(None, None));
        assert!(color_allowed(None, some("xterm-256color")));
        assert!(!color_allowed(None, some("dumb")));
        assert!(!color_allowed(some("1"), None));
        assert!(color_allowed(some(""), None), "an empty NO_COLOR does not count");
    }
}
