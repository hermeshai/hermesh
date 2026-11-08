// Prints every non-space character pdf.js extracts from a PDF (all pages), one string.
import fs from "node:fs";
import * as pdfjs from "pdfjs-dist/legacy/build/pdf.mjs";
const doc = await pdfjs.getDocument({ data: new Uint8Array(fs.readFileSync(process.argv[2])), isEvalSupported: false,
  cMapUrl: new URL("./node_modules/pdfjs-dist/cmaps/", import.meta.url).pathname, cMapPacked: true }).promise;
let s = "";
for (let p = 1; p <= doc.numPages; p++) s += (await (await doc.getPage(p)).getTextContent()).items.map((i) => i.str).join("");
process.stdout.write(s.replace(/\s+/g, ""));
