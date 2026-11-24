import { useTranslation } from "react-i18next";
import type { Session } from "@hermesh/proto/session";
import { displayTitle } from "@hermesh/core";
import { Icon } from "./icons";
import { layoutOf, setCard } from "./layout";
import { keys, ownKeys } from "./platform";
import { useStore } from "./store";
import { Chat } from "./session/Chat";
import { useSessionEvents } from "./session/events";
import { Inspector } from "./session/Inspector";
import "./session/markdown/markdown.css";
import "./session/session.css";
import "./thread/thread.css";

// A session: its conversation, the bottom control area while a turn is
// live, and the info panel.
export function SessionPage({ s }: { s: Session }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const { events, draft } = useSessionEvents(s.sessionId, s.progress?.lastEventSeq ?? 0n);
  // The card keeps the conversation clear of it when there is room beside it; over the conversation (no room) it leaves it be.
  const { card } = layoutOf(st);
  const info = card !== "hidden";
  const machine = st.machines.find((m) => m.machineId === s.machineId);
  return (
    <>
      <div className="ctool drag">
        <div className="ctitle">
          <h2>{displayTitle(s)}</h2>
          {/* Where it runs, as a v2 session's bar says it. */}
          <span className="th-where" data-testid="session-where">
            <Icon name={machine?.local ? "laptop" : "server"} />
            {machine?.machineName ?? s.machineId} · {s.projectId}
          </span>
        </div>
        {/* A toolbar button as the sidebar's toggle is, highlighted while the info shows. */}
        <button
          className={`tb ${info ? "on" : ""}`}
          title={t(info ? "page.hideInfo" : "page.showInfo", { keys: keys(ownKeys.toggleInspector) })}
          aria-label={t("page.info")}
          aria-pressed={info}
          onClick={() => setCard(st, !info)}
        >
          <Icon name="infoCard" />
        </button>
      </div>
      <div className={`dwrap ${card === "docked" ? "dock" : ""}`}>
        <div className="dmain">
          <Chat key={s.sessionId} s={s} events={events} draft={draft} />
        </div>
        {info && <Inspector s={s} />}
      </div>
    </>
  );
}
