//! Requests between DeckStore and `her deck-worker`: one JSON object a line each way, tagged
//! with an id so replies for different decks may come back in any order.

use std::path::PathBuf;

use serde::{Deserialize, Serialize};

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Envelope<T> {
    pub id: u64,
    #[serde(flatten)]
    pub body: T,
}

/// Binary payloads are base64.
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(tag = "op", rename_all = "snake_case")]
pub enum Request {
    /// Opens the deck in `dir` (its source, snapshot and log), on a thread of its own.
    Open { deck: String, dir: PathBuf },
    /// Merges a replica's update, logs it, and replies with the new revision.
    Sync { deck: String, update: String },
    /// What a replica with `state_vector` lacks.
    Diff { deck: String, state_vector: String },
    /// The whole state, for a replica joining.
    State { deck: String },
    /// Saves to `to` after the guard (her-slide `Deck::save`).
    Save { deck: String, to: PathBuf },
    /// Slide lines and size for `DeckSummary`, from the deck as it is now.
    Summary { deck: String },
    Close { deck: String },
    /// Makes the agent's edits, the engine's `ProposalEdit`s (JSON), at once; replies `Edited`.
    Apply { deck: String, agent_id: String, note: Option<String>, edits: serde_json::Value },
    /// Takes back the agent's edit that made revision `edit`, its latest one; replies `Changed`.
    Undo { deck: String, edit: u64 },
    /// The deterministic checks of the deck now.
    Check { deck: String },
    /// The engine's snapshot of the deck now (JSON): its ids are the ones edits address.
    Snapshot { deck: String },
    /// A Word document: makes the agent's batch (her-word `EditBatch`, JSON); replies `WordEdited`.
    WordApply { deck: String, batch: serde_json::Value },
    /// A Word document: accepts or rejects revisions, by their numbers (`her_word::revision_number`;
    /// all pending ones without `ids`); replies `Changed`.
    WordReview { deck: String, action: her_word::ReviewAction, ids: Option<Vec<u64>> },
    /// A Word document: reads it as it is now.
    WordRead { deck: String, query: WordQuery },
    /// A Word document: writes its Yrs state and peer metadata to the files given, for the
    /// serializer; replies `Synced` with the revision they hold.
    WordState { deck: String, state: PathBuf, metadata: PathBuf },
    /// A Word document: opens what the serializer wrote at `path`; replies `WordChecked`.
    WordCheck { deck: String, path: PathBuf },
    /// A Word document: writes it to `path` itself (her-word `Word::save`), whole or not at
    /// all; replies `Synced` with the revision written.
    WordWrite { deck: String, path: PathBuf },
    /// Ends the process: after shutdown, and in tests as a crash.
    Exit,
}

impl Request {
    pub fn deck(&self) -> Option<&str> {
        match self {
            Request::Open { deck, .. }
            | Request::Sync { deck, .. }
            | Request::Diff { deck, .. }
            | Request::State { deck }
            | Request::Save { deck, .. }
            | Request::Summary { deck }
            | Request::Close { deck }
            | Request::Apply { deck, .. }
            | Request::Undo { deck, .. }
            | Request::Check { deck }
            | Request::Snapshot { deck }
            | Request::WordApply { deck, .. }
            | Request::WordReview { deck, .. }
            | Request::WordRead { deck, .. }
            | Request::WordState { deck, .. }
            | Request::WordCheck { deck, .. }
            | Request::WordWrite { deck, .. } => Some(deck),
            Request::Exit => None,
        }
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Opened {
    pub revision: u64,
    pub slide_count: usize,
    /// Edits replayed from the log or snapshot.
    pub recovered: bool,
    /// A log written by another engine could not be replayed safely: set aside, not used.
    #[serde(default)]
    pub set_aside: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Synced {
    pub revision: u64,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Bytes {
    pub data: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Saved {
    pub revision: u64,
    pub hash: String,
    pub size: u64,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Summary {
    pub width_pt: f64,
    pub height_pt: f64,
    pub slides: Vec<her_protocol::deck::SlideLine>,
}

/// A change's reply: the revision after it, and the update it made (base64, empty for none:
/// then nothing was logged and the revision is as it was).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Changed {
    pub revision: u64,
    #[serde(default)]
    pub update: String,
}

/// What `Apply` did. The revision it made is the edit's id for `Undo`; with nothing changed
/// (`applied` false) there is nothing to take back.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Edited {
    #[serde(flatten)]
    pub changed: Changed,
    pub applied: bool,
    /// What it did to each slide, in the deck's new order.
    pub strip: Vec<StripEntry>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct StripEntry {
    pub slide_id: String,
    /// `unchanged`, `changed`, `inserted`, `deleted` or `moved`.
    pub status: String,
    /// From 0: where it is in the deck now, or for a deleted slide where it was.
    pub index: usize,
}

/// What `WordRead` reads; the reply is the matching her-word type as JSON.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "what", rename_all = "snake_case")]
pub enum WordQuery {
    Outline,
    Read { from: String, count: usize },
    Find { query: String, limit: usize },
    Comments,
    Revisions,
}

/// What `WordApply` did; as `Edited`, the revision it made is the edit's id for `Undo`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct WordEdited {
    #[serde(flatten)]
    pub changed: Changed,
    /// Revisions the batch made, by their numbers (`her_word::revision_number`): the ids the
    /// editor scrolls to and `WordReview` takes.
    pub revisions: Vec<u64>,
    /// Paragraphs it edited or made, in batch order.
    pub paragraphs: Vec<String>,
}

/// What `WordCheck` found in serialized bytes.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct WordChecked {
    pub paragraphs: usize,
    pub tables: usize,
    pub pages: u32,
    pub hash: String,
    pub size: u64,
}
