//! Secure cards in Her's own conversation (docs/her-secure-cards.md): the `connectors` group
//! prepares a card, `send_to_user` shows it, and how it ends wakes the bot. Scripted model, a
//! connector host of the test's own, real JSONL session log.

use async_trait::async_trait;
use her_core::BotIdentity;
use her_core::Config;
use her_core::HerHome;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::KernelDeps;
use her_core::ResumeOptions;
use her_core::SessionRef;
use her_core::StartOptions;
use her_core::connect::ConnectorHost;
use her_core::connect::ConnectorInfo;
use her_core::store::EntryKind;
use her_core::store::SessionStore;
use her_llm::Catalog;
use her_llm::Context;
use her_llm::Message;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use her_llm::ThinkingLevel;
use her_llm::UserContent;
use her_llm::api::scripted::Script;
use her_llm::api::scripted::ScriptedApi;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::PermissionMode;
use her_protocol::TaskTrigger;
use her_protocol::api::TranscriptItem;
use her_protocol::card::AskedBy;
use her_protocol::card::CardState;
use her_protocol::customize::McpProbeKind;
use her_protocol::customize::McpProbeResult;
use her_protocol::customize::McpProbeSuggestion;
use her_protocol::customize::McpStatusState;
use her_protocol::customize::McpSuggestedAuth;
use her_tools::BashTool;
use her_tools::LocalEnvironment;
use her_tools::builtin_registry;
use serde_json::json;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use tempfile::TempDir;

const URL: &str = "https://mcp.notion.com/mcp";
const DONE: &str = "Notion connected: 2 tools (notion_search, notion_fetch).";

/// One connector, `drive`, that signs in with OAuth; any address checks out as OAuth.
struct Host;

#[async_trait]
impl ConnectorHost for Host {
    async fn connectors(&self) -> Result<Vec<ConnectorInfo>, String> {
        Ok(vec![ConnectorInfo {
            name: "drive".into(),
            url: Some("https://drive.example.com/mcp".into()),
            enabled: true,
            oauth: true,
            state: McpStatusState::NeedsLogin,
            tools: None,
        }])
    }

    async fn probe(&self, _name: &str, _url: &str) -> Result<McpProbeResult, String> {
        Ok(McpProbeResult {
            kind: McpProbeKind::Remote,
            steps: Vec::new(),
            transport: None,
            auth: None,
            suggested: McpProbeSuggestion { auth: McpSuggestedAuth::Oauth, client: None },
            tools: Vec::new(),
            icon: None,
            server_name: Some("Notion MCP".into()),
            error: None,
        })
    }
}

struct Harness {
    kernel: Kernel,
    api: Arc<ScriptedApi>,
    home: HerHome,
    folder: PathBuf,
    _dir: TempDir,
}

fn deps(api: &Arc<ScriptedApi>, work: &Path, home: &HerHome) -> KernelDeps {
    let mut registry = ModelRegistry::new(Catalog::default(), RegistryOptions::default());
    registry.register_api(api.clone());
    registry.insert_provider(ScriptedApi::provider());
    KernelDeps {
        llm: Arc::new(registry),
        tools: Arc::new(builtin_registry(BashTool::default())),
        env: Arc::new(LocalEnvironment::new(work.to_path_buf())),
        store: Arc::new(JsonlStore::new(home.sessions_dir())),
        hooks: vec![],
        mcp_servers: Vec::new(),
        browser_pool: None,
        docs: None,
        decks: None,
        words: None,
        routines: None,
        connectors: Some(Arc::new(Host)),
        mesh: None,
    }
}

