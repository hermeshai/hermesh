//! Faces for BetterOffice from the machine itself; the only fonts compiled in are Carlito and
//! Caladea (SIL OFL, `fonts/`), metric-compatible with Calibri and Cambria.
//!
//! A family resolves to an installed face, including fonts in Office's app bundles, then the
//! bundled Carlito/Caladea for Calibri/Cambria, then a macOS stand-in for other missing
//! families, then Helvetica Neue, and finally the bundled Carlito.

use std::collections::HashMap;
use std::fs::File;
use std::io::{Read, Seek, SeekFrom};
use std::path::{Path, PathBuf};
use std::sync::{Arc, OnceLock};

use betteroffice_pptx::fonts::{
    FontError, FontFaceHandle, FontPack, FontProvider, LastResortOffice,
};

const OFFICE_APPS: [&str; 3] = ["PowerPoint", "Word", "Excel"];
/// Office apps whose bundled `DFonts` (Calibri, Cambria, Wingdings, Symbol, ...) are read in place.
const OFFICE_FONT_APPS: [&str; 4] = ["PowerPoint", "Word", "Excel", "Outlook"];
/// The largest standalone face the host will send to the editor (the RPC limit is 20 MiB).
const MAX_FACE_BYTES: u64 = 16 << 20;
/// The glyph-fallback face is only drawn natively, where the engine's limit is 32 MiB.
const MAX_FALLBACK_BYTES: u64 = 30 << 20;
const ASSETS: &str = "/System/Library/AssetsV2";
/// PingFang SC's faces in PingFang.ttc.
const PINGFANG_REGULAR: usize = 3;
const PINGFANG_SEMIBOLD: usize = 11;
/// Families the machine may lack, and the macOS family drawn instead.
const STAND_INS: [(&str, &str); 9] = [
    ("aptos", "Helvetica Neue"),
    ("segoe ui", "Helvetica Neue"),
    ("candara", "Helvetica Neue"),
    ("corbel", "Helvetica Neue"),
    ("tahoma", "Verdana"),
    ("constantia", "Georgia"),
    ("consolas", "Menlo"),
    ("liberation sans", "Arial"),
    ("liberation serif", "Times New Roman"),
];

/// Carlito (Calibri's metrics) and Caladea (Cambria's), each as regular, bold, italic, bold italic.
const CARLITO: [&[u8]; 4] = [
    include_bytes!("../fonts/Carlito-Regular.ttf"),
    include_bytes!("../fonts/Carlito-Bold.ttf"),
    include_bytes!("../fonts/Carlito-Italic.ttf"),
    include_bytes!("../fonts/Carlito-BoldItalic.ttf"),
];
const CALADEA: [&[u8]; 4] = [
    include_bytes!("../fonts/Caladea-Regular.ttf"),
    include_bytes!("../fonts/Caladea-Bold.ttf"),
    include_bytes!("../fonts/Caladea-Italic.ttf"),
    include_bytes!("../fonts/Caladea-BoldItalic.ttf"),
];

/// The bundled metric-compatible face drawn for a family Office would have: Calibri (and
/// Light, which has no face of its own here) and Carlito itself; Cambria and Caladea.
fn bundled_face(family: &str, bold: bool, italic: bool) -> Option<FontFaceHandle> {
    let faces = match family.trim().to_lowercase().as_str() {
        "calibri" | "calibri light" | "carlito" => &CARLITO,
        "cambria" | "caladea" => &CALADEA,
        _ => return None,
    };
    let bytes: &'static [u8] = faces[usize::from(bold) + 2 * usize::from(italic)];
    // Registered under the document's own name, so its runs find the face.
    Some(FontFaceHandle::new(family, bold, italic, move || Ok(bytes.to_vec())))
}

const LAST_RESORT: [&str; 3] = ["Helvetica Neue", "Helvetica", "Arial"];

pub struct SystemFonts;

impl SystemFonts {
    pub fn new() -> Result<Arc<Self>, FontError> {
        Ok(Arc::new(Self))
    }

    /// A standalone local face for the browser WASM engine and Canvas painter.
    pub fn face_bytes(&self, family: &str, bold: bool, italic: bool) -> Result<Vec<u8>, FontError> {
        self.resolve_family(family, bold, italic)
            .unwrap_or_else(|| self.resolve_last_resort(family, bold, italic, LastResortOffice::default()))
            .load()
    }
}

