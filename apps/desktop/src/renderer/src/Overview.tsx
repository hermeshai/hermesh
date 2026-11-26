import { useEffect, useMemo, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { agentLimits, agoText, dateOf, hasUnseenLowLimits, markLowLimitsSeen, type SeenLowLimits } from "@hermesh/core";
import { Icon } from "./icons";
import { Attention } from "./overview/Attention";
import { Limits } from "./overview/Limits";
import { LinkMap } from "./overview/Map";
import { dayText, Usage, usageStart } from "./overview/Usage";
import { Working } from "./overview/Working";
import { layoutOf, setSidebar } from "./layout";
import { useNow, useStore } from "./store";
import { notNow } from "./start";
import { FinishedRow, useTeam } from "./team";
import "./overview/overview.css";

const views = ["live", "usage", "limits"] as const;
const seenLimitsKey = "hermesh.limits.seen";

function loadSeenLimits(): SeenLowLimits {
  try {
    const raw = JSON.parse(localStorage.getItem(seenLimitsKey) ?? "{}");
    if (!raw || typeof raw !== "object") return {};
    const out: SeenLowLimits = {};
    for (const [k, v] of Object.entries(raw)) if (typeof v === "number") out[k] = v;
    return out;
  } catch {
    return {};
  }
}

function saveSeenLimits(seen: SeenLowLimits) {
  try {
    if (Object.keys(seen).length) localStorage.setItem(seenLimitsKey, JSON.stringify(seen));
    else localStorage.removeItem(seenLimitsKey);
  } catch {
    // Storage may be unavailable in restricted environments.
  }
}

// The overview (docs/overview-usage.md): Live is what goes on now, which asks for no counts: what needs the owner
// beyond the session list, the link map of grants, machines and projects, what runs and what finished last. What
// was used and what is left are the page's other views, by the control in its head, so they take no place of the
// sidebar's.
// On a machine only, the same for this Mac: nothing waits on it, and the map is its own.
export function Overview() {
  const { t } = useTranslation("overview");
  const st = useStore();
  const now = useNow(30_000);
  const [view, setView] = useState<(typeof views)[number]>("live");
  const [usage, setUsage] = useState(usageStart);
  // A node picked on the map (a grant, machine or project) turns In Progress into the sessions through it.
  const [pick, setPick] = useState("");
  const cards = useMemo(() => agentLimits(st.machines, now), [st.machines, now]);
  const [seenLimits, setSeenLimits] = useState<SeenLowLimits>(loadSeenLimits);
  const hasUnseenLow = hasUnseenLowLimits(cards, seenLimits, now);
  const limitDot = view !== "limits" && hasUnseenLow;
  const markSeen = () => {
    setSeenLimits((prev) => {
      const next = markLowLimitsSeen(cards, prev, now);
      saveSeenLimits(next);
      return next;
    });
  };
  // Any low limits discovered while the owner is already on Limits are marked seen too.
  useEffect(() => {
    if (view === "limits" && hasUnseenLow) markSeen();
  }, [view, hasUnseenLow, cards, now]);
  const openView = (v: (typeof views)[number]) => {
    if (v === "limits" && hasUnseenLow) markSeen();
    setView(v);
  };
  return (
    <>
      <div className="ctool drag">
        <div className="ctitle" />
      </div>
      <div className="cscroll">
        <div className="ov">
          <div className="ov-h">
            <div className="ov-t">
              <h1>{t("title")}</h1>
              <span>{dayText(now)}</span>
            </div>
            <div className="seg" role="tablist" aria-label={t("views.label")}>
              {views.map((v) => (
                <button key={v} role="tab" aria-selected={view === v} className={view === v ? "on" : ""} onClick={() => openView(v)}>
                  {t(`views.${v}`)}
                  {v === "limits" && limitDot && <span className="dotc warn" data-testid="limits-low-dot" aria-hidden="true" />}
                </button>
              ))}
            </div>
          </div>
          {view === "live" ? (
            <>
              <HerNotOn />
              {st.sessionsUnreachable.length > 0 && <Unreachable now={now} />}
              <Attention now={now} />
              <section aria-label={t("map.title")}>
                <LinkMap pick={pick} onPick={setPick} now={now} />
              </section>
              <Working pick={pick} onClear={() => setPick("")} />
              {!pick && <RecentlyFinished />}
            </>
          ) : view === "usage" ? (
            <Usage now={now} state={usage} onChange={setUsage} />
          ) : (
            <Limits now={now} />
          )}
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

/**
 * The three sessions that finished last (docs/her-team.md §14.4). Every session is the sidebar's list: See All
 * shows it while the sidebar is hidden.
 */
function RecentlyFinished() {
  const { t } = useTranslation("common");
  const st = useStore();
  const team = useTeam();
  if (team.finished.length === 0) return null;
  return (
    <section aria-label={t("team.recentlyFinished")}>
      <div className="sec-h">
        {t("team.recentlyFinished")}
        {layoutOf(st).sidebar === "hidden" && <button className="link" onClick={() => setSidebar(st, true)}>{t("team.seeAll")}</button>}
      </div>
      <div className="card wk-list">
        {team.finished.slice(0, 3).map((s) => <FinishedRow key={s.sessionId} s={s} />)}
      </div>
    </section>
  );
}

/** After Not Now, while Her runs nowhere (docs/her-team.md §4.6): a line that brings her first run back. */
function HerNotOn() {
  const { t } = useTranslation("common");
  const st = useStore();
  if (st.status?.machineOnly || st.machines.length === 0 || st.machines.some((m) => m.her?.enabled) || !notNow()) return null;
  return (
    <div className="ov-warn her-off" role="status">
      <Icon name="her" />
      <span>{t("team.herOff")}</span>
      <button className="link" onClick={() => st.set({ selection: { kind: "her" } })}>{t("team.turnHerOn")}</button>
    </div>
  );
}
