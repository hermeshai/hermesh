import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { test } from "node:test";

// Every language has the same text as English, the source: the same keys
// (plural forms aside, which differ by language), each with the same
// {{values}} and <tags>.

const desktop = path.join(import.meta.dirname, "..", "locales");
const core = path.join(import.meta.dirname, "..", "..", "..", "..", "packages", "core", "src", "locales");
const languages = ["zh-CN"];

/** The files of each namespace, by language: core's, and the desktop app's. */
function namespaces(): { ns: string; file: (lng: string) => string }[] {
  return [
    { ns: "core", file: (lng) => path.join(core, `${lng}.json`) },
    ...fs.readdirSync(path.join(desktop, "en")).map((f) => ({ ns: path.basename(f, ".json"), file: (lng: string) => path.join(desktop, lng, f) })),
  ];
}

/** Each text by its key, plural forms ("files_one", "files_other") under one key ("files"). */
function texts(file: string): Map<string, string[]> {
  const out = new Map<string, string[]>();
  const walk = (o: Record<string, unknown>, prefix: string) => {
    for (const [k, v] of Object.entries(o)) {
      if (typeof v === "string") {
        const key = prefix + k.replace(/_(zero|one|two|few|many|other)$/, "");
        out.set(key, [...(out.get(key) ?? []), v]);
      } else if (v && typeof v === "object") walk(v as Record<string, unknown>, `${prefix}${k}.`);
      else assert.fail(`${file}: ${prefix}${k} is neither text nor a group`);
    }
  };
  walk(JSON.parse(fs.readFileSync(file, "utf8")), "");
  return out;
}

/** The {{values}} and <tags> a text uses. */
function slots(s: string): string[] {
  return [...new Set([...s.matchAll(/\{\{\s*([\w.]+)[^}]*\}\}|<\/?(\w+)\s*\/?>/g)].map((m) => m[1] ?? `<${m[2]}>`))].sort();
}

test("every language has English's keys, no others, and the same values and tags", () => {
  for (const { ns, file } of namespaces()) {
    const en = texts(file("en"));
    for (const lng of languages) {
      const other = texts(file(lng));
      assert.deepEqual([...other.keys()].sort(), [...en.keys()].sort(), `${lng}/${ns}: keys`);
      for (const [key, forms] of en) {
        const want = slots(forms.join(" ")).filter((s) => s !== "count");
        const got = slots(other.get(key)!.join(" ")).filter((s) => s !== "count");
        assert.deepEqual(got, want, `${lng}/${ns}: ${key}`);
      }
    }
  }
});

test("tags around text aren't HTML's empty elements, which <Trans> can't wrap around text", () => {
  const empty = ["area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr"];
  for (const { ns, file } of namespaces()) {
    for (const lng of ["en", ...languages]) {
      for (const [key, forms] of texts(file(lng))) {
        for (const m of forms.join(" ").matchAll(/<(\w+)>/g)) assert.ok(!empty.includes(m[1]), `${lng}/${ns}: ${key} wraps text in <${m[1]}>`);
      }
    }
  }
});

test("English plural forms come in pairs", () => {
  for (const { ns, file } of namespaces()) {
    const keys = new Set<string>();
    const walk = (o: Record<string, unknown>, prefix: string) => {
      for (const [k, v] of Object.entries(o)) typeof v === "string" ? keys.add(prefix + k) : walk(v as Record<string, unknown>, `${prefix}${k}.`);
    };
    walk(JSON.parse(fs.readFileSync(file("en"), "utf8")), "");
    for (const k of keys) {
      if (k.endsWith("_one")) assert.ok(keys.has(k.replace(/_one$/, "_other")), `en/${ns}: ${k} without _other`);
      if (k.endsWith("_other")) assert.ok(keys.has(k.replace(/_other$/, "_one")), `en/${ns}: ${k} without _one`);
    }
  }
});

test("a Windows variant stands in for a key that exists, and takes its place on Windows", async () => {
  const { windowsWords } = await import("./words.ts");
  for (const { ns, file } of namespaces()) {
    const keys = texts(file("en"));
    for (const key of keys.keys()) if (key.endsWith("_win")) assert.ok(keys.has(key.slice(0, -4)), `${ns}: ${key} has no ${key.slice(0, -4)}`);
  }
  assert.deepEqual(windowsWords({ thisMac: "This Mac", thisMac_win: "This PC", n: { files_one: "a file", files_win_one: "a PC file", files_other: "files" } }), {
    thisMac: "This PC",
    n: { files_one: "a PC file", files_other: "files" },
  });
});
