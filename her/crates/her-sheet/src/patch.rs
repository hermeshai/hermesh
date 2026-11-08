//! Applying cell edits as splices into the original parts.
//!
//! What changes, and nothing else:
//! - the edited worksheet parts: the edited `<c>` elements, new cells and
//!   rows, `<dimension>`, and members of a shared formula whose group an
//!   edit breaks (written out as ordinary formulas);
//! - the shared string table, when new text is added (appended);
//! - `calcChain.xml` is dropped when formulas change (Excel rebuilds it;
//!   a stale one makes Excel "repair" the file), with its relationship and
//!   content-type entry;
//! - `workbook.xml` gets `fullCalcOnLoad="1"`, so Excel recomputes values we
//!   could not (stale cached values in dependents, add-in functions).
//!
//! Every other zip entry is copied as its original compressed bytes.

use std::collections::BTreeMap;

use std::collections::HashMap;

use std::collections::HashSet;
use std::io::Cursor;
use std::io::Write;
use std::ops::Range;

use quick_xml::events::Event;
use serde::Deserialize;
use serde::Serialize;

use crate::cellref::CellRef;

use crate::Error;
use crate::cellref::Rect;
use crate::formula::normalize_for_write;
use crate::formula::translate;
use crate::package::Package;
use crate::package::SheetKind;
use crate::package::WorkbookInfo;
use crate::package::attrs;
use crate::package::parse_rels;
use crate::package::rels_part_for;
use crate::package::resolve;
use crate::scan::CellInfo;
use crate::scan::FormulaKind;
use crate::scan::SheetIndex;
use crate::scan::scan;
use crate::xmltext::encode_xstring;
use crate::xmltext::escape_xml;
use crate::xmltext::format_number;
use crate::xmltext::unescape_xml;

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type", content = "value", rename_all = "snake_case")]
pub enum Cached {
    Number(f64),
    Text(String),
    Bool(bool),
    Error(String),
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum NewValue {
    Number {
        value: f64,
    },
    Text {
        value: String,
    },
    Bool {
        value: bool,
    },
    Empty,
    /// A formula cell's newly computed result: the formula stays byte for byte, only the value
    /// Excel shows until it recalculates changes (design §5.6.4).
    Result {
        value: Cached,
    },
    /// `text` as typed (`=SUM(A1:A3)`); `cached` is the value we computed,
    /// if we could.
    Formula {
        text: String,
        cached: Option<Cached>,
    },
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct CellEdit {
    pub sheet: String,
    pub cell: String,
    #[serde(flatten)]
    pub value: NewValue,
}

#[derive(Clone, Debug)]
pub struct Options {
    pub full_calc_on_load: bool,
}

impl Default for Options {
    fn default() -> Self {
        Self {
            full_calc_on_load: true,
        }
    }
}

/// What a sheet's edit turned into, for the self-check.
#[derive(Debug, Default, Clone)]
pub struct SheetChanges {
    /// Edited cells and the stored formula text (for formulas).
    pub edited: BTreeMap<CellRef, (NewValue, Option<String>)>,
    /// Shared-formula members written out as ordinary formulas: the text.
    pub materialized: BTreeMap<CellRef, String>,
}

#[derive(Debug)]
pub struct Outcome {
    pub bytes: Vec<u8>,
    pub changed_parts: Vec<String>,
    pub removed_parts: Vec<String>,
    pub sheets: BTreeMap<String, SheetChanges>,
}

/// A replacement of `range` in a part's bytes. Zero-length ranges insert.
pub(crate) struct Splice {
    pub(crate) range: Range<usize>,
    pub(crate) text: String,
}

pub(crate) fn apply_splices(src: &[u8], mut splices: Vec<Splice>) -> Result<Vec<u8>, Error> {
    splices.sort_by_key(|s| (s.range.start, s.range.end));
    let mut out =
        Vec::with_capacity(src.len() + splices.iter().map(|s| s.text.len()).sum::<usize>());
    let mut pos = 0;
    for s in splices {
        if s.range.start < pos {
            return Err(Error::Internal(format!(
                "overlapping edits at byte {}",
                s.range.start
            )));
        }
        out.extend_from_slice(&src[pos..s.range.start]);
        out.extend_from_slice(s.text.as_bytes());
        pos = s.range.end;
    }
    out.extend_from_slice(&src[pos..]);
    Ok(out)
}

/// The shared string table, appended to but never rewritten.
struct Sst {
    xml: Vec<u8>,
    index: HashMap<String, usize>,
    unique: usize,
    count_delta: i64,
    added: Vec<String>,
    /// Old strings by index, to know what an edited cell held.
    #[allow(dead_code)]
    texts: Vec<Option<String>>,
}

impl Sst {
    fn load(xml: Vec<u8>) -> Result<Self, Error> {
        let (mut rd, base) = crate::xmltext::reader(&xml[..]);
        let mut texts = Vec::new();
        let mut cur: Option<(String, usize, bool)> = None; // text, number of <t>, has runs/phonetic
        let mut t_start = None;
        let mut depth_rph = false;
        loop {
            let start = rd.buffer_position() as usize + base;
            let ev = rd.read_event()?;
            let end = rd.buffer_position() as usize + base;
            match ev {
                Event::Start(e) => match e.local_name().as_ref() {
                    "si" => cur = Some((String::new(), 0, false)),
                    "r" => {
                        if let Some(c) = cur.as_mut() {
                            c.2 = true
                        }
                    }
                    "rPh" => {
                        depth_rph = true;
                        if let Some(c) = cur.as_mut() {
                            c.2 = true
                        }
                    }
                    "t" if !depth_rph => t_start = Some(end),
                    _ => {}
                },
                Event::Empty(e) => match e.local_name().as_ref() {
                    "si" => texts.push(Some(String::new())),
                    "t" => {
                        if let Some(c) = cur.as_mut() {
                            c.1 += 1
                        }
                    }
                    _ => {}
                },
                Event::End(e) => match e.local_name().as_ref() {
                    "t" if t_start.is_some() => {
                        let raw = String::from_utf8_lossy(&xml[t_start.take().unwrap()..start]);
                        if let Some(c) = cur.as_mut() {
                            c.0.push_str(&crate::xmltext::decode_xstring(&unescape_xml(&raw)));
                            c.1 += 1;
                        }
                    }
                    "rPh" => depth_rph = false,
                    "si" => {
                        let (text, _, _) = cur.take().unwrap_or_default();
                        texts.push(Some(text));
                    }
                    _ => {}
                },
                Event::Eof => break,
                _ => {}
            }
        }
        // Reuse only plain entries: a rich-text entry with the same letters
        // has formatting the new cell should not get.
        let mut index = HashMap::new();
        {
            let (mut rd, _) = crate::xmltext::reader(&xml[..]);
            let mut i = 0usize;
            let mut rich = false;
            loop {
                match rd.read_event()? {
                    Event::Start(e) if e.local_name().as_ref() == "si" => rich = false,
                    Event::Start(e) | Event::Empty(e)
                        if matches!(e.local_name().as_ref(), "r" | "rPh") =>
                    {
                        rich = true
                    }
                    Event::Empty(e) if e.local_name().as_ref() == "si" => {
                        index.entry(String::new()).or_insert(i);
                        i += 1;
                    }
                    Event::End(e) if e.local_name().as_ref() == "si" => {
                        if !rich && let Some(Some(t)) = texts.get(i) {
                            index.entry(t.clone()).or_insert(i);
                        }
                        i += 1;
                    }
                    Event::Eof => break,
                    _ => {}
                }
            }
        }
        let unique = texts.len();
        Ok(Self {
            xml,
            index,
            unique,
            count_delta: 0,
            added: Vec::new(),
            texts,
        })
    }

    fn intern(&mut self, s: &str) -> usize {
        if let Some(&i) = self.index.get(s) {
            return i;
        }
        let i = self.unique + self.added.len();
        self.added.push(s.to_string());
        self.index.insert(s.to_string(), i);
        i
    }

    fn render(&self) -> Result<Option<Vec<u8>>, Error> {
        if self.added.is_empty() && self.count_delta == 0 {
            return Ok(None);
        }
        // Find the root open tag (for count attributes and prefix) and </sst>.
        let (mut rd, base) = crate::xmltext::reader(&self.xml[..]);
        // The open tag's span, its namespace prefix and its attributes.
        type Root = (Range<usize>, Option<String>, HashMap<String, String>);
        let mut root: Option<Root> = None;
        let mut close = None;
        loop {
            let start = rd.buffer_position() as usize + base;
            let ev = rd.read_event()?;
            let end = rd.buffer_position() as usize + base;
            match ev {
                Event::Start(e) if e.local_name().as_ref() == "sst" => {
                    let p = e.name().prefix().map(|p| p.as_ref().to_string());
                    root = Some((start..end, p, attrs(&e)?));
                }
                Event::Empty(e) if e.local_name().as_ref() == "sst" => {
                    let p = e.name().prefix().map(|p| p.as_ref().to_string());
                    root = Some((start..end, p, attrs(&e)?));
                    close = Some(end);
                }
                Event::End(e) if e.local_name().as_ref() == "sst" => close = Some(start),
                Event::Eof => break,
                _ => {}
            }
        }
        let (open, prefix, a) =
            root.ok_or_else(|| Error::Corrupt("sharedStrings has no <sst>".into()))?;
        let close = close.ok_or_else(|| Error::Corrupt("sharedStrings not closed".into()))?;
        let p = prefix.map(|p| format!("{p}:")).unwrap_or_default();
        let mut splices = Vec::new();
        let tag = String::from_utf8_lossy(&self.xml[open.clone()]).into_owned();
        let self_closing = tag.ends_with("/>");
        let unique = (self.unique + self.added.len()).to_string();
        let mut new_tag = set_attr(&tag, "uniqueCount", &unique, a.contains_key("uniqueCount"));
        if let Some(c) = a.get("count").and_then(|c| c.parse::<i64>().ok()) {
            new_tag = set_attr(
                &new_tag,
                "count",
                &(c + self.count_delta).max(0).to_string(),
                true,
            );
        }
        let mut items = String::new();
        for s in &self.added {
            items.push_str(&format!("<{p}si>{}</{p}si>", t_element(&p, s)));
        }
        if self_closing {
            let opened = format!("{}>", new_tag.trim_end_matches("/>").trim_end());
            splices.push(Splice {
                range: open,
                text: format!("{opened}{items}</{p}sst>"),
            });
        } else {
            splices.push(Splice {
                range: open,
                text: new_tag,
            });
            splices.push(Splice {
                range: close..close,
                text: items,
            });
        }
        apply_splices(&self.xml, splices).map(Some)
    }
}

fn t_element(p: &str, s: &str) -> String {
    let needs_space =
        s.starts_with(char::is_whitespace) || s.ends_with(char::is_whitespace) || s.contains('\n');
    let space = if needs_space {
        " xml:space=\"preserve\""
    } else {
        ""
    };
    format!(
        "<{p}t{space}>{}</{p}t>",
        escape_xml(&encode_xstring(s), false)
    )
}

/// Sets `name="value"` on a start tag given as text: replaces the value if
/// present, else adds it before the tag closes.
pub(crate) fn set_attr(tag: &str, name: &str, value: &str, present: bool) -> String {
    if present {
        // Match ` name="` exactly, not a longer attribute ending in `name`.
        for q in ['"', '\''] {
            let key = format!(" {name}={q}");
            if let Some(i) = tag.find(&key) {
                let vstart = i + key.len();
                if let Some(len) = tag[vstart..].find(q) {
                    return format!(
                        "{}{}{}",
                        &tag[..vstart],
                        escape_xml(value, true),
                        &tag[vstart + len..]
                    );
                }
            }
        }
    }
    let (body, close) = if let Some(b) = tag.strip_suffix("/>") {
        (b.trim_end(), "/>")
    } else {
        (tag.strip_suffix('>').unwrap_or(tag), ">")
    };
    format!("{body} {name}=\"{}\"{close}", escape_xml(value, true))
}

struct Ctx<'a> {
    prefix: String,
    sst: Option<&'a mut Sst>,
}

impl Ctx<'_> {
    fn cell_xml(
        &mut self,
        at: CellRef,
        style: Option<&str>,
        value: &NewValue,
        stored_formula: Option<&str>,
    ) -> Result<String, Error> {
        let p = &self.prefix;
        let s = style
            .map(|s| format!(" s=\"{}\"", escape_xml(s, true)))
            .unwrap_or_default();
        Ok(match value {
            NewValue::Empty => {
                if style.is_some() {
                    format!("<{p}c r=\"{at}\"{s}/>")
                } else {
                    String::new()
                }
            }
            NewValue::Number { value } => {
                let n = format_number(*value)
                    .ok_or_else(|| Error::Unsupported("NaN or infinite number".into()))?;
                format!("<{p}c r=\"{at}\"{s}><{p}v>{n}</{p}v></{p}c>")
            }
            NewValue::Bool { value } => {
                format!(
                    "<{p}c r=\"{at}\"{s} t=\"b\"><{p}v>{}</{p}v></{p}c>",
                    *value as u8
                )
            }
            NewValue::Text { value } => {
                if value.chars().count() > 32767 {
                    return Err(Error::Unsupported(
                        "text longer than 32767 characters".into(),
                    ));
                }
                match self.sst.as_deref_mut() {
                    Some(sst) => {
                        let i = sst.intern(value);
                        sst.count_delta += 1;
                        format!("<{p}c r=\"{at}\"{s} t=\"s\"><{p}v>{i}</{p}v></{p}c>")
                    }
                    None => format!(
                        "<{p}c r=\"{at}\"{s} t=\"inlineStr\"><{p}is>{}</{p}is></{p}c>",
                        t_element(p, value)
                    ),
                }
            }
            NewValue::Formula { cached, .. } => {
                let f = format!(
                    "<{p}f>{}</{p}f>",
                    escape_xml(stored_formula.unwrap(), false)
                );
                formula_cell(p, at, &s, &f, cached.as_ref())
            }
            NewValue::Result { .. } => {
                return Err(Error::Internal(format!(
                    "{at}: a result for a cell without a formula"
                )));
            }
        })
    }
}

