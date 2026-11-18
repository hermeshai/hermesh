import type { BrowserStatus, ConfigEdit, ProviderSummary, SkillEntry, SkillsListResult, StoragePart, WebSearchStatus } from "@hermesh/her-protocol";

// What Her's panel makes of the answers her environment's console also reads (docs/her-panel-demo.html,
// docs/her-server.md §10), without words: the pages word it. Her skills by group, the writes web search's switches
// and order make, the browsers as they stand, the Models page's providers, and her data by size. The console's own
// helpers (her/apps/console) read the same answers the same way.

// ---- skills ----

/** Where a skill lives: the owner's own, a folder in skills.paths, or built into her. */
export type SkillGroup = "user" | "extra" | "system";
/** Where her installed it from, as its page says. */
export type SkillSource = "system" | "catalog" | "upload" | "local" | "extra";

export type SkillItem = {
  entry: SkillEntry;
  /** Its folder on the Her Server: what skills/set_enabled and skills/remove take. */
  dir: string;
  group: SkillGroup;
  on: boolean;
  source: SkillSource;
};

export function skillDir(path: string): string {
  return path.replace(/[/\\]SKILL\.md$/i, "");
}

/** The skills a conversation here would find, those turned off included: not a project's own, not one shadowed, not one broken. */
export function skillItems(list: SkillsListResult | null): SkillItem[] {
  if (!list) return [];
  return list.skills
    .filter((e) => e.state !== "invalid" && e.state !== "shadowed" && e.scope !== "project")
    .map((entry) => {
      const group: SkillGroup = entry.scope === "system" ? "system" : entry.scope === "extra" ? "extra" : "user";
      const source: SkillSource = group !== "user" ? group : entry.source?.kind === "catalog" ? "catalog" : entry.source?.kind === "upload" ? "upload" : "local";
      return { entry, dir: skillDir(entry.path), group, on: entry.state !== "disabled", source };
    });
}

/** Skills that did not load, with why. */
export function brokenSkills(list: SkillsListResult | null): SkillEntry[] {
  return list?.skills.filter((e) => e.state === "invalid" && e.scope !== "project") ?? [];
}

// ---- web search ----

/** The writes that turn one service on or off: `web_search.disabled` lists those off. */
export function searchSwitchEdits(status: WebSearchStatus, id: string, on: boolean): ConfigEdit[] {
  return [{ key_path: "web_search.disabled", value: status.sources.filter((s) => (s.id === id ? !on : !s.enabled)).map((s) => s.id) }];
}

/** The services in their new order, one moved from `from` to `to` (both indexes in the list as it is). */
export function searchMoved(status: WebSearchStatus, from: number, to: number): string[] {
  const ids = status.sources.map((s) => s.id);
  const [moved] = ids.splice(from, 1);
  ids.splice(Math.max(0, Math.min(to, ids.length)), 0, moved);
  return ids;
}

export const searchOrderEdits = (order: string[]): ConfigEdit[] => [{ key_path: "web_search.order", value: order }];
export const searchEnabledEdits = (on: boolean): ConfigEdit[] => [{ key_path: "web_search.enabled", value: on }];

// ---- browsers ----

/** How a site may be used: opened and acted on, only opened, asked about, or never. */
export const siteLevels = ["act", "navigate", "ask", "deny"] as const;
export type SiteLevel = (typeof siteLevels)[number];

/** The owner's Chrome: not connected and not used; connected, not used yet; used; used but not connected now; too old on one side. */
export type Yours = "off" | "ready" | "on" | "lost" | "outdated";

export type Browsers = {
  /** Her controls browsers: as set, else where a desktop app shows them or the owner's Chrome is used. */
  controlling: boolean;
  yours: Yours;
  /** New pages open in the owner's Chrome: only while Her uses it and it is connected. */
  opensInYours: boolean;
  /** The connected Chrome, as it named itself. */
  chrome: string | null;
  /** The browser the built-in browser starts, when there is one. */
  program: string | null;
  window: boolean;
  os: "macos" | "linux";
  /** "All sites": ask, navigate or act. */
  allSites: Exclude<SiteLevel, "deny">;
  /** The sites set apart from "All sites", by origin. */
  exceptions: [string, SiteLevel][];
  /** `backend = "extension"`: the old way, rewritten on the next write. */
  legacyBackend: boolean;
};

