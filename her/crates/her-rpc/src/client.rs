//! A general-purpose client: requests with ids and awaited responses, notifications, and
//! requests from the other side, which arrive in [`IncomingRx`] with a receive time.

use parking_lot::Mutex;
use serde::Serialize;
use serde::de::DeserializeOwned;
use serde_json::Value;
use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::AtomicI64;
use std::sync::atomic::Ordering;
use std::time::Instant;
use tokio::io::AsyncRead;
use tokio::io::AsyncWrite;
use tokio::io::AsyncWriteExt;
use tokio::sync::mpsc;
use tokio::sync::oneshot;

use crate::encode;
use crate::frame::Frame;
use crate::frame::FrameReader;
use crate::message::Id;
use crate::message::RpcError;

type Pending = Arc<Mutex<Option<HashMap<Id, oneshot::Sender<Result<Value, RpcError>>>>>>;

#[derive(Debug, thiserror::Error)]
pub enum ClientError {
    #[error(transparent)]
    Rpc(#[from] RpcError),
    #[error("unexpected result: {0}")]
    Decode(#[from] serde_json::Error),
}

/// A message from the other side that is not a response.
#[derive(Debug, Clone)]
pub enum Incoming {
    Notification {
        method: String,
        params: Value,
        received: Instant,
    },
    Request {
        id: Id,
        method: String,
        params: Value,
        received: Instant,
    },
}

impl Incoming {
    pub fn method(&self) -> &str {
        match self {
            Incoming::Notification { method, .. } | Incoming::Request { method, .. } => method,
        }
    }

    pub fn params(&self) -> &Value {
        match self {
            Incoming::Notification { params, .. } | Incoming::Request { params, .. } => params,
        }
    }
}

pub type IncomingRx = mpsc::UnboundedReceiver<Incoming>;

/// Cheap to clone; every clone shares the connection.
#[derive(Clone)]
pub struct Client {
    out: mpsc::UnboundedSender<Vec<u8>>,
    pending: Pending,
    next_id: Arc<AtomicI64>,
}

impl Client {
    /// Spawns the reader and writer tasks. The connection ends when the reader sees end of input.
    pub fn connect<R, W>(reader: R, writer: W) -> (Client, IncomingRx)
    where
        R: AsyncRead + Unpin + Send + 'static,
        W: AsyncWrite + Unpin + Send + 'static,
    {
        let (out_tx, mut out_rx) = mpsc::unbounded_channel::<Vec<u8>>();
        let (in_tx, in_rx) = mpsc::unbounded_channel();
        let pending: Pending = Arc::new(Mutex::new(Some(HashMap::new())));
        tokio::spawn(async move {
            let mut writer = writer;
            while let Some(first) = out_rx.recv().await {
                let mut batch = first;
                while let Ok(more) = out_rx.try_recv() {
                    batch.extend_from_slice(&more);
                }
                if writer.write_all(&batch).await.is_err() || writer.flush().await.is_err() {
                    break;
                }
            }
            let _ = writer.shutdown().await;
        });
        let reader_pending = pending.clone();
        tokio::spawn(async move {
            let mut frames = FrameReader::new(reader, usize::MAX / 2);
            while let Ok(Some(frame)) = frames.next().await {
                let Frame::Line(line) = frame else { continue };
                let received = Instant::now();
                let Ok(mut value) = serde_json::from_slice::<Value>(&line) else {
                    continue;
                };
                let method = value.get("method").and_then(Value::as_str).map(str::to_string);
                let id = value.get("id").cloned().and_then(|v| serde_json::from_value::<Id>(v).ok());
                match (method, id) {
                    (Some(method), Some(id)) => {
                        let params = value.get_mut("params").map(Value::take).unwrap_or(Value::Null);
                        let _ = in_tx.send(Incoming::Request {
                            id,
                            method,
                            params,
                            received,
                        });
                    }
                    (Some(method), None) => {
                        let params = value.get_mut("params").map(Value::take).unwrap_or(Value::Null);
                        let _ = in_tx.send(Incoming::Notification {
                            method,
                            params,
                            received,
                        });
                    }
                    (None, Some(id)) => {
                        let result = match value.get_mut("error").map(Value::take) {
                            Some(error) if !error.is_null() => Err(serde_json::from_value(error)
                                .unwrap_or_else(|_| RpcError::internal("malformed error object"))),
                            _ => Ok(value.get_mut("result").map(Value::take).unwrap_or(Value::Null)),
                        };
                        let waiter = reader_pending.lock().as_mut().and_then(|map| map.remove(&id));
                        if let Some(waiter) = waiter {
                            let _ = waiter.send(result);
                        }
                    }
                    (None, None) => {}
                }
            }
            // Fail everything still waiting, and refuse new requests.
            let waiters = reader_pending.lock().take().unwrap_or_default();
            for (_, waiter) in waiters {
                let _ = waiter.send(Err(RpcError::internal("the connection closed")));
            }
        });
        (
            Client {
                out: out_tx,
                pending,
                next_id: Arc::new(AtomicI64::new(1)),
            },
            in_rx,
        )
    }

    /// Sends a request and waits for its response.
    pub async fn request<P: Serialize>(&self, method: &str, params: P) -> Result<Value, RpcError> {
        let id = Id::Int(self.next_id.fetch_add(1, Ordering::Relaxed));
        let (tx, rx) = oneshot::channel();
        match self.pending.lock().as_mut() {
            Some(map) => {
                map.insert(id.clone(), tx);
            }
            None => return Err(RpcError::internal("the connection closed")),
        }
        self.send_raw(encode::request(&id, method, &params));
        rx.await.unwrap_or_else(|_| Err(RpcError::internal("the connection closed")))
    }

    /// [`Client::request`] with the result deserialized.
    pub async fn call<P: Serialize, T: DeserializeOwned>(&self, method: &str, params: P) -> Result<T, ClientError> {
        let value = self.request(method, params).await?;
        Ok(serde_json::from_value(value)?)
    }

    pub fn notify<P: Serialize>(&self, method: &str, params: P) {
        self.send_raw(encode::notification(method, &params));
    }

    /// Answers a request from the other side.
    pub fn respond<T: Serialize>(&self, id: &Id, result: T) {
        self.send_raw(encode::response(id, &result));
    }

    pub fn respond_error(&self, id: &Id, error: &RpcError) {
        self.send_raw(encode::error_response(Some(id), error));
    }

    /// Writes bytes as they are, for example a malformed line in a test.
    pub fn send_raw(&self, bytes: Vec<u8>) {
        let _ = self.out.send(bytes);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::message::Kind;
    use crate::message::RawMessage;
    use serde_json::json;

    /// A tiny echo server: answers `echo` with its params, emits a notification first.
    async fn echo_server(stream: tokio::io::DuplexStream) {
        let (read, mut write) = tokio::io::split(stream);
        let mut frames = FrameReader::new(read, 1 << 20);
        while let Some(Frame::Line(line)) = frames.next().await.unwrap() {
            let message = RawMessage::parse(&line).unwrap();
            if let Kind::Request { id, method, params } = message.kind() {
                let params: Value = crate::message::params(params).unwrap();
                write
                    .write_all(&encode::notification("note", &json!({"for": method})))
                    .await
                    .unwrap();
                write.write_all(&encode::response(id, &params)).await.unwrap();
            }
        }
    }

    #[tokio::test]
    async fn requests_get_their_own_responses() {
        let (a, b) = tokio::io::duplex(1024);
        tokio::spawn(echo_server(b));
        let (read, write) = tokio::io::split(a);
        let (client, mut incoming) = Client::connect(read, write);
        let (r1, r2) = tokio::join!(client.request("echo", json!({"n": 1})), client.request("echo", json!({"n": 2})));
        assert_eq!(r1.unwrap(), json!({"n": 1}));
        assert_eq!(r2.unwrap(), json!({"n": 2}));
        let note = incoming.recv().await.unwrap();
        assert_eq!(note.method(), "note");
    }
}
