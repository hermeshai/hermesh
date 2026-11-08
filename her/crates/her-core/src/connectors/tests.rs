use super::*;
use std::io::Write;

fn archive(entries: &[(&str, &[u8], tar::EntryType)]) -> Vec<u8> {
    let encoder = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast());
    let mut tar = tar::Builder::new(encoder);
    for (name, bytes, kind) in entries {
        let mut h = tar::Header::new_gnu();
        h.set_size(bytes.len() as u64);
        h.set_mode(0o755);
        h.set_entry_type(*kind);
        if kind.is_symlink() {
            h.set_link_name("/tmp/not-a-program").unwrap();
        }
        h.set_cksum();
        tar.append_data(&mut h, name, *bytes).unwrap();
    }
    tar.into_inner().unwrap().finish().unwrap()
}
fn artifact(bytes: &[u8]) -> Artifact {
    Artifact {
        url: "https://example.invalid/release".into(),
        sha256: digest(bytes),
        entry: "dws".into(),
        notices: default_notices(),
    }
}
#[test]
fn only_pinned_regular_program_and_notices_are_extracted() {
    let bytes = archive(&[
        ("./dws", b"program", tar::EntryType::Regular),
        ("LICENSE", b"license", tar::EntryType::Regular),
        ("NOTICE", b"notice", tar::EntryType::Regular),
        ("other", b"ignored", tar::EntryType::Regular),
    ]);
    let bundle = unpack(&bytes, &artifact(&bytes)).unwrap();
    assert_eq!(bundle.binary, b"program");
    assert_eq!(bundle.notices.len(), 2);
    let mut bad = artifact(&bytes);
    bad.sha256 = "0".repeat(64);
    assert!(unpack(&bytes, &bad).unwrap_err().contains("SHA-256"));
    for entries in [
        vec![("dws", b"".as_slice(), tar::EntryType::Symlink)],
        vec![
            ("dws", b"one".as_slice(), tar::EntryType::Regular),
            ("dws", b"two".as_slice(), tar::EntryType::Regular),
        ],
        vec![("dws", b"one".as_slice(), tar::EntryType::Regular)],
    ] {
        let bytes = archive(&entries);
        assert!(unpack(&bytes, &artifact(&bytes)).is_err());
    }
}
#[test]
fn manifests_bind_accounts_and_reject_management_overrides() {
    let m = manifest("dingtalk").unwrap();
    assert!(manifest("../../evil").is_err());
    let dir = tempfile::tempdir().unwrap();
    let mut config = server_config(dir.path(), &m, &std::env::current_exe().unwrap());
    assert!(for_server(dir.path(), &config).is_some());
    config.args.push("--extra".into());
    assert!(for_server(dir.path(), &config).is_none());
    for args in [
        vec!["auth", "login"],
        vec!["--help", "auth"],
        vec!["doc", "--profile=another"],
        vec!["doc", "--client-secret", "secret"],
    ] {
        assert!(
            validate_args(&m, &args.into_iter().map(String::from).collect::<Vec<_>>()).is_err()
        );
    }
    assert!(
        validate_args(
            &m,
            &[
                "doc".into(),
                "search".into(),
                "$(touch /tmp/not-executed)".into()
            ]
        )
        .is_ok()
    );
    assert!(
        auth_url(
            &m.auth.authorize_origin, &m.auth.authorize_path, None,
            "https://login.dingtalk.com.evil.test/oauth2/auth?code=secret"
        )
        .is_none()
    );
    assert!(auth_url(&m.auth.authorize_origin, &m.auth.authorize_path, None, "https://user@login.dingtalk.com/oauth2/auth").is_none());
    assert!(auth_url(&m.auth.authorize_origin, &m.auth.authorize_path, None, "https://login.dingtalk.com/oauth2/auth?state=abc\n").is_some());
}

#[cfg(unix)]
fn fixture() -> (tempfile::TempDir, Manifest) {
    // No network, no real credential store; argv and lifecycle behave like DWS.
    let dir = tempfile::tempdir().unwrap();
    let m = manifest("dingtalk").unwrap();
    let script = br##"#!/bin/sh
case "$1 $2" in
  "auth status")
    if test -f "$DWS_CONFIG_DIR/account"; then echo '{"authenticated":true}'; else echo '{"authenticated":false}'; fi ;;
  "auth login")
    echo $$ > "$DWS_CONFIG_DIR/pid"
    printf 'Open https://login.dingtalk.com/oauth2/auth?state=fixture\n'
    while ! test -f "$DWS_CONFIG_DIR/finish"; do sleep 0.05; done
    touch "$DWS_CONFIG_DIR/account" ;;
  "auth logout") rm -f "$DWS_CONFIG_DIR/account" ;;
  "doc slow") echo $$ > "$DWS_CONFIG_DIR/call-pid"; exec sleep 30 ;;
  "doc fail") echo 'provider error' >&2; exit 1 ;;
  *) printf '%s\n' "$@" ;;
