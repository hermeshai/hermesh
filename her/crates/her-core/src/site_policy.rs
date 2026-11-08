//! Approval of browser calls by what they do on which site (docs/kernel/browser-design.md
//! §7.2, §7.3): the URL, the address class, the domain lists, the configured sites and the
//! session's grants, then the permission mode. Reads only the `SessionView`.

use her_browser::policy::ANY_SITE;
use her_browser::policy::AddressClass;
use her_browser::policy::SiteLevel;
use her_browser::policy::UrlKind;
use her_browser::policy::classify_url;
use her_browser::policy::inside;
use her_browser::policy::origin_of;
use her_browser::policy::url_kind;
use her_protocol::GrantAuthority;
use her_protocol::PermissionMode;
use her_protocol::SiteGrant;
use her_tools::Authority;
use std::path::PathBuf;
use url::Url;

use crate::config::AppLevel;
use crate::hooks::Gate;
use crate::hooks::SessionView;

/// The verdict on one authority.
enum Verdict {
    Allow,
    Deny(String),
    Ask {
        summary: String,
        reason: String,
        grants: Vec<SiteGrant>,
    },
}

pub(crate) async fn evaluate(authorities: &[Authority], view: &SessionView) -> Gate {
    let mut summaries = Vec::new();
    let mut reasons = Vec::new();
    let mut grants: Vec<SiteGrant> = Vec::new();
    let mut own_browser = false;
    for authority in authorities {
        let own = matches!(
            authority,
            Authority::Navigate { own: true, .. }
                | Authority::Act { own: true, .. }
                | Authority::Upload { own: true, .. }
                | Authority::Script { own: true, .. }
                | Authority::UserStep { own: true, .. }
        );
        match judge(authority, view).await {
            Verdict::Allow => {}
            Verdict::Deny(reason) => return Gate::Deny(reason),
            Verdict::Ask {
                summary,
                reason,
                grants: proposed,
            } => {
                own_browser |= own;
                summaries.push(summary);
                reasons.push(reason);
                for grant in proposed {
                    if !grants.contains(&grant) {
                        grants.push(grant);
                    }
                }
            }
        }
    }
    if summaries.is_empty() {
        return Gate::Allow;
    }
    Gate::Ask {
        summary: summaries.join("; "),
        reason: Some(reasons.join(" ")),
        proposed_rule: None,
        proposed_mode: None,
        // A grant covers what it names only, so one is offered only when it covers every
        // question: with more than one question none is.
        proposed_grants: if summaries.len() == 1 { grants } else { Vec::new() },
        own_browser,
    }
}

async fn judge(authority: &Authority, view: &SessionView) -> Verdict {
    match authority {
        Authority::Navigate { url, own } => navigate(url, *own, view).await,
        // Reading the page a session already has open asks nothing; getting there did.
        Authority::Observe { .. } => Verdict::Allow,
        Authority::Act { origin, action, own, .. } => act(origin, action, *own, view).await,
        Authority::Upload { origin, paths, own, .. } => upload(origin, paths, *own, view).await,
        Authority::Script { origin, .. } => script(origin, view).await,
        // The call waits for the user, so the question is the point of it: asked in every mode.
        Authority::UserStep { what, .. } => Verdict::Ask {
            summary: what.clone(),
            reason: "Only the user can do this in the page; the call goes on when they are done.".into(),
            grants: Vec::new(),
        },
        Authority::AppObserve { app } => desktop_app(app, None, view),
        Authority::AppAct { app, action } => desktop_app(app, Some(action), view),
    }
}

/// The origin a desktop app's grant is kept under.
pub(crate) fn app_origin(app: &str) -> String {
    format!("{APP_SCHEME}{app}")
}

const APP_SCHEME: &str = "app://";