/// Her's conversation, whose model asks for the connector named in the owner's message
/// (`connect NAME`, or `connect NAME URL`), shows the card, and passes on how it ended.
async fn her() -> Harness {
    let dir = tempfile::tempdir().unwrap();
    let home = HerHome::new(dir.path().canonicalize().unwrap().join("home"));
    home.ensure().unwrap();
    let folder = home.bot_files_dir("b1");
    std::fs::create_dir_all(&folder).unwrap();
    let folder = folder.canonicalize().unwrap();
    let api = ScriptedApi::new();
    api.respond_with(model);
    let options = StartOptions {
        model: "test/scripted".into(),
        thinking: ThinkingLevel::Off,
        permission_mode: PermissionMode::Manual,
        cwd: folder.clone(),
        home: home.clone(),
        config: Arc::new(Config::default()),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot: Some(BotIdentity { id: "b1".into(), name: "Her".into() }),
    };
    let kernel = Kernel::start(options, deps(&api, &folder, &home)).await.unwrap();
    Harness { kernel, api, home, folder, _dir: dir }
}

fn text_of(message: &Message) -> String {
    match message {
        Message::User(user) => user.text_content(),
        Message::ToolResult(result) => result.text_content(),
        _ => String::new(),
    }
}

fn send_last(id: &str, text: &str) -> Script {
    Script::tool_call(id, "send_to_user", json!({ "text": text, "end_turn": true }))
}

fn model(ctx: &Context) -> Option<Script> {
    let last = ctx.messages.last()?;
    let text = text_of(last);
    if let Message::ToolResult(result) = last {
        if result.is_error {
            return Some(send_last("c8", "That did not work."));
        }
        let id = text.split("Card ").nth(1)?.split_whitespace().next()?;
        return Some(Script::tool_call("c2", "send_to_user", json!({ "type": "card", "card": id, "text": "I need it for this." })));
    }
    if let Some(card) = text.strip_prefix("<card ") {
        let note = card.split_once('>')?.1.strip_suffix("</card>")?;
        return Some(send_last("c9", &format!("转告：{note}")));
    }
    let asked = ctx.messages.iter().rev().filter_map(|m| text_of(m).strip_prefix("connect ").map(str::to_string)).next()?;
    let mut words = asked.split_whitespace();
    let name = words.next()?;
    let mut args = json!({ "name": name });
    if let Some(url) = words.next() {
        args["url"] = json!(url);
    }
    Some(Script::tool_call("c1", "call_tool", json!({ "name": "connector_connect", "arguments": args })))
}

fn prompt(text: &str) -> Vec<UserContent> {
    vec![UserContent::Text { text: text.into() }]
}

/// Collects events until `stop` matches (inclusive). Fails after 10 seconds.
async fn until(kernel: &mut Kernel, stop: impl Fn(&EventMsg) -> bool) -> Vec<Event> {
    let mut events = Vec::new();
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        let remaining = deadline.saturating_duration_since(Instant::now());
        let event = tokio::time::timeout(remaining, kernel.next_event()).await.expect("timed out waiting for an event").expect("session closed");
        let done = stop(&event.msg);
        events.push(event);
        if done {
            return events;
        }
    }
}

/// The card the task showed, and the details it came in.
fn shown(events: &[Event]) -> serde_json::Value {
    events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::ToolCompleted { result, .. } if result.tool_name == "send_to_user" && !result.is_error => {
                serde_json::from_str(result.details.as_ref()?.as_str()).ok()
            }
            _ => None,
        })
        .expect("a card was shown")
}

fn changes(events: &[Event]) -> Vec<(String, CardState)> {
    events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::CardChanged { card_id, state, .. } => Some((card_id.clone(), *state)),
            _ => None,
        })
        .collect()
}

