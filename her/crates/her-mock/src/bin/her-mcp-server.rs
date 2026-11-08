//! `her-mcp-server [options]`: the scriptable MCP test server (see `her_mock::mcp`).
//!
//! Options: `--name NAME`, `--instructions TEXT`, `--tools N` (generated tools),
//! `--domain NAME` (only that domain's generated tools), `--page-size N`, `--log PATH`, `--stderr-lines N` (noise on stderr at start-up),
//! `--http ADDR` (Streamable HTTP instead of stdio; prints the URL). Over HTTP:
//! `--sse` (the older HTTP+SSE transport), `--token TOKEN` (a fixed bearer token),
//! `--oauth` (an OAuth authorization server beside it), with `--no-metadata-document`,
//! `--no-registration` and `--token-ttl SECONDS`; `--public-host HOST` (the host clients reach
//! it by, in its URL and its sign-in's addresses).

use her_mock::mcp::McpTestOptions;
use her_mock::mcp_auth::McpTestAuth;
use her_mock::mcp_auth::OAuthOptions;
use std::io::Write;

#[tokio::main(flavor = "multi_thread", worker_threads = 2)]
async fn main() -> std::io::Result<()> {
    let mut options = McpTestOptions {
        own_process: true,
        ..Default::default()
    };
    let mut http: Option<String> = None;
    let mut stderr_lines = 0usize;
    let mut oauth: Option<OAuthOptions> = None;
    let mut args = std::env::args().skip(1);
    while let Some(arg) = args.next() {
        let mut value = || args.next().unwrap_or_default();
        match arg.as_str() {
            "--name" => options.name = value(),
            "--instructions" => options.instructions = Some(value()).filter(|v| !v.is_empty()),
            "--tools" => options.generated = value().parse().unwrap_or(0),
            "--domain" => options.domain = Some(value()),
            "--page-size" => options.page_size = value().parse().unwrap_or(0),
            "--log" => options.log = Some(value().into()),
            "--stderr-lines" => stderr_lines = value().parse().unwrap_or(0),
            "--http" => http = Some(value()),
            "--simulation" => options.simulation = true,
            "--sse" => options.legacy_sse = true,
            "--public-host" => options.public_host = Some(value()),
            "--token" => options.auth = McpTestAuth::Token(value()),
            "--oauth" => oauth = Some(oauth.unwrap_or_default()),
            "--no-metadata-document" => oauth.get_or_insert_with(OAuthOptions::default).metadata_document = false,
            "--no-registration" => oauth.get_or_insert_with(OAuthOptions::default).registration = false,
            "--token-ttl" => oauth.get_or_insert_with(OAuthOptions::default).token_ttl_secs = value().parse().unwrap_or(3600),
            _ => {}
        }
    }
    if let Some(oauth) = oauth {
        options.auth = McpTestAuth::OAuth(oauth);
    }
    let mut err = std::io::stderr().lock();
    for n in 0..stderr_lines {
        writeln!(err, "noise line {n} from the test server, long enough to fill a pipe buffer quickly")?;
    }
    drop(err);
    match http {
        Some(addr) => {
            let server = her_mock::mcp::serve_http(&addr, options).await?;
            let mut out = std::io::stdout().lock();
            writeln!(out, "{}", server.url())?;
            out.flush()?;
            drop(out);
            tokio::signal::ctrl_c().await?;
        }
        None => her_mock::mcp::serve_stdio(tokio::io::stdin(), tokio::io::stdout(), options).await,
    }
    Ok(())
}
