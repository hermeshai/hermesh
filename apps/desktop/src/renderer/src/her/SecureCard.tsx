import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { RpcClient, SessionView } from "@hermesh/her-client";
import { Icon } from "../icons";
import { errorText, useNow, useStore } from "../store";
import { cardNow, cardShown, connectBody, filled, isChromeTakeover, isKeyRotate, siteLoginBody, type Card, type CardAnswer, type CardStatus, type ConnectBody, type SiteLoginBody } from "./cards";
import { useHer } from "./context";
import { SecretInput } from "./ModelOnboarding";

// A secure card under Her's bubble (docs/her-secure-cards.md §4): a lock, what it asks for ("MCP
// connection" or "Website Sign-In") and the environment it is for, drawn by the app alone, as a
// message's words can't. Values go directly into cards/answer over the live connection (never
// queued offline, never sent to the model) and leave component state as soon as sent or cleared.

/** What her answered this device, by environment and card: how a card answered here stands while the app runs, its conversation opened again too. */
const answered = new Map<string, CardStatus>();

/** The sign-in this device runs for a card, until it ended; it starts once for each approve. */
const signing = new Map<string, "running" | "ended">();

/** cards/answer, until her-protocol's generated types have it. */
type Answering = { request(method: "cards/answer", params: { session_id: string; card_id: string; answer: CardAnswer }): Promise<CardStatus> };

/** A name as the card says it: the connector's, its first letter capital ("notion": Notion). */
const named = (name: string) => name.charAt(0).toUpperCase() + name.slice(1);

