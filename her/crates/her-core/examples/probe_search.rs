//! One real search service, through the pipeline the tool uses (docs/kernel/web-search-design.md §7):
//! queries whose answers are known, and whether the text the model would read has them.
//!
//!   cargo run -p her-core --example probe_search -- exa
//!   TAVILY_API_KEY=... cargo run -p her-core --example probe_search -- tavily --show
//!
//! DeepSeek takes its model account's key (`DEEPSEEK_API_KEY`). To leave the keychain alone, run it
//! with `HER_CREDENTIAL_STORE=file` and a `HER_HOME` of its own.

#![allow(clippy::print_stdout, clippy::print_stderr)]

use std::sync::Arc;
use std::time::Instant;

use her_core::config::WebSearchSettings;
use her_core::web_search;
use her_llm::Catalog;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use tokio_util::sync::CancellationToken;

/// As much as a search may leave in the conversation by default, roughly (§5.5).
const BUDGET: usize = 24_000;

/// A query, and the facts its answer should hold: each is any of its `|` spellings.
const CASES: [(&str, &[&str]); 6] = [
    ("贵州茅台 2026 年第三季度 营业收入 同比", &["402.67|40,267|402.7", "3.08"]),
    ("中国人民银行 2026年10月 LPR 报价", &["3.0", "3.5"]),
    ("DeepSeek Search API pricing per 1000 requests", &["0.75"]),
    ("Rust tokio JoinSet abort_all documentation", &["abort_all"]),
    ("OpenAI Responses web_search allowed_domains filters", &["allowed_domains"]),
    ("宁德时代 2026 年三季报 净利润", &["净利润"]),
];

#[tokio::main]
async fn main() {
    let mut args = std::env::args().skip(1);
    let Some(service) = args.next() else {
        eprintln!("usage: probe_search <deepseek|exa|parallel|tavily|brave|bocha> [--show]");
        std::process::exit(2);
    };
    let show = args.next().as_deref() == Some("--show");
    let llm = Arc::new(ModelRegistry::new(Catalog::builtin().unwrap(), RegistryOptions::default()));
    let settings = WebSearchSettings::default();
    let cancel = CancellationToken::new();
    let (mut found, mut facts, mut failed) = (0, 0, 0);
    for (query, expected) in CASES {
        let started = Instant::now();
        match web_search::probe(&settings, &llm, &service, query, BUDGET, &cancel).await {
            Ok(text) => {
                let has: Vec<bool> = expected.iter().map(|fact| fact.split('|').any(|spelling| text.contains(spelling))).collect();
                found += has.iter().filter(|h| **h).count();
                facts += has.len();
                let results = text.lines().filter(|line| line.starts_with("http://") || line.starts_with("https://")).count();
                println!("{:>5.1}s  {results:>2} results  facts {}/{}  {query}", started.elapsed().as_secs_f32(), has.iter().filter(|h| **h).count(), has.len());
                if show {
                    println!("{text}\n");
                }
            }
            Err(error) => {
                failed += 1;
                println!("{:>5.1}s  failed: {error}  {query}", started.elapsed().as_secs_f32());
            }
        }
    }
    println!("{service}: facts {found}/{facts}, {failed} failed");
    if failed > 0 {
        std::process::exit(1);
    }
}
