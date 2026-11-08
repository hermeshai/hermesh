//! The browser tools end to end: a scripted model drives a real Chromium through the kernel,
//! with approvals, site grants and the check that a call runs only on the page it was
//! approved for (docs/kernel/browser-design.md §11.2). Skipped where no browser is installed.
//!
//! The pages are served on 127.0.0.1, but under the name `shop.test`: the browser maps it
//! there (a wrapper adds `--host-resolver-rules`), while the kernel cannot resolve it and so
//! treats it as an ordinary public site, which asks before it is opened or acted on.

#![allow(clippy::print_stderr)]

use her_core::BrowserPool;
use her_core::HerHome;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::KernelDeps;
use her_core::StartOptions;
use her_core::TaskEnd;
use her_llm::Catalog;
use her_llm::Message;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use her_llm::ThinkingLevel;
use her_llm::UserContent;
use her_llm::api::scripted::Script;
use her_llm::api::scripted::ScriptedApi;
use her_protocol::ApprovalDecision;
use her_protocol::Event;
use her_protocol::EventMsg;
use her_protocol::GrantAuthority;
use her_protocol::Op;
use her_protocol::PermissionMode;
use her_protocol::SiteGrant;
use her_tools::BashTool;
use her_tools::LocalEnvironment;
use her_tools::builtin_registry;
use serde_json::json;
use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use tempfile::TempDir;
use tokio::io::AsyncReadExt;
use tokio::io::AsyncWriteExt;
use tokio::net::TcpListener;

/// Serves `pages` by path on 127.0.0.1; returns the port.
async fn serve(pages: HashMap<&'static str, &'static str>) -> u16 {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let pages = Arc::new(pages);
    tokio::spawn(async move {
        loop {
            let Ok((mut stream, _)) = listener.accept().await else { break };
            let pages = pages.clone();
            tokio::spawn(async move {
                let mut buf = vec![0u8; 8192];
                let n = stream.read(&mut buf).await.unwrap_or(0);
                let request = String::from_utf8_lossy(&buf[..n]).to_string();
                let target = request.split_whitespace().nth(1).unwrap_or("/");
                let (path, query) = target.split_once('?').unwrap_or((target, ""));
                if path == "/beat" {
                    *BEATS.lock().unwrap().entry((port, query.to_string())).or_default() += 1;
                }
                let (status, body) = match pages.get(path) {
                    Some(body) => ("200 OK", *body),
                    None => ("404 Not Found", "<title>missing</title>"),
                };
                let response = format!(
                    "HTTP/1.1 {status}\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
                let _ = stream.write_all(response.as_bytes()).await;
            });
        }
    });
    port
}

/// Heartbeats of `/alive` pages, by server port and the page's query: a page beats while its
/// tab is open.
static BEATS: std::sync::LazyLock<std::sync::Mutex<HashMap<(u16, String), u64>>> = std::sync::LazyLock::new(Default::default);

fn beats(origin: &str, who: &str) -> u64 {
    let port: u16 = origin.rsplit(':').next().unwrap().parse().unwrap();
    BEATS.lock().unwrap().get(&(port, who.to_string())).copied().unwrap_or(0)
}

/// Whether the page of `who` is still beating: beats come within `window`.
async fn beating(origin: &str, who: &str, window: Duration) -> bool {
    let before = beats(origin, who);
    tokio::time::sleep(window).await;
    beats(origin, who) > before
}

fn pages() -> HashMap<&'static str, &'static str> {
    let mut pages = HashMap::new();
    pages.insert("/beat", "");
    pages.insert(
        "/alive",
        "<title>Alive</title><h1>Alive</h1><script>setInterval(() => fetch('/beat' + location.search), 150)</script>",
    );
    pages.insert(
        "/login",
        r#"<title>Sign in</title><h1>Sign in</h1>
        <form action="/welcome" method="get">
          <label>Email <input name="email" type="email"></label>
          <label>Password <input name="password" type="password"></label>
          <button type="submit">Continue</button>
        </form>"#,
    );
    pages.insert("/welcome", "<title>Welcome</title><h1>Welcome back</h1><p>Signed in.</p>");
    pages.insert("/other", "<title>Other</title><h1>Another page</h1><button>Nothing</button>");
    pages
}

/// A script that runs the installed browser with `shop.test` mapped to 127.0.0.1.
fn wrapper(dir: &Path) -> Option<PathBuf> {
    let browser = her_browser::launch::find_executable()?;
    if cfg!(windows) {
        return None;
    }
    let path = dir.join("browser.sh");
    std::fs::write(
        &path,
        format!(
            "#!/bin/sh\nexec \"{}\" --host-resolver-rules=\"MAP *.test 127.0.0.1\" --no-proxy-server \"$@\"\n",
            browser.display()
        ),
    )
    .unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).unwrap();
    }
    Some(path)
}

