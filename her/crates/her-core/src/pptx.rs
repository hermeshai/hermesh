//! File transport for the browser PPTX editor. No rasterization happens here.
use std::collections::BTreeSet;
use std::io::{self, Cursor, Read};
use std::path::Path;

use base64::{Engine as _, engine::general_purpose::STANDARD};
use her_protocol::files::{FileWriteStatus, FilesWriteResult};
use her_protocol::pptx::{PptxReadResult, PptxReadStage};
use quick_xml::{Reader, events::Event};
use zip::ZipArchive;

use crate::{files::{self, FileScope}, paths::HerHome};

// Base64 plus the RPC envelope must fit the default 32 MiB frame.
pub const MAX_BYTES: u64 = 20 * 1024 * 1024;
const MAX_XML: u64 = 8 * 1024 * 1024;

fn invalid(message: impl ToString) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, message.to_string())
}

fn check_path(path: &Path) -> io::Result<()> {
    if !path.extension().is_some_and(|ext| ext.eq_ignore_ascii_case("pptx")) {
        return Err(invalid("only .pptx files can be opened in the presentation editor"));
    }
    Ok(())
}

fn package(bytes: &[u8]) -> io::Result<ZipArchive<Cursor<&[u8]>>> {
    let mut zip = ZipArchive::new(Cursor::new(bytes)).map_err(|_| invalid("not a PPTX package (encrypted presentations are not supported)"))?;
    for name in ["[Content_Types].xml", "ppt/presentation.xml"] {
        let mut part = zip.by_name(name).map_err(invalid)?;
        if part.size() > MAX_XML { return Err(invalid("presentation XML is too large")); }
        // Read required members, checking their decompression and CRC before accepting a save.
        io::copy(&mut part, &mut io::sink()).map_err(invalid)?;
    }
    Ok(zip)
}

/// The font families a package names, plus the host's fallbacks: what an editor loads.
pub fn font_families_of(bytes: &[u8]) -> io::Result<Vec<String>> {
    font_families(&mut package(bytes)?, "ppt/", &mut |_, _, _| {})
}

/// The same for a Word document (word-design §6): its runs' fonts (`w:rFonts`) and its theme's.
pub fn docx_font_families_of(bytes: &[u8]) -> io::Result<Vec<String>> {
    let mut zip = ZipArchive::new(Cursor::new(bytes)).map_err(|_| invalid("not a DOCX package (encrypted documents are not supported)"))?;
    font_families(&mut zip, "word/", &mut |_, _, _| {})
}

fn font_families(zip: &mut ZipArchive<Cursor<&[u8]>>, parts: &str, progress: &mut impl FnMut(PptxReadStage, u64, u64)) -> io::Result<Vec<String>> {
    let mut names = BTreeSet::from(["Arial".to_owned(), "PingFang SC".to_owned()]);
    let mut budget = 32 * 1024 * 1024;
    let total = zip.len() as u64;
    for i in 0..zip.len() {
        if i % 16 == 0 { progress(PptxReadStage::Inspecting, i as u64, total); }
        let mut entry = zip.by_index(i).map_err(invalid)?;
        let name = entry.name().map_err(invalid)?;
        if !name.starts_with(parts) || !name.ends_with(".xml") { continue; }
        if entry.size() > MAX_XML || entry.size() > budget { return Err(invalid("presentation XML exceeds the read limit")); }
        budget -= entry.size();
        let mut bytes = Vec::new();
        entry.read_to_end(&mut bytes)?;
        let mut reader = Reader::from_reader(bytes.as_slice());
        loop {
            match reader.read_event().map_err(invalid)? {
                Event::Start(e) | Event::Empty(e) => {
                    // Themes also list dozens of supplemental script fonts. Retain Hans
                    // (our default East Asian language), plus the explicit font slots.
                    let local = e.local_name();
                    let hans = local.as_ref() == "font" && e.attributes().flatten()
                        .any(|a| a.key.as_ref() == "script" && a.value.as_ref() == "Hans");
                    let run_fonts = local.as_ref() == "rFonts";
                    if !matches!(local.as_ref(), "latin" | "ea" | "cs") && !hans && !run_fonts { continue; }
                    for a in e.attributes() {
                        let a = a.map_err(invalid)?;
                        let named = if run_fonts { matches!(a.key.local_name().as_ref(), "ascii" | "hAnsi" | "eastAsia" | "cs") } else { a.key.as_ref() == "typeface" };
                        if !named { continue; }
                        let family = a.normalized_value(quick_xml::XmlVersion::Implicit1_0).map_err(invalid)?;
                        let family = family.trim();
                        if !family.is_empty() && !family.starts_with('+') && family.len() <= 200 && names.len() < 64 {
                            names.insert(family.to_owned());
                        }
                    }
                }
                Event::Eof => break,
                _ => {}
            }
        }
    }
    progress(PptxReadStage::Inspecting, total, total);
    Ok(names.into_iter().collect())
}