/// Desktop apps: one question per app and session, which allows reading and acting in it alike
/// (computer-use-design §4), unless `[desktop.apps]` decides. Bypass skips the question, like it does for commands: there
/// a shell could drive the same apps without asking, and a desktop tool that asks only pushes
/// the model to it. A `deny` is the user's own word and holds in every mode.
fn desktop_app(app: &str, action: Option<&str>, view: &SessionView) -> Verdict {
    let level = view.desktop.app(app);
    if level == Some(AppLevel::Deny) {
        return Verdict::Deny(format!("{app} is denied under [desktop.apps]"));
    }
    if view.permission_mode == PermissionMode::BypassPermissions {
        return Verdict::Allow;
    }
    let origin = app_origin(app);
    let acting = action.is_some();
    let granted = view
        .site_grants
        .iter()
        .any(|g| g.origin == origin && opens(g.authority, acting));
    if granted || level == Some(AppLevel::Act) || (level == Some(AppLevel::Read) && !acting) {
        return Verdict::Allow;
    }
    let summary = match action {
        Some(action) => format!("{action} in {app}"),
        None => format!("read the window of {app}"),
    };
    Verdict::Ask {
        summary,
        reason: format!(
            "First time this session uses {app}. To allow it without asking, add \"{app}\" = \"allow\" under \
             [desktop.apps] in ~/.her/config.toml."
        ),
        grants: vec![grant(GrantAuthority::Act, &origin)],
    }
}

/// Scripts: only with `browser.script_enabled`, never on a refused site; asked once per site
/// and session in every mode, and that answer can keep scripts on the site allowed for the
/// rest of the session, never in the configuration (§7.3, §7.6, D-175 revised).
async fn script(origin: &str, view: &SessionView) -> Verdict {
    if !view.browser.script_enabled {
        return Verdict::Deny("running scripts is off (browser.script_enabled)".into());
    }
    if let Ok(url) = Url::parse(origin) {
        if let Some(reason) = view.browser.refusal(&url) {
            return Verdict::Deny(reason);
        }
        if classify_url(&url).await == AddressClass::Metadata {
            return Verdict::Deny(format!("{origin} is a cloud metadata address"));
        }
    }
    if view
        .site_grants
        .iter()
        .any(|g| g.origin == origin && g.authority == GrantAuthority::Script)
    {
        return Verdict::Allow;
    }
    Verdict::Ask {
        summary: format!("run a script on {origin}"),
        reason: format!(
            "First script on {origin} in this session: a script can read and change anything on the page."
        ),
        grants: vec![grant(GrantAuthority::Script, origin)],
    }
}

/// Whether a grant lets the session open pages of its origin, or with `act` act on them. A
/// script grant does neither.
fn opens(authority: GrantAuthority, act: bool) -> bool {
    match authority {
        GrantAuthority::Act => true,
        GrantAuthority::Navigate => !act,
        GrantAuthority::Script => false,
    }
}

fn grant(authority: GrantAuthority, origin: &str) -> SiteGrant {
    SiteGrant {
        authority,
        origin: origin.to_string(),
    }
}

fn granted(view: &SessionView, origin: &str, act: bool) -> bool {
    view.site_grants.iter().any(|g| {
        g.origin == origin && opens(g.authority, act)
    }) || covers(view.browser.site(origin), act)
}

/// "Every site", granted for the session or kept as `"*"` in `browser.sites`: public sites
/// only, so a private address never asks this (D-221).
fn any_site(view: &SessionView, act: bool) -> bool {
    view.site_grants.iter().any(|g| {
        g.origin == ANY_SITE && opens(g.authority, act)
    }) || covers(view.browser.any_site(), act)
}

fn covers(level: Option<SiteLevel>, act: bool) -> bool {
    match level {
        Some(SiteLevel::Act) => true,
        Some(SiteLevel::Navigate) => !act,
        _ => false,
    }
}

/// Whether the mode skips the questions that it may skip. Not in the user's own browser,
/// whose sign-ins are real (§8.4): there only grants and configured sites skip them. `own`
/// says which browser the call works in.
fn bypass(view: &SessionView, own: bool) -> bool {
    view.permission_mode == PermissionMode::BypassPermissions && !own
}

/// Why a public site asks: the first use in the session, and in the user's own browser,
/// that it uses their real sign-ins (why bypass did not skip it).
fn first_use_reason(view: &SessionView, origin: &str, own: bool) -> String {
    let own = match (own, view.permission_mode == PermissionMode::BypassPermissions) {
        (true, true) => " It is in the user's own browser, with their real sign-ins, so bypass-permissions still asks.",
        (true, false) => " It is in the user's own browser, with their real sign-ins.",
        (false, _) => "",
    };
    format!("First use of {origin} in this session.{own} {}", config_hint(origin, "act"))
}

/// One question per site, as one grant: a site allowed is a site both opened and acted on
/// (D-221). The second grant is every public site.
fn site_grants(origin: &str) -> Vec<SiteGrant> {
    vec![grant(GrantAuthority::Act, origin), grant(GrantAuthority::Act, ANY_SITE)]
}

