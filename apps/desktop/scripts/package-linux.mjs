#!/usr/bin/env node
// Builds the Linux app (docs/linux.md §7.2) into dist/:
//   dist/Hermesh-linux-<arch>/                the app's folder, as the packages install it in /opt/Hermesh
//   dist/Hermesh-linux-<arch>.tar.gz          the same folder, for other distributions
//   dist/Hermesh-linux-<arch>.deb             with dpkg-deb: Debian and Ubuntu
//   dist/Hermesh-linux-<arch>.rpm             with rpmbuild: Fedora, RHEL and openSUSE
//   dist/RELEASES-linux-<arch>.json           the feed the app updates from (main/updater.ts),
//   dist/RELEASES-linux-<arch>.json.sig         signed with the release key when given one
//
// The app's folder:
//   Hermesh, resources/app.asar, chrome-sandbox   Electron
//   bin/hermeshd, bin/hermesh                      the daemon, run as the user's systemd service, and the agents' CLI
//   bin/hermesh-signer                             the protected signer, a system service (cmd/hermesh-signer)
//   bin/her, bin/her-console/, bin/her-pdf/, bin/her-computer/, bin/hermesh-screen   Her, beside hermeshd as install.sh puts it
// The packages add /usr/bin/hermesh and /usr/bin/hermeshd, the desktop entry (which takes hermesh: links),
// the icon, the signer's systemd units and its polkit action (build/linux).
//
// Needs Go and Node; the packages need dpkg-deb and rpmbuild, so Linux.
//
// Environment:
//   VERSION                     version (default: package.json's)
//   HERMESH_ARCH                x64 (default) or arm64
//   HERMESH_DIST                where to put it all (default: dist/)
//   HER                         her for Linux, static (release.yml's her job)
//   HER_CONSOLE                 her console's built files (default: her/apps/console/dist)
//   HER_PDF                     her PDF component's folder: her-doc-worker and libpdfium.so (release.yml)
//   HER_COMPUTER                agent-desktop's folder (default: from the pinned npm package)
//   HERMESH_SCREEN              hermesh-screen for Linux, when there is one
//   HERMESH_RELEASE_URL         where the release's files are downloaded from, for the feed
//   RELEASE_SIGNING_KEY_FILE    the release key (ECDSA P-256, PEM), to sign the feed
import { packager } from "@electron/packager";
import { flipFuses, FuseV1Options, FuseVersion } from "@electron/fuses";
import { execFileSync } from "node:child_process";
import { createHash, sign } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { releaseCatalog } from "../src/shared/update.ts";
import enNotes from "../src/locales/en/releases.json" with { type: "json" };
import zhNotes from "../src/locales/zh-CN/releases.json" with { type: "json" };
import { agentDesktopBinaries, agentDesktopRelease } from "./agent-desktop.mjs";
import { appIconPNG } from "./windows-icons.mjs";

const desktop = path.resolve(import.meta.dirname, "..");
const root = path.resolve(desktop, "../..");
const dist = path.resolve(process.env.HERMESH_DIST || path.join(root, "dist"));
const tmp = path.join(dist, "tmp-linux");
const pkg = JSON.parse(fs.readFileSync(path.join(desktop, "package.json"), "utf8"));
const version = process.env.VERSION || pkg.version;
const electronVersion = JSON.parse(fs.readFileSync(path.join(desktop, "node_modules/electron/package.json"), "utf8")).version;
const arch = process.env.HERMESH_ARCH || "x64";
const names = { x64: { go: "amd64", deb: "amd64", rpm: "x86_64" }, arm64: { go: "arm64", deb: "arm64", rpm: "aarch64" } }[arch];
if (!names) throw new Error(`HERMESH_ARCH: ${arch}`);
const her = process.env.HER;
if (!her) throw new Error("HER: her for Linux, built beforehand");

