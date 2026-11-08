import type { ThemeRegistration } from "shiki/core";

// Code in blues, with readable companions for strings, comments, numbers and types; light and dark.
export const palettes = {
  light: { foreground: "#0B1633", keyword: "#1B36D9", function: "#1A5CE6", string: "#087F78", comment: "#626775", constant: "#7543BA", type: "#7543BA", property: "#A94716", invalid: "#B0232F" },
  dark: { foreground: "#E8ECF4", keyword: "#7AB0FF", function: "#91BFFF", string: "#59CDB8", comment: "#9AA3B5", constant: "#B69AFF", type: "#B69AFF", property: "#F3B46B", invalid: "#FF8C94" },
};
export const themes: ThemeRegistration[] = Object.entries(palettes).map(([mode, c]) => ({
  name: `hermesh-${mode}`, type: mode as "light" | "dark",
  colors: { "editor.foreground": c.foreground, "editor.background": mode === "light" ? "#F6F7FA" : "#18181B" },
  settings: [
    { settings: { foreground: c.foreground } },
    { scope: ["keyword", "storage"], settings: { foreground: c.keyword } },
    { scope: ["entity.name.function", "support.function"], settings: { foreground: c.function } },
    { scope: ["string"], settings: { foreground: c.string } },
    { scope: ["comment", "punctuation.definition.comment"], settings: { foreground: c.comment } },
    { scope: ["constant", "variable.language"], settings: { foreground: c.constant } },
    { scope: ["entity.name.type", "support.type", "entity.name.class"], settings: { foreground: c.type } },
    { scope: ["variable.other.property", "entity.other.attribute-name"], settings: { foreground: c.property } },
    { scope: ["invalid"], settings: { foreground: c.invalid } },
  ],
}));
