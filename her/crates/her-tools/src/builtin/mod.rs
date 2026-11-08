//! Built-in tools: read, write, edit and bash.

pub mod bash;
pub mod edit;
pub mod read;
pub mod write;

use std::sync::Arc;

pub use bash::BashTool;
pub use edit::EditTool;
pub use read::ReadTool;
pub use write::WriteTool;

use crate::registry::ToolRegistry;
use crate::tool::Tool;
use crate::typed::Typed;

pub const DEFAULT_TOOLS: [&str; 4] = ["read", "write", "edit", "bash"];

pub fn builtin_tools(bash: BashTool) -> Vec<Arc<dyn Tool>> {
    vec![
        Arc::new(Typed::new(ReadTool)),
        Arc::new(Typed::new(WriteTool)),
        Arc::new(Typed::new(EditTool)),
        Arc::new(Typed::new(bash)),
    ]
}

pub fn builtin_registry(bash: BashTool) -> ToolRegistry {
    let mut registry = ToolRegistry::new();
    for tool in builtin_tools(bash) {
        // Built-in names are valid and unique.
        let _ = registry.register(tool);
    }
    registry
}

#[cfg(test)]
pub(crate) mod test_support {
    use serde_json::Value;
    use std::path::Path;
    use std::sync::Arc;
    use tokio_util::sync::CancellationToken;

    use crate::files::FileTracker;
    use crate::local_env::LocalEnvironment;
    use crate::tool::ProgressSink;
    use crate::tool::Tool;
    use crate::tool::ToolCtx;
    use crate::tool::ToolError;
    use crate::tool::ToolOutput;
    use crate::truncate::OutputLimits;
    use crate::typed::Typed;
    use crate::typed::TypedTool;

    pub fn test_ctx(dir: &Path) -> ToolCtx {
        ToolCtx {
            call_id: "call_test".into(),
            env: Arc::new(LocalEnvironment::new(dir.to_path_buf())),
            files: Arc::new(FileTracker::new()),
            locks: Arc::new(crate::files::PathLocks::new()),
            limits: OutputLimits::default(),
            progress: ProgressSink::noop(),
            cancel: CancellationToken::new(),
            model_accepts_images: false,
            output_dir: dir.join(".her-output"),
            session_state: Default::default(),
            authorities: Default::default(),
        }
    }

    pub async fn run<T>(tool: &T, args: Value, ctx: ToolCtx) -> Result<ToolOutput, ToolError>
    where
        T: TypedTool + Clone,
    {
        Typed::new(tool.clone()).execute(args, ctx).await
    }

    impl ToolOutput {
        pub fn content_text(&self) -> String {
            her_llm::ToolResultMessage {
                call_id: String::new(),
                tool_name: String::new(),
                content: self.content.clone(),
                is_error: self.is_error,
                details: None,
                ts: 0,
                duration_ms: None,
            }
            .text_content()
        }
    }

    impl Clone for super::ReadTool {
        fn clone(&self) -> Self {
            super::ReadTool
        }
    }
    impl Clone for super::WriteTool {
        fn clone(&self) -> Self {
            super::WriteTool
        }
    }
    impl Clone for super::EditTool {
        fn clone(&self) -> Self {
            super::EditTool
        }
    }
    impl Clone for super::BashTool {
        fn clone(&self) -> Self {
            super::BashTool {
                default_timeout: self.default_timeout,
                max_timeout: self.max_timeout,
            }
        }
    }
}
