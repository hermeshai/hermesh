//! PipeWire, where GNOME and Plasma put the picture. Its library is loaded
//! at run time, so a machine without it runs the same program; the helper
//! takes the frames in memory (it offers no DMA-BUF format, so none comes)
//! and hands them to the encoder.

use std::ffi::{c_char, c_int, c_void, CString};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, OnceLock};

use libloading::Library;

use crate::video::{Frame, Order, Video};

type Ptr = *mut c_void;

struct Api {
    _lib: Library,
    thread_loop_new: unsafe extern "C" fn(*const c_char, Ptr) -> Ptr,
    thread_loop_get_loop: unsafe extern "C" fn(Ptr) -> Ptr,
    thread_loop_start: unsafe extern "C" fn(Ptr) -> c_int,
    thread_loop_lock: unsafe extern "C" fn(Ptr),
    thread_loop_unlock: unsafe extern "C" fn(Ptr),
    context_new: unsafe extern "C" fn(Ptr, Ptr, usize) -> Ptr,
    context_connect: unsafe extern "C" fn(Ptr, Ptr, usize) -> Ptr,
    context_connect_fd: unsafe extern "C" fn(Ptr, c_int, Ptr, usize) -> Ptr,
    properties_new_string: unsafe extern "C" fn(*const c_char) -> Ptr,
    stream_new: unsafe extern "C" fn(Ptr, *const c_char, Ptr) -> Ptr,
    stream_add_listener: unsafe extern "C" fn(Ptr, Ptr, *const StreamEvents, Ptr),
    stream_connect: unsafe extern "C" fn(Ptr, u32, u32, u32, *mut *const c_void, u32) -> c_int,
    stream_dequeue_buffer: unsafe extern "C" fn(Ptr) -> *mut PwBuffer,
    stream_queue_buffer: unsafe extern "C" fn(Ptr, *mut PwBuffer) -> c_int,
    stream_update_params: unsafe extern "C" fn(Ptr, *mut *const c_void, u32) -> c_int,
}

unsafe impl Send for Api {}
unsafe impl Sync for Api {}

fn load() -> Result<Api, String> {
    unsafe {
        let lib = Library::new("libpipewire-0.3.so.0")
            .map_err(|e| format!("PipeWire is not installed, or can't be loaded: {e}"))?;
        macro_rules! sym {
            ($name:literal) => {
                *lib.get(concat!($name, "\0").as_bytes())
                    .map_err(|e| format!("{}: {e}", $name))?
            };
        }
        let init: unsafe extern "C" fn(*mut c_int, *mut *mut *mut c_char) = sym!("pw_init");
        init(std::ptr::null_mut(), std::ptr::null_mut());
        Ok(Api {
            thread_loop_new: sym!("pw_thread_loop_new"),
            thread_loop_get_loop: sym!("pw_thread_loop_get_loop"),
            thread_loop_start: sym!("pw_thread_loop_start"),
            thread_loop_lock: sym!("pw_thread_loop_lock"),
            thread_loop_unlock: sym!("pw_thread_loop_unlock"),
            context_new: sym!("pw_context_new"),
            context_connect: sym!("pw_context_connect"),
            context_connect_fd: sym!("pw_context_connect_fd"),
            properties_new_string: sym!("pw_properties_new_string"),
            stream_new: sym!("pw_stream_new"),
            stream_add_listener: sym!("pw_stream_add_listener"),
            stream_connect: sym!("pw_stream_connect"),
            stream_dequeue_buffer: sym!("pw_stream_dequeue_buffer"),
            stream_queue_buffer: sym!("pw_stream_queue_buffer"),
            stream_update_params: sym!("pw_stream_update_params"),
            _lib: lib,
        })
    }
}

static API: OnceLock<Result<Api, String>> = OnceLock::new();

fn api() -> Result<&'static Api, String> {
    API.get_or_init(load).as_ref().map_err(|e| e.clone())
}

/// The picture's size, as PipeWire last said (width << 32 | height).
static SIZE: AtomicU64 = AtomicU64::new(0);

