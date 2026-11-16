import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import type { BrowserState } from "./useBrowser";
import { builtin, builtinConfig, herSystem, programName } from "./yours";

// The console's Browser section, its last part: the built-in browser, a browser of Her's own on
// the computer she runs on, without the owner's sign-ins: the program it starts, and what to
// install where there is none. A server without a desktop runs it without a window. Whether it
// is on goes with browser control, at the top of the page.

/** Google Chrome for Linux comes as a .deb for x86-64 only. */
// Google Chrome's package for the computer's own architecture: x86-64 or, since 2026, arm64.
const LINUX_INSTALL =
  "wget https://dl.google.com/linux/direct/google-chrome-stable_current_$(dpkg --print-architecture).deb\nsudo apt install ./google-chrome-stable_current_$(dpkg --print-architecture).deb";

export function BuiltinBrowser({ browser }: { browser: BrowserState }) {
  const { t } = useTranslation("yours");
  const { status } = browser;
  const config = useMemo(() => builtinConfig(browser.config), [browser.config]);
  const b = builtin(status, config);
  const system = herSystem(status);
  const computer = t(`computer.${system}`);

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
        </div>
      </div>
      {status && !b.program && system === "linux" && (
        <div className="set-note builtin-install">
          {t("builtin.linuxInstall")}
          <pre className="sel-text">{LINUX_INSTALL}</pre>
        </div>
      )}
    </section>
  );
}
