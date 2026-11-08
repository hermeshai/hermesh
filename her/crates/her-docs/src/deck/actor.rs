//! The worker's side: every open deck lives on a thread of its own (`Deck` is not `Send`, K3),
//! which also makes each deck's requests run strictly one after another. A Word document is an
//! entry of the same kind (word-design §2): a directory with `source.docx` holds a `Word`.

// The worker has no other log than its stderr, which the kernel keeps.
#![allow(clippy::print_stderr)]

use std::collections::HashMap;
use std::io::{BufRead, Write};
use std::path::{Path, PathBuf};
use std::sync::mpsc;
use std::sync::{Arc, Mutex};

use base64::Engine as _;
use base64::engine::general_purpose::STANDARD as B64;
use her_slide::Deck;
use serde_json::Value;

use her_slide::{ProposalEdit, ProposalRequest, SlideError};
use her_word::Word;

use super::journal::{self, Journal};
use super::wire::{Bytes, Changed, Edited, Envelope, Opened, Request, Saved, StripEntry, Summary, Synced, WordChecked, WordEdited, WordQuery};

type Reply = Box<dyn FnOnce(Result<Value, String>) + Send>;

struct Job {
    request: Request,
    reply: Reply,
}

/// Serves requests from `input` until it ends or an `exit` comes.
pub fn serve(input: impl BufRead, output: impl Write + Send + 'static) -> std::io::Result<()> {
    let output = Arc::new(Mutex::new(output));
    let mut decks: HashMap<String, mpsc::Sender<Job>> = HashMap::new();
    for line in input.lines() {
        let line = line?;
        if line.trim().is_empty() {
            continue;
        }
        let envelope: Envelope<Request> = match serde_json::from_str(&line) {
            Ok(e) => e,
            Err(e) => {
                // Without an id nobody waits for the reply; the line is only logged.
                eprintln!("deck-worker: bad request: {e}");
                continue;
            }
        };
        let id = envelope.id;
        let out = output.clone();
        let reply: Reply = Box::new(move |result| write_reply(&out, id, result));
        match envelope.body {
            Request::Exit => return Ok(()),
            Request::Open { deck, dir } => {
                if decks.contains_key(&deck) {
                    reply(Err(format!("deck {deck} is already open")));
                    continue;
                }
                let (tx, rx) = mpsc::channel();
                decks.insert(deck.clone(), tx);
                std::thread::Builder::new()
                    .name(format!("deck-{deck}"))
                    .spawn(move || run(dir, rx, reply))
                    .expect("spawn deck thread");
            }
            Request::Close { deck } => {
                // The thread ends once its queue is drained and the sender is gone.
                match decks.remove(&deck) {
                    Some(tx) => {
                        let _ = tx.send(Job { request: Request::Close { deck }, reply });
                    }
                    None => reply(Ok(Value::Null)),
                }
            }
            request => {
                let deck = request.deck().unwrap_or_default().to_owned();
                match decks.get(&deck) {
                    Some(tx) => {
                        if let Err(mpsc::SendError(job)) = tx.send(Job { request, reply }) {
                            decks.remove(&deck);
                            (job.reply)(Err(format!("deck {deck} stopped")));
                        }
                    }
                    None => reply(Err(format!("{NOT_OPEN}{deck}"))),
                }
            }
        }
    }
    Ok(())
}

/// Prefix of the error for a deck this process has not opened: the store opens it and retries.
pub const NOT_OPEN: &str = "not open: ";

fn write_reply(out: &Mutex<impl Write>, id: u64, result: Result<Value, String>) {
    let reply = match result {
        Ok(value) => serde_json::json!({ "id": id, "ok": value }),
        Err(e) => serde_json::json!({ "id": id, "err": e }),
    };
    let mut out = out.lock().unwrap_or_else(|e| e.into_inner());
    let _ = serde_json::to_writer(&mut *out, &reply);
    let _ = out.write_all(b"\n");
    let _ = out.flush();
}

/// The engine an entry holds.
enum Doc {
    Deck(Box<Deck>),
    Word(Box<Word>),
}

impl Doc {
    fn open(dir: &Path) -> Result<Self, String> {
        if let Ok(source) = std::fs::read(dir.join(journal::WORD_SOURCE)) {
            return Word::open(source).map(|w| Doc::Word(Box::new(w))).map_err(|e| e.to_string());
        }
        let source = std::fs::read(dir.join(journal::SOURCE)).map_err(|e| format!("source: {e}"))?;
        Deck::open(source).map(|d| Doc::Deck(Box::new(d))).map_err(|e| e.to_string())
    }

