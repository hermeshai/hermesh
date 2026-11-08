//! End-to-end: what the 自定义 page does (docs/desktop/customize-design.md), through `her
//! serve` and through the commands that do the same from a terminal. Skills are installed,
//! switched and removed; MCP servers are added with a secret kept in the credential store,
//! checked, listed and removed; and a page hears about changes made under it.

mod common;

use common::SchemaCheck;
use common::TestEnv;
use common::native_init;
use her_rpc::Incoming;
use her_rpc::IncomingRx;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::path::PathBuf;
use std::sync::OnceLock;
use std::time::Duration;

/// Builds the test MCP server once, next to this test's own binary.
fn server_bin() -> PathBuf {
    static BIN: OnceLock<PathBuf> = OnceLock::new();
    BIN.get_or_init(|| {
        let exe = std::env::current_exe().unwrap();
        let profile_dir = exe.parent().unwrap().parent().unwrap().to_path_buf();
        let cargo = std::env::var("CARGO").unwrap_or_else(|_| "cargo".into());
        let mut build = std::process::Command::new(cargo);
        build.args(["build", "-q", "-p", "her-mock", "--bin", "her-mcp-server"]);
        if profile_dir.ends_with("release") {
            build.arg("--release");
        }
        assert!(build.status().expect("cargo build").success(), "building her-mcp-server failed");
        profile_dir.join("her-mcp-server")
    })
    .clone()
}

/// A skill folder with a script, the way one arrives from a download.
fn write_skill(parent: &Path, name: &str, description: &str) -> PathBuf {
    let dir = parent.join(name);
    std::fs::create_dir_all(dir.join("scripts")).unwrap();
    std::fs::write(dir.join("SKILL.md"), format!("---\nname: {name}\ndescription: {description}\n---\nDo it.\n")).unwrap();
    std::fs::write(dir.join("scripts/run.sh"), "echo hi\n").unwrap();
    dir
}

/// A directory that is a project: a git repository.
fn project(env: &TestEnv, name: &str) -> PathBuf {
    let dir = env.workspace(name);
    std::fs::create_dir_all(dir.join(".git")).unwrap();
    dir
}

async fn run(env: &TestEnv, cwd: &Path, args: &[&str]) -> (i32, String) {
    let output = env.her_in(cwd, args).output().await.unwrap();
    let text = format!("{}{}", String::from_utf8_lossy(&output.stdout), String::from_utf8_lossy(&output.stderr));
    (output.status.code().unwrap_or(-1), text)
}

fn user_config(env: &TestEnv) -> String {
    std::fs::read_to_string(env.home.path().join("config.toml")).unwrap()
}

/// The names in the credential file the tests use instead of the keychain.
fn saved_credentials(env: &TestEnv) -> Vec<String> {
    std::fs::read_to_string(env.home.path().join("credentials.toml"))
        .unwrap_or_default()
        .lines()
        .filter_map(|line| line.split_once('=').map(|(name, _)| name.trim().trim_matches('"').to_string()))
        .filter(|name| name.starts_with("mcp"))
        .collect()
}

