//! Phase-0 checks for the document editor (docs/desktop/document-editor-design.md §9.2).
//!
//! doc-spike annotate <in> <out> <inc|full>   every markup type on page 1, CJK and Latin text
//! doc-spike spans <in> <page>                spans with bboxes as JSON (V3)
//! doc-spike text-in <in> <page> <x> <y> <w> <h>
//! doc-spike sign <in> <out> <p12> <password>
//! doc-spike encrypt <in> <out> <user-password>
//! doc-spike info <in> [password]
//! doc-spike bench <in> <revisions>           V4: commit, replay, save timings

use std::time::Instant;

use pdf_oxide::editor::{DocumentEditor, EditableDocument, EncryptionConfig, SaveOptions};
use pdf_oxide::geometry::Rect;
use pdf_oxide::writer::{
    FreeTextAnnotation, InkAnnotation, LineAnnotation, ShapeAnnotation, TextAnnotation, TextMarkupAnnotation,

};
use pdf_oxide::annotation_types::TextMarkupType;
use pdf_oxide::layout::RectFilterMode;
use pdf_oxide::PdfDocument;

type R<T> = Result<T, Box<dyn std::error::Error>>;

fn main() -> R<()> {
    let a: Vec<String> = std::env::args().skip(1).collect();
    match a.first().map(String::as_str) {
        Some("annotate") => annotate(&a[1], &a[2], a[3] == "inc"),
        Some("spans") => spans(&a[1], a[2].parse()?),
        Some("text-in") => text_in(&a[1], a[2].parse()?, [a[3].parse()?, a[4].parse()?, a[5].parse()?, a[6].parse()?]),
        Some("sign") => sign(&a[1], &a[2], &a[3], &a[4]),
        Some("encrypt") => encrypt(&a[1], &a[2], &a[3]),
        Some("info") => info(&a[1], a.get(2).map(String::as_str)),
        Some("bench") => bench(&a[1], a[2].parse()?),
        Some("resolve") => resolve(&a[1], a[2].parse()?),
        Some("markdown") => {
            let doc = PdfDocument::open(&a[1])?;
            print!("{}", doc.to_markdown(a[2].parse()?, &pdf_oxide::converters::ConversionOptions::default())?);
            Ok(())
        }
        Some("fill") => fill(&a[1], &a[2], a[3] == "inc"),
        _ => Err("unknown command".into()),
    }
}

fn quad(r: &Rect) -> [f64; 8] {
    // x1 y1 x2 y2 x3 y3 x4 y4: top-left, top-right, bottom-left, bottom-right (as Acrobat writes them).
    let (x0, y0, x1, y1) = (r.x as f64, r.y as f64, (r.x + r.width) as f64, (r.y + r.height) as f64);
    [x0, y1, x1, y1, x0, y0, x1, y0]
}

/// One of every markup the editor would write, on page 1.
fn add_all(ed: &mut DocumentEditor, spans: &[pdf_oxide::layout::TextSpan]) -> R<()> {
    let mut page = ed.get_page(0)?;
    let kinds = [TextMarkupType::Highlight, TextMarkupType::Underline, TextMarkupType::StrikeOut];
    for (span, kind) in spans.iter().filter(|s| s.text.trim().chars().count() > 3).zip(kinds) {
        let r = span.bbox;
        page.add_annotation(TextMarkupAnnotation::new(kind, r, vec![quad(&r)]).with_color(1.0, 0.85, 0.0));
    }
    page.add_annotation(TextAnnotation::new(Rect::new(40.0, 40.0, 20.0, 20.0), "便签：违约金条款需要复核 (note)"));
    page.add_annotation(
        FreeTextAnnotation::new(Rect::new(300.0, 60.0, 220.0, 40.0), "Latin text box 123").with_font("Helvetica", 12.0),
    );
    page.add_annotation(
        FreeTextAnnotation::new(Rect::new(300.0, 110.0, 220.0, 40.0), "中文文本框：营业收入同比增长 12.5%")
            .with_font("Helvetica", 12.0),
    );
    page.add_annotation(
        InkAnnotation::with_strokes(vec![vec![(60.0, 200.0), (90.0, 230.0), (120.0, 200.0), (150.0, 230.0)]])
            .with_stroke_color(0.9, 0.1, 0.1)
            .with_line_width(2.0),
    );
    page.add_annotation(ShapeAnnotation::square(Rect::new(60.0, 260.0, 80.0, 40.0)));
    page.add_annotation(ShapeAnnotation::circle(Rect::new(160.0, 260.0, 80.0, 40.0)));
    page.add_annotation(LineAnnotation::new((60.0, 320.0), (240.0, 320.0)));
    ed.save_page(page)?;
    Ok(())
}

