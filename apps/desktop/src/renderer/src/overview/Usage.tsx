import { useEffect, useMemo, useRef, useState, type CSSProperties, type MouseEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { QueryWorkRequestSchema, QueryWorkResponse } from "@hermesh/proto/local";
import {
  codingAgentIds, codingAgentTitle, dayKey, language, noUsageFilter, tokensText, usageChoices, usageGroups, usageMenusRequest, usagePage, usagePageRequest, usageParts, usageRanges,
  usageWeeksRequest, usageYear, type UsageChoice, type UsageFilter, type UsageGroup, type UsagePart, type UsageRange,
} from "@hermesh/core";
import type { MenuItem } from "../../../shared/bridge";
import { i18n } from "../../../shared/i18n";
import { local } from "../api";
import { Icon } from "../icons";
import { machineName, senderName } from "../Sidebar";
import { useStore, type Store } from "../store";

/** A day as the overview's header and the usage tip show it: "Mon, Sep 29". */
export function dayText(d: Date): string {
  const date = new Intl.DateTimeFormat(language(), { month: "short", day: "numeric" }).format(d);
  const weekday = new Intl.DateTimeFormat(language(), { weekday: "short" }).format(d);
  return i18n.t("overview:day", { date, weekday });
}

/** What the Usage view shows: kept by the overview, so it is there again when the view is. By agent to start with. */
export type UsageState = { range: UsageRange; filter: UsageFilter; group: UsageGroup };
export const usageStart: UsageState = { range: "week", filter: noUsageFilter, group: "agent" };

type WorkRequest = MessageInitShape<typeof QueryWorkRequestSchema>;

/**
 * QueryWork's answer to a question (from what hermeshd holds), asked again when the question changes and as the
 * counts move; the last answer stays until the next replaces it, and null until the first.
 */
function useWork(req: WorkRequest, version: number | bigint | undefined): QueryWorkResponse | null {
  const [res, setRes] = useState<QueryWorkResponse | null>(null);
  const key = JSON.stringify(req);
  useEffect(() => {
    let stop = false;
    local
      .queryWork(req)
      .then((r) => !stop && setRes(r))
      .catch(() => {}); // kept as it was
    return () => {
      stop = true;
    };
  }, [key, version]);
  return res;
}

/** "20 hr 8 min" of work. */
function durationText(seconds: number): string {
  const m = Math.round(seconds / 60);
  if (m < 1) return i18n.t("overview:usage.duration.under");
  if (m < 60) return i18n.t("overview:usage.duration.minutes", { m });
  return i18n.t(m % 60 ? "overview:usage.duration.hoursMinutes" : "overview:usage.duration.hours", { h: Math.floor(m / 60), m: m % 60 });
}

const placeName = (st: Store, id: string) => (st.machines.find((m) => m.machineId === id)?.local ? i18n.t("overview:thisMac") : machineName(st, id));

/**
 * "Usage" (docs/overview-usage.md §6): what was used over a range, under the filters of the row at its top: a
 * measure a row, its number beside its chart, and a year of tokens by day. Asked only while it shows.
 */
export function Usage({ now, state, onChange }: { now: Date; state: UsageState; onChange: (s: UsageState) => void }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const { range, filter, group } = state;
  const today = dayKey(now);
  // Asked again each day, and as the counts move.
  const res = useWork(useMemo(() => usagePageRequest(range, filter, group, now), [range, filter, group, today]), st.workVersion);
  const menus = useWork(useMemo(() => usageMenusRequest(range, now), [range, today]), st.workVersion);
  const weeks = useWork(useMemo(() => usageWeeksRequest(filter, now), [filter, today]), st.workVersion);
  const page = useMemo(() => usagePage(res, range, now), [res, range, today]);
  const choices = useMemo(() => usageChoices(menus, filter), [menus, filter]);
  // The bars in parts (docs/overview-usage.md §6.9): this Mac first, then the machines as the app lists them.
  const machineOrder = useMemo(() => [...st.machines].sort((a, b) => Number(!!b.local) - Number(!!a.local)).map((m) => m.machineId), [st.machines]);
  const split = useMemo(() => usageParts(res, range, group, filter, { agents: codingAgentIds, machines: machineOrder }), [res, range, group, filter, machineOrder]);
  // A part named in a legend: pointed at, it stands out in both charts; clicked, it is seen alone in them (the page's filters stay).
  const [lit, setLit] = useState<string | null>(null);
  const [solo, setSolo] = useState<string | null>(null);
  const parts = split?.parts ?? [];
  const alone = parts.find((p) => p.id === solo) ?? null;
  const partName = (p: UsagePart) =>
    p.color < 0 ? t("usage.group.other") : split!.by === "agent" ? codingAgentTitle(p.id) : split!.by === "machine" ? placeName(st, p.id) : p.id;

  const said = (c: UsageChoice) => (c.sessions || c.tokens ? t("usage.filter.counts", { count: c.sessions, tokens: tokensText(c.tokens) }) : t("usage.filter.none"));
  // A filter's menu is the system's, under its button; a choice's second line is its work in the range.
  const pop = async (e: MouseEvent<HTMLButtonElement>, items: MenuItem[], choose: (id: string) => UsageState) => {
    const r = e.currentTarget.getBoundingClientRect();
    const id = await window.hermesh?.popupMenu(items, { x: r.left, y: r.bottom + 4 });
    if (id != null) onChange(choose(id));
  };
  const set = (f: Partial<UsageFilter>): UsageState => ({ ...state, filter: { ...filter, ...f } });
  const all = (chosen: boolean): MenuItem[] => [{ id: "", label: t("usage.filter.all"), checked: !chosen }, { separator: true }];
  const rangeMenu = (e: MouseEvent<HTMLButtonElement>) =>
    pop(e, usageRanges.map((r) => ({ id: r, label: t(`usage.range.${r}`), checked: r === range })), (id) => ({ ...state, range: id as UsageRange }));
  const agentMenu = (e: MouseEvent<HTMLButtonElement>) =>
    pop(e, [...all(!!filter.agent), ...choices.agents.map((c) => ({ id: c.id, label: codingAgentTitle(c.id), detail: said(c), checked: c.id === filter.agent }))], (agent) => set({ agent }));
  const clientMenu = (e: MouseEvent<HTMLButtonElement>) =>
    pop(e, [...all(!!filter.client), ...choices.clients.map((c) => ({ id: c.id, label: senderName(st, c.id), detail: said(c), checked: c.id === filter.client }))], (client) => set({ client }));
  // Where: an environment opens its projects; its whole is the first line inside (a line that opens a menu is not a choice).
  const placeMenu = (e: MouseEvent<HTMLButtonElement>) =>
    pop(
      e,
      [
        ...all(!!filter.machineId),
        ...choices.machines.map((m) => ({
          id: m.id,
          label: placeName(st, m.id),
          detail: said(m),
          submenu: [
            { id: JSON.stringify([m.id, ""]), label: t("usage.filter.allProjects"), detail: said(m), checked: m.id === filter.machineId && !filter.projectId },
            { separator: true as const },
            ...m.projects.map((p) => ({ id: JSON.stringify([m.id, p.id]), label: p.id, detail: said(p), checked: m.id === filter.machineId && p.id === filter.projectId })),
          ],
        })),
      ],
      (id) => {
        const [machineId, projectId] = id ? (JSON.parse(id) as [string, string]) : ["", ""];
        return set({ machineId, projectId });
      },
    );

  const delta = (d: number | null) => (d === null ? "" : `${t(`usage.delta.${range}`)} ${d >= 0 ? "↑" : "↓"} ${Math.abs(d)}%`);
  const day = (i: number) => new Date(now.getFullYear(), now.getMonth(), now.getDate() - (page.buckets.length - 1 - i));
  const weekday = new Intl.DateTimeFormat(language(), { weekday: "short" });
  // Under the chart: the hours by six and now; a week's days; a month's by week back from today.
  const label = (i: number) =>
    range === "today"
      ? i === page.now ? t("usage.now") : i % 6 === 0 && Math.abs(i - page.now) > 1 ? t("usage.hour", { h: i }) : ""
      : i === page.now ? t("usage.today") : range === "week" ? weekday.format(day(i)) : (page.now - i) % 7 === 0 ? `${day(i).getMonth() + 1}/${day(i).getDate()}` : "";
  const when = (i: number) => (range === "today" ? t("usage.hour", { h: i }) : dayText(day(i)));

  // Machines counted only up to their last sync, or still taking their history.
  const syncing = (res?.sources ?? []).filter((s) => s.syncing).map((s) => placeName(st, s.machineId));
  const behind = (res?.sources ?? []).filter((s) => !s.live && !s.syncing && s.syncedAt);
  const oldest = behind.length ? new Date(Math.min(...behind.map((s) => Number(s.syncedAt!.seconds) * 1000))) : null;
  return (
    <section aria-label={t("usage.title")} className="us">
      <div className="us-filters">
        <Pop label={t("usage.filter.range")} onClick={rangeMenu}>{t(`usage.range.${range}`)}</Pop>
        <i aria-hidden="true" />
        <Pop label={t("usage.filter.agent")} on={!!filter.agent} onClick={agentMenu}>{filter.agent ? codingAgentTitle(filter.agent) : t("usage.filter.agent")}</Pop>
        <Pop label={t("usage.filter.place")} on={!!filter.machineId} onClick={placeMenu}>
          {filter.machineId ? placeName(st, filter.machineId) + (filter.projectId ? `/${filter.projectId}` : "") : t("usage.filter.place")}
        </Pop>
        <Pop label={t("usage.filter.client")} on={!!filter.client} onClick={clientMenu}>{filter.client ? senderName(st, filter.client) : t("usage.filter.client")}</Pop>
        {/* What the bars are in parts of: not a filter, so not a pop-up beside them; its three ways shown at once. */}
        <span className="us-group">
          <span aria-hidden="true">{t("usage.group.label")}</span>
          <span className="seg" role="radiogroup" aria-label={t("usage.group.label")}>
            {usageGroups.map((g) => (
              <button key={g} role="radio" aria-checked={g === group} className={g === group ? "on" : ""} onClick={() => {
                  setSolo(null);
                  onChange({ ...state, group: g });
                }}>
                {t(`usage.group.${g}`)}
              </button>
            ))}
          </span>
        </span>
      </div>
      <div className="us-stack">
        <Measure
          title={t("usage.sessions.title")}
          big={String(page.sessions)}
          unit={t("usage.sessions.unit", { count: page.sessions })}
          delta={delta(page.sessionsDelta)}
          facts={[[t("usage.sessions.done"), String(page.done)], [t("usage.sessions.failed"), t("usage.sessions.failedTurns", { count: page.failedTurns })], [t("usage.sessions.busy"), durationText(page.busySeconds)]]}
          legend={split && <Legend parts={parts} name={partName} value={(p) => String(p.sessions)} whole={page.sessions} lit={setLit} solo={solo} onSolo={setSolo} />}
        >
          <Chart
            values={page.buckets.map((b) => b.sessions)}
            now={page.now}
            whole
            label={label}
            scale={String}
            tip={(i) => [when(i), t("usage.tip.sessions", { count: (alone ? alone.buckets[i] : page.buckets[i]).sessions })]}
            parts={split && { parts, value: (b) => b.sessions, name: partName, text: String, lit, alone }}
          />
        </Measure>
        <Measure
          title={t("usage.tokens.title")}
          big={tokensText(page.inputTokens + page.outputTokens)}
          unit={t("usage.tokens.unit")}
          delta={delta(page.tokensDelta)}
          facts={[[t("usage.tokens.input"), tokensText(page.inputTokens)], [t("usage.tokens.output"), tokensText(page.outputTokens)]]}
          legend={split && <Legend parts={parts} name={partName} value={(p) => tokensText(p.tokens)} whole={page.inputTokens + page.outputTokens} share={(p) => p.tokens} lit={setLit} solo={solo} onSolo={setSolo} />}
        >
          <Chart
            values={page.buckets.map((b) => b.tokens)}
            now={page.now}
            label={label}
            scale={(v) => tokensText(v).replace(".0", "")}
            tip={(i) => [when(i), t("usage.tip.tokens", { tokens: tokensText((alone ? alone.buckets[i] : page.buckets[i]).tokens) })]}
            parts={split && { parts, value: (b) => b.tokens, name: partName, text: tokensText, lit, alone }}
          />
        </Measure>
        <Year res={weeks} now={now} />
      </div>
      <div className="us-foot">
        {t("usage.cache")}
        {syncing.length > 0
          ? ` ${t("usage.syncing", { machines: syncing.join(t("listSeparator")) })}`
          : oldest && ` ${t("usage.behind", { machines: behind.map((s) => placeName(st, s.machineId)).join(t("listSeparator")), time: new Intl.DateTimeFormat(language(), { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }).format(oldest) })}`}
      </div>
    </section>
  );
}

/** A filter's pop-up button: what is chosen, tinted when it narrows the page. */
function Pop({ label, on, onClick, children }: { label: string; on?: boolean; onClick: (e: MouseEvent<HTMLButtonElement>) => void; children: ReactNode }) {
  return (
    <button className={`us-pop ${on ? "on" : ""}`} aria-haspopup="menu" aria-label={label} onClick={onClick}>
      <span>{children}</span>
      <Icon name="chevUD" />
    </button>
  );
}

/** A measure's row: its number, how it stands against the span before and its other figures; beside them its chart, under the parts' legend when it is in parts. */
function Measure({ title, big, unit, delta, facts, legend, children }: { title: string; big: string; unit: string; delta: string; facts: [string, string][]; legend?: ReactNode; children: ReactNode }) {
  return (
    <div className="card tint us-m" role="group" aria-label={title}>
      <div className="us-lead">
        <div className="us-ttl">{title}</div>
        <div className="us-big"><b>{big}</b><span>{unit}</span></div>
        <div className="us-delta">{delta || " "}</div>
        <dl className="us-facts">
          {facts.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v}</dd></div>)}
        </dl>
      </div>
      <div className="us-plot">
        {legend}
        {children}
      </div>
    </div>
  );
}