esac
"##;
    publish(
        dir.path(),
        &m,
        &Bundle {
            binary: script.to_vec(),
            notices: BTreeMap::new(),
        },
    )
    .unwrap();
    (dir, m)
}
#[cfg(unix)]
#[tokio::test]
async fn login_cancel_retry_call_and_logout_use_isolated_credentials() {
    let (dir, m) = fixture();
    assert!(!status(dir.path(), &m).await.unwrap());
    assert!(execute(dir.path(), &m, vec!["doc".into()]).await.is_err());
    let cancel = CancellationToken::new();
    let pending = login(dir.path().to_path_buf(), m.clone(), cancel.clone())
        .await
        .unwrap();
    assert!(pending.url.contains("state=fixture"));
    cancel.cancel();
    assert!(pending.wait().await.unwrap_err().contains("取消"));
    assert!(!status(dir.path(), &m).await.unwrap());
    let completed = CancellationToken::new();
    let pending = login(
        dir.path().to_path_buf(),
        m.clone(),
        completed.clone(),
    )
    .await
    .unwrap();
    std::fs::write(root(dir.path(), &m).join("config/finish"), "").unwrap();
    pending.wait().await.unwrap();
    assert!(!completed.is_cancelled());
    assert!(signed_in(dir.path(), &m));
    let payload = "$(touch /tmp/her-connector-must-not-execute); literal";
    let result = execute(dir.path(), &m, vec!["doc".into(), payload.into()])
        .await
        .unwrap();
    assert_eq!(result["content"][0]["text"], format!("doc\n{payload}\n"));
    assert_eq!(result["isError"], false);
    assert_eq!(
        execute(dir.path(), &m, vec!["doc".into(), "fail".into()])
            .await
            .unwrap()["isError"],
        true
    );
    logout(dir.path(), &m).await.unwrap();
    assert!(!status(dir.path(), &m).await.unwrap());
    assert!(!signed_in(dir.path(), &m));
    assert!(execute(dir.path(), &m, vec!["doc".into()]).await.is_err());
    // Receipt detects executable corruption before another invocation.
    std::fs::OpenOptions::new()
        .append(true)
        .open(binary(dir.path(), &m))
        .unwrap()
        .write_all(b"modified")
        .unwrap();
    assert!(verify(dir.path(), &m).is_err());
}
#[cfg(unix)]
#[tokio::test]
async fn stdio_mcp_handshake_and_tool_call_are_usable_by_existing_runtime() {
    let (dir, m) = fixture();
    let _ = command(dir.path(), &m, &[]).unwrap();
    std::fs::write(root(dir.path(), &m).join("config/account"), "").unwrap();
    let requests = [
        json!({"jsonrpc":"2.0","id":1,"method":"initialize"}),
        json!({"jsonrpc":"2.0","method":"notifications/initialized"}),
        json!({"jsonrpc":"2.0","id":2,"method":"tools/list"}),
        json!({"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run","arguments":{"args":["doc","search","literal"]}}}),
        json!({"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"run","arguments":{"args":["auth","login"]}}}),
    ].iter().map(|r| format!("{r}\n")).collect::<String>();
    let mut out = Vec::new();
    serve(dir.path(), "dingtalk", requests.as_bytes(), &mut out)
        .await
        .unwrap();
    let mut responses: Vec<Value> = String::from_utf8(out)
        .unwrap()
        .lines()
        .map(|l| serde_json::from_str(l).unwrap())
        .collect();
    responses.sort_by_key(|r| r["id"].as_i64().unwrap());
    assert_eq!(responses.len(), 4);
    assert_eq!(responses[0]["result"]["serverInfo"]["name"], "dingtalk");
    assert_eq!(responses[1]["result"]["tools"][0]["name"], "run");
    assert_eq!(
        responses[2]["result"]["content"][0]["text"],
        "doc\nsearch\nliteral\n"
    );
    assert_eq!(responses[3]["result"]["isError"], true);
}

#[cfg(unix)]
#[tokio::test]
async fn cancelling_a_running_mcp_call_stops_the_cli_and_keeps_the_connection() {
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt};
    let (dir, m) = fixture();
    let _ = command(dir.path(), &m, &[]).unwrap();
    std::fs::write(root(dir.path(), &m).join("config/account"), "").unwrap();
    let (client, server) = tokio::io::duplex(8192);
    let (reader, writer) = tokio::io::split(server);
    let home = dir.path().to_path_buf();
    let task = tokio::spawn(async move { serve(&home, "dingtalk", reader, writer).await });
    let (r, mut w) = tokio::io::split(client);
    let mut r = tokio::io::BufReader::new(r);
    w.write_all(b"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"run\",\"arguments\":{\"args\":[\"doc\",\"slow\"]}}}\n").await.unwrap();
    let pid = root(dir.path(), &m).join("config/call-pid");
    tokio::time::timeout(Duration::from_secs(5), async {
        while !pid.is_file() {
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    })
    .await
    .unwrap();
    w.write_all(b"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/cancelled\",\"params\":{\"requestId\":1}}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"ping\"}\n").await.unwrap();
    let mut responses = Vec::new();
    for _ in 0..2 {
        let mut line = String::new();
        tokio::time::timeout(Duration::from_secs(5), r.read_line(&mut line))
            .await
            .unwrap()
            .unwrap();
        responses.push(serde_json::from_str::<Value>(&line).unwrap());
    }
    assert_eq!(
        responses.iter().find(|r| r["id"] == 1).unwrap()["result"]["isError"],
        true
    );
    assert_eq!(
        responses.iter().find(|r| r["id"] == 2).unwrap()["result"],
        json!({})
    );
    let pid = std::fs::read_to_string(pid).unwrap();
    tokio::time::timeout(Duration::from_secs(5), async {
        loop {
            if !std::process::Command::new("kill")
                .args(["-0", pid.trim()])
                .stderr(Stdio::null())
                .status()
                .unwrap()
                .success()
            {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    })
    .await
    .unwrap();
    w.shutdown().await.unwrap();
    drop(w);
    task.await.unwrap().unwrap();
}

#[tokio::test]
async fn provider_output_is_bounded() {
    let bytes = vec![b'x'; MAX_OUTPUT as usize + 1];
    assert!(
        bounded(bytes.as_slice())
            .await
            .unwrap_err()
            .contains("输出过大")
    );
}

#[cfg(unix)]
#[tokio::test]
async fn authorization_controls_config_writes_and_runtime_startup() {
    use crate::config::{LayerFiles, McpSettings};
    use crate::config_edit::{Target, write};
    use her_protocol::customize::{ConfigEdit, ConfigLayer, ConfigMerge};
    let (dir, m) = fixture();
    let mut config = server_config(dir.path(), &m, &std::env::current_exe().unwrap());
    assert_eq!(config.enabled, Some(false));
    let user = dir.path().join("config.toml");
    let target = Target {
        layer: ConfigLayer::User,
        files: LayerFiles {
            user: &user,
            project: None,
            user_project: None,
            project_root: None,
        },
        trusted: true,
        overrides: &[],
        header: None,
        allow_repo: false,
    };
    let edit = |value| ConfigEdit {
        key_path: "mcp_servers.dingtalk".into(),
        value,
        merge: ConfigMerge::Replace,
    };
    write(
        &target,
        &[edit(serde_json::to_value(&config).unwrap())],
        None,
    )
    .unwrap();
    config.enabled = Some(true);
    assert!(
        write(
            &target,
            &[edit(serde_json::to_value(&config).unwrap())],
            None
        )
        .unwrap_err()
        .to_string()
        .contains("授权")
    );
    let settings = McpSettings {
        enabled: true, project_json: false, startup_wait: Duration::ZERO,
        exposure: her_protocol::McpExposure::Deferred, cache_dir: None,
        her_home: Some(dir.path().to_path_buf()), client_metadata_url: None,
    };
    assert!(
        crate::mcp::McpManager::new(
            vec![config.clone()],
            &settings,
            None,
            vec![],
            dir.path().to_path_buf(),
            None
        )
        .is_none()
    );
    let mut out = Vec::new();
    assert!(
        serve(dir.path(), "dingtalk", b"".as_slice(), &mut out)
            .await
            .unwrap_err()
            .contains("尚未授权")
    );
    assert!(out.is_empty());
    std::fs::write(root(dir.path(), &m).join("config/account"), "").unwrap();
    assert!(status(dir.path(), &m).await.unwrap());
    write(
        &target,
        &[edit(serde_json::to_value(&config).unwrap())],
        None,
    )
    .unwrap();
    assert!(authorized(dir.path(), &config));
    logout(dir.path(), &m).await.unwrap();
    assert!(!authorized(dir.path(), &config));
    assert!(
        crate::mcp::McpManager::new(
            vec![config],
            &settings,
            None,
            vec![],
            dir.path().to_path_buf(),
            None
        )
        .is_none()
    );
}

#[test]
fn provider_manifests_and_nested_license_archives_are_valid() {
    for id in ["dingtalk", "feishu", "wecom"] {
        let m = manifest(id).unwrap();
        assert_eq!(m.artifacts.len(), 4);
        for a in m.artifacts.values() {
            assert_eq!(a.sha256.len(), 64);
            assert!(a.url.starts_with("https://"));
            let mut entries = vec![(a.entry.as_str(), b"program".as_slice(), tar::EntryType::Regular)];
            for n in &a.notices { entries.push((n, b"license", tar::EntryType::Regular)); }
            let bytes = archive(&entries);
            let mut a = a.clone(); a.sha256 = digest(&bytes);
            let bundle = unpack(&bytes, &a).unwrap();
            assert!(bundle.notices.contains_key("LICENSE"));
            assert!(bundle.notices.keys().all(|n| !n.contains('/')));
        }
        assert!(validate_args(&m, &["auth".into(), "show".into()]).is_err());
    }
    let m = manifest("feishu").unwrap();
    let text = r#"{"verification_uri":"https://accounts.feishu.cn/oauth/v1/device/verify","verification_uri_complete":"https://accounts.feishu.cn/oauth/v1/device/verify?user_code=test&flow_id=xyz"}"#;
    let url = auth_url(&m.auth.authorize_origin, &m.auth.authorize_path, m.auth.url_field.as_deref(), text).unwrap();
    assert!(url.contains("user_code=test&flow_id=xyz"));
    assert!(auth_url(&m.auth.authorize_origin, &m.auth.authorize_path, m.auth.url_field.as_deref(), &text.replace("accounts.feishu.cn", "accounts.feishu.cn.evil.test")).is_none());
}

#[cfg(unix)]
#[tokio::test]
async fn feishu_setup_is_not_user_consent_and_retry_skips_finished_setup() {
    let dir = tempfile::tempdir().unwrap();
    let m = manifest("feishu").unwrap();
    let script = br##"#!/bin/sh
D="$LARKSUITE_CLI_CONFIG_DIR"
case "$1 $2" in
 "config init")
   echo 'https://open.feishu.cn/page/cli?user_code=setup'
   while ! test -f "$D/setup-finish"; do sleep 0.01; done
   echo '{}' > "$D/config.json" ;;
 "auth login")
   echo '{"verification_uri":"https://accounts.feishu.cn/oauth/v1/device/verify","verification_uri_complete":"https://accounts.feishu.cn/oauth/v1/device/verify?user_code=consent"}'
   while ! test -f "$D/finish"; do sleep 0.01; done
   touch "$D/account" ;;
 "auth status")
   if test -f "$D/account"; then echo '{"identity":"user"}'; else echo '{"identity":"bot"}'; fi ;;
 "auth logout") rm -f "$D/account" ;;
