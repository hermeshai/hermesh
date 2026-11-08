//! The command line's shape, checked against the real command tree and source
//! (docs/kernel/standalone-cli-design.md §4). What a rule cannot decide by itself is written down
//! in `cli_surface.json`, so adding a command, a flag or an environment variable means saying what
//! it is, and an entry that no longer matches anything fails too.

use crate::Cli;
use crate::Command;
use crate::McpCommand;
use crate::OutputFormat;
use crate::TOP_LEVEL_HELP;
use crate::pointer;
use crate::wants_json;
use clap::CommandFactory;
use clap::Parser;
use her_protocol::PermissionMode;
use serde_json::Value;
use std::collections::BTreeMap;
use std::collections::BTreeSet;
use std::path::Path;
use std::path::PathBuf;

fn surface() -> Value {
    serde_json::from_str(include_str!("../cli_surface.json")).expect("cli_surface.json is JSON")
}

struct Entry {
    /// `mcp add`; empty for the root.
    path: String,
    command: clap::Command,
    /// It, or a command above it, is hidden.
    hidden: bool,
}

/// Every command, hidden ones included. `help` is clap's own.
fn commands() -> Vec<Entry> {
    fn visit(mut command: clap::Command, path: String, hidden: bool, out: &mut Vec<Entry>) {
        command.build();
        let hidden = hidden || command.is_hide_set();
        let children: Vec<_> = command.get_subcommands().filter(|child| child.get_name() != "help").cloned().collect();
        for child in children {
            let name = if path.is_empty() { child.get_name().to_string() } else { format!("{path} {}", child.get_name()) };
            visit(child, name, hidden, out);
        }
        out.push(Entry { path, command, hidden });
    }
    let mut out = Vec::new();
    visit(Cli::command(), String::new(), false, &mut out);
    out
}

fn visible() -> Vec<Entry> {
    commands().into_iter().filter(|entry| !entry.hidden && !entry.path.is_empty()).collect()
}

/// The names in an object of `cli_surface.json`, without its `_about`.
fn keys(value: &Value) -> BTreeSet<String> {
    value.as_object().expect("an object").keys().filter(|key| *key != "_about").cloned().collect()
}

fn strings(value: &Value) -> BTreeSet<String> {
    value.as_array().expect("a list").iter().map(|item| item.as_str().expect("a string").to_string()).collect()
}

fn is_kebab(flag: &str) -> bool {
    !flag.is_empty()
        && flag.split('-').all(|word| !word.is_empty() && word.chars().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit()))
}

fn longs(command: &clap::Command) -> BTreeSet<String> {
    command.get_arguments().filter_map(|arg| arg.get_long()).map(|long| format!("--{long}")).collect()
}

#[test]
fn flags_are_kebab_case_and_short_ones_are_few() {
    let surface = surface();
    let allowed = keys(&surface["short_flags"]);
    let mut used = BTreeSet::new();
    for entry in commands() {
        for arg in entry.command.get_arguments() {
            let aliases = arg.get_all_aliases().unwrap_or_default();
            for long in arg.get_long().into_iter().chain(aliases) {
                assert!(is_kebab(long), "`{}`: --{long} is not kebab-case", entry.path);
            }
            let shorts = arg.get_all_short_aliases().unwrap_or_default();
            for short in arg.get_short().into_iter().chain(shorts) {
                assert!(allowed.contains(&short.to_string()), "`{}`: -{short} is not in short_flags", entry.path);
                used.insert(short.to_string());
            }
        }
    }
    assert_eq!(used, allowed, "short_flags lists one nothing uses");
}

