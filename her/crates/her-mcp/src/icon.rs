//! A server's logo (mcp-design §13.9): the icons the server declares in its handshake first,
//! then the icon of its website, found the way browsers find one. Icons are untrusted: only
//! https or `data:` addresses, no credentials or cookies, no redirects off the site, a size
//! limit, the type read from the bytes, and SVG without scripts. What is found is kept in
//! `~/.her/mcp/icons`, one file per service, so showing a list never fetches anything.
//! The browsers' sites get theirs the same way ([`refresh_site`]), except that a page's icon
//! may be kept on another host, a CDN, as browsers allow.

use std::collections::HashSet;
use parking_lot::Mutex;
use std::sync::LazyLock;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::Ordering;
use std::path::Path;
use std::path::PathBuf;
use std::time::Duration;
use std::time::SystemTime;
use std::time::UNIX_EPOCH;

use base64::Engine;
use reqwest::Url;
use serde::Deserialize;
use serde::Deserializer;
use serde::Serialize;
use serde_json::Value;

use crate::types::Implementation;

/// Larger icons are refused.
pub const MAX_BYTES: usize = 256 * 1024;
const MAX_PAGE_BYTES: usize = 512 * 1024;
const MAX_MANIFEST_BYTES: usize = 64 * 1024;
const MAX_DIMENSION: u32 = 2048;
const REQUEST_TIMEOUT: Duration = Duration::from_secs(5);
/// A whole search, all its requests.
const LOOKUP_TIMEOUT: Duration = Duration::from_secs(12);
/// At most this many candidate images are fetched for one server.
const MAX_ATTEMPTS: usize = 6;
/// At most this many services' results are kept.
const MAX_FILES: usize = 200;
/// A stored result, an icon or none found, is looked for again after this long.
pub const REFRESH_AFTER: Duration = Duration::from_secs(7 * 24 * 3600);

/// One entry of `icons` in the handshake.
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct IconSpec {
    pub src: String,
    #[serde(default)]
    pub mime_type: Option<String>,
    #[serde(default)]
    pub sizes: Vec<String>,
    /// "light" or "dark": the background the icon is drawn for.
    #[serde(default)]
    pub theme: Option<String>,
}

/// `icons` as a server sends it: entries that do not parse are dropped, not the handshake.
pub fn lenient_icons<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Vec<IconSpec>, D::Error> {
    let value = Option::<Value>::deserialize(deserializer)?;
    Ok(match value {
        Some(Value::Array(items)) => items.into_iter().filter_map(|v| serde_json::from_value(v).ok()).collect(),
        _ => Vec::new(),
    })
}

/// A checked image.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Icon {
    pub bytes: Vec<u8>,
    pub mime: &'static str,
    /// Where it came from: its address, or "data:" for an inline one.
    pub source: String,
    /// Declared by the server, rather than found on its website.
    pub declared: bool,
}

impl Icon {
    pub fn data_uri(&self) -> String {
        format!("data:{};base64,{}", self.mime, base64::engine::general_purpose::STANDARD.encode(&self.bytes))
    }
}

/// The image's type from its first bytes, if it is one her shows.
pub fn sniff(bytes: &[u8]) -> Option<&'static str> {
    if bytes.starts_with(b"\x89PNG\r\n\x1a\n") {
        Some("image/png")
    } else if bytes.starts_with(&[0xff, 0xd8, 0xff]) {
        Some("image/jpeg")
    } else if bytes.len() > 12 && &bytes[..4] == b"RIFF" && &bytes[8..12] == b"WEBP" {
        Some("image/webp")
    } else if bytes.starts_with(&[0, 0, 1, 0]) {
        Some("image/x-icon")
    } else {
        // The beginning is enough to tell; a character cut at its end does not matter.
        let head = String::from_utf8_lossy(&bytes[..bytes.len().min(4096)]);
        let lower = head.trim_start_matches('\u{feff}').trim_start().to_ascii_lowercase();
        (lower.starts_with("<svg") || ((lower.starts_with("<?xml") || lower.starts_with("<!--") || lower.starts_with("<!doctype svg")) && lower.contains("<svg"))).then_some("image/svg+xml")
    }
}

