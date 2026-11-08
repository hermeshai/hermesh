//! Text files as lines: line breaks, decoding and the window of lines a read returns. The
//! scan streams a buffer at a time, so memory stays bounded by the window, not by the file.

use std::io;
use std::io::Read;
use std::io::Seek;
use std::io::SeekFrom;

use crate::env::FileContent;
use crate::env::LineWindow;
use crate::env::TextEncoding;
use crate::env::TextWindow;
use crate::files::FileStamp;
use crate::truncate::truncate_line_start;

/// A NUL byte among the first this many bytes makes a file binary.
pub const SNIFF_BYTES: usize = 8 * 1024;
/// Text decoded as a whole (UTF-16, or any text where it cannot be streamed) is read up to
/// this size.
pub const MAX_IN_MEMORY: u64 = 16 * 1024 * 1024;
/// Bytes read at a time, at most.
const BUFFER: usize = 1024 * 1024;
/// Bytes read at a time when a read starts from a [`LineIndex`]: it needs little more than the
/// window.
const RESUMED_BUFFER: usize = 128 * 1024;
const UTF8_BOM: &[u8] = b"\xEF\xBB\xBF";

/// Whether a file that starts with `head` is binary: a NUL byte among its first
/// [`SNIFF_BYTES`], unless a UTF-16 byte-order mark makes it text.
pub fn is_binary(head: &[u8]) -> bool {
    utf16_bom(head).is_none() && memchr::memchr(0, &head[..head.len().min(SNIFF_BYTES)]).is_some()
}

/// The UTF-16 encoding named by a byte-order mark at the start of `bytes`.
pub fn utf16_bom(bytes: &[u8]) -> Option<TextEncoding> {
    match bytes {
        // The UTF-32 little-endian mark starts like the UTF-16 one.
        [0xFF, 0xFE, 0, 0, ..] => None,
        [0xFF, 0xFE, ..] => Some(TextEncoding::Utf16Le),
        [0xFE, 0xFF, ..] => Some(TextEncoding::Utf16Be),
        _ => None,
    }
}

/// UTF-16 text that follows a byte-order mark, as UTF-8. Unpaired surrogates and an odd last
/// byte become U+FFFD.
pub fn decode_utf16(bytes: &[u8], encoding: TextEncoding) -> String {
    let (pairs, odd) = bytes.as_chunks::<2>();
    let units = pairs.iter().map(|pair| match encoding {
        TextEncoding::Utf16Be => u16::from_be_bytes(*pair),
        _ => u16::from_le_bytes(*pair),
    });
    let mut text: String = char::decode_utf16(units).map(|c| c.unwrap_or(char::REPLACEMENT_CHARACTER)).collect();
    if !odd.is_empty() {
        text.push(char::REPLACEMENT_CHARACTER);
    }
    text
}

/// UTF-16 text that follows a byte-order mark, as UTF-8, or `None` when it would not come back
/// byte for byte: an unpaired surrogate or an odd last byte.
pub fn decode_utf16_exact(bytes: &[u8], encoding: TextEncoding) -> Option<String> {
    let (pairs, odd) = bytes.as_chunks::<2>();
    if !odd.is_empty() {
        return None;
    }
    let units = pairs.iter().map(|pair| match encoding {
        TextEncoding::Utf16Be => u16::from_be_bytes(*pair),
        _ => u16::from_le_bytes(*pair),
    });
    char::decode_utf16(units).collect::<Result<String, _>>().ok()
}

/// `text` as the bytes of a file in `encoding`; UTF-16 starts with its byte-order mark.
pub fn encode(text: String, encoding: TextEncoding) -> Vec<u8> {
    let units = text.encode_utf16();
    match encoding {
        TextEncoding::Utf8 => text.into_bytes(),
        TextEncoding::Utf16Le => [0xFF, 0xFE].into_iter().chain(units.flat_map(u16::to_le_bytes)).collect(),
        TextEncoding::Utf16Be => [0xFE, 0xFF].into_iter().chain(units.flat_map(u16::to_be_bytes)).collect(),
    }
}

fn not_text() -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, "not a text file (NUL bytes)")
}

