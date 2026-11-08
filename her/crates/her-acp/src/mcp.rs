//! ACP's `mcpServers` as her server configurations: stdio and http servers are started,
//! other transports are skipped.

use her_protocol::McpServerConfig;
use serde_json::Value;
use std::collections::BTreeMap;
use std::collections::HashSet;

/// The servers her can start, and one line for each one it cannot. Names are made valid and
/// unique; a name equal to a configured server's replaces that server.
pub fn servers(values: &[Value]) -> (Vec<McpServerConfig>, Vec<String>) {
    let mut out = Vec::new();
    let mut skipped = Vec::new();
    let mut taken = HashSet::new();
    for value in values {
        let name = value.get("name").and_then(Value::as_str).unwrap_or_default();
        let text = |key: &str| value.get(key).and_then(Value::as_str).map(str::to_string);
        let mut server = match value.get("type").and_then(Value::as_str).unwrap_or("stdio") {
            "stdio" => {
                let Some(command) = text("command") else {
                    skipped.push(format!("MCP server '{name}' was not started: it has no command"));
                    continue;
                };
                McpServerConfig {
                    command: Some(command),
                    args: value
                        .get("args")
                        .and_then(Value::as_array)
                        .map(|args| args.iter().filter_map(|a| a.as_str().map(str::to_string)).collect())
                        .unwrap_or_default(),
                    env: pairs(value.get("env")),
                    ..McpServerConfig::default()
                }
            }
            "http" => {
                let Some(url) = text("url") else {
                    skipped.push(format!("MCP server '{name}' was not started: it has no url"));
                    continue;
                };
                McpServerConfig {
                    url: Some(url),
                    headers: pairs(value.get("headers")),
                    ..McpServerConfig::default()
                }
            }
            other => {
                skipped.push(format!(
                    "MCP server '{name}' was not started: her does not support the {other} transport"
                ));
                continue;
            }
        };
        server.name = her_mcp::naming::client_server_name(name, &taken);
        taken.insert(server.name.clone());
        out.push(server);
    }
    (out, skipped)
}

/// `[{name, value}]` as a map.
fn pairs(list: Option<&Value>) -> BTreeMap<String, String> {
    list.and_then(Value::as_array)
        .map(|items| {
            items
                .iter()
                .filter_map(|item| Some((item.get("name")?.as_str()?.to_string(), item.get("value")?.as_str()?.to_string())))
                .collect()
        })
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn converts_stdio_and_http_and_skips_the_rest() {
        let (servers, skipped) = servers(&[
            json!({"name": "fs", "command": "/usr/bin/fs-mcp", "args": ["--root", "."], "env": [{"name": "A", "value": "1"}]}),
            json!({"type": "http", "name": "api server", "url": "https://example.com/mcp", "headers": [{"name": "X-Key", "value": "k"}]}),
            json!({"type": "sse", "name": "events", "url": "https://example.com/sse", "headers": []}),
            json!({"name": "fs", "command": "/usr/bin/other", "args": null, "env": null}),
        ]);
        assert_eq!(servers.len(), 3);
        assert_eq!(servers[0].name, "fs");
        assert_eq!(servers[0].args, vec!["--root", "."]);
        assert_eq!(servers[0].env.get("A").map(String::as_str), Some("1"));
        assert_eq!(servers[1].name, "api_server");
        assert_eq!(servers[1].headers.get("X-Key").map(String::as_str), Some("k"));
        assert_eq!(servers[2].name, "fs_2", "names stay unique");
        assert!(servers.iter().all(|s| s.validate().is_ok()));
        assert_eq!(skipped.len(), 1);
        assert!(skipped[0].contains("sse"), "{skipped:?}");
    }
}
