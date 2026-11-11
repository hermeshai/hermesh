//! X11: the picture from shared memory when DAMAGE says the screen
//! changed, the pointer drawn in from XFIXES, input through XTEST. No
//! permission, no dialog; a locked screen shows and takes input.

use std::os::fd::AsRawFd;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;

use x11rb::connection::{Connection, RequestConnection};
use x11rb::protocol::damage::{self, ConnectionExt as _};
use x11rb::protocol::randr::{self, ConnectionExt as _};
use x11rb::protocol::shm::{self, ConnectionExt as _};
use x11rb::protocol::xfixes::{self, ConnectionExt as _};
use x11rb::protocol::xproto::{self, ConnectionExt as _, ImageFormat, Window};
use x11rb::protocol::xtest::{self, ConnectionExt as _};
use x11rb::protocol::Event;
use x11rb::rust_connection::RustConnection;

use crate::keys;
use crate::pipe::{self, Input};
use crate::video::{Frame, Order, Video};
use crate::Way;

/// The part of the root window that is the primary display.
#[derive(Clone, Copy, Debug)]
struct Rect {
    x: i16,
    y: i16,
    w: u16,
    h: u16,
}

fn primary(conn: &RustConnection, root: Window, screen: &xproto::Screen) -> Rect {
    let whole = Rect {
        x: 0,
        y: 0,
        w: screen.width_in_pixels,
        h: screen.height_in_pixels,
    };
    let found = (|| -> Option<Rect> {
        conn.extension_information(randr::X11_EXTENSION_NAME)
            .ok()??;
        let res = conn
            .randr_get_screen_resources_current(root)
            .ok()?
            .reply()
            .ok()?;
        let prim = conn
            .randr_get_output_primary(root)
            .ok()?
            .reply()
            .ok()?
            .output;
        let mut outputs = vec![prim];
        outputs.extend(res.outputs.iter().copied().filter(|o| *o != prim));
        for o in outputs {
            let info = conn
                .randr_get_output_info(o, res.config_timestamp)
                .ok()?
                .reply()
                .ok();
            let Some(info) = info else { continue };
            if info.crtc == 0 {
                continue;
            }
            let c = conn
                .randr_get_crtc_info(info.crtc, res.config_timestamp)
                .ok()?
                .reply()
                .ok()?;
            if c.width > 0 && c.height > 0 {
                return Some(Rect {
                    x: c.x,
                    y: c.y,
                    w: c.width,
                    h: c.height,
                });
            }
        }
        None
    })();
    found.unwrap_or(whole)
}

/// probe says whether an X display can be shown: it can be reached and
/// has the extensions this way needs.
pub fn probe() -> Result<(), String> {
    let (conn, _) = RustConnection::connect(None).map_err(|e| e.to_string())?;
    for ext in [damage::X11_EXTENSION_NAME, xtest::X11_EXTENSION_NAME] {
        if conn.extension_information(ext).ok().flatten().is_none() {
            return Err(format!("the X server has no {ext}"));
        }
    }
    Ok(())
}

pub struct X11 {
    input: X11Input,
    pointer: Arc<AtomicBool>,
}

impl X11 {
    pub fn start(video: Arc<Video>, pointer: bool) -> Result<X11, String> {
        let shown = Arc::new(AtomicBool::new(pointer));
        let (conn, n) = RustConnection::connect(None).map_err(|e| e.to_string())?;
        let screen = conn.setup().roots[n].clone();
        let rect = primary(&conn, screen.root, &screen);
        let input = X11Input::new(rect)?;
        let s = shown.clone();
        std::thread::Builder::new()
            .name("x11".into())
            .spawn(move || {
                if let Err(e) = capture(conn, screen, rect, video, s) {
                    eprintln!("hermesh-screen: x11: {e}");
                    crate::fail("capture", &e);
                }
            })
            .unwrap();
        Ok(X11 {
            input,
            pointer: shown,
        })
    }
}

impl Way for X11 {
    fn input(&mut self, i: &Input) {
        self.input.handle(i);
    }
    fn show_pointer(&mut self, shown: bool) {
        self.pointer.store(shown, Ordering::Relaxed);
    }
    fn release_all(&mut self) {
        self.input.release_all();
    }
    fn can_control(&self) -> bool {
        true
    }
}

/// Shared memory the X server writes the picture into.
struct Shm {
    seg: shm::Seg,
    addr: *mut u8,
    size: usize,
}

