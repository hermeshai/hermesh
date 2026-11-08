//! A workbook as read (design §5.1, §5.2): its sheets, each cell's stored value and formula
//! (shared formulas written out for every member), the shared strings and the names. What the
//! calculation, the agent and the grid read; writes go through `patch` on the original bytes.

use std::collections::BTreeMap;
use std::collections::HashMap;

use quick_xml::events::Event;

use crate::Error;
use crate::cellref::CellRef;
use crate::formula::translate;
use crate::package::Package;
use crate::package::SheetKind;
use crate::package::WorkbookInfo;
use crate::scan::CellInfo;
use crate::scan::FormulaKind;
use crate::scan::SheetIndex;
use crate::scan::scan;
use crate::styles::CellStyle;
use crate::styles::Styles;
use crate::styles::format_number;
use crate::xmltext::decode_xstring;
use crate::xmltext::unescape_xml;

/// A cell's value, stored or computed.
#[derive(Clone, Debug, PartialEq)]
pub enum Value {
    Number(f64),
    Text(String),
    Bool(bool),
    /// `#N/A`, `#REF!`…
    Error(String),
    Empty,
}

impl Value {
    pub fn as_number(&self) -> Option<f64> {
        match self {
            Value::Number(n) => Some(*n),
            _ => None,
        }
    }

    /// Equal, numbers within a relative `tol`.
    pub fn close_to(&self, other: &Value, tol: f64) -> bool {
        match (self, other) {
            (Value::Number(a), Value::Number(b)) => {
                (a - b).abs() <= tol * a.abs().max(b.abs()).max(1.0)
            }
            (Value::Empty, Value::Text(t)) | (Value::Text(t), Value::Empty) => t.is_empty(),
            _ => self == other,
        }
    }
}

pub struct Sheet {
    pub name: String,
    pub kind: SheetKind,
    /// Worksheets only.
    pub index: Option<SheetIndex>,
    /// Every formula cell's text as stored (no `=`), shared formulas written out.
    pub formulas: BTreeMap<CellRef, String>,
    /// The charts drawn on a worksheet (spreadsheet-design §4.1).
    pub charts: Vec<crate::chart::Chart>,
}

pub struct Book {
    pub tables: Vec<crate::tables::Table>,
    pub info: WorkbookInfo,
    pub strings: Vec<String>,
    pub styles: Styles,
    pub sheets: Vec<Sheet>,
}

impl Book {
    pub fn open(bytes: &[u8]) -> Result<Self, Error> {
        let pkg = Package::open(bytes.to_vec())?;
        let info = WorkbookInfo::load(&pkg)?;
        let strings = shared_strings(&pkg, &info)?;
        let styles = match &info.styles {
            Some(part) => match pkg.read(part)? {
                Some(xml) => Styles::parse(&xml)?,
                None => Styles::default(),
            },
            None => Styles::default(),
        };
        let mut sheets = Vec::with_capacity(info.sheets.len());
        for entry in &info.sheets {
            let index = match (&entry.kind, &entry.part) {
                (SheetKind::Worksheet, Some(part)) => match pkg.read(part)? {
                    Some(xml) => Some(scan(&xml)?),
                    None => None,
                },
                _ => None,
            };
            let formulas = index.as_ref().map(formulas_of).unwrap_or_default();
            sheets.push(Sheet {
                name: entry.name.clone(),
                kind: entry.kind.clone(),
                index,
                formulas,
                charts: Vec::new(),
            });
        }
        let tables = crate::tables::load(&pkg, &info)?;
        let mut book = Self {
            tables,
            info,
            strings,
            styles,
            sheets,
        };
        // After the cells are in: a chart reads its numbers from them.
        for i in 0..book.sheets.len() {
            if book.sheets[i].kind == SheetKind::Worksheet {
                book.sheets[i].charts = crate::chart::load(&pkg, &book, i);
            }
        }
        Ok(book)
    }

    pub fn sheet_index(&self, name: &str) -> Option<usize> {
        self.sheets.iter().position(|s| s.name == name)
    }

    pub fn cell(&self, sheet: usize, at: CellRef) -> Option<&CellInfo> {
        self.sheets.get(sheet)?.index.as_ref()?.cell(at)
    }

    /// The value the file holds for a cell: for a formula, the result Excel saved.
    pub fn value(&self, sheet: usize, at: CellRef) -> Value {
        match self.cell(sheet, at) {
            Some(c) => value_of(c, &self.strings),
            None => Value::Empty,
        }
    }

    pub fn style(&self, sheet: usize, at: CellRef) -> CellStyle {
        let s = self
            .cell(sheet, at)
            .and_then(|c| c.s.clone())
            .or_else(|| self.sheets.get(sheet)?.index.as_ref()?.default_style(at));
        self.styles.get(s.as_deref())
    }

    /// What Excel shows for a value in a cell's number format, and a colour the format asks for.
    pub fn display(&self, value: &Value, style: &CellStyle) -> (String, Option<&'static str>) {
        match value {
            Value::Number(n) => format_number(*n, &style.num_fmt, self.info.date1904),
            Value::Text(t) => (t.clone(), None),
            Value::Bool(b) => ((if *b { "TRUE" } else { "FALSE" }).to_string(), None),
            Value::Error(e) => (e.clone(), None),
            Value::Empty => (String::new(), None),
        }
    }