/// `<c>` for a formula cell: `f` is the whole `<f>` element, `cached` its result.
fn formula_cell(p: &str, at: CellRef, s: &str, f: &str, cached: Option<&Cached>) -> String {
    let (t, v) = match cached {
        None => ("", String::new()),
        Some(Cached::Number(n)) => (
            "",
            format_number(*n)
                .map(|n| format!("<{p}v>{n}</{p}v>"))
                .unwrap_or_default(),
        ),
        Some(Cached::Bool(b)) => (" t=\"b\"", format!("<{p}v>{}</{p}v>", *b as u8)),
        Some(Cached::Text(x)) => (
            " t=\"str\"",
            format!("<{p}v>{}</{p}v>", escape_xml(&encode_xstring(x), false)),
        ),
        Some(Cached::Error(e)) => (" t=\"e\"", format!("<{p}v>{}</{p}v>", escape_xml(e, false))),
    };
    format!("<{p}c r=\"{at}\"{s}{t}>{f}{v}</{p}c>")
}

/// Header rows of the tables on a sheet: their text is the column name in
/// the table part, so editing them needs a second change we do not make yet.
fn table_headers(pkg: &Package, sheet_part: &str) -> Result<Vec<Rect>, Error> {
    let Some(rels) = pkg.read(&rels_part_for(sheet_part))? else {
        return Ok(vec![]);
    };
    let mut out = Vec::new();
    for rel in parse_rels(&rels)?
        .iter()
        .filter(|r| r.is("table") && !r.external)
    {
        let part = resolve(sheet_part, &rel.target);
        let Some(xml) = pkg.read(&part)? else {
            continue;
        };
        let (mut rd, _) = crate::xmltext::reader(&xml[..]);
        loop {
            match rd.read_event()? {
                Event::Start(e) | Event::Empty(e) if e.local_name().as_ref() == "table" => {
                    let a = attrs(&e)?;
                    let header_rows: u32 = a
                        .get("headerRowCount")
                        .and_then(|v| v.parse().ok())
                        .unwrap_or(1);
                    if let Some(r) = a.get("ref").and_then(|r| Rect::parse(r))
                        && header_rows > 0
                    {
                        out.push(Rect {
                            first: r.first,
                            last: CellRef::new(r.first.row + header_rows - 1, r.last.col),
                        });
                    }
                    break;
                }
                Event::Eof => break,
                _ => {}
            }
        }
    }
    Ok(out)
}

