//! her-browser against a real Chromium: a local HTTP server serves test pages and the
//! session drives them as the tools would. Skipped (with a note) where no browser is
//! installed.

#![allow(clippy::print_stderr)]

use async_trait::async_trait;
use her_browser::BrowserConfig;
use her_browser::BrowserError;
use her_browser::BrowserPool;
use her_browser::BrowserSession;
use her_browser::Headless;
use her_browser::NavigationGuard;
use her_browser::SessionOptions;
use her_browser::session::Action;
use her_browser::session::Place;
use her_browser::session::SnapshotMode;
use her_browser::session::SnapshotRequest;
use std::collections::HashMap;
use std::net::IpAddr;
use std::sync::Arc;
use std::time::Duration;
use tokio::io::AsyncReadExt;
use tokio::io::AsyncWriteExt;
use tokio::net::TcpListener;
use tokio_util::sync::CancellationToken;
use url::Url;

/// Serves `pages` by path on 127.0.0.1. `Set-Cookie` for `/set-cookie`.
async fn serve(pages: HashMap<&'static str, String>) -> String {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let pages = Arc::new(pages);
    tokio::spawn(async move {
        loop {
            let Ok((mut stream, _)) = listener.accept().await else { break };
            let pages = pages.clone();
            tokio::spawn(async move {
                let mut buf = vec![0u8; 8192];
                let n = stream.read(&mut buf).await.unwrap_or(0);
                let request = String::from_utf8_lossy(&buf[..n]).to_string();
                let path = request.split_whitespace().nth(1).unwrap_or("/").to_string();
                let path = path.split('?').next().unwrap_or("/").to_string();
                let cookie = request
                    .lines()
                    .find(|l| l.to_ascii_lowercase().starts_with("cookie:"))
                    .map(|l| l[7..].trim().to_string())
                    .unwrap_or_default();
                let (status, body, extra) = match pages.get(path.as_str()) {
                    Some(body) => ("200 OK", body.replace("{{cookie}}", &cookie), String::new()),
                    None if path == "/set-cookie" => (
                        "200 OK",
                        "<title>set</title><p>cookie set</p>".to_string(),
                        "Set-Cookie: her=shared; Path=/\r\n".to_string(),
                    ),
                    None if path == "/file.txt" => (
                        "200 OK",
                        "downloaded text".to_string(),
                        "Content-Disposition: attachment; filename=\"report.txt\"\r\n".to_string(),
                    ),
                    None => ("404 Not Found", "<title>missing</title>".to_string(), String::new()),
                };
                let kind = if path.ends_with(".txt") { "text/plain" } else { "text/html; charset=utf-8" };
                let response = format!(
                    "HTTP/1.1 {status}\r\nContent-Type: {kind}\r\nContent-Length: {}\r\n{extra}Connection: close\r\n\r\n{body}",
                    body.len()
                );
                let _ = stream.write_all(response.as_bytes()).await;
            });
        }
    });
    format!("http://127.0.0.1:{}", addr.port())
}

fn pages() -> HashMap<&'static str, String> {
    let mut pages = HashMap::new();
    pages.insert(
        "/login",
        r#"<title>Sign in</title>
        <h1>Sign in</h1>
        <form action="/welcome" method="get">
          <label>Email <input name="email" type="email"></label>
          <label>Password <input name="password" type="password" autocomplete="current-password"></label>
          <button type="submit">Continue</button>
        </form>"#
            .to_string(),
    );
    pages.insert("/welcome", "<title>Welcome</title><h1>Welcome back</h1><a href=\"/login\">Sign out</a>".to_string());
    pages.insert(
        "/covered",
        r#"<title>Covered</title>
        <button id="target" onclick="document.title='clicked'">Buy</button>
        <div id="banner" style="position:fixed;inset:0;background:rgba(0,0,0,.5)">Accept cookies</div>"#
            .to_string(),
    );
    pages.insert(
        "/dialogs",
        r#"<title>Dialogs</title>
        <button onclick="alert('hello'); document.getElementById('out').textContent = confirm('Delete everything?') ? 'yes' : 'no'">Ask</button>
        <p id="out">waiting</p>"#
            .to_string(),
    );
    pages.insert(
        "/console",
        r#"<title>Console</title>
        <button onclick="console.error('boom from page'); throw new Error('uncaught one')">Break</button>"#
            .to_string(),
    );
    pages.insert(
        "/links",
        r#"<title>Links</title><main><h1>Article</h1><p>First paragraph with a <a href="/welcome">link to welcome</a>.</p>
        <p>Second paragraph.</p><a href="https://blocked.test/x">Bad link</a></main>"#
            .to_string(),
    );
    pages.insert(
        "/clickable",
        r#"<title>Clickable</title><style>.card { cursor: pointer }</style>
        <div class="card" onclick="document.title='opened'"><span>Open the report</span></div>
        <div class="card"><button onclick="document.title='bought'">Buy</button></div>
        <p>Plain text</p>"#
            .to_string(),
    );
    pages.insert("/cookie", "<title>Cookie</title><p id=c>cookie={{cookie}}</p>".to_string());
    pages.insert("/download", "<title>Download</title><a href=\"/file.txt\">Get report</a>".to_string());
    pages.insert(
        "/form",
        r#"<title>Form</title><select id=s aria-label="Size"><option value=s>Small</option><option value=l>Large</option></select>
        <input type=checkbox aria-label="Agree"><input type=file aria-label="Attachment"><input type=color aria-label="Tint">
        <p id=state></p>
        <script>
          document.getElementById('s').addEventListener('change', e => document.getElementById('state').textContent = 'size ' + e.target.value);
          document.querySelector('input[type=file]').addEventListener('change', e => document.getElementById('state').textContent = 'file ' + e.target.files[0].name);
        </script>"#
            .to_string(),
    );
    pages
}

