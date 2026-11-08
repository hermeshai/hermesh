import type { ModelSummary, ModelsListResult, PermissionMode, ThinkingLevel } from "@hermesh/her-protocol";

// What Overview, Defaults and Browser make of her's answers, without words: the pages word it.

export type StartsWith = { model: string | null; thinking: ThinkingLevel; permission_mode: PermissionMode };

/**
 * What a new assistant starts with: what her's file says (config/read), else her's own choice.
 * models/list's defaults hold the file as her read it when it started, so a change since then
 * is only in the file.
 */
export function startsWith(list: ModelsListResult | null, config: Record<string, unknown> | null): StartsWith {
  const c = config ?? {};
  const said = <T>(key: string) => (typeof c[key] === "string" && c[key] ? (c[key] as T) : undefined);
  return {
    model: said<string>("model") ?? list?.defaults?.model ?? null,
    thinking: said<ThinkingLevel>("thinking") ?? list?.defaults?.thinking ?? "default",
    permission_mode: said<PermissionMode>("permission_mode") ?? list?.defaults?.permission_mode ?? "manual",
  };
}

/** The model `id`, when it can run: else null, and no model is ready. */
export function readyModel(list: ModelsListResult | null, id: string | null): ModelSummary | null {
  return list?.models.find((m) => m.id === id && m.available) ?? null;
}

/** The models a new assistant can start with: those that can run and are in the model menu, and `current`. */
export function modelChoices(list: ModelsListResult | null, current: string | null): ModelSummary[] {
  return (list?.models ?? []).filter((m) => m.available && (m.shown || m.id === current));
}

/** The thinking levels a new assistant may start with on `model`: the provider's own choice, then the model's levels. */
export function thinkingChoices(model: ModelSummary | undefined): ThinkingLevel[] {
  return ["default", ...(model?.thinking_levels ?? []).filter((l) => l !== "default")];
}

/** The approval modes the console sets. Full access is for one assistant at a time, signed in the app with Touch ID (docs/her-server.md §8). */
export const approvals: PermissionMode[] = ["manual", "accept-edits"];

/** What a site may be: opened and acted on, only opened, or never. "All sites" is never refused as a whole. */
export const siteLevels = ["act", "navigate", "deny"] as const;
export type SiteLevel = (typeof siteLevels)[number];

/** A site as `browser.sites` keys it: an origin is quoted, as it has dots and a colon. */
export const siteKey = (origin: string) => `browser.sites.${JSON.stringify(origin)}`;

/** `[browser] sites` of config/read: "All sites" first, then by origin. */
export function sitesOf(config: unknown): [string, string][] {
  const browser = ((config as Record<string, unknown> | null)?.browser ?? {}) as Record<string, unknown>;
  const sites = Object.entries((browser.sites ?? {}) as Record<string, unknown>).filter((e): e is [string, string] => typeof e[1] === "string");
  return sites.sort(([a], [b]) => (a === "*" ? -1 : b === "*" ? 1 : a.localeCompare(b)));
}

/** A site as the person types it, as her keeps it: the origin of an http(s) address, a bare host being https. Null when it is none. */
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
