//! Spreadsheets for the document column (docs/desktop/spreadsheet-design.md §5).
//!
//! Bytes in, bytes out: no DocStore, no protocol. A write changes only the cells it edits and
//! what they necessarily touch (§5.3), and every write is checked by three independent views
//! before it is handed back (§5.4).

pub mod book;
pub mod calc;
pub mod chart;
pub mod cellref;
pub mod csv;
pub mod deps;
pub mod formula;
pub mod package;
pub mod patch;
pub mod scan;
pub mod structure;
pub mod styles;
pub mod tables;
pub mod verify;
pub mod xls;
pub mod xmltext;

pub use patch::Cached;
pub use patch::CellEdit;
pub use patch::NewValue;
pub use patch::Options;
pub use patch::Outcome;

#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// Doing it would risk the file: refused, with the reason (§5.3.3).
    #[error("{0}")]
    Unsupported(String),
    #[error("formula: {0}")]
    Formula(String),
    #[error("{0}")]
    NotFound(String),
    #[error("the file is damaged: {0}")]
    Corrupt(String),
    #[error("xml: {0}")]
    Xml(String),
    #[error("zip: {0}")]
    Zip(#[from] zip::result::ZipError),
    #[error("{0}")]
    Io(#[from] std::io::Error),
    /// The self-check found a write that changed what it should not: a bug here, never the file.
    #[error("the write failed its check: {0}")]
    SelfCheck(String),
    #[error("internal: {0}")]
    Internal(String),
}

impl From<quick_xml::Error> for Error {
    fn from(e: quick_xml::Error) -> Self {
        Error::Xml(e.to_string())
    }
}

/// Applies `edits` to the workbook in `input` and checks the result (§5.4): the bytes to write,
/// or why not.
pub fn write(input: &[u8], edits: &[CellEdit], opts: &Options) -> Result<Outcome, Error> {
    let out = patch::apply(input, edits, opts)?;
    let report = verify::verify(input, &out)?;
    if !report.ok() {
        return Err(Error::SelfCheck(report.problems.join("; ")));
    }
    Ok(out)
}
