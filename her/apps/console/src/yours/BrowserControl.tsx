import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { RpcClient } from "@hermesh/her-client";
import type { ConfigEdit } from "@hermesh/her-protocol";
import { Switch } from "../customize/parts";
import type { BrowserState } from "./useBrowser";
import { browserName, controlConfig, controlEdits, controlOn, defaultBrowser, defaultEdits, yours, yoursConfig, type DefaultBrowser } from "./yours";

// The Browser page's first rows: whether Her controls browsers at all (`browser.enabled`), and where new pages
// open when the agent names no browser (`browser.default`). The owner's Chrome can be chosen only while Her uses
// it and it is connected; until then pages open in the built-in browser, as her does.

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));

export function BrowserControl({ rpc, browser }: { rpc: RpcClient | null; browser: BrowserState }) {
  const { t } = useTranslation("yours");
  const { status, readConfig } = browser;
  const control = useMemo(() => controlConfig(browser.config), [browser.config]);
  const y = yours(status, browser.config === null ? null : yoursConfig(browser.config));
  const on = controlOn(status, control, browser.config === null ? null : yoursConfig(browser.config));
  const chosen = defaultBrowser(control, y.state);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const write = async (edits: ConfigEdit[]) => {
    if (!rpc) return;
    setBusy(true);
    setError("");
    try {
      await rpc.request("config/write", { layer: "user", edits });
      await readConfig();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const unread = !rpc || browser.config === null || busy;
  return (
    <section className="browser-control">
      <div className="fgroup">
        <div className="frow">
          <div className="ft">
            <div className="fn">{t("control.title")}</div>
            <div className="fdesc">{t("control.desc")}</div>
          </div>
          <Switch on={on} label={t("control.title")} disabled={unread} onChange={(v) => void write(controlEdits(v))} />
        </div>
        <div className="frow">
          <div className="ft">
            <label className="fn" htmlFor="browser-default">
              {t("control.default")}
            </label>
            <div className="fdesc">{t(y.state === "on" ? "control.defaultDesc" : "control.defaultLocked")}</div>
          </div>
          <select id="browser-default" className="psel" value={chosen} disabled={unread || !on || y.state !== "on"} onChange={(e) => void write(defaultEdits(e.target.value as DefaultBrowser))}>
            <option value="builtin">{t("control.builtin")}</option>
            {y.state === "on" && <option value="yours">{t("control.yours", { name: browserName(y.connected?.browser) })}</option>}
          </select>
        </div>
      </div>
      {error && <p className="set-note warn">{error}</p>}
    </section>
  );
}
