//! An MCP tool as a her tool. Once registered it goes the way every tool goes: hooks, the
//! permission policy, concurrency, truncation, events and the session log.

use async_trait::async_trait;
use her_llm::ToolSpec;
use her_tools::Environment;
use her_tools::Tool;
use her_tools::ToolCategory;
use her_tools::ToolCtx;
use her_tools::ToolDescription;
use her_tools::ToolError;
use her_tools::ToolKind;
use her_tools::ToolOrigin;
use her_tools::ToolOutput;
use serde_json::Value;
use serde_json::json;
use std::sync::Arc;

use crate::McpError;
use crate::content;
use crate::schema;
use crate::server::CallOptions;
use crate::server::McpServer;
use crate::types::ToolInfo;

/// Longest description sent to the model; a few servers ship very long ones.
pub const MAX_DESCRIPTION: usize = 4096;

pub struct McpTool {
    spec: ToolSpec,
    server: Arc<McpServer>,
    tool: String,
    title: String,
    parallel: bool,
    runs_unasked: bool,
}

impl McpTool {
    /// `Err` when the tool cannot be offered, with the reason.
    pub fn new(server: Arc<McpServer>, info: &ToolInfo, name: String) -> Result<McpTool, String> {
        let parameters = schema::normalize(&info.input_schema)?;
        Ok(McpTool {
            spec: ToolSpec {
                name,
                description: description(info),
                parameters,
            },
            parallel: info.read_only || server.config().parallel_tools,
            runs_unasked: server.config().runs_unasked(info.read_only),
            title: format!("{}: {}", server.name(), info.display_title()),
            tool: info.name.clone(),
            server,
        })
    }

    pub fn server(&self) -> &Arc<McpServer> {
        &self.server
    }

    pub fn tool_name(&self) -> &str {
        &self.tool
    }
}

/// The description the model sees: the server's, its title, or the name; trimmed and capped.
pub fn description(info: &ToolInfo) -> String {
    let text = info
        .description
        .as_deref()
        .or(info.title.as_deref())
        .unwrap_or(&info.name)
        .trim();
    if text.len() <= MAX_DESCRIPTION {
        return text.to_string();
    }
    let mut end = MAX_DESCRIPTION;
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    format!("{}…", &text[..end])
}

pub fn kind(parallel: bool) -> ToolKind {
    if parallel { ToolKind::ReadOnly } else { ToolKind::Mutating }
}

/// How a call of `tool` on `server` looks in user interfaces.
pub fn describe_call(server: &McpServer, tool: &str) -> ToolDescription {
    let title = server
        .tools()
        .iter()
        .find(|t| t.name == tool)
        .map(|t| t.display_title().to_string())
        .unwrap_or_else(|| tool.to_string());
    ToolDescription {
        category: ToolCategory::Other,
        title: format!("{}: {title}", server.name()),
        locations: Vec::new(),
    }
}

#[async_trait]
impl Tool for McpTool {
    fn spec(&self) -> &ToolSpec {
        &self.spec
    }

    fn kind(&self) -> ToolKind {
        kind(self.parallel)
    }

    fn origin(&self) -> ToolOrigin {
        ToolOrigin::Mcp
    }

    fn runs_unasked(&self, _args: &Value) -> bool {
        self.runs_unasked
    }

    fn describe(&self, _args: &Value, _env: &dyn Environment) -> ToolDescription {
        ToolDescription {
            category: ToolCategory::Other,
            title: self.title.clone(),
            locations: Vec::new(),
        }
    }

    async fn execute(&self, args: Value, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        call(&self.server, &self.tool, args, ctx).await
    }
}

/// Runs `tool` on `server` and converts the result. Failures become error results that say
/// what happened; only an interruption is an error of its own.
pub async fn call(server: &Arc<McpServer>, tool: &str, args: Value, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
    let args = match args {
        Value::Null => json!({}),
        Value::Object(_) => args,
        _ => return Ok(ToolOutput::error("MCP tool arguments must be a JSON object.")),
    };
    let sink = ctx.progress.clone();
    let options = CallOptions {
        cancel: ctx.cancel.clone(),
        progress: Some(Arc::new(move |text: &str| sink.emit(text))),
    };
    let name = server.name().to_string();
    match server.call_tool(tool, args, options).await {
        Ok(result) => Ok(content::to_output(result, &ctx, &name, tool)),
        Err(McpError::Cancelled) => Err(ToolError::Cancelled),
        Err(McpError::Timeout(secs)) => Ok(ToolOutput::error(format!(
            "MCP tool '{tool}' timed out after {secs} s without progress."
        ))),
        Err(McpError::Rpc { code, message }) => Ok(ToolOutput::error(format!("MCP error {code}: {message}"))),
        Err(McpError::Unavailable(reason)) => Ok(ToolOutput::error(format!(
            "MCP server '{name}' is unavailable: {reason}"
        ))),
        Err(McpError::UnknownTool(_)) => Ok(ToolOutput::error(format!(
            "The MCP tool '{tool}' is no longer offered by server '{name}'."
        ))),
        Err(err) => Ok(ToolOutput::error(format!("MCP server '{name}': {err}"))),
    }
}
