//! Finding out how to talk to a service from its address alone (docs/kernel/custom-endpoint-design.md
//! §4): which protocols it speaks, under which prefixes, which header takes the key, and what
//! models it lists. Only requests that cost nothing are sent: model lists, and POSTs with an
//! empty body, which a service refuses while it checks the body, before any model runs.

use futures::future::join_all;
use serde::Serialize;
use serde_json::Value;
use std::net::IpAddr;
use std::time::Duration;

use crate::discovery::ListedModel;
use crate::model::AuthHeader;

const PROBE_TIMEOUT: Duration = Duration::from_secs(10);
/// A probe's answer is an error message or a page: more than this is not one.
const PROBE_BODY: usize = 64 * 1024;
const LIST_BODY: usize = 32 * 1024 * 1024;
const ANTHROPIC_VERSION: &str = "2023-06-01";

/// What to probe.
#[derive(Clone, Debug, Default)]
pub struct ProbeInput {
    pub base_url: String,
    pub api_key: Option<String>,
    /// Use only this protocol: skip finding out which ones the service speaks.
    pub api: Option<String>,
}

/// A protocol the service speaks.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct FoundProtocol {
    pub api: String,
    pub base_url: String,
    /// The header the key went in; `None` without a key.
    pub auth_header: Option<AuthHeader>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ListShape {
    Openai,
    Anthropic,
    Ollama,
}

/// The model list the service gave.
#[derive(Clone, Debug)]
pub struct FoundList {
    /// The prefix the list was under: `{prefix}/models`.
    pub base_url: String,
    pub shape: ListShape,
    pub auth_header: Option<AuthHeader>,
    pub models: Vec<ListedModel>,
}

#[derive(Clone, Debug, Default)]
pub struct ProbeReport {
    /// The address, tidied (§4.1).
    pub base_url: String,
    /// Protocols found, Chat Completions first.
    pub protocols: Vec<FoundProtocol>,
    /// Protocols that could not be told: the service answers every path alike (§4.4).
    pub unsure: Vec<String>,
    pub list: Option<FoundList>,
    /// A local server whose details endpoint was found: `ollama`, `lmstudio` or `llamacpp`.
    pub details: Option<String>,
    /// `http://` to an address that is not local: the key travels unencrypted.
    pub insecure: bool,
    /// Steps that did not work, for the person.
    pub problems: Vec<(String, String)>,
}

#[derive(Clone, Debug, PartialEq, Eq, thiserror::Error)]
pub enum ProbeError {
    #[error("{0}")]
    InvalidUrl(String),
    #[error("cannot reach {0}")]
    Unreachable(String),
    #[error("the certificate of {0} is not valid")]
    Tls(String),
    #[error("no OpenAI- or Anthropic-compatible interface was found at this address")]
    NotAModelService,
    #[error("this service needs an API key")]
    KeyRequired,
    #[error("the key was not accepted: {0}")]
    KeyRejected(String),
}

impl ProbeError {
    /// The `data.reason` of the protocol error.
    pub fn reason(&self) -> &'static str {
        match self {
            ProbeError::InvalidUrl(_) => "invalid_url",
            ProbeError::Unreachable(_) => "unreachable",
            ProbeError::Tls(_) => "tls",
            ProbeError::NotAModelService => "not_a_model_service",
            ProbeError::KeyRequired => "key_required",
            ProbeError::KeyRejected(_) => "key_rejected",
        }
    }
}

/// An address as a service's base: scheme, host and path, no query.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Address {
    /// `scheme://host[:port]`
    pub root: String,
    /// Without a trailing `/`; empty for the root.
    pub path: String,
    pub host: String,
    pub https: bool,
}

impl Address {
    pub fn url(&self) -> String {
        format!("{}{}", self.root, self.path)
    }
}

/// Whether a host is this machine or a private network, by its name or address alone.
pub fn is_local_host(host: &str) -> bool {
    let host = host.trim_start_matches('[').trim_end_matches(']');
    if host == "localhost" || host.ends_with(".localhost") || host.ends_with(".local") {
        return true;
    }
    host.parse::<IpAddr>().is_ok_and(is_private_ip)
}

fn is_private_ip(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(v4) => v4.is_loopback() || v4.is_private() || v4.is_link_local(),
        IpAddr::V6(v6) => {
            v6.is_loopback() || (v6.segments()[0] & 0xfe00) == 0xfc00 || (v6.segments()[0] & 0xffc0) == 0xfe80
        }
    }
}

