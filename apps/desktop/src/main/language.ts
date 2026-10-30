import { app } from "electron";
import { resolveLanguage, setLanguage } from "../shared/i18n";
import { preferences } from "./notify";

// The app's language for this run: the owner's choice in Settings › General,
// or by default the first of the system's preferred languages the app has
// (which also honors the per-app language in System Settings › General ›
// Language & Region). Chosen once, at start; a new choice needs a restart.

export function chooseLanguage() {
  // Test runs on Windows, which has no -AppleLanguages, name the system's languages instead.
  const system = !app.isPackaged && process.env.HERMESH_TEST_LANGUAGES ? process.env.HERMESH_TEST_LANGUAGES.split(",") : app.getPreferredSystemLanguages();
  setLanguage(resolveLanguage(preferences().language, system));
}
