import { createClient, ConnectError } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { appLoginURL, centerBase, connectURLFrom, discoveryURL, parseAppLink, pkceChallenge, rootPlatform } from "@hermesh/core";
import { LocalService } from "@hermesh/proto/local";
import { app, net, shell } from "electron";
import { randomBytes } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { i18n } from "../shared/i18n";
import { socketPath } from "./paths";
import { platform } from "./platform";
import { mainWindow, showMain } from "./windows";

// The browser sign-in (docs/desktop-app.md §5.3). The app keeps a random
// verifier and gives the browser only its SHA-256; the link code that comes
// back by hermesh://link is useless without the verifier, so another app
// that registered the scheme and caught the link cannot use it. The
// verifier stays here in the main process: the page never sees it.

type Pending = { center: string; verifier: string; startedAt: number; code?: string };

// Link codes last 15 minutes; so does a sign-in kept on disk, so the link
// still works when the browser has to start the app again.
const lifetime = 15 * 60_000;
let pending: Pending | undefined;

const file = () => path.join(app.getPath("userData"), "sign-in.json");

function save(p: Pending) {
  fs.mkdirSync(path.dirname(file()), { recursive: true, mode: 0o700 });
  fs.writeFileSync(file(), JSON.stringify(p), { mode: 0o600 });
  fs.chmodSync(file(), 0o600);
}

/** The sign-in under way, also one a previous run of the app started. */
function current(): Pending | undefined {
  if (!pending) {
    try {
      pending = JSON.parse(fs.readFileSync(file(), "utf8")) as Pending;
    } catch {
      return undefined;
    }
  }
  if (Date.now() - pending.startedAt > lifetime) {
    cancelSignIn();
    return undefined;
  }
  return pending;
}

/** Starts the browser sign-in at center, or opens the browser again for the one under way. */
export async function signIn(center: string): Promise<void> {
  let p = current();
  if (!p || p.center !== centerBase(center) || p.code) {
    p = { center: centerBase(center), verifier: randomBytes(32).toString("base64url"), startedAt: Date.now() };
    pending = p;
    save(p);
  }
  await shell.openExternal(appLoginURL(p.center, await pkceChallenge(p.verifier)));
}

export function signInPending(): boolean {
  return current() !== undefined;
}

export function cancelSignIn() {
  pending = undefined;
  fs.rmSync(file(), { force: true });
}

// hermesh:// links for the page, kept until it takes them (it may not be
// loaded yet when the browser starts the app with one).
const links: string[] = [];

/** A hermesh:// link the app was opened with: a link code (only while signing in) or an owner to join. */
export function receiveLink(url: string) {
  const l = parseAppLink(url);
  // Any other hermesh: link (hermesh://open) brings the app forward, as opening it would.
  if (!l) return void (app.isReady() && showMain());
  if (l.kind === "link") {
    const p = current();
    if (!p) return; // not ours, or not now
    p.code = l.code;
    save(p);
  }
  links.push(url);
  if (!app.isReady()) return; // the window opens once ready, and takes it
  const win = mainWindow();
  showMain();
  win?.webContents.send("link");
}

export function takeLinks(): string[] {
  return links.splice(0);
}

/** Where center's devices connect (its .well-known/hermesh.json; a center without one is its own). */
export async function connectURL(center: string): Promise<string> {
  const res = await net.fetch(discoveryURL(center), { signal: AbortSignal.timeout(10_000), cache: "no-store" });
  return connectURLFrom(center, res.status, await res.text());
}

/**
 * Makes this computer the owner's first trusted device with the link code
 * the browser sent back: hermeshd asks the signer for the new root key and
 * the one signature (Touch ID), then claims the account's owner with the
 * code and the verifier. localMachine: agents may work here too.
 */
export async function completeSignIn(o: { localMachine: boolean }): Promise<void> {
  const p = current();
  if (!p?.code) throw new Error(i18n.t("main:signInExpired"));
  const name = await platform.computerName();
  await platform.readySigner();
  const transport = createConnectTransport({ baseUrl: "http://hermeshd", httpVersion: "1.1", useBinaryFormat: true, nodeOptions: { socketPath: socketPath() } });
  await createClient(LocalService, transport).setup({
    relayUrl: await connectURL(p.center),
    claimCode: p.code,
    claimVerifier: p.verifier,
    rootLabel: name,
    rootPlatform: rootPlatform(process.platform),
    localMachineName: o.localMachine ? name : "",
  });
  cancelSignIn();
}

/** An error's message for the person (a daemon error without its code). */
export function errorMessage(e: unknown): string {
  if (e instanceof ConnectError) return e.rawMessage;
  return e instanceof Error ? e.message : String(e);
}
