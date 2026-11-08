//! `her-eval`: run the browser cases against one or more models and report.

use clap::Parser;
use her_eval::Case;
use her_eval::RunResult;
use her_eval::Runner;
use her_eval::report;
use std::io::Write as _;
use std::path::PathBuf;
use std::time::Duration;

#[derive(Parser)]
#[command(about = "Evaluate the browser tools with real models on local fixture sites")]
struct Args {
    /// Model as provider/model-id (repeatable); the configured default when omitted.
    #[arg(short, long)]
    model: Vec<String>,
    /// Only cases whose name contains this (repeatable).
    #[arg(long)]
    case: Vec<String>,
    /// Runs of each case per model.
    #[arg(long, default_value_t = 1)]
    repeat: u32,
    /// Thinking level passed to `her exec`.
    #[arg(long)]
    thinking: Option<String>,
    /// Extra configuration for `her exec`, e.g. -c browser.headless=false (repeatable).
    #[arg(short = 'c', long = "config", value_name = "KEY=VALUE")]
    config: Vec<String>,
    /// Seconds a run may take.
    #[arg(long, default_value_t = 300)]
    timeout: u64,
    /// The `her` binary; by default the one built next to this program, else `her`.
    #[arg(long)]
    her: Option<PathBuf>,
    /// Directory of cases.
    #[arg(long, default_value = concat!(env!("CARGO_MANIFEST_DIR"), "/cases"))]
    cases: PathBuf,
    /// Where to write the JSON report (every run with its answer and requests).
    #[arg(long)]
    out: Option<PathBuf>,
    /// Keep each run's session log (in the report as `session_file`), to read a run back.
    /// Each shows in the session list as a project of its own.
    #[arg(long)]
    keep_sessions: bool,
    /// List the cases and exit.
    #[arg(long)]
    list: bool,
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let args = Args::parse();
    let mut stdout = std::io::stdout();
    let cases: Vec<Case> = Case::load_all(&args.cases)?
        .into_iter()
        .filter(|case| args.case.is_empty() || args.case.iter().any(|name| case.name.contains(name.as_str())))
        .collect();
    if args.list {
        for case in &cases {
            writeln!(stdout, "{:<22} {}", case.name, case.description)?;
        }
        return Ok(());
    }
    anyhow::ensure!(!cases.is_empty(), "no case matches");

    let her = args.her.clone().unwrap_or_else(default_her);
    let models: Vec<Option<String>> = if args.model.is_empty() {
        vec![None]
    } else {
        args.model.iter().cloned().map(Some).collect()
    };
    let mut results: Vec<RunResult> = Vec::new();
    for model in models {
        let runner = Runner {
            her: her.clone(),
            model,
            thinking: args.thinking.clone(),
            config: args.config.clone(),
            env: Vec::new(),
            timeout: Duration::from_secs(args.timeout),
            keep_sessions: args.keep_sessions,
        };
        let mut first = true;
        'cases: for case in &cases {
            for attempt in 1..=args.repeat {
                let result = runner.run(case, attempt).await;
                writeln!(stdout, "{}", report::run_line(&result))?;
                let stalled = result.stalled;
                results.push(result);
                // Most likely a setup problem (no key, unknown model) that every run would hit.
                if std::mem::replace(&mut first, false) && stalled {
                    writeln!(stdout, "      the first run could not start; skipping the rest for this model")?;
                    break 'cases;
                }
            }
        }
    }
    write!(stdout, "{}", report::summary(&results))?;

    let out = args.out.unwrap_or_else(|| {
        let stamp = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap_or_default().as_secs();
        std::env::temp_dir().join(format!("her-eval-{stamp}.json"))
    });
    std::fs::write(&out, serde_json::to_string_pretty(&results)?)?;
    writeln!(stdout, "report: {}", out.display())?;
    if results.iter().all(|r| r.passed) { Ok(()) } else { std::process::exit(1) }
}

/// `her` built into the same directory as this program (`cargo run -p her-eval` after
/// `cargo build -p her-cli`), else whatever `her` is on PATH.
fn default_her() -> PathBuf {
    std::env::current_exe()
        .ok()
        .and_then(|exe| exe.parent().map(|dir| dir.join("her")))
        .filter(|path| path.is_file())
        .unwrap_or_else(|| PathBuf::from("her"))
}
