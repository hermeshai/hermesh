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
//   Contents/Resources/console                      Her's console (her/apps/console), which hermeshd serves
//   Contents/Resources/chrome-extension             the Hermesh extension (her/apps/chrome-extension), which hermeshd copies where
//                                                     the console's guide has the owner load it from
//   Contents/Resources/model-connection-licenses    notices of her's model connections (her/crates/her-connections/licenses)
//   Contents/Resources/third-party-licenses         licences of her's deck engine and fonts, and of PDFium
//   Contents/Helpers/hermeshd                       the Go daemon, a LaunchAgent
//   Contents/Helpers/hermesh                        the CLI (including agent login)
//   Contents/Helpers/her                            Her's kernel (her/), which hermeshd runs from beside itself (docs/her-server.md §6.4)
//   Contents/Helpers/agent-desktop                  desktop control, which her's desktop tools find beside her,
//   Contents/Helpers/agent-desktop-macos-helper       and its helper (scripts/agent-desktop.mjs: a pinned release, checked)
//   Contents/Helpers/hermesh-screen                 what Computer Control shows this Mac's screen with (apps/screen/macos), which
//                                                     hermeshd runs from beside itself (docs/computer-control.md §4.1)
//   Contents/Helpers/Hermesh Signer.app             the native signer
//   Contents/Frameworks/libpdfium.dylib             PDFium, which her's document worker finds there (scripts/pdfium.mjs: a pinned release, checked)
//   Contents/Library/LaunchAgents/com.hermesh.app.daemon.plist
//
// Needs Go, Rust (rustup), Node, Xcode and python3 (for dmgbuild, kept in node_modules/.cache).
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
import { agentDesktopBinaries, agentDesktopFiles, agentDesktopRelease } from "./agent-desktop.mjs";
import { pdfiumFor, pdfiumLibrary, pdfiumRelease } from "./pdfium.mjs";
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

// her as her's own releases build it (her/tools/release): the dist profile, and without
// local-accounts, which borrows other apps' sign-ins on this computer.
const archs = arch === "universal" ? ["arm64", "x64"] : [arch];
const triples = { arm64: "aarch64-apple-darwin", x64: "x86_64-apple-darwin" };
const herRoot = path.join(root, "her");
step(`building her (${arch})`);
sh("rustup", ["target", "add", ...archs.map((a) => triples[a])], { cwd: herRoot });
for (const a of archs) {
  sh("cargo", ["build", "--profile", "dist", "--locked", "-p", "her-cli", "--bin", "her", "--no-default-features", "--target", triples[a]], { cwd: herRoot });
}
fat(archs.map((a) => path.join(herRoot, "target", triples[a], "dist/her")), path.join(tmp, "her"));

step(`fetching PDFium ${pdfiumRelease.tag} and agent-desktop ${agentDesktopRelease.version}`);
const cache = path.join(desktop, "node_modules/.cache");
const pdfium = archs.map((a) => pdfiumFor(a, path.join(cache, "pdfium")));
fat(pdfium.map((dir) => path.join(dir, "lib", pdfiumLibrary)), path.join(tmp, pdfiumLibrary));
const desktopTools = archs.map((a) => agentDesktopBinaries(a, path.join(cache, "agent-desktop")));
for (const name of agentDesktopFiles) fat(desktopTools.map((files) => files[name]), path.join(tmp, name));

// Her's console (docs/her-server.md §6.3), until it is written: the app then ships without it.
const consoleApp = path.join(herRoot, "apps/console");
const hasConsole = fs.existsSync(path.join(consoleApp, "package.json"));
if (hasConsole) {
  step("building Her's console");
  sh("pnpm", ["--dir", consoleApp, "build"]);
} else {
  console.warn("⚠️  her/apps/console is not there yet: this app has no Her console (hermeshd says it is not installed).");
}

step(`building hermesh-screen (${arch})`);
const screenRoot = path.join(root, "apps/screen/macos");
const swiftArchs = archs.flatMap((a) => ["--arch", a === "x64" ? "x86_64" : "arm64"]);
sh("swift", ["build", "-c", "release", "--product", "hermesh-screen", ...swiftArchs], { cwd: screenRoot });
fs.copyFileSync(path.join(out("swift", ["build", "-c", "release", ...swiftArchs, "--show-bin-path", "--package-path", screenRoot]), "hermesh-screen"), path.join(tmp, "hermesh-screen"));

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