fn is_formula_cell(c: Option<&CellInfo>) -> bool {
    c.is_some_and(|c| c.formula.is_some())
}

fn next_has_no_address(ix: &SheetIndex, at: CellRef) -> bool {
    ix.row(at.row).is_some_and(|row| {
        let mut after = row.cells.iter().skip_while(|c| c.at != at).skip(1);
        after.next().is_some_and(|c| !c.has_r)
    })
}

fn edit_sheet(
    pkg: &Package,
    sheet_part: &str,
    xml: &[u8],
    edits: &BTreeMap<CellRef, NewValue>,
    sst: Option<&mut Sst>,
) -> Result<(Vec<u8>, SheetChanges, bool), Error> {
    let ix: SheetIndex = scan(xml)?;
    if ix.protected {
        return Err(Error::Unsupported("sheet is protected".into()));
    }
    let headers = table_headers(pkg, sheet_part)?;
    let arrays: Vec<Rect> = ix
        .cells()
        .filter_map(|c| match &c.formula {
            Some(f) => match &f.kind {
                FormulaKind::Array { range } => Some(range.unwrap_or(Rect::single(c.at))),
                FormulaKind::DataTable => Some(Rect::single(c.at)),
                _ => None,
            },
            None => None,
        })
        .collect();

    let mut changes = SheetChanges::default();
    let mut touches_formulas = false;
    let mut stored: HashMap<CellRef, String> = HashMap::new();

    for (&at, value) in edits {
        if ix.merges.iter().any(|m| m.contains(at) && m.first != at) {
            return Err(Error::Unsupported(format!(
                "{at} is inside a merged range but not its top-left cell"
            )));
        }
        if headers.iter().any(|h| h.contains(at)) {
            return Err(Error::Unsupported(format!("{at} is a table header")));
        }
        if arrays.iter().any(|a| a.contains(at)) {
            return Err(Error::Unsupported(format!(
                "{at} is part of an array formula or data table"
            )));
        }
        let old = ix.cell(at);
        if old.is_some_and(|c| c.has_metadata) {
            return Err(Error::Unsupported(format!(
                "{at} holds a dynamic array or rich value"
            )));
        }
        if let NewValue::Result { .. } = value {
            if !is_formula_cell(old) {
                return Err(Error::Internal(format!(
                    "{at}: a result for a cell without a formula"
                )));
            }
            continue;
        }
        if let NewValue::Formula { text, .. } = value {
            stored.insert(at, normalize_for_write(text)?);
            touches_formulas = true;
        }
        if is_formula_cell(old) {
            touches_formulas = true;
        }
        if old.is_none() && !matches!(value, NewValue::Empty) {
            if !ix.ordered {
                return Err(Error::Unsupported(
                    "rows or cells are out of order; cannot insert".into(),
                ));
            }
            if let Some(r) = ix.row(at.row)
                && (!r.has_r || r.cells.iter().any(|c| !c.has_r))
            {
                return Err(Error::Unsupported(format!(
                    "row {} has cells without addresses; cannot insert",
                    at.row
                )));
            }
        }
    }

    let mut ctx = Ctx {
        prefix: ix
            .prefix
            .clone()
            .map(|p| format!("{p}:"))
            .unwrap_or_default(),
        sst,
    };
    let p = ctx.prefix.clone();
    let mut splices: Vec<Splice> = Vec::new();

    // Shared formulas: a group any edit touches is written out in full.
    let broken: HashSet<String> = edits
        .iter()
        .filter(|(_, v)| !matches!(v, NewValue::Result { .. }))
        .filter_map(|(at, _)| ix.cell(*at))
        .filter_map(|c| match &c.formula {
            Some(f) => match &f.kind {
                FormulaKind::Shared { si, .. } => Some(si.clone()),
                _ => None,
            },
            None => None,
        })
        .collect();
    for si in &broken {
        let members: Vec<&CellInfo> = ix
            .cells()
            .filter(|c| matches!(&c.formula, Some(f) if matches!(&f.kind, FormulaKind::Shared { si: s, .. } if s == si)))
            .collect();
        let master = members
            .iter()
            .find(|c| {
                matches!(
                    &c.formula.as_ref().unwrap().kind,
                    FormulaKind::Shared { master: true, .. }
                )
            })
            .ok_or_else(|| Error::Unsupported(format!("shared formula {si} has no master cell")))?;
        let mtext = &master.formula.as_ref().unwrap().text;
        for m in &members {
            let result_only = matches!(edits.get(&m.at), Some(NewValue::Result { .. }));
            if edits.contains_key(&m.at) && !result_only {
                continue;
            }
            let text = translate(
                mtext,
                m.at.row as i64 - master.at.row as i64,
                m.at.col as i64 - master.at.col as i64,
            )?;
            if result_only {
                // The whole cell is rewritten below, with this formula.
                changes.materialized.insert(m.at, text);
                continue;
            }
            let span = m
                .f_span
                .clone()
                .ok_or_else(|| Error::Internal("formula without span".into()))?;
            splices.push(Splice {
                range: span,
                text: format!("<{p}f>{}</{p}f>", escape_xml(&text, false)),
            });
            changes.materialized.insert(m.at, text);
        }
    }

    // Old shared strings an edit replaces no longer count.
    if let Some(sst) = ctx.sst.as_deref_mut() {
        for at in edits.keys() {
            if let Some(c) = ix.cell(*at)
                && c.t.as_deref() == Some("s")
                && c.formula.is_none()
            {
                sst.count_delta -= 1;
            }
        }
    }

    let mut new_by_row: BTreeMap<u32, Vec<(CellRef, &NewValue)>> = BTreeMap::new();
    for (&at, value) in edits {
        let f = stored.get(&at).map(String::as_str);
        match ix.cell(at) {
            Some(old) if matches!(value, NewValue::Result { .. }) => {
                let NewValue::Result { value: cached } = value else {
                    unreachable!()
                };
                let style = old
                    .s
                    .as_ref()
                    .map(|s| format!(" s=\"{}\"", escape_xml(s, true)))
                    .unwrap_or_default();
                let f = match changes.materialized.get(&at) {
                    Some(text) => format!("<{p}f>{}</{p}f>", escape_xml(text, false)),
                    None => String::from_utf8_lossy(&xml[old.f_span.clone().unwrap()]).into_owned(),
                };
                splices.push(Splice {
                    range: old.span.clone(),
                    text: formula_cell(&p, at, &style, &f, Some(cached)),
                });
            }
            Some(old) => {
                let mut xml = ctx.cell_xml(at, old.s.as_deref(), value, f)?;
                // The next cell, if it has no `r`, takes its column from this element: cleared,
                // it stays as an empty cell so that one and those after it do not slide left.
                if xml.is_empty() && next_has_no_address(&ix, at) {
                    xml = format!("<{p}c r=\"{at}\"/>");
                }
                splices.push(Splice {
                    range: old.span.clone(),
                    text: xml,
                });
            }
            None if matches!(value, NewValue::Empty) => {}
            None => new_by_row.entry(at.row).or_default().push((at, value)),
        }
        changes
            .edited
            .insert(at, (value.clone(), f.map(str::to_string)));
    }

    let mut new_rows: BTreeMap<usize, String> = BTreeMap::new(); // insert position → rows
    for (r, cells) in &new_by_row {
        match ix.row(*r) {
            Some(row) => {
                let mut at_pos: BTreeMap<usize, String> = BTreeMap::new();
                for (at, value) in cells {
                    let style = ix.default_style(*at);
                    let xml = ctx.cell_xml(
                        *at,
                        style.as_deref(),
                        value,
                        stored.get(at).map(String::as_str),
                    )?;
                    let pos = row
                        .cells
                        .iter()
                        .find(|c| c.at.col > at.col)
                        .map(|c| c.span.start)
                        .unwrap_or(row.close_start);
                    at_pos.entry(pos).or_default().push_str(&xml);
                }
                if row.self_closing {
                    let tag = String::from_utf8_lossy(&xml[row.open_tag.clone()]);
                    let open = format!("{}>", tag.trim_end_matches("/>").trim_end());
                    let body: String = at_pos.into_values().collect();
                    splices.push(Splice {
                        range: row.open_tag.clone(),
                        text: format!("{open}{body}</{p}row>"),
                    });
                } else {
                    for (pos, text) in at_pos {
                        splices.push(Splice {
                            range: pos..pos,
                            text,
                        });
                    }
                }
            }
            None => {
                let mut body = String::new();
                for (at, value) in cells {
                    let style = ix.default_style(*at);
                    body.push_str(&ctx.cell_xml(
                        *at,
                        style.as_deref(),
                        value,
                        stored.get(at).map(String::as_str),
                    )?);
                }
                let pos = ix
                    .rows
                    .iter()
                    .find(|x| x.r > *r)
                    .map(|x| x.span.start)
                    .unwrap_or(ix.sheet_data_close);
                new_rows
                    .entry(pos)
                    .or_default()
                    .push_str(&format!("<{p}row r=\"{r}\">{body}</{p}row>"));
            }
        }
    }
    if ix.sheet_data_self_closing {
        let body: String = new_rows.into_values().collect();
        if !body.is_empty() {
            let tag = String::from_utf8_lossy(&xml[ix.sheet_data_open.clone()]);
            let open = format!("{}>", tag.trim_end_matches("/>").trim_end());
            splices.push(Splice {
                range: ix.sheet_data_open.clone(),
                text: format!("{open}{body}</{p}sheetData>"),
            });
        }
    } else {
        for (pos, text) in new_rows {
            splices.push(Splice {
                range: pos..pos,
                text,
            });
        }
    }

    // <dimension>: grow it to cover new cells.
    if let Some((span, old)) = &ix.dimension {
        let mut rect = *old;
        for (&at, v) in edits {
            if !matches!(v, NewValue::Empty) {
                rect = Some(match rect {
                    Some(r) => r.union(at),
                    None => Rect::single(at),
                });
            }
        }
        if rect != *old {
            splices.push(Splice {
                range: span.clone(),
                text: format!("<{p}dimension ref=\"{}\"/>", rect.unwrap()),
            });
        }
    }

    let out = apply_splices(xml, splices)?;
    Ok((out, changes, touches_formulas || !broken.is_empty()))
}

