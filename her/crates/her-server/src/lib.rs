//! The protocol service: one process holds many sessions and many client connections.
//!
//! Each loaded session has a hub task that owns the order of its events, attaches and
//! operations. Every protocol is a projection of the kernel's events encoded in this process:
//! an event is serialized at most once per protocol, and native-protocol bytes are shared by
//! all connections attached to the session.

pub mod conn;
pub mod errors;
pub mod frame;
pub mod hub;
pub mod limits;
pub mod list;
pub mod memory;
pub mod mirror;
pub mod native;
pub mod server;
pub mod transport;
pub mod watch;

pub use conn::ConnHandle;
pub use conn::Encoder;
pub use conn::Outgoing;
pub use conn::Protocol;
pub use conn::run_connection;
pub use hub::AttachRequest;
pub use native::NativeProtocol;
pub use server::NewSession;
pub use server::ResumeOverrides;
pub use server::Server;
pub use server::ServerOptions;
pub use transport::UnixService;
pub use transport::default_socket_path;
pub use transport::serve_stdio;
pub use transport::serve_unix;

mod bots;
mod browser_host;
mod browser_status;
mod connections;
mod customize;
mod db;
mod decks;
mod desktop_status;
mod docs;
mod docx_host;
mod words;
mod files;
mod models;
mod peer;
mod routines;
mod schedule;
mod web_search;
