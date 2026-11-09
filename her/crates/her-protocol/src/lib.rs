//! Kernel protocol: operations go in, events come out.
//!
//! JSON uses a `type` tag and snake_case fields. Within a protocol version changes are
//! additive; clients must ignore unknown event types and fields.

pub mod api;
pub mod bot;
pub mod card;
pub mod customize;
pub mod deck;
pub mod doc;
pub mod docx;
pub mod files;
pub mod pptx;
mod event;
pub mod mcp;
mod op;
pub mod schema;
pub mod web_search;

pub use event::AgentRef;
pub use event::AgentStatus;
pub use event::CompactionReason;
pub use event::ContextPart;
pub use event::ContextPartKind;
pub use event::ContextReport;
pub use event::ContextUsage;
pub use event::CompletionReason;
pub use event::Delta;
pub use event::ErrorInfo;
pub use event::Event;
pub use event::EventMsg;
pub use event::Location;
pub use event::MailInfo;
pub use event::MailKind;
pub use event::QueuedInput;
pub use event::Role;
pub use event::SkillUse;
pub use event::TaskTrigger;
pub use event::ToolCategory;
pub use event::WarningCode;
pub use mcp::McpApproval;
pub use mcp::McpAuthMode;
pub use mcp::McpExposure;
pub use mcp::McpOAuthClient;
pub use mcp::McpRemoteTransport;
pub use mcp::McpServerConfig;
pub use mcp::McpServerInfo;
pub use mcp::McpServerState;
pub use op::ApprovalDecision;
pub use op::PermissionMode;
pub use op::CommandRule;
pub use op::GrantAuthority;
pub use op::SiteGrant;
pub use op::Op;
pub use op::PromptMode;
pub use op::RuleDecision;

pub const PROTOCOL_VERSION: u32 = 2;
