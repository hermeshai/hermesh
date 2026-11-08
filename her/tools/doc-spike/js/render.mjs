// Renders one page with pdf.js (annotations in display mode) to PNG, and lists the annotations pdf.js sees.
import fs from "node:fs";
import { createCanvas } from "@napi-rs/canvas";
import * as pdfjs from "pdfjs-dist/legacy/build/pdf.mjs";

const [file, pageNo, out, password] = process.argv.slice(2);
const doc = await pdfjs.getDocument({ data: new Uint8Array(fs.readFileSync(file)), password, isEvalSupported: false, enableXfa: false,
  standardFontDataUrl: new URL("./node_modules/pdfjs-dist/standard_fonts/", import.meta.url).pathname,
  cMapUrl: new URL("./node_modules/pdfjs-dist/cmaps/", import.meta.url).pathname, cMapPacked: true }).promise;
const page = await doc.getPage(Number(pageNo));
const vp = page.getViewport({ scale: 1.5 });
const canvas = createCanvas(vp.width, vp.height);
await page.render({ canvas, canvasContext: canvas.getContext("2d"), viewport: vp, annotationMode: pdfjs.AnnotationMode.ENABLE }).promise;
fs.writeFileSync(out, canvas.toBuffer("image/png"));
const annots = await page.getAnnotations();
console.log("annotations:", annots.map(a => `${a.subtype}${a.hasAppearance ? "+AP" : ""}${a.contentsObj?.str ? `「${a.contentsObj.str.slice(0, 12)}」` : ""}`).join(", "));
