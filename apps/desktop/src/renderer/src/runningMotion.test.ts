import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { pointOnRoundedSquare } from "./orbitPath.ts";

describe("pointOnRoundedSquare", () => {
  it("stays on the concentric rounded-square perimeter across wrap-around", () => {
    const cx = 10;
    const cy = 10;
    const w = 16.99;
    const r = 4.575;

    // Top-center at u = 0 and u = 1
    const p0 = pointOnRoundedSquare(cx, cy, w, r, 0);
    const p1 = pointOnRoundedSquare(cx, cy, w, r, 1);
    assert.ok(Math.abs(p0.x - cx) < 1e-6);
    assert.ok(Math.abs(p0.y - (cy - w / 2)) < 1e-6);
    assert.ok(Math.hypot(p0.x - p1.x, p0.y - p1.y) < 1e-6);

    // Bottom-center at u = 0.5
    const pHalf = pointOnRoundedSquare(cx, cy, w, r, 0.5);
    assert.ok(Math.abs(pHalf.x - cx) < 1e-6);
    assert.ok(Math.abs(pHalf.y - (cy + w / 2)) < 1e-6);

    // Smoothness & negative u wrap-around continuity
    let prev = pointOnRoundedSquare(cx, cy, w, r, -0.36);
    for (let i = -35; i <= 64; i++) {
      const cur = pointOnRoundedSquare(cx, cy, w, r, i / 100);
      const step = Math.hypot(cur.x - prev.x, cur.y - prev.y);
      assert.ok(step > 0.45 && step < 0.65, `expected uniform perimeter step, got ${step}`);
      prev = cur;
    }
  });
});
