//! Presentations held by the kernel (docs/desktop/presentation-ai-design.md §6, §12): how
//! people and agents point into a deck, the edits they make (`DeckOp`), proposals, checks,
//! layout candidates, template profiles, data links, comparisons, and the `deck/*` and
//! `ai/action` methods.
//!
//! Conventions, the same in every type here:
//! - Lengths are points (1 pt = 12,700 EMU), origin at the slide's top left, y down.
//! - Angles are degrees, clockwise. Fractions (`lum_mod`, `alpha`, gradient stops) are 0–1.
//! - Text offsets are story-local UTF-16 code units, half-open `[start, end)`, as the engine and
//!   JavaScript strings count them.
//! - Slides count from 1 where an `index` is shown to people; edits address slides and shapes by
//!   their stable ids, never by position.
//! - Inputs are strict (unknown fields are refused, so a misspelt field is not a silent no-op);
//!   outputs may grow, and clients ignore fields they do not know.

use std::collections::BTreeMap;

use schemars::JsonSchema;
use serde::Deserialize;
use serde::Deserializer;
use serde::Serialize;

/// The version of this contract. It rises only for changes that are not additive.
pub const DECK_CONTRACT_VERSION: u32 = 1;

/// A field that may be left out (no change) or `null` (clear it): `None`, `Some(None)`,
/// `Some(Some(v))`.
fn present<'de, D: Deserializer<'de>, T: Deserialize<'de>>(
    deserializer: D,
) -> Result<Option<Option<T>>, D::Error> {
    Option::<T>::deserialize(deserializer).map(Some)
}

fn is_false(value: &bool) -> bool {
    !value
}

fn yes() -> bool {
    true
}

// ---- geometry and colour ----

/// A rectangle on a slide, in points from its top left.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckRect {
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
}

/// A point on a slide, in points from its top left.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckPoint {
    pub x: f64,
    pub y: f64,
}

/// A colour of the theme's scheme. `tx1`/`bg1`/`tx2`/`bg2` go through the master's colour map.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum SchemeColor {
    Dk1,
    Lt1,
    Dk2,
    Lt2,
    Accent1,
    Accent2,
    Accent3,
    Accent4,
    Accent5,
    Accent6,
    Hlink,
    /// `folHlink`.
    FolHlink,
    Tx1,
    Bg1,
    Tx2,
    Bg2,
}

/// A theme colour, optionally lighter or darker, as PowerPoint's palette derives them
/// (`accent1`, 25% lighter = `lum_mod: 0.75, lum_off: 0.25`).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct ThemeColorRef {
    pub scheme: SchemeColor,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub lum_mod: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub lum_off: Option<f64>,
}

/// `"#RRGGBB"`, or a theme colour. Prefer theme colours: they follow a theme change.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(untagged)]
pub enum DeckColor {
    Rgb(String),
    Theme(ThemeColorRef),
}

// ---- addressing (§6.3 PD-02) ----
//
// Every id below may also be `@name`: the object an earlier op of the same batch created with
// `ref: "name"`. The result's `refs` gives the ids they got.

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct SlideRef {
    pub slide_id: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct ShapeRef {
    pub slide_id: String,
    pub shape_id: String,
}

/// A whole text story: a shape's text body, or one table cell's.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckStoryTarget {
    pub slide_id: String,
    pub shape_id: String,
    /// Required for a table (a story per cell); a shape with one story may leave it out.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub story_id: Option<String>,
}

/// Text inside a story: `start`/`end`, or the one occurrence of `find`, or (neither) all of it.
/// `expect` is the text there now; when it differs the batch fails with `content_mismatch`
/// (PD-03). A `find` target expects its own text.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckTextTarget {
    pub slide_id: String,
    pub shape_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub story_id: Option<String>,
    /// UTF-16 offsets in the story; both or neither.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub start: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub end: Option<u32>,
    /// Exact, case-sensitive, within one paragraph; more than one occurrence is
    /// `ambiguous_target`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub find: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub expect: Option<String>,
}

/// A slide layout: its part path in the package (`ppt/slideLayouts/slideLayout2.xml`), as
/// `DeckSummary.layouts` and `TemplateProfile` list them.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct LayoutRef {
    pub layout_id: String,
}

/// `p:ph/@type`, snake_cased (`ctrTitle` is `ctr_title`). A placeholder without a type is `obj`.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum PlaceholderKind {
    Title,
    CtrTitle,
    SubTitle,
    Body,
    Obj,
    Pic,
    Chart,
    Tbl,
    Dgm,
    Media,
    ClipArt,
    Dt,
    Ftr,
    SldNum,
    Hdr,
    SldImg,
}

/// A placeholder of the slide's layout, by type and, when the layout has several of the type,
/// `idx`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct PlaceholderRef {
    #[serde(rename = "type")]
    pub kind: PlaceholderKind,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub idx: Option<u32>,
}

/// Where a slide goes.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum SlidePosition {
    Start,
    End,
    After(String),
    Before(String),
}

/// A slide to read or check: its id, or its number (from 1) as people say it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(untagged)]
pub enum SlidePick {
    Index(u32),
    Id(String),
}

/// What the user has selected, for `ai/action` and `deck/focus`.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckSelection {
    pub slide_id: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub shape_ids: Vec<String>,
    /// Selected text, when the caret is in a text box.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub text: Option<DeckTextTarget>,
}

// ---- text ----

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum UnderlineStyle {
    None,
    Single,
    Double,
    Heavy,
    Dotted,
    Dash,
    Wavy,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum StrikeStyle {
    None,
    Single,
    Double,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum TextCaps {
    None,
    Small,
    All,
}

/// Character formatting. In an edit, fields left out stay as they are; in a read, the
/// effective values after inheritance (left out when mixed).
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct RunStyle {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bold: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub italic: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub underline: Option<UnderlineStyle>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub strike: Option<StrikeStyle>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub size_pt: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub color: Option<DeckColor>,
    /// `null` removes the highlight.
    #[serde(default, skip_serializing_if = "Option::is_none", deserialize_with = "present")]
    #[schemars(with = "Option<DeckColor>")]
    pub highlight: Option<Option<DeckColor>>,
    /// The Latin face; `+mj-lt` / `+mn-lt` are the theme's heading and body fonts.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub font_latin: Option<String>,
    /// The East Asian face; `+mj-ea` / `+mn-ea` for the theme's. The font box sets both.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub font_ea: Option<String>,
    /// Character spacing.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub spacing_pt: Option<f64>,
    /// Superscript above 0 (30), subscript below (-25); 0 is the baseline.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub baseline_pct: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub caps: Option<TextCaps>,
}

/// Where a link goes.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum HyperlinkTarget {
    Url {
        url: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        tooltip: Option<String>,
    },
    Slide {
        slide_id: String,
    },
    NextSlide,
    PreviousSlide,
    FirstSlide,
    LastSlide,
}

/// A run of text and its formatting.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct RunSpec {
    pub text: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub style: Option<RunStyle>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub link: Option<HyperlinkTarget>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ParagraphAlign {
    Left,
    Center,
    Right,
    Justify,
    /// 分散对齐.
    Distributed,
}

/// `a:buAutoNum@type`, snake_cased: `arabic_period` is "1.", `alpha_lc_paren_r` is "a)",
/// `ea1_chs_period` is "一、". Others the engine knows pass through as written.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum NumberScheme {
    ArabicPeriod,
    ArabicParenR,
    ArabicParenBoth,
    ArabicPlain,
    AlphaLcPeriod,
    AlphaUcPeriod,
    AlphaLcParenR,
    AlphaUcParenR,
    RomanLcPeriod,
    RomanUcPeriod,
    CircleNumDbPlain,
    Ea1ChsPeriod,
    Ea1ChsPlain,
}

/// A paragraph's list marker.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum BulletSpec {
    /// No marker, even where the layout gives one (`a:buNone`).
    None,
    /// The marker the layout or master gives this level.
    Inherit,
    Char {
        /// `•`, `–`, `▪`…
        char: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        font: Option<String>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        color: Option<DeckColor>,
        /// Marker size relative to the text, 1.0 = the same.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        size_ratio: Option<f64>,
    },
    Number {
        scheme: NumberScheme,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        start_at: Option<u32>,
    },
}

/// Line spacing: a multiple of single spacing (`{"multiple": 1.2}`) or exact points.
#[derive(Clone, Copy, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum LineSpacing {
    Multiple(f64),
    Points(f64),
}

/// Paragraph formatting. In an edit, fields left out stay as they are.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct ParagraphProps {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub align: Option<ParagraphAlign>,
    /// Outline level, 0–8.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub level: Option<u8>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bullet: Option<BulletSpec>,
    /// Left margin (`marL`).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub indent_pt: Option<f64>,
    /// First line relative to the margin (`indent`); negative hangs, as list items do.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub first_line_pt: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_spacing: Option<LineSpacing>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub space_before_pt: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub space_after_pt: Option<f64>,
}