struct Harness {
    kernel: Kernel,
    api: Arc<ScriptedApi>,
    origin: String,
    /// The user configuration file.
    config: PathBuf,
    _dir: TempDir,
}

/// A session with the browser tools on, or `None` without a browser on this machine.
async fn harness(mode: PermissionMode, interactive: bool, sites: &[(&str, &str)]) -> Option<Harness> {
    harness_with(mode, interactive, sites, "").await
}

/// `extra`: more lines for `[browser]`.
async fn harness_with(mode: PermissionMode, interactive: bool, sites: &[(&str, &str)], extra: &str) -> Option<Harness> {
    harness_config(mode, interactive, sites, extra, true).await
}

/// `enabled`: the built-in browser is on from the start.
async fn harness_config(mode: PermissionMode, interactive: bool, sites: &[(&str, &str)], extra: &str, enabled: bool) -> Option<Harness> {
    let dir = tempfile::tempdir().unwrap();
    let Some(executable) = wrapper(dir.path()) else {
        eprintln!("no Chromium-family browser installed; skipping");
        return None;
    };
    let port = serve(pages()).await;
    let origin = format!("http://shop.test:{port}");
    let work = dir.path().join("work");
    std::fs::create_dir_all(&work).unwrap();
    let home = HerHome::new(dir.path().join("home"));
    home.ensure().unwrap();
    let user = home.config_file();
    let mut toml = format!(
        "[browser]\nenabled = {enabled}\nheadless = true\nexecutable = \"{}\"\n{extra}\n[browser.sites]\n",
        executable.display()
    );
    for (site, level) in sites {
        toml.push_str(&format!("\"{}\" = \"{level}\"\n", site.replace("ORIGIN", &origin)));
    }
    std::fs::write(&user, toml).unwrap();
    let files = her_core::config::LayerFiles {
        user: &user,
        project: None,
        user_project: None,
        project_root: None,
    };
    let config = her_core::config::load_layers(&files, true, &[]).unwrap().config;
    let api = ScriptedApi::new();
    let mut registry = ModelRegistry::new(Catalog::default(), RegistryOptions::default());
    registry.register_api(api.clone());
    registry.insert_provider(ScriptedApi::provider());
    let deps = KernelDeps {
        llm: Arc::new(registry),
        tools: Arc::new(builtin_registry(BashTool::default())),
        env: Arc::new(LocalEnvironment::new(work.clone())),
        store: Arc::new(JsonlStore::new(home.sessions_dir())),
        hooks: vec![],
        mcp_servers: Vec::new(),
        browser_pool: Some(Arc::new(BrowserPool::new(home.browser_dir(), false))),
        docs: None,
        decks: None,
        words: None,
        routines: None,
    };
    let options = StartOptions {
        model: "test/scripted".into(),
        thinking: ThinkingLevel::Off,
        permission_mode: mode,
        cwd: work,
        home,
        config: Arc::new(config),
        project_root: None,
        trusted: true,
        interactive_approvals: interactive,
        bot: None,
    };
    let kernel = Kernel::start(options, deps).await.unwrap();
    Some(Harness {
        kernel,
        api,
        origin,
        config: user,
        _dir: dir,
    })
}

/// Runs a task to its end; fails after 60 seconds.
async fn run(kernel: &mut Kernel, text: &str) -> her_core::TaskResult {
    let input = vec![UserContent::Text { text: text.into() }];
    tokio::time::timeout(Duration::from_secs(60), kernel.run(input))
        .await
        .unwrap_or_else(|_| panic!("\"{text}\" did not finish"))
        .unwrap()
}

/// The tool results of the last request to the model.
fn last_results(api: &ScriptedApi) -> Vec<String> {
    let requests = api.requests();
    let last = requests.last().unwrap();
    let results: Vec<String> = last
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::ToolResult(r) => Some(r.text_content()),
            _ => None,
        })
        .collect();
    results
}

/// The ref of the line in `text` that contains `what`.
fn ref_of(text: &str, what: &str) -> String {
    let line = text
        .lines()
        .find(|l| l.contains(what) && l.contains("[ref="))
        .unwrap_or_else(|| panic!("no ref for {what} in:\n{text}"));
    let start = line.find("[ref=").unwrap() + 5;
    line[start..].split(']').next().unwrap().to_string()
}

/// Collects events until `stop` matches (inclusive), answering approvals with `answer`.
async fn until(kernel: &mut Kernel, stop: impl Fn(&EventMsg) -> bool) -> Vec<Event> {
    let mut events = Vec::new();
    let deadline = Instant::now() + Duration::from_secs(60);
    loop {
        let remaining = deadline.saturating_duration_since(Instant::now());
        let event = tokio::time::timeout(remaining, kernel.next_event())
            .await
            .expect("timed out waiting for an event")
            .expect("the session ended");
        let done = stop(&event.msg);
        events.push(event);
        if done {
            return events;
        }
    }
}