fn annotate(input: &str, out: &str, incremental: bool) -> R<()> {
    let spans = PdfDocument::open(input)?.extract_spans(0)?;
    let mut ed = DocumentEditor::open(input)?;
    add_all(&mut ed, &spans)?;
    let opts = if incremental { SaveOptions::incremental() } else { SaveOptions::full_rewrite() };
    ed.save_with_options(out, opts)?;
    let bytes = std::fs::read(out)?;
    let orig = std::fs::metadata(input)?.len();
    println!("{{\"out\":\"{out}\",\"bytes\":{},\"prefix_preserved\":{}}}", bytes.len(), {
        let o = std::fs::read(input)?;
        bytes.len() as u64 >= orig && bytes[..o.len()] == o[..]
    });
    Ok(())
}

fn spans(input: &str, page: usize) -> R<()> {
    let doc = PdfDocument::open(input)?;
    let (_, _, w, h) = doc.get_page_media_box(page)?;
    let spans: Vec<_> = doc
        .extract_spans(page)?
        .into_iter()
        .map(|s| serde_json::json!({"text": s.text, "x": s.bbox.x, "y": s.bbox.y, "w": s.bbox.width, "h": s.bbox.height}))
        .collect();
    println!("{}", serde_json::json!({"page_w": w, "page_h": h, "spans": spans}));
    Ok(())
}

fn text_in(input: &str, page: usize, r: [f32; 4]) -> R<()> {
    let doc = PdfDocument::open(input)?;
    let spans = doc.extract_spans_in_rect(page, Rect::new(r[0], r[1], r[2], r[3]), RectFilterMode::Intersects)?;
    let text: Vec<_> = spans.iter().map(|s| s.text.as_str()).collect();
    println!("{}", serde_json::json!(text.join(" ")));
    Ok(())
}

/// What `doc/resolve` would do: stdin is a JSON array of [x, y, w, h] rects (PDF user space, origin bottom-left);
/// stdout is, per rect, the characters whose centre lies inside it, in content order.
fn resolve(input: &str, page: usize) -> R<()> {
    let doc = PdfDocument::open(input)?;
    // Per-character boxes from spans: x advances by char_widths (the char API mis-maps ligatures, see §9.2 V3).
    let mut chars: Vec<(char, f32, f32)> = Vec::new(); // (char, centre x, centre y)
    let mut mismatched = 0;
    for s in doc.extract_spans(page)? {
        let text: Vec<char> = s.text.chars().collect();
        let cy = s.bbox.y + s.bbox.height / 2.0;
        if s.char_widths.len() != text.len() {
            mismatched += 1;
            let w = s.bbox.width / text.len().max(1) as f32;
            for (i, c) in text.into_iter().enumerate() {
                chars.push((c, s.bbox.x + w * (i as f32 + 0.5), cy));
            }
            continue;
        }
        let mut x = s.bbox.x;
        for (c, w) in text.into_iter().zip(&s.char_widths) {
            chars.push((c, x + w / 2.0, cy));
            let hs = if s.horizontal_scaling > 0.0 { s.horizontal_scaling / 100.0 } else { 1.0 };
            x += w + (s.char_spacing + if c == ' ' { s.word_spacing } else { 0.0 }) * hs * std::env::var("TC").map(|v| v.parse::<f32>().unwrap()).unwrap_or(0.0);
        }
    }
    eprintln!("spans whose char_widths do not match their text: {mismatched}");
    let rects: Vec<[f32; 4]> = serde_json::from_reader(std::io::stdin())?;
    let out: Vec<String> = rects
        .iter()
        .map(|r| {
            chars
                .iter()
                .filter(|(_, cx, cy)| *cx >= r[0] && *cx <= r[0] + r[2] && *cy >= r[1] && *cy <= r[1] + r[3])
                .map(|(c, _, _)| *c)
                .collect()
        })
        .collect();
    println!("{}", serde_json::json!(out));
    Ok(())
}

fn fill(input: &str, out: &str, incremental: bool) -> R<()> {
    use pdf_oxide::editor::form_fields::FormFieldValue;
    let mut ed = DocumentEditor::open(input)?;
    ed.set_form_field_value("company", FormFieldValue::Text("Acme Holdings Ltd.".into()))?;
    ed.set_form_field_value("signer", FormFieldValue::Text("王小明（财务总监）".into()))?;
    ed.set_form_field_value("date", FormFieldValue::Text("2026-11-02".into()))?;
    ed.set_form_field_value("agree", FormFieldValue::Boolean(true))?;
    if std::env::var("FLATTEN").is_ok() {
        ed.flatten_forms()?;
    }
    ed.save_with_options(out, if incremental { SaveOptions::incremental() } else { SaveOptions::full_rewrite() })?;
    Ok(())
}

