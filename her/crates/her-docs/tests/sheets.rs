//! Workbooks through the DocStore (spreadsheet-design §6, §10): a CSV read a range at a time,
//! edited, taken back and forward, saved; a change on disk guarded; a legacy .xls read only.

use std::path::Path;

use her_docs::DocError;
use her_docs::DocStore;
use her_docs::Launcher;
use her_protocol::doc::CellInput;
use her_protocol::doc::DocFormat;
use her_protocol::doc::SheetOp;

fn store(dir: &Path) -> DocStore {
    DocStore::new(dir.join("docs"), Launcher::Thread)
}

fn set(cell: &str, text: &str) -> SheetOp {
    SheetOp::SetValue { sheet: "people".into(), cell: cell.into(), value: CellInput::Text(text.into()) }
}

#[tokio::test]
async fn a_csv_is_read_by_range_edited_undone_and_saved() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("people.csv");
    let mut body = String::from("name,code\r\n");
    for i in 1..=500 {
        body.push_str(&format!("n{i},{i:06}\r\n"));
    }
    std::fs::write(&path, &body).unwrap();
    let docs = store(dir.path());
    let (doc, _token) = docs.open(&path, None, "s1").await.unwrap();
    let info = doc.info();
    assert_eq!(info.format, DocFormat::Csv);
    assert_eq!(info.sheets[0].used.as_deref(), Some("A1:B501"));

    // Only the rows asked for, leading zeros kept.
    let cells = docs.cells(&doc, "people", "A300:B301").await.unwrap();
    let shown: Vec<(String, String)> = cells.cells.iter().map(|c| (c.cell.clone(), c.text.clone())).collect();
    assert_eq!(shown, [("A300".into(), "n299".into()), ("B300".into(), "000299".into()), ("A301".into(), "n300".into()), ("B301".into(), "000300".into())]);

    // An edit, its control characters dropped; then undone and redone.
    let (rev, impact, dirty) = docs.apply(&doc, "s1", vec![set("A2", "Ann\u{7}e")], Some(0), false).await.unwrap();
    assert_eq!((rev, dirty), (1, true));
    assert_eq!(impact.edited[0].after_text, "Anne");
    assert!(matches!(docs.apply(&doc, "s1", vec![set("A3", "x")], Some(0), false).await, Err(DocError::Stale { base: 0, now: 1 })));
    let text_at = |cells: her_docs::sheet::Cells| cells.cells.first().map(|c| c.text.clone());
    docs.undo(&doc).await.unwrap();
    assert_eq!(text_at(docs.cells(&doc, "people", "A2").await.unwrap()).as_deref(), Some("n1"));
    assert!(!doc.info().dirty);
    docs.redo(&doc).await.unwrap();
    assert_eq!(text_at(docs.cells(&doc, "people", "A2").await.unwrap()).as_deref(), Some("Anne"));
    assert!(matches!(docs.redo(&doc).await, Err(DocError::NotEditable(m)) if m == "nothing to redo"));

    // Formulas are refused in a CSV.
    let formula = SheetOp::SetFormula { sheet: "people".into(), cell: "C1".into(), formula: "=1+1".into() };
    assert!(matches!(docs.apply(&doc, "s1", vec![formula], None, false).await, Err(DocError::Refused(_))));

    // Saved in place: only the edited row changed, line endings and all.
    let (info, written) = docs.save(&doc, None, false).await.unwrap();
    assert!(!info.dirty);
    assert_eq!(written.values, 1);
    let saved = std::fs::read_to_string(&path).unwrap();
    assert_eq!(saved, body.replacen("n1,000001", "Anne,000001", 1));
    let beside: Vec<_> = std::fs::read_dir(dir.path()).unwrap().map(|e| e.unwrap().file_name().into_string().unwrap()).filter(|n| n.contains("partial")).collect();
    assert!(beside.is_empty(), "{beside:?}");

    // Changed on disk since: saving over it needs force; saving as another kind is refused.
    docs.apply(&doc, "s1", vec![set("A3", "Bob")], None, false).await.unwrap();
    std::thread::sleep(std::time::Duration::from_millis(20));
    std::fs::write(&path, "name,code\r\nother,1\r\n").unwrap();
    assert!(matches!(docs.save(&doc, None, false).await, Err(DocError::Conflict(_))));
    assert!(matches!(docs.save(&doc, Some(&dir.path().join("people.xlsx")), false).await, Err(DocError::Refused(_))));
    docs.save(&doc, None, true).await.unwrap();
    assert!(std::fs::read_to_string(&path).unwrap().contains("Bob,000002"));
}

