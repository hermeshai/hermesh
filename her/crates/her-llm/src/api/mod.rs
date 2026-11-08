//! Wire-protocol adapters. One adapter per protocol; providers and models are data.

pub mod anthropic;
#[cfg(test)]
mod body_snapshots;
pub(crate) mod http;
pub mod openai_chat;
pub mod openai_responses;
pub mod scripted;

use std::sync::Arc;
use std::time::Duration;

use crate::auth::Credentials;
use crate::model::Model;
use crate::stream::LlmStream;
use crate::types::Context;
use crate::types::RequestOptions;

/// A wire protocol. Implementations must follow the contract documented on
/// [`crate::stream::StreamEvent`].
pub trait WireApi: Send + Sync + 'static {
    fn id(&self) -> &'static str;
    fn stream(&self, req: WireRequest) -> LlmStream;
}

#[derive(Clone, Debug)]
pub struct Timeouts {
    pub connect: Duration,
    /// Until the response headers arrive.
    pub first_byte: Duration,
    /// Between two stream events.
    pub idle: Duration,
}

impl Default for Timeouts {
    fn default() -> Self {
        Self {
            connect: Duration::from_secs(10),
            first_byte: Duration::from_secs(60),
            idle: Duration::from_secs(300),
        }
    }
}

pub struct WireRequest {
    /// `base_url` and `compat` are already merged.
    pub model: Arc<Model>,
    pub credentials: Credentials,
    /// Already converted for the target model; shared across retries.
    pub context: Arc<Context>,
    pub options: RequestOptions,
    pub http: reqwest::Client,
    pub timeouts: Timeouts,
}
