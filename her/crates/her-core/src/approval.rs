//! The built-in permission policy (registered as the last `before_tool` hook).
//!
//! In every mode deny rules deny, while deleting a critical path, scripts nested too deeply
//! to check, and ask rules ask. Rules see every command a line may run (see
//! [`rules::analyze`]). Then:
//! - manual: read-only commands and commands matching an allow rule run; the rest asks.
//! - accept-edits: file changes and `mkdir`/`touch`/`cp`/`mv` inside the workspace run too,
//!   unless they touch a protected path.
//! - bypass-permissions: everything else runs, protected paths and paths outside the
//!   workspace included.

use async_trait::async_trait;
use her_llm::ToolCall;
use her_protocol::CommandRule;
use her_protocol::PermissionMode;
use her_protocol::RuleDecision;
use her_tools::Authority;
use her_tools::Tool;
use her_tools::ToolKind;
use her_tools::ToolOrigin;
use std::path::Path;
use std::path::PathBuf;

use crate::hooks::Gate;
use crate::hooks::Hooks;
use crate::hooks::SessionView;
use crate::protected;
use crate::protected::canonical_lossy;
use crate::rules;
use crate::rules::Analysis;
use crate::rules::Cwds;
use crate::rules::FileCommand;
use crate::rules::Found;
use crate::rules::ShellContext;
use crate::rules::Target;

const OUTSIDE_WORKSPACE: &str = "The path is outside the workspace.";
const TOO_DEEP: &str = "The command nests scripts too deeply to check.";
const HARD_LINK: &str = "The copy would write into a file that has other hard links, which may be outside the workspace.";

pub struct ApprovalPolicy;

#[async_trait]
impl Hooks for ApprovalPolicy {
    async fn before_tool(&self, call: &ToolCall, tool: &dyn Tool, authorities: &[Authority], view: &SessionView) -> Gate {
        // A built-in tool run through call_tool is judged as itself.
        if tool.origin_for(&call.arguments.to_value()) == ToolOrigin::Mcp {
            return evaluate_mcp(call, tool, view);
        }
        // Tools that say what they do on which site are judged by that (browser-design §7.1).
        if !authorities.is_empty() {
            return crate::site_policy::evaluate(authorities, view).await;
        }
        let kind = tool.kind_for(&call.arguments.to_value());
        match through_call_tool(call) {
            Some(inner) => evaluate(&inner, kind, view),
            None => evaluate(call, kind, view),
        }
    }
}

/// A built-in tool run through call_tool, as the call it stands for: judged by its own rule,
/// not as an unknown change (a deferred deck_edit is still a deck edit).
fn through_call_tool(call: &ToolCall) -> Option<ToolCall> {
    if call.name != crate::mcp::tools::CALL_TOOL {
        return None;
    }
    let args = call.arguments.to_value();
    let name = args.get("name")?.as_str()?.to_owned();
    let inner = args.get("arguments").cloned().unwrap_or(serde_json::Value::Object(Default::default()));
    Some(ToolCall { id: call.id.clone(), name, arguments: inner.into(), arguments_error: None, provider_data: None })
}

/// The kernel cannot tell what a tool from outside does (standalone-cli-design §8.5). So it asks,
/// unless the tool says it only reads and its server is the user's own, the user trusts the
/// server, or every action runs. Rules name such a tool by its full name, as a command's first
/// word; that is also how "don't ask again" is kept for the session.
fn evaluate_mcp(call: &ToolCall, tool: &dyn Tool, view: &SessionView) -> Gate {
    let args = call.arguments.to_value();
    let through_call_tool = call.name == crate::mcp::tools::CALL_TOOL;
    let name = match args.get("name").and_then(|v| v.as_str()) {
        Some(name) if through_call_tool => name.to_string(),
        _ => call.name.clone(),
    };
    let rules: Vec<CommandRule> = view.config_rules.iter().chain(view.session_rules.iter()).cloned().collect();
    let words = [name.clone()];
    if let Some(rule) = rules::first_match(&rules, &words, RuleDecision::Deny) {
        return Gate::Deny(format!(
            "The tool is blocked by the rule `{}`{}",
            rule.prefix.join(" "),
            rule.reason.as_deref().map(|r| format!(": {r}")).unwrap_or_default()
        ));
    }
    if let Some(rule) = rules::first_match(&rules, &words, RuleDecision::Ask) {
        return ask(mcp_summary(&name, &args, through_call_tool), Some(rule_reason(rule)));
    }
    if view.permission_mode == PermissionMode::BypassPermissions
        || tool.runs_unasked(&args)
        || rules::first_match(&rules, &words, RuleDecision::Allow).is_some()
    {
        return Gate::Allow;
    }
    Gate::Ask {
        summary: mcp_summary(&name, &args, through_call_tool),
        reason: None,
        proposed_rule: Some(CommandRule { prefix: vec![name], decision: RuleDecision::Allow, reason: None }),
        proposed_mode: None,
        proposed_grants: Vec::new(),
        own_browser: false,
    }
}

fn mcp_summary(name: &str, args: &serde_json::Value, through_call_tool: bool) -> String {
    let shown = if through_call_tool { args.get("arguments").unwrap_or(&serde_json::Value::Null) } else { args };
    format!("{name} {shown}")
}

pub fn evaluate(call: &ToolCall, kind: ToolKind, view: &SessionView) -> Gate {
    let args = call.arguments.to_value();
    let arg = |key: &str| args.get(key).and_then(|v| v.as_str()).unwrap_or("");
    match call.name.as_str() {
        "bash" => evaluate_bash(arg("command"), view),
        "write" | "edit" => evaluate_file_change(call, &args, arg("path"), view),
        "doc_edit" => evaluate_doc_edit(&args, arg("doc"), view),
        "doc_save" => evaluate_doc_save(arg("doc"), arg("path"), view),
        "deck_edit" => evaluate_deck_edit(&args, arg("deck"), view),
        "deck_save" => evaluate_doc_save(arg("deck"), arg("path"), view),
        // Tracked changes are the user's review in the document itself (word-design §11-2).
        "docx_edit" if args.get("mode").and_then(|v| v.as_str()).unwrap_or("suggest") == "suggest" => Gate::Allow,
        "docx_edit" | "docx_review" => evaluate_edit_in_kernel(docx_edit_summary(&args, arg("docx")), view),
        "docx_save" => evaluate_doc_save(arg("docx"), arg("path"), view),
        _ => match (kind, view.permission_mode) {
            (ToolKind::ReadOnly, _) | (ToolKind::Mutating, PermissionMode::BypassPermissions) => Gate::Allow,
            (ToolKind::Mutating, _) if call.name == crate::routine::MANAGE_ROUTINE => ask(crate::routine::approval_summary(&args), None),
            (ToolKind::Mutating, _) => ask(format!("{} {}", call.name, call.arguments), None),
        },
    }
}