fn approval(events: &[Event]) -> (String, String, Vec<SiteGrant>) {
    match &events.last().unwrap().msg {
        EventMsg::ApprovalRequested {
            call_id,
            summary,
            proposed_grants,
            ..
        } => (call_id.clone(), summary.clone(), proposed_grants.clone()),
        other => panic!("expected an approval request, got {other:?}"),
    }
}

async fn approve(kernel: &mut Kernel, call_id: String, decision: ApprovalDecision) {
    kernel.submit(Op::Approval { call_id, decision }).await.unwrap();
}

fn asked(events: &[Event]) -> bool {
    events.iter().any(|e| matches!(e.msg, EventMsg::ApprovalRequested { .. }))
}

#[tokio::test]
async fn approvals_grants_and_the_approved_page() {
    let Some(mut h) = harness(PermissionMode::Manual, true, &[]).await else {
        return;
    };
    let login = format!("{}/login", h.origin);

    // 1. Opening a site asks, offering this site and every site; approving once opens it.
    h.api.push(Script::tool_call("c1", "browser_navigate", json!({ "url": login })));
    h.api.push(Script::text("opened"));
    h.kernel.submit(Op::prompt("sign in")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let (call_id, summary, grants) = approval(&events);
    assert_eq!(summary, format!("open {login}"));
    let site_grants = vec![
        SiteGrant {
            authority: GrantAuthority::Act,
            origin: h.origin.clone(),
        },
        SiteGrant {
            authority: GrantAuthority::Act,
            origin: "*".into(),
        },
    ];
    assert_eq!(grants, site_grants);
    approve(&mut h.kernel, call_id, ApprovalDecision::Approve).await;
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let page = last_results(&h.api).pop().unwrap();
    assert!(page.contains("Sign in") && page.contains("<page-content"), "{page}");
    assert!(page.contains("[type=password]") || page.contains("Password"), "{page}");
    let email = ref_of(&page, "Email");
    let password = ref_of(&page, "Password");
    let button = ref_of(&page, "Continue");

    // 2. Reading asks nothing; the first action on the site asks the same question again.
    h.api.push(
        Script::tool_call("c2", "browser_snapshot", json!({ "query": "continue" }))
            .then_tool_call("c3", "browser_act", json!({ "action": "fill", "ref": email, "text": "ada@example.com" })),
    );
    h.api.push(Script::text("filled"));
    h.kernel.submit(Op::prompt("fill it")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let (call_id, summary, grants) = approval(&events);
    assert!(summary.starts_with("fill") && summary.ends_with(&format!("on {}", h.origin)), "{summary}");
    assert_eq!(grants, site_grants);
    approve(&mut h.kernel, call_id, ApprovalDecision::ApproveForSession { grant: None, persist: false }).await;
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let results = last_results(&h.api);
    let found = &results[results.len() - 2];
    assert!(found.contains("Continue") && !found.contains("Email"), "query: {found}");
    assert!(results.last().unwrap().contains("ada@example.com"), "{}", results.last().unwrap());

    // 3. With the grant, typing a password and submitting ask nothing; the password does
    // not show, and the click lands on the next page.
    h.api.push(
        Script::tool_call("c4", "browser_act", json!({ "action": "fill", "ref": password, "text": "hunter2" }))
            .then_tool_call("c5", "browser_act", json!({ "action": "click", "ref": button })),
    );
    h.api.push(Script::text("signed in"));
    let result = run(&mut h.kernel, "submit").await;
    assert!(matches!(result.end, TaskEnd::Completed { .. }));
    assert!(!asked(&result.events));
    let results = last_results(&h.api);
    let filled = &results[results.len() - 2];
    assert!(!filled.contains("hunter2"), "{filled}");
    let clicked = results.last().unwrap();
    assert!(clicked.contains("Welcome back"), "{clicked}");

    // 4. An old ref after the navigation is refused before anything is asked or sent.
    h.api.push(Script::tool_call("c6", "browser_act", json!({ "action": "click", "ref": button })));
    h.api.push(Script::text("stale"));
    let result = run(&mut h.kernel, "click again").await;
    assert!(!asked(&result.events));
    let stale = last_results(&h.api).pop().unwrap();
    assert!(stale.contains("navigated"), "{stale}");

    // 5. Navigate and act in one reply: the act was approved for the page before the
    // navigation, so it sends nothing.
    let other = format!("{}/other", h.origin);
    let current = {
        h.api.push(Script::tool_call("c8", "browser_navigate", json!({ "url": login })));
        h.api.push(Script::text("back at login"));
        run(&mut h.kernel, "login again").await;
        last_results(&h.api).pop().unwrap()
    };
    let continue_now = ref_of(&current, "Continue");
    h.api.push(
        Script::tool_call("c9", "browser_navigate", json!({ "url": other }))
            .then_tool_call("c10", "browser_act", json!({ "action": "click", "ref": continue_now })),
    );
    h.api.push(Script::text("raced"));
    let result = run(&mut h.kernel, "race").await;
    assert!(!asked(&result.events), "the site is granted");
    let results = last_results(&h.api);
    assert!(results[results.len() - 2].contains("Another page"), "{}", results[results.len() - 2]);
    let raced = results.last().unwrap();
    assert!(raced.contains("nothing was sent") || raced.contains("navigated"), "{raced}");

    // 6. Tabs: listed, and a details payload for user interfaces.
    let details = result
        .events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::ToolCompleted { result, .. } if result.tool_name == "browser_navigate" => result.details.clone(),
            _ => None,
        })
        .expect("details");
    let details: serde_json::Value = serde_json::from_str(details.as_str()).unwrap();
    assert_eq!(details["url"], json!(other), "{details}");
    assert_eq!(details["title"], json!("Other"));
    h.api.push(Script::tool_call("c11", "browser_tabs", json!({})));
    h.api.push(Script::text("listed"));
    run(&mut h.kernel, "tabs").await;
    let listing = last_results(&h.api).pop().unwrap();
    assert!(listing.contains("(current)") && listing.contains("/other"), "{listing}");
    h.kernel.shutdown().await;
}

