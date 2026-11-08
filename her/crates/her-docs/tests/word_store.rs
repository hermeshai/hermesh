//! WordStore end to end, with the worker on a thread and a stand-in for the desktop app's
//! serializer (word-design §4, §7). Paragraph ids are those of her-word's `report.docx`.

use std::future::Future;
use std::path::{Path, PathBuf};
use std::pin::Pin;
use std::sync::{Arc, Mutex};

use docx_edit::session::{EditSessionCore, Host};
use her_docs::deck::{DeckError, DeckLauncher, DeckStore, WordQuery};
use her_docs::word::{AUTHOR, DocxSerializer, SaveEngine, WordEvent, WordStore};
use her_word::{EditBatch, EditMode, ParagraphView, ReviewAction, Revision, WordOp};

const FIXTURE: &[u8] = include_bytes!("../../her-word/tests/fixtures/report.docx");

/// Writes the source as it is (so the result opens) and remembers what it was given.
#[derive(Default)]
struct Copier {
    calls: Mutex<Vec<(u64, u64)>>,
    absent: bool,
}

impl DocxSerializer for Copier {
    fn serialize<'a>(&'a self, source: &'a Path, state: &'a Path, metadata: &'a Path, out: &'a Path) -> Pin<Box<dyn Future<Output = Result<u64, String>> + Send + 'a>> {
        Box::pin(async move {
            if self.absent {
                return Err("保存 Word 文档需要 Hermesh 桌面端".into());
            }
            let sizes = (std::fs::metadata(state).unwrap().len(), std::fs::metadata(metadata).unwrap().len());
            self.calls.lock().unwrap().push(sizes);
            std::fs::copy(source, out).map_err(|e| e.to_string())
        })
    }
}

struct Setup {
    _dir: tempfile::TempDir,
    root: PathBuf,
    file: PathBuf,
}

fn setup() -> Setup {
    let dir = tempfile::tempdir().unwrap();
    let file = dir.path().join("report.docx");
    std::fs::write(&file, FIXTURE).unwrap();
    Setup { root: dir.path().to_owned(), file, _dir: dir }
}

fn store(s: &Setup, serializer: Arc<Copier>) -> (DeckStore, WordStore) {
    store_with(s, serializer, SaveEngine::Native)
}

fn store_with(s: &Setup, serializer: Arc<Copier>, engine: SaveEngine) -> (DeckStore, WordStore) {
    let decks = DeckStore::new(s.root.join("decks"), DeckLauncher::Thread);
    let words = WordStore::new(s.root.join("docx"), &decks, serializer).with_engine(engine);
    (decks, words)
}

/// The text of a body paragraph of the .docx at `path`.
fn saved_text(path: &Path, paragraph: &str) -> String {
    let word = her_word::Word::open(std::fs::read(path).unwrap()).unwrap();
    word.read(paragraph, 1).unwrap().iter().map(|p| p.text.clone()).collect()
}

fn replica(state: &[u8]) -> EditSessionCore {
    let host = Host::new(Arc::new(|| 0), Arc::new(|| 7), Arc::new(|| 0.0));
    let replica = EditSessionCore::new(424_242, host);
    replica.load(state).unwrap();
    replica
}

