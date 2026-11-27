import { useLayoutEffect, useSyncExternalStore } from "react";
import {
  isAppearance,
  isDarkPalette,
  isLightPalette,
  resolvePalette,
  usesSidebarMaterial,
  type AppearancePrefs,
  type DarkPalette,
  type LightPalette,
  type Palette,
} from "../../shared/appearance";
import { syncOrbitStrip } from "./runningMotion";

let prefs: AppearancePrefs = {
  appearance: isAppearance(window.hermesh?.appearance) ? window.hermesh!.appearance! : "system",
  preferredLight: isLightPalette(window.hermesh?.preferredLight) ? window.hermesh!.preferredLight : "light",
  preferredDark: isDarkPalette(window.hermesh?.preferredDark) ? window.hermesh!.preferredDark : "dark",
};
let initializedFromIPC = false;
const listeners = new Set<() => void>();

const darkMedia = () => (typeof window !== "undefined" ? window.matchMedia("(prefers-color-scheme: dark)") : null);
const isDarkNow = () => darkMedia()?.matches ?? false;

function currentLightStyle(): LightPalette {
  if (isLightPalette(prefs.appearance)) return prefs.appearance;
  return prefs.preferredLight ?? "light";
}

function currentDarkStyle(): DarkPalette {
  if (isDarkPalette(prefs.appearance)) return prefs.appearance;
  return prefs.preferredDark ?? "dark";
}

export function currentPalette(): Palette {
  return resolvePalette(prefs, isDarkNow());
}

function applyRootDatasets() {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  const light = currentLightStyle();
  const dark = currentDarkStyle();
  if (root.dataset.lightStyle !== light) root.dataset.lightStyle = light;
  if (root.dataset.darkStyle !== dark) root.dataset.darkStyle = dark;
  syncOrbitStrip();
  listeners.forEach((fn) => fn());
}

export function initAppearance(): void {
  applyRootDatasets();
  darkMedia()?.addEventListener("change", applyRootDatasets);
  window.hermesh?.onPreferences((p) => {
    initializedFromIPC = true;
    prefs = {
      appearance: p.appearance,
      preferredLight: p.preferredLight ?? (isLightPalette(p.appearance) ? p.appearance : prefs.preferredLight),
      preferredDark: p.preferredDark ?? (isDarkPalette(p.appearance) ? p.appearance : prefs.preferredDark),
    };
    applyRootDatasets();
  });
  window.hermesh
    ?.getPreferences()
    .then((p) => {
      if (!p || initializedFromIPC) return;
      prefs = {
        appearance: p.appearance,
        preferredLight: p.preferredLight ?? "light",
        preferredDark: p.preferredDark ?? "dark",
      };
      applyRootDatasets();
    })
    .catch(() => {});
}

const subscribe = (cb: () => void) => {
  listeners.add(cb);
  return () => listeners.delete(cb);
};

export function useIsDark(): boolean {
  return useSyncExternalStore(subscribe, isDarkNow, () => false);
}

/** Toggles html.vibrant on the main and settings windows for glass themes ("light" and "dark") only. */
export function useSidebarVibrant(): void {
  const vibrant = useSyncExternalStore(
    subscribe,
    () => !!window.hermesh?.material && usesSidebarMaterial(currentPalette()),
    () => false,
  );
  useLayoutEffect(() => {
    document.documentElement.classList.toggle("vibrant", vibrant);
    return () => document.documentElement.classList.remove("vibrant");
  }, [vibrant]);
}
