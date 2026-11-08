//! Connections: a reader task that parses and dispatches, a writer task that owns the
//! protocol state, and a send queue between the hubs and the writer.

use bytes::Bytes;
use her_protocol::api::SessionDetachedParams;
use her_rpc::Frame;
use her_rpc::FrameReader;
use her_rpc::RpcError;
use parking_lot::Mutex;
use std::any::Any;
use std::collections::VecDeque;
use std::io;
use std::io::IoSlice;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::Ordering;
use tokio::io::AsyncRead;
use tokio::io::AsyncWrite;
use tokio::io::AsyncWriteExt;
use tokio::sync::Notify;

use crate::frame::EventFrame;
use crate::frame::MergedRun;
use crate::frame::coalesce;
use crate::frame::mergeable;
use crate::server::Server;

pub type ConnId = u64;

/// Most items one writer pass takes from the queue.
const BATCH_ITEMS: usize = 128;
const BATCH_BYTES: usize = 256 * 1024;
/// A connection is detached after its backlog stays above the limit this long, or at once
/// when the backlog passes four times the limit.
const OVERFLOW_GRACE: std::time::Duration = std::time::Duration::from_secs(1);

pub enum Outgoing {
    /// A session event.
    Event(Arc<EventFrame>),
    /// An encoded response, notification or request.
    Frame(Bytes),
    /// The connection no longer receives this session's events.
    Detached(SessionDetachedParams),
    /// Protocol-specific instruction for the writer, kept in queue order.
    Control(Box<dyn Any + Send>),
}

impl Outgoing {
    fn size(&self) -> usize {
        match self {
            Outgoing::Event(frame) => frame.size_hint(),
            Outgoing::Frame(bytes) => bytes.len(),
            Outgoing::Detached(_) | Outgoing::Control(_) => 64,
        }
    }
}

enum Item {
    Out(Outgoing),
    /// Deltas merged while waiting; becomes one event when taken.
    Merged(MergedRun),
}

struct QueueState {
    items: VecDeque<Item>,
    /// Backlog as charged (see `OutQueue::charge`).
    bytes: usize,
    closed: bool,
    /// When the backlog last went above the limit.
    over_since: Option<std::time::Instant>,
}

/// Unbounded for responses and control items; events stop at the byte limit. While the
/// writer is behind, a delta that continues the last queued delta is appended to it, so a
/// slow reader costs memory for the text only and receives fewer, larger events.
pub struct OutQueue {
    state: Mutex<QueueState>,
    notify: Notify,
    hard_bytes: usize,
    coalesce: Arc<AtomicBool>,
}

/// Why an event was not queued.
#[derive(Debug, PartialEq, Eq)]
pub enum PushError {
    Overflow,
    Closed,
}

impl OutQueue {
    pub fn new(hard_bytes: usize, coalesce: Arc<AtomicBool>) -> Self {
        Self {
            state: Mutex::new(QueueState {
                items: VecDeque::new(),
                bytes: 0,
                closed: false,
                over_since: None,
            }),
            notify: Notify::new(),
            hard_bytes,
            coalesce,
        }
    }

    /// The backlog an item counts for: one large event (a long message, say) is not a
    /// reason to detach a reader that keeps up, so no single item counts for more than a
    /// quarter of the limit. Merged text always counts in full.
    fn charge(&self, size: usize) -> usize {
        size.min(self.hard_bytes / 4)
    }

    /// Queues anything; dropped only when the connection is closed.
    pub fn push(&self, item: Outgoing) {
        {
            let mut state = self.state.lock();
            if state.closed {
                return;
            }
            state.bytes += self.charge(item.size());
            state.items.push_back(Item::Out(item));
        }
        self.notify.notify_one();
    }

    pub fn push_all(&self, items: Vec<Outgoing>) {
        if items.is_empty() {
            return;
        }
        {
            let mut state = self.state.lock();
            if state.closed {
                return;
            }
            for item in items {
                state.bytes += self.charge(item.size());
                state.items.push_back(Item::Out(item));
            }
        }
        self.notify.notify_one();
    }

