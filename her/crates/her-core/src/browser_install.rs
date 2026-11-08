//! `her browser install` (browser-design D-194): Chrome for Testing, for machines without a
//! Chromium-family browser. It goes to `~/.her/browser/chromium`, which the managed browser
//! uses only when none is installed on the machine. Only the person runs it; nothing installs
//! a browser on its own.
//!
//! Checks: the archive comes over HTTPS from Google's storage, whose `x-goog-hash` header
//! carries its MD5, compared with what arrived; every file in it has its CRC checked as it is
//! unpacked; on macOS the app's signature is verified. The old installation is replaced only
//! once the new one passed all of these.

use base64::Engine;
use her_browser::launch::testing_executable;
use her_browser::launch::testing_platform;
use md5::Digest;
use std::fs::File;
use std::io;
use std::io::Write;
use std::path::Path;
use std::path::PathBuf;

/// The last known good versions, with their download URLs.
pub const MANIFEST_URL: &str =
    "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json";

/// What an installation came to.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Installed {
    pub version: String,
    pub executable: PathBuf,
    /// The same version was there already: nothing was downloaded.
    pub unchanged: bool,
}

/// Installs the current stable Chrome for Testing under `browser_dir` (`~/.her/browser`).
/// `progress` gets the bytes received and the total, when known.
pub async fn install(browser_dir: &Path, progress: impl Fn(u64, Option<u64>)) -> Result<Installed, String> {
    install_from(&client(false)?, MANIFEST_URL, browser_dir, progress).await
}

/// An HTTP client with her's TLS settings; redirects only to HTTPS.
pub(crate) fn client(no_proxy: bool) -> Result<reqwest::Client, String> {
    let tls = her_llm::tls_client_config().map_err(|e| format!("cannot set up TLS: {e}"))?;
    let redirects = reqwest::redirect::Policy::custom(|attempt| {
        if attempt.previous().len() >= 5 || attempt.url().scheme() != "https" {
            attempt.stop()
        } else {
            attempt.follow()
        }
    });
    let mut builder = reqwest::Client::builder()
        .use_preconfigured_tls(tls)
        .connect_timeout(std::time::Duration::from_secs(20))
        .redirect(redirects)
        .user_agent(format!("her/{}", env!("CARGO_PKG_VERSION")));
    if no_proxy {
        builder = builder.no_proxy();
    }
    builder.build().map_err(|e| format!("cannot set up HTTP: {e}"))
}

async fn install_from(
    client: &reqwest::Client,
    manifest_url: &str,
    browser_dir: &Path,
    progress: impl Fn(u64, Option<u64>),
) -> Result<Installed, String> {
    let platform = testing_platform().ok_or("Chrome for Testing has no build for this platform")?;
    let relative = testing_executable(platform).ok_or("Chrome for Testing has no build for this platform")?;
    let manifest: serde_json::Value = client
        .get(manifest_url)
        .send()
        .await
        .and_then(reqwest::Response::error_for_status)
        .map_err(|e| format!("cannot get the list of versions: {e}"))?
        .json()
        .await
        .map_err(|e| format!("cannot read the list of versions: {e}"))?;
    let (version, url) = stable_download(&manifest, platform)?;
    let target = browser_dir.join("chromium");
    let version_file = target.join("VERSION");
    if std::fs::read_to_string(&version_file).is_ok_and(|v| v.trim() == version) && target.join(&relative).is_file() {
        return Ok(Installed {
            version,
            executable: target.join(relative),
            unchanged: true,
        });
    }
    // Tests serve it locally.
    if !url.starts_with("https://") && !cfg!(test) {
        return Err(format!("refusing a download that is not over HTTPS: {url}"));
    }
    std::fs::create_dir_all(browser_dir).map_err(|e| format!("cannot create {}: {e}", browser_dir.display()))?;
    let pid = std::process::id();
    let archive = browser_dir.join(format!("chromium-{pid}.zip"));
    let staging = browser_dir.join(format!("chromium-{pid}.new"));
    let result = async {
        download(client, &url, &archive, &progress).await?;
        let (from, into) = (archive.clone(), staging.clone());
        tokio::task::spawn_blocking(move || unpack(&from, &into))
            .await
            .map_err(|e| e.to_string())??;
        let executable = staging.join(&relative);
        if !executable.is_file() {
            return Err(format!("the archive has no {}", relative.display()));
        }
        verify_signature(&executable)?;
        std::fs::write(staging.join("VERSION"), format!("{version}\n")).map_err(|e| e.to_string())?;
        // Replaced only now: a failed install leaves the old one as it was.
        let old = browser_dir.join(format!("chromium-{pid}.old"));
        if target.exists() {
            std::fs::rename(&target, &old).map_err(|e| format!("cannot replace the old browser: {e}"))?;
        }
        std::fs::rename(&staging, &target).map_err(|e| format!("cannot install the browser: {e}"))?;
        let _ = std::fs::remove_dir_all(&old);
        Ok(())
    }
    .await;
    let _ = std::fs::remove_file(&archive);
    let _ = std::fs::remove_dir_all(&staging);
    result?;
    Ok(Installed {
        version,
        executable: target.join(relative),
        unchanged: false,
    })
}