esac
"##;
    publish(dir.path(), &m, &Bundle { binary: script.to_vec(), notices: BTreeMap::new() }).unwrap();
    let token = CancellationToken::new();
    let mut pending = login(dir.path().into(), m.clone(), token.clone()).await.unwrap();
    assert!(pending.url.contains("/page/cli"));
    let mut updates = pending.take_updates();
    std::fs::write(root(dir.path(), &m).join("config/setup-finish"), "").unwrap();
    let next = tokio::time::timeout(Duration::from_secs(5), updates.recv()).await.unwrap().unwrap();
    assert!(next.contains("user_code=consent"));
    assert!(!status(dir.path(), &m).await.unwrap());
    assert!(!signed_in(dir.path(), &m));
    token.cancel();
    assert!(pending.wait().await.is_err());
    let pending = login(dir.path().into(), m.clone(), CancellationToken::new()).await.unwrap();
    assert!(pending.url.contains("user_code=consent"));
    std::fs::write(root(dir.path(), &m).join("config/finish"), "").unwrap();
    pending.wait().await.unwrap();
    assert!(signed_in(dir.path(), &m));
    logout(dir.path(), &m).await.unwrap();
    assert!(!status(dir.path(), &m).await.unwrap());
}

#[cfg(unix)]
#[tokio::test]
async fn wecom_logout_removes_only_our_binding_and_prevents_legacy_restore() {
    let dir = tempfile::tempdir().unwrap();
    let m = manifest("wecom").unwrap();
    let script = br##"#!/bin/sh
if test -f "$WECOM_CLI_CONFIG_DIR/credentials.enc"; then echo authorized; else echo unauthorized; fi
"##;
    publish(dir.path(), &m, &Bundle { binary: script.to_vec(), notices: BTreeMap::new() }).unwrap();
    assert!(!status(dir.path(), &m).await.unwrap());
    let config = root(dir.path(), &m).join("config");
    for file in ["credentials.enc", "bot.enc", "token.enc", "keep"] { std::fs::write(config.join(file), "fixture").unwrap(); }
    assert!(status(dir.path(), &m).await.unwrap());
    logout(dir.path(), &m).await.unwrap();
    assert!(!status(dir.path(), &m).await.unwrap());
    assert!(config.join("keep").is_file());
    for file in ["credentials.enc", "bot.enc", "token.enc"] { assert!(!config.join(file).exists()); }
    logout(dir.path(), &m).await.unwrap();
}

