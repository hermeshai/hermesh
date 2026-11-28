import { app, BrowserWindow, dialog, ipcMain, Menu, shell, type IpcMainEvent, type IpcMainInvokeEvent, type WebContents } from "electron";
import path from "node:path";
import type { Command, MenuItem, MenuState, PageRect, Preferences, TerminalCommand } from "../shared/bridge";
import { i18n } from "../shared/i18n";
import { openComputerPermission } from "./computer";
import { daemonInfo, quitForRelaunch, startBackgroundService, stopBackgroundService } from "./daemon";
import { cancelHerChatGpt, cancelHerConnector, herConsoleURL, herFilesKept, reopenHerChatGpt, reopenHerConnector, readHerFile, saveHerFile, signInHerChatGpt, signInHerConnector } from "./her";
import { preferences, setPreferences, world } from "./notify";
import { closeSessionPage, openCheckedFolder, openSessionFolder, openSessionPage, openSessionPageInBrowser, placeSessionPage, quickLookSessionFile, refOf, revealSessionFile, saveSessionFile } from "./sessionFiles";
import { platform } from "./platform";
import { cancelSignIn, completeResume, completeSignIn, connectURL, errorMessage, signIn, signInPending, takeLinks } from "./signin";
import { catchUp, check, download, install, openUpdates, returnToApproval, setAutoDownload, snooze, updateInfo } from "./updater";
import { mainWindow, ours, setMode, setNoticeTarget, showHerConsole, showMain, showSettings, showWhatsNew, sizeUpdates } from "./windows";

// The page's requests to the main process (see shared/bridge.ts). Each
// handler checks the request comes from one of our own pages.
function trusted(e: IpcMainEvent | IpcMainInvokeEvent): boolean {
  return !!e.senderFrame && ours(e.senderFrame.url);
}

