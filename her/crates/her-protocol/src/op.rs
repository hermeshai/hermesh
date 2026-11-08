use her_llm::ThinkingLevel;
use her_llm::UserContent;
use schemars::JsonSchema;
use serde::Deserialize;
use serde::Serialize;
use std::fmt;
use std::str::FromStr;

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum Op {
    /// User input. Starts a task when the session is idle; queued by `mode` otherwise.
    Prompt {
        input: Vec<UserContent>,
        #[serde(default)]
        mode: PromptMode,
        /// Whether a leading `/name` is a command: a built-in one such as `/compact`, or a
        /// skill, whose instructions replace it. Front ends leave it out; messages between
        /// agents set false.
        #[serde(default = "yes", skip_serializing_if = "is_yes")]
        commands: bool,
    },
    /// Starts again from a message the user sent earlier: the running task stops, the
    /// conversation goes back to just before that message, and `input` takes its place. Nothing
    /// changes until this op: `session_rewound` says it happened, a `warning` with the code
    /// `resend_rejected` that it did not (docs/kernel/message-resend-design.md).
    Resend {
        /// The `message_id` of a user message on the current branch.
        message_id: String,
        input: Vec<UserContent>,
        /// As for `Prompt`.
        #[serde(default = "yes", skip_serializing_if = "is_yes")]
        commands: bool,
    },
    /// Continue from the current history (retry after a failure).
    Continue,
    Interrupt,
    /// Takes input that waits for the running task out of the queue: `input_withdrawn` says it
    /// went. Nothing happens once it has joined the history.
    Withdraw {
        id: String,
    },
    /// Moves waiting input to the other mode, at the end of it.
    Requeue {
        id: String,
        mode: PromptMode,
    },
    /// Ends the running step, the model's reply or its tool calls, so that waiting steering input
    /// joins the history now; the task goes on. Nothing happens when no steering input waits or
    /// an approval does.
    SteerNow,
    /// Stops one sub-agent's current task. It keeps its session and takes messages again; its
    /// report arrives as an interrupted one and does not wake the session.
    AgentInterrupt {
        /// The agent's id or name.
        agent: String,
    },
    Approval {
        call_id: String,
        decision: ApprovalDecision,
    },
    /// Answers what a sub-agent asks: its `approval_requested`, carried by an `agent_event`.
    AgentApproval {
        /// The agent's id.
        agent: String,
        call_id: String,
        decision: ApprovalDecision,
    },
    SetModel {
        model: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        thinking: Option<ThinkingLevel>,
    },
    #[serde(alias = "set_approval_mode")]
    SetPermissionMode {
        mode: PermissionMode,
    },
    /// Names the session; recorded in its log, at any time.
    SetTitle {
        title: String,
    },
    /// Archives the session or brings it back; recorded in its log, at any time.
    SetArchived {
        archived: bool,
    },
    Compact,
    Shutdown,
}

impl Op {
    pub fn prompt(text: impl Into<String>) -> Self {
        Op::Prompt {
            input: vec![UserContent::Text { text: text.into() }],
            mode: PromptMode::Steer,
            commands: true,
        }
    }
}

fn yes() -> bool {
    true
}

