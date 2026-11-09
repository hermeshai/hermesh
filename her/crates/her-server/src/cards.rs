//! Secure cards on the server's side (docs/her-secure-cards.md §3.3, §6): the connectors Her's
//! conversation reads and checks, and `cards/answer`, which carries out an approved card through
//! the console's own handlers. A connector that signs in ends its card when the sign-in does.

use std::collections::HashMap;
use std::sync::Arc;
use std::sync::OnceLock;
use std::sync::Weak;

use async_trait::async_trait;
use her_core::cards::Cards;
use her_core::connect::ConnectorHost;
use her_core::connect::ConnectorInfo;
use her_core::mcp::views;
use her_protocol::McpServerConfig;
use her_protocol::api::error_code;
use her_protocol::card::Card;
use her_protocol::card::CardAnswer;
use her_protocol::card::CardOp;
use her_protocol::card::CardState;
use her_protocol::card::CardsAnswerParams;
use her_protocol::card::CardsAnswerResult;
use her_protocol::card::SignIn;
use her_protocol::customize::ConfigEdit;
use her_protocol::customize::ConfigLayer;
use her_protocol::customize::ConfigMerge;
use her_protocol::customize::ConfigWriteParams;
use her_protocol::customize::CredentialsDeleteParams;
use her_protocol::customize::McpLogoutParams;
use her_protocol::customize::McpProbeParams;
use her_protocol::customize::McpProbeResult;
use her_protocol::customize::McpServerParams;
use her_protocol::customize::McpStatusState;
use her_rpc::RpcError;
use parking_lot::Mutex;
use serde_json::Value;
use tokio::sync::oneshot;

use crate::errors;
use crate::hub::HubCmd;
use crate::server::Inner;
use crate::server::ResumeOverrides;
use crate::server::Server;

/// The states in which a card waits for something: the owner, her or a sign-in.
const WAITING: [CardState; 4] = [CardState::Open, CardState::Working, CardState::SignIn, CardState::Retry];

/// How many of a connector's tools the note names.
const NAMED_TOOLS: usize = 3;

/// The server's connectors as Her's conversation reaches them, and the cards that wait for a
/// connector's sign-in.
#[derive(Default)]
pub(crate) struct Connectors {
    server: OnceLock<Weak<Inner>>,
    /// By connector: the session and the card.
    signing_in: Mutex<HashMap<String, (String, String)>>,
}

impl Connectors {
    pub(crate) fn bind(&self, server: Weak<Inner>) {
        let _ = self.server.set(server);
    }

    fn server(&self) -> Result<Server, String> {
        self.server.get().and_then(Weak::upgrade).map(Server).ok_or_else(|| "the service is stopping".to_string())
    }
}

#[async_trait]
impl ConnectorHost for Connectors {
    async fn connectors(&self) -> Result<Vec<ConnectorInfo>, String> {
        let server = self.server()?;
        tokio::task::spawn_blocking(move || {
            let ws = server.workspace(None).map_err(|e| e.message)?;
            let live = server.live_mcp(&views::workspace_dir(&ws));
            Ok(ws
                .mcp_servers
                .iter()
                .map(|config| {
                    let view = views::view(&ws, &server.0.home, config, live.get(&config.name));
                    ConnectorInfo {
                        name: config.name.clone(),
                        url: config.url.clone(),
                        enabled: view.enabled,
                        oauth: config.uses_oauth(),
                        state: view.status.state,
                        tools: view.status.tools,
                    }
                })
                .collect())
        })
        .await
        .map_err(|e| e.to_string())?
    }

    async fn probe(&self, name: &str, url: &str) -> Result<McpProbeResult, String> {
        let config = McpServerConfig { name: name.to_string(), url: Some(url.to_string()), ..Default::default() };
        let params = McpProbeParams { probe_id: format!("card:{name}"), server: config, cwd: None };
        self.server()?.probe(params, Arc::new(|_| {})).await.map_err(|e| e.message)
    }
}

