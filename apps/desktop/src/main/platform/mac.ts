import { app, shell } from "electron";
import { execFile } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import type { TerminalCommand } from "../../shared/bridge";
import { chooseMacTerminal, parseRunningBundleIds } from "../../shared/terminal";
import { settleInApplications } from "../install";
import { testDataDir } from "../paths";
import type { Platform, WindowKind } from "./types";

// The installed app runs hermeshd as a LaunchAgent (its plist in
// Contents/Library/LaunchAgents, with RunAtLoad=false and KeepAlive only on
// non-zero exit), so the app controls when it starts and stops while keeping
// crash recovery, TCC attribution, and seamless updates. Development and test
// runs use a daemon of their own, and must not register the Electron binary to
// start at login. Nor does a packaged app pointed at another daemon
// (HERMESH_DATA_DIR, as tests do).
const installed = app.isPackaged && process.platform === "darwin" && !process.env.HERMESH_DATA_DIR && !testDataDir;
const agent = { type: "agentService", serviceName: "com.hermesh.app.daemon.plist" } as const;
const daemonJob = () => `gui/${process.getuid?.()}/com.hermesh.app.daemon`;
let devLoginItem = false;

function launchctl(args: string[]): Promise<{ ok: boolean; stdout: string }> {
  return new Promise((resolve) => {
    execFile("launchctl", args, (err, stdout) => resolve({ ok: !err, stdout: String(stdout ?? "") }));
  });
}

async function kickstartDaemon() {
  const job = daemonJob();
  const deadline = Date.now() + 3_000;
  for (;;) {
    const r = await launchctl(["kickstart", job]);
    if (r.ok || mac.daemonNeedsApproval() || Date.now() >= deadline) return;
    await new Promise((res) => setTimeout(res, 200));
  }
}

// The CLIs that come with the Claude and ChatGPT (Codex) desktop apps, after
// the person's own PATH, as hermeshd finds them: for those who have only the app.
const appCLIs = String.raw`path+=(~/Library/Application\ Support/Claude/claude-code/*/*/claude.app/Contents/MacOS/claude(Nn[-1]:h) {,~}/Applications/{ChatGPT.app/Contents/Resources/codex-cli/bin,Codex.app/Contents/Resources}/codex(N:h))`;

