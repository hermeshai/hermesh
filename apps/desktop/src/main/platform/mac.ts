import { app, shell } from "electron";
import { execFile } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import type { TerminalCommand } from "../../shared/bridge";
import { settleInApplications } from "../install";
import { testDataDir } from "../paths";
import type { Platform, WindowKind } from "./types";

// The installed app runs hermeshd as a LaunchAgent (its plist in
// Contents/Library/LaunchAgents), so it keeps running when the app quits,
// and opens itself at login when the owner wants it to (Open Hermesh at login).
// Development and test runs do neither: they use a daemon of their own,
// and must not register the Electron binary to start at login. Nor does a
// packaged app pointed at another daemon (HERMESH_DATA_DIR, as tests do).
const installed = app.isPackaged && process.platform === "darwin" && !process.env.HERMESH_DATA_DIR && !testDataDir;
const agent = { type: "agentService", serviceName: "com.hermesh.app.daemon.plist" } as const;
let devLoginItem = false;

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
    if (installed) app.setLoginItemSettings({ ...agent, openAtLogin: true });
  },

  /** Restarts the LaunchAgent: launchd stops it with SIGTERM and starts it again from the bundle. */
  restartDaemon(why) {
    if (!installed) return;
    console.warn(`restarting hermeshd: ${why}`);
    execFile("launchctl", ["kickstart", "-k", `gui/${process.getuid?.()}/com.hermesh.app.daemon`], (err) => err && console.warn("restarting hermeshd:", err));
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
    // A .command file opens in Terminal and runs there, in the person's login shell.
    const dir = path.join(os.homedir(), "Library", "Caches", "Hermesh");
    fs.mkdirSync(dir, { recursive: true });
    const file = path.join(dir, `${c}.command`);
    fs.writeFileSync(file, `#!/bin/zsh -l\n${appCLIs}\n${command}\n`, { mode: 0o700 });
    shell.openPath(file);
  },

  openFolder(dir, to) {
    if (fs.statSync(dir, { throwIfNoEntry: false })?.isDirectory()) execFile("open", ["-a", to === "files" ? "Finder" : "Terminal", dir]);
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