    /// How a person would name a cell: the nearest text to its left in the row, else above it in
    /// the column ("企业价值", "WACC"). Numbers and dates are not names.
    pub fn label(&self, sheet: usize, at: CellRef) -> Option<String> {
        let text = |c: CellRef| match self.value(sheet, c) {
            Value::Text(t) if t.trim().chars().any(|c| c.is_alphabetic()) => Some(
                t.trim()
                    .trim_end_matches(['=', ':', '：'])
                    .trim()
                    .to_string(),
            ),
            _ => None,
        };
        (1..at.col)
            .rev()
            .take(20)
            .find_map(|c| text(CellRef::new(at.row, c)))
            .or_else(|| {
                (1..at.row)
                    .rev()
                    .take(20)
                    .find_map(|r| text(CellRef::new(r, at.col)))
            })
    }

    pub fn formula(&self, sheet: usize, at: CellRef) -> Option<&str> {
        self.sheets
            .get(sheet)?
            .formulas
            .get(&at)
            .map(String::as_str)
    }
}

pub fn value_of(c: &CellInfo, strings: &[String]) -> Value {
    let v = c.v.as_deref();
    match c.t.as_deref() {
        Some("s") => v
            .and_then(|i| i.trim().parse::<usize>().ok())
            .and_then(|i| strings.get(i).cloned())
            .map(Value::Text)
            .unwrap_or(Value::Empty),
        Some("inlineStr") => Value::Text(c.inline.clone().unwrap_or_default()),
        Some("str") | Some("d") => v
            .map(|s| Value::Text(decode_xstring(s)))
            .unwrap_or(Value::Empty),
        Some("b") => v
            .map(|s| Value::Bool(s.trim() == "1" || s.trim() == "true"))
            .unwrap_or(Value::Empty),
        Some("e") => v
            .map(|s| Value::Error(s.trim().to_string()))
            .unwrap_or(Value::Empty),
        _ => match v.map(str::trim) {
            Some(s) if !s.is_empty() => s
                .parse()
                .map(Value::Number)
                .unwrap_or_else(|_| Value::Text(s.to_string())),
            _ => Value::Empty,
        },
    }
}

/// Formula text for every formula cell; a shared formula's members get the master's text
/// moved to them, as Excel reads it.
fn formulas_of(ix: &SheetIndex) -> BTreeMap<CellRef, String> {
    let mut masters: HashMap<&str, (CellRef, &str)> = HashMap::new();
    for c in ix.cells() {
        if let Some(f) = &c.formula
            && let FormulaKind::Shared {
                si, master: true, ..
            } = &f.kind
        {
            masters.insert(si, (c.at, &f.text));
        }
    }
    let mut out = BTreeMap::new();
    for c in ix.cells() {
        let Some(f) = &c.formula else { continue };
        let text = match &f.kind {
            FormulaKind::Shared {
                si, master: false, ..
            } => match masters.get(si.as_str()) {
                Some((at, text)) => {
                    match translate(
                        text,
                        c.at.row as i64 - at.row as i64,
                        c.at.col as i64 - at.col as i64,
                    ) {
                        Ok(t) => t,
                        Err(_) => continue,
                    }
                }
                None => continue,
            },
            _ => f.text.clone(),
        };
        if !text.is_empty() {
            out.insert(c.at, text);
        }
    }
    out
}

/// The shared string table, as text (runs joined, phonetic hints left out).
pub fn shared_strings(p: &Package, wb: &WorkbookInfo) -> Result<Vec<String>, Error> {
    let Some(part) = &wb.shared_strings else {
        return Ok(vec![]);
    };
    let Some(xml) = p.read(part)? else {
        return Ok(vec![]);
    };
    let (mut rd, base) = crate::xmltext::reader(&xml[..]);
    let mut out = Vec::new();
    let mut cur: Option<String> = None;
    let mut t_start = None;
    let mut rph = false;
    loop {
        let start = rd.buffer_position() as usize + base;
        let ev = rd.read_event()?;
        let end = rd.buffer_position() as usize + base;
        match ev {
            Event::Start(e) => match e.local_name().as_ref() {
                "si" => cur = Some(String::new()),
                "rPh" => rph = true,
                "t" if !rph => t_start = Some(end),
                _ => {}
            },
            Event::Empty(e) if e.local_name().as_ref() == "si" => out.push(String::new()),
            Event::End(e) => match e.local_name().as_ref() {
                "t" if t_start.is_some() => {
                    let raw = String::from_utf8_lossy(&xml[t_start.take().unwrap()..start]);
                    if let Some(c) = cur.as_mut() {
                        c.push_str(&decode_xstring(&unescape_xml(&raw)));
                    }
                }
                "rPh" => rph = false,
                "si" => out.push(cur.take().unwrap_or_default()),
                _ => {}
            },
            Event::Eof => break,
            _ => {}
        }
    }
    Ok(out)
}
