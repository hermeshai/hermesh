//! Inserting and deleting rows or columns (Excel's 插入 / 删除), as splices into the original
//! parts like every other write (§5.3): the cells at and past the change move, and every
//! reference to them moves with them, as Excel moves it.
//!
//! What moves:
//! - on the sheet changed: its rows and cells, `<dimension>`, merged cells, conditional
//!   formats, data validations, hyperlinks, the autofilter, ignored errors, protected ranges,
//!   column widths (for columns), the anchors of its pictures and charts, and its comments
//!   (threaded ones too) with their note boxes — those on deleted cells go with them;
//! - in every sheet: formulas that read the sheet changed (shared formulas that do are written
//!   out as ordinary ones), and the formulas of conditional formats and validations;
//! - defined names and the ranges charts plot.
//!
//! A reference into deleted cells becomes `#REF!`, as in Excel; so does the cached value of a
//! formula that gains one. `calcChain.xml` is dropped and `fullCalcOnLoad` set, as for any
//! formula edit.
//!
//! Refused, as moving them faithfully is not done here: Excel tables, pivot tables,
//! the x14 extensions on the sheet (newer conditional formats, validations, sparklines),
//! form controls and OLE objects, 3-D references, data tables, and array formulas the change
//! would cut through.

use std::collections::BTreeMap;
use std::collections::HashMap;
use std::ops::Range;

use quick_xml::events::Event;

use crate::Error;
use crate::book::Book;
use crate::book::Value;
use crate::cellref::CellRef;
use crate::cellref::MAX_COL;
use crate::cellref::MAX_ROW;
use crate::cellref::Rect;
use crate::cellref::col_name;
use crate::formula::Tok;
use crate::formula::parse_cell;
use crate::formula::parse_col_part;
use crate::formula::parse_row_part;
use crate::formula::tokenize;
use crate::formula::translate;
use crate::package::Package;
use crate::package::SheetKind;
use crate::package::WorkbookInfo;
use crate::package::attrs;
use crate::package::parse_rels;
use crate::package::rels_part_for;
use crate::package::resolve;
use crate::patch::Splice;
use crate::patch::apply_splices;
use crate::patch::content_type_override_span;
use crate::patch::remove_spans;
use crate::patch::set_attr;
use crate::patch::set_full_calc;
use crate::patch::write_package;
use crate::scan::FormulaKind;
use crate::scan::scan;
use crate::xmltext::escape_xml;
use crate::xmltext::unescape_xml;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Axis {
    Rows,
    Cols,
}

/// `count` rows (or columns) inserted before `at`, or deleted from `at` on; 1-based.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Shift {
    pub axis: Axis,
    pub at: u32,
    pub count: u32,
    pub delete: bool,
}

impl Shift {
    fn max(&self) -> u32 {
        match self.axis {
            Axis::Rows => MAX_ROW,
            Axis::Cols => MAX_COL,
        }
    }

    /// The last row (or column) deleted.
    fn end(&self) -> u32 {
        self.at + self.count - 1
    }

    /// Where the rows (or columns) `lo..=hi` are after the change; None when all are deleted.
    pub fn span(&self, lo: u32, hi: u32) -> Option<(u32, u32)> {
        if !self.delete {
            let moved = |v: u32| if v >= self.at { v + self.count } else { v };
            let (a, b) = (moved(lo), moved(hi));
            // What reached the sheet's edge stays at it, as `A1:A1048576` does in Excel.
            return (a <= self.max()).then(|| (a, b.min(self.max())));
        }
        let end = self.end();
        if lo >= self.at && hi <= end {
            return None;
        }
        let a = if lo < self.at { lo } else if lo > end { lo - self.count } else { self.at };
        let b = if hi < self.at { hi } else if hi > end { hi - self.count } else { self.at - 1 };
        Some((a, b))
    }

    pub fn rect(&self, r: Rect) -> Option<Rect> {
        match self.axis {
            Axis::Rows => self
                .span(r.first.row, r.last.row)
                .map(|(a, b)| Rect { first: CellRef::new(a, r.first.col), last: CellRef::new(b, r.last.col) }),
            Axis::Cols => self
                .span(r.first.col, r.last.col)
                .map(|(a, b)| Rect { first: CellRef::new(r.first.row, a), last: CellRef::new(r.last.row, b) }),
        }
    }

    pub fn cell(&self, c: CellRef) -> Option<CellRef> {
        self.rect(Rect::single(c)).map(|r| r.first)
    }

    /// A list of ranges (`sqref`: `A1:B2 D4`), each moved; those deleted drop out.
    fn sqref(&self, s: &str) -> Option<String> {
        let kept: Vec<String> = s.split_whitespace().filter_map(|r| Rect::parse(r)).filter_map(|r| self.rect(r)).map(|r| r.to_string()).collect();
        (!kept.is_empty()).then(|| kept.join(" "))
    }

    /// Whether the change cuts through `lo..=hi` without taking all of it: rows inserted
    /// inside it, or some but not all of it deleted.
    fn cuts(&self, lo: u32, hi: u32) -> bool {
        if self.delete {
            let end = self.end();
            let overlaps = lo <= end && hi >= self.at;
            overlaps && !(lo >= self.at && hi <= end)
        } else {
            self.at > lo && self.at <= hi
        }
    }

    fn along(&self, r: Rect) -> (u32, u32) {
        match self.axis {
            Axis::Rows => (r.first.row, r.last.row),
            Axis::Cols => (r.first.col, r.last.col),
        }
    }
}

