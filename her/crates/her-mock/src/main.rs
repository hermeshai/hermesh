//! `her-mock [ADDR] [--simulation]`: model gateway, optionally with companion MCP tools.
use std::io::Write;

#[tokio::main(flavor = "multi_thread", worker_threads = 4)]
async fn main() -> std::io::Result<()> {
    let mut addr = "127.0.0.1:0".to_string();
    let mut simulation = false;
    for arg in std::env::args().skip(1) {
        match arg.as_str() {
            "--simulation" => simulation = true,
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
    out.flush()?;
    drop(out);
    tokio::signal::ctrl_c().await?;
    Ok(())
}
