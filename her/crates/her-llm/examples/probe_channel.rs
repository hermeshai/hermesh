//! Asks a channel's real models a one-line question at every level their menu offers, to check a
//! channel or a `thinking_format` against the real service (docs/kernel/model-catalog-design.md
//! §18.7). This spends the channel's quota: little, since each request is a few dozen tokens.
//!
//! cargo run -p her-llm --example probe_channel -- <provider> [model-id ...]
//!
//! Without model ids: the chat models of its menu, as `her models` lists them. Keys and lists are
//! those of `HER_HOME` (default `~/.her`). Prints each model and level with what came back:
//! `ok` and the reasoning tokens, or the provider's error.
#![allow(clippy::print_stdout, clippy::print_stderr)]

use her_llm::Catalog;
use her_llm::Context;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use her_llm::RequestOptions;
use her_llm::ThinkingLevel;
use her_llm::stream::StreamEvent;
use her_llm::types::Message;
use her_llm::types::UserMessage;
use futures::StreamExt;
use std::path::PathBuf;
use std::sync::Arc;

const QUESTION: &str = "How many times does the letter r appear in \"strawberry raspberry\"? Answer with the number only.";
/// Requests at once: a plan's limit on concurrency is not what is being checked.
const CONCURRENCY: usize = 3;

#[tokio::main(flavor = "current_thread")]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut args = std::env::args().skip(1);
    let Some(provider_id) = args.next() else {
        eprintln!("usage: probe_channel <provider> [model-id ...]");
        std::process::exit(2);
    };
    let chosen: Vec<String> = args.collect();
    let home = std::env::var_os("HER_HOME")
        .map(PathBuf::from)
        .or_else(|| std::env::var_os("HOME").map(|h| PathBuf::from(h).join(".her")))
        .ok_or("no home directory")?;
    if let Some(store) = her_llm::credentials::default_store(home.join("credentials.toml")) {
        her_llm::credentials::install(store);
    }
    let catalog = Catalog::load(Some(&home.join("models.toml")))?;
    let options = RegistryOptions { models_dir: Some(home.join("models")), ..Default::default() };
    let llm = Arc::new(ModelRegistry::new(catalog, options));
    let provider = llm.provider(&provider_id).ok_or_else(|| format!("unknown provider '{provider_id}'"))?;
    let merged = llm.directory().models(&provider);
    let by_default = merged.default_shown(provider.listing.from.is_some());
    let selection = llm.directory().selection();
    let models: Vec<_> = merged
        .models
        .iter()
        .filter(|m| {
            if chosen.is_empty() {
                m.meta.chat && selection.is_shown(&m.model.reference(), by_default.contains(&m.model.id))
            } else {
                chosen.contains(&m.model.id)
            }
        })
        .map(|m| m.model.clone())
        .collect();
    let jobs: Vec<_> = models
        .iter()
        .flat_map(|model| std::iter::once(ThinkingLevel::Default).chain(model.thinking.iter().copied()).map(move |level| (model.clone(), level)))
        .collect();
    println!("{} requests to {}", jobs.len(), provider.base_url);
    let results: Vec<_> = futures::stream::iter(jobs)
        .map(|(model, level)| {
            let llm = llm.clone();
            async move {
                let context = Context {
                    system: String::new(),
                    messages: vec![Message::from(UserMessage::text(QUESTION))],
                    tools: Vec::new(),
                };
                let options = RequestOptions { thinking: level, ..Default::default() };
                let mut stream = llm.stream(model.clone(), context, options);
                let mut outcome = "no answer".to_string();
                while let Some(event) = stream.next().await {
                    match event {
                        StreamEvent::Done(message) => {
                            outcome = format!(
                                "ok    reasoning {:>6}  output {:>6}  {}",
                                message.usage.reasoning.map_or("?".to_string(), |r| r.to_string()),
                                message.usage.output,
                                message.text().trim().chars().take(30).collect::<String>(),
                            );
                        }
                        StreamEvent::Failed(message) => {
                            let error = message.error.as_ref().map_or("failed".to_string(), |e| e.message.clone());
                            outcome = format!("ERROR {}", error.chars().take(160).collect::<String>());
                        }
                        _ => {}
                    }
                }
                println!("{:<32} {:<8} {outcome}", model.id, level.as_str());
                (model.id.clone(), level, outcome)
            }
        })
        .buffer_unordered(CONCURRENCY)
        .collect()
        .await;
    let failed = results.iter().filter(|(_, _, outcome)| outcome.starts_with("ERROR") || outcome == "no answer").count();
    println!("{} of {} failed", failed, results.len());
    std::process::exit(i32::from(failed > 0));
}
