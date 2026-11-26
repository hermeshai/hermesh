import { contextBridge, ipcRenderer, webFrame, webUtils } from "electron";
import type { DaemonInfo } from "../shared/daemon";
import type { Language } from "../shared/i18n";
import type { Appearance, Bridge, Command, ComputerPermission, MenuItem, MenuState, Preferences, TerminalCommand, TrayAction, TrayView } from "../shared/bridge";
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
  material: arg("material") === "1",
  userName: arg("user"),
  version: arg("version"),
  language: arg("lang") as Language,
  appearance: (arg("appearance") || "system") as Appearance,
  macos: process.platform === "darwin" ? Number.parseInt(process.getSystemVersion(), 10) : 0,
  openComputerPermission: (kind: ComputerPermission) => ipcRenderer.send("open-computer-permission", kind),
  openSettings: (tab?: string) => ipcRenderer.send("open-settings", tab),
  onSettingsTab: (cb: (tab: string) => void) => listen("settings-tab", cb),
  onWindowFocus: (cb: (focused: boolean) => void) => listen("window-focus", cb),
  showInMain: (filter: { grant: string } | { machine: string }) => ipcRenderer.send("show-in-main", filter),
  showUpgrade: () => ipcRenderer.send("show-upgrade"),
  showAppMenu: (x: number, y: number) => ipcRenderer.send("app-menu", x, y),
  popupMenu: (items: MenuItem[], at?: { x: number; y: number }) => ipcRenderer.invoke("popup-menu", items, at),
  releaseMemory: () => webFrame.clearCache(),
  onCommand: (cb: (c: Command) => void) => listen("command", cb),
  setMenuState: (s: MenuState) => ipcRenderer.send("menu-state", s),
  setVisibleHer: (machineId, botId) => ipcRenderer.send("visible-her", machineId, botId),
  waitForPairings: (on: boolean) => ipcRenderer.send("wait-pairings", on === true),
  openExternal: (url: string) => ipcRenderer.send("open-external", url),
  pickDirectory: () => ipcRenderer.invoke("pick-directory"),
  pathForFile: (file: File) => {
    try {
      return webUtils.getPathForFile(file);
    } catch {
      return "";
    }
  },
  openSessionPage: (file, rect, slot) => ask("session-page-open", file, rect, slot),
  placeSessionPage: (rect, slot) => ipcRenderer.send("session-page-place", rect, slot),
  closeSessionPage: (slot) => ipcRenderer.send("session-page-close", slot),
  onSessionPage: (cb) => listen("session-page", cb),
  openSessionPageInBrowser: (file) => ask("session-page-browser", file),
  quickLookSessionFile: (file, name) => ask("session-file-quicklook", file, name),
  saveSessionFile: (file, name) => ask("session-file-save", file, name),
  revealSessionFile: (file) => ask("session-file-reveal", file),
  openHerConsole: (machineId: string, page?: string) => ask("her-console", machineId, page),
  saveHerFile: (machineId: string, botId: string, path: string, sha?: string) => ask("her-save-file", machineId, botId, path, sha),
  readHerFile: (machineId: string, botId: string, path: string, sha?: string) => ask("her-read-file", machineId, botId, path, sha),
  herFilesKept: (machineId: string, files: [string, string][]) => ask("her-files-kept", machineId, files),
  signInHerChatGpt: (machineId: string) => ask("her-chatgpt", machineId),
  reopenHerChatGpt: (machineId: string) => ipcRenderer.send("her-chatgpt-reopen", machineId),
  cancelHerChatGpt: (machineId: string) => ipcRenderer.send("her-chatgpt-cancel", machineId),
  signInHerConnector: (machineId: string, name: string) => ask("her-connector", machineId, name),
  reopenHerConnector: (machineId: string, name: string) => ipcRenderer.send("her-connector-reopen", machineId, name),
  cancelHerConnector: (machineId: string, name: string) => ipcRenderer.send("her-connector-cancel", machineId, name),
  runInTerminal: (c: TerminalCommand) => ipcRenderer.send("run-in-terminal", c),
  setWindowMode: (m: "onboarding" | "main") => ipcRenderer.send("window-mode", m),
  signIn: (center: string, owner?: string) => ask("sign-in", center, owner ?? ""),
  completeResume: () => ask("complete-resume"),
  signInPending: () => ask("sign-in-pending"),
  cancelSignIn: () => ask("cancel-sign-in"),
  completeSignIn: (o: { localMachine: boolean }) => ask("complete-sign-in", o),
  connectURL: (center: string) => ask("connect-url", center),
  computerName: () => ask("computer-name"),
  readySigner: () => ask("ready-signer"),
  signerUpdate: () => ask("signer-update"),
  updateSigner: () => ask("update-signer"),
  elevatesSilently: () => ask("elevates-silently"),
  openElevationSettings: () => ipcRenderer.send("open-elevation-settings"),
  onLink: (cb: (url: string) => void) => {
    // Links wait in the main process until taken: the page may load after one arrived.
    const take = () => ask<string[]>("take-links").then((urls) => urls.forEach(cb));
    take();
    return listen("link", take);
  },
  getPreferences: () => ipcRenderer.invoke("preferences"),
  setPreferences: (p: Partial<Preferences>) => ipcRenderer.invoke("set-preferences", p),
  onPreferences: (cb: (p: Preferences) => void) => listen("preferences", cb),
  relaunch: () => ipcRenderer.send("relaunch"),
  loginItem: () => ipcRenderer.invoke("login-item"),
  setLoginItem: (on: boolean) => ipcRenderer.invoke("set-login-item", on),
  daemonNeedsApproval: () => ipcRenderer.invoke("daemon-needs-approval"),
  daemonState: () => ipcRenderer.invoke("daemon-state"),
  onDaemonState: (cb: (d: DaemonInfo) => void) => listen("daemon-state", cb),
  startDaemon: () => ipcRenderer.invoke("start-daemon"),
  stopDaemon: () => ipcRenderer.invoke("stop-daemon"),
  openLoginItems: () => ipcRenderer.send("open-login-items"),
  openActivityMonitor: () => ipcRenderer.send("open-activity-monitor"),
  openFolder: (path: string, to: "files" | "terminal") => ipcRenderer.send("open-folder", path, to),
  openSessionFolder: (sessionId: string, to: "files" | "terminal") => ipcRenderer.invoke("open-session-folder", sessionId, to),
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
