// What Setup.exe shows while it installs the Windows app (Squirrel's
// loading GIF): the mark on the icon's dark fill, and a thin bar running
// under it. No words: Setup.exe can't pick the person's language. Drawn
// from the macOS icon's mark, and written as a GIF here (no image library).
import fs from "node:fs";
import path from "node:path";
import { readPNG } from "./windows-icons.mjs";

const desktop = path.resolve(import.meta.dirname, "..");
const fill = [15, 15, 20]; // the icon's fill (windows-icons.mjs)
const [width, height] = [320, 200];
const markSize = 112; // the mark's picture, which holds the mark at about 64%
const bar = { y: 158, width: 96, height: 2, run: 32 };
const ends = [[122, 235, 223], [176, 156, 255]]; // the mark's teal and violet
const frames = 30; // at 40 ms: once along the bar every 1.2 s

/** Writes the splash to gifPath. */
export function installSplash(gifPath) {
  const mark = markPicture();
  const pictures = Array.from({ length: frames }, (_, i) => picture(mark, i / frames));
  const palette = paletteOf(pictures);
  fs.writeFileSync(gifPath, gif(pictures.map((p) => indexed(p, palette)), palette));
}

/** The mark's picture at markSize, premultiplied RGBA floats (0 to 1), averaged down from 1024 px. */
function markPicture() {
  const src = readPNG(fs.readFileSync(path.join(desktop, "build/AppIcon.icon/Assets/mark.png")));
  const out = new Float64Array(markSize * markSize * 4);
  const step = src.width / markSize;
  for (let y = 0; y < markSize; y++) {
    for (let x = 0; x < markSize; x++) {
      const acc = [0, 0, 0, 0];
      let count = 0;
      for (let sy = Math.floor(y * step); sy < Math.floor((y + 1) * step); sy++) {
        for (let sx = Math.floor(x * step); sx < Math.floor((x + 1) * step); sx++) {
          const k = (sy * src.width + sx) * 4;
          const a = src.px[k + 3] / 255;
          for (let c = 0; c < 3; c++) acc[c] += (src.px[k + c] / 255) * a;
          acc[3] += a;
          count++;
        }
      }
      for (let c = 0; c < 4; c++) out[(y * markSize + x) * 4 + c] = acc[c] / count;
    }
  }
  return out;
}

/** One frame, RGB bytes: the mark centred a little above the middle, the bar's run at t (0 to 1) along it. */
function picture(mark, t) {
  const px = Buffer.alloc(width * height * 3);
  const mx = (width - markSize) / 2, my = 34;
  const bx = (width - bar.width) / 2;
  // The run enters from the left and leaves at the right, eased.
  const e = t < 0.5 ? 2 * t * t : 1 - (-2 * t + 2) ** 2 / 2;
  const from = bx - bar.run + e * (bar.width + bar.run);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      let rgb = fill.map((c) => c / 255);
      const ix = x - mx, iy = y - my;
      if (ix >= 0 && iy >= 0 && ix < markSize && iy < markSize) {
        const k = (iy * markSize + ix) * 4;
        rgb = rgb.map((c, i) => mark[k + i] + (1 - mark[k + 3]) * c);
      }
      if (y >= bar.y && y < bar.y + bar.height && x >= bx && x < bx + bar.width) {
        // The track, faint; the run over it, teal to violet, its ends fading.
        rgb = rgb.map((c) => c + 0.1 * (1 - c));
        const at = (x + 0.5 - from) / bar.run;
        if (at >= 0 && at < 1) {
          const a = Math.min(1, at * 4, (1 - at) * 4);
          rgb = rgb.map((c, i) => (1 - a) * c + (a * (ends[0][i] + (ends[1][i] - ends[0][i]) * at)) / 255);
        }
      }
      for (let c = 0; c < 3; c++) px[(y * width + x) * 3 + c] = Math.round(Math.min(1, rgb[c]) * 255);
    }
  }
  return px;
}