#[test]
fn commands_use_the_agreed_verbs() {
    let surface = surface();
    let verbs = strings(&surface["verbs"]);
    let exceptions = keys(&surface["verb_exceptions"]);
    let mut excused = BTreeSet::new();
    for entry in visible() {
        let Some((_, verb)) = entry.path.rsplit_once(' ') else { continue };
        if exceptions.contains(&entry.path) {
            excused.insert(entry.path.clone());
        } else {
            assert!(verbs.contains(verb), "`{}`: `{verb}` is not an agreed verb", entry.path);
        }
    }
    assert_eq!(excused, exceptions, "verb_exceptions lists a command that needs no exception");
}

#[test]
fn every_command_says_what_it_prints() {
    let surface = surface();
    let lists = &surface["commands"];
    let json = strings(&lists["json"]);
    let always = keys(&lists["always_json"]);
    let no_data = strings(&lists["no_data"]);
    let mut known = BTreeSet::new();
    for entry in visible() {
        let in_json = json.contains(&entry.path);
        let kinds = usize::from(in_json) + usize::from(always.contains(&entry.path)) + usize::from(no_data.contains(&entry.path));
        assert_eq!(kinds, 1, "`{}` should be in exactly one list under commands", entry.path);
        assert_eq!(longs(&entry.command).contains("--json"), in_json, "`{}`: --json", entry.path);
        known.insert(entry.path);
    }
    let listed: BTreeSet<_> = json.iter().chain(&always).chain(&no_data).cloned().collect();
    assert_eq!(listed, known, "commands lists one that does not exist");
}

#[test]
fn the_top_level_help_lists_every_command_once() {
    let groups = TOP_LEVEL_HELP.split_once("\n\nOptions:").expect("the options follow the groups").0;
    let mut listed = BTreeMap::new();
    for line in groups.lines().filter_map(|line| line.strip_prefix("  ")) {
        let (name, about) = line.split_once(' ').expect("a name and what it does");
        assert!(listed.insert(name.to_string(), about.trim().to_string()).is_none(), "{name} is listed twice");
    }
    let top_level: BTreeMap<String, String> = visible()
        .into_iter()
        .filter(|entry| !entry.path.contains(' '))
        .map(|entry| (entry.path, entry.command.get_about().expect("every command says what it does").to_string()))
        .collect();
    assert_eq!(listed, top_level);
    for (name, about) in &top_level {
        assert!(about.len() <= 60, "{name}: the summary should fit one line of the help: {about}");
    }
}

/// The `HER_*` string literals in a text.
fn variables_in(text: &str, found: &mut BTreeSet<String>) {
    for (at, _) in text.match_indices("\"HER_") {
        let rest = &text[at + 1..];
        let Some(end) = rest.find(|c: char| !(c.is_ascii_uppercase() || c.is_ascii_digit() || c == '_')) else { continue };
        // A name ending in `_` is a prefix someone checks for, not a variable.
        if rest[end..].starts_with('"') && !rest[..end].ends_with('_') {
            found.insert(rest[..end].to_string());
        }
    }
}

fn rust_files(dir: &Path, out: &mut Vec<PathBuf>) {
    for entry in std::fs::read_dir(dir).unwrap().flatten() {
        let path = entry.path();
        if path.is_dir() {
            rust_files(&path, out);
        } else if path.extension().is_some_and(|extension| extension == "rs") {
            out.push(path);
        }
    }
}

fn repository() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
}

#[test]
fn environment_variables_are_registered() {
    let surface = surface();
    let env = &surface["env"];
    let registered = keys(env);
    let mut files = Vec::new();
    rust_files(&repository().join("crates"), &mut files);
    let mut found = BTreeSet::new();
    for file in files {
        variables_in(&std::fs::read_to_string(&file).unwrap_or_default(), &mut found);
    }
    let unregistered: Vec<_> = found.difference(&registered).collect();
    assert!(unregistered.is_empty(), "add these to env in cli_surface.json: {unregistered:?}");
    let stale: Vec<_> = registered.difference(&found).collect();
    assert!(stale.is_empty(), "no code uses these any more: {stale:?}");
    for name in &registered {
        let entry = &env[name];
        let job = entry["job"].as_str().expect("job");
        let stability = entry["stability"].as_str().expect("stability");
        assert!(["setting-override", "location", "environment", "diagnostics", "tool-child", "test", "build"].contains(&job), "{name}: job {job}");
        assert!(["stable", "deprecated", "internal"].contains(&stability), "{name}: stability {stability}");
        if job == "test" {
            assert_eq!(stability, "internal", "{name}");
            assert!(name.starts_with("HER_TEST_") || entry["legacy"] == true, "{name}: a test variable starts with HER_TEST_");
        }
    }
}