/// Endpoint paths people paste with the address.
const PASTED_PATHS: &[&str] = &["/chat/completions", "/completions", "/responses", "/messages", "/models"];

/// Tidies an address (§4.1): a scheme when it has none, no trailing `/`, no pasted endpoint path.
pub fn normalize(given: &str) -> Result<Address, ProbeError> {
    let given = given.trim();
    if given.is_empty() {
        return Err(ProbeError::InvalidUrl("the address is empty".into()));
    }
    let with_scheme = if given.contains("://") {
        given.to_string()
    } else {
        let host = given.split(['/', ':']).next().unwrap_or_default();
        let scheme = if is_local_host(host) { "http" } else { "https" };
        format!("{scheme}://{given}")
    };
    let url = reqwest::Url::parse(&with_scheme).map_err(|e| ProbeError::InvalidUrl(format!("not an address: {e}")))?;
    if !matches!(url.scheme(), "http" | "https") {
        return Err(ProbeError::InvalidUrl("use an http:// or https:// address".into()));
    }
    if url.query().is_some() || url.fragment().is_some() {
        return Err(ProbeError::InvalidUrl("leave out the part after ? or #".into()));
    }
    if !url.username().is_empty() || url.password().is_some() {
        return Err(ProbeError::InvalidUrl("put the key in the key field, not in the address".into()));
    }
    let host = url.host_str().ok_or_else(|| ProbeError::InvalidUrl("the address has no host".into()))?.to_ascii_lowercase();
    let root = match url.port() {
        Some(port) => format!("{}://{host}:{port}", url.scheme()),
        None => format!("{}://{host}", url.scheme()),
    };
    let mut path = url.path().trim_end_matches('/').to_string();
    for pasted in PASTED_PATHS {
        if let Some(rest) = path.strip_suffix(pasted) {
            path = rest.trim_end_matches('/').to_string();
            break;
        }
    }
    Ok(Address { root, path, host, https: url.scheme() == "https" })
}

/// Whether a path ends with a version segment, as `/v1`, `/v4` or `/api/v1` do.
fn ends_with_version(path: &str) -> bool {
    path.rsplit('/').next().and_then(|last| last.strip_prefix('v')).is_some_and(|n| !n.is_empty() && n.bytes().all(|b| b.is_ascii_digit()))
}

/// Prefixes for the model list, Chat Completions and Responses, in order (§4.2).
pub fn openai_prefixes(a: &Address) -> Vec<String> {
    let mut out = Vec::new();
    let mut add = |p: String| {
        if !out.contains(&p) {
            out.push(p);
        }
    };
    if ends_with_version(&a.path) {
        add(a.url());
    } else {
        add(format!("{}/v1", a.url()));
        add(a.url());
    }
    if a.path.is_empty() {
        for common in ["/openai/v1", "/api/v1", "/inference/v1", "/v1/openai"] {
            add(format!("{}{common}", a.root));
        }
    }
    out
}

/// Prefixes for Messages, whose adapter adds `/v1/messages` (§4.2).
pub fn messages_prefixes(a: &Address) -> Vec<String> {
    let r = a.url().strip_suffix("/v1").map(str::to_string).unwrap_or_else(|| a.url());
    let mut out = Vec::new();
    for p in [r.clone(), format!("{r}/anthropic"), format!("{}/anthropic", a.root)] {
        if !out.contains(&p) {
            out.push(p);
        }
    }
    out
}

/// What one request was answered with.
#[derive(Clone, Debug)]
struct Answer {
    status: Option<u16>,
    json: Option<Value>,
    /// A body that is neither JSON nor a page, kept for what it says (`Authentication Fails`).
    plain: Option<String>,
    /// The connection failed: why, and whether it was the certificate.
    failure: Option<(String, bool)>,
}

