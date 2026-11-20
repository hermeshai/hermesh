//! hermesh-screen [--test-source] [--input-log <file>] [--test-clipboard <file>]
//!     | --status | --allow
//!
//! Started by hermeshd for one Computer Control session
//! (docs/computer-control-linux.md). It finds the desktop, captures its
//! primary display, encodes it and writes the frames to stdout; it reads the
//! viewer's input from stdin and posts it. When stdin closes it lets go of
//! what it holds and exits.

mod clip;
mod detect;
mod keys;
mod mutter;
mod notify;
mod pipe;
mod pipewire;
mod portal;
mod video;
mod wlr;
mod x11;

use std::io::Write;
use std::sync::{Arc, Mutex, OnceLock};

use pipe::{Input, Out};
use video::{Frame, Order, Video};

/// A way of reaching the desktop: it captures on threads of its own and
/// takes the viewer's input here.
pub trait Way: Send {
    fn input(&mut self, i: &Input);
    fn show_pointer(&mut self, shown: bool);
    fn release_all(&mut self);
    fn can_control(&self) -> bool;
}

static OUT: OnceLock<Arc<Out>> = OnceLock::new();

pub fn out() -> &'static Arc<Out> {
    OUT.get_or_init(|| Arc::new(Out::new()))
}

/// fail tells hermeshd why there is no picture, and ends.
pub fn fail(error: &str, detail: &str) -> ! {
    out().state(error, detail, false);
    std::process::exit(1)
}

fn main() {
    let mut test_source = false;
    let mut input_log: Option<String> = None;
    let mut test_clipboard: Option<String> = None;
    let mut args = std::env::args().skip(1);
    while let Some(a) = args.next() {
        match a.as_str() {
            "--status" => {
                print_status();
                return;
            }
            "--allow" => match portal::allow() {
                Ok(()) => return,
                Err(e) => {
                    eprintln!("hermesh-screen: {e}");
                    std::process::exit(1);
                }
            },
            "--version" => {
                println!("hermesh-screen {}", env!("CARGO_PKG_VERSION"));
                return;
            }
            "--test-source" => test_source = true,
            "--input-log" => input_log = args.next(),
            "--test-clipboard" => test_clipboard = args.next(),
            _ => {
                eprintln!("hermesh-screen: unknown argument {a}");
                std::process::exit(2);
            }
        }
    }
    unsafe { libc::signal(libc::SIGPIPE, libc::SIG_IGN) };
    let log = input_log.map(|p| Arc::new(Mutex::new(std::fs::File::create(p).expect("input log"))));
    let way: Arc<Mutex<Option<Box<dyn Way>>>> = Arc::new(Mutex::new(None));
    let mut video: Option<Arc<Video>> = None;
    pipe::read_messages(|kind, payload| match kind {
        pipe::INPUT => {
            if let Some(log) = &log {
                let mut f = log.lock().unwrap();
                let _ = f.write_all(payload);
                let _ = f.write_all(b"\n");
            }
            let Ok(i) = serde_json::from_slice::<Input>(payload) else {
                return;
            };
            // In tests the clipboard is a file, and nothing is pasted.
            if let (Some(f), Some(c)) = (&test_clipboard, &i.clipboard) {
                clip::put(&c.text);
                let _ = std::fs::write(f, &c.text);
                return;
            }
            if log.is_some() {
                return;
            }
            if let Some(w) = way.lock().unwrap().as_mut() {
                w.input(&i);
            }
        }
        pipe::START => {
            if video.is_some() {
                return;
            }
            let m: pipe::Start = serde_json::from_slice(payload).unwrap_or_default();
            let v = Video::start(
                out().clone(),
                m.max_edge.unwrap_or(1920),
                m.fps.unwrap_or(video::MAX_FPS),
                m.bitrate.unwrap_or(3_000_000),
            );
            video = Some(v.clone());
            let pointer = m.pointer.unwrap_or(true);
            if let Some(f) = &test_clipboard {
                clip::watch_file(f.into());
            }
            if test_source {
                out().state("", "", true);
                clip::started();
                std::thread::spawn(move || test_picture(v, m.fps.unwrap_or(30).min(60)));
                return;
            }
            let slot = way.clone();
            // Connecting may wait (a dialog at the computer): input meanwhile goes nowhere.
            std::thread::spawn(move || {
                let (w, kind) = start(v, pointer);
                let can = w.can_control();
                *slot.lock().unwrap() = Some(w);
                out().state("", "", can);
                clip::started();
                notify::session_started(kind);
            });
        }
        pipe::BITRATE => {
            if let (Some(v), Ok(m)) = (&video, serde_json::from_slice::<pipe::Bitrate>(payload)) {
                v.set_bitrate(m.bps);
            }
        }
        pipe::KEYFRAME => {
            if let Some(v) = &video {
                v.keyframe();
            }
        }
        pipe::POINTER => {
            if let Ok(m) = serde_json::from_slice::<pipe::Pointer>(payload) {
                if let Some(w) = way.lock().unwrap().as_mut() {
                    w.show_pointer(m.shown);
                }
            }
        }
        _ => {}
    });
    // hermeshd closed the pipe, or is gone.
    if let Some(w) = way.lock().unwrap().as_mut() {
        w.release_all();
    }
    std::process::exit(0);
}

