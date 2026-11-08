//! `her install` and `her upgrade` against a local release: a signed manifest and archives.

mod common;

use base64::Engine;
use common::TestEnv;
use ring::signature::Ed25519KeyPair;
use ring::signature::KeyPair;
use serde_json::json;
use std::collections::HashMap;
use std::io::Read;
use std::io::Write;
use std::net::TcpListener;
use std::sync::Arc;
use std::sync::Mutex;

const VERSION: &str = env!("CARGO_PKG_VERSION");

/// Serves files by path until the test ends.
struct Releases {
    base: String,
    files: Arc<Mutex<HashMap<String, Vec<u8>>>>,
}

impl Releases {
    fn start() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let files: Arc<Mutex<HashMap<String, Vec<u8>>>> = Default::default();
        let served = files.clone();
        std::thread::spawn(move || {
            for stream in listener.incoming() {
                let mut stream = stream.unwrap();
                let mut request = [0u8; 2048];
                let read = stream.read(&mut request).unwrap_or(0);
                let text = String::from_utf8_lossy(&request[..read]).into_owned();
                let path = text.split_whitespace().nth(1).unwrap_or("/").to_string();
                let found = served.lock().unwrap().get(&path).cloned();
                let reply = match found {
                    Some(body) => {
                        let mut reply = format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len()).into_bytes();
                        reply.extend(body);
                        reply
                    }
                    None => b"HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n".to_vec(),
                };
                let _ = stream.write_all(&reply);
            }
        });
        Self { base, files }
    }

    fn put(&self, path: &str, body: Vec<u8>) {
        self.files.lock().unwrap().insert(path.to_string(), body);
    }
}

fn key() -> Ed25519KeyPair {
    Ed25519KeyPair::from_seed_unchecked(&[42; 32]).unwrap()
}

fn base64(bytes: &[u8]) -> String {
    base64::engine::general_purpose::STANDARD.encode(bytes)
}

fn target() -> &'static str {
    her_core::release::target().expect("a supported system")
}

fn sha256(bytes: &[u8]) -> String {
    ring::digest::digest(&ring::digest::SHA256, bytes).as_ref().iter().map(|byte| format!("{byte:02x}")).collect()
}

/// A `.tar.gz` of (name, content, mode).
fn archive(files: &[(&str, &[u8], u32)]) -> Vec<u8> {
    let mut tar = tar::Builder::new(flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast()));
    for (name, content, mode) in files {
        let mut header = tar::Header::new_gnu();
        header.set_size(content.len() as u64);
        header.set_mode(*mode);
        header.set_cksum();
        tar.append_data(&mut header, name, *content).unwrap();
    }
    tar.into_inner().unwrap().finish().unwrap()
}

/// Publishes `version` with these archives, signed by `signer`.
fn publish(releases: &Releases, version: &str, main: Option<&[u8]>, pdf: Option<&[u8]>, signer: &Ed25519KeyPair) {
    let dir = format!("/download/v{version}");
    let mut manifest = json!({ "version": version, "artifacts": {}, "addons": {} });
    if let Some(main) = main {
        releases.put(&format!("{dir}/her.tar.gz"), main.to_vec());
        manifest["artifacts"][target()] = json!({ "url": format!("{}{dir}/her.tar.gz", releases.base), "sha256": sha256(main), "size": main.len() });
    }
    if let Some(pdf) = pdf {
        releases.put(&format!("{dir}/pdf.tar.gz"), pdf.to_vec());
        manifest["addons"]["pdf"] = json!({ target(): { "url": format!("{}{dir}/pdf.tar.gz", releases.base), "sha256": sha256(pdf), "size": pdf.len() } });
    }
    let bytes = serde_json::to_vec(&manifest).unwrap();
    let signature = base64(signer.sign(&bytes).as_ref());
    for prefix in [dir.as_str(), "/latest/download"] {
        releases.put(&format!("{prefix}/her-manifest.json"), bytes.clone());
        releases.put(&format!("{prefix}/her-manifest.json.sig"), signature.clone().into_bytes());
    }
}

