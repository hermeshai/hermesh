//! The terminal of the interactive mode (standalone-cli-design §5): finished text goes into the
//! terminal's own scrollback, and only a few lines at the bottom are drawn again and again.

pub mod editor;
pub mod live;
pub mod markdown;

use std::io::Write;

const ON: &str = "\x1b[?2004h\x1b[>1u";
const OFF: &str = "\x1b[<1u\x1b[?2004l";

/// Raw mode and bracketed paste, for as long as this lives. Panics restore the terminal too.
pub struct Raw;

impl Raw {
    pub fn enter() -> std::io::Result<Self> {
        static HOOK: std::sync::Once = std::sync::Once::new();
        HOOK.call_once(|| {
            let default = std::panic::take_hook();
            std::panic::set_hook(Box::new(move |info| {
                restore();
                default(info);
            }));
        });
        crossterm::terminal::enable_raw_mode()?;
        let mut out = std::io::stdout();
        out.write_all(ON.as_bytes())?;
        out.flush()?;
        Ok(Self)
    }
}

impl Drop for Raw {
    fn drop(&mut self) {
        restore();
    }
}

pub fn restore() {
    let _ = crossterm::terminal::disable_raw_mode();
    let mut out = std::io::stdout();
    let _ = out.write_all(OFF.as_bytes());
    let _ = out.flush();
}
