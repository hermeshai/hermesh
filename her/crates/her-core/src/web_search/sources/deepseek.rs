//! DeepSeek's Search API, with the key of the DeepSeek model account
//! (https://api-docs.deepseek.com/guides/search). Filters are operators inside the query, which
//! counts toward its 128 characters.

use async_trait::async_trait;
use her_protocol::web_search::WebSearchNeeds;
use serde_json::Value;
use serde_json::json;

use super::Adapter;
use super::Found;
use super::Net;
use super::SearchError;
use super::SearchRequest;
use super::Spec;
use super::json_answer;
use super::opt_text;
use super::text_of;
use crate::web_search::pipeline::MAX_QUERY_CHARS;
use crate::web_search::pipeline::MAX_RESULTS;
use crate::web_search::pipeline::RawHit;

pub(super) struct DeepSeek;

static SPEC: Spec = Spec {
    id: "deepseek",
    name: "DeepSeek",
    needs: WebSearchNeeds::Account,
    env: None,
    key_url: None,
    endpoint: "",
};

/// The query with as many filters as fit: the date first, then the sites to keep, then the ones
/// to leave out. What does not fit is left to the pipeline.
fn query_with_operators(request: &SearchRequest) -> (String, bool) {
    let mut query = request.query.clone();
    let mut add = |word: String| {
        let fits = query.chars().count() + 1 + word.chars().count() <= MAX_QUERY_CHARS;
        if fits {
            query.push(' ');
            query.push_str(&word);
        }
        fits
    };
    let recency_applied = request.since.is_some_and(|since| add(format!("after:{since}")));
    for (i, domain) in request.allowed.iter().enumerate() {
        if !add(if i == 0 { format!("site:{domain}") } else { format!("OR site:{domain}") }) {
            break;
        }
    }
    for domain in &request.blocked {
        if !add(format!("-site:{domain}")) {
            break;
        }
    }
    (query, recency_applied)
}

fn parse(answer: &Value) -> Result<Vec<RawHit>, SearchError> {
    let results = answer.get("results").and_then(Value::as_array).ok_or_else(|| SearchError::BadResponse("no results".into()))?;
    Ok(results
        .iter()
        .map(|result| RawHit {
            title: text_of(result, "title"),
            url: text_of(result, "url"),
            date: opt_text(result, "date"),
            // The long text meant for models, else the short one meant for lists.
            passages: opt_text(result, "summary").or_else(|| opt_text(result, "snippet")).into_iter().collect(),
        })
        .collect())
}

#[async_trait]
impl Adapter for DeepSeek {
    fn spec(&self) -> &'static Spec {
        &SPEC
    }

    async fn search(&self, request: &SearchRequest, net: &Net) -> Result<Found, SearchError> {
        let (query, recency_applied) = query_with_operators(request);
        let body = json!({ "query": query, "count": MAX_RESULTS, "need_content": false });
        let call = net.http.post(format!("{}/v1/tools/search", net.endpoint)).bearer_auth(net.key.as_deref().unwrap_or_default()).json(&body);
        Ok(Found { hits: parse(&json_answer(call).await?)?, recency_applied })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::NaiveDate;

    fn request(query: &str) -> SearchRequest {
        SearchRequest { query: query.into(), ..SearchRequest::default() }
    }

    #[test]
    fn filters_become_operators() {
        let mut r = request("贵州茅台 三季报");
        r.allowed = vec!["cninfo.com.cn".into(), "sse.com.cn".into()];
        r.blocked = vec!["sina.com.cn".into()];
        r.since = NaiveDate::from_ymd_opt(2026, 10, 28);
        let (query, applied) = query_with_operators(&r);
        assert_eq!(query, "贵州茅台 三季报 after:2026-10-28 site:cninfo.com.cn OR site:sse.com.cn -site:sina.com.cn");
        assert!(applied);
    }

    #[test]
    fn what_does_not_fit_is_left_out_and_the_query_stays_within_the_limit() {
        let mut r = request(&"词".repeat(120));
        r.allowed = vec!["cninfo.com.cn".into()];
        r.blocked = vec!["a.cn".into()];
        r.since = NaiveDate::from_ymd_opt(2026, 10, 28);
        let (query, applied) = query_with_operators(&r);
        assert!(query.chars().count() <= MAX_QUERY_CHARS, "{}", query.chars().count());
        assert!(!applied, "the date did not fit: the pipeline checks it, and the model is told");
        assert!(!query.contains("site:cninfo"));
    }

    #[test]
    fn the_long_text_is_preferred() {
        let hits = parse(&json!({"results": [
            {"title": "t", "url": "https://a.test", "snippet": "短", "summary": "长", "date": "2026-10-30T00:00:00Z"},
            {"title": "t2", "url": "https://b.test", "snippet": "短", "summary": "", "date": null},
        ]})).unwrap();
        assert_eq!(hits[0].passages, ["长"]);
        assert_eq!(hits[0].date.as_deref(), Some("2026-10-30T00:00:00Z"));
        assert_eq!(hits[1].passages, ["短"]);
        assert_eq!(hits[1].date, None);
        assert!(matches!(parse(&json!({"error": "x"})), Err(SearchError::BadResponse(_))));
        assert!(parse(&json!({"results": []})).unwrap().is_empty());
    }
}