fn refuse(msg: impl Into<String>) -> Error {
    Error::Unsupported(msg.into())
}

/// The sheet a `Sheet1!` or `'My sheet'!` prefix names; None for another workbook's.
fn prefix_sheet(p: &str) -> Option<String> {
    let p = p.strip_suffix('!').unwrap_or(p);
    if p.starts_with('[') || p.starts_with("'[") {
        return None;
    }
    Some(match p.strip_prefix('\'').and_then(|q| q.strip_suffix('\'')) {
        Some(inner) => inner.replace("''", "'"),
        None => p.to_string(),
    })
}

fn same_sheet(a: &str, b: &str) -> bool {
    a.to_lowercase() == b.to_lowercase()
}

/// Whether a formula names `target` in a reference.
fn mentions(text: &str, target: &str) -> bool {
    tokenize(text).is_ok_and(|toks| toks.iter().any(|t| matches!(t, Tok::SheetPrefix(p) if prefix_sheet(p).is_some_and(|s| same_sheet(&s, target)))))
}

/// A formula as stored, its references to `target` moved; `home`: the formula is on `target`,
/// so its references without a sheet are to it.
pub fn adjust_formula(text: &str, home: bool, target: &str, shift: &Shift) -> Result<String, Error> {
    let toks = tokenize(text)?;
    let colon = Tok::Other(":".into());
    let mut out = String::with_capacity(text.len() + 8);
    let mut named: Option<bool> = None;
    let mut i = 0;
    while i < toks.len() {
        let tok = &toks[i];
        if let Tok::SheetPrefix(p) = tok {
            // `Sheet1:Sheet3!A1`: a reference through several sheets.
            if i >= 2 && toks[i - 1] == colon && matches!(toks[i - 2], Tok::Name(_) | Tok::Cell(_)) {
                return Err(refuse("a formula reads through several sheets (Sheet1:Sheet3!A1); insert or delete in Excel"));
            }
            named = Some(prefix_sheet(p).is_some_and(|s| same_sheet(&s, target)));
            out.push_str(p);
            i += 1;
            continue;
        }
        let ours = named.take().unwrap_or(home);
        let pair = |want: fn(&Tok) -> bool| i + 2 < toks.len() && toks[i + 1] == colon && want(&toks[i + 2]);
        match tok {
            Tok::Cell(a) if ours => {
                if pair(|t| matches!(t, Tok::Cell(_))) {
                    out.push_str(&cell_range(a, toks[i + 2].text(), shift));
                    i += 3;
                } else {
                    let (c, ca, r, ra) = parse_cell(a).unwrap();
                    out.push_str(&match shift.cell(CellRef::new(r, c)) {
                        Some(n) => cell_text(n, ca, ra),
                        None => "#REF!".into(),
                    });
                    i += 1;
                }
                continue;
            }
            Tok::Col(a) if ours && pair(|t| matches!(t, Tok::Col(_))) => {
                out.push_str(&line_range(a, toks[i + 2].text(), Axis::Cols, shift));
                i += 3;
                continue;
            }
            Tok::Row(a) if ours && pair(|t| matches!(t, Tok::Row(_))) => {
                out.push_str(&line_range(a, toks[i + 2].text(), Axis::Rows, shift));
                i += 3;
                continue;
            }
            _ => out.push_str(tok.text()),
        }
        i += 1;
    }
    Ok(out)
}

fn cell_text(at: CellRef, col_abs: bool, row_abs: bool) -> String {
    format!("{}{}{}{}", if col_abs { "$" } else { "" }, col_name(at.col), if row_abs { "$" } else { "" }, at.row)
}

/// `A1:B9` moved; each end keeps its `$`s.
fn cell_range(a: &str, b: &str, shift: &Shift) -> String {
    let (c1, ca1, r1, ra1) = parse_cell(a).unwrap();
    let (c2, ca2, r2, ra2) = parse_cell(b).unwrap();
    let (lo, hi) = match shift.axis {
        Axis::Rows => (r1.min(r2), r1.max(r2)),
        Axis::Cols => (c1.min(c2), c1.max(c2)),
    };
    let Some((nlo, nhi)) = shift.span(lo, hi) else { return "#REF!".into() };
    let moved = |v: u32| if v == lo { nlo } else { nhi };
    let (p, q) = match shift.axis {
        Axis::Rows => (CellRef::new(moved(r1), c1), CellRef::new(if r1 == r2 { nhi } else { moved(r2) }, c2)),
        Axis::Cols => (CellRef::new(r1, moved(c1)), CellRef::new(r2, if c1 == c2 { nhi } else { moved(c2) })),
    };
    format!("{}:{}", cell_text(p, ca1, ra1), cell_text(q, ca2, ra2))
}

/// `A:C` or `3:5`, moved when the change is along it.
fn line_range(a: &str, b: &str, axis: Axis, shift: &Shift) -> String {
    if axis != shift.axis {
        return format!("{a}:{b}");
    }
    let part = |s: &str| match axis {
        Axis::Cols => parse_col_part(s).unwrap(),
        Axis::Rows => parse_row_part(s).unwrap(),
    };
    let ((x, xa), (y, ya)) = (part(a), part(b));
    let (lo, hi) = (x.min(y), x.max(y));
    let Some((nlo, nhi)) = shift.span(lo, hi) else { return "#REF!".into() };
    let (nx, ny) = if x <= y { (nlo, nhi) } else { (nhi, nlo) };
    let show = |v: u32, abs: bool| format!("{}{}", if abs { "$" } else { "" }, if axis == Axis::Cols { col_name(v) } else { v.to_string() });
    format!("{}:{}", show(nx, xa), show(ny, ya))
}

