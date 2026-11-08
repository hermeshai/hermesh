//! A reply, line by line (§5.7): headings bold, code in another color, the rest as written.

use crate::render::Style;

#[derive(Default)]
pub struct Formatter {
    in_code: bool,
}

impl Formatter {
    pub fn line(&mut self, style: &Style, line: &str) -> String {
        if line.trim_start().starts_with("```") {
            self.in_code = !self.in_code;
            return style.dim(line);
        }
        if self.in_code {
            return style.cyan(line);
        }
        let trimmed = line.trim_start_matches('#');
        if trimmed.len() < line.len() && line.len() - trimmed.len() <= 6 && trimmed.starts_with(' ') {
            return style.bold(line);
        }
        inline_code(style, line)
    }
}

fn inline_code(style: &Style, line: &str) -> String {
    let mut parts = line.split('`');
    let mut out = parts.next().unwrap_or_default().to_string();
    let rest: Vec<&str> = parts.collect();
    // An odd number of backticks: the last one opens nothing.
    let pairs = rest.len().is_multiple_of(2);
    for (index, part) in rest.iter().enumerate() {
        let code = index % 2 == 0 && (pairs || index + 1 < rest.len());
        if code {
            out.push_str(&style.cyan(part));
        } else {
            if !pairs && index + 1 == rest.len() {
                out.push('`');
            }
            out.push_str(part);
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn plain_text_stays_as_written_without_color() {
        let style = Style::plain();
        let mut f = Formatter::default();
        assert_eq!(f.line(&style, "# Title"), "# Title");
        assert_eq!(f.line(&style, "a `b` c"), "a b c");
        assert_eq!(f.line(&style, "one ` tick"), "one ` tick");
        assert_eq!(f.line(&style, "```rust"), "```rust");
        assert_eq!(f.line(&style, "let `x` = 1;"), "let `x` = 1;", "inside a block nothing is touched");
        assert_eq!(f.line(&style, "```"), "```");
        assert_eq!(f.line(&style, "#not a heading"), "#not a heading");
    }
}
