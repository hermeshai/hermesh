// agent-desktop (https://github.com/vercel-labs/agent-desktop), the program her's desktop tools
// run (her/crates/her-desktop), as Hermesh.app carries it in Contents/Helpers beside her: one
// pinned npm release, checked against the registry's integrity and each binary's own sha256, kept
// between builds in node_modules/.cache. The package's postinstall never runs: only its prebuilt
// binaries are taken.
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

/** The release Hermesh ships; a new one needs its `npm view agent-desktop@<v> dist.integrity`. */
export const agentDesktopRelease = {
  version: "1.0.2",
  integrity: "sha512-k2Qi3f0y1U60YaJOw1dob3EO55P/DuH4HK3WKdI9sMZLjVxpGZAzuG50rIWuIqS0JIVy07g+0pp9Of2JDjTX0w==",
};

/** The program and the helper it starts, which must stay beside it, by the names they are installed under. */
export const agentDesktopFiles = ["agent-desktop", "agent-desktop-macos-helper"];

/** The same for Linux, where Hermesh's release carries them for her (docs/linux.md §5): the program and its clipboard helper. */
export const agentDesktopLinuxFiles = ["agent-desktop", "agent-desktop-linux-clipboard-owner"];

/** `sha512-…` of `file`, as npm writes integrity. */
export function integrityOf(file) {
  return `sha512-${createHash("sha512").update(fs.readFileSync(file)).digest("base64")}`;
}

/**
 * The binaries for `arch` (arm64 or x64) and `system` (darwin, linux, or win32, whose one program is
 * agent-desktop.exe), name → path, from `cache`; fetched with `npm pack` (the user's registry;
 * the integrity makes any registry safe) when not there yet.
 */
export function agentDesktopBinaries(arch, cache, release = agentDesktopRelease, pack = npmPack, system = "darwin") {
  const dir = path.join(cache, release.version);
  const bin = path.join(dir, "package", "bin");
  if (!fs.existsSync(path.join(dir, "integrity")) || fs.readFileSync(path.join(dir, "integrity"), "utf8") !== release.integrity) {
    fs.rmSync(dir, { recursive: true, force: true });
    const staging = fs.mkdtempSync(path.join(os.tmpdir(), "agent-desktop-"));
    try {
      const tarball = pack(`agent-desktop@${release.version}`, staging);
      const got = integrityOf(tarball);
      if (got !== release.integrity) throw new Error(`agent-desktop ${release.version}: the package's integrity is ${got}, expected ${release.integrity}`);
      // By names in the staging folder: a tar that takes "C:" for a host never sees one.
      fs.mkdirSync(path.join(staging, "out"));
      execFileSync("tar", ["-xzf", path.relative(staging, tarball), "-C", "out"], { cwd: staging });
      fs.cpSync(path.join(staging, "out"), dir, { recursive: true });
      fs.writeFileSync(path.join(dir, "integrity"), release.integrity);
    } finally {
      fs.rmSync(staging, { recursive: true, force: true });
    }
  }
  const packed =
    system === "win32"
      ? { "agent-desktop.exe": `agent-desktop-win32-${arch}.exe` }
      : Object.fromEntries((system === "linux" ? agentDesktopLinuxFiles : agentDesktopFiles).map((name) => [name, `${name}-${system}-${arch}`]));
  const out = {};
  for (const [name, as] of Object.entries(packed)) {
    const file = path.join(bin, as);
    const expected = fs.readFileSync(`${file}.sha256`, "utf8").trim().split(/\s+/)[0].toLowerCase();
    const actual = createHash("sha256").update(fs.readFileSync(file)).digest("hex");
    if (actual !== expected) throw new Error(`${path.basename(file)}: sha256 ${actual}, the package says ${expected}`);
    out[name] = file;
  }
  return out;
}

/** `npm pack <spec>` into `dir`; the tarball's path. */
function npmPack(spec, dir) {
  // On Windows npm is a script, which only a shell starts.
  const [packed] = JSON.parse(execFileSync("npm", ["pack", spec, "--json", "--pack-destination", dir], { cwd: dir, encoding: "utf8", shell: process.platform === "win32" }));
  return path.join(dir, packed.filename);
}

// `node agent-desktop.mjs linux <x64|arm64> <folder>`: the folder a Linux release carries as
// her-computer-linux-<arch>.tar.gz (scripts/release.sh), its files programs whatever the umask.
if (process.argv[1] && path.resolve(process.argv[1]) === import.meta.filename && process.argv[2] === "linux") {
  const [arch, folder] = process.argv.slice(3);
  if (!["x64", "arm64"].includes(arch) || !folder) throw new Error("usage: agent-desktop.mjs linux <x64|arm64> <folder>");
  const cache = path.join(import.meta.dirname, "..", "node_modules", ".cache", "agent-desktop");
  fs.mkdirSync(folder, { recursive: true });
  fs.chmodSync(folder, 0o755);
  for (const [name, file] of Object.entries(agentDesktopBinaries(arch, cache, agentDesktopRelease, npmPack, "linux"))) {
    fs.copyFileSync(file, path.join(folder, name));
    fs.chmodSync(path.join(folder, name), 0o755);
  }
}
