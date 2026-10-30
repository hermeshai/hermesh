import { useEffect, useRef, useState } from "react";
import type { TFunction } from "i18next";
import { Trans, useTranslation } from "react-i18next";
import { Code, ConnectError } from "@connectrpc/connect";
import { officialCenter, parseAppLink, parseConnectAddress, rootPlatform } from "@hermesh/core";
import { Mark } from "@hermesh/ui";
import { local } from "../api";
import { CodeTiles } from "../components";
import { Icon } from "../icons";
import { errorText, save, useStore } from "../store";
import { i18n } from "../../../shared/i18n";
import { Frame, StepIcon } from "./Frame";
import { setProgress } from "./progress";

// Before this computer belongs to an owner: the welcome, the browser
// sign-in, a self-hosted relay or joining an owner, and the authorization
// page before its first Touch ID (docs/trusted-devices.md 4.1–4.2).

/**
 * What this computer is about to become, shown on the authorization page;
 * nothing happens before Continue. typed: the person typed the owner's
 * connection address (not handed over by the center).
 */
type How = { kind: "signIn" } | { kind: "selfHosted"; relay: string; claim: string } | { kind: "join"; owner: string; center: string; typed: boolean };

type Page =
  | { kind: "welcome" }
  | { kind: "browser" }
  | { kind: "selfHosted" }
  | { kind: "joinAddress" }
  | { kind: "authorize"; how: How }
  | { kind: "join"; owner: string; center: string; machine: boolean; trusted: boolean; typed: boolean };

const bridge = () => window.hermesh!;

// The center to sign in to: the official one (development and tests may set
// localStorage centerURL to another).
function savedCenter(): string {
  try {
    return localStorage.getItem("centerURL") || officialCenter;
  } catch {
    return officialCenter;
  }
}

function hostOf(center: string): string {
  try {
    return new URL(center).host;
  } catch {
    return center;
  }
}

export function SignIn() {
  const [page, setPage] = useState<Page>({ kind: "welcome" });
  const [center] = useState(savedCenter);
  const [relay, setRelay] = useState("");
  const [claim, setClaim] = useState("");
  const centerRef = useRef(center);
  centerRef.current = center;

  useEffect(() => {
    let live = true;
    // A sign-in started before the app was restarted (the browser may bring the link any moment).
    bridge()
      .signInPending()
      .then((p) => live && p && setPage((x) => (x.kind === "welcome" ? { kind: "browser" } : x)));
    const stop = bridge().onLink((url) => {
      const l = parseAppLink(url);
      if (l?.kind === "link") setPage({ kind: "authorize", how: { kind: "signIn" } });
      if (l?.kind === "join") setPage({ kind: "authorize", how: { kind: "join", owner: l.owner, center: centerRef.current, typed: false } });
    });
    return () => {
      live = false;
      stop();
    };
  }, []);

  const welcome = () => setPage({ kind: "welcome" });
  switch (page.kind) {
    case "welcome":
      return <Welcome center={center} signedIn={() => setPage({ kind: "browser" })} selfHosted={() => setPage({ kind: "selfHosted" })} />;
    case "browser":
      return <Browser center={center} cancel={() => (bridge().cancelSignIn(), welcome())} />;
    case "selfHosted":
      return (
        <SelfHosted
          relay={relay}
          claim={claim}
          set={(r, c) => (setRelay(r), setClaim(c))}
          back={welcome}
          next={() => setPage({ kind: "authorize", how: { kind: "selfHosted", relay: relay.trim(), claim: claim.trim() } })}
          join={() => setPage({ kind: "joinAddress" })}
        />
      );
    case "joinAddress":
      return <JoinAddress back={() => setPage({ kind: "selfHosted" })} next={(owner, at) => setPage({ kind: "authorize", how: { kind: "join", owner, center: at, typed: true } })} />;
    case "authorize":
      return (
        <Authorize
          how={page.how}
          cancel={() => {
            if (page.how.kind === "signIn") bridge().cancelSignIn();
            setPage(page.how.kind === "selfHosted" ? { kind: "selfHosted" } : { kind: "welcome" });
          }}
          join={(machine, trusted) =>
            page.how.kind === "join" && setPage({ kind: "join", owner: page.how.owner, center: page.how.center, machine, trusted, typed: page.how.typed })
          }
        />
      );
    case "join":
      return <Join owner={page.owner} center={page.center} machine={page.machine} trusted={page.trusted} typed={page.typed} cancel={welcome} />;
  }
}

