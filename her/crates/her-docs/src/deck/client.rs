//! The kernel's side of `her deck-worker`. Requests for different decks are in flight together;
//! a reader thread hands each reply to its caller. When the process dies every caller waiting
//! gets `Died`; the next request starts another process, opens its deck there again from the
//! deck's directory (source, snapshot and log: nothing acknowledged is lost) and tries once more.
//! A deck that brings the process down three times in a minute is quarantined (§7.3).

// The worker has no other log than its stderr, which the kernel keeps.
#![allow(clippy::print_stderr)]

use std::collections::{HashMap, HashSet, VecDeque};
use std::io::{BufRead, BufReader, Write};
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::sync::Arc;
use std::time::{Duration, Instant};

use parking_lot::Mutex;
use serde::de::DeserializeOwned;
use serde_json::Value;
use tokio::sync::oneshot;

use super::actor::NOT_OPEN;
use super::wire::{Envelope, Opened, Request};
use crate::worker::WorkerError;

const CRASHES: usize = 3;
const CRASH_WINDOW: Duration = Duration::from_secs(60);

/// How the deck worker runs.
#[derive(Clone, Debug)]
pub enum DeckLauncher {
    /// `<program> deck-worker`.
    Process { program: PathBuf, args: Vec<String> },
    /// A thread of this process, for tests.
    Thread,
}

impl DeckLauncher {
    /// This program, run as `<exe> deck-worker`. Not the PDF component's worker: that one does
    /// not carry the presentation engine.
    pub fn this_program() -> Self {
        let program = std::env::current_exe().unwrap_or_else(|_| PathBuf::from("her"));
        DeckLauncher::Process { program, args: vec!["deck-worker".into()] }
    }

    /// The same kind of worker as the document worker's: a thread in tests, else a process.
    pub fn like(doc: &crate::Launcher) -> Self {
        match doc {
            crate::Launcher::Thread => DeckLauncher::Thread,
            crate::Launcher::Process { .. } => Self::this_program(),
        }
    }
}

type Waiting = HashMap<u64, oneshot::Sender<Result<Value, WorkerError>>>;

struct Running {
    input: Box<dyn Write + Send>,
    child: Option<Child>,
    /// Shared with the reader thread, which empties it when the process ends.
    waiting: Arc<Mutex<Option<Waiting>>>,
    open: HashSet<String>,
}

impl Drop for Running {
    fn drop(&mut self) {
        if let Some(child) = &mut self.child {
            let _ = child.kill();
            let _ = child.wait();
        }
    }
}

#[derive(Default)]
struct State {
    running: Option<Running>,
    next: u64,
    /// Every deck opened, to open again in a new process.
    decks: HashMap<String, PathBuf>,
    crashes: HashMap<String, VecDeque<Instant>>,
    quarantined: HashSet<String>,
}

#[derive(Clone)]
pub struct DeckWorker {
    launcher: DeckLauncher,
    state: Arc<Mutex<State>>,
}

impl DeckWorker {
    pub fn new(launcher: DeckLauncher) -> Self {
        Self { launcher, state: Arc::default() }
    }

    pub async fn open(&self, deck: &str, dir: PathBuf) -> Result<Opened, WorkerError> {
        self.state.lock().decks.insert(deck.to_owned(), dir.clone());
        let opened = self.request(Request::Open { deck: deck.to_owned(), dir }).await;
        if opened.is_err() {
            self.state.lock().decks.remove(deck);
        }
        opened
    }

    pub async fn close(&self, deck: &str) {
        self.state.lock().decks.remove(deck);
        let _ = self.request::<Value>(Request::Close { deck: deck.to_owned() }).await;
        if let Some(running) = self.state.lock().running.as_mut() {
            running.open.remove(deck);
        }
    }

    pub fn is_quarantined(&self, deck: &str) -> bool {
        self.state.lock().quarantined.contains(deck)
    }

    /// Ends the process, as a crash would (tests), or at shutdown.
    pub async fn exit(&self) {
        let _ = self.send(Request::Exit).map(drop);
        // The reader thread notices the end; a new request starts a new process.
        if let Some(mut running) = self.state.lock().running.take()
            && let Some(child) = running.child.as_mut()
        {
            let _ = child.wait();
        }
    }

    /// Sends a request; when the process died under it, starts another and tries once more.
    pub async fn request<T: DeserializeOwned>(&self, request: Request) -> Result<T, WorkerError> {
        let deck = request.deck().map(str::to_owned);
        if let Some(deck) = &deck
            && self.is_quarantined(deck)
        {
            return Err(WorkerError::Failed(format!("{QUARANTINED}{deck}")));
        }
        let first = self.attempt(&request).await;
        let value = match first {
            Err(WorkerError::Died(reason)) => {
                if let Some(deck) = &deck
                    && self.crashed(deck)
                {
                    return Err(WorkerError::Failed(format!("{QUARANTINED}{deck}: {reason}")));
                }
                self.attempt(&request).await?
            }
            other => other?,
        };
        serde_json::from_value(value).map_err(|e| WorkerError::Failed(format!("unexpected reply: {e}")))
    }

