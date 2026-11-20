#!/usr/bin/env node
// Builds the Windows app (docs/windows.md §6.1) into dist/:
//   dist/Hermesh-windows-<arch>/              the app's files, as Setup.exe installs them
//   dist/Hermesh-windows-<arch>-setup.exe     on Windows: the installer (Squirrel.Windows), per user, unsigned
//   dist/Hermesh-windows-<arch>.nupkg         on Windows: the update, Squirrel's package
//   dist/RELEASES-win32-<arch>.json           on Windows: the feed naming it, and its hash (main/updater.ts),
//   dist/RELEASES-win32-<arch>.json.sig         signed with the release key when given one
//   dist/Hermesh.ico                          on Windows: the icon Installed apps shows, a release file
//
// The app:
//   Hermesh.exe, resources\app.asar    Electron: main, preload and the page
//   bin\hermeshd.exe                   console, every command
//   bin\hermeshdw.exe                  windowless; the daemon's logon task
//   bin\hermesh.exe                    the agents' CLI
//   bin\hermesh-signer.exe             copied into Program Files when this PC becomes a trusted device
//   bin\her.exe                        Her's kernel (her/), which hermeshd runs from beside itself (docs/her-server.md §6.4)
//   bin\her-console\                   Her's console (her/apps/console), which hermeshd serves
//   bin\agent-desktop.exe              desktop control, which her's desktop tools find beside her (scripts/agent-desktop.mjs)
//   bin\pdfium.dll                     PDFium, which her's document worker finds beside her (scripts/pdfium.mjs)
//   resources\model-connection-licenses, resources\third-party-licenses   notices, as in the Mac app
// The app copies bin's first three, and her with what is hers, where they stay across updates, on PATH
// (main/platform/win.ts). her carries the Hermesh extension itself, and writes it where hermeshd says.
//
// Needs Go and Node, on any system; her and the installer need Windows (Rust builds for Windows there),
// so the files made elsewhere are an app without Her.
//
// Environment:
//   VERSION                     version (default: package.json's)
//   HERMESH_ARCH                x64 (default) or arm64
//   HERMESH_DIST                where to put it all (default: dist/)
//   HERMESH_RELEASE_URL         where the release's files are downloaded from, for the feed
//   RELEASE_SIGNING_KEY_FILE    the release key (ECDSA P-256, PEM), to sign the feed
//   HERMESH_UPDATE_FEED         test builds only: where the app looks for updates (default: the latest GitHub release)
//   HERMESH_RELEASE_PUBKEY_FILE test builds only: the key the app checks the feed with (default: scripts/release-pubkey.pem)
//   HER                         her.exe for Windows, built beforehand (optional; built from her/ when unset)
//   HERMESH_HER_PROFILE         test builds only: the cargo profile her is built with (default: dist, as her's releases)
//   HER_GITHUB_OAUTH_CLIENT_SECRET   the secret of her's GitHub OAuth app, built into her, as for the Mac app (build-app.mjs)
import { packager } from "@electron/packager";
import { flipFuses, FuseV1Options, FuseVersion } from "@electron/fuses";
import { execFileSync } from "node:child_process";
import { createHash, sign } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { createWindowsInstaller } from "electron-winstaller";
import { releaseCatalog } from "../src/shared/update.ts";
import enNotes from "../src/locales/en/releases.json" with { type: "json" };
import zhNotes from "../src/locales/zh-CN/releases.json" with { type: "json" };
import { agentDesktopBinaries, agentDesktopRelease } from "./agent-desktop.mjs";
import { minimalPdf, pdfiumFor, pdfiumLibraryIn, pdfiumRelease } from "./pdfium.mjs";
import { windowsIcons } from "./windows-icons.mjs";
import { installSplash } from "./windows-splash.mjs";