    /// Queues an event, unless the backlog has stayed above the limit for too long.
    pub fn push_event(&self, frame: Arc<EventFrame>) -> Result<(), PushError> {
        {
            let mut state = self.state.lock();
            if state.closed {
                return Err(PushError::Closed);
            }
            if state.bytes > self.hard_bytes {
                let since = *state.over_since.get_or_insert_with(std::time::Instant::now);
                if state.bytes > self.hard_bytes.saturating_mul(4) || since.elapsed() >= OVERFLOW_GRACE {
                    return Err(PushError::Overflow);
                }
            }
            if self.coalesce.load(Ordering::Relaxed) {
                match state.items.back_mut() {
                    Some(Item::Merged(run)) if run.accepts(&frame) => {
                        run.push(&frame);
                        state.bytes += MergedRun::added(&frame);
                        return Ok(());
                    }
                    Some(back @ Item::Out(Outgoing::Event(_))) => {
                        if let Item::Out(Outgoing::Event(last)) = back
                            && mergeable(last, &frame)
                        {
                            let run = MergedRun::new(last.clone(), &frame);
                            *back = Item::Merged(run);
                            state.bytes += MergedRun::added(&frame);
                            return Ok(());
                        }
                    }
                    _ => {}
                }
            }
            state.bytes += self.charge(frame.size_hint());
            state.items.push_back(Item::Out(Outgoing::Event(frame)));
        }
        self.notify.notify_one();
        Ok(())
    }

    pub fn close(&self) {
        self.state.lock().closed = true;
        self.notify.notify_one();
    }

    pub fn is_closed(&self) -> bool {
        self.state.lock().closed
    }

    pub fn pending_bytes(&self) -> usize {
        self.state.lock().bytes
    }

    /// Waits for items and takes up to one batch. `None` once closed and drained.
    async fn take(&self) -> Option<Vec<Outgoing>> {
        loop {
            {
                let mut state = self.state.lock();
                if !state.items.is_empty() {
                    let mut batch = Vec::with_capacity(state.items.len().min(BATCH_ITEMS));
                    let mut bytes = 0;
                    while !state.items.is_empty() {
                        if batch.len() >= BATCH_ITEMS || (bytes >= BATCH_BYTES && !batch.is_empty()) {
                            break;
                        }
                        let item = match state.items.pop_front().expect("not empty") {
                            Item::Out(item) => item,
                            Item::Merged(run) => Outgoing::Event(run.into_frame()),
                        };
                        bytes += self.charge(item.size());
                        batch.push(item);
                    }
                    // Merged runs are charged by text; the estimate settles at zero when empty.
                    state.bytes = if state.items.is_empty() { 0 } else { state.bytes.saturating_sub(bytes) };
                    if state.bytes <= self.hard_bytes {
                        state.over_since = None;
                    }
                    return Some(batch);
                }
                if state.closed {
                    return None;
                }
            }
            self.notify.notified().await;
        }
    }
}

/// What hubs and handlers hold to reach a connection.
#[derive(Clone)]
pub struct ConnHandle {
    pub id: ConnId,
    pub queue: Arc<OutQueue>,
    /// Whether adjacent deltas may be merged (the client's `coalesce` capability).
    pub coalesce: Arc<AtomicBool>,
    /// The process on the other end of a Unix socket, when the system says.
    pub peer_pid: Option<u32>,
}

impl ConnHandle {
    pub fn send(&self, bytes: Vec<u8>) {
        self.queue.push(Outgoing::Frame(Bytes::from(bytes)));
    }

    pub fn send_error(&self, id: Option<&her_rpc::Id>, error: &RpcError) {
        self.send(her_rpc::error_response(id, error));
    }
}

/// One protocol spoken on a connection.
pub trait Protocol: Send + Sync + 'static {
    /// Handles one incoming line on the reader task. Long work must be spawned.
    fn on_line(self: Arc<Self>, line: Bytes, conn: &ConnHandle);

    /// The encoder for this connection's writer task.
    fn encoder(&self, conn: &ConnHandle) -> Box<dyn Encoder>;

    /// The client went away (end of input).
    fn on_closed(&self, _conn: &ConnHandle) {}
}

/// Runs on the writer task and owns the protocol state of the connection.
pub trait Encoder: Send {
    fn encode(&mut self, item: Outgoing, out: &mut Vec<Bytes>);
}

