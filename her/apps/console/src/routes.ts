// The console's routes beside her's JSON-RPC, served next to the console's files, so they are
// addressed relative to the page: a file goes up to the Her Server, and its data comes down
// (hermeshd on the machine); a sign-in's redirect is caught on the owner's device, and a
// connector started as a command is signed there (hermeshd on the device, docs/her-server.md
// §5.4, §8).

/** Why a route said no: the text it answered, or the `error` of its JSON. */
async function refusal(r: Response): Promise<Error> {
  const text = (await r.text()).trim();
  try {
    const said = JSON.parse(text) as { error?: unknown };
    if (typeof said.error === "string") return new Error(said.error);
  } catch {
    // Plain text.
  }
  return new Error(text || `HTTP ${r.status}`);
}

async function json<T>(r: Response): Promise<T> {
  if (!r.ok) throw await refusal(r);
  return (await r.json()) as T;
}

/**
 * A file onto the Her Server; the answer is where it landed there. The name is
 * percent-encoded, as a header carries only ASCII.
 */
export async function upload(file: Blob, name: string): Promise<string> {
  const r = await fetch("./upload", {
    method: "POST",
    headers: { "Her-File-Name": encodeURIComponent(name), "Content-Type": "application/octet-stream" },
    body: file,
  });
  return (await json<{ path: string }>(r)).path;
}

/**
 * An archive of the Her Server's data, which hermeshd there has her write, sends, then removes:
 * its bytes, and the name the file came with.
 */
export async function exportData(): Promise<{ blob: Blob; name: string }> {
  const r = await fetch("./export");
  if (!r.ok) throw await refusal(r);
  const name = /filename="?([^";]+)"?/.exec(r.headers.get("Content-Disposition") ?? "")?.[1] ?? "her-export.zip";
  return { blob: await r.blob(), name };
}

/** A catcher waiting on the owner's device, and the address a sign-in sends the browser back to. */
export type Catcher = { id: string; redirect_uri: string };

/** What the browser brought back to the catcher. */
export type Caught = { code: string; state: string; client_id?: string; iss?: string } | { error: string; error_description?: string };

/** Starts a catcher for a sign-in's redirect: `/auth/callback` for ChatGPT, `/callback` for a connector. */
export async function catchRedirect(path: string): Promise<Catcher> {
  const r = await fetch("./signin", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path }) });
  return json<Catcher>(r);
}

const pause = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => (clearTimeout(timer), reject(signal.reason)), { once: true });
  });

/**
 * What the catcher caught, once it has: each GET waits a while and says when it is still
 * pending, so this asks again; a request that fails on the way is asked again too. `signal`
 * stops waiting.
 */
export async function caught(id: string, signal?: AbortSignal): Promise<Caught> {
  for (;;) {
    let r: Response;
    try {
      r = await fetch(`./signin/${encodeURIComponent(id)}`, { signal });
    } catch (e) {
      if (signal?.aborted) throw e;
      await pause(1000, signal);
      continue;
    }
    const body = await json<Caught | { pending: true }>(r);
    if (!("pending" in body && body.pending)) return body as Caught;
  }
}

/** A connector started as a command, as the owner's device signs it. */
export type CommandConnector = { name: string; command: string; args: string[]; env: Record<string, string> };

/**
 * Asks the owner's device to sign a connector started as a command, with its root key (Touch
 * ID on a Mac), then to apply it: answered once it is applied, or refused with why.
 */
export async function signCommandConnector(c: CommandConnector): Promise<void> {
  const r = await fetch("./command-connector", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(c) });
  if (!r.ok) throw await refusal(r);
}
