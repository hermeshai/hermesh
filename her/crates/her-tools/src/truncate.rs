//! Output limits shared by the tools and the kernel's safety net.
//!
//! Budgets are in tokens, estimated at [`BYTES_PER_TOKEN`] bytes each so that no tokenizer
//! runs; text is cut at the bytes a budget stands for. A result is cut once, before it enters
//! the history, and never changes afterwards. Whatever is cut stays readable: the full output
//! is saved to a file and the result says where.

use her_llm::UserContent;
use std::io;
use std::io::Write;
use std::path::Path;
use std::path::PathBuf;

/// Bytes per token in every budget here.
pub const BYTES_PER_TOKEN: usize = 4;

/// The smallest budget of one result, however small the window.
const MIN_TOKENS: usize = 256;

/// The bytes that `tokens` tokens stand for.
pub const fn bytes_for_tokens(tokens: usize) -> usize {
    tokens.saturating_mul(BYTES_PER_TOKEN)
}

/// The tokens that `bytes` bytes count as, rounded up.
pub const fn tokens_for_bytes(bytes: u64) -> u64 {
    bytes.div_ceil(BYTES_PER_TOKEN as u64)
}

#[derive(Clone, Debug)]
pub struct OutputLimits {
    pub max_lines: usize,
    /// Tokens of one tool's result.
    pub max_tokens: usize,
    pub max_line_chars: usize,
    /// Tokens of command output kept from the beginning.
    pub head_tokens: usize,
    /// Tokens of command output kept from the end.
    pub tail_tokens: usize,
    /// Size cap of one saved full-output file.
    pub max_saved_bytes: u64,
}

impl Default for OutputLimits {
    fn default() -> Self {
        Self {
            max_lines: 2000,
            max_tokens: 10_000,
            max_line_chars: 2000,
            head_tokens: 2_000,
            tail_tokens: 8_000,
            max_saved_bytes: 64 * 1024 * 1024,
        }
    }
}

impl OutputLimits {
    /// The limits with the budgets scaled down to `max_tokens`, keeping the head/tail split.
    pub fn capped(&self, max_tokens: usize) -> OutputLimits {
        let max_tokens = max_tokens.max(MIN_TOKENS);
        if max_tokens >= self.max_tokens {
            return self.clone();
        }
        let scale = |n: usize| (n as u128 * max_tokens as u128 / self.max_tokens as u128) as usize;
        OutputLimits {
            max_tokens,
            head_tokens: scale(self.head_tokens),
            tail_tokens: scale(self.tail_tokens),
            ..self.clone()
        }
    }

    /// The limits with a budget of `max_tokens`. A smaller budget also shrinks the command
    /// output's head and tail to fit.
    pub fn with_max_tokens(&self, max_tokens: usize) -> OutputLimits {
        let max_tokens = max_tokens.max(MIN_TOKENS);
        OutputLimits {
            max_tokens,
            ..self.capped(max_tokens)
        }
    }

    /// The kernel's safety net: a fifth over the tool budget, room for the notes and headers
    /// that tools add, so that what a tool has cut is not cut again.
    pub fn safety_net_tokens(&self) -> usize {
        self.max_tokens.saturating_add(self.max_tokens / 5)
    }
}

/// Cuts a line to `max_chars` characters. A cut line ends with a note that gives its length,
/// so that the rest can be fetched another way.
pub fn truncate_line(line: &str, max_chars: usize) -> String {
    truncate_line_start(line, 0, true, max_chars)
}

/// [`truncate_line`] for a line of which only the beginning, `start`, is at hand: `more_chars`
/// characters follow it, or at least that many when not `exact`.
pub fn truncate_line_start(start: &str, more_chars: u64, exact: bool, max_chars: usize) -> String {
    let cut = start.char_indices().nth(max_chars).map_or(start.len(), |(byte, _)| byte);
    if cut == start.len() && more_chars == 0 && exact {
        return start.to_string();
    }
    let shown = start[..cut].chars().count();
    let total = (shown + start[cut..].chars().count()) as u64 + more_chars;
    let at_least = if exact { "" } else { "at least " };
    format!("{}… [line truncated: {shown} of {at_least}{total} characters]", &start[..cut])
}