/// Serves one connection until the client closes its side, then lets the writer drain.
pub async fn run_connection<R, W>(server: Server, reader: R, writer: W, protocol: Arc<dyn Protocol>)
where
    R: AsyncRead + Unpin + Send + 'static,
    W: AsyncWrite + Unpin + Send + 'static,
{
    run_peer_connection(server, reader, writer, protocol, None).await;
}

/// [`run_connection`] for a peer whose process is known.
pub async fn run_peer_connection<R, W>(server: Server, reader: R, writer: W, protocol: Arc<dyn Protocol>, peer_pid: Option<u32>)
where
    R: AsyncRead + Unpin + Send + 'static,
    W: AsyncWrite + Unpin + Send + 'static,
{
    let mut conn = server.new_connection();
    conn.peer_pid = peer_pid;
    let encoder = protocol.encoder(&conn);
    let writer_task = tokio::spawn(write_loop(conn.clone(), encoder, writer));
    let mut frames = FrameReader::new(reader, server.settings().max_frame_bytes);
    loop {
        match frames.next().await {
            Ok(Some(Frame::Line(line))) => protocol.clone().on_line(line, &conn),
            Ok(Some(Frame::Oversize { head })) => {
                let limit = server.settings().max_frame_bytes;
                tracing::warn!(conn = conn.id, limit, "rejected a message over the size limit");
                let error = RpcError::invalid_request(format!("the message is over the limit of {limit} bytes (server.max_frame_bytes)"));
                conn.send_error(her_rpc::peek_request_id(&head).as_ref(), &error);
            }
            Ok(None) => break,
            Err(err) => {
                tracing::debug!(conn = conn.id, %err, "connection read failed");
                break;
            }
        }
    }
    protocol.on_closed(&conn);
    server.connection_closed(&conn);
    conn.queue.close();
    let _ = writer_task.await;
}

async fn write_loop<W: AsyncWrite + Unpin>(conn: ConnHandle, mut encoder: Box<dyn Encoder>, mut writer: W) {
    // Grows to the largest batch seen; an idle connection keeps nothing.
    let mut out: Vec<Bytes> = Vec::new();
    while let Some(batch) = conn.queue.take().await {
        let batch = if conn.coalesce.load(Ordering::Relaxed) {
            coalesce_items(batch)
        } else {
            batch
        };
        for item in batch {
            encoder.encode(item, &mut out);
        }
        if out.is_empty() {
            continue;
        }
        if let Err(err) = write_all(&mut writer, &out).await {
            tracing::debug!(conn = conn.id, %err, "connection write failed");
            conn.queue.close();
            break;
        }
        out.clear();
    }
    let _ = writer.shutdown().await;
}

/// Merges adjacent mergeable events; other items stay in place.
fn coalesce_items(batch: Vec<Outgoing>) -> Vec<Outgoing> {
    let events = batch.iter().filter(|i| matches!(i, Outgoing::Event(_))).count();
    if events < 2 {
        return batch;
    }
    let mut out = Vec::with_capacity(batch.len());
    let mut run: Vec<Arc<EventFrame>> = Vec::new();
    for item in batch {
        match item {
            Outgoing::Event(frame) => run.push(frame),
            other => {
                out.extend(coalesce(std::mem::take(&mut run)).into_iter().map(Outgoing::Event));
                out.push(other);
            }
        }
    }
    out.extend(coalesce(run).into_iter().map(Outgoing::Event));
    out
}

