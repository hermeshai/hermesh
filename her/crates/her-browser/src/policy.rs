//! What may be visited: schemes, the class of an address, the domain lists and the
//! configured sites (browser-design §7.2). Pure checks; asking the user is the kernel's job.

use std::collections::BTreeMap;
use std::net::IpAddr;
use std::net::Ipv4Addr;
use std::net::Ipv6Addr;
use std::path::Path;
use std::path::PathBuf;
use url::Url;

/// What a URL is, for the first gate.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum UrlKind {
    /// http or https.
    Web,
    /// `about:blank`.
    Blank,
    /// A local file.
    File(PathBuf),
    /// Never opened; the reason says why.
    Refused(String),
}

pub fn url_kind(url: &Url) -> UrlKind {
    match url.scheme() {
        "http" | "https" if url.host().is_some() => UrlKind::Web,
        "about" if url.path() == "blank" => UrlKind::Blank,
        "file" => match url.to_file_path() {
            Ok(path) => UrlKind::File(path),
            Err(()) => UrlKind::Refused(format!("{url} is not a local file path")),
        },
        scheme => UrlKind::Refused(format!("{scheme}: URLs cannot be opened")),
    }
}

/// The origin as the policy names it: `https://example.com`, `http://localhost:5173`,
/// `file://` for every local file, `null` for `about:blank` and the like.
pub fn origin_of(url: &Url) -> String {
    match url.scheme() {
        "file" => "file://".to_string(),
        _ => url.origin().ascii_serialization(),
    }
}

/// Where an address leads.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum AddressClass {
    /// This machine: treated as development, like an ordinary site.
    Loopback,
    Public,
    /// A private network: every origin needs its own permission, whatever the mode.
    Private,
    /// Cloud instance metadata: never.
    Metadata,
}

pub fn classify_ip(ip: IpAddr) -> AddressClass {
    match ip {
        IpAddr::V4(v4) => classify_v4(v4),
        IpAddr::V6(v6) => match v6.to_ipv4_mapped() {
            Some(v4) => classify_v4(v4),
            None => classify_v6(v6),
        },
    }
}

fn classify_v4(ip: Ipv4Addr) -> AddressClass {
    let [a, b, c, d] = ip.octets();
    if [[169, 254, 169, 254], [169, 254, 170, 2], [100, 100, 100, 200]].contains(&[a, b, c, d]) {
        return AddressClass::Metadata;
    }
    if ip.is_loopback() {
        return AddressClass::Loopback;
    }
    let private = ip.is_private()
        || ip.is_link_local()
        || ip.is_unspecified()
        || ip.is_broadcast()
        // Carrier-grade NAT.
        || (a == 100 && (64..128).contains(&b))
        // "This network".
        || a == 0;
    if private { AddressClass::Private } else { AddressClass::Public }
}

fn classify_v6(ip: Ipv6Addr) -> AddressClass {
    let segments = ip.segments();
    if segments == [0xfd00, 0x0ec2, 0, 0, 0, 0, 0, 0x0254] {
        return AddressClass::Metadata;
    }
    if ip.is_loopback() {
        return AddressClass::Loopback;
    }
    let unique_local = segments[0] & 0xfe00 == 0xfc00;
    let link_local = segments[0] & 0xffc0 == 0xfe80;
    if unique_local || link_local || ip.is_unspecified() {
        AddressClass::Private
    } else {
        AddressClass::Public
    }
}

/// The class of a URL's host: the most restrictive of the addresses it resolves to. A name
/// that does not resolve counts as public; loading it fails anyway, and the address the
/// response came from is checked again (§7.2).
pub async fn classify_url(url: &Url) -> AddressClass {
    let Some(host) = url.host() else {
        return AddressClass::Public;
    };
    let port = url.port_or_known_default().unwrap_or(80);
    match host {
        url::Host::Ipv4(ip) => classify_ip(IpAddr::V4(ip)),
        url::Host::Ipv6(ip) => classify_ip(IpAddr::V6(ip)),
        url::Host::Domain(name) => {
            let name = name.trim_end_matches('.').to_ascii_lowercase();
            if name == "localhost" || name.ends_with(".localhost") {
                return AddressClass::Loopback;
            }
            match tokio::net::lookup_host((name.as_str(), port)).await {
                Ok(addresses) => addresses
                    .map(|address| classify_ip(address.ip()))
                    .max()
                    .unwrap_or(AddressClass::Public),
                Err(_) => AddressClass::Public,
            }
        }
    }
}

/// What a configured site allows.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum SiteLevel {
    Deny,
    Navigate,
    Act,
}

impl SiteLevel {
    pub fn parse(text: &str) -> Option<Self> {
        match text {
            "deny" => Some(Self::Deny),
            "navigate" => Some(Self::Navigate),
            "act" => Some(Self::Act),
            _ => None,
        }
    }
}

/// The configured part of the policy: domain lists and sites by origin.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SitePolicy {
    /// When not empty, only these domains.
    pub allowed_domains: Vec<String>,
    pub denied_domains: Vec<String>,
    /// By origin, as [`origin_of`] writes it.
    pub sites: BTreeMap<String, SiteLevel>,
    /// `browser.script_enabled`: `browser_eval` may be asked about at all (§7.6).
    pub script_enabled: bool,
}

impl SitePolicy {
    /// Why the domain lists or a `deny` site refuse `url`, if they do.
    pub fn refusal(&self, url: &Url) -> Option<String> {
        let origin = origin_of(url);
        if self.sites.get(&origin) == Some(&SiteLevel::Deny) {
            return Some(format!("{origin} is denied by browser.sites"));
        }
        let host = url.host_str()?.trim_end_matches('.').to_ascii_lowercase();
        if let Some(pattern) = self.denied_domains.iter().find(|p| domain_matches(p, &host)) {
            return Some(format!("{host} is blocked by browser.denied_domains ({pattern})"));
        }
        if !self.allowed_domains.is_empty() && !self.allowed_domains.iter().any(|p| domain_matches(p, &host)) {
            return Some(format!("{host} is not in browser.allowed_domains"));
        }
        None
    }

