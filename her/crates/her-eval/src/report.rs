//! The summary of a set of runs: a line per run as it finishes, then a table per model.

use crate::run::RunResult;
use std::collections::BTreeMap;
use std::fmt::Write as _;

/// One line for a finished run.
pub fn run_line(result: &RunResult) -> String {
    let verdict = if result.passed { "PASS" } else { "FAIL" };
    let mut line = format!(
        "{verdict}  {:<22} {:<28} turns {:>2}  tools {:>2} ({} err)  {:>6.1}s  {:>7} tok",
        result.case,
        result.model,
        result.turns,
        result.tool_calls(),
        result.tool_errors(),
        result.duration_ms as f64 / 1000.0,
        result.input_tokens + result.output_tokens,
    );
    for failure in &result.failures {
        let _ = write!(line, "\n      - {failure}");
    }
    line
}

/// Per model: pass rate and means over its runs; then which cases each model failed.
pub fn summary(results: &[RunResult]) -> String {
    let mut by_model: BTreeMap<&str, Vec<&RunResult>> = BTreeMap::new();
    for result in results {
        by_model.entry(&result.model).or_default().push(result);
    }
    let mut out = String::new();
    let _ = writeln!(
        out,
        "\n{:<28} {:>9} {:>10} {:>10} {:>11} {:>10}",
        "model", "pass", "turns", "tools", "tokens", "seconds"
    );
    for (model, runs) in &by_model {
        let n = runs.len() as f64;
        let passed = runs.iter().filter(|r| r.passed).count();
        let mean = |f: &dyn Fn(&RunResult) -> f64| runs.iter().map(|r| f(r)).sum::<f64>() / n;
        let _ = writeln!(
            out,
            "{:<28} {:>4}/{:<4} {:>10.1} {:>10.1} {:>11.0} {:>10.1}",
            model,
            passed,
            runs.len(),
            mean(&|r| r.turns as f64),
            mean(&|r| r.tool_calls() as f64),
            mean(&|r| (r.input_tokens + r.output_tokens) as f64),
            mean(&|r| r.duration_ms as f64 / 1000.0),
        );
    }
    for (model, runs) in &by_model {
        let failed: Vec<&str> = runs.iter().filter(|r| !r.passed).map(|r| r.case.as_str()).collect();
        if !failed.is_empty() {
            let _ = writeln!(out, "{model} failed: {}", failed.join(", "));
        }
    }
    out
}
