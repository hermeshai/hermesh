//! The calculation agrees with Excel (design §5.6, §13.1): on files Excel saved, every formula
//! we compute equals the value Excel stored; functions finance models use match their
//! reference values; real models with circular references settle where Excel did.

use std::path::Path;
use std::path::PathBuf;

use her_sheet::NewValue;
use her_sheet::book::Book;
use her_sheet::book::Value;
use her_sheet::calc::Engine;
use her_sheet::cellref::CellRef;

fn fixture(name: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures")
        .join(name)
}

fn engine(path: &Path) -> Engine {
    Engine::new(Book::open(&std::fs::read(path).unwrap()).unwrap()).unwrap()
}

/// Formula cells whose computed value differs from the stored one.
fn disagreements(e: &Engine) -> Vec<String> {
    let mut out = Vec::new();
    for (s, sheet) in e.book.sheets.iter().enumerate() {
        for at in sheet.formulas.keys() {
            let (ours, excel) = (e.value((s, *at)), e.book.value(s, *at));
            if !ours.close_to(&excel, 1e-9) {
                out.push(format!(
                    "{}!{at}: Excel {excel:?}, ours {ours:?}",
                    sheet.name
                ));
            }
        }
    }
    out
}

/// Files saved by Excel whose formulas hold no volatile function, cycle or external link.
#[test]
fn agrees_with_excel_on_saved_files() {
    for name in [
        "column_row_ranges.xlsx",
        "formula_range_without_column_component.xlsx",
        "non_ascii_shared_formula.xlsx",
        "non_monotonic_si.xlsx",
        "pivots.xlsx",
        "shared_formula_bench.xlsx",
        "shared_formula_dragged.xlsx",
        "shared_formula_function_names.xlsx",
        "shared_formula_reversed.xlsx",
        "shared_formula_simple.xlsx",
        "issues.xlsx",
        "string-ref.xlsx",
    ] {
        let e = engine(&fixture(name));
        let bad = disagreements(&e);
        assert!(bad.is_empty(), "{name}: {bad:?}");
    }
}

/// `gen/funcs.xlsx`: column B a function, column C its reference value (computed from the
/// function's definition, not by Excel), including AGGREGATE and TEXTJOIN.
#[test]
fn finance_functions_match_reference_values() {
    let e = engine(&fixture("gen/funcs.xlsx"));
    let s = e.book.sheet_index("Funcs").unwrap();
    let (mut ok, mut wrong) = (0, Vec::new());
    for row in 1.. {
        let Value::Text(name) = e.book.value(s, CellRef::new(row, 1)) else {
            break;
        };
        let want = e.book.value(s, CellRef::new(row, 3));
        if want == Value::Empty {
            continue;
        }
        let got = e.value((s, CellRef::new(row, 2)));
        if got.close_to(&want, 1e-6) {
            ok += 1;
        } else {
            wrong.push(name);
        }
    }
    assert!(wrong.is_empty(), "{wrong:?}");
    assert!(ok >= 68, "{ok}");
}

/// A cycle on a workbook that iterates: x = 0.5·x + 1 settles at 2.
#[test]
fn a_cycle_settles() {
    let mut e = engine(&fixture("gen/funcs.xlsx"));
    let s = e.book.sheet_index("Data").unwrap();
    e.book.info.calc.iterate = true;
    let (a, b) = (CellRef::parse("K1").unwrap(), CellRef::parse("K2").unwrap());
    let impact = e
        .apply(&[
            (
                (s, a),
                NewValue::Formula {
                    text: "=K2*0.5+1".into(),
                    cached: None,
                },
            ),
            (
                (s, b),
                NewValue::Formula {
                    text: "=K1".into(),
                    cached: None,
                },
            ),
        ])
        .unwrap();
    assert!(impact.converged, "{impact:?}");
    assert!(impact.iterations.is_some());
    assert!(
        e.value((s, a)).close_to(&Value::Number(2.0), 1e-9),
        "{:?}",
        e.value((s, a))
    );
    assert!(e.value((s, b)).close_to(&Value::Number(2.0), 1e-9));
}

