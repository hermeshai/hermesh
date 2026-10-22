import { app, BrowserWindow } from "electron";
import path from "node:path";

// The floor: Electron opening a window like the main window (same size,
// title bar and material, sandboxed) with a static page and no app code.
// What the benchmark measures here no app can do better than.
app.whenReady().then(() => {
  const win = new BrowserWindow({
    width: 1240,
    height: 800,
    show: false,
    titleBarStyle: "hiddenInset",
    vibrancy: "sidebar",
    visualEffectState: "followWindow",
    backgroundColor: "#00000000",
    webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false, spellcheck: false },
  });
  win.once("ready-to-show", () => win.show());
  win.loadFile(path.join(import.meta.dirname, "index.html"));
});
