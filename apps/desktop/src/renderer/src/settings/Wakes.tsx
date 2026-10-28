import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Code, ConnectError } from "@connectrpc/connect";
import type { Client, Wake } from "@hermesh/proto/local";
import { agoText, dateOf } from "@hermesh/core";
import { agentName } from "@hermesh/ui";
import { local } from "../api";
import { GrantAvatar } from "../components";
import { wakeReason } from "../sheets/Wake";
import { useNow, useStore } from "../store";
import { GoRow } from "./common";

/**
 * An agent's doorbells (docs/agent-names.md §7), on its Access page: the
 * webhook Hermesh calls to wake the agent when messages wait for it, set up
 * in a sheet, and, for a connection several bots share, one per bot. A relay
 * without the MCP gateway keeps none: the group is left out.
 */
export function Wakes({ c }: { c: Client }) {
  const { t } = useTranslation("settings");
  const st = useStore();
  const now = useNow(60_000);
  const [wakes, setWakes] = useState<Wake[]>();
  const [kept, setKept] = useState(true);
  const load = useCallback(() => {
    local.listWakes({ clientId: c.clientId }).then(
      (r) => (setWakes(r.wakes), setKept(true)),
      (e) => ConnectError.from(e).code === Code.Unimplemented && setKept(false),
    );
  }, [c.clientId]);
  // Again whenever a sheet closes (one may have changed them), and whenever
  // hermeshd is back: an app just updated talks to the previous version's,
  // which keeps none, until it restarts it.
  const up = !!st.status;
  useEffect(() => {
    if (!st.sheet && up) load();
  }, [st.sheet, load, up]);
  if (!kept || !wakes) return null;
  const agent = agentName(c.label);
  const open = (w?: Wake, named?: boolean) => st.set({ sheet: { kind: "wake", clientId: c.clientId, agent, wake: w, named } });
  return (
    <>
      <div>
        <div className="fhead">{t("wake.title")}</div>
        <div className="fgroup" role="list" aria-label={t("wake.title")}>
          {wakes.map((w) => {
            const rang = dateOf(w.lastRingAt);
            const sub =
              w.state !== "on" ? wakeReason(w.lastStatus, w.reason)
              : !rang ? t("wake.notYet")
              : `${t("wake.lastRang", { ago: agoText(rang, now) })} · ${w.waiting ? t("wake.waitingCount", { count: w.waiting }) : t("wake.allRead")}`;
            return (
              <GoRow key={w.name} icon={w.name ? <span className="wk-av">{w.name.slice(0, 1).toUpperCase()}</span> : <GrantAvatar label={c.label} size="sm" />}
                title={t("wake.row", { name: w.name || agent })} sub={sub} onClick={() => open(w)}>
                <span className={w.state === "on" ? "wk-state" : "wk-state bad"}>
                  <i />
                  {w.state === "on" ? t("wake.on") : t("wake.broken")}
                </span>
              </GoRow>
            );
          })}
          {/* The agent's own, which wakes it for every name without one, while it has none. */}
          {!wakes.some((w) => w.name === "") && (
            <div className="frow">
              <span className="fl">{wakes.length ? t("wake.noneOwn", { agent }) : t("wake.none")}</span>
              <span className="fv">
                <button className="btn sm" onClick={() => open()}>{t("wake.setUp")}</button>
              </span>
            </div>
          )}
        </div>
      </div>
      <p className="set-note">
        {t("wake.note")}
        {t("wake.perBotAsk")} <button className="link" onClick={() => open(undefined, true)}>{t("wake.perBot")}</button>
      </p>
    </>
  );
}