/// The image's type when it is safe to show: small enough, a known type by its bytes, not too
/// large in pixels, and an SVG with nothing in it that runs or loads.
pub fn check(bytes: &[u8]) -> Result<&'static str, String> {
    if bytes.is_empty() {
        return Err("empty".into());
    }
    if bytes.len() > MAX_BYTES {
        return Err(format!("larger than {} KB", MAX_BYTES / 1024));
    }
    let mime = sniff(bytes).ok_or("not a PNG, JPEG, WebP, ICO or SVG image")?;
    match mime {
        "image/png" if bytes.len() >= 24 => {
            let width = u32::from_be_bytes(bytes[16..20].try_into().unwrap_or_default());
            let height = u32::from_be_bytes(bytes[20..24].try_into().unwrap_or_default());
            if width == 0 || height == 0 || width > MAX_DIMENSION || height > MAX_DIMENSION {
                return Err(format!("{width}×{height} pixels"));
            }
        }
        "image/svg+xml" => {
            let text = String::from_utf8_lossy(bytes).to_ascii_lowercase();
            let compact: String = text.chars().filter(|c| !c.is_whitespace()).collect();
            let banned = ["<script", "<foreignobject", "<iframe", "<embed", "<object", "javascript:", "<!entity", "@import"];
            if let Some(found) = banned.iter().find(|b| compact.contains(&b.replace(' ', ""))) {
                return Err(format!("an SVG with {found}"));
            }
            if has_event_handler(&text) {
                return Err("an SVG with an event handler".into());
            }
            // Nothing loaded from elsewhere: references stay inside the image.
            for attr in ["href=", "src=", "url("] {
                let mut rest = compact.as_str();
                while let Some(at) = rest.find(attr) {
                    rest = &rest[at + attr.len()..];
                    let value = rest.trim_start_matches(['"', '\'']);
                    if !(value.starts_with('#') || value.starts_with("data:image/png") || value.starts_with("data:image/jpeg")) {
                        return Err("an SVG that refers to another file".into());
                    }
                }
            }
        }
        _ => {}
    }
    Ok(mime)
}

/// An `on…=` attribute, as in `onload="…"`.
fn has_event_handler(text: &str) -> bool {
    let bytes = text.as_bytes();
    let mut i = 0;
    while let Some(at) = text[i..].find("on") {
        let start = i + at;
        let before = start.checked_sub(1).map(|j| bytes[j]);
        let mut end = start + 2;
        while end < bytes.len() && bytes[end].is_ascii_lowercase() {
            end += 1;
        }
        let mut after = end;
        while after < bytes.len() && bytes[after].is_ascii_whitespace() {
            after += 1;
        }
        if end > start + 2 && before.is_some_and(|b| b.is_ascii_whitespace()) && bytes.get(after) == Some(&b'=') {
            return true;
        }
        i = start + 2;
    }
    false
}

/// First labels that only say which part of a company's site a host is, so the company's own
/// site may stand for it: `mcp.hermesh.io` → `hermesh.io`. Any other host keeps to itself:
/// the parent of `my-tools.vercel.app` is a hosting platform, not the service's brand.
const SERVICE_LABELS: [&str; 8] = ["mcp", "api", "www", "server", "servers", "gateway", "app", "sse"];

/// The host of an https address on the internet: not an IP address or `localhost`.
fn public_host(url: &Url) -> Option<String> {
    let host = url.host_str()?.to_ascii_lowercase();
    (url.scheme() == "https" && host.parse::<std::net::IpAddr>().is_err() && host != "localhost" && !host.starts_with('[')).then_some(host)
}

/// The sites whose icons may stand for a server: its own host and, for a service host, the
/// company site it belongs to with or without `www.`; and the website it names. A remote
/// server's website counts only on the server's own site, since icons come from where the
/// server is (MCP); a local server has no address, so its website is all there is.
fn sites(server_url: Option<&Url>, website: Option<&Url>) -> Vec<String> {
    let server = server_url.and_then(public_host);
    let website = website.and_then(public_host).filter(|w| server.as_ref().is_none_or(|s| registrable(w) == registrable(s)));
    let mut hosts: Vec<String> = Vec::new();
    for host in [website, server].into_iter().flatten() {
        let site = registrable(&host);
        let first = host.split('.').next().unwrap_or_default();
        let mut add = vec![host.clone()];
        if host == site || SERVICE_LABELS.contains(&first) {
            add.extend([site.clone(), format!("www.{site}")]);
        }
        for h in add {
            if !hosts.contains(&h) {
                hosts.push(h);
            }
        }
    }
    hosts
}

