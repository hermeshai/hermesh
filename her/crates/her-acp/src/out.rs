//! Outgoing ACP messages, serialized straight from borrowed kernel data (no owned ACP types,
//! no copied strings). Tests check every shape against the official schema types.

use her_core::lang::say;
use bytes::Bytes;
use her_llm::ThinkingLevel;
use her_llm::UserContent;
use her_protocol::GrantAuthority;
use her_protocol::PermissionMode;
use her_protocol::SiteGrant;
use her_protocol::CompletionReason;
use her_protocol::api::CommandInfo;
use her_protocol::api::SessionInfo;
use her_server::Server;
use serde::Serialize;
use serde_json::Value;
use serde_json::json;
use std::path::Path;

pub const SESSION_UPDATE: &str = "session/update";
pub const REQUEST_PERMISSION: &str = "session/request_permission";

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct Notification<'a, U> {
    session_id: &'a str,
    update: U,
}

/// One `session/update` notification line.
pub fn update<U: Serialize>(session_id: &str, update: U) -> Bytes {
    Bytes::from(her_rpc::notification(SESSION_UPDATE, &Notification { session_id, update }))
}

#[derive(Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum Block<'a> {
    Text {
        text: &'a str,
    },
    Image {
        data: &'a str,
        #[serde(rename = "mimeType")]
        mime_type: &'a str,
    },
}

impl<'a> Block<'a> {
    pub fn from_user(content: &'a UserContent) -> Self {
        match content {
            UserContent::Text { text } => Block::Text { text },
            UserContent::Image { mime_type, data } => Block::Image { data, mime_type },
        }
    }
}

/// `user_message_chunk`, `agent_message_chunk` or `agent_thought_chunk`.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Chunk<'a> {
    pub session_update: &'static str,
    pub content: Block<'a>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message_id: Option<&'a str>,
}

#[derive(Serialize)]
pub struct LocationOut<'a> {
    pub path: &'a Path,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub line: Option<u32>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolCallOut<'a> {
    pub session_update: &'static str,
    pub tool_call_id: &'a str,
    pub title: &'a str,
    pub name: &'a str,
    pub kind: &'a str,
    pub status: &'static str,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub locations: Vec<LocationOut<'a>>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub content: Vec<ToolContent<'a>>,
    pub raw_input: &'a Value,
    #[serde(skip_serializing_if = "Option::is_none")]
    /// The tool's details, as stored.
    pub raw_output: Option<&'a her_llm::JsonText>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolCallUpdateOut<'a> {
    pub session_update: &'static str,
    pub tool_call_id: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub status: Option<&'static str>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub content: Option<Vec<ToolContent<'a>>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    /// The tool's details, as stored.
    pub raw_output: Option<&'a her_llm::JsonText>,
}

impl<'a> ToolCallUpdateOut<'a> {
    pub fn status(tool_call_id: &'a str, status: &'static str) -> Self {
        Self {
            session_update: "tool_call_update",
            tool_call_id,
            status: Some(status),
            content: None,
            raw_output: None,
        }
    }
}

#[derive(Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ToolContent<'a> {
    Content {
        content: Block<'a>,
    },
    Diff {
        path: &'a Path,
        #[serde(rename = "oldText", skip_serializing_if = "Option::is_none")]
        old_text: Option<&'a str>,
        #[serde(rename = "newText")]
        new_text: &'a str,
    },
}

impl<'a> ToolContent<'a> {
    pub fn text(text: &'a str) -> Self {
        ToolContent::Content {
            content: Block::Text { text },
        }
    }
}

#[derive(Serialize)]
pub struct Cost<'a> {
    pub amount: f64,
    pub currency: &'a str,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UsageOut<'a> {
    pub session_update: &'static str,
    pub used: u64,
    pub size: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cost: Option<Cost<'a>>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct NoticeOut<'a> {
    pub session_update: &'static str,
    pub severity: &'static str,
    pub title: &'a str,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CompactionOut<'a> {
    pub session_update: &'static str,
    pub compaction_id: &'a str,
    pub status: &'static str,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConfigUpdateOut {
    pub session_update: &'static str,
    pub config_options: Value,
}

/// `available_commands_update`: what typing `/` offers.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CommandsOut<'a> {
    pub session_update: &'static str,
    pub available_commands: Vec<CommandOut<'a>>,
}

#[derive(Serialize)]
pub struct CommandOut<'a> {
    pub name: &'a str,
    pub description: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub input: Option<CommandInput<'a>>,
}

/// Unstructured input: whatever follows the name.
#[derive(Serialize)]
pub struct CommandInput<'a> {
    pub hint: &'a str,
}

impl<'a> CommandsOut<'a> {
    pub fn new(commands: &'a [CommandInfo]) -> Self {
        Self {
            session_update: "available_commands_update",
            available_commands: commands
                .iter()
                .map(|command| CommandOut {
                    name: &command.name,
                    description: &command.description,
                    input: command.hint.as_deref().map(|hint| CommandInput { hint }),
                })
                .collect(),
        }
    }
}

pub fn stop_reason(reason: CompletionReason) -> &'static str {
    match reason {
        CompletionReason::Done | CompletionReason::Hook => "end_turn",
        CompletionReason::MaxTurns => "max_turn_requests",
        CompletionReason::OutputLimit => "max_tokens",
    }
}