/// size is the stream's picture: positions in it are what the desktop's
/// input calls take.
pub fn size() -> (u32, u32) {
    let v = SIZE.load(Ordering::Relaxed);
    ((v >> 32) as u32, v as u32)
}

// The layouts of spa/buffer/buffer.h and pipewire/stream.h.
#[repr(C)]
struct SpaChunk {
    offset: u32,
    size: u32,
    stride: i32,
    flags: i32,
}

#[repr(C)]
struct SpaData {
    kind: u32,
    flags: u32,
    fd: i64,
    mapoffset: u32,
    maxsize: u32,
    data: Ptr,
    chunk: *mut SpaChunk,
}

#[repr(C)]
struct SpaBuffer {
    n_metas: u32,
    n_datas: u32,
    metas: Ptr,
    datas: *mut SpaData,
}

#[repr(C)]
struct PwBuffer {
    buffer: *mut SpaBuffer,
    user_data: Ptr,
    size: u64,
    requested: u64,
}

type Cb = Option<unsafe extern "C" fn()>;

#[repr(C)]
struct StreamEvents {
    version: u32,
    destroy: Cb,
    state_changed: Option<unsafe extern "C" fn(Ptr, c_int, c_int, *const c_char)>,
    control_info: Cb,
    io_changed: Cb,
    param_changed: Option<unsafe extern "C" fn(Ptr, u32, *const c_void)>,
    add_buffer: Cb,
    remove_buffer: Cb,
    process: Option<unsafe extern "C" fn(Ptr)>,
    drained: Cb,
    command: Cb,
    trigger_done: Cb,
}

static EVENTS: StreamEvents = StreamEvents {
    version: 2,
    destroy: None,
    state_changed: Some(on_state),
    control_info: None,
    io_changed: None,
    param_changed: Some(on_param),
    add_buffer: None,
    remove_buffer: None,
    process: Some(on_process),
    drained: None,
    command: None,
    trigger_done: None,
};

// SPA's ids (spa/utils/type.h, spa/param/*.h).
const TYPE_ID: u32 = 3;
const TYPE_INT: u32 = 4;
const TYPE_RECTANGLE: u32 = 10;
const TYPE_FRACTION: u32 = 11;
const TYPE_OBJECT: u32 = 15;
const TYPE_CHOICE: u32 = 19;
const OBJECT_FORMAT: u32 = 0x40003;
const OBJECT_PARAM_BUFFERS: u32 = 0x40004;
const PARAM_ENUM_FORMAT: u32 = 3;
const PARAM_FORMAT: u32 = 4;
const PARAM_BUFFERS: u32 = 5;
const FORMAT_MEDIA_TYPE: u32 = 1;
const FORMAT_MEDIA_SUBTYPE: u32 = 2;
const FORMAT_VIDEO_FORMAT: u32 = 0x20001;
const FORMAT_VIDEO_SIZE: u32 = 0x20003;
const FORMAT_VIDEO_FRAMERATE: u32 = 0x20004;
const FORMAT_VIDEO_MAX_FRAMERATE: u32 = 0x20005;
const PARAM_BUFFERS_DATA_TYPE: u32 = 6;
const MEDIA_TYPE_VIDEO: u32 = 2;
const MEDIA_SUBTYPE_RAW: u32 = 1;
const VIDEO_RGBX: u32 = 7;
const VIDEO_BGRX: u32 = 8;
const VIDEO_RGBA: u32 = 11;
const VIDEO_BGRA: u32 = 12;
const CHOICE_RANGE: u32 = 1;
const CHOICE_ENUM: u32 = 3;
const CHOICE_FLAGS: u32 = 4;
const DATA_MEMPTR: u32 = 1;
const DATA_MEMFD: u32 = 2;
const STREAM_FLAG_AUTOCONNECT: u32 = 1 << 0;
const STREAM_FLAG_MAP_BUFFERS: u32 = 1 << 2;
const STATE_ERROR: c_int = -1;
const STATE_UNCONNECTED: c_int = 0;
const STATE_STREAMING: c_int = 3;