function Welcome({ center, signedIn, selfHosted }: { center: string; signedIn: () => void; selfHosted: () => void }) {
  const { t } = useTranslation("firstrun");
  const [error, setError] = useState("");
  const start = async () => {
    setError("");
    try {
      await bridge().signIn(center);
      signedIn();
    } catch (e) {
      setError(errorText(e));
    }
  };
  return (
    <Frame>
      <ErasedNote />
      <div className="hero">
        <div className="mk">
          <div className="glow" />
          <Mark size={96} variant="reveal" />
        </div>
        <h1>{t("welcome.title")}</h1>
        <p className="lede">
          <Trans t={t} i18nKey="welcome.lede" components={{ br: <br /> }} />
        </p>
        <button className="btn primary lg cta" onClick={start}>
          {t("welcome.start")}
        </button>
        {error && <Problem text={error} />}
      </div>
      <div className="center-line">
        <button className="link" onClick={() => bridge().openExternal(center)}>
          {hostOf(center)}
        </button>
        <span className="faint">·</span>
        <button className="link" onClick={selfHosted}>
          {t("welcome.selfHosted")}
        </button>
      </div>
      <p className="promise">
        <Icon name="shield" />
        {t("welcome.promise")}
      </p>
    </Frame>
  );
}

/**
 * Once, after this Mac erased everything (or was erased from another device):
 * what was done, who erases next time it connects, and the worktrees kept.
 */
