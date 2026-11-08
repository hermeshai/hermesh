//! What the frame reader asks of the allocator, whatever a client sends. A test binary of its own,
//! because the allocator that counts is global.

use bytes::Bytes;
use her_rpc::Frame;
use her_rpc::FrameReader;
use std::alloc::GlobalAlloc;
use std::alloc::Layout;
use std::alloc::System;
use std::collections::VecDeque;
use std::io;
use std::pin::Pin;
use std::sync::Arc;
use std::sync::atomic::AtomicUsize;
use std::sync::atomic::Ordering::Relaxed;
use std::task::Context;
use std::task::Poll;
use tokio::io::AsyncRead;
use tokio::io::ReadBuf;

const MIB: usize = 1 << 20;
/// The service's default limit (`server.max_frame_bytes`).
const DEFAULT_LIMIT: usize = 128 * MIB;
/// Past this size a `realloc` is what fills macOS's allocator with dirty blocks that it keeps.
const LARGE: usize = 4 * MIB;

struct Counting;

static LIVE: AtomicUsize = AtomicUsize::new(0);
static PEAK: AtomicUsize = AtomicUsize::new(0);
/// Every byte asked for, whether freed since or not.
static ASKED: AtomicUsize = AtomicUsize::new(0);
static LARGE_REALLOCS: AtomicUsize = AtomicUsize::new(0);

fn counted(size: usize) {
    ASKED.fetch_add(size, Relaxed);
    let live = LIVE.fetch_add(size, Relaxed) + size;
    PEAK.fetch_max(live, Relaxed);
}

// SAFETY: passes everything on to the system's allocator, and only counts.
unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        counted(layout.size());
        // SAFETY: the caller's guarantees are the same.
        unsafe { System.alloc(layout) }
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        LIVE.fetch_sub(layout.size(), Relaxed);
        // SAFETY: the caller's guarantees are the same.
        unsafe { System.dealloc(ptr, layout) }
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        if new_size > LARGE {
            LARGE_REALLOCS.fetch_add(1, Relaxed);
        }
        LIVE.fetch_sub(layout.size(), Relaxed);
        counted(new_size);
        // SAFETY: the caller's guarantees are the same.
        unsafe { System.realloc(ptr, layout, new_size) }
    }
}

#[global_allocator]
static ALLOCATOR: Counting = Counting;

/// Measures from now on: what is live now counts as the floor.
fn start_counting() -> usize {
    let live = LIVE.load(Relaxed);
    PEAK.store(live, Relaxed);
    ASKED.store(0, Relaxed);
    LARGE_REALLOCS.store(0, Relaxed);
    live
}