/// Removes elements (by span) from a part.
pub(crate) fn remove_spans(xml: &[u8], spans: Vec<Range<usize>>) -> Result<Vec<u8>, Error> {
    apply_splices(
        xml,
        spans
            .into_iter()
            .map(|range| Splice {
                range,
                text: String::new(),
            })
            .collect(),
    )
}

pub(crate) fn content_type_override_span(xml: &[u8], part: &str) -> Result<Option<Range<usize>>, Error> {
    let (mut rd, base) = crate::xmltext::reader(xml);
    let want = format!("/{}", part.trim_start_matches('/')).to_lowercase();
    loop {
        let start = rd.buffer_position() as usize + base;
        let ev = rd.read_event()?;
        let end = rd.buffer_position() as usize + base;
        match ev {
            Event::Empty(e) | Event::Start(e) if e.local_name().as_ref() == "Override" => {
                if attrs(&e)?
                    .get("PartName")
                    .is_some_and(|n| n.to_lowercase() == want)
                {
                    if !xml[..end].ends_with(b"/>") {
                        rd.read_to_end(e.name())?;
                        return Ok(Some(start..rd.buffer_position() as usize + base));
                    }
                    return Ok(Some(start..end));
                }
            }
            Event::Eof => return Ok(None),
            _ => {}
        }
    }
}

