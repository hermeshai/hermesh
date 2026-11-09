import { useEffect, useId, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { BotSummary } from "@hermesh/her-protocol";
import { Icon } from "../icons";
import { errorText } from "../store";
import { useHer } from "./context";
import { modelTitle } from "./format";
import {
  checking,
  chooseChatGpt,
  chooseKey,
  choosePlan,
  clause,
  connectedName,
  controls,
  finish,
  folds,
  keyChoices,
  keyFailed,
  keyPage,
  keySaved,
  otherWay,
  pickProvider,
  planChoices,
  providerName,
  signInEnded,
  start,
  type Flow,
  type Said,
} from "./onboarding";

// The onboarding in Her's conversation while no model works there (onboarding.ts): Her's
// bubbles and the owner's, a quiet list of choices under Her's last one, and a field for a key
// that goes to her and nowhere else. Each environment has its own, kept while the app runs; an answer
// that comes once another environment shows goes to its own.

const flows = new Map<string, Flow>();
const listeners = new Set<() => void>();
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => void listeners.delete(l);
};

function put(machineId: string, f: Flow) {
  flows.set(machineId, f);
  for (const l of listeners) l();
}

/** Moves an environment's onboarding on, whichever environment shows. */
function update(machineId: string, change: (f: Flow) => Flow) {
  const f = flows.get(machineId);
  const next = f && change(f);
  if (next && next !== f) put(machineId, next);
}

/**
 * How long HerStatus has to say no model works before that counts without her saying so too:
 * it says Her runs a moment before her has listed its models (internal/herhost).
 */
const SETTLE = 3000;

/** Whether `on` has held for `ms`. */
function useLasting(on: boolean, ms: number): boolean {
  const [held, setHeld] = useState(false);
  useEffect(() => {
    setHeld(false);
    if (!on) return;
    const timer = setTimeout(() => setHeld(true), ms);
    return () => clearTimeout(timer);
  }, [on, ms]);
  return on && held;
}

/**
 * An environment's onboarding: begun when no model works there (her says so when the
 * conversation opens, or HerStatus does for a while), done once the conversation opens on a
 * model (`opened`), however that was connected. It stays after `last`, the conversation's last
 * message then.
 */
export function useOnboarding(machineId: string, s: { confirmed: boolean; reported: boolean; opened: string | null; session: string | null; last: string | null }): Flow | null {
  const flow = useSyncExternalStore(subscribe, () => flows.get(machineId) ?? null);
  const wanted = useLasting(s.reported, SETTLE) || s.confirmed;
  useEffect(() => {
    const f = flows.get(machineId);
    if (wanted && (!f || f.step === "done")) put(machineId, start(Date.now()));
  }, [machineId, wanted]);
  useEffect(() => {
    const model = s.opened;
    if (model) update(machineId, (f) => finish(f, { model, session: s.session, after: s.last, at: Date.now() }));
  }, [machineId, s.opened, s.session]);
  return flow;
}

