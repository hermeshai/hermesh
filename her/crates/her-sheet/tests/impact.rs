//! Edit → impact → write → reopen, as a person changing an assumption would (design §5.5,
//! §5.6.4): the impact names the outputs that moved, the written file passes its self-check,
//! and reopened it holds the new results, which a fresh calculation agrees with.

use std::path::Path;

use her_sheet::Cached;
use her_sheet::CellEdit;
use her_sheet::NewValue;
use her_sheet::Options;
use her_sheet::book::Book;
use her_sheet::book::Value;
use her_sheet::calc::Engine;
use her_sheet::cellref::CellRef;
use her_sheet::deps::Node;

fn open(bytes: &[u8]) -> Engine {
    Engine::new(Book::open(bytes).unwrap()).unwrap()
}

fn at(e: &Engine, sheet: &str, cell: &str) -> Node {
    (
        e.book.sheet_index(sheet).unwrap(),
        CellRef::parse(cell).unwrap(),
    )
}

/// Every formula's computed result written into the file, as Excel would have saved it.
/// Generated fixtures hold 0 for every formula; Excel-saved files need nothing.
fn settle(bytes: &[u8]) -> Vec<u8> {
    let e = open(bytes);
    let mut edits = Vec::new();
    for (s, sheet) in e.book.sheets.iter().enumerate() {
        for at in sheet.formulas.keys() {
            if e.constants().contains(&(s, *at))
                || e.book.cell(s, *at).is_some_and(|c| c.has_metadata)
            {
                continue;
            }
            let value = match e.value((s, *at)) {
                Value::Number(v) => Cached::Number(v),
                Value::Text(v) => Cached::Text(v),
                Value::Bool(v) => Cached::Bool(v),
                Value::Error(v) if her_sheet::formula::is_excel_error(&v) => Cached::Error(v),
                Value::Error(_) => continue,
                Value::Empty => Cached::Text(String::new()),
            };
            edits.push(CellEdit {
                sheet: sheet.name.clone(),
                cell: at.to_string(),
                value: NewValue::Result { value },
            });
        }
    }
    her_sheet::write(bytes, &edits, &Options::default())
        .unwrap()
        .bytes
}

/// Formula cells whose stored value is not what we compute.
fn stale(e: &Engine) -> Vec<String> {
    let mut out = Vec::new();
    for (s, sheet) in e.book.sheets.iter().enumerate() {
        for at in sheet.formulas.keys() {
            if !e.value((s, *at)).close_to(&e.book.value(s, *at), 1e-9)
                && !e.constants().contains(&(s, *at))
            {
                out.push(format!("{}!{at}", sheet.name));
            }
        }
    }
    out
}

#[test]
fn changing_wacc_moves_the_valuation_and_the_file_keeps_up() {
    let original = std::fs::read(
        Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/gen/dcf_model.xlsx"),
    )
    .unwrap();
    let settled = settle(&original);
    let mut e = open(&settled);
    // Left as generated: the XLOOKUP cell (a dynamic array, not written) and the table's
    // calculated column (structured references, which IronCalc does not compute).
    let left = [
        "估值!D17",
        "可比公司!F2",
        "可比公司!F3",
        "可比公司!F4",
        "可比公司!F5",
    ];
    assert!(
        stale(&e).iter().all(|c| left.contains(&c.as_str())),
        "{:?}",
        stale(&e)
    );

    let wacc = at(&e, "假设", "B6");
    let ev = at(&e, "估值", "D15");
    let before = e.value(ev).as_number().unwrap();
    let edits = vec![(wacc, NewValue::Number { value: 0.09 })];

    // Trying it first changes nothing.
    let preview = e.preview(&edits).unwrap();
    assert_eq!(e.value(wacc), Value::Number(0.085));
    assert_eq!(e.value(ev).as_number().unwrap(), before);

    let impact = e.apply(&edits).unwrap();
    assert_eq!(impact, preview);
    let after = e.value(ev).as_number().unwrap();
    assert!(
        after < before,
        "a higher WACC lowers the value: {before} → {after}"
    );
    assert!(impact.key_outputs.contains(&ev), "{:?}", impact.key_outputs);
    // Discount factors, present values, terminal value and the total move; the P&L does not.
    assert!(impact.changed.len() >= 20, "{}", impact.changed.len());
    assert!(
        impact
            .changed
            .iter()
            .all(|c| e.book.sheets[c.node.0].name == "估值")
    );

    let writes = e.writes(&edits, &impact);
    let out = her_sheet::write(&settled, &writes, &Options::default()).unwrap();
    let reopened = open(&out.bytes);
    assert!(
        stale(&reopened).iter().all(|c| left.contains(&c.as_str())),
        "{:?}",
        stale(&reopened)
    );
    assert_eq!(reopened.book.value(ev.0, ev.1).as_number().unwrap(), after);
    assert_eq!(reopened.book.value(wacc.0, wacc.1), Value::Number(0.09));
}

#[test]
fn a_new_formula_is_written_with_its_result() {
    let settled = settle(
        &std::fs::read(
            Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/gen/dcf_model.xlsx"),
        )
        .unwrap(),
    );
    let mut e = open(&settled);
    let cell = at(&e, "估值", "F20");
    let edits = vec![(
        cell,
        NewValue::Formula {
            text: "=D15/1000".into(),
            cached: None,
        },
    )];
    let impact = e.apply(&edits).unwrap();
    let writes = e.writes(&edits, &impact);
    assert!(matches!(
        &writes[0].value,
        NewValue::Formula {
            cached: Some(Cached::Number(_)),
            ..
        }
    ));
    let out = her_sheet::write(&settled, &writes, &Options::default()).unwrap();
    let reopened = open(&out.bytes);
    assert_eq!(reopened.book.formula(cell.0, cell.1), Some("D15/1000"));
    assert!(
        reopened
            .book
            .value(cell.0, cell.1)
            .close_to(&e.value(cell), 1e-12)
    );
}

/// Real models from `HER_SHEET_CORPUS`: change an input, write, reopen: the file holds what a
/// fresh calculation (iteration included) gives.
#[test]
fn real_models_what_if_round_trip() {
    let Some(dir) = std::env::var_os("HER_SHEET_CORPUS") else {
        return;
    };
    let path = Path::new(&dir).join("capstru.xlsx");
    let Ok(original) = std::fs::read(&path) else {
        return;
    };
    let mut e = open(&original);
    let edits = vec![
        (at(&e, "Inputs", "B37"), NewValue::Number { value: 0.05 }),
        (at(&e, "Inputs", "B14"), NewValue::Number { value: 3000.0 }),
    ];
    let impact = e.apply(&edits).unwrap();
    assert!(impact.converged && impact.iterations.is_some());
    assert!(!impact.key_outputs.is_empty());
    let out =
        her_sheet::write(&original, &e.writes(&edits, &impact), &Options::default()).unwrap();
    let reopened = open(&out.bytes);
    assert!(
        stale(&reopened).is_empty(),
        "{:?}",
        &stale(&reopened)[..stale(&reopened).len().min(5)]
    );
}