export function SecureCard({ card, view, rpc, session }: { card: Card; view: SessionView; rpc: RpcClient | null; session: string | null }) {
  const { t } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const now = useNow(60_000).getTime();
  const machineId = her.machine.machineId;
  const machineName = her.machine.machineName;
  const key = `${machineId}/${card.id}`;
  const [mine, setMine] = useState(() => answered.get(key));
  const [sending, setSending] = useState(false);
  const [values, setValues] = useState<Record<string, string>>({});
  const connect = connectBody(card);
  const siteLogin = siteLoginBody(card);
  const shown = cardShown(cardNow(card, view, mine, now), mine !== undefined);
  const name = connect ? named(connect.name) : "";
  const online = rpc !== null && session !== null;

  const signIn = () => {
    if (!connect) return;
    signing.set(key, "running");
    (window.hermesh ? window.hermesh.signInHerConnector(machineId, connect.name) : Promise.reject(new Error("no bridge")))
      // How it went, her says on the card.
      .catch((e: unknown) => st.show(errorText(e)))
      .finally(() => signing.get(key) === "running" && signing.set(key, "ended"));
  };
  // her waits for the owner to sign in: the browser opens here, once for each approve.
  useEffect(() => {
    if (connect && shown.kind === "signIn" && !signing.has(key)) signIn();
  }, [connect, shown.kind, key]);
  // Nothing typed stays once the form is gone or the connection drops.
  useEffect(() => {
    if (shown.kind !== "form" || !online) setValues({});
  }, [shown.kind, online]);
  useEffect(() => () => setValues({}), []);

  const answer = async (a: CardAnswer) => {
    if (!rpc || !session) {
      setValues({});
      return st.show(t("card.offlineNoQueue", { name: machineName }));
    }
    setSending(true);
    try {
      const r = await (rpc as unknown as Answering).request("cards/answer", { session_id: session, card_id: card.id, answer: a });
      answered.set(key, r);
      setMine(r);
    } catch (e) {
      st.show(errorText(e));
    } finally {
      setSending(false);
    }
  };

  if (!connect && !siteLogin) {
    return (
      <div className="sc-end" role="status">
        <Icon name="lock" className="sm" />
        <span>{t("card.update")}</span>
      </div>
    );
  }

  if (shown.kind === "ended") {
    if (siteLogin) {
      const takeover = isChromeTakeover(siteLogin);
      const line =
        shown.outcome === "done"
          ? takeover
            ? t("card.chromeTakeoverDone", { host: siteLogin.host })
            : siteLogin.browser
              ? t("card.siteLoginDoneBrowser", { host: siteLogin.host, browser: siteLogin.browser })
              : t("card.siteLoginDone", { host: siteLogin.host })
          : shown.outcome === "failed"
            ? shown.note || t(takeover ? "card.chromeTakeoverFailed" : "card.siteLoginFailed", { host: siteLogin.host })
            : shown.outcome === "declined" && takeover
              ? t("card.chromeTakeoverDeclined")
              : t(`card.${shown.outcome}`);
      return (
        <div className={`sc-end ${shown.outcome}`} role="status" title={shown.note}>
          <Icon name={shown.outcome === "done" ? "check" : "lock"} className="sm" />
          <span>{line}</span>
        </div>
      );
    }
    const rotate = connect ? isKeyRotate(connect) : false;
    const line =
      shown.outcome === "done"
        ? shown.tools !== undefined
          ? t(rotate ? "card.rotatedTools" : "card.doneTools", { name, count: shown.tools })
          : t(rotate ? "card.rotated" : "card.done", { name })
        : shown.outcome === "failed"
          ? t("card.failed", { name })
          : t(`card.${shown.outcome}`);
    return (
      <div className={`sc-end ${shown.outcome}`} role="status" title={shown.note}>
        <Icon name={shown.outcome === "done" ? "check" : "lock"} className="sm" />
        <span>{line}</span>
        {/* Connected: its page in the console is a click away. */}
        {shown.outcome === "done" && connect && (
          <button className="btn sm" onClick={() => her.openConsole(`connectors/${encodeURIComponent(connect.name)}`)}>
            {t("card.manage")}
          </button>
        )}
      </div>
    );
  }

  if (siteLogin) {
    if (isChromeTakeover(siteLogin)) {
      return (
        <ChromeTakeoverCard
          card={card}
          body={siteLogin}
          machineName={machineName}
          shown={shown}
          online={online}
          sending={sending}
          onStart={() => void answer({ decision: "takeover_start" })}
          onDone={() => void answer({ decision: "takeover_done" })}
          onDecline={() => void answer({ decision: "decline" })}
        />
      );
    }
    return (
      <SiteLoginForm
        card={card}
        body={siteLogin}
        machineName={machineName}
        shown={shown}
        online={online}
        sending={sending}
        values={values}
        setValues={setValues}
        onFill={(typed) => {
          setValues({});
          void answer({ decision: "fill", fields: typed });
        }}
        onDecline={() => {
          setValues({});
          void answer({ decision: "decline" });
        }}
      />
    );
  }

  return (
    <ConnectForm
      card={card}
      body={connect as ConnectBody}
      name={name}
      machineId={machineId}
      machineName={machineName}
      keyId={key}
      shown={shown}
      online={online}
      sending={sending}
      values={values}
      setValues={setValues}
      onApprove={(typed) => {
        setValues({});
        signing.delete(key);
        void answer({ decision: "approve", ops: filled(card.ops, typed) });
      }}
      onCancel={(signingIn) => {
        setValues({});
        if (signingIn) window.hermesh?.cancelHerConnector(machineId, (connect as ConnectBody).name);
        void answer({ decision: "decline" });
      }}
      onReopen={() => (signing.get(key) === "running" ? window.hermesh?.reopenHerConnector(machineId, (connect as ConnectBody).name) : signIn())}
    />
  );
}

