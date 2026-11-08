//! Pages of saved `.docx` bytes, drawn with the machine's own fonts (her-slide's
//! `SystemFonts`, the faces PowerPoint decks use too).

use betteroffice_docx::fonts::FontSource;
use betteroffice_docx::{Document, OpenOptions, PngTarget, RenderOptions};
use her_slide::SystemFonts;

use crate::{Result, WordError};

/// Scale for probing whether a page exists: the layout is the same, the pixmap tiny.
const PROBE_SCALE: f32 = 0.02;

pub(crate) fn open(bytes: &[u8]) -> Result<Document> {
    let options = OpenOptions {
        font_source: FontSource::Provider(SystemFonts::new()?),
        ..OpenOptions::default()
    };
    Ok(Document::open_with_options(bytes, &options)?)
}

/// Page `page` (from 1) of `docx` as PNG bytes; `scale` 1 is one pixel per CSS pixel.
pub fn render_page(docx: &[u8], page: u32, scale: f32) -> Result<Vec<u8>> {
    let document = open(docx)?;
    if page == 0 {
        return Err(WordError::NoPage(page));
    }
    draw(&document, page - 1, scale)?.ok_or(WordError::NoPage(page))
}

/// Pages `docx` lays out to.
pub fn page_count(docx: &[u8]) -> Result<u32> {
    count(&open(docx)?)
}

/// The engine does not report a page count, so pages are probed: doubling, then halving.
// TODO(engine): a page count from the layout, instead of one layout per probe.
pub(crate) fn count(document: &Document) -> Result<u32> {
    let exists = |ordinal: u32| draw(document, ordinal, PROBE_SCALE).map(|png| png.is_some());
    if !exists(0)? {
        return Ok(0);
    }
    // Page `known` - 1 exists; page `beyond` - 1 does not.
    let (mut known, mut beyond) = (1, 2);
    while exists(beyond - 1)? {
        known = beyond;
        beyond *= 2;
    }
    while beyond - known > 1 {
        let middle = (known + beyond) / 2;
        if exists(middle - 1)? {
            known = middle
        } else {
            beyond = middle
        }
    }
    Ok(known)
}

/// `None` past the last page.
fn draw(document: &Document, ordinal: u32, scale: f32) -> Result<Option<Vec<u8>>> {
    match document.render_png(
        PngTarget {
            page: ordinal as usize,
        },
        &RenderOptions { scale },
    ) {
        Ok(page) => Ok(Some(page.bytes)),
        Err(betteroffice_docx::Error::Render(message)) if message.contains("out of range") => {
            Ok(None)
        }
        Err(e) => Err(e.into()),
    }
}