async fn skill_state(env: &TestEnv, cwd: &Path, name: &str) -> String {
    let output = env.her_in(cwd, &["skills", "--all", "--json"]).output().await.unwrap();
    let report: Value = serde_json::from_slice(&output.stdout).unwrap();
    let skills = report["skills"].as_array().unwrap();
    skills.iter().find(|s| s["name"] == name).map(|s| s["state"].as_str().unwrap().to_string()).unwrap_or_else(|| "missing".into())
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_command_line_adds_switches_and_removes_skills_and_servers() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let work = project(&env, "repo");
    let download = write_skill(&env.workspace("downloads"), "weekly", "Weekly report.");
    let from = download.to_str().unwrap();

    // A skill: copied into ~/.her/skills, refused a second time, off and on by name.
    let (code, out) = run(&env, &work, &["skills", "add", from]).await;
    assert_eq!(code, 0, "{out}");
    let installed = env.home.path().join("skills/weekly");
    assert!(installed.join("scripts/run.sh").is_file());
    let (code, out) = run(&env, &work, &["skills", "add", from]).await;
    assert_eq!(code, 2);
    assert!(out.contains("--replace"), "{out}");
    assert_eq!(run(&env, &work, &["skills", "add", from, "--replace"]).await.0, 0);
    assert_eq!(run(&env, &work, &["skills", "disable", "weekly"]).await.0, 0);
    assert!(user_config(&env).contains("disabled = [\"weekly\"]"), "{}", user_config(&env));
    assert_eq!(skill_state(&env, &work, "weekly").await, "disabled");
    assert_eq!(run(&env, &work, &["skills", "enable", "weekly"]).await.0, 0);
    assert_eq!(skill_state(&env, &work, "weekly").await, "active");
    assert!(!user_config(&env).contains("disabled"), "{}", user_config(&env));

    // Only the user's own skills are removed, and into the trash.
    let (code, out) = run(&env, &work, &["skills", "remove", "skill-creator"]).await;
    assert_eq!(code, 2, "the built-in skill stays: {out}");
    let (code, out) = run(&env, &work, &["skills", "remove", "weekly"]).await;
    assert_eq!(code, 0, "{out}");
    assert!(!installed.exists());
    let trashed = if cfg!(target_os = "macos") { ".Trash/weekly" } else { ".local/share/Trash/files/weekly" };
    assert!(env.user_home().join(trashed).join("SKILL.md").is_file(), "{out}");

    // A server with a secret: the value goes to the credential store, the file gets a reference.
    let bin = server_bin();
    let args = ["mcp", "add", "vault", "--env", "API_KEY=sk-test-1", "--secret-env", "API_KEY", "--", bin.to_str().unwrap()];
    let (code, out) = run(&env, &work, &args).await;
    assert_eq!(code, 0, "{out}");
    assert!(out.contains("connected:"), "checked once after adding: {out}");
    let config = user_config(&env);
    assert!(config.contains("API_KEY = \"${credential:mcp.vault.API_KEY}\""), "{config}");
    assert!(!config.contains("sk-test-1"));
    assert_eq!(saved_credentials(&env), vec!["mcp.vault.API_KEY"]);
    let (code, out) = run(&env, &work, &["mcp", "add", "vault", "--url", "http://127.0.0.1:9/mcp"]).await;
    assert_eq!(code, 2, "{out}");

    // A session gets the secret's value.
    let (code, out) = run(&env, &work, &["exec", "[[mock:mcp_env:vault:API_KEY]] read it"]).await;
    assert_eq!(code, 0, "{out}");
    assert!(out.contains("mcp done: sk-test-1"), "{out}");

    // Off for this project only, then on again.
    assert_eq!(run(&env, &work, &["mcp", "disable", "vault", "--local"]).await.0, 0);
    let personal = std::fs::read_dir(env.home.path().join("projects")).unwrap().flatten().next().unwrap().path().join("config.toml");
    assert!(std::fs::read_to_string(&personal).unwrap().contains("enabled = false"));
    let (_, listed) = run(&env, &work, &["mcp", "list"]).await;
    assert!(listed.contains("disabled"), "{listed}");
    let (_, elsewhere) = run(&env, &env.workspace("other"), &["mcp", "list"]).await;
    assert!(!elsewhere.contains("disabled"), "another directory still has it: {elsewhere}");
    assert_eq!(run(&env, &work, &["mcp", "enable", "vault"]).await.0, 0);
    assert!(!std::fs::read_to_string(&personal).unwrap().contains("enabled"));

    // Removed: its credential, used nowhere else, goes too.
    let (code, out) = run(&env, &work, &["mcp", "remove", "vault"]).await;
    assert_eq!(code, 0, "{out}");
    assert!(!user_config(&env).contains("vault"));
    assert!(saved_credentials(&env).is_empty(), "{out}");

    // The repository's file only with --project; its locks stay out of the repository.
    let (code, out) = run(&env, &work, &["mcp", "add", "shared", "--project", "--", bin.to_str().unwrap()]).await;
    assert_eq!(code, 0, "{out}");
    assert!(out.contains("not trusted"), "{out}");
    assert!(std::fs::read_to_string(work.join(".her/config.toml")).unwrap().contains("[mcp_servers.shared]"));
    assert!(run(&env, &work, &["mcp", "remove", "shared"]).await.1.contains("--project"));
    assert_eq!(run(&env, &work, &["mcp", "remove", "shared", "--project"]).await.0, 0);
    let entries: Vec<String> = std::fs::read_dir(work.join(".her")).unwrap().flatten().map(|e| e.file_name().to_string_lossy().into_owned()).collect();
    assert_eq!(entries, vec!["config.toml"]);
}

