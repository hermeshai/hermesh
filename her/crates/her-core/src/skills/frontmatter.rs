//! The header of a `SKILL.md`: YAML between two `---` lines. Parsing is lenient so that
//! skills written for other clients still load (docs/kernel/skill-design.md §3.3).

use yaml_rust2::Yaml;
use yaml_rust2::YamlLoader;

/// What a header declares. Fields other than these are ignored.
#[derive(Debug, Default, PartialEq, Eq)]
pub(crate) struct Header {
    pub name: Option<String>,
    pub description: Option<String>,
    pub disable_model_invocation: bool,
    pub allowed_tools: bool,
    /// What to type after `/name`, e.g. `[file]`.
    pub argument_hint: Option<String>,
}

/// Parses the header at the start of `text`.
pub(crate) fn parse(text: &str) -> Result<Header, String> {
    let block = header_block(text)?;
    let doc = match load(block) {
        Ok(doc) => doc,
        // Retried once with such values quoted; the first error is the one to report.
        Err(err) => match quote_colon_values(block) {
            Some(repaired) => load(&repaired).map_err(|_| err)?,
            None => return Err(err),
        },
    };
    let map = match doc {
        Yaml::Hash(map) => map,
        Yaml::Null => Default::default(),
        _ => return Err("the header is not a mapping".into()),
    };
    let get = |key: &str| map.get(&Yaml::String(key.to_string()));
    let string = |key: &str| match get(key) {
        Some(Yaml::String(value)) => Some(value.clone()),
        _ => None,
    };
    Ok(Header {
        name: string("name"),
        description: string("description"),
        disable_model_invocation: get("disable-model-invocation").is_some_and(truthy),
        allowed_tools: get("allowed-tools").is_some(),
        argument_hint: string("argument-hint"),
    })
}

/// The YAML between the opening and the closing `---` line.
fn header_block(text: &str) -> Result<&str, String> {
    split(text).map(|(block, _)| block)
}

/// The text after the header: the instructions. All of it when there is no header.
pub(crate) fn body(text: &str) -> &str {
    match split(text) {
        Ok((_, body)) => body,
        Err(_) => text.strip_prefix('\u{feff}').unwrap_or(text),
    }
}

/// The header's YAML and the rest of the file.
fn split(text: &str) -> Result<(&str, &str), String> {
    let text = text.strip_prefix('\u{feff}').unwrap_or(text);
    let mut lines = text.split_inclusive('\n');
    let first = lines.next().unwrap_or_default();
    if first.trim_end() != "---" {
        return Err("no header: the file must start with a --- line".into());
    }
    let start = first.len();
    let mut end = start;
    for line in lines {
        if line.trim_end() == "---" {
            return Ok((&text[start..end], &text[end + line.len()..]));
        }
        end += line.len();
    }
    Err("the header has no closing --- line".into())
}

fn load(block: &str) -> Result<Yaml, String> {
    let docs = YamlLoader::load_from_str(block).map_err(|e| format!("invalid YAML in the header: {e}"))?;
    Ok(docs.into_iter().next().unwrap_or(Yaml::Null))
}

/// Booleans as other clients write them: `true`, `yes`, `on`, `1`.
fn truthy(value: &Yaml) -> bool {
    match value {
        Yaml::Boolean(b) => *b,
        Yaml::Integer(n) => *n == 1,
        Yaml::String(s) => matches!(s.to_ascii_lowercase().as_str(), "true" | "yes" | "on" | "1"),
        _ => false,
    }
}

