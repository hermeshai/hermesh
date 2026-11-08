// How a connector's logo is drawn (customize-design §10.10), decided from its pixels. A logo
// is one of two things. A tile, opaque to its edges (an app icon, a touch icon), is the
// connector's tile itself: it fills the shape edge to edge, with nothing added around it. A
// glyph, a mark on transparency (most favicons and SVGs), is set on a plate with room around
// it, as a mark on a page is; the plate changes only when the mark would vanish on it.

/** The part of the image that is the tile, as fractions of its sides. */
export type Crop = { x: number; y: number; w: number; h: number };

export type LogoLook = {
  /** Set when the image is a tile: the part to fill the shape with. */
  tile: Crop | null;
  /** The tile brings its own rounded corners, so no edge is drawn over them. */
  cut: boolean;
  /** "dark" vanishes on a dark plate, "light" on a light one, "mid" reads on both. */
  tone: "dark" | "light" | "mid";
};

const OPAQUE = 240;

/** Reads a square RGBA sample of the image, `side` pixels a side. Null: nothing visible. */
export function classify(rgba: Uint8ClampedArray, side: number): LogoLook | null {
  const a = (x: number, y: number) => rgba[(Math.round(y) * side + Math.round(x)) * 4 + 3];
  // The opaque part's bounds: an icon drawn inside a transparent margin is still a tile.
  let x0 = side, y0 = side, x1 = -1, y1 = -1;
  let sum = 0, weight = 0;
  for (let y = 0; y < side; y++)
    for (let x = 0; x < side; x++) {
      const i = (y * side + x) * 4;
      const alpha = rgba[i + 3];
      if (alpha >= 200) {
        x0 = Math.min(x0, x), y0 = Math.min(y0, y), x1 = Math.max(x1, x), y1 = Math.max(y1, y);
      }
      if (alpha > 128) {
        sum += luminance(rgba[i], rgba[i + 1], rgba[i + 2]) * alpha;
        weight += alpha;
      }
    }
  if (!weight) return null;
  const mean = sum / weight;
  const tone = mean < 0.11 ? "dark" : mean > 0.5 ? "light" : "mid";
  if (x1 < 0) return { tile: null, cut: false, tone };
  const w = x1 - x0 + 1, h = y1 - y0 + 1;
  // A tile is square and opaque at the middle of each edge and a little in from each corner,
  // which a rounded square is and a circle or a wordmark is not.
  const inset = Math.max(1, Math.round(w * 0.12));
  const probes = [
    [(x0 + x1) / 2, y0 + 1], [(x0 + x1) / 2, y1 - 1], [x0 + 1, (y0 + y1) / 2], [x1 - 1, (y0 + y1) / 2],
    [x0 + inset, y0 + inset], [x1 - inset, y0 + inset], [x0 + inset, y1 - inset], [x1 - inset, y1 - inset],
  ];
  const square = Math.abs(w - h) <= Math.max(1, side * 0.03) && w >= side * 0.5;
  if (!square || !probes.every(([x, y]) => a(x, y) >= OPAQUE)) return { tile: null, cut: false, tone };
  const cut = [a(x0, y0), a(x1, y0), a(x0, y1), a(x1, y1)].some((v) => v < 128);
  return { tile: { x: x0 / side, y: y0 / side, w: w / side, h: h / side }, cut, tone };
}

/** Relative luminance of an sRGB colour, 0 to 1. */
function luminance(r: number, g: number, b: number): number {
  const lin = (c: number) => ((c /= 255) <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}