const sh = (cmd, args, opts = {}) => execFileSync(cmd, args, { stdio: "inherit", ...opts });
const has = (cmd) => {
  try {
    execFileSync("sh", ["-c", `command -v ${cmd}`], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
};
const step = (s) => console.log(`› ${s}`);
const linux = path.join(desktop, "build/linux");

fs.rmSync(tmp, { recursive: true, force: true });
fs.mkdirSync(tmp, { recursive: true });

step("building the page and the main process");
const staged = path.join(tmp, "app");
sh("pnpm", ["--dir", desktop, "build"], { env: { ...process.env, VERSION: version, HERMESH_DESKTOP_OUT: staged } });
fs.writeFileSync(
  path.join(staged, "package.json"),
  JSON.stringify({ name: "hermesh", productName: "Hermesh", version, main: "main/index.js", type: "module" }, null, 2),
);

step(`building hermeshd, hermesh and hermesh-signer (${arch})`);
const bin = path.join(tmp, "bin");
const goEnv = { ...process.env, GOOS: "linux", GOARCH: names.go, CGO_ENABLED: "0" };
for (const [name, cmd, ldflags] of [
  ["hermeshd", "./cmd/hermeshd", `-s -w -X github.com/hermeshai/hermesh/internal/machine.Version=${version}`],
  ["hermesh", "./cmd/hermesh", `-s -w -X github.com/hermeshai/hermesh/internal/machine.Version=${version}`],
  ["hermesh-signer", "./cmd/hermesh-signer", `-s -w -X main.version=${version}`],
]) {
  sh("go", ["build", "-trimpath", "-ldflags", ldflags, "-o", path.join(bin, name), cmd], { cwd: root, env: goEnv });
}

step(`adding Her: her, its console, PDF component and agent-desktop ${agentDesktopRelease.version}`);
fs.copyFileSync(her, path.join(bin, "her"));
fs.chmodSync(path.join(bin, "her"), 0o755);
fs.cpSync(process.env.HER_CONSOLE || path.join(root, "her/apps/console/dist"), path.join(bin, "her-console"), { recursive: true });
if (process.env.HER_PDF) fs.cpSync(process.env.HER_PDF, path.join(bin, "her-pdf"), { recursive: true });
else console.warn("⚠️  no HER_PDF: this app's Her reads no PDFs.");
const computer = path.join(bin, "her-computer");
if (process.env.HER_COMPUTER) fs.cpSync(process.env.HER_COMPUTER, computer, { recursive: true });
else {
  fs.mkdirSync(computer, { recursive: true });
  const files = agentDesktopBinaries(arch, path.join(desktop, "node_modules/.cache/agent-desktop"), agentDesktopRelease, undefined, "linux");
  for (const [name, file] of Object.entries(files)) fs.copyFileSync(file, path.join(computer, name));
}
for (const f of fs.readdirSync(computer)) fs.chmodSync(path.join(computer, f), 0o755);
if (process.env.HERMESH_SCREEN) {
  fs.copyFileSync(process.env.HERMESH_SCREEN, path.join(bin, "hermesh-screen"));
  fs.chmodSync(path.join(bin, "hermesh-screen"), 0o755);
}

step("packaging Hermesh");
const layout = path.join(dist, `Hermesh-linux-${arch}`);
fs.rmSync(layout, { recursive: true, force: true });
const icon = path.join(tmp, "hermesh.png");
fs.writeFileSync(icon, appIconPNG(512));
const [built] = await packager({
  dir: staged,
  out: tmp,
  name: "Hermesh",
  executableName: "Hermesh",
  electronVersion,
  platform: "linux",
  arch,
  appVersion: version,
  icon,
  asar: true,
  prune: false,
  overwrite: true,
});
fs.renameSync(built, layout);
// Chromium's own words: only the app's languages stay.
for (const f of fs.readdirSync(path.join(layout, "locales"))) if (!["en-US.pak", "zh-CN.pak"].includes(f)) fs.rmSync(path.join(layout, "locales", f));

step("flipping Electron fuses");
// As on the Mac and Windows, but for cookie encryption: on Linux its key is the desktop keyring's, and
// where none is unlocked (a desktop logged in without a password, a KDE wallet never made) the app's
// first request waits on it for good. The app keeps nothing in cookies: it signs in through the browser.
await flipFuses(path.join(layout, "Hermesh"), {
  version: FuseVersion.V1,
  [FuseV1Options.RunAsNode]: false,
  [FuseV1Options.EnableCookieEncryption]: false,
  [FuseV1Options.EnableNodeOptionsEnvironmentVariable]: false,
  [FuseV1Options.EnableNodeCliInspectArguments]: false,
  [FuseV1Options.EnableEmbeddedAsarIntegrityValidation]: true,
  [FuseV1Options.OnlyLoadAppFromAsar]: true,
  [FuseV1Options.GrantFileProtocolExtraPrivileges]: false,
});
fs.cpSync(bin, path.join(layout, "bin"), { recursive: true });
// Everyone's to read and run (packager makes its folder the builder's alone); only root writes, once installed.
sh("chmod", ["-R", "u+rwX,go+rX,go-w", layout]);
fs.chmodSync(path.join(layout, "chrome-sandbox"), 0o4755);
console.log(`✓ ${layout}`);

step("the folder as a .tar.gz");
const tarball = path.join(dist, `Hermesh-linux-${arch}.tar.gz`);
sh("tar", ["-czf", tarball, "-C", dist, path.basename(layout)]);
console.log(`✓ ${tarball}`);

// The files a package installs, at their places.
const fsRoot = path.join(tmp, "root");
const place = (from, to, mode = 0o644) => {
  fs.mkdirSync(path.dirname(path.join(fsRoot, to)), { recursive: true });
  fs.copyFileSync(from, path.join(fsRoot, to));
  fs.chmodSync(path.join(fsRoot, to), mode);
};
fs.mkdirSync(path.join(fsRoot, "opt"), { recursive: true });
sh("cp", ["-a", layout, path.join(fsRoot, "opt/Hermesh")]);
fs.mkdirSync(path.join(fsRoot, "usr/bin"), { recursive: true });
for (const name of ["hermesh", "hermeshd"]) fs.symlinkSync(`/opt/Hermesh/bin/${name}`, path.join(fsRoot, "usr/bin", name));
place(path.join(linux, "hermesh.desktop"), "usr/share/applications/hermesh.desktop");
for (const size of [16, 32, 48, 64, 128, 256, 512]) {
  const to = path.join(fsRoot, `usr/share/icons/hicolor/${size}x${size}/apps/hermesh.png`);
  fs.mkdirSync(path.dirname(to), { recursive: true });
  fs.writeFileSync(to, appIconPNG(size));
}
place(path.join(linux, "hermesh-signer.service"), "usr/lib/systemd/system/hermesh-signer.service");
place(path.join(linux, "hermesh-signer.socket"), "usr/lib/systemd/system/hermesh-signer.socket");
place(path.join(linux, "io.hermesh.signer.policy"), "usr/share/polkit-1/actions/io.hermesh.signer.policy");

const description = "Let your agents talk to each other\n Hermesh connects cloud assistants and coding agents over an end-to-end encrypted network,\n and Her, its own agent, works on this computer.";
const packages = {};
if (has("dpkg-deb")) {
  step("making the .deb");
  const debian = path.join(fsRoot, "DEBIAN");
  fs.mkdirSync(debian, { recursive: true });
  const size = Number(execFileSync("du", ["-sk", path.join(fsRoot, "opt"), path.join(fsRoot, "usr")], { encoding: "utf8" }).split("\n").reduce((n, l) => n + (Number(l.split(/\s+/)[0]) || 0), 0));
  fs.writeFileSync(path.join(debian, "control"), [
    "Package: hermesh",
    `Version: ${version.replace("-", "~")}`,
    `Architecture: ${names.deb}`,
    "Maintainer: Hermesh <hello@hermesh.io>",
    `Installed-Size: ${size}`,
    "Depends: libgtk-3-0t64 | libgtk-3-0, libnss3, libasound2t64 | libasound2, libgbm1, libxss1, libsecret-1-0, polkitd | policykit-1, xdg-utils",
    "Recommends: libnotify-bin, at-spi2-core",
    "Section: devel",
    "Priority: optional",
    "Homepage: https://hermesh.io",
    `Description: ${description}`,
    "",
  ].join("\n"));
  for (const s of ["postinst", "prerm", "postrm"]) place(path.join(linux, s), `DEBIAN/${s}`, 0o755);
  const deb = path.join(dist, `Hermesh-linux-${arch}.deb`);
  sh("dpkg-deb", ["--root-owner-group", "-Zxz", "--build", fsRoot, deb]);
  fs.rmSync(debian, { recursive: true });
  packages.deb = deb;
  console.log(`✓ ${deb}`);
} else console.warn("⚠️  no dpkg-deb: no .deb");

if (has("rpmbuild")) {
  step("making the .rpm");
  const top = path.join(tmp, "rpm");
  for (const d of ["BUILD", "RPMS", "SPECS", "SOURCES", "SRPMS"]) fs.mkdirSync(path.join(top, d), { recursive: true });
  const script = (name) => fs.readFileSync(path.join(linux, name), "utf8").replace(/^#!.*\n/, "");
  // rpm's scriptlets get the count of installed versions where dpkg's get a word.
  const spec = path.join(top, "SPECS/hermesh.spec");
  fs.writeFileSync(spec, `Name: hermesh
Version: ${version.replace("-", "~")}
Release: 1
Summary: Let your agents talk to each other
License: Apache-2.0
URL: https://hermesh.io
Requires: gtk3, nss, alsa-lib, mesa-libgbm, libXScrnSaver, libsecret, polkit, xdg-utils
Recommends: libnotify, at-spi2-core
AutoReqProv: no
%define _build_id_links none
%define __strip /bin/true
%define _binary_payload w9.xzdio

%description
${description.split("\n").slice(1).map((l) => l.trim()).join(" ")}

%install
cp -a ${fsRoot}/. %{buildroot}/

%files
/opt/Hermesh
/usr/bin/hermesh
/usr/bin/hermeshd
/usr/share/applications/hermesh.desktop
/usr/share/icons/hicolor/*/apps/hermesh.png
/usr/lib/systemd/system/hermesh-signer.service
/usr/lib/systemd/system/hermesh-signer.socket
/usr/share/polkit-1/actions/io.hermesh.signer.policy
%attr(4755, root, root) /opt/Hermesh/chrome-sandbox

%post
${script("postinst")}
%preun
[ "$1" = 0 ] || exit 0
${script("prerm").replace(/case "\$1" in[\s\S]*?esac/, (m) => m.replace("remove|0)", "*)"))}
%postun
[ "$1" = 0 ] && rm -rf /var/lib/hermesh-signer
[ -d /run/systemd/system ] && systemctl daemon-reload || true
`);
  sh("rpmbuild", ["-bb", "--target", names.rpm, "--define", `_topdir ${top}`, spec]);
  const built = fs.readdirSync(path.join(top, "RPMS", names.rpm)).find((f) => f.endsWith(".rpm"));
  const rpm = path.join(dist, `Hermesh-linux-${arch}.rpm`);
  fs.copyFileSync(path.join(top, "RPMS", names.rpm, built), rpm);
  packages.rpm = rpm;
  console.log(`✓ ${rpm}`);
} else console.warn("⚠️  no rpmbuild: no .rpm");

step("the update feed");
const releaseURL = process.env.HERMESH_RELEASE_URL || `https://github.com/hermeshai/hermesh/releases/download/v${version}`;
const feed = path.join(dist, `RELEASES-linux-${arch}.json`);
fs.writeFileSync(feed, JSON.stringify({
  currentRelease: version,
  hermeshNotes: releaseCatalog(enNotes, zhNotes, version),
  packages: Object.fromEntries(Object.entries(packages).map(([kind, file]) => [kind, {
    url: `${releaseURL}/${path.basename(file)}`,
    sha256: createHash("sha256").update(fs.readFileSync(file)).digest("hex"),
  }])),
}, null, 2));
if (process.env.RELEASE_SIGNING_KEY_FILE) fs.writeFileSync(`${feed}.sig`, sign("sha256", fs.readFileSync(feed), fs.readFileSync(process.env.RELEASE_SIGNING_KEY_FILE, "utf8")));
console.log(`✓ ${feed}`);
