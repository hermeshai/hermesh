//! Model access layer.
//!
//! Three concepts keep vendors pluggable: a wire protocol ([`WireApi`]) is code, while
//! providers and models ([`ProviderConfig`], [`Model`]) are data. Callers talk to
//! [`ModelRegistry`], which handles credentials, history conversion, retries and pricing.

pub mod api;
pub mod auth;
pub mod catalog;
pub mod credentials;
pub mod connection;
pub mod directory;
pub mod discovery;
pub mod error;
pub mod model;
pub mod public_catalog;
pub mod registry;
pub mod stream;
mod tls;
pub mod transform;
pub mod types;
pub mod usage;

/// The TLS setup her's own HTTP clients use: system roots, verified lazily.
pub use tls::client_config as tls_client_config;
pub use api::Timeouts;
pub use api::WireApi;
pub use api::WireRequest;
pub use auth::AuthConfig;
pub use auth::AuthStatus;
pub use auth::Credentials;
pub use auth::mask_key;
pub use catalog::Catalog;
pub use catalog::CatalogError;
pub use catalog::ProviderConfig;
pub use error::ErrorKind;
pub use error::LlmError;
pub use model::Compat;
pub use model::Modality;
pub use model::Model;
pub use model::ModelCost;
pub use registry::ListRefresh;
pub use registry::ModelRegistry;
pub use registry::RegistryOptions;
pub use registry::ResolveError;
pub use registry::ResolvedModel;
pub use registry::RetryPolicy;
pub use stream::LlmStream;
pub use stream::MessageAccumulator;
pub use stream::StreamEvent;
pub use types::*;
pub use usage::UsageTotals;
