//! Assigns sequence numbers and delivers events in that order.

use her_llm::now_ms;
use her_protocol::Event;
use her_protocol::EventMsg;
use parking_lot::Mutex;
use tokio::sync::mpsc;

/// Events travel boxed: the channel keeps its blocks of slots for the life of the session, and
/// a slot for a box is 8 bytes instead of the size of the largest event.
pub(crate) struct Emitter {
    inner: Mutex<(u64, mpsc::UnboundedSender<Box<Event>>)>,
}

impl Emitter {
    pub(crate) fn new(tx: mpsc::UnboundedSender<Box<Event>>) -> Self {
        Self {
            inner: Mutex::new((0, tx)),
        }
    }

    /// Sequence assignment and delivery happen under one lock, so `seq` order is delivery order.
    pub(crate) fn emit(&self, msg: EventMsg) {
        let mut inner = self.inner.lock();
        inner.0 += 1;
        let event = Box::new(Event {
            seq: inner.0,
            ts: now_ms(),
            msg,
        });
        let _ = inner.1.send(event);
    }
}