impl Answer {
    fn text(&self) -> String {
        match &self.json {
            Some(Value::Object(map)) => map
                .get("error")
                .and_then(|e| e.get("message").or(Some(e)))
                .or_else(|| map.get("message"))
                .map(|v| v.as_str().map(str::to_string).unwrap_or_else(|| v.to_string()))
                .unwrap_or_default(),
            Some(other) => other.to_string(),
            None => self.plain.clone().unwrap_or_default(),
        }
        .chars()
        .take(500)
        .collect()
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Verdict {
    /// The interface is there: it refused the empty body.
    Present,
    /// The key or its header was not accepted.
    Auth,
    Absent,
    /// The service failed or did not answer.
    Unknown,
}

/// What an answer says about an interface (§4.4). Messages wants an Anthropic-shaped error, or an
/// OpenAI-shaped one that speaks of its fields, so a gateway's generic 400 for unknown paths is
/// not taken for it.
fn judge(answer: &Answer, messages: bool) -> Verdict {
    let Some(status) = answer.status else { return Verdict::Unknown };
    match status {
        401 | 403 => Verdict::Auth,
        404 | 405 => Verdict::Absent,
        429 => Verdict::Present,
        500..=599 => Verdict::Unknown,
        _ => match &answer.json {
            None => Verdict::Absent,
            Some(body) if messages && matches!(status, 400 | 422) => {
                let anthropic = body.get("type").and_then(Value::as_str) == Some("error");
                // The path itself says "messages": only a field named in the error counts.
                let text = body.to_string().to_ascii_lowercase().replace("/v1/messages", "").replace("/messages", "");
                let fields = ["model", "messages", "max_tokens"].iter().any(|f| text.contains(f));
                if anthropic || fields { Verdict::Present } else { Verdict::Absent }
            }
            Some(_) if (200..300).contains(&status) || matches!(status, 400 | 422) => Verdict::Present,
            Some(_) => Verdict::Unknown,
        },
    }
}

fn with_key(request: reqwest::RequestBuilder, key: Option<&str>, header: AuthHeader) -> reqwest::RequestBuilder {
    match key {
        Some(key) => header.apply(request, key),
        None => request,
    }
}

async fn ask(request: reqwest::RequestBuilder, limit: usize) -> Answer {
    let response = match request.timeout(PROBE_TIMEOUT).send().await {
        Ok(r) => r,
        Err(e) => {
            let text = format!("{e:?}").to_ascii_lowercase();
            let tls = text.contains("certificate") || text.contains("tls") || text.contains("handshake");
            return Answer { status: None, json: None, plain: None, failure: Some((e.to_string(), tls)) };
        }
    };
    let status = response.status().as_u16();
    let html = response
        .headers()
        .get(reqwest::header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .is_some_and(|t| t.contains("text/html"));
    let body = crate::discovery::read_limited(response, limit).await.unwrap_or_default();
    let json: Option<Value> = if html { None } else { serde_json::from_slice(&body).ok() };
    let plain = (json.is_none() && !html).then(|| String::from_utf8_lossy(&body).trim().to_string()).filter(|t| !t.is_empty());
    Answer { status: Some(status), json, plain, failure: None }
}

/// Header orders to try: the protocol's usual one first.
const OPENAI_HEADERS: &[AuthHeader] = &[AuthHeader::Bearer, AuthHeader::XApiKey, AuthHeader::ApiKey];
const ANTHROPIC_HEADERS: &[AuthHeader] = &[AuthHeader::XApiKey, AuthHeader::Bearer];

/// Asks with each header in turn while the answer is "not accepted"; returns the last answer and
/// the header it came with. Without a key, one request.
async fn ask_with_headers(
    make: impl Fn() -> reqwest::RequestBuilder,
    key: Option<&str>,
    headers: &[AuthHeader],
    limit: usize,
    messages: bool,
) -> (Answer, AuthHeader) {
    let mut first: Option<(Answer, AuthHeader)> = None;
    for header in headers.iter().copied().take(if key.is_some() { headers.len() } else { 1 }) {
        let answer = ask(with_key(make(), key, header), limit).await;
        if judge(&answer, messages) != Verdict::Auth {
            return (answer, header);
        }
        first.get_or_insert((answer, header));
    }
    // Refused whichever way: the usual header's answer is about the key itself; the others' are
    // often "no key provided", from a service that never reads them.
    first.expect("at least one header")
}

/// The models in a list answer, and its shape.
fn parse_list(answer: &Answer) -> Option<(ListShape, Vec<ListedModel>)> {
    if answer.status != Some(200) {
        return None;
    }
    let body = answer.json.as_ref()?;
    let shape = if body.get("models").is_some_and(Value::is_array) {
        ListShape::Ollama
    } else if body.get("has_more").is_some() || body.get("first_id").is_some() {
        ListShape::Anthropic
    } else if body.get("data").is_some_and(Value::is_array) || body.is_array() {
        ListShape::Openai
    } else {
        return None;
    };
    let mut models = Vec::new();
    crate::discovery::parse_page(body, &mut models).ok()?;
    Some((shape, models))
}

/// Probes a service (§4.3–§4.5). `http` must not follow redirects: the key stays with the host
/// the person gave.
pub async fn probe(http: &reqwest::Client, input: &ProbeInput) -> Result<ProbeReport, ProbeError> {
    let address = normalize(&input.base_url)?;
    let key = input.api_key.as_deref().map(str::trim).filter(|k| !k.is_empty());
    let mut report = ProbeReport { base_url: address.url(), ..Default::default() };
    report.insecure = !address.https && !is_local_host(&address.host) && !resolves_locally(&address.host).await;
    let openai = openai_prefixes(&address);
    let anthropic = messages_prefixes(&address);
    let mut answers: Vec<Answer> = Vec::new();

    // 1. The model list: OpenAI-shaped first, then Anthropic-shaped for Messages-only services.
    let mut list_auth_refused: Option<Answer> = None;
    for prefix in &openai {
        let url = format!("{prefix}/models");
        let (answer, header) = ask_with_headers(|| http.get(&url).header(reqwest::header::ACCEPT, "application/json"), key, OPENAI_HEADERS, LIST_BODY, false).await;
        if let Some((shape, models)) = parse_list(&answer) {
            report.list = Some(FoundList { base_url: prefix.clone(), shape, auth_header: key.map(|_| header), models });
            break;
        }
        if judge(&answer, false) == Verdict::Auth {
            list_auth_refused.get_or_insert_with(|| answer.clone());
        }
        answers.push(answer);
    }
    if report.list.is_none() {
        let prefix = &anthropic[0];
        let url = format!("{prefix}/v1/models?limit=1000");
        let (answer, header) = ask_with_headers(
            || http.get(&url).header(reqwest::header::ACCEPT, "application/json").header("anthropic-version", ANTHROPIC_VERSION),
            key,
            ANTHROPIC_HEADERS,
            LIST_BODY,
            false,
        )
        .await;
        if let Some((_, models)) = parse_list(&answer) {
            report.list = Some(FoundList { base_url: format!("{prefix}/v1"), shape: ListShape::Anthropic, auth_header: key.map(|_| header), models });
        } else {
            answers.push(answer);
        }
    }

    // 2–3. The protocols, each prefix with its control path.
    let list_prefix = report.list.as_ref().filter(|l| l.shape != ListShape::Anthropic).map(|l| l.base_url.clone());
    let chat_prefixes: Vec<String> = match &list_prefix {
        Some(p) => vec![p.clone()],
        None => openai.clone(),
    };
    let forced = input.api.as_deref();
    let marker: String = format!("her-probe-{:08x}", rand::random::<u32>());
    let control = |prefix: String| {
        let marker = marker.clone();
        async move {
            let url = format!("{prefix}/{marker}");
            let (answer, _) = ask_with_headers(|| http.post(&url).json(&serde_json::json!({})), key, OPENAI_HEADERS, PROBE_BODY, false).await;
            (prefix, judge(&answer, false))
        }
    };
    let mut prefixes: Vec<String> = chat_prefixes.clone();
    for p in &anthropic {
        if !prefixes.contains(p) {
            prefixes.push(p.clone());
        }
    }
    let controls: Vec<(String, Verdict)> = join_all(prefixes.into_iter().map(control)).await;
    let trusted = |prefix: &str| controls.iter().any(|(p, v)| p == prefix && *v == Verdict::Absent);
    let try_openai = |path: &'static str| {
        let prefixes = chat_prefixes.clone();
        async move {
            let mut results = Vec::new();
            for prefix in prefixes {
                let url = format!("{prefix}{path}");
                let (answer, header) = ask_with_headers(|| http.post(&url).json(&serde_json::json!({})), key, OPENAI_HEADERS, PROBE_BODY, false).await;
                let verdict = judge(&answer, false);
                results.push((prefix, verdict, header, answer));
                if verdict == Verdict::Present {
                    break;
                }
            }
            results
        }
    };
    let try_messages = async {
        let mut results = Vec::new();
        for prefix in anthropic.clone() {
            let url = format!("{prefix}/v1/messages");
            let (answer, header) = ask_with_headers(
                || http.post(&url).header("anthropic-version", ANTHROPIC_VERSION).json(&serde_json::json!({})),
                key,
                ANTHROPIC_HEADERS,
                PROBE_BODY,
                true,
            )
            .await;
            let verdict = judge(&answer, true);
            results.push((prefix, verdict, header, answer));
            if verdict == Verdict::Present {
                break;
            }
        }
        results
    };
    let (chat, responses, messages) = tokio::join!(try_openai("/chat/completions"), try_openai("/responses"), try_messages);
    let mut auth_refused = list_auth_refused;
    for (api, results) in [("openai-chat", &chat), ("openai-responses", &responses), ("anthropic-messages", &messages)] {
        let found = results.iter().find(|(prefix, verdict, _, _)| *verdict == Verdict::Present && trusted(prefix));
        // Where even the control path answers, only a 404 or 405 says anything.
        let untrusted =
            results.iter().any(|(prefix, _, _, answer)| !trusted(prefix) && answer.status.is_some_and(|s| !matches!(s, 404 | 405)));
        if let Some((prefix, _, header, _)) = found {
            report.protocols.push(FoundProtocol { api: api.to_string(), base_url: prefix.clone(), auth_header: key.map(|_| *header) });
        } else if untrusted {
            report.unsure.push(api.to_string());
        }
        for (_, verdict, _, answer) in results {
            if *verdict == Verdict::Auth && auth_refused.is_none() {
                auth_refused = Some(answer.clone());
            }
            answers.push(answer.clone());
        }
    }
    // Nothing answered as there, but something refused the key: that is the answer, whatever the
    // list did (a public list says nothing about the key) and however the paths answer (§4.4).
    if report.protocols.is_empty()
        && let Some(refused) = &auth_refused
    {
        return Err(match key {
            Some(_) => ProbeError::KeyRejected(refused.text()),
            None => ProbeError::KeyRequired,
        });
    }
    // A service that answers every path alike, with a list: Chat Completions at the list's prefix.
    if !report.protocols.iter().any(|p| p.api == "openai-chat")
        && report.unsure.iter().any(|a| a == "openai-chat")
        && let Some(list) = report.list.as_ref().filter(|l| l.shape != ListShape::Anthropic)
    {
        report.protocols.insert(0, FoundProtocol { api: "openai-chat".into(), base_url: list.base_url.clone(), auth_header: list.auth_header });
        report.unsure.retain(|a| a != "openai-chat");
    }
    if let Some(api) = forced {
        let base = match api {
            "anthropic-messages" => anthropic[0].clone(),
            _ => list_prefix.clone().unwrap_or_else(|| chat_prefixes[0].clone()),
        };
        let usual = if api == "anthropic-messages" { AuthHeader::XApiKey } else { AuthHeader::Bearer };
        let found = report.protocols.iter().find(|p| p.api == api).cloned();
        report.protocols = vec![found.unwrap_or(FoundProtocol { api: api.to_string(), base_url: base, auth_header: key.map(|_| usual) })];
        report.unsure.clear();
    }
    // Chat Completions first: it is the default protocol.
    report.protocols.sort_by_key(|p| match p.api.as_str() {
        "openai-chat" => 0,
        "anthropic-messages" => 1,
        _ => 2,
    });

    if report.protocols.is_empty() {
        if report.list.is_none()
            && let Some((message, tls)) = answers.iter().find_map(|a| a.failure.clone())
            && answers.iter().all(|a| a.failure.is_some())
        {
            return Err(if tls { ProbeError::Tls(address.host.clone()) } else { ProbeError::Unreachable(format!("{}: {message}", address.host)) });
        }
        if report.list.is_none() {
            return Err(ProbeError::NotAModelService);
        }
    }
    if report.list.is_none() {
        report.problems.push(("list".into(), "the service gave no model list: name its models".into()));
    }

    // 4. A local server's details endpoint.
    if is_local_host(&address.host) || report.list.as_ref().is_some_and(|l| l.models.iter().any(|m| m.owner.as_deref() == Some("llamacpp"))) {
        report.details = local_details(http, &address.root).await;
    }
    Ok(report)
}

/// Whether every address a host name resolves to is local or private: an intranet name.
async fn resolves_locally(host: &str) -> bool {
    match tokio::net::lookup_host((host, 80)).await {
        Ok(addresses) => {
            let addresses: Vec<_> = addresses.collect();
            !addresses.is_empty() && addresses.iter().all(|a| is_private_ip(a.ip()))
        }
        Err(_) => false,
    }
}

/// Which local server answers at `root`: Ollama, LM Studio or llama.cpp.
async fn local_details(http: &reqwest::Client, root: &str) -> Option<String> {
    let get = |path: &'static str| {
        let url = format!("{root}{path}");
        async move { ask(http.get(url), PROBE_BODY).await }
    };
    let (ollama, lmstudio, llamacpp) = tokio::join!(get("/api/version"), get("/api/v1/models"), get("/props"));
    let ok = |a: &Answer| a.status == Some(200) && a.json.is_some();
    if ok(&ollama) && ollama.json.as_ref().is_some_and(|j| j.get("version").is_some()) {
        return Some("ollama".into());
    }
    if ok(&lmstudio)
        && lmstudio.json.as_ref().and_then(|j| j.get("models").or_else(|| j.get("data"))).and_then(Value::as_array).is_some_and(|items| {
            items.iter().any(|i| i.get("max_context_length").is_some() || i.get("key").is_some())
        })
    {
        return Some("lmstudio".into());
    }
    if ok(&llamacpp) && llamacpp.json.as_ref().is_some_and(|j| j.get("default_generation_settings").is_some()) {
        return Some("llamacpp".into());
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn addresses_are_tidied() {
        let a = normalize(" https://GW.example.com/v1/chat/completions/ ").unwrap();
        assert_eq!((a.root.as_str(), a.path.as_str()), ("https://gw.example.com", "/v1"));
        assert_eq!(normalize("localhost:11434").unwrap().url(), "http://localhost:11434");
        assert_eq!(normalize("192.168.1.20:8000/v1").unwrap().url(), "http://192.168.1.20:8000/v1");
        assert_eq!(normalize("llm.example.com").unwrap().url(), "https://llm.example.com");
        assert!(normalize("https://x.example.com/v1?api-version=1").is_err());
        assert!(normalize("https://user:pass@x.example.com").is_err());
        assert!(normalize("ftp://x.example.com").is_err());
        assert!(normalize("").is_err());
    }

    #[test]
    fn candidate_prefixes() {
        let p = |u: &str| openai_prefixes(&normalize(u).unwrap());
        let m = |u: &str| messages_prefixes(&normalize(u).unwrap());
        assert_eq!(p("https://gw.example.com")[..2], ["https://gw.example.com/v1", "https://gw.example.com"]);
        assert!(p("https://gw.example.com").contains(&"https://gw.example.com/openai/v1".to_string()));
        assert_eq!(p("https://openrouter.ai/api/v1"), ["https://openrouter.ai/api/v1"]);
        assert_eq!(p("https://open.bigmodel.cn/api/paas/v4"), ["https://open.bigmodel.cn/api/paas/v4"]);
        assert_eq!(m("https://openrouter.ai/api/v1")[0], "https://openrouter.ai/api");
        assert_eq!(m("https://api.moonshot.cn/v1")[..2], ["https://api.moonshot.cn", "https://api.moonshot.cn/anthropic"]);
        assert!(m("https://bedrock-runtime.us-east-1.amazonaws.com/openai/v1")
            .contains(&"https://bedrock-runtime.us-east-1.amazonaws.com/anthropic".to_string()));
    }

    #[test]
    fn local_hosts() {
        for host in ["localhost", "127.0.0.1", "10.1.2.3", "172.20.0.5", "192.168.0.9", "[::1]", "box.local"] {
            assert!(is_local_host(host), "{host}");
        }
        for host in ["api.openai.com", "8.8.8.8", "172.32.0.1"] {
            assert!(!is_local_host(host), "{host}");
        }
    }

    fn answer(status: u16, json: Option<Value>) -> Answer {
        Answer { status: Some(status), json, plain: None, failure: None }
    }

    #[test]
    fn answers_are_judged() {
        use serde_json::json;
        let err = || Some(json!({"error": {"message": "model is required"}}));
        assert_eq!(judge(&answer(400, err()), false), Verdict::Present);
        assert_eq!(judge(&answer(422, err()), false), Verdict::Present);
        assert_eq!(judge(&answer(401, None), false), Verdict::Auth, "a plain-text 401 is still a 401");
        assert_eq!(judge(&answer(404, err()), false), Verdict::Absent);
        assert_eq!(judge(&answer(200, None), false), Verdict::Absent, "a web page is no interface");
        assert_eq!(judge(&answer(429, None), false), Verdict::Present);
        assert_eq!(judge(&answer(502, err()), false), Verdict::Unknown);
        let anthropic = Some(json!({"type": "error", "error": {"type": "invalid_request_error", "message": "x"}}));
        assert_eq!(judge(&answer(400, anthropic), true), Verdict::Present);
        assert_eq!(judge(&answer(400, Some(json!({"error": {"message": "Invalid URL (POST /v1/messages)"}}))), true), Verdict::Absent);
        assert_eq!(judge(&answer(400, Some(json!({"error": {"message": "Model name not specified"}}))), true), Verdict::Present);
    }
}