/// Keeps the beginning of `text`, at most `max_bytes` bytes, cut at a character boundary.
pub fn cap_text(text: &str, max_bytes: usize) -> (String, bool) {
    if text.len() <= max_bytes {
        return (text.to_string(), false);
    }
    let mut end = max_bytes;
    while end > 0 && !text.is_char_boundary(end) {
        end -= 1;
    }
    (text[..end].to_string(), true)
}

/// What was cut from the middle of an output.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Omission {
    /// The omitted lines, 1-based and inclusive, when the cut fell on line boundaries.
    pub lines: Option<(u64, u64)>,
    pub total_lines: u64,
    pub bytes: u64,
    /// The omitted bytes of the saved text, 1-based and inclusive. A cut inside a long line
    /// can only be read back by position.
    pub byte_range: (u64, u64),
}

/// Where the full output was saved.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SavedOutput {
    pub path: PathBuf,
    /// Only the first `max_saved_bytes` were saved.
    pub capped: bool,
}

/// The note that stands in for the omitted part, written for the model.
pub fn omission_note(omission: &Omission, saved: Option<&SavedOutput>) -> String {
    let size = format!("{} tokens", tokens_for_bytes(omission.bytes));
    let (start, end) = omission.byte_range;
    let mut note = match omission.lines {
        Some((first, last)) => format!(
            "[Lines {first}-{last} of {} omitted ({size}).",
            omission.total_lines
        ),
        None => format!("[Bytes {start}-{end} omitted ({size}), inside a long line."),
    };
    match saved {
        Some(saved) => {
            let which = if saved.capped { "Full output (first part only)" } else { "Full output" };
            let path = saved.path.display();
            note.push_str(&format!(" {which}: {path}"));
            match omission.lines {
                Some((first, _)) => note.push_str(&format!(" (read it with offset={first})")),
                // read shows lines and cuts long ones; bytes need a command.
                None => note.push_str(&format!(" (print those bytes with: head -c {end} {path} | tail -c +{start})")),
            }
            note.push(']');
        }
        None => note.push(']'),
    }
    note
}

/// "512 B", "56.7 KB", "3.2 MB".
pub fn format_size(bytes: u64) -> String {
    if bytes < 1024 {
        format!("{bytes} B")
    } else if bytes < 1024 * 1024 {
        format!("{:.1} KB", bytes as f64 / 1024.0)
    } else {
        format!("{:.1} MB", bytes as f64 / (1024.0 * 1024.0))
    }
}

/// Lines in `text`, counting a last line without a newline.
pub fn count_lines(text: &[u8]) -> u64 {
    let newlines = text.iter().filter(|b| **b == b'\n').count() as u64;
    if text.last().is_some_and(|b| *b != b'\n') { newlines + 1 } else { newlines }
}

/// Splits a cut into whole lines where possible: the head ends after its last newline and the
/// tail starts after its first one (unless it already starts a line). Returns the trimmed
/// head and tail and what lies between them.
pub fn trim_to_lines<'a>(
    head: &'a [u8],
    tail: &'a [u8],
    tail_starts_line: bool,
    total_bytes: u64,
    total_lines: u64,
) -> (&'a [u8], &'a [u8], Omission) {
    let head_cut = head.iter().rposition(|b| *b == b'\n').map(|i| i + 1);
    let tail_cut = if tail_starts_line {
        Some(0)
    } else {
        tail.iter().position(|b| *b == b'\n').map(|i| i + 1)
    };
    let (head, tail, lines) = match (head_cut, tail_cut) {
        (Some(h), Some(t)) => {
            let head = &head[..h];
            let tail = &tail[t..];
            let first = count_lines(head) + 1;
            let last = total_lines.saturating_sub(count_lines(tail));
            (head, tail, (first <= last).then_some((first, last)))
        }
        _ => (head, tail, None),
    };
    let bytes = total_bytes.saturating_sub(head.len() as u64 + tail.len() as u64);
    let byte_range = (head.len() as u64 + 1, total_bytes.saturating_sub(tail.len() as u64));
    (head, tail, Omission {
        lines,
        total_lines,
        bytes,
        byte_range,
    })
}

