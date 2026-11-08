//! The `<available_skills>` section of the system prompt (docs/kernel/skill-design.md §5).

use super::SkillEntry;

/// The tool the model loads a skill with.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Reader {
    Read,
    Bash,
}

impl Reader {
    /// `read` when it is enabled, else `bash`; `None` when the model cannot read files.
    pub fn for_tools(names: &[String]) -> Option<Reader> {
        let has = |name: &str| names.iter().any(|n| n == name);
        if has("read") {
            Some(Reader::Read)
        } else if has("bash") {
            Some(Reader::Bash)
        } else {
            None
        }
    }

    fn intro(self) -> &'static str {
        match self {
            Reader::Read => {
                "The following skills provide specialized instructions for specific tasks. When a task matches a \
                 skill's description, read its SKILL.md with the read tool before acting, unless the conversation \
                 already has its instructions in a <skill> block. Resolve relative paths in a skill against the \
                 directory that contains its SKILL.md."
            }
            Reader::Bash => {
                "The following skills provide specialized instructions for specific tasks. When a task matches a \
                 skill's description, read its SKILL.md with bash before acting, unless the conversation already \
                 has its instructions in a <skill> block. Resolve relative paths in a skill against the directory \
                 that contains its SKILL.md."
            }
        }
    }
}

/// The catalog section within a system prompt, or empty when it has none.
pub(crate) fn section_in(prompt: &str) -> &str {
    let Some(start) = prompt.find(OPEN) else { return "" };
    match prompt[start..].find(CLOSE) {
        Some(end) => &prompt[start..start + end + CLOSE.len()],
        None => "",
    }
}

/// The budget when the configuration sets none: 2% of the context window, at most 10,000.
pub fn default_budget(context_window: u64) -> u64 {
    (context_window / 50).min(10_000)
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Catalog {
    /// The section, or empty when there is nothing to list.
    pub text: String,
    /// Estimate of `text`.
    pub tokens: u64,
    pub budget: u64,
    pub listed: usize,
    /// Descriptions were cut to this many characters to fit the budget.
    pub shortened_to: Option<usize>,
    /// Skills left out to fit the budget, in priority order.
    pub left_out: Vec<String>,
}

impl Catalog {
    /// Problems with the budget, for the user.
    pub fn warnings(&self) -> Vec<String> {
        let mut warnings = Vec::new();
        if let Some(chars) = self.shortened_to {
            warnings.push(format!(
                "skill descriptions were shortened to {chars} characters to fit the {}-token catalog budget; \
                 raise skills.max_catalog_tokens or disable unused skills",
                self.budget
            ));
        }
        if !self.left_out.is_empty() {
            warnings.push(format!(
                "{} skills were left out of the catalog to fit the {}-token budget: {}",
                self.left_out.len(),
                self.budget,
                self.left_out.join(", ")
            ));
        }
        warnings
    }
}

const OPEN: &str = "<available_skills>\n";
const CLOSE: &str = "</available_skills>";
const LINE_END: &str = "</skill>\n";
const ELLIPSIS: char = '…';

/// What [`crate::context::estimate_text`] counts: ASCII characters and other characters.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
struct Counts {
    ascii: u64,
    other: u64,
}

impl Counts {
    fn of(text: &str) -> Self {
        let ascii = text.chars().filter(char::is_ascii).count() as u64;
        Self {
            ascii,
            other: text.chars().count() as u64 - ascii,
        }
    }

    fn add(self, other: Counts) -> Self {
        Self {
            ascii: self.ascii + other.ascii,
            other: self.other + other.other,
        }
    }

    fn sub(self, other: Counts) -> Self {
        Self {
            ascii: self.ascii - other.ascii,
            other: self.other - other.other,
        }
    }

    /// Same as [`crate::context::estimate_text`] of the text counted.
    fn tokens(self) -> u64 {
        self.ascii.div_ceil(4) + self.other
    }
}

/// One skill's line, measured once for every length its description may be cut to.
struct Line<'a> {
    /// `<skill name="…" path="…">`, escaped.
    head: String,
    skill: &'a SkillEntry,
    /// `prefix[i]`: the head, the escaped first `i` characters of the description and the
    /// line's end.
    prefix: Vec<Counts>,
}

