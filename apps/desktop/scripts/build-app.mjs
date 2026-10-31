#!/usr/bin/env node
// Builds, signs and (optionally) notarizes Hermesh.app into dist/:
//   dist/Hermesh.app
//   dist/Hermesh-macos-<arch>.dmg   what people download: drag Hermesh to Applications (docs/desktop-app.md §6.7)
//   dist/Hermesh-macos-<arch>.zip   what installed apps update from (Squirrel.Mac takes zips only), and for
//   arm64 and x64 dist/RELEASES-darwin-<arch>.json, the update feed naming it (docs/desktop-app.md §6.5)
//
// The bundle (docs/desktop-app.md §1):
//   Contents/MacOS/Hermesh                          Electron (this app)
//   Contents/Resources/app.asar                     main, preload and the page
//   Contents/Helpers/hermeshd                       the Go daemon, a LaunchAgent
//   Contents/Helpers/hermesh                        the CLI (including agent login)
//   Contents/Helpers/Hermesh Signer.app             the native signer
//   Contents/Library/LaunchAgents/com.hermesh.app.daemon.plist
//
// Needs Go, Node, Xcode and python3 (for dmgbuild, kept in node_modules/.cache).
//
// Environment:
//   HERMESH_SIGNING_IDENTITY   signing identity; default the first "Developer ID Application", else ad-hoc
//   HERMESH_SIGNER_PROFILE     Developer ID provisioning profile for the signer (its keychain access group)
//   HERMESH_NOTARY_PROFILE     notarytool keychain profile, or
//   HERMESH_NOTARY_KEY_PATH / _KEY_ID / _ISSUER   an App Store Connect API key
//   VERSION                    version (default: package.json's)
//   HERMESH_ARCH               universal (default), arm64 or x64
//   HERMESH_DIST               where to put it all (default: dist/)
//   HERMESH_RELEASE_URL        where the release's zips are downloaded from, for the feed
//                              (default: the GitHub release v<VERSION>)
//   HERMESH_UPDATE_FEED        test builds only: where the app looks for updates (default: the latest GitHub release)
//   HERMESH_TEST_DATA_DIR      test builds only: the data directory (and so the daemon socket) baked into the app
import { packager } from "@electron/packager";
import { flipFuses, FuseV1Options, FuseVersion } from "@electron/fuses";
import { sign } from "@electron/osx-sign";
import { notarize } from "@electron/notarize";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { releaseCatalog, releaseFeed } from "../src/shared/update.ts";
import enNotes from "../src/locales/en/releases.json" with { type: "json" };
import zhNotes from "../src/locales/zh-CN/releases.json" with { type: "json" };

const desktop = path.resolve(import.meta.dirname, "..");
const root = path.resolve(desktop, "../..");
const dist = path.resolve(process.env.HERMESH_DIST || path.join(root, "dist"));
const tmp = path.join(dist, "tmp");
const pkg = JSON.parse(fs.readFileSync(path.join(desktop, "package.json"), "utf8"));
const version = process.env.VERSION || pkg.version;
const electronVersion = JSON.parse(fs.readFileSync(path.join(desktop, "node_modules/electron/package.json"), "utf8")).version;
const build = new Date().toISOString().replace(/\D/g, "").slice(0, 12);

const sh = (cmd, args, opts = {}) => execFileSync(cmd, args, { stdio: "inherit", ...opts });
const out = (cmd, args) => execFileSync(cmd, args, { encoding: "utf8" }).trim();
const step = (s) => console.log(`› ${s}`);

let identity = process.env.HERMESH_SIGNING_IDENTITY || "";
if (!identity) {
  const m = /"(Developer ID Application[^"]*)"/.exec(out("security", ["find-identity", "-v", "-p", "codesigning"]));
  identity = m ? m[1] : "-";
}

fs.rmSync(tmp, { recursive: true, force: true });
fs.mkdirSync(tmp, { recursive: true });

step("building the page and the main process");
const staged = path.join(tmp, "app");
sh("pnpm", ["--dir", desktop, "build"], { env: { ...process.env, VERSION: version, HERMESH_DESKTOP_OUT: staged } });
fs.writeFileSync(
  path.join(staged, "package.json"),
  JSON.stringify({ name: "hermesh", productName: "Hermesh", version, main: "main/index.js", type: "module" }, null, 2),
);