#[test]
fn partial_grants_require_the_explicit_completion_event_and_expected_exit_code() {
    let m = manifest("feishu").unwrap();
    let rule = m.auth.partial_success.as_ref().unwrap();
    let complete = br#"{"event":"authorization_complete","user_open_id":"fixture","missing":["example:scope"],"warning":{"type":"missing_scope"}}"#;
    assert!(partial_success(rule, Some(3), complete));
    assert!(!partial_success(rule, Some(1), complete));
    assert!(!partial_success(rule, Some(3), br#"{"event":"authorization_failed","error":"denied"}"#));
    assert!(!partial_success(rule, Some(3), br#"{"event":"authorization_complete","missing":["example:scope"],"warning":{"type":"missing_scope"}}"#));
    let mut failed_after = complete.to_vec();
    failed_after.extend_from_slice(b"\n{\"event\":\"authorization_failed\"}\n");
    assert!(!partial_success(rule, Some(3), &failed_after));
}

#[cfg(unix)]
#[tokio::test]
async fn partial_grant_still_requires_valid_user_credentials_and_clears_on_full_login() {
    let dir = tempfile::tempdir().unwrap();
    let m = manifest("feishu").unwrap();
    let script = br##"#!/bin/sh
D="$LARKSUITE_CLI_CONFIG_DIR"
case "$1 $2" in
 "auth login")
   echo '{"verification_uri_complete":"https://accounts.feishu.cn/oauth/v1/device/verify?user_code=fixture"}'
   while ! test -f "$D/finish"; do sleep 0.01; done
   if test -f "$D/full"; then exit 0; fi
   echo '{"event":"authorization_complete","user_open_id":"fixture","missing":["example:scope"],"warning":{"type":"missing_scope"}}'
   exit 3 ;;
 "auth status") if test -f "$D/account"; then echo '{"identity":"user"}'; else echo '{"identity":"bot"}'; fi ;;
 "auth logout") rm -f "$D/account" ;;
esac
"##;
    publish(dir.path(), &m, &Bundle { binary: script.to_vec(), notices: BTreeMap::new() }).unwrap();
    let _ = command(dir.path(), &m, &[]).unwrap();
    let config = root(dir.path(), &m).join("config");
    std::fs::write(config.join("config.json"), "{}").unwrap();
    std::fs::write(config.join("finish"), "").unwrap();
    let start = || login(dir.path().into(), m.clone(), CancellationToken::new());
    assert!(start().await.unwrap().wait().await.unwrap_err().contains("未检测到有效登录"));
    assert!(!signed_in(dir.path(), &m));
    std::fs::write(config.join("account"), "").unwrap();
    start().await.unwrap().wait().await.unwrap();
    assert!(signed_in(dir.path(), &m));
    assert!(partial_permissions(dir.path(), &m));
    std::fs::write(config.join("full"), "").unwrap();
    start().await.unwrap().wait().await.unwrap();
    assert!(!partial_permissions(dir.path(), &m));
    logout(dir.path(), &m).await.unwrap();
    assert!(!partial_permissions(dir.path(), &m));
}
