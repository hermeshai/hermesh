import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { useLeaving } from "../components";
import { Icon } from "../icons";
import { useStore } from "../store";
import { fx } from "./fx";
import "./ink.css";
import "./firstrun.css";

// The first run's window (docs/cold-start-prd.md §5.2), always in ink (§4): the traffic lights,
// the progress line, the aurora behind and its lights, the particles' canvas, the page, and a
// sheet over it. A page is known by its key: when the key changes the old page fades out
// (160ms), and the new one comes in 180ms later, its parts rising in turn (.in, --i, --d).

/** Where the aurora's two lights sit (§5.2's table), by the page shown. */
export type Aura = "welcome" | "team" | "her" | "awake" | "far" | "linked" | "done";

const progress: Record<Aura, string> = { welcome: "5%", team: "28%", her: "52%", awake: "52%", far: "76%", linked: "76%", done: "100%" };

export function Stage({ aura, page, children, sheet }: { aura: Aura; page: string; children: ReactNode; sheet?: ReactNode }) {
  const st = useStore();
  const root = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const [shown, setShown] = useState(page);
  const last = useRef<ReactNode>(children);
  const leaving = shown !== page;
  if (!leaving) last.current = children;
  const toast = useLeaving(st.toast);

  // Ink is the first run's alone: the window's own chrome (title bar, scroll bars) dark too.
  useLayoutEffect(() => {
    document.documentElement.dataset.ob = "ink";
    return () => void delete document.documentElement.dataset.ob;
  }, []);
  // Before the pages' own effects (a child's run before its parent's): the welcome's particles need the canvas at once.
  useLayoutEffect(() => {
    fx.attach(canvas.current, root.current);
    fx.ambient(true);
    return () => fx.attach(null, null);
  }, []);
  useEffect(() => {
    if (!leaving) return;
    fx.reset();
    const timer = setTimeout(() => setShown(page), 180);
    return () => clearTimeout(timer);
  }, [leaving, page]);
  // Return goes on: the page's first enabled primary button (fields and buttons handle it themselves).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Enter" || e.defaultPrevented || (e.target as HTMLElement).closest("input, textarea, select, button")) return;
      const b = root.current?.querySelector<HTMLButtonElement>(".fr-sheet .btn.primary:not(:disabled), .fr-page .btn.primary:not(:disabled)");
      if (!b) return;
      e.preventDefault();
      b.click();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  return (
    <div className="fr-stage" ref={root} data-aura={aura} style={{ "--p": progress[aura] } as CSSProperties}>
      <div className="fr-aurora" aria-hidden="true">
        <i />
        <i />
      </div>
      <div className="fr-prog" aria-hidden="true">
        <i />
      </div>
      <div className="fr-lights drag" />
      <div key={shown} className={`fr-page ${leaving ? "leaving" : "entering"}`} inert={leaving}>
        {leaving ? last.current : children}
      </div>
      <canvas className="fr-fx" ref={canvas} aria-hidden="true" />
      {sheet && (
        <div className="fr-scrim">
          <div className="fr-sheet" role="dialog" aria-modal="true">
            {sheet}
          </div>
        </div>
      )}
      {toast.shown && (
        <div className={`fr-toast ${toast.leaving ? "leaving" : ""}`} role="status">
          <Icon name="check" />
          <span>{toast.shown.text}</span>
        </div>
      )}
    </div>
  );
}

/** Text that comes in a character at a time, from delay ms after its page (§4.4). */
export function Typed({ text, delay = 0 }: { text: string; delay?: number }) {
  return (
    <span className="ty" style={{ "--d": `${delay}ms` } as CSSProperties} aria-label={text}>
      {[...text].map((ch, i) => (
        <span key={i} style={{ "--i": i } as CSSProperties} aria-hidden="true">
          {ch}
        </span>
      ))}
    </span>
  );
}

/** A part of a page that rises in: i-th in turn (80ms apart), or delay ms after the page. */
export const rise = (i: number, delay = 0): CSSProperties => ({ "--i": i, "--d": `${delay}ms` }) as CSSProperties;
