import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { Trans, useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { connectionNotice, connectionWaits, relayDownFor, retrying, type ConnectionNotice } from "./connection";
import { Icon } from "./icons";
import { useStore } from "./store";

const onlineEvents = (f: () => void) => {
  addEventListener("online", f);
  addEventListener("offline", f);
  return () => {
    removeEventListener("online", f);
    removeEventListener("offline", f);
  };
};

/**
 * The line above a page's box while the connection that page uses is down (docs/connection-status-demo.html): a pill
 * of the window's own material, saying why and what happens to what is sent meanwhile; gone once the connection is
 * back. What it says is `connectionNotice`'s, as on the iPhone: a short wait never shows, and once shown it stays at
 * least half a second. It comes in and goes from the box's top, so what is above it moves as it does.
 */
export function ConnectionCapsule({ machine, linkUp }: { machine: Machine | undefined; linkUp: boolean }) {
  const { t } = useTranslation("common");
  const st = useStore();
  const online = useSyncExternalStore(onlineEvents, () => navigator.onLine);
  const relayUp = st.status?.relayConnected === true;
  const downSince = useRef<number | null>(null);
  if (linkUp) downSince.current = null;
  else downSince.current ??= Date.now();
  const [shown, setShown] = useState<{ notice: ConnectionNotice; at: number } | null>(null);
  const facts = { online, relayUp, local: !!machine?.local, machineOnline: machine ? machine.online : true, unreachable: !!machine?.unreachable, linkUp };
  const latest = useRef(facts);
  latest.current = facts;

  useEffect(() => {
    const tick = () => {
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
    };
    tick();
    const timer = setInterval(tick, 250);
    return () => clearInterval(timer);
  }, []);

  // What it said last, kept a moment while it goes.
  const [last, setLast] = useState<ConnectionNotice | null>(null);
  useEffect(() => {
    if (shown) return setLast(shown.notice);
    const timer = setTimeout(() => setLast(null), 220);
    return () => clearTimeout(timer);
  }, [shown?.notice]);
  const notice = shown?.notice ?? last;
  if (!notice) return null;

  const name = machine ? machine.machineName || machine.machineId : "";
  const glyph =
    notice === "noNetwork" ? <Icon name="wifiOff" className="conn-i" />
    : notice === "cantReachHermesh" ? <Icon name="cloudOff" className="conn-i" />
    : notice === "offline" ? <i className="conn-ring" />
    : <i className="conn-dot" />;
  const words =
    notice === "noNetwork" ? t("connection.noNetwork")
    : notice === "connecting" ? (machine ? t("connection.connecting", { name }) : t("connection.connectingHermesh"))
    : notice === "cantReachHermesh" ? t("connection.cantReach")
    : <Trans t={t} i18nKey={notice === "offline" ? "connection.offline" : "connection.notResponding"} values={{ name }} components={{ b: <b /> }} />;
  return (
    <div className={`conn-cap ${shown ? "" : "out"}`} role="status" data-testid="connection-capsule">
      {glyph}
      <span className={`conn-t ${retrying(notice) ? "wb-sweep" : ""}`}>{words}</span>
    </div>
  );
}
