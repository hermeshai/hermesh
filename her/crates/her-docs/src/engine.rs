//! The PDFium side, run in the worker process only (design §9.4.8): PDFium is not thread-safe
//! and a crash in it must not take `her serve` with it. Requests come one at a time.

use her_protocol::doc::OutlineItem;
use her_protocol::doc::Rect;
use pdfium_render::prelude::*;
use serde::Deserialize;
use serde::Serialize;
use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;

use crate::layout::Char;
use crate::layout::PageChars;

/// A request to the worker. `handle`s are the client's: it names a document when opening it.
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(tag = "op", rename_all = "snake_case")]
pub enum Request {
    Open { handle: u64, path: PathBuf, password: Option<String> },
    Close { handle: u64 },
    /// The characters of a page (1-based).
    Chars { handle: u64, page: u32 },
    /// A page, or the part of it in `clip`, as PNG, at `dpi`; never more than `max_side` pixels
    /// on its longer side.
    Render { handle: u64, page: u32, clip: Option<Rect>, dpi: u32, max_side: u32 },
    /// A workbook (spreadsheet-design §5.7); `name`: the file's own name without its extension,
    /// a CSV's one sheet (the working copy is named `current`).
    SheetOpen {
        handle: u64,
        path: PathBuf,
        #[serde(default)]
        name: Option<String>,
    },
    /// A range's cells, as the grid shows them.
    SheetCells { handle: u64, sheet: String, range: String },
    /// Edits: written to the working copy (or only tried, `preview`), with their impact.
    SheetApply { handle: u64, ops: Vec<her_protocol::doc::SheetOp>, preview: bool },
    /// What the agent reads first about a workbook.
    SheetRecalc { handle: u64, circular_only: bool },
    SheetSummary { handle: u64 },
    SheetFind { handle: u64, query: String, limit: usize },
    SheetTrace { handle: u64, sheet: String, cell: String, direction: crate::sheet::Direction, depth: u32 },
    /// The edits a fill down or right makes, not applied.
    SheetFill { handle: u64, sheet: String, range: String, direction: crate::sheet::FillDirection },
    /// Rows or columns inserted or deleted, written to the working copy; the sheets as they are.
    SheetStructure { handle: u64, change: her_protocol::doc::SheetStructure },
    /// The working copy read again after it was put back (an undo of a structure change).
    SheetReload { handle: u64 },
    /// The edits a sort makes, not applied.
    SheetSort { handle: u64, sheet: String, range: String, by: String, descending: bool, header: bool },
    /// The edits the fill handle makes, not applied.
    SheetAutofill { handle: u64, sheet: String, source: String, target: String, switch: bool },
}

