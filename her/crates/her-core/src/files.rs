//! A session's files as the desktop's file panel sees them (docs/desktop/file-panel-design.md
//! §9): folders a level at a time, files read for showing, and text written back only while
//! the file is still the version it was edited from, under the lock the session's own edit
//! and write tools take, so a save and a tool's write never interleave.

use std::cmp::Ordering;
use std::io;
use std::io::Read;
use std::path::Path;
use std::sync::Arc;

use base64::Engine as _;
use her_protocol::files::FileEntry;
use her_protocol::files::FileImage;
use her_protocol::files::FileKind;
use her_protocol::files::FileWriteStatus;
use her_protocol::files::FilesListResult;
use her_protocol::files::FilesReadResult;
use her_protocol::files::FilesWriteResult;
use her_tools::Environment;
use her_tools::PathLocks;
use her_tools::image::ImageFormat;

use crate::paths::HerHome;

/// Text over this is only described (D-262).
pub const MAX_TEXT: u64 = 5 * 1024 * 1024;
/// Images over this are only described.
pub const MAX_IMAGE: u64 = 10 * 1024 * 1024;
/// Entries of one folder given at most.
pub const MAX_ENTRIES: usize = 5000;

const BOM: &[u8] = &[0xEF, 0xBB, 0xBF];

/// A session's files: its environment resolves paths as its tools do, and its locks are the
/// ones its edit and write tools take.
#[derive(Clone)]
pub struct FileScope {
    pub env: Arc<dyn Environment>,
    pub locks: Arc<PathLocks>,
}

/// SHA-256 of `bytes`, in hex.
pub fn hash(bytes: &[u8]) -> String {
    ring::digest::digest(&ring::digest::SHA256, bytes).as_ref().iter().map(|b| format!("{b:02x}")).collect()
}

/// Names compared as people read them: case aside, runs of digits by their value.
pub fn natural_cmp(a: &str, b: &str) -> Ordering {
    let (mut x, mut y) = (a.chars().peekable(), b.chars().peekable());
    loop {
        match (x.peek().copied(), y.peek().copied()) {
            (None, None) => return a.cmp(b),
            (None, Some(_)) => return Ordering::Less,
            (Some(_), None) => return Ordering::Greater,
            (Some(c), Some(d)) if c.is_ascii_digit() && d.is_ascii_digit() => {
                let take = |it: &mut std::iter::Peekable<std::str::Chars<'_>>| {
                    let mut digits = String::new();
                    while let Some(c) = it.peek().copied().filter(char::is_ascii_digit) {
                        digits.push(c);
                        it.next();
                    }
                    digits
                };
                let (m, n) = (take(&mut x), take(&mut y));
                let (m, n) = (m.trim_start_matches('0'), n.trim_start_matches('0'));
                let order = m.len().cmp(&n.len()).then_with(|| m.cmp(n));
                if order != Ordering::Equal {
                    return order;
                }
            }
            (Some(c), Some(d)) => {
                let order = c.to_lowercase().cmp(d.to_lowercase());
                if order != Ordering::Equal {
                    return order;
                }
                x.next();
                y.next();
            }
        }
    }
}

/// A folder's entries: folders first, then files, each by [`natural_cmp`]; `.git` and
/// `.DS_Store` left out, at most [`MAX_ENTRIES`].
pub fn list(dir: &Path) -> io::Result<FilesListResult> {
    let mut entries = Vec::new();
    for entry in std::fs::read_dir(dir)? {
        let entry = entry?;
        let name = entry.file_name().to_string_lossy().into_owned();
        if name == ".git" || name == ".DS_Store" {
            continue;
        }
        let link = entry.file_type().is_ok_and(|t| t.is_symlink());
        // A link is what it points to; a broken one is a file of no size.
        let meta = std::fs::metadata(entry.path()).ok();
        let dir = meta.as_ref().is_some_and(std::fs::Metadata::is_dir);
        let size = meta.filter(|m| m.is_file()).map_or(0, |m| m.len());
        entries.push(FileEntry { name, dir, link, size });
    }
    entries.sort_by(|a, b| b.dir.cmp(&a.dir).then_with(|| natural_cmp(&a.name, &b.name)));
    let truncated = entries.len() > MAX_ENTRIES;
    entries.truncate(MAX_ENTRIES);
    Ok(FilesListResult { path: dir.to_path_buf(), entries, truncated })
}

