//! Skills in the kernel: discovery into the frozen system prompt and the session log, reading
//! a skill without approval, protection of skill directories, and resume.

use her_core::Config;
use her_core::HerHome;
use her_core::JsonlStore;
use her_core::Kernel;
use her_core::KernelDeps;
use her_core::ResumeOptions;
use her_core::SessionRef;
use her_core::StartOptions;
use her_core::TaskEnd;
use her_core::config::SkillsSettings;
use her_llm::Catalog;
use her_llm::Message;
use her_llm::ModelRegistry;
use her_llm::RegistryOptions;
use her_llm::ThinkingLevel;
use her_llm::UserContent;
use her_llm::api::scripted::Script;
use her_llm::api::scripted::ScriptedApi;
use her_protocol::EventMsg;
use her_protocol::PermissionMode;
use her_tools::BashTool;
use her_tools::LocalEnvironment;
use her_tools::builtin_registry;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use tempfile::TempDir;

struct Env {
    base: PathBuf,
    work: PathBuf,
    user: PathBuf,
    home: HerHome,
    _dir: TempDir,
}

fn env() -> Env {
    let dir = tempfile::tempdir().unwrap();
    let base = std::fs::canonicalize(dir.path()).unwrap();
    let work = base.join("work");
    std::fs::create_dir_all(&work).unwrap();
    let user = base.join("user");
    std::fs::create_dir_all(&user).unwrap();
    let home = HerHome::new(base.join("her-home")).with_user_home(&user);
    home.ensure().unwrap();
    Env {
        base,
        work,
        user,
        home,
        _dir: dir,
    }
}

/// Writes `<dir>/SKILL.md` and returns its path.
fn write_skill(dir: &Path, header: &str, body: &str) -> PathBuf {
    std::fs::create_dir_all(dir).unwrap();
    let file = dir.join("SKILL.md");
    std::fs::write(&file, format!("---\n{header}\n---\n{body}")).unwrap();
    file
}

fn deps(api: &Arc<ScriptedApi>, env: &Env, tools: &[&str]) -> KernelDeps {
    let mut registry = ModelRegistry::new(Catalog::default(), RegistryOptions::default());
    registry.register_api(api.clone());
    registry.insert_provider(ScriptedApi::provider());
    let names: Vec<String> = tools.iter().map(|t| t.to_string()).collect();
    KernelDeps {
        llm: Arc::new(registry),
        tools: Arc::new(builtin_registry(BashTool::default()).subset(&names).unwrap()),
        env: Arc::new(LocalEnvironment::new(env.work.clone())),
        store: Arc::new(JsonlStore::new(env.home.sessions_dir())),
        hooks: vec![],
        mcp_servers: Vec::new(),
        browser_pool: None,
        docs: None,
        decks: None,
        words: None,
        routines: None,
        connectors: None,
        mesh: None,
    }
}

const ALL_TOOLS: &[&str] = &["read", "write", "edit", "bash"];

async fn start(env: &Env, api: &Arc<ScriptedApi>, mode: PermissionMode, config: Config, tools: &[&str]) -> Kernel {
    let options = StartOptions {
        model: "test/scripted".into(),
        thinking: ThinkingLevel::Off,
        permission_mode: mode,
        cwd: env.work.clone(),
        home: env.home.clone(),
        config: Arc::new(config),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot: None,
    };
    Kernel::start(options, deps(api, env, tools)).await.unwrap()
}

fn prompt(text: &str) -> Vec<UserContent> {
    vec![UserContent::Text { text: text.into() }]
}

fn tool_results(context: &her_llm::Context) -> Vec<String> {
    context
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::ToolResult(r) => Some(r.text_content()),
            _ => None,
        })
        .collect()
}

/// The system_prompt entry of a session log.
fn logged_system_prompt(file: &Path) -> Value {
    std::fs::read_to_string(file)
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str::<Value>(line).unwrap())
        .find(|entry| entry["type"] == "system_prompt")
        .expect("a system_prompt entry")
}