/** The colour of a part: its own of six, or Other's grey. */
const partColor = (p: UsagePart) => (p.color < 0 ? "var(--us-other)" : `var(--us-s${p.color + 1})`);

/**
 * The parts of a measure in a line over its chart, the busiest first: each its colour, its number and its share of
 * the whole. Pointed at, a part stands out; clicked, it is seen alone in the charts, and again, they are whole.
 */
function Legend({ parts, name, value, whole, share = (p) => p.sessions, lit, solo, onSolo }: {
  parts: UsagePart[];
  name: (p: UsagePart) => string;
  value: (p: UsagePart) => string;
  whole: number;
  share?: (p: UsagePart) => number;
  lit: (id: string | null) => void;
  solo: string | null;
  onSolo: (id: string | null) => void;
}) {
  const alone = parts.some((p) => p.id === solo);
  return (
    <div className={`us-legend ${alone ? "alone" : ""}`}>
      {parts.map((p) => (
        <button key={p.id} aria-pressed={p.id === solo} onMouseEnter={() => lit(p.id)} onMouseLeave={() => lit(null)} onClick={() => onSolo(p.id === solo ? null : p.id)}>
          <i style={{ background: partColor(p) }} />
          {name(p)}
          <em>{value(p)}</em>
          <small>{whole ? `${Math.round((share(p) / whole) * 100)}%` : ""}</small>
        </button>
      ))}
    </div>
  );
}

