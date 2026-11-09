import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { LocalService } from "@hermesh/proto/local";
import { app, shell } from "electron";
import { i18n } from "../shared/i18n";
import { consoleURL, opensOutside } from "../shared/herConsole";
import { connectorPage, signInChatGpt, signInConnector, type ChatGptSignIn, type ConnectorSignIn, type SignInDeps } from "../shared/herSignin";
import { socketPath } from "./paths";

// What the main process asks hermeshd for Her itself, never for the page: a
// console's address (its window is the main process's), a file of an
// assistant's saved where the person looks for downloads, and a sign-in to
// ChatGPT or to a connector, whose redirect comes back to this computer.

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

/** The sign-ins under way, ChatGPT's by machine and connectors' by machine and name: what stops each, and the page it opened. */
const signIns = new Map<string, { stop: AbortController; page?: string }>();

const connectorKey = (machineId: string, name: string) => `${machineId}\0${name}`;

/** Runs a sign-in for a machine's Her, opening only pages `opens` lets through; started again, it replaces the one under way. */
async function signInFor<T>(key: string, machineId: string, opens: (url: string) => boolean, run: (d: SignInDeps) => Promise<T>): Promise<T> {
  signIns.get(key)?.stop.abort();
  const s: { stop: AbortController; page?: string } = { stop: new AbortController() };
  signIns.set(key, s);
  try {
    const base = consoleURL(await herConsoleURL(machineId));
    if (!base) throw new Error(i18n.t("main:herConsoleBad"));
    return await run({
      base: base.href,
      fetch,
      call: (method, params) => herCall(machineId, method, params),
      open: async (url) => {
        if (!opens(url)) throw new Error(`not a page to open: ${url}`);
        s.page = url;
        await shell.openExternal(url);
      },
      signal: s.stop.signal,
    });
  } finally {
    if (signIns.get(key) === s) signIns.delete(key);
  }
}

/** Opens the browser again at a sign-in under way. */
function reopen(key: string) {
  const page = signIns.get(key)?.page;
  if (page) shell.openExternal(page).catch(() => {});
}

// Development builds only: her's ChatGPT fixture, a loopback address, stands in for OpenAI (as her takes
// HER_TEST_CHATGPT_BASE only as a debug build).
const testChatGpt = !app.isPackaged ? process.env.HERMESH_TEST_CHATGPT_BASE : undefined;

/** Signs in to ChatGPT for a machine's Her (shared/herSignin.ts): only an https page goes to the browser. */
export function signInHerChatGpt(machineId: string): Promise<ChatGptSignIn> {
  const opens = (url: string) => opensOutside(url) || (!!testChatGpt && url.startsWith(`${testChatGpt}/`));
  return signInFor(machineId, machineId, opens, signInChatGpt);
}

/** Opens the browser again at the ChatGPT sign-in under way for a machine. */
export function reopenHerChatGpt(machineId: string) {
  reopen(machineId);
}

/** Stops the ChatGPT sign-in under way for a machine. */
export function cancelHerChatGpt(machineId: string) {
  signIns.get(machineId)?.stop.abort();
}

/** Signs in to a connector of a machine's Her, for the secure card that added it or asks to sign in again. */
export function signInHerConnector(machineId: string, name: string): Promise<ConnectorSignIn> {
  return signInFor(connectorKey(machineId, name), machineId, connectorPage, (d) => signInConnector(d, name));
}

/** Opens the browser again at a connector's sign-in under way. */
export function reopenHerConnector(machineId: string, name: string) {
  reopen(connectorKey(machineId, name));
}

/** Stops a connector's sign-in under way: her's side is cancelled with it. */
export function cancelHerConnector(machineId: string, name: string) {
  signIns.get(connectorKey(machineId, name))?.stop.abort();
}
