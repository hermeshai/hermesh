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
  },
};

export const pdfiumLibrary = "libpdfium.dylib";
/** The library in an extracted release, by system: the Mac's (`mac`) or Windows' (`win`). */
export const pdfiumLibraryIn = { mac: path.join("lib", pdfiumLibrary), win: path.join("bin", "pdfium.dll") };

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
