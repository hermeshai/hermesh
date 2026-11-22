import { app, BrowserWindow, dialog } from "electron";
import { assessDaemonImpact, hasActiveWork, hasAnyImpact, impactLines, resolveDaemonState, type DaemonInfo, type DaemonState } from "../shared/daemon";
import { i18n } from "../shared/i18n";
import { setGatewayPaused } from "./gateway";
import { preferences, setWatchPaused, wakeWatch, world, worldListeners } from "./notify";
import { platform } from "./platform";
import { signerBusy } from "./signer";

// Coordinates starting, stopping, and impact-aware quitting of hermeshd.
// Keeps the renderer, menu bar, Dock menu, and app menu in sync.

let userStopped = false;
let transition: "starting" | "stopping" | null = null;
let quitFlow: "none" | "handling" | "proceed-keep" | "proceed-stop" = "none";
let guardInstalled = false;

export const daemonListeners = new Set<() => void>();

export function daemonControlAvailable(): boolean {
  return platform.installed;
}

export function daemonState(): DaemonState {
  return resolveDaemonState(world.status !== null, userStopped, transition);
}

export function daemonInfo(): DaemonInfo {
  return { state: daemonState(), manageable: daemonControlAvailable() };
}

function emit() {
  const info = daemonInfo();
  daemonListeners.forEach((f) => f());
  for (const w of BrowserWindow.getAllWindows()) {
    if (!w.isDestroyed()) w.webContents.send("daemon-state", info);
  }
}

/** Watches connectivity changes so daemonState stays current. */
export function initDaemonControl(isStarted: () => boolean) {
  if (guardInstalled) return;
  guardInstalled = true;

  let lastState = daemonState();
  worldListeners.add(() => {
    if (world.status !== null && transition !== "stopping") {
      userStopped = false;
      if (transition === "starting") transition = null;
    }
    const next = daemonState();
    if (next !== lastState) {
      lastState = next;
      emit();
    }
  });

  app.on("before-quit", (e) => {
    if (quitFlow === "proceed-keep" || quitFlow === "proceed-stop") return;
    if (quitFlow === "handling") {
      e.preventDefault();
      return;
    }
    if (!isStarted() || !daemonControlAvailable()) return;
    const st = daemonState();
    if (st === "stopped" || st === "unavailable" || world.status === null) return;
    if (preferences().keepDaemonOnQuit) return;
    e.preventDefault();
    void handleQuitWithStop(false);
  });
}

function dialogOptions(title: string, detail: string, buttons: string[], defaultId: number, cancelId: number, warning: boolean) {
  return {
    type: (warning ? "warning" : "question") as "warning" | "question",
    message: title,
    detail,
    buttons,
    defaultId,
    cancelId,
  };
}

/** Starts the background service and waits briefly for it to reconnect. */
export async function startBackgroundService(): Promise<void> {
  if (!daemonControlAvailable() || transition !== null) return;
  if (daemonState() === "running") return;
  userStopped = false;
  transition = "starting";
  setGatewayPaused(false);
  setWatchPaused(false);
  emit();
  try {
    await platform.startDaemon();
    wakeWatch();
    if (world.status === null) {
      await new Promise<void>((resolve) => {
        const timer = setTimeout(() => {
          worldListeners.delete(check);
          resolve();
        }, 8_000);
        const check = () => {
          if (world.status !== null) {
            clearTimeout(timer);
            worldListeners.delete(check);
            resolve();
          }
        };
        worldListeners.add(check);
      });
    }
  } finally {
    if (transition === "starting") transition = null;
    emit();
  }
}

/** Stops the background service after explaining what will be affected. Returns true when stopped. */
export async function stopBackgroundService(opts: { confirm?: boolean } = {}): Promise<boolean> {
  if (!daemonControlAvailable() || transition !== null) return false;
  if (daemonState() === "stopped" || world.status === null) return false;
  if (opts.confirm !== false) {
    const t = i18n.getFixedT(null, "main");
    const tr = (k: string, v?: Record<string, unknown>) => String(t(k as never, v as never));
    const impact = assessDaemonImpact(world, signerBusy());
    const active = hasActiveWork(impact);
    const { response } = await dialog.showMessageBox(
      dialogOptions(
        t("daemon.stopTitle"),
        impactLines(impact, tr).join("\n"),
        [t("daemon.stopButton"), t("daemon.cancel")],
        active ? 1 : 0,
        1,
        active,
      ),
    );
    if (response !== 0) return false;
  }
  userStopped = true;
  transition = "stopping";
  setGatewayPaused(true);
  setWatchPaused(true);
  emit();
  try {
    await platform.stopDaemon();
  } finally {
    transition = null;
    emit();
  }
  return true;
}

async function handleQuitWithStop(forceConfirm: boolean): Promise<void> {
  quitFlow = "handling";
  const impact = assessDaemonImpact(world, signerBusy());
  if (forceConfirm || hasAnyImpact(impact)) {
    const t = i18n.getFixedT(null, "main");
    const tr = (k: string, v?: Record<string, unknown>) => String(t(k as never, v as never));
    const active = hasActiveWork(impact);
    const { response } = await dialog.showMessageBox(
      dialogOptions(
        active ? t("daemon.quitActiveTitle") : t("daemon.quitTitle"),
        impactLines(impact, tr).join("\n"),
        [t("daemon.quitAndStop"), t("daemon.quitKeepBackground"), t("daemon.cancel")],
        active ? 2 : 0,
        2,
        active,
      ),
    );
    if (response === 2) {
      quitFlow = "none";
      return;
    }
    if (response === 1) {
      quitFlow = "proceed-keep";
      app.quit();
      return;
    }
  }
  userStopped = true;
  transition = "stopping";
  setGatewayPaused(true);
  setWatchPaused(true);
  emit();
  try {
    await platform.stopDaemon();
  } finally {
    transition = null;
    emit();
  }
  quitFlow = "proceed-stop";
  app.quit();
}

/** Quits the UI while keeping hermeshd running in the background. */
export function quitKeepingDaemon() {
  quitFlow = "proceed-keep";
  app.quit();
}

/** Quits the app and explicitly stops the background service (even if keepDaemonOnQuit is enabled). */
export function quitStoppingDaemon() {
  if (!daemonControlAvailable() || daemonState() === "stopped" || world.status === null) {
    quitFlow = "proceed-stop";
    app.quit();
    return;
  }
  if (quitFlow === "handling") return;
  void handleQuitWithStop(true);
}

/** Relaunches the app (e.g. after changing language) without stopping hermeshd. */
export function quitForRelaunch() {
  quitFlow = "proceed-keep";
  app.relaunch();
  app.quit();
}

/** Marks an upcoming quitAndInstall so before-quit leaves hermeshd running across the update. */
export function prepareUpdateQuit() {
  quitFlow = "proceed-keep";
}