/// One paragraph to write. It starts from the formatting of paragraph `like` (an id from
/// `deck/read`), or of the paragraph at the same position, then applies its own fields (those of
/// `ParagraphProps`): this is how a rewrite keeps the original look (§5.2.1).
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct ParagraphSpec {
    pub runs: Vec<RunSpec>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub like: Option<String>,
    // `ParagraphProps`, inline so a paragraph reads flat.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub align: Option<ParagraphAlign>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub level: Option<u8>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bullet: Option<BulletSpec>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub indent_pt: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub first_line_pt: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_spacing: Option<LineSpacing>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub space_before_pt: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub space_after_pt: Option<f64>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum TextDirection {
    Horizontal,
    /// Rotated 90°.
    Vertical,
    Vertical270,
    /// 竖排: East Asian characters upright (`eaVert`).
    EastAsianVertical,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum TextAnchor {
    Top,
    Middle,
    Bottom,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum TextAutofit {
    /// Text may overflow.
    None,
    /// Shrink text on overflow (`normAutofit`).
    Shrink,
    /// Resize the shape to fit the text (`spAutoFit`).
    Resize,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct TextInsets {
    pub left: f64,
    pub top: f64,
    pub right: f64,
    pub bottom: f64,
}

// ---- shapes ----

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct GradientStop {
    /// 0–1 along the gradient.
    pub position: f64,
    pub color: DeckColor,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum PictureFillMode {
    Stretch,
    Tile,
}

/// An image: a file the session may read, or its bytes.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum PictureSource {
    /// Read by the kernel under the session's file rules.
    Path { path: String },
    Bytes {
        /// Base64; at most 20 MiB decoded.
        base64: String,
        /// `image/png`, `image/jpeg`, `image/svg+xml`…
        content_type: String,
    },
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum FillSpec {
    /// No fill (`a:noFill`).
    None,
    Solid {
        color: DeckColor,
        /// Opacity, 1 = opaque.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        alpha: Option<f64>,
    },
    Gradient {
        #[serde(default, skip_serializing_if = "is_false")]
        radial: bool,
        /// Linear gradients only.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        angle_deg: Option<f64>,
        stops: Vec<GradientStop>,
    },
    Picture {
        source: PictureSource,
        mode: PictureFillMode,
    },
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum LineDash {
    Solid,
    Dot,
    Dash,
    DashDot,
    LongDash,
    LongDashDot,
    SysDot,
    SysDash,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ArrowHead {
    None,
    Triangle,
    Stealth,
    Diamond,
    Oval,
    Arrow,
}

/// An outline. In an edit, fields left out stay as they are.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct LineSpec {
    /// False draws no line (`a:ln/a:noFill`); the other fields are then ignored.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub visible: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub color: Option<DeckColor>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub width_pt: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dash: Option<LineDash>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub head: Option<ArrowHead>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tail: Option<ArrowHead>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct ShadowSpec {
    pub color: DeckColor,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub alpha: Option<f64>,
    pub blur_pt: f64,
    pub distance_pt: f64,
    pub angle_deg: f64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ZOrder {
    Front,
    Back,
    Forward,
    Backward,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ConnectorKind {
    Straight,
    Elbow,
    Curved,
}

/// A connector's end: glued to a shape's connection site, or free.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum ConnectorEnd {
    Shape {
        shape_id: String,
        /// The preset's connection site index (rectangles: 0 top, 1 left, 2 bottom, 3 right).
        site: u32,
    },
    Point {
        x: f64,
        y: f64,
    },
}

/// A placeholder shape moved onto another layout's placeholder `idx` (`set_layout`).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct PlaceholderMove {
    pub shape_id: String,
    pub idx: u32,
}

// ---- tables ----

/// Cells `first_row..=last_row` × `first_col..=last_col`, counted from 0.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct CellRange {
    pub first_row: u32,
    pub first_col: u32,
    pub last_row: u32,
    pub last_col: u32,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum InsertSide {
    Before,
    After,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum CellBorderSide {
    Top,
    Bottom,
    Left,
    Right,
    /// Lines between the range's cells.
    InsideH,
    InsideV,
    /// Every line of the range.
    All,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct CellBorder {
    pub side: CellBorderSide,
    pub line: LineSpec,
}

/// A table cell, counted from 0.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct CellPos {
    pub row: u32,
    pub col: u32,
}

/// One cell's text, for filling a table quickly.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct CellText {
    pub row: u32,
    pub col: u32,
    pub text: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub expect: Option<String>,
}

/// Which parts of a table style are switched on (`a:tblPr` flags).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct TableStyleFlags {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub header_row: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub total_row: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub first_col: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub last_col: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub banded_rows: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub banded_cols: Option<bool>,
}

/// One change to a table's structure or look. Cell text is edited as text: every cell is a
/// story (`deck/read` lists their `story_id`s), or `set_cells`.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum TableEdit {
    InsertRows {
        at: u32,
        side: InsertSide,
        #[serde(default = "one")]
        count: u32,
    },
    DeleteRows {
        at: u32,
        #[serde(default = "one")]
        count: u32,
    },
    InsertCols {
        at: u32,
        side: InsertSide,
        #[serde(default = "one")]
        count: u32,
    },
    DeleteCols {
        at: u32,
        #[serde(default = "one")]
        count: u32,
    },
    Merge {
        range: CellRange,
    },
    /// Undoes the merge the cell belongs to.
    Split {
        row: u32,
        col: u32,
    },
    SetCells {
        cells: Vec<CellText>,
    },
    CellStyle {
        range: CellRange,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        fill: Option<FillSpec>,
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        borders: Vec<CellBorder>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        anchor: Option<TextAnchor>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        margins_pt: Option<TextInsets>,
    },
    ColumnWidth {
        col: u32,
        width_pt: f64,
    },
    RowHeight {
        row: u32,
        height_pt: f64,
    },
    /// A table style by its GUID (`{5C22544A-…}`), and its flags.
    Style {
        #[serde(default, skip_serializing_if = "Option::is_none")]
        style_id: Option<String>,
        #[serde(default)]
        flags: TableStyleFlags,
    },
}

fn one() -> u32 {
    1
}

// ---- charts ----

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ChartType {
    /// Vertical bars.
    Column,
    /// Horizontal bars.
    Bar,
    Line,
    Pie,
    Doughnut,
    Area,
    Scatter,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ChartGrouping {
    Clustered,
    Stacked,
    PercentStacked,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct ChartSeries {
    pub name: String,
    /// One per category; `null` is a gap.
    pub values: Vec<Option<f64>>,
    /// Scatter charts: the x of each value.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub x_values: Option<Vec<f64>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub color: Option<DeckColor>,
    /// Combo charts: this series drawn as another type.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub chart_type: Option<ChartType>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub secondary_axis: bool,
}

/// A chart's data, written to the chart part's cache and its embedded workbook.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct ChartData {
    pub categories: Vec<String>,
    pub series: Vec<ChartSeries>,
    /// Excel number format of the values (`#,##0.0`, `0%`).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub number_format: Option<String>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum LegendPosition {
    None,
    Top,
    Bottom,
    Left,
    Right,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DataLabels {
    #[serde(default, skip_serializing_if = "is_false")]
    pub value: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub percent: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub category: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub series: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub number_format: Option<String>,
}

/// A chart's look. Fields left out stay as they are; `title: null` removes the title.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct ChartStyle {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub chart_type: Option<ChartType>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub grouping: Option<ChartGrouping>,
    #[serde(default, skip_serializing_if = "Option::is_none", deserialize_with = "present")]
    #[schemars(with = "Option<String>")]
    pub title: Option<Option<String>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub legend: Option<LegendPosition>,
    /// `null` hides the labels.
    #[serde(default, skip_serializing_if = "Option::is_none", deserialize_with = "present")]
    #[schemars(with = "Option<DataLabels>")]
    pub data_labels: Option<Option<DataLabels>>,
    #[serde(default, skip_serializing_if = "Option::is_none", deserialize_with = "present")]
    #[schemars(with = "Option<String>")]
    pub x_axis_title: Option<Option<String>>,
    #[serde(default, skip_serializing_if = "Option::is_none", deserialize_with = "present")]
    #[schemars(with = "Option<String>")]
    pub y_axis_title: Option<Option<String>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub gridlines: Option<bool>,
    /// Bar and column charts: the gap between categories, % of a bar's width.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub gap_width_pct: Option<u32>,
    /// Line charts.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub smooth: Option<bool>,
}

// ---- slides, motion, theme ----

/// Transitions Hermesh writes; others the file has are kept and shown by name.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum TransitionKind {
    None,
    Cut,
    Fade,
    Push,
    Wipe,
    Split,
    Reveal,
    Cover,
    Zoom,
    /// 平滑 (`p159:morph`); drawn as a fade in the slide show for now.
    Morph,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum MotionDirection {
    Left,
    Right,
    Up,
    Down,
    In,
    Out,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum AnimationClass {
    Entrance,
    Emphasis,
    Exit,
}

/// The effects Hermesh writes; the file's others are kept and listed by their preset id.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum AnimationEffect {
    Appear,
    Fade,
    FlyIn,
    Wipe,
    Zoom,
    FloatIn,
    Split,
    Pulse,
    Spin,
    GrowShrink,
    Teeter,
    Disappear,
    FlyOut,
    FloatOut,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum AnimationTrigger {
    OnClick,
    WithPrevious,
    AfterPrevious,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct AnimationSpec {
    pub effect: AnimationEffect,
    pub trigger: AnimationTrigger,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub direction: Option<MotionDirection>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub duration_ms: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub delay_ms: Option<u32>,
    /// Position in the slide's sequence, from 1; at the end when left out.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub order: Option<u32>,
}

/// The theme's twelve colours, `#RRGGBB`. In `set_theme_colors`, those left out stay.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct ThemeColors {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dk1: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub lt1: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dk2: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub lt2: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub accent1: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub accent2: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub accent3: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub accent4: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub accent5: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub accent6: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub hlink: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fol_hlink: Option<String>,
}

/// The theme's font scheme: headings (major) and body (minor), per script. In
/// `set_theme_fonts`, those left out stay.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct ThemeFonts {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub major_latin: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub major_ea: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub minor_latin: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub minor_ea: Option<String>,
}

/// What happens to the content when the slide size changes.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum SlideResize {
    /// Positions and sizes stay.
    Keep,
    /// Scale down to fit inside the new size (PowerPoint's 确保适合).
    Fit,
    /// Scale up to fill it (最大化).
    Fill,
}

/// A section, from its first slide to the next section's.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct SectionSpec {
    /// Keeps an existing section's id; a new one gets an id.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub section_id: Option<String>,
    pub name: String,
    pub first_slide_id: String,
}

/// The date in the footer.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum FooterDate {
    Hidden,
    Fixed { text: String },
    /// Updated when the deck is opened; `format` is the field type (`datetime1`, `datetime13`…).
    Auto { format: String },
}

/// How `paste` brings content in (粘贴选项).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum PasteMode {
    KeepSource,
    UseDestinationTheme,
    PlainText,
}