/// One installed family as a font menu shows it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SystemFamily {
    /// The English (or only) family name: what a slide stores and what resolves it.
    pub family: String,
    /// The Simplified Chinese name, when the font has one (华文行楷 for STXingkai).
    pub localized: Option<String>,
}

impl SystemFonts {
    /// Every family the machine can draw in the editor, sorted by name. Hidden system
    /// families (`.SF NS`, ...) and families whose every face is too big to send are left out.
    pub fn families(&self) -> Vec<SystemFamily> {
        catalog().families.clone()
    }
}

impl FontProvider for SystemFonts {
    fn resolve_family(&self, family: &str, bold: bool, italic: bool) -> Option<FontFaceHandle> {
        office_face(family, bold)
            .or_else(|| installed_face(family, bold, italic))
            .or_else(|| bundled_face(family, bold, italic))
            .or_else(|| {
                let lower = family.trim().to_lowercase();
                STAND_INS
                    .iter()
                    .find(|(name, _)| *name == lower)
                    .and_then(|(_, stand_in)| installed_face(stand_in, bold, italic))
            })
            .or_else(|| {
                if !is_cjk_family(family) {
                    return None;
                }
                cjk_stand_ins(family)
                    .iter()
                    .find_map(|name| installed_face(name, bold, italic))
                    .or_else(|| pingfang(family, bold))
            })
    }

    fn resolve_script_fallback(
        &self,
        script: &str,
        bold: bool,
        _italic: bool,
    ) -> Option<FontFaceHandle> {
        if script.starts_with("cjk") {
            return pingfang("PingFang SC", bold).or_else(|| {
                cjk_stand_ins("PingFang SC")
                    .iter()
                    .find_map(|name| installed_face(name, bold, false))
            });
        }
        None
    }

    fn resolve_glyph_fallback(
        &self,
        _family: &str,
        bold: bool,
        italic: bool,
        _office: LastResortOffice,
    ) -> Option<FontFaceHandle> {
        installed_face_within("Arial Unicode MS", bold, italic, MAX_FALLBACK_BYTES)
    }

    fn resolve_last_resort(
        &self,
        family: &str,
        bold: bool,
        italic: bool,
        _office: LastResortOffice,
    ) -> FontFaceHandle {
        LAST_RESORT
            .iter()
            .find_map(|name| installed_face(name, bold, italic))
            // A machine with none of them (Linux CI, a bare container) still draws: Carlito is compiled in.
            .unwrap_or_else(|| {
                let bytes: &'static [u8] = CARLITO[usize::from(bold) + 2 * usize::from(italic)];
                FontFaceHandle::new(family, bold, italic, move || Ok(bytes.to_vec()))
            })
    }

    /// `None`: with packs named, the font manager drops every face outside them, and the
    /// machine's faces would draw as boxes.
    fn bundled_packs(&self) -> Option<Vec<FontPack>> {
        None
    }
}

/// One face of a font file, as its name and OS/2 tables describe it.
#[derive(Clone, Debug)]
struct Installed {
    path: PathBuf,
    index: usize,
    bold: bool,
    italic: bool,
    /// Bytes of the face as a standalone font.
    size: u64,
}

/// The face for `family` among the installed fonts: the asked-for style, else the regular
/// weight of the same slant (the rasterizer emboldens it), else any face of the family.
fn installed_face(family: &str, bold: bool, italic: bool) -> Option<FontFaceHandle> {
    installed_face_within(family, bold, italic, MAX_FACE_BYTES)
}

/// Like [`installed_face`], skipping faces larger than `cap` bytes so the next candidate is tried.
fn installed_face_within(
    family: &str,
    bold: bool,
    italic: bool,
    cap: u64,
) -> Option<FontFaceHandle> {
    let faces: Vec<&Installed> = installed()
        .get(&family.trim().to_lowercase())?
        .iter()
        .filter(|f| f.size <= cap)
        .collect();
    let pick = faces
        .iter()
        .find(|f| f.bold == bold && f.italic == italic)
        .or_else(|| faces.iter().find(|f| !f.bold && f.italic == italic))
        .or_else(|| faces.iter().find(|f| !f.bold && !f.italic))
        .or_else(|| faces.first())?;
    Some(file_face(
        family,
        bold,
        italic,
        pick.path.clone(),
        pick.index,
    ))
}

