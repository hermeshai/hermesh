//! CSV and TSV (spreadsheet-design §10.1): read as they are, written back the way they came.
//!
//! - **Encoding**: a UTF-8 byte-order mark, else valid UTF-8, else GB18030 (what Excel saves a
//!   CSV as on a Chinese system). It is written back in the same encoding, mark and all; text
//!   that encoding cannot hold is refused.
//! - **No types**: a field is text as written. `000001` stays `000001`, a 18-digit account number
//!   keeps its digits. A field reads as a number only when the number is written exactly so.
//! - **Only edited rows change**: every other record, and every line ending, stays byte for byte.

use std::ops::Range;

use encoding_rs::Encoding;
use encoding_rs::GB18030;
use encoding_rs::UTF_8;
use encoding_rs::UTF_16BE;
use encoding_rs::UTF_16LE;

use crate::Error;
use crate::NewValue;
use crate::book::Value;
use crate::cellref::CellRef;
use crate::xmltext::format_number;

#[derive(Clone, Debug, PartialEq)]
pub struct Field {
    pub text: String,
    /// Written in quotes: written back in quotes.
    pub quoted: bool,
}

#[derive(Clone, Debug)]
pub struct Record {
    pub fields: Vec<Field>,
    /// Where it is in the decoded text, without its line ending.
    pub span: Range<usize>,
}

#[derive(Clone, Debug)]
pub struct Csv {
    pub encoding: &'static Encoding,
    pub bom: bool,
    pub delimiter: char,
    /// The line ending new rows get: the file's own.
    pub newline: &'static str,
    pub text: String,
    pub records: Vec<Record>,
}

fn sniff(text: &str, tab_by_name: bool) -> char {
    if tab_by_name {
        return '\t';
    }
    let mut best = (',', 0usize);
    for d in [',', '\t', ';', '|'] {
        let Ok(records) = parse(text, d) else {
            continue;
        };
        let counts: Vec<usize> = records
            .iter()
            .take(20)
            .filter(|r| r.fields.iter().any(|f| !f.text.trim().is_empty()))
            .map(|r| r.fields.len() - 1)
            .collect();
        let score = match counts.first() {
            Some(&n) if n > 0 => counts.iter().filter(|&&c| c == n).count() * 1000 + n,
            _ => 0,
        };
        if score > best.1 {
            best = (d, score);
        }
    }
    best.0
}

fn parse(text: &str, delimiter: char) -> Result<Vec<Record>, Error> {
    let mut records = Vec::new();
    let mut fields = Vec::new();
    let mut cur = String::new();
    let mut quoted = false;
    let mut in_quotes = false;
    let mut start = 0usize;
    let mut chars = text.char_indices().peekable();
    while let Some((i, c)) = chars.next() {
        if in_quotes {
            if c == '"' {
                if chars.peek().is_some_and(|(_, n)| *n == '"') {
                    chars.next();
                    cur.push('"');
                } else {
                    in_quotes = false;
                }
            } else {
                cur.push(c);
            }
            continue;
        }
        if quoted && c != delimiter && c != '\r' && c != '\n' {
            return Err(Error::Unsupported(
                "characters after a quoted CSV field; repair the CSV before editing".into(),
            ));
        }
        match c {
            '"' if cur.is_empty() && !quoted => {
                in_quotes = true;
                quoted = true;
            }
            c if c == delimiter => {
                fields.push(Field {
                    text: std::mem::take(&mut cur),
                    quoted,
                });
                quoted = false;
            }
            '\r' | '\n' => {
                fields.push(Field {
                    text: std::mem::take(&mut cur),
                    quoted,
                });
                quoted = false;
                records.push(Record {
                    fields: std::mem::take(&mut fields),
                    span: start..i,
                });
                if c == '\r' && chars.peek().is_some_and(|(_, n)| *n == '\n') {
                    chars.next();
                }
                start = chars.peek().map(|(j, _)| *j).unwrap_or(text.len());
            }
            c => cur.push(c),
        }
    }
    if in_quotes {
        return Err(Error::Unsupported(
            "an unterminated CSV quote; repair the CSV before editing".into(),
        ));
    }
    if start < text.len() || !cur.is_empty() || !fields.is_empty() {
        fields.push(Field { text: cur, quoted });
        records.push(Record {
            fields,
            span: start..text.len(),
        });
    }
    Ok(records)
}