/// Where pasted content goes: onto a slide (shapes), among slides, or into text.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum PasteAt {
    Slide {
        slide_id: String,
        /// Top left of the pasted shapes' bounds; where they were copied from when left out.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        at: Option<DeckPoint>,
    },
    Slides {
        position: SlidePosition,
    },
    Text {
        target: DeckTextTarget,
    },
}

// ---- data links (§5.2.5) ----

/// What on the slide a data link feeds.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum DataLinkTarget {
    /// The chart's data: the range is categories down the first column, series across.
    Chart { shape_id: String },
    Table { shape_id: String },
    /// A number in text, `start`/`end` in the story when linked.
    Text {
        shape_id: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        story_id: Option<String>,
        start: u32,
        end: u32,
    },
}

/// Cells in a workbook, as the spreadsheet tools name them.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DataLinkSource {
    /// Relative to the deck's folder.
    pub path: String,
    pub sheet: String,
    /// `D15` or `B3:F9`.
    pub range: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum DataLinkStatus {
    Fresh,
    /// The source's values differ from the snapshot: offer a refresh.
    Stale,
    /// The file, sheet or range is gone.
    Missing,
    /// Not checked yet.
    Unknown,
}

/// A link from slide content to a spreadsheet, stored in the deck (custom XML, kept by
/// PowerPoint).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DataLink {
    pub link_id: String,
    pub slide_id: String,
    pub target: DataLinkTarget,
    pub source: DataLinkSource,
    /// Excel number format used to show a linked number in text.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub format: Option<String>,
    /// The values when linked or last refreshed, row by row, as shown.
    pub snapshot: Vec<Vec<String>>,
    /// Of the source cells' values at the snapshot.
    pub source_hash: String,
    /// Unix milliseconds.
    pub refreshed_at: i64,
    pub status: DataLinkStatus,
}

// ---- the operations (§6.3) ----

/// One edit to a deck. People (toolbar, shortcuts) and agents use the same ops; a batch of them
/// becomes one proposal or one undo step (§6.3).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "op", rename_all = "snake_case", deny_unknown_fields)]
pub enum DeckOp {
    // text
    InsertText {
        target: DeckTextTarget,
        /// Which end of the target.
        #[serde(default)]
        at: TextEdge,
        text: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        style: Option<RunStyle>,
    },
    /// `\n` starts a new paragraph that takes the formatting of the one it splits.
    ReplaceText {
        target: DeckTextTarget,
        text: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        style: Option<RunStyle>,
    },
    DeleteText {
        target: DeckTextTarget,
    },
    FormatText {
        target: DeckTextTarget,
        style: RunStyle,
    },
    /// 清除格式: runs go back to what the paragraph, shape and layout give them.
    ClearTextFormat {
        target: DeckTextTarget,
    },
    /// Replaces a story's paragraphs (rewrites, outline edits). `expect` is the story's whole
    /// text now.
    SetParagraphs {
        target: DeckStoryTarget,
        paragraphs: Vec<ParagraphSpec>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        expect: Option<String>,
    },
    /// Every paragraph the target touches.
    SetParagraphProps {
        target: DeckTextTarget,
        props: ParagraphProps,
    },
    SetTextBody {
        shape: ShapeRef,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        direction: Option<TextDirection>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        anchor: Option<TextAnchor>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        autofit: Option<TextAutofit>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        insets_pt: Option<TextInsets>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        wrap: Option<bool>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        columns: Option<u8>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        column_gap_pt: Option<f64>,
    },
    SetHyperlink {
        /// A link on text; or, instead, `shape`: a click on the whole shape.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        text: Option<DeckTextTarget>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        shape: Option<ShapeRef>,
        /// `null` removes the link.
        link: Option<HyperlinkTarget>,
    },
    /// Replaces the speaker notes; empty text clears them.
    SetNotes {
        slide: SlideRef,
        text: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        expect: Option<String>,
    },

    // objects
    /// A preset shape (`rect`, `roundRect`, `ellipse`, `rightArrow`, `chevron`… the
    /// `a:prstGeom` names).
    AddShape {
        slide: SlideRef,
        #[serde(default, rename = "ref", skip_serializing_if = "Option::is_none")]
        reference: Option<String>,
        preset: String,
        rect: DeckRect,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        name: Option<String>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        fill: Option<FillSpec>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        line: Option<LineSpec>,
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        paragraphs: Vec<ParagraphSpec>,
    },
    AddTextBox {
        slide: SlideRef,
        #[serde(default, rename = "ref", skip_serializing_if = "Option::is_none")]
        reference: Option<String>,
        rect: DeckRect,
        paragraphs: Vec<ParagraphSpec>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        name: Option<String>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        autofit: Option<TextAutofit>,
    },
    AddPicture {
        slide: SlideRef,
        #[serde(default, rename = "ref", skip_serializing_if = "Option::is_none")]
        reference: Option<String>,
        source: PictureSource,
        /// Left out: the image's own size, centred.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        rect: Option<DeckRect>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        name: Option<String>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        alt_text: Option<String>,
    },
    AddTable {
        slide: SlideRef,
        #[serde(default, rename = "ref", skip_serializing_if = "Option::is_none")]
        reference: Option<String>,
        rect: DeckRect,
        rows: u32,
        cols: u32,
        /// Row by row; may be shorter than the table.
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        cells: Vec<Vec<String>>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        style_id: Option<String>,
        #[serde(default)]
        flags: TableStyleFlags,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        name: Option<String>,
    },
    AddChart {
        slide: SlideRef,
        #[serde(default, rename = "ref", skip_serializing_if = "Option::is_none")]
        reference: Option<String>,
        rect: DeckRect,
        chart_type: ChartType,
        data: ChartData,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        style: Option<ChartStyle>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        name: Option<String>,
    },
    AddConnector {
        slide: SlideRef,
        #[serde(default, rename = "ref", skip_serializing_if = "Option::is_none")]
        reference: Option<String>,
        kind: ConnectorKind,
        start: ConnectorEnd,
        end: ConnectorEnd,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        line: Option<LineSpec>,
    },
    /// Copies of shapes, on their slide or another, `offset` from the originals. The refs, when
    /// given, name the copies in order.
    DuplicateShapes {
        shapes: Vec<ShapeRef>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        to_slide: Option<SlideRef>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        offset: Option<DeckPoint>,
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        refs: Vec<String>,
    },
    DeleteShapes {
        shapes: Vec<ShapeRef>,
    },
    /// Position, size, rotation and flips of one shape. Aligning and distributing several shapes
    /// is one `transform` each.
    Transform {
        shape: ShapeRef,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        rect: Option<DeckRect>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        rotation_deg: Option<f64>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        flip_h: Option<bool>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        flip_v: Option<bool>,
        /// The rectangle the shape has now; `content_mismatch` when it moved.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        expect_rect: Option<DeckRect>,
    },
    SetFill {
        shape: ShapeRef,
        fill: FillSpec,
    },
    SetLine {
        shape: ShapeRef,
        line: LineSpec,
    },
    /// `null` removes the shadow.
    SetEffects {
        shape: ShapeRef,
        shadow: Option<ShadowSpec>,
    },
    /// Another preset, and/or its adjust handles (`adj`, `adj1`…).
    SetGeometry {
        shape: ShapeRef,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        preset: Option<String>,
        #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
        adjust: BTreeMap<String, f64>,
    },
    /// What the selection pane shows: name, alternative text, hidden, locked aspect ratio.
    SetShapeProps {
        shape: ShapeRef,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        name: Option<String>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        alt_text: Option<String>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        decorative: Option<bool>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        hidden: Option<bool>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        lock_aspect: Option<bool>,
    },
    /// Shapes of one slide into a group.
    Group {
        shapes: Vec<ShapeRef>,
        #[serde(default, rename = "ref", skip_serializing_if = "Option::is_none")]
        reference: Option<String>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        name: Option<String>,
    },
    Ungroup {
        shape: ShapeRef,
    },
    ZOrder {
        shape: ShapeRef,
        to: ZOrder,
    },
    /// Text (or a picture) into a placeholder of the slide's layout, making the placeholder
    /// shape when the slide has none yet. `ref` names that shape.
    FillPlaceholder {
        slide: SlideRef,
        placeholder: PlaceholderRef,
        #[serde(default, rename = "ref", skip_serializing_if = "Option::is_none")]
        reference: Option<String>,
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        paragraphs: Vec<ParagraphSpec>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        picture: Option<PictureSource>,
    },
    /// 重置: placeholders back to the layout's position, size and formatting.
    ResetSlide {
        slide: SlideRef,
    },
    TableEdit {
        shape: ShapeRef,
        edit: TableEdit,
    },
    ChartData {
        shape: ShapeRef,
        data: ChartData,
    },
    ChartStyle {
        shape: ShapeRef,
        style: ChartStyle,
    },

    // slides
    InsertSlide {
        position: SlidePosition,
        #[serde(default, rename = "ref", skip_serializing_if = "Option::is_none")]
        reference: Option<String>,
        /// The layout of the slide before it when left out.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        layout: Option<LayoutRef>,
    },
    DuplicateSlide {
        slide: SlideRef,
        #[serde(default, rename = "ref", skip_serializing_if = "Option::is_none")]
        reference: Option<String>,
        /// Right after the original when left out.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        position: Option<SlidePosition>,
    },
    DeleteSlide {
        slide: SlideRef,
    },
    MoveSlide {
        slide: SlideRef,
        position: SlidePosition,
    },
    /// Placeholders move to the new layout's by type; `mapping` overrides that.
    SetLayout {
        slide: SlideRef,
        layout: LayoutRef,
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        mapping: Vec<PlaceholderMove>,
    },
    /// `null` follows the layout again.
    SetBackground {
        slide: SlideRef,
        fill: Option<FillSpec>,
    },
    SetSlideHidden {
        slide: SlideRef,
        hidden: bool,
    },
    SetTransition {
        slide: SlideRef,
        kind: TransitionKind,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        direction: Option<MotionDirection>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        duration_ms: Option<u32>,
        #[serde(default = "yes")]
        advance_on_click: bool,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        advance_after_ms: Option<u32>,
    },
    /// The shape's animation of `class`; `animation: null` removes it.
    SetAnimation {
        shape: ShapeRef,
        class: AnimationClass,
        animation: Option<AnimationSpec>,
    },

