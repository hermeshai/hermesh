import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { TrayAction, TrayView } from "../../shared/bridge";
import { i18n } from "../../shared/i18n";
import { Mark } from "@hermesh/ui";
import { Icon } from "./icons";
import "./tray.css";

/**
 * The menu bar panel (#tray): Hermesh and whether agents can reach this Mac,
 * how many sessions need the owner and how many run (each opens the first
 * one), then the main window, settings and quit. The main process works out
 * what it shows (main/tray.ts).
 */
export function TrayPanel() {
  const { t } = useTranslation("common");
  const [v, setV] = useState<TrayView | null>(null);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    document.documentElement.classList.add("vibrant");
    window.hermesh?.trayView().then(setV);
    return window.hermesh?.onTrayView(setV);
  }, []);
  // The window takes the panel's height.
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => window.hermesh?.traySize(el.offsetHeight));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const go = (a: TrayAction) => window.hermesh?.trayAction(a);
  const ok = v?.state === "ok";
  const ready = v?.state === "ok" || v?.state === "down";
  return (
    <div className="tp" ref={box}>
      <div className="tp-head">
        <Mark size={18} />
        <span className="tp-word">Hermesh</span>
        {v && (
          <span className={`tp-chip ${ok ? "" : "off"}`} role="status">
            <i />
            {chip(v)}
          </span>
        )}
      </div>
      <div className="tp-line">{v ? line(v) : ""}</div>
      {ready && v && (
        <div className="tp-tiles">
          <Tile n={v.attention} label={t("tray.attention")} hot onClick={() => go("attention")} />
          <Tile n={v.running} label={t("tray.running")} onClick={() => go("running")} />
        </div>
      )}
      {v?.update && (
        <button className="tp-update" onClick={() => go("install")}>
          {t("tray.update", { version: v.update })}
        </button>
      )}
      <div className="tp-foot">
        <button className="tp-open" onClick={() => go("open")}>{t("tray.open")}</button>
        <button className="tp-ico" aria-label={t("tray.settings")} title={t("tray.settingsTitle")} onClick={() => go("settings")}>
          <Icon name="gear" />
        </button>
        <button className="tp-ico" aria-label={t("tray.quit")} title={t("tray.quit")} onClick={() => go("quit")}>
          <Icon name="power" />
        </button>
      </div>
    </div>
  );
}

function Tile({ n, label, hot, onClick }: { n: number; label: string; hot?: boolean; onClick: () => void }) {
  return (
    <button className={`tp-tile ${n === 0 ? "zero" : hot ? "hot" : ""}`} aria-label={`${label} ${n}`} onClick={onClick}>
      <span className="tp-n">{n}</span>
      <span className="tp-k">
        {label}
        <Icon name="chevR" className="sm" />
      </span>
    </button>
  );
}

function chip(v: TrayView): string {
  return i18n.t(`common:tray.chip.${v.state}`);
}

/** What the state means for the owner's agents. */
function line(v: TrayView): string {
  switch (v.state) {
    case "ok":
      return v.online ? i18n.t("common:tray.online", { count: v.online }) : i18n.t("common:tray.noneOnline");
    case "down":
      return i18n.t("common:tray.down");
    case "setup":
      return i18n.t("common:tray.setup");
    case "daemon":
      return i18n.t("common:tray.daemon");
  }
}