step("adding hermeshd, her, the signer and the LaunchAgent");
fs.mkdirSync(path.join(contents, "Helpers"), { recursive: true });
fs.mkdirSync(path.join(contents, "Library/LaunchAgents"), { recursive: true });
// Programs go in Helpers and the library in Frameworks, where her looks after its own folder
// (her-docs); what is not code goes in Resources, since codesign takes every file in those for code.
const helpers = ["hermeshd", "hermesh", "her", "hermesh-screen", ...agentDesktopFiles];
for (const name of helpers) {
  fs.copyFileSync(path.join(tmp, name), path.join(contents, "Helpers", name));
  fs.chmodSync(path.join(contents, "Helpers", name), 0o755);
}
const library = path.join(contents, "Frameworks", pdfiumLibrary);
fs.copyFileSync(path.join(tmp, pdfiumLibrary), library);
fs.chmodSync(library, 0o755);
if (hasConsole) fs.cpSync(path.join(consoleApp, "dist"), path.join(contents, "Resources/console"), { recursive: true });
// What loads, as the store's zip has it (her/apps/chrome-extension/scripts/pack.mjs), but keeping the
// manifest's "key": unpacked, the extension then has the ID her lets start the host.
const extension = path.join(herRoot, "apps/chrome-extension");
fs.mkdirSync(path.join(contents, "Resources/chrome-extension"));
fs.copyFileSync(path.join(extension, "manifest.json"), path.join(contents, "Resources/chrome-extension/manifest.json"));
for (const dir of ["src", "icons", "_locales"]) {
  fs.cpSync(path.join(extension, dir), path.join(contents, "Resources/chrome-extension", dir), {
    recursive: true,
    filter: (f) => !f.endsWith(".test.js") && !f.endsWith(".d.ts") && path.basename(f) !== ".DS_Store",
  });
}
fs.cpSync(path.join(herRoot, "crates/her-connections/licenses"), path.join(contents, "Resources/model-connection-licenses"), { recursive: true });
const licenses = path.join(contents, "Resources/third-party-licenses");
fs.cpSync(path.join(herRoot, "crates/her-slide/licenses"), licenses, { recursive: true });
// her carries Carlito and Caladea inside it (her-slide's fonts.rs), and their licence with them.
for (const name of ["OFL-Carlito.txt", "OFL-Caladea.txt"]) fs.copyFileSync(path.join(herRoot, "crates/her-slide/fonts", name), path.join(licenses, name));
fs.cpSync(path.join(pdfium[0], "licenses"), path.join(licenses, "PDFium"), { recursive: true });
fs.copyFileSync(path.join(pdfium[0], "LICENSE"), path.join(licenses, "PDFium/pdfium-binaries.txt"));
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
// Nor do her, agent-desktop and hermesh-screen: they read and write the user's files, start processes and open
// connections, which the hardened runtime allows, and the accessibility API and screen capture are
// TCC permissions. her loads PDFium, signed below with the bundle, which the hardened runtime's
// library validation lets in only from the same team ID: an ad-hoc her goes without it.
const timestamp = identity === "-" ? "--timestamp=none" : "--timestamp";
for (const name of helpers) {
  const runtime = name === "her" && identity === "-" ? [] : ["--options", "runtime"];
  sh("codesign", ["--force", ...runtime, timestamp, "--sign", identity, path.join(contents, "Helpers", name)]);
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
for (const file of [...helpers.map((name) => path.join(contents, "Helpers", name)), library]) sh("codesign", ["--verify", "--strict", file]);
// As signed, where this Mac can run them: her opens a PDF with the bundle's PDFium (no HER_PDFIUM,
// a home without one), and agent-desktop answers.
if (archs.includes(process.arch)) {
  const home = fs.mkdtempSync(path.join(tmp, "home-"));
  const { HER_PDFIUM: _, ...env } = { ...process.env, HOME: home, HER_HOME: path.join(home, ".her") };
  const her = path.join(contents, "Helpers/her");
  step(`checking ${execFileSync(her, ["--version"], { env, encoding: "utf8" }).trim()}, PDFium and agent-desktop`);
  fs.writeFileSync(path.join(home, "check.pdf"), minimalPdf());
  const opened = JSON.parse(execFileSync(her, ["doc-worker"], { input: JSON.stringify({ op: "open", handle: 1, path: path.join(home, "check.pdf") }) + "\n", env, cwd: home, encoding: "utf8" }).split("\n")[0]);
  if (opened.ok?.pages !== 1) throw new Error(`the bundled her cannot open a PDF: ${JSON.stringify(opened)}`);
  const said = JSON.parse(out(path.join(contents, "Helpers/agent-desktop"), ["version"]).split("\n").pop());
  if (said.data?.version !== agentDesktopRelease.version) throw new Error(`the bundled agent-desktop says ${JSON.stringify(said)}, not ${agentDesktopRelease.version}`);
}

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

/** `files`, one per architecture of this build in `archs`' order, as one Mach-O file `to` holding exactly those. */
function fat(files, to) {
  sh("lipo", ["-create", "-output", to, ...files]);
  const want = archs.map((a) => ({ arm64: "arm64", x64: "x86_64" })[a]).sort().join(" ");
  const got = out("lipo", ["-archs", to]).split(/\s+/).sort().join(" ");
  if (got !== want) throw new Error(`${to} holds ${got}, not ${want}`);
}

/** A one-page PDF, for checking the bundle's PDFium. */
function minimalPdf() {
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