/// Every installed face by lower-cased family name, built once. Only each file's table
/// directory and its name and OS/2 tables are read, not the whole font.
fn installed() -> &'static HashMap<String, Vec<Installed>> {
    &catalog().index
}

struct Catalog {
    index: HashMap<String, Vec<Installed>>,
    families: Vec<SystemFamily>,
}

fn catalog() -> &'static Catalog {
    static CATALOG: OnceLock<Catalog> = OnceLock::new();
    CATALOG.get_or_init(|| {
        let mut dirs: Vec<PathBuf> = Vec::new();
        if let Some(home) = std::env::var_os("HOME") {
            dirs.push(Path::new(&home).join("Library/Fonts"));
        }
        dirs.extend(
            [
                "/Library/Fonts",
                "/System/Library/Fonts/Supplemental",
                "/System/Library/Fonts",
            ]
            .map(PathBuf::from),
        );
        // Fonts the user downloaded in Font Book (Kaiti SC, Lantinghei SC, ...). Older
        // catalogues sort first, so the newest copy of a family is indexed last.
        dirs.extend(asset_font_dirs());
        // Office's own fonts come after the machine's: they fill in what it lacks (Calibri,
        // Cambria, Wingdings, ...) before any stand-in is drawn.
        for app in OFFICE_FONT_APPS {
            for sub in ["DFonts", "Fonts"] {
                dirs.push(PathBuf::from(format!(
                    "/Applications/Microsoft {app}.app/Contents/Resources/{sub}"
                )));
            }
        }
        let mut index: HashMap<String, Vec<Installed>> = HashMap::new();
        let mut families: Vec<SystemFamily> = Vec::new();
        let mut listed = std::collections::HashSet::new();
        for dir in dirs {
            let Ok(entries) = std::fs::read_dir(&dir) else {
                continue;
            };
            let mut files: Vec<PathBuf> = entries.flatten().map(|e| e.path()).collect();
            files.sort();
            for path in files {
                let ext = path
                    .extension()
                    .and_then(|e| e.to_str())
                    .map(str::to_lowercase);
                if !matches!(ext.as_deref(), Some("ttf" | "otf" | "ttc")) {
                    continue;
                }
                let Some(faces) = describe(&path) else {
                    continue;
                };
                for (i, (names, bold, italic, size, label)) in faces.into_iter().enumerate() {
                    if let Some(family) = label.filter(|f| size <= MAX_FACE_BYTES && listable(&f.family))
                        && listed.insert(family.family.to_lowercase())
                    {
                        families.push(family);
                    }
                    for name in names {
                        let list = index.entry(name).or_default();
                        // Keep later candidates too: an earlier face may exceed the size cap.
                        list.push(Installed {
                            path: path.clone(),
                            index: i,
                            bold,
                            italic,
                            size,
                        });
                    }
                }
            }
        }
        families.sort_by_key(|f| f.family.to_lowercase());
        Catalog { index, families }
    })
}

/// `AssetData` folders of downloaded font assets under `AssetsV2`, oldest catalogue first.
fn asset_font_dirs() -> Vec<PathBuf> {
    let Ok(kinds) = std::fs::read_dir(ASSETS) else {
        return Vec::new();
    };
    let mut kinds: Vec<PathBuf> = kinds
        .flatten()
        .filter(|kind| kind.file_name().to_string_lossy().starts_with("com_apple_MobileAsset_Font"))
        .map(|kind| kind.path())
        .collect();
    kinds.sort();
    let mut dirs = Vec::new();
    for kind in kinds {
        let Ok(assets) = std::fs::read_dir(&kind) else { continue };
        let mut found: Vec<PathBuf> = assets.flatten().map(|a| a.path().join("AssetData")).filter(|p| p.is_dir()).collect();
        found.sort();
        dirs.extend(found);
    }
    dirs
}

