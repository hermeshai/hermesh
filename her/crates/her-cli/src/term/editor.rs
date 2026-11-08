//! The input box: text with a cursor, lines, wide characters and history. No terminal in here.

use unicode_width::UnicodeWidthStr;

#[derive(Default)]
pub struct Editor {
    text: String,
    /// A byte offset, always on a character boundary.
    cursor: usize,
    history: Vec<String>,
    /// Which history entry is shown, and what was being written before going there.
    browsing: Option<(usize, String)>,
}

fn prev(text: &str, at: usize) -> usize {
    text[..at].char_indices().next_back().map_or(0, |(i, _)| i)
}

fn next(text: &str, at: usize) -> usize {
    text[at..].chars().next().map_or(at, |c| at + c.len_utf8())
}

impl Editor {
    pub fn with_history(history: Vec<String>) -> Self {
        Self { history, ..Self::default() }
    }

    pub fn text(&self) -> &str {
        &self.text
    }

    pub fn is_empty(&self) -> bool {
        self.text.is_empty()
    }

    pub fn set(&mut self, text: &str) {
        self.text = text.to_string();
        self.cursor = self.text.len();
        self.browsing = None;
    }

    /// Takes the text out, for sending.
    pub fn take(&mut self) -> String {
        self.cursor = 0;
        self.browsing = None;
        std::mem::take(&mut self.text)
    }

    pub fn remember(&mut self, text: &str) {
        if self.history.last().map(String::as_str) != Some(text) {
            self.history.push(text.to_string());
        }
    }

    pub fn insert(&mut self, text: &str) {
        self.text.insert_str(self.cursor, text);
        self.cursor += text.len();
        self.browsing = None;
    }

    pub fn backspace(&mut self) {
        if self.cursor > 0 {
            let start = prev(&self.text, self.cursor);
            self.text.replace_range(start..self.cursor, "");
            self.cursor = start;
        }
    }

    pub fn delete(&mut self) {
        if self.cursor < self.text.len() {
            let end = next(&self.text, self.cursor);
            self.text.replace_range(self.cursor..end, "");
        }
    }

    pub fn left(&mut self) {
        self.cursor = prev(&self.text, self.cursor);
    }

    pub fn right(&mut self) {
        self.cursor = next(&self.text, self.cursor);
    }

    fn word_start(&self) -> usize {
        let before = &self.text[..self.cursor];
        let trimmed = before.trim_end();
        trimmed.rfind(char::is_whitespace).map_or(0, |i| next(trimmed, i))
    }

    pub fn word_left(&mut self) {
        self.cursor = self.word_start();
    }

    pub fn word_right(&mut self) {
        let after = &self.text[self.cursor..];
        let skipped = after.len() - after.trim_start().len();
        let word = after[skipped..].find(char::is_whitespace).unwrap_or(after.len() - skipped);
        self.cursor += skipped + word;
    }

    pub fn delete_word(&mut self) {
        let start = self.word_start();
        self.text.replace_range(start..self.cursor, "");
        self.cursor = start;
    }

    fn line_start(&self) -> usize {
        self.text[..self.cursor].rfind('\n').map_or(0, |i| i + 1)
    }

    fn line_end(&self) -> usize {
        self.text[self.cursor..].find('\n').map_or(self.text.len(), |i| self.cursor + i)
    }

    pub fn home(&mut self) {
        self.cursor = self.line_start();
    }

    pub fn end(&mut self) {
        self.cursor = self.line_end();
    }

    pub fn kill_to_start(&mut self) {
        let start = self.line_start();
        self.text.replace_range(start..self.cursor, "");
        self.cursor = start;
    }

    pub fn kill_to_end(&mut self) {
        let end = self.line_end();
        self.text.replace_range(self.cursor..end, "");
    }

    /// One line up in the text; on the first line, the entry before in the history.
    pub fn up(&mut self) {
        let start = self.line_start();
        if start > 0 {
            let column = self.text[start..self.cursor].width();
            let above = prev(&self.text, start);
            let above_start = self.text[..above].rfind('\n').map_or(0, |i| i + 1);
            self.cursor = at_column(&self.text, above_start, above, column);
            return;
        }
        let index = match &self.browsing {
            Some((0, _)) => return,
            Some((index, _)) => index - 1,
            None if self.history.is_empty() => return,
            None => self.history.len() - 1,
        };
        let draft = self.browsing.take().map_or_else(|| self.text.clone(), |(_, draft)| draft);
        self.text = self.history[index].clone();
        self.cursor = self.text.len();
        self.browsing = Some((index, draft));
    }

