// The app's updates (docs/desktop-app.md §6.5): the state the main process
// keeps (main/updater.ts) and the pages show, and the feed the release
// publishes (scripts/build-app.mjs). No imports, so the build script and
// `node --test` load this file as it is.

/** Where the app is with its next version. */
export type Update =
  /** Development builds, and platforms without updates yet. */
  | { state: "unsupported" }
  | { state: "idle" }
  | { state: "checking" }
  /** Up to date. */
  | { state: "current" }
  /** A newer version, not downloaded: automatic download is off. */
  | { state: "available"; version: string }
  | { state: "downloading"; version: string }
  /** Downloaded and verified: installed on quit, or now with Restart to Update. */
  | { state: "ready"; version: string }
  /** What went wrong, in words for the owner, and Squirrel's or the network's own message under Details. */
  | { state: "error"; reason: UpdateFailure; message: string };

/**
 * Why an update failed: the feed could not be read (offline, or the server
 * failed); the app runs from a read-only place (a disk image, or App
 * Translocation), where Squirrel cannot replace it; or Squirrel failed to
 * download or verify the release.
 */
export type UpdateFailure = "network" | "location" | "download";

export type UpdateInfo = {
  /** The running app's version. */
  version: string;
  /** Download updates automatically. */
  autoDownload: boolean;
  /** Last successful feed check (ms since the epoch), or null. */
  checkedAt: number | null;
  update: Update;
  /** Last attempt, including a failed one. */
  attemptedAt: number | null;
  snoozedVersion: string;
  snoozedUntil: number;
  approvalPending: boolean;
  notes: ReleaseNote[];
};

export type UpdateEvent =
  | { type: "check" }
  /** The feed was read: its latest version ("" when there is none for this architecture). */
  | { type: "feed"; latest: string; current: string; autoDownload: boolean }
  /** A download started (asked for, or by Squirrel after its own look at the feed). */
  | { type: "download" }
  | { type: "downloaded"; version: string }
  | { type: "not-available" }
  | { type: "error"; reason: UpdateFailure; message: string };

/** The next state. A downloaded update stays ready whatever happens next. */
export function nextUpdate(u: Update, e: UpdateEvent): Update {
  if (u.state === "ready" || u.state === "unsupported") return u;
  switch (e.type) {
    case "check":
      return u.state === "downloading" ? u : { state: "checking" };
    case "feed":
      if (!e.latest || !newer(e.latest, e.current)) return { state: "current" };
      return { state: e.autoDownload ? "downloading" : "available", version: e.latest };
    case "download":
      return { state: "downloading", version: "version" in u ? u.version : "" };
    case "downloaded":
      return { state: "ready", version: e.version || ("version" in u ? u.version : "") };
    case "not-available":
      return { state: "current" };
    case "error":
      return { state: "error", reason: e.reason, message: e.message };
  }
}

/**
 * Is version a newer than b? Dot-separated parts compare as numbers
 * ("0.10.0" > "0.9.1"); other parts ("1.0.0-beta.2") as text. Squirrel.Mac
 * agrees: it downloads a feed's release only when the running app is older.
 */
export function newer(a: string, b: string): boolean {
  const [coreA, ...preA] = a.split("-");
  const [coreB, ...preB] = b.split("-");
  const c = compareParts(coreA.split("."), coreB.split("."), "0");
  if (c !== 0) return c > 0;
  // The same version: a release ("1.0.0") is newer than its pre-releases ("1.0.0-beta.1").
  if (!preA.length || !preB.length) return !preA.length && preB.length > 0;
  return compareParts(preA.join("-").split("."), preB.join("-").split("."), "") > 0;
}

function compareParts(a: string[], b: string[], missing: string): number {
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const x = a[i] ?? missing;
    const y = b[i] ?? missing;
    if (x === y) continue;
    const nx = Number(x);
    const ny = Number(y);
    if (x !== "" && y !== "" && Number.isInteger(nx) && Number.isInteger(ny)) return nx - ny;
    return x > y ? 1 : -1;
  }
  return 0;
}

/**
 * Why Squirrel.Mac failed, from its error. It refuses to update an app on a
 * read-only volume with this message (always English: the framework has no
 * translations); anything else happened while downloading or verifying.
 */
export function squirrelFailure(message: string): UpdateFailure {
  return /read-only volume/i.test(message) ? "location" : "download";
}

/** The release's page, with its notes. */
export function releaseNotesURL(version: string): string {
  return `https://github.com/hermeshai/hermesh/releases/tag/v${version}`;
}

/**
 * Squirrel.Mac's JSON feed (serverType "json") for one release of one
 * architecture: the app downloads `url` when currentRelease is newer than
 * itself.
 */
export function releaseFeed(r: { version: string; url: string; date: Date; notes?: ReleaseNote[] }) {
  return {
    currentRelease: r.version,
    ...(r.notes && { hermeshNotes: r.notes }),
    releases: [
      {
        version: r.version,
        updateTo: { version: r.version, name: r.version, notes: releaseNotesURL(r.version), pub_date: rfc3339(r.date), url: r.url },
      },
    ],
  };
}

