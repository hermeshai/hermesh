import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useStore, type Sheet } from "../store";
import { EraseHerData } from "./HerOff";

/**
 * A destructive action asked for by another screen: its title, what happens, and the button that does it;
 * removing a machine with Her's data, the choice to erase it too, or why it stays (docs/her-server.md §5.6).
 */
export function ConfirmSheet({ sheet }: { sheet: Extract<Sheet, { kind: "confirm" }> }) {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const [busy, setBusy] = useState(false);
  const [erase, setErase] = useState(false);
  const her = sheet.herData;
  const go = async () => {
    setBusy(true);
    if (await st.run(() => sheet.run(erase), sheet.done)) st.set({ sheet: null });
    setBusy(false);
  };
  return (
    <>
      <h3 id="sheet-title">{sheet.title}</h3>
      <p className="sub">{sheet.message}</p>
      {her && (her.offline ? <p className="sub">{t("herOff.offline", { name: her.name })}</p> : <EraseHerData name={her.name} on={erase} set={setErase} disabled={busy} />)}
      <div className="sheet-foot">
        <button className="btn" onClick={() => st.set({ sheet: null })}>{t("cancel")}</button>
        <button className="btn primary destructive" disabled={busy} onClick={go}>{sheet.button}</button>
      </div>
    </>
  );
}
