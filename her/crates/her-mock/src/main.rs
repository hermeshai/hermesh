//! `her-mock [ADDR] [--simulation | --tools]`: model gateway, optionally with companion MCP tools: the
//! simulation's, or the test server's own (echo, env…) for a connector added by its address: open to anyone, and
//! twice more behind a sign-in, one that wants a token and one that signs in with an account.
use std::io::Write;

/// The token the test server behind a token takes.
const TEST_TOKEN: &str = "her-mock-token";

#[tokio::main(flavor = "multi_thread", worker_threads = 4)]
async fn main() -> std::io::Result<()> {
    let mut addr = "127.0.0.1:0".to_string();
    let mut simulation = false;
    let mut tools = false;
    for arg in std::env::args().skip(1) {
        match arg.as_str() {
            "--simulation" => simulation = true,
            "--tools" => tools = true,
            "--list-scenarios" => {
                let cases: Vec<_> = her_mock::simulation::scenarios().iter().map(|s| serde_json::json!({
                    "id":s.id,"version":s.version,"title":s.title,"turns":s.turns,"steps":s.steps.len(),"tool_calls_per_turn":s.calls_per_turn()
                })).collect();
                writeln!(
                    std::io::stdout(),
                    "{}",
                    serde_json::json!({"asset_sha256":her_mock::simulation::asset_digest(),"scenarios":cases})
                )?;
                return Ok(());
            }
            flag if flag.starts_with('-') => {
                return Err(std::io::Error::other(format!("unknown option: {flag}")));
            }
            _ => addr = arg,
        }
    }
    let server = her_mock::MockServer::bind(&addr).await?;
    let companion = if simulation {
        Some(
            her_mock::mcp::serve_http(
                "127.0.0.1:0",
                her_mock::mcp::McpTestOptions {
                    name: "sim".into(),
                    simulation: true,
                    ..Default::default()
                },
            )
            .await?,
        )
    } else {
        None
    };
    let test_tools = if tools { Some(her_mock::mcp::serve_http("127.0.0.1:0", her_mock::mcp::McpTestOptions::default()).await?) } else { None };
    // The same tools behind a sign-in, for a connector that asks for one: a token, and an account (the client
    // registers itself, so that nothing outside this machine is asked).
    let signed_in = if tools {
        let token = her_mock::mcp::McpTestOptions { auth: her_mock::mcp_auth::McpTestAuth::Token(TEST_TOKEN.into()), ..Default::default() };
        let account = her_mock::mcp::McpTestOptions {
            auth: her_mock::mcp_auth::McpTestAuth::OAuth(her_mock::mcp_auth::OAuthOptions { metadata_document: false, ..Default::default() }),
            ..Default::default()
        };
        Some((her_mock::mcp::serve_http("127.0.0.1:0", token).await?, her_mock::mcp::serve_http("127.0.0.1:0", account).await?))
    } else {
        None
    };
    let mut out = std::io::stdout().lock();
    writeln!(
        out,
        "# models.toml (model: mock/mock-1)\n{}",
        server.models_toml()
    )?;
    if let Some(tools) = &companion {
        writeln!(
            out,
            "# Add the following to config.toml\n[mcp_servers.sim]\nurl = {:?}\nexposure = \"direct\"\nrequired = true",
            tools.url()
        )?;
    }
    if let Some(tools) = &test_tools {
        writeln!(out, "# MCP test tools\nmcp_url = {:?}", tools.url())?;
    }
    if let Some((token, account)) = &signed_in {
        writeln!(out, "mcp_token_url = {:?}\nmcp_token = {:?}\nmcp_account_url = {:?}", token.url(), TEST_TOKEN, account.url())?;
    }
    out.flush()?;
    drop(out);
    tokio::signal::ctrl_c().await?;
    Ok(())
}