async fn run(env: &TestEnv, releases: &Releases, args: &[&str]) -> (i32, String, String) {
    let out = env
        .her(args)
        .env("HER_TEST_RELEASES", &releases.base)
        .env("HER_TEST_RELEASE_KEY", base64(key().public_key().as_ref()))
        .output()
        .await
        .expect("run her");
    (out.status.code().unwrap_or(-1), String::from_utf8_lossy(&out.stdout).into_owned(), String::from_utf8_lossy(&out.stderr).into_owned())
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_component_installs_from_a_signed_release_and_uninstalls() {
    let env = TestEnv::new("manual", "").await;
    let releases = Releases::start();
    let pdf = archive(&[("libpdfium.test", b"library", 0o644)]);
    publish(&releases, VERSION, None, Some(&pdf), &key());
    let (code, stdout, stderr) = run(&env, &releases, &["install", "pdf"]).await;
    assert_eq!(code, 0, "{stdout}{stderr}");
    assert!(stdout.contains(&format!("pdf {VERSION} installed")), "{stdout}");
    let dir = env.home.path().join("addons/pdf").join(VERSION);
    assert_eq!(std::fs::read(dir.join("libpdfium.test")).unwrap(), b"library");

    let (code, stdout, _) = run(&env, &releases, &["install", "pdf"]).await;
    assert_eq!(code, 0);
    assert!(stdout.contains("installed already"), "{stdout}");

    let (code, stdout, _) = run(&env, &releases, &["uninstall", "pdf"]).await;
    assert_eq!(code, 0);
    assert!(stdout.contains("pdf removed"), "{stdout}");
    assert!(!dir.exists());
    let (_, stdout, _) = run(&env, &releases, &["uninstall", "pdf"]).await;
    assert!(stdout.contains("was not installed"), "{stdout}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn nothing_installs_without_the_right_signature_or_the_right_file() {
    let env = TestEnv::new("manual", "").await;
    let releases = Releases::start();
    let pdf = archive(&[("libpdfium.test", b"library", 0o644)]);
    let stranger = Ed25519KeyPair::from_seed_unchecked(&[9; 32]).unwrap();
    publish(&releases, VERSION, None, Some(&pdf), &stranger);
    let (code, _, stderr) = run(&env, &releases, &["install", "pdf"]).await;
    assert_eq!(code, 2);
    assert!(stderr.contains("signature"), "{stderr}");

    publish(&releases, VERSION, None, Some(&pdf), &key());
    releases.put(&format!("/download/v{VERSION}/pdf.tar.gz"), archive(&[("evil", b"another file", 0o644)]));
    let (code, _, stderr) = run(&env, &releases, &["install", "pdf"]).await;
    assert_eq!(code, 2);
    assert!(stderr.contains("does not match the manifest"), "{stderr}");
    let addons = env.home.path().join("addons/pdf");
    let left: Vec<_> = std::fs::read_dir(&addons).map(|dir| dir.flatten().map(|e| e.file_name()).collect()).unwrap_or_default();
    assert!(left.is_empty(), "nothing stays behind: {left:?}");

    // A release without this component says so.
    publish(&releases, VERSION, None, None, &key());
    let (code, _, stderr) = run(&env, &releases, &["install", "pdf"]).await;
    assert_eq!(code, 2);
    assert!(stderr.contains("has no pdf component"), "{stderr}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn upgrade_replaces_the_program_and_its_components() {
    let env = TestEnv::new("manual", "").await;
    let releases = Releases::start();
    let new_program = b"#!/bin/sh\necho new her\n";
    let main = archive(&[("her", new_program, 0o755)]);
    let pdf = archive(&[("libpdfium.test", b"library", 0o644)]);
    publish(&releases, VERSION, None, Some(&pdf), &key());
    let (code, ..) = run(&env, &releases, &["install", "pdf"]).await;
    assert_eq!(code, 0);
    publish(&releases, "99.0.0", Some(&main), Some(&pdf), &key());

    let (code, stdout, _) = run(&env, &releases, &["upgrade", "--check", "--json"]).await;
    assert_eq!(code, 0, "{stdout}");
    let found: serde_json::Value = serde_json::from_str(&stdout).unwrap();
    assert_eq!(found["upgrade_available"], true);
    assert_eq!(found["latest"], "99.0.0");

    // A copy of the program, so that the one the tests run stays.
    let bin = tempfile::tempdir().unwrap();
    let copy = bin.path().join("her");
    std::fs::copy(env!("CARGO_BIN_EXE_her"), &copy).unwrap();
    let out = tokio::process::Command::new(&copy)
        .arg("upgrade")
        .env("HER_HOME", env.home.path())
        .env("HOME", env.home.path())
        .env("HER_TEST_RELEASES", &releases.base)
        .env("HER_TEST_RELEASE_KEY", base64(key().public_key().as_ref()))
        .output()
        .await
        .unwrap();
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(out.status.success(), "{stdout}{}", String::from_utf8_lossy(&out.stderr));
    assert!(stdout.contains("upgraded from") && stdout.contains("99.0.0"), "{stdout}");
    assert_eq!(std::fs::read(&copy).unwrap(), new_program);
    assert!(env.home.path().join("addons/pdf/99.0.0/libpdfium.test").is_file(), "the component came along");
    assert!(!env.home.path().join("addons/pdf").join(VERSION).exists(), "and the old one went");
}

/// tools/release/install.sh against a local release: what `curl … | sh` does.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_install_script_checks_and_installs() {
    let releases = Releases::start();
    let program = b"#!/bin/sh\necho her 9.9.9\n";
    let main = archive(&[("her", program, 0o755), ("README.md", b"read me", 0o644)]);
    let name = format!("her-9.9.9-{}.tar.gz", target());
    let sums = format!("{}  {name}\n", sha256(&main));
    for dir in ["/latest/download", "/download/v9.9.9"] {
        releases.put(&format!("{dir}/{name}"), main.clone());
        releases.put(&format!("{dir}/SHA256SUMS"), sums.clone().into_bytes());
    }
    let script = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../tools/release/install.sh");
    let home = tempfile::tempdir().unwrap();
    let dir = home.path().join("bin");
    let install = |extra: &[&str]| {
        std::process::Command::new("sh")
            .arg(&script)
            .args(extra)
            .env("HOME", home.path())
            .env("HER_RELEASES_URL", &releases.base)
            .env("HER_INSTALL_DIR", &dir)
            .output()
            .unwrap()
    };
    let out = install(&[]);
    let stdout = String::from_utf8_lossy(&out.stdout).into_owned();
    assert!(out.status.success(), "{stdout}{}", String::from_utf8_lossy(&out.stderr));
    assert!(stdout.contains("installed her 9.9.9"), "{stdout}");
    assert!(stdout.contains("is not in your PATH"), "{stdout}");
    assert_eq!(std::fs::read(dir.join("her")).unwrap(), program);
    let out = install(&["--version", "9.9.9"]);
    assert!(out.status.success());

    // A file that is not what the list says is not installed.
    std::fs::remove_file(dir.join("her")).unwrap();
    releases.put(&format!("/latest/download/{name}"), archive(&[("her", b"something else", 0o755)]));
    let out = install(&[]);
    assert!(!out.status.success());
    assert!(String::from_utf8_lossy(&out.stderr).contains("does not match"));
    assert!(!dir.join("her").exists());

    let out = install(&["--version", "1.0.0"]);
    assert!(!out.status.success(), "a version that does not exist");
}
