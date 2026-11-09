//! The `connectors` group (docs/her-secure-cards.md §2.2): Her's own conversation reads which
//! connectors there are, and prepares a `connect` card to add one by its address or sign in to
//! one again. The tools change nothing: the owner's answer to the card does. The service is
//! behind [`ConnectorHost`].

use async_trait::async_trait;
use her_protocol::McpServerConfig;
use her_protocol::card::Card;
use her_protocol::card::CardField;
use her_protocol::card::CardKind;
use her_protocol::card::CardOp;
use her_protocol::card::ConnectBody;
use her_protocol::card::SignIn;
use her_protocol::customize::ConfigEdit;
use her_protocol::customize::ConfigLayer;
use her_protocol::customize::ConfigMerge;
use her_protocol::customize::ConfigWriteParams;
use her_protocol::customize::McpProbeResult;
use her_protocol::customize::McpStatusState;
use her_protocol::customize::McpSuggestedAuth;
use her_tools::Tool;
use her_tools::ToolCtx;
use her_tools::ToolError;
use her_tools::ToolOutput;
use her_tools::Typed;
use her_tools::TypedTool;
use schemars::JsonSchema;
use serde::Deserialize;
use serde_json::Value;
use serde_json::json;
use std::sync::Arc;

use crate::cards::Cards;

pub(crate) const GROUP: &str = "connectors";
pub(crate) const GROUP_SUMMARY: &str = "add a connector (an MCP server) by its address, when the owner asks to install or connect one or the task needs an app or service; or sign in to one again. You do it yourself; the owner approves on a card";

const GUIDELINES: &[&str] = &[
    "Nothing changes until the owner answers the card: never say a connector is connected before you hear it is.",
    "Never ask for a key, a token or a password in a message: the card asks, and you never see it.",
];

/// The field a bearer token is typed in, and the credential it is saved under.
const TOKEN_FIELD: &str = "f1";

/// The service's side of the `connectors` group. Errors are one sentence for the model.
#[async_trait]
pub trait ConnectorHost: Send + Sync + 'static {
    /// Every connector, as the console lists them.
    async fn connectors(&self) -> Result<Vec<ConnectorInfo>, String>;
    /// Checks a server that is not added yet, as `mcp/probe` does.
    async fn probe(&self, name: &str, url: &str) -> Result<McpProbeResult, String>;
}

/// A connector as `connector_status` tells it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ConnectorInfo {
    pub name: String,
    /// Its address; none for one started as a command.
    pub url: Option<String>,
    pub enabled: bool,
    /// It signs in with OAuth.
    pub oauth: bool,
    pub state: McpStatusState,
    pub tools: Option<u32>,
}

/// How Her's conversation reaches the connectors.
pub(crate) struct ConnectorLink {
    pub(crate) host: Arc<dyn ConnectorHost>,
}

pub(crate) fn tools() -> Vec<Arc<dyn Tool>> {
    vec![Arc::new(Typed::new(ConnectorStatus)), Arc::new(Typed::new(ConnectorConnect))]
}

/// What a connector is called on its card and in what Her hears: `notion` is Notion.
pub fn display_name(name: &str) -> String {
    let mut chars = name.chars();
    chars.next().map(|first| first.to_uppercase().chain(chars).collect()).unwrap_or_default()
}

/// The credential a connector's bearer token is saved under, as the console saves it.
fn token_credential(name: &str) -> String {
    format!("mcp.{name}")
}

fn link(ctx: &ToolCtx) -> Result<(Arc<ConnectorLink>, Arc<Cards>), ToolError> {
    let unavailable = || ToolError::Failed("connectors are not available in this session".into());
    Ok((ctx.session_state.get::<ConnectorLink>().ok_or_else(unavailable)?, ctx.session_state.get::<Cards>().ok_or_else(unavailable)?))
}

fn host_of(url: &str) -> String {
    url::Url::parse(url).ok().and_then(|url| url.host_str().map(str::to_string)).unwrap_or_else(|| url.to_string())
}

#[derive(Debug, Deserialize, JsonSchema)]
struct StatusArgs {}

struct ConnectorStatus;

