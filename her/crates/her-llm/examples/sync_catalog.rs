//! Refreshes the compiled-in public catalog snapshot (docs/kernel/model-catalog-design.md §5.1).
//!
//! cargo run -p her-llm --example sync_catalog [-- --input api.json] [--check]
//!
//! Downloads the public catalog (or reads `--input`), projects the providers the built-in
//! catalog names and the model makers, rewrites `crates/her-llm/catalog/public/*.json` and prints what changed.
//! `--check` only reports, and exits 1 when the snapshot is out of date.
#![allow(clippy::print_stdout, clippy::print_stderr)]

use her_llm::Catalog;
use her_llm::catalog::ProviderConfig;
use her_llm::catalog::ModelToml;
use her_llm::catalog::offered_by_row;
use her_llm::directory::derive_thinking;
use her_llm::public_catalog;
use her_llm::public_catalog::PublicModel;
use her_llm::public_catalog::PublicProvider;
use std::collections::BTreeMap;
use std::path::PathBuf;

#[tokio::main(flavor = "current_thread")]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let input = args.iter().position(|a| a == "--input").and_then(|i| args.get(i + 1)).cloned();
    let check = args.iter().any(|a| a == "--check");
    let out_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("catalog/public");

    let builtin = Catalog::builtin()?;
    let mut ids: Vec<String> = builtin.providers.values().filter_map(|p| p.listing.catalog_id.clone()).collect();
    ids.extend(public_catalog::MAKERS.iter().map(|id| id.to_string()));
    ids.sort();
    ids.dedup();

    let raw: serde_json::Value = match input {
        Some(path) => serde_json::from_slice(&std::fs::read(path)?)?,
        None => {
            // The kernel's own client: system certificates, her's User-Agent.
            let http = her_llm::ModelRegistry::new(Catalog::default(), Default::default()).http_client()?;
            let response = http
                .get(public_catalog::SOURCE_URL)
                .send()
                .await?
                .error_for_status()?;
            serde_json::from_slice(&response.bytes().await?)?
        }
    };
    let refs: Vec<&str> = ids.iter().map(String::as_str).collect();
    let projection = public_catalog::project(&raw, &refs, &her_llm::discovery::now())?;
    // The snapshot has to cover every id: the service tolerates a provider going missing upstream,
    // this does not.
    if !projection.failed.is_empty() {
        eprintln!("{}", projection.failed.join("\n"));
        std::process::exit(1);
    }
    let projected = projection.providers;

    let mut stale = false;
    for (id, provider) in &projected {
        let path = out_dir.join(format!("{id}.json"));
        let old: Option<PublicProvider> =
            std::fs::read_to_string(&path).ok().and_then(|t| serde_json::from_str(&t).ok());
        let report = diff(old.as_ref(), provider);
        if !report.is_empty() {
            stale = true;
            println!("{id}:");
            for line in report {
                println!("  {line}");
            }
        }
        if !check && old.as_ref().map(|o| &o.models) != Some(&provider.models) {
            std::fs::write(&path, public_catalog::to_file(provider))?;
        }
    }
    for (pid, provider) in &builtin.providers {
        let Some(public) = provider.listing.catalog_id.as_ref().and_then(|id| projected.get(id)) else {
            continue;
        };
        for entry in &provider.listing.entries {
            for field in redundant(provider, entry, public.model(&entry.id)) {
                println!("{pid}/{}: `{field}` in builtin.toml now matches the catalog and can go", entry.id);
            }
        }
    }
    if check && stale {
        eprintln!("the public catalog snapshot is out of date");
        std::process::exit(1);
    }
    Ok(())
}

/// Added, removed and changed models, one line each.
fn diff(old: Option<&PublicProvider>, new: &PublicProvider) -> Vec<String> {
    let before: BTreeMap<&str, &PublicModel> =
        old.map(|o| o.models.iter().map(|m| (m.id.as_str(), m)).collect()).unwrap_or_default();
    let after: BTreeMap<&str, &PublicModel> = new.models.iter().map(|m| (m.id.as_str(), m)).collect();
    let mut lines = Vec::new();
    for (id, model) in &after {
        match before.get(id) {
            None => lines.push(format!("+ {id}")),
            Some(prev) if prev != model => lines.push(format!("~ {id}")),
            Some(_) => {}
        }
    }
    for id in before.keys().filter(|id| !after.contains_key(*id)) {
        lines.push(format!("- {id} (removed upstream: check before merging)"));
    }
    lines
}

/// Fields of a built-in entry that say the same as the catalog.
fn redundant(provider: &ProviderConfig, entry: &ModelToml, public: Option<&PublicModel>) -> Vec<&'static str> {
    let Some(p) = public else { return Vec::new() };
    let mut out = Vec::new();
    if entry.context_window.is_some() && entry.context_window == p.context_window {
        out.push("context_window");
    }
    if entry.max_output_tokens.is_some() && entry.max_output_tokens == p.max_output_tokens {
        out.push("max_output_tokens");
    }
    if entry.reasoning.is_some() && entry.reasoning == p.reasoning {
        out.push("reasoning");
    }
    if entry.name.is_some() && entry.name == p.name {
        out.push("name");
    }
    if let Some(levels) = &entry.thinking
        && *levels == offered_by_row(&derive_thinking(&p.reasoning_options).0, &provider.api, provider.compat.thinking_format)
    {
        out.push("thinking");
    }
    out
}
