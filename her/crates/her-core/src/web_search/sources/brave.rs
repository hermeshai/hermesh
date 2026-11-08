//! Brave Search (https://api-dashboard.search.brave.com/app/documentation/web-search/query). Sites
//! are operators inside the query; its excerpts mark the words searched for in HTML, which the
//! pipeline removes.

use async_trait::async_trait;
use her_protocol::web_search::WebSearchNeeds;
use serde_json::Value;

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
use super::texts_of;
use crate::web_search::pipeline::MAX_RESULTS;
use crate::web_search::pipeline::RawHit;

pub(super) struct Brave;

static SPEC: Spec = Spec {
    id: "brave",
    name: "Brave",
    needs: WebSearchNeeds::Key,
    env: Some("BRAVE_SEARCH_API_KEY"),
    key_url: Some("https://api-dashboard.search.brave.com/app/keys"),
    endpoint: "https://api.search.brave.com",
};

fn parameters(request: &SearchRequest) -> Vec<(&'static str, String)> {
    let mut query = request.query.clone();
    for (i, domain) in request.allowed.iter().enumerate() {
        query.push_str(&if i == 0 { format!(" site:{domain}") } else { format!(" OR site:{domain}") });
    }
    for domain in &request.blocked {
        query.push_str(&format!(" -site:{domain}"));
    }
    let mut parameters = vec![("q", query), ("count", MAX_RESULTS.to_string()), ("extra_snippets", "true".to_string())];
    if let Some(recency) = request.recency {
        let freshness = match recency {
            Recency::Day => "pd",
            Recency::Week => "pw",
            Recency::Month => "pm",
            Recency::Year => "py",
        };
        parameters.push(("freshness", freshness.to_string()));
    }
    parameters
}

fn parse(answer: &Value) -> Result<Vec<RawHit>, SearchError> {
    if !answer.is_object() {
        return Err(SearchError::BadResponse("not an object".into()));
    }
    // A search that found nothing has no `web` at all.
    let Some(web) = answer.get("web") else {
        return Ok(Vec::new());
    };
    let results = web.get("results").and_then(Value::as_array).ok_or_else(|| SearchError::BadResponse("no results".into()))?;
    Ok(results
        .iter()
        .map(|result| {
            let mut passages: Vec<String> = opt_text(result, "description").into_iter().collect();
            passages.extend(texts_of(result, "extra_snippets"));
            RawHit {
                title: text_of(result, "title"),
                url: text_of(result, "url"),
                date: opt_text(result, "page_age"),
                passages,
            }
        })
        .collect())
}

#[async_trait]
impl Adapter for Brave {
    fn spec(&self) -> &'static Spec {
        &SPEC
    }

    async fn search(&self, request: &SearchRequest, net: &Net) -> Result<Found, SearchError> {
        let call = net
            .http
            .get(format!("{}/res/v1/web/search", net.endpoint))
            .query(&parameters(request))
            .header("Accept", "application/json")
            .header("X-Subscription-Token", net.key.as_deref().unwrap_or_default());
        Ok(Found { hits: parse(&json_answer(call).await?)?, recency_applied: true })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn sites_join_the_query_and_recency_is_freshness() {
        let request = SearchRequest {
            query: "q".into(),
            allowed: vec!["a.cn".into(), "b.cn".into()],
            blocked: vec!["c.cn".into()],
            recency: Some(Recency::Month),
            ..SearchRequest::default()
        };
        let p = parameters(&request);
        assert_eq!(p[0], ("q", "q site:a.cn OR site:b.cn -site:c.cn".to_string()));
        assert!(p.contains(&("freshness", "pm".to_string())));
        assert!(p.contains(&("extra_snippets", "true".to_string())));
    }

    #[test]
    fn its_answer_parses_and_nothing_found_is_no_failure() {
        let hits = parse(&json!({"type": "search", "web": {"results": [
            {"title": "t", "url": "https://a.test", "description": "<strong>d</strong>", "extra_snippets": ["e1", "e2"], "page_age": "2026-10-30T08:00:00"},
        ]}})).unwrap();
        assert_eq!(hits[0].passages, ["<strong>d</strong>", "e1", "e2"]);
        assert_eq!(hits[0].date.as_deref(), Some("2026-10-30T08:00:00"));
        assert!(parse(&json!({"type": "search", "query": {"original": "q"}})).unwrap().is_empty());
        assert!(matches!(parse(&json!([])), Err(SearchError::BadResponse(_))));
    }
}