fn card_ended(id: &str, state: CardState) -> RpcError {
    RpcError::with_kind(error_code::CARD_ENDED, "card_ended", format!("card {id} has ended: {}", state.as_str()))
}

fn no_card(id: &str) -> RpcError {
    RpcError::invalid_params(format!("no card {id} waits in this conversation"))
}

/// The values an answer's ops fill in, by field: the card's ops with each `"<value:ID>"` given a
/// non-empty string, and nothing else changed. Compared as JSON values, so that how a device
/// spells the JSON does not matter.
fn filled(card: &[CardOp], answer: &[CardOp]) -> Option<HashMap<String, String>> {
    fn walk(card: &Value, answer: &Value, values: &mut HashMap<String, String>) -> bool {
        match (card, answer) {
            (Value::String(slot), Value::String(value)) if slot.starts_with("<value:") && slot.ends_with('>') => {
                let id = &slot["<value:".len()..slot.len() - 1];
                !value.is_empty() && values.entry(id.to_string()).or_insert_with(|| value.clone()) == value
            }
            (Value::Object(card), Value::Object(answer)) => {
                card.len() == answer.len() && card.iter().all(|(key, item)| answer.get(key).is_some_and(|other| walk(item, other, values)))
            }
            (Value::Array(card), Value::Array(answer)) => card.len() == answer.len() && card.iter().zip(answer).all(|(a, b)| walk(a, b, values)),
            (card, answer) => card == answer,
        }
    }
    let mut values = HashMap::new();
    let same = card.len() == answer.len() && card.iter().zip(answer).all(|(a, b)| a.method == b.method && walk(&a.params, &b.params, &mut values));
    same.then_some(values)
}

/// `params` with each `"<value:ID>"` replaced by its value.
fn fill(params: &Value, values: &HashMap<String, String>) -> Value {
    match params {
        Value::String(slot) => match slot.strip_prefix("<value:").and_then(|rest| rest.strip_suffix('>')).and_then(|id| values.get(id)) {
            Some(value) => Value::String(value.clone()),
            None => params.clone(),
        },
        Value::Object(map) => Value::Object(map.iter().map(|(key, item)| (key.clone(), fill(item, values))).collect()),
        Value::Array(items) => Value::Array(items.iter().map(|item| fill(item, values)).collect()),
        other => other.clone(),
    }
}

impl Server {
    fn connectors(&self) -> &Connectors {
        &self.0.connectors
    }

    /// The cards of a conversation, which is loaded if it is not.
    async fn cards_of(&self, session: &str, card: &str) -> Result<Arc<Cards>, RpcError> {
        self.load_session(session, ResumeOverrides::default(), None).await?;
        let hub = self.hub(session).ok_or_else(|| errors::session_closed(session))?;
        let (reply, answer) = oneshot::channel();
        hub.send(HubCmd::Cards { card_id: card.to_string(), reply }).await?;
        answer.await.map_err(|_| errors::session_closed(session))?.ok_or_else(|| no_card(card))
    }