#[async_trait]
impl TypedTool for ConnectorStatus {
    type Args = StatusArgs;
    const NAME: &'static str = "connector_status";
    const DESCRIPTION: &'static str = "List the connectors (MCP servers) the user has: each one's name, where it is, whether it is on, \
        whether it is connected, needs a sign-in or failed, and how many tools it has.";

    fn guidelines(&self) -> &[&'static str] {
        GUIDELINES
    }

    async fn run(&self, _args: StatusArgs, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        let (link, _) = link(&ctx)?;
        let connectors = link.host.connectors().await.map_err(ToolError::Failed)?;
        if connectors.is_empty() {
            return Ok(ToolOutput::text("No connectors yet."));
        }
        let lines: Vec<String> = connectors
            .iter()
            .map(|c| {
                let place = c.url.as_deref().map_or_else(|| "a command on this machine".to_string(), host_of);
                let state = match c.state {
                    McpStatusState::Ok => "connected",
                    McpStatusState::NeedsLogin => "needs sign-in",
                    McpStatusState::Failed => "failed",
                    McpStatusState::Unknown => "not checked yet",
                    McpStatusState::Untrusted => "in a project that is not trusted",
                };
                let mut line = format!("- {} ({place}): {}, {state}", c.name, if c.enabled { "on" } else { "off" });
                if let Some(tools) = c.tools {
                    line.push_str(&format!(", {tools} tools"));
                }
                line
            })
            .collect();
        Ok(ToolOutput::text(lines.join("\n")))
    }
}

#[derive(Debug, Deserialize, JsonSchema)]
struct ConnectArgs {
    /// The connector's name: a short lowercase slug, such as notion. To sign in to an existing connector again, its name.
    name: String,
    /// The MCP server's address, https. Leave it out to sign in to an existing connector again.
    #[serde(default)]
    url: Option<String>,
}

struct ConnectorConnect;

#[async_trait]
impl TypedTool for ConnectorConnect {
    type Args = ConnectArgs;
    const NAME: &'static str = "connector_connect";
    const DESCRIPTION: &'static str = "Prepare a card that asks the user to connect an app or service by its MCP server's address, or to sign \
        in to an existing connector again. The address is checked first. Nothing changes until the user answers the card: show it with \
        send_to_user.";

    fn guidelines(&self) -> &[&'static str] {
        GUIDELINES
    }

    async fn run(&self, args: ConnectArgs, ctx: ToolCtx) -> Result<ToolOutput, ToolError> {
        let (link, cards) = link(&ctx)?;
        let name = args.name.trim();
        if !is_slug(name) {
            return Err(ToolError::InvalidArgs("name must be a short lowercase slug of letters, digits and '-', such as notion".into()));
        }
        let url = args.url.as_deref().map(str::trim).filter(|url| !url.is_empty());
        let existing = link.host.connectors().await.map_err(ToolError::Failed)?.into_iter().find(|c| c.name == name);
        let mut card = match (url, existing) {
            (Some(url), Some(existing)) if existing.url.as_deref() != Some(url) => {
                return Err(ToolError::InvalidArgs(format!("{name} already connects to {}: give the new one another name", existing.url.as_deref().unwrap_or("a command"))));
            }
            (_, Some(existing)) => again(&existing)?,
            (None, None) => return Err(ToolError::InvalidArgs(format!("no connector is named {name}: give its url to add it"))),
            (Some(url), None) => {
                check_address(url)?;
                let probed = link.host.probe(name, url).await.map_err(ToolError::Failed)?;
                if let Some(error) = probed.error {
                    return Err(ToolError::Failed(format!("{url} did not pass the check: {error}. Nothing was prepared.")));
                }
                new_card(name, url, &probed)
            }
        };
        card.asked_by = cards.asked_by();
        let sign_in = if card.body.sign_in == SignIn::Oauth { " and sign in" } else { "" };
        let text = format!(
            "Nothing changed yet. Card {id} asks the owner to connect {name} ({url}){sign_in}. Show it with send_to_user {{type: \"card\", card: \"{id}\"}}: that ends your turn, and you hear how it went.",
            id = card.id,
            url = card.body.url,
        );
        cards.prepare(card);
        Ok(ToolOutput::text(text))
    }
}

