//! What kind of document a file is, decided once (from its first bytes and its extension), and
//! what follows from that: the working copy's extension, the protocol's format and layout, and
//! what can be done with it here.

use her_protocol::doc::Capability;
use her_protocol::doc::DocFormat;
use her_protocol::doc::DocLayout;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DocKind {
    Pdf,
    Xlsx,
    Xlsm,
    Csv,
    Tsv,
    /// A legacy binary workbook: read only.
    Xls,
}

/// Why a file is not opened.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Unopenable {
    /// Not a format her opens.
    Unsupported,
    /// An OOXML workbook wrapped in an encrypted compound file.
    EncryptedWorkbook,
}

const OLE: [u8; 4] = [0xD0, 0xCF, 0x11, 0xE0];

impl DocKind {
    /// The kind of a file, from (up to) its first kilobyte and its extension.
    pub fn detect(head: &[u8], ext: Option<&str>) -> Result<Self, Unopenable> {
        let ext = ext.map(str::to_ascii_lowercase);
        let ext = ext.as_deref();
        if head.windows(5).any(|w| w == b"%PDF-") {
            return Ok(Self::Pdf);
        }
        match ext {
            Some("xlsx") if head.starts_with(b"PK") => Ok(Self::Xlsx),
            Some("xlsm") if head.starts_with(b"PK") => Ok(Self::Xlsm),
            Some("xlsx" | "xlsm") if head.starts_with(&OLE) => Err(Unopenable::EncryptedWorkbook),
            Some("csv") => Ok(Self::Csv),
            Some("tsv") => Ok(Self::Tsv),
            Some("xls") if head.starts_with(&OLE) || head.starts_with(&[0x09]) => Ok(Self::Xls),
            _ => Err(Unopenable::Unsupported),
        }
    }

    /// The kind of a working copy, from its extension (it was named by `ext`).
    pub fn from_ext(ext: &str) -> Option<Self> {
        Some(match ext.to_ascii_lowercase().as_str() {
            "pdf" => Self::Pdf,
            "xlsx" => Self::Xlsx,
            "xlsm" => Self::Xlsm,
            "csv" => Self::Csv,
            "tsv" => Self::Tsv,
            "xls" => Self::Xls,
            _ => return None,
        })
    }

    /// The working copy's extension.
    pub fn ext(self) -> &'static str {
        match self {
            Self::Pdf => "pdf",
            Self::Xlsx => "xlsx",
            Self::Xlsm => "xlsm",
            Self::Csv => "csv",
            Self::Tsv => "tsv",
            Self::Xls => "xls",
        }
    }

    pub fn is_sheet(self) -> bool {
        self != Self::Pdf
    }

    /// Values only, no styles or calculation (spreadsheet-design §10).
    pub fn is_flat(self) -> bool {
        matches!(self, Self::Csv | Self::Tsv | Self::Xls)
    }

    /// Never written here.
    pub fn read_only(self) -> bool {
        self == Self::Xls
    }

    pub fn format(self) -> DocFormat {
        match self {
            Self::Pdf => DocFormat::Pdf,
            Self::Xlsx => DocFormat::Xlsx,
            Self::Xlsm => DocFormat::Xlsm,
            Self::Csv | Self::Tsv => DocFormat::Csv,
            Self::Xls => DocFormat::Xls,
        }
    }

    pub fn layout(self) -> DocLayout {
        if self.is_sheet() { DocLayout::Grid } else { DocLayout::Paged }
    }

    /// A workbook's edit capabilities (a PDF's depend on what is in it).
    pub fn sheet_capabilities(self) -> Vec<Capability> {
        let mut out = vec![Capability::View, Capability::Cells];
        match self {
            Self::Pdf | Self::Xls => {}
            Self::Csv | Self::Tsv => out.push(Capability::EditValues),
            Self::Xlsx | Self::Xlsm => out.extend([Capability::EditValues, Capability::EditFormulas]),
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn kinds_come_from_the_bytes_then_the_name() {
        assert_eq!(DocKind::detect(b"%PDF-1.7", Some("bin")), Ok(DocKind::Pdf));
        assert_eq!(DocKind::detect(b"PK\x03\x04", Some("XLSX")), Ok(DocKind::Xlsx));
        assert_eq!(DocKind::detect(b"PK\x03\x04", Some("xlsm")), Ok(DocKind::Xlsm));
        assert_eq!(DocKind::detect(b"a,b\n", Some("csv")), Ok(DocKind::Csv));
        assert_eq!(DocKind::detect(b"a\tb\n", Some("tsv")), Ok(DocKind::Tsv));
        assert_eq!(DocKind::detect(&OLE, Some("xls")), Ok(DocKind::Xls));
        assert_eq!(DocKind::detect(&OLE, Some("xlsx")), Err(Unopenable::EncryptedWorkbook));
        assert_eq!(DocKind::detect(b"PK\x03\x04", Some("docx")), Err(Unopenable::Unsupported));
        assert_eq!(DocKind::detect(b"text", Some("xls")), Err(Unopenable::Unsupported));
        assert_eq!(DocKind::detect(b"PK", None), Err(Unopenable::Unsupported));
    }

    #[test]
    fn a_kind_round_trips_through_its_extension() {
        for k in [DocKind::Pdf, DocKind::Xlsx, DocKind::Xlsm, DocKind::Csv, DocKind::Tsv, DocKind::Xls] {
            assert_eq!(DocKind::from_ext(k.ext()), Some(k));
        }
        assert!(DocKind::Csv.sheet_capabilities().contains(&Capability::EditValues));
        assert!(!DocKind::Csv.sheet_capabilities().contains(&Capability::EditFormulas));
        assert!(!DocKind::Xls.sheet_capabilities().contains(&Capability::EditValues));
    }
}