    /// `cards/answer`: the owner's answer to a card. An approval carries out the card's ops as
    /// the console would send them; what a card wrote goes again when it is declined or fails.
    pub async fn cards_answer(&self, p: CardsAnswerParams) -> Result<CardsAnswerResult, RpcError> {
        let id = p.card_id.as_str();
        let cards = self.cards_of(&p.session_id, id).await?;
        cards.expire_if_due(id);
        let (card, state) = cards.get(id).ok_or_else(|| no_card(id))?;
        if state.is_ended() {
            return Err(card_ended(id, state));
        }
        let ops = match p.answer {
            CardAnswer::Decline => {
                let note = "The owner declined.".to_string();
                let left = cards.set(id, &WAITING, CardState::Declined, Some(note.clone())).map_err(|now| now.map_or_else(|| no_card(id), |now| card_ended(id, now)))?;
                if left != CardState::Open {
                    self.undo(&card).await;
                }
                return Ok(CardsAnswerResult { state: CardState::Declined, note: Some(note) });
            }
            CardAnswer::Approve { ops } => ops,
        };
        let values = filled(&card.ops, &ops).ok_or_else(|| RpcError::invalid_params("the ops are not the card's, with each value filled in"))?;
        match cards.set(id, &[CardState::Open, CardState::Retry], CardState::Working, None) {
            Ok(_) => {}
            // Already under way: the same answer again.
            Err(Some(now @ (CardState::Working | CardState::SignIn))) => return Ok(CardsAnswerResult { state: now, note: None }),
            Err(Some(now)) => return Err(card_ended(id, now)),
            Err(None) => return Err(no_card(id)),
        }
        let (to, note) = self.carry_out(&p.session_id, &card, &values).await;
        match cards.set(id, &[CardState::Working], to, note.clone()) {
            Ok(_) => Ok(CardsAnswerResult { state: to, note }),
            // It ended meanwhile, declined or expired: what this wrote goes too.
            Err(now) => {
                self.undo(&card).await;
                let now = now.ok_or_else(|| no_card(id))?;
                Ok(CardsAnswerResult { state: now, note: None })
            }
        }
    }

    /// Runs an approved card's ops through the console's handlers, then follows the connector
    /// to where the card goes next, with her's words.
    async fn carry_out(&self, session: &str, card: &Card, values: &HashMap<String, String>) -> (CardState, Option<String>) {
        let name = her_core::connect::display_name(&card.body.name);
        for op in &card.ops {
            let params = fill(&op.params, values);
            let done = match op.method.as_str() {
                "credentials/set" => match serde_json::from_value(params) {
                    Ok(p) => self.credentials_set(p).await.map(drop),
                    Err(e) => Err(RpcError::invalid_params(e.to_string())),
                },
                "config/write" => match serde_json::from_value(params) {
                    Ok(p) => self.config_write(p).await.map(drop),
                    Err(e) => Err(RpcError::invalid_params(e.to_string())),
                },
                other => Err(RpcError::internal(format!("a card does not call {other}"))),
            };
            if let Err(err) = done {
                self.undo(card).await;
                return (CardState::Failed, Some(format!("{name} could not be added: {}", err.message)));
            }
        }
        if card.body.sign_in == SignIn::Oauth {
            self.connectors().signing_in.lock().insert(card.body.name.clone(), (session.to_string(), card.id.clone()));
            return (CardState::SignIn, None);
        }
        self.connected(card).await
    }

    /// Done once the connector connects and lists its tools; another try with why it did not.
    async fn connected(&self, card: &Card) -> (CardState, Option<String>) {
        let name = her_core::connect::display_name(&card.body.name);
        match self.mcp_check(McpServerParams { name: card.body.name.clone(), cwd: None }).await {
            Ok(checked) if checked.server.status.state == McpStatusState::Ok => {
                let mut named: Vec<&str> = checked.tools.iter().take(NAMED_TOOLS).map(|tool| tool.name.as_str()).collect();
                if checked.tools.len() > NAMED_TOOLS {
                    named.push("…");
                }
                let tools = if named.is_empty() { String::new() } else { format!(" ({})", named.join(", ")) };
                (CardState::Done, Some(format!("{name} connected: {} tools{tools}.", checked.tools.len())))
            }
            Ok(checked) => (CardState::Retry, Some(format!("{name} did not connect: {}", checked.server.status.error.unwrap_or_else(|| "no answer".into())))),
            Err(err) => (CardState::Retry, Some(format!("{name} did not connect: {}", err.message))),
        }
    }