/// A connector's name as the console takes it, in lowercase.
fn is_slug(name: &str) -> bool {
    her_protocol::mcp::valid_server_name(name) && name.chars().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '-')
}

/// https, or http on this machine's loopback.
fn check_address(url: &str) -> Result<(), ToolError> {
    let refused = || ToolError::InvalidArgs(format!("url must be an https address (http only on this machine's loopback), not {url}"));
    let parsed = url::Url::parse(url).map_err(|_| refused())?;
    let loopback = match parsed.host() {
        Some(url::Host::Ipv4(ip)) => ip.is_loopback(),
        Some(url::Host::Ipv6(ip)) => ip.is_loopback(),
        Some(url::Host::Domain(name)) => name == "localhost",
        None => false,
    };
    match parsed.scheme() {
        "https" if parsed.host().is_some() => Ok(()),
        "http" if loopback => Ok(()),
        _ => Err(refused()),
    }
}

/// A card that signs an existing connector in again.
fn again(existing: &ConnectorInfo) -> Result<Card, ToolError> {
    let url = existing.url.clone().filter(|_| existing.oauth).ok_or_else(|| ToolError::InvalidArgs(format!("{} has no sign-in to do again", existing.name)))?;
    let body = ConnectBody { name: existing.name.clone(), host: host_of(&url), url, reconnect: true, sign_in: SignIn::Oauth, server_name: None, fields: Vec::new() };
    Ok(card(format!("Sign in to {} again", display_name(&existing.name)), Vec::new(), body))
}

/// A card that adds a connector as the console's Add Connector writes it: OAuth signs in after
/// the connector is written; a server that wants a token and offers no OAuth gets a field for it,
/// saved in the credential store and named in the configuration.
fn new_card(name: &str, url: &str, probed: &McpProbeResult) -> Card {
    let mut server = json!({ "url": url });
    let written = McpServerConfig { name: name.to_string(), url: Some(url.to_string()), ..Default::default() };
    if let Some(transport) = probed.transport.filter(|t| *t != written.remote_transport()) {
        server["transport"] = json!(transport);
    }
    let mut ops = Vec::new();
    let mut fields = Vec::new();
    let sign_in = match probed.suggested.auth {
        McpSuggestedAuth::Oauth => {
            server["auth"] = json!("oauth");
            SignIn::Oauth
        }
        McpSuggestedAuth::Token => {
            let credential = token_credential(name);
            ops.push(CardOp { method: "credentials/set".into(), params: json!({ "name": credential, "value": format!("<value:{TOKEN_FIELD}>") }) });
            server["headers"] = json!({ "Authorization": format!("Bearer ${{credential:{credential}}}") });
            fields.push(CardField { id: TOKEN_FIELD.into(), label: "Authorization".into(), secret: true });
            SignIn::None
        }
        McpSuggestedAuth::None => SignIn::None,
    };
    let write = ConfigWriteParams {
        layer: ConfigLayer::User,
        cwd: None,
        edits: vec![ConfigEdit { key_path: format!("mcp_servers.{name}"), value: server, merge: ConfigMerge::Replace }],
        expected_version: None,
    };
    ops.push(CardOp { method: "config/write".into(), params: serde_json::to_value(write).unwrap_or(Value::Null) });
    let body = ConnectBody {
        name: name.to_string(),
        url: url.to_string(),
        host: host_of(url),
        reconnect: false,
        sign_in,
        server_name: probed.server_name.clone(),
        fields,
    };
    card(format!("Connect {}", display_name(name)), ops, body)
}

