//! GNOME: Mutter's own remote desktop service, the one GNOME's Remote
//! Desktop uses. It takes calls from any program of the user's session and
//! shows no dialog; the picture comes through PipeWire. It is not a public
//! interface: when GNOME refuses, the helper uses the portal.

use std::collections::HashMap;
use std::io::Write;
use std::os::fd::{FromRawFd, IntoRawFd};
use std::sync::{Arc, Mutex};

use zbus::blocking::{fdo::DBusProxy, Connection, Proxy};
use zbus::zvariant::{OwnedFd, OwnedObjectPath, Value};

use crate::keys;
use crate::pipe::Input;
use crate::video::Video;
use crate::{pipewire, Way};

const RD: &str = "org.gnome.Mutter.RemoteDesktop";
const SC: &str = "org.gnome.Mutter.ScreenCast";

/// available says whether this desktop has the service.
pub fn available() -> bool {
    let Ok(c) = Connection::session() else {
        return false;
    };
    let Ok(dbus) = DBusProxy::new(&c) else {
        return false;
    };
    [RD, SC].iter().all(|n| {
        zbus::names::BusName::try_from(*n)
            .ok()
            .and_then(|n| dbus.name_has_owner(n).ok())
            .unwrap_or(false)
    })
}

pub struct Mutter {
    session: Proxy<'static>,
    stream: String,
    held: Vec<u32>,
    wheel: (f64, f64),
    /// Text waiting to be pasted, given to whoever asks for the clipboard.
    clip: Option<Arc<Mutex<String>>>,
}

fn err(e: impl std::fmt::Display) -> String {
    e.to_string()
}

impl Mutter {
    pub fn start(video: Arc<Video>, _pointer: bool) -> Result<Mutter, String> {
        let t = std::time::Instant::now();
        let log = |what: &str| {
            crate::out().log(&format!(
                "gnome: {what} after {} ms",
                t.elapsed().as_millis()
            ))
        };
        let conn = Connection::session().map_err(err)?;
        let rd = Proxy::new(&conn, RD, "/org/gnome/Mutter/RemoteDesktop", RD).map_err(err)?;
        let path: OwnedObjectPath = rd.call("CreateSession", &()).map_err(err)?;
        let session =
            Proxy::new_owned(conn.clone(), RD, path, format!("{RD}.Session")).map_err(err)?;
        let id: String = session.get_property("SessionId").map_err(err)?;
        let sc = Proxy::new(&conn, SC, "/org/gnome/Mutter/ScreenCast", SC).map_err(err)?;
        let opts: HashMap<&str, Value> =
            HashMap::from([("remote-desktop-session-id", Value::from(id.as_str()))]);
        let sc_path: OwnedObjectPath = sc.call("CreateSession", &(opts,)).map_err(err)?;
        let sc_session =
            Proxy::new_owned(conn.clone(), SC, sc_path, format!("{SC}.Session")).map_err(err)?;
        // The pointer drawn in the picture: Mutter can't change it later in a stream.
        let rec: HashMap<&str, Value> = HashMap::from([("cursor-mode", Value::U32(1))]);
        let stream_path: OwnedObjectPath =
            sc_session.call("RecordMonitor", &("", rec)).map_err(err)?;
        let stream = Proxy::new_owned(
            conn.clone(),
            SC,
            stream_path.clone(),
            format!("{SC}.Stream"),
        )
        .map_err(err)?;
        let mut added = stream.receive_signal("PipeWireStreamAdded").map_err(err)?;
        let mut closed = session.receive_signal("Closed").map_err(err)?;
        log("session made");
        session.call::<_, _, ()>("Start", &()).map_err(err)?;
        log("started");
        let msg = added.next().ok_or("GNOME gave no stream")?;
        let (node,): (u32,) = msg.body().deserialize().map_err(err)?;
        log(&format!("stream {node}"));
        pipewire::connect(None, node, video)?;
        log("reading the stream");
        std::thread::spawn(move || {
            if closed.next().is_some() {
                // GNOME ends remote sessions as it locks, and says it is locked a moment later.
                for _ in 0..20 {
                    if crate::detect::logind_session().is_some_and(|s| s.locked) {
                        crate::fail("locked", "");
                    }
                    std::thread::sleep(std::time::Duration::from_millis(150));
                }
                crate::fail("capture", "GNOME ended the session");
            }
        });
        let clip = enable_clipboard(&conn, &session);
        let m = Mutter {
            session,
            stream: stream_path.as_str().to_string(),
            held: vec![],
            wheel: (0.0, 0.0),
            clip,
        };
        // GNOME makes the session's keyboard with its first key, and what is typed while programs
        // take it up is lost: a Shift, which types nothing, goes first.
        m.press(&[], keys::SHIFT_L);
        Ok(m)
    }

