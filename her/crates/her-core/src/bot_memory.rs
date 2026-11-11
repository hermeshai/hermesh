//! A bot's memory (docs/her-memory.md): notes the bot keeps in `memory/` beside its working
//! folder, with its ordinary file tools. `MEMORY.md` there is the last part of the bot's system
//! prompt, as it was when the prompt last took it. A new copy changes the prompt and so costs a
//! prompt-cache miss; it is taken only when that miss is free: a conversation starts, a
//! compaction has just replaced the context, or the conversation was quiet long enough for the
//! provider's cache to be gone.

use std::io::Read;
use std::path::Path;
use std::time::Duration;

/// The folder's name in a bot's directory.
pub const DIR: &str = "memory";
/// The file that is in the system prompt.
pub const FILE: &str = "MEMORY.md";
/// Bytes of the file that are loaded: every request carries them.
pub const LIMIT: usize = 8 * 1024;
/// With no model response for this long, a provider's prompt cache has most likely expired.
pub const QUIET: Duration = Duration::from_secs(10 * 60);

/// `MEMORY.md` in `dir` as the model is to have it: its text, cut at the last line that fits
/// [`LIMIT`] with a word on what was left out. Empty when there is no file or it cannot be read.
fn load(dir: &Path) -> String {
    let path = dir.join(FILE);
    let mut bytes = Vec::new();
    let read = std::fs::File::open(&path).and_then(|file| file.take(LIMIT as u64 + 1).read_to_end(&mut bytes));
    if read.is_err() {
        return String::new();
    }
    if bytes.len() <= LIMIT {
        return String::from_utf8_lossy(&bytes).trim_end().to_string();
    }
    let size = std::fs::metadata(&path).map_or(bytes.len() as u64, |meta| meta.len());
    bytes.truncate(LIMIT);
    // Whole lines only; a first line longer than the limit is cut where it stands.
    let kept = bytes.iter().rposition(|b| *b == b'\n').map_or(bytes.len(), |at| at + 1);
    let text = String::from_utf8_lossy(&bytes[..kept]);
    format!(
        "{}\n({FILE} is {size} bytes and only its first {kept} are loaded: the lines after these are not in front of you. Shorten it in this turn.)",
        text.trim_end()
    )
}

/// The memory part of a bot's system prompt, from `MEMORY.md` in `dir` as it is now. A bot with
/// nothing written yet still gets it: it says where to write.
pub(crate) fn section(dir: &Path) -> String {
    let text = load(dir);
    let text = if text.is_empty() { "(empty)" } else { text.as_str() };
    format!("<memory path=\"{}\">\n{text}\n</memory>", dir.join(FILE).display())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_missing_file_is_an_empty_memory() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join(FILE);
        assert_eq!(section(dir.path()), format!("<memory path=\"{}\">\n(empty)\n</memory>", path.display()));
        std::fs::write(&path, "- a\n- b\n\n").unwrap();
        assert_eq!(section(dir.path()), format!("<memory path=\"{}\">\n- a\n- b\n</memory>", path.display()));
    }

    #[test]
    fn a_line_longer_than_the_limit_is_cut_where_it_stands() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join(FILE), "字".repeat(LIMIT)).unwrap();
        let text = load(dir.path());
        let (kept, note) = text.split_once('\n').unwrap();
        assert!(kept.len() <= LIMIT + 3, "{}", kept.len());
        assert!(note.starts_with(&format!("({FILE} is {} bytes and only its first {LIMIT} are loaded", 3 * LIMIT)), "{note}");
    }
}
