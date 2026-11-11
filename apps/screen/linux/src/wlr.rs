//! wlroots compositors (Sway, Hyprland, niri, river, labwc): the picture
//! through wlr-screencopy, a frame each time the screen changes; input
//! through a virtual pointer and a virtual keyboard whose keymap the helper
//! writes, so any character can be typed. They ask nothing.

use std::io::Write;
use std::os::fd::{AsFd, FromRawFd, OwnedFd};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Instant;

use wayland_client::globals::{registry_queue_init, GlobalListContents};
use wayland_client::protocol::{wl_buffer, wl_output, wl_registry, wl_seat, wl_shm, wl_shm_pool};
use wayland_client::{delegate_noop, Connection, Dispatch, Proxy, QueueHandle, WEnum};
use wayland_protocols_misc::zwp_virtual_keyboard_v1::client::{
    zwp_virtual_keyboard_manager_v1, zwp_virtual_keyboard_v1,
};
use wayland_protocols_wlr::screencopy::v1::client::{
    zwlr_screencopy_frame_v1, zwlr_screencopy_manager_v1,
};
use wayland_protocols_wlr::virtual_pointer::v1::client::{
    zwlr_virtual_pointer_manager_v1, zwlr_virtual_pointer_v1,
};

use crate::keys;
use crate::pipe::Input;
use crate::video::{Frame, Order, Video};
use crate::Way;

/// available says whether the compositor offers the protocols this way needs.
pub fn available() -> bool {
    let Ok(conn) = Connection::connect_to_env() else {
        return false;
    };
    let Ok((globals, _q)) = registry_queue_init::<Probe>(&conn) else {
        return false;
    };
    let names: Vec<String> = globals
        .contents()
        .clone_list()
        .into_iter()
        .map(|g| g.interface)
        .collect();
    [
        "zwlr_screencopy_manager_v1",
        "zwlr_virtual_pointer_manager_v1",
        "zwp_virtual_keyboard_manager_v1",
        "wl_shm",
    ]
    .iter()
    .all(|n| names.iter().any(|g| g == n))
}

struct Probe;
impl Dispatch<wl_registry::WlRegistry, GlobalListContents> for Probe {
    fn event(
        _: &mut Self,
        _: &wl_registry::WlRegistry,
        _: wl_registry::Event,
        _: &GlobalListContents,
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
    }
}

fn memfd(size: usize) -> Result<OwnedFd, String> {
    unsafe {
        let fd = libc::memfd_create(c"hermesh-screen".as_ptr(), libc::MFD_CLOEXEC);
        if fd < 0 {
            return Err(std::io::Error::last_os_error().to_string());
        }
        if libc::ftruncate(fd, size as libc::off_t) < 0 {
            libc::close(fd);
            return Err(std::io::Error::last_os_error().to_string());
        }
        Ok(OwnedFd::from_raw_fd(fd))
    }
}

// The capture's side.

#[derive(Default)]
struct Capture {
    format: Option<(wl_shm::Format, u32, u32, u32)>,
    buffer_done: bool,
    flipped: bool,
    ready: bool,
    failed: bool,
}

impl Dispatch<wl_registry::WlRegistry, GlobalListContents> for Capture {
    fn event(
        _: &mut Self,
        _: &wl_registry::WlRegistry,
        _: wl_registry::Event,
        _: &GlobalListContents,
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
    }
}
delegate_noop!(Capture: ignore wl_output::WlOutput);
delegate_noop!(Capture: ignore wl_shm::WlShm);
delegate_noop!(Capture: ignore wl_shm_pool::WlShmPool);
delegate_noop!(Capture: ignore wl_buffer::WlBuffer);
delegate_noop!(Capture: zwlr_screencopy_manager_v1::ZwlrScreencopyManagerV1);

impl Dispatch<zwlr_screencopy_frame_v1::ZwlrScreencopyFrameV1, ()> for Capture {
    fn event(
        s: &mut Self,
        _: &zwlr_screencopy_frame_v1::ZwlrScreencopyFrameV1,
        ev: zwlr_screencopy_frame_v1::Event,
        _: &(),
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
        use zwlr_screencopy_frame_v1::Event;
        match ev {
            Event::Buffer {
                format: WEnum::Value(f),
                width,
                height,
                stride,
            } => {
                if s.format.is_none()
                    || matches!(f, wl_shm::Format::Xrgb8888 | wl_shm::Format::Argb8888)
                {
                    s.format = Some((f, width, height, stride));
                }
            }
            Event::BufferDone => s.buffer_done = true,
            Event::Flags {
                flags: WEnum::Value(f),
            } => s.flipped = f.contains(zwlr_screencopy_frame_v1::Flags::YInvert),
            Event::Ready { .. } => s.ready = true,
            Event::Failed => s.failed = true,
            _ => {}
        }
    }
}

