import { test } from "node:test";
import assert from "node:assert/strict";
import { KindMeans, parts, positions, rowsIn, segments, windowOf, type Part } from "./virtual.ts";

test("positions: each row takes its height and the gap after it", () => {
  assert.deepEqual([...positions([10, 20, 30], 5)], [0, 15, 40, 75]);
  assert.deepEqual([...positions([], 8)], [0]);
});

test("rowsIn: the rows a stretch overlaps, the gaps between them belonging to none", () => {
  // Rows 0..2 at [0,10) [15,35) [40,70), gaps of 5.
  const P = positions([10, 20, 30], 5);
  assert.deepEqual(rowsIn(P, 5, 0, 10), [0, 1]);
  assert.deepEqual(rowsIn(P, 5, 0, 16), [0, 2]);
  // Starting in the gap after row 0: row 0 is above it.
  assert.deepEqual(rowsIn(P, 5, 11, 20), [1, 2]);
  // Ending exactly where a row starts leaves that row out.
  assert.deepEqual(rowsIn(P, 5, 20, 40), [1, 2]);
  assert.deepEqual(rowsIn(P, 5, 20, 41), [1, 3]);
  // Above the list, past its end, and the whole of it.
  assert.deepEqual(rowsIn(P, 5, -100, -1), [0, 0]);
  assert.deepEqual(rowsIn(P, 5, 200, 300), [3, 3]);
  assert.deepEqual(rowsIn(P, 5, -50, 500), [0, 3]);
  // Nothing to overlap.
  assert.deepEqual(rowsIn(positions([], 5), 5, 0, 100), [0, 0]);
  assert.deepEqual(rowsIn(P, 5, 30, 30), [0, 0]);
});

test("windowOf: whole steps, never empty while there are rows", () => {
  const P = positions(Array(100).fill(10), 0);
  assert.deepEqual(windowOf(P, 0, 205, 305, 8), [16, 32]);
  assert.deepEqual(windowOf(P, 0, -500, 15, 8), [0, 8]);
  assert.deepEqual(windowOf(P, 0, 995, 2000, 8), [96, 100]);
  // Out past the end, or above the start: the nearest step of rows.
  assert.deepEqual(windowOf(P, 0, 5000, 6000, 8), [96, 100]);
  assert.deepEqual(windowOf(P, 0, -900, -800, 8), [0, 8]);
  assert.deepEqual(windowOf(positions([], 0), 0, 0, 100, 8), [0, 0]);
  assert.deepEqual(windowOf(positions([10, 10], 0), 0, 0, 5, 8), [0, 2]);
});

test("segments: overlapping and touching ranges join, empty and outside ones go", () => {
  assert.deepEqual(segments([[10, 20], [15, 30], [30, 32], [40, 41]], 100), [[10, 32], [40, 41]]);
  assert.deepEqual(segments([[50, 60], [5, 6]], 100), [[5, 6], [50, 60]]);
  assert.deepEqual(segments([[-5, 3], [98, 120], [7, 7], [9, 8]], 100), [[0, 3], [98, 100]]);
  assert.deepEqual(segments([], 10), []);
});

test("parts: spaces stand for the rows not drawn, their own gap left out", () => {
  const P = positions([10, 20, 30, 40, 50], 5);
  assert.deepEqual(parts(P, 5, [[1, 3]]), [{ space: 10, at: 0 }, { from: 1, to: 3 }, { space: 40 + 5 + 50, at: 3 }]);
  assert.deepEqual(parts(P, 5, [[0, 5]]), [{ from: 0, to: 5 }]);
  assert.deepEqual(parts(P, 5, [[0, 1], [4, 5]]), [{ from: 0, to: 1 }, { space: 20 + 5 + 30 + 5 + 40, at: 1 }, { from: 4, to: 5 }]);
  assert.deepEqual(parts(P, 5, []), [{ space: P[5] - 5, at: 0 }]);
});

test("parts: drawn or stood in for, the list is as tall as with every row drawn", () => {
  // A seeded generator, so a failure repeats.
  let seed = 7;
  const random = () => ((seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648);
  for (let round = 0; round < 200; round++) {
    const n = 1 + Math.floor(random() * 60);
    const gap = Math.floor(random() * 12);
    const heights = Array.from({ length: n }, () => Math.floor(random() * 300));
    const P = positions(heights, gap);
    const ranges = Array.from({ length: Math.floor(random() * 4) }, () => {
      const a = Math.floor(random() * n);
      return [a, a + 1 + Math.floor(random() * 10)] as [number, number];
    });
    const drawn = parts(P, gap, segments(ranges, n));
    // Flex items in a column: each item's height, and a gap between neighbours.
    const items = drawn.flatMap((p: Part) => ("space" in p ? [p.space] : heights.slice(p.from, p.to)));
    const tall = items.reduce((a, h) => a + h, 0) + gap * (items.length - 1);
    const all = heights.reduce((a, h) => a + h, 0) + gap * (n - 1);
    assert.equal(tall, all, `round ${round}`);
    // Every row is drawn or stood in for, once, in order: a space reaches to the next rows drawn.
    let at = 0;
    drawn.forEach((p, i) => {
      if ("space" in p) {
        assert.equal(p.at, at);
        const next = drawn[i + 1];
        at = next && "from" in next ? next.from : n;
        assert.ok(at > p.at);
      } else {
        assert.equal(p.from, at);
        at = p.to;
      }
    });
    assert.equal(at, n);
  }
});

test("KindMeans: an average by kind, a row measured again counting once", () => {
  const m = new KindMeans();
  assert.equal(m.of("text", 160), 160);
  m.set("a", "text", 100);
  m.set("b", "text", 300);
  m.set("c", "user", 50);
  assert.equal(m.of("text", 0), 200);
  m.set("a", "text", 500);
  assert.equal(m.of("text", 0), 400);
  // A key can change kind (a group of calls becomes a reply): it leaves the old kind's average.
  m.set("c", "text", 400);
  assert.equal(m.of("user", 64), 64);
  assert.equal(m.of("text", 0), 400);
  assert.deepEqual([...m.snapshot()], [["text", 400]]);
});