    // the whole deck
    SetThemeColors {
        colors: ThemeColors,
    },
    SetThemeFonts {
        fonts: ThemeFonts,
    },
    SetSlideSize {
        width_pt: f64,
        height_pt: f64,
        #[serde(default = "keep")]
        resize: SlideResize,
    },
    /// Replaces the section list, in deck order; empty removes the sections. Moving a section is
    /// `move_slide` ops, then this.
    SetSections {
        sections: Vec<SectionSpec>,
    },
    /// 页眉页脚. Fields left out stay; `slides` empty is every slide.
    SetHeaderFooter {
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        slides: Vec<SlideRef>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        slide_number: Option<bool>,
        /// `null` hides the footer.
        #[serde(default, skip_serializing_if = "Option::is_none", deserialize_with = "present")]
        #[schemars(with = "Option<String>")]
        footer: Option<Option<String>>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        date: Option<FooterDate>,
        /// 标题幻灯片中不显示.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        hide_on_title: Option<bool>,
    },
    /// Content `deck/copy` gave, from this deck or another.
    Paste {
        at: PasteAt,
        clip: String,
        mode: PasteMode,
        /// Names the pasted shapes or slides, in order.
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        refs: Vec<String>,
    },
    /// Links content to a workbook; `link_id` given replaces that link.
    SetDataLink {
        slide: SlideRef,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        link_id: Option<String>,
        target: DataLinkTarget,
        source: DataLinkSource,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        format: Option<String>,
    },
    RemoveDataLink {
        link_id: String,
    },
}

fn keep() -> SlideResize {
    SlideResize::Keep
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum TextEdge {
    Start,
    #[default]
    End,
}

/// Every `op` name, in the order of §12.2. `DeckOpenResult.supported_ops` is a subset.
pub const DECK_OPS: &[&str] = &[
    "insert_text",
    "replace_text",
    "delete_text",
    "format_text",
    "clear_text_format",
    "set_paragraphs",
    "set_paragraph_props",
    "set_text_body",
    "set_hyperlink",
    "set_notes",
    "add_shape",
    "add_text_box",
    "add_picture",
    "add_table",
    "add_chart",
    "add_connector",
    "duplicate_shapes",
    "delete_shapes",
    "transform",
    "set_fill",
    "set_line",
    "set_effects",
    "set_geometry",
    "set_shape_props",
    "group",
    "ungroup",
    "z_order",
    "fill_placeholder",
    "reset_slide",
    "table_edit",
    "chart_data",
    "chart_style",
    "insert_slide",
    "duplicate_slide",
    "delete_slide",
    "move_slide",
    "set_layout",
    "set_background",
    "set_slide_hidden",
    "set_transition",
    "set_animation",
    "set_theme_colors",
    "set_theme_fonts",
    "set_slide_size",
    "set_sections",
    "set_header_footer",
    "paste",
    "set_data_link",
    "remove_data_link",
];

// ---- edit batches ----

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum DeckEditMode {
    /// One proposal, applied only when accepted (§6.4). The default for agents.
    #[default]
    Propose,
    /// Applied now, as one undo step.
    Apply,
    /// Checked against the deck; nothing is changed or proposed.
    Validate,
}

/// `deck/edit`, and the `deck_edit` tool without `session_id`: a batch that lands whole or not at
/// all.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckEditParams {
    pub session_id: String,
    pub deck_id: String,
    /// At most 256.
    pub ops: Vec<DeckOp>,
    /// What the batch does, in the user's words ("按 Q3 财报更新第 3–5 页的营收").
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
    /// The version the targets were read at; behind, the batch fails with `stale_version`.
    pub version: String,
    #[serde(default)]
    pub mode: DeckEditMode,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum SlideChangeKind {
    Changed,
    Inserted,
    Deleted,
    Moved,
}

/// What a batch or proposal does to one slide, for the canvas markers and the approval card.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct SlideChangeSummary {
    pub slide_id: String,
    /// From 1; where it ends up, or was for a deleted slide.
    pub index: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    pub kind: SlideChangeKind,
    /// Shapes added, removed or changed.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub shape_ids: Vec<String>,
    /// In the user's words: "替换 3 处文字、移动 1 个对象".
    pub summary: String,
}

/// The engine's refusal codes (`EditFailureCode`), snake_cased.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum DeckEditFailureCode {
    /// The deck changed since `version`: read again, then edit.
    StaleVersion,
    MissingTarget,
    /// A `find` matched more than once, or a story was left out where a shape has several.
    AmbiguousTarget,
    /// `expect` or `expect_rect` differs from what is there.
    ContentMismatch,
    /// Two ops write the same text or object.
    OverlappingSteps,
    /// Not something Hermesh can do to this content (or this build); say so, or suggest
    /// PowerPoint.
    Unsupported,
    InvalidStep,
    LimitExceeded,
}

/// Why a batch did not land. The deck is as it was at `version`.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckEditFailure {
    pub code: DeckEditFailureCode,
    /// The op, from 0, in `ops`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub op_index: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub conflicting_op_index: Option<u32>,
    pub message: String,
    /// The deck's version now.
    pub version: String,
    /// `content_mismatch`: what the target holds now (at most 512 characters).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub actual: Option<String>,
    /// `stale_version`: the deck now, so an agent can re-read without another call.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub latest: Option<Box<DeckSummary>>,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckEditResult {
    /// The deck's version after the batch; unchanged by `propose` and `validate`.
    pub version: String,
    /// `propose`: the proposal made.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub proposal_id: Option<String>,
    /// `apply`: false when every op was a no-op.
    #[serde(default, skip_serializing_if = "is_false")]
    pub applied: bool,
    /// The ids the batch's `ref`s got.
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub refs: BTreeMap<String, String>,
    pub slides: Vec<SlideChangeSummary>,
    /// The deterministic check of the slides the batch touches, after it (§6.5).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub diagnostics: Vec<DeckDiagnostic>,
    /// Present when the batch did not land; nothing else then changed.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub failure: Option<DeckEditFailure>,
}

// ---- opening, syncing, saving (§6.2) ----

/// `deck/open`. Progress goes out as `pptx/readProgress`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckOpenParams {
    pub session_id: String,
    pub path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub progress_id: Option<String>,
    /// The caller joins as a Yrs replica (the editor): the result carries the state.
    #[serde(default)]
    pub replica: bool,
    /// Drop the kernel's unsaved edits of this file and read it anew ("load the disk's version").
    #[serde(default, skip_serializing_if = "is_false")]
    pub discard: bool,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckOpenResult {
    /// From the file's canonical path: the same in every session.
    pub deck_id: String,
    pub path: String,
    /// SHA-256 of the file as read.
    pub hash: String,
    pub writable: bool,
    pub version: String,
    pub dirty: bool,
    /// `DECK_CONTRACT_VERSION` of the kernel.
    pub contract_version: u32,
    /// The engine build (`betteroffice 0.5.1+her.3`): a replica built from another is refused.
    pub engine: String,
    /// Replicas: the whole Yrs document as one v1 update, base64.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub state: Option<String>,
    /// Replicas: the file as first opened, base64, which the state builds on (replicas open
    /// from these bytes). Transitional until it streams over `herdoc://` (kernel-authority §4).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source: Option<String>,
    /// Explicit font families in the package, plus the host's Latin and CJK fallbacks.
    pub font_families: Vec<String>,
    /// The `op`s this kernel and engine carry out; others are `unsupported`.
    pub supported_ops: Vec<String>,
    pub summary: DeckSummary,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckIdParams {
    pub session_id: String,
    pub deck_id: String,
}

/// `deck/sync` from a replica: its local edits, and what it lacks.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckSyncParams {
    pub session_id: String,
    pub deck_id: String,
    /// A Yrs v1 update, base64; empty to only ask.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub update: String,
    /// The replica's state vector, base64: the result then carries what it lacks.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub state_vector: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeckSyncResult {
    pub version: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub update: Option<String>,
}

/// Who made a change.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum DeckOrigin {
    /// Someone in an editor (this replica or another).
    Human,
    Agent,
    /// The kernel itself: reload, recovery, data-link refresh.
    System,
}

/// `deck/sync` from the kernel: edits made elsewhere, for every replica of the deck but the one
/// that made them.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeckSyncNotice {
    pub deck_id: String,
    pub update: String,
    pub version: String,
    pub origin: DeckOrigin,
    /// The accepted proposal the update comes from.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub proposal_id: Option<String>,
}

/// `deck/changed`: the deck's state, after any edit, undo, save or reload; to every connection
/// that opened it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeckChangedParams {
    pub deck_id: String,
    pub version: String,
    pub dirty: bool,
    /// After a save: the file's new hash.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub hash: Option<String>,
    pub origin: DeckOrigin,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub slide_ids: Vec<String>,
    /// The file changed on disk under unsaved edits: the editor offers to reload or keep.
    #[serde(default, skip_serializing_if = "is_false")]
    pub conflict: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckSaveParams {
    pub session_id: String,
    pub deck_id: String,
    /// Save as: another file.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,
    /// Write over a file that changed on disk since it was read.
    #[serde(default, skip_serializing_if = "is_false")]
    pub force: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeckSaveResult {
    pub path: String,
    pub hash: String,
    pub version: String,
}

/// `deck/focus`: what the user is looking at, sent as it changes, for the short context block
/// each message carries (§6.6) and for "this slide" in commands.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckFocusParams {
    pub session_id: String,
    pub deck_id: String,
    pub slide_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub selection: Option<DeckSelection>,
}

// ---- reading (§7) ----

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct SlideSize {
    pub width_pt: f64,
    pub height_pt: f64,
    /// `16:9`, `4:3`, or `custom`.
    pub ratio: String,
}