impl Shm {
    fn new(conn: &RustConnection, size: usize) -> Option<Shm> {
        conn.extension_information(shm::X11_EXTENSION_NAME).ok()??;
        unsafe {
            let id = libc::shmget(libc::IPC_PRIVATE, size, libc::IPC_CREAT | 0o600);
            if id < 0 {
                return None;
            }
            let addr = libc::shmat(id, std::ptr::null(), 0);
            // Removed now, it lasts until both ends let go of it.
            let seg = conn.generate_id().ok();
            let ok = seg.and_then(|seg| conn.shm_attach(seg, id as u32, false).ok()?.check().ok());
            libc::shmctl(id, libc::IPC_RMID, std::ptr::null_mut());
            if addr as isize == -1 || ok.is_none() {
                return None;
            }
            Some(Shm {
                seg: seg.unwrap(),
                addr: addr as *mut u8,
                size,
            })
        }
    }
}

fn capture(
    conn: RustConnection,
    screen: xproto::Screen,
    rect: Rect,
    video: Arc<Video>,
    shown: Arc<AtomicBool>,
) -> Result<(), String> {
    let root = screen.root;
    let size = rect.w as usize * rect.h as usize * 4;
    let shm = Shm::new(&conn, size);
    conn.damage_query_version(1, 1)
        .map_err(|e| e.to_string())?
        .reply()
        .map_err(|e| e.to_string())?;
    let dmg = conn.generate_id().map_err(|e| e.to_string())?;
    conn.damage_create(dmg, root, damage::ReportLevel::NON_EMPTY)
        .map_err(|e| e.to_string())?;
    let fixes = conn
        .xfixes_query_version(4, 0)
        .ok()
        .and_then(|c| c.reply().ok())
        .is_some();
    if fixes {
        let _ = conn.xfixes_select_cursor_input(root, xfixes::CursorNotifyMask::DISPLAY_CURSOR);
    }
    conn.flush().map_err(|e| e.to_string())?;
    let mut cursor: Option<xfixes::GetCursorImageReply> = None;
    let mut cursor_stale = true;
    let mut dirty = true;
    let mut last_pointer = (i16::MIN, i16::MIN);
    let mut drawn = shown.load(Ordering::Relaxed);
    let fd = conn.stream().as_raw_fd();
    loop {
        while let Some(ev) = conn.poll_for_event().map_err(|e| e.to_string())? {
            match ev {
                Event::DamageNotify(_) => dirty = true,
                Event::XfixesCursorNotify(_) => cursor_stale = true,
                _ => {}
            }
        }
        let show = shown.load(Ordering::Relaxed);
        if show != drawn {
            drawn = show;
            dirty = true;
        }
        let mut pointer = last_pointer;
        if show && fixes {
            if let Ok(p) = conn.query_pointer(root).map_err(|e| e.to_string())?.reply() {
                pointer = (p.root_x, p.root_y);
            }
            if pointer != last_pointer {
                dirty = true;
            }
        }
        if dirty {
            dirty = false;
            last_pointer = pointer;
            conn.damage_subtract(dmg, x11rb::NONE, x11rb::NONE)
                .map_err(|e| e.to_string())?;
            let captured = pipe::now_us();
            let mut frame = match &shm {
                Some(s) => {
                    conn.shm_get_image(
                        root,
                        rect.x,
                        rect.y,
                        rect.w,
                        rect.h,
                        !0,
                        ImageFormat::Z_PIXMAP.into(),
                        s.seg,
                        0,
                    )
                    .map_err(|e| e.to_string())?
                    .reply()
                    .map_err(|e| e.to_string())?;
                    let pixels = unsafe { std::slice::from_raw_parts(s.addr, s.size) };
                    Frame::from_rows(
                        pixels,
                        rect.w as u32,
                        rect.h as u32,
                        rect.w as usize * 4,
                        Order::Bgra,
                        false,
                    )
                }
                None => {
                    let img = conn
                        .get_image(
                            ImageFormat::Z_PIXMAP,
                            root,
                            rect.x,
                            rect.y,
                            rect.w,
                            rect.h,
                            !0,
                        )
                        .map_err(|e| e.to_string())?
                        .reply()
                        .map_err(|e| e.to_string())?;
                    Frame::from_rows(
                        &img.data,
                        rect.w as u32,
                        rect.h as u32,
                        rect.w as usize * 4,
                        Order::Bgra,
                        false,
                    )
                }
            }
            .ok_or("the X server gave a picture of another size")?;
            frame.captured = captured;
            frame.displayed = captured;
            if show && fixes {
                if cursor_stale || cursor.is_none() {
                    cursor = conn
                        .xfixes_get_cursor_image()
                        .ok()
                        .and_then(|c| c.reply().ok());
                    cursor_stale = false;
                }
                if let Some(c) = &cursor {
                    draw_cursor(&mut frame, c, pointer.0 - rect.x, pointer.1 - rect.y);
                }
            }
            video.push(frame);
        }
        // Until the X server says something, or the pointer may have moved.
        let mut pfd = libc::pollfd {
            fd,
            events: libc::POLLIN,
            revents: 0,
        };
        let wait = if show { 8 } else { 250 };
        unsafe { libc::poll(&mut pfd, 1, wait) };
        if pfd.revents & (libc::POLLHUP | libc::POLLERR) != 0 {
            return Err("the X server closed the connection".into());
        }
    }
}

