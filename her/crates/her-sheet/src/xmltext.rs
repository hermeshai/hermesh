//! Escaping for the text we write into SpreadsheetML, and unescaping for
//! the text we read back.
//!
//! Two layers: XML entities, and the OOXML `_xHHHH_` escape for characters
//! XML 1.0 cannot carry (ST_Xstring). A literal `_x0041_` in a cell must be
//! written as `_x005F_x0041_`, or Excel would read it as `A`.

pub fn escape_xml(s: &str, attr: bool) -> String {
    let mut out = String::with_capacity(s.len() + 8);
    for c in s.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' if attr => out.push_str("&quot;"),
            '\n' if attr => out.push_str("&#10;"),
            '\t' if attr => out.push_str("&#9;"),
            _ => out.push(c),
        }
    }
    out
}

/// Cell text to ST_Xstring: control characters other than tab and line
/// feed become `_xHHHH_`, and an underscore that starts something looking
/// like an escape is itself escaped.
pub fn encode_xstring(s: &str) -> String {
    let chars: Vec<char> = s.chars().collect();
    let mut out = String::with_capacity(s.len());
    for (i, &c) in chars.iter().enumerate() {
        if c == '_' && looks_like_escape(&chars[i..]) {
            out.push_str("_x005F_");
        } else if (c as u32) < 0x20 && c != '\t' && c != '\n' || c == '\u{FFFE}' || c == '\u{FFFF}'
        {
            out.push_str(&format!("_x{:04X}_", c as u32));
        } else {
            out.push(c);
        }
    }
    out
}

fn looks_like_escape(s: &[char]) -> bool {
    s.len() >= 7 && s[1] == 'x' && s[2..6].iter().all(|c| c.is_ascii_hexdigit()) && s[6] == '_'
}

/// Reverses `encode_xstring`.
pub fn decode_xstring(s: &str) -> String {
    if !s.contains("_x") {
        return s.to_string();
    }
    let chars: Vec<char> = s.chars().collect();
    let mut out = String::with_capacity(s.len());
    let mut i = 0;
    while i < chars.len() {
        if chars[i] == '_' && looks_like_escape(&chars[i..]) {
            let hex: String = chars[i + 2..i + 6].iter().collect();
            if let Some(c) = u32::from_str_radix(&hex, 16).ok().and_then(char::from_u32) {
                out.push(c);
                i += 7;
                continue;
            }
        }
        out.push(chars[i]);
        i += 1;
    }
    out
}

/// XML entity unescape (the five named entities and character references).
pub fn unescape_xml(s: &str) -> String {
    if !s.contains('&') {
        return s.to_string();
    }
    let mut out = String::with_capacity(s.len());
    let mut rest = s;
    while let Some(i) = rest.find('&') {
        out.push_str(&rest[..i]);
        let tail = &rest[i..];
        let Some(end) = tail.find(';') else {
            out.push_str(tail);
            return out;
        };
        let ent = &tail[1..end];
        let ch = match ent {
            "amp" => Some('&'),
            "lt" => Some('<'),
            "gt" => Some('>'),
            "quot" => Some('"'),
            "apos" => Some('\''),
            _ if ent.starts_with("#x") || ent.starts_with("#X") => {
                u32::from_str_radix(&ent[2..], 16)
                    .ok()
                    .and_then(char::from_u32)
            }
            _ if ent.starts_with('#') => ent[1..].parse().ok().and_then(char::from_u32),
            _ => None,
        };
        match ch {
            Some(c) => out.push(c),
            None => out.push_str(&tail[..=end]),
        }
        rest = &tail[end + 1..];
    }
    out.push_str(rest);
    out
}

/// A reader whose positions are offsets into `xml`. quick-xml skips a
/// UTF-8 byte-order mark and counts positions from after it, so a file
/// that starts with one would put every splice three bytes off.
pub fn reader(xml: &[u8]) -> (quick_xml::Reader<&[u8]>, usize) {
    let base = if xml.starts_with(&[0xEF, 0xBB, 0xBF]) {
        3
    } else {
        0
    };
    (quick_xml::Reader::from_reader(&xml[base..]), base)
}

/// Shortest text that reads back as the same f64.
pub fn format_number(v: f64) -> Option<String> {
    v.is_finite().then(|| {
        let a = v.abs();
        let s = if a != 0.0 && (a >= 1e15 || a < 1e-6) {
            format!("{v:e}").replace('e', "E")
        } else {
            format!("{v}")
        };
        if s == "-0" { "0".to_string() } else { s }
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn xstring_round_trip() {
        for s in [
            "plain",
            "a_x0041_b",
            "tab\there",
            "cr\r\nlf",
            "bell\u{7}",
            "中文_x",
            "_x005F_",
        ] {
            assert_eq!(decode_xstring(&encode_xstring(s)), s, "{s:?}");
        }
        assert_eq!(encode_xstring("a_x0041_b"), "a_x005F_x0041_b");
    }

    #[test]
    fn entities() {
        assert_eq!(
            unescape_xml("a&amp;b&lt;&#65;&#x42;&bogus;"),
            "a&b<AB&bogus;"
        );
    }

    #[test]
    fn numbers() {
        assert_eq!(format_number(1.0).unwrap(), "1");
        assert_eq!(format_number(0.1 + 0.2).unwrap(), "0.30000000000000004");
        assert_eq!(format_number(-0.0).unwrap(), "0");
        assert!(format_number(f64::NAN).is_none());
    }
}