/// What a layout is for, from its type or, for templates that name layouts loosely, its
/// placeholders (§6.8).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum LayoutTag {
    Title,
    Section,
    TitleContent,
    TwoContent,
    Comparison,
    TitleOnly,
    Blank,
    PictureCaption,
    ContentCaption,
    Other,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct PlaceholderInfo {
    #[serde(rename = "type")]
    pub kind: PlaceholderKind,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub idx: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    pub rect: DeckRect,
    /// The size its first level of text gets.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub size_pt: Option<f64>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct LayoutInfo {
    pub layout_id: String,
    pub name: String,
    /// `p:sldLayout/@type` as written (`title`, `obj`, `twoObj`…).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub layout_type: Option<String>,
    pub tag: LayoutTag,
    pub master_id: String,
    pub placeholders: Vec<PlaceholderInfo>,
    /// Slides using it.
    pub used_by: u32,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct ThemeInfo {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    /// All twelve, resolved.
    pub colors: ThemeColors,
    pub fonts: ThemeFonts,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SectionInfo {
    pub section_id: String,
    pub name: String,
    pub first_slide_id: String,
    pub slide_count: u32,
}

/// One line of the summary per slide.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SlideLine {
    pub index: u32,
    pub slide_id: String,
    pub layout_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    pub shape_count: u32,
    #[serde(default, skip_serializing_if = "is_false")]
    pub has_table: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub has_chart: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub has_picture: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub has_notes: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub hidden: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub section_id: Option<String>,
}

/// The structure summary `deck_open` gives (§7): at most about 2,000 tokens.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckSummary {
    pub deck_id: String,
    pub path: String,
    pub version: String,
    pub slide_count: u32,
    pub size: SlideSize,
    pub layouts: Vec<LayoutInfo>,
    pub theme: ThemeInfo,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub sections: Vec<SectionInfo>,
    /// Every slide, unless `truncated`: then the first and last ones, and `deck/read` for the rest.
    pub slides: Vec<SlideLine>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub truncated: bool,
    /// Data links whose source changed (§5.2.5).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub stale_links: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub pending_proposals: Option<u32>,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ReadDetail {
    /// Text and a style summary per shape.
    #[default]
    Brief,
    /// Paragraphs and runs with their formatting.
    Full,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckReadParams {
    pub session_id: String,
    pub deck_id: String,
    /// Empty: every slide, at most 30 a call.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub slides: Vec<SlidePick>,
    #[serde(default)]
    pub detail: ReadDetail,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum DeckShapeKind {
    Shape,
    TextBox,
    Picture,
    Table,
    Chart,
    Group,
    Connector,
    /// SmartArt, video, OLE and the like: kept, not edited.
    Other,
}

/// The look of a shape's text, as one line can say it; mixed values are left out.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct TextLook {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub font: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub size_pt: Option<f64>,
    /// `#RRGGBB`, resolved.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub color: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bold: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub align: Option<ParagraphAlign>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub autofit: Option<TextAutofit>,
    /// The engine's layout says the text does not fit.
    #[serde(default, skip_serializing_if = "is_false")]
    pub overflows: bool,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct RunRead {
    pub start: u32,
    pub end: u32,
    pub text: String,
    pub style: RunStyle,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub link: Option<HyperlinkTarget>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct ParagraphRead {
    pub paragraph_id: String,
    pub start: u32,
    pub end: u32,
    pub text: String,
    /// Effective, after inheritance.
    pub props: ParagraphProps,
    pub runs: Vec<RunRead>,
    /// False when an edit here could not keep a field (a slide number…).
    #[serde(default = "yes")]
    pub editable: bool,
}

/// A text story of a shape: its body, or one table cell's.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct StoryRead {
    pub story_id: String,
    pub text: String,
    /// A table cell's.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cell: Option<CellPos>,
    /// `detail: full` only.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub paragraphs: Vec<ParagraphRead>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct TableInfo {
    pub rows: u32,
    pub cols: u32,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub merges: Vec<CellRange>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub style_id: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct ChartInfo {
    pub chart_type: ChartType,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    /// `detail: full`, or always for small charts (≤ 200 values).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub data: Option<ChartData>,
    pub series_count: u32,
    pub category_count: u32,
}

/// One object of a slide, in z order (back first).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct ShapeInfo {
    pub shape_id: String,
    pub name: String,
    pub kind: DeckShapeKind,
    /// The `a:prstGeom` name, for preset shapes.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub preset: Option<String>,
    pub rect: DeckRect,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub rotation_deg: Option<f64>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub flip_h: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub flip_v: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub placeholder: Option<PlaceholderRef>,
    /// The group it is in.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub parent_id: Option<String>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub hidden: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub alt_text: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub stories: Vec<StoryRead>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub text_look: Option<TextLook>,
    /// `#RRGGBB` of a solid fill, `none`, `gradient` or `picture`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fill: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub table: Option<TableInfo>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub chart: Option<ChartInfo>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub data_link_id: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckSlideRead {
    pub slide_id: String,
    pub index: u32,
    pub layout_id: String,
    pub layout_name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    /// The slide as Markdown, a block per object headed with its shape id (`<!-- s12 -->`).
    pub markdown: String,
    pub shapes: Vec<ShapeInfo>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub notes: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub transition: Option<TransitionKind>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub hidden: bool,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckReadResult {
    pub version: String,
    pub slides: Vec<DeckSlideRead>,
    /// Slides asked for but left out to stay in the limit: read them next.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub more: Vec<String>,
}

/// `deck/find`: text or numbers, case folded unless `case_sensitive`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckFindParams {
    pub session_id: String,
    pub deck_id: String,
    pub query: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub slides: Vec<SlidePick>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub case_sensitive: bool,
    /// Search the speaker notes too.
    #[serde(default, skip_serializing_if = "is_false")]
    pub notes: bool,
    /// Defaults to 100; at most 1,000.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub limit: Option<u32>,
}

/// One hit. `slide_id`, `shape_id`, `story_id`, `start`, `end` and `text` (as `expect`) make a
/// `DeckTextTarget`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeckFindMatch {
    pub slide_id: String,
    pub index: u32,
    pub shape_id: String,
    pub shape_name: String,
    pub story_id: String,
    pub start: u32,
    pub end: u32,
    pub text: String,
    /// Up to 40 characters either side.
    pub before: String,
    pub after: String,
    /// In the speaker notes.
    #[serde(default, skip_serializing_if = "is_false")]
    pub in_notes: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeckFindResult {
    pub version: String,
    pub matches: Vec<DeckFindMatch>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub truncated: bool,
}

/// `deck/view`: a slide rasterized on this machine (§6.5).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckViewParams {
    pub session_id: String,
    pub deck_id: String,
    pub slide: SlidePick,
    /// Only this part of the slide.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub region: Option<DeckRect>,
    /// Pixels across the whole slide; 1280 when left out, at most 2560.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub width_px: Option<u32>,
    /// The slide as this proposal would leave it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub proposal_id: Option<String>,
    /// With `proposal_id`: before and after, side by side in one image.
    #[serde(default, skip_serializing_if = "is_false")]
    pub compare: bool,
    /// Outlined in the image.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub highlight: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeckViewResult {
    pub png_base64: String,
    pub width_px: u32,
    pub height_px: u32,
    pub version: String,
}

/// `deck/summary`: the structure summary now.
pub type DeckSummaryParams = DeckIdParams;

/// `deck/copy`: shapes, slides or text, for `paste` here or in another deck.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckCopyParams {
    pub session_id: String,
    pub deck_id: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub shapes: Vec<ShapeRef>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub slides: Vec<SlideRef>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub text: Option<DeckTextTarget>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeckCopyResult {
    /// Opaque to clients, base64: the engine's clip with its media, fonts and theme.
    pub clip: String,
    /// For the system clipboard.
    pub plain_text: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub png_base64: Option<String>,
}

// ---- proposals (§6.4) ----

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ProposalSource {
    Human,
    Agent,
}

/// What made a proposal, for the AI ledger and the card's wording.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ProposalOrigin {
    /// `deck_edit` or `deck/edit`.
    Edit,
    AiAction,
    CheckFix,
    Layout,
    Generate,
    LinkRefresh,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ProposalStatus {
    Pending,
    /// Wholly, or the slides in `accepted_slides`.
    Accepted,
    Rejected,
    /// Accepted, then undone from the AI ledger.
    Reverted,
    /// Its targets changed: accepting needs `force`, or a new proposal.
    Stale,
    /// Replaced by a newer proposal of the same action ("再来一版").
    Superseded,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckProposalInfo {
    pub proposal_id: String,
    pub deck_id: String,
    pub source: ProposalSource,
    pub origin: ProposalOrigin,
    /// The session whose agent or action made it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub session_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
    pub status: ProposalStatus,
    /// The deck version it was made against.
    pub base_version: String,
    pub slides: Vec<SlideChangeSummary>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub accepted_slides: Vec<String>,
    /// Slides whose targets changed under it.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub stale_slides: Vec<String>,
    pub ops: Vec<DeckOp>,
    /// The engine's edits for the same change, JSON, for a replica to preview it on the canvas
    /// with its own engine. Opaque to clients; tied to `DeckOpenResult.engine`.
    pub engine_edits: String,
    /// Unix milliseconds.
    pub created_at: i64,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckProposalListParams {
    pub session_id: String,
    pub deck_id: String,
    /// Accepted, rejected and reverted ones too: the AI ledger.
    #[serde(default, skip_serializing_if = "is_false")]
    pub closed: bool,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckProposalListResult {
    pub proposals: Vec<DeckProposalInfo>,
}

/// `deck/undo`: takes back the agent's edit `edit` (the revision its `deck_edit` made), when it
/// is the agent's latest one still in the deck (presentation-deck-edit §5).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckUndoParams {
    pub session_id: String,
    pub deck_id: String,
    pub edit: u64,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DeckUndoResult {
    /// The deck's revision after it.
    pub revision: u64,
}

/// `deck/proposal/accept`, `deck/proposal/reject`: all of it, or only `slide_ids` (逐页). The
/// rest stays pending as `remaining_id`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckProposalDecideParams {
    pub session_id: String,
    pub deck_id: String,
    pub proposal_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub slide_ids: Option<Vec<String>>,
    /// Accept although targets changed (stale).
    #[serde(default, skip_serializing_if = "is_false")]
    pub force: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckProposalIdParams {
    pub session_id: String,
    pub deck_id: String,
    pub proposal_id: String,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckProposalResult {
    pub proposal: DeckProposalInfo,
    pub version: String,
    /// The part not accepted or rejected, still pending.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub remaining_id: Option<String>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ProposalChange {
    Created,
    Updated,
    Accepted,
    Rejected,
    Reverted,
    Stale,
    Superseded,
}

/// `deck/proposal`: to every connection that opened the deck.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckProposalEvent {
    pub deck_id: String,
    pub change: ProposalChange,
    pub proposal: DeckProposalInfo,
}

// ---- checks (§5.2.3) ----

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum DiagnosticSeverity {
    Error,
    Warning,
    Info,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum DiagnosticCategory {
    Layout,
    Consistency,
    Content,
    Data,
    Accessibility,
    File,
}

/// Who finds a problem: the rule engine (milliseconds, always run) or a model (on request).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum DiagnosticChecker {
    Rule,
    Ai,
}

/// The built-in checks (§5.2.3). `DeckDiagnostic.rule` is one of these, or a scene pack's own
/// (`pack.<pack>.<rule>`).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub enum DiagnosticRule {
    #[serde(rename = "layout.text_overflow")]
    TextOverflow,
    #[serde(rename = "layout.off_slide")]
    OffSlide,
    #[serde(rename = "layout.safe_area")]
    SafeArea,
    #[serde(rename = "layout.overlap")]
    Overlap,
    #[serde(rename = "layout.near_alignment")]
    NearAlignment,
    #[serde(rename = "layout.title_drift")]
    TitleDrift,
    #[serde(rename = "layout.empty_placeholder")]
    EmptyPlaceholder,
    #[serde(rename = "layout.too_dense")]
    TooDense,
    #[serde(rename = "layout.weak_hierarchy")]
    WeakHierarchy,
    #[serde(rename = "consistency.font_count")]
    FontCount,
    #[serde(rename = "consistency.off_theme_color")]
    OffThemeColor,
    #[serde(rename = "consistency.title_size")]
    TitleSize,
    #[serde(rename = "consistency.bullet_mix")]
    BulletMix,
    #[serde(rename = "consistency.footer_missing")]
    FooterMissing,
    #[serde(rename = "consistency.slide_number_gap")]
    SlideNumberGap,
    #[serde(rename = "consistency.terms")]
    Terms,
    #[serde(rename = "consistency.units")]
    Units,
    #[serde(rename = "content.typo")]
    Typo,
    #[serde(rename = "content.grammar")]
    Grammar,
    #[serde(rename = "content.punctuation_width")]
    PunctuationWidth,
    #[serde(rename = "content.cjk_latin_spacing")]
    CjkLatinSpacing,
    #[serde(rename = "content.number_mismatch")]
    NumberMismatch,
    #[serde(rename = "content.claim_conflict")]
    ClaimConflict,
    #[serde(rename = "data.link_stale")]
    LinkStale,
    #[serde(rename = "data.no_source")]
    NoSource,
    #[serde(rename = "a11y.contrast")]
    Contrast,
    #[serde(rename = "a11y.alt_text")]
    AltText,
    #[serde(rename = "a11y.small_text")]
    SmallText,
    #[serde(rename = "file.image_resolution")]
    ImageResolution,
    #[serde(rename = "file.image_size")]
    ImageSize,
    #[serde(rename = "file.missing_font")]
    MissingFont,
}

/// What `DiagnosticRule::spec` says about a rule.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RuleSpec {
    pub id: &'static str,
    pub category: DiagnosticCategory,
    pub checker: DiagnosticChecker,
    pub severity: DiagnosticSeverity,
    /// It can come with `fix` ops.
    pub fixable: bool,
}

impl DiagnosticRule {
    pub const ALL: &'static [DiagnosticRule] = &[
        Self::TextOverflow,
        Self::OffSlide,
        Self::SafeArea,
        Self::Overlap,
        Self::NearAlignment,
        Self::TitleDrift,
        Self::EmptyPlaceholder,
        Self::TooDense,
        Self::WeakHierarchy,
        Self::FontCount,
        Self::OffThemeColor,
        Self::TitleSize,
        Self::BulletMix,
        Self::FooterMissing,
        Self::SlideNumberGap,
        Self::Terms,
        Self::Units,
        Self::Typo,
        Self::Grammar,
        Self::PunctuationWidth,
        Self::CjkLatinSpacing,
        Self::NumberMismatch,
        Self::ClaimConflict,
        Self::LinkStale,
        Self::NoSource,
        Self::Contrast,
        Self::AltText,
        Self::SmallText,
        Self::ImageResolution,
        Self::ImageSize,
        Self::MissingFont,
    ];

    pub fn spec(self) -> RuleSpec {
        use DiagnosticCategory as C;
        use DiagnosticChecker::Ai;
        use DiagnosticChecker::Rule;
        use DiagnosticSeverity::Error;
        use DiagnosticSeverity::Info;
        use DiagnosticSeverity::Warning;
        let (id, category, checker, severity, fixable) = match self {
            Self::TextOverflow => ("layout.text_overflow", C::Layout, Rule, Error, true),
            Self::OffSlide => ("layout.off_slide", C::Layout, Rule, Error, true),
            Self::SafeArea => ("layout.safe_area", C::Layout, Rule, Warning, true),
            Self::Overlap => ("layout.overlap", C::Layout, Rule, Warning, false),
            Self::NearAlignment => ("layout.near_alignment", C::Layout, Rule, Info, true),
            Self::TitleDrift => ("layout.title_drift", C::Layout, Rule, Warning, true),
            Self::EmptyPlaceholder => ("layout.empty_placeholder", C::Layout, Rule, Warning, true),
            Self::TooDense => ("layout.too_dense", C::Layout, Ai, Info, false),
            Self::WeakHierarchy => ("layout.weak_hierarchy", C::Layout, Ai, Info, false),
            Self::FontCount => ("consistency.font_count", C::Consistency, Rule, Warning, true),
            Self::OffThemeColor => ("consistency.off_theme_color", C::Consistency, Rule, Info, true),
            Self::TitleSize => ("consistency.title_size", C::Consistency, Rule, Warning, true),
            Self::BulletMix => ("consistency.bullet_mix", C::Consistency, Rule, Info, true),
            Self::FooterMissing => ("consistency.footer_missing", C::Consistency, Rule, Warning, true),
            Self::SlideNumberGap => ("consistency.slide_number_gap", C::Consistency, Rule, Warning, true),
            Self::Terms => ("consistency.terms", C::Consistency, Ai, Info, true),
            Self::Units => ("consistency.units", C::Consistency, Ai, Warning, true),
            Self::Typo => ("content.typo", C::Content, Ai, Error, true),
            Self::Grammar => ("content.grammar", C::Content, Ai, Warning, true),
            Self::PunctuationWidth => ("content.punctuation_width", C::Content, Ai, Info, true),
            Self::CjkLatinSpacing => ("content.cjk_latin_spacing", C::Content, Ai, Info, true),
            Self::NumberMismatch => ("content.number_mismatch", C::Content, Ai, Error, false),
            Self::ClaimConflict => ("content.claim_conflict", C::Content, Ai, Warning, false),
            Self::LinkStale => ("data.link_stale", C::Data, Rule, Warning, true),
            Self::NoSource => ("data.no_source", C::Data, Ai, Info, false),
            Self::Contrast => ("a11y.contrast", C::Accessibility, Rule, Warning, true),
            Self::AltText => ("a11y.alt_text", C::Accessibility, Rule, Info, true),
            Self::SmallText => ("a11y.small_text", C::Accessibility, Rule, Warning, true),
            Self::ImageResolution => ("file.image_resolution", C::File, Rule, Warning, false),
            Self::ImageSize => ("file.image_size", C::File, Rule, Info, false),
            Self::MissingFont => ("file.missing_font", C::File, Rule, Warning, false),
        };
        RuleSpec { id, category, checker, severity, fixable }
    }

    pub fn id(self) -> &'static str {
        self.spec().id
    }

    pub fn from_id(id: &str) -> Option<Self> {
        Self::ALL.iter().copied().find(|rule| rule.id() == id)
    }
}

