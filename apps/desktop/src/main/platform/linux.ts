import { app, nativeTheme, shell } from "electron";
import { execFile, spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import type { TerminalCommand } from "../../shared/bridge";
import { testDataDir } from "../paths";
import type { Platform, WindowKind } from "./types";

// The installed app (a package, docs/linux.md §7.2) runs hermeshd as the
// systemd user service it is on every Linux machine, so it keeps running
// when the app quits. Development and test runs use a daemon of their own,
// as does a packaged app pointed at another daemon (HERMESH_DATA_DIR).
const installed = app.isPackaged && process.platform === "linux" && !process.env.HERMESH_DATA_DIR && !testDataDir;
// The programs the package carries, beside the app's resources.
const bundled = (name: string) => path.join(process.resourcesPath, "..", "bin", name);
// What opens the app at login, where the desktops look for it.
const autostart = () => path.join(process.env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config"), "autostart", "hermesh.desktop");
let devLoginItem = false;

/** Starts the first of `programs` there is, each with its own arguments; the next when one is not installed. */
function first(programs: [string, string[]][], cwd?: string) {
  const [next, ...rest] = programs;
  if (!next) return;
  const child = spawn(next[0], next[1], { cwd, detached: true, stdio: "ignore" });
  child.on("error", () => first(rest, cwd));
  child.unref();
}

/** The desktop's terminal running `args` (none: a shell): the one the person chose, else the desktops' own. */
const terminal = (args: string[]): [string, string[]][] => [
  ["xdg-terminal-exec", args],
  ["x-terminal-emulator", args.length ? ["-e", ...args] : []],
  ["gnome-terminal", args.length ? ["--", ...args] : []],
  ["konsole", args.length ? ["-e", ...args] : []],
  ["xterm", args.length ? ["-e", ...args] : []],
];

export const linux: Platform = {
  token: "linux",
  installed,
  // The window has the desktop's own frame and no material behind the page.
  material: false,

  settle() {
    return Promise.resolve(true);
  },

  installerEvent() {
    return false;
  },

  // The package's desktop entry names the scheme; this makes it the user's choice too.
  registerLinks() {
    if (app.isPackaged) app.setAsDefaultProtocolClient("hermesh");
  },

  registerDaemon() {
    if (installed) execFile(bundled("hermeshd"), ["service", "install", "--owner"], (err) => err && console.warn("registering hermeshd:", err));
  },

  restartDaemon(why) {
    if (!installed) return;
    console.warn(`restarting hermeshd: ${why}`);
    execFile("systemctl", ["--user", "restart", "hermeshd.service"], (err) => err && console.warn("restarting hermeshd:", err));
  },

  // systemd asks nobody before it runs a user's own service.
  daemonNeedsApproval() {
    return false;
  },

  loginItem() {
    return { on: installed ? fs.existsSync(autostart()) : devLoginItem, needsApproval: false };
  },

  setLoginItem(on) {
    if (!installed) devLoginItem = on;
    else if (on) {
      fs.mkdirSync(path.dirname(autostart()), { recursive: true });
      fs.writeFileSync(autostart(), `[Desktop Entry]\nType=Application\nName=Hermesh\nExec="${process.execPath}" --login\nX-GNOME-Autostart-enabled=true\n`);
    } else fs.rmSync(autostart(), { force: true });
    return linux.loginItem();
  },

  /** Opened by the autostart entry: the app starts without its window. */
  openedAtLogin() {
    return installed && process.argv.includes("--login");
  },

  // No desktop has one place for this that every other has too.
  openLoginItems() {},

  // The signer is a service of the system's, not the app's child (docs/linux.md §7.3).
  signer() {
    return undefined;
  },
  async readySigner() {},
  async signerUpdate() {
    return undefined;
  },
  async updateSigner() {},
  async elevatesSilently() {
    return false;
  },
  openElevationSettings() {},

  runInTerminal(c: TerminalCommand, command: string) {
    if (c === "antigravityLogin" && app.isPackaged) command = `'${bundled("hermesh").replaceAll("'", "'\\''")}' agent login antigravity`;
    // A script the terminal runs in the person's login shell, which stays open when the command ends.
    const dir = path.join(process.env.XDG_CACHE_HOME || path.join(os.homedir(), ".cache"), "hermesh");
    fs.mkdirSync(dir, { recursive: true });
    const file = path.join(dir, `${c}.sh`);
    fs.writeFileSync(file, `#!/bin/sh\n${command}\nexec "\${SHELL:-/bin/sh}" -l\n`, { mode: 0o700 });
    first(terminal(["sh", "-lc", `exec '${file.replaceAll("'", "'\\''")}'`]));
  },

  openFolder(dir, to) {
    if (!fs.statSync(dir, { throwIfNoEntry: false })?.isDirectory()) return;
    if (to === "files") void shell.openPath(dir);
    else first(terminal([]), dir);
  },

  openProcessMonitor() {
    first([
      ["gnome-system-monitor", []],
      ["plasma-systemmonitor", []],
      ["ksysguard", []],
      ["xfce4-taskmanager", []],
    ]);
  },

  /** The name the person gave the computer, else its host name. */
  async computerName() {
    try {
      const { stdout } = await promisify(execFile)("hostnamectl", ["--pretty"]);
      if (stdout.trim()) return stdout.trim();
    } catch {
      // No systemd: the host name.
    }
    return os.hostname();
  },

  frame(_kind: WindowKind) {
    // The desktop draws the frame and its buttons; the page's own background from the first frame.
    return { backgroundColor: nativeTheme.shouldUseDarkColors ? "#1e1e21" : "#ffffff", autoHideMenuBar: true };
  },

  // The desktop places the window's buttons.
  placeButtons() {},

  grow(win, bounds) {
    win.setBounds(bounds);
  },
};
