//! Finding old_text in a file. The text is searched in views: normalized copies of the text
//! after the byte order mark that remember where each of their bytes came from, so a match in
//! any view maps back to an exact span of the original bytes and nothing outside it changes.
//!
//! Each old_text goes down a ladder and stops at the first level that finds it: exact, then
//! with trailing whitespace ignored, then with typographic quotes, dashes and spaces folded
//! to ASCII as well. Indentation always has to match.

use memchr::memchr;
use memchr::memchr2;
use memchr::memchr3;
use memchr::memmem;
use std::borrow::Cow;
use std::collections::HashMap;
use std::hash::BuildHasher;
use std::hash::BuildHasherDefault;
use std::hash::DefaultHasher;

/// Occurrences counted before a count is reported as "more than".
pub const MAX_COUNT: usize = 100;
/// Occurrences whose positions are kept, for line numbers in messages.
pub const KEEP: usize = 10;
/// Places worth naming when old_text was not found.
pub const PLACES: usize = 5;
/// From this many old_texts with a complete line in them (see [`has_complete_line`]) on, views
/// get a line index, so that each of them is found without scanning the whole view. Building
/// the index costs about as much as this many scans.
pub const INDEX_FROM: usize = 16;
/// A complete line with more occurrences than this is a poor guide: the view is scanned.
const MAX_CANDIDATES: usize = 1024;

/// How an old_text was found.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Level {
    Exact,
    /// After ignoring whitespace at the end of each line.
    TrailingWhitespace,
    /// After also folding typographic quotes, dashes and spaces to ASCII.
    Typography,
}

/// The file text and an index of its lines. A line break is `\r\n`, a lone `\r` or `\n`.
/// Offsets are kept as `u32`, so the text must be smaller than 4 GiB.
pub struct Text {
    raw: String,
    /// Length of the byte order mark, 0 or 3. Views start after it.
    base: usize,
    /// Where every line starts. The first line starts after the byte order mark; a text that
    /// ends with a line break also has an entry for the end of the text.
    starts: Vec<u32>,
}

impl Text {
    pub fn new(raw: String) -> Self {
        debug_assert!(raw.len() < u32::MAX as usize);
        let base = if raw.starts_with('\u{feff}') { 3 } else { 0 };
        let bytes = raw.as_bytes();
        let mut starts = vec![base as u32];
        let mut at = base;
        while let Some(found) = memchr2(b'\r', b'\n', &bytes[at..]) {
            at += found + break_len(bytes, at + found);
            starts.push(at as u32);
        }
        Self { raw, base, starts }
    }

    pub fn raw(&self) -> &str {
        &self.raw
    }

    /// Lines in the text; a final line break does not start another one.
    pub fn line_count(&self) -> usize {
        self.starts.len() - usize::from(self.starts.last().is_some_and(|&s| s as usize == self.raw.len()))
    }

    /// The 0-based line that holds byte `offset`.
    pub fn line_of(&self, offset: usize) -> usize {
        self.starts.partition_point(|&s| s as usize <= offset).saturating_sub(1)
    }

    pub fn line_start(&self, line: usize) -> usize {
        self.starts.get(line).map_or(self.raw.len(), |&s| s as usize)
    }

    /// End of `line`, its line break included.
    pub fn line_end(&self, line: usize) -> usize {
        self.starts.get(line + 1).map_or(self.raw.len(), |&s| s as usize)
    }

    /// The line break that ends `line`, if it has one.
    pub fn line_break(&self, line: usize) -> Option<&'static str> {
        let end = *self.starts.get(line + 1)? as usize;
        Some(break_before(self.raw.as_bytes(), end))
    }

    /// The most common line break; `\n` when there is none. Ties go to `\n`, then `\r\n`.
    pub fn usual_break(&self) -> &'static str {
        let mut counts = [("\n", 0usize), ("\r\n", 0), ("\r", 0)];
        for &end in &self.starts[1..] {
            let brk = break_before(self.raw.as_bytes(), end as usize);
            if let Some(entry) = counts.iter_mut().find(|(b, _)| *b == brk) {
                entry.1 += 1;
            }
        }
        // `max_by_key` keeps the last of equal maxima, so look from the least preferred.
        counts.iter().rev().max_by_key(|(_, n)| *n).map_or("\n", |(b, _)| b)
    }

    /// `line` with its line break written as `\n`.
    pub fn line_lf(&self, line: usize) -> Cow<'_, str> {
        let text = &self.raw[self.line_start(line)..self.line_end(line)];
        match self.line_break(line) {
            Some("\n") | None => Cow::Borrowed(text),
            Some(brk) => Cow::Owned(format!("{}\n", &text[..text.len() - brk.len()])),
        }
    }
}