/// `fullCalcOnLoad="1"` on `<calcPr>`, adding the element in schema order if absent.
pub(crate) fn set_full_calc(xml: &[u8]) -> Result<Vec<u8>, Error> {
    const AFTER_CALC_PR: &[&str] = &[
        "oleSize",
        "customWorkbookViews",
        "pivotCaches",
        "smartTagPr",
        "smartTagTypes",
        "webPublishing",
        "fileRecoveryPr",
        "webPublishObjects",
        "extLst",
    ];
    let (mut rd, base) = crate::xmltext::reader(xml);
    let mut depth = 0;
    let mut prefix = String::new();
    let mut insert_at = None;
    loop {
        let start = rd.buffer_position() as usize + base;
        let ev = rd.read_event()?;
        let end = rd.buffer_position() as usize + base;
        match ev {
            Event::Start(e) | Event::Empty(e)
                if depth == 1 && e.local_name().as_ref() == "calcPr" =>
            {
                let tag = String::from_utf8_lossy(&xml[start..end]).into_owned();
                let a = attrs(&e)?;
                if a.get("fullCalcOnLoad")
                    .is_some_and(|v| v == "1" || v == "true")
                {
                    return Ok(xml.to_vec());
                }
                let new = set_attr(
                    &tag,
                    "fullCalcOnLoad",
                    "1",
                    a.contains_key("fullCalcOnLoad"),
                );
                return apply_splices(
                    xml,
                    vec![Splice {
                        range: start..end,
                        text: new,
                    }],
                );
            }
            Event::Start(e) => {
                if depth == 0 {
                    prefix = e
                        .name()
                        .prefix()
                        .map(|p| format!("{}:", p.as_ref()))
                        .unwrap_or_default();
                } else if depth == 1
                    && insert_at.is_none()
                    && AFTER_CALC_PR.contains(&e.local_name().as_ref())
                {
                    insert_at = Some(start);
                }
                depth += 1;
            }
            Event::Empty(e) => {
                if depth == 1
                    && insert_at.is_none()
                    && AFTER_CALC_PR.contains(&e.local_name().as_ref())
                {
                    insert_at = Some(start);
                }
            }
            Event::End(_) => {
                depth -= 1;
                if depth == 0 && insert_at.is_none() {
                    insert_at = Some(start);
                }
            }
            Event::Eof => break,
            _ => {}
        }
    }
    let pos = insert_at.ok_or_else(|| Error::Corrupt("workbook.xml has no root element".into()))?;
    apply_splices(
        xml,
        vec![Splice {
            range: pos..pos,
            text: format!("<{prefix}calcPr fullCalcOnLoad=\"1\"/>"),
        }],
    )
}

