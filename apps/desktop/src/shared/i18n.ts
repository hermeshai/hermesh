import { i18n, languages, pickLanguage, type CoreResources, type Language } from "@hermesh/core";
import enCommon from "../locales/en/common.json" with { type: "json" };
import enFirstrun from "../locales/en/firstrun.json" with { type: "json" };
import enMain from "../locales/en/main.json" with { type: "json" };
import enOverview from "../locales/en/overview.json" with { type: "json" };
import enSession from "../locales/en/session.json" with { type: "json" };
import enSettings from "../locales/en/settings.json" with { type: "json" };
import enUpdates from "../locales/en/updates.json" with { type: "json" };
import zhUpdates from "../locales/zh-CN/updates.json" with { type: "json" };
import enSheets from "../locales/en/sheets.json" with { type: "json" };
import zhCommon from "../locales/zh-CN/common.json" with { type: "json" };
import zhFirstrun from "../locales/zh-CN/firstrun.json" with { type: "json" };
import zhMain from "../locales/zh-CN/main.json" with { type: "json" };
import zhOverview from "../locales/zh-CN/overview.json" with { type: "json" };
import zhSession from "../locales/zh-CN/session.json" with { type: "json" };
import zhSettings from "../locales/zh-CN/settings.json" with { type: "json" };
import zhSheets from "../locales/zh-CN/sheets.json" with { type: "json" };

// The desktop app's text, by namespace: main (the main process: menus,
// notifications, dialogs), common (what several pages share), and one per
// part of the app. English holds every key; zh-CN translates them.

export const resources = {
  en: { main: enMain, common: enCommon, firstrun: enFirstrun, overview: enOverview, session: enSession, settings: enSettings, sheets: enSheets, updates: enUpdates },
  "zh-CN": { main: zhMain, common: zhCommon, firstrun: zhFirstrun, overview: zhOverview, session: zhSession, settings: zhSettings, sheets: zhSheets, updates: zhUpdates },
} satisfies Record<Language, Record<string, object>>;

for (const lng of languages) {
  for (const [ns, r] of Object.entries(resources[lng])) i18n.addResourceBundle(lng, ns, r);
}

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "core";
    resources: (typeof resources)["en"] & { core: CoreResources };
  }
}

/** What the owner chose in Settings › General › Language: the system's languages, or one of the app's. */
export type LanguageChoice = "system" | Language;

/** The language for a choice and the system's preferred languages (most preferred first). */
export function resolveLanguage(choice: LanguageChoice | undefined, preferred: readonly string[]): Language {
  return choice && choice !== "system" && languages.includes(choice) ? choice : pickLanguage(preferred);
}

/** Sets the process's language, once, before anything shows text. */
export function setLanguage(lng: Language) {
  i18n.changeLanguage(lng);
}

export { i18n, languages, type Language };
