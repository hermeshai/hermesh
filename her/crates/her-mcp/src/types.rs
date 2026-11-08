//! The parts of the MCP schema the client uses. Unknown fields are ignored; malformed entries
//! are reported one at a time instead of failing a whole list.

use serde::Deserialize;
use serde_json::Value;

/// Offered in `initialize`.
pub const LATEST_PROTOCOL_VERSION: &str = "2025-11-25";
/// Versions a server may answer with.
pub const SUPPORTED_PROTOCOL_VERSIONS: [&str; 4] = ["2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"];

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct InitializeResult {
    pub protocol_version: String,
    #[serde(default)]
    pub capabilities: ServerCapabilities,
    #[serde(default)]
    pub server_info: Option<Implementation>,
    #[serde(default)]
    pub instructions: Option<String>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct ServerCapabilities {
    #[serde(default)]
    pub tools: Option<Value>,
    #[serde(default)]
    pub resources: Option<Value>,
    #[serde(default)]
    pub prompts: Option<Value>,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Implementation {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub version: String,
    #[serde(default)]
    pub title: Option<String>,
    /// The server's icons (2025-11-25); entries that are not icons are dropped.
    #[serde(default, deserialize_with = "crate::icon::lenient_icons")]
    pub icons: Vec<crate::icon::IconSpec>,
    #[serde(default)]
    pub website_url: Option<String>,
}

/// One entry of `tools/list`.
#[derive(Clone, Debug, PartialEq)]
pub struct ToolInfo {
    pub name: String,
    pub title: Option<String>,
    pub description: Option<String>,
    pub input_schema: Value,
    /// `annotations.readOnlyHint`. Only a hint: used for parallel execution, never for trust.
    pub read_only: bool,
    /// `_meta.ui.visibility` is present and lacks `model`: a tool for user interfaces only.
    pub app_only: bool,
}

impl ToolInfo {
    pub fn parse(value: &Value) -> Result<ToolInfo, String> {
        let name = value
            .get("name")
            .and_then(Value::as_str)
            .filter(|n| !n.is_empty())
            .ok_or("a tool without a name")?;
        let input_schema = match value.get("inputSchema") {
            Some(schema @ Value::Object(_)) => schema.clone(),
            None | Some(Value::Null) => serde_json::json!({ "type": "object" }),
            Some(_) => return Err(format!("tool '{name}': inputSchema is not an object")),
        };
        let annotations = value.get("annotations");
        let text = |v: Option<&Value>| v.and_then(Value::as_str).map(str::to_string).filter(|s| !s.trim().is_empty());
        let title = text(value.get("title")).or_else(|| text(annotations.and_then(|a| a.get("title"))));
        let read_only = annotations
            .and_then(|a| a.get("readOnlyHint"))
            .and_then(Value::as_bool)
            .unwrap_or(false);
        let app_only = value
            .pointer("/_meta/ui/visibility")
            .and_then(Value::as_array)
            .is_some_and(|v| !v.iter().any(|item| item.as_str() == Some("model")));
        Ok(ToolInfo {
            name: name.to_string(),
            title,
            description: text(value.get("description")),
            input_schema,
            read_only,
            app_only,
        })
    }

    pub fn display_title(&self) -> &str {
        self.title.as_deref().unwrap_or(&self.name)
    }
}

/// The result of `tools/call`.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct CallToolResult {
    pub content: Vec<Content>,
    pub structured_content: Option<Value>,
    pub is_error: bool,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Content {
    Text(String),
    Image {
        data: String,
        mime_type: String,
    },
    Audio {
        data: String,
        mime_type: String,
    },
    ResourceLink {
        uri: String,
        name: Option<String>,
        mime_type: Option<String>,
    },
    Resource {
        uri: String,
        mime_type: Option<String>,
        text: Option<String>,
        blob: Option<String>,
    },
    /// A content type this client does not know; only its type name is kept.
    Unknown(String),
}

impl CallToolResult {
    pub fn parse(value: Value) -> Result<CallToolResult, String> {
        let Value::Object(mut map) = value else {
            return Err("the result is not an object".into());
        };
        let content = match map.remove("content") {
            None | Some(Value::Null) => Vec::new(),
            Some(Value::Array(items)) => items.iter().map(Content::parse).collect(),
            Some(_) => return Err("`content` is not a list".into()),
        };
        Ok(CallToolResult {
            content,
            structured_content: map.remove("structuredContent").filter(|v| !v.is_null()),
            is_error: map.get("isError").and_then(Value::as_bool).unwrap_or(false),
        })
    }
}

impl Content {
    fn parse(value: &Value) -> Content {
        let field = |key: &str| value.get(key).and_then(Value::as_str).map(str::to_string);
        let kind = field("type").unwrap_or_default();
        match kind.as_str() {
            "text" => Content::Text(field("text").unwrap_or_default()),
            "image" => Content::Image {
                data: field("data").unwrap_or_default(),
                mime_type: field("mimeType").unwrap_or_else(|| "image/png".into()),
            },
            "audio" => Content::Audio {
                data: field("data").unwrap_or_default(),
                mime_type: field("mimeType").unwrap_or_else(|| "audio/wav".into()),
            },
            "resource_link" => Content::ResourceLink {
                uri: field("uri").unwrap_or_default(),
                name: field("name").or_else(|| field("title")),
                mime_type: field("mimeType"),
            },
            "resource" => {
                let resource = value.get("resource");
                let get = |key: &str| resource.and_then(|r| r.get(key)).and_then(Value::as_str).map(str::to_string);
                Content::Resource {
                    uri: get("uri").unwrap_or_default(),
                    mime_type: get("mimeType"),
                    text: get("text"),
                    blob: get("blob"),
                }
            }
            _ => Content::Unknown(kind),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn parses_tools_leniently() {
        let tool = ToolInfo::parse(&json!({
            "name": "search", "description": "  ", "annotations": {"title": "Search", "readOnlyHint": true}
        }))
        .unwrap();
        assert_eq!(tool.title.as_deref(), Some("Search"));
        assert_eq!(tool.description, None, "blank descriptions are dropped");
        assert!(tool.read_only);
        assert_eq!(tool.input_schema, json!({"type": "object"}));
        assert!(ToolInfo::parse(&json!({"description": "x"})).is_err());
        assert!(ToolInfo::parse(&json!({"name": "x", "inputSchema": "nope"})).is_err());
        let app = ToolInfo::parse(&json!({"name": "x", "_meta": {"ui": {"visibility": ["app"]}}})).unwrap();
        assert!(app.app_only);
    }

    #[test]
    fn parses_results_and_content() {
        let result = CallToolResult::parse(json!({
            "content": [
                {"type": "text", "text": "hi"},
                {"type": "image", "data": "AAAA", "mimeType": "image/png"},
                {"type": "resource", "resource": {"uri": "file:///a", "text": "body"}},
                {"type": "hologram"}
            ],
            "structuredContent": {"n": 1},
            "isError": true
        }))
        .unwrap();
        assert!(result.is_error);
        assert_eq!(result.content.len(), 4);
        assert_eq!(result.content[3], Content::Unknown("hologram".into()));
        assert_eq!(result.structured_content, Some(json!({"n": 1})));
        let empty = CallToolResult::parse(json!({})).unwrap();
        assert!(empty.content.is_empty() && !empty.is_error);
    }
}
