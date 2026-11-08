//! The user's commands and mentions: `/compact` or `/context` at the start of the input, and `/skill:name`
//! anywhere in it, which sends the skill's instructions along with the message
//! (docs/kernel/skill-design.md §6.3).

use her_llm::UserContent;
use her_protocol::api::CommandInfo;
use her_tools::OutputLimits;
use std::collections::HashSet;
use std::ops::Range;

use crate::skills;
use crate::skills::SkillEntry;
use crate::skills::SkillRef;
use crate::skills::SkillState;
use crate::store::SkillText;

/// Built-in commands and what they do. They work only at the start of the input.
pub const BUILT_IN: &[(&str, &str)] = &[
    ("compact", "Summarize the conversation so far to free up the context window"),
    ("context", "Show what fills the context window"),
];

/// What a mention starts with.
pub const MENTION_PREFIX: &str = "/skill:";

/// How a text item carries an embedded resource (ACP): mentions inside it are not the user's.
pub fn resource_text(uri: &str, text: &str) -> String {
    format!("<resource uri=\"{uri}\">\n{text}\n</resource>")
}

fn is_resource(text: &str) -> bool {
    text.starts_with("<resource uri=\"")
}

/// What typing `/` offers: the built-in commands, then the skills that can be mentioned, in
/// priority order (`skills` as [`skills::describe`] gives them). A skill's command is
/// `skill:name`, what follows the `/`.
pub fn available(skills: &[SkillEntry]) -> Vec<CommandInfo> {
    let built_in = BUILT_IN.iter().map(|(name, description)| CommandInfo {
        name: name.to_string(),
        description: description.to_string(),
        hint: None,
        skill: None,
    });
    let mentionable = skills
        .iter()
        .filter(|skill| skill.state == SkillState::Active && is_mentionable(&skill.name))
        .map(|skill| CommandInfo {
            name: format!("skill:{}", skill.name),
            description: skill.description.clone(),
            hint: skill.argument_hint.clone(),
            skill: Some(skill.path.clone()),
        });
    built_in.chain(mentionable).collect()
}

/// Whether `/skill:name` can be written for this name: other characters would end the mention.
pub fn is_mentionable(name: &str) -> bool {
    !name.is_empty() && name.chars().all(is_name_char)
}

/// A `/skill:name` in a text: `start..end` covers all of it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Mention<'a> {
    pub start: usize,
    pub end: usize,
    pub name: &'a str,
}

/// Every `/skill:name` in `text` that stands on its own, outside code, whether or not a skill
/// has that name (docs/kernel/skill-design.md §6.3.2). The desktop app reads text the same way; both
/// are tested against `testdata/mentions.json`.
pub fn mentions(text: &str) -> Vec<Mention<'_>> {
    let code = code_ranges(text);
    let mut out = Vec::new();
    let mut from = 0;
    while let Some(found) = text[from..].find(MENTION_PREFIX) {
        let start = from + found;
        from = start + 1;
        if code.iter().any(|range| range.contains(&start)) || text[..start].chars().next_back().is_some_and(is_path_char) {
            continue;
        }
        let name_start = start + MENTION_PREFIX.len();
        let name_len = text[name_start..].find(|c: char| !is_name_char(c)).unwrap_or(text.len() - name_start);
        let end = name_start + name_len;
        if name_len == 0 || !ends_mention(&text[end..]) {
            continue;
        }
        out.push(Mention {
            start,
            end,
            name: &text[name_start..end],
        });
        from = end;
    }
    out
}

fn is_name_char(c: char) -> bool {
    c.is_ascii_alphanumeric() || c == '_' || c == '-'
}

/// ASCII characters that go on in a path or an address: next to one, `/skill:name` is part of
/// something else.
fn is_path_char(c: char) -> bool {
    c.is_ascii_alphanumeric() || "/\\.:~_-@#%&=?+$*".contains(c)
}

/// Whether what follows a name ends the mention: nothing, a space, a character outside ASCII,
/// an ASCII character that is not part of a path, or `.`, `:` or `?` ending a sentence.
fn ends_mention(rest: &str) -> bool {
    let mut chars = rest.chars();
    match chars.next() {
        None => true,
        Some('.' | ':' | '?') => chars.next().is_none_or(|next| next.is_whitespace() || !next.is_ascii()),
        Some(c) => c.is_whitespace() || !c.is_ascii() || !is_path_char(c),
    }
}