/// The stable version and its download for `platform`.
fn stable_download(manifest: &serde_json::Value, platform: &str) -> Result<(String, String), String> {
    let stable = &manifest["channels"]["Stable"];
    let version = stable["version"].as_str().ok_or("the list of versions has no stable version")?;
    let url = stable["downloads"]["chrome"]
        .as_array()
        .and_then(|downloads| downloads.iter().find(|d| d["platform"] == platform))
        .and_then(|d| d["url"].as_str())
        .ok_or_else(|| format!("the stable version has no download for {platform}"))?;
    Ok((version.to_string(), url.to_string()))
}

/// Downloads `url` into `to`, comparing it with the MD5 the server announces.
async fn download(client: &reqwest::Client, url: &str, to: &Path, progress: &impl Fn(u64, Option<u64>)) -> Result<(), String> {
    let mut response = client
        .get(url)
        .send()
        .await
        .and_then(reqwest::Response::error_for_status)
        .map_err(|e| format!("cannot download the browser: {e}"))?;
    let expected = response
        .headers()
        .get_all("x-goog-hash")
        .iter()
        .filter_map(|v| v.to_str().ok())
        .find_map(md5_of_header)
        .ok_or("the download came without a checksum (x-goog-hash), so it cannot be checked")?;
    let total = response.content_length();
    let mut file = File::create(to).map_err(|e| format!("cannot write {}: {e}", to.display()))?;
    let mut hasher = md5::Md5::new();
    let mut received = 0u64;
    while let Some(chunk) = response.chunk().await.map_err(|e| format!("the download broke off: {e}"))? {
        hasher.update(&chunk);
        file.write_all(&chunk).map_err(|e| format!("cannot write {}: {e}", to.display()))?;
        received += chunk.len() as u64;
        progress(received, total);
    }
    file.flush().map_err(|e| e.to_string())?;
    if total.is_some_and(|total| total != received) {
        return Err("the download ended early".into());
    }
    if hasher.finalize().as_slice() != expected.as_slice() {
        return Err("the download does not match its checksum; nothing was installed".into());
    }
    Ok(())
}

/// The MD5 in an `x-goog-hash` value such as `crc32c=n03x6A==,md5=Ojk9c3dhfxgoKVVHYwFbHQ==`.
fn md5_of_header(value: &str) -> Option<Vec<u8>> {
    value.split(',').find_map(|part| {
        let encoded = part.trim().strip_prefix("md5=")?;
        base64::engine::general_purpose::STANDARD.decode(encoded).ok().filter(|d| d.len() == 16)
    })
}

