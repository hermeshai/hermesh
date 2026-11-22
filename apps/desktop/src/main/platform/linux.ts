import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { language } from "@hermesh/core";
import { LocalService } from "@hermesh/proto/local";
import { app, nativeTheme, shell } from "electron";
import { execFile, spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import type { TerminalCommand } from "../../shared/bridge";
import { i18n } from "../../shared/i18n";
import { socketPath, testDataDir } from "../paths";
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

// The protected signer (docs/linux.md §7.3), a service of the system's the package installs; test runs name one of their own.
const signerSocket = () => (!app.isPackaged && process.env.HERMESH_TEST_SIGNER_SOCKET) || "/run/hermesh-signer.sock";
// The signer's refusals (cmd/hermesh-signer serve_linux.go), in the app's words.
const signerRefusals = { open_sudo: "main:signer.openSudo", no_polkit: "main:signer.noPolkit", not_yours: "main:signer.notYours" } as const;

/**
 * Asks the signer to watch this hermeshd's signing requests, and to ask this app's process, in the desktop's session,
 * when one waits for the owner; again at each start, as the process is another one.
 */
function askSigner(): Promise<void> {
  return new Promise((resolve, reject) => {
    const c = net.connect(signerSocket());
    let answer = "";
    c.setTimeout(15_000, () => c.destroy(new Error(i18n.t("main:signer.notRunning"))));
    c.on("connect", () => c.write(`${JSON.stringify({ socket: socketPath(), language: language() })}\n`));
    c.on("data", (d) => (answer += d.toString()));
    c.on("error", (e: NodeJS.ErrnoException) => reject(new Error(i18n.t(e.code === "ENOENT" || e.code === "ECONNREFUSED" ? "main:signer.noService" : "main:signer.notRunning"))));
    c.on("end", () => {
      try {
        const r = JSON.parse(answer) as { ok?: boolean; error?: string; message?: string };
        if (r.ok) return resolve();
        const refusal = r.error && r.error in signerRefusals ? signerRefusals[r.error as keyof typeof signerRefusals] : undefined;
        reject(new Error(refusal ? i18n.t(refusal) : (r.message ?? i18n.t("main:signer.notRunning"))));
      } catch {
        reject(new Error(i18n.t("main:signer.notRunning")));
      }
    });
  });
}

/** Whether hermeshd answers on its socket now. */
function answers(): Promise<boolean> {
  return new Promise((resolve) => {
    const c = net.connect(socketPath());
    const done = (ok: boolean) => {
      c.destroy();
      resolve(ok);
    };
    c.once("connect", () => done(true));
    c.once("error", () => done(false));
    c.setTimeout(2000, () => done(false));
  });
}

/** Whether a signer watches hermeshd's requests, within wait ms. */
async function signerWatching(wait: number): Promise<boolean> {
  const local = createClient(LocalService, createConnectTransport({ baseUrl: "http://hermeshd", httpVersion: "1.1", useBinaryFormat: true, nodeOptions: { socketPath: socketPath() } }));
  for (const end = Date.now() + wait; ; ) {
    try {
      if ((await local.getStatus({})).signer?.connected) return true;
    } catch {
      // hermeshd not answering yet
    }
    if (Date.now() > end) return false;
    await new Promise((r) => setTimeout(r, 500));
  }
}

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

  /** hermeshd as this user's systemd service, the owner's and this computer's machine: installed, and started, unless it answers. */
  registerDaemon() {
    if (!installed) return;
    void answers().then((ok) => ok || execFile(bundled("hermeshd"), ["service", "install", "--owner"], (err) => err && console.warn("registering hermeshd:", err)));
  },

  async startDaemon() {
    if (!installed) return;
    try {
      await promisify(execFile)("systemctl", ["--user", "start", "hermeshd.service"]);
    } catch {
      await promisify(execFile)(bundled("hermeshd"), ["service", "install", "--owner"]).catch((err) => console.warn("starting hermeshd:", err));
    }
  },

  async stopDaemon() {
    if (!installed) return;
    await promisify(execFile)("systemctl", ["--user", "stop", "hermeshd.service"]).catch((err) => console.warn("stopping hermeshd:", err));
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
  /** The signer watches this hermeshd, and asks this process: always asked again, as the app may be a new process. */
  async readySigner() {
    try {
      await askSigner();
    } catch (e) {
      // Another signer watches already (a test's, or `hermeshd approve` in a terminal): it answers instead.
      if (await signerWatching(1000)) return;
      throw e;
    }
    if (!(await signerWatching(20_000))) throw new Error(i18n.t("main:signer.notRunning"));
  },
  // The package updates the signer with the app.
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
