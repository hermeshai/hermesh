import i18next from "i18next";
import { initReactI18next } from "react-i18next";
import enConsole from "./locales/en/console.json" with { type: "json" };
import enCustomize from "./locales/en/customize.json" with { type: "json" };
import enSettings from "./locales/en/settings.json" with { type: "json" };
import enYours from "./locales/en/yours.json" with { type: "json" };
import zhConsole from "./locales/zh-CN/console.json" with { type: "json" };
import zhCustomize from "./locales/zh-CN/customize.json" with { type: "json" };
import zhSettings from "./locales/zh-CN/settings.json" with { type: "json" };
import zhYours from "./locales/zh-CN/yours.json" with { type: "json" };

// The console's text, by namespace: console (the frame, Browser, Data), customize (Connectors
// and Skills), settings (Models and Web search) and yours (the owner's Chrome, on the Browser
// page). English holds every key; zh-CN translates them
// (locales.test.ts). The language is the browser's.

export const languages = ["zh-CN", "en"] as const;
export type Language = (typeof languages)[number];

export const resources = {
  en: { console: enConsole, customize: enCustomize, settings: enSettings, yours: enYours },
  "zh-CN": { console: zhConsole, customize: zhCustomize, settings: zhSettings, yours: zhYours },
} satisfies Record<Language, Record<string, object>>;

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "console";
    resources: (typeof resources)["en"];
  }
}

/**
 * The language for the person's preferred languages, most preferred first (BCP 47 tags as
 * the browser reports them: "zh-Hans-CN", "en-GB"): the first one the console has, else
 * English. Traditional Chinese doesn't take Simplified Chinese. (The Mac app's own rule,
 * packages/core/src/i18n.ts.)
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

export const i18n = i18next.createInstance();
i18n.use(initReactI18next).init({
  lng: pickLanguage(navigator.languages),
  fallbackLng: "en",
  supportedLngs: languages,
  resources,
  ns: ["console", "customize", "settings", "yours"],
  defaultNS: "console",
  initAsync: false,
  interpolation: { escapeValue: false },
});
