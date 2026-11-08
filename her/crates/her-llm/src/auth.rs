//! Credential resolution. Keys never leave this module except inside `Credentials`.

use parking_lot::Mutex;
use std::collections::HashMap;
use std::fmt;
use std::sync::LazyLock;
use std::time::Duration;
use std::time::Instant;

use crate::credentials;
use crate::error::ErrorKind;
use crate::error::LlmError;

#[derive(Clone, Debug, PartialEq)]
pub enum AuthConfig {
    /// Credentials resolved by an in-process connection adapter, never from configuration.
    Managed,
    /// No authentication (local servers).
    None,
    /// API key: `value`, then the first non-empty variable in `env`.
    ApiKey { env: Vec<String>, value: Option<String> },
    /// Run a command and use its trimmed stdout as the key; cached for `ttl_secs`.
    Command { command: Vec<String>, ttl_secs: u64 },
}

impl AuthConfig {
    /// Environment variables that may hold a key for this provider.
    pub fn env_vars(&self) -> &[String] {
        match self {
            AuthConfig::ApiKey { env, .. } => env,
            _ => &[],
        }
    }

    /// Whether a key is available without running a command: the configuration, then the
    /// environment, then the saved keys (`credentials`).
    pub fn status(&self, provider: &str) -> AuthStatus {
        match self {
            AuthConfig::None => AuthStatus::NotRequired,
            AuthConfig::Managed => AuthStatus::Managed,
            AuthConfig::ApiKey { env, value } => {
                if value.as_deref().is_some_and(|v| !v.is_empty()) {
                    return AuthStatus::Configured("config".into());
                }
                for name in env {
                    if std::env::var(name).is_ok_and(|v| !v.trim().is_empty()) {
                        return AuthStatus::Configured(name.clone());
                    }
                }
                if credentials::get(provider).is_some() {
                    return AuthStatus::Stored;
                }
                AuthStatus::Missing(env.clone())
            }
            AuthConfig::Command { .. } => AuthStatus::Command,
        }
    }

    pub async fn resolve(&self, provider: &str) -> Result<Credentials, LlmError> {
        match self {
            AuthConfig::None => Ok(Credentials::default()),
            AuthConfig::Managed => Err(LlmError::new(ErrorKind::Auth, "connection authorizer required")),
            AuthConfig::ApiKey { env, value } => {
                if let Some(v) = value.as_ref().filter(|v| !v.is_empty()) {
                    return Ok(Credentials::with_key(v.clone()));
                }
                for name in env {
                    if let Ok(v) = std::env::var(name)
                        && !v.trim().is_empty()
                    {
                        return Ok(Credentials::with_key(v.trim().to_string()));
                    }
                }
                // The keychain read runs `security` once per process; keep it off the runtime.
                let saved = {
                    let provider = provider.to_string();
                    tokio::task::spawn_blocking(move || credentials::get(&provider)).await.ok().flatten()
                };
                if let Some(key) = saved {
                    return Ok(Credentials::with_key(key));
                }
                let hint = if env.is_empty() {
                    "configure api_key_env in models.toml".to_string()
                } else {
                    format!("set {}", env.join(" or "))
                };
                Err(LlmError::new(
                    ErrorKind::Auth,
                    format!("no API key for provider '{provider}': {hint}"),
                ))
            }
            AuthConfig::Command { command, ttl_secs } => {
                resolve_command(provider, command, Duration::from_secs(*ttl_secs)).await
            }
        }
    }
}

#[derive(Clone, Debug, PartialEq)]
pub enum AuthStatus {
    Managed,
    NotRequired,
    /// Where the key comes from (variable name or "config").
    Configured(String),
    /// A saved key (`auth/set`).
    Stored,
    Missing(Vec<String>),
    Command,
}

/// Command line → (key, fetched at).
type KeyCache = Mutex<HashMap<Vec<String>, (String, Instant)>>;

static COMMAND_CACHE: LazyLock<KeyCache> = LazyLock::new(|| Mutex::new(HashMap::new()));

async fn resolve_command(provider: &str, command: &[String], ttl: Duration) -> Result<Credentials, LlmError> {
    if let Some((key, at)) = COMMAND_CACHE.lock().get(command)
        && at.elapsed() < ttl
    {
        return Ok(Credentials::with_key(key.clone()));
    }
    let Some((program, args)) = command.split_first() else {
        return Err(LlmError::new(ErrorKind::Auth, format!("empty api_key_command for provider '{provider}'")));
    };
    let output = tokio::process::Command::new(program)
        .args(args)
        .stdin(std::process::Stdio::null())
        .output()
        .await
        .map_err(|e| LlmError::new(ErrorKind::Auth, format!("api_key_command for '{provider}' failed to start: {e}")))?;
    if !output.status.success() {
        return Err(LlmError::new(
            ErrorKind::Auth,
            format!("api_key_command for '{provider}' exited with {}", output.status),
        ));
    }
    let key = String::from_utf8_lossy(&output.stdout).trim().to_string();
    if key.is_empty() {
        return Err(LlmError::new(ErrorKind::Auth, format!("api_key_command for '{provider}' printed nothing")));
    }
    COMMAND_CACHE.lock().insert(command.to_vec(), (key.clone(), Instant::now()));
    Ok(Credentials::with_key(key))
}

/// Resolved credentials. `Debug` never prints the key.
#[derive(Clone, Default)]
pub struct Credentials {
    pub api_key: Option<String>,
    pub headers: std::collections::BTreeMap<String, String>,
}

impl Credentials {
    pub fn with_key(key: String) -> Self {
        Self { api_key: Some(key), headers: Default::default() }
    }
}

impl fmt::Debug for Credentials {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Credentials")
            .field("api_key", &self.api_key.as_deref().map(mask_key))
            .finish()
    }
}

/// `sk-…1234`
pub fn mask_key(key: &str) -> String {
    let chars: Vec<char> = key.chars().collect();
    if chars.len() <= 8 {
        return "****".to_string();
    }
    let head: String = chars[..3].iter().collect();
    let tail: String = chars[chars.len() - 4..].iter().collect();
    format!("{head}…{tail}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn masks_keys() {
        assert_eq!(mask_key("sk-1234567890abcd"), "sk-…abcd");
        assert_eq!(mask_key("short"), "****");
        let creds = Credentials::with_key("sk-1234567890abcd".into());
        assert!(!format!("{creds:?}").contains("567890"));
    }

    #[tokio::test]
    async fn missing_key_is_auth_error() {
        let auth = AuthConfig::ApiKey {
            env: vec!["HER_TEST_SURELY_UNSET_KEY".into()],
            value: None,
        };
        let err = auth.resolve("demo").await.unwrap_err();
        assert_eq!(err.kind, ErrorKind::Auth);
        assert!(err.message.contains("HER_TEST_SURELY_UNSET_KEY"));
    }
}