#[tokio::test]
async fn the_model_reads_a_listed_skill_without_approval_and_resume_keeps_the_catalog() {
    let env = env();
    write_skill(&env.work.join(".agents/skills/lint"), "name: lint\ndescription: Lint rules for this repo.", "");
    let pdf = write_skill(
        &env.home.skills_dir().join("pdf-tools"),
        "name: pdf-tools\ndescription: >-\n  Extract text and tables\n  from PDF files.",
        "Read `references/formats.md` first.\n",
    );
    std::fs::create_dir_all(env.home.skills_dir().join("pdf-tools/references")).unwrap();
    std::fs::write(env.home.skills_dir().join("pdf-tools/references/formats.md"), "FORMAT-MARKER\n").unwrap();
    write_skill(&env.work.join(".claude/skills/pdf-tools"), "name: pdf-tools\ndescription: Shadowed copy.", "");
    write_skill(
        &env.home.skills_dir().join("deploy"),
        "name: deploy\ndescription: Deploy.\ndisable-model-invocation: true",
        "",
    );
    write_skill(&env.home.skills_dir().join("broken"), "name: broken", "");
    // Another app's folder in the home directory is not read (skills.other_apps is off).
    write_skill(&env.user.join(".agents/skills/elsewhere"), "name: elsewhere\ndescription: Another app's.", "");

    let api = ScriptedApi::new();
    api.push(Script::tool_call("call_1", "read", json!({ "path": pdf })));
    api.push(Script::tool_call(
        "call_2",
        "read",
        json!({ "path": env.home.skills_dir().join("pdf-tools/references/formats.md") }),
    ));
    api.push(Script::text("done"));
    // No deferred group, so what the model gets is the frozen prompt alone, as logged.
    let no_search = || Config { web_search: her_core::config::WebSearchSettings { enabled: false, ..Default::default() }, ..Config::default() };
    let mut kernel = start(&env, &api, PermissionMode::Manual, no_search(), ALL_TOOLS).await;
    let result = kernel.run(prompt("extract the tables from report.pdf")).await.unwrap();
    assert_eq!(result.end, TaskEnd::Completed { last_message: Some("done".into()) });
    assert!(
        !result.events.iter().any(|e| matches!(e.msg, EventMsg::ApprovalRequested { .. })),
        "reading a skill outside the workspace needs no approval, even in manual mode"
    );
    let warnings: Vec<String> = result
        .events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::Warning { message, .. } => Some(message.clone()),
            _ => None,
        })
        .collect();
    assert_eq!(warnings.len(), 1, "{warnings:?}");
    assert!(warnings[0].contains("broken") && warnings[0].contains("no description"), "{warnings:?}");

    let requests = api.requests();
    let system = requests[0].system.clone();
    let catalog_at = system.find("<available_skills>").expect("a catalog");
    assert!(catalog_at < system.find("\n<environment>\n").unwrap(), "the catalog comes before the environment");
    let lint = system.find("<skill name=\"lint\"").unwrap();
    let pdf_line = format!(
        "<skill name=\"pdf-tools\" path=\"{}\">Extract text and tables from PDF files.</skill>",
        pdf.display()
    );
    assert!(system.contains(&pdf_line), "{system}");
    assert!(lint < system.find(&pdf_line).unwrap(), "project skills first");
    assert!(!system.contains("Shadowed copy.") && !system.contains("name=\"deploy\"") && !system.contains("broken"));
    assert!(!system.contains("elsewhere"), "{system}");
    let results = tool_results(&requests[2]);
    assert!(results[0].contains("Read `references/formats.md` first."));
    assert!(results[1].contains("FORMAT-MARKER"));

    // The log keeps the session's skills, those only the user may invoke included.
    let logged = logged_system_prompt(kernel.session_file());
    assert!(system.starts_with(logged["text"].as_str().unwrap()), "the catalog of deferred tools follows it");
    let skills: Vec<(String, String, bool)> = logged["skills"]
        .as_array()
        .unwrap()
        .iter()
        .map(|s| {
            (
                s["name"].as_str().unwrap().to_string(),
                s["scope"].as_str().unwrap().to_string(),
                s["model_invocable"].as_bool().unwrap(),
            )
        })
        .collect();
    assert_eq!(
        skills,
        vec![
            ("lint".to_string(), "project".to_string(), true),
            ("deploy".to_string(), "user".to_string(), false),
            ("pdf-tools".to_string(), "user".to_string(), true),
            ("skill-creator".to_string(), "system".to_string(), false),
        ],
        "priority order: layers first, then paths within a root"
    );

    // A skill added later shows up in new sessions only: the resumed one keeps its prompt.
    write_skill(&env.home.skills_dir().join("late"), "name: late\ndescription: Added later.", "");
    let session_id = kernel.session_id().to_string();
    kernel.shutdown().await;
    let api = ScriptedApi::new();
    api.push(Script::text("resumed"));
    let options = ResumeOptions {
        session: SessionRef::Id(her_core::short_id(&session_id).to_string()),
        model: None,
        thinking: None,
        permission_mode: None,
        home: env.home.clone(),
        config: Arc::new(no_search()),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot_name: None,
    };
    let mut kernel = Kernel::resume(options, deps(&api, &env, ALL_TOOLS)).await.unwrap();
    kernel.run(prompt("again")).await.unwrap();
    assert_eq!(api.requests()[0].system, system);
    let fresh = ScriptedApi::new();
    fresh.push(Script::text("new"));
    let mut kernel = start(&env, &fresh, PermissionMode::Manual, Config::default(), ALL_TOOLS).await;
    kernel.run(prompt("hello")).await.unwrap();
    assert!(fresh.requests()[0].system.contains("<skill name=\"late\""));
}