fn card(title: String, ops: Vec<CardOp>, body: ConnectBody) -> Card {
    const DIGITS: &[u8] = b"abcdefghijklmnopqrstuvwxyz0123456789";
    let mut bits = rand::random::<u128>();
    let mut id = String::from("c_");
    for _ in 0..10 {
        id.push(DIGITS[(bits % DIGITS.len() as u128) as usize] as char);
        bits /= DIGITS.len() as u128;
    }
    let expires = chrono::Utc::now() + chrono::TimeDelta::hours(crate::cards::LIFE_HOURS);
    Card {
        id,
        kind: CardKind::Connect,
        title,
        asked_by: her_protocol::card::AskedBy::Owner,
        expires_at: expires.to_rfc3339_opts(chrono::SecondsFormat::Secs, true),
        ops,
        body,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_protocol::McpRemoteTransport;
    use her_protocol::customize::McpProbeKind;
    use her_protocol::customize::McpProbeSuggestion;

    fn probed(auth: McpSuggestedAuth, transport: McpRemoteTransport) -> McpProbeResult {
        McpProbeResult {
            kind: McpProbeKind::Remote,
            steps: Vec::new(),
            transport: Some(transport),
            auth: None,
            suggested: McpProbeSuggestion { auth, client: None },
            tools: Vec::new(),
            icon: None,
            server_name: Some("Notion MCP".into()),
            error: None,
        }
    }

    /// The ops are what the console's Add Connector sends for the same server.
    #[test]
    fn a_new_card_writes_the_connector_as_the_console_does() {
        let card = new_card("notion", "https://mcp.notion.com/mcp", &probed(McpSuggestedAuth::Oauth, McpRemoteTransport::StreamableHttp));
        assert!(card.id.starts_with("c_") && card.id.len() == 12, "{}", card.id);
        assert_eq!(card.title, "Connect Notion");
        assert_eq!((card.body.host.as_str(), card.body.sign_in, card.body.reconnect), ("mcp.notion.com", SignIn::Oauth, false));
        assert_eq!(card.body.server_name.as_deref(), Some("Notion MCP"));
        let ops = serde_json::to_value(&card.ops).unwrap();
        assert_eq!(
            ops,
            json!([{"method": "config/write", "params": {"layer": "user", "edits": [{"key_path": "mcp_servers.notion", "value": {"url": "https://mcp.notion.com/mcp", "auth": "oauth"}, "merge": "replace"}]}}])
        );

        let card = new_card("tracker", "https://example.com/sse", &probed(McpSuggestedAuth::Token, McpRemoteTransport::StreamableHttp));
        let ops = serde_json::to_value(&card.ops).unwrap();
        assert_eq!(
            ops,
            json!([
                {"method": "credentials/set", "params": {"name": "mcp.tracker", "value": "<value:f1>"}},
                {"method": "config/write", "params": {"layer": "user", "edits": [{"key_path": "mcp_servers.tracker", "merge": "replace", "value": {
                    "url": "https://example.com/sse", "transport": "streamable_http", "headers": {"Authorization": "Bearer ${credential:mcp.tracker}"}}}]}},
            ])
        );
        assert_eq!(card.body.fields, vec![CardField { id: "f1".into(), label: "Authorization".into(), secret: true }]);
        assert_eq!(card.body.sign_in, SignIn::None);
    }

    #[test]
    fn only_https_or_loopback_addresses_are_taken() {
        for good in ["https://mcp.notion.com/mcp", "http://127.0.0.1:8080/mcp", "http://[::1]:9/mcp", "http://localhost:3000/mcp"] {
            assert!(check_address(good).is_ok(), "{good}");
        }
        for bad in ["http://mcp.notion.com/mcp", "http://192.168.1.2/mcp", "ftp://x/y", "notion", "https://"] {
            assert!(check_address(bad).is_err(), "{bad}");
        }
        assert!(is_slug("notion") && is_slug("my-tracker2"));
        assert!(!is_slug("Notion") && !is_slug("my_tracker") && !is_slug("") && !is_slug("a b"));
    }

    #[test]
    fn signing_in_again_needs_a_sign_in() {
        let mut existing = ConnectorInfo {
            name: "notion".into(),
            url: Some("https://mcp.notion.com/mcp".into()),
            enabled: true,
            oauth: true,
            state: McpStatusState::NeedsLogin,
            tools: None,
        };
        let card = again(&existing).unwrap();
        assert_eq!((card.title.as_str(), card.body.reconnect, card.ops.is_empty()), ("Sign in to Notion again", true, true));
        existing.oauth = false;
        assert!(again(&existing).is_err());
    }
}
