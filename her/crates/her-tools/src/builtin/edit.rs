use async_trait::async_trait;
use schemars::JsonSchema;
use serde::Deserialize;
use serde_json::Map;
use serde_json::Value;
use serde_json::json;
use std::io;
use std::path::PathBuf;

use crate::builtin::read::find_path_variant;
use crate::env::NotRegularFile;
use crate::env::TextEncoding;
use crate::text;
use crate::tool::ToolCtx;
use crate::tool::ToolError;
use crate::tool::ToolKind;
use crate::tool::ToolOutput;
use crate::truncate::cap_text;
use crate::typed::TypedTool;

mod diff;
mod matching;

use matching::Diagnosis;
use matching::KEEP;
use matching::Level;
use matching::MAX_COUNT;
use matching::Matcher;
use matching::Outcome;
use matching::Text;

const MAX_EDIT_BYTES: u64 = 16 * 1024 * 1024;
// Offsets into the file are kept as `u32`.
const _: () = assert!(MAX_EDIT_BYTES < u32::MAX as u64);
const DIFF_FOR_MODEL_BYTES: usize = 4 * 1024;
/// File lines shown when an old_text differs from them only in indentation.
const EXCERPT_LINES: usize = 40;
const EXCERPT_BYTES: usize = 4 * 1024;

/// Other names models use for the fields of an edit; the first is the real one.
const OLD_TEXT: [&str; 3] = ["old_text", "oldText", "old_string"];
const NEW_TEXT: [&str; 3] = ["new_text", "newText", "new_string"];

pub struct EditTool;

#[derive(Debug, Deserialize, JsonSchema)]
pub struct EditArgs {
    /// Path to the file, relative to the working directory or absolute.
    pub path: String,
    /// One or more replacements. Every old_text is matched against the original file, not
    /// against the result of earlier edits.
    pub edits: Vec<EditItem>,
}

#[derive(Debug, Deserialize, JsonSchema)]
pub struct EditItem {
    /// Exact text to replace, including indentation. It must appear exactly once in the file.
    pub old_text: String,
    /// The replacement text.
    pub new_text: String,
}

#[async_trait]
impl TypedTool for EditTool {
    type Args = EditArgs;
    const NAME: &'static str = "edit";
    const DESCRIPTION: &'static str = "Replace exact text in an existing file. Each old_text must appear exactly \
        once in the original file; several non-overlapping edits can be made in one call. Line endings are \
        preserved. Either all edits are applied or none.";

    fn kind(&self) -> ToolKind {
        ToolKind::Mutating
    }

    fn guidelines(&self) -> &[&'static str] {
        &[
            "edit: each old_text must match the file exactly, including whitespace, and be unique; include just enough surrounding text to make it so.",
            "Put several changes to one file in a single edit call.",
        ]
    }

    fn describe(&self, args: &serde_json::Value, env: &dyn crate::env::Environment) -> crate::tool::ToolDescription {
        crate::tool::ToolDescription::for_path(crate::tool::ToolCategory::Edit, "edit", args, env, None)
    }

    fn prepare_arguments(&self, args: Value) -> Value {
        repair(args)
    }

    async fn check(&self, args: &EditArgs, ctx: &ToolCtx) -> Result<(), ToolError> {
        plan(args, ctx).await.map(drop)
    }

    async fn run(&self, args: EditArgs, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        let path = ctx.env.resolve(&args.path);
        // One change to a file at a time, also across sessions that share it (sub-agents).
        let _lock = ctx.locks.lock(&path).await;
        let plan = plan(&args, &ctx).await?;
        apply(plan, &ctx).await
    }
}

/// An edit checked against the file, ready to be written.
struct Plan {
    path: PathBuf,
    display: String,
    text: Text,
    /// What the file is written back in.
    encoding: TextEncoding,
    /// Sorted by position; none overlap.
    replacements: Vec<Replacement>,
    /// Edits (1-based) that matched only loosely, and how.
    loose: Vec<(usize, Level)>,
}

/// One replacement, in byte offsets of the original file.
struct Replacement {
    start: usize,
    end: usize,
    /// new_text, with the line breaks of the text it replaces.
    text: String,
    /// The 1-based edit it comes from.
    edit: usize,
}

/// Reads the file and finds every edit in it. Changes nothing.
async fn plan(args: &EditArgs, ctx: &ToolCtx) -> Result<Plan, ToolError> {
    if args.edits.is_empty() {
        return Err(ToolError::InvalidArgs("edits must contain at least one item".into()));
    }
    if let Some(i) = args.edits.iter().position(|e| e.old_text.is_empty()) {
        return Err(ToolError::InvalidArgs(format!(
            "old_text #{} is empty; use write to create or replace a whole file",
            i + 1
        )));
    }
    let path = ctx.env.resolve(&args.path);
    let display = path.display().to_string();
    match ctx.env.metadata(&path).await {
        Ok(meta) if meta.is_dir => return Err(ToolError::Failed(format!("{display} is a directory."))),
        Ok(meta) if !meta.is_file => {
            return Err(ToolError::Failed(format!("{display} is not a regular file; it cannot be edited.")));
        }
        Ok(meta) if meta.readonly => return Err(ToolError::Failed(format!("{display} is read-only."))),
        Ok(_) => {}
        Err(err) if err.kind() == io::ErrorKind::NotFound => {
            // Edit does not switch to another path itself: the call was approved for this one.
            let hint = match find_path_variant(ctx.env.as_ref(), &args.path).await {
                Some(other) => format!(" Did you mean {}?", other.display()),
                None => " Use write to create a new file.".to_string(),
            };
            return Err(ToolError::Failed(format!("File not found: {display}.{hint}")));
        }
        Err(err) => return Err(ToolError::Failed(format!("Cannot access {display}: {err}"))),
    }
    ctx.env
        .check_write(&path)
        .await
        .map_err(|err| file_error(&display, "write", &err))?;
    let file = ctx
        .env
        .read_file(&path, MAX_EDIT_BYTES)
        .await
        .map_err(|err| file_error(&display, "read", &err))?;
    if file.truncated {
        return Err(ToolError::Failed(format!("{display} is too large to edit.")));
    }
    let (raw, encoding) = decode(file.bytes, &display)?;
    let text = Text::new(raw);
    let (replacements, loose) = locate(&text, &args.edits, &display)?;
    Ok(Plan {
        path,
        display,
        text,
        encoding,
        replacements,
        loose,
    })
}

