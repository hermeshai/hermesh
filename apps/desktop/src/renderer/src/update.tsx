import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Mark } from "@hermesh/ui";
import { newer, releaseNotesURL, type ReleaseNote, type UpdateInfo } from "../../shared/update";
import { Icon } from "./icons";
import { useNow } from "./store";
import "./update.css";

export function useUpdate(): UpdateInfo | undefined {
  const [info, setInfo] = useState<UpdateInfo>();
  useEffect(() => {
    let active = true, received = false;
    const off = window.hermesh?.onUpdate((i) => { received = true; setInfo(i); });
    window.hermesh?.getUpdate().then((i) => { if (active && !received) setInfo(i); });
    return () => { active = false; off?.(); };
  }, []);
  return info;
}

/** Quiet, version-scoped reminder above the account; opening it never restarts. */
export function UpdateReminder() {
  const { t } = useTranslation("updates");
  const info = useUpdate();
  const now = useNow(60_000).getTime();
  const u = info?.update;
  if (!info || !u || (u.state !== "ready" && u.state !== "available")) return null;
  if (info.snoozedVersion === u.version && info.snoozedUntil > now) return null;
  return (
    <button className="update-reminder" onClick={() => window.hermesh?.openUpdates()}>
      <Icon name="arrowD" />
      <span><b>{t(u.state === "ready" ? "ready" : "available")}</b><small>{t("version", { version: u.version })}</small></span>
      <Icon name="chevR" />
    </button>
  );
}

function ReleaseSummary({ note }: { note: ReleaseNote }) {
  const { t, i18n } = useTranslation("updates");
  const summary = note.summary[i18n.language === "zh-CN" ? "zh-CN" : "en"];
  return <article className="release-summary">
    <h2>{t("version", { version: note.version })}</h2>
    {(["improvements", "fixes"] as const).map((group) => summary[group].length > 0 && <section key={group}>
      <h3>{t(group)}</h3><ul>{summary[group].map((text, i) => <li key={i}>{text}</li>)}</ul>
    </section>)}
  </article>;
}

/** Fit either update window to its intrinsic content, with scrolling for long notes. */
function useUpdateWindowHeight() {
  const content = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const body = content.current;
    const scroll = body?.parentElement;
    const dialog = scroll?.parentElement;
    const title = dialog?.querySelector<HTMLElement>(".update-title");
    const actions = dialog?.querySelector<HTMLElement>(".update-actions");
    if (!body || !scroll || !title || !actions) return;
    let previous = 0;
    const fit = () => {
      const style = getComputedStyle(scroll);
      const height = Math.ceil(title.offsetHeight + body.offsetHeight + actions.offsetHeight + parseFloat(style.paddingTop) + parseFloat(style.paddingBottom));
      if (height !== previous) {
        previous = height;
        window.hermesh?.updateWindowHeight(height);
      }
    };
    // Measure intrinsic content, so resizing the scroll viewport never feeds back into its height.
    const observer = new ResizeObserver(fit);
    [body, title, actions].forEach((element) => observer.observe(element));
    fit();
    return () => observer.disconnect();
  }, []);
  return content;
}

