//! Transports carry single JSON-RPC messages; the client does correlation and timeouts.

pub(crate) mod http;
pub(crate) mod sse;
pub(crate) mod stdio;

use async_trait::async_trait;
use serde_json::Value;

use crate::McpError;

/// What a transport delivers to the client.
#[derive(Debug)]
pub(crate) enum Inbound {
    Message(Value),
    /// The connection is gone: the process exited, or the HTTP session cannot continue.
    Closed(String),
}

#[async_trait]
pub(crate) trait Transport: Send + Sync + 'static {
    /// Sends one message. A request's response arrives later as [`Inbound::Message`].
    async fn send(&self, message: Value) -> Result<(), McpError>;

    /// The version agreed in `initialize`; HTTP sends it as a header from then on.
    fn set_protocol_version(&self, version: &str) {
        let _ = version;
    }

    /// `notifications/initialized` was sent; HTTP opens its stream for server messages.
    fn initialized(&self) {}

    /// Ends the connection and, for a local server, the process.
    async fn close(&self);
}
