//! The skills in use when the context is compacted, kept with their instructions so that the
//! work goes on following them (docs/kernel/skill-design.md §7.2).

use her_llm::AssistantContent;
use her_llm::Message;
use her_llm::ToolCall;
use her_tools::Environment;
use her_tools::OutputLimits;
use std::collections::HashMap;
use std::collections::HashSet;
use std::path::Path;
use std::path::PathBuf;

use super::SkillRef;
use crate::commands;
use crate::context::estimate_text;
use crate::store::Entry;
use crate::store::EntryKind;
use crate::store::SkillText;

/// The most of one skill's instructions a compaction keeps.
pub(crate) const MAX_KEPT_TOKENS: u64 = 5_000;
/// Commands that print a file: a skill read with one of them is in use.
const READERS: &[&str] = &["cat", "head", "tail", "sed", "less", "more", "bat", "awk"];

/// A skill in use, and what the last compaction kept of it.
pub(crate) struct InUse {
    skill: SkillRef,
    previous: Option<String>,
}

/// The SKILL.md files used in `window` (the history since the last compaction), most recent
/// first: mentioned with `/skill:name`, read with read or printed with bash. Cheap, without touching
/// the file system, since it runs with the session locked.
pub(crate) fn used_files(window: &[&Entry], env: &dyn Environment) -> Vec<PathBuf> {
    let failed: HashSet<&str> = window
        .iter()
        .filter_map(|entry| match &entry.kind {
            EntryKind::Message {
                message: Message::ToolResult(result),
                ..
            } if result.is_error => Some(result.call_id.as_str()),
            _ => None,
        })
        .collect();
    let mut out = Vec::new();
    for entry in window.iter().rev() {
        match &entry.kind {
            EntryKind::Message {
                message: Message::User(_),
                skills,
                skill,
            } => {
                // Mentioned last in the message, used last.
                out.extend(skill.iter().map(|legacy| legacy.path.clone()));
                out.extend(skills.iter().rev().map(|mentioned| mentioned.path.clone()));
            }
            EntryKind::Message {
                message: Message::Assistant(assistant),
                ..
            } => {
                for content in assistant.content.iter().rev() {
                    let AssistantContent::ToolCall(call) = content else { continue };
                    if failed.contains(call.id.as_str()) {
                        continue;
                    }
                    out.extend(skill_files_read(call).iter().map(|path| env.resolve(path)));
                }
            }
            _ => {}
        }
    }
    out
}

/// The SKILL.md files a tool call reads with read, or prints with bash.
pub(crate) fn skill_files_read(call: &ToolCall) -> Vec<String> {
    // Before parsing the arguments, which may be a whole file written.
    if !matches!(call.name.as_str(), "read" | "bash") {
        return Vec::new();
    }
    let args = call.arguments.to_value();
    let arg = |name: &str| args.get(name).and_then(|v| v.as_str()).unwrap_or_default();
    let paths: Vec<&str> = match call.name.as_str() {
        "read" => vec![arg("path")].into_iter().filter(|p| is_skill_file(p)).collect(),
        "bash" => printed_skill_files(arg("command")),
        _ => Vec::new(),
    };
    paths.into_iter().map(str::to_string).collect()
}

/// The session's skills behind `used` (from [`used_files`]), most recent first, then the ones
/// the last compaction kept, which were used before.
pub(crate) fn in_use(used: &[PathBuf], previous: &[SkillText], skills: &[SkillRef]) -> Vec<InUse> {
    let real = |path: &Path| std::fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf());
    let by_path: HashMap<PathBuf, &SkillRef> = skills.iter().map(|skill| (real(&skill.path), skill)).collect();
    let mut seen = HashSet::new();
    let mut out = Vec::new();
    let mut add = |skill: SkillRef, previous: Option<String>| {
        if seen.insert(real(&skill.path)) {
            out.push(InUse { skill, previous });
        }
    };
    for path in used {
        if let Some(skill) = by_path.get(&real(path)) {
            add((*skill).clone(), None);
        }
    }
    for kept in previous {
        // Still a skill of the session, or kept as it was: the instructions are in the log.
        let skill = by_path.get(&real(&kept.path)).map(|s| (*s).clone()).unwrap_or_else(|| SkillRef {
            name: kept.name.clone(),
            path: kept.path.clone(),
            scope: super::SkillScope::Extra,
            model_invocable: true,
            argument_hint: None,
        });
        add(skill, Some(kept.content.clone()));
    }
    out
}