    async fn attempt(&self, request: &Request) -> Result<Value, WorkerError> {
        // Opened in this process first: after a restart, from its directory.
        if let Some(deck) = request.deck()
            && !matches!(request, Request::Open { .. })
        {
            let reopen = {
                let state = self.state.lock();
                let open_here = state.running.as_ref().is_some_and(|r| r.open.contains(deck));
                (!open_here).then(|| state.decks.get(deck).cloned()).flatten()
            };
            if let Some(dir) = reopen {
                let open = Request::Open { deck: deck.to_owned(), dir };
                self.send(open)?.await.map_err(|_| WorkerError::Died("no reply".into()))??;
                if let Some(r) = self.state.lock().running.as_mut() {
                    r.open.insert(deck.to_owned());
                }
            }
        }
        let rx = self.send(request.clone())?;
        let reply = rx.await.map_err(|_| WorkerError::Died("no reply".into()))?;
        match (&reply, request) {
            (Ok(_), Request::Open { deck, .. }) => {
                if let Some(r) = self.state.lock().running.as_mut() {
                    r.open.insert(deck.clone());
                }
            }
            (Err(WorkerError::Failed(e)), _) if e.starts_with(NOT_OPEN) => {
                // The process restarted between the check and the send.
                return Err(WorkerError::Died(e.clone()));
            }
            _ => {}
        }
        reply
    }

    /// Writes a request; the receiver gets its reply.
    fn send(&self, request: Request) -> Result<oneshot::Receiver<Result<Value, WorkerError>>, WorkerError> {
        let mut state = self.state.lock();
        let alive = state.running.as_ref().is_some_and(|r| r.waiting.lock().is_some());
        if !alive {
            state.running = None;
            state.running = Some(self.start()?);
        }
        state.next += 1;
        let id = state.next;
        let running = state.running.as_mut().expect("started above");
        let (tx, rx) = oneshot::channel();
        match running.waiting.lock().as_mut() {
            Some(waiting) => waiting.insert(id, tx),
            None => return Err(WorkerError::Died("it exited".into())),
        };
        let mut line = serde_json::to_vec(&Envelope { id, body: request }).map_err(|e| WorkerError::Failed(e.to_string()))?;
        line.push(b'\n');
        if let Err(e) = running.input.write_all(&line).and_then(|()| running.input.flush()) {
            running.waiting.lock().as_mut().and_then(|w| w.remove(&id));
            return Err(WorkerError::Died(e.to_string()));
        }
        Ok(rx)
    }

    /// Counts a crash under `deck`; true when it is now quarantined.
    fn crashed(&self, deck: &str) -> bool {
        let mut state = self.state.lock();
        let now = Instant::now();
        let times = state.crashes.entry(deck.to_owned()).or_default();
        times.push_back(now);
        while times.front().is_some_and(|t| now.duration_since(*t) > CRASH_WINDOW) {
            times.pop_front();
        }
        if times.len() >= CRASHES {
            state.quarantined.insert(deck.to_owned());
            return true;
        }
        false
    }

    fn start(&self) -> Result<Running, WorkerError> {
        let (input, output, child): (Box<dyn Write + Send>, Box<dyn std::io::Read + Send>, Option<Child>) = match &self.launcher {
            DeckLauncher::Process { program, args } => {
                let mut child = Command::new(program)
                    .args(args)
                    .stdin(Stdio::piped())
                    .stdout(Stdio::piped())
                    .stderr(Stdio::inherit())
                    .spawn()
                    .map_err(|e| WorkerError::Start(format!("{}: {e}", program.display())))?;
                let input = child.stdin.take().expect("piped");
                let output = child.stdout.take().expect("piped");
                (Box::new(input), Box::new(output), Some(child))
            }
            DeckLauncher::Thread => {
                let (request_rx, request_tx) = std::io::pipe().map_err(|e| WorkerError::Start(e.to_string()))?;
                let (reply_rx, reply_tx) = std::io::pipe().map_err(|e| WorkerError::Start(e.to_string()))?;
                std::thread::spawn(move || super::actor::serve(BufReader::new(request_rx), reply_tx));
                (Box::new(request_tx), Box::new(reply_rx), None)
            }
        };
        let waiting: Arc<Mutex<Option<Waiting>>> = Arc::new(Mutex::new(Some(HashMap::new())));
        let reader = waiting.clone();
        std::thread::Builder::new()
            .name("deck-worker-replies".into())
            .spawn(move || read_replies(BufReader::new(output), &reader))
            .map_err(|e| WorkerError::Start(e.to_string()))?;
        Ok(Running { input, child, waiting, open: HashSet::new() })
    }
}

/// Prefix of the error for a quarantined deck.
pub const QUARANTINED: &str = "quarantined after repeated worker crashes: ";

fn read_replies(output: impl BufRead, waiting: &Mutex<Option<Waiting>>) {
    for line in output.lines() {
        let Ok(line) = line else { break };
        let Ok(mut reply) = serde_json::from_str::<Value>(&line) else {
            eprintln!("deck-worker: bad reply");
            break;
        };
        let Some(id) = reply.get("id").and_then(Value::as_u64) else { continue };
        let result = match reply.get("err").and_then(Value::as_str) {
            Some(err) => Err(WorkerError::Failed(err.to_owned())),
            None => Ok(reply.get_mut("ok").map(Value::take).unwrap_or_default()),
        };
        if let Some(tx) = waiting.lock().as_mut().and_then(|w| w.remove(&id)) {
            let _ = tx.send(result);
        }
    }
    // The process ended: everyone still waiting learns it.
    if let Some(waiting) = waiting.lock().take() {
        for (_, tx) in waiting {
            let _ = tx.send(Err(WorkerError::Died("it exited".into())));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn three_crashes_in_a_minute_quarantine_a_deck() {
        let worker = DeckWorker::new(DeckLauncher::Thread);
        assert!(!worker.crashed("a"));
        assert!(!worker.crashed("b"));
        assert!(!worker.crashed("a"));
        assert!(worker.crashed("a"));
        assert!(worker.is_quarantined("a"));
        assert!(!worker.is_quarantined("b"));
    }
}
