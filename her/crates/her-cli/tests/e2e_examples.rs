//! The programs in `examples/rpc` run for real, so that the documentation does not go stale.

mod common;

use common::TestEnv;
use std::path::PathBuf;
use std::process::Command;

fn example(name: &str) -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../examples/rpc").join(name)
}

fn run(env: &TestEnv, interpreter: &str, script: &str, prompt: &str) -> Option<(String, String)> {
    if Command::new(interpreter).arg("--version").output().is_err() {
        eprintln!("no {interpreter} here");
        return None;
    }
    let output = Command::new(interpreter)
        .arg(example(script))
        .arg(prompt)
        .arg(env!("CARGO_BIN_EXE_her"))
        .current_dir(env.work.path())
        .env("HER_HOME", env.home.path())
        .env("HOME", env.user_home())
        .env("HER_CREDENTIAL_STORE", "file")
        .output()
        .unwrap();
    let (stdout, stderr) = (String::from_utf8_lossy(&output.stdout).into_owned(), String::from_utf8_lossy(&output.stderr).into_owned());
    assert!(output.status.success(), "{script}: {stdout}\n{stderr}");
    Some((stdout, stderr))
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_python_example_gets_a_reply_and_answers_an_approval() {
    let env = TestEnv::new("manual", "").await;
    let Some((reply, _)) = run(&env, "python3", "minimal.py", "say hi") else { return };
    assert_eq!(reply.trim(), "Hello from mock.");
    let Some((reply, log)) = run(&env, "python3", "minimal.py", "[[mock:files]] go") else { return };
    assert!(reply.contains("files done"), "{reply}");
    assert!(log.contains("approving: write notes.txt"), "{log}");
    assert_eq!(std::fs::read_to_string(env.work.path().join("notes.txt")).unwrap(), "gamma\nbeta\n");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_node_example_gets_a_reply_and_answers_an_approval() {
    let env = TestEnv::new("manual", "").await;
    let Some((reply, _)) = run(&env, "node", "minimal.mjs", "say hi") else { return };
    assert_eq!(reply.trim(), "Hello from mock.");
    let Some((reply, log)) = run(&env, "node", "minimal.mjs", "[[mock:files]] go") else { return };
    assert!(reply.contains("files done"), "{reply}");
    assert!(log.contains("approving: write notes.txt"), "{log}");
}