pub fn read(home: &HerHome, path: &Path) -> io::Result<PptxReadResult> {
    read_with_progress(home, path, |_, _, _| {})
}

pub fn read_with_progress(home: &HerHome, path: &Path, mut progress: impl FnMut(PptxReadStage, u64, u64)) -> io::Result<PptxReadResult> {
    check_path(path)?;
    let meta = std::fs::metadata(path)?;
    if !meta.is_file() || meta.len() > MAX_BYTES { return Err(invalid("PPTX files must be at most 20 MiB")); }
    let mut bytes = Vec::new();
    let mut file = std::fs::File::open(path)?.take(MAX_BYTES + 1);
    let mut chunk = [0; 256 * 1024];
    progress(PptxReadStage::Reading, 0, meta.len());
    loop {
        let count = file.read(&mut chunk)?;
        if count == 0 { break; }
        bytes.extend_from_slice(&chunk[..count]);
        progress(PptxReadStage::Reading, bytes.len() as u64, meta.len().max(bytes.len() as u64));
    }
    if bytes.len() as u64 > MAX_BYTES { return Err(invalid("PPTX files must be at most 20 MiB")); }
    progress(PptxReadStage::Inspecting, 0, 0);
    let fonts = font_families(&mut package(&bytes)?, "ppt/", &mut progress)?;
    progress(PptxReadStage::Encoding, 0, 0);
    Ok(PptxReadResult { path: path.to_path_buf(), hash: files::hash(&bytes), data: STANDARD.encode(bytes),
        writable: !meta.permissions().readonly() && !files::refused(home, path), font_families: fonts })
}

pub async fn write(scope: &FileScope, home: &HerHome, path: &Path, data: &str, base: &str) -> io::Result<FilesWriteResult> {
    check_path(path)?;
    if files::refused(home, path) {
        return Ok(FilesWriteResult { status: FileWriteStatus::Refused, hash: None, message: Some("her's own data is not written from here".into()) });
    }
    if data.len() as u64 > MAX_BYTES.div_ceil(3) * 4 { return Err(invalid("PPTX files must be at most 20 MiB")); }
    let bytes = STANDARD.decode(data).map_err(invalid)?;
    if bytes.len() as u64 > MAX_BYTES { return Err(invalid("PPTX files must be at most 20 MiB")); }
    package(&bytes)?;
    // Validate the browser's output with the native parser before replacing the source.
    her_slide::Deck::open(bytes.clone()).map_err(invalid)?;
    let _guard = scope.locks.lock(path).await;
    let current = match tokio::fs::read(path).await {
        Ok(current) => Some(files::hash(&current)),
        Err(e) if e.kind() == io::ErrorKind::NotFound => None,
        Err(e) => return Err(e),
    };
    if current.as_deref() != Some(base) {
        return Ok(FilesWriteResult { status: FileWriteStatus::Conflict, hash: current, message: None });
    }
    let hash = files::hash(&bytes);
    scope.env.write_file(path, bytes).await?;
    Ok(FilesWriteResult { status: FileWriteStatus::Saved, hash: Some(hash), message: None })
}

