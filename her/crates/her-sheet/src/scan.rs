//! One pass over a worksheet part that records where every row and cell is,
//! by byte offset, without building a document. Edits are then splices
//! into the original bytes, so everything we do not touch stays as it was.

use std::ops::Range;

use quick_xml::events::BytesStart;

use quick_xml::events::Event;

use crate::cellref::CellRef;

use crate::Error;
use crate::cellref::Rect;
use crate::package::attrs;
use crate::xmltext::decode_xstring;
use crate::xmltext::unescape_xml;

#[derive(Clone, Debug, PartialEq)]
pub enum FormulaKind {
    Normal,
    /// `t="shared"`. The master carries the text and `ref`; the others only `si`.
    Shared {
        si: String,
        master: bool,
        range: Option<Rect>,
    },
    Array {
        range: Option<Rect>,
    },
    DataTable,
}

#[derive(Clone, Debug)]
pub struct FormulaInfo {
    pub kind: FormulaKind,
    pub text: String,
}

#[derive(Clone, Debug)]
pub struct CellInfo {
    pub at: CellRef,
    pub has_r: bool,
    pub span: Range<usize>,
    pub s: Option<String>,
    pub t: Option<String>,
    /// `cm` (dynamic array) or `vm` (rich value: pictures in cells, data types).
    pub has_metadata: bool,
    pub formula: Option<FormulaInfo>,
    /// Byte range of the `<f>` element.
    pub f_span: Option<Range<usize>>,
    pub v: Option<String>,
    pub inline: Option<String>,
}

#[derive(Clone, Debug)]
pub struct RowInfo {
    pub r: u32,
    pub has_r: bool,
    pub span: Range<usize>,
    /// Where the open tag ends; for a self-closing row, the whole tag.
    pub open_tag: Range<usize>,
    pub self_closing: bool,
    /// Start of `</row>`.
    pub close_start: usize,
    pub style: Option<String>,
    pub custom_format: bool,
    /// In points, when the row's height is its own.
    pub height: Option<f64>,
    pub hidden: bool,
    pub cells: Vec<CellInfo>,
}

#[derive(Clone, Debug)]
pub struct ColInfo {
    pub min: u32,
    pub max: u32,
    pub style: Option<String>,
    /// In characters of the default font, as Excel stores it.
    pub width: Option<f64>,
    pub hidden: bool,
}

#[derive(Debug, Default)]
pub struct SheetIndex {
    /// Namespace prefix of SpreadsheetML elements (`x` in `<x:c>`), if any.
    pub prefix: Option<String>,
    pub dimension: Option<(Range<usize>, Option<Rect>)>,
    /// `<sheetData>` open tag (whole tag when self-closing).
    pub sheet_data_open: Range<usize>,
    pub sheet_data_self_closing: bool,
    /// Start of `</sheetData>`.
    pub sheet_data_close: usize,
    pub rows: Vec<RowInfo>,
    pub cols: Vec<ColInfo>,
    pub merges: Vec<Rect>,
    pub protected: bool,
    /// Frozen panes: rows above and columns left of the split.
    pub frozen: Option<(u32, u32)>,
    /// `<sheetFormatPr>`: column width in characters, row height in points.
    pub default_width: Option<f64>,
    pub default_height: Option<f64>,
    /// Rows and cells are in ascending order (Excel requires it; we rely on it to insert).
    pub ordered: bool,
}

impl SheetIndex {
    pub fn cell(&self, at: CellRef) -> Option<&CellInfo> {
        let row = self.row(at.row)?;
        row.cells.iter().find(|c| c.at == at)
    }

    pub fn row(&self, r: u32) -> Option<&RowInfo> {
        if self.ordered {
            self.rows
                .binary_search_by_key(&r, |x| x.r)
                .ok()
                .map(|i| &self.rows[i])
        } else {
            self.rows.iter().find(|x| x.r == r)
        }
    }

    pub fn cells(&self) -> impl Iterator<Item = &CellInfo> {
        self.rows.iter().flat_map(|r| r.cells.iter())
    }

    /// Style a new cell gets when Excel would show one there: the row's
    /// style if the row has a custom format, else the column's.
    pub fn default_style(&self, at: CellRef) -> Option<String> {
        if let Some(r) = self.row(at.row)
            && r.custom_format
            && r.style.is_some()
        {
            return r.style.clone();
        }
        self.cols
            .iter()
            .find(|c| at.col >= c.min && at.col <= c.max)
            .and_then(|c| c.style.clone())
    }
}

fn prefix_of(e: &BytesStart<'_>) -> Option<String> {
    e.name().prefix().map(|p| p.as_ref().to_string())
}

