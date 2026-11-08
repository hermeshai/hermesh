//! Comparing two saves of one document, part by part.

use std::collections::BTreeMap;
use std::io::{Cursor, Read};

/// How `theirs` and `ours`, two `.docx` packages, differ: `None` when they hold the same parts
/// with the same bytes, `dcterms:modified` aside (each save stamps its own clock).
pub fn package_difference(theirs: &[u8], ours: &[u8]) -> Option<String> {
    let (theirs, ours) = match (parts(theirs), parts(ours)) {
        (Ok(a), Ok(b)) => (a, b),
        (Err(e), _) => return Some(format!("the first package does not open: {e}")),
        (_, Err(e)) => return Some(format!("the second package does not open: {e}")),
    };
    let names = |p: &BTreeMap<String, Vec<u8>>| p.keys().cloned().collect::<Vec<_>>();
    if names(&theirs) != names(&ours) {
        let only = |a: &BTreeMap<String, Vec<u8>>, b: &BTreeMap<String, Vec<u8>>| {
            a.keys().filter(|k| !b.contains_key(*k)).cloned().collect::<Vec<_>>().join(", ")
        };
        return Some(format!("parts only in the first: [{}]; only in the second: [{}]", only(&theirs, &ours), only(&ours, &theirs)));
    }
    for (name, a) in &theirs {
        let b = &ours[name];
        let (a, b) = if name == "docProps/core.xml" { (unstamped(a), unstamped(b)) } else { (a.clone(), b.clone()) };
        if a != b {
            let at = a.iter().zip(&b).position(|(x, y)| x != y).unwrap_or(a.len().min(b.len()));
            let around = |p: &[u8]| String::from_utf8_lossy(&p[at.saturating_sub(80)..(at + 120).min(p.len())]).into_owned();
            return Some(format!("{name} differs at byte {at} ({} vs {} bytes): {:?} vs {:?}", a.len(), b.len(), around(&a), around(&b)));
        }
    }
    None
}

fn parts(bytes: &[u8]) -> zip::result::ZipResult<BTreeMap<String, Vec<u8>>> {
    let mut archive = zip::ZipArchive::new(Cursor::new(bytes))?;
    let mut parts = BTreeMap::new();
    for i in 0..archive.len() {
        let mut file = archive.by_index(i)?;
        let name = file.name().map(|n| n.into_owned()).unwrap_or_else(|_| format!("#{i}"));
        let mut data = Vec::new();
        file.read_to_end(&mut data)?;
        parts.insert(name, data);
    }
    Ok(parts)
}

fn unstamped(xml: &[u8]) -> Vec<u8> {
    let text = String::from_utf8_lossy(xml);
    let Some(start) = text.find("<dcterms:modified") else {
        return xml.to_vec();
    };
    let Some(open_end) = text[start..].find('>').map(|i| start + i + 1) else {
        return xml.to_vec();
    };
    let Some(close) = text[open_end..].find("</dcterms:modified>").map(|i| open_end + i) else {
        return xml.to_vec();
    };
    format!("{}{}", &text[..open_end], &text[close..]).into_bytes()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

    fn package(parts: &[(&str, &str)]) -> Vec<u8> {
        let mut out = zip::ZipWriter::new(Cursor::new(Vec::new()));
        for (name, body) in parts {
            out.start_file(*name, zip::write::SimpleFileOptions::default()).unwrap();
            out.write_all(body.as_bytes()).unwrap();
        }
        out.finish().unwrap().into_inner()
    }

    #[test]
    fn the_modified_stamp_is_not_a_difference() {
        let a = package(&[("docProps/core.xml", "<cp><dcterms:modified x=\"1\">2026-01-01</dcterms:modified></cp>"), ("word/document.xml", "<w/>")]);
        let b = package(&[("docProps/core.xml", "<cp><dcterms:modified x=\"1\">2026-11-07</dcterms:modified></cp>"), ("word/document.xml", "<w/>")]);
        assert_eq!(package_difference(&a, &b), None);
    }

    #[test]
    fn a_changed_or_missing_part_is() {
        let a = package(&[("word/document.xml", "<w>a</w>")]);
        let b = package(&[("word/document.xml", "<w>b</w>")]);
        assert!(package_difference(&a, &b).unwrap().starts_with("word/document.xml differs at byte 3"));
        let c = package(&[("word/document.xml", "<w>a</w>"), ("word/x.xml", "")]);
        assert!(package_difference(&a, &c).unwrap().contains("only in the second: [word/x.xml]"));
    }
}
