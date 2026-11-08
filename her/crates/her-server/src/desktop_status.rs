//! `desktop/status`: whether computer use can work where her runs. The permissions are asked by
//! running agent-desktop as the desktop tools do, so they are those of the process macOS holds
//! responsible for her: hermeshd when Hermesh runs her (docs/her-server.md §4.4), which nothing
//! outside her could ask.

use her_desktop::Permission;
use her_protocol::api::DesktopPermission;
use her_protocol::api::DesktopStatus;
use her_rpc::RpcError;

use crate::server::Server;

fn permission(p: Permission) -> DesktopPermission {
    match p {
        Permission::Granted => DesktopPermission::Granted,
        Permission::Denied => DesktopPermission::Denied,
        Permission::Unknown => DesktopPermission::Unknown,
    }
}

impl Server {
    pub(crate) async fn desktop_status(&self) -> Result<DesktopStatus, RpcError> {
        let server = self.clone();
        let settings = tokio::task::spawn_blocking(move || server.workspace(None).map(|w| w.config.desktop.clone()))
            .await
            .map_err(|e| RpcError::internal(e.to_string()))??;
        let mut status = DesktopStatus {
            enabled: settings.on(self.0.browser_pool.has_host()),
            program: None,
            accessibility: DesktopPermission::Unsupported,
            screen_recording: DesktopPermission::Unsupported,
        };
        if !cfg!(target_os = "macos") {
            return Ok(status);
        }
        (status.accessibility, status.screen_recording) = (DesktopPermission::Unknown, DesktopPermission::Unknown);
        let Ok(program) = her_desktop::locate(settings.executable.as_deref()) else {
            return Ok(status);
        };
        if let Ok(found) = her_desktop::permissions(&program).await {
            (status.accessibility, status.screen_recording) = (permission(found.accessibility), permission(found.screen_recording));
        }
        status.program = Some(program);
        Ok(status)
    }
}

#[cfg(all(test, target_os = "macos"))]
mod tests {
    use super::*;
    use crate::server::ServerOptions;
    use her_core::HerHome;
    use std::os::unix::fs::PermissionsExt;

    #[tokio::test]
    async fn status_is_what_agent_desktop_finds() {
        let dir = tempfile::tempdir().unwrap();
        let program = dir.path().join("agent-desktop");
        std::fs::write(
            &program,
            "#!/bin/sh\necho '{\"ok\":true,\"data\":{\"accessibility\":{\"state\":\"denied\"},\"screen_recording\":{\"state\":\"granted\"}}}'\n",
        )
        .unwrap();
        std::fs::set_permissions(&program, std::fs::Permissions::from_mode(0o755)).unwrap();
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        let config = format!("[desktop]\nenabled = true\nexecutable = \"{}\"\n", program.display());
        std::fs::write(home.config_file(), config).unwrap();
        let server = Server::new(ServerOptions {
            home: home.clone(),
            cwd: dir.path().to_path_buf(),
            overrides: Vec::new(),
            mcp_servers: Vec::new(),
            doc_worker: her_docs::Launcher::Thread,
        })
        .unwrap();
        let status = server.desktop_status().await.unwrap();
        assert_eq!(
            status,
            DesktopStatus {
                enabled: true,
                program: Some(program.clone()),
                accessibility: DesktopPermission::Denied,
                screen_recording: DesktopPermission::Granted,
            }
        );

        // Off, with a program that is not there: nothing to ask.
        std::fs::write(home.config_file(), "[desktop]\nenabled = false\nexecutable = \"/nonexistent/agent-desktop\"\n").unwrap();
        let status = server.desktop_status().await.unwrap();
        assert_eq!((status.enabled, status.program, status.accessibility), (false, None, DesktopPermission::Unknown));
    }
}
