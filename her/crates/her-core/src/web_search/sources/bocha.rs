//! 博查 (https://open.bochaai.com/). Its answer carries its own `code`, which can say more than the
//! HTTP status does, and some answers come without the `data` wrapper.

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
use super::status_error;
use super::text_of;
use crate::web_search::pipeline::MAX_RESULTS;
use crate::web_search::pipeline::RawHit;

pub(super) struct Bocha;

static SPEC: Spec = Spec {
    id: "bocha",
    name: "博查",
    needs: WebSearchNeeds::Key,
    env: Some("BOCHA_API_KEY"),
    key_url: Some("https://open.bochaai.com/"),
    endpoint: "https://api.bochaai.com",
};

fn body(request: &SearchRequest) -> Value {
    let mut body = json!({ "query": request.query, "count": MAX_RESULTS, "summary": true });
    if !request.allowed.is_empty() {
        body["include"] = json!(request.allowed.join("|"));
    }
    if !request.blocked.is_empty() {
        body["exclude"] = json!(request.blocked.join("|"));
    }
    if let Some(recency) = request.recency {
        body["freshness"] = json!(match recency {
            Recency::Day => "oneDay",
            Recency::Week => "oneWeek",
            Recency::Month => "oneMonth",
            Recency::Year => "oneYear",
        });
    }
    body
}

fn parse(answer: &Value) -> Result<Vec<RawHit>, SearchError> {
    if !answer.is_object() {
        return Err(SearchError::BadResponse("not an object".into()));
    }
    let code = answer.get("code").and_then(|code| code.as_u64().or_else(|| code.as_str().and_then(|text| text.parse().ok())));
    if let Some(code) = code.filter(|code| *code != 200) {
        return Err(status_error(code as u16, &answer.to_string()));
    }
    let data = answer.get("data").unwrap_or(answer);
    // A search that found nothing may have no pages at all.
    let Some(pages) = data.get("webPages") else {
        return if code == Some(200) || answer.get("data").is_some() { Ok(Vec::new()) } else { Err(SearchError::BadResponse("no results".into())) };
    };
    let results = pages.get("value").and_then(Value::as_array).ok_or_else(|| SearchError::BadResponse("no results".into()))?;
    Ok(results
        .iter()
        .map(|result| RawHit {
            title: text_of(result, "name"),
            url: text_of(result, "url"),
            date: opt_text(result, "datePublished"),
            passages: opt_text(result, "summary").or_else(|| opt_text(result, "snippet")).into_iter().collect(),
        })
        .collect())
}

#[async_trait]
impl Adapter for Bocha {
    fn spec(&self) -> &'static Spec {
        &SPEC
    }

    async fn search(&self, request: &SearchRequest, net: &Net) -> Result<Found, SearchError> {
        let call = net.http.post(format!("{}/v1/web-search", net.endpoint)).bearer_auth(net.key.as_deref().unwrap_or_default()).json(&body(request));
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
            allowed: vec!["a.cn".into(), "b.cn".into()],
            blocked: vec!["c.cn".into()],
            recency: Some(Recency::Year),
            ..SearchRequest::default()
        };
        let b = body(&request);
        assert_eq!(b["include"], "a.cn|b.cn");
        assert_eq!(b["exclude"], "c.cn");
        assert_eq!(b["freshness"], "oneYear");
        assert_eq!(b["summary"], true);
    }

    #[test]
    fn its_answer_parses_with_or_without_the_wrapper() {
        let page = json!({"name": "t", "url": "https://a.test", "siteName": "a", "snippet": "短", "summary": "长", "datePublished": "2026-10-30T12:00:00+08:00"});
        for answer in [
            json!({"code": 200, "msg": null, "data": {"webPages": {"value": [page.clone()]}}}),
            json!({"webPages": {"value": [page.clone()]}}),
        ] {
            let hits = parse(&answer).unwrap();
            assert_eq!(hits[0].title, "t");
            assert_eq!(hits[0].passages, ["长"]);
        }
        assert!(parse(&json!({"code": 200, "data": {}})).unwrap().is_empty());
    }

    #[test]
    fn its_own_code_is_understood() {
        assert_eq!(parse(&json!({"code": 401, "msg": "Invalid API KEY"})).unwrap_err(), SearchError::Auth);
        assert_eq!(parse(&json!({"code": "403", "message": "You do not have enough money"})).unwrap_err(), SearchError::Quota);
        assert_eq!(parse(&json!({"code": 429, "msg": "too many"})).unwrap_err(), SearchError::RateLimited);
        assert!(matches!(parse(&json!({"hello": "world"})), Err(SearchError::BadResponse(_))));
    }
}
