//! Reference models of the three tools, written from their documented behavior and kept simple
//! on purpose: how read splits, decodes and pages a file; which bytes write leaves; how edit
//! finds an old_text (exactly, then with trailing whitespace ignored, then with typography
//! folded) and which bytes it replaces. The tools stream, index and map offsets; these models
//! copy and search. A difference between the two is a bug in one of them.

use serde_json::Value;
use serde_json::json;
use std::borrow::Cow;
use std::cell::OnceCell;

/// The default output limits of read.
pub const MAX_LINES: usize = 2000;
pub const MAX_TOKENS: usize = 10_000;
/// The bytes the token budget stands for, at 4 bytes per token.
pub const MAX_BYTES: usize = MAX_TOKENS * 4;
pub const MAX_LINE_CHARS: usize = 2000;
/// The bytes of a long line read decodes. Characters past them are counted as the bytes that
/// start a character, so for invalid UTF-8 there the count is approximate (see text.rs).
const KEPT_BYTES: usize = (MAX_LINE_CHARS + 1) * 4;
/// A NUL byte among the first this many bytes makes a file binary.
pub const SNIFF_BYTES: usize = 8 * 1024;

// ---------------------------------------------------------------------------------------------
// read

/// A text file as read shows it: its lines, decoded, without line breaks.
pub struct ReadModel {
    pub lines: Vec<Line>,
}

pub struct Line {
    pub text: String,
    /// Characters, as the truncation note gives them.
    pub chars: u64,
}

/// The lines of `body`: `\r\n`, a lone `\r` and `\n` each end one.
pub fn split_lines(body: &[u8]) -> Vec<&[u8]> {
    let mut lines = Vec::new();
    let (mut start, mut at) = (0, 0);
    while let Some(i) = memchr::memchr2(b'\r', b'\n', &body[at..]) {
        let pos = at + i;
        lines.push(&body[start..pos]);
        at = if body[pos] == b'\r' && body.get(pos + 1) == Some(&b'\n') { pos + 2 } else { pos + 1 };
        start = at;
    }
    if start < body.len() {
        lines.push(&body[start..]);
    }
    lines
}

/// The line breaks in `text`, in order.
pub fn line_breaks(text: &[u8]) -> Vec<&'static str> {
    let mut breaks = Vec::new();
    let mut at = 0;
    while let Some(i) = memchr::memchr2(b'\r', b'\n', &text[at..]) {
        let pos = at + i;
        if text[pos] == b'\r' && text.get(pos + 1) == Some(&b'\n') {
            breaks.push("\r\n");
            at = pos + 2;
        } else {
            breaks.push(if text[pos] == b'\r' { "\r" } else { "\n" });
            at = pos + 1;
        }
    }
    breaks
}

/// Where each line of a UTF-8 file starts, in bytes from the start of the file; `None` for
/// UTF-16.
pub fn line_starts(bytes: &[u8]) -> Option<Vec<usize>> {
    if utf16(bytes).is_some() {
        return None;
    }
    let bom = if bytes.starts_with(super::corpus::UTF8_BOM) { 3 } else { 0 };
    let body = &bytes[bom..];
    Some(split_lines(body).iter().map(|line| bom + (line.as_ptr() as usize - body.as_ptr() as usize)).collect())
}

/// The UTF-16 encoding of a file with a byte order mark: `Some(big_endian)`.
pub fn utf16(bytes: &[u8]) -> Option<bool> {
    match bytes {
        [0xFF, 0xFE, 0, 0, ..] => None,
        [0xFF, 0xFE, ..] => Some(false),
        [0xFE, 0xFF, ..] => Some(true),
        _ => None,
    }
}

/// UTF-16 after its byte order mark, decoded; unpaired surrogates and an odd last byte become
/// U+FFFD.
pub fn decode_utf16(body: &[u8], big_endian: bool) -> String {
    let (pairs, _) = body.as_chunks::<2>();
    let units = pairs.iter().map(|&p| if big_endian { u16::from_be_bytes(p) } else { u16::from_le_bytes(p) });
    let mut text: String = char::decode_utf16(units).map(|c| c.unwrap_or('\u{FFFD}')).collect();
    if body.len() % 2 == 1 {
        text.push('\u{FFFD}');
    }
    text
}

pub fn is_binary(bytes: &[u8]) -> bool {
    utf16(bytes).is_none() && memchr::memchr(0, &bytes[..bytes.len().min(SNIFF_BYTES)]).is_some()
}

/// Bytes that start a character (or are invalid on their own): not 0x80-0xBF.
fn char_starts(bytes: &[u8]) -> u64 {
    bytes.iter().filter(|b| !(0x80..0xC0).contains(*b)).count() as u64
}

