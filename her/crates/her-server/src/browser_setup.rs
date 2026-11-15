//! `browser/open_setup`: what adding the Hermesh extension by hand takes, opened on the computer
//! her runs on, for the console's guide (her/apps/console/src/yours/YourBrowser.tsx). The console
//! may be on another device, so it cannot open these itself.

use her_protocol::api::BrowserSetupPlace;
use her_rpc::RpcError;
use std::path::Path;
use std::path::PathBuf;
use std::process::Stdio;
use tokio::process::Command;

const EXTENSIONS_PAGE: &str = "chrome://extensions";

/// The Mac apps the extension's host is registered for (her-cli's `browser_dirs`): the
/// default browser is used for the extensions page when it is one of them.
const MAC_BROWSERS: &[&str] = &["Google Chrome", "Google Chrome Beta", "Google Chrome Canary", "Chromium", "Microsoft Edge", "Brave Browser"];

/// The default browser's app, as Launch Services has it.
const DEFAULT_BROWSER_JXA: &str =
    r#"ObjC.import("AppKit"); $.NSWorkspace.sharedWorkspace.URLForApplicationToOpenURL($.NSURL.URLWithString("https://example.com")).path.js"#;

pub(crate) async fn open_setup(what: BrowserSetupPlace) -> Result<(), RpcError> {
    match what {
        BrowserSetupPlace::ExtensionsPage => open_extensions_page().await,
        BrowserSetupPlace::ExtensionFolder => show_extension_folder().await,
    }
}

async fn open_extensions_page() -> Result<(), RpcError> {
    if cfg!(target_os = "macos") {
        let default = Command::new("osascript").args(["-l", "JavaScript", "-e", DEFAULT_BROWSER_JXA]).output().await.ok();
        let default = default.map(|out| String::from_utf8_lossy(&out.stdout).trim().to_owned()).unwrap_or_default();
        let app = mac_browser(&default).unwrap_or("Google Chrome");
        return run(Command::new("open").args(["-a", app, EXTENSIONS_PAGE])).await;
    }
    let program = her_browser::launch::find_executable().ok_or_else(|| RpcError::invalid_request("no Chrome-family browser was found on this computer"))?;
    // A browser that was not running stays open: it is started, not waited for.
    Command::new(&program)
        .arg(EXTENSIONS_PAGE)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .map(drop)
        .map_err(|e| RpcError::internal(format!("cannot start {}: {e}", program.display())))
}

/// The default browser's app, when the extension works in it.
fn mac_browser(app: &str) -> Option<&str> {
    let name = Path::new(app).file_stem()?.to_str()?;
    (app.ends_with(".app") && MAC_BROWSERS.contains(&name)).then_some(app)
}

/// Where hermeshd keeps the extension (internal/herhost/extension.go, ExtensionFolder).
fn extension_folder(home: &Path) -> PathBuf {
    if cfg!(target_os = "macos") {
        home.join("Library/Application Support/Hermesh/Chrome Extension")
    } else {
        home.join(".local/share/hermesh/chrome-extension")
    }
}

async fn show_extension_folder() -> Result<(), RpcError> {
    let home = std::env::var_os("HOME").map(PathBuf::from).ok_or_else(|| RpcError::internal("HOME is not set"))?;
    let folder = extension_folder(&home);
    if !folder.join("manifest.json").is_file() {
        return Err(RpcError::invalid_request(format!("the Hermesh extension is not in {} yet", folder.display())));
    }
    if cfg!(target_os = "macos") {
        // Selected in its Finder window, ready to drag.
        run(Command::new("open").arg("-R").arg(&folder)).await
    } else {
        run(Command::new("xdg-open").arg(&folder)).await
    }
}

async fn run(command: &mut Command) -> Result<(), RpcError> {
    let program = command.as_std().get_program().to_string_lossy().into_owned();
    let out = command.stdin(Stdio::null()).output().await.map_err(|e| RpcError::internal(format!("cannot run {program}: {e}")))?;
    if out.status.success() {
        return Ok(());
    }
    let said = String::from_utf8_lossy(&out.stderr).trim().to_owned();
    Err(RpcError::internal(format!("{program} failed ({}): {said}", out.status)))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_default_browser_is_used_when_the_extension_works_in_it() {
        assert_eq!(mac_browser("/Applications/Google Chrome.app"), Some("/Applications/Google Chrome.app"));
        assert_eq!(mac_browser("/Users/me/Applications/Brave Browser.app"), Some("/Users/me/Applications/Brave Browser.app"));
        assert_eq!(mac_browser("/Applications/Safari.app"), None);
        assert_eq!(mac_browser("/System/Volumes/Preboot/Cryptexes/App/System/Applications/Safari.app"), None);
        assert_eq!(mac_browser(""), None);
    }
}