/// Real models from `HER_SHEET_CORPUS` (kept out of the repository): every formula, cycles
/// included, equals what Excel saved; started from 0 the cycles settle at the same values.
#[test]
fn real_models_agree_with_excel() {
    let Some(dir) = std::env::var_os("HER_SHEET_CORPUS") else {
        return;
    };
    let mut files: Vec<PathBuf> = std::fs::read_dir(dir)
        .unwrap()
        .filter_map(|e| Some(e.ok()?.path()))
        .filter(|p| p.extension().is_some_and(|x| x == "xlsx" || x == "xlsm"))
        .collect();
    files.sort();
    for path in files {
        let mut e = engine(&path);
        let formulas: usize = e.book.sheets.iter().map(|s| s.formulas.len()).sum();
        // Not vacuous: real formulas, computed (not held as stored values), cycles cut.
        assert!(
            formulas > 500 && e.constants().len() * 100 < formulas,
            "{}: {formulas} formulas, {} constants",
            path.display(),
            e.constants().len()
        );
        assert!(
            !e.cut().is_empty() && e.iterations.is_some(),
            "{}: no cycles",
            path.display()
        );
        let bad = disagreements(&e);
        assert!(
            bad.is_empty(),
            "{}: {} differ, e.g. {:?}",
            path.display(),
            bad.len(),
            &bad[..bad.len().min(3)]
        );
        e.recalc_cycles_from_zero().unwrap();
        let bad = disagreements(&e);
        assert!(
            bad.is_empty(),
            "{} from 0: {:?}",
            path.display(),
            &bad[..bad.len().min(3)]
        );
    }
}

#[test]
fn structured_references_keep_the_formula_and_trace_their_inputs() {
    let bytes = std::fs::read(fixture("gen/dcf_model.xlsx")).unwrap();
    let e = Engine::new(Book::open(&bytes).unwrap()).unwrap();
    let s = e.book.sheet_index("可比公司").unwrap();
    // This generated fixture has zero caches: seed the table's correct reference values
    // before testing trusted downstream writes, as a spreadsheet app would save them.
    let caches: Vec<_> = (2..=5)
        .map(|r| her_sheet::CellEdit {
            sheet: "可比公司".into(),
            cell: format!("F{r}"),
            value: NewValue::Result {
                value: her_sheet::Cached::Number(
                    e.value((s, CellRef::new(r, 6))).as_number().unwrap(),
                ),
            },
        })
        .collect();
    let settled = her_sheet::write(&bytes, &caches, &her_sheet::Options::default())
        .unwrap()
        .bytes;
    let mut e = Engine::new(Book::open(&settled).unwrap()).unwrap();
    let at = |a| CellRef::parse(a).unwrap();
    let pe = e.value((s, at("C2"))).as_number().unwrap();
    let roe = e.value((s, at("E2"))).as_number().unwrap();
    let original = e.book.sheets[s].formulas[&at("F2")].clone();
    assert!(
        e.value((s, at("F2")))
            .close_to(&Value::Number(pe / roe), 1e-12)
    );
    assert!(e.graph.readers((s, at("C2"))).contains(&(s, at("F2"))));
    for formula in [
        "=[@PE]/[@ROE]",
        "=Comps[@PE]/Comps[@ROE]",
        "=Comps[[#This Row],[PE]]/Comps[[#This Row],[ROE]]",
    ] {
        e.apply(&[(
            (s, at("F2")),
            NewValue::Formula {
                text: formula.into(),
                cached: None,
            },
        )])
        .unwrap();
        assert!(
            e.value((s, at("F2")))
                .close_to(&Value::Number(pe / roe), 1e-12),
            "{formula}"
        );
    }
    e.apply(&[(
        (s, at("H8")),
        NewValue::Formula {
            text: "=SUM(Comps[PE])".into(),
            cached: None,
        },
    )])
    .unwrap();
    let expected: f64 = (2..=5)
        .map(|r| e.value((s, CellRef::new(r, 3))).as_number().unwrap())
        .sum();
    assert_eq!(e.value((s, at("H8"))), Value::Number(expected));
    assert!(e.graph.readers((s, at("C2"))).contains(&(s, at("H8"))));
    let i = e
        .apply(&[((s, at("C2")), NewValue::Number { value: pe + 3.0 })])
        .unwrap();
    assert!(i.changed.iter().any(|c| c.node == (s, at("F2"))));
    assert_eq!(e.value((s, at("H8"))), Value::Number(expected + 3.0));
    // The compatibility expression must never replace the user's structured formula.
    let mut fresh = Engine::new(Book::open(&settled).unwrap()).unwrap();
    let edits = [((s, at("C2")), NewValue::Number { value: pe + 3.0 })];
    let impact = fresh.apply(&edits).unwrap();
    let writes = fresh.writes(&edits, &impact);
    let output = her_sheet::write(&settled, &writes, &her_sheet::Options::default()).unwrap();
    let reopened = Book::open(&output.bytes).unwrap();
    assert_eq!(reopened.sheets[s].formulas[&at("F2")], original);
    assert!(
        reopened
            .value(s, at("F2"))
            .close_to(&Value::Number((pe + 3.0) / roe), 1e-12)
    );
}