/// A workbook edit lands in its working copy, where it can be undone (spreadsheet-design §5.3,
/// document-editor-design §5.3): it runs without asking when file edits do.
fn evaluate_doc_edit(args: &serde_json::Value, doc: &str, view: &SessionView) -> Gate {
    let n = args.get("ops").and_then(|v| v.as_array()).map_or(0, Vec::len);
    let note = args.get("note").and_then(|v| v.as_str()).map(|n| format!(": {n}")).unwrap_or_default();
    match view.permission_mode {
        PermissionMode::AcceptEdits | PermissionMode::BypassPermissions => Gate::Allow,
        PermissionMode::Manual => Gate::Ask {
            summary: format!("edit {doc} ({n} change(s)){note}"),
            reason: None,
            proposed_rule: None,
            proposed_mode: Some(PermissionMode::AcceptEdits),
            proposed_grants: Vec::new(),
            own_browser: false,
        },
    }
}

/// A deck edit lands in the kernel's deck, not the file, and can be taken back
/// (presentation-deck-edit §5): asked like a workbook edit, with each step in the question.
fn evaluate_deck_edit(args: &serde_json::Value, deck: &str, view: &SessionView) -> Gate {
    evaluate_edit_in_kernel(deck_edit_summary(args, deck), view)
}

/// An edit of a document the kernel holds, which the user can take back: asked only in manual mode.
fn evaluate_edit_in_kernel(summary: String, view: &SessionView) -> Gate {
    match view.permission_mode {
        PermissionMode::AcceptEdits | PermissionMode::BypassPermissions => Gate::Allow,
        PermissionMode::Manual => Gate::Ask {
            summary,
            reason: None,
            proposed_rule: None,
            proposed_mode: Some(PermissionMode::AcceptEdits),
            proposed_grants: Vec::new(),
            own_browser: false,
        },
    }
}

/// "edit a.pptx (2 change(s)): note", then a line a step, as the person reads them.
fn deck_edit_summary(args: &serde_json::Value, deck: &str) -> String {
    const SHOWN: usize = 12;
    let edits = args.get("edits").and_then(|v| v.as_array()).map(Vec::as_slice).unwrap_or_default();
    let note = args.get("note").and_then(|v| v.as_str()).map(|n| format!(": {n}")).unwrap_or_default();
    let name = Path::new(deck).file_name().and_then(|n| n.to_str()).unwrap_or(deck);
    let mut out = format!("edit {name} ({} change(s)){note}", edits.len());
    let s = |e: &serde_json::Value, k: &str| e.get(k).and_then(|v| v.as_str()).map(|t| t.chars().take(80).collect::<String>());
    for e in edits.iter().take(SHOWN) {
        let line = match e.get("op").and_then(|v| v.as_str()).unwrap_or("") {
            "replace_text" => format!("replace \"{}\" with \"{}\"", s(e, "find").unwrap_or_default(), s(e, "text").unwrap_or_default()),
            "set_paragraphs" => {
                let texts: Vec<String> = e.get("paragraphs").and_then(|v| v.as_array()).map(|ps| ps.iter().filter_map(|p| s(p, "text")).collect()).unwrap_or_default();
                format!("set the text to \"{}\"", texts.join(" / "))
            }
            "format_text" => format!("format {}", s(e, "find").map(|f| format!("\"{f}\"")).unwrap_or_else(|| "the text".into())),
            "set_alignment" => format!("align {}", s(e, "find").map(|f| format!("\"{f}\"")).unwrap_or_else(|| "the text".into())),
            "add_text_box" => format!("add a text box \"{}\"", s(e, "text").unwrap_or_default()),
            "remove_shape" => "remove an object".into(),
            "set_shape_rect" => "move or resize an object".into(),
            "insert_slide" => "insert a slide".into(),
            "duplicate_slide" => "duplicate a slide".into(),
            "delete_slide" => "delete a slide".into(),
            "move_slide" => "move a slide".into(),
            "set_slide_layout" => format!("set a slide's layout to {}", s(e, "layout").unwrap_or_default()),
            "set_notes" => format!("set the speaker notes to \"{}\"", s(e, "text").unwrap_or_default()),
            "set_fill" => format!("fill an object with {}", s(e, "color").unwrap_or_default()),
            "set_background" => format!("set a slide's background to {}", s(e, "color").unwrap_or_default()),
            other => other.replace('_', " "),
        };
        out.push_str("\n- ");
        out.push_str(&line);
    }
    if edits.len() > SHOWN {
        out.push_str(&format!("\n- and {} more", edits.len() - SHOWN));
    }
    out
}

/// "edit a.docx directly (2 change(s)): note", then a line a step; or what a review settles.
fn docx_edit_summary(args: &serde_json::Value, docx: &str) -> String {
    const SHOWN: usize = 12;
    let name = Path::new(docx).file_name().and_then(|n| n.to_str()).unwrap_or(docx);
    let s = |e: &serde_json::Value, k: &str| e.get(k).and_then(|v| v.as_str()).map(|t| t.chars().take(80).collect::<String>()).unwrap_or_default();
    if let Some(action) = args.get("action").and_then(|v| v.as_str()) {
        let which = match args.get("ids").and_then(|v| v.as_array()) {
            Some(ids) => format!("{} tracked change(s)", ids.len()),
            None => "all tracked changes".into(),
        };
        return format!("{action} {which} in {name}");
    }
    let edits = args.get("edits").and_then(|v| v.as_array()).map(Vec::as_slice).unwrap_or_default();
    let note = args.get("note").and_then(|v| v.as_str()).map(|n| format!(": {n}")).unwrap_or_default();
    let mut out = format!("edit {name} directly ({} change(s)){note}", edits.len());
    for e in edits.iter().take(SHOWN) {
        let line = match e.get("op").and_then(|v| v.as_str()).unwrap_or("") {
            "replace_text" => format!("replace \"{}\" with \"{}\"", s(e, "find"), s(e, "replace")),
            "insert_text" => format!("insert \"{}\"", s(e, "text")),
            "delete_text" => format!("delete \"{}\"", s(e, "find")),
            "insert_paragraphs" => {
                let texts: Vec<String> = e.get("paragraphs").and_then(|v| v.as_array()).map(|ps| ps.iter().map(|p| s(p, "text")).collect()).unwrap_or_default();
                format!("insert \"{}\"", texts.join(" / "))
            }
            "set_cell_text" => format!("set a cell to \"{}\"", s(e, "text")),
            "add_comment" | "reply_comment" => format!("comment \"{}\"", s(e, "text")),
            other => other.replace('_', " "),
        };
        out.push_str("\n- ");
        out.push_str(&line);
    }
    if edits.len() > SHOWN {
        out.push_str(&format!("\n- and {} more", edits.len() - SHOWN));
    }
    out
}