/// Refuses `blocked.test`, as the domain policy would.
struct TestGuard;

#[async_trait]
impl NavigationGuard for TestGuard {
    async fn check_request(&self, url: &Url) -> Result<(), String> {
        if url.host_str() == Some("blocked.test") {
            return Err("blocked.test is blocked by browser.denied_domains".into());
        }
        Ok(())
    }
    fn check_address(&self, _url: &Url, _ip: IpAddr) -> Result<(), String> {
        Ok(())
    }
}

struct Fixture {
    pool: BrowserPool,
    config: BrowserConfig,
    base: String,
    dir: tempfile::TempDir,
}

impl Fixture {
    async fn new() -> Option<Fixture> {
        if her_browser::launch::find_executable().is_none() {
            eprintln!("no Chromium-family browser installed; skipping");
            return None;
        }
        let dir = tempfile::tempdir().unwrap();
        Some(Fixture {
            pool: BrowserPool::new(dir.path().join("browser"), false),
            config: BrowserConfig {
                headless: Headless::On,
                ..BrowserConfig::default()
            },
            base: serve(pages()).await,
            dir,
        })
    }

    async fn session(&self) -> BrowserSession {
        let browser = self.pool.acquire(&self.config).await.expect("browser starts");
        BrowserSession::new(
            browser,
            SessionOptions {
                guard: Arc::new(TestGuard),
                downloads: self.dir.path().join("downloads"),
                navigation_timeout: Duration::from_secs(15),
                action_timeout: Duration::from_secs(10),
                label: None,
                prefix: "",
                numbers: Default::default(),
                note: None,
            },
        )
    }

    fn url(&self, path: &str) -> Url {
        Url::parse(&format!("{}{path}", self.base)).unwrap()
    }
}

/// The ref on the first line containing `needle`.
fn ref_of(text: &str, needle: &str) -> String {
    let line = text
        .lines()
        .find(|l| l.contains(needle) && l.contains("[ref="))
        .unwrap_or_else(|| panic!("no ref for {needle} in:\n{text}"));
    let start = line.find("[ref=").unwrap() + 5;
    line[start..].split(']').next().unwrap().to_string()
}

