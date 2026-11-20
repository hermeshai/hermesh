//! Consent for conversational data leaving a Hermesh execution host.
use crate::{ErrorKind, LlmError, Model};
use parking_lot::RwLock;
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};
use tokio_util::sync::CancellationToken;

#[derive(Clone, Debug, Deserialize, Eq, JsonSchema, PartialEq, Serialize)]
pub struct Recipient {
    pub provider: String,
    pub endpoint: String,
}

impl Recipient {
    fn valid_endpoint(raw: &str) -> Option<String> {
        let url = reqwest::Url::parse(raw).ok()?;
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
        Some(url.as_str().trim_end_matches('/').to_string())
    }

    pub(crate) fn of(model: &Model) -> Option<Self> {
        Some(Self {
            provider: model.provider.clone(),
            endpoint: Self::valid_endpoint(&model.base_url)?,
        })
    }
}

#[derive(Deserialize, JsonSchema, Serialize)]
pub struct Status {
    pub recipient: Option<Recipient>,
    pub allowed: Option<Recipient>,
    pub revision: String,
}

#[derive(Deserialize, Serialize)]
struct StoredConsent {
    allowed: Recipient,
}

struct State {
    revision: String,
    grant: Option<(Recipient, CancellationToken)>,
}

pub(crate) struct Consent {
    path: Option<PathBuf>,
    state: RwLock<State>,
}

impl Default for Consent {
    fn default() -> Self {
        Self::new(None)
    }
}

fn revision() -> String {
    format!("{:032x}", rand::random::<u128>())
}

fn load_grant(path: &Path) -> Option<Recipient> {
    let raw = std::fs::read_to_string(path).ok()?;
    let stored: StoredConsent = serde_json::from_str(&raw).ok()?;
    if stored.allowed.provider.is_empty() {
        return None;
    }
    let endpoint = Recipient::valid_endpoint(&stored.allowed.endpoint)?;
    if endpoint != stored.allowed.endpoint {
        return None;
    }
    Some(stored.allowed)
}

fn save_grant(path: &Path, recipient: &Recipient) -> std::io::Result<()> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let data = serde_json::to_vec(&StoredConsent { allowed: recipient.clone() })
        .map_err(std::io::Error::other)?;
    let tmp = path.with_extension(format!("json.{}.tmp", revision()));
    std::fs::write(&tmp, data)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o600));
    }
    if let Err(err) = std::fs::rename(&tmp, path) {
        let _ = std::fs::remove_file(&tmp);
        return Err(err);
    }
    Ok(())
}

fn clear_grant(path: &Path) {
    let _ = std::fs::remove_file(path);
}

impl Consent {
    pub fn new(path: Option<PathBuf>) -> Self {
        let grant = path
            .as_deref()
            .and_then(load_grant)
            .map(|recipient| (recipient, CancellationToken::new()));
        Self {
            path,
            state: RwLock::new(State { revision: revision(), grant }),
        }
    }

    pub fn status(&self, recipient: Option<Recipient>) -> Status {
        let state = self.state.read();
        Status {
            recipient,
            allowed: state.grant.as_ref().map(|(recipient, _)| recipient.clone()),
            revision: state.revision.clone(),
        }
    }

    /// Compare with what the owner saw, including across revocations and restarts.
    pub fn allow(&self, recipient: Recipient, expected: &str) -> bool {
        let mut state = self.state.write();
        if state.revision != expected {
            return false;
        }
        if let Some(path) = &self.path
            && let Err(err) = save_grant(path, &recipient)
        {
            tracing::warn!(%err, path = %path.display(), "failed to persist AI sharing consent");
            return false;
        }
        if let Some((_, lease)) = state.grant.replace((recipient, CancellationToken::new())) {
            lease.cancel();
        }
        state.revision = revision();
        true
    }

    pub fn revoke(&self) {
        let mut state = self.state.write();
        if let Some((_, lease)) = state.grant.take() {
            lease.cancel();
        }
        if let Some(path) = &self.path {
            clear_grant(path);
        }
        state.revision = revision();
    }

    pub fn revoke_if(&self, matches: impl FnOnce(&Recipient) -> bool) {
        let mut state = self.state.write();
        if let Some((recipient, lease)) = &state.grant
            && matches(recipient)
        {
            lease.cancel();
            state.grant = None;
            if let Some(path) = &self.path {
                clear_grant(path);
            }
            state.revision = revision();
        }
    }

    /// A revoke that has returned cannot race a later transport poll.
    pub fn dispatch<T>(&self, lease: &CancellationToken, poll: impl FnOnce() -> T) -> Option<T> {
        let _state = self.state.read();
        (!lease.is_cancelled()).then(poll)
    }

    pub fn lease(&self, recipient: Option<&Recipient>) -> Result<CancellationToken, LlmError> {
        let state = self.state.read();
        match &state.grant {
            Some((granted, lease)) if Some(granted) == recipient => Ok(lease.clone()),
            _ => Err(blocked()),
        }
    }
}

pub(crate) fn blocked() -> LlmError {
    LlmError::new(ErrorKind::InvalidRequest, "AI sharing is blocked. Review the current recipient in Her’s settings.")
}
