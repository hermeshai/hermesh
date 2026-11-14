// What the Browser page makes of her's answers, without words: the page words it.

/** What a site may be: opened and acted on, only opened, asked about whatever "All sites" allows, or never. */
export const siteLevels = ["act", "navigate", "ask", "deny"] as const;
export type SiteLevel = (typeof siteLevels)[number];

/** What "All sites" may be: asked about (no `"*"` at all), only opened, or opened and acted on; never refused as a whole. */
export const allSitesLevels = ["ask", "navigate", "act"] as const;
export type AllSitesLevel = (typeof allSitesLevels)[number];

/** A site as `browser.sites` keys it: an origin is quoted, as it has dots and a colon. */
export const siteKey = (origin: string) => `browser.sites.${JSON.stringify(origin)}`;

/** `[browser] sites` of config/read: "All sites" first, then by origin. */
export function sitesOf(config: unknown): [string, string][] {
  const browser = ((config as Record<string, unknown> | null)?.browser ?? {}) as Record<string, unknown>;
  const sites = Object.entries((browser.sites ?? {}) as Record<string, unknown>).filter((e): e is [string, string] => typeof e[1] === "string");
  return sites.sort(([a], [b]) => (a === "*" ? -1 : b === "*" ? 1 : a.localeCompare(b)));
}

/** "All sites": `"*"` in `[browser] sites`, else asked about. */
export function allSitesOf(config: unknown): AllSitesLevel {
  const level = sitesOf(config).find(([origin]) => origin === "*")?.[1];
  return level === "navigate" || level === "act" ? level : "ask";
}

/** The sites set apart from "All sites", by origin. */
export function exceptionsOf(config: unknown): [string, string][] {
  return sitesOf(config).filter(([origin]) => origin !== "*");
}

/** "All sites" set to `level`: asking is no `"*"`. */
export function allSitesEdits(level: AllSitesLevel) {
  return [{ key_path: siteKey("*"), value: level === "ask" ? null : level }];
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