pub fn apply(input: &[u8], edits: &[CellEdit], opts: &Options) -> Result<Outcome, Error> {
    let pkg = Package::open(input.to_vec())?;
    let wb = WorkbookInfo::load(&pkg)?;

    let mut by_sheet: BTreeMap<String, BTreeMap<CellRef, NewValue>> = BTreeMap::new();
    for e in edits {
        let at = CellRef::parse(&e.cell)
            .ok_or_else(|| Error::NotFound(format!("bad cell address {}", e.cell)))?;
        by_sheet
            .entry(e.sheet.clone())
            .or_default()
            .insert(at, e.value.clone());
    }

    // Loaded for any edit: replacing a string cell changes the table's count.
    let mut sst = match (&wb.shared_strings, !edits.is_empty()) {
        (Some(part), true) => match pkg.read(part)? {
            Some(x) => Some(Sst::load(x)?),
            None => None,
        },
        _ => None,
    };

    let mut changed: BTreeMap<String, Vec<u8>> = BTreeMap::new();
    let mut removed: Vec<String> = Vec::new();
    let mut sheets = BTreeMap::new();
    let mut formulas_touched = false;

    for (name, cells) in &by_sheet {
        let entry = wb
            .sheet(name)
            .ok_or_else(|| Error::NotFound(format!("no sheet named {name}")))?;
        if entry.kind != SheetKind::Worksheet {
            return Err(Error::Unsupported(format!("{name} is not a worksheet")));
        }
        let part = entry
            .part
            .clone()
            .ok_or_else(|| Error::Corrupt(format!("{name} has no part")))?;
        let xml = pkg
            .read(&part)?
            .ok_or_else(|| Error::Corrupt(format!("missing {part}")))?;
        let (out, ch, f) = edit_sheet(&pkg, &part, &xml, cells, sst.as_mut())?;
        formulas_touched |= f;
        changed.insert(pkg.entry_name(&part).unwrap().to_string(), out);
        sheets.insert(name.clone(), ch);
    }

    if let (Some(sst), Some(part)) = (&sst, &wb.shared_strings)
        && let Some(x) = sst.render()?
    {
        changed.insert(pkg.entry_name(part).unwrap().to_string(), x);
    }

    if formulas_touched && let Some(rel) = &wb.calc_chain {
        let part = resolve(&wb.part, &rel.target);
        if let Some(entry) = pkg.entry_name(&part) {
            removed.push(entry.to_string());
            let rels = pkg.read(&wb.rels_part)?.unwrap();
            changed.insert(
                pkg.entry_name(&wb.rels_part).unwrap().to_string(),
                remove_spans(&rels, vec![rel.span.clone()])?,
            );
            let ct = pkg
                .read("[Content_Types].xml")?
                .ok_or_else(|| Error::Corrupt("no [Content_Types].xml".into()))?;
            if let Some(span) = content_type_override_span(&ct, &part)? {
                changed.insert(
                    pkg.entry_name("[Content_Types].xml").unwrap().to_string(),
                    remove_spans(&ct, vec![span])?,
                );
            }
        }
    }

    if opts.full_calc_on_load && !edits.is_empty() {
        let xml = pkg.read(&wb.part)?.unwrap();
        let new = set_full_calc(&xml)?;
        if new != xml {
            changed.insert(pkg.entry_name(&wb.part).unwrap().to_string(), new);
        }
    }

    let bytes = write_package(&pkg, &changed, &removed)?;
    Ok(Outcome {
        bytes,
        changed_parts: changed.keys().cloned().collect(),
        removed_parts: removed,
        sheets,
    })
}

pub(crate) fn write_package(
    pkg: &Package,
    changed: &BTreeMap<String, Vec<u8>>,
    removed: &[String],
) -> Result<Vec<u8>, Error> {
    let mut zin = zip::ZipArchive::new(Cursor::new(&pkg.data[..]))?;
    let mut zout = zip::ZipWriter::new(Cursor::new(Vec::with_capacity(pkg.data.len() + 4096)));
    for i in 0..zin.len() {
        let f = zin.by_index_raw(i)?;
        let name = f.name()?.to_string();
        if removed.contains(&name) {
            continue;
        }
        match changed.get(&name) {
            Some(bytes) => {
                let mut opts = zip::write::SimpleFileOptions::default()
                    .compression_method(zip::CompressionMethod::Deflated)
                    .large_file(bytes.len() as u64 >= u32::MAX as u64);
                if let Some(t) = f.last_modified() {
                    opts = opts.last_modified_time(t);
                }
                drop(f);
                zout.start_file(name, opts)?;
                zout.write_all(bytes)?;
            }
            None => zout.raw_copy_file(f)?,
        }
    }
    Ok(zout.finish()?.into_inner())
}
