import { useState } from "react";
import { useTranslation } from "react-i18next";
import { PageTop } from "../components";
import { errorText } from "../format";
import { useConfig, useHer } from "../her";
import { Icon } from "../icons";
import { BrowserControl } from "../yours/BrowserControl";
import { BuiltinBrowser } from "../yours/BuiltinBrowser";
import { useBrowser } from "../yours/useBrowser";
import { controlConfig } from "../yours/yours";
import { YourBrowser } from "../yours/YourBrowser";
import { allSitesEdits, allSitesLevels, allSitesOf, exceptionsOf, siteKey, siteLevels, siteOrigin, type AllSitesLevel, type SiteLevel } from "./settings";

// Browser: whether Her controls browsers and where new pages open (BrowserControl), the owner's own Chrome
// (YourBrowser), the sites Her may open without asking (`[browser] sites` of her's user file), then Her's own
// browser (BuiltinBrowser). "All sites" comes first and always shows, asking unless set; the sites set apart
// from it follow, each with its level, which can be changed or removed, and a site added by hand. Her's
// "Always allow" adds sites there too. While browser control is turned off, the rest shows dimmed.

/** `item` "connect": opened to connect the owner's Chrome (the Mac app's chat sends the owner here), the guide open. */
export function BrowserPage({ item, go }: { item: string | null; go: (item?: string) => void }) {
  const { t } = useTranslation();
  const her = useHer();
  const browser = useBrowser(her.rpc);
  const { config, failed, read } = useConfig();
  const exceptions = config === null ? null : exceptionsOf(config);
  const all = config === null ? null : allSitesOf(config);
  // Turned off in so many words: not set, connecting the owner's Chrome is what turns it on.
  const controlling = controlConfig(browser.config).enabled !== false;
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
  const name = (origin: string) => origin.replace(/^https:\/\//, "");
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
          <BrowserControl rpc={her.rpc} browser={browser} />
          <div className={`browser-rest ${controlling ? "" : "off"}`} inert={!controlling}>
            <YourBrowser rpc={her.rpc} browser={browser} connect={item === "connect"} onConnectOpened={() => go()} />
            <section>
              <h3 className="fhead">{t("browser.sites")}</h3>
              <div className="fgroup">
                {all === null ? (
                  <div className="frow">{failed ? <span className="fdesc warn">{t("failedRead", { error: failed })}</span> : <span className="mini-spin" />}</div>
                ) : (
                  <div className="frow mrow site-row">
                    <span className="site-icon">
                      <Icon name="web" />
                    </span>
                    <div className="ft">
                      <div className="fn">{t("browser.allSites")}</div>
                      <div className="fdesc">{t(`browser.allSitesDesc.${all}`)}</div>
                    </div>
                    <select className="psel" aria-label={t("browser.siteLevel", { site: t("browser.allSites") })} value={all} onChange={(e) => void write(allSitesEdits(e.target.value as AllSitesLevel))}>
                      {allSitesLevels.map((l) => (
                        <option key={l} value={l}>
                          {t(`browser.level.${l}`)}
                        </option>
                      ))}
                    </select>
                  </div>
                )}
              </div>
              <h3 className="fhead site-exceptions">{t("browser.exceptions")}</h3>
              <div className="fgroup">
                {exceptions?.length === 0 && (
                  <div className="frow">
                    <div className="ft">
                      <div className="fdesc">{t("browser.exceptionsEmpty")}</div>
                    </div>
                  </div>
                )}
                {exceptions?.map(([origin, value]) => (
                  <div className={`frow mrow site-row ${value === "deny" ? "denied" : ""}`} key={origin}>
                    <div className="ft">
                      <div className="fn mono" title={origin}>
                        {name(origin)}
                      </div>
                    </div>
                    <select className="psel" aria-label={t("browser.siteLevel", { site: name(origin) })} value={value} onChange={(e) => void write([{ key_path: siteKey(origin), value: e.target.value }])}>
                      {siteLevels.map((l) => (
                        <option key={l} value={l}>
                          {t(`browser.level.${l}`)}
                        </option>
                      ))}
                    </select>
                    <button className="tb" title={t("browser.removeSite", { site: name(origin) })} aria-label={t("browser.removeSite", { site: name(origin) })} onClick={() => void write([{ key_path: siteKey(origin), value: null }])}>
                      <Icon name="trash" />
                    </button>
                  </div>
                ))}
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
                  <button type="submit" className="btn" disabled={!site.trim() || exceptions === null}>
                    {t("browser.add")}
                  </button>
                </form>
              </div>
              <p className={`set-note ${bad ? "warn" : ""}`}>{bad ? t("browser.badSite") : t("browser.sitesNote")}</p>
            </section>
            <BuiltinBrowser browser={browser} />
          </div>
        </div>
      </div>
    </>
  );
}