impl<'a> Line<'a> {
    fn new(skill: &'a SkillEntry) -> Self {
        let mut head = String::with_capacity(64 + skill.path.as_os_str().len());
        head.push_str("<skill name=\"");
        escape(&skill.name, true, &mut head);
        head.push_str("\" path=\"");
        escape(&skill.path.to_string_lossy(), true, &mut head);
        head.push_str("\">");
        let mut counts = Counts::of(&head).add(Counts::of(LINE_END));
        let mut prefix = Vec::with_capacity(skill.description.len() + 1);
        prefix.push(counts);
        let mut escaped = String::new();
        for c in skill.description.chars() {
            escaped.clear();
            escape_char(c, false, &mut escaped);
            counts = counts.add(Counts::of(&escaped));
            prefix.push(counts);
        }
        Self { head, skill, prefix }
    }

    fn chars(&self) -> usize {
        self.prefix.len() - 1
    }

    /// The line with the description cut to `cap` characters.
    fn counts(&self, cap: usize) -> Counts {
        if cap >= self.chars() {
            return self.prefix[self.chars()];
        }
        let ellipsis = Counts {
            ascii: 0,
            other: u64::from(cap > 0),
        };
        self.prefix[cap].add(ellipsis)
    }

    fn render(&self, cap: usize, out: &mut String) {
        out.push_str(&self.head);
        if cap >= self.chars() {
            escape(&self.skill.description, false, out);
        } else if cap > 0 {
            for c in self.skill.description.chars().take(cap) {
                escape_char(c, false, out);
            }
            out.push(ELLIPSIS);
        }
        out.push_str(LINE_END);
    }
}

/// The catalog of `skills` (active ones, in priority order) within `budget` tokens. Skills
/// that only the user may invoke are not listed.
pub fn catalog(skills: &[&SkillEntry], reader: Option<Reader>, budget: u64) -> Catalog {
    let listed: Vec<&SkillEntry> = skills.iter().copied().filter(|s| s.model_invocable).collect();
    let Some(reader) = reader.filter(|_| !listed.is_empty()) else {
        return Catalog {
            budget,
            ..Catalog::default()
        };
    };
    let frame = frame(reader);
    let mut lines: Vec<Line<'_>> = listed.into_iter().map(Line::new).collect();
    let total = |lines: &[Line<'_>], cap: usize| {
        lines.iter().fold(frame, |sum, line| sum.add(line.counts(cap))).tokens()
    };
    let longest = lines.iter().map(Line::chars).max().unwrap_or(0);
    let mut cap = longest;
    let mut left_out = Vec::new();
    if total(&lines, longest) > budget {
        // Every description gets shorter before any skill goes; skills go from the lowest priority.
        let mut bare = lines.iter().fold(frame, |sum, line| sum.add(line.counts(0)));
        while bare.tokens() > budget
            && let Some(line) = lines.pop()
        {
            bare = bare.sub(line.counts(0));
            left_out.push(line.skill.name.clone());
        }
        left_out.reverse();
        let (mut low, mut high) = (0, longest);
        while low < high {
            let mid = (low + high).div_ceil(2);
            if total(&lines, mid) <= budget {
                low = mid;
            } else {
                high = mid - 1;
            }
        }
        cap = low;
    }
    if lines.is_empty() {
        return Catalog {
            budget,
            left_out,
            ..Catalog::default()
        };
    }
    let size: usize = lines.iter().map(|l| l.head.len() + l.skill.description.len() + 16).sum();
    let mut text = String::with_capacity(512 + size);
    text.push_str(OPEN);
    text.push_str(reader.intro());
    text.push('\n');
    for line in &lines {
        line.render(cap, &mut text);
    }
    text.push_str(CLOSE);
    Catalog {
        tokens: total(&lines, cap),
        text,
        budget,
        listed: lines.len(),
        shortened_to: (cap < longest).then_some(cap),
        left_out,
    }
}

/// Everything but the lines: the tags and the instructions.
fn frame(reader: Reader) -> Counts {
    Counts::of(OPEN)
        .add(Counts::of(reader.intro()))
        .add(Counts::of("\n"))
        .add(Counts::of(CLOSE))
}

pub(crate) fn escape(text: &str, attribute: bool, out: &mut String) {
    for c in text.chars() {
        escape_char(c, attribute, out);
    }
}

