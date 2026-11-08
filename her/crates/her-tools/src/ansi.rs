//! Removes terminal escape sequences from command output, keeping the text as it reads:
//! newlines and tabs stay, `\r\n` becomes `\n`, other control characters go.
//!
//! It works as a stream, so a sequence split across two reads is still removed, and the full
//! output saved to a file matches the text the model was shown.

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
enum State {
    #[default]
    Text,
    /// After ESC.
    Escape,
    /// ESC followed by intermediate bytes (e.g. a character set selection).
    EscapeIntermediate,
    /// Control sequence: ESC [ parameters final.
    Csi,
    /// Operating system command (titles, hyperlinks), up to BEL or ESC \.
    Osc,
    /// Device control and similar strings, up to ESC \.
    Str,
    /// ESC inside an OSC or a string: ends it when followed by '\'.
    StrEscape,
}

#[derive(Clone, Debug, Default)]
pub struct AnsiStripper {
    state: State,
}

impl AnsiStripper {
    pub fn new() -> Self {
        Self::default()
    }

    /// Appends the text of `input` to `out`.
    pub fn feed(&mut self, input: &[u8], out: &mut Vec<u8>) {
        for &byte in input {
            // A newline always survives, even inside a broken sequence, so line numbers of
            // the stripped text match the original.
            if byte == b'\n' {
                self.state = State::Text;
                out.push(byte);
                continue;
            }
            self.state = match self.state {
                State::Text => match byte {
                    0x1b => State::Escape,
                    b'\t' => {
                        out.push(byte);
                        State::Text
                    }
                    0x00..=0x1f | 0x7f => State::Text,
                    _ => {
                        out.push(byte);
                        State::Text
                    }
                },
                State::Escape => match byte {
                    b'[' => State::Csi,
                    b']' => State::Osc,
                    b'P' | b'X' | b'^' | b'_' => State::Str,
                    0x20..=0x2f => State::EscapeIntermediate,
                    0x1b => State::Escape,
                    _ => State::Text,
                },
                State::EscapeIntermediate => match byte {
                    0x20..=0x2f => State::EscapeIntermediate,
                    _ => State::Text,
                },
                State::Csi => match byte {
                    0x20..=0x3f => State::Csi,
                    0x1b => State::Escape,
                    _ => State::Text,
                },
                State::Osc => match byte {
                    0x07 => State::Text,
                    0x1b => State::StrEscape,
                    _ => State::Osc,
                },
                State::Str => match byte {
                    0x1b => State::StrEscape,
                    _ => State::Str,
                },
                State::StrEscape => match byte {
                    b'\\' => State::Text,
                    _ => State::Str,
                },
            };
        }
    }
}

/// Stripped text of a complete output.
pub fn strip(input: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(input.len());
    AnsiStripper::new().feed(input, &mut out);
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn text(input: &[u8]) -> String {
        String::from_utf8(strip(input)).unwrap()
    }

    #[test]
    fn keeps_text_tabs_and_newlines() {
        assert_eq!(text(b"a\tb\r\nc\x1b[31mred\x1b[0m\n"), "a\tb\ncred\n");
        assert_eq!(text(b"\x1b[1;32m    Finished\x1b[m dev"), "    Finished dev");
        assert_eq!(text("你好\x1b[1m世界".as_bytes()), "你好世界");
        // Progress bars redraw with a bare CR; the redraws run together.
        assert_eq!(text(b"10%\r20%\r100%\n"), "10%20%100%\n");
    }

    #[test]
    fn removes_titles_links_and_charsets() {
        assert_eq!(text(b"\x1b]0;title\x07rest"), "rest");
        assert_eq!(text(b"\x1b]8;;https://x.y\x1b\\link\x1b]8;;\x1b\\ end"), "link end");
        assert_eq!(text(b"\x1b(Bok\x1b=\x1b>"), "ok");
        assert_eq!(text(b"\x1bPq#0;2;0;0;0\x1b\\after"), "after");
    }

    #[test]
    fn a_sequence_split_across_reads_is_still_removed() {
        let mut stripper = AnsiStripper::new();
        let mut out = Vec::new();
        stripper.feed(b"one \x1b[3", &mut out);
        stripper.feed(b"1mred\x1b", &mut out);
        stripper.feed(b"[0m two", &mut out);
        assert_eq!(String::from_utf8(out).unwrap(), "one red two");
    }

    #[test]
    fn newlines_survive_broken_sequences() {
        assert_eq!(text(b"\x1b[3\nx\x1b]0;t\ny"), "\nx\ny");
    }
}