/// Writes all buffers: vectored when the writer supports it, else one copy.
async fn write_all<W: AsyncWrite + Unpin>(writer: &mut W, bufs: &[Bytes]) -> io::Result<()> {
    if bufs.len() == 1 || !writer.is_write_vectored() {
        if bufs.len() == 1 {
            writer.write_all(&bufs[0]).await?;
        } else {
            let mut joined = Vec::with_capacity(bufs.iter().map(Bytes::len).sum());
            for buf in bufs {
                joined.extend_from_slice(buf);
            }
            writer.write_all(&joined).await?;
        }
        return writer.flush().await;
    }
    let mut index = 0;
    let mut offset = 0;
    while index < bufs.len() {
        let slices: Vec<IoSlice<'_>> = std::iter::once(IoSlice::new(&bufs[index][offset..]))
            .chain(bufs[index + 1..].iter().take(63).map(|b| IoSlice::new(b)))
            .collect();
        let mut written = writer.write_vectored(&slices).await?;
        if written == 0 {
            return Err(io::Error::new(io::ErrorKind::WriteZero, "connection closed"));
        }
        while written > 0 && index < bufs.len() {
            let left = bufs[index].len() - offset;
            if written >= left {
                written -= left;
                index += 1;
                offset = 0;
            } else {
                offset += written;
                written = 0;
            }
        }
    }
    writer.flush().await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn vectored_writes_survive_partial_writes() {
        let (mut client, server) = tokio::io::duplex(7);
        let bufs: Vec<Bytes> = (0..50).map(|i| Bytes::from(format!("line {i}\n"))).collect();
        let expected: Vec<u8> = bufs.iter().flat_map(|b| b.to_vec()).collect();
        let writer = tokio::spawn(async move {
            let mut server = server;
            write_all(&mut server, &bufs).await.unwrap();
        });
        let mut got = vec![0u8; expected.len()];
        tokio::io::AsyncReadExt::read_exact(&mut client, &mut got).await.unwrap();
        writer.await.unwrap();
        assert_eq!(got, expected);
    }

    fn delta(session: &Arc<crate::frame::SessionShared>, seq: u64, index: usize, text: &str) -> Arc<EventFrame> {
        Arc::new(EventFrame::new(
            session.clone(),
            her_protocol::Event {
                seq,
                ts: 0,
                msg: her_protocol::EventMsg::MessageDelta {
                    message_id: "m".into(),
                    delta: her_protocol::Delta::Text { index, text: text.into() },
                },
            },
        ))
    }

    fn texts(batch: &[Outgoing]) -> Vec<(u64, String)> {
        batch
            .iter()
            .filter_map(|item| match item {
                Outgoing::Event(frame) => match &frame.event.msg {
                    her_protocol::EventMsg::MessageDelta {
                        delta: her_protocol::Delta::Text { text, .. },
                        ..
                    } => Some((frame.event.seq, text.clone())),
                    _ => None,
                },
                _ => None,
            })
            .collect()
    }

    #[tokio::test]
    async fn queued_deltas_merge_in_place_until_taken() {
        let queue = OutQueue::new(1 << 20, Arc::new(AtomicBool::new(true)));
        let s = crate::frame::SessionShared::new("s");
        for (seq, t) in [(1, "a"), (2, "b"), (3, "c")] {
            queue.push_event(delta(&s, seq, 0, t)).unwrap();
        }
        queue.push(Outgoing::Frame(Bytes::from_static(b"{}\n")));
        queue.push_event(delta(&s, 4, 0, "d")).unwrap();
        queue.push_event(delta(&s, 5, 1, "e")).unwrap();
        let batch = queue.take().await.unwrap();
        assert_eq!(batch.len(), 4, "abc merged; the reply and index 1 break runs");
        assert_eq!(
            texts(&batch),
            vec![(3, "abc".to_string()), (4, "d".to_string()), (5, "e".to_string())]
        );
        // Without the capability, every delta stays separate.
        let plain = OutQueue::new(1 << 20, Arc::new(AtomicBool::new(false)));
        for (seq, t) in [(1, "a"), (2, "b")] {
            plain.push_event(delta(&s, seq, 0, t)).unwrap();
        }
        assert_eq!(plain.take().await.unwrap().len(), 2);
    }

    #[test]
    fn events_stop_at_the_byte_limit_but_replies_do_not() {
        let queue = OutQueue::new(40, Arc::new(AtomicBool::new(true)));
        let session = crate::frame::SessionShared::new("s");
        let frame = Arc::new(EventFrame::new(
            session,
            her_protocol::Event {
                seq: 1,
                ts: 0,
                msg: her_protocol::EventMsg::SessionClosed,
            },
        ));
        // Each event is charged a quarter of the limit; past four times the limit it overflows.
        for _ in 0..17 {
            assert_eq!(queue.push_event(frame.clone()), Ok(()));
        }
        assert_eq!(queue.push_event(frame.clone()), Err(PushError::Overflow));
        queue.push(Outgoing::Frame(Bytes::from_static(b"{}\n")));
        assert!(queue.pending_bytes() > 160);
        queue.close();
        assert_eq!(queue.push_event(frame), Err(PushError::Closed));
    }
}
