//! Local PPTX bytes for the browser's editable BetterOffice session. Layout and editing
//! belong to that session; the host owns fonts, conflict checks and atomic file writes.
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use std::path::PathBuf;

#[derive(Clone, Debug, Serialize, Deserialize, JsonSchema)]
pub struct PptxReadResult {
    pub path: PathBuf,
    /// Base64 PPTX bytes (at most 20 MiB before encoding).
    pub data: String,
    pub hash: String,
    pub writable: bool,
    /// Explicit font families in the package, plus the host's Latin and CJK fallbacks.
    pub font_families: Vec<String>,
}

#[derive(Clone, Debug, Serialize, Deserialize, JsonSchema)]
pub struct PptxWriteParams {
    pub session_id: String,
    pub path: String,
    /// Base64 of the edited PPTX.
    pub data: String,
    /// Required: never silently replace an external edit or recreate a deleted file.
    pub base_hash: String,
}

#[derive(Clone, Debug, Serialize, Deserialize, JsonSchema)]
pub struct PptxMetafileParams {
    pub session_id: String,
    /// Original EMF/WMF media bytes, base64. The source package is never changed.
    pub data: String,
}

#[derive(Clone, Debug, Serialize, Deserialize, JsonSchema)]
pub struct PptxMetafileResult {
    pub svg: String,
    /// Drawing operations the converter could not replay, if any.
    pub omissions: Vec<String>,
}

/// Optional correlation ID keeps concurrent opens and reopened tabs isolated.
#[derive(Clone, Debug, Serialize, Deserialize, JsonSchema)]
pub struct PptxReadParams {
    pub session_id: String,
    pub path: String,
    #[serde(default)]
    pub progress_id: Option<String>,
}

#[derive(Clone, Copy, Debug, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum PptxReadStage { Reading, Inspecting, Encoding }

/// Counts describe this stage only, not an estimated overall percentage.
#[derive(Clone, Debug, Serialize, Deserialize, JsonSchema)]
pub struct PptxReadProgress {
    pub session_id: String,
    pub progress_id: String,
    pub stage: PptxReadStage,
    pub completed: u64,
    pub total: u64,
}