/// Only UTC is built in (technical design D-99). NOW() and TODAY(), all that Excel has, work;
/// IronCalc's own NOW("Asia/Shanghai"), which Excel does not take, is an error.
#[test]
fn time_functions_with_only_utc() {
    let mut e = engine(&fixture("gen/funcs.xlsx"));
    let s = e.book.sheet_index("Data").unwrap();
    let at = |a| CellRef::parse(a).unwrap();
    for (cell, text) in [
        ("Z1", "=NOW()"),
        ("Z2", "=TODAY()"),
        ("Z3", "=NOW(\"Asia/Shanghai\")"),
    ] {
        let v = NewValue::Formula {
            text: text.into(),
            cached: None,
        };
        e.apply(&[((s, at(cell)), v)]).unwrap();
    }
    assert!(matches!(e.value((s, at("Z1"))), Value::Number(v) if v > 40_000.0));
    assert!(matches!(e.value((s, at("Z2"))), Value::Number(v) if v > 40_000.0));
    assert_eq!(e.value((s, at("Z3"))), Value::Error("#VALUE!".into()));
}

#[test]
fn aggregate_options_and_textjoin_empty_values() {
    let mut e = engine(&fixture("gen/funcs.xlsx"));
    let s = e.book.sheet_index("Data").unwrap();
    let at = |a| CellRef::parse(a).unwrap();
    for (cell, v) in [
        ("Z1", NewValue::Number { value: 2.0 }),
        ("Z2", NewValue::Number { value: 4.0 }),
        (
            "Z3",
            NewValue::Formula {
                text: "=1/0".into(),
                cached: None,
            },
        ),
        (
            "Z4",
            NewValue::Formula {
                text: "=SUBTOTAL(9,Z1:Z2)".into(),
                cached: None,
            },
        ),
        (
            "Z5",
            NewValue::Formula {
                text: "=AGGREGATE(9,4,Z1:Z2)".into(),
                cached: None,
            },
        ),
    ] {
        e.apply(&[((s, at(cell)), v)]).unwrap();
    }
    for (formula, want) in [
        ("=AGGREGATE(9,2,Z1:Z5)", Value::Number(6.0)),
        ("=AGGREGATE(9,6,Z1:Z5)", Value::Number(18.0)),
        ("=AGGREGATE(9,4,Z1:Z5)", Value::Error("#DIV/0!".into())),
        ("=AGGREGATE(14,6,Z1:Z5,2)", Value::Number(6.0)),
        ("=AGGREGATE(15,2,Z1:Z5,1)", Value::Number(2.0)),
        ("=AGGREGATE(12,2,Z1:Z5)", Value::Number(3.0)),
        ("=AGGREGATE(16,2,Z1:Z5,0.5)", Value::Number(3.0)),
        ("=AGGREGATE(17,2,Z1:Z5,2)", Value::Number(3.0)),
        ("=AGGREGATE(18,2,Z1:Z5,0.5)", Value::Number(3.0)),
        ("=AGGREGATE(19,2,Z1:Z5,2)", Value::Number(3.0)),
        ("=AGGREGATE(14,6,Z1:Z5)", Value::Error("#VALUE!".into())),
        (
            "=TEXTJOIN(\",\",TRUE,\"a\",\"\",\"b\")",
            Value::Text("a,b".into()),
        ),
        (
            "=TEXTJOIN(\",\",FALSE,\"a\",\"\",\"b\")",
            Value::Text("a,,b".into()),
        ),
        ("=TEXTJOIN(\",\",TRUE,Z1:Z2)", Value::Text("2,4".into())),
        ("=TEXTJOIN(\",\",FALSE,AB1:AB3)", Value::Text(",,".into())),
        (
            "=TEXTJOIN(\",\",TRUE,{\"a\",\"\",\"b\"})",
            Value::Text("a,b".into()),
        ),
        (
            "=TEXTJOIN(\",\",FALSE,{\"a\",\"\",\"b\"})",
            Value::Text("a,,b".into()),
        ),
        (
            "=TEXTJOIN(\"\",TRUE,REPT(\"x\",32767),\"x\")",
            Value::Error("#VALUE!".into()),
        ),
    ] {
        e.apply(&[(
            (s, at("AA1")),
            NewValue::Formula {
                text: formula.into(),
                cached: None,
            },
        )])
        .unwrap();
        assert_eq!(e.value((s, at("AA1"))), want, "{formula}");
    }
}

