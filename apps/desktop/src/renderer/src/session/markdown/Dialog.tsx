import { useEffect, useRef, type ReactNode } from "react";

/**
 * Something of a reply shown large over the window (a table, a diagram): a modal dialog that
 * Escape and a click on its backdrop close.
 */
export function Dialog({ label, width, className, onClose, children }: { label: string; width: number | string; className?: string; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    if (ref.current && !ref.current.open) ref.current.showModal();
  }, []);
  return (
    <dialog
      ref={ref}
      className={className ? `md-dialog ${className}` : "md-dialog"}
      aria-label={label}
      style={{ width }}
      onKeyDown={(e) => {
        if (e.key !== "Escape") return;
        e.preventDefault();
        e.stopPropagation();
        onClose();
      }}
      onCancel={(e) => (e.preventDefault(), onClose())}
      // Only the backdrop is the dialog itself; its content is inside .md-dialog-in.
      onClick={(e) => e.target === e.currentTarget && onClose()}
    >
      <div className="md-dialog-in">{children}</div>
    </dialog>
  );
}
