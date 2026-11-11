import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { local } from "../api";
import { useStore } from "../store";
import { lacksGrant, reachable } from "./environments";

/**
 * Her on without her grant (docs/her-mesh.md §3.4): one line that lets her use the owner's computers.
 * SetHer once more signs the grant alone (one Touch ID); her machine then says she holds it, and the
 * line goes.
 */
export function GrantLine({ m }: { m: Machine }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const [busy, setBusy] = useState(false);
  if (!lacksGrant(m.her)) return null;
  const allow = async () => {
    setBusy(true);
    await st.run(() => local.setHer({ machineId: m.machineId, enabled: true }));
    setBusy(false);
  };
  return (
    <p className="set-note">
      <button className="link" disabled={busy || !reachable(m)} onClick={() => void allow()}>{t("grant.allow")}</button>
      <br />
      {t("grant.desc")}
    </p>
  );
}
