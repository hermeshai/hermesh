//! Product behavior stays behind this boundary; the manager owns lifecycle and storage.
use crate::{Result, qwenwork::QwenWork};
use async_trait::async_trait;
use her_llm::connection::Authorization;
use her_protocol::api::ConnectionModel;
#[async_trait]
pub(crate) trait ProductAdapter: Send + Sync {
    async fn catalog(&mut self, http: &reqwest::Client) -> Result<Vec<ConnectionModel>>;
    async fn authorize(&mut self, http: &reqwest::Client, rejected: bool) -> Result<Authorization>;
}
#[async_trait]
impl ProductAdapter for QwenWork {
    async fn catalog(&mut self, http: &reqwest::Client) -> Result<Vec<ConnectionModel>> {
        QwenWork::catalog(self, http).await
    }
    async fn authorize(&mut self, http: &reqwest::Client, rejected: bool) -> Result<Authorization> {
        QwenWork::authorize(self, http, rejected).await
    }
}
pub(crate) fn name(adapter: &str) -> &'static str {
    match adapter {
        "qoder" => "Qoder",
        _ => "千问办公",
    }
}
/// The product's name by language tag, for clients that have an interface language; `name` is
/// for the rest. Qoder goes by one name everywhere.
pub(crate) fn names(adapter: &str) -> std::collections::BTreeMap<String, String> {
    match adapter {
        "qoder" => Default::default(),
        _ => [("zh", "千问办公"), ("en", "QwenWork")]
            .map(|(language, name)| (language.to_string(), name.to_string()))
            .into(),
    }
}
pub(crate) fn base(adapter: &str) -> &'static str {
    match adapter {
        "qoder" => "https://api2-v2.qoder.sh/model/v1",
        _ => "https://gateway.qwenwork.cn/v1",
    }
}

pub(crate) fn create(
    adapter: &str,
    id: String,
    identity: String,
    env: std::sync::Arc<dyn crate::platform::Environment>,
) -> Box<dyn ProductAdapter> {
    match adapter {
        "qoder" => Box::new(crate::qoder::Qoder::new(id, identity, env)),
        _ => Box::new(QwenWork::new(id, identity, env)),
    }
}
/// Both gateways expose effort levels. Qoder maps its controls in its own transport.
pub(crate) fn thinking_format(adapter: &str) -> Option<her_llm::model::ThinkingFormat> {
    match adapter {
        "qoder" => Some(her_llm::model::ThinkingFormat::Openai),
        _ => Some(her_llm::model::ThinkingFormat::Openai),
    }
}

pub(crate) fn supported(adapter: &str, model: &str) -> bool {
    adapter != "qoder" || crate::qoder::supports_model(model)
}

/// QwenWork's gateway answers 429 when the account's share of requests or tokens is used up for
/// the moment, in the words OpenAI uses for an unpaid account. Seen 2026-11-02: an account's
/// sub-agents failed with it while its other requests, at the same seconds, went through. So a
/// 429 of its is waited out and retried, unless it speaks of arrears.
pub(crate) fn reclassify(adapter: &str, error: &mut her_llm::LlmError) {
    const ARREARS: &[&str] = &["余额", "欠费", "充值", "arrear", "balance"];
    if adapter == "qoder"
        || error.status != Some(429)
        || error.kind != her_llm::ErrorKind::QuotaExceeded
    {
        return;
    }
    let text = format!(
        "{} {}",
        error.message,
        error.provider_code.as_deref().unwrap_or_default()
    )
    .to_lowercase();
    if !ARREARS.iter().any(|word| text.contains(word)) {
        error.kind = her_llm::ErrorKind::RateLimited;
    }
}

/// A gateway's error text without what may stand for the account: long runs of letters and
/// digits (tokens, session and request ids) become `…`.
pub(crate) fn scrub(text: &str) -> String {
    const MAX_CHARS: usize = 500;
    let secret = |run: &str| run.chars().count() >= 20 && run.chars().any(|c| c.is_ascii_digit());
    let mut out = String::new();
    let mut run = String::new();
    for c in text.chars().chain(std::iter::once(' ')) {
        if c.is_ascii_alphanumeric() || matches!(c, '-' | '_' | '.' | '=' | '+' | '/') {
            run.push(c);
            continue;
        }
        out.push_str(if secret(&run) { "…" } else { &run });
        run.clear();
        out.push(c);
    }
    out.pop();
    match out.char_indices().nth(MAX_CHARS) {
        Some((end, _)) => format!("{}…", &out[..end]),
        None => out,
    }
}

pub(crate) fn authentication_failure(_adapter: &str, error: &her_llm::LlmError) -> bool {
    error.kind == her_llm::ErrorKind::Auth
        && (error.status == Some(401)
            || matches!(
                error.provider_code.as_deref(),
                Some("connection_login_required" | "identity_changed")
            ))
}