fn config_hint(origin: &str, level: &str) -> String {
    format!("To allow it without asking, add \"{origin}\" = \"{level}\" under [browser.sites] in ~/.her/config.toml.")
}

fn private_reason(origin: &str, level: &str) -> String {
    format!(
        "{origin} is on a private network: every such site needs its own permission, in every permission mode. {}",
        config_hint(origin, level)
    )
}

async fn navigate(raw: &str, own: bool, view: &SessionView) -> Verdict {
    let Ok(url) = Url::parse(raw) else {
        return Verdict::Deny(format!("\"{raw}\" is not a valid URL"));
    };
    let summary = format!("open {url}");
    match url_kind(&url) {
        UrlKind::Refused(reason) => return Verdict::Deny(reason),
        UrlKind::Blank => return Verdict::Allow,
        UrlKind::File(path) => {
            return if inside(&path, view.env.workspace_roots()) {
                Verdict::Allow
            } else {
                Verdict::Ask {
                    summary,
                    reason: "The file is outside the workspace.".into(),
                    grants: Vec::new(),
                }
            };
        }
        UrlKind::Web => {}
    }
    if let Some(reason) = view.browser.refusal(&url) {
        return Verdict::Deny(reason);
    }
    let origin = origin_of(&url);
    match classify_url(&url).await {
        AddressClass::Metadata => return Verdict::Deny(format!("{origin} is a cloud metadata address")),
        AddressClass::Private => {
            return if granted(view, &origin, false) {
                Verdict::Allow
            } else {
                Verdict::Ask {
                    summary,
                    reason: private_reason(&origin, "act"),
                    grants: vec![grant(GrantAuthority::Act, &origin)],
                }
            };
        }
        AddressClass::Loopback => return Verdict::Allow,
        AddressClass::Public => {}
    }
    if granted(view, &origin, false) || any_site(view, false) || bypass(view, own) {
        return Verdict::Allow;
    }
    Verdict::Ask {
        summary,
        reason: first_use_reason(view, &origin, own),
        grants: site_grants(&origin),
    }
}

async fn act(origin: &str, action: &str, own: bool, view: &SessionView) -> Verdict {
    // Blank pages and local files of the workspace: nothing to send anywhere.
    if origin == "null" || origin == "file://" {
        return Verdict::Allow;
    }
    let Ok(url) = Url::parse(origin) else {
        return Verdict::Deny(format!("cannot act on a page of origin {origin}"));
    };
    if let Some(reason) = view.browser.refusal(&url) {
        return Verdict::Deny(reason);
    }
    let summary = format!("{action} on {origin}");
    match classify_url(&url).await {
        AddressClass::Metadata => Verdict::Deny(format!("{origin} is a cloud metadata address")),
        AddressClass::Private if !granted(view, origin, true) => Verdict::Ask {
            summary,
            reason: private_reason(origin, "act"),
            grants: vec![grant(GrantAuthority::Act, origin)],
        },
        AddressClass::Private | AddressClass::Loopback => Verdict::Allow,
        AddressClass::Public if granted(view, origin, true) || any_site(view, true) => Verdict::Allow,
        AddressClass::Public if bypass(view, own) => Verdict::Allow,
        AddressClass::Public => Verdict::Ask {
            summary,
            reason: first_use_reason(view, origin, own),
            grants: site_grants(origin),
        },
    }
}

async fn upload(origin: &str, paths: &[PathBuf], own: bool, view: &SessionView) -> Verdict {
    let roots = view.env.workspace_roots();
    let names: Vec<String> = paths.iter().map(|p| p.display().to_string()).collect();
    let in_workspace = paths.iter().all(|p| inside(p, roots));
    let acting = act(origin, "upload", own, view).await;
    if let Verdict::Deny(reason) = acting {
        return Verdict::Deny(reason);
    }
    if in_workspace && bypass(view, own) && matches!(acting, Verdict::Allow) {
        return Verdict::Allow;
    }
    Verdict::Ask {
        summary: format!("upload {} to {origin}", names.join(", ")),
        reason: if in_workspace {
            "Uploading sends the files to the site.".into()
        } else {
            "Uploading sends the files to the site, and some are outside the workspace.".into()
        },
        grants: Vec::new(),
    }
}

