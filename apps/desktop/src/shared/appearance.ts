export type LightPalette = "light" | "soft";
export type DarkPalette = "dark" | "black";
export type Palette = LightPalette | DarkPalette;
/** Crisp light, soft paper light ("柔白"), soft dark, deep black ("暗黑"), or the system's. */
export type Appearance = "system" | Palette;

export type AppearancePrefs = {
  appearance: Appearance;
  preferredLight?: LightPalette;
  preferredDark?: DarkPalette;
};

export const isLightPalette = (v: unknown): v is LightPalette => v === "light" || v === "soft";
export const isDarkPalette = (v: unknown): v is DarkPalette => v === "dark" || v === "black";
export const isAppearance = (v: unknown): v is Appearance => v === "system" || isLightPalette(v) || isDarkPalette(v);

/** What Electron's nativeTheme.themeSource takes for a chosen appearance. */
export function themeSourceOf(appearance: Appearance): "system" | "light" | "dark" {
  if (appearance === "system") return "system";
  return isLightPalette(appearance) ? "light" : "dark";
}

/** Resolves the active 4-way palette given preferences and whether dark mode is active. */
export function resolvePalette(prefs: AppearancePrefs, dark: boolean): Palette {
  if (prefs.appearance !== "system") return prefs.appearance;
  return dark ? (prefs.preferredDark ?? "dark") : (prefs.preferredLight ?? "light");
}

/** Only Crisp Light ("light") and Soft Dark ("dark") use OS sidebar vibrancy / Mica; FinxWork Soft & Black are matte solid. */
export function usesSidebarMaterial(palette: Palette): boolean {
  return palette === "light" || palette === "dark";
}

/** Opaque window background (--win) matching each palette from the first frame. */
export function windowBackgroundOf(palette: Palette): string {
  switch (palette) {
    case "soft":
      return "#f4f5f7";
    case "dark":
      return "#1e1e21";
    case "black":
      return "#070709";
    default:
      return "#ffffff";
  }
}
