import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import { pdfiumFor, pdfiumLibrary, pdfiumLibraryIn } from "./pdfium.mjs";

const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), "hermesh-pdfium-"));

/** An archive laid out like the release's, and its sha256. */
function fakeArchive() {
  const root = tmp();
  fs.mkdirSync(path.join(root, "lib"));
  fs.writeFileSync(path.join(root, "lib", pdfiumLibrary), "library");
  fs.writeFileSync(path.join(root, "LICENSE"), "licence");
  const archive = path.join(tmp(), "pdfium.tgz");
  execFileSync("tar", ["-czf", archive, "-C", root, "lib", "LICENSE"]);
  return { archive, sha256: createHash("sha256").update(fs.readFileSync(archive)).digest("hex") };
}

test("the pinned archive is downloaded once, checked and extracted", () => {
  const { archive, sha256 } = fakeArchive();
  const release = { tag: "chromium/1", sha256: { arm64: sha256 } };
  const cache = tmp();
  const urls = [];
  const download = (url, to) => (urls.push(url), fs.copyFileSync(archive, to));
  const dir = pdfiumFor("arm64", cache, release, download);
  assert.equal(fs.readFileSync(path.join(dir, "lib", pdfiumLibrary), "utf8"), "library");
  assert.equal(fs.readFileSync(path.join(dir, "LICENSE"), "utf8"), "licence");
  assert.deepEqual(urls, ["https://github.com/bblanchon/pdfium-binaries/releases/download/chromium/1/pdfium-mac-arm64.tgz"]);
  assert.equal(pdfiumFor("arm64", cache, release, download), dir);
  assert.equal(urls.length, 1, "the cache is reused");
});

test("Linux's archive of the same release is another one, with its own digest", () => {
  const root = tmp();
  fs.mkdirSync(path.join(root, "lib"));
  fs.writeFileSync(path.join(root, "lib", "libpdfium.so"), "linux library");
  fs.writeFileSync(path.join(root, "LICENSE"), "licence");
  const archive = path.join(tmp(), "pdfium.tgz");
  execFileSync("tar", ["-czf", archive, "-C", root, "lib", "LICENSE"]);
  const sha256 = createHash("sha256").update(fs.readFileSync(archive)).digest("hex");
  const urls = [];
  const download = (url, to) => (urls.push(url), fs.copyFileSync(archive, to));
  const release = { tag: "chromium/1", sha256: { x64: "0".repeat(64), "linux-x64": sha256, "linux-arm64": "0".repeat(64) } };
  const dir = pdfiumFor("x64", tmp(), release, download, "linux");
  assert.equal(fs.readFileSync(path.join(dir, pdfiumLibraryIn.linux), "utf8"), "linux library");
  assert.deepEqual(urls, ["https://github.com/bblanchon/pdfium-binaries/releases/download/chromium/1/pdfium-linux-x64.tgz"]);
  assert.throws(() => pdfiumFor("arm64", tmp(), release, download, "linux"), /sha256/);
});

test("an archive that is not the pinned one is refused, and nothing is kept", () => {
  const { archive } = fakeArchive();
  const cache = tmp();
  assert.throws(() => pdfiumFor("arm64", cache, { tag: "chromium/1", sha256: { arm64: "0".repeat(64) } }, (_url, to) => fs.copyFileSync(archive, to)), /sha256/);
  assert.deepEqual(fs.readdirSync(cache), []);
  assert.throws(() => pdfiumFor("x64", cache, { tag: "chromium/1", sha256: { arm64: "0".repeat(64) } }), /no archive for x64/);
});

test("Windows' archive has its own name, digest and library", () => {
  const root = tmp();
  fs.mkdirSync(path.join(root, "bin"));
  fs.writeFileSync(path.join(root, "bin", "pdfium.dll"), "library");
  const archive = path.join(tmp(), "pdfium.tgz");
  execFileSync("tar", ["-czf", "pdfium.tgz", "-C", path.relative(path.dirname(archive), root), "bin"], { cwd: path.dirname(archive) });
  const release = { tag: "chromium/1", sha256: { arm64: "0".repeat(64), "win-arm64": createHash("sha256").update(fs.readFileSync(archive)).digest("hex") } };
  const urls = [];
  const dir = pdfiumFor("arm64", tmp(), release, (url, to) => (urls.push(url), fs.copyFileSync(archive, to)), "win");
  assert.equal(fs.readFileSync(path.join(dir, pdfiumLibraryIn.win), "utf8"), "library");
  assert.deepEqual(urls, ["https://github.com/bblanchon/pdfium-binaries/releases/download/chromium/1/pdfium-win-arm64.tgz"]);
  assert.throws(() => pdfiumFor("x64", tmp(), release, () => {}, "win"), /no archive for win-x64/);
});