/// The window of a file held in memory. `file.bytes` is the whole file unless
/// `file.truncated`; then the lines past them are unknown and the total is a lower bound.
pub fn window_in_memory(file: &FileContent, window: &LineWindow) -> io::Result<TextWindow> {
    let complete = !file.truncated;
    let (lines, encoding) = match utf16_bom(&file.bytes) {
        Some(encoding) => {
            let text = decode_utf16(&file.bytes[2..], encoding);
            (scan(text.as_bytes(), window, complete), encoding)
        }
        None if is_binary(&file.bytes) => return Err(not_text()),
        None => {
            let body = file.bytes.strip_prefix(UTF8_BOM).unwrap_or(&file.bytes);
            (scan(body, window, complete), TextEncoding::Utf8)
        }
    };
    Ok(lines.into_window(encoding, file.total_size, file.stamp))
}

fn scan(text: &[u8], window: &LineWindow, complete: bool) -> Scanned {
    let mut scanner = LineScanner::new(window);
    scanner.feed(text);
    scanner.finish(complete)
}

/// Streams the window out of `file`, a whole file of `total_size` bytes. Lines before the
/// window are passed over at memchr speed. After the window, lines (and the length of a cut
/// line) are counted through at most `count_limit` more bytes; past them the totals are lower
/// bounds. UTF-16 is decoded in memory, up to [`MAX_IN_MEMORY`].
pub fn read_window(
    file: &mut impl Read,
    total_size: u64,
    stamp: FileStamp,
    window: &LineWindow,
    count_limit: u64,
) -> io::Result<TextWindow> {
    scan_file(file, total_size, stamp, window, count_limit).map(|(window, _)| window)
}

/// [`read_window`] with an index of the file. An index of this version (the same stamp) makes
/// the read start near the window and stop after it, with the total taken from the index; a
/// scan that reaches the end of a UTF-8 file larger than one buffer returns a new index.
pub fn read_window_indexed(
    file: &mut (impl Read + Seek),
    total_size: u64,
    stamp: FileStamp,
    window: &LineWindow,
    count_limit: u64,
    index: Option<&LineIndex>,
) -> io::Result<(TextWindow, Option<LineIndex>)> {
    match index {
        Some(index) if index.stamp == stamp => Ok((index.read(file, total_size, window)?, None)),
        _ => scan_file(file, total_size, stamp, window, count_limit),
    }
}

/// Where a scan of a file can start: the beginning of a buffer, and the state of the scan there.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct Checkpoint {
    byte: u64,
    /// Lines ended before it.
    ended: u64,
    /// It falls inside a line.
    in_line: bool,
    /// The byte before it is a `\r`, whose `\n` may follow.
    after_cr: bool,
}

/// The line count of one version of a UTF-8 file and a checkpoint at the start of every buffer
/// a complete scan read. Paging through a large file then costs each page about its window,
/// instead of passing over every line before it and counting every line after it.
#[derive(Clone, Debug)]
pub struct LineIndex {
    stamp: FileStamp,
    total_lines: u64,
    checkpoints: Vec<Checkpoint>,
}

impl LineIndex {
    /// The version of the file it describes.
    pub fn stamp(&self) -> FileStamp {
        self.stamp
    }

    fn read(&self, file: &mut (impl Read + Seek), total_size: u64, window: &LineWindow) -> io::Result<TextWindow> {
        // The last checkpoint before the first line of the window, or at its start.
        let offset = window.offset.max(1);
        let start = self
            .checkpoints
            .iter()
            .rev()
            .find(|c| c.ended + 1 < offset || (c.ended + 1 == offset && !c.in_line))
            .copied()
            .unwrap_or(self.checkpoints[0]);
        file.seek(SeekFrom::Start(start.byte))?;
        let mut scanner = LineScanner::resume(window, start);
        let mut buf = vec![0u8; RESUMED_BUFFER];
        let mut complete = false;
        while scanner.phase != Phase::Count {
            let n = fill(file, &mut buf)?;
            scanner.feed(&buf[..n]);
            if n < buf.len() {
                complete = true;
                break;
            }
        }
        let scanned = scanner.finish(complete);
        Ok(TextWindow {
            lines: scanned.lines,
            total_lines: self.total_lines,
            total_exact: true,
            byte_limited: scanned.byte_limited,
            encoding: TextEncoding::Utf8,
            total_size,
            stamp: self.stamp,
        })
    }
}