impl ReadModel {
    /// The model of a file; `None` when read takes it for binary.
    pub fn of(bytes: &[u8]) -> Option<ReadModel> {
        if let Some(big_endian) = utf16(bytes) {
            let text = decode_utf16(&bytes[2..], big_endian);
            let lines = split_lines(text.as_bytes())
                .into_iter()
                .map(|line| {
                    let text = String::from_utf8(line.to_vec()).unwrap();
                    Line {
                        chars: text.chars().count() as u64,
                        text,
                    }
                })
                .collect();
            return Some(ReadModel { lines });
        }
        if is_binary(bytes) {
            return None;
        }
        let body = bytes.strip_prefix(super::corpus::UTF8_BOM).unwrap_or(bytes);
        let lines = split_lines(body)
            .into_iter()
            .map(|line| {
                let text = String::from_utf8_lossy(line).into_owned();
                let chars = if std::str::from_utf8(line).is_ok() || line.len() <= KEPT_BYTES {
                    text.chars().count() as u64
                } else {
                    String::from_utf8_lossy(&line[..KEPT_BYTES]).chars().count() as u64 + char_starts(&line[KEPT_BYTES..])
                };
                Line { text, chars }
            })
            .collect();
        Some(ReadModel { lines })
    }

    pub fn total(&self) -> u64 {
        self.lines.len() as u64
    }

    /// Line `i` (0-based) as read shows it: cut after 2000 characters, with its length.
    pub fn shown(&self, i: usize) -> Cow<'_, str> {
        let line = &self.lines[i];
        if line.chars <= MAX_LINE_CHARS as u64 {
            return Cow::Borrowed(&line.text);
        }
        let cut = line.text.char_indices().nth(MAX_LINE_CHARS).map_or(line.text.len(), |(at, _)| at);
        Cow::Owned(format!(
            "{}… [line truncated: {MAX_LINE_CHARS} of {} characters]",
            &line.text[..cut],
            line.chars
        ))
    }

    /// The lines (0-based) of the window from line `offset` on, and whether the byte budget
    /// ended it.
    pub fn page(&self, offset: u64, max_lines: usize) -> (Vec<usize>, bool) {
        let mut taken = Vec::new();
        let mut bytes = 0;
        for i in (offset.max(1) - 1) as usize..self.lines.len() {
            if taken.len() >= max_lines {
                break;
            }
            let len = self.shown(i).len();
            if !taken.is_empty() && bytes + len + 1 > MAX_BYTES {
                return (taken, true);
            }
            bytes += len + 1;
            taken.push(i);
        }
        (taken, false)
    }

    /// What read returns for `offset` and `limit`: the text and details, or the error.
    pub fn expected(&self, display: &str, offset: Option<u64>, limit: Option<u64>) -> Result<(String, Value), String> {
        let start = offset.unwrap_or(1).max(1);
        let max_lines = (limit.unwrap_or(MAX_LINES as u64) as usize).clamp(1, MAX_LINES);
        let (lines, byte_limited) = self.page(start, max_lines);
        let total = self.total();
        if lines.is_empty() {
            if total == 0 {
                return Ok(("(empty file)".into(), json!({"path": display, "total_lines": 0})));
            }
            return Err(format!("offset {start} is beyond the end of {display} ({total} lines)."));
        }
        let end = start + lines.len() as u64 - 1;
        let mut out = String::new();
        for &i in &lines {
            out.push_str(&self.shown(i));
            out.push('\n');
        }
        let more = end < total;
        if more {
            let why = if byte_limited { format!(" ({MAX_TOKENS}-token limit)") } else { String::new() };
            out.push_str(&format!("\n[Showing lines {start}-{end} of {total}{why}. Use offset={} to continue.]", end + 1));
        }
        let details = json!({
            "path": display,
            "start_line": start,
            "end_line": end,
            "total_lines": total,
            "truncated": more,
        });
        Ok((out, details))
    }

    /// The text a model would write back after reading the file: its lines joined by `\n`,
    /// with a final one when the file ends with a line break. `None` when read did not show
    /// the file losslessly: a line was cut, or bytes were replaced.
    pub fn content(&self, bytes: &[u8]) -> Option<String> {
        if self.lines.iter().any(|l| l.chars > MAX_LINE_CHARS as u64 || l.text.contains('\u{FFFD}')) {
            return None;
        }
        let mut text = self.lines.iter().map(|l| l.text.as_str()).collect::<Vec<_>>().join("\n");
        let ends_with_break = match utf16(bytes) {
            Some(big_endian) => decode_utf16(&bytes[2..], big_endian).ends_with(['\r', '\n']),
            None => bytes.ends_with(b"\n") || bytes.ends_with(b"\r"),
        };
        if ends_with_break && !self.lines.is_empty() {
            text.push('\n');
        }
        Some(text)
    }
}

// ---------------------------------------------------------------------------------------------
// write

const UTF8_BOM: &str = "\u{FEFF}";
/// How much of a replaced file decides what write keeps of its format.
pub const FORMAT_PROBE_BYTES: usize = 64 * 1024;

