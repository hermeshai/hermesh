//! The OPC package: zip entries, relationships, content types, and the
//! workbook part's list of sheets.

use std::collections::HashMap;
use std::io::Cursor;
use std::io::Read;
use std::ops::Range;

use quick_xml::events::Event;

use crate::Error;
use crate::xmltext::unescape_xml;

pub struct Package {
    pub data: Vec<u8>,
    /// Entry names in zip order.
    pub names: Vec<String>,
    lower: HashMap<String, usize>,
}

impl Package {
    pub fn open(data: Vec<u8>) -> Result<Self, Error> {
        if data.starts_with(&[0xD0, 0xCF, 0x11, 0xE0]) {
            // A compound file: an encrypted xlsx, or a binary .xls.
            return Err(Error::Unsupported(
                "encrypted workbook or legacy .xls (compound file), not a zip package".into(),
            ));
        }
        if !data.starts_with(b"PK") {
            // Excel 2.x–4.0 worksheets (BIFF2–4) and anything else that is not a package.
            return Err(Error::Unsupported(
                "not an xlsx/xlsm package; open it in Excel and save as .xlsx".into(),
            ));
        }
        let mut zip = zip::ZipArchive::new(Cursor::new(&data[..]))?;
        let mut names = Vec::with_capacity(zip.len());
        for i in 0..zip.len() {
            names.push(zip.by_index_raw(i)?.name()?.to_string());
        }
        // Some writers put `\` in entry names; Excel reads them as `/`.
        let lower = names
            .iter()
            .enumerate()
            .map(|(i, n)| (n.replace('\\', "/").to_lowercase(), i))
            .collect();
        Ok(Self { data, names, lower })
    }

    /// Entry name for a part name, ignoring case (OPC part names are
    /// case-insensitive and rels targets are not always in the zip's case).
    pub fn entry_name(&self, part: &str) -> Option<&str> {
        let key = part
            .replace('\\', "/")
            .trim_start_matches('/')
            .to_lowercase();
        self.lower.get(&key).map(|&i| self.names[i].as_str())
    }

    pub fn read(&self, part: &str) -> Result<Option<Vec<u8>>, Error> {
        let Some(name) = self.entry_name(part) else {
            return Ok(None);
        };
        let mut zip = zip::ZipArchive::new(Cursor::new(&self.data[..]))?;
        let mut f = zip.by_name(name)?;
        let mut buf = Vec::with_capacity(f.size() as usize);
        f.read_to_end(&mut buf)?;
        Ok(Some(buf))
    }
}

#[derive(Clone, Debug)]
pub struct Rel {
    pub id: String,
    pub kind: String,
    pub target: String,
    pub external: bool,
    /// Byte range of the `<Relationship>` element in the rels part.
    pub span: Range<usize>,
}

impl Rel {
    /// Relationship types differ between transitional and strict OOXML;
    /// the last path segment does not.
    pub fn is(&self, last: &str) -> bool {
        self.kind.rsplit('/').next() == Some(last)
    }
}

pub fn rels_part_for(part: &str) -> String {
    let part = part.trim_start_matches('/');
    match part.rsplit_once('/') {
        Some((dir, file)) => format!("{dir}/_rels/{file}.rels"),
        None => format!("_rels/{part}.rels"),
    }
}

/// Resolves a relationship target against the part that owns the rels.
pub fn resolve(source_part: &str, target: &str) -> String {
    let target = target.split('#').next().unwrap_or(target);
    let mut parts: Vec<&str> = if let Some(abs) = target.strip_prefix('/') {
        return normalize(abs.split('/').collect());
    } else {
        source_part.trim_start_matches('/').split('/').collect()
    };
    parts.pop();
    parts.extend(target.split('/'));
    normalize(parts)
}

fn normalize(parts: Vec<&str>) -> String {
    let mut out: Vec<&str> = Vec::new();
    for p in parts {
        match p {
            "" | "." => {}
            ".." => {
                out.pop();
            }
            _ => out.push(p),
        }
    }
    out.join("/")
}