fn prefix(p: &Option<String>) -> String {
    p.as_ref().map(|p| format!("{p}:")).unwrap_or_default()
}

/// The end of the start tag that begins at `start`.
fn open_tag_end(xml: &[u8], start: usize) -> usize {
    let mut quote = None;
    for (i, &b) in xml.iter().enumerate().skip(start) {
        match (quote, b) {
            (None, b'"' | b'\'') => quote = Some(b),
            (Some(q), _) if b == q => quote = None,
            (None, b'>') => return i + 1,
            _ => {}
        }
    }
    xml.len()
}

/// Removes ` name="…"` from a start tag given as text.
fn drop_attr(tag: &str, name: &str) -> String {
    for q in ['"', '\''] {
        let key = format!(" {name}={q}");
        if let Some(i) = tag.find(&key) {
            let vstart = i + key.len();
            if let Some(len) = tag[vstart..].find(q) {
                return format!("{}{}", &tag[..i], &tag[vstart + len + 1..]);
            }
        }
    }
    tag.to_string()
}

fn text(xml: &[u8], r: Range<usize>) -> String {
    String::from_utf8_lossy(&xml[r]).into_owned()
}

/// A worksheet's `<sheetData>`: the cells moved (`moves`: this is the sheet changed) and the
/// formulas that read the sheet changed adjusted.
fn sheet_data(xml: &[u8], target: &str, shift: &Shift, moves: bool) -> Result<Vec<Splice>, Error> {
    let ix = scan(xml)?;
    let p = prefix(&ix.prefix);
    let mut out = Vec::new();
    if moves {
        if ix.rows.iter().any(|r| !r.has_r) || ix.cells().any(|c| !c.has_r) {
            return Err(refuse("the sheet has rows or cells without their address; open it in Excel and save it once, then try again"));
        }
        if !shift.delete {
            let last = ix.cells().map(|c| if shift.axis == Axis::Rows { c.at.row } else { c.at.col }).max().unwrap_or(0);
            if last + shift.count > shift.max() {
                return Err(refuse("there is no room: the change would push cells off the end of the sheet"));
            }
        }
    }
    // Shared formulas: the master's text by group, and whether the group is written out.
    let mut masters: HashMap<&str, (CellRef, &str)> = HashMap::new();
    for c in ix.cells() {
        if let Some(f) = &c.formula
            && let FormulaKind::Shared { si, master: true, .. } = &f.kind
        {
            masters.insert(si.as_str(), (c.at, f.text.as_str()));
        }
    }
    let expand = |si: &str| moves || masters.get(si).is_some_and(|(_, t)| mentions(t, target));

    for row in &ix.rows {
        let new_row = if moves && shift.axis == Axis::Rows { shift.span(row.r, row.r).map(|(a, _)| a) } else { Some(row.r) };
        let Some(new_row) = new_row else {
            out.push(Splice { range: row.span.clone(), text: String::new() });
            continue;
        };
        if moves {
            let tag = text(xml, row.open_tag.clone());
            let mut new = if new_row != row.r { set_attr(&tag, "r", &new_row.to_string(), true) } else { tag.clone() };
            // `spans` says which columns the row's cells are in: an optimisation Excel rebuilds.
            if shift.axis == Axis::Cols {
                new = drop_attr(&new, "spans");
            }
            if new != tag {
                out.push(Splice { range: row.open_tag.clone(), text: new });
            }
        }
        for c in &row.cells {
            let at = if moves { shift.cell(c.at) } else { Some(c.at) };
            let Some(at) = at else {
                out.push(Splice { range: c.span.clone(), text: String::new() });
                continue;
            };
            let tag_end = open_tag_end(xml, c.span.start).min(c.span.end);
            let tag = text(xml, c.span.start..tag_end);
            let new_tag = if at != c.at { set_attr(&tag, "r", &at.to_string(), true) } else { tag.clone() };
            let (Some(f), Some(f_span)) = (&c.formula, &c.f_span) else {
                if new_tag != tag {
                    out.push(Splice { range: c.span.start..tag_end, text: new_tag });
                }
                continue;
            };
            let f_tag_end = open_tag_end(xml, f_span.start).min(f_span.end);
            let f_tag = text(xml, f_span.start..f_tag_end);
            let (old_text, new_f) = match &f.kind {
                FormulaKind::DataTable if moves => return Err(refuse(format!("{} holds a data table (模拟运算表); insert or delete in Excel", c.at))),
                FormulaKind::DataTable => (None, None),
                FormulaKind::Shared { si, .. } if expand(si) => {
                    let Some((m_at, m_text)) = masters.get(si.as_str()) else {
                        return Err(Error::Corrupt(format!("{}: a shared formula without its master", c.at)));
                    };
                    let here = translate(m_text, c.at.row as i64 - m_at.row as i64, c.at.col as i64 - m_at.col as i64)?;
                    let new = adjust_formula(&here, moves, target, shift)?;
                    (Some(here), Some(format!("<{p}f>{}</{p}f>", escape_xml(&new, false))))
                }
                FormulaKind::Shared { .. } => (None, None),
                FormulaKind::Array { range } => {
                    let new = adjust_formula(&f.text, moves, target, shift)?;
                    let mut open = f_tag.clone();
                    if moves && let Some(r) = range {
                        let (lo, hi) = shift.along(*r);
                        if shift.cuts(lo, hi) {
                            return Err(refuse(format!("the change cuts through the array formula at {r}; Excel cannot change part of an array either")));
                        }
                        if let Some(n) = shift.rect(*r) {
                            open = set_attr(&open, "ref", &n.to_string(), true);
                        }
                    }
                    let changed = new != f.text || open != f_tag;
                    (Some(f.text.clone()), changed.then(|| format!("{open}{}</{p}f>", escape_xml(&new, false))))
                }
                FormulaKind::Normal => {
                    let new = adjust_formula(&f.text, moves, target, shift)?;
                    (Some(f.text.clone()), (new != f.text).then(|| format!("{f_tag}{}</{p}f>", escape_xml(&new, false))))
                }
            };
            let broke = match (&old_text, &new_f) {
                (Some(old), Some(new)) => new.contains("#REF!") && !old.contains("#REF!"),
                _ => false,
            };
            if broke {
                // Its value is an error now, as Excel shows it once it recalculates.
                let open = set_attr(&new_tag, "t", "e", c.t.is_some());
                out.push(Splice { range: c.span.clone(), text: format!("{open}{}<{p}v>#REF!</{p}v></{p}c>", new_f.unwrap()) });
                continue;
            }
            if new_tag != tag {
                out.push(Splice { range: c.span.start..tag_end, text: new_tag });
            }
            if let Some(new_f) = new_f {
                out.push(Splice { range: f_span.clone(), text: new_f });
            }
        }
    }
    Ok(out)
}

