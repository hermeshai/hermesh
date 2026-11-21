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
            credential_header: None,
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
    let home = HerHome::new(dunce::canonicalize(dir.path()).unwrap().join("home"));
    home.ensure().unwrap();
    let folder = home.bot_files_dir("b1");
    std::fs::create_dir_all(&folder).unwrap();
    let folder = dunce::canonicalize(folder).unwrap();
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
    let body = card.connect_body().expect("connect body");
    assert_eq!(details["text"], "I need it for this.");
    assert_eq!((card.title.as_str(), card.asked_by, body.url.as_str(), body.host.as_str()), ("Connect Notion", AskedBy::Owner, URL, "mcp.notion.com"));
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
    assert_eq!((card.connect_body().unwrap().name.as_str(), state), ("notion", CardState::Open));
    assert!(kernel.cards("c_unknown").unwrap().get("c_unknown").is_none());
}

/// Real isolated headless Chromium end-to-end defensive test for `site_login` secure cards using
/// purely synthetic dummy credentials against a harmless local loopback fixture.
#[tokio::test]
async fn site_login_card_enforces_isolation_observation_blocking_target_binding_and_zero_leakage() {
    use std::sync::atomic::{AtomicUsize, Ordering};
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::TcpListener;

    let Some(chrome) = her_browser::launch::find_executable() else {
        eprintln!("skipping real browser site_login test: no Chromium found");
        return;
    };

    const DUMMY_EMAIL: &str = "dummy-user-e2e@example.test";
    const DUMMY_PASS: &str = "DUMMY_PASS_CANARY_8k2p9z4m";

    let submits = Arc::new(AtomicUsize::new(0));
    let submits_srv = submits.clone();
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let origin = format!("http://127.0.0.1:{port}");
    let localhost_origin = format!("http://localhost:{port}");

    tokio::spawn(async move {
        loop {
            let Ok((mut stream, _)) = listener.accept().await else { break };
            let submits_srv = submits_srv.clone();
            let localhost_origin = localhost_origin.clone();
            tokio::spawn(async move {
                let mut buf = vec![0u8; 8192];
                let n = stream.read(&mut buf).await.unwrap_or(0);
                let req = String::from_utf8_lossy(&buf[..n]);
                let path = req.split_whitespace().nth(1).unwrap_or("/");
                if path.starts_with("/submit") {
                    submits_srv.fetch_add(1, Ordering::SeqCst);
                }
                let body = match path {
                    "/login" => r#"<!doctype html><html><head><title>Fixture Sign In</title></head><body>
                        <form action="/submit" method="post">
                          <label for="u">Email</label><input id="u" name="email" type="email" autocomplete="username">
                          <label for="p">Password</label><input id="p" name="password" type="password" autocomplete="current-password">
                          <button type="submit">Sign In</button>
                        </form>
                      </body></html>"#.to_string(),
                    "/after" => "<!doctype html><html><head><title>Signed In Home</title></head><body><h1>Welcome</h1></body></html>".to_string(),
                    "/captcha" => r#"<!doctype html><html><head><title>Captcha Gate</title></head><body>
                        <div class="g-recaptcha" data-sitekey="dummy-sitekey"></div>
                        <form><label for="p">Password</label><input id="p" type="password"></form>
                      </body></html>"#.to_string(),
                    "/generic" => r#"<!doctype html><html><head><title>Notes</title></head><body>
                        <form><label for="n">Feedback</label><input id="n" name="note" type="text"></form>
                      </body></html>"#.to_string(),
                    "/iframe" => format!(r#"<!doctype html><html><head><title>Iframe Host</title></head><body>
                        <iframe src="{localhost_origin}/login" width="400" height="300"></iframe>
                      </body></html>"#),
                    _ => "<!doctype html><html><head><title>Other</title></head><body>OK</body></html>".to_string(),
                };
                let resp = format!(
                    "HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
                let _ = stream.write_all(resp.as_bytes()).await;
            });
        }
    });

    let dir = tempfile::tempdir().unwrap();
    let home = HerHome::new(dunce::canonicalize(dir.path()).unwrap().join("home"));
    home.ensure().unwrap();
    let folder = home.bot_files_dir("b1");
    std::fs::create_dir_all(&folder).unwrap();
    let folder = dunce::canonicalize(folder).unwrap();
    let api = ScriptedApi::new();
    let mut cfg = Config::default();
    cfg.browser.enabled = Some(true);
    cfg.browser.config.executable = Some(chrome);
    cfg.browser.config.headless = her_browser::Headless::On;

    let mut d = deps(&api, &folder, &home);
    d.browser_pool = Some(Arc::new(her_core::BrowserPool::new(home.browser_dir(), false)));

    let options = StartOptions {
        model: "test/scripted".into(),
        thinking: ThinkingLevel::Off,
        permission_mode: PermissionMode::BypassPermissions,
        cwd: folder.clone(),
        home: home.clone(),
        config: Arc::new(cfg),
        project_root: None,
        trusted: true,
        interactive_approvals: false,
        bot: Some(BotIdentity { id: "b1".into(), name: "Her".into() }),
    };
    let mut kernel = Kernel::start(options, d).await.unwrap();

    // 1. Generic non-credential form must be rejected (no arbitrary collection card).
    api.push(Script::tool_call("n1", "call_tool", json!({"name": "browser_navigate", "arguments": {"url": format!("{origin}/generic")}})));
    api.push(Script::tool_call("n2", "call_tool", json!({"name": "browser_act", "arguments": {"action": "ask_owner", "refs": ["e1"]}})));
    api.push(send_last("n3", "generic checked"));
    let res = kernel.run(prompt("check generic")).await.unwrap();
    let err_generic = res.events.iter().find_map(|e| match &e.msg {
        EventMsg::ToolCompleted { call_id, result, .. } if call_id == "n2" => Some((result.is_error, result.text_content())),
        _ => None,
    }).unwrap();
    assert!(err_generic.0, "generic input must be rejected: {:?}", err_generic);

    // 2. CAPTCHA page must refuse card fill and instruct native takeover fallback.
    api.push(Script::tool_call("c1", "call_tool", json!({"name": "browser_navigate", "arguments": {"url": format!("{origin}/captcha")}})));
    api.push(Script::tool_call("c2", "call_tool", json!({"name": "browser_act", "arguments": {"action": "ask_owner", "refs": ["e2"]}})));
    api.push(send_last("c3", "captcha checked"));
    let res = kernel.run(prompt("check captcha")).await.unwrap();
    let err_captcha = res.events.iter().find_map(|e| match &e.msg {
        EventMsg::ToolCompleted { call_id, result, .. } if call_id == "c2" => Some((result.is_error, result.text_content())),
        _ => None,
    }).unwrap();
    assert!(err_captcha.0 && err_captcha.1.contains("browser_wait"), "captcha must fall back to takeover: {:?}", err_captcha);

    // 3. Normal isolated login page: prepare & show site_login card, block observation, fill without
    //    auto-submit, block observation after fill, verify one-time consumption, then recover on nav.
    api.push(Script::tool_call("l1", "call_tool", json!({"name": "browser_navigate", "arguments": {"url": format!("{origin}/login")}})));
    // Dynamic scripted step reads refs from l1's snapshot, calls ask_owner, then send_to_user card.
    api.respond_with(|ctx| {
        let last = ctx.messages.last()?;
        let text = text_of(last);
        if let Message::ToolResult(r) = last {
            if r.call_id == "l1" {
                let mut refs = Vec::new();
                for line in text.lines() {
                    if (line.contains("Email") || line.contains("Password"))
                        && let Some(rest) = line.split("[ref=").nth(1)
                        && let Some(id) = rest.split(']').next()
                    {
                        refs.push(id.to_string());
                    }
                }
                return Some(Script::tool_call("l2", "call_tool", json!({"name": "browser_act", "arguments": {"action": "ask_owner", "refs": refs}})));
            }
            if r.call_id == "l2" {
                let id = text.split("Card ").nth(1)?.split_whitespace().next()?;
                return Some(Script::tool_call("l3", "send_to_user", json!({"type": "card", "card": id, "text": "Please sign in."})));
            }
        }
        if text.starts_with("<card ") {
            return Some(send_last("l9", "card handled"));
        }
        None
    });

    let res = kernel.run(prompt("sign in to fixture")).await.unwrap();
    let details = shown(&res.events);
    let card: her_protocol::card::Card = serde_json::from_value(details["card"].clone()).unwrap();
    assert_eq!(card.kind, her_protocol::card::CardKind::SiteLogin);
    assert!(card.ops.is_empty());
    let body = card.site_login_body().expect("site_login body");
    assert_eq!(body.origin, origin);
    assert_eq!(body.fields.len(), 2);
    assert_eq!(body.fields[0].secret, false);
    assert_eq!(body.fields[1].secret, true);

    // While card is pending: ALL model observation APIs on the isolated browser must be blocked!
    let browser = kernel.builtin_browser().expect("builtin browser");
    assert!(browser.snapshot(&her_browser::session::SnapshotRequest::default()).await.is_err());
    assert!(browser.screenshot(None, false, None).await.is_err());
    assert!(browser.console(None, None, false).is_err());
    assert!(browser.wait(None, &her_browser::session::WaitFor::Text("Email".into()), Duration::from_millis(200)).await.is_err());
    let page = browser.page(None).unwrap();
    assert!(browser.eval(&page, "document.title").await.is_err());

    // Fill via bound target (server-side path) — never auto-submits, never saves password.
    let cards = kernel.cards(&card.id).expect("cards");
    let her_core::cards::SiteLoginBinding::Fill(binding) = cards.binding(&card.id).expect("binding") else {
        panic!("expected Fill binding");
    };
    let pairs: Vec<(i64, String, String)> = vec![
        (binding.fields[0].backend, binding.fields[0].input_type.clone(), DUMMY_EMAIL.into()),
        (binding.fields[1].backend, binding.fields[1].input_type.clone(), DUMMY_PASS.into()),
    ];
    browser.fill_site_login(&card.id, &binding.tab, &binding.frame, &binding.loader, &binding.origin, &pairs).await.unwrap();
    cards.set(&card.id, &[CardState::Open], CardState::Done, Some(format!("Filled the requested sign-in fields on {origin}. The form was not submitted."))).unwrap();
    let _ = until(&mut kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;

    // Must NOT have auto-submitted!
    assert_eq!(submits.load(Ordering::SeqCst), 0, "site_login must never auto-submit");

    // After fill, before navigation: ALL model observations remain blocked!
    assert!(browser.snapshot(&her_browser::session::SnapshotRequest::default()).await.is_err());
    assert!(browser.screenshot(None, false, None).await.is_err());
    assert!(browser.console(None, None, false).is_err());
    assert!(browser.eval(&page, "document.getElementById('p').value").await.is_err());
    assert!(browser.wait(None, &her_browser::session::WaitFor::Text("Welcome".into()), Duration::from_millis(200)).await.is_err());

    // One-time consumption: cannot transition Done card again.
    assert!(cards.set(&card.id, &[CardState::Open], CardState::Working, None).is_err());

    // Safe recovery: navigating to /after clears the sensitive guard and observation resumes.
    let after_url = url::Url::parse(&format!("{origin}/after")).unwrap();
    browser.navigate(None, &after_url, true).await.unwrap();
    let snap = browser.snapshot(&her_browser::session::SnapshotRequest::default()).await.unwrap();
    assert!(snap.text.contains("Welcome"), "{}", snap.text);

    // 4. Target change (TOCTOU): navigate away before fill -> PageChanged, zero fill.
    let login_url = url::Url::parse(&format!("{origin}/login")).unwrap();
    browser.navigate(None, &login_url, true).await.unwrap();
    let snap = browser.snapshot(&her_browser::session::SnapshotRequest::default()).await.unwrap();
    let refs: Vec<String> = snap.text.lines()
        .filter(|l| l.contains("Email") || l.contains("Password"))
        .filter_map(|l| l.split("[ref=").nth(1)?.split(']').next().map(str::to_string))
        .collect();
    let inspected = browser.inspect_site_login(&refs).await.unwrap();
    browser.begin_sensitive_card("c_toctou0001", &inspected.tab, &inspected.frame, &inspected.loader, &inspected.origin, &[inspected.fields[0].backend, inspected.fields[1].backend]);
    // Page moves away before user submits!
    browser.navigate(None, &after_url, true).await.unwrap();
    let toctou = browser.fill_site_login(
        "c_toctou0001",
        &inspected.tab,
        &inspected.frame,
        &inspected.loader,
        &inspected.origin,
        &[(inspected.fields[0].backend, inspected.fields[0].input_type.clone(), DUMMY_EMAIL.into()), (inspected.fields[1].backend, inspected.fields[1].input_type.clone(), DUMMY_PASS.into())],
    ).await;
    assert!(matches!(toctou, Err(her_browser::BrowserError::PageChanged { .. })), "TOCTOU must fail safely: {toctou:?}");

    // 5. Cancel/decline releases observation guard immediately.
    browser.navigate(None, &login_url, true).await.unwrap();
    let snap = browser.snapshot(&her_browser::session::SnapshotRequest::default()).await.unwrap();
    let refs: Vec<String> = snap.text.lines()
        .filter(|l| l.contains("Email") || l.contains("Password"))
        .filter_map(|l| l.split("[ref=").nth(1)?.split(']').next().map(str::to_string))
        .collect();
    let inspected = browser.inspect_site_login(&refs).await.unwrap();
    browser.begin_sensitive_card("c_cancel0001", &inspected.tab, &inspected.frame, &inspected.loader, &inspected.origin, &[inspected.fields[0].backend, inspected.fields[1].backend]);
    assert!(browser.snapshot(&her_browser::session::SnapshotRequest::default()).await.is_err());
    browser.end_sensitive_card("c_cancel0001");
    assert!(browser.snapshot(&her_browser::session::SnapshotRequest::default()).await.is_ok());

    // 6. Zero leakage check across all LLM requests and all files under home (sessions, logs, credentials).
    for req in api.requests() {
        let dumped = format!("{:?}", req);
        assert!(!dumped.contains(DUMMY_PASS), "password leaked to model context!");
        assert!(!dumped.contains(DUMMY_EMAIL), "email leaked to model context!");
    }
    let session_id = kernel.session_id().to_string();
    kernel.shutdown().await;
    // Check that site_login card is NEVER restored across restart (unresolved skips SiteLogin).
    let (_, entries) = JsonlStore::new(home.sessions_dir()).read(&SessionRef::Id(session_id)).unwrap();
    assert!(entries.iter().any(|e| matches!(&e.kind, EntryKind::CardResolved(r) if r.card_id == card.id && r.kind == "site_login")));
    fn walk_files(dir: &Path, out: &mut Vec<PathBuf>) {
        for e in std::fs::read_dir(dir).into_iter().flatten().flatten() {
            let p = e.path();
            if p.is_dir() { walk_files(&p, out); } else { out.push(p); }
        }
    }
    let mut disk_files = Vec::new();
    walk_files(home.root(), &mut disk_files);
    for path in disk_files {
        let text = String::from_utf8_lossy(&std::fs::read(&path).unwrap_or_default()).into_owned();
        assert!(!text.contains(DUMMY_PASS), "password leaked to disk file {}", path.display());
        assert!(!text.contains(DUMMY_EMAIL), "email leaked to disk file {}", path.display());
    }
}
