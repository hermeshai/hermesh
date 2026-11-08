//! Names the model sees: `mcp__<server>__<tool>`, at most 64 characters of `[A-Za-z0-9_]`.
//!
//! Names are stable: the same server and tool always give the same name, so the history and
//! the request prefix match across restarts. Routing looks names up; it never parses them.

use std::collections::HashMap;
use std::collections::HashSet;

pub const MAX_NAME: usize = 64;
const HASH_LEN: usize = 8;

/// Characters other than ASCII letters, digits and `_` become `_` (including `-`, so a name
/// also works as an identifier).
pub fn sanitize(part: &str) -> String {
    let out: String = part
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() || c == '_' { c } else { '_' })
        .collect();
    if out.is_empty() { "_".to_string() } else { out }
}

/// 64-bit FNV-1a: tiny, and stable across Rust versions (unlike `DefaultHasher`).
fn fnv1a(bytes: impl Iterator<Item = u8>) -> u64 {
    let mut hash: u64 = 0xcbf2_9ce4_8422_2325;
    for byte in bytes {
        hash ^= u64::from(byte);
        hash = hash.wrapping_mul(0x0000_0100_0000_01b3);
    }
    hash
}

fn suffix(server: &str, tool: &str) -> String {
    let bytes = server.bytes().chain(std::iter::once(0)).chain(tool.bytes());
    format!("{:016x}", fnv1a(bytes))[..HASH_LEN].to_string()
}

fn with_suffix(base: &str, server: &str, tool: &str) -> String {
    let keep = MAX_NAME - HASH_LEN - 1;
    let head: String = base.chars().take(keep).collect();
    format!("{head}_{}", suffix(server, tool))
}

/// Qualified names for the tools of one server, in the same order. Tools whose sanitized
/// names collide all get a hash suffix, so the result does not depend on the order.
pub fn qualify(server: &str, tools: &[&str]) -> Vec<String> {
    let server_part = sanitize(server);
    let bases: Vec<String> = tools
        .iter()
        .map(|tool| format!("mcp__{server_part}__{}", sanitize(tool)))
        .collect();
    let mut counts: HashMap<&str, usize> = HashMap::new();
    for base in &bases {
        *counts.entry(base.as_str()).or_default() += 1;
    }
    bases
        .iter()
        .zip(tools)
        .map(|(base, tool)| {
            if base.len() > MAX_NAME || counts[base.as_str()] > 1 {
                with_suffix(base, server, tool)
            } else {
                base.clone()
            }
        })
        .collect()
}

/// A server name from a client (for example an editor) made valid: invalid characters become
/// `_`, at most 32 characters, and `_2`, `_3`... when the result is taken.
pub fn client_server_name(name: &str, taken: &HashSet<String>) -> String {
    let mut base: String = name
        .trim()
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() || c == '_' || c == '-' { c } else { '_' })
        .take(28)
        .collect();
    if base.is_empty() {
        base = "server".into();
    }
    if !taken.contains(&base) {
        return base;
    }
    (2..)
        .map(|n| format!("{base}_{n}"))
        .find(|candidate| !taken.contains(candidate))
        .unwrap_or(base)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn plain_names() {
        assert_eq!(qualify("github", &["create_issue"]), vec!["mcp__github__create_issue"]);
        assert_eq!(qualify("server-everything", &["get-sum"]), vec!["mcp__server_everything__get_sum"]);
        assert_eq!(sanitize("a.b/c d"), "a_b_c_d");
    }

    #[test]
    fn long_and_colliding_names_get_stable_suffixes() {
        let long = "a".repeat(80);
        let names = qualify("srv", &[long.as_str()]);
        assert_eq!(names[0].len(), MAX_NAME);
        assert_eq!(names, qualify("srv", &[long.as_str()]), "stable");
        let both = qualify("s", &["get-issue", "get_issue"]);
        assert_ne!(both[0], both[1]);
        assert!(both.iter().all(|n| n.starts_with("mcp__s__get_issue_") && n.len() == "mcp__s__get_issue_".len() + 8));
        let reversed = qualify("s", &["get_issue", "get-issue"]);
        assert_eq!((both[0].clone(), both[1].clone()), (reversed[1].clone(), reversed[0].clone()), "order-independent");
        assert!(names[0].chars().all(|c| c.is_ascii_alphanumeric() || c == '_'));
    }

    #[test]
    fn client_names() {
        let taken: HashSet<String> = ["github".to_string()].into();
        assert_eq!(client_server_name("GitHub Copilot", &taken), "GitHub_Copilot");
        assert_eq!(client_server_name("github", &taken), "github_2");
        assert_eq!(client_server_name("  ", &HashSet::new()), "server");
    }
}
