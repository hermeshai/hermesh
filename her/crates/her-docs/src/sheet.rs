//! The spreadsheet side of the worker (spreadsheet-design §5.7): a workbook's working copy, read
//! as cells for the grid and the agent, changed by surgical writes, with a calculation engine
//! kept beside it for the impact of each edit.
//!
//! The grid shows what the working copy holds (§5.6.3): Excel's values until something changes,
//! then the results we wrote. The engine only answers "what changes if…".

use std::collections::BTreeSet;
use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;

use her_protocol::doc::CellChange;
use her_protocol::doc::CellData;
use her_protocol::doc::CellInput;
use her_protocol::doc::CellLook;
use her_protocol::doc::CellValue;
use her_protocol::doc::ColumnWidth;
use her_protocol::doc::RowHeight;
use her_protocol::doc::Impact;
use her_protocol::doc::SheetChart;
use her_protocol::doc::SheetChartCorner;
use her_protocol::doc::SheetChartGrouping;
use her_protocol::doc::SheetChartKind;
use her_protocol::doc::SheetChartSeries;
use her_protocol::doc::SheetInfo;
use her_protocol::doc::SheetOp;
use her_protocol::doc::SheetStructure;
use her_protocol::doc::StructureKind;
use crate::kind::DocKind;
use her_sheet::CellEdit;
use her_sheet::NewValue;
use her_sheet::Options;
use her_sheet::book::Book;
use her_sheet::book::Value;
use her_sheet::calc::Engine;
use her_sheet::csv::Csv;
use her_sheet::xls::XlsBook;
use her_sheet::cellref::CellRef;
use her_sheet::cellref::Rect;
use her_sheet::deps::Node;
use her_sheet::formula::display;
use her_sheet::package::SheetKind;
use serde::Deserialize;
use serde::Serialize;

