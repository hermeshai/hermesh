//! Tavily (https://docs.tavily.com/documentation/api-reference/endpoint/search). A basic search
//! costs one credit; the service's own answer and raw pages stay off.

use async_trait::async_trait;
use her_protocol::web_search::WebSearchNeeds;
use serde_json::Value;
use serde_json::json;

use super::Adapter;
use super::Found;
use super::Net;
use super::Recency;
use super::SearchError;
use super::SearchRequest;
use super::Spec;
use super::json_answer;
use super::opt_text;
use super::text_of;
use crate::web_search::pipeline::MAX_RESULTS;
use crate::web_search::pipeline::RawHit;

pub(super) struct Tavily;

static SPEC: Spec = Spec {
    id: "tavily",
    name: "Tavily",
    needs: WebSearchNeeds::Key,
    env: Some("TAVILY_API_KEY"),
    key_url: Some("https://app.tavily.com/home"),
    endpoint: "https://api.tavily.com",
};

fn body(request: &SearchRequest) -> Value {
    let mut body = json!({
        "query": request.query,
        "max_results": MAX_RESULTS,
        "search_depth": "basic",
        "include_published_date": true,
    });
    if !request.allowed.is_empty() {
        body["include_domains"] = json!(request.allowed);
    }
    if !request.blocked.is_empty() {
        body["exclude_domains"] = json!(request.blocked);
    }
    if let Some(recency) = request.recency {
        body["time_range"] = json!(match recency {
            Recency::Day => "day",
            Recency::Week => "week",
            Recency::Month => "month",
            Recency::Year => "year",
        });
    }
    body
}

fn parse(answer: &Value) -> Result<Vec<RawHit>, SearchError> {
    let results = answer.get("results").and_then(Value::as_array).ok_or_else(|| SearchError::BadResponse("no results".into()))?;
    Ok(results
        .iter()
        .map(|result| RawHit {
            title: text_of(result, "title"),
            url: text_of(result, "url"),
            date: opt_text(result, "published_date"),
            passages: opt_text(result, "content").into_iter().collect(),
        })
        .collect())
}

#[async_trait]
impl Adapter for Tavily {
    fn spec(&self) -> &'static Spec {
        &SPEC
    }

    async fn search(&self, request: &SearchRequest, net: &Net) -> Result<Found, SearchError> {
        let call = net.http.post(format!("{}/search", net.endpoint)).bearer_auth(net.key.as_deref().unwrap_or_default()).json(&body(request));
        Ok(Found { hits: parse(&json_answer(call).await?)?, recency_applied: true })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn filters_go_as_the_service_names_them() {
        let request = SearchRequest {
            query: "q".into(),
            allowed: vec!["a.cn".into()],
            blocked: vec!["b.cn".into()],
            recency: Some(Recency::Week),
            ..SearchRequest::default()
        };
        let b = body(&request);
        assert_eq!(b["include_domains"], json!(["a.cn"]));
        assert_eq!(b["exclude_domains"], json!(["b.cn"]));
        assert_eq!(b["time_range"], "week");
        assert_eq!(b["search_depth"], "basic");
        assert!(b.get("include_answer").is_none());
    }

    #[test]
    fn its_answer_parses() {
        let hits = parse(&json!({"query": "q", "results": [{"title": "t", "url": "https://a.test", "content": "c", "score": 0.9, "published_date": "2026-10-30"}], "response_time": 1.2})).unwrap();
        assert_eq!(hits[0].passages, ["c"]);
        assert_eq!(hits[0].date.as_deref(), Some("2026-10-30"));
        assert!(matches!(parse(&json!({"detail": "x"})), Err(SearchError::BadResponse(_))));
    }
}
