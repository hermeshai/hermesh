//! `scripted`: replays prepared responses, for tests that must not touch the network.

use parking_lot::Mutex;
use serde_json::Value;
use std::collections::BTreeMap;
use std::collections::VecDeque;
use std::sync::Arc;
use std::time::Duration;

use crate::api::WireApi;
use crate::api::WireRequest;
use crate::auth::AuthConfig;
use crate::catalog::ModelToml;
use crate::catalog::ProviderConfig;
use crate::error::ErrorKind;
use crate::error::LlmError;
use crate::model::Compat;
use crate::model::Model;
use crate::stream::LlmStream;
use crate::stream::MessageAccumulator;
use crate::stream::StreamEvent;
use crate::types::Context;
use crate::types::Currency;
use crate::types::RequestOptions;
use crate::types::StopReason;
use crate::types::Usage;

pub const API_ID: &str = "scripted";
pub const PROVIDER_ID: &str = "test";
pub const MODEL_ID: &str = "scripted";

#[derive(Clone, Debug)]
pub enum ScriptStep {
    Text(String),
    Thinking(String),
    ToolCall { id: String, name: String, args: Value },
    /// Ends the response with a failure of this kind.
    Fail(ErrorKind, String),
    Sleep(Duration),
}

/// One prepared model response.
#[derive(Clone, Debug, Default)]
pub struct Script {
    pub steps: Vec<ScriptStep>,
    pub stop: Option<StopReason>,
    pub usage: Usage,
}

impl Script {
    pub fn text(text: impl Into<String>) -> Self {
        Self::default().then_text(text)
    }

    pub fn tool_call(id: impl Into<String>, name: impl Into<String>, args: Value) -> Self {
        Self::default().then_tool_call(id, name, args)
    }

    pub fn fail(kind: ErrorKind, message: impl Into<String>) -> Self {
        Self {
            steps: vec![ScriptStep::Fail(kind, message.into())],
            ..Default::default()
        }
    }

    pub fn then_text(mut self, text: impl Into<String>) -> Self {
        self.steps.push(ScriptStep::Text(text.into()));
        self
    }

    pub fn then_thinking(mut self, text: impl Into<String>) -> Self {
        self.steps.push(ScriptStep::Thinking(text.into()));
        self
    }

    pub fn then_tool_call(mut self, id: impl Into<String>, name: impl Into<String>, args: Value) -> Self {
        self.steps.push(ScriptStep::ToolCall {
            id: id.into(),
            name: name.into(),
            args,
        });
        self
    }

    pub fn then_sleep(mut self, duration: Duration) -> Self {
        self.steps.push(ScriptStep::Sleep(duration));
        self
    }

    pub fn with_stop(mut self, stop: StopReason) -> Self {
        self.stop = Some(stop);
        self
    }

    pub fn with_usage(mut self, input: u64, output: u64) -> Self {
        self.usage = Usage {
            input,
            output,
            ..Default::default()
        };
        self
    }

    /// Usage with `cache_read` of the `input` tokens served from the cache.
    pub fn with_cached_usage(mut self, input: u64, cache_read: u64, output: u64) -> Self {
        self.usage = Usage {
            input: input - cache_read,
            cache_read,
            output,
            ..Default::default()
        };
        self
    }
}

/// One request as the API received it.
#[derive(Clone, Debug)]
pub struct ScriptedCall {
    /// `provider/model`.
    pub model: String,
    pub options: RequestOptions,
    pub context: Context,
}

/// Decides a response from the request, for scripts that depend on what the model is shown.
pub type Responder = Box<dyn Fn(&Context) -> Option<Script> + Send + Sync>;

#[derive(Default)]
pub struct ScriptedApi {
    scripts: Mutex<VecDeque<Script>>,
    responder: Mutex<Option<Responder>>,
    requests: Mutex<Vec<Context>>,
    calls: Mutex<Vec<ScriptedCall>>,
}

impl ScriptedApi {
    pub fn new() -> Arc<Self> {
        Arc::new(Self::default())
    }

