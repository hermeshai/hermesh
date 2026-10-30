import { contextBridge, ipcRenderer } from "electron";
import type { Language } from "../shared/i18n";
import type { Bridge, Command, MenuItem, MenuState, Preferences, TerminalCommand, TrayAction, TrayView } from "../shared/bridge";
import type { UpdateInfo } from "../shared/update";

// The only things the page gets from outside its sandbox. Data comes from
// hermeshd through app://hermesh/api (see main/gateway.ts).
const arg = (name: string) => process.argv.find((a) => a.startsWith(`--hermesh-${name}=`))?.split("=").slice(1).join("=") ?? "";

const listen = <T>(channel: string, cb: (v: T) => void) => {
  const h = (_: unknown, v: T) => cb(v);
  ipcRenderer.on(channel, h);
  return () => {
    ipcRenderer.removeListener(channel, h);
  };
};

// A main-process call that answers { value } or { error } (a message for the person).
async function ask<T>(channel: string, ...args: unknown[]): Promise<T> {
  const r = await ipcRenderer.invoke(channel, ...args);
  if (r?.error !== undefined) throw new Error(r.error);
  return r?.value as T;
}

const bridge: Bridge = {
  platform: process.platform,
  userName: arg("user"),
  version: arg("version"),
  language: arg("lang") as Language,
  openSettings: (tab?: string) => ipcRenderer.send("open-settings", tab),
  onSettingsTab: (cb: (tab: string) => void) => listen("settings-tab", cb),
  onWindowFocus: (cb: (focused: boolean) => void) => listen("window-focus", cb),
  showInMain: (filter: { grant: string } | { machine: string }) => ipcRenderer.send("show-in-main", filter),
  showUpgrade: () => ipcRenderer.send("show-upgrade"),
  showAppMenu: (x: number, y: number) => ipcRenderer.send("app-menu", x, y),
  popupMenu: (items: MenuItem[]) => ipcRenderer.invoke("popup-menu", items),
  onCommand: (cb: (c: Command) => void) => listen("command", cb),
  setMenuState: (s: MenuState) => ipcRenderer.send("menu-state", s),
  waitForPairings: (on: boolean) => ipcRenderer.send("wait-pairings", on === true),
  openExternal: (url: string) => ipcRenderer.send("open-external", url),
  pickDirectory: () => ipcRenderer.invoke("pick-directory"),
  runInTerminal: (c: TerminalCommand) => ipcRenderer.send("run-in-terminal", c),
  setWindowMode: (m: "onboarding" | "main") => ipcRenderer.send("window-mode", m),
  signIn: (center: string) => ask("sign-in", center),
  signInPending: () => ask("sign-in-pending"),
  cancelSignIn: () => ask("cancel-sign-in"),
  completeSignIn: (o: { localMachine: boolean }) => ask("complete-sign-in", o),
  connectURL: (center: string) => ask("connect-url", center),
  computerName: () => ask("computer-name"),
  readySigner: () => ask("ready-signer"),
  onLink: (cb: (url: string) => void) => {
    // Links wait in the main process until taken: the page may load after one arrived.
    const take = () => ask<string[]>("take-links").then((urls) => urls.forEach(cb));
    take();
    return listen("link", take);
  },
  getPreferences: () => ipcRenderer.invoke("preferences"),
  setPreferences: (p: Partial<Preferences>) => ipcRenderer.invoke("set-preferences", p),
  relaunch: () => ipcRenderer.send("relaunch"),
  loginItem: () => ipcRenderer.invoke("login-item"),
  setLoginItem: (on: boolean) => ipcRenderer.invoke("set-login-item", on),
  daemonNeedsApproval: () => ipcRenderer.invoke("daemon-needs-approval"),
  openLoginItems: () => ipcRenderer.send("open-login-items"),
  openActivityMonitor: () => ipcRenderer.send("open-activity-monitor"),
  openFolder: (path: string, to: "files" | "terminal") => ipcRenderer.send("open-folder", path, to),
  revealKeptWorktree: (path: string) => ipcRenderer.send("reveal-kept-worktree", path),
  openUpdates: (manual = false) => ipcRenderer.send("open-updates", manual),
  updateWindowHeight: (height: number) => ipcRenderer.send("update-window-height", height),
  openWhatsNew: () => ipcRenderer.send("open-whats-new"),
  snoozeUpdate: () => ipcRenderer.invoke("snooze-update"),
  returnToApproval: () => ipcRenderer.send("return-to-approval"),
  getUpdate: () => ipcRenderer.invoke("update"),
  onUpdate: (cb: (u: UpdateInfo) => void) => listen("update", cb),
  checkForUpdates: () => ipcRenderer.send("check-for-updates"),
  downloadUpdate: () => ipcRenderer.send("download-update"),
  installUpdate: () => ipcRenderer.send("install-update"),
  setAutoDownload: (on: boolean) => ipcRenderer.invoke("set-auto-download", on),
  trayView: () => ipcRenderer.invoke("tray-view"),
  onTrayView: (cb: (v: TrayView) => void) => listen("tray-view", cb),
  trayAction: (a: TrayAction) => ipcRenderer.send("tray-action", a),
  traySize: (height: number) => ipcRenderer.send("tray-size", height),
};

window.addEventListener("online", () => ipcRenderer.send("updates-online"));

contextBridge.exposeInMainWorld("hermesh", bridge);
