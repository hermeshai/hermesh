//! Keys: what the viewer's key messages are in X keysyms and evdev codes,
//! the two ways Linux desktops take keys.

use crate::pipe::Key;

pub const SHIFT_L: u32 = 0xffe1;
pub const CONTROL_L: u32 = 0xffe3;
pub const ALT_L: u32 = 0xffe9;
pub const SUPER_L: u32 = 0xffeb;

/// The keysym of a named key, as the proto names it.
pub fn named(name: &str) -> Option<u32> {
    Some(match name {
        "NAMED_KEY_RETURN" => 0xff0d,
        "NAMED_KEY_TAB" => 0xff09,
        "NAMED_KEY_SPACE" => 0x20,
        "NAMED_KEY_BACKSPACE" => 0xff08,
        "NAMED_KEY_ESCAPE" => 0xff1b,
        "NAMED_KEY_LEFT" => 0xff51,
        "NAMED_KEY_UP" => 0xff52,
        "NAMED_KEY_RIGHT" => 0xff53,
        "NAMED_KEY_DOWN" => 0xff54,
        "NAMED_KEY_DELETE" => 0xffff,
        "NAMED_KEY_HOME" => 0xff50,
        "NAMED_KEY_END" => 0xff57,
        "NAMED_KEY_PAGE_UP" => 0xff55,
        "NAMED_KEY_PAGE_DOWN" => 0xff56,
        _ => return None,
    })
}

/// The keysym that types c.
pub fn of_char(c: char) -> u32 {
    match c {
        '\n' | '\r' => 0xff0d,
        '\t' => 0xff09,
        '\u{8}' => 0xff08,
        c if (0x20..0x7f).contains(&(c as u32)) || (0xa0..0x100).contains(&(c as u32)) => c as u32,
        c => 0x0100_0000 | c as u32,
    }
}

/// What a key message presses: the modifiers' keysyms, then the key's.
/// A character with a modifier is its key, as a shortcut names it: lower
/// case, Shift being a modifier of its own.
pub fn of_key(k: &Key) -> Option<(Vec<u32>, u32)> {
    let sym = if !k.key.is_empty() {
        named(&k.key)?
    } else {
        let c = k.character.chars().next()?;
        let any = k.control || k.option || k.command;
        of_char(if any { c.to_ascii_lowercase() } else { c })
    };
    Some((modifiers(k), sym))
}

pub fn modifiers(k: &Key) -> Vec<u32> {
    let mut m = Vec::new();
    if k.control {
        m.push(CONTROL_L);
    }
    if k.option {
        m.push(ALT_L);
    }
    if k.command {
        m.push(SUPER_L);
    }
    if k.shift {
        m.push(SHIFT_L);
    }
    m
}

/// The xkb name of a keysym, for a keymap the helper writes.
pub fn xkb_name(sym: u32) -> String {
    match sym {
        0xff0d => "Return".into(),
        0xff09 => "Tab".into(),
        0x20 => "space".into(),
        0xff08 => "BackSpace".into(),
        0xff1b => "Escape".into(),
        0xff51 => "Left".into(),
        0xff52 => "Up".into(),
        0xff53 => "Right".into(),
        0xff54 => "Down".into(),
        0xffff => "Delete".into(),
        0xff50 => "Home".into(),
        0xff57 => "End".into(),
        0xff55 => "Prior".into(),
        0xff56 => "Next".into(),
        SHIFT_L => "Shift_L".into(),
        CONTROL_L => "Control_L".into(),
        ALT_L => "Alt_L".into(),
        SUPER_L => "Super_L".into(),
        s if s & 0xff00_0000 == 0x0100_0000 => format!("U{:04X}", s & 0x00ff_ffff),
        s if s < 0x100 => format!("U{:04X}", s),
        s => format!("0x{s:08x}"),
    }
}

/// The evdev button codes.
pub const BTN_LEFT: u32 = 0x110;
pub const BTN_RIGHT: u32 = 0x111;

/// Wheel: the viewer sends how far the content should move, in points;
/// a step of a wheel is this many.
pub const WHEEL_STEP: f64 = 40.0;

/// Steps turns the distance gathered so far into whole wheel steps,
/// keeping what is left for the next.
pub fn steps(acc: &mut f64, d: f64) -> i32 {
    *acc += d;
    let n = (*acc / WHEEL_STEP).trunc();
    *acc -= n * WHEEL_STEP;
    n as i32
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn chars_and_keys() {
        assert_eq!(of_char('a'), 0x61);
        assert_eq!(of_char('É'), 0xc9);
        assert_eq!(of_char('你'), 0x0100_4f60);
        assert_eq!(of_char('\n'), 0xff0d);
        let k = Key {
            character: "C".into(),
            control: true,
            ..Default::default()
        };
        assert_eq!(of_key(&k), Some((vec![CONTROL_L], 0x63)));
        let k = Key {
            key: "NAMED_KEY_ESCAPE".into(),
            ..Default::default()
        };
        assert_eq!(of_key(&k), Some((vec![], 0xff1b)));
        assert_eq!(xkb_name(0x0100_4f60), "U4F60");
        assert_eq!(xkb_name(0x61), "U0061");
    }

    #[test]
    fn wheel_steps() {
        let mut acc = 0.0;
        assert_eq!(steps(&mut acc, 30.0), 0);
        assert_eq!(steps(&mut acc, 30.0), 1);
        assert_eq!(steps(&mut acc, -100.0), -2);
        assert!((acc - -0.0).abs() < 1e-9 || acc.abs() < WHEEL_STEP);
    }
}