#[test]
fn aggregate_operations_and_hidden_rows() {
    let mut book = Book::open(&std::fs::read(fixture("gen/funcs.xlsx")).unwrap()).unwrap();
    let s = book.sheet_index("Data").unwrap();
    book.sheets[s]
        .index
        .as_mut()
        .unwrap()
        .rows
        .iter_mut()
        .find(|r| r.r == 2)
        .unwrap()
        .hidden = true;
    let mut e = Engine::new(book).unwrap();
    for (r, n) in [(1, 1.0), (2, 2.0), (3, 3.0), (4, 4.0)] {
        e.apply(&[((s, CellRef::new(r, 26)), NewValue::Number { value: n })])
            .unwrap();
    }
    for (function, expected) in [
        (1, 2.5),
        (2, 4.0),
        (3, 4.0),
        (4, 4.0),
        (5, 1.0),
        (6, 24.0),
        (7, (5.0_f64 / 3.0).sqrt()),
        (8, 1.25_f64.sqrt()),
        (9, 10.0),
        (10, 5.0 / 3.0),
        (11, 1.25),
        (12, 2.5),
    ] {
        e.apply(&[(
            (s, CellRef::new(1, 27)),
            NewValue::Formula {
                text: format!("=AGGREGATE({function},4,Z1:Z4)"),
                cached: None,
            },
        )])
        .unwrap();
        assert!(
            e.value((s, CellRef::new(1, 27)))
                .close_to(&Value::Number(expected), 1e-12),
            "operation {function}"
        );
    }
    for (option, expected) in [
        (0, 10.0),
        (1, 8.0),
        (2, 10.0),
        (3, 8.0),
        (4, 10.0),
        (5, 8.0),
        (6, 10.0),
        (7, 8.0),
    ] {
        e.apply(&[(
            (s, CellRef::new(1, 27)),
            NewValue::Formula {
                text: format!("=AGGREGATE(9,{option},Z1:Z4)"),
                cached: None,
            },
        )])
        .unwrap();
        assert_eq!(
            e.value((s, CellRef::new(1, 27))),
            Value::Number(expected),
            "option {option}"
        );
    }
}
