//! The fixture site of one run: the case's files over HTTP on a loopback port, and a log of
//! every request to `/api/…` with its fields, which is what checks look at.
//!
//! Loopback needs no approval from the browser tools, so `her exec` can do the whole task
//! without anyone answering prompts. One site per run gives each run its own origin, so
//! cookies and storage left by an earlier run never leak into the next one.

use parking_lot::Mutex;
use serde::Serialize;
use std::collections::BTreeMap;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;
use tokio::io::AsyncReadExt;
use tokio::io::AsyncWriteExt;
use tokio::net::TcpListener;
use tokio::net::TcpStream;
use tokio::task::JoinHandle;

/// Largest request read, headers and body.
const MAX_REQUEST: usize = 1 << 20;

/// A request to `/api/…`.
#[derive(Debug, Clone, Serialize)]
pub struct Request {
    pub method: String,
    /// Without the query.
    pub path: String,
    /// Query, form and JSON-object fields; non-string JSON values as JSON text.
    pub fields: BTreeMap<String, String>,
}

impl Request {
    /// `METHOD /path`, as checks name a request.
    pub fn line(&self) -> String {
        format!("{} {}", self.method, self.path)
    }
}

pub struct Site {
    pub base: String,
    log: Arc<Mutex<Vec<Request>>>,
    task: JoinHandle<()>,
}

impl Site {
    pub async fn start(root: &Path) -> std::io::Result<Site> {
        let listener = TcpListener::bind("127.0.0.1:0").await?;
        let base = format!("http://{}", listener.local_addr()?);
        let log = Arc::new(Mutex::new(Vec::new()));
        let root = root.to_path_buf();
        let task = tokio::spawn({
            let log = log.clone();
            async move {
                while let Ok((stream, _)) = listener.accept().await {
                    tokio::spawn(serve(stream, root.clone(), log.clone()));
                }
            }
        });
        Ok(Site { base, log, task })
    }

    pub fn requests(&self) -> Vec<Request> {
        self.log.lock().clone()
    }
}

impl Drop for Site {
    fn drop(&mut self) {
        self.task.abort();
    }
}

async fn serve(mut stream: TcpStream, root: PathBuf, log: Arc<Mutex<Vec<Request>>>) {
    let Some((head, body)) = read_request(&mut stream).await else {
        return;
    };
    let mut first = head.lines().next().unwrap_or_default().split_whitespace();
    let (method, target) = (first.next().unwrap_or_default(), first.next().unwrap_or("/"));
    let (path, query) = target.split_once('?').unwrap_or((target, ""));
    let content_type = header(&head, "content-type").unwrap_or_default();

    let (status, kind, content) = if path.starts_with("/api/") {
        let mut fields = form_fields(query);
        if content_type.starts_with("application/json") {
            fields.extend(json_fields(&body));
        } else {
            fields.extend(form_fields(&String::from_utf8_lossy(&body)));
        }
        log.lock().push(Request {
            method: method.to_string(),
            path: path.to_string(),
            fields,
        });
        // A form posted here lands on a page that says so; fetch() callers ignore it.
        (200, "text/html; charset=utf-8", b"<!doctype html><title>Received</title><h1>Received</h1>".to_vec())
    } else {
        match file(&root, path) {
            Some((kind, content)) => (200, kind, content),
            None => (404, "text/plain", b"not found".to_vec()),
        }
    };
    let reason = if status == 200 { "OK" } else { "Not Found" };
    let head = format!(
        "HTTP/1.1 {status} {reason}\r\nContent-Type: {kind}\r\nContent-Length: {}\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n",
        content.len()
    );
    let _ = stream.write_all(head.as_bytes()).await;
    let _ = stream.write_all(&content).await;
    let _ = stream.shutdown().await;
}

