//! The kernel's side of the worker (design §9.4.8): one process serves every document, a
//! request at a time. When it dies — a crash inside PDFium — the next request starts a new one
//! and opens the documents again before going on.

use parking_lot::Mutex;
use serde::de::DeserializeOwned;
use std::collections::HashMap;
use std::collections::HashSet;
use std::io::BufRead;
use std::io::BufReader;
use std::io::Write;
use std::path::PathBuf;
use std::process::Child;
use std::process::Command;
use std::process::Stdio;
use std::sync::Arc;

use crate::engine::Request;

/// How the worker is started.
#[derive(Clone, Debug)]
pub enum Launcher {
    /// A process: `her doc-worker`.
    Process { program: PathBuf, args: Vec<String> },
    /// A thread of this process, for tests: no crash isolation.
    Thread,
}

impl Launcher {
    /// The `pdf` component's worker where it has one (static builds cannot load the library
    /// themselves), otherwise this program, run as `<exe> doc-worker`.
    pub fn this_program() -> Self {
        if let Some(worker) = crate::pdf_addon_dir().map(|dir| dir.join("her-doc-worker")).filter(|path| path.is_file()) {
            return Launcher::Process { program: worker, args: Vec::new() };
        }
        let program = std::env::current_exe().unwrap_or_else(|_| PathBuf::from("her"));
        Launcher::Process { program, args: vec!["doc-worker".into()] }
    }
}

struct Running {
    input: Box<dyn Write + Send>,
    output: BufReader<Box<dyn std::io::Read + Send>>,
    child: Option<Child>,
    /// The handles opened in this process.
    open: HashSet<u64>,
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
    /// Every document opened, by handle, to open again in a new process.
    documents: HashMap<u64, Request>,
    next: u64,
}

#[derive(Clone)]
pub struct Worker {
    launcher: Launcher,
    state: Arc<Mutex<State>>,
}

#[derive(Debug, thiserror::Error)]
pub enum WorkerError {
    #[error("the document worker could not start: {0}")]
    Start(String),
    #[error("the document worker stopped: {0}")]
    Died(String),
    #[error("{0}")]
    Failed(String),
}

impl Worker {
    pub fn new(launcher: Launcher) -> Self {
        Self { launcher, state: Arc::default() }
    }