    pub fn push(&self, script: Script) {
        self.scripts.lock().push_back(script);
    }

    /// Answers requests that find no prepared script left.
    pub fn respond_with(&self, responder: impl Fn(&Context) -> Option<Script> + Send + Sync + 'static) {
        *self.responder.lock() = Some(Box::new(responder));
    }

    /// Contexts received so far, in order.
    pub fn requests(&self) -> Vec<Context> {
        self.requests.lock().clone()
    }

    /// Requests received so far with their model and options, in order.
    pub fn calls(&self) -> Vec<ScriptedCall> {
        self.calls.lock().clone()
    }

    pub fn remaining(&self) -> usize {
        self.scripts.lock().len()
    }

    /// The entry of model `scripted`, for tests to adjust.
    pub fn entry() -> ModelToml {
        ModelToml {
            name: Some("Scripted".into()),
            context_window: Some(128_000),
            max_output_tokens: Some(8_192),
            ..ModelToml::new(MODEL_ID)
        }
    }

    /// Provider `test` with model `scripted`, wired to this API.
    pub fn provider() -> ProviderConfig {
        Self::provider_with(vec![Self::entry()])
    }

    /// Provider `test` with the given models, wired to this API.
    pub fn provider_with(entries: Vec<ModelToml>) -> ProviderConfig {
        ProviderConfig {
            plan: None,
            id: PROVIDER_ID.into(),
            name: "Test".into(),
            names: Default::default(),
            api: API_ID.into(),
            base_url: "scripted://local".into(),
            auth: AuthConfig::None,
            headers: BTreeMap::new(),
            currency: Currency::Usd,
            compat: Compat::default(),
            listing: crate::catalog::Listing { entries, ..Default::default() },
        }
    }

    /// Model `scripted`, as a provider of this API resolves it.
    pub fn model() -> Model {
        Self::provider().build_model(None, &Self::entry())
    }
}

impl WireApi for ScriptedApi {
    fn id(&self) -> &'static str {
        API_ID
    }

    fn stream(&self, req: WireRequest) -> LlmStream {
        self.requests.lock().push(req.context.as_ref().clone());
        self.calls.lock().push(ScriptedCall {
            model: req.model.reference(),
            options: req.options.clone(),
            context: req.context.as_ref().clone(),
        });
        let script = self.scripts.lock().pop_front().or_else(|| {
            self.responder
                .lock()
                .as_ref()
                .and_then(|respond| respond(req.context.as_ref()))
        });
        let model = req.model.clone();
        Box::pin(async_stream::stream! {
            let mut acc = MessageAccumulator::for_model(&model);
            let Some(script) = script else {
                yield StreamEvent::Failed(acc.fail(LlmError::new(ErrorKind::Unknown, "no scripted response left")));
                return;
            };
            let mut index = 0;
            for step in script.steps {
                match step {
                    ScriptStep::Text(text) => {
                        let ev = StreamEvent::TextDelta { index, text };
                        acc.apply(&ev);
                        yield ev;
                        index += 1;
                    }
                    ScriptStep::Thinking(text) => {
                        let ev = StreamEvent::ThinkingDelta { index, text };
                        acc.apply(&ev);
                        yield ev;
                        index += 1;
                    }
                    ScriptStep::ToolCall { id, name, args } => {
                        let start = StreamEvent::ToolCallStart { index, id, name };
                        acc.apply(&start);
                        yield start;
                        let args = StreamEvent::ToolCallArgs { index, json: args.to_string() };
                        acc.apply(&args);
                        yield args;
                        index += 1;
                    }
                    ScriptStep::Fail(kind, message) => {
                        yield StreamEvent::Failed(acc.fail(LlmError::new(kind, message)));
                        return;
                    }
                    ScriptStep::Sleep(duration) => tokio::time::sleep(duration).await,
                }
            }
            acc.usage = script.usage;
            yield StreamEvent::Done(acc.finish(script.stop.unwrap_or(StopReason::Stop)));
        })
    }
}