/// Length of the line break at `at`: 2 for `\r\n`, else 1.
fn break_len(bytes: &[u8], at: usize) -> usize {
    if bytes[at] == b'\r' && bytes.get(at + 1) == Some(&b'\n') { 2 } else { 1 }
}

/// The line break that ends right before `end`.
fn break_before(bytes: &[u8], end: usize) -> &'static str {
    match bytes[end - 1] {
        b'\r' => "\r",
        _ if end >= 2 && bytes[end - 2] == b'\r' => "\r\n",
        _ => "\n",
    }
}

/// `text` with `\r\n` and `\r` written as `\n`.
pub fn lf(text: &str) -> Cow<'_, str> {
    if memchr(b'\r', text.as_bytes()).is_none() {
        return Cow::Borrowed(text);
    }
    Cow::Owned(text.replace("\r\n", "\n").replace('\r', "\n"))
}

/// Whether `needle` has a complete line: one with a line break before and after it.
pub fn has_complete_line(needle: &str) -> bool {
    memchr::memchr_iter(b'\n', lf(needle).as_bytes()).nth(1).is_some()
}

/// `text` with every line break written as `brk`.
pub fn with_breaks(text: &str, brk: &str) -> String {
    let text = lf(text);
    if brk == "\n" { text.into_owned() } else { text.replace('\n', brk) }
}

/// The ASCII character a typographic quote, dash or space folds to.
fn fold_char(c: char) -> Option<u8> {
    match c {
        '\u{2018}' | '\u{2019}' | '\u{201A}' | '\u{201B}' => Some(b'\''),
        '\u{201C}' | '\u{201D}' | '\u{201E}' | '\u{201F}' => Some(b'"'),
        '\u{2010}'..='\u{2015}' | '\u{2212}' => Some(b'-'),
        '\u{00A0}' | '\u{2002}'..='\u{200A}' | '\u{202F}' | '\u{205F}' | '\u{3000}' => Some(b' '),
        _ => None,
    }
}

/// What a view leaves out of the text, besides writing every line break as `\n`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Shape {
    /// Nothing.
    Lf,
    /// Whitespace at the end of each line.
    TrimEnd,
    /// Whitespace at the end of each line; typographic quotes, dashes and spaces become ASCII.
    Fold,
    /// Whitespace at the start and at the end of each line.
    Trim,
}

/// A normalized copy of the text after the byte order mark.
pub struct View<'a> {
    raw: &'a str,
    text: Cow<'a, str>,
    /// `(view offset, original offset)` of every view byte that does not simply follow the
    /// original bytes of the byte before it: one that stands for `\r\n` or for a folded
    /// character, or one that comes after left-out whitespace. Sorted.
    jumps: Vec<(u32, u32)>,
    base: usize,
    fold: bool,
    /// Characters folded to ASCII.
    pub folded: usize,
    /// Start offsets of the lines followed by a line break, by a hash of their content.
    lines: Option<HashMap<u64, Vec<u32>>>,
}

fn line_hash(line: &str) -> u64 {
    BuildHasherDefault::<DefaultHasher>::default().hash_one(line)
}

impl<'a> View<'a> {
    pub fn new(text: &'a Text, shape: Shape) -> Self {
        let raw = text.raw.as_str();
        let body = &raw[text.base..];
        if shape == Shape::Lf && memchr(b'\r', body.as_bytes()).is_none() {
            return Self {
                raw,
                text: Cow::Borrowed(body),
                jumps: Vec::new(),
                base: text.base,
                fold: false,
                folded: 0,
                lines: None,
            };
        }
        let mut builder = Builder {
            raw,
            out: String::with_capacity(body.len()),
            jumps: Vec::new(),
            next: text.base,
            folded: 0,
        };
        for line in 0..text.line_count() {
            let end = text.line_end(line);
            let brk = text.line_break(line).map_or(0, str::len);
            builder.content(text.line_start(line), end - brk, shape);
            if brk > 0 {
                builder.unit(b'\n', end - brk, end);
            }
        }
        Self {
            raw,
            text: Cow::Owned(builder.out),
            jumps: builder.jumps,
            base: text.base,
            fold: shape == Shape::Fold,
            folded: builder.folded,
            lines: None,
        }
    }