const noMenuState: MenuState = { main: false, her: false, session: null, sidebar: true, undo: false };
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
  antigravityLogin: "hermesh agent login antigravity",
  copilotLogin: "copilot",
  opencodeLogin: "opencode auth login",
  qoderLogin: "qodercli",
  qwenLogin: "qwen",
  kimiLogin: "kimi",
  codebuddyLogin: "codebuddy",
  grokLogin: "grok",
  auggieLogin: "auggie login",
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
    quitForRelaunch();
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
  ipcMain.on("notice-target", (e, target: unknown, visible: unknown) => {
    if (!trusted(e) || e.senderFrame !== e.sender.mainFrame) return;
    if (typeof target === "string" && typeof visible === "boolean") setNoticeTarget(e.sender, target, visible);
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
  // at: where the menu opens in the window (under a pop-up button); the pointer otherwise.
  ipcMain.handle("popup-menu", (e, items: MenuItem[], at?: { x: number; y: number }) => {
    if (!trusted(e) || !Array.isArray(items)) return null;
    return new Promise<string | null>((resolve) => {
      let chosen: string | null = null;
      const build = (items: MenuItem[]): Electron.MenuItemConstructorOptions[] =>
        items.map((it) => {
          if ("separator" in it) return { type: "separator" as const };
          const detail = it.detail ? String(it.detail) : "";
          const label = detail && process.platform !== "darwin" ? `${it.label}    ${detail}` : String(it.label);
          const sublabel = detail && process.platform === "darwin" ? detail : undefined;
          // An item that opens a menu cannot carry a check: its own first line inside says what is chosen.
          if (Array.isArray(it.submenu)) return { label, sublabel, submenu: build(it.submenu) };
          return { label, sublabel, enabled: it.enabled !== false, type: it.checked === undefined ? ("normal" as const) : ("checkbox" as const), checked: it.checked, click: () => (chosen = it.id) };
        });
      const where = at && typeof at.x === "number" && typeof at.y === "number" ? { x: Math.round(at.x), y: Math.round(at.y) } : {};
      Menu.buildFromTemplate(build(items)).popup({ window: BrowserWindow.fromWebContents(e.sender) ?? undefined, ...where, callback: () => setTimeout(() => resolve(chosen)) });
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
  ipcMain.handle("sign-in", answer((center: unknown, owner: unknown) => signIn(String(center), typeof owner === "string" && owner ? owner : undefined)));
  ipcMain.handle("complete-resume", answer(completeResume));
  ipcMain.handle("sign-in-pending", answer(signInPending));
  ipcMain.handle("cancel-sign-in", answer(cancelSignIn));
  ipcMain.handle("complete-sign-in", answer((o: { localMachine?: unknown }) => completeSignIn({ localMachine: o?.localMachine === true })));
  ipcMain.handle("connect-url", answer((center: unknown) => connectURL(String(center))));
  ipcMain.handle("computer-name", answer(platform.computerName));
  ipcMain.handle("ready-signer", answer(platform.readySigner));
  ipcMain.handle("signer-update", answer(platform.signerUpdate));
  ipcMain.handle("update-signer", answer(platform.updateSigner));
  ipcMain.handle("elevates-silently", answer(platform.elevatesSilently));
  ipcMain.on("open-elevation-settings", (e) => trusted(e) && platform.openElevationSettings());
  ipcMain.handle("take-links", answer(takeLinks));

  // A coding session's files (main/sessionFiles.ts): a page over the window, Quick Look, Downloads, Finder.
  const pageRect = (v: unknown) => {
    const r = v as Record<string, unknown> | null;
    return r && ["x", "y", "width", "height"].every((k) => typeof r[k] === "number" && Number.isFinite(r[k])) ? (r as unknown as PageRect) : null;
  };
  const fileRef = (v: unknown) => {
    const ref = refOf(v);
    if (!ref) throw new Error("no file");
    return ref;
  };
  ipcMain.handle("session-page-open", (e, file: unknown, rect: unknown) => {
    const ref = refOf(file);
    const r = pageRect(rect);
    if (!trusted(e) || !ref || !r) return { value: false };
    return openSessionPage(e.sender, ref, r).then((value) => ({ value }), (err: unknown) => ({ error: errorMessage(err) }));
  });
  ipcMain.on("session-page-place", (e, rect: unknown) => {
    if (!trusted(e)) return;
    if (rect === null) return placeSessionPage(e.sender, null);
    const r = pageRect(rect);
    if (r) placeSessionPage(e.sender, r);
  });
  ipcMain.on("session-page-close", (e) => trusted(e) && closeSessionPage(e.sender));
  ipcMain.handle("session-page-browser", answer((file: unknown) => openSessionPageInBrowser(fileRef(file))));
  ipcMain.handle("session-file-save", answer((file: unknown, name: unknown) => saveSessionFile(fileRef(file), typeof name === "string" ? name : "file")));
  ipcMain.handle("session-file-reveal", answer((file: unknown) => revealSessionFile(fileRef(file))));
  ipcMain.handle("session-file-quicklook", (e, file: unknown, name: unknown) => {
    const ref = refOf(file);
    if (!trusted(e) || !ref) return { error: "no file" };
    return quickLookSessionFile(e.sender, ref, typeof name === "string" ? name : "file").then(() => ({ value: undefined }), (err: unknown) => ({ error: errorMessage(err) }));
  });

  // Her (main/her.ts): a machine's console in its own window, an assistant's file saved to Downloads, and
  // signing in to ChatGPT, or to a connector a secure card connects, for a machine's Her.
  const id = (v: unknown) => (typeof v === "string" && v !== "" ? v : null);
  ipcMain.handle(
    "her-console",
    answer((machineId: unknown, page: unknown) => {
      const m = id(machineId);
      if (!m) throw new Error("no machine");
      const name = world.machines.find((x) => x.machineId === m)?.machineName ?? m;
      // A console page and item by its address's hash, e.g. "browser/connect" or "connectors/my-mcp"; nothing else.
      const at = typeof page === "string" && /^[a-z]+(\/[A-Za-z0-9_-]{1,32})?$/.test(page) ? page : undefined;
      return showHerConsole(m, i18n.t("main:herConsole", { name }), () => herConsoleURL(m), at);
    }),
  );
  ipcMain.handle(
    "her-save-file",
    answer((machineId: unknown, botId: unknown, file: unknown, sha: unknown) => {
      const [m, b, p] = [id(machineId), id(botId), id(file)];
      if (!m || !b || !p) throw new Error("no file");
      return saveHerFile(m, b, p, id(sha) ?? undefined);
    }),
  );
  ipcMain.handle(
    "her-read-file",
    answer((machineId: unknown, botId: unknown, file: unknown, sha: unknown) => {
      const [m, b, p] = [id(machineId), id(botId), id(file)];
      if (!m || !b || !p) throw new Error("no file");
      return readHerFile(m, b, p, id(sha) ?? undefined);
    }),
  );
  ipcMain.handle(
    "her-files-kept",
    answer((machineId: unknown, files: unknown) => {
      const m = id(machineId);
      if (!m || !Array.isArray(files)) throw new Error("no files");
      const pairs = files.flatMap((f) => (Array.isArray(f) && typeof f[0] === "string" && typeof f[1] === "string" ? [[f[0], f[1]] as [string, string]] : []));
      return herFilesKept(m, pairs);
    }),
  );
  ipcMain.handle(
    "her-chatgpt",
    answer((machineId: unknown) => {
      const m = id(machineId);
      if (!m) throw new Error("no machine");
      return signInHerChatGpt(m);
    }),
  );
  ipcMain.on("her-chatgpt-reopen", (e, machineId: unknown) => {
    const m = id(machineId);
    if (trusted(e) && m) reopenHerChatGpt(m);
  });
  ipcMain.on("her-chatgpt-cancel", (e, machineId: unknown) => {
    const m = id(machineId);
    if (trusted(e) && m) cancelHerChatGpt(m);
  });
  ipcMain.handle(
    "her-connector",
    answer((machineId: unknown, name: unknown) => {
      const [m, n] = [id(machineId), id(name)];
      if (!m || !n) throw new Error("no connector");
      return signInHerConnector(m, n);
    }),
  );
  ipcMain.on("her-connector-reopen", (e, machineId: unknown, name: unknown) => {
    const [m, n] = [id(machineId), id(name)];
    if (trusted(e) && m && n) reopenHerConnector(m, n);
  });
  ipcMain.on("her-connector-cancel", (e, machineId: unknown, name: unknown) => {
    const [m, n] = [id(machineId), id(name)];
    if (trusted(e) && m && n) cancelHerConnector(m, n);
  });

  // Notifications, the menu bar and opening at login (main/notify.ts, main/platform).
  ipcMain.handle("preferences", (e) => (trusted(e) ? preferences() : null));
  ipcMain.handle("set-preferences", (e, p: Partial<Preferences>) => (trusted(e) && typeof p === "object" && p !== null ? setPreferences(p) : null));
  ipcMain.handle("login-item", (e) => (trusted(e) ? platform.loginItem() : null));
  ipcMain.handle("set-login-item", (e, on: unknown) => (trusted(e) && typeof on === "boolean" ? platform.setLoginItem(on) : null));
  ipcMain.handle("daemon-needs-approval", (e) => trusted(e) && platform.daemonNeedsApproval());
  ipcMain.handle("daemon-state", (e) => (trusted(e) ? daemonInfo() : null));
  ipcMain.handle("start-daemon", (e) => (trusted(e) ? startBackgroundService() : undefined));
  ipcMain.handle("stop-daemon", (e) => (trusted(e) ? stopBackgroundService({ confirm: true }) : false));
  ipcMain.on("open-login-items", (e) => trusted(e) && platform.openLoginItems());
  ipcMain.on("open-activity-monitor", (e) => trusted(e) && platform.openProcessMonitor());
  // Her's computer use: a permission's pane of System Settings (main/computer.ts).
  ipcMain.on("open-computer-permission", (e, kind: unknown) => trusted(e) && void openComputerPermission(kind));
  // A folder only, opened by the system's file browser or a terminal there: never a file or an app run.
  ipcMain.on("open-folder", (e, dir: unknown, to: unknown) => {
    if (trusted(e) && (to === "files" || to === "terminal") && typeof dir === "string" && path.isAbsolute(dir)) void openCheckedFolder(dir, to);
  });
  ipcMain.handle("open-session-folder", (e, sessionId: unknown, to: unknown) => {
    if (!trusted(e) || (to !== "files" && to !== "terminal") || typeof sessionId !== "string") return false;
    return openSessionFolder(sessionId, to);
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