const desktop = path.resolve(import.meta.dirname, "..");
const root = path.resolve(desktop, "../..");
const dist = path.resolve(process.env.HERMESH_DIST || path.join(root, "dist"));
const tmp = path.join(dist, "tmp-win");
const pkg = JSON.parse(fs.readFileSync(path.join(desktop, "package.json"), "utf8"));
const version = process.env.VERSION || pkg.version;
const electronVersion = JSON.parse(fs.readFileSync(path.join(desktop, "node_modules/electron/package.json"), "utf8")).version;
const arch = process.env.HERMESH_ARCH || "x64";
if (!["x64", "arm64"].includes(arch)) throw new Error(`HERMESH_ARCH: ${arch}`);
const onWindows = process.platform === "win32";

const sh = (cmd, args, opts = {}) => execFileSync(cmd, args, { stdio: "inherit", ...opts });
const step = (s) => console.log(`› ${s}`);

fs.rmSync(tmp, { recursive: true, force: true });
fs.mkdirSync(tmp, { recursive: true });

step("building the page and the main process");
const staged = path.join(tmp, "app");
sh("pnpm", ["--dir", desktop, "build"], { env: { ...process.env, VERSION: version, HERMESH_DESKTOP_OUT: staged }, shell: onWindows });
fs.writeFileSync(
  path.join(staged, "package.json"),
  JSON.stringify({ name: "hermesh", productName: "Hermesh", version, main: "main/index.js", type: "module" }, null, 2),
);

step(`building hermeshd, hermeshdw, hermesh and hermesh-signer (${arch})`);
const bin = path.join(tmp, "bin");
const ldflags = `-s -w -X github.com/hermeshai/hermesh/internal/machine.Version=${version}`;
const goEnv = { ...process.env, GOOS: "windows", GOARCH: arch === "x64" ? "amd64" : "arm64", CGO_ENABLED: "0" };
for (const [name, cmd, flags] of [
  ["hermeshd.exe", "./cmd/hermeshd", ldflags],
  ["hermeshdw.exe", "./cmd/hermeshd", `${ldflags} -H windowsgui`],
  ["hermesh.exe", "./cmd/hermesh", ldflags],
  ["hermesh-signer.exe", "./cmd/hermesh-signer", `-s -w -X main.version=${version} -H windowsgui`],
]) {
  sh("go", ["build", "-trimpath", "-ldflags", flags, "-o", path.join(bin, name), cmd], { cwd: root, env: goEnv });
}

// her as her's own releases build it (her/tools/release): the dist profile, and without
// local-accounts, which borrows other apps' sign-ins on this computer.
const herRoot = path.join(root, "her");
const cache = path.join(desktop, "node_modules/.cache");
let pdfium = null;
if (onWindows) {
  const triple = { x64: "x86_64-pc-windows-msvc", arm64: "aarch64-pc-windows-msvc" }[arch];
  const profile = process.env.HERMESH_HER_PROFILE || "dist";
  if (process.env.HER) {
    step(`using prebuilt her (${arch})`);
    fs.copyFileSync(path.resolve(process.env.HER), path.join(bin, "her.exe"));
  } else {
    step(`building her (${arch})`);
    const githubSecret = process.env.HER_GITHUB_OAUTH_CLIENT_SECRET || "";
    if (!githubSecret) console.warn("⚠️  no HER_GITHUB_OAUTH_CLIENT_SECRET: this her signs in to GitHub with a token only.");
    sh("rustup", ["target", "add", triple], { cwd: herRoot });
    sh("cargo", ["build", "--profile", profile, "--locked", "-p", "her-cli", "--bin", "her", "--no-default-features", "--target", triple], {
      cwd: herRoot,
      env: { ...process.env, HER_GITHUB_OAUTH_CLIENT_SECRET: githubSecret },
    });
    fs.copyFileSync(path.join(herRoot, "target", triple, profile, "her.exe"), path.join(bin, "her.exe"));
  }

  step(`fetching PDFium ${pdfiumRelease.tag} and agent-desktop ${agentDesktopRelease.version}`);
  pdfium = pdfiumFor(arch, path.join(cache, "pdfium"), pdfiumRelease, undefined, "win");
  fs.copyFileSync(path.join(pdfium, pdfiumLibraryIn.win), path.join(bin, "pdfium.dll"));
  for (const [name, file] of Object.entries(agentDesktopBinaries(arch, path.join(cache, "agent-desktop"), agentDesktopRelease, undefined, "win32"))) {
    fs.copyFileSync(file, path.join(bin, name));
  }

  step("building Her's console");
  sh("pnpm", ["--dir", path.join(herRoot, "apps/console"), "build"], { shell: true });
  fs.cpSync(path.join(herRoot, "apps/console/dist"), path.join(bin, "her-console"), { recursive: true });
} else {
  console.warn("⚠️  her is built on Windows only: these files are an app without Her.");
}