/// Unpacks the archive, with its links and modes (a macOS app has both); paths that would
/// leave `into` are refused. Reading checks each file's CRC.
fn unpack(archive: &Path, into: &Path) -> Result<(), String> {
    let file = File::open(archive).map_err(|e| e.to_string())?;
    let mut zip = zip::ZipArchive::new(file).map_err(|e| format!("not a zip archive: {e}"))?;
    std::fs::create_dir_all(into).map_err(|e| e.to_string())?;
    for i in 0..zip.len() {
        let mut entry = zip.by_index(i).map_err(|e| format!("cannot read the archive: {e}"))?;
        let relative = entry
            .enclosed_name()
            .ok_or_else(|| format!("the archive has an unsafe path: {}", entry.name().unwrap_or_default()))?;
        let out = into.join(&relative);
        if entry.is_dir() {
            std::fs::create_dir_all(&out).map_err(|e| e.to_string())?;
            continue;
        }
        if let Some(parent) = out.parent() {
            std::fs::create_dir_all(parent).map_err(|e| e.to_string())?;
        }
        if entry.is_symlink() {
            let mut target = String::new();
            io::Read::read_to_string(&mut entry, &mut target).map_err(|e| format!("cannot read the archive: {e}"))?;
            // A link may point inside the archive only.
            let resolved = relative.parent().unwrap_or(Path::new("")).join(&target);
            if Path::new(&target).is_absolute() || escapes(&resolved) {
                return Err(format!("the archive has a link out of it: {}", relative.display()));
            }
            #[cfg(unix)]
            std::os::unix::fs::symlink(&target, &out).map_err(|e| e.to_string())?;
            #[cfg(not(unix))]
            return Err("the archive has links, which this platform's install does not support".into());
            #[cfg(unix)]
            continue;
        }
        let mut file = File::create(&out).map_err(|e| e.to_string())?;
        io::copy(&mut entry, &mut file).map_err(|e| format!("the archive is damaged ({}): {e}", relative.display()))?;
        #[cfg(unix)]
        if let Some(mode) = entry.unix_mode() {
            use std::os::unix::fs::PermissionsExt;
            let _ = file.set_permissions(std::fs::Permissions::from_mode(mode & 0o755));
        }
    }
    Ok(())
}

/// Whether a relative path climbs out of where it starts.
fn escapes(path: &Path) -> bool {
    let mut depth = 0i32;
    for part in path.components() {
        match part {
            std::path::Component::ParentDir => depth -= 1,
            std::path::Component::Normal(_) => depth += 1,
            _ => return true,
        }
        if depth < 0 {
            return true;
        }
    }
    false
}

