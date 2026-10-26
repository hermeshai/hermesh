import { app, BrowserWindow, dialog, ipcMain, Menu, shell, type IpcMainEvent, type IpcMainInvokeEvent, type WebContents } from "electron";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import type { Command, MenuItem, MenuState, Preferences, TerminalCommand } from "../shared/bridge";
import { daemonNeedsApproval, loginItem, openLoginItems, setLoginItem } from "./daemon";
import { preferences, setPreferences } from "./notify";
import { cancelSignIn, completeSignIn, computerName, connectURL, errorMessage, signIn, signInPending, takeLinks } from "./signin";
import { catchUp, check, download, install, openUpdates, returnToApproval, setAutoDownload, snooze, updateInfo } from "./updater";
import { mainWindow, ours, setMode, showMain, showSettings, showWhatsNew, sizeUpdates } from "./windows";

// The page's requests to the main process (see shared/bridge.ts). Each
// handler checks the request comes from one of our own pages.
function trusted(e: IpcMainEvent | IpcMainInvokeEvent): boolean {
  return !!e.senderFrame && ours(e.senderFrame.url);
}

const noMenuState: MenuState = { main: false, session: null, sidebar: true, undo: false };
let menuState = noMenuState;
export const currentMenuState = () => menuState;
export const menuStateListeners = new Set<() => void>();
const reporting = new WeakSet<WebContents>();
let pending: Command | undefined;
// The page waiting for pairing requests, in whichever window (waitForPairings).
let waiting: WebContents | undefined;
const watched = new WeakSet<WebContents>();

/**
 * Hands a command to the main window's page (app menu, menu bar,
 * notifications), opening the window first if needed; a page still loading
 * gets it once it reports its menu state. A pairing request's goes to the
 * page waiting for requests, if one is (the first run's included).
 */
export function command(c: Command) {
  if (c.startsWith("pairing:") && waiting) {
    const win = BrowserWindow.fromWebContents(waiting);
    win?.show();
    win?.focus();
    waiting.send("command", c);
    return;
  }
  const win = showMain();
  if (menuState.main) win.webContents.send("command", c);
  else pending = c;
}

// Commands run in Terminal, by key: the page never passes command text.
const terminalCommands: Record<TerminalCommand, string> = {
  codexLogin: "codex login",
  claudeLogin: "claude",
};

// The CLIs that come with the Claude and ChatGPT (Codex) desktop apps, after
// the person's own PATH, as hermeshd finds them: for those who have only the app.
const appCLIs = String.raw`path+=(~/Library/Application\ Support/Claude/claude-code/*/*/claude.app/Contents/MacOS/claude(Nn[-1]:h) {,~}/Applications/{ChatGPT.app/Contents/Resources/codex-cli/bin,Codex.app/Contents/Resources}/codex(N:h))`;