/// The file as text, and the encoding it is written back in: UTF-8, or UTF-16 after a
/// byte-order mark, which read shows as text too. UTF-16 that would not come back byte for byte
/// is refused.
fn decode(bytes: Vec<u8>, display: &str) -> Result<(String, TextEncoding), ToolError> {
    match text::utf16_bom(&bytes) {
        Some(encoding) => text::decode_utf16_exact(&bytes[2..], encoding)
            .map(|decoded| (decoded, encoding))
            .ok_or_else(|| ToolError::Failed(format!("{display} is not valid UTF-16 text."))),
        None => String::from_utf8(bytes)
            .map(|decoded| (decoded, TextEncoding::Utf8))
            .map_err(|_| ToolError::Failed(format!("{display} is not valid UTF-8 text."))),
    }
}

type Located = (Vec<Replacement>, Vec<(usize, Level)>);

/// A lone CR right before a replacement's text and an LF that starts it (or follows it, when it
/// is empty), or a CR that ends it and an LF right after it, would read as one CRLF and lose a
/// line. That CR becomes a CRLF. "Right before" and "right after" are in the result: a touching
/// replacement's text, else the file's. `replacements` are sorted and do not overlap.
fn keep_breaks_apart(raw: &str, replacements: &mut [Replacement]) {
    let bytes = raw.as_bytes();
    let byte_before = |at: usize| at.checked_sub(1).map(|i| bytes[i]);
    for i in 0..replacements.len() {
        let (start, end) = (replacements[i].start, replacements[i].end);
        if replacements[i].text.as_bytes() == &bytes[start..end] {
            continue;
        }
        let before = match i.checked_sub(1).map(|j| &replacements[j]) {
            Some(prev) if prev.end == start => prev.text.as_bytes().last().copied().or(byte_before(prev.start)),
            _ => byte_before(start),
        };
        let after = match replacements.get(i + 1) {
            Some(next) if next.start == end => next.text.as_bytes().first().copied().or(bytes.get(next.end).copied()),
            _ => bytes.get(end).copied(),
        };
        let text = &mut replacements[i].text;
        if before == Some(b'\r') && text.as_bytes().first().copied().or(after) == Some(b'\n') {
            text.insert(0, '\n');
        }
        if text.ends_with('\r') && after == Some(b'\n') {
            text.push('\n');
        }
    }
}

/// Finds every old_text and turns the edits into replacements.
fn locate(text: &Text, edits: &[EditItem], display: &str) -> Result<Located, ToolError> {
    let multiline = edits.iter().filter(|e| matching::has_complete_line(&e.old_text)).count();
    let mut matcher = Matcher::new(text, multiline);
    let mut replacements = Vec::with_capacity(edits.len());
    let mut loose = Vec::new();
    let mut usual_break = None;
    for (i, edit) in edits.iter().enumerate() {
        let n = i + 1;
        let needle = matching::lf(&edit.old_text);
        let (level, start, end) = match matcher.find(&needle) {
            Outcome::Unique { level, start, end } => (level, start, end),
            Outcome::Several { level, count, lines } => {
                return Err(ToolError::Failed(several(n, display, level, count, &lines)));
            }
            Outcome::Missing => {
                return Err(ToolError::Failed(not_found(n, display, text, matcher.diagnose(&needle))));
            }
        };
        let new_text = if edit.new_text == edit.old_text {
            // Nothing to change, even where the file differs from old_text in whitespace or
            // typography.
            text.raw()[start..end].to_string()
        } else {
            // Line breaks take the style of the first one in the replaced text, which is the one
            // that ends the line where it starts.
            let brk = match text.line_break(text.line_of(start)) {
                Some(brk) => brk,
                None => *usual_break.get_or_insert_with(|| text.usual_break()),
            };
            matching::with_breaks(&edit.new_text, brk)
        };
        replacements.push(Replacement {
            start,
            end,
            text: new_text,
            edit: n,
        });
        if level != Level::Exact {
            loose.push((n, level));
        }
    }
    replacements.sort_by_key(|r| r.start);
    if let Some(pair) = replacements.windows(2).find(|pair| pair[1].start < pair[0].end) {
        return Err(ToolError::Failed(format!(
            "old_text #{} and #{} overlap; merge them into one edit.",
            pair[0].edit, pair[1].edit
        )));
    }
    keep_breaks_apart(text.raw(), &mut replacements);
    Ok((replacements, loose))
}

/// Writes a planned edit. Only the replaced bytes change.
async fn apply(plan: Plan, ctx: &ToolCtx) -> Result<ToolOutput, ToolError> {
    let Plan {
        path,
        display,
        text,
        encoding,
        replacements,
        loose,
    } = plan;
    let raw = text.raw();
    let Some(first) = replacements.iter().find(|r| r.text != raw[r.start..r.end]) else {
        return Ok(ToolOutput::text(format!("No changes: the new text equals the old text in {display}.")));
    };
    let same = raw[first.start..first.end]
        .bytes()
        .zip(first.text.bytes())
        .take_while(|(a, b)| a == b)
        .count();
    let first_changed_line = text.line_of(first.start + same) + 1;

    let added: usize = replacements.iter().map(|r| r.text.len()).sum();
    let mut output = String::with_capacity(raw.len() + added);
    let mut at = 0;
    for r in &replacements {
        output.push_str(&raw[at..r.start]);
        output.push_str(&r.text);
        at = r.end;
    }
    output.push_str(&raw[at..]);
    let outcome = ctx
        .env
        .write_file(&path, text::encode(output, encoding))
        .await
        .map_err(|err| file_error(&display, "write", &err))?;
    ctx.files.record(&path, outcome.stamp);

    let diff = diff::unified(&display, &text, &replacements);
    let mut content = format!(
        "Edited {display}: {} replacement(s), first change at line {first_changed_line}.",
        replacements.len()
    );
    // A loose match may not be what the model meant: show it what changed.
    if !loose.is_empty() {
        content.push('\n');
        content.push_str(&loose_note(&loose));
        if !diff.is_empty() {
            content.push_str("\n\n");
            content.push_str(&diff_for_model(&diff));
        }
    }
    let loose_matches: Vec<Value> = loose
        .iter()
        .map(|&(edit, level)| json!({"edit": edit, "kind": level_name(level)}))
        .collect();
    Ok(ToolOutput::text(content).with_details(json!({
        "path": display,
        "replacements": replacements.len(),
        "first_changed_line": first_changed_line,
        "diff": diff,
        "loose_matches": loose_matches,
    })))
}

