import assert from "node:assert/strict";
import { test } from "node:test";
import { checkEvery, feedNotes, newer, nextCheckDelay, nextUpdate, releaseBody, releaseCatalog, releaseFeed, releaseNotesProblem, squirrelFailure, type Update, type UpdateEvent } from "./update.ts";

test("newer compares versions as numbers, releases after their pre-releases", () => {
  const cases: [string, string, boolean][] = [
    ["0.2.0", "0.1.0", true],
    ["0.10.0", "0.9.1", true],
    ["1.0.0", "1.0.0", false],
    ["0.1.0", "0.2.0", false],
    ["1.0", "1.0.0", false],
    ["1.0.1", "1.0", true],
    ["1.0.0", "1.0.0-beta.1", true],
    ["1.0.0-beta.1", "1.0.0", false],
    ["1.0.0-beta.10", "1.0.0-beta.9", true],
    ["1.0.0-rc.1", "1.0.0-beta.2", true],
    ["1.0.0-beta.2", "0.9.0", true],
  ];
  for (const [a, b, want] of cases) assert.equal(newer(a, b), want, `newer(${a}, ${b})`);
});

test("the feed is Squirrel.Mac's JSON format for one release", () => {
  const f = releaseFeed({ version: "0.3.0", url: "https://example.com/Hermesh-macos-arm64.zip", date: new Date("2026-10-19T08:00:00.250Z") });
  assert.deepEqual(f, {
    currentRelease: "0.3.0",
    releases: [
      {
        version: "0.3.0",
        updateTo: {
          version: "0.3.0",
          name: "0.3.0",
          notes: "https://github.com/hermeshai/hermesh/releases/tag/v0.3.0",
          pub_date: "2026-10-19T08:00:00Z",
          url: "https://example.com/Hermesh-macos-arm64.zip",
        },
      },
    ],
  });
});

const run = (events: UpdateEvent[], from: Update = { state: "idle" }) => events.reduce(nextUpdate, from);
const feed = (latest: string, autoDownload = true): UpdateEvent => ({ type: "feed", latest, current: "0.2.0", autoDownload });

test("an update downloads by itself and then waits for a restart", () => {
  assert.deepEqual(run([{ type: "check" }]), { state: "checking" });
  assert.deepEqual(run([{ type: "check" }, feed("0.3.0")]), { state: "downloading", version: "0.3.0" });
  const ready = run([{ type: "check" }, feed("0.3.0"), { type: "download" }, { type: "downloaded", version: "0.3.0" }]);
  assert.deepEqual(ready, { state: "ready", version: "0.3.0" });
  // Later checks and errors leave a downloaded update ready.
  assert.deepEqual(run([{ type: "check" }, { type: "error", reason: "network", message: "net::ERR_INTERNET_DISCONNECTED" }, feed("0.2.0")], ready), ready);
});

test("with automatic download off, an update waits to be asked for", () => {
  const avail = run([{ type: "check" }, feed("0.3.0", false)]);
  assert.deepEqual(avail, { state: "available", version: "0.3.0" });
  assert.deepEqual(run([{ type: "download" }], avail), { state: "downloading", version: "0.3.0" });
});

test("no newer version, no feed, or a failure", () => {
  assert.deepEqual(run([{ type: "check" }, feed("0.2.0")]), { state: "current" });
  assert.deepEqual(run([{ type: "check" }, feed("0.1.0")]), { state: "current" });
  assert.deepEqual(run([{ type: "check" }, feed("")]), { state: "current" });
  // Squirrel found nothing after all (the feed changed in between).
  assert.deepEqual(run([{ type: "check" }, feed("0.3.0"), { type: "not-available" }]), { state: "current" });
  assert.deepEqual(run([{ type: "check" }, { type: "error", reason: "network", message: "HTTP 500" }]), { state: "error", reason: "network", message: "HTTP 500" });
  // A check during a download does not interrupt it.
  assert.deepEqual(run([{ type: "check" }], { state: "downloading", version: "0.3.0" }), { state: "downloading", version: "0.3.0" });
  assert.deepEqual(run([{ type: "check" }, feed("0.3.0")], { state: "unsupported" }), { state: "unsupported" });
});

test("Squirrel's errors: a read-only volume, or the download", () => {
  // Electron's message for SQRLUpdaterErrorDomain's read-only volume error, as the app showed it.
  const readOnly =
    "Cannot update while running on a read-only volume. The application is on a read-only volume. Please move the application and try again. If you're on macOS Sierra or later, you'll need to move the application out of the Downloads directory. See https://github.com/Squirrel/Squirrel.Mac/issues/182 for more information.";
  assert.equal(squirrelFailure(readOnly), "location");
  assert.equal(squirrelFailure("The Internet connection appears to be offline."), "download");
  assert.equal(squirrelFailure("Could not get code signature for running application"), "download");
});


