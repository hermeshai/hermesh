//! `tests/fixtures/report.docx`: a heading 1 (10000001), a body paragraph (10000002), a heading
//! 2 (10000003), two numbered list items (10000004/5), a 2x2 table (10000006..9) and a closing
//! paragraph (1000000A) carrying comment 0 by 张三.

use std::sync::Arc;

use docx_edit::session::{EditSessionCore, Host};
use her_word::{
    Edge, EditBatch, EditMode, NewParagraph, ReviewAction, RevisionKind, RowPosition, Word,
    WordError, WordOp,
};

const FIXTURE: &[u8] = include_bytes!("fixtures/report.docx");
const AUTHOR: &str = "Her";

fn open() -> Word {
    Word::open(FIXTURE.to_vec()).unwrap()
}

fn batch(mode: EditMode, edits: Vec<WordOp>) -> EditBatch {
    EditBatch {
        mode,
        author: AUTHOR.into(),
        note: String::new(),
        edits,
    }
}

fn direct(word: &Word, edits: Vec<WordOp>) -> her_word::Applied {
    word.apply(&batch(EditMode::Direct, edits)).unwrap()
}

fn text(word: &Word, id: &str) -> String {
    word.read(id, 1).unwrap().remove(0).text
}

fn replace(paragraph: &str, find: &str, with: &str) -> WordOp {
    WordOp::ReplaceText {
        paragraph: paragraph.into(),
        find: find.into(),
        replace: with.into(),
    }
}

/// A replica as the editor joins: another client, from the kernel's state.
fn replica(word: &Word) -> EditSessionCore {
    let host = Host::new(Arc::new(|| 0), Arc::new(|| 7), Arc::new(|| 0.0));
    let replica = EditSessionCore::new(424_242, host);
    replica.load(&word.encode_state()).unwrap();
    replica
}

