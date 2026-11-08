import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ChatGptStatus } from "@hermesh/her-protocol";
import { errorText } from "../format";
import { useHer } from "../her";
import { openPage, signInRemotely } from "../signin";
import { ManageModels, useModelCounts } from "./ManageModels";
import { Brand } from "./providers";

/**
 * The ChatGPT subscription. The sign-in ends on the
 * owner's device (docs/her-server.md §5.4): the browser comes back to a catcher there, and
 * what it brought goes to her, which keeps the tokens on the Her Server.
 */
export function ChatGptConnection() {
  const { t } = useTranslation("settings");
  const her = useHer();
  const { rpc, call } = her;
  const [status, setStatus] = useState<ChatGptStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [managing, setManaging] = useState(false);
  /** A sign-in this page waits for, and its authorization page once her made it. */
  const [waiting, setWaiting] = useState<{ url: string | null } | null>(null);
  const stop = useRef<AbortController | null>(null);
  const alive = useRef(true);
  const connection = status?.connection;
  const pending = status?.state === "pending";
  const ready = connection?.state === "ready";
  const { count, shown } = useModelCounts(connection?.provider_id);

  const refresh = useCallback(async () => {
    try {
      const next = await call("chatgpt/status", {});
      if (alive.current) setStatus(next);
    } catch (e) {
      if (alive.current) setError(errorText(e));
    }
  }, [call]);
  useEffect(() => {
    alive.current = true;
    if (rpc) void refresh();
    const off = rpc?.on("models/changed", () => void refresh());
    return () => {
      alive.current = false;
      off?.();
    };
  }, [rpc, refresh]);
  useEffect(() => () => stop.current?.abort(), []);

  const signIn = async () => {
    const abort = new AbortController();
    stop.current = abort;
    setError("");
    setWaiting({ url: null });
    try {
      const done = await signInRemotely({
        path: "/auth/callback",
        start: async (redirect_uri) => {
          const s = await call("chatgpt/login", { redirect_uri });
          if (alive.current) setStatus(s);
          if (!s.authorize_url || !s.login_id) throw new Error(s.message || t("models.chatgpt.notStarted"));
          return { authorize_url: s.authorize_url, login_id: s.login_id };
        },
        open: (url) => {
          if (alive.current) setWaiting({ url });
          openPage(url);
        },
        // ChatGPT names the client it registered in the redirect of a first sign-in.
        complete: (login_id, g) => call("chatgpt/complete", { login_id, code: g.code, state: g.state, client_id: g.client_id }),
        cancel: (login_id) => call("chatgpt/cancel", { login_id }),
        signal: abort.signal,
      });
      if (!alive.current) return;
      setStatus(done);
      if (done.state === "connected") her.show(t("models.chatgpt.signedIn"));
      else if (done.message) setError(done.message);
      await her.refreshModels();
    } catch (e) {
      if (!abort.signal.aborted && alive.current) setError(errorText(e));
      await refresh();
    } finally {
      stop.current = null;
      if (alive.current) setWaiting(null);
    }
  };

  const act = async (action: "cancel" | "logout" | "models" | "forget") => {
    setBusy(true);
    setError("");
    try {
      let next: ChatGptStatus;
      if (action === "forget") {
        await call("connections/remove", { connection_id: "chatgpt-api" });
        next = await call("chatgpt/status", {});
      } else if (action === "models") {
        await call("connections/models", { connection_id: "chatgpt-api" });
        next = await call("chatgpt/status", {});
      } else if (action === "cancel") {
        next = await call("chatgpt/cancel", { login_id: status!.login_id! });
      } else {
        next = await call("chatgpt/logout", {});
      }
      if (!alive.current) return;
      setStatus(next);
      await her.refreshModels();
    } catch (e) {
      if (alive.current) setError(errorText(e));
    } finally {
      if (alive.current) setBusy(false);
    }
  };

  const line = pending || waiting ? t("models.chatgpt.waiting") : ready ? t("models.state.readyShown", { shown, count }) : connection?.state === "needs_login" ? t("models.chatgpt.expired") : t("models.chatgpt.note");
  return (
    <>
      <div className="frow mrow" data-testid="chatgpt-connection">
        <Brand id="openai" name="ChatGPT" />
        <div className="ft">
          <div className="fn">ChatGPT</div>
          <div className="fdesc" aria-live="polite">
            {[status?.account, line].filter(Boolean).join(" · ")}
          </div>
        </div>
        {waiting ? (
          <>
            <button className="btn" disabled={!waiting.url} onClick={() => waiting.url && openPage(waiting.url)}>
              {t("models.chatgpt.openBrowser")}
            </button>
            <button className="btn" onClick={() => stop.current?.abort()}>
              {t("models.cancel")}
            </button>
          </>
        ) : pending ? (
          // Started elsewhere: its redirect goes back to that page, so it can only be ended here.
          <button className="btn" disabled={busy} onClick={() => void act("cancel")}>
            {t("models.cancel")}
          </button>
        ) : (
          <>
            {ready ? (
              <button className="btn" disabled={busy} onClick={() => setManaging(true)}>
                {t("models.manage.open")}
              </button>
            ) : (
              <button className="btn primary" disabled={busy || !rpc || !status} onClick={() => void signIn()}>
                {t("models.connect")}
              </button>
            )}
            {connection && (
              <button className="btn" disabled={busy} onClick={() => void act("logout")}>
                {t("models.disconnect")}
              </button>
            )}
            {!connection && status?.account && (
              <button className="btn" disabled={busy} onClick={() => void act("forget")}>
                {t("models.chatgpt.forget")}
              </button>
            )}
            {connection?.state === "unavailable" && (
              <button className="btn" disabled={busy} onClick={() => void act("models")}>
                {t("models.manage.refresh")}
              </button>
            )}
          </>
        )}
      </div>
      {/* A sign-in the owner cancelled needs no word: her's says so in English. */}
      {(error || (status?.state !== "cancelled" && status?.message)) && (
        <p className="mrow-err" role="status">
          {error || status?.message}
        </p>
      )}
      {managing && connection && (
        <ManageModels
          source={{
            id: connection.provider_id,
            name: "ChatGPT",
            mark: <Brand id="openai" name="ChatGPT" />,
            origin: t("models.chatgpt.origin"),
            fetchedAt: connection.models_fetched_at,
            refresh: () => act("models"),
          }}
          onClose={() => setManaging(false)}
        />
      )}
    </>
  );
}
