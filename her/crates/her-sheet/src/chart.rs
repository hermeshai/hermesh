//! The charts drawn on a worksheet (spreadsheet-design §4.1: shown, not edited): where each is
//! anchored, what kind it is, and the numbers behind it, read from the cells it points at (or,
//! where the cells are empty, from the cache the chart itself carries).
//!
//! Reading is best effort: a drawing or chart we cannot make sense of is left out, never an error,
//! so the cells still open.

use crate::book::Book;
use crate::book::Value;
use crate::cellref::CellRef;
use crate::cellref::Rect;
use crate::package::Package;
use crate::package::parse_rels;
use crate::package::rels_part_for;
use crate::package::resolve;
use crate::xmltext::unescape_xml;

const EMU_PER_PX: f64 = 9525.0;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    Bar,
    Line,
    Area,
    Pie,
    Doughnut,
    Scatter,
    Radar,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Grouping {
    Clustered,
    Stacked,
    Percent,
}

#[derive(Clone, Debug)]
pub struct Series {
    pub name: Option<String>,
    pub color: Option<String>,
    pub values: Vec<Option<f64>>,
    pub x: Vec<Option<f64>>,
}

#[derive(Clone, Copy, Debug)]
pub struct Corner {
    pub col: u32,
    pub row: u32,
    pub dx: f32,
    pub dy: f32,
}

#[derive(Clone, Debug)]
pub struct Chart {
    pub kind: Kind,
    pub horizontal: bool,
    pub grouping: Grouping,
    pub title: Option<String>,
    pub legend: Option<String>,
    pub labels: bool,
    pub categories: Vec<String>,
    pub series: Vec<Series>,
    pub min: Option<f64>,
    pub max: Option<f64>,
    pub from: Corner,
    pub to: Option<Corner>,
    pub width: f32,
    pub height: f32,
}

// ── a small element tree: these parts are tiny, and the questions are about structure ──

#[derive(Default, Debug)]
struct Node {
    name: String,
    attrs: Vec<(String, String)>,
    text: String,
    kids: Vec<Node>,
}

impl Node {
    fn attr(&self, k: &str) -> Option<&str> {
        self.attrs.iter().find(|(n, _)| n == k).map(|(_, v)| v.as_str())
    }
    fn kid(&self, name: &str) -> Option<&Node> {
        self.kids.iter().find(|k| k.name == name)
    }
    fn kids_named<'a>(&'a self, name: &'a str) -> impl Iterator<Item = &'a Node> {
        self.kids.iter().filter(move |k| k.name == name)
    }
    /// `a/b/c` by local names.
    fn path(&self, path: &str) -> Option<&Node> {
        path.split('/').try_fold(self, |n, p| n.kid(p))
    }
    fn find(&self, name: &str) -> Option<&Node> {
        self.kids.iter().find_map(|k| if k.name == name { Some(k) } else { k.find(name) })
    }
    fn val(&self, name: &str) -> Option<&str> {
        self.kid(name)?.attr("val")
    }
    fn flag(&self, name: &str) -> Option<bool> {
        self.val(name).map(|v| v != "0" && v != "false")
    }
    fn texts(&self, out: &mut String) {
        if self.name == "t" {
            out.push_str(&self.text);
        }
        for k in &self.kids {
            k.texts(out);
        }
    }
}

fn parse_tree(xml: &[u8]) -> Option<Node> {
    use quick_xml::events::Event;
    let (mut r, _) = crate::xmltext::reader(xml);
    let open = |e: &quick_xml::events::BytesStart<'_>| -> Option<Node> {
        Some(Node {
            name: e.local_name().as_ref().to_string(),
            attrs: crate::package::attrs(e).ok()?.into_iter().collect(),
            ..Node::default()
        })
    };
    let mut stack: Vec<Node> = vec![Node::default()];
    loop {
        match r.read_event().ok()? {
            Event::Start(e) => stack.push(open(&e)?),
            Event::Empty(e) => {
                let n = open(&e)?;
                stack.last_mut()?.kids.push(n);
            }
            Event::End(_) => {
                let n = stack.pop()?;
                stack.last_mut()?.kids.push(n);
            }
            Event::Text(t) => {
                if let Some(top) = stack.last_mut() {
                    top.text.push_str(&unescape_xml(&t));
                }
            }
            Event::Eof => break,
            _ => {}
        }
    }
    let mut root = stack.pop()?;
    root.kids.pop()
}