fn version_of(text: &str) -> Vec<u32> {
    text.split('.').map(|part| part.parse().expect("a version")).collect()
}

/// An old spelling says when it goes, and goes then.
#[test]
fn deprecated_spellings_go_when_their_release_comes() {
    let surface = surface();
    let version = version_of(env!("CARGO_PKG_VERSION"));
    let env = &surface["env"];
    let variables = keys(env).into_iter().filter(|name| env[name]["stability"] == "deprecated");
    for name in variables {
        let remove_in = env[&name]["remove_in"].as_str().unwrap_or_else(|| panic!("{name} needs remove_in"));
        assert!(version < version_of(remove_in), "{name} should be gone in {remove_in}");
    }
    let exec = commands().into_iter().find(|entry| entry.path == "exec").unwrap().command;
    let aliases: BTreeSet<String> = exec
        .get_arguments()
        .flat_map(|arg| arg.get_all_aliases().unwrap_or_default())
        .map(|alias| format!("--{alias}"))
        .collect();
    let flags = &surface["deprecated_flags"];
    assert_eq!(aliases, keys(flags), "deprecated_flags and the aliases of her exec differ");
    for flag in keys(flags) {
        let remove_in = flags[&flag]["remove_in"].as_str().unwrap();
        assert!(version < version_of(remove_in), "{flag} should be gone in {remove_in}");
    }
}

/// Every option of a run is named on every surface it has, and each name is real.
#[test]
fn run_options_are_named_on_every_surface() {
    let surface = surface();
    let options = &surface["run_options"];
    let exec = commands().into_iter().find(|entry| entry.path == "exec").unwrap().command;
    let schema: Value = serde_json::from_str(include_str!("../../her-protocol/schema.json")).unwrap();
    let rpc = &schema["definitions"]["SessionNewParams"]["properties"];
    let acp_source = include_str!("../../her-acp/src/out.rs");
    let mut acp_named = BTreeSet::new();
    let mut flags = BTreeSet::new();
    for name in keys(options) {
        let row = &options[&name];
        let flag = row["flag"].as_str().unwrap_or_else(|| panic!("{name}: no flag"));
        assert!(longs(&exec).contains(flag), "{name}: her exec has no {flag}");
        flags.insert(flag.to_string());
        if let Some(config) = row["config"].as_str() {
            let sample = row["sample"].as_str().unwrap_or_else(|| panic!("{name}: no sample"));
            her_core::config::load(Path::new("/nonexistent"), None, false, &[format!("{config}={sample}")])
                .unwrap_or_else(|err| panic!("{name}: configuration key {config}: {err}"));
        }
        if let Some(variable) = row["env"].as_str() {
            assert_eq!(surface["env"][variable]["job"], "setting-override", "{name}: {variable}");
            let config = row["config"].as_str().unwrap_or_else(|| panic!("{name}: a variable overrides a setting"));
            assert_eq!(variable, format!("HER_{}", config.to_uppercase()), "{name}: a variable has the setting's name");
        }
        if let Some(param) = row["rpc"].as_str() {
            assert!(rpc.get(param).is_some(), "{name}: session/new has no {param}");
        }
        if let Some(id) = row["acp"].as_str() {
            assert!(acp_source.contains(&format!("{{ \"id\": \"{id}\"")), "{name}: ACP has no option {id}");
            acp_named.insert(id.to_string());
        }
    }
    // Nothing a client can set goes without a row.
    let offered: BTreeSet<String> = acp_source
        .match_indices("{ \"id\": \"")
        .map(|(at, found)| acp_source[at + found.len()..].split('"').next().unwrap().to_string())
        .collect();
    assert_eq!(offered, acp_named, "ACP offers an option run_options does not name");
    let others = keys(&surface["exec_flags"]);
    let all: BTreeSet<_> = flags.union(&others).cloned().collect();
    assert_eq!(longs(&exec), all, "each flag of her exec is in run_options or exec_flags");
}