/// Families a menu offers: not hidden (`.SF NS`, `System Font`) or private (`#GungSeo`) system faces.
fn listable(family: &str) -> bool {
    !family.is_empty() && !family.starts_with(['.', '#']) && family != "System Font"
}

/// Each face of a font file: its family names (every language), bold and italic.
type Described = (Vec<String>, bool, bool, u64, Option<SystemFamily>);

fn describe(path: &Path) -> Option<Vec<Described>> {
    let mut file = File::open(path).ok()?;
    let mut read_at = |at: u64, len: usize| -> Option<Vec<u8>> {
        file.seek(SeekFrom::Start(at)).ok()?;
        let mut buf = vec![0; len];
        file.read_exact(&mut buf).ok()?;
        Some(buf)
    };
    let head = read_at(0, 12)?;
    let offsets: Vec<u64> = if &head[0..4] == b"ttcf" {
        let count = be32(&head, 8)? as usize;
        let table = read_at(12, 4 * count.min(256))?;
        (0..count.min(256))
            .filter_map(|i| be32(&table, 4 * i).map(u64::from))
            .collect()
    } else {
        vec![0]
    };
    let mut faces = Vec::new();
    for offset in offsets {
        let dir = read_at(offset, 12)?;
        let tables = be16(&dir, 4)? as usize;
        let records = read_at(offset + 12, 16 * tables)?;
        let mut name = None;
        let mut os2 = None;
        let mut size = 12 + 16 * tables as u64;
        for r in records.as_chunks::<16>().0 {
            let (at, len) = (u64::from(be32(r, 8)?), be32(r, 12)? as usize);
            size += (len as u64).next_multiple_of(4);
            match &r[0..4] {
                b"name" => name = Some((at, len)),
                b"OS/2" => os2 = Some((at, len.min(64))),
                _ => {}
            }
        }
        let table = name.and_then(|(at, len)| read_at(at, len));
        let names = table.as_deref().map(family_names).unwrap_or_default();
        let label = table.as_deref().and_then(family_label);
        let (bold, italic) = os2
            .and_then(|(at, len)| read_at(at, len))
            .and_then(|t| Some((be16(&t, 4)?, be16(&t, 62)?)))
            .map(|(weight, selection)| {
                (
                    weight >= 600 || selection & 0x20 != 0,
                    selection & 0x01 != 0,
                )
            })
            .unwrap_or((false, false));
        faces.push((names, bold, italic, size, label));
    }
    Some(faces)
}

/// The family names (IDs 1 and 16) of a `name` table, lower-cased, in every language.
fn family_names(table: &[u8]) -> Vec<String> {
    let mut names = Vec::new();
    for (_, _, text) in family_records(table) {
        let text = text.to_lowercase();
        if !names.contains(&text) {
            names.push(text);
        }
    }
    names
}