/// Byte ranges of `text` that are code: fenced blocks, from a line starting with ``` or ~~~ to
/// the next such line (or the end), and inline spans between equal runs of backticks.
fn code_ranges(text: &str) -> Vec<Range<usize>> {
    let mut ranges = Vec::new();
    let mut fence: Option<(usize, &str)> = None;
    let mut prose_start = 0;
    let mut offset = 0;
    for line in text.split_inclusive('\n') {
        let trimmed = line.trim_start_matches(' ');
        let marker = ["```", "~~~"]
            .into_iter()
            .find(|marker| line.len() - trimmed.len() <= 3 && trimmed.starts_with(marker));
        match (fence, marker) {
            (None, Some(marker)) => {
                inline_code(text, prose_start..offset, &mut ranges);
                fence = Some((offset, marker));
            }
            (Some((start, open)), Some(close)) if open == close => {
                ranges.push(start..offset + line.len());
                fence = None;
                prose_start = offset + line.len();
            }
            _ => {}
        }
        offset += line.len();
    }
    match fence {
        Some((start, _)) => ranges.push(start..text.len()),
        None => inline_code(text, prose_start..text.len(), &mut ranges),
    }
    ranges
}

/// Inline code in `text[range]`: from a run of backticks to the next run of the same length.
/// A run that is never closed is text.
fn inline_code(text: &str, range: Range<usize>, out: &mut Vec<Range<usize>>) {
    let bytes = text.as_bytes();
    let run_end = |mut i: usize| {
        while i < range.end && bytes[i] == b'`' {
            i += 1;
        }
        i
    };
    let mut i = range.start;
    while i < range.end {
        if bytes[i] != b'`' {
            i += 1;
            continue;
        }
        let open = i;
        i = run_end(i);
        let len = i - open;
        let mut j = i;
        while j < range.end {
            if bytes[j] != b'`' {
                j += 1;
                continue;
            }
            let close = j;
            j = run_end(j);
            if j - close == len {
                out.push(open..j);
                i = j;
                break;
            }
        }
    }
}

pub(crate) enum Command {
    Compact,
    Context,
    /// A message for the model: the instructions of the skills it mentions, in order, and
    /// what the user should hear about mentions that load nothing.
    Prompt { skills: Vec<SkillText>, warnings: Vec<String> },
}

/// Reads the user's input: `/compact` or `/context` as its first word is a built-in command; otherwise
/// each skill the text mentions is loaded once, in the order of its first mention. The input
/// itself is never changed. Text items carrying an embedded resource are not read.
pub(crate) fn resolve(input: &[UserContent], skills: &[SkillRef], limits: &OutputLimits) -> Command {
    if let Some(UserContent::Text { text }) = input.first() {
        let first = text.trim_start();
        let is = |command: &str| first.strip_prefix(command).is_some_and(|rest| rest.chars().next().is_none_or(char::is_whitespace));
        if is("/compact") {
            return Command::Compact;
        }
        if is("/context") {
            return Command::Context;
        }
    }
    let mut loaded = Vec::new();
    let mut warnings = Vec::new();
    let mut seen = HashSet::new();
    let texts = input.iter().filter_map(|content| match content {
        UserContent::Text { text } if !is_resource(text) => Some(text),
        _ => None,
    });
    for text in texts {
        for mention in mentions(text) {
            let name = mention.name;
            if !seen.insert(name) {
                continue;
            }
            let Some(skill) = skills.iter().find(|skill| skill.name == name) else {
                warnings.push(format!("no skill named '{name}'; \"{MENTION_PREFIX}{name}\" was sent as text"));
                continue;
            };
            match std::fs::read_to_string(&skill.path) {
                Ok(file) => loaded.push(SkillText {
                    name: skill.name.clone(),
                    path: skill.path.clone(),
                    content: skill_block(skill, skills::body(&file), limits),
                }),
                Err(err) => warnings.push(format!(
                    "cannot read skill '{name}' at {}: {err}; \"{MENTION_PREFIX}{name}\" was sent as text",
                    skill.path.display()
                )),
            }
        }
    }
    Command::Prompt {
        skills: loaded,
        warnings,
    }
}

/// The skill's instructions as the model receives them (docs/kernel/skill-design.md appendix A.2),
/// cut to the tool output limits; the rest stays in the file. Unlike a catalog entry, the
/// block names no SKILL.md to read: the instructions are here, and the file's path is given
/// only when something was cut.
pub(crate) fn skill_block(skill: &SkillRef, body: &str, limits: &OutputLimits) -> String {
    let dir = skill.path.parent().unwrap_or(&skill.path).display().to_string();
    let mut out = String::with_capacity(body.len() + 256);
    out.push_str("<skill name=\"");
    skills::escape(&skill.name, true, &mut out);
    out.push_str("\">\nRelative paths in this skill are relative to ");
    out.push_str(&dir);
    out.push_str(".\n\n");
    let (kept, cut) = head(body.trim(), limits.max_lines, her_tools::bytes_for_tokens(limits.max_tokens));
    out.push_str(kept);
    if cut {
        out.push_str(&format!("\n[Truncated. Read {} for the rest.]", skill.path.display()));
    }
    out.push_str("\n</skill>");
    out
}

