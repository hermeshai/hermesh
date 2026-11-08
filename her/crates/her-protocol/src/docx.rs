//! Word documents held by the kernel (docs/desktop/word-design.md §4): the editor joins as a Yrs
//! replica and syncs through `docx/sync`, as it does a deck through `deck/sync`. Binary payloads
//! are base64.

use schemars::JsonSchema;
use serde::Deserialize;
use serde::Serialize;

fn is_false(value: &bool) -> bool {
    !*value
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DocxOpenParams {
    pub session_id: String,
    pub path: String,
    /// The caller joins as a Yrs replica (the editor): the result carries the source and state.
    #[serde(default)]
    pub replica: bool,
    /// Drop the kernel's unsaved edits of this file and read it anew.
    #[serde(default, skip_serializing_if = "is_false")]
    pub discard: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DocxOpenResult {
    /// From the file's canonical path: the same in every session.
    pub docx_id: String,
    pub path: String,
    /// The file as the kernel opened it (base64), for a replica: the editor's engine needs the
    /// source parts the state does not carry.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source: Option<String>,
    /// The whole Yrs state (base64), for a replica.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub state: Option<String>,
    /// Of the file as last read or written.
    pub hash: String,
    pub writable: bool,
    /// Unsaved edits, maybe brought back from an earlier run.
    pub dirty: bool,
    pub revision: u64,
    /// Font families the document names, with the host's fallbacks: what the editor loads.
    pub font_families: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DocxIdParams {
    pub session_id: String,
    pub docx_id: String,
}

/// A replica's update (merged and sent to the other replicas), and/or its state vector (the
/// result then carries what it lacks).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DocxSyncParams {
    pub session_id: String,
    pub docx_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub update: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub state_vector: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DocxSyncResult {
    pub revision: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub update: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DocxSaveParams {
    pub session_id: String,
    pub docx_id: String,
    /// Save as another .docx instead.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,
    /// Write over the file although it changed on disk.
    #[serde(default, skip_serializing_if = "is_false")]
    pub force: bool,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum DocxSaveStatus {
    Saved,
    /// The file changed on disk (or is gone, then `hash` is null): nothing was written.
    Conflict,
    /// Read only, or not a .docx, or the written document did not check: nothing was written.
    Refused,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DocxSaveResult {
    pub status: DocxSaveStatus,
    /// Saved: of the file written. Conflict: `changed`, or null when the file is gone.
    #[serde(default)]
    pub hash: Option<String>,
    #[serde(default)]
    pub message: Option<String>,
    pub revision: u64,
}

/// Takes back the agent's edit `edit` (the revision it made), when it is the agent's latest
/// step still in the document.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DocxUndoParams {
    pub session_id: String,
    pub docx_id: String,
    pub edit: u64,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DocxRevisionResult {
    /// The document's revision after it.
    pub revision: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum DocxReviewAction {
    Accept,
    Reject,
}

/// Accepts or rejects tracked changes, the person's choice in the document (word-design §5.2).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct DocxReviewParams {
    pub session_id: String,
    pub docx_id: String,
    pub action: DocxReviewAction,
    /// The revisions, by the numbers the editor knows them by (`scrollToChange`) and `docx_edit`
    /// gives; all pending ones when absent.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ids: Option<Vec<u64>>,
}

/// Server notification to the other replicas: the kernel's update or another replica's.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DocxSyncNotice {
    pub docx_id: String,
    pub update: String,
    pub revision: u64,
}

/// Server notification to connections that opened the document.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct DocxChangedParams {
    pub docx_id: String,
    pub revision: u64,
    pub dirty: bool,
    /// Who made the change: the agent's name for its own edits, else null.
    #[serde(default)]
    pub author: Option<String>,
    /// The file changed on disk under unsaved edits.
    #[serde(default, skip_serializing_if = "is_false")]
    pub conflict: bool,
}
