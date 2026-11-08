//! Which of Qoder's two inference routes a request takes. Every request goes to the App's
//! legacy chat, which takes bodies up to 50 MiB; the OpenAI-style gateway is only where a
//! request goes once the legacy chat cannot serve it. A conversation stays on one route, as the
//! provider's prompt cache is the route's own: moving between them would start it cold.
//!
//! The legacy chat is unavailable when it refuses a request in a way no retry changes, before
//! giving anything of an answer: the model is not in its catalog, the envelope or the stream is
//! not what it takes, or the endpoint is gone. A queue, a network failure, a server error or a
//! lapsed sign-in is not that: the kernel's retry waits it out on the same route. Once
//! unavailable, the legacy chat stays so until the catalog is read again.
use super::{legacy::Legacy, transport::Http};
use her_llm::{
    ErrorKind, LlmError, LlmStream, StreamEvent, WireApi, WireRequest, api::openai_chat,
};
use futures::StreamExt;
use std::collections::HashSet;

/// The gateway refuses a body over about 256 KiB with a bare 500 (measured 2026-11-03 on
/// `qfmodel`: 257,294 bytes of prompt went through, 261,835 failed, at any `context_length`).
/// When a request that large fails there with a server error, it is the size.
pub(crate) const HTTP_BODY_LIMIT: usize = 256 * 1024;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Path {
    /// The OpenAI-style model gateway (api2-v2 `model/v1`).
    Http,
    /// The App's own chat (api3 `agent_chat_generation`).
    Legacy,
}

/// A route forced for every request with `HER_QODER_ROUTE=http|legacy`, for diagnosis; a
/// forced legacy route is never left.
fn forced() -> Option<Path> {
    match std::env::var("HER_QODER_ROUTE").as_deref() {
        Ok("legacy") => Some(Path::Legacy),
        Ok("http") => Some(Path::Http),
        _ => None,
    }
}

/// Where the legacy chat could not serve, for the connection's life or until the catalog is
/// read again: every model, or the models it has no entry for.
#[derive(Default)]
pub(crate) struct Degraded {
    all: parking_lot::Mutex<Option<String>>,
    models: parking_lot::Mutex<HashSet<String>>,
}

impl Degraded {
    fn holds(&self, model: &str) -> bool {
        self.all.lock().is_some() || self.models.lock().contains(model)
    }

    fn record(&self, model: &str, error: &LlmError) {
        if error.provider_code.as_deref() == Some("qoder_legacy_unknown_model") {
            self.models.lock().insert(model.into());
        } else {
            *self.all.lock() = Some(
                error
                    .provider_code
                    .clone()
                    .unwrap_or_else(|| error.kind.to_string()),
            );
        }
    }

    /// A fresh catalog: the legacy chat is tried again.
    pub(crate) fn reset(&self) {
        *self.all.lock() = None;
        self.models.lock().clear();
    }
}

/// Whether a legacy failure means the legacy chat cannot serve this request at all.
fn unavailable(error: &LlmError) -> bool {
    error.provider_code.as_deref() == Some("qoder_legacy_unknown_model")
        || error.kind == ErrorKind::Protocol
        || matches!(error.status, Some(404 | 405 | 415))
}

/// The request as the gateway gets it, when the legacy chat is unavailable.
fn copy(req: &WireRequest) -> WireRequest {
    WireRequest {
        model: req.model.clone(),
        credentials: req.credentials.clone(),
        context: req.context.clone(),
        options: req.options.clone(),
        http: req.http.clone(),
        timeouts: req.timeouts.clone(),
    }
}

pub(crate) struct Route {
    pub(crate) legacy: Legacy,
    pub(crate) degraded: std::sync::Arc<Degraded>,
}

/// The gateway's stream, where a server error of a body over its limit is the size, so that
/// the kernel compacts instead of retrying a request that cannot pass.
fn gateway(req: WireRequest) -> LlmStream {
    let oversized =
        openai_chat::build_body(&req.model, &req.context, &req.options).len() > HTTP_BODY_LIMIT;
    let stream = Http.stream(req);
    if !oversized {
        return stream;
    }
    Box::pin(stream.map(|event| {
        match event {
            StreamEvent::Failed(mut message)
                if message
                    .error
                    .as_ref()
                    .is_some_and(|e| e.kind == ErrorKind::Server && e.status == Some(500)) =>
            {
                if let Some(error) = message.error.as_mut() {
                    error.kind = ErrorKind::ContextOverflow;
                    error.provider_code = Some("request_too_large".into());
                    error.message = format!(
                        "请求体超出 Qoder 模型网关约 256 KiB 的上限：{}",
                        error.message
                    );
                }
                StreamEvent::Failed(message)
            }
            other => other,
        }
    }))
}

impl WireApi for Route {
    fn id(&self) -> &'static str {
        "qoder"
    }

    fn stream(&self, req: WireRequest) -> LlmStream {
        let model = req.model.id.clone();
        match forced() {
            Some(Path::Http) => return gateway(req),
            Some(Path::Legacy) => return self.legacy.stream(req),
            None => {}
        }
        if self.degraded.holds(&model) {
            return gateway(req);
        }
        let fallback = copy(&req);
        let mut legacy = self.legacy.stream(req);
        let degraded = self.degraded.clone();
        Box::pin(async_stream::stream! {
            match legacy.next().await {
                // Nothing of an answer was given: the gateway answers this request instead,
                // and every later one, so the conversation does not move back and forth.
                Some(StreamEvent::Failed(message)) if message.error.as_deref().is_some_and(unavailable) => {
                    if let Some(error) = message.error.as_deref() {
                        degraded.record(&model, error);
                        tracing::warn!(%model, kind = %error.kind, code = ?error.provider_code, status = ?error.status, "qoder_legacy_unavailable_using_http");
                    }
                    let mut fallback = gateway(fallback);
                    while let Some(event) = fallback.next().await {
                        yield event;
                    }
                }
                Some(first) => {
                    drop(fallback);
                    yield first;
                    while let Some(event) = legacy.next().await {
                        yield event;
                    }
                }
                None => {}
            }
        })
    }
}
