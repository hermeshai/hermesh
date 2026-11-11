//! Who is in control, said at the computer. GNOME and Plasma show their own
//! mark while a remote session lasts; on X11 and wlroots nothing does, so
//! the helper sends one desktop notification, with Stop.

use std::collections::HashMap;

use zbus::blocking::{Connection, Proxy};
use zbus::zvariant::Value;

pub fn session_started(way: crate::detect::Kind) {
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
