//! Qoder's legacy chat, the route its desktop App infers on: `agent_chat_generation` on
//! api3.qoder.sh, signed with the COSY envelope and sent in the `Encode=1` body codec. Its
//! request body may be up to 50 MiB, where the OpenAI-style route refuses about 256 KiB.
//! Self-contained: the kernel sees an ordinary `WireApi` and stream events.
//! Protocol reference: crates/her-connections/licenses/README.md.
mod body;
mod codec;
mod queue;
mod reply;
#[cfg(test)]
mod tests;

use super::cosy;
use her_llm::{
    ErrorKind, LlmError, LlmStream, MessageAccumulator, StreamEvent, WireApi, WireRequest,
};
use serde_json::{Value, json};
use std::{collections::HashMap, sync::Arc};

pub(crate) use queue::Queue;

pub(crate) const BASE: &str = "https://api3.qoder.sh";
const CATALOG_PATH: &str = "/algo/api/v2/model/list?Encode=1";
const CHAT_PATH: &str = "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1";

/// The catalog's entries by model, shared by the adapter, which fills it whenever it reads the
/// catalog, and by its legacy requests, which read it once themselves when it is empty.
pub(crate) type Targets = Arc<parking_lot::RwLock<HashMap<String, Target>>>;

/// The enabled entries of a catalog response.
pub(crate) fn targets_of(catalog: &Value) -> HashMap<String, Target> {
    catalog["app"]
        .as_array()
        .into_iter()
        .flatten()
        .filter(|m| m["enable"] == true)
        .filter_map(|m| Some((m["key"].as_str()?.to_owned(), Target::from_entry(m)?)))
        .collect()
}

/// What a legacy request needs of a model's catalog entry.
#[derive(Clone, Debug, Default)]
pub(crate) struct Target {
    key: String,
    display_name: String,
    format: String,
    is_vl: bool,
    pub(crate) is_reasoning: bool,
    source: String,
    outer_provider: Option<String>,
    max_input_tokens: u64,
    /// Declared context tiers in tokens, ascending; the default one apart.
    tiers: Vec<u64>,
    default_tier: Option<u64>,
    pub(crate) efforts: Vec<String>,
}

impl Target {
    /// From one entry of the catalog; `None` for one without a key of its own: Qoder's
    /// worker would send it as "auto".
    pub(crate) fn from_entry(entry: &Value) -> Option<Target> {
        let key = entry["key"].as_str()?.trim();
        if key.is_empty() || matches!(key, "default") {
            return None;
        }
        let number = |v: &Value| {
            v.as_u64()
                .or_else(|| v.as_str()?.parse().ok())
                .filter(|n| *n > 0)
        };
        let mut tiers = Vec::new();
        let mut default_tier = None;
        for tier in entry["context_config"]
            .as_object()
            .into_iter()
            .flat_map(|m| m.values())
        {
            if let Some(tokens) = number(&tier["token_count"]) {
                tiers.push(tokens);
                if tier["is_default"] == true {
                    default_tier = Some(tokens);
                }
            }
        }
        tiers.sort_unstable();
        tiers.dedup();
        let thinking = &entry["thinking_config"];
        let efforts = entry
            .get("efforts")
            .or_else(|| thinking.get("efforts"))
            .or_else(|| thinking.get("enabled")?.get("efforts"));
        let efforts = match efforts {
            Some(Value::Object(map)) => map.keys().cloned().collect(),
            Some(Value::Array(list)) => list
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect(),
            _ => Vec::new(),
        };
        Some(Target {
            key: key.into(),
            display_name: entry["display_name"]
                .as_str()
                .filter(|s| !s.is_empty())
                .unwrap_or(key)
                .into(),
            format: entry["format"].as_str().unwrap_or("openai").into(),
            is_vl: entry["is_vl"] == true,
            is_reasoning: entry["is_reasoning"] == true,
            source: entry["source"]
                .as_str()
                .filter(|s| !s.is_empty())
                .unwrap_or("system")
                .into(),
            outer_provider: entry["outer_provider"]
                .as_str()
                .filter(|s| !s.is_empty())
                .map(str::to_owned),
            max_input_tokens: number(&entry["max_input_tokens"]).unwrap_or(200_000),
            tiers,
            default_tier,
            efforts,
        })
    }