export const mac: Platform = {
  token: "mac",
  installed,
  material: true,

  /** The installed app runs from the Applications folder (install.ts). */
  settle() {
    return installed ? settleInApplications() : Promise.resolve(true);
  },

  installerEvent() {
    return false;
  },

  registerLinks() {
    if (app.isPackaged) app.setAsDefaultProtocolClient("hermesh");
  },

  registerDaemon() {
    if (!installed) return;
    app.setLoginItemSettings({ ...agent, openAtLogin: true });
    void kickstartDaemon();
  },

  async startDaemon() {
    if (!installed) return;
    app.setLoginItemSettings({ ...agent, openAtLogin: true });
    await kickstartDaemon();
  },

  async stopDaemon() {
    if (!installed) return;
    const job = daemonJob();
    await launchctl(["kill", "SIGTERM", job]);
    const deadline = Date.now() + 5_000;
    while (Date.now() < deadline) {
      const r = await launchctl(["print", job]);
      if (!r.ok || !/^\s*pid = \d+/m.test(r.stdout)) return;
      await new Promise((res) => setTimeout(res, 150));
    }
    // Fallback if hermeshd did not exit after SIGTERM.
    await launchctl(["bootout", job]);
  },

  /**
   * Restarts the LaunchAgent from the bundle, plist included. launchd keeps a job as it
   * loaded it: a kickstart would run the new binary under the old plist. Booting the job
   * out (SIGTERM) leaves it registered and allowed, and registering again loads the plist;
   * kickstart then starts it (RunAtLoad is false).
   */
  restartDaemon(why) {
    if (!installed) return;
    console.warn(`restarting hermeshd: ${why}`);
    const job = daemonJob();
    const deadline = Date.now() + 15_000; // launchd kills it 5 s after the SIGTERM
    // SMAppService-submitted jobs reject `launchctl bootout`; unregistering
    // through SMAppService first cleanly unloads the old job and plist so
    // re-registering picks up the new bundle's binary and HERMESH_APP_VERSION.
    try {
      app.setLoginItemSettings({ ...agent, openAtLogin: false });
    } catch (e) {
      console.warn("unregistering hermeshd:", e);
    }
    const whenGone = () => execFile("launchctl", ["print", job], (notLoaded) => {
      if (notLoaded) {
        app.setLoginItemSettings({ ...agent, openAtLogin: true });
        void kickstartDaemon();
      } else if (Date.now() < deadline) setTimeout(whenGone, 200);
      else {
        app.setLoginItemSettings({ ...agent, openAtLogin: true });
        execFile("launchctl", ["kickstart", "-k", job], (err) => err && console.warn("restarting hermeshd:", err));
      }
    });
    execFile("launchctl", ["bootout", job], () => whenGone());
  },

  /** The owner turned hermeshd off in System Settings › Login Items (or never allowed it): it does not run until they do. */
  daemonNeedsApproval() {
    return installed && app.getLoginItemSettings(agent).status === "requires-approval";
  },

  loginItem() {
    if (!installed) return { on: devLoginItem, needsApproval: false };
    const { status } = app.getLoginItemSettings({ type: "mainAppService" });
    return { on: status === "enabled" || status === "requires-approval", needsApproval: status === "requires-approval" };
  },

  setLoginItem(on) {
    if (installed) app.setLoginItemSettings({ type: "mainAppService", openAtLogin: on });
    else devLoginItem = on;
    return mac.loginItem();
  },

  /** Opened at login: the app starts in the menu bar only, without its window. */
  openedAtLogin() {
    return installed && app.getLoginItemSettings().wasOpenedAtLogin;
  },

  openLoginItems() {
    shell.openExternal("x-apple.systempreferences:com.apple.LoginItems-Settings.extension");
  },

  // The signer (Hermesh Signer.app) holds the root key and shows what is
  // signed; it speaks the app's language (-AppleLanguages, as macOS reads it).
  signer(language) {
    const bin = !app.isPackaged ? process.env.HERMESH_SIGNER || undefined : path.join(process.resourcesPath, "..", "Helpers", "Hermesh Signer.app", "Contents", "MacOS", "HermeshSigner");
    const names: Record<string, string> = { en: "en", "zh-CN": "zh-Hans" };
    return bin ? { bin, args: ["-AppleLanguages", `(${names[language] ?? "en"})`] } : undefined;
  },

  // The signer is the app's child (signer.ts).
  async readySigner() {},

  // The signer comes and goes with the app.
  async signerUpdate() {
    return undefined;
  },
  async updateSigner() {},

  // Elevation is never silent on a Mac: Touch ID or the password, each time.
  async elevatesSilently() {
    return false;
  },
  openElevationSettings() {},

  runInTerminal(c: TerminalCommand, command: string) {
    if (c === "antigravityLogin" && app.isPackaged) {
      const bin = path.join(process.resourcesPath, "..", "Helpers", "hermesh");
      command = `'${bin.replaceAll("'", "'\\''")}' agent login antigravity`;
    }
    // A .command file opens in Terminal and runs there, in the person's login shell.
    const dir = path.join(os.homedir(), "Library", "Caches", "Hermesh");
    fs.mkdirSync(dir, { recursive: true });
    const file = path.join(dir, `${c}.command`);
    fs.writeFileSync(file, `#!/bin/zsh -l\n${appCLIs}\n${command}\nexec "\${SHELL:-/bin/zsh}" -l\n`, { mode: 0o700 });
    shell.openPath(file);
  },

  openFolder(dir, to) {
    if (!fs.statSync(dir, { throwIfNoEntry: false })?.isDirectory()) return;
    if (to === "files") {
      execFile("open", ["-a", "Finder", dir]);
      return;
    }
    execFile("/usr/bin/lsappinfo", ["list"], { timeout: 1500 }, (_err, stdout) => {
      const running = parseRunningBundleIds(typeof stdout === "string" ? stdout : "");
      const terminalApp = chooseMacTerminal(os.homedir(), running, (p) => fs.existsSync(p));
      execFile("open", ["-a", terminalApp, dir]);
    });
  },

  openProcessMonitor() {
    void shell.openPath("/System/Applications/Utilities/Activity Monitor.app");
  },

  async computerName() {
    try {
      const { stdout } = await promisify(execFile)("/usr/sbin/scutil", ["--get", "ComputerName"]);
      if (stdout.trim()) return stdout.trim();
    } catch {
      // Fall back to the host name.
    }
    return os.hostname().replace(/\.local$/, "");
  },

  frame(kind: WindowKind) {
    switch (kind) {
      case "panel":
        return { type: "panel", vibrancy: "popover", visualEffectState: "active", backgroundColor: "#00000000" };
      case "auxiliary":
        return { titleBarStyle: "hiddenInset", trafficLightPosition: { x: 12, y: 15 } };
      default:
        // The lights on the 44 px top band; the sidebar's material showing through (the content is opaque).
        return {
          titleBarStyle: "hiddenInset",
          trafficLightPosition: kind === "onboarding" ? { x: 24, y: 24 } : { x: 12, y: 15 },
          vibrancy: "sidebar",
          visualEffectState: "followWindow",
          backgroundColor: "#00000000",
        };
    }
  },

  placeButtons(win, kind) {
    // The traffic lights sit on the middle of the main window's 44 px top band.
    win.setWindowButtonPosition(kind === "main" ? { x: 12, y: 15 } : { x: 24, y: 24 });
  },

  grow(win, bounds) {
    win.setBounds(bounds, true);
  },
};
