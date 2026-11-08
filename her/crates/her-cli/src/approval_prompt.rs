//! Inline approval questions on the terminal.

use her_protocol::ApprovalDecision;
use her_protocol::CommandRule;
use her_protocol::GrantAuthority;
use her_protocol::PermissionMode;
use her_protocol::SiteGrant;
use serde_json::Value;
use std::io::Write;

use crate::render::Style;

/// The lines of a new file shown before asking.
const WRITE_LINES: usize = 12;
/// How much of a change shows before the question; `v` shows the rest.
pub const FOLD_LINES: usize = 40;

async fn read_line() -> Option<String> {
    tokio::task::spawn_blocking(|| {
        let mut line = String::new();
        match std::io::stdin().read_line(&mut line) {
            Ok(0) | Err(_) => None,
            Ok(_) => Some(line),
        }
    })
    .await
    .ok()
    .flatten()
}

/// What the kernel asks about, from an `ApprovalRequested` event.
pub struct Question<'a> {
    /// The sub-agent that asks, if one does.
    pub agent: Option<&'a str>,
    pub name: &'a str,
    pub summary: &'a str,
    pub reason: Option<&'a str>,
    /// Command prefix that "always" allows for the rest of the session.
    pub rule: Option<&'a CommandRule>,
    /// Mode that "always" switches the session to.
    pub mode: Option<PermissionMode>,
    /// Site permissions to keep for the rest of the session, one of them by number.
    pub grants: &'a [SiteGrant],
}

fn grant_label(grant: &SiteGrant) -> String {
    match (grant.authority, grant.origin.as_str()) {
        (GrantAuthority::Navigate, "*") => "allow opening every site in this session".into(),
        (GrantAuthority::Act, "*") => "allow every site in this session (an injected page could make the model act on sites you are signed in to)".into(),
        (GrantAuthority::Navigate, origin) => format!("allow opening {origin} in this session"),
        (GrantAuthority::Act, origin) => format!("allow {origin} in this session"),
        (GrantAuthority::Script, origin) => format!("allow scripts on {origin} in this session"),
    }
}

/// What "always" means here, when the kernel can remember something.
fn always_label(question: &Question<'_>) -> Option<String> {
    match (question.rule, question.mode) {
        (Some(rule), _) => Some(format!("always allow `{}` in this session", rule.prefix.join(" "))),
        (None, Some(PermissionMode::AcceptEdits)) => Some("allow all edits in this session".to_string()),
        (None, Some(mode)) => Some(format!("switch to {mode} for this session")),
        (None, None) => None,
    }
}

/// What the tool is about to do, so that a person can see it (standalone-cli-design §5.5).
pub fn details(style: &Style, name: &str, args: &Value) -> Vec<String> {
    let mut lines = Vec::new();
    match name {
        "edit" => {
            for edit in args.get("edits").and_then(Value::as_array).into_iter().flatten() {
                let part = |key: &str| edit.get(key).and_then(Value::as_str).unwrap_or_default();
                for line in part("old_text").lines() {
                    lines.push(style.red(&format!("  - {line}")));
                }
                for line in part("new_text").lines() {
                    lines.push(style.green(&format!("  + {line}")));
                }
            }
        }
        "write" => {
            let content = args.get("content").and_then(Value::as_str).unwrap_or_default();
            let total = content.lines().count();
            for line in content.lines().take(WRITE_LINES) {
                lines.push(style.green(&format!("  + {line}")));
            }
            if total > WRITE_LINES {
                lines.push(style.dim(&format!("  … and {} more line(s)", total - WRITE_LINES)));
            }
        }
        _ => {}
    }
    lines
}

/// The question, the part above the answer: what happens, why, and the choices.
pub fn lines(style: &Style, question: &Question<'_>) -> Vec<String> {
    let mut out = Vec::new();
    let who = question.agent.map_or(String::new(), |agent| format!("[agent {agent}] "));
    out.push(style.yellow(&format!("? {who}Allow {}: {}", question.name, question.summary)));
    if let Some(reason) = question.reason {
        out.push(format!("  {}", style.dim(&format!("reason: {reason}"))));
    }
    match always_label(question) {
        Some(always) => out.push(format!("  [y] yes   [a] {always}   [n] no   [q] stop the task")),
        None => out.push("  [y] yes   [n] no   [q] stop the task".to_string()),
    }
    for (index, grant) in question.grants.iter().enumerate() {
        out.push(format!("  [{}] {}", index + 1, grant_label(grant)));
    }
    // "Always": kept in ~/.her/config.toml, every site included (D-197, D-221).
    if !question.grants.is_empty() {
        out.push(format!("  {}", style.dim("[number]! also saves it in ~/.her/config.toml, e.g. 1!")));
    }
    out
}

