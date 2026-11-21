//! Secure cards on the server's side (docs/her-secure-cards.md §3.3, §6): the connectors Her's
//! conversation reads and checks, and `cards/answer`, which carries out an approved card through
//! the console's own handlers. A connector that signs in ends its card when the sign-in does.

use std::collections::HashMap;
use std::sync::Arc;
use std::sync::OnceLock;
use std::sync::Weak;

use async_trait::async_trait;
use her_browser::BrowserError;
use her_core::Workspace;
use her_core::cards::Cards;
use her_core::cards::SiteLoginBinding;
use her_core::cards::SiteLoginFillBinding;
use her_core::connect::ConnectorHost;
use her_core::connect::ConnectorInfo;
use her_core::mcp::views;
use her_protocol::McpServerConfig;
use her_protocol::api::error_code;
use her_protocol::card::Card;
use her_protocol::card::CardAnswer;
use her_protocol::card::CardKind;
use her_protocol::card::CardOp;
use her_protocol::card::CardState;
use her_protocol::card::CardsAnswerParams;
use her_protocol::card::CardsAnswerResult;
use her_protocol::card::SignIn;
use her_protocol::card::SiteLoginMode;
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
use her_protocol::mcp::McpTransportKind;
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
                        credential_header: credential_header(config),
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