export function UpdatesWindow() {
  const { t, i18n } = useTranslation("updates");
  const info = useUpdate();
  const content = useUpdateWindowHeight();
  const u = info?.update;
  const state = u?.state ?? "checking";
  const version = u && "version" in u ? u.version : info?.version ?? "";
  const ready = state === "ready";
  const available = state === "available";
  const busy = state === "checking" || state === "downloading";
  const blocked = ready && info?.approvalPending;
  const title = blocked ? t("approvalTitle") : state === "error" ? t("errorTitle") : state === "downloading" ? t("downloading", { version }) : t(state);
  const highlights = info?.notes.find((n) => n.version === version);
  const skipped = info?.notes.filter((n) => newer(n.version, info.version) && newer(version, n.version)) ?? [];
  const later = async () => { await window.hermesh?.snoozeUpdate(); window.close(); };
  return <main className="update-window update-dialog" aria-label={t("title")}>
    <header className="update-title drag">{t("title")}</header>
    <div className="update-scroll"><div className="update-content" ref={content}>
      <div className="update-heading"><Mark size={44} variant={busy ? "wait" : "seq"} /><div>
        <h1 role="status" aria-label={t("status")}>{title}</h1>
        <p>{ready || available || state === "downloading" ? t("transition", { current: info?.version, version }) : t("version", { version: info?.version ?? "" })}</p>
      </div></div>
      {u?.state === "error" ? <div className="update-error">
        <p>{t(`error.${u.reason}`)}</p>
        <details key={u.message}><summary>{t("details")}</summary><pre className="upd-raw">{u.message}</pre></details>
      </div> : <>
        {ready && <p>{t("readyBody", { version })}</p>}
        {available && <p>{t("availableBody", { version })}</p>}
        {state === "downloading" && <><div className="update-progress" role="progressbar" aria-label={t("downloading", { version })} /><p>{t("background")}</p></>}
        {blocked && <p className="update-callout">{t("approvalBody")}</p>}
        {(ready || available || state === "downloading") && <>
          {highlights ? <ReleaseSummary note={highlights} /> : <p className="update-muted">{t("notesMissing")}</p>}
          {skipped.length > 0 && <details className="update-skipped"><summary>{t("intermediate")}</summary>{skipped.map((n) => <ReleaseSummary key={n.version} note={n} />)}</details>}
          {ready && <div className="update-callout"><p>{t("installOnQuit")}</p><small>{t("laterHint")}</small></div>}
        </>}
        {state === "current" && <p className="update-muted">{info?.checkedAt ? t("checkedAt", { time: new Date(info.checkedAt).toLocaleString(i18n.language) }) : t("neverChecked")}</p>}
      </>}
      <div className="update-links">
        <button className="link" onClick={() => window.hermesh?.openWhatsNew()}>{t("history")}</button>
        <button className="link" onClick={() => window.hermesh?.openExternal(releaseNotesURL(version))}>{t("browser")} <Icon name="ext" /></button>
      </div>
    </div></div>
    <footer className="update-actions">
      <button className="btn" onClick={ready || available ? later : () => window.close()}>{t(ready || available ? "later" : busy || state === "error" ? "close" : "done")}</button>
      {blocked ? <button className="btn primary" onClick={() => window.hermesh?.returnToApproval()}>{t("returnToApproval")}</button>
        : ready ? <button className="btn primary" onClick={() => window.hermesh?.installUpdate()}>{t("restart")}</button>
        : available ? <button className="btn primary" onClick={() => window.hermesh?.downloadUpdate()}>{t("download")}</button>
        : state === "error" || state === "idle" ? <button className="btn primary" onClick={() => window.hermesh?.checkForUpdates()}>{t(state === "error" ? "retry" : "check")}</button> : null}
    </footer>
  </main>;
}

export function WhatsNewWindow() {
  const { t } = useTranslation("updates");
  const info = useUpdate();
  const content = useUpdateWindowHeight();
  return <main className="update-window update-dialog" aria-label={t("whatsNew")}>
    <header className="update-title drag">{t("whatsNew")}</header>
    <div className="update-scroll"><div className="update-content" ref={content}><h1>{t("whatsNew")}</h1>
      {info?.notes.length ? info.notes.map((n) => <ReleaseSummary key={n.version} note={n} />) : <p>{t("noHistory")}</p>}
      <div className="update-links"><button className="link" onClick={() => window.hermesh?.openExternal("https://github.com/hermeshai/hermesh/releases")}>{t("fullHistory")} <Icon name="ext" /></button></div>
    </div></div>
    <footer className="update-actions"><button className="btn" onClick={() => window.close()}>{t("done")}</button></footer>
  </main>;
}

export function AboutWindow() {
  const { t } = useTranslation("updates");
  return <main className="update-window about-window" aria-label={t("about")}>
    <header className="update-title drag">{t("about")}</header>
    <div className="update-scroll"><Mark size={64} /><h1>Hermesh</h1><p>{t("version", { version: window.hermesh?.version })}</p><p className="update-muted">{t("aboutBody")}</p>
      <div className="update-links"><button className="link" onClick={() => window.hermesh?.openUpdates(true)}>{t("check")}</button><button className="link" onClick={() => window.hermesh?.openWhatsNew()}>{t("whatsNew")}</button></div>
    </div>
  </main>;
}
