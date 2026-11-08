import { useEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { errorText } from "./format";
import { useHer } from "./her";
import { Icon } from "./icons";

// What every page draws with: its bar, a sheet over it, the toast and the confirmation.

/** A page's bar: its title, a way back when an item of it is open, and its own buttons. */
export function PageTop({ title, back, children }: { title: string; back?: { label: string; go: () => void }; children?: ReactNode }) {
  const { t } = useTranslation();
  return (
    <div className="set-top">
      {back && (
        <button className="set-back" aria-label={t("back", { page: back.label })} title={t("back", { page: back.label })} onClick={back.go}>
          <Icon name="chevR" />
        </button>
      )}
      <h1>{title}</h1>
      <div className="grow" />
      {children}
    </div>
  );
}

/** A sheet over the page, as a native dialog: Esc and a click beside it close it. */
export function Dialog({ label, width, className, onClose, children }: { label: string; width: number | string; className?: string; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    if (ref.current && !ref.current.open) ref.current.showModal();
  }, []);
  return (
    <dialog
      ref={ref}
      className={className ? `sheet ${className}` : "sheet"}
      aria-label={label}
      style={{ width }}
      onKeyDown={(e) => {
        if (e.key !== "Escape") return;
        e.preventDefault();
        e.stopPropagation();
        onClose();
      }}
      onCancel={(e) => (e.preventDefault(), onClose())}
      // Only the backdrop is the dialog itself; its content is inside .sheet-in.
      onClick={(e) => e.target === e.currentTarget && onClose()}
    >
      <div className="sheet-in">{children}</div>
    </dialog>
  );
}

export function ToastView() {
  const { t } = useTranslation();
  const { toast, dismiss } = useHer();
  if (!toast) return null;
  return (
    <div className={`toast ${toast.bad ? "bad" : ""}`} role="status" key={toast.id}>
      <Icon name={toast.bad ? "warn" : "check"} />
      <span>{toast.text}</span>
      {toast.undo && (
        <button
          className="btn sm"
          onClick={() => {
            toast.undo?.();
            dismiss();
          }}
        >
          {t("undo")}
        </button>
      )}
    </div>
  );
}

/** The confirmation a page asked for. */
export function AskSheet() {
  const { t } = useTranslation();
  const { ask, confirm, show } = useHer();
  const [busy, setBusy] = useState(false);
  if (!ask) return null;
  const close = () => confirm(null);
  return (
    <Dialog label={ask.title} width={420} onClose={close}>
      <h3>{ask.title}</h3>
      <p className="sub">{ask.message}</p>
      <div className="sheet-foot">
        <button className="btn" onClick={close}>
          {t("cancel")}
        </button>
        <button
          className="btn primary danger"
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            try {
              await ask.run();
            } catch (e) {
              show(errorText(e), undefined, true);
            } finally {
              setBusy(false);
              close();
            }
          }}
        >
          {ask.button}
        </button>
      </div>
    </Dialog>
  );
}

/** A picture over the window, larger. */
export function Lightbox({ src, onClose }: { src: string; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <Dialog label={t("close")} width="auto" className="lightbox" onClose={onClose}>
      <img src={src} alt="" onClick={onClose} />
    </Dialog>
  );
}
