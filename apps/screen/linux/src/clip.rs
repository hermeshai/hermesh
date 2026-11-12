//! The clipboard both ways (docs/computer-control-linux.md §3.8): the
//! phone's text goes on the desktop's clipboard and is pasted as the phone
//! asks; what is copied on the machine is told to hermeshd. Each way keeps
//! the clipboard of its own desktop; what they share is here.

use std::io::Read;
use std::os::fd::{AsRawFd, OwnedFd};
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::{Duration, Instant};

use zbus::zvariant::{OwnedValue, Value};

use crate::keys;

/// The most text a clipboard carries either way, in bytes (ClipboardMax of
/// internal/screen).
pub const MAX: usize = 60000;

/// The types the helper offers its text as.
pub const OFFERED: [&str; 3] = ["text/plain;charset=utf-8", "text/plain", "UTF8_STRING"];

/// text_type picks the type to read copied text as, of those offered: UTF-8
/// text only.
pub fn text_type(offered: &[String]) -> Option<&'static str> {
    ["text/plain;charset=utf-8", "UTF8_STRING"]
        .into_iter()
        .find(|t| offered.iter().any(|o| o == t))
}

/// strings reads the types a program offers from a desktop's options: a
/// list of strings, which Mutter's session sends inside a struct, `(as)`.
pub fn strings(v: &OwnedValue) -> Vec<String> {
    fn list(v: &Value) -> Vec<String> {
        match v {
            Value::Array(a) => a.iter().flat_map(list).collect(),
            Value::Structure(s) => s.fields().iter().flat_map(list).collect(),
            Value::Value(v) => list(v),
            Value::Str(s) => vec![s.to_string()],
            _ => vec![],
        }
    }
    list(v)
}

/// The modifiers pressed with V to paste as the phone asks: an app's
/// Ctrl+V, a terminal's Ctrl+Shift+V; none, not pasted.
pub fn paste_mods(paste: &str) -> Option<&'static [u32]> {
    match paste {
        "PASTE_APP" => Some(&[keys::CONTROL_L]),
        "PASTE_TERMINAL" => Some(&[keys::CONTROL_L, keys::SHIFT_L]),
        _ => None,
    }
}

/// The modifiers for text that can't be typed: as the phone asks, else as
/// an app pastes.
pub fn text_paste_mods(paste: &str) -> &'static [u32] {
    paste_mods(paste).unwrap_or(&[keys::CONTROL_L])
}

static STARTED: AtomicBool = AtomicBool::new(false);
/// What the helper put on the clipboard or told last.
static LAST: Mutex<String> = Mutex::new(String::new());

/// started: the session is under way; what is copied from now on is told.
pub fn started() {
    STARTED.store(true, Ordering::SeqCst);
}

/// put notes text the helper itself puts on the clipboard: it is not told
/// back.
pub fn put(text: &str) {
    *LAST.lock().unwrap() = text.to_string();
}

/// fresh says whether copied text is to be told: not empty, not too long,
/// not what was put or told last.
fn fresh(last: &mut String, text: &str) -> bool {
    if text.is_empty() || text.len() > MAX || *last == text {
        return false;
    }
    *last = text.to_string();
    true
}

/// copied tells hermeshd that text was copied on the machine.
pub fn copied(text: &str) {
    if !STARTED.load(Ordering::SeqCst) || !fresh(&mut LAST.lock().unwrap(), text) {
        return;
    }
    crate::out().json(crate::pipe::CLIPBOARD, &serde_json::json!({ "text": text }));
}