/// What an answer comes to.
pub enum Answer {
    Decided(ApprovalDecision),
    /// `n`: a word for the model may follow.
    NeedsFeedback,
    /// `v`: everything the question has folded away.
    ShowAll,
    Again(&'static str),
}

pub fn answer(question: &Question<'_>, line: &str) -> Answer {
    let always = always_label(question).is_some();
    let answer = line.trim().to_lowercase();
    let (number, persist) = match answer.strip_suffix('!') {
        Some(number) => (number, true),
        None => (answer.as_str(), false),
    };
    let grant = number.parse::<usize>().ok().filter(|n| (1..=question.grants.len()).contains(n));
    match answer.as_str() {
        "y" | "yes" => Answer::Decided(ApprovalDecision::Approve),
        "a" | "always" if always => Answer::Decided(ApprovalDecision::ApproveForSession { grant: None, persist: false }),
        _ if grant.is_some() => Answer::Decided(ApprovalDecision::ApproveForSession { grant: grant.map(|n| n - 1), persist }),
        "n" | "no" => Answer::NeedsFeedback,
        "v" => Answer::ShowAll,
        "q" | "quit" => Answer::Decided(ApprovalDecision::Abort),
        _ if !question.grants.is_empty() => Answer::Again("please answer y, a number, n or q"),
        _ if always => Answer::Again("please answer y, a, n or q"),
        _ => Answer::Again("please answer y, n or q"),
    }
}

/// Asks on an ordinary terminal, line by line: `her exec`.
pub async fn ask(style: &Style, question: Question<'_>, args: &Value) -> ApprovalDecision {
    eprintln!();
    let details = details(style, question.name, args);
    let folded = details.len() > FOLD_LINES;
    for line in details.iter().take(FOLD_LINES) {
        eprintln!("{line}");
    }
    if folded {
        eprintln!("{}", style.dim(&format!("  … {} more line(s): v shows them", details.len() - FOLD_LINES)));
    }
    for line in lines(style, &question) {
        eprintln!("{line}");
    }
    loop {
        eprint!("> ");
        let _ = std::io::stderr().flush();
        let Some(line) = read_line().await else {
            return ApprovalDecision::Abort;
        };
        match answer(&question, &line) {
            Answer::Decided(decision) => {
                if matches!(decision, ApprovalDecision::ApproveForSession { grant: None, .. })
                    && let Some(mode) = question.mode
                {
                    eprintln!("  {}", style.dim(&format!("permission mode is now {mode}")));
                }
                return decision;
            }
            Answer::NeedsFeedback => {
                eprint!("  feedback for the model (optional): ");
                let _ = std::io::stderr().flush();
                let feedback = read_line().await.map(|l| l.trim().to_string()).filter(|l| !l.is_empty());
                return ApprovalDecision::Deny { feedback };
            }
            Answer::ShowAll => {
                for line in details.iter().skip(FOLD_LINES) {
                    eprintln!("{line}");
                }
            }
            Answer::Again(hint) => eprintln!("  {hint}"),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn an_edit_shows_what_goes_and_what_comes() {
        let style = Style::plain();
        let args = json!({ "path": "a.rs", "edits": [{ "old_text": "let t = 30;", "new_text": "let t = 60;\nlet u = 1;" }] });
        assert_eq!(details(&style, "edit", &args), vec!["  - let t = 30;", "  + let t = 60;", "  + let u = 1;"]);
        assert!(details(&style, "bash", &json!({ "command": "ls" })).is_empty());
    }

    #[test]
    fn answers_are_what_the_question_offered() {
        let grants = [SiteGrant { authority: GrantAuthority::Act, origin: "https://a.example".into() }];
        let with_grant = Question { agent: None, name: "x", summary: "s", reason: None, rule: None, mode: None, grants: &grants };
        assert!(matches!(
            answer(&with_grant, "1!\n"),
            Answer::Decided(ApprovalDecision::ApproveForSession { grant: Some(0), persist: true })
        ));
        assert!(matches!(answer(&with_grant, "2"), Answer::Again(_)));
        let bare = Question { agent: None, name: "x", summary: "s", reason: None, rule: None, mode: None, grants: &[] };
        assert!(matches!(answer(&bare, "a"), Answer::Again(_)), "nothing to remember");
        assert!(matches!(answer(&bare, "n"), Answer::NeedsFeedback));
        assert!(matches!(answer(&bare, "Y"), Answer::Decided(ApprovalDecision::Approve)));
        assert!(matches!(answer(&bare, "q"), Answer::Decided(ApprovalDecision::Abort)));
    }
}