fn replica_text(replica: &EditSessionCore, id: &str) -> String {
    let read = replica
        .read_paragraphs_json(&format!(r#"{{"paraIds":["{id}"],"view":"accepted"}}"#))
        .unwrap();
    let read: serde_json::Value = serde_json::from_str(&read).unwrap();
    read["paragraphs"][0]["text"].as_str().unwrap().to_owned()
}

#[test]
fn reads_headings_lists_tables_and_comments() {
    let word = open();
    assert_eq!(word.source_bytes(), FIXTURE);
    assert_eq!(word.source_sha256().len(), 64);
    let outline = word.outline().unwrap();
    let ids: Vec<&str> = outline.iter().map(|e| e.id.as_str()).collect();
    assert_eq!(
        ids,
        [
            "10000001", "10000002", "10000003", "10000004", "10000005", "10000006", "10000007",
            "10000008", "10000009", "1000000A"
        ]
    );
    assert_eq!(outline[0].heading, Some(1));
    assert_eq!(outline[0].style.as_deref(), Some("Heading1"));
    assert_eq!(outline[0].summary, "年度报告");
    assert_eq!(outline[2].heading, Some(2));
    assert_eq!(outline[1].heading, None);
    assert_eq!(outline[4].list.as_ref().unwrap().marker, "2.");
    let cell = outline[7].table.as_ref().unwrap();
    assert_eq!((cell.table, cell.row, cell.column), (1, 2, 1));
    assert_eq!(outline[9].summary, "结论：风险可控。");

    let comments = word.comments().unwrap();
    assert_eq!(comments.len(), 1);
    assert_eq!(comments[0].author, "张三");
    assert_eq!(comments[0].text, "请补充依据。");
    assert_eq!(comments[0].paragraphs, ["1000000A"]);

    let read = word.read("10000008", 3).unwrap();
    assert_eq!(
        read.iter().map(|p| p.id.as_str()).collect::<Vec<_>>(),
        ["10000008", "10000009", "1000000A"]
    );
    assert_eq!(read[0].text, "收入");
    assert_eq!(read[2].comments, ["0"]);
    assert!(matches!(
        word.read("FFFFFFFF", 1),
        Err(WordError::NoParagraph(_))
    ));

    let found = word.find("收入", 10).unwrap();
    assert_eq!(
        found
            .iter()
            .map(|f| f.paragraph.as_str())
            .collect::<Vec<_>>(),
        ["10000002", "10000004", "10000008"]
    );
    assert_eq!(found[0].offset, 16);
    assert!(found[0].context.contains("收入增长"));
    assert_eq!(word.find("收入", 1).unwrap().len(), 1);
}

#[test]
fn text_edits_land_directly() {
    let word = open();
    let applied = direct(
        &word,
        vec![
            replace("10000002", "百分之十", "百分之十二"),
            WordOp::InsertText {
                paragraph: "10000002".into(),
                text: "（未审计）".into(),
                after: Some("经营情况".into()),
                at: None,
            },
            WordOp::InsertText {
                paragraph: "10000001".into(),
                text: "2026 ".into(),
                after: None,
                at: Some(Edge::Start),
            },
            WordOp::DeleteText {
                paragraph: "1000000A".into(),
                find: "：".into(),
            },
        ],
    );
    assert_eq!(
        text(&word, "10000002"),
        "这是第一段，介绍公司的经营情况（未审计）。收入增长了百分之十二。"
    );
    assert_eq!(text(&word, "10000001"), "2026 年度报告");
    assert!(text(&word, "1000000A").starts_with("结论风险可控。"));
    assert!(applied.revision_ids_created.is_empty());
    assert_eq!(
        applied.paragraphs_touched,
        ["10000002", "10000001", "1000000A"]
    );
    assert!(!applied.update.is_empty());
    assert!(word.revisions().unwrap().is_empty());
}

#[test]
fn paragraph_edits() {
    let word = open();
    let applied = direct(
        &word,
        vec![
            WordOp::InsertParagraphs {
                after: Some("10000002".into()),
                before: None,
                paragraphs: vec![
                    NewParagraph {
                        text: "新增一段。".into(),
                        style: None,
                    },
                    NewParagraph {
                        text: "引用".into(),
                        style: Some("Quote".into()),
                    },
                ],
            },
            WordOp::DeleteParagraph {
                paragraph: "10000005".into(),
            },
            WordOp::SetParagraphStyle {
                paragraph: "10000002".into(),
                style: "Heading2".into(),
            },
            WordOp::SetAlignment {
                paragraph: "1000000A".into(),
                align: her_word::Align::Center,
            },
            WordOp::FormatText {
                paragraph: "10000002".into(),
                find: Some("经营情况".into()),
                bold: Some(true),
                italic: None,
                underline: None,
                strike: None,
                color: Some("#C00000".into()),
                highlight: Some("yellow".into()),
            },
        ],
    );
    let outline = word.outline().unwrap();
    let summaries: Vec<&str> = outline.iter().map(|e| e.summary.as_str()).collect();
    let at = summaries.iter().position(|s| *s == "新增一段。").unwrap();
    assert_eq!(outline[at - 1].id, "10000002");
    assert_eq!(outline[at + 1].style.as_deref(), Some("Quote"));
    assert!(!outline.iter().any(|e| e.id == "10000005"));
    assert_eq!(outline[1].heading, Some(2));
    assert_eq!(applied.paragraphs_touched.len(), 5);
}

#[test]
fn ambiguous_or_missing_find_is_refused_whole() {
    let word = open();
    let before = word.encode_state();
    let err = word
        .apply(&batch(
            EditMode::Direct,
            vec![
                replace("10000002", "百分之十", "一成"),
                replace("10000002", "利润", ""),
            ],
        ))
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        r#"edit 1: "利润" is not in paragraph 10000002"#
    );
    let err = word
        .apply(&batch(
            EditMode::Direct,
            vec![replace("10000002", "。", "")],
        ))
        .unwrap_err();
    assert!(err.to_string().contains("occurs 2 times"), "{err}");
    let err = word
        .apply(&batch(
            EditMode::Suggest,
            vec![
                replace("10000002", "百分之十", "一成"),
                replace("10000099", "a", "b"),
            ],
        ))
        .unwrap_err();
    assert_eq!(err.to_string(), "edit 1: no paragraph 10000099");
    assert_eq!(
        text(&word, "10000002"),
        "这是第一段，介绍公司的经营情况。收入增长了百分之十。"
    );
    assert!(word.revisions().unwrap().is_empty());
    assert_eq!(word.undo().unwrap(), None);
    assert_eq!(word.encode_state(), before);
}

#[test]
fn suggestions_are_revisions_to_accept_or_reject() {
    let word = open();
    let applied = word
        .apply(&batch(
            EditMode::Suggest,
            vec![
                replace("10000002", "收入", "营收"),
                WordOp::DeleteText {
                    paragraph: "1000000A".into(),
                    find: "风险".into(),
                },
            ],
        ))
        .unwrap();
    assert_eq!(applied.revision_ids_created.len(), 2);
    let revisions = word.revisions().unwrap();
    let kinds: Vec<_> = revisions
        .iter()
        .map(|r| (r.kind, r.text.as_str(), r.paragraph.as_str()))
        .collect();
    assert_eq!(
        kinds,
        [
            (RevisionKind::Deletion, "收入", "10000002"),
            (RevisionKind::Insertion, "营收", "10000002"),
            (RevisionKind::Deletion, "风险", "1000000A"),
        ]
    );
    assert!(
        revisions
            .iter()
            .all(|r| r.author == AUTHOR && r.date.ends_with('Z'))
    );
    // The text reads as if accepted; the paragraph lists its revisions.
    let read = word.read("10000002", 1).unwrap().remove(0);
    assert!(read.text.contains("营收增长"));
    assert_eq!(read.revisions, [applied.revision_ids_created[0].clone()]);

    // Reject the replacement, accept the deletion.
    let replacement = applied.revision_ids_created[0].clone();
    assert!(
        !word
            .review(ReviewAction::Reject, Some(&[replacement]))
            .unwrap()
            .is_empty()
    );
    assert!(text(&word, "10000002").contains("收入增长"));
    word.review(ReviewAction::Accept, None).unwrap();
    assert_eq!(
        text(&word, "1000000A").trim_end_matches(her_word::OBJECT),
        "结论：可控。"
    );
    assert!(word.revisions().unwrap().is_empty());
    assert!(matches!(
        word.review(ReviewAction::Accept, Some(&["9:9".into()])),
        Err(WordError::NoRevision(_))
    ));
}

#[test]
fn suggested_formatting_is_a_revision() {
    let word = open();
    let applied = word
        .apply(&batch(
            EditMode::Suggest,
            vec![WordOp::FormatText {
                paragraph: "10000002".into(),
                find: Some("收入".into()),
                bold: Some(true),
                italic: None,
                underline: None,
                strike: None,
                color: None,
                highlight: None,
            }],
        ))
        .unwrap();
    assert_eq!(applied.revision_ids_created.len(), 1);
    assert_eq!(word.revisions().unwrap()[0].kind, RevisionKind::Format);
}

#[test]
fn comments_add_reply_resolve() {
    let word = open();
    direct(
        &word,
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
    );
    let comments = word.comments().unwrap();
    let added = comments.iter().find(|c| c.text == "数字来源？").unwrap();
    assert_eq!(added.author, AUTHOR);
    assert_eq!(added.paragraphs, ["10000002"]);
    let reply = comments.iter().find(|c| c.text == "已补充。").unwrap();
    assert_eq!(reply.parent.as_deref(), Some("0"));
    assert!(comments.iter().find(|c| c.id == "0").unwrap().done);
    assert!(
        word.read("10000002", 1).unwrap()[0]
            .comments
            .contains(&added.id)
    );
}

#[test]
fn table_rows_and_cells() {
    let word = open();
    let applied = direct(
        &word,
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
    );
    assert_eq!(applied.paragraphs_touched.len(), 3);
    let cells: Vec<_> = word
        .outline()
        .unwrap()
        .into_iter()
        .filter(|e| e.table.is_some())
        .collect();
    assert_eq!(
        cells.iter().map(|e| e.summary.as_str()).collect::<Vec<_>>(),
        ["项目", "金额", "收入", "120", "成本", "60"]
    );
    assert_eq!(
        cells[5].table.as_ref().map(|t| (t.row, t.column)),
        Some((3, 2))
    );

    direct(
        &word,
        vec![WordOp::DeleteTableRow {
            row_of: "10000008".into(),
        }],
    );
    let cells: Vec<String> = word
        .outline()
        .unwrap()
        .into_iter()
        .filter(|e| e.table.is_some())
        .map(|e| e.summary)
        .collect();
    assert_eq!(cells, ["项目", "金额", "成本", "60"]);

    let err = word
        .apply(&batch(
            EditMode::Direct,
            vec![WordOp::InsertTableRow {
                row_of: "10000002".into(),
                position: RowPosition::Before,
                cells: vec![],
            }],
        ))
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        "edit 0: paragraph 10000002 is not in a table"
    );
}

