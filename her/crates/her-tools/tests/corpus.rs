//! read, write and edit on a corpus of complex files, checked against simple reference models.
//!
//! The files (`support::corpus`) cover every encoding and line break read knows, characters of
//! many kinds, sizes at the boundaries the tools care about, long lines, repeated blocks and
//! source code. Every call goes through the tools' public interface, as the kernel makes it, and
//! its result is compared with `support::model`. Seeds are fixed and named in every failure;
//! `HER_CORPUS_SEED=<n>` explores others. The large cases run with
//! `cargo test -p her-tools --release -- --ignored`.

mod support;

use serde_json::Value;
use serde_json::json;
use std::sync::OnceLock;
use support::Session;
use support::assert_same;
use support::corpus::KIB;
use support::corpus::MIB;
use support::corpus::Sample;
use support::excerpt;
use support::model::ReadModel;
use support::text;

const SEED: u64 = 0xC0FF_EE00_F11E;

fn seed() -> u64 {
    support::seed(SEED)
}

/// The corpus, generated once for all tests.
fn corpus() -> &'static [Sample] {
    static CORPUS: OnceLock<Vec<Sample>> = OnceLock::new();
    CORPUS.get_or_init(|| support::corpus::samples(seed()))
}

fn sample(name: &str) -> &'static Sample {
    corpus().iter().find(|s| s.name == name).unwrap_or_else(|| panic!("no sample {name}"))
}

/// A read that matched the model.
struct Page {
    /// The text, or the error.
    shown: Result<String, String>,
    /// The offset its note gives.
    next: Option<u64>,
}

/// Checks one read against the model.
async fn check_read(
    s: &Session,
    name: &str,
    model: &ReadModel,
    offset: Option<u64>,
    limit: Option<u64>,
    what: &str,
) -> Page {
    let mut args = json!({"path": name});
    if let Some(offset) = offset {
        args["offset"] = json!(offset);
    }
    if let Some(limit) = limit {
        args["limit"] = json!(limit);
    }
    let what = format!("{what}: read {name} offset {offset:?} limit {limit:?}");
    match (s.read(args).await, model.expected(&s.display(name), offset, limit)) {
        (Ok(out), Ok((expected, details))) => {
            let got = text(&out);
            assert_same(got.as_bytes(), expected.as_bytes(), &what);
            assert_eq!(out.details.as_ref(), Some(&details), "{what}");
            let next = support::next_offset(got);
            let more = details["truncated"].as_bool() == Some(true);
            assert_eq!(next, more.then(|| details["end_line"].as_u64().unwrap() + 1), "{what}: the note");
            Page {
                shown: Ok(got.to_string()),
                next,
            }
        }
        (Err(err), Err(expected)) => {
            assert_eq!(err.to_string(), expected, "{what}");
            Page {
                shown: Err(expected),
                next: None,
            }
        }
        (Ok(out), Err(expected)) => panic!("{what}: expected the error {expected:?}, got {}", excerpt(text(&out))),
        (Err(err), Ok(_)) => panic!("{what}: {err}"),
    }
}

/// Reads `name` from the start to the end, `limit` lines at a time, following the notes.
async fn page_through(s: &Session, name: &str, model: &ReadModel, limit: Option<u64>, what: &str) -> usize {
    let mut offset = None;
    let mut pages = 0;
    loop {
        pages += 1;
        match check_read(s, name, model, offset, limit, what).await.next {
            Some(next) => offset = Some(next),
            None => return pages,
        }
    }
}

mod read {
    use super::*;
    use rand::Rng;
    use rand::seq::SliceRandom;
    use std::sync::Arc;
    use std::time::Duration;
    use support::corpus::rng;
    use support::model::line_starts;

