// The release notes of one version, from the app's catalogs
// (src/locales/*/releases.json): checks that the version has its own
// highlights in both languages, and prints the GitHub release's text.
// The release workflow runs it before building anything.
//
//   node apps/desktop/scripts/release-notes.mjs 0.1.1-beta.3 > notes.md
import { releaseBody, releaseNotesProblem } from "../src/shared/update.ts";
import enNotes from "../src/locales/en/releases.json" with { type: "json" };
import zhNotes from "../src/locales/zh-CN/releases.json" with { type: "json" };
import enUpdates from "../src/locales/en/updates.json" with { type: "json" };
import zhUpdates from "../src/locales/zh-CN/updates.json" with { type: "json" };

const version = (process.argv[2] ?? "").replace(/^v/, "");
if (!version) {
  console.error("usage: release-notes.mjs <version>");
  process.exit(2);
}
const problem = releaseNotesProblem(enNotes, zhNotes, version);
if (problem) {
  console.error(`release notes for ${version}: ${problem} (docs/desktop-app.md, Release highlights)`);
  process.exit(1);
}
process.stdout.write(releaseBody(enNotes, zhNotes, version, { en: enUpdates, "zh-CN": zhUpdates }));
