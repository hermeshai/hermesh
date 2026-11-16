import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import { agentDesktopBinaries, agentDesktopFiles, agentDesktopLinuxFiles, integrityOf } from "./agent-desktop.mjs";

const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), "hermesh-ad-"));

/** A package laid out like agent-desktop's, as a tarball; `corrupt` breaks one binary after its sha256 is written. */
function fakePackage(corrupt = false) {
  const root = tmp();
  const bin = path.join(root, "package", "bin");
  fs.mkdirSync(bin, { recursive: true });
  for (const [system, names] of [["darwin", agentDesktopFiles], ["linux", agentDesktopLinuxFiles]]) {
    for (const name of names) {
      for (const arch of ["arm64", "x64"]) {
        const file = path.join(bin, `${name}-${system}-${arch}`);
        const body = system === "darwin" ? `${name} ${arch}` : `${name} ${system} ${arch}`;
        fs.writeFileSync(file, body);
        fs.writeFileSync(`${file}.sha256`, `${createHash("sha256").update(body).digest("hex")}  ${path.basename(file)}\n`);
      }
    }
  }
  for (const arch of ["arm64", "x64"]) {
    const file = path.join(bin, `agent-desktop-win32-${arch}.exe`);
    fs.writeFileSync(file, `agent-desktop.exe ${arch}`);
    fs.writeFileSync(`${file}.sha256`, `${createHash("sha256").update(`agent-desktop.exe ${arch}`).digest("hex")}  ${path.basename(file)}\n`);
  }
  if (corrupt) fs.writeFileSync(path.join(bin, "agent-desktop-darwin-arm64"), "tampered");
  const tarball = path.join(root, "agent-desktop-9.9.9.tgz");
  execFileSync("tar", ["-czf", tarball, "-C", root, "package"]);
  return tarball;
}

test("the pinned package is fetched once, checked, and its binaries picked by arch", () => {
  const tarball = fakePackage();
  const release = { version: "9.9.9", integrity: integrityOf(tarball) };
  const cache = tmp();
  let packs = 0;
  const pack = (spec, dir) => {
    packs++;
    assert.equal(spec, "agent-desktop@9.9.9");
    fs.copyFileSync(tarball, path.join(dir, "p.tgz"));
    return path.join(dir, "p.tgz");
  };
  const got = agentDesktopBinaries("x64", cache, release, pack);
  assert.deepEqual(Object.keys(got), agentDesktopFiles);
  assert.equal(fs.readFileSync(got["agent-desktop"], "utf8"), "agent-desktop x64");
  assert.equal(fs.readFileSync(got["agent-desktop-macos-helper"], "utf8"), "agent-desktop-macos-helper x64");
  agentDesktopBinaries("arm64", cache, release, pack);
  // Windows has one program, under its own name.
  const windows = agentDesktopBinaries("arm64", cache, release, pack, "win32");
  assert.deepEqual(Object.keys(windows), ["agent-desktop.exe"]);
  assert.equal(fs.readFileSync(windows["agent-desktop.exe"], "utf8"), "agent-desktop.exe arm64");
  // Linux's, for the release that carries them for her: the program and its clipboard helper.
  const linux = agentDesktopBinaries("arm64", cache, release, pack, "linux");
  assert.deepEqual(Object.keys(linux), agentDesktopLinuxFiles);
  assert.equal(fs.readFileSync(linux["agent-desktop-linux-clipboard-owner"], "utf8"), "agent-desktop-linux-clipboard-owner linux arm64");
  assert.equal(packs, 1, "the cache is reused");
});

test("a package that is not the pinned one is refused", () => {
  const tarball = fakePackage();
  const cache = tmp();
  const pack = (_spec, dir) => (fs.copyFileSync(tarball, path.join(dir, "p.tgz")), path.join(dir, "p.tgz"));
  assert.throws(() => agentDesktopBinaries("arm64", cache, { version: "9.9.9", integrity: "sha512-AAAA" }, pack), /integrity/);
  assert.equal(fs.existsSync(path.join(cache, "9.9.9")), false, "nothing is kept");
});

test("a binary that does not match its sha256 is refused", () => {
  const tarball = fakePackage(true);
  const pack = (_spec, dir) => (fs.copyFileSync(tarball, path.join(dir, "p.tgz")), path.join(dir, "p.tgz"));
  assert.throws(() => agentDesktopBinaries("arm64", tmp(), { version: "9.9.9", integrity: integrityOf(tarball) }, pack), /sha256/);
});
