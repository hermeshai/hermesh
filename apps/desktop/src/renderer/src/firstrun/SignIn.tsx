import { useEffect, useRef, useState } from "react";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { Code, ConnectError } from "@connectrpc/connect";
import { officialCenter, rootPlatform } from "@hermesh/core";
import { Mark } from "@hermesh/ui";
import { local } from "../api";
import { CodeTiles } from "../components";
import { Icon } from "../icons";
import { errorText, useStore } from "../store";
import { i18n } from "../../../shared/i18n";
import { Frame } from "./Frame";
import { setFirstRunDone, setProgress } from "./progress";

// What the first run (SignInFlow.tsx) and the main window share of signing in: the center, the
// note after erasing everything, a machine only becoming a trusted device (Join), a computer
// another device removed, and how a failure is said (docs/trusted-devices.md 4.1–4.2).

const bridge = () => window.hermesh!;

// The center to sign in to: the official one (development and tests may set
// localStorage centerURL to another).
export function savedCenter(): string {
  try {
    return localStorage.getItem("centerURL") || officialCenter;
  } catch {
    return officialCenter;
  }
}

/**
 * Once, after this Mac erased everything (or was erased from another device):
 * what was done, who erases next time it connects, and the worktrees kept.
 */
export function ErasedNote() {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const e = st.status?.erased;
  const id = e ? e.eraseId || String(e.at?.seconds ?? "") : "";
  const [seen, setSeen] = useState(() => {
    try {
      return localStorage.getItem("erasedSeen") ?? "";
    } catch {
      return "";
    }
  });
  if (!e || seen === id) return null;
  const dismiss = () => {
    try {
      localStorage.setItem("erasedSeen", id);
    } catch {
      // Shown again next time.
    }
    setSeen(id);
  };
  return (
    <div className="erased-note" role="status">
      <Icon name={e.by ? "info" : "check"} />
      <div className="t">
        <b>{e.by ? t("erased.by", { device: e.by }) : t("erased.done")}</b>
        {e.by && <span>{t("erased.byBody")}</span>}
        {e.pending.length > 0 && <span>{t("erased.pending", { names: e.pending.join(i18n.t("settings:listSeparator")) })}</span>}
        {e.keptWorktrees.length > 0 && (
          <>
            <span>{t("erased.kept")}</span>
            {e.keptWorktrees.map((p) => (
              <span key={p} className="kept">
                <span className="mono sel-text">{p}</span>
                <button className="link" onClick={() => bridge().revealKeptWorktree(p)}>{t("erased.reveal")}</button>
              </span>
            ))}
          </>
        )}
      </div>
      <button className="x" aria-label={t("erased.dismiss")} onClick={dismiss}>
        <Icon name="x" />
      </button>
    </div>
  );
}

/**
 * Whether a server address is plain HTTP to another computer: "http://…", or
 * an IP address, which the app reaches by HTTP (the relay gets certificates
 * for domain names only; internal/owner's normalizeRelayURL).
 */
export function plainHTTP(address: string): boolean {
  const s = address.trim();
  const http = /^http:\/\//i.test(s);
  if (!http && s.includes("://")) return false;
  const host = (http ? s.slice(7) : s).split("/")[0];
  const v6 = /^\[([^\]]+)\]/.exec(host);
  const h = v6 ? v6[1] : host.replace(/:\d*$/, "");
  if (!http && !v6 && !/^\d{1,3}(\.\d{1,3}){3}$/.test(h)) return false;
  return !(h === "localhost" || h.startsWith("127.") || h === "::1");
}

type JoinState =
  | { s: "asking" }
  | { s: "waiting"; code: string; verification: string }
  | { s: "approved"; code: string; by: string }
  | { s: "failed"; trouble: Trouble; shown: boolean };