    /// Takes back what a card wrote: the connector it added and the values saved for it, and a
    /// sign-in in progress with what it saved. A connector that signs in again keeps what it had.
    async fn undo(&self, card: &Card) {
        let name = card.body.name.clone();
        {
            let mut signing_in = self.connectors().signing_in.lock();
            if signing_in.get(&name).is_some_and(|(_, id)| *id == card.id) {
                signing_in.remove(&name);
            }
        }
        if card.body.reconnect {
            let _ = self.mcp_login_cancel(McpLogoutParams { name }).await;
            return;
        }
        if card.body.sign_in == SignIn::Oauth
            && let Err(err) = self.mcp_logout(McpLogoutParams { name: name.clone() }).await
        {
            tracing::warn!(connector = %name, "cannot sign out of a connector a card added: {}", err.message);
        }
        let removed = ConfigWriteParams {
            layer: ConfigLayer::User,
            cwd: None,
            edits: vec![ConfigEdit { key_path: format!("mcp_servers.{name}"), value: Value::Null, merge: ConfigMerge::Replace }],
            expected_version: None,
        };
        if let Err(err) = self.config_write(removed).await {
            tracing::warn!(connector = %name, "cannot remove a connector a card added: {}", err.message);
        }
        for op in card.ops.iter().filter(|op| op.method == "credentials/set") {
            if let Some(credential) = op.params.get("name").and_then(Value::as_str) {
                let _ = self.credentials_delete(CredentialsDeleteParams { name: credential.to_string() }).await;
            }
        }
    }

    /// A connector's sign-in ended: the card that waited for it is done once the connector lists
    /// its tools, or waits for another try.
    pub(crate) async fn card_sign_in_ended(&self, connector: &str, outcome: Result<(), String>) {
        let Some((session, id)) = self.connectors().signing_in.lock().remove(connector) else {
            return;
        };
        let Ok(cards) = self.cards_of(&session, &id).await else {
            return;
        };
        let Some((card, CardState::SignIn)) = cards.get(&id) else {
            return;
        };
        let (to, note) = match outcome {
            Ok(()) => self.connected(&card).await,
            Err(error) => (CardState::Retry, Some(format!("The sign-in did not finish: {error}"))),
        };
        let _ = cards.set(&id, &[CardState::SignIn], to, note);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn op(method: &str, params: Value) -> CardOp {
        CardOp { method: method.into(), params }
    }

    #[test]
    fn an_answer_fills_the_card_and_changes_nothing_else() {
        let card = vec![
            op("credentials/set", json!({"name": "mcp.tracker", "value": "<value:f1>"})),
            op("config/write", json!({"layer": "user", "edits": [{"key_path": "mcp_servers.tracker", "value": {"url": "http://127.0.0.1:9/mcp"}, "merge": "replace"}]})),
        ];
        let answer = |token: &str, url: &str| {
            vec![
                op("credentials/set", json!({"value": token, "name": "mcp.tracker"})),
                op("config/write", json!({"edits": [{"merge": "replace", "value": {"url": url}, "key_path": "mcp_servers.tracker"}], "layer": "user"})),
            ]
        };
        let values = filled(&card, &answer("t-123", "http://127.0.0.1:9/mcp")).expect("the same ops, keys in another order");
        assert_eq!(values.get("f1").map(String::as_str), Some("t-123"));
        assert_eq!(fill(&card[0].params, &values), json!({"name": "mcp.tracker", "value": "t-123"}));
        assert!(filled(&card, &answer("", "http://127.0.0.1:9/mcp")).is_none(), "an empty value");
        assert!(filled(&card, &answer("t-123", "http://evil.example/mcp")).is_none(), "another address");
        assert!(filled(&card, &answer("t-123", "http://127.0.0.1:9/mcp")[..1]).is_none(), "an op left out");
        let mut more = answer("t-123", "http://127.0.0.1:9/mcp");
        more.push(op("config/write", json!({"layer": "user", "edits": []})));
        assert!(filled(&card, &more).is_none(), "an op more");
        let mut renamed = answer("t-123", "http://127.0.0.1:9/mcp");
        renamed[0].params["name"] = json!("mcp.other");
        assert!(filled(&card, &renamed).is_none(), "a value where the card has none");
    }
}