fn is_yes(value: &bool) -> bool {
    *value
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum PromptMode {
    /// Injected at the next turn boundary of the running task.
    #[default]
    Steer,
    /// Processed when the running task would otherwise finish.
    FollowUp,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ApprovalDecision {
    Approve,
    /// Approves this call and what the request proposed for the rest of the session.
    ApproveForSession {
        /// Which of `proposed_grants` to keep, by index; the first when left out.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        grant: Option<usize>,
        /// Keep that grant beyond the session too: written to the user's configuration under
        /// `[browser.sites]` (browser-design D-197). Not for "any site".
        #[serde(default, skip_serializing_if = "std::ops::Not::not")]
        persist: bool,
    },
    Deny {
        #[serde(default, skip_serializing_if = "Option::is_none")]
        feedback: Option<String>,
    },
    /// Deny and interrupt the whole task.
    Abort,
}

/// How much the session may do without asking. Deny and ask rules apply in every mode.
/// Deserializing also accepts the names used before protocol version 2.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, JsonSchema)]
#[serde(rename_all = "kebab-case")]
pub enum PermissionMode {
    /// Every file change and every command that is not known to be read-only asks first.
    #[default]
    Manual,
    /// File changes and simple file-system commands inside the workspace run without asking.
    AcceptEdits,
    /// Everything runs without asking, protected paths included. Only deny rules, ask rules
    /// and deleting a critical path still stop an action.
    BypassPermissions,
}

impl PermissionMode {
    pub const ALL: [PermissionMode; 3] = [
        PermissionMode::Manual,
        PermissionMode::AcceptEdits,
        PermissionMode::BypassPermissions,
    ];

    pub fn as_str(self) -> &'static str {
        match self {
            PermissionMode::Manual => "manual",
            PermissionMode::AcceptEdits => "accept-edits",
            PermissionMode::BypassPermissions => "bypass-permissions",
        }
    }

    /// The mode a name from before the rename stands for. `read-only` has no counterpart and
    /// becomes `manual`.
    pub fn from_legacy(name: &str) -> Option<PermissionMode> {
        match name {
            "ask" | "read-only" => Some(PermissionMode::Manual),
            "auto-edit" => Some(PermissionMode::AcceptEdits),
            "full-auto" => Some(PermissionMode::BypassPermissions),
            _ => None,
        }
    }

    /// Parses a mode name and says which current name a legacy one maps to, so that callers
    /// can tell the user.
    pub fn parse_with_notice(name: &str) -> Result<(PermissionMode, Option<String>), String> {
        if let Some(mode) = PermissionMode::ALL.into_iter().find(|m| m.as_str() == name) {
            return Ok((mode, None));
        }
        if let Some(mode) = PermissionMode::from_legacy(name) {
            return Ok((mode, Some(format!("permission mode '{name}' is now called '{mode}'"))));
        }
        if matches!(name, "plan" | "auto") {
            return Err(format!("permission mode '{name}' is not available yet"));
        }
        Err(format!(
            "invalid permission mode '{name}' (expected manual, accept-edits or bypass-permissions)"
        ))
    }
}

impl fmt::Display for PermissionMode {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl FromStr for PermissionMode {
    type Err = String;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        PermissionMode::parse_with_notice(s).map(|(mode, _)| mode)
    }
}

impl<'de> Deserialize<'de> for PermissionMode {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        String::deserialize(deserializer)?.parse().map_err(serde::de::Error::custom)
    }
}

/// Command prefix rule for the bash tool.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct CommandRule {
    pub prefix: Vec<String>,
    pub decision: RuleDecision,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
}