enum Part {
    /// This many `x`s, without a newline.
    Filler(usize),
    Text(&'static [u8]),
}

/// Input that costs nothing to prepare, however long it is.
struct Input {
    parts: VecDeque<Part>,
    read: Arc<AtomicUsize>,
}

static CHUNK: [u8; 64 * 1024] = [b'x'; 64 * 1024];

impl Input {
    fn new(parts: impl IntoIterator<Item = Part>) -> (Self, Arc<AtomicUsize>) {
        let read = Arc::new(AtomicUsize::new(0));
        (
            Self {
                parts: parts.into_iter().collect(),
                read: read.clone(),
            },
            read,
        )
    }
}

impl AsyncRead for Input {
    fn poll_read(mut self: Pin<&mut Self>, _cx: &mut Context<'_>, buf: &mut ReadBuf<'_>) -> Poll<io::Result<()>> {
        let before = buf.filled().len();
        match self.parts.front_mut() {
            None => {}
            Some(Part::Filler(left)) => {
                let n = (*left).min(CHUNK.len()).min(buf.remaining());
                buf.put_slice(&CHUNK[..n]);
                *left -= n;
                if *left == 0 {
                    self.parts.pop_front();
                }
            }
            Some(Part::Text(text)) => {
                let n = text.len().min(buf.remaining());
                buf.put_slice(&text[..n]);
                *text = &text[n..];
                if text.is_empty() {
                    self.parts.pop_front();
                }
            }
        }
        self.read.fetch_add(buf.filled().len() - before, Relaxed);
        Poll::Ready(Ok(()))
    }
}

fn is_line(frame: &Option<Frame>, length: usize) -> bool {
    matches!(frame, Some(Frame::Line(line)) if line.len() == length)
}

fn is_oversize(frame: &Option<Frame>) -> bool {
    matches!(frame, Some(Frame::Oversize { .. }))
}

/// `her acp < /dev/zero`: the line never ends, so it must be reported without waiting for that,
/// and none of it may be held once it has been.
async fn a_line_without_end() {
    let limit = DEFAULT_LIMIT;
    let (input, read) = Input::new([Part::Filler(4 * limit), Part::Text(b"\n{\"ok\":1}\n")]);
    let mut reader = FrameReader::new(input, limit);
    let floor = start_counting();

    let frame = reader.next().await.unwrap();
    assert!(is_oversize(&frame), "{frame:?}");
    assert!(read.load(Relaxed) <= limit + CHUNK.len(), "reported after {} bytes", read.load(Relaxed));
    // One block for the line, and nothing of it left. Two would mean it moved at the last moment.
    assert!(PEAK.load(Relaxed) - floor <= limit + 2 * LARGE, "peak {} over {limit}", PEAK.load(Relaxed) - floor);
    drop(frame);
    assert!(LIVE.load(Relaxed) - floor <= MIB, "still holding {}", LIVE.load(Relaxed) - floor);

    // The rest of the line, three times the limit, costs next to nothing.
    start_counting();
    let frame = reader.next().await.unwrap();
    assert_eq!(frame, Some(Frame::Line(Bytes::from_static(b"{\"ok\":1}"))));
    assert_eq!(read.load(Relaxed), 4 * limit + 10);
    assert!(ASKED.load(Relaxed) <= MIB, "asked for {} while dropping", ASKED.load(Relaxed));
    drop(frame);
    assert_eq!(reader.next().await.unwrap(), None);
    assert_eq!(LARGE_REALLOCS.load(Relaxed), 0);
}

/// Prompts with large images, one after the other. With the next line already arriving, the block
/// stays for it: moving house for every line costs page faults for every line, on Linux anyway.
async fn long_lines_in_a_row() {
    let length = 9 * MIB;
    let mut parts = vec![Part::Filler(length)];
    for _ in 1..20 {
        parts.push(Part::Text(b"\nyyyyyyyy"));
        parts.push(Part::Filler(length - 8));
    }
    parts.push(Part::Text(b"\n"));
    let (input, _) = Input::new(parts);
    let mut reader = FrameReader::new(input, DEFAULT_LIMIT);
    let floor = start_counting();
    for i in 0..20 {
        let frame = reader.next().await.unwrap();
        assert!(is_line(&frame, length), "line {i}: {frame:?}");
    }
    assert_eq!(reader.next().await.unwrap(), None);
    assert_eq!(LARGE_REALLOCS.load(Relaxed), 0, "large lines must not grow by realloc");
    assert!(PEAK.load(Relaxed) - floor <= DEFAULT_LIMIT + 2 * LARGE, "peak {}", PEAK.load(Relaxed) - floor);
    assert!(ASKED.load(Relaxed) <= DEFAULT_LIMIT + 3 * LARGE, "asked for {} in all", ASKED.load(Relaxed));
    assert!(LIVE.load(Relaxed) - floor <= MIB, "still holding {}", LIVE.load(Relaxed) - floor);
}

/// A client that keeps sending lines over the limit, each with its end.
async fn oversized_lines_in_a_row() {
    let limit = 8 * MIB;
    let mut parts = Vec::new();
    for _ in 0..5 {
        parts.push(Part::Filler(20 * MIB));
        parts.push(Part::Text(b"\n"));
    }
    let (input, _) = Input::new(parts);
    let mut reader = FrameReader::new(input, limit);
    let floor = start_counting();
    for i in 0..5 {
        let frame = reader.next().await.unwrap();
        assert!(is_oversize(&frame), "line {i}: {frame:?}");
    }
    assert_eq!(reader.next().await.unwrap(), None);
    assert_eq!(LARGE_REALLOCS.load(Relaxed), 0);
    assert!(PEAK.load(Relaxed) - floor <= limit + 2 * LARGE, "peak {}", PEAK.load(Relaxed) - floor);
    assert!(LIVE.load(Relaxed) - floor <= MIB, "still holding {}", LIVE.load(Relaxed) - floor);
}

/// The longest line allowed goes through, in one block; one byte more does not.
async fn lines_at_the_limit() {
    for limit in [8 * MIB, DEFAULT_LIMIT] {
        let (input, _) = Input::new([
            Part::Filler(limit),
            Part::Text(b"\n"),
            Part::Filler(limit + 1),
            Part::Text(b"\n{\"ok\":1}\n"),
        ]);
        let mut reader = FrameReader::new(input, limit);
        let floor = start_counting();
        let frame = reader.next().await.unwrap();
        assert!(is_line(&frame, limit), "{frame:?}");
        assert!(PEAK.load(Relaxed) - floor <= limit + 2 * LARGE, "peak {} over {limit}", PEAK.load(Relaxed) - floor);
        drop(frame);
        let frame = reader.next().await.unwrap();
        assert!(is_oversize(&frame), "{frame:?}");
        assert_eq!(reader.next().await.unwrap(), Some(Frame::Line(Bytes::from_static(b"{\"ok\":1}"))));
        assert_eq!(reader.next().await.unwrap(), None);
    }
}

#[test]
fn the_reader_holds_little_whatever_comes() {
    // One test, one thread: the counters would mix otherwise.
    let runtime = tokio::runtime::Builder::new_current_thread().build().unwrap();
    runtime.block_on(async {
        a_line_without_end().await;
        long_lines_in_a_row().await;
        oversized_lines_in_a_row().await;
        lines_at_the_limit().await;
    });
}