/// The three session settings as ACP config options.
pub fn config_options(server: &Server, info: &SessionInfo) -> Value {
    let models = server.list_models().models;
    let mut model_options: Vec<Value> = models
        .iter()
        .filter(|m| (m.available && m.shown) || m.id == info.model)
        .map(|m| json!({ "value": m.id, "name": m.name }))
        .collect();
    if !models.iter().any(|m| m.id == info.model) {
        model_options.insert(0, json!({ "value": info.model, "name": info.model }));
    }
    // "Default" first (the provider decides), then the model's own levels (model-catalog §18).
    let levels = models.iter().find(|m| m.id == info.model).map(|m| m.thinking_levels.clone()).unwrap_or_default();
    let level_options: Vec<Value> = std::iter::once(ThinkingLevel::Default)
        .chain(levels)
        .map(|l| json!({ "value": l.as_str(), "name": thinking_label(l) }))
        .collect();
    let mode_options: Vec<Value> = PermissionMode::ALL
        .iter()
        .map(|m| json!({ "value": m.as_str(), "name": mode_label(*m) }))
        .collect();
    json!([
        { "id": "model", "name": say("Model", "模型"), "category": "model", "type": "select",
          "currentValue": info.model, "options": model_options },
        { "id": "thinking", "name": say("Thinking", "推理等级"), "category": "thought_level", "type": "select",
          "currentValue": info.thinking.as_str(), "options": level_options },
        { "id": "permission_mode", "name": say("Permissions", "权限模式"), "category": "mode", "type": "select",
          "currentValue": info.permission_mode.as_str(), "options": mode_options },
    ])
}

fn thinking_label(level: ThinkingLevel) -> &'static str {
    match level {
        ThinkingLevel::Default => say("Default", "默认"),
        ThinkingLevel::Off => say("Off", "不思考"),
        ThinkingLevel::On => say("On", "思考"),
        ThinkingLevel::Minimal => say("Minimal", "最少"),
        ThinkingLevel::Low => say("Low", "低"),
        ThinkingLevel::Medium => say("Medium", "中"),
        ThinkingLevel::High => say("High", "高"),
        ThinkingLevel::Xhigh => say("Extra high", "超高"),
        ThinkingLevel::Max => say("Max", "最高"),
    }
}

fn mode_label(mode: PermissionMode) -> &'static str {
    match mode {
        PermissionMode::Manual => say("Ask each time", "逐项确认"),
        PermissionMode::AcceptEdits => say("Accept edits", "自动接受编辑"),
        PermissionMode::BypassPermissions => say("Bypass permissions", "跳过权限确认"),
    }
}

/// Permission options for an approval. "Always" is offered only when approving for the
/// session means more than this call: a command rule, a switch of the permission mode, or a
/// site grant; each grant gets its own option, `approve_for_session:<index>`.
pub fn permission_options(rule: Option<&[String]>, mode: Option<PermissionMode>, grants: &[SiteGrant]) -> Value {
    let always = match (rule, mode) {
        (Some(prefix), _) if !prefix.is_empty() => Some(format!("{} {}", say("Always allow in this session:", "本会话总是允许"), prefix.join(" "))),
        (_, Some(mode)) => Some(format!("{} “{}”", say("Allow, and switch to", "允许，并切换到"), mode_label(mode))),
        _ => None,
    };
    let mut options = vec![json!({ "optionId": "approve", "name": say("Allow", "允许"), "kind": "allow_once" })];
    if let Some(always) = always {
        options.push(json!({ "optionId": "approve_for_session", "name": always, "kind": "allow_always" }));
    }
    for (index, grant) in grants.iter().enumerate() {
        let name = match (grant.authority, grant.origin.as_str()) {
            (GrantAuthority::Navigate, "*") => say("Open any site in this session", "本会话允许打开所有网站").to_string(),
            (GrantAuthority::Act, "*") => say("Allow any site in this session", "本会话允许所有网站").to_string(),
            (GrantAuthority::Navigate, origin) => format!("{} {origin}", say("Open in this session:", "本会话允许打开")),
            (GrantAuthority::Act, origin) => format!("{} {origin}", say("Allow in this session:", "本会话允许")),
            (GrantAuthority::Script, origin) => format!("{} {origin}", say("Allow scripts in this session:", "本会话允许运行脚本：")),
        };
        options.push(json!({ "optionId": format!("approve_for_session:{index}"), "name": name, "kind": "allow_always" }));
    }
    // Kept in the configuration, every site included (D-197, D-221).
    for (index, grant) in grants.iter().enumerate() {
        let name = match (grant.authority, grant.origin.as_str()) {
            // Scripts are allowed for the session only (browser-design §7.6).
            (GrantAuthority::Script, _) => continue,
            (_, "*") => say(
                "Always allow any site (saved; a page with injected instructions could steer actions on sites you are signed in to)",
                "总是允许所有网站（写入配置；风险：被注入指令的页面可能诱导操作你登录的网站）",
            )
            .to_string(),
            (GrantAuthority::Navigate, origin) => format!("{} {origin} ({})", say("Always open", "总是允许打开"), say("saved", "写入配置")),
            (GrantAuthority::Act, origin) => format!("{} {origin} ({})", say("Always allow", "总是允许"), say("saved", "写入配置")),
        };
        options.push(json!({ "optionId": format!("approve_always:{index}"), "name": name, "kind": "allow_always" }));
    }
    options.push(json!({ "optionId": "deny", "name": say("Deny", "拒绝"), "kind": "reject_once" }));
    Value::Array(options)
}
