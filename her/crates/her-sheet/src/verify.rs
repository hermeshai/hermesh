//! The self-check run on every write before the file is handed back.
//!
//! Three independent views must agree that exactly the intended cells
//! changed:
//! 1. the package: untouched entries are byte-identical (compressed bytes),
//!    changed parts are well-formed XML, no relationship or content-type
//!    entry points at a missing part that did not before;
//! 2. our own scanner, cell by cell, and the bytes of each edited sheet
//!    outside `<sheetData>`;
//! 3. calamine, a reader we did not write: every value and formula outside
//!    the edit is unchanged, and shared formulas we wrote out read the same
//!    as calamine's own expansion of the original.

use std::collections::BTreeMap;

use std::collections::BTreeSet;

use std::collections::HashMap;
use std::io::Cursor;
use std::io::Read;

use calamine::open_workbook_from_rs;

use calamine::Data;

use calamine::Reader as _;

use calamine::Xlsx;
use quick_xml::events::Event;

use crate::Error;
use crate::book::shared_strings;
use crate::cellref::CellRef;
use crate::package::Package;
use crate::package::SheetKind;
use crate::package::WorkbookInfo;
use crate::package::parse_rels;
use crate::package::resolve;
use crate::patch::Cached;
use crate::patch::NewValue;
use crate::patch::Outcome;
use crate::scan::CellInfo;
use crate::scan::FormulaKind;
use crate::scan::SheetIndex;
use crate::scan::scan;
use crate::xmltext::decode_xstring;

#[derive(Debug, Default)]
pub struct Report {
    pub problems: Vec<String>,
    /// calamine could not read the original, so view 3 was skipped.
    pub calamine_skipped: Option<String>,
}

impl Report {
    pub fn ok(&self) -> bool {
        self.problems.is_empty()
    }
}

pub fn verify(before: &[u8], out: &Outcome) -> Result<Report, Error> {
    let mut rep = Report::default();
    let a = Package::open(before.to_vec())?;
    let b = match Package::open(out.bytes.clone()) {
        Ok(p) => p,
        Err(e) => {
            rep.problems.push(format!("output does not open: {e}"));
            return Ok(rep);
        }
    };
    package_level(&a, &b, out, &mut rep)?;
    if !rep.ok() {
        return Ok(rep);
    }
    sheet_level(&a, &b, out, &mut rep)?;
    calamine_level(before, out, &mut rep);
    Ok(rep)
}

fn raw_entry(data: &[u8], name: &str) -> Result<(Vec<u8>, u32), Error> {
    let mut z = zip::ZipArchive::new(Cursor::new(data))?;
    let idx = z
        .index_for_name(name)
        .ok_or_else(|| Error::Internal(format!("no entry {name}")))?;
    let mut f = z.by_index_raw(idx)?;
    let crc = f.crc32();
    let mut buf = Vec::new();
    f.read_to_end(&mut buf)?;
    Ok((buf, crc))
}

fn well_formed(xml: &[u8]) -> Result<(), String> {
    let (mut rd, base) = crate::xmltext::reader(xml);
    let mut depth = 0i64;
    loop {
        match rd.read_event() {
            Ok(Event::Start(_)) => depth += 1,
            Ok(Event::End(_)) => depth -= 1,
            Ok(Event::Eof) => break,
            Ok(_) => {}
            Err(e) => {
                return Err(format!(
                    "{e} at byte {}",
                    (rd.error_position() as usize + base)
                ));
            }
        }
    }
    if depth != 0 {
        return Err(format!("{depth} unclosed elements"));
    }
    Ok(())
}

/// Internal relationship targets and content-type overrides that name a part
/// the package does not have.
fn dangling(p: &Package) -> Result<BTreeSet<String>, Error> {
    let mut out = BTreeSet::new();
    for name in p.names.iter().filter(|n| n.ends_with(".rels")) {
        let Some(xml) = p.read(name)? else { continue };
        let Ok(rels) = parse_rels(&xml) else { continue };
        // `xl/_rels/workbook.xml.rels` belongs to `xl/workbook.xml`.
        let source = name
            .replace("_rels/", "")
            .trim_end_matches(".rels")
            .to_string();
        for r in rels.iter().filter(|r| !r.external) {
            let t = resolve(&source, &r.target);
            if p.entry_name(&t).is_none() {
                out.insert(format!("{name} → {t}"));
            }
        }
    }
    if let Some(ct) = p.read("[Content_Types].xml")? {
        let (mut rd, _) = crate::xmltext::reader(&ct[..]);
        loop {
            match rd.read_event()? {
                Event::Empty(e) | Event::Start(e) if e.local_name().as_ref() == "Override" => {
                    if let Some(n) = crate::package::attrs(&e)?.get("PartName")
                        && p.entry_name(n).is_none()
                    {
                        out.insert(format!("[Content_Types].xml → {n}"));
                    }
                }
                Event::Eof => break,
                _ => {}
            }
        }
    }
    Ok(out)
}