/// The next notification of `method`, skipping others.
async fn notification(rx: &mut IncomingRx, method: &str) -> Value {
    let deadline = tokio::time::Instant::now() + Duration::from_secs(20);
    loop {
        let incoming = tokio::time::timeout_at(deadline, rx.recv()).await.unwrap_or_else(|_| panic!("no {method}")).expect("connection closed");
        if let Incoming::Notification { method: m, params, .. } = incoming
            && m == method
        {
            return params;
        }
    }
}

fn error_kind(err: &her_rpc::RpcError) -> &str {
    err.data.as_ref().and_then(|d| d["kind"].as_str()).unwrap_or("")
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_page_manages_configuration_skills_and_servers_through_the_service() {
    let env = TestEnv::new("manual", "").await;
    let schema = SchemaCheck::new(&[
        "ConfigReadResult",
        "ConfigWriteResult",
        "SkillsInstallResult",
        "SkillsFilesResult",
        "SkillsReadResult",
        "SkillsSetEnabledResult",
        "SkillsListResult",
        "McpListResult",
        "McpCheckResult",
        "McpToolsResult",
    ]);
    let work = project(&env, "repo");
    let (mut child, client, mut rx) = env.spawn_rpc(&["serve", "--stdio"]);
    let init = client.request("initialize", json!({"protocol_version": 2, "client_info": {"name": "e2e"}})).await.unwrap();
    assert_eq!(init["capabilities"]["customize"], true);

    // The layers, each with its file and version; reading subscribes to changes.
    let read = client.request("config/read", json!({"cwd": work, "layers": true})).await.unwrap();
    schema.check("ConfigReadResult", &read);
    let layers: Vec<&str> = read["layers"].as_array().unwrap().iter().map(|l| l["layer"].as_str().unwrap()).collect();
    assert!(layers.contains(&"user") && layers.contains(&"user_project"), "{layers:?}");
    let user = read["layers"].as_array().unwrap().iter().find(|l| l["layer"] == "user").unwrap().clone();
    assert_eq!(user["writable"], true);
    let project_layer = read["layers"].as_array().unwrap().iter().find(|l| l["layer"] == "project").unwrap().clone();
    assert_eq!(project_layer["writable"], false);

    // A connector as the custom-connector form adds it: the secret first, then the server.
    client.request("credentials/set", json!({"name": "mcp.vault.API_KEY", "value": "sk-page-2"})).await.unwrap();
    let err = client.request("credentials/set", json!({"name": "deepseek", "value": "x"})).await.unwrap_err();
    assert_eq!(err.code, -32602, "only names her manages");
    let edit = json!({"key_path": "mcp_servers.vault", "value": {
        "command": server_bin(), "args": ["--tools", "3"], "env": {"API_KEY": "${credential:mcp.vault.API_KEY}"}}});
    let written = client
        .request("config/write", json!({"layer": "user", "edits": [edit], "expected_version": user["version"]}))
        .await
        .unwrap();
    schema.check("ConfigWriteResult", &written);
    assert_eq!(written["status"], "ok");
    // A second write against the old version is refused; the repository's file is not written.
    let stale = json!({"layer": "user", "edits": [{"key_path": "model", "value": "mock/mock-1"}], "expected_version": user["version"]});
    let err = client.request("config/write", stale).await.unwrap_err();
    assert_eq!((err.code, error_kind(&err)), (-32023, "config_version_conflict"));
    let repo = json!({"layer": "project", "cwd": work, "edits": [{"key_path": "model", "value": "x/y"}]});
    let err = client.request("config/write", repo).await.unwrap_err();
    assert_eq!((err.code, error_kind(&err)), (-32024, "config_layer_readonly"));
    let bad = json!({"layer": "user", "edits": [{"key_path": "mcp_servers.half", "value": {"args": ["x"]}}]});
    let err = client.request("config/write", bad).await.unwrap_err();
    assert_eq!((err.code, error_kind(&err)), (-32025, "config_validation_error"));

    // Listed without starting it, checked once, then its tools come from the cache.
    let listed = client.request("mcp/list", json!({"cwd": work})).await.unwrap();
    schema.check("McpListResult", &listed);
    let vault = listed["servers"].as_array().unwrap().iter().find(|s| s["name"] == "vault").unwrap().clone();
    assert_eq!((vault["layer"].as_str(), vault["editable"].as_bool()), (Some("user"), Some(true)));
    assert_eq!(vault["status"]["state"], "unknown", "{vault:#}");
    assert_eq!(vault["auth"], "credential");
    assert_eq!(vault["auth_state"], "saved");
    assert_eq!(vault["credentials"], json!(["mcp.vault.API_KEY"]));
    let checked = client.request("mcp/check", json!({"name": "vault", "cwd": work})).await.unwrap();
    schema.check("McpCheckResult", &checked);
    assert_eq!(checked["server"]["status"]["state"], "ok", "{checked:#}");
    let count = checked["tools"].as_array().unwrap().len();
    assert!(count > 3, "{checked:#}");
    let tools = client.request("mcp/tools", json!({"name": "vault", "cwd": work})).await.unwrap();
    schema.check("McpToolsResult", &tools);
    assert_eq!((tools["source"].as_str(), tools["tools"].as_array().unwrap().len()), (Some("cache"), count));
    let listed = client.request("mcp/list", json!({"cwd": work})).await.unwrap();
    let vault = listed["servers"].as_array().unwrap().iter().find(|s| s["name"] == "vault").unwrap().clone();
    assert_eq!((vault["status"]["state"].as_str(), vault["status"]["source"].as_str()), (Some("ok"), Some("cache")));
    assert_eq!(vault["status"]["tools"].as_u64(), Some(count as u64));

    // Off for this project only: the personal project layer, which the page may write.
    let off = json!({"layer": "user_project", "cwd": work, "edits": [{"key_path": "mcp_servers.vault.enabled", "value": false}]});
    let written = client.request("config/write", off).await.unwrap();
    assert!(written["file"].as_str().unwrap().contains("/projects/"), "{written:#}");
    let listed = client.request("mcp/list", json!({"cwd": work})).await.unwrap();
    let vault = listed["servers"].as_array().unwrap().iter().find(|s| s["name"] == "vault").unwrap().clone();
    assert_eq!((vault["enabled"].as_bool(), vault["disabled_by"].as_str()), (Some(false), Some("user_project")));

    // Skills: installed from a folder, described, switched off by path, removed.
    let download = write_skill(&env.workspace("downloads"), "weekly", "Weekly report.");
    let dry = client.request("skills/install", json!({"from": download, "scope": "user", "dry_run": true})).await.unwrap();
    schema.check("SkillsInstallResult", &dry);
    assert!(dry.get("conflict").is_none());
    let installed = client.request("skills/install", json!({"from": download, "scope": "user"})).await.unwrap();
    let target = installed["target"].as_str().unwrap().to_string();
    assert_eq!(installed["skill"]["removable"], true);
    let err = client.request("skills/install", json!({"from": download, "scope": "user"})).await.unwrap_err();
    assert_eq!(error_kind(&err), "skill_conflict");
    let files = client.request("skills/files", json!({"path": target})).await.unwrap();
    schema.check("SkillsFilesResult", &files);
    let paths: Vec<&str> = files["files"].as_array().unwrap().iter().filter_map(|f| f["path"].as_str()).collect();
    assert_eq!(paths, vec!["SKILL.md", "scripts", "scripts/run.sh"]);
    let script = client.request("skills/read", json!({"path": target, "file": "scripts/run.sh"})).await.unwrap();
    schema.check("SkillsReadResult", &script);
    assert_eq!(script["text"], "echo hi\n");
    let err = client.request("skills/read", json!({"path": target, "file": "../weekly/SKILL.md"})).await.unwrap_err();
    assert_eq!(error_kind(&err), "skill_invalid");
    let switched = client.request("skills/set_enabled", json!({"path": target, "enabled": false})).await.unwrap();
    schema.check("SkillsSetEnabledResult", &switched);
    let scanned = client.request("skills/list", json!({"cwd": work})).await.unwrap();
    let weekly = scanned["skills"].as_array().unwrap().iter().find(|s| s["name"] == "weekly").unwrap().clone();
    assert_eq!(weekly["state"], "disabled");
    let builtin = scanned["skills"].as_array().unwrap().iter().find(|s| s["name"] == "skill-creator").unwrap().clone();
    assert_eq!((builtin["scope"].as_str(), builtin.get("removable")), (Some("system"), None));
    let bundled = client.request("skills/files", json!({"path": builtin["path"]})).await.unwrap();
    let paths: Vec<&str> = bundled["files"].as_array().unwrap().iter().filter_map(|f| f["path"].as_str()).collect();
    assert!(paths.contains(&"agents/grader.md") && paths.contains(&"eval-viewer/viewer.html"));
    assert!(paths.contains(&"LICENSE.txt") && paths.contains(&"UPSTREAM.md"));
    assert!(!paths.contains(&"scripts/run_eval.py"));
    let err = client.request("skills/remove", json!({"path": builtin["path"]})).await.unwrap_err();
    assert_eq!(error_kind(&err), "not_removable");
    client.request("skills/remove", json!({"path": target})).await.unwrap();
    assert!(!Path::new(&target).exists());

    // A skill in the repository's .claude is listed off by default; the same switch turns it
    // on, through opt_in in the user's file, and the catalog takes it.
    let theirs = write_skill(&work.join(".claude/skills"), "deploy", "Deploy.");
    let skill_of = |scanned: &Value, name: &str| scanned["skills"].as_array().unwrap().iter().find(|s| s["name"] == name).unwrap().clone();
    let scanned = client.request("skills/list", json!({"cwd": work})).await.unwrap();
    schema.check("SkillsListResult", &scanned);
    let deploy = skill_of(&scanned, "deploy");
    assert_eq!((deploy["state"].as_str(), deploy["default_enabled"].as_bool()), (Some("disabled"), Some(false)));
    let switched = client.request("skills/set_enabled", json!({"path": theirs, "enabled": true})).await.unwrap();
    assert!(std::fs::read_to_string(switched["file"].as_str().unwrap()).unwrap().contains("opt_in"));
    let scanned = client.request("skills/list", json!({"cwd": work})).await.unwrap();
    assert_eq!(skill_of(&scanned, "deploy")["state"], "active");

    // Changes made elsewhere reach the page: the command line removes the server, and its
    // credential goes with it.
    while rx.try_recv().is_ok() {}
    let (code, out) = run(&env, &work, &["mcp", "remove", "vault"]).await;
    assert_eq!(code, 0, "{out}");
    let changed = notification(&mut rx, "config/changed").await;
    assert!(changed["file"].as_str().unwrap().ends_with("config.toml"), "{changed}");
    notification(&mut rx, "mcp/changed").await;
    let listed = client.request("mcp/list", json!({"cwd": work})).await.unwrap();
    assert!(listed["servers"].as_array().unwrap().iter().all(|s| s["name"] != "vault"), "{listed:#}");
    assert!(saved_credentials(&env).is_empty());

    drop(client);
    assert_eq!(common::exit_code(&mut child).await, 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_session_records_its_servers_for_the_page() {
    let env = TestEnv::new("bypass-permissions", "").await;
    let work = project(&env, "repo");
    std::fs::write(
        env.home.path().join("config.toml"),
        format!(
            "{}\n[mcp_servers.vault]\ncommand = {:?}\n",
            std::fs::read_to_string(env.home.path().join("config.toml")).unwrap(),
            server_bin().display().to_string()
        ),
    )
    .unwrap();
    let (mut child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let demux = common::Demux::spawn(incoming);
    let created = client.request("session/new", json!({"cwd": work})).await.unwrap();
    let session = created["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);
    let turn = common::native_prompt(&client, &mut events, &session, "[[mock:mcp:vault:Orion]] go", false).await;
    assert_eq!(turn.end["type"], "task_completed", "{:#?}", turn.events);

    // An open session's view comes first; the cache keeps it for later.
    let listed = client.request("mcp/list", json!({"cwd": work})).await.unwrap();
    let vault = listed["servers"].as_array().unwrap().iter().find(|s| s["name"] == "vault").unwrap().clone();
    assert_eq!((vault["status"]["state"].as_str(), vault["status"]["source"].as_str()), (Some("ok"), Some("session")), "{vault:#}");
    assert_eq!(vault["status"]["sessions"], 1);
    client.request("session/close", json!({"session_id": session})).await.unwrap();
    let deadline = std::time::Instant::now() + Duration::from_secs(10);
    let cached = loop {
        let listed = client.request("mcp/list", json!({"cwd": work})).await.unwrap();
        let vault = listed["servers"].as_array().unwrap().iter().find(|s| s["name"] == "vault").unwrap().clone();
        if vault["status"]["source"] == "cache" || std::time::Instant::now() > deadline {
            break vault;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    };
    assert_eq!((cached["status"]["state"].as_str(), cached["status"]["source"].as_str()), (Some("ok"), Some("cache")), "{cached:#}");
    drop(client);
    common::exit_code(&mut child).await;
}
