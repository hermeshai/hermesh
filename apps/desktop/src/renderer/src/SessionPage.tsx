import { useTranslation } from "react-i18next";
import type { Session } from "@hermesh/proto/session";
import { displayTitle } from "@hermesh/core";
import { Icon } from "./icons";
import { SidebarToggle } from "./MainWindow";
import { keys, ownKeys } from "./platform";
import { useStore } from "./store";
import { Chat } from "./session/Chat";
import { useSessionEvents } from "./session/events";
import { Inspector } from "./session/Inspector";
import "./session/session.css";

// A session: its conversation, the bottom control area while a turn is
// live, and the info panel.
export function SessionPage({ s }: { s: Session }) {
  const { t } = useTranslation("session");
  const st = useStore();
  const { events, draft } = useSessionEvents(s.sessionId, s.progress?.lastEventSeq ?? 0n);
  return (
    <>
      <div className="ctool drag">
        <SidebarToggle />
        <div className="ctitle">
          <h2>{displayTitle(s)}</h2>
        </div>
        {/* A toolbar button as the sidebar's toggle is, highlighted while the info shows. */}
        <button
          className={`tb ${st.inspector ? "on" : ""}`}
          title={t(st.inspector ? "page.hideInfo" : "page.showInfo", { keys: keys(ownKeys.toggleInspector) })}
          aria-label={t("page.info")}
          aria-pressed={st.inspector}
          onClick={() => st.set({ inspector: !st.inspector })}
        >
          <Icon name="infoCard" />
        </button>
      </div>
      <div className={`dwrap ${st.inspector ? "dock" : ""}`}>
        <div className="dmain">
          <Chat key={s.sessionId} s={s} events={events} draft={draft} />
        </div>
        {st.inspector && <Inspector s={s} />}
      </div>
    </>
  );
}
