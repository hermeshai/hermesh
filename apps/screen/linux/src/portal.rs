//! The desktop portal's RemoteDesktop session with its screen (Plasma,
//! COSMIC, and GNOME when its own service refuses). The first session
//! shows a dialog at the computer; the helper keeps the token it is given
//! and uses it from then on. On Plasma 6.3 and later, `--allow` writes the
//! permission that makes the dialog never appear.

use std::collections::HashMap;
use std::io::Write;
use std::os::fd::{FromRawFd, IntoRawFd};
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use zbus::blocking::{Connection, Proxy};
use zbus::zvariant::{OwnedFd, OwnedObjectPath, OwnedValue, Value};

use crate::keys;
use crate::pipe::Input;
use crate::video::Video;
use crate::{pipewire, Way};

const DEST: &str = "org.freedesktop.portal.Desktop";
const PATH: &str = "/org/freedesktop/portal/desktop";
const RD: &str = "org.freedesktop.portal.RemoteDesktop";
const SC: &str = "org.freedesktop.portal.ScreenCast";
const CLIP: &str = "org.freedesktop.portal.Clipboard";

fn err(e: impl std::fmt::Display) -> String {
    e.to_string()
}

/// available says whether a portal here offers remote desktop sessions.
pub fn available() -> bool {
    let Ok(c) = Connection::session() else {
        return false;
    };
    let Ok(p) = Proxy::new(&c, DEST, PATH, RD) else {
        return false;
    };
    p.get_property::<u32>("AvailableDeviceTypes")
        .is_ok_and(|t| t & 3 != 0)
}

/// The token a portal gave, kept for the next session.
fn token_file() -> PathBuf {
    let base = std::env::var_os("XDG_STATE_HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| {
            PathBuf::from(std::env::var_os("HOME").unwrap_or_default()).join(".local/state")
        });
    base.join("hermesh").join("screen-portal-token")
}

/// allow writes the permission Plasma looks for before it shows its dialog,
/// for the helper as the portal names it (a program systemd started with
/// hermeshd's unit has no name of its own there).
pub fn allow() -> Result<(), String> {
    crate::detect::session_env();
    let c = Connection::session().map_err(err)?;
    let store = Proxy::new(
        &c,
        "org.freedesktop.impl.portal.PermissionStore",
        "/org/freedesktop/impl/portal/PermissionStore",
        "org.freedesktop.impl.portal.PermissionStore",
    )
    .map_err(err)?;
    for id in ["remote-desktop", "screencast"] {
        store
            .call::<_, _, ()>(
                "SetPermission",
                &("kde-authorized", true, id, "", vec!["yes"]),
            )
            .map_err(err)?;
    }
    println!("Plasma will let Hermesh show this screen without asking.");
    Ok(())
}

struct Requester {
    conn: Connection,
    sender: String,
    n: u32,
}

impl Requester {
    /// call makes a portal request and waits for its answer. after: called
    /// once if the answer has not come after a moment (a dialog is up).
    fn call<B>(
        &mut self,
        iface: &str,
        method: &str,
        body: impl FnOnce(String) -> B,
        after: Option<&dyn Fn()>,
    ) -> Result<HashMap<String, OwnedValue>, (&'static str, String)>
    where
        B: serde::Serialize + zbus::zvariant::DynamicType,
    {
        self.n += 1;
        let token = format!("hermesh{}_{}", std::process::id(), self.n);
        let path = format!("{PATH}/request/{}/{token}", self.sender);
        let req = Proxy::new(&self.conn, DEST, path, "org.freedesktop.portal.Request")
            .map_err(|e| ("capture", err(e)))?;
        let mut answers = req
            .receive_signal("Response")
            .map_err(|e| ("capture", err(e)))?;
        let p = Proxy::new(&self.conn, DEST, PATH, iface).map_err(|e| ("capture", err(e)))?;
        let _: OwnedObjectPath = p
            .call(method, &body(token))
            .map_err(|e| ("capture", format!("{method}: {e}")))?;
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let _ = tx.send(answers.next());
        });
        let msg = match rx.recv_timeout(Duration::from_millis(1500)) {
            Ok(m) => m,
            Err(_) => {
                if let Some(f) = after {
                    f();
                }
                rx.recv().ok().flatten()
            }
        }
        .ok_or(("capture", format!("{method}: no answer")))?;
        let (code, results): (u32, HashMap<String, OwnedValue>) =
            msg.body().deserialize().map_err(|e| ("capture", err(e)))?;
        match code {
            0 => Ok(results),
            1 => Err(("consent", "refused at the computer".into())),
            _ => Err(("capture", format!("{method} failed"))),
        }
    }
}