fn scan_file(
    file: &mut impl Read,
    total_size: u64,
    stamp: FileStamp,
    window: &LineWindow,
    count_limit: u64,
) -> io::Result<(TextWindow, Option<LineIndex>)> {
    let mut buf = vec![0u8; total_size.saturating_add(1).clamp(SNIFF_BYTES as u64, BUFFER as u64) as usize];
    let n = fill(file, &mut buf)?;
    if utf16_bom(&buf[..n]).is_some() {
        buf.truncate(n);
        file.take(MAX_IN_MEMORY + 1 - n as u64).read_to_end(&mut buf)?;
        let truncated = buf.len() as u64 > MAX_IN_MEMORY;
        buf.truncate(MAX_IN_MEMORY as usize);
        let content = FileContent {
            bytes: buf,
            truncated,
            total_size,
            stamp,
        };
        return Ok((window_in_memory(&content, window)?, None));
    }
    if is_binary(&buf[..n]) {
        return Err(not_text());
    }
    let bom = if buf[..n].starts_with(UTF8_BOM) { UTF8_BOM.len() } else { 0 };
    let mut scanner = LineScanner::new(window);
    let mut checkpoints = vec![scanner.checkpoint(bom as u64)];
    scanner.feed(&buf[bom..n]);
    let mut read = n as u64;
    let mut complete = n < buf.len();
    while !complete && scanner.counted < count_limit {
        checkpoints.push(scanner.checkpoint(read));
        let n = fill(file, &mut buf)?;
        scanner.feed(&buf[..n]);
        read += n as u64;
        complete = n < buf.len();
    }
    let scanned = scanner.finish(complete);
    let index = (complete && checkpoints.len() > 1).then_some(LineIndex {
        stamp,
        total_lines: scanned.total_lines,
        checkpoints,
    });
    Ok((scanned.into_window(TextEncoding::Utf8, total_size, stamp), index))
}

/// Reads until `buf` is full or the input ends; returns how much was read.
fn fill(input: &mut impl Read, buf: &mut [u8]) -> io::Result<usize> {
    let mut n = 0;
    while n < buf.len() {
        match input.read(&mut buf[n..]) {
            Ok(0) => break,
            Ok(read) => n += read,
            Err(err) if err.kind() == io::ErrorKind::Interrupted => {}
            Err(err) => return Err(err),
        }
    }
    Ok(n)
}

/// What a scan found.
struct Scanned {
    lines: Vec<String>,
    total_lines: u64,
    total_exact: bool,
    byte_limited: bool,
}