/// An element whose address list moves, and the attribute that holds it.
fn address_attr(name: &str, axis: Axis) -> Option<&'static str> {
    Some(match name {
        "mergeCell" | "hyperlink" | "autoFilter" => "ref",
        "conditionalFormatting" | "dataValidation" | "ignoredError" | "protectedRange" => "sqref",
        "col" if axis == Axis::Cols => "min",
        _ => return None,
    })
}

/// Lists that are removed when nothing is left in them; `count` kept right.
const LISTS: &[&str] = &["mergeCells", "dataValidations", "hyperlinks", "ignoredErrors", "protectedRanges", "cols"];

/// Elements whose text is a formula.
const FORMULA_TEXT: &[&str] = &["formula", "formula1", "formula2", "f"];

struct List {
    start: usize,
    open: Range<usize>,
    tag: String,
    kept: usize,
    removed: usize,
    splices: Vec<Splice>,
}

/// The rest of a worksheet part, `<sheetData>` left out: on the sheet changed, what holds
/// addresses moves; on every sheet, formulas in conditional formats and validations adjust.
fn sheet_rest(xml: &[u8], target: &str, shift: &Shift, moves: bool) -> Result<Vec<Splice>, Error> {
    let (mut rd, base) = crate::xmltext::reader(xml);
    let mut out: Vec<Splice> = Vec::new();
    let mut lists: Vec<List> = Vec::new();
    // Open elements, and where each one's content starts.
    let mut stack: Vec<(String, usize)> = Vec::new();
    fn sink<'a>(lists: &'a mut [List], out: &'a mut Vec<Splice>) -> &'a mut Vec<Splice> {
        match lists.last_mut() {
            Some(l) => &mut l.splices,
            None => out,
        }
    }
    loop {
        let start = rd.buffer_position() as usize + base;
        let ev = rd.read_event()?;
        let end = rd.buffer_position() as usize + base;
        match ev {
            Event::Start(e) | Event::Empty(e) => {
                let empty = xml[..end].ends_with(b"/>");
                let name = e.local_name().as_ref().to_string();
                if name == "sheetData" {
                    if !empty {
                        rd.read_to_end(e.name())?;
                    }
                    continue;
                }
                if moves {
                    match name.as_str() {
                        "dimension" => {
                            let a = attrs(&e)?;
                            if let Some(r) = a.get("ref").and_then(|r| Rect::parse(r)) {
                                let n = shift.rect(r).unwrap_or(Rect::single(CellRef::new(1, 1)));
                                sink(&mut lists, &mut out).push(Splice { range: start..end, text: set_attr(&text(xml, start..end), "ref", &n.to_string(), true) });
                            }
                            if !empty {
                                stack.push((name, end));
                            }
                            continue;
                        }
                        // Where the cursor was: Excel puts it back at A1 without one.
                        "selection" => {
                            let stop = if empty { end } else {
                                rd.read_to_end(e.name())?;
                                rd.buffer_position() as usize + base
                            };
                            sink(&mut lists, &mut out).push(Splice { range: start..stop, text: String::new() });
                            continue;
                        }
                        n if LISTS.contains(&n) && (n != "cols" || shift.axis == Axis::Cols) => {
                            if empty {
                                continue;
                            }
                            lists.push(List { start, open: start..end, tag: text(xml, start..end), kept: 0, removed: 0, splices: Vec::new() });
                            stack.push((name, end));
                            continue;
                        }
                        _ => {}
                    }
                    if let Some(attr) = address_attr(&name, shift.axis) {
                        let a = attrs(&e)?;
                        let tag = text(xml, start..end);
                        let moved = if name == "col" {
                            let (min, max) = (a.get("min").and_then(|v| v.parse().ok()).unwrap_or(1u32), a.get("max").and_then(|v| v.parse().ok()).unwrap_or(1u32));
                            shift.span(min, max).map(|(lo, hi)| set_attr(&set_attr(&tag, "min", &lo.to_string(), true), "max", &hi.to_string(), true))
                        } else {
                            match a.get(attr) {
                                // A list a sheet cannot read is left as it is.
                                Some(v) if v.split_whitespace().all(|r| Rect::parse(r).is_some()) => shift.sqref(v).map(|n| set_attr(&tag, attr, &n, true)),
                                _ => Some(tag.clone()),
                            }
                        };
                        if name == "autoFilter" && shift.axis == Axis::Cols && !empty {
                            return Err(refuse("the sheet's filter has conditions set; clear them, or insert or delete columns in Excel"));
                        }
                        match moved {
                            None => {
                                let stop = if empty { end } else {
                                    rd.read_to_end(e.name())?;
                                    rd.buffer_position() as usize + base
                                };
                                if let Some(l) = lists.last_mut() {
                                    l.removed += 1;
                                }
                                sink(&mut lists, &mut out).push(Splice { range: start..stop, text: String::new() });
                                continue;
                            }
                            Some(new) => {
                                if let Some(l) = lists.last_mut() {
                                    l.kept += 1;
                                }
                                if new != tag {
                                    sink(&mut lists, &mut out).push(Splice { range: start..end, text: new });
                                }
                            }
                        }
                    }
                }
                if !empty {
                    stack.push((name, end));
                }
            }
            Event::End(e) => {
                let name = e.local_name().as_ref().to_string();
                if let Some((open, from)) = stack.pop()
                    && open == name
                    && FORMULA_TEXT.contains(&name.as_str())
                {
                    let f = unescape_xml(&text(xml, from..start));
                    let new = adjust_formula(&f, moves, target, shift)?;
                    if new != f {
                        sink(&mut lists, &mut out).push(Splice { range: from..start, text: escape_xml(&new, false) });
                    }
                }
                if moves && LISTS.contains(&name.as_str()) && lists.last().is_some_and(|l| l.tag.contains(&name)) {
                    let l = lists.pop().unwrap();
                    let into = match lists.last_mut() {
                        Some(o) => &mut o.splices,
                        None => &mut out,
                    };
                    if l.kept == 0 && l.removed > 0 {
                        into.push(Splice { range: l.start..end, text: String::new() });
                    } else {
                        if l.removed > 0 && l.tag.contains(" count=") {
                            into.push(Splice { range: l.open.clone(), text: set_attr(&l.tag, "count", &l.kept.to_string(), true) });
                        }
                        into.extend(l.splices);
                    }
                }
            }
            Event::Eof => break,
            _ => {}
        }
    }
    Ok(out)
}