/// Cuts `text` to its first `head_bytes` and last `tail_bytes` (on line boundaries where
/// possible). `None` when it fits.
pub fn cut_head_tail(text: &str, head_bytes: usize, tail_bytes: usize) -> Option<(String, String, Omission)> {
    if text.len() <= head_bytes + tail_bytes {
        return None;
    }
    let mut head_end = head_bytes;
    while !text.is_char_boundary(head_end) {
        head_end -= 1;
    }
    let mut tail_start = text.len() - tail_bytes;
    while !text.is_char_boundary(tail_start) {
        tail_start += 1;
    }
    let bytes = text.as_bytes();
    let tail_starts_line = bytes[tail_start - 1] == b'\n';
    let (head, tail, omission) = trim_to_lines(
        &bytes[..head_end],
        &bytes[tail_start..],
        tail_starts_line,
        text.len() as u64,
        count_lines(bytes),
    );
    // The cuts are at character boundaries or right after a newline.
    let head = String::from_utf8_lossy(head).into_owned();
    let tail = String::from_utf8_lossy(tail).into_owned();
    Some((head, tail, omission))
}

/// Head, note and tail, as one text.
pub fn join_cut(head: &str, note: &str, tail: &str) -> String {
    let mut text = String::with_capacity(head.len() + note.len() + tail.len() + 2);
    text.push_str(head);
    if !text.is_empty() && !text.ends_with('\n') {
        text.push('\n');
    }
    text.push_str(note);
    text.push('\n');
    text.push_str(tail);
    text
}

/// File name for a tool call's full output: the call id with unsafe characters replaced.
pub fn output_file_name(call_id: &str) -> String {
    let name: String = call_id
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() || c == '-' || c == '_' { c } else { '_' })
        .collect();
    format!("{name}.txt")
}

/// Saves `data` (at most `max_bytes` of it) as a new file in `dir`, named after the call.
pub fn save_full_output(dir: &Path, call_id: &str, data: &[u8], max_bytes: u64) -> io::Result<SavedOutput> {
    let (mut file, path) = create_unique(&dir.join(output_file_name(call_id)))?;
    let capped = data.len() as u64 > max_bytes;
    let keep = data.len().min(max_bytes as usize);
    file.write_all(&data[..keep])?;
    Ok(SavedOutput { path, capped })
}

/// Creates `path`, or `name-1.ext`, `name-2.ext`… when it exists: saved outputs are never
/// overwritten, because results in the history point at them. Some providers reuse call ids
/// such as `call_0` in every response.
pub fn create_unique(path: &Path) -> io::Result<(std::fs::File, PathBuf)> {
    let stem = path.file_stem().map(|s| s.to_string_lossy().into_owned()).unwrap_or_default();
    let ext = path.extension().map(|e| format!(".{}", e.to_string_lossy())).unwrap_or_default();
    for attempt in 0..1000 {
        let candidate = if attempt == 0 {
            path.to_path_buf()
        } else {
            path.with_file_name(format!("{stem}-{attempt}{ext}"))
        };
        match create_private(&candidate) {
            Ok(file) => return Ok((file, candidate)),
            Err(err) if err.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(err) => return Err(err),
        }
    }
    Err(io::Error::other("no free file name for the output"))
}

/// Creates a new file readable only by the owner, and its directory if needed.
pub fn create_private(path: &Path) -> io::Result<std::fs::File> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let mut options = std::fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options.open(path)
}