    fn engine(&self) -> &'static str {
        match self {
            Doc::Deck(_) => her_slide::ENGINE,
            Doc::Word(_) => her_word::ENGINE,
        }
    }

    fn apply_update(&self, update: &[u8]) -> Result<(), String> {
        match self {
            Doc::Deck(d) => d.apply_update(update).map_err(|e| e.to_string()),
            Doc::Word(w) => w.apply_update(update).map_err(|e| e.to_string()),
        }
    }

    fn encode_state(&self) -> Vec<u8> {
        match self {
            Doc::Deck(d) => d.encode_state(),
            Doc::Word(w) => w.encode_state(),
        }
    }

    fn encode_diff(&self, sv: &[u8]) -> Result<Vec<u8>, String> {
        match self {
            Doc::Deck(d) => d.encode_diff(sv).map_err(|e| e.to_string()),
            Doc::Word(w) => w.encode_diff(sv).map_err(|e| e.to_string()),
        }
    }

    fn undo(&self) -> Result<Option<Vec<u8>>, String> {
        match self {
            Doc::Deck(d) => d.undo().map_err(slide_error),
            Doc::Word(w) => w.undo().map_err(|e| e.to_string()),
        }
    }

    /// Under another engine, a replayed log must still give a document it can write (§7.2).
    fn check(&self) -> Result<(), String> {
        match self {
            Doc::Deck(d) => d.save().map(drop).map_err(|e| e.to_string()),
            // Only the desktop app writes .docx; replaying is the check here.
            Doc::Word(_) => Ok(()),
        }
    }

    fn slide_count(&self) -> usize {
        match self {
            Doc::Deck(d) => d.slide_count(),
            Doc::Word(_) => 0,
        }
    }

    fn deck(&self) -> Result<&Deck, String> {
        match self {
            Doc::Deck(d) => Ok(d),
            Doc::Word(_) => Err("not a presentation".into()),
        }
    }

    fn word(&self) -> Result<&Word, String> {
        match self {
            Doc::Word(w) => Ok(w),
            Doc::Deck(_) => Err("not a Word document".into()),
        }
    }
}

struct Held {
    deck: Doc,
    journal: Journal,
    revision: u64,
    /// The revisions the agent's edits made, oldest first, as far as the deck's undo history
    /// holds them: this run of the worker only (a replayed log brings back no history).
    edits: Vec<u64>,
}

/// Prefix of the error for an edit that cannot be taken back.
pub const NOT_UNDOABLE: &str = "not undoable: ";
/// Prefix of the error for edits the engine refused.
pub const REFUSED: &str = "edit refused: ";

fn run(dir: PathBuf, jobs: mpsc::Receiver<Job>, opened: Reply) {
    let mut held = match open(&dir) {
        Ok((held, reply)) => {
            opened(to_value(reply));
            held
        }
        Err(e) => {
            opened(Err(e));
            return;
        }
    };
    for job in jobs {
        let close = matches!(job.request, Request::Close { .. });
        let result = handle(&mut held, job.request);
        (job.reply)(result);
        if close {
            return;
        }
    }
}

fn open(dir: &Path) -> Result<(Held, Opened), String> {
    let (journal, recovered) = Journal::open(dir).map_err(|e| format!("log: {e}"))?;
    if recovered.dropped_bytes > 0 {
        eprintln!("deck-worker: {}: dropped {} bytes of a torn log tail", dir.display(), recovered.dropped_bytes);
    }
    let fresh = || Doc::open(dir);
    let mut deck = fresh()?;
    let engine = deck.engine();
    let mut set_aside = None;
    let mut revision = 0;
    if !recovered.is_empty() {
        let same_engine = recovered.engine.as_deref().is_none_or(|e| e == engine);
        // Replay; under another engine the result must also still save (§7.2).
        let replayed = replay(&deck, &recovered).and_then(|()| if same_engine { Ok(()) } else { deck.check() });
        match replayed {
            Ok(()) => revision = recovered.revision(),
            Err(e) => {
                let tag = recovered.engine.clone().unwrap_or_else(|| "unknown".into()).replace(|c: char| !c.is_ascii_alphanumeric(), "-");
                eprintln!("deck-worker: {}: log not replayed ({e}); kept as .{tag}", dir.display());
                drop(journal);
                Journal::set_aside(dir, &tag).map_err(|e| e.to_string())?;
                set_aside = Some(tag);
                deck = fresh()?;
                let (journal, _) = Journal::open(dir).map_err(|e| e.to_string())?;
                journal.stamp_engine(engine).map_err(|e| e.to_string())?;
                let opened = Opened { revision: 0, slide_count: deck.slide_count(), recovered: false, set_aside };
                return Ok((Held { deck, journal, revision: 0, edits: Vec::new() }, opened));
            }
        }
    }
    journal.stamp_engine(engine).map_err(|e| e.to_string())?;
    let opened = Opened { revision, slide_count: deck.slide_count(), recovered: revision > 0, set_aside };
    Ok((Held { deck, journal, revision, edits: Vec::new() }, opened))
}