/// A failed read or write of the file, in words the model can act on.
fn file_error(display: &str, doing: &str, err: &io::Error) -> ToolError {
    if let Some(special) = NotRegularFile::of(err) {
        return ToolError::Failed(format!(
            "{display} is not a regular file ({}); it cannot be edited.",
            special.kind
        ));
    }
    ToolError::Failed(match err.kind() {
        io::ErrorKind::ReadOnlyFilesystem => format!("{display} is read-only."),
        _ => format!("Cannot {doing} {display}: {err}"),
    })
}

fn level_name(level: Level) -> &'static str {
    match level {
        Level::Exact => "exact",
        Level::TrailingWhitespace => "trailing_whitespace",
        Level::Typography => "typography",
    }
}

/// "Edit #2 matched after ignoring trailing whitespace."
fn loose_note(loose: &[(usize, Level)]) -> String {
    let reasons = [
        (Level::TrailingWhitespace, "ignoring trailing whitespace"),
        (Level::Typography, "normalizing typographic quotes, dashes and spaces"),
    ];
    let mut sentences = Vec::new();
    for (level, reason) in reasons {
        let edits: Vec<String> = loose
            .iter()
            .filter(|(_, l)| *l == level)
            .map(|(n, _)| format!("#{n}"))
            .collect();
        if !edits.is_empty() {
            let noun = if edits.len() == 1 { "Edit" } else { "Edits" };
            sentences.push(format!("{noun} {} matched after {reason}.", edits.join(", ")));
        }
    }
    sentences.join(" ")
}

/// The diff cut to [`DIFF_FOR_MODEL_BYTES`], at a line end when there is one.
fn diff_for_model(diff: &str) -> String {
    let (mut shown, cut) = cap_text(diff, DIFF_FOR_MODEL_BYTES);
    if cut {
        match shown.rfind('\n') {
            Some(i) => shown.truncate(i + 1),
            None => shown.push('\n'),
        }
        shown.push_str("[diff truncated]");
    }
    shown
}

fn several(n: usize, display: &str, level: Level, count: usize, lines: &[usize]) -> String {
    let times = if count > MAX_COUNT { format!("more than {MAX_COUNT}") } else { count.to_string() };
    let ranges: Vec<(usize, usize)> = lines.iter().map(|&l| (l, l)).collect();
    let when = match level {
        Level::Exact => "",
        Level::TrailingWhitespace => " when trailing whitespace is ignored",
        Level::Typography => " when typographic quotes, dashes and spaces are normalized",
    };
    format!(
        "old_text #{n} matches {times} times in {display} ({}){when}; include more surrounding lines to make it \
         unique.",
        line_list(&ranges, count > KEEP)
    )
}

fn not_found(n: usize, display: &str, text: &Text, diagnosis: Diagnosis) -> String {
    let missing = format!("old_text #{n} was not found in {display}");
    match diagnosis {
        Diagnosis::Indentation { ranges, more: false } if ranges.len() == 1 => {
            let (first, last) = ranges[0];
            let (lines, verb, them) = if first == last {
                (format!("line {}", first + 1), "matches", "it")
            } else {
                (format!("lines {}-{}", first + 1, last + 1), "match", "them")
            };
            format!(
                "{missing}, but {lines} {verb} it except for indentation. Copy {them} exactly, including \
                 indentation:\n{}",
                excerpt(text, first, last)
            )
        }
        Diagnosis::Indentation { ranges, more } => {
            let ranges: Vec<(usize, usize)> = ranges.iter().map(|&(a, b)| (a + 1, b + 1)).collect();
            format!(
                "{missing}, but it matches {} except for indentation. Read the lines you mean and copy them exactly, \
                 including indentation.",
                line_list(&ranges, more)
            )
        }
        Diagnosis::FirstLine(lines) => {
            let ranges: Vec<(usize, usize)> = lines.iter().map(|&l| (l, l)).collect();
            format!(
                "{missing}. Its first line occurs at {}, but the lines after it differ. Read the file again and copy \
                 the exact text, including whitespace.",
                line_list(&ranges, false)
            )
        }
        Diagnosis::Unknown => {
            format!("{missing}. Read the file again and copy the exact text, including whitespace.")
        }
    }
}

/// "line 7", "lines 7-9", "lines 3, 8-9, 12", ending with ", …" when there are more.
fn line_list(ranges: &[(usize, usize)], more: bool) -> String {
    let items: Vec<String> = ranges
        .iter()
        .map(|&(a, b)| if a == b { a.to_string() } else { format!("{a}-{b}") })
        .collect();
    let noun = if !more && matches!(ranges, [(a, b)] if a == b) { "line" } else { "lines" };
    format!("{noun} {}{}", items.join(", "), if more { ", …" } else { "" })
}

/// Lines `first..=last` (0-based) as the file has them, up to about [`EXCERPT_LINES`] lines
/// or [`EXCERPT_BYTES`] bytes.
fn excerpt(text: &Text, first: usize, last: usize) -> String {
    let mut out = String::new();
    let mut shown = 0;
    for line in first..=last {
        let content = text.line_lf(line);
        if shown == EXCERPT_LINES || (shown > 0 && out.len() + content.len() > EXCERPT_BYTES) {
            break;
        }
        let (part, cut) = cap_text(&content, EXCERPT_BYTES);
        out.push_str(&part);
        shown += 1;
        if cut {
            out.push_str("… [line truncated]\n");
            break;
        }
    }
    let hidden = last + 1 - first - shown;
    if hidden > 0 {
        out.push_str(&format!("[{hidden} more lines not shown]"));
    } else if out.ends_with('\n') {
        out.pop();
    }
    out
}

