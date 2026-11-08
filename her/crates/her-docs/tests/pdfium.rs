//! DocStore on PDFium, in a worker thread: a PDF written here, and, when the machine has it, the
//! phase-0 corpus's real Chinese broker report (`HER_DOC_CORPUS`).

use her_docs::DocStore;
use her_docs::Launcher;
use her_protocol::doc::Anchor;
use her_protocol::doc::BlockKind;
use her_protocol::doc::Capability;
use her_protocol::doc::Rect;
use std::path::Path;
use std::path::PathBuf;

/// A PDF of `pages`, each a list of (x, y, size, text) in Helvetica.
fn write_pdf(path: &Path, pages: &[Vec<(f32, f32, f32, &str)>]) {
    let raw: Vec<RawPage> = pages
        .iter()
        .map(|lines| RawPage {
            media: [0.0, 0.0, 600.0, 800.0],
            rotate: 0,
            content: lines.iter().map(|(x, y, size, text)| format!("BT /F1 {size} Tf {x} {y} Td ({text}) Tj ET\n")).collect(),
        })
        .collect();
    write_raw(path, &raw);
}

struct RawPage {
    media: [f32; 4],
    rotate: u16,
    content: String,
}

fn write_raw(path: &Path, pages: &[RawPage]) {
    let mut objects: Vec<String> = Vec::new();
    let n = pages.len();
    // 1 catalog, 2 pages, 3 font, then per page: page, contents.
    objects.push("<< /Type /Catalog /Pages 2 0 R >>".into());
    let kids: Vec<String> = (0..n).map(|i| format!("{} 0 R", 4 + i * 2)).collect();
    objects.push(format!("<< /Type /Pages /Kids [{}] /Count {n} >>", kids.join(" ")));
    objects.push("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>".into());
    for (i, page) in pages.iter().enumerate() {
        let [a, b, c, d] = page.media;
        objects.push(format!(
            "<< /Type /Page /Parent 2 0 R /MediaBox [{a} {b} {c} {d}] /Rotate {} /Resources << /Font << /F1 3 0 R >> >> /Contents {} 0 R >>",
            page.rotate,
            5 + i * 2
        ));
        let stream = &page.content;
        objects.push(format!("<< /Length {} >>\nstream\n{stream}endstream", stream.len()));
    }
    let mut out = b"%PDF-1.4\n".to_vec();
    let mut offsets = Vec::new();
    for (i, body) in objects.iter().enumerate() {
        offsets.push(out.len());
        out.extend(format!("{} 0 obj\n{body}\nendobj\n", i + 1).bytes());
    }
    let xref = out.len();
    out.extend(format!("xref\n0 {}\n0000000000 65535 f \n", objects.len() + 1).bytes());
    for o in offsets {
        out.extend(format!("{o:010} 00000 n \n").bytes());
    }
    out.extend(format!("trailer\n<< /Size {} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n", objects.len() + 1).bytes());
    std::fs::write(path, out).unwrap();
}

fn store(dir: &Path) -> DocStore {
    DocStore::new(dir.join("docs"), Launcher::Thread)
}