fn replay(deck: &Doc, recovered: &journal::Recovered) -> Result<(), String> {
    if let Some((_, state)) = &recovered.snapshot {
        deck.apply_update(state).map_err(|e| format!("snapshot: {e}"))?;
    }
    for (revision, update) in &recovered.records {
        deck.apply_update(update).map_err(|e| format!("record {revision}: {e}"))?;
    }
    Ok(())
}

fn handle(held: &mut Held, request: Request) -> Result<Value, String> {
    match request {
        Request::Sync { update, .. } => {
            let update = B64.decode(update).map_err(|e| format!("update: {e}"))?;
            // A rejected update leaves the deck as it was (I5) and is not logged.
            held.deck.apply_update(&update).map_err(|e| format!("protocol: {e}"))?;
            record(held, &update)?;
            to_value(Synced { revision: held.revision })
        }
        Request::Apply { agent_id, note, edits, .. } => {
            let edits: Vec<ProposalEdit> = serde_json::from_value(edits).map_err(|e| format!("{REFUSED}edits: {e}"))?;
            let applied = held.deck.deck()?.apply(ProposalRequest { agent_id, note, edits }).map_err(slide_error)?;
            // Logged before the acknowledgement, as every change is.
            let changed = record(held, &applied.update)?;
            if !applied.update.is_empty() {
                held.edits.push(changed.revision);
            }
            let strip = applied
                .strip
                .into_iter()
                .map(|e| StripEntry {
                    status: serde_json::to_value(e.status).ok().and_then(|v| v.as_str().map(str::to_owned)).unwrap_or_default(),
                    index: e.preview_index.or(e.live_index).unwrap_or_default(),
                    slide_id: e.slide_id,
                })
                .collect();
            to_value(Edited { changed, applied: applied.applied, strip })
        }
        Request::Undo { edit, .. } => {
            match held.edits.last() {
                Some(&last) if last == edit => {}
                Some(_) if held.edits.contains(&edit) => return Err(format!("{NOT_UNDOABLE}a later edit of the agent is still in the deck; take that back first")),
                _ => return Err(format!("{NOT_UNDOABLE}it is no longer in the deck's history (already taken back, or the deck was opened again since)")),
            }
            let update = held.deck.undo()?.ok_or_else(|| format!("{NOT_UNDOABLE}nothing to take back"))?;
            held.edits.pop();
            to_value(record(held, &update)?)
        }
        Request::Check { .. } => to_value(held.deck.deck()?.check().map_err(slide_error)?),
        Request::Snapshot { .. } => to_value(held.deck.deck()?.snapshot().map_err(|e| e.to_string())?),
        Request::Diff { state_vector, .. } => {
            let sv = B64.decode(state_vector).map_err(|e| format!("state vector: {e}"))?;
            let diff = held.deck.encode_diff(&sv).map_err(|e| format!("protocol: {e}"))?;
            to_value(Bytes { data: B64.encode(diff) })
        }
        Request::State { .. } => to_value(Bytes { data: B64.encode(held.deck.encode_state()) }),
        Request::Save { to, .. } => {
            let bytes = held.deck.deck()?.save().map_err(|e| format!("save: {e}"))?;
            crate::atomic::write(&to, &bytes).map_err(|e| format!("write: {e}"))?;
            to_value(Saved { revision: held.revision, hash: super::sha256_hex(&bytes), size: bytes.len() as u64 })
        }
        Request::Summary { .. } => {
            let outline = held.deck.deck()?.outline().map_err(|e| e.to_string())?;
            let slides = outline
                .slides
                .iter()
                .map(|s| her_protocol::deck::SlideLine {
                    index: s.index as u32,
                    slide_id: s.slide_id.clone(),
                    layout_id: s.layout.clone().unwrap_or_default(),
                    title: s.title.clone(),
                    shape_count: s.objects.iter().filter(|o| o.depth == 0).count() as u32,
                    has_table: s.has("table"),
                    has_chart: s.has("chart"),
                    has_picture: s.has("picture"),
                    has_notes: !s.notes.trim().is_empty(),
                    hidden: s.hidden,
                    section_id: None,
                })
                .collect();
            to_value(Summary { width_pt: outline.width_pt, height_pt: outline.height_pt, slides })
        }
        Request::WordApply { batch, .. } => {
            let batch: her_word::EditBatch = serde_json::from_value(batch).map_err(|e| format!("{REFUSED}edits: {e}"))?;
            let applied = held.deck.word()?.apply(&batch).map_err(word_error)?;
            let changed = record(held, &applied.update)?;
            if !applied.update.is_empty() {
                held.edits.push(changed.revision);
            }
            let revisions = applied.revision_ids_created.iter().map(|id| her_word::revision_number(id)).collect();
            to_value(WordEdited { changed, revisions, paragraphs: applied.paragraphs_touched })
        }
        Request::WordReview { action, ids, .. } => {
            let word = held.deck.word()?;
            let ids = ids.map(|n| word.revision_ids(&n)).transpose().map_err(word_error)?;
            let update = word.review(action, ids.as_deref()).map_err(word_error)?;
            let changed = record(held, &update)?;
            // Settling is a step of the document's own undo history too: the agent's earlier
            // edits are no longer its latest.
            if !update.is_empty() {
                held.edits.push(changed.revision);
            }
            to_value(changed)
        }
        Request::WordRead { query, .. } => {
            let word = held.deck.word()?;
            match query {
                WordQuery::Outline => read(word.outline()),
                WordQuery::Read { from, count } => read(word.read(&from, count)),
                WordQuery::Find { query, limit } => read(word.find(&query, limit)),
                WordQuery::Comments => read(word.comments()),
                WordQuery::Revisions => read(word.revisions()),
            }
        }
        Request::WordState { state, metadata, .. } => {
            let word = held.deck.word()?;
            let peer = word.peer_metadata().map_err(|e| e.to_string())?;
            crate::atomic::write(&state, &word.encode_state()).map_err(|e| format!("write: {e}"))?;
            crate::atomic::write(&metadata, &peer).map_err(|e| format!("write: {e}"))?;
            to_value(Synced { revision: held.revision })
        }
        Request::WordWrite { path, .. } => {
            let bytes = held.deck.word()?.save().map_err(|e| e.to_string())?;
            crate::atomic::write(&path, &bytes).map_err(|e| format!("write: {e}"))?;
            to_value(Synced { revision: held.revision })
        }
        Request::WordCheck { path, .. } => {
            let bytes = std::fs::read(&path).map_err(|e| format!("read: {e}"))?;
            let summary = held.deck.word()?.check_saved(&bytes).map_err(|e| format!("{REFUSED}the written document does not open: {e}"))?;
            to_value(WordChecked { paragraphs: summary.paragraphs, tables: summary.tables, pages: summary.pages, hash: super::sha256_hex(&bytes), size: bytes.len() as u64 })
        }
        Request::Close { .. } => Ok(Value::Null),
        Request::Open { .. } | Request::Exit => Err("unexpected request".into()),
    }
}

