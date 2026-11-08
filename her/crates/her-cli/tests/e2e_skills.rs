//! End-to-end: skills found by the real `her` binary, listed in the system prompt it sends,
//! and followed by the mock model with read and bash; `her skills` lists what was found.

mod common;

use common::TestEnv;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::path::PathBuf;
use std::process::Stdio;
use tokio::io::AsyncBufReadExt;
use tokio::io::AsyncWriteExt;
use tokio::io::BufReader;

fn write(path: &Path, text: &str) {
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    std::fs::write(path, text).unwrap();
}

/// The user's weekly-report skill with a format reference and a script, a copy in another
/// app's folder (not read unless `skills.other_apps`), a project skill, one in the project's
/// `.claude` (off by default) and a broken one.
fn install_skills(env: &TestEnv, work: &Path) -> PathBuf {
    let user = env.user_home();
    let own = env.home.path().join("skills");
    let skill = own.join("gen-report");
    write(
        &skill.join("SKILL.md"),
        "---\nname: gen-report\ndescription: >-\n  Generate the weekly report. Use when the user\n  \
         asks for a report.\n---\n# Weekly report\n\nRead `references/format.md` for the layout, \
         then run `scripts/gen.sh` from the project directory.\n",
    );
    write(&skill.join("references/format.md"), "Title line: WEEKLY-REPORT\n");
    write(&skill.join("scripts/gen.sh"), "echo 'WEEKLY-REPORT from the skill' > report.md\necho generated\n");
    let copy = user.join(".claude/skills/gen-report");
    write(
        &copy.join("SKILL.md"),
        "---\nname: gen-report\ndescription: Shadowed copy.\n---\nRun `scripts/other.sh`.\n",
    );
    write(&copy.join("scripts/other.sh"), "echo 'WRONG COPY' > report.md\n");
    write(
        &work.join(".agents/skills/lint/SKILL.md"),
        "---\nname: lint\ndescription: Lint rules for this repository.\n---\n",
    );
    write(
        &work.join(".claude/skills/deploy/SKILL.md"),
        "---\nname: deploy\ndescription: Deploy this repository.\n---\n",
    );
    write(&own.join("broken/SKILL.md"), "no header here\n");
    skill
}

/// Runs `her exec --output stream-json --ops-stdin` in `cwd`, approving every request; returns the exit
/// code, the events and how many approvals were asked for.
async fn exec_approving(env: &TestEnv, cwd: &Path, args: &[&str]) -> (i32, Vec<Value>, Vec<Value>) {
    let mut all = vec!["exec", "--output", "stream-json", "--ops-stdin"];
    all.extend_from_slice(args);
    let mut child = env
        .her_in(cwd, &all)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let mut stdin = child.stdin.take().unwrap();
    let mut lines = BufReader::new(child.stdout.take().unwrap()).lines();
    let mut events = Vec::new();
    let mut approvals = Vec::new();
    while let Some(line) = lines.next_line().await.unwrap() {
        let event: Value = serde_json::from_str(&line).unwrap();
        if event["type"] == "approval_requested" {
            approvals.push(event.clone());
            let op = json!({"type": "approval", "call_id": event["call_id"], "decision": {"type": "approve"}});
            stdin.write_all(format!("{op}\n").as_bytes()).await.unwrap();
            stdin.flush().await.unwrap();
        }
        events.push(event);
    }
    (common::exit_code(&mut child).await, events, approvals)
}

fn of_type<'a>(events: &'a [Value], kind: &str) -> Vec<&'a Value> {
    events.iter().filter(|e| e["type"] == kind).collect()
}