    /// A view with its lines indexed when `indexed`, for [`View::find`].
    fn make(text: &'a Text, shape: Shape, indexed: bool) -> Self {
        let mut view = Self::new(text, shape);
        if indexed {
            let mut lines: HashMap<u64, Vec<u32>> = HashMap::new();
            let mut start = 0;
            for end in memchr::memchr_iter(b'\n', view.text.as_bytes()) {
                lines.entry(line_hash(&view.text[start..end])).or_default().push(start as u32);
                start = end + 1;
            }
            view.lines = Some(lines);
        }
        view
    }

    #[cfg(test)]
    pub fn as_str(&self) -> &str {
        &self.text
    }

    /// Original offset of view byte `p`.
    pub fn start(&self, p: usize) -> usize {
        match self.jump_at_or_before(p) {
            None => self.base + p,
            Some((v, o)) if v == p => o,
            Some((v, o)) => o + self.width(o) + (p - v - 1),
        }
    }

    /// Original offset right after view byte `e - 1`.
    pub fn end(&self, e: usize) -> usize {
        match self.jump_at_or_before(e - 1) {
            None => self.base + e,
            Some((v, o)) => o + self.width(o) + (e - 1 - v),
        }
    }

    fn jump_at_or_before(&self, p: usize) -> Option<(usize, usize)> {
        let i = self.jumps.partition_point(|&(v, _)| v as usize <= p);
        let (v, o) = *self.jumps.get(i.checked_sub(1)?)?;
        Some((v as usize, o as usize))
    }

    /// Original bytes behind the view byte of a jump at original offset `o`. Whitespace is
    /// only left out before a line break or before the first byte of a line's content, so a
    /// jump is a line break, a folded character or the first byte of copied text.
    fn width(&self, o: usize) -> usize {
        let bytes = self.raw.as_bytes();
        match bytes[o] {
            b'\r' | b'\n' => break_len(bytes, o),
            _ if self.fold => self.raw[o..]
                .chars()
                .next()
                .filter(|c| fold_char(*c).is_some())
                .map_or(1, char::len_utf8),
            _ => 1,
        }
    }

    /// Original span of the match `start..end` of `needle`. A loose needle may take in the
    /// whitespace at the line ends it touches (see [`Loose`]).
    pub fn span(&self, start: usize, end: usize, loose: Option<&Loose>) -> (usize, usize) {
        let from = match loose {
            Some(l) if l.from_line_end && start == 0 => self.base,
            Some(l) if l.from_line_end => self.end(start),
            _ => self.start(start),
        };
        let to = match loose {
            Some(l) if l.to_line_end && end == self.text.len() => self.raw.len(),
            Some(l) if l.to_line_end => self.start(end),
            _ => self.end(end),
        };
        (from, to)
    }

    /// Occurrences of `needle`, overlapping ones included, counted up to `limit + 1`. With
    /// `at_line_end`, only occurrences followed by a line break or the end of the text count.
    pub fn find(&self, needle: &str, at_line_end: bool, limit: usize) -> Found {
        if let Some(lines) = &self.lines
            && let Some(found) = self.find_by_line(lines, needle, at_line_end, limit)
        {
            return found;
        }
        let mut found = Found::default();
        if needle.is_empty() {
            return found;
        }
        let pattern = if at_line_end { Cow::Owned(format!("{needle}\n")) } else { Cow::Borrowed(needle) };
        let finder = memmem::Finder::new(pattern.as_bytes());
        let hay = self.text.as_bytes();
        let mut from = 0;
        while let Some(i) = finder.find(&hay[from..]) {
            let start = from + i;
            found.add(start, start + needle.len());
            if found.count > limit {
                return found;
            }
            from = start + 1;
        }
        if at_line_end && self.text.ends_with(needle) {
            found.add(hay.len() - needle.len(), hay.len());
        }
        found
    }

