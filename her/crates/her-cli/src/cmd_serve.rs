//! `her serve` and `her acp`: the protocol service, on stdio or a Unix socket.

use anyhow::Context as _;
use her_server::NativeProtocol;
use her_server::Protocol;
use her_server::Server;
use her_server::ServerOptions;
use her_server::UnixService;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use crate::setup;

pub(crate) fn start(overrides: &[String]) -> anyhow::Result<(Server, Option<tracing_appender::non_blocking::WorkerGuard>)> {
    let home = setup::home()?;
    let guard = setup::init_logging(&home);
    match her_server::limits::raise_open_file_limit() {
        Ok((before, after)) if after > before => tracing::debug!(before, after, "raised the open file limit"),
        Ok(_) => {}
        Err(err) => tracing::warn!(%err, "could not raise the open file limit"),
    }
    // Environment variables beat the config files; `-c` beats them.
    let mut warnings = Vec::new();
    let overrides: Vec<String> = setup::env_overrides(&mut warnings)?
        .into_iter()
        .chain(overrides.iter().cloned())
        .collect();
    let mcp = setup::mcp_config_files();
    for warning in warnings.iter().chain(&mcp.warnings) {
        tracing::warn!("{warning}");
    }
    let server = Server::new(ServerOptions {
        home,
        cwd: std::env::current_dir()?,
        overrides,
        mcp_servers: mcp.servers.clone(),
        doc_worker: her_docs::Launcher::this_program(),
    })?;
    spawn_signal_handler(server.clone());
    Ok((server, guard))
}

/// SIGINT and SIGTERM stop the service gracefully: sessions are closed and their logs saved.
fn spawn_signal_handler(server: Server) {
    tokio::spawn(async move {
        let mut term = match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            Ok(term) => term,
            Err(_) => return,
        };
        tokio::select! {
            _ = tokio::signal::ctrl_c() => {}
            _ = term.recv() => {}
        }
        server.request_shutdown();
    });
}

pub async fn serve(overrides: &[String], listen: Option<String>, idle_exit: Option<u64>) -> anyhow::Result<u8> {
    let (server, _guard) = start(overrides)?;
    // The resident service keeps model lists and the public catalog fresh in the background.
    server.start_model_upkeep();
    server.start_routines();
    match listen {
        None => her_server::serve_stdio(server.clone(), NativeProtocol::new(server)).await,
        Some(url) => {
            let path = match url.strip_prefix("unix://") {
                Some("") => her_server::default_socket_path(&server),
                Some(path) => std::path::absolute(PathBuf::from(path))?,
                None => anyhow::bail!("unsupported --listen URL '{url}': expected unix:// or unix://PATH"),
            };
            let service = UnixService {
                path,
                idle_exit: idle_exit.map(Duration::from_secs),
            };
            let protocol_server = server.clone();
            her_server::serve_unix(server, service, move || -> Arc<dyn Protocol> {
                NativeProtocol::new(protocol_server.clone())
            })
            .await
            .context("the service stopped")?;
        }
    }
    Ok(0)
}

/// `her protocol schema`: the protocol's JSON Schema on stdout.
pub fn schema() -> anyhow::Result<u8> {
    use std::io::Write as _;
    let mut out = std::io::stdout().lock();
    serde_json::to_writer_pretty(&mut out, &her_protocol::schema::document())?;
    writeln!(out)?;
    Ok(0)
}

pub async fn acp(overrides: &[String]) -> anyhow::Result<u8> {
    let (server, _guard) = start(overrides)?;
    her_server::serve_stdio(server.clone(), her_acp::AcpProtocol::new(server)).await;
    Ok(0)
}

/// Whether a service answers on the default socket.
pub(crate) fn service_runs(home: &her_core::paths::HerHome) -> bool {
    std::os::unix::net::UnixStream::connect(home.root().join("run").join("her.sock")).is_ok()
}