fn exec_args(cli: Cli) -> crate::ExecArgs {
    match cli.command {
        Command::Exec { args, .. } => args,
        _ => panic!("not her exec"),
    }
}

#[test]
fn the_last_flag_wins() {
    let parse = |args: &[&str]| {
        let line = [&["her", "exec", "hello"], args].concat();
        exec_args(Cli::try_parse_from(line).unwrap())
    };
    assert_eq!(parse(&["-m", "a/b", "-m", "c/d"]).run.model.as_deref(), Some("c/d"));
    let mode = |args: &[&str]| parse(args).run.permission_mode();
    assert_eq!(mode(&["--permission-mode", "manual", "--dangerously-skip-permissions"]), Some(PermissionMode::BypassPermissions));
    assert_eq!(mode(&["--dangerously-skip-permissions", "--permission-mode", "manual"]), Some(PermissionMode::Manual));
    assert_eq!(mode(&["--permission-mode", "manual", "--permission-mode", "accept-edits"]), Some(PermissionMode::AcceptEdits));
    assert_eq!(parse(&["--json", "--output", "text"]).format(), OutputFormat::Text);
    assert_eq!(parse(&["--output", "text", "--json"]).format(), OutputFormat::Json);
    assert_eq!(parse(&["--output", "stream-json"]).format(), OutputFormat::StreamJson);
    assert_eq!(parse(&["--json"]).format(), OutputFormat::Json, "--json is one JSON value");

    let scope = |args: &[&str]| {
        let line = [&["her", "mcp", "add", "x", "--url", "https://x.invalid"], args].concat();
        match Cli::try_parse_from(line).unwrap().command {
            Command::Mcp { command: Some(McpCommand::Add { global, local, project, .. }), .. } => (global, local, project),
            _ => panic!("not her mcp add"),
        }
    };
    assert_eq!(scope(&[]), (false, false, false));
    assert_eq!(scope(&["--local", "--project"]), (false, false, true));
    assert_eq!(scope(&["--project", "--local"]), (false, true, false));
    assert_eq!(scope(&["--local", "--global"]), (true, false, false));
}

#[test]
fn a_group_without_a_verb_lists_its_things() {
    let parse = |args: &[&str]| Cli::try_parse_from([&["her"], args].concat()).unwrap().command;
    assert!(matches!(parse(&["mcp"]), Command::Mcp { command: None, .. }));
    assert!(matches!(parse(&["mcp", "--json"]), Command::Mcp { command: None, list, .. } if list.json));
    assert!(matches!(parse(&["mcp", "list", "--check"]), Command::Mcp { command: Some(McpCommand::List(list)), .. } if list.check));
    assert!(matches!(parse(&["models", "list", "--json"]), Command::Models { action: Some(crate::ModelsAction::List(list)), .. } if list.json));
    assert!(matches!(parse(&["sessions", "list", "--all"]), Command::Sessions { action: Some(crate::SessionsAction::List(list)), .. } if list.all));
    assert!(matches!(parse(&["skills", "list", "--all"]), Command::Skills { action: Some(crate::SkillsAction::List(list)), .. } if list.all));
}

fn os(args: &[&str]) -> Vec<std::ffi::OsString> {
    args.iter().map(Into::into).collect()
}

