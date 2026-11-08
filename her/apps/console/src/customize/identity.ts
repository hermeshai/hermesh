// Which service a connector reaches, whatever it is called (mcp-design §13.10). The kernel
// computes the same value (her/crates/her-mcp/src/identity.rs) and sends it with each server;
// the page computes it for an address being added, to say at once that it is already there.

/** Package runners whose "yes, install it" flags do not change what runs. */
const RUNNERS = ["npx", "bunx", "pnpx", "uvx"];

/** A remote server: scheme and host in lower case, no default port, no trailing slash, no fragment. */
export function remoteIdentity(url: string): string {
  const text = url.trim();
  let u: URL;
  try {
    u = new URL(text);
  } catch {
    return text;
  }
  return `${u.protocol}//${u.hostname}${u.port ? `:${u.port}` : ""}${u.pathname.replace(/\/+$/, "")}${u.search}`;
}

/** A local server: its command line, without the runners' -y/--yes. */
export function localIdentity(command: string, args: string[]): string {
  const program = command.split("/").pop() ?? command;
  const runner = RUNNERS.includes(program);
  return [command.trim(), ...args.filter((a) => !(runner && (a === "-y" || a === "--yes")))].join(" ");
}