/// The last two labels of a host, or three under a two-letter country code with a generic
/// second level (`example.co.uk`). Enough to find a company's site from its API host.
fn registrable(host: &str) -> String {
    let labels: Vec<&str> = host.split('.').collect();
    let keep = if labels.len() >= 3 && labels[labels.len() - 1].len() == 2 && ["co", "com", "org", "net", "gov", "edu", "ac"].contains(&labels[labels.len() - 2]) { 3 } else { 2 };
    labels[labels.len().saturating_sub(keep)..].join(".")
}

/// Where an icon may come from: a server's own sites (MCP requires it), or, for a site the
/// browsers open, any https host on the internet, as a browser takes a page's icon from its CDN.
#[derive(Clone)]
enum Scope {
    Sites(HashSet<String>),
    Internet,
}

impl Scope {
    fn allows(&self, url: &Url) -> bool {
        match self {
            Scope::Sites(hosts) => url.scheme() == "https" && url.host_str().is_some_and(|h| hosts.contains(&h.to_ascii_lowercase())),
            Scope::Internet => public_host(url).is_some(),
        }
    }
}

/// A client with no cookies and no credentials, that follows redirects only within `scope`.
fn client(scope: Scope) -> Option<reqwest::Client> {
    let policy = reqwest::redirect::Policy::custom(move |attempt| {
        let allowed = attempt.previous().len() < 3 && scope.allows(attempt.url());
        if allowed { attempt.follow() } else { attempt.stop() }
    });
    reqwest::Client::builder()
        .use_preconfigured_tls(her_llm::tls_client_config().ok()?)
        .redirect(policy)
        .timeout(REQUEST_TIMEOUT)
        .user_agent(concat!("her/", env!("CARGO_PKG_VERSION")))
        .build()
        .ok()
}

/// One search: its client, where icons may come from, the size the icon is shown at, and
/// whether any site answered at all (none did: the network is down, so nothing is learnt).
struct Lookup {
    http: reqwest::Client,
    scope: Scope,
    target: u32,
    answered: AtomicBool,
}

/// A connector's tile: the size icons are picked for (mcp-design §13.9).
const TILE: u32 = 180;
/// A site's row in a list, at most 18 points on a 3x screen: 64 px, the common size nearest 54.
const SITE_ROW: u32 = 64;

/// No site answered: nothing can be said about its icon.
#[derive(Debug)]
pub struct Unreachable;

impl Lookup {
    fn new(scope: Scope, target: u32) -> Option<Self> {
        Some(Lookup { http: client(scope.clone())?, scope, target, answered: AtomicBool::new(false) })
    }

    /// The body, up to `limit` bytes, and the address it came from after redirects. A longer
    /// body is refused, unless `head` says its beginning will do: a page, whose icons are in
    /// `<head>`, which is read no further than its end.
    async fn get(&self, url: &Url, limit: usize, head: bool) -> Option<(Vec<u8>, Url)> {
        let mut response = self.http.get(url.clone()).send().await.ok()?;
        // A server error may pass; any other answer says what the site has.
        if !response.status().is_server_error() {
            self.answered.store(true, Ordering::Relaxed);
        }
        if !response.status().is_success() {
            return None;
        }
        let final_url = response.url().clone();
        let mut body = Vec::new();
        while let Some(chunk) = response.chunk().await.ok()? {
            let seen = body.len().saturating_sub(HEAD_END.len() - 1);
            body.extend_from_slice(&chunk);
            if head && body[seen..].windows(HEAD_END.len()).any(|w| w.eq_ignore_ascii_case(HEAD_END)) {
                break;
            }
            if body.len() > limit {
                if !head {
                    return None;
                }
                body.truncate(limit);
                break;
            }
        }
        Some((body, final_url))
    }
}

const HEAD_END: &[u8] = b"</head>";

