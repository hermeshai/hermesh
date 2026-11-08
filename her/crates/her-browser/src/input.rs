//! Keys for `Input.dispatchKeyEvent`: names such as `Enter` or `Control+a` turned into the
//! fields the browser needs.

use serde_json::Value;
use serde_json::json;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Key {
    pub key: String,
    pub code: String,
    pub key_code: i64,
    /// What the key types, if anything.
    pub text: Option<String>,
}

/// A key combination: modifiers held around one key.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Chord {
    pub modifiers: Vec<Key>,
    pub key: Key,
}

/// CDP modifier bits: Alt 1, Control 2, Meta 4, Shift 8.
pub fn modifier_bits(modifiers: &[Key]) -> i64 {
    modifiers
        .iter()
        .map(|m| match m.key.as_str() {
            "Alt" => 1,
            "Control" => 2,
            "Meta" => 4,
            "Shift" => 8,
            _ => 0,
        })
        .sum()
}

/// Parses `Enter`, `Shift+Tab`, `Control+a`, `Meta+Shift+K`.
pub fn parse_chord(text: &str) -> Result<Chord, String> {
    let parts: Vec<&str> = text.split('+').collect();
    // `+` itself, or a chord ending in it ("Shift++").
    let (modifier_names, key_name) = if text.ends_with("++") || text == "+" {
        (&parts[..parts.len().saturating_sub(2)], "+")
    } else {
        (&parts[..parts.len() - 1], parts[parts.len() - 1])
    };
    let mut modifiers = Vec::new();
    for name in modifier_names {
        let key = match name.to_ascii_lowercase().as_str() {
            "alt" | "option" => named("Alt"),
            "control" | "ctrl" => named("Control"),
            "meta" | "cmd" | "command" => named("Meta"),
            "shift" => named("Shift"),
            _ => None,
        }
        .ok_or_else(|| format!("unknown modifier \"{name}\" in \"{text}\""))?;
        modifiers.push(key);
    }
    let key = key_for(key_name).ok_or_else(|| format!("unknown key \"{key_name}\" in \"{text}\""))?;
    Ok(Chord { modifiers, key })
}

/// A named key or a single character.
pub fn key_for(name: &str) -> Option<Key> {
    if let Some(key) = named(name) {
        return Some(key);
    }
    let mut chars = name.chars();
    let c = chars.next()?;
    if chars.next().is_some() {
        return None;
    }
    Some(char_key(c))
}

/// The key that types `c`.
pub fn char_key(c: char) -> Key {
    let (code, key_code) = match c {
        'a'..='z' => (format!("Key{}", c.to_ascii_uppercase()), c.to_ascii_uppercase() as i64),
        'A'..='Z' => (format!("Key{c}"), c as i64),
        '0'..='9' => (format!("Digit{c}"), c as i64),
        ' ' => ("Space".to_string(), 32),
        _ => (String::new(), 0),
    };
    Key {
        key: c.to_string(),
        code,
        key_code,
        text: Some(c.to_string()),
    }
}

fn named(name: &str) -> Option<Key> {
    let (key, code, key_code, text) = match name {
        "Enter" | "Return" => ("Enter", "Enter", 13, Some("\r")),
        "Tab" => ("Tab", "Tab", 9, None),
        "Escape" | "Esc" => ("Escape", "Escape", 27, None),
        "Backspace" => ("Backspace", "Backspace", 8, None),
        "Delete" => ("Delete", "Delete", 46, None),
        "Space" => (" ", "Space", 32, Some(" ")),
        "ArrowUp" => ("ArrowUp", "ArrowUp", 38, None),
        "ArrowDown" => ("ArrowDown", "ArrowDown", 40, None),
        "ArrowLeft" => ("ArrowLeft", "ArrowLeft", 37, None),
        "ArrowRight" => ("ArrowRight", "ArrowRight", 39, None),
        "Home" => ("Home", "Home", 36, None),
        "End" => ("End", "End", 35, None),
        "PageUp" => ("PageUp", "PageUp", 33, None),
        "PageDown" => ("PageDown", "PageDown", 34, None),
        "Insert" => ("Insert", "Insert", 45, None),
        "Alt" => ("Alt", "AltLeft", 18, None),
        "Control" => ("Control", "ControlLeft", 17, None),
        "Meta" => ("Meta", "MetaLeft", 91, None),
        "Shift" => ("Shift", "ShiftLeft", 16, None),
        f if f.len() >= 2 && f.starts_with('F') && f[1..].parse::<u8>().is_ok_and(|n| (1..=12).contains(&n)) => {
            let n: i64 = f[1..].parse().ok()?;
            return Some(Key {
                key: f.to_string(),
                code: f.to_string(),
                key_code: 111 + n,
                text: None,
            });
        }
        _ => return None,
    };
    Some(Key {
        key: key.to_string(),
        code: code.to_string(),
        key_code,
        text: text.map(str::to_string),
    })
}

/// Parameters of one key event. Text is typed only without Control, Alt or Meta held.
pub fn key_event(kind: &str, key: &Key, modifiers: i64) -> Value {
    let mut params = json!({
        "type": kind,
        "key": key.key,
        "code": key.code,
        "windowsVirtualKeyCode": key.key_code,
        "nativeVirtualKeyCode": key.key_code,
        "modifiers": modifiers,
    });
    let commands = modifiers & 0b0111 != 0;
    if kind == "keyDown"
        && !commands
        && let Some(text) = &key.text
    {
        params["text"] = Value::String(text.clone());
        params["unmodifiedText"] = Value::String(text.clone());
    }
    if kind == "keyDown" && (key.text.is_none() || commands) {
        params["type"] = Value::String("rawKeyDown".into());
    }
    params
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn chords() {
        let chord = parse_chord("Control+a").unwrap();
        assert_eq!(modifier_bits(&chord.modifiers), 2);
        assert_eq!(chord.key.code, "KeyA");
        let chord = parse_chord("Shift+Tab").unwrap();
        assert_eq!((modifier_bits(&chord.modifiers), chord.key.key_code), (8, 9));
        assert_eq!(parse_chord("Enter").unwrap().key.text.as_deref(), Some("\r"));
        assert_eq!(parse_chord("Shift++").unwrap().key.key, "+");
        assert_eq!(parse_chord("F5").unwrap().key.key_code, 116);
        assert!(parse_chord("Hyper+a").is_err());
        assert!(parse_chord("Nope").is_err());
    }

    #[test]
    fn events_type_text_only_without_command_modifiers() {
        let a = char_key('a');
        assert_eq!(key_event("keyDown", &a, 0)["text"], "a");
        let held = key_event("keyDown", &a, 2);
        assert!(held.get("text").is_none());
        assert_eq!(held["type"], "rawKeyDown");
        assert_eq!(key_event("keyDown", &key_for("Tab").unwrap(), 0)["type"], "rawKeyDown");
    }
}
