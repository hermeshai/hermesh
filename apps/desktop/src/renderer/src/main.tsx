import "./i18n";
import { I18nextProvider } from "react-i18next";
import { createRoot } from "react-dom/client";
import { i18n } from "../../shared/i18n";
import { App } from "./App";
import { releaseWhileHidden } from "./memory";
import { platform } from "./platform";
import { initRunningMotion, syncOrbitStrip } from "./runningMotion";
import { initWindowVisibility } from "./visibility";
import "./styles.css";

document.documentElement.dataset.platform = platform;

const applyDarkStyle = (appearance?: string) => {
  const want = appearance === "black" ? "black" : "dark";
  if (document.documentElement.dataset.darkStyle !== want) document.documentElement.dataset.darkStyle = want;
  syncOrbitStrip();
};
applyDarkStyle(window.hermesh?.appearance);
initRunningMotion();
let appearanceUpdated = false;
window.hermesh?.onPreferences((p) => {
  appearanceUpdated = true;
  applyDarkStyle(p.appearance);
});
window.hermesh?.getPreferences().then((p) => {
  if (p && !appearanceUpdated) applyDarkStyle(p.appearance);
}).catch(() => {});

// Titles go grey while another window has the focus (the .blurred rules).
window.hermesh?.onWindowFocus((focused) => document.documentElement.classList.toggle("blurred", !focused));

// Pause infinite animations while the window is hidden or minimized so the compositor and GPU go completely idle.
initWindowVisibility();

// A scroller shows its scrollbar while it moves and for a moment after, as macOS overlays do (app.css).
const settle = new WeakMap<Element, number>();
document.addEventListener("scroll", (e) => {
  const el = e.target instanceof Element ? e.target : document.documentElement;
  el.classList.add("scrolling");
  clearTimeout(settle.get(el));
  settle.set(el, window.setTimeout(() => el.classList.remove("scrolling"), 1000));
}, { capture: true, passive: true });

// Memory a long conversation held goes back while the window is hidden (memory.ts).
releaseWhileHidden();

createRoot(document.getElementById("root")!).render(
  <I18nextProvider i18n={i18n}>
    <App />
  </I18nextProvider>,
);
