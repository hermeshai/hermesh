import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Code, ConnectError } from "@connectrpc/connect";
import { parseAppLink, parseConnectAddress, rootPlatform } from "@hermesh/core";
import { Mark } from "@hermesh/ui";
import { local } from "../api";
import { CodeTiles } from "../components";
import { useStore } from "../store";
import { Icon } from "../icons";
import { plainHTTP, savedCenter, TroubleSheet, troubleOf, type Trouble } from "./SignIn";
import "./signin.css";

// Signing in (docs/cold-start-prd.md §5.6.3, §7.5): with the official service in the browser,
// with a server of one's own, or joining a Hermesh one has already. The same pages on the first
// run's sign-in step, from "… needs an account" in the main window, and in Settings › Account. A
// device signed out of its owner signs in again instead (SignIn): the browser on the official
// service, nothing more on a server of one's own. This computer's machine is kept throughout:
// its sessions and Her go on.

export type Way = "official" | "own" | "join";

type Page =
  | { kind: "browser" }
  | { kind: "resuming" }
  | { kind: "own" }
  | { kind: "address" }
  | { kind: "auth"; join?: { owner: string; center: string; relay?: string } ; relay?: string; claim?: string }
  | { kind: "wait"; verification: string }
  | { kind: "digits" };

const bridge = () => window.hermesh!;