fn opts(
    token: String,
    more: Vec<(&'static str, Value<'static>)>,
) -> HashMap<&'static str, Value<'static>> {
    let mut m: HashMap<&str, Value> = more.into_iter().collect();
    m.insert("handle_token", Value::from(token));
    m
}

pub struct Portal {
    rd: Proxy<'static>,
    session: OwnedObjectPath,
    node: u32,
    size: (f64, f64),
    devices: u32,
    held: Vec<u32>,
    wheel: (f64, f64),
    clip: Option<(Proxy<'static>, Arc<Mutex<String>>)>,
}

impl Portal {
    pub fn start(video: Arc<Video>, _pointer: bool) -> Result<Portal, (&'static str, String)> {
        let cap = |e: zbus::Error| ("capture", err(e));
        let conn = Connection::session().map_err(cap)?;
        let sender = conn
            .unique_name()
            .map(|n| n.trim_start_matches(':').replace('.', "_"))
            .unwrap_or_default();
        let mut r = Requester {
            conn: conn.clone(),
            sender,
            n: 0,
        };
        let st = r.call(
            RD,
            "CreateSession",
            |t| {
                (opts(
                    t,
                    vec![("session_handle_token", Value::from("hermesh"))],
                ),)
            },
            None,
        )?;
        let handle: String = st
            .get("session_handle")
            .and_then(|v| String::try_from(v.clone()).ok())
            .ok_or(("capture", "no session".to_string()))?;
        let session = OwnedObjectPath::try_from(handle).map_err(|e| ("capture", err(e)))?;
        let saved = std::fs::read_to_string(token_file()).unwrap_or_default();
        let s1 = session.clone();
        r.call(
            RD,
            "SelectDevices",
            move |t| {
                let mut more = vec![("types", Value::U32(3)), ("persist_mode", Value::U32(2))];
                if !saved.trim().is_empty() {
                    more.push(("restore_token", Value::from(saved.trim().to_string())));
                }
                (s1, opts(t, more))
            },
            None,
        )?;
        let sc = Proxy::new(&conn, DEST, PATH, SC).map_err(cap)?;
        let modes: u32 = sc.get_property("AvailableCursorModes").unwrap_or(0);
        let s2 = session.clone();
        r.call(
            SC,
            "SelectSources",
            move |t| {
                let mut more = vec![("types", Value::U32(1)), ("multiple", Value::Bool(false))];
                if modes & 2 != 0 {
                    more.push(("cursor_mode", Value::U32(2))); // drawn in the picture
                }
                (s2, opts(t, more))
            },
            None,
        )?;
        // The clipboard, for text no key types; asked for before the session starts.
        let clip_proxy = Proxy::new_owned(conn.clone(), DEST, PATH, CLIP)
            .ok()
            .filter(|p| p.get_property::<u32>("version").is_ok());
        if let Some(p) = &clip_proxy {
            let _ = p.call::<_, _, ()>(
                "RequestClipboard",
                &(session.clone(), HashMap::<&str, Value>::new()),
            );
        }
        let s3 = session.clone();
        let consent = || crate::out().state("consent", "", false);
        let started = r.call(
            RD,
            "Start",
            move |t| (s3, "", opts(t, vec![])),
            Some(&consent),
        )?;
        if let Some(tok) = started
            .get("restore_token")
            .and_then(|v| String::try_from(v.clone()).ok())
        {
            let f = token_file();
            let _ = std::fs::create_dir_all(f.parent().unwrap());
            let _ = std::fs::write(&f, tok);
        }
        let devices = started
            .get("devices")
            .and_then(|v| u32::try_from(v.clone()).ok())
            .unwrap_or(0);
        let streams: Vec<(u32, HashMap<String, OwnedValue>)> = started
            .get("streams")
            .and_then(|v| v.try_clone().ok())
            .and_then(|v| v.try_into().ok())
            .unwrap_or_default();
        let (node, props) = streams
            .into_iter()
            .next()
            .ok_or(("capture", "the portal gave no stream".to_string()))?;
        let size = props
            .get("size")
            .and_then(|v| <(i32, i32)>::try_from(v.clone()).ok())
            .map(|(w, h)| (w as f64, h as f64))
            .unwrap_or((0.0, 0.0));
        let fd: OwnedFd = sc
            .call(
                "OpenPipeWireRemote",
                &(session.clone(), HashMap::<&str, Value>::new()),
            )
            .map_err(cap)?;
        let raw = std::os::fd::OwnedFd::from(fd).into_raw_fd();
        pipewire::connect(Some(raw), node, video).map_err(|e| ("capture", e))?;
        let clip_on = started
            .get("clipboard_enabled")
            .and_then(|v| bool::try_from(v.clone()).ok())
            .unwrap_or(false);
        let clip = match (clip_on, clip_proxy) {
            (true, Some(p)) => Some((p.clone(), serve_clipboard(p, session.clone()))),
            _ => None,
        };
        let rd = Proxy::new_owned(conn.clone(), DEST, PATH, RD).map_err(cap)?;
        // The session ends at the computer, or on a locked screen: so does this one.
        let watch = Proxy::new_owned(
            conn.clone(),
            DEST,
            session.clone(),
            "org.freedesktop.portal.Session",
        )
        .map_err(cap)?;
        if let Ok(mut closed) = watch.receive_signal("Closed") {
            std::thread::spawn(move || {
                if closed.next().is_some() {
                    crate::fail("capture", "the desktop ended the session");
                }
            });
        }
        // Plasma 6.6 and later type any character given as a keysym; elsewhere what the layout
        // has no key for is pasted.
        let keysyms_any = std::env::var("XDG_CURRENT_DESKTOP")
            .is_ok_and(|d| d.split(':').any(|d| d.eq_ignore_ascii_case("KDE")));
        let p = Portal {
            rd,
            session,
            node,
            size,
            devices,
            held: vec![],
            wheel: (0.0, 0.0),
            clip: if keysyms_any { None } else { clip },
        };
        // The session's keyboard may come with its first key: a Shift, which types nothing, goes first.
        p.press(&[], keys::SHIFT_L);
        Ok(p)
    }

