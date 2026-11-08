//! System prompt assembly. The prompt is built once per session and then frozen.

use her_tools::Environment;
use her_tools::ToolRegistry;
use std::io;
use std::path::Path;

use crate::config::Config;
use crate::instructions::InstructionFile;

/// The built-in base sections (bot-design §2.3): a session's and a bot's. The sections both
/// share are in `prompts/shared/` and come in where a base names them.
const BASE_PROMPT: &str = include_str!("prompts/base.md");
const BOT_PROMPT: &str = include_str!("prompts/bot.md");
const SHARED: [(&str, &str); 2] = [
    ("{{numbers_and_data}}", include_str!("prompts/shared/numbers_and_data.md")),
    ("{{acting_with_care}}", include_str!("prompts/shared/acting_with_care.md")),
];
/// How to write files and commands for the desktop app, which turns them into controls
/// (content-actions-design §9.1).
pub const APP_PROMPT: &str = include_str!("prompts/app.md");
/// The same for a bot, which hands over files with `send_to_user` instead of linking them
/// (bot-design §3.12).
const BOT_APP_PROMPT: &str = include_str!("prompts/app_bot.md");

/// Which base section a system prompt starts with; the sections after it are the same.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum PromptProfile {
    /// A session, and a sub-agent: `prompts/base.md`, or `system_prompt_file`.
    Session,
    /// A bot's session: `prompts/bot.md`, or `bot_prompt_file`, given the bot's name.
    Bot { name: String },
}

pub struct PromptInputs<'a> {
    pub profile: &'a PromptProfile,
    pub config: &'a Config,
    pub tools: &'a ToolRegistry,
    pub env: &'a dyn Environment,
    pub instructions: &'a [InstructionFile],
    /// The `<available_skills>` section, or empty.
    pub skills: &'a str,
    pub project_root: Option<&'a Path>,
    pub git_branch: Option<String>,
    /// As [`date_text`] gives it.
    pub date: String,
    /// The session starts while the desktop app is connected, where its replies are read.
    pub in_app: bool,
}

/// A date as the model is given it, with its weekday: `2026-11-03 (Tuesday)`. Models often
/// get the weekday of a date wrong, and work is planned by weekdays.
pub fn date_text(date: chrono::NaiveDate) -> String {
    date.format("%Y-%m-%d (%A)").to_string()
}

/// The date in a system prompt's `<environment>` section.
pub fn prompt_date(prompt: &str) -> Option<chrono::NaiveDate> {
    const FIELD: &str = "\ndate: ";
    let environment = &prompt[prompt.rfind("<environment>")?..];
    let start = environment.find(FIELD)? + FIELD.len();
    chrono::NaiveDate::parse_from_str(environment.get(start..start + 10)?, "%Y-%m-%d").ok()
}

/// A base section with the shared sections filled in.
fn with_shared(text: &str) -> String {
    SHARED
        .iter()
        .fold(text.to_string(), |text, (mark, section)| text.replace(mark, section.trim_end()))
}

fn base_section(profile: &PromptProfile, config: &Config) -> io::Result<String> {
    let (file, built_in) = match profile {
        PromptProfile::Session => (&config.system_prompt_file, BASE_PROMPT),
        PromptProfile::Bot { .. } => (&config.bot_prompt_file, BOT_PROMPT),
    };
    let text = match file {
        Some(path) => std::fs::read_to_string(path)?,
        None => built_in.to_string(),
    };
    let text = with_shared(&text);
    Ok(match profile {
        PromptProfile::Session => text,
        PromptProfile::Bot { name } => text.replace("{{name}}", name),
    })
}

/// Which `bot.md` and app section a bot's prompt comes from: what is stored with its prompt, so
/// that a new one reaches the bots that already have a conversation (bot-turn-design §8).
pub fn bot_template(config: &Config) -> io::Result<String> {
    use std::hash::Hash;
    use std::hash::Hasher;
    let text = match &config.bot_prompt_file {
        Some(path) => std::fs::read_to_string(path)?,
        None => BOT_PROMPT.to_string(),
    };
    let mut hasher = std::collections::hash_map::DefaultHasher::new();
    with_shared(&text).hash(&mut hasher);
    BOT_APP_PROMPT.hash(&mut hasher);
    Ok(format!("{:016x}", hasher.finish()))
}

