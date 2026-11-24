import assert from "node:assert/strict";
import { test } from "node:test";
import { pastedFiles } from "./attachments.ts";

// What the window's clipboard gives a paste (Chromium's, as found in Electron): a picture has no path on this Mac,
// a file copied in Finder has one.
const paths = new Map<File, string>();
(globalThis as { window?: unknown }).window = { hermesh: { pathForFile: (f: File) => paths.get(f) ?? "" } };

const paste = (text: string, files: File[]) => ({ files, getData: (t: string) => (t === "text/plain" ? text : "") }) as unknown as DataTransfer;
const picture = () => new File([new Uint8Array([1, 2, 3])], "image.png", { type: "image/png" });

test("a screenshot pastes as its picture", () => {
  const p = picture();
  assert.deepEqual(pastedFiles(paste("", [p])), [p]);
});

test("words copied with a picture of them (a spreadsheet's cells) paste as the words", () => {
  assert.deepEqual(pastedFiles(paste("收入\t123\n成本\t45", [picture()])), []);
});

test("files copied in Finder paste as the files, whatever text names them", () => {
  const pdf = new File(["%PDF-1.4"], "report.pdf", { type: "application/pdf" });
  paths.set(pdf, "/Users/me/report.pdf");
  assert.deepEqual(pastedFiles(paste("report.pdf", [pdf])), [pdf]);
});

test("words alone paste as words", () => {
  assert.deepEqual(pastedFiles(paste("hello", [])), []);
});
