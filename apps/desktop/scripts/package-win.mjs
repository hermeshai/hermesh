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
// The app copies bin's first three where they stay across updates, on PATH (main/platform/win.ts).
//
// Needs Go and Node, on any system; the installer needs Windows.
//
// Environment:
//   VERSION                     version (default: package.json's)
//   HERMESH_ARCH                x64 (default) or arm64
//   HERMESH_DIST                where to put it all (default: dist/)
//   HERMESH_RELEASE_URL         where the release's files are downloaded from, for the feed
//   RELEASE_SIGNING_KEY_FILE    the release key (ECDSA P-256, PEM), to sign the feed
//   HERMESH_UPDATE_FEED         test builds only: where the app looks for updates (default: the latest GitHub release)
//   HERMESH_RELEASE_PUBKEY_FILE test builds only: the key the app checks the feed with (default: scripts/release-pubkey.pem)
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
import { windowsIcons } from "./windows-icons.mjs";

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
console.log(`✓ ${layout}`);

if (onWindows) {
  step("making the installer");
  // pnpm runs no install scripts of dependencies: electron-winstaller's picks this computer's 7-Zip.
  const vendor = path.join(desktop, "node_modules/electron-winstaller/vendor");
  for (const ext of ["exe", "dll"]) fs.copyFileSync(path.join(vendor, `7z-${process.arch}.${ext}`), path.join(vendor, `7z.${ext}`));
  const out = path.join(dist, `squirrel-${arch}`);
  fs.rmSync(out, { recursive: true, force: true });
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
