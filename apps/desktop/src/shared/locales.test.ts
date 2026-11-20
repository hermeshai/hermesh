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

test("a Windows or Linux variant stands in for a key that exists, and takes its place on that system", async () => {
  const { systemWords } = await import("./words.ts");
  for (const { ns, file } of namespaces()) {
    const keys = texts(file("en"));
    for (const key of keys.keys()) {
      if (key.endsWith("_win")) assert.ok(keys.has(key.slice(0, -4)), `${ns}: ${key} has no ${key.slice(0, -4)}`);
      if (key.endsWith("_linux")) assert.ok(keys.has(key.slice(0, -6)), `${ns}: ${key} has no ${key.slice(0, -6)}`);
      // What Windows words differently, Linux does too: neither is a Mac, nor the other.
      if (key.endsWith("_win")) assert.ok(keys.has(`${key.slice(0, -4)}_linux`), `${ns}: ${key} has no Linux variant`);
    }
    for (const lang of ["en", "zh-CN"]) {
      for (const [key, text] of texts(file(lang))) if (key.endsWith("_linux")) assert.doesNotMatch(String(text), /Windows|\bPC\b|Touch ID|\bMac\b|TPM/, `${ns} ${lang}: ${key}`);
    }
  }
  const words = { thisMac: "This Mac", thisMac_win: "This PC", thisMac_linux: "This Computer", n: { files_one: "a file", files_win_one: "a PC file", files_other: "files" } };
  assert.deepEqual(systemWords(words, "win"), { thisMac: "This PC", n: { files_one: "a PC file", files_other: "files" } });
  assert.deepEqual(systemWords(words, "linux"), { thisMac: "This Computer", n: { files_one: "a file", files_other: "files" } });
});

test("Her third-party AI disclosure covers provider, model, data and purpose in both English and Chinese", () => {
  const enHer = JSON.parse(fs.readFileSync(path.join(desktop, "en", "her.json"), "utf8"));
  const zhHer = JSON.parse(fs.readFileSync(path.join(desktop, "zh-CN", "her.json"), "utf8"));
  assert.match(enHer.aiSharing.disclosure, /Provider: \{\{provider\}\} · \{\{endpoint\}\}/);
  assert.match(enHer.aiSharing.disclosure, /Model: \{\{model\}\}/);
  assert.match(enHer.aiSharing.disclosure, /Data:/);
  assert.match(enHer.aiSharing.disclosure, /Purpose:/);
  assert.doesNotMatch(enHer.aiSharing.disclosure, /expires when Her restarts/i);

  assert.match(zhHer.aiSharing.disclosure, /供应商：\{\{provider\}\} · \{\{endpoint\}\}/);
  assert.match(zhHer.aiSharing.disclosure, /模型：\{\{model\}\}/);
  assert.match(zhHer.aiSharing.disclosure, /数据：/);
  assert.match(zhHer.aiSharing.disclosure, /用途：/);
  assert.doesNotMatch(zhHer.aiSharing.disclosure, /重启后授权失效/);

  const iosStringsPath = path.join(import.meta.dirname, "..", "..", "..", "ios", "Hermesh", "Localizable.xcstrings");
  const xc = JSON.parse(fs.readFileSync(iosStringsPath, "utf8")).strings as Record<string, { localizations?: Record<string, { stringUnit?: { value?: string } }> }>;
  const iosKey = Object.keys(xc).find((k) => k.startsWith("Provider: %@ · %@\nModel: %@\n\nData:"));
  assert.ok(iosKey, "iOS Localizable.xcstrings must contain the Her provider/model/data/purpose disclosure");
  assert.match(iosKey, /Purpose:/);
  assert.doesNotMatch(iosKey, /expires when Her restarts/i);
  const zhIOS = xc[iosKey]?.localizations?.["zh-Hans"]?.stringUnit?.value ?? "";
  assert.match(zhIOS, /供应商：%1\$@ · %2\$@/);
  assert.match(zhIOS, /模型：%3\$@/);
  assert.match(zhIOS, /数据：/);
  assert.match(zhIOS, /用途：/);
  assert.doesNotMatch(zhIOS, /重启后授权失效/);
});
