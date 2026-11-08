//! Unified diff of an edit, computed from the replaced lines only: its cost follows the size
//! of the change, not the size of the file.

use similar::Algorithm;
use similar::DiffOp;
use similar::DiffTag;
use similar::capture_diff_slices_deadline;
use similar::group_diff_ops;
use similar::udiff::UnifiedHunkHeader;
use std::time::Duration;
use std::time::Instant;

use super::Replacement;
use super::matching::Text;
use super::matching::lf;

/// Lines of context around each change.
const CONTEXT: usize = 3;
/// Time for the line diffs of all changed blocks; after it, the rest gets a coarser diff.
const TIME_LIMIT: Duration = Duration::from_secs(1);

/// The new lines of one block of changed lines.
struct Block {
    /// Index of its first line in the new text.
    start: usize,
    lines: Vec<String>,
}

/// Unified diff between `text` and `text` with `replacements` (sorted, not overlapping)
/// applied. Line breaks are shown as `\n`; empty when nothing changes line by line.
pub fn unified(display: &str, text: &Text, replacements: &[Replacement]) -> String {
    let deadline = Some(Instant::now() + TIME_LIMIT);
    let raw = text.raw();
    let mut ops = Vec::new();
    let mut blocks = Vec::new();
    // The first old and new lines that no op covers yet.
    let (mut old_at, mut new_at) = (0, 0);
    let mut i = 0;
    while i < replacements.len() {
        // A block holds the whole lines of replacements that share lines. It grows by a line
        // while its new text does not end a line, as the next line then joins its last one.
        let first = first_line(text, &replacements[i]);
        let mut last = first;
        let mut new = String::new();
        let mut at = text.line_start(first);
        loop {
            while let Some(r) = replacements.get(i)
                && first_line(text, r) <= last
            {
                new.push_str(&raw[at..r.start]);
                new.push_str(&r.text);
                at = r.end;
                last = last.max(text.line_of(r.end - 1));
                i += 1;
            }
            let end = text.line_end(last);
            if at < end || end == raw.len() || new.is_empty() || new.ends_with(['\n', '\r']) {
                new.push_str(&raw[at..end]);
                break;
            }
            last += 1;
        }
        let old = lf(&raw[text.line_start(first)..text.line_end(last)]);
        let new = lf(&new);
        let old_lines: Vec<&str> = old.split_inclusive('\n').collect();
        let new_lines: Vec<&str> = new.split_inclusive('\n').collect();
        push(&mut ops, DiffOp::Equal {
            old_index: old_at,
            new_index: new_at,
            len: first - old_at,
        });
        new_at += first - old_at;
        for op in capture_diff_slices_deadline(Algorithm::Myers, &old_lines, &new_lines, deadline) {
            push(&mut ops, shift(op, first, new_at));
        }
        blocks.push(Block {
            start: new_at,
            lines: new_lines.iter().map(|l| l.to_string()).collect(),
        });
        old_at = last + 1;
        new_at += new_lines.len();
    }
    push(&mut ops, DiffOp::Equal {
        old_index: old_at,
        new_index: new_at,
        len: text.line_count().saturating_sub(old_at),
    });
    render(display, text, &blocks, renumber(ops))
}

/// The ops with their positions counted afresh, in order. After its compaction step, similar
/// can return a delete placed after the equal run that follows it (`new_index` 1 before an
/// equal run at 0), which gives hunk headers that do not match their lines.
fn renumber(ops: Vec<DiffOp>) -> Vec<DiffOp> {
    let (mut old, mut new) = (0, 0);
    ops.into_iter()
        .map(|op| {
            let op = match op {
                DiffOp::Equal { len, .. } => DiffOp::Equal {
                    old_index: old,
                    new_index: new,
                    len,
                },
                DiffOp::Delete { old_len, .. } => DiffOp::Delete {
                    old_index: old,
                    old_len,
                    new_index: new,
                },
                DiffOp::Insert { new_len, .. } => DiffOp::Insert {
                    old_index: old,
                    new_index: new,
                    new_len,
                },
                DiffOp::Replace { old_len, new_len, .. } => DiffOp::Replace {
                    old_index: old,
                    old_len,
                    new_index: new,
                    new_len,
                },
            };
            let (old_len, new_len) = match op {
                DiffOp::Equal { len, .. } => (len, len),
                DiffOp::Delete { old_len, .. } => (old_len, 0),
                DiffOp::Insert { new_len, .. } => (0, new_len),
                DiffOp::Replace { old_len, new_len, .. } => (old_len, new_len),
            };
            old += old_len;
            new += new_len;
            op
        })
        .collect()
}

/// The first line a replacement changes: its own, or the line before when that line's lone CR
/// was made a CRLF to stay apart from an LF of the new text.
fn first_line(text: &Text, r: &Replacement) -> usize {
    let after_cr = r.start > 0 && text.raw().as_bytes()[r.start - 1] == b'\r';
    if after_cr && r.text.starts_with('\n') { text.line_of(r.start - 1) } else { text.line_of(r.start) }
}

/// Appends `op`, merging it into an equal run before it and dropping empty runs.
fn push(ops: &mut Vec<DiffOp>, op: DiffOp) {
    if let DiffOp::Equal { len, .. } = op {
        if len == 0 {
            return;
        }
        if let Some(DiffOp::Equal { len: before, .. }) = ops.last_mut() {
            *before += len;
            return;
        }
    }
    ops.push(op);
}

