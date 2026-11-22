import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { i18n } from "../../../shared/i18n";
import { local } from "../api";
import { ComputerUse } from "../her-computer/ComputerUse";
import { herDataKept } from "../her/environments";
import { GrantLine } from "../her/GrantLine";
import { useStore } from "../store";
import { SRow, Switch } from "./common";

/** An environment's Her: running her there, her data when she is off, her grant, and what macOS lets her do here. */
export function EnvHer({ m }: { m: Machine }) {
  const st = useStore();
  return (
    <div className="form">
      <div>
        <div className="fgroup">
          <HerRow m={m} />
        </div>
        <GrantLine m={m} />
      </div>
      {/* What macOS lets Her's desktop tools do here: the Mac's permissions (macOS checks hermeshd), not an assistant's. */}
      {m.local && m.her?.enabled && !st.status?.machineOnly && <ComputerUse machineId={m.machineId} />}
    </div>
  );
}

/**
 * Her on a machine (docs/her-server.md §11, phase 1): turned on with Touch ID (SetHer); turned off on a
 * sheet that asks whether her data goes too (§5.6). Windows can't run her yet. Turning her on also lets
 * her use the owner's computers (docs/her-mesh.md §3.3), and says so first.
 */
function HerRow({ m }: { m: Machine }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  // The switch's new state while hermeshd and the signer work on it.
  const [pending, setPending] = useState<boolean | null>(null);
  const on = pending ?? m.her?.enabled ?? false;
  // What it says of Her while it answers (Machine.her is kept only once Her was on there); none in its status:
  // its Hermesh is from before Her (docs/version-skew.md §6.7).
  const beforeHer = !!m.status && !m.status.her;
  const unsupported = beforeHer ? "" : ((m.status ? m.status.her?.unsupported : m.her?.unsupported) ?? "");
  // A PC whose Hermesh is from before her ran on Windows.
  const windows = unsupported === "windows";
  const set = async (v: boolean) => {
    if (!v) return st.set({ sheet: { kind: "herOff", machineId: m.machineId, machineName: m.machineName } });
    setPending(v);
    await st.run(() => local.setHer({ machineId: m.machineId, enabled: v }));
    setPending(null);
  };
  const eraseData = () =>
    st.set({
      sheet: {
        kind: "confirm",
        title: t("machines.herEraseTitle", { name: m.machineName }),
        message: t("machines.herEraseMessage", { name: m.machineName }),
        button: t("machines.herEraseButton"),
        run: () => local.setHer({ machineId: m.machineId, enabled: false, erase: true }),
        done: t("machines.herErased", { name: m.machineName }),
      },
    });
  const desc = beforeHer ? t("machines.herNeedsUpdate") : windows ? t("machines.herWindows") : unsupported ? t("machines.herUnsupported", { reason: unsupported }) : m.her?.enabled ? t("machines.herDesc") : <>{i18n.t("her:grant.runs")}<br />{i18n.t("her:grant.desc")}</>;
  return (
    <>
      <SRow title={t("machines.herRun")} desc={desc}>
        <Switch on={on} label={t("machines.herRun")} set={set} disabled={pending !== null || (!on && (beforeHer || windows || unsupported !== "")) || (!m.online && !m.local)} />
      </SRow>
      {/* Off, her data kept (docs/her-server.md §5.6): erased while the machine answers. */}
      {!on && herDataKept(m) && (
        <div className="frow">
          <span style={{ flex: 1 }}>{t("machines.herKept")}</span>
          {(m.online || m.local) && <button className="btn sm" onClick={eraseData}>{t("machines.herErase")}</button>}
        </div>
      )}
    </>
  );
}

