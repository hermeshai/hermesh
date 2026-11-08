//! `her install`, `her uninstall` and `her upgrade` (standalone-cli-design §8.4, §13.9).

use anyhow::Context;
use anyhow::bail;
use her_core::release;
use her_core::release::Addon;
use std::path::Path;

use crate::cmd_customize::shorten;
use crate::setup;

fn addon_of(component: &str) -> Option<Addon> {
    match component {
        "pdf" => Some(Addon::Pdf),
        "computer" => Some(Addon::Computer),
        _ => None,
    }
}

fn progress(received: u64, total: u64) {
    let mb = received >> 20;
    let step = received.saturating_sub(received % (5 << 20));
    if received == step || received == total {
        eprint!("\r  {mb} / {} MB", total >> 20);
    }
}

/// The checks that only a person on this machine can act on, before anything downloads.
fn can_run_here(addon: Addon) -> anyhow::Result<()> {
    match addon {
        Addon::Computer if !cfg!(target_os = "macos") => bail!("computer use is for macOS only"),
        // The PDF worker on Linux links against glibc; Alpine and the like have none.
        Addon::Pdf if cfg!(target_os = "linux") && !["/lib64/ld-linux-x86-64.so.2", "/lib/ld-linux-aarch64.so.1"].iter().any(|p| Path::new(p).exists()) => {
            bail!("PDF needs a system with glibc; workbooks open without it. To read a PDF here, use a tool such as pdftotext")
        }
        _ => Ok(()),
    }
}

pub async fn install(component: &str) -> anyhow::Result<u8> {
    if component == "browser" {
        return crate::cmd_info::browser_install().await;
    }
    let addon = addon_of(component).context("unknown component")?;
    can_run_here(addon)?;
    let home = setup::home()?;
    if addon.installed(&home, release::VERSION) && addon == Addon::Pdf {
        println!("{component} is installed already in {}", shorten(&home, &addon.dir(&home, release::VERSION)));
        return Ok(0);
    }
    let manifest = release::fetch_manifest(Some(release::VERSION)).await.map_err(anyhow::Error::msg)?;
    let dir = release::install_addon(&home, addon, &manifest, progress).await.map_err(anyhow::Error::msg)?;
    eprintln!();
    println!("{component} {} installed in {}", manifest.version, shorten(&home, &dir));
    if addon == Addon::Computer {
        println!("{}", her_core::lang::say(
            "It also needs Accessibility and Screen Recording for the terminal app that runs her: System Settings › Privacy & Security.",
            "还要给运行 her 的终端程序授予辅助功能和屏幕录制权限：系统设置 › 隐私与安全性。",
        ));
    }
    Ok(0)
}

pub fn uninstall(component: &str) -> anyhow::Result<u8> {
    let home = setup::home()?;
    if component == "browser" {
        let dir = home.browser_dir().join("chromium");
        if !dir.exists() {
            println!("browser was not installed");
            return Ok(0);
        }
        std::fs::remove_dir_all(&dir).with_context(|| format!("cannot remove {}", dir.display()))?;
        println!("browser removed from {}", shorten(&home, &dir));
        return Ok(0);
    }
    let addon = addon_of(component).context("unknown component")?;
    if release::uninstall_addon(&home, addon).map_err(anyhow::Error::msg)? {
        println!("{component} removed from {}", shorten(&home, home.root()));
    } else {
        println!("{component} was not installed");
    }
    Ok(0)
}

/// What the path of this program says about who keeps it up to date, if not `her upgrade`.
fn managed_by(path: &Path) -> Option<&'static str> {
    let text = path.to_string_lossy();
    if text.contains("/Cellar/") || text.contains("/homebrew/") {
        return Some("this copy comes from Homebrew: run `brew upgrade her`");
    }
    if text.contains(".app/") {
        return Some("this copy comes with Hermesh, which updates it");
    }
    if text.contains("/.cargo/bin/") || text.contains("/target/") {
        return Some("this copy was built from source: build it again");
    }
    None
}

pub async fn upgrade(check: bool, version: Option<String>, as_json: bool) -> anyhow::Result<u8> {
    let exe = std::env::current_exe().context("cannot tell where this program is")?;
    let exe = std::fs::canonicalize(&exe).unwrap_or(exe);
    let manifest = release::fetch_manifest(version.as_deref()).await.map_err(anyhow::Error::msg)?;
    let newer = release::is_newer(&manifest.version, release::VERSION);
    if check {
        if as_json {
            println!("{}", serde_json::json!({ "current": release::VERSION, "latest": manifest.version, "upgrade_available": newer }));
        } else if newer {
            println!("her {} is available (this is {}): run `her upgrade`", manifest.version, release::VERSION);
        } else {
            println!("her {} is up to date", release::VERSION);
        }
        return Ok(0);
    }
    // In tests a development build stands for an installed copy.
    if (!cfg!(debug_assertions) || std::env::var_os("HER_TEST_RELEASES").is_none())
        && let Some(how) = managed_by(&exe)
    {
        bail!("{how}");
    }
    if version.is_none() && !newer {
        println!("her {} is up to date", release::VERSION);
        return Ok(0);
    }
    if manifest.version == release::VERSION {
        println!("her {} is what is installed", release::VERSION);
        return Ok(0);
    }
    let target = release::target().context("no builds for this system")?;
    let artifact = manifest.artifacts.get(target).with_context(|| format!("release {} has no build for {target}", manifest.version))?;
    let dir = exe.parent().context("this program has no folder")?;
    let work = tempfile::Builder::new().prefix(".her-upgrade-").tempdir_in(dir).with_context(|| format!("cannot write in {}", dir.display()))?;
    let archive = work.path().join("archive.tar.gz");
    release::download(artifact, &archive, progress).await.map_err(anyhow::Error::msg)?;
    eprintln!();
    release::unpack(&archive, work.path()).map_err(anyhow::Error::msg)?;
    let new = work.path().join("her");
    if !new.is_file() {
        bail!("release {} has no her in its archive; nothing changed", manifest.version);
    }
    #[cfg(target_os = "macos")]
    release::verify_team(&new).map_err(anyhow::Error::msg)?;
    // The name changes at once; a process running the old file keeps it.
    std::fs::rename(&new, &exe).with_context(|| format!("cannot replace {}", exe.display()))?;
    println!("her upgraded from {} to {} at {}", release::VERSION, manifest.version, exe.display());
    let home = setup::home()?;
    for addon in [Addon::Pdf, Addon::Computer] {
        if !addon.installed(&home, release::VERSION) {
            continue;
        }
        match release::install_addon(&home, addon, &manifest, progress).await {
            Ok(_) => {
                eprintln!();
                println!("{} upgraded to {}", addon.name(), manifest.version);
            }
            Err(err) => eprintln!("warning: {} was not upgraded: {err}. Run `her install {}`", addon.name(), addon.name()),
        }
    }
    if crate::cmd_serve::service_runs(&home) {
        eprintln!("note: a `her serve` is running; it stays on the version it started with until it restarts");
    }
    Ok(0)
}