/// A request for more cells than this is cut short.
pub const MAX_CELLS: u64 = 10_000;
/// Cells named one by one in an impact; beyond this only the count.
const MAX_AFFECTED: usize = 5_000;
/// The prefix of a refusal (§5.3.3), so the kernel can tell it from a failure.
pub const REFUSED: &str = "refused: ";

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct SheetOpened {
    pub sheets: Vec<SheetInfo>,
    /// Why edits come back without their impact, when the workbook cannot be calculated here.
    pub calc_unavailable: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Cells {
    pub cells: Vec<CellData>,
    pub merges: Vec<String>,
    pub rows: Vec<RowHeight>,
}

/// Excel's column width (characters of the default font, Calibri 11) in pixels.
fn width_px(chars: f64) -> f32 {
    (chars * 7.0 + 5.0).round() as f32
}

/// Points to pixels.
fn height_px(points: f64) -> f32 {
    (points * 4.0 / 3.0).round() as f32
}

pub use her_protocol::doc::FillDirection;
pub use her_protocol::doc::FoundCell as Found;
pub use her_protocol::doc::TraceDirection as Direction;

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Applied {
    pub impact: Impact,
    /// What puts the cells back as they were.
    pub undo: Vec<SheetOp>,
}

struct SheetDoc {
    path: PathBuf,
    bytes: Vec<u8>,
    book: Book,
    engine: Result<Engine, String>,
}

/// A flat cell: where, its value, its shown text, its formula (an `.xls` keeps them as saved).
type FlatCell = (CellRef, Value, String, Option<String>);

/// A CSV or a legacy `.xls`: values only, no styles, no calculation (spreadsheet-design §10).
enum Flat {
    Csv { path: PathBuf, csv: Csv, name: String },
    Xls(XlsBook),
}

/// An open workbook, by the kind it is.
enum Open {
    Xlsx(Box<SheetDoc>),
    Flat(Flat),
}

#[derive(Default)]
pub struct Sheets {
    docs: HashMap<u64, Open>,
}

const CSV_WIDTH: f32 = 96.0;

fn flat_sheet(name: &str, rows: u32, cols: u32) -> SheetInfo {
    SheetInfo {
        name: name.to_string(),
        chart: false,
        used: (rows > 0 && cols > 0).then(|| format!("A1:{}", CellRef::new(rows, cols))),
        frozen: None,
        hidden: false,
        protected: false,
        columns: Vec::new(),
        default_width: CSV_WIDTH,
        default_height: 20.0,
        charts: Vec::new(),
    }
}

impl Flat {
    /// A sheet's cells within `rect` (all of them without), in row order: value, shown text,
    /// formula. Only the rows asked for are looked at: scrolling a long CSV stays cheap.
    fn cells(&self, name: &str, rect: Option<Rect>) -> Option<Vec<FlatCell>> {
        let (first, last) = rect.map_or((1, u32::MAX), |r| (r.first.row, r.last.row));
        let inside = |at: CellRef| rect.is_none_or(|r| r.contains(at));
        match self {
            Flat::Csv { csv, name: n, .. } if n == name => Some(
                csv.records
                    .iter()
                    .enumerate()
                    .skip(first as usize - 1)
                    .take((last - first) as usize + 1)
                    .flat_map(|(r, rec)| rec.fields.iter().enumerate().map(move |(c, f)| (CellRef::new(r as u32 + 1, c as u32 + 1), f)))
                    .filter(|(at, f)| !f.text.is_empty() && inside(*at))
                    .map(|(at, f)| (at, csv.value(at), f.text.clone(), None))
                    .collect(),
            ),
            Flat::Xls(book) => book.sheets.iter().find(|s| s.name == name).map(|s| {
                s.cells
                    .range(CellRef::new(first, 0)..=CellRef::new(last, u32::MAX))
                    .filter(|(at, _)| inside(**at))
                    .map(|(at, c)| (*at, c.value.clone(), c.text.clone(), c.formula.clone()))
                    .collect()
            }),
            _ => None,
        }
    }

    fn names(&self) -> Vec<String> {
        match self {
            Flat::Csv { name, .. } => vec![name.clone()],
            Flat::Xls(book) => book.sheets.iter().map(|s| s.name.clone()).collect(),
        }
    }
}

fn engine_for(bytes: &[u8]) -> Result<Engine, String> {
    let book = Book::open(bytes).map_err(|e| e.to_string())?;
    Engine::new(book).map_err(|e| e.to_string())
}

fn refusal(e: her_sheet::Error) -> String {
    match e {
        her_sheet::Error::Unsupported(m) | her_sheet::Error::Formula(m) => format!("{REFUSED}{m}"),
        other => other.to_string(),
    }
}

fn protocol_chart(c: &her_sheet::chart::Chart) -> SheetChart {
    use her_sheet::chart::Grouping;
    use her_sheet::chart::Kind;
    let corner = |k: her_sheet::chart::Corner| SheetChartCorner { col: k.col, row: k.row, dx: k.dx, dy: k.dy };
    SheetChart {
        kind: match c.kind {
            Kind::Bar => SheetChartKind::Bar,
            Kind::Line => SheetChartKind::Line,
            Kind::Area => SheetChartKind::Area,
            Kind::Pie => SheetChartKind::Pie,
            Kind::Doughnut => SheetChartKind::Doughnut,
            Kind::Scatter => SheetChartKind::Scatter,
            Kind::Radar => SheetChartKind::Radar,
        },
        horizontal: c.horizontal,
        grouping: match c.grouping {
            Grouping::Clustered => SheetChartGrouping::Clustered,
            Grouping::Stacked => SheetChartGrouping::Stacked,
            Grouping::Percent => SheetChartGrouping::Percent,
        },
        title: c.title.clone(),
        legend: c.legend.clone(),
        labels: c.labels,
        categories: c.categories.clone(),
        series: c.series.iter().map(|s| SheetChartSeries { name: s.name.clone(), color: s.color.clone(), values: s.values.clone(), x: s.x.clone() }).collect(),
        min: c.min,
        max: c.max,
        from: corner(c.from),
        to: c.to.map(corner),
        width: c.width,
        height: c.height,
    }
}

fn protocol_value(v: &Value) -> Option<CellValue> {
    match v {
        Value::Number(n) => Some(CellValue::Number(*n)),
        Value::Text(t) => Some(CellValue::Text(t.clone())),
        Value::Bool(b) => Some(CellValue::Bool(*b)),
        Value::Error(e) => Some(CellValue::Error(e.clone())),
        Value::Empty => None,
    }
}

impl Sheets {
    /// Opens a working copy, its kind by its extension; `name`: a CSV's sheet, else its file's stem.
    pub fn open(&mut self, handle: u64, path: &Path, name: Option<&str>) -> Result<SheetOpened, String> {
        let bytes = std::fs::read(path).map_err(|e| e.to_string())?;
        let kind = path.extension().and_then(|e| e.to_str()).and_then(DocKind::from_ext);
        if let Some(kind @ (DocKind::Csv | DocKind::Tsv)) = kind {
            let csv = Csv::parse(&bytes, kind == DocKind::Tsv).map_err(|e| e.to_string())?;
            let name = name.map(str::to_string).or_else(|| path.file_stem().map(|s| s.to_string_lossy().into_owned())).unwrap_or_else(|| "CSV".into());
            let sheets = vec![flat_sheet(&name, csv.rows(), csv.cols())];
            self.docs.insert(handle, Open::Flat(Flat::Csv { path: path.to_path_buf(), csv, name }));
            return Ok(SheetOpened { sheets, calc_unavailable: Some("a CSV holds values only".into()) });
        }
        if kind == Some(DocKind::Xls) {
            let book = XlsBook::open(&bytes).map_err(|e| e.to_string())?;
            let sheets = book
                .sheets
                .iter()
                .map(|s| {
                    let last = s.cells.keys().fold(CellRef::new(0, 0), |m, c| CellRef::new(m.row.max(c.row), m.col.max(c.col)));
                    flat_sheet(&s.name, last.row, last.col)
                })
                .collect();
            self.docs.insert(handle, Open::Flat(Flat::Xls(book)));
            return Ok(SheetOpened { sheets, calc_unavailable: Some("a legacy .xls is read only".into()) });
        }
        let book = Book::open(&bytes).map_err(|e| match e {
            her_sheet::Error::Unsupported(m) => m,
            e => format!("cannot open the workbook: {e}"),
        })?;
        let sheets = book
            .sheets
            .iter()
            .zip(&book.info.sheets)
            .filter(|(s, _)| s.kind != SheetKind::Other)
            .map(|(s, entry)| {
                let ix = s.index.as_ref();
                let used = ix.and_then(used_range).map(|r| r.to_string());
                SheetInfo {
                    name: s.name.clone(),
                    chart: s.kind == SheetKind::Chartsheet,
                    used,
                    frozen: ix.and_then(|ix| ix.frozen),
                    hidden: entry.hidden,
                    protected: ix.is_some_and(|ix| ix.protected),
                    columns: ix
                        .map(|ix| ix.cols.iter().filter(|c| c.width.is_some() || c.hidden).map(|c| ColumnWidth { first: c.min, last: c.max, width: width_px(c.width.unwrap_or(8.43)), hidden: c.hidden }).collect())
                        .unwrap_or_default(),
                    default_width: width_px(ix.and_then(|ix| ix.default_width).unwrap_or(8.43)),
                    default_height: height_px(ix.and_then(|ix| ix.default_height).unwrap_or(15.0)),
                    charts: s.charts.iter().map(protocol_chart).collect(),
                }
            })
            .collect();
        let engine = engine_for(&bytes);
        let calc_unavailable = engine.as_ref().err().cloned();
        self.docs.insert(handle, Open::Xlsx(Box::new(SheetDoc { path: path.to_path_buf(), bytes, book, engine })));
        Ok(SheetOpened { sheets, calc_unavailable })
    }

    pub fn close(&mut self, handle: u64) -> bool {
        self.docs.remove(&handle).is_some()
    }

    fn flat_cells(flat: &Flat, sheet: &str, range: &str) -> Result<Cells, String> {
        let rect = Rect::parse(range).ok_or_else(|| format!("not a range: {range}"))?;
        let all = flat.cells(sheet, Some(rect)).ok_or_else(|| format!("no sheet named {sheet}"))?;
        let cells = all
            .into_iter()
            .map(|(at, value, text, formula)| CellData { cell: at.to_string(), value: protocol_value(&value), text, formula: formula.map(|f| formula_text(&f)), look: CellLook::default() })
            .collect();
        Ok(Cells { cells, merges: Vec::new(), rows: Vec::new() })
    }

    /// A CSV edit: written at once, every other row as it was; `.xls` is refused.
    fn flat_apply(flat: &mut Flat, ops: &[SheetOp], preview: bool) -> Result<Applied, String> {
        let Flat::Csv { path, csv, name } = flat else {
            return Err(format!("{REFUSED}a legacy .xls is read only here: open it in your spreadsheet app and save it as .xlsx to edit it"));
        };
        let mut edits: Vec<(CellRef, NewValue)> = Vec::new();
        for op in ops {
            let target = match op { SheetOp::SetValue { sheet, .. } | SheetOp::SetFormula { sheet, .. } | SheetOp::Clear { sheet, .. } => sheet };
            if target != name { return Err(format!("no sheet named {target}")); }
            match op {
                SheetOp::SetValue { cell, value, .. } => {
                    let at = CellRef::parse(cell).ok_or_else(|| format!("not a cell: {cell}"))?;
                    edits.push((at, new_value(value)));
                }
                SheetOp::SetFormula { .. } => return Err(format!("{REFUSED}a CSV keeps values only, not formulas: type the value, or save the file as .xlsx")),
                SheetOp::Clear { range, .. } => {
                    let r = Rect::parse(range).ok_or_else(|| format!("not a range: {range}"))?;
                    let rows = csv.records.iter().enumerate().skip(r.first.row as usize - 1).take((r.last.row - r.first.row) as usize + 1);
                    for (i, rec) in rows {
                        for (j, f) in rec.fields.iter().enumerate() {
                            let at = CellRef::new(i as u32 + 1, j as u32 + 1);
                            if r.contains(at) && !f.text.is_empty() {
                                edits.push((at, NewValue::Empty));
                            }
                        }
                    }
                }
            }
        }
        if edits.is_empty() || edits.len() as u64 > MAX_CELLS {
            return Err(format!("{REFUSED}edit between 1 and {MAX_CELLS} cells at a time"));
        }
        let shown = |v: &Value| match v {
            Value::Number(n) => her_sheet::styles::general(*n),
            Value::Text(t) => t.clone(),
            Value::Bool(b) => (if *b { "TRUE" } else { "FALSE" }).into(),
            _ => String::new(),
        };
        let undo = edits
            .iter()
            .map(|(at, _)| match csv.text_at(*at).unwrap_or("") {
                "" => SheetOp::Clear { sheet: name.clone(), range: at.to_string() },
                t => SheetOp::SetValue { sheet: name.clone(), cell: at.to_string(), value: CellInput::Text(t.to_string()) },
            })
            .collect();
        let edited = edits
            .iter()
            .map(|(at, v)| {
                let after = match v {
                    NewValue::Number { value } => Value::Number(*value),
                    NewValue::Text { value } => Value::Text(value.clone()),
                    NewValue::Bool { value } => Value::Bool(*value),
                    _ => Value::Empty,
                };
                let before = csv.value(*at);
                CellChange { sheet: name.clone(), cell: at.to_string(), before_text: shown(&before), after_text: shown(&after), before: protocol_value(&before), after: protocol_value(&after), label: None }
            })
            .collect();
        if !preview {
            let bytes = csv.write(&edits).map_err(refusal)?;
            crate::atomic::write(path, &bytes).map_err(|e| e.to_string())?;
            *csv = Csv::parse(&bytes, csv.delimiter == '\t').map_err(|e| e.to_string())?;
        }
        Ok(Applied { impact: Impact { edited, converged: true, ..Default::default() }, undo })
    }

    fn doc(&mut self, handle: u64) -> Result<&mut SheetDoc, String> {
        match self.docs.get_mut(&handle) {
            Some(Open::Xlsx(doc)) => Ok(doc),
            _ => Err("the document is not open".to_string()),
        }
    }

    fn flat(&mut self, handle: u64) -> Option<&mut Flat> {
        match self.docs.get_mut(&handle) {
            Some(Open::Flat(flat)) => Some(flat),
            _ => None,
        }
    }

    pub fn cells(&mut self, handle: u64, sheet: &str, range: &str) -> Result<Cells, String> {
        if let Some(flat) = self.flat(handle) {
            return Self::flat_cells(flat, sheet, range);
        }
        let doc = self.doc(handle)?;
        let book = &doc.book;
        let s = book.sheet_index(sheet).ok_or_else(|| format!("no sheet named {sheet}"))?;
        let rect = Rect::parse(range).ok_or_else(|| format!("not a range: {range}"))?;
        let size = (rect.last.row - rect.first.row + 1) as u64 * (rect.last.col - rect.first.col + 1) as u64;
        if size > MAX_CELLS {
            return Err(format!("{range} has {size} cells; ask for at most {MAX_CELLS} at a time"));
        }
        let Some(ix) = &book.sheets[s].index else { return Ok(Cells { cells: vec![], merges: vec![], rows: vec![] }) };
        let mut cells = Vec::new();
        for row in ix.rows.iter().filter(|r| r.r >= rect.first.row && r.r <= rect.last.row) {
            for c in row.cells.iter().filter(|c| rect.contains(c.at)) {
                let value = book.value(s, c.at);
                let style = book.style(s, c.at);
                let (text, format_color) = book.display(&value, &style);
                let look = CellLook {
                    bold: style.bold,
                    italic: style.italic,
                    color: format_color.map(str::to_string).or(style.color.clone()),
                    fill: style.fill.clone(),
                    align: style.align.clone(),
                };
                let formula = book.formula(s, c.at).map(|f| formula_text(&display(f)));
                if value == Value::Empty && formula.is_none() && look == CellLook::default() {
                    continue;
                }
                cells.push(CellData { cell: c.at.to_string(), value: protocol_value(&value), text, formula, look });
            }
        }
        let merges = ix
            .merges
            .iter()
            .filter(|m| m.intersects(&rect))
            .map(ToString::to_string)
            .collect();
        let rows = ix
            .rows
            .iter()
            .filter(|r| r.r >= rect.first.row && r.r <= rect.last.row && (r.height.is_some() || r.hidden))
            .map(|r| RowHeight { row: r.r, height: r.height.map(height_px).unwrap_or(0.0), hidden: r.hidden })
            .collect();
        Ok(Cells { cells, merges, rows })
    }

    pub fn recalc(&mut self, handle: u64, circular_only: bool) -> Result<Impact, String> {
        if self.flat(handle).is_some() { return Err(format!("{REFUSED}this format has no calculation engine")); }
        let doc = self.doc(handle)?;
        let engine = doc.engine.as_mut().map_err(|e| format!("{REFUSED}{e}"))?;
        // The engine retains original input/cache metadata; compare against what the grid
        // currently displays, including caches written by intervening edits.
        engine.book = Book::open(&doc.bytes).map_err(|e| e.to_string())?;
        let impact = engine.check_recalculation(circular_only).map_err(refusal)?;
        Ok(protocol_impact(&doc.book, &doc.book, &[], Vec::new(), Some(&impact), &doc.engine))
    }

    pub fn apply(&mut self, handle: u64, ops: &[SheetOp], preview: bool) -> Result<Applied, String> {
        if let Some(flat) = self.flat(handle) {
            return Self::flat_apply(flat, ops, preview);
        }
        let doc = self.doc(handle)?;
        let edits = edits_of(&doc.book, ops)?;
        let undo = undo_of(&doc.book, &edits);
        let before = |doc: &SheetDoc, n: Node| doc.book.value(n.0, n.1);
        let impact = match &mut doc.engine {
            Ok(engine) => {
                let result = if preview { engine.preview(&edits) } else { engine.apply(&edits) };
                match result {
                    Ok(i) => Some(i),
                    Err(e) => {
                        // The engine may hold part of the edit: start it again from the file.
                        doc.engine = engine_for(&doc.bytes);
                        return Err(refusal(e));
                    }
                }
            }
            Err(_) => None,
        };
        if !preview {
            let writes: Vec<CellEdit> = match (&doc.engine, &impact) {
                (Ok(engine), Some(impact)) => engine.writes(&edits, impact),
                _ => edits.iter().map(|(n, v)| CellEdit { sheet: doc.book.sheets[n.0].name.clone(), cell: n.1.to_string(), value: v.clone() }).collect(),
            };
            let out = match her_sheet::write(&doc.bytes, &writes, &Options::default()) {
                Ok(out) => out,
                Err(e) => {
                    doc.engine = engine_for(&doc.bytes);
                    return Err(refusal(e));
                }
            };
            // The working copy is never half written.
            crate::atomic::write(&doc.path, &out.bytes).map_err(|e| e.to_string())?;
            let book = Book::open(&out.bytes).map_err(|e| e.to_string())?;
            let edited_before: Vec<Value> = edits.iter().map(|(n, _)| before(doc, *n)).collect();
            let old_book = std::mem::replace(&mut doc.book, book);
            doc.bytes = out.bytes;
            return Ok(Applied { impact: protocol_impact(&old_book, &doc.book, &edits, edited_before, impact.as_ref(), &doc.engine), undo });
        }
        let edited_before: Vec<Value> = edits.iter().map(|(n, _)| before(doc, *n)).collect();
        Ok(Applied { impact: protocol_impact(&doc.book, &doc.book, &edits, edited_before, impact.as_ref(), &doc.engine), undo })
    }
}

impl Sheets {
    fn found(book: &Book, n: Node, depth: u32, via: Option<Node>) -> Found {
        let style = book.style(n.0, n.1);
        Found {
            sheet: book.sheets[n.0].name.clone(),
            cell: n.1.to_string(),
            text: book.display(&book.value(n.0, n.1), &style).0,
            formula: book.formula(n.0, n.1).map(|f| formula_text(&display(f))),
            label: book.label(n.0, n.1),
            depth,
            via: via.map(|v| format!("{}!{}", book.sheets[v.0].name, v.1)),
        }
    }

    /// Cells whose shown text, or formula, contains `query` (case folded), in sheet order.
    pub fn find(&mut self, handle: u64, query: &str, limit: usize) -> Result<Vec<Found>, String> {
        if let Some(flat) = self.flat(handle) {
            let q = query.to_lowercase();
            let mut out = Vec::new();
            for name in flat.names() {
                for (at, _, text, formula) in flat.cells(&name, None).unwrap_or_default() {
                    if text.to_lowercase().contains(&q) || formula.as_deref().is_some_and(|f| f.to_lowercase().contains(&q)) {
                        out.push(Found { sheet: name.clone(), cell: at.to_string(), text, formula: formula.map(|f| formula_text(&f)), label: None, depth: 0, via: None });
                        if out.len() >= limit {
                            return Ok(out);
                        }
                    }
                }
            }
            return Ok(out);
        }
        let book = &self.doc(handle)?.book;
        let q = query.to_lowercase();
        let mut out = Vec::new();
        for (s, sheet) in book.sheets.iter().enumerate() {
            let Some(ix) = &sheet.index else { continue };
            for c in ix.cells() {
                let style = book.style(s, c.at);
                let text = book.display(&book.value(s, c.at), &style).0;
                let formula = book.formula(s, c.at).unwrap_or("");
                if text.to_lowercase().contains(&q) || display(formula).to_lowercase().contains(&q) {
                    out.push(Self::found(book, (s, c.at), 0, None));
                    if out.len() >= limit {
                        return Ok(out);
                    }
                }
            }
        }
        Ok(out)
    }

    /// The cells a cell reads (up) or the formulas that read it (down), `depth` steps out.
    pub fn trace(&mut self, handle: u64, sheet: &str, cell: &str, direction: Direction, depth: u32) -> Result<Vec<Found>, String> {
        if self.flat(handle).is_some() {
            return Err("tracing formulas needs an .xlsx: this file has values only, or is a legacy .xls".into());
        }
        let doc = self.doc(handle)?;
        let engine = doc.engine.as_ref().map_err(Clone::clone)?;
        let book = &doc.book;
        let s = book.sheet_index(sheet).ok_or_else(|| format!("no sheet named {sheet}"))?;
        let start = (s, CellRef::parse(cell).ok_or_else(|| format!("not a cell: {cell}"))?);
        let mut seen = BTreeSet::from([start]);
        let mut frontier = vec![start];
        let mut out = Vec::new();
        for d in 1..=depth.clamp(1, 10) {
            let mut next = Vec::new();
            for n in frontier {
                let step: Vec<Node> = match direction {
                    Direction::Down => engine.graph.readers(n),
                    // Every cell a formula reads, inputs included, one area at a time.
                    Direction::Up => engine
                        .graph
                        .areas
                        .get(&n)
                        .into_iter()
                        .flatten()
                        .flat_map(|a| {
                            let ix = book.sheets.get(a.sheet).and_then(|s| s.index.as_ref());
                            ix.into_iter().flat_map(move |ix| {
                                ix.cells().filter(move |c| a.contains(c.at)).map(move |c| (a.sheet, c.at))
                            })
                        })
                        .collect(),
                };
                for m in step {
                    if seen.insert(m) {
                        out.push(Self::found(book, m, d, Some(n)));
                        next.push(m);
                        if out.len() >= 500 {
                            return Ok(out);
                        }
                    }
                }
            }
            frontier = next;
        }
        Ok(out)
    }

    /// Excel's fill down (⌘D) or right (⌘R) as plain edits: the range's first row (or column)
    /// copied across it, formulas moved by `formula::translate` as Excel moves them. A source
    /// cell that is empty empties its targets; an error value is refused, as Excel would keep
    /// it but a typed value would turn it into text.
    pub fn fill(&mut self, handle: u64, sheet: &str, range: &str, direction: FillDirection) -> Result<Vec<SheetOp>, String> {
        let rect = Rect::parse(range).ok_or_else(|| format!("not a range: {range}"))?;
        let (first, last) = (rect.first, rect.last);
        let targets = match direction {
            FillDirection::Down => (last.row - first.row) as u64 * (last.col - first.col + 1) as u64,
            FillDirection::Right => (last.col - first.col) as u64 * (last.row - first.row + 1) as u64,
        };
        if targets == 0 {
            return Err(format!("{REFUSED}select the cells to fill as well as the ones to copy"));
        }
        if targets > MAX_CELLS {
            return Err(format!("{range} fills {targets} cells; fill at most {MAX_CELLS} at a time"));
        }
        // Each source cell, as (value, stored formula).
        let source: Box<dyn Fn(CellRef) -> (Value, Option<String>)> = if let Some(flat) = self.flat(handle) {
            let cells: HashMap<CellRef, (Value, Option<String>)> = flat.cells(sheet, Some(rect)).ok_or_else(|| format!("no sheet named {sheet}"))?.into_iter().map(|(at, v, _, f)| (at, (v, f))).collect();
            Box::new(move |at| cells.get(&at).cloned().unwrap_or((Value::Empty, None)))
        } else {
            let doc = self.doc(handle)?;
            let book = &doc.book;
            let s = book.sheet_index(sheet).ok_or_else(|| format!("no sheet named {sheet}"))?;
            let cells: HashMap<CellRef, (Value, Option<String>)> = match direction {
                FillDirection::Down => (first.col..=last.col).map(|c| CellRef::new(first.row, c)).collect::<Vec<_>>(),
                FillDirection::Right => (first.row..=last.row).map(|r| CellRef::new(r, first.col)).collect(),
            }
            .into_iter()
            .map(|at| (at, (book.value(s, at), book.formula(s, at).map(str::to_string))))
            .collect();
            Box::new(move |at| cells.get(&at).cloned().unwrap_or((Value::Empty, None)))
        };
        let mut ops = Vec::new();
        let pairs: Vec<(CellRef, CellRef)> = match direction {
            FillDirection::Down => (first.col..=last.col).flat_map(|c| (first.row + 1..=last.row).map(move |r| (CellRef::new(first.row, c), CellRef::new(r, c)))).collect(),
            FillDirection::Right => (first.row..=last.row).flat_map(|r| (first.col + 1..=last.col).map(move |c| (CellRef::new(r, first.col), CellRef::new(r, c)))).collect(),
        };
        for (from, to) in pairs {
            let (value, formula) = source(from);
            ops.push(copy_op(sheet, from, to, value, formula, "fill")?);
        }
        Ok(ops)
    }

    /// Inserts or deletes rows or columns (Excel's 插入 / 删除): the working copy written, and
    /// the sheets as they are now (their extents and column widths moved).
    pub fn structure(&mut self, handle: u64, change: &SheetStructure) -> Result<SheetOpened, String> {
        if change.count == 0 || change.count > MAX_STRUCTURE {
            return Err(format!("{REFUSED}insert or delete between 1 and {MAX_STRUCTURE} rows or columns at a time"));
        }
        let shift = shift_of(change);
        match self.docs.get(&handle) {
            Some(Open::Xlsx(doc)) => {
                let out = her_sheet::structure::shift(&doc.bytes, &change.sheet, shift).map_err(refusal)?;
                crate::atomic::write(&doc.path, &out).map_err(|e| e.to_string())?;
            }
            Some(Open::Flat(Flat::Csv { path, csv, name })) => {
                if *name != change.sheet {
                    return Err(format!("no sheet named {}", change.sheet));
                }
                let out = csv.reshape(shift).map_err(refusal)?;
                crate::atomic::write(path, &out).map_err(|e| e.to_string())?;
            }
            Some(Open::Flat(Flat::Xls(_))) => return Err(format!("{REFUSED}a legacy .xls is read only here: open it in your spreadsheet app and save it as .xlsx to edit it")),
            None => return Err("the document is not open".into()),
        }
        self.reload(handle)
    }

    /// The working copy read again, as it is on disk now.
    pub fn reload(&mut self, handle: u64) -> Result<SheetOpened, String> {
        let (path, name) = match self.docs.get(&handle) {
            Some(Open::Xlsx(doc)) => (doc.path.clone(), None),
            Some(Open::Flat(Flat::Csv { path, name, .. })) => (path.clone(), Some(name.clone())),
            Some(Open::Flat(Flat::Xls(_))) => return Err(format!("{REFUSED}a legacy .xls is read only here")),
            None => return Err("the document is not open".into()),
        };
        self.open(handle, &path, name.as_deref())
    }

    /// Excel's 排序 as plain edits: the rows of `range` (its first left in place with
    /// `header`) put in order of the values in column `by`. As Excel orders them: numbers,
    /// then text (case folded), then TRUE/FALSE, then errors; blanks last either way; ties
    /// keep their order. A formula moves with its row, its relative references shifted.
    pub fn sort(&mut self, handle: u64, sheet: &str, range: &str, by: &str, descending: bool, header: bool) -> Result<Vec<SheetOp>, String> {
        let rect = Rect::parse(range).ok_or_else(|| format!("not a range: {range}"))?;
        let col = her_sheet::cellref::parse_col(by.trim()).ok_or_else(|| format!("not a column: {by}"))?;
        if col < rect.first.col || col > rect.last.col {
            return Err(format!("{REFUSED}column {by} is not in {range}"));
        }
        let size = (rect.last.row - rect.first.row + 1) as u64 * (rect.last.col - rect.first.col + 1) as u64;
        if size > MAX_CELLS {
            return Err(format!("{range} has {size} cells; sort at most {MAX_CELLS} at a time"));
        }
        let first = rect.first.row + u32::from(header);
        if first >= rect.last.row {
            return Err(format!("{REFUSED}select at least two rows to sort"));
        }
        let cells: HashMap<CellRef, (Value, Option<String>)> = if let Some(flat) = self.flat(handle) {
            flat.cells(sheet, Some(rect)).ok_or_else(|| format!("no sheet named {sheet}"))?.into_iter().map(|(at, v, _, f)| (at, (v, f))).collect()
        } else {
            let doc = self.doc(handle)?;
            let book = &doc.book;
            let s = book.sheet_index(sheet).ok_or_else(|| format!("no sheet named {sheet}"))?;
            let Some(ix) = &book.sheets[s].index else { return Err(format!("{REFUSED}{sheet} is empty")) };
            if let Some(m) = ix.merges.iter().find(|m| m.intersects(&rect)) {
                return Err(format!("{REFUSED}{range} holds merged cells ({m}); Excel cannot sort them either"));
            }
            ix.cells().filter(|c| rect.contains(c.at)).map(|c| (c.at, (book.value(s, c.at), book.formula(s, c.at).map(str::to_string)))).collect()
        };
        let at = |r: u32, c: u32| cells.get(&CellRef::new(r, c)).cloned().unwrap_or((Value::Empty, None));
        let mut rows: Vec<u32> = (first..=rect.last.row).collect();
        rows.sort_by(|a, b| sort_order(&at(*a, col).0, &at(*b, col).0, descending));
        let mut ops = Vec::new();
        for (i, from) in rows.iter().enumerate() {
            let to = first + i as u32;
            if *from == to {
                continue;
            }
            for c in rect.first.col..=rect.last.col {
                let (value, formula) = at(*from, c);
                if formula.is_none() && at(to, c) == (value.clone(), None) {
                    continue;
                }
                ops.push(copy_op(sheet, CellRef::new(*from, c), CellRef::new(to, c), value, formula, "sort")?);
            }
        }
        if ops.is_empty() {
            return Err(format!("{REFUSED}the rows are already in that order"));
        }
        Ok(ops)
    }

    /// Excel's fill handle dragged from `source` to `target` (the source and the rows or columns
    /// the drag added on one side), as plain edits: each column (or row) of the source carried on
    /// by `autofill::extend`, the cells it repeats copied as `fill` copies them.
    pub fn autofill(&mut self, handle: u64, sheet: &str, source: &str, target: &str, switch: bool) -> Result<Vec<SheetOp>, String> {
        use crate::autofill::{Filled, Shown, Source};
        let src = Rect::parse(source).ok_or_else(|| format!("not a range: {source}"))?;
        let to = Rect::parse(target).ok_or_else(|| format!("not a range: {target}"))?;
        let same_cols = src.first.col == to.first.col && src.last.col == to.last.col;
        let same_rows = src.first.row == to.first.row && src.last.row == to.last.row;
        // Along the drag: (rows?, away from the source's first cell?)
        let (vertical, forward) = match () {
            _ if same_cols && to.first.row == src.first.row && to.last.row > src.last.row => (true, true),
            _ if same_cols && to.last.row == src.last.row && to.first.row < src.first.row => (true, false),
            _ if same_rows && to.first.col == src.first.col && to.last.col > src.last.col => (false, true),
            _ if same_rows && to.last.col == src.last.col && to.first.col < src.first.col => (false, false),
            _ => return Err(format!("{REFUSED}{target} does not carry {source} on along one side")),
        };
        let (lines, along) = if vertical { (src.first.col..=src.last.col, (src.first.row, src.last.row)) } else { (src.first.row..=src.last.row, (src.first.col, src.last.col)) };
        let added = if vertical { (to.last.row - to.first.row) - (src.last.row - src.first.row) } else { (to.last.col - to.first.col) - (src.last.col - src.first.col) } as usize;
        let targets = added as u64 * (lines.end() - lines.start() + 1) as u64;
        if targets > MAX_CELLS {
            return Err(format!("{target} fills {targets} cells; fill at most {MAX_CELLS} at a time"));
        }
        // Each source cell, as (value, stored formula, shown as a date or time).
        type Read = (Value, Option<String>, Shown);
        let read: HashMap<CellRef, Read>;
        let date1904;
        if let Some(flat) = self.flat(handle) {
            date1904 = false;
            read = flat.cells(sheet, Some(src)).ok_or_else(|| format!("no sheet named {sheet}"))?.into_iter().map(|(at, v, _, f)| (at, (v, f, Shown::Plain))).collect();
        } else {
            let doc = self.doc(handle)?;
            let book = &doc.book;
            date1904 = book.info.date1904;
            let s = book.sheet_index(sheet).ok_or_else(|| format!("no sheet named {sheet}"))?;
            let mut cells = HashMap::new();
            for r in src.first.row..=src.last.row {
                for c in src.first.col..=src.last.col {
                    let at = CellRef::new(r, c);
                    let fmt = book.style(s, at).num_fmt;
                    let shown = if !her_sheet::styles::is_date_format(&fmt) {
                        Shown::Plain
                    } else if fmt.to_ascii_lowercase().replace("[h]", "").contains(['y', 'd']) {
                        Shown::Date
                    } else {
                        Shown::Time
                    };
                    cells.insert(at, (book.value(s, at), book.formula(s, at).map(str::to_string), shown));
                }
            }
            read = cells;
        }
        let cell = |line: u32, pos: u32| if vertical { CellRef::new(pos, line) } else { CellRef::new(line, pos) };
        let mut ops = Vec::new();
        for line in lines {
            // The source in the order the drag leaves it.
            let order: Vec<u32> = if forward { (along.0..=along.1).collect() } else { (along.0..=along.1).rev().collect() };
            let cells: Vec<(CellRef, Read)> = order.iter().map(|&p| (cell(line, p), read.get(&cell(line, p)).cloned().unwrap_or((Value::Empty, None, Shown::Plain)))).collect();
            let sources: Vec<Source> = cells
                .iter()
                .map(|(_, (value, formula, shown))| match (formula, value) {
                    (Some(_), _) => Source::Other,
                    (None, Value::Number(n)) => Source::Number { value: *n, shown: *shown },
                    (None, Value::Text(t)) => Source::Text(t.clone()),
                    (None, Value::Empty) => Source::Empty,
                    (None, _) => Source::Other,
                })
                .collect();
            for (k, filled) in crate::autofill::extend(&sources, added, switch, date1904).into_iter().enumerate() {
                let pos = if forward { along.1 + 1 + k as u32 } else { along.0 - 1 - k as u32 };
                let at = cell(line, pos);
                let (sheet, cell_name) = (sheet.to_string(), at.to_string());
                ops.push(match filled {
                    Filled::Number(n) => SheetOp::SetValue { sheet, cell: cell_name, value: CellInput::Number(n) },
                    Filled::Text(t) => SheetOp::SetValue { sheet, cell: cell_name, value: CellInput::Text(t) },
                    Filled::Copy(i) => {
                        let (from, (value, formula, _)) = &cells[i];
                        match (formula, value) {
                            (Some(f), _) => {
                                let moved = her_sheet::formula::translate(f, at.row as i64 - from.row as i64, at.col as i64 - from.col as i64)
                                    .map_err(|e| format!("{REFUSED}the formula in {from} cannot move to {at}: {e}"))?;
                                SheetOp::SetFormula { sheet, cell: cell_name, formula: formula_text(&display(&moved)) }
                            }
                            (None, Value::Number(n)) => SheetOp::SetValue { sheet, cell: cell_name, value: CellInput::Number(*n) },
                            (None, Value::Bool(b)) => SheetOp::SetValue { sheet, cell: cell_name, value: CellInput::Bool(*b) },
                            (None, Value::Text(t)) => SheetOp::SetValue { sheet, cell: cell_name, value: CellInput::Text(t.clone()) },
                            (None, Value::Error(e)) => return Err(format!("{REFUSED}{from} holds the error {e}; fill it in Excel")),
                            (None, Value::Empty) => SheetOp::Clear { sheet, range: cell_name },
                        }
                    }
                });
            }
        }
        Ok(ops)
    }

    /// What the agent reads first (spreadsheet-design §9.1): each sheet's extent and inputs, the
    /// model's outputs, values held as saved (add-ins, links), and the cycles.
    pub fn summary(&mut self, handle: u64) -> Result<String, String> {
        if let Some(flat) = self.flat(handle) {
            return Ok(match flat {
                Flat::Csv { csv, name, .. } => format!(
                    "A CSV ({}, separated by {}): sheet \"{name}\", {} rows × {} columns. Every field is text as written (leading zeros and long numbers kept); no formulas. Edits are written back in the same encoding.\n",
                    csv.encoding_name(),
                    match csv.delimiter { '\t' => "tabs".to_string(), d => format!("'{d}'") },
                    csv.rows(),
                    csv.cols()
                ),
                Flat::Xls(book) => {
                    let sheets: Vec<String> = book.sheets.iter().map(|s| format!("{} ({} cells, {} formulas)", s.name, s.cells.len(), s.cells.values().filter(|c| c.formula.is_some()).count())).collect();
                    format!("A legacy .xls, read only here (to edit it, the user saves it as .xlsx): {}.\n", sheets.join("; "))
                }
            });
        }
        let doc = self.doc(handle)?;
        let book = &doc.book;
        let mut out = String::new();
        let calc = book.info.calc;
        out.push_str(&format!(
            "Iterative calculation: {}.\n",
            if calc.iterate { format!("on ({} rounds, {})", calc.iterate_count, calc.iterate_delta) } else { "off".into() }
        ));
        let names: Vec<String> = book.info.names.iter().filter(|n| !n.hidden && !n.name.starts_with("_xlnm.")).take(30).map(|n| format!("{} = {}", n.name, n.formula)).collect();
        if !names.is_empty() {
            out.push_str(&format!("Names: {}\n", names.join("; ")));
        }
        let engine = doc.engine.as_ref().ok();
        // Inputs: cells holding a number that a formula reads.
        let mut inputs: HashMap<usize, BTreeSet<CellRef>> = HashMap::new();
        if let Some(e) = engine {
            for areas in e.graph.areas.values() {
                for a in areas {
                    let Some(ix) = book.sheets.get(a.sheet).and_then(|s| s.index.as_ref()) else { continue };
                    for c in ix.cells().filter(|c| c.formula.is_none() && a.contains(c.at)) {
                        if matches!(book.value(a.sheet, c.at), Value::Number(_)) {
                            inputs.entry(a.sheet).or_default().insert(c.at);
                        }
                    }
                }
            }
        }
        for (s, sheet) in book.sheets.iter().enumerate() {
            match sheet.kind {
                SheetKind::Chartsheet => {
                    out.push_str(&format!("\n## {} (chart sheet)\n", sheet.name));
                    continue;
                }
                SheetKind::Other => continue,
                SheetKind::Worksheet => {}
            }
            let Some(ix) = &sheet.index else { continue };
            let used = used_range(ix).map_or_else(|| "empty".into(), |r| r.to_string());
            out.push_str(&format!("\n## {} {used}: {} formulas", sheet.name, sheet.formulas.len()));
            if ix.protected {
                out.push_str(", protected");
            }
            out.push('\n');
            if let Some(cells) = inputs.get(&s) {
                let shown: Vec<String> = cells.iter().take(12).map(|c| {
                    let f = Self::found(book, (s, *c), 0, None);
                    format!("{}{} = {}", f.label.map(|l| format!("{l} ")).unwrap_or_default(), f.cell, f.text)
                }).collect();
                out.push_str(&format!("Inputs ({}): {}{}\n", cells.len(), shown.join("; "), if cells.len() > 12 { "; …" } else { "" }));
            }
        }
        if let Some(e) = engine {
            let outputs: Vec<Node> = e.graph.reads.keys().copied().filter(|n| e.graph.read_by.get(n).is_none_or(|r| r.is_empty())).collect::<BTreeSet<_>>().into_iter().collect();
            let labelled: Vec<String> = outputs.iter().map(|n| Self::found(book, *n, 0, None)).filter(|f| f.label.is_some()).take(15).map(|f| format!("{}!{} {} = {}", f.sheet, f.cell, f.label.unwrap(), f.text)).collect();
            if !labelled.is_empty() {
                out.push_str(&format!("\nOutputs (formulas nothing else reads, {} in all): {}\n", outputs.len(), labelled.join("; ")));
            }
            let held: Vec<String> = e.constants().iter().take(10).map(|n| Self::found(book, *n, 0, None)).map(|f| format!("{}!{} {}", f.sheet, f.cell, f.formula.unwrap_or_default())).collect();
            if !held.is_empty() {
                out.push_str(&format!("Held at their saved value (add-in functions or links to other files; Excel recomputes them): {}\n", held.join("; ")));
            }
            let cycles = e.graph.cycles(None);
            if !cycles.is_empty() {
                let cells: usize = cycles.iter().map(Vec::len).sum();
                let first: Vec<String> = cycles[0].iter().take(6).map(|n| format!("{}!{}", book.sheets[n.0].name, n.1)).collect();
                out.push_str(&format!("Circular references: {} cycles, {cells} cells, e.g. {}{}\n", cycles.len(), first.join(" → "), if calc.iterate { "" } else { " (iteration is off: Excel reports them as errors)" }));
            }
        } else if let Err(e) = &doc.engine {
            out.push_str(&format!("\nNo calculation here: {e}. Edits are written without their impact.\n"));
        }
        Ok(out)
    }
}

/// Rows or columns inserted or deleted at a time.
const MAX_STRUCTURE: u32 = 10_000;

fn shift_of(c: &SheetStructure) -> her_sheet::structure::Shift {
    use her_sheet::structure::Axis;
    let (axis, delete) = match c.kind {
        StructureKind::InsertRows => (Axis::Rows, false),
        StructureKind::DeleteRows => (Axis::Rows, true),
        StructureKind::InsertColumns => (Axis::Cols, false),
        StructureKind::DeleteColumns => (Axis::Cols, true),
    };
    her_sheet::structure::Shift { axis, at: c.at, count: c.count, delete }
}

/// Excel's sort order of values; blanks after everything, whichever way.
fn sort_order(a: &Value, b: &Value, descending: bool) -> std::cmp::Ordering {
    use std::cmp::Ordering;
    let rank = |v: &Value| match v {
        Value::Number(_) => 0,
        Value::Text(_) => 1,
        Value::Bool(_) => 2,
        Value::Error(_) => 3,
        Value::Empty => 4,
    };
    let (ra, rb) = (rank(a), rank(b));
    if ra == 4 || rb == 4 {
        return ra.cmp(&rb);
    }
    let o = match (a, b) {
        (Value::Number(x), Value::Number(y)) => x.partial_cmp(y).unwrap_or(Ordering::Equal),
        (Value::Text(x), Value::Text(y)) => x.to_lowercase().cmp(&y.to_lowercase()),
        (Value::Bool(x), Value::Bool(y)) => x.cmp(y),
        _ => ra.cmp(&rb),
    };
    if descending { o.reverse() } else { o }
}

/// The edit that puts what `from` holds into `to`: a formula moved as Excel moves it, a value
/// as it is, an empty cell emptied. An error value is refused, as a typed one would be text.
fn copy_op(sheet: &str, from: CellRef, to: CellRef, value: Value, formula: Option<String>, what: &str) -> Result<SheetOp, String> {
    let (sheet, cell) = (sheet.to_string(), to.to_string());
    Ok(match (formula, value) {
        (Some(f), _) => {
            let moved = her_sheet::formula::translate(&f, to.row as i64 - from.row as i64, to.col as i64 - from.col as i64)
                .map_err(|e| format!("{REFUSED}the formula in {from} cannot move to {to}: {e}"))?;
            SheetOp::SetFormula { sheet, cell, formula: formula_text(&display(&moved)) }
        }
        (None, Value::Number(n)) => SheetOp::SetValue { sheet, cell, value: CellInput::Number(n) },
        (None, Value::Bool(b)) => SheetOp::SetValue { sheet, cell, value: CellInput::Bool(b) },
        (None, Value::Text(t)) => SheetOp::SetValue { sheet, cell, value: CellInput::Text(t) },
        (None, Value::Error(e)) => return Err(format!("{REFUSED}{from} holds the error {e}; {what} it in Excel")),
        (None, Value::Empty) => SheetOp::Clear { sheet, range: cell },
    })
}

/// How a formula is shown and sent: with its `=`.
fn formula_text(f: &str) -> String {
    format!("={f}")
}

/// The cells holding something, as one rectangle; None when the sheet is empty.
fn used_range(ix: &her_sheet::scan::SheetIndex) -> Option<Rect> {
    let mut cells = ix.cells().filter(|c| c.v.is_some() || c.formula.is_some() || c.inline.is_some()).map(|c| c.at);
    let first = cells.next()?;
    Some(cells.fold(Rect::single(first), |r, c| r.union(c)))
}

/// A value typed or sent, as written to a cell, in a workbook or a CSV alike. Control
/// characters other than tab and line feed are dropped (§5.3.2).
fn new_value(value: &CellInput) -> NewValue {
    match value {
        CellInput::Number(n) => NewValue::Number { value: *n },
        CellInput::Bool(b) => NewValue::Bool { value: *b },
        CellInput::Text(t) => NewValue::Text { value: t.chars().filter(|c| !c.is_control() || *c == '\t' || *c == '\n').collect() },
    }
}

/// The ops as cell edits, checked against the workbook.
fn edits_of(book: &Book, ops: &[SheetOp]) -> Result<Vec<(Node, NewValue)>, String> {
    let sheet = |name: &str| -> Result<usize, String> {
        let s = book.sheet_index(name).ok_or_else(|| format!("no sheet named {name}"))?;
        if book.sheets[s].kind != SheetKind::Worksheet {
            return Err(format!("{REFUSED}{name} is a chart sheet"));
        }
        Ok(s)
    };
    let cell = |c: &str| CellRef::parse(c).ok_or_else(|| format!("not a cell: {c}"));
    let mut out = Vec::new();
    for op in ops {
        match op {
            SheetOp::SetValue { sheet: s, cell: c, value } => {
                out.push(((sheet(s)?, cell(c)?), new_value(value)));
            }
            SheetOp::SetFormula { sheet: s, cell: c, formula } => out.push(((sheet(s)?, cell(c)?), NewValue::Formula { text: formula.clone(), cached: None })),
            SheetOp::Clear { sheet: s, range } => {
                let s = sheet(s)?;
                let r = Rect::parse(range).ok_or_else(|| format!("not a range: {range}"))?;
                let Some(ix) = &book.sheets[s].index else { continue };
                let cells: Vec<CellRef> = ix.cells().map(|c| c.at).filter(|c| r.contains(*c)).filter(|c| book.value(s, *c) != Value::Empty || book.formula(s, *c).is_some()).collect();
                if cells.len() as u64 > MAX_CELLS {
                    return Err(format!("{range} holds {} cells; clear at most {MAX_CELLS} at a time", cells.len()));
                }
                out.extend(cells.into_iter().map(|c| ((s, c), NewValue::Empty)));
            }
        }
    }
    if out.is_empty() {
        return Err("nothing to change".into());
    }
    Ok(out)
}

/// Ops that put the edited cells back.
fn undo_of(book: &Book, edits: &[(Node, NewValue)]) -> Vec<SheetOp> {
    let mut seen = BTreeSet::new();
    let mut out = Vec::new();
    for ((s, at), _) in edits {
        if !seen.insert((*s, *at)) {
            continue;
        }
        let (sheet, cell) = (book.sheets[*s].name.clone(), at.to_string());
        out.push(match (book.formula(*s, *at), book.value(*s, *at)) {
            (Some(f), _) => SheetOp::SetFormula { sheet, cell, formula: formula_text(&display(f)) },
            (None, Value::Number(n)) => SheetOp::SetValue { sheet, cell, value: CellInput::Number(n) },
            (None, Value::Bool(b)) => SheetOp::SetValue { sheet, cell, value: CellInput::Bool(b) },
            (None, Value::Text(t)) => SheetOp::SetValue { sheet, cell, value: CellInput::Text(t) },
            (None, _) => SheetOp::Clear { sheet, range: cell },
        });
    }
    out
}

fn change(old: &Book, new: &Book, n: Node, before: &Value, after: &Value, label: bool) -> CellChange {
    let style = new.style(n.0, n.1);
    CellChange {
        sheet: new.sheets[n.0].name.clone(),
        cell: n.1.to_string(),
        before: protocol_value(before),
        after: protocol_value(after),
        before_text: old.display(before, &old.style(n.0, n.1)).0,
        after_text: new.display(after, &style).0,
        label: if label { new.label(n.0, n.1) } else { None },
    }
}

fn protocol_impact(
    old: &Book,
    new: &Book,
    edits: &[(Node, NewValue)],
    edited_before: Vec<Value>,
    impact: Option<&her_sheet::calc::Impact>,
    engine: &Result<Engine, String>,
) -> Impact {
    let name = |n: &Node| format!("{}!{}", new.sheets[n.0].name, n.1);
    let Some(impact) = impact else {
        // No engine: the edited cells only, as written.
        let edited = edits.iter().zip(edited_before).map(|((n, _), b)| change(old, new, *n, &b, &new.value(n.0, n.1), false)).collect();
        return Impact { edited, converged: true, calc_unavailable: engine.as_ref().err().cloned(), ..Default::default() };
    };
    let changed: HashMap<Node, &her_sheet::calc::Change> = impact.changed.iter().map(|c| (c.node, c)).collect();
    Impact {
        edited: impact.edited.iter().map(|c| change(old, new, c.node, &c.before, &c.after, false)).collect(),
        key_outputs: impact.key_outputs.iter().filter_map(|n| changed.get(n)).map(|c| change(old, new, c.node, &c.before, &c.after, true)).collect(),
        affected: impact.changed.iter().take(MAX_AFFECTED).map(|c| name(&c.node)).collect(),
        affected_count: impact.changed.len(),
        needs_excel: impact.needs_excel.iter().take(MAX_AFFECTED).map(name).collect(),
        iterations: impact.iterations,
        converged: impact.converged,
        calc_unavailable: None,
    }
}