#[tokio::test]
async fn skill_directories_are_protected_paths() {
    let env = env();
    let shared = env.base.join("shared-skills");
    std::fs::create_dir_all(&shared).unwrap();
    let config = Config {
        skills: SkillsSettings {
            paths: vec![shared.display().to_string()],
            ..SkillsSettings::default()
        },
        ..Config::default()
    };
    let api = ScriptedApi::new();
    api.push(
        Script::tool_call("call_1", "write", json!({"path": ".claude/skills/new/SKILL.md", "content": "x"}))
            .then_tool_call("call_2", "write", json!({"path": "notes.txt", "content": "fine"}))
            .then_tool_call("call_3", "write", json!({"path": shared.join("x/SKILL.md"), "content": "x"}))
            .then_tool_call("call_4", "write", json!({"path": ".agents/skills/y/SKILL.md", "content": "x"})),
    );
    api.push(Script::text("ok"));
    // accept-edits writes in the workspace without asking, except protected paths; with
    // nobody to answer, asking means a refusal.
    let mut kernel = start(&env, &api, PermissionMode::AcceptEdits, config, ALL_TOOLS).await;
    kernel.run(prompt("write some files")).await.unwrap();
    assert!(env.work.join("notes.txt").exists());
    assert!(!env.work.join(".claude/skills/new/SKILL.md").exists());
    assert!(!shared.join("x/SKILL.md").exists());
    assert!(!env.work.join(".agents/skills/y/SKILL.md").exists());
    let results = tool_results(&api.requests()[1]);
    let refused: Vec<bool> = results.iter().map(|r| r.contains("nobody can approve it")).collect();
    assert_eq!(refused, vec![true, false, true, true], "{results:?}");
}

#[tokio::test]
async fn no_catalog_when_turned_off_or_unreadable() {
    let env = env();
    write_skill(&env.home.skills_dir().join("x"), "name: x\ndescription: X.", "");
    let system_of = |config: Config, tools: &'static [&'static str]| {
        let env = &env;
        async move {
            let api = ScriptedApi::new();
            api.push(Script::text("hi"));
            let mut kernel = start(env, &api, PermissionMode::Manual, config, tools).await;
            kernel.run(prompt("hello")).await.unwrap();
            api.requests()[0].system.clone()
        }
    };
    assert!(system_of(Config::default(), ALL_TOOLS).await.contains("with the read tool"));
    let off = Config {
        skills: SkillsSettings {
            enabled: false,
            ..SkillsSettings::default()
        },
        ..Config::default()
    };
    assert!(!system_of(off, ALL_TOOLS).await.contains("<available_skills>"));
    assert!(system_of(Config::default(), &["bash"]).await.contains("read its SKILL.md with bash"));
    assert!(!system_of(Config::default(), &["write", "edit"]).await.contains("<available_skills>"));
}

fn user_texts(context: &her_llm::Context) -> Vec<String> {
    context
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::User(u) => Some(u.text_content()),
            _ => None,
        })
        .collect()
}

/// The text items of each user message.
fn user_parts(context: &her_llm::Context) -> Vec<Vec<String>> {
    context
        .messages
        .iter()
        .filter_map(|m| match m {
            Message::User(u) => Some(
                u.content
                    .iter()
                    .filter_map(|c| match c {
                        UserContent::Text { text } => Some(text.clone()),
                        _ => None,
                    })
                    .collect(),
            ),
            _ => None,
        })
        .collect()
}

