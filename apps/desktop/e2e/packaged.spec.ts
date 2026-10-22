import { chromium, expect, test, type Browser } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fixture } from "./app";

// The packaged Hermesh.app (HERMESH_DESKTOP_APP; run by TestDesktopPackaged):
// with its fuses on, Playwright cannot drive Electron's main process, so the
// app runs as people run it and the page is reached over Chromium's
// debugging port. Its data directory points at the fixture's daemon.
const bundle = process.env.HERMESH_DESKTOP_APP;
const image = process.env.HERMESH_DESKTOP_DMG;

const freePort = () =>
  new Promise<number>((resolve) => {
    const s = net.createServer().listen(0, "127.0.0.1", () => {
      const p = (s.address() as net.AddressInfo).port;
      s.close(() => resolve(p));
    });
  });

const signers = (parent: number) =>
  execFileSync("ps", ["-axo", "pid=,ppid=,command="], { encoding: "utf8" })
    .split("\n")
    .filter((l) => l.includes("Hermesh Signer.app/Contents/MacOS/HermeshSigner") && l.trim().split(/\s+/)[1] === String(parent));

async function checkApp(bundle: string) {
  const data = fs.mkdtempSync(path.join(os.tmpdir(), "hmpkg-"));
  fs.mkdirSync(path.join(data, "run"));
  fs.symlinkSync(fixture.socket, path.join(data, "run", "hermeshd.sock"));
  const port = await freePort();
  // As a system in Chinese (the tests read the app in Chinese).
  const child = spawn(path.join(bundle, "Contents", "MacOS", "Hermesh"), [`--remote-debugging-port=${port}`, "-AppleLanguages", "(zh-Hans)"], {
    env: { ...process.env, HERMESH_DATA_DIR: data },
    stdio: "ignore",
  });
  let browser: Browser | undefined;
  try {
    for (let i = 0; !browser && i < 60; i++) {
      browser = await chromium.connectOverCDP(`http://127.0.0.1:${port}`).catch(() => undefined);
      if (!browser) await new Promise((r) => setTimeout(r, 500));
    }
    expect(browser, "the app's debugging port").toBeTruthy();
    await expect.poll(() => browser!.contexts().flatMap((c) => c.pages()).length).toBeGreaterThan(0);
    const page = browser!.contexts().flatMap((c) => c.pages())[0];
    await expect(page.getByRole("list", { name: "会话" })).toContainText("写 hello");
    expect(page.url()).toMatch(/^app:\/\/hermesh\//);
    expect(await page.evaluate(() => typeof (globalThis as { require?: unknown }).require)).toBe("undefined");
    await expect.poll(() => signers(child.pid!).length, { message: "the bundled signer runs as the app's child" }).toBe(1);
    expect(signers(child.pid!)[0], "the signer speaks the app's language").toContain("-AppleLanguages (zh-Hans)");
  } finally {
    await browser?.close();
    child.kill();
    await expect.poll(() => child.exitCode !== null || child.signalCode !== null).toBe(true);
    await expect.poll(() =>
      execFileSync("ps", ["-axo", "command="], { encoding: "utf8" }).split("\n").filter((command) => command.includes(path.join(bundle, "Contents"))).length,
      { message: "the app and all its helpers exit", timeout: 15_000 },
    ).toBe(0);
  }
  // The signer leaves with the app.
  await expect.poll(() => signers(child.pid!).length, { timeout: 15_000 }).toBe(0);
  fs.rmSync(data, { recursive: true, force: true });
}

test("the packaged app shows the daemon's sessions and keeps its signer running", async () => {
  test.skip(!bundle, "set HERMESH_DESKTOP_APP");
  await checkApp(bundle!);
});

test("the signed disk image opens read-only with an Applications shortcut and a working app", async () => {
  test.skip(!image, "set HERMESH_DESKTOP_DMG");
  test.setTimeout(90_000);
  const mount = fs.mkdtempSync(path.join(os.tmpdir(), "hmdmg-"));
  try {
    execFileSync("codesign", ["--verify", "--strict", image!]);
    execFileSync("hdiutil", ["attach", "-readonly", "-nobrowse", "-noautoopen", "-mountpoint", mount, image!], { timeout: 30_000 });
    try {
      expect(fs.readlinkSync(path.join(mount, "Applications"))).toBe("/Applications");
      expect(fs.existsSync(path.join(mount, ".DS_Store"))).toBe(true);
      expect(() => fs.writeFileSync(path.join(mount, "write-test"), "")).toThrow(/EROFS/);
      const app = path.join(mount, "Hermesh.app");
      execFileSync("codesign", ["--verify", "--deep", "--strict", app]);
      await checkApp(app);
    } finally {
      execFileSync("hdiutil", ["detach", "-force", mount], { timeout: 30_000 });
    }
  } finally {
    fs.rmSync(mount, { recursive: true, force: true });
  }
});

test("the packaged app offers macOS its two languages, the signer too", () => {
  test.skip(!bundle, "set HERMESH_DESKTOP_APP");
  const lproj = (dir: string) => fs.readdirSync(dir).filter((f) => f.endsWith(".lproj")).sort();
  const plist = (file: string, key: string) => execFileSync("plutil", ["-extract", key, "json", "-o", "-", file], { encoding: "utf8" });
  const contents = path.join(bundle!, "Contents");
  expect(lproj(path.join(contents, "Resources"))).toEqual(["en.lproj", "zh_CN.lproj"]);
  expect(JSON.parse(plist(path.join(contents, "Info.plist"), "CFBundleLocalizations"))).toEqual(["en", "zh_CN"]);
  const signer = path.join(contents, "Helpers", "Hermesh Signer.app", "Contents");
  expect(JSON.parse(plist(path.join(signer, "Info.plist"), "CFBundleLocalizations"))).toEqual(["en", "zh-Hans"]);
  // SwiftPM's older build system lays the resource bundle out flat and names the folder zh-hans.lproj.
  const strings = fs.readdirSync(path.join(signer, "Resources", "HermeshSigner_HermeshSigner.bundle"), { recursive: true }).map(String);
  expect(strings.map((f) => f.toLowerCase())).toContainEqual(expect.stringMatching(/(^|\/)zh-hans\.lproj\/localizable\.strings$/));
});