#[tokio::test]
async fn a_written_pdf_reads_finds_resolves_and_renders() {
    let tmp = tempfile::tempdir().unwrap();
    let file = tmp.path().join("report.pdf");
    write_pdf(
        &file,
        &[
            vec![
                (50.0, 720.0, 24.0, "Annual Report"),
                (50.0, 660.0, 11.0, "Revenue grew 12.5% to 3.2 billion,"),
                (50.0, 646.0, 11.0, "driven by the services segment."),
                (280.0, 20.0, 9.0, "1"),
            ],
            vec![(50.0, 700.0, 11.0, "Risks: the services segment may slow.")],
        ],
    );
    let store = store(tmp.path());
    let (doc, token) = store.open(&file, None, "s1").await.unwrap();
    let info = doc.info();
    assert_eq!(info.pages, Some(2));
    assert!(info.capabilities.contains(&Capability::Text));
    assert!(!info.scanned);
    // The token names the document for the app.
    let token_file = tmp.path().join("docs/.tokens").join(&token);
    assert_eq!(std::fs::read_to_string(token_file).unwrap(), doc.id);
    assert!(doc.current().is_file());

    let page = store.page(&doc, 1).await.unwrap();
    let blocks: Vec<(BlockKind, &str)> = page.blocks.iter().map(|b| (b.kind, b.text.as_str())).collect();
    assert_eq!(
        blocks,
        vec![
            (BlockKind::Heading, "Annual Report"),
            (BlockKind::Body, "Revenue grew 12.5% to 3.2 billion, driven by the services segment."),
            (BlockKind::Margin, "1"),
        ]
    );

    let hits = store.find(&doc, "services segment", 10).await.unwrap();
    assert_eq!(hits.len(), 2);
    assert_eq!(hits[0].anchor.page(), 1);
    assert_eq!(hits[1].anchor.page(), 2);
    assert_eq!(hits[0].block.as_deref(), Some("p1.b1"));
    assert!(hits[0].context.contains("services segment"), "{}", hits[0].context);

    // A selection over the first body line, as the page would send it.
    let (anchor, _, boxes) = store.resolve(&doc, 1, &[Rect { x0: 45.0, y0: 655.0, x1: 400.0, y1: 672.0 }], None).await.unwrap();
    match &anchor {
        Anchor::Text { text, block, quads, .. } => {
            assert_eq!(text, "Revenue grew 12.5% to 3.2 billion,");
            assert_eq!(block.as_deref(), Some("p1.b1"));
            assert_eq!(quads.len(), 1);
        }
        other => panic!("{other:?}"),
    }
    assert_eq!(boxes.len(), "Revenuegrew12.5%to3.2billion,".len());

    // A region drawn over the heading: a region anchor, with the text inside.
    let (anchor, text, _) = store.resolve(&doc, 1, &[], Some(Rect { x0: 40.0, y0: 700.0, x1: 400.0, y1: 760.0 })).await.unwrap();
    assert!(matches!(anchor, Anchor::Region { page: 1, .. }));
    assert_eq!(text, "Annual Report");

    // A citation's targets.
    match store.locate(&doc, "p1.b1").await.unwrap() {
        Anchor::Text { text, quads, .. } => {
            assert!(text.starts_with("Revenue grew"));
            assert_eq!(quads.len(), 2);
        }
        other => panic!("{other:?}"),
    }
    assert_eq!(store.locate(&doc, "p2").await.unwrap(), Anchor::Page { page: 2 });
    assert!(store.locate(&doc, "p1.b9").await.is_err());
    assert!(store.locate(&doc, "p7").await.is_err());

    let png = store.render(&doc, &Anchor::Page { page: 1 }, 72).await.unwrap();
    assert_eq!((png.width, png.height), (600, 800));
    let region = store.render(&doc, &Anchor::Region { page: 1, rect: Rect { x0: 0.0, y0: 400.0, x1: 300.0, y1: 800.0 } }, 144).await.unwrap();
    assert_eq!((region.width, region.height), (600, 800));
    assert!(store.page(&doc, 3).await.is_err());

    // A second session joins the same entry; the last one to close lets it go.
    let (again, other) = store.open(&file, None, "s2").await.unwrap();
    assert!(std::sync::Arc::ptr_eq(&doc, &again));
    assert_ne!(token, other);
    store.set_view(&doc.id, "s2", 2, None).unwrap();
    assert_eq!(store.viewing("s2")[0].page, 2);
    store.close(&doc.id, "s1").await.unwrap();
    assert!(store.get(&doc.id).is_ok());
    store.close(&doc.id, "s2").await.unwrap();
    assert!(store.get(&doc.id).is_err());
    assert!(std::fs::read_dir(tmp.path().join("docs/.tokens")).unwrap().next().is_none());
}

