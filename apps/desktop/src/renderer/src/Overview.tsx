import { useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { agoText, dateOf } from "@hermesh/core";
import { SidebarToggle } from "./MainWindow";
import { Icon } from "./icons";
import { Attention } from "./overview/Attention";
import { LinkMap } from "./overview/Map";
import { dayText, Usage, useUsage } from "./overview/Usage";
import { Working } from "./overview/Working";
import { useNow, useStore } from "./store";
import "./overview/overview.css";

// The overview: what needs the owner beyond the session list, the link map of grants, machines and projects, what runs now, and a year of usage.
// On a machine only, the same for this Mac: nothing waits on it, and the map is its own.
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
          {st.sessionsUnreachable.length > 0 && <Unreachable now={now} />}
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

/** Machines whose sessions can't be listed now: offline, or not answering (as the iPhone says it). */
function Unreachable({ now }: { now: Date }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const gone = st.sessionsUnreachable.map((u) => {
    const m = st.machines.find((x) => x.machineId === u.machineId);
    return { name: u.machineName, online: m?.online ?? false, seen: dateOf(m?.lastSeen) };
  });
  const g = gone[0];
  const many = gone.length > 1;
  const key = many ? "unreachable.many" : g.online ? "unreachable.notResponding" : "unreachable.offline";
  const more = many
    ? gone.map((x) => x.name).join(t("listSeparator"))
    : g.online
      ? t("unreachable.retrying")
      : g.seen
        ? t("unreachable.lastOnline", { ago: agoText(g.seen, now) })
        : t("unreachable.reconnect");
  return (
    <div className="ov-warn" role="status">
      <Icon name="wifi" />
      <span>
        <Trans t={t} i18nKey={key} values={{ name: g.name, count: gone.length }} components={{ b: <b /> }} />
        {" · "}
        {more}
      </span>
    </div>
  );
}
