//! The `mesh` group: tools that a host outside the kernel gives a bot's own conversation and runs
//! for it. The kernel has no words of its own for them: the group's line in the catalog, its
//! guidelines and its tools' definitions are the host's, and so is what the user reads when a
//! call that writes asks for approval. The service's side is behind [`MeshHost`].

use async_trait::async_trait;
use her_llm::ToolSpec;
use her_protocol::api::MeshCallResult;
use her_protocol::api::MeshDescribeResult;
use her_protocol::api::MeshHostParams;
use her_tools::ApprovalWords;
use her_tools::Tool;
use her_tools::ToolCtx;
use her_tools::ToolError;
use her_tools::ToolKind;
use her_tools::ToolOutput;
use serde_json::Value;
use std::sync::Arc;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;

use crate::deferred::Group;

pub(crate) const GROUP: &str = "mesh";

/// The service's side of the `mesh` group, for one bot's conversation. Errors are one sentence
/// for the model.
#[async_trait]
pub trait MeshHost: Send + Sync + 'static {
    /// The group as its host gives it now, none while no host offers tools; with a number that
    /// changes whenever the group does.
    fn group(&self) -> (u64, Option<Arc<MeshHostParams>>);
    /// What a call that writes would do, as `mesh/describe` answers.
    async fn describe(&self, name: &str, arguments: &Value) -> Result<MeshDescribeResult, String>;
    /// Runs one tool, as `mesh/call` does.
    async fn call(&self, call_id: &str, name: &str, arguments: Value) -> Result<MeshCallResult, String>;
}

/// How a bot's conversation reaches the mesh host, and which of its groups the session has.
pub(crate) struct MeshLink {
    host: Arc<dyn MeshHost>,
    /// The number of the group that `group` gave last.
    applied: AtomicU64,
}

impl MeshLink {
    pub(crate) fn new(host: Arc<dyn MeshHost>) -> Self {
        Self { host, applied: AtomicU64::new(u64::MAX) }
    }

    /// Whether the host's group is no longer the one `group` gave last.
    pub(crate) fn changed(&self) -> bool {
        self.host.group().0 != self.applied.load(Ordering::Acquire)
    }

    /// The host's group now, as a session's deferred tools.
    pub(crate) fn group(&self) -> Option<Group> {
        let (number, group) = self.host.group();
        self.applied.store(number, Ordering::Release);
        let group = group?;
        let tools = group
            .tools
            .iter()
            .map(|tool| {
                Arc::new(MeshTool {
                    spec: ToolSpec { name: tool.name.clone(), description: tool.description.clone(), parameters: tool.parameters.clone() },
                    writes: tool.writes,
                    host: self.host.clone(),
                }) as Arc<dyn Tool>
            })
            .collect();
        Some(Group { name: GROUP, summary: group.summary.clone(), guidelines: group.guidelines.clone(), tools })
    }
}

/// One of the host's tools. One that writes is a change the approval policy asks about, in the
/// host's words; one that only reads runs in every mode.
struct MeshTool {
    spec: ToolSpec,
    writes: bool,
    host: Arc<dyn MeshHost>,
}

#[async_trait]
impl Tool for MeshTool {
    fn spec(&self) -> &ToolSpec {
        &self.spec
    }

    fn kind(&self) -> ToolKind {
        if self.writes { ToolKind::Mutating } else { ToolKind::ReadOnly }
    }

    async fn approval_words(&self, args: &Value) -> Option<ApprovalWords> {
        let described = self.host.describe(&self.spec.name, args).await.ok()?;
        // A rule without words would never match.
        Some(ApprovalWords { summary: described.summary, rule: described.rule.filter(|rule| !rule.is_empty()) })
    }

    async fn execute(&self, args: Value, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        let result = self.host.call(&ctx.call_id, &self.spec.name, args).await.map_err(ToolError::Failed)?;
        let output = if result.is_error { ToolOutput::error(result.output) } else { ToolOutput::text(result.output) };
        Ok(match result.details {
            Some(details) => output.with_details(details),
            None => output,
        })
    }
}