/// Writes a grant into `[browser.sites]` of the user's configuration (D-197): never over a
/// `deny`, and never down from `act` to `navigate`; every site is kept as `"*"` (D-221).
/// Comments and the rest of the file stay as they are.
/// A desktop app's grant goes into `[desktop.apps]` instead, as `allow`.
pub(crate) fn remember(file: &std::path::Path, grant: &SiteGrant) -> Result<(), String> {
    // Scripts are allowed for one session at a time, never kept (§7.6).
    if grant.authority == GrantAuthority::Script {
        return Ok(());
    }
    let text = match std::fs::read_to_string(file) {
        Ok(text) => text,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => String::new(),
        Err(err) => return Err(err.to_string()),
    };
    let mut doc: toml_edit::DocumentMut = text.parse().map_err(|e: toml_edit::TomlError| e.to_string())?;
    let (section, table, key, weaker) = match grant.origin.strip_prefix(APP_SCHEME) {
        Some(app) => ("desktop", "apps", app.to_string(), "allow"),
        None => ("browser", "sites", grant.origin.clone(), "navigate"),
    };
    let parent = doc
        .entry(section)
        .or_insert_with(|| toml_edit::Item::Table(toml_edit::Table::new()))
        .as_table_mut()
        .ok_or(format!("{section} is not a table"))?;
    let entries = parent
        .entry(table)
        .or_insert_with(|| toml_edit::Item::Table(toml_edit::Table::new()))
        .as_table_like_mut()
        .ok_or(format!("{section}.{table} is not a table"))?;
    let level = match grant.authority {
        GrantAuthority::Navigate => weaker,
        GrantAuthority::Act if section == "desktop" => "allow",
        GrantAuthority::Act => "act",
        GrantAuthority::Script => unreachable!("scripts are never kept"),
    };
    let current = entries.get(&key).and_then(|v| v.as_str()).map(str::to_string);
    match current.as_deref() {
        Some("deny") | Some("act") => return Ok(()),
        Some(kept) if kept == weaker && level == weaker => return Ok(()),
        _ => {}
    }
    entries.insert(&key, toml_edit::value(level));
    if let Some(dir) = file.parent() {
        std::fs::create_dir_all(dir).map_err(|e| e.to_string())?;
    }
    crate::config_edit::replace_file(file, doc.to_string().as_bytes()).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_browser::policy::SitePolicy;
    use her_tools::LocalEnvironment;
    use her_tools::PageTarget;
    use std::sync::Arc;

    fn view(mode: PermissionMode, grants: Vec<SiteGrant>, policy: SitePolicy, root: &std::path::Path) -> SessionView {
        SessionView {
            permission_mode: mode,
            config_rules: Vec::new(),
            session_rules: Vec::new(),
            trusted: true,
            env: Arc::new(LocalEnvironment::new(root.to_path_buf())),
            data_dir: root.join(".her-data"),
            own_folder: None,
            skill_roots: Arc::from([]),
            site_grants: grants,
            browser: Arc::new(policy),
            desktop: Default::default(),
        }
    }

    fn page() -> PageTarget {
        PageTarget {
            tab: "1".into(),
            frame: "F".into(),
            loader: "L".into(),
        }
    }

    fn nav(url: &str) -> Authority {
        Authority::Navigate { url: url.into(), own: false }
    }

    fn act_on(origin: &str) -> Authority {
        Authority::Act {
            origin: origin.into(),
            target: page(),
            action: "click button \"Go\"".into(),
            own: false,
        }
    }

    /// The same, in the user's own browser.
    fn own(authority: Authority) -> Authority {
        match authority {
            Authority::Navigate { url, .. } => Authority::Navigate { url, own: true },
            Authority::Act { origin, target, action, .. } => Authority::Act { origin, target, action, own: true },
            other => other,
        }
    }

    fn asks(gate: &Gate) -> Option<Vec<SiteGrant>> {
        match gate {
            Gate::Ask { proposed_grants, .. } => Some(proposed_grants.clone()),
            _ => None,
        }
    }

    #[tokio::test]
    async fn manual_mode_asks_once_per_origin_and_offers_grants() {
        let dir = tempfile::tempdir().unwrap();
        let manual = view(PermissionMode::Manual, vec![], SitePolicy::default(), dir.path());
        let gate = evaluate(&[nav("https://93.184.215.14/docs")], &manual).await;
        let grants = asks(&gate).expect("asks");
        assert_eq!(grants, vec![grant(GrantAuthority::Act, "https://93.184.215.14"), grant(GrantAuthority::Act, "*")]);
        let Gate::Ask { reason, summary, .. } = &gate else { unreachable!() };
        assert_eq!(summary, "open https://93.184.215.14/docs");
        assert!(reason.as_deref().unwrap().contains("[browser.sites]"));
        // Reading asks nothing; local and loopback pages neither.
        assert_eq!(evaluate(&[Authority::Observe { origin: "https://93.184.215.14".into() }], &manual).await, Gate::Allow);
        assert_eq!(evaluate(&[nav("http://localhost:5173/")], &manual).await, Gate::Allow);
        assert_eq!(evaluate(&[act_on("http://127.0.0.1:3000")], &manual).await, Gate::Allow);
        assert_eq!(evaluate(&[nav("about:blank")], &manual).await, Gate::Allow);
        // The first action on an origin asks the same question.
        let gate = evaluate(&[act_on("https://93.184.215.14")], &manual).await;
        assert_eq!(asks(&gate).unwrap(), grants);

        // An act grant covers both; "any site" covers what its level names.
        let granted = view(PermissionMode::Manual, vec![grant(GrantAuthority::Act, "https://93.184.215.14")], SitePolicy::default(), dir.path());
        assert_eq!(evaluate(&[nav("https://93.184.215.14/x"), act_on("https://93.184.215.14")], &granted).await, Gate::Allow);
        let browsing = view(PermissionMode::Manual, vec![grant(GrantAuthority::Navigate, "*")], SitePolicy::default(), dir.path());
        assert_eq!(evaluate(&[nav("https://93.184.215.14/x")], &browsing).await, Gate::Allow);
        assert!(asks(&evaluate(&[act_on("https://93.184.215.14")], &browsing).await).is_some());
        let every = view(PermissionMode::Manual, vec![grant(GrantAuthority::Act, "*")], SitePolicy::default(), dir.path());
        assert_eq!(evaluate(&[nav("https://93.184.215.14/x"), act_on("https://93.184.215.14")], &every).await, Gate::Allow);
        assert!(asks(&evaluate(&[nav("http://192.168.0.7/")], &every).await).is_some(), "never a private site");
    }

    /// A page handed to the user waits for them, so the question is the point of it: asked in
    /// every mode and whatever is granted, with nothing to keep (browser-design §8.4.7).
    #[tokio::test]
    async fn a_step_left_to_the_user_asks_in_every_mode() {
        let dir = tempfile::tempdir().unwrap();
        for mode in [PermissionMode::Manual, PermissionMode::AcceptEdits, PermissionMode::BypassPermissions] {
            let granted = view(mode, vec![grant(GrantAuthority::Act, "*")], SitePolicy::default(), dir.path());
            let gate = evaluate(&[Authority::UserStep { what: "sign in".into(), own: true }], &granted).await;
            let Gate::Ask { summary, proposed_grants, own_browser, .. } = gate else { panic!("{mode:?}: {gate:?}") };
            assert_eq!(summary, "sign in");
            assert!(proposed_grants.is_empty());
            assert!(own_browser);
        }
    }

    /// `"*"` in `browser.sites`: every public site, without a question, in every mode and in
    /// the user's own browser; a site's own `deny` and private addresses still hold (D-221).
    #[tokio::test]
    async fn every_site_kept_in_the_configuration() {
        let dir = tempfile::tempdir().unwrap();
        let mut policy = SitePolicy::default();
        policy.sites.insert("*".into(), SiteLevel::Act);
        policy.sites.insert("https://93.184.215.15".into(), SiteLevel::Deny);
        let manual = view(PermissionMode::Manual, vec![], policy.clone(), dir.path());
        assert_eq!(evaluate(&[nav("https://93.184.215.14/x"), act_on("https://93.184.215.14")], &manual).await, Gate::Allow);
        assert_eq!(
            evaluate(&[own(nav("https://93.184.215.14/x")), own(act_on("https://93.184.215.14"))], &manual).await,
            Gate::Allow,
            "in the user's own browser too"
        );
        assert!(matches!(evaluate(&[nav("https://93.184.215.15/")], &manual).await, Gate::Deny(_)), "a deny holds");
        assert!(asks(&evaluate(&[nav("http://192.168.0.7/")], &manual).await).is_some(), "private still asks");
        policy.sites.insert("*".into(), SiteLevel::Navigate);
        let browsing = view(PermissionMode::Manual, vec![], policy, dir.path());
        assert_eq!(evaluate(&[nav("https://93.184.215.14/x")], &browsing).await, Gate::Allow);
        assert!(asks(&evaluate(&[act_on("https://93.184.215.14")], &browsing).await).is_some(), "navigate does not act");
    }

    #[tokio::test]
    async fn private_and_metadata_addresses_and_policy_hold_in_every_mode() {
        let dir = tempfile::tempdir().unwrap();
        let mut policy = SitePolicy::default();
        policy.denied_domains.push("evil.test".into());
        policy.sites.insert("http://192.168.1.10:8080".into(), SiteLevel::Act);
        let bypass = view(PermissionMode::BypassPermissions, vec![], policy, dir.path());
        assert_eq!(evaluate(&[nav("https://93.184.215.14/")], &bypass).await, Gate::Allow);
        let private = evaluate(&[nav("http://192.168.0.7/")], &bypass).await;
        assert_eq!(asks(&private).unwrap(), vec![grant(GrantAuthority::Act, "http://192.168.0.7")], "bypass still asks, never for every site");
        assert_eq!(evaluate(&[nav("http://192.168.1.10:8080/admin")], &bypass).await, Gate::Allow, "configured");
        assert!(matches!(evaluate(&[nav("http://169.254.169.254/latest")], &bypass).await, Gate::Deny(_)));
        assert!(matches!(evaluate(&[nav("https://www.evil.test/")], &bypass).await, Gate::Deny(_)));
        assert!(matches!(evaluate(&[nav("javascript:alert(1)")], &bypass).await, Gate::Deny(_)));
        assert!(matches!(evaluate(&[act_on("https://evil.test")], &bypass).await, Gate::Deny(_)));
    }

    /// In the user's own browser the logins are real: bypass skips nothing, while grants,
    /// configured sites and loopback still do (§8.4). The same call in the built-in browser
    /// asks nothing in bypass: which browser is the call's, not the session's (D-225).
    #[tokio::test]
    async fn the_users_own_browser_asks_even_in_bypass() {
        let dir = tempfile::tempdir().unwrap();
        let mut policy = SitePolicy::default();
        policy.sites.insert("https://93.184.215.15".into(), SiteLevel::Act);
        let bypass = view(PermissionMode::BypassPermissions, vec![], policy.clone(), dir.path());
        let gate = evaluate(&[own(nav("https://93.184.215.14/"))], &bypass).await;
        let Gate::Ask { reason, own_browser, .. } = &gate else { panic!("navigation asks") };
        assert!(reason.as_deref().unwrap().contains("user's own browser"), "says why bypass asks");
        assert!(own_browser, "the question says which browser");
        assert_eq!(evaluate(&[nav("https://93.184.215.14/")], &bypass).await, Gate::Allow, "the built-in browser does not ask");
        assert_eq!(
            asks(&evaluate(&[own(act_on("https://93.184.215.14"))], &bypass).await).unwrap(),
            vec![grant(GrantAuthority::Act, "https://93.184.215.14"), grant(GrantAuthority::Act, "*")],
            "acting asks, offering the site and every site"
        );
        assert_eq!(evaluate(&[own(act_on("https://93.184.215.15"))], &bypass).await, Gate::Allow, "configured");
        assert_eq!(evaluate(&[own(act_on("http://localhost:5173"))], &bypass).await, Gate::Allow, "loopback");
        let granted = view(PermissionMode::BypassPermissions, vec![grant(GrantAuthority::Act, "https://93.184.215.14")], policy, dir.path());
        assert_eq!(evaluate(&[own(act_on("https://93.184.215.14"))], &granted).await, Gate::Allow, "granted");
        let manual = view(PermissionMode::Manual, vec![], SitePolicy::default(), dir.path());
        let Gate::Ask { own_browser, .. } = evaluate(&[nav("https://93.184.215.14/")], &manual).await else { panic!("asks") };
        assert!(!own_browser, "a question about the built-in browser says so too");
    }

    #[test]
    fn remembered_grants_go_into_the_user_configuration() {
        let dir = tempfile::tempdir().unwrap();
        let file = dir.path().join("config.toml");
        std::fs::write(&file, "# mine\nmodel = \"x\"\n\n[browser]\nenabled = true\n[browser.sites]\n\"https://bank.test\" = \"deny\"\n").unwrap();
        remember(&file, &grant(GrantAuthority::Navigate, "https://a.test")).unwrap();
        remember(&file, &grant(GrantAuthority::Act, "https://a.test")).unwrap();
        remember(&file, &grant(GrantAuthority::Navigate, "https://a.test")).unwrap();
        remember(&file, &grant(GrantAuthority::Act, "https://bank.test")).unwrap();
        remember(&file, &grant(GrantAuthority::Act, "*")).unwrap();
        let text = std::fs::read_to_string(&file).unwrap();
        assert!(text.starts_with("# mine\nmodel = \"x\""), "{text}");
        let parsed: toml::Table = text.parse().unwrap();
        let sites = &parsed["browser"]["sites"];
        assert_eq!(sites["https://a.test"].as_str(), Some("act"), "never down from act");
        assert_eq!(sites["https://bank.test"].as_str(), Some("deny"), "never over a deny");
        assert_eq!(sites["*"].as_str(), Some("act"), "every site is kept too");
        // A file that does not exist yet.
        let fresh = dir.path().join("new/config.toml");
        remember(&fresh, &grant(GrantAuthority::Act, "http://192.168.1.10:8080")).unwrap();
        assert!(std::fs::read_to_string(&fresh).unwrap().contains("[browser.sites]\n\"http://192.168.1.10:8080\" = \"act\""));
    }

    #[tokio::test]
    async fn scripts_ask_once_per_site_and_only_when_enabled() {
        let dir = tempfile::tempdir().unwrap();
        let origin = "http://localhost:3000";
        let script = Authority::Script {
            origin: origin.into(),
            target: page(),
            own: false,
        };
        let off = view(PermissionMode::BypassPermissions, vec![], SitePolicy::default(), dir.path());
        assert!(matches!(evaluate(std::slice::from_ref(&script), &off).await, Gate::Deny(r) if r.contains("script_enabled")));
        let on = SitePolicy {
            script_enabled: true,
            ..SitePolicy::default()
        };
        let mode = |mode, grants| view(mode, grants, on.clone(), dir.path());
        // Acting on the site does not cover its scripts: the first one asks, in every mode,
        // and offers this site's scripts for the session.
        for m in [PermissionMode::Manual, PermissionMode::BypassPermissions] {
            let acting = mode(m, vec![grant(GrantAuthority::Act, origin), grant(GrantAuthority::Act, ANY_SITE)]);
            assert_eq!(
                asks(&evaluate(std::slice::from_ref(&script), &acting).await),
                Some(vec![grant(GrantAuthority::Script, origin)]),
                "{m:?}"
            );
            let scripts = mode(m, vec![grant(GrantAuthority::Script, origin)]);
            assert_eq!(evaluate(std::slice::from_ref(&script), &scripts).await, Gate::Allow, "{m:?}: asked once");
        }
        // Another site's grant does not carry over, and a script grant opens nothing.
        let other = mode(PermissionMode::Manual, vec![grant(GrantAuthority::Script, "http://localhost:4000")]);
        assert!(asks(&evaluate(std::slice::from_ref(&script), &other).await).is_some());
        let only_scripts = mode(PermissionMode::Manual, vec![grant(GrantAuthority::Script, "https://93.184.215.14")]);
        assert!(asks(&evaluate(&[nav("https://93.184.215.14/")], &only_scripts).await).is_some());
        assert!(asks(&evaluate(&[act_on("https://93.184.215.14")], &only_scripts).await).is_some());
        // Never kept in the configuration.
        let file = dir.path().join("config.toml");
        remember(&file, &grant(GrantAuthority::Script, origin)).unwrap();
        assert!(!file.exists());
    }

    #[tokio::test]
    async fn files_and_uploads() {
        let dir = tempfile::tempdir().unwrap();
        let root = std::fs::canonicalize(dir.path()).unwrap();
        std::fs::write(root.join("index.html"), "<p>hi</p>").unwrap();
        let manual = view(PermissionMode::Manual, vec![], SitePolicy::default(), &root);
        let inside_url = Url::from_file_path(root.join("index.html")).unwrap().to_string();
        assert_eq!(evaluate(&[nav(&inside_url)], &manual).await, Gate::Allow);
        let outside = evaluate(&[nav("file:///etc/hosts")], &manual).await;
        assert_eq!(asks(&outside).unwrap(), vec![], "no grant for files outside");
        let upload = Authority::Upload {
            origin: "http://localhost:3000".into(),
            paths: vec![root.join("index.html")],
            target: page(),
            own: false,
        };
        assert!(asks(&evaluate(std::slice::from_ref(&upload), &manual).await).is_some(), "uploads ask in manual mode");
        let bypass = view(PermissionMode::BypassPermissions, vec![], SitePolicy::default(), &root);
        assert_eq!(evaluate(std::slice::from_ref(&upload), &bypass).await, Gate::Allow);
        let outside_upload = Authority::Upload {
            origin: "http://localhost:3000".into(),
            paths: vec![PathBuf::from("/etc/hosts")],
            target: page(),
            own: false,
        };
        assert!(asks(&evaluate(&[outside_upload], &bypass).await).is_some(), "files outside the workspace always ask");
    }

    #[tokio::test]
    async fn desktop_apps_once_per_session() {
        let dir = tempfile::tempdir().unwrap();
        let read = Authority::AppObserve { app: "com.apple.TextEdit".into() };
        let act = Authority::AppAct { app: "com.apple.TextEdit".into(), action: "type @s1:e2".into() };
        let manual = view(PermissionMode::Manual, vec![], SitePolicy::default(), dir.path());
        let app_grant = grant(GrantAuthority::Act, "app://com.apple.TextEdit");
        // One question per app, whether the call reads or acts.
        assert_eq!(asks(&evaluate(std::slice::from_ref(&read), &manual).await), Some(vec![app_grant.clone()]));
        assert_eq!(asks(&evaluate(std::slice::from_ref(&act), &manual).await), Some(vec![app_grant.clone()]));

        let granted = view(PermissionMode::Manual, vec![app_grant], SitePolicy::default(), dir.path());
        assert_eq!(evaluate(&[read.clone(), act.clone()], &granted).await, Gate::Allow, "allowed once, it reads and acts");
        let other = Authority::AppObserve { app: "com.tencent.xinWeChat".into() };
        assert!(asks(&evaluate(&[other], &granted).await).is_some(), "a grant names one app");

        let mut bypass = view(PermissionMode::BypassPermissions, vec![], SitePolicy::default(), dir.path());
        assert_eq!(evaluate(&[read.clone(), act.clone()], &bypass).await, Gate::Allow, "bypass does not ask");
        let toml = "[desktop.apps]\n\"com.apple.TextEdit\" = \"deny\"\n";
        bypass.desktop = crate::config::Config::from_file(toml::from_str(toml).unwrap()).unwrap().desktop;
        assert!(matches!(evaluate(std::slice::from_ref(&read), &bypass).await, Gate::Deny(_)), "a deny holds in bypass");

        let mut configured = view(PermissionMode::Manual, vec![], SitePolicy::default(), dir.path());
        let toml = "[desktop.apps]\n\"com.apple.textedit\" = \"allow\"\n\"com.tencent.xinWeChat\" = \"deny\"\n\"com.apple.Notes\" = \"read\"\n";
        let file: crate::config::ConfigFile = toml::from_str(toml).unwrap();
        configured.desktop = crate::config::Config::from_file(file).unwrap().desktop;
        assert_eq!(evaluate(&[read.clone(), act.clone()], &configured).await, Gate::Allow, "allowed in config");
        let notes = Authority::AppAct { app: "com.apple.Notes".into(), action: "click @s1:e2".into() };
        assert!(asks(&evaluate(&[notes], &configured).await).is_some(), "an old read still asks before acting");
        let wechat = Authority::AppObserve { app: "com.tencent.xinWeChat".into() };
        assert!(matches!(evaluate(&[wechat], &configured).await, Gate::Deny(_)));
    }

    #[test]
    fn app_grants_are_kept_under_desktop_apps() {
        let dir = tempfile::tempdir().unwrap();
        let file = dir.path().join("config.toml");
        std::fs::write(&file, "[desktop.apps]\n\"com.tencent.xinWeChat\" = \"deny\"\n").unwrap();
        remember(&file, &grant(GrantAuthority::Act, "app://com.apple.TextEdit")).unwrap();
        remember(&file, &grant(GrantAuthority::Act, "app://com.apple.TextEdit")).unwrap();
        remember(&file, &grant(GrantAuthority::Act, "app://com.tencent.xinWeChat")).unwrap();
        let kept: toml::Table = toml::from_str(&std::fs::read_to_string(&file).unwrap()).unwrap();
        let apps = kept["desktop"]["apps"].as_table().unwrap();
        assert_eq!(apps["com.apple.TextEdit"].as_str(), Some("allow"));
        assert_eq!(apps["com.tencent.xinWeChat"].as_str(), Some("deny"), "never over deny");
        assert!(kept.get("browser").is_none());
    }
}