/// Pods: SPA's self-describing values, each a size and a type, its body,
/// padded to 8 bytes.
mod pod {
    pub fn value(kind: u32, body: &[u8]) -> Vec<u8> {
        let mut v = Vec::with_capacity(body.len() + 16);
        v.extend_from_slice(&(body.len() as u32).to_ne_bytes());
        v.extend_from_slice(&kind.to_ne_bytes());
        v.extend_from_slice(body);
        while v.len() % 8 != 0 {
            v.push(0);
        }
        v
    }

    pub fn u32s(vals: &[u32]) -> Vec<u8> {
        vals.iter().flat_map(|v| v.to_ne_bytes()).collect()
    }

    /// choice of the given kind over values of one type, each size bytes.
    pub fn choice(kind: u32, child: u32, size: u32, values: &[u8]) -> Vec<u8> {
        let mut body = u32s(&[kind, 0, size, child]);
        body.extend_from_slice(values);
        value(super::TYPE_CHOICE, &body)
    }

    pub fn object(kind: u32, id: u32, props: &[(u32, Vec<u8>)]) -> Vec<u8> {
        let mut body = u32s(&[kind, id]);
        for (key, v) in props {
            body.extend_from_slice(&u32s(&[*key, 0]));
            body.extend_from_slice(v);
        }
        value(super::TYPE_OBJECT, &body)
    }

    fn u32_at(b: &[u8], i: usize) -> Option<u32> {
        Some(u32::from_ne_bytes(b.get(i..i + 4)?.try_into().ok()?))
    }

    /// props reads an object's properties: (key, type, body), a choice
    /// reduced to its first value.
    pub fn props(b: &[u8]) -> Vec<(u32, u32, &[u8])> {
        let mut out = Vec::new();
        let (Some(size), Some(kind)) = (u32_at(b, 0), u32_at(b, 4)) else {
            return out;
        };
        if kind != super::TYPE_OBJECT || b.len() < 8 + size as usize {
            return out;
        }
        let end = 8 + size as usize;
        let mut i = 16;
        while i + 16 <= end {
            let (Some(key), Some(vs), Some(vt)) =
                (u32_at(b, i), u32_at(b, i + 8), u32_at(b, i + 12))
            else {
                break;
            };
            let body = i + 16;
            let Some(mut val) = b.get(body..body + vs as usize) else {
                break;
            };
            let mut t = vt;
            if vt == super::TYPE_CHOICE && val.len() >= 16 {
                let csize = u32_at(val, 8).unwrap_or(0) as usize;
                t = u32_at(val, 12).unwrap_or(0);
                val = val.get(16..16 + csize).unwrap_or(&[]);
            }
            out.push((key, t, val));
            i = body + (vs as usize).div_ceil(8) * 8;
        }
        out
    }
}

fn enum_format() -> Vec<u8> {
    let formats = pod::u32s(&[VIDEO_BGRX, VIDEO_BGRX, VIDEO_BGRA, VIDEO_RGBX, VIDEO_RGBA]);
    pod::object(
        OBJECT_FORMAT,
        PARAM_ENUM_FORMAT,
        &[
            (
                FORMAT_MEDIA_TYPE,
                pod::value(TYPE_ID, &pod::u32s(&[MEDIA_TYPE_VIDEO])),
            ),
            (
                FORMAT_MEDIA_SUBTYPE,
                pod::value(TYPE_ID, &pod::u32s(&[MEDIA_SUBTYPE_RAW])),
            ),
            (
                FORMAT_VIDEO_FORMAT,
                pod::choice(CHOICE_ENUM, TYPE_ID, 4, &formats),
            ),
            (
                FORMAT_VIDEO_SIZE,
                pod::choice(
                    CHOICE_RANGE,
                    TYPE_RECTANGLE,
                    8,
                    &pod::u32s(&[1920, 1080, 1, 1, 8192, 8192]),
                ),
            ),
            (
                FORMAT_VIDEO_FRAMERATE,
                pod::value(TYPE_FRACTION, &pod::u32s(&[0, 1])),
            ),
            (
                FORMAT_VIDEO_MAX_FRAMERATE,
                pod::choice(
                    CHOICE_RANGE,
                    TYPE_FRACTION,
                    8,
                    &pod::u32s(&[60, 1, 1, 1, 60, 1]),
                ),
            ),
        ],
    )
}

