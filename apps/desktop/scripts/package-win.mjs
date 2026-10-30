#!/usr/bin/env node
// Builds the Windows app (docs/windows.md §6.1) into dist/:
//   dist/Hermesh-windows-<arch>/          the package's files (a loose layout: Developer Mode installs it
//                                         with Add-AppxPackage -Register <it>\AppxManifest.xml)
//   dist/Hermesh-windows-<arch>.msix      on Windows, with the Windows SDK: the package, for the
//                                         Microsoft Store (which signs it) or signed for tests
//
// The package:
//   Hermesh.exe, resources\app.asar    Electron: main, preload and the page
//   bin\hermeshd.exe                   console, every command; alias hermeshd
//   bin\hermeshdw.exe                  windowless; the daemon's startup task
//   bin\hermesh.exe                    the agents' CLI; alias hermesh
//   bin\hermesh-signer.exe             copied into Program Files when this PC becomes a trusted device
//   Assets\, resources.pri             the logos
//   AppxManifest.xml
//
// Needs Go and Node, on any system; the .msix needs Windows and its SDK (makepri, makeappx).
//
// Environment:
//   VERSION                     version (default: package.json's)
//   HERMESH_ARCH                x64 (default) or arm64
//   HERMESH_DIST                where to put it all (default: dist/)
//   HERMESH_MSIX_NAME           the package's name (default Hermesh; the Store assigns one)
//   HERMESH_MSIX_PUBLISHER      its publisher, as the Store assigns it (default CN=Hermesh)
//   HERMESH_MSIX_PUBLISHER_NAME the publisher people see (default Hermesh)
//   HERMESH_MSIX_CERT           a .pfx to sign the package with (tests), and HERMESH_MSIX_CERT_PASSWORD
import { packager } from "@electron/packager";
import { flipFuses, FuseV1Options, FuseVersion } from "@electron/fuses";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
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
windowsIcons(path.join(tmp, "Assets"), ico);

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

step("adding the programs, the logos and the manifest");
fs.cpSync(bin, path.join(layout, "bin"), { recursive: true });
fs.cpSync(path.join(tmp, "Assets"), path.join(layout, "Assets"), { recursive: true });
fs.writeFileSync(path.join(layout, "AppxManifest.xml"), manifest());
console.log(`✓ ${layout}`);

if (onWindows) {
  const sdk = windowsSDK();
  step("indexing the logos (resources.pri)");
  sh(path.join(sdk, "makepri.exe"), ["createconfig", "/cf", path.join(tmp, "priconfig.xml"), "/dq", "en-US", "/pv", "10.0.0", "/o"]);
  sh(path.join(sdk, "makepri.exe"), ["new", "/pr", layout, "/cf", path.join(tmp, "priconfig.xml"), "/of", path.join(layout, "resources.pri"), "/o"]);
  step("packing the .msix");
  const msix = path.join(dist, `Hermesh-windows-${arch}.msix`);
  sh(path.join(sdk, "makeappx.exe"), ["pack", "/d", layout, "/p", msix, "/o"]);
  if (process.env.HERMESH_MSIX_CERT) {
    step("signing it for tests");
    sh(path.join(sdk, "signtool.exe"), ["sign", "/fd", "SHA256", "/f", process.env.HERMESH_MSIX_CERT, ...(process.env.HERMESH_MSIX_CERT_PASSWORD ? ["/p", process.env.HERMESH_MSIX_CERT_PASSWORD] : []), msix]);
  }
  console.log(`✓ ${msix}`);
}

