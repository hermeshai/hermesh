import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { RpcClient, SessionView } from "@hermesh/her-client";
import { Icon } from "../icons";
import { errorText, useNow, useStore } from "../store";
import { cardNow, cardShown, connectBody, filled, type Card, type CardAnswer, type CardStatus } from "./cards";
import { useHer } from "./context";
import { SecretInput } from "./ModelOnboarding";

// A secure card under Her's bubble (docs/her-secure-cards.md §4): a lock, what it asks for ("MCP
// connection") and the environment it is for, drawn by the app alone, as a message's words can't. A `connect` card
// asks to add a connector (or sign in to one again): the owner's values go into the answer and
// leave the page as it is sent; a sign-in then runs here, in the main process. Once it ended, one
// line stays.

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
  const key = `${machineId}/${card.id}`;
  const [mine, setMine] = useState(() => answered.get(key));
  const [sending, setSending] = useState(false);
  const [values, setValues] = useState<Record<string, string>>({});
  const body = connectBody(card);
  const shown = cardShown(cardNow(card, view, mine, now), mine !== undefined);
  const name = body ? named(body.name) : "";

  const signIn = () => {
    if (!body) return;
    signing.set(key, "running");
    (window.hermesh ? window.hermesh.signInHerConnector(machineId, body.name) : Promise.reject(new Error("no bridge")))
      // How it went, her says on the card.
      .catch((e: unknown) => st.show(errorText(e)))
      .finally(() => signing.get(key) === "running" && signing.set(key, "ended"));
  };
  // her waits for the owner to sign in: the browser opens here, once for each approve.
  useEffect(() => {
    if (shown.kind === "signIn" && !signing.has(key)) signIn();
  }, [shown.kind, key]);
  // Nothing typed stays once the form is gone.
  useEffect(() => {
    if (shown.kind !== "form") setValues({});
  }, [shown.kind]);

  const answer = async (a: CardAnswer) => {
    if (!rpc || !session) return st.show(t("env.notConnected", { name: her.machine.machineName }));
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

  if (!body) {
    return (
      <div className="sc-end" role="status">
        <Icon name="lock" className="sm" />
        <span>{t("card.update")}</span>
      </div>
    );
  }

  if (shown.kind === "ended") {
    const line =
      shown.outcome === "done"
        ? shown.tools !== undefined
          ? t("card.doneTools", { name, count: shown.tools })
          : t("card.done", { name })
        : shown.outcome === "failed"
          ? t("card.failed", { name })
          : t(`card.${shown.outcome}`);
    return (
      <div className={`sc-end ${shown.outcome}`} role="status" title={shown.note}>
        <Icon name={shown.outcome === "done" ? "check" : "lock"} className="sm" />
        <span>{line}</span>
      </div>
    );
  }

  const busy = sending || (shown.kind === "form" && shown.busy);
  const ready = body.fields.every((f) => (values[f.id] ?? "").trim() !== "");
  const connect = () => {
    if (busy || !ready) return;
    const typed = Object.fromEntries(body.fields.map((f) => [f.id, values[f.id].trim()]));
    setValues({});
    signing.delete(key);
    void answer({ decision: "approve", ops: filled(card.ops, typed) });
  };
  const cancel = () => {
    if (shown.kind === "signIn") window.hermesh?.cancelHerConnector(machineId, body.name);
    void answer({ decision: "decline" });
  };

  return (
    <div className="scard" role="group" aria-label={t("card.mcp")}>
      <div className="sc-h">
        <Icon name="lock" />
        <span>{t("card.mcp")}</span>
        <span>·</span>
        <span className="sc-env">{her.machine.machineName}</span>
        {card.asked_by === "her" && (
          <span className="sc-who">
            <Icon name="alert" />
            {t("card.notAsked")}
          </span>
        )}
      </div>
      <div className="sc-t">
        <span>{body.reconnect ? t("card.reconnect", { name }) : t("card.connect", { name })}</span>
        <b className="sel-text">{body.host}</b>
      </div>
      <p className="sc-what">{t("card.what")}</p>
      {shown.kind === "signIn" ? (
        <>
          <div className="sc-wait" role="status">
            <span className="mini-spin" />
            {t("card.waiting")}
          </div>
          <div className="sc-f">
            <button className="btn" disabled={sending || !rpc} onClick={cancel}>
              {t("card.cancel")}
            </button>
            <button className="btn" onClick={() => (signing.get(key) === "running" ? window.hermesh?.reopenHerConnector(machineId, body.name) : signIn())}>
              {t("card.reopen")}
            </button>
          </div>
        </>
      ) : (
        <form
          className="sc-form"
          onSubmit={(e) => {
            e.preventDefault();
            connect();
          }}
        >
          {body.fields.map((f) => (
            <SecretInput key={f.id} label={f.label} placeholder={f.label} disabled={busy} value={values[f.id] ?? ""} onChange={(v) => setValues((x) => ({ ...x, [f.id]: v }))} />
          ))}
          {shown.note && (
            <p className="sc-note sel-text" role="alert">
              {shown.note}
            </p>
          )}
          <div className="sc-f">
            <button type="button" className="btn" disabled={busy || !rpc} onClick={cancel}>
              {t("card.deny")}
            </button>
            <button type="submit" className="btn primary" disabled={busy || !ready || !rpc}>
              {shown.busy ? t("card.connecting") : body.reconnect ? t("card.signIn") : t("card.connectButton")}
            </button>
          </div>
        </form>
      )}
    </div>
  );
}