/**
 * Joining an owner as another trusted device (docs/trusted-devices.md 4.2):
 * ask (one Touch ID: the new key signs the request), show the verification
 * code to compare on a device the owner has, then take the two digits that
 * device shows once it approved. Not trusted: as a machine only, with no
 * Touch ID here, and no digits when the person typed the connection address
 * (typed). relay: where devices connect, when known already (a machine only
 * becoming a trusted device); otherwise found from the center.
 */
export function Join(p: { owner: string; center: string; relay?: string; machine: boolean; trusted?: boolean; typed?: boolean; cancel: () => void }) {
  const { owner, center, relay, machine, trusted = true, typed = false, cancel } = p;
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const [state, setState] = useState<JoinState>({ s: "asking" });
  const [digits, setDigits] = useState("");
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const code = state.s === "waiting" || state.s === "approved" ? state.code : "";
  const codeRef = useRef("");
  codeRef.current = code;

  useEffect(() => {
    const abort = new AbortController();
    const signal = abort.signal;
    (async () => {
      try {
        bridge().cancelSignIn();
        const name = await bridge().computerName();
        const relayUrl = relay || (await bridge().connectURL(center));
        const platform = rootPlatform(bridge().platform);
        if (trusted) await bridge().readySigner();
        const res = await local.requestJoin(
          trusted
            ? { relayUrl, ownerId: owner, rootLabel: name, rootPlatform: platform, localMachineName: machine ? name : "" }
            : { relayUrl, ownerId: owner, rootLabel: name, rootPlatform: platform, machineOnly: true, ownerTyped: typed },
          { signal },
        );
        setState({ s: "waiting", code: res.code, verification: res.verificationCode });
        for (;;) {
          const w = await local.waitJoin({ code: res.code }, { signal });
          if (w.done && !w.matchRequired) return await finish(res.code, "");
          if (w.done) return setState({ s: "approved", code: res.code, by: w.approvedBy });
          if (w.expired) throw new Error(t("join.expired"));
        }
      } catch (e) {
        if (!signal.aborted) setState({ s: "failed", trouble: troubleOf(t, e, t("join.failed")), shown: true });
      }
    })();
    return () => abort.abort();
  }, [owner, center, relay, machine, trusted, typed, attempt]);

  // Keeps the owner: the digits typed (none when not asked for).
  const finish = async (code: string, matchCode: string) => {
    // A trusted device that is a machine too sets this Mac up next; a machine only is done.
    setProgress(null);
    try {
      await local.confirmJoin({ code, accept: true, matchCode });
      await st.refresh();
    } catch (e) {
      setProgress(null);
      throw e;
    }
  };
  const confirm = async (accept: boolean) => {
    setError("");
    try {
      if (!accept) return (await local.confirmJoin({ code, accept: false }), cancel());
      await finish(code, digits);
    } catch (e) {
      if (e instanceof ConnectError && e.code === Code.InvalidArgument) setError(t("join.wrongDigits"));
      else setState({ s: "failed", trouble: troubleOf(t, e, t("join.failed")), shown: true });
    }
  };
  const stop = () => {
    if (codeRef.current) local.confirmJoin({ code: codeRef.current, accept: false }).catch(() => {});
    cancel();
  };

  return (
    <Frame sheet={state.s === "failed" && state.shown && <TroubleSheet trouble={state.trouble} close={() => setState({ ...state, shown: false })} />}>
      <div className="status">
        <Mark size={64} variant={state.s === "failed" ? undefined : state.s === "approved" ? "lock" : state.s === "asking" ? "seq" : "wait"} />
        <h2 className="ob-h">{state.s === "approved" ? t("join.titleDigits") : t("join.titleApprove")}</h2>
        {state.s === "asking" && <p className="ob-p">{t("join.asking")}</p>}
        {state.s === "waiting" && (
          <>
            <p className="ob-p">{t(trusted ? "join.waiting" : "join.waitingMachine")}</p>
            <CodeTiles code={state.verification} hint={t("join.codeHint")} />
          </>
        )}
        {state.s === "approved" && (
          <>
            <p className="ob-p">{t("join.approved", { name: state.by })}</p>
            <input
              className="field join-digits"
              aria-label={t("join.digits")}
              inputMode="numeric"
              placeholder="00"
              value={digits}
              onChange={(e) => (setDigits(e.target.value.replace(/\D/g, "").slice(0, 2)), setError(""))}
              onKeyDown={(e) => e.key === "Enter" && digits.length === 2 && confirm(true)}
              autoFocus
            />
            {error && <Problem text={error} />}
            <div className="status-actions">
              <button className="btn" onClick={() => confirm(false)}>
                {t("join.notMe")}
              </button>
              <button className="btn primary" disabled={digits.length !== 2} onClick={() => confirm(true)}>
                {t("join.join")}
              </button>
            </div>
            {/* An approving device too old to know about machines only shows no digits. */}
            <p className="status-note">{t(trusted ? "join.notMeNote" : "join.notMeNoteMachine")}</p>
          </>
        )}
        {state.s === "failed" && (
          <>
            <button className="btn primary" onClick={() => (setState({ s: "asking" }), setAttempt((n) => n + 1))}>
              {t("join.retry")}
            </button>
          </>
        )}
        <button className="link status-cancel" onClick={stop}>
          {t("cancel")}
        </button>
      </div>
    </Frame>
  );
}

