use async_trait::async_trait;
use schemars::JsonSchema;
use serde::Deserialize;
use serde_json::json;
use std::collections::HashMap;
use std::time::Duration;

use crate::env::CaptureConfig;
use crate::env::ExecRequest;
use crate::tool::ToolCtx;
use crate::tool::ToolError;
use crate::tool::ToolKind;
use crate::tool::ToolOutput;
use crate::truncate::bytes_for_tokens;
use crate::truncate::output_file_name;
use crate::typed::TypedTool;

pub struct BashTool {
    pub default_timeout: Duration,
    pub max_timeout: Duration,
}

impl Default for BashTool {
    fn default() -> Self {
        Self {
            default_timeout: Duration::from_secs(120),
            max_timeout: Duration::from_secs(600),
        }
    }
}

#[derive(Debug, Deserialize, JsonSchema)]
pub struct BashArgs {
    /// The shell command to run.
    pub command: String,
    /// Timeout in seconds. Defaults to 120; the maximum is 600.
    #[serde(default, deserialize_with = "crate::de::opt_u64")]
    pub timeout: Option<u64>,
}

#[async_trait]
impl TypedTool for BashTool {
    type Args = BashArgs;
    const NAME: &'static str = "bash";
    const DESCRIPTION: &'static str = "Run a shell command and return its exit code and output. Every command \
        starts in the working directory. stdout and stderr are merged. Commands run non-interactively (stdin is \
        empty) with a timeout; long output keeps its beginning and end, and the full output is saved to a file.";

    fn kind(&self) -> ToolKind {
        ToolKind::Mutating
    }

    fn guidelines(&self) -> &[&'static str] {
        &[
            "Use bash for builds, tests, git and other commands. Commands run non-interactively with a timeout; do not start long-running servers or interactive programs.",
            "Files from others (downloads, archives, attachments) can carry code: run nothing from inside their folder and keep your scripts out of it; pass their paths as arguments.",
        ]
    }

    fn describe(&self, args: &serde_json::Value, _env: &dyn crate::env::Environment) -> crate::tool::ToolDescription {
        let command = args.get("command").and_then(serde_json::Value::as_str).unwrap_or("bash");
        let first = command.lines().next().unwrap_or(command).trim();
        let mut title: String = first.chars().take(200).collect();
        if title.len() < first.len() || command.lines().nth(1).is_some() {
            title.push('…');
        }
        crate::tool::ToolDescription {
            category: crate::tool::ToolCategory::Execute,
            title,
            locations: Vec::new(),
        }
    }

    async fn run(&self, args: BashArgs, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        let command = args.command.trim();
        if command.is_empty() {
            return Err(ToolError::InvalidArgs("command is empty".into()));
        }
        let timeout = args
            .timeout
            .map(Duration::from_secs)
            .unwrap_or(self.default_timeout)
            .clamp(Duration::from_secs(1), self.max_timeout);
        let mut env = HashMap::new();
        env.insert("HER_CALL_ID".to_string(), ctx.call_id.clone());
        // Lets tools and scripts notice that an agent runs them.
        env.insert("AI_AGENT".to_string(), "her".to_string());
        let request = ExecRequest {
            command: command.to_string(),
            cwd: ctx.env.cwd().to_path_buf(),
            env,
            timeout,
            capture: CaptureConfig {
                head_bytes: bytes_for_tokens(ctx.limits.head_tokens),
                tail_bytes: bytes_for_tokens(ctx.limits.tail_tokens),
                spill_path: Some(ctx.output_dir.join(output_file_name(&ctx.call_id))),
                max_saved_bytes: ctx.limits.max_saved_bytes,
            },
            progress: Some(ctx.progress.clone()),
            cancel: ctx.cancel.clone(),
        };
        let out = ctx
            .env
            .exec(request)
            .await
            .map_err(|e| ToolError::Failed(format!("Failed to start the command: {e}")))?;
        if out.cancelled {
            return Err(ToolError::Cancelled);
        }
        let status = match (out.exit_code, out.signal) {
            (Some(code), _) => code.to_string(),
            (None, Some(signal)) => format!("killed by signal {signal}"),
            (None, None) => "unknown".to_string(),
        };
        let body = out.output.text();
        let body = if body.trim().is_empty() { "(no output)".to_string() } else { body };
        let mut text = String::new();
        if out.timed_out {
            text.push_str(&format!(
                "Command timed out after {}s and was terminated.\n",
                timeout.as_secs()
            ));
        }
        text.push_str(&format!(
            "Exit code: {status}\nDuration: {:.1}s\nOutput:\n{body}",
            out.duration.as_secs_f64()
        ));
        Ok(ToolOutput {
            content: vec![her_llm::UserContent::Text { text }],
            details: Some(json!({
                "exit_code": out.exit_code,
                "signal": out.signal,
                "timed_out": out.timed_out,
                "duration_ms": out.duration.as_millis() as u64,
                "truncated": out.output.truncated,
                "full_output_path": out.output.saved.as_ref().map(|s| &s.path),
            })),
            is_error: out.timed_out,
            ends_task: false,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::builtin::test_support::run;
    use crate::builtin::test_support::test_ctx;

    #[tokio::test]
    async fn reports_exit_code_and_output() {
        let dir = tempfile::tempdir().unwrap();
        let out = run(&BashTool::default(), json!({"command": "echo hi; exit 2"}), test_ctx(dir.path()))
            .await
            .unwrap();
        let text = out.content_text();
        assert!(text.starts_with("Exit code: 2\nDuration: "));
        assert!(text.ends_with("Output:\nhi\n"));
        assert!(!out.is_error, "a non-zero exit code is a normal result");
    }

    #[tokio::test]
    async fn timeouts_are_errors() {
        let dir = tempfile::tempdir().unwrap();
        let tool = BashTool {
            default_timeout: Duration::from_secs(1),
            max_timeout: Duration::from_secs(1),
        };
        let out = run(&tool, json!({"command": "sleep 5"}), test_ctx(dir.path())).await.unwrap();
        assert!(out.is_error);
        assert!(out.content_text().starts_with("Command timed out after 1s"));
    }

    #[tokio::test]
    async fn cancellation_is_reported() {
        let dir = tempfile::tempdir().unwrap();
        let ctx = test_ctx(dir.path());
        let cancel = ctx.cancel.clone();
        tokio::spawn(async move {
            tokio::time::sleep(Duration::from_millis(200)).await;
            cancel.cancel();
        });
        let err = run(&BashTool::default(), json!({"command": "sleep 20"}), ctx).await.unwrap_err();
        assert!(matches!(err, ToolError::Cancelled));
    }

    #[tokio::test]
    async fn empty_output_is_explicit() {
        let dir = tempfile::tempdir().unwrap();
        let out = run(&BashTool::default(), json!({"command": "true"}), test_ctx(dir.path()))
            .await
            .unwrap();
        assert!(out.content_text().ends_with("Output:\n(no output)"));
    }
}
