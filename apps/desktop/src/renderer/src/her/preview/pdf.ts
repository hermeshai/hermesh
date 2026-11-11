import * as pdfjs from "pdfjs-dist";
import workerUrl from "pdfjs-dist/build/pdf.worker.min.mjs?url";
import "pdfjs-dist/web/pdf_viewer.css";

// pdf.js for a file shown over the window: it only shows pages and lets people select and
// search. Scripts never run; the bytes are the whole file, fetched from the assistant's machine.

pdfjs.GlobalWorkerOptions.workerSrc = workerUrl;
// The viewer components read the library from here.
(globalThis as { pdfjsLib?: unknown }).pdfjsLib = pdfjs;

export { pdfjs };
export const viewerLib = () => import("pdfjs-dist/web/pdf_viewer.mjs");

/** pdf.js's data files beside the page (electron.vite.config.ts). */
const data = (dir: string) => new URL(`pdfjs/${dir}/`, document.baseURI).href;

/** Opens a PDF's bytes; pdf.js takes what it is given to its worker, so it gets a copy (a password may ask again). */
export function loadPdf(bytes: Uint8Array, password?: string): Promise<pdfjs.PDFDocumentProxy> {
  return pdfjs.getDocument({
    data: bytes.slice(),
    isEvalSupported: false,
    enableXfa: false,
    password,
    cMapUrl: data("cmaps"),
    cMapPacked: true,
    standardFontDataUrl: data("standard_fonts"),
    wasmUrl: data("wasm"),
  }).promise;
}

export type OutlineItem = { title: string; page: number | null; children: OutlineItem[] };

/** The document's outline, each entry with the page it goes to (null for one that goes nowhere in it). */
export async function outlineOf(pdf: pdfjs.PDFDocumentProxy): Promise<OutlineItem[]> {
  const items = (await pdf.getOutline()) ?? [];
  const pageOf = async (dest: unknown): Promise<number | null> => {
    try {
      const explicit = typeof dest === "string" ? await pdf.getDestination(dest) : dest;
      const ref = Array.isArray(explicit) ? explicit[0] : null;
      if (typeof ref === "number") return ref + 1;
      return ref ? (await pdf.getPageIndex(ref)) + 1 : null;
    } catch {
      return null;
    }
  };
  const walk = (list: typeof items): Promise<OutlineItem[]> =>
    Promise.all(list.map(async (i) => ({ title: i.title, page: await pageOf(i.dest), children: await walk(i.items ?? []) })));
  return walk(items);
}
