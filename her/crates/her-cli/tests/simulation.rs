//! Real HTTP model streaming + real MCP + the production CLI and kernel.
mod common;
use her_mock::mcp::{McpTestOptions, serve_http};
use her_mock::simulation::scenarios;
use serde_json::Value;

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn research_and_coding_assets_cross_the_real_cli_model_and_tool_boundaries() {
    let tools = serve_http(
        "127.0.0.1:0",
        McpTestOptions {
            simulation: true,
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let config = format!(
        "[mcp_servers.sim]\nurl = {:?}\nexposure = \"direct\"\nrequired = true\n",
        tools.url()
    );
    let env = common::TestEnv::new("bypass-permissions", &config).await;
    for case in scenarios().iter().filter(|s| s.turns == 1) {
        let output = env
            .her(&["exec", "--output", "stream-json", &case.prompt("e2e", 0)])
            .output()
            .await
            .unwrap();
        let events: Vec<Value> = String::from_utf8(output.stdout)
            .unwrap()
            .lines()
            .map(|l| serde_json::from_str(l).unwrap())
            .collect();
        assert!(
            output.status.success(),
            "{}\n{events:#?}",
            String::from_utf8_lossy(&output.stderr)
        );
        common::assert_increasing(&events);
        let end = events
            .iter()
            .find(|v| v["type"] == "task_completed")
            .unwrap();
        assert_eq!(end["last_message"], case.expected_reply("e2e", 0));
        assert_eq!(
            events
                .iter()
                .filter(|v| v["type"] == "tool_started")
                .count(),
            case.calls_per_turn()
        );
        let audit = tools.simulation().finish(&case.id, "e2e");
        assert_eq!(audit["completed_turns"], 1);
        assert_eq!(audit["artifacts"].as_object().unwrap().len(), 1);
    }
    assert_eq!(tools.simulation().violations(), 0);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn corrupted_tool_evidence_fails_the_real_task_instead_of_reporting_success() {
    let tools = serve_http(
        "127.0.0.1:0",
        McpTestOptions {
            simulation: true,
            ..Default::default()
        },
    )
    .await
    .unwrap();
    tools.simulation().corrupt_next_result();
    let config = format!(
        "[mcp_servers.sim]\nurl = {:?}\nexposure = \"direct\"\nrequired = true\n",
        tools.url()
    );
    let env = common::TestEnv::new("bypass-permissions", &config).await;
    let case = her_mock::simulation::scenario("macro").unwrap();
    let output = env
        .her(&["exec", "--output", "stream-json", &case.prompt("corrupt", 0)])
        .output()
        .await
        .unwrap();
    assert_eq!(output.status.code(), Some(1));
    let text = String::from_utf8(output.stdout).unwrap();
    let events: Vec<Value> = text
        .lines()
        .map(|l| serde_json::from_str(l).unwrap())
        .collect();
    assert!(events.iter().any(|e| e["type"] == "task_failed"));
    assert!(!events.iter().any(|e| e["type"] == "task_completed"));
    assert!(text.contains("simulation contract violation"));
    assert_eq!(
        tools.simulation().finish("macro", "corrupt")["tool_calls"],
        1
    );
}
