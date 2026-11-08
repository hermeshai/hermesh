//! What tools there are: `her tools` and `tools/list` (standalone-cli-design §8.3). The tools
//! are the ones sessions run, so the list cannot drift from them; only what a group needs is
//! written here.

use her_llm::ToolSpec;
use her_protocol::api::ToolEntry;
use her_protocol::api::ToolEntryKind;
use her_protocol::api::ToolEntryOrigin;
use her_tools::BashTool;
use her_tools::Tool;
use her_tools::ToolKind;
use her_tools::ToolOrigin;
use serde_json::Value;
use std::sync::Arc;

use crate::config::Config;
use crate::deferred::AgentRole;
use crate::deferred::Deferred;
use crate::system::SystemSettings;

/// The tools a command-line or served session would have under `config`, before any MCP server
/// connects, and the ones it would have with other settings, marked by what they need.
pub fn builtin(config: &Config) -> Vec<ToolEntry> {
    let settings = SystemSettings::of(config);
    let on = Deferred::of(&settings, true, false, true, AgentRole::Main);
    let mut list = Vec::new();
    let all = her_tools::builtin_registry(BashTool::default());
    for spec in all.iter_specs() {
        let enabled = config.enabled_tools.contains(&spec.name);
        let tool = all.get(&spec.name).expect("listed");
        list.push(entry(
            &*tool,
            "base",
            true,
            enabled,
            (!enabled).then(|| format!("add \"{}\" to tools.enabled", spec.name)),
        ));
    }
    for tool in crate::agents::agent_tools() {
        let enabled = config.agents_enabled;
        list.push(entry(
            &*tool,
            "agents",
            true,
            enabled,
            (!enabled).then(|| "agents.enabled = true".to_string()),
        ));
    }
    let none: crate::deferred::DeferredCell = Default::default();
    for tool in crate::mcp::tools::kernel_tools(None, &none, &Default::default()) {
        list.push(entry(&*tool, "deferred", true, true, None));
    }
    let pdf = pdf_needs();
    for group in &on.groups {
        for tool in &group.tools {
            let needs = (group.name == "doc").then(|| pdf.clone()).flatten();
            list.push(entry(&**tool, group.name, false, true, needs));
        }
    }
    // Those of the groups that are off: the first to name a tool says what it needs.
    let mut off = |name: &str, tools: Vec<Arc<dyn Tool>>, needs: &str| {
        for tool in tools {
            if !list
                .iter()
                .any(|known: &ToolEntry| known.name == tool.spec().name)
            {
                list.push(entry(&*tool, name, false, false, Some(needs.to_string())));
            }
        }
    };
    off(
        crate::web_search::GROUP,
        crate::web_search::tools(&settings.web_search),
        "web_search.enabled = true",
    );
    off(
        "browser",
        crate::browser::one_browser(),
        "browser.enabled = true",
    );
    off(
        "browser",
        crate::browser::one_browser_with_scripts(),
        "browser.script_enabled = true",
    );
    let desktop = if cfg!(target_os = "macos") {
        "desktop.enabled = true"
    } else {
        "macOS"
    };
    off("cua", crate::desktop::desktop_tools(), desktop);
    list
}

/// How many tools of the base group are on, and the groups found through `tool_search`: what
/// `her status` says, cheaply.
pub fn summary(config: &Config) -> (usize, Vec<&'static str>) {
    let settings = SystemSettings::of(config);
    let role = if config.agents_enabled {
        AgentRole::Main
    } else {
        AgentRole::None
    };
    let groups = Deferred::names_of(&settings, true, false, true, role);
    (config.enabled_tools.len(), groups)
}

/// What a session has now.
pub(crate) fn of_session(session: &crate::session::Session) -> Vec<ToolEntry> {
    let mut list = Vec::new();
    let loaded = session.tools();
    for spec in loaded.iter_specs() {
        if let Some(tool) = loaded.get(&spec.name) {
            list.push(entry(&*tool, group_of(&*tool), true, true, None));
        }
    }
    let deferred = session.deferred.lock().clone();
    let pdf = pdf_needs();
    for group in &deferred.groups {
        for tool in &group.tools {
            let needs = (group.name == "doc").then(|| pdf.clone()).flatten();
            list.push(entry(&**tool, group.name, false, true, needs));
        }
    }
    if let Some(mcp) = &session.mcp {
        for found in mcp.catalog().entries.iter().filter(|e| e.deferred) {
            let spec = ToolSpec {
                name: found.qualified.clone(),
                description: found.info.description.clone().unwrap_or_default(),
                parameters: found.parameters.clone(),
            };
            let kind = if found.info.read_only {
                ToolEntryKind::ReadOnly
            } else {
                ToolEntryKind::Mutating
            };
            list.push(from_spec(spec, "mcp", ToolEntryOrigin::Mcp, kind, false));
        }
    }
    list
}

/// Why PDFs would not open, while they would not.
fn pdf_needs() -> Option<String> {
    her_docs::engine::locate_library()
        .is_none()
        .then(|| "PDF: her install pdf".to_string())
}