fn package_level(a: &Package, b: &Package, out: &Outcome, rep: &mut Report) -> Result<(), Error> {
    let expect: Vec<&String> = a
        .names
        .iter()
        .filter(|n| !out.removed_parts.contains(n))
        .collect();
    let got: Vec<&String> = b.names.iter().collect();
    if expect != got {
        rep.problems.push(format!(
            "entry list differs: expected {expect:?}, got {got:?}"
        ));
        return Ok(());
    }
    for name in &a.names {
        if out.removed_parts.contains(name) {
            continue;
        }
        if out.changed_parts.contains(name) {
            let xml = b.read(name)?.unwrap();
            if let Err(e) = well_formed(&xml) {
                rep.problems.push(format!("{name} is not well-formed: {e}"));
            }
            continue;
        }
        if raw_entry(&a.data, name)? != raw_entry(&b.data, name)? {
            rep.problems
                .push(format!("{name} changed but was not edited"));
        }
    }
    let before = dangling(a)?;
    for d in dangling(b)?.difference(&before) {
        rep.problems.push(format!("dangling reference: {d}"));
    }
    Ok(())
}

/// A formula cell's stored result is `cached` (none: no value).
fn cached_ok(c: &CellInfo, cached: Option<&Cached>) -> bool {
    match cached {
        None => c.v.is_none(),
        Some(Cached::Number(n)) => {
            matches!(c.t.as_deref(), None | Some("n"))
                && c.v.as_ref().and_then(|v| v.parse::<f64>().ok()) == Some(*n)
        }
        Some(Cached::Text(s)) => {
            c.t.as_deref() == Some("str")
                && c.v.as_ref().map(|v| decode_xstring(v)) == Some(s.clone())
        }
        Some(Cached::Bool(x)) => {
            c.t.as_deref() == Some("b") && c.v.as_deref() == Some(if *x { "1" } else { "0" })
        }
        Some(Cached::Error(e)) => c.t.as_deref() == Some("e") && c.v.as_ref() == Some(e),
    }
}

/// The bytes of a sheet part outside `<sheetData>` and `<dimension>`.
fn outside_sheet_data(xml: &[u8], ix: &SheetIndex) -> Vec<u8> {
    let mut head_end = ix.sheet_data_open.start;
    let mut out = Vec::new();
    let mut pos = 0;
    if let Some((d, _)) = &ix.dimension
        && d.start < head_end
    {
        out.extend_from_slice(&xml[..d.start]);
        pos = d.end;
    }
    out.extend_from_slice(&xml[pos..head_end]);
    head_end = if ix.sheet_data_self_closing {
        ix.sheet_data_open.end
    } else {
        let close = ix.sheet_data_close;
        close + xml[close..].iter().position(|&c| c == b'>').unwrap() + 1
    };
    out.extend_from_slice(&xml[head_end..]);
    out
}

