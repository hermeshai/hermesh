//! A CDP connection over a byte stream: each message is one JSON text ended by `\0`, as
//! `--remote-debugging-pipe` speaks it. Commands are paired with their responses by id;
//! everything else is an event, handed to one consumer in arrival order.

use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use std::time::Duration;
use tokio::io::AsyncRead;
use tokio::io::AsyncReadExt;
use tokio::io::AsyncWrite;
use tokio::io::AsyncWriteExt;
use tokio::sync::mpsc;
use tokio::sync::oneshot;

/// Largest message accepted. Accessibility trees of big pages run to a few megabytes.
const MAX_MESSAGE: usize = 256 << 20;

#[derive(Clone, Debug, thiserror::Error, PartialEq)]
pub enum CdpError {
    #[error("the browser connection is closed")]
    Closed,
    #[error("{method} timed out")]
    Timeout { method: String },
    #[error("{method}: {message}")]
    Protocol { method: String, code: i64, message: String },
}

/// An event; `session` is the flattened target session it belongs to, none for the browser.
#[derive(Clone, Debug)]
pub struct CdpEvent {
    pub session: Option<String>,
    pub method: String,
    pub params: Value,
}

type Reply = oneshot::Sender<Result<Value, CdpError>>;

pub struct Conn {
    writer: tokio::sync::Mutex<Box<dyn AsyncWrite + Send + Unpin>>,
    pending: Mutex<HashMap<u64, (String, Reply)>>,
    next_id: AtomicU64,
    closed: AtomicBool,
    timeout: Duration,
}

impl Conn {
    /// Starts reading `reader` in the background. The receiver ends when the stream does.
    pub fn start(
        reader: impl AsyncRead + Send + Unpin + 'static,
        writer: impl AsyncWrite + Send + Unpin + 'static,
        timeout: Duration,
    ) -> (Arc<Conn>, mpsc::UnboundedReceiver<CdpEvent>) {
        let conn = Arc::new(Conn {
            writer: tokio::sync::Mutex::new(Box::new(writer)),
            pending: Mutex::new(HashMap::new()),
            next_id: AtomicU64::new(1),
            closed: AtomicBool::new(false),
            timeout,
        });
        let (tx, rx) = mpsc::unbounded_channel();
        tokio::spawn(read_loop(conn.clone(), reader, tx));
        (conn, rx)
    }

    pub fn is_closed(&self) -> bool {
        self.closed.load(Ordering::Acquire)
    }

    /// Sends a command to `session` (the browser when `None`) and waits for its result.
    pub async fn send(&self, session: Option<&str>, method: &str, params: Value) -> Result<Value, CdpError> {
        self.send_with_timeout(session, method, params, self.timeout).await
    }

    pub async fn send_with_timeout(
        &self,
        session: Option<&str>,
        method: &str,
        params: Value,
        timeout: Duration,
    ) -> Result<Value, CdpError> {
        if self.is_closed() {
            return Err(CdpError::Closed);
        }
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let mut message = json!({ "id": id, "method": method, "params": params });
        if let Some(session) = session {
            message["sessionId"] = Value::String(session.to_string());
        }
        let (tx, rx) = oneshot::channel();
        self.pending.lock().insert(id, (method.to_string(), tx));
        let mut bytes = serde_json::to_vec(&message).expect("a JSON value serializes");
        bytes.push(0);
        let written = {
            let mut writer = self.writer.lock().await;
            match writer.write_all(&bytes).await {
                Ok(()) => writer.flush().await,
                Err(err) => Err(err),
            }
        };
        if written.is_err() {
            self.pending.lock().remove(&id);
            self.close();
            return Err(CdpError::Closed);
        }
        match tokio::time::timeout(timeout, rx).await {
            Ok(Ok(result)) => result,
            Ok(Err(_)) => Err(CdpError::Closed),
            Err(_) => {
                self.pending.lock().remove(&id);
                Err(CdpError::Timeout {
                    method: method.to_string(),
                })
            }
        }
    }

    /// Sends without waiting for the result; failures are logged.
    pub fn post(self: &Arc<Self>, session: Option<&str>, method: &str, params: Value) {
        let conn = self.clone();
        let session = session.map(str::to_string);
        let method = method.to_string();
        tokio::spawn(async move {
            if let Err(err) = conn.send(session.as_deref(), &method, params).await {
                tracing::debug!(%err, "CDP command without a waiter failed");
            }
        });
    }

    /// Fails every waiting command; later ones fail at once.
    fn close(&self) {
        self.closed.store(true, Ordering::Release);
        let pending: Vec<(String, Reply)> = self.pending.lock().drain().map(|(_, v)| v).collect();
        for (_, reply) in pending {
            let _ = reply.send(Err(CdpError::Closed));
        }
    }

    fn resolve(&self, message: &Value) {
        let Some(id) = message.get("id").and_then(Value::as_u64) else {
            return;
        };
        let Some((method, reply)) = self.pending.lock().remove(&id) else {
            return;
        };
        let result = match message.get("error") {
            Some(error) => Err(CdpError::Protocol {
                method,
                code: error.get("code").and_then(Value::as_i64).unwrap_or(0),
                message: error.get("message").and_then(Value::as_str).unwrap_or("unknown error").to_string(),
            }),
            None => Ok(message.get("result").cloned().unwrap_or(Value::Null)),
        };
        let _ = reply.send(result);
    }
}

