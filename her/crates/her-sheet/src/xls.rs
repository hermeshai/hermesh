//! Excel 97–2003 workbooks (`.xls`, BIFF8), read only (spreadsheet-design §10.2): values,
//! formulas and the text Excel would show, through calamine. To change one, people save it as
//! `.xlsx` in their spreadsheet app.

use std::collections::BTreeMap;
use std::io::Cursor;

use calamine::Data;
use calamine::Reader;
use calamine::Xls;
use calamine::open_workbook_from_rs;

use crate::Error;
use crate::book::Value;
use crate::cellref::CellRef;
use crate::styles::format_number;
use crate::styles::general;

#[derive(Clone, Debug)]
pub struct XlsCell {
    pub value: Value,
    pub text: String,
    pub formula: Option<String>,
}

#[derive(Clone, Debug)]
pub struct XlsSheet {
    pub name: String,
    pub cells: BTreeMap<CellRef, XlsCell>,
}

#[derive(Clone, Debug)]
pub struct XlsBook {
    pub sheets: Vec<XlsSheet>,
}

impl XlsBook {
    pub fn open(bytes: &[u8]) -> Result<Self, Error> {
        let mut wb: Xls<_> = open_workbook_from_rs(Cursor::new(bytes.to_vec()))
            .map_err(|e: calamine::XlsError| Error::Unsupported(format!("cannot read this .xls ({e}); an Excel 4.0 or older file must be saved as .xlsx first")))?;
        let mut sheets = Vec::new();
        for name in wb.sheet_names() {
            let mut cells = BTreeMap::new();
            if let Ok(range) = wb.worksheet_range(&name)
                && let Some((r0, c0)) = range.start()
            {
                for (r, c, d) in range.used_cells() {
                    let (value, text) = match d {
                        Data::Int(i) => (Value::Number(*i as f64), i.to_string()),
                        Data::Float(f) => (Value::Number(*f), general(*f)),
                        Data::String(s) => (Value::Text(s.clone()), s.clone()),
                        Data::Bool(b) => (
                            Value::Bool(*b),
                            (if *b { "TRUE" } else { "FALSE" }).to_string(),
                        ),
                        Data::DateTime(t) => {
                            let v = t.as_f64();
                            let fmt = if v.fract() == 0.0 {
                                "yyyy-mm-dd"
                            } else if v < 1.0 {
                                "hh:mm:ss"
                            } else {
                                "yyyy-mm-dd hh:mm"
                            };
                            (Value::Number(v), format_number(v, fmt, false).0)
                        }
                        Data::DateTimeIso(s) | Data::DurationIso(s) => {
                            (Value::Text(s.clone()), s.clone())
                        }
                        Data::Error(e) => (Value::Error(e.to_string()), e.to_string()),
                        Data::Empty => continue,
                    };
                    cells.insert(
                        CellRef::new(r0 + r as u32 + 1, c0 + c as u32 + 1),
                        XlsCell {
                            value,
                            text,
                            formula: None,
                        },
                    );
                }
            }
            if let Ok(formulas) = wb.worksheet_formula(&name)
                && let Some((r0, c0)) = formulas.start()
            {
                for (r, c, f) in formulas.used_cells() {
                    if f.is_empty() {
                        continue;
                    }
                    let at = CellRef::new(r0 + r as u32 + 1, c0 + c as u32 + 1);
                    cells
                        .entry(at)
                        .or_insert(XlsCell {
                            value: Value::Empty,
                            text: String::new(),
                            formula: None,
                        })
                        .formula = Some(f.clone());
                }
            }
            sheets.push(XlsSheet { name, cells });
        }
        Ok(Self { sheets })
    }
}