struct Shm {
    fd: OwnedFd,
    size: usize,
    map: *mut u8,
    buffer: wl_buffer::WlBuffer,
    key: (u32, u32, u32, u32),
}

fn capture(video: Arc<Video>, shown: Arc<AtomicBool>) -> Result<(), String> {
    let conn = Connection::connect_to_env().map_err(|e| e.to_string())?;
    let (globals, mut queue) = registry_queue_init::<Capture>(&conn).map_err(|e| e.to_string())?;
    let qh = queue.handle();
    let shm: wl_shm::WlShm = globals.bind(&qh, 1..=1, ()).map_err(|e| e.to_string())?;
    let output: wl_output::WlOutput = globals.bind(&qh, 1..=4, ()).map_err(|e| e.to_string())?;
    let manager: zwlr_screencopy_manager_v1::ZwlrScreencopyManagerV1 =
        globals.bind(&qh, 1..=3, ()).map_err(|e| e.to_string())?;
    let version = manager.version();
    let mut buf: Option<Shm> = None;
    let mut first = true;
    loop {
        let mut state = Capture::default();
        let frame = manager.capture_output(shown.load(Ordering::Relaxed) as i32, &output, &qh, ());
        while state.format.is_none() || (version >= 3 && !state.buffer_done) {
            if state.failed {
                return Err("the compositor would not copy its screen".into());
            }
            queue
                .blocking_dispatch(&mut state)
                .map_err(|e| e.to_string())?;
        }
        let (format, w, h, stride) = state.format.unwrap();
        let order = match format {
            wl_shm::Format::Xrgb8888 | wl_shm::Format::Argb8888 => Order::Bgra,
            wl_shm::Format::Xbgr8888 | wl_shm::Format::Abgr8888 => Order::Rgba,
            other => return Err(format!("the compositor copies its screen as {other:?}")),
        };
        let key = (w, h, stride, format as u32);
        if buf.as_ref().map(|b| b.key) != Some(key) {
            if let Some(b) = buf.take() {
                b.buffer.destroy();
                unsafe { libc::munmap(b.map.cast(), b.size) };
            }
            let size = (stride * h) as usize;
            let fd = memfd(size)?;
            let map = unsafe {
                libc::mmap(
                    std::ptr::null_mut(),
                    size,
                    libc::PROT_READ | libc::PROT_WRITE,
                    libc::MAP_SHARED,
                    std::os::fd::AsRawFd::as_raw_fd(&fd),
                    0,
                )
            };
            if map == libc::MAP_FAILED {
                return Err("mmap failed".into());
            }
            let pool = shm.create_pool(fd.as_fd(), size as i32, &qh, ());
            let buffer = pool.create_buffer(0, w as i32, h as i32, stride as i32, format, &qh, ());
            pool.destroy();
            buf = Some(Shm {
                fd,
                size,
                map: map.cast(),
                buffer,
                key,
            });
            first = true;
        }
        let b = buf.as_ref().unwrap();
        let _ = &b.fd;
        // After the first, the compositor answers once something changed.
        if first || version < 2 {
            frame.copy(&b.buffer);
        } else {
            frame.copy_with_damage(&b.buffer);
        }
        first = false;
        while !state.ready && !state.failed {
            queue
                .blocking_dispatch(&mut state)
                .map_err(|e| e.to_string())?;
        }
        frame.destroy();
        if state.failed {
            first = true;
            continue;
        }
        let pixels = unsafe { std::slice::from_raw_parts(b.map, b.size) };
        if let Some(f) = Frame::from_rows(pixels, w, h, stride as usize, order, state.flipped) {
            video.push(f);
        }
        if version < 2 {
            std::thread::sleep(std::time::Duration::from_millis(16));
        }
    }
}

// The input's side.