#[tokio::test]
async fn a_changed_file_is_taken_again_and_bad_files_are_refused() {
    let tmp = tempfile::tempdir().unwrap();
    let file = tmp.path().join("a.pdf");
    write_pdf(&file, &[vec![(50.0, 700.0, 11.0, "first version")]]);
    let store = store(tmp.path());
    let (doc, _) = store.open(&file, None, "s").await.unwrap();
    assert_eq!(store.page(&doc, 1).await.unwrap().blocks[0].text, "first version");
    std::thread::sleep(std::time::Duration::from_millis(20));
    write_pdf(&file, &[vec![(50.0, 700.0, 11.0, "second version")], vec![]]);
    let (doc, _) = store.open(&file, None, "s").await.unwrap();
    assert_eq!(doc.pages(), 2);
    assert_eq!(store.page(&doc, 1).await.unwrap().blocks[0].text, "second version");

    let text = tmp.path().join("notes.pdf");
    std::fs::write(&text, "not a pdf").unwrap();
    assert!(matches!(store.open(&text, None, "s").await, Err(her_docs::DocError::Unsupported(_))));
    assert!(matches!(store.open(&tmp.path().join("missing.pdf"), None, "s").await, Err(her_docs::DocError::NotFound(_))));
}

fn corpus(name: &str) -> Option<PathBuf> {
    let dir = PathBuf::from(std::env::var_os("HER_DOC_CORPUS")?);
    let path = dir.join(name);
    path.is_file().then_some(path)
}

/// The report pdf_oxide's Markdown lost punctuation in (design §9.2.3): it must come through.
#[tokio::test]
async fn the_broker_report_keeps_its_punctuation() {
    let Some(file) = corpus("1.pdf") else { return };
    let tmp = tempfile::tempdir().unwrap();
    let store = store(tmp.path());
    let (doc, _) = store.open(&file, None, "s").await.unwrap();
    let page = store.page(&doc, 1).await.unwrap();
    let body: String = page.blocks.iter().filter(|b| b.kind != BlockKind::Margin).map(|b| b.text.as_str()).collect::<Vec<_>>().join("\n");
    for expected in ["20-25%", "10%-20%", "5%-10%", "021-38674944"] {
        assert!(body.contains(expected), "{expected} missing from:\n{body}");
    }
    // The vertical "证券研究报告" in the margin does not break into the body.
    assert!(!body.contains("研 比超过"), "{body}");
    let hits = store.find(&doc, "20-25%", 5).await.unwrap();
    assert!(!hits.is_empty());
}

#[tokio::test]
async fn a_document_is_found_again_by_its_id() {
    let tmp = tempfile::tempdir().unwrap();
    let file = tmp.path().join("cited.pdf");
    write_pdf(&file, &[vec![(50.0, 700.0, 11.0, "cited text")]]);
    let id = {
        let store = store(tmp.path());
        let (doc, _) = store.open(&file, None, "s").await.unwrap();
        doc.id.clone()
    };
    // Another run: nothing open, the directory remembers the file.
    let store = store(tmp.path());
    assert_eq!(store.path_of(&id).unwrap(), std::fs::canonicalize(&file).unwrap());
    assert!(store.path_of("../etc").is_err());
    assert!(store.path_of("0123456789abcdef").is_err());
}

/// The colours of a PNG (base64), as RGBA bytes, with its size.
fn pixels(png_b64: &str) -> (u32, u32, Vec<u8>) {
    use base64::Engine as _;
    let bytes = base64::engine::general_purpose::STANDARD.decode(png_b64).unwrap();
    let mut decoder = png::Decoder::new(std::io::Cursor::new(bytes)).read_info().unwrap();
    let mut buf = vec![0; decoder.output_buffer_size().unwrap()];
    let info = decoder.next_frame(&mut buf).unwrap();
    buf.truncate(info.buffer_size());
    (info.width, info.height, buf)
}

fn has_ink(rgba: &[u8]) -> bool {
    rgba.chunks(4).any(|p| p[0] < 128 && p[1] < 128 && p[2] < 128)
}

