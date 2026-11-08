import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

import { headline } from "./panel.js";

const read = (path) => fs.readFileSync(new URL(path, import.meta.url), "utf8");
const en = JSON.parse(read("../_locales/en/messages.json"));
const zh = JSON.parse(read("../_locales/zh_CN/messages.json"));

test("both languages have every message", () => {
  assert.deepEqual(Object.keys(zh).sort(), Object.keys(en).sort());
});

test("every message the manifest, the page and the worker name is there", () => {
  const named = [...read("../manifest.json").matchAll(/__MSG_(\w+)__/g)].map((m) => m[1]);
  const code = read("popup.js") + read("background.js");
  named.push(...[...code.matchAll(/msg\("(\w+)"/g)].map((m) => m[1]));
  named.push(...[...code.matchAll(/"(title[A-Z]\w*)"/g)].map((m) => m[1]));
  const waiting = { id: "a", title: "A", state: "awaiting_approval", tabs: 1 };
  for (const host of ["connected", "connecting", "not_open", "missing", "outdated"]) {
    for (const over of [{}, { inUse: false }, { sessions: [waiting] }, { sessions: [waiting, waiting] }, { stuck: true }, { update: "her" }]) {
      const line = headline({ host, inUse: true, stuck: false, sessions: [], ...over });
      named.push(line.title, line.hint, line.action?.label);
    }
  }
  for (const name of named.filter(Boolean)) assert.ok(en[name], name);
});

test("they speak of Her and Hermesh, as the apps do", () => {
  for (const [lang, messages] of Object.entries({ en, zh_CN: zh })) {
    for (const [name, { message }] of Object.entries(messages)) {
      assert.doesNotMatch(message, /finx|agent|智能体/i, `${lang}: ${name}`);
    }
  }
});