async fn read_request(stream: &mut TcpStream) -> Option<(String, Vec<u8>)> {
    let mut buf = Vec::new();
    let mut chunk = [0u8; 8192];
    loop {
        if let Some(end) = buf.windows(4).position(|w| w == b"\r\n\r\n") {
            let head = String::from_utf8_lossy(&buf[..end]).into_owned();
            let length: usize = header(&head, "content-length").and_then(|v| v.parse().ok()).unwrap_or(0);
            let mut body = buf[end + 4..].to_vec();
            while body.len() < length.min(MAX_REQUEST) {
                let n = stream.read(&mut chunk).await.ok().filter(|n| *n > 0)?;
                body.extend_from_slice(&chunk[..n]);
            }
            return Some((head, body));
        }
        if buf.len() > MAX_REQUEST {
            return None;
        }
        let n = stream.read(&mut chunk).await.ok().filter(|n| *n > 0)?;
        buf.extend_from_slice(&chunk[..n]);
    }
}

fn header(head: &str, name: &str) -> Option<String> {
    head.lines()
        .skip(1)
        .filter_map(|line| line.split_once(':'))
        .find(|(key, _)| key.trim().eq_ignore_ascii_case(name))
        .map(|(_, value)| value.trim().to_string())
}

fn form_fields(text: &str) -> BTreeMap<String, String> {
    url::form_urlencoded::parse(text.as_bytes()).into_owned().collect()
}

fn json_fields(body: &[u8]) -> BTreeMap<String, String> {
    let Ok(serde_json::Value::Object(map)) = serde_json::from_slice(body) else {
        return BTreeMap::new();
    };
    map.into_iter()
        .map(|(key, value)| match value {
            serde_json::Value::String(text) => (key, text),
            other => (key, other.to_string()),
        })
        .collect()
}

/// A file of the case directory: `/` is `index.html`; nothing outside the directory, and
/// never `case.toml`, which holds the expected answers.
fn file(root: &Path, path: &str) -> Option<(&'static str, Vec<u8>)> {
    let relative = path.trim_start_matches('/');
    let relative = if relative.is_empty() { "index.html" } else { relative };
    if relative == "case.toml" || relative.split('/').any(|part| part == ".." || part.is_empty()) {
        return None;
    }
    let content = std::fs::read(root.join(relative)).ok()?;
    let kind = match relative.rsplit('.').next() {
        Some("html") => "text/html; charset=utf-8",
        Some("js") => "text/javascript",
        Some("css") => "text/css",
        Some("json") => "application/json",
        Some("png") => "image/png",
        Some("svg") => "image/svg+xml",
        _ => "application/octet-stream",
    };
    Some((kind, content))
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::AsyncReadExt;

    async fn send(base: &str, raw: String) -> String {
        let mut stream = TcpStream::connect(base.trim_start_matches("http://")).await.unwrap();
        stream.write_all(raw.as_bytes()).await.unwrap();
        let mut reply = String::new();
        stream.read_to_string(&mut reply).await.unwrap();
        reply
    }

    #[tokio::test]
    async fn serves_files_and_logs_api_requests_with_their_fields() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("index.html"), "<h1>Home</h1>").unwrap();
        std::fs::write(dir.path().join("case.toml"), "secret").unwrap();
        let site = Site::start(dir.path()).await.unwrap();

        assert!(send(&site.base, "GET / HTTP/1.1\r\n\r\n".into()).await.contains("<h1>Home</h1>"));
        assert!(send(&site.base, "GET /case.toml HTTP/1.1\r\n\r\n".into()).await.starts_with("HTTP/1.1 404"));
        assert!(send(&site.base, "GET /../x HTTP/1.1\r\n\r\n".into()).await.starts_with("HTTP/1.1 404"));

        let form = "user=ada+l&plan=pro";
        send(
            &site.base,
            format!(
                "POST /api/signup?ref=home HTTP/1.1\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: {}\r\n\r\n{form}",
                form.len()
            ),
        )
        .await;
        let json = r#"{"id":"b7","qty":2}"#;
        send(
            &site.base,
            format!("POST /api/click HTTP/1.1\r\ncontent-type: application/json\r\ncontent-length: {}\r\n\r\n{json}", json.len()),
        )
        .await;

        let requests = site.requests();
        assert_eq!(requests.len(), 2);
        assert_eq!(requests[0].line(), "POST /api/signup");
        assert_eq!(requests[0].fields["user"], "ada l");
        assert_eq!(requests[0].fields["ref"], "home");
        assert_eq!(requests[1].fields["id"], "b7");
        assert_eq!(requests[1].fields["qty"], "2");
    }
}
