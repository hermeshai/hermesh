import { app } from "electron";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

// Test builds only (HERMESH_TEST_DATA_DIR when building): a data directory
// baked in, so an app that Squirrel relaunches after an update still talks
// to the test's daemon, without touching the session's environment.
declare const __HERMESH_TEST_DATA_DIR__: string;
export const testDataDir = __HERMESH_TEST_DATA_DIR__;

// Mirrors DataDir and LocalAPI in internal/paths.
export function dataDir(): string {
  if (testDataDir) return testDataDir;
  if (process.env.HERMESH_DATA_DIR) return process.env.HERMESH_DATA_DIR;
  const home = os.homedir();
  if (process.platform === "darwin") return path.join(home, "Library", "Application Support", "Hermesh");
  if (process.platform === "win32") return path.join(process.env.LOCALAPPDATA || path.join(home, "AppData", "Local"), "Hermesh");
  if (process.env.XDG_DATA_HOME) return path.join(process.env.XDG_DATA_HOME, "hermesh");
  return path.join(home, ".local", "share", "hermesh");
}

export function socketPath(): string {
  if (process.platform === "win32") return pipeName(process.env.HERMESH_SOCKET && !app.isPackaged ? process.env.HERMESH_SOCKET : path.join(dataDir(), "run", "hermeshd.pipe"));
  if (process.env.HERMESH_SOCKET && !app.isPackaged) return process.env.HERMESH_SOCKET;
  const p = path.join(dataDir(), "run", "hermeshd.sock");
  if (Buffer.byteLength(p) > 100) return path.join(os.tmpdir(), `hermesh-${process.getuid?.() ?? 0}`, "hermeshd.sock");
  return p;
}

/** Local-user-only diagnostic endpoint path, mirroring paths.DesktopPerf in Go. */
export function perfEndpointPath(): string {
  if (!app.isPackaged && process.env.HERMESH_PERF_SOCKET) return process.env.HERMESH_PERF_SOCKET;
  if (!app.isPackaged && process.env.HERMESH_USER_DATA && !process.env.HERMESH_DATA_DIR) {
    const ext = process.platform === "win32" ? "pipe" : "sock";
    const p = path.join(process.env.HERMESH_USER_DATA, `desktop-perf.${ext}`);
    if (process.platform !== "win32" && Buffer.byteLength(p) > 100) {
      const h = crypto.createHash("sha256").update(process.env.HERMESH_USER_DATA).digest("hex").slice(0, 16);
      return path.join(os.tmpdir(), `hermesh-perf-${process.getuid?.() ?? 0}-${h}.sock`);
    }
    return p;
  }
  if (process.platform === "win32") return path.join(dataDir(), "run", "desktop-perf.pipe");
  const p = path.join(dataDir(), "run", "desktop-perf.sock");
  if (Buffer.byteLength(p) > 100) return path.join(os.tmpdir(), `hermesh-${process.getuid?.() ?? 0}`, "desktop-perf.sock");
  return p;
}

/**
 * On Windows hermeshd serves a pipe with a random name, written in a file
 * only this user can read (internal/localipc): read again for each
 * connection, since it changes whenever hermeshd starts. Before it has
 * started there is no name, and connecting to the file's path fails as
 * a missing socket does.
 */
function pipeName(file: string): string {
  if (file.startsWith("\\\\.\\pipe\\")) return file;
  try {
    return fs.readFileSync(file, "utf8").trim() || file;
  } catch {
    return file;
  }
}
