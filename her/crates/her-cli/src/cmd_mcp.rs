//! `her mcp list` and `her mcp get`: the MCP servers a session here would start.

use her_core::mcp::ServerCheck;
use her_protocol::McpExposure;
use her_protocol::McpServerConfig;
use her_protocol::McpServerState;
use serde_json::Value;
use serde_json::json;
use std::collections::BTreeMap;
use std::io::Write;

use crate::render::Style;
use crate::render::truncate;
use crate::setup;
use crate::setup::Runtime;

fn print_json(value: &Value) -> anyhow::Result<()> {
    let mut out = std::io::stdout().lock();
    serde_json::to_writer_pretty(&mut out, value)?;
    writeln!(out)?;
    Ok(())
}

fn transport(server: &McpServerConfig) -> &'static str {
    if server.url.is_some() { "http" } else { "stdio" }
}

/// The command line or the URL without its query, which may carry a key.
fn target(server: &McpServerConfig) -> String {
    match (&server.command, &server.url) {
        (Some(command), _) => std::iter::once(command.as_str())
            .chain(server.args.iter().map(String::as_str))
            .collect::<Vec<_>>()
            .join(" "),
        (None, Some(url)) => url.split(['?', '#']).next().unwrap_or(url).to_string(),
        (None, None) => String::new(),
    }
}

fn exposure(rt: &Runtime, server: &McpServerConfig) -> McpExposure {
    server.exposure.unwrap_or(rt.config.mcp.exposure)
}

fn exposure_name(exposure: McpExposure) -> &'static str {
    match exposure {
        McpExposure::Deferred => "deferred",
        McpExposure::Direct => "direct",
    }
}

fn sources(rt: &Runtime, name: &str) -> Vec<String> {
    rt.mcp_sources.get(name).cloned().unwrap_or_default()
}