/// Fetches one address and checks the image; `data:` addresses are decoded instead.
async fn fetch(look: &Lookup, src: &str, declared: bool) -> Option<Icon> {
    if let Some(rest) = src.strip_prefix("data:") {
        let (head, data) = rest.split_once(',')?;
        let bytes = if head.ends_with(";base64") {
            base64::engine::general_purpose::STANDARD.decode(data.trim()).ok()?
        } else {
            data.as_bytes().to_vec()
        };
        let mime = check(&bytes).ok()?;
        return Some(Icon { bytes, mime, source: "data:".into(), declared });
    }
    let url = Url::parse(src).ok()?;
    if !look.scope.allows(&url) {
        return None;
    }
    let (bytes, _) = look.get(&url, MAX_BYTES, false).await?;
    match check(&bytes) {
        Ok(mime) => Some(Icon { bytes, mime, source: url.to_string(), declared }),
        Err(why) => {
            tracing::debug!("icon {url} refused: {why}");
            None
        }
    }
}

/// One image a page offers, and how well it would fit a tile.
#[derive(Debug, PartialEq)]
struct Candidate {
    url: Url,
    score: i64,
}

/// How well an image of this size fits where it is shown at `target` px: about that size is
/// best, a larger one needlessly heavy; scalable is nearly as good; ones under half of it (or
/// under 64 px) come last. An Apple touch icon with no size is 180 px.
fn score(size: Option<u32>, svg: bool, apple: bool, target: u32) -> i64 {
    let target = i64::from(target);
    match size.map(i64::from) {
        Some(s) if s >= (target / 2).min(64) => 1000 - (s - target).abs(),
        Some(s) => s,
        None if apple => 990 - (180 - target).abs(),
        None if svg => 900,
        None => 100,
    }
}

/// The largest square of a `sizes` value such as "32x32 180x180", or none for "any".
fn largest(sizes: &str) -> Option<u32> {
    sizes.split_whitespace().filter_map(|s| s.split_once(['x', 'X'])).filter_map(|(w, _)| w.parse().ok()).max()
}

/// The `<link>` tags of a page: each as its attributes, names in lower case.
fn links(html: &str) -> Vec<Vec<(String, String)>> {
    let lower = html.to_ascii_lowercase();
    let mut out = Vec::new();
    let mut at = 0;
    while let Some(found) = lower[at..].find("<link") {
        let start = at + found + 5;
        let Some(len) = lower[start..].find('>') else { break };
        out.push(attributes(&html[start..start + len]));
        at = start + len;
    }
    out
}

fn attributes(tag: &str) -> Vec<(String, String)> {
    let mut out = Vec::new();
    let chars: Vec<char> = tag.chars().collect();
    let mut i = 0;
    while i < chars.len() {
        while i < chars.len() && (chars[i].is_whitespace() || chars[i] == '/') {
            i += 1;
        }
        let name_start = i;
        while i < chars.len() && !chars[i].is_whitespace() && chars[i] != '=' && chars[i] != '/' {
            i += 1;
        }
        let name: String = chars[name_start..i].iter().collect::<String>().to_ascii_lowercase();
        while i < chars.len() && chars[i].is_whitespace() {
            i += 1;
        }
        let mut value = String::new();
        if i < chars.len() && chars[i] == '=' {
            i += 1;
            while i < chars.len() && chars[i].is_whitespace() {
                i += 1;
            }
            if i < chars.len() && (chars[i] == '"' || chars[i] == '\'') {
                let quote = chars[i];
                i += 1;
                let start = i;
                while i < chars.len() && chars[i] != quote {
                    i += 1;
                }
                value = chars[start..i].iter().collect();
                i += 1;
            } else {
                let start = i;
                while i < chars.len() && !chars[i].is_whitespace() {
                    i += 1;
                }
                value = chars[start..i].iter().collect();
            }
        }
        if !name.is_empty() {
            out.push((name, value));
        }
        if i == name_start {
            i += 1;
        }
    }
    out
}

