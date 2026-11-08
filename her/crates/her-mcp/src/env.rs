//! Values from the configuration: `${VAR}` expansion, and the environment of a local server,
//! which starts empty and only gets a safe base set plus what the configuration names.

use std::collections::BTreeMap;

/// Passed through from her's environment when set.
pub const BASE_VARS: &[&str] = &[
    "HOME",
    "USER",
    "LOGNAME",
    "SHELL",
    "PATH",
    "TMPDIR",
    "TERM",
    "LANG",
    "LC_ALL",
    "LC_CTYPE",
    "TZ",
    "__CF_USER_TEXT_ENCODING",
    "HTTP_PROXY",
    "HTTPS_PROXY",
    "ALL_PROXY",
    "NO_PROXY",
    "http_proxy",
    "https_proxy",
    "all_proxy",
    "no_proxy",
    "SSL_CERT_FILE",
    "SSL_CERT_DIR",
    "NODE_EXTRA_CA_CERTS",
    "REQUESTS_CA_BUNDLE",
];

fn is_base(name: &str) -> bool {
    BASE_VARS.contains(&name) || name.starts_with("XDG_")
}

/// Expands `${VAR}`, `${VAR:-default}` and `${credential:<name>}`. A variable that is unset
/// (or empty, with a default) and has no default is an error naming it; so is a credential
/// that is not saved. `lookup` gets credentials as `credential:<name>`.
pub fn expand(value: &str, lookup: &dyn Fn(&str) -> Option<String>) -> Result<String, String> {
    let mut out = String::with_capacity(value.len());
    let mut rest = value;
    while let Some(start) = rest.find("${") {
        out.push_str(&rest[..start]);
        let after = &rest[start + 2..];
        let Some(end) = after.find('}') else {
            return Err(format!("unclosed `${{` in '{value}'"));
        };
        let inner = &after[..end];
        if let Some(credential) = inner.strip_prefix("credential:") {
            if credential.is_empty() || !credential.chars().all(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | '-')) {
                return Err(format!("invalid credential name '{credential}' in '{value}'"));
            }
            match lookup(inner).filter(|v| !v.is_empty()) {
                Some(found) => out.push_str(&found),
                None => return Err(format!("credential {credential} is not saved")),
            }
            rest = &after[end + 1..];
            continue;
        }
        let (name, default) = match inner.split_once(":-") {
            Some((name, default)) => (name, Some(default)),
            None => (inner, None),
        };
        if name.is_empty() || !name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_') {
            return Err(format!("invalid variable name '{name}' in '{value}'"));
        }
        match (lookup(name).filter(|v| default.is_none() || !v.is_empty()), default) {
            (Some(found), _) => out.push_str(&found),
            (None, Some(default)) => out.push_str(default),
            (None, None) => return Err(format!("environment variable {name} is not set")),
        }
        rest = &after[end + 1..];
    }
    out.push_str(rest);
    Ok(out)
}

/// The environment of a local server: base variables, then `env_vars`, then `env` (expanded).
pub fn server_env(
    env_vars: &[String],
    env: &BTreeMap<String, String>,
    lookup: &dyn Fn(&str) -> Option<String>,
    parent: impl Iterator<Item = (String, String)>,
) -> Result<BTreeMap<String, String>, String> {
    let mut out: BTreeMap<String, String> = parent.filter(|(name, _)| is_base(name)).collect();
    for name in env_vars {
        match lookup(name) {
            Some(value) => {
                out.insert(name.clone(), value);
            }
            None => return Err(format!("environment variable {name} (listed in env_vars) is not set")),
        }
    }
    for (name, value) in env {
        out.insert(name.clone(), expand(value, lookup)?);
    }
    Ok(out)
}

/// Looks variables up in her's own environment.
pub fn process_lookup(name: &str) -> Option<String> {
    std::env::var(name).ok()
}

/// The `${credential:<name>}` names in `values`.
pub fn credential_names<'a>(values: impl IntoIterator<Item = &'a str>) -> Vec<String> {
    let mut names = Vec::new();
    for value in values {
        let mut rest = value;
        while let Some(start) = rest.find("${credential:") {
            let after = &rest[start + "${credential:".len()..];
            let Some(end) = after.find('}') else { break };
            let name = after[..end].to_string();
            if !names.contains(&name) {
                names.push(name);
            }
            rest = &after[end + 1..];
        }
    }
    names
}

#[cfg(test)]
mod tests {
    use super::*;

    fn lookup(name: &str) -> Option<String> {
        match name {
            "TOKEN" => Some("t0k".into()),
            "EMPTY" => Some(String::new()),
            "credential:mcp.github" => Some("ghp_x".into()),
            _ => None,
        }
    }

    #[test]
    fn expands_saved_credentials() {
        assert_eq!(expand("Bearer ${credential:mcp.github}", &lookup).unwrap(), "Bearer ghp_x");
        assert_eq!(expand("${credential:mcp.missing}", &lookup).unwrap_err(), "credential mcp.missing is not saved");
        assert!(expand("${credential:bad name}", &lookup).is_err());
        assert_eq!(
            credential_names(["Bearer ${credential:mcp.a}", "${credential:mcp.b}${credential:mcp.a}", "${TOKEN}"]),
            vec!["mcp.a", "mcp.b"]
        );
    }

    #[test]
    fn expands_variables_and_defaults() {
        assert_eq!(expand("Bearer ${TOKEN}", &lookup).unwrap(), "Bearer t0k");
        assert_eq!(expand("${MISSING:-x}/${TOKEN}", &lookup).unwrap(), "x/t0k");
        assert_eq!(expand("${EMPTY:-fallback}", &lookup).unwrap(), "fallback");
        assert_eq!(expand("no vars", &lookup).unwrap(), "no vars");
        assert!(expand("${MISSING}", &lookup).unwrap_err().contains("MISSING"));
        assert!(expand("${OPEN", &lookup).is_err());
    }

    #[test]
    fn environment_keeps_only_the_base_set_and_what_is_named() {
        let parent = vec![
            ("PATH".to_string(), "/bin".to_string()),
            ("AWS_SECRET_ACCESS_KEY".to_string(), "s3cr3t".to_string()),
            ("XDG_CACHE_HOME".to_string(), "/c".to_string()),
        ];
        let env = BTreeMap::from([("API".to_string(), "${TOKEN}".to_string())]);
        let out = server_env(&["TOKEN".to_string()], &env, &lookup, parent.into_iter()).unwrap();
        assert_eq!(out.get("PATH").map(String::as_str), Some("/bin"));
        assert_eq!(out.get("XDG_CACHE_HOME").map(String::as_str), Some("/c"));
        assert_eq!(out.get("TOKEN").map(String::as_str), Some("t0k"));
        assert_eq!(out.get("API").map(String::as_str), Some("t0k"));
        assert!(!out.contains_key("AWS_SECRET_ACCESS_KEY"));
        assert!(server_env(&["NOPE".to_string()], &BTreeMap::new(), &lookup, std::iter::empty()).is_err());
    }
}
