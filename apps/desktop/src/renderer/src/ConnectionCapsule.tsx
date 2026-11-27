import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";
import { clockText, elapsedSeconds } from "@hermesh/core";
import type { Machine } from "@hermesh/proto/local";
import { Mark } from "@hermesh/ui";
import { connectionNotice, connectionWaits, relayDownFor, retrying, type ConnectionNotice } from "./connection";
import { Icon } from "./icons";
import { useNow, useStore } from "./store";

const onlineEvents = (f: () => void) => {
  addEventListener("online", f);
  addEventListener("offline", f);
  return () => {
    removeEventListener("online", f);
    removeEventListener("offline", f);
  };
};

/**
 * Tracks a page's connection while it is down (docs/connection-status-v2-demo.html): a short wait never shows, and
 * once shown it stays at least half a second (`connectionWaits.shownAtLeast`).
 */
export function useConnectionNotice(machine: Machine | undefined, linkUp: boolean): ConnectionNotice | null {
  const st = useStore();
  const online = useSyncExternalStore(onlineEvents, () => navigator.onLine);
  const relayUp = st.status?.relayConnected === true;
  const downSince = useRef<number | null>(null);
  if (linkUp) downSince.current = null;
  else downSince.current ??= Date.now();
  const [shown, setShown] = useState<{ notice: ConnectionNotice; at: number } | null>(null);
  const local = !!machine?.local;
  const machineOnline = machine ? machine.online : true;
  const unreachable = !!machine?.unreachable;
  const facts = { online, relayUp, local, machineOnline, unreachable, linkUp };
  const latest = useRef(facts);
  latest.current = facts;

  const tick = useCallback(() => {
    const f = latest.current, now = Date.now();
    const notice = connectionNotice({
      noNetwork: !f.online,
      relayUp: f.relayUp,
      relayDownFor: f.relayUp ? 0 : relayDownFor(now),
      machineLocal: f.local,
      machineOnline: f.machineOnline,
      machineUnreachable: f.unreachable,
      linkUp: f.linkUp,
      linkDownFor: downSince.current === null ? 0 : now - downSince.current,
    });
    setShown((was) => {
      if (notice) return was?.notice === notice ? was : { notice, at: was?.at ?? now };
      return was && now - was.at < connectionWaits.shownAtLeast ? was : null;
    });
  }, []);

  const needsTimer = !online || !relayUp || (!local && (!machineOnline || unreachable)) || !linkUp || shown !== null;

  useEffect(() => {
    tick();
  }, [tick, online, relayUp, local, machineOnline, unreachable, linkUp]);

  useEffect(() => {
    if (!needsTimer) return;
    const timer = setInterval(tick, 250);
    return () => clearInterval(timer);
  }, [tick, needsTimer]);

  return shown?.notice ?? null;
}

export function ConnectionGlyph({ notice, markSize = 18 }: { notice: ConnectionNotice; markSize?: number }) {
  if (notice === "noNetwork") return <Icon name="wifiOff" className="conn-i" />;
  if (notice === "cantReachHermesh") return <Icon name="cloudOff" className="conn-i" />;
  if (notice === "offline") return <i className="conn-ring" aria-hidden="true" />;
  return <Mark size={markSize} variant="wait" pad={20} />;
}

export function ConnectionWords({ notice, machine }: { notice: ConnectionNotice; machine: Machine | undefined }) {
  const { t } = useTranslation("common");
  const name = machine ? machine.machineName || machine.machineId : "";
  if (notice === "noNetwork") return <>{t("connection.noNetwork")}</>;
  if (notice === "connecting") return <>{machine ? t("connection.connecting", { name }) : t("connection.connectingHermesh")}</>;
  if (notice === "cantReachHermesh") return <>{t("connection.cantReach")}</>;
  return <Trans t={t} i18nKey={notice === "offline" ? "connection.offline" : "connection.notResponding"} values={{ name }} components={{ b: <b /> }} />;
}