/// A landscape page as such pages are made: a box away from the origin, turned 90° when shown,
/// its text drawn up the page so that it reads across once turned.
#[tokio::test]
async fn a_page_turned_on_its_side_reads_in_lines_and_renders_its_regions() {
    let tmp = tempfile::tempdir().unwrap();
    let file = tmp.path().join("landscape.pdf");
    // Shown, the page is 600 wide and 800 tall; user x goes down the shown page, user y across.
    let up = |size: f32, x: f32, y: f32, text: &str| format!("BT /F1 {size} Tf 0 1 -1 0 {x} {y} Tm ({text}) Tj ET\n");
    let content = [
        up(24.0, 170.0, 100.0, "Rotated Report"),
        up(11.0, 220.0, 100.0, "Revenue grew 12.5% to 3.2 billion,"),
        up(11.0, 234.0, 100.0, "driven by services."),
        up(9.0, 890.0, 330.0, "1"),
    ]
    .concat();
    write_raw(&file, &[RawPage { media: [100.0, 50.0, 900.0, 650.0], rotate: 90, content }]);
    let store = store(tmp.path());
    let (doc, _) = store.open(&file, None, "s").await.unwrap();
    let page = store.page(&doc, 1).await.unwrap();
    assert_eq!((page.chars.width, page.chars.height), (600.0, 800.0));
    let blocks: Vec<(BlockKind, &str)> = page.blocks.iter().map(|b| (b.kind, b.text.as_str())).collect();
    assert_eq!(
        blocks,
        vec![
            (BlockKind::Heading, "Rotated Report"),
            (BlockKind::Body, "Revenue grew 12.5% to 3.2 billion, driven by services."),
            (BlockKind::Margin, "1"),
        ]
    );
    // Rectangles stay in user space, where pdf.js's points are: the text runs up the page there.
    let heading = page.blocks[0].rect;
    assert!(heading.height() > heading.width() * 3.0, "{heading:?}");
    assert!(heading.x0 > 140.0 && heading.x1 < 175.0, "{heading:?}");

    let hits = store.find(&doc, "services", 5).await.unwrap();
    match &hits[0].anchor {
        Anchor::Text { quads, .. } => assert!(quads[0].height() > quads[0].width(), "{quads:?}"),
        other => panic!("{other:?}"),
    }

    // A selection over the first body line, in user space.
    let (anchor, text, _) = store.resolve(&doc, 1, &[Rect { x0: 205.0, y0: 80.0, x1: 224.0, y1: 600.0 }], None).await.unwrap();
    assert_eq!(text, "Revenue grew 12.5% to 3.2 billion,");
    assert!(matches!(anchor, Anchor::Text { .. }));

    // The heading's region, rendered: turned as shown (wider than tall), with the text in it;
    // an empty part of the page has none.
    let shot = store.render(&doc, &Anchor::Region { page: 1, rect: heading }, 72).await.unwrap();
    let (w, h, rgba) = pixels(&shot.png);
    assert!(w > h * 3, "{w}×{h}");
    assert!(has_ink(&rgba), "the heading's region is blank");
    let empty = Rect { x0: 500.0, y0: 400.0, x1: 600.0, y1: 500.0 };
    let (_, _, rgba) = pixels(&store.render(&doc, &Anchor::Region { page: 1, rect: empty }, 72).await.unwrap().png);
    assert!(!has_ink(&rgba), "an empty region has ink");
    let (w, h, _) = pixels(&store.render(&doc, &Anchor::Page { page: 1 }, 72).await.unwrap().png);
    assert_eq!((w, h), (600, 800));
}

// ---- encrypted files: the standard security handler, RC4, revision 2 (40-bit) ----

const PAD: [u8; 32] = [
    0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08, 0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E,
    0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
];

fn rc4(key: &[u8], data: &[u8]) -> Vec<u8> {
    let mut s: Vec<u8> = (0..=255).collect();
    let mut j = 0usize;
    for i in 0..256 {
        j = (j + s[i] as usize + key[i % key.len()] as usize) % 256;
        s.swap(i, j);
    }
    let (mut i, mut j) = (0usize, 0usize);
    data.iter()
        .map(|b| {
            i = (i + 1) % 256;
            j = (j + s[i] as usize) % 256;
            s.swap(i, j);
            b ^ s[(s[i] as usize + s[j] as usize) % 256]
        })
        .collect()
}