/// The icons a site's home page and its web app manifest offer, best first, then
/// `/favicon.ico`.
async fn site_candidates(look: &Lookup, site: &str) -> Vec<Candidate> {
    let mut found = Vec::new();
    let Ok(home) = Url::parse(&format!("https://{site}/")) else { return found };
    if let Some((page, base)) = look.get(&home, MAX_PAGE_BYTES, true).await {
        let html = String::from_utf8_lossy(&page);
        let mut manifest = None;
        for attrs in links(&html) {
            let attr = |n: &str| attrs.iter().find(|(k, _)| k == n).map(|(_, v)| v.trim().to_string());
            let rel = attr("rel").unwrap_or_default().to_ascii_lowercase();
            let Some(href) = attr("href").filter(|h| !h.is_empty()) else { continue };
            let Ok(url) = base.join(&href) else { continue };
            let rels: Vec<&str> = rel.split_whitespace().collect();
            if rels.contains(&"manifest") {
                manifest = Some(url);
                continue;
            }
            let apple = rels.iter().any(|r| r.starts_with("apple-touch-icon"));
            if !(apple || rels.contains(&"icon")) {
                continue;
            }
            let svg = attr("type").is_some_and(|t| t.contains("svg")) || url.path().ends_with(".svg");
            found.push(Candidate { score: score(attr("sizes").as_deref().and_then(largest), svg, apple, look.target), url });
        }
        if let Some(url) = manifest.filter(|u| look.scope.allows(u))
            && let Some((body, base)) = look.get(&url, MAX_MANIFEST_BYTES, false).await
            && let Ok(manifest) = serde_json::from_slice::<Value>(&body)
        {
            for icon in manifest.get("icons").and_then(Value::as_array).into_iter().flatten() {
                let Some(src) = icon.get("src").and_then(Value::as_str) else { continue };
                if icon.get("purpose").and_then(Value::as_str).is_some_and(|p| !p.split_whitespace().any(|x| x == "any")) {
                    continue;
                }
                let Ok(url) = base.join(src) else { continue };
                let svg = icon.get("type").and_then(Value::as_str).is_some_and(|t| t.contains("svg")) || url.path().ends_with(".svg");
                found.push(Candidate { score: score(icon.get("sizes").and_then(Value::as_str).and_then(largest), svg, false, look.target), url });
            }
        }
    }
    found.sort_by_key(|c| -c.score);
    if let Ok(ico) = home.join("/favicon.ico") {
        found.push(Candidate { url: ico, score: 0 });
    }
    found
}

/// The declared icon to try first: for a light tile, one drawn for light backgrounds or for
/// any; the largest of those.
fn declared_order(icons: &[IconSpec]) -> Vec<&IconSpec> {
    let mut list: Vec<&IconSpec> = icons.iter().filter(|i| i.theme.as_deref() != Some("dark")).collect();
    list.extend(icons.iter().filter(|i| i.theme.as_deref() == Some("dark")));
    list.sort_by_key(|i| {
        let svg = i.sizes.iter().any(|s| s == "any") || i.mime_type.as_deref().is_some_and(|m| m.contains("svg"));
        let dark = i.theme.as_deref() == Some("dark");
        (dark, -score(i.sizes.iter().filter_map(|s| largest(s)).max(), svg, false, TILE))
    });
    list
}

/// Finds the server's icon: the ones it declares, then its website's. `server_url` is the
/// remote server's address; a local server has none, and only its declared `data:` icons or
/// those on the website it names are used.
pub async fn find(server_url: Option<&str>, info: Option<&Implementation>) -> Result<Option<Icon>, Unreachable> {
    let server_url = server_url.and_then(|u| Url::parse(u).ok());
    let website = info.and_then(|i| i.website_url.as_deref()).and_then(|u| Url::parse(u).ok());
    let sites = sites(server_url.as_ref(), website.as_ref());
    let Some(look) = Lookup::new(Scope::Sites(sites.iter().cloned().collect()), TILE) else { return Ok(None) };
    let found = search(&look, &sites, info).await;
    // A declared `data:` icon needs no site.
    if found.is_none() && !look.answered.load(Ordering::Relaxed) && !sites.is_empty() {
        return Err(Unreachable);
    }
    Ok(found)
}

/// A site's icon, for the browsers' lists of sites (browser-design §10.1): the icons its home
/// page names, wherever they are kept, picked for a row, then its `/favicon.ico`. `origin` is
/// an https origin on the internet; any other has none to look for.
pub async fn find_site(origin: &str) -> Result<Option<Icon>, Unreachable> {
    let Some(host) = Url::parse(origin).ok().as_ref().and_then(public_host) else { return Ok(None) };
    let Some(look) = Lookup::new(Scope::Internet, SITE_ROW) else { return Ok(None) };
    let found = search(&look, &[host], None).await;
    if found.is_none() && !look.answered.load(Ordering::Relaxed) {
        return Err(Unreachable);
    }
    Ok(found)
}

