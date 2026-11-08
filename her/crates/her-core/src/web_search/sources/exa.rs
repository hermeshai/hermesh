//! Exa, over its hosted MCP server: free within its limits, or with the user's key
//! (https://exa.ai/docs/get-started/exa-mcp). The advanced tool, opted into in the address,
//! answers in JSON and takes filters; the default one answers in text and takes none. `lite`
//! matched `auto` on freshness and facts, and stayed near a second where `auto` took up to 11.7 s.

use async_trait::async_trait;
use her_protocol::web_search::WebSearchNeeds;
use serde_json::Value;
use serde_json::json;
use std::collections::BTreeMap;

use super::Adapter;
use super::Found;
use super::Net;
use super::SearchError;
use super::SearchRequest;
use super::Spec;
use super::call_mcp;
use super::opt_text;
use super::text_of;
use super::texts_of;
use super::tool_json;
use crate::web_search::pipeline::MAX_RESULTS;
use crate::web_search::pipeline::RawHit;

pub(super) struct Exa;

static SPEC: Spec = Spec {
    id: "exa",
    name: "Exa",
    needs: WebSearchNeeds::Nothing,
    env: Some("EXA_API_KEY"),
    key_url: Some("https://dashboard.exa.ai/api-keys"),
    endpoint: "https://mcp.exa.ai/mcp?tools=web_search_advanced_exa",
};

const TOOL: &str = "web_search_advanced_exa";
/// Characters of highlights per page; the pipeline's budget in bytes decides what stays.
const HIGHLIGHT_CHARS: u32 = 1600;

fn arguments(request: &SearchRequest) -> Value {
    let mut arguments = json!({
        "query": request.query,
        "numResults": MAX_RESULTS,
        "type": "lite",
        "enableHighlights": true,
        "highlightsQuery": request.query,
        "highlightsMaxCharacters": HIGHLIGHT_CHARS,
    });
    if !request.allowed.is_empty() {
        arguments["includeDomains"] = json!(request.allowed);
    }
    if !request.blocked.is_empty() {
        arguments["excludeDomains"] = json!(request.blocked);
    }
    if let Some(since) = request.since {
        arguments["startPublishedDate"] = json!(since.to_string());
    }
    arguments
}

fn parse(answer: &Value) -> Result<Vec<RawHit>, SearchError> {
    let results = answer.get("results").and_then(Value::as_array).ok_or_else(|| SearchError::BadResponse("no results".into()))?;
    Ok(results
        .iter()
        .map(|result| {
            let mut passages = texts_of(result, "highlights");
            if passages.is_empty() {
                passages.extend(opt_text(result, "text"));
            }
            RawHit {
                title: text_of(result, "title"),
                url: text_of(result, "url"),
                date: opt_text(result, "publishedDate"),
                passages,
            }
        })
        .collect())
}

#[async_trait]
impl Adapter for Exa {
    fn spec(&self) -> &'static Spec {
        &SPEC
    }

    async fn search(&self, request: &SearchRequest, net: &Net) -> Result<Found, SearchError> {
        let headers: BTreeMap<String, String> = net.key.iter().map(|key| ("x-api-key".to_string(), key.clone())).collect();
        let result = call_mcp(SPEC.id, &net.endpoint, headers, TOOL, arguments(request), net).await?;
        Ok(Found { hits: parse(&tool_json(&result)?)?, recency_applied: true })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::NaiveDate;

    #[test]
    fn filters_go_as_the_service_names_them() {
        let request = SearchRequest {
            query: "贵州茅台".into(),
            allowed: vec!["cninfo.com.cn".into()],
            blocked: vec!["sina.com.cn".into()],
            since: NaiveDate::from_ymd_opt(2026, 10, 28),
            ..SearchRequest::default()
        };
        let a = arguments(&request);
        assert_eq!(a["includeDomains"], json!(["cninfo.com.cn"]));
        assert_eq!(a["excludeDomains"], json!(["sina.com.cn"]));
        assert_eq!(a["startPublishedDate"], "2026-10-28");
        assert_eq!(a["type"], "lite");
        assert_eq!(a["numResults"], 10);
        let plain = arguments(&SearchRequest { query: "x".into(), ..SearchRequest::default() });
        assert!(plain.get("includeDomains").is_none() && plain.get("startPublishedDate").is_none());
    }

    #[test]
    fn a_real_answer_parses() {
        let answer: Value = serde_json::from_str(include_str!("../fixtures/exa.json")).unwrap();
        let hits = parse(&answer).unwrap();
        assert_eq!(hits.len(), 4);
        assert!(hits[0].title.contains("第三季度报告"));
        assert_eq!(hits[0].date, None, "the filing has no date");
        assert_eq!(hits[1].date.as_deref(), Some("2026-10-30T00:00:00.000Z"));
        assert!(!hits[1].passages.is_empty());
    }
}
