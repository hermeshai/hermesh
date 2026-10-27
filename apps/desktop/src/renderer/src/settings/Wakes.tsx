import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Code, ConnectError } from "@connectrpc/connect";
import type { Client, Wake } from "@hermesh/proto/local";
import { agoText, dateOf } from "@hermesh/core";
import { local } from "../api";
import { wakeReason } from "../sheets/Wake";
import { useNow, useStore } from "../store";
import { GoRow } from "./common";

/**
 * An agent's doorbells (docs/agent-names.md §7), on its Access page: the
 * webhooks Hermesh calls when messages wait for the agent or one of its
 * bots, each opening its sheet, and Add… for another. A relay without the
 * MCP gateway keeps none: the group is left out.
 */
export function Wakes({ c }: { c: Client }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const now = useNow(60_000);
  const [wakes, setWakes] = useState<Wake[]>();
  const [kept, setKept] = useState(true);
  const load = useCallback(() => {
    local.listWakes({ clientId: c.clientId }).then(
      (r) => setWakes(r.wakes),
      (e) => ConnectError.from(e).code === Code.Unimplemented && setKept(false),
    );
  }, [c.clientId]);
  // Again whenever a sheet closes: one may have changed them.
  useEffect(() => {
    if (!st.sheet) load();
  }, [st.sheet, load]);
  if (!kept || !wakes) return null;
  const open = (w?: Wake) => st.set({ sheet: { kind: "wake", clientId: c.clientId, agent: c.label, wake: w } });
  return (
    <>
      <div>
        <div className="fhead">{t("wake.title")}</div>
        <div className="fgroup" role="list" aria-label={t("wake.title")}>
          {wakes.length ? (
            wakes.map((w) => {
              const rang = dateOf(w.lastRingAt);
              const sub = w.state === "on" ? (rang ? t("wake.lastRang", { ago: agoText(rang, now) }) : t("wake.notYet")) : wakeReason(w.lastStatus, w.reason);
              return (
                <GoRow key={w.name} icon={<span className="wk-av">{(w.name || c.label).slice(0, 1).toUpperCase()}</span>}
                  title={w.name || t("wake.self", { agent: c.label })} sub={sub} onClick={() => open(w)}>
                  <span className={w.state === "on" ? "wk-state" : "wk-state bad"}>
                    <i />
                    {w.state === "on" ? t("wake.on") : t("wake.broken")}
                  </span>
                </GoRow>
              );
            })
          ) : (
            <div className="frow muted">{t("wake.none")}</div>
          )}
        </div>
      </div>
      <div className="set-acts">
        <button className="btn" onClick={() => open()}>{t("wake.add")}</button>
      </div>
      <p className="set-note">{t("wake.note")}</p>
    </>
  );
}
