import { memo, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { GetUsageResponse } from "@hermesh/proto/local";
import { codingAgentShort, language, tokensText, usageSince, usageYear, type UsageCell } from "@hermesh/core";
import { AgentLogo } from "@hermesh/ui";
import { i18n } from "../../../shared/i18n";
import { local } from "../api";
import { useStore } from "../store";

/** A day as the overview's header and the usage tip show it: "Mon, Sep 29". */
export function dayText(d: Date): string {
  const date = new Intl.DateTimeFormat(language(), { month: "short", day: "numeric" }).format(d);
  const weekday = new Intl.DateTimeFormat(language(), { weekday: "short" }).format(d);
  return i18n.t("overview:day", { date, weekday });
}

/** A year of tokens by day (GetUsage), asked again as turns end (hermeshd keeps each machine's answer until then); null until the first answer. */
export function useUsage(now: Date): GetUsageResponse | null {
  const st = useStore();
  const [res, setRes] = useState<GetUsageResponse | null>(null);
  const since = usageSince(now);
  // Turns ended anywhere: the sum moves when one does.
  const turns = st.sessions.reduce((n, s) => n + (s.progress?.turnCount ?? 0), 0);
  useEffect(() => {
    let stop = false;
    const ask = () =>
      local
        .getUsage({ since, timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone })
        .then((r) => !stop && setRes(r))
        .catch(() => {}); // kept as it was; machines that can't answer are listed in the response
    ask();
    return () => {
      stop = true;
    };
  }, [since, turns]);
  return res;
}

/** "Usage": a year of tokens by day, as GitHub's contributions. */
export const Usage = memo(function Usage({ now, res }: { now: Date; res: GetUsageResponse | null }) {
  const { t } = useTranslation("overview");
  const y = useMemo(() => usageYear(res?.days ?? [], now), [res, now]);
  const card = useRef<HTMLDivElement>(null);
  const [tip, setTip] = useState<{ c: UsageCell; x: number; y: number } | null>(null);
  const hover = (c: UsageCell, el: HTMLElement) => {
    const r = el.getBoundingClientRect();
    const b = card.current?.getBoundingClientRect();
    if (!b) return;
    setTip({ c, x: Math.min(Math.max(r.left - b.left + r.width / 2, 70), b.width - 70), y: r.top - b.top });
  };
  const month = new Intl.DateTimeFormat(language(), { month: "short" });
  // A weekday's initial, 0 for Sunday (4 January 2026 was one).
  const weekday = (day: number) => new Intl.DateTimeFormat(language(), { weekday: "narrow" }).format(new Date(2026, 0, 4 + day));
  let lastMonth = -1;
  const months = Array.from({ length: y.weeks }, (_, w) => {
    const c = y.cells[w * 7];
    const m = c ? c.date.getMonth() : lastMonth;
    const label = c && m !== lastMonth ? month.format(c.date) : "";
    lastMonth = m;
    return label;
  });
  const unreachable = res?.unreachable ?? [];
  const todayCell = y.cells.findLastIndex((c) => c !== null);
  return (
    <section aria-label={t("usage.title")}>
      <div className="sec-h">{t("usage.title")}</div>
      <div className="card usage" ref={card}>
        <div className="u-top">
          <div className="u-big">
            <small>{t("usage.year")}</small>
            <b>{tokensText(y.total)}</b>
          </div>
          {y.delta !== null && <span className="u-delta">{y.delta >= 0 ? "↑" : "↓"} {Math.abs(y.delta)}%<span>{t("usage.last30")}</span></span>}
          <div className="u-kpis">
            <div className="u-kpi"><small>{t("usage.today")}</small><b>{tokensText(y.today)}</b></div>
            <div className="u-kpi"><small>{t("usage.activeDays")}</small><b>{y.active}</b></div>
            <div className="u-kpi"><small>{t("usage.streak")}</small><b>{t("usage.days", { count: y.streak })}</b></div>
          </div>
        </div>
        <div className="u-heat">
          <div className="u-in" style={{ "--weeks": y.weeks } as React.CSSProperties}>
            <div className="u-months" aria-hidden="true">
              {months.map((m, i) => <span key={i}>{m}</span>)}
            </div>
            <div className="u-wd" aria-hidden="true">
              <span /><span>{weekday(1)}</span><span /><span>{weekday(3)}</span><span /><span>{weekday(5)}</span><span />
            </div>
            <div className="u-cells" role="img" aria-label={t("usage.cells", { tokens: tokensText(y.total), active: y.active })} onMouseLeave={() => setTip(null)}>
              {y.cells.map((c, i) =>
                c ? (
                  <div key={i} className={`l${c.level} ${i === todayCell ? "today" : ""}`} onMouseEnter={(e) => hover(c, e.currentTarget)} />
                ) : (
                  <div key={i} className="none" />
                ),
              )}
            </div>
          </div>
        </div>
        <div className="u-foot">
          {y.total > 0 ? (
            <div className="u-mix">
              <span className="faint">{t("usage.byAgent")}</span>
              {y.byAgent.slice(0, 4).map((a) => (
                <span key={a.agent}>
                  <AgentLogo agent={a.agent} size={14} />
                  {codingAgentShort(a.agent)} {Math.round(a.share * 100)}%
                </span>
              ))}
            </div>
          ) : (
            <span className="u-empty">{res ? t("usage.empty") : ""}</span>
          )}
          {unreachable.length > 0 && <span className="u-empty">{t("usage.unreachable", { machines: unreachable.map((u) => u.machineName).join(t("listSeparator")) })}</span>}
          <div className="u-scale" aria-hidden="true">{t("usage.less")}{[0, 1, 2, 3, 4].map((l) => <div key={l} className={`l${l}`} />)}{t("usage.more")}</div>
        </div>
        {tip && (
          <div className="u-tip" style={{ left: tip.x, top: tip.y }}>
            <b>{tip.c.tokens ? `${tokensText(tip.c.tokens)} tokens` : t("usage.noUsage")}</b>
            {tip.c.sessions > 0 && t("usage.sessions", { count: tip.c.sessions })}
            <br />
            {dayText(tip.c.date)}
          </div>
        )}
      </div>
    </section>
  );
});