/// The bytes write leaves when `content` replaces a file holding `old` (`None`: a new file),
/// and what it says it kept. A replaced UTF-16 file stays UTF-16 with its byte order mark; a
/// replaced UTF-8 file keeps its byte order mark. Both keep CRLF line endings when the first
/// 64 KiB (decoded, for UTF-16) have a CRLF and no bare LF, unless the content has a CR of its
/// own.
pub fn written(old: Option<&[u8]>, content: &str) -> (Vec<u8>, Vec<&'static str>) {
    let mut out = content.to_string();
    let mut kept = Vec::new();
    let Some(old) = old else {
        return (out.into_bytes(), kept);
    };
    let probe = &old[..old.len().min(FORMAT_PROBE_BYTES)];
    if let Some(big_endian) = utf16(old) {
        let decoded = decode_utf16(&probe[2..], big_endian);
        let decoded = decoded.as_bytes();
        let bare_lf = decoded.iter().enumerate().any(|(i, b)| *b == b'\n' && (i == 0 || decoded[i - 1] != b'\r'));
        let crlf = !bare_lf && decoded.windows(2).any(|w| w == b"\r\n");
        if let Some(rest) = out.strip_prefix(UTF8_BOM) {
            out = rest.to_string();
        }
        kept.push(if big_endian { "UTF-16BE encoding" } else { "UTF-16LE encoding" });
        if crlf && out.contains('\n') && !out.contains('\r') {
            out = out.replace('\n', "\r\n");
            kept.push("CRLF line endings");
        }
        let units = out.encode_utf16();
        let bytes = if big_endian {
            [0xFE, 0xFF].into_iter().chain(units.flat_map(u16::to_be_bytes)).collect()
        } else {
            [0xFF, 0xFE].into_iter().chain(units.flat_map(u16::to_le_bytes)).collect()
        };
        return (bytes, kept);
    }
    let cut = old.len() > FORMAT_PROBE_BYTES;
    let utf8 = match std::str::from_utf8(probe) {
        Ok(_) => true,
        Err(err) => cut && err.error_len().is_none(),
    };
    if !utf8 {
        return (out.into_bytes(), kept);
    }
    let bare_lf = probe.iter().enumerate().any(|(i, b)| *b == b'\n' && (i == 0 || probe[i - 1] != b'\r'));
    let crlf = !bare_lf && probe.windows(2).any(|w| w == b"\r\n");
    if probe.starts_with(UTF8_BOM.as_bytes()) && !out.starts_with(UTF8_BOM) {
        out.insert_str(0, UTF8_BOM);
        kept.push("byte order mark");
    }
    if crlf && out.contains('\n') && !out.contains('\r') {
        out = out.replace('\n', "\r\n");
        kept.push("CRLF line endings");
    }
    (out.into_bytes(), kept)
}

/// The text of a successful write.
pub fn write_result(display: &str, created: bool, bytes: usize, kept: &[&str]) -> String {
    let action = if created { "created" } else { "overwritten" };
    let kept = if kept.is_empty() { String::new() } else { format!(", {} kept", kept.join(" and ")) };
    format!("Wrote {bytes} bytes to {display} ({action}{kept}).")
}

// ---------------------------------------------------------------------------------------------
// edit

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Level {
    Exact,
    TrailingWhitespace,
    Typography,
}

impl Level {
    pub fn name(self) -> &'static str {
        match self {
            Level::Exact => "exact",
            Level::TrailingWhitespace => "trailing_whitespace",
            Level::Typography => "typography",
        }
    }
}

/// Counted before "more than 100"; line numbers are given for the first 10.
const MAX_COUNT: usize = 100;
const KEEP: usize = 10;
const PLACES: usize = 5;

/// `text` with `\r\n` and `\r` written as `\n`.
pub fn lf(text: &str) -> String {
    text.replace("\r\n", "\n").replace('\r', "\n")
}