fn sheet_level(a: &Package, b: &Package, out: &Outcome, rep: &mut Report) -> Result<(), Error> {
    let wa = WorkbookInfo::load(a)?;
    let wb = WorkbookInfo::load(b)?;
    let sst_a = shared_strings(a, &wa)?;
    let sst_b = shared_strings(b, &wb)?;
    // Shared strings: the old entries must be unchanged (indices stay valid).
    if sst_b.len() < sst_a.len() || sst_b[..sst_a.len()] != sst_a[..] {
        rep.problems.push("existing shared strings changed".into());
    }
    for (name, ch) in &out.sheets {
        let part = wa.sheet(name).and_then(|s| s.part.clone()).unwrap();
        let xa = a.read(&part)?.unwrap();
        let xb = b.read(&part)?.unwrap();
        let (ia, ib) = (scan(&xa)?, scan(&xb)?);
        if outside_sheet_data(&xa, &ia) != outside_sheet_data(&xb, &ib) {
            rep.problems
                .push(format!("{name}: bytes outside sheetData changed"));
        }
        let ma: HashMap<CellRef, &CellInfo> = ia.cells().map(|c| (c.at, c)).collect();
        let mb: HashMap<CellRef, &CellInfo> = ib.cells().map(|c| (c.at, c)).collect();
        let text_of = |c: &CellInfo, sst: &[String]| -> Option<String> {
            match c.t.as_deref() {
                Some("s") => {
                    c.v.as_ref()
                        .and_then(|v| v.parse::<usize>().ok())
                        .and_then(|i| sst.get(i).cloned())
                }
                Some("inlineStr") => c.inline.clone(),
                _ => None,
            }
        };
        let mut all: BTreeSet<CellRef> = ma.keys().copied().collect();
        all.extend(mb.keys().copied());
        for at in all {
            let (ca, cb) = (ma.get(&at).copied(), mb.get(&at).copied());
            if let Some((value, stored)) = ch.edited.get(&at) {
                let want_style = match ca {
                    Some(c) => c.s.clone(),
                    None => ia.default_style(at),
                };
                if let Some(cb) = cb
                    && cb.s != want_style
                {
                    rep.problems.push(format!(
                        "{name}!{at}: style {:?}, expected {:?}",
                        cb.s, want_style
                    ));
                }
                let ok = match (value, cb) {
                    (NewValue::Empty, None) => true,
                    (NewValue::Empty, Some(c)) => {
                        c.v.is_none() && c.formula.is_none() && c.inline.is_none()
                    }
                    (_, None) => false,
                    (NewValue::Number { value }, Some(c)) => {
                        c.formula.is_none()
                            && matches!(c.t.as_deref(), None | Some("n"))
                            && c.v.as_ref().and_then(|v| v.parse::<f64>().ok()) == Some(*value)
                    }
                    (NewValue::Bool { value }, Some(c)) => {
                        c.t.as_deref() == Some("b")
                            && c.v.as_deref() == Some(if *value { "1" } else { "0" })
                    }
                    (NewValue::Text { value }, Some(c)) => {
                        c.formula.is_none() && text_of(c, &sst_b).as_ref() == Some(value)
                    }
                    (NewValue::Formula { cached, .. }, Some(c)) => {
                        let f_ok = matches!(&c.formula, Some(f) if f.kind == FormulaKind::Normal && Some(&f.text) == stored.as_ref());
                        f_ok && cached_ok(c, cached.as_ref())
                    }
                    // The formula stays: as it was, or written out if its shared group broke.
                    (NewValue::Result { value }, Some(c)) => {
                        let f_ok = match ch.materialized.get(&at) {
                            Some(text) => {
                                matches!(&c.formula, Some(f) if f.kind == FormulaKind::Normal && &f.text == text)
                            }
                            None => ca.is_some_and(|a| a.content().formula == c.content().formula),
                        };
                        f_ok && cached_ok(c, Some(value))
                    }
                };
                if !ok {
                    rep.problems.push(format!(
                        "{name}!{at}: wrote {:?}, reads back as {:?}",
                        value,
                        cb.map(|c| c.content())
                    ));
                }
            } else if let Some(text) = ch.materialized.get(&at) {
                let (Some(ca), Some(cb)) = (ca, cb) else {
                    rep.problems
                        .push(format!("{name}!{at}: materialized cell missing"));
                    continue;
                };
                let f_ok = matches!(&cb.formula, Some(f) if f.kind == FormulaKind::Normal && &f.text == text);
                if !f_ok || ca.v != cb.v || ca.t != cb.t || ca.s != cb.s {
                    rep.problems.push(format!(
                        "{name}!{at}: shared formula written out wrong: {:?} → {:?}",
                        ca.content(),
                        cb.content()
                    ));
                }
            } else {
                let same = match (ca, cb) {
                    (Some(x), Some(y)) => {
                        let (mut cx, cy) = (x.content(), y.content());
                        // Text cells may point at a different index for the same text only if we
                        // changed them, which we did not: compare raw.
                        cx.formula = cx.formula.clone();
                        cx == cy
                    }
                    (None, None) => true,
                    _ => false,
                };
                if !same {
                    rep.problems.push(format!(
                        "{name}!{at}: changed but not edited: {:?} → {:?}",
                        ca.map(|c| c.content()),
                        cb.map(|c| c.content())
                    ));
                }
            }
        }
    }
    let _ = SheetKind::Worksheet;
    Ok(())
}

type Grid = BTreeMap<(u32, u32), Data>;
type FGrid = BTreeMap<(u32, u32), String>;