function stripTrailingEllipsis(text: string): string {
  if (text.endsWith("…")) return text.slice(0, -1);
  if (text.endsWith("...")) return text.slice(0, -3);
  return text;
}

/**
 * Live chain-of-thought preview under the active working line: shows the latest
 * three lines anchored to the bottom with a top fade when overflowing, and opens
 * inline to a scrollable full view on click. Unmounts cleanly once thinking ends.
 */
export function LiveThinking({ text, children }: { text: string; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const [over, setOver] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const atEnd = useRef(true);
  const body = text.trim();

  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    if (open && atEnd.current) el.scrollTop = el.scrollHeight;
    const inner = el.firstElementChild as HTMLElement | null;
    setOver(!!inner && inner.offsetHeight > el.clientHeight + 1);
  }, [body, open]);

  return (
    <>
      <button
        type="button"
        className="wk-line tk-h"
        data-testid="live-thinking-toggle"
        onClick={() => {
          if (!body) return;
          atEnd.current = true;
          setOpen((v) => !v);
        }}
        aria-expanded={body ? open : undefined}
      >
        {children}
        {body && <Icon name="chevD" className="chev" />}
      </button>
      {body && (
        <div
          ref={box}
          className={`tk-peek sel-text ${open ? "open" : over ? "over" : ""}`}
          data-testid="live-thinking-body"
          onScroll={(e) => {
            const el = e.currentTarget;
            atEnd.current = el.scrollHeight - el.scrollTop - el.clientHeight < 8;
          }}
        >
          <div>{body}</div>
        </div>
      )}
    </>
  );
}

/**
 * The inline status line at the end of a session's conversation (or under the environment picker on a new session):
 * what the agent does while the connection is up, the connection state while idle and disconnected, or both in one
 * line while a running session reconnects.
 */
export function ConnectionLine({
  machine,
  linkUp,
  work,
  delayWork = false,
}: {
  machine: Machine | undefined;
  linkUp: boolean;
  work?: { text: string; since?: Date; thinking?: string };
  delayWork?: boolean;
}) {
  const notice = useConnectionNotice(machine, linkUp);
  const now = useNow(work?.since && !notice ? 1000 : 60_000);

  if (notice) {
    if (work) {
      return (
        <div className="th-work" data-testid="thread-working">
          <ConnectionGlyph notice={notice} />
          <span className="conn-t">
            {retrying(notice) ? (
              <span className="shimmer">
                {stripTrailingEllipsis(work.text)} ·{" "}
                <span data-testid="connection-line" role="status">
                  <ConnectionWords notice={notice} machine={machine} />
                </span>
              </span>
            ) : (
              <span data-testid="connection-line" role="status">
                <ConnectionWords notice={notice} machine={machine} />
              </span>
            )}
          </span>
        </div>
      );
    }
    return (
      <div className="th-work" data-testid="connection-line" role="status">
        <ConnectionGlyph notice={notice} />
        <span className={`conn-t ${retrying(notice) ? "shimmer" : ""}`}>
          <ConnectionWords notice={notice} machine={machine} />
        </span>
      </div>
    );
  }

  if (work) {
    const line = (
      <>
        <span className="th-work-t shimmer" title={work.text}>{work.text}</span>
        {work.since && <span className="el">· {clockText(elapsedSeconds(work.since, now))}</span>}
      </>
    );
    if (work.thinking) {
      return (
        <div className={`th-work ${delayWork ? "delayed" : ""}`} data-testid="thread-working">
          <Mark size={18} variant="seq" />
          <div className="wk-now">
            <LiveThinking text={work.thinking}>{line}</LiveThinking>
          </div>
        </div>
      );
    }
    return (
      <div className={`th-work ${delayWork ? "delayed" : ""}`} data-testid="thread-working">
        <Mark size={18} variant="seq" />
        {line}
      </div>
    );
  }

  return null;
}