function ConnectForm({
  card,
  body,
  name,
  machineName,
  shown,
  online,
  sending,
  values,
  setValues,
  onApprove,
  onCancel,
  onReopen,
}: {
  card: Card;
  body: ConnectBody;
  name: string;
  machineId: string;
  machineName: string;
  keyId: string;
  shown: Extract<ReturnType<typeof cardShown>, { kind: "form" | "signIn" }>;
  online: boolean;
  sending: boolean;
  values: Record<string, string>;
  setValues: React.Dispatch<React.SetStateAction<Record<string, string>>>;
  onApprove: (typed: Record<string, string>) => void;
  onCancel: (signingIn: boolean) => void;
  onReopen: () => void;
}) {
  const { t } = useTranslation("her");
  const busy = sending || (shown.kind === "form" && shown.busy);
  const rotate = isKeyRotate(body);
  const ready = body.fields.every((f) => (values[f.id] ?? "").trim() !== "");
  const submit = () => {
    if (busy || !ready || !online) return;
    onApprove(Object.fromEntries(body.fields.map((f) => [f.id, values[f.id].trim()])));
  };

  return (
    <div className="scard" role="group" aria-label={t("card.mcp")}>
      <div className="sc-h">
        <Icon name="lock" />
        <span>{t("card.mcp")}</span>
        <span>·</span>
        <span className="sc-env">{machineName}</span>
        {card.asked_by === "her" && (
          <span className="sc-who">
            <Icon name="alert" />
            {t("card.notAsked")}
          </span>
        )}
      </div>
      <div className="sc-t">
        <span>{rotate ? t("card.rotate", { name }) : body.reconnect ? t("card.reconnect", { name }) : t("card.connect", { name })}</span>
        <b className="sel-text">{body.host}</b>
        {body.url && <span className="sc-url sel-text">{body.url}</span>}
      </div>
      <p className="sc-what">{body.fields.length > 0 ? t("card.secretBoundary", { machine: machineName }) : t("card.what")}</p>
      {shown.kind === "signIn" ? (
        <>
          <div className="sc-wait" role="status">
            <span className="mini-spin" />
            {t("card.waiting")}
          </div>
          <div className="sc-f">
            <button className="btn" disabled={sending || !online} onClick={() => onCancel(true)}>
              {t("card.cancel")}
            </button>
            <button className="btn" onClick={onReopen}>
              {t("card.reopen")}
            </button>
          </div>
        </>
      ) : (
        <form
          className="sc-form"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          {body.fields.map((f) => (
            <SecretInput key={f.id} label={f.label} placeholder={f.label} disabled={busy || !online} value={values[f.id] ?? ""} onChange={(v) => setValues((x) => ({ ...x, [f.id]: v }))} />
          ))}
          {!online && (
            <p className="sc-note" role="status">
              {t("card.offlineNoQueue", { name: machineName })}
            </p>
          )}
          {shown.note && (
            <p className="sc-note sel-text" role="alert">
              {shown.note}
            </p>
          )}
          <div className="sc-f">
            <button type="button" className="btn" disabled={busy || !online} onClick={() => onCancel(false)}>
              {t("card.deny")}
            </button>
            <button type="submit" className="btn primary" disabled={busy || !ready || !online}>
              {shown.busy ? t("card.connecting") : rotate ? t("card.updateKeyButton") : body.reconnect ? t("card.signIn") : t("card.connectButton")}
            </button>
          </div>
        </form>
      )}
    </div>
  );
}

function SiteLoginForm({
  card,
  body,
  machineName,
  shown,
  online,
  sending,
  values,
  setValues,
  onFill,
  onDecline,
}: {
  card: Card;
  body: SiteLoginBody;
  machineName: string;
  shown: Extract<ReturnType<typeof cardShown>, { kind: "form" | "signIn" }>;
  online: boolean;
  sending: boolean;
  values: Record<string, string>;
  setValues: React.Dispatch<React.SetStateAction<Record<string, string>>>;
  onFill: (typed: Record<string, string>) => void;
  onDecline: () => void;
}) {
  const { t } = useTranslation("her");
  const busy = sending || (shown.kind === "form" && shown.busy);
  const ready = body.fields.every((f) => (values[f.id] ?? "").trim() !== "");
  const submit = () => {
    if (busy || !ready || !online) return;
    onFill(Object.fromEntries(body.fields.map((f) => [f.id, values[f.id].trim()])));
  };

  return (
    <div className="scard" role="group" aria-label={t("card.siteLogin")}>
      <div className="sc-h">
        <Icon name="lock" />
        <span>{t("card.siteLogin")}</span>
        <span>·</span>
        <span className="sc-env">{machineName}</span>
        {body.browser && (
          <>
            <span>·</span>
            <span className="sc-env">{body.tab ? `${body.browser} (${body.tab})` : body.browser}</span>
          </>
        )}
        {card.asked_by === "her" && (
          <span className="sc-who">
            <Icon name="alert" />
            {t("card.notAsked")}
          </span>
        )}
      </div>
      <div className="sc-t">
        <span>{t("card.siteLoginTitle")}</span>
        <b className="sel-text">{body.origin}</b>
        {body.title && <span className="sc-url sel-text">{body.title}</span>}
      </div>
      <p className="sc-what">
        {body.browser
          ? t("card.siteLoginWhatBrowser", { machine: machineName, browser: body.browser, origin: body.origin })
          : t("card.siteLoginWhat", { machine: machineName, origin: body.origin })}
      </p>
      <form
        className="sc-form"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {body.fields.map((f) =>
          f.secret ? (
            <SecretInput key={f.id} label={f.label} placeholder={f.label} disabled={busy || !online} value={values[f.id] ?? ""} onChange={(v) => setValues((x) => ({ ...x, [f.id]: v }))} />
          ) : (
            <input
              key={f.id}
              className="field"
              type={f.kind === "email" ? "email" : "text"}
              autoComplete="off"
              spellCheck={false}
              disabled={busy || !online}
              aria-label={f.label}
              placeholder={f.label}
              value={values[f.id] ?? ""}
              onChange={(e) => setValues((x) => ({ ...x, [f.id]: e.target.value }))}
            />
          ),
        )}
        {!online && (
          <p className="sc-note" role="status">
            {t("card.offlineNoQueue", { name: machineName })}
          </p>
        )}
        {shown.kind === "form" && shown.note && (
          <p className="sc-note sel-text" role="alert">
            {shown.note}
          </p>
        )}
        <div className="sc-f">
          <button type="button" className="btn" disabled={busy || !online} onClick={onDecline}>
            {t("card.skipTakeover")}
          </button>
          <button type="submit" className="btn primary" disabled={busy || !ready || !online}>
            {busy ? t("card.filling") : t("card.fillButton")}
          </button>
        </div>
      </form>
    </div>
  );
}

