//! Signed releases: what `her install` and `her upgrade` download (standalone-cli-design §13).
//!
//! A manifest lists each version's files with their SHA-256; an ed25519 signature over it is
//! checked with the public key built into this program. So taking over the release repository
//! is not enough to get a file installed. Everything comes from GitHub Releases directly, with no
//! API calls and so no rate limit.

use base64::Engine;
use ring::digest;
use ring::signature;
use serde::Deserialize;
use std::collections::BTreeMap;
use std::io::Write;
use std::path::Path;
use std::path::PathBuf;

use crate::paths::HerHome;

const RELEASES: &str = "https://github.com/hermeshai/her-releases/releases";

/// The release signing key's public half, base64. Its private half stays with the maintainers.
const PUBLIC_KEY: &str = include_str!("release_public_key.txt");

const MANIFEST: &str = "her-manifest.json";

/// This build's version, the one its components have to match.
pub const VERSION: &str = env!("CARGO_PKG_VERSION");

/// The release this program was built for: a static build on Linux, so a musl target there.
pub fn target() -> Option<&'static str> {
    match (std::env::consts::OS, std::env::consts::ARCH) {
        ("macos", "aarch64") => Some("aarch64-apple-darwin"),
        ("macos", "x86_64") => Some("x86_64-apple-darwin"),
        ("linux", "x86_64") => Some("x86_64-unknown-linux-musl"),
        ("linux", "aarch64") => Some("aarch64-unknown-linux-musl"),
        _ => None,
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
pub struct Artifact {
    pub url: String,
    pub sha256: String,
    pub size: u64,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
pub struct Manifest {
    pub version: String,
    #[serde(default)]
    pub artifacts: BTreeMap<String, Artifact>,
    #[serde(default)]
    pub addons: BTreeMap<String, BTreeMap<String, Artifact>>,
}

/// Where releases come from and who signs them. Tests of a development build bring their own.
struct Source {
    releases: String,
    key: Vec<u8>,
}

impl Source {
    fn current() -> Result<Self, String> {
        let mut releases = RELEASES.to_string();
        let mut key = PUBLIC_KEY.trim().to_string();
        if cfg!(debug_assertions) {
            if let Ok(url) = std::env::var("HER_TEST_RELEASES") {
                releases = url;
            }
            if let Ok(test_key) = std::env::var("HER_TEST_RELEASE_KEY") {
                key = test_key;
            }
        }
        let key = base64::engine::general_purpose::STANDARD.decode(key).map_err(|e| format!("the built-in signing key is damaged: {e}"))?;
        Ok(Self { releases, key })
    }

    fn url(&self, version: Option<&str>, file: &str) -> String {
        match version {
            Some(version) => format!("{}/download/v{version}/{file}", self.releases),
            None => format!("{}/latest/download/{file}", self.releases),
        }
    }
}

/// `true` when `signature` over `bytes` is from the holder of `key`.
fn signed_by(key: &[u8], bytes: &[u8], signature_text: &str) -> bool {
    let Ok(sig) = base64::engine::general_purpose::STANDARD.decode(signature_text.trim()) else {
        return false;
    };
    signature::UnparsedPublicKey::new(&signature::ED25519, key).verify(bytes, &sig).is_ok()
}

/// The manifest of `version`, or of the latest release, with its signature checked.
pub async fn fetch_manifest(version: Option<&str>) -> Result<Manifest, String> {
    let source = Source::current()?;
    let client = crate::browser_install::client(false)?;
    let bytes = get(&client, &source.url(version, MANIFEST)).await?;
    let sig = get(&client, &source.url(version, &format!("{MANIFEST}.sig"))).await?;
    let sig = String::from_utf8_lossy(&sig);
    if !signed_by(&source.key, &bytes, &sig) {
        return Err("the release manifest's signature does not check out; nothing was installed".to_string());
    }
    let manifest: Manifest = serde_json::from_slice(&bytes).map_err(|e| format!("cannot read the release manifest: {e}"))?;
    if let Some(version) = version
        && manifest.version != version
    {
        return Err(format!("the manifest of {version} says it is {}; nothing was installed", manifest.version));
    }
    Ok(manifest)
}

async fn get(client: &reqwest::Client, url: &str) -> Result<Vec<u8>, String> {
    let response = client.get(url).send().await.map_err(|e| format!("cannot reach {url}: {e}"))?;
    if !response.status().is_success() {
        return Err(format!("{url}: HTTP {}", response.status().as_u16()));
    }
    response.bytes().await.map(|b| b.to_vec()).map_err(|e| format!("cannot read {url}: {e}"))
}

/// Downloads an artifact to `path`, checking its size and SHA-256.
pub async fn download(artifact: &Artifact, path: &Path, progress: impl Fn(u64, u64)) -> Result<(), String> {
    if !cfg!(debug_assertions) && !artifact.url.starts_with("https://") {
        return Err(format!("{} is not an HTTPS address; nothing was installed", artifact.url));
    }
    let client = crate::browser_install::client(false)?;
    let mut response = client.get(&artifact.url).send().await.map_err(|e| format!("cannot reach {}: {e}", artifact.url))?;
    if !response.status().is_success() {
        return Err(format!("{}: HTTP {}", artifact.url, response.status().as_u16()));
    }
    let mut file = std::fs::File::create(path).map_err(|e| format!("cannot write {}: {e}", path.display()))?;
    let mut hash = digest::Context::new(&digest::SHA256);
    let mut received = 0u64;
    while let Some(chunk) = response.chunk().await.map_err(|e| format!("the download failed: {e}"))? {
        received += chunk.len() as u64;
        if received > artifact.size {
            return Err("the download is larger than the manifest says; nothing was installed".to_string());
        }
        hash.update(&chunk);
        file.write_all(&chunk).map_err(|e| format!("cannot write {}: {e}", path.display()))?;
        progress(received, artifact.size);
    }
    let found: String = hash.finish().as_ref().iter().map(|byte| format!("{byte:02x}")).collect();
    if received != artifact.size || !found.eq_ignore_ascii_case(&artifact.sha256) {
        return Err("the download does not match the manifest; nothing was installed".to_string());
    }
    Ok(())
}

/// Unpacks a `.tar.gz` under `dir`. Entries that point out of it are refused by `tar`.
pub fn unpack(archive: &Path, dir: &Path) -> Result<(), String> {
    let file = std::fs::File::open(archive).map_err(|e| format!("cannot read {}: {e}", archive.display()))?;
    let mut tar = tar::Archive::new(flate2::read::GzDecoder::new(file));
    tar.set_preserve_permissions(true);
    tar.set_overwrite(true);
    for entry in tar.entries().map_err(|e| format!("cannot read the archive: {e}"))? {
        let mut entry = entry.map_err(|e| format!("cannot read the archive: {e}"))?;
        let unpacked = entry.unpack_in(dir).map_err(|e| format!("cannot unpack the archive: {e}"))?;
        if !unpacked {
            return Err("the archive has a file outside its folder; nothing was installed".to_string());
        }
    }
    Ok(())
}

/// A component this build can install.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Addon {
    Pdf,
    Computer,
}

impl Addon {
    pub fn name(self) -> &'static str {
        match self {
            Addon::Pdf => "pdf",
            Addon::Computer => "computer",
        }
    }

    /// The directory it lives in now, whether or not it is there.
    pub fn dir(self, home: &HerHome, version: &str) -> PathBuf {
        match self {
            Addon::Pdf => home.root().join("addons").join("pdf").join(version),
            // Not by version: the desktop tools find it by this name, and it speaks its own CLI.
            Addon::Computer => home.root().join("desktop").join("bin"),
        }
    }

    pub fn installed(self, home: &HerHome, version: &str) -> bool {
        let dir = self.dir(home, version);
        match self {
            Addon::Pdf => dir.is_dir(),
            Addon::Computer => dir.join(her_desktop::PROGRAM).is_file(),
        }
    }
}