/// Only a value that is exactly one `${NAME}` reference is shown: anything else may be a secret.
fn hidden(value: &str) -> String {
    let reference = value
        .strip_prefix("${")
        .and_then(|rest| rest.strip_suffix('}'))
        .is_some_and(|name| !name.is_empty() && name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_'));
    if reference { value.to_string() } else { "***".to_string() }
}

fn hidden_map(map: &BTreeMap<String, String>) -> BTreeMap<String, String> {
    map.iter().map(|(k, v)| (k.clone(), hidden(v))).collect()
}

fn mcp_off(rt: &Runtime) -> bool {
    if rt.config.mcp.enabled {
        return false;
    }
    println!("MCP is turned off (mcp.enabled = false)");
    true
}

fn print_warnings(rt: &Runtime) {
    let style = Style::detect();
    for warning in &rt.warnings {
        eprintln!("{}", style.yellow(&format!("warning: {warning}")));
    }
}

pub fn list(overrides: &[String], as_json: bool) -> anyhow::Result<u8> {
    let rt = setup::init_local(std::env::current_dir()?, overrides)?;
    if as_json {
        let rows: Vec<Value> = rt
            .mcp_servers
            .iter()
            .map(|s| {
                json!({
                    "name": s.name,
                    "enabled": s.is_enabled(),
                    "transport": transport(s),
                    "target": target(s),
                    "exposure": exposure_name(exposure(rt, s)),
                    "required": s.required,
                    "sources": sources(rt, &s.name),
                })
            })
            .collect();
        print_json(&json!({ "enabled": rt.config.mcp.enabled, "servers": rows, "warnings": rt.warnings }))?;
        return Ok(0);
    }
    if mcp_off(rt) {
        return Ok(0);
    }
    let style = Style::detect();
    let mut out = std::io::BufWriter::new(std::io::stdout().lock());
    if rt.mcp_servers.is_empty() {
        writeln!(out, "no MCP servers configured (add [mcp_servers.<name>] to the config, or use --mcp-config)")?;
    }
    let width = rt.mcp_servers.iter().map(|s| s.name.len()).max().unwrap_or(0);
    for server in &rt.mcp_servers {
        let state = if server.is_enabled() { exposure_name(exposure(rt, server)) } else { "disabled" };
        let line = format!(
            "{:<width$}  {:<5}  {:<8}  {}  {}",
            server.name,
            transport(server),
            state,
            truncate(&target(server), 60),
            style.dim(&format!("({})", sources(rt, &server.name).join(" + ")))
        );
        writeln!(out, "{line}")?;
    }
    out.flush()?;
    print_warnings(rt);
    Ok(0)
}

/// `her mcp list --check`: connects to every enabled server at once, then disconnects.
/// Fails when a server could not connect.
pub async fn check(overrides: &[String], as_json: bool) -> anyhow::Result<u8> {
    let rt = setup::init(std::env::current_dir()?, overrides)?;
    if !as_json && mcp_off(&rt) {
        return Ok(0);
    }
    let servers: Vec<McpServerConfig> = rt.mcp_servers.iter().filter(|s| s.is_enabled()).cloned().collect();
    let http = servers.iter().any(|s| s.url.is_some()).then(|| rt.llm.http_client().ok()).flatten();
    let checks =
        her_core::mcp::check_servers(servers, http, vec![rt.cwd.clone()], rt.cwd.clone(), rt.project_root.clone(), Some(rt.home.mcp_cache_dir()), Some(rt.home.root().to_path_buf()))
            .await;
    let failed = checks.iter().any(|c| c.state != McpServerState::Ready);
    if as_json {
        let rows: Vec<Value> = checks
            .iter()
            .map(|c| {
                json!({
                    "name": c.name,
                    "state": c.state,
                    "error": c.error,
                    "elapsed_ms": c.elapsed_ms,
                    "tools": c.tools,
                })
            })
            .collect();
        print_json(&json!({ "servers": rows, "warnings": rt.warnings }))?;
    } else {
        print_checks(&checks)?;
        print_warnings(&rt);
    }
    Ok(if failed { 1 } else { 0 })
}

fn print_checks(checks: &[ServerCheck]) -> anyhow::Result<()> {
    let style = Style::detect();
    let mut out = std::io::BufWriter::new(std::io::stdout().lock());
    if checks.is_empty() {
        writeln!(out, "no enabled MCP servers")?;
    }
    for check in checks {
        let elapsed = check.elapsed_ms.map(|ms| format!(", {ms} ms")).unwrap_or_default();
        if check.state == McpServerState::Ready {
            let line = format!("✓ {}: {} tools{elapsed}", check.name, check.tools.len());
            writeln!(out, "{}", style.green(&line))?;
            for tool in &check.tools {
                writeln!(out, "    {tool}")?;
            }
        } else {
            let error = check.error.as_deref().unwrap_or("not ready");
            writeln!(out, "{}", style.red(&format!("✗ {}: {error}{elapsed}", check.name)))?;
        }
    }
    out.flush()?;
    Ok(())
}

pub fn get(overrides: &[String], name: &str, as_json: bool) -> anyhow::Result<u8> {
    let rt = setup::init_local(std::env::current_dir()?, overrides)?;
    let Some(server) = rt.mcp_servers.iter().find(|s| s.name == name) else {
        let names: Vec<&str> = rt.mcp_servers.iter().map(|s| s.name.as_str()).collect();
        anyhow::bail!(
            "no MCP server named '{name}'{}",
            if names.is_empty() { String::new() } else { format!(" (servers: {})", names.join(", ")) }
        );
    };
    let mut shown = server.clone();
    shown.env = hidden_map(&server.env);
    shown.headers = hidden_map(&server.headers);
    if as_json {
        let mut value = serde_json::to_value(&shown)?;
        value["sources"] = json!(sources(rt, name));
        value["transport"] = json!(transport(server));
        value["exposure"] = json!(exposure_name(exposure(rt, server)));
        print_json(&value)?;
        return Ok(0);
    }
    let list = |items: &[String]| if items.is_empty() { "-".to_string() } else { items.join(", ") };
    let map = |map: &BTreeMap<String, String>| {
        if map.is_empty() {
            "-".to_string()
        } else {
            map.iter().map(|(k, v)| format!("{k}={v}")).collect::<Vec<_>>().join(", ")
        }
    };
    let seconds = |value: Option<u64>, default: u64| match value {
        Some(secs) => format!("{secs} s"),
        None => format!("{default} s (default)"),
    };
    let yes_no = |flag: bool| if flag { "yes" } else { "no" }.to_string();
    let mut rows = vec![
        ("name", shown.name.clone()),
        ("sources", list(&sources(rt, name))),
        ("enabled", yes_no(shown.is_enabled())),
        ("transport", transport(server).to_string()),
    ];
    if let Some(command) = &shown.command {
        rows.push(("command", command.clone()));
        rows.push(("args", if shown.args.is_empty() { "-".to_string() } else { shown.args.join(" ") }));
        rows.push(("env", map(&shown.env)));
        rows.push(("env_vars", list(&shown.env_vars)));
        rows.push(("cwd", shown.cwd.as_ref().map_or("-".to_string(), |p| p.display().to_string())));
    }
    if let Some(url) = &shown.url {
        let query = if url.contains(['?', '#']) { " (query not shown)" } else { "" };
        rows.push(("url", format!("{}{query}", target(server))));
        rows.push(("headers", map(&shown.headers)));
        rows.push(("bearer token", shown.bearer_token_env_var.clone().map_or("-".to_string(), |v| format!("${v}"))));
    }
    rows.extend([
        ("exposure", exposure_name(exposure(rt, server)).to_string()),
        ("description", shown.description.clone().unwrap_or_else(|| "-".to_string())),
        ("direct tools", list(&shown.direct_tools)),
        ("enabled tools", shown.enabled_tools.as_deref().map_or("all".to_string(), list)),
        ("disabled tools", list(&shown.disabled_tools)),
        ("startup timeout", seconds(shown.startup_timeout_secs, 30)),
        ("tool timeout", seconds(shown.tool_timeout_secs, 300)),
        ("parallel tools", yes_no(shown.parallel_tools)),
        ("required", yes_no(shown.required)),
    ]);
    let mut out = std::io::BufWriter::new(std::io::stdout().lock());
    for (label, value) in rows {
        writeln!(out, "{label:<17}{value}")?;
    }
    out.flush()?;
    print_warnings(rt);
    Ok(0)
}