fn credential_header(config: &McpServerConfig) -> Option<(String, String)> {
    if config.transport() != Ok(McpTransportKind::Http) {
        return None;
    }
    config.headers.iter().find_map(|(header, value)| {
        her_core::config_edit::credential_refs(value)
            .into_iter()
            .find(|name| name.starts_with("mcp."))
            .map(|name| (name, header.clone()))
    })
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

    async fn site_login_browser_of(
        &self,
        session: &str,
        own: bool,
        url: &str,
    ) -> Result<Arc<her_browser::BrowserSession>, &'static str> {
        let Some(hub) = self.hub(session) else {
            return Err("The conversation is closed.");
        };
        let (reply, answer) = oneshot::channel();
        if hub
            .send(HubCmd::SiteLoginBrowser { own, url: url.to_string(), reply })
            .await
            .is_err()
        {
            return Err("The conversation is closed.");
        }
        answer.await.map_err(|_| "The conversation is closed.")?
    }

    async fn record_site_login_fill(&self, session: &str, own: bool, target_id: &str, origin: &str, url: &str) {
        if let Some(hub) = self.hub(session) {
            let (reply, ack) = oneshot::channel();
            if hub
                .send(HubCmd::RecordSiteLoginFill {
                    own,
                    target_id: target_id.to_string(),
                    origin: origin.to_string(),
                    url: url.to_string(),
                    reply,
                })
                .await
                .is_ok()
            {
                let _ = ack.await;
            }
        }
    }

    async fn clear_site_login_fill(&self, session: &str, own: bool, target_id: &str) {
        if let Some(hub) = self.hub(session) {
            let (reply, ack) = oneshot::channel();
            if hub
                .send(HubCmd::ClearSiteLoginFill {
                    own,
                    target_id: target_id.to_string(),
                    reply,
                })
                .await
                .is_ok()
            {
                let _ = ack.await;
            }
        }
    }

    /// `cards/answer`: the owner's answer to a card. An approval carries out the card's ops as
    /// the console would send them; a `site_login` fill types once into the bound isolated page
    /// without submitting or saving; what a card wrote goes again when it is declined or fails.
    pub async fn cards_answer(&self, p: CardsAnswerParams) -> Result<CardsAnswerResult, RpcError> {
        let mut answer = ZeroizingAnswer(p.answer);
        let id = p.card_id.as_str();
        let cards = self.cards_of(&p.session_id, id).await?;
        cards.expire_if_due(id);
        let (card, state) = cards.get(id).ok_or_else(|| no_card(id))?;
        if state.is_ended() {
            return Err(card_ended(id, state));
        }
        match &mut answer.0 {
            CardAnswer::Decline => {
                let takeover = card.site_login_body().is_some_and(|b| b.mode == SiteLoginMode::ChromeTakeover);
                let note = if takeover {
                    "The owner cancelled Chrome sign-in. Fall back to manual login.".to_string()
                } else {
                    "The owner declined.".to_string()
                };
                let from: &[CardState] = if takeover {
                    &[CardState::Open, CardState::SignIn]
                } else if card.kind == CardKind::SiteLogin {
                    &[CardState::Open]
                } else {
                    &WAITING
                };
                let left = cards.set(id, from, CardState::Declined, Some(note.clone())).map_err(|now| now.map_or_else(|| no_card(id), |now| card_ended(id, now)))?;
                if card.kind == CardKind::Connect && left != CardState::Open {
                    self.undo(&card).await;
                }
                Ok(CardsAnswerResult { state: CardState::Declined, note: Some(note) })
            }
            CardAnswer::Fill { fields } => {
                self.answer_site_login(&p.session_id, &cards, &card, fields).await
            }
            CardAnswer::TakeoverStart => {
                self.answer_chrome_takeover_start(&p.session_id, &cards, &card, state)
            }
            CardAnswer::TakeoverDone => {
                self.answer_chrome_takeover_done(&p.session_id, &cards, &card, state)
            }
            CardAnswer::Approve { ops } => {
                if card.kind != CardKind::Connect || card.connect_body().is_none() {
                    return Err(RpcError::invalid_params("only a connect card can be approved with ops"));
                }
                let mut values = filled(&card.ops, ops).ok_or_else(|| RpcError::invalid_params("the ops are not the card's, with each value filled in"))?;
                match cards.set(id, &[CardState::Open, CardState::Retry], CardState::Working, None) {
                    Ok(_) => {}
                    // Already under way: the same answer again.
                    Err(Some(now @ (CardState::Working | CardState::SignIn))) => {
                        for v in values.values_mut() {
                            zeroize_string(v);
                        }
                        return Ok(CardsAnswerResult { state: now, note: None });
                    }
                    Err(Some(now)) => {
                        for v in values.values_mut() {
                            zeroize_string(v);
                        }
                        return Err(card_ended(id, now));
                    }
                    Err(None) => {
                        for v in values.values_mut() {
                            zeroize_string(v);
                        }
                        return Err(no_card(id));
                    }
                }
                let mut prior_credentials = Vec::new();
                let (to, note) = self.carry_out(&p.session_id, &card, &values, &mut prior_credentials).await;
                for v in values.values_mut() {
                    zeroize_string(v);
                }
                match cards.set(id, &[CardState::Working], to, note.clone()) {
                    Ok(_) => {
                        for (_, old) in &mut prior_credentials {
                            if let Some(old) = old {
                                zeroize_string(old);
                            }
                        }
                        Ok(CardsAnswerResult { state: to, note })
                    }
                    // It ended meanwhile, declined or expired: what this wrote goes too.
                    Err(now) => {
                        self.undo(&card).await;
                        self.restore_credentials(&mut prior_credentials).await;
                        if to == CardState::Done
                            && let Some(body) = card.connect_body().filter(|body| body.reconnect && !body.fields.is_empty())
                        {
                            self.add_to_conversation(&p.session_id, &body.name, Some(&body.url)).await;
                        }
                        let now = now.ok_or_else(|| no_card(id))?;
                        Ok(CardsAnswerResult { state: now, note: None })
                    }
                }
            }
        }
    }

    async fn answer_site_login(
        &self,
        session_id: &str,
        cards: &Arc<Cards>,
        card: &Card,
        fields: &mut HashMap<String, String>,
    ) -> Result<CardsAnswerResult, RpcError> {
        let id = card.id.as_str();
        let Some(body) = card.site_login_body().filter(|b| b.mode == SiteLoginMode::Fill) else {
            return Err(RpcError::invalid_params("only a site_login fill card can be answered with fill"));
        };
        let Some(SiteLoginBinding::Fill(binding)) = cards.binding(id) else {
            let note = "The page changed before the fields could be filled.".to_string();
            let _ = cards.set(id, &[CardState::Open], CardState::Failed, Some(note.clone()));
            return Ok(CardsAnswerResult { state: CardState::Failed, note: Some(note) });
        };
        if fields.len() != binding.fields.len()
            || !binding.fields.iter().all(|f| fields.get(&f.id).is_some_and(|v| !v.is_empty()))
        {
            return Err(RpcError::invalid_params("the fields do not match the card's sign-in fields"));
        }
        match cards.set(id, &[CardState::Open], CardState::Working, None) {
            Ok(_) => {}
            Err(Some(now @ CardState::Working)) => return Ok(CardsAnswerResult { state: now, note: None }),
            Err(Some(now)) => return Err(card_ended(id, now)),
            Err(None) => return Err(no_card(id)),
        }
        let (to, note) = self.carry_out_site_login(session_id, id, &body.origin, &binding, fields).await;
        match cards.set(id, &[CardState::Working], to, note.clone()) {
            Ok(_) => Ok(CardsAnswerResult { state: to, note }),
            Err(now) => {
                let now = now.ok_or_else(|| no_card(id))?;
                Ok(CardsAnswerResult { state: now, note: None })
            }
        }
    }

    fn fail_chrome_takeover(
        cards: &Arc<Cards>,
        id: &str,
        expected: &[CardState],
        note: String,
    ) -> Result<CardsAnswerResult, RpcError> {
        match cards.set(id, expected, CardState::Failed, Some(note.clone())) {
            Ok(_) => Ok(CardsAnswerResult { state: CardState::Failed, note: Some(note) }),
            Err(Some(now)) => Err(card_ended(id, now)),
            Err(None) => Err(no_card(id)),
        }
    }

    fn answer_chrome_takeover_start(
        &self,
        session_id: &str,
        cards: &Arc<Cards>,
        card: &Card,
        state: CardState,
    ) -> Result<CardsAnswerResult, RpcError> {
        let id = card.id.as_str();
        let Some(body) = card.site_login_body().filter(|b| b.mode == SiteLoginMode::ChromeTakeover) else {
            return Err(RpcError::invalid_params("only a Chrome takeover site_login card can be answered with takeover_start"));
        };
        let Some(SiteLoginBinding::ChromeTakeover(binding)) = cards.binding(id) else {
            return Self::fail_chrome_takeover(
                cards,
                id,
                &[CardState::Open, CardState::SignIn],
                "Chrome sign-in target binding is no longer available. Fall back to manual login.".to_string(),
            );
        };
        let (conn, tab) = match self.chrome_takeover_tab(binding.extension_host, session_id, &binding.target_id) {
            Ok(found) => found,
            Err(reason) => {
                return Self::fail_chrome_takeover(cards, id, &[CardState::Open, CardState::SignIn], reason.to_string());
            }
        };
        if tab.attached {
            return Self::fail_chrome_takeover(
                cards,
                id,
                &[CardState::Open, CardState::SignIn],
                "Chrome isolation could not be verified (debugger attached). Sign-in card invalidated; fall back to manual login.".to_string(),
            );
        }
        let valid_target = match state {
            CardState::Open => {
                tab.url == binding.url
                    && her_browser::session::url_origin(&tab.url) == binding.origin
                    && her_browser::session::is_allowed_login_url(&tab.url)
            }
            CardState::SignIn => her_browser::session::is_allowed_login_url(&tab.url),
            _ => false,
        };
        if !valid_target {
            return Self::fail_chrome_takeover(
                cards,
                id,
                &[CardState::Open, CardState::SignIn],
                "The Chrome page or target changed before sign-in. Sign-in card invalidated; fall back to manual login.".to_string(),
            );
        }
        if state == CardState::Open {
            match cards.set(id, &[CardState::Open], CardState::SignIn, None) {
                Ok(_) => {}
                Err(Some(now @ CardState::SignIn)) => {
                    self.reveal_bound_tab(&conn, session_id, &binding.target_id);
                    return Ok(CardsAnswerResult { state: now, note: None });
                }
                Err(Some(now)) => return Err(card_ended(id, now)),
                Err(None) => return Err(no_card(id)),
            }
        } else {
            match cards.get(id) {
                Some((_, CardState::SignIn)) => {}
                Some((_, now)) => return Err(card_ended(id, now)),
                None => return Err(no_card(id)),
            }
        }
        // Only reveal the exact verified bound tab on the bound extension host; never touch passwords/cookies.
        let _ = &body.origin;
        self.reveal_bound_tab(&conn, session_id, &binding.target_id);
        Ok(CardsAnswerResult { state: CardState::SignIn, note: None })
    }

    fn answer_chrome_takeover_done(
        &self,
        session_id: &str,
        cards: &Arc<Cards>,
        card: &Card,
        state: CardState,
    ) -> Result<CardsAnswerResult, RpcError> {
        let id = card.id.as_str();
        let Some(_) = card.site_login_body().filter(|b| b.mode == SiteLoginMode::ChromeTakeover) else {
            return Err(RpcError::invalid_params("only a Chrome takeover site_login card can be answered with takeover_done"));
        };
        if state != CardState::SignIn {
            return Err(RpcError::invalid_params("confirm and open the Chrome tab before completing sign-in"));
        }
        let Some(SiteLoginBinding::ChromeTakeover(binding)) = cards.binding(id) else {
            return Self::fail_chrome_takeover(
                cards,
                id,
                &[CardState::SignIn],
                "Chrome sign-in target binding is no longer available. Fall back to manual login.".to_string(),
            );
        };
        let (_, tab) = match self.chrome_takeover_tab(binding.extension_host, session_id, &binding.target_id) {
            Ok(found) => found,
            Err(reason) => {
                return Self::fail_chrome_takeover(cards, id, &[CardState::SignIn], reason.to_string());
            }
        };
        if tab.attached
            || !her_browser::session::is_allowed_login_url(&tab.url)
            || her_browser::session::url_origin(&tab.url) != binding.origin
        {
            return Self::fail_chrome_takeover(
                cards,
                id,
                &[CardState::SignIn],
                "The Chrome tab changed origin or lost isolation before sign-in finished. Sign-in card invalidated; fall back to manual login.".to_string(),
            );
        }
        // Fixed status only: never includes URL path/query, page title, DOM, cookies, or credentials.
        let note = format!(
            "The owner completed native Chrome sign-in on {} (tab {}).",
            binding.origin, binding.tab
        );
        match cards.set(id, &[CardState::SignIn], CardState::Done, Some(note.clone())) {
            Ok(_) => Ok(CardsAnswerResult { state: CardState::Done, note: Some(note) }),
            Err(Some(now)) => Err(card_ended(id, now)),
            Err(None) => Err(no_card(id)),
        }
    }

    async fn carry_out_site_login(
        &self,
        session_id: &str,
        card_id: &str,
        origin: &str,
        binding: &SiteLoginFillBinding,
        fields: &HashMap<String, String>,
    ) -> (CardState, Option<String>) {
        let browser = match self
            .site_login_browser_of(session_id, binding.own_browser, &binding.url)
            .await
        {
            Ok(browser) => browser,
            Err(reason) => return (CardState::Failed, Some(reason.into())),
        };
        let mut pairs = ZeroizingPairs(Vec::with_capacity(binding.fields.len()));
        for f in &binding.fields {
            let Some(value) = fields.get(&f.id) else {
                return (CardState::Failed, Some("The fields do not match the card.".into()));
            };
            pairs.0.push((f.backend, f.input_type.clone(), value.clone()));
        }
        // Record the filled target synchronously BEFORE writing so an extension disconnect mid-fill
        // cannot bypass the reconnect observation / desktop guard.
        self.record_site_login_fill(
            session_id,
            binding.own_browser,
            &binding.target_id,
            &binding.origin,
            &binding.url,
        )
        .await;
        let outcome = browser
            .fill_site_login(
                card_id,
                &binding.tab,
                &binding.frame,
                &binding.loader,
                &binding.origin,
                &binding.url,
                &pairs.0,
            )
            .await;
        drop(pairs);
        if outcome.is_err() && !browser.is_dead() && !browser.has_filled_target(&binding.target_id) {
            self.clear_site_login_fill(session_id, binding.own_browser, &binding.target_id).await;
        }
        match outcome {
            Ok(()) => {
                let note = match &binding.browser_label {
                    Some(browser) => format!(
                        "Filled the requested sign-in fields on {origin} in {browser} (tab {}). The form was not submitted.",
                        binding.tab
                    ),
                    None => format!("Filled the requested sign-in fields on {origin}. The form was not submitted."),
                };
                (CardState::Done, Some(note))
            }
            Err(BrowserError::Cdp(her_browser::CdpError::Closed)) => (
                CardState::Failed,
                Some("The browser disconnected before the fields could be filled.".into()),
            ),
            Err(BrowserError::PageChanged { .. } | BrowserError::StaleRef(_)) => (
                CardState::Failed,
                Some("The page changed before the fields could be filled.".into()),
            ),
            Err(_) => (
                CardState::Failed,
                Some(format!("Could not fill the sign-in fields on {origin}. Use browser_wait with user so the owner can sign in directly.")),
            ),
        }
    }

    /// Runs an approved card's ops through the console's handlers, then follows the connector
    /// to where the card goes next, with her's words.
    async fn carry_out(
        &self,
        session: &str,
        card: &Card,
        values: &HashMap<String, String>,
        prior_credentials: &mut Vec<(String, Option<String>)>,
    ) -> (CardState, Option<String>) {
        let Some(body) = card.connect_body() else {
            return (CardState::Failed, Some("Invalid card body.".into()));
        };
        let name = her_core::connect::display_name(&body.name);
        let secrets: Vec<&str> = values.values().map(|value| value.trim()).collect();
        if body.reconnect && let Some(field) = body.fields.first() {
            let credential = card.ops.first().and_then(|op| op.params.get("name")).and_then(Value::as_str);
            let current = self.connectors().connectors().await.ok();
            let still_matches = current.as_ref().is_some_and(|connectors| connectors.iter().any(|existing| {
                existing.name == body.name && existing.url.as_deref() == Some(body.url.as_str()) && !existing.oauth
                    && existing.credential_header.as_ref().is_some_and(|(name, header)| Some(name.as_str()) == credential && header == &field.label)
            }));
            if !still_matches {
                return (CardState::Failed, Some("The connector changed. Ask for a new card.".into()));
            }
        }
        // Keep the previous credential until the card has committed its outcome.
        for op in card.ops.iter().filter(|op| op.method == "credentials/set") {
            if let Some(cred_name) = op.params.get("name").and_then(Value::as_str) {
                let prev = tokio::task::spawn_blocking({
                    let cred_name = cred_name.to_string();
                    move || her_llm::credentials::get(&cred_name)
                })
                .await
                .ok()
                .flatten();
                prior_credentials.push((cred_name.to_string(), prev));
            }
        }
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
                self.restore_credentials(prior_credentials).await;
                return (CardState::Failed, Some(format!("{name} could not be added: {}", redact_secrets(&err.message, &secrets))));
            }
        }
        if body.sign_in == SignIn::Oauth {
            self.connectors().signing_in.lock().insert(body.name.clone(), (session.to_string(), card.id.clone()));
            return (CardState::SignIn, None);
        }
        let (state, note) = self.connected(session, card, &secrets).await;
        if state != CardState::Done {
            // Never leave an unverified/refused key in the credential store.
            self.restore_credentials(prior_credentials).await;
        }
        (state, note)
    }

    async fn restore_credentials(&self, prior: &mut Vec<(String, Option<String>)>) {
        for (cred_name, prev) in prior.drain(..) {
            match prev {
                Some(mut old) => {
                    let _ = self
                        .credentials_set(her_protocol::customize::CredentialsSetParams {
                            name: cred_name,
                            value: old.clone(),
                        })
                        .await;
                    zeroize_string(&mut old);
                }
                None => {
                    let _ = self
                        .credentials_delete(CredentialsDeleteParams { name: cred_name })
                        .await;
                }
            }
        }
    }

    /// Done once the connector connects and lists its tools, and Her's conversation has it; another
    /// try with why it did not.
    async fn connected(&self, session: &str, card: &Card, secrets: &[&str]) -> (CardState, Option<String>) {
        let Some(body) = card.connect_body() else {
            return (CardState::Failed, Some("Invalid card body.".into()));
        };
        let name = her_core::connect::display_name(&body.name);
        match self.mcp_check_target(McpServerParams { name: body.name.clone(), cwd: None }, Some(&body.url)).await {
            Ok(checked) if checked.server.status.state == McpStatusState::Ok => {
                self.add_to_conversation(session, &body.name, Some(&body.url)).await;
                let mut named: Vec<&str> = checked.tools.iter().take(NAMED_TOOLS).map(|tool| tool.name.as_str()).collect();
                if checked.tools.len() > NAMED_TOOLS {
                    named.push("…");
                }
                let tools = if named.is_empty() { String::new() } else { format!(" ({})", named.join(", ")) };
                let verb = if body.reconnect && !body.fields.is_empty() { "key updated" } else { "connected" };
                (CardState::Done, Some(redact_secrets(&format!("{name} {verb}: {} tools{tools}.", checked.tools.len()), secrets)))
            }
            Ok(checked) => {
                let raw = checked.server.status.error.unwrap_or_else(|| "no answer".into());
                (CardState::Retry, Some(format!("{name} did not connect: {}", redact_secrets(&raw, secrets))))
            }
            Err(err) => (CardState::Retry, Some(format!("{name} did not connect: {}", redact_secrets(&err.message, secrets)))),
        }
    }

    /// Her's conversation takes up the connector as the configuration has it now, and connects
    /// it, so that the turn the card's end wakes knows its tools: one it does not know yet would
    /// wait for the owner's approval.
    pub(crate) async fn add_to_conversation(&self, session: &str, connector: &str, approved_url: Option<&str>) {
        let Some(hub) = self.hub(session) else {
            return;
        };
        let cwd = hub.shared.summary.lock().cwd.clone();
        let (server, connector) = (self.clone(), connector.to_string());
        let config = tokio::task::spawn_blocking(move || {
            let workspace = Workspace::open(&server.0.home, &cwd, &server.0.overrides).ok()?;
            server.session_mcp_servers(&workspace, &[]).into_iter().find(|s| s.name == connector)
        })
        .await
        .ok()
        .flatten();
        if let Some(config) = config
            && approved_url.is_none_or(|url| config.url.as_deref() == Some(url))
        {
            let (reply, added) = oneshot::channel();
            if hub.send(HubCmd::AddMcpServer { server: Box::new(config), reply }).await.is_ok()
                && let Ok(Some(settled)) = added.await
            {
                settled.await;
            }
        }
    }

    /// Takes back what a card wrote: the connector it added and the values saved for it, and a
    /// sign-in in progress with what it saved. A connector that signs in again keeps what it had.
    async fn undo(&self, card: &Card) {
        let Some(body) = card.connect_body() else {
            return;
        };
        let name = body.name.clone();
        {
            let mut signing_in = self.connectors().signing_in.lock();
            if signing_in.get(&name).is_some_and(|(_, id)| *id == card.id) {
                signing_in.remove(&name);
            }
        }
        if body.reconnect {
            if body.sign_in == SignIn::Oauth {
                let _ = self.mcp_login_cancel(McpLogoutParams { name }).await;
            }
            return;
        }
        if body.sign_in == SignIn::Oauth
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
            Ok(()) => self.connected(&session, &card, &[]).await,
            Err(error) => (CardState::Retry, Some(format!("The sign-in did not finish: {error}"))),
        };
        let _ = cards.set(&id, &[CardState::SignIn], to, note);
    }
}

