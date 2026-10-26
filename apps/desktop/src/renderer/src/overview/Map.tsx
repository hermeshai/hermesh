import { useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode, type Ref } from "react";
import { Trans, useTranslation } from "react-i18next";
import type { Client, GetUsageResponse, Machine } from "@hermesh/proto/local";
import { SessionStatus, type Session } from "@hermesh/proto/session";
import { agoText, codingAgentTitle, createdDate, dateOf, isActive, isFailed, monthDay, sameDay, todayStats, tokensText, usageYear } from "@hermesh/core";
import { AgentLogo, AgentTile, grantColor, kindNamed, Mark } from "@hermesh/ui";
import { i18n } from "../../../shared/i18n";
import { Icon } from "../icons";
import { hostOf } from "../sheets/Pairing";
import { useStore, type Store } from "../store";

// "Connections": who may use this computer (grants: a personal agent connects only
// while it runs a command, so they are never drawn offline), Hermesh in the middle, the
// machines, and their projects; each session that is live runs along its
// grant → machine → project path. On a machine only it is this Mac's: the
// grants that cover it, this Mac in the middle, its projects; one that never
// came is drawn faint, and a grant clicked opens a card of what it may do
// here. Drawn on a fixed stage that is scaled to the width, so the lines
// (SVG) and the nodes (HTML) stay together.

const W = 820;
const GAP = 64;
const TOP = 13;
const MAX = 6;
const COL = { g: 24, m: 448, p: 670 };
const WIDTH = { g: 164, m: 168, p: 134 };
const HEIGHT = { g: 42, m: 46, p: 34 };
const HUB = { x: 318, s: 36 };
// A machine only's map: grants, this Mac (the hub), projects.
const ONE = { g: { x: 24, w: 196 }, p: { x: 640, w: 150 }, hub: 410 };

type Kind = "g" | "m" | "p";
type Spot = { key: string; kind: Kind; x: number; y: number; w: number; h: number };
type GrantNode = Spot & { kind: "g"; client: Client };
type MachineNode = Spot & { kind: "m"; machine: Machine };
type ProjectNode = Spot & { kind: "p"; machineId: string; projectId: string; online: boolean };
type Extra = Spot & ExtraItem;
type ExtraItem = { key: string; label: string; action?: () => void };
type Path = { s: Session; keys: string[]; d: string };

/** A session the map shows: a turn in progress, a question open, or a failure. */
export function liveOnMap(s: Session): boolean {
  return s.status !== SessionStatus.ARCHIVED && (isActive(s) || s.status === SessionStatus.WAITING_INPUT || isFailed(s));
}

const grantKey = (id: string) => `g:${id}`;
const machineKey = (id: string) => `m:${id}`;
const projectKey = (m: string, p: string) => `p:${m}/${p}`;
const curve = (x1: number, y1: number, x2: number, y2: number) => {
  const k = (x2 - x1) * 0.55;
  return `C${x1 + k} ${y1} ${x2 - k} ${y2} ${x2} ${y2}`;
};

