//! The few lines at the bottom of the screen that change (§5.3). Each update goes back to the
//! first of them, clears to the end of the screen, prints what has been finished, so that it
//! scrolls up on its own, and prints those lines again.

use std::io::Write;
use unicode_width::UnicodeWidthStr;

/// What a line takes on screen: escape sequences take nothing.
pub fn width(text: &str) -> usize {
    if !text.contains('\x1b') {
        return text.width();
    }
    let mut total = 0;
    let mut rest = text;
    while let Some(start) = rest.find('\x1b') {
        total += rest[..start].width();
        let after = &rest[start + 1..];
        // `ESC [ … letter`
        rest = match after.char_indices().find(|(_, c)| c.is_ascii_alphabetic()) {
            Some((end, c)) => &after[end + c.len_utf8()..],
            None => "",
        };
    }
    total + rest.width()
}

fn rows_of(line_width: usize, columns: usize) -> usize {
    line_width.div_ceil(columns.max(1)).max(1)
}

#[derive(Default)]
pub struct Live {
    /// Where the cursor was left, counted in rows from the first line of the region.
    cursor_row: usize,
}

impl Live {
    /// Prints `finished` into the scrollback and draws `lines` below it, with the cursor on line
    /// `cursor.0` at column `cursor.1`.
    pub fn draw(&mut self, out: &mut impl Write, finished: &[String], lines: &[String], cursor: (usize, usize), size: (usize, usize)) -> std::io::Result<()> {
        let frame = self.frame(finished, lines, cursor, size);
        out.write_all(frame.as_bytes())?;
        out.flush()
    }

    /// Clears the region, for writing something else there; the next draw starts below it.
    pub fn clear(&mut self, out: &mut impl Write) -> std::io::Result<()> {
        let mut frame = String::new();
        if self.cursor_row > 0 {
            frame.push_str(&format!("\x1b[{}A", self.cursor_row));
        }
        frame.push_str("\r\x1b[J");
        self.cursor_row = 0;
        out.write_all(frame.as_bytes())?;
        out.flush()
    }

    fn frame(&mut self, finished: &[String], lines: &[String], cursor: (usize, usize), (columns, screen): (usize, usize)) -> String {
        let columns = columns.max(1);
        let mut frame = String::from("\x1b[?2026h");
        if self.cursor_row > 0 {
            frame.push_str(&format!("\x1b[{}A", self.cursor_row));
        }
        frame.push_str("\r\x1b[J");
        for line in finished {
            frame.push_str(line);
            frame.push_str("\r\n");
        }
        // A region taller than the screen would lose its top to the scrollback; the end of it
        // holds the input box, so that is what stays.
        let mut first = 0;
        let mut rows: usize = lines.iter().map(|line| rows_of(width(line), columns)).sum();
        while rows >= screen.max(2) && first + 1 < lines.len() && first < cursor.0 {
            rows -= rows_of(width(&lines[first]), columns);
            first += 1;
        }
        let mut row = 0;
        let mut cursor_row = 0;
        let mut cursor_column = 0;
        for (index, line) in lines.iter().enumerate().skip(first) {
            if index > first {
                frame.push_str("\r\n");
            }
            frame.push_str(line);
            let line_width = width(line);
            if index == cursor.0 {
                cursor_row = row + cursor.1 / columns;
                cursor_column = cursor.1 % columns;
                // The cursor stands just past a full last row: that row has to exist.
                if cursor.1 == line_width && line_width > 0 && line_width.is_multiple_of(columns) {
                    frame.push_str(" \r");
                }
            }
            row += rows_of(line_width, columns);
            if index == cursor.0 && cursor.1 == line_width && line_width > 0 && line_width.is_multiple_of(columns) {
                row += 1;
            }
        }
        let last_row = row.saturating_sub(1);
        if last_row > cursor_row {
            frame.push_str(&format!("\x1b[{}A", last_row - cursor_row));
        }
        frame.push('\r');
        if cursor_column > 0 {
            frame.push_str(&format!("\x1b[{cursor_column}C"));
        }
        frame.push_str("\x1b[?2026l");
        self.cursor_row = cursor_row;
        frame
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn width_skips_colors_and_counts_wide_characters_twice() {
        assert_eq!(width("abc"), 3);
        assert_eq!(width("修复"), 4);
        assert_eq!(width("\x1b[36mabc\x1b[0m d"), 5);
    }

    #[test]
    fn a_frame_goes_back_to_the_first_row_and_leaves_the_cursor_at_the_edit() {
        let mut live = Live::default();
        let lines = vec!["status".to_string(), "› hi".to_string()];
        let first = live.frame(&[], &lines, (1, 4), (80, 24));
        assert!(!first.contains("\x1b[1A\r\x1b[J"), "nothing to go back over: {first:?}");
        assert!(first.contains("status\r\n› hi"), "{first:?}");
        assert!(first.ends_with("\r\x1b[4C\x1b[?2026l"), "{first:?}");
        assert_eq!(live.cursor_row, 1);
        let second = live.frame(&["done".to_string()], &lines, (1, 4), (80, 24));
        assert!(second.starts_with("\x1b[?2026h\x1b[1A\r\x1b[Jdone\r\n"), "{second:?}");
    }

    #[test]
    fn long_lines_count_the_rows_they_wrap_to() {
        let mut live = Live::default();
        let long = "x".repeat(25);
        let lines = vec![long, "› ".to_string()];
        live.frame(&[], &lines, (1, 2), (10, 24));
        assert_eq!(live.cursor_row, 3, "three rows of the long line, then the input");
        let frame = live.frame(&[], &[format!("› {}", "y".repeat(25))], (0, 15), (10, 24));
        assert_eq!(live.cursor_row, 1);
        assert!(frame.contains("\x1b[1A"), "up from the third row to the second: {frame:?}");
    }

    #[test]
    fn a_cursor_past_a_full_row_gets_a_row_of_its_own() {
        let mut live = Live::default();
        let frame = live.frame(&[], &["x".repeat(10)], (0, 10), (10, 24));
        assert_eq!(live.cursor_row, 1);
        assert!(frame.contains(" \r"), "{frame:?}");
    }

    #[test]
    fn a_region_taller_than_the_screen_keeps_its_end() {
        let mut live = Live::default();
        let lines: Vec<String> = (0..30).map(|n| format!("line {n}")).collect();
        let frame = live.frame(&[], &lines, (29, 2), (80, 10));
        assert!(frame.contains("line 29"));
        assert!(!frame.contains("line 5\r\n"), "{frame:?}");
    }
}
