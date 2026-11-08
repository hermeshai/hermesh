//! JSON-RPC 2.0 over newline-delimited JSON (one message per line).
//!
//! The server side parses each line once into a borrowed [`RawMessage`] and dispatches on the
//! method before touching the parameters; replies are encoded straight into line buffers.
//! [`Client`] is a small general-purpose client, used by tests and tools.

mod client;
mod encode;
mod frame;
mod message;

pub use client::Client;
pub use client::ClientError;
pub use client::Incoming;
pub use client::IncomingRx;
pub use encode::error_response;
pub use encode::notification;
pub use encode::request;
pub use encode::response;
pub use frame::Frame;
pub use frame::FrameReader;
pub use message::Id;
pub use message::Kind;
pub use message::RawMessage;
pub use message::RpcError;
pub use message::code;
pub use message::params;
pub use message::peek_request_id;
