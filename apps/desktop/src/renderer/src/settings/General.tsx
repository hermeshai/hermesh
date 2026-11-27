import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { pickLanguage } from "@hermesh/core";
import type { Appearance, Preferences } from "../../../shared/bridge";
import { languages, resolveLanguage, type LanguageChoice } from "../../../shared/i18n";
import { useIsDark } from "../appearance";
import { useUpdate } from "../update";
import { SRow, Switch } from "./common";

/** The preferences that are switches. */
type Flag = { [K in keyof Preferences]-?: Preferences[K] extends boolean ? K : never }[keyof Preferences];

const appearances: Appearance[] = ["system", "light", "soft", "dark", "black"];

function MiniAnatomy() {
  return (
    <>
      <span className="mw-side">
        <span className="mw-dots"><i /><i /><i /></span>
        <span className="mw-nav sel" />
        <span className="mw-nav" />
        <span className="mw-nav short" />
      </span>
      <span className="mw-body">
        <span className="mw-bub" />
        <span className="mw-l1" />
        <span className="mw-code" />
        <span className="mw-pill"><i /></span>
      </span>
    </>
  );
}

/** General: the appearance and language, and updates (docs/settings-environments.md §5.8). */
export function General() {
  const { t } = useTranslation("settings");
  const isDark = useIsDark();
  // Kept by the main process, which acts on them while no window is open.
  const [prefs, setPrefs] = useState<Preferences>();

  useEffect(() => {
    window.hermesh?.getPreferences().then(setPrefs);
    return window.hermesh?.onPreferences(setPrefs);
  }, []);

  const prefLight = prefs?.preferredLight ?? "light";
  const prefDark = prefs?.preferredDark ?? "dark";

  const look = (a: Appearance) => {
    const active = prefs?.appearance === a;
    const paired = prefs?.appearance === "system" && (a === prefLight || a === prefDark);
    return (
      <button
        key={a}
        type="button"
        className={`look-card ${active ? "on" : ""} ${paired ? "paired" : ""}`}
        aria-pressed={active}
        disabled={!prefs}
        onClick={() => window.hermesh?.setPreferences({ appearance: a }).then(setPrefs)}
      >
        {a === "system" ? (
          <span className={`look-stage sys-stage ${isDark ? "night-now" : ""}`} aria-hidden="true">
            <span className={`sys-win day m-${prefLight}`}><MiniAnatomy /></span>
            <span className={`sys-win night m-${prefDark}`}><MiniAnatomy /></span>
          </span>
        ) : (
          <span className="look-stage" aria-hidden="true">
            <span className={`mini-win m-${a}`}><MiniAnatomy /></span>
          </span>
        )}
        <span className="look-cap">
          <span>{t(`appearance.${a}`)}</span>
          <i className="look-pair-dot" aria-hidden="true" />
        </span>
      </button>
    );
  };

  return (
    <div className="form">
      <div className="fgroup">
        <div className="look-block">
          <div className="look-head">{t("appearance.title")}</div>
          <div className="set-look" role="group" aria-label={t("appearance.title")}>
            {appearances.map(look)}
          </div>
        </div>
        <Language prefs={prefs} setPrefs={setPrefs} />
      </div>
      <Updates />
    </div>
  );
}

/** Notifications: what Hermesh tells about, and the menu bar's dot (docs/settings-environments.md §5.8). */
export function NotificationsPage() {
  const { t } = useTranslation("settings");
  const [prefs, setPrefs] = useState<Preferences>();
  useEffect(() => {
    window.hermesh?.getPreferences().then(setPrefs);
  }, []);
  const pref = (k: Flag, title: string, desc?: string) => (
    <SRow title={title} desc={desc}>
      <Switch on={prefs?.[k] ?? false} label={title} disabled={!prefs} set={(on) => window.hermesh?.setPreferences({ [k]: on }).then(setPrefs)} />
    </SRow>
  );
  return (
    <div className="form">
      <p className="set-foot">{t("general.quietWhileViewing")}</p>
      <section>
        <h2 className="fhead">{t("general.sessions")}</h2>
        <div className="fgroup">
          {pref("notifyHerReply", t("general.herReply"))}
          {pref("notifyResult", t("general.sessionResults"), t("general.resultsDesc"))}
          {pref("notifyStarted", t("general.sessionStarted"))}
        </div>
      </section>
      <section>
        <h2 className="fhead">{t("general.needsYou")}</h2>
        <div className="fgroup">
          {pref("notifyHerRequest", t("general.herRequest"))}
          {pref("notifyQuestion", t("general.sessionRequests"), t("general.requestsDesc"))}
          <SRow title={t("general.connectionRequests")} desc={t("general.connectionRequestsDesc")}>
            <Switch on label={t("general.connectionRequests")} disabled />
          </SRow>
        </div>
      </section>
      <div>
        <h2 className="fhead">{t("general.menuBar")}</h2>
        <div className="fgroup">{pref("menuBadge", t("general.menuBadgeTitle"))}</div>
        <p className="set-foot">{t("general.menuBadge")}</p>
      </div>
    </div>
  );
}

/**
 * The app's language: the system's (named after the language it gives now)
 * or one of the app's, each named in its own language. A new choice takes
 * effect when the app next starts; until then, a way to restart now.
 */
function Language({ prefs, setPrefs }: { prefs?: Preferences; setPrefs: (p: Preferences) => void }) {
  const { t } = useTranslation("settings");
  const running = window.hermesh?.language;
  const system = pickLanguage(navigator.languages);
  const restart = prefs && running && resolveLanguage(prefs.language, navigator.languages) !== running;
  return (
        <SRow
          title={t("language.title")}
          desc={
            restart && (
              <>
                {t("language.restartHint")}{" "}
                <button className="link" onClick={() => window.hermesh?.relaunch()}>{t("language.restartNow")}</button>
              </>
            )
          }
        >
          <select
            className="psel"
            aria-label={t("language.title")}
            value={prefs?.language ?? "system"}
            disabled={!prefs}
            onChange={(e) => window.hermesh?.setPreferences({ language: e.target.value as LanguageChoice }).then(setPrefs)}
          >
            <option value="system">{t("language.system", { name: t(`language.names.${system}`) })}</option>
            {languages.map((l) => (
              <option key={l} value={l}>{t(`language.names.${l}`)}</option>
            ))}
          </select>
        </SRow>
  );
}

/** Update status lives in its own window; General keeps only the download preference. */
function Updates() {
  const { t } = useTranslation("settings");
  const info = useUpdate();
  return <div><div className="fgroup">
    <SRow title={t("update.autoDownload")}>
      <Switch label={t("update.autoDownload")} on={info?.autoDownload ?? true} disabled={!info || info.update.state === "unsupported"} set={(on) => { void window.hermesh?.setAutoDownload(on); }} />
    </SRow>
  </div><p className="set-foot">{t("update.autoDownloadDesc")}</p></div>;
}
