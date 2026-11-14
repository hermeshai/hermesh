import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { RpcClient } from "@hermesh/her-client";
import { errorText } from "../store";
import { Icon } from "../icons";

// Above the composer while the conversation waits for the owner's own Chrome: the agent asked for it (a site that
// needs their sign-ins) and its extension is not connected. her holds the step while someone can tell the owner
// (browser/status's `waiting`); the owner connects Chrome in the Her console's guide, or goes on without it this
// time (browser/cancel_wait), which the agent hears as their choice.

/** Whether `session` waits for the owner's Chrome to connect: browser/status, then browser/changed. */
export function useWaitsForChrome(rpc: RpcClient | null, session: string | null): boolean {
  const [waiting, setWaiting] = useState<string[]>([]);
  useEffect(() => {
    if (!rpc) return;
    let live = true;
    const off = rpc.on("browser/changed", (s) => live && setWaiting(s.waiting ?? []));
    rpc.request("browser/status", {}).then(
      (s) => live && setWaiting(s.waiting ?? []),
      () => {},
    );
    return () => {
      live = false;
      off();
    };
  }, [rpc]);
  return !!session && waiting.includes(session);
}

export function ChromeWait({ rpc, session, onConnect }: { rpc: RpcClient; session: string; onConnect: () => void }) {
  const { t } = useTranslation("her");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const without = async () => {
    setBusy(true);
    setError("");
    try {
      await rpc.request("browser/cancel_wait", { session_id: session });
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
    }
  };
  return (
    <div className="appr" role="region" aria-label={t("chrome.title")}>
      <div className="knote-h">
        <Icon name="web" className="knote-i" />
        <b>{t("chrome.title")}</b>
      </div>
      <p className="knote-b">{error || t("chrome.body")}</p>
      <div className="appr-f">
        <span className="appr-sp" />
        <button className="btn" disabled={busy} onClick={() => void without()}>
          {t("chrome.builtin")}
        </button>
        <button className="btn primary" disabled={busy} onClick={onConnect}>
          {t("chrome.connect")}
        </button>
      </div>
    </div>
  );
}