/** Lays out the columns around the hub's height, the busiest first, at most MAX a column. */
function layout(st: Store, live: Session[]) {
  const only = st.status?.machineOnly === true;
  const count = (k: (s: Session) => string) => {
    const n = new Map<string, number>();
    for (const s of live) n.set(k(s), (n.get(k(s)) ?? 0) + 1);
    return n;
  };
  const byGrant = count((s) => s.createdBy);
  const byMachine = count((s) => s.machineId);
  const byProject = count((s) => projectKey(s.machineId, s.projectId));

  const clients = st.clients
    .filter((c) => !c.revoked)
    .sort((a, b) => (byGrant.get(b.clientId) ?? 0) - (byGrant.get(a.clientId) ?? 0));
  const machines = only ? [] : [...st.machines].sort((a, b) => Number(b.local) - Number(a.local) || Number(b.online) - Number(a.online));
  const projects = new Map<string, { machineId: string; projectId: string }>();
  for (const s of live) projects.set(projectKey(s.machineId, s.projectId), { machineId: s.machineId, projectId: s.projectId });
  for (const p of st.projects) projects.set(projectKey(p.machineId, p.projectId), { machineId: p.machineId, projectId: p.projectId });
  const machineRank = new Map(machines.map((m, i) => [m.machineId, i]));
  const projectList = [...projects.entries()].sort(
    ([ka, a], [kb, b]) =>
      Number((byProject.get(kb) ?? 0) > 0) - Number((byProject.get(ka) ?? 0) > 0) ||
      (machineRank.get(a.machineId) ?? 99) - (machineRank.get(b.machineId) ?? 99),
  );

  const extras: Extra[] = [];
  const column = <T,>(items: T[], kind: Kind, ghost: ExtraItem | null, more: "moreGrants" | "moreMachines" | "moreProjects") => {
    const shown = items.length > MAX ? items.slice(0, MAX - 1) : items;
    const rest = items.length - shown.length;
    const tail: ExtraItem[] = [];
    if (rest > 0) tail.push({ key: `more:${kind}`, label: i18n.t(`overview:map.${more}`, { count: rest }) });
    else if (ghost) tail.push(ghost);
    return { shown, tail };
  };
  const g = column(clients, "g", !only && clients.length < 3 ? { key: "ghost:g", label: i18n.t("overview:map.connectMore"), action: () => st.set({ sheet: { kind: "newGrant" } }) } : null, "moreGrants");
  const m = column(machines, "m", !only && machines.length < 3 ? { key: "ghost:m", label: i18n.t("overview:map.addMachine"), action: () => st.set({ sheet: { kind: "addMachine" } }) } : null, "moreMachines");
  const p = column(projectList, "p", null, "moreProjects");

  const rows = Math.max(g.shown.length + g.tail.length, m.shown.length + m.tail.length, p.shown.length + p.tail.length, 1);
  const H = Math.max(190, rows * GAP + TOP + 44);
  // The hub with its label (about 127 tall, the icon on top) is what sits in the middle.
  const cy = TOP + (H - TOP) / 2 - 19;
  const y = (i: number, n: number) => cy + (i - (n - 1) / 2) * GAP;
  const col = (kind: Kind) => (only && kind !== "m" ? ONE[kind] : { x: COL[kind], w: WIDTH[kind] });
  const spot = (kind: Kind, i: number, n: number) => ({ kind, x: col(kind).x, y: y(i, n), w: col(kind).w, h: HEIGHT[kind] });

  const gn = g.shown.length + g.tail.length;
  const grants: GrantNode[] = g.shown.map((c, i) => ({ ...spot("g", i, gn), kind: "g", key: grantKey(c.clientId), client: c }));
  g.tail.forEach((t, i) => extras.push({ ...spot("g", g.shown.length + i, gn), ...t }));
  const mn = m.shown.length + m.tail.length;
  const machineNodes: MachineNode[] = m.shown.map((x, i) => ({ ...spot("m", i, mn), kind: "m", key: machineKey(x.machineId), machine: x }));
  m.tail.forEach((t, i) => extras.push({ ...spot("m", m.shown.length + i, mn), ...t }));
  const pn = p.shown.length + p.tail.length;
  const online = new Map(st.machines.map((x) => [x.machineId, x.online]));
  const projectNodes: ProjectNode[] = p.shown.map(([key, x], i) => ({ ...spot("p", i, pn), kind: "p", key, ...x, online: online.get(x.machineId) ?? false }));
  p.tail.forEach((t, i) => extras.push({ ...spot("p", p.shown.length + i, pn), ...t }));

  const hx = only ? ONE.hub : HUB.x;
  const hub = { x: hx, y: cy, l: hx - HUB.s - 4, r: hx + HUB.s + 4 };
  const at = new Map<string, Spot>([...grants, ...machineNodes, ...projectNodes].map((n) => [n.key, n]));
  const edges: { keys: string[]; d: string; off: boolean }[] = [];
  for (const n of grants) edges.push({ keys: [n.key], off: only && !n.client.lastSeen, d: `M${n.x + n.w} ${n.y} ${curve(n.x + n.w, n.y, hub.l, hub.y)}` });
  for (const n of machineNodes) edges.push({ keys: [n.key], off: !n.machine.online, d: `M${hub.r} ${hub.y} ${curve(hub.r, hub.y, n.x, n.y)}` });
  for (const n of projectNodes) {
    if (only) {
      edges.push({ keys: [n.key], off: !n.online, d: `M${hub.r} ${hub.y} ${curve(hub.r, hub.y, n.x, n.y)}` });
      continue;
    }
    const mm = at.get(machineKey(n.machineId));
    if (mm) edges.push({ keys: [mm.key, n.key], off: !n.online, d: `M${mm.x + mm.w} ${mm.y} ${curve(mm.x + mm.w, mm.y, n.x, n.y)}` });
  }
  const paths: Path[] = [];
  for (const s of live) {
    const a = at.get(grantKey(s.createdBy));
    const b = at.get(machineKey(s.machineId));
    const c = at.get(projectKey(s.machineId, s.projectId));
    if (only && a && c) {
      paths.push({ s, keys: [a.key, c.key], d: `M${a.x + a.w} ${a.y} ${curve(a.x + a.w, a.y, hub.l, hub.y)} L${hub.r} ${hub.y} ${curve(hub.r, hub.y, c.x, c.y)}` });
      continue;
    }
    if (!a || !b || !c) continue;
    const d =
      `M${a.x + a.w} ${a.y} ${curve(a.x + a.w, a.y, hub.l, hub.y)} L${hub.r} ${hub.y} ` +
      `${curve(hub.r, hub.y, b.x, b.y)} L${b.x + b.w} ${b.y} ${curve(b.x + b.w, b.y, c.x, c.y)}`;
    paths.push({ s, keys: [a.key, b.key, c.key], d });
  }
  return { H, hub, grants, machines: machineNodes, projects: projectNodes, extras, edges, paths, byGrant, byMachine, byProject, at };
}

