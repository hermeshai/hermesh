//! `Word::save`, the native save, on `tests/fixtures/report.docx` (paragraph ids as in
//! `word.rs`): after each kind of edit, what was written reopens with the edit in it, parts no
//! edit touches keep their source bytes, and the bytes equal those of a session rebuilt from the
//! state and peer metadata the way the desktop's serializer rebuilds it.

use std::collections::BTreeMap;
use std::io::{Cursor, Read};
use std::sync::Arc;

use docx_edit::session::{EditSessionCore, Host};
use docx_native_save::{OpenedSource, SaveOptions, save_docx};
use her_word::{
    Align, EditBatch, EditMode, NewParagraph, ReviewAction, RowPosition, Word, WordOp,
    package_difference,
};

const FIXTURE: &[u8] = include_bytes!("fixtures/report.docx");
const AUTHOR: &str = "Her";
const NOW: &str = "2026-11-07T08:00:00.000Z";

fn batch(mode: EditMode, edits: Vec<WordOp>) -> EditBatch {
    EditBatch {
        mode,
        author: AUTHOR.into(),
        note: String::new(),
        edits,
    }
}

fn replace(paragraph: &str, find: &str, with: &str) -> WordOp {
    WordOp::ReplaceText {
        paragraph: paragraph.into(),
        find: find.into(),
        replace: with.into(),
    }
}

fn options() -> SaveOptions {
    SaveOptions {
        now: Some(NOW.into()),
        ..SaveOptions::default()
    }
}

fn parts(bytes: &[u8]) -> BTreeMap<String, Vec<u8>> {
    let mut archive = zip::ZipArchive::new(Cursor::new(bytes)).unwrap();
    (0..archive.len())
        .map(|i| {
            let mut file = archive.by_index(i).unwrap();
            let mut data = Vec::new();
            file.read_to_end(&mut data).unwrap();
            (file.name().unwrap().into_owned(), data)
        })
        .collect()
}

/// The save of a session rebuilt as the desktop's serializer does: the source opened without
/// its stories, then the kernel's state and peer metadata.
fn rebuilt_save(word: &Word) -> Vec<u8> {
    let host = Host::new(Arc::new(|| 0), Arc::new(|| 5), Arc::new(|| 0.0));
    let session = EditSessionCore::new(99, host);
    let opened = session.open_docx(FIXTURE, false, None, None).unwrap();
    session
        .bootstrap_peer(&word.encode_state(), &word.peer_metadata().unwrap(), Some(FIXTURE.to_vec()))
        .unwrap();
    let source = OpenedSource::after_open(FIXTURE.to_vec(), &opened).unwrap();
    save_docx(&session, &source, &options()).unwrap().bytes
}

/// Saves `word`, checks the rebuilt session writes the same bytes and that the parts outside
/// `touched` are the source's, and returns the reopened document.
fn saved(word: &Word, touched: &[&str]) -> Word {
    let bytes = word.save_with(&options()).unwrap();
    assert_eq!(package_difference(&bytes, &rebuilt_save(word)), None);
    word.check_saved(&bytes).unwrap();
    let (source, written) = (parts(FIXTURE), parts(&bytes));
    for (name, data) in &source {
        let ours = written.get(name).unwrap_or_else(|| panic!("{name} was dropped"));
        let edited = touched.contains(&name.as_str()) || name == "docProps/core.xml";
        assert!(edited || ours == data, "{name} changed");
    }
    Word::open(bytes).unwrap()
}

fn text(word: &Word, id: &str) -> String {
    word.read(id, 1).unwrap().remove(0).text
}

#[test]
fn an_unedited_document_saves_as_its_source() {
    let word = Word::open(FIXTURE.to_vec()).unwrap();
    let reopened = saved(&word, &[]);
    assert_eq!(reopened.outline().unwrap().len(), word.outline().unwrap().len());
    assert_eq!(text(&reopened, "10000002"), text(&word, "10000002"));
}

