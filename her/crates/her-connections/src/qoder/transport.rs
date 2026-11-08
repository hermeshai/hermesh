//! Qoder App's model-server envelope. The core OpenAI protocol stays product-neutral.
use her_llm::{LlmStream, WireApi, WireRequest, api::openai_chat::OpenAiChatApi};
use serde_json::json;
use std::sync::Arc;

pub(super) struct Http;

impl WireApi for Http {
    fn id(&self) -> &'static str {
        "openai-chat"
    }

    fn stream(&self, mut req: WireRequest) -> LlmStream {
        if !super::capabilities::verified_temperature(&req.model.id, req.options.temperature) {
            let mut error = her_llm::LlmError::new(
                her_llm::ErrorKind::InvalidRequest,
                "当前 Qoder Kimi 连接仅支持默认温度或 1.0，请移除自定义温度或设为 1.0",
            );
            error.provider_code = Some("qoder_temperature_unverified".into());
            let message = her_llm::MessageAccumulator::for_model(&req.model).fail(error);
            return Box::pin(futures::stream::once(async {
                her_llm::StreamEvent::Failed(message)
            }));
        }
        if super::capabilities::blocked_control(&req.model.id, req.options.thinking) {
            let mut error = her_llm::LlmError::new(
                her_llm::ErrorKind::InvalidRequest,
                "Qoder 当前路由未能关闭此模型的思考，请使用默认或其他思考档位",
            );
            error.provider_code = Some("qoder_thinking_off_unverified".into());
            let message = her_llm::MessageAccumulator::for_model(&req.model).fail(error);
            return Box::pin(futures::stream::once(async {
                her_llm::StreamEvent::Failed(message)
            }));
        }
        // Verified against the installed Qoder App 0.4.11 worker's Dkn envelope.
        // Without the App business envelope, Auto returns "All backends failed".
        // Until callers provide a turn scope, use independent remote request scopes.
        // Never expose local session IDs, paths or user-supplied metadata to this envelope.
        let request_id = uuid::Uuid::new_v4().to_string();
        let session_id = uuid::Uuid::new_v4().to_string();
        let task_id = uuid::Uuid::new_v4().to_string();
        // UI controls are resolved against this account's catalog; extra_body cannot
        // override them or inject a different request envelope.
        req.options.extra_body.clear();
        let thinking = req.model.resolve_thinking(req.options.thinking);
        use her_llm::ThinkingLevel;
        match thinking {
            ThinkingLevel::Off => {
                req.options
                    .extra_body
                    .insert("reasoning_effort".into(), json!("none"));
                req.options
                    .extra_body
                    .insert("enable_thinking".into(), json!(false));
            }
            level if level.is_effort() => {
                req.options
                    .extra_body
                    .insert("reasoning_effort".into(), json!(level.as_str()));
                req.options
                    .extra_body
                    .insert("enable_thinking".into(), json!(true));
            }
            _ => {}
        }
        // Avoid a second interpretation by the generic OpenAI serializer.
        req.options.thinking = ThinkingLevel::Default;
        req.options
            .extra_body
            .insert("context_length".into(), json!(req.model.context_window));
        req.options.extra_body.insert(
            "metadata".into(),
            json!({
                "context": {
                    "request_id": request_id,
                    "request_set_id": task_id,
                    "session_id": session_id,
                    "task_id": task_id,
                    "client_type": "10"
                },
                "business": {"product": "app", "type": "agent", "id": task_id}
            }),
        );
        // Only this request's snapshot is changed, not the shared model/catalog.
        let model = Arc::make_mut(&mut req.model);
        model.compat.max_tokens_field = Some(her_llm::model::MaxTokensField::MaxTokens);
        model.compat.stream_usage = Some(true);
        model.headers.insert("X-Request-ID".into(), request_id);
        model.headers.insert("X-Session-ID".into(), session_id);
        OpenAiChatApi::stream_with_events(req, super::sse::decode)
    }
}
