//! Parallel, over its hosted MCP server: free within its limits, or with the user's key at
//! another address (https://docs.parallel.ai/integrations/mcp/search-mcp). Its queries may say
//! `site:`, which it treats as a preference; there is no date filter on this tool, so the pipeline
//! does both and the model hears about the date. The conversation's id and the model's name are
//! optional here and are not sent.

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
use crate::web_search::pipeline::RawHit;

pub(super) struct Parallel;

static SPEC: Spec = Spec {
    id: "parallel",
    name: "Parallel",
    needs: WebSearchNeeds::Nothing,
    env: Some("PARALLEL_API_KEY"),
    key_url: Some("https://platform.parallel.ai/"),
    endpoint: "https://search.parallel.ai/mcp",
};

const TOOL: &str = "web_search";
/// Queries in one call, when several sites are asked for.
const MAX_SITE_QUERIES: usize = 3;

fn arguments(request: &SearchRequest) -> Value {
    let queries: Vec<String> = if request.allowed.is_empty() {
        vec![request.query.clone()]
    } else {
        request.allowed.iter().take(MAX_SITE_QUERIES).map(|domain| format!("{} site:{domain}", request.query)).collect()
    };
    json!({ "objective": request.query, "search_queries": queries })
}

fn parse(answer: &Value) -> Result<Vec<RawHit>, SearchError> {
    let results = answer.get("results").and_then(Value::as_array).ok_or_else(|| SearchError::BadResponse("no results".into()))?;
    Ok(results
        .iter()
        .map(|result| RawHit {
            title: text_of(result, "title"),
            url: text_of(result, "url"),
            date: opt_text(result, "publish_date"),
            passages: texts_of(result, "excerpts"),
        })
        .collect())
}

#[async_trait]
impl Adapter for Parallel {
    fn spec(&self) -> &'static Spec {
        &SPEC
    }

    async fn search(&self, request: &SearchRequest, net: &Net) -> Result<Found, SearchError> {
        let (url, headers): (String, BTreeMap<String, String>) = match &net.key {
            Some(key) => (format!("{}-oauth", net.endpoint), BTreeMap::from([("Authorization".to_string(), format!("Bearer {key}"))])),
            None => (net.endpoint.clone(), BTreeMap::new()),
        };
        let result = call_mcp(SPEC.id, &url, headers, TOOL, arguments(request), net).await?;
        Ok(Found { hits: parse(&tool_json(&result)?)?, recency_applied: false })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sites_become_queries() {
        let plain = arguments(&SearchRequest { query: "LPR 报价".into(), ..SearchRequest::default() });
        assert_eq!(plain["search_queries"], json!(["LPR 报价"]));
        assert_eq!(plain["objective"], "LPR 报价");
        assert!(plain.get("session_id").is_none() && plain.get("model_name").is_none());
        let sites = arguments(&SearchRequest {
            query: "三季报".into(),
            allowed: vec!["a.cn".into(), "b.cn".into(), "c.cn".into(), "d.cn".into()],
            ..SearchRequest::default()
        });
        assert_eq!(sites["search_queries"], json!(["三季报 site:a.cn", "三季报 site:b.cn", "三季报 site:c.cn"]));
    }

    #[test]
    fn a_real_answer_parses() {
        let answer: Value = serde_json::from_str(include_str!("../fixtures/parallel_off_target.json")).unwrap();
        let hits = parse(&answer).unwrap();
        assert_eq!(hits.len(), 4);
        assert_eq!(hits[0].date.as_deref(), Some("2026-10-30"));
        assert!(!hits[0].passages.is_empty());
    }
}