#[tokio::test]
async fn without_an_approver_only_configured_sites_open() {
    // `her exec`: nobody to ask. An unconfigured site gets an answer that says how to allow it.
    let Some(mut h) = harness(PermissionMode::Manual, false, &[]).await else {
        return;
    };
    let login = format!("{}/login", h.origin);
    h.api.push(Script::tool_call("c1", "browser_navigate", json!({ "url": login })));
    h.api.push(Script::text("cannot"));
    let result = run(&mut h.kernel, "open it").await;
    assert!(!asked(&result.events));
    let text = last_results(&h.api).pop().unwrap();
    assert!(text.contains("nobody can approve it") && text.contains("[browser.sites]"), "{text}");
    assert!(text.contains(&h.origin), "{text}");
    h.kernel.shutdown().await;

    // Configured to act: opens and acts without asking.
    let Some(mut h) = harness(PermissionMode::Manual, false, &[("ORIGIN", "act")]).await else {
        return;
    };
    let login = format!("{}/login", h.origin);
    h.api.push(Script::tool_call("c1", "browser_navigate", json!({ "url": login })));
    h.api.push(Script::text("opened"));
    run(&mut h.kernel, "open it").await;
    let page = last_results(&h.api).pop().unwrap();
    let button = ref_of(&page, "Continue");
    h.api.push(Script::tool_call("c2", "browser_act", json!({ "action": "click", "ref": button })));
    h.api.push(Script::text("done"));
    let result = run(&mut h.kernel, "continue").await;
    assert!(!asked(&result.events));
    assert!(last_results(&h.api).pop().unwrap().contains("Welcome back"));
    h.kernel.shutdown().await;
}

#[tokio::test]
async fn denied_sites_and_metadata_addresses_are_refused_in_every_mode() {
    let Some(mut h) = harness(PermissionMode::BypassPermissions, true, &[("ORIGIN", "deny")]).await else {
        return;
    };
    let login = format!("{}/login", h.origin);
    h.api.push(
        Script::tool_call("c1", "browser_navigate", json!({ "url": login }))
            .then_tool_call("c2", "browser_navigate", json!({ "url": "http://169.254.169.254/latest/meta-data" })),
    );
    h.api.push(Script::text("refused"));
    let result = run(&mut h.kernel, "open").await;
    assert!(!asked(&result.events));
    let results = last_results(&h.api);
    assert!(results[0].contains("denied by browser.sites"), "{}", results[0]);
    assert!(results[1].contains("cloud metadata"), "{}", results[1]);
    // Bypass opens other sites without asking; reading before anything is open says so.
    h.api.push(Script::tool_call("c3", "browser_snapshot", json!({})));
    h.api.push(Script::text("nothing"));
    run(&mut h.kernel, "read").await;
    assert!(last_results(&h.api).pop().unwrap().contains("No page is open"));
    h.kernel.shutdown().await;
}