fn sign(input: &str, out: &str, p12: &str, pw: &str) -> R<()> {
    use pdf_oxide::signatures::{sign_pdf_bytes, SignOptions, SigningCredentials};
    let creds = SigningCredentials::from_pkcs12(&std::fs::read(p12)?, pw)?;
    let signed = sign_pdf_bytes(&std::fs::read(input)?, &creds, SignOptions::default())?;
    std::fs::write(out, signed)?;
    Ok(())
}

fn encrypt(input: &str, out: &str, user: &str) -> R<()> {
    let mut ed = DocumentEditor::open(input)?;
    let cfg = EncryptionConfig { user_password: user.into(), owner_password: format!("{user}-owner"), ..Default::default() };
    std::fs::write(out, ed.save_to_bytes_with_options(SaveOptions::with_encryption(cfg))?)?;
    Ok(())
}

fn info(input: &str, password: Option<&str>) -> R<()> {
    let bytes = std::fs::read(input)?;
    let doc = PdfDocument::from_bytes(bytes.clone())?;
    if let Some(p) = password {
        println!("authenticated: {}", doc.authenticate(p.as_bytes())?);
    }
    let text = doc.extract_text(0).unwrap_or_else(|e| format!("<error {e}>"));
    let encrypt = bytes.windows(8).any(|w| w == b"/Encrypt");
    println!("pages: {}  /Encrypt in file: {encrypt}  page1 text: {:?}", doc.page_count()?, text.chars().take(80).collect::<String>());
    Ok(())
}

fn bench(input: &str, revisions: usize) -> R<()> {
    let base = std::fs::read(input)?;
    let t = Instant::now();
    let doc = PdfDocument::from_bytes(base.clone())?;
    let pages = doc.page_count()?;
    let spans = doc.extract_spans(0)?;
    println!("open+spans p1: {:?} ({pages} pages, {} bytes)", t.elapsed(), base.len());

    // One revision = one highlight on page (i % pages), saved incrementally onto the previous state.
    let mut current = base.clone();
    let mut commit = Vec::new();
    for i in 0..revisions {
        let t = Instant::now();
        std::fs::write("out/bench_cur.pdf", &current)?;
        let mut ed = DocumentEditor::open("out/bench_cur.pdf")?;
        let mut page = ed.get_page(i % pages)?;
        let r = spans.first().map(|s| s.bbox).unwrap_or(Rect::new(50.0, 50.0, 100.0, 12.0));
        page.add_annotation(TextMarkupAnnotation::new(TextMarkupType::Highlight, r, vec![quad(&r)]));
        ed.save_page(page)?;
        ed.save_with_options("out/bench_next.pdf", SaveOptions::incremental())?;
        current = std::fs::read("out/bench_next.pdf")?;
        commit.push(t.elapsed());
    }
    commit.sort();
    println!(
        "commit (open working copy + 1 highlight + incremental save): median {:?}, max {:?}, final {} bytes",
        commit[commit.len() / 2],
        commit.last().unwrap(),
        current.len()
    );

    // Replay: open the base once, apply all revisions, save once (what undo does from a checkpoint).
    let t = Instant::now();
    let mut ed = DocumentEditor::open(input)?;
    for i in 0..revisions {
        let mut page = ed.get_page(i % pages)?;
        let r = spans.first().map(|s| s.bbox).unwrap_or(Rect::new(50.0, 50.0, 100.0, 12.0));
        page.add_annotation(TextMarkupAnnotation::new(TextMarkupType::Highlight, r, vec![quad(&r)]));
        ed.save_page(page)?;
    }
    ed.save_with_options("out/bench_replay.pdf", SaveOptions::incremental())?;
    let inc = std::fs::read("out/bench_replay.pdf")?;
    println!("replay {revisions} revisions from base, incremental save: {:?} ({} bytes)", t.elapsed(), inc.len());

    let t = Instant::now();
    let mut ed = DocumentEditor::open(input)?;
    for i in 0..revisions {
        let mut page = ed.get_page(i % pages)?;
        let r = spans.first().map(|s| s.bbox).unwrap_or(Rect::new(50.0, 50.0, 100.0, 12.0));
        page.add_annotation(TextMarkupAnnotation::new(TextMarkupType::Highlight, r, vec![quad(&r)]));
        ed.save_page(page)?;
    }
    let applied = t.elapsed();
    ed.save_with_options("out/bench_replay_full.pdf", SaveOptions::full_rewrite())?;
    let full = std::fs::read("out/bench_replay_full.pdf")?;
    let n = full.windows(10).filter(|w| w == b"/Highlight").count();
    println!("replay {revisions} revisions from base, full rewrite: {:?} (apply {:?}; {} bytes; {n} highlights in file)", t.elapsed(), applied, full.len());

    let t = Instant::now();
    let mut ed = DocumentEditor::from_bytes(base)?;
    let full = ed.save_to_bytes_with_options(SaveOptions::full_rewrite())?;
    println!("full rewrite, no edits: {:?} ({} bytes)", t.elapsed(), full.len());
    Ok(())
}