// ── loading ──

/// The charts on worksheet `sheet`, in the order the drawing holds them.
pub fn load(pkg: &Package, book: &Book, sheet: usize) -> Vec<Chart> {
    try_load(pkg, book, sheet).unwrap_or_default()
}

fn try_load(pkg: &Package, book: &Book, sheet: usize) -> Option<Vec<Chart>> {
    let part = book.info.sheets.get(sheet)?.part.as_deref()?;
    let rels = parse_rels(&pkg.read(&rels_part_for(part)).ok()??).ok()?;
    let mut out = Vec::new();
    for rel in rels.iter().filter(|r| r.kind.ends_with("/drawing") && !r.external) {
        let drawing = resolve(part, &rel.target);
        let Some(xml) = pkg.read(&drawing).ok().flatten() else { continue };
        let Some(tree) = parse_tree(&xml) else { continue };
        let drawing_rels = pkg
            .read(&rels_part_for(&drawing))
            .ok()
            .flatten()
            .and_then(|x| parse_rels(&x).ok())
            .unwrap_or_default();
        for anchor in &tree.kids {
            let Some(frame) = anchor.find("graphicFrame") else { continue };
            let Some(id) = frame.find("chart").and_then(|c| c.attr("id")) else { continue };
            let Some(rel) = drawing_rels.iter().find(|r| r.id == id) else { continue };
            let chart_part = resolve(&drawing, &rel.target);
            let Some(cxml) = pkg.read(&chart_part).ok().flatten() else { continue };
            let Some(ctree) = parse_tree(&cxml) else { continue };
            let Some((from, to, width, height)) = anchor_of(anchor) else { continue };
            if let Some(c) = chart_of(&ctree, book, sheet, from, to, width, height) {
                out.push(c);
            }
        }
    }
    Some(out)
}

fn emu(n: &Node, name: &str) -> f64 {
    n.kid(name).and_then(|k| k.text.trim().parse::<f64>().ok()).unwrap_or(0.0)
}

fn corner(n: &Node) -> Corner {
    Corner {
        col: emu(n, "col") as u32 + 1,
        row: emu(n, "row") as u32 + 1,
        dx: (emu(n, "colOff") / EMU_PER_PX) as f32,
        dy: (emu(n, "rowOff") / EMU_PER_PX) as f32,
    }
}

type Placed = (Corner, Option<Corner>, f32, f32);

fn anchor_of(a: &Node) -> Option<Placed> {
    let size = |n: &Node| {
        let cx = n.attr("cx").and_then(|v| v.parse::<f64>().ok()).unwrap_or(0.0);
        let cy = n.attr("cy").and_then(|v| v.parse::<f64>().ok()).unwrap_or(0.0);
        ((cx / EMU_PER_PX) as f32, (cy / EMU_PER_PX) as f32)
    };
    match a.name.as_str() {
        "oneCellAnchor" => {
            let (w, h) = size(a.kid("ext")?);
            Some((corner(a.kid("from")?), None, w, h))
        }
        "twoCellAnchor" => {
            Some((corner(a.kid("from")?), Some(corner(a.kid("to")?)), 480.0, 288.0))
        }
        // A fixed place on the sheet: measured from the top-left corner.
        "absoluteAnchor" => {
            let (w, h) = size(a.kid("ext")?);
            let pos = a.kid("pos")?;
            let at = Corner {
                col: 1,
                row: 1,
                dx: (pos.attr("x")?.parse::<f64>().ok()? / EMU_PER_PX) as f32,
                dy: (pos.attr("y")?.parse::<f64>().ok()? / EMU_PER_PX) as f32,
            };
            Some((at, None, w, h))
        }
        _ => None,
    }
}