/// Installs a component of `manifest` for `version`, whole: it replaces what was there only once
/// it is fully unpacked. Older versions of a versioned component go.
pub async fn install_addon(home: &HerHome, addon: Addon, manifest: &Manifest, progress: impl Fn(u64, u64)) -> Result<PathBuf, String> {
    let target = target().ok_or_else(|| format!("no builds of {} for this system", addon.name()))?;
    let artifact = manifest
        .addons
        .get(addon.name())
        .and_then(|by_target| by_target.get(target))
        .ok_or_else(|| format!("release {} has no {} component for {target}", manifest.version, addon.name()))?;
    let dir = addon.dir(home, &manifest.version);
    let parent = dir.parent().ok_or("a component directory has a parent")?;
    std::fs::create_dir_all(parent).map_err(|e| format!("cannot create {}: {e}", parent.display()))?;
    let work = tempfile::Builder::new().prefix(".install-").tempdir_in(parent).map_err(|e| format!("cannot write in {}: {e}", parent.display()))?;
    let archive = work.path().join("archive.tar.gz");
    download(artifact, &archive, progress).await?;
    let unpacked = work.path().join("unpacked");
    std::fs::create_dir(&unpacked).map_err(|e| format!("cannot write in {}: {e}", parent.display()))?;
    unpack(&archive, &unpacked)?;
    #[cfg(target_os = "macos")]
    verify_team_in(&unpacked)?;
    if dir.exists() {
        std::fs::remove_dir_all(&dir).map_err(|e| format!("cannot replace {}: {e}", dir.display()))?;
    }
    std::fs::rename(&unpacked, &dir).map_err(|e| format!("cannot install into {}: {e}", dir.display()))?;
    if addon == Addon::Pdf {
        remove_other_versions(parent, &manifest.version);
    }
    Ok(dir)
}