/// The declared icons, then those of each site in turn, at most [`MAX_ATTEMPTS`] fetched.
async fn search(look: &Lookup, sites: &[String], info: Option<&Implementation>) -> Option<Icon> {
    let mut attempts = 0;
    if let Some(info) = info {
        for spec in declared_order(&info.icons) {
            if attempts >= MAX_ATTEMPTS {
                return None;
            }
            attempts += 1;
            if let Some(icon) = fetch(look, &spec.src, true).await {
                return Some(icon);
            }
        }
    }
    let mut tried = HashSet::new();
    for site in sites {
        for candidate in site_candidates(look, site).await {
            if attempts >= MAX_ATTEMPTS {
                return None;
            }
            if !tried.insert(candidate.url.to_string()) {
                continue;
            }
            attempts += 1;
            if let Some(icon) = fetch(look, candidate.url.as_str(), false).await {
                return Some(icon);
            }
        }
    }
    None
}

/// What is kept for one service: its icon, or that none was found, and when.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Stored {
    pub identity: String,
    pub fetched_at: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub mime: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub data: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source: Option<String>,
    #[serde(default)]
    pub declared: bool,
}

impl Stored {
    pub fn data_uri(&self) -> Option<String> {
        Some(format!("data:{};base64,{}", self.mime.as_deref()?, self.data.as_deref()?))
    }

    pub fn stale(&self) -> bool {
        now().saturating_sub(self.fetched_at) > REFRESH_AFTER.as_secs()
    }
}

fn now() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or_default()
}

/// The icons folder under the her home.
pub fn dir(her_home: &Path) -> PathBuf {
    her_home.join("mcp").join("icons")
}

fn file(dir: &Path, identity: &str) -> PathBuf {
    let digest = ring::digest::digest(&ring::digest::SHA256, identity.as_bytes());
    dir.join(format!("{}.json", digest.as_ref()[..8].iter().map(|b| format!("{b:02x}")).collect::<String>()))
}

pub fn load(dir: &Path, identity: &str) -> Option<Stored> {
    let stored: Stored = serde_json::from_slice(&std::fs::read(file(dir, identity)).ok()?).ok()?;
    (stored.identity == identity).then_some(stored)
}

/// Keeps what was found. A website's icon never replaces one the server declared.
pub fn store(dir: &Path, identity: &str, icon: Option<&Icon>) -> std::io::Result<()> {
    if icon.is_none_or(|i| !i.declared)
        && let Some(previous) = load(dir, identity)
        && previous.declared
        && previous.data.is_some()
        && !previous.stale()
    {
        return Ok(());
    }
    let stored = Stored {
        identity: identity.to_string(),
        fetched_at: now(),
        mime: icon.map(|i| i.mime.to_string()),
        data: icon.map(|i| base64::engine::general_purpose::STANDARD.encode(&i.bytes)),
        source: icon.map(|i| i.source.clone()),
        declared: icon.is_some_and(|i| i.declared),
    };
    std::fs::create_dir_all(dir)?;
    let target = file(dir, identity);
    let temp = target.with_extension(format!("{}.tmp", std::process::id()));
    std::fs::write(&temp, serde_json::to_vec(&stored).map_err(std::io::Error::other)?)?;
    std::fs::rename(temp, target)?;
    prune(dir);
    Ok(())
}

/// Keeps the [`MAX_FILES`] most recently written, dropping the rest: services checked once and
/// never added do not pile up.
fn prune(dir: &Path) {
    let Ok(entries) = std::fs::read_dir(dir) else { return };
    let mut files: Vec<(SystemTime, PathBuf)> = entries
        .flatten()
        .filter(|e| e.path().extension().is_some_and(|x| x == "json"))
        .filter_map(|e| Some((e.metadata().ok()?.modified().ok()?, e.path())))
        .collect();
    if files.len() <= MAX_FILES {
        return;
    }
    files.sort();
    for (_, path) in &files[..files.len() - MAX_FILES] {
        let _ = std::fs::remove_file(path);
    }
}

/// Looks for the icon unless one was looked for lately, or the server now declares one and the
/// stored one came from its website. Returns the stored result.
pub async fn refresh(dir: &Path, identity: &str, server_url: Option<&str>, info: Option<&Implementation>) -> Option<Stored> {
    let previous = load(dir, identity);
    let declares = info.is_some_and(|i| !i.icons.is_empty());
    let fresh = previous.as_ref().is_some_and(|p| !p.stale() && (p.declared || !declares));
    if fresh {
        return previous;
    }
    // Out of time or unreachable, nothing was learnt: what was kept stays, and the next check asks again.
    let Ok(Ok(found)) = tokio::time::timeout(LOOKUP_TIMEOUT, find(server_url, info)).await else { return previous };
    if let Err(err) = store(dir, identity, found.as_ref()) {
        tracing::warn!("could not keep the icon of {identity}: {err}");
    }
    load(dir, identity)
}