async fn act(session: &BrowserSession, action: Action, reference: &str) -> Result<String, BrowserError> {
    let element = session.element(reference).await?;
    session
        .act(&action, &Place::Element(element), &CancellationToken::new())
        .await
        .map(|r| r.text)
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn login_form_end_to_end() {
    let Some(fx) = Fixture::new().await else { return };
    let session = fx.session().await;
    let opened = session.navigate(None, &fx.url("/login"), false).await.unwrap();
    assert!(opened.text.contains("heading \"Sign in\""), "{}", opened.text);
    assert_eq!(opened.title, "Sign in");
    let email = ref_of(&opened.text, "textbox \"Email\"");
    let password = ref_of(&opened.text, "textbox \"Password\"");
    let filled = act(&session, Action::Fill("ada@example.com".into()), &email).await.unwrap();
    assert!(filled.contains("Filled textbox \"Email\""), "{filled}");
    assert!(filled.contains("ada@example.com"), "the change shows the new value: {filled}");
    let filled = act(&session, Action::Fill("hunter2".into()), &password).await.unwrap();
    assert!(!filled.contains("hunter2"), "a password never shows: {filled}");
    // A second snapshot of the same document keeps the refs.
    let again = session.snapshot(&SnapshotRequest::default()).await.unwrap();
    assert_eq!(ref_of(&again.text, "textbox \"Email\""), email);
    assert!(again.text.contains("[type=password] [value=<hidden>]"), "{}", again.text);
    let button = ref_of(&again.text, "button \"Continue\"");
    let clicked = act(&session, Action::Click, &button).await.unwrap();
    assert!(clicked.contains("Welcome back"), "the new page's snapshot follows: {clicked}");
    assert!(clicked.contains("/welcome?email=ada%40example.com"), "{clicked}");
    // Refs of the old document are stale, and say why.
    let stale = session.element(&email).await.unwrap_err();
    assert!(stale.to_string().contains("before the page navigated"), "{stale}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn an_approved_page_that_changed_gets_nothing() {
    let Some(fx) = Fixture::new().await else { return };
    let session = fx.session().await;
    let opened = session.navigate(None, &fx.url("/covered"), false).await.unwrap();
    let button = ref_of(&opened.text, "button \"Buy\"");
    // As if approved now and run after another call navigated.
    let element = session.element(&button).await.unwrap();
    session.navigate(None, &fx.url("/welcome"), false).await.unwrap();
    let err = session
        .act(&Action::Click, &Place::Element(element), &CancellationToken::new())
        .await
        .unwrap_err();
    assert!(matches!(err, BrowserError::PageChanged { .. }), "{err}");
    assert!(err.to_string().contains("nothing was sent"));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_covered_button_is_not_clicked() {
    let Some(fx) = Fixture::new().await else { return };
    let session = fx.session().await;
    let opened = session.navigate(None, &fx.url("/covered"), false).await.unwrap();
    let button = ref_of(&opened.text, "button \"Buy\"");
    let err = act(&session, Action::Click, &button).await.unwrap_err().to_string();
    assert!(err.contains("covered by div#banner"), "{err}");
    let page = session.snapshot(&SnapshotRequest::default()).await.unwrap();
    assert_eq!(page.title, "Covered", "nothing was clicked");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_div_that_looks_clickable_gets_a_ref_and_clicks() {
    let Some(fx) = Fixture::new().await else { return };
    let session = fx.session().await;
    let opened = session.navigate(None, &fx.url("/clickable"), false).await.unwrap();
    let card = ref_of(&opened.text, "clickable \"Open the report\"");
    assert_eq!(opened.text.matches("[ref=").count(), 2, "the button's wrapper gets none: {}", opened.text);
    assert!(!opened.text.contains("Plain text\" [ref="), "{}", opened.text);
    act(&session, Action::Click, &card).await.unwrap();
    let page = session.snapshot(&SnapshotRequest::default()).await.unwrap();
    assert_eq!(page.title, "opened");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn dialogs_console_and_text_mode() {
    let Some(fx) = Fixture::new().await else { return };
    let session = fx.session().await;
    let opened = session.navigate(None, &fx.url("/dialogs"), false).await.unwrap();
    let ask = ref_of(&opened.text, "button \"Ask\"");
    let result = act(&session, Action::Click, &ask).await.unwrap();
    assert!(result.contains("alert dialog was shown and accepted: \"hello\""), "{result}");
    assert!(result.contains("confirm dialog was shown and dismissed: \"Delete everything?\""), "{result}");
    assert!(result.contains("\"no\""), "confirm answered no: {result}");

    let opened = session.navigate(None, &fx.url("/console"), false).await.unwrap();
    let broken = act(&session, Action::Click, &ref_of(&opened.text, "button \"Break\"")).await.unwrap();
    assert!(broken.contains("2 new console errors"), "{broken}");
    let console = session.console(None, Some("error"), false).unwrap();
    assert!(console.text.contains("boom from page") && console.text.contains("uncaught one"), "{}", console.text);
    assert!(console.text.contains("<page-content"), "console text is page content");
    assert!(session.console(None, None, false).unwrap().text.contains("No new console messages"));

    session.navigate(None, &fx.url("/links"), false).await.unwrap();
    let text = session
        .snapshot(&SnapshotRequest {
            mode: SnapshotMode::Text,
            ..SnapshotRequest::default()
        })
        .await
        .unwrap();
    assert!(text.text.contains("# Article"), "{}", text.text);
    let link = ref_of(&text.text, "link to welcome");
    let followed = act(&session, Action::Click, &link).await.unwrap();
    assert!(followed.contains("Welcome back"), "{followed}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn page_made_navigation_to_a_refused_domain_is_blocked() {
    let Some(fx) = Fixture::new().await else { return };
    let session = fx.session().await;
    let opened = session.navigate(None, &fx.url("/links"), false).await.unwrap();
    let bad = ref_of(&opened.text, "link \"Bad link\"");
    let result = act(&session, Action::Click, &bad).await.unwrap();
    assert!(result.contains("Blocked loading https://blocked.test/x"), "{result}");
    assert!(session.page(None).unwrap().origin.starts_with("http://127.0.0.1"));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn select_check_upload_and_download() {
    let Some(fx) = Fixture::new().await else { return };
    let session = fx.session().await;
    let opened = session.navigate(None, &fx.url("/form"), false).await.unwrap();
    let size = ref_of(&opened.text, "combobox \"Size\"");
    let selected = act(&session, Action::Select(vec!["Large".into()]), &size).await.unwrap();
    assert!(selected.contains("size l"), "{selected}");
    let agree = ref_of(&opened.text, "checkbox \"Agree\"");
    act(&session, Action::Check(true), &agree).await.unwrap();
    let again = act(&session, Action::Check(true), &agree).await.unwrap();
    assert!(again.contains("already checked"), "{again}");
    let file = fx.dir.path().join("cv.pdf");
    std::fs::write(&file, b"%PDF").unwrap();
    let attachment = ref_of(&opened.text, "Attachment");
    let uploaded = act(&session, Action::Upload(vec![file]), &attachment).await.unwrap();
    assert!(uploaded.contains("file cv.pdf"), "{uploaded}");

    let opened = session.navigate(None, &fx.url("/download"), false).await.unwrap();
    let link = ref_of(&opened.text, "Get report");
    let mut result = act(&session, Action::Click, &link).await.unwrap();
    for _ in 0..20 {
        if result.contains("Downloaded") {
            break;
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
        result = session.snapshot(&SnapshotRequest::default()).await.unwrap().text;
    }
    assert!(result.contains("Downloaded"), "{result}");
    let saved = std::fs::read_to_string(fx.dir.path().join("downloads").join("report.txt")).unwrap();
    assert_eq!(saved, "downloaded text");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn clicks_never_open_what_would_cover_the_screen() {
    let Some(fx) = Fixture::new().await else { return };
    let session = fx.session().await;
    let opened = session.navigate(None, &fx.url("/form"), false).await.unwrap();

    let size = ref_of(&opened.text, "combobox \"Size\"");
    let refused = act(&session, Action::Click, &size).await.unwrap_err().to_string();
    assert!(refused.contains("Use select"), "{refused}");
    let tint = ref_of(&opened.text, "Tint");
    let refused = act(&session, Action::Click, &tint).await.unwrap_err().to_string();
    assert!(refused.contains("color picker"), "{refused}");

    // The page's file chooser goes no further than the tab, and the agent hears of it.
    let attachment = ref_of(&opened.text, "Attachment");
    let clicked = act(&session, Action::Click, &attachment).await.unwrap();
    assert!(clicked.contains("Use upload"), "{clicked}");
    // Only around its actions: nothing is left to say afterwards, and upload still works.
    let file = fx.dir.path().join("cv.pdf");
    std::fs::write(&file, b"%PDF").unwrap();
    let uploaded = act(&session, Action::Upload(vec![file]), &attachment).await.unwrap();
    assert!(uploaded.contains("file cv.pdf") && !uploaded.contains("Use upload"), "{uploaded}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn two_sessions_share_one_browser_and_its_cookies_but_not_tabs() {
    let Some(fx) = Fixture::new().await else { return };
    let a = fx.session().await;
    let b = fx.session().await;
    a.navigate(None, &fx.url("/set-cookie"), false).await.unwrap();
    b.navigate(None, &fx.url("/login"), false).await.unwrap();
    assert_eq!(a.tabs().len(), 1);
    assert_eq!(b.tabs().len(), 1);
    assert_ne!(a.tabs()[0].target_id, b.tabs()[0].target_id);
    let seen = b.navigate(None, &fx.url("/cookie"), false).await.unwrap();
    assert!(seen.text.contains("cookie=her=shared"), "{}", seen.text);
    // The pool hands out the same browser.
    let first = fx.pool.acquire(&fx.config).await.unwrap();
    let second = fx.pool.acquire(&fx.config).await.unwrap();
    assert!(Arc::ptr_eq(&first, &second));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_browser_closes_with_its_last_session_and_starts_again() {
    let Some(fx) = Fixture::new().await else { return };
    let session = fx.session().await;
    session.navigate(None, &fx.url("/login"), false).await.unwrap();
    drop(session);
    // Right away: the pool waits for the old process to let go of the profile.
    let session = fx.session().await;
    let opened = session.navigate(None, &fx.url("/welcome"), false).await.unwrap();
    assert!(opened.text.contains("Welcome back"));
    assert!(!opened.text.contains("temporary profile"), "{}", opened.text);
}