fn warnings(events: &[her_protocol::Event]) -> Vec<String> {
    events
        .iter()
        .filter_map(|e| match &e.msg {
            EventMsg::Warning { message, .. } => Some(message.clone()),
            _ => None,
        })
        .collect()
}

/// Collects events until the task ends.
async fn until_task_end(kernel: &mut Kernel) -> Vec<her_protocol::Event> {
    let mut events = Vec::new();
    while let Some(event) = kernel.next_event().await {
        let end = event.msg.is_task_end();
        events.push(event);
        if end {
            break;
        }
    }
    events
}

#[tokio::test]
async fn mentions_anywhere_load_each_skill_once_and_keep_the_users_words() {
    let env = env();
    let deploy = write_skill(
        &env.home.skills_dir().join("deploy"),
        "name: deploy\ndescription: Deploy the service.\ndisable-model-invocation: true",
        "# Deploy\n\nRun `scripts/ship.sh`.\n",
    );
    let review = write_skill(&env.home.skills_dir().join("review"), "name: review\ndescription: Review.", "Check it twice.\n");
    let api = ScriptedApi::new();
    api.push(Script::text("deployed"));
    let mut kernel = start(&env, &api, PermissionMode::Manual, Config::default(), ALL_TOOLS).await;
    let typed = "请先按 /skill:review 检查，再用/skill:deploy发到 staging；/skill:review 之后再看一遍";
    let result = kernel.run(prompt(typed)).await.unwrap();
    assert_eq!(result.end, TaskEnd::Completed { last_message: Some("deployed".into()) });
    assert!(warnings(&result.events).is_empty());

    // The model gets each skill's instructions once, in the order of the first mention, then
    // the user's words as written.
    let request = &api.requests()[0];
    assert!(!request.system.contains("name=\"deploy\""), "only the user may use it");
    let block = |path: &Path, name: &str, body: &str| {
        format!(
            "<skill name=\"{name}\">\nRelative paths in this skill are relative to {}.\n\n{body}\n</skill>",
            path.parent().unwrap().display()
        )
    };
    assert_eq!(
        user_parts(request),
        vec![vec![
            block(&review, "review", "Check it twice."),
            block(&deploy, "deploy", "# Deploy\n\nRun `scripts/ship.sh`."),
            typed.to_string(),
        ]]
    );

    // Clients see the user's words and the skills.
    let uses = vec![
        her_protocol::SkillUse {
            name: "review".into(),
            path: review.clone(),
        },
        her_protocol::SkillUse {
            name: "deploy".into(),
            path: deploy.clone(),
        },
    ];
    let (message, skills) = result
        .events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::MessageCompleted {
                message: Message::User(user),
                skills,
                ..
            } => Some((user.text_content(), skills.clone())),
            _ => None,
        })
        .unwrap();
    assert_eq!((message.as_str(), &skills), (typed, &uses));
    let her_protocol::api::TranscriptItem::Message {
        message: Message::User(user),
        skills,
        ..
    } = &kernel.transcript()[0]
    else {
        panic!("a user message first");
    };
    assert_eq!((user.text_content().as_str(), skills), (typed, &uses));

    // The log keeps the words and the instructions apart.
    let logged = std::fs::read_to_string(kernel.session_file()).unwrap();
    let entry: Value = logged
        .lines()
        .map(|l| serde_json::from_str::<Value>(l).unwrap())
        .find(|e| e["type"] == "message" && e["message"]["role"] == "user")
        .unwrap();
    assert_eq!(entry["message"]["content"][0]["text"], typed);
    assert_eq!(entry["skills"][0]["name"], "review");
    assert_eq!(entry["skills"][1]["content"], block(&deploy, "deploy", "# Deploy\n\nRun `scripts/ship.sh`."));
}