fn logged_system_prompt(file: &str) -> Value {
    std::fs::read_to_string(file)
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str::<Value>(line).unwrap())
        .find(|entry| entry["type"] == "system_prompt")
        .expect("a system_prompt entry")
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn exec_follows_a_skill_from_the_catalog() {
    let env = TestEnv::new("manual", "").await;
    let work = env.workspace("project");
    let skill = install_skills(&env, &work);

    let (code, events, approvals) =
        exec_approving(&env, &work, &["[[mock:skill:gen-report]] write this week's report"]).await;
    assert_eq!(code, 0, "{events:#?}");

    // The model read the SKILL.md and its reference, then ran the script: three tools, and
    // only the script needed an approval, even though manual mode asks for any change.
    let started = of_type(&events, "tool_started");
    let calls: Vec<(String, String)> = started
        .iter()
        .map(|e| (e["name"].as_str().unwrap().to_string(), e["args"].to_string()))
        .collect();
    assert_eq!(calls.len(), 3, "{calls:?}");
    assert_eq!(calls[0].0, "read");
    assert!(calls[0].1.contains(&skill.join("SKILL.md").display().to_string()), "{calls:?}");
    assert_eq!(calls[1].0, "read");
    assert!(calls[1].1.contains("references/format.md"));
    assert_eq!(calls[2].0, "bash");
    assert!(calls[2].1.contains(&skill.join("scripts/gen.sh").display().to_string()), "{calls:?}");
    assert_eq!(approvals.len(), 1);
    assert_eq!(approvals[0]["call_id"], started[2]["call_id"], "the script asked, the reads did not");
    let results = of_type(&events, "tool_completed");
    assert!(results[0]["result"]["content"][0]["text"].as_str().unwrap().contains("# Weekly report"));
    assert!(results[1]["result"]["content"][0]["text"].as_str().unwrap().contains("WEEKLY-REPORT"));
    assert_eq!(
        std::fs::read_to_string(work.join("report.md")).unwrap(),
        "WEEKLY-REPORT from the skill\n",
        "the user's own skill ran, not the other app's copy"
    );
    assert_eq!(of_type(&events, "task_completed")[0]["last_message"], "skill done");

    // A warning for the broken skill, once, when the session started.
    let warnings: Vec<&str> = of_type(&events, "warning").iter().filter_map(|w| w["message"].as_str()).collect();
    assert_eq!(warnings.len(), 1, "{warnings:?}");
    assert!(warnings[0].contains("broken") && warnings[0].contains("was not loaded"));

    // The catalog is in the system prompt the log keeps, with the skills of the session.
    let session_file = of_type(&events, "session_started")[0]["session_file"].as_str().unwrap().to_string();
    let logged = logged_system_prompt(&session_file);
    let text = logged["text"].as_str().unwrap();
    assert!(text.contains("<available_skills>"));
    let line = format!(
        "<skill name=\"gen-report\" path=\"{}\">Generate the weekly report. Use when the user asks for a \
         report.</skill>",
        skill.join("SKILL.md").display()
    );
    assert!(text.contains(&line), "{text}");
    assert!(text.find("<skill name=\"lint\"").unwrap() < text.find("<skill name=\"gen-report\"").unwrap());
    assert!(!text.contains("Shadowed copy.") && !text.contains("<skill name=\"deploy\""));
    let names: Vec<&str> = logged["skills"].as_array().unwrap().iter().filter_map(|s| s["name"].as_str()).collect();
    // The built-in skill is the user's to invoke, never in the catalog.
    assert_eq!(names, vec!["lint", "gen-report", "skill-creator"]);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn skills_can_be_turned_off_or_disabled() {
    let env = TestEnv::new("manual", "").await;
    let work = env.workspace("project");
    install_skills(&env, &work);

    let (code, events, _) = exec_approving(
        &env,
        &work,
        &["-c", "skills.enabled=false", "[[mock:skill:gen-report]] write the report"],
    )
    .await;
    assert_eq!(code, 0);
    assert_eq!(of_type(&events, "task_completed")[0]["last_message"], "no skill named gen-report");
    assert!(of_type(&events, "warning").is_empty(), "nothing is scanned when turned off");

    let (code, events, _) = exec_approving(
        &env,
        &work,
        &["-c", "skills.disabled=[\"gen-report\"]", "[[mock:skill:gen-report]] write the report"],
    )
    .await;
    assert_eq!(code, 0);
    assert_eq!(of_type(&events, "task_completed")[0]["last_message"], "no skill named gen-report");
    assert!(!work.join("report.md").exists());
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_skills_command_lists_layers_shadows_and_problems() {
    let env = TestEnv::new("manual", "").await;
    let work = env.workspace("project");
    let skill = install_skills(&env, &work);

    let output = env.her_in(&work, &["skills", "--all", "--json"]).output().await.unwrap();
    assert_eq!(output.status.code(), Some(0));
    let report: Value = serde_json::from_slice(&output.stdout).unwrap();
    let rows: Vec<(String, String, String)> = report["skills"]
        .as_array()
        .unwrap()
        .iter()
        .map(|s| {
            (
                s["name"].as_str().unwrap().to_string(),
                s["scope"].as_str().unwrap().to_string(),
                s["state"].as_str().unwrap().to_string(),
            )
        })
        .collect();
    let row = |name: &str, scope: &str, state: &str| (name.to_string(), scope.to_string(), state.to_string());
    assert_eq!(
        rows,
        vec![
            row("lint", "project", "active"),
            row("deploy", "project", "disabled"),
            row("broken", "user", "invalid"),
            row("gen-report", "user", "active"),
            row("skill-creator", "system", "active"),
        ]
    );
    assert_eq!(report["skills"][1]["default_enabled"], false, "the project's .claude is off by default");
    assert_eq!(report["skills"][0]["default_enabled"], true);
    assert_eq!(report["not_read"], json!([env.user_home().join(".claude/skills")]));
    assert_eq!(report["catalog"]["listed"], 2);
    assert!(report["catalog"]["tokens"].as_u64().unwrap() > 0);
    assert_eq!(report["catalog"]["budget"], 4000, "2% of the mock model's 200,000-token window");
    assert_eq!(report["warnings"].as_array().unwrap().len(), 1);

    // The text form: active skills, paths short, and a hint at what --all would add.
    let output = env.her_in(&work, &["skills"]).output().await.unwrap();
    let text = String::from_utf8_lossy(&output.stdout);
    assert!(text.contains("project  lint"), "{text}");
    assert!(text.contains(".agents/skills/lint/SKILL.md"), "{text}");
    assert!(text.contains("gen-report/SKILL.md"), "{text}");
    assert!(text.contains("2 more shadowed, disabled or broken"), "{text}");
    assert!(text.contains("catalog: 2 skills"), "{text}");
    assert!(text.contains("not read: ~/.claude/skills (skills.other_apps is off)"), "{text}");
    let output = env.her_in(&work, &["skills", "--all"]).output().await.unwrap();
    let text = String::from_utf8_lossy(&output.stdout);
    assert!(text.contains("off by default; her skills enable .claude/skills/deploy turns it on"), "{text}");
    assert!(text.contains("not loaded: no header"), "{text}");

    // Reading the other apps' folders brings the copy in, behind the user's own.
    let output = env.her_in(&work, &["-c", "skills.other_apps=true", "skills", "--all"]).output().await.unwrap();
    let text = String::from_utf8_lossy(&output.stdout);
    assert!(text.contains(&format!("shadowed by {}", skill.join("SKILL.md").display())), "{text}");
    assert!(!text.contains("not read"), "{text}");

    // Turning the .claude skill on writes opt_in; it then joins the catalog.
    let output = env.her_in(&work, &["skills", "enable", ".claude/skills/deploy"]).output().await.unwrap();
    assert_eq!(output.status.code(), Some(0), "{}", String::from_utf8_lossy(&output.stderr));
    let config = std::fs::read_to_string(env.home.path().join("config.toml")).unwrap();
    assert!(config.contains("opt_in = [") && config.contains(".claude/skills/deploy"), "{config}");
    let output = env.her_in(&work, &["skills"]).output().await.unwrap();
    let text = String::from_utf8_lossy(&output.stdout);
    assert!(text.contains("project  deploy") && text.contains("catalog: 3 skills"), "{text}");
    let output = env.her_in(&work, &["skills", "disable", ".claude/skills/deploy"]).output().await.unwrap();
    assert_eq!(output.status.code(), Some(0));
    let config = std::fs::read_to_string(env.home.path().join("config.toml")).unwrap();
    assert!(!config.contains("opt_in") && !config.contains("deploy"), "{config}");

    // The removed setting says what replaced it.
    let output = env.her_in(&work, &["-c", "skills.dirs=[\".agents\"]", "skills"]).output().await.unwrap();
    assert_ne!(output.status.code(), Some(0));
    assert!(String::from_utf8_lossy(&output.stderr).contains("skills.dirs was removed"));

    let output = env.her_in(&work, &["status", "--json"]).output().await.unwrap();
    let status: Value = serde_json::from_slice(&output.stdout).unwrap();
    assert_eq!(status["skills"]["active"], 3);
    let output = env.her_in(&work, &["doctor", "--json"]).output().await.unwrap();
    let doctor: Value = serde_json::from_slice(&output.stdout).unwrap();
    let check = doctor["checks"].as_array().unwrap().iter().find(|c| c["name"] == "skills").unwrap().clone();
    assert_eq!(check["ok"], true, "a broken skill does not fail the setup");
    assert!(check["detail"].as_str().unwrap().contains("1 failed to load"));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn exec_runs_a_skill_the_user_mentions() {
    let env = TestEnv::new("manual", "").await;
    let work = env.workspace("project");
    let skill = install_skills(&env, &work);
    let typed = "请用 /skill:gen-report [[mock:invoked]] 写本周的报告";

    let (code, events, approvals) = exec_approving(&env, &work, &[typed]).await;
    assert_eq!(code, 0, "{events:#?}");
    // The instructions came with the message: no read of SKILL.md, straight to the steps.
    let calls: Vec<(String, String)> = of_type(&events, "tool_started")
        .iter()
        .map(|e| (e["name"].as_str().unwrap().to_string(), e["args"].to_string()))
        .collect();
    assert_eq!(calls.len(), 2, "{calls:?}");
    assert_eq!(calls[0].0, "read");
    assert!(calls[0].1.contains(&skill.join("references/format.md").display().to_string()), "{calls:?}");
    assert_eq!(calls[1].0, "bash");
    assert!(calls[1].1.contains(&skill.join("scripts/gen.sh").display().to_string()), "{calls:?}");
    assert_eq!(approvals.len(), 1, "only the script asks");
    assert_eq!(std::fs::read_to_string(work.join("report.md")).unwrap(), "WEEKLY-REPORT from the skill\n");
    assert_eq!(of_type(&events, "task_completed")[0]["last_message"], "invoked done");

    // The message is the user's words; the skill goes beside it.
    let user = of_type(&events, "message_completed")
        .into_iter()
        .find(|e| e["message"]["role"] == "user")
        .unwrap()
        .clone();
    assert_eq!(user["message"]["content"][0]["text"], typed);
    assert_eq!(user["skills"], json!([{"name": "gen-report", "path": skill.join("SKILL.md")}]));

    // The session list shows the words too.
    let output = env.her_in(&work, &["sessions", "--json"]).output().await.unwrap();
    let sessions: Value = serde_json::from_slice(&output.stdout).unwrap();
    assert_eq!(sessions[0]["first_message"], typed);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn exec_loads_only_what_is_mentioned_explicitly() {
    let env = TestEnv::new("manual", "").await;
    let work = env.workspace("project");
    install_skills(&env, &work);
    write(
        &env.home.path().join("skills/commit-style/SKILL.md"),
        "---\nname: commit-style\ndescription: Commit messages.\n---\nTag it.\n",
    );
    let (code, events, _) = exec_approving(
        &env,
        &work,
        &["[[mock:context]] /skill:gen-report 和 /skill:commit-style，另外 /gen-report、`/skill:lint`、src/skill:lint 都不算"],
    )
    .await;
    assert_eq!(code, 0, "{events:#?}");
    assert_eq!(
        of_type(&events, "task_completed")[0]["last_message"],
        "skills in context: gen-report, commit-style",
        "two blocks, in the order of the mentions"
    );
    let (code, events, _) = exec_approving(&env, &work, &["[[mock:context]] /skill:gen-reprot"]).await;
    assert_eq!(code, 0);
    assert_eq!(of_type(&events, "task_completed")[0]["last_message"], "skills in context: none");
    let warnings: Vec<&str> = of_type(&events, "warning").iter().filter_map(|w| w["message"].as_str()).collect();
    assert!(warnings.contains(&"no skill named 'gen-reprot'; \"/skill:gen-reprot\" was sent as text"), "{warnings:?}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_skill_in_use_survives_compaction() {
    let env = TestEnv::new("manual", "").await;
    let work = env.workspace("project");
    install_skills(&env, &work);
    let (code, events, _) = exec_approving(&env, &work, &["[[mock:skill:gen-report]] write this week's report"]).await;
    assert_eq!(code, 0, "{events:#?}");
    let session_id = of_type(&events, "session_started")[0]["session_id"].as_str().unwrap().to_string();
    let session_file = of_type(&events, "session_started")[0]["session_file"].as_str().unwrap().to_string();

    let (code, events, _) = exec_approving(&env, &work, &["--resume", &session_id, "/compact"]).await;
    assert_eq!(code, 0, "{events:#?}");
    let completed = of_type(&events, "compaction_completed");
    assert_eq!(completed.len(), 1);
    assert_eq!(completed[0]["kept_skills"], json!(["gen-report"]), "read with the read tool, so in use");
    let logged = std::fs::read_to_string(&session_file).unwrap();
    let entry: Value = logged
        .lines()
        .map(|l| serde_json::from_str::<Value>(l).unwrap())
        .find(|e| e["type"] == "compaction")
        .unwrap();
    assert!(entry["skills"][0]["content"].as_str().unwrap().contains("# Weekly report"));

    // The model sees the instructions once after the compaction, and resume brings them back.
    let (code, events, _) = exec_approving(&env, &work, &["--resume", &session_id, "[[mock:context]]"]).await;
    assert_eq!(code, 0);
    assert_eq!(of_type(&events, "task_completed")[0]["last_message"], "skills in context: gen-report");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn slash_compact_ends_even_with_nothing_to_compact() {
    let env = TestEnv::new("manual", "").await;
    let work = env.workspace("project");
    let (code, events, _) = exec_approving(&env, &work, &["/compact"]).await;
    assert_eq!(code, 0);
    let warnings: Vec<&str> = of_type(&events, "warning").iter().filter_map(|w| w["message"].as_str()).collect();
    assert_eq!(warnings, vec!["nothing to compact"]);
    assert_eq!(env.mock.stats().requests.load(std::sync::atomic::Ordering::Relaxed), 0);
}
