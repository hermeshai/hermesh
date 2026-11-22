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

test("iPhone first-use Her local AI consent covers provider, model, data and purpose in EN and ZH while desktop has no backend consent gate", () => {
  const enHer = JSON.parse(fs.readFileSync(path.join(desktop, "en", "her.json"), "utf8"));
  const zhHer = JSON.parse(fs.readFileSync(path.join(desktop, "zh-CN", "her.json"), "utf8"));
  assert.equal(enHer.aiSharing, undefined);
  assert.equal(zhHer.aiSharing, undefined);

  const iosStringsPath = path.join(import.meta.dirname, "..", "..", "..", "ios", "Hermesh", "Localizable.xcstrings");
  const xc = JSON.parse(fs.readFileSync(iosStringsPath, "utf8")).strings as Record<string, { localizations?: Record<string, { stringUnit?: { value?: string } }> }>;
  const iosKey = Object.keys(xc).find((k) => k.startsWith("Provider: %@\nModel: %@\n\nData:"));
  assert.ok(iosKey, "iOS Localizable.xcstrings must contain the Her local first-use provider/model/data/purpose disclosure");
  assert.match(iosKey, /Purpose:/);
  assert.doesNotMatch(iosKey, /expires when Her restarts/i);
  assert.doesNotMatch(iosKey, /revoke/i);
  assert.equal(xc["Revoke Third-Party AI Sharing"], undefined);
  assert.equal(xc["Review Third-Party AI Sharing"], undefined);
  const zhIOS = xc[iosKey]?.localizations?.["zh-Hans"]?.stringUnit?.value ?? "";
  assert.match(zhIOS, /供应商：%1\$@/);
  assert.match(zhIOS, /模型：%2\$@/);
  assert.match(zhIOS, /数据：/);
  assert.match(zhIOS, /用途：/);
  assert.doesNotMatch(zhIOS, /重启后授权失效/);
  assert.doesNotMatch(zhIOS, /撤回/);

  const iosFallbackKey = Object.keys(xc).find((k) => k.startsWith("Data: messages, chat history, images, attachment contents"));
  assert.ok(iosFallbackKey, "iOS Localizable.xcstrings must contain the fallback local first-use disclosure");
  assert.match(iosFallbackKey, /Purpose:/);
  const zhIOSFallback = xc[iosFallbackKey]?.localizations?.["zh-Hans"]?.stringUnit?.value ?? "";
  assert.match(zhIOSFallback, /数据：/);
  assert.match(zhIOSFallback, /用途：/);
});

test("Chinese calls a machine an environment (环境) and a coding agent an Agent provider (Agent 提供方)", () => {
  const old = /机器(?!人)|编码 ?agent/i;
  // Past releases' notes keep the words of their day.
  for (const { ns, file } of namespaces().filter((n) => n.ns !== "releases")) {
    for (const [key, forms] of texts(file("zh-CN"))) {
      for (const s of forms) assert.doesNotMatch(s, old, `zh-CN/${ns}: ${key}`);
    }
  }
  // The iPhone's, the signing panels' on Apple devices (HermeshKit) and on Windows and Linux (internal/preview).
  const repo = path.join(import.meta.dirname, "..", "..", "..", "..");
  for (const f of ["apps/ios/Hermesh/Localizable.xcstrings", "apps/ios/Hermesh/InfoPlist.xcstrings"]) {
    type Unit = { stringUnit?: { value?: string }; variations?: { plural?: Record<string, Unit> } };
    const strings = JSON.parse(fs.readFileSync(path.join(repo, f), "utf8")).strings as Record<string, { localizations?: Record<string, Unit> }>;
    const values = (u: Unit | undefined): string[] => (u ? [u.stringUnit?.value ?? "", ...Object.values(u.variations?.plural ?? {}).flatMap(values)] : []);
    for (const [key, s] of Object.entries(strings)) {
      for (const v of values(s.localizations?.["zh-Hans"])) assert.doesNotMatch(v, old, `${f}: ${key}`);
    }
  }
  const kit = fs.readFileSync(path.join(repo, "packages/HermeshKit/Sources/HermeshKit/Resources/zh-Hans.lproj/Localizable.strings"), "utf8");
  for (const line of kit.split("\n").filter((l) => l.includes(" = "))) assert.doesNotMatch(line.split(" = ")[1], old, `HermeshKit: ${line}`);
  const preview = JSON.parse(fs.readFileSync(path.join(repo, "internal/preview/locales/zh-CN.json"), "utf8")) as Record<string, string>;
  for (const [key, v] of Object.entries(preview)) assert.doesNotMatch(v, old, `internal/preview: ${key}`);
});