/** A chart's bars in parts: each part's share of a bucket, by its value; one part seen alone, or one pointed at. */
type ChartParts = { parts: UsagePart[]; value: (b: { sessions: number; tokens: number }) => number; name: (p: UsagePart) => string; text: (v: number) => string; lit: string | null; alone: UsagePart | null };

/**
 * A measure over time: a thin bar a bucket in the map's own ink, the one now is in in colour, with a ruled scale
 * at the trailing edge and a name under the buckets there is room for. whole: the measure is counted in ones, so
 * the scale's middle is one too. In parts, a bar is its parts one on another, the busiest at its foot, each in its
 * colour; one part seen alone is its bars alone on the same scale.
 */
function Chart({ values, now, whole, label, scale, tip, parts }: {
  values: number[];
  now: number;
  whole?: boolean;
  label: (i: number) => string;
  scale: (v: number) => string;
  tip: (i: number) => [string, string];
  parts?: ChartParts | null;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [at, setAt] = useState<{ i: number; x: number } | null>(null);
  const raw = Math.max(1, ...values);
  const p = 10 ** Math.floor(Math.log10(raw));
  const top = whole && raw < 10 ? [2, 4, 6, 8, 10].find((v) => v >= raw)! : [1, 2, 5, 10].find((m) => m * p >= raw)! * p;
  const hover = (i: number, el: HTMLElement) => {
    const b = box.current?.getBoundingClientRect();
    if (b) setAt({ i, x: el.getBoundingClientRect().left - b.left + el.offsetWidth / 2 });
  };
  return (
    <div className="us-chart" ref={box} onMouseLeave={() => setAt(null)}>
      <div className="us-plot-in">
        {[1, 0.5, 0].map((f) => <div key={f} className="us-rule" style={{ bottom: `${f * 100}%` }}><span>{scale(top * f)}</span></div>)}
        <div className="us-slots" role="img" aria-label={values.slice(0, now + 1).map((_, i) => tip(i).join(" ")).join("; ")}>
          {values.map((v, i) =>
            i > now ? <i key={i} /> : !parts ? (
              <i key={i} className={`in ${i === now ? "now" : ""}`} onMouseEnter={(e) => hover(i, e.currentTarget)}>
                {v > 0 && <u style={{ height: `${(v / top) * 100}%` }} />}
              </i>
            ) : (
              <i key={i} className="in" onMouseEnter={(e) => hover(i, e.currentTarget)}>
                {(parts.alone ? parts.value(parts.alone.buckets[i]) : v) > 0 && (
                  <span className="us-bar" style={{ height: `${((parts.alone ? parts.value(parts.alone.buckets[i]) : v) / top) * 100}%` }}>
                    {(parts.alone ? [parts.alone] : parts.parts).map((p) => {
                      const n = parts.value(p.buckets[i]);
                      return n > 0 && <u key={p.id} className={parts.lit !== null && parts.lit !== p.id ? "dim" : ""} style={{ flexGrow: n, background: partColor(p) }} />;
                    })}
                  </span>
                )}
              </i>
            ),
          )}
        </div>
      </div>
      <div className="us-x" aria-hidden="true">{values.map((_, i) => <span key={i} className={i === now ? "now" : ""}>{label(i)}</span>)}</div>
      {at && (
        <div className="u-tip" style={{ left: at.x, top: 0 }}>
          <b>{tip(at.i)[1]}</b>
          <br />
          {parts?.alone ? `${parts.name(parts.alone)} · ${tip(at.i)[0]}` : tip(at.i)[0]}
          {parts && !parts.alone && (
            <span className="u-parts">
              {parts.parts.filter((p) => parts.value(p.buckets[at.i]) > 0).map((p) => (
                <span key={p.id}>
                  <span><i style={{ background: partColor(p) }} />{parts.name(p)}</span>
                  <em>{parts.text(parts.value(p.buckets[at.i]))}</em>
                </span>
              ))}
            </span>
          )}
        </div>
      )}
    </div>
  );
}

/** A year of tokens by day, as GitHub's contributions: under the filters, not the range. */
function Year({ res, now }: { res: QueryWorkResponse | null; now: Date }) {
  const { t } = useTranslation("overview");
  const y = useMemo(() => usageYear(res, now), [res, dayKey(now)]);
  const card = useRef<HTMLDivElement>(null);
  const [tip, setTip] = useState<{ i: number; x: number; y: number } | null>(null);
  const hover = (i: number, el: HTMLElement) => {
    const r = el.getBoundingClientRect();
    const b = card.current?.getBoundingClientRect();
    if (b) setTip({ i, x: Math.min(Math.max(r.left - b.left + r.width / 2, 70), b.width - 70), y: r.top - b.top });
  };
  const month = new Intl.DateTimeFormat(language(), { month: "short" });
  let lastMonth = -1;
  const months = Array.from({ length: y.weeks }, (_, w) => {
    const c = y.cells[w * 7];
    const m = c ? c.date.getMonth() : lastMonth;
    const label = c && m !== lastMonth ? month.format(c.date) : "";
    lastMonth = m;
    return label;
  });
  const at = tip && y.cells[tip.i];
  return (
    <div className="card tint us-year" ref={card} role="group" aria-label={t("usage.year.title")}>
      <div className="us-year-h">
        <b>{t("usage.year.title")}</b>
        <span>{t("usage.year.summary", { active: y.active, streak: y.streak })}</span>
      </div>
      <div className="us-heat" style={{ "--weeks": y.weeks } as CSSProperties}>
        <div className="us-cells" role="img" aria-label={t("usage.cells", { tokens: tokensText(y.total), active: y.active })} onMouseLeave={() => setTip(null)}>
          {y.cells.map((c, i) => (c ? <div key={i} className={`l${c.level}`} onMouseEnter={(e) => hover(i, e.currentTarget)} /> : <div key={i} className="none" />))}
        </div>
        <div className="us-months" aria-hidden="true">{months.map((m, i) => <span key={i}>{m}</span>)}</div>
      </div>
      <div className="us-scale" aria-hidden="true">{t("usage.less")}{[0, 1, 2, 3, 4].map((l) => <div key={l} className={`l${l}`} />)}{t("usage.more")}</div>
      {tip && at && (
        <div className="u-tip" style={{ left: tip.x, top: tip.y }}>
          <b>{at.tokens ? t("usage.tip.tokens", { tokens: tokensText(at.tokens) }) : t("usage.noUsage")}</b>
          <br />
          {dayText(at.date)}
        </div>
      )}
    </div>
  );
}