#[tokio::test]
async fn only_explicit_mentions_of_the_users_own_input_load_skills() {
    let env = env();
    let deploy = write_skill(&env.home.skills_dir().join("deploy"), "name: deploy\ndescription: Deploy.", "Body.\n");
    let api = ScriptedApi::new();
    for _ in 0..6 {
        api.push(Script::text("ok"));
    }
    let mut kernel = start(&env, &api, PermissionMode::Manual, Config::default(), ALL_TOOLS).await;
    kernel
        .submit(her_protocol::Op::Prompt {
            input: prompt("/skill:deploy from another agent"),
            mode: her_protocol::PromptMode::Steer,
            commands: false,
        })
        .await
        .unwrap();
    until_task_end(&mut kernel).await;
    for typed in ["/deploy now", "看看 /deploy 接口为什么 404", "write `/skill:deploy` in the docs"] {
        let result = kernel.run(prompt(typed)).await.unwrap();
        assert!(warnings(&result.events).is_empty(), "{typed}");
    }
    let result = kernel.run(prompt("/skill:deplyo now")).await.unwrap();
    assert_eq!(warnings(&result.events), ["no skill named 'deplyo'; \"/skill:deplyo\" was sent as text"]);
    std::fs::remove_file(&deploy).unwrap();
    let result = kernel.run(prompt("/skill:deploy now")).await.unwrap();
    let warned = warnings(&result.events);
    assert_eq!(warned.len(), 1, "{warned:?}");
    assert!(warned[0].starts_with("cannot read skill 'deploy'") && warned[0].ends_with("\"/skill:deploy\" was sent as text"));
    assert_eq!(
        user_texts(&api.requests()[5]),
        vec![
            "/skill:deploy from another agent",
            "/deploy now",
            "看看 /deploy 接口为什么 404",
            "write `/skill:deploy` in the docs",
            "/skill:deplyo now",
            "/skill:deploy now"
        ],
        "every message goes to the model as written, without instructions"
    );
}

#[tokio::test]
async fn slash_compact_runs_as_a_task_even_with_nothing_to_compact() {
    let env = env();
    let api = ScriptedApi::new();
    api.push(Script::text("first answer"));
    api.push(Script::text("## Summary\nThe user said hi."));
    let mut kernel = start(&env, &api, PermissionMode::Manual, Config::default(), ALL_TOOLS).await;
    let nothing = kernel.run(prompt("/compact")).await.unwrap();
    assert!(matches!(nothing.end, TaskEnd::Completed { .. }));
    let warned = |events: &[her_protocol::Event]| {
        events
            .iter()
            .any(|e| matches!(&e.msg, EventMsg::Warning { message, .. } if message == "nothing to compact"))
    };
    assert!(warned(&nothing.events));
    assert!(api.requests().is_empty(), "no model request");

    kernel.run(prompt("hi")).await.unwrap();
    let compacted = kernel.run(prompt("/compact")).await.unwrap();
    assert!(matches!(compacted.end, TaskEnd::Completed { .. }));
    assert!(compacted.events.iter().any(|e| matches!(e.msg, EventMsg::CompactionCompleted { .. })));
    assert!(!warned(&compacted.events));
    assert_eq!(api.requests().len(), 2, "the answer and the summary");
    assert!(
        !user_texts(&api.requests()[1]).iter().any(|t| t.contains("/compact")),
        "the command itself never reaches the model"
    );
}

fn kept_skills(events: &[her_protocol::Event]) -> Vec<String> {
    events
        .iter()
        .find_map(|e| match &e.msg {
            EventMsg::CompactionCompleted { kept_skills, .. } => Some(kept_skills.clone()),
            _ => None,
        })
        .expect("a compaction")
}

