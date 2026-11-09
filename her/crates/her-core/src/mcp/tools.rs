//! `tool_search` and `call_tool`: how the model reaches deferred tools, those of the built-in
//! groups (the browsers, computer use) and of deferred MCP servers. Their definitions never
//! change, so the tool list stays the same for the whole session.

use async_trait::async_trait;
use her_llm::ToolSpec;
use her_tools::Environment;
use her_tools::Tool;
use her_tools::ToolCategory;
use her_tools::ToolCtx;
use her_tools::ToolDescription;
use her_tools::ToolError;
use her_tools::ToolKind;
use her_tools::Authority;
use her_tools::ToolOrigin;
use her_tools::ToolOutput;
use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::collections::HashSet;
use std::fmt::Write as _;
use std::sync::Arc;

use super::Catalog;
use crate::deferred::DeferredCell;
use crate::deferred::Group;
use super::Entry;
use super::INSTRUCTIONS_LIMIT;
use super::McpManager;
use super::cap;

pub(crate) const SEARCH_TOOL: &str = "tool_search";
pub(crate) const CALL_TOOL: &str = "call_tool";
const DEFAULT_LIMIT: usize = 5;
const MAX_LIMIT: usize = 20;
/// Above this many tools, a server listing gives the names alone.
const LIST_LIMIT: usize = 100;

/// What a session's search results have shown since its last compaction: definitions and
/// server instructions are not repeated.
#[derive(Default)]
pub(crate) struct Shown {
    tools: HashSet<String>,
    servers: HashSet<String>,
}

impl Shown {
    pub(crate) fn clear(&mut self) {
        self.tools.clear();
        self.servers.clear();
    }
}

/// The two tools for one session, sharing its `shown` record and its deferred built-in tools.
pub(crate) fn kernel_tools(manager: Option<&Arc<McpManager>>, deferred: &DeferredCell, shown: &Arc<Mutex<Shown>>) -> Vec<Arc<dyn Tool>> {
    vec![
        Arc::new(ToolSearch {
            manager: manager.cloned(),
            deferred: deferred.clone(),
            shown: shown.clone(),
            spec: ToolSpec {
                name: SEARCH_TOOL.into(),
                description: "Search the tools of the groups listed in <deferred_tools> and return their \
                    definitions. Those tools are not in your tool list: run them with call_tool. With `server` and no \
                    `query`, lists all tools of that group."
                    .into(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "query": { "type": "string", "description": "Keywords, or a tool's full name. Several full names load several tools." },
                        "server": { "type": "string", "description": "Only search this group." },
                        "limit": { "type": "integer", "description": "Maximum number of tools to return (default 5, at most 20)." }
                    },
                    "additionalProperties": false
                }),
            },
        }),
        Arc::new(CallTool {
            manager: manager.cloned(),
            deferred: deferred.clone(),
            spec: ToolSpec {
                name: CALL_TOOL.into(),
                description: "Run a tool found with tool_search.".into(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "name": { "type": "string", "description": "The tool's full name, e.g. browser_navigate or mcp__github__create_issue." },
                        "arguments": { "type": "object", "description": "Arguments matching the tool's parameters." }
                    },
                    "required": ["name", "arguments"],
                    "additionalProperties": false
                }),
            },
        }),
    ]
}

struct ToolSearch {
    manager: Option<Arc<McpManager>>,
    deferred: DeferredCell,
    shown: Arc<Mutex<Shown>>,
    spec: ToolSpec,
}

impl ToolSearch {
    /// A built-in group, whole: it is small, and its tools work together. Its guidelines come
    /// the first time it appears, as an MCP server's instructions do.
    fn describe_group(&self, out: &mut String, group: &Group) {
        let first = self.shown.lock().servers.insert(group.name.to_string());
        let _ = writeln!(out, "Group {}: {}.", group.name, group.summary);
        if first {
            for line in group.guidelines() {
                let _ = writeln!(out, "  {line}");
            }
        }
        for tool in &group.tools {
            let spec = tool.spec();
            let first = self.shown.lock().tools.insert(spec.name.clone());
            if first {
                let _ = writeln!(out, "- {}: {}", spec.name, spec.description);
                let _ = writeln!(out, "  {}", spec.parameters);
            } else {
                let _ = writeln!(out, "- {}: {} (definition shown earlier)", spec.name, first_sentence(&spec.description));
            }
        }
    }

    /// Writes a server's header (its instructions, the first time it appears).
    fn server_header(&self, out: &mut String, entry: &Entry) {
        let server = entry.server.name();
        let first = self.shown.lock().servers.insert(server.to_string());
        match entry.server.instructions().filter(|_| first) {
            Some(instructions) => {
                let _ = writeln!(out, "Server {server}: {}", cap(instructions.trim(), INSTRUCTIONS_LIMIT));
            }
            None => {
                let _ = writeln!(out, "Server {server}:");
            }
        }
    }

