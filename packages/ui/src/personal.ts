// The personal agents the picker offers, the popular ones first. logo is the
// id of its logo in agent-logo-images.ts, the one registry the coding agents'
// logos are in too: the same app is drawn the same everywhere (ChatGPT is the
// app Codex runs in, so it is Codex's icon; Claude is Claude Code's).
// domains are where the app's hosted MCP connector returns to: a connection to
// mcp.hermesh.io is named "Name (host)": the name is the app's own claim, the host
// where it signs in (Grok Bot signs in the way Cursor does).
export type PersonalAgentKind = { id: string; name: string; color: string; logo?: string; domains?: string[] };

export const personalAgents: PersonalAgentKind[] = [
  { id: "grok", name: "Grok Bot", color: "#1c1c1f", logo: "grokbot", domains: ["cursor.com", "anysphere.cursor-mcp"] },
  { id: "muse", name: "Muse", color: "#0a5cd6", logo: "muse", domains: ["meta.ai"] },
  { id: "chatgpt", name: "ChatGPT", color: "#10a37f", logo: "codex", domains: ["chatgpt.com"] },
  { id: "claude", name: "Claude", color: "#d97757", logo: "claude", domains: ["claude.ai"] },
  { id: "openclaw", name: "OpenClaw", color: "#e0413b", logo: "openclaw" },
  { id: "other", name: "其他 Agent", color: "var(--accent)" },
];

// Apps that connect over MCP without being offered in the picker, drawn and
// named the same way: Grok Bot signs in as Cursor.
const hostedApps: PersonalAgentKind[] = [{ id: "cursor", name: "Cursor", color: "#1c1c1f", logo: "cursor", domains: ["cursor.com"] }];

/**
 * A personal agent kind by the name its grant was given (not 其他). A hosted
 * connection's "Name (host)" is the kind only from its app's own domain: any
 * app can call itself Muse.
 */
export function kindNamed(label: string): PersonalAgentKind | undefined {
  const hosted = /^(.+) \(([^()\s]+)\)$/.exec(label);
  const name = hosted ? hosted[1] : label;
  const host = hosted?.[2] ?? "";
  return [...personalAgents, ...hostedApps].find(
    (k) => k.name === name && k.id !== "other" && (!hosted || !!k.domains?.some((d) => host === d || host.endsWith("." + d))),
  );
}

/**
 * The name a grant is shown by: a hosted app's own name ("Claude"), without
 * where it signs in, once that is its app's domain; else its label whole.
 */
export function agentName(label: string): string {
  const hosted = /^(.+) \(([^()\s]+)\)$/.exec(label);
  return hosted && kindNamed(label) ? hosted[1] : label;
}

/** The colour a grant's avatar is drawn in: its kind's, else one derived from its name. */
export function grantColor(label: string): string {
  const k = kindNamed(label);
  if (k) return k.color;
  let h = 0;
  for (const ch of label) h = (h * 31 + (ch.codePointAt(0) ?? 0)) & 0xffff;
  const hue = h % 360;
  return `linear-gradient(135deg,hsl(${hue} 62% 58%),hsl(${(hue + 40) % 360} 60% 46%))`;
}