    /// Every file of the corpus within `sizes`, read page by page; small ones also a few lines
    /// at a time.
    async fn every_page(sizes: std::ops::Range<usize>) {
        let s = Session::new();
        let mut files = 0;
        for sample in corpus().iter().filter(|s| sizes.contains(&s.bytes.len())) {
            let path = s.put(&sample.name, &sample.bytes);
            let what = format!("seed {:#x}, {}", seed(), sample.name);
            let Some(model) = ReadModel::of(&sample.bytes) else {
                let err = support::error(s.read(json!({"path": sample.name})).await);
                let expected = format!(
                    "{} is a binary file ({} bytes) and cannot be shown as text.",
                    path.display(),
                    sample.bytes.len()
                );
                assert_eq!(err, expected, "{what}");
                continue;
            };
            let size = sample.bytes.len();
            let limit = if size <= KIB {
                Some(1)
            } else if size <= 8 * KIB {
                Some(3)
            } else if size <= 64 * KIB {
                Some(500)
            } else {
                None
            };
            page_through(&s, &sample.name, &model, None, &what).await;
            if limit.is_some() {
                page_through(&s, &sample.name, &model, limit, &what).await;
            }
            files += 1;
        }
        assert!(files > 0);
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn every_page_of_small_files_is_the_models() {
        every_page(0..64 * KIB + 1).await;
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn every_page_of_streamed_files_is_the_models() {
        every_page(64 * KIB + 1..MIB + 100 * KIB).await;
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn every_page_of_indexed_files_is_the_models() {
        every_page(MIB + 100 * KIB..usize::MAX).await;
    }

    /// Windows of `bytes` worth reading: the lines at 64 KiB and around every MiB, where
    /// buffers end, line index checkpoints fall and reads from a checkpoint read 128 KiB at a
    /// time; the last lines and past them; and some anywhere.
    fn windows(rng: &mut impl Rng, bytes: &[u8], model: &ReadModel) -> Vec<(Option<u64>, Option<u64>)> {
        let total = model.total();
        let mut offsets = vec![1, 2, total.saturating_sub(1), total, total + 1, total + 7];
        if let Some(starts) = line_starts(bytes) {
            let mut boundaries = vec![64 * KIB];
            for mib in (MIB..bytes.len()).step_by(MIB) {
                boundaries.extend([mib - 128 * KIB, mib, mib + 128 * KIB, mib + 256 * KIB]);
            }
            for boundary in boundaries.into_iter().filter(|b| *b < bytes.len()) {
                // The 1-based line holding the boundary byte.
                let line = starts.partition_point(|&s| s <= boundary) as u64;
                offsets.extend([line, line + 1]);
            }
        }
        offsets.extend((0..6).map(|_| rng.random_range(1..=total.max(1))));
        let limits = [None, Some(1), Some(2), Some(3), Some(40), Some(2000), Some(0)];
        offsets
            .into_iter()
            .map(|offset| (Some(offset.max(1)), limits[rng.random_range(0..limits.len())]))
            .collect()
    }

    /// Any window of a file, read in any order, again from the line index, and by a new
    /// environment without one, is the same text.
    async fn windows_again(sizes: std::ops::Range<usize>, salt: u64) {
        let seed = seed() ^ salt;
        let mut rng = rng(seed);
        let s = Session::new();
        for sample in corpus().iter().filter(|s| sizes.contains(&s.bytes.len())) {
            let Some(model) = ReadModel::of(&sample.bytes) else { continue };
            s.put(&sample.name, &sample.bytes);
            let what = format!("seed {seed:#x}, {}", sample.name);
            let mut windows = windows(&mut rng, &sample.bytes, &model);
            windows.shuffle(&mut rng);
            let mut first = Vec::new();
            for &(offset, limit) in &windows {
                first.push(check_read(&s, &sample.name, &model, offset, limit, &what).await.shown);
            }
            let mut order: Vec<usize> = (0..windows.len()).collect();
            order.shuffle(&mut rng);
            let fresh = s.fresh_env();
            for (i, &k) in order.iter().enumerate() {
                let (offset, limit) = windows[k];
                // Every other one also from a new environment, which scans the file again.
                let sessions = if i % 2 == 0 { &[&s, &fresh][..] } else { &[&s][..] };
                for session in sessions {
                    let again = check_read(session, &sample.name, &model, offset, limit, &what).await.shown;
                    assert_eq!(again, first[k], "{what}: offset {offset:?} limit {limit:?}, read again");
                }
            }
        }
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn windows_of_streamed_files_read_again_are_the_same() {
        windows_again(64 * KIB + 1..MIB + 100 * KIB, 0x11).await;
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn windows_of_indexed_files_read_again_are_the_same() {
        windows_again(MIB + 100 * KIB..usize::MAX, 0x12).await;
    }

    /// A file changed after it was read (and indexed) is read anew: in place with the same size
    /// and modification time, renamed over, grown and shrunk.
    #[tokio::test]
    async fn a_changed_file_is_read_anew() {
        let s = Session::new();
        let name = "big/mixed-3m.txt";
        let original = sample(name).bytes.clone();
        let path = s.put(name, &original);
        let what = format!("seed {:#x}, {name}", seed());
        let probe = async |bytes: &[u8], label: &str| {
            let model = ReadModel::of(bytes).unwrap();
            let total = model.total();
            for offset in [1, total / 3, total / 2, total - 5, total + 1] {
                check_read(&s, name, &model, Some(offset), Some(30), &format!("{what}, {label}")).await;
            }
        };
        probe(&original, "original").await;

        // Same size and modification time, but other lines: a stale index would give the old
        // line count.
        let mut changed = original.clone();
        let at = 2 * MIB + 100;
        changed[at..at + 64].copy_from_slice(&[b'\n'; 64]);
        support::patch_in_place(&path, at as u64, &[b'\n'; 64]);
        assert_eq!(std::fs::read(&path).unwrap(), changed);
        probe(&changed, "changed in place").await;

        // Renamed over by a file of the same size with the same modification time.
        let mut renamed = changed.clone();
        renamed[10..20].copy_from_slice(b"\r\r\r\r\r\r\r\r\r\r");
        let other = s.put("other.txt", &renamed);
        let file = std::fs::File::options().write(true).open(&other).unwrap();
        file.set_modified(std::fs::metadata(&path).unwrap().modified().unwrap()).unwrap();
        std::fs::rename(&other, &path).unwrap();
        probe(&renamed, "renamed over").await;

        let mut grown = b"first\nsecond\r\n".to_vec();
        grown.extend_from_slice(&renamed);
        std::fs::write(&path, &grown).unwrap();
        probe(&grown, "grown").await;

        let shrunk = renamed[..MIB + 5].to_vec();
        std::fs::write(&path, &shrunk).unwrap();
        probe(&shrunk, "shrunk").await;
        let small = &renamed[..10 * KIB];
        std::fs::write(&path, small).unwrap();
        probe(small, "small").await;
    }

    /// Many reads of one large file at once, through one environment and its line index.
    #[tokio::test(flavor = "multi_thread", worker_threads = 8)]
    async fn concurrent_reads_of_one_file_agree() {
        let seed = seed() ^ 0x22;
        let s = Session::new();
        let name = "big/mixed-3m.txt";
        let bytes = &sample(name).bytes;
        s.put(name, bytes);
        let model = Arc::new(ReadModel::of(bytes).unwrap());
        let mut tasks = Vec::new();
        for task in 0..8u64 {
            let (s, model) = (s.clone(), model.clone());
            tasks.push(tokio::spawn(async move {
                let mut rng = rng(seed + task);
                let what = format!("seed {seed:#x}, task {task}");
                for _ in 0..12 {
                    let offset = rng.random_range(1..=model.total() + 1);
                    let limit = [None, Some(1), Some(100)][rng.random_range(0..3)];
                    check_read(&s, name, &model, Some(offset), limit, &what).await;
                }
            }));
        }
        for task in tasks {
            task.await.unwrap();
        }
    }

    /// Named pipes, sockets and devices fail at once, without waiting for data that never
    /// comes; a directory says to list it.
    #[cfg(unix)]
    #[tokio::test]
    async fn special_files_fail_at_once() {
        use std::os::unix::ffi::OsStrExt;
        let s = Session::new();
        let fifo = s.path("pipe");
        let name = std::ffi::CString::new(fifo.as_os_str().as_bytes()).unwrap();
        assert_eq!(unsafe { libc::mkfifo(name.as_ptr(), 0o644) }, 0);
        let _socket = std::os::unix::net::UnixListener::bind(s.path("sock")).unwrap();
        std::os::unix::fs::symlink("/dev/zero", s.path("zero")).unwrap();
        std::os::unix::fs::symlink("pipe", s.path("to-pipe")).unwrap();
        std::fs::create_dir(s.path("dir")).unwrap();
        let cases = [
            ("pipe", "is not a regular file (named pipe); it cannot be read."),
            ("to-pipe", "is not a regular file (named pipe); it cannot be read."),
            ("sock", "is not a regular file (socket); it cannot be read."),
            ("zero", "is not a regular file (character device); it cannot be read."),
            ("/dev/null", "is not a regular file (character device); it cannot be read."),
            ("dir", "is a directory, not a file. Use bash (ls) to list it."),
        ];
        for (name, tail) in cases {
            let call = s.read(json!({"path": name}));
            let result = tokio::time::timeout(Duration::from_secs(5), call).await.expect("returns at once");
            let display = if name.starts_with('/') { name.to_string() } else { s.display(name) };
            assert_eq!(support::error(result), format!("{display} {tail}"));
        }
    }

    /// Images are recognized by their signature, measured from their headers, and refused
    /// past the provider limits.
    #[tokio::test]
    async fn images_by_signature_size_and_dimensions() {
        use base64::Engine;
        let mut s = Session::new();
        let jpeg = |width: u16, height: u16| {
            // SOI, an APP0 segment, then SOF0 with height and width; padded to 1536 bytes.
            let mut bytes = vec![0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10];
            bytes.extend_from_slice(b"JFIF\0\x01\x01\0\0\x01\0\x01\0\0");
            bytes.extend_from_slice(&[0xFF, 0xFF, 0xC0, 0x00, 0x11, 0x08]);
            bytes.extend_from_slice(&height.to_be_bytes());
            bytes.extend_from_slice(&width.to_be_bytes());
            bytes.resize(1536, 0);
            bytes
        };
        let gif = |width: u16, height: u16| {
            let mut bytes = b"GIF89a".to_vec();
            bytes.extend_from_slice(&width.to_le_bytes());
            bytes.extend_from_slice(&height.to_le_bytes());
            bytes.resize(1536, 0);
            bytes
        };
        let webp = |width: u32, height: u32| {
            let mut bytes = b"RIFF\0\0\0\0WEBPVP8X\x0a\0\0\0\0\0\0\0".to_vec();
            bytes.extend_from_slice(&(width - 1).to_le_bytes()[..3]);
            bytes.extend_from_slice(&(height - 1).to_le_bytes()[..3]);
            bytes.resize(1536, 0);
            bytes
        };
        let cases: [(&str, Vec<u8>, &str); 4] = [
            ("photo.jpg", jpeg(640, 480), "JPEG, 640x480, 1.5 KB"),
            ("anim.gif", gif(320, 200), "GIF, 320x200, 1.5 KB"),
            ("pic.webp", webp(8000, 1), "WebP, 8000x1, 1.5 KB"),
            // The signature decides, not the name.
            ("notes.txt", jpeg(16, 9), "JPEG, 16x9, 1.5 KB"),
        ];
        for (name, bytes, _) in &cases {
            s.put(name, bytes);
            let out = s.read(json!({"path": name})).await.unwrap();
            let expected = format!("[Image file {} (1536 bytes). The current model does not accept images.]", s.display(name));
            assert_eq!(text(&out), expected);
        }
        s.ctx.model_accepts_images = true;
        for (name, bytes, label) in &cases {
            let out = s.read(json!({"path": name})).await.unwrap();
            let [her_llm::UserContent::Text { text }, her_llm::UserContent::Image { data, .. }] = out.content.as_slice()
            else {
                panic!("{name}: {:?}", out.content.len());
            };
            assert_eq!(text, &format!("Image: {} ({label})", s.display(name)));
            assert_eq!(&base64::engine::general_purpose::STANDARD.decode(data).unwrap(), bytes);
        }
        for (name, bytes, pixels) in [
            ("wide.jpg", jpeg(8001, 10), "8001x10"),
            ("tall.gif", gif(1, 9000), "1x9000"),
            ("big.webp", webp(8001, 8001), "8001x8001"),
        ] {
            s.put(name, &bytes);
            let err = support::error(s.read(json!({"path": name})).await);
            assert!(err.contains(&format!(" is {pixels} pixels; the limit is 8000 pixels per side.")), "{err}");
        }
    }

    /// Numbers as strings or floats, limits out of range and other names for path.
    #[tokio::test]
    async fn arguments_in_odd_shapes() {
        let s = Session::new();
        let name = "code/rs-lf.rs";
        let bytes = &sample(name).bytes;
        s.put(name, bytes);
        let model = ReadModel::of(bytes).unwrap();
        let display = s.display(name);
        for (args, offset, limit) in [
            (json!({"path": name, "offset": "3", "limit": "2"}), Some(3), Some(2)),
            (json!({"path": name, "offset": 4.0, "limit": 1.0}), Some(4), Some(1)),
            (json!({"path": name, "offset": 0, "limit": 0}), Some(1), Some(1)),
            (json!({"path": name, "offset": null, "limit": 5000}), None, Some(2000)),
            (json!({"file_path": name, "offset": " 7 "}), Some(7), None),
        ] {
            let args = her_tools::Tool::prepare_arguments(&her_tools::Typed::new(her_tools::builtin::ReadTool), args);
            let out = s.read(args.clone()).await.unwrap();
            let (expected, _) = model.expected(&display, offset, limit).unwrap();
            assert_eq!(text(&out), expected, "{args}");
        }
        for args in [json!({"path": name, "offset": -1}), json!({"path": name, "limit": "ten"}), json!({"offset": 1})] {
            let err = support::error(s.read(args.clone()).await);
            assert!(err.starts_with("invalid arguments: "), "{args}: {err}");
        }
    }

    /// A path that names nothing is corrected to the file it means, and the result says so
    /// after any note of its own.
    #[tokio::test]
    async fn a_mistyped_path_reads_the_file_it_means() {
        let s = Session::new();
        let real = "Shot d\u{2019}été 2026-10-29 at 10.15.32\u{202F}PM.txt";
        let bytes = &sample("code/js-lf.js").bytes;
        s.put(real, bytes);
        let model = ReadModel::of(bytes).unwrap();
        let note = format!("[Read {}; the path given does not exist.]", s.display(real));
        let typed = "@Shot d'été 2026-10-29 at 10.15.32 PM.txt";
        let mut offset = None;
        loop {
            let mut args = json!({"path": typed, "limit": 150});
            if let Some(offset) = offset {
                args["offset"] = json!(offset);
            }
            let got = s.read_text(args).await;
            let (page, _) = model.expected(&s.display(real), offset, Some(150)).unwrap();
            let gap = if page.ends_with('\n') { "\n" } else { "\n\n" };
            assert_eq!(got, format!("{page}{gap}{note}"));
            match support::next_offset(&got) {
                Some(next) => offset = Some(next),
                None => break,
            }
        }
        s.put("empty.txt", b"");
        assert_eq!(s.read_text(json!({"path": "@empty.txt"})).await, format!(
            "(empty file)\n\n[Read {}; the path given does not exist.]",
            s.display("empty.txt")
        ));
    }
}

mod edit {
    use super::*;
    use rand::Rng;
    use rand::rngs::SmallRng;
    use rand::seq::IndexedRandom;
    use std::collections::BTreeMap;
    use support::corpus::rng;
    use support::model::EditModel;
    use support::model::Edited;
    use support::model::Level;
    use support::model::Located;
    use support::model::check_diff;
    use support::model::diff_for_model;
    use support::model::fold_char;
    use support::model::lf;

    /// What old_text is copied from: the file as read shows it, line breaks as `\n`.
    pub fn shown(raw: &str) -> String {
        lf(raw.strip_prefix('\u{FEFF}').unwrap_or(raw))
    }

    fn floor(text: &str, mut at: usize) -> usize {
        while !text.is_char_boundary(at) {
            at -= 1;
        }
        at
    }

    /// A random span of `text`: whole lines, with or without the last line break, or a few
    /// characters.
    fn span(rng: &mut SmallRng, text: &str) -> (usize, usize) {
        if text.is_empty() {
            return (0, 0);
        }
        let at = floor(text, rng.random_range(0..text.len()));
        if rng.random_bool(0.5) {
            let start = text[..at].rfind('\n').map_or(0, |i| i + 1);
            let mut end = start;
            for _ in 0..rng.random_range(1..=5) {
                match text[end..].find('\n') {
                    Some(i) => end += i + 1,
                    None => {
                        end = text.len();
                        break;
                    }
                }
            }
            if end - start > 1 && text[..end].ends_with('\n') && rng.random_bool(0.5) {
                end -= 1;
            }
            (start, end)
        } else {
            let n = rng.random_range(1..=40);
            let end = text[at..].char_indices().nth(n).map_or(text.len(), |(i, _)| at + i);
            (at, end)
        }
    }

    /// A span of `text` that occurs once in the file, grown a line at a time until it does.
    fn unique_span(rng: &mut SmallRng, model: &EditModel, text: &str) -> Option<(usize, usize)> {
        for _ in 0..6 {
            let (start, mut end) = span(rng, text);
            for _ in 0..4 {
                if end == start {
                    break;
                }
                if let Located::Unique { level: Level::Exact, .. } = model.locate(&text[start..end]) {
                    return Some((start, end));
                }
                match text[end..].find('\n') {
                    Some(i) => end += i + 1,
                    None if end < text.len() => end = text.len(),
                    None => break,
                }
            }
        }
        None
    }

    /// Replacement text: nothing, a word, lines with `\n` or `\r\n`, characters of every kind.
    /// Never a U+FEFF first: at the start of a file that is a byte order mark, which read and
    /// edit do not show.
    pub fn new_text(rng: &mut SmallRng) -> String {
        let text = any_text(rng);
        text.strip_prefix('\u{FEFF}').map(str::to_string).unwrap_or(text)
    }

    fn any_text(rng: &mut SmallRng) -> String {
        use support::corpus::mixed_line;
        match rng.random_range(0..7) {
            0 => String::new(),
            1 => ["x", "value", "日本", "😀", "“q”", "\t"].choose(rng).unwrap().to_string(),
            2 => mixed_line(rng, 6),
            3 => (0..rng.random_range(2..5)).map(|_| mixed_line(rng, 5)).collect::<Vec<_>>().join("\n"),
            4 => format!("{}\r\n{}\r\n", mixed_line(rng, 4), mixed_line(rng, 4)),
            5 => format!("\n{}\n", mixed_line(rng, 4)),
            _ => format!("{}\n", mixed_line(rng, 8)),
        }
    }

    /// Each line of `text` changed by `f`.
    fn per_line(text: &str, f: impl Fn(&str) -> String) -> String {
        text.split('\n').map(f).collect::<Vec<_>>().join("\n")
    }

    #[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
    enum Kind {
        /// One old_text, unique.
        One,
        /// A few unique old_texts.
        Batch,
        /// A short old_text, likely repeated.
        Common,
        /// old_text without the trailing whitespace the file has.
        NoTrailing,
        /// old_text with trailing whitespace the file does not have.
        ExtraTrailing,
        /// old_text with typographic characters typed as ASCII.
        Ascii,
        /// old_text with ASCII quotes and dashes typed as typographic ones.
        Curly,
        /// old_text with other indentation.
        Indented,
        /// new_text equal to old_text.
        Same,
        /// Two old_texts that overlap.
        Overlap,
        /// old_text that is not in the file, or whose later lines differ.
        Absent,
        /// Many old_texts of several lines: views get a line index.
        Many,
    }

    const KINDS: [Kind; 12] = [
        Kind::One,
        Kind::Batch,
        Kind::Common,
        Kind::NoTrailing,
        Kind::ExtraTrailing,
        Kind::Ascii,
        Kind::Curly,
        Kind::Indented,
        Kind::Same,
        Kind::Overlap,
        Kind::Absent,
        Kind::Many,
    ];

    /// Edits of `kind` for the file whose text read shows as `text`; `None` when the file has
    /// nothing for this kind.
    fn edits(rng: &mut SmallRng, model: &EditModel, text: &str, kind: Kind) -> Option<Vec<(String, String)>> {
        let one = |rng: &mut SmallRng| unique_span(rng, model, text).map(|(s, e)| text[s..e].to_string());
        Some(match kind {
            Kind::One => vec![(one(rng)?, new_text(rng))],
            Kind::Batch | Kind::Many => {
                let (want, lines) = if kind == Kind::Many { (20, true) } else { (rng.random_range(2..=5), false) };
                let mut spans: Vec<(usize, usize)> = Vec::new();
                for _ in 0..want * 3 {
                    let Some((start, end)) = unique_span(rng, model, text) else { continue };
                    if lines && text[start..end].matches('\n').count() < 2 {
                        continue;
                    }
                    if spans.iter().all(|&(s, e)| end <= s || start >= e) {
                        spans.push((start, end));
                    }
                    if spans.len() == want {
                        break;
                    }
                }
                if spans.len() < 2 || (kind == Kind::Many && spans.len() < 16) {
                    return None;
                }
                spans.into_iter().map(|(s, e)| (text[s..e].to_string(), new_text(rng))).collect()
            }
            Kind::Common => {
                let at = floor(text, rng.random_range(0..text.len().max(1)));
                let end = text[at..].char_indices().nth(rng.random_range(1..=2)).map_or(text.len(), |(i, _)| at + i);
                if at == end {
                    return None;
                }
                vec![(text[at..end].to_string(), new_text(rng))]
            }
            Kind::NoTrailing => {
                let old = one(rng)?;
                let trimmed = per_line(&old, |l| l.trim_end().to_string());
                (trimmed != old && trimmed.contains(|c: char| !c.is_whitespace())).then_some(())?;
                vec![(trimmed, new_text(rng))]
            }
            Kind::ExtraTrailing => {
                let old = one(rng)?;
                let lines = old.split('\n').count();
                let padded = per_line(&old, |l| if l.is_empty() { String::new() } else { format!("{l} \t") });
                (lines > 1 || rng.random_bool(0.3)).then_some(())?;
                vec![(padded, new_text(rng))]
            }
            Kind::Ascii => {
                let old = one(rng)?;
                let ascii: String = old.chars().map(|c| fold_char(c).unwrap_or(c)).collect();
                (ascii != old).then_some(())?;
                vec![(ascii, new_text(rng))]
            }
            Kind::Curly => {
                let old = one(rng)?;
                let mut open = false;
                let curly: String = old
                    .chars()
                    .map(|c| match c {
                        '"' => {
                            open = !open;
                            if open { '“' } else { '”' }
                        }
                        '\'' => '’',
                        '-' => '–',
                        ' ' if rng.random_ratio(1, 20) => '\u{A0}',
                        c => c,
                    })
                    .collect();
                (curly != old).then_some(())?;
                vec![(curly, new_text(rng))]
            }
            Kind::Indented => {
                let old = one(rng)?;
                let indented = if rng.random_bool(0.5) {
                    per_line(&old, |l| format!("  {l}"))
                } else {
                    per_line(&old, |l| l.trim_start().to_string())
                };
                (indented != old).then_some(())?;
                vec![(indented, new_text(rng))]
            }
            Kind::Same => {
                let old = one(rng)?;
                vec![(old.clone(), old)]
            }
            Kind::Overlap => {
                let (start, end) = unique_span(rng, model, text)?;
                let longer = text[start..].find('\n').map_or(text.len(), |i| start + i + 1).max(end);
                let first = (text[start..end].to_string(), new_text(rng));
                let second = (text[start..longer.max(end)].to_string(), new_text(rng));
                if rng.random_bool(0.5) { vec![first, second] } else { vec![second, first] }
            }
            Kind::Absent => {
                if rng.random_bool(0.5) {
                    vec![(format!("absent {} text", rng.random::<u32>()), new_text(rng))]
                } else {
                    // The first line is there; a later one is not.
                    let old = one(rng)?;
                    (old.trim_end().contains('\n')).then_some(())?;
                    vec![(format!("{}\nnot in the file", old.trim_end()), new_text(rng))]
                }
            }
        })
    }

    /// Outcomes seen, to check that every level and kind of error happened.
    #[derive(Default)]
    pub struct Tally(BTreeMap<String, usize>);

    impl Tally {
        fn add(&mut self, key: impl Into<String>) {
            *self.0.entry(key.into()).or_default() += 1;
        }

        fn note(&mut self, expected: &Result<Edited, String>) {
            match expected {
                Ok(e) if e.first_changed_line.is_none() => self.add("no changes"),
                Ok(e) if e.joins_breaks => self.add("joins breaks"),
                Ok(e) if e.loose.is_empty() => self.add("exact"),
                Ok(e) => {
                    for (_, level) in &e.loose {
                        self.add(level.name());
                    }
                }
                Err(msg) if msg.contains("when trailing whitespace") => self.add("several, trailing whitespace"),
                Err(msg) if msg.contains("when typographic") => self.add("several, typography"),
                Err(msg) if msg.contains(" times in ") => self.add("several"),
                Err(msg) if msg.contains("except for indentation") => self.add("missing, indentation"),
                Err(msg) if msg.contains("Its first line occurs") => self.add("missing, first line"),
                Err(msg) if msg.contains("was not found") => self.add("missing"),
                Err(msg) if msg.contains("overlap") => self.add("overlap"),
                Err(msg) => self.add(msg.clone()),
            }
        }

        fn check(&self, wanted: &[&str], what: &str) {
            for key in wanted {
                assert!(self.0.get(*key).is_some_and(|n| *n > 0), "{what}: no {key:?} in {:?}", self.0);
            }
        }
    }

    pub fn edit_args(name: &str, edits: &[(String, String)]) -> Value {
        let list: Vec<Value> = edits.iter().map(|(old, new)| json!({"old_text": old, "new_text": new})).collect();
        json!({"path": name, "edits": list})
    }

    /// Runs `edits` on `name` and checks the file and the result against the model. Returns
    /// what the model expected.
    pub async fn check_edit(s: &Session, name: &str, edits: &[(String, String)], what: &str) -> Result<Edited, String> {
        let before = s.bytes(name);
        let raw = std::str::from_utf8(&before).expect("UTF-8");
        let display = s.display(name);
        let expected = EditModel::new(raw).apply(&display, edits);
        let got = s.edit(edit_args(name, edits)).await;
        let after = s.bytes(name);
        let what = format!("{what}: edits {}", excerpt(&format!("{edits:?}")));
        match (got, &expected) {
            (Ok(out), Ok(e)) => {
                assert_same(&after, &e.bytes, &what);
                let content = text(&out);
                let Some(line) = e.first_changed_line else {
                    assert_eq!(content, e.text, "{what}");
                    assert_eq!(after, before, "{what}");
                    return expected;
                };
                let details = out.details.as_ref().expect("details");
                let diff = details["diff"].as_str().expect("a diff");
                if let Err(err) = check_diff(&display, &e.old_lf, &e.new_lf, diff) {
                    panic!("{what}: bad diff, {err}:\n{}", excerpt(diff));
                }
                if let Some(shown_after) = &e.shown_after {
                    assert_eq!(&e.new_lf, shown_after, "{what}: lines the edit did not touch changed");
                }
                let loose: Vec<Value> = e.loose.iter().map(|(n, l)| json!({"edit": n, "kind": l.name()})).collect();
                assert_eq!(
                    details,
                    &json!({
                        "path": display,
                        "replacements": e.replacements,
                        "first_changed_line": line,
                        "diff": diff,
                        "loose_matches": loose,
                    }),
                    "{what}"
                );
                let mut text = e.text.clone();
                if !e.loose.is_empty() && !diff.is_empty() {
                    text.push_str("\n\n");
                    text.push_str(&diff_for_model(diff));
                }
                assert_eq!(content, text, "{what}");
            }
            (Err(err), Err(message)) => {
                assert_eq!(&err.to_string(), message, "{what}");
                assert_same(&after, &before, &format!("{what}: the file after a failed edit"));
            }
            (Ok(out), Err(message)) => panic!("{what}: expected {message:?}, got {}", excerpt(text(&out))),
            (Err(err), Ok(_)) => panic!("{what}: unexpected error {err}"),
        }
        expected
    }

    /// Valid UTF-8 files of the corpus small enough for many edits.
    fn editable(prefixes: &[&str]) -> impl Iterator<Item = &'static Sample> {
        corpus()
            .iter()
            .filter(move |s| prefixes.iter().any(|p| s.name.starts_with(p)) && s.utf8().is_some() && s.bytes.len() < 300 * KIB)
    }

    /// Random edits of every kind on the files under `prefixes`, each file edited again and
    /// again, every result compared with the model.
    async fn random_edits(prefixes: &[&str], salt: u64) -> Tally {
        let seed = seed() ^ salt;
        let mut rng = rng(seed);
        let s = Session::new();
        let mut tally = Tally::default();
        for sample in editable(prefixes) {
            s.put(&sample.name, &sample.bytes);
            let rounds = (sample.bytes.len() / 1024).clamp(3, 16);
            for round in 0..rounds {
                let before = s.bytes(&sample.name);
                let raw = std::str::from_utf8(&before).unwrap();
                let model = EditModel::new(raw);
                let text = shown(raw);
                let kind = *KINDS.choose(&mut rng).unwrap();
                let Some(edits) = edits(&mut rng, &model, &text, kind) else { continue };
                let what = format!("seed {seed:#x}, {} round {round} ({kind:?})", sample.name);
                tally.add(format!("{kind:?}"));
                let expected = check_edit(&s, &sample.name, &edits, &what).await;
                tally.note(&expected);
            }
        }
        tally
    }

    #[tokio::test]
    async fn random_edits_on_text_in_every_encoding_and_line_break() {
        let tally = random_edits(&["enc/", "tiny/", "breaks/"], 0x21).await;
        tally.check(&["exact", "several", "missing", "trailing_whitespace", "overlap"], "enc");
    }

    #[tokio::test]
    async fn random_edits_on_source_code() {
        let tally = random_edits(&["code/", "repeat/"], 0x22).await;
        let wanted = [
            "exact",
            "no changes",
            "several",
            "missing",
            "missing, indentation",
            "missing, first line",
            "trailing_whitespace",
            "typography",
            "overlap",
            "Many",
        ];
        tally.check(&wanted, "code");
    }

    #[tokio::test]
    async fn random_edits_on_long_lines_and_boundaries() {
        let tally = random_edits(&["long/", "size/", "place/", "nul/"], 0x23).await;
        tally.check(&["exact", "several", "missing"], "long");
    }

    /// An edit followed by its reverse puts back every byte: the file's line breaks, byte
    /// order mark and anything else outside the replaced text never change.
    #[tokio::test]
    async fn an_edit_and_its_reverse_restore_every_byte() {
        let seed = seed() ^ 0x24;
        let mut rng = rng(seed);
        let s = Session::new();
        for sample in editable(&["enc/", "code/", "breaks/", "repeat/", "long/", "tiny/"]) {
            let original = sample.bytes.clone();
            s.put(&sample.name, &original);
            let raw = sample.utf8().unwrap();
            let model = EditModel::new(raw);
            let text = shown(raw);
            // Files whose line breaks are all of one kind.
            let mut kinds = support::model::line_breaks(raw.as_bytes());
            kinds.sort();
            kinds.dedup();
            let uniform = kinds.len() <= 1;
            for round in 0..4 {
                let Some((start, end)) = unique_span(&mut rng, &model, &text) else { continue };
                let old = text[start..end].to_string();
                let Located::Unique { start, end, .. } = model.locate(&old) else { unreachable!() };
                let replaced = &raw[start..end];
                // Unique in the edited file, so the reverse finds it.
                let marker = format!("⟦{round} {}⟧", rng.random::<u32>());
                let new = match rng.random_range(0..3) {
                    0 => marker,
                    1 => format!("{marker}\n{}", support::corpus::mixed_line(&mut rng, 4)),
                    _ => format!("{}\r\n{marker}\n", new_text(&mut rng).replace(['\r', '\n'], " ")),
                };
                let what = format!("seed {seed:#x}, {} round {round}", sample.name);
                check_edit(&s, &sample.name, &[(old.clone(), new.clone())], &format!("{what}, forward")).await.unwrap();
                // The reverse writes the old text with the line break of the line where it
                // starts: the replaced bytes come back when they had that one.
                let edited = String::from_utf8(s.bytes(&sample.name)).unwrap();
                let brk = EditModel::new(&edited).break_at(start);
                let same_breaks = support::model::line_breaks(replaced.as_bytes()).iter().all(|b| *b == brk);
                check_edit(&s, &sample.name, &[(lf(&new), old.clone())], &format!("{what}, reverse")).await.unwrap();
                let single_line = !old.contains('\n') && !new.contains(['\n', '\r']);
                if single_line || (uniform && same_breaks) {
                    assert_same(&s.bytes(&sample.name), &original, &format!("{what}: restored"));
                }
                s.put(&sample.name, &original);
            }
        }
    }

    /// Text that occurs several times is refused with the lines where it does, at the level
    /// that found it.
    #[tokio::test]
    async fn repeated_text_is_refused_with_its_lines() {
        let s = Session::new();
        let name = "repeat/blocks-crlf.rs";
        s.put(name, &sample(name).bytes);
        let what = format!("seed {:#x}, {name}", seed());
        let one = |old: &str| vec![(old.to_string(), "x".to_string())];
        for old in [
            "flush(buffer);",
            "    if (ready) {\n        flush(buffer);\n    }\n",
            "count += 1;\n",
            "}",
            "\n",
            // Several only when trailing whitespace is ignored, or typography folded.
            "flush(buffer);   \n",
            "flush(buffer);\u{A0}",
        ] {
            let err = check_edit(&s, name, &one(old), &what).await.unwrap_err();
            assert!(err.contains(" times in "), "{old:?}: {err}");
        }
        // Text found 101 times and more is "more than 100".
        let many = "x;\n".repeat(150);
        s.put("many.txt", many.as_bytes());
        let err = check_edit(&s, "many.txt", &one("x;"), &what).await.unwrap_err();
        assert!(err.contains("matches more than 100 times"), "{err}");
    }

    /// The ladder: exact first; trailing whitespace ignored only when nothing matches exactly;
    /// typography folded only when that finds nothing either; indentation never ignored.
    #[tokio::test]
    async fn loose_matches_only_when_exact_ones_fail() {
        let s = Session::new();
        let file = "fn a() {   \n    let s = “x” — 1;\t\n}\nfn a() {\n    let s = \"x\" - 1;\n}\n\tindented();\n";
        s.put("f.rs", file.as_bytes());
        let what = "ladder";
        for (old, level) in [
            // Exact, although the first lines also match loosely.
            ("fn a() {\n    let s = \"x\" - 1;", Some(Level::Exact)),
            ("fn a() {   \n    let s = “x” — 1;", Some(Level::Exact)),
            // Only with trailing whitespace ignored.
            ("fn a() {\n    let s = “x” — 1;\n}", Some(Level::TrailingWhitespace)),
            ("    let s = “x” — 1; \n}\nfn", Some(Level::TrailingWhitespace)),
            // Only with typography folded too.
            ("let s = \"x\" - 1;\t\n}\nfn a() {\n    let", Some(Level::Typography)),
            // Indentation differs: never.
            ("indented();", Some(Level::Exact)),
            ("  indented();", None),
            ("fn a() {\n  let s = \"x\" - 1;", None),
        ] {
            s.put("f.rs", file.as_bytes());
            let edits = vec![(old.to_string(), "changed".to_string())];
            let result = check_edit(&s, "f.rs", &edits, what).await;
            match (level, result) {
                (Some(level), Ok(e)) => {
                    let got = e.loose.first().map_or(Level::Exact, |l| l.1);
                    assert_eq!(got, level, "{old:?}");
                }
                (None, Err(err)) => assert!(err.contains("was not found"), "{old:?}: {err}"),
                (level, result) => panic!("{old:?}: expected {level:?}, got {result:?}"),
            }
        }
    }

    /// Argument shapes models send instead of the documented one do the same edit.
    #[tokio::test]
    async fn repaired_arguments_do_the_same_edit() {
        let s = Session::new();
        let name = "code/go-crlf.go";
        let bytes = &sample(name).bytes;
        let model = EditModel::new(std::str::from_utf8(bytes).unwrap());
        let text = shown(std::str::from_utf8(bytes).unwrap());
        let mut rng = rng(seed() ^ 0x25);
        let (start, end) = unique_span(&mut rng, &model, &text).unwrap();
        let (old, new) = (&text[start..end], "replaced\nlines\n");
        let item = json!({"old_text": old, "new_text": new});
        let expected = model.apply(&s.display(name), &[(old.to_string(), new.to_string())]).unwrap().bytes;
        for args in [
            json!({"path": name, "edits": [item]}),
            json!({"path": name, "edits": json!([item]).to_string()}),
            json!({"path": name, "edits": item}),
            json!({"path": name, "edits": item.to_string()}),
            json!({"file_path": name, "old_string": old, "new_string": new}),
            json!({"path": name, "oldText": old, "newText": new}),
            json!({"path": name, "edits": [{"old_string": old, "new_string": new}]}),
        ] {
            s.put(name, bytes);
            s.edit(args.clone()).await.unwrap_or_else(|err| panic!("{args}: {err}"));
            assert_same(&s.bytes(name), &expected, &format!("{args}"));
        }
    }

    /// Files edit cannot change are refused before anything is written.
    #[tokio::test]
    async fn refuses_what_it_cannot_edit() {
        let s = Session::new();
        let one = json!([{"old_text": "a", "new_text": "b"}]);
        // UTF-16 after a byte order mark is edited in its encoding; other non-UTF-8 is refused.
        let refused = |s: &&support::corpus::Sample| s.utf8().is_none() && support::model::utf16(&s.bytes).is_none();
        for sample in corpus().iter().filter(|s| s.name.starts_with("enc/")).filter(refused) {
            s.put(&sample.name, &sample.bytes);
            let err = support::error(s.edit(json!({"path": sample.name, "edits": one})).await);
            assert_eq!(err, format!("{} is not valid UTF-8 text.", s.display(&sample.name)));
            assert_eq!(s.bytes(&sample.name), sample.bytes);
        }
        std::fs::create_dir(s.path("dir")).unwrap();
        let cases = [
            ("dir", format!("{} is a directory.", s.display("dir"))),
            ("missing.txt", format!("File not found: {}. Use write to create a new file.", s.display("missing.txt"))),
        ];
        for (name, expected) in cases {
            assert_eq!(support::error(s.edit(json!({"path": name, "edits": one})).await), expected);
        }
        assert_eq!(
            support::error(s.edit(json!({"path": "missing.txt", "edits": []})).await),
            "invalid arguments: edits must contain at least one item"
        );
    }

    /// Many old_texts of several lines, whose rarest line is common: the line index has too
    /// many candidates and the views are scanned instead. The results are the same.
    #[tokio::test]
    async fn many_edits_whose_lines_are_all_common() {
        let s = Session::new();
        let text: String = (0..1500).map(|i| format!("fn f{i}() {{\r\n}}\r\n\r\n")).collect();
        s.put("f.rs", text.as_bytes());
        let edits: Vec<(String, String)> = (0..24)
            .map(|k| {
                let i = k * 60 + 7;
                (format!("fn f{i}() {{\n}}\n\nfn f{}() {{", i + 1), format!("fn g{i}() {{}}\nfn h{i}() {{"))
            })
            .collect();
        check_edit(&s, "f.rs", &edits, "common lines").await.unwrap();
        // Text found everywhere, and one old_text twice, among them.
        for extra in [("}\n\nfn f".to_string(), "x".to_string()), edits[3].clone()] {
            s.put("f.rs", text.as_bytes());
            let mut all = edits.clone();
            all.push(extra);
            assert!(check_edit(&s, "f.rs", &all, "common lines").await.is_err());
        }
    }

    /// The largest file edit takes is 16 MiB; one byte more is refused.
    #[tokio::test]
    async fn sixteen_mib_is_the_limit() {
        let s = Session::new();
        let mut big = "line of a large file\r\n".repeat(16 * MIB / 22 + 1).into_bytes();
        big.truncate(16 * MIB - 4);
        big.extend_from_slice(b"\r\nZZ");
        s.put("big.txt", &big);
        let one = json!([{"old_text": "\nZZ", "new_text": "\nlast\nline"}]);
        s.edit(json!({"path": "big.txt", "edits": one})).await.unwrap();
        let after = s.bytes("big.txt");
        assert_eq!(after.len(), 16 * MIB - 2 + "last\r\nline".len());
        assert!(after.ends_with(b"\r\nlast\r\nline"));
        assert_same(&after[..16 * MIB - 4], &big[..16 * MIB - 4], "the rest");
        big.push(b'!');
        s.put("big.txt", &big);
        let err = support::error(s.edit(json!({"path": "big.txt", "edits": one})).await);
        assert_eq!(err, format!("{} is too large to edit.", s.display("big.txt")));
    }
}

mod write {
    use super::*;
    use std::os::unix::fs::PermissionsExt;
    use std::os::unix::fs::symlink;
    use support::error;
    use support::model::write_result;
    use support::model::written;

    /// Every file read showed without loss, written back as read showed it: the bytes are
    /// those write promises (byte order mark and CRLF kept, anything else as given), the same
    /// file again when it was UTF-8 with one kind of line break, and read shows the same lines.
    #[tokio::test]
    async fn writing_back_what_read_showed() {
        let s = Session::new();
        let (mut written_back, mut identical) = (0, 0);
        for sample in corpus().iter().filter(|s| s.bytes.len() <= 2 * MIB) {
            let Some(model) = ReadModel::of(&sample.bytes) else { continue };
            let Some(content) = model.content(&sample.bytes) else { continue };
            let what = format!("seed {:#x}, {}", seed(), sample.name);
            s.put(&sample.name, &sample.bytes);
            s.read(json!({"path": sample.name, "limit": 1})).await.unwrap();
            let (expected, kept) = written(Some(&sample.bytes), &content);
            let out = s.write(json!({"path": sample.name, "content": content})).await.unwrap();
            assert_eq!(text(&out), write_result(&s.display(&sample.name), false, expected.len(), &kept), "{what}");
            let after = s.bytes(&sample.name);
            assert_same(&after, &expected, &what);
            let mut kinds = support::model::line_breaks(&sample.bytes);
            kinds.sort();
            kinds.dedup();
            // A U+FEFF that starts the content counts as its byte order mark, and read does not
            // show it: a file that starts with one after its mark loses it.
            if content.starts_with('\u{FEFF}') {
                continue;
            }
            if sample.utf8().is_some() && matches!(kinds.as_slice(), [] | ["\n"] | ["\r\n"]) {
                assert_same(&after, &sample.bytes, &format!("{what}: written back unchanged"));
                identical += 1;
            }
            let shown = |m: &ReadModel| m.lines.iter().map(|l| l.text.clone()).collect::<Vec<_>>();
            assert_eq!(shown(&ReadModel::of(&after).unwrap()), shown(&model), "{what}: lines read back");
            written_back += 1;
        }
        assert!(written_back > 80 && identical > 40, "{written_back} written back, {identical} unchanged");
    }

    /// New content over every kind of file: a UTF-16 encoding, the byte order mark of a UTF-8
    /// file and CRLF are kept (CRLF unless the content has a CR of its own); other encodings are
    /// replaced as given.
    #[tokio::test]
    async fn new_content_over_every_kind_of_file() {
        let seed = seed() ^ 0x31;
        let mut rng = support::corpus::rng(seed);
        let s = Session::new();
        for sample in corpus().iter().filter(|s| s.name.starts_with("enc/") || s.name.starts_with("tiny/")) {
            let what = format!("seed {seed:#x}, {}", sample.name);
            for content in [
                support::corpus::mixed_text(&mut rng, 300, support::corpus::Breaks::Lf),
                support::corpus::mixed_text(&mut rng, 300, support::corpus::Breaks::Crlf),
                "no line break".to_string(),
                String::new(),
                "\u{FEFF}with a mark\n".to_string(),
            ] {
                s.put(&sample.name, &sample.bytes);
                s.mark_read(&sample.name);
                let (expected, kept) = written(Some(&sample.bytes), &content);
                let out = s.write(json!({"path": sample.name, "content": content})).await.unwrap();
                assert_eq!(text(&out), write_result(&s.display(&sample.name), false, expected.len(), &kept), "{what}");
                assert_same(&s.bytes(&sample.name), &expected, &what);
            }
        }
    }

    /// write replaces only a version of the file this session read or wrote; any change since,
    /// even one that keeps the size and the modification time, needs another read.
    #[tokio::test]
    async fn a_write_needs_a_read_of_the_version_on_disk() {
        let s = Session::new();
        let name = "code/c-lf.c";
        let path = s.put(name, &sample(name).bytes);
        let display = s.display(name);
        let not_read = format!("{display} already exists and has not been read in this session. Read it first, then write it.");
        let changed = format!("{display} {STALE}");
        let write = async |content: &str| s.write(json!({"path": name, "content": content})).await;

        assert_eq!(error(write("x").await), not_read);
        // Another session's read does not count.
        s.other().read(json!({"path": name})).await.unwrap();
        assert_eq!(error(write("x").await), not_read);
        // One window anywhere is a read.
        s.read(json!({"path": name, "offset": 40, "limit": 1})).await.unwrap();
        write("one\n").await.unwrap();
        // Its own write is the version it knows.
        write("two\n").await.unwrap();

        let change = |how: &str| match how {
            "rewritten" => std::fs::write(&path, "rewritten by someone else\n").unwrap(),
            "same size, time put back" => support::patch_in_place(&path, 0, b"T"),
            "renamed over, same size and time" => {
                let other = s.put("other", &std::fs::read(&path).unwrap());
                let mtime = std::fs::metadata(&path).unwrap().modified().unwrap();
                std::fs::File::options().write(true).open(&other).unwrap().set_modified(mtime).unwrap();
                std::fs::rename(&other, &path).unwrap();
            }
            "mode changed" => std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o640)).unwrap(),
            "touched" => {
                let later = std::fs::metadata(&path).unwrap().modified().unwrap() + std::time::Duration::from_secs(5);
                std::fs::File::options().write(true).open(&path).unwrap().set_modified(later).unwrap();
            }
            _ => unreachable!(),
        };
        for how in ["rewritten", "same size, time put back", "renamed over, same size and time", "mode changed", "touched"] {
            s.read(json!({"path": name})).await.unwrap();
            let before = std::fs::read(&path).unwrap();
            change(how);
            let now = std::fs::read(&path).unwrap();
            assert_eq!(error(write("mine\n").await), changed, "{how}");
            assert_eq!(std::fs::read(&path).unwrap(), now, "{how}: nothing written");
            assert!(how != "same size, time put back" || now.len() == before.len());
            // Read again, and the write goes through.
            s.read(json!({"path": name, "limit": 1})).await.unwrap();
            write("mine\n").await.unwrap();
        }
        // Removed after the read: written as a new file.
        s.read(json!({"path": name})).await.unwrap();
        std::fs::remove_file(&path).unwrap();
        let out = write("new\n").await.unwrap();
        assert_eq!(text(&out), write_result(&display, true, 4, &[]));
    }

    /// Through a link the file it points to is replaced and the link stays; the session knows
    /// the file under every name. A dangling link creates its target.
    #[tokio::test]
    async fn writes_go_through_symbolic_links() {
        let s = Session::new();
        let name = "code/bom-crlf.c";
        let target = s.put("docs/guide.c", &sample(name).bytes);
        symlink("docs/guide.c", s.path("relative")).unwrap();
        symlink(&target, s.path("absolute")).unwrap();
        symlink("relative", s.path("chain")).unwrap();
        std::fs::create_dir(s.path("sub")).unwrap();
        symlink("../docs/guide.c", s.path("sub/up")).unwrap();
        let mut version = sample(name).bytes.clone();
        for link in ["relative", "absolute", "chain", "sub/up"] {
            let points_to = std::fs::read_link(s.path(link)).unwrap();
            let s = s.other();
            assert!(error(s.write(json!({"path": link, "content": "x"})).await).contains("has not been read"), "{link}");
            s.read(json!({"path": link})).await.unwrap();
            let content = format!("int {}(void);\nint main(void);\n", link.replace('/', "_"));
            let (expected, kept) = written(Some(&version), &content);
            assert_eq!(kept, ["byte order mark", "CRLF line endings"], "{link}");
            s.write(json!({"path": link, "content": content})).await.unwrap();
            assert_eq!(std::fs::read(&target).unwrap(), expected, "{link}");
            assert_eq!(std::fs::read_link(s.path(link)).unwrap(), points_to, "{link} is still the same link");
            // Known under the target's name too, and the other way round.
            s.write(json!({"path": "docs/guide.c", "content": content})).await.unwrap();
            s.write(json!({"path": link, "content": content})).await.unwrap();
            version = std::fs::read(&target).unwrap();
        }
        assert!(support::leftovers(s.root()).is_empty());

        symlink("docs/new.md", s.path("dangling")).unwrap();
        let out = s.write(json!({"path": "dangling", "content": "made\n"})).await.unwrap();
        assert_eq!(text(&out), write_result(&s.display("dangling"), true, 5, &[]));
        assert_eq!(std::fs::read(s.path("docs/new.md")).unwrap(), b"made\n");
        assert!(std::fs::symlink_metadata(s.path("dangling")).unwrap().file_type().is_symlink());
    }

    /// Writes that cannot happen are refused, and leave the directory as it was, without
    /// temporary files.
    #[tokio::test]
    async fn failed_writes_change_nothing_and_leave_nothing_behind() {
        use std::os::unix::ffi::OsStrExt;
        let s = Session::new();
        let root = s.root();
        s.put("locked.txt", b"keep\r\n");
        std::fs::set_permissions(s.path("locked.txt"), std::fs::Permissions::from_mode(0o444)).unwrap();
        s.mark_read("locked.txt");
        std::fs::create_dir(s.path("dir")).unwrap();
        let fifo = std::ffi::CString::new(s.path("pipe").as_os_str().as_bytes()).unwrap();
        assert_eq!(unsafe { libc::mkfifo(fifo.as_ptr(), 0o644) }, 0);
        s.put("file.txt", b"a file\n");
        symlink("loop-b", s.path("loop-a")).unwrap();
        symlink("loop-a", s.path("loop-b")).unwrap();
        symlink("missing/", s.path("to-missing-dir")).unwrap();
        let d = |name: &str| s.display(name);
        let mut cases = vec![
            ("dir", format!("{} is a directory.", d("dir"))),
            ("pipe", format!("{} is not a regular file; it cannot be written.", d("pipe"))),
            ("file.txt/new.txt", format!("Cannot create {}: {} is not a directory.", d("file.txt/new.txt"), d("file.txt"))),
            ("loop-a", format!("Cannot access {}: ", d("loop-a"))),
            ("to-missing-dir", format!("Cannot write {}: ", d("to-missing-dir"))),
        ];
        if !support::is_root() {
            cases.push(("locked.txt", format!("{} is read-only.", d("locked.txt"))));
        }
        for (name, expected) in cases {
            let before = support::snapshot(root);
            let err = error(s.write(json!({"path": name, "content": "new\n"})).await);
            assert!(err.starts_with(&expected), "{name}: {err}");
            assert_eq!(support::snapshot(root), before, "{name}: nothing changed");
            assert!(support::leftovers(root).is_empty(), "{name}: {:?}", support::leftovers(root));
        }
    }

    /// A JSON value sent as content is written as formatted JSON; other names for path and
    /// content work too.
    #[tokio::test]
    async fn repaired_arguments_write_what_they_mean() {
        let s = Session::new();
        let value = json!({"name": "日本", "list": [1, 2.5, null], "nested": {"ok": true}});
        let pretty = format!("{}\n", serde_json::to_string_pretty(&value).unwrap());
        s.write(json!({"file_path": "new.json", "contents": value})).await.unwrap();
        assert_eq!(s.bytes("new.json"), pretty.as_bytes());
        // Over a CRLF file it gets CRLF line endings, as any content would.
        s.put("crlf.json", b"{\r\n  \"old\": 1\r\n}\r\n");
        s.mark_read("crlf.json");
        s.write(json!({"path": "crlf.json", "content": value})).await.unwrap();
        assert_eq!(s.bytes("crlf.json"), pretty.replace('\n', "\r\n").as_bytes());
        s.write(json!({"path": "list.json", "content": [1, "two"]})).await.unwrap();
        assert_eq!(s.bytes("list.json"), b"[\n  1,\n  \"two\"\n]\n");
    }

    /// Readers never see half a write: every read while another task replaces the file shows
    /// one whole version.
    #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
    async fn readers_never_see_a_torn_write() {
        let s = Session::new();
        let versions: Vec<Vec<u8>> = vec![
            b"small version\nof two lines\n".to_vec(),
            sample("size/crlf-65536.txt").bytes.clone(),
            sample("code/rs-lf.rs").bytes.clone(),
        ];
        let models: Vec<String> = versions
            .iter()
            .map(|v| ReadModel::of(v).unwrap().expected(&s.display("f.txt"), None, None).unwrap().0)
            .collect();
        s.put("f.txt", &versions[0]);
        let writer = {
            let (s, versions) = (s.clone(), versions.clone());
            tokio::spawn(async move {
                for i in 0..60 {
                    s.ctx.env.write_file(&s.path("f.txt"), versions[i % versions.len()].clone()).await.unwrap();
                }
            })
        };
        let mut seen = std::collections::BTreeSet::new();
        while !writer.is_finished() || seen.is_empty() {
            let got = s.read_text(json!({"path": "f.txt"})).await;
            let which = models.iter().position(|m| *m == got).unwrap_or_else(|| panic!("torn read: {}", excerpt(&got)));
            seen.insert(which);
        }
        writer.await.unwrap();
        assert!(support::leftovers(s.root()).is_empty());
    }
}

mod flows {
    use super::*;
    use support::error;
    use support::model::EditModel;
    use support::model::Located;

    /// read, edit, read: the second read shows the edit at the line the edit named, every line
    /// before it as it was, and the whole file as the model has it.
    #[tokio::test]
    async fn read_edit_read() {
        let seed = seed() ^ 0x41;
        let mut rng = support::corpus::rng(seed);
        let s = Session::new();
        for sample in corpus().iter().filter(|s| {
            (s.name.starts_with("code/") || s.name.starts_with("enc/utf8")) && s.utf8().is_some()
        }) {
            s.put(&sample.name, &sample.bytes);
            for round in 0..4 {
                let what = format!("seed {seed:#x}, {} round {round}", sample.name);
                let before = s.bytes(&sample.name);
                let raw = String::from_utf8(before.clone()).unwrap();
                let model = EditModel::new(&raw);
                let shown = edit::shown(&raw);
                let old_model = ReadModel::of(&before).unwrap();
                page_through(&s, &sample.name, &old_model, None, &what).await;
                // A line of the file, as a model would pick it from what read showed.
                let lines: Vec<&str> = shown.split_inclusive('\n').collect();
                let Some(old) = (0..20).map(|_| lines[rand::Rng::random_range(&mut rng, 0..lines.len())]).find(|l| {
                    matches!(model.locate(l), Located::Unique { .. })
                }) else {
                    continue;
                };
                let new = edit::new_text(&mut rng);
                let edits = [(old.to_string(), new)];
                let Ok(e) = edit::check_edit(&s, &sample.name, &edits, &what).await else { continue };
                let Some(line) = e.first_changed_line else { continue };
                let new_model = ReadModel::of(&e.bytes).unwrap();
                let same = |m: &ReadModel| m.lines[..line - 1].iter().map(|l| l.text.clone()).collect::<Vec<_>>();
                assert_eq!(same(&new_model), same(&old_model), "{what}: lines before line {line}");
                if (line as u64) <= new_model.total() {
                    check_read(&s, &sample.name, &new_model, Some(line as u64), Some(3), &what).await;
                }
                page_through(&s, &sample.name, &new_model, None, &what).await;
            }
        }
    }

    /// The session knows the versions its own edits and writes made: no read in between.
    #[tokio::test]
    async fn edits_and_writes_follow_each_other_without_reads() {
        let s = Session::new();
        let name = "code/sh-crlf.sh";
        s.put(name, &sample(name).bytes);
        let one = |old: &str, new: &str| json!({"path": name, "edits": [{"old_text": old, "new_text": new}]});
        // edit needs no read; write after it needs none either.
        let first_line = edit::shown(sample(name).utf8().unwrap()).lines().next().unwrap().to_string();
        s.edit(one(&first_line, "# Title\nintro")).await.unwrap();
        s.write(json!({"path": name, "content": "# Title\nbody\n"})).await.unwrap();
        assert_eq!(s.bytes(name), b"# Title\r\nbody\r\n");
        s.edit(one("body", "body\nmore")).await.unwrap();
        s.edit(one("more", "more\nand more")).await.unwrap();
        s.write(json!({"path": name, "content": "replaced\n"})).await.unwrap();
        assert_eq!(s.read_text(json!({"path": name})).await, "replaced\n");

        // Another session's edit is a change this one has not read.
        s.other().edit(one("replaced", "theirs")).await.unwrap();
        let err = error(s.write(json!({"path": name, "content": "mine\n"})).await);
        assert!(err.ends_with(STALE), "{err}");
        assert_eq!(s.read_text(json!({"path": name})).await, "theirs\n");
        s.write(json!({"path": name, "content": "mine\n"})).await.unwrap();
        assert_eq!(s.bytes(name), b"mine\r\n");
    }

    /// A change by another program between read and write is caught at every size of file, in
    /// the streaming and the indexed read alike.
    #[tokio::test]
    async fn outside_changes_between_read_and_write_are_caught() {
        let s = Session::new();
        for name in ["tiny/one-byte.txt", "size/mixed-65537.txt", "place/crlf-at-1m.txt"] {
            let path = s.put(name, &sample(name).bytes);
            let total = ReadModel::of(&sample(name).bytes).unwrap().total();
            s.read(json!({"path": name, "offset": total, "limit": 1})).await.unwrap();
            support::patch_in_place(&path, 0, b"Z");
            let err = error(s.write(json!({"path": name, "content": "mine\n"})).await);
            assert!(err.ends_with(STALE), "{name}: {err}");
            // The Z may have joined two lines: read from the start.
            s.read(json!({"path": name, "limit": 1})).await.unwrap();
            s.write(json!({"path": name, "content": "mine\n"})).await.unwrap();
        }
    }
}

/// What write says about a file that changed since it was read.
const STALE: &str =
    concat!("changed since it was last read, possibly by another agent or the user. ", "Read it again, then write it.");

/// Regressions: each was a bug this suite found, fixed since.
mod regressions {
    use super::*;
    use her_tools::Tool;
    use her_tools::Typed;
    use her_tools::builtin::EditTool;
    use her_tools::builtin::WriteTool;
    use std::os::unix::fs::PermissionsExt;
    use support::model::EditModel;
    use support::model::Located;

    /// An edit must change the lines it replaces and no others. Next to a lone CR, an LF that
    /// starts the new text, or that follows text deleted up to it, makes one CRLF with that CR:
    /// read then shows a line fewer than the edit made, and the diff in the details describes
    /// a file that is not on disk.
    #[tokio::test]
    async fn edit_never_joins_a_lone_cr_and_an_lf() {
        let s = Session::new();
        // The file, old_text, new_text, and the file as read must show it afterwards.
        for (file, old, new, shown) in [
            // The line between a lone CR and an LF emptied.
            ("a\rx\nb\n", "x", "", "a\n\nb\n"),
            // A line inserted at the start of a line that follows a lone CR.
            ("a\rb\nc\n", "b", "\nb", "a\n\nb\nc\n"),
            // New text that ends a line, in a CR line, right before an LF.
            ("a\rb\nc\n", "a\nb", "q\n", "q\n\nc\n"),
        ] {
            s.put("f.txt", file.as_bytes());
            let what = format!("{file:?}: {old:?} -> {new:?}");
            let edits = [(old.to_string(), new.to_string())];
            let out = s.edit(edit::edit_args("f.txt", &edits)).await.unwrap();
            assert_eq!(s.read_text(json!({"path": "f.txt"})).await, shown, "{what}");
            let diff = out.details.unwrap()["diff"].as_str().unwrap().to_string();
            let after = String::from_utf8(s.bytes("f.txt")).unwrap();
            let headers = support::model::check_diff(&s.display("f.txt"), &edit::shown(file), &edit::shown(&after), &diff)
                .unwrap_or_else(|err| panic!("{what}: {err}\n{diff}"));
            assert!(headers.is_empty(), "{what}: {headers:?}");
        }

        // Random edits on the files of the corpus with lone CRs and LFs: after each, read shows
        // what it showed before with the old_text replaced by the new_text, and the diff
        // describes the file.
        let seed = seed() ^ 0x51;
        let mut rng = support::corpus::rng(seed);
        let mut joins = 0;
        for sample in corpus().iter().filter(|s| {
            s.utf8().is_some_and(|t| t.contains('\r') && t.replace("\r\n", "").contains('\n')) && s.bytes.len() < 100 * KIB
        }) {
            s.put(&sample.name, &sample.bytes);
            for round in 0..20 {
                let raw = String::from_utf8(s.bytes(&sample.name)).unwrap();
                let model = EditModel::new(&raw);
                let shown = edit::shown(&raw);
                let line = shown.split_inclusive('\n').nth(rand::Rng::random_range(&mut rng, 0..shown.lines().count().max(1)));
                let Some(old) = line.map(|l| l.trim_end_matches('\n')).filter(|l| !l.is_empty()) else { continue };
                if !matches!(model.locate(old), Located::Unique { .. }) {
                    continue;
                }
                let new = ["", "\nnew line", "new line\n", "x"][round % 4];
                let edits = [(old.to_string(), new.to_string())];
                let expected = model.apply(&s.display(&sample.name), &edits).unwrap();
                joins += usize::from(expected.joins_breaks);
                let what = format!("seed {seed:#x}, {} round {round}: {old:?} -> {new:?}", sample.name);
                let out = s.edit(edit::edit_args(&sample.name, &edits)).await.unwrap();
                let after = edit::shown(&String::from_utf8(s.bytes(&sample.name)).unwrap());
                assert_same(after.as_bytes(), expected.shown_after.as_deref().unwrap_or_default().as_bytes(), &what);
                // "No changes" has no details.
                let Some(details) = out.details else { continue };
                let diff = details["diff"].as_str().unwrap().to_string();
                support::model::check_diff(&s.display(&sample.name), &shown, &after, &diff)
                    .unwrap_or_else(|err| panic!("{what}: {err}\n{diff}"));
            }
        }
        assert!(joins > 0, "no edit joined a CR and an LF");
    }

    /// edit's check passes for a file it may not write, so the user approves an edit that
    /// then fails; write's check refuses the same file.
    #[tokio::test]
    async fn edit_check_refuses_a_read_only_file() {
        if support::is_root() {
            return;
        }
        let s = Session::new();
        s.put("locked.txt", b"a\n");
        std::fs::set_permissions(s.path("locked.txt"), std::fs::Permissions::from_mode(0o444)).unwrap();
        let args = json!({"path": "locked.txt", "edits": [{"old_text": "a", "new_text": "b"}]});
        let run = Typed::new(EditTool).execute(args.clone(), s.ctx.clone()).await.unwrap_err();
        let check = Typed::new(EditTool).check(&args, &s.ctx).await;
        assert_eq!(check.map_err(|e| e.to_string()), Err(run.to_string()), "check and run");
    }

    /// write's check passes for a link whose target is in a directory that does not exist,
    /// although the run never creates that directory.
    #[tokio::test]
    async fn write_check_refuses_a_link_into_a_missing_directory() {
        let s = Session::new();
        std::os::unix::fs::symlink("missing/new.md", s.path("link")).unwrap();
        let args = json!({"path": "link", "content": "x"});
        let run = Typed::new(WriteTool).execute(args.clone(), s.ctx.clone()).await.unwrap_err();
        let check = Typed::new(WriteTool).check(&args, &s.ctx).await;
        assert!(check.is_err(), "check passed; the run said: {run}");
    }

    /// The checks of write and edit pass for files in a directory this process may not
    /// write, where neither can put its temporary file.
    #[tokio::test]
    async fn checks_refuse_a_file_in_a_read_only_directory() {
        if support::is_root() {
            return;
        }
        let s = Session::new();
        std::fs::create_dir(s.path("ro")).unwrap();
        s.put("ro/f.txt", b"a\n");
        s.mark_read("ro/f.txt");
        std::fs::set_permissions(s.path("ro"), std::fs::Permissions::from_mode(0o555)).unwrap();
        let calls: [(&dyn Tool, Value); 3] = [
            (&Typed::new(WriteTool), json!({"path": "ro/new.txt", "content": "x"})),
            (&Typed::new(WriteTool), json!({"path": "ro/f.txt", "content": "x"})),
            (&Typed::new(EditTool), json!({"path": "ro/f.txt", "edits": [{"old_text": "a", "new_text": "b"}]})),
        ];
        let mut approved = Vec::new();
        for (tool, args) in calls {
            let run = tool.execute(args.clone(), s.ctx.clone()).await;
            if let (Ok(()), Err(run)) = (tool.check(&args, &s.ctx).await, run) {
                approved.push(format!("{args}: {run}"));
            }
        }
        std::fs::set_permissions(s.path("ro"), std::fs::Permissions::from_mode(0o755)).unwrap();
        assert!(approved.is_empty(), "check passed, the run failed:\n{}", approved.join("\n"));
    }

    /// The hunk headers of edit's diff count the lines of their hunks. The diff of the similar
    /// crate (2.7) can give a delete a position in the new text past the next line: here the
    /// header says the new lines start at line 2, where they start at line 1.
    #[tokio::test]
    async fn edit_diff_hunk_headers_count_their_lines() {
        let s = Session::new();
        let file = "A\n\nB\nC\nD\nrest\n";
        s.put("f.txt", file.as_bytes());
        let out = s.edit(edit::edit_args("f.txt", &[("A\n\nB\nC\nD".into(), "\nF\n".into())])).await.unwrap();
        let diff = out.details.unwrap()["diff"].as_str().unwrap().to_string();
        let after = String::from_utf8(s.bytes("f.txt")).unwrap();
        assert_eq!(after, "\nF\n\nrest\n");
        let headers = support::model::check_diff(&s.display("f.txt"), file, &after, &diff).unwrap();
        assert!(headers.is_empty(), "{headers:?} in\n{diff}");
    }

    /// A UTF-16 file is decoded up to 16 MiB. When the last line in them goes on past them, the
    /// line is counted but cannot be read: the last page's note points at it, and the error
    /// there says that line can be read. Reading the lines the error says can be read must
    /// work.
    #[tokio::test]
    async fn utf16_past_16_mib_names_the_lines_it_can_read() {
        let s = Session::new();
        let text: String = (1..=470_000u64).map(|i| format!("{}\r\n", support::corpus::numbered_line(i))).collect();
        let bytes: Vec<u8> = [0xFF, 0xFE].into_iter().chain(text.encode_utf16().flat_map(u16::to_le_bytes)).collect();
        assert!(bytes.len() > 16 * MIB);
        s.put("u16.txt", &bytes);
        let decoded = support::model::decode_utf16(&bytes[2..16 * MIB], false);
        let complete = support::model::line_breaks(decoded.as_bytes()).len() as u64;
        let last_page = s.read_text(json!({"path": "u16.txt", "offset": complete, "limit": 5})).await;
        let next = support::next_offset(&last_page).expect("a note on the last page");
        let err = support::error(s.read(json!({"path": "u16.txt", "offset": next})).await);
        let readable: u64 = err
            .split("only its first ")
            .nth(1)
            .and_then(|rest| rest.split(' ').next())
            .and_then(|n| n.parse().ok())
            .unwrap_or_else(|| panic!("{err}"));
        let result = s.read(json!({"path": "u16.txt", "offset": readable, "limit": 1})).await;
        assert!(result.is_ok(), "the note said offset={next}, which says: {err}; reading line {readable} says: {}", support::error(result));
    }
}

/// Files of hundreds of megabytes and work of many seconds in a debug build. Run them with
/// `cargo test -p her-tools --release -- --ignored`.
mod large {
    use super::*;
    use std::time::Instant;
    use support::corpus::numbered;
    use support::model::EditModel;

    /// Pages of a file of over a hundred megabytes from its start, middle and end; again from
    /// its line index, and from a new environment; in LF and in CRLF.
    #[ignore = "large: run with --release -- --ignored"]
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_file_of_over_a_hundred_megabytes() {
        for brk in ["\n", "\r\n"] {
            let lines = 6_000_000;
            let bytes = numbered(lines, brk);
            assert!(bytes.len() > 100 * MIB);
            let s = Session::new();
            s.put("big.log", &bytes);
            let model = ReadModel::of(&bytes).unwrap();
            let what = format!("{} bytes, breaks {brk:?}", bytes.len());
            let offsets = [None, Some(2), Some(lines / 3), Some(lines / 2 + 7), Some(lines - 1999), Some(lines), Some(lines + 1)];
            let mut first = Vec::new();
            for offset in offsets {
                first.push(check_read(&s, "big.log", &model, offset, None, &what).await.shown);
            }
            let fresh = s.fresh_env();
            for session in [&s, &fresh, &s] {
                for (offset, before) in offsets.iter().zip(&first) {
                    let started = Instant::now();
                    let again = check_read(session, "big.log", &model, *offset, Some(2000), &what).await.shown;
                    assert_eq!(&again, before, "{what}: offset {offset:?}");
                    // From the line index a page costs about its window, not the file.
                    assert!(std::ptr::eq(session, &fresh) || started.elapsed().as_secs() < 2, "{what}: {:?}", started.elapsed());
                }
            }
        }
    }

    /// Past 256 MiB after a window the lines are no longer counted: the total is a lower
    /// bound, until a read that reaches the end indexes the file and every total is exact.
    #[ignore = "large: run with --release -- --ignored"]
    #[tokio::test]
    async fn past_256_mib_totals_are_lower_bounds_until_a_read_reaches_the_end() {
        // Lines of 64 bytes: line i is i, zero-padded.
        let lines: u64 = 300 * MIB as u64 / 64;
        let mut bytes = Vec::with_capacity(300 * MIB);
        for i in 1..=lines {
            bytes.extend_from_slice(format!("{i:063}\n").as_bytes());
        }
        let s = Session::new();
        s.put("huge.txt", &bytes);
        let page = |from: u64, to: u64| (from..=to).map(|i| format!("{i:063}\n")).collect::<String>();
        let first = s.read_text(json!({"path": "huge.txt", "limit": 3})).await;
        let (shown, note) = first.split_at(first.find("\n\n[").unwrap() + 1);
        assert_eq!(shown, page(1, 3));
        let at_least: u64 = note
            .strip_prefix("\n[Showing lines 1-3 of at least ")
            .and_then(|rest| rest.strip_suffix(". Use offset=4 to continue.]"))
            .and_then(|n| n.parse().ok())
            .unwrap_or_else(|| panic!("{note}"));
        assert!(at_least >= (256 * MIB / 64) as u64 && at_least < lines, "{at_least}");
        // A read past the count limit still finds its lines, and reaches the end.
        let deep = s.read_text(json!({"path": "huge.txt", "offset": lines - 1})).await;
        assert_eq!(deep, page(lines - 1, lines));
        // That indexed the file: totals are exact now, from anywhere.
        let first = s.read_text(json!({"path": "huge.txt", "limit": 3})).await;
        assert_eq!(first, format!("{}\n[Showing lines 1-3 of {lines}. Use offset=4 to continue.]", page(1, 3)));
        let err = support::error(s.read(json!({"path": "huge.txt", "offset": lines + 1})).await);
        assert_eq!(err, format!("offset {} is beyond the end of {} ({lines} lines).", lines + 1, s.display("huge.txt")));
    }

    /// A line of sixty megabytes is cut and measured exactly; the lines after it read as
    /// usual, and so do its neighbours in CRLF and without a final line break.
    #[ignore = "large: run with --release -- --ignored"]
    #[tokio::test]
    async fn a_line_of_sixty_megabytes() {
        let unit = "日本語 and ascii, “quoted” — 😀\t";
        let line = unit.repeat(60 * MIB / unit.len());
        for (name, file) in [
            ("lf", format!("first\n{line}\nlast\n")),
            ("crlf", format!("first\r\n{line}\r\nlast")),
            ("eof", format!("first\n{line}")),
        ] {
            let s = Session::new();
            s.put(name, file.as_bytes());
            let model = ReadModel::of(file.as_bytes()).unwrap();
            assert_eq!(model.lines[1].chars, line.chars().count() as u64);
            page_through(&s, name, &model, None, name).await;
            page_through(&s, name, &model, Some(1), name).await;
            check_read(&s, name, &model, Some(2), Some(1), name).await;
        }
    }

    /// UTF-16 is decoded in memory up to 16 MiB: the pages before that are those of the
    /// model, with lower bounds for totals.
    #[ignore = "large: run with --release -- --ignored"]
    #[tokio::test]
    async fn utf16_past_16_mib_reads_its_first_16_mib() {
        let s = Session::new();
        let text: String = (1..=500_000u64).map(|i| format!("{}\r\n", support::corpus::numbered_line(i))).collect();
        let bytes: Vec<u8> = [0xFE, 0xFF].into_iter().chain(text.encode_utf16().flat_map(u16::to_be_bytes)).collect();
        s.put("u16.txt", &bytes);
        let decoded = support::model::decode_utf16(&bytes[2..16 * MIB], true);
        let lines: Vec<&str> = support::model::split_lines(decoded.as_bytes())
            .into_iter()
            .map(|l| std::str::from_utf8(l).unwrap())
            .collect();
        // The last line goes on past 16 MiB: counted, not shown.
        let complete = support::model::line_breaks(decoded.as_bytes()).len();
        assert_eq!(lines.len(), complete + 1);
        for offset in [1, 2, complete / 2, complete - 3] {
            let got = s.read_text(json!({"path": "u16.txt", "offset": offset, "limit": 3})).await;
            let shown: String = lines[offset - 1..offset + 2].iter().map(|l| format!("{l}\n")).collect();
            let note = format!(
                "\n[Showing lines {offset}-{} of at least {}. Use offset={} to continue.]",
                offset + 2,
                complete + 1,
                offset + 3
            );
            assert_eq!(got, format!("{shown}{note}"));
        }
    }

    /// 150 MB written through the tool, kept exactly, and read back at its end; then written
    /// over as CRLF.
    #[ignore = "large: run with --release -- --ignored"]
    #[tokio::test]
    async fn writes_and_reads_back_a_hundred_and_fifty_megabytes() {
        let s = Session::new();
        let lines = 7_500_000;
        let content = String::from_utf8(numbered(lines, "\n")).unwrap();
        assert!(content.len() > 150 * 1000 * 1000);
        let out = s.write(json!({"path": "out/big.txt", "content": content})).await.unwrap();
        assert_eq!(text(&out), support::model::write_result(&s.display("out/big.txt"), true, content.len(), &[]));
        assert!(s.bytes("out/big.txt") == content.as_bytes());
        let model = ReadModel::of(content.as_bytes()).unwrap();
        check_read(&s, "out/big.txt", &model, Some(lines - 10), None, "150 MB").await;
        // Over a CRLF file the same content gets CRLF line endings.
        let crlf = numbered(lines, "\r\n");
        s.put("out/big.txt", &crlf);
        s.read(json!({"path": "out/big.txt", "offset": lines})).await.unwrap();
        let out = s.write(json!({"path": "out/big.txt", "content": content})).await.unwrap();
        assert!(text(&out).ends_with("(overwritten, CRLF line endings kept)."), "{}", text(&out));
        assert!(s.bytes("out/big.txt") == crlf);
        assert!(support::leftovers(s.root()).is_empty());
    }

    /// Thousands of random edits in batches on a file of megabytes of mixed text and code,
    /// every batch compared with the model.
    #[ignore = "large: run with --release -- --ignored"]
    #[tokio::test]
    async fn thousands_of_edits_on_a_large_file() {
        let seed = seed() ^ 0x61;
        let mut rng = support::corpus::rng(seed);
        let s = Session::new();
        let mut text = String::new();
        for (i, lang) in support::corpus::Lang::ALL.into_iter().enumerate() {
            let breaks = [support::corpus::Breaks::Lf, support::corpus::Breaks::Crlf][i % 2];
            text.push_str(&support::corpus::source(&mut rng, lang, MIB, breaks));
        }
        text.push_str(&support::corpus::mixed_text(&mut rng, 4 * MIB, support::corpus::Breaks::Mixed));
        s.put("big.txt", text.as_bytes());
        let mut applied = 0;
        for round in 0..12 {
            let raw = String::from_utf8(s.bytes("big.txt")).unwrap();
            let model = EditModel::new(&raw);
            let shown = edit::shown(&raw);
            let lines: Vec<(usize, &str)> = shown.match_indices('\n').map(|(i, _)| (i, "")).collect();
            // Edits of whole lines, spread over the file, none overlapping.
            let mut edits: Vec<(String, String)> = Vec::new();
            let mut taken: Vec<(usize, usize)> = Vec::new();
            for _ in 0..400 {
                let k = rand::Rng::random_range(&mut rng, 1..lines.len() - 4);
                let (start, end) = (lines[k - 1].0 + 1, lines[k + 2].0);
                let old = &shown[start..end];
                if taken.iter().any(|&(s, e)| start < e && s < end) || old.trim().is_empty() {
                    continue;
                }
                if matches!(model.locate(old), support::model::Located::Unique { .. }) {
                    taken.push((start, end));
                    edits.push((old.to_string(), edit::new_text(&mut rng)));
                }
            }
            let what = format!("seed {seed:#x}, round {round}");
            let e = edit::check_edit(&s, "big.txt", &edits, &what).await.unwrap();
            applied += e.replacements;
        }
        assert!(applied > 3000, "{applied}");
    }
}