/// Replay a single legacy vector asset as SVG, retaining all original package bytes.
pub fn metafile(data: &str) -> io::Result<her_protocol::pptx::PptxMetafileResult> {
    if data.len() as u64 > MAX_BYTES.div_ceil(3) * 4 { return Err(invalid("metafile exceeds 20 MiB")); }
    let bytes = STANDARD.decode(data).map_err(invalid)?;
    if bytes.len() as u64 > MAX_BYTES { return Err(invalid("metafile exceeds 20 MiB")); }
    let converted = ooxml_metafile::to_svg(&bytes).map_err(invalid)?;
    Ok(her_protocol::pptx::PptxMetafileResult { svg: converted.markup,
        omissions: converted.omissions.iter().map(|omission| format!("{omission:?}")).collect() })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;
    use her_tools::{LocalEnvironment, PathLocks};

    const DECK: &[u8] = include_bytes!("../../her-slide/tests/fixtures/editor.pptx");

    #[test]
    fn read_progress_reports_actual_bytes_and_package_entries() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        let path = dir.path().join("progress.pptx");
        std::fs::write(&path, DECK).unwrap();
        let mut events = Vec::new();
        let file = read_with_progress(&home, &path, |stage, completed, total| events.push((stage, completed, total))).unwrap();
        assert_eq!(STANDARD.decode(file.data).unwrap(), DECK);
        let readings: Vec<_> = events.iter().filter(|e| matches!(e.0, PptxReadStage::Reading)).collect();
        assert_eq!(readings.first().unwrap().1, 0);
        assert_eq!(readings.last().unwrap().1, DECK.len() as u64);
        assert_eq!(readings.last().unwrap().2, DECK.len() as u64);
        let entries = ZipArchive::new(Cursor::new(DECK)).unwrap().len() as u64;
        let inspected = events.iter().rfind(|e| matches!(e.0, PptxReadStage::Inspecting)).unwrap();
        assert_eq!((inspected.1, inspected.2), (entries, entries));
        assert!(matches!(events.last().unwrap().0, PptxReadStage::Encoding));
        std::fs::write(&path, b"not a presentation").unwrap();
        assert!(read_with_progress(&home, &path, |stage, _, _| {
            assert!(!matches!(stage, PptxReadStage::Encoding), "invalid files must never reach transfer");
        }).is_err());
    }

    #[test]
    fn legacy_chart_preview_replays_as_vectors_and_text() {
        let bytes = include_bytes!("../../her-slide/tests/fixtures/ole-chart.emf");
        let converted = metafile(&STANDARD.encode(bytes)).unwrap();
        assert!(converted.svg.contains("<svg"));
        assert!(converted.svg.contains("<path") || converted.svg.contains("<rect"));
        assert!(converted.svg.contains("Q1") && converted.svg.contains("North"));
        assert!(converted.omissions.is_empty(), "{:?}", converted.omissions);
        assert!(metafile("not base64!").is_err());
        assert!(metafile(&STANDARD.encode(b"not an emf")).is_err());
    }

    #[tokio::test]
    async fn browser_bytes_round_trip_without_overwriting_external_edits() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        let scope = FileScope { env: Arc::new(LocalEnvironment::new(dir.path().to_owned())), locks: Arc::new(PathLocks::new()) };
        let path = dir.path().join("deck.PPTX");
        std::fs::write(&path, DECK).unwrap();
        let opened = read(&home, &path).unwrap();
        assert!(opened.font_families.contains(&"Arial".to_owned()));
        let docx = docx_font_families_of(include_bytes!("../../her-word/tests/fixtures/report.docx")).unwrap();
        assert!(docx.contains(&"PingFang SC".to_owned()) && docx.len() > 2, "{docx:?}");
        assert!(opened.writable);
        let deck = her_slide::Deck::open(STANDARD.decode(&opened.data).unwrap()).unwrap();
        assert_eq!(deck.slide_count(), 2);
        assert_eq!(deck.replace_text("Quarterly review", "Edited title").unwrap(), 1);
        let edited = deck.save().unwrap();
        let saved = write(&scope, &home, &path, &STANDARD.encode(&edited), &opened.hash).await.unwrap();
        assert_eq!(saved.status, FileWriteStatus::Saved);
        let reopened = her_slide::Deck::open(std::fs::read(&path).unwrap()).unwrap();
        assert!(reopened.markdown().unwrap().contains("Edited title"));

        // An agent/another application writes before the browser's next save.
        let guard = scope.locks.lock(&path).await;
        let pending = {
            let (scope, home, path, edited, base) = (scope.clone(), home.clone(), path.clone(), edited.clone(), saved.hash.unwrap());
            tokio::spawn(async move { write(&scope, &home, &path, &STANDARD.encode(edited), &base).await.unwrap() })
        };
        tokio::task::yield_now().await;
        assert!(!pending.is_finished());
        std::fs::write(&path, DECK).unwrap();
        drop(guard);
        assert_eq!(pending.await.unwrap().status, FileWriteStatus::Conflict);
        assert_eq!(std::fs::read(&path).unwrap(), DECK);
        std::fs::remove_file(&path).unwrap();
        assert_eq!(write(&scope, &home, &path, &STANDARD.encode(edited), &opened.hash).await.unwrap().status, FileWriteStatus::Conflict);
        assert!(!path.exists());
    }

    #[tokio::test]
    async fn invalid_packages_and_protected_files_cannot_be_saved() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        let scope = FileScope { env: Arc::new(LocalEnvironment::new(dir.path().to_owned())), locks: Arc::new(PathLocks::new()) };
        let path = dir.path().join("deck.pptx");
        std::fs::write(&path, DECK).unwrap();
        let base = files::hash(DECK);
        assert!(write(&scope, &home, &path, "not base64!", &base).await.is_err());
        assert!(write(&scope, &home, &path, &STANDARD.encode(b"not a zip"), &base).await.is_err());
        assert_eq!(std::fs::read(&path).unwrap(), DECK);
        assert!(read(&home, &dir.path().join("deck.ppt")).is_err());
        let protected = home.sessions_dir().join("deck.pptx");
        assert_eq!(write(&scope, &home, &protected, &STANDARD.encode(DECK), &base).await.unwrap().status, FileWriteStatus::Refused);
    }
}
