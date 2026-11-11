//! Finding the desktop. hermeshd runs as a systemd user service that often
//! starts before anyone logs in, so its environment names no display: the
//! helper asks logind and the user's systemd, and looks for the sockets.

use std::collections::HashMap;
use std::path::Path;
use std::process::Command;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    X11,
    /// GNOME's own remote desktop service (Mutter's D-Bus interfaces).
    Mutter,
    /// The desktop portal's RemoteDesktop and ScreenCast (Plasma, COSMIC).
    Portal,
    /// wlroots' protocols (Sway, Hyprland, niri and their kind).
    Wlroots,
}

impl Kind {
    pub fn name(self) -> &'static str {
        match self {
            Kind::X11 => "x11",
            Kind::Mutter => "gnome",
            Kind::Portal => "portal",
            Kind::Wlroots => "wlroots",
        }
    }
}

const VARS: [&str; 7] = [
    "DISPLAY",
    "XAUTHORITY",
    "WAYLAND_DISPLAY",
    "XDG_CURRENT_DESKTOP",
    "XDG_SESSION_TYPE",
    "XDG_RUNTIME_DIR",
    "DBUS_SESSION_BUS_ADDRESS",
];

/// The graphical session logind knows of for this user.
#[derive(Default, Debug)]
pub struct Session {
    pub kind: String,
    pub locked: bool,
}

fn uid() -> u32 {
    unsafe { libc::getuid() }
}

fn run(cmd: &str, args: &[&str]) -> Option<String> {
    let out = Command::new(cmd)
        .args(args)
        .stderr(std::process::Stdio::null())
        .output()
        .ok()?;
    out.status
        .success()
        .then(|| String::from_utf8_lossy(&out.stdout).into_owned())
}

fn props(text: &str) -> HashMap<String, String> {
    text.lines()
        .filter_map(|l| l.split_once('='))
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}

/// logind_session is the user's graphical session, if logind has one.
pub fn logind_session() -> Option<Session> {
    let u = uid().to_string();
    let user = props(&run(
        "loginctl",
        &["show-user", &u, "-p", "Display", "-p", "Sessions"],
    )?);
    let mut ids: Vec<String> = user
        .get("Display")
        .filter(|d| !d.is_empty())
        .cloned()
        .into_iter()
        .collect();
    ids.extend(
        user.get("Sessions")
            .map(|s| s.split_whitespace().map(String::from).collect::<Vec<_>>())
            .unwrap_or_default(),
    );
    for id in ids {
        let s = props(&run(
            "loginctl",
            &[
                "show-session",
                &id,
                "-p",
                "Type",
                "-p",
                "LockedHint",
                "-p",
                "State",
            ],
        )?);
        let kind = s.get("Type").cloned().unwrap_or_default();
        if kind == "x11" || kind == "wayland" {
            return Some(Session {
                kind,
                locked: s.get("LockedHint").is_some_and(|v| v == "yes"),
            });
        }
    }
    None
}

/// session_env fills in, in this process's environment, what the desktop's
/// programs have: from the user's systemd, where GNOME and Plasma put it,
/// else from the sockets the desktop made.
pub fn session_env() {
    let missing: Vec<&str> = VARS
        .iter()
        .copied()
        .filter(|v| std::env::var_os(v).is_none())
        .collect();
    if missing.is_empty() {
        return;
    }
    if let Some(text) = run("systemctl", &["--user", "show-environment"]) {
        let env = props(&text);
        for v in &missing {
            if let Some(val) = env.get(*v) {
                std::env::set_var(v, val);
            }
        }
    }
    if std::env::var_os("XDG_RUNTIME_DIR").is_none() {
        let dir = format!("/run/user/{}", uid());
        if Path::new(&dir).is_dir() {
            std::env::set_var("XDG_RUNTIME_DIR", dir);
        }
    }
    let runtime = std::env::var("XDG_RUNTIME_DIR").unwrap_or_default();
    if std::env::var_os("DBUS_SESSION_BUS_ADDRESS").is_none()
        && Path::new(&runtime).join("bus").exists()
    {
        std::env::set_var(
            "DBUS_SESSION_BUS_ADDRESS",
            format!("unix:path={runtime}/bus"),
        );
    }
    if std::env::var_os("WAYLAND_DISPLAY").is_none() && !runtime.is_empty() {
        if let Ok(rd) = std::fs::read_dir(&runtime) {
            let mut names: Vec<String> = rd
                .flatten()
                .map(|e| e.file_name().to_string_lossy().into_owned())
                .filter(|n| n.starts_with("wayland-") && !n.ends_with(".lock"))
                .collect();
            names.sort();
            if let Some(n) = names.first() {
                std::env::set_var("WAYLAND_DISPLAY", n);
            }
        }
    }
    if std::env::var_os("DISPLAY").is_none() {
        if let Ok(rd) = std::fs::read_dir("/tmp/.X11-unix") {
            let mut nums: Vec<u32> = rd
                .flatten()
                .filter_map(|e| {
                    e.file_name()
                        .to_string_lossy()
                        .strip_prefix('X')
                        .and_then(|n| n.parse().ok())
                })
                .collect();
            nums.sort();
            if let Some(n) = nums.first() {
                std::env::set_var("DISPLAY", format!(":{n}"));
            }
        }
    }
}

/// find picks the way to this user's desktop, or says why there is none:
/// ("no_display", why) or ("locked", "").
pub fn find() -> Result<Kind, (&'static str, String)> {
    session_env();
    let forced = std::env::var("HERMESH_SCREEN_WAY").unwrap_or_default();
    let session = logind_session();
    let kind = session
        .as_ref()
        .map(|s| s.kind.clone())
        .or_else(|| std::env::var("XDG_SESSION_TYPE").ok())
        .unwrap_or_default();
    let wayland = std::env::var_os("WAYLAND_DISPLAY").is_some();
    let x = std::env::var_os("DISPLAY").is_some();
    let pick = |k: Kind| -> Result<Kind, (&'static str, String)> {
        if k == Kind::Mutter && session.as_ref().is_some_and(|s| s.locked) {
            // GNOME ends every remote session while it is locked, and refuses new ones.
            return Err(("locked", String::new()));
        }
        Ok(k)
    };
    match forced.as_str() {
        "x11" => return Ok(Kind::X11),
        "gnome" => return pick(Kind::Mutter),
        "portal" => return Ok(Kind::Portal),
        "wlroots" => return Ok(Kind::Wlroots),
        _ => {}
    }
    let use_wayland = match kind.as_str() {
        "x11" => false,
        "wayland" => true,
        _ => wayland,
    };
    if use_wayland && wayland {
        if crate::mutter::available() {
            return pick(Kind::Mutter);
        }
        if crate::wlr::available() {
            return Ok(Kind::Wlroots);
        }
        if crate::portal::available() {
            return Ok(Kind::Portal);
        }
        return Err((
            "no_display",
            "this Wayland desktop offers no way to share its screen".into(),
        ));
    }
    if x {
        return match crate::x11::probe() {
            Ok(()) => Ok(Kind::X11),
            Err(e) => Err(("no_display", e)),
        };
    }
    Err(("no_display", "no desktop is running".into()))
}
