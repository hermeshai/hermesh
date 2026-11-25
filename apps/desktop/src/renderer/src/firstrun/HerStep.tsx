import { useEffect, useRef, useState, type CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { AgentLogo } from "@hermesh/ui";
import { local } from "../api";
import { Cat } from "../her/Cat";
import { useEnvironment } from "../her/connection";
import { modelTitle } from "../her/format";
import { KeyField, putFlow, update, useFlow } from "../her/ModelOnboarding";
import { checking, chooseChatGpt, chooseKey, choosePlan, keyChoices, keyFailed, keyPage, keySaved, otherWay, pickProvider, planChoices, providerName, signInEnded, start, type Flow } from "../her/onboarding";
import { Icon } from "../icons";
import { useComputerName } from "../computer";
import { errorText } from "../store";
import { catColors, fx } from "./fx";
import "../her/her.css";
import { actStarts, frameMs, show, showWires, type Card, type TaskState } from "./samples";
import { rise, Typed } from "./Stage";

// 3 Her (docs/cold-start-prd.md §5.5): Her is turned on here (no Touch ID before an account); the
// page shows what she does with a team, as an example that plays in a loop, and asks for a model
// with three plain buttons. The model is connected by the same onboarding the Her page has
// (her/onboarding.ts), so the main window carries on from where this left off. Once a model
// works, Her wakes.

/** Her is awake: her says a model is ready (connected here, or already). */
export const awake = (m: Machine | undefined): boolean => !!m?.her?.running && !!m.her.modelReady;

/** Turns Her on for this Mac as the step opens, once; the error, if she could not be. */
export function useHerOn(m: Machine | undefined): string {
  const [error, setError] = useState("");
  const asked = useRef(false);
  useEffect(() => {
    if (!m || m.her?.enabled || asked.current) return;
    asked.current = true;
    local.setHer({ machineId: m.machineId, enabled: true }).catch((e: unknown) => setError(errorText(e)));
  }, [m?.machineId, m?.her?.enabled]);
  return error || m?.her?.error || "";
}

export function HerChoose({ m, next }: { m: Machine; next: () => void }) {
  const { t, i18n } = useTranslation("firstrun");
  const her = useTranslation("her").t;
  const id = m.machineId;
  const flow = useFlow(id);
  const env = useEnvironment(m);
  const error = useHerOn(m);
  const running = !!m.her?.running && !!env.rpc;
  const [sheet, setSheet] = useState(false);
  const [busy, setBusy] = useState(false);
  // This Mac by its own name (as in Settings), not its machine's short one.
  const computer = useComputerName(m.machineName);
  // The onboarding begins here, as the Her page would begin it.
  useEffect(() => {
    if (!flow || flow.step === "done") putFlow(id, start(Date.now()));
  }, [id]);

  const providers = env.models.providers;
  const name = (p: string) => {
    const x = providers.find((y) => y.id === p);
    if (!x) return p;
    return x.plan ? her(`plans.${x.id}.name` as "plans.bailian-token-plan.name", { defaultValue: x.name }) : providerName(x, i18n.language);
  };
  const chatgpt = () => {
    update(id, chooseChatGpt);
    const ended = window.hermesh ? window.hermesh.signInHerChatGpt(id) : Promise.reject(new Error("no bridge"));
    ended.then(
      (r) => update(id, (f) => signInEnded(f, r)),
      (e: unknown) => update(id, (f) => signInEnded(f, { state: "failed", message: errorText(e) })),
    );
  };
  const stop = () => {
    update(id, otherWay);
    window.hermesh?.cancelHerChatGpt(id);
  };
  const open = (way: (f: Flow) => Flow) => (update(id, way), setSheet(true));
  const close = () => (update(id, otherWay), setSheet(false));
  const save = async (key: string) => {
    if (!flow?.provider || !env.rpc) return;
    update(id, checking);
    setBusy(true);
    try {
      const r = await env.rpc.request("auth/set", { provider: flow.provider, api_key: key });
      update(id, (f) => keySaved(f, r.verified));
      setSheet(false);
    } catch (e) {
      update(id, (f) => keyFailed(f, e, errorText(e)));
    } finally {
      setBusy(false);
    }
  };

  const last = flow?.said[flow.said.length - 1];
  const signing = flow?.step === "signingIn" || flow?.step === "stopping";
  const pickTitle = error
    ? error
    : last?.what === "signInFailed"
      ? her("onboard.signInFailed")
      : last?.what === "signInCancelled"
        ? her("onboard.signInCancelled")
        : t("herStep.pick");

  return (
    <>
      <div className="fr-main mid wide">
        <h2 className="fr-title in" style={rise(0)}>
          {t("herStep.title")}
        </h2>
        <p className="fr-text in" style={{ ...rise(1), maxWidth: 600 }}>
          {t("herStep.sub")}
        </p>
        <Show />
        <div className="fr-pick in" style={rise(4)}>
          {signing ? (
            <>
              <b>{t("herStep.browser")}</b>
              <div className="fr-ways">
                <button className="fr-way" onClick={stop}>
                  {t("herStep.otherWay")}
                </button>
              </div>
              <span>{t("herStep.browserNote")}</span>
            </>
          ) : (
            <>
              <b className={error ? "fr-warn" : undefined}>{pickTitle}</b>
              <div className="fr-ways">
                <button className="fr-way" disabled={!running} onClick={chatgpt}>
                  <Icon name="bubble" />
                  {t("herStep.chatgpt")}
                </button>
                {/* An older her has no Token Plans. */}
                {planChoices(providers).length > 0 && (
                  <button className="fr-way" disabled={!running} onClick={() => open(choosePlan)}>
                    <Icon name="card" />
                    {t("herStep.plan")}
                  </button>
                )}
                <button className="fr-way" disabled={!running} onClick={() => open(chooseKey)}>
                  <Icon name="key" />
                  {t("herStep.key")}
                </button>
              </div>
              {/* The buttons wait for her to start (a few seconds on a new computer): said, not left to guess. */}
              <span>{running || error ? t("herStep.note") : t("herStep.starting")}</span>
            </>
          )}
        </div>
      </div>
      <div className="fr-end in" style={{ ...rise(5), minHeight: 54, paddingTop: 8 }}>
        <button className="link push" onClick={next}>
          {t("herStep.later")}
        </button>
      </div>
      {sheet && flow && (flow.step === "plan" || flow.step === "provider" || flow.step === "key" || flow.step === "checking") && (
        <div className="fr-scrim">
          <div className="fr-sheet" role="dialog" aria-modal="true" aria-label={flow.said.some((s) => s.what === "whichPlan") ? t("herStep.plan") : t("herStep.key")}>
            <div className="sh">
              <b>{flow.said.some((s) => s.what === "whichPlan") ? her("onboard.whichPlan") : her("onboard.whichProvider")}</b>
            </div>
            <div className="sb">
              <div className="fr-grp" role="list">
                {(flow.said.some((s) => s.what === "whichPlan") ? planChoices(providers) : keyChoices(providers)).map((p) => (
                  <button key={p.id} className="fr-row go" role="listitem" disabled={busy} onClick={() => update(id, (f) => pickProvider(f, p.id))}>
                    <div className="t">
                      <div className="n">{name(p.id)}</div>
                    </div>
                    {flow.provider === p.id && <Icon name="check" />}
                  </button>
                ))}
              </div>
              {flow.provider && (
                <>
                  <p className="foot">{her("onboard.pasteKey", { provider: name(flow.provider), machine: computer })}</p>
                  <KeyField
                    key={flow.provider}
                    name={name(flow.provider)}
                    page={keyPage(flow.provider, providers.find((p) => p.id === flow.provider)?.plan)}
                    busy={busy}
                    onSave={(key) => void save(key)}
                  />
                  {last?.what === "keyRejected" || last?.what === "keyFailed" ? <p className="fr-err">{last.what === "keyFailed" ? her("onboard.keyFailed", { machine: computer }) : her("onboard.keyRejectedPlain", { provider: name(flow.provider) })}</p> : null}
                </>
              )}
            </div>
            <div className="sf">
              <button className="btn push" disabled={busy} onClick={close}>
                {t("cancel")}
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

/** Her woken: she hops, the rings spread, her first words come in, and the model she talks with. */
export function HerAwake({ m, next }: { m: Machine; next: () => void }) {
  const { t } = useTranslation("firstrun");
  const env = useEnvironment(m);
  const face = useRef<HTMLDivElement>(null);
  useEffect(() => fx.later(() => fx.burst(face.current, catColors, 56, 6), 650), []);
  const model = env.models.defaults?.model;
  return (
    <>
      <div className="fr-main mid">
        <div className="fr-face" ref={face}>
          <span className="ring" style={{ "--i": 0 } as CSSProperties} />
          <span className="ring" style={{ "--i": 1 } as CSSProperties} />
          <Cat small={false} mood="wake" />
        </div>
        <p className="fr-said">
          <Typed text={t("herStep.hello")} delay={1300} />
          <br />
          <Typed text={t("herStep.ready")} delay={2100} />
        </p>
        {model && (
          <p className="fr-using in" style={rise(0, 3100)}>
            {t("herStep.model", { model: modelTitle(model, env.models) })}
          </p>
        )}
      </div>
      <div className="fr-end in" style={rise(0, 3300)}>
        <button className="btn primary md push" onClick={next}>
          {t("herStep.next")}
        </button>
      </div>
    </>
  );
}

/** The example: Her patrols and asks, hands one fix around three machines, and reports (§5.5.2). */
function Show() {
  const { t } = useTranslation("firstrun");
  const [i, setI] = useState(0);
  const [timer, setTimer] = useState(0);
  const wires = useRef<Partial<Record<Card, SVGPathElement | null>>>({});
  const f = show[i % show.length];
  const still = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
  // Starts 700ms after the page, then a frame every 2.1s; an act's tab starts again from its first frame.
  useEffect(() => {
    if (still) return;
    let tick: ReturnType<typeof setInterval> | undefined;
    const begin = setTimeout(() => {
      tick = setInterval(() => setI((n) => n + 1), frameMs);
    }, timer === 0 ? 700 : 0);
    return () => (clearTimeout(begin), clearInterval(tick));
  }, [timer, still]);
  useEffect(() => {
    for (const [card, back] of f.go ?? []) fx.travel(wires.current[card] ?? null, back);
  }, [i]);
  const jump = (act: number) => (setI(actStarts[act]), setTimer((n) => n + 1));
  const said = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = said.current;
    if (!el) return;
    el.classList.remove("pop");
    void el.offsetWidth;
    el.classList.add("pop");
  }, [f.say]);

  const card = (id: Card, logo: string, title: string, where: string) => {
    const [task, state] = f.cards[id] ?? (["idle", "idle"] as [string, TaskState]);
    return (
      <div className={`fr-card ${state}`} aria-label={title}>
        <div className="th">
          <AgentLogo agent={logo} size={26} />
          <span>
            <b>{title}</b>
            <small>{where}</small>
          </span>
        </div>
        <div className="task">{t(`herStep.show.task.${task}` as "herStep.show.task.idle")}</div>
        <div className="st" key={`${task}-${state}`}>
          {state === "run" && (
            <>
              <i />
              {t("herStep.show.run")}
            </>
          )}
          {state === "ok" && (
            <>
              <Icon name="check" />
              {t("herStep.show.ok")}
            </>
          )}
          {state === "fail" && <>✕ {t("herStep.show.fail")}</>}
        </div>
      </div>
    );
  };

  return (
    <>
      <div className="fr-show in" style={rise(2)} data-ask={f.ask ?? 0} data-out={f.out ? 1 : 0} aria-label={t("herStep.sample")}>
        <span className="tag">{t("herStep.sample")}</span>
        <svg className="wires" width="632" height="232" viewBox="0 0 632 232" aria-hidden="true">
          {(Object.keys(showWires) as Card[]).map((c) => (
            <path key={c} ref={(el) => void (wires.current[c] = el)} d={showWires[c]} />
          ))}
        </svg>
        <div className="s-top">
          <Cat small />
          <div className="fr-say" ref={said}>
            {t(`herStep.show.${f.say}` as "herStep.show.s1")}
          </div>
        </div>
        <div className="s-row s-ask" aria-hidden="true">
          <span className="btn primary">{t("herStep.show.fix")}</span>
          <span className="btn">{t("herStep.show.skip")}</span>
        </div>
        <svg className="cur" viewBox="0 0 16 16" aria-hidden="true">
          <path d="M2 1l11 6-4.6 1.4L6.5 13z" fill="currentColor" stroke="#000" strokeWidth=".8" />
        </svg>
        <div className="s-row s-out" aria-hidden="true">
          <span>PR #222</span>
          <span>{t("herStep.show.notes")}</span>
        </div>
        <div className="fr-cards">
          {card("cc", "claude", "Claude Code", t("herStep.show.here"))}
          {card("cx", "codex", "Codex", t("herStep.show.windows"))}
          {card("gm", "gemini", "Gemini", t("herStep.show.linux"))}
        </div>
      </div>
      <div className="fr-acts in" style={rise(3)} role="tablist">
        {[t("herStep.acts.patrol"), t("herStep.acts.relay"), t("herStep.acts.report")].map((label, act) => (
          <button key={act} role="tab" aria-selected={f.act === act} className={f.act === act ? "on" : ""} onClick={() => jump(act)}>
            {label}
          </button>
        ))}
      </div>
    </>
  );
}
