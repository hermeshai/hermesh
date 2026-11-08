#!/usr/bin/env node
// Publishes this version of the her CLI as a GitHub release of the public releases repository
// (docs/kernel/standalone-cli-design.md §13.10). The files go up to a draft, which is published in
// one step: until then the latest release stays the previous one.
//
//   pnpm release:cli                  every target
//   pnpm release:cli --dry-run        build, sign and check everything, publish nothing
//   pnpm release:cli --dry-run --targets aarch64-apple-darwin
//   pnpm release:cli --linux-host HOST   run the smoke test of a Linux build there too
//
// Needs: gh signed in with write access to the releases repository, notary credentials
// (HERMESH_NOTARY_*, as for the app), the signing key in the keychain, cargo-zigbuild and zig for
// Linux, a clean working tree, LICENSE, and what changed in crates/her-cli/releases/<version>.md.
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { parseArgs } from "node:util";
import { agentDesktopBinaries, agentDesktopFiles } from "../../apps/desktop/scripts/agent-desktop.mjs";
import { pdfiumLibraryFor, pdfiumLibrary } from "../../apps/desktop/scripts/pdfium.mjs";
import { notices } from "./notices.mjs";

const root = path.resolve(import.meta.dirname, "../..");
const REPO = "hermeshai/her-releases";
const TAP = "hermeshai/homebrew-tap";
const RELEASES = `https://github.com/${REPO}/releases`;
const KEYCHAIN = ["-a", "her", "-s", "her-release-signing"];
const ALL = ["aarch64-apple-darwin", "x86_64-apple-darwin", "x86_64-unknown-linux-musl", "aarch64-unknown-linux-musl"];
const IS_MAC = (target) => target.endsWith("apple-darwin");
const ARCH = { "aarch64-apple-darwin": "arm64", "x86_64-apple-darwin": "x64" };
// The PDF component's worker on Linux: dynamically linked, so that it can load the library.
const GLIBC = { "x86_64-unknown-linux-musl": "x86_64-unknown-linux-gnu.2.28", "aarch64-unknown-linux-musl": "aarch64-unknown-linux-gnu.2.28" };
const PDFIUM_LINUX = {
  tag: "chromium/8076",
  archive: { "x86_64-unknown-linux-musl": "pdfium-linux-x64.tgz", "aarch64-unknown-linux-musl": "pdfium-linux-arm64.tgz" },
};
// What ACP's registry and Homebrew call them.
const REGISTRY = { "aarch64-apple-darwin": "darwin-aarch64", "x86_64-apple-darwin": "darwin-x86_64", "x86_64-unknown-linux-musl": "linux-x86_64", "aarch64-unknown-linux-musl": "linux-aarch64" };

const { values: opts } = parseArgs({
  options: {
    "dry-run": { type: "boolean", default: false },
    targets: { type: "string", default: ALL.join(",") },
    out: { type: "string", default: path.join(root, "target/release-cli") },
    "linux-host": { type: "string" },
    "skip-components": { type: "boolean", default: false },
  },
});
const dry = opts["dry-run"];
const targets = opts.targets.split(",").map((t) => t.trim()).filter(Boolean);
const cargoToml = fs.readFileSync(path.join(root, "Cargo.toml"), "utf8");
const version = /\[workspace\.package\]\s*\nversion = "([^"]+)"/.exec(cargoToml)?.[1];
const tag = `v${version}`;
const out = path.resolve(opts.out, version);

