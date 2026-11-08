//! Byte comparison of two OPC packages, part by part.

use std::collections::{BTreeMap, BTreeSet};
use std::io::{Cursor, Read};

use zip::ZipArchive;
use zip::result::ZipResult;

/// Parts whose bytes differ between `before` and `after`, or that only one of them has.
pub(crate) fn changed(before: &[u8], after: &[u8]) -> ZipResult<Vec<String>> {
    let (before, mut after) = (parts(before)?, parts(after)?);
    let mut changed: Vec<String> = before
        .into_iter()
        .filter(|(name, bytes)| after.remove(name).as_ref() != Some(bytes))
        .map(|(name, _)| name)
        .collect();
    changed.extend(after.into_keys());
    changed.sort();
    Ok(changed)
}

/// Names of every part in `package`.
pub(crate) fn names(package: &[u8]) -> ZipResult<BTreeSet<String>> {
    let mut archive = ZipArchive::new(Cursor::new(package))?;
    (0..archive.len())
        .filter_map(|i| match archive.by_index(i) {
            Ok(entry) if entry.is_dir() => None,
            Ok(entry) => Some(entry.name().map(|n| n.into_owned())),
            Err(e) => Some(Err(e)),
        })
        .collect()
}

/// The relationships part of `part`: `ppt/slides/slide1.xml` → `ppt/slides/_rels/slide1.xml.rels`.
pub(crate) fn rels_of(part: &str) -> String {
    let (dir, file) = part.rsplit_once('/').unwrap_or(("", part));
    if dir.is_empty() {
        format!("_rels/{file}.rels")
    } else {
        format!("{dir}/_rels/{file}.rels")
    }
}

/// The bytes of one part, if the package has it.
pub(crate) fn read(package: &[u8], name: &str) -> Option<Vec<u8>> {
    let mut archive = ZipArchive::new(Cursor::new(package)).ok()?;
    let mut entry = archive.by_name(name).ok()?;
    let mut bytes = Vec::new();
    entry.read_to_end(&mut bytes).ok()?;
    Some(bytes)
}

/// The notes slide `slide`'s relationships name in `package`, as a part name.
pub(crate) fn notes_of(package: &[u8], slide: &str) -> Option<String> {
    let rels = String::from_utf8(read(package, &rels_of(slide))?).ok()?;
    let rel = rels.split('<').find(|r| r.contains("relationships/notesSlide\""))?;
    let target = rel.split("Target=\"").nth(1)?.split('"').next()?;
    let dir = slide.rsplit_once('/').map_or("", |(d, _)| d);
    let mut path: Vec<&str> = if target.starts_with('/') { Vec::new() } else { dir.split('/').filter(|s| !s.is_empty()).collect() };
    for piece in target.trim_start_matches('/').split('/') {
        match piece {
            ".." => {
                path.pop();
            }
            "." | "" => {}
            p => path.push(p),
        }
    }
    Some(path.join("/"))
}

fn parts(package: &[u8]) -> ZipResult<BTreeMap<String, Vec<u8>>> {
    let mut archive = ZipArchive::new(Cursor::new(package))?;
    let mut parts = BTreeMap::new();
    for i in 0..archive.len() {
        let mut entry = archive.by_index(i)?;
        if entry.is_dir() {
            continue;
        }
        let mut bytes = Vec::with_capacity(entry.size() as usize);
        entry.read_to_end(&mut bytes)?;
        parts.insert(entry.name()?.into_owned(), bytes);
    }
    Ok(parts)
}

#[cfg(test)]
mod tests {
    use std::io::Write;

    use zip::ZipWriter;
    use zip::write::SimpleFileOptions;

    use super::*;

    fn package(parts: &[(&str, &str)]) -> Vec<u8> {
        let mut zip = ZipWriter::new(Cursor::new(Vec::new()));
        for (name, body) in parts {
            zip.start_file(*name, SimpleFileOptions::default()).unwrap();
            zip.write_all(body.as_bytes()).unwrap();
        }
        zip.finish().unwrap().into_inner()
    }

    #[test]
    fn only_differing_parts_are_changed() {
        let before = package(&[("a.xml", "1"), ("b.xml", "2"), ("gone.xml", "3")]);
        let after = package(&[("b.xml", "2"), ("a.xml", "9"), ("new.xml", "4")]);
        assert_eq!(
            changed(&before, &after).unwrap(),
            ["a.xml", "gone.xml", "new.xml"]
        );
        assert!(changed(&before, &before).unwrap().is_empty());
    }

    #[test]
    fn names_and_rels() {
        let pkg = package(&[("a.xml", "1"), ("ppt/b.xml", "2")]);
        assert_eq!(
            names(&pkg).unwrap().into_iter().collect::<Vec<_>>(),
            ["a.xml", "ppt/b.xml"]
        );
        assert_eq!(
            rels_of("ppt/slides/slide1.xml"),
            "ppt/slides/_rels/slide1.xml.rels"
        );
        assert_eq!(
            rels_of("ppt/presentation.xml"),
            "ppt/_rels/presentation.xml.rels"
        );
    }
}
