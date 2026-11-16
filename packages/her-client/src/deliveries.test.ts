import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import type { DeliveriesListResult, DeliveryKind } from "@hermesh/her-protocol";
import { deliveryDay, deliveryFile, deliveryQuote, groupDeliveries, isMediaKind, isNewDelivery, joinDeliveries } from "./deliveries.ts";

// The cases HermeshHer runs too, so both apps read the list the same way.
type Case = {
  now: string;
  offset_minutes: number;
  page: DeliveriesListResult;
  by_time: { key: string; files: string[] }[];
  media: Record<string, boolean>;
  new: string[];
  read: { file: string; version: number; path: string; snapshot?: string }[];
  quotes: { file: string; version: number; quote: { ref: string; own: boolean; text: string; partial: boolean } }[];
  next_page: DeliveriesListResult;
  joined: string[];
};
const shared = JSON.parse(readFileSync(new URL("../testdata/deliveries.json", import.meta.url), "utf8")) as Case;
const now = Date.parse(shared.now);
const file = (name: string) => shared.page.files.find((f) => f.name === name)!;

test("the list groups by the day each file was last sent, in local time", () => {
  const groups = groupDeliveries(shared.page.files, now, shared.offset_minutes);
  assert.deepEqual(groups.map((g) => ({ key: g.key, files: g.files.map((f) => f.name) })), shared.by_time);
  // At UTC the day ends elsewhere: a file sent at 16:05Z on the 15th is yesterday there, today at UTC+8.
  assert.equal(deliveryDay("2026-11-15T16:05:00Z", now, 0), "yesterday");
  assert.equal(deliveryDay("2026-11-15T16:05:00Z", now, 480), "today");
});

test("pictures, video and audio are media; the rest are documents", () => {
  const kinds: Record<string, DeliveryKind> = { mov: "video", m4a: "audio", png: "image", pdf: "doc", go: "code", zip: "other" };
  for (const [name, media] of Object.entries(shared.media)) assert.equal(isMediaKind(kinds[name.split(".").pop()!]), media, name);
});

test("a file is new while its latest send is after what the owner saw", () => {
  assert.deepEqual(shared.page.files.filter((f) => isNewDelivery(f, shared.page.seen)).map((f) => f.name), shared.new);
});

test("a version is read from its copy, and from where the file is without one", () => {
  for (const c of shared.read) {
    const f = file(c.file);
    assert.deepEqual(deliveryFile(f, f.versions[c.version]), c.snapshot ? { path: c.path, snapshot: c.snapshot } : { path: c.path });
  }
});

test("a reply to a file quotes its message by its name and Her's words", () => {
  for (const c of shared.quotes) {
    const f = file(c.file);
    assert.deepEqual(deliveryQuote(f, f.versions[c.version]), c.quote);
  }
});

test("pages join in order, a file once", () => {
  const joined = joinDeliveries([shared.page, shared.next_page]);
  assert.deepEqual(joined.files.map((f) => f.name), shared.joined);
  assert.equal(joined.next, undefined, "the last page has none");
  assert.equal(joinDeliveries([shared.page]).next, 2);
});
