//! A single writer per session: entries are appended in the order they are recorded.
//!
//! The writer is a task, not a thread. Commands queue on a channel and each batch of them
//! runs on the blocking pool, so an idle session holds no thread.

use std::io;
use tokio::sync::mpsc;
use tokio::sync::oneshot;
use tokio::sync::watch;

use crate::store::entry::Entry;

/// Where serialized lines go (a file, or memory in tests).
pub trait LineSink: Send + 'static {
    fn write_line(&mut self, line: &str) -> io::Result<()>;
    /// Several lines in order; a file writes them with one call.
    fn write_lines(&mut self, lines: &[String]) -> io::Result<()> {
        lines.iter().try_for_each(|line| self.write_line(line))
    }
    /// Makes everything written so far durable.
    fn sync(&mut self) -> io::Result<()>;
    /// A log that does not exist yet. Its first lines are held in memory until the session
    /// does something, so that starting a session costs no file I/O.
    fn is_new(&self) -> bool {
        false
    }
}

enum WriterCmd {
    Append(Box<Entry>),
    Sync(oneshot::Sender<io::Result<()>>),
    /// What was written so far need not be forced to disk when the log closes.
    Settle,
}

#[derive(Clone)]
pub struct SessionWriter {
    tx: mpsc::UnboundedSender<WriterCmd>,
    /// Closed (its sender dropped) when the writer has finished and the sink is gone.
    done: watch::Receiver<()>,
}

/// Resolves once the last `SessionWriter` for a log is dropped and the log is flushed and
/// closed. Until then the file stays locked.
pub struct WriterClosed(watch::Receiver<()>);

impl WriterClosed {
    pub async fn wait(mut self) {
        while self.0.changed().await.is_ok() {}
    }
}

impl SessionWriter {
    /// Starts the writer on the current runtime, or on a thread of its own outside one.
    pub fn spawn(sink: impl LineSink) -> io::Result<Self> {
        let (tx, rx) = mpsc::unbounded_channel::<WriterCmd>();
        let (done_tx, done) = watch::channel(());
        let job = async move {
            run(sink, rx).await;
            drop(done_tx);
        };
        match tokio::runtime::Handle::try_current() {
            Ok(handle) => {
                handle.spawn(job);
            }
            Err(_) => {
                let runtime = tokio::runtime::Builder::new_current_thread().build()?;
                std::thread::Builder::new()
                    .name("her-session-writer".into())
                    .spawn(move || runtime.block_on(job))?;
            }
        }
        Ok(Self { tx, done })
    }

    pub fn closed(&self) -> WriterClosed {
        WriterClosed(self.done.clone())
    }

    /// Queues an entry; never blocks.
    pub fn append(&self, entry: Entry) {
        let _ = self.tx.send(WriterCmd::Append(Box::new(entry)));
    }

    /// Marks everything queued so far as not worth forcing to disk on close, such as the
    /// entries a new session starts with. Later entries are synced as usual.
    pub fn settle(&self) {
        let _ = self.tx.send(WriterCmd::Settle);
    }

    /// Waits until everything queued so far is durable; reports earlier write failures.
    pub async fn sync(&self) -> io::Result<()> {
        let (tx, rx) = oneshot::channel();
        self.tx
            .send(WriterCmd::Sync(tx))
            .map_err(|_| io::Error::other("session writer stopped"))?;
        rx.await.unwrap_or_else(|_| Err(io::Error::other("session writer stopped")))
    }
}

struct WriterState<S> {
    sink: S,
    failure: Option<(io::ErrorKind, String)>,
    /// Written since the last sync. Starts set: the header is written before anything else.
    dirty: bool,
    /// Entries of a new log not written yet: they go to the sink with the first entry after
    /// `Settle`, at the first sync, or when the log closes. Kept as entries, which share
    /// their text with the session, and written out only then.
    held: Option<Vec<Entry>>,
    settled: bool,
    /// Lines for the sink, written together at the end of a batch or before a sync.
    pending: Vec<String>,
}

impl<S: LineSink> WriterState<S> {
    fn new(sink: S) -> Self {
        Self {
            held: sink.is_new().then(Vec::new),
            sink,
            failure: None,
            dirty: true,
            settled: false,
            pending: Vec::new(),
        }
    }