/// The family as a menu names it: the typographic family (ID 16) over the style-linked one
/// (ID 1), in English and, if present, Simplified Chinese.
fn family_label(table: &[u8]) -> Option<SystemFamily> {
    let records = family_records(table);
    let pick = |id: u16, lang: Lang| records.iter().find(|r| r.0 == id && r.1 == lang).map(|r| r.2.clone());
    let any = |id: u16| records.iter().find(|r| r.0 == id).map(|r| r.2.clone());
    let family = pick(16, Lang::English).or_else(|| pick(1, Lang::English)).or_else(|| any(16)).or_else(|| any(1))?;
    let localized = pick(16, Lang::Chinese).or_else(|| pick(1, Lang::Chinese)).filter(|name| *name != family);
    Some(SystemFamily { family, localized })
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Lang {
    English,
    Chinese,
    Other,
}

/// Mac (platform 1) and Windows (platform 3) language IDs of English and Simplified Chinese.
fn lang(platform: u16, language: u16) -> Lang {
    match (platform, language) {
        (1, 0) | (3, 0x0409) => Lang::English,
        (1, 33) | (3, 0x0804 | 0x1004) => Lang::Chinese,
        _ => Lang::Other,
    }
}

/// Each family record (ID 1 or 16) of a `name` table: its ID, language and trimmed text.
fn family_records(table: &[u8]) -> Vec<(u16, Lang, String)> {
    let mut names = Vec::new();
    let (Some(count), Some(strings)) = (be16(table, 2), be16(table, 4)) else {
        return names;
    };
    for i in 0..count as usize {
        let r = 6 + 12 * i;
        let (Some(platform), Some(language), Some(id), Some(len), Some(at)) = (
            be16(table, r),
            be16(table, r + 4),
            be16(table, r + 6),
            be16(table, r + 8),
            be16(table, r + 10),
        ) else {
            break;
        };
        if id != 1 && id != 16 {
            continue;
        }
        let start = strings as usize + at as usize;
        let Some(bytes) = table.get(start..start + len as usize) else {
            continue;
        };
        let text = match platform {
            0 | 3 => String::from_utf16_lossy(
                &bytes
                    .as_chunks::<2>()
                    .0
                    .iter()
                    .map(|c| u16::from_be_bytes([c[0], c[1]]))
                    .collect::<Vec<_>>(),
            ),
            // Mac Simplified Chinese records are GB2312; other Mac records are Mac Roman.
            1 if language == 33 => encoding_rs::GBK.decode_without_bom_handling(bytes).0.into_owned(),
            1 => bytes.iter().map(|&b| b as char).collect(),
            _ => continue,
        };
        let text = text.trim().to_string();
        if !text.is_empty() {
            names.push((id, lang(platform, language), text));
        }
    }
    names
}

fn be16(b: &[u8], at: usize) -> Option<u16> {
    Some(u16::from_be_bytes(b.get(at..at + 2)?.try_into().ok()?))
}

fn be32(b: &[u8], at: usize) -> Option<u32> {
    Some(u32::from_be_bytes(b.get(at..at + 4)?.try_into().ok()?))
}

/// Office's file for a CJK family: its regular face and, if Office has one, its bold face.
fn office_files(family: &str) -> Option<(&'static str, Option<&'static str>)> {
    let lower = family.to_lowercase();
    let is = |names: &[&str]| names.iter().any(|n| lower.contains(n));
    if is(&["microsoft yahei", "微软雅黑"]) {
        Some(("msyh.ttf", Some("msyhbd.ttf")))
    } else if is(&["dengxian", "等线"]) {
        Some(("Deng.ttf", Some("Dengb.ttf")))
    } else if is(&["simhei", "黑体"]) {
        Some(("SimHei.ttf", None))
    } else if is(&["simsun", "宋体"]) {
        Some(("SimSun.ttf", None))
    } else {
        None
    }
}

fn office_face(family: &str, bold: bool) -> Option<FontFaceHandle> {
    let (regular, bold_file) = office_files(family)?;
    let file = if bold {
        bold_file.unwrap_or(regular)
    } else {
        regular
    };
    let path = OFFICE_APPS
        .iter()
        .map(|app| {
            PathBuf::from(format!(
                "/Applications/Microsoft {app}.app/Contents/Resources/Fonts/{file}"
            ))
        })
        .find(|path| {
            describe(path)
                .and_then(|faces| faces.first().map(|face| face.3 <= MAX_FACE_BYTES))
                .unwrap_or(false)
        })?;
    Some(file_face(
        family,
        bold && bold_file.is_some(),
        false,
        path,
        0,
    ))
}

fn pingfang(family: &str, bold: bool) -> Option<FontFaceHandle> {
    let path = pingfang_path()?.clone();
    let index = if bold {
        PINGFANG_SEMIBOLD
    } else {
        PINGFANG_REGULAR
    };
    if describe(&path)?.get(index)?.3 > MAX_FACE_BYTES {
        return None;
    }
    Some(file_face(family, bold, false, path, index))
}

/// PingFang.ttc: a downloadable asset under a hashed directory of `AssetsV2`, or in
/// `/System/Library/Fonts` on older systems.
fn pingfang_path() -> Option<&'static PathBuf> {
    static PATH: OnceLock<Option<PathBuf>> = OnceLock::new();
    PATH.get_or_init(|| {
        let legacy = PathBuf::from("/System/Library/Fonts/PingFang.ttc");
        if legacy.exists() {
            return Some(legacy);
        }
        let mut found: Vec<PathBuf> = std::fs::read_dir(ASSETS)
            .ok()?
            .flatten()
            .filter(|kind| {
                kind.file_name()
                    .to_string_lossy()
                    .starts_with("com_apple_MobileAsset_Font")
            })
            .flat_map(|kind| {
                std::fs::read_dir(kind.path())
                    .into_iter()
                    .flatten()
                    .flatten()
            })
            .map(|asset| asset.path().join("AssetData/PingFang.ttc"))
            .filter(|path| path.exists())
            .collect();
        // The newest asset catalog sorts last (Font7, Font8, ...).
        found.sort();
        found.pop()
    })
    .as_ref()
}

