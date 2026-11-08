import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { MENTION_PREFIX, isMentionable, mentions, segments, skillMentions } from "./mentions.ts";

// The cases the kernel runs too, so both read text the same way.
const shared = JSON.parse(readFileSync(new URL("../../../her/crates/her-core/testdata/mentions.json", import.meta.url), "utf8")) as {
  cases: { text: string; mentions: string[] }[];
};

test("mentions follow the shared cases", () => {
  assert.ok(shared.cases.length > 30);
  for (const c of shared.cases) {
    const found = mentions(c.text);
    assert.deepEqual(
      found.map((m) => m.name),
      c.mentions,
      JSON.stringify(c.text),
    );
    for (const m of found) assert.equal(c.text.slice(m.start, m.end), MENTION_PREFIX + m.name);
  }
});

test("only known names become tags, and segments rebuild the text", () => {
  const text = "请用 /skill:gen-report 整理，再按 /skill:other 写；`/skill:gen-report` 不算";
  assert.deepEqual(
    skillMentions(text, ["gen-report"]).map((m) => m.name),
    ["gen-report"],
  );
  const parts = segments(text, ["gen-report"]);
  assert.deepEqual(parts, [
    { text: "请用 " },
    { text: "/skill:gen-report", skill: "gen-report" },
    { text: " 整理，再按 /skill:other 写；`/skill:gen-report` 不算" },
  ]);
  assert.equal(parts.map((p) => p.text).join(""), text);
  assert.deepEqual(segments("", ["x"]), [{ text: "" }]);
  assert.deepEqual(segments("/skill:x", ["x"]), [{ text: "/skill:x", skill: "x" }]);
  assert.ok(isMentionable("gen-report") && isMentionable("a_b2"));
  assert.ok(!isMentionable("Weekly Report") && !isMentionable(""));
});