step("drawing the icons");
const layout = path.join(dist, `Hermesh-windows-${arch}`);
fs.rmSync(layout, { recursive: true, force: true });
const ico = path.join(tmp, "Hermesh.ico");
windowsIcons(ico);

step("packaging Hermesh.exe");
const [built] = await packager({
  dir: staged,
  out: tmp,
  name: "Hermesh",
  executableName: "Hermesh",
  electronVersion,
  platform: "win32",
  arch,
  appVersion: version,
  icon: ico,
  win32metadata: { CompanyName: "Hermesh", FileDescription: "Hermesh", ProductName: "Hermesh", InternalName: "Hermesh", OriginalFilename: "Hermesh.exe" },
  asar: true,
  prune: false,
  overwrite: true,
});
fs.renameSync(built, layout);
// Chromium's own words: only the app's languages stay.
for (const f of fs.readdirSync(path.join(layout, "locales"))) if (!["en-US.pak", "zh-CN.pak"].includes(f)) fs.rmSync(path.join(layout, "locales", f));

step("flipping Electron fuses");
// As on the Mac (packager embeds the asar's digest in Hermesh.exe).
await flipFuses(path.join(layout, "Hermesh.exe"), {
  version: FuseVersion.V1,
  [FuseV1Options.RunAsNode]: false,
  [FuseV1Options.EnableCookieEncryption]: true,
  [FuseV1Options.EnableNodeOptionsEnvironmentVariable]: false,
  [FuseV1Options.EnableNodeCliInspectArguments]: false,
  [FuseV1Options.EnableEmbeddedAsarIntegrityValidation]: true,
  [FuseV1Options.OnlyLoadAppFromAsar]: true,
  [FuseV1Options.GrantFileProtocolExtraPrivileges]: false,
});

step("adding the programs");
fs.cpSync(bin, path.join(layout, "bin"), { recursive: true });
if (pdfium) {
  // The notices of what her carries, as in the Mac app (build-app.mjs).
  fs.cpSync(path.join(herRoot, "crates/her-connections/licenses"), path.join(layout, "resources/model-connection-licenses"), { recursive: true });
  const licenses = path.join(layout, "resources/third-party-licenses");
  fs.cpSync(path.join(herRoot, "crates/her-slide/licenses"), licenses, { recursive: true });
  for (const name of ["OFL-Carlito.txt", "OFL-Caladea.txt"]) fs.copyFileSync(path.join(herRoot, "crates/her-slide/fonts", name), path.join(licenses, name));
  fs.cpSync(path.join(pdfium, "licenses"), path.join(licenses, "PDFium"), { recursive: true });
  fs.copyFileSync(path.join(pdfium, "LICENSE"), path.join(licenses, "PDFium/pdfium-binaries.txt"));
  // As packaged, where this PC can run them: her opens a PDF with the app's PDFium (no HER_PDFIUM,
  // a home without one), and agent-desktop answers.
  if (arch === process.arch) {
    const home = fs.mkdtempSync(path.join(tmp, "home-"));
    const { HER_PDFIUM: _, ...env } = { ...process.env, USERPROFILE: home, HER_HOME: path.join(home, ".her") };
    const her = path.join(layout, "bin/her.exe");
    step(`checking ${execFileSync(her, ["--version"], { env, encoding: "utf8" }).trim()}, PDFium and agent-desktop`);
    fs.writeFileSync(path.join(home, "check.pdf"), minimalPdf());
    const opened = JSON.parse(execFileSync(her, ["doc-worker"], { input: JSON.stringify({ op: "open", handle: 1, path: path.join(home, "check.pdf") }) + "\n", env, cwd: home, encoding: "utf8" }).split("\n")[0]);
    if (opened.ok?.pages !== 1) throw new Error(`the packaged her cannot open a PDF: ${JSON.stringify(opened)}`);
    const said = execFileSync(path.join(layout, "bin/agent-desktop.exe"), ["--version"], { encoding: "utf8" }).trim();
    if (said !== `agent-desktop ${agentDesktopRelease.version}`) throw new Error(`the packaged agent-desktop says ${JSON.stringify(said)}, not ${agentDesktopRelease.version}`);
  }
}
console.log(`✓ ${layout}`);