#[tokio::test]
async fn a_card_is_prepared_shown_and_its_end_wakes_her() {
    let mut h = her().await;
    let result = h.kernel.run(prompt(&format!("connect notion {URL}"))).await.unwrap();
    let details = shown(&result.events);
    let card: her_protocol::card::Card = serde_json::from_value(details["card"].clone()).unwrap();
    assert_eq!(details["text"], "I need it for this.");
    assert_eq!((card.title.as_str(), card.asked_by, card.body.url.as_str(), card.body.host.as_str()), ("Connect Notion", AskedBy::Owner, URL, "mcp.notion.com"));
    assert_eq!(changes(&result.events), [(card.id.clone(), CardState::Open)]);
    // Showing it ended the task: no request after it.
    assert_eq!(h.api.requests().len(), 2);
    // The group is in the catalog, and the result tells the model how to show the card.
    let first = &h.api.requests()[0];
    assert!(first.system.contains("connectors: add a connector (an MCP server) by its address, when the owner asks to install or connect one or the task needs an app or service; or sign in to one again. You do it yourself; the owner approves on a card"), "{}", first.system);
    let prepared = text_of(&h.api.requests()[1].messages.last().unwrap().clone());
    assert!(prepared.starts_with(&format!("Nothing changed yet. Card {} asks the owner to connect notion ({URL}) and sign in.", card.id)), "{prepared}");

    // The server carries out the owner's answer; the end wakes Her, who passes it on.
    let cards = h.kernel.cards(&card.id).expect("cards");
    assert_eq!(cards.set(&card.id, &[CardState::Open], CardState::Working, None), Ok(CardState::Open));
    assert_eq!(cards.set(&card.id, &[CardState::Open], CardState::Working, None), Err(Some(CardState::Working)));
    cards.set(&card.id, &[CardState::Working], CardState::Done, Some(DONE.into())).unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert!(events.iter().any(|e| matches!(&e.msg, EventMsg::TaskStarted { trigger: TaskTrigger::Mail, .. })), "{events:#?}");
    let told: Vec<&str> = events.iter().filter_map(|e| match &e.msg {
        EventMsg::ToolStarted { name, args, .. } if name == "send_to_user" => args["text"].as_str(),
        _ => None,
    }).collect();
    assert_eq!(told, [format!("转告：{DONE}")]);
    let read = text_of(h.api.requests().last().unwrap().messages.iter().rev().find(|m| text_of(m).starts_with("<card ")).unwrap());
    assert_eq!(read, format!("<card id=\"{}\" kind=\"connect\" outcome=\"done\">{DONE}</card>", card.id));

    // Clients read how it ended in the transcript, and the log keeps it.
    let ended = h.kernel.transcript().into_iter().find_map(|item| match item {
        TranscriptItem::CardResolved { card_id, outcome, note } => Some((card_id, outcome, note)),
        _ => None,
    });
    assert_eq!(ended, Some((card.id.clone(), CardState::Done, DONE.to_string())));
    let id = h.kernel.session_id().to_string();
    h.kernel.shutdown().await;
    let (_, entries) = JsonlStore::new(h.home.sessions_dir()).read(&SessionRef::Id(id)).unwrap();
    assert_eq!(entries.iter().filter(|e| matches!(e.kind, EntryKind::CardResolved(_))).count(), 1);
}