/// A site's icon, for a list showing it now: what is kept is given at once, even when old, and
/// an old one is looked for again in the background, for the next time it is shown. Only a site
/// never looked for is waited on.
pub async fn site(dir: &Path, origin: &str) -> Option<Stored> {
    match load(dir, origin) {
        Some(previous) if !previous.stale() => Some(previous),
        Some(previous) => {
            let (dir, origin) = (dir.to_path_buf(), origin.to_string());
            if SITES_LOOKED_FOR.lock().insert(origin.clone()) {
                tokio::spawn(async move {
                    look_for_site(&dir, &origin).await;
                    SITES_LOOKED_FOR.lock().remove(&origin);
                });
            }
            Some(previous)
        }
        None => look_for_site(dir, origin).await,
    }
}

/// The sites whose icons are being looked for in the background, each once at a time.
static SITES_LOOKED_FOR: LazyLock<Mutex<HashSet<String>>> = LazyLock::new(Default::default);

async fn look_for_site(dir: &Path, origin: &str) -> Option<Stored> {
    let Ok(Ok(found)) = tokio::time::timeout(LOOKUP_TIMEOUT, find_site(origin)).await else { return load(dir, origin) };
    if let Err(err) = store(dir, origin, found.as_ref()) {
        tracing::warn!("could not keep the icon of {origin}: {err}");
    }
    load(dir, origin)
}

#[cfg(test)]
mod tests {
    use super::*;

    const PNG: &[u8] = b"\x89PNG\r\n\x1a\n\0\0\0\rIHDR\0\0\0\x40\0\0\0\x40\x08\x06\0\0\0";

    #[test]
    fn types_come_from_the_bytes() {
        assert_eq!(check(PNG), Ok("image/png"));
        assert_eq!(check(&[0, 0, 1, 0, 1, 0]), Ok("image/x-icon"));
        assert_eq!(check(b"<svg xmlns=\"http://www.w3.org/2000/svg\"><path d=\"M0 0h1\"/></svg>"), Ok("image/svg+xml"));
        // A character cut at the 4096th byte does not hide what it is.
        let long = format!("<svg>{}</svg>", "<!-- 图标 -->".repeat(400));
        assert_eq!(check(long.as_bytes()), Ok("image/svg+xml"));
        assert!(check(b"<html><body>hi</body></html>").is_err());
        assert!(check(b"GIF89a....").is_err());
        let huge = [b"\x89PNG\r\n\x1a\n\0\0\0\rIHDR".as_slice(), &[0, 0, 0x40, 0, 0, 0, 0x40, 0]].concat();
        assert!(check(&huge).is_err(), "16384 pixels wide");
        assert!(check(&vec![0u8; MAX_BYTES + 1]).is_err());
    }

    #[test]
    fn svg_that_runs_or_loads_is_refused() {
        for svg in [
            "<svg><script>alert(1)</script></svg>",
            "<svg onload=\"alert(1)\"></svg>",
            "<svg><a href=\"javascript:alert(1)\">x</a></svg>",
            "<svg><image href=\"https://tracker.example/p.png\"/></svg>",
            "<svg><foreignObject><div/></foreignObject></svg>",
            "<svg style=\"background:url(https://x.example/a)\"></svg>",
        ] {
            assert!(check(svg.as_bytes()).is_err(), "{svg}");
        }
        assert!(check(b"<svg><use href=\"#a\"/><g id=\"a\"/></svg>").is_ok());
        assert!(check(b"<svg><text>button</text></svg>").is_ok(), "\"on\" inside a word");
    }