const arch = process.env.HERMESH_ARCH || "universal";
if (!["universal", "arm64", "x64"].includes(arch)) throw new Error(`HERMESH_ARCH: ${arch}`);
const goArchs = arch === "universal" ? ["arm64", "amd64"] : [arch === "x64" ? "amd64" : "arm64"];
for (const name of ["hermeshd", "hermesh"]) {
  step(`building ${name} (${arch})`);
  for (const arch of goArchs) {
    sh("go", ["build", "-trimpath", "-ldflags", `-s -w -X github.com/hermeshai/hermesh/internal/machine.Version=${version}`, "-o", path.join(tmp, `${name}-${arch}`), `./cmd/${name}`], {
      cwd: root,
      env: { ...process.env, GOOS: "darwin", GOARCH: arch, CGO_ENABLED: "0" },
    });
  }
  sh("lipo", ["-create", "-output", path.join(tmp, name), ...goArchs.map((a) => path.join(tmp, `${name}-${a}`))]);
}

step("building Hermesh Signer.app");
sh(path.join(root, "apps/signer/macos/scripts/build-signer.sh"), [tmp], { env: { ...process.env, HERMESH_SIGNING_IDENTITY: identity, VERSION: version } });
const signerApp = path.join(tmp, "Hermesh Signer.app");

step("compiling the app icon");
const icons = path.join(tmp, "icon");
fs.mkdirSync(icons);
sh("xcrun", ["actool", path.join(desktop, "build/AppIcon.icon"), "--compile", icons, "--platform", "macosx", "--minimum-deployment-target", "14.0",
  "--app-icon", "AppIcon", "--output-partial-info-plist", path.join(tmp, "AppIcon.plist")], { stdio: "ignore" });

step("packaging Hermesh.app");
const [built] = await packager({
  dir: staged,
  out: tmp,
  name: "Hermesh",
  executableName: "Hermesh",
  electronVersion,
  platform: "darwin",
  arch,
  appBundleId: "com.hermesh.app",
  helperBundleId: "com.hermesh.app.helper",
  appVersion: version,
  buildVersion: build,
  appCategoryType: "public.app-category.developer-tools",
  icon: path.join(icons, "AppIcon.icns"),
  asar: true,
  prune: false,
  overwrite: true,
  protocols: [{ name: "Hermesh link", schemes: ["hermesh"] }],
  // The app's languages (src/locales), which macOS offers in System Settings' per-app language.
  extendInfo: { CFBundleIconName: "AppIcon", LSMinimumSystemVersion: "14.0", NSHumanReadableCopyright: "Hermesh", CFBundleDevelopmentRegion: "en", CFBundleLocalizations: ["en", "zh_CN"] },
});
const app = path.join(dist, "Hermesh.app");
fs.rmSync(app, { recursive: true, force: true });
fs.renameSync(path.join(built, "Hermesh.app"), app);

const contents = path.join(app, "Contents");
// Electron brings a folder for each language Chromium speaks; macOS takes
// them for the app's languages, so only the app's own stay.
for (const f of fs.readdirSync(path.join(contents, "Resources"))) {
  if (f.endsWith(".lproj") && !["en.lproj", "zh_CN.lproj"].includes(f)) fs.rmSync(path.join(contents, "Resources", f), { recursive: true });
}

step("adding hermeshd, the signer and the LaunchAgent");
fs.mkdirSync(path.join(contents, "Helpers"), { recursive: true });
fs.mkdirSync(path.join(contents, "Library/LaunchAgents"), { recursive: true });
for (const name of ["hermeshd", "hermesh"]) {
  fs.copyFileSync(path.join(tmp, name), path.join(contents, "Helpers", name));
  fs.chmodSync(path.join(contents, "Helpers", name), 0o755);
}
sh("ditto", [signerApp, path.join(contents, "Helpers/Hermesh Signer.app")]);
fs.copyFileSync(path.join(desktop, "build/com.hermesh.app.daemon.plist"), path.join(contents, "Library/LaunchAgents/com.hermesh.app.daemon.plist"));
// A changed launch configuration makes ServiceManagement re-evaluate its
// cached launch constraint when replacing a development or older build.
sh("/usr/libexec/PlistBuddy", ["-c", "Add :EnvironmentVariables dict", "-c", `Add :EnvironmentVariables:HERMESH_APP_VERSION string ${version}`,
  path.join(contents, "Library/LaunchAgents/com.hermesh.app.daemon.plist")]);
fs.copyFileSync(path.join(icons, "Assets.car"), path.join(contents, "Resources/Assets.car"));

step("flipping Electron fuses");
await flipFuses(path.join(contents, "MacOS/Hermesh"), {
  version: FuseVersion.V1,
  [FuseV1Options.RunAsNode]: false,
  [FuseV1Options.EnableCookieEncryption]: true,
  [FuseV1Options.EnableNodeOptionsEnvironmentVariable]: false,
  [FuseV1Options.EnableNodeCliInspectArguments]: false,
  [FuseV1Options.EnableEmbeddedAsarIntegrityValidation]: true,
  [FuseV1Options.OnlyLoadAppFromAsar]: true,
  [FuseV1Options.GrantFileProtocolExtraPrivileges]: false,
});

