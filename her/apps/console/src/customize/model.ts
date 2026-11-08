import type { McpServerView, SkillEntry, SkillsListResult } from "@hermesh/her-protocol";

// What the Connectors and Skills pages show, made from her's answers (skills/list, mcp/list):
// the groups of skills, and each server's state as one line.
// The console manages her's user layer, so nothing here is a project's. No text here:
// the pages word it.

/** Where a skill lives: the person's own, a folder in skills.paths, or built into her. */
export type SkillGroup = "user" | "extra" | "system";

export type SkillItem = {
  entry: SkillEntry;
  /** The skill's folder, on the Her Server. */
  dir: string;
  group: SkillGroup;
  on: boolean;
  /** The other app's folder it lives in (".claude"), read with `skills.other_apps`. */
  from: string | null;
};

export function skillDir(path: string): string {
  return path.replace(/[/\\]SKILL\.md$/i, "");
}

/** `.claude` for `…/.claude/skills/x/SKILL.md`. */
function clientFolder(path: string): string | null {
  const m = /[/\\](\.[^/\\]+)[/\\]skills[/\\]/.exec(path);
  return m ? m[1] : null;
}

/** Every skill an assistant here would find, those turned off included; broken ones are listed apart. */
export function skillItems(list: SkillsListResult | null): SkillItem[] {
  if (!list) return [];
  return list.skills
    .filter((e) => e.state !== "invalid" && e.state !== "shadowed" && e.scope !== "project")
    .map((entry) => ({
      entry,
      dir: skillDir(entry.path),
      group: entry.scope as SkillGroup,
      on: entry.state !== "disabled",
      from: entry.scope === "user" && !entry.removable ? clientFolder(entry.path) : null,
    }));
}

/** Skills that did not load, with why. */
export function brokenSkills(list: SkillsListResult | null): SkillEntry[] {
  return list?.skills.filter((e) => e.state === "invalid") ?? [];
}

/** The copy of the same name that takes over when `item` is turned off. */
export function nextInLine(list: SkillsListResult | null, item: SkillItem): SkillEntry | null {
  return list?.skills.find((e) => e.state === "shadowed" && e.name === item.entry.name && e.detail === item.entry.path) ?? null;
}

/** The two letters on a server's tile. */
export function monogram(name: string): string {
  const parts = name.split(/[-_\s]+/).filter(Boolean);
  const letters = parts.length > 1 ? parts[0][0] + parts[1][0] : name.slice(0, 2);
  return letters.toUpperCase();
}

/** A server started as a command on the Her Server: only the owner's Mac adds, changes or removes one. */
export function isCommand(s: McpServerView): boolean {
  return s.transport === "stdio";
}

/** A server's state as a row shows it (§8.1): off, being checked, then her's. */
export type StatusLook =
  | { kind: "off" }
  | { kind: "checking" }
  | { kind: "ok"; tools: number; sessions: number | null; checkedAt: string | null }
  | { kind: "failed"; error: string | null }
  | { kind: "needs_login" }
  | { kind: "unknown" };

export function statusOf(s: McpServerView, checking: boolean): StatusLook {
  if (s.connector && s.auth_state !== "logged_in") return { kind: "needs_login" };
  if (!s.enabled) return { kind: "off" };
  if (checking) return { kind: "checking" };
  switch (s.status.state) {
    case "ok":
      return { kind: "ok", tools: s.status.tools ?? 0, sessions: s.status.source === "session" ? (s.status.sessions ?? null) : null, checkedAt: s.status.checked_at ?? null };
    case "failed":
      return { kind: "failed", error: s.status.error ?? null };
    case "needs_login":
      return { kind: "needs_login" };
    default:
      return { kind: "unknown" };
  }
}

/** A command line as the form shows it, quoting arguments with spaces. */
export function joinCommand(command: string, args: string[]): string {
  return [command, ...args].map((a) => (/[\s"']/.test(a) ? JSON.stringify(a) : a)).join(" ");
}

/** The words of a command line: spaces separate them, quotes keep them together. */
export function splitCommand(line: string): string[] {
  const out: string[] = [];
  let word = "";
  let quote: string | null = null;
  let started = false;
  for (let i = 0; i < line.length; i++) {
    const c = line[i];
    if (quote) {
      if (c === quote) quote = null;
      else if (c === "\\" && quote === '"' && i + 1 < line.length) word += line[++i];
      else word += c;
    } else if (c === '"' || c === "'") {
      quote = c;
      started = true;
    } else if (/\s/.test(c)) {
      if (started) out.push(word);
      word = "";
      started = false;
    } else {
      word += c === "\\" && i + 1 < line.length ? line[++i] : c;
      started = true;
    }
  }
  if (started) out.push(word);
  return out;
}

/** A server name her accepts. */
export const serverName = /^[A-Za-z0-9_-]{1,32}$/;

/** The credential a locked value of a server is kept under. */
export function credentialName(server: string, key: string): string {
  return `mcp.${server}.${key.replace(/[^A-Za-z0-9._-]/g, "_")}`;
}

/** The credential a value refers to: `Bearer ${credential:name}` → name. */
export function credentialRef(value: string): string | null {
  const m = /\$\{credential:([^}]+)\}/.exec(value);
  return m ? m[1] : null;
}

/** The value written for a secret now kept as `name`: the old value's words around its reference stay ("Bearer "). */
export function withCredential(old: string | null, name: string): string {
  const ref = `\${credential:${name}}`;
  return old && credentialRef(old) ? old.replace(/\$\{credential:[^}]+\}/, ref) : ref;
}

/** A name as one segment of a key path. */
export function quoteKey(name: string): string {
  return /^[A-Za-z0-9_-]+$/.test(name) ? name : JSON.stringify(name);
}