pub fn scan(xml: &[u8]) -> Result<SheetIndex, Error> {
    let (mut rd, base) = crate::xmltext::reader(xml);
    let mut ix = SheetIndex {
        ordered: true,
        ..Default::default()
    };
    let mut in_sheet_data = false;
    let mut row: Option<RowInfo> = None;
    let mut cell: Option<CellInfo> = None;
    // Text being collected: which element, and where its content starts.
    let mut text_of: Option<(&'static str, usize)> = None;
    let mut f_attrs: Option<std::collections::HashMap<String, String>> = None;
    let mut in_is = 0usize; // depth inside <is>
    let mut in_rph = false;
    let mut is_text = String::new();
    let mut last_row = 0u32;
    let mut last_col = 0u32;
    let mut saw_sheet_data = false;
    let mut f_start = 0usize;

    loop {
        let start = rd.buffer_position() as usize + base;
        let ev = rd.read_event()?;
        let end = rd.buffer_position() as usize + base;
        match ev {
            Event::Start(e) | Event::Empty(e) if !in_sheet_data => {
                let empty = end >= 2 && &xml[end - 2..end] == b"/>";
                match e.local_name().as_ref() {
                    "dimension" => {
                        let a = attrs(&e)?;
                        // Replaced whole, so take `</dimension>` too when it is written out.
                        let end = if empty {
                            end
                        } else {
                            rd.read_to_end(e.name())?;
                            rd.buffer_position() as usize + base
                        };
                        ix.dimension =
                            Some((start..end, a.get("ref").and_then(|r| Rect::parse(r))));
                    }
                    "sheetData" => {
                        saw_sheet_data = true;
                        ix.prefix = prefix_of(&e);
                        ix.sheet_data_open = start..end;
                        if empty {
                            ix.sheet_data_self_closing = true;
                            ix.sheet_data_close = start;
                        } else {
                            in_sheet_data = true;
                        }
                    }
                    "col" => {
                        let a = attrs(&e)?;
                        if let (Some(min), Some(max)) = (a.get("min"), a.get("max")) {
                            ix.cols.push(ColInfo {
                                min: min.parse().unwrap_or(0),
                                max: max.parse().unwrap_or(0),
                                style: a.get("style").cloned(),
                                width: a.get("width").and_then(|w| w.parse().ok()),
                                hidden: a.get("hidden").is_some_and(|v| v == "1" || v == "true"),
                            });
                        }
                    }
                    "mergeCell" => {
                        if let Some(r) = attrs(&e)?.get("ref").and_then(|r| Rect::parse(r)) {
                            ix.merges.push(r);
                        }
                    }
                    "sheetProtection" => {
                        let a = attrs(&e)?;
                        ix.protected = a.get("sheet").is_some_and(|v| v == "1" || v == "true");
                    }
                    "sheetFormatPr" => {
                        let a = attrs(&e)?;
                        let num = |k: &str| a.get(k).and_then(|v| v.parse::<f64>().ok());
                        // baseColWidth leaves out the padding defaultColWidth includes.
                        ix.default_width = num("defaultColWidth").or_else(|| num("baseColWidth").map(|b| b + 0.71));
                        ix.default_height = num("defaultRowHeight");
                    }
                    "pane" => {
                        let a = attrs(&e)?;
                        if a.get("state")
                            .is_some_and(|s| s == "frozen" || s == "frozenSplit")
                        {
                            let n = |k: &str| {
                                a.get(k).and_then(|v| v.parse::<f64>().ok()).unwrap_or(0.0) as u32
                            };
                            ix.frozen = Some((n("ySplit"), n("xSplit")));
                        }
                    }
                    _ => {}
                }
            }
            Event::Start(e) | Event::Empty(e) => {
                let empty = end >= 2 && &xml[end - 2..end] == b"/>";
                let name = e.local_name();
                match name.as_ref() {
                    "row" if row.is_none() => {
                        let a = attrs(&e)?;
                        let explicit = a.get("r").and_then(|r| r.parse::<u32>().ok());
                        let r = explicit.unwrap_or(last_row + 1);
                        if r <= last_row {
                            ix.ordered = false;
                        }
                        last_row = r;
                        last_col = 0;
                        let info = RowInfo {
                            r,
                            has_r: explicit.is_some(),
                            span: start..end,
                            open_tag: start..end,
                            self_closing: empty,
                            close_start: end,
                            style: a.get("s").cloned(),
                            custom_format: a
                                .get("customFormat")
                                .is_some_and(|v| v == "1" || v == "true"),
                            height: a.get("ht").and_then(|h| h.parse().ok()),
                            hidden: a.get("hidden").is_some_and(|v| v == "1" || v == "true"),
                            cells: Vec::new(),
                        };
                        if empty {
                            ix.rows.push(info);
                        } else {
                            row = Some(info);
                        }
                    }
                    "c" if row.is_some() && cell.is_none() => {
                        let a = attrs(&e)?;
                        let explicit = a.get("r").and_then(|r| CellRef::parse(r));
                        let rr = row.as_ref().unwrap().r;
                        let at = match explicit {
                            Some(c) => c,
                            None => CellRef::new(rr, last_col + 1),
                        };
                        if at.row != rr || at.col <= last_col {
                            ix.ordered = false;
                        }
                        last_col = at.col;
                        let info = CellInfo {
                            at,
                            has_r: explicit.is_some(),
                            span: start..end,
                            s: a.get("s").cloned(),
                            t: a.get("t").cloned(),
                            has_metadata: a.contains_key("cm") || a.contains_key("vm"),
                            formula: None,
                            f_span: None,
                            v: None,
                            inline: None,
                        };
                        if empty {
                            row.as_mut().unwrap().cells.push(info);
                        } else {
                            cell = Some(info);
                            is_text.clear();
                        }
                    }
                    "f" if cell.is_some() && in_is == 0 => {
                        let a = attrs(&e)?;
                        if empty {
                            set_formula(cell.as_mut().unwrap(), &a, String::new());
                            cell.as_mut().unwrap().f_span = Some(start..end);
                        } else {
                            f_attrs = Some(a);
                            f_start = start;
                            text_of = Some(("f", end));
                        }
                    }
                    "v" if cell.is_some() && in_is == 0 && !empty => text_of = Some(("v", end)),
                    "is" if cell.is_some() && !empty => in_is = 1,
                    "rPh" if in_is > 0 && !empty => in_rph = true,
                    "t" if in_is > 0 && !in_rph && !empty => text_of = Some(("t", end)),
                    _ => {
                        if in_is > 0 && !empty {
                            in_is += 1;
                        }
                    }
                }
            }
            Event::End(e) => {
                let name = e.local_name();
                match name.as_ref() {
                    "sheetData" if in_sheet_data && row.is_none() => {
                        in_sheet_data = false;
                        ix.sheet_data_close = start;
                    }
                    "row" if cell.is_none() && row.is_some() => {
                        let mut r = row.take().unwrap();
                        r.span.end = end;
                        r.close_start = start;
                        ix.rows.push(r);
                    }
                    "c" if cell.is_some() && in_is == 0 => {
                        let mut c = cell.take().unwrap();
                        c.span.end = end;
                        if c.t.as_deref() == Some("inlineStr") {
                            c.inline = Some(std::mem::take(&mut is_text));
                        }
                        row.as_mut().unwrap().cells.push(c);
                    }
                    "f" if text_of.map(|t| t.0) == Some("f") => {
                        let raw = String::from_utf8_lossy(&xml[text_of.unwrap().1..start]);
                        let a = f_attrs.take().unwrap_or_default();
                        set_formula(cell.as_mut().unwrap(), &a, unescape_xml(&raw));
                        cell.as_mut().unwrap().f_span = Some(f_start..end);
                        text_of = None;
                    }
                    "v" if text_of.map(|t| t.0) == Some("v") => {
                        let raw = String::from_utf8_lossy(&xml[text_of.unwrap().1..start]);
                        cell.as_mut().unwrap().v = Some(unescape_xml(&raw));
                        text_of = None;
                    }
                    "t" if text_of.map(|t| t.0) == Some("t") => {
                        let raw = String::from_utf8_lossy(&xml[text_of.unwrap().1..start]);
                        is_text.push_str(&decode_xstring(&unescape_xml(&raw)));
                        text_of = None;
                    }
                    "rPh" if in_rph => in_rph = false,
                    "is" if in_is == 1 => in_is = 0,
                    _ => {
                        if in_is > 1 {
                            in_is -= 1;
                        }
                    }
                }
            }
            Event::Eof => break,
            _ => {}
        }
    }
    if !saw_sheet_data {
        return Err(Error::Corrupt("worksheet has no sheetData".into()));
    }
    Ok(ix)
}

fn set_formula(c: &mut CellInfo, a: &std::collections::HashMap<String, String>, text: String) {
    let kind = match a.get("t").map(String::as_str) {
        Some("shared") => FormulaKind::Shared {
            si: a.get("si").cloned().unwrap_or_default(),
            master: !text.is_empty(),
            range: a.get("ref").and_then(|r| Rect::parse(r)),
        },
        Some("array") => FormulaKind::Array {
            range: a.get("ref").and_then(|r| Rect::parse(r)),
        },
        Some("dataTable") => FormulaKind::DataTable,
        _ => FormulaKind::Normal,
    };
    c.formula = Some(FormulaInfo { kind, text });
}

/// What a cell holds, for comparing before and after.
#[derive(Clone, Debug, PartialEq)]
pub struct CellContent {
    pub t: Option<String>,
    pub v: Option<String>,
    pub inline: Option<String>,
    pub formula: Option<String>,
    pub s: Option<String>,
}

impl CellInfo {
    pub fn content(&self) -> CellContent {
        CellContent {
            t: self.t.clone(),
            v: self.v.clone(),
            inline: self.inline.clone(),
            formula: self.formula.as_ref().map(|f| match &f.kind {
                FormulaKind::Shared { si, .. } => format!("shared#{si}:{}", f.text),
                _ => f.text.clone(),
            }),
            s: self.s.clone(),
        }
    }
}
