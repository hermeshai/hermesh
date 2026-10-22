import { sessionFilter, sessionSections } from "@hermesh/core";
import type { Session } from "@hermesh/proto/session";
import { app, BrowserWindow, ipcMain, nativeImage, screen, Tray, type NativeImage } from "electron";
import glyph1x from "../../resources/menuBarIconTemplate.png?inline";
import glyph2x from "../../resources/menuBarIconTemplate@2x.png?inline";
import type { TrayAction, TrayView } from "../shared/bridge";
import { command } from "./ipc";
import { preferences, world, worldListeners } from "./notify";
import { install, openUpdates, updateInfo, updateListeners } from "./updater";
import { ours, showMain, showSettings, trayPanel } from "./windows";

// The menu bar extra: a panel under the icon with how many sessions need the
// owner and how many run, whether agents can reach this Mac, settings and
// quit, also while no window is open. Windows and Linux come later
// (docs/desktop-app.md §9).

let tray: Tray | undefined;
let panel: BrowserWindow | undefined;
let shownIcon = "";
// Set while the panel's window is made (it is announced before panel is assigned).
let makingPanel = false;
// Its renderer goes 30 s after it is hidden: opened again soon it shows at once, idle it costs nothing.
let closing: NodeJS.Timeout | undefined;

export function showTray() {
  if (process.platform !== "darwin") return;
  tray = new Tray(icon(false, true));
  tray.setToolTip("Hermesh");
  tray.on("click", toggle);
  tray.on("right-click", toggle);
  update();
  worldListeners.add(update);
  updateListeners.add(update);
  ipcMain.handle("tray-view", (e) => (e.senderFrame && ours(e.senderFrame.url) ? view() : null));
  ipcMain.on("tray-action", (e, a: TrayAction) => e.sender === panel?.webContents && act(a));
  ipcMain.on("tray-size", (e, h: unknown) => {
    if (e.sender === panel?.webContents && typeof h === "number" && h > 0 && h < 600) panel.setSize(268, Math.ceil(h));
  });
  // With no window open the app lives in the menu bar only (Open Hermesh at login, in the menu bar only). The panel doesn't count.
  const watch = (w: BrowserWindow) =>
    w.on("closed", () => BrowserWindow.getAllWindows().every((x) => x === w || x === panel) && app.dock?.hide());
  BrowserWindow.getAllWindows().forEach(watch);
  app.on("browser-window-created", (_, w) => {
    if (makingPanel) return;
    app.dock?.show();
    watch(w);
  });
  if (BrowserWindow.getAllWindows().length === 0) app.dock?.hide();
}

/** For tests: what the panel shows, whether it is open, and whether the icon shows its dot. */
export function trayState() {
  return { view: view(), open: panel?.isVisible() ?? false, badge: shownIcon.startsWith("dot"), panel: () => panel, toggle };
}

function update() {
  if (!tray) return;
  const v = view();
  const badge = preferences().menuBadge && v.attention > 0;
  const key = `${badge ? "dot" : ""}${v.state === "ok" ? "" : "faded"}`;
  if (key !== shownIcon) {
    tray.setImage(icon(badge, v.state !== "ok"));
    shownIcon = key;
  }
  panel?.webContents.send("tray-view", v);
}

function sections() {
  return sessionSections(world.sessions, sessionFilter(), new Date());
}

/** Questions first, then failures, as the sidebar lists them. */
function attention(): Session[] {
  return sections().find((x) => x.bucket === "attention")?.sessions ?? [];
}

function running(): Session[] {
  return sections().find((x) => x.bucket === "active")?.sessions ?? [];
}

function view(): TrayView {
  const st = world.status;
  const u = updateInfo().update;
  return {
    state: !st ? "daemon" : !st.initialized && !st.machineOnly ? "setup" : st.relayConnected ? "ok" : "down",
    online: world.machines.filter((m) => m.online).length,
    attention: attention().length + world.pairings.length,
    running: running().length,
    update: u.state === "ready" ? u.version : "",
  };
}

/** Opens the panel under the icon, or closes it. It closes by itself when it loses focus. */
function toggle() {
  if (!tray) return;
  if (!panel) {
    makingPanel = true;
    panel = trayPanel();
    makingPanel = false;
    panel.on("blur", () => panel?.hide());
    panel.on("hide", () => (closing = setTimeout(() => panel?.destroy(), 30_000)));
    panel.on("closed", () => (panel = undefined));
  }
  if (panel.isVisible()) return panel.hide();
  clearTimeout(closing);
  const b = tray.getBounds();
  const area = screen.getDisplayMatching(b).workArea;
  const { width } = panel.getBounds();
  const x = Math.round(Math.min(Math.max(area.x + 8, b.x + b.width / 2 - width / 2), area.x + area.width - width - 8));
  panel.setPosition(x, Math.round(b.y + b.height + 5));
  panel.webContents.send("tray-view", view());
  panel.show();
}

function act(a: TrayAction) {
  panel?.hide();
  if (a === "attention") {
    // A pairing request first (it times out), then the sessions in the sidebar's order.
    const p = world.pairings[0];
    const s = attention()[0];
    return p ? command(`pairing:${p.code}`) : s ? command(`open:${s.sessionId}`) : showMain();
  }
  if (a === "running") {
    const s = running()[0];
    return s ? command(`open:${s.sessionId}`) : showMain();
  }
  if (a === "open") return showMain();
  if (a === "settings") return showSettings();
  if (a === "updates") return openUpdates(true);
  if (a === "install") return install();
  if (a === "quit") return app.quit();
}

// --- Images, drawn per pixel at 1x and 2x so their edges stay sharp ---

/** How much of a pixel (scale pixels per point) at d points from a circle's centre the circle of radius r covers. */
function cover(r: number, d: number, scale: number): number {
  return Math.min(1, Math.max(0, (r - d) * scale + 0.5));
}

/**
 * The brand's menu bar template (brand/macos/MenuBarIconTemplate.svg, as
 * PNGs). Not connected: 40% opacity. Something waits on the owner: a dot,
 * bottom right, with a gap that keeps it apart from the beam beneath.
 */
function icon(badge: boolean, faded: boolean): NativeImage {
  const img = nativeImage.createEmpty();
  [glyph1x, glyph2x].forEach((url, i) => {
    const scale = i + 1;
    const png = nativeImage.createFromDataURL(url);
    const { width, height } = png.getSize();
    const px = png.toBitmap();
    for (let y = 0; y < height; y++) {
      for (let x = 0; x < width; x++) {
        const k = (y * width + x) * 4;
        let a = (px[k + 3] / 255) * (faded ? 0.4 : 1);
        if (badge) {
          const d = Math.hypot((x + 0.5) / scale - 15.25, (y + 0.5) / scale - 15.25);
          a *= 1 - cover(3.45, d, scale);
          a += (1 - a) * cover(2.25, d, scale);
        }
        px.set([0, 0, 0, Math.round(a * 255)], k);
      }
    }
    img.addRepresentation({ scaleFactor: scale, width, height, buffer: px });
  });
  img.setTemplateImage(true);
  return img;
}
