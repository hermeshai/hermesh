import { useEffect, type ReactNode } from "react";
import { Icon } from "../icons";
import { useStore } from "../store";
import "./firstrun.css";

/**
 * A first-run page in the compact window: the page, and its buttons at the
 * bottom; sheet over both. There is no way back to an earlier step, so no
 * steps are shown.
 */
export function Frame({ children, foot, sheet, className }: { children: ReactNode; foot?: ReactNode; sheet?: ReactNode; className?: string }) {
  const st = useStore();
  // Return goes on: the page's first enabled primary button (fields and buttons handle it themselves).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Enter" || e.defaultPrevented || (e.target as HTMLElement).closest("input, textarea, select, button")) return;
      const b = document.querySelector<HTMLButtonElement>(".ob-wrap .btn.primary:not(:disabled)");
      if (!b) return;
      e.preventDefault();
      b.click();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);
  return (
    <div className="ob-wrap">
      <div className="ob-top drag">
        <span className="tl-room" />
      </div>
      <div className={className ? `ob-body ${className}` : "ob-body"}>{children}</div>
      {foot && <div className="ob-foot">{foot}</div>}
      {sheet}
      {st.toast && (
        <div className="toast" role="status">
          <Icon name="check" />
          <span>{st.toast.text}</span>
        </div>
      )}
    </div>
  );
}

/**
 * A step page's picture, above its title: what the step is about, as a line
 * icon on a pale aurora tile. The mark itself is kept for the welcome and for
 * the brand at work (waiting, joining).
 */
export function StepIcon({ name }: { name: string }) {
  return (
    <span className="step-icon" aria-hidden="true">
      <Icon name={name} />
    </span>
  );
}