struct ZeroizingAnswer(CardAnswer);

impl Drop for ZeroizingAnswer {
    fn drop(&mut self) {
        if let CardAnswer::Fill { fields } = &mut self.0 {
            for value in fields.values_mut() {
                zeroize_string(value);
            }
        }
    }
}

struct ZeroizingPairs(Vec<(i64, String, String)>);

impl Drop for ZeroizingPairs {
    fn drop(&mut self) {
        for (_, _, value) in &mut self.0 {
            zeroize_string(value);
        }
    }
}

fn zeroize_string(s: &mut String) {
    // SAFETY: writing ASCII NUL bytes over the string's valid UTF-8 capacity keeps it valid UTF-8 before clearing.
    unsafe {
        for b in s.as_bytes_mut() {
            std::ptr::write_volatile(b, 0);
        }
    }
    std::sync::atomic::compiler_fence(std::sync::atomic::Ordering::SeqCst);
    s.clear();
}

/// Ensures no submitted secret value can ever be echoed back into a card note, history, or model turn.
fn redact_secrets(text: &str, secrets: &[&str]) -> String {
    let mut out = text.to_string();
    for secret in secrets {
        if !secret.is_empty() && out.contains(secret) {
            out = out.replace(secret, "[redacted]");
        }
    }
    out
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

    #[test]
    fn secret_values_are_redacted_from_error_notes() {
        let note = redact_secrets("HTTP 401: invalid bearer DUMMY_SECRET_99x in header", &["DUMMY_SECRET_99x", ""]);
        assert!(!note.contains("DUMMY_SECRET_99x"), "{note}");
        assert!(note.contains("[redacted]"), "{note}");
    }
}