function ChromeTakeoverCard({
  card,
  body,
  machineName,
  shown,
  online,
  sending,
  onStart,
  onDone,
  onDecline,
}: {
  card: Card;
  body: SiteLoginBody;
  machineName: string;
  shown: Extract<ReturnType<typeof cardShown>, { kind: "form" | "signIn" }>;
  online: boolean;
  sending: boolean;
  onStart: () => void;
  onDone: () => void;
  onDecline: () => void;
}) {
  const { t } = useTranslation("her");
  const busy = sending || (shown.kind === "form" && shown.busy);
  const tabLabel = [
    body.tab ? t("card.chromeTargetTab", { tab: body.tab }) : "",
    body.title ?? "",
    body.browser ?? "",
  ]
    .filter(Boolean)
    .join(" · ");

  return (
    <div className="scard" role="group" aria-label={t("card.chromeTakeover")}>
      <div className="sc-h">
        <Icon name="lock" />
        <span>{t("card.chromeTakeover")}</span>
        <span>·</span>
        <span className="sc-env">{machineName}</span>
        {card.asked_by === "her" && (
          <span className="sc-who">
            <Icon name="alert" />
            {t("card.notAsked")}
          </span>
        )}
      </div>
      <div className="sc-t">
        <span>{t("card.chromeTakeoverTitle")}</span>
        <b className="sel-text">{body.origin}</b>
        {tabLabel && <span className="sc-url sel-text">{tabLabel}</span>}
      </div>
      <p className="sc-what">{t("card.chromeTakeoverWhat", { machine: machineName })}</p>
      {!online && (
        <p className="sc-note" role="status">
          {t("card.offlineNoQueue", { name: machineName })}
        </p>
      )}
      {shown.kind === "signIn" ? (
        <>
          <div className="sc-wait" role="status">
            <span className="mini-spin" />
            {t("card.chromeTakeoverWaiting", { origin: body.origin, tab: body.tab ?? "" })}
          </div>
          <div className="sc-f">
            <button type="button" className="btn" disabled={busy || !online} onClick={onDecline}>
              {t("card.cancel")}
            </button>
            <button type="button" className="btn" disabled={busy || !online} onClick={onStart}>
              {t("card.chromeTakeoverReopen")}
            </button>
            <button type="button" className="btn primary" disabled={busy || !online} onClick={onDone}>
              {t("card.chromeTakeoverDoneButton")}
            </button>
          </div>
        </>
      ) : (
        <>
          {shown.note && (
            <p className="sc-note sel-text" role="alert">
              {shown.note}
            </p>
          )}
          <div className="sc-f">
            <button type="button" className="btn" disabled={busy || !online} onClick={onDecline}>
              {t("card.chromeTakeoverCancelButton")}
            </button>
            <button type="button" className="btn primary" disabled={busy || !online} onClick={onStart}>
              {t("card.chromeTakeoverOpenButton")}
            </button>
          </div>
        </>
      )}
    </div>
  );
}
