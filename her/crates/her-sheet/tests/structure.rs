//! Inserting and deleting rows and columns over the corpus: every write passes its own check
//! or is refused, and inserting then deleting the same rows gives the workbook back.

use her_sheet::Error;
use her_sheet::book::Book;
use her_sheet::package::SheetKind;
use her_sheet::structure::Axis;
use her_sheet::structure::Shift;
use her_sheet::structure::shift;

fn corpus() -> Vec<(String, Vec<u8>)> {
    let dir = format!("{}/tests/fixtures", env!("CARGO_MANIFEST_DIR"));
    let mut out: Vec<(String, Vec<u8>)> = std::fs::read_dir(&dir)
        .unwrap()
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.extension().is_some_and(|x| x == "xlsx" || x == "xlsm"))
        .map(|p| (p.file_name().unwrap().to_string_lossy().into_owned(), std::fs::read(&p).unwrap()))
        .collect();
    out.sort();
    out
}

/// Each worksheet's cells: value and formula, by address.
fn contents(book: &Book) -> Vec<Vec<(String, String, Option<String>)>> {
    book.sheets
        .iter()
        .enumerate()
        .map(|(s, sheet)| {
            sheet
                .index
                .iter()
                .flat_map(|ix| ix.cells())
                .map(|c| (c.at.to_string(), format!("{:?}", book.value(s, c.at)), book.formula(s, c.at).map(str::to_string)))
                .filter(|(_, v, f)| v != "Empty" || f.is_some())
                .collect()
        })
        .collect()
}

#[test]
fn corpus_is_written_or_refused() {
    let shifts = [
        Shift { axis: Axis::Rows, at: 2, count: 3, delete: false },
        Shift { axis: Axis::Rows, at: 2, count: 1, delete: true },
        Shift { axis: Axis::Cols, at: 2, count: 2, delete: false },
        Shift { axis: Axis::Cols, at: 1, count: 1, delete: true },
    ];
    let (mut written, mut refused) = (0, 0);
    for (name, data) in corpus() {
        let Ok(book) = Book::open(&data) else { continue };
        for sheet in book.sheets.iter().filter(|s| s.kind == SheetKind::Worksheet) {
            for s in shifts {
                match shift(&data, &sheet.name, s) {
                    Ok(out) => {
                        Book::open(&out).unwrap_or_else(|e| panic!("{name} {}: {s:?} does not open: {e}", sheet.name));
                        written += 1;
                    }
                    Err(Error::Unsupported(_) | Error::Formula(_)) => refused += 1,
                    Err(e) => panic!("{name} {}: {s:?}: {e}", sheet.name),
                }
            }
        }
    }
    eprintln!("written {written}, refused {refused}");
    assert!(written > 0);
}

#[test]
fn insert_then_delete_gives_it_back() {
    for (name, data) in corpus() {
        let Ok(book) = Book::open(&data) else { continue };
        let before = contents(&book);
        for sheet in book.sheets.iter().filter(|s| s.kind == SheetKind::Worksheet) {
            for axis in [Axis::Rows, Axis::Cols] {
                let Ok(grown) = shift(&data, &sheet.name, Shift { axis, at: 2, count: 2, delete: false }) else { continue };
                let back = shift(&grown, &sheet.name, Shift { axis, at: 2, count: 2, delete: true }).unwrap_or_else(|e| panic!("{name} {}: {e}", sheet.name));
                let after = contents(&Book::open(&back).unwrap());
                assert_eq!(before, after, "{name} {} {axis:?}", sheet.name);
            }
        }
    }
}

fn part(bytes: &[u8], name: &str) -> String {
    let pkg = her_sheet::package::Package::open(bytes.to_vec()).unwrap();
    String::from_utf8(pkg.read(name).unwrap().unwrap()).unwrap()
}