/// The ASCII character a typographic quote, dash or space folds to.
pub fn fold_char(c: char) -> Option<char> {
    match c {
        '\u{2018}' | '\u{2019}' | '\u{201A}' | '\u{201B}' => Some('\''),
        '\u{201C}' | '\u{201D}' | '\u{201E}' | '\u{201F}' => Some('"'),
        '\u{2010}'..='\u{2015}' | '\u{2212}' => Some('-'),
        '\u{00A0}' | '\u{2002}'..='\u{200A}' | '\u{202F}' | '\u{205F}' | '\u{3000}' => Some(' '),
        _ => None,
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Shape {
    /// Line breaks as `\n`.
    Lf,
    /// Also without whitespace at the end of each line.
    TrimEnd,
    /// Also with typographic quotes, dashes and spaces folded to ASCII.
    Fold,
    /// Line breaks as `\n`, without whitespace at both ends of each line.
    Trim,
}

/// A normalized copy of the text after the byte order mark. Every byte of it knows the span
/// of original bytes it stands for.
struct View {
    text: String,
    origin: Vec<(usize, usize)>,
}

/// old_text for a loose level: whitespace at the end of each line dropped (a last line of
/// only whitespace is kept: it is the indentation of the next line), folded when asked.
struct Loose {
    text: String,
    /// The first line held only whitespace: the span reaches back over the whitespace that
    /// ends the line before.
    from_line_end: bool,
    /// The last line lost trailing whitespace: the match must end a line, and the span takes
    /// in the file's whitespace there.
    to_line_end: bool,
}

fn loosen(needle: &str, fold: bool) -> Option<Loose> {
    let segments: Vec<&str> = needle.split('\n').collect();
    let last = segments.len() - 1;
    let (mut from_line_end, mut to_line_end) = (false, false);
    let mut parts = Vec::new();
    for (i, segment) in segments.iter().enumerate() {
        let trimmed = segment.trim_end();
        let kept = if i < last {
            from_line_end |= i == 0 && trimmed.is_empty() && !segment.is_empty();
            trimmed
        } else if trimmed.is_empty() {
            segment
        } else {
            to_line_end = trimmed.len() < segment.len();
            trimmed
        };
        parts.push(if fold { kept.chars().map(|c| fold_char(c).unwrap_or(c)).collect() } else { kept.to_string() });
    }
    let text = parts.join("\n");
    text.chars().any(|c| !c.is_whitespace()).then_some(Loose {
        text,
        from_line_end,
        to_line_end,
    })
}

/// Where an old_text was found.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Located {
    Unique { level: Level, start: usize, end: usize },
    Several { level: Level, count: usize, lines: Vec<usize> },
    Missing,
}

/// What a successful edit does.
#[derive(Debug)]
pub struct Edited {
    /// The file afterwards.
    pub bytes: Vec<u8>,
    /// The result text, without the diff shown after a loose match.
    pub text: String,
    pub replacements: usize,
    pub first_changed_line: Option<usize>,
    /// Edits (1-based) that matched loosely.
    pub loose: Vec<(usize, Level)>,
    /// The texts the diff is between: before and after, line breaks as `\n`, no byte order mark.
    pub old_lf: String,
    pub new_lf: String,
    /// When every old_text matched exactly: the text as read shows it, with each old_text
    /// replaced by its new_text as read would show that. A file whose lines differ from it
    /// changed in lines the edit did not touch.
    pub shown_after: Option<String>,
    /// A replacement would have put a CR right before an LF, or an LF right after a CR, that
    /// were not together before (read would take the two for one CRLF line break); the CR was
    /// made a CRLF to keep them apart.
    pub joins_breaks: bool,
}

/// Where a line's content starts and ends, and its line break if it has one.
type LineSpan = (usize, usize, Option<(usize, usize)>);

/// A UTF-8 file as edit sees it.
pub struct EditModel<'a> {
    raw: &'a str,
    /// Length of the byte order mark: 0 or 3.
    base: usize,
    /// `(start, end)` of every line break.
    breaks: Vec<(usize, usize)>,
    views: [OnceCell<View>; 4],
}

impl<'a> EditModel<'a> {
    pub fn new(raw: &'a str) -> Self {
        let base = if raw.starts_with(UTF8_BOM) { 3 } else { 0 };
        let bytes = raw.as_bytes();
        let mut breaks = Vec::new();
        let mut at = base;
        while let Some(i) = memchr::memchr2(b'\r', b'\n', &bytes[at..]) {
            let pos = at + i;
            let end = if bytes[pos] == b'\r' && bytes.get(pos + 1) == Some(&b'\n') { pos + 2 } else { pos + 1 };
            breaks.push((pos, end));
            at = end;
        }
        Self {
            raw,
            base,
            breaks,
            views: Default::default(),
        }
    }

    /// The 0-based line that holds byte `offset`.
    pub fn line_of(&self, offset: usize) -> usize {
        self.breaks.partition_point(|&(_, end)| end <= offset)
    }

    fn line_start(&self, line: usize) -> usize {
        if line == 0 { self.base } else { self.breaks[line - 1].1 }
    }

    /// Lines: content start and end, and the line break after it if any.
    fn lines(&self) -> Vec<LineSpan> {
        let mut out = Vec::with_capacity(self.breaks.len() + 1);
        for k in 0..=self.breaks.len() {
            let start = self.line_start(k);
            match self.breaks.get(k) {
                Some(&brk) => out.push((start, brk.0, Some(brk))),
                None if start < self.raw.len() => out.push((start, self.raw.len(), None)),
                None => {}
            }
        }
        out
    }