/// On macOS, the app must carry a valid signature.
fn verify_signature(executable: &Path) -> Result<(), String> {
    if !cfg!(target_os = "macos") || std::env::var_os("HER_TEST_UNSIGNED_BROWSER").is_some() {
        return Ok(());
    }
    // `…/X.app/Contents/MacOS/X` → `…/X.app`.
    let app = executable.ancestors().nth(3).ok_or("unexpected app layout")?;
    let status = std::process::Command::new("/usr/bin/codesign")
        .args(["--verify", "--deep", "--strict"])
        .arg(app)
        .stderr(std::process::Stdio::null())
        .status()
        .map_err(|e| format!("cannot check the app's signature: {e}"))?;
    if status.success() {
        Ok(())
    } else {
        Err("the app's signature does not verify; nothing was installed".into())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;
    use tokio::io::AsyncReadExt;
    use tokio::io::AsyncWriteExt;

    #[test]
    fn manifest_and_hash_headers() {
        let manifest = serde_json::json!({
            "channels": { "Stable": { "version": "140.0.1", "downloads": { "chrome": [
                { "platform": "linux64", "url": "https://x/linux.zip" },
                { "platform": "mac-arm64", "url": "https://x/mac.zip" },
            ] } } }
        });
        assert_eq!(stable_download(&manifest, "mac-arm64").unwrap(), ("140.0.1".into(), "https://x/mac.zip".into()));
        assert!(stable_download(&manifest, "win64").is_err());
        let md5 = md5_of_header("crc32c=n03x6A==, md5=Ojk9c3dhfxgoKVVHYwFbHQ==").unwrap();
        assert_eq!(md5.len(), 16);
        assert!(md5_of_header("crc32c=n03x6A==").is_none());
        assert!(escapes(Path::new("a/../../b")));
        assert!(!escapes(Path::new("a/b/../c")));
    }

    /// The archive a platform's download would be: its executable and a link beside it.
    fn archive(platform: &str, extra: Option<(&str, &str)>) -> Vec<u8> {
        let mut out = io::Cursor::new(Vec::new());
        let mut zip = zip::ZipWriter::new(&mut out);
        let options = zip::write::SimpleFileOptions::default().unix_permissions(0o755);
        let exe = testing_executable(platform).unwrap();
        zip.start_file(exe.to_string_lossy(), options).unwrap();
        zip.write_all(b"#!/bin/sh\necho chrome\n").unwrap();
        zip.add_symlink(format!("chrome-{platform}/current"), exe.file_name().unwrap().to_string_lossy(), options)
            .unwrap();
        if let Some((link, target)) = extra {
            zip.add_symlink(link, target, options).unwrap();
        }
        zip.finish().unwrap();
        out.into_inner()
    }

    /// Serves the manifest and the archive; `md5` as announced (None: no header).
    async fn serve(archive: Vec<u8>, md5: Option<String>) -> String {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let base = format!("http://127.0.0.1:{}", listener.local_addr().unwrap().port());
        let platform = testing_platform().unwrap();
        let manifest = serde_json::json!({ "channels": { "Stable": { "version": "140.0.1", "downloads": {
            "chrome": [{ "platform": platform, "url": format!("{base}/chrome.zip") }] } } } })
        .to_string();
        let archive = Arc::new(archive);
        tokio::spawn(async move {
            while let Ok((mut stream, _)) = listener.accept().await {
                let (archive, manifest, md5) = (archive.clone(), manifest.clone(), md5.clone());
                tokio::spawn(async move {
                    let mut buf = vec![0u8; 4096];
                    let n = stream.read(&mut buf).await.unwrap_or(0);
                    let request = String::from_utf8_lossy(&buf[..n]).to_string();
                    let (body, hash): (Vec<u8>, String) = if request.starts_with("GET /chrome.zip") {
                        let hash = md5.map(|m| format!("x-goog-hash: crc32c=AAAAAA==\r\nx-goog-hash: md5={m}\r\n")).unwrap_or_default();
                        ((*archive).clone(), hash)
                    } else {
                        (manifest.into_bytes(), String::new())
                    };
                    let head = format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\n{hash}Connection: close\r\n\r\n", body.len());
                    let _ = stream.write_all(head.as_bytes()).await;
                    let _ = stream.write_all(&body).await;
                });
            }
        });
        format!("{base}/manifest.json")
    }

    fn md5_base64(bytes: &[u8]) -> String {
        base64::engine::general_purpose::STANDARD.encode(md5::Md5::digest(bytes))
    }

    #[tokio::test]
    async fn installs_checks_and_replaces_only_when_all_is_well() {
        // SAFETY: set before any thread of this test reads it; no other test reads it.
        unsafe { std::env::set_var("HER_TEST_UNSIGNED_BROWSER", "1") };
        let Some(platform) = testing_platform() else { return };
        let dir = tempfile::tempdir().unwrap();
        let client = client(true).unwrap();
        let good = archive(platform, None);

        // A checksum that does not match: nothing installed, nothing left behind.
        let url = serve(good.clone(), Some(md5_base64(b"something else"))).await;
        let err = install_from(&client, &url, dir.path(), |_, _| {}).await.unwrap_err();
        assert!(err.contains("checksum"), "{err}");
        // No checksum at all.
        let url = serve(good.clone(), None).await;
        assert!(install_from(&client, &url, dir.path(), |_, _| {}).await.unwrap_err().contains("without a checksum"));
        // A link out of the archive.
        let bad = archive(platform, Some((&format!("chrome-{platform}/evil"), "../../../etc/passwd")));
        let url = serve(bad.clone(), Some(md5_base64(&bad))).await;
        assert!(install_from(&client, &url, dir.path(), |_, _| {}).await.unwrap_err().contains("link out"));
        assert!(!dir.path().join("chromium").exists());
        assert_eq!(std::fs::read_dir(dir.path()).unwrap().count(), 0, "no leftovers");

        // A good one installs, with its link, and the pool would find it.
        let url = serve(good.clone(), Some(md5_base64(&good))).await;
        let seen = std::sync::Mutex::new(0u64);
        let installed = install_from(&client, &url, dir.path(), |n, _| *seen.lock().unwrap() = n).await.unwrap();
        assert_eq!(installed.version, "140.0.1");
        assert!(!installed.unchanged);
        assert_eq!(*seen.lock().unwrap(), good.len() as u64);
        assert!(installed.executable.is_file());
        assert_eq!(her_browser::launch::installed_executable(dir.path()), Some(installed.executable.clone()));
        #[cfg(unix)]
        assert!(dir.path().join(format!("chromium/chrome-{platform}/current")).is_symlink());
        // Again: the same version is kept.
        assert!(install_from(&client, &url, dir.path(), |_, _| {}).await.unwrap().unchanged);
    }
}