/** The onboarding as a row of the conversation; once done, one quiet line and Her's first words. */
export function Onboarding({ flow, bot }: { flow: Flow; bot: BotSummary }) {
  const { t, i18n } = useTranslation("her");
  const her = useHer();
  const id = her.machine.machineId;
  const machine = her.machine.machineName;
  const { models } = her.env;
  const lang = i18n.language;
  const provider = (id: string) => models.providers.find((x) => x.id === id);
  // A Token Plan by the name the app gives it (her's catalog names them in English only).
  const name = (id: string) => {
    const p = provider(id);
    if (!p) return id;
    return p.plan ? t(`plans.${p.id}.name` as "plans.bailian-token-plan.name", { defaultValue: p.name }) : providerName(p, lang);
  };
  // What is said while it shows slides in.
  const shown = useRef(flow.step === "done" ? Infinity : flow.said.length);

  if (flow.step === "done") {
    return (
      <div className="ob">
        <div className="ob-fold">
          <Icon name="check" className="sm" />
          <span>{t("onboard.connected", { name: connectedName(flow, name) })}</span>
        </div>
        <Hers fresh={shown.current !== Infinity} text={t("onboard.done", { model: modelTitle(flow.done!.model, models) })} />
      </div>
    );
  }

  const signIn = () => {
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
  // The key goes to her (auth/set) and is let go: nothing here keeps it.
  const save = async (key: string) => {
    const provider = flow.provider;
    if (!provider) return;
    update(id, checking);
    try {
      const r = await her.call("auth/set", { provider, api_key: key });
      update(id, (f) => keySaved(f, r.verified));
    } catch (e) {
      update(id, (f) => keyFailed(f, e, errorText(e)));
    }
  };

  const text = (s: Said): string => {
    switch (s.what) {
      case "greeting":
        return t("onboard.greeting", { name: bot.name, machine });
      case "browser":
        return t("onboard.browser");
      case "signedIn":
        return s.account ? t("onboard.signedIn", { account: s.account }) : t("onboard.signedInPlain");
      case "signInFailed":
        return t("onboard.signInFailed");
      case "signInCancelled":
        return t("onboard.signInCancelled");
      case "whichProvider":
        return t("onboard.whichProvider");
      case "whichPlan":
        return t("onboard.whichPlan");
      case "whichWay":
        return t("onboard.whichWay");
      case "pasteKey":
        return t("onboard.pasteKey", { provider: name(s.provider), machine });
      case "keyRejected":
        return folds(s.message) ? t("onboard.keyRejectedPlain", { provider: name(s.provider) }) : t("onboard.keyRejected", { provider: name(s.provider), message: clause(s.message) });
      case "keyFailed":
        return t("onboard.keyFailed", { machine });
      case "keyUnverified":
        // A Token Plan's key is checked only by the first reply (her has nothing to ask before).
        return provider(s.provider)?.plan ? t("onboard.planUnverified") : t("onboard.keyUnverified", { provider: name(s.provider) });
      case "chatgpt":
        return t("onboard.chatgpt");
      case "plan":
        return t("onboard.plan");
      case "key":
        return t("onboard.key");
      case "otherWay":
        return t("onboard.otherWay");
      case "provider":
        return name(s.provider);
    }
  };
  /** An error's own words, under Her's: inside the bubble when short, folded away when long. */
  const detail = (s: Said) => (s.what === "signInFailed" || s.what === "keyFailed" ? s.detail : s.what === "keyRejected" && folds(s.message) ? s.message : "");

  const busy = flow.step === "checking";
  const asked = flow.step === "key" || busy ? flow.provider : undefined;
  const plans = planChoices(models.providers);
  const pick = (p: string) => update(id, (f) => pickProvider(f, p));
  const other = (
    <button className="link ob-get" disabled={busy} onClick={() => update(id, otherWay)}>
      {t("onboard.otherWay")}
    </button>
  );
  const at = controls(flow);
  const control = (i: number, s: Said): ReactNode => {
    switch (at.get(i)) {
      case "ways":
        return (
          <>
            <Choices
              label={t("onboard.ways")}
              rows={[
                { id: "chatgpt", title: t("onboard.chatgpt"), note: t("onboard.chatgptNote"), onPick: signIn },
                // An older her has no Token Plans.
                ...(plans.length ? [{ id: "plan", title: t("onboard.plan"), note: t("onboard.planNote"), onPick: () => update(id, choosePlan) }] : []),
                { id: "key", title: t("onboard.key"), note: t("onboard.keyNote"), onPick: () => update(id, chooseKey) },
              ]}
            />
            {s.what === "greeting" && <p className="ob-note">{t("onboard.stays", { machine })}</p>}
          </>
        );
      case "browser":
        return (
          <div className="ob-pills">
            <button className="ob-pill" onClick={() => window.hermesh?.reopenHerChatGpt(id)}>
              {t("onboard.reopen")}
            </button>
            <button className="ob-pill" onClick={stop}>
              {t("onboard.otherWay")}
            </button>
          </div>
        );
      case "providers":
        return (
          <>
            <Choices
              label={t("onboard.providers")}
              disabled={busy}
              rows={[
                ...keyChoices(models.providers).map((p) => ({ id: p.id, title: name(p.id), current: p.id === asked, onPick: () => pick(p.id) })),
                { id: "", title: t("onboard.otherServices"), icon: "ext" as const, onPick: her.openConsole },
              ]}
            />
            {other}
          </>
        );
      case "plans":
        return (
          <>
            <Choices
              label={t("onboard.plans")}
              disabled={busy}
              rows={plans.map((p) => ({
                id: p.id,
                title: name(p.id),
                note: t(`plans.${p.id}.note` as "plans.bailian-token-plan.note", { defaultValue: "" }),
                current: p.id === asked,
                onPick: () => pick(p.id),
              }))}
            />
            {other}
          </>
        );
      case "key":
        return flow.provider ? (
          <KeyField key={i} name={name(flow.provider)} page={keyPage(flow.provider, provider(flow.provider)?.plan)} busy={busy} onSave={(key) => void save(key)} />
        ) : null;
      default:
        return null;
    }
  };

  return (
    <div className="ob">
      {flow.said.map((s, i) =>
        s.who === "owner" ? (
          <div key={i} className={`um ${i >= shown.current ? "add-anim" : ""}`}>
            <div className="bub">{text(s)}</div>
          </div>
        ) : (
          <Hers key={i} fresh={i >= shown.current} text={text(s)} detail={detail(s)}>
            {control(i, s)}
          </Hers>
        ),
      )}
      {flow.step === "connecting" && (
        <div className="bm working">
          <div className="bm-b">
            <div className="bline">
              <div className="bbub wbub">
                <div className="wb-h" title={t("line.typing")}>
                  <span className="dots" aria-hidden="true">
                    <i />
                    <i />
                    <i />
                  </span>
                </div>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

type Choice = { id: string; title: string; note?: string; current?: boolean; icon?: "ext"; onPick: () => void };

/**
 * What to connect, as a quiet list under Her's message: a name, a line under it when the names
 * alone don't tell the choices apart, a check on the one picked.
 */
function Choices({ label, rows, disabled }: { label: string; rows: Choice[]; disabled?: boolean }) {
  return (
    <div className="ob-list" role="group" aria-label={label}>
      {rows.map((r) => (
        <ChoiceRow key={r.id || r.title} row={r} disabled={disabled} />
      ))}
    </div>
  );
}

function ChoiceRow({ row, disabled }: { row: Choice; disabled?: boolean }) {
  const note = useId();
  return (
    <button className="ob-row" aria-label={row.title} aria-describedby={row.note ? note : undefined} aria-current={row.current || undefined} disabled={disabled} onClick={row.onPick}>
      <span className="ob-row-t">
        <span className="ob-row-n">{row.title}</span>
        {row.note && (
          <span id={note} className="ob-row-d">
            {row.note}
          </span>
        )}
      </span>
      <Icon name={row.current ? "check" : (row.icon ?? "chevR")} className="sm" />
    </button>
  );
}

/** One of Her's messages, as the app writes it: the bubble of Her's own, an error's words with it, what can be done under it. */
function Hers({ text, detail = "", fresh, children }: { text: string; detail?: string; fresh: boolean; children?: ReactNode }) {
  const { t } = useTranslation("her");
  const [open, setOpen] = useState(false);
  const long = folds(detail);
  return (
    <div className={`bm ${fresh ? "add-anim" : ""}`}>
      <div className="bm-b">
        <div className="bline">
          <div className="bbub sel-text">
            {text}
            {detail && !long && <span className="ob-detail">{detail}</span>}
          </div>
        </div>
        {long && (
          <button className="ob-raw" aria-expanded={open} onClick={() => setOpen(!open)}>
            {open ? t("chat.hideError") : t("chat.showError")}
            <Icon name="chevR" className={`sm chev ${open ? "open" : ""}`} />
          </button>
        )}
        {long && open && <div className="fc-raw sel-text">{detail}</div>}
        {children}
      </div>
    </div>
  );
}

/**
 * A secret the owner types for her alone (a provider's key, a secure card's value): a password
 * field that nothing fills, remembers or corrects. Whoever holds the value empties it once sent.
 */
export function SecretInput({ label, placeholder, value, onChange, disabled, autoFocus }: {
  label: string;
  placeholder?: string;
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  autoFocus?: boolean;
}) {
  return (
    <input
      className="field"
      type="password"
      autoComplete="off"
      spellCheck={false}
      autoFocus={autoFocus}
      disabled={disabled}
      aria-label={label}
      placeholder={placeholder}
      value={value}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}

/** A provider's key, written where only her gets it (auth/set), emptied as it is saved. ↩ saves. */
function KeyField({ name, page, busy, onSave }: { name: string; page?: string; busy: boolean; onSave: (key: string) => void }) {
  const { t } = useTranslation("her");
  const [key, setKey] = useState("");
  const save = () => {
    const k = key.trim();
    if (!k || busy) return;
    setKey("");
    onSave(k);
  };
  return (
    <>
      <form
        className="ob-key"
        onSubmit={(e) => {
          e.preventDefault();
          save();
        }}
      >
        <SecretInput autoFocus disabled={busy} label={t("onboard.keyLabel", { provider: name })} placeholder={t("onboard.keyPlaceholder")} value={key} onChange={setKey} />
        <button type="submit" className="btn primary" disabled={busy || !key.trim()}>
          {busy ? t("onboard.checking") : t("onboard.save")}
        </button>
      </form>
      {page && (
        <button className="link ob-get" onClick={() => window.hermesh?.openExternal(page)}>
          {t("onboard.getKey", { provider: name })}
        </button>
      )}
    </>
  );
}
