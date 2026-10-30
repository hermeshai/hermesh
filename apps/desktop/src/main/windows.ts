import { language } from "@hermesh/core";
import { app, BrowserWindow, screen, shell, type WebContents } from "electron";
import os from "node:os";
import path from "node:path";
import { i18n } from "../shared/i18n";
import { platform } from "./platform";

export const origin = "app://hermesh";
export const devURL = !app.isPackaged ? process.env.ELECTRON_RENDERER_URL : undefined;

let main: BrowserWindow | undefined;
let settings: BrowserWindow | undefined;
const auxiliary = new Map<string, BrowserWindow>();
let mode: "onboarding" | "main" = "main";

const sizes = {
  onboarding: { width: 720, height: 660, minWidth: 720, minHeight: 600 },
  main: { width: 1240, height: 800, minWidth: 980, minHeight: 620 },
};

/** Is url one of our own pages (the only pages that may call the main process)? */
export function ours(url: string): boolean {
  return url.startsWith(origin + "/") || (devURL !== undefined && url.startsWith(devURL));
}

function page(hash = ""): string {
  return (devURL ?? `${origin}/index.html`) + hash;
}

function secure(win: BrowserWindow) {
  const wc = win.webContents;
  // Nothing but our own pages, ever; links open in the browser.
  wc.on("will-navigate", (e, url) => {
    if (!ours(url)) e.preventDefault();
  });
  wc.setWindowOpenHandler(({ url }) => {
    if (url.startsWith("https://")) shell.openExternal(url);
    return { action: "deny" };
  });
  // Its titles go grey while another window has the focus, as macOS titles do.
  const focus = () => wc.send("window-focus", win.isFocused());
  win.on("focus", focus);
  win.on("blur", focus);
  wc.on("did-finish-load", focus);
}

function webPreferences() {
  return {
    preload: path.join(import.meta.dirname, "../preload/index.cjs"),
    sandbox: true,
    contextIsolation: true,
    nodeIntegration: false,
    spellcheck: false,
    additionalArguments: [`--hermesh-user=${os.userInfo().username}`, `--hermesh-version=${app.getVersion()}`, `--hermesh-lang=${language()}`],
  };
}

export function mainWindow(): BrowserWindow | undefined {
  return main;
}

export function showMain(): BrowserWindow {
  if (main) {
    main.show();
    main.focus();
    return main;
  }
  main = new BrowserWindow({
    ...sizes[mode],
    show: false,
    title: "Hermesh",
    ...platform.frame(mode),
    webPreferences: webPreferences(),
  });
  secure(main);
  main.once("ready-to-show", () => main?.show());
  main.on("closed", () => (main = undefined));
  main.loadURL(page());
  return main;
}

/** The compact first-run window, or the main window (grown in place, animated). */
export function setMode(m: "onboarding" | "main") {
  if (m === mode) return;
  mode = m;
  if (!main) return;
  const s = sizes[m];
  platform.placeButtons(main, m);
  const b = main.getBounds();
  const area = screen.getDisplayMatching(b).workArea;
  const width = Math.min(s.width, area.width);
  const height = Math.min(s.height, area.height);
  const x = Math.round(Math.min(Math.max(area.x, b.x + (b.width - width) / 2), area.x + area.width - width));
  const y = Math.round(Math.min(Math.max(area.y, b.y + (b.height - height) / 2), area.y + area.height - height));
  main.setMinimumSize(Math.min(s.minWidth, width), Math.min(s.minHeight, height));
  main.setBounds({ x, y, width, height }, true);
}

/**
 * The menu bar panel: a small borderless window of the menu material under
 * the menu bar icon (main/tray.ts places, shows and hides it). It never
 * takes the Dock icon or the app's activation.
 */
export function trayPanel(): BrowserWindow {
  const win = new BrowserWindow({
    width: 268,
    height: 190,
    show: false,
    frame: false,
    resizable: false,
    movable: false,
    minimizable: false,
    maximizable: false,
    fullscreenable: false,
    skipTaskbar: true,
    alwaysOnTop: true,
    ...platform.frame("panel"),
    webPreferences: webPreferences(),
  });
  secure(win);
  win.loadURL(page("#tray"));
  return win;
}

export const settingsVisible = () => !!settings && !settings.isDestroyed() && settings.isVisible();

export function showSettings(tab?: string) {
  if (settings) {
    if (tab) settings.webContents.send("settings-tab", tab);
    settings.show();
    settings.focus();
    return;
  }
  settings = new BrowserWindow({
    width: 820,
    height: 620,
    minWidth: 720,
    minHeight: 480,
    show: false,
    title: i18n.t("main:settingsWindow"),
    // As the main window's frame.
    ...platform.frame("settings"),
    minimizable: false,
    fullscreenable: false,
    webPreferences: webPreferences(),
  });
  secure(settings);
  settings.once("ready-to-show", () => settings?.show());
  settings.on("closed", () => (settings = undefined));
  settings.loadURL(page(`#settings${tab ? "/" + tab : ""}`));
}

function showAuxiliary(route: "updates" | "about" | "whats-new", title: string) {
  const existing = auxiliary.get(route);
  if (existing) { existing.show(); existing.focus(); return; }
  const win = new BrowserWindow({
    width: 560, height: 360, minWidth: 440, minHeight: route === "about" ? 340 : 280,
    show: false, title, minimizable: false, fullscreenable: false,
    ...platform.frame("auxiliary"), webPreferences: webPreferences(),
  });
  auxiliary.set(route, win);
  secure(win);
  win.once("ready-to-show", () => win.show());
  win.on("closed", () => auxiliary.delete(route));
  win.loadURL(page(`#${route}`));
}
export const showUpdates = () => showAuxiliary("updates", i18n.t("updates:title"));
export const showAbout = () => showAuxiliary("about", i18n.t("main:menu.app.about"));
export const showWhatsNew = () => showAuxiliary("whats-new", i18n.t("updates:whatsNew"));

/** Only the update and release history windows can fit themselves; longer notes scroll. */
export function sizeUpdates(sender: WebContents, height: unknown) {
  const win = [auxiliary.get("updates"), auxiliary.get("whats-new")].find((w) => w && !w.isDestroyed() && w.webContents === sender);
  if (!win || typeof height !== "number" || !Number.isFinite(height) || height <= 0) return;
  const bounds = win.getBounds();
  const area = screen.getDisplayMatching(bounds).workArea;
  const frame = bounds.height - win.getContentBounds().height;
  const maximum = area.height - 42;
  const next = Math.min(maximum, Math.max(280, Math.ceil(height) + frame));
  if (bounds.height === next) return;
  const y = Math.max(area.y + 21, Math.min(bounds.y, area.y + area.height - next - 21));
  win.setBounds({ height: next, y });
}