struct Inputs;
impl Dispatch<wl_registry::WlRegistry, GlobalListContents> for Inputs {
    fn event(
        _: &mut Self,
        _: &wl_registry::WlRegistry,
        _: wl_registry::Event,
        _: &GlobalListContents,
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
    }
}
delegate_noop!(Inputs: ignore wl_seat::WlSeat);
delegate_noop!(Inputs: ignore wl_output::WlOutput);
delegate_noop!(Inputs: zwlr_virtual_pointer_manager_v1::ZwlrVirtualPointerManagerV1);
delegate_noop!(Inputs: zwlr_virtual_pointer_v1::ZwlrVirtualPointerV1);
delegate_noop!(Inputs: zwp_virtual_keyboard_manager_v1::ZwpVirtualKeyboardManagerV1);
delegate_noop!(Inputs: zwp_virtual_keyboard_v1::ZwpVirtualKeyboardV1);

const EXTENT: u32 = 1 << 16;
const BASE: [u32; 14] = [
    0xff0d, 0xff09, 0x20, 0xff08, 0xff1b, 0xff51, 0xff52, 0xff53, 0xff54, 0xffff, 0xff50, 0xff57,
    0xff55, 0xff56,
];
/// The keymap's keycodes run 9 to 255: 247 keysyms at most.
const SLOTS: usize = 247;

/// keymap writes an xkb keymap with each keysym on a key of its own: key
/// i (evdev) is keycode i + 8 and types syms[i - 1].
pub fn keymap(syms: &[u32]) -> String {
    let mut s =
        String::from("xkb_keymap {\nxkb_keycodes \"hermesh\" {\nminimum = 8;\nmaximum = 255;\n");
    for i in 0..syms.len() {
        s += &format!("<K{i}> = {};\n", i + 9);
    }
    s += "};\nxkb_types \"hermesh\" { include \"complete\" };\nxkb_compatibility \"hermesh\" { include \"complete\" };\nxkb_symbols \"hermesh\" {\n";
    for (i, sym) in syms.iter().enumerate() {
        s += &format!("key <K{i}> {{ [ {} ] }};\n", keys::xkb_name(*sym));
    }
    s += "};\n};\n";
    s
}

pub struct Wlr {
    conn: Connection,
    pointer: zwlr_virtual_pointer_v1::ZwlrVirtualPointerV1,
    keyboard: zwp_virtual_keyboard_v1::ZwpVirtualKeyboardV1,
    syms: Vec<u32>,
    began: Instant,
    held: Vec<u32>,
    wheel: (f64, f64),
    shown: Arc<AtomicBool>,
    _queue: wayland_client::EventQueue<Inputs>,
}

impl Wlr {
    pub fn start(video: Arc<Video>, pointer: bool) -> Result<Wlr, String> {
        let shown = Arc::new(AtomicBool::new(pointer));
        let s = shown.clone();
        let conn = Connection::connect_to_env().map_err(|e| e.to_string())?;
        let (globals, queue) = registry_queue_init::<Inputs>(&conn).map_err(|e| e.to_string())?;
        let qh = queue.handle();
        let seat: wl_seat::WlSeat = globals.bind(&qh, 1..=1, ()).map_err(|e| e.to_string())?;
        let output: wl_output::WlOutput =
            globals.bind(&qh, 1..=4, ()).map_err(|e| e.to_string())?;
        let pm: zwlr_virtual_pointer_manager_v1::ZwlrVirtualPointerManagerV1 =
            globals.bind(&qh, 1..=2, ()).map_err(|e| e.to_string())?;
        let ptr = if pm.version() >= 2 {
            pm.create_virtual_pointer_with_output(Some(&seat), Some(&output), &qh, ())
        } else {
            pm.create_virtual_pointer(Some(&seat), &qh, ())
        };
        let km: zwp_virtual_keyboard_manager_v1::ZwpVirtualKeyboardManagerV1 =
            globals.bind(&qh, 1..=1, ()).map_err(|e| e.to_string())?;
        let keyboard = km.create_virtual_keyboard(&seat, &qh, ());
        let mut w = Wlr {
            conn,
            pointer: ptr,
            keyboard,
            syms: BASE.to_vec(),
            began: Instant::now(),
            held: vec![],
            wheel: (0.0, 0.0),
            shown,
            _queue: queue,
        };
        w.upload()?;
        w.conn.flush().map_err(|e| e.to_string())?;
        std::thread::Builder::new()
            .name("wlr".into())
            .spawn(move || {
                if let Err(e) = capture(video, s) {
                    crate::fail("capture", &e);
                }
            })
            .unwrap();
        Ok(w)
    }

    fn time(&self) -> u32 {
        self.began.elapsed().as_millis() as u32
    }

