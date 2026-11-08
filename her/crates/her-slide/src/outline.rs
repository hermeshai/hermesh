//! A deck as the agent reads it (presentation-ai-design §7): one record a slide, with its text,
//! notes and objects, taken from the engine snapshot. Positions are points from the slide's top
//! left, like the deck protocol.

use betteroffice_pptx::{DeckSnapshot, ShapeKind, ShapeSnapshot, SlideSnapshot};

/// EMU in a point.
const EMU_PER_PT: f64 = 12_700.0;

#[derive(Clone, Debug, PartialEq)]
pub struct Outline {
    pub width_pt: f64,
    pub height_pt: f64,
    pub slides: Vec<SlideOutline>,
}

#[derive(Clone, Debug, PartialEq)]
pub struct SlideOutline {
    /// From 1, as people count.
    pub index: usize,
    pub slide_id: String,
    pub title: Option<String>,
    pub layout: Option<String>,
    pub hidden: bool,
    pub notes: String,
    /// The slide's own background (`#RRGGBB`, `theme <slot>`, or a fill kind such as `gradient`);
    /// `None` when it takes the layout's.
    pub background: Option<String>,
    pub objects: Vec<ObjectOutline>,
}

#[derive(Clone, Debug, PartialEq)]
pub struct ObjectOutline {
    pub shape_id: String,
    pub name: String,
    /// `title`, `text_box`, `shape`, `picture`, `table`, `chart`, `diagram`, `graphic`, `group`.
    pub kind: &'static str,
    /// The layout placeholder it fills (`title`, `body`, `ctrTitle`…).
    pub placeholder: Option<String>,
    /// x, y, width, height in points.
    pub rect: [f64; 4],
    pub text: String,
    /// A table's cells, row by row, when its stories line up with its grid.
    pub table: Option<Vec<Vec<String>>>,
    /// Alternative text of pictures and charts.
    pub alt: Option<String>,
    /// The shape's fill and outline colours, `#RRGGBB`, theme colours resolved.
    pub fill: Option<String>,
    pub line: Option<String>,
    /// How its text looks: neighbouring runs of one look together, in reading order.
    pub looks: Vec<TextLook>,
    pub depth: usize,
}

/// A stretch of text in one look. Unset values come from the placeholder, layout or theme.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct TextLook {
    /// The stretch's first words.
    pub sample: String,
    pub size_pt: Option<f64>,
    pub color: Option<String>,
    pub bold: bool,
    pub italic: bool,
    pub font: Option<String>,
}

/// Looks an object lists at most.
const MAX_LOOKS: usize = 8;
/// Characters of a look's sample.
const SAMPLE_CHARS: usize = 16;

impl SlideOutline {
    pub fn has(&self, kind: &str) -> bool {
        self.objects.iter().any(|o| o.kind == kind)
    }
}

pub fn outline(deck: &DeckSnapshot) -> Outline {
    Outline {
        width_pt: deck.width_emu as f64 / EMU_PER_PT,
        height_pt: deck.height_emu as f64 / EMU_PER_PT,
        slides: deck.slides.iter().enumerate().map(|(i, s)| slide(i + 1, s)).collect(),
    }
}