/// The person replaces `find` in paragraph `id` on the replica; the update.
fn type_over(replica: &EditSessionCore, id: &str, find: &str, text: &str) -> Vec<u8> {
    let sv = replica.encode_state_vector();
    let request = format!(
        r#"{{"expectVersion":"{}","steps":[{{"op":"replaceText","target":{{"kind":"search","text":"{find}","within":{{"kind":"paragraph","story":"body","paraId":"{id}"}},"view":"accepted"}},"text":"{text}"}}]}}"#,
        replica.version()
    );
    assert!(replica.apply_edits_json(&request).unwrap().contains(r#""ok":true"#));
    replica.encode_diff(&sv).unwrap()
}

fn replica_text(replica: &EditSessionCore, id: &str) -> String {
    let read = replica.read_paragraphs_json(&format!(r#"{{"paraIds":["{id}"],"view":"accepted"}}"#)).unwrap();
    let read: serde_json::Value = serde_json::from_str(&read).unwrap();
    read["paragraphs"][0]["text"].as_str().unwrap().to_owned()
}

async fn text(words: &WordStore, id: &str, paragraph: &str) -> String {
    let read: Vec<ParagraphView> = words.read(id, WordQuery::Read { from: paragraph.into(), count: 1 }).await.unwrap();
    read[0].text.clone()
}

fn batch(mode: EditMode, edits: Vec<WordOp>) -> EditBatch {
    EditBatch { mode, author: AUTHOR.into(), note: String::new(), edits }
}

fn replace(paragraph: &str, find: &str, with: &str) -> WordOp {
    WordOp::ReplaceText { paragraph: paragraph.into(), find: find.into(), replace: with.into() }
}

#[tokio::test(flavor = "multi_thread")]
async fn replicas_sync_both_ways_and_see_each_other() {
    let s = setup();
    let (_decks, words) = store(&s, Arc::default());
    let a = words.open(&s.file, "a", true).await.unwrap();
    let id = a.entry.deck_id.clone();
    let ra = replica(&a.state.unwrap());
    let (revision, _) = words.sync(&id, Some("a"), &type_over(&ra, "10000005", "成本", "费用"), None).await.unwrap();
    assert_eq!(revision, 1);
    assert!(words.get(&id).unwrap().info().dirty);
    assert_eq!(text(&words, &id, "10000005").await, "第二项：费用");
    // Another session joins with the edit in its state.
    let b = words.open(&s.file, "b", true).await.unwrap();
    assert_eq!(b.entry.deck_id, id);
    let rb = replica(&b.state.unwrap());
    assert_eq!(replica_text(&rb, "10000005"), "第二项：费用");
    // B's edit reaches A through the state vector.
    words.sync(&id, Some("b"), &type_over(&rb, "10000005", "第二项", "其二"), None).await.unwrap();
    let (_, missing) = words.sync(&id, Some("a"), &[], Some(&ra.encode_state_vector())).await.unwrap();
    ra.apply_update(&missing.unwrap()).unwrap();
    assert_eq!(replica_text(&ra, "10000005"), "其二：费用");
    // A malformed update changes nothing.
    assert!(matches!(words.sync(&id, Some("a"), &[0xff, 1, 2], None).await, Err(DeckError::Protocol(_))));
    assert_eq!(words.get(&id).unwrap().info().revision, 2);
    // A deck is not opened as a document.
    assert!(matches!(words.open(Path::new("/x/a.pptx"), "a", false).await, Err(DeckError::Refused(_))));
}

#[tokio::test(flavor = "multi_thread")]
async fn the_agents_edit_reaches_replicas_and_undo_takes_back_only_it() {
    let s = setup();
    let (_decks, words) = store(&s, Arc::default());
    let heard = Arc::new(Mutex::new(Vec::new()));
    let h = heard.clone();
    words.on_event(move |e| h.lock().unwrap().push(e.clone()));
    let a = words.open(&s.file, "a", true).await.unwrap();
    let id = a.entry.deck_id.clone();
    let human = replica(&a.state.unwrap());

    let edited = words.apply(&id, &batch(EditMode::Suggest, vec![replace("10000002", "百分之十", "一成")])).await.unwrap();
    assert_eq!(edited.changed.revision, 1);
    assert_eq!(edited.paragraphs, vec!["10000002".to_owned()]);
    assert!(!edited.revisions.is_empty());
    let update = heard
        .lock()
        .unwrap()
        .iter()
        .find_map(|e| match e {
            WordEvent::Update { session: None, update, .. } => Some(update.clone()),
            _ => None,
        })
        .expect("the agent's update");
    assert!(heard.lock().unwrap().iter().any(|e| matches!(e, WordEvent::Changed { author: Some(a), info } if a == AUTHOR && info.dirty)));
    human.apply_update(&update).unwrap();
    let pending: Vec<Revision> = words.read(&id, WordQuery::Revisions).await.unwrap();
    assert!(pending.iter().all(|r| r.author == AUTHOR) && !pending.is_empty());

    // The person edits elsewhere; undo takes back the agent's step only.
    words.sync(&id, Some("a"), &type_over(&human, "10000005", "成本", "费用"), None).await.unwrap();
    let after = words.undo(&id, edited.changed.revision).await.unwrap();
    assert_eq!(after, 3);
    assert!(text(&words, &id, "10000002").await.ends_with("百分之十。"));
    assert_eq!(text(&words, &id, "10000005").await, "第二项：费用");
    let none: Vec<Revision> = words.read(&id, WordQuery::Revisions).await.unwrap();
    assert!(none.is_empty());
    assert!(matches!(words.undo(&id, edited.changed.revision).await, Err(DeckError::NotUndoable(_))));

    // A batch with a bad step changes nothing.
    let bad = batch(EditMode::Direct, vec![replace("10000002", "百分之十", "x"), replace("10000002", "没有这段", "y")]);
    assert!(matches!(words.apply(&id, &bad).await, Err(DeckError::Refused(m)) if m.contains("edit 1")));
    assert_eq!(words.get(&id).unwrap().info().revision, 3);
}

#[tokio::test(flavor = "multi_thread")]
async fn review_settles_and_nothing_to_settle_logs_nothing() {
    let s = setup();
    let (_decks, words) = store(&s, Arc::default());
    let id = words.open(&s.file, "a", false).await.unwrap().entry.deck_id.clone();
    // Nothing pending: an empty change, no revision.
    assert_eq!(words.review(&id, ReviewAction::Accept, None, None).await.unwrap(), 0);
    let edited = words.apply(&id, &batch(EditMode::Suggest, vec![replace("10000002", "百分之十", "一成")])).await.unwrap();
    let revision = words.review(&id, ReviewAction::Accept, Some(edited.revisions.clone()), None).await.unwrap();
    assert_eq!(revision, 2);
    assert!(text(&words, &id, "10000002").await.ends_with("一成。"));
    let none: Vec<Revision> = words.read(&id, WordQuery::Revisions).await.unwrap();
    assert!(none.is_empty());
    // The settled edit is no longer the agent's latest step.
    assert!(matches!(words.undo(&id, edited.changed.revision).await, Err(DeckError::NotUndoable(_))));
    assert!(matches!(words.review(&id, ReviewAction::Reject, Some(vec![1]), None).await, Err(DeckError::Refused(_))));
}

#[tokio::test(flavor = "multi_thread")]
async fn a_worker_crash_or_restart_loses_nothing_acknowledged() {
    let s = setup();
    {
        let (_decks, words) = store(&s, Arc::default());
        let id = words.open(&s.file, "a", false).await.unwrap().entry.deck_id.clone();
        words.apply(&id, &batch(EditMode::Direct, vec![replace("10000002", "百分之十", "一成")])).await.unwrap();
        words.kill_worker().await;
        // Reopened in a new process from the log.
        assert!(text(&words, &id, "10000002").await.ends_with("一成。"));
        words.kill_worker().await;
    }
    // A new kernel: the unsaved edit comes back, dirty.
    let (_decks, words) = store(&s, Arc::default());
    let opened = words.open(&s.file, "a", false).await.unwrap();
    assert!(opened.recovered && opened.entry.info().dirty);
    assert!(text(&words, &opened.entry.deck_id, "10000002").await.ends_with("一成。"));
}

#[tokio::test(flavor = "multi_thread")]
async fn saves_through_the_serializer_with_the_guard_of_decks() {
    let s = setup();
    let copier = Arc::new(Copier::default());
    let (_decks, words) = store_with(&s, copier.clone(), SaveEngine::Desktop);
    let id = words.open(&s.file, "a", false).await.unwrap().entry.deck_id.clone();
    // Nothing unsaved: nothing written.
    words.save(&id, None, false).await.unwrap();
    assert!(copier.calls.lock().unwrap().is_empty());
    words.apply(&id, &batch(EditMode::Direct, vec![replace("10000002", "百分之十", "一成")])).await.unwrap();
    let info = words.save(&id, None, false).await.unwrap();
    assert!(!info.dirty);
    let calls = copier.calls.lock().unwrap().clone();
    assert!(calls.len() == 1 && calls[0].0 > 0 && calls[0].1 > 0, "{calls:?}");
    assert_eq!(info.hash, sha256_hex(&std::fs::read(&s.file).unwrap()));
    // The staged and state files are gone.
    let dir = words.get(&id).unwrap().dir.clone();
    let left: Vec<_> = std::fs::read_dir(&dir).unwrap().flatten().map(|e| e.file_name().into_string().unwrap()).collect();
    assert!(!left.iter().any(|n| n.starts_with("saving") || n.starts_with("state-") || n.starts_with("metadata-")), "{left:?}");

    // Changed on disk: not written over unless forced.
    words.apply(&id, &batch(EditMode::Direct, vec![replace("10000002", "一成", "两成")])).await.unwrap();
    std::fs::write(&s.file, b"someone else's").unwrap();
    assert!(matches!(words.save(&id, None, false).await, Err(DeckError::Conflict(_))));
    assert!(words.get(&id).unwrap().info().conflict);
    assert_eq!(std::fs::read(&s.file).unwrap(), b"someone else's");
    assert!(!words.save(&id, None, true).await.unwrap().dirty);
    // Save as another .docx only.
    assert!(matches!(words.save(&id, Some(&s.root.join("x.pdf")), false).await, Err(DeckError::Refused(_))));
    words.save(&id, Some(&s.root.join("copy.docx")), false).await.unwrap();
    assert!(s.root.join("copy.docx").is_file());
}

#[tokio::test(flavor = "multi_thread")]
async fn saving_with_the_desktop_only_needs_the_desktop_app_and_write_access() {
    let s = setup();
    let (_decks, words) = store_with(&s, Arc::new(Copier { absent: true, ..Copier::default() }), SaveEngine::Desktop);
    let id = words.open(&s.file, "a", false).await.unwrap().entry.deck_id.clone();
    words.apply(&id, &batch(EditMode::Direct, vec![replace("10000002", "百分之十", "一成")])).await.unwrap();
    assert!(matches!(words.save(&id, None, false).await, Err(DeckError::Host(m)) if m.contains("桌面端")));
    assert!(matches!(words.current_bytes(&id).await, Err(DeckError::Host(_))));
    assert_eq!(std::fs::read(&s.file).unwrap(), FIXTURE);
    assert!(words.get(&id).unwrap().info().dirty);
    // Reading and editing go on.
    assert!(text(&words, &id, "10000002").await.ends_with("一成。"));

    let ro = s.root.join("ro.docx");
    std::fs::write(&ro, FIXTURE).unwrap();
    let mut perms = std::fs::metadata(&ro).unwrap().permissions();
    perms.set_readonly(true);
    std::fs::set_permissions(&ro, perms).unwrap();
    let (_d, words) = store(&s, Arc::default());
    let entry = words.open(&ro, "a", false).await.unwrap().entry;
    assert!(!entry.writable);
    words.apply(&entry.deck_id, &batch(EditMode::Direct, vec![replace("10000002", "百分之十", "一成")])).await.unwrap();
    assert!(matches!(words.save(&entry.deck_id, None, false).await, Err(DeckError::Refused(_))));
}

#[tokio::test(flavor = "multi_thread")]
async fn saves_natively_without_the_desktop_app() {
    let s = setup();
    let copier = Arc::new(Copier { absent: true, ..Copier::default() });
    let (_decks, words) = store(&s, copier.clone());
    let id = words.open(&s.file, "a", false).await.unwrap().entry.deck_id.clone();
    words.apply(&id, &batch(EditMode::Direct, vec![replace("10000002", "百分之十", "一成")])).await.unwrap();
    // Drawing pages of the unsaved document, then saving it: no serializer involved.
    let peek = words.current_bytes(&id).await.unwrap();
    assert!(her_word::Word::open(peek).is_ok());
    let info = words.save(&id, None, false).await.unwrap();
    assert!(!info.dirty);
    assert!(copier.calls.lock().unwrap().is_empty());
    assert_eq!(info.hash, sha256_hex(&std::fs::read(&s.file).unwrap()));
    assert!(saved_text(&s.file, "10000002").ends_with("一成。"));
    let dir = words.get(&id).unwrap().dir.clone();
    let left: Vec<_> = std::fs::read_dir(&dir).unwrap().flatten().map(|e| e.file_name().into_string().unwrap()).collect();
    assert!(!left.iter().any(|n| n.starts_with("saving") || n.starts_with("peek-") || n.starts_with("state-")), "{left:?}");
    // A tracked change is saved as one: reopened, it is still pending.
    words.apply(&id, &batch(EditMode::Suggest, vec![replace("10000002", "一成", "两成")])).await.unwrap();
    words.save(&id, Some(&s.root.join("tracked.docx")), false).await.unwrap();
    let reopened = her_word::Word::open(std::fs::read(s.root.join("tracked.docx")).unwrap()).unwrap();
    assert!(!reopened.revisions().unwrap().is_empty());
    // The file left untouched on disk by the save-as.
    assert!(saved_text(&s.file, "10000002").ends_with("一成。"));
}

#[tokio::test(flavor = "multi_thread")]
async fn compare_keeps_the_desktops_bytes() {
    let s = setup();
    let copier = Arc::new(Copier::default());
    let (_decks, words) = store_with(&s, copier.clone(), SaveEngine::Compare);
    let id = words.open(&s.file, "a", false).await.unwrap().entry.deck_id.clone();
    words.apply(&id, &batch(EditMode::Direct, vec![replace("10000002", "百分之十", "一成")])).await.unwrap();
    words.save(&id, None, false).await.unwrap();
    // The stand-in writes the source as it is: that is what was saved, not the native bytes.
    assert_eq!(copier.calls.lock().unwrap().len(), 1);
    assert_eq!(std::fs::read(&s.file).unwrap(), FIXTURE);
    let dir = words.get(&id).unwrap().dir.clone();
    let left: Vec<_> = std::fs::read_dir(&dir).unwrap().flatten().map(|e| e.file_name().into_string().unwrap()).collect();
    assert!(!left.iter().any(|n| n.starts_with("native-")), "{left:?}");
}

/// The digest the store keeps (`DeckInfo.hash`).
fn sha256_hex(bytes: &[u8]) -> String {
    ring::digest::digest(&ring::digest::SHA256, bytes).as_ref().iter().map(|b| format!("{b:02x}")).collect()
}