export function browsers(status: BrowserStatus, config: unknown): Browsers {
  const b = ((config as Record<string, unknown> | null)?.browser ?? {}) as Record<string, unknown>;
  const legacyBackend = b.backend === "extension";
  const used = typeof b.extension === "boolean" ? b.extension : legacyBackend;
  const problem = !!status.extension?.problem;
  const connected = !problem && !!status.extension;
  const yours: Yours = problem ? "outdated" : connected ? (used ? "on" : "ready") : used ? "lost" : "off";
  const sites = Object.entries((b.sites ?? {}) as Record<string, unknown>).filter((e): e is [string, SiteLevel] => siteLevels.includes(e[1] as SiteLevel));
  const all = sites.find(([o]) => o === "*")?.[1];
  return {
    controlling: typeof b.enabled === "boolean" ? b.enabled : status.embedded || used,
    yours,
    opensInYours: b.default === "yours" && yours === "on",
    chrome: connected ? (status.extension?.browser ?? null) : null,
    program: status.program ?? null,
    window: status.window,
    os: status.os === "linux" ? "linux" : "macos",
    allSites: all === "navigate" || all === "act" ? all : "ask",
    exceptions: sites.filter(([o]) => o !== "*").sort(([a], [b]) => a.localeCompare(b)),
    legacyBackend,
  };
}

/** A site as `browser.sites` keys it: an origin is quoted, as it has dots and a colon. */
export const siteKey = (origin: string) => `browser.sites.${JSON.stringify(origin)}`;
export const controlEdits = (on: boolean): ConfigEdit[] => [{ key_path: "browser.enabled", value: on }];
export const defaultEdits = (yours: boolean): ConfigEdit[] => [{ key_path: "browser.default", value: yours ? "yours" : "builtin" }];
/** "All sites" set: asking is no `"*"` at all. */
export const allSitesEdits = (level: SiteLevel): ConfigEdit[] => [{ key_path: siteKey("*"), value: level === "ask" ? null : level }];
/** A site set apart, or (null) taken out. */
export const siteEdits = (origin: string, level: SiteLevel | null): ConfigEdit[] => [{ key_path: siteKey(origin), value: level }];
export const extensionEdits = (on: boolean, legacyBackend: boolean): ConfigEdit[] => [
  { key_path: "browser.extension", value: on },
  ...(legacyBackend ? [{ key_path: "browser.backend", value: null }] : []),
];

/** A site as the owner types it, as her keeps it: the origin of an http(s) address, a bare host being https. Null when it is none. */
export function siteOrigin(text: string): string | null {
  const t = text.trim();
  if (!t) return null;
  let u: URL;
  try {
    u = new URL(/^[a-z][a-z0-9+.-]*:\/\//i.test(t) ? t : `https://${t}`);
  } catch {
    return null;
  }
  if ((u.protocol !== "https:" && u.protocol !== "http:") || !u.hostname || u.username || u.password) return null;
  return u.origin;
}

/** "Google Chrome" of "Google Chrome 141.0.7390.54". */
export const chromeName = (s: string) => s.replace(/\s+[\d.]+$/, "");

// ---- models ----

/** The order the Token Plans are offered in; one not named here comes after them, by name. */
const planOrder = ["bailian-token-plan", "tencent-token-plan", "volcengine-agent-plan", "volcengine-coding-plan", "zai-coding-plan", "zhipu-coding-plan"];
const planRank = (p: ProviderSummary) => (planOrder.includes(p.id) ? planOrder.indexOf(p.id) : planOrder.length);

export type Providers = {
  /** Token Plans with a key, and those without one to connect. */
  addedPlans: ProviderSummary[];
  plansLeft: ProviderSummary[];
  /** Providers with a key from somewhere, and every service added by its address (a local one takes no key); the built-in keyless ones need nothing. */
  keyed: ProviderSummary[];
};

export function providers(list: ProviderSummary[]): Providers {
  const plans = list.filter((p) => p.plan).sort((a, b) => planRank(a) - planRank(b) || a.name.localeCompare(b.name));
  return {
    addedPlans: plans.filter((p) => p.credential !== "missing"),
    plansLeft: plans.filter((p) => p.credential === "missing"),
    keyed: list.filter((p) => p.custom || (!p.plan && p.auth === "api_key" && p.credential !== "missing")),
  };
}

// ---- storage ----

/** Her data's kinds, the largest first: a bar's segments and the list under it. */
export function bySize(parts: StoragePart[]): StoragePart[] {
  return [...parts].sort((a, b) => b.bytes - a.bytes);
}
