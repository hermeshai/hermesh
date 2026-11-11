import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { RpcClient } from "@hermesh/her-client";
import { Switch } from "../customize/parts";
import type { BrowserState } from "./useBrowser";
import { builtin, builtinConfig, builtinEdits, herSystem, programName } from "./yours";

// The console's Browser section, its second part: the built-in browser, a browser of Her's own on
// the computer she runs on, without the owner's sign-ins: on or off, the program it starts, and
// what to install where there is none. A server without a desktop runs it without a window.

/** Google Chrome for Linux comes as a .deb for x86-64 only. */
const LINUX_INSTALL = "wget https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb\nsudo apt install ./google-chrome-stable_current_amd64.deb";

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));

export function BuiltinBrowser({ rpc, browser }: { rpc: RpcClient | null; browser: BrowserState }) {
  const { t } = useTranslation("yours");
  const { status, readConfig } = browser;
  const config = useMemo(() => builtinConfig(browser.config), [browser.config]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const b = builtin(status, config);
  const system = herSystem(status);
  const computer = t(`computer.${system}`);

  const turn = async (on: boolean) => {
    if (!rpc) return;
    setBusy(true);
    setError("");
    try {
      await rpc.request("config/write", { layer: "user", edits: builtinEdits(on) });
      await readConfig();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="builtin">
      <h3 className="fhead">{t("builtin.title")}</h3>
      <div className="fgroup">
        <div className="frow">
          <div className="ft">
            <div className="fn" title={b.program ?? undefined}>
              {b.program ? programName(b.program) : t("builtin.none")}
            </div>
            <div className={`fdesc ${!b.program && status ? "warn" : ""}`}>
              {b.program ? t("builtin.desc", { program: programName(b.program), computer, how: t(b.window ? "builtin.window" : "builtin.noWindow") }) : status ? t("builtin.missing", { computer }) : ""}
            </div>
          </div>
          <Switch on={b.on} label={t("builtin.title")} disabled={!rpc || browser.config === null || busy} onChange={(on) => void turn(on)} />
        </div>
      </div>
      {status && !b.program && system === "linux" && (
        <div className="set-note builtin-install">
          {t("builtin.linuxInstall")}
          <pre className="sel-text">{LINUX_INSTALL}</pre>
        </div>
      )}
      {error && <p className="set-note warn">{error}</p>}
    </section>
  );
}
