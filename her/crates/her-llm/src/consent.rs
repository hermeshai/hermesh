//! Consent for conversational data leaving a Hermesh execution host.
use crate::{ErrorKind, LlmError, Model};
use parking_lot::RwLock;
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use tokio_util::sync::CancellationToken;

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
pub struct Recipient {
    pub provider: String,
    pub endpoint: String,
}

impl Recipient {
    pub(crate) fn of(model: &Model) -> Option<Self> {
        let url = reqwest::Url::parse(&model.base_url).ok()?;
        let loopback = matches!(url.host_str(), Some("localhost" | "127.0.0.1" | "[::1]"));
        if !(url.scheme() == "https" || url.scheme() == "http" && loopback)
            || url.host_str().is_none()
            || !url.username().is_empty()
            || url.password().is_some()
            || url.query().is_some()
            || url.fragment().is_some()
        {
            return None;
        }
        Some(Self {
            provider: model.provider.clone(),
            endpoint: url.as_str().trim_end_matches('/').to_string(),
        })
    }
}

#[derive(Deserialize, JsonSchema, Serialize)]
pub struct Status {
    pub recipient: Option<Recipient>,
    pub allowed: Option<Recipient>,
    pub revision: String,
}

struct State {
    revision: String,
    grant: Option<(Recipient, CancellationToken)>,
}

pub(crate) struct Consent(RwLock<State>);

impl Default for Consent {
    fn default() -> Self {
        Self(RwLock::new(State { revision: revision(), grant: None }))
    }
}

fn revision() -> String {
    format!("{:032x}", rand::random::<u128>())
}

impl Consent {
    pub fn status(&self, recipient: Option<Recipient>) -> Status {
        let state = self.0.read();
        Status {
            recipient,
            allowed: state.grant.as_ref().map(|(recipient, _)| recipient.clone()),
            revision: state.revision.clone(),
        }
    }

    /// Compare with what the owner saw, including across revocations and restarts.
    pub fn allow(&self, recipient: Recipient, expected: &str) -> bool {
        let mut state = self.0.write();
        if state.revision != expected {
            return false;
        }
        if let Some((_, lease)) = state.grant.replace((recipient, CancellationToken::new())) {
            lease.cancel();
        }
        state.revision = revision();
        true
    }

    pub fn revoke(&self) {
        let mut state = self.0.write();
        if let Some((_, lease)) = state.grant.take() {
            lease.cancel();
        }
        state.revision = revision();
    }

    pub fn revoke_if(&self, matches: impl FnOnce(&Recipient) -> bool) {
        let mut state = self.0.write();
        if let Some((recipient, lease)) = &state.grant
            && matches(recipient)
        {
            lease.cancel();
            state.grant = None;
            state.revision = revision();
        }
    }

    pub fn lease(&self, recipient: Option<&Recipient>) -> Result<CancellationToken, LlmError> {
        let state = self.0.read();
        match &state.grant {
            Some((granted, lease)) if Some(granted) == recipient => Ok(lease.clone()),
            _ => Err(blocked()),
        }
    }
}

pub(crate) fn blocked() -> LlmError {
    LlmError::new(ErrorKind::InvalidRequest, "AI sharing is blocked. Review the current recipient in Her’s settings.")
}