/// start finds the desktop and opens the way to it, or tells hermeshd why
/// it can't and ends.
fn start(video: Arc<Video>, pointer: bool) -> (Box<dyn Way>, detect::Kind) {
    let kind = match detect::find() {
        Ok(k) => k,
        Err((error, detail)) => fail(error, &detail),
    };
    let way: Box<dyn Way> = match kind {
        detect::Kind::X11 => match x11::X11::start(video, pointer) {
            Ok(w) => Box::new(w),
            Err(e) => fail("capture", &e),
        },
        detect::Kind::Wlroots => match wlr::Wlr::start(video, pointer) {
            Ok(w) => Box::new(w),
            Err(e) => fail("capture", &e),
        },
        detect::Kind::Mutter => match mutter::Mutter::start(video.clone(), pointer) {
            Ok(w) => Box::new(w),
            // Refused (a GNOME that keeps its service to itself): the portal.
            Err(e) => {
                out().log(&format!(
                    "GNOME's remote desktop service: {e}; trying the portal"
                ));
                match portal::Portal::start(video, pointer) {
                    Ok(w) => Box::new(w),
                    Err((error, detail)) => fail(error, &detail),
                }
            }
        },
        detect::Kind::Portal => match portal::Portal::start(video, pointer) {
            Ok(w) => Box::new(w),
            Err((error, detail)) => fail(error, &detail),
        },
    };
    (way, kind)
}

/// print_status says, for hermeshd, whether this desktop can be shown and
/// which way, or why not. A locked GNOME is there all the same: the phone
/// keeps its row and is told why it can't connect now.
fn print_status() {
    let (available, way, error, detail) = match detect::find() {
        Ok(k) => (true, k.name(), "", String::new()),
        Err(("locked", d)) => (true, detect::Kind::Mutter.name(), "locked", d),
        Err((e, d)) => (false, "", e, d),
    };
    let v =
        serde_json::json!({ "available": available, "way": way, "error": error, "detail": detail });
    println!("{v}");
}

/// test_picture stands in for the screen in tests: a pattern that moves
/// with every frame, encoded like the real picture.
fn test_picture(video: Arc<Video>, fps: u32) {
    let (w, h) = (640u32, 400u32);
    let mut n = 0u32;
    loop {
        let mut pixels = vec![0u8; (w * h * 4) as usize];
        let bar = (n * 8) % w;
        for y in 0..h {
            for x in 0..w {
                let on = (x as i32 - bar as i32).abs() < 24;
                let v = if on {
                    235
                } else {
                    (40 + x * 120 / w + y * 40 / h) as u8
                };
                let i = ((y * w + x) * 4) as usize;
                pixels[i..i + 3].copy_from_slice(&[v, v, v]);
            }
        }
        let now = pipe::now_us();
        video.push(Frame {
            pixels,
            width: w,
            height: h,
            order: Order::Bgra,
            displayed: now,
            captured: now,
        });
        n += 1;
        std::thread::sleep(std::time::Duration::from_secs(1) / fps.max(1));
    }
}