/** The package's manifest (docs/windows.md §6.1). */
function manifest() {
  const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  // MSIX versions are four numbers.
  const v = [...version.replace(/[-+].*$/, "").split(".").map(Number), 0, 0, 0].slice(0, 4).join(".");
  const name = process.env.HERMESH_MSIX_NAME || "Hermesh";
  const publisher = process.env.HERMESH_MSIX_PUBLISHER || "CN=Hermesh";
  const publisherName = process.env.HERMESH_MSIX_PUBLISHER_NAME || "Hermesh";
  return `<?xml version="1.0" encoding="utf-8"?>
<Package
  xmlns="http://schemas.microsoft.com/appx/manifest/foundation/windows10"
  xmlns:uap="http://schemas.microsoft.com/appx/manifest/uap/windows10"
  xmlns:uap5="http://schemas.microsoft.com/appx/manifest/uap/windows10/5"
  xmlns:uap10="http://schemas.microsoft.com/appx/manifest/uap/windows10/10"
  xmlns:desktop="http://schemas.microsoft.com/appx/manifest/desktop/windows10"
  xmlns:desktop4="http://schemas.microsoft.com/appx/manifest/desktop/windows10/4"
  xmlns:desktop6="http://schemas.microsoft.com/appx/manifest/desktop/windows10/6"
  xmlns:rescap="http://schemas.microsoft.com/appx/manifest/foundation/windows10/restrictedcapabilities"
  IgnorableNamespaces="uap uap5 uap10 desktop desktop4 desktop6 rescap">
  <Identity Name="${esc(name)}" Publisher="${esc(publisher)}" Version="${v}" ProcessorArchitecture="${arch}"/>
  <Properties>
    <DisplayName>Hermesh</DisplayName>
    <PublisherDisplayName>${esc(publisherName)}</PublisherDisplayName>
    <Logo>Assets\\StoreLogo.png</Logo>
    <!-- No private copies: the data folder, and what agents write to AppData and the registry, are the real ones. -->
    <desktop6:FileSystemWriteVirtualization>disabled</desktop6:FileSystemWriteVirtualization>
    <desktop6:RegistryWriteVirtualization>disabled</desktop6:RegistryWriteVirtualization>
  </Properties>
  <Dependencies>
    <TargetDeviceFamily Name="Windows.Desktop" MinVersion="10.0.22621.0" MaxVersionTested="10.0.26100.0"/>
  </Dependencies>
  <Resources>
    <Resource Language="en-us"/>
    <Resource Language="zh-cn"/>
  </Resources>
  <Applications>
    <Application Id="Hermesh" Executable="Hermesh.exe" EntryPoint="Windows.FullTrustApplication">
      <uap:VisualElements DisplayName="Hermesh" Description="Hermesh" BackgroundColor="transparent" Square150x150Logo="Assets\\Square150x150Logo.png" Square44x44Logo="Assets\\Square44x44Logo.png"/>
      <Extensions>
        <uap:Extension Category="windows.protocol">
          <uap:Protocol Name="hermesh">
            <uap:DisplayName>Hermesh link</uap:DisplayName>
          </uap:Protocol>
        </uap:Extension>
        <desktop:Extension Category="windows.startupTask" Executable="bin\\hermeshdw.exe" EntryPoint="Windows.FullTrustApplication" uap10:Parameters="run --supervise --owner">
          <desktop:StartupTask TaskId="HermeshDaemon" Enabled="true" DisplayName="Hermesh background service"/>
        </desktop:Extension>
        <desktop:Extension Category="windows.startupTask" Executable="Hermesh.exe" EntryPoint="Windows.FullTrustApplication" uap10:Parameters="--hermesh-at-login">
          <desktop:StartupTask TaskId="HermeshApp" Enabled="false" DisplayName="Hermesh"/>
        </desktop:Extension>
      </Extensions>
    </Application>
    <!-- The command line on PATH: one alias per application, which Start doesn't list. -->${["hermesh", "hermeshd"].map((cli) => `
    <Application Id="${cli}" Executable="bin\\${cli}.exe" EntryPoint="Windows.FullTrustApplication" desktop4:SupportsMultipleInstances="true">
      <uap:VisualElements DisplayName="${cli}" Description="${cli}" BackgroundColor="transparent" Square150x150Logo="Assets\\Square150x150Logo.png" Square44x44Logo="Assets\\Square44x44Logo.png" AppListEntry="none"/>
      <Extensions>
        <uap5:Extension Category="windows.appExecutionAlias">
          <uap5:AppExecutionAlias desktop4:Subsystem="console">
            <uap5:ExecutionAlias Alias="${cli}.exe"/>
          </uap5:AppExecutionAlias>
        </uap5:Extension>
      </Extensions>
    </Application>`).join("")}
  </Applications>
  <Capabilities>
    <rescap:Capability Name="runFullTrust"/>
    <rescap:Capability Name="unvirtualizedResources"/>
  </Capabilities>
</Package>
`;
}

/** The newest Windows SDK's tools for this machine. */
function windowsSDK() {
  const kits = path.join(process.env["ProgramFiles(x86)"] || "C:\\Program Files (x86)", "Windows Kits", "10", "bin");
  const host = process.arch === "arm64" ? "arm64" : "x64";
  const versions = fs.readdirSync(kits).filter((d) => /^10\./.test(d) && fs.existsSync(path.join(kits, d, host, "makeappx.exe")));
  if (!versions.length) throw new Error(`no Windows SDK with makeappx.exe in ${kits}`);
  versions.sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
  return path.join(kits, versions.at(-1), host);
}
