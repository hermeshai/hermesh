// A Her console's address (docs/her-server.md §5.3): this computer's hermeshd
// serves it on 127.0.0.1, under a token in its path (GetHerConsole). Its
// window opens nothing else: other pages of that origin only, https links in
// the browser.

/** The console's address when it is one the app opens: http on 127.0.0.1 with a port, no credentials; else null. */
export function consoleURL(raw: string): URL | null {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return null;
  }
  if (u.protocol !== "http:" || u.hostname !== "127.0.0.1" || !/^\d+$/.test(u.port) || u.username !== "" || u.password !== "") return null;
  return u;
}

/** Where the console's window may go on its own: the console's origin. */
export function inConsole(console: URL, to: string): boolean {
  try {
    return new URL(to).origin === console.origin;
  } catch {
    return false;
  }
}

/** What the console's window hands to the browser: https links. */
export function opensOutside(to: string): boolean {
  try {
    return new URL(to).protocol === "https:";
  } catch {
    return false;
  }
}