fn md5(parts: &[&[u8]]) -> [u8; 16] {
    use md5::Digest as _;
    let mut h = md5::Md5::new();
    for p in parts {
        h.update(p);
    }
    h.finalize().into()
}

fn padded(password: &str) -> Vec<u8> {
    password.bytes().chain(PAD).take(32).collect()
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02X}")).collect()
}

/// A one-page PDF with `text`, encrypted with `password` for opening.
fn write_encrypted(path: &Path, password: &str, text: &str) {
    let id = [7u8; 16];
    let p: i32 = -4;
    let o = rc4(&md5(&[&padded("owner")])[..5], &padded(password));
    let key = md5(&[&padded(password), &o, &p.to_le_bytes(), &id])[..5].to_vec();
    let u = rc4(&key, &PAD);
    let stream = format!("BT /F1 18 Tf 50 700 Td ({text}) Tj ET\n");
    // Streams are encrypted with the object's own key.
    let object_key = |n: u32| md5(&[&key, &n.to_le_bytes()[..3], &[0, 0]])[..10].to_vec();
    let content = rc4(&object_key(5), stream.as_bytes());
    let mut objects: Vec<Vec<u8>> = vec![
        b"<< /Type /Catalog /Pages 2 0 R >>".to_vec(),
        b"<< /Type /Pages /Kids [4 0 R] /Count 1 >>".to_vec(),
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>".to_vec(),
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 600 800] /Resources << /Font << /F1 3 0 R >> >> /Contents 5 0 R >>".to_vec(),
    ];
    let mut body = format!("<< /Length {} >>\nstream\n", content.len()).into_bytes();
    body.extend(&content);
    body.extend(b"\nendstream");
    objects.push(body);
    objects.push(format!("<< /Filter /Standard /V 1 /R 2 /O <{}> /U <{}> /P {p} >>", hex(&o), hex(&u)).into_bytes());
    let mut out = b"%PDF-1.4\n".to_vec();
    let mut offsets = Vec::new();
    for (i, obj) in objects.iter().enumerate() {
        offsets.push(out.len());
        out.extend(format!("{} 0 obj\n", i + 1).bytes());
        out.extend(obj);
        out.extend(b"\nendobj\n");
    }
    let xref = out.len();
    out.extend(format!("xref\n0 {}\n0000000000 65535 f \n", objects.len() + 1).bytes());
    for o in offsets {
        out.extend(format!("{o:010} 00000 n \n").bytes());
    }
    out.extend(
        format!("trailer\n<< /Size {} /Root 1 0 R /Encrypt 6 0 R /ID [<{}> <{}>] >>\nstartxref\n{xref}\n%%EOF\n", objects.len() + 1, hex(&id), hex(&id)).bytes(),
    );
    std::fs::write(path, out).unwrap();
}

#[tokio::test]
async fn an_encrypted_document_opens_with_its_password_only() {
    let tmp = tempfile::tempdir().unwrap();
    let file = tmp.path().join("locked.pdf");
    write_encrypted(&file, "s3cret", "Confidential terms");
    let store = store(tmp.path());
    assert!(matches!(store.open(&file, None, "s").await, Err(her_docs::DocError::Password)));
    assert!(matches!(store.open(&file, Some("wrong".into()), "s").await, Err(her_docs::DocError::Password)));
    let (doc, _) = store.open(&file, Some("s3cret".into()), "s").await.unwrap();
    assert!(doc.info().encrypted);
    assert_eq!(doc.password(), Some("s3cret"));
    // Another session joins the open document without asking again.
    let (again, _) = store.open(&file, None, "other").await.unwrap();
    assert_eq!(again.password(), Some("s3cret"));
    assert_eq!(store.page(&doc, 1).await.unwrap().blocks[0].text, "Confidential terms");
    // Nothing of the password is kept on disk.
    for entry in std::fs::read_dir(tmp.path().join("docs").join(&doc.id)).unwrap() {
        let bytes = std::fs::read(entry.unwrap().path()).unwrap();
        assert!(!bytes.windows(6).any(|w| w == b"s3cret"));
    }
}