#[test]
fn direct_text_and_paragraph_edits() {
    let word = Word::open(FIXTURE.to_vec()).unwrap();
    word.apply(&batch(
        EditMode::Direct,
        vec![
            replace("10000002", "百分之十", "一成"),
            WordOp::InsertParagraphs {
                after: Some("10000002".into()),
                before: None,
                paragraphs: vec![NewParagraph {
                    text: "新增的一段。".into(),
                    style: None,
                }],
            },
            WordOp::SetParagraphStyle {
                paragraph: "10000003".into(),
                style: "Heading1".into(),
            },
            WordOp::SetAlignment {
                paragraph: "1000000A".into(),
                align: Align::Center,
            },
        ],
    ))
    .unwrap();
    let reopened = saved(&word, &["word/document.xml"]);
    assert!(text(&reopened, "10000002").contains("一成"));
    let outline = reopened.outline().unwrap();
    assert!(outline.iter().any(|e| e.summary.contains("新增的一段")));
    assert_eq!(outline.len(), word.outline().unwrap().len());
    let heading = reopened.read("10000003", 1).unwrap().remove(0);
    assert_eq!(heading.heading, Some(1));

    word.apply(&batch(
        EditMode::Direct,
        vec![WordOp::DeleteParagraph {
            paragraph: "10000004".into(),
        }],
    ))
    .unwrap();
    let reopened = saved(&word, &["word/document.xml"]);
    assert!(reopened.read("10000004", 1).is_err());
}

#[test]
fn suggestions_save_as_revisions_and_settle() {
    let word = Word::open(FIXTURE.to_vec()).unwrap();
    word.apply(&batch(
        EditMode::Suggest,
        vec![
            replace("10000002", "百分之十", "一成"),
            WordOp::SetParagraphStyle {
                paragraph: "10000003".into(),
                style: "Heading1".into(),
            },
        ],
    ))
    .unwrap();
    let pending = word.revisions().unwrap().len();
    assert!(pending >= 2);
    let reopened = saved(&word, &["word/document.xml", "word/people.xml", "[Content_Types].xml", "word/_rels/document.xml.rels"]);
    let revisions = reopened.revisions().unwrap();
    assert_eq!(revisions.len(), pending);
    assert!(revisions.iter().all(|r| r.author == AUTHOR));

    word.review(ReviewAction::Accept, None).unwrap();
    let reopened = saved(&word, &["word/document.xml", "word/people.xml", "[Content_Types].xml", "word/_rels/document.xml.rels"]);
    assert!(reopened.revisions().unwrap().is_empty());
    assert!(text(&reopened, "10000002").contains("一成"));
}

#[test]
fn rejected_suggestions_leave_the_text_as_it_was() {
    let word = Word::open(FIXTURE.to_vec()).unwrap();
    word.apply(&batch(EditMode::Suggest, vec![replace("10000002", "百分之十", "一成")]))
        .unwrap();
    word.review(ReviewAction::Reject, None).unwrap();
    let reopened = saved(&word, &["word/document.xml", "word/people.xml", "[Content_Types].xml", "word/_rels/document.xml.rels"]);
    assert!(reopened.revisions().unwrap().is_empty());
    assert!(text(&reopened, "10000002").contains("百分之十"));
}

