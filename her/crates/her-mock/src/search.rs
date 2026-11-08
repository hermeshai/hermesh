//! The search services for tests: one answer in the shape each service's documentation gives,
//! with the same pages in it, so what the pipeline makes of them must come out the same. A page
//! comes twice (with a tracking parameter) and one is old, for the checks to catch; none of them
//! does any filtering, so whatever holds is the kernel's doing.

use parking_lot::Mutex;
use serde_json::Value;
use serde_json::json;
use std::collections::HashMap;
use std::net::SocketAddr;

use crate::REJECTED_KEY;
use crate::Request;

#[derive(Clone, Debug)]
pub struct SearchCall {
    pub service: String,
    /// The key it came with, if any.
    pub key: Option<String>,
    /// What the service was asked: the body, the query string's parameters, or the tool's arguments.
    pub request: Value,
}

#[derive(Default)]
pub(crate) struct SearchState {
    failing: Mutex<HashMap<String, u16>>,
    calls: Mutex<Vec<SearchCall>>,
}

impl SearchState {
    pub(crate) fn fail(&self, service: &str, status: u16) {
        let mut failing = self.failing.lock();
        if status == 0 {
            failing.remove(service);
        } else {
            failing.insert(service.to_string(), status);
        }
    }

    pub(crate) fn calls(&self) -> Vec<SearchCall> {
        self.calls.lock().clone()
    }
}

pub(crate) fn endpoints_toml(addr: SocketAddr) -> String {
    format!(
        "[web_search.endpoints]\nexa = \"http://{addr}/exa/mcp\"\nparallel = \"http://{addr}/parallel/mcp\"\ntavily = \"http://{addr}/tavily\"\nbrave = \"http://{addr}/brave\"\nbocha = \"http://{addr}/bocha\"\n"
    )
}

struct Page {
    title: &'static str,
    url: &'static str,
    date: Option<String>,
    text: String,
}

/// Yesterday's page, so that it stays within a week whenever the test runs. The words searched
/// for appear in the pages, without the operators some services take in the query.
fn pages(query: &str) -> Vec<Page> {
    let query = query.split_whitespace().filter(|w| *w != "OR" && !w.starts_with("site:") && !w.starts_with("-site:") && !w.starts_with("after:")).collect::<Vec<_>>().join(" ");
    let query = query.as_str();
    let recent = (chrono::Local::now() - chrono::Days::new(1)).format("%Y-%m-%d").to_string();
    vec![
        Page { title: "指南", url: "https://docs.example.com/guide", date: Some(recent.clone()), text: format!("关于 {query} 的说明。这里有数字 402.67。") },
        Page { title: "报道", url: "https://news.example.org/story?id=1", date: None, text: format!("{query} 的报道。") },
        Page { title: "指南", url: "https://docs.example.com/guide?utm_source=mock", date: Some(recent), text: format!("关于 {query} 的说明。这里有数字 402.67。") },
        Page { title: "旧闻", url: "https://old.example.net/archive", date: Some("2020-01-01".to_string()), text: format!("多年前的 {query}。") },
    ]
}

fn deepseek(query: &str) -> Value {
    let results: Vec<Value> = pages(query)
        .iter()
        .map(|p| json!({"rerank_score": 2.1, "title": p.title, "url": p.url, "snippet": "短", "summary": p.text, "content": "", "site_name": null, "site_icon": null, "date": p.date.as_ref().map(|d| format!("{d}T08:00:00Z")), "authority": 2}))
        .collect();
    json!({ "results": results })
}

fn tavily(query: &str) -> Value {
    let results: Vec<Value> = pages(query)
        .iter()
        .map(|p| json!({"title": p.title, "url": p.url, "content": p.text, "score": 0.9, "published_date": p.date, "raw_content": null}))
        .collect();
    json!({"query": query, "answer": null, "images": [], "results": results, "response_time": 0.5})
}

fn brave(query: &str) -> Value {
    let results: Vec<Value> = pages(query)
        .iter()
        .map(|p| json!({"title": format!("<strong>{}</strong>", p.title), "url": p.url, "description": format!("<strong>{}</strong>", p.text), "page_age": p.date.as_ref().map(|d| format!("{d}T08:00:00"))}))
        .collect();
    json!({"type": "search", "query": {"original": query}, "web": {"results": results}})
}

fn bocha(query: &str) -> Value {
    let value: Vec<Value> = pages(query)
        .iter()
        .map(|p| json!({"name": p.title, "url": p.url, "siteName": "x", "snippet": "短", "summary": p.text, "datePublished": p.date.as_ref().map(|d| format!("{d}T08:00:00+08:00"))}))
        .collect();
    json!({"code": 200, "msg": null, "data": {"webPages": {"value": value}}})
}

fn exa(query: &str) -> Value {
    let results: Vec<Value> = pages(query)
        .iter()
        .map(|p| {
            let mut result = json!({"id": p.url, "url": p.url, "title": p.title, "highlights": [p.text]});
            if let Some(date) = &p.date {
                result["publishedDate"] = json!(format!("{date}T00:00:00.000Z"));
            }
            result
        })
        .collect();
    json!({"requestId": "r", "results": results})
}