/// Pictures and charts on the sheet changed: their anchors move with the cells (0-based in
/// the part); those pinned to the page (`editAs="absolute"`) stay.
fn drawing(xml: &[u8], shift: &Shift) -> Result<Vec<Splice>, Error> {
    let (mut rd, base) = crate::xmltext::reader(xml);
    let mut out = Vec::new();
    let mut absolute = false;
    let mut corner = false;
    let mut field: Option<String> = None;
    let want = if shift.axis == Axis::Rows { "row" } else { "col" };
    loop {
        let start = rd.buffer_position() as usize + base;
        let ev = rd.read_event()?;
        let end = rd.buffer_position() as usize + base;
        match ev {
            Event::Start(e) => match e.local_name().as_ref() {
                "twoCellAnchor" | "oneCellAnchor" => absolute = attrs(&e)?.get("editAs").is_some_and(|v| v == "absolute"),
                "from" | "to" => corner = true,
                n if corner && n == want => field = Some(want.into()),
                _ => {}
            },
            Event::Text(_) if field.is_some() && !absolute => {
                if let Ok(v) = text(xml, start..end).trim().parse::<u32>() {
                    let one = v + 1;
                    let moved = if shift.delete {
                        if one < shift.at { one } else if one > shift.end() { one - shift.count } else { shift.at.min(shift.max()) }
                    } else if one >= shift.at {
                        (one + shift.count).min(shift.max())
                    } else {
                        one
                    };
                    if moved != one {
                        out.push(Splice { range: start..end, text: (moved - 1).to_string() });
                    }
                }
            }
            Event::End(e) => match e.local_name().as_ref() {
                "from" | "to" => corner = false,
                n if n == want => field = None,
                _ => {}
            },
            Event::Eof => break,
            _ => {}
        }
    }
    Ok(out)
}

/// Only the formulas in a part's elements named `names` adjusted (defined names, chart series).
fn formula_texts(xml: &[u8], names: &[&str], target: &str, shift: &Shift) -> Result<Vec<Splice>, Error> {
    let (mut rd, base) = crate::xmltext::reader(xml);
    let mut out = Vec::new();
    let mut from: Option<usize> = None;
    loop {
        let start = rd.buffer_position() as usize + base;
        let ev = rd.read_event()?;
        let end = rd.buffer_position() as usize + base;
        match ev {
            Event::Start(e) if names.iter().any(|n| e.local_name().as_ref() == *n) => from = Some(end),
            Event::End(e) if from.is_some() && names.iter().any(|n| e.local_name().as_ref() == *n) => {
                let at = from.take().unwrap();
                let f = unescape_xml(&text(xml, at..start));
                let new = adjust_formula(&f, false, target, shift)?;
                if new != f {
                    out.push(Splice { range: at..start, text: escape_xml(&new, false) });
                }
            }
            Event::Eof => break,
            _ => {}
        }
    }
    Ok(out)
}

