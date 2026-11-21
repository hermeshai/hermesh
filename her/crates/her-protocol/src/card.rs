//! Secure cards (docs/her-secure-cards.md): what Her asks of the owner on a card that only the
//! apps draw. A tool prepares it, `send_to_user` shows it in its result's details
//! (`{"card": Card, "text"?: string}`), and the owner's device answers it with `cards/answer`.

use schemars::JsonSchema;
use serde::Deserialize;
use serde::Serialize;
use serde_json::Value;
use std::collections::HashMap;

/// A card as the apps draw it.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct Card {
    /// `c_` and 10 random letters and digits. It means something only in its session.
    pub id: String,
    pub kind: CardKind,
    /// In English: for a kind the app does not know, and for notifications. Apps draw the kinds
    /// they know from `body`, in the owner's language.
    pub title: String,
    pub asked_by: AskedBy,
    /// RFC 3339: when the card expires (24 hours for `connect`, 5 minutes for `site_login`).
    pub expires_at: String,
    /// The console calls approving makes, in order, as the console sends them. A value the owner
    /// types stands as the whole string `"<value:FIELD_ID>"`. Empty for `site_login`.
    pub ops: Vec<CardOp>,
    pub body: CardBody,
}

impl Card {
    pub fn connect_body(&self) -> Option<&ConnectBody> {
        match &self.body {
            CardBody::Connect(body) if self.kind == CardKind::Connect => Some(body),
            _ => None,
        }
    }

    pub fn site_login_body(&self) -> Option<&SiteLoginBody> {
        match &self.body {
            CardBody::SiteLogin(body) if self.kind == CardKind::SiteLogin => Some(body),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum CardKind {
    /// A connector added by its address, or one that signs in again / updates its key.
    Connect,
    /// A website's sign-in form filled once in an isolated browser, never saved or auto-submitted.
    SiteLogin,
}

impl CardKind {
    pub fn as_str(self) -> &'static str {
        match self {
            CardKind::Connect => "connect",
            CardKind::SiteLogin => "site_login",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(untagged)]
pub enum CardBody {
    Connect(ConnectBody),
    SiteLogin(SiteLoginBody),
}

/// What began the task that made the card: the owner's message, or something else that reached
/// Her (a routine's report, a card's outcome, a sub-agent's report), which the owner did not ask for.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum AskedBy {
    Owner,
    Her,
}

/// One console call: its method and its params.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct CardOp {
    pub method: String,
    pub params: Value,
}

/// A `connect` card's body.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct ConnectBody {
    /// The connector's name.
    pub name: String,
    pub url: String,
    /// The URL's host, which the card shows large.
    pub host: String,
    /// An existing connector signs in again; `ops` is empty then.
    pub reconnect: bool,
    pub sign_in: SignIn,
    /// What the server calls itself, when the check found out.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub server_name: Option<String>,
    /// Values the owner types.
    pub fields: Vec<CardField>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum SignIn {
    /// The device runs the sign-in (`mcp/login`, `mcp/login_complete`) once the card says `sign_in`.
    Oauth,
    None,
}

/// A value the owner types into the card.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct CardField {
    pub id: String,
    pub label: String,
    /// Masked as it is typed.
    pub secret: bool,
}

/// A `site_login` card's mode.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum SiteLoginMode {
    /// Bound form fill: fill 1–3 DOM-verified login fields once without auto-submitting or saving.
    #[default]
    Fill,
    /// Local Chrome isolated user takeover: owner confirms actual HTTPS origin and target tab,
    /// then signs in natively in Chrome while model access and debugger are paused/detached.
    ChromeTakeover,
}

/// A `site_login` card's body: inspected directly from the page's real DOM in a bound browser tab
/// (`fill`), or from a verified local Chrome tab for isolated user takeover (`chrome_takeover`).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SiteLoginBody {
    /// How the sign-in card works: `fill` (one-time form fill) or `chrome_takeover` (isolated local Chrome takeover).
    #[serde(default)]
    pub mode: SiteLoginMode,
    /// Verified top-level and frame origin (`https://example.com`, or loopback for local fixtures).
    pub origin: String,
    /// The origin's host (with non-default port), shown large on the card.
    pub host: String,
    /// The page's title, sanitized and clipped.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    /// The browser holding the bound tab when targeting the owner's own Chrome (omitted for the built-in browser).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub browser: Option<String>,
    /// The session's bound tab id (for example `y1` in the owner's own Chrome).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tab: Option<String>,
    /// Verified login fields on the page (never authored by the model; empty for `chrome_takeover`).
    #[serde(default)]
    pub fields: Vec<SiteLoginField>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum SiteLoginFieldKind {
    Email,
    Username,
    Password,
    OneTimeCode,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SiteLoginField {
    pub id: String,
    pub label: String,
    pub kind: SiteLoginFieldKind,
    /// Masked as it is typed (`password` and `one_time_code`).
    pub secret: bool,
}

/// Where a card stands. Ended: done, declined, failed, expired, replaced.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum CardState {
    /// Shown, waiting for the owner.
    Open,
    /// Approved: her carries it out.
    Working,
    /// The device signs in now.
    SignIn,
    /// It did not work (a refused token, a failed sign-in): the owner may approve again with new
    /// values, or decline.
    Retry,
    Done,
    Declined,
    Failed,
    Expired,
    /// A newer card for the same thing took its place.
    Replaced,
}

impl CardState {
    pub fn is_ended(self) -> bool {
        matches!(self, CardState::Done | CardState::Declined | CardState::Failed | CardState::Expired | CardState::Replaced)
    }

    pub fn as_str(self) -> &'static str {
        match self {
            CardState::Open => "open",
            CardState::Working => "working",
            CardState::SignIn => "sign_in",
            CardState::Retry => "retry",
            CardState::Done => "done",
            CardState::Declined => "declined",
            CardState::Failed => "failed",
            CardState::Expired => "expired",
            CardState::Replaced => "replaced",
        }
    }
}

/// Parameters of `cards/answer`, which only the owner's devices may call.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct CardsAnswerParams {
    pub session_id: String,
    pub card_id: String,
    pub answer: CardAnswer,
}

#[derive(Clone, PartialEq, Serialize, Deserialize, JsonSchema)]
#[serde(tag = "decision", rename_all = "snake_case")]
pub enum CardAnswer {
    /// `ops` are the card's, each `"<value:ID>"` replaced by what the owner typed.
    Approve { ops: Vec<CardOp> },
    /// For `site_login` (`fill` mode): values keyed by field id (`f1`, `f2`, …); carries no `ops`.
    Fill { fields: HashMap<String, String> },
    /// For `site_login` (`chrome_takeover` mode): owner confirmed the verified HTTPS origin and
    /// target tab and wants to bring that Chrome tab to the front to sign in natively.
    TakeoverStart,
    /// For `site_login` (`chrome_takeover` mode): owner finished signing in natively in Chrome.
    TakeoverDone,
    Decline,
}

/// Never shows the values the owner typed.
impl std::fmt::Debug for CardAnswer {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CardAnswer::Approve { ops } => f.debug_struct("Approve").field("ops", &ops.len()).finish_non_exhaustive(),
            CardAnswer::Fill { fields } => f.debug_struct("Fill").field("fields", &fields.len()).finish_non_exhaustive(),
            CardAnswer::TakeoverStart => f.write_str("TakeoverStart"),
            CardAnswer::TakeoverDone => f.write_str("TakeoverDone"),
            CardAnswer::Decline => f.write_str("Decline"),
        }
    }
}

/// Result of `cards/answer`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct CardsAnswerResult {
    pub state: CardState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}
