// The files Her sent, in a list of their own (docs/her-deliverables.md §3.1,
// §4.3): what both apps read of deliveries/list. her answers the pages; here
// they are joined, grouped by time or by what they came from, marked new, and
// turned into what to read and the quote a reply carries. The iPhone's
// HermeshHer (Deliveries.swift) does the same, and both run the cases in
// testdata/deliveries.json.

import type { DeliveredFile, DeliveriesListResult, Delivery, DeliverySource } from "@hermesh/her-protocol";
import type { ReplyQuote } from "./steps.ts";

/** How the list is grouped. */
export type DeliveryGroupBy = "time" | "source";

/** When a file was last sent, as the list groups it. */
export type DeliveryDay = "today" | "yesterday" | "week" | "earlier";

/**
 * A group of the list: by time, its day; by source, `conversation` for what the owner asked for, else
 * `routine:<id>` or `session:<id>`, with that source as its latest file names it.
 */
export type DeliveryGroup = { key: string; source?: DeliverySource; files: DeliveredFile[] };

const DAY = 86_400_000;

/** The day of `sentAt` from the day of `now`, both at `offsetMinutes` east of UTC. */
export function deliveryDay(sentAt: string, now: number, offsetMinutes: number): DeliveryDay {
  const day = (ms: number) => Math.floor((ms + offsetMinutes * 60_000) / DAY);
  const ago = day(now) - day(Date.parse(sentAt));
  return ago <= 0 ? "today" : ago === 1 ? "yesterday" : ago < 7 ? "week" : "earlier";
}

/** The files in groups, each group where its first file is (the files come newest first). */
export function groupDeliveries(files: DeliveredFile[], by: DeliveryGroupBy, now: number, offsetMinutes: number): DeliveryGroup[] {
  const groups: DeliveryGroup[] = [];
  for (const f of files) {
    const v = f.versions[0];
    const source = v?.source;
    const key = by === "time" ? deliveryDay(v?.sent_at ?? "", now, offsetMinutes) : source ? `${source.kind}:${source.id}` : "conversation";
    let g = groups.find((g) => g.key === key);
    if (!g) groups.push((g = { key, ...(by === "source" && source ? { source } : {}), files: [] }));
    g.files.push(f);
  }
  return groups;
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