/// Text from a file's bytes and how it was decoded, or None when it is not text: UTF-8 (a
/// byte-order mark dropped), UTF-16 by its mark, else GB18030 when the bytes are that.
pub fn decode(bytes: &[u8]) -> Option<(String, &'static str)> {
    if let Some(body) = bytes.strip_prefix(BOM) {
        return String::from_utf8(body.to_vec()).ok().map(|s| (s, "utf-8-bom"));
    }
    for (mark, encoding, name) in [([0xFF, 0xFE], encoding_rs::UTF_16LE, "utf-16le"), ([0xFE, 0xFF], encoding_rs::UTF_16BE, "utf-16be")] {
        if let Some(body) = bytes.strip_prefix(&mark) {
            let (text, malformed) = encoding.decode_without_bom_handling(body);
            return (!malformed).then(|| (text.into_owned(), name));
        }
    }
    if bytes[..bytes.len().min(8192)].contains(&0) {
        return None;
    }
    if let Ok(text) = std::str::from_utf8(bytes) {
        return Some((text.to_owned(), "utf-8"));
    }
    encoding_rs::GB18030.decode_without_bom_handling_and_without_replacement(bytes).map(|text| (text.into_owned(), "gb18030"))
}

/// her's own data, which the file panel shows but never writes (D-263): session logs and their
/// data, logs, and the service's run folder.
pub fn refused(home: &HerHome, path: &Path) -> bool {
    let real = |p: &Path| std::fs::canonicalize(p).unwrap_or_else(|_| p.to_path_buf());
    // The file may not exist yet: its folder decides.
    let target = match std::fs::canonicalize(path) {
        Ok(p) => p,
        Err(_) => path.parent().map(|dir| real(dir).join(path.file_name().unwrap_or_default())).unwrap_or_else(|| path.to_path_buf()),
    };
    [home.sessions_dir(), home.logs_dir(), home.root().join("run")].iter().any(|dir| target.starts_with(real(dir)))
}

/// A file for showing: its text, an image's bytes, or only what it is.
pub fn read(home: &HerHome, path: &Path) -> io::Result<FilesReadResult> {
    let meta = std::fs::metadata(path)?;
    if meta.is_dir() {
        return Err(io::Error::new(io::ErrorKind::IsADirectory, format!("{} is a folder", path.display())));
    }
    let size = meta.len();
    let mut result = FilesReadResult { path: path.to_path_buf(), size, kind: FileKind::TooLarge, hash: None, text: None, encoding: None, writable: false, image: None };
    let mut head = Vec::with_capacity(16);
    std::fs::File::open(path)?.take(16).read_to_end(&mut head)?;
    if let Some(format) = ImageFormat::detect(&head) {
        if size <= MAX_IMAGE {
            let bytes = std::fs::read(path)?;
            result.kind = FileKind::Image;
            result.hash = Some(hash(&bytes));
            result.image = Some(FileImage { mime_type: format.mime_type().to_owned(), data: base64::engine::general_purpose::STANDARD.encode(&bytes) });
        }
        return Ok(result);
    }
    if size > MAX_TEXT {
        return Ok(result);
    }
    let bytes = std::fs::read(path)?;
    result.hash = Some(hash(&bytes));
    match decode(&bytes) {
        Some((text, encoding)) => {
            result.kind = FileKind::Text;
            result.writable = encoding.starts_with("utf-8") && !refused(home, path);
            result.text = Some(text);
            result.encoding = Some(encoding.to_owned());
        }
        None => result.kind = FileKind::Binary,
    }
    Ok(result)
}

