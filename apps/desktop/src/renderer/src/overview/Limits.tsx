import { useMemo, type CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { agentLimits, agoText, codingAgentTitle, hourMinute, limitLow, sameDay, weekday, type AgentLimitsCard, type LimitShown } from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { i18n } from "../../../shared/i18n";
import { useCheckLimits } from "../agents";
import { machineName } from "../Sidebar";
import { useStore, type Store } from "../store";

// "Limits" (docs/overview-usage.md §7.4): what each coding agent last said of its account's rate limits, a row an
// agent, the one with the least left first. Each window says what is left twice, of its allowance and of its time
// until it resets, so that one is read against the other. Opening it checks this Mac's agents, which asks those that
// answer for their limits.

/** The agents that report their limits (the others never will). */
const reporting = new Set(["claude", "codex"]);

/** Google harnesses currently have no official compliant quota query API. */
const googleHarnesses = new Set(["antigravity", "gemini"]);

/** "5-Hour Window", "Weekly Window". */
function windowName(minutes: number): string {
  const k = "overview:limits.window";
  if (minutes === 300 || minutes === 10080) return i18n.t(`${k}.m${minutes}`);
  return minutes < 1440 ? i18n.t(`${k}.hours`, { count: Math.round(minutes / 60) }) : i18n.t(`${k}.days`, { count: Math.round(minutes / 1440) });
}

/**
 * "Resets at 16:40", or on another day "Resets Thu 09:00"; for a window of a day or less, with how long is left. One
 * not started: that it counts from its first use.
 */
function resetText(w: LimitShown, now: Date): string {
  if (!w.resetsAt) return i18n.t("overview:limits.notStarted");
  const time = hourMinute(w.resetsAt);
  const at = sameDay(w.resetsAt, now) ? i18n.t("overview:limits.resets", { time }) : i18n.t("overview:limits.resetsOn", { day: weekday(w.resetsAt), time });
  if (w.minutes > 1440) return at;
  const m = Math.max(1, Math.ceil((w.resetsAt.getTime() - now.getTime()) / 60_000));
  const left =
    m < 60 ? i18n.t("overview:limits.untilMinutes", { count: m })
    : m % 60 ? i18n.t("overview:limits.untilHours", { h: Math.floor(m / 60), m: m % 60 })
    : i18n.t("overview:limits.untilWholeHours", { count: m / 60 });
  return `${at} · ${left}`;
}

/** Who said it and when: "Reported by MacBook Pro · 2 min ago". */
function reportedText(st: Store, c: AgentLimitsCard, now: Date): string {
  const machine = st.machines.find((m) => m.machineId === c.machineId)?.local ? i18n.t("overview:thisMac") : machineName(st, c.machineId);
  return i18n.t("overview:limits.reported", { machine, ago: c.observedAt ? agoText(c.observedAt, now) : "" });
}

export function Limits({ now }: { now: Date }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const cards = useMemo(() => agentLimits(st.machines, now), [st.machines, now]);
  useCheckLimits(st.machines);
  return (
    <section aria-label={t("limits.title")}>
      <div className="us-stack">
        {cards.map((c) => <Card key={c.agent} c={c} now={now} />)}
      </div>
      <div className="us-foot">{t("limits.foot")}</div>
    </section>
  );
}

function Card({ c, now }: { c: AgentLimitsCard; now: Date }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const empty = googleHarnesses.has(c.agent) ? t("limits.noCompliantQuery") : c.stale ? t("limits.stale") : reporting.has(c.agent) ? t("limits.notYet") : t("limits.none");
  const showReported = c.observedAt && (!googleHarnesses.has(c.agent) || c.windows.length > 0);
  return (
    <div className="card tint lim" role="group" aria-label={codingAgentTitle(c.agent)}>
      <div className="lim-id">
        <span className="lim-logo"><AgentLogo agent={c.agent} size={40} /></span>
        <div className="lim-tx">
          <b>{codingAgentTitle(c.agent)}</b>
          {showReported && <small>{reportedText(st, c, now)}</small>}
          {/* What the account is, always: a window running low is said by its own row, as it may be one of several. */}
          {c.plan && <span className="lim-badge">{c.plan.charAt(0).toUpperCase() + c.plan.slice(1)}</span>}
        </div>
      </div>
      {c.windows.some((w) => w.group) ? (
        <div className="lim-groups">
          {[...new Set(c.windows.map((w) => w.group))].map((g) => (
            <div key={g} className="lim-grp" role="group" aria-label={g}>
              <div className="lim-gname">{g}</div>
              <div className="lim-ws">{c.windows.filter((w) => w.group === g).map((w) => <Window key={w.minutes} w={w} now={now} />)}</div>
            </div>
          ))}
        </div>
      ) : c.windows.length ? (
        <div className="lim-ws">{c.windows.map((w) => <Window key={w.minutes} w={w} now={now} />)}</div>
      ) : (
        <div className="lim-none">{empty}</div>
      )}
    </div>
  );
}

function Window({ w, now }: { w: LimitShown; now: Date }) {
  const { t } = useTranslation("overview");
  return (
    <div className={`lim-w ${w.left <= limitLow ? "warn" : ""}`} role="group" aria-label={windowName(w.minutes)}>
      <div className="lim-name">{windowName(w.minutes)}</div>
      <div className="lim-left"><b>{w.left}</b><i>%</i><small>{t("limits.left")}</small></div>
      <div className="lim-bars" aria-hidden="true">
        <small>{t("limits.allowance")}</small>
        <div className="lim-ticks" style={{ "--n": w.left } as CSSProperties}><u /></div>
        <small>{t("limits.time")}</small>
        <div className="lim-time"><u style={{ width: `${w.timeLeft}%` }} /></div>
      </div>
      <div className="lim-reset">{resetText(w, now)}</div>
    </div>
  );
}