#[tokio::test]
async fn compaction_keeps_the_skills_in_use_and_resume_rebuilds_them() {
    let env = env();
    let deploy = write_skill(
        &env.home.skills_dir().join("deploy"),
        "name: deploy\ndescription: Deploy.",
        "Ship with DEPLOY-STEPS.\n",
    );
    let review = write_skill(
        &env.home.skills_dir().join("review"),
        "name: review\ndescription: Review.",
        "Check REVIEW-V1.\n",
    );
    write_skill(&env.home.skills_dir().join("unused"), "name: unused\ndescription: Unused.", "UNUSED-BODY\n");
    let api = ScriptedApi::new();
    api.push(Script::text("deploying"));
    api.push(Script::tool_call("call_1", "read", json!({ "path": review })));
    api.push(Script::text("reviewed"));
    api.push(Script::text("## Summary\nDeployed and reviewed."));
    api.push(Script::text("ok"));
    let mut kernel = start(&env, &api, PermissionMode::Manual, Config::default(), ALL_TOOLS).await;
    kernel.run(prompt("/skill:deploy staging")).await.unwrap();
    kernel.run(prompt("check the code")).await.unwrap();
    let compacted = kernel.run(prompt("/compact")).await.unwrap();
    assert_eq!(kept_skills(&compacted.events), vec!["review", "deploy"], "most recently used first");
    kernel.run(prompt("go on")).await.unwrap();

    let after = &api.requests()[4];
    let texts = user_texts(after);
    assert_eq!(texts.len(), 5, "{texts:?}");
    assert_eq!(&texts[..2], &["/skill:deploy staging", "check the code"], "kept as written");
    let skills = &texts[2];
    assert!(skills.starts_with("These skills were in use before the conversation was compacted."));
    let (at_review, at_deploy) = (skills.find("Check REVIEW-V1.").unwrap(), skills.find("Ship with DEPLOY-STEPS.").unwrap());
    assert!(at_review < at_deploy);
    assert!(skills.contains(&format!(
        "<skill name=\"review\">\nRelative paths in this skill are relative to {}.",
        review.parent().unwrap().display()
    )));
    assert!(!skills.contains("UNUSED-BODY"));
    assert!(texts[3].starts_with("<conversation_summary>"));
    assert_eq!(texts[4], "go on");

    // Resume rebuilds the same context from the log.
    let session_id = kernel.session_id().to_string();
    kernel.shutdown().await;
    let api = ScriptedApi::new();
    api.push(Script::text("again"));
    api.push(Script::text("## Summary\nStill going."));
    let options = ResumeOptions {
        session: SessionRef::Id(session_id),
        model: None,
        thinking: None,
        permission_mode: None,
        home: env.home.clone(),
        config: Arc::new(Config::default()),
        project_root: None,
        trusted: false,
        interactive_approvals: false,
        bot_name: None,
    };
    let mut kernel = Kernel::resume(options, deps(&api, &env, ALL_TOOLS)).await.unwrap();
    kernel.run(prompt("and now")).await.unwrap();
    let resumed = &api.requests()[0];
    assert_eq!(&resumed.messages[..after.messages.len()], &after.messages[..]);

    // The next compaction keeps them, read again: the new version of a changed file, the last
    // copy of a deleted one.
    write_skill(&env.home.skills_dir().join("review"), "name: review\ndescription: Review.", "Check REVIEW-V2.\n");
    std::fs::remove_file(&deploy).unwrap();
    let compacted = kernel.run(prompt("/compact")).await.unwrap();
    assert_eq!(kept_skills(&compacted.events), vec!["review", "deploy"]);
    let logged = std::fs::read_to_string(kernel.session_file()).unwrap();
    let last: Value = logged
        .lines()
        .rev()
        .map(|l| serde_json::from_str::<Value>(l).unwrap())
        .find(|e| e["type"] == "compaction")
        .unwrap();
    let contents: Vec<&str> = last["skills"].as_array().unwrap().iter().map(|s| s["content"].as_str().unwrap()).collect();
    assert!(contents[0].contains("Check REVIEW-V2.") && !contents[0].contains("REVIEW-V1"));
    assert!(contents[1].contains("Ship with DEPLOY-STEPS."));
}

#[tokio::test]
async fn queued_input_comes_back_as_written() {
    let env = env();
    write_skill(&env.home.skills_dir().join("deploy"), "name: deploy\ndescription: Deploy.", "Body.\n");
    let api = ScriptedApi::new();
    api.push(Script::tool_call("call_1", "bash", json!({"command": "sleep 30"})));
    let mut kernel = start(&env, &api, PermissionMode::BypassPermissions, Config::default(), ALL_TOOLS).await;
    kernel.submit(her_protocol::Op::prompt("wait")).await.unwrap();
    loop {
        let event = kernel.next_event().await.unwrap();
        if matches!(event.msg, EventMsg::ToolRunning { .. }) {
            break;
        }
    }
    kernel
        .submit(her_protocol::Op::Prompt {
            input: prompt("/skill:deploy later"),
            mode: her_protocol::PromptMode::FollowUp,
            commands: true,
        })
        .await
        .unwrap();
    kernel.submit(her_protocol::Op::Interrupt).await.unwrap();
    let returned = loop {
        let event = kernel.next_event().await.unwrap();
        if let EventMsg::TaskInterrupted { returned_input, .. } = event.msg {
            break returned_input;
        }
    };
    let texts: Vec<String> = returned.iter().map(|m| m.text_content()).collect();
    assert_eq!(texts, vec!["/skill:deploy later"]);
}
