import { useEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { AgentLogo } from "@hermesh/ui";
import { Cat } from "../her/Cat";
import { Icon } from "../icons";
import { aurora, fx } from "./fx";
import { messageMs, messages, meshWires, type Node } from "./samples";
import { rise } from "./Stage";

// 4 Sign in (docs/cold-start-prd.md §5.6): what the Hermesh network does, as the website's
// connection map at work, and signing in, which can wait. Signed in, every node sparks.

export function Network({ signedIn, signIn, own, next }: { signedIn: boolean; signIn: () => void; own: () => void; next: () => void }) {
  const { t } = useTranslation("firstrun");
  const nodes = useRef<Partial<Record<Node, HTMLElement | null>>>({});
  useEffect(() => {
    if (!signedIn) return;
    (["cl", "her", "you", "cc", "cx", "gm"] as Node[]).forEach((n, i) => fx.later(() => fx.burst(nodes.current[n] ?? null, aurora(), 16, 3.6), 200 + i * 110));
  }, [signedIn]);
  return (
    <>
      <div className="fr-main mid wide" style={{ paddingBottom: 6 }}>
        <h2 className="fr-title in" style={rise(0)}>
          {signedIn ? t("net.titleIn") : t("net.title")}
        </h2>
        <p className="fr-text in" style={{ ...rise(1), maxWidth: 520 }}>
          {signedIn ? t("net.subIn") : t("net.sub")}
        </p>
        <MeshMap nodes={nodes} />
        <div className="fr-gains in" style={rise(3)}>
          {[1, 2, 3].map((n) => (
            <div key={n}>
              <b>{t(`net.gain${n}` as "net.gain1")}</b>
              {t(`net.gain${n}Body` as "net.gain1Body")}
            </div>
          ))}
        </div>
      </div>
      <div className="fr-end in" style={rise(4)}>
        {signedIn ? (
          <button className="btn primary md push" onClick={next}>
            {t("net.next")}
          </button>
        ) : (
          <>
            <button className="link" onClick={own}>
              {t("net.own")}
            </button>
            <button className="link push" style={{ marginRight: 8 }} onClick={next}>
              {t("net.skip")}
            </button>
            <button className="btn primary md" onClick={signIn}>
              {t("net.signIn")}
            </button>
          </>
        )}
      </div>
    </>
  );
}

/** The map: three that send (cloud assistants, Her, you) and three agents on three computers, all reaching all (§5.6.2). */
function MeshMap({ nodes }: { nodes: React.MutableRefObject<Partial<Record<Node, HTMLElement | null>>> }) {
  const { t } = useTranslation("firstrun");
  const wires = useRef<Record<string, SVGPathElement | null>>({});
  const [i, setI] = useState(-1);
  const [shown, setShown] = useState(-1);
  const still = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
  useEffect(() => {
    if (still) return;
    let tick: ReturnType<typeof setInterval> | undefined;
    const begin = setTimeout(() => {
      setI(0);
      tick = setInterval(() => setI((n) => n + 1), messageMs);
    }, 1100);
    return () => (clearTimeout(begin), clearInterval(tick));
  }, [still]);
  const m = i >= 0 ? messages[i % messages.length] : undefined;
  useEffect(() => {
    if (!m) return;
    setShown(-1);
    for (const [w, back] of m.wires) fx.travel(wires.current[w] ?? null, back);
    const timer = setTimeout(() => setShown(i), 650);
    return () => clearTimeout(timer);
  }, [i]);
  const lit = new Set(m?.wires.map(([w]) => w));
  const on = new Set(shown === i && m ? m.on : []);
  const node = (id: Node, x: number, y: number, face: ReactNode, name: string, sub: string) => (
    <div className={`fr-node ${on.has(id) ? "on" : ""}`} ref={(el) => void (nodes.current[id] = el)} style={{ left: x, top: y }}>
      {face}
      {name}
      <small>{sub}</small>
    </div>
  );
  const chip = (id: Node, x: number, logo: string, name: string, where: string) => (
    <div className={`fr-chip ${on.has(id) ? "on" : ""}`} ref={(el) => void (nodes.current[id] = el)} style={{ left: x }}>
      <AgentLogo agent={logo} size={28} />
      <span>
        <b>{name}</b>
        <small>{where}</small>
      </span>
    </div>
  );
  // The bubble sits halfway along the message's first wire.
  let at = { left: 316, top: 112 };
  const w = m && wires.current[m.wires[0][0]];
  if (w) {
    const q = w.getPointAtLength(w.getTotalLength() / 2);
    at = { left: Math.max(110, Math.min(522, q.x)), top: Math.max(98, q.y - 16) };
  }
  const face = (from: Node) =>
    from === "her" ? <Cat small /> : (
      <span className="av">
        {from === "you" ? <Icon name="user" /> : from === "cl" ? <Icon name="robot" /> : <AgentLogo agent={from === "cx" ? "codex" : from === "cc" ? "claude" : "gemini"} size={22} />}
      </span>
    );
  return (
    <div className="fr-mesh in" style={rise(2)} aria-label={t("net.map")}>
      <svg className="wires" width="632" height="286" viewBox="0 0 632 286" aria-hidden="true">
        {Object.entries(meshWires).map(([id, d]) => (
          <path key={id} ref={(el) => void (wires.current[id] = el)} d={d} className={lit.has(id) ? "lit" : ""} />
        ))}
      </svg>
      {node("cl", 96, 10, <span className="dot"><Icon name="robot" /></span>, t("net.cloud"), t("net.cloudSub"))}
      {node("her", 316, 4, <Cat small={false} />, "Her", t("net.herSub"))}
      {node("you", 536, 10, <span className="dot round"><Icon name="user" /></span>, t("net.you"), t("net.youSub"))}
      <div className="fr-teambox">
        <span>{t("net.team")}</span>
        <em>
          <Icon name="shield" />
          {t("net.e2e")}
        </em>
      </div>
      {chip("cc", 116, "claude", "Claude Code", t("herStep.show.here"))}
      {chip("cx", 316, "codex", "Codex", t("herStep.show.windows"))}
      {chip("gm", 516, "gemini", "Gemini", t("herStep.show.linux"))}
      <div className={`fr-bubble ${shown === i && m ? "up" : ""}`} style={at} aria-live="polite">
        {m && (
          <>
            {face(m.from)}
            <em>→ {t(`net.${m.to}` as "net.toYou")}</em>
            {t(`net.${m.say}` as "net.m1")}
          </>
        )}
      </div>
    </div>
  );
}
