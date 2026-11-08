//! The interactive mode on a real pseudo-terminal: what a person would type and see.

mod common;

use common::TestEnv;
use std::io::Read;
use std::io::Write;
use std::os::fd::AsRawFd;
use std::os::fd::FromRawFd;
use std::os::fd::OwnedFd;
use std::os::unix::process::CommandExt;
use std::process::Child;
use std::process::Command;
use std::process::Stdio;
use std::sync::Arc;
use std::sync::Mutex;
use std::time::Duration;
use std::time::Instant;

const WAIT: Duration = Duration::from_secs(30);

/// `her` on a pseudo-terminal, with what it wrote so far.
struct Pty {
    child: Child,
    input: std::fs::File,
    output: Arc<Mutex<String>>,
}

impl Pty {
    fn start(env: &TestEnv, args: &[&str]) -> Pty {
        let (mut master, mut slave) = (0, 0);
        let mut size = libc::winsize { ws_row: 40, ws_col: 120, ws_xpixel: 0, ws_ypixel: 0 };
        // SAFETY: all out-pointers are valid; the name and termios may be null.
        let opened = unsafe { libc::openpty(&mut master, &mut slave, std::ptr::null_mut(), std::ptr::null_mut(), &mut size) };
        assert_eq!(opened, 0, "openpty");
        // SAFETY: openpty returned these descriptors just now and nothing else owns them.
        let (master, slave) = unsafe { (OwnedFd::from_raw_fd(master), OwnedFd::from_raw_fd(slave)) };
        let mut cmd = Command::new(env!("CARGO_BIN_EXE_her"));
        cmd.args(args)
            .current_dir(env.work.path())
            .env("HER_HOME", env.home.path())
            .env("HOME", env.user_home())
            .env("HER_CREDENTIAL_STORE", "file")
            .env("HER_NO_BROWSER", "1")
            .env("HER_LANG", "en")
            .env("TERM", "xterm-256color")
            .env_remove("HER_MODEL")
            .env_remove("NO_COLOR")
            .stdin(Stdio::from(slave.try_clone().unwrap()))
            .stdout(Stdio::from(slave.try_clone().unwrap()))
            .stderr(Stdio::from(slave.try_clone().unwrap()));
        let slave_fd = slave.as_raw_fd();
        // SAFETY: only calls that are safe after fork: a new session, and the terminal as its own.
        unsafe {
            cmd.pre_exec(move || {
                if libc::setsid() < 0 || libc::ioctl(slave_fd, libc::TIOCSCTTY as _, 0) < 0 {
                    return Err(std::io::Error::last_os_error());
                }
                Ok(())
            });
        }
        let child = cmd.spawn().expect("run her");
        drop(slave);
        let input = std::fs::File::from(master.try_clone().unwrap());
        let mut reader = std::fs::File::from(master);
        let output = Arc::new(Mutex::new(String::new()));
        let written = output.clone();
        std::thread::spawn(move || {
            let mut buffer = [0; 4096];
            while let Ok(read) = reader.read(&mut buffer) {
                if read == 0 {
                    break;
                }
                written.lock().unwrap().push_str(&String::from_utf8_lossy(&buffer[..read]));
            }
        });
        Pty { child, input, output }
    }

    fn type_text(&mut self, text: &str) {
        self.input.write_all(text.as_bytes()).unwrap();
        self.input.flush().unwrap();
    }

    fn seen(&self) -> String {
        self.output.lock().unwrap().clone()
    }

    /// Waits until `text` shows, counting only what came after `from`.
    fn wait_for_after(&self, from: usize, text: &str) -> usize {
        let start = Instant::now();
        loop {
            let seen = self.seen();
            if let Some(at) = seen.get(from..).and_then(|rest| rest.find(text)) {
                return from + at + text.len();
            }
            assert!(start.elapsed() < WAIT, "did not see {text:?} in:\n{seen}");
            std::thread::sleep(Duration::from_millis(20));
        }
    }

    fn wait_for(&self, text: &str) -> usize {
        self.wait_for_after(0, text)
    }