    fn describe_entry(&self, out: &mut String, entry: &Entry) {
        let description = her_mcp::tool::description(&entry.info);
        let first = self.shown.lock().tools.insert(entry.qualified.clone());
        if first {
            let _ = writeln!(out, "- {}: {description}", entry.qualified);
            let _ = writeln!(out, "  {}", entry.parameters);
        } else {
            let _ = writeln!(out, "- {}: {} (definition shown earlier)", entry.qualified, first_sentence(&description));
        }
    }

    fn list_server(&self, catalog: &Catalog, server: &str) -> ToolOutput {
        let entries: Vec<&Entry> = catalog.entries.iter().filter(|e| e.deferred && e.server.name() == server).collect();
        let Some(first) = entries.first() else {
            return ToolOutput::error(format!(
                "No deferred MCP server named '{server}' has tools. Servers: {}.",
                self.deferred_servers().join(", ")
            ));
        };
        let mut out = String::new();
        self.server_header(&mut out, first);
        if entries.len() <= LIST_LIMIT {
            for entry in &entries {
                let description = her_mcp::tool::description(&entry.info);
                let _ = writeln!(out, "- {}: {}", entry.qualified, first_sentence(&description));
            }
            out.push_str("Search with a tool's full name to get its parameters.\n");
            return ToolOutput::text(out);
        }
        // Every name, so none is missed, in a third of the room descriptions would take.
        let prefix = format!("mcp__{}__", her_mcp::naming::sanitize(server));
        let names: Vec<&str> = entries
            .iter()
            .map(|e| e.qualified.strip_prefix(prefix.as_str()).unwrap_or(&e.qualified))
            .collect();
        let _ = writeln!(out, "{} tools; the full name of each is {prefix}<name>:", entries.len());
        let _ = writeln!(out, "{}", names.join(", "));
        out.push_str("Search with full names (several at once) or with `query` to get their descriptions and parameters.\n");
        ToolOutput::text(out)
    }

    /// The groups and deferred servers there are, by name.
    fn deferred_servers(&self) -> Vec<String> {
        let mut names: Vec<String> = self.deferred.lock().groups.iter().map(|g| g.name.to_string()).collect();
        if let Some(manager) = &self.manager {
            names.extend(
                manager
                    .servers()
                    .iter()
                    .enumerate()
                    .filter(|(i, _)| manager.is_deferred(*i))
                    .map(|(_, s)| s.name().to_string()),
            );
        }
        names
    }
}

/// Whether `query` is about `group`: its name, a tool's name or a part of it, or a word of its
/// summary.
fn group_matches(group: &Group, query: &str) -> bool {
    let summary = group.summary.to_lowercase();
    query
        .split(|c: char| !c.is_alphanumeric() && c != '_')
        .map(str::to_lowercase)
        .filter(|w| !w.is_empty())
        .any(|word| {
            word == group.name
                || group.tools.iter().any(|t| {
                    let name = t.spec().name.as_str();
                    name == word || name.split('_').skip(1).any(|part| part == word)
                })
                || (word.chars().count() >= 4 && summary.split(|c: char| !c.is_alphanumeric()).any(|w| w == word))
        })
}

fn first_sentence(text: &str) -> &str {
    let line = text.lines().next().unwrap_or(text);
    match line.find(". ") {
        Some(end) => &line[..=end],
        None => line,
    }
}

#[async_trait]
impl Tool for ToolSearch {
    fn spec(&self) -> &ToolSpec {
        &self.spec
    }

    fn origin(&self) -> ToolOrigin {
        ToolOrigin::Mcp
    }

    // It only looks through the definitions her already holds.
    fn runs_unasked(&self, _args: &Value) -> bool {
        true
    }

    fn describe(&self, args: &Value, _env: &dyn Environment) -> ToolDescription {
        let query = args.get("query").and_then(Value::as_str).unwrap_or_default();
        let server = args.get("server").and_then(Value::as_str);
        let title = match (query.trim(), server) {
            ("", Some(server)) => format!("list tools of {server}"),
            (query, Some(server)) => format!("search {server} for {query}"),
            (query, None) => format!("search tools for {query}"),
        };
        ToolDescription {
            category: ToolCategory::Search,
            title,
            locations: Vec::new(),
        }
    }

