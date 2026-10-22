import { language, type Language } from "@hermesh/core";
import { app } from "electron";
import { resolveLanguage, setLanguage } from "../shared/i18n";
import { preferences } from "./notify";

// The app's language for this run: the owner's choice in Settings › General,
// or by default the first of the system's preferred languages the app has
// (which also honors the per-app language in System Settings › General ›
// Language & Region). Chosen once, at start; a new choice needs a restart.

export function chooseLanguage() {
  setLanguage(resolveLanguage(preferences().language, app.getPreferredSystemLanguages()));
}

/** The language as macOS names it (-AppleLanguages), for the signer to match the app. */
export function appleLanguage(): string {
  const names: Record<Language, string> = { en: "en", "zh-CN": "zh-Hans" };
  return names[language()];
}
