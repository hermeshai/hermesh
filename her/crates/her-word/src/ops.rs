//! What the agent asks of a document: one batch of edits, and how review settles revisions.

use schemars::JsonSchema;
use serde::{Deserialize, Serialize};

/// How a batch's text changes land.
#[derive(Clone, Copy, Debug, Default, Deserialize, Serialize, PartialEq, Eq, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum EditMode {
    /// As Word revisions (`w:ins`/`w:del`, formatting as `w:rPrChange`) by the batch's author.
    #[default]
    Suggest,
    /// Straight into the text.
    Direct,
}

/// Edits applied together or not at all.
#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct EditBatch {
    #[serde(default)]
    pub mode: EditMode,
    /// Author of the revisions and comments the batch makes.
    pub author: String,
    /// Why, for the caller's log; the document does not keep it.
    #[serde(default)]
    pub note: String,
    pub edits: Vec<WordOp>,
}

/// Which end of a paragraph `insert_text` writes at.
#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum Edge {
    Start,
    End,
}

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum RowPosition {
    Before,
    After,
}

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum Align {
    Left,
    Center,
    Right,
    Justify,
}

impl Align {
    /// The `w:jc` value.
    pub(crate) fn ooxml(self) -> &'static str {
        match self {
            Self::Left => "left",
            Self::Center => "center",
            Self::Right => "right",
            Self::Justify => "both",
        }
    }
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct NewParagraph {
    pub text: String,
    /// A paragraph style id of the document; without one, the anchor paragraph's style.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub style: Option<String>,
}

/// One edit. Paragraphs are addressed by their id (from `outline`, `read` or `find`); `find`
/// must occur exactly once in that paragraph's text as it reads now.
#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq, JsonSchema)]
#[serde(tag = "op", rename_all = "snake_case", deny_unknown_fields)]
pub enum WordOp {
    ReplaceText {
        paragraph: String,
        find: String,
        replace: String,
    },
    /// After `after` when given, otherwise at `at` (default the end).
    InsertText {
        paragraph: String,
        text: String,
        #[serde(default)]
        after: Option<String>,
        #[serde(default)]
        at: Option<Edge>,
    },
    DeleteText {
        paragraph: String,
        find: String,
    },
    /// The whole paragraph without `find`. `color` is hex (`FF0000`); `highlight` a Word
    /// highlight name or hex. `false` clears a flag.
    FormatText {
        paragraph: String,
        #[serde(default)]
        find: Option<String>,
        #[serde(default)]
        bold: Option<bool>,
        #[serde(default)]
        italic: Option<bool>,
        #[serde(default)]
        underline: Option<bool>,
        #[serde(default)]
        strike: Option<bool>,
        #[serde(default)]
        color: Option<String>,
        #[serde(default)]
        highlight: Option<String>,
    },
    /// Exactly one of `after` and `before`.
    InsertParagraphs {
        #[serde(default)]
        after: Option<String>,
        #[serde(default)]
        before: Option<String>,
        paragraphs: Vec<NewParagraph>,
    },
    DeleteParagraph {
        paragraph: String,
    },
    SetParagraphStyle {
        paragraph: String,
        style: String,
    },
    SetAlignment {
        paragraph: String,
        align: Align,
    },
    AddComment {
        paragraph: String,
        #[serde(default)]
        find: Option<String>,
        text: String,
    },
    ReplyComment {
        comment: String,
        text: String,
    },
    ResolveComment {
        comment: String,
    },
    /// One text per column of the row.
    InsertTableRow {
        row_of: String,
        position: RowPosition,
        cells: Vec<String>,
    },
    DeleteTableRow {
        row_of: String,
    },
    /// Replaces the text of the cell holding `cell_of`; the cell keeps one paragraph.
    SetCellText {
        cell_of: String,
        text: String,
    },
}

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ReviewAction {
    Accept,
    Reject,
}

/// What [`crate::Word::apply`] did.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Applied {
    /// Revisions the batch made, in the order `revisions()` lists them.
    pub revision_ids_created: Vec<String>,
    /// Paragraphs the batch edited or made, in batch order, each once.
    pub paragraphs_touched: Vec<String>,
    /// The Yrs update for replicas; empty when nothing changed.
    pub update: Vec<u8>,
}
