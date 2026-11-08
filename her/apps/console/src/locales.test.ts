import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { test } from "node:test";

// Every language has English's keys, with the same {{values}} and <tags>. Plural forms differ by
// language: English has _one and _other, Chinese only _other, so keys are compared without
// their plural suffix.

const dir = path.join(import.meta.dirname, "locales");
const languages = readdirSync(dir);

type Tree = { [k: string]: string | Tree };

function flat(t: Tree, prefix = ""): Map<string, string> {
  const out = new Map<string, string>();
  for (const [k, v] of Object.entries(t)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (typeof v === "string") out.set(key.replace(/_(zero|one|two|few|many|other)$/, ""), v);
    else for (const [kk, vv] of flat(v, key)) out.set(kk, vv);
  }
  return out;
}

const marks = (s: string) => [...s.matchAll(/\{\{\s*(\w+)\s*\}\}|<(\/?\w+)\/?>/g)].map((m) => m[1] ?? m[2]).sort();

test("the console speaks English and Simplified Chinese", () => {
  assert.deepEqual(languages.sort(), ["en", "zh-CN"]);
});

for (const file of readdirSync(path.join(dir, "en"))) {
  test(`every language translates en/${file}`, () => {
    const en = flat(JSON.parse(readFileSync(path.join(dir, "en", file), "utf8")));
    for (const lng of languages.filter((l) => l !== "en")) {
      const other = flat(JSON.parse(readFileSync(path.join(dir, lng, file), "utf8")));
      for (const [k, v] of en) {
        assert.ok(other.has(k), `${lng}/${file} lacks ${k}`);
        assert.deepEqual(marks(other.get(k)!), marks(v), `${lng}/${file} ${k}: values and tags differ from English`);
      }
      for (const k of other.keys()) assert.ok(en.has(k), `${lng}/${file} has ${k}, which English doesn't`);
    }
  });
}

test("Names from before the rename, and the UI machine's Finder and keychain, are gone from the text", () => {
  for (const lng of languages)
    for (const file of readdirSync(path.join(dir, lng)))
      for (const [k, v] of flat(JSON.parse(readFileSync(path.join(dir, lng, file), "utf8"))))
        assert.doesNotMatch(v, /finx|Finder|访达|钥匙串|keychain/i, `${lng}/${file} ${k}`);
});
