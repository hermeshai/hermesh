// V3: for every pdf.js text item, ask pdf_oxide for the characters inside the item's box; compare normalised.
import fs from "node:fs";
import { execFileSync } from "node:child_process";
import * as pdfjs from "pdfjs-dist/legacy/build/pdf.mjs";

const norm = (s) => s.normalize("NFKC").replace(/-\s*$/, "").replace(/\s+/g, "")
  .replace(/(?<=[　-鿿＀-￯])\s+(?=[　-鿿＀-￯])/g, "");
const [file, ...pages] = process.argv.slice(2);
const doc = await pdfjs.getDocument({ data: new Uint8Array(fs.readFileSync(file)), isEvalSupported: false,
  cMapUrl: new URL("./node_modules/pdfjs-dist/cmaps/", import.meta.url).pathname, cMapPacked: true,
  standardFontDataUrl: new URL("./node_modules/pdfjs-dist/standard_fonts/", import.meta.url).pathname }).promise;
let total = 0, same = 0, geom = [];
const bad = [];
for (const p of pages.length ? pages.map(Number) : [...Array(doc.numPages).keys()].map((i) => i + 1)) {
  const page = await doc.getPage(p);
  const items = (await page.getTextContent()).items.filter((i) => i.str.trim().length > 1 && Math.abs(i.transform[1]) < 1e-3);
  // box in PDF space: baseline at transform[5]; pdf.js height ~ font size. Pad a little vertically so glyph centres fall inside.
  const rects = items.map((i) => [i.transform[4], i.transform[5] - 0.25 * i.height, i.width, i.height * 1.1]);
  const got = JSON.parse((process.env.RESOLVER === "pdfium"
    ? execFileSync(process.env.PDFIUM_PY, ["-I", "scripts/pdfium_resolve.py", file, String(p - 1)], { input: JSON.stringify(rects) })
    : execFileSync("target/release/doc-spike", ["resolve", file, String(p - 1)], { input: JSON.stringify(rects) })).toString());
  items.forEach((it, k) => {
    total++;
    if (norm(it.str) === norm(got[k])) same++;
    else if (bad.length < 12) bad.push({ page: p, pdfjs: it.str.slice(0, 50), oxide: got[k].slice(0, 50) });
  });
}
console.log(JSON.stringify({ file: file.split("/").pop(), items: total, match: same, rate: +(same / total * 100).toFixed(2) }));
for (const b of bad) console.log("  ", JSON.stringify(b));