/// The SKILL.md files that a bash command prints with `cat` and the like.
fn printed_skill_files(command: &str) -> Vec<&str> {
    let words: Vec<&str> = command
        .split(|c: char| c.is_whitespace() || matches!(c, ';' | '|' | '&' | '(' | ')'))
        .map(|word| word.trim_matches(|c| c == '\'' || c == '"'))
        .filter(|word| !word.is_empty())
        .collect();
    if !words.iter().any(|word| READERS.contains(word)) {
        return Vec::new();
    }
    words.into_iter().filter(|word| is_skill_file(word)).collect()
}

fn is_skill_file(path: &str) -> bool {
    Path::new(path)
        .file_name()
        .is_some_and(|name| name.eq_ignore_ascii_case(super::SKILL_FILE))
}

/// Each skill's `<skill>` block, at most [`MAX_KEPT_TOKENS`], read again from its file: the
/// current instructions. A file that is gone keeps what the last compaction kept, if anything.
pub(crate) fn render(used: &[InUse], limits: &OutputLimits) -> Vec<(SkillText, u64)> {
    used.iter()
        .filter_map(|used| {
            let content = match std::fs::read_to_string(&used.skill.path) {
                Ok(file) => fit(&used.skill, super::body(&file), limits),
                Err(_) => used.previous.clone()?,
            };
            let tokens = estimate_text(&content);
            Some((
                SkillText {
                    name: used.skill.name.clone(),
                    path: used.skill.path.clone(),
                    content,
                },
                tokens,
            ))
        })
        .collect()
}

/// The block within [`MAX_KEPT_TOKENS`], cut at the end.
fn fit(skill: &SkillRef, body: &str, limits: &OutputLimits) -> String {
    let mut tokens = MAX_KEPT_TOKENS as usize;
    loop {
        let capped = OutputLimits {
            max_tokens: tokens.min(limits.max_tokens),
            ..limits.clone()
        };
        let block = commands::skill_block(skill, body, &capped);
        if estimate_text(&block) <= MAX_KEPT_TOKENS || tokens < 64 {
            return block;
        }
        tokens = tokens * 3 / 4;
    }
}