/// draw_cursor puts XFIXES's pointer (premultiplied ARGB) over the picture,
/// its hot spot at (x, y).
fn draw_cursor(f: &mut Frame, c: &xfixes::GetCursorImageReply, x: i16, y: i16) {
    let (fw, fh) = (f.width as i32, f.height as i32);
    let (ox, oy) = (x as i32 - c.xhot as i32, y as i32 - c.yhot as i32);
    for cy in 0..c.height as i32 {
        let py = oy + cy;
        if py < 0 || py >= fh {
            continue;
        }
        for cx in 0..c.width as i32 {
            let px = ox + cx;
            if px < 0 || px >= fw {
                continue;
            }
            let argb = c.cursor_image[(cy * c.width as i32 + cx) as usize];
            let a = argb >> 24;
            if a == 0 {
                continue;
            }
            let i = ((py * fw + px) * 4) as usize;
            let p = &mut f.pixels[i..i + 4];
            for (k, shift) in [(0, 0), (1, 8), (2, 16)] {
                let s = (argb >> shift) & 0xff;
                p[k] = (s + p[k] as u32 * (255 - a) / 255).min(255) as u8;
            }
        }
    }
}

/// X11Input posts the viewer's input with XTEST on a connection of its own.
struct X11Input {
    conn: RustConnection,
    root: Window,
    rect: Rect,
    min: u8,
    per: usize,
    syms: Vec<u32>,
    /// Keycodes nothing uses, given a character to type for a moment.
    spare: Vec<u8>,
    spare_used: bool,
    held: Vec<u8>,
    last: (i16, i16),
    wheel: (f64, f64),
}

impl X11Input {
    fn new(rect: Rect) -> Result<X11Input, String> {
        let (conn, n) = RustConnection::connect(None).map_err(|e| e.to_string())?;
        conn.xtest_get_version(2, 2)
            .map_err(|e| e.to_string())?
            .reply()
            .map_err(|e| e.to_string())?;
        let root = conn.setup().roots[n].root;
        let mut i = X11Input {
            conn,
            root,
            rect,
            min: 0,
            per: 0,
            syms: vec![],
            spare: vec![],
            spare_used: false,
            held: vec![],
            last: (-1, -1),
            wheel: (0.0, 0.0),
        };
        i.read_map()?;
        Ok(i)
    }

    fn read_map(&mut self) -> Result<(), String> {
        let setup = self.conn.setup();
        let (min, max) = (setup.min_keycode, setup.max_keycode);
        let m = self
            .conn
            .get_keyboard_mapping(min, max - min + 1)
            .map_err(|e| e.to_string())?
            .reply()
            .map_err(|e| e.to_string())?;
        self.min = min;
        self.per = m.keysyms_per_keycode as usize;
        self.syms = m.keysyms;
        if self.spare.is_empty() {
            let count = self.syms.len() / self.per.max(1);
            self.spare = (0..count)
                .rev()
                .filter(|i| {
                    self.syms[i * self.per..(i + 1) * self.per]
                        .iter()
                        .all(|s| *s == 0)
                })
                .take(8)
                .map(|i| min + i as u8)
                .collect();
        }
        Ok(())
    }

    /// keycode finds the key that types sym, and whether it needs Shift.
    fn keycode(&self, sym: u32) -> Option<(u8, bool)> {
        if self.per == 0 {
            return None;
        }
        for (i, syms) in self.syms.chunks(self.per).enumerate() {
            let kc = self.min + i as u8;
            if self.spare.contains(&kc) {
                continue;
            }
            if syms[0] == sym {
                return Some((kc, false));
            }
            if self.per > 1 && syms[1] == sym {
                return Some((kc, true));
            }
        }
        None
    }

    fn point(&self, x: f64, y: f64) -> (i16, i16) {
        let r = self.rect;
        let fx = x.clamp(0.0, 1.0);
        let fy = y.clamp(0.0, 1.0);
        (
            (r.x as f64 + fx * (r.w as f64 - 1.0)).round() as i16,
            (r.y as f64 + fy * (r.h as f64 - 1.0)).round() as i16,
        )
    }

    fn fake(&self, kind: u8, detail: u8, x: i16, y: i16) {
        let _ = self
            .conn
            .xtest_fake_input(kind, detail, x11rb::CURRENT_TIME, self.root, x, y, 0);
    }

    fn move_to(&mut self, p: (i16, i16)) {
        if p != self.last {
            self.fake(xproto::MOTION_NOTIFY_EVENT, 0, p.0, p.1);
            self.last = p;
        }
    }

