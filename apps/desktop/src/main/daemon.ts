import { app, shell } from "electron";
import { execFile } from "node:child_process";
import type { LoginItem } from "../shared/bridge";
import { testDataDir } from "./paths";

// The installed app runs hermeshd as a LaunchAgent (its plist in
// Contents/Library/LaunchAgents), so it keeps running when the app quits,
// and opens itself at login when the owner wants it to (Open Hermesh at login).
// Development and test runs do neither: they use a daemon of their own,
// and must not register the Electron binary to start at login. Nor does a
// packaged app pointed at another daemon (HERMESH_DATA_DIR, as tests do).
export const installed = app.isPackaged && process.platform === "darwin" && !process.env.HERMESH_DATA_DIR && !testDataDir;
const agent = { type: "agentService", serviceName: "com.hermesh.app.daemon.plist" } as const;
let devLoginItem = false;

export function registerDaemon() {
  if (installed) app.setLoginItemSettings({ ...agent, openAtLogin: true });
}

/** Restarts the LaunchAgent: launchd stops it with SIGTERM and starts it again from the bundle. */
export function restartDaemon(why: string) {
  if (!installed) return;
  console.warn(`restarting hermeshd: ${why}`);
  execFile("launchctl", ["kickstart", "-k", `gui/${process.getuid?.()}/com.hermesh.app.daemon`], (err) => err && console.warn("restarting hermeshd:", err));
}

/** The owner turned hermeshd off in System Settings › Login Items (or never allowed it): it does not run until they do. */
export function daemonNeedsApproval(): boolean {
  return installed && app.getLoginItemSettings(agent).status === "requires-approval";
}

export function loginItem(): LoginItem {
  if (!installed) return { on: devLoginItem, needsApproval: false };
  const { status } = app.getLoginItemSettings({ type: "mainAppService" });
  return { on: status === "enabled" || status === "requires-approval", needsApproval: status === "requires-approval" };
}

export function setLoginItem(on: boolean): LoginItem {
  if (installed) app.setLoginItemSettings({ type: "mainAppService", openAtLogin: on });
  else devLoginItem = on;
  return loginItem();
}

/** Opened at login: the app starts in the menu bar only, without its window. */
export function openedAtLogin(): boolean {
  return installed && app.getLoginItemSettings().wasOpenedAtLogin;
}

export function openLoginItems() {
  if (process.platform === "darwin") shell.openExternal("x-apple.systempreferences:com.apple.LoginItems-Settings.extension");
}