    /// call waits for each answer: the portal takes calls on threads of its
    /// own, and ones sent without waiting arrive out of order.
    fn call<B: serde::Serialize + zbus::zvariant::DynamicType>(&self, method: &str, body: &B) {
        let _ = self.rd.call::<_, _, ()>(method, body);
    }

    fn none() -> HashMap<&'static str, Value<'static>> {
        HashMap::new()
    }

    fn keysym(&self, sym: u32, down: bool) {
        self.call(
            "NotifyKeyboardKeysym",
            &(&self.session, Self::none(), sym as i32, down as u32),
        );
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
        let (w, h) = if self.size.0 > 0.0 {
            self.size
        } else {
            let (w, h) = pipewire::size();
            (w as f64, h as f64)
        };
        let (x, y) = (
            x.clamp(0.0, 1.0) * (w - 1.0).max(0.0),
            y.clamp(0.0, 1.0) * (h - 1.0).max(0.0),
        );
        self.call(
            "NotifyPointerMotionAbsolute",
            &(&self.session, Self::none(), self.node, x, y),
        );
    }
}

/// serve_clipboard gives the text waiting to whoever pastes.
fn serve_clipboard(p: Proxy<'static>, session: OwnedObjectPath) -> Arc<Mutex<String>> {
    let text = Arc::new(Mutex::new(String::new()));
    let t = text.clone();
    if let Ok(transfers) = p.receive_signal("SelectionTransfer") {
        std::thread::spawn(move || {
            for m in transfers {
                let Ok((_s, _mime, serial)) =
                    m.body().deserialize::<(OwnedObjectPath, String, u32)>()
                else {
                    continue;
                };
                let ok = (|| -> Option<()> {
                    let fd: OwnedFd = p.call("SelectionWrite", &(&session, serial)).ok()?;
                    let raw = std::os::fd::OwnedFd::from(fd).into_raw_fd();
                    let mut f = unsafe { std::fs::File::from_raw_fd(raw) };
                    f.write_all(t.lock().unwrap().as_bytes()).ok()
                })()
                .is_some();
                let _ = p.call::<_, _, ()>("SelectionWriteDone", &(&session, serial, ok));
            }
        });
    }
    text
}

impl Way for Portal {
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
            self.call(
                "NotifyPointerButton",
                &(&self.session, Self::none(), n as i32, b.down as u32),
            );
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
                self.call(
                    "NotifyPointerAxisDiscrete",
                    &(&self.session, Self::none(), 0u32, down),
                );
            }
            if right != 0 {
                self.call(
                    "NotifyPointerAxisDiscrete",
                    &(&self.session, Self::none(), 1u32, right),
                );
            }
        }
        if let Some(k) = &i.key {
            if let Some((mods, sym)) = keys::of_key(k) {
                self.press(&mods, sym);
            }
        }
        if let Some(t) = &i.text {
            match &self.clip {
                Some((p, clip)) if !crate::mutter::plain(&t.text) => {
                    *clip.lock().unwrap() = t.text.clone();
                    let mimes: HashMap<&str, Value> = HashMap::from([(
                        "mime_types",
                        Value::from(vec![
                            "text/plain;charset=utf-8",
                            "text/plain",
                            "UTF8_STRING",
                        ]),
                    )]);
                    if p.call::<_, _, ()>("SetSelection", &(&self.session, mimes))
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
            self.call(
                "NotifyPointerButton",
                &(&self.session, Self::none(), b as i32, 0u32),
            );
        }
    }

    fn can_control(&self) -> bool {
        self.devices & 3 != 0
    }
}