/// The start of `text` within `max_lines` and `max_bytes`, cut at a line boundary when there
/// is one; whether anything was left out.
fn head(text: &str, max_lines: usize, max_bytes: usize) -> (&str, bool) {
    let mut end = 0;
    for (lines, line) in text.split_inclusive('\n').enumerate() {
        if lines == max_lines || end + line.len() > max_bytes {
            if end == 0 {
                // One line longer than the limit: keep its start.
                let mut cut = max_bytes.min(line.len());
                while !line.is_char_boundary(cut) {
                    cut -= 1;
                }
                return (&text[..cut], true);
            }
            return (text[..end].trim_end(), true);
        }
        end += line.len();
    }
    (text, false)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::skills::SkillScope;
    use serde_json::Value;
    use std::path::Path;
    use std::path::PathBuf;

    fn skill_at(dir: &Path, name: &str, text: &str) -> SkillRef {
        let skill_dir = dir.join(name);
        std::fs::create_dir_all(&skill_dir).unwrap();
        let path = skill_dir.join("SKILL.md");
        std::fs::write(&path, text).unwrap();
        SkillRef {
            name: name.into(),
            path,
            scope: SkillScope::User,
            model_invocable: false,
            argument_hint: None,
        }
    }

    fn text(input: &str) -> Vec<UserContent> {
        vec![UserContent::Text { text: input.into() }]
    }

    fn prompt(command: Command) -> (Vec<SkillText>, Vec<String>) {
        match command {
            Command::Prompt { skills, warnings } => (skills, warnings),
            Command::Compact | Command::Context => panic!("not a prompt"),
        }
    }

    #[test]
    fn mentions_follow_the_shared_cases() {
        let cases: Value = serde_json::from_str(include_str!("../testdata/mentions.json")).unwrap();
        for case in cases["cases"].as_array().unwrap() {
            let text = case["text"].as_str().unwrap();
            let found = mentions(text);
            let names: Vec<&str> = found.iter().map(|m| m.name).collect();
            let expected: Vec<&str> = case["mentions"].as_array().unwrap().iter().map(|n| n.as_str().unwrap()).collect();
            assert_eq!(names, expected, "{text:?}");
            for mention in &found {
                assert_eq!(&text[mention.start..mention.end], format!("{MENTION_PREFIX}{}", mention.name));
            }
        }
    }

    #[test]
    fn mentioned_skills_load_once_in_order_and_the_input_stays() {
        let dir = tempfile::tempdir().unwrap();
        let report = skill_at(dir.path(), "gen-report", "---\nname: gen-report\ndescription: d\n---\n# Report\n\nRun it.\n");
        let style = skill_at(dir.path(), "commit-style", "---\ndescription: d\n---\nTag it.\n");
        let skills = [style.clone(), report.clone()];
        let input = vec![
            UserContent::Text {
                text: "  请用 /skill:gen-report 整理，再按 /skill:commit-style 写；/skill:gen-report 再来一次".into(),
            },
            UserContent::Image {
                data: "aGk=".into(),
                mime_type: "image/png".into(),
            },
        ];
        let (loaded, warnings) = prompt(resolve(&input, &skills, &OutputLimits::default()));
        assert!(warnings.is_empty(), "{warnings:?}");
        let names: Vec<&str> = loaded.iter().map(|s| s.name.as_str()).collect();
        assert_eq!(names, ["gen-report", "commit-style"], "first mention first, each once");
        let dir_text = report.path.parent().unwrap().display().to_string();
        assert_eq!(
            loaded[0].content,
            format!("<skill name=\"gen-report\">\nRelative paths in this skill are relative to {dir_text}.\n\n# Report\n\nRun it.\n</skill>")
        );
        assert_eq!(loaded[0].path, report.path);

        // Later text items count; embedded resources do not.
        let input = vec![
            UserContent::Text { text: "look at ".into() },
            UserContent::Text {
                text: resource_text("file:///w/README.md", "Run /skill:commit-style to ship."),
            },
            UserContent::Text { text: " and use /skill:gen-report".into() },
        ];
        let (loaded, _) = prompt(resolve(&input, &skills, &OutputLimits::default()));
        assert_eq!(loaded.iter().map(|s| s.name.as_str()).collect::<Vec<_>>(), ["gen-report"]);
    }

    #[test]
    fn compact_only_at_the_start_and_unknown_or_unreadable_mentions_warn() {
        let dir = tempfile::tempdir().unwrap();
        let report = skill_at(dir.path(), "gen-report", "---\ndescription: d\n---\nbody\n");
        let gone = SkillRef {
            name: "gone".into(),
            path: PathBuf::from("/nonexistent/gone/SKILL.md"),
            scope: SkillScope::Project,
            model_invocable: true,
            argument_hint: None,
        };
        let skills = [report, gone];
        let limits = OutputLimits::default();
        assert!(matches!(resolve(&text("  /compact please"), &skills, &limits), Command::Compact));
        assert!(matches!(resolve(&text("/context"), &skills, &limits), Command::Context));
        assert!(matches!(resolve(&text("/contexts"), &skills, &limits), Command::Prompt { .. }));
        for typed in ["/compactly", "please /compact", "/gen-report 本周", "看看 /deploy 接口", "`/skill:gen-report`"] {
            let (loaded, warnings) = prompt(resolve(&text(typed), &skills, &limits));
            assert!(loaded.is_empty() && warnings.is_empty(), "{typed}");
        }
        let (loaded, warnings) = prompt(resolve(&text("/skill:gen-reprot and /skill:gone, /skill:gen-reprot"), &skills, &limits));
        assert!(loaded.is_empty());
        assert_eq!(warnings.len(), 2, "{warnings:?}");
        assert_eq!(warnings[0], "no skill named 'gen-reprot'; \"/skill:gen-reprot\" was sent as text");
        assert!(warnings[1].starts_with("cannot read skill 'gone' at /nonexistent/gone/SKILL.md"));
        let image_only = vec![UserContent::Image {
            data: "aGk=".into(),
            mime_type: "image/png".into(),
        }];
        assert!(prompt(resolve(&image_only, &skills, &limits)).0.is_empty());
    }

    #[test]
    fn the_menu_lists_built_ins_then_the_skills_as_their_files_say_now() {
        let dir = tempfile::tempdir().unwrap();
        let report = skill_at(
            dir.path(),
            "report",
            "---\nname: report\ndescription: Weekly\n  reports.\nargument-hint: \"[week]\"\n---\nbody\n",
        );
        let spaced = SkillRef {
            name: "Weekly Report".into(),
            ..skill_at(dir.path(), "spaced", "---\nname: Weekly Report\ndescription: d\n---\n")
        };
        let gone = SkillRef {
            name: "gone".into(),
            path: dir.path().join("gone/SKILL.md"),
            scope: SkillScope::Project,
            model_invocable: true,
            argument_hint: None,
        };
        let described = skills::describe(&[report.clone(), spaced, gone]);
        assert_eq!(described[0].description, "Weekly reports.");
        assert!(!described[0].model_invocable, "as the session started");
        assert_eq!(described[2].state, SkillState::Invalid);
        assert!(described[2].detail.as_deref().unwrap().starts_with("cannot read the file"));

        let menu = available(&described);
        let names: Vec<&str> = menu.iter().map(|c| c.name.as_str()).collect();
        assert_eq!(names, ["compact", "context", "skill:report"], "a name with a space cannot be written; a broken skill is left out");
        assert_eq!(menu[0].description, BUILT_IN[0].1);
        assert_eq!(menu[0].skill, None);
        assert_eq!(menu[2].hint.as_deref(), Some("[week]"));
        assert_eq!(menu[2].skill.as_ref(), Some(&report.path));
        assert!(is_mentionable("gen-report") && is_mentionable("a_b2") && !is_mentionable("a b") && !is_mentionable(""));
    }

    #[test]
    fn long_bodies_are_cut() {
        let dir = tempfile::tempdir().unwrap();
        let long = skill_at(dir.path(), "long", &format!("---\ndescription: d\n---\n{}", "line\n".repeat(50)));
        let limits = OutputLimits {
            max_lines: 10,
            ..OutputLimits::default()
        };
        let block = skill_block(&long, skills::body(&std::fs::read_to_string(&long.path).unwrap()), &limits);
        assert_eq!(block.matches("line").count(), 10, "ten lines kept");
        assert!(block.contains(&format!("[Truncated. Read {} for the rest.]", long.path.display())));
        assert_eq!(head("abcdef", 10, 4), ("abcd", true));
        assert_eq!(head("short\n", 10, 100), ("short\n", false));
    }
}