/** At most 256 colours: the most frequent, at 5 bits a channel. */
function paletteOf(pictures) {
  const counts = new Map();
  for (const p of pictures) {
    for (let i = 0; i < p.length; i += 3) {
      const key = ((p[i] >> 3) << 10) | ((p[i + 1] >> 3) << 5) | (p[i + 2] >> 3);
      counts.set(key, (counts.get(key) ?? 0) + 1);
    }
  }
  return [...counts.entries()].sort((a, b) => b[1] - a[1]).slice(0, 256)
    .map(([key]) => [key >> 10, (key >> 5) & 31, key & 31].map((c) => (c << 3) | (c >> 2)));
}

/** A frame as palette indices, each pixel its nearest colour. */
function indexed(p, palette) {
  const out = new Uint8Array(p.length / 3);
  const near = new Map();
  for (let i = 0; i < out.length; i++) {
    const [r, g, b] = [p[i * 3], p[i * 3 + 1], p[i * 3 + 2]];
    const key = (r << 16) | (g << 8) | b;
    let best = near.get(key);
    if (best === undefined) {
      let d = Infinity;
      palette.forEach(([pr, pg, pb], j) => {
        const dj = (pr - r) ** 2 + (pg - g) ** 2 + (pb - b) ** 2;
        if (dj < d) [d, best] = [dj, j];
      });
      near.set(key, best);
    }
    out[i] = best;
  }
  return out;
}

/** An animated GIF89a, looping, of indexed frames over one palette. */
function gif(indexedFrames, palette) {
  const parts = [Buffer.from("GIF89a")];
  const screen = Buffer.alloc(7);
  screen.writeUInt16LE(width, 0);
  screen.writeUInt16LE(height, 2);
  screen[4] = 0xf7; // a global palette of 256 colours
  parts.push(screen, Buffer.from(Array.from({ length: 256 }, (_, i) => palette[i] ?? [0, 0, 0]).flat()));
  parts.push(Buffer.from([0x21, 0xff, 11, ...Buffer.from("NETSCAPE2.0"), 3, 1, 0, 0, 0])); // loop forever
  for (const f of indexedFrames) {
    parts.push(Buffer.from([0x21, 0xf9, 4, 0, 4, 0, 0, 0])); // 40 ms
    const desc = Buffer.alloc(10);
    desc[0] = 0x2c;
    desc.writeUInt16LE(width, 5);
    desc.writeUInt16LE(height, 7);
    parts.push(desc, Buffer.from([8]), subBlocks(lzw(f)));
  }
  parts.push(Buffer.from([0x3b]));
  return Buffer.concat(parts);
}

/** GIF's LZW at 8 bits a pixel. */
function lzw(pixels) {
  const clear = 256, end = 257;
  const bytes = [];
  let bits = 0, acc = 0;
  const put = (code, size) => {
    acc |= code << bits;
    bits += size;
    while (bits >= 8) {
      bytes.push(acc & 0xff);
      acc >>>= 8;
      bits -= 8;
    }
  };
  let dict = new Map(), next = 258, size = 9;
  put(clear, size);
  let prefix = pixels[0];
  for (let i = 1; i < pixels.length; i++) {
    const key = prefix * 256 + pixels[i];
    const known = dict.get(key);
    if (known !== undefined) {
      prefix = known;
      continue;
    }
    put(prefix, size);
    if (next < 4096) {
      dict.set(key, next++);
      if (next > 1 << size && size < 12) size++;
    } else {
      put(clear, size);
      dict = new Map();
      next = 258;
      size = 9;
    }
    prefix = pixels[i];
  }
  put(prefix, size);
  put(end, size);
  if (bits) bytes.push(acc & 0xff);
  return Buffer.from(bytes);
}

/** Data in GIF's blocks of at most 255 bytes. */
function subBlocks(data) {
  const out = [];
  for (let i = 0; i < data.length; i += 255) {
    const n = Math.min(255, data.length - i);
    out.push(Buffer.from([n]), data.subarray(i, i + n));
  }
  out.push(Buffer.from([0]));
  return Buffer.concat(out);
}
