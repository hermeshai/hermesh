//! The methods behind the desktop's file panel (docs/desktop/file-panel-design.md §9): a
//! session's folders listed a level at a time, its files read for showing and written back
//! when the base they were edited from is still what is on disk, and changes to the ones on
//! screen told as they happen. Paths resolve as the session's tools resolve them.

use schemars::JsonSchema;
use serde::Deserialize;
use serde::Serialize;
use std::path::PathBuf;

/// Parameters of `files/list`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct FilesListParams {
    pub session_id: String,
    /// A folder: absolute, or relative to the session's folder; empty for the folder itself.
    #[serde(default)]
    pub path: String,
}

/// One entry of a folder.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct FileEntry {
    pub name: String,
    /// A folder (or a link to one).
    pub dir: bool,
    /// A symbolic link.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub link: bool,
    /// In bytes; 0 for a folder.
    pub size: u64,
}

/// The result of `files/list`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct FilesListResult {
    /// The folder, absolute.
    pub path: PathBuf,
    /// Folders first, then files, each by name (numbers by value, case aside). `.git` and
    /// `.DS_Store` are left out.
    pub entries: Vec<FileEntry>,
    /// The folder had more entries than are given.
    pub truncated: bool,
}

/// Parameters of `files/read`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct FilesReadParams {
    pub session_id: String,
    /// Absolute, or relative to the session's folder.
    pub path: String,
}

/// What a file is, as far as showing it goes.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum FileKind {
    Text,
    /// PNG, JPEG, GIF or WebP, by its signature.
    Image,
    /// Neither text nor a known image.
    Binary,
    /// Text over 5 MB or an image over 10 MB: only its size is given.
    TooLarge,
}

/// An image's bytes.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct FileImage {
    pub mime_type: String,
    /// Base64.
    pub data: String,
}

/// The result of `files/read`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct FilesReadResult {
    /// The file, absolute.
    pub path: PathBuf,
    pub size: u64,
    pub kind: FileKind,
    /// SHA-256 of the file's bytes, in hex: the base `files/write` compares with. Not set for
    /// a file too large to read.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub hash: Option<String>,
    /// The text, decoded, without a byte-order mark; line ends as they are.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub text: Option<String>,
    /// How the text was decoded: `utf-8`, `utf-8-bom`, `utf-16le`, `utf-16be` or `gb18030`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub encoding: Option<String>,
    /// The text can be saved back with `files/write`: UTF-8, and not her's own data.
    pub writable: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub image: Option<FileImage>,
}

/// Parameters of `files/write`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct FilesWriteParams {
    pub session_id: String,
    /// Absolute, or relative to the session's folder.
    pub path: String,
    pub text: String,
    /// The hash of the version the text was edited from (`files/read`, or the last save): when
    /// the file on disk is no longer that version, nothing is written. Not set: written
    /// whatever is there (the person chose to).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub base_hash: Option<String>,
}

/// How a write went.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum FileWriteStatus {
    Saved,
    /// The file is no longer the base the text was edited from (or no longer there).
    Conflict,
    /// Not written: her's own data, or a file that is not UTF-8 text.
    Refused,
}

/// The result of `files/write`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct FilesWriteResult {
    pub status: FileWriteStatus,
    /// Saved: the new file's hash, the next base. Conflict: the hash of what is on disk now;
    /// not set when the file is gone.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub hash: Option<String>,
    /// Why it was refused.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub message: Option<String>,
}

/// Parameters of `files/watch`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct FilesWatchParams {
    pub session_id: String,
    /// Files and folders to hear about (a folder: when its entries change). Replaces what
    /// this connection watched for the session; empty stops.
    pub paths: Vec<String>,
}

/// Server notification `files/changed`: watched files and folders that changed, gathered for
/// a moment.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct FilesChangedParams {
    pub session_id: String,
    /// As they were watched, made absolute.
    pub paths: Vec<PathBuf>,
}