/// The skills that fit in `budget` tokens, most recently used first.
pub(crate) fn select(rendered: &[(SkillText, u64)], budget: u64) -> Vec<SkillText> {
    let mut left = budget;
    let mut out = Vec::new();
    for (skill, tokens) in rendered {
        if *tokens <= left {
            left -= tokens;
            out.push(skill.clone());
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_llm::AssistantMessage;
    use her_llm::StopReason;
    use her_llm::ToolCall;
    use her_llm::ToolResultMessage;
    use her_llm::UserContent;
    use her_llm::UserMessage;
    use her_tools::LocalEnvironment;
    use serde_json::json;
    use std::sync::Arc;

    fn entry(kind: EntryKind) -> Entry {
        Entry {
            id: "e".into(),
            parent: None,
            ts: 0,
            kind,
        }
    }

    fn calls(calls: &[(&str, &str, serde_json::Value)]) -> Entry {
        entry(EntryKind::Message {
            message: Message::Assistant(Arc::new(AssistantMessage {
                content: calls
                    .iter()
                    .map(|(id, name, args)| {
                        AssistantContent::ToolCall(ToolCall {
                            id: id.to_string(),
                            name: name.to_string(),
                            arguments: args.clone().into(),
                            arguments_error: None,
                            provider_data: None,
                        })
                    })
                    .collect(),
                provider: "t".into(),
                api: "t".into(),
                model: "t".into(),
                response_id: None,
                usage: Default::default(),
                stop_reason: StopReason::ToolUse,
                error: None,
                ts: 0,
                duration_ms: None,
            })),
            skills: Vec::new(),
            skill: None,
        })
    }

    fn result(id: &str, is_error: bool) -> Entry {
        entry(EntryKind::Message {
            message: Message::ToolResult(Arc::new(ToolResultMessage {
                call_id: id.into(),
                tool_name: "read".into(),
                content: vec![UserContent::Text { text: "x".into() }],
                is_error,
                details: None,
                ts: 0,
                duration_ms: None,
            })),
            skills: Vec::new(),
            skill: None,
        })
    }

    fn skill(dir: &Path, name: &str) -> SkillRef {
        let path = dir.join(name).join("SKILL.md");
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(&path, format!("---\nname: {name}\ndescription: d\n---\nSteps for {name}.\n")).unwrap();
        SkillRef {
            name: name.into(),
            path,
            scope: super::super::SkillScope::User,
            model_invocable: true,
            argument_hint: None,
        }
    }

    #[test]
    fn reads_invocations_and_previous_ones_count_most_recent_first() {
        let dir = tempfile::tempdir().unwrap();
        let base = std::fs::canonicalize(dir.path()).unwrap();
        let skills = vec![
            skill(&base, "alpha"),
            skill(&base, "beta"),
            skill(&base, "gamma"),
            skill(&base, "delta"),
            skill(&base, "unused"),
        ];
        let env = LocalEnvironment::new(base.clone());
        let path = |name: &str| base.join(name).join("SKILL.md").display().to_string();
        let window = [
            entry(EntryKind::Message {
                message: Message::from(UserMessage::text("use /skill:alpha")),
                skills: vec![SkillText {
                    name: "alpha".into(),
                    path: base.join("alpha/SKILL.md"),
                    content: "<skill name=\"alpha\">…</skill>".into(),
                }],
                skill: None,
            }),
            calls(&[("c1", "read", json!({ "path": "beta/SKILL.md" }))]),
            result("c1", false),
            calls(&[("c2", "bash", json!({ "command": format!("cat '{}' && ls", path("gamma")) }))]),
            result("c2", false),
            // A failed read and a mere mention do not count.
            calls(&[
                ("c3", "read", json!({ "path": path("unused") })),
                ("c4", "bash", json!({ "command": format!("ls {}", path("unused")) })),
            ]),
            result("c3", true),
            result("c4", false),
        ];
        let window: Vec<&Entry> = window.iter().collect();
        let previous = vec![SkillText {
            name: "delta".into(),
            path: base.join("delta/SKILL.md"),
            content: "old delta".into(),
        }];
        let used = in_use(&used_files(&window, &env), &previous, &skills);
        let names: Vec<&str> = used.iter().map(|u| u.skill.name.as_str()).collect();
        assert_eq!(names, vec!["gamma", "beta", "alpha", "delta"]);

        // Files are read again; one that is gone keeps the last compaction's copy.
        std::fs::remove_file(base.join("delta/SKILL.md")).unwrap();
        let rendered = render(&used, &OutputLimits::default());
        assert_eq!(rendered.len(), 4);
        assert!(rendered[0].0.content.contains("Steps for gamma."));
        assert!(rendered[0].0.content.starts_with("<skill name=\"gamma\""));
        assert_eq!(rendered[3].0.content, "old delta");

        // What does not fit is skipped; a smaller, older one may still fit after it.
        let budget = rendered[0].1 + rendered[1].1 + rendered[3].1;
        let kept = select(&rendered, budget);
        assert_eq!(kept.iter().map(|k| k.name.as_str()).collect::<Vec<_>>(), vec!["gamma", "beta", "delta"]);
        assert!(select(&rendered, 0).is_empty());
    }

    #[test]
    fn long_instructions_are_cut_to_the_cap() {
        let dir = tempfile::tempdir().unwrap();
        let big = skill(dir.path(), "big");
        std::fs::write(&big.path, format!("---\ndescription: d\n---\n{}", "word ".repeat(20_000))).unwrap();
        let used = [InUse {
            skill: big.clone(),
            previous: None,
        }];
        let (kept, tokens) = &render(&used, &OutputLimits::default())[0];
        assert!(*tokens <= MAX_KEPT_TOKENS, "{tokens}");
        assert!(kept.content.contains(&format!("[Truncated. Read {} for the rest.]", big.path.display())));
        assert_eq!(printed_skill_files("head -n 50 \"/s/x/SKILL.md\"; echo"), vec!["/s/x/SKILL.md"]);
        assert!(printed_skill_files("rm /s/x/SKILL.md").is_empty());
    }
}
