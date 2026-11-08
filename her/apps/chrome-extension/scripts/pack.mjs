// Packs the extension for the Chrome Web Store (browser-design D-219):
// her/apps/chrome-extension/dist/hermesh-extension-<version>.zip, holding what loads (manifest.json,
// src/, icons/, _locales/) without tests, types or the manifest's "key", which the store does not take.
//
//   node her/apps/chrome-extension/scripts/pack.mjs [--key path/to/key.pem]
//
// --key puts the private key in the zip as key.pem. Only for the very first upload, and only
// if you have the key behind the manifest's "key": the store then keeps the ID
// igeolfcjdhlfopmimahmhleloehmfkdc. Without it the store picks another ID; put that in
// STORE_EXTENSION_ID (her/crates/her-cli/src/cmd_browser.rs) so her lets it start the host.
//
// Raise "version" in manifest.json before each upload: the store refuses a version it has.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const args = process.argv.slice(2);
const keyAt = args.indexOf("--key");
const key = keyAt >= 0 ? args[keyAt + 1] : undefined;
if (keyAt >= 0 && !key) throw new Error("--key needs the path of a .pem file");

const manifest = JSON.parse(fs.readFileSync(path.join(root, "manifest.json"), "utf8"));
delete manifest.key;

// methods.js must match the kernel it ships for; a stale copy fails `cargo test -p her-browser methods`.
const methods = fs.readFileSync(path.join(root, "src/methods.js"), "utf8");
const bridge = /export const bridge = (\d+);/.exec(methods)?.[1];
if (!bridge) throw new Error("src/methods.js has no bridge version; regenerate it with HER_UPDATE_SNAPSHOTS=1 cargo test -p her-browser methods");

const dist = path.join(root, "dist");
const stage = path.join(dist, "stage");
fs.rmSync(stage, { recursive: true, force: true });
fs.mkdirSync(stage, { recursive: true });
fs.writeFileSync(path.join(stage, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
for (const dir of ["src", "icons", "_locales"]) {
  fs.cpSync(path.join(root, dir), path.join(stage, dir), {
    recursive: true,
    filter: (f) => !f.endsWith(".test.js") && !f.endsWith(".d.ts") && path.basename(f) !== ".DS_Store",
  });
}
if (key) fs.copyFileSync(path.resolve(key), path.join(stage, "key.pem"));

const zip = path.join(dist, `hermesh-extension-${manifest.version}.zip`);
fs.rmSync(zip, { force: true });
execFileSync("zip", ["-r", "-X", "-q", zip, "."], { cwd: stage, stdio: "inherit" });
fs.rmSync(stage, { recursive: true, force: true });

console.log(`${path.relative(process.cwd(), zip)}: version ${manifest.version}, bridge ${bridge}${key ? ", with key.pem (first upload only)" : ""}`);