fn remove_other_versions(parent: &Path, keep: &str) {
    let Ok(entries) = std::fs::read_dir(parent) else { return };
    for entry in entries.flatten() {
        let name = entry.file_name();
        let name = name.to_string_lossy();
        if name != keep && !name.starts_with(".install-") {
            let _ = std::fs::remove_dir_all(entry.path());
        }
    }
}

/// Removes a component. Returns false when it was not there.
pub fn uninstall_addon(home: &HerHome, addon: Addon) -> Result<bool, String> {
    let dir = match addon {
        Addon::Pdf => home.root().join("addons").join("pdf"),
        Addon::Computer => addon.dir(home, VERSION),
    };
    if !dir.exists() {
        return Ok(false);
    }
    std::fs::remove_dir_all(&dir).map_err(|e| format!("cannot remove {}: {e}", dir.display()))?;
    Ok(true)
}

/// `0.2.10` after `0.2.9`; a pre-release before its release. Anything else compares as text.
pub fn is_newer(version: &str, than: &str) -> bool {
    fn parts(version: &str) -> (Vec<u64>, Option<&str>) {
        let (numbers, pre) = version.split_once('-').map_or((version, None), |(n, p)| (n, Some(p)));
        (numbers.split('.').map(|part| part.parse().unwrap_or(0)).collect(), pre)
    }
    let (a, a_pre) = parts(version);
    let (b, b_pre) = parts(than);
    match a.cmp(&b) {
        std::cmp::Ordering::Greater => true,
        std::cmp::Ordering::Less => false,
        std::cmp::Ordering::Equal => match (a_pre, b_pre) {
            (None, Some(_)) => true,
            (Some(a), Some(b)) => a > b,
            _ => false,
        },
    }
}

/// The team that signs our macOS files; the same check as the app's updates (D-531).
#[cfg(target_os = "macos")]
const TEAM_ID: &str = include_str!("release_team_id.txt");

/// Whether a file on macOS is signed by us. Skipped in development builds, whose tests bring
/// unsigned files.
#[cfg(target_os = "macos")]
pub fn verify_team(path: &Path) -> Result<(), String> {
    if cfg!(debug_assertions) {
        return Ok(());
    }
    let requirement = format!(
        "=anchor apple generic and certificate leaf[subject.OU] = \"{}\"",
        TEAM_ID.trim()
    );
    let status = std::process::Command::new("/usr/bin/codesign")
        .args(["--verify", "--strict", "-R", &requirement])
        .arg(path)
        .stderr(std::process::Stdio::null())
        .status()
        .map_err(|e| format!("cannot run codesign: {e}"))?;
    if status.success() { Ok(()) } else { Err(format!("{} is not signed by her's team; nothing was installed", path.display())) }
}

#[cfg(target_os = "macos")]
fn verify_team_in(dir: &Path) -> Result<(), String> {
    let entries = std::fs::read_dir(dir).map_err(|e| format!("cannot read {}: {e}", dir.display()))?;
    for entry in entries.flatten() {
        let path = entry.path();
        // Text beside the programs (a licence) has nothing to sign.
        let program = path.extension().is_some_and(|e| e == "dylib") || is_executable(&path);
        if program {
            verify_team(&path)?;
        }
    }
    Ok(())
}

#[cfg(target_os = "macos")]
fn is_executable(path: &Path) -> bool {
    use std::os::unix::fs::PermissionsExt;
    std::fs::metadata(path).is_ok_and(|meta| meta.is_file() && meta.permissions().mode() & 0o111 != 0)
}

#[cfg(test)]
mod tests {
    use super::*;
    use ring::signature::KeyPair;

    #[test]
    fn versions_compare_by_number_and_a_prerelease_comes_first() {
        assert!(is_newer("0.2.10", "0.2.9"));
        assert!(is_newer("0.3.0", "0.2.99"));
        assert!(!is_newer("0.2.0", "0.2.0"));
        assert!(!is_newer("0.2.0", "0.3.0"));
        assert!(is_newer("0.2.0", "0.2.0-rc.1"));
        assert!(!is_newer("0.2.0-rc.1", "0.2.0"));
    }

    #[test]
    fn only_the_signing_key_makes_a_good_signature() {
        let pair = signature::Ed25519KeyPair::from_seed_unchecked(&[7; 32]).expect("key");
        let other = signature::Ed25519KeyPair::from_seed_unchecked(&[8; 32]).expect("key");
        let text = base64::engine::general_purpose::STANDARD.encode(pair.sign(b"manifest").as_ref());
        assert!(signed_by(pair.public_key().as_ref(), b"manifest", &text));
        assert!(!signed_by(pair.public_key().as_ref(), b"another manifest", &text));
        assert!(!signed_by(other.public_key().as_ref(), b"manifest", &text));
        assert!(!signed_by(pair.public_key().as_ref(), b"manifest", "not base64!"));
    }

    #[test]
    fn the_built_in_key_is_a_key() {
        let key = base64::engine::general_purpose::STANDARD.decode(PUBLIC_KEY.trim()).expect("base64");
        assert_eq!(key.len(), 32);
    }
}