    /// [`View::find`] from the occurrences of the rarest complete line of `needle` (one with a
    /// line break before and after it). Every occurrence of the needle has that line as a whole
    /// line of the view, so none is missed. `None` when the needle has no complete line or its
    /// rarest one is too common to help.
    fn find_by_line(
        &self,
        lines: &HashMap<u64, Vec<u32>>,
        needle: &str,
        at_line_end: bool,
        limit: usize,
    ) -> Option<Found> {
        let segments: Vec<&str> = needle.split('\n').collect();
        let mut best: Option<(&[u32], usize)> = None;
        let mut offset = segments[0].len() + 1;
        for line in segments.iter().take(segments.len().saturating_sub(1)).skip(1) {
            let starts = lines.get(&line_hash(line)).map_or(&[][..], Vec::as_slice);
            if best.is_none_or(|(fewest, _)| starts.len() < fewest.len()) {
                best = Some((starts, offset));
            }
            offset += line.len() + 1;
        }
        let (starts, offset) = best?;
        if starts.len() > MAX_CANDIDATES {
            return None;
        }
        let hay = self.text.as_bytes();
        let mut found = Found::default();
        for &line_start in starts {
            let Some(start) = (line_start as usize).checked_sub(offset) else {
                continue;
            };
            let end = start + needle.len();
            if hay.get(start..end) != Some(needle.as_bytes()) {
                continue;
            }
            if at_line_end && hay.get(end).is_some_and(|b| *b != b'\n') {
                continue;
            }
            found.add(start, end);
            if found.count > limit {
                break;
            }
        }
        Some(found)
    }

    /// Occurrences of `block` that start at a line start and end at a line end, counted up to
    /// `limit + 1`.
    pub fn find_lines(&self, block: &str, limit: usize) -> Found {
        let mut found = Found::default();
        let text: &str = &self.text;
        let hay = text.as_bytes();
        let len = block.len();
        if len == 0 {
            return found;
        }
        if text.starts_with(block) && hay.get(len).is_none_or(|b| *b == b'\n') {
            found.add(0, len);
        }
        let pattern = format!("\n{block}\n");
        let finder = memmem::Finder::new(pattern.as_bytes());
        let mut from = 0;
        while let Some(i) = finder.find(&hay[from..]) {
            let start = from + i + 1;
            found.add(start, start + len);
            if found.count > limit {
                return found;
            }
            from = start;
        }
        if hay.len() > len && text.ends_with(block) && hay[hay.len() - len - 1] == b'\n' {
            found.add(hay.len() - len, hay.len());
        }
        found
    }
}

/// Builds the text and jumps of a view.
struct Builder<'a> {
    raw: &'a str,
    out: String,
    jumps: Vec<(u32, u32)>,
    /// Original offset of the next view byte when nothing is left out before it.
    next: usize,
    folded: usize,
}

impl Builder<'_> {
    /// Adds the content of a line, `raw[start..end]`, without its line break.
    fn content(&mut self, start: usize, end: usize, shape: Shape) {
        let from = match shape {
            Shape::Trim => end - self.raw[start..end].trim_start().len(),
            _ => start,
        };
        let to = match shape {
            Shape::Lf => end,
            _ => from + self.raw[from..end].trim_end().len(),
        };
        if shape == Shape::Fold { self.fold(from, to) } else { self.copy(from, to) }
    }

    /// Copies `raw[from..to]` as it is.
    fn copy(&mut self, from: usize, to: usize) {
        if from == to {
            return;
        }
        if from != self.next {
            self.jumps.push((self.out.len() as u32, from as u32));
        }
        self.out.push_str(&self.raw[from..to]);
        self.next = to;
    }

    /// Adds one ASCII byte that stands for `raw[from..to]`.
    fn unit(&mut self, byte: u8, from: usize, to: usize) {
        if from != self.next || to - from != 1 {
            self.jumps.push((self.out.len() as u32, from as u32));
        }
        self.out.push(char::from(byte));
        self.next = to;
    }

    /// Copies `raw[from..to]` with typographic characters folded to ASCII.
    fn fold(&mut self, from: usize, to: usize) {
        let bytes = self.raw.as_bytes();
        let mut copied = from;
        let mut at = from;
        // Every character that folds starts with one of these bytes.
        while let Some(found) = memchr3(0xC2, 0xE2, 0xE3, &bytes[at..to]) {
            let pos = at + found;
            let Some(c) = self.raw[pos..].chars().next() else { break };
            at = pos + c.len_utf8();
            if let Some(ascii) = fold_char(c) {
                self.copy(copied, pos);
                self.unit(ascii, pos, at);
                self.folded += 1;
                copied = at;
            }
        }
        self.copy(copied, to);
    }
}