const run = (cmd, args, options = {}) => execFileSync(cmd, args, { encoding: "utf8", maxBuffer: 1 << 28, ...options }).trim();
const sh = (cmd, args, options = {}) => execFileSync(cmd, args, { stdio: "inherit", cwd: root, ...options });
const step = (text) => console.log(`› ${text}`);
const fail = (message) => {
  console.error(`release ${tag}: ${message}`);
  process.exit(1);
};
const ready = (ok, message) => ok || (dry ? console.warn(`release ${tag}: ${message} (ignored in a dry run)`) : fail(message));
const has = (cmd, args = ["--version"]) => {
  try {
    execFileSync(cmd, args, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
};
const sha256 = (file) => createHash("sha256").update(fs.readFileSync(file)).digest("hex");

// ---- 1. Everything that can stop a release, before the long builds.
for (const target of targets) if (!ALL.includes(target)) fail(`unknown target ${target}`);
const notes = path.join(root, "crates/her-cli/releases", `${version}.md`);
ready(fs.existsSync(notes) && fs.readFileSync(notes, "utf8").trim() !== "", `write what changed in ${path.relative(root, notes)}`);
ready(!run("git", ["status", "--porcelain"], { cwd: root }), "the working tree has changes: a release is built from a commit");
ready(fs.existsSync(path.join(root, "LICENSE")), "there is no LICENSE: the licence is the owner's decision (standalone-cli-design §18.5)");
const notarize = process.env.HERMESH_NOTARY_PROFILE;
if (targets.some(IS_MAC)) ready(Boolean(notarize), "no notary credentials: set HERMESH_NOTARY_PROFILE");
let seed;
try {
  seed = run("security", ["find-generic-password", ...KEYCHAIN, "-w"]);
} catch {
  fail("the signing key is not in the keychain");
}
if (!dry) {
  const repo = has("gh", ["repo", "view", REPO, "--json", "isPrivate"]) ? JSON.parse(run("gh", ["repo", "view", REPO, "--json", "isPrivate"])) : null;
  if (!repo) fail(`${REPO} does not exist yet, or gh cannot see it`);
  if (repo.isPrivate) fail(`${REPO} is private: installs would fail`);
  if (has("gh", ["release", "view", tag, "--repo", REPO])) fail(`${tag} is released already`);
}
if (targets.some((t) => !IS_MAC(t))) {
  const zig = has("zig", ["version"]) || has("python3", ["-m", "ziglang", "version"]);
  if (!has("cargo-zigbuild") || !zig) fail("Linux builds need cargo-zigbuild and zig");
}

// ---- 2. Build, and 3. sign what is for macOS.
fs.rmSync(out, { recursive: true, force: true });
fs.mkdirSync(out, { recursive: true });
const work = fs.mkdtempSync(path.join(os.tmpdir(), "her-release-"));
const cache = path.join(root, "node_modules/.cache/her-release");

const identity = () => /"(Developer ID Application[^"]*)"/.exec(run("security", ["find-identity", "-v", "-p", "codesigning"]))?.[1];
function codesign(file, id) {
  const name = identity();
  if (!name) return ready(false, "no Developer ID identity: the macOS files stay unsigned");
  sh("codesign", ["--force", "--options", "runtime", "--timestamp", "--identifier", id, "--sign", name, file]);
}
function notary(file) {
  if (!notarize) return;
  const zip = `${file}.zip`;
  sh("ditto", ["-c", "-k", "--keepParent", file, zip]);
  sh("xcrun", ["notarytool", "submit", zip, "--keychain-profile", notarize, "--wait"]);
  fs.rmSync(zip);
}

function buildMain(target) {
  const profile = ["--profile", "dist", "--locked", "-p", "her-cli", "--bin", "her", "--no-default-features", "--target", target];
  if (IS_MAC(target)) {
    sh("cargo", ["build", ...profile]);
  } else {
    // The same linking as CI's: a static PIE from the toolchain's own files.
    const empty = path.join(root, "target/empty-libs");
    fs.mkdirSync(empty, { recursive: true });
    fs.writeFileSync(path.join(empty, "libdl.a"), "!<arch>\n");
    const name = `CARGO_TARGET_${target.toUpperCase().replaceAll("-", "_")}_RUSTFLAGS`;
    sh("cargo-zigbuild", ["zigbuild", ...profile], {
      env: { ...process.env, [name]: `-C linker=rust-lld -C linker-flavor=ld.lld -C link-self-contained=yes -L native=${empty}` },
    });
  }
  return path.join(root, "target", target, "dist", "her");
}

function pack(name, files) {
  const dir = path.join(work, name);
  fs.rmSync(dir, { recursive: true, force: true });
  fs.mkdirSync(dir, { recursive: true });
  for (const [source, as, mode] of files) {
    fs.copyFileSync(source, path.join(dir, as));
    fs.chmodSync(path.join(dir, as), mode);
  }
  const archive = path.join(out, `${name}.tar.gz`);
  // No macOS attributes in the archive: GNU tar on Linux warns about each.
  sh("tar", ["--no-xattrs", "--no-mac-metadata", "-czf", archive, "-C", dir, ...fs.readdirSync(dir)], { env: { ...process.env, COPYFILE_DISABLE: "1" } });
  return archive;
}

const generated = path.join(work, "THIRD_PARTY_NOTICES.md");
fs.writeFileSync(generated, notices(root));
const license = path.join(root, "LICENSE");
const readme = path.join(root, "README.md");