/// Streams cells (sparse): a dense range would allocate the whole used
/// rectangle, which for a sheet with a cell at XFD1048576 is all of it.
fn calamine_read(bytes: &[u8]) -> Result<BTreeMap<String, (Grid, FGrid)>, String> {
    let mut wb: Xlsx<_> = open_workbook_from_rs(Cursor::new(bytes.to_vec()))
        .map_err(|e: calamine::XlsxError| e.to_string())?;
    let mut out = BTreeMap::new();
    for name in wb.sheet_names() {
        let Ok(mut rd) = wb.worksheet_cells_reader(&name) else {
            continue;
        };
        let mut g = Grid::new();
        let mut fg = FGrid::new();
        loop {
            match rd.next_cell_with_formula() {
                Ok(Some(c)) => {
                    let k = (c.pos.0 + 1, c.pos.1 + 1);
                    let v: Data = c.value.into();
                    if v != Data::Empty {
                        g.insert(k, v);
                    }
                    if let Some(f) = c.formula.filter(|f| !f.is_empty()) {
                        fg.insert(k, f);
                    }
                }
                Ok(None) => break,
                Err(e) => return Err(format!("{name}: {e}")),
            }
        }
        out.insert(name, (g, fg));
    }
    Ok(out)
}

fn num(d: &Data) -> Option<f64> {
    match d {
        Data::Int(i) => Some(*i as f64),
        Data::Float(f) => Some(*f),
        Data::DateTime(t) => Some(t.as_f64()),
        _ => None,
    }
}

fn calamine_level(before: &[u8], out: &Outcome, rep: &mut Report) {
    let a = match calamine_read(before) {
        Ok(a) => a,
        Err(e) => {
            rep.calamine_skipped = Some(e);
            return;
        }
    };
    let b = match calamine_read(&out.bytes) {
        Ok(b) => b,
        Err(e) => {
            rep.problems
                .push(format!("calamine cannot read the output: {e}"));
            return;
        }
    };
    for (name, (ga, fa)) in &a {
        let Some((gb, fb)) = b.get(name) else {
            rep.problems.push(format!("calamine: sheet {name} missing"));
            continue;
        };
        let ch = out.sheets.get(name);
        let edited = |rc: &(u32, u32)| ch.and_then(|c| c.edited.get(&CellRef::new(rc.0, rc.1)));
        let mut keys: BTreeSet<(u32, u32)> = ga.keys().copied().collect();
        keys.extend(gb.keys().copied());
        keys.extend(fa.keys().copied());
        keys.extend(fb.keys().copied());
        let mut n = 0;
        for k in keys {
            let (va, vb) = (ga.get(&k), gb.get(&k));
            let (fa_, fb_) = (fa.get(&k), fb.get(&k));
            let at = CellRef::new(k.0, k.1);
            let problem = match edited(&k) {
                Some((value, stored)) => {
                    let v_ok = match value {
                        NewValue::Empty => vb.is_none_or(|v| *v == Data::Empty),
                        NewValue::Number { value } => vb.and_then(num) == Some(*value),
                        NewValue::Bool { value } => vb == Some(&Data::Bool(*value)),
                        NewValue::Text { value } => vb == Some(&Data::String(value.clone())),
                        NewValue::Formula {
                            cached: Some(Cached::Number(n)),
                            ..
                        } => vb.and_then(num) == Some(*n),
                        NewValue::Formula {
                            cached: Some(Cached::Text(s)),
                            ..
                        } => vb == Some(&Data::String(s.clone())),
                        NewValue::Formula { .. } => true,
                        NewValue::Result {
                            value: Cached::Number(n),
                        } => vb.and_then(num) == Some(*n),
                        NewValue::Result {
                            value: Cached::Text(s),
                        } => vb == Some(&Data::String(s.clone())),
                        NewValue::Result {
                            value: Cached::Bool(b),
                        } => vb == Some(&Data::Bool(*b)),
                        NewValue::Result { .. } => true,
                    };
                    let f_ok = match value {
                        NewValue::Formula { .. } => fb_ == stored.as_ref(),
                        // calamine may not have expanded the shared formula before (master later in the stream).
                        NewValue::Result { .. } => fb_ == fa_ || fa_.is_none(),
                        _ => fb_.is_none(),
                    };
                    (!v_ok || !f_ok).then(|| format!("edited cell reads back as {vb:?} / {fb_:?}"))
                }
                None => {
                    let materialized = ch.is_some_and(|c| c.materialized.contains_key(&at));
                    // calamine expands a shared formula only once it has seen the master in stream
                    // order; for members before it, it gives no text to compare with.
                    let f_same = fa_ == fb_ || (materialized && fa_.is_none());
                    (va != vb || !f_same).then(|| format!("{va:?} {fa_:?} → {vb:?} {fb_:?}"))
                }
            };
            if let Some(p) = problem {
                n += 1;
                if n <= 5 {
                    rep.problems.push(format!("calamine {name}!{at}: {p}"));
                }
            }
        }
        if n > 5 {
            rep.problems
                .push(format!("calamine {name}: {} more differences", n - 5));
        }
    }
}
