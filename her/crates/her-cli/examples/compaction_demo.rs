//! Watch context compaction on the real `her` binary against a simulated model service.
//!
//!     cargo build -p her-cli && cargo run -p her-cli --example compaction_demo
//!
//! Options: `--steps N` (default 24), `--window N` to make the provider reject requests over
//! N tokens (a provider whose real window is smaller than configured), `--keep` to keep the
//! temporary data directory for inspection.

#![allow(clippy::print_stdout, clippy::print_stderr)]

#[path = "../tests/mock_model/mod.rs"]
mod mock_model;

use mock_model::Behavior;
use mock_model::Kind;
use mock_model::MockModel;
use serde_json::Value;
use std::path::PathBuf;
use std::process::Command;

fn main() {
    let args: Vec<String> = std::env::args().collect();
    let value = |name: &str| {
        args.iter()
            .position(|a| a == name)
            .and_then(|i| args.get(i + 1))
            .and_then(|v| v.parse::<u64>().ok())
    };
    let steps = value("--steps").unwrap_or(24) as u32;
    let window = value("--window");
    let keep = args.iter().any(|a| a == "--keep");

    let server = MockModel::start(Behavior {
        steps,
        lines_per_step: 2000,
        read_back_after: Some(2),
        window,
    });
    let root = std::env::temp_dir().join(format!("her-compaction-demo-{}", std::process::id()));
    let home = root.join("home");
    let work = root.join("work");
    std::fs::create_dir_all(&home).unwrap();
    std::fs::create_dir_all(&work).unwrap();
    std::fs::write(
        home.join("models.toml"),
        format!(
            "[providers.mock]\napi = \"openai-chat\"\nbase_url = \"{}\"\napi_key = \"sk-mock\"\n\
             compat = {{ max_tokens_field = \"max_tokens\" }}\n\n[[providers.mock.models]]\nid = \"mock-model\"\n\
             context_window = 16000\nmax_output_tokens = 2000\n",
            server.base_url()
        ),
    )
    .unwrap();
    std::fs::write(home.join("config.toml"), "model = \"mock/mock-model\"\n").unwrap();

    let her = std::env::current_exe()
        .unwrap()
        .parent()
        .and_then(|examples| examples.parent())
        .map(|dir| dir.join("her"))
        .unwrap();
    let run = |extra: &[&str]| -> Vec<Value> {
        let output = Command::new(&her)
            .args(["exec", "--output", "stream-json", "--approval", "full-auto"])
            .args(extra)
            .current_dir(&work)
            .env("HER_HOME", &home)
            .output()
            .expect("run her (build it first: cargo build -p her-cli)");
        String::from_utf8_lossy(&output.stdout)
            .lines()
            .filter_map(|l| serde_json::from_str(l).ok())
            .collect()
    };

    println!("simulated model service at {}", server.base_url());
    println!("configured window 16,000 tokens (soft limit 12,600, hard limit 14,000)");
    if let Some(window) = window {
        println!("the provider's real window: {window} tokens");
    }
    println!();
    let events = run(&["Run the steps of the job one at a time until all of them are complete."]);
    report_events(&events);
    let requests = server.requests();
    report_requests(&requests, 0);

    let session_id = events
        .iter()
        .find(|e| e["type"] == "session_started")
        .and_then(|e| e["session_id"].as_str())
        .unwrap_or_default()
        .to_string();
    println!("\n--- her exec --resume {session_id} \"Is everything done?\"");
    let resumed = run(&["--resume", &session_id, "Is everything done?"]);
    report_events(&resumed);
    report_requests(&server.requests(), requests.len());

    if let Some(read_back) = server.read_back() {
        println!(
            "\nread-back of a cut output: lines {}-{} of {} in {} read call(s) → {}",
            read_back.first,
            read_back.last,
            read_back.path,
            read_back.pages,
            if read_back.verified {
                "head + read-back + tail match the command's output byte for byte"
            } else {
                "MISMATCH"
            }
        );
    }
    if keep {
        println!("\ndata kept in {}", root.display());
    } else {
        let _ = std::fs::remove_dir_all(&root);
    }
}

fn report_events(events: &[Value]) {
    for event in events {
        match event["type"].as_str().unwrap_or_default() {
            "session_started" => println!(
                "session {} · context at start: {} tokens ({} estimated)",
                event["session_id"].as_str().unwrap_or_default(),
                event["context"]["tokens"],
                event["context"]["estimated"]
            ),
            "compaction_started" => println!("  compaction started ({})", event["reason"].as_str().unwrap_or_default()),
            "compaction_completed" => println!(
                "  compaction #{} done: {} → {} tokens, kept {} user message(s)",
                event["compaction_seq"], event["tokens_before"], event["tokens_after"], event["retained_messages"]
            ),
            "compaction_failed" => println!("  compaction failed: {}", event["error"]["message"]),
            "warning" => println!("  warning: {}", event["message"].as_str().unwrap_or_default()),
            "task_completed" => println!(
                "task completed: {}",
                event["last_message"].as_str().unwrap_or_default()
            ),
            "task_failed" => println!("task failed: {}", event["error"]["message"]),
            _ => {}
        }
    }
}

fn report_requests(requests: &[mock_model::Recorded], from: usize) {
    println!("\n  #  kind      messages  prompt  cached   hit");
    for (i, r) in requests.iter().enumerate().skip(from) {
        let kind = match r.kind {
            Kind::Turn => "turn",
            Kind::Handoff => "SUMMARY",
            Kind::Rejected => "REJECTED",
        };
        let hit = if r.prompt_tokens > 0 {
            format!("{:>4.0}%", 100.0 * r.cached_tokens as f64 / r.prompt_tokens as f64)
        } else {
            String::new()
        };
        println!(
            "{:>3}  {kind:<9} {:>8}  {:>6}  {:>6}  {hit}",
            i + 1,
            r.messages().len(),
            r.prompt_tokens,
            r.cached_tokens
        );
    }
    let _ = PathBuf::new();
}