fn slide(index: usize, s: &SlideSnapshot) -> SlideOutline {
    let mut objects = Vec::new();
    for shape in &s.shapes {
        object(shape, 0, &mut objects);
    }
    let title = s
        .title
        .clone()
        .filter(|t| !t.trim().is_empty())
        .or_else(|| objects.iter().find(|o| o.kind == "title" && !o.text.trim().is_empty()).map(|o| o.text.replace('\n', " ")));
    SlideOutline {
        index,
        slide_id: s.id.clone(),
        title,
        layout: s.layout.as_ref().and_then(|l| l.name.clone().or_else(|| l.r#type.clone())),
        hidden: s.hidden == Some(true),
        notes: s.notes.trim().to_owned(),
        background: s.background.as_ref().and_then(fill_name),
        objects,
    }
}

fn object(shape: &ShapeSnapshot, depth: usize, out: &mut Vec<ObjectOutline>) {
    let placeholder = shape.placeholder.as_ref().and_then(|p| p.placeholder_type.clone());
    // The slide number, date and the like are the master's furniture, not the slide's content.
    if shape.hidden || matches!(placeholder.as_deref(), Some("sldNum" | "dt")) {
        return;
    }
    let kind = kind(shape, placeholder.as_deref());
    let (x, y, w, h) = shape.inherited.as_ref().map_or((shape.x, shape.y, shape.width, shape.height), |r| (r.x, r.y, r.width, r.height));
    let text = shape
        .text_stories
        .iter()
        .map(|s| s.plain_text())
        .filter(|t| !t.trim().is_empty())
        .collect::<Vec<_>>()
        .join("\n");
    let table = (kind == "table").then(|| cells(shape)).flatten();
    let alt = [shape.description.as_deref(), shape.title.as_deref()].into_iter().flatten().find(|t| !t.trim().is_empty()).map(str::to_owned);
    out.push(ObjectOutline {
        shape_id: shape.id.clone(),
        name: shape.name.clone(),
        kind,
        placeholder,
        rect: [x, y, w, h].map(|v| (v as f64 / EMU_PER_PT * 10.0).round() / 10.0),
        text,
        table,
        alt,
        fill: shape.resolved_fill_color.as_deref().map(hex),
        line: shape.resolved_outline_color.as_deref().map(hex),
        looks: looks(shape),
        depth,
    });
    for child in &shape.children {
        object(child, depth + 1, out);
    }
}

fn kind(shape: &ShapeSnapshot, placeholder: Option<&str>) -> &'static str {
    use betteroffice_pptx::GraphicFrameData;
    match shape.kind {
        ShapeKind::Group => "group",
        ShapeKind::Picture => "picture",
        ShapeKind::GraphicFrame => match &shape.graphic {
            Some(GraphicFrameData::Table(_)) => "table",
            Some(GraphicFrameData::Chart { .. }) => "chart",
            Some(GraphicFrameData::Diagram { .. }) => "diagram",
            _ if shape.table_structure.is_some() => "table",
            _ => "graphic",
        },
        ShapeKind::Shape if matches!(placeholder, Some("title" | "ctrTitle")) => "title",
        ShapeKind::Shape if shape.text_box => "text_box",
        ShapeKind::Shape => "shape",
    }
}

fn cells(shape: &ShapeSnapshot) -> Option<Vec<Vec<String>>> {
    use betteroffice_pptx::GraphicFrameData;
    let cols = match &shape.graphic {
        Some(GraphicFrameData::Table(t)) if !t.grid.is_empty() => t.grid.len(),
        _ => shape.table_structure.as_ref().map(|t| t.grid.len()).filter(|&n| n > 0)?,
    };
    let texts: Vec<String> = shape.text_stories.iter().map(|s| s.plain_text().replace('\n', " ")).collect();
    texts.len().is_multiple_of(cols).then(|| texts.chunks(cols).map(<[String]>::to_vec).collect())
}

/// `#RRGGBB` of a colour the engine gives with or without `#`, alpha dropped.
fn hex(c: &str) -> String {
    let c = c.trim_start_matches('#');
    format!("#{}", c.get(..6).unwrap_or(c).to_ascii_uppercase())
}

fn fill_name(fill: &betteroffice_pptx::ShapeFill) -> Option<String> {
    match fill.fill_type.as_str() {
        "none" => None,
        "solid" => fill.color.as_ref().and_then(|c| c.rgb.as_deref().map(hex).or_else(|| c.theme_color.as_ref().map(|t| format!("theme {t}")))),
        other => Some(other.to_owned()),
    }
}

fn looks(shape: &ShapeSnapshot) -> Vec<TextLook> {
    let mut out: Vec<TextLook> = Vec::new();
    let runs = shape.text_stories.iter().flat_map(|s| &s.paragraphs).flat_map(|p| &p.runs);
    for run in runs.filter(|r| !r.text.trim().is_empty()) {
        let st = &run.style;
        let look = TextLook {
            sample: String::new(),
            size_pt: st.font_size_pt,
            color: st.color.as_deref().map(hex),
            bold: st.bold == Some(true),
            italic: st.italic == Some(true),
            font: st.font_family.clone().filter(|f| !f.starts_with('+')),
        };
        if out.last().is_some_and(|last| TextLook { sample: String::new(), ..last.clone() } == look) {
            continue;
        }
        if out.len() == MAX_LOOKS {
            break;
        }
        out.push(TextLook { sample: run.text.trim().chars().take(SAMPLE_CHARS).collect(), ..look });
    }
    out
}
