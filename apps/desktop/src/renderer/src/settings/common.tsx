import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { i18n } from "../../../shared/i18n";
import { Icon } from "../icons";
import type { Store } from "../store";

/**
 * A sheet only the settings window needs: a new name (field), or an
 * explanation (no field). Confirmations use the app's confirm sheet.
 */
export type Ask = {
  title: string;
  message: string;
  field?: { label: string; value: string; button: string; run: (value: string) => Promise<unknown> };
};

export function AskSheet({ ask, close }: { ask: Ask; close: () => void }) {
  const { t } = useTranslation("settings");
  const f = ask.field;
  const [value, setValue] = useState(f?.value ?? "");
  const [busy, setBusy] = useState(false);
  const go = async () => {
    if (!f || !value.trim() || busy) return;
    setBusy(true);
    await f.run(value.trim()); // failures show as a toast
    close();
  };
  return (
    <>
      <div className="sheet-scrim" />
      <div className="sheet" role="dialog" aria-label={ask.title} style={{ width: 440 }}>
        <h3>{ask.title}</h3>
        <p className="sub">{ask.message}</p>
        {f && (
          <input
            className="field"
            aria-label={f.label}
            placeholder={f.label}
            value={value}
            autoFocus
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && go()}
          />
        )}
        <div className="sheet-foot">
          {f ? (
            <>
              <button className="btn" onClick={close}>{t("cancel")}</button>
              <button className="btn primary" disabled={busy || !value.trim()} onClick={go}>
                {f.button}
              </button>
            </>
          ) : (
            <button className="btn primary" onClick={close} autoFocus>{t("ok")}</button>
          )}
        </div>
      </div>
    </>
  );
}

/** A setting: what it is, a line on what it means, and its control on the right. */
export function SRow({ title, desc, children }: { title: ReactNode; desc?: ReactNode; children?: ReactNode }) {
  return (
    <div className="frow">
      <div className="ft">
        <div>{title}</div>
        {desc && <div className="fdesc">{desc}</div>}
      </div>
      {children}
    </div>
  );
}

export function Switch({ on, label, set, disabled }: { on: boolean; label: string; set?: (on: boolean) => void; disabled?: boolean }) {
  return <button className={`sw ${on ? "on" : ""}`} role="switch" aria-checked={on} aria-label={label} disabled={disabled} onClick={() => set?.(!on)} />;
}

/** A row that opens an item of a list: its icon, name and a line, what to glance at, and ›. */
export function GoRow({ icon, title, sub, onClick, children }: { icon: ReactNode; title: string; sub?: ReactNode; onClick: () => void; children?: ReactNode }) {
  return (
    <button className="frow go" onClick={onClick}>
      {icon}
      <div className="ft">
        <div className="fn">{title}</div>
        {sub && <div className="fdesc">{sub}</div>}
      </div>
      {children}
      <Icon name="chevR" className="sm chev" />
    </button>
  );
}

/** What is rarely needed, folded under a quiet "More" (or another name). */
export function More({ label, children }: { label?: string; children: ReactNode }) {
  const { t } = useTranslation("settings");
  const [open, setOpen] = useState(false);
  return (
    <div className="set-more-wrap">
      <button className={`set-more ${open ? "open" : ""}`} aria-expanded={open} onClick={() => setOpen(!open)}>
        {label ?? t("more")}
        <Icon name="chevR" className="sm" />
      </button>
      {open && children}
    </div>
  );
}

/** A label and its value, in a group. */
export function Row({ k, children }: { k: string; children: ReactNode }) {
  return (
    <div className="frow">
      <span className="fl">{k}</span>
      <span className="fv">{children}</span>
    </div>
  );
}

export function copy(st: Store, text: string) {
  navigator.clipboard.writeText(text).then(() => st.show(i18n.t("settings:copied")));
}
