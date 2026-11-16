// The files Her sent, in a list of their own (docs/her-deliverables.md §3.1,
// §4.3): what both apps read of deliveries/list. her answers the pages; here
// they are joined, grouped by when each was last sent, marked new, and
// turned into what to read and the quote a reply carries. The iPhone's
// HermeshHer (Deliveries.swift) does the same, and both run the cases in
// testdata/deliveries.json.

import type { DeliveredFile, DeliveriesListResult, Delivery, DeliveryKind, DeliverySourceCount, DeliverySourceFilter } from "@hermesh/her-protocol";
import type { ReplyQuote } from "./steps.ts";

/** When a file was last sent, as the list groups it. */
export type DeliveryDay = "today" | "yesterday" | "week" | "earlier";

/** A group of the list: the day its files were last sent. */
export type DeliveryGroup = { key: DeliveryDay; files: DeliveredFile[] };

/** Pictures, video and audio: what the list shows apart from documents (deliveries/list's `media`). */
export const isMediaKind = (kind: DeliveryKind) => kind === "image" || kind === "video" || kind === "audio";

const DAY = 86_400_000;

/** The day of `sentAt` from the day of `now`, both at `offsetMinutes` east of UTC. */
export function deliveryDay(sentAt: string, now: number, offsetMinutes: number): DeliveryDay {
  const day = (ms: number) => Math.floor((ms + offsetMinutes * 60_000) / DAY);
  const ago = day(now) - day(Date.parse(sentAt));
  return ago <= 0 ? "today" : ago === 1 ? "yesterday" : ago < 7 ? "week" : "earlier";
}

/** The files by the day each was last sent, each group where its first file is (the files come newest first). */
export function groupDeliveries(files: DeliveredFile[], now: number, offsetMinutes: number): DeliveryGroup[] {
  const groups: DeliveryGroup[] = [];
  for (const f of files) {
    const key = deliveryDay(f.versions[0]?.sent_at ?? "", now, offsetMinutes);
    let g = groups.find((g) => g.key === key);
    if (!g) groups.push((g = { key, files: [] }));
    g.files.push(f);
  }
  return groups;
}

/** Only media (true) or only documents (false), or all (undefined): what a tab shows, whatever her answered (one older than the `media` filter answers all of them). */
export function onlyDeliveries(page: DeliveriesListResult, media: boolean | undefined): DeliveriesListResult {
  return media === undefined ? page : { ...page, files: page.files.filter((f) => isMediaKind(f.kind) === media) };
}

/** Sent since the owner last looked at the list. */
export function isNewDelivery(f: DeliveredFile, seen: number): boolean {
  return (f.versions[0]?.seq ?? 0) > seen;
}

/** What to read of a version: its copy, as it was sent, and where the file is, for when there is none (readSent). */
export function deliveryFile(f: DeliveredFile, v: Delivery): { path: string; snapshot?: string } {
  return v.snapshot ? { path: f.path, snapshot: v.snapshot } : { path: f.path };
}

/** The quote a reply to a version carries: its message, quoted in part as the file's name and Her's words with it. */
export function deliveryQuote(f: DeliveredFile, v: Delivery): ReplyQuote {
  const words = v.text?.trim();
  return { ref: v.call_id, own: false, text: words ? `${f.name}\n${words}` : f.name, partial: true };
}

/** Pages joined, in order: a file on two pages (sent again between them) stays where it was first. */
export function joinDeliveries(pages: DeliveriesListResult[]): DeliveriesListResult {
  const files: DeliveredFile[] = [];
  const have = new Set<string>();
  for (const p of pages) {
    for (const f of p.files) {
      if (have.has(f.path)) continue;
      have.add(f.path);
      files.push(f);
    }
  }
  const last = pages.at(-1);
  return { files, seen: Math.max(0, ...pages.map((p) => p.seen)), ...(last?.next !== undefined ? { next: last.next } : {}) };
}

/** Two filters of where files came from are the same one. */
export function sameSource(a: DeliverySourceFilter | null | undefined, b: DeliverySourceFilter | null | undefined): boolean {
  if (!a || !b) return !a && !b;
  if (a.kind === "routine" && b.kind === "routine") return a.id === b.id;
  if (a.kind === "session" && b.kind === "session") return a.name === b.name;
  return a.kind === b.kind;
}

/** The file came from that source: its latest send did (deliveries/list's `source`, for the page kept offline). */
export function fromSource(f: DeliveredFile, filter: DeliverySourceFilter): boolean {
  const s = f.versions[0]?.source;
  if (filter.kind === "conversation") return !s;
  if (filter.kind === "routine") return s?.kind === "routine" && s.id === filter.id;
  return s?.kind === "session" && s.name === filter.name;
}

/** How many of a source's files a tab shows: all, only media, or only documents. */
export function sourceCount(s: DeliverySourceCount, media: boolean | undefined): number {
  return media === undefined ? s.files : media ? s.media : s.files - s.media;
}

/** The sources as the filter lists them, each kind apart and the latest first; a search keeps those with it in their name. */
export function sourceGroups(sources: DeliverySourceCount[], query: string): { conversation?: DeliverySourceCount; routines: DeliverySourceCount[]; sessions: DeliverySourceCount[] } {
  const q = query.trim().toLowerCase();
  const kept = sources.filter((s) => (q ? (s.name ?? "").toLowerCase().includes(q) : true));
  const conversation = kept.find((s) => s.filter.kind === "conversation");
  return {
    ...(conversation ? { conversation } : {}),
    routines: kept.filter((s) => s.filter.kind === "routine"),
    sessions: kept.filter((s) => s.filter.kind === "session"),
  };
}