impl Csv {
    /// `tab`: the file is named `.tsv`.
    pub fn parse(bytes: &[u8], tab: bool) -> Result<Self, Error> {
        let (encoding, bom, body) = if let Some(rest) = bytes.strip_prefix(&[0xEF, 0xBB, 0xBF]) {
            (UTF_8, true, rest)
        } else if let Some(rest) = bytes.strip_prefix(&[0xFF, 0xFE]) {
            // Excel's "Unicode Text" (a tab-separated file).
            (UTF_16LE, true, rest)
        } else if let Some(rest) = bytes.strip_prefix(&[0xFE, 0xFF]) {
            (UTF_16BE, true, rest)
        } else if std::str::from_utf8(bytes).is_ok() {
            (UTF_8, false, bytes)
        } else {
            (GB18030, false, bytes)
        };
        let (text, bad) = encoding.decode_without_bom_handling(body);
        if bad {
            return Err(Error::Unsupported(
                "the CSV is in an encoding her does not recognise".into(),
            ));
        }
        let text = text.into_owned();
        let newline = if text.contains("\r\n") {
            "\r\n"
        } else if text.contains('\r') {
            "\r"
        } else {
            "\n"
        };
        let delimiter = sniff(&text, tab);
        let records = parse(&text, delimiter)?;
        Ok(Self {
            encoding,
            bom,
            delimiter,
            newline,
            text,
            records,
        })
    }

