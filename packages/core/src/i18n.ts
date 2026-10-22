import i18next from "i18next";
import en from "./locales/en.json" with { type: "json" };
import zhCN from "./locales/zh-CN.json" with { type: "json" };

// The app's languages and its one i18next instance, shared by the main
// process and every page. English is the source language: its files hold
// every key, and a key missing from another language falls back to it.
// The language is chosen once per process, before anything is shown
// (main/language.ts), so text is never computed at a module's top level.

export const languages = ["en", "zh-CN"] as const;
export type Language = (typeof languages)[number];
/** The core namespace's text, by key (the desktop app types its keys with it). */
export type CoreResources = typeof en;

export const i18n = i18next.createInstance();
i18n.init({
  lng: "en",
  fallbackLng: "en",
  supportedLngs: languages,
  resources: { en: { core: en }, "zh-CN": { core: zhCN } },
  ns: ["core"],
  defaultNS: "core",
  initAsync: false,
  interpolation: { escapeValue: false },
});

/** Text of the core namespace (the session list, failures, notices, times). */
export const t = i18n.getFixedT(null, "core");

/** The current language. */
export function language(): Language {
  return i18n.language as Language;
}

/**
 * The language for the person's preferred languages, most preferred first
 * (BCP 47 tags as macOS reports them: "zh-Hans-CN", "en-GB"): the first one
 * the app has, else English. As macOS picks a localization, Traditional
 * Chinese doesn't take Simplified Chinese.
 */
export function pickLanguage(preferred: readonly string[]): Language {
  for (const tag of preferred) {
    const [lang, ...rest] = tag.toLowerCase().replace(/_/g, "-").split("-");
    if (lang === "en") return "en";
    if (lang === "zh") {
      const script = rest.find((s) => s === "hans" || s === "hant");
      if (script === "hans" || (!script && !rest.some((r) => ["tw", "hk", "mo"].includes(r)))) return "zh-CN";
    }
  }
  return "en";
}
