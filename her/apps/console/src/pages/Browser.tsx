import { useState } from "react";
import { useTranslation } from "react-i18next";
import { PageTop } from "../components";
import { errorText } from "../format";
import { useConfig, useHer } from "../her";
import { Icon } from "../icons";
import { BuiltinBrowser } from "../yours/BuiltinBrowser";
import { useBrowser } from "../yours/useBrowser";
import { YourBrowser } from "../yours/YourBrowser";
import { siteKey, siteLevels, siteOrigin, sitesOf, type SiteLevel } from "./settings";

// Browser: the owner's own Chrome (YourBrowser), Her's own browser (BuiltinBrowser), then the sites Her may open without asking
// (`[browser] sites` of her's user file), each with its level, which can be changed or removed, and a site added by hand.
// "All sites" comes first; Her's "Always allow" adds sites here too.

export function BrowserPage() {
  const { t } = useTranslation();
  const her = useHer();
  const browser = useBrowser(her.rpc);
  const { config, failed, read } = useConfig();
  const sites = config === null ? null : sitesOf(config);
  const [site, setSite] = useState("");
  const [level, setLevel] = useState<SiteLevel>("act");
  const [bad, setBad] = useState(false);
  const write = async (edits: { key_path: string; value: unknown }[]) => {
    try {
      await her.call("config/write", { layer: "user", edits });
      await read();
      return true;
    } catch (e) {
      her.show(errorText(e), undefined, true);
      return false;
    }
  };
  const name = (origin: string) => (origin === "*" ? t("browser.allSites") : origin.replace(/^https:\/\//, ""));
  const add = async () => {
    const typed = site;
    const origin = siteOrigin(typed);
    if (!origin) return setBad(true);
    if (await write([{ key_path: siteKey(origin), value: level }])) {
      // What was typed meanwhile stays.
      setSite((now) => (now === typed ? "" : now));
      her.show(t("browser.added", { site: name(origin), level: t(`browser.level.${level}`) }));
    }
  };

  return (
    <>
      <PageTop title={t("nav.browser")} />
      <div className="set-body">
        <div className="form browser-set">
          <YourBrowser rpc={her.rpc} browser={browser} />
          <BuiltinBrowser rpc={her.rpc} browser={browser} />
          <section>
            <h3 className="fhead">{t("browser.sites")}</h3>
            <div className="fgroup">
              {sites === null && <div className="frow">{failed ? <span className="fdesc warn">{t("failedRead", { error: failed })}</span> : <span className="mini-spin" />}</div>}
              {sites?.length === 0 && (
                <div className="frow">
                  <div className="ft">
                    <div className="fdesc">{t("browser.sitesEmpty")}</div>
                  </div>
                </div>
              )}
              {sites?.map(([origin, value]) => {
                const all = origin === "*";
                return (
                  <div className={`frow mrow site-row ${value === "deny" ? "denied" : ""}`} key={origin}>
                    <span className="site-icon">
                      <Icon name="web" />
                    </span>
                    <div className="ft">
                      <div className={`fn ${all ? "" : "mono"}`} title={all ? undefined : origin}>
                        {name(origin)}
                      </div>
                      {all && <div className="fdesc">{t("browser.allSitesDesc")}</div>}
                    </div>
                    <select className="psel" aria-label={t("browser.siteLevel", { site: name(origin) })} value={value} onChange={(e) => void write([{ key_path: siteKey(origin), value: e.target.value }])}>
                      {siteLevels
                        .filter((l) => !all || l !== "deny")
                        .map((l) => (
                          <option key={l} value={l}>
                            {t(`browser.level.${l}`)}
                          </option>
                        ))}
                    </select>
                    <button className="tb" title={t("browser.removeSite", { site: name(origin) })} aria-label={t("browser.removeSite", { site: name(origin) })} onClick={() => void write([{ key_path: siteKey(origin), value: null }])}>
                      <Icon name="trash" />
                    </button>
                  </div>
                );
              })}
              <form
                className="frow site-add"
                onSubmit={(e) => {
                  e.preventDefault();
                  void add();
                }}
              >
                <input
                  className={`field mono ${bad ? "bad" : ""}`}
                  value={site}
                  placeholder={t("browser.addPlaceholder")}
                  aria-label={t("browser.addLabel")}
                  spellCheck={false}
                  autoComplete="off"
                  onChange={(e) => (setSite(e.target.value), setBad(false))}
                />
                <select className="psel" aria-label={t("browser.addLevel")} value={level} onChange={(e) => setLevel(e.target.value as SiteLevel)}>
                  {siteLevels.map((l) => (
                    <option key={l} value={l}>
                      {t(`browser.level.${l}`)}
                    </option>
                  ))}
                </select>
                <button type="submit" className="btn" disabled={!site.trim() || sites === null}>
                  {t("browser.add")}
                </button>
              </form>
            </div>
            <p className={`set-note ${bad ? "warn" : ""}`}>{bad ? t("browser.badSite") : t("browser.sitesNote")}</p>
          </section>
        </div>
      </div>
    </>
  );
}