/// `<comment ref>` and `<threadedComment ref>`: moved, or gone with their cell.
fn comment_refs(xml: &[u8], shift: &Shift) -> Result<Vec<Splice>, Error> {
    let (mut rd, base) = crate::xmltext::reader(xml);
    let mut out = Vec::new();
    loop {
        let start = rd.buffer_position() as usize + base;
        let ev = rd.read_event()?;
        let end = rd.buffer_position() as usize + base;
        match ev {
            Event::Start(e) | Event::Empty(e) if matches!(e.local_name().as_ref(), "comment" | "threadedComment") => {
                let empty = xml[..end].ends_with(b"/>");
                let Some(at) = attrs(&e)?.get("ref").and_then(|r| CellRef::parse(r)) else { continue };
                match shift.cell(at) {
                    Some(n) if n == at => {}
                    Some(n) => out.push(Splice { range: start..end, text: set_attr(&text(xml, start..end), "ref", &n.to_string(), true) }),
                    None => {
                        let stop = if empty { end } else {
                            rd.read_to_end(e.name())?;
                            rd.buffer_position() as usize + base
                        };
                        out.push(Splice { range: start..stop, text: String::new() });
                    }
                }
            }
            Event::Eof => break,
            _ => {}
        }
    }
    Ok(out)
}

/// The note boxes of comments in a legacy VML part (not always well-formed XML, so read as
/// text): each `<v:shape>` whose `<x:Row>`/`<x:Column>` (0-based) is its cell moves with it,
/// its `<x:Anchor>` too; one on a deleted cell goes. Any other kind of shape (a form control)
/// is refused. None when nothing changes.
fn notes(vml: &str, shift: &Shift) -> Result<Option<String>, Error> {
    let tag = |body: &str, name: &str| -> Option<(usize, usize)> {
        let open = format!("<x:{name}>");
        let i = body.find(&open)? + open.len();
        let j = body[i..].find(&format!("</x:{name}>"))? + i;
        Some((i, j))
    };
    let mut out = String::with_capacity(vml.len());
    let mut rest = vml;
    let mut changed = false;
    while let Some(i) = rest.find("<v:shape ") {
        out.push_str(&rest[..i]);
        let shape = &rest[i..];
        let len = shape.find("</v:shape>").map(|j| j + "</v:shape>".len()).ok_or_else(|| Error::Corrupt("a VML shape is not closed".into()))?;
        let mut body = shape[..len].to_string();
        rest = &shape[len..];
        if !body.contains("ObjectType=\"Note\"") {
            return Err(refuse("the sheet has form controls; insert or delete rows and columns in Excel"));
        }
        let num = |b: &str, name: &str| tag(b, name).and_then(|(a, z)| b[a..z].trim().parse::<u32>().ok());
        let (Some(row), Some(col)) = (num(&body, "Row"), num(&body, "Column")) else {
            out.push_str(&body);
            continue;
        };
        let Some(at) = shift.cell(CellRef::new(row + 1, col + 1)) else {
            changed = true;
            continue;
        };
        if (at.row, at.col) != (row + 1, col + 1) {
            changed = true;
            for (name, v) in [("Row", at.row - 1), ("Column", at.col - 1)] {
                let (a, z) = tag(&body, name).unwrap();
                body.replace_range(a..z, &v.to_string());
            }
            // Anchor: left col, offset, top row, offset, right col, offset, bottom row, offset.
            if let Some((a, z)) = tag(&body, "Anchor") {
                let mut parts: Vec<String> = body[a..z].split(',').map(|x| x.trim().to_string()).collect();
                let idx: &[usize] = if shift.axis == Axis::Rows { &[2, 6] } else { &[0, 4] };
                for &k in idx {
                    if let Some(v) = parts.get(k).and_then(|x| x.parse::<u32>().ok()) {
                        let one = v + 1;
                        let moved = if shift.delete {
                            if one < shift.at { one } else if one > shift.end() { one - shift.count } else { shift.at }
                        } else if one >= shift.at { (one + shift.count).min(shift.max()) } else { one };
                        parts[k] = (moved - 1).to_string();
                    }
                }
                body.replace_range(a..z, &parts.join(", "));
            }
        }
        out.push_str(&body);
    }
    out.push_str(rest);
    Ok(changed.then_some(out))
}

fn has_tag(xml: &[u8], local: &str) -> bool {
    let s = String::from_utf8_lossy(xml);
    s.contains(&format!("<{local}")) || s.contains(&format!(":{local} ")) || s.contains(&format!(":{local}>")) || s.contains(&format!(":{local}/"))
}

