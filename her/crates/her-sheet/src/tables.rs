//! Table metadata and calculation-only expansion of structured references. The original
//! formula and table parts are never changed by this expansion.
use crate::{
    Error,
    book::Book,
    cellref::{CellRef, Rect, col_name},
    formula::{Tok, tokenize},
    package::{Package, WorkbookInfo, attrs, parse_rels, rels_part_for, resolve},
    xmltext::decode_xstring,
};
use quick_xml::events::Event;

#[derive(Clone, Debug)]
pub struct Table {
    pub sheet: usize,
    pub name: String,
    pub range: Rect,
    pub headers: u32,
    pub totals: u32,
    pub columns: Vec<String>,
}

pub fn load(pkg: &Package, info: &WorkbookInfo) -> Result<Vec<Table>, Error> {
    let mut tables = Vec::new();
    for (sheet, entry) in info.sheets.iter().enumerate() {
        let Some(part) = &entry.part else { continue };
        let Some(rels) = pkg.read(&rels_part_for(part))? else {
            continue;
        };
        for rel in parse_rels(&rels)?
            .iter()
            .filter(|r| r.is("table") && !r.external)
        {
            let Some(xml) = pkg.read(&resolve(part, &rel.target))? else {
                continue;
            };
            let (mut reader, _) = crate::xmltext::reader(&xml);
            let mut table = None;
            loop {
                match reader.read_event()? {
                    Event::Start(e) | Event::Empty(e) => {
                        let a = attrs(&e)?;
                        match e.local_name().as_ref() {
                            "table" => {
                                if let (Some(name), Some(range)) = (
                                    a.get("displayName").or(a.get("name")),
                                    a.get("ref").and_then(|r| Rect::parse(r)),
                                ) {
                                    table = Some(Table {
                                        sheet,
                                        name: name.clone(),
                                        range,
                                        headers: a
                                            .get("headerRowCount")
                                            .and_then(|v| v.parse().ok())
                                            .unwrap_or(1),
                                        totals: a
                                            .get("totalsRowCount")
                                            .and_then(|v| v.parse().ok())
                                            .unwrap_or(0),
                                        columns: Vec::new(),
                                    });
                                }
                            }
                            "tableColumn" => {
                                if let (Some(t), Some(name)) = (&mut table, a.get("name")) {
                                    t.columns.push(decode_xstring(name));
                                }
                            }
                            _ => {}
                        }
                    }
                    Event::Eof => break,
                    _ => {}
                }
            }
            if let Some(t) = table {
                tables.push(t);
            }
        }
    }
    Ok(tables)
}

fn reference(t: &Table, spec: &str, row: u32, book: &Book) -> Option<String> {
    // Apostrophes escape special characters in column captions. Do not treat escaped #/@
    // as selectors, or escaped brackets/commas as syntax.
    let inner = spec.strip_prefix('[')?.strip_suffix(']')?;
    let mut current = inner.starts_with('@');
    let mut parts = Vec::new();
    let mut text = String::new();
    let mut escaped = false;
    let mut selector = false;
    let mut depth = 0_u32;
    let mut column_range = false;
    for c in inner.strip_prefix('@').unwrap_or(inner).chars() {
        if escaped {
            text.push(c);
            escaped = false;
            continue;
        }
        match c {
            '\'' => escaped = true,
            '[' => depth += 1,
            ']' => {
                depth = depth.checked_sub(1)?;
            }
            ',' | ':' if depth == 0 => {
                column_range |= c == ':';
                if !text.is_empty() {
                    parts.push((std::mem::take(&mut text), selector));
                    selector = false;
                }
            }
            _ => {
                if text.is_empty() && c == '#' {
                    selector = true;
                }
                text.push(c);
            }
        }
    }
    if escaped || depth != 0 {
        return None;
    }
    if !text.is_empty() {
        parts.push((text, selector));
    }
    let mut first = t.range.first.row.checked_add(t.headers)?;
    let mut last = t.range.last.row.checked_sub(t.totals)?;
    let mut cols = Vec::new();
    let mut selectors = 0;
    for (p, special) in &parts {
        if *special {
            selectors += 1;
            // Multiple row sections denote a union, which we do not approximate as a range.
            if selectors > 1 {
                return None;
            }
            match p.to_ascii_lowercase().as_str() {
                "#this row" => current = true,
                "#all" => {
                    first = t.range.first.row;
                    last = t.range.last.row;
                }
                "#headers" if t.headers > 0 => {
                    first = t.range.first.row;
                    last = first + t.headers - 1;
                }
                "#totals" if t.totals > 0 => {
                    first = t.range.last.row - t.totals + 1;
                    last = t.range.last.row;
                }
                "#data" => {}
                _ => return None,
            }
        } else {
            let c = t.columns.iter().position(|c| c.eq_ignore_ascii_case(p))?;
            cols.push(t.range.first.col + c as u32);
        }
    }
    if cols.len() > 2 || cols.len() == 2 && !column_range || parts.is_empty() && !current {
        return None;
    }
    if current {
        if row < t.range.first.row + t.headers || row > t.range.last.row - t.totals {
            return Some("#VALUE!".into());
        }
        first = row;
        last = row;
    }
    if first > last {
        return Some("#REF!".into());
    }
    let c1 = cols.first().copied().unwrap_or(t.range.first.col);
    let c2 = cols.last().copied().unwrap_or(t.range.last.col);
    let prefix = format!("'{}'!", book.sheets[t.sheet].name.replace('\'', "''"));
    let a = format!("${}${first}", col_name(c1));
    let z = format!("${}${last}", col_name(c2));
    Some(if a == z {
        format!("{prefix}{a}")
    } else {
        format!("{prefix}{a}:{z}")
    })
}

pub fn expand(formula: &str, sheet: usize, at: CellRef, book: &Book) -> String {
    let Ok(toks) = tokenize(formula) else {
        return formula.into();
    };
    let mut out = String::new();
    let mut i = 0;
    while i < toks.len() {
        let named = matches!(toks.get(i), Some(Tok::Name(_) | Tok::Cell(_)))
            && matches!(toks.get(i + 1), Some(Tok::Bracket(_)));
        let spec = if named { toks.get(i + 1) } else { toks.get(i) };
        if let Some(Tok::Bracket(spec)) = spec {
            let table = if named {
                book.tables
                    .iter()
                    .find(|t| t.name.eq_ignore_ascii_case(toks[i].text()))
            } else {
                book.tables
                    .iter()
                    .find(|t| t.sheet == sheet && t.range.contains(at))
            };
            if let Some(r) = table.and_then(|t| reference(t, spec, at.row, book)) {
                out.push_str(&r);
                i += if named { 2 } else { 1 };
                continue;
            }
        }
        out.push_str(toks[i].text());
        i += 1;
    }
    out
}