/// Occurrences of a needle in a view.
#[derive(Debug, Default)]
pub struct Found {
    pub count: usize,
    /// View ranges of the first [`KEEP`] occurrences.
    pub spans: Vec<(usize, usize)>,
}

impl Found {
    fn add(&mut self, start: usize, end: usize) {
        self.count += 1;
        if self.spans.len() < KEEP {
            self.spans.push((start, end));
        }
    }
}

/// old_text prepared for a loose level: no whitespace at the end of each line, and folded
/// when the level folds.
#[derive(Debug, PartialEq, Eq)]
pub struct Loose {
    pub text: String,
    /// The first line held only whitespace (the end of the line before): the span reaches
    /// back over the whitespace that ends that line in the file.
    pub from_line_end: bool,
    /// The last line lost trailing whitespace: the match must end at a line end, and the span
    /// takes in the whitespace there.
    pub to_line_end: bool,
}

/// `needle` (line breaks as `\n`) for a loose level. A last line of only whitespace is kept:
/// it is the indentation of the line that follows. `None` when only whitespace is left.
pub fn loosen(needle: &str, fold: bool) -> Option<Loose> {
    let mut text = String::with_capacity(needle.len());
    let mut from_line_end = false;
    let mut to_line_end = false;
    let last = memchr::memchr_iter(b'\n', needle.as_bytes()).count();
    for (i, line) in needle.split('\n').enumerate() {
        if i > 0 {
            text.push('\n');
        }
        let trimmed = line.trim_end();
        let kept = if i < last {
            from_line_end |= i == 0 && trimmed.is_empty() && !line.is_empty();
            trimmed
        } else if trimmed.is_empty() {
            line
        } else {
            to_line_end = trimmed.len() < line.len();
            trimmed
        };
        if fold {
            text.extend(kept.chars().map(|c| fold_char(c).map_or(c, char::from)));
        } else {
            text.push_str(kept);
        }
    }
    text.contains(|c: char| !c.is_whitespace()).then_some(Loose {
        text,
        from_line_end,
        to_line_end,
    })
}

/// Result of looking for one old_text.
#[derive(Debug, PartialEq, Eq)]
pub enum Outcome {
    /// Found once, at original bytes `start..end`.
    Unique { level: Level, start: usize, end: usize },
    /// Found `count` times (counted up to [`MAX_COUNT`] + 1) at `level`, starting on these
    /// 1-based lines (the first [`KEEP`] occurrences, repeats removed).
    Several { level: Level, count: usize, lines: Vec<usize> },
    Missing,
}

/// Why an old_text was not found.
#[derive(Debug, PartialEq, Eq)]
pub enum Diagnosis {
    /// Its lines match whole lines of the file when whitespace at the start and end of every
    /// line is ignored: 0-based line ranges (at most [`PLACES`]); `more` when there are more.
    Indentation { ranges: Vec<(usize, usize)>, more: bool },
    /// It has several lines, and its first non-blank line, trimmed, occurs on these 1-based
    /// lines.
    FirstLine(Vec<usize>),
    Unknown,
}

/// Looks for old_texts in one text. Loose views are built the first time they are needed and
/// shared by all old_texts.
pub struct Matcher<'a> {
    text: &'a Text,
    /// Whether views get a line index.
    indexed: bool,
    lf: View<'a>,
    trim_end: Option<View<'a>>,
    fold: Option<View<'a>>,
    ascii: Option<bool>,
}