    /// The tier a request sized for `window` tokens goes out on: the default one when it
    /// holds it, else the smallest that does, else the largest. A larger tier may cost more.
    pub(crate) fn tier_for(&self, window: u64) -> Option<u64> {
        if let Some(tier) = self.default_tier.filter(|t| *t >= window) {
            return Some(tier);
        }
        self.tiers
            .iter()
            .copied()
            .find(|t| *t >= window)
            .or_else(|| self.tiers.last().copied())
    }

    /// The `model_config` Qoder's App sends: the entry's own fields, never the entry whole.
    fn model_config(&self, thinking: bool) -> Value {
        let mut config = json!({
            "key": self.key,
            "display_name": self.display_name,
            "format": self.format,
            "is_vl": self.is_vl,
            "is_reasoning": self.is_reasoning && thinking,
            "api_key": "",
            "url": "",
            "source": self.source,
            "max_input_tokens": self.max_input_tokens,
        });
        match &self.outer_provider {
            Some(provider) => config["outer_provider"] = json!(provider),
            None => config["model"] = json!(""),
        }
        config
    }
}

/// The legacy route of one authorization: the account it signs for, the catalog entries and
/// the account's queue, which outlives the authorization.
pub(crate) struct Legacy {
    pub(crate) base: String,
    pub(crate) uid: String,
    pub(crate) machine: String,
    pub(crate) targets: Targets,
    pub(crate) queue: Arc<Queue>,
}

/// Reads the catalog for the legacy route's entries, as the adapter reads it.
async fn read_targets(
    http: &reqwest::Client,
    signer: &queue::Signer,
) -> Option<HashMap<String, Target>> {
    let url = format!("{}{CATALOG_PATH}", signer.base);
    let headers = cosy::headers(&url, &signer.uid, &signer.token, &signer.machine).ok()?;
    let response = http
        .get(&url)
        .headers(headers)
        .timeout(std::time::Duration::from_secs(30))
        .send()
        .await
        .ok()?;
    if !response.status().is_success() {
        tracing::warn!(
            status = response.status().as_u16(),
            "qoder_legacy_catalog_unavailable"
        );
        return None;
    }
    let value: Value = response.json().await.ok()?;
    Some(targets_of(&value)).filter(|t| !t.is_empty())
}

/// A queued task's attempt ends here; the kernel's retry waits the queue's delay.
fn queued(acc: &MessageAccumulator, retry_after: Option<f64>) -> StreamEvent {
    let mut error = LlmError::new(
        ErrorKind::Overloaded,
        "Qoder 正在为该模型排队，稍后自动重试",
    );
    error.provider_code = Some("qoder_queued".into());
    error.retry_after_ms = Some(queue::pause(retry_after).as_millis() as u64);
    StreamEvent::Failed(acc.fail(error))
}

fn fail(acc: &MessageAccumulator, kind: ErrorKind, code: &str, message: &str) -> StreamEvent {
    let mut error = LlmError::new(kind, message);
    error.provider_code = Some(code.into());
    StreamEvent::Failed(acc.fail(error))
}