/// Saving writes the user's file: judged like `write` when the call names the file by its
/// path; a document named by its id is asked about unless every action runs.
fn evaluate_doc_save(doc: &str, path: &str, view: &SessionView) -> Gate {
    if view.permission_mode == PermissionMode::BypassPermissions {
        return Gate::Allow;
    }
    let target = if path.is_empty() { doc } else { path };
    let summary = format!("save {target}");
    if !target.contains(['/', '.']) {
        return ask(summary, None);
    }
    let resolved = canonical_lossy(&view.env.resolve(target));
    if !workspace_roots(view).iter().any(|root| resolved.starts_with(root)) {
        return ask(summary, Some(OUTSIDE_WORKSPACE.into()));
    }
    if let Some(name) = protected::protected(&resolved, view) {
        return ask(summary, Some(protected_reason(&name)));
    }
    match view.permission_mode {
        PermissionMode::AcceptEdits => Gate::Allow,
        _ => Gate::Ask {
            summary,
            reason: None,
            proposed_rule: None,
            proposed_mode: Some(PermissionMode::AcceptEdits),
            proposed_grants: Vec::new(),
            own_browser: false,
        },
    }
}

/// A question whose answer applies to this call only.
fn ask(summary: String, reason: Option<String>) -> Gate {
    Gate::Ask {
        summary,
        reason,
        proposed_rule: None,
        proposed_mode: None,
        proposed_grants: Vec::new(),
        own_browser: false,
    }
}

fn evaluate_bash(command: &str, view: &SessionView) -> Gate {
    let rules: Vec<CommandRule> = view
        .config_rules
        .iter()
        .chain(view.session_rules.iter())
        .cloned()
        .collect();
    let home = dirs::home_dir();
    let ctx = ShellContext {
        cwd: view.env.cwd(),
        home: home.as_deref(),
        cdpath: std::env::var_os("CDPATH").is_some_and(|value| !value.is_empty()),
    };
    let analysis = rules::analyze(command, &ctx);
    // Deny and ask rules see every command the line may run: behind wrappers, inside
    // scripts, under an absolute path, and past git's global options.
    let matching = |decision: RuleDecision| {
        analysis.found.iter().find_map(|found| {
            rules::rule_forms(found)
                .iter()
                .find_map(|form| rules::first_match(&rules, form, decision))
        })
    };
    if let Some(rule) = matching(RuleDecision::Deny) {
        return Gate::Deny(format!(
            "The command is blocked by the rule `{}`{}",
            rule.prefix.join(" "),
            rule.reason.as_deref().map(|r| format!(": {r}")).unwrap_or_default()
        ));
    }
    if analysis.too_deep {
        return ask(command.to_string(), Some(TOO_DEEP.into()));
    }
    if let Some(reason) = critical_deletion(&analysis, view, home.as_deref()) {
        return ask(command.to_string(), Some(reason));
    }
    if let Some(rule) = matching(RuleDecision::Ask) {
        return ask(command.to_string(), Some(rule_reason(rule)));
    }
    let mode = view.permission_mode;
    if mode == PermissionMode::BypassPermissions {
        return Gate::Allow;
    }
    // Only a command line made of simple commands can run without asking.
    let top_level: Vec<&Found> = analysis.found.iter().filter(|found| found.top_level).collect();
    if !analysis.simple || top_level.is_empty() {
        return ask(command.to_string(), None);
    }
    let mut pending: Vec<&[String]> = Vec::new();
    for found in top_level {
        let words = &found.words;
        if rules::is_read_only(words, view.trusted) {
            continue;
        }
        if let Some(rule) = rules::first_match(&rules, words, RuleDecision::Allow) {
            // An allow rule never covers writing a protected path named on the command line.
            if let Some(name) = protected_operand(&words[rule.prefix.len()..], &found.cwds, view, home.as_deref()) {
                return ask(command.to_string(), Some(protected_reason(&name)));
            }
            continue;
        }
        if mode == PermissionMode::AcceptEdits
            && let Some(file_command) = rules::file_command(words)
        {
            match check_workspace_operands(&file_command, &found.cwds, view, home.as_deref()) {
                Ok(()) => continue,
                Err(Some(reason)) => return ask(command.to_string(), Some(reason)),
                Err(None) => {}
            }
        }
        pending.push(words);
    }
    if pending.is_empty() {
        return Gate::Allow;
    }
    // "Always allow" adds one prefix rule, so it is offered only when that rule covers every
    // command that asks.
    let proposed_rule = rules::propose_rule(pending[0])
        .filter(|rule| pending.iter().all(|words| rules::rule_matches(rule, words)));
    Gate::Ask {
        summary: command.to_string(),
        reason: None,
        proposed_rule,
        proposed_mode: None,
        proposed_grants: Vec::new(),
        own_browser: false,
    }
}

fn rule_reason(rule: &CommandRule) -> String {
    rule.reason
        .clone()
        .unwrap_or_else(|| format!("matches the rule `{}`", rule.prefix.join(" ")))
}

fn protected_reason(name: &str) -> String {
    format!("The path is protected ({name}).")
}

/// Why the command must be confirmed for what it deletes, whatever the mode.
fn critical_deletion(analysis: &Analysis, view: &SessionView, home: Option<&Path>) -> Option<String> {
    for target in rules::deletion_targets(analysis) {
        let (word, judged, cwds, contents) = match target {
            Target::Unknown(word) => {
                return Some(format!("Deleting {} cannot be checked before the command runs.", code(&word)));
            }
            Target::Input => {
                return Some("Deleting what xargs reads from its input cannot be checked before the command runs.".into());
            }
            Target::Path { word, cwds } => (word.clone(), emptied_directory(&word).unwrap_or(word), cwds, false),
            Target::Contents { word, cwds } => (word.clone(), word, cwds, true),
        };
        for cwd in &cwds {
            if let Some(what) = protected::critical(&rules::resolve_in(cwd, &judged, home), view) {
                return Some(if contents {
                    format!("Deleting everything in {} would remove {what}.", code(&word))
                } else {
                    format!("Deleting {} would remove {what}.", code(&word))
                });
            }
        }
    }
    None
}

