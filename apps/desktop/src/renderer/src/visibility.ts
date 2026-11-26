// Unified window visibility: combines Chromium's Page Visibility API with the
// BrowserWindow's native show/hide/minimize state from the main process, since
// macOS `win.hide()` / Cmd+H does not always flip `document.hidden`.

let hostVisible = true;
const listeners = new Set<(hidden: boolean) => void>();
let initialized = false;

export function isWindowHidden(): boolean {
  return (typeof document !== "undefined" && document.hidden) || !hostVisible;
}

function notify() {
  const hidden = isWindowHidden();
  if (typeof document !== "undefined") {
    document.documentElement.classList.toggle("win-hidden", hidden);
  }
  listeners.forEach((fn) => fn(hidden));
}

export function initWindowVisibility() {
  if (initialized || typeof document === "undefined") return;
  initialized = true;
  document.addEventListener("visibilitychange", notify);
  window.hermesh?.onWindowVisible?.((visible) => {
    if (hostVisible !== visible) {
      hostVisible = visible;
      notify();
    }
  });
  notify();
}

export function onWindowHiddenChange(fn: (hidden: boolean) => void): () => void {
  initWindowVisibility();
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}