/// Quotes top-level values such as `description: Use when: the user asks`, which YAML
/// rejects but many skills contain. `None` when there was nothing to quote.
fn quote_colon_values(block: &str) -> Option<String> {
    let mut out = String::with_capacity(block.len() + 16);
    let mut changed = false;
    for line in block.split_inclusive('\n') {
        let body = line.trim_end_matches(['\r', '\n']);
        let ending = &line[body.len()..];
        // Indented lines belong to a value above, block scalars included.
        let top_level = !body.is_empty() && !body.starts_with([' ', '\t', '-', '#']);
        let Some((key, value)) = body.split_once(": ").filter(|_| top_level) else {
            out.push_str(line);
            continue;
        };
        let value = value.trim();
        let needs_quotes = !value.starts_with(['|', '>', '\'', '"', '[', '{', '&', '*', '!'])
            && (value.contains(": ") || value.ends_with(':'));
        if needs_quotes {
            out.push_str(key);
            out.push_str(": '");
            out.push_str(&value.replace('\'', "''"));
            out.push('\'');
            out.push_str(ending);
            changed = true;
        } else {
            out.push_str(line);
        }
    }
    changed.then_some(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_the_fields_it_uses() {
        let header = parse(
            "---\nname: pdf-tools\ndescription: Extract text from PDFs.\ndisable-model-invocation: true\n\
             allowed-tools: Bash(git:*)\nlicense: MIT\nmetadata:\n  author: me\n---\n# Body\n",
        )
        .unwrap();
        assert_eq!(header.name.as_deref(), Some("pdf-tools"));
        assert_eq!(header.description.as_deref(), Some("Extract text from PDFs."));
        assert!(header.disable_model_invocation);
        assert!(header.allowed_tools);
    }

    #[test]
    fn block_scalars_quotes_bom_and_crlf() {
        let folded = parse("---\ndescription: >-\n  Folded over\n  two lines.\n---\n").unwrap();
        assert_eq!(folded.description.as_deref(), Some("Folded over two lines."));
        let literal = parse("---\ndescription: |\n  line one\n  line two\n---\n").unwrap();
        assert_eq!(literal.description.as_deref(), Some("line one\nline two\n"));
        let quoted = parse("\u{feff}---\r\nname: \"x\"\r\ndescription: 'It''s quoted'\r\n---\r\nbody").unwrap();
        assert_eq!(quoted.name.as_deref(), Some("x"));
        assert_eq!(quoted.description.as_deref(), Some("It's quoted"));
        assert!(!quoted.disable_model_invocation);
    }

    #[test]
    fn colons_in_plain_values_are_repaired() {
        let header = parse("---\nname: aws\ndescription: Use when: the user deploys to AWS: ECS\n---\n").unwrap();
        assert_eq!(header.description.as_deref(), Some("Use when: the user deploys to AWS: ECS"));
        let header = parse("---\ndescription: Ends with a colon:\nname: it's\n---\n").unwrap();
        assert_eq!(header.description.as_deref(), Some("Ends with a colon:"));
        assert_eq!(header.name.as_deref(), Some("it's"));
        // Block scalars and quoted values are left alone.
        let header = parse("---\ndescription: >\n  Inside: a block\nname: \"a: b\"\n---\n").unwrap();
        assert_eq!(header.description.as_deref(), Some("Inside: a block\n"));
        assert_eq!(header.name.as_deref(), Some("a: b"));
    }

    #[test]
    fn booleans_in_other_spellings() {
        for value in ["yes", "on", "1", "True"] {
            let header = parse(&format!("---\ndescription: d\ndisable-model-invocation: {value}\n---\n")).unwrap();
            assert!(header.disable_model_invocation, "{value}");
        }
        let header = parse("---\ndescription: d\ndisable-model-invocation: no\n---\n").unwrap();
        assert!(!header.disable_model_invocation);
    }

    #[test]
    fn argument_hint_and_body() {
        let text = "---\ndescription: d\nargument-hint: '[app directory path or GitHub URL]'\n---\n# Steps\nRun it.\n";
        let header = parse(text).unwrap();
        assert_eq!(header.argument_hint.as_deref(), Some("[app directory path or GitHub URL]"));
        assert_eq!(body(text), "# Steps\nRun it.\n");
        assert_eq!(body("\u{feff}---\r\nname: x\r\n---\r\nBody\r\n"), "Body\r\n");
        assert_eq!(body("No header at all.\n"), "No header at all.\n");
        assert_eq!(body("---\nunterminated\n"), "---\nunterminated\n");
    }

    #[test]
    fn broken_headers_are_errors() {
        assert!(parse("# No header\n").unwrap_err().contains("must start with"));
        assert!(parse("---\nname: x\n").unwrap_err().contains("no closing"));
        assert!(parse("---\nname: [unclosed\n---\n").unwrap_err().contains("invalid YAML"));
        assert!(parse("---\n- a\n- b\n---\n").unwrap_err().contains("not a mapping"));
        // An empty header parses; the caller reports the missing description.
        assert_eq!(parse("---\n---\n").unwrap(), Header::default());
        // Values that are not strings count as missing.
        let header = parse("---\nname: 42\ndescription: [a, b]\n---\n").unwrap();
        assert_eq!((header.name, header.description), (None, None));
    }
}
