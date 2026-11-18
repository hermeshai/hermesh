//! Refreshes the compiled-in public catalog snapshot (docs/kernel/model-catalog-design.md §5.1).
//!
//! cargo run -p her-llm --example sync_catalog [-- --input api.json] [--lab models.json] [--check] [--canonical-only]
//!
//! Downloads the public catalog (or reads `--input`), projects the providers the built-in
//! catalog names and the model makers, rewrites `crates/her-llm/catalog/public/*.json` and prints what changed.
//! It also writes the canonical models' rows and the alias table (`_canonical.json`,
//! `_aliases.json`, custom-endpoint-design §6.2) from the whole catalog and its lab-level file
//! (`--lab`, else downloaded). `--canonical-only` writes only those two.
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
    let canonical_only = args.iter().any(|a| a == "--canonical-only");
    let lab_input = args.iter().position(|a| a == "--lab").and_then(|i| args.get(i + 1)).cloned();
    let out_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("catalog/public");

    let builtin = Catalog::builtin()?;
    let mut ids: Vec<String> = builtin.providers.values().filter_map(|p| p.listing.catalog_id.clone()).collect();
    ids.extend(public_catalog::MAKERS.iter().map(|id| id.to_string()));
    ids.sort();
    ids.dedup();

    // The kernel's own client: system certificates, her's User-Agent.
    let http = her_llm::ModelRegistry::new(Catalog::default(), Default::default()).http_client()?;
    let download = |url: String| {
        let http = http.clone();
        async move {
            let response = http.get(url).send().await?.error_for_status()?;
            Ok::<serde_json::Value, Box<dyn std::error::Error>>(serde_json::from_slice(&response.bytes().await?)?)
        }
    };
    let raw: serde_json::Value = match input {
        Some(path) => serde_json::from_slice(&std::fs::read(path)?)?,
        None => download(public_catalog::SOURCE_URL.to_string()).await?,
    };
    let lab: serde_json::Value = match lab_input {
        Some(path) => serde_json::from_slice(&std::fs::read(path)?)?,
        None => download(her_llm::canonical::lab_url(public_catalog::SOURCE_URL).expect("the lab file's address")).await?,
    };
    let now = her_llm::discovery::now();
    let canonical = her_llm::canonical::project(&raw, Some(&lab), &now)?;
    let mut stale = write_canonical(&out_dir, &canonical, check)?;
    if canonical_only {
        if check && stale {
            eprintln!("the canonical models are out of date");
            std::process::exit(1);
        }
        return Ok(());
    }
    let refs: Vec<&str> = ids.iter().map(String::as_str).collect();
    let projection = public_catalog::project(&raw, &refs, &now)?;
    // The snapshot has to cover every id: the service tolerates a provider going missing upstream,
    // this does not.
    if !projection.failed.is_empty() {
        eprintln!("{}", projection.failed.join("\n"));
        std::process::exit(1);
    }
    let projected = projection.providers;

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

/// Writes the canonical rows and the alias table when they changed; returns whether they did.
fn write_canonical(out_dir: &std::path::Path, projected: &her_llm::canonical::Projected, check: bool) -> Result<bool, Box<dyn std::error::Error>> {
    use her_llm::canonical::{ALIASES, AliasFile, CANONICAL};
    let rows_path = out_dir.join(format!("{CANONICAL}.json"));
    let aliases_path = out_dir.join(format!("{ALIASES}.json"));
    let old_rows: Option<PublicProvider> = std::fs::read_to_string(&rows_path).ok().and_then(|t| serde_json::from_str(&t).ok());
    let old_aliases: Option<AliasFile> = std::fs::read_to_string(&aliases_path).ok().and_then(|t| serde_json::from_str(&t).ok());
    let report = diff(old_rows.as_ref(), &projected.canonical);
    let names_changed = old_aliases.as_ref().map(|a| &a.names) != Some(&projected.aliases.names);
    if !report.is_empty() {
        println!("{CANONICAL}: {} canonical models, {} changed", projected.canonical.models.len(), report.len());
    }
    if names_changed {
        let before = old_aliases.map_or(0, |a| a.names.len());
        println!("{ALIASES}: {} names (was {before})", projected.aliases.names.len());
    }
    let changed = !report.is_empty() || names_changed;
    if changed && !check {
        std::fs::write(&rows_path, public_catalog::to_file(&projected.canonical))?;
        std::fs::write(&aliases_path, her_llm::canonical::to_alias_file(&projected.aliases))?;
    }
    Ok(changed)
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