async fn read_loop(conn: Arc<Conn>, mut reader: impl AsyncRead + Unpin, events: mpsc::UnboundedSender<CdpEvent>) {
    let mut buffer: Vec<u8> = Vec::with_capacity(64 << 10);
    let mut chunk = vec![0u8; 64 << 10];
    'read: loop {
        let n = match reader.read(&mut chunk).await {
            Ok(0) | Err(_) => break,
            Ok(n) => n,
        };
        let start = buffer.len();
        buffer.extend_from_slice(&chunk[..n]);
        let mut from = 0;
        let mut scan = start;
        while let Some(end) = memchr(&buffer[scan..]).map(|i| scan + i) {
            dispatch(&conn, &buffer[from..end], &events);
            from = end + 1;
            scan = from;
        }
        buffer.drain(..from);
        if buffer.len() > MAX_MESSAGE {
            tracing::warn!("CDP message over {MAX_MESSAGE} bytes; closing the connection");
            break 'read;
        }
    }
    conn.close();
}

fn memchr(bytes: &[u8]) -> Option<usize> {
    bytes.iter().position(|b| *b == 0)
}

fn dispatch(conn: &Conn, bytes: &[u8], events: &mpsc::UnboundedSender<CdpEvent>) {
    let message: Value = match serde_json::from_slice(bytes) {
        Ok(message) => message,
        Err(err) => {
            tracing::debug!(%err, "ignored a CDP message that is not JSON");
            return;
        }
    };
    if message.get("id").is_some() {
        conn.resolve(&message);
        return;
    }
    let Some(method) = message.get("method").and_then(Value::as_str) else {
        return;
    };
    let _ = events.send(CdpEvent {
        session: message.get("sessionId").and_then(Value::as_str).map(str::to_string),
        method: method.to_string(),
        params: message.get("params").cloned().unwrap_or(Value::Null),
    });
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::AsyncBufReadExt;
    use tokio::io::BufReader;

    /// A fake browser on the other end of an in-memory stream.
    fn pair() -> (Arc<Conn>, mpsc::UnboundedReceiver<CdpEvent>, tokio::io::DuplexStream, tokio::io::DuplexStream) {
        let (ours_in, theirs_out) = tokio::io::duplex(1 << 20);
        let (theirs_in, ours_out) = tokio::io::duplex(1 << 20);
        let (conn, events) = Conn::start(ours_in, ours_out, Duration::from_millis(500));
        (conn, events, theirs_in, theirs_out)
    }

    async fn next_command(reader: &mut BufReader<tokio::io::DuplexStream>) -> Value {
        let mut bytes = Vec::new();
        reader.read_until(0, &mut bytes).await.unwrap();
        bytes.pop();
        serde_json::from_slice(&bytes).unwrap()
    }

    #[tokio::test]
    async fn pairs_responses_out_of_order_and_passes_events_on() {
        let (conn, mut events, theirs_in, mut theirs_out) = pair();
        let mut reader = BufReader::new(theirs_in);
        let a = tokio::spawn({
            let conn = conn.clone();
            async move { conn.send(None, "A.one", json!({})).await }
        });
        let first = next_command(&mut reader).await;
        let b = tokio::spawn({
            let conn = conn.clone();
            async move { conn.send(Some("S1"), "B.two", json!({"x": 1})).await }
        });
        let second = next_command(&mut reader).await;
        assert_eq!(second["sessionId"], "S1");
        assert_eq!(second["params"]["x"], 1);
        // An event and both answers in one write, the later command answered first.
        let mut bytes = Vec::new();
        for value in [
            json!({"method": "Page.loadEventFired", "sessionId": "S1", "params": {"t": 1}}),
            json!({"id": second["id"], "result": {"b": true}}),
            json!({"id": first["id"], "error": {"code": -32000, "message": "nope"}}),
        ] {
            bytes.extend(serde_json::to_vec(&value).unwrap());
            bytes.push(0);
        }
        theirs_out.write_all(&bytes).await.unwrap();
        assert_eq!(b.await.unwrap().unwrap(), json!({"b": true}));
        let err = a.await.unwrap().unwrap_err();
        assert!(matches!(err, CdpError::Protocol { code: -32000, .. }), "{err:?}");
        let event = events.recv().await.unwrap();
        assert_eq!((event.session.as_deref(), event.method.as_str()), (Some("S1"), "Page.loadEventFired"));
    }

    #[tokio::test]
    async fn times_out_and_fails_everything_when_the_stream_ends() {
        let (conn, mut events, theirs_in, theirs_out) = pair();
        let err = conn.send(None, "Slow.call", json!({})).await.unwrap_err();
        assert_eq!(err, CdpError::Timeout { method: "Slow.call".into() });
        let waiting = tokio::spawn({
            let conn = conn.clone();
            async move { conn.send(None, "Never.answered", json!({})).await }
        });
        tokio::time::sleep(Duration::from_millis(50)).await;
        drop(theirs_out);
        drop(theirs_in);
        assert_eq!(waiting.await.unwrap().unwrap_err(), CdpError::Closed);
        assert!(events.recv().await.is_none());
        assert_eq!(conn.send(None, "After.close", json!({})).await.unwrap_err(), CdpError::Closed);
    }

    #[tokio::test]
    async fn a_message_split_across_reads_is_put_back_together() {
        let (conn, _events, theirs_in, mut theirs_out) = pair();
        let mut reader = BufReader::new(theirs_in);
        let call = tokio::spawn({
            let conn = conn.clone();
            async move { conn.send(None, "Big.result", json!({})).await }
        });
        let command = next_command(&mut reader).await;
        let text = "x".repeat(200_000);
        let mut bytes = serde_json::to_vec(&json!({"id": command["id"], "result": {"text": text}})).unwrap();
        bytes.push(0);
        for part in bytes.chunks(7_000) {
            theirs_out.write_all(part).await.unwrap();
        }
        assert_eq!(call.await.unwrap().unwrap()["text"].as_str().unwrap().len(), 200_000);
    }
}