fn chart_of(
    tree: &Node,
    book: &Book,
    sheet: usize,
    from: Corner,
    to: Option<Corner>,
    width: f32,
    height: f32,
) -> Option<Chart> {
    let chart = tree.kid("chart")?;
    let plot = chart.kid("plotArea")?;
    let group = plot.kids.iter().find(|k| kind_of(&k.name).is_some())?;
    let kind = kind_of(&group.name)?;
    let horizontal = group.val("barDir") == Some("bar");
    let grouping = match group.val("grouping") {
        Some("stacked") => Grouping::Stacked,
        Some("percentStacked") => Grouping::Percent,
        _ => Grouping::Clustered,
    };
    let group_labels = group.path("dLbls").and_then(|d| d.flag("showVal")).unwrap_or(false);
    let mut labels = group_labels;
    let mut categories: Vec<String> = Vec::new();
    let mut series = Vec::new();
    for ser in group.kids_named("ser") {
        let name = ser.kid("tx").and_then(|t| texts_of(t, book, sheet)).and_then(|v| v.into_iter().next());
        let color = ser
            .path("spPr/solidFill/srgbClr")
            .or_else(|| ser.path("spPr/ln/solidFill/srgbClr"))
            .and_then(|c| c.attr("val"))
            .map(str::to_uppercase);
        if ser.path("dLbls").and_then(|d| d.flag("showVal")).unwrap_or(false) {
            labels = true;
        }
        let (cat, val, x) = if kind == Kind::Scatter {
            (None, ser.kid("yVal"), ser.kid("xVal"))
        } else {
            (ser.kid("cat"), ser.kid("val"), None)
        };
        if categories.is_empty()
            && let Some(c) = cat.and_then(|c| texts_of(c, book, sheet))
        {
            categories = c;
        }
        let values = val.and_then(|v| numbers_of(v, book, sheet)).unwrap_or_default();
        let x = x.and_then(|v| numbers_of(v, book, sheet)).unwrap_or_default();
        series.push(Series { name, color, values, x });
    }
    if series.is_empty() {
        return None;
    }
    let n = series.iter().map(|s| s.values.len()).max().unwrap_or(0);
    while categories.len() < n {
        categories.push((categories.len() + 1).to_string());
    }
    let mut title = String::new();
    if let Some(t) = chart.kid("title") {
        t.texts(&mut title);
    }
    let legend = chart
        .kid("legend")
        .filter(|l| l.flag("delete") != Some(true))
        .map(|l| l.val("legendPos").unwrap_or("r").to_string());
    let value_axis = plot.kid("valAx");
    let scale = |k: &str| value_axis.and_then(|a| a.path("scaling")?.val(k)?.parse::<f64>().ok());
    Some(Chart {
        kind,
        horizontal,
        grouping,
        title: (!title.trim().is_empty()).then(|| title.trim().to_string()),
        legend,
        labels,
        categories,
        series,
        min: scale("min"),
        max: scale("max"),
        from,
        to,
        width,
        height,
    })
}

fn kind_of(name: &str) -> Option<Kind> {
    Some(match name {
        "barChart" | "bar3DChart" => Kind::Bar,
        "lineChart" | "line3DChart" | "stockChart" => Kind::Line,
        "areaChart" | "area3DChart" => Kind::Area,
        "pieChart" | "pie3DChart" | "ofPieChart" => Kind::Pie,
        "doughnutChart" => Kind::Doughnut,
        "scatterChart" | "bubbleChart" => Kind::Scatter,
        "radarChart" => Kind::Radar,
        _ => return None,
    })
}