#[test]
fn comments_replies_and_resolution() {
    let word = Word::open(FIXTURE.to_vec()).unwrap();
    word.apply(&batch(
        EditMode::Direct,
        vec![
            WordOp::AddComment {
                paragraph: "10000002".into(),
                find: Some("百分之十".into()),
                text: "数字来源？".into(),
            },
            WordOp::ReplyComment {
                comment: "0".into(),
                text: "已补充。".into(),
            },
            WordOp::ResolveComment {
                comment: "0".into(),
            },
        ],
    ))
    .unwrap();
    let comment_parts = [
        "word/document.xml",
        "word/comments.xml",
        "word/commentsExtended.xml",
        "word/commentsIds.xml",
        "word/commentsExtensible.xml",
        "word/people.xml",
        "[Content_Types].xml",
        "word/_rels/document.xml.rels",
    ];
    let reopened = saved(&word, &comment_parts);
    let comments = reopened.comments().unwrap();
    let added = comments.iter().find(|c| c.text == "数字来源？").unwrap();
    assert_eq!(added.author, AUTHOR);
    assert_eq!(added.paragraphs, ["10000002"]);
    let reply = comments.iter().find(|c| c.text == "已补充。").unwrap();
    assert_eq!(reply.parent.as_deref(), Some("0"));
    assert!(comments.iter().find(|c| c.id == "0").unwrap().done);
    assert!(comments.iter().any(|c| c.author == "张三"));
}

#[test]
fn table_rows_and_cells() {
    let word = Word::open(FIXTURE.to_vec()).unwrap();
    word.apply(&batch(
        EditMode::Direct,
        vec![
            WordOp::InsertTableRow {
                row_of: "10000008".into(),
                position: RowPosition::After,
                cells: vec!["成本".into(), "60".into()],
            },
            WordOp::SetCellText {
                cell_of: "10000009".into(),
                text: "120".into(),
            },
        ],
    ))
    .unwrap();
    let cells = |w: &Word| -> Vec<String> {
        w.outline().unwrap().into_iter().filter(|e| e.table.is_some()).map(|e| e.summary).collect()
    };
    let reopened = saved(&word, &["word/document.xml"]);
    assert_eq!(cells(&reopened), ["项目", "金额", "收入", "120", "成本", "60"]);

    word.apply(&batch(
        EditMode::Direct,
        vec![WordOp::DeleteTableRow {
            row_of: "10000008".into(),
        }],
    ))
    .unwrap();
    let reopened = saved(&word, &["word/document.xml"]);
    assert_eq!(cells(&reopened), ["项目", "金额", "成本", "60"]);
}

#[test]
fn an_undone_edit_is_not_saved() {
    let word = Word::open(FIXTURE.to_vec()).unwrap();
    word.apply(&batch(EditMode::Direct, vec![replace("10000002", "百分之十", "一成")]))
        .unwrap();
    word.undo().unwrap();
    let reopened = saved(&word, &["word/document.xml"]);
    assert_eq!(text(&reopened, "10000002"), text(&Word::open(FIXTURE.to_vec()).unwrap(), "10000002"));
}

#[test]
fn a_replicas_edit_is_saved() {
    let word = Word::open(FIXTURE.to_vec()).unwrap();
    let replica = EditSessionCore::new(424_242, Host::new(Arc::new(|| 0), Arc::new(|| 7), Arc::new(|| 0.0)));
    replica.load(&word.encode_state()).unwrap();
    let sv = replica.encode_state_vector();
    let request = format!(
        r#"{{"expectVersion":"{}","steps":[{{"op":"replaceText","target":{{"kind":"search","text":"百分之十","within":{{"kind":"paragraph","story":"body","paraId":"10000002"}},"view":"accepted"}},"text":"两成"}}]}}"#,
        replica.version()
    );
    assert!(replica.apply_edits_json(&request).unwrap().contains(r#""ok":true"#));
    word.apply_update(&replica.encode_diff(&sv).unwrap()).unwrap();
    let reopened = saved(&word, &["word/document.xml"]);
    assert!(text(&reopened, "10000002").contains("两成"));
}

#[test]
fn saving_does_not_change_the_document() {
    let word = Word::open(FIXTURE.to_vec()).unwrap();
    word.apply(&batch(EditMode::Direct, vec![replace("10000002", "百分之十", "一成")]))
        .unwrap();
    let before = word.encode_state();
    let first = word.save_with(&options()).unwrap();
    assert_eq!(word.encode_state(), before);
    assert_eq!(word.save_with(&options()).unwrap(), first);
}