    async fn execute(&self, args: Value, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        let query = args.get("query").and_then(Value::as_str).unwrap_or_default().trim().to_string();
        let server = args.get("server").and_then(Value::as_str).map(str::trim).filter(|s| !s.is_empty());
        let limit = args
            .get("limit")
            .and_then(Value::as_u64)
            .map_or(DEFAULT_LIMIT, |l| (l as usize).clamp(1, MAX_LIMIT));
        if query.is_empty() && server.is_none() {
            return Ok(ToolOutput::error("Give a `query`, or a `server` to list its tools."));
        }
        // The built-in groups first: one asked for by name, or those the query is about.
        let deferred = self.deferred.lock().clone();
        let mut out = String::new();
        match server {
            Some(name) => {
                if let Some(group) = deferred.group(name) {
                    self.describe_group(&mut out, group);
                    out.push_str("Run a tool with call_tool(name, arguments).\n");
                    return Ok(ToolOutput::text(out));
                }
            }
            None => {
                for group in deferred.groups.iter().filter(|g| group_matches(g, &query)) {
                    self.describe_group(&mut out, group);
                }
            }
        }
        let Some(manager) = &self.manager else {
            if out.is_empty() {
                return Ok(ToolOutput::text(format!(
                    "No matching tools. Groups: {}. Try other words, or set `server` to list a group's tools.",
                    self.deferred_servers().join(", ")
                )));
            }
            out.push_str("Run a tool with call_tool(name, arguments).\n");
            return Ok(ToolOutput::text(out));
        };
        manager.wait_for_deferred(&ctx.cancel).await;
        if ctx.cancel.is_cancelled() {
            return Err(ToolError::Cancelled);
        }
        let catalog = manager.catalog();
        if query.is_empty() {
            return Ok(self.list_server(&catalog, server.unwrap_or_default()));
        }
        let hits: Vec<&Entry> = catalog
            .index
            .search(&query, catalog.index_entries.len())
            .into_iter()
            .map(|doc| &catalog.entries[catalog.index_entries[doc]])
            .filter(|entry| server.is_none_or(|s| entry.server.name() == s))
            .take(limit)
            .collect();
        let unavailable = manager.unavailable();
        if hits.is_empty() && !out.is_empty() {
            out.push_str("Run a tool with call_tool(name, arguments).\n");
            return Ok(ToolOutput::text(out));
        }
        if hits.is_empty() {
            let mut out = format!(
                "No matching tools. Deferred servers: {}. Try other words, or set `server` to list a server's tools.",
                self.deferred_servers().join(", ")
            );
            if !unavailable.is_empty() {
                let _ = write!(out, "\nUnavailable now: {}.", unavailable.join("; "));
            }
            return Ok(ToolOutput::text(out));
        }
        // Grouped by server, in the order the servers first appear.
        let mut order: Vec<&str> = Vec::new();
        for hit in &hits {
            if !order.contains(&hit.server.name()) {
                order.push(hit.server.name());
            }
        }
        for name in order {
            let group: Vec<&&Entry> = hits.iter().filter(|e| e.server.name() == name).collect();
            self.server_header(&mut out, group[0]);
            for entry in group {
                self.describe_entry(&mut out, entry);
            }
        }
        out.push_str("Run a tool with call_tool(name, arguments).\n");
        if !unavailable.is_empty() {
            let _ = writeln!(out, "Unavailable now: {}.", unavailable.join("; "));
        }
        Ok(ToolOutput::text(out))
    }
}

struct CallTool {
    manager: Option<Arc<McpManager>>,
    deferred: DeferredCell,
    spec: ToolSpec,
}

/// What a call runs: a deferred built-in tool, judged and run as itself; or an MCP server's
/// tool, with whether it may run beside others.
enum Target {
    Builtin(Arc<dyn Tool>),
    Mcp(Arc<her_mcp::McpServer>, String, bool),
}

impl CallTool {
    fn target(&self, args: &Value) -> Option<Target> {
        let name = args.get("name").and_then(Value::as_str)?;
        if let Some((_, tool)) = self.deferred.lock().find(name) {
            return Some(Target::Builtin(tool.clone()));
        }
        let catalog = self.manager.as_ref()?.catalog();
        let entry = catalog.find(name)?;
        let parallel = entry.info.read_only || entry.server.config().parallel_tools;
        Some(Target::Mcp(entry.server.clone(), entry.info.name.clone(), parallel))
    }

    /// A built-in target, with the arguments it gets.
    fn builtin(&self, args: &Value) -> Option<(Arc<dyn Tool>, Value)> {
        match self.target(args)? {
            Target::Builtin(tool) => Some((tool, inner(args))),
            Target::Mcp(..) => None,
        }
    }
}

/// The arguments for the tool called: an object, empty when none were given.
fn inner(args: &Value) -> Value {
    match args.get("arguments") {
        Some(arguments @ Value::Object(_)) => arguments.clone(),
        _ => json!({}),
    }
}