/// The workbook in `input` with rows or columns of `sheet` inserted or deleted, checked: every
/// value that is not a formula is where it should be, and nothing else moved.
pub fn shift(input: &[u8], sheet: &str, shift: Shift) -> Result<Vec<u8>, Error> {
    if shift.count == 0 || shift.at == 0 || shift.at > shift.max() || (shift.delete && shift.end() > shift.max()) {
        return Err(refuse("nothing to insert or delete there"));
    }
    let pkg = Package::open(input.to_vec())?;
    let wb = WorkbookInfo::load(&pkg)?;
    let entry = wb.sheet(sheet).ok_or_else(|| Error::NotFound(format!("no sheet named {sheet}")))?;
    if entry.kind != SheetKind::Worksheet {
        return Err(refuse(format!("{sheet} is not a worksheet")));
    }
    let target = entry.name.clone();
    let part = entry.part.clone().ok_or_else(|| Error::Corrupt(format!("{sheet} has no part")))?;
    let xml = pkg.read(&part)?.ok_or_else(|| Error::Corrupt(format!("missing {part}")))?;

    // What is not moved here.
    let rels_part = rels_part_for(&part);
    let rels = match pkg.read(&rels_part)? {
        Some(x) => parse_rels(&x)?,
        None => Vec::new(),
    };
    for (kind, what) in [("table", "an Excel table (表格)"), ("pivotTable", "a pivot table")] {
        if rels.iter().any(|r| r.is(kind)) {
            return Err(refuse(format!("{target} has {what}; insert or delete rows and columns in Excel")));
        }
    }
    for (tag, what) in [("oleObjects", "embedded objects"), ("controls", "form controls"), ("sparklineGroups", "sparklines")] {
        if has_tag(&xml, tag) {
            return Err(refuse(format!("{target} has {what}; insert or delete rows and columns in Excel")));
        }
    }
    let s = String::from_utf8_lossy(&xml);
    if s.contains("x14:conditionalFormatting") || s.contains("x14:dataValidation") {
        return Err(refuse(format!("{target} has conditional formats or validations from a newer Excel; insert or delete rows and columns in Excel")));
    }
    for name in &pkg.names {
        let lower = name.to_lowercase();
        if lower.contains("pivotcachedefinition") && lower.ends_with(".xml") && !lower.contains("_rels") {
            let x = pkg.read(name)?.unwrap_or_default();
            let (mut rd, _) = crate::xmltext::reader(&x);
            loop {
                match rd.read_event()? {
                    Event::Start(e) | Event::Empty(e) if e.local_name().as_ref() == "worksheetSource" => {
                        if attrs(&e)?.get("sheet").is_some_and(|s| same_sheet(s, &target)) {
                            return Err(refuse(format!("a pivot table reads {target}; insert or delete rows and columns in Excel")));
                        }
                    }
                    Event::Eof => break,
                    _ => {}
                }
            }
        }
    }

    let mut changed: BTreeMap<String, Vec<u8>> = BTreeMap::new();
    let entry_name = |p: &str| pkg.entry_name(p).map(str::to_string).ok_or_else(|| Error::Corrupt(format!("missing {p}")));

    for s in &wb.sheets {
        let (Some(p), SheetKind::Worksheet) = (&s.part, &s.kind) else { continue };
        let moves = same_sheet(&s.name, &target);
        let x = if moves { xml.clone() } else { pkg.read(p)?.ok_or_else(|| Error::Corrupt(format!("missing {p}")))? };
        let mut splices = sheet_data(&x, &target, &shift, moves)?;
        splices.extend(sheet_rest(&x, &target, &shift, moves)?);
        if !splices.is_empty() {
            changed.insert(entry_name(p)?, apply_splices(&x, splices)?);
        }
    }

    // The sheet's pictures and charts.
    for r in rels.iter().filter(|r| r.is("drawing") && !r.external) {
        let p = resolve(&part, &r.target);
        if let Some(x) = pkg.read(&p)? {
            let splices = drawing(&x, &shift)?;
            if !splices.is_empty() {
                changed.insert(entry_name(&p)?, apply_splices(&x, splices)?);
            }
        }
    }
    // Comments, threaded comments and their note boxes move with their cells.
    for r in rels.iter().filter(|r| !r.external) {
        let p = resolve(&part, &r.target);
        let Some(x) = pkg.read(&p)? else { continue };
        let new = if r.is("comments") || r.is("threadedComment") {
            let splices = comment_refs(&x, &shift)?;
            if splices.is_empty() { continue }
            apply_splices(&x, splices)?
        } else if r.is("vmlDrawing") {
            match notes(&String::from_utf8_lossy(&x), &shift)? {
                Some(v) => v.into_bytes(),
                None => continue,
            }
        } else {
            continue;
        };
        changed.insert(entry_name(&p)?, new);
    }
    // What every chart plots, wherever it is.
    for name in &pkg.names {
        let lower = name.to_lowercase();
        if lower.starts_with("xl/charts/") && lower.ends_with(".xml") && !lower.contains("_rels") {
            let x = pkg.read(name)?.unwrap_or_default();
            let splices = formula_texts(&x, &["f"], &target, &shift)?;
            if !splices.is_empty() {
                changed.insert(name.clone(), apply_splices(&x, splices)?);
            }
        }
    }
    // Defined names, and Excel recalculates on opening.
    let wb_xml = pkg.read(&wb.part)?.ok_or_else(|| Error::Corrupt("no workbook part".into()))?;
    let names = formula_texts(&wb_xml, &["definedName"], &target, &shift)?;
    let wb_xml = if names.is_empty() { wb_xml } else { apply_splices(&wb_xml, names)? };
    changed.insert(entry_name(&wb.part)?, set_full_calc(&wb_xml)?);

    let mut removed = Vec::new();
    if let Some(rel) = &wb.calc_chain {
        let p = resolve(&wb.part, &rel.target);
        if let Some(e) = pkg.entry_name(&p) {
            removed.push(e.to_string());
            let rels = pkg.read(&wb.rels_part)?.unwrap_or_default();
            changed.insert(entry_name(&wb.rels_part)?, remove_spans(&rels, vec![rel.span.clone()])?);
            if let Some(ct) = pkg.read("[Content_Types].xml")?
                && let Some(span) = content_type_override_span(&ct, &p)?
            {
                changed.insert(entry_name("[Content_Types].xml")?, remove_spans(&ct, vec![span])?);
            }
        }
    }

    let bytes = write_package(&pkg, &changed, &removed)?;
    check(input, &bytes, &target, &shift)?;
    Ok(bytes)
}

