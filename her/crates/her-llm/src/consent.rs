//! Consent for conversational data leaving a Hermesh execution host.
use crate::{ErrorKind, LlmError, Model};
use parking_lot::RwLock;
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use std::io::{self, Write};
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

fn save_grant(path: &Path, recipient: &Recipient) -> io::Result<()> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let data = serde_json::to_vec(&StoredConsent { allowed: recipient.clone() })
        .map_err(io::Error::other)?;
    let tmp = path.with_extension(format!("json.{}.tmp", revision()));
    let mut options = std::fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    std::os::unix::fs::OpenOptionsExt::mode(&mut options, 0o600);
    let mut file = options.open(&tmp)?;
    let written = file.write_all(&data).and_then(|()| file.sync_all());
    drop(file);
    let result = written.and_then(|()| std::fs::rename(&tmp, path));
    if result.is_err() {
        let _ = std::fs::remove_file(&tmp);
    }
    result
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
            allowed: state.grant.as_ref()
                .filter(|(_, lease)| !lease.is_cancelled())
                .map(|(recipient, _)| recipient.clone()),
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

    pub fn revoke(&self) -> io::Result<()> {
        self.revoke_locked(&mut self.state.write())
    }

    pub fn revoke_if(&self, matches: impl FnOnce(&Recipient) -> bool) -> io::Result<()> {
        let mut state = self.state.write();
        if state.grant.as_ref().is_some_and(|(recipient, _)| matches(recipient)) {
            self.revoke_locked(&mut state)?;
        }
        Ok(())
    }

    fn revoke_locked(&self, state: &mut State) -> io::Result<()> {
        // Block live inference even if disk cleanup fails. Keep the cancelled
        // grant until cleanup succeeds so switching recipients can retry it.
        if let Some((_, lease)) = &state.grant {
            lease.cancel();
        }
        state.revision = revision();
        if let Some(path) = &self.path {
            match std::fs::remove_file(path) {
                Ok(()) => (),
                Err(err) if err.kind() == io::ErrorKind::NotFound => (),
                Err(err) => return Err(err),
            }
        }
        state.grant = None;
        Ok(())
    }

    /// A revoke that has returned cannot race a later transport poll.
    pub fn dispatch<T>(&self, lease: &CancellationToken, poll: impl FnOnce() -> T) -> Option<T> {
        let _state = self.state.read();
        (!lease.is_cancelled()).then(poll)
    }

    pub fn lease(&self, recipient: Option<&Recipient>) -> Result<CancellationToken, LlmError> {
        let state = self.state.read();
        match &state.grant {
            Some((granted, lease)) if Some(granted) == recipient && !lease.is_cancelled() => Ok(lease.clone()),
            _ => Err(blocked()),
        }
    }
}

pub(crate) fn blocked() -> LlmError {
    LlmError::new(ErrorKind::InvalidRequest, "AI sharing is blocked. Review the current recipient in Her’s settings.")
}