fn buffers() -> Vec<u8> {
    let types = (1 << DATA_MEMPTR) | (1 << DATA_MEMFD);
    pod::object(
        OBJECT_PARAM_BUFFERS,
        PARAM_BUFFERS,
        &[(
            PARAM_BUFFERS_DATA_TYPE,
            pod::choice(CHOICE_FLAGS, TYPE_INT, 4, &pod::u32s(&[types])),
        )],
    )
}

/// What a stream's callbacks reach.
struct Shared {
    api: &'static Api,
    stream: Ptr,
    video: Arc<Video>,
    format: Option<(u32, u32, Order)>,
    streamed: bool,
}

unsafe extern "C" fn on_state(data: Ptr, _old: c_int, state: c_int, error: *const c_char) {
    let s = &mut *(data as *mut Shared);
    match state {
        STATE_ERROR => {
            let why = if error.is_null() {
                "PipeWire failed".to_string()
            } else {
                std::ffi::CStr::from_ptr(error)
                    .to_string_lossy()
                    .into_owned()
            };
            crate::fail("capture", &why);
        }
        STATE_STREAMING => s.streamed = true,
        STATE_UNCONNECTED if s.streamed => {
            crate::fail("capture", "the desktop stopped sharing its screen")
        }
        _ => {}
    }
}

unsafe extern "C" fn on_param(data: Ptr, id: u32, param: *const c_void) {
    let s = &mut *(data as *mut Shared);
    if id != PARAM_FORMAT || param.is_null() {
        return;
    }
    let size = *(param as *const u32) as usize + 8;
    let b = std::slice::from_raw_parts(param as *const u8, size);
    let (mut w, mut h, mut order) = (0, 0, None);
    for (key, t, v) in pod::props(b) {
        match (key, t) {
            (FORMAT_VIDEO_FORMAT, TYPE_ID) if v.len() >= 4 => {
                order = match u32::from_ne_bytes(v[..4].try_into().unwrap()) {
                    VIDEO_BGRX | VIDEO_BGRA => Some(Order::Bgra),
                    VIDEO_RGBX | VIDEO_RGBA => Some(Order::Rgba),
                    _ => None,
                }
            }
            (FORMAT_VIDEO_SIZE, TYPE_RECTANGLE) if v.len() >= 8 => {
                w = u32::from_ne_bytes(v[..4].try_into().unwrap());
                h = u32::from_ne_bytes(v[4..8].try_into().unwrap());
            }
            _ => {}
        }
    }
    let Some(order) = order else {
        crate::fail(
            "capture",
            "the desktop offers its picture in no format the helper reads",
        );
    };
    s.format = Some((w, h, order));
    SIZE.store((w as u64) << 32 | h as u64, Ordering::Relaxed);
    let b = buffers();
    let mut params = [b.as_ptr() as *const c_void];
    (s.api.stream_update_params)(s.stream, params.as_mut_ptr(), 1);
}

unsafe extern "C" fn on_process(data: Ptr) {
    let s = &mut *(data as *mut Shared);
    // The newest buffer only: older ones go back at once.
    let mut newest: *mut PwBuffer = std::ptr::null_mut();
    loop {
        let b = (s.api.stream_dequeue_buffer)(s.stream);
        if b.is_null() {
            break;
        }
        if !newest.is_null() {
            (s.api.stream_queue_buffer)(s.stream, newest);
        }
        newest = b;
    }
    if newest.is_null() {
        return;
    }
    let frame = (|| {
        let (w, h, order) = s.format?;
        let buf = &*(*newest).buffer;
        if buf.n_datas < 1 {
            return None;
        }
        let d = &*buf.datas;
        if d.data.is_null() || d.chunk.is_null() {
            return None;
        }
        let c = &*d.chunk;
        if c.size == 0 || c.flags & 1 != 0 {
            return None; // empty, or corrupted
        }
        let stride = if c.stride > 0 {
            c.stride as usize
        } else {
            w as usize * 4
        };
        let offset = if d.maxsize > 0 {
            c.offset % d.maxsize
        } else {
            0
        } as usize;
        let len = (c.size as usize).min(d.maxsize as usize - offset);
        let bytes = std::slice::from_raw_parts((d.data as *const u8).add(offset), len);
        Frame::from_rows(bytes, w, h, stride, order, false)
    })();
    (s.api.stream_queue_buffer)(s.stream, newest);
    if let Some(f) = frame {
        s.video.push(f);
    }
}