impl<'a> Matcher<'a> {
    /// A matcher for old_texts of which `multiline` have a complete line.
    pub fn new(text: &'a Text, multiline: usize) -> Self {
        let indexed = multiline >= INDEX_FROM;
        Self {
            text,
            indexed,
            lf: View::make(text, Shape::Lf, indexed),
            trim_end: None,
            fold: None,
            ascii: None,
        }
    }

    /// Finds `needle` (line breaks as `\n`) at the first level where it occurs.
    pub fn find(&mut self, needle: &str) -> Outcome {
        let (text, indexed) = (self.text, self.indexed);
        let found = self.lf.find(needle, false, MAX_COUNT);
        if found.count > 0 {
            return outcome(text, &self.lf, Level::Exact, &found, None);
        }
        let Some(loose) = loosen(needle, false) else {
            return Outcome::Missing;
        };
        let view = self.trim_end.get_or_insert_with(|| View::make(text, Shape::TrimEnd, indexed));
        let found = view.find(&loose.text, loose.to_line_end, MAX_COUNT);
        if found.count > 0 {
            return outcome(text, view, Level::TrailingWhitespace, &found, Some(&loose));
        }
        let Some(folded) = loosen(needle, true) else {
            return Outcome::Missing;
        };
        // Folding changes nothing when neither side has a character to fold.
        let needle_folds = folded.text != loose.text;
        if !needle_folds && *self.ascii.get_or_insert_with(|| text.raw().is_ascii()) {
            return Outcome::Missing;
        }
        let view = self.fold.get_or_insert_with(|| View::make(text, Shape::Fold, indexed));
        if !needle_folds && view.folded == 0 {
            return Outcome::Missing;
        }
        let found = view.find(&folded.text, folded.to_line_end, MAX_COUNT);
        if found.count > 0 {
            return outcome(text, view, Level::Typography, &found, Some(&folded));
        }
        Outcome::Missing
    }

    /// Looks for what `needle` probably meant, after [`Matcher::find`] found nothing.
    pub fn diagnose(&self, needle: &str) -> Diagnosis {
        let lines: Vec<&str> = needle.split('\n').collect();
        let Some(first) = lines.iter().position(|l| !l.trim().is_empty()) else {
            return Diagnosis::Unknown;
        };
        let last = lines.iter().rposition(|l| !l.trim().is_empty()).unwrap_or(first);
        let block: Vec<&str> = lines[first..=last].iter().map(|l| l.trim()).collect();
        let view = View::new(self.text, Shape::Trim);
        let found = view.find_lines(&block.join("\n"), PLACES);
        if found.count > 0 {
            let mut ranges: Vec<(usize, usize)> = found
                .spans
                .iter()
                .map(|&(s, e)| (self.text.line_of(view.start(s)), self.text.line_of(view.end(e) - 1)))
                .collect();
            ranges.dedup();
            ranges.truncate(PLACES);
            return Diagnosis::Indentation {
                ranges,
                more: found.count > PLACES,
            };
        }
        if last == first {
            return Diagnosis::Unknown;
        }
        let found = self.lf.find(lines[first].trim(), false, PLACES);
        if (1..=PLACES).contains(&found.count) {
            let mut at: Vec<usize> = found
                .spans
                .iter()
                .map(|&(s, _)| self.text.line_of(self.lf.start(s)) + 1)
                .collect();
            at.dedup();
            return Diagnosis::FirstLine(at);
        }
        Diagnosis::Unknown
    }
}