/// Every value that is not a formula is where the change puts it, on the sheet changed and on
/// every other; the shape of each sheet holds.
fn check(before: &[u8], after: &[u8], target: &str, shift: &Shift) -> Result<(), Error> {
    let a = Book::open(before)?;
    let b = Book::open(after).map_err(|e| Error::SelfCheck(format!("the result does not open: {e}")))?;
    if a.sheets.len() != b.sheets.len() {
        return Err(Error::SelfCheck("a sheet went missing".into()));
    }
    for (s, sheet) in a.sheets.iter().enumerate() {
        let Some(ix) = &sheet.index else { continue };
        let moves = same_sheet(&sheet.name, target);
        let mut expected = 0usize;
        for c in ix.cells() {
            if c.formula.is_some() {
                continue;
            }
            let value = a.value(s, c.at);
            if value == Value::Empty {
                continue;
            }
            let at = if moves { shift.cell(c.at) } else { Some(c.at) };
            let Some(at) = at else { continue };
            expected += 1;
            let got = b.value(s, at);
            if got != value {
                return Err(Error::SelfCheck(format!("{}!{} should hold {value:?} after the change, not {got:?}", sheet.name, at)));
            }
        }
        let found = b.sheets[s].index.as_ref().map_or(0, |ix| ix.cells().filter(|c| c.formula.is_none() && b.value(s, c.at) != Value::Empty).count());
        if found != expected {
            return Err(Error::SelfCheck(format!("{} has {found} values after the change, not {expected}", sheet.name)));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    const ROWS_AT_3: Shift = Shift { axis: Axis::Rows, at: 3, count: 2, delete: false };
    const DEL_ROWS_3_4: Shift = Shift { axis: Axis::Rows, at: 3, count: 2, delete: true };
    const COL_B: Shift = Shift { axis: Axis::Cols, at: 2, count: 1, delete: false };
    const DEL_COL_B: Shift = Shift { axis: Axis::Cols, at: 2, count: 1, delete: true };

    fn adj(f: &str, home: bool, s: &Shift) -> String {
        adjust_formula(f, home, "Data", s).unwrap()
    }

    #[test]
    fn insert_moves_what_is_at_or_past() {
        assert_eq!(adj("A1+A3+$B$5", true, &ROWS_AT_3), "A1+A5+$B$7");
        // Inside a range it grows; at its top it moves; after its end it is left.
        assert_eq!(adj("SUM(A1:A10)+SUM(A3:A4)+SUM(A1:A2)", true, &ROWS_AT_3), "SUM(A1:A12)+SUM(A5:A6)+SUM(A1:A2)");
        assert_eq!(adj("SUM(3:5)+SUM(A:A)", true, &ROWS_AT_3), "SUM(5:7)+SUM(A:A)");
        assert_eq!(adj("B2*C9", true, &COL_B), "C2*D9");
        assert_eq!(adj("SUM(A1:C1)+SUM($B:$B)", true, &COL_B), "SUM(A1:D1)+SUM($C:$C)");
    }

    #[test]
    fn only_the_sheet_changed() {
        assert_eq!(adj("A5+Data!A5+'Other'!A5", false, &ROWS_AT_3), "A5+Data!A7+'Other'!A5");
        assert_eq!(adj("A5+Other!A5", true, &ROWS_AT_3), "A7+Other!A5");
        assert_eq!(adjust_formula("'My ''s'!A5", false, "my 's", &ROWS_AT_3).unwrap(), "'My ''s'!A7");
        assert_eq!(adj("[1]Data!A5+\"A5\"", false, &ROWS_AT_3), "[1]Data!A5+\"A5\"");
        assert!(adjust_formula("SUM(Data:Other!A1)", false, "Data", &ROWS_AT_3).is_err());
    }

    #[test]
    fn delete_shrinks_or_breaks() {
        assert_eq!(adj("A2+A5+A3", true, &DEL_ROWS_3_4), "A2+A3+#REF!");
        assert_eq!(adj("SUM(A1:A10)+SUM(A3:A4)+SUM(A4:A6)+SUM(A2:A3)", true, &DEL_ROWS_3_4), "SUM(A1:A8)+SUM(#REF!)+SUM(A3:A4)+SUM(A2:A2)");
        assert_eq!(adj("Data!B1+SUM(A1:C1)", false, &DEL_COL_B), "Data!#REF!+SUM(A1:C1)");
        assert_eq!(adj("SUM(A1:C1)", true, &DEL_COL_B), "SUM(A1:B1)");
    }

    #[test]
    fn spans() {
        assert_eq!(ROWS_AT_3.span(1, MAX_ROW), Some((1, MAX_ROW)));
        assert_eq!(ROWS_AT_3.span(MAX_ROW, MAX_ROW), None);
        assert_eq!(DEL_ROWS_3_4.sqref("A1:A2 A3:B4 A5"), Some("A1:A2 A3".into()));
        assert!(DEL_ROWS_3_4.cuts(2, 3));
        assert!(!DEL_ROWS_3_4.cuts(3, 4));
        assert!(ROWS_AT_3.cuts(2, 3));
        assert!(!ROWS_AT_3.cuts(3, 4));
    }
}