export function SignInFlow({ way, close, done }: { way: Way; close: () => void; done: (kind: "official" | "own") => void }) {
  const { t } = useTranslation("firstrun");
  const st = useStore();
  const s = st.status;
  const again = !!s?.initialized && !!s.signedOut;
  const [page, setPage] = useState<Page>(() => (way === "own" ? { kind: "own" } : way === "join" ? { kind: "address" } : again && !s?.hosted ? { kind: "resuming" } : { kind: "browser" }));
  const [center] = useState(savedCenter);
  const [relay, setRelay] = useState("");
  const [claim, setClaim] = useState("");
  const [address, setAddress] = useState("");
  const [digits, setDigits] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // A failure that stops a step: the same sheet as before, its raw cause behind Details.
  const [trouble, setTrouble] = useState<Trouble | null>(null);
  const join = useRef<{ code: string; abort: AbortController } | null>(null);
  const kindOf = (): "official" | "own" => (way === "official" && (s?.hosted ?? true) ? "official" : "own");
  const fail = (e: unknown, title: string) => setTrouble(troubleOf(t, e, title));

  // The browser: opened as the page shows, its link taken as it comes back.
  useEffect(() => {
    if (page.kind !== "browser") return;
    bridge()
      .signIn(center, again ? s?.ownerId : undefined)
      .catch((e: unknown) => fail(e, t("signin.failed")));
    const stop = bridge().onLink((url) => {
      const l = parseAppLink(url);
      if (l?.kind === "link") {
        if (!again) return setPage({ kind: "auth" });
        setBusy(true);
        bridge()
          .completeResume()
          .then(() => st.refresh())
          .then(() => done("official"))
          // The link was just made for this browser's account: it is not the one this Mac belongs to.
          .catch(() => (setBusy(false), setError(t("signin.wrongAccount"))));
      }
      if (l?.kind === "join") setPage({ kind: "auth", join: { owner: l.owner, center } });
    });
    return () => stop();
  }, [page.kind]);
  // A server of one's own: signing in again only checks it.
  useEffect(() => {
    if (page.kind !== "resuming") return;
    local
      .signIn({})
      .then(() => st.refresh())
      .then(() => done("own"))
      .catch((e: unknown) => fail(e, t("signin.failed")));
  }, [page.kind]);
  useEffect(() => () => join.current?.abort.abort(), []);

  const cancel = () => {
    if (page.kind === "browser") void bridge().cancelSignIn();
    if (join.current) local.confirmJoin({ code: join.current.code, accept: false }).catch(() => {});
    join.current?.abort.abort();
    close();
  };

  // Continue on the authorization: one Touch ID.
  const authorize = async (p: Extract<Page, { kind: "auth" }>) => {
    setBusy(true);
    setError("");
    try {
      const name = await bridge().computerName();
      const platform = rootPlatform(bridge().platform);
      if (p.join) return await requestJoin(p.join, name, platform);
      if (p.relay) {
        await bridge().readySigner();
        await local.setup({ relayUrl: p.relay, claimCode: p.claim ?? "", rootLabel: name, rootPlatform: platform, localMachineName: name });
      } else {
        await bridge().completeSignIn({ localMachine: true });
      }
      await st.refresh();
      done(kindOf());
    } catch (e) {
      setBusy(false);
      fail(e, t("authorize.failed"));
    }
  };
  const requestJoin = async (to: { owner: string; center: string; relay?: string }, name: string, platform: string) => {
    void bridge().cancelSignIn();
    const abort = new AbortController();
    const relayUrl = to.relay || (await bridge().connectURL(to.center));
    await bridge().readySigner();
    const res = await local.requestJoin({ relayUrl, ownerId: to.owner, rootLabel: name, rootPlatform: platform, localMachineName: name }, { signal: abort.signal });
    join.current = { code: res.code, abort };
    setBusy(false);
    setPage({ kind: "wait", verification: res.verificationCode });
    try {
      for (;;) {
        const w = await local.waitJoin({ code: res.code }, { signal: abort.signal });
        if (w.done && !w.matchRequired) return await finishJoin(res.code, "");
        if (w.done) return setPage({ kind: "digits" });
        if (w.expired) throw new Error(t("join.expired"));
      }
    } catch (e) {
      if (!abort.signal.aborted) fail(e, t("join.failed"));
    }
  };
  const finishJoin = async (code: string, matchCode: string) => {
    setBusy(true);
    setError("");
    try {
      await local.confirmJoin({ code, accept: true, matchCode });
      join.current = null;
      await st.refresh();
      done(kindOf());
    } catch (e) {
      setBusy(false);
      if (e instanceof ConnectError && e.code === Code.InvalidArgument) setError(t("join.wrongDigits"));
      else fail(e, t("join.failed"));
    }
  };

  const err = error && (
    <p className="si-err" role="alert">
      {error}
    </p>
  );

  const shown = content();
  return (
    <>
      {shown}
      {trouble && <TroubleSheet trouble={trouble} close={() => setTrouble(null)} />}
    </>
  );

  function content() {
  switch (page.kind) {
    case "browser":
    case "resuming":
      return (
        <>
          <div className="si-status">
            <Mark size={52} variant={busy || page.kind === "resuming" ? "wait" : "draw"} delay={200} />
            <h2>{page.kind === "resuming" ? t("signin.resuming") : t("signin.browserTitle")}</h2>
            {page.kind === "browser" && <p>{t("signin.browserBody")}</p>}
            {err}
          </div>
          <div className="si-foot">
            <button className="btn" onClick={cancel}>
              {t("cancel")}
            </button>
            {page.kind === "browser" && (
              <button className="link push" onClick={() => void bridge().signIn(center, again ? s?.ownerId : undefined)}>
                {t("browser.reopen")}
              </button>
            )}
          </div>
        </>
      );
    case "own":
      return (
        <>
          <div className="si-head">
            <b>{t("signin.ownTitle")}</b>
          </div>
          <div className="si-body">
            <div className="si-grp">
              <label className="si-row">
                <span className="t">{t("signin.ownAddress")}</span>
                <input className="field" value={relay} placeholder="relay.example.com" spellCheck={false} autoFocus onChange={(e) => setRelay(e.target.value)} />
              </label>
              <label className="si-row">
                <span className="t">{t("signin.ownClaim")}</span>
                <input className="field mono" value={claim} spellCheck={false} onChange={(e) => setClaim(e.target.value)} />
              </label>
            </div>
            <p className="si-note">{t("signin.ownNote")}</p>
            {plainHTTP(relay) && (
              <p className="si-warn" role="note">
                <Icon name="warn" />
                {t("selfHosted.plain")}
              </p>
            )}
            {err}
          </div>
          <div className="si-foot">
            <button className="link" onClick={() => (setError(""), setPage({ kind: "address" }))}>
              {t("signin.ownJoin")}
            </button>
            <button className="btn push" onClick={cancel}>
              {t("cancel")}
            </button>
            <button className="btn primary" disabled={!relay.trim() || !claim.trim()} onClick={() => (setError(""), setPage({ kind: "auth", relay: relay.trim(), claim: claim.trim() }))}>
              {t("continue")}
            </button>
          </div>
        </>
      );
    case "address":
      return (
        <>
          <div className="si-head">
            <b>{t("signin.joinTitle")}</b>
          </div>
          <div className="si-body">
            <div className="si-grp">
              <label className="si-row">
                <span className="t">{t("signin.joinAddress")}</span>
                <input className="field mono" value={address} spellCheck={false} autoFocus onChange={(e) => (setAddress(e.target.value), setError(""))} />
              </label>
            </div>
            <p className="si-note">{t("signin.joinNote")}</p>
            {err}
          </div>
          <div className="si-foot">
            <button className="btn push" onClick={cancel}>
              {t("cancel")}
            </button>
            <button
              className="btn primary"
              disabled={!address.trim()}
              onClick={() => {
                const a = parseConnectAddress(address);
                if (!a) return setError(t("joinAddress.invalid"));
                setPage({ kind: "auth", join: { owner: a.owner, center: a.relay, relay: a.relay } });
              }}
            >
              {t("continue")}
            </button>
          </div>
        </>
      );
    case "auth":
      return (
        <>
          <div className="si-head">
            <b>{page.join ? t("signin.authJoinTitle") : t("signin.authTitle")}</b>
            <span>{page.join ? t("signin.authJoinBody") : t("signin.authBody")}</span>
            {err}
          </div>
          <div className="si-foot">
            <button className="btn push" disabled={busy} onClick={cancel}>
              {t("cancel")}
            </button>
            <button className="btn primary" disabled={busy} onClick={() => void authorize(page)}>
              {busy ? t("authorize.settingUp") : t("continue")}
            </button>
          </div>
        </>
      );
    case "wait":
      return (
        <>
          <div className="si-status">
            <Mark size={52} variant="wait" />
            <h2>{t("join.titleApprove")}</h2>
            <CodeTiles code={page.verification} hint={t("join.codeHint")} />
            {err}
          </div>
          <div className="si-foot">
            <button className="btn" onClick={cancel}>
              {t("cancel")}
            </button>
          </div>
        </>
      );
    case "digits":
      return (
        <>
          <div className="si-status">
            <h2>{t("join.titleDigits")}</h2>
            <input
              className="si-digits"
              aria-label={t("join.digits")}
              inputMode="numeric"
              placeholder="00"
              value={digits}
              autoFocus
              onChange={(e) => (setDigits(e.target.value.replace(/\D/g, "").slice(0, 2)), setError(""))}
              onKeyDown={(e) => e.key === "Enter" && digits.length === 2 && join.current && void finishJoin(join.current.code, digits)}
            />
            {err}
          </div>
          <div className="si-foot">
            <button className="btn push" disabled={busy} onClick={cancel}>
              {t("join.notMe")}
            </button>
            <button className="btn primary" disabled={busy || digits.length !== 2} onClick={() => join.current && void finishJoin(join.current.code, digits)}>
              {t("join.join")}
            </button>
          </div>
        </>
      );
  }
  }
}