fn outcome(text: &Text, view: &View, level: Level, found: &Found, loose: Option<&Loose>) -> Outcome {
    if found.count == 1 {
        let (start, end) = view.span(found.spans[0].0, found.spans[0].1, loose);
        return Outcome::Unique { level, start, end };
    }
    let mut lines: Vec<usize> = found
        .spans
        .iter()
        .map(|&(s, _)| text.line_of(view.start(s)) + 1)
        .collect();
    lines.dedup();
    Outcome::Several {
        level,
        count: found.count,
        lines,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn text(raw: &str) -> Text {
        Text::new(raw.to_string())
    }

    /// The original bytes a view match covers.
    fn covered<'t>(t: &'t Text, view: &View, needle: &str) -> &'t str {
        let found = view.find(needle, false, MAX_COUNT);
        assert_eq!(found.count, 1, "{needle:?} in {:?}", view.as_str());
        let (s, e) = found.spans[0];
        &t.raw()[view.start(s)..view.end(e)]
    }

    #[test]
    fn indexes_lines_with_every_kind_of_break() {
        let t = text("\u{feff}a\r\nb\rc\nd");
        assert_eq!(t.line_count(), 4);
        assert_eq!((t.line_start(0), t.line_end(0)), (3, 6));
        assert_eq!(t.line_of(3), 0);
        assert_eq!(t.line_of(5), 0, "the \\n of a \\r\\n belongs to its line");
        assert_eq!(t.line_of(6), 1);
        assert_eq!(t.line_of(t.raw().len() - 1), 3);
        assert_eq!(
            (0..4).map(|l| t.line_break(l)).collect::<Vec<_>>(),
            [Some("\r\n"), Some("\r"), Some("\n"), None]
        );
        assert_eq!(t.line_lf(0), "a\n");
        assert_eq!(t.line_lf(1), "b\n");
        assert_eq!(t.line_lf(3), "d");
        assert_eq!(text("a\n").line_count(), 1);
        assert_eq!(text("").line_count(), 0);
        assert_eq!(text("\u{feff}").line_count(), 0);
        assert_eq!(text("a\r\nb\r\nc\n").usual_break(), "\r\n");
        assert_eq!(text("a\r\nb\n").usual_break(), "\n", "a tie goes to \\n");
        assert_eq!(text("a\rb\r\n").usual_break(), "\r\n", "then to \\r\\n");
        assert_eq!(text("abc").usual_break(), "\n");
    }

    #[test]
    fn views_map_back_to_original_bytes() {
        let t = text("\u{feff}one \r\ntwo\t\rthree\n  four  ");
        let lf = View::new(&t, Shape::Lf);
        assert_eq!(lf.as_str(), "one \ntwo\t\nthree\n  four  ");
        assert_eq!(covered(&t, &lf, " \ntwo"), " \r\ntwo");
        assert_eq!(covered(&t, &lf, "\t\nthree\n"), "\t\rthree\n");
        assert_eq!(covered(&t, &lf, "four  "), "four  ");

        let trim_end = View::new(&t, Shape::TrimEnd);
        assert_eq!(trim_end.as_str(), "one\ntwo\nthree\n  four");
        // Left-out whitespace inside a match is covered; at its edges it is not.
        assert_eq!(covered(&t, &trim_end, "one\ntwo"), "one \r\ntwo");
        assert_eq!(covered(&t, &trim_end, "two"), "two");
        assert_eq!(covered(&t, &trim_end, "\nthree"), "\rthree");
        assert_eq!(covered(&t, &trim_end, "  four"), "  four");

        let trim = View::new(&t, Shape::Trim);
        assert_eq!(trim.as_str(), "one\ntwo\nthree\nfour");
        assert_eq!(covered(&t, &trim, "three\nfour"), "three\n  four");
        assert_eq!(covered(&t, &trim, "four"), "four");
    }

    #[test]
    fn folding_maps_multibyte_characters_to_their_whole_bytes() {
        let t = text("say “hi”\u{a0}—\u{3000}ok  \r\nnext ‘x’");
        let view = View::new(&t, Shape::Fold);
        assert_eq!(view.as_str(), "say \"hi\" - ok\nnext 'x'");
        assert_eq!(view.folded, 7);
        assert_eq!(covered(&t, &view, "\"hi\""), "“hi”");
        assert_eq!(covered(&t, &view, "hi\" -"), "hi”\u{a0}—");
        assert_eq!(covered(&t, &view, "ok\nnext '"), "ok  \r\nnext ‘");
        assert_eq!(covered(&t, &view, "x'"), "x’");
        // Multibyte characters that do not fold are copied byte for byte.
        let t = text("  é—ü\n");
        let view = View::new(&t, Shape::Fold);
        assert_eq!(covered(&t, &view, "é-ü"), "é—ü");
        let trim = View::new(&t, Shape::Trim);
        assert_eq!(covered(&t, &trim, "é—ü"), "é—ü");
    }

    #[test]
    fn finds_overlapping_occurrences_up_to_a_limit() {
        let t = text("aaaa\nab\n");
        let view = View::new(&t, Shape::Lf);
        let found = view.find("aa", false, MAX_COUNT);
        assert_eq!(found.count, 3);
        assert_eq!(found.spans, [(0, 2), (1, 3), (2, 4)]);
        assert_eq!(view.find("a", false, 2).count, 3, "stops one past the limit");
        // At a line end: followed by a break or the end of the text.
        assert_eq!(view.find("a", true, MAX_COUNT).spans, [(3, 4)]);
        let t = text("ab\nab");
        assert_eq!(View::new(&t, Shape::Lf).find("ab", true, MAX_COUNT).spans, [(0, 2), (3, 5)]);
    }

    #[test]
    fn loosening_trims_line_ends_and_keeps_indentation() {
        let loose = loosen("a  \n  b\t\n    ", false).unwrap();
        assert_eq!(loose.text, "a\n  b\n    ");
        assert!(!loose.from_line_end && !loose.to_line_end);
        let loose = loosen("  \n  x = 1  ", false).unwrap();
        assert_eq!(loose.text, "\n  x = 1");
        assert!(loose.from_line_end && loose.to_line_end);
        assert_eq!(loosen("“a”\u{a0}–\u{a0}", true).unwrap().text, "\"a\" -");
        assert_eq!(loosen("  \n\t", false), None);
        assert_eq!(lf("a\r\nb\rc"), "a\nb\nc");
        assert_eq!(with_breaks("a\r\nb\nc", "\r\n"), "a\r\nb\r\nc");
    }

    #[test]
    fn the_ladder_stops_at_the_first_level_with_a_match() {
        let t = text("x = 1;  \ny = “2”;\nx = 1;\nend\n");
        for count in [1, INDEX_FROM] {
            let mut m = Matcher::new(&t, count);
            // Line 3 matches exactly, so line 1, which would match loosely, does not count.
            assert_eq!(m.find("x = 1;\n"), Outcome::Unique {
                level: Level::Exact,
                start: 22,
                end: 29,
            });
            assert_eq!(m.find("x = 1;\ny"), Outcome::Unique {
                level: Level::TrailingWhitespace,
                start: 0,
                end: 10,
            });
            // Several matches at a loose level are an answer: the ladder stops there.
            assert_eq!(m.find("x = 1;\t"), Outcome::Several {
                level: Level::TrailingWhitespace,
                count: 2,
                lines: vec![1, 3],
            });
            assert_eq!(m.find("y = \"2\";"), Outcome::Unique {
                level: Level::Typography,
                start: 9,
                end: 21,
            });
            // Found from a complete line when the views are indexed.
            assert_eq!(m.find("x = 1; \ny = \"2\";\nx = 1;\nend"), Outcome::Unique {
                level: Level::Typography,
                start: 0,
                end: 32,
            });
            assert_eq!(m.find("z"), Outcome::Missing);
        }
    }

    #[test]
    fn the_line_index_finds_what_a_scan_finds() {
        // Short lines from a small alphabet, so that needles repeat, overlap and cross lines.
        let mut seed = 7u64;
        let mut next = |n: u64| {
            seed = seed.wrapping_mul(6364136223846793005).wrapping_add(1442695040888963407);
            (seed >> 33) % n
        };
        let mut raw = String::new();
        for _ in 0..400 {
            let len = next(4);
            raw.extend((0..len).map(|_| ['a', 'b', ' '][next(3) as usize]));
            raw.push_str(["\n", "\r\n", "  \n"][next(3) as usize]);
        }
        let t = text(&raw);
        for shape in [Shape::Lf, Shape::TrimEnd] {
            let plain = View::make(&t, shape, false);
            let indexed = View::make(&t, shape, true);
            let hay = plain.text.as_ref();
            for _ in 0..2000 {
                let start = next(hay.len() as u64 - 12) as usize;
                let needle = &hay[start..start + 1 + next(12) as usize];
                for at_line_end in [false, true] {
                    let a = plain.find(needle, at_line_end, 50);
                    let b = indexed.find(needle, at_line_end, 50);
                    assert_eq!((a.count, &a.spans), (b.count, &b.spans), "{needle:?} {at_line_end}");
                }
            }
        }
    }
}