/// Permission for the browser tools on one origin, for the rest of the session
/// (docs/kernel/browser-design.md §7.3).
#[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, JsonSchema)]
pub struct SiteGrant {
    pub authority: GrantAuthority,
    /// `https://example.com`, or `*` for every site (navigation only).
    pub origin: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum GrantAuthority {
    /// Open pages of the origin.
    Navigate,
    /// Open pages of the origin and act on them: click, type, select.
    Act,
    /// Run scripts in pages of the origin (`browser_eval`). Only this: it neither opens nor
    /// acts, and opening or acting never runs scripts. Kept for the session only, never in
    /// the configuration (browser-design §7.6, D-175 revised).
    Script,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum RuleDecision {
    Allow,
    Ask,
    Deny,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ops_round_trip_as_tagged_json() {
        let op = Op::prompt("hi");
        let json = serde_json::to_string(&op).unwrap();
        assert_eq!(json, r#"{"type":"prompt","input":[{"type":"text","text":"hi"}],"mode":"steer"}"#);
        let parsed: Op = serde_json::from_str(r#"{"type":"prompt","input":[{"type":"text","text":"x"}]}"#).unwrap();
        assert!(matches!(parsed, Op::Prompt { mode: PromptMode::Steer, commands: true, .. }));
        let plain: Op =
            serde_json::from_str(r#"{"type":"prompt","input":[{"type":"text","text":"/x"}],"commands":false}"#).unwrap();
        assert!(matches!(plain, Op::Prompt { commands: false, .. }));
        assert!(serde_json::to_string(&plain).unwrap().ends_with(r#""commands":false}"#));
        let approval: Op = serde_json::from_str(
            r#"{"type":"approval","call_id":"c1","decision":{"type":"deny","feedback":"no"}}"#,
        )
        .unwrap();
        assert_eq!(
            approval,
            Op::Approval {
                call_id: "c1".into(),
                decision: ApprovalDecision::Deny {
                    feedback: Some("no".into())
                }
            }
        );
        let mode = Op::SetPermissionMode { mode: PermissionMode::AcceptEdits };
        assert_eq!(
            serde_json::to_string(&mode).unwrap(),
            r#"{"type":"set_permission_mode","mode":"accept-edits"}"#
        );
        // Clients written against protocol version 1 keep working.
        let legacy: Op = serde_json::from_str(r#"{"type":"set_approval_mode","mode":"full-auto"}"#).unwrap();
        assert_eq!(legacy, Op::SetPermissionMode { mode: PermissionMode::BypassPermissions });
        assert_eq!(serde_json::to_string(&Op::Interrupt).unwrap(), r#"{"type":"interrupt"}"#);
    }

    #[test]
    fn approve_for_session_takes_an_optional_grant() {
        // Clients that predate site grants send no index.
        let plain: ApprovalDecision = serde_json::from_str(r#"{"type":"approve_for_session"}"#).unwrap();
        assert_eq!(plain, ApprovalDecision::ApproveForSession { grant: None, persist: false });
        assert_eq!(serde_json::to_string(&plain).unwrap(), r#"{"type":"approve_for_session"}"#);
        let second: ApprovalDecision = serde_json::from_str(r#"{"type":"approve_for_session","grant":1}"#).unwrap();
        assert_eq!(second, ApprovalDecision::ApproveForSession { grant: Some(1), persist: false });
        let kept: ApprovalDecision = serde_json::from_str(r#"{"type":"approve_for_session","grant":0,"persist":true}"#).unwrap();
        assert_eq!(kept, ApprovalDecision::ApproveForSession { grant: Some(0), persist: true });
        let grant = SiteGrant {
            authority: GrantAuthority::Act,
            origin: "https://example.com".into(),
        };
        assert_eq!(
            serde_json::to_string(&grant).unwrap(),
            r#"{"authority":"act","origin":"https://example.com"}"#
        );
    }

    #[test]
    fn permission_mode_names() {
        for mode in PermissionMode::ALL {
            assert_eq!(mode.as_str().parse::<PermissionMode>(), Ok(mode));
            assert_eq!(serde_json::to_string(&mode).unwrap(), format!("\"{mode}\""));
        }
        for (legacy, mode) in [
            ("ask", PermissionMode::Manual),
            ("read-only", PermissionMode::Manual),
            ("auto-edit", PermissionMode::AcceptEdits),
            ("full-auto", PermissionMode::BypassPermissions),
        ] {
            let (parsed, notice) = PermissionMode::parse_with_notice(legacy).unwrap();
            assert_eq!(parsed, mode);
            assert!(notice.unwrap().contains(mode.as_str()));
            assert_eq!(serde_json::from_str::<PermissionMode>(&format!("\"{legacy}\"")).unwrap(), mode);
        }
        assert!("plan".parse::<PermissionMode>().unwrap_err().contains("not available yet"));
        assert!("auto".parse::<PermissionMode>().unwrap_err().contains("not available yet"));
        assert!("yolo".parse::<PermissionMode>().unwrap_err().contains("expected manual"));
    }
}