function pdfLinux(target) {
  const gnu = GLIBC[target];
  sh("cargo-zigbuild", ["zigbuild", "--profile", "dist", "--locked", "-p", "her-docs", "--bin", "her-doc-worker", "--target", gnu]);
  const worker = path.join(root, "target", gnu.replace(/\.[0-9.]+$/, ""), "dist", "her-doc-worker");
  const dir = path.join(cache, "pdfium-linux", target);
  if (!fs.existsSync(path.join(dir, "lib/libpdfium.so"))) {
    fs.mkdirSync(dir, { recursive: true });
    const url = `https://github.com/bblanchon/pdfium-binaries/releases/download/${PDFIUM_LINUX.tag}/${PDFIUM_LINUX.archive[target]}`;
    sh("sh", ["-c", `curl -fsSL "${url}" | tar -xz -C "${dir}"`]);
  }
  return [[worker, "her-doc-worker", 0o755], [path.join(dir, "lib/libpdfium.so"), "libpdfium.so", 0o644]];
}

const artifacts = {};
const addons = { pdf: {}, computer: {} };
for (const target of targets) {
  step(`build her for ${target}`);
  const main = buildMain(target);
  if (IS_MAC(target)) {
    codesign(main, "her");
    notary(main);
  }
  const files = [[main, "her", 0o755], [generated, "THIRD_PARTY_NOTICES.md", 0o644], [readme, "README.md", 0o644]];
  if (fs.existsSync(license)) files.push([license, "LICENSE", 0o644]);
  artifacts[target] = pack(`her-${version}-${target}`, files);
  if (opts["skip-components"]) continue;
  step(`components for ${target}`);
  if (IS_MAC(target)) {
    const arch = ARCH[target];
    const library = path.join(work, `${arch}-${pdfiumLibrary}`);
    fs.copyFileSync(pdfiumLibraryFor(arch, cache, root), library);
    fs.chmodSync(library, 0o755);
    codesign(library, "libpdfium");
    addons.pdf[target] = pack(`her-pdf-${version}-${target}`, [[library, pdfiumLibrary, 0o755]]);
    const binaries = agentDesktopBinaries(arch, cache);
    const dir = path.join(work, `computer-${arch}`);
    fs.mkdirSync(dir, { recursive: true });
    const signed = agentDesktopFiles.map((name) => {
      const copy = path.join(dir, name);
      fs.copyFileSync(binaries[name], copy);
      fs.chmodSync(copy, 0o755);
      codesign(copy, name);
      return [copy, name, 0o755];
    });
    addons.computer[target] = pack(`her-computer-${version}-${target}`, signed);
  } else {
    addons.pdf[target] = pack(`her-pdf-${version}-${target}`, pdfLinux(target));
  }
}

// ---- 4. Does it run?
for (const target of targets) {
  const dir = fs.mkdtempSync(path.join(work, "smoke-"));
  sh("tar", ["-xzf", artifacts[target], "-C", dir]);
  const her = path.join(dir, "her");
  const native = (process.platform === "darwin" && IS_MAC(target) && target.startsWith(process.arch === "arm64" ? "aarch64" : "x86_64"));
  if (native) {
    step(`smoke test ${target}`);
    const home = fs.mkdtempSync(path.join(work, "home-"));
    const env = { ...process.env, HER_HOME: home };
    if (run(her, ["--version"], { env }).trim() !== `her ${version}`) fail(`${target}: --version does not say ${version}`);
    JSON.parse(run(her, ["doctor", "--json"], { env, stdio: ["ignore", "pipe", "ignore"] }));
  } else if (opts["linux-host"] && !IS_MAC(target) && target.startsWith("x86_64")) {
    step(`smoke test ${target} on ${opts["linux-host"]}`);
    const remote = `/tmp/her-smoke-${version}`;
    sh("ssh", [opts["linux-host"], `mkdir -p ${remote}`]);
    sh("scp", ["-q", her, `${opts["linux-host"]}:${remote}/her`]);
    const said = run("ssh", [opts["linux-host"], `HER_HOME=${remote}/home ${remote}/her --version && HER_HOME=${remote}/home ${remote}/her doctor --json >/dev/null; rm -rf ${remote}`]);
    if (!said.includes(version)) fail(`${target}: ${said}`);
  } else {
    ready(false, `${target} was not run here: run it on such a system before publishing`);
  }
}

