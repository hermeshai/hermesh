//! Which service a server configuration reaches, whatever it is called (mcp-design §13.10).
//! Two configurations with the same identity are the same service: adding it a second time
//! only gives the model two copies of every tool. The desktop computes the same value in
//! `apps/desktop/src/shared/identity.ts`; keep the two alike.

use her_protocol::McpServerConfig;
use reqwest::Url;

/// Package runners whose "yes, install it" flags do not change what runs.
const RUNNERS: [&str; 4] = ["npx", "bunx", "pnpx", "uvx"];

/// A remote server: its address with the scheme and host in lower case, the default port and a
/// trailing slash dropped, and no fragment. A local one: its command line, without the
/// runners' `-y`/`--yes`. Environment, headers and credentials do not take part.
pub fn identity(config: &McpServerConfig) -> Option<String> {
    if let Some(url) = config.url.as_deref() {
        return Some(remote(url));
    }
    let command = config.command.as_deref()?;
    Some(local(command, &config.args))
}

pub fn remote(url: &str) -> String {
    let url = url.trim();
    let Ok(parsed) = Url::parse(url) else { return url.to_string() };
    let mut out = format!("{}://{}", parsed.scheme(), parsed.host_str().unwrap_or_default());
    if let Some(port) = parsed.port() {
        out.push_str(&format!(":{port}"));
    }
    let path = parsed.path().trim_end_matches('/');
    out.push_str(path);
    if let Some(query) = parsed.query() {
        out.push('?');
        out.push_str(query);
    }
    out
}

pub fn local(command: &str, args: &[String]) -> String {
    let program = command.rsplit('/').next().unwrap_or(command);
    let runner = RUNNERS.contains(&program);
    let mut words = vec![command.trim().to_string()];
    words.extend(args.iter().filter(|a| !(runner && (*a == "-y" || *a == "--yes"))).cloned());
    words.join(" ")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn url(u: &str) -> McpServerConfig {
        McpServerConfig { name: "a".into(), url: Some(u.into()), ..Default::default() }
    }

    #[test]
    fn the_same_service_under_other_spellings() {
        let one = identity(&url("https://mcp.hermesh.io/mcp")).unwrap();
        assert_eq!(identity(&url("HTTPS://MCP.Hermesh.io:443/mcp/")).unwrap(), one);
        assert_eq!(identity(&url("https://mcp.hermesh.io/mcp#x")).unwrap(), one);
        assert_ne!(identity(&url("https://mcp.hermesh.io/mcp?team=b")).unwrap(), one);
        assert_ne!(identity(&url("https://mcp.hermesh.io/sse")).unwrap(), one);
        let npx = |args: &[&str]| McpServerConfig { name: "b".into(), command: Some("npx".into()), args: args.iter().map(|s| s.to_string()).collect(), ..Default::default() };
        assert_eq!(identity(&npx(&["-y", "@acme/server"])), identity(&npx(&["@acme/server"])));
        assert_ne!(identity(&npx(&["@acme/server"])), identity(&npx(&["@acme/other"])));
    }
}