pub fn parse_rels(xml: &[u8]) -> Result<Vec<Rel>, Error> {
    let (mut r, base) = crate::xmltext::reader(xml);
    let mut out = Vec::new();
    loop {
        let start = r.buffer_position() as usize + base;
        match r.read_event()? {
            Event::Empty(e) | Event::Start(e) if e.local_name().as_ref() == "Relationship" => {
                let a = attrs(&e)?;
                // `<Relationship ...></Relationship>`: the span must take the end tag too.
                let is_start = !xml[..r.buffer_position() as usize + base].ends_with(b"/>");
                if is_start {
                    r.read_to_end(e.name())?;
                }
                let end = r.buffer_position() as usize + base;
                out.push(Rel {
                    id: a.get("Id").cloned().unwrap_or_default(),
                    kind: a.get("Type").cloned().unwrap_or_default(),
                    target: a.get("Target").cloned().unwrap_or_default(),
                    external: a.get("TargetMode").is_some_and(|m| m == "External"),
                    span: start..end,
                });
            }
            Event::Eof => break,
            _ => {}
        }
    }
    Ok(out)
}

/// Attributes by local name, unescaped.
pub fn attrs(e: &quick_xml::events::BytesStart<'_>) -> Result<HashMap<String, String>, Error> {
    let mut m = HashMap::new();
    for a in e.attributes() {
        let a = a.map_err(|e| Error::Xml(e.to_string()))?;
        let k = a.key.local_name().as_ref().to_string();
        let v = unescape_xml(&a.value);
        m.insert(k, v);
    }
    Ok(m)
}

#[derive(Clone, Debug, PartialEq)]
pub enum SheetKind {
    Worksheet,
    Chartsheet,
    Other,
}

#[derive(Clone, Debug)]
pub struct SheetEntry {
    pub name: String,
    pub part: Option<String>,
    pub kind: SheetKind,
    pub hidden: bool,
}

/// `<definedName>`: `scope` is the index of the sheet it belongs to, if any.
#[derive(Clone, Debug, PartialEq)]
pub struct DefinedName {
    pub name: String,
    pub scope: Option<usize>,
    pub formula: String,
    pub hidden: bool,
}

/// `<calcPr>`: whether Excel iterates circular references, and how far.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct CalcSettings {
    pub iterate: bool,
    pub iterate_count: u32,
    pub iterate_delta: f64,
}

impl Default for CalcSettings {
    fn default() -> Self {
        Self {
            iterate: false,
            iterate_count: 100,
            iterate_delta: 0.001,
        }
    }
}

#[derive(Debug)]
pub struct WorkbookInfo {
    pub part: String,
    pub rels_part: String,
    pub rels: Vec<Rel>,
    pub sheets: Vec<SheetEntry>,
    pub shared_strings: Option<String>,
    pub styles: Option<String>,
    pub calc_chain: Option<Rel>,
    pub date1904: bool,
    pub names: Vec<DefinedName>,
    pub calc: CalcSettings,
}