    fn upload(&mut self) -> Result<(), String> {
        let text = keymap(&self.syms);
        let fd = memfd(text.len() + 1)?;
        let mut f = std::fs::File::from(fd.try_clone().map_err(|e| e.to_string())?);
        f.write_all(text.as_bytes()).map_err(|e| e.to_string())?;
        f.write_all(&[0]).map_err(|e| e.to_string())?;
        self.keyboard.keymap(1, fd.as_fd(), (text.len() + 1) as u32);
        Ok(())
    }

    /// ensure puts each keysym in the keymap, writing it again if one is new.
    fn ensure(&mut self, syms: &[u32]) {
        let new: Vec<u32> = syms
            .iter()
            .copied()
            .filter(|s| !self.syms.contains(s))
            .collect();
        if new.is_empty() {
            return;
        }
        if self.syms.len() + new.len() > SLOTS {
            self.syms = BASE.to_vec();
        }
        for s in new {
            if !self.syms.contains(&s) && self.syms.len() < SLOTS {
                self.syms.push(s);
            }
        }
        let _ = self.upload();
    }

    fn key(&self, sym: u32, mods: u32) {
        let Some(i) = self.syms.iter().position(|s| *s == sym) else {
            return;
        };
        let t = self.time();
        if mods != 0 {
            self.keyboard.modifiers(mods, 0, 0, 0);
        }
        self.keyboard.key(t, i as u32 + 1, 1);
        self.keyboard.key(t, i as u32 + 1, 0);
        if mods != 0 {
            self.keyboard.modifiers(0, 0, 0, 0);
        }
    }

    fn point(&self, x: f64, y: f64) {
        let fx = (x.clamp(0.0, 1.0) * (EXTENT - 1) as f64) as u32;
        let fy = (y.clamp(0.0, 1.0) * (EXTENT - 1) as f64) as u32;
        self.pointer
            .motion_absolute(self.time(), fx, fy, EXTENT, EXTENT);
        self.pointer.frame();
    }
}

/// The real modifiers' bits: Shift, Control, Mod1 (Alt), Mod4 (Super).
fn mask(mods: &[u32]) -> u32 {
    mods.iter()
        .map(|m| match *m {
            keys::SHIFT_L => 1,
            keys::CONTROL_L => 4,
            keys::ALT_L => 8,
            keys::SUPER_L => 64,
            _ => 0,
        })
        .fold(0, |a, b| a | b)
}

impl Way for Wlr {
    fn input(&mut self, i: &Input) {
        use wayland_client::protocol::wl_pointer::{Axis, AxisSource, ButtonState};
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
            self.pointer.button(
                self.time(),
                n,
                if b.down {
                    ButtonState::Pressed
                } else {
                    ButtonState::Released
                },
            );
            self.pointer.frame();
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
            for (axis, n) in [
                (Axis::VerticalScroll, down),
                (Axis::HorizontalScroll, right),
            ] {
                if n != 0 {
                    self.pointer.axis_source(AxisSource::Wheel);
                    self.pointer
                        .axis_discrete(self.time(), axis, n as f64 * 15.0, n);
                    self.pointer.frame();
                }
            }
        }
        if let Some(k) = &i.key {
            if let Some((mods, sym)) = keys::of_key(k) {
                self.ensure(&[sym]);
                self.key(sym, mask(&mods));
            }
        }
        if let Some(t) = &i.text {
            let syms: Vec<u32> = t.text.chars().map(keys::of_char).collect();
            for chunk in syms.chunks(SLOTS - BASE.len()) {
                self.ensure(chunk);
                for s in chunk {
                    self.key(*s, 0);
                }
            }
        }
        let _ = self.conn.flush();
    }

    fn show_pointer(&mut self, shown: bool) {
        self.shown.store(shown, Ordering::Relaxed);
    }

    fn release_all(&mut self) {
        use wayland_client::protocol::wl_pointer::ButtonState;
        for b in std::mem::take(&mut self.held) {
            self.pointer.button(self.time(), b, ButtonState::Released);
        }
        self.pointer.frame();
        let _ = self.conn.flush();
    }

    fn can_control(&self) -> bool {
        true
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn keymap_names_each_key() {
        let k = super::keymap(&[0xff0d, 0x61, 0x0100_4f60]);
        assert!(k.contains("<K0> = 9;") && k.contains("<K2> = 11;"));
        assert!(k.contains("key <K0> { [ Return ] };"));
        assert!(k.contains("key <K2> { [ U4F60 ] };"));
    }
}