/**
 * This computer is no longer one of the owner's trusted devices: another
 * removed it, or the account started over. Or, a machine only, its owner
 * removed it.
 */
export function Removed() {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  return (
    <Frame>
      <div className="status">
        <Icon name="devices" className="ob-big-ic" />
        <h2 className="ob-h">{t("removed.title")}</h2>
        <p className="ob-p">{t(st.status?.machineOnly ? "removed.bodyMachine" : "removed.body")}</p>
        <button
          className="btn primary lg"
          onClick={() =>
            st.run(async () => {
              await local.leave({});
              // Set up again: through the first run, as a computer new to Hermesh.
              setFirstRunDone(false);
              setProgress(null);
            })
          }
        >
          {t("removed.setUp")}
        </button>
      </div>
    </Frame>
  );
}

/** A failure that stops this step: what happened, what to do, and the raw cause behind Details. */
export type Trouble = { title: string; message: string; detail?: string };

/**
 * e said for the person. hermeshd checks the relay before any Touch ID and
 * says "relay unreachable (host): reason" when this network can't reach it.
 */
export function troubleOf(t: TFunction<"firstrun">, e: unknown, title: string): Trouble {
  const text = errorText(e);
  const m = /^relay unreachable \(([^)]+)\): (.*)$/s.exec(text);
  if (m) return { title: t("unreachable.title"), message: t("unreachable.body", { host: m[1] }), detail: m[2] };
  return { title, message: text };
}

export function TroubleSheet({ trouble, close }: { trouble: Trouble; close: () => void }) {
  const { t } = useTranslation("firstrun");
  const [open, setOpen] = useState(false);
  useEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === "Escape" && close();
    window.addEventListener("keydown", esc);
    return () => window.removeEventListener("keydown", esc);
  }, [close]);
  return (
    <>
      <div className="sheet-scrim" />
      <div className="sheet trouble" role="alertdialog" aria-modal="true" aria-labelledby="trouble-title" aria-describedby="trouble-body">
        <h3 id="trouble-title">{trouble.title}</h3>
        <p className="sub" id="trouble-body">{trouble.message}</p>
        {trouble.detail && (
          <>
            <button className="link" aria-expanded={open} onClick={() => setOpen(!open)}>
              {t("details")} <Icon name="chevR" className={`sm chev ${open ? "open" : ""}`} />
            </button>
            {open && <div className="trouble-raw sel-text">{trouble.detail}</div>}
          </>
        )}
        <div className="sheet-foot">
          <button className="btn primary" onClick={close} autoFocus>
            {t("ok")}
          </button>
        </div>
      </div>
    </>
  );
}

function Problem({ text }: { text: string }) {
  return (
    <span className="ob-err" role="alert">
      <Icon name="warn" />
      {text}
    </span>
  );
}
