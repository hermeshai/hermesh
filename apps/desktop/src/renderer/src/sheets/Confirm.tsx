import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useStore, type Sheet } from "../store";

/** A destructive action asked for by another screen: its title, what happens, and the button that does it. */
export function ConfirmSheet({ sheet }: { sheet: Extract<Sheet, { kind: "confirm" }> }) {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const [busy, setBusy] = useState(false);
  const go = async () => {
    setBusy(true);
    if (await st.run(sheet.run, sheet.done)) st.set({ sheet: null });
    setBusy(false);
  };
  return (
    <>
      <h3 id="sheet-title">{sheet.title}</h3>
      <p className="sub">{sheet.message}</p>
      <div className="sheet-foot">
        <button className="btn" onClick={() => st.set({ sheet: null })}>{t("cancel")}</button>
        <button className="btn primary destructive" disabled={busy} onClick={go}>{sheet.button}</button>
      </div>
    </>
  );
}