/** The keys of the nodes a session runs through: its grant, machine and project. */
export function sessionKeys(s: Session): string[] {
  return [grantKey(s.createdBy), machineKey(s.machineId), projectKey(s.machineId, s.projectId)];
}

/**
 * The map; pick is the node the owner clicked (its links stay lit and
 * Working Now narrows to the sessions through it), onPick toggles it ("" for
 * none).
 */
export function LinkMap({ pick, onPick, now, usage }: { pick: string; onPick: (key: string) => void; now: Date; usage: GetUsageResponse | null }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const live = useMemo(() => st.sessions.filter(liveOnMap), [st.sessions]);
  const L = useMemo(() => layout(st, live), [st, live]);
  const running = live.filter((s) => s.status === SessionStatus.RUNNING || s.status === SessionStatus.PREPARING || s.status === SessionStatus.QUEUED);
  const waiting = live.filter((s) => s.status === SessionStatus.WAITING_INPUT);
  const failed = live.filter(isFailed);
  const offline = st.machines.some((m) => !m.online);
  const only = st.status?.machineOnly === true;
  // A machine only: grants that never came here, and the card of the one picked.
  const unseen = only && L.grants.some((n) => !n.client.lastSeen);
  const card = only ? L.grants.find((n) => n.key === pick) : undefined;
  const cardRef = useRef<HTMLDivElement>(null);
  const [cardH, setCardH] = useState(0);
  useLayoutEffect(() => setCardH(card ? (cardRef.current?.offsetHeight ?? 0) : 0), [card, st.sessions, st.clients]);
  const cardTop = card ? card.y + card.h / 2 + 8 : 0;
  const H = card ? Math.max(L.H, cardTop + cardH + 13) : L.H;
  useEffect(() => {
    if (!card) return;
    const esc = (e: KeyboardEvent) => e.key === "Escape" && onPick("");
    document.addEventListener("keydown", esc);
    return () => document.removeEventListener("keydown", esc);
  }, [card, onPick]);

  // Scale the stage to the card's width.
  const box = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(1);
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setScale(Math.min(1.15, el.clientWidth / W)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Hovering a node lights what it is linked to; without a hover, the picked node does.
  const [hover, setHover] = useState<string | null>(null);
  const focus = hover ?? (pick || null);
  const toggle = (key: string) => onPick(pick === key ? "" : key);
  // A grant in focus also shows where it may work: the machines and projects its grant allows.
  const reach = useMemo(() => {
    const c = focus?.startsWith("g:") ? st.clients.find((x) => grantKey(x.clientId) === focus) : undefined;
    if (!c) return null;
    const allows = (scope: string[] | undefined, v: string) => !!scope && (scope.includes("*") || scope.includes(v));
    const keys = new Set<string>();
    for (const n of L.machines) if (allows(c.spec?.machineIds, n.machine.machineId)) keys.add(n.key);
    for (const n of L.projects) if (allows(c.spec?.machineIds, n.machineId) && allows(c.spec?.projectIds, n.projectId)) keys.add(n.key);
    return keys;
  }, [focus, L, st.clients]);
  const lit = useMemo(() => {
    if (!focus) return null;
    const keys = new Set([focus]);
    for (const p of L.paths) if (p.keys.includes(focus)) p.keys.forEach((k) => keys.add(k));
    for (const e of L.edges) if (e.keys.includes(focus)) e.keys.forEach((k) => keys.add(k));
    reach?.forEach((k) => keys.add(k));
    return keys;
  }, [focus, L, reach]);
  const on = (keys: string[]) => (lit && (keys.includes(focus ?? "") || (lit.size > 1 && keys.every((k) => lit.has(k)))) ? "on" : "");
  const inScope = (keys: string[]) => (reach && keys.every((k) => reach.has(k)) ? "scope" : "");
  const lightNode = (key: string) => (lit?.has(key) ? "on" : "");
  const onPath = (keys: string[]) => (lit && keys.includes(focus ?? "") ? "on" : "");

  // Comets: a dot and its fading tail along each running session's path. At
  // u (0–1, from the document's time) a comet's dots sit at u, u − 0.011, …
  // along it, faded in and out at its ends. Each path is sampled once and
  // each dot gets a track of it, so the compositor moves them, not the page.
  // Made after the map is drawn; the dots stay hidden until then.
  const isRunning = (s: Session) => isActive(s);
  const comets = useRef<HTMLDivElement>(null);
  const flights = L.paths.map((p, i) => (isRunning(p.s) ? `${i}:${p.d}` : "")).join("|");
  useEffect(() => {
    if (!comets.current) return;
    const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
    const anims: Animation[] = [];
    const now = new Map<string, Track>();
    comets.current.querySelectorAll<HTMLElement>("[data-i]").forEach((g) => {
      const d = L.paths[Number(g.dataset.i)].d;
      const t = now.get(d) ?? tracks.get(d) ?? track(d);
      now.set(d, t);
      const { at, keep, n } = t;
      const dur = Number(g.dataset.dur);
      const o = Number(g.dataset.o);
      g.querySelectorAll<HTMLElement>("i").forEach((c) => {
        const lag = Number(c.dataset.t) * 0.011;
        const base = Number(c.dataset.a);
        if (still) {
          c.style.transform = at(0.62 - o * 0.3 - lag);
          c.style.opacity = String(base);
          return;
        }
        // Where the head is at u, the dot is at u − lag (held at the start before), its opacity the comet's.
        const frames: Keyframe[] = [{ offset: 0, transform: at(0), opacity: 0 }];
        for (const k of keep) if (k / n <= 1 - lag) frames.push({ offset: lag + k / n, transform: at(k / n) });
        frames.push({ offset: 1, transform: at(1 - lag), opacity: 0 });
        for (const [offset, opacity] of [[0.1, base], [0.9, base]]) frames.splice(frames.findIndex((f) => (f.offset as number) > offset), 0, { offset, opacity });
        const a = c.animate(frames, { duration: dur, iterations: Infinity });
        a.startTime = -o * dur; // u = (t / dur + o) % 1
        anims.push(a);
      });
    });
    tracks = now;
    return () => anims.forEach((a) => a.cancel());
  }, [flights]);

  return (
    <div className={`lmap ${lit ? "focus" : ""}`}>
      <div className="lmap-top">
        <b>{t("map.title")}</b>
        <Today now={now} usage={usage} running={running.length > 0} />
      </div>
      <div className="lmap-box" ref={box} style={{ height: H * scale }}>
        <div className="lmap-stage" style={{ width: W, height: H, transform: `scale(${scale})` }}>
          <div
            className={`lmap-aura ${running.length ? "on" : ""}`}
            style={{ left: L.hub.x - HUB.s - 22, top: L.hub.y - HUB.s - 16, width: (HUB.s + 22) * 2, height: (HUB.s + 16) * 2 }}
            aria-hidden="true"
          />
          <svg className="lmap-back" viewBox={`0 0 ${W} ${L.H}`} width={W} height={L.H} aria-hidden="true">
            {L.edges.map((e, i) => (
              <path key={i} className={`lm-edge ${e.off ? "off" : ""} ${on(e.keys)} ${inScope(e.keys)}`} d={e.d} />
            ))}
            {L.paths.map((p) => (
              <path
                key={p.s.sessionId}
                className={`lm-flow ${isRunning(p.s) ? "" : p.s.status === SessionStatus.WAITING_INPUT ? "wait" : "fail"} ${onPath(p.keys)}`}
                d={p.d}
              />
            ))}
          </svg>
          <div className="lmap-comets" ref={comets} aria-hidden="true">
            {L.paths.map((p, i) =>
              !isRunning(p.s)
                ? null
                : [0, 1].map((j) => (
                    <div key={`${p.s.sessionId}-${j}`} className="lm-comet" data-i={i} data-o={j / 2} data-dur={4200 + i * 600}>
                      {[5, 4, 3, 2, 1, 0].map((t) => (
                        <i key={t} data-t={t} data-a={t ? 0.4 - t * 0.07 : 1} style={dot(t)} />
                      ))}
                    </div>
                  )),
            )}
          </div>

          <div
            className={`lmap-hub ${pick ? "back" : ""}`}
            style={{ left: L.hub.x - HUB.s, top: L.hub.y - HUB.s, width: HUB.s * 2 }}
            title={pick ? t("map.backToAll") : undefined}
            onClick={() => pick && onPick("")}
          >
            {only ? (
              <ThisMacHub />
            ) : (
              <>
                <div className="lmap-icon" style={{ width: HUB.s * 2, height: HUB.s * 2 }}>
                  <Mark size={46} />
                </div>
                <b>Hermesh</b>
                <span>
                  <Icon name="lock" className="sm" />
                  {t("map.encrypted")}
                </span>
              </>
            )}
          </div>

          {L.grants.map((n) => (
            <Node
              key={n.key}
              n={n}
              className={`g ${only && !n.client.lastSeen ? "off" : ""} ${lightNode(n.key)}`}
              pinned={pick === n.key}
              label={n.client.label}
              onFocus={setHover}
              onClick={() => toggle(n.key)}
            >
              <GrantIcon label={n.client.label} />
              <span className="lm-t">
                <b>{n.client.label}</b>
                {only && !n.client.lastSeen ? <small>{t("map.notYet")}</small> : <Expiry c={n.client} now={now} />}
              </span>
              <Tail online running={runningCount(L.paths, n.key)} />
            </Node>
          ))}
          {L.machines.map((n) => (
            <Node
              key={n.key}
              n={n}
              className={`m ${n.machine.online ? "" : "off"} ${lightNode(n.key)}`}
              label={n.machine.local ? t("thisMac") : n.machine.machineName}
              pinned={pick === n.key}
              onFocus={setHover}
              onClick={() => toggle(n.key)}
              onMenu={() => machineMenu(n.machine)}
            >
              <span className="lm-ic"><Icon name={machineIcon(n.machine)} /></span>
              <span className="lm-t">
                <b>{n.machine.local ? t("thisMac") : n.machine.machineName}</b>
                <span className="lm-agents">
                  {(n.machine.status?.agents ?? []).filter((a) => a.available).map((a) => (
                    <span key={a.name} className={a.loggedIn ? "" : "out"} title={a.loggedIn ? undefined : t("map.notSignedIn")}>
                      <AgentLogo agent={a.name} size={13} />
                    </span>
                  ))}
                </span>
              </span>
              {n.machine.status?.paused ? <small className="lm-meta">{t("map.paused")}</small> : <Tail online={n.machine.online} running={runningCount(L.paths, n.key)} />}
            </Node>
          ))}
          {L.projects.map((n) => (
            <Node
              key={n.key}
              n={n}
              className={`p ${n.online ? "" : "off"} ${lightNode(n.key)}`}
              label={n.projectId}
              pinned={pick === n.key}
              onFocus={setHover}
              onClick={() => toggle(n.key)}
            >
              <span className="lm-ic"><Icon name="folder" /></span>
              <span className="lm-t"><b>{n.projectId}</b></span>
              <Tail online={n.online} running={runningCount(L.paths, n.key)} />
            </Node>
          ))}
          {L.extras.map((n) =>
            n.action ? (
              <button key={n.key} className="lm-node ghost" style={pos(n)} onClick={n.action}>
                + {n.label}
              </button>
            ) : (
              <div key={n.key} className="lm-node more" style={pos(n)}>{n.label}</div>
            ),
          )}

          {card && <ClientCard ref={cardRef} c={card.client} now={now} style={{ left: card.x, top: cardTop }} />}

          {/* A question's ping, under its dots (drawn by the compositor, as the comets). */}
          <div className="lmap-pings" aria-hidden="true">
            {L.paths.map((p) => {
              const a = L.at.get(p.keys[0])!;
              return p.s.status === SessionStatus.WAITING_INPUT ? (
                <span key={p.s.sessionId} className={`lm-mark wait ${onPath(p.keys)}`} style={{ left: a.x + a.w, top: a.y }}>
                  <i className="ping" />
                </span>
              ) : null;
            })}
          </div>
          <svg className="lmap-front" viewBox={`0 0 ${W} ${L.H}`} width={W} height={L.H} aria-hidden="true">
            {L.paths.map((p) => {
              const a = L.at.get(p.keys[0])!;
              const c = L.at.get(p.keys[2])!;
              if (p.s.status === SessionStatus.WAITING_INPUT)
                return (
                  <g key={p.s.sessionId} className={`lm-mark wait ${onPath(p.keys)}`}>
                    <circle cx={a.x + a.w} cy={a.y} r={3.5} />
                    <circle cx={c.x} cy={c.y} r={3.5} />
                  </g>
                );
              if (isFailed(p.s))
                return (
                  <g key={p.s.sessionId} className={`lm-mark fail ${onPath(p.keys)}`}>
                    <circle cx={c.x} cy={c.y} r={7} />
                    <text x={c.x} y={c.y + 3.5} textAnchor="middle">!</text>
                  </g>
                );
              return null;
            })}
          </svg>
        </div>
      </div>
      <div className="lmap-foot">
        {running.length > 0 && <span><i className="run" />{t("map.running")}</span>}
        {waiting.length > 0 && <span><i className="wait" />{t("map.waiting")}</span>}
        {failed.length > 0 && <span><i className="fail" />{t("map.failed")}</span>}
        {offline && <span><i className="off" />{t("map.offline")}</span>}
        {unseen && <span><i className="off" />{t("map.notYet")}</span>}
        {live.length > 0 && <span className="hint">{pick ? t("map.hintPicked") : t("map.hintPick")}</span>}
      </div>
    </div>
  );
}

function Node({ n, className, pinned, label, onFocus, onClick, onMenu, children }: {
  n: Spot; className: string; pinned: boolean; label: string; onFocus: (k: string | null) => void; onClick: () => void; onMenu?: () => void; children: ReactNode;
}) {
  return (
    <button
      className={`lm-node ${className} ${pinned ? "pin" : ""}`}
      style={pos(n)}
      aria-label={label}
      aria-pressed={pinned}
      onContextMenu={onMenu && ((e) => (e.preventDefault(), onMenu()))}
      onMouseEnter={() => onFocus(n.key)}
      onMouseLeave={() => onFocus(null)}
      onFocus={() => onFocus(n.key)}
      onBlur={() => onFocus(null)}
      onClick={onClick}
    >
      {children}
    </button>
  );
}

/** The map's headline: today's sessions, how they ended, and the tokens they used; the dot beats while one runs. */
function Today({ now, usage, running }: { now: Date; usage: GetUsageResponse | null; running: boolean }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const n = todayStats(st.sessions, now);
  const tokens = useMemo(() => (usage ? usageYear(usage.days, now).today : null), [usage, now]);
  return (
    <span className={`lmap-pill ${running ? "" : "quiet"}`} role="status" aria-label={t("map.today")}>
      <i />
      <Trans
        t={t}
        i18nKey="map.todayStats"
        count={n.sessions}
        values={{ done: n.done, failed: n.failed }}
        components={{ b: <b />, bad: <b className={n.failed ? "bad" : ""} /> }}
      />
      {tokens !== null && <> · <b>{tokensText(tokens)}</b> tokens</>}
    </span>
  );
}

/** A grant's second line: Personal agent, or how soon it expires once that is within a week. */
function Expiry({ c, now }: { c: Client; now: Date }) {
  const { t } = useTranslation("overview");
  const until = dateOf(c.expiresAt);
  const days = until ? Math.ceil((until.getTime() - now.getTime()) / 86_400_000) : Infinity;
  if (days > 7) return <small>{t("map.personalAgent")}</small>;
  return <small className="soon">{days <= 0 ? t("map.expiresToday") : t("map.expiresIn", { count: days })}</small>;
}

/** A machine only's hub: this Mac, whether it is online and takes new sessions. */
function ThisMacHub() {
  const { t } = useTranslation("overview");
  const st = useStore();
  const m = st.machines.find((x) => x.local);
  const online = st.status?.relayConnected === true;
  const paused = m?.status?.paused === true;
  return (
    <>
      <div className="lmap-icon mac" style={{ width: HUB.s * 2, height: HUB.s * 2 }} onContextMenu={(e) => (e.preventDefault(), m && machineMenu(m))}>
        <Icon name="laptop" />
      </div>
      <b>{t("thisMac")}</b>
      <span>
        <i className={`dotc ${online && !paused ? "on" : paused ? "warn" : ""}`} />
        {!online ? t("map.offline") : paused ? t("map.paused") : t("map.accepting")}
      </span>
    </>
  );
}

/**
 * What a grant lets its agent do on this Mac (a machine only), under its
 * node: how much it is trusted, the projects and coding agents it may use
 * here, how long, and what it did here.
 */
function ClientCard({ c, now, style, ref }: { c: Client; now: Date; style: CSSProperties; ref: Ref<HTMLDivElement> }) {
  const { t } = useTranslation("overview");
  const st = useStore();
  const until = dateOf(c.expiresAt);
  const seen = dateOf(c.lastSeen);
  const today = st.sessions.filter((s) => s.createdBy === c.clientId && sameDay(createdDate(s), now)).length;
  const projects = c.spec?.projectIds ?? [];
  const agents = c.spec?.agents ?? [];
  const chips = (all: boolean, items: string[], allText: string) =>
    all || items.length === 0 ? <span>{allText}</span> : <span className="lm-chips">{items.map((x) => <em key={x}>{x}</em>)}</span>;
  return (
    <div className="lm-card" ref={ref} style={style} role="dialog" aria-label={c.label} onClick={(e) => e.stopPropagation()}>
      <div className="lm-card-h">
        <GrantIcon label={c.label} />
        <b>{c.label}</b>
      </div>
      <p className="lm-card-sub">{c.custodian ? t("card.custodian", { host: hostOf(c.custodian) }) : t("card.ownKey")}</p>
      <div className="lm-card-full">
        <Icon name="shield" />
        <span>{t("card.fullAccess")}</span>
      </div>
      <dl className="lm-card-kv">
        <dt>{t("card.projects")}</dt>
        <dd>{chips(projects.includes("*"), projects, t("card.allProjects"))}</dd>
        <dt>{t("card.agents")}</dt>
        <dd>{chips(agents.includes("*"), agents.map(codingAgentTitle), t("card.allAgents"))}</dd>
        <dt>{t("card.expiry")}</dt>
        <dd>{until ? t("card.daysLeft", { count: Math.max(0, Math.ceil((until.getTime() - now.getTime()) / 86_400_000)), date: monthDay(until) }) : "—"}</dd>
        <dt>{t("card.here")}</dt>
        <dd>{seen ? t("card.seen", { count: today, ago: agoText(seen, now) }) : t("map.notYet")}</dd>
      </dl>
      <p className="lm-card-note">{t("card.revokeElsewhere")}</p>
    </div>
  );
}

/** A machine node's context menu: its settings (a click on it drills down). */
async function machineMenu(m: Machine) {
  const id = await window.hermesh?.popupMenu([{ id: "settings", label: i18n.t("overview:map.machineSettings") }]);
  if (id === "settings") window.hermesh?.openSettings(`machines/${m.machineId}`);
}

/** Offline, or how many of its sessions run now. */
function Tail({ online, running }: { online: boolean; running: number }) {
  const { t } = useTranslation("overview");
  if (!online) return <small className="lm-meta">{t("map.offline")}</small>;
  if (!running) return null;
  return (
    <span className="lm-badge" aria-label={t("runningCount", { count: running })}>
      <i />
      {running}
    </span>
  );
}

function GrantIcon({ label }: { label: string }) {
  const logo = kindNamed(label)?.logo;
  if (logo) return <AgentTile agent={logo} size={26} />;
  return (
    <span className="av" style={{ width: 26, height: 26, background: grantColor(label) }} aria-hidden="true">
      {label.slice(0, 1).toUpperCase()}
    </span>
  );
}

const pos = (n: Spot) => ({ left: n.x, top: n.y - n.h / 2, width: n.w, height: n.h });

/**
 * A dot's way along a path of the map's (M, L and C commands) at even speed:
 * at(u) is where it is when it has gone u (0–1) of the length, and keep the
 * steps (of n) where its track must turn for a straight move between them to
 * stay within 0.05 px of the curve.
 */
type Track = { at: (u: number) => string; keep: number[]; n: number };
/** The tracks of the comets on the map now, kept for when it is drawn again. */
let tracks = new Map<string, Track>();

function track(d: string): Track {
  const line: [number, number][] = [];
  for (const [, cmd, args] of d.matchAll(/([MLC])([^MLC]*)/g)) {
    const v = args.trim().split(/\s+/).map(Number);
    if (cmd !== "C") {
      line.push([v[0], v[1]]);
      continue;
    }
    const [x0, y0] = line.at(-1)!;
    for (let j = 1; j <= 48; j++) {
      const t = j / 48;
      const u = 1 - t;
      const b = [u * u * u, 3 * u * u * t, 3 * u * t * t, t * t * t];
      line.push([b[0] * x0 + b[1] * v[0] + b[2] * v[2] + b[3] * v[4], b[0] * y0 + b[1] * v[1] + b[2] * v[3] + b[3] * v[5]]);
    }
  }
  const at0 = [0];
  for (let j = 1; j < line.length; j++) at0.push(at0[j - 1] + Math.hypot(line[j][0] - line[j - 1][0], line[j][1] - line[j - 1][1]));
  const len = at0.at(-1)!;
  // Points 1 px apart by length.
  const n = Math.max(24, Math.ceil(len));
  const pts: [number, number][] = [];
  for (let k = 0, j = 1; k <= n; k++) {
    const want = (k / n) * len;
    while (j < line.length - 1 && at0[j] < want) j++;
    const f = at0[j] === at0[j - 1] ? 0 : (want - at0[j - 1]) / (at0[j] - at0[j - 1]);
    pts.push([line[j - 1][0] + (line[j][0] - line[j - 1][0]) * f, line[j - 1][1] + (line[j][1] - line[j - 1][1]) * f]);
  }
  const keep = new Set([0, n]);
  const simplify = (a: number, b: number) => {
    let worst = 0;
    let at = -1;
    for (let k = a + 1; k < b; k++) {
      const f = (k - a) / (b - a);
      const e = Math.hypot(pts[a][0] + (pts[b][0] - pts[a][0]) * f - pts[k][0], pts[a][1] + (pts[b][1] - pts[a][1]) * f - pts[k][1]);
      if (e > worst) [worst, at] = [e, k];
    }
    if (worst <= 0.05) return;
    keep.add(at);
    simplify(a, at);
    simplify(at, b);
  };
  simplify(0, n);
  const at = (u: number) => {
    const f = Math.min(1, Math.max(0, u)) * n;
    const k = Math.min(n - 1, Math.floor(f));
    const x = pts[k][0] + (pts[k + 1][0] - pts[k][0]) * (f - k);
    const y = pts[k][1] + (pts[k + 1][1] - pts[k][1]) * (f - k);
    return `translate(${x.toFixed(2)}px,${y.toFixed(2)}px)`;
  };
  return { at, keep: [...keep].sort((a, b) => a - b), n };
}

/** A comet's dot t (0 its head, 1–5 its tail, smaller; data-a, fainter), centred on where its track puts it. */
const dot = (t: number) => {
  const r = t ? 2.2 - t * 0.3 : 2.6;
  return { left: -r, top: -r, width: r * 2, height: r * 2 };
};

function runningCount(paths: Path[], key: string): number {
  return paths.filter((p) => isActive(p.s) && p.keys.includes(key)).length;
}

function machineIcon(m: Machine): string {
  if (m.local) return "laptop";
  const p = (m.status?.platform ?? "").toLowerCase();
  return p.includes("darwin") || p.includes("mac") ? "mini" : "server";
}