impl WorkbookInfo {
    pub fn load(pkg: &Package) -> Result<Self, Error> {
        let root = pkg
            .read("_rels/.rels")?
            .ok_or_else(|| Error::Corrupt("no _rels/.rels".into()))?;
        let root = parse_rels(&root)?;
        let wb_rel = root
            .iter()
            .find(|r| r.is("officeDocument"))
            .ok_or_else(|| Error::Corrupt("no officeDocument relationship".into()))?;
        let part = resolve("", &wb_rel.target);
        let xml = pkg
            .read(&part)?
            .ok_or_else(|| Error::Corrupt(format!("missing {part}")))?;
        let rels_part = rels_part_for(&part);
        let rels = match pkg.read(&rels_part)? {
            Some(x) => parse_rels(&x)?,
            None => Vec::new(),
        };
        let by_id: HashMap<&str, &Rel> = rels.iter().map(|r| (r.id.as_str(), r)).collect();

        let mut sheets = Vec::new();
        let mut date1904 = false;
        let mut names = Vec::new();
        let mut calc = CalcSettings::default();
        let (mut r, base) = crate::xmltext::reader(&xml[..]);
        loop {
            let ev = r.read_event()?;
            let end = r.buffer_position() as usize + base;
            match ev {
                Event::Start(e) if e.local_name().as_ref() == "definedName" => {
                    let a = attrs(&e)?;
                    r.read_to_end(e.name())?;
                    let close = r.buffer_position() as usize + base;
                    let inner = &xml[end..close];
                    let inner = &inner[..inner.iter().rposition(|&b| b == b'<').unwrap_or(0)];
                    names.push(DefinedName {
                        name: a.get("name").cloned().unwrap_or_default(),
                        scope: a.get("localSheetId").and_then(|v| v.parse().ok()),
                        formula: crate::xmltext::unescape_xml(&String::from_utf8_lossy(inner)),
                        hidden: a.get("hidden").is_some_and(|v| v == "1" || v == "true"),
                    });
                }
                Event::Empty(e) | Event::Start(e) => match e.local_name().as_ref() {
                    "sheet" => {
                        let a = attrs(&e)?;
                        let rel = a.get("id").and_then(|id| by_id.get(id.as_str()));
                        let kind = match rel {
                            Some(r) if r.is("worksheet") => SheetKind::Worksheet,
                            Some(r) if r.is("chartsheet") => SheetKind::Chartsheet,
                            _ => SheetKind::Other,
                        };
                        sheets.push(SheetEntry {
                            name: a.get("name").cloned().unwrap_or_default(),
                            part: rel.map(|r| resolve(&part, &r.target)),
                            kind,
                            hidden: a.get("state").is_some_and(|s| s != "visible"),
                        });
                    }
                    "workbookPr" => {
                        let a = attrs(&e)?;
                        date1904 = a.get("date1904").is_some_and(|v| v == "1" || v == "true");
                    }
                    "calcPr" => {
                        let a = attrs(&e)?;
                        calc.iterate = a.get("iterate").is_some_and(|v| v == "1" || v == "true");
                        if let Some(n) = a.get("iterateCount").and_then(|v| v.parse().ok()) {
                            calc.iterate_count = n;
                        }
                        if let Some(d) = a.get("iterateDelta").and_then(|v| v.parse().ok()) {
                            calc.iterate_delta = d;
                        }
                    }
                    _ => {}
                },
                Event::Eof => break,
                _ => {}
            }
        }
        let shared_strings = rels
            .iter()
            .find(|r| r.is("sharedStrings"))
            .map(|r| resolve(&part, &r.target));
        let styles = rels
            .iter()
            .find(|r| r.is("styles"))
            .map(|r| resolve(&part, &r.target));
        let calc_chain = rels.iter().find(|r| r.is("calcChain")).cloned();
        Ok(Self {
            part,
            rels_part,
            rels,
            sheets,
            shared_strings,
            styles,
            calc_chain,
            date1904,
            names,
            calc,
        })
    }

    pub fn sheet(&self, name: &str) -> Option<&SheetEntry> {
        self.sheets.iter().find(|s| s.name == name)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn resolves_targets() {
        assert_eq!(
            resolve("xl/workbook.xml", "worksheets/sheet1.xml"),
            "xl/worksheets/sheet1.xml"
        );
        assert_eq!(
            resolve("xl/workbook.xml", "/xl/worksheets/sheet1.xml"),
            "xl/worksheets/sheet1.xml"
        );
        assert_eq!(
            resolve("xl/worksheets/sheet1.xml", "../tables/table1.xml"),
            "xl/tables/table1.xml"
        );
        assert_eq!(resolve("", "xl/workbook.xml"), "xl/workbook.xml");
        assert_eq!(
            rels_part_for("xl/worksheets/sheet1.xml"),
            "xl/worksheets/_rels/sheet1.xml.rels"
        );
    }
}