/// One problem a check found.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckDiagnostic {
    /// Unique within one check, for "fix this one".
    pub id: String,
    /// A `DiagnosticRule` id, or a scene pack's.
    pub rule: String,
    pub category: DiagnosticCategory,
    pub severity: DiagnosticSeverity,
    pub checker: DiagnosticChecker,
    pub slide_id: String,
    pub index: u32,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub shape_ids: Vec<String>,
    /// For text problems: where, with `expect`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub text: Option<DeckTextTarget>,
    /// In the user's words.
    pub message: String,
    /// Ops that fix it, to send to `deck/edit` as they are.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fix: Option<Vec<DeckOp>>,
    /// The fix in a few words: "统一为 24 磅".
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fix_label: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckCheckParams {
    pub session_id: String,
    pub deck_id: String,
    /// Empty: every slide.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub slides: Vec<SlidePick>,
    /// Rule ids or categories (`layout`); empty: every rule-engine rule.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub rules: Vec<String>,
    /// Also the model's checks (content, terms…): slower, and uses the network model.
    #[serde(default, skip_serializing_if = "is_false")]
    pub ai: bool,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckCheckResult {
    pub version: String,
    pub diagnostics: Vec<DeckDiagnostic>,
    /// The rules that ran.
    pub rules: Vec<String>,
}

// ---- layout candidates (§5.2.2, §6.7) ----

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ContentBlockKind {
    Bullets,
    Paragraph,
    /// A key figure (KPI).
    Number,
    Image,
    Table,
    Chart,
    Quote,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct ContentItem {
    pub text: String,
    #[serde(default)]
    pub level: u8,
    /// `number` blocks: the label under the figure ("同比增长").
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub label: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct ContentBlock {
    pub kind: ContentBlockKind,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub items: Vec<ContentItem>,
    /// The visual lead of the slide.
    #[serde(default, skip_serializing_if = "is_false")]
    pub emphasis: bool,
    /// The shapes it came from; `image`, `table` and `chart` blocks move that shape.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub shape_ids: Vec<String>,
}

/// What a slide says, without how it looks: the solver's input (§5.2.2).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct SlideContentTree {
    pub title: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub subtitle: Option<String>,
    pub blocks: Vec<ContentBlock>,
    /// The one-line conclusion.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub takeaway: Option<String>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum CandidateSource {
    /// A layout of the deck's own masters, its placeholders filled.
    Template,
    /// A diagram template (cards, timeline, process…) drawn in the content area.
    Structure,
    /// A table's numbers as a chart.
    Data,
}

/// How the solver scored a candidate, each 0–1.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct LayoutScore {
    pub fill: f64,
    pub alignment: f64,
    pub hierarchy: f64,
    pub focus: f64,
    pub consistency: f64,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct LayoutCandidate {
    pub id: String,
    /// The solver template (`cards.3`, `timeline.h`, `layout:twoObj`).
    pub template: String,
    pub source: CandidateSource,
    /// In the user's words: "三卡片".
    pub label: String,
    /// 0–1, best first.
    pub score: f64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub score_parts: Option<LayoutScore>,
    /// The deck layout it uses.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub layout_id: Option<String>,
    /// Applies it: send to `deck/edit` as they are.
    pub ops: Vec<DeckOp>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub thumbnail_png_base64: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckLayoutsParams {
    pub session_id: String,
    pub deck_id: String,
    pub slide: SlideRef,
    /// 3 when left out, at most 6.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub count: Option<u32>,
    /// The content, when the caller already has it; otherwise the kernel analyses the slide.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub content: Option<SlideContentTree>,
    /// Only these solver templates or families (`cards`, `timeline`).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub templates: Vec<String>,
    #[serde(default = "yes")]
    pub thumbnails: bool,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckLayoutsResult {
    pub version: String,
    pub content: SlideContentTree,
    pub candidates: Vec<LayoutCandidate>,
}

// ---- template profiles (§6.8) ----

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct TemplateLayout {
    pub layout_id: String,
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub layout_type: Option<String>,
    pub tag: LayoutTag,
    pub master_id: String,
    pub placeholders: Vec<PlaceholderInfo>,
    /// The area free for content: the body placeholders' union, or the safe area.
    pub content_area: DeckRect,
    /// Shapes the layout draws besides placeholders (logos, bars).
    #[serde(default)]
    pub decorations: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub thumbnail_png_base64: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct TemplateMaster {
    pub master_id: String,
    pub name: String,
    pub layout_ids: Vec<String>,
}

/// Which footer parts the template shows, and where.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct FooterRule {
    pub slide_number: bool,
    pub footer: bool,
    pub date: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub footer_text: Option<String>,
    pub hide_on_title: bool,
}

/// What generating into a template needs to know of it (§5.2.4, §6.8).
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct TemplateProfile {
    /// For "my templates" in `HER_HOME/templates/`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub template_id: Option<String>,
    pub path: String,
    pub name: String,
    pub size: SlideSize,
    pub theme: ThemeInfo,
    pub masters: Vec<TemplateMaster>,
    pub layouts: Vec<TemplateLayout>,
    pub footer: FooterRule,
    /// Inside the slide's edges, where nothing should go.
    pub safe_area: DeckRect,
    /// Title, subtitle, body and caption sizes the template uses, largest first.
    pub type_scale_pt: Vec<f64>,
}

/// `deck/template_profile`: of an open deck, or of a template file.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckTemplateProfileParams {
    pub session_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub deck_id: Option<String>,
    /// A .pptx or .potx file, instead of `deck_id`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub thumbnails: bool,
}

// ---- data links ----

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckLinksParams {
    pub session_id: String,
    pub deck_id: String,
    /// Read the sources now to set each link's status.
    #[serde(default, skip_serializing_if = "is_false")]
    pub check: bool,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckLinksResult {
    pub links: Vec<DataLink>,
    /// With `check`: the ops that bring stale links up to date, for one `link_refresh` proposal.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub refresh: Vec<DeckOp>,
}

// ---- comparing (§5.2.6) ----

/// One side of a comparison: an open deck, or a file.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckDiffSide {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub deck_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DeckDiffParams {
    pub session_id: String,
    pub left: DeckDiffSide,
    pub right: DeckDiffSide,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DiffSlide {
    pub slide_id: String,
    pub index: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum SlidePairStatus {
    Same,
    Changed,
    /// Only on the right.
    Inserted,
    /// Only on the left.
    Deleted,
    /// Same content, another place.
    Moved,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum DiffChangeKind {
    Text,
    /// Text whose numbers changed: listed apart, they matter most.
    Number,
    ShapeAdded,
    ShapeRemoved,
    ShapeMoved,
    Style,
    Layout,
    Notes,
    ChartData,
    Table,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DiffChange {
    pub kind: DiffChangeKind,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub left_shape_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub right_shape_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub before: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub after: Option<String>,
}

/// Slides matched across two decks by title and content, not position.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct SlidePair {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub left: Option<DiffSlide>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub right: Option<DiffSlide>,
    pub status: SlidePairStatus,
    /// 0–1.
    pub similarity: f64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub changes: Vec<DiffChange>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct DeckDiff {
    pub left_path: String,
    pub right_path: String,
    /// In the right deck's order, deleted slides where they were.
    pub pairs: Vec<SlidePair>,
}

// ---- instant AI actions (§5.2.1, §5.2.9, §6.6) ----

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum AiActionKind {
    Rewrite,
    Shorten,
    Expand,
    Bullets,
    Translate,
    /// 美化: the slide's look, content unchanged.
    Beautify,
    /// 对齐整理.
    Align,
    ToDiagram,
    ToChart,
    /// ⌘K: `instruction` in natural language.
    Command,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum RewriteTone {
    Formal,
    Casual,
    Persuasive,
    Concise,
    ConclusionFirst,
}

/// `ai/action`. Answers when the proposal is ready; `ai/action_progress` goes out meanwhile, and
/// `$/cancel_request` stops it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct AiActionParams {
    pub session_id: String,
    pub deck_id: String,
    /// Chosen by the client, to match the progress notifications.
    pub action_id: String,
    pub action: AiActionKind,
    pub selection: DeckSelection,
    /// The user's own words: ⌘K, a custom rewrite, a saved instruction.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub instruction: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tone: Option<RewriteTone>,
    /// `translate`: BCP 47 (`en`, `ja`, `zh-Hans`).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub language: Option<String>,
    /// "再来一版": supersedes this proposal.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub replaces: Option<String>,
    pub version: String,
}

/// An action too large for an instant answer, handed to the conversation (§5.2.9).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct AiHandoff {
    pub reason: String,
    /// The message to start the conversation turn with, context included.
    pub prompt: String,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct AiActionResult {
    pub action_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub proposal_id: Option<String>,
    pub version: String,
    /// One line for the preview box.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub handoff: Option<AiHandoff>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub failure: Option<DeckEditFailure>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum AiActionStage {
    Reading,
    Writing,
    LayingOut,
    Checking,
}

/// `ai/action_progress`: to the connection that asked.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct AiActionProgress {
    pub session_id: String,
    pub action_id: String,
    pub stage: AiActionStage,
    /// Text the model wrote since the last notice, for the preview box to stream.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub text_delta: Option<String>,
    /// The story the streamed text will replace.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub target: Option<DeckStoryTarget>,
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::Value;
    use serde_json::json;

    /// One sample per op, as an agent would write it.
    fn samples() -> Vec<Value> {
        let shape = json!({"slide_id": "s1", "shape_id": "sh2"});
        let text = json!({"slide_id": "s1", "shape_id": "sh2", "start": 0, "end": 4, "expect": "营业收入"});
        let rect = json!({"x": 40, "y": 120, "width": 400, "height": 200});
        let para = json!({"runs": [{"text": "收入增长 ", "style": {"bold": true}}, {"text": "18%"}],
                          "level": 0, "bullet": {"kind": "char", "char": "•"}, "like": "p7"});
        vec![
            json!({"op": "insert_text", "target": text, "at": "end", "text": "（同比）"}),
            json!({"op": "replace_text", "target": {"slide_id": "s1", "shape_id": "sh2", "find": "营收"}, "text": "营业收入"}),
            json!({"op": "delete_text", "target": text}),
            json!({"op": "format_text", "target": text, "style": {"size_pt": 24, "color": {"scheme": "accent1", "lum_mod": 0.75}, "highlight": null}}),
            json!({"op": "clear_text_format", "target": text}),
            json!({"op": "set_paragraphs", "target": {"slide_id": "s1", "shape_id": "sh2"}, "paragraphs": [para], "expect": "旧文字"}),
            json!({"op": "set_paragraph_props", "target": text, "props": {"align": "center", "line_spacing": {"multiple": 1.2}, "space_after_pt": 6}}),
            json!({"op": "set_text_body", "shape": shape, "direction": "east_asian_vertical", "anchor": "middle", "autofit": "shrink", "insets_pt": {"left": 7.2, "top": 3.6, "right": 7.2, "bottom": 3.6}}),
            json!({"op": "set_hyperlink", "text": text, "link": {"kind": "url", "url": "https://example.com"}}),
            json!({"op": "set_notes", "slide": {"slide_id": "s1"}, "text": "先讲结论"}),
            json!({"op": "add_shape", "slide": {"slide_id": "s1"}, "ref": "card1", "preset": "roundRect", "rect": rect, "fill": {"kind": "solid", "color": "#1F4E79"}, "line": {"visible": false}, "paragraphs": [para]}),
            json!({"op": "add_text_box", "slide": {"slide_id": "s1"}, "rect": rect, "paragraphs": [para]}),
            json!({"op": "add_picture", "slide": {"slide_id": "s1"}, "source": {"kind": "path", "path": "logo.png"}, "alt_text": "公司标志"}),
            json!({"op": "add_table", "slide": {"slide_id": "s1"}, "rect": rect, "rows": 3, "cols": 2, "cells": [["项目", "金额"]], "flags": {"header_row": true}}),
            json!({"op": "add_chart", "slide": {"slide_id": "s1"}, "rect": rect, "chart_type": "column", "data": {"categories": ["Q1", "Q2"], "series": [{"name": "收入", "values": [1.0, null]}]}}),
            json!({"op": "add_connector", "slide": {"slide_id": "s1"}, "kind": "elbow", "start": {"kind": "shape", "shape_id": "@card1", "site": 3}, "end": {"kind": "point", "x": 500, "y": 200}}),
            json!({"op": "duplicate_shapes", "shapes": [shape], "offset": {"x": 10, "y": 10}, "refs": ["copy1"]}),
            json!({"op": "delete_shapes", "shapes": [shape]}),
            json!({"op": "transform", "shape": shape, "rect": rect, "rotation_deg": 90, "expect_rect": rect}),
            json!({"op": "set_fill", "shape": shape, "fill": {"kind": "gradient", "angle_deg": 90, "stops": [{"position": 0, "color": "#FFFFFF"}, {"position": 1, "color": {"scheme": "accent2"}}]}}),
            json!({"op": "set_line", "shape": shape, "line": {"color": "#333333", "width_pt": 1.5, "dash": "dash", "tail": "triangle"}}),
            json!({"op": "set_effects", "shape": shape, "shadow": null}),
            json!({"op": "set_geometry", "shape": shape, "preset": "roundRect", "adjust": {"adj": 16667}}),
            json!({"op": "set_shape_props", "shape": shape, "name": "标题卡片", "hidden": false}),
            json!({"op": "group", "shapes": [shape, {"slide_id": "s1", "shape_id": "sh3"}], "ref": "g1"}),
            json!({"op": "ungroup", "shape": shape}),
            json!({"op": "z_order", "shape": shape, "to": "front"}),
            json!({"op": "fill_placeholder", "slide": {"slide_id": "@new"}, "placeholder": {"type": "title"}, "paragraphs": [para]}),
            json!({"op": "reset_slide", "slide": {"slide_id": "s1"}}),
            json!({"op": "table_edit", "shape": shape, "edit": {"kind": "merge", "range": {"first_row": 0, "first_col": 0, "last_row": 0, "last_col": 1}}}),
            json!({"op": "chart_data", "shape": shape, "data": {"categories": ["2024"], "series": [{"name": "利润", "values": [12.5]}], "number_format": "#,##0.0"}}),
            json!({"op": "chart_style", "shape": shape, "style": {"title": null, "legend": "bottom", "data_labels": {"value": true}}}),
            json!({"op": "insert_slide", "position": {"after": "s1"}, "ref": "new", "layout": {"layout_id": "ppt/slideLayouts/slideLayout2.xml"}}),
            json!({"op": "duplicate_slide", "slide": {"slide_id": "s1"}}),
            json!({"op": "delete_slide", "slide": {"slide_id": "s3"}}),
            json!({"op": "move_slide", "slide": {"slide_id": "s3"}, "position": "start"}),
            json!({"op": "set_layout", "slide": {"slide_id": "s1"}, "layout": {"layout_id": "ppt/slideLayouts/slideLayout4.xml"}, "mapping": [{"shape_id": "sh2", "idx": 2}]}),
            json!({"op": "set_background", "slide": {"slide_id": "s1"}, "fill": null}),
            json!({"op": "set_slide_hidden", "slide": {"slide_id": "s1"}, "hidden": true}),
            json!({"op": "set_transition", "slide": {"slide_id": "s1"}, "kind": "push", "direction": "left", "duration_ms": 500}),
            json!({"op": "set_animation", "shape": shape, "class": "entrance", "animation": {"effect": "fade", "trigger": "after_previous", "duration_ms": 400}}),
            json!({"op": "set_theme_colors", "colors": {"accent1": "#C00000"}}),
            json!({"op": "set_theme_fonts", "fonts": {"major_ea": "思源黑体", "minor_ea": "思源宋体"}}),
            json!({"op": "set_slide_size", "width_pt": 960, "height_pt": 540, "resize": "fit"}),
            json!({"op": "set_sections", "sections": [{"name": "背景", "first_slide_id": "s1"}]}),
            json!({"op": "set_header_footer", "slide_number": true, "footer": "内部资料", "date": {"kind": "hidden"}, "hide_on_title": true}),
            json!({"op": "paste", "at": {"kind": "slide", "slide_id": "s2"}, "clip": "AAAA", "mode": "use_destination_theme"}),
            json!({"op": "set_data_link", "slide": {"slide_id": "s1"}, "target": {"kind": "chart", "shape_id": "sh5"}, "source": {"path": "模型.xlsx", "sheet": "利润表", "range": "B3:F9"}}),
            json!({"op": "remove_data_link", "link_id": "l1"}),
        ]
    }

    #[test]
    fn every_op_has_a_sample_that_round_trips() {
        let samples = samples();
        let mut seen = Vec::new();
        for sample in &samples {
            let op: DeckOp = serde_json::from_value(sample.clone())
                .unwrap_or_else(|e| panic!("{sample}: {e}"));
            let back = serde_json::to_value(&op).unwrap();
            let again: DeckOp = serde_json::from_value(back.clone()).unwrap();
            assert_eq!(op, again, "{sample}");
            seen.push(back["op"].as_str().unwrap().to_owned());
        }
        assert_eq!(seen, DECK_OPS, "DECK_OPS lists every op, in sample order");
    }

    #[test]
    fn nulls_clear_and_absent_fields_stay() {
        let set: RunStyle = serde_json::from_value(json!({"highlight": "#FFFF00"})).unwrap();
        assert_eq!(set.highlight, Some(Some(DeckColor::Rgb("#FFFF00".into()))));
        let cleared: RunStyle = serde_json::from_value(json!({"highlight": null})).unwrap();
        assert_eq!(cleared.highlight, Some(None));
        let untouched: RunStyle = serde_json::from_value(json!({"bold": true})).unwrap();
        assert_eq!(untouched.highlight, None);
        assert_eq!(serde_json::to_value(&cleared).unwrap(), json!({"highlight": null}));
        assert_eq!(serde_json::to_value(&untouched).unwrap(), json!({"bold": true}));
    }

    #[test]
    fn inputs_refuse_unknown_fields() {
        let typo = json!({"op": "format_text",
            "target": {"slide_id": "s1", "shape_id": "sh2"}, "style": {"size": 24}});
        assert!(serde_json::from_value::<DeckOp>(typo).is_err());
        let unknown = json!({"op": "make_it_pretty", "slide": {"slide_id": "s1"}});
        assert!(serde_json::from_value::<DeckOp>(unknown).is_err());
    }

    #[test]
    fn colors_and_positions_read_as_written() {
        let rgb: DeckColor = serde_json::from_value(json!("#1F4E79")).unwrap();
        assert_eq!(rgb, DeckColor::Rgb("#1F4E79".into()));
        let theme: DeckColor = serde_json::from_value(json!({"scheme": "fol_hlink"})).unwrap();
        assert!(matches!(theme, DeckColor::Theme(ThemeColorRef { scheme: SchemeColor::FolHlink, .. })));
        let picks: Vec<SlidePick> = serde_json::from_value(json!([3, "s9"])).unwrap();
        assert_eq!(picks, [SlidePick::Index(3), SlidePick::Id("s9".into())]);
        for (value, position) in [
            (json!("end"), SlidePosition::End),
            (json!({"before": "s2"}), SlidePosition::Before("s2".into())),
        ] {
            assert_eq!(serde_json::from_value::<SlidePosition>(value.clone()).unwrap(), position);
            assert_eq!(serde_json::to_value(&position).unwrap(), value);
        }
    }

    #[test]
    fn rule_ids_are_unique_and_match_serde() {
        let mut ids: Vec<&str> = DiagnosticRule::ALL.iter().map(|r| r.id()).collect();
        for rule in DiagnosticRule::ALL {
            assert_eq!(serde_json::to_value(rule).unwrap(), json!(rule.id()));
            assert_eq!(DiagnosticRule::from_id(rule.id()), Some(*rule));
            let category = serde_json::to_value(rule.spec().category).unwrap();
            let prefix = rule.id().split('.').next().unwrap();
            let expected = if prefix == "a11y" { "accessibility" } else { prefix };
            assert_eq!(category, json!(expected), "{}", rule.id());
        }
        let count = ids.len();
        ids.sort();
        ids.dedup();
        assert_eq!(ids.len(), count);
    }

    #[test]
    fn edit_results_round_trip() {
        let failure = json!({
            "code": "content_mismatch", "op_index": 2, "message": "目标文字已变化",
            "version": "00ab-7", "actual": "营收"
        });
        let result = json!({
            "version": "00ab-7", "proposal_id": "p3", "refs": {"card1": "sh41"},
            "slides": [{"slide_id": "s1", "index": 1, "kind": "changed", "shape_ids": ["sh41"],
                        "summary": "新增 1 个对象"}],
            "failure": failure
        });
        let parsed: DeckEditResult = serde_json::from_value(result.clone()).unwrap();
        assert_eq!(serde_json::to_value(&parsed).unwrap(), result);
        let params = json!({"session_id": "a", "deck_id": "d", "ops": [samples()[0]],
                            "version": "00ab-7"});
        let params: DeckEditParams = serde_json::from_value(params).unwrap();
        assert_eq!(params.mode, DeckEditMode::Propose);
    }
}
