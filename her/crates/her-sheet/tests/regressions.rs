//! Bugs the self-check caught during validation, kept as tests.

use her_sheet::Cached;
use her_sheet::CellEdit;
use her_sheet::NewValue;
use her_sheet::Options;
use her_sheet::patch::apply;

fn corpus(name: &str) -> Vec<u8> {
    std::fs::read(format!(
        "{}/tests/fixtures/{name}",
        env!("CARGO_MANIFEST_DIR")
    ))
    .unwrap()
}

fn edit(sheet: &str, cell: &str, value: NewValue) -> CellEdit {
    CellEdit {
        sheet: sheet.into(),
        cell: cell.into(),
        value,
    }
}

/// First worksheet's name and its scanned index.
fn first_sheet(data: &[u8]) -> (String, her_sheet::scan::SheetIndex) {
    let pkg = her_sheet::package::Package::open(data.to_vec()).unwrap();
    let wb = her_sheet::package::WorkbookInfo::load(&pkg).unwrap();
    let s = wb
        .sheets
        .iter()
        .find(|s| s.kind == her_sheet::package::SheetKind::Worksheet)
        .unwrap();
    let xml = pkg.read(s.part.as_ref().unwrap()).unwrap().unwrap();
    (s.name.clone(), her_sheet::scan::scan(&xml).unwrap())
}

fn check(file: &str, edits: Vec<CellEdit>) -> her_sheet::Outcome {
    let data = corpus(file);
    her_sheet::write(&data, &edits, &Options::default()).unwrap_or_else(|e| panic!("{file}: {e}"))
}

/// The sheet starts with a UTF-8 byte-order mark; quick-xml counts positions
/// from after it, which put every splice three bytes off.
#[test]
fn sheet_with_byte_order_mark() {
    let (name, ix) = first_sheet(&corpus("missing_r_attributes.xlsx"));
    let cells: Vec<String> = ix
        .cells()
        .filter(|c| {
            !ix.merges
                .iter()
                .any(|m| m.contains(c.at) && m.first != c.at)
        })
        .map(|c| c.at.to_string())
        .take(20)
        .collect();
    let edits = cells
        .iter()
        .map(|c| edit(&name, c, NewValue::Number { value: 7.0 }))
        .collect();
    check("missing_r_attributes.xlsx", edits);
}

/// Row 61 is `<c r="E61"/>` followed by cells with no address, which take their column from the
/// cell before them. Clearing E61, which has no style, dropped its element: F61..H61 slid to
/// A61..C61, the 939 in G61 with them. The fuzz test found it with these edits.
#[test]
fn clearing_a_cell_keeps_the_column_of_the_cells_after_it() {
    let sheet = "Some Sheet";
    let out = check(
        "missing_r_attributes.xlsx",
        vec![
            edit(
                sheet,
                "F20",
                NewValue::Text {
                    value: "#REF!".into(),
                },
            ),
            edit(sheet, "E61", NewValue::Empty),
            edit(sheet, "D30", NewValue::Empty),
            edit(
                sheet,
                "B51",
                NewValue::Formula {
                    text: "=SUM(F:J)".into(),
                    cached: Some(Cached::Text("是".into())),
                },
            ),
        ],
    );
    let (_, ix) = first_sheet(&out.bytes);
    let at = |cell: &str| ix.cell(her_sheet::cellref::CellRef::parse(cell).unwrap());
    assert!(at("A61").is_none());
    let e = at("E61").unwrap();
    assert!(e.v.is_none() && e.t.is_none() && e.s.is_none());
    assert_eq!(at("F61").unwrap().s.as_deref(), Some("28"));
    assert_eq!(at("G61").unwrap().v.as_deref(), Some("939"));
    assert_eq!(at("H61").unwrap().s.as_deref(), Some("49"));
}

/// `<dimension ref=".."></dimension>` written as a pair: replacing only the
/// start tag left a stray `</dimension>`.
#[test]
fn dimension_written_as_a_pair() {
    check(
        "non_monotonic_si.xlsx",
        vec![edit(
            "Sheet1",
            "Z99",
            NewValue::Text {
                value: "新".into()
            },
        )],
    );
}

/// Editing one member of a shared formula writes the rest of the group out
/// as ordinary formulas, and calamine's own expansion agrees with ours.
#[test]
fn breaking_a_shared_formula() {
    let out = check(
        "shared_formula_bench.xlsx",
        vec![edit(
            "Sheet1",
            "B2",
            NewValue::Formula {
                text: "=A2*3".into(),
                cached: Some(Cached::Number(3.0)),
            },
        )],
    );
    assert!(!out.sheets["Sheet1"].materialized.is_empty());
}

/// Formulas changed: the stale calcChain goes, with its relationship and
/// content type, and Excel is told to recompute on open.
#[test]
fn calc_chain_dropped_and_full_calc_set() {
    let out = check(
        "issues.xlsx",
        vec![edit(
            "Sheet1",
            "A1",
            NewValue::Formula {
                text: "=1+1".into(),
                cached: None,
            },
        )],
    );
    assert!(
        out.removed_parts
            .iter()
            .any(|p| p.ends_with("calcChain.xml"))
    );
    let pkg = her_sheet::package::Package::open(out.bytes).unwrap();
    let wb = String::from_utf8(pkg.read("xl/workbook.xml").unwrap().unwrap()).unwrap();
    assert!(wb.contains("fullCalcOnLoad=\"1\""));
}

/// What the first version refuses, and why.
#[test]
fn refusals() {
    let refused = |file: &str, pick: &dyn Fn(&her_sheet::scan::SheetIndex) -> String| {
        let data = corpus(file);
        let (name, ix) = first_sheet(&data);
        let cell = pick(&ix);
        let r = apply(
            &data,
            &[edit(&name, &cell, NewValue::Number { value: 1.0 })],
            &Options::default(),
        );
        assert!(
            matches!(r, Err(her_sheet::Error::Unsupported(_))),
            "{file} {cell}: not refused"
        );
    };
    // Inside a merged range, not its top-left cell.
    refused("merge_cells.xlsx", &|ix| {
        let m = ix.merges.iter().find(|m| m.first != m.last).unwrap();
        m.last.to_string()
    });
    // A table header.
    refused("inventory-table.xlsx", &|_| "A1".into());
    // A picture in a cell (rich value).
    refused("picture_richdata.xlsx", &|ix| {
        ix.cells().find(|c| c.has_metadata).unwrap().at.to_string()
    });
    // A spilling function.
    let r = apply(
        &corpus("issues.xlsx"),
        &[edit(
            "Sheet1",
            "A1",
            NewValue::Formula {
                text: "=FILTER(A:A,B:B>1)".into(),
                cached: None,
            },
        )],
        &Options::default(),
    );
    assert!(matches!(r, Err(her_sheet::Error::Unsupported(_))));
}
