import { app } from "electron";
import os from "node:os";
import path from "node:path";

// Test builds only (HERMESH_TEST_DATA_DIR when building): a data directory
// baked in, so an app that Squirrel relaunches after an update still talks
// to the test's daemon, without touching the session's environment.
declare const __HERMESH_TEST_DATA_DIR__: string;
export const testDataDir = __HERMESH_TEST_DATA_DIR__;

// Mirrors defaultDataDir and SocketPath in cmd/hermeshd.
export function dataDir(): string {
  if (testDataDir) return testDataDir;
  if (process.env.HERMESH_DATA_DIR) return process.env.HERMESH_DATA_DIR;
  const home = os.homedir();
  if (process.platform === "darwin") return path.join(home, "Library", "Application Support", "Hermesh");
  if (process.env.XDG_DATA_HOME) return path.join(process.env.XDG_DATA_HOME, "hermesh");
  return path.join(home, ".local", "share", "hermesh");
}

export function socketPath(): string {
  if (process.env.HERMESH_SOCKET && !app.isPackaged) return process.env.HERMESH_SOCKET;
  const p = path.join(dataDir(), "run", "hermeshd.sock");
  if (Buffer.byteLength(p) > 100) return path.join(os.tmpdir(), `hermesh-${process.getuid?.() ?? 0}`, "hermeshd.sock");
  return p;
}