/// Kernel safety net applied to every tool result: text over `max_tokens` keeps its beginning
/// and end, and the whole text is saved to `dir` first so that the model can read the rest.
/// Returns the saved file, if any.
pub fn cap_result(
    content: &mut Vec<UserContent>,
    max_tokens: usize,
    dir: Option<&Path>,
    call_id: &str,
    max_saved_bytes: u64,
) -> Option<SavedOutput> {
    let text_bytes: usize = content
        .iter()
        .map(|c| match c {
            UserContent::Text { text } => text.len() + 1,
            UserContent::Image { .. } => 0,
        })
        .sum();
    if tokens_for_bytes(text_bytes as u64) <= max_tokens as u64 {
        return None;
    }
    let max_bytes = bytes_for_tokens(max_tokens);
    let mut full = String::with_capacity(text_bytes);
    for block in content.iter() {
        if let UserContent::Text { text } = block {
            if !full.is_empty() {
                full.push('\n');
            }
            full.push_str(text);
        }
    }
    let half = max_bytes / 2;
    let (head, tail, omission) = cut_head_tail(&full, half, max_bytes - half)?;
    let saved = dir.and_then(|dir| match save_full_output(dir, call_id, full.as_bytes(), max_saved_bytes) {
        Ok(saved) => Some(saved),
        Err(err) => {
            tracing::warn!(%err, "cannot save the full tool output");
            None
        }
    });
    let text = join_cut(&head, &omission_note(&omission, saved.as_ref()), &tail);
    let images = content.drain(..).filter(|c| matches!(c, UserContent::Image { .. }));
    let mut out = vec![UserContent::Text { text }];
    out.extend(images);
    *content = out;
    saved
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn truncates_lines_by_characters() {
        assert_eq!(truncate_line("你好世界", 2), "你好… [line truncated: 2 of 4 characters]");
        assert_eq!(truncate_line("short", 10), "short");
        assert_eq!(truncate_line("exact", 5), "exact");
        // Only the beginning of the line is at hand.
        assert_eq!(truncate_line_start("abcdef", 94, true, 3), "abc… [line truncated: 3 of 100 characters]");
        assert_eq!(
            truncate_line_start("abcdef", 94, false, 3),
            "abc… [line truncated: 3 of at least 100 characters]"
        );
    }

    #[test]
    fn caps_at_char_boundaries() {
        let (capped, truncated) = cap_text("ab你好", 4);
        assert!(truncated);
        assert_eq!(capped, "ab");
    }

    fn numbered(lines: u64) -> String {
        (1..=lines).map(|n| format!("line {n}\n")).collect()
    }

    #[test]
    fn cuts_on_line_boundaries_and_names_the_omitted_lines() {
        let text = numbered(1000);
        let (head, tail, omission) = cut_head_tail(&text, 100, 200).unwrap();
        assert!(head.ends_with('\n') && tail.ends_with('\n'));
        let (first, last) = omission.lines.unwrap();
        // The head ends with line first-1 and the tail starts with line last+1.
        assert_eq!(head.lines().last().unwrap(), format!("line {}", first - 1));
        assert_eq!(tail.lines().next().unwrap(), format!("line {}", last + 1));
        assert_eq!(tail.lines().last().unwrap(), "line 1000");
        assert_eq!(omission.total_lines, 1000);
        assert_eq!(
            omission.bytes,
            (text.len() - head.len() - tail.len()) as u64
        );
        assert!(cut_head_tail("short", 100, 100).is_none());
    }

    #[test]
    fn a_single_long_line_is_cut_by_bytes() {
        let text: String = (0..2_000).map(|n| format!("{n:05}")).collect();
        let (head, tail, omission) = cut_head_tail(&text, 100, 100).unwrap();
        assert_eq!((head.len(), tail.len()), (100, 100));
        assert_eq!(omission.lines, None);
        assert_eq!(omission.byte_range, (101, 9_900));
        assert_eq!(&text[100..9_900].len(), &(omission.bytes as usize));
        let note = omission_note(&omission, None);
        assert_eq!(note, "[Bytes 101-9900 omitted (2450 tokens), inside a long line.]");
        let saved = SavedOutput {
            path: PathBuf::from("/s/out.txt"),
            capped: false,
        };
        assert!(
            omission_note(&omission, Some(&saved)).ends_with("(print those bytes with: head -c 9900 /s/out.txt | tail -c +101)]")
        );
    }

    #[test]
    fn output_without_a_final_newline_reassembles_exactly() {
        let text: String = (1..=500).map(|n| format!("row {n}")).collect::<Vec<_>>().join("\n");
        let (head, tail, omission) = cut_head_tail(&text, 300, 300).unwrap();
        let (first, last) = omission.lines.unwrap();
        let middle: Vec<&str> = text.lines().skip(first as usize - 1).take((last - first + 1) as usize).collect();
        assert_eq!(format!("{head}{}\n{tail}", middle.join("\n")), text);
        assert_eq!(omission.total_lines, 500);
    }

    #[test]
    fn notes_point_at_the_saved_file() {
        let omission = Omission {
            lines: Some((201, 1434)),
            total_lines: 1520,
            bytes: 58_060,
            byte_range: (4_001, 62_060),
        };
        let saved = SavedOutput {
            path: PathBuf::from("/s/tool-output/call_1.txt"),
            capped: false,
        };
        assert_eq!(
            omission_note(&omission, Some(&saved)),
            "[Lines 201-1434 of 1520 omitted (14515 tokens). Full output: /s/tool-output/call_1.txt (read it with offset=201)]"
        );
    }

    #[test]
    fn saving_never_overwrites_and_respects_the_cap() {
        let dir = tempfile::tempdir().unwrap();
        let first = save_full_output(dir.path(), "call/1", b"abcdef", 4).unwrap();
        assert!(first.capped);
        assert_eq!(first.path.file_name().unwrap(), "call_1.txt");
        assert_eq!(std::fs::read(&first.path).unwrap(), b"abcd");
        let second = save_full_output(dir.path(), "call/1", b"xyz", 10).unwrap();
        assert_eq!(second.path.file_name().unwrap(), "call_1-1.txt");
        assert!(!second.capped);
    }

    #[test]
    fn the_safety_net_saves_then_cuts() {
        let dir = tempfile::tempdir().unwrap();
        let mut content = vec![UserContent::Text { text: numbered(5000) }];
        let saved = cap_result(&mut content, 1024, Some(dir.path()), "call_9", u64::MAX).unwrap();
        assert_eq!(std::fs::read_to_string(&saved.path).unwrap(), numbered(5000));
        let UserContent::Text { text } = &content[0] else { panic!() };
        assert!(text.len() < 4096 + 300, "{}", text.len());
        assert!(text.starts_with("line 1\n"));
        assert!(text.trim_end().ends_with("line 5000"));
        let note = text.lines().find(|l| l.starts_with("[Lines ")).unwrap();
        let offset: u64 = note
            .split("offset=")
            .nth(1)
            .unwrap()
            .trim_end_matches(")]")
            .parse()
            .unwrap();
        // The offset is the first line that is not shown.
        let full = std::fs::read_to_string(&saved.path).unwrap();
        let first_hidden = full.lines().nth(offset as usize - 1).unwrap();
        assert!(!text.lines().any(|l| l == first_hidden));
        assert!(text.lines().any(|l| l == full.lines().nth(offset as usize - 2).unwrap()));

        let mut small = vec![UserContent::Text { text: "ok".into() }];
        assert!(cap_result(&mut small, 1024, Some(dir.path()), "call_10", u64::MAX).is_none());
    }

    #[test]
    fn the_safety_net_counts_tokens_by_bytes() {
        assert_eq!(bytes_for_tokens(10_000), 40_000);
        assert_eq!((tokens_for_bytes(40_000), tokens_for_bytes(40_001)), (10_000, 10_001));
        let dir = tempfile::tempdir().unwrap();
        // A result of 4,000 bytes (a block counts with its separator) is 1,000 tokens and fits.
        let mut fits = vec![UserContent::Text { text: "x".repeat(3_999) }];
        assert!(cap_result(&mut fits, 1_000, Some(dir.path()), "call_1", u64::MAX).is_none());
        let mut over = vec![UserContent::Text { text: "x\n".repeat(2_001) }];
        assert!(cap_result(&mut over, 1_000, Some(dir.path()), "call_2", u64::MAX).is_some());
        assert_eq!(OutputLimits::default().safety_net_tokens(), 12_000);
    }

    #[test]
    fn limits_scale_with_the_cap() {
        let limits = OutputLimits::default().capped(2_500);
        assert_eq!(limits.max_tokens, 2_500);
        assert_eq!(limits.head_tokens + limits.tail_tokens, 2_500);
        assert_eq!(limits.tail_tokens, 2_000);
        let same = OutputLimits::default().capped(1 << 30);
        assert_eq!(same.max_tokens, 10_000);
        let larger = OutputLimits::default().with_max_tokens(20_000);
        assert_eq!((larger.max_tokens, larger.head_tokens, larger.tail_tokens), (20_000, 2_000, 8_000));
        assert_eq!(OutputLimits::default().with_max_tokens(1).max_tokens, MIN_TOKENS);
    }
}