/** "2026-10-19T08:00:00Z": Squirrel.Mac parses dates without fractions of a second. */
function rfc3339(d: Date): string {
  return d.toISOString().replace(/\.\d+Z$/, "Z");
}

export type ReleaseSummary = { improvements: string[]; fixes: string[] };
export type ReleaseNote = { version: string; summary: { en: ReleaseSummary; "zh-CN": ReleaseSummary } };

/** Localized history is bundled for offline reading and included in the Squirrel feed. */
export function releaseCatalog(en: Record<string, ReleaseSummary>, zh: Record<string, ReleaseSummary>, version: string): ReleaseNote[] {
  const versions = new Set(Object.keys(en).filter((v) => v !== "unreleased" && (!version || !newer(v, version))));
  if (version) versions.add(version);
  return [...versions].sort((a, b) => newer(a, b) ? -1 : newer(b, a) ? 1 : 0).map((v) => ({
    version: v,
    summary: { en: en[v] ?? en.unreleased, "zh-CN": zh[v] ?? zh.unreleased },
  }));
}

/**
 * Why version can't be released yet ("" when it can): each catalog needs
 * the version's own highlights, at least one line, within what the update
 * window takes (feedNotes).
 */
export function releaseNotesProblem(en: Record<string, ReleaseSummary>, zh: Record<string, ReleaseSummary>, version: string): string {
  for (const [lang, catalog] of [["en", en], ["zh-CN", zh]] as const) {
    const s = catalog[version];
    if (!s) return `${lang}/releases.json has no "${version}": move "unreleased" there (both languages) and write what changed for users`;
    const lines = [...s.improvements, ...s.fixes];
    if (lines.length === 0) return `${lang}/releases.json lists nothing for "${version}"`;
    if (s.improvements.length > 10 || s.fixes.length > 10 || lines.some((l) => !l.trim() || l.length > 1000)) return `${lang}/releases.json: "${version}" has more than 10 lines in a list, or an empty or over-long line`;
  }
  return "";
}

/** Which file is for what, at the top of a release's text (updates.json's install). */
type InstallGuide = { heading: string; mac: string; windows: string; machine: string; device: string; cli: string; relay: string; note: string };
type Headings = { improvements: string; fixes: string; install?: InstallGuide };

/**
 * The GitHub release's text: what to install (with the install command and
 * the relay's image), then the version's highlights, in English, then in
 * Chinese, under the update window's headings.
 */
export function releaseBody(en: Record<string, ReleaseSummary>, zh: Record<string, ReleaseSummary>, version: string, headings: { en: Headings; "zh-CN": Headings }, install?: { command: string; image: string }): string {
  const guide = (h: Headings) => {
    const g = h.install;
    if (!install || !g) return "";
    const fill = (l: string) => l.replaceAll("{{command}}", install.command).replaceAll("{{image}}", install.image);
    return `### ${g.heading}\n\n${[g.mac, g.windows, g.machine, g.device, g.cli, g.relay].map((l) => `- ${fill(l)}`).join("\n")}\n\n${g.note}\n\n`;
  };
  const part = (s: ReleaseSummary, h: Headings) =>
    guide(h) +
    (["improvements", "fixes"] as const)
      .filter((g) => s[g].length > 0)
      .map((g) => `### ${h[g]}\n\n${s[g].map((l) => `- ${l}`).join("\n")}\n`)
      .join("\n");
  return `${part(en[version], headings.en)}\n---\n\n${part(zh[version], headings["zh-CN"])}`;
}

/** Only bounded plain text crosses from the feed into the update window. */
export function feedNotes(value: unknown): ReleaseNote[] {
  if (!Array.isArray(value)) return [];
  return value.slice(0, 50).flatMap((n) => {
    if (!n || typeof n.version !== "string" || !/^\d+\.\d+\.\d+(?:-[\w.-]+)?$/.test(n.version)) return [];
    const summary = (v: unknown): ReleaseSummary | undefined => {
      if (!v || typeof v !== "object") return;
      const s = v as Record<string, unknown>;
      const list = (a: unknown) => Array.isArray(a) && a.length <= 10 && a.every((x) => typeof x === "string" && x.length <= 1000) ? a as string[] : undefined;
      const improvements = list(s.improvements), fixes = list(s.fixes);
      return improvements && fixes ? { improvements, fixes } : undefined;
    };
    const en = summary(n.summary?.en), zh = summary(n.summary?.["zh-CN"]);
    return en && zh ? [{ version: n.version, summary: { en, "zh-CN": zh } }] : [];
  });
}

export const checkEvery = 4 * 60 * 60 * 1000;

/** Success controls freshness; failed attempts control retry backoff, including across launches. */
export function nextCheckDelay(checkedAt: number | null, attemptedAt: number | null, failures: number, now: number): number {
  if (failures && attemptedAt !== null) {
    const delay = [5, 30, 120][Math.min(failures - 1, 2)] * 60_000;
    return Math.max(0, attemptedAt + delay - now);
  }
  return checkedAt === null ? 0 : Math.max(0, checkedAt + checkEvery - now);
}
