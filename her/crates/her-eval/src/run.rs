//! One run: a case's site, `her exec` with only the browser tools, and the verdict.

use crate::case::Case;
use crate::site::Request;
use crate::site::Site;
use serde::Serialize;
use serde_json::Value;
use std::collections::BTreeMap;
use std::ffi::OsString;
use std::path::PathBuf;
use std::process::Stdio;
use std::time::Duration;
use std::time::Instant;
use tokio::process::Command;

/// How runs start `her`.
#[derive(Debug, Clone)]
pub struct Runner {
    pub her: PathBuf,
    /// `provider/model`; the configured default when `None`.
    pub model: Option<String>,
    pub thinking: Option<String>,
    /// Extra `-c KEY=VALUE` overrides, after the eval's own.
    pub config: Vec<String>,
    /// Extra environment, e.g. a private `HER_HOME`.
    pub env: Vec<(OsString, OsString)>,
    pub timeout: Duration,
    /// Keep each run's session log. Off by default: every run works in a temporary folder,
    /// so a kept session shows in the session list (and the desktop's sidebar) as a project
    /// of its own.
    pub keep_sessions: bool,
}

/// Counts of one tool's calls.
#[derive(Debug, Clone, Default, Serialize)]
pub struct ToolCount {
    pub ok: u32,
    pub error: u32,
}

#[derive(Debug, Clone, Serialize)]
pub struct RunResult {
    pub case: String,
    pub model: String,
    pub attempt: u32,
    pub passed: bool,
    /// The run never got going: her did not start or failed before any tool call.
    pub stalled: bool,
    /// Broken checks, or why the run itself failed.
    pub failures: Vec<String>,
    pub turns: u64,
    pub duration_ms: u64,
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub tools: BTreeMap<String, ToolCount>,
    pub answer: String,
    /// What the site received.
    pub requests: Vec<Request>,
    pub session_file: Option<String>,
}

impl RunResult {
    pub fn tool_calls(&self) -> u32 {
        self.tools.values().map(|c| c.ok + c.error).sum()
    }

    pub fn tool_errors(&self) -> u32 {
        self.tools.values().map(|c| c.error).sum()
    }
}

impl Runner {
    /// The overrides every run gets: the browser on, in her's own browser and its own
    /// profile (never the user's Chrome or everyday profile), and no other tools, so the
    /// model cannot reach the site with curl instead.
    fn eval_config(&self) -> Vec<String> {
        let mut config = vec![
            "browser.enabled=true".to_string(),
            "browser.backend=\"managed\"".to_string(),
            "browser.profile=\"eval\"".to_string(),
            "tools.enabled=[]".to_string(),
        ];
        config.extend(self.config.iter().cloned());
        config
    }

    pub async fn run(&self, case: &Case, attempt: u32) -> RunResult {
        let model = self.model.clone().unwrap_or_else(|| "default".into());
        let mut result = RunResult {
            case: case.name.clone(),
            model,
            attempt,
            passed: false,
            stalled: false,
            failures: Vec::new(),
            turns: 0,
            duration_ms: 0,
            input_tokens: 0,
            output_tokens: 0,
            tools: BTreeMap::new(),
            answer: String::new(),
            requests: Vec::new(),
            session_file: None,
        };
        let site = match Site::start(&case.dir).await {
            Ok(site) => site,
            Err(e) => {
                result.stalled = true;
                result.failures.push(format!("could not start the site: {e}"));
                return result;
            }
        };
        let started = Instant::now();
        let outcome = self.exec(case, &site.base).await;
        result.duration_ms = started.elapsed().as_millis() as u64;
        result.requests = site.requests();
        let report = match outcome {
            Ok(report) => report,
            Err(e) => {
                result.stalled = true;
                result.failures.push(e);
                return result;
            }
        };
        result.absorb(&report);
        if let Some(error) = report.get("error").filter(|e| !e.is_null()) {
            let message = error.get("message").and_then(Value::as_str).unwrap_or("failed");
            result.failures.push(format!("the run ended with an error: {message}"));
            result.stalled = result.tool_calls() == 0;
        }
        result.failures.extend(case.judge(&result.requests, &result.answer));
        result.passed = result.failures.is_empty();
        result
    }

    /// The `--output json` result of `her exec`.
    async fn exec(&self, case: &Case, base: &str) -> Result<Value, String> {
        // A fresh project per run, so project config stays out of the way.
        let work = tempfile::tempdir().map_err(|e| format!("temp dir: {e}"))?;
        let mut cmd = Command::new(&self.her);
        cmd.arg("exec").arg("--output").arg("json").arg("--max-turns").arg(case.max_turns.to_string());
        if !self.keep_sessions {
            cmd.arg("--no-save");
        }
        if let Some(model) = &self.model {
            cmd.arg("--model").arg(model);
        }
        if let Some(thinking) = &self.thinking {
            cmd.arg("--thinking").arg(thinking);
        }
        for item in self.eval_config() {
            cmd.arg("-c").arg(item);
        }
        cmd.arg("--cwd").arg(work.path()).arg(case.prompt_for(base));
        cmd.current_dir(work.path())
            .envs(self.env.iter().cloned())
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        let child = cmd.spawn().map_err(|e| format!("could not start {}: {e}", self.her.display()))?;
        let output = tokio::time::timeout(self.timeout, child.wait_with_output())
            .await
            .map_err(|_| format!("timed out after {}s", self.timeout.as_secs()))?
            .map_err(|e| format!("her exec: {e}"))?;
        serde_json::from_slice(&output.stdout).map_err(|_| {
            let stderr = String::from_utf8_lossy(&output.stderr);
            let tail: Vec<&str> = stderr.lines().rev().take(5).collect();
            format!(
                "her exec gave no result ({}): {}",
                output.status,
                tail.into_iter().rev().collect::<Vec<_>>().join(" | ")
            )
        })
    }
}

impl RunResult {
    fn absorb(&mut self, report: &Value) {
        let number = |v: &Value| v.as_u64().unwrap_or(0);
        if let Some(model) = report["model"].as_str().filter(|m| !m.is_empty()) {
            self.model = model.to_string();
        }
        self.turns = number(&report["turns"]);
        self.input_tokens = number(&report["usage"]["input_tokens"])
            + number(&report["usage"]["cache_read_tokens"])
            + number(&report["usage"]["cache_write_tokens"]);
        self.output_tokens = number(&report["usage"]["output_tokens"]);
        self.answer = report["final_output"].as_str().unwrap_or_default().to_string();
        self.session_file = report["session_file"].as_str().map(str::to_string);
        for call in report["tool_calls"].as_array().into_iter().flatten() {
            let count = self.tools.entry(call["name"].as_str().unwrap_or("?").to_string()).or_default();
            if call["status"] == "ok" {
                count.ok += 1;
            } else {
                count.error += 1;
            }
        }
    }
}