#[test]
fn undo_takes_back_only_the_kernels_batch() {
    let word = open();
    let human = replica(&word);

    // The agent edits; the replica takes the update.
    let applied = direct(&word, vec![replace("10000002", "百分之十", "一成")]);
    human.apply_update(&applied.update).unwrap();
    assert!(replica_text(&human, "10000002").ends_with("一成。"));

    // The human edits another paragraph; the kernel merges it.
    let sv = human.encode_state_vector();
    let request = format!(
        r#"{{"expectVersion":"{}","steps":[{{"op":"replaceText","target":{{"kind":"search","text":"成本","within":{{"kind":"paragraph","story":"body","paraId":"10000005"}},"view":"accepted"}},"text":"费用"}}]}}"#,
        human.version()
    );
    assert!(
        human
            .apply_edits_json(&request)
            .unwrap()
            .contains(r#""ok":true"#)
    );
    word.apply_update(&human.encode_diff(&word.state_vector()).unwrap())
        .unwrap();
    drop(sv);
    assert_eq!(text(&word, "10000005"), "第二项：费用");

    let undone = word.undo().unwrap().expect("the agent's batch");
    assert!(text(&word, "10000002").ends_with("百分之十。"));
    assert_eq!(text(&word, "10000005"), "第二项：费用");
    // Nothing older of the kernel's own to take back: the human's edit is not its history.
    assert_eq!(word.undo().unwrap(), None);
    assert_eq!(text(&word, "10000005"), "第二项：费用");

    human.apply_update(&undone).unwrap();
    assert!(replica_text(&human, "10000002").ends_with("百分之十。"));
    assert_eq!(replica_text(&human, "10000005"), "第二项：费用");
    assert_eq!(human.encode_state_vector(), word.state_vector());
}

#[test]
fn a_bad_update_changes_nothing() {
    let word = open();
    let state = word.encode_state();
    for bad in [
        &[1u8, 2, 3, 4, 5][..],
        &[0xff; 64][..],
        &state[..state.len() / 2],
    ] {
        assert!(matches!(word.apply_update(bad), Err(WordError::Update(_))));
        assert_eq!(word.encode_state(), state);
    }
    assert_eq!(text(&word, "10000001"), "年度报告");
}

#[test]
fn renders_and_checks_saved_bytes() {
    let png = her_word::render_page(FIXTURE, 1, 1.0).unwrap();
    let decoder = png::Decoder::new(std::io::Cursor::new(&png));
    let reader = decoder.read_info().unwrap();
    let info = reader.info();
    assert!(
        info.width > 500 && info.height > info.width,
        "{}x{}",
        info.width,
        info.height
    );
    assert_eq!(her_word::page_count(FIXTURE).unwrap(), 1);
    assert!(matches!(
        her_word::render_page(FIXTURE, 2, 1.0),
        Err(WordError::NoPage(2))
    ));
    assert!(matches!(
        her_word::render_page(FIXTURE, 0, 1.0),
        Err(WordError::NoPage(0))
    ));

    let word = open();
    let summary = word.check_saved(FIXTURE).unwrap();
    assert_eq!((summary.tables, summary.pages), (1, 1));
    assert!(summary.paragraphs >= 6);
    assert!(word.check_saved(b"not a zip").is_err());
}

#[test]
fn revision_numbers_are_the_editors() {
    // The engine's yrs ids hash into the safe-integer range; numeric ids stay themselves.
    assert_eq!(her_word::revision_number("7"), 7);
    let n = her_word::revision_number("1:0");
    assert!(n < 1 << 53 && n != 0);
    let word = open();
    let applied = word
        .apply(&batch(
            EditMode::Suggest,
            vec![replace("10000002", "百分之十", "一成")],
        ))
        .unwrap();
    let ids = word
        .revision_ids(&[her_word::revision_number(&applied.revision_ids_created[0])])
        .unwrap();
    assert_eq!(ids, applied.revision_ids_created);
    assert!(matches!(
        word.revision_ids(&[1]),
        Err(WordError::NoRevision(_))
    ));
}

/// A paragraph in a block content control (a template's placeholder) is read, found and edited
/// like any other: the editor's selection there names it by its id.
#[test]
fn a_paragraph_in_a_content_control_is_one_of_the_documents() {
    let word = Word::open(include_bytes!("fixtures/content-control.docx").to_vec()).unwrap();
    assert!(word.outline().unwrap().iter().any(|p| p.id == "10000002"));
    assert_eq!(text(&word, "10000002"), "这是第一段，介绍公司的经营情况。收入增长了百分之十。");
    assert_eq!(word.find("收入增长", 5).unwrap().len(), 1);
    let applied = word
        .apply(&batch(EditMode::Suggest, vec![replace("10000002", "百分之十", "百分之二十")]))
        .unwrap();
    assert_eq!(applied.revision_ids_created.len(), 1);
}
