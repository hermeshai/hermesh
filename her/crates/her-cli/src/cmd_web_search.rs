//! `her web-search`: which service searches use, its key, and a real search to try it.

use her_core::runtime::Workspace;
use her_core::web_search;
use her_protocol::web_search::WebSearchSetKeyParams;
use std::sync::Arc;
use tokio_util::sync::CancellationToken;

use crate::setup;

fn plain(value: &serde_json::Value) -> String {
    value.as_str().map_or_else(|| value.to_string(), str::to_string)
}

pub fn status(overrides: &[String], as_json: bool) -> anyhow::Result<u8> {
    let rt = setup::init_local(std::env::current_dir()?, overrides)?;
    let status = web_search::status(&rt.config.web_search, &rt.llm);
    if as_json {
        println!("{}", serde_json::to_string_pretty(&status)?);
        return Ok(0);
    }
    if !status.enabled {
        println!("web search is off: her config set web_search.enabled true");
    }
    for source in &status.sources {
        let state = match (source.enabled, source.configured) {
            (false, _) => "off",
            (true, false) => "not set up",
            (true, true) if status.current.as_deref() == Some(&source.id) => "in use",
            (true, true) => "ready",
        };
        let key = match (&source.key, &source.key_env) {
            (_, Some(env)) => format!("  key from {env}"),
            (Some(_), None) => "  key saved".to_string(),
            (None, None) => String::new(),
        };
        println!("{:<12}{:<14}{state}{key}", source.id, source.name);
    }
    Ok(0)
}

/// The key comes from standard input, so that it stays out of the shell's history.
pub async fn set_key(overrides: &[String], service: &str) -> anyhow::Result<u8> {
    let key = crate::cmd_login::read_key()?;
    let (server, _guard) = crate::cmd_serve::start(overrides)?;
    let result = server
        .web_search_set_key(WebSearchSetKeyParams { source: service.to_string(), key })
        .await
        .map_err(|err| anyhow::anyhow!("{}", err.message))?;
    if !result.saved {
        anyhow::bail!("{service} refused the key; nothing was saved");
    }
    let found = if result.test.ok {
        format!("a test search found {}", result.test.count)
    } else {
        "the test search failed, for a reason other than the key".to_string()
    };
    println!("{service} key saved in the credential store; {found}");
    Ok(0)
}

pub async fn check(overrides: &[String], service: Option<String>, as_json: bool) -> anyhow::Result<u8> {
    let home = setup::home()?;
    let mut warnings = Vec::new();
    let all: Vec<String> = setup::env_overrides(&mut warnings)?.into_iter().chain(overrides.iter().cloned()).collect();
    let workspace = Workspace::open(&home, &std::env::current_dir()?, &all)?;
    let llm = Arc::new(her_core::runtime::model_registry(&home, &workspace.config)?);
    let settings = &workspace.config.web_search;
    let id = match service.or_else(|| web_search::status(settings, &llm).current) {
        Some(id) => id,
        None => anyhow::bail!("no search service is ready: `her web-search status` lists them"),
    };
    let result = web_search::test(settings, &llm, &id, None, &CancellationToken::new()).await.map_err(anyhow::Error::msg)?;
    if as_json {
        println!("{}", serde_json::json!({ "service": id, "result": result }));
    } else if result.ok {
        println!("{id}: {} result(s) in {} ms", result.count, result.elapsed_ms);
    } else {
        let why = result.error.map_or_else(|| "no reason given".to_string(), |kind| serde_json::to_value(kind).map_or_else(|_| String::new(), |v| crate::cmd_web_search::plain(&v)));
        println!("{id}: failed: {why}");
    }
    Ok(u8::from(!result.ok))
}