/// connect reads the node's frames into video until the helper ends: from
/// the user's PipeWire, or from the one the portal opened for it (fd).
pub fn connect(fd: Option<c_int>, node: u32, video: Arc<Video>) -> Result<(), String> {
    let api = api()?;
    unsafe {
        let name = CString::new("hermesh-screen").unwrap();
        let tl = (api.thread_loop_new)(name.as_ptr(), std::ptr::null_mut());
        if tl.is_null() {
            return Err("PipeWire: no loop".into());
        }
        (api.thread_loop_lock)(tl);
        let unlock = || (api.thread_loop_unlock)(tl);
        let ctx = (api.context_new)((api.thread_loop_get_loop)(tl), std::ptr::null_mut(), 0);
        if ctx.is_null() {
            unlock();
            return Err("PipeWire: no context".into());
        }
        let core = match fd {
            Some(fd) => (api.context_connect_fd)(ctx, fd, std::ptr::null_mut(), 0),
            None => (api.context_connect)(ctx, std::ptr::null_mut(), 0),
        };
        if core.is_null() {
            unlock();
            return Err(format!(
                "PipeWire: can't connect ({})",
                std::io::Error::last_os_error()
            ));
        }
        let props =
            CString::new("media.type=Video media.category=Capture media.role=Screen").unwrap();
        let stream = (api.stream_new)(
            core,
            name.as_ptr(),
            (api.properties_new_string)(props.as_ptr()),
        );
        if stream.is_null() {
            unlock();
            return Err("PipeWire: no stream".into());
        }
        // One stream for the helper's life: what the callbacks reach stays.
        let shared = Box::leak(Box::new(Shared {
            api,
            stream,
            video,
            format: None,
            streamed: false,
        }));
        let hook = Box::leak(Box::new([0usize; 8]));
        (api.stream_add_listener)(
            stream,
            hook.as_mut_ptr().cast(),
            &EVENTS,
            (shared as *mut Shared).cast(),
        );
        let f = enum_format();
        let mut params = [f.as_ptr() as *const c_void];
        let r = (api.stream_connect)(
            stream,
            0,
            node,
            STREAM_FLAG_AUTOCONNECT | STREAM_FLAG_MAP_BUFFERS,
            params.as_mut_ptr(),
            1,
        );
        if r < 0 {
            unlock();
            return Err(format!("PipeWire: can't connect to node {node} ({r})"));
        }
        if (api.thread_loop_start)(tl) < 0 {
            unlock();
            return Err("PipeWire: the loop did not start".into());
        }
        unlock();
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn format_pod_reads_back() {
        let f = enum_format();
        assert_eq!(f.len() % 8, 0);
        let props = pod::props(&f);
        assert_eq!(props.len(), 6);
        assert_eq!(
            props[0],
            (
                FORMAT_MEDIA_TYPE,
                TYPE_ID,
                &MEDIA_TYPE_VIDEO.to_ne_bytes()[..]
            )
        );
        // A choice reads as its first value.
        assert_eq!(props[2].0, FORMAT_VIDEO_FORMAT);
        assert_eq!(props[2].1, TYPE_ID);
        assert_eq!(props[2].2, &VIDEO_BGRX.to_ne_bytes()[..]);
        assert_eq!(
            (props[3].0, props[3].1),
            (FORMAT_VIDEO_SIZE, TYPE_RECTANGLE)
        );
        assert_eq!(props[3].2, &pod::u32s(&[1920, 1080])[..]);
        let b = buffers();
        assert_eq!(
            pod::props(&b),
            vec![(PARAM_BUFFERS_DATA_TYPE, TYPE_INT, &pod::u32s(&[6])[..])]
        );
    }
}
