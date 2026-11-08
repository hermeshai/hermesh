//! The ACP requests her reads, as minimal local types. The official schema crate is used
//! only by tests: linking it would also switch serde_json to insertion-ordered maps for the
//! whole program. Capability fields are read leniently, like the official types do.

use serde::Deserialize;
use serde_json::Value;
use std::path::PathBuf;

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct InitializeRequest {
    #[allow(dead_code)]
    pub protocol_version: Value,
    #[serde(default)]
    pub client_capabilities: Value,
}

impl InitializeRequest {
    /// Whether the client can run a terminal method (`clientCapabilities.auth.terminal`).
    pub fn can_run_terminal_auth(&self) -> bool {
        self.client_capabilities.pointer("/auth/terminal").and_then(Value::as_bool) == Some(true)
    }

    /// Whether `clientCapabilities.session.<name>` is present.
    pub fn session_capability(&self, name: &str) -> bool {
        self.client_capabilities
            .get("session")
            .and_then(|s| s.get(name))
            .is_some_and(|v| !v.is_null())
    }
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct NewSessionRequest {
    pub cwd: PathBuf,
    #[serde(default)]
    pub additional_directories: Vec<PathBuf>,
    #[serde(default)]
    pub mcp_servers: Vec<Value>,
}

/// `session/load` and `session/resume`.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct OpenSessionRequest {
    pub session_id: String,
    #[serde(default)]
    pub mcp_servers: Vec<Value>,
}

#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ListSessionsRequest {
    #[serde(default)]
    pub cwd: Option<PathBuf>,
    #[serde(default)]
    pub cursor: Option<String>,
}

/// `session/close`, `session/delete` and `session/cancel`.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionRequest {
    pub session_id: String,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PromptRequest {
    pub session_id: String,
    pub prompt: Vec<ContentBlock>,
}

#[derive(Debug, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ContentBlock {
    Text {
        text: String,
    },
    Image {
        data: String,
        #[serde(rename = "mimeType")]
        mime_type: String,
    },
    Audio {},
    ResourceLink {
        uri: String,
    },
    Resource {
        resource: EmbeddedResource,
    },
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Deserialize)]
#[serde(untagged)]
pub enum EmbeddedResource {
    Text { uri: String, text: String },
    Blob {},
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SetConfigOptionRequest {
    pub session_id: String,
    pub config_id: String,
    pub value: Value,
}

#[derive(Debug, Deserialize)]
pub struct RequestPermissionResponse {
    pub outcome: Outcome,
}

#[derive(Debug, Deserialize)]
#[serde(tag = "outcome", rename_all = "snake_case")]
pub enum Outcome {
    Cancelled,
    Selected {
        #[serde(rename = "optionId")]
        option_id: String,
    },
}

/// `authenticate`
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AuthenticateRequest {
    pub method_id: String,
}

#[cfg(test)]
mod tests {
    use super::*;
    use agent_client_protocol_schema::v1 as acp;
    use serde::de::DeserializeOwned;
    use serde_json::json;

    /// The sample must be valid for the official type and readable by ours.
    fn both<Official: DeserializeOwned, Ours: DeserializeOwned>(sample: Value) -> Ours {
        serde_json::from_value::<Official>(sample.clone()).expect("a valid ACP sample");
        serde_json::from_value::<Ours>(sample).expect("her reads it")
    }

    #[test]
    fn local_request_types_read_what_the_official_ones_accept() {
        let init: InitializeRequest = both::<acp::InitializeRequest, _>(json!({
            "protocolVersion": 1,
            "clientCapabilities": {"fs": {"readTextFile": true}, "terminal": true, "session": {"notices": {}}}
        }));
        assert!(init.session_capability("notices"));
        assert!(!init.session_capability("compaction"));
        let new: NewSessionRequest = both::<acp::NewSessionRequest, _>(json!({
            "cwd": "/w", "additionalDirectories": ["/x"], "mcpServers": []
        }));
        assert_eq!(new.additional_directories, vec![PathBuf::from("/x")]);
        let load: OpenSessionRequest =
            both::<acp::LoadSessionRequest, _>(json!({"sessionId": "s", "cwd": "/w", "mcpServers": []}));
        assert_eq!(load.session_id, "s");
        both::<acp::ResumeSessionRequest, OpenSessionRequest>(json!({"sessionId": "s", "cwd": "/w"}));
        both::<acp::ListSessionsRequest, ListSessionsRequest>(json!({"cwd": "/w", "cursor": "2"}));
        both::<acp::CloseSessionRequest, SessionRequest>(json!({"sessionId": "s"}));
        both::<acp::CancelNotification, SessionRequest>(json!({"sessionId": "s"}));
        let prompt: PromptRequest = both::<acp::PromptRequest, _>(json!({"sessionId": "s", "prompt": [
            {"type": "text", "text": "hi"},
            {"type": "image", "data": "AAAA", "mimeType": "image/png"},
            {"type": "resource_link", "uri": "file:///w/a.rs", "name": "a.rs"},
            {"type": "resource", "resource": {"uri": "file:///w/b.rs", "text": "fn b() {}"}},
            {"type": "audio", "data": "AAAA", "mimeType": "audio/wav"}
        ]}));
        assert!(matches!(prompt.prompt[3], ContentBlock::Resource { resource: EmbeddedResource::Text { .. } }));
        assert!(matches!(prompt.prompt[4], ContentBlock::Audio {}));
        let set: SetConfigOptionRequest = both::<acp::SetSessionConfigOptionRequest, _>(json!({
            "sessionId": "s", "configId": "permission_mode", "value": "manual"
        }));
        assert_eq!(set.value, json!("manual"));
        let answer: RequestPermissionResponse = both::<acp::RequestPermissionResponse, _>(json!({
            "outcome": {"outcome": "selected", "optionId": "approve"}
        }));
        assert!(matches!(answer.outcome, Outcome::Selected { ref option_id } if option_id == "approve"));
        let cancelled: RequestPermissionResponse =
            both::<acp::RequestPermissionResponse, _>(json!({"outcome": {"outcome": "cancelled"}}));
        assert!(matches!(cancelled.outcome, Outcome::Cancelled));
    }
}