/// `op` of a block, moved to the block's place in the whole texts.
fn shift(op: DiffOp, old: usize, new: usize) -> DiffOp {
    match op {
        DiffOp::Equal {
            old_index,
            new_index,
            len,
        } => DiffOp::Equal {
            old_index: old_index + old,
            new_index: new_index + new,
            len,
        },
        DiffOp::Delete {
            old_index,
            old_len,
            new_index,
        } => DiffOp::Delete {
            old_index: old_index + old,
            old_len,
            new_index: new_index + new,
        },
        DiffOp::Insert {
            old_index,
            new_index,
            new_len,
        } => DiffOp::Insert {
            old_index: old_index + old,
            new_index: new_index + new,
            new_len,
        },
        DiffOp::Replace {
            old_index,
            old_len,
            new_index,
            new_len,
        } => DiffOp::Replace {
            old_index: old_index + old,
            old_len,
            new_index: new_index + new,
            new_len,
        },
    }
}

fn render(display: &str, text: &Text, blocks: &[Block], ops: Vec<DiffOp>) -> String {
    let mut out = String::new();
    for group in group_diff_ops(ops, CONTEXT) {
        if out.is_empty() {
            out.push_str(&format!("--- {display}\n+++ {display}\n"));
        }
        out.push_str(&format!("{}\n", UnifiedHunkHeader::new(&group)));
        for op in &group {
            let (tag, old, new) = op.as_tag_tuple();
            match tag {
                DiffTag::Equal => old.for_each(|k| line(&mut out, ' ', &text.line_lf(k))),
                DiffTag::Delete => old.for_each(|k| line(&mut out, '-', &text.line_lf(k))),
                DiffTag::Insert => new.for_each(|k| line(&mut out, '+', new_line(blocks, k))),
                DiffTag::Replace => {
                    old.for_each(|k| line(&mut out, '-', &text.line_lf(k)));
                    new.for_each(|k| line(&mut out, '+', new_line(blocks, k)));
                }
            }
        }
    }
    out
}

/// New line `k`; only lines inside blocks differ from the old text.
fn new_line(blocks: &[Block], k: usize) -> &str {
    let block = &blocks[blocks.partition_point(|b| b.start <= k).saturating_sub(1)];
    &block.lines[k - block.start]
}

fn line(out: &mut String, tag: char, text: &str) {
    out.push(tag);
    out.push_str(text);
    if !text.ends_with('\n') {
        out.push_str("\n\\ No newline at end of file\n");
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rep(raw: &str, old: &str, new: &str) -> Replacement {
        let start = raw.find(old).unwrap();
        Replacement {
            start,
            end: start + old.len(),
            text: new.to_string(),
            edit: 1,
        }
    }

    /// The diff of the whole texts, as the similar crate writes it.
    fn reference(display: &str, old: &str, new: &str) -> String {
        similar::TextDiff::from_lines(old, new)
            .unified_diff()
            .context_radius(CONTEXT)
            .header(display, display)
            .to_string()
    }

    fn apply(raw: &str, replacements: &[Replacement]) -> String {
        let mut out = String::new();
        let mut at = 0;
        for r in replacements {
            out.push_str(&raw[at..r.start]);
            out.push_str(&r.text);
            at = r.end;
        }
        out.push_str(&raw[at..]);
        out
    }

    #[test]
    fn matches_a_diff_of_the_whole_texts() {
        let raw: String = (1..=40).map(|i| format!("line {i}\n")).collect();
        let cases: Vec<Vec<Replacement>> = vec![
            vec![rep(&raw, "line 2\n", "two\n")],
            // Two changes close enough to share a hunk, and one far away.
            vec![
                rep(&raw, "line 10\n", "ten\n"),
                rep(&raw, "line 14\n", ""),
                rep(&raw, "line 30\n", "thirty\nand more\n"),
            ],
            // Two replacements on one line, and a change that keeps its first lines.
            vec![rep(&raw, "line 2", "TWO"), rep(&raw, "line 3\nline 4", "line 3\nline 4\nnew")],
            // A replacement that joins two lines, and one at the very end.
            vec![rep(&raw, "line 7\n", "line 7 "), rep(&raw, "line 40\n", "last")],
            vec![rep(&raw, "line 1\n", "")],
        ];
        for (n, replacements) in cases.iter().enumerate() {
            let new = apply(&raw, replacements);
            let text = Text::new(raw.clone());
            assert_eq!(unified("f.txt", &text, replacements), reference("f.txt", &raw, &new), "case {n}");
        }
    }

    #[test]
    fn shows_line_breaks_as_lf_and_skips_the_byte_order_mark() {
        let raw = "\u{feff}a\r\nb\r\nc";
        let text = Text::new(raw.to_string());
        let diff = unified("f", &text, &[rep(raw, "b\r\nc", "B\r\nC")]);
        let no_newline = "\\ No newline at end of file\n";
        assert_eq!(diff, format!("--- f\n+++ f\n@@ -1,3 +1,3 @@\n a\n-b\n-c\n{no_newline}+B\n+C\n{no_newline}"));
        let diff = unified("f", &text, &[rep(raw, "a", "A")]);
        assert_eq!(diff, "--- f\n+++ f\n@@ -1,3 +1,3 @@\n-a\n+A\n b\n c\n\\ No newline at end of file\n");
        // Only the line breaks changed.
        assert_eq!(unified("f", &text, &[rep(raw, "a\r\n", "a\n")]), "");
    }
}
