import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { LocalService } from "@hermesh/proto/local";
import { app, shell } from "electron";
import { socketPath } from "./paths";

// What the main process asks hermeshd for Her itself, never for the page: a
// console's address (its window is the main process's), and a file of an
// assistant's saved where the person looks for downloads.

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