/// Logs a change the deck made, under the next revision; nothing for an empty update.
/// Logged and synced before the acknowledgement (§5.2-4).
fn record(held: &mut Held, update: &[u8]) -> Result<Changed, String> {
    if update.is_empty() {
        return Ok(Changed { revision: held.revision, update: String::new() });
    }
    held.revision += 1;
    held.journal.append(held.revision, update).map_err(|e| format!("log: {e}"))?;
    if held.journal.wants_compaction()
        && let Err(e) = held.journal.compact(held.revision, &held.deck.encode_state())
    {
        eprintln!("deck-worker: compaction failed, the log keeps growing: {e}");
    }
    Ok(Changed { revision: held.revision, update: B64.encode(update) })
}

fn read<T: serde::Serialize>(result: her_word::Result<T>) -> Result<Value, String> {
    to_value(result.map_err(word_error)?)
}

fn word_error(e: her_word::WordError) -> String {
    match e {
        her_word::WordError::Step { .. } | her_word::WordError::NoParagraph(_) | her_word::WordError::NoRevision(_) => format!("{REFUSED}{e}"),
        e => e.to_string(),
    }
}

fn slide_error(e: SlideError) -> String {
    match e {
        SlideError::Proposal { .. } => format!("{REFUSED}{e}"),
        e => e.to_string(),
    }
}

fn to_value(v: impl serde::Serialize) -> Result<Value, String> {
    serde_json::to_value(v).map_err(|e| e.to_string())
}