    /// Opens a PDF; its handle is used in the other requests.
    pub async fn open<T: DeserializeOwned + Send + 'static>(&self, path: PathBuf, password: Option<String>) -> Result<(u64, T), WorkerError> {
        self.open_with(|handle| Request::Open { handle, path, password }).await
    }

    /// Opens a workbook (spreadsheet-design §5.7); after a crash it is opened again from its
    /// working copy, which holds every edit written so far.
    pub async fn open_sheet<T: DeserializeOwned + Send + 'static>(&self, path: PathBuf, name: Option<String>) -> Result<(u64, T), WorkerError> {
        self.open_with(|handle| Request::SheetOpen { handle, path, name }).await
    }

    /// Opens under a new handle, kept to open it again in a new process after a crash.
    async fn open_with<T: DeserializeOwned + Send + 'static>(&self, request: impl FnOnce(u64) -> Request) -> Result<(u64, T), WorkerError> {
        let handle = {
            let mut state = self.state.lock();
            state.next += 1;
            state.next
        };
        let request = request(handle);
        let opened = self.request(request.clone()).await?;
        self.state.lock().documents.insert(handle, request);
        Ok((handle, opened))
    }

    pub async fn close(&self, handle: u64) {
        self.state.lock().documents.remove(&handle);
        let _ = self.request::<serde_json::Value>(Request::Close { handle }).await;
    }

    /// Sends a request; when the process has died, starts another and tries once more.
    pub async fn request<T: DeserializeOwned + Send + 'static>(&self, request: Request) -> Result<T, WorkerError> {
        let this = self.clone();
        tokio::task::spawn_blocking(move || {
            let mut state = this.state.lock();
            match this.exchange(&mut state, &request) {
                Err(WorkerError::Died(_)) => this.exchange(&mut state, &request),
                other => other,
            }
        })
        .await
        .map_err(|e| WorkerError::Died(e.to_string()))?
    }

    fn exchange<T: DeserializeOwned>(&self, state: &mut State, request: &Request) -> Result<T, WorkerError> {
        if state.running.is_none() {
            state.running = Some(self.start()?);
        }
        // The document a request names, opened in this process first.
        let handle = request.handle();
        if let Some(handle) = handle
            && !state.running.as_ref().is_some_and(|r| r.open.contains(&handle))
            && let Some(open) = state.documents.get(&handle).cloned()
        {
            let reply = send(state, &open);
            if let Err(WorkerError::Died(_)) = reply {
                state.running = None;
            }
            reply?;
            state.running.as_mut().expect("started above").open.insert(handle);
        }
        let reply = send(state, request);
        match &reply {
            Err(WorkerError::Died(_)) => state.running = None,
            Ok(_) => {
                if let (Request::Open { handle, .. } | Request::SheetOpen { handle, .. }, Some(running)) = (request, state.running.as_mut()) {
                    running.open.insert(*handle);
                }
            }
            Err(_) => {}
        }
        let value = reply?;
        serde_json::from_value(value).map_err(|e| WorkerError::Failed(format!("unexpected reply: {e}")))
    }

    fn start(&self) -> Result<Running, WorkerError> {
        match &self.launcher {
            Launcher::Process { program, args } => {
                let mut child = Command::new(program)
                    .args(args)
                    .stdin(Stdio::piped())
                    .stdout(Stdio::piped())
                    .stderr(Stdio::inherit())
                    .spawn()
                    .map_err(|e| WorkerError::Start(format!("{}: {e}", program.display())))?;
                let input = child.stdin.take().expect("piped");
                let output = child.stdout.take().expect("piped");
                Ok(Running {
                    input: Box::new(input),
                    output: BufReader::new(Box::new(output)),
                    child: Some(child),
                    open: HashSet::new(),
                })
            }
            Launcher::Thread => {
                let (request_rx, request_tx) = std::io::pipe().map_err(|e| WorkerError::Start(e.to_string()))?;
                let (reply_rx, reply_tx) = std::io::pipe().map_err(|e| WorkerError::Start(e.to_string()))?;
                std::thread::spawn(move || crate::engine::serve(BufReader::new(request_rx), reply_tx));
                Ok(Running {
                    input: Box::new(request_tx),
                    output: BufReader::new(Box::new(reply_rx)),
                    child: None,
                    open: HashSet::new(),
                })
            }
        }
    }
}

fn send(state: &mut State, request: &Request) -> Result<serde_json::Value, WorkerError> {
    let running = state.running.as_mut().ok_or_else(|| WorkerError::Died("not running".into()))?;
    let mut line = serde_json::to_vec(request).map_err(|e| WorkerError::Failed(e.to_string()))?;
    line.push(b'\n');
    running.input.write_all(&line).and_then(|()| running.input.flush()).map_err(|e| WorkerError::Died(e.to_string()))?;
    let mut reply = String::new();
    match running.output.read_line(&mut reply) {
        Ok(0) => return Err(WorkerError::Died("it exited".into())),
        Err(e) => return Err(WorkerError::Died(e.to_string())),
        Ok(_) => {}
    }
    let mut reply: serde_json::Value = serde_json::from_str(&reply).map_err(|e| WorkerError::Died(format!("bad reply: {e}")))?;
    if let Some(err) = reply.get("err").and_then(|e| e.as_str()) {
        return Err(WorkerError::Failed(err.to_string()));
    }
    Ok(reply.get_mut("ok").map(serde_json::Value::take).unwrap_or_default())
}
