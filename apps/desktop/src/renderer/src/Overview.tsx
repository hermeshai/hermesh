import { useState } from "react";
import { useTranslation } from "react-i18next";
import { SidebarToggle } from "./MainWindow";
import { Icon } from "./icons";
import { Attention } from "./overview/Attention";
import { LinkMap } from "./overview/Map";
import { dayText, Usage, useUsage } from "./overview/Usage";
import { Working } from "./overview/Working";
import { useNow, useStore } from "./store";
import "./overview/overview.css";

// The overview: what needs the owner beyond the session list, the link map of grants, machines and projects, what runs now, and a year of usage.
export function Overview() {
  const { t } = useTranslation("overview");
  const st = useStore();
  const now = useNow(30_000);
  // A node picked on the map (a grant, machine or project) narrows Working Now to the sessions through it.
  const [pick, setPick] = useState("");
  const usage = useUsage(now);
  return (
    <>
      <div className="ctool drag">
        <SidebarToggle />
        <div className="ctitle" />
      </div>
      <div className="cscroll">
        <div className="ov">
          <div className="ov-h">
            <div className="ov-t">
              <h1>{t("title")}</h1>
              <span>{dayText(now)}</span>
            </div>
          </div>
          {st.sessionsUnreachable.length > 0 && (
            <div className="ov-warn" role="status">
              <Icon name="wifi" />
              {t("unreachable.line", {
                machines: st.sessionsUnreachable.map((u) => t("unreachable.machine", { name: u.machineName, error: u.error })).join(t("listSeparator")),
              })}
            </div>
          )}
          <Attention now={now} />
          <section aria-label={t("map.title")}>
            <LinkMap pick={pick} onPick={setPick} now={now} usage={usage} />
          </section>
          <Working pick={pick} onClear={() => setPick("")} />
          <Usage now={now} res={usage} />
        </div>
      </div>
    </>
  );
}