/// Repairs well-known mistakes in edit arguments. Shapes it does not recognize are left alone.
fn repair(mut args: Value) -> Value {
    let Value::Object(map) = &mut args else {
        return args;
    };
    if !map.contains_key("path")
        && let Some(path) = map.remove("file_path")
    {
        map.insert("path".into(), path);
    }
    // The list sent as a JSON string, or a single edit sent on its own.
    if let Some(Value::String(edits)) = map.get("edits")
        && let Ok(parsed @ (Value::Array(_) | Value::Object(_))) = serde_json::from_str::<Value>(edits)
    {
        map.insert("edits".into(), parsed);
    }
    if let Some(edits) = map.get_mut("edits")
        && edits.is_object()
    {
        let single = edits.take();
        *edits = Value::Array(vec![single]);
    }
    // An edit given at the top level is one more edit.
    if OLD_TEXT.iter().chain(&NEW_TEXT).any(|key| map.contains_key(*key))
        && matches!(map.get("edits"), None | Some(Value::Null | Value::Array(_)))
    {
        let mut item = Map::new();
        if let Some(old) = take_first(map, &OLD_TEXT) {
            item.insert(OLD_TEXT[0].into(), old);
        }
        if let Some(new) = take_first(map, &NEW_TEXT) {
            item.insert(NEW_TEXT[0].into(), new);
        }
        let edits = map.entry("edits").or_insert(Value::Null);
        if !edits.is_array() {
            *edits = Value::Array(Vec::new());
        }
        if let Value::Array(list) = edits {
            list.push(Value::Object(item));
        }
    }
    if let Some(Value::Array(items)) = map.get_mut("edits") {
        for item in items {
            if let Value::Object(fields) = item {
                rename(fields, &OLD_TEXT);
                rename(fields, &NEW_TEXT);
            }
        }
    }
    args
}

/// Removes all of `keys` and returns the value of the first one present.
fn take_first(map: &mut Map<String, Value>, keys: &[&str]) -> Option<Value> {
    keys.iter().fold(None, |first, key| {
        let value = map.remove(*key);
        first.or(value)
    })
}