    fn click(&self, button: u8) {
        self.fake(xproto::BUTTON_PRESS_EVENT, button, 0, 0);
        self.fake(xproto::BUTTON_RELEASE_EVENT, button, 0, 0);
    }

    fn key(&self, kc: u8, down: bool) {
        self.fake(
            if down {
                xproto::KEY_PRESS_EVENT
            } else {
                xproto::KEY_RELEASE_EVENT
            },
            kc,
            0,
            0,
        );
    }

    fn sync(&self) {
        let _ = self.conn.get_input_focus().map(|c| c.reply());
    }

    /// press types sym with the modifiers held, all let go after.
    fn press(&mut self, mods: &[u32], sym: u32) {
        let mut downs: Vec<u8> = mods
            .iter()
            .filter_map(|m| self.keycode(*m).map(|k| k.0))
            .collect();
        let (kc, shift) = match self.keycode(sym) {
            Some(k) => k,
            None => {
                self.type_spare(&[sym], &downs);
                return;
            }
        };
        if shift && !mods.contains(&keys::SHIFT_L) {
            if let Some((s, _)) = self.keycode(keys::SHIFT_L) {
                downs.push(s);
            }
        }
        for k in &downs {
            self.key(*k, true);
        }
        self.key(kc, true);
        self.key(kc, false);
        for k in downs.iter().rev() {
            self.key(*k, false);
        }
    }

    /// type_spare types characters no key has: each given to a keycode
    /// nothing uses (both levels, or a capital comes out small), a few at a
    /// time, with the mapping settled before the keys are pressed.
    fn type_spare(&mut self, syms: &[u32], mods: &[u8]) {
        if self.spare.is_empty() {
            return;
        }
        self.spare_used = true;
        for batch in syms.chunks(self.spare.len()) {
            for (sym, kc) in batch.iter().zip(&self.spare) {
                let _ = self.conn.change_keyboard_mapping(1, *kc, 2, &[*sym, *sym]);
            }
            self.sync();
            std::thread::sleep(Duration::from_millis(10));
            for kc in self.spare.iter().take(batch.len()) {
                for m in mods {
                    self.key(*m, true);
                }
                self.key(*kc, true);
                self.key(*kc, false);
                for m in mods.iter().rev() {
                    self.key(*m, false);
                }
            }
            self.sync();
            std::thread::sleep(Duration::from_millis(10));
        }
    }

    fn text(&mut self, text: &str) {
        let mut pending: Vec<u32> = Vec::new();
        for c in text.chars() {
            let sym = keys::of_char(c);
            if self.keycode(sym).is_some() {
                if !pending.is_empty() {
                    self.type_spare(&std::mem::take(&mut pending), &[]);
                }
                self.press(&[], sym);
            } else {
                pending.push(sym);
            }
        }
        if !pending.is_empty() {
            self.type_spare(&pending, &[]);
        }
    }

    fn handle(&mut self, i: &Input) {
        if let Some(m) = &i.mov {
            let p = self.point(m.x, m.y);
            self.move_to(p);
        }
        if let Some(b) = &i.button {
            let p = self.point(b.x, b.y);
            self.move_to(p);
            let n = if b.button == "MOUSE_BUTTON_RIGHT" {
                3
            } else {
                1
            };
            self.fake(
                if b.down {
                    xproto::BUTTON_PRESS_EVENT
                } else {
                    xproto::BUTTON_RELEASE_EVENT
                },
                n,
                0,
                0,
            );
            if b.down {
                self.held.push(n);
            } else {
                self.held.retain(|h| *h != n);
            }
        }
        if let Some(w) = &i.wheel {
            let p = self.point(w.x, w.y);
            self.move_to(p);
            let down = keys::steps(&mut self.wheel.1, w.dy);
            let right = keys::steps(&mut self.wheel.0, w.dx);
            for _ in 0..down.unsigned_abs() {
                self.click(if down > 0 { 5 } else { 4 });
            }
            for _ in 0..right.unsigned_abs() {
                self.click(if right > 0 { 7 } else { 6 });
            }
        }
        if let Some(k) = &i.key {
            if let Some((mods, sym)) = keys::of_key(k) {
                self.press(&mods, sym);
            }
        }
        if let Some(t) = &i.text {
            self.text(&t.text);
        }
        let _ = self.conn.flush();
    }

    fn release_all(&mut self) {
        for b in std::mem::take(&mut self.held) {
            self.fake(xproto::BUTTON_RELEASE_EVENT, b, 0, 0);
        }
        if self.spare_used {
            for kc in self.spare.clone() {
                let _ = self.conn.change_keyboard_mapping(1, kc, 2, &[0, 0]);
            }
        }
        self.sync();
    }
}