// ---- 5. The lists and the signature.
step("manifest and signature");
const entry = (file) => ({ url: `${RELEASES}/download/${tag}/${path.basename(file)}`, sha256: sha256(file), size: fs.statSync(file).size });
const protocolVersion = JSON.parse(fs.readFileSync(path.join(root, "crates/her-protocol/schema.json"), "utf8"))["x-protocol-version"];
const manifest = {
  version,
  protocol_version: protocolVersion,
  published_at: new Date().toISOString().replace(/\.\d+Z$/, "Z"),
  artifacts: Object.fromEntries(Object.entries(artifacts).map(([target, file]) => [target, entry(file)])),
  addons: Object.fromEntries(Object.entries(addons).filter(([, byTarget]) => Object.keys(byTarget).length).map(([name, byTarget]) => [name, Object.fromEntries(Object.entries(byTarget).map(([target, file]) => [target, entry(file)]))])),
};
const manifestFile = path.join(out, "her-manifest.json");
fs.writeFileSync(manifestFile, JSON.stringify(manifest, null, 2) + "\n");
sh("cargo", ["build", "-q", "--locked", "-p", "her-core", "--example", "release_sign"]);
const signTool = path.join(root, "target/debug/examples/release_sign");
fs.writeFileSync(`${manifestFile}.sig`, execFileSync(signTool, ["sign", manifestFile], { input: seed, encoding: "utf8" }));
const builtIn = fs.readFileSync(path.join(root, "crates/her-core/src/release_public_key.txt"), "utf8").trim();
if (execFileSync(signTool, ["public"], { input: seed, encoding: "utf8" }).trim() !== builtIn) fail("the key in the keychain is not the one built into her");
fs.copyFileSync(path.join(root, "tools/release/install.sh"), path.join(out, "install.sh"));
const sums = fs.readdirSync(out).filter((name) => name !== "SHA256SUMS").sort().map((name) => `${sha256(path.join(out, name))}  ${name}`);
fs.writeFileSync(path.join(out, "SHA256SUMS"), sums.join("\n") + "\n");

// What the other channels need, generated here so that they cannot disagree with the release.
const macs = { arm: artifacts["aarch64-apple-darwin"], intel: artifacts["x86_64-apple-darwin"] };
const linux = { arm: artifacts["aarch64-unknown-linux-musl"], intel: artifacts["x86_64-unknown-linux-musl"] };
const block = (file) => (file ? `url "${entry(file).url}"\n      sha256 "${sha256(file)}"` : `odie "no build for this system in ${version}"`);
const channels = path.join(root, "target/release-cli", `${version}-channels`);
fs.mkdirSync(channels, { recursive: true });
fs.writeFileSync(path.join(channels, "her.rb"), `class Her < Formula
  desc "AI agent for your terminal, your editor and your programs"
  homepage "https://github.com/${REPO}"
  version "${version}"

  on_macos do
    on_arm do
      ${block(macs.arm)}
    end
    on_intel do
      ${block(macs.intel)}
    end
  end
  on_linux do
    on_arm do
      ${block(linux.arm)}
    end
    on_intel do
      ${block(linux.intel)}
    end
  end

  def install
    bin.install "her"
    generate_completions_from_executable(bin/"her", "completion")
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/her --version")
  end
end
`);
fs.writeFileSync(path.join(channels, "agent.json"), JSON.stringify({
  id: "her",
  name: "her",
  version,
  description: "An AI agent for your terminal, your editor and your programs",
  repository: `https://github.com/${REPO}`,
  license_url: `${RELEASES}/download/${tag}/LICENSE`,
  distribution: {
    binary: Object.fromEntries(Object.entries(artifacts).map(([target, file]) => [REGISTRY[target], { archive: entry(file).url, cmd: "./her", args: ["acp"], sha256: sha256(file) }])),
  },
}, null, 2) + "\n");

console.log(`\nfiles in ${out}:`);
for (const name of fs.readdirSync(out).sort()) console.log(`  ${name}  ${(fs.statSync(path.join(out, name)).size / 1048576).toFixed(1)} MB`);
if (dry) {
  console.log(`\ndry run: nothing was published. Homebrew formula and registry entry are in ${channels}`);
  fs.rmSync(work, { recursive: true, force: true });
  process.exit(0);
}

// ---- 6. Up in one step.
step(`release ${tag}`);
const prerelease = version.includes("-");
sh("gh", ["release", "create", tag, "--repo", REPO, "--draft", "--title", tag, "--notes-file", notes, ...(prerelease ? ["--prerelease"] : []), ...fs.readdirSync(out).map((name) => path.join(out, name))]);
sh("gh", ["release", "edit", tag, "--repo", REPO, "--draft=false", ...(prerelease ? [] : ["--latest"])]);

// ---- 7. What follows.
fs.copyFileSync(path.join(root, "crates/her-protocol/schema.json"), path.join(root, "crates/her-protocol/schema-released.json"));
console.log(`\nreleased ${tag}. Still to do by hand:
  - commit crates/her-protocol/schema-released.json, the promise the next release is held to
  - push ${path.join(channels, "her.rb")} to ${TAP} as Formula/her.rb
  - first release only: open a pull request to the ACP registry with ${path.join(channels, "agent.json")}`);
fs.rmSync(work, { recursive: true, force: true });
