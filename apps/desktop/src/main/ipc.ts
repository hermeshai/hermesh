import { app, BrowserWindow, dialog, ipcMain, Menu, shell, type IpcMainEvent, type IpcMainInvokeEvent, type WebContents } from "electron";
import path from "node:path";
import type { Command, MenuItem, MenuState, Preferences, TerminalCommand } from "../shared/bridge";
import { preferences, setPreferences } from "./notify";
import { platform } from "./platform";
import { cancelSignIn, completeSignIn, connectURL, errorMessage, signIn, signInPending, takeLinks } from "./signin";
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

// Commands run in a terminal, by key: the page never passes command text.
const terminalCommands: Record<TerminalCommand, string> = {
  codexLogin: "codex login",
  claudeLogin: "claude",
};

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
  ipcMain.on("app-menu", (e, x: unknown, y: unknown) => {
    if (!trusted(e) || typeof x !== "number" || typeof y !== "number") return;
    Menu.getApplicationMenu()?.popup({ window: BrowserWindow.fromWebContents(e.sender) ?? undefined, x: Math.round(x), y: Math.round(y) });
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
    if (trusted(e) && c in terminalCommands) platform.runInTerminal(c, terminalCommands[c]);
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
  ipcMain.handle("computer-name", answer(platform.computerName));
  ipcMain.handle("ready-signer", answer(platform.readySigner));
  ipcMain.handle("take-links", answer(takeLinks));

  // Notifications, the menu bar and opening at login (main/notify.ts, main/platform).
  ipcMain.handle("preferences", (e) => (trusted(e) ? preferences() : null));
  ipcMain.handle("set-preferences", (e, p: Partial<Preferences>) => (trusted(e) && typeof p === "object" && p !== null ? setPreferences(p) : null));
  ipcMain.handle("login-item", (e) => (trusted(e) ? platform.loginItem() : null));
  ipcMain.handle("set-login-item", (e, on: unknown) => (trusted(e) && typeof on === "boolean" ? platform.setLoginItem(on) : null));
  ipcMain.handle("daemon-needs-approval", (e) => trusted(e) && platform.daemonNeedsApproval());
  ipcMain.on("open-login-items", (e) => trusted(e) && platform.openLoginItems());
  ipcMain.on("open-activity-monitor", (e) => trusted(e) && platform.openProcessMonitor());
  // A folder only, opened by the system's file browser or a terminal there: never a file or an app run.
  ipcMain.on("open-folder", (e, dir: unknown, to: unknown) => {
    if (trusted(e) && (to === "files" || to === "terminal") && typeof dir === "string" && path.isAbsolute(dir)) platform.openFolder(path.normalize(dir), to);
  });
  // Only what erasing everything kept (hermeshd's kept-worktrees), never any path the page names.
  ipcMain.on("reveal-kept-worktree", (e, p) => {
    if (trusted(e) && typeof p === "string" && path.isAbsolute(p) && path.normalize(p).split(path.sep).includes("kept-worktrees")) shell.showItemInFolder(path.normalize(p));
  });

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