#[tokio::test]
async fn only_a_card_prepared_in_the_task_shows_once() {
    let mut h = her().await;
    // Prepared and never shown: gone when the task ends.
    h.api.push(Script::tool_call("c1", "call_tool", json!({ "name": "connector_connect", "arguments": { "name": "notion", "url": URL } })));
    h.api.push(send_last("c1b", "Not now."));
    let result = h.kernel.run(prompt("think about it")).await.unwrap();
    let prepared = result.events.iter().find_map(|e| match &e.msg {
        EventMsg::ToolCompleted { result, .. } => result.text_content().split("Card ").nth(1).map(|r| r.split_whitespace().next().unwrap().to_string()),
        _ => None,
    }).unwrap();
    assert!(h.kernel.cards(&prepared).unwrap().get(&prepared).is_none());
    h.api.push(Script::tool_call("c2", "send_to_user", json!({ "type": "card", "card": prepared })));
    h.api.push(Script::tool_call("c3", "send_to_user", json!({ "type": "card", "card": "c_nothing" })));
    h.api.push(Script::tool_call("c4", "send_to_user", json!({ "type": "card", "card": prepared, "options": [{ "label": "yes" }] })));
    h.api.push(send_last("c6", "Fine."));
    let result = h.kernel.run(prompt("show it")).await.unwrap();
    let errors: Vec<String> = result.events.iter().filter_map(|e| match &e.msg {
        EventMsg::ToolCompleted { result, .. } if result.is_error => Some(result.text_content()),
        _ => None,
    }).collect();
    assert_eq!(errors.len(), 3, "{errors:?}");
    assert!(errors[0].contains(&format!("no card {prepared} was prepared")), "{errors:?}");
    assert!(errors[2].contains("options goes with type question"), "{errors:?}");

    // Shown once; a second showing is refused.
    let result = h.kernel.run(prompt(&format!("connect notion {URL}"))).await.unwrap();
    let id = shown(&result.events)["card"]["id"].as_str().unwrap().to_string();
    h.api.push(Script::tool_call("c5", "send_to_user", json!({ "type": "card", "card": id })));
    h.api.push(send_last("c6", "Fine."));
    let result = h.kernel.run(prompt("again")).await.unwrap();
    assert!(result.events.iter().any(|e| matches!(&e.msg, EventMsg::ToolCompleted { result, .. } if result.is_error)));
}

#[tokio::test]
async fn a_newer_card_replaces_the_open_one_and_the_names_are_checked() {
    let mut h = her().await;
    let first = h.kernel.run(prompt(&format!("connect notion {URL}"))).await.unwrap();
    let first = shown(&first.events)["card"]["id"].as_str().unwrap().to_string();
    let second = h.kernel.run(prompt(&format!("connect notion {URL}"))).await.unwrap();
    assert_eq!(changes(&second.events)[1], (first.clone(), CardState::Replaced));
    // The replaced card's end wakes Her too.
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert_eq!(h.kernel.cards(&first).unwrap().get(&first).unwrap().1, CardState::Replaced);

    // An existing connector signs in again without its address, or with the same one.
    let again = h.kernel.run(prompt("connect drive")).await.unwrap();
    let card = &shown(&again.events)["card"];
    assert_eq!((card["title"].as_str(), card["body"]["reconnect"].as_bool(), card["ops"].as_array().map(Vec::len)), (Some("Sign in to Drive again"), Some(true), Some(0)));
    // Another address under its name, or no address for a new name, prepares nothing.
    for asked in ["connect drive https://other.example.com/mcp", "connect nothing", "connect Notion https://x.example.com/mcp"] {
        let result = h.kernel.run(prompt(asked)).await.unwrap();
        assert!(result.events.iter().any(|e| matches!(&e.msg, EventMsg::ToolCompleted { result, .. } if result.is_error)), "{asked}");
    }
}

#[tokio::test]
async fn an_open_card_is_taken_back_after_a_restart() {
    let h = her().await;
    let mut kernel = h.kernel;
    let result = kernel.run(prompt(&format!("connect notion {URL}"))).await.unwrap();
    let id = shown(&result.events)["card"]["id"].as_str().unwrap().to_string();
    let session = kernel.session_id().to_string();
    kernel.shutdown().await;
    let options = ResumeOptions {
        session: SessionRef::Id(session),
        model: None,
        thinking: None,
        permission_mode: None,
        home: h.home.clone(),
        config: Arc::new(Config::default()),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot_name: None,
    };
    let kernel = Kernel::resume(options, deps(&h.api, &h.folder, &h.home)).await.unwrap();
    let cards = kernel.cards(&id).unwrap();
    let (card, state) = cards.get(&id).expect("taken back");
    assert_eq!((card.body.name.as_str(), state), ("notion", CardState::Open));
    assert!(kernel.cards("c_unknown").unwrap().get("c_unknown").is_none());
}