pub fn build_system_prompt(inputs: &PromptInputs<'_>) -> io::Result<String> {
    let base = base_section(inputs.profile, inputs.config)?;
    let mut prompt = base.trim_end().to_string();
    // Fixed text right after the base, so it stays in the cached prefix; a custom base gets it
    // too, as it says how the app shows replies, not who the agent is.
    if inputs.in_app {
        prompt.push_str("\n\n");
        prompt.push_str(match inputs.profile {
            PromptProfile::Session => APP_PROMPT.trim_end(),
            PromptProfile::Bot { .. } => BOT_APP_PROMPT.trim_end(),
        });
    }

    let guidelines = inputs.tools.guidelines();
    if !guidelines.is_empty() {
        prompt.push_str("\n\n<tool_guidelines>\n");
        for line in guidelines {
            prompt.push_str("- ");
            prompt.push_str(line);
            prompt.push('\n');
        }
        prompt.push_str("</tool_guidelines>");
    }

    for file in inputs.instructions {
        let tag = if file.global { "user_instructions" } else { "project_instructions" };
        prompt.push_str(&format!(
            "\n\n<{tag} path=\"{}\">\n{}\n</{tag}>",
            file.path.display(),
            file.content.trim_end()
        ));
    }
    if !inputs.config.instructions_extra.trim().is_empty() {
        prompt.push_str(&format!(
            "\n\n<extra_instructions>\n{}\n</extra_instructions>",
            inputs.config.instructions_extra.trim_end()
        ));
    }
    // Before the environment, which changes most often (date, branch): sessions of a project
    // share the longest prefix.
    if !inputs.skills.is_empty() {
        prompt.push_str("\n\n");
        prompt.push_str(inputs.skills);
    }

    let os = inputs.env.os();
    let roots: Vec<String> = inputs
        .env
        .workspace_roots()
        .iter()
        .map(|p| p.display().to_string())
        .collect();
    let git = match (inputs.project_root, &inputs.git_branch) {
        (Some(_), Some(branch)) => format!("yes (branch {branch})"),
        (Some(root), None) if root.join(".git").exists() => "yes".to_string(),
        _ => "no".to_string(),
    };
    prompt.push_str(&format!(
        "\n\n<environment>\ncwd: {}\nworkspace_roots: {}\nos: {} ({})\nshell: {}\ndate: {}\ngit: {}\n</environment>\n",
        inputs.env.cwd().display(),
        roots.join(", "),
        os.name,
        os.arch,
        os.shell,
        inputs.date,
        git
    ));
    Ok(prompt)
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_tools::BashTool;
    use her_tools::LocalEnvironment;
    use her_tools::builtin_registry;
    use std::path::PathBuf;

    #[test]
    fn assembles_sections_in_order() {
        let env = LocalEnvironment::new(PathBuf::from("/work/demo"));
        let tools = builtin_registry(BashTool::default());
        let config = Config {
            instructions_extra: "Prefer Rust.".into(),
            ..Config::default()
        };
        let instructions = vec![InstructionFile {
            path: PathBuf::from("/work/demo/AGENTS.md"),
            global: false,
            content: "Run cargo test before finishing.".into(),
        }];
        let prompt = build_system_prompt(&PromptInputs {
            profile: &PromptProfile::Session,
            config: &config,
            tools: &tools,
            env: &env,
            instructions: &instructions,
            skills: "<available_skills>\n…\n</available_skills>",
            project_root: None,
            git_branch: None,
            date: "2026-10-28".into(),
            in_app: false,
        })
        .unwrap();
        assert!(prompt.starts_with("You are Her"));
        assert!(!prompt.contains("In the Hermesh app"));
        let order = [
            "<tool_guidelines>",
            "<project_instructions path=\"/work/demo/AGENTS.md\">",
            "<extra_instructions>",
            "<available_skills>",
            // The section, not the base prompt's mention of its tag.
            "\n<environment>\n",
        ];
        let positions: Vec<usize> = order.iter().map(|tag| prompt.find(tag).unwrap()).collect();
        assert!(positions.windows(2).all(|w| w[0] < w[1]));
        assert!(prompt.contains("cwd: /work/demo"));
        assert!(prompt.contains("date: 2026-10-28"));
        assert!(prompt.contains("- Use read to view files"));
    }

    #[test]
    fn the_app_section_follows_the_base_only_in_the_app() {
        let env = LocalEnvironment::new(PathBuf::from("/work/demo"));
        let tools = builtin_registry(BashTool::default());
        let config = Config::default();
        let prompt = build_system_prompt(&PromptInputs {
            profile: &PromptProfile::Session,
            config: &config,
            tools: &tools,
            env: &env,
            instructions: &[],
            skills: "",
            project_root: None,
            git_branch: None,
            date: "2026-11-03".into(),
            in_app: true,
        })
        .unwrap();
        let app = prompt.find("In the Hermesh app").expect("the app section");
        assert!(prompt[..app].trim_end() == with_shared(BASE_PROMPT).trim_end(), "right after the base");
        assert!(app < prompt.find("<tool_guidelines>").unwrap());
        let text = APP_PROMPT.split_whitespace().collect::<Vec<_>>().join(" ");
        assert!(text.contains("[prompt.rs](src/prompt.rs:11)"));
        assert!(text.contains("```bash block, with no $ prompt"));
    }

    #[test]
    fn the_environment_date_has_its_weekday_and_reads_back() {
        let day = chrono::NaiveDate::from_ymd_opt(2026, 11, 3).unwrap();
        assert_eq!(date_text(day), "2026-11-03 (Tuesday)");
        let prompt = format!("You are Her.\n\n<environment>\ncwd: /w\ndate: {}\ngit: no\n</environment>\n", date_text(day));
        assert_eq!(prompt_date(&prompt), Some(day));
        // Logs written before the weekday was added.
        assert_eq!(prompt_date("<environment>\ndate: 2026-10-28\n</environment>"), chrono::NaiveDate::from_ymd_opt(2026, 10, 28));
        assert_eq!(prompt_date("You are Her. date: 2026-10-28"), None);
    }

    /// A bot's prompt changes the base section (its name, its turns, how it talks) and how files
    /// reach the user; the shared sections and everything after the app section are a session's
    /// (bot-design §2.3, §3.12).
    #[test]
    fn a_bots_prompt_differs_from_a_sessions_only_in_the_base() {
        let env = LocalEnvironment::new(PathBuf::from("/work/bot"));
        let tools = builtin_registry(BashTool::default());
        let config = Config::default();
        let build = |profile: &PromptProfile| {
            build_system_prompt(&PromptInputs {
                profile,
                config: &config,
                tools: &tools,
                env: &env,
                instructions: &[],
                skills: "",
                project_root: None,
                git_branch: None,
                date: "2026-11-04".into(),
                in_app: true,
            })
            .unwrap()
        };
        let session = build(&PromptProfile::Session);
        let bot = build(&PromptProfile::Bot { name: "研究员".into() });
        assert!(bot.starts_with("You are 研究员, one of the user's bots in Hermesh."));
        assert!(bot.contains("The user sees only what you send with send_to_user."));
        for text in [&session, &bot] {
            assert!(!text.contains("{{"), "every mark is filled in");
            for shared in SHARED.map(|(_, section)| section.trim_end()) {
                assert!(text.contains(shared));
            }
        }
        assert!(session.contains("Link files in Markdown"));
        assert!(!bot.contains("Link files in Markdown"), "a bot hands files over in send_to_user");
        assert!(bot.contains("```bash block, with no $ prompt"));
        let after = |text: &str| text[text.find("<tool_guidelines>").unwrap()..].to_string();
        assert_eq!(after(&session), after(&bot));
    }

    #[test]
    fn bot_prompt_file_replaces_the_bots_base_only() {
        let dir = tempfile::tempdir().unwrap();
        let file = dir.path().join("bot.md");
        std::fs::write(&file, "You are {{name}}, testing.\n\n{{acting_with_care}}\n").unwrap();
        let config = Config {
            bot_prompt_file: Some(file),
            ..Config::default()
        };
        let bot = base_section(&PromptProfile::Bot { name: "HerBot".into() }, &config).unwrap();
        assert!(bot.starts_with("You are HerBot, testing.\n\nActing with care:"));
        let session = base_section(&PromptProfile::Session, &config).unwrap();
        assert!(session.starts_with("You are Her"));
    }

    #[test]
    fn base_prompt_says_not_to_work_around_refusals() {
        let text = with_shared(BASE_PROMPT).split_whitespace().collect::<Vec<_>>().join(" ");
        assert!(text.contains(
            "When a tool call is denied or not approved, do not retry it or get the same effect another way, \
             such as a different command, a script or another tool."
        ));
    }
}