fn group_of(tool: &dyn Tool) -> &'static str {
    if tool.origin() == ToolOrigin::Mcp {
        return "mcp";
    }
    match tool.spec().name.as_str() {
        crate::mcp::tools::SEARCH_TOOL | crate::mcp::tools::CALL_TOOL => "deferred",
        "spawn_agent" | "send_message" => "agents",
        "send_to_user" | crate::routine::MANAGE_ROUTINE => "bot",
        _ => "base",
    }
}

fn entry(
    tool: &dyn Tool,
    group: &str,
    loaded: bool,
    available: bool,
    needs: Option<String>,
) -> ToolEntry {
    let spec = tool.spec();
    ToolEntry {
        name: spec.name.clone(),
        description: spec.description.clone(),
        parameters: spec.parameters.clone(),
        group: group.to_string(),
        origin: match tool.origin() {
            ToolOrigin::Mcp => ToolEntryOrigin::Mcp,
            _ => ToolEntryOrigin::Builtin,
        },
        kind: match tool.kind() {
            ToolKind::ReadOnly => ToolEntryKind::ReadOnly,
            ToolKind::Mutating => ToolEntryKind::Mutating,
        },
        loaded,
        available,
        needs,
    }
}

fn from_spec(
    spec: ToolSpec,
    group: &str,
    origin: ToolEntryOrigin,
    kind: ToolEntryKind,
    loaded: bool,
) -> ToolEntry {
    ToolEntry {
        name: spec.name,
        description: spec.description,
        parameters: spec.parameters,
        group: group.to_string(),
        origin,
        kind,
        loaded,
        available: true,
        needs: None,
    }
}

/// The parts of the list that models and session logs depend on, for a snapshot test: the same
/// on every machine.
#[doc(hidden)]
pub fn contract(config: &Config) -> Value {
    let tools: Vec<Value> = builtin(config)
        .into_iter()
        .map(|tool| {
            serde_json::json!({
                "name": tool.name,
                "group": tool.group,
                "kind": tool.kind,
                "loaded": tool.loaded,
                "description": tool.description,
                "parameters": tool.parameters,
            })
        })
        .collect();
    sorted(Value::Array(tools))
}

/// Keys in order, whichever way this build keeps JSON objects.
fn sorted(value: Value) -> Value {
    match value {
        Value::Object(map) => {
            let mut entries: Vec<(String, Value)> = map.into_iter().collect();
            entries.sort_by(|a, b| a.0.cmp(&b.0));
            Value::Object(
                entries
                    .into_iter()
                    .map(|(key, value)| (key, sorted(value)))
                    .collect(),
            )
        }
        Value::Array(items) => Value::Array(items.into_iter().map(sorted).collect()),
        other => other,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_turned_off_group_is_listed_with_what_it_needs() {
        let list = builtin(&Config::default());
        let browser = list
            .iter()
            .find(|t| t.name == "browser_navigate")
            .expect("browser tools are listed");
        assert!(!browser.available);
        assert!(!browser.loaded);
        assert_eq!(browser.needs.as_deref(), Some("browser.enabled = true"));
        let read = list.iter().find(|t| t.name == "read").expect("read");
        assert!(read.available && read.loaded);
        assert_eq!(read.group, "base");
        let web = list.iter().find(|t| t.name == "web_search").expect("web");
        assert!(web.available && !web.loaded);
        let names: Vec<&str> = list.iter().map(|t| t.name.as_str()).collect();
        let unique: std::collections::HashSet<&&str> = names.iter().collect();
        assert_eq!(names.len(), unique.len(), "each tool once: {names:?}");
    }

    #[test]
    fn a_tool_left_out_of_the_enabled_list_says_so() {
        let config = Config {
            enabled_tools: vec!["read".to_string()],
            ..Config::default()
        };
        let list = builtin(&config);
        let bash = list.iter().find(|t| t.name == "bash").expect("bash");
        assert!(!bash.available);
        assert!(
            bash.needs
                .as_deref()
                .is_some_and(|n| n.contains("tools.enabled"))
        );
    }

    /// Tool names and parameters are what models and old logs rely on: a change has to show in
    /// the review. `HER_UPDATE_SNAPSHOTS=1` rewrites the copy.
    #[test]
    fn the_tool_contract_is_the_one_in_the_repository() {
        let path =
            std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/snapshots/tools.json");
        let now = serde_json::to_string_pretty(&contract(&Config::default())).expect("json") + "\n";
        if std::env::var_os("HER_UPDATE_SNAPSHOTS").is_some() {
            std::fs::create_dir_all(path.parent().expect("parent")).expect("dir");
            std::fs::write(&path, &now).expect("write");
        }
        let saved = std::fs::read_to_string(&path).unwrap_or_default();
        assert!(
            saved == now,
            "the tools changed: run with HER_UPDATE_SNAPSHOTS=1 and review the diff"
        );
    }
}