/// `word` in backticks, unless it already has some.
fn code(word: &str) -> String {
    if word.contains('`') { word.to_string() } else { format!("`{word}`") }
}

/// The directory that `dir/*` and the like empty completely. A pattern with other
/// characters, such as `*.o`, only removes some entries and is judged as a path.
fn emptied_directory(word: &str) -> Option<String> {
    let trimmed = word.trim_end_matches('/');
    let last = trimmed.rsplit('/').next()?;
    let wildcard = last.strip_prefix('.').unwrap_or(last);
    if wildcard.is_empty() || !wildcard.chars().all(|c| c == '*' || c == '?') {
        return None;
    }
    let fixed: Vec<&str> = trimmed.split('/').take_while(|part| !has_glob(part)).collect();
    let fixed = fixed.join("/");
    Some(match fixed.as_str() {
        "" if word.starts_with('/') => "/".to_string(),
        "" => ".".to_string(),
        _ => fixed,
    })
}

fn has_glob(word: &str) -> bool {
    word.contains(['*', '?', '['])
}

/// The first protected path among a command's operands, including `--option=value` values.
fn protected_operand(words: &[String], cwds: &Cwds, view: &SessionView, home: Option<&Path>) -> Option<String> {
    let operands = words
        .iter()
        .filter_map(|word| match word.strip_prefix("--") {
            Some(option) => option.split_once('=').map(|(_, value)| value),
            None => (!word.starts_with('-')).then_some(word.as_str()),
        })
        .filter(|operand| !operand.is_empty() && !has_glob(operand));
    for operand in operands {
        let anchored = operand.starts_with('/') || operand == "~" || operand.starts_with("~/");
        let found = match cwds {
            Cwds::Known(dirs) => dirs
                .iter()
                .find_map(|cwd| protected::protected(&rules::resolve_in(cwd, operand, home), view)),
            Cwds::Unknown if anchored => protected::protected(&rules::resolve_in(Path::new("/"), operand, home), view),
            // Without the directory, only the names in the path itself can be judged.
            Cwds::Unknown => protected::protected_name(Path::new(operand)),
        };
        if found.is_some() {
            return found;
        }
    }
    None
}

/// Accept-edits runs `mkdir`, `touch`, `cp` and `mv` without asking when every operand is
/// inside the workspace from every directory the command may run in, is not a workspace
/// root and is not protected. `Err` carries the reason to show when the command asks.
fn check_workspace_operands(
    command: &FileCommand,
    cwds: &Cwds,
    view: &SessionView,
    home: Option<&Path>,
) -> Result<(), Option<String>> {
    let Cwds::Known(dirs) = cwds else {
        return Err(None);
    };
    let roots = workspace_roots(view);
    for operand in &command.operands {
        if has_glob(operand) {
            return Err(None);
        }
        for cwd in dirs {
            let path = canonical_lossy(&rules::resolve_in(cwd, operand, home));
            if !roots.iter().any(|root| path.starts_with(root)) {
                return Err(Some(OUTSIDE_WORKSPACE.into()));
            }
            if roots.contains(&path) {
                return Err(None);
            }
            if let Some(name) = protected::protected(&path, view) {
                return Err(Some(protected_reason(&name)));
            }
        }
    }
    if command.name == "cp" && copies_into_hard_link(command, dirs, home) {
        return Err(Some(HARD_LINK.into()));
    }
    Ok(())
}

/// Whether `cp` would write into an existing file with other hard links: the write changes
/// the file under every name it has, including names outside the workspace.
fn copies_into_hard_link(command: &FileCommand, cwds: &[PathBuf], home: Option<&Path>) -> bool {
    let Some((destination, sources)) = command.operands.split_last() else {
        return false;
    };
    cwds.iter().any(|cwd| {
        let destination = rules::resolve_in(cwd, destination, home);
        if destination.is_dir() {
            sources.iter().any(|source| {
                Path::new(source)
                    .file_name()
                    .is_some_and(|name| has_other_links(&destination.join(name)))
            })
        } else {
            has_other_links(&destination)
        }
    })
}

#[cfg(unix)]
fn has_other_links(path: &Path) -> bool {
    use std::os::unix::fs::MetadataExt;
    std::fs::metadata(path).is_ok_and(|meta| meta.is_file() && meta.nlink() > 1)
}

#[cfg(not(unix))]
fn has_other_links(_path: &Path) -> bool {
    false
}

fn workspace_roots(view: &SessionView) -> Vec<PathBuf> {
    view.env.workspace_roots().iter().map(|root| canonical_lossy(root)).collect()
}