    /// One line down in the text; on the last line, the entry after in the history.
    pub fn down(&mut self) {
        let end = self.line_end();
        if end < self.text.len() {
            let column = self.text[self.line_start()..self.cursor].width();
            let below = end + 1;
            let below_end = self.text[below..].find('\n').map_or(self.text.len(), |i| below + i);
            self.cursor = at_column(&self.text, below, below_end, column);
            return;
        }
        let Some((index, draft)) = self.browsing.take() else { return };
        if index + 1 < self.history.len() {
            self.text = self.history[index + 1].clone();
            self.browsing = Some((index + 1, draft));
        } else {
            self.text = draft;
        }
        self.cursor = self.text.len();
    }

    pub fn lines(&self) -> Vec<&str> {
        self.text.split('\n').collect()
    }

    /// The line the cursor is on, and its width up to the cursor.
    pub fn cursor_position(&self) -> (usize, usize) {
        let before = &self.text[..self.cursor];
        let line = before.matches('\n').count();
        (line, before[self.line_start()..].width())
    }

    /// The word being written, for completing: what comes after the last space.
    pub fn last_word(&self) -> &str {
        let before = &self.text[..self.cursor];
        before.rfind(char::is_whitespace).map_or(before, |i| &before[next(before, i)..])
    }
}

/// The offset in `start..end` closest to `column`, on a character boundary.
fn at_column(text: &str, start: usize, end: usize, column: usize) -> usize {
    let mut width = 0;
    for (index, c) in text[start..end].char_indices() {
        if width >= column {
            return start + index;
        }
        width += unicode_width::UnicodeWidthChar::width(c).unwrap_or(0);
    }
    end
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wide_characters_take_two_columns_and_move_as_one() {
        let mut e = Editor::default();
        e.insert("修复a");
        assert_eq!(e.cursor_position(), (0, 5));
        e.left();
        assert_eq!(e.cursor_position(), (0, 4));
        e.left();
        assert_eq!(e.cursor_position(), (0, 2));
        e.backspace();
        assert_eq!(e.text(), "复a");
        e.delete();
        assert_eq!(e.text(), "a");
    }

    #[test]
    fn lines_keep_the_cursor_column() {
        let mut e = Editor::default();
        e.insert("first line\nab\nthird line");
        assert_eq!(e.cursor_position(), (2, 10));
        e.up();
        assert_eq!(e.cursor_position(), (1, 2));
        e.up();
        assert_eq!(e.cursor_position(), (0, 2));
        e.down();
        e.down();
        assert_eq!(e.cursor_position(), (2, 2));
        e.home();
        assert_eq!(e.cursor_position(), (2, 0));
        e.end();
        e.kill_to_start();
        assert_eq!(e.lines(), vec!["first line", "ab", ""]);
    }

    #[test]
    fn history_comes_back_and_keeps_the_draft() {
        let mut e = Editor::with_history(vec!["one".into(), "two".into()]);
        e.insert("draft");
        e.up();
        assert_eq!(e.text(), "two");
        e.up();
        assert_eq!(e.text(), "one");
        e.up();
        assert_eq!(e.text(), "one");
        e.down();
        assert_eq!(e.text(), "two");
        e.down();
        assert_eq!(e.text(), "draft");
    }

    #[test]
    fn words_are_what_stands_between_spaces() {
        let mut e = Editor::default();
        e.insert("read src/lib.rs  now");
        e.delete_word();
        assert_eq!(e.text(), "read src/lib.rs  ");
        assert_eq!(e.last_word(), "");
        e.delete_word();
        assert_eq!(e.text(), "read ");
        e.insert("src/li");
        assert_eq!(e.last_word(), "src/li");
        e.word_left();
        assert_eq!(e.cursor_position(), (0, 5));
        e.word_right();
        assert_eq!(e.cursor_position(), (0, 11));
    }
}
