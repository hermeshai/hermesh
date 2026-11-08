//! In-process authorization boundary. Product adapters live outside this crate.
use crate::{Credentials, LlmError};
use async_trait::async_trait;
use tokio_util::sync::CancellationToken;

/// A request-scoped route/credential pair; deliberately neither Debug nor Serialize.
pub struct Authorization {
    pub base_url: String,
    pub credentials: Credentials,
    pub transport: Option<std::sync::Arc<dyn crate::WireApi>>,
}

#[async_trait]
pub trait ConnectionAuthorizer: Send + Sync {
    /// Acquires a request lease (released when dropped), before any remote request.
    async fn begin(
        &self,
        provider: &str,
        model: &str,
        http: reqwest::Client,
    ) -> Result<Box<dyn ConnectionRequest>, LlmError>;
}

#[async_trait]
pub trait ConnectionRequest: Send + Sync {
    /// `rejected` is only true after one explicit 401, before any model content.
    async fn authorize(&self, rejected: bool) -> Result<Authorization, LlmError>;
    fn cancellation(&self) -> CancellationToken;
    /// Called for terminal model errors. Adapters may sanitize upstream diagnostics.
    fn failed(&self, error: &mut LlmError);
}
