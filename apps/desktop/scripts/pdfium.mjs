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
  },
};

export const pdfiumLibrary = "libpdfium.dylib";

/** The folder `pdfiumFor` extracted the release for `arch` (arm64 or x64) into, from `cache`: lib/, LICENSE and licenses/. */
export function pdfiumFor(arch, cache, release = pdfiumRelease, download = curl) {
  const expected = release.sha256[arch];
  if (!expected) throw new Error(`PDFium: no archive for ${arch}`);
  const dir = path.join(cache, release.tag.replace(/\W/g, "-"), arch);
  const stamp = path.join(dir, "sha256");
  if (!fs.existsSync(path.join(dir, "lib", pdfiumLibrary)) || !fs.existsSync(stamp) || fs.readFileSync(stamp, "utf8") !== expected) {
    fs.rmSync(dir, { recursive: true, force: true });
    const staging = fs.mkdtempSync(path.join(os.tmpdir(), "pdfium-"));
    try {
      const archive = path.join(staging, `pdfium-mac-${arch}.tgz`);
      download(`https://github.com/bblanchon/pdfium-binaries/releases/download/${release.tag}/pdfium-mac-${arch}.tgz`, archive);
      const got = createHash("sha256").update(fs.readFileSync(archive)).digest("hex");
      if (got !== expected) throw new Error(`PDFium ${release.tag} ${arch}: the archive's sha256 is ${got}, expected ${expected}`);
      fs.mkdirSync(dir, { recursive: true });
      execFileSync("tar", ["-xzf", archive, "-C", dir]);
      fs.writeFileSync(stamp, expected);
    } finally {
      fs.rmSync(staging, { recursive: true, force: true });
    }
  }
  return dir;
}

function curl(url, to) {
  execFileSync("curl", ["--fail", "--location", "--silent", "--show-error", "--output", to, url], { stdio: "inherit" });
}