step(`signing with: ${identity}`);
// hermeshd needs no entitlements; the signer was signed with its own (and its profile) when built.
const timestamp = identity === "-" ? "--timestamp=none" : "--timestamp";
for (const name of ["hermeshd", "hermesh"]) {
  sh("codesign", ["--force", "--options", "runtime", timestamp, "--sign", identity, path.join(contents, "Helpers", name)]);
}
await sign({
  app,
  identity,
  identityValidation: identity !== "-",
  platform: "darwin",
  ignore: (file) => file.includes("/Contents/Helpers/"),
  // The hardened runtime's library validation needs one team ID across the
  // bundle, which an ad-hoc signature does not have.
  optionsForFile: () => ({ hardenedRuntime: identity !== "-", entitlements: path.join(desktop, "build/app.entitlements") }),
});
sh("codesign", ["--verify", "--strict", "--deep", app]);

const auth = process.env.HERMESH_NOTARY_PROFILE
  ? { keychainProfile: process.env.HERMESH_NOTARY_PROFILE }
  : process.env.HERMESH_NOTARY_KEY_PATH
    ? { appleApiKey: process.env.HERMESH_NOTARY_KEY_PATH, appleApiKeyId: process.env.HERMESH_NOTARY_KEY_ID, appleApiIssuer: process.env.HERMESH_NOTARY_ISSUER }
    : null;
if (auth) {
  if (identity === "-") throw new Error("notarization needs a Developer ID signature, not ad-hoc");
  step("notarizing (usually a few minutes)");
  await notarize({ appPath: app, ...auth });
  sh("spctl", ["--assess", "--type", "execute", "--verbose=2", app]);
}

const zip = path.join(dist, `Hermesh-macos-${arch}.zip`);
fs.rmSync(zip, { force: true });
sh("ditto", ["-c", "-k", "--keepParent", app, zip]);

// The disk image holds the notarized, stapled app, and is itself signed,
// notarized and stapled: Apple notarizes the outermost container, and the
// ticket stapled to each lets Gatekeeper pass them offline.
step("making the disk image");
const dmg = path.join(dist, `Hermesh-macos-${arch}.dmg`);
fs.rmSync(dmg, { force: true });
sh(dmgbuild(), ["-s", path.join(desktop, "build/dmg/settings.py"), "-D", `app=${app}`, "-D", `icon=${path.join(icons, "AppIcon.icns")}`,
  "-D", `background=${path.join(desktop, "build/dmg/background.png")}`, "Hermesh", dmg]);
if (identity !== "-") sh("codesign", ["--sign", identity, "--timestamp", dmg]);
if (auth) {
  step("notarizing the disk image");
  await notarize({ appPath: dmg, ...auth });
  sh("spctl", ["--assess", "--type", "open", "--context", "context:primary-signature", "--verbose=2", dmg]);
}
console.log(`✓ ${app}\n✓ ${dmg}\n✓ ${zip}`);
if (arch !== "universal") {
  // The app reads the feed of its own architecture (main/updater.ts).
  const releaseURL = process.env.HERMESH_RELEASE_URL || `https://github.com/hermeshai/hermesh/releases/download/v${version}`;
  const feed = path.join(dist, `RELEASES-darwin-${arch}.json`);
  fs.writeFileSync(feed, JSON.stringify(releaseFeed({ version, url: `${releaseURL}/${path.basename(zip)}`, date: new Date(), notes: releaseCatalog(enNotes, zhNotes, version) }), null, 2));
  console.log(`✓ ${feed}`);
}
if (identity === "-") console.warn("⚠️  ad-hoc signature: the LaunchAgent (SMAppService) and Gatekeeper need a Developer ID signature.");
else if (!auth) console.warn("⚠️  signed but not notarized: others who download it will be blocked the first time they open it.");

/** dmgbuild (build/dmg/requirements.txt, pinned with hashes) in a virtual environment of its own, kept between builds. */
function dmgbuild() {
  const venv = path.join(desktop, "node_modules/.cache/dmgbuild");
  const requirements = fs.readFileSync(path.join(desktop, "build/dmg/requirements.txt"), "utf8");
  const installed = path.join(venv, "requirements.txt");
  if (!fs.existsSync(installed) || fs.readFileSync(installed, "utf8") !== requirements) {
    fs.rmSync(venv, { recursive: true, force: true });
    sh("python3", ["-m", "venv", venv]);
    sh(path.join(venv, "bin/pip"), ["install", "--quiet", "--disable-pip-version-check", "--require-hashes", "--only-binary", ":all:", "-r", path.join(desktop, "build/dmg/requirements.txt")]);
    fs.writeFileSync(installed, requirements);
  }
  return path.join(venv, "bin/dmgbuild");
}