#[tokio::test]
async fn a_batch_runs_its_steps_and_stops_where_approval_is_needed() {
    // A configured site: every step runs, the form is filled and sent in one call.
    let Some(mut h) = harness(PermissionMode::Manual, true, &[("ORIGIN", "act")]).await else {
        return;
    };
    let login = format!("{}/login", h.origin);
    h.api.push(Script::tool_call("c1", "browser_navigate", json!({ "url": login })));
    h.api.push(Script::text("opened"));
    run(&mut h.kernel, "open").await;
    let page = last_results(&h.api).pop().unwrap();
    let steps = json!([
        { "tool": "browser_act", "args": { "action": "fill", "ref": ref_of(&page, "Email"), "text": "ada@example.com" } },
        { "tool": "browser_act", "args": { "action": "fill", "ref": ref_of(&page, "Password"), "text": "hunter2" } },
        { "tool": "browser_act", "args": { "action": "click", "ref": ref_of(&page, "Continue") } },
        { "tool": "browser_snapshot", "args": { "query": "welcome" } },
    ]);
    h.api.push(Script::tool_call("c2", "browser_batch", json!({ "steps": steps })));
    h.api.push(Script::text("sent"));
    let result = run(&mut h.kernel, "sign in").await;
    assert!(!asked(&result.events));
    let text = last_results(&h.api).pop().unwrap();
    assert!(text.contains("Step 4 of 4 (browser_snapshot)") && text.contains("Welcome back"), "{text}");
    assert!(!text.contains("value=\"hunter2\""), "{text}");
    h.kernel.shutdown().await;

    // No grant: the first step is asked about with the batch; the second, on a site not yet
    // allowed for acting, stops the batch.
    let Some(mut h) = harness(PermissionMode::Manual, true, &[]).await else {
        return;
    };
    let other = format!("{}/other", h.origin);
    let steps = json!([
        { "tool": "browser_navigate", "args": { "url": other } },
        { "tool": "browser_act", "args": { "action": "scroll" } },
        { "tool": "browser_snapshot" },
    ]);
    h.api.push(Script::tool_call("c1", "browser_batch", json!({ "steps": steps })));
    h.api.push(Script::text("stopped"));
    h.kernel.submit(Op::prompt("go")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let (call_id, summary, _) = approval(&events);
    assert_eq!(summary, format!("open {other}"));
    approve(&mut h.kernel, call_id, ApprovalDecision::Approve).await;
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert_eq!(events.iter().filter(|e| matches!(e.msg, EventMsg::ApprovalRequested { .. })).count(), 0);
    let text = last_results(&h.api).pop().unwrap();
    // Steps before the last report briefly: where the page is, not its snapshot.
    assert!(text.contains("Page: \"Other\"") && !text.contains("Another page"), "the first step ran: {text}");
    assert!(text.contains("Take a snapshot to see the page."), "{text}");
    assert!(text.contains("Step 2 (browser_act) needs approval") && text.contains("1 of 3 steps ran"), "{text}");

    // Batches do not nest, and an unknown tool fails before anything runs.
    h.api.push(Script::tool_call("c2", "browser_batch", json!({ "steps": [{ "tool": "browser_batch" }] })));
    h.api.push(Script::text("no"));
    run(&mut h.kernel, "nest").await;
    assert!(last_results(&h.api).pop().unwrap().contains("cannot be batched"));
    h.kernel.shutdown().await;
}

#[tokio::test]
async fn scripts_run_only_when_enabled_and_ask_once_per_site() {
    let Some(mut h) = harness_with(PermissionMode::BypassPermissions, true, &[], "script_enabled = false").await else {
        return;
    };
    let other = format!("{}/other", h.origin);
    h.api.push(Script::tool_call("c1", "browser_navigate", json!({ "url": other })));
    h.api.push(Script::text("opened"));
    run(&mut h.kernel, "open").await;
    h.api.push(Script::tool_call("c2", "browser_eval", json!({ "expression": "1 + 1" })));
    h.api.push(Script::text("no tool"));
    run(&mut h.kernel, "eval").await;
    assert!(last_results(&h.api).pop().unwrap().contains("Unknown tool"), "turned off: not even listed");
    h.kernel.shutdown().await;

    // On by default (browser-design §10.1), and asked about once per site, even in bypass.
    let Some(mut h) = harness(PermissionMode::BypassPermissions, true, &[]).await else {
        return;
    };
    let other = format!("{}/other", h.origin);
    h.api.push(Script::tool_call("c1", "browser_navigate", json!({ "url": other })));
    h.api.push(Script::text("opened"));
    run(&mut h.kernel, "open").await;
    h.api.push(Script::tool_call(
        "c2",
        "browser_eval",
        json!({ "expression": "document.querySelector('h1').textContent = 'Changed'; ({ title: document.title, n: 6 * 7 })" }),
    ));
    h.api.push(Script::text("ran"));
    h.kernel.submit(Op::prompt("eval")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let (call_id, summary, grants) = approval(&events);
    assert_eq!(summary, format!("run a script on {}", h.origin), "asked even in bypass");
    assert_eq!(grants, vec![SiteGrant { authority: GrantAuthority::Script, origin: h.origin.clone() }]);
    approve(&mut h.kernel, call_id, ApprovalDecision::ApproveForSession { grant: Some(0), persist: true }).await;
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let text = last_results(&h.api).pop().unwrap();
    assert!(text.contains("\"n\": 42") && text.contains("\"title\": \"Other\""), "{text}");
    assert!(text.contains("Changed"), "the change shows: {text}");
    // The site's next script runs without a question; nothing went into the configuration.
    h.api.push(Script::tool_call("c3", "browser_eval", json!({ "expression": "6 * 7" })));
    h.api.push(Script::text("ran again"));
    h.kernel.submit(Op::prompt("again")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert!(!events.iter().any(|e| matches!(e.msg, EventMsg::ApprovalRequested { .. })), "asked once per site");
    assert!(last_results(&h.api).pop().unwrap().contains("42"));
    let saved = std::fs::read_to_string(&h.config).unwrap_or_default();
    assert!(!saved.contains("script"), "{saved}");
    h.kernel.shutdown().await;
}

#[tokio::test]
async fn always_allowing_a_site_writes_it_to_the_user_configuration() {
    let Some(mut h) = harness(PermissionMode::Manual, true, &[]).await else {
        return;
    };
    let login = format!("{}/login", h.origin);
    h.api.push(Script::tool_call("c1", "browser_navigate", json!({ "url": login })));
    h.api.push(Script::text("opened"));
    h.kernel.submit(Op::prompt("open")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let (call_id, _, _) = approval(&events);
    approve(&mut h.kernel, call_id, ApprovalDecision::ApproveForSession { grant: Some(0), persist: true }).await;
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let saved: toml::Table = std::fs::read_to_string(&h.config).unwrap().parse().unwrap();
    assert_eq!(saved["browser"]["sites"][&h.origin].as_str(), Some("act"), "{saved}");
    assert_eq!(saved["browser"]["enabled"].as_bool(), Some(true), "the rest of the file stays");
    h.kernel.shutdown().await;
}

#[tokio::test]
async fn always_allowing_every_site_writes_it_to_the_user_configuration() {
    let Some(mut h) = harness(PermissionMode::Manual, true, &[]).await else {
        return;
    };
    let login = format!("{}/login", h.origin);
    h.api.push(Script::tool_call("c1", "browser_navigate", json!({ "url": login })));
    h.api.push(Script::text("opened"));
    h.kernel.submit(Op::prompt("open")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let (call_id, _, grants) = approval(&events);
    assert_eq!(grants[1].origin, "*");
    approve(&mut h.kernel, call_id, ApprovalDecision::ApproveForSession { grant: Some(1), persist: true }).await;
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    let saved: toml::Table = std::fs::read_to_string(&h.config).unwrap().parse().unwrap();
    assert_eq!(saved["browser"]["sites"]["*"].as_str(), Some("act"), "{saved}");
    // Acting on the page asks nothing from then on.
    let page = last_results(&h.api).pop().unwrap();
    let email = ref_of(&page, "Email");
    h.api.push(Script::tool_call("c2", "browser_act", json!({ "action": "fill", "ref": email, "text": "ada@example.com" })));
    h.api.push(Script::text("filled"));
    let result = run(&mut h.kernel, "fill").await;
    assert!(!asked(&result.events), "every site is allowed");
    h.kernel.shutdown().await;
}

#[tokio::test]
async fn tabs_close_when_the_session_ends() {
    let Some(mut h) = harness(PermissionMode::Manual, false, &[("ORIGIN", "act")]).await else {
        return;
    };
    let alive = format!("{}/alive?end", h.origin);
    h.api.push(Script::tool_call("c1", "browser_navigate", json!({ "url": alive })));
    h.api.push(Script::text("open"));
    run(&mut h.kernel, "open").await;
    assert!(beating(&h.origin, "end", Duration::from_secs(1)).await, "the page runs");
    h.kernel.shutdown().await;
    // Let a beat already on its way arrive, then nothing more.
    tokio::time::sleep(Duration::from_millis(500)).await;
    assert!(!beating(&h.origin, "end", Duration::from_millis(1500)).await, "the tab closed with the session");
}

#[tokio::test]
async fn a_sub_agents_tabs_close_when_it_reports_and_the_main_sessions_stay() {
    let Some(mut h) = harness(PermissionMode::Manual, false, &[("ORIGIN", "act")]).await else {
        return;
    };
    let main = format!("{}/alive?main", h.origin);
    let child = format!("{}/alive?child", h.origin);
    // The main session opens a page, then waits for an agent that opens one of its own.
    h.api.push(Script::tool_call("c1", "browser_navigate", json!({ "url": main })));
    h.api.push(Script::tool_call("c2", "spawn_agent", json!({ "description": "open the page", "task": "open the page", "name": "web", "wait": true })));
    h.api.push(Script::tool_call("k1", "browser_navigate", json!({ "url": child })));
    // While the agent still works, its page runs beside the main one.
    h.api.push(Script::tool_call("k2", "browser_wait", json!({ "seconds": 1 })));
    h.api.push(Script::text("opened it"));
    h.api.push(Script::text("the agent is done"));
    let result = run(&mut h.kernel, "go").await;
    assert!(matches!(result.end, TaskEnd::Completed { .. }), "{:?}", result.end);
    let all: Vec<String> = h
        .api
        .requests()
        .iter()
        .flat_map(|r| r.messages.iter().filter_map(|m| match m {
            Message::ToolResult(r) => Some(format!("{}: {}", r.tool_name, r.text_content())),
            _ => None,
        }))
        .collect();
    assert!(beats(&h.origin, "child") > 0, "the agent's page ran: {all:#?}");
    tokio::time::sleep(Duration::from_millis(500)).await;
    assert!(!beating(&h.origin, "child", Duration::from_millis(1500)).await, "the agent's tab closed when it reported");
    assert!(beating(&h.origin, "main", Duration::from_millis(1500)).await, "the main session's tab is still open");
    // The main session still sees only its own tab.
    h.api.push(Script::tool_call("c3", "browser_tabs", json!({})));
    h.api.push(Script::text("listed"));
    run(&mut h.kernel, "tabs").await;
    let listing = last_results(&h.api).pop().unwrap();
    assert!(listing.contains("1 tab:") && listing.contains("alive?main"), "{listing}");
    h.kernel.shutdown().await;
}

#[tokio::test]
async fn a_sub_agent_asks_for_a_site_and_the_grant_holds_for_the_tree() {
    let Some(mut h) = harness(PermissionMode::Manual, true, &[]).await else {
        return;
    };
    let login = format!("{}/login", h.origin);
    h.api.push(Script::tool_call("c1", "spawn_agent", json!({ "description": "open the page", "task": "open the page", "name": "web", "wait": true })));
    h.api.push(Script::tool_call("k1", "browser_navigate", json!({ "url": login })));
    h.api.push(Script::text("opened it"));
    // The site is allowed now: the main session opens it without asking.
    h.api.push(Script::tool_call("c2", "browser_navigate", json!({ "url": login })));
    h.api.push(Script::text("done"));
    h.kernel.submit(Op::prompt("go")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::AgentEvent { event, .. } if matches!(event.msg, EventMsg::ApprovalRequested { .. }))).await;
    let EventMsg::AgentEvent { agent_id, event } = &events.last().unwrap().msg else {
        unreachable!()
    };
    let EventMsg::ApprovalRequested { call_id, proposed_grants, .. } = &event.msg else {
        unreachable!()
    };
    assert_eq!(proposed_grants[0].origin, h.origin);
    h.kernel
        .submit(Op::AgentApproval {
            agent: agent_id.clone(),
            call_id: call_id.clone(),
            decision: ApprovalDecision::ApproveForSession { grant: Some(0), persist: false },
        })
        .await
        .unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert!(!asked(&events), "the main session was not asked");
    let page = last_results(&h.api).pop().unwrap();
    assert!(page.contains("Sign in"), "{page}");
    h.kernel.shutdown().await;
}

/// The browser settings as the user configuration file has them now, as the service hands them
/// to an open session when the file changes (connectors-entry-design §9.4).
fn settings_from(file: &Path) -> her_core::SystemSettings {
    let files = her_core::config::LayerFiles {
        user: file,
        project: None,
        user_project: None,
        project_root: None,
    };
    her_core::SystemSettings::of(&her_core::config::load_layers(&files, true, &[]).unwrap().config)
}

fn rewrite(file: &Path, from: &str, to: &str) {
    let text = std::fs::read_to_string(file).unwrap();
    assert!(text.contains(from), "{text}");
    std::fs::write(file, text.replace(from, to)).unwrap();
}

fn tool_names(api: &ScriptedApi) -> Vec<String> {
    api.requests().last().unwrap().tools.iter().map(|t| t.name.clone()).collect()
}

/// The browser turned on while a session is open: from its next request the catalog lists the
/// browser group, tool_search gives its definitions and guidelines, and call_tool runs them; the
/// tool list itself only gains the two search tools, and the frozen part of the prompt stays.
/// Turned off again, the group goes and the session's tabs close (connectors-entry-design §9.4,
/// computer-use-design §5).
#[tokio::test]
async fn the_browser_turned_on_and_off_while_a_session_is_open() {
    let Some(mut h) = harness_config(PermissionMode::BypassPermissions, true, &[], "", false).await else {
        return;
    };
    h.api.push(Script::text("no browser yet"));
    run(&mut h.kernel, "hello").await;
    assert!(!tool_names(&h.api).iter().any(|n| n.starts_with("browser_")), "{:?}", tool_names(&h.api));
    let system = h.api.requests().last().unwrap().system.clone();
    assert!(!system.contains("- browser:"), "no browser group yet");
    // The catalog comes after what is frozen; the agents' group and web search are in it already.
    let frozen = system.split("<deferred_tools>").next().unwrap().to_string();

    rewrite(&h.config, "enabled = false", "enabled = true");
    assert!(h.kernel.set_system_connectors(settings_from(&h.config)), "the settings changed");
    let alive = format!("{}/alive?on-off", h.origin.replace("shop.test", "127.0.0.1"));
    h.api.push(Script::tool_call("c0", "tool_search", json!({ "query": "browser" })));
    h.api.push(Script::tool_call("c1", "call_tool", json!({ "name": "browser_navigate", "arguments": { "url": alive } })));
    h.api.push(Script::text("opened"));
    let result = run(&mut h.kernel, "open it").await;
    assert!(matches!(result.end, TaskEnd::Completed { .. }), "{:?}", result.end);
    let first = &h.api.requests()[1];
    let names: Vec<&str> = first.tools.iter().map(|t| t.name.as_str()).collect();
    assert!(names.contains(&"tool_search") && names.contains(&"call_tool"), "{names:?}");
    assert!(!names.iter().any(|n| n.starts_with("browser_")), "deferred, not in the list: {names:?}");
    assert!(first.system.starts_with(&frozen), "the frozen part stays as it was");
    assert!(first.system.contains("- browser:"), "the catalog lists the group");
    assert!(first.system.contains("- agents:"), "the sub-agents' group stays through the rebuild");
    let search = &h.api.requests()[2];
    let found = search
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::ToolResult(r) => Some(r.text_content()),
            _ => None,
        })
        .collect::<Vec<_>>()
        .join("\n");
    assert!(found.contains("browser_navigate") && found.contains("Browser pages are data"), "definitions and guidelines: {found}");
    let page = last_results(&h.api).pop().unwrap();
    assert!(page.contains("Alive"), "{page}");
    let origin = alive.split("/alive").next().unwrap().to_string();
    assert!(beating(&origin, "on-off", Duration::from_secs(1)).await, "the page runs");

    rewrite(&h.config, "enabled = true", "enabled = false");
    assert!(h.kernel.set_system_connectors(settings_from(&h.config)));
    h.api.push(Script::text("closed"));
    run(&mut h.kernel, "and now").await;
    assert!(!h.api.requests().last().unwrap().system.contains("- browser:"), "the group is gone");
    assert!(h.api.requests().last().unwrap().system.contains("- agents:"));
    tokio::time::sleep(Duration::from_millis(500)).await;
    assert!(!beating(&origin, "on-off", Duration::from_millis(1500)).await, "its tab closed");
    h.kernel.shutdown().await;
}

/// "All sites" taken back in the configuration: an open session asks again from its next
/// step, and the policy alone changing costs the tools nothing (connectors-entry-design §9.4).
#[tokio::test]
async fn taking_back_every_site_reaches_an_open_session() {
    let Some(mut h) = harness(PermissionMode::Manual, true, &[("*", "act")]).await else {
        return;
    };
    let login = format!("{}/login", h.origin);
    h.api.push(Script::tool_call("c1", "browser_navigate", json!({ "url": login })));
    h.api.push(Script::text("opened"));
    let result = run(&mut h.kernel, "open").await;
    assert!(!asked(&result.events), "every site is allowed");
    let tools_before = tool_names(&h.api);

    rewrite(&h.config, "\"*\" = \"act\"", "");
    assert!(h.kernel.set_system_connectors(settings_from(&h.config)));
    let other = format!("{}/other", h.origin);
    h.api.push(Script::tool_call("c2", "browser_navigate", json!({ "url": other })));
    h.api.push(Script::text("asked"));
    h.kernel.submit(Op::prompt("again")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let (call_id, summary, _) = approval(&events);
    assert!(summary.contains("/other"), "{summary}");
    assert_eq!(tool_names(&h.api), tools_before, "the policy alone changes no tool");
    approve(&mut h.kernel, call_id, ApprovalDecision::Approve).await;
    until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    h.kernel.shutdown().await;
}

/// A browser tool run through call_tool is judged as itself: in manual mode the first visit to
/// a site asks, with the site's grants, and the tool started says what it does
/// (computer-use-design §5).
#[tokio::test]
async fn a_browser_tool_through_call_tool_is_judged_as_itself() {
    let Some(mut h) = harness(PermissionMode::Manual, true, &[]).await else {
        return;
    };
    let login = format!("{}/login", h.origin);
    h.api.push(Script::tool_call("c1", "call_tool", json!({ "name": "browser_navigate", "arguments": { "url": login } })));
    h.api.push(Script::text("opened"));
    h.kernel.submit(Op::prompt("open the login page")).await.unwrap();
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::ApprovalRequested { .. })).await;
    let started = events.iter().find_map(|e| match &e.msg {
        EventMsg::ToolStarted { title, .. } => Some(title.clone()),
        _ => None,
    });
    assert_eq!(started.as_deref(), Some("open shop.test"), "the browser tool's own title");
    let (call_id, summary, grants) = approval(&events);
    assert!(summary.contains("/login"), "{summary}");
    assert!(!grants.is_empty(), "the site's grants are offered");
    approve(&mut h.kernel, call_id, ApprovalDecision::Approve).await;
    let events = until(&mut h.kernel, |m| matches!(m, EventMsg::TaskCompleted { .. })).await;
    assert!(!events.iter().any(|e| matches!(e.msg, EventMsg::ApprovalRequested { .. })));
    assert!(last_results(&h.api).pop().unwrap().contains("Sign in"), "the page opened");
    h.kernel.shutdown().await;
}
