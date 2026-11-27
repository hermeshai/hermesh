export function pointOnRoundedSquare(cx: number, cy: number, w: number, radius: number, u: number): { x: number; y: number } {
  const r = Math.max(0, Math.min(radius, w / 2));
  const straight = Math.max(0, w - 2 * r);
  const arc = 0.5 * Math.PI * r;
  const perim = 4 * straight + 4 * arc;
  let d = (((u % 1) + 1) % 1) * perim;
  const half = w / 2;

  if (d <= straight / 2) return { x: cx + d, y: cy - half };
  d -= straight / 2;
  if (d <= arc) {
    const a = -Math.PI / 2 + d / r;
    return { x: cx + half - r + Math.cos(a) * r, y: cy - half + r + Math.sin(a) * r };
  }
  d -= arc;
  if (d <= straight) return { x: cx + half, y: cy - half + r + d };
  d -= straight;
  if (d <= arc) {
    const a = d / r;
    return { x: cx + half - r + Math.cos(a) * r, y: cy + half - r + Math.sin(a) * r };
  }
  d -= arc;
  if (d <= straight) return { x: cx + half - r - d, y: cy + half };
  d -= straight;
  if (d <= arc) {
    const a = Math.PI / 2 + d / r;
    return { x: cx - half + r + Math.cos(a) * r, y: cy + half - r + Math.sin(a) * r };
  }
  d -= arc;
  if (d <= straight) return { x: cx - half, y: cy + half - r - d };
  d -= straight;
  if (d <= arc) {
    const a = Math.PI + d / r;
    return { x: cx - half + r + Math.cos(a) * r, y: cy - half + r + Math.sin(a) * r };
  }
  d -= arc;
  return { x: cx - half + r + d, y: cy - half };
}