fn file_face(
    family: &str,
    bold: bool,
    italic: bool,
    path: PathBuf,
    index: usize,
) -> FontFaceHandle {
    FontFaceHandle::new(family, bold, italic, move || load_face(&path, index))
}

fn load_face(path: &Path, index: usize) -> Result<Vec<u8>, FontError> {
    let bytes = std::fs::read(path)
        .map_err(|e| FontError::InvalidFontOptions(format!("{}: {e}", path.display())))?;
    collection_face(&bytes, index).ok_or_else(|| {
        FontError::InvalidFontOptions(format!("{}: no face {index}", path.display()))
    })
}

fn is_cjk_family(family: &str) -> bool {
    let lower = family.to_lowercase();
    const NAMES: [&str; 15] = [
        "pingfang",
        "苹方",
        "思源",
        "source han",
        "noto sans sc",
        "noto sans cjk",
        "hiragino",
        "stheiti",
        "heiti",
        "songti",
        "kaiti",
        "fangsong",
        "stsong",
        "stkaiti",
        "stfangsong",
    ];
    NAMES.iter().any(|n| lower.contains(n))
        || family
            .chars()
            .any(|c| ('\u{4e00}'..='\u{9fff}').contains(&c))
}

/// macOS families to try, in order, for a Chinese family the machine lacks or ships too large.
fn cjk_stand_ins(family: &str) -> &'static [&'static str] {
    let lower = family.to_lowercase();
    let is = |names: &[&str]| names.iter().any(|n| lower.contains(n));
    if is(&["kaiti", "楷"]) {
        &["Kaiti SC", "STKaiti"]
    } else if is(&["fangsong", "仿宋"]) {
        &["STFangsong", "Songti SC", "STSong"]
    } else if is(&["songti", "simsun", "stsong", "宋"]) {
        &["Songti SC", "STSong"]
    } else {
        &["Heiti SC", "STHeiti"]
    }
}

