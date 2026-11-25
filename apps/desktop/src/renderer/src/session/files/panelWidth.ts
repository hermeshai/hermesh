import { useRef, useState, type CSSProperties, type KeyboardEvent, type PointerEvent as ReactPointerEvent, type RefObject } from "react";

// The width of the file panel on the window's right (FinxWork's panelWidth.ts), dragged by its
// left edge or moved by the arrow keys there, and kept for next time. The conversation keeps
// `room` beside it at least.

const minWidth = 360;

function savedWidth(key: string): number | null {
  const n = Number(localStorage.getItem(key));
  return Number.isFinite(n) && n >= minWidth ? n : null;
}

export type PanelWidth = {
  panel: RefObject<HTMLElement | null>;
  width: number | null;
  resizing: boolean;
  /** The panel's own style: its width, unless it takes the window. */
  style: (max: boolean) => CSSProperties | undefined;
  /** For the grip on the left edge. */
  grip: { onPointerDown: (e: ReactPointerEvent) => void; onKeyDown: (e: KeyboardEvent) => void; "aria-valuenow": number | undefined };
};

/**
 * The room the conversation keeps beside it. A width kept from a wider window (or from another
 * file's room, a document's being more) gives way to it, so the panel never takes the page whole
 * and the conversation's column never shrinks to nothing.
 */
const fit = (room: number): CSSProperties => ({ maxWidth: `calc(100% - ${room}px)` });

export function usePanelWidth(key: string, room: () => number): PanelWidth {
  const panel = useRef<HTMLElement>(null);
  const [width, setWidth] = useState<number | null>(() => savedWidth(key));
  const [resizing, setResizing] = useState(false);

  /** The widest the panel may be: the conversation keeps its room. */
  const maxWidth = () => {
    const right = panel.current?.getBoundingClientRect().right ?? window.innerWidth;
    const left = panel.current?.parentElement?.getBoundingClientRect().left ?? 0;
    return Math.max(minWidth, right - left - room());
  };
  const clamp = (w: number, most: number) => Math.round(Math.min(most, Math.max(minWidth, w)));
  const keep = (w: number) => {
    const next = clamp(w, maxWidth());
    setWidth(next);
    localStorage.setItem(key, String(next));
  };

  // The arrow keys on the edge: left widens, right narrows.
  const onKeyDown = (e: KeyboardEvent) => {
    const now = panel.current?.getBoundingClientRect().width ?? minWidth;
    if (e.key === "ArrowLeft") keep(now + 24);
    else if (e.key === "ArrowRight") keep(now - 24);
    else return;
    e.preventDefault();
  };

  // Dragging the left edge; the width is kept once it is let go.
  const onPointerDown = (e: ReactPointerEvent) => {
    e.preventDefault();
    const right = panel.current?.getBoundingClientRect().right ?? window.innerWidth;
    const most = maxWidth();
    setResizing(true);
    const move = (ev: PointerEvent) => setWidth(clamp(right - ev.clientX, most));
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      setResizing(false);
      setWidth((w) => {
        if (w !== null) localStorage.setItem(key, String(w));
        return w;
      });
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };

  return {
    panel,
    width,
    resizing,
    style: (max) => {
      if (max) return undefined;
      return { ...fit(room()), ...(width !== null ? ({ "--sfp-w": `${width}px` } as CSSProperties) : {}) };
    },
    grip: { onPointerDown, onKeyDown, "aria-valuenow": width ?? undefined },
  };
}
