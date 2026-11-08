//! The sub-agents a main session started before it was loaded, as the logs left them
//! (subagent-design §9.4). The main session's log says which agents it started; each agent's
//! own log says how its last task ended, and is what clients show of its work. Their sessions
//! are not loaded: they take no messages.

use her_llm::Timestamp;
use her_llm::UsageTotals;
use her_protocol::AgentStatus;
use her_protocol::ErrorInfo;
use her_protocol::api::TranscriptItem;
use std::path::PathBuf;

use crate::history::History;
use crate::kernel;
use crate::store::Entry;
use crate::store::EntryKind;
use crate::store::SessionRef;
use crate::store::SessionStore;
use crate::store::TaskStatus;

/// A sub-agent from an earlier run of its main session.
#[derive(Clone, Debug)]
pub struct RestoredAgent {
    pub agent_id: String,
    pub name: String,
    pub description: String,
    pub call_id: String,
    pub session_file: PathBuf,
    /// How its last task ended. A task the process stopped in was interrupted. `Closed` when its
    /// log cannot be read and no report came from it.
    pub status: AgentStatus,
    /// When it got that status.
    pub since: Timestamp,
    /// Why its last task failed.
    pub error: Option<ErrorInfo>,
    /// Its own session, as its log has it; empty when the log cannot be read.
    pub transcript: Vec<TranscriptItem>,
    pub usage: UsageTotals,
}

/// The agents started on a main session's branch, in the order they started.
pub(crate) fn restore(branch: &[&Entry], store: &dyn SessionStore) -> Vec<RestoredAgent> {
    branch
        .iter()
        .filter_map(|entry| match &entry.kind {
            EntryKind::AgentSpawned(spawned) => Some((entry.ts, spawned)),
            _ => None,
        })
        .map(|(ts, spawned)| {
            let mut agent = RestoredAgent {
                agent_id: spawned.agent_id.clone(),
                name: spawned.name.clone(),
                description: spawned.description.clone(),
                call_id: spawned.call_id.clone(),
                session_file: spawned.session_file.clone(),
                status: AgentStatus::Closed,
                since: ts,
                error: None,
                transcript: Vec::new(),
                usage: UsageTotals::default(),
            };
            match store.read(&SessionRef::Path(spawned.session_file.clone())) {
                Ok((_, entries)) => {
                    let history = History::from_entries(entries);
                    let own = history.branch();
                    // An agent always has a task; without one, the process stopped as it began.
                    agent.status = AgentStatus::Interrupted;
                    if let Some((at, status, error)) = own.iter().rev().find_map(|e| match &e.kind {
                        EntryKind::Task { status, error, .. } => Some((e.ts, *status, error)),
                        _ => None,
                    }) {
                        agent.since = at;
                        agent.status = match status {
                            TaskStatus::Completed => AgentStatus::Completed,
                            TaskStatus::Failed => AgentStatus::Failed,
                            TaskStatus::Started | TaskStatus::Interrupted => AgentStatus::Interrupted,
                        };
                        agent.error = error.as_deref().cloned();
                    }
                    agent.usage = kernel::usage_of(own.iter().copied());
                    agent.transcript = kernel::transcript_of(own);
                }
                // Its last report says how it ended.
                Err(err) => {
                    tracing::warn!(agent = %spawned.agent_id, "cannot read a sub-agent's log: {err}");
                    if let Some((at, status)) = branch.iter().rev().find_map(|e| match &e.kind {
                        EntryKind::Mail { mail, .. } if mail.from.id == spawned.agent_id => Some((e.ts, mail.status)),
                        _ => None,
                    }) {
                        agent.since = at;
                        agent.status = status;
                    }
                }
            }
            agent
        })
        .collect()
}