#[test]
fn json_is_asked_for_in_every_spelling() {
    for line in [
        &["her", "status", "--json"][..],
        &["her", "exec", "hi", "--output", "json"],
        &["her", "exec", "hi", "--output=json"],
        &["her", "exec", "hi", "--output", "stream-json"],
        &["her", "exec", "hi", "--output=stream-json"],
    ] {
        assert!(wants_json(&os(line)), "{line:?}");
    }
    for line in [
        &["her", "status"][..],
        &["her", "exec", "hi", "--output", "text"],
        &["her", "exec", "--", "--json"],
    ] {
        assert!(!wants_json(&os(line)), "{line:?}");
    }
}

#[test]
fn spellings_from_elsewhere_get_a_pointer() {
    let to_resume = "her exec --resume";
    for line in [
        &["her", "resume", "last", "-p", "next"][..],
        &["her", "--continue"],
        &["her", "-r"],
        &["her", "--resume", "abc"],
        &["her", "exec", "hi", "--continue"],
    ] {
        assert!(pointer(&os(line)).is_some_and(|hint| hint.contains(to_resume)), "{line:?}");
    }
    for line in [&["her", "-p", "hi"][..], &["her", "--print", "hi"]] {
        assert!(pointer(&os(line)).is_some_and(|hint| hint.contains("her exec \"PROMPT\"")), "{line:?}");
    }
    assert!(pointer(&os(&["her", "models", "show", "a/b"])).is_some_and(|hint| hint.contains("unhide")));
    // Its own error says what is wrong: nothing to point at.
    assert_eq!(pointer(&os(&["her", "exec", "hi", "--resume"])), None);
    assert_eq!(pointer(&os(&["her", "status", "--nope"])), None);
    assert_eq!(pointer(&os(&["her", "exec", "--", "-p"])), None);
}

/// The first word is judged before the parser is built, from this list (§4.5).
#[test]
fn the_list_of_command_words_is_the_parsers() {
    let mut named: BTreeSet<String> = Cli::command().get_subcommands().flat_map(|c| std::iter::once(c.get_name().to_string()).chain(c.get_all_aliases().map(str::to_string))).collect();
    named.insert("help".to_string());
    let listed: BTreeSet<String> = crate::COMMAND_WORDS.iter().map(|word| (*word).to_string()).collect();
    assert_eq!(named, listed);
}

#[test]
fn the_first_word_decides_what_a_command_line_means() {
    let rewritten = |args: &[&str]| crate::first_word(args.iter().map(Into::into).collect()).map(|out| out.iter().map(|a| a.to_string_lossy().into_owned()).collect::<Vec<_>>());
    let of = |words: &[&str]| Ok(words.iter().map(|w| (*w).to_string()).collect::<Vec<_>>());
    assert_eq!(rewritten(&["her"]), of(&["her", "interactive"]));
    assert_eq!(rewritten(&["her", "-m", "a/b"]), of(&["her", "interactive", "-m", "a/b"]));
    assert_eq!(rewritten(&["her", "fix the test"]), of(&["her", "interactive", "fix the test"]));
    assert_eq!(rewritten(&["her", "-c", "x=1", "fix it"]), of(&["her", "interactive", "-c", "x=1", "fix it"]));
    assert_eq!(rewritten(&["her", "--", "status", "how"]), of(&["her", "interactive", "--", "status how"]));
    assert_eq!(rewritten(&["her", "status"]), of(&["her", "status"]));
    assert_eq!(rewritten(&["her", "-c", "x=1", "status"]), of(&["her", "-c", "x=1", "status"]));
    assert_eq!(rewritten(&["her", "--help"]), of(&["her", "--help"]));
    assert_eq!(rewritten(&["her", "-V"]), of(&["her", "-V"]));
    assert!(rewritten(&["her", "stauts"]).is_err(), "a slip does not start a session");
    assert!(rewritten(&["her", "hello"]).is_ok(), "hello is not help");
}