/// A reference like `'Sheet 1'!$B$5:$B$14` (or `Sheet1!A1`): the sheet's index and the cells.
fn parse_ref(book: &Book, home: usize, f: &str) -> Option<(usize, Rect)> {
    let f = f.trim().trim_start_matches('(').trim_end_matches(')');
    let (sheet, cells) = match f.rsplit_once('!') {
        Some((s, c)) => (Some(s.trim_matches('\'').replace("''", "'")), c),
        None => (None, f),
    };
    let ix = match sheet {
        Some(name) => book.sheet_index(&name)?,
        None => home,
    };
    Some((ix, Rect::parse(&cells.replace('$', ""))?))
}

fn cells_of(rect: Rect) -> Vec<CellRef> {
    let mut out = Vec::new();
    for r in rect.first.row..=rect.last.row {
        for c in rect.first.col..=rect.last.col {
            out.push(CellRef::new(r, c));
        }
    }
    out
}

/// What a `strRef`/`numRef`/`strLit`/`numLit` holds: the cache when the cells say nothing.
fn cache_of(n: &Node) -> Vec<Option<String>> {
    let Some(cache) = n.kids.iter().find(|k| matches!(k.name.as_str(), "strCache" | "numCache" | "strLit" | "numLit")) else {
        return Vec::new();
    };
    let count = cache.val("ptCount").and_then(|c| c.parse::<usize>().ok()).unwrap_or(0);
    let mut out = vec![None; count];
    for pt in cache.kids_named("pt") {
        let Some(ix) = pt.attr("idx").and_then(|i| i.parse::<usize>().ok()) else { continue };
        if ix >= 100_000 {
            continue;
        }
        if out.len() <= ix {
            out.resize(ix + 1, None);
        }
        out[ix] = pt.kid("v").map(|v| v.text.clone());
    }
    out
}

fn source_of<'a>(n: &'a Node) -> Option<&'a Node> {
    n.kids.iter().find(|k| matches!(k.name.as_str(), "strRef" | "numRef" | "multiLvlStrRef" | "strLit" | "numLit"))
        .or(Some(n))
}

fn texts_of(n: &Node, book: &Book, home: usize) -> Option<Vec<String>> {
    // `<c:tx><c:v>name</c:v></c:tx>` is a literal name.
    if let Some(v) = n.kid("v") {
        return Some(vec![v.text.clone()]);
    }
    let src = source_of(n)?;
    let cached = cache_of(src);
    if let Some((ix, rect)) = src.kid("f").and_then(|f| parse_ref(book, home, &f.text)) {
        let cells = cells_of(rect);
        if cells.len() <= 100_000 {
            let live: Vec<String> = cells
                .iter()
                .enumerate()
                .map(|(i, at)| {
                    let v = book.value(ix, *at);
                    if v == Value::Empty {
                        cached.get(i).cloned().flatten().unwrap_or_default()
                    } else {
                        book.display(&v, &book.style(ix, *at)).0
                    }
                })
                .collect();
            return Some(live);
        }
    }
    (!cached.is_empty()).then(|| cached.into_iter().map(Option::unwrap_or_default).collect())
}

fn numbers_of(n: &Node, book: &Book, home: usize) -> Option<Vec<Option<f64>>> {
    let src = source_of(n)?;
    let cached = cache_of(src);
    let parse = |s: &Option<String>| s.as_deref().and_then(|v| v.trim().parse::<f64>().ok());
    if let Some((ix, rect)) = src.kid("f").and_then(|f| parse_ref(book, home, &f.text)) {
        let cells = cells_of(rect);
        if cells.len() <= 100_000 {
            return Some(
                cells
                    .iter()
                    .enumerate()
                    .map(|(i, at)| match book.value(ix, *at) {
                        Value::Number(v) => Some(v),
                        Value::Empty => parse(cached.get(i).unwrap_or(&None)),
                        _ => None,
                    })
                    .collect(),
            );
        }
    }
    (!cached.is_empty()).then(|| cached.iter().map(parse).collect())
}