    fn apply(&mut self, cmd: WriterCmd) {
        match cmd {
            WriterCmd::Append(entry) => {
                if self.failure.is_some() {
                    return;
                }
                if let Some(held) = &mut self.held
                    && !self.settled
                {
                    held.push(*entry);
                    self.dirty = true;
                    return;
                }
                self.release();
                match serde_json::to_string(&*entry) {
                    Ok(line) => {
                        self.pending.push(line);
                        self.dirty = true;
                    }
                    Err(err) => self.record(Err(io::Error::other(err))),
                }
            }
            WriterCmd::Sync(reply) => {
                let result = match &self.failure {
                    Some((kind, message)) => Err(io::Error::new(*kind, message.clone())),
                    None if !self.dirty => Ok(()),
                    None => {
                        self.release();
                        match self.flush() {
                            Ok(()) => self.sink.sync(),
                            // A failed write fails the log, as it does at the end of a batch.
                            Err(err) => {
                                let reply = io::Error::new(err.kind(), err.to_string());
                                self.record(Err(err));
                                Err(reply)
                            }
                        }
                    }
                };
                if result.is_ok() {
                    self.dirty = false;
                }
                let _ = reply.send(result);
            }
            WriterCmd::Settle => {
                self.dirty = false;
                self.settled = true;
            }
        }
    }

    /// Queues the held entries for the sink; writing them creates a new log.
    fn release(&mut self) {
        let Some(held) = self.held.take() else {
            return;
        };
        let mut lines = Vec::with_capacity(held.len());
        for entry in held {
            match serde_json::to_string(&entry) {
                Ok(line) => lines.push(line),
                Err(err) => self.record(Err(io::Error::other(err))),
            }
        }
        self.pending.splice(0..0, lines);
    }

    /// Writes the pending lines.
    fn flush(&mut self) -> io::Result<()> {
        if self.pending.is_empty() {
            return Ok(());
        }
        let result = self.sink.write_lines(&self.pending);
        self.pending.clear();
        result
    }

    /// Writes the pending lines at the end of a batch.
    fn end_batch(&mut self) {
        if self.failure.is_none() {
            let result = self.flush();
            self.record(result);
        }
    }

    fn record(&mut self, result: io::Result<()>) {
        if let Err(err) = result {
            tracing::error!(%err, "failed to append to the session log");
            self.failure = Some((err.kind(), err.to_string()));
        }
    }

    /// The log closes: held lines are written so that every session leaves a log, and
    /// unsynced lines are synced.
    fn close(mut self) {
        if self.failure.is_some() {
            return;
        }
        self.release();
        let result = self.flush();
        self.record(result);
        if self.dirty && self.failure.is_none() {
            let _ = self.sink.sync();
        }
    }
}