test("successful checks stay fresh across launches; failures back off separately", () => {
  const now = 10 * checkEvery;
  assert.equal(nextCheckDelay(null, null, 0, now), 0);
  assert.equal(nextCheckDelay(now - 1000, now - 1000, 0, now), checkEvery - 1000);
  assert.equal(nextCheckDelay(now - checkEvery, now - 1000, 1, now), 5 * 60_000 - 1000);
  assert.equal(nextCheckDelay(now - checkEvery, now - 1000, 2, now), 30 * 60_000 - 1000);
  assert.equal(nextCheckDelay(null, now - 1000, 99, now), 120 * 60_000 - 1000);
  assert.equal(nextCheckDelay(now - checkEvery, now - checkEvery, 3, now), 0);
});

test("localized history includes skipped versions, with the current release first", () => {
  const summary = { improvements: ["A useful change"], fixes: [] };
  const source = { unreleased: summary, "0.1.0-rc7": summary, "0.2.0": summary };
  const catalog = releaseCatalog(source, source, "0.1.0-rc8");
  assert.deepEqual(catalog.map((n) => n.version), ["0.1.0-rc8", "0.1.0-rc7"]);
  const feed = releaseFeed({ version: "0.1.0-rc8", url: "https://example.com/app.zip", date: new Date(), notes: catalog });
  assert.deepEqual(feedNotes(feed.hermeshNotes), catalog);
  assert.equal(feed.releases[0].updateTo.notes, "https://github.com/hermeshai/hermesh/releases/tag/v0.1.0-rc8");
  assert.deepEqual(feedNotes([{ version: "0.1.0", summary: { en: summary } }, null, { version: "wrong" }]), []);
  assert.deepEqual(feedNotes(undefined), []);
  assert.deepEqual(feedNotes([{ version: "0.1.0", summary: { en: { improvements: ["a".repeat(1001)], fixes: [] }, "zh-CN": summary } }]), []);
});

test("a version is released only with its own highlights in both languages", () => {
  const en = { unreleased: { improvements: ["Soon"], fixes: [] }, "0.2.0": { improvements: ["New"], fixes: ["Fixed"] } };
  const zh = { unreleased: { improvements: ["快了"], fixes: [] }, "0.2.0": { improvements: ["新"], fixes: [] } };
  assert.equal(releaseNotesProblem(en, zh, "0.2.0"), "");
  assert.match(releaseNotesProblem(en, zh, "0.3.0"), /en\/releases.json has no "0.3.0"/);
  assert.match(releaseNotesProblem(en, { ...zh, "0.2.0": { improvements: [], fixes: [] } }, "0.2.0"), /zh-CN\/releases.json lists nothing/);
  assert.match(releaseNotesProblem({ ...en, "0.2.0": { improvements: Array(11).fill("x"), fixes: [] } }, zh, "0.2.0"), /more than 10 lines/);

  const body = releaseBody(en, zh, "0.2.0", { en: { improvements: "Improvements", fixes: "Fixes" }, "zh-CN": { improvements: "改进", fixes: "修复" } });
  assert.equal(body, "### Improvements\n\n- New\n\n### Fixes\n\n- Fixed\n\n---\n\n### 改进\n\n- 新\n");

  // What to install comes first, in each language, with the command and the image.
  const guide = { heading: "Install", mac: "Mac", machine: "Run `{{command}}`", device: "Device", cli: "CLI", relay: "Image {{image}}", note: "Rest." };
  const zhGuide = { heading: "安装", mac: "Mac", machine: "运行 `{{command}}`", device: "设备", cli: "命令行", relay: "镜像 {{image}}", note: "其余。" };
  const withGuide = releaseBody(en, zh, "0.2.0", { en: { improvements: "Improvements", fixes: "Fixes", install: guide }, "zh-CN": { improvements: "改进", fixes: "修复", install: zhGuide } },
    { command: "curl x | sh", image: "relay:v0.2.0" });
  assert.ok(withGuide.startsWith("### Install\n\n- Mac\n- Run `curl x | sh`\n- Device\n- CLI\n- Image relay:v0.2.0\n\nRest.\n\n### Improvements"), withGuide);
  assert.match(withGuide, /---\n\n### 安装\n\n- Mac\n- 运行 `curl x \| sh`[\s\S]*其余。\n\n### 改进/);
});