function ErasedNote() {
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

function Browser({ center, cancel }: { center: string; cancel: () => void }) {
  const { t } = useTranslation("firstrun");
  const [error, setError] = useState("");
  return (
    <Frame>
      <div className="status">
        <Mark size={64} variant="wait" />
        <h2 className="ob-h">{t("browser.title")}</h2>
        <p className="ob-p">{t("browser.body")}</p>
        {error && <Problem text={error} />}
        <div className="status-actions">
          <button className="btn" onClick={() => bridge().signIn(center).catch((e) => setError(errorText(e)))}>
            {t("browser.reopen")}
          </button>
          <button className="btn" onClick={cancel}>
            {t("cancel")}
          </button>
        </div>
      </div>
    </Frame>
  );
}

/** A relay of one's own (hermesh-relay): its address and claim code. */
function SelfHosted(p: { relay: string; claim: string; set: (relay: string, claim: string) => void; back: () => void; next: () => void; join: () => void }) {
  const { t } = useTranslation("firstrun");
  const ok = p.relay.trim() !== "" && p.claim.trim() !== "";
  return (
    <Frame
      foot={
        <>
          <span className="note" />
          <button className="btn" onClick={p.back}>
            {t("back")}
          </button>
          <button className="btn primary" disabled={!ok} onClick={p.next}>
            {t("continue")}
          </button>
        </>
      }
    >
      <StepIcon name="server" />
      <h2 className="ob-h">{t("selfHosted.title")}</h2>
      <p className="ob-p">{t("selfHosted.body")}</p>
      <form className="fgroup ob-form" onSubmit={(e) => (e.preventDefault(), ok && p.next())}>
        <div className="frow">
          <label htmlFor="relay">{t("selfHosted.address")}</label>
          <span className="fv">
            <input id="relay" className="field" placeholder="relay.example.com" value={p.relay} onChange={(e) => p.set(e.target.value, p.claim)} autoFocus />
          </span>
        </div>
        <div className="frow">
          <label htmlFor="claim">{t("selfHosted.claim")}</label>
          <span className="fv">
            <input id="claim" className="field mono" placeholder="XXXX-XXXX-XXXX-XXXX" value={p.claim} onChange={(e) => p.set(p.relay, e.target.value)} />
          </span>
        </div>
        <button type="submit" hidden />
      </form>
      <p className="ob-alt">
        <Trans t={t} i18nKey="selfHosted.join" components={{ join: <button className="link" onClick={p.join} /> }} />
      </p>
    </Frame>
  );
}

/** Another device of an owner: the owner's connection address, then the same join as from the console. */
function JoinAddress({ back, next }: { back: () => void; next: (owner: string, relay: string) => void }) {
  const { t } = useTranslation("firstrun");
  const [address, setAddress] = useState("");
  const [error, setError] = useState("");
  const go = () => {
    const a = parseConnectAddress(address);
    if (!a) return setError(t("joinAddress.invalid"));
    next(a.owner, a.relay);
  };
  return (
    <Frame
      foot={
        <>
          {error ? <Problem text={error} /> : <span className="note" />}
          <button className="btn" onClick={back}>
            {t("back")}
          </button>
          <button className="btn primary" disabled={address.trim() === ""} onClick={go}>
            {t("continue")}
          </button>
        </>
      }
    >
      <StepIcon name="devices" />
      <h2 className="ob-h">{t("joinAddress.title")}</h2>
      <p className="ob-p">{t("joinAddress.body")}</p>
      <form className="fgroup ob-form" onSubmit={(e) => (e.preventDefault(), address.trim() && go())}>
        <div className="frow">
          <label htmlFor="address">{t("joinAddress.address")}</label>
          <span className="fv">
            <input id="address" className="field mono" placeholder="relay.example.com/o_xxxxxxxx" value={address} onChange={(e) => (setAddress(e.target.value), setError(""))} autoFocus />
          </span>
        </div>
      </form>
    </Frame>
  );
}

/**
 * Before this computer's first Touch ID: what it will do, row by row.
 * Working here and reminders are on unless the person turns them off.
 */
function Authorize({ how, cancel, join }: { how: How; cancel: () => void; join: (machine: boolean, trusted: boolean) => void }) {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  // Joining, this Mac may be a machine only (a work Mac): then sharing its agents is what it is for.
  const [trusted, setTrusted] = useState(true);
  const [share, setShare] = useState(true);
  const machine = share || !trusted;
  const [remind, setRemind] = useState(true);
  const [busy, setBusy] = useState(false);
  const [trouble, setTrouble] = useState<Trouble | null>(null);
  const joining = how.kind === "join";

  const go = async () => {
    // Reminders: start with the login session (in the menu bar) and notify.
    for (const k of ["openAtLogin", "notifyQuestion", "notifyFailed"]) save(k, remind);
    (window.hermesh as { setLoginItem?: (on: boolean) => void } | undefined)?.setLoginItem?.(remind);
    if (how.kind === "join") return join(machine, trusted);
    setBusy(true);
    // The pages after this one, before hermeshd says it is set up (so the main window never flashes).
    setProgress(machine ? { step: "prepare" } : { step: "agent" });
    try {
      if (how.kind === "signIn") {
        await bridge().completeSignIn({ localMachine: machine });
      } else {
        const name = await bridge().computerName();
        await local.setup({ relayUrl: how.relay, claimCode: how.claim, rootLabel: name, rootPlatform: rootPlatform(bridge().platform), localMachineName: machine ? name : "" });
      }
      await st.refresh();
    } catch (e) {
      setProgress(null);
      setTrouble(troubleOf(t, e, t("authorize.failed")));
      setBusy(false);
    }
  };

  return (
    <Frame
      sheet={trouble && <TroubleSheet trouble={trouble} close={() => setTrouble(null)} />}
      foot={
        <>
          <span className="note">{joining ? t(trusted ? "authorize.joinNote" : "authorize.machineOnlyNote") : ""}</span>
          <button className="btn" onClick={cancel} disabled={busy}>
            {t("cancel")}
          </button>
          <button className="btn primary" onClick={go} disabled={busy}>
            {busy ? t("authorize.settingUp") : t("continue")}
          </button>
        </>
      }
    >
      <StepIcon name="key" />
      <h2 className="ob-h">{joining ? t("authorize.titleJoin") : t("authorize.titleTrust")}</h2>
      <p className="ob-p">{t("authorize.body")}</p>
      <div className="perm">
        <div className="prow">
          <span className="pi">
            <Icon name="key" />
          </span>
          <div className="pt">
            <b>{t("authorize.trusted")}</b>
            <span>{t(trusted ? "authorize.trustedBody" : "authorize.trustedOff")}</span>
          </div>
          {joining ? <Switch on={trusted} set={setTrusted} label={t("authorize.trusted")} disabled={busy} /> : <span className="req">{t("authorize.required")}</span>}
        </div>
        <div className="prow">
          <span className="pi">
            <Icon name="terminal" />
          </span>
          <div className="pt">
            <b>{t("authorize.share")}</b>
            <span>{t(trusted ? "authorize.shareBody" : "authorize.shareBodyMachine")}</span>
          </div>
          {trusted ? <Switch on={share} set={setShare} label={t("authorize.share")} disabled={busy} /> : <span className="req">{t("authorize.required")}</span>}
        </div>
        <div className="prow">
          <span className="pi">
            <Icon name="bell" />
          </span>
          <div className="pt">
            <b>{t("authorize.remind")}</b>
            <span>{t(trusted ? "authorize.remindBody" : "authorize.remindBodyMachine")}</span>
          </div>
          <Switch on={remind} set={setRemind} label={t("authorize.remind")} disabled={busy} />
        </div>
      </div>
    </Frame>
  );
}

function Switch({ on, set, label, disabled }: { on: boolean; set: (on: boolean) => void; label: string; disabled?: boolean }) {
  return <button className={`sw ${on ? "on" : ""}`} role="switch" aria-checked={on} aria-label={label} disabled={disabled} onClick={() => set(!on)} />;
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
    setProgress(trusted && machine ? { step: "prepare", joined: true } : null);
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
type Trouble = { title: string; message: string; detail?: string };

/**
 * e said for the person. hermeshd checks the relay before any Touch ID and
 * says "relay unreachable (host): reason" when this network can't reach it.
 */
function troubleOf(t: TFunction<"firstrun">, e: unknown, title: string): Trouble {
  const text = errorText(e);
  const m = /^relay unreachable \(([^)]+)\): (.*)$/s.exec(text);
  if (m) return { title: t("unreachable.title"), message: t("unreachable.body", { host: m[1] }), detail: m[2] };
  return { title, message: text };
}

function TroubleSheet({ trouble, close }: { trouble: Trouble; close: () => void }) {
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
