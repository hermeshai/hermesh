//! The pipe to hermeshd (internal/screen/pipe.go has the other end):
//! messages of a 4-byte big-endian length, then one byte of kind and the
//! payload. The same as the Mac's helper.

use std::io::{Read, Write};
use std::sync::Mutex;

use serde::{Deserialize, Serialize};

pub const START: u8 = b's';
pub const BITRATE: u8 = b'b';
pub const KEYFRAME: u8 = b'k';
pub const INPUT: u8 = b'i';
pub const POINTER: u8 = b'c';
pub const FORMAT: u8 = b'f';
pub const VIDEO: u8 = b'v';
pub const STATE: u8 = b'e';
pub const LOG: u8 = b'l';
/// To hermeshd: JSON {"text"}, what was copied on the machine.
pub const CLIPBOARD: u8 = b'x';

const MAX_MESSAGE: usize = 16 << 20;

/// Microseconds since 1970, for the stamps a frame carries.
pub fn now_us() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_micros() as u64)
        .unwrap_or(0)
}

/// Out writes whole messages to stdout, one at a time. When hermeshd is gone
/// the helper exits.
pub struct Out {
    lock: Mutex<std::io::Stdout>,
}

impl Out {
    pub fn new() -> Out {
        Out {
            lock: Mutex::new(std::io::stdout()),
        }
    }

    pub fn send(&self, kind: u8, payload: &[u8]) {
        let mut buf = Vec::with_capacity(payload.len() + 5);
        buf.extend_from_slice(&((payload.len() + 1) as u32).to_be_bytes());
        buf.push(kind);
        buf.extend_from_slice(payload);
        let mut out = self.lock.lock().unwrap();
        if out.write_all(&buf).and_then(|_| out.flush()).is_err() {
            std::process::exit(0); // hermeshd went away
        }
    }

    pub fn json<T: Serialize>(&self, kind: u8, v: &T) {
        if let Ok(b) = serde_json::to_vec(v) {
            self.send(kind, &b);
        }
    }

    pub fn log(&self, text: &str) {
        self.send(LOG, text.as_bytes());
    }

    pub fn state(&self, error: &str, detail: &str, can_control: bool) {
        self.json(
            STATE,
            &State {
                error: error.into(),
                detail: detail.into(),
                can_control,
            },
        );
    }
}

/// read_messages calls handle for each message on stdin, until it closes.
pub fn read_messages(mut handle: impl FnMut(u8, &[u8])) {
    let mut stdin = std::io::stdin().lock();
    let mut head = [0u8; 4];
    loop {
        if stdin.read_exact(&mut head).is_err() {
            return;
        }
        let n = u32::from_be_bytes(head) as usize;
        if !(1..=MAX_MESSAGE).contains(&n) {
            return;
        }
        let mut body = vec![0u8; n];
        if stdin.read_exact(&mut body).is_err() {
            return;
        }
        handle(body[0], &body[1..]);
    }
}

#[derive(Deserialize, Default)]
pub struct Start {
    pub max_edge: Option<u32>,
    pub fps: Option<u32>,
    pub bitrate: Option<u32>,
    pub pointer: Option<bool>,
}

#[derive(Deserialize)]
pub struct Bitrate {
    pub bps: u32,
}

#[derive(Deserialize)]
pub struct Pointer {
    pub shown: bool,
}

#[derive(Serialize)]
pub struct Format {
    pub width: u32,
    pub height: u32,
}

#[derive(Serialize)]
pub struct State {
    /// "" while all is well; else "no_display", "consent", "locked",
    /// "capture", "encoder".
    pub error: String,
    pub detail: String,
    /// Whether the viewer's input can be posted.
    pub can_control: bool,
}

/// hermesh.screen.v1.Input as protojson writes it with the proto's field
/// names: one of its members is set.
#[derive(Deserialize, Debug, Default)]
pub struct Input {
    #[serde(rename = "move")]
    pub mov: Option<Move>,
    pub button: Option<Button>,
    pub wheel: Option<Wheel>,
    pub key: Option<Key>,
    pub text: Option<Text>,
    pub clipboard: Option<Clipboard>,
}

#[derive(Deserialize, Debug, Default, Clone, Copy)]
pub struct Move {
    #[serde(default)]
    pub x: f64,
    #[serde(default)]
    pub y: f64,
}

#[derive(Deserialize, Debug, Default)]
pub struct Button {
    #[serde(default)]
    pub x: f64,
    #[serde(default)]
    pub y: f64,
    #[serde(default)]
    pub button: String,
    #[serde(default)]
    pub down: bool,
}

#[derive(Deserialize, Debug, Default)]
pub struct Wheel {
    #[serde(default)]
    pub x: f64,
    #[serde(default)]
    pub y: f64,
    #[serde(default)]
    pub dx: f64,
    #[serde(default)]
    pub dy: f64,
}

#[derive(Deserialize, Debug, Default)]
pub struct Key {
    #[serde(default)]
    pub key: String,
    #[serde(default)]
    pub character: String,
    /// On Linux: Super.
    #[serde(default)]
    pub command: bool,
    /// On Linux: Alt.
    #[serde(default)]
    pub option: bool,
    #[serde(default)]
    pub control: bool,
    #[serde(default)]
    pub shift: bool,
}

#[derive(Deserialize, Debug, Default)]
pub struct Text {
    #[serde(default)]
    pub text: String,
    /// How what can't be typed is pasted: "PASTE_TERMINAL", else as an app.
    #[serde(default)]
    pub paste: String,
}

/// The phone's clipboard, for the machine's: pasted as paste says
/// ("PASTE_APP", "PASTE_TERMINAL"), or only put there.
#[derive(Deserialize, Debug, Default)]
pub struct Clipboard {
    #[serde(default)]
    pub text: String,
    #[serde(default)]
    pub paste: String,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn input_as_protojson_writes_it() {
        let i: Input = serde_json::from_str(
            r#"{"button":{"x":0.5,"y":0.25,"button":"MOUSE_BUTTON_LEFT","down":true,"clicks":1}}"#,
        )
        .unwrap();
        let b = i.button.unwrap();
        assert_eq!(
            (b.x, b.y, b.button.as_str(), b.down),
            (0.5, 0.25, "MOUSE_BUTTON_LEFT", true)
        );
        let i: Input = serde_json::from_str(r#"{"move":{"x":1}}"#).unwrap();
        assert_eq!((i.mov.unwrap().x, i.mov.unwrap().y), (1.0, 0.0));
        let i: Input =
            serde_json::from_str(r#"{"key":{"key":"NAMED_KEY_RETURN","control":true}}"#).unwrap();
        let k = i.key.unwrap();
        assert!(k.control && !k.shift && k.key == "NAMED_KEY_RETURN");
        let i: Input = serde_json::from_str(r#"{"text":{"text":"你好"}}"#).unwrap();
        assert_eq!(i.text.unwrap().text, "你好");
        let i: Input =
            serde_json::from_str(r#"{"text":{"text":"你好","paste":"PASTE_TERMINAL"}}"#).unwrap();
        assert_eq!(i.text.unwrap().paste, "PASTE_TERMINAL");
        let i: Input =
            serde_json::from_str(r#"{"clipboard":{"text":"剪贴板","paste":"PASTE_APP"}}"#).unwrap();
        let c = i.clipboard.unwrap();
        assert_eq!((c.text.as_str(), c.paste.as_str()), ("剪贴板", "PASTE_APP"));
        let i: Input = serde_json::from_str(r#"{"clipboard":{"text":"x"}}"#).unwrap();
        assert_eq!(i.clipboard.unwrap().paste, "");
    }
}