    pub fn encoding_name(&self) -> &'static str {
        if self.encoding == UTF_8 {
            if self.bom { "UTF-8 (BOM)" } else { "UTF-8" }
        } else if self.encoding == UTF_16LE {
            "UTF-16 LE"
        } else if self.encoding == UTF_16BE {
            "UTF-16 BE"
        } else {
            "GBK"
        }
    }

    pub fn rows(&self) -> u32 {
        self.records.len() as u32
    }

    pub fn cols(&self) -> u32 {
        self.records
            .iter()
            .map(|r| r.fields.len())
            .max()
            .unwrap_or(0) as u32
    }

    pub fn text_at(&self, at: CellRef) -> Option<&str> {
        let r = self.records.get(at.row as usize - 1)?;
        r.fields.get(at.col as usize - 1).map(|f| f.text.as_str())
    }

    /// A field as a value: a number only when it is written exactly as that number would be.
    pub fn value(&self, at: CellRef) -> Value {
        match self.text_at(at) {
            None | Some("") => Value::Empty,
            Some(t) => match t.parse::<f64>() {
                Ok(n) if format_number(n).as_deref() == Some(t) => Value::Number(n),
                _ => Value::Text(t.to_string()),
            },
        }
    }

    fn field_text(&self, f: &Field) -> String {
        let needs = f.text.contains(self.delimiter) || f.text.contains(['"', '\n', '\r']);
        if f.quoted || needs {
            format!("\"{}\"", f.text.replace('"', "\"\""))
        } else {
            f.text.clone()
        }
    }

    fn record_text(&self, fields: &[Field]) -> String {
        fields
            .iter()
            .map(|f| self.field_text(f))
            .collect::<Vec<_>>()
            .join(&self.delimiter.to_string())
    }

    /// The text in the file's own encoding, its byte-order mark first when it had one.
    fn encode(&self, text: &str) -> Result<Vec<u8>, Error> {
        // encoding_rs only decodes UTF-16 (it encodes to UTF-8 instead): written here.
        if self.encoding == UTF_16LE || self.encoding == UTF_16BE {
            let le = self.encoding == UTF_16LE;
            let mut file = if le { vec![0xFF, 0xFE] } else { vec![0xFE, 0xFF] };
            for unit in text.encode_utf16() {
                file.extend_from_slice(&if le { unit.to_le_bytes() } else { unit.to_be_bytes() });
            }
            return Ok(file);
        }
        let (bytes, _, bad) = self.encoding.encode(text);
        if bad {
            return Err(Error::Unsupported(format!(
                "the new text has characters {} cannot hold",
                self.encoding_name()
            )));
        }
        let mut file = if self.bom {
            vec![0xEF, 0xBB, 0xBF]
        } else {
            Vec::new()
        };
        file.extend_from_slice(&bytes);
        Ok(file)
    }

    /// The file with `edits` made, every other record as it was.
    pub fn write(&self, edits: &[(CellRef, NewValue)]) -> Result<Vec<u8>, Error> {
        let mut rows: Vec<Option<Vec<Field>>> = vec![None; self.records.len()];
        let mut extra: Vec<Vec<Field>> = Vec::new();
        for (at, v) in edits {
            if at.row == 0
                || at.col == 0
                || at.row > crate::cellref::MAX_ROW
                || at.col > crate::cellref::MAX_COL
            {
                return Err(Error::Unsupported("CSV cell is outside the grid".into()));
            }
            let text = match v {
                NewValue::Number { value } => format_number(*value)
                    .ok_or_else(|| Error::Unsupported("not a finite number".into()))?,
                NewValue::Text { value } => value.clone(),
                NewValue::Bool { value } => (if *value { "TRUE" } else { "FALSE" }).to_string(),
                NewValue::Empty => String::new(),
                NewValue::Formula { .. } | NewValue::Result { .. } => {
                    return Err(Error::Unsupported("a CSV keeps values only, not formulas: write the value, or save the file as .xlsx".into()));
                }
            };
            let r = at.row as usize - 1;
            let c = at.col as usize - 1;
            let fields = if r < rows.len() {
                rows[r].get_or_insert_with(|| self.records[r].fields.clone())
            } else {
                while extra.len() <= r - rows.len() {
                    extra.push(vec![Field {
                        text: String::new(),
                        quoted: false,
                    }]);
                }
                &mut extra[r - rows.len()]
            };
            while fields.len() <= c {
                fields.push(Field {
                    text: String::new(),
                    quoted: false,
                });
            }
            fields[c].text = text;
        }
        let mut out = String::with_capacity(self.text.len() + 64);
        let mut pos = 0;
        for (i, rec) in self.records.iter().enumerate() {
            out.push_str(&self.text[pos..rec.span.start]);
            match &rows[i] {
                Some(fields) => out.push_str(&self.record_text(fields)),
                None => out.push_str(&self.text[rec.span.clone()]),
            }
            pos = rec.span.end;
        }
        out.push_str(&self.text[pos..]);
        for fields in &extra {
            if !out.is_empty() && !out.ends_with(['\n', '\r']) {
                out.push_str(self.newline);
            }
            out.push_str(&self.record_text(fields));
            out.push_str(self.newline);
        }
        let file = self.encode(&out)?;
        // Check: read back, every field as intended.
        let back = Csv::parse(&file, self.delimiter == '\t')?;
        for (i, rec) in self.records.iter().enumerate() {
            let want: Vec<&str> = rows[i]
                .as_ref()
                .unwrap_or(&rec.fields)
                .iter()
                .map(|f| f.text.as_str())
                .collect();
            let got: Vec<&str> = back
                .records
                .get(i)
                .map(|r| r.fields.iter().map(|f| f.text.as_str()).collect())
                .unwrap_or_default();
            if want != got {
                return Err(Error::SelfCheck(format!(
                    "CSV row {} reads back differently",
                    i + 1
                )));
            }
        }
        for (i, fields) in extra.iter().enumerate() {
            let want: Vec<_> = fields.iter().map(|f| &f.text).collect();
            let got: Vec<_> = back
                .records
                .get(self.records.len() + i)
                .map(|r| r.fields.iter().map(|f| &f.text).collect())
                .unwrap_or_default();
            if want != got {
                return Err(Error::SelfCheck(
                    "new CSV row reads back differently".into(),
                ));
            }
        }
        Ok(file)
    }

    /// The file with rows or columns inserted or deleted (Excel's 插入 / 删除). A CSV has no
    /// references: only the fields move. Rows not touched keep their text as written.
    pub fn reshape(&self, shift: crate::structure::Shift) -> Result<Vec<u8>, Error> {
        use crate::structure::Axis;
        let (at, count) = (shift.at as usize - 1, shift.count as usize);
        let mut lines: Vec<String> = self.records.iter().map(|r| self.text[r.span.clone()].to_string()).collect();
        let mut want: Vec<Vec<String>> = self.records.iter().map(|r| r.fields.iter().map(|f| f.text.clone()).collect()).collect();
        match shift.axis {
            Axis::Rows => {
                if at >= lines.len() {
                    return Err(Error::Unsupported("there are no rows there to move".into()));
                }
                if shift.delete {
                    let end = (at + count).min(lines.len());
                    lines.drain(at..end);
                    want.drain(at..end);
                } else {
                    lines.splice(at..at, std::iter::repeat_n(String::new(), count));
                    want.splice(at..at, std::iter::repeat_n(Vec::new(), count));
                }
            }
            Axis::Cols => {
                for (i, rec) in self.records.iter().enumerate() {
                    let mut fields = rec.fields.clone();
                    if fields.len() <= at {
                        continue;
                    }
                    if shift.delete {
                        let end = (at + count).min(fields.len());
                        fields.drain(at..end);
                    } else {
                        fields.splice(at..at, std::iter::repeat_n(Field { text: String::new(), quoted: false }, count));
                    }
                    lines[i] = self.record_text(&fields);
                    want[i] = fields.into_iter().map(|f| f.text).collect();
                }
            }
        }
        let mut out = lines.join(self.newline);
        if self.text.ends_with(['\n', '\r']) && !out.is_empty() {
            out.push_str(self.newline);
        }
        let file = self.encode(&out)?;
        // Check: read back, every row as intended (an empty row reads as one empty field).
        let back = Csv::parse(&file, self.delimiter == '\t')?;
        let blank = |f: &[String]| f.iter().all(String::is_empty);
        for (i, w) in want.iter().enumerate() {
            let got: Vec<String> = back.records.get(i).map(|r| r.fields.iter().map(|f| f.text.clone()).collect()).unwrap_or_default();
            if *w != got && !(blank(w) && blank(&got)) {
                return Err(Error::SelfCheck(format!("CSV row {} reads back differently", i + 1)));
            }
        }
        Ok(file)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn at(s: &str) -> CellRef {
        CellRef::parse(s).unwrap()
    }

    #[test]
    fn reads_quotes_and_keeps_text() {
        let c = Csv::parse("代码,名称,收盘\r\n000001,\"平安银行, A\",12.50\r\n600519,\"贵州\"\"茅台\"\"\",1688\r\n".as_bytes(), false).unwrap();
        assert_eq!(c.delimiter, ',');
        assert_eq!(c.newline, "\r\n");
        assert_eq!(c.rows(), 3);
        assert_eq!(c.value(at("A2")), Value::Text("000001".into()));
        assert_eq!(c.value(at("B2")), Value::Text("平安银行, A".into()));
        assert_eq!(c.value(at("C2")), Value::Text("12.50".into()));
        assert_eq!(c.value(at("C3")), Value::Number(1688.0));
        assert_eq!(c.value(at("B3")), Value::Text("贵州\"茅台\"".into()));
    }

    #[test]
    fn writes_only_the_edited_rows_in_the_same_encoding() {
        let (gbk, _, _) = GB18030.encode("代码;名称\n000001;平安银行\n600519;贵州茅台\n");
        let c = Csv::parse(&gbk, false).unwrap();
        assert_eq!(c.encoding_name(), "GBK");
        assert_eq!(c.delimiter, ';');
        let out = c
            .write(&[
                (
                    at("B3"),
                    NewValue::Text {
                        value: "茅台; 白酒".into(),
                    },
                ),
                (at("A5"), NewValue::Number { value: 1.5 }),
            ])
            .unwrap();
        let (text, _, _) = GB18030.decode(&out);
        assert_eq!(
            text,
            "代码;名称\n000001;平安银行\n600519;\"茅台; 白酒\"\n\n1.5\n"
        );
    }

    #[test]
    fn keeps_the_bom_and_refuses_what_gbk_cannot_hold() {
        let c = Csv::parse("\u{feff}a,b\n1,2\n".as_bytes(), false).unwrap();
        let out = c
            .write(&[(
                at("A2"),
                NewValue::Text {
                    value: "😀".into()
                },
            )])
            .unwrap();
        assert!(out.starts_with(&[0xEF, 0xBB, 0xBF]));
        let (gbk, _, _) = GB18030.encode("a,中\n");
        let c = Csv::parse(&gbk, false).unwrap();
        assert!(
            c.write(&[(
                at("A1"),
                NewValue::Formula {
                    text: "=1".into(),
                    cached: None
                }
            )])
            .is_err()
        );
    }
}