if (onWindows) {
  step("making the installer");
  // pnpm runs no install scripts of dependencies: electron-winstaller's picks this computer's 7-Zip.
  const vendor = path.join(desktop, "node_modules/electron-winstaller/vendor");
  for (const ext of ["exe", "dll"]) fs.copyFileSync(path.join(vendor, `7z-${process.arch}.${ext}`), path.join(vendor, `7z.${ext}`));
  const out = path.join(dist, `squirrel-${arch}`);
  fs.rmSync(out, { recursive: true, force: true });
  const splash = path.join(tmp, "installing.gif");
  installSplash(splash);
  await createWindowsInstaller({
    appDirectory: layout,
    outputDirectory: out,
    // Squirrel installs into %LOCALAPPDATA%\<name> and removes that folder on uninstall:
    // not Hermesh, the data folder's name.
    name: "HermeshApp",
    title: "Hermesh",
    authors: "Hermesh",
    description: "Hermesh",
    exe: "Hermesh.exe",
    version,
    setupIcon: ico,
    // What Setup.exe shows while it installs (Squirrel's own is an old editor's).
    loadingGif: splash,
    // Apps › Installed apps shows it; Squirrel downloads it while installing (the release carries it).
    iconUrl: "https://github.com/hermeshai/hermesh/releases/latest/download/Hermesh.ico",
    // The template takes Electron's files only.
    additionalFiles: [{ src: "bin\\**", target: "lib\\net45\\bin" }],
    noMsi: true,
    noDelta: true,
  });
  const setup = path.join(dist, `Hermesh-windows-${arch}-setup.exe`);
  fs.renameSync(path.join(out, "HermeshSetup.exe"), setup);
  fs.copyFileSync(ico, path.join(dist, "Hermesh.ico"));
  console.log(`✓ ${setup}`);

  step("the update and its feed");
  // RELEASES: one line, "<SHA-1> <package> <size>", which the app writes beside the package it downloads.
  const releases = fs.readFileSync(path.join(out, "RELEASES"), "utf8").replace(/^\uFEFF/, "").trim();
  const nupkg = path.join(dist, `Hermesh-windows-${arch}.nupkg`);
  fs.copyFileSync(path.join(out, releases.split(/\s+/)[1]), nupkg);
  const releaseURL = process.env.HERMESH_RELEASE_URL || `https://github.com/hermeshai/hermesh/releases/download/v${version}`;
  const feed = path.join(dist, `RELEASES-win32-${arch}.json`);
  fs.writeFileSync(feed, JSON.stringify({
    currentRelease: version,
    hermeshNotes: releaseCatalog(enNotes, zhNotes, version),
    package: { url: `${releaseURL}/${path.basename(nupkg)}`, sha256: createHash("sha256").update(fs.readFileSync(nupkg)).digest("hex"), releases },
  }, null, 2));
  if (process.env.RELEASE_SIGNING_KEY_FILE) fs.writeFileSync(`${feed}.sig`, sign("sha256", fs.readFileSync(feed), fs.readFileSync(process.env.RELEASE_SIGNING_KEY_FILE, "utf8")));
  console.log(`✓ ${nupkg}\n✓ ${feed}`);
}