#[async_trait]
impl Tool for CallTool {
    fn spec(&self) -> &ToolSpec {
        &self.spec
    }

    fn kind(&self) -> ToolKind {
        ToolKind::Mutating
    }

    fn kind_for(&self, args: &Value) -> ToolKind {
        match self.target(args) {
            Some(Target::Builtin(tool)) => tool.kind_for(&inner(args)),
            Some(Target::Mcp(_, _, parallel)) => her_mcp::tool::kind(parallel),
            None => ToolKind::Mutating,
        }
    }

    fn origin(&self) -> ToolOrigin {
        ToolOrigin::Mcp
    }

    fn origin_for(&self, args: &Value) -> ToolOrigin {
        match self.builtin(args) {
            Some((tool, inner)) => tool.origin_for(&inner),
            None => ToolOrigin::Mcp,
        }
    }

    fn runs_unasked(&self, args: &Value) -> bool {
        let Some(name) = args.get("name").and_then(Value::as_str) else { return true };
        let Some(manager) = &self.manager else { return true };
        let catalog = manager.catalog();
        match catalog.find(name) {
            Some(entry) => entry.server.config().runs_unasked(entry.info.read_only),
            // Nothing runs under a name no server has, unless one still connecting brings it.
            None => !manager.connecting(),
        }
    }

    fn prepare_arguments(&self, mut args: Value) -> Value {
        // The arguments sent as a JSON string.
        let parsed = match args.get("arguments") {
            Some(Value::String(text)) => serde_json::from_str::<Value>(text).ok().filter(Value::is_object),
            _ => None,
        };
        if let Some(parsed) = parsed {
            args["arguments"] = parsed;
        }
        // A built-in tool repairs its own arguments, as when it is called directly.
        if let Some((tool, inner)) = self.builtin(&args) {
            args["arguments"] = tool.prepare_arguments(inner);
        }
        args
    }

    fn describe(&self, args: &Value, env: &dyn Environment) -> ToolDescription {
        match self.target(args) {
            Some(Target::Builtin(tool)) => tool.describe(&inner(args), env),
            Some(Target::Mcp(server, tool, _)) => her_mcp::tool::describe_call(&server, &tool),
            None => ToolDescription {
                category: ToolCategory::Other,
                title: match args.get("name").and_then(Value::as_str) {
                    Some(name) => format!("call {name}"),
                    None => CALL_TOOL.to_string(),
                },
                locations: Vec::new(),
            },
        }
    }

    async fn authorities(&self, args: &Value, ctx: &ToolCtx) -> Result<Vec<Authority>, ToolError> {
        match self.builtin(args) {
            Some((tool, inner)) => tool.authorities(&inner, ctx).await,
            None => Ok(Vec::new()),
        }
    }

    async fn check(&self, args: &Value, ctx: &ToolCtx) -> Result<(), ToolError> {
        match self.builtin(args) {
            Some((tool, inner)) => tool.check(&inner, ctx).await,
            None => Ok(()),
        }
    }

    async fn execute(&self, args: Value, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        let Some(name) = args.get("name").and_then(Value::as_str) else {
            return Ok(ToolOutput::error("call_tool needs `name`: the full name of a tool found with tool_search."));
        };
        let mut target = self.target(&args);
        if target.is_none()
            && let Some(manager) = &self.manager
        {
            // Its server may still be connecting, for example right after a resume.
            manager.wait_for_deferred(&ctx.cancel).await;
            if ctx.cancel.is_cancelled() {
                return Err(ToolError::Cancelled);
            }
            target = self.target(&args);
        }
        match target {
            Some(Target::Builtin(tool)) => tool.execute(inner(&args), ctx).await,
            Some(Target::Mcp(server, tool, _)) => {
                let arguments = args.get("arguments").cloned().unwrap_or(Value::Null);
                let connectors = ctx.session_state.get::<crate::connect::ConnectorLink>().is_some();
                let mut output = her_mcp::tool::call(&server, &tool, arguments, ctx).await?;
                // A call that failed for want of a sign-in: Her's own conversation has the owner sign
                // in again on a card (docs/her-secure-cards.md §2.2).
                if output.is_error && connectors {
                    let (way, again) = (her_mcp::oauth::sign_in_way(server.name()), format!("have the owner sign in again with connector_connect {{name: \"{}\"}}", server.name()));
                    for content in &mut output.content {
                        if let her_llm::UserContent::Text { text } = content {
                            *text = text.replace(&way, &again);
                        }
                    }
                }
                Ok(output)
            }
            None => Ok(ToolOutput::error(format!("Unknown tool '{name}'. Use tool_search to find tools."))),
        }
    }
}