#[cfg(test)]
#[test]
fn quoted_delimiters_and_new_rows_are_checked() {
    let csv = Csv::parse(b"\"a;b;c\",value\r\nx,2\r\n", false).unwrap();
    assert_eq!(csv.delimiter, ',');
    let bytes = csv
        .write(&[(
            CellRef::new(4, 2),
            NewValue::Text {
                value: "new,field".into(),
            },
        )])
        .unwrap();
    let back = Csv::parse(&bytes, false).unwrap();
    assert_eq!(back.text_at(CellRef::new(4, 2)), Some("new,field"));
    assert_eq!(back.text_at(CellRef::new(3, 1)), Some(""));
    assert!(bytes.starts_with(b"\"a;b;c\",value\r\nx,2\r\n"));
    assert!(Csv::parse(b"a,b\n\"unclosed,b", false).is_err());
    assert!(Csv::parse(b"a,b\n\"closed\"junk,b", false).is_err());
}

#[cfg(test)]
#[test]
fn utf16_is_read_by_its_mark_and_written_back_so() {
    let le = |text: &str| [0xFF, 0xFE].into_iter().chain(text.encode_utf16().flat_map(u16::to_le_bytes)).collect::<Vec<u8>>();
    let csv = Csv::parse(&le("代码\t名称\r\n000001\t平安银行\r\n"), false).unwrap();
    assert_eq!(csv.delimiter, '\t');
    assert_eq!(csv.text_at(CellRef::new(2, 2)), Some("平安银行"));
    let bytes = csv.write(&[(CellRef::new(2, 2), NewValue::Text { value: "招商银行😀".into() })]).unwrap();
    assert_eq!(bytes, le("代码\t名称\r\n000001\t招商银行😀\r\n"));
    let be: Vec<u8> = [0xFE, 0xFF].into_iter().chain("a,b\n".encode_utf16().flat_map(u16::to_be_bytes)).collect();
    let csv = Csv::parse(&be, false).unwrap();
    assert_eq!(csv.text_at(CellRef::new(1, 2)), Some("b"));
    assert_eq!(csv.write(&[(CellRef::new(1, 1), NewValue::Text { value: "x".into() })]).unwrap()[..2], [0xFE, 0xFF]);
}
