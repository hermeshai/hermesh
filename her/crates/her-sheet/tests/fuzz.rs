//! Random edits on every fixture, each checked by `her_sheet::write` (design §13.1).
//!
//! A write may be refused (§5.3.3); it may never fail its self-check or error otherwise.
//! `HER_SHEET_FUZZ_ITERS` sets the edits per file (default 40); `HER_SHEET_FUZZ_SEED` the seed;
//! `HER_SHEET_CORPUS` adds a directory of real models kept out of the repository.

use std::collections::BTreeMap;
use std::path::Path;
use std::path::PathBuf;

use her_sheet::Cached;
use her_sheet::CellEdit;
use her_sheet::Error;
use her_sheet::NewValue;
use her_sheet::Options;
use her_sheet::cellref::CellRef;
use her_sheet::cellref::MAX_COL;
use her_sheet::cellref::MAX_ROW;
use her_sheet::cellref::col_name;
use her_sheet::package::Package;
use her_sheet::package::SheetKind;
use her_sheet::package::WorkbookInfo;
use her_sheet::scan::SheetIndex;
use her_sheet::scan::scan;

struct Rng(u64);

impl Rng {
    fn next(&mut self) -> u64 {
        self.0 ^= self.0 >> 12;
        self.0 ^= self.0 << 25;
        self.0 ^= self.0 >> 27;
        self.0.wrapping_mul(0x2545F4914F6CDD1D)
    }
    fn below(&mut self, n: u64) -> u64 {
        if n == 0 { 0 } else { self.next() % n }
    }
    fn pick<'a, T>(&mut self, xs: &'a [T]) -> &'a T {
        &xs[self.below(xs.len() as u64) as usize]
    }
}

const TEXTS: &[&str] = &[
    "营业收入",
    "  leading and trailing  ",
    "a<b & c>d \"quoted\" 'apos'",
    "_x0041_ literal",
    "line1\nline2",
    "tab\there",
    "000001",
    "=not a formula",
    "😀",
    "",
    " ",
    "1E+5",
    "#REF!",
    "TRUE",
];
const NUMBERS: &[f64] = &[
    0.0,
    1.0,
    -3.5,
    0.1 + 0.2,
    1e-10,
    1e20,
    123456789.123,
    45658.0,
    -0.0,
    2.5e-7,
    9.99e15,
];

fn sheet_ref(name: &str) -> String {
    if name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_')
        && !name.starts_with(|c: char| c.is_ascii_digit())
    {
        format!("{name}!")
    } else {
        format!("'{}'!", name.replace('\'', "''"))
    }
}

fn value(rng: &mut Rng, sheets: &[String], max: CellRef) -> NewValue {
    let cell = |rng: &mut Rng| {
        CellRef::new(
            1 + rng.below(max.row as u64 + 3) as u32,
            1 + rng.below(max.col as u64 + 2) as u32,
        )
    };
    match rng.below(10) {
        0 | 1 => NewValue::Number {
            value: *rng.pick(NUMBERS),
        },
        2 | 3 => NewValue::Text {
            value: format!(
                "{}{}",
                rng.pick(TEXTS),
                if rng.below(3) == 0 { "x" } else { "" }
            ),
        },
        4 => NewValue::Bool {
            value: rng.below(2) == 0,
        },
        5 => NewValue::Empty,
        _ => {
            let (a, b) = (cell(rng), cell(rng));
            let (c1, c2) = (col_name(a.col.min(b.col)), col_name(a.col.max(b.col)));
            let (r1, r2) = (a.row.min(b.row), a.row.max(b.row));
            let text = match rng.below(8) {
                0 => format!("={a}+1"),
                1 => format!("=SUM({c1}{r1}:{c2}{r2})*2"),
                2 => format!("=IF({a}>0,\"是\",\"否\")"),
                3 => format!("=xlookup({a},{c1}:{c1},{c2}:{c2})"),
                4 => format!("={}{b}", sheet_ref(rng.pick(sheets))),
                5 => "=WSS(\"600519.SH\",\"close\",\"tradeDate=20251231\")".to_string(),
                6 => format!("=ROUND({a},2)&\"元\""),
                _ => format!("=SUM({c1}:{c2})"),
            };
            let cached = match rng.below(4) {
                0 => None,
                1 => Some(Cached::Number(42.5)),
                2 => Some(Cached::Text("是".into())),
                _ => Some(Cached::Error("#N/A".into())),
            };
            NewValue::Formula { text, cached }
        }
    }
}