async fn run(sink: impl LineSink, mut rx: mpsc::UnboundedReceiver<WriterCmd>) {
    let mut state = WriterState::new(sink);
    let mut batch = Vec::new();
    while rx.recv_many(&mut batch, 256).await > 0 {
        // Starting a session only holds entries until Settle. Those commands do no
        // serialization or I/O, so they need neither a blocking worker nor a handoff.
        if state.held.is_some() {
            let mut settled = state.settled;
            let held = batch.iter().take_while(|cmd| match cmd {
                WriterCmd::Append(_) => !settled,
                WriterCmd::Settle => { settled = true; true }
                WriterCmd::Sync(_) => false,
            }).count();
            for cmd in batch.drain(..held) { state.apply(cmd); }
            if batch.is_empty() { continue; }
        }
        let mut cmds = std::mem::take(&mut batch);
        let result = tokio::task::spawn_blocking(move || {
            for cmd in cmds.drain(..) {
                state.apply(cmd);
            }
            state.end_batch();
            // Reuse the channel-drain allocation across batches.
            (state, cmds)
        }).await;
        match result {
            Ok((next, capacity)) => { state = next; batch = capacity; }
            // The runtime is shutting down; the sink is closed with the task.
            Err(_) => return,
        }
    }
    let _ = tokio::task::spawn_blocking(move || state.close()).await;
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::store::entry::EntryKind;
    use parking_lot::Mutex;
    use std::sync::Arc;

    /// Records what reaches the sink: lines, and `sync` as `"<sync>"`.
    #[derive(Clone)]
    struct Recorder {
        calls: Arc<Mutex<Vec<String>>>,
        new: bool,
    }

    impl LineSink for Recorder {
        fn write_line(&mut self, line: &str) -> io::Result<()> {
            let kind: serde_json::Value = serde_json::from_str(line).unwrap();
            self.calls.lock().push(kind["type"].as_str().unwrap().to_string());
            Ok(())
        }

        fn sync(&mut self) -> io::Result<()> {
            self.calls.lock().push("<sync>".into());
            Ok(())
        }

        fn is_new(&self) -> bool {
            self.new
        }
    }

    fn recorder(new: bool) -> (Recorder, Arc<Mutex<Vec<String>>>) {
        let calls = Arc::new(Mutex::new(Vec::new()));
        (Recorder { calls: calls.clone(), new }, calls)
    }

    fn entry(kind: EntryKind) -> Entry {
        Entry {
            id: "e".into(),
            parent: None,
            ts: 0,
            kind,
        }
    }

    fn settings() -> Entry {
        entry(EntryKind::Settings {
            model: Some("m".into()),
            thinking: None,
            permission_mode: None,
        })
    }

    fn branch() -> Entry {
        entry(EntryKind::Branch { before_first: false })
    }

    async fn calls_after_close(writer: SessionWriter, calls: &Arc<Mutex<Vec<String>>>) -> Vec<String> {
        let closed = writer.closed();
        drop(writer);
        closed.wait().await;
        calls.lock().clone()
    }

    /// Records each `write_lines` call as its lines joined by `+`.
    struct Batches(Arc<Mutex<Vec<String>>>);

    impl LineSink for Batches {
        fn write_line(&mut self, _line: &str) -> io::Result<()> {
            unreachable!("lines are written in batches")
        }

        fn write_lines(&mut self, lines: &[String]) -> io::Result<()> {
            let kinds: Vec<String> = lines
                .iter()
                .map(|l| serde_json::from_str::<serde_json::Value>(l).unwrap()["type"].as_str().unwrap().to_string())
                .collect();
            self.0.lock().push(kinds.join("+"));
            Ok(())
        }

        fn sync(&mut self) -> io::Result<()> {
            self.0.lock().push("<sync>".into());
            Ok(())
        }
    }

    #[test]
    fn a_batch_reaches_the_sink_in_one_write_and_before_its_sync() {
        let calls = Arc::new(Mutex::new(Vec::new()));
        let mut state = WriterState::new(Batches(calls.clone()));
        for cmd in [
            WriterCmd::Append(Box::new(settings())),
            WriterCmd::Append(Box::new(branch())),
            WriterCmd::Sync(oneshot::channel().0),
            WriterCmd::Append(Box::new(branch())),
            WriterCmd::Append(Box::new(settings())),
        ] {
            state.apply(cmd);
        }
        state.end_batch();
        assert_eq!(*calls.lock(), vec!["settings+branch", "<sync>", "branch+settings"]);
        state.close();
        assert_eq!(calls.lock().last().map(String::as_str), Some("<sync>"));
    }

    #[tokio::test]
    async fn a_new_log_is_written_once_the_session_does_something() {
        let (sink, calls) = recorder(true);
        let writer = SessionWriter::spawn(sink).unwrap();
        writer.append(settings());
        writer.settle();
        // Give the writer a chance to run: nothing may reach the sink yet.
        for _ in 0..20 {
            tokio::task::yield_now().await;
        }
        tokio::time::sleep(std::time::Duration::from_millis(20)).await;
        assert!(calls.lock().is_empty(), "{:?}", calls.lock());
        writer.append(branch());
        writer.sync().await.unwrap();
        assert_eq!(*calls.lock(), vec!["settings", "branch", "<sync>"]);
        assert_eq!(calls_after_close(writer, &calls).await, vec!["settings", "branch", "<sync>"]);
    }

    #[tokio::test]
    async fn an_untouched_new_log_is_written_on_close_without_a_sync() {
        let (sink, calls) = recorder(true);
        let writer = SessionWriter::spawn(sink).unwrap();
        writer.append(settings());
        writer.settle();
        assert_eq!(calls_after_close(writer, &calls).await, vec!["settings"]);
    }

    #[tokio::test]
    async fn a_sync_writes_held_lines_first() {
        let (sink, calls) = recorder(true);
        let writer = SessionWriter::spawn(sink).unwrap();
        writer.append(settings());
        writer.sync().await.unwrap();
        assert_eq!(*calls.lock(), vec!["settings", "<sync>"]);
    }

    #[tokio::test]
    async fn an_existing_log_is_written_right_away_and_synced_on_close() {
        let (sink, calls) = recorder(false);
        let writer = SessionWriter::spawn(sink).unwrap();
        writer.append(branch());
        writer.sync().await.unwrap();
        writer.append(settings());
        assert_eq!(calls_after_close(writer, &calls).await, vec!["branch", "<sync>", "settings", "<sync>"]);
    }

    #[test]
    fn works_without_a_runtime() {
        let (sink, calls) = recorder(false);
        let writer = SessionWriter::spawn(sink).unwrap();
        writer.append(branch());
        let closed = writer.closed();
        drop(writer);
        tokio::runtime::Builder::new_current_thread().enable_all().build().unwrap().block_on(closed.wait());
        assert_eq!(*calls.lock(), vec!["branch", "<sync>"]);
    }
}