impl Scanned {
    fn into_window(self, encoding: TextEncoding, total_size: u64, stamp: FileStamp) -> TextWindow {
        TextWindow {
            lines: self.lines,
            total_lines: self.total_lines,
            total_exact: self.total_exact,
            byte_limited: self.byte_limited,
            encoding,
            total_size,
            stamp,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Phase {
    /// Passing over the lines before the window.
    Skip,
    /// Keeping the lines of the window.
    Collect,
    /// Counting the lines after it.
    Count,
}

/// Splits text into lines as it streams by, and keeps those of the window.
struct LineScanner {
    window: LineWindow,
    /// Bytes of a line kept to show it; the rest of a long line is only counted.
    keep: usize,
    phase: Phase,
    /// Lines that have ended.
    ended: u64,
    /// Part of a line that has not ended yet was seen.
    in_line: bool,
    /// The last byte was a `\r`: a `\n` right after it belongs to the same line break.
    after_cr: bool,
    /// The beginning of the current line, while collecting.
    line: Vec<u8>,
    /// Characters of the current line past `line`.
    more_chars: u64,
    lines: Vec<String>,
    bytes: usize,
    byte_limited: bool,
    /// Bytes looked at only to count them: past the window, and past the kept part of long
    /// lines.
    counted: u64,
}

impl LineScanner {
    fn new(window: &LineWindow) -> Self {
        let window = LineWindow {
            offset: window.offset.max(1),
            max_lines: window.max_lines.max(1),
            ..window.clone()
        };
        Self {
            // One character more than is shown, even if each takes four bytes.
            keep: window.max_line_chars.saturating_add(1).saturating_mul(4),
            phase: if window.offset == 1 { Phase::Collect } else { Phase::Skip },
            window,
            ended: 0,
            in_line: false,
            after_cr: false,
            line: Vec::new(),
            more_chars: 0,
            lines: Vec::new(),
            bytes: 0,
            byte_limited: false,
            counted: 0,
        }
    }

    /// A scan that starts at `at`, a checkpoint at or before the first line of the window.
    fn resume(window: &LineWindow, at: Checkpoint) -> Self {
        let mut scanner = Self::new(window);
        scanner.ended = at.ended;
        scanner.in_line = at.in_line;
        scanner.after_cr = at.after_cr;
        scanner.phase = if at.ended + 1 < scanner.window.offset { Phase::Skip } else { Phase::Collect };
        scanner
    }

    fn checkpoint(&self, byte: u64) -> Checkpoint {
        Checkpoint {
            byte,
            ended: self.ended,
            in_line: self.in_line,
            after_cr: self.after_cr,
        }
    }

    fn feed(&mut self, chunk: &[u8]) {
        let Some(&last) = chunk.last() else {
            return;
        };
        // The `\n` of a `\r\n` split between two chunks.
        let mut pos = usize::from(self.after_cr && chunk[0] == b'\n');
        while pos < chunk.len() {
            pos = match self.phase {
                Phase::Skip => self.skip(chunk, pos),
                Phase::Collect => self.collect(chunk, pos),
                Phase::Count => {
                    self.count(&chunk[pos..]);
                    chunk.len()
                }
            };
        }
        self.after_cr = last == b'\r';
    }

    /// Passes over lines before the window; returns where the scan goes on.
    fn skip(&mut self, chunk: &[u8], pos: usize) -> usize {
        let rest = &chunk[pos..];
        let breaks = count_breaks(rest);
        if breaks < self.window.offset - 1 - self.ended {
            self.ended += breaks;
            self.in_line = !ends_line(rest);
            return chunk.len();
        }
        // The window starts in this chunk.
        let mut at = pos;
        while let Some(i) = memchr::memchr2(b'\n', b'\r', &chunk[at..]) {
            at = after_break(chunk, at + i);
            self.ended += 1;
            if self.ended + 1 == self.window.offset {
                break;
            }
        }
        self.in_line = false;
        self.phase = Phase::Collect;
        at
    }

    /// Keeps lines of the window; returns where the scan goes on.
    fn collect(&mut self, chunk: &[u8], mut pos: usize) -> usize {
        while self.phase == Phase::Collect {
            let Some(i) = memchr::memchr2(b'\n', b'\r', &chunk[pos..]) else {
                self.push(&chunk[pos..]);
                return chunk.len();
            };
            self.push(&chunk[pos..pos + i]);
            pos = after_break(chunk, pos + i);
            self.end_line(true);
        }
        pos
    }

    fn push(&mut self, bytes: &[u8]) {
        if bytes.is_empty() {
            return;
        }
        self.in_line = true;
        let take = self.keep.saturating_sub(self.line.len()).min(bytes.len());
        self.line.extend_from_slice(&bytes[..take]);
        let rest = &bytes[take..];
        self.more_chars += char_starts(rest);
        self.counted += rest.len() as u64;
    }

    /// Ends the current line of the window. Unless `exact`, it goes on past the input.
    fn end_line(&mut self, exact: bool) {
        self.ended += 1;
        self.in_line = false;
        let start = String::from_utf8_lossy(&self.line);
        let shown = truncate_line_start(&start, self.more_chars, exact, self.window.max_line_chars);
        self.line.clear();
        self.more_chars = 0;
        if !self.lines.is_empty() && self.bytes + shown.len() + 1 > self.window.max_bytes {
            self.byte_limited = true;
            self.phase = Phase::Count;
            return;
        }
        self.bytes += shown.len() + 1;
        self.lines.push(shown);
        if self.lines.len() >= self.window.max_lines {
            self.phase = Phase::Count;
        }
    }

    fn count(&mut self, bytes: &[u8]) {
        self.ended += count_breaks(bytes);
        self.in_line = !ends_line(bytes);
        self.counted += bytes.len() as u64;
    }

    /// The result once the input ends. Unless `complete`, the file goes on past the input.
    fn finish(mut self, complete: bool) -> Scanned {
        if self.in_line {
            // A last line without a line break. One that goes on past the input is shown only
            // when it is cut anyway.
            if self.phase == Phase::Collect && (complete || self.line_is_cut()) {
                self.end_line(complete);
            } else {
                self.ended += 1;
            }
        }
        Scanned {
            lines: self.lines,
            total_lines: self.ended,
            total_exact: complete,
            byte_limited: self.byte_limited,
        }
    }

    fn line_is_cut(&self) -> bool {
        self.more_chars > 0 || String::from_utf8_lossy(&self.line).chars().nth(self.window.max_line_chars).is_some()
    }
}

/// Where the text goes on after the line break at `at`.
fn after_break(chunk: &[u8], at: usize) -> usize {
    if chunk[at] == b'\r' && chunk.get(at + 1) == Some(&b'\n') { at + 2 } else { at + 1 }
}

/// Line breaks in `bytes`: each `\n` and each `\r`, a `\r\n` counting once.
fn count_breaks(bytes: &[u8]) -> u64 {
    let newlines = memchr::memchr_iter(b'\n', bytes).count();
    let mut returns = 0;
    let mut pairs = 0;
    for at in memchr::memchr_iter(b'\r', bytes) {
        returns += 1;
        pairs += usize::from(bytes.get(at + 1) == Some(&b'\n'));
    }
    (newlines + returns - pairs) as u64
}

fn ends_line(bytes: &[u8]) -> bool {
    matches!(bytes.last(), Some(b'\n' | b'\r'))
}

/// Characters in UTF-8 text, counted as the bytes that do not continue a character.
fn char_starts(bytes: &[u8]) -> u64 {
    bytes.iter().filter(|b| (**b as i8) >= -0x40).count() as u64
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::truncate::truncate_line;

    fn window(offset: u64, max_lines: usize, max_bytes: usize, max_line_chars: usize) -> LineWindow {
        LineWindow {
            offset,
            max_lines,
            max_bytes,
            max_line_chars,
        }
    }

    fn scan_chunks(text: &[u8], window: &LineWindow, chunk: usize) -> Scanned {
        let mut scanner = LineScanner::new(window);
        for part in text.chunks(chunk) {
            scanner.feed(part);
        }
        scanner.finish(true)
    }

    /// Lines the simple way: split on `\r\n`, `\r` and `\n`, each decoded on its own.
    fn reference_lines(text: &[u8]) -> Vec<String> {
        let mut lines = Vec::new();
        let (mut start, mut i) = (0, 0);
        while i < text.len() {
            match text[i] {
                b'\n' | b'\r' => {
                    lines.push(String::from_utf8_lossy(&text[start..i]).into_owned());
                    i += if text[i] == b'\r' && text.get(i + 1) == Some(&b'\n') { 2 } else { 1 };
                    start = i;
                }
                _ => i += 1,
            }
        }
        if start < text.len() {
            lines.push(String::from_utf8_lossy(&text[start..]).into_owned());
        }
        lines
    }

    /// The window the simple way: (lines, total, byte_limited).
    fn reference_window(text: &[u8], window: &LineWindow) -> (Vec<String>, u64, bool) {
        reference_window_of(&reference_lines(text), window)
    }

    fn reference_window_of(all: &[String], window: &LineWindow) -> (Vec<String>, u64, bool) {
        let mut lines: Vec<String> = Vec::new();
        let mut bytes = 0;
        let mut byte_limited = false;
        for line in all.iter().skip(window.offset as usize - 1) {
            if lines.len() >= window.max_lines {
                break;
            }
            let shown = truncate_line(line, window.max_line_chars);
            if !lines.is_empty() && bytes + shown.len() + 1 > window.max_bytes {
                byte_limited = true;
                break;
            }
            bytes += shown.len() + 1;
            lines.push(shown);
        }
        (lines, all.len() as u64, byte_limited)
    }

    /// Text with every kind of line break, empty and long lines, multi-byte characters and
    /// invalid bytes, from a fixed seed.
    fn mixed_text(seed: u64, pieces: usize) -> Vec<u8> {
        const PARTS: [&[u8]; 12] = [
            b"abc", b" ", b"\xC3\xA9", "日本".as_bytes(), "😀".as_bytes(), b"\xFF", b"\xC3(", b"\n", b"\r\n", b"\r",
            b"\n\n", b"\r\r\n",
        ];
        let mut state = seed;
        let mut text = Vec::new();
        for _ in 0..pieces {
            state ^= state << 13;
            state ^= state >> 7;
            state ^= state << 17;
            let part = PARTS[(state % PARTS.len() as u64) as usize];
            // Now and then a long run of one part, to make long lines.
            let breaks = part.contains(&b'\n') || part.contains(&b'\r');
            let times = if state.is_multiple_of(23) && !breaks { 90 } else { 1 };
            for _ in 0..times {
                text.extend_from_slice(part);
            }
        }
        text
    }

    #[test]
    fn matches_the_simple_way_wherever_the_chunks_end() {
        for seed in 1..=12u64 {
            let text = mixed_text(seed * 0x9E37_79B9, 600);
            let total = reference_lines(&text).len() as u64;
            for (offset, max_lines, max_bytes, max_chars) in [
                (1, 2000, 50 * 1024, 2000),
                (1, 7, 50 * 1024, 2000),
                (1, 2000, 120, 2000),
                (2, 2000, 50 * 1024, 20),
                (total / 2, 9, 200, 30),
                (total, 2000, 50 * 1024, 2000),
                (total + 1, 2000, 50 * 1024, 2000),
            ] {
                let window = window(offset.max(1), max_lines, max_bytes, max_chars);
                let expected = reference_window(&text, &window);
                for chunk in [1, 2, 3, 5, 64, 4096, text.len().max(1)] {
                    let got = scan_chunks(&text, &window, chunk);
                    assert_eq!(
                        (got.lines, got.total_lines, got.byte_limited),
                        expected,
                        "seed {seed}, window {window:?}, chunks of {chunk}"
                    );
                    assert!(got.total_exact);
                }
            }
        }
    }

    #[test]
    fn line_breaks() {
        let lines = |text: &[u8]| scan_chunks(text, &window(1, 100, 1 << 20, 100), 1).lines;
        assert_eq!(lines(b"a\r\nb\rc\nd"), ["a", "b", "c", "d"]);
        assert_eq!(lines(b"\r\n\r\n"), ["", ""]);
        assert_eq!(lines(b"\r\r\n\n"), ["", "", ""]);
        assert_eq!(lines(b"a\n"), ["a"]);
        assert_eq!(lines(b"a\r"), ["a"]);
        assert!(lines(b"").is_empty());
    }

    #[test]
    fn long_lines_are_cut_and_measured() {
        let text = format!("{}\nshort\n", "日本語".repeat(10_000));
        let got = scan_chunks(text.as_bytes(), &window(1, 100, 1 << 20, 10), 1000);
        assert_eq!(got.lines, ["日本語日本語日本語日… [line truncated: 10 of 30000 characters]", "short"]);
        // Only the beginning of the long line was kept.
        let mut scanner = LineScanner::new(&window(1, 100, 1 << 20, 10));
        scanner.feed(text.as_bytes());
        assert!(scanner.line.capacity() < 1024);
    }

    #[test]
    fn the_byte_budget_ends_the_window_but_always_shows_a_line() {
        let text = b"0123456789\n0123456789\n0123456789\n0123456789\n";
        let got = scan_chunks(text, &window(1, 100, 35, 100), 7);
        assert_eq!((got.lines.len(), got.total_lines, got.byte_limited), (3, 4, true));
        let got = scan_chunks(text, &window(2, 100, 5, 100), 7);
        assert_eq!((got.lines.len(), got.total_lines, got.byte_limited), (1, 4, true));
        let got = scan_chunks(text, &window(1, 4, 44, 100), 7);
        assert_eq!((got.lines.len(), got.byte_limited), (4, false));
    }

    fn stream(text: &[u8], window: &LineWindow, count_limit: u64) -> TextWindow {
        let mut input = text;
        read_window(&mut input, text.len() as u64, FileStamp::default(), window, count_limit).unwrap()
    }

    #[test]
    fn counting_stops_at_the_limit_and_says_so() {
        let text: String = (1..=400_000).map(|i| format!("{i}\n")).collect();
        let all = stream(text.as_bytes(), &window(10, 2, 1 << 20, 100), u64::MAX);
        assert_eq!((all.lines, all.total_lines, all.total_exact), (vec!["10".into(), "11".into()], 400_000, true));
        let some = stream(text.as_bytes(), &window(10, 2, 1 << 20, 100), 1);
        assert_eq!(some.lines, ["10", "11"]);
        assert!(!some.total_exact);
        assert!(some.total_lines > 11 && some.total_lines < 400_000, "{}", some.total_lines);
        // The lines before the window are always passed over, however many.
        let deep = stream(text.as_bytes(), &window(399_999, 5, 1 << 20, 100), 1);
        assert_eq!(deep.lines, ["399999", "400000"]);
        assert_eq!((deep.total_lines, deep.total_exact), (400_000, true));
    }

    #[test]
    fn a_line_longer_than_the_count_limit_gives_a_lower_bound() {
        let text = "x".repeat(3 << 20);
        let got = stream(text.as_bytes(), &window(1, 10, 1 << 20, 5), 1);
        assert_eq!(got.lines.len(), 1);
        assert!(got.lines[0].starts_with("xxxxx… [line truncated: 5 of at least "), "{}", got.lines[0]);
        assert_eq!((got.total_lines, got.total_exact), (1, false));
        let got = stream(text.as_bytes(), &window(1, 10, 1 << 20, 5), u64::MAX);
        assert_eq!(got.lines, [format!("xxxxx… [line truncated: 5 of {} characters]", 3 << 20)]);
        assert!(got.total_exact);
    }

    fn indexed(
        text: &[u8],
        stamp: FileStamp,
        window: &LineWindow,
        index: Option<&LineIndex>,
    ) -> (TextWindow, Option<LineIndex>) {
        read_window_indexed(&mut io::Cursor::new(text), text.len() as u64, stamp, window, u64::MAX, index).unwrap()
    }

    #[test]
    fn reads_from_an_index_match_the_simple_way() {
        // Buffers end inside a CRLF, inside a line, right after a line and after a byte order
        // mark; long lines and every kind of break on the way.
        let filled = |prefix: &[u8], at: usize, rest: &[u8], seed: u64| {
            let mut text = prefix.to_vec();
            text.resize(at, b'x');
            text.extend_from_slice(rest);
            text.extend(mixed_text(seed, 500_000));
            text
        };
        let mut texts = Vec::new();
        for prefix in [&b""[..], UTF8_BOM] {
            texts.push(filled(prefix, BUFFER - 1, b"\r\nnext\n", 7));
            texts.push(filled(prefix, BUFFER - 1, b"\nnext line\r", 11));
        }
        texts.push(mixed_text(3, 900_000));
        let stamp = FileStamp::default();
        for text in &texts {
            let body = text.strip_prefix(UTF8_BOM).unwrap_or(text);
            let all = reference_lines(body);
            let total = all.len() as u64;
            let (_, index) = indexed(text, stamp, &window(1, 1, 100, 100), None);
            let index = index.expect("a complete scan of a large file gives an index");
            assert!(index.checkpoints.len() > 2, "{} bytes", text.len());
            let mut offsets = vec![1, 2, 3, total / 3, total - 1, total, total + 1];
            // The lines on both sides of every checkpoint.
            for checkpoint in &index.checkpoints {
                offsets.extend([checkpoint.ended, checkpoint.ended + 1, checkpoint.ended + 2]);
            }
            for offset in offsets.into_iter().filter(|o| *o > 0) {
                let shapes = [(2000, 50 * 1024, 2000), (3, 50 * 1024, 2000), (40, 300, 40)];
                for (max_lines, max_bytes, max_chars) in shapes {
                    let window = window(offset, max_lines, max_bytes, max_chars);
                    let (got, none) = indexed(text, stamp, &window, Some(&index));
                    assert!(none.is_none());
                    assert!(got.total_exact);
                    assert_eq!(
                        (got.lines, got.total_lines, got.byte_limited),
                        reference_window_of(&all, &window),
                        "window {window:?}"
                    );
                }
            }
        }
    }

    #[test]
    fn an_index_belongs_to_one_version() {
        let text: String = (1..=300_000).map(|i| format!("line {i}\n")).collect();
        let window = window(5, 2, 1 << 20, 100);
        let stamp = FileStamp {
            size: 1,
            ..FileStamp::default()
        };
        let (_, index) = indexed(text.as_bytes(), stamp, &window, None);
        let index = index.unwrap();
        assert_eq!(index.stamp(), stamp);
        // Another version is scanned again, and gives its own index.
        let other = FileStamp { size: 2, ..stamp };
        let (got, new) = indexed(text.as_bytes(), other, &window, Some(&index));
        assert_eq!(got.lines, ["line 5", "line 6"]);
        assert_eq!(new.unwrap().stamp(), other);
        // A small file is not indexed, and a count stopped early gives no index.
        assert!(indexed(b"a\nb\n", stamp, &window, None).1.is_none());
        let mut input = io::Cursor::new(text.as_bytes());
        let (_, none) = read_window_indexed(&mut input, text.len() as u64, stamp, &window, 1, None).unwrap();
        assert!(none.is_none());
    }

    #[test]
    fn utf16_and_byte_order_marks() {
        let text = "a\u{00E9}\r\n\u{1F600}\n";
        let le: Vec<u8> = [0xFF, 0xFE].into_iter().chain(text.encode_utf16().flat_map(u16::to_le_bytes)).collect();
        let be: Vec<u8> = [0xFE, 0xFF].into_iter().chain(text.encode_utf16().flat_map(u16::to_be_bytes)).collect();
        for (bytes, encoding) in [(le, TextEncoding::Utf16Le), (be, TextEncoding::Utf16Be)] {
            assert!(!is_binary(&bytes));
            let got = stream(&bytes, &window(1, 10, 1 << 20, 100), u64::MAX);
            assert_eq!((got.lines, got.encoding), (vec!["a\u{00E9}".to_string(), "\u{1F600}".to_string()], encoding));
        }
        // An unpaired surrogate and an odd last byte.
        assert_eq!(decode_utf16(&[0x3D, 0xD8, 0x41, 0x00, 0x42], TextEncoding::Utf16Le), "\u{FFFD}A\u{FFFD}");
        // UTF-32 is not taken for UTF-16.
        assert!(is_binary(&[0xFF, 0xFE, 0, 0, b'a', 0, 0, 0]));
        let got = stream(b"\xEF\xBB\xBFfirst\nsecond\n", &window(1, 10, 1 << 20, 100), u64::MAX);
        assert_eq!(got.lines, ["first", "second"]);
    }

    #[test]
    fn nul_bytes_early_on_make_a_file_binary() {
        let mut text = vec![b'a'; SNIFF_BYTES];
        text.push(0);
        assert!(!is_binary(&text));
        assert_eq!(stream(&text, &window(1, 10, 1 << 20, 100), u64::MAX).lines.len(), 1);
        text[100] = 0;
        assert!(is_binary(&text));
        let mut input = &text[..];
        let err = read_window(&mut input, 0, FileStamp::default(), &window(1, 10, 100, 10), u64::MAX).unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::InvalidData);
    }

    #[test]
    fn a_file_cut_short_gives_lower_bounds() {
        let content = |bytes: &[u8]| FileContent {
            bytes: bytes.to_vec(),
            truncated: true,
            total_size: 1 << 30,
            stamp: FileStamp::default(),
        };
        // The last line may go on past what was loaded: it is not shown.
        let got = window_in_memory(&content(b"one\ntwo\nthr"), &window(1, 10, 1 << 20, 100)).unwrap();
        assert_eq!((got.lines, got.total_lines, got.total_exact), (vec!["one".into(), "two".into()], 3, false));
        // Unless it is cut anyway.
        let got = window_in_memory(&content(b"one\ntwo\nthree"), &window(3, 10, 1 << 20, 2)).unwrap();
        assert_eq!(got.lines, ["th… [line truncated: 2 of at least 5 characters]"]);
        // Past what was loaded.
        let got = window_in_memory(&content(b"one\ntwo\n"), &window(5, 10, 1 << 20, 100)).unwrap();
        assert_eq!((got.lines.len(), got.total_lines, got.total_exact), (0, 2, false));
    }
}