/// Renames the first other name present in `fields` to `names[0]`, unless that is present.
fn rename(fields: &mut Map<String, Value>, names: &[&str]) {
    if fields.contains_key(names[0]) {
        return;
    }
    if let Some(value) = names[1..].iter().find_map(|name| fields.remove(*name)) {
        fields.insert(names[0].into(), value);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::builtin::WriteTool;
    use crate::builtin::test_support::run;
    use crate::builtin::test_support::test_ctx;
    use crate::tool::Tool;
    use crate::typed::Typed;
    use std::path::Path;
    use std::time::Duration;
    use std::time::Instant;

    async fn edit(dir: &Path, edits: Value) -> Result<ToolOutput, ToolError> {
        run(&EditTool, json!({"path": "f.txt", "edits": edits}), test_ctx(dir)).await
    }

    /// Runs `edits` on a file holding `before`; returns the file's bytes after the call.
    async fn bytes_after(before: &str, edits: Value) -> Vec<u8> {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("f.txt"), before).unwrap();
        edit(dir.path(), edits).await.unwrap();
        std::fs::read(dir.path().join("f.txt")).unwrap()
    }

    /// Runs `edits` on a file holding `before`; returns the error and checks that the file
    /// did not change.
    async fn error(before: &str, edits: Value) -> String {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("f.txt");
        std::fs::write(&path, before).unwrap();
        let err = edit(dir.path(), edits).await.unwrap_err();
        assert_eq!(std::fs::read_to_string(&path).unwrap(), before);
        err.to_string().replace(&path.display().to_string(), "F")
    }

    #[tokio::test]
    async fn applies_multiple_edits_against_the_original() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("f.txt"), "fn a() {}\nfn b() {}\nfn c() {}\n").unwrap();
        let out = edit(
            dir.path(),
            json!([
                {"old_text": "fn c() {}", "new_text": "fn c() { 3 }"},
                {"old_text": "fn a() {}", "new_text": "fn a() { 1 }"},
            ]),
        )
        .await
        .unwrap();
        assert_eq!(
            std::fs::read_to_string(dir.path().join("f.txt")).unwrap(),
            "fn a() { 1 }\nfn b() {}\nfn c() { 3 }\n"
        );
        // No diff for the model when every old_text matched exactly; user interfaces get it.
        let path = dir.path().join("f.txt").display().to_string();
        assert_eq!(out.content_text(), format!("Edited {path}: 2 replacement(s), first change at line 1."));
        let details = out.details.unwrap();
        assert_eq!(details["replacements"], 2);
        assert_eq!(details["first_changed_line"], 1);
        assert_eq!(details["loose_matches"], json!([]));
        let diff = details["diff"].as_str().unwrap();
        assert!(diff.starts_with(&format!("--- {path}\n+++ {path}\n@@ -1,3 +1,3 @@\n")), "{diff}");
        assert!(diff.contains("-fn a() {}\n+fn a() { 1 }\n fn b() {}\n-fn c() {}\n+fn c() { 3 }\n"), "{diff}");
    }

    #[tokio::test]
    async fn rejects_missing_ambiguous_and_overlapping_text() {
        let before = "x = 1\nx = 1\ny = 2\n";
        let missing = error(before, json!([{"old_text": "z = 3", "new_text": ""}])).await;
        assert!(missing.contains("old_text #1 was not found in F"), "{missing}");
        let ambiguous = error(before, json!([{"old_text": "x = 1", "new_text": "x = 9"}])).await;
        assert_eq!(
            ambiguous,
            "old_text #1 matches 2 times in F (lines 1, 2); include more surrounding lines to make it unique."
        );
        let overlap = error(
            before,
            json!([
                {"old_text": "1\ny = 2", "new_text": "a"},
                {"old_text": "y = 2\n", "new_text": "b"},
            ]),
        )
        .await;
        assert_eq!(overlap, "old_text #1 and #2 overlap; merge them into one edit.");
        let empty = error(before, json!([{"old_text": "", "new_text": "a"}])).await;
        assert!(empty.contains("old_text #1 is empty"), "{empty}");
    }

    #[tokio::test]
    async fn preserves_crlf_and_bom() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("f.txt"), "\u{feff}one\r\ntwo\r\n").unwrap();
        edit(dir.path(), json!([{"old_text": "one\ntwo", "new_text": "one\nTWO"}]))
            .await
            .unwrap();
        assert_eq!(std::fs::read_to_string(dir.path().join("f.txt")).unwrap(), "\u{feff}one\r\nTWO\r\n");
    }

    #[tokio::test]
    async fn bytes_outside_the_replaced_text_never_change() {
        let one = |old: &str, new: &str| json!([{"old_text": old, "new_text": new}]);
        assert_eq!(bytes_after("a\r\nb\nc\r\n", one("a", "A")).await, b"A\r\nb\nc\r\n");
        // New line breaks follow the line where the replaced text starts.
        let both = json!([
            {"old_text": "y", "new_text": "Y1\nY2"},
            {"old_text": "x", "new_text": "X1\r\nX2"},
        ]);
        assert_eq!(bytes_after("x\r\ny\nz\r\n", both).await, b"X1\r\nX2\r\nY1\nY2\nz\r\n");
        assert_eq!(bytes_after("a\rb\rc", one("b", "B1\r\nB2")).await, b"a\rB1\rB2\rc");
        // The first line break inside the replaced text decides, even when later ones differ.
        assert_eq!(bytes_after("p\nq\r\nr", one("p\nq\nr", "P\r\nQ\nR")).await, b"P\nQ\nR");
        assert_eq!(
            bytes_after("\u{feff}one\r\ntwo\nthree", one("one\ntwo", "1\n2")).await,
            "\u{feff}1\r\n2\nthree".as_bytes()
        );
        // The last line has no break of its own: the file's most common one is used.
        assert_eq!(bytes_after("a\r\nb\r\nc", one("c", "c1\nc2")).await, b"a\r\nb\r\nc1\r\nc2");
        assert_eq!(bytes_after("single", one("single", "1\r\n2")).await, b"1\n2");
    }

    #[tokio::test]
    async fn ignores_trailing_whitespace_only_when_nothing_matches_exactly() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("f.txt");
        std::fs::write(&path, "fn a() {  \n    x();\t\n}\n").unwrap();
        let out = edit(dir.path(), json!([{"old_text": "fn a() {\n    x();", "new_text": "fn a() {\n    y();"}]))
            .await
            .unwrap();
        // Whitespace inside the match is replaced; the tab after it is not part of it.
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "fn a() {\n    y();\t\n}\n");
        let display = path.display().to_string();
        assert_eq!(
            out.content_text(),
            format!(
                "Edited {display}: 1 replacement(s), first change at line 1.\n\
                 Edit #1 matched after ignoring trailing whitespace.\n\n\
                 --- {display}\n+++ {display}\n@@ -1,3 +1,3 @@\n\
                 -fn a() {{  \n-    x();\t\n+fn a() {{\n+    y();\t\n }}\n"
            )
        );
        assert_eq!(out.details.unwrap()["loose_matches"], json!([{"edit": 1, "kind": "trailing_whitespace"}]));

        // Trailing whitespace on the last line of old_text: the match must end a line, and the
        // file's whitespace there is replaced too.
        let one = |old: &str, new: &str| json!([{"old_text": old, "new_text": new}]);
        assert_eq!(bytes_after("x = 1\t\ny = 2\n", one("x = 1  ", "x = 9")).await, b"x = 9\ny = 2\n");
        assert_eq!(bytes_after("a\t\n  b\n", one("a \n  ", "A\n  ")).await, b"A\n  b\n");
        let mid_line = error("x = 1 + y\n", one("x = 1  ", "x = 9")).await;
        assert!(mid_line.contains("was not found"), "{mid_line}");
        assert_eq!(bytes_after("a\nx = 1\t", one("x = 1 ", "y")).await, b"a\ny", "at the end of the file");
        // A first line of only whitespace is the end of the line before.
        assert_eq!(bytes_after("a\t\nb\n", one("  \nb", " \nB")).await, b"a \nB\n");
        assert_eq!(bytes_after("  \nb\n", one("\t\nb", "\t\nB")).await, b"\t\nB\n", "at the start of the file");
    }

    #[tokio::test]
    async fn folds_typographic_characters_but_replaces_only_the_match() {
        let one = |old: &str, new: &str| json!([{"old_text": old, "new_text": new}]);
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("f.txt");
        std::fs::write(&path, "let s = \"a\"; // “note”\n").unwrap();
        let out = edit(dir.path(), one("let s = “a”;", "let s = \"b\";")).await.unwrap();
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "let s = \"b\"; // “note”\n");
        assert!(
            out.content_text()
                .contains("\nEdit #1 matched after normalizing typographic quotes, dashes and spaces.\n\n--- ")
        );
        assert_eq!(out.details.unwrap()["loose_matches"], json!([{"edit": 1, "kind": "typography"}]));
        assert_eq!(bytes_after("a – b\u{a0}c  \nd\n", one("a - b c", "x")).await, b"x  \nd\n");
        assert_eq!(bytes_after("say ‘hi’ — ok\n", one("say 'hi' - ok\n", "bye\n")).await, b"bye\n");
    }

    #[tokio::test]
    async fn names_every_loose_edit_in_the_result() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("f.txt"), "a  \nb\n“c”\nd\n").unwrap();
        let out = edit(
            dir.path(),
            json!([
                {"old_text": "\"c\"", "new_text": "C"},
                {"old_text": "a\nb", "new_text": "AB"},
                {"old_text": "d", "new_text": "D"},
            ]),
        )
        .await
        .unwrap();
        let text = out.content_text();
        assert!(
            text.contains(
                "\nEdit #2 matched after ignoring trailing whitespace. Edit #1 matched after normalizing typographic \
                 quotes, dashes and spaces.\n"
            ),
            "{text}"
        );
        assert_eq!(std::fs::read_to_string(dir.path().join("f.txt")).unwrap(), "AB\nC\nD\n");
    }

    #[tokio::test]
    async fn uniqueness_is_decided_at_the_level_that_matched() {
        let one = |old: &str, new: &str| json!([{"old_text": old, "new_text": new}]);
        let loose = error("a  \nb\na\t\nb\n", one("a\nb", "X")).await;
        assert_eq!(
            loose,
            "old_text #1 matches 2 times in F (lines 1, 3) when trailing whitespace is ignored; include more \
             surrounding lines to make it unique."
        );
        // One exact match wins over loose ones.
        assert_eq!(bytes_after("a\nb\na  \nb\n", one("a\nb", "X")).await, b"X\na  \nb\n");
    }

    #[tokio::test]
    async fn many_multiline_edits_follow_the_same_rules() {
        // Enough old_texts with a complete line for the line index to be used.
        let count = matching::INDEX_FROM + 4;
        let block = |i: usize| format!("fn f{i}() {{\n    body({i});\n}}\n");
        let blocks: String = (0..count).map(block).collect();
        let edits =
            Value::Array((0..count).map(|i| json!({"old_text": block(i), "new_text": format!("// f{i}\n")})).collect());
        let expected: String = (0..count).map(|i| format!("// f{i}\n")).collect();
        let twice = format!("{blocks}{}", block(0));
        assert_eq!(
            error(&twice, edits.clone()).await,
            format!(
                "old_text #1 matches 2 times in F (lines 1, {}); include more surrounding lines to make it unique.",
                3 * count + 1
            )
        );
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("f.txt"), blocks.replacen("body(5);\n", "body(5);  \n", 1)).unwrap();
        let out = edit(dir.path(), edits).await.unwrap();
        assert!(out.content_text().contains("\nEdit #6 matched after ignoring trailing whitespace.\n"));
        assert_eq!(std::fs::read_to_string(dir.path().join("f.txt")).unwrap(), expected);
    }

    #[tokio::test]
    async fn names_the_lines_of_repeated_text() {
        let one = |old: &str| json!([{"old_text": old, "new_text": "y"}]);
        let three = error("a\nx = 1\nb\nc\nx = 1\nd\ne\nf\nx = 1\n", one("x = 1")).await;
        assert!(three.starts_with("old_text #1 matches 3 times in F (lines 2, 5, 9);"), "{three}");
        let twelve = error(&"x\n".repeat(12), one("x")).await;
        assert!(twelve.starts_with("old_text #1 matches 12 times in F (lines 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, …);"));
        let same_line = error("aaa\n", one("aa")).await;
        assert!(same_line.starts_with("old_text #1 matches 2 times in F (line 1);"), "{same_line}");
        let many = error(&"x\n".repeat(500), one("x")).await;
        assert!(many.starts_with("old_text #1 matches more than 100 times in F (lines 1, 2,"), "{many}");
    }

    #[tokio::test]
    async fn shows_the_lines_that_differ_only_in_indentation() {
        let one = |old: &str| json!([{"old_text": old, "new_text": "y"}]);
        let before = "class A:\n    def f(self):\n        return 1\n";
        assert_eq!(
            error(before, one("def f(self):\n    return 1")).await,
            "old_text #1 was not found in F, but lines 2-3 match it except for indentation. Copy them exactly, \
             including indentation:\n    def f(self):\n        return 1"
        );
        assert_eq!(
            error(before, one("            return 1")).await,
            "old_text #1 was not found in F, but line 3 matches it except for indentation. Copy it exactly, including \
             indentation:\n        return 1"
        );
        // Only whole lines count: here the line goes on after the text.
        assert_eq!(
            error("x = 1 + y\n", one("    x = 1")).await,
            "old_text #1 was not found in F. Read the file again and copy the exact text, including whitespace."
        );
        let twice = "if a:\n    x = 1\n    y = 2\nif b:\n        x = 1\n        y = 2\n";
        assert_eq!(
            error(twice, one("  x = 1\n  y = 2")).await,
            "old_text #1 was not found in F, but it matches lines 2-3, 5-6 except for indentation. Read the lines you \
             mean and copy them exactly, including indentation."
        );
        // A long block is cut.
        let long: String = (0..60).map(|i| format!("  line {i}\n")).collect();
        let needle: String = (0..60).map(|i| format!("line {i}\n")).collect();
        let message = error(&long, one(&needle)).await;
        assert!(message.contains("but lines 1-60 match it"), "{message}");
        assert!(message.ends_with("  line 39\n[20 more lines not shown]"), "{message}");
    }

    #[tokio::test]
    async fn points_at_the_first_line_when_the_rest_differs() {
        let one = |old: &str| json!([{"old_text": old, "new_text": "y"}]);
        let before = "fn main() {\n    let x = 1;\n}\n";
        assert_eq!(
            error(before, one("fn main() {\n    let x = 2;\n}")).await,
            "old_text #1 was not found in F. Its first line occurs at line 1, but the lines after it differ. Read \
             the file again and copy the exact text, including whitespace."
        );
        assert_eq!(
            error(before, one("nothing like this")).await,
            "old_text #1 was not found in F. Read the file again and copy the exact text, including whitespace."
        );
        // A first line found in too many places says nothing useful.
        let common = "}\n".repeat(6);
        assert_eq!(
            error(&common, one("}\nfn x() {")).await,
            "old_text #1 was not found in F. Read the file again and copy the exact text, including whitespace."
        );
    }

    #[tokio::test]
    async fn reports_the_first_line_that_really_changes() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("f.txt"), "a\nb\nc\nd\n").unwrap();
        let out = edit(
            dir.path(),
            json!([
                {"old_text": "a\n", "new_text": "a\n"},
                {"old_text": "b\nc", "new_text": "b\nC"},
            ]),
        )
        .await
        .unwrap();
        assert!(out.content_text().ends_with("2 replacement(s), first change at line 3."));
        let same = edit(dir.path(), json!([{"old_text": "d", "new_text": "d"}])).await.unwrap();
        assert!(same.content_text().starts_with("No changes: the new text equals the old text in "));
        // Also when old_text matched only loosely, and across mixed line breaks.
        std::fs::write(dir.path().join("f.txt"), "x = “1”  \r\ny\nz\r\n").unwrap();
        for old in ["x = \"1\"\ny", "x = “1”  \ny\nz"] {
            let same = edit(dir.path(), json!([{"old_text": old, "new_text": old}])).await.unwrap();
            assert!(same.content_text().starts_with("No changes"), "{}", same.content_text());
        }
        assert_eq!(std::fs::read_to_string(dir.path().join("f.txt")).unwrap(), "x = “1”  \r\ny\nz\r\n");
    }

    #[test]
    fn prepare_arguments_repairs_common_mistakes() {
        let prepare = |args: Value| EditTool.prepare_arguments(args);
        let edit = json!({"old_text": "a", "new_text": "b"});
        let canonical = json!({"path": "f", "edits": [edit]});
        // The list as a JSON string, a single edit, a single edit as a JSON string.
        assert_eq!(prepare(json!({"path": "f", "edits": json!([edit]).to_string()})), canonical);
        assert_eq!(prepare(json!({"path": "f", "edits": edit})), canonical);
        assert_eq!(prepare(json!({"path": "f", "edits": edit.to_string()})), canonical);
        // An edit at the top level, under any of its names, is added to the others.
        for (old, new) in [("old_text", "new_text"), ("oldText", "newText"), ("old_string", "new_string")] {
            assert_eq!(prepare(json!({"path": "f", old: "a", new: "b"})), canonical);
            assert_eq!(
                prepare(json!({"path": "f", "edits": [{"old_text": "x", "new_text": "y"}], old: "a", new: "b"})),
                json!({"path": "f", "edits": [{"old_text": "x", "new_text": "y"}, edit]})
            );
            assert_eq!(prepare(json!({"path": "f", "edits": [{old: "a", new: "b"}]})), canonical);
        }
        assert_eq!(prepare(json!({"path": "f", "edits": null, "old_text": "a", "new_text": "b"})), canonical);
        // A half edit becomes an item, so that the error names the missing field.
        assert_eq!(
            prepare(json!({"path": "f", "old_text": "a"})),
            json!({"path": "f", "edits": [{"old_text": "a"}]})
        );
        assert_eq!(prepare(json!({"file_path": "f", "edits": [edit]})), canonical);
        assert_eq!(
            prepare(json!({"path": "f", "file_path": "g", "edits": [edit]})),
            json!({"path": "f", "file_path": "g", "edits": [edit]})
        );
        // Shapes it does not know are left alone.
        for odd in [
            json!("text"),
            json!([1, 2]),
            json!({"path": "f", "edits": "not json"}),
            json!({"path": "f", "edits": "42"}),
            json!({"path": "f", "edits": 7, "old_text": "a", "new_text": "b"}),
            json!({"path": "f", "edits": [{"old_text": "a", "oldText": "z", "new_text": "b"}, 3]}),
        ] {
            assert_eq!(prepare(odd.clone()), odd);
        }
        // The result deserializes.
        let args: EditArgs = serde_json::from_value(prepare(json!({"file_path": "f", "oldText": "a", "newText": "b"})))
            .unwrap();
        assert_eq!((args.path.as_str(), args.edits.len()), ("f", 1));
    }

    #[tokio::test]
    async fn check_finds_every_problem_without_writing() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("f.txt");
        std::fs::write(&path, "x\nx\ny\n").unwrap();
        let tool = Typed::new(EditTool);
        let ctx = test_ctx(dir.path());
        let call = |old: &str| json!({"path": "f.txt", "edits": [{"old_text": old, "new_text": "z"}]});
        let ambiguous = tool.check(&call("x"), &ctx).await.unwrap_err();
        assert!(ambiguous.to_string().contains("matches 2 times"), "{ambiguous}");
        let missing = tool.check(&call("w"), &ctx).await.unwrap_err();
        assert!(missing.to_string().contains("was not found"), "{missing}");
        let other = |path: &str| json!({"path": path, "edits": [{"old_text": "x", "new_text": "z"}]});
        assert!(tool.check(&other("nope.txt"), &ctx).await.unwrap_err().to_string().contains("File not found"));
        assert!(tool.check(&other("."), &ctx).await.unwrap_err().to_string().ends_with("is a directory."));
        std::fs::write(dir.path().join("bin"), [b'x', 0xff, 0xfe]).unwrap();
        let binary = tool.check(&other("bin"), &ctx).await.unwrap_err();
        assert!(binary.to_string().ends_with("is not valid UTF-8 text."), "{binary}");
        tool.check(&call("y"), &ctx).await.unwrap();
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "x\nx\ny\n", "check never writes");
        tool.execute(call("y"), ctx.clone()).await.unwrap();
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "x\nx\nz\n");
        // The edit recorded the version it wrote: write may replace the file without a read.
        run(&WriteTool, json!({"path": "f.txt", "content": "w"}), ctx).await.unwrap();
    }

    #[tokio::test]
    async fn edits_utf16_files_in_their_encoding() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("Localizable.strings");
        let le = |text: &str| -> Vec<u8> {
            [0xFF, 0xFE].into_iter().chain(text.encode_utf16().flat_map(u16::to_le_bytes)).collect()
        };
        let be = |text: &str| -> Vec<u8> {
            [0xFE, 0xFF].into_iter().chain(text.encode_utf16().flat_map(u16::to_be_bytes)).collect()
        };
        let before = "\"title\" = \"Hello\";\r\n\"emoji\" = \"😀\";\r\n";
        let after = "\"title\" = \"Grüß Gott\";\r\n\"emoji\" = \"😀\";\r\n";
        for encode in [le, be] {
            std::fs::write(&path, encode(before)).unwrap();
            let edits = json!([{"old_text": "\"Hello\"", "new_text": "\"Grüß Gott\""}]);
            let out = run(&EditTool, json!({"path": "Localizable.strings", "edits": edits}), test_ctx(dir.path()))
                .await
                .unwrap();
            assert!(out.content_text().contains("1 replacement(s), first change at line 1"));
            assert_eq!(std::fs::read(&path).unwrap(), encode(after));
        }
        // An unpaired surrogate could not be written back as it was.
        std::fs::write(&path, [0xFF, 0xFE, 0x3D, 0xD8, b'a', 0]).unwrap();
        let edits = json!([{"old_text": "a", "new_text": "b"}]);
        let err = run(&EditTool, json!({"path": "Localizable.strings", "edits": edits}), test_ctx(dir.path()))
            .await
            .unwrap_err();
        assert!(err.to_string().ends_with("is not valid UTF-16 text."), "{err}");
    }

    #[tokio::test]
    async fn files_that_cannot_be_edited_are_named_as_such() {
        use std::os::unix::ffi::OsStrExt;
        use std::os::unix::fs::PermissionsExt;
        let dir = tempfile::tempdir().unwrap();
        let tool = Typed::new(EditTool);
        let ctx = test_ctx(dir.path());
        let call = |path: &str| json!({"path": path, "edits": [{"old_text": "a", "new_text": "b"}]});
        let error = |err: ToolError| err.to_string();

        let fifo = dir.path().join("pipe");
        let name = std::ffi::CString::new(fifo.as_os_str().as_bytes()).unwrap();
        assert_eq!(unsafe { libc::mkfifo(name.as_ptr(), 0o644) }, 0);
        let expected = format!("{} is not a regular file; it cannot be edited.", fifo.display());
        assert_eq!(error(tool.check(&call("pipe"), &ctx).await.unwrap_err()), expected);
        assert_eq!(error(tool.execute(call("pipe"), ctx.clone()).await.unwrap_err()), expected);

        // Root may write anything.
        if unsafe { libc::geteuid() } != 0 {
            let locked = dir.path().join("locked.txt");
            std::fs::write(&locked, "a").unwrap();
            std::fs::set_permissions(&locked, std::fs::Permissions::from_mode(0o444)).unwrap();
            let expected = format!("{} is read-only.", locked.display());
            assert_eq!(error(tool.check(&call("locked.txt"), &ctx).await.unwrap_err()), expected);
            assert_eq!(std::fs::read_to_string(&locked).unwrap(), "a");
        }

        // A path that does not exist, when a variant of it does: a hint, not a silent switch.
        let real = dir.path().join("Screenshot 1.06.07\u{202F}PM.txt");
        std::fs::write(&real, "a").unwrap();
        let err = error(tool.check(&call("@Screenshot 1.06.07 PM.txt"), &ctx).await.unwrap_err());
        assert!(err.ends_with(&format!("Did you mean {}?", real.display())), "{err}");
        assert_eq!(std::fs::read_to_string(&real).unwrap(), "a");
        let err = error(tool.check(&call("nothing.txt"), &ctx).await.unwrap_err());
        assert!(err.ends_with("Use write to create a new file."), "{err}");
    }

    /// Release builds run the full sizes (`cargo test --release`); debug builds check the same
    /// shapes smaller. The limits are far above the expected times and catch only a blow-up.
    #[tokio::test]
    async fn large_files_and_many_edits_stay_fast() {
        let full = !cfg!(debug_assertions);
        let limit = Duration::from_secs(if full { 5 } else { 30 });
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("f.txt");

        // Many edits spread over a large file: one-line old_texts are each searched for in the
        // whole file, three-line ones are found from the line index.
        let (lines, edits) = if full { (100_000, 2_000) } else { (20_000, 400) };
        let line = |i: usize| format!("    let value_{i} = compute(\"{i}\", {}) + offset;\n", i % 97);
        let content: String = (0..lines).map(line).collect();
        let step = lines / edits;
        let one_line = |i: usize| (format!("value_{i} = compute(\"{i}\""), format!("value_{i} = other(\"{i}\""));
        let three_lines =
            |i: usize| (format!("{}{}{}", line(i), line(i + 1), line(i + 2).trim_end()), "other();".to_string());
        let shapes: [&dyn Fn(usize) -> (String, String); 2] = [&one_line, &three_lines];
        for shape in shapes {
            std::fs::write(&path, &content).unwrap();
            let list: Vec<Value> = (0..edits)
                .map(|k| {
                    let (old, new) = shape(k * step + step / 2);
                    json!({"old_text": old, "new_text": new})
                })
                .collect();
            let started = Instant::now();
            let out = edit(dir.path(), json!(list)).await.unwrap();
            assert!(started.elapsed() < limit, "{edits} edits: {:?}", started.elapsed());
            assert_eq!(out.details.unwrap()["replacements"], edits);
            let after = std::fs::read_to_string(&path).unwrap();
            assert_eq!(after.matches("other(").count(), edits);
        }

        // One edit near the end of a file at the size limit, with CRLF line breaks.
        let size = if full { MAX_EDIT_BYTES as usize - 1024 } else { 2 * 1024 * 1024 };
        let mut big = String::with_capacity(size);
        let mut i = 0;
        while big.len() + 100 < size {
            big.push_str(&format!("line {i} of a large file, padded to look like code;\r\n"));
            i += 1;
        }
        std::fs::write(&path, &big).unwrap();
        let last = format!("line {} of", i - 2);
        let started = Instant::now();
        edit(dir.path(), json!([{"old_text": last, "new_text": "changed\nline"}])).await.unwrap();
        assert!(started.elapsed() < limit, "edit near the end: {:?}", started.elapsed());
        assert_eq!(std::fs::read_to_string(&path).unwrap(), big.replacen(&last, "changed\r\nline", 1));

        // old_text that occurs on every line.
        let started = Instant::now();
        let err = edit(dir.path(), json!([{"old_text": "padded", "new_text": "x"}])).await.unwrap_err();
        assert!(started.elapsed() < limit, "many matches: {:?}", started.elapsed());
        assert!(err.to_string().contains("matches more than 100 times"));
    }
}
