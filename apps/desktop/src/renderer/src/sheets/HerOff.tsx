import { useState } from "react";
import { useTranslation } from "react-i18next";
import { local } from "../api";
import { SRow, Switch } from "../settings/common";
import { useStore, type Sheet } from "../store";
import { ErrorLine, SignerWaiting, useAction } from "./common";

/**
 * Turning Her off on a machine (docs/her-server.md §5.6): her data stays for next time, unless the owner
 * chooses to erase it too; then Touch ID, its card naming the choice.
 */
export function HerOffSheet({ sheet }: { sheet: Extract<Sheet, { kind: "herOff" }> }) {
  const { t } = useTranslation("sheets");
  const st = useStore();
  const [erase, setErase] = useState(false);
  const { busy, error, go } = useAction();
  const name = sheet.machineName;
  const off = async () => {
    if (await go(() => local.setHer({ machineId: sheet.machineId, enabled: false, erase }))) {
      st.set({ sheet: null });
      st.show(t(erase ? "herOff.erased" : "herOff.done", { name }));
    }
  };
  return (
    <>
      <h3 id="sheet-title">{t("herOff.title", { name })}</h3>
      <p className="sub">{t("herOff.sub")}</p>
      <EraseHerData name={name} on={erase} set={setErase} disabled={busy} />
      {busy && <SignerWaiting detail={t("herOff.approve")} />}
      <ErrorLine text={error} />
      <div className="sheet-foot">
        <button className="btn" disabled={busy} onClick={() => st.set({ sheet: null })}>{t("cancel")}</button>
        <button className={`btn primary${erase ? " destructive" : ""}`} disabled={busy} onClick={off}>{t(erase ? "herOff.buttonErase" : "herOff.button")}</button>
      </div>
    </>
  );
}

/** The choice to erase Her's data on a machine too, off by default: turning her off, or removing the machine. */
export function EraseHerData({ name, on, set, disabled }: { name: string; on: boolean; set: (on: boolean) => void; disabled: boolean }) {
  const { t } = useTranslation("sheets");
  return (
    <div className="fgroup">
      <SRow title={t("herOff.erase")} desc={t("herOff.eraseDesc", { name })}>
        <Switch on={on} label={t("herOff.erase")} set={set} disabled={disabled} />
      </SRow>
    </div>
  );
}