fn edits(rng: &mut Rng, sheets: &[(String, SheetIndex)]) -> Vec<CellEdit> {
    let names: Vec<String> = sheets.iter().map(|s| s.0.clone()).collect();
    let (name, ix) = rng.pick(sheets);
    let cells: Vec<CellRef> = ix.cells().map(|c| c.at).collect();
    let formulas: Vec<CellRef> = ix
        .cells()
        .filter(|c| c.formula.is_some())
        .map(|c| c.at)
        .collect();
    let max = cells.iter().fold(CellRef::new(1, 1), |m, c| {
        CellRef::new(m.row.max(c.row), m.col.max(c.col))
    });
    (0..1 + rng.below(8))
        .map(|_| {
            let at = match rng.below(10) {
                0..=2 if !formulas.is_empty() => *rng.pick(&formulas),
                0..=4 if !cells.is_empty() => *rng.pick(&cells),
                // A little past the used range, but on the grid: a sheet may reach XFD1048576.
                5..=8 => CellRef::new(
                    (1 + rng.below(max.row as u64 + 5) as u32).min(MAX_ROW),
                    (1 + rng.below(max.col as u64 + 3) as u32).min(MAX_COL),
                ),
                _ => CellRef::new(1 + rng.below(3000) as u32, 1 + rng.below(40) as u32),
            };
            CellEdit {
                sheet: name.clone(),
                cell: at.to_string(),
                value: value(rng, &names, max),
            }
        })
        .collect()
}

fn worksheets(data: &[u8]) -> Option<Vec<(String, SheetIndex)>> {
    let pkg = Package::open(data.to_vec()).ok()?;
    let wb = WorkbookInfo::load(&pkg).ok()?;
    let sheets: Vec<_> = wb
        .sheets
        .iter()
        .filter(|s| s.kind == SheetKind::Worksheet)
        .filter_map(|s| {
            Some((
                s.name.clone(),
                scan(&pkg.read(s.part.as_ref()?).ok()??).ok()?,
            ))
        })
        .collect();
    (!sheets.is_empty()).then_some(sheets)
}

fn files() -> Vec<PathBuf> {
    let mut dirs = vec![Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures")];
    dirs.push(dirs[0].join("gen"));
    if let Some(extra) = std::env::var_os("HER_SHEET_CORPUS") {
        dirs.push(PathBuf::from(extra));
    }
    let mut out: Vec<PathBuf> = dirs
        .iter()
        .flat_map(|d| {
            std::fs::read_dir(d)
                .unwrap()
                .filter_map(|e| Some(e.ok()?.path()))
        })
        .filter(|p| p.extension().is_some_and(|e| e == "xlsx" || e == "xlsm"))
        .collect();
    out.sort();
    out
}

/// FNV-1a of the file name: each file gets its own edits, the same ones in any checkout.
fn name_hash(path: &Path) -> u64 {
    let name = path.file_name().unwrap_or_default().as_encoded_bytes();
    name.iter().fold(0xCBF29CE484222325, |h, b| {
        (h ^ *b as u64).wrapping_mul(0x100000001B3)
    })
}

#[test]
fn random_edits_never_break_a_file() {
    let iters: u64 = std::env::var("HER_SHEET_FUZZ_ITERS")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(40);
    let seed: u64 = std::env::var("HER_SHEET_FUZZ_SEED")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(1);
    let (mut written, mut refused) = (0, 0);
    let mut reasons: BTreeMap<String, usize> = BTreeMap::new();
    let mut failures = Vec::new();
    for path in files() {
        let data = std::fs::read(&path).unwrap();
        // Encrypted and damaged files cannot be loaded at all; opening them is refused elsewhere.
        let Some(sheets) = worksheets(&data) else {
            continue;
        };
        let mut rng = Rng(seed.wrapping_mul(0x9E3779B97F4A7C15) ^ name_hash(&path) | 1);
        for _ in 0..iters {
            let edits = edits(&mut rng, &sheets);
            match her_sheet::write(&data, &edits, &Options::default()) {
                Ok(_) => written += 1,
                Err(Error::Unsupported(m)) | Err(Error::Formula(m)) => {
                    refused += 1;
                    *reasons
                        .entry(m.split(" is ").last().unwrap_or(&m).to_string())
                        .or_default() += 1;
                }
                Err(e) => failures.push(format!(
                    "{}: {e}\n  edits: {}",
                    path.display(),
                    serde_json::to_string(&edits).unwrap()
                )),
            }
        }
    }
    assert!(written > 0);
    assert!(
        failures.is_empty(),
        "{} of {} writes failed:\n{}",
        failures.len(),
        written + refused + failures.len(),
        failures[..failures.len().min(5)].join("\n")
    );
    // Refusals are by design; a new reason means a new kind of structure to look at.
    let known = [
        "merged range",
        "table header",
        "dynamic array or rich value",
        "array formula",
        "without addresses",
        "protected",
    ];
    for r in reasons.keys() {
        assert!(
            known.iter().any(|k| r.contains(k)),
            "unexpected refusal: {r}"
        );
    }
}