    pub fn site(&self, origin: &str) -> Option<SiteLevel> {
        self.sites.get(origin).copied()
    }

    /// `"*"` in `browser.sites`: every public site, never a private one (D-221).
    pub fn any_site(&self) -> Option<SiteLevel> {
        self.sites.get(ANY_SITE).copied()
    }
}

/// The key of "every site" in `browser.sites` and in a session's grants.
pub const ANY_SITE: &str = "*";

/// `example.com` matches the domain and its subdomains; `*.example.com` only the subdomains.
pub fn domain_matches(pattern: &str, host: &str) -> bool {
    let pattern = pattern.trim().trim_end_matches('.').to_ascii_lowercase();
    match pattern.strip_prefix("*.") {
        Some(base) => host.len() > base.len() && host.ends_with(base) && host[..host.len() - base.len()].ends_with('.'),
        None => host == pattern || (host.ends_with(&pattern) && host[..host.len() - pattern.len()].ends_with('.')),
    }
}

/// An origin as written in `browser.sites`, normalized; `None` when it is not one.
pub fn normalize_origin(text: &str) -> Option<String> {
    let url = Url::parse(text.trim()).ok()?;
    match url.scheme() {
        "http" | "https" => Some(origin_of(&url)),
        _ => None,
    }
}

/// Whether `path` lies inside one of `roots` (both canonical when they exist).
pub fn inside(path: &Path, roots: &[PathBuf]) -> bool {
    let path = std::fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf());
    roots.iter().any(|root| {
        let root = std::fs::canonicalize(root).unwrap_or_else(|_| root.clone());
        path.starts_with(root)
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn url(text: &str) -> Url {
        Url::parse(text).unwrap()
    }

    #[test]
    fn schemes() {
        assert_eq!(url_kind(&url("https://example.com/a")), UrlKind::Web);
        assert_eq!(url_kind(&url("about:blank")), UrlKind::Blank);
        assert!(matches!(url_kind(&url("file:///tmp/a.html")), UrlKind::File(_)));
        for refused in ["javascript:alert(1)", "data:text/html,hi", "chrome://settings", "chrome-extension://x/y", "about:config"] {
            assert!(matches!(url_kind(&url(refused)), UrlKind::Refused(_)), "{refused}");
        }
        assert_eq!(origin_of(&url("https://Example.com:443/x?y")), "https://example.com");
        assert_eq!(origin_of(&url("http://localhost:5173/")), "http://localhost:5173");
        assert_eq!(origin_of(&url("file:///tmp/a.html")), "file://");
    }

    #[test]
    fn address_classes() {
        let class = |text: &str| classify_ip(text.parse().unwrap());
        assert_eq!(class("127.0.0.1"), AddressClass::Loopback);
        assert_eq!(class("::1"), AddressClass::Loopback);
        assert_eq!(class("169.254.169.254"), AddressClass::Metadata);
        assert_eq!(class("fd00:ec2::254"), AddressClass::Metadata);
        assert_eq!(class("::ffff:169.254.169.254"), AddressClass::Metadata);
        for private in ["10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.10", "169.254.1.1", "100.64.0.1", "0.0.0.0", "fc00::1", "fe80::1"] {
            assert_eq!(class(private), AddressClass::Private, "{private}");
        }
        for public in ["8.8.8.8", "172.32.0.1", "100.128.0.1", "2606:4700::1111"] {
            assert_eq!(class(public), AddressClass::Public, "{public}");
        }
    }

    #[tokio::test]
    async fn hosts_resolve_to_their_most_restrictive_address() {
        assert_eq!(classify_url(&url("http://localhost:3000/")).await, AddressClass::Loopback);
        assert_eq!(classify_url(&url("http://app.localhost/")).await, AddressClass::Loopback);
        assert_eq!(classify_url(&url("http://192.168.0.5:8080/")).await, AddressClass::Private);
        assert_eq!(classify_url(&url("http://[::1]:8080/")).await, AddressClass::Loopback);
        assert_eq!(classify_url(&url("http://169.254.169.254/latest/")).await, AddressClass::Metadata);
    }

    #[test]
    fn domain_lists_and_sites() {
        assert!(domain_matches("example.com", "example.com"));
        assert!(domain_matches("example.com", "a.example.com"));
        assert!(!domain_matches("example.com", "badexample.com"));
        assert!(domain_matches("*.example.com", "a.example.com"));
        assert!(!domain_matches("*.example.com", "example.com"));
        let mut policy = SitePolicy {
            allowed_domains: vec![],
            denied_domains: vec!["evil.test".into()],
            sites: BTreeMap::new(),
            script_enabled: false,
        };
        policy.sites.insert("https://bank.example.com".into(), SiteLevel::Deny);
        assert!(policy.refusal(&url("https://www.evil.test/")).is_some());
        assert!(policy.refusal(&url("https://bank.example.com/login")).is_some());
        assert!(policy.refusal(&url("https://example.com/")).is_none());
        policy.allowed_domains = vec!["github.com".into()];
        assert!(policy.refusal(&url("https://example.com/")).unwrap().contains("allowed_domains"));
        assert!(policy.refusal(&url("https://api.github.com/")).is_none());
        assert_eq!(normalize_origin("https://GitHub.com/"), Some("https://github.com".into()));
        assert_eq!(normalize_origin("github.com"), None);
    }
}
