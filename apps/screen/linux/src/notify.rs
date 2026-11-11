//! Who is in control, said at the computer. GNOME and Plasma show their own
//! mark while a remote session lasts; on X11 and wlroots nothing does, so
//! the helper sends one desktop notification, with Stop. And the desktop
//! kept from going idle (and locking) while a session lasts.

use std::collections::HashMap;

use zbus::blocking::{Connection, Proxy};
use zbus::zvariant::Value;

pub fn session_started(way: crate::detect::Kind) {
    std::thread::spawn(keep_awake);
    if !matches!(way, crate::detect::Kind::X11 | crate::detect::Kind::Wlroots) {
        return;
    }
    std::thread::spawn(|| {
        let _ = notify();
    });
}

fn notify() -> zbus::Result<()> {
    let c = Connection::session()?;
    let p = Proxy::new(
        &c,
        "org.freedesktop.Notifications",
        "/org/freedesktop/Notifications",
        "org.freedesktop.Notifications",
    )?;
    let actions = p.receive_signal("ActionInvoked")?;
    let hints: HashMap<&str, Value> =
        HashMap::from([("resident", Value::Bool(true)), ("urgency", Value::U8(1))]);
    let id: u32 = p.call(
        "Notify",
        &(
            "Hermesh",
            0u32,
            "",
            "Your screen is shared",
            "One of your devices is viewing and controlling this computer through Hermesh.",
            vec!["stop", "Stop"],
            hints,
            0i32,
        ),
    )?;
    for m in actions {
        if let Ok((n, key)) = m.body().deserialize::<(u32, String)>() {
            if n == id && key == "stop" {
                crate::fail("ended", "");
            }
        }
    }
    Ok(())
}

/// keep_awake asks the desktop not to go idle while the helper lives: the
/// inhibition ends with its connection, when the helper exits. GNOME takes
/// it through its session manager, Plasma and others through the
/// freedesktop screen saver interface.
fn keep_awake() {
    let Ok(c) = Connection::session() else { return };
    let gnome = Proxy::new(
        &c,
        "org.gnome.SessionManager",
        "/org/gnome/SessionManager",
        "org.gnome.SessionManager",
    )
    .and_then(|p| p.call::<_, _, u32>("Inhibit", &("Hermesh", 0u32, "Computer Control", 8u32)));
    if gnome.is_err() {
        let _ = Proxy::new(
            &c,
            "org.freedesktop.ScreenSaver",
            "/org/freedesktop/ScreenSaver",
            "org.freedesktop.ScreenSaver",
        )
        .and_then(|p| p.call::<_, _, u32>("Inhibit", &("Hermesh", "Computer Control")));
    }
    // The connection stays open for the helper's life.
    std::mem::forget(c);
}
