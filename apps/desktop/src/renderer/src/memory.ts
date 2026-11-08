// Memory a heavy page held goes back once the window has moved on from it (docs/desktop-performance.md §7). The renderer is told
// memory is short (window.hermesh.releaseMemory, Electron's webFrame.clearCache): it drops caches and returns the pages
// its allocators keep after a long conversation was read. Only then, and while the window is hidden: the caches refill
// at a cost, so never while a page is in use.

/** After the last move away from a heavy page: what follows has drawn, and the person stopped moving between pages. */
const SETTLE = 3000;
/** Hidden this long (minimized, on another desktop): the window is not being looked at. */
const HIDDEN = 30_000;
/** Rows a conversation held for it to count as heavy: a long one, not a few turns. */
export const HEAVY_ROWS = 200;

let settle: ReturnType<typeof setTimeout> | undefined;
let hidden: ReturnType<typeof setTimeout> | undefined;

const release = () => window.hermesh?.releaseMemory();

/** A heavy page went away: give back what it held once the window settles on what follows (again and again only once). */
export function releaseAfterLeaving() {
  clearTimeout(settle);
  settle = setTimeout(release, SETTLE);
}

/** Gives memory back once the window has been hidden a while; called once, when the window starts. */
export function releaseWhileHidden() {
  document.addEventListener("visibilitychange", () => {
    clearTimeout(hidden);
    if (document.visibilityState === "hidden") hidden = setTimeout(release, HIDDEN);
  });
}