/// Face `index` of a TrueType/OpenType collection as a standalone font; a plain font is its
/// own face 0.
pub(crate) fn collection_face(file: &[u8], index: usize) -> Option<Vec<u8>> {
    let u32_at = |at: usize| Some(u32::from_be_bytes(file.get(at..at + 4)?.try_into().ok()?));
    let u16_at = |at: usize| Some(u16::from_be_bytes(file.get(at..at + 2)?.try_into().ok()?));
    if file.get(0..4)? != b"ttcf" {
        return (index == 0).then(|| file.to_vec());
    }
    if index >= u32_at(8)? as usize {
        return None;
    }
    let offset = u32_at(12 + 4 * index)? as usize;
    let tables = u16_at(offset + 4)? as usize;
    let header_len = 12 + 16 * tables;
    let mut records = file.get(offset..offset + 12)?.to_vec();
    let mut data = Vec::new();
    for i in 0..tables {
        let record = offset + 12 + 16 * i;
        let (start, len) = (u32_at(record + 8)? as usize, u32_at(record + 12)? as usize);
        records.extend_from_slice(file.get(record..record + 8)?);
        records.extend_from_slice(&((header_len + data.len()) as u32).to_be_bytes());
        records.extend_from_slice(&(len as u32).to_be_bytes());
        data.extend_from_slice(file.get(start..start + len)?);
        data.resize(data.len().next_multiple_of(4), 0);
    }
    records.extend(data);
    Some(records)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A collection of two faces with one table each.
    fn collection() -> Vec<u8> {
        let face = |tag: &[u8; 4], table_at: u32| {
            let mut f = vec![0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0];
            f.extend_from_slice(tag);
            f.extend_from_slice(&[0; 4]);
            f.extend_from_slice(&table_at.to_be_bytes());
            f.extend_from_slice(&3u32.to_be_bytes());
            f
        };
        let mut file = b"ttcf".to_vec();
        file.extend_from_slice(&[0, 1, 0, 0, 0, 0, 0, 2]);
        file.extend_from_slice(&20u32.to_be_bytes());
        file.extend_from_slice(&48u32.to_be_bytes());
        file.extend(face(b"aaaa", 76));
        file.extend(face(b"bbbb", 80));
        file.extend_from_slice(b"AAA\0BBB\0");
        file
    }

    #[test]
    fn calibri_and_cambria_have_bundled_metric_compatible_faces() {
        let load = |family: &str, bold, italic| bundled_face(family, bold, italic).unwrap().load().unwrap();
        for (family, faces) in [("Calibri", &CARLITO), ("calibri light", &CARLITO), ("Carlito", &CARLITO), ("Cambria", &CALADEA), ("Caladea", &CALADEA)] {
            assert_eq!(load(family, false, false), faces[0], "{family} regular");
            assert_eq!(load(family, true, false), faces[1], "{family} bold");
            assert_eq!(load(family, false, true), faces[2], "{family} italic");
            assert_eq!(load(family, true, true), faces[3], "{family} bold italic");
        }
        assert!(CARLITO.iter().chain(&CALADEA).all(|b| b.starts_with(&[0, 1, 0, 0]) && b.len() > 50_000));
        assert!(bundled_face("Cambria Math", false, false).is_none());
        // Office's own copy, then the machine's, win; the bundled face is what a machine without them draws.
        assert!(SystemFonts.resolve_family("Calibri", false, false).is_some());
    }

    #[test]
    fn a_collection_face_stands_alone() {
        let face = collection_face(&collection(), 1).unwrap();
        assert_eq!(&face[12..16], b"bbbb");
        assert_eq!(u32::from_be_bytes(face[20..24].try_into().unwrap()), 28);
        assert_eq!(&face[28..31], b"BBB");
        assert_eq!(face.len(), 32);
        assert!(collection_face(&collection(), 2).is_none());
    }

    #[test]
    fn a_plain_font_is_its_own_face() {
        assert_eq!(
            collection_face(b"\0\x01\0\0rest", 0).unwrap(),
            b"\0\x01\0\0rest"
        );
        assert!(collection_face(b"\0\x01\0\0rest", 1).is_none());
    }

    #[test]
    fn office_families_map_to_their_files() {
        assert_eq!(
            office_files("微软雅黑"),
            Some(("msyh.ttf", Some("msyhbd.ttf")))
        );
        assert_eq!(
            office_files("Microsoft YaHei UI"),
            Some(("msyh.ttf", Some("msyhbd.ttf")))
        );
        assert_eq!(
            office_files("DengXian"),
            Some(("Deng.ttf", Some("Dengb.ttf")))
        );
        assert_eq!(office_files("宋体"), Some(("SimSun.ttf", None)));
        assert_eq!(office_files("Arial"), None);
    }

    #[test]
    fn cjk_families_are_told_from_latin_ones() {
        assert!(is_cjk_family("PingFang SC"));
        assert!(is_cjk_family("华文楷体"));
        assert!(!is_cjk_family("Calibri"));
    }

    #[test]
    fn chinese_families_have_stand_ins() {
        for f in ["Heiti SC", "黑体", "STHeiti", "Songti SC", "宋体", "楷体", "KaiTi", "FangSong", "仿宋"] {
            assert!(is_cjk_family(f), "{f}");
        }
        assert_eq!(cjk_stand_ins("楷体")[0], "Kaiti SC");
        assert_eq!(cjk_stand_ins("宋体")[0], "Songti SC");
        assert_eq!(cjk_stand_ins("Heiti SC")[0], "Heiti SC");
    }

    #[test]
    fn faces_are_sized_and_oversized_ones_are_skipped() {
        let sizes: Vec<u64> = installed().values().flatten().map(|f| f.size).collect();
        assert!(sizes.iter().any(|&s| s > 0));
        // Whatever resolves for a CJK family fits the host's limit.
        let provider = SystemFonts::new().unwrap();
        for family in ["Heiti SC", "宋体", "楷体", "仿宋", "PingFang SC", "微软雅黑"] {
            if let Some(face) = provider.resolve_family(family, false, false) {
                let n = face.load().unwrap().len() as u64; assert!(n <= MAX_FACE_BYTES, "{family} {n}");
            }
        }
        if let Some(face) = provider.resolve_script_fallback("cjk", false, false) {
            assert!(face.load().unwrap().len() as u64 <= MAX_FACE_BYTES);
        }
    }

    #[test]
    fn office_and_macos_families_are_found_by_name() {
        assert!(installed_face("Arial", false, false).is_some());
        assert!(installed_face("Times New Roman", true, true).is_some());
        let provider = SystemFonts::new().unwrap();
        // A family the machine lacks still draws, in its stand-in or the last resort.
        assert!(provider.resolve_family("Cambria", false, false).is_some());
        let _ = provider.resolve_last_resort("Nope", false, false, LastResortOffice::default());
    }

    #[test]
    fn the_provider_keeps_every_face() {
        assert!(SystemFonts::new().unwrap().bundled_packs().is_none());
    }

    /// A `name` table of (platform, language, name ID, UTF-16 or Mac Roman text) records.
    fn name_table(records: &[(u16, u16, u16, &str)]) -> Vec<u8> {
        let encoded: Vec<Vec<u8>> = records
            .iter()
            .map(|(platform, _, _, text)| match platform {
                1 => text.bytes().collect(),
                _ => text.encode_utf16().flat_map(u16::to_be_bytes).collect(),
            })
            .collect();
        let mut table = vec![0, 0];
        table.extend((records.len() as u16).to_be_bytes());
        table.extend((6 + 12 * records.len() as u16).to_be_bytes());
        let mut at = 0u16;
        for ((platform, language, id, _), bytes) in records.iter().zip(&encoded) {
            for field in [*platform, 0, *language, *id, bytes.len() as u16, at] {
                table.extend(field.to_be_bytes());
            }
            at += bytes.len() as u16;
        }
        table.extend(encoded.concat());
        table
    }

    #[test]
    fn a_family_is_listed_by_its_english_and_chinese_names() {
        let table = name_table(&[
            (3, 0x0409, 1, "STXingkai"),
            (3, 0x0804, 1, "华文行楷"),
            (3, 0x0404, 1, "華文行楷"),
        ]);
        let label = family_label(&table).unwrap();
        assert_eq!(label, SystemFamily { family: "STXingkai".into(), localized: Some("华文行楷".into()) });
        assert_eq!(family_names(&table), ["stxingkai", "华文行楷", "華文行楷"]);
    }

    #[test]
    fn mac_chinese_names_are_decoded_from_gb2312() {
        let mut table = name_table(&[(1, 0, 1, "STXingkai"), (1, 33, 1, "")]);
        let gb = encoding_rs::GBK.encode("华文行楷").0.into_owned();
        // Patch the empty second record to point at GB2312 bytes appended to the table.
        let at = (table.len() - (6 + 24)) as u16;
        table[6 + 12 + 8..6 + 12 + 12].copy_from_slice(&[(gb.len() as u16).to_be_bytes(), at.to_be_bytes()].concat());
        table.extend(gb);
        assert_eq!(family_label(&table).unwrap().localized.as_deref(), Some("华文行楷"));
    }

    #[test]
    fn the_typographic_family_wins_over_the_style_linked_one() {
        let table = name_table(&[(1, 0, 1, "Helvetica Neue Light"), (3, 0x0409, 16, "Helvetica Neue")]);
        assert_eq!(family_label(&table).unwrap(), SystemFamily { family: "Helvetica Neue".into(), localized: None });
    }

    #[test]
    fn the_menu_lists_installed_families_but_not_hidden_ones() {
        let families = SystemFonts::new().unwrap().families();
        assert!(families.iter().any(|f| f.family == "Helvetica Neue"), "{families:?}");
        assert!(families.iter().all(|f| listable(&f.family)));
        let keys: Vec<String> = families.iter().map(|f| f.family.to_lowercase()).collect();
        assert!(keys.windows(2).all(|w| w[0] < w[1]), "sorted and unique");
    }

}