    fn call<B: serde::Serialize + zbus::zvariant::DynamicType>(&self, method: &str, body: &B) {
        let _ = self.session.call_noreply(method, body);
    }

    fn keysym(&self, sym: u32, down: bool) {
        self.call("NotifyKeyboardKeysym", &(sym, down));
    }

    fn press(&self, mods: &[u32], sym: u32) {
        for m in mods {
            self.keysym(*m, true);
        }
        self.keysym(sym, true);
        self.keysym(sym, false);
        for m in mods.iter().rev() {
            self.keysym(*m, false);
        }
    }

    fn point(&self, x: f64, y: f64) {
        let (w, h) = pipewire::size();
        let (x, y) = (
            x.clamp(0.0, 1.0) * (w.max(1) - 1) as f64,
            y.clamp(0.0, 1.0) * (h.max(1) - 1) as f64,
        );
        self.call("NotifyPointerMotionAbsolute", &(self.stream.as_str(), x, y));
    }
}

/// enable_clipboard lets the helper offer text to paste: what the keyboard
/// layout has no key for can't be typed through Mutter (it drops such
/// keysyms).
fn enable_clipboard(conn: &Connection, session: &Proxy<'static>) -> Option<Arc<Mutex<String>>> {
    session
        .call::<_, _, ()>("EnableClipboard", &(HashMap::<&str, Value>::new(),))
        .ok()?;
    let text = Arc::new(Mutex::new(String::new()));
    let transfers = session.receive_signal("SelectionTransfer").ok()?;
    let s = Proxy::new_owned(
        conn.clone(),
        RD,
        session.path().to_owned(),
        format!("{RD}.Session"),
    )
    .ok()?;
    let t = text.clone();
    std::thread::spawn(move || {
        for m in transfers {
            let Ok((_mime, serial)) = m.body().deserialize::<(String, u32)>() else {
                continue;
            };
            let ok = (|| -> Option<()> {
                let fd: OwnedFd = s.call("SelectionWrite", &(serial,)).ok()?;
                let raw = std::os::fd::OwnedFd::from(fd).into_raw_fd();
                let mut f = unsafe { std::fs::File::from_raw_fd(raw) };
                f.write_all(t.lock().unwrap().as_bytes()).ok()
            })()
            .is_some();
            let _ = s.call::<_, _, ()>("SelectionWriteDone", &(serial, ok));
        }
    });
    Some(text)
}

/// plain says whether every character of text has a key on a US layout.
pub fn plain(text: &str) -> bool {
    text.chars()
        .all(|c| (' '..='~').contains(&c) || c == '\n' || c == '\t')
}

impl Way for Mutter {
    fn input(&mut self, i: &Input) {
        if let Some(m) = &i.mov {
            self.point(m.x, m.y);
        }
        if let Some(b) = &i.button {
            self.point(b.x, b.y);
            let n = if b.button == "MOUSE_BUTTON_RIGHT" {
                keys::BTN_RIGHT
            } else {
                keys::BTN_LEFT
            };
            self.call("NotifyPointerButton", &(n as i32, b.down));
            if b.down {
                self.held.push(n);
            } else {
                self.held.retain(|h| *h != n);
            }
        }
        if let Some(w) = &i.wheel {
            self.point(w.x, w.y);
            let down = keys::steps(&mut self.wheel.1, w.dy);
            let right = keys::steps(&mut self.wheel.0, w.dx);
            if down != 0 {
                self.call("NotifyPointerAxisDiscrete", &(0u32, down));
            }
            if right != 0 {
                self.call("NotifyPointerAxisDiscrete", &(1u32, right));
            }
        }
        if let Some(k) = &i.key {
            if let Some((mods, sym)) = keys::of_key(k) {
                self.press(&mods, sym);
            }
        }
        if let Some(t) = &i.text {
            match &self.clip {
                Some(clip) if !plain(&t.text) => {
                    *clip.lock().unwrap() = t.text.clone();
                    let mimes: HashMap<&str, Value> = HashMap::from([(
                        "mime-types",
                        Value::from(vec![
                            "text/plain;charset=utf-8",
                            "text/plain",
                            "UTF8_STRING",
                        ]),
                    )]);
                    if self
                        .session
                        .call::<_, _, ()>("SetSelection", &(mimes,))
                        .is_ok()
                    {
                        self.press(&[keys::CONTROL_L], 'v' as u32);
                    }
                }
                _ => {
                    for c in t.text.chars() {
                        self.press(&[], keys::of_char(c));
                    }
                }
            }
        }
    }

    fn show_pointer(&mut self, _shown: bool) {}

    fn release_all(&mut self) {
        for b in std::mem::take(&mut self.held) {
            self.call("NotifyPointerButton", &(b as i32, false));
        }
    }

    fn can_control(&self) -> bool {
        true
    }
}