fn evaluate_file_change(call: &ToolCall, args: &serde_json::Value, raw_path: &str, view: &SessionView) -> Gate {
    if view.permission_mode == PermissionMode::BypassPermissions {
        return Gate::Allow;
    }
    let summary = match call.name.as_str() {
        "write" => {
            let bytes = args.get("content").and_then(|v| v.as_str()).map_or(0, str::len);
            format!("write {raw_path} ({bytes} bytes)")
        }
        _ => {
            let changes = args.get("edits").and_then(|v| v.as_array()).map_or(0, Vec::len);
            format!("edit {raw_path} ({changes} change(s))")
        }
    };
    let path = canonical_lossy(&view.env.resolve(raw_path));
    if !workspace_roots(view).iter().any(|root| path.starts_with(root)) {
        return ask(summary, Some(OUTSIDE_WORKSPACE.into()));
    }
    if let Some(name) = protected::protected(&path, view) {
        return ask(summary, Some(protected_reason(&name)));
    }
    match view.permission_mode {
        PermissionMode::Manual => Gate::Ask {
            summary,
            reason: None,
            proposed_rule: None,
            proposed_mode: Some(PermissionMode::AcceptEdits),
            proposed_grants: Vec::new(),
        own_browser: false,
        },
        PermissionMode::AcceptEdits | PermissionMode::BypassPermissions => Gate::Allow,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_tools::LocalEnvironment;
    use serde_json::json;
    use std::sync::Arc;

    fn view(dir: &Path, mode: PermissionMode) -> SessionView {
        SessionView {
            permission_mode: mode,
            config_rules: vec![
                rule(&["git", "push"], RuleDecision::Ask, Some("Confirm before pushing")),
                rule(&["rm", "-rf"], RuleDecision::Deny, None),
                rule(&["cargo", "test"], RuleDecision::Allow, None),
            ],
            session_rules: vec![],
            trusted: false,
            env: Arc::new(LocalEnvironment::new(dir.to_path_buf())),
            data_dir: canonical_lossy(&dir.join("her-home")),
            own_folder: None,
            skill_roots: Arc::from([]),
            site_grants: vec![],
            browser: Arc::default(),
            desktop: Default::default(),
        }
    }

    fn rule(prefix: &[&str], decision: RuleDecision, reason: Option<&str>) -> CommandRule {
        CommandRule {
            prefix: prefix.iter().map(|w| w.to_string()).collect(),
            decision,
            reason: reason.map(str::to_string),
        }
    }

    fn bash(command: &str) -> ToolCall {
        ToolCall {
            id: "c".into(),
            name: "bash".into(),
            arguments: json!({ "command": command }).into(),
            arguments_error: None,
            provider_data: None,
        }
    }

    fn write(path: &str) -> ToolCall {
        ToolCall {
            id: "c".into(),
            name: "write".into(),
            arguments: json!({ "path": path, "content": "x" }).into(),
            arguments_error: None,
            provider_data: None,
        }
    }

    fn gate(command: &str, view: &SessionView) -> Gate {
        evaluate(&bash(command), ToolKind::Mutating, view)
    }

    fn doc_call(name: &str, args: serde_json::Value) -> ToolCall {
        ToolCall { id: "c".into(), name: name.into(), arguments: args.into(), arguments_error: None, provider_data: None }
    }

    #[test]
    fn workbook_edits_ask_only_when_file_edits_do() {
        let dir = tempfile::tempdir().unwrap();
        let edit = doc_call("doc_edit", json!({"doc": "model.xlsx", "ops": [{"op": "set_value", "sheet": "S", "cell": "B6", "value": 0.09}], "note": "WACC 9%"}));
        match evaluate(&edit, ToolKind::Mutating, &view(dir.path(), PermissionMode::Manual)) {
            Gate::Ask { summary, proposed_mode, .. } => {
                assert_eq!(summary, "edit model.xlsx (1 change(s)): WACC 9%");
                assert_eq!(proposed_mode, Some(PermissionMode::AcceptEdits));
            }
            other => panic!("{other:?}"),
        }
        assert!(matches!(evaluate(&edit, ToolKind::Mutating, &view(dir.path(), PermissionMode::AcceptEdits)), Gate::Allow));

        // Saving is writing the file: inside the workspace like `write`, outside always asked.
        let save = doc_call("doc_save", json!({"doc": "model.xlsx"}));
        assert!(matches!(evaluate(&save, ToolKind::Mutating, &view(dir.path(), PermissionMode::AcceptEdits)), Gate::Allow));
        let outside = doc_call("doc_save", json!({"doc": "/elsewhere/model.xlsx"}));
        assert!(matches!(evaluate(&outside, ToolKind::Mutating, &view(dir.path(), PermissionMode::AcceptEdits)), Gate::Ask { .. }));
        let by_id = doc_call("doc_save", json!({"doc": "3f2a9c0d1e2b4a5c"}));
        assert!(matches!(evaluate(&by_id, ToolKind::Mutating, &view(dir.path(), PermissionMode::AcceptEdits)), Gate::Ask { .. }));
        assert!(matches!(evaluate(&by_id, ToolKind::Mutating, &view(dir.path(), PermissionMode::BypassPermissions)), Gate::Allow));
    }

    #[test]
    fn deck_edits_ask_only_when_file_edits_do_and_saves_like_files() {
        let dir = tempfile::tempdir().unwrap();
        let edit = doc_call("deck_edit", json!({"deck": "/w/a.pptx", "note": "改标题", "edits": [{"op": "replace_text", "slide_id": "s", "shape_id": "t", "find": "Quarterly", "text": "年度"}, {"op": "delete_slide", "slide_id": "s2"}]}));
        match evaluate(&edit, ToolKind::Mutating, &view(dir.path(), PermissionMode::Manual)) {
            Gate::Ask { summary, proposed_mode, .. } => {
                assert_eq!(summary, "edit a.pptx (2 change(s)): 改标题\n- replace \"Quarterly\" with \"年度\"\n- delete a slide");
                assert_eq!(proposed_mode, Some(PermissionMode::AcceptEdits));
            }
            other => panic!("{other:?}"),
        }
        for mode in [PermissionMode::AcceptEdits, PermissionMode::BypassPermissions] {
            assert!(matches!(evaluate(&edit, ToolKind::Mutating, &view(dir.path(), mode)), Gate::Allow));
        }
        // Deferred, through call_tool: judged as itself, not as an unknown change.
        let deferred = doc_call(crate::mcp::tools::CALL_TOOL, json!({"name": "deck_edit", "arguments": {"deck": "a.pptx", "edits": []}}));
        let inner = through_call_tool(&deferred).unwrap();
        assert_eq!(inner.name, "deck_edit");
        assert!(matches!(evaluate(&inner, ToolKind::Mutating, &view(dir.path(), PermissionMode::Manual)), Gate::Ask { .. }));
        assert!(matches!(evaluate(&inner, ToolKind::Mutating, &view(dir.path(), PermissionMode::AcceptEdits)), Gate::Allow));
        let save = doc_call("deck_save", json!({"deck": "a.pptx"}));
        assert!(matches!(evaluate(&save, ToolKind::Mutating, &view(dir.path(), PermissionMode::AcceptEdits)), Gate::Allow));
        assert!(matches!(evaluate(&save, ToolKind::Mutating, &view(dir.path(), PermissionMode::Manual)), Gate::Ask { .. }));
        let outside = doc_call("deck_save", json!({"deck": "/elsewhere/a.pptx"}));
        assert!(matches!(evaluate(&outside, ToolKind::Mutating, &view(dir.path(), PermissionMode::AcceptEdits)), Gate::Ask { .. }));
    }

    #[test]
    fn suggested_docx_edits_never_ask_direct_ones_and_reviews_ask_like_deck_edits() {
        let dir = tempfile::tempdir().unwrap();
        let edits = json!([{"op": "replace_text", "paragraph": "10000002", "find": "百分之十", "replace": "一成"}]);
        let suggest = doc_call("docx_edit", json!({"docx": "/w/a.docx", "edits": edits, "note": "润色"}));
        let explicit = doc_call("docx_edit", json!({"docx": "/w/a.docx", "edits": edits, "mode": "suggest"}));
        let direct = doc_call("docx_edit", json!({"docx": "/w/a.docx", "edits": edits, "mode": "direct", "note": "润色"}));
        let review = doc_call("docx_review", json!({"docx": "/w/a.docx", "action": "accept", "ids": [3]}));
        for mode in [PermissionMode::Manual, PermissionMode::AcceptEdits, PermissionMode::BypassPermissions] {
            assert!(matches!(evaluate(&suggest, ToolKind::Mutating, &view(dir.path(), mode)), Gate::Allow));
            assert!(matches!(evaluate(&explicit, ToolKind::Mutating, &view(dir.path(), mode)), Gate::Allow));
        }
        match evaluate(&direct, ToolKind::Mutating, &view(dir.path(), PermissionMode::Manual)) {
            Gate::Ask { summary, proposed_mode, .. } => {
                assert_eq!(summary, "edit a.docx directly (1 change(s)): 润色\n- replace \"百分之十\" with \"一成\"");
                assert_eq!(proposed_mode, Some(PermissionMode::AcceptEdits));
            }
            other => panic!("{other:?}"),
        }
        match evaluate(&review, ToolKind::Mutating, &view(dir.path(), PermissionMode::Manual)) {
            Gate::Ask { summary, .. } => assert_eq!(summary, "accept 1 tracked change(s) in a.docx"),
            other => panic!("{other:?}"),
        }
        for mode in [PermissionMode::AcceptEdits, PermissionMode::BypassPermissions] {
            assert!(matches!(evaluate(&direct, ToolKind::Mutating, &view(dir.path(), mode)), Gate::Allow));
            assert!(matches!(evaluate(&review, ToolKind::Mutating, &view(dir.path(), mode)), Gate::Allow));
        }
        // Deferred, through call_tool: judged as itself.
        let deferred = doc_call(crate::mcp::tools::CALL_TOOL, json!({"name": "docx_edit", "arguments": {"docx": "a.docx", "edits": []}}));
        assert!(matches!(evaluate(&through_call_tool(&deferred).unwrap(), ToolKind::Mutating, &view(dir.path(), PermissionMode::Manual)), Gate::Allow));
        // Saving as deck_save does.
        let save = doc_call("docx_save", json!({"docx": "a.docx"}));
        assert!(matches!(evaluate(&save, ToolKind::Mutating, &view(dir.path(), PermissionMode::AcceptEdits)), Gate::Allow));
        assert!(matches!(evaluate(&save, ToolKind::Mutating, &view(dir.path(), PermissionMode::Manual)), Gate::Ask { .. }));
        let outside = doc_call("docx_save", json!({"docx": "/elsewhere/a.docx"}));
        assert!(matches!(evaluate(&outside, ToolKind::Mutating, &view(dir.path(), PermissionMode::AcceptEdits)), Gate::Ask { .. }));
    }

    /// The reason of an `Ask` that offers nothing for the rest of the session.
    fn one_time_reason(gate: Gate) -> String {
        match gate {
            Gate::Ask {
                reason: Some(reason),
                proposed_rule: None,
                proposed_mode: None,
                ..
            } => reason,
            other => panic!("expected a one-time question, got {other:?}"),
        }
    }

    fn proposal(gate: Gate) -> Option<String> {
        match gate {
            Gate::Ask { proposed_rule, .. } => proposed_rule.map(|rule| rule.prefix.join(" ")),
            other => panic!("expected a question, got {other:?}"),
        }
    }

    fn is_deny(gate: &Gate) -> bool {
        matches!(gate, Gate::Deny(_))
    }

    /// A workspace `a/proj` inside a temporary directory, so that `..` and `../..` stay inside
    /// it too: the temporary directory itself may sit right under a top-level one, as in `/tmp`.
    fn workspace() -> (tempfile::TempDir, PathBuf) {
        let dir = tempfile::tempdir().unwrap();
        let workspace = dir.path().join("a").join("proj");
        std::fs::create_dir_all(&workspace).unwrap();
        (dir, workspace)
    }

    #[test]
    fn manual_mode_commands() {
        let dir = tempfile::tempdir().unwrap();
        let manual = view(dir.path(), PermissionMode::Manual);
        assert_eq!(gate("ls -la", &manual), Gate::Allow);
        assert_eq!(gate("cargo test --all", &manual), Gate::Allow);
        assert_eq!(gate("cd src && cargo test", &manual), Gate::Allow, "cd changes nothing by itself");
        assert_eq!(proposal(gate("npm install", &manual)).as_deref(), Some("npm install"));
        assert_eq!(proposal(gate("ls > out.txt", &manual)), None);
        assert!(matches!(gate("mkdir src/new", &manual), Gate::Ask { .. }));
        assert!(is_deny(&gate("cd x && rm -rf /", &manual)));
        assert!(is_deny(&gate("echo $(rm -rf /)", &manual)));
        assert_eq!(one_time_reason(gate("git push origin main", &manual)), "Confirm before pushing");

        let mut session_allowed = view(dir.path(), PermissionMode::Manual);
        session_allowed
            .session_rules
            .push(rules::propose_rule(&["npm".into(), "install".into()]).unwrap());
        session_allowed.session_rules.push(rule(&["cp"], RuleDecision::Allow, None));
        assert_eq!(gate("npm install left-pad", &session_allowed), Gate::Allow);
        assert_eq!(gate("cp a.txt b.txt", &session_allowed), Gate::Allow);
        assert!(one_time_reason(gate("cp hook .git/hooks/pre-commit", &session_allowed)).contains(".git"));
        assert!(one_time_reason(gate("cp x ~/.bashrc", &session_allowed)).contains(".bashrc"));
        assert!(one_time_reason(gate("cd .git && cp x hooks/pre-commit", &session_allowed)).contains(".git"));
        assert!(one_time_reason(gate("cd - && cp x .bashrc", &session_allowed)).contains(".bashrc"));
        assert!(one_time_reason(gate("cd - && cp x /etc/.profile", &session_allowed)).contains(".profile"));
        assert!(one_time_reason(gate("cp --target=.npmrc x", &session_allowed)).contains(".npmrc"));
    }

    #[test]
    fn rules_see_every_command_the_line_runs() {
        let dir = tempfile::tempdir().unwrap();
        let mut bypass = view(dir.path(), PermissionMode::BypassPermissions);
        bypass.config_rules.push(rule(&["curl"], RuleDecision::Deny, Some("No network")));
        for denied in [
            "sudo rm -rf build",
            "/bin/rm -rf build",
            "env A=1 rm -rf build",
            "bash -c 'rm -rf build'",
            "sh -c \"cd x && rm -rf $Y\"",
            "eval rm -rf build",
            "trap 'rm -rf build' EXIT",
            "find . -name x -exec rm -rf {} +",
            "ls | xargs rm -rf",
            "/usr/bin/curl https://example.com",
            "timeout 5 curl x",
        ] {
            assert!(is_deny(&gate(denied, &bypass)), "{denied}");
        }
        for asked in [
            "/usr/bin/git push",
            "git -C .. push --force",
            "git --no-pager -c a=b push",
            "bash -lc 'git push'",
            "sudo git push",
            "cd sub; git push",
        ] {
            assert_eq!(one_time_reason(gate(asked, &bypass)), "Confirm before pushing", "{asked}");
        }
        // Allow rules match the words as written only.
        let manual = view(dir.path(), PermissionMode::Manual);
        assert!(matches!(gate("sudo cargo test", &manual), Gate::Ask { .. }));
        assert!(matches!(gate("/usr/bin/cargo test", &manual), Gate::Ask { .. }));
        assert!(matches!(gate("bash -c 'cargo test'", &manual), Gate::Ask { .. }));
    }

    #[test]
    fn accept_edits_runs_file_commands_in_the_workspace() {
        let (_dir, workspace) = workspace();
        std::fs::create_dir(workspace.join("src")).unwrap();
        let edits = view(&workspace, PermissionMode::AcceptEdits);
        assert_eq!(gate("mkdir -p src/new && touch src/new/a.rs", &edits), Gate::Allow);
        assert_eq!(gate("cp -r src backup && mv backup old", &edits), Gate::Allow);
        assert_eq!(gate("cd src && mkdir new && cd new && touch a.rs", &edits), Gate::Allow);
        assert!(one_time_reason(gate("mkdir /tmp/her-elsewhere", &edits)).contains("outside"));
        assert!(one_time_reason(gate("cp -r src ../copy", &edits)).contains("outside"));
        assert!(one_time_reason(gate("cd .. && mkdir x", &edits)).contains("outside"));
        assert!(one_time_reason(gate("cd src && mkdir ../../x", &edits)).contains("outside"));
        assert!(one_time_reason(gate("mkdir .git/hooks", &edits)).contains(".git"));
        assert!(one_time_reason(gate("touch .envrc", &edits)).contains(".envrc"));
        assert!(one_time_reason(gate("mkdir -p .aws && touch .aws/config", &edits)).contains(".aws"));
        assert!(matches!(gate("mv src .", &edits), Gate::Ask { reason: None, .. }), "the root itself");
        assert!(matches!(gate("cd src && mv x ..", &edits), Gate::Ask { reason: None, .. }), "the root from src");
        assert!(matches!(gate("touch *.rs", &edits), Gate::Ask { reason: None, .. }));
        assert!(matches!(gate("mkdir -m 700 x", &edits), Gate::Ask { reason: None, .. }));
        assert!(matches!(gate("cd $X && touch a", &edits), Gate::Ask { reason: None, .. }));
        assert!(matches!(gate("npm install", &edits), Gate::Ask { .. }));
    }

    #[cfg(unix)]
    #[test]
    fn accept_edits_does_not_copy_into_hard_links() {
        let (dir, workspace) = workspace();
        let outside = dir.path().join("outside.txt");
        std::fs::write(&outside, "keep").unwrap();
        std::fs::hard_link(&outside, workspace.join("linked.txt")).unwrap();
        std::fs::create_dir(workspace.join("dest")).unwrap();
        std::fs::hard_link(&outside, workspace.join("dest/new.txt")).unwrap();
        std::fs::write(workspace.join("new.txt"), "x").unwrap();
        std::fs::write(workspace.join("plain.txt"), "x").unwrap();
        let edits = view(&workspace, PermissionMode::AcceptEdits);
        assert!(one_time_reason(gate("cp new.txt linked.txt", &edits)).contains("hard links"));
        assert!(one_time_reason(gate("cp new.txt dest", &edits)).contains("hard links"));
        assert_eq!(gate("cp new.txt plain.txt", &edits), Gate::Allow);
        assert_eq!(gate("cp new.txt fresh.txt", &edits), Gate::Allow);
        assert_eq!(gate("mv new.txt linked.txt", &edits), Gate::Allow, "mv replaces the name");
    }

    #[test]
    fn bypass_stops_only_for_rules_and_critical_deletions() {
        let (dir, workspace) = workspace();
        let bypass = view(&workspace, PermissionMode::BypassPermissions);
        for allowed in [
            "npm install",
            "rm -r build",
            "rm -f *.o",
            "rm -r build/*",
            "cd build && rm -r *",
            "find . -name '*.pyc' -delete",
            "find build -delete",
            "echo hi > .git/description",
            "cd /tmp && rm -r her-scratch",
        ] {
            assert_eq!(gate(allowed, &bypass), Gate::Allow, "{allowed}");
        }
        assert!(is_deny(&gate("rm -rf build", &bypass)), "deny rules apply in every mode");
        assert_eq!(one_time_reason(gate("git push", &bypass)), "Confirm before pushing");

        let critical = |command: &str| one_time_reason(gate(command, &bypass));
        assert!(critical("rm -r /").contains("file system root"));
        assert!(critical("rm -r /usr").contains("top-level"));
        assert!(critical("rm -r /*").contains("file system root"));
        assert!(critical("sudo rm -r ~").contains("home directory"));
        assert!(critical("rm -r ~/*").contains("home directory"));
        assert!(critical("rm -r .").contains("workspace"));
        assert!(critical("rm -r *").contains("workspace"));
        assert!(critical("rm -r ..").contains("workspace"));
        assert!(critical(&format!("rmdir {}", dir.path().display())).contains("workspace"));
        assert!(critical("bash -c 'rm -r ../..'").contains("workspace"));
        assert!(critical("rm -r \"$TARGET\"").contains("cannot be checked"));
        assert_eq!(critical("rm -r `pwd`"), "Deleting `…` cannot be checked before the command runs.");
        // Following cd and its relatives.
        assert!(critical("cd .. && rm -r proj").contains("workspace"));
        assert!(critical("cd build; rm -r *").contains("workspace"), "if the cd fails, * is the workspace");
        assert!(critical("cd build || rm -r *").contains("workspace"));
        assert!(critical("cd ..; rmdir proj").contains("workspace"));
        assert!(critical("(cd .. && rm -r proj)").contains("workspace"));
        assert!(critical("pushd .. && rm -r proj").contains("workspace"));
        assert!(critical("env -C .. rm -r proj").contains("workspace"));
        assert!(critical("sh -c 'cd .. && rm -r proj'").contains("workspace"));
        assert!(critical("cd && rm -r *").contains("home directory"));
        assert!(critical("cd - && rm -r proj").contains("cannot be checked"));
        // Scripts, find and xargs.
        assert!(critical("trap 'rm -r ..' EXIT").contains("workspace"));
        assert!(critical("eval 'rm -r ..'").contains("workspace"));
        assert!(critical("find . -delete").contains("everything in `.`"));
        assert!(critical("find ~ -type f -exec rm {} +").contains("home directory"));
        assert!(critical("find . ! -name keep -delete").contains("workspace"));
        assert!(critical("ls | xargs rm -r").contains("xargs"));
        // The same holds in the other modes, and allow rules do not change it.
        let mut manual = view(&workspace, PermissionMode::Manual);
        manual.session_rules.push(rule(&["rm"], RuleDecision::Allow, None));
        assert_eq!(gate("rm -r build", &manual), Gate::Allow);
        assert!(one_time_reason(gate("rm -r ~", &manual)).contains("home directory"));
        assert!(one_time_reason(gate("cd .. && rm -r proj", &manual)).contains("workspace"));
    }

    #[test]
    fn deeply_nested_scripts_ask_in_every_mode() {
        let dir = tempfile::tempdir().unwrap();
        let mut command = "echo hi".to_string();
        for _ in 0..9 {
            command = format!("eval '{}'", command.replace('\'', r"'\''"));
        }
        for mode in PermissionMode::ALL {
            assert_eq!(one_time_reason(gate(&command, &view(dir.path(), mode))), TOO_DEEP, "{mode}");
        }
    }

    #[test]
    fn always_allow_is_narrow_and_covers_the_whole_command() {
        let dir = tempfile::tempdir().unwrap();
        let manual = view(dir.path(), PermissionMode::Manual);
        let offered = |command: &str| proposal(gate(command, &manual));
        assert_eq!(offered("npm run build").as_deref(), Some("npm run build"));
        assert_eq!(offered("python3 -m pytest -x").as_deref(), Some("python3 -m pytest"));
        assert_eq!(offered("cd web && npm install").as_deref(), Some("npm install"));
        assert_eq!(offered("npm install && cargo test").as_deref(), Some("npm install"));
        assert_eq!(offered("npm install && npm install x").as_deref(), Some("npm install"));
        // One rule cannot cover two different commands.
        assert_eq!(offered("npm install && make"), None);
        // Rules that would allow any code are never offered.
        for broad in [
            "python3 -c 'print(1)'",
            "bash -lc 'make'",
            "node -e 'x'",
            "npm run",
            "sudo make install",
            "env FOO=1 make",
            "git -c core.pager=less log",
            "xargs touch",
            "find . -newer x -exec touch {} +",
        ] {
            assert_eq!(offered(broad), None, "{broad}");
        }
    }

    #[test]
    fn file_changes() {
        let dir = tempfile::tempdir().unwrap();
        let manual = view(dir.path(), PermissionMode::Manual);
        match evaluate(&write("src/a.rs"), ToolKind::Mutating, &manual) {
            Gate::Ask {
                reason: None,
                proposed_mode: Some(PermissionMode::AcceptEdits),
                ..
            } => {}
            other => panic!("unexpected {other:?}"),
        }
        let edits = view(dir.path(), PermissionMode::AcceptEdits);
        assert_eq!(evaluate(&write("src/a.rs"), ToolKind::Mutating, &edits), Gate::Allow);
        let outside = one_time_reason(evaluate(&write("/etc/hosts"), ToolKind::Mutating, &edits));
        assert!(outside.contains("outside the workspace"));
        assert!(one_time_reason(evaluate(&write("../escape.txt"), ToolKind::Mutating, &edits)).contains("outside"));
        for protected in [
            ".git/hooks/pre-commit",
            ".vscode/settings.json",
            "web/.npmrc",
            ".mcp.json",
            ".aws/config",
            ".ssh/authorized_keys",
            ".kube/config",
            ".agents/skills/x.md",
            "her-home/config.toml",
        ] {
            let reason = one_time_reason(evaluate(&write(protected), ToolKind::Mutating, &edits));
            assert!(reason.contains("protected"), "{protected}: {reason}");
        }
        // A write through a dangling link creates its target, so the target is what is checked.
        let (_root, workspace) = workspace();
        let edits = view(&workspace, PermissionMode::AcceptEdits);
        std::fs::create_dir_all(workspace.join(".git/hooks")).unwrap();
        std::os::unix::fs::symlink("../outside/new.txt", workspace.join("escape")).unwrap();
        std::os::unix::fs::symlink(".git/hooks/pre-commit", workspace.join("hook")).unwrap();
        std::os::unix::fs::symlink("hook", workspace.join("chain")).unwrap();
        std::os::unix::fs::symlink("docs/new.md", workspace.join("inside")).unwrap();
        assert!(one_time_reason(evaluate(&write("escape"), ToolKind::Mutating, &edits)).contains("outside"));
        assert!(one_time_reason(evaluate(&write("hook"), ToolKind::Mutating, &edits)).contains("protected"));
        assert!(one_time_reason(evaluate(&write("chain"), ToolKind::Mutating, &edits)).contains("protected"));
        assert_eq!(evaluate(&write("inside"), ToolKind::Mutating, &edits), Gate::Allow);

        let bypass = view(dir.path(), PermissionMode::BypassPermissions);
        assert_eq!(evaluate(&write("/etc/hosts"), ToolKind::Mutating, &bypass), Gate::Allow);
        assert_eq!(evaluate(&write(".git/config"), ToolKind::Mutating, &bypass), Gate::Allow);
        let read = ToolCall {
            id: "c".into(),
            name: "read".into(),
            arguments: json!({"path": "/etc/hosts"}).into(),
            arguments_error: None,
            provider_data: None,
        };
        assert_eq!(evaluate(&read, ToolKind::ReadOnly, &manual), Gate::Allow);
    }

    #[test]
    fn emptied_directories() {
        assert_eq!(emptied_directory("*").as_deref(), Some("."));
        assert_eq!(emptied_directory("/*").as_deref(), Some("/"));
        assert_eq!(emptied_directory("/*/*").as_deref(), Some("/"));
        assert_eq!(emptied_directory("~/*").as_deref(), Some("~"));
        assert_eq!(emptied_directory("build/.*").as_deref(), Some("build"));
        assert_eq!(emptied_directory("*/").as_deref(), Some("."));
        assert_eq!(emptied_directory("*.o"), None);
        assert_eq!(emptied_directory("build"), None);
        assert_eq!(emptied_directory("/"), None);
    }
}