#[tokio::test]
async fn a_legacy_xls_is_read_only_and_other_files_are_refused() {
    let dir = tempfile::tempdir().unwrap();
    let fixture = Path::new(env!("CARGO_MANIFEST_DIR")).join("../her-sheet/tests/fixtures/legacy.xls");
    let path = dir.path().join("legacy.xls");
    std::fs::copy(fixture, &path).unwrap();
    let docs = store(dir.path());
    let (doc, _) = docs.open(&path, None, "s1").await.unwrap();
    let info = doc.info();
    assert_eq!(info.format, DocFormat::Xls);
    assert!(!info.save_in_place);
    let sheet = info.sheets[0].name.clone();
    assert!(!docs.cells(&doc, &sheet, "A1:Z100").await.unwrap().cells.is_empty());
    let op = SheetOp::SetValue { sheet: sheet.clone(), cell: "A1".into(), value: CellInput::Number(1.0) };
    assert!(matches!(docs.apply(&doc, "s1", vec![op], None, false).await, Err(DocError::Refused(_))));
    assert!(matches!(docs.save(&doc, None, true).await, Err(DocError::Refused(_))));

    let fake = dir.path().join("fake.xls");
    std::fs::write(&fake, "not a workbook").unwrap();
    assert!(matches!(docs.open(&fake, None, "s1").await, Err(DocError::Unsupported(_))));
}

/// Rows and columns inserted and deleted, undone back to the copy before, redone; a range sorted.
#[tokio::test]
async fn structure_and_sort_go_through_the_history() {
    use her_protocol::doc::SheetStructure;
    use her_protocol::doc::StructureKind;
    let dir = tempfile::tempdir().unwrap();
    let csv = dir.path().join("people.csv");
    std::fs::write(&csv, "name,n\nb,2\na,3\nc,1\n").unwrap();
    let wb = dir.path().join("model.xlsx");
    std::fs::copy(format!("{}/../her-sheet/tests/fixtures/structure_model.xlsx", env!("CARGO_MANIFEST_DIR")), &wb).unwrap();
    let docs = store(dir.path());
    let texts = |c: her_docs::sheet::Cells| c.cells.iter().map(|c| c.text.clone()).collect::<Vec<_>>();

    let (doc, _) = docs.open(&csv, None, "s1").await.unwrap();
    let ops = docs.sort_ops(&doc, "people", "A1:B4", "B", false, true).await.unwrap();
    docs.apply(&doc, "s1", ops, None, false).await.unwrap();
    assert_eq!(texts(docs.cells(&doc, "people", "A1:A4").await.unwrap()), ["name", "c", "b", "a"]);
    let change = SheetStructure { sheet: "people".into(), kind: StructureKind::DeleteRows, at: 2, count: 1 };
    docs.structure(&doc, "s1", change, None).await.unwrap();
    assert_eq!(texts(docs.cells(&doc, "people", "A1:A4").await.unwrap()), ["name", "b", "a"]);
    assert_eq!(doc.info().sheets[0].used.as_deref(), Some("A1:B3"));
    docs.undo(&doc).await.unwrap();
    assert_eq!(texts(docs.cells(&doc, "people", "A1:A4").await.unwrap()), ["name", "c", "b", "a"]);
    docs.redo(&doc).await.unwrap();
    assert_eq!(texts(docs.cells(&doc, "people", "A1:A4").await.unwrap()), ["name", "b", "a"]);

    let (doc, _) = docs.open(&wb, None, "s1").await.unwrap();
    let change = SheetStructure { sheet: "Model".into(), kind: StructureKind::InsertRows, at: 1, count: 2 };
    docs.structure(&doc, "s1", change, None).await.unwrap();
    let out = docs.cells(&doc, "Out", "A1").await.unwrap();
    assert_eq!(out.cells[0].formula.as_deref(), Some("=Model!B8+SUM(Model!B3:B12)"));
    docs.undo(&doc).await.unwrap();
    let out = docs.cells(&doc, "Out", "A1").await.unwrap();
    assert_eq!(out.cells[0].formula.as_deref(), Some("=Model!B6+SUM(Model!B1:B10)"));
    let (_, written) = { docs.redo(&doc).await.unwrap(); docs.save(&doc, None, false).await.unwrap() };
    assert_eq!(written.reshaped, 1);
}