impl WireApi for Legacy {
    fn id(&self) -> &'static str {
        "qoder-legacy"
    }

    fn stream(&self, req: WireRequest) -> LlmStream {
        let url = format!("{}{CHAT_PATH}", self.base);
        let (base, uid, machine, queue, targets) = (
            self.base.clone(),
            self.uid.clone(),
            self.machine.clone(),
            self.queue.clone(),
            self.targets.clone(),
        );
        Box::pin(async_stream::stream! {
            let mut acc = MessageAccumulator::for_model(&req.model);
            let Some(token) = req.credentials.api_key.clone() else {
                yield fail(&acc, ErrorKind::Auth, "connection_login_required", "Qoder 授权缺失");
                return;
            };
            let signer = queue::Signer { base, uid: uid.clone(), token: token.clone(), machine: machine.clone() };
            if targets.read().is_empty() {
                // A catalog that cannot be read now is retried, not taken for one without the model.
                let Some(read) = read_targets(&req.http, &signer).await else {
                    yield fail(&acc, ErrorKind::Server, "qoder_legacy_catalog_unavailable", "暂时无法读取 Qoder 模型目录，稍后自动重试");
                    return;
                };
                *targets.write() = read;
            }
            let target = targets.read().get(&req.model.id).cloned();
            let Some(target) = target else {
                yield fail(&acc, ErrorKind::InvalidRequest, "qoder_legacy_unknown_model", "Qoder 目录中没有该模型，无法使用 legacy 通道");
                return;
            };
            let ids = body::ids(&req);
            // Tasks that are over leave the queue, as the App leaves it: one given up (the
            // kernel stopped retrying, or the task was stopped) and one its session moved on from.
            for place in queue.over(&ids.session, &ids.task) {
                let (http, signer) = (req.http.clone(), signer.clone());
                tokio::spawn(async move { queue::finish(&http, &signer, &place).await });
            }
            // A task waiting in the queue asks where it stands before it is sent again.
            if let Some(place) = queue.place(&ids.task).filter(|p| p.waiting) {
                match queue::status(&req.http, &signer, &place).await {
                    Some(state) if !state.queued => tokio::time::sleep(queue::pause(state.retry_after)).await,
                    Some(state) => {
                        yield queued(&acc, state.retry_after);
                        return;
                    }
                    None if !queue.status_failed(&ids.task) => {
                        yield queued(&acc, None);
                        return;
                    }
                    None => {}
                }
            }
            let mut sends = 0;
            let mut request;
            let response = loop {
                sends += 1;
                request = uuid::Uuid::new_v4().to_string();
                let plain = body::build(&req, &target, &ids, &request).to_string();
                let wire = codec::encode(&plain);
                let mut headers = match cosy::signed(&url, &uid, &token, &machine, &wire, cosy::Kind::Chat) {
                    Ok(headers) => headers,
                    Err(_) => {
                        yield fail(&acc, ErrorKind::Protocol, "qoder_envelope_invalid", "无法构造 Qoder 请求签名");
                        return;
                    }
                };
                for (name, value) in [
                    ("Content-Type", "application/json"),
                    ("Cache-Control", "no-cache"),
                    ("Accept-Encoding", "identity"),
                    ("X-Model-Key", target.key.as_str()),
                    ("X-Model-Source", target.source.as_str()),
                    ("X-Request-ID", request.as_str()),
                    ("X-Session-ID", ids.session.as_str()),
                ] {
                    if let Ok(value) = reqwest::header::HeaderValue::from_str(value) {
                        headers.insert(name, value);
                    }
                }
                let sent = req.http.post(&url).headers(headers).body(wire).send();
                let response = match tokio::time::timeout(req.timeouts.first_byte, sent).await {
                    Ok(Ok(response)) => response,
                    Ok(Err(err)) => {
                        yield StreamEvent::Failed(acc.fail(LlmError::from_reqwest(&err)));
                        return;
                    }
                    Err(_) => {
                        yield StreamEvent::Failed(acc.fail(LlmError::new(ErrorKind::Timeout, "Qoder 未在时限内响应")));
                        return;
                    }
                };
                if response.status().is_success() {
                    break response;
                }
                let status = response.status().as_u16();
                let text: String = response.text().await.unwrap_or_default().chars().take(reply::LINE_LIMIT).collect();
                let error = reply::failure(status, &text);
                if error.provider_code.as_deref() != Some("qoder_queued") {
                    yield StreamEvent::Failed(acc.fail(error));
                    return;
                }
                let state = queue::state_of(&text).unwrap_or_default();
                queue.enter(&ids.task, &target.key, state.queue_type.clone(), &request, &ids.session);
                // Asked at once, as the App asks: a queue that is ready lets the chat go now.
                if sends < 2
                    && let Some(place) = queue.place(&ids.task)
                    && let Some(now) = queue::status(&req.http, &signer, &place).await
                    && !now.queued
                {
                    tokio::time::sleep(queue::pause(now.retry_after)).await;
                    continue;
                }
                yield queued(&acc, state.retry_after);
                return;
            };
            queue.admitted(&ids.task);
            let mut reply = reply::Reply::new(&req.context.tools);
            let mut response = response;
            let mut pending: Vec<u8> = Vec::new();
            let mut done = false;
            let mut refused = None;
            'read: loop {
                let chunk = match tokio::time::timeout(req.timeouts.idle, response.chunk()).await {
                    Ok(Ok(Some(chunk))) => chunk,
                    Ok(Ok(None)) => break,
                    Ok(Err(err)) => {
                        yield StreamEvent::Failed(acc.fail(LlmError::from_reqwest(&err)));
                        return;
                    }
                    Err(_) => {
                        yield StreamEvent::Failed(acc.fail(LlmError::new(ErrorKind::Timeout, "model stream timed out")));
                        return;
                    }
                };
                pending.extend_from_slice(&chunk);
                while let Some(end) = pending.iter().position(|b| *b == b'\n') {
                    let line: Vec<u8> = pending.drain(..=end).collect();
                    let line = String::from_utf8_lossy(&line);
                    match reply.line(line.trim_end_matches(['\n', '\r'])) {
                        reply::Step::Events(events) => {
                            for event in events {
                                acc.apply(&event);
                                yield event;
                            }
                        }
                        reply::Step::Done => {
                            done = true;
                            break 'read;
                        }
                        reply::Step::Refused(status, text) => {
                            refused = Some((status, text));
                            break 'read;
                        }
                    }
                }
                if pending.len() > reply::LINE_LIMIT {
                    yield fail(&acc, ErrorKind::Protocol, "sse_event_too_large", "Qoder 返回了无法识别的模型流");
                    return;
                }
            }
            if refused.is_none() && !done && !pending.is_empty() {
                let line = String::from_utf8_lossy(&pending).into_owned();
                match reply.line(line.trim_end_matches(['\n', '\r'])) {
                    reply::Step::Events(events) => {
                        for event in events {
                            acc.apply(&event);
                            yield event;
                        }
                    }
                    reply::Step::Done => done = true,
                    reply::Step::Refused(status, text) => refused = Some((status, text)),
                }
            }
            if let Some((status, text)) = refused {
                let error = reply::failure(status, &text);
                if error.provider_code.as_deref() == Some("qoder_queued") && !acc.has_content() {
                    let state = queue::state_of(&text).unwrap_or_default();
                    queue.enter(&ids.task, &target.key, state.queue_type, &request, &ids.session);
                    yield queued(&acc, state.retry_after);
                } else {
                    yield StreamEvent::Failed(acc.fail(error));
                }
                return;
            }
            for event in reply.flush() {
                acc.apply(&event);
                yield event;
            }
            reply::apply_usage(&mut acc, &reply);
            if reply.nameless() > 0 && !acc.has_content() {
                yield fail(&acc, ErrorKind::Protocol, "qoder_nameless_tool_call", "Qoder 返回了没有名称的工具调用");
                return;
            }
            if !done && reply.finish.is_none() && !acc.has_content() {
                yield fail(&acc, ErrorKind::Protocol, "missing_terminal_marker", "Qoder 的模型流在给出内容前结束");
                return;
            }
            // Thinking goes back in the field it came in, with the turns after it.
            acc.set_missing_thinking_signatures("reasoning_content");
            let stop = reply.stop();
            let message = acc.finish(stop);
            if message.tool_calls().iter().any(|call| call.arguments_error.is_some()) {
                yield fail(&acc, ErrorKind::Protocol, "invalid_tool_call", "Qoder 返回的工具调用参数无法解析");
                return;
            }
            // An answer without tool calls ends the turn: its queue lease goes back, before the
            // answer is given, so that a process that ends with it still releases it.
            if stop != her_llm::StopReason::ToolUse
                && let Some(place) = queue.end(&ids.task)
            {
                queue::finish(&req.http, &signer, &place).await;
            }
            yield StreamEvent::Done(message);
        })
    }
}