fn escape_char(c: char, attribute: bool, out: &mut String) {
    match c {
        '&' => out.push_str("&amp;"),
        '<' => out.push_str("&lt;"),
        '>' => out.push_str("&gt;"),
        '"' if attribute => out.push_str("&quot;"),
        _ => out.push(c),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::context::estimate_text;
    use crate::skills::SkillScope;
    use crate::skills::SkillState;
    use std::path::PathBuf;

    fn skill(name: &str, description: &str) -> SkillEntry {
        SkillEntry {
            name: name.into(),
            description: description.into(),
            path: PathBuf::from(format!("/skills/{name}/SKILL.md")),
            scope: SkillScope::User,
            model_invocable: true,
            argument_hint: None,
            state: SkillState::Active,
            detail: None,
            notes: Vec::new(),
            source: None,
            removable: false,
            default_enabled: true,
        }
    }

    #[test]
    fn renders_one_line_per_skill_with_escaping() {
        let a = skill("pdf-tools", "Extract <tables> & text. Use when \"PDF\" is mentioned.");
        let mut b = skill("deploy", "Deploy.");
        b.model_invocable = false;
        let c = skill("a\"b", "x");
        let catalog = catalog(&[&a, &b, &c], Some(Reader::Read), 10_000);
        assert_eq!(
            catalog.text,
            format!(
                "<available_skills>\n{}\n\
                 <skill name=\"pdf-tools\" path=\"/skills/pdf-tools/SKILL.md\">Extract &lt;tables&gt; &amp; text. \
                 Use when \"PDF\" is mentioned.</skill>\n\
                 <skill name=\"a&quot;b\" path=\"/skills/a&quot;b/SKILL.md\">x</skill>\n\
                 </available_skills>",
                Reader::Read.intro()
            )
        );
        assert_eq!(catalog.listed, 2, "skills only the user may invoke are not listed");
        assert_eq!(catalog.tokens, estimate_text(&catalog.text));
        assert!(catalog.warnings().is_empty());
    }

    #[test]
    fn nothing_to_list_or_no_way_to_read() {
        let mut only_user = skill("deploy", "Deploy.");
        only_user.model_invocable = false;
        assert!(catalog(&[&only_user], Some(Reader::Read), 10_000).text.is_empty());
        assert!(catalog(&[], Some(Reader::Read), 10_000).text.is_empty());
        assert!(catalog(&[&skill("a", "b")], None, 10_000).text.is_empty());
        let bash = catalog(&[&skill("a", "b")], Some(Reader::Bash), 10_000);
        assert!(bash.text.contains("read its SKILL.md with bash before acting"));
        assert_eq!(Reader::for_tools(&["bash".into(), "read".into()]), Some(Reader::Read));
        assert_eq!(Reader::for_tools(&["bash".into()]), Some(Reader::Bash));
        assert_eq!(Reader::for_tools(&["write".into()]), None);
    }

    #[test]
    fn over_budget_descriptions_shrink_together_then_skills_go() {
        let long = "wörd & ".repeat(150);
        let skills: Vec<SkillEntry> = (0..6).map(|i| skill(&format!("s{i}"), &long)).collect();
        let refs: Vec<&SkillEntry> = skills.iter().collect();
        let full = catalog(&refs, Some(Reader::Read), 100_000);
        assert_eq!(full.shortened_to, None);
        assert_eq!(full.tokens, estimate_text(&full.text));

        let budget = full.tokens / 3;
        let shrunk = catalog(&refs, Some(Reader::Read), budget);
        assert!(shrunk.tokens <= budget, "{} > {budget}", shrunk.tokens);
        assert_eq!(shrunk.tokens, estimate_text(&shrunk.text), "the estimate is that of the text");
        let cap = shrunk.shortened_to.expect("descriptions shortened");
        assert!(cap > 0 && cap < long.chars().count());
        assert_eq!(shrunk.listed, 6);
        assert_eq!(shrunk.text.matches('…').count(), 6, "every description is cut, none dropped");
        // One more character would not fit.
        let lines: Vec<Line<'_>> = refs.iter().copied().map(Line::new).collect();
        let frame = frame(Reader::Read);
        let at = |cap: usize| lines.iter().fold(frame, |sum, l| sum.add(l.counts(cap))).tokens();
        assert!(at(cap + 1) > budget);
        assert_eq!(shrunk.warnings().len(), 1);

        let names_only = lines[..3].iter().fold(frame, |sum, l| sum.add(l.counts(0))).tokens();
        let dropped = catalog(&refs, Some(Reader::Read), names_only);
        assert_eq!(dropped.listed, 3);
        assert_eq!(dropped.left_out, vec!["s3", "s4", "s5"], "lowest priority first out, listed in order");
        assert!(dropped.text.contains("<skill name=\"s2\" path=\"/skills/s2/SKILL.md\"></skill>"));
        assert_eq!(dropped.tokens, estimate_text(&dropped.text));
        assert_eq!(dropped.warnings().len(), 2);

        let nothing = catalog(&refs, Some(Reader::Read), 10);
        assert!(nothing.text.is_empty());
        assert_eq!(nothing.left_out.len(), 6);
    }

    #[test]
    fn default_budget_is_two_percent_capped() {
        assert_eq!(default_budget(131_072), 2_621);
        assert_eq!(default_budget(1_048_576), 10_000);
    }
}