impl Request {
    /// The document a request is about, once it is open.
    pub fn handle(&self) -> Option<u64> {
        match self {
            Request::Close { handle }
            | Request::Chars { handle, .. }
            | Request::Render { handle, .. }
            | Request::SheetCells { handle, .. }
            | Request::SheetApply { handle, .. }
            | Request::SheetRecalc { handle, .. }
            | Request::SheetSummary { handle }
            | Request::SheetFind { handle, .. }
            | Request::SheetTrace { handle, .. }
            | Request::SheetFill { handle, .. }
            | Request::SheetStructure { handle, .. }
            | Request::SheetReload { handle }
            | Request::SheetSort { handle, .. }
            | Request::SheetAutofill { handle, .. } => Some(*handle),
            Request::Open { .. } | Request::SheetOpen { .. } => None,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Opened {
    pub pages: u32,
    pub outline: Vec<OutlineItem>,
    pub encrypted: bool,
    /// Some text on the first pages: not a scan.
    pub has_text: bool,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Rendered {
    /// PNG, base64.
    pub png: String,
    pub width: u32,
    pub height: u32,
}

/// Where the PDFium library is: `HER_PDFIUM`, else beside the program (Hermesh.app's
/// `Contents/MacOS` or `Contents/Frameworks`), else the `pdf` component (`her install pdf`), else
/// `$HER_HOME/pdfium/lib`, else, in development builds, the copy `tools/pdfium` downloads into.
pub fn locate_library() -> Option<PathBuf> {
    let name = Pdfium::pdfium_platform_library_name();
    if let Some(path) = std::env::var_os("HER_PDFIUM") {
        return Some(PathBuf::from(path));
    }
    let exe_dir = std::env::current_exe().ok().and_then(|e| e.parent().map(Path::to_path_buf));
    let mut candidates: Vec<PathBuf> = Vec::new();
    if let Some(dir) = &exe_dir {
        candidates.push(dir.join(&name));
        candidates.push(dir.join("../Frameworks").join(&name));
    }
    if let Some(dir) = crate::pdf_addon_dir() {
        candidates.push(dir.join(&name));
    }
    if let Some(home) = crate::her_home() {
        candidates.push(home.join("pdfium/lib").join(&name));
    }
    if cfg!(debug_assertions) {
        let platform = match (std::env::consts::OS, std::env::consts::ARCH) {
            ("macos", "aarch64") => "mac-arm64",
            ("macos", _) => "mac-x64",
            ("linux", "aarch64") => "linux-arm64",
            ("linux", _) => "linux-x64",
            _ => "win-x64",
        };
        candidates.push(Path::new(env!("CARGO_MANIFEST_DIR")).join("../../tools/pdfium").join(platform).join("lib").join(&name));
    }
    candidates.into_iter().find(|p| p.is_file())
}

pub struct Engine {
    pdfium: &'static Pdfium,
    docs: HashMap<u64, PdfDocument<'static>>,
}

impl Engine {
    pub fn new() -> Result<Self, String> {
        // One per process, for its whole life: documents borrow it, and PDFium binds only once.
        static PDFIUM: std::sync::OnceLock<Result<Pdfium, String>> = std::sync::OnceLock::new();
        let pdfium = PDFIUM.get_or_init(|| {
            let path = locate_library().ok_or("PDF support is not installed. Ask the user to run \"her install pdf\", or read the file another way.")?;
            let bindings = Pdfium::bind_to_library(&path).map_err(|e| format!("cannot load {}: {e}", path.display()))?;
            Ok(Pdfium::new(bindings))
        });
        Ok(Self { pdfium: pdfium.as_ref().map_err(Clone::clone)?, docs: HashMap::new() })
    }

    pub fn handle(&mut self, request: Request) -> Result<serde_json::Value, String> {
        let value = match request {
            Request::Open { handle, path, password } => serde_json::to_value(self.open(handle, &path, password.as_deref())?),
            Request::Close { handle } => {
                self.docs.remove(&handle);
                Ok(serde_json::Value::Null)
            }
            Request::Chars { handle, page } => serde_json::to_value(self.chars(handle, page)?),
            Request::Render { handle, page, clip, dpi, max_side } => serde_json::to_value(self.render(handle, page, clip, dpi, max_side)?),
            _ => return Err("not a PDF request".into()),
        };
        value.map_err(|e| e.to_string())
    }

    fn open(&mut self, handle: u64, path: &Path, password: Option<&str>) -> Result<Opened, String> {
        let doc = self.pdfium.load_pdf_from_file(path, password).map_err(|e| match e {
            PdfiumError::PdfiumLibraryInternalError(PdfiumInternalError::PasswordError) => "password required or wrong".to_string(),
            PdfiumError::PdfiumLibraryInternalError(PdfiumInternalError::FormatError) => "not a PDF, or damaged beyond repair".to_string(),
            e => format!("cannot open the PDF: {e}"),
        })?;
        let pages = doc.pages().len().max(0) as u32;
        let encrypted = !matches!(doc.permissions().security_handler_revision(), Ok(PdfSecurityHandlerRevision::Unprotected) | Err(_));
        let outline = doc.bookmarks().root().map(|root| outline(&root, 0)).unwrap_or_default();
        let has_text = (0..pages.min(5)).any(|i| {
            doc.pages().get(i as i32).ok().and_then(|p| p.text().ok().map(|t| !t.chars().is_empty())).unwrap_or(false)
        });
        self.docs.insert(handle, doc);
        Ok(Opened { pages, outline, encrypted, has_text })
    }

    fn page(&self, handle: u64, page: u32) -> Result<PdfPage<'static>, String> {
        let doc = self.docs.get(&handle).ok_or("the document is not open")?;
        let count = doc.pages().len().max(0) as u32;
        if page == 0 || page > count {
            return Err(format!("no page {page}: the document has {count}"));
        }
        doc.pages().get((page - 1) as i32).map_err(|e| e.to_string())
    }

    fn chars(&self, handle: u64, page: u32) -> Result<PageChars, String> {
        let p = self.page(handle, page)?;
        let text = p.text().map_err(|e| e.to_string())?;
        let mut chars = Vec::new();
        for ch in text.chars().iter() {
            let Some(c) = ch.unicode_char() else { continue };
            if c.is_control() && c != '\n' {
                continue;
            }
            // Line breaks PDFium generated are layout, which we redo; spaces it generated stay.
            if c == '\r' || c == '\n' {
                continue;
            }
            let invisible = ch.text_object().ok().is_some_and(|o| matches!(o.render_mode(), PdfPageTextRenderMode::Invisible));
            if invisible {
                continue;
            }
            let r = if c.is_whitespace() {
                Rect::default()
            } else {
                let Ok(b) = ch.tight_bounds() else { continue };
                Rect { x0: b.left().value, y0: b.bottom().value, x1: b.right().value, y1: b.top().value }
            };
            let bold = matches!(ch.font_weight(), Some(PdfFontWeight::Weight700Bold | PdfFontWeight::Weight800 | PdfFontWeight::Weight900))
                || ch.font_name().to_ascii_lowercase().contains("bold");
            // PDFium gives no scaled size for text drawn turned (up the page): the loose box, across
            // the direction of the text, is the size then.
            let mut size = ch.scaled_font_size().value;
            if size < 0.5
                && let Ok(l) = ch.loose_bounds()
            {
                let angle = ch.angle_degrees().unwrap_or(0.0).rem_euclid(180.0);
                size = if (45.0..135.0).contains(&angle) { l.width().value } else { l.height().value };
            }
            chars.push(Char { c, r, size, bold });
        }
        let (crop, rotation) = shown_box(&p);
        Ok(PageChars { width: p.width().value, height: p.height().value, chars, crop, rotation })
    }

    fn render(&self, handle: u64, page: u32, clip: Option<Rect>, dpi: u32, max_side: u32) -> Result<Rendered, String> {
        let p = self.page(handle, page)?;
        // The region's size as shown (a page turned on its side swaps it), for the scale.
        let (crop, rotation) = shown_box(&p);
        let clip = clip
            .map(|c| Rect { x0: c.x0.max(crop.x0), y0: c.y0.max(crop.y0), x1: c.x1.min(crop.x1), y1: c.y1.min(crop.y1) })
            .filter(|c| c.width() > 1.0 && c.height() > 1.0);
        let area = clip.unwrap_or(crop);
        let (aw, ah) = if rotation % 180 == 90 { (area.height(), area.width()) } else { (area.width(), area.height()) };
        let full = (p.width().value, p.height().value);
        let mut scale = dpi.clamp(36, 600) as f32 / 72.0;
        // The bitmap is the whole page: keep both it and the region within reason.
        let longest = aw.max(ah) * scale;
        if longest > max_side as f32 {
            scale *= max_side as f32 / longest;
        }
        let page_longest = full.0.max(full.1) * scale;
        if page_longest > 10_000.0 {
            scale *= 10_000.0 / page_longest;
        }
        let config = PdfRenderConfig::new()
            .scale_page_by_factor(scale)
            .render_form_data(true)
            .render_annotations(true)
            .set_format(PdfBitmapFormat::BGRA);
        let bitmap = p.render_with_config(&config).map_err(|e| e.to_string())?;
        let (bw, bh) = (bitmap.width() as u32, bitmap.height() as u32);
        let rgba = bitmap.as_rgba_bytes();
        // The region in pixels, as PDFium maps the page (its rotation and box included).
        let corners = [(area.x0, area.y0), (area.x1, area.y0), (area.x0, area.y1), (area.x1, area.y1)];
        let mut pixels = Vec::new();
        for (x, y) in corners {
            let (px, py) = p.points_to_pixels(PdfPoints::new(x), PdfPoints::new(y), &config).map_err(|e| e.to_string())?;
            pixels.push((px.max(0) as u32, py.max(0) as u32));
        }
        let px0 = pixels.iter().map(|p| p.0).min().unwrap_or(0).min(bw.saturating_sub(1));
        let px1 = pixels.iter().map(|p| p.0).max().unwrap_or(bw).clamp(px0 + 1, bw);
        let py0 = pixels.iter().map(|p| p.1).min().unwrap_or(0).min(bh.saturating_sub(1));
        let py1 = pixels.iter().map(|p| p.1).max().unwrap_or(bh).clamp(py0 + 1, bh);
        let (cw, ch) = (px1 - px0, py1 - py0);
        let mut cropped = Vec::with_capacity((cw * ch * 4) as usize);
        for row in py0..py1 {
            let start = ((row * bw + px0) * 4) as usize;
            cropped.extend_from_slice(&rgba[start..start + (cw * 4) as usize]);
        }
        let mut png = Vec::new();
        {
            let mut encoder = png::Encoder::new(&mut png, cw, ch);
            encoder.set_color(png::ColorType::Rgba);
            encoder.set_depth(png::BitDepth::Eight);
            let mut writer = encoder.write_header().map_err(|e| e.to_string())?;
            writer.write_image_data(&cropped).map_err(|e| e.to_string())?;
        }
        use base64::Engine as _;
        Ok(Rendered { png: base64::engine::general_purpose::STANDARD.encode(png), width: cw, height: ch })
    }
}

/// The page's visible box in user space (its crop box, else its media box), and how far it is
/// turned clockwise when shown.
fn shown_box(p: &PdfPage<'_>) -> (Rect, u16) {
    let b = p.boundaries();
    let bounds = b.crop().or_else(|_| b.media()).map(|x| x.bounds).ok();
    let crop = bounds
        .map(|r| Rect { x0: r.left().value.min(r.right().value), y0: r.bottom().value.min(r.top().value), x1: r.left().value.max(r.right().value), y1: r.bottom().value.max(r.top().value) })
        .unwrap_or(Rect { x0: 0.0, y0: 0.0, x1: p.width().value, y1: p.height().value });
    let rotation = match p.rotation() {
        Ok(PdfPageRenderRotation::Degrees90) => 90,
        Ok(PdfPageRenderRotation::Degrees180) => 180,
        Ok(PdfPageRenderRotation::Degrees270) => 270,
        _ => 0,
    };
    (crop, rotation)
}

fn outline(parent: &PdfBookmark<'_>, depth: usize) -> Vec<OutlineItem> {
    if depth > 8 {
        return Vec::new();
    }
    parent
        .iter_direct_children()
        .take(500)
        .map(|b| OutlineItem {
            title: b.title().unwrap_or_default().trim().to_string(),
            page: b.destination().and_then(|d| d.page_index().ok()).filter(|i| *i >= 0).map(|i| i as u32 + 1),
            children: outline(&b, depth + 1),
        })
        .collect()
}

/// The worker's loop: one JSON request a line in, one JSON reply a line out, until the input
/// ends. Replies are `{"ok": …}` or `{"err": "…"}`.
pub fn serve(input: impl std::io::BufRead, mut output: impl std::io::Write) -> std::io::Result<()> {
    // PDFium is loaded on the first PDF request: workbooks do not need it.
    let mut pdf: Option<Result<Engine, String>> = None;
    let mut sheets = crate::sheet::Sheets::default();
    for line in input.lines() {
        let line = line?;
        if line.trim().is_empty() {
            continue;
        }
        let result = match serde_json::from_str::<Request>(&line) {
            Err(e) => Err(format!("bad request: {e}")),
            Ok(request) => handle(&mut pdf, &mut sheets, request),
        };
        let reply = match result {
            Ok(value) => serde_json::json!({ "ok": value }),
            Err(e) => serde_json::json!({ "err": e }),
        };
        serde_json::to_writer(&mut output, &reply)?;
        output.write_all(b"\n")?;
        output.flush()?;
    }
    Ok(())
}

fn handle(pdf: &mut Option<Result<Engine, String>>, sheets: &mut crate::sheet::Sheets, request: Request) -> Result<serde_json::Value, String> {
    let json = |v: Result<serde_json::Value, serde_json::Error>| v.map_err(|e| e.to_string());
    match request {
        Request::SheetOpen { handle, path, name } => json(serde_json::to_value(sheets.open(handle, &path, name.as_deref())?)),
        Request::SheetCells { handle, sheet, range } => json(serde_json::to_value(sheets.cells(handle, &sheet, &range)?)),
        Request::SheetApply { handle, ops, preview } => json(serde_json::to_value(sheets.apply(handle, &ops, preview)?)),
        Request::SheetRecalc { handle, circular_only } => json(serde_json::to_value(sheets.recalc(handle, circular_only)?)),
        Request::SheetSummary { handle } => json(serde_json::to_value(sheets.summary(handle)?)),
        Request::SheetFind { handle, query, limit } => json(serde_json::to_value(sheets.find(handle, &query, limit)?)),
        Request::SheetTrace { handle, sheet, cell, direction, depth } => json(serde_json::to_value(sheets.trace(handle, &sheet, &cell, direction, depth)?)),
        Request::SheetFill { handle, sheet, range, direction } => json(serde_json::to_value(sheets.fill(handle, &sheet, &range, direction)?)),
        Request::SheetStructure { handle, change } => json(serde_json::to_value(sheets.structure(handle, &change)?)),
        Request::SheetReload { handle } => json(serde_json::to_value(sheets.reload(handle)?)),
        Request::SheetSort { handle, sheet, range, by, descending, header } => json(serde_json::to_value(sheets.sort(handle, &sheet, &range, &by, descending, header)?)),
        Request::SheetAutofill { handle, sheet, source, target, switch } => json(serde_json::to_value(sheets.autofill(handle, &sheet, &source, &target, switch)?)),
        Request::Close { handle } if sheets.close(handle) => Ok(serde_json::Value::Null),
        request => match pdf.get_or_insert_with(Engine::new) {
            Ok(engine) => engine.handle(request),
            Err(e) => Err(e.clone()),
        },
    }
}