    fn wait_exit(&mut self) -> i32 {
        let start = Instant::now();
        loop {
            if let Some(status) = self.child.try_wait().unwrap() {
                return status.code().unwrap_or(-1);
            }
            assert!(start.elapsed() < WAIT, "still running; saw:\n{}", self.seen());
            std::thread::sleep(Duration::from_millis(20));
        }
    }
}

impl Drop for Pty {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

const QUIET: &str = "[update]\ncheck = false\n";

/// The text of the only session's log.
fn find_log(env: &TestEnv) -> String {
    fn walk(dir: &std::path::Path, out: &mut String) {
        for entry in std::fs::read_dir(dir).into_iter().flatten().flatten() {
            let path = entry.path();
            if path.is_dir() {
                walk(&path, out);
            } else if path.extension().is_some_and(|e| e == "jsonl") {
                out.push_str(&std::fs::read_to_string(path).unwrap_or_default());
            }
        }
    }
    let mut out = String::new();
    walk(&env.home.path().join("sessions"), &mut out);
    out
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_message_gets_a_reply_and_quit_says_how_to_continue() {
    let env = TestEnv::new("manual", QUIET).await;
    let mut pty = Pty::start(&env, &[]);
    pty.wait_for("mock/mock-1");
    pty.wait_for("› ");
    pty.type_text("say hi\r");
    pty.wait_for("Hello from mock.");
    pty.wait_for("tokens:");
    pty.type_text("/quit\r");
    assert_eq!(pty.wait_exit(), 0);
    let seen = pty.seen();
    assert!(seen.contains("continue with: her resume"), "{seen}");
    assert!(seen.contains("\x1b[?2004h"), "bracketed paste went on");
    assert!(seen.contains("\x1b[?2004l"), "and off again");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_first_message_can_come_on_the_command_line() {
    let env = TestEnv::new("manual", QUIET).await;
    let mut pty = Pty::start(&env, &["say hi"]);
    pty.wait_for("Hello from mock.");
    pty.wait_for("tokens:");
    pty.type_text("\x04");
    assert_eq!(pty.wait_exit(), 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_change_shows_what_it_does_and_waits_for_the_answer() {
    let env = TestEnv::new("manual", QUIET).await;
    let mut pty = Pty::start(&env, &[]);
    pty.wait_for("› ");
    pty.type_text("[[mock:files]] change it\r");
    // The write: its first lines, and the question.
    pty.wait_for("+ alpha");
    let asked = pty.wait_for("[y] yes");
    pty.type_text("what\r");
    pty.wait_for_after(asked, "please answer");
    pty.type_text("y\r");
    pty.wait_for("allowed: write");
    // The edit: what goes and what comes.
    pty.wait_for("- alpha");
    pty.wait_for("+ gamma");
    let at = pty.wait_for("allowed: write");
    pty.wait_for_after(at, "[y] yes");
    pty.type_text("y\r");
    pty.wait_for("files done");
    pty.wait_for("tokens:");
    assert_eq!(std::fs::read_to_string(env.work.path().join("notes.txt")).unwrap(), "gamma\nbeta\n");
    pty.type_text("/exit\r");
    assert_eq!(pty.wait_exit(), 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn no_answers_with_words_for_the_model() {
    let env = TestEnv::new("manual", QUIET).await;
    let mut pty = Pty::start(&env, &[]);
    pty.wait_for("› ");
    pty.type_text("[[mock:files]] change it\r");
    pty.wait_for("[y] yes");
    pty.type_text("n\r");
    pty.wait_for("feedback (optional)");
    pty.type_text("not this one\r");
    pty.wait_for("denied: write");
    assert!(!env.work.path().join("notes.txt").exists());
    pty.wait_for("files done");
    pty.wait_for("tokens:");
    pty.type_text("/quit\r");
    assert_eq!(pty.wait_exit(), 0);
    let log = find_log(&env);
    assert!(log.contains("not this one"), "the model hears why: {log}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_message_sent_while_a_task_runs_waits_and_comes_back_with_up() {
    let env = TestEnv::new("manual", QUIET).await;
    let mut pty = Pty::start(&env, &[]);
    pty.wait_for("› ");
    pty.type_text("[[mock:slow:100]] take your time\r");
    pty.wait_for("replying");
    pty.type_text("after this\t");
    pty.wait_for("after this task: after this");
    let at = pty.seen().len();
    pty.type_text("\x1b[A");
    pty.wait_for_after(at, "› after this");
    pty.type_text("\x15\x03");
    pty.wait_for("interrupted");
    pty.type_text("/quit\r");
    assert_eq!(pty.wait_exit(), 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn commands_do_their_own_work_and_other_slashes_are_messages() {
    let env = TestEnv::new("manual", QUIET).await;
    let mut pty = Pty::start(&env, &[]);
    pty.wait_for("› ");
    pty.type_text("/help\r");
    pty.wait_for("/rewind");
    let at = pty.seen().len();
    pty.type_text("/status\r");
    pty.wait_for_after(at, "credentials");
    let at = pty.seen().len();
    pty.type_text("/tools\r");
    pty.wait_for_after(at, "web_search");
    let at = pty.seen().len();
    pty.type_text("/permissions accept-edits\r");
    pty.wait_for_after(at, "permissions: accept-edits");
    let at = pty.seen().len();
    pty.type_text("/rename a good name\r");
    pty.wait_for_after(at, "renamed: a good name");
    let at = pty.seen().len();
    pty.type_text("/etc/hosts is what?\r");
    pty.wait_for_after(at, "Hello from mock.");
    pty.wait_for_after(at, "tokens:");
    pty.type_text("/quit\r");
    assert_eq!(pty.wait_exit(), 0);
    let out = env.her(&["sessions", "--all", "--json"]).output().await.unwrap();
    assert!(String::from_utf8_lossy(&out.stdout).contains("a good name"));
}

/// A command the kernel answers without a task leaves the mode ready for the next message.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_kernels_own_commands_end_with_the_mode_idle() {
    let env = TestEnv::new("manual", QUIET).await;
    let mut pty = Pty::start(&env, &["say hi"]);
    pty.wait_for("tokens:");
    let at = pty.seen().len();
    pty.type_text("/context\r");
    pty.wait_for_after(at, "Free");
    std::thread::sleep(Duration::from_millis(400));
    let at = pty.seen().len();
    std::thread::sleep(Duration::from_millis(400));
    let after = pty.seen();
    assert!(!after[at..].contains("replying") && !after[at..].contains("thinking"), "no spinner left behind: {:?}", &after[at..]);
    pty.type_text("/compact\r");
    pty.wait_for_after(at, "context compacted");
    pty.type_text("/quit\r");
    assert_eq!(pty.wait_exit(), 0);
}

/// Text in the input box stays while a question is answered, and a resize does not lose it.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_pasted_block_stays_one_message() {
    let env = TestEnv::new("manual", QUIET).await;
    let mut pty = Pty::start(&env, &[]);
    pty.wait_for("› ");
    pty.type_text("\x1b[200~first line\nsecond line\x1b[201~");
    pty.wait_for("second line");
    assert!(!pty.seen().contains("Hello from mock."), "a paste does not send");
    pty.type_text("\r");
    pty.wait_for("Hello from mock.");
    pty.wait_for("tokens:");
    pty.type_text("/quit\r");
    assert_eq!(pty.wait_exit(), 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_session_continues_with_resume() {
    let env = TestEnv::new("manual", QUIET).await;
    let mut pty = Pty::start(&env, &["say hi"]);
    pty.wait_for("tokens:");
    pty.type_text("/quit\r");
    pty.wait_exit();
    drop(pty);
    let mut pty = Pty::start(&env, &["resume"]);
    pty.wait_for("say hi");
    pty.wait_for("› ");
    pty.type_text("and again\r");
    let at = pty.wait_for("and again");
    pty.wait_for_after(at, "Hello from mock.");
    pty.wait_for_after(at, "tokens:");
    pty.type_text("/quit\r");
    assert_eq!(pty.wait_exit(), 0);
}

/// The number a list on screen gives `name`.
fn number_of(seen: &str, name: &str) -> String {
    let line = seen.lines().rev().find(|line| line.trim_end().ends_with(name)).unwrap_or_else(|| panic!("{name} is not listed in:\n{seen}"));
    line.split_whitespace().next().unwrap().to_string()
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn first_time_without_a_model_goes_through_logging_in_to_a_reply() {
    let env = TestEnv::new("manual", QUIET).await;
    std::fs::write(env.home.path().join("config.toml"), QUIET).unwrap();
    let models = format!("[providers.deepseek]\nbase_url = \"{}\"\n", env.mock.base_url());
    std::fs::write(env.home.path().join("models.toml"), models).unwrap();
    let mut pty = Pty::start(&env, &[]);
    pty.wait_for("Let's connect one");
    let asked = pty.wait_for("How do you want to connect a model?");
    pty.wait_for_after(asked, "> ");
    pty.type_text("1\n");
    let asked = pty.wait_for("Which one?");
    let choice = pty.wait_for_after(asked, "> ");
    let number = number_of(&pty.seen()[..choice], "DeepSeek");
    pty.type_text(&format!("{number}\n"));
    pty.wait_for("Paste the key of DeepSeek");
    pty.type_text("sk-secret-words\n");
    let asked = pty.wait_for("Which model should new sessions start on?");
    pty.wait_for_after(asked, "> ");
    pty.type_text("\n");
    pty.wait_for("› ");
    assert!(!pty.seen().contains("sk-secret-words"), "a key is not shown: {}", pty.seen());
    pty.type_text("say hi\r");
    pty.wait_for("Hello from mock.");
    pty.wait_for("tokens:");
    pty.type_text("/quit\r");
    assert_eq!(pty.wait_exit(), 0);
    let config = std::fs::read_to_string(env.home.path().join("config.toml")).unwrap();
    assert!(config.contains("deepseek/"), "the model it starts on is saved: {config}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn logging_in_at_a_terminal_hides_the_key_and_takes_a_refusal() {
    let env = TestEnv::new("manual", QUIET).await;
    let mut pty = Pty::start(&env, &["login", "deepseek"]);
    pty.wait_for("Paste the key of DeepSeek");
    pty.type_text(&format!("{}\n", her_mock::REJECTED_KEY));
    assert_eq!(pty.wait_exit(), 2);
    assert!(!pty.seen().contains(her_mock::REJECTED_KEY), "{}", pty.seen());
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_project_that_brings_servers_asks_once() {
    let env = TestEnv::new("manual", QUIET).await;
    std::fs::create_dir(env.work.path().join(".git")).unwrap();
    std::fs::write(
        env.work.path().join(".mcp.json"),
        r#"{"mcpServers":{"files":{"command":"run-this-program","args":["--flag"]}}}"#,
    )
    .unwrap();
    let mut pty = Pty::start(&env, &[]);
    pty.wait_for("run-this-program --flag");
    pty.wait_for("[y/N]");
    pty.type_text("\r");
    pty.wait_for("mock/mock-1");
    pty.wait_for("› ");
    pty.type_text("/quit\r");
    assert_eq!(pty.wait_exit(), 0);
    drop(pty);
    let mut pty = Pty::start(&env, &[]);
    pty.wait_for("› ");
    assert!(!pty.seen().contains("[y/N]"), "no is remembered");
    pty.type_text("/quit\r");
    assert_eq!(pty.wait_exit(), 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn typing_in_a_script_is_refused_and_slips_are_caught() {
    let env = TestEnv::new("manual", QUIET).await;
    let out = env.her(&[]).stdin(Stdio::null()).output().await.unwrap();
    assert_eq!(out.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&out.stderr).contains("her exec"));
    let out = env.her(&["stauts"]).output().await.unwrap();
    assert_eq!(out.status.code(), Some(2));
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(stderr.contains("did you mean `her status`") && stderr.contains("her -- stauts"), "{stderr}");
    let out = env.her(&["fix", "the", "test"]).output().await.unwrap();
    assert_eq!(out.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&out.stderr).contains("in quotes"));
    let out = env.her(&["resume"]).stdin(Stdio::null()).output().await.unwrap();
    assert_eq!(out.status.code(), Some(2));
    let out = env.her(&["-p", "x"]).output().await.unwrap();
    assert_eq!(out.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&out.stderr).contains("her exec"));
}
