import assert from "node:assert/strict";
import { test } from "node:test";
import { classify } from "./logo.ts";

const SIDE = 64;

/** A sample painted by `paint(x, y)`, which gives [r, g, b, a] or null for transparent. */
function sample(paint: (x: number, y: number) => number[] | null): Uint8ClampedArray {
  const out = new Uint8ClampedArray(SIDE * SIDE * 4);
  for (let y = 0; y < SIDE; y++)
    for (let x = 0; x < SIDE; x++) {
      const px = paint(x + 0.5, y + 0.5);
      if (px) out.set(px, (y * SIDE + x) * 4);
    }
  return out;
}

/** Inside a rounded square from `m` to SIDE - m with corner radius `r`. */
const rounded = (m: number, r: number) => (x: number, y: number) => {
  if (x < m || y < m || x > SIDE - m || y > SIDE - m) return false;
  const cx = Math.min(Math.max(x, m + r), SIDE - m - r),
    cy = Math.min(Math.max(y, m + r), SIDE - m - r);
  return (x - cx) ** 2 + (y - cy) ** 2 <= r * r;
};

test("an opaque square image is a tile filling the shape", () => {
  const look = classify(sample(() => [20, 20, 30, 255]), SIDE)!;
  assert.deepEqual(look.tile, { x: 0, y: 0, w: 1, h: 1 });
  assert.equal(look.cut, false);
  assert.equal(look.tone, "dark");
});

test("an icon with its own corners inside a transparent margin is a tile, cropped", () => {
  const inside = rounded(6, 12);
  const look = classify(sample((x, y) => (inside(x, y) ? [240, 80, 40, 255] : null)), SIDE)!;
  assert.ok(look.tile);
  assert.ok(Math.abs(look.tile.x - 6 / SIDE) < 0.03 && Math.abs(look.tile.w - 52 / SIDE) < 0.04);
  assert.equal(look.cut, true);
  assert.equal(look.tone, "mid");
});

test("a circle, a wordmark and a thin mark are glyphs", () => {
  const circle = sample((x, y) => ((x - 32) ** 2 + (y - 32) ** 2 <= 31 ** 2 ? [255, 255, 255, 255] : null));
  assert.deepEqual(classify(circle, SIDE), { tile: null, cut: false, tone: "light" });
  const word = sample((_, y) => (y > 24 && y < 40 ? [0, 0, 0, 255] : null));
  assert.equal(classify(word, SIDE)!.tile, null);
  const cross = sample((x, y) => (Math.abs(x - 32) < 4 || Math.abs(y - 32) < 4 ? [0, 0, 0, 255] : null));
  assert.equal(classify(cross, SIDE)!.tile, null);
});

test("a dark mark is dark, a white one light, a coloured one reads on both", () => {
  const mark = (rgb: number[]) => classify(sample((x, y) => ((x - 32) ** 2 + (y - 32) ** 2 < 300 ? [...rgb, 255] : null)), SIDE)!.tone;
  assert.equal(mark([0, 0, 0]), "dark");
  assert.equal(mark([20, 30, 90]), "dark");
  assert.equal(mark([255, 255, 255]), "light");
  assert.equal(mark([230, 30, 30]), "mid");
});

test("an empty image is nothing", () => {
  assert.equal(classify(new Uint8ClampedArray(SIDE * SIDE * 4), SIDE), null);
});
