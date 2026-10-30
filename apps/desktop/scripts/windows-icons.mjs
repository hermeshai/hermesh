// The Windows app's icons, drawn from the mark of the macOS icon
// (build/AppIcon.icon, whose picture holds the mark at 64%) on the icon's
// dark fill: a rounded square with a margin, as Windows 11 draws app icons. No image library:
// PNGs are read and written with zlib, and scaled down by averaging.
import fs from "node:fs";
import path from "node:path";
import zlib from "node:zlib";

const desktop = path.resolve(import.meta.dirname, "..");
const fill = [15, 15, 20]; // icon.json's fill, srgb 0.06 0.06 0.08

/** Decodes an 8-bit RGBA, non-interlaced PNG. */
export function readPNG(buf) {
  let pos = 8;
  let width = 0, height = 0;
  const idat = [];
  while (pos < buf.length) {
    const len = buf.readUInt32BE(pos);
    const type = buf.toString("latin1", pos + 4, pos + 8);
    const data = buf.subarray(pos + 8, pos + 8 + len);
    if (type === "IHDR") {
      width = data.readUInt32BE(0);
      height = data.readUInt32BE(4);
      if (data[8] !== 8 || data[9] !== 6 || data[12] !== 0) throw new Error("only 8-bit RGBA, non-interlaced PNGs");
    } else if (type === "IDAT") idat.push(data);
    pos += 12 + len;
  }
  const raw = zlib.inflateSync(Buffer.concat(idat));
  const stride = width * 4;
  const px = Buffer.alloc(stride * height);
  for (let y = 0; y < height; y++) {
    const f = raw[y * (stride + 1)];
    const row = raw.subarray(y * (stride + 1) + 1, (y + 1) * (stride + 1));
    for (let x = 0; x < stride; x++) {
      const a = x >= 4 ? px[y * stride + x - 4] : 0;
      const b = y > 0 ? px[(y - 1) * stride + x] : 0;
      const c = x >= 4 && y > 0 ? px[(y - 1) * stride + x - 4] : 0;
      const p = a + b - c;
      const pa = Math.abs(p - a), pb = Math.abs(p - b), pc = Math.abs(p - c);
      const pred = [0, a, b, (a + b) >> 1, pa <= pb && pa <= pc ? a : pb <= pc ? b : c][f];
      px[y * stride + x] = (row[x] + pred) & 255;
    }
  }
  return { width, height, px };
}

/** Encodes RGBA pixels as a PNG. */
export function writePNG({ width, height, px }) {
  const chunk = (type, data) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const body = Buffer.concat([Buffer.from(type, "latin1"), data]);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(zlib.crc32(body));
    return Buffer.concat([len, body, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr.set([8, 6, 0, 0, 0], 8);
  const raw = Buffer.alloc((width * 4 + 1) * height);
  for (let y = 0; y < height; y++) px.copy(raw, y * (width * 4 + 1) + 1, y * width * 4, (y + 1) * width * 4);
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", ihdr), chunk("IDAT", zlib.deflateSync(raw, { level: 9 })), chunk("IEND", Buffer.alloc(0))]);
}

/** The icon at 1024 px, premultiplied: the rounded square (its margin and radius as parts of the side) and the mark's picture over it. */
function master(mark, { margin = 0.06, radius = 0.22 } = {}) {
  const n = 1024;
  const out = new Float64Array(n * n * 4);
  const lo = n * margin, hi = n - lo, r = (hi - lo) * radius;
  // How much of the pixel at (x, y) the rounded square covers (one sample per 1/4 pixel).
  const inside = (x, y) => {
    const cx = Math.min(Math.max(x, lo + r), hi - r), cy = Math.min(Math.max(y, lo + r), hi - r);
    return x >= lo && x <= hi && y >= lo && y <= hi && Math.hypot(x - cx, y - cy) <= r;
  };
  const size = hi - lo, off = lo;
  for (let y = 0; y < n; y++) {
    for (let x = 0; x < n; x++) {
      let cover = 0;
      for (const [dx, dy] of [[0.25, 0.25], [0.75, 0.25], [0.25, 0.75], [0.75, 0.75]]) cover += inside(x + dx, y + dy) ? 0.25 : 0;
      if (!cover) continue;
      // The mark, sampled from its 1024 px source.
      const mx = Math.floor(((x + 0.5 - off) / size) * mark.width), my = Math.floor(((y + 0.5 - off) / size) * mark.height);
      let [r0, g0, b0, a0] = [0, 0, 0, 0];
      if (mx >= 0 && my >= 0 && mx < mark.width && my < mark.height) {
        const k = (my * mark.width + mx) * 4;
        [r0, g0, b0, a0] = [mark.px[k], mark.px[k + 1], mark.px[k + 2], mark.px[k + 3] / 255];
      }
      const k = (y * n + x) * 4;
      for (let c = 0; c < 3; c++) out[k + c] = cover * (a0 * [r0, g0, b0][c] + (1 - a0) * fill[c]);
      out[k + 3] = cover;
    }
  }
  return { n, out };
}

/** The master scaled to size × size by averaging, as RGBA. */
function scaled({ n, out }, size) {
  const px = Buffer.alloc(size * size * 4);
  const step = n / size;
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const acc = [0, 0, 0, 0];
      let count = 0;
      for (let sy = Math.floor(y * step); sy < Math.floor((y + 1) * step); sy++) {
        for (let sx = Math.floor(x * step); sx < Math.floor((x + 1) * step); sx++) {
          const k = (sy * n + sx) * 4;
          for (let c = 0; c < 4; c++) acc[c] += out[k + c];
          count++;
        }
      }
      const a = acc[3] / count;
      const k = (y * size + x) * 4;
      for (let c = 0; c < 3; c++) px[k + c] = a ? Math.round(acc[c] / count / a) : 0;
      px[k + 3] = Math.round(a * 255);
    }
  }
  return writePNG({ width: size, height: size, px });
}

/** An .ico of PNG images (Windows Vista and later read them). */
function ico(images) {
  const head = Buffer.alloc(6 + 16 * images.length);
  head.writeUInt16LE(1, 2);
  head.writeUInt16LE(images.length, 4);
  let offset = head.length;
  images.forEach(({ size, png }, i) => {
    const e = 6 + 16 * i;
    head[e] = size >= 256 ? 0 : size;
    head[e + 1] = size >= 256 ? 0 : size;
    head.writeUInt16LE(1, e + 4);
    head.writeUInt16LE(32, e + 6);
    head.writeUInt32LE(png.length, e + 8);
    head.writeUInt32LE(offset, e + 12);
    offset += png.length;
  });
  return Buffer.concat([head, ...images.map((i) => i.png)]);
}

/** Writes the program's icon (Hermesh.exe's, its shortcuts' and Setup.exe's) to icoPath. */
export function windowsIcons(icoPath) {
  const mark = readPNG(fs.readFileSync(path.join(desktop, "build/AppIcon.icon/Assets/mark.png")));
  const icon = master(mark);
  fs.writeFileSync(icoPath, ico([16, 24, 32, 48, 64, 128, 256].map((size) => ({ size, png: scaled(icon, size) }))));
}