/// read_text reads what a program copied from the end of a pipe: UTF-8 up
/// to MAX bytes, within a few seconds, or nothing.
pub fn read_text(fd: OwnedFd) -> Option<String> {
    let raw = fd.as_raw_fd();
    let mut f = std::fs::File::from(fd);
    let deadline = Instant::now() + Duration::from_secs(3);
    let mut buf = Vec::new();
    let mut chunk = [0u8; 16384];
    loop {
        let left = deadline.saturating_duration_since(Instant::now());
        let mut p = libc::pollfd {
            fd: raw,
            events: libc::POLLIN,
            revents: 0,
        };
        if left.is_zero() || unsafe { libc::poll(&mut p, 1, left.as_millis() as i32) } <= 0 {
            return None;
        }
        match f.read(&mut chunk) {
            Ok(0) => break,
            Ok(n) => {
                buf.extend_from_slice(&chunk[..n]);
                if buf.len() > MAX {
                    return None;
                }
            }
            Err(e) if e.kind() == std::io::ErrorKind::Interrupted => {}
            Err(_) => return None,
        }
    }
    String::from_utf8(buf).ok()
}

/// pipe makes a pipe: its end to read, its end to write.
pub fn pipe() -> Option<(OwnedFd, OwnedFd)> {
    use std::os::fd::FromRawFd;
    let mut fds = [0; 2];
    if unsafe { libc::pipe2(fds.as_mut_ptr(), libc::O_CLOEXEC) } != 0 {
        return None;
    }
    unsafe { Some((OwnedFd::from_raw_fd(fds[0]), OwnedFd::from_raw_fd(fds[1]))) }
}

/// watch_file stands in for the machine's clipboard in tests
/// (--test-clipboard): what others write to the file is told, as it changes.
pub fn watch_file(path: PathBuf) {
    std::thread::spawn(move || {
        let read = || std::fs::read_to_string(&path).unwrap_or_default();
        let mut was = read();
        loop {
            std::thread::sleep(Duration::from_millis(200));
            let now = read();
            if now != was {
                copied(&now);
                was = now;
            }
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn what_is_told() {
        let mut last = String::new();
        assert!(!fresh(&mut last, ""));
        assert!(fresh(&mut last, "a"));
        assert!(!fresh(&mut last, "a"), "the same twice in a row");
        assert!(fresh(&mut last, "b"));
        assert!(!fresh(&mut last, &"x".repeat(MAX + 1)));
        assert!(fresh(&mut last, &"x".repeat(MAX)));
    }

    #[test]
    fn types_and_shortcuts() {
        let offered = |t: &[&str]| t.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        assert_eq!(
            text_type(&offered(&[
                "TEXT",
                "UTF8_STRING",
                "text/plain;charset=utf-8"
            ])),
            Some("text/plain;charset=utf-8")
        );
        assert_eq!(text_type(&offered(&["image/png", "text/plain"])), None);
        let types = vec!["UTF8_STRING".to_string(), "TEXT".to_string()];
        let plain = OwnedValue::try_from(Value::from(types.clone())).unwrap();
        let wrapped = OwnedValue::try_from(Value::from((types.clone(),))).unwrap();
        assert_eq!((strings(&plain), strings(&wrapped)), (types.clone(), types));
        assert_eq!(
            paste_mods("PASTE_TERMINAL"),
            Some(&[keys::CONTROL_L, keys::SHIFT_L][..])
        );
        assert_eq!(paste_mods("PASTE_APP"), Some(&[keys::CONTROL_L][..]));
        assert_eq!(paste_mods(""), None);
        assert_eq!(text_paste_mods(""), &[keys::CONTROL_L]);
    }

    #[test]
    fn reads_a_pipe() {
        use std::io::Write;
        let (r, w) = pipe().unwrap();
        let mut w = std::fs::File::from(w);
        w.write_all("你好".as_bytes()).unwrap();
        drop(w);
        assert_eq!(read_text(r).as_deref(), Some("你好"));
        let (r, w) = pipe().unwrap();
        std::thread::spawn(move || {
            let mut w = std::fs::File::from(w);
            for _ in 0..(MAX / 1000 + 1) {
                let _ = w.write_all(&[b'a'; 1000]);
            }
        });
        assert_eq!(read_text(r), None, "too long");
    }
}