/// A model with what moves besides cells: merges, a conditional format, a validation, a
/// hyperlink, a row height, a column width, a defined name and another sheet reading it.
#[test]
fn deleting_rows_moves_everything_that_points_at_them() {
    let data = std::fs::read(format!("{}/tests/fixtures/structure_model.xlsx", env!("CARGO_MANIFEST_DIR"))).unwrap();
    let out = shift(&data, "Model", Shift { axis: Axis::Rows, at: 3, count: 2, delete: true }).unwrap();
    let book = Book::open(&out).unwrap();
    let (m, o) = (book.sheet_index("Model").unwrap(), book.sheet_index("Out").unwrap());
    let f = |s: usize, c: &str| book.formula(s, her_sheet::cellref::CellRef::parse(c).unwrap()).map(str::to_string);
    assert_eq!(f(m, "D1").as_deref(), Some("SUM(B1:B8)"));
    assert_eq!(f(m, "D2").as_deref(), Some("B3+$B$7"));
    assert_eq!(f(m, "C3").as_deref(), Some("B3*2"));
    assert_eq!(f(o, "A1").as_deref(), Some("Model!B4+SUM(Model!B1:B8)"));
    assert_eq!(f(o, "A2").as_deref(), Some("Model!#REF!"));
    assert_eq!(format!("{:?}", book.value(o, her_sheet::cellref::CellRef::parse("A2").unwrap())), "Error(\"#REF!\")");
    let sheet = part(&out, "xl/worksheets/sheet1.xml");
    assert!(sheet.contains("<mergeCell ref=\"A10:C10\""), "{sheet}");
    assert!(!sheet.contains("E3:E4") && sheet.contains("mergeCells count=\"1\""), "{sheet}");
    assert!(sheet.contains("sqref=\"B2:B6\"") && sheet.contains("<formula>B$7</formula>"), "{sheet}");
    assert!(sheet.contains("sqref=\"F1:F8\"") && sheet.contains("<formula1>$A$1:$A$3</formula1>"), "{sheet}");
    assert!(sheet.contains(" ref=\"G1\" r:id"), "{sheet}");
    assert!(sheet.contains("<row r=\"4\"") && sheet.contains("ht=\"40\""), "{sheet}");
    assert!(part(&out, "xl/workbook.xml").contains("Model!$B$1:$B$8"));

    let wide = shift(&data, "Model", Shift { axis: Axis::Cols, at: 2, count: 1, delete: false }).unwrap();
    let book = Book::open(&wide).unwrap();
    let f = |s: usize, c: &str| book.formula(s, her_sheet::cellref::CellRef::parse(c).unwrap()).map(str::to_string);
    assert_eq!(f(m, "D5").as_deref(), Some("C5*2"));
    assert_eq!(f(o, "A1").as_deref(), Some("Model!C6+SUM(Model!C1:C10)"));
    let sheet = part(&wide, "xl/worksheets/sheet1.xml");
    assert!(sheet.contains("<mergeCell ref=\"A12:D12\"") && sheet.contains("min=\"4\" max=\"4\" />"), "{sheet}");
}

/// A comment and its note box move with their cell; deleting the cell's row takes them away.
#[test]
fn comments_move_with_their_cells() {
    let data = std::fs::read(format!("{}/tests/fixtures/gen/dcf_model.xlsx", env!("CARGO_MANIFEST_DIR"))).unwrap();
    let out = shift(&data, "假设", Shift { axis: Axis::Rows, at: 2, count: 3, delete: false }).unwrap();
    assert!(part(&out, "xl/comments1.xml").contains("<comment ref=\"B9\""));
    let vml = part(&out, "xl/drawings/vmlDrawing1.vml");
    assert!(vml.contains("<x:Row>8</x:Row>") && vml.contains("<x:Column>1</x:Column>") && vml.contains("<x:Anchor>2, 15, 7, 10, 4, 15, 11, 4</x:Anchor>"), "{vml}");
    let gone = shift(&data, "假设", Shift { axis: Axis::Rows, at: 6, count: 1, delete: true }).unwrap();
    assert!(!part(&gone, "xl/comments1.xml").contains("<comment "));
    assert!(!part(&gone, "xl/drawings/vmlDrawing1.vml").contains("<v:shape "));
    Book::open(&gone).unwrap();
}