fn parallel(query: &str) -> Value {
    let results: Vec<Value> = pages(query).iter().map(|p| json!({"url": p.url, "title": p.title, "publish_date": p.date, "excerpts": [p.text]})).collect();
    json!({"search_id": "s", "results": results, "session_id": "generated"})
}

fn text_of(value: &Value, key: &str) -> String {
    value.get(key).and_then(Value::as_str).unwrap_or_default().to_string()
}

/// `a=b&c=d`, decoded, as an object.
fn query_string(path: &str) -> Value {
    let query = path.split_once('?').map_or("", |(_, q)| q);
    let pairs: serde_json::Map<String, Value> = url::form_urlencoded::parse(query.as_bytes()).map(|(k, v)| (k.into_owned(), Value::String(v.into_owned()))).collect();
    Value::Object(pairs)
}

/// What the request is for, when it is one for a search service. The status comes with a body.
pub(crate) fn answer(state: &SearchState, request: &Request) -> Option<(u16, Value)> {
    let path = request.path.as_str();
    let service = if path.ends_with("/v1/tools/search") {
        "deepseek"
    } else if path.starts_with("/tavily/") {
        "tavily"
    } else if path.starts_with("/brave/") {
        "brave"
    } else if path.starts_with("/bocha/") {
        "bocha"
    } else if path.starts_with("/exa/") {
        "exa"
    } else if path.starts_with("/parallel/") {
        "parallel"
    } else {
        return None;
    };
    let key = request.token.clone().or_else(|| request.authorization.as_deref().map(|a| a.trim_start_matches("Bearer ").to_string()));
    if key.as_deref() == Some(REJECTED_KEY) {
        return Some((401, json!({"error": "invalid key"})));
    }
    let failing = state.failing.lock().get(service).copied();
    if matches!(service, "exa" | "parallel") {
        return Some(mcp(state, service, request, key, failing));
    }
    if let Some(status) = failing {
        state.calls.lock().push(SearchCall { service: service.into(), key, request: json!({}) });
        return Some((status, json!({"error": "made to fail"})));
    }
    let asked: Value = if service == "brave" { query_string(path) } else { serde_json::from_slice(&request.body).unwrap_or(json!({})) };
    let query = if service == "brave" { text_of(&asked, "q") } else { text_of(&asked, "query") };
    state.calls.lock().push(SearchCall { service: service.into(), key, request: asked });
    Some((
        200,
        match service {
            "deepseek" => deepseek(&query),
            "tavily" => tavily(&query),
            "brave" => brave(&query),
            _ => bocha(&query),
        },
    ))
}

/// Exa and Parallel are MCP servers: this is the little of the protocol they need.
fn mcp(state: &SearchState, service: &str, request: &Request, key: Option<String>, failing: Option<u16>) -> (u16, Value) {
    if request.method != "POST" {
        return (405, json!({"error": "no stream here"}));
    }
    if let Some(status) = failing {
        return (status, json!({"error": "made to fail"}));
    }
    let message: Value = serde_json::from_slice(&request.body).unwrap_or(json!({}));
    let id = message.get("id").cloned().unwrap_or(Value::Null);
    let tool = if service == "exa" { "web_search_advanced_exa" } else { "web_search" };
    let result = match message.get("method").and_then(Value::as_str).unwrap_or_default() {
        "initialize" => json!({
            "protocolVersion": message["params"]["protocolVersion"],
            "capabilities": {"tools": {}},
            "serverInfo": {"name": service, "version": "1"},
        }),
        "tools/list" => json!({"tools": [{"name": tool, "description": "Search", "inputSchema": {"type": "object"}}]}),
        "tools/call" => {
            let arguments = message["params"]["arguments"].clone();
            let query = if service == "exa" { text_of(&arguments, "query") } else { text_of(&arguments, "objective") };
            state.calls.lock().push(SearchCall { service: service.into(), key, request: arguments });
            if service == "exa" {
                json!({"content": [{"type": "text", "text": exa(&query).to_string()}]})
            } else {
                let body = parallel(&query);
                json!({"content": [{"type": "text", "text": body.to_string()}], "structuredContent": body})
            }
        }
        // A notification.
        _ => return (202, json!({})),
    };
    (200, json!({"jsonrpc": "2.0", "id": id, "result": result}))
}

/// `QUERY|allowed=A,B|blocked=C|recency=week` as a call's arguments.
pub(crate) fn call_arguments(arg: &str) -> Value {
    let mut parts = arg.split('|');
    let mut arguments = json!({ "query": parts.next().unwrap_or_default() });
    for part in parts {
        match part.split_once('=') {
            Some(("allowed", list)) => arguments["allowed_domains"] = json!(list.split(',').collect::<Vec<_>>()),
            Some(("blocked", list)) => arguments["blocked_domains"] = json!(list.split(',').collect::<Vec<_>>()),
            Some(("recency", value)) => arguments["recency"] = json!(value),
            _ => {}
        }
    }
    arguments
}
