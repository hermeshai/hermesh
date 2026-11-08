import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { LocalService } from "@hermesh/proto/local";
import { app, shell } from "electron";
import { i18n } from "../shared/i18n";
import { consoleURL, opensOutside } from "../shared/herConsole";
import { signInChatGpt, type ChatGptSignIn } from "../shared/herSignin";
import { socketPath } from "./paths";

// What the main process asks hermeshd for Her itself, never for the page: a
// console's address (its window is the main process's), a file of an
// assistant's saved where the person looks for downloads, and a ChatGPT
// sign-in, whose redirect comes back to this computer.

/** hermeshd's LocalService, where it is now (a pipe's name changes each time it starts on Windows). */
function localService() {
  return createClient(LocalService, createConnectTransport({ baseUrl: "http://hermeshd", httpVersion: "1.1", useBinaryFormat: true, nodeOptions: { socketPath: socketPath() } }));
}

/** A machine's console, served by this computer's hermeshd on 127.0.0.1 with a token in its path. */
export async function herConsoleURL(machineId: string): Promise<string> {
  return (await localService().getHerConsole({ machineId })).url;
}

/** Saves a file from an assistant's folder on its machine to Downloads (a number added when its name is taken), and shows it there. */
export async function saveHerFile(machineId: string, botId: string, path: string): Promise<void> {
  const r = await localService().herDownload({ machineId, botId, path, localDir: app.getPath("downloads") });
  shell.showItemInFolder(r.localPath);
}

/** A request to her on a machine, on a connection of its own (HerCall): its result, or her's error thrown. */
async function herCall<T>(machineId: string, method: string, params: unknown): Promise<T> {
  const r = await localService().herCall({ machineId, call: { connectionId: "", request: { method, params: new TextEncoder().encode(JSON.stringify(params)) } } });
  const res = r.response;
  if (res?.error) throw new Error(res.error.message);
  return (res?.result.length ? JSON.parse(new TextDecoder().decode(res.result)) : {}) as T;
}

/** The ChatGPT sign-in under way for each machine: what stops it, and the page it opened. */
const signIns = new Map<string, { stop: AbortController; page?: string }>();

/** Only an https page goes to the browser. */
async function openPage(url: string): Promise<void> {
  if (!opensOutside(url)) throw new Error(`not an https page: ${url}`);
  await shell.openExternal(url);
}

/** Signs in to ChatGPT for a machine's Her (shared/herSignin.ts); started again, it replaces the one under way. */
export async function signInHerChatGpt(machineId: string): Promise<ChatGptSignIn> {
  signIns.get(machineId)?.stop.abort();
  const s: { stop: AbortController; page?: string } = { stop: new AbortController() };
  signIns.set(machineId, s);
  try {
    const base = consoleURL(await herConsoleURL(machineId));
    if (!base) throw new Error(i18n.t("main:herConsoleBad"));
    return await signInChatGpt({
      base: base.href,
      fetch,
      call: (method, params) => herCall(machineId, method, params),
      open: (url) => ((s.page = url), openPage(url)),
      signal: s.stop.signal,
    });
  } finally {
    if (signIns.get(machineId) === s) signIns.delete(machineId);
  }
}

/** Opens the browser again at the sign-in under way for a machine. */
export function reopenHerChatGpt(machineId: string) {
  const page = signIns.get(machineId)?.page;
  if (page) openPage(page).catch(() => {});
}

/** Stops the sign-in under way for a machine. */
export function cancelHerChatGpt(machineId: string) {
  signIns.get(machineId)?.stop.abort();
}
