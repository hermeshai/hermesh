//! The commands the user documentation shows under `doc-test: run` work as written, on a mock model.

mod common;

use common::TestEnv;
use std::path::Path;
use std::path::PathBuf;
use std::process::Command;

/// The lines of the code blocks that follow a `doc-test: run` marker.
fn commands_in(text: &str) -> Vec<String> {
    let mut commands = Vec::new();
    let mut lines = text.lines();
    while let Some(line) = lines.next() {
        if !line.contains("doc-test: run") {
            continue;
        }
        assert!(lines.next().is_some_and(|fence| fence.starts_with("```")), "a marker comes right before a code block");
        for line in lines.by_ref() {
            if line.starts_with("```") {
                break;
            }
            commands.push(line.to_string());
        }
    }
    commands
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_documented_commands_run() {
    let docs = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../docs/cli");
    let env = TestEnv::new("manual", "").await;
    let program = PathBuf::from(env!("CARGO_BIN_EXE_her"));
    let bin = program.parent().unwrap();
    let path = format!("{}:{}", bin.display(), std::env::var("PATH").unwrap());
    let mut ran = 0;
    for page in ["quickstart.md", "tools.md", "scripting.md", "troubleshooting.md"] {
        let text = std::fs::read_to_string(docs.join(page)).unwrap();
        for command in commands_in(&text) {
            run(&env, &path, env.work.path(), page, &command);
            ran += 1;
        }
    }
    assert!(ran > 15, "only {ran} commands found");
}

fn run(env: &TestEnv, path: &str, cwd: &Path, page: &str, command: &str) {
    let output = Command::new("sh")
        .args(["-c", command])
        .current_dir(cwd)
        .env("PATH", path)
        .env_remove("AI_AGENT")
        .env("HER_HOME", env.home.path())
        .env("HOME", env.user_home())
        .env("HER_CREDENTIAL_STORE", "file")
        .env("HER_NO_BROWSER", "1")
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "{page}: `{command}` exited {:?}\n{}\n{}",
        output.status.code(),
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

/// Every local link in the user documentation leads somewhere.
#[test]
fn links_between_the_pages_are_not_broken() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..");
    let mut pages: Vec<PathBuf> = std::fs::read_dir(root.join("docs/cli")).unwrap().flatten().map(|e| e.path()).collect();
    pages.push(root.join("README.md"));
    for page in pages {
        let text = std::fs::read_to_string(&page).unwrap();
        for part in text.split("](").skip(1) {
            let target = part.split(')').next().unwrap();
            if target.starts_with("http") || target.starts_with('#') {
                continue;
            }
            let target = target.split('#').next().unwrap();
            assert!(page.parent().unwrap().join(target).exists(), "{}: {target}", page.display());
        }
    }
}