/// Writes `text` to `path` (§9.3) while the file is still the version `base` names (its
/// [`hash`]); `base` None writes whatever is there. Under the session's lock for the path,
/// as its tools write; atomically, as they do. A UTF-8 byte-order mark the file had stays.
pub async fn write(scope: &FileScope, home: &HerHome, path: &Path, text: &str, base: Option<&str>) -> io::Result<FilesWriteResult> {
    let refuse = |message: &str| FilesWriteResult { status: FileWriteStatus::Refused, hash: None, message: Some(message.to_owned()) };
    if refused(home, path) {
        return Ok(refuse("her's own data is not written from here"));
    }
    let _guard = scope.locks.lock(path).await;
    let current = match tokio::fs::read(path).await {
        Ok(bytes) => Some(bytes),
        Err(err) if err.kind() == io::ErrorKind::NotFound => None,
        Err(err) => return Err(err),
    };
    if let Some(bytes) = &current
        && std::str::from_utf8(bytes.strip_prefix(BOM).unwrap_or(bytes)).is_err()
    {
        return Ok(refuse("the file is not UTF-8 text"));
    }
    if let Some(base) = base {
        let now = current.as_deref().map(hash);
        if now.as_deref() != Some(base) {
            return Ok(FilesWriteResult { status: FileWriteStatus::Conflict, hash: now, message: None });
        }
    }
    let bom = current.as_deref().is_some_and(|b| b.starts_with(BOM));
    let mut data = Vec::with_capacity(text.len() + 3);
    if bom {
        data.extend_from_slice(BOM);
    }
    data.extend_from_slice(text.as_bytes());
    let saved = hash(&data);
    scope.env.write_file(path, data).await?;
    Ok(FilesWriteResult { status: FileWriteStatus::Saved, hash: Some(saved), message: None })
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_tools::LocalEnvironment;
    use std::time::Duration;

    fn scope(dir: &Path) -> FileScope {
        FileScope { env: Arc::new(LocalEnvironment::new(dir.to_path_buf())), locks: Arc::new(PathLocks::new()) }
    }

    #[test]
    fn folders_list_folders_first_by_name_without_git() {
        let dir = tempfile::tempdir().unwrap();
        for f in ["b.txt", "a10.txt", "a2.txt", "A1.txt", ".DS_Store"] {
            std::fs::write(dir.path().join(f), "x").unwrap();
        }
        for d in ["src", ".git", "Docs"] {
            std::fs::create_dir(dir.path().join(d)).unwrap();
        }
        #[cfg(unix)]
        std::os::unix::fs::symlink(dir.path().join("src"), dir.path().join("linked")).unwrap();
        let listed = list(dir.path()).unwrap();
        let names: Vec<(&str, bool, bool)> = listed.entries.iter().map(|e| (e.name.as_str(), e.dir, e.link)).collect();
        assert_eq!(names, [("Docs", true, false), ("linked", true, true), ("src", true, false), ("A1.txt", false, false), ("a2.txt", false, false), ("a10.txt", false, false), ("b.txt", false, false)]);
        assert_eq!(listed.entries[3].size, 1);
        assert!(!listed.truncated);
    }

    #[test]
    fn text_is_decoded_by_its_mark_then_as_utf8_then_gb18030() {
        assert_eq!(decode(b"plain\r\n"), Some(("plain\r\n".into(), "utf-8")));
        assert_eq!(decode(b"\xEF\xBB\xBFwith bom"), Some(("with bom".into(), "utf-8-bom")));
        assert_eq!(decode(b"\xFF\xFEh\x00i\x00"), Some(("hi".into(), "utf-16le")));
        assert_eq!(decode(b"\xFE\xFF\x00h\x00i"), Some(("hi".into(), "utf-16be")));
        // "报告" in GBK.
        assert_eq!(decode(b"\xB1\xA8\xB8\xE6,1\n"), Some(("报告,1\n".into(), "gb18030")));
        assert_eq!(decode(b"PK\x03\x04\x00\x00binary"), None);
        assert_eq!(decode(b""), Some((String::new(), "utf-8")));
    }

    #[test]
    fn reading_tells_text_images_binary_and_size() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        std::fs::write(dir.path().join("a.md"), "# hi\n").unwrap();
        std::fs::write(dir.path().join("g.csv"), b"\xB1\xA8\xB8\xE6\n").unwrap();
        std::fs::write(dir.path().join("x.bin"), b"\x00\x01\x02").unwrap();
        std::fs::write(dir.path().join("i.png"), b"\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR").unwrap();
        let text = read(&home, &dir.path().join("a.md")).unwrap();
        assert_eq!((text.kind, text.text.as_deref(), text.encoding.as_deref(), text.writable), (FileKind::Text, Some("# hi\n"), Some("utf-8"), true));
        assert_eq!(text.hash.as_deref(), Some(hash(b"# hi\n").as_str()));
        let gbk = read(&home, &dir.path().join("g.csv")).unwrap();
        assert_eq!((gbk.kind, gbk.encoding.as_deref(), gbk.writable), (FileKind::Text, Some("gb18030"), false));
        assert_eq!(read(&home, &dir.path().join("x.bin")).unwrap().kind, FileKind::Binary);
        let image = read(&home, &dir.path().join("i.png")).unwrap();
        assert_eq!((image.kind, image.image.map(|i| i.mime_type)), (FileKind::Image, Some("image/png".to_owned())));
        assert_eq!(read(&home, dir.path()).unwrap_err().kind(), io::ErrorKind::IsADirectory);
        // her's own data shows, but cannot be written.
        std::fs::create_dir_all(home.sessions_dir()).unwrap();
        std::fs::write(home.sessions_dir().join("log.jsonl"), "{}\n").unwrap();
        assert!(!read(&home, &home.sessions_dir().join("log.jsonl")).unwrap().writable);
        assert!(refused(&home, &home.root().join("run/her.sock")));
        assert!(!refused(&home, &home.root().join("config.toml")));
    }

    #[tokio::test]
    async fn a_save_needs_the_base_it_was_edited_from() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        let scope = scope(dir.path());
        let file = dir.path().join("notes.txt");
        std::fs::write(&file, "one\r\n").unwrap();
        let base = hash(b"one\r\n");
        let saved = write(&scope, &home, &file, "two\r\n", Some(&base)).await.unwrap();
        assert_eq!((saved.status, saved.hash.as_deref()), (FileWriteStatus::Saved, Some(hash(b"two\r\n").as_str())));
        assert_eq!(std::fs::read(&file).unwrap(), b"two\r\n");
        // The old base no longer matches: nothing is written, and the current hash comes back.
        let conflict = write(&scope, &home, &file, "three", Some(&base)).await.unwrap();
        assert_eq!((conflict.status, conflict.hash), (FileWriteStatus::Conflict, saved.hash.clone()));
        assert_eq!(std::fs::read(&file).unwrap(), b"two\r\n");
        // Written anyway when the person chose to; a deleted file is a conflict, then made again.
        assert_eq!(write(&scope, &home, &file, "three", None).await.unwrap().status, FileWriteStatus::Saved);
        std::fs::remove_file(&file).unwrap();
        let gone = write(&scope, &home, &file, "four", saved.hash.as_deref()).await.unwrap();
        assert_eq!((gone.status, gone.hash), (FileWriteStatus::Conflict, None));
        assert_eq!(write(&scope, &home, &file, "four", None).await.unwrap().status, FileWriteStatus::Saved);
    }

    #[tokio::test]
    async fn a_save_keeps_the_mark_and_refuses_other_encodings_and_her_data() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        let scope = scope(dir.path());
        let bom = dir.path().join("bom.txt");
        std::fs::write(&bom, b"\xEF\xBB\xBFold").unwrap();
        let saved = write(&scope, &home, &bom, "new", Some(&hash(b"\xEF\xBB\xBFold"))).await.unwrap();
        assert_eq!(saved.status, FileWriteStatus::Saved);
        assert_eq!(std::fs::read(&bom).unwrap(), b"\xEF\xBB\xBFnew");
        assert_eq!(saved.hash.as_deref(), Some(hash(b"\xEF\xBB\xBFnew").as_str()));
        let gbk = dir.path().join("g.csv");
        std::fs::write(&gbk, b"\xB1\xA8\xB8\xE6").unwrap();
        assert_eq!(write(&scope, &home, &gbk, "x", None).await.unwrap().status, FileWriteStatus::Refused);
        std::fs::create_dir_all(home.logs_dir()).unwrap();
        let log = home.logs_dir().join("her.log");
        assert_eq!(write(&scope, &home, &log, "x", None).await.unwrap().status, FileWriteStatus::Refused);
        assert!(!log.exists());
    }

    #[tokio::test]
    async fn a_save_waits_for_the_tool_that_holds_the_file() {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        let scope = scope(dir.path());
        let file = dir.path().join("shared.txt");
        std::fs::write(&file, "base").unwrap();
        let base = hash(b"base");
        // A tool's write: it holds the lock, and changes the file before letting go.
        let guard = scope.locks.lock(&file).await;
        let save = {
            let (scope, home, file, base) = (scope.clone(), home.clone(), file.clone(), base.clone());
            tokio::spawn(async move { write(&scope, &home, &file, "mine", Some(&base)).await.unwrap() })
        };
        tokio::time::sleep(Duration::from_millis(100)).await;
        assert!(!save.is_finished(), "the save went on while the tool held the file");
        std::fs::write(&file, "the tool's").unwrap();
        drop(guard);
        // It sees the tool's version, which is not its base: nothing of the tool's is lost.
        let result = save.await.unwrap();
        assert_eq!(result.status, FileWriteStatus::Conflict);
        assert_eq!(std::fs::read_to_string(&file).unwrap(), "the tool's");
    }

    #[test]
    fn names_sort_as_people_read_them() {
        let mut names = vec!["file10", "File2", "file1", "a", "B", "file02"];
        names.sort_by(|a, b| natural_cmp(a, b));
        assert_eq!(names, ["a", "B", "file1", "File2", "file02", "file10"]);
    }
}
