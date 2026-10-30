import { language } from "@hermesh/core";
import { app } from "electron";
import { spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs";
import { dataDir } from "./paths";
import { platform } from "./platform";

let busy = false;
let signer: ChildProcess | undefined;
export const signerListeners = new Set<() => void>();
export const signerBusy = () => busy;
function setBusy(value: boolean) {
  if (busy === value) return;
  busy = value;
  signerListeners.forEach((f) => f());
}
/** Bring the existing approval forward without accepting or rejecting it. */
export function showSigner() { signer?.kill("SIGUSR1"); }

// The signer holds the root key and shows what is signed. It talks only
// to hermeshd; we just keep it running, where it is the app's child (macOS).
export function keepSignerRunning(socket: string): () => void {
  const program = platform.signer(language());
  if (!program || !fs.existsSync(program.bin)) {
    console.warn(`signer not found${program ? ` at ${program.bin}` : ""}; signing requests will wait`);
    return () => {};
  }
  // Test runs pass extra flags (a software key, a scripted decision).
  const extra = !app.isPackaged && process.env.HERMESH_SIGNER_ARGS ? process.env.HERMESH_SIGNER_ARGS.split(" ").filter(Boolean) : [];
  let child: ChildProcess | undefined;
  let stopped = false;
  let delay = 1000;
  const start = () => {
    const began = Date.now();
    // The signer keeps its files in the same data directory as the app, and speaks its language.
    setBusy(true); // Until the signer reports its initial state.
    child = spawn(program.bin, ["--socket", socket, "--parent-pid", String(process.pid), ...program.args, ...extra], {
      stdio: ["ignore", "pipe", "inherit"],
      env: { ...process.env, HERMESH_DATA_DIR: dataDir() },
    });
    signer = child;
    let output = "";
    child.stdout?.on("data", (data: Buffer) => {
      output += data.toString();
      const lines = output.split("\n");
      output = lines.pop() ?? "";
      for (const line of lines) {
        if (line === "HERMESH_SIGNER_BUSY=1") setBusy(true);
        else if (line === "HERMESH_SIGNER_BUSY=0") setBusy(false);
        else console.log(line);
      }
    });
    child.on("exit", (code, signal) => {
      signer = undefined;
      setBusy(!stopped);
      if (stopped) return;
      if (Date.now() - began > 60_000) delay = 1000;
      console.warn(`signer exited (${code ?? signal}); restarting in ${delay} ms`);
      setTimeout(start, delay);
      delay = Math.min(delay * 2, 30_000);
    });
  };
  start();
  return () => {
    stopped = true;
    child?.kill();
  };
}
