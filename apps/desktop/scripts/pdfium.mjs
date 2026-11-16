// PDFium (https://github.com/bblanchon/pdfium-binaries), the library her's document worker loads
// (her/crates/her-docs), as Hermesh.app carries it in Contents/Frameworks: one pinned release,
// each archive checked against its sha256, kept between builds in node_modules/.cache.
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

/** The release Hermesh ships (its VERSION: 156.0.8076.0, as her's CI tests against); a new one needs the archives' digests from `gh release view`. */
export const pdfiumRelease = {
  tag: "chromium/8076",
  sha256: {
    arm64: "0d6781fe08906baff3d82c90953e519fbc4eb253fe76431e5ed53b157763b97c",
    x64: "40865f34642c34d82cc336132df9e0347133f4692cd46776647af160f9a5cca9",
    "win-arm64": "ab7c45c25fc8456fd9b94666ce3dd27a940f4984f54b328f012468f869d34346",
    "win-x64": "808d36da9bc5a3104315fb307c80998121f565ee53953633bf33e80d7429e5ac",
    // Linux's, which Hermesh's release carries beside her PDF worker (docs/linux.md §4).
    "linux-arm64": "d7247b33ae5545615a5e877235dd97afc879e3a8805689684f528cae3339d352",
    "linux-x64": "d9d67bc40af03aef4fe28a60b19b1086f28ace019c8c9caf19cb7fe3d14ceca3",
  },
};

export const pdfiumLibrary = "libpdfium.dylib";
/** The library in an extracted release, by system: the Mac's (`mac`), Windows' (`win`) or Linux's (`linux`). */
export const pdfiumLibraryIn = { mac: path.join("lib", pdfiumLibrary), win: path.join("bin", "pdfium.dll"), linux: path.join("lib", "libpdfium.so") };

/**
 * The folder `pdfiumFor` extracted the release for `arch` (arm64 or x64) and `system` into, from
 * `cache`: the library (`pdfiumLibraryIn`), LICENSE and licenses/.
 */
export function pdfiumFor(arch, cache, release = pdfiumRelease, download = curl, system = "mac") {
  const name = system === "mac" ? arch : `${system}-${arch}`;
  const expected = release.sha256[name];
  if (!expected) throw new Error(`PDFium: no archive for ${name}`);
  const dir = path.join(cache, release.tag.replace(/\W/g, "-"), name);
  const stamp = path.join(dir, "sha256");
  if (!fs.existsSync(path.join(dir, pdfiumLibraryIn[system])) || !fs.existsSync(stamp) || fs.readFileSync(stamp, "utf8") !== expected) {
    fs.rmSync(dir, { recursive: true, force: true });
    const staging = fs.mkdtempSync(path.join(os.tmpdir(), "pdfium-"));
    try {
      const file = `pdfium-${system}-${arch}.tgz`;
      const archive = path.join(staging, file);
      download(`https://github.com/bblanchon/pdfium-binaries/releases/download/${release.tag}/${file}`, archive);
      const got = createHash("sha256").update(fs.readFileSync(archive)).digest("hex");
      if (got !== expected) throw new Error(`PDFium ${release.tag} ${name}: the archive's sha256 is ${got}, expected ${expected}`);
      // By names in the staging folder: a tar that takes "C:" for a host never sees one.
      fs.mkdirSync(path.join(staging, "out"));
      execFileSync("tar", ["-xzf", file, "-C", "out"], { cwd: staging });
      fs.cpSync(path.join(staging, "out"), dir, { recursive: true });
      fs.writeFileSync(stamp, expected);
    } finally {
      fs.rmSync(staging, { recursive: true, force: true });
    }
  }
  return dir;
}

/** A one-page PDF, for checking the PDFium an app carries. */
export function minimalPdf() {
  const objects = ["<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>"];
  let pdf = "%PDF-1.4\n";
  const offsets = objects.map((o, i) => {
    const at = pdf.length;
    pdf += `${i + 1} 0 obj\n${o}\nendobj\n`;
    return at;
  });
  const xref = pdf.length;
  pdf += `xref\n0 ${objects.length + 1}\n0000000000 65535 f \n${offsets.map((o) => `${String(o).padStart(10, "0")} 00000 n \n`).join("")}`;
  return pdf + `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`;
}

function curl(url, to) {
  execFileSync("curl", ["--fail", "--location", "--silent", "--show-error", "--output", to, url], { stdio: "inherit" });
}

// `node pdfium.mjs linux <x64|arm64> <folder>`: PDFium and its licence into the folder a Linux
// release carries as her-pdf-linux-<arch>.tar.gz (scripts/release.sh), beside her PDF worker.
if (process.argv[1] && path.resolve(process.argv[1]) === import.meta.filename && process.argv[2] === "linux") {
  const [arch, folder] = process.argv.slice(3);
  if (!pdfiumRelease.sha256[`linux-${arch}`] || !folder) throw new Error("usage: pdfium.mjs linux <x64|arm64> <folder>");
  const from = pdfiumFor(arch, path.join(import.meta.dirname, "..", "node_modules", ".cache", "pdfium"), pdfiumRelease, curl, "linux");
  fs.mkdirSync(folder, { recursive: true });
  fs.chmodSync(folder, 0o755);
  fs.copyFileSync(path.join(from, pdfiumLibraryIn.linux), path.join(folder, "libpdfium.so"));
  fs.copyFileSync(path.join(from, "LICENSE"), path.join(folder, "PDFium-LICENSE"));
  for (const name of ["libpdfium.so", "PDFium-LICENSE"]) fs.chmodSync(path.join(folder, name), 0o644);
}
