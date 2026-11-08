import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { local } from "../api";
import { computerUseView, parseDesktopStatus, type DesktopStatus } from "./status";
import "./computer.css";

// Computer use, in the Her entry's settings while the environment is this Mac
// (docs/her-server.md §4.4): whether macOS lets Her's desktop tools work, and
// a way to each missing permission's pane of System Settings.

/** her's desktop/status on a machine, asked again whenever the window comes back to the front (from System Settings); null until known, or when her can't say. */
export function useComputerUse(machineId: string): DesktopStatus | null {
  const [status, setStatus] = useState<DesktopStatus | null>(null);
  useEffect(() => {
    let live = true;
    const ask = () =>
      local.herCall({ machineId, call: { request: { method: "desktop/status" } } }).then(
        (r) => live && setStatus(parseDesktopStatus(r.response?.result, !!r.response?.error)),
        () => live && setStatus(null),
      );
    ask();
    window.addEventListener("focus", ask);
    return () => {
      live = false;
      window.removeEventListener("focus", ask);
    };
  }, [machineId]);
  return status;
}

/** The permissions computer use needs on this Mac, each allowed or a button to its pane; nothing while her can't say. */
export function ComputerUse({ machineId }: { machineId: string }) {
  const { t } = useTranslation("computer");
  const view = computerUseView(useComputerUse(machineId), window.hermesh?.macos ?? 0);
  if (view.kind === "none") return null;
  const note = view.kind === "missing" ? "" : !view.enabled ? t("off") : view.rows.some((r) => !r.granted) ? t("note") : t("intro");
  return (
    <section className="cu">
      <div className="fhead">{t("title")}</div>
      <div className="fgroup">
        {view.kind === "missing" ? (
          <div className="frow">
            <div className="cu-text fdesc">{t("missing")}</div>
          </div>
        ) : (
          view.rows.map((r) => (
            <div className="frow" key={r.kind}>
              <div className="cu-text">
                <div>{t(r.list)}</div>
                <div className="fdesc">{t(`${r.kind}Desc`)}</div>
              </div>
              {r.granted ? (
                <span className="cu-state">{t("granted")}</span>
              ) : (
                <button className="btn" onClick={() => window.hermesh?.openComputerPermission(r.kind)}>
                  {t("open")}
                </button>
              )}
            </div>
          ))
        )}
      </div>
      {note && <p className="cu-note">{note}</p>}
    </section>
  );
}