    /// The line break that new text written at `offset` gets: that of the line there, or the
    /// file's most common one when that line has none.
    pub fn break_at(&self, offset: usize) -> &'static str {
        self.line_break(self.line_of(offset)).unwrap_or_else(|| self.usual_break())
    }

    fn line_break(&self, line: usize) -> Option<&'static str> {
        let &(start, end) = self.breaks.get(line)?;
        Some(match &self.raw.as_bytes()[start..end] {
            b"\r\n" => "\r\n",
            b"\r" => "\r",
            _ => "\n",
        })
    }

    /// The most common line break; ties go to `\n`, then `\r\n`.
    fn usual_break(&self) -> &'static str {
        let count = |brk: &str| (0..self.breaks.len()).filter(|&k| self.line_break(k) == Some(brk)).count();
        let (lf, crlf, cr) = (count("\n"), count("\r\n"), count("\r"));
        if lf >= crlf && lf >= cr {
            "\n"
        } else if crlf >= cr {
            "\r\n"
        } else {
            "\r"
        }
    }

    fn view(&self, shape: Shape) -> &View {
        self.views[shape as usize].get_or_init(|| {
            let mut text = String::with_capacity(self.raw.len());
            let mut origin = Vec::with_capacity(self.raw.len());
            for (start, end, brk) in self.lines() {
                let content = &self.raw[start..end];
                let (from, to) = match shape {
                    Shape::Lf => (start, end),
                    Shape::TrimEnd | Shape::Fold => (start, start + content.trim_end().len()),
                    Shape::Trim => {
                        let from = start + content.len() - content.trim_start().len();
                        (from, from + content.trim().len())
                    }
                };
                for (i, c) in self.raw[from..to].char_indices() {
                    let o = from + i;
                    match fold_char(c).filter(|_| shape == Shape::Fold) {
                        Some(ascii) => {
                            text.push(ascii);
                            origin.push((o, o + c.len_utf8()));
                        }
                        None => {
                            text.push(c);
                            origin.extend((o..o + c.len_utf8()).map(|b| (b, b + 1)));
                        }
                    }
                }
                if let Some(brk) = brk {
                    text.push('\n');
                    origin.push(brk);
                }
            }
            View { text, origin }
        })
    }

    /// Starts of the occurrences of `pattern` in a view (overlapping ones included), at most
    /// `cap`; with `at_line_end` only those followed by a line break or the end.
    fn occurrences(view: &View, pattern: &str, at_line_end: bool, cap: usize) -> Vec<usize> {
        let hay = view.text.as_bytes();
        let finder = memchr::memmem::Finder::new(pattern.as_bytes());
        let mut found = Vec::new();
        let mut from = 0;
        while from <= hay.len()
            && let Some(i) = finder.find(&hay[from..])
        {
            let start = from + i;
            let end = start + pattern.len();
            if !at_line_end || end == hay.len() || hay[end] == b'\n' {
                found.push(start);
                if found.len() >= cap {
                    break;
                }
            }
            from = start + 1;
        }
        found
    }

    /// Finds `old_text` at the first level of the ladder where it occurs.
    pub fn locate(&self, old_text: &str) -> Located {
        let needle = lf(old_text);
        let view = self.view(Shape::Lf);
        let found = Self::occurrences(view, &needle, false, MAX_COUNT + 1);
        if !found.is_empty() {
            return self.outcome(Level::Exact, view, &found, needle.len(), None);
        }
        let Some(loose) = loosen(&needle, false) else {
            return Located::Missing;
        };
        let view = self.view(Shape::TrimEnd);
        let found = Self::occurrences(view, &loose.text, loose.to_line_end, MAX_COUNT + 1);
        if !found.is_empty() {
            return self.outcome(Level::TrailingWhitespace, view, &found, loose.text.len(), Some(&loose));
        }
        let Some(folded) = loosen(&needle, true) else {
            return Located::Missing;
        };
        let view = self.view(Shape::Fold);
        let found = Self::occurrences(view, &folded.text, folded.to_line_end, MAX_COUNT + 1);
        if !found.is_empty() {
            return self.outcome(Level::Typography, view, &found, folded.text.len(), Some(&folded));
        }
        Located::Missing
    }

    fn outcome(&self, level: Level, view: &View, found: &[usize], len: usize, loose: Option<&Loose>) -> Located {
        if let [s] = *found {
            let e = s + len;
            let start = match loose {
                Some(l) if l.from_line_end && s == 0 => self.base,
                Some(l) if l.from_line_end => view.origin[s - 1].1,
                _ => view.origin[s].0,
            };
            let end = match loose {
                Some(l) if l.to_line_end && e == view.text.len() => self.raw.len(),
                Some(l) if l.to_line_end => view.origin[e].0,
                _ => view.origin[e - 1].1,
            };
            return Located::Unique { level, start, end };
        }
        let mut lines: Vec<usize> = found.iter().take(KEEP).map(|&s| self.line_of(view.origin[s].0) + 1).collect();
        lines.dedup();
        Located::Several {
            level,
            count: found.len(),
            lines,
        }
    }

    /// What edit does with `edits` (old_text, new_text): the file and result afterwards, or the
    /// error.
    pub fn apply(&self, display: &str, edits: &[(String, String)]) -> Result<Edited, String> {
        if edits.is_empty() {
            return Err("invalid arguments: edits must contain at least one item".into());
        }
        if let Some(i) = edits.iter().position(|(old, _)| old.is_empty()) {
            return Err(format!(
                "invalid arguments: old_text #{} is empty; use write to create or replace a whole file",
                i + 1
            ));
        }
        // (start, end, new bytes, edit)
        let mut replacements: Vec<(usize, usize, String, usize)> = Vec::new();
        // A lone CR and an LF had to be kept apart at a seam.
        let mut kept_apart = false;
        // The same in the text as read shows it, while every match is exact.
        let mut shown: Option<Vec<(usize, usize, String)>> = Some(Vec::new());
        let mut loose = Vec::new();
        for (i, (old, new)) in edits.iter().enumerate() {
            let n = i + 1;
            match self.locate(old) {
                Located::Unique { level, start, end } => {
                    if level == Level::Exact
                        && let Some(shown) = &mut shown
                    {
                        let view = self.view(Shape::Lf);
                        let at = view.origin.partition_point(|o| o.0 < start);
                        let needle = lf(old);
                        shown.push((at, at + needle.len(), lf(new)));
                    } else {
                        shown = None;
                    }
                    let text = if new == old {
                        self.raw[start..end].to_string()
                    } else {
                        lf(new).replace('\n', self.break_at(start))
                    };
                    replacements.push((start, end, text, n));
                    if level != Level::Exact {
                        loose.push((n, level));
                    }
                }
                Located::Several { level, count, lines } => return Err(several(n, display, level, count, &lines)),
                Located::Missing => return Err(self.not_found(n, display, &lf(old))),
            }
        }
        replacements.sort_by_key(|r| r.0);
        if let Some(pair) = replacements.windows(2).find(|pair| pair[1].0 < pair[0].1) {
            return Err(format!("old_text #{} and #{} overlap; merge them into one edit.", pair[0].3, pair[1].3));
        }
        // A lone CR and an LF that would meet at a seam of the result: the CR becomes a CRLF.
        let bytes = self.raw.as_bytes();
        for i in 0..replacements.len() {
            let (start, end) = (replacements[i].0, replacements[i].1);
            if replacements[i].2.as_bytes() == &bytes[start..end] {
                continue;
            }
            let before = if i > 0 && replacements[i - 1].1 == start {
                let prev = &replacements[i - 1];
                prev.2.as_bytes().last().copied().or(prev.0.checked_sub(1).map(|k| bytes[k]))
            } else {
                start.checked_sub(1).map(|k| bytes[k])
            };
            let after = match replacements.get(i + 1) {
                Some(next) if next.0 == end => next.2.as_bytes().first().copied().or(bytes.get(next.1).copied()),
                _ => bytes.get(end).copied(),
            };
            let text = &mut replacements[i].2;
            if before == Some(b'\r') && text.as_bytes().first().copied().or(after) == Some(b'\n') {
                text.insert(0, '\n');
                kept_apart = true;
            }
            if text.ends_with('\r') && after == Some(b'\n') {
                text.push('\n');
                kept_apart = true;
            }
        }
        let raw = self.raw;
        let old_lf = lf(&raw[self.base..]);
        let shown_after = shown.map(|mut spans| {
            spans.sort_by_key(|s| s.0);
            let view = &self.view(Shape::Lf).text;
            let mut out = String::new();
            let mut at = 0;
            for (start, end, text) in spans {
                out.push_str(&view[at..start]);
                out.push_str(&text);
                at = end;
            }
            out.push_str(&view[at..]);
            out
        });
        let Some(first) = replacements.iter().find(|r| r.2 != raw[r.0..r.1]) else {
            return Ok(Edited {
                bytes: raw.as_bytes().to_vec(),
                text: format!("No changes: the new text equals the old text in {display}."),
                replacements: replacements.len(),
                first_changed_line: None,
                loose,
                new_lf: old_lf.clone(),
                old_lf,
                shown_after,
                joins_breaks: false,
            });
        };
        let same = raw[first.0..first.1].bytes().zip(first.2.bytes()).take_while(|(a, b)| a == b).count();
        let first_changed_line = self.line_of(first.0 + same) + 1;
        let mut out = String::with_capacity(raw.len());
        let mut at = 0;
        // Where replaced text begins and ends in the result.
        let mut seams = Vec::new();
        for (start, end, text, _) in &replacements {
            out.push_str(&raw[at..*start]);
            seams.push(out.len());
            out.push_str(text);
            seams.push(out.len());
            at = *end;
        }
        out.push_str(&raw[at..]);
        let bytes = out.as_bytes();
        let joins = seams.iter().any(|&p| p > 0 && bytes[p - 1] == b'\r' && bytes.get(p) == Some(&b'\n'));
        let joins_breaks = kept_apart || joins;
        let mut text = format!(
            "Edited {display}: {} replacement(s), first change at line {first_changed_line}.",
            replacements.len()
        );
        if !loose.is_empty() {
            text.push('\n');
            text.push_str(&loose_note(&loose));
        }
        let new_lf = lf(&out[self.base..]);
        Ok(Edited {
            bytes: out.into_bytes(),
            text,
            replacements: replacements.len(),
            first_changed_line: Some(first_changed_line),
            loose,
            old_lf,
            new_lf,
            shown_after,
            joins_breaks,
        })
    }

    /// The error for an old_text that was not found, with what it probably meant.
    fn not_found(&self, n: usize, display: &str, needle: &str) -> String {
        let missing = format!("old_text #{n} was not found in {display}");
        let lines: Vec<&str> = needle.split('\n').collect();
        let unknown = format!("{missing}. Read the file again and copy the exact text, including whitespace.");
        let Some(first) = lines.iter().position(|l| !l.trim().is_empty()) else {
            return unknown;
        };
        let last = lines.iter().rposition(|l| !l.trim().is_empty()).unwrap_or(first);
        // The lines match whole lines of the file when indentation is ignored.
        let block = lines[first..=last].iter().map(|l| l.trim()).collect::<Vec<_>>().join("\n");
        let view = self.view(Shape::Trim);
        let hay = view.text.as_bytes();
        let mut ranges = Vec::new();
        let mut count = 0;
        let line_starts = std::iter::once(0).chain(memchr::memchr_iter(b'\n', hay).map(|i| i + 1));
        for s in line_starts {
            let e = s + block.len();
            if hay[s..].starts_with(block.as_bytes()) && (e == hay.len() || hay[e] == b'\n') {
                count += 1;
                if ranges.len() < PLACES {
                    ranges.push((self.line_of(view.origin[s].0), self.line_of(view.origin[e - 1].1 - 1)));
                }
                if count > PLACES {
                    break;
                }
            }
        }
        if count > 0 {
            let more = count > PLACES;
            if let ([(first, last)], false) = (ranges.as_slice(), more) {
                let (lines, verb, them) = if first == last {
                    (format!("line {}", first + 1), "matches", "it")
                } else {
                    (format!("lines {}-{}", first + 1, last + 1), "match", "them")
                };
                return format!(
                    "{missing}, but {lines} {verb} it except for indentation. Copy {them} exactly, including \
                     indentation:\n{}",
                    self.excerpt(*first, *last)
                );
            }
            let ranges: Vec<(usize, usize)> = ranges.iter().map(|&(a, b)| (a + 1, b + 1)).collect();
            return format!(
                "{missing}, but it matches {} except for indentation. Read the lines you mean and copy them exactly, \
                 including indentation.",
                line_list(&ranges, more)
            );
        }
        if first == last {
            return unknown;
        }
        // Its first line occurs, the rest differs.
        let view = self.view(Shape::Lf);
        let found = Self::occurrences(view, lines[first].trim(), false, PLACES + 1);
        if (1..=PLACES).contains(&found.len()) {
            let mut at: Vec<(usize, usize)> = found
                .iter()
                .map(|&s| self.line_of(view.origin[s].0) + 1)
                .map(|l| (l, l))
                .collect();
            at.dedup();
            return format!(
                "{missing}. Its first line occurs at {}, but the lines after it differ. Read the file again and copy \
                 the exact text, including whitespace.",
                line_list(&at, false)
            );
        }
        unknown
    }

    /// Lines `first..=last` (0-based) as the file has them, line breaks as `\n`, up to 40 lines
    /// or 4 KiB.
    fn excerpt(&self, first: usize, last: usize) -> String {
        const LINES: usize = 40;
        const BYTES: usize = 4 * 1024;
        let mut out = String::new();
        let mut shown = 0;
        for line in first..=last {
            let start = self.line_start(line);
            let content = match self.breaks.get(line) {
                Some(&(brk, _)) => format!("{}\n", &self.raw[start..brk]),
                None => self.raw[start..].to_string(),
            };
            if shown == LINES || (shown > 0 && out.len() + content.len() > BYTES) {
                break;
            }
            let mut end = content.len().min(BYTES);
            while !content.is_char_boundary(end) {
                end -= 1;
            }
            out.push_str(&content[..end]);
            shown += 1;
            if end < content.len() {
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
        "old_text #{n} matches {times} times in {display} ({}){when}; include more surrounding lines to make it unique.",
        line_list(&ranges, count > KEEP)
    )
}

/// "line 7", "lines 7-9", "lines 3, 8-9, 12", with ", …" when there are more.
fn line_list(ranges: &[(usize, usize)], more: bool) -> String {
    let items: Vec<String> = ranges.iter().map(|&(a, b)| if a == b { a.to_string() } else { format!("{a}-{b}") }).collect();
    let noun = if !more && matches!(ranges, [(a, b)] if a == b) { "line" } else { "lines" };
    format!("{noun} {}{}", items.join(", "), if more { ", …" } else { "" })
}

/// "Edit #2 matched after ignoring trailing whitespace."
fn loose_note(loose: &[(usize, Level)]) -> String {
    let mut sentences = Vec::new();
    for (level, reason) in [
        (Level::TrailingWhitespace, "ignoring trailing whitespace"),
        (Level::Typography, "normalizing typographic quotes, dashes and spaces"),
    ] {
        let edits: Vec<String> = loose.iter().filter(|(_, l)| *l == level).map(|(n, _)| format!("#{n}")).collect();
        if !edits.is_empty() {
            let noun = if edits.len() == 1 { "Edit" } else { "Edits" };
            sentences.push(format!("{noun} {} matched after {reason}.", edits.join(", ")));
        }
    }
    sentences.join(" ")
}

/// The diff as the model sees it after a loose match: cut to 4 KiB at a line end.
pub fn diff_for_model(diff: &str) -> String {
    const LIMIT: usize = 4 * 1024;
    if diff.len() <= LIMIT {
        return diff.to_string();
    }
    let mut end = LIMIT;
    while !diff.is_char_boundary(end) {
        end -= 1;
    }
    let mut shown = diff[..end].to_string();
    match shown.rfind('\n') {
        Some(i) => shown.truncate(i + 1),
        None => shown.push('\n'),
    }
    shown.push_str("[diff truncated]");
    shown
}

/// Checks that `diff` is a unified diff from `old` to `new` (line breaks as `\n`): a header
/// naming `display`, hunks whose context and removed lines are those of `old` where their
/// headers put them, and that turn `old` into `new`; equal texts have an empty diff. A hunk
/// whose header gives another new range or other lengths than its lines is listed in the
/// result; anything else wrong is the error.
pub fn check_diff(display: &str, old: &str, new: &str, diff: &str) -> Result<Vec<String>, String> {
    if diff.is_empty() {
        return if old == new { Ok(Vec::new()) } else { Err("empty diff for different texts".into()) };
    }
    let old_lines: Vec<&str> = old.split_inclusive('\n').collect();
    let mut lines = diff.split_inclusive('\n').peekable();
    let header = format!("--- {display}\n+++ {display}\n");
    let first_two: String = lines.by_ref().take(2).collect();
    if first_two != header {
        return Err(format!("bad header {first_two:?}"));
    }
    let mut bad_headers = Vec::new();
    let mut out = String::with_capacity(new.len());
    let mut at = 0;
    let mut new_at = 0;
    while let Some(head) = lines.next() {
        let parse = |range: &str| -> Option<(usize, usize)> {
            let (start, len) = range.split_once(',').unwrap_or((range, "1"));
            let (start, len) = (start.parse::<usize>().ok()?, len.parse::<usize>().ok()?);
            // An empty range names the line before it.
            Some((if len == 0 { start } else { start.checked_sub(1)? }, len))
        };
        let ranges = head
            .strip_prefix("@@ -")
            .and_then(|h| h.strip_suffix(" @@\n"))
            .and_then(|h| h.split_once(" +"))
            .and_then(|(a, b)| Some((parse(a)?, parse(b)?)));
        let Some(((old_start, old_len), (new_start, new_len))) = ranges else {
            return Err(format!("bad hunk header {head:?}"));
        };
        if old_start < at || old_start > old_lines.len() {
            return Err(format!("hunk {head:?} out of order or out of range"));
        }
        for line in &old_lines[at..old_start] {
            out.push_str(line);
            new_at += 1;
        }
        at = old_start;
        let (mut seen_old, mut seen_new) = (0, 0);
        while let Some(line) = lines.next_if(|l| !l.starts_with("@@ ")) {
            let (tag, body) = line.split_at(1);
            let body = match lines.next_if(|l| *l == "\\ No newline at end of file\n") {
                Some(_) => body.strip_suffix('\n').unwrap_or(body),
                None => body,
            };
            if !matches!(tag, " " | "-" | "+") {
                return Err(format!("bad diff line {line:?}"));
            }
            if tag != "+" {
                if old_lines.get(at) != Some(&body) {
                    return Err(format!("hunk {head:?}: {tag}{body:?} is not old line {}", at + 1));
                }
                at += 1;
                seen_old += 1;
            }
            if tag != "-" {
                out.push_str(body);
                seen_new += 1;
            }
        }
        if (new_start, seen_old, seen_new) != (new_at, old_len, new_len) {
            let range = |start: usize, len: usize| if len == 1 { (start + 1).to_string() } else { format!("{},{len}", start + usize::from(len > 0)) };
            bad_headers.push(format!(
                "{} for @@ -{} +{} @@",
                head.trim_end(),
                range(old_start, seen_old),
                range(new_at, seen_new)
            ));
        }
        new_at += seen_new;
    }
    for line in &old_lines[at..] {
        out.push_str(line);
    }
    if out != new {
        return Err("the diff does not turn the old text into the new one".into());
    }
    Ok(bad_headers)
}