    #[test]
    fn a_page_offers_its_icons() {
        let html = r#"<head><link rel="stylesheet" href="/a.css"><LINK REL="icon" type="image/svg+xml" href="/logo.svg">
            <link rel='apple-touch-icon' sizes='180x180' href='/apple.png'><link rel="icon" sizes="16x16" href="/16.png"></head>"#;
        let found: Vec<(String, String)> = links(html)
            .into_iter()
            .filter_map(|a| Some((a.iter().find(|(k, _)| k == "rel")?.1.clone(), a.iter().find(|(k, _)| k == "href")?.1.clone())))
            .collect();
        assert_eq!(found[1], ("icon".into(), "/logo.svg".into()));
        assert_eq!(found[2], ("apple-touch-icon".into(), "/apple.png".into()));
        assert!(score(Some(180), false, true, TILE) > score(None, true, false, TILE));
        assert!(score(None, true, false, TILE) > score(Some(16), false, false, TILE));
        // A row wants a small one: 64 px over a touch icon, 32 px over 16.
        assert!(score(Some(64), false, false, SITE_ROW) > score(None, false, true, SITE_ROW));
        assert!(score(Some(32), false, false, SITE_ROW) > score(Some(180), false, true, SITE_ROW));
        assert!(score(Some(32), false, false, SITE_ROW) > score(Some(16), false, false, SITE_ROW));
    }

    #[test]
    fn sites_are_the_server_and_its_company() {
        let url = Url::parse("https://mcp.hermesh.io/mcp").unwrap();
        assert_eq!(sites(Some(&url), None), vec!["mcp.hermesh.io", "hermesh.io", "www.hermesh.io"]);
        let uk = Url::parse("https://api.shop.co.uk/mcp").unwrap();
        assert_eq!(sites(Some(&uk), None)[1], "shop.co.uk");
        let local = Url::parse("http://127.0.0.1:3000/mcp").unwrap();
        assert!(sites(Some(&local), None).is_empty());
        // A platform's subdomain is not the platform's brand.
        let hosted = Url::parse("https://my-tools.vercel.app/mcp").unwrap();
        assert_eq!(sites(Some(&hosted), None), vec!["my-tools.vercel.app"]);
        // A remote server's website elsewhere does not count; a local server's does.
        let elsewhere = Url::parse("https://tracker.example/").unwrap();
        assert!(!sites(Some(&url), Some(&elsewhere)).contains(&"tracker.example".to_string()));
        assert_eq!(sites(None, Some(&elsewhere)), vec!["tracker.example", "www.tracker.example"]);
    }

    #[test]
    fn a_website_icon_does_not_replace_a_declared_one() {
        let dir = tempfile::tempdir().unwrap();
        let declared = Icon { bytes: PNG.to_vec(), mime: "image/png", source: "data:".into(), declared: true };
        store(dir.path(), "https://a.example/mcp", Some(&declared)).unwrap();
        let site = Icon { declared: false, source: "https://a.example/favicon.ico".into(), ..declared.clone() };
        store(dir.path(), "https://a.example/mcp", Some(&site)).unwrap();
        let kept = load(dir.path(), "https://a.example/mcp").unwrap();
        assert!(kept.declared);
        assert!(kept.data_uri().unwrap().starts_with("data:image/png;base64,"));
        assert!(load(dir.path(), "https://b.example/mcp").is_none());
    }

    #[tokio::test]
    async fn a_site_unreached_keeps_what_it_had_and_learns_nothing() {
        let dir = tempfile::tempdir().unwrap();
        // `.invalid` never resolves: the network as good as down.
        let origin = "https://icons.invalid";
        assert!(matches!(find_site(origin).await, Err(Unreachable)));
        assert!(site(dir.path(), origin).await.is_none());
        assert!(load(dir.path(), origin).is_none(), "a miss was kept while offline");

        // A week-old icon is given at once, and stays when looking again finds nothing.
        let icon = Icon { bytes: PNG.to_vec(), mime: "image/png", source: format!("{origin}/favicon.ico"), declared: false };
        store(dir.path(), origin, Some(&icon)).unwrap();
        let mut old = load(dir.path(), origin).unwrap();
        old.fetched_at = 0;
        std::fs::write(file(dir.path(), origin), serde_json::to_vec(&old).unwrap()).unwrap();
        let given = tokio::time::timeout(Duration::from_millis(100), site(dir.path(), origin)).await.expect("waited for the site");
        assert_eq!(given.unwrap().data_uri(), Some(icon.data_uri()));
        for _ in 0..50 {
            if !SITES_LOOKED_FOR.lock().contains(origin) {
                break;
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
        assert_eq!(load(dir.path(), origin).unwrap().fetched_at, 0, "the old icon was dropped");
    }
}
