import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { LocalService } from "@hermesh/proto/local";
import { app, BrowserWindow, nativeTheme, shell } from "electron";
import { execFile, spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import type { LoginItem } from "../../shared/bridge";
import { i18n } from "../../shared/i18n";
import { newer } from "../../shared/update";
import { socketPath, testDataDir } from "../paths";
import type { Platform, WindowKind } from "./types";

// The Windows app is an MSIX package (docs/windows.md §6.1). Its manifest
// declares hermeshd's startup task (hermeshdw.exe, the windowless build,
// which keeps the daemon running). Open Hermesh at login is the user's Run
// value, which Electron writes: the package writes the real registry
// (no write virtualization), and Startup apps lists and switches it.
// Development and test runs use a daemon of their own and touch neither.
const installed = app.isPackaged && process.platform === "win32" && !process.env.HERMESH_DATA_DIR && !testDataDir;
const daemonArgs = ["run", "--supervise", "--owner"];
let devLoginItem = false;
/** Opened at login, the app gets this argument: in the notification area only. */
const atLogin = { path: process.execPath, args: ["--hermesh-at-login"] };

// The package's programs, beside Hermesh.exe (package-win.mjs).
const program = (name: string) => path.join(path.dirname(process.execPath), "bin", name);
const daemonBinary = () => program("hermeshdw.exe");

/** Whether hermeshd answers on its pipe now. */
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

function startDaemon() {
  const p = spawn(daemonBinary(), daemonArgs, { detached: true, stdio: "ignore", windowsHide: true });
  p.on("error", (e) => console.warn("starting hermeshd:", e));
  p.unref();
}

/**
 * A startup task of the package's (StartupTask, through PowerShell's WinRT):
 * its state, after turning it on when asked.
 */
async function startupTask(id: string, set?: "on"): Promise<string> {
  const script = `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Runtime.WindowsRuntime
$null = [Windows.ApplicationModel.StartupTask, Windows.ApplicationModel, ContentType = WindowsRuntime]
$asTask = [System.WindowsRuntimeSystemExtensions].GetMethods() | Where-Object { $_.Name -eq 'AsTask' -and $_.IsGenericMethod -and $_.GetParameters().Count -eq 1 -and $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncOperation\`1' } | Select-Object -First 1
function Await($op, [Type]$t) { $task = $asTask.MakeGenericMethod($t).Invoke($null, @($op)); $null = $task.Wait(-1); $task.Result }
$st = Await ([Windows.ApplicationModel.StartupTask]::GetAsync('${id}')) ([Windows.ApplicationModel.StartupTask])
switch ('${set ?? ""}') {
  'on' { (Await ($st.RequestEnableAsync()) ([Windows.ApplicationModel.StartupTaskState])).ToString() }
  default { $st.State.ToString() }
}`;
  const { stdout } = await promisify(execFile)("powershell.exe", ["-NoProfile", "-NonInteractive", "-Command", script], { windowsHide: true, timeout: 20_000 });
  return stdout.trim();
}

/** Open at login: on, and whether the owner turned it off in Startup apps. */
function loginItem(): LoginItem {
  const s = app.getLoginItemSettings(atLogin);
  return { on: s.openAtLogin, needsApproval: s.openAtLogin && !s.executableWillLaunchAtLogin };
}

const powershell = (script: string, timeout = 20_000) =>
  promisify(execFile)("powershell.exe", ["-NoProfile", "-NonInteractive", "-Command", script], { windowsHide: true, timeout });

/** The signer as set up: a copy of the app's, which only administrators change (tests: one of their own). */
const installedSigner = () =>
  path.join((!app.isPackaged && process.env.HERMESH_TEST_SIGNER_DIR) || path.join(process.env.ProgramFiles || "C:\\Program Files", "Hermesh Signer"), "hermesh-signer.exe");

/**
 * Sets the app's signer up, elevated (one UAC prompt): it copies itself
 * over the installed one, if any, keeping the keys, and starts its task.
 */
async function setUpSigner() {
  const { stdout } = await powershell("[System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value");
  const bin = program("hermesh-signer.exe").replace(/'/g, "''");
  const script = `try { $p = Start-Process -FilePath '${bin}' -ArgumentList 'setup','--user','${stdout.trim()}' -Verb RunAs -WindowStyle Hidden -Wait -PassThru; exit $p.ExitCode } catch { exit 1223 }`;
  let status = 0;
  try {
    await powershell(script, 5 * 60_000);
  } catch (e) {
    status = typeof (e as { code?: unknown }).code === "number" ? (e as { code: number }).code : 1;
  }
  if (status) throw new Error(i18n.t(signerRefusals[status as keyof typeof signerRefusals] ?? "main:signer.failed", { code: status }));
  if (!(await signerWatching(20_000))) throw new Error(i18n.t("main:signer.notRunning"));
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

/** The signer's task (hermesh-signer: setup_windows.go), named after the user as there. */
const signerTask = () => `Hermesh Signer (${(process.env.USERNAME ?? "").replace(/[\\/:*?"<>|]/g, "_")})`;

// The signer's own exit statuses when it can't keep a key apart (main_windows.go), and a declined UAC prompt.
const signerRefusals = { 3: "main:signer.notAdmin", 4: "main:signer.uacOff", 5: "main:signer.noTPM", 1223: "main:signer.declined" } as const;

const symbols = () => (nativeTheme.shouldUseDarkColors ? "#ffffff" : "#1a1a1a");
/** The caption buttons as high as the top band: the first run's (48 px) or the toolbar's (44 px). */
const buttonsHeight = (kind: WindowKind) => (kind === "onboarding" ? 48 : 44);

export const win: Platform = {
  token: "win",
  installed,

  // The package is installed where it runs, and its manifest declares the hermesh: protocol.
  settle() {
    return Promise.resolve(true);
  },
  registerLinks() {},

  registerDaemon() {
    if (!installed) return;
    // The startup task starts it at logon; until then, or when it stopped, the app does
    // (one supervisor runs per data folder, so a second one leaves at once).
    void answers().then((ok) => ok || startDaemon());
    // An update moves the package's folder: Open at login follows it.
    if (app.getLoginItemSettings(atLogin).launchItems.some((i) => i.path !== process.execPath)) app.setLoginItemSettings({ ...atLogin, openAtLogin: true });
    // On from the first launch, unless the owner turned it off in Startup apps.
    void startupTask("HermeshDaemon")
      .then(async (state) => {
        if (state === "Disabled") await startupTask("HermeshDaemon", "on");
      })
      .catch((e) => console.warn("hermeshd's startup task:", e));
    // The caption buttons follow the theme.
    nativeTheme.on("updated", () => {
      for (const w of BrowserWindow.getAllWindows()) {
        try {
          w.setTitleBarOverlay({ symbolColor: symbols() });
        } catch {
          // A window without the overlay (the notification area's panel).
        }
      }
    });
  },

  restartDaemon(why) {
    if (!installed) return;
    console.warn(`restarting hermeshd: ${why}`);
    // Its supervisor, and with it the daemon (its job ends with the supervisor); once gone, the one in this package.
    const stop = `Get-CimInstance Win32_Process -Filter "Name='hermeshdw.exe'" | Where-Object { $_.CommandLine -like '*--supervise*' -and $_.CommandLine -like '*--owner*' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force; Wait-Process -Id $_.ProcessId -Timeout 10 -ErrorAction SilentlyContinue }`;
    execFile("powershell.exe", ["-NoProfile", "-NonInteractive", "-Command", stop], { windowsHide: true }, (err) => {
      if (err) console.warn("stopping hermeshd:", err);
      startDaemon();
    });
  },

  daemonNeedsApproval() {
    return false; // read with the app's own task in Settings (needsApproval): DisabledByUser
  },

  loginItem() {
    return installed ? loginItem() : { on: devLoginItem, needsApproval: false };
  },

  setLoginItem(on) {
    if (!installed) {
      devLoginItem = on;
      return { on, needsApproval: false };
    }
    app.setLoginItemSettings({ ...atLogin, openAtLogin: on });
    return loginItem();
  },

  openedAtLogin() {
    return installed && process.argv.includes("--hermesh-at-login");
  },

  openLoginItems() {
    shell.openExternal("ms-settings:startupapps");
  },

  // The protected signer (docs/windows.md §7.3) is started by its own task,
  // elevated, never by the app.
  signer() {
    return undefined;
  },

  /**
   * The signer watches already; or its task, set up but stopped, starts
   * again; or it is set up now: Windows asks once for permission (UAC), and
   * the signer copies itself where only administrators write and starts.
   */
  async readySigner() {
    if (await signerWatching(1000)) return;
    try {
      await promisify(execFile)("schtasks.exe", ["/Run", "/TN", signerTask()], { windowsHide: true });
      if (await signerWatching(10_000)) return;
    } catch {
      // Not set up yet.
    }
    await setUpSigner();
  },

  async signerUpdate() {
    if (!fs.existsSync(installedSigner())) return undefined;
    try {
      const { stdout } = await promisify(execFile)(installedSigner(), ["version"], { windowsHide: true, timeout: 10_000 });
      const v = stdout.trim();
      return newer(app.getVersion(), v) ? v : undefined;
    } catch {
      return undefined;
    }
  },

  updateSigner: setUpSigner,

  runInTerminal(_c, command) {
    // A tab of Windows Terminal, else a PowerShell window; either stays when the command ends.
    const powershell = ["powershell.exe", "-NoExit", "-Command", command];
    const wt = spawn("wt.exe", powershell, { detached: true, stdio: "ignore" });
    wt.on("error", () => spawn("cmd.exe", ["/c", "start", "", ...powershell], { detached: true, stdio: "ignore", windowsHide: true }).unref());
    wt.unref();
  },

  openFolder(dir, to) {
    if (!fs.statSync(dir, { throwIfNoEntry: false })?.isDirectory()) return;
    if (to === "files") return void spawn("explorer.exe", [dir], { detached: true, stdio: "ignore" }).unref();
    // A tab of Windows Terminal there, else a PowerShell window.
    const wt = spawn("wt.exe", ["-d", dir], { detached: true, stdio: "ignore" });
    wt.on("error", () => spawn("cmd.exe", ["/c", "start", "", "powershell.exe", "-NoExit"], { cwd: dir, detached: true, stdio: "ignore", windowsHide: true }).unref());
    wt.unref();
  },

  openProcessMonitor() {
    spawn("taskmgr.exe", [], { detached: true, stdio: "ignore" }).unref();
  },

  async computerName() {
    return os.hostname();
  },

  frame(kind: WindowKind) {
    // The page's own background from the first frame (--win), for no white flash in the dark.
    const backgroundColor = nativeTheme.shouldUseDarkColors ? "#1e1e21" : "#ffffff";
    if (kind === "panel") return { backgroundColor };
    // Windows draws the caption buttons over the page's top band, whose toolbar rows are the drag area.
    return { titleBarStyle: "hidden", titleBarOverlay: { color: "#00000000", symbolColor: symbols(), height: buttonsHeight(kind) }, backgroundColor };
  },

  placeButtons(win, kind) {
    win.setTitleBarOverlay({ height: buttonsHeight(kind) });
  },

  grow(win, to) {
    // Windows doesn't animate setBounds: a few frames of it, easing out, as macOS does.
    const from = win.getBounds();
    const start = Date.now();
    const step = () => {
      if (win.isDestroyed()) return;
      const t = Math.min(1, (Date.now() - start) / 200);
      const e = 1 - (1 - t) ** 3;
      const at = (a: number, b: number) => Math.round(a + (b - a) * e);
      win.setBounds({ x: at(from.x, to.x), y: at(from.y, to.y), width: at(from.width, to.width), height: at(from.height, to.height) });
      if (t < 1) setTimeout(step, 16);
    };
    step();
  },
};
