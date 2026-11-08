//! End-to-end: the file panel's methods (docs/desktop/file-panel-design.md §9) over
//! `her serve --stdio`: a session's folder listed a level at a time, its files read for
//! showing, saved only while they are the version they were edited from, and changes to
//! watched files and folders told.

mod common;

use common::SchemaCheck;
use common::TestEnv;
use common::native_init;
use her_rpc::Incoming;
use her_rpc::IncomingRx;
use serde_json::Value;
use serde_json::json;
use std::path::Path;
use std::time::Duration;

/// The next `files/changed` that names `path`, within 20 seconds.
async fn changed(rx: &mut IncomingRx, path: &Path) -> Value {
    let deadline = tokio::time::Instant::now() + Duration::from_secs(20);
    loop {
        let incoming = tokio::time::timeout_at(deadline, rx.recv()).await.unwrap_or_else(|_| panic!("no files/changed for {}", path.display())).expect("connection closed");
        if let Incoming::Notification { method, params, .. } = incoming
            && method == "files/changed"
            && params["paths"].as_array().is_some_and(|p| p.iter().any(|x| x.as_str() == path.to_str()))
        {
            return params;
        }
    }
}

/// Lets notifications already on their way go by.
async fn drain(rx: &mut IncomingRx) {
    while tokio::time::timeout(Duration::from_millis(1200), rx.recv()).await.is_ok_and(|m| m.is_some()) {}
}

fn sha256(bytes: &[u8]) -> String {
    her_core::files::hash(bytes)
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_file_panel_lists_reads_saves_and_hears_changes() {
    let env = TestEnv::new("manual", "").await;
    let schema = SchemaCheck::new(&["FilesListResult", "FilesReadResult", "FilesWriteResult", "FilesChangedParams"]);
    let project = env.workspace("proj");
    std::fs::create_dir_all(project.join("src")).unwrap();
    std::fs::create_dir_all(project.join(".git")).unwrap();
    std::fs::write(project.join("src/app.ts"), "one\r\ntwo\r\n").unwrap();
    std::fs::write(project.join("README.md"), "# hi\n").unwrap();
    // "报告" in GBK.
    std::fs::write(project.join("data.csv"), b"\xB1\xA8\xB8\xE6\n").unwrap();

    let (mut child, client, mut rx) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let created = client.request("session/new", json!({"cwd": project})).await.unwrap();
    let id = created["session_id"].as_str().unwrap().to_string();

    // A level at a time: folders first, `.git` left out.
    let root = client.request("files/list", json!({"session_id": id, "path": ""})).await.unwrap();
    schema.check("FilesListResult", &root);
    let names: Vec<&str> = root["entries"].as_array().unwrap().iter().map(|e| e["name"].as_str().unwrap()).collect();
    assert_eq!(names, ["src", "data.csv", "README.md"]);
    assert_eq!(root["path"], json!(project));
    let src = client.request("files/list", json!({"session_id": id, "path": "src"})).await.unwrap();
    assert_eq!(src["entries"][0]["name"], "app.ts");
    assert_eq!(src["entries"][0]["size"], 10);

    // Read as it is, line ends and all; a GBK file reads, and is not to be written back.
    let app = client.request("files/read", json!({"session_id": id, "path": "src/app.ts"})).await.unwrap();
    schema.check("FilesReadResult", &app);
    assert_eq!((app["kind"].as_str(), app["text"].as_str(), app["encoding"].as_str(), app["writable"].as_bool()), (Some("text"), Some("one\r\ntwo\r\n"), Some("utf-8"), Some(true)));
    assert_eq!(app["hash"], sha256(b"one\r\ntwo\r\n"));
    let gbk = client.request("files/read", json!({"session_id": id, "path": "data.csv"})).await.unwrap();
    assert_eq!((gbk["text"].as_str(), gbk["encoding"].as_str(), gbk["writable"].as_bool()), (Some("报告\n"), Some("gb18030"), Some(false)));
    assert!(client.request("files/read", json!({"session_id": id, "path": "src"})).await.is_err(), "a folder read as a file");

    // Watch the open file and its folder; save on the base it was read at.
    client.request("files/watch", json!({"session_id": id, "paths": ["src/app.ts", "src"]})).await.unwrap();
    let saved = client.request("files/write", json!({"session_id": id, "path": "src/app.ts", "text": "one\r\nTWO\r\n", "base_hash": app["hash"]})).await.unwrap();
    schema.check("FilesWriteResult", &saved);
    assert_eq!((saved["status"].as_str(), saved["hash"].as_str()), (Some("saved"), Some(sha256(b"one\r\nTWO\r\n").as_str())));
    assert_eq!(std::fs::read(project.join("src/app.ts")).unwrap(), b"one\r\nTWO\r\n");
    drain(&mut rx).await;

    // Changed from elsewhere: told; a save on the old base writes nothing.
    std::fs::write(project.join("src/app.ts"), "elsewhere\n").unwrap();
    let told = changed(&mut rx, &project.join("src/app.ts")).await;
    schema.check("FilesChangedParams", &told);
    assert_eq!(told["session_id"], json!(id));
    let conflict = client.request("files/write", json!({"session_id": id, "path": "src/app.ts", "text": "mine", "base_hash": saved["hash"]})).await.unwrap();
    assert_eq!((conflict["status"].as_str(), conflict["hash"].as_str()), (Some("conflict"), Some(sha256(b"elsewhere\n").as_str())));
    assert_eq!(std::fs::read_to_string(project.join("src/app.ts")).unwrap(), "elsewhere\n");
    // The person chose to keep theirs.
    let kept = client.request("files/write", json!({"session_id": id, "path": "src/app.ts", "text": "mine"})).await.unwrap();
    assert_eq!(kept["status"], "saved");
    assert_eq!(std::fs::read_to_string(project.join("src/app.ts")).unwrap(), "mine");
    drain(&mut rx).await;

    // A file new in a watched folder: told for the folder.
    std::fs::write(project.join("src/new.ts"), "x").unwrap();
    changed(&mut rx, &project.join("src")).await;

    // Watching nothing more: no more news.
    client.request("files/watch", json!({"session_id": id, "paths": []})).await.unwrap();
    drain(&mut rx).await;
    std::fs::write(project.join("src/app.ts"), "after").unwrap();
    let quiet = tokio::time::timeout(Duration::from_millis(1500), async {
        loop {
            if let Some(Incoming::Notification { method, .. }) = rx.recv().await
                && method == "files/changed"
            {
                break;
            }
        }
    })
    .await;
    assert!(quiet.is_err(), "told of a file no longer watched");

    // her's own data reads, but is not written.
    let logs = env.home.path().join("logs");
    std::fs::create_dir_all(&logs).unwrap();
    let refused = client.request("files/write", json!({"session_id": id, "path": logs.join("x.log"), "text": "x"})).await.unwrap();
    assert_eq!(refused["status"], "refused");
    assert!(!logs.join("x.log").exists());
    // A session that does not exist.
    assert!(client.request("files/list", json!({"session_id": "nope", "path": ""})).await.is_err());

    drop(client);
    let _ = child.kill().await;
}