export function registerIPC() {
  ipcMain.on("open-settings", (e, tab?: string) => trusted(e) && showSettings(typeof tab === "string" ? tab : undefined));
  // From the settings window: the main window's search, narrowed to a grant or a machine.
  ipcMain.on("show-in-main", (e, f: { grant?: unknown; machine?: unknown }) => {
    if (!trusted(e) || typeof f !== "object" || f === null) return;
    const c: Command | undefined =
      typeof f.grant === "string" ? `searchGrant:${f.grant}` : typeof f.machine === "string" ? `searchMachine:${f.machine}` : undefined;
    if (!c) return;
    command(c);
  });
  ipcMain.on("show-upgrade", (e) => trusted(e) && command("upgrade"));
  ipcMain.on("relaunch", (e) => {
    if (!trusted(e)) return;
    app.relaunch();
    app.quit();
  });
  ipcMain.on("window-mode", (e, m) => trusted(e) && (m === "onboarding" || m === "main") && setMode(m));
  ipcMain.on("open-external", (e, url: string) => {
    if (trusted(e) && typeof url === "string" && url.startsWith("https://")) shell.openExternal(url);
  });
  ipcMain.on("menu-state", (e, s: MenuState) => {
    if (!trusted(e) || BrowserWindow.fromWebContents(e.sender) !== mainWindow()) return;
    // A closed window's page shows nothing any more.
    if (!reporting.has(e.sender)) {
      reporting.add(e.sender);
      e.sender.once("destroyed", () => {
        menuState = noMenuState;
        menuStateListeners.forEach((f) => f());
      });
    }
    menuState = s;
    if (s.main && pending) {
      e.sender.send("command", pending);
      pending = undefined;
    }
    menuStateListeners.forEach((f) => f());
  });
  ipcMain.on("wait-pairings", (e, on: boolean) => {
    if (!trusted(e)) return;
    if (!on) {
      if (waiting === e.sender) waiting = undefined;
      return;
    }
    // A page closed or loaded again waits no more.
    if (!watched.has(e.sender)) {
      watched.add(e.sender);
      const gone = () => waiting === e.sender && (waiting = undefined);
      e.sender.on("destroyed", gone);
      e.sender.on("did-start-loading", gone);
    }
    waiting = e.sender;
    // One clicked before the page showed (the first run, still setting up).
    if (pending?.startsWith("pairing:")) {
      e.sender.send("command", pending);
      pending = undefined;
    }
  });
  ipcMain.handle("pick-directory", async (e) => {
    if (!trusted(e)) return null;
    const win = BrowserWindow.fromWebContents(e.sender);
    const r = await (win ? dialog.showOpenDialog(win, { properties: ["openDirectory", "createDirectory"] }) : dialog.showOpenDialog({ properties: ["openDirectory"] }));
    return r.canceled ? null : (r.filePaths[0] ?? null);
  });
  ipcMain.handle("popup-menu", (e, items: MenuItem[]) => {
    if (!trusted(e) || !Array.isArray(items)) return null;
    return new Promise<string | null>((resolve) => {
      let chosen: string | null = null;
      const menu = Menu.buildFromTemplate(
        items.map((it) =>
          "separator" in it
            ? { type: "separator" as const }
            : { label: String(it.label), enabled: it.enabled !== false, type: it.checked === undefined ? ("normal" as const) : ("checkbox" as const), checked: it.checked, click: () => (chosen = it.id) },
        ),
      );
      menu.popup({ window: BrowserWindow.fromWebContents(e.sender) ?? undefined, callback: () => setTimeout(() => resolve(chosen)) });
    });
  });
  ipcMain.on("run-in-terminal", (e, c: TerminalCommand) => {
    if (!trusted(e) || !(c in terminalCommands) || process.platform !== "darwin") return;
    // A .command file opens in Terminal and runs there, in the person's login shell.
    const dir = path.join(os.homedir(), "Library", "Caches", "Hermesh");
    fs.mkdirSync(dir, { recursive: true });
    const file = path.join(dir, `${c}.command`);
    fs.writeFileSync(file, `#!/bin/zsh -l\n${appCLIs}\n${terminalCommands[c]}\n`, { mode: 0o700 });
    shell.openPath(file);
  });

  // Signing in (main/signin.ts). Failures come back as { error } with a
  // message for the person (the preload throws it).
  const answer =
    <A extends unknown[], T>(f: (...a: A) => T | Promise<T>) =>
    async (e: IpcMainInvokeEvent, ...a: A) => {
      if (!trusted(e)) return { error: "not allowed" };
      try {
        return { value: await f(...a) };
      } catch (err) {
        return { error: errorMessage(err) };
      }
    };
  ipcMain.handle("sign-in", answer((center: unknown) => signIn(String(center))));
  ipcMain.handle("sign-in-pending", answer(signInPending));
  ipcMain.handle("cancel-sign-in", answer(cancelSignIn));
  ipcMain.handle("complete-sign-in", answer((o: { localMachine?: unknown }) => completeSignIn({ localMachine: o?.localMachine === true })));
  ipcMain.handle("connect-url", answer((center: unknown) => connectURL(String(center))));
  ipcMain.handle("computer-name", answer(computerName));
  ipcMain.handle("take-links", answer(takeLinks));

  // Notifications, the menu bar and opening at login (main/notify.ts, main/daemon.ts).
  ipcMain.handle("preferences", (e) => (trusted(e) ? preferences() : null));
  ipcMain.handle("set-preferences", (e, p: Partial<Preferences>) => (trusted(e) && typeof p === "object" && p !== null ? setPreferences(p) : null));
  ipcMain.handle("login-item", (e) => (trusted(e) ? loginItem() : null));
  ipcMain.handle("set-login-item", (e, on: unknown) => (trusted(e) && typeof on === "boolean" ? setLoginItem(on) : null));
  ipcMain.handle("daemon-needs-approval", (e) => trusted(e) && daemonNeedsApproval());
  ipcMain.on("open-login-items", (e) => trusted(e) && openLoginItems());
  ipcMain.on("open-activity-monitor", (e) => trusted(e) && process.platform === "darwin" && void shell.openPath("/System/Applications/Utilities/Activity Monitor.app"));

  // Updates (main/updater.ts).
  ipcMain.on("open-updates", (e, manual) => trusted(e) && openUpdates(manual === true));
  ipcMain.on("update-window-height", (e, height: unknown) => trusted(e) && sizeUpdates(e.sender, height));
  ipcMain.on("open-whats-new", (e) => trusted(e) && showWhatsNew());
  ipcMain.handle("snooze-update", (e) => trusted(e) && snooze());
  ipcMain.on("return-to-approval", (e) => trusted(e) && returnToApproval());
  ipcMain.on("updates-online", (e) => trusted(e) && catchUp());
  ipcMain.handle("update", (e) => (trusted(e) ? updateInfo() : null));
  ipcMain.on("check-for-updates", (e) => trusted(e) && check());
  ipcMain.on("download-update", (e) => trusted(e) && download());
  ipcMain.on("install-update", (e) => trusted(e) && install());
  ipcMain.handle("set-auto-download", (e, on: unknown) => (trusted(e) && typeof on === "boolean" ? setAutoDownload(on) : null));
}
