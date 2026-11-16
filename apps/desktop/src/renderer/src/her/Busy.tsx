import { useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { BusyAgent } from "@hermesh/her-client";
import { Icon } from "../icons";
import { useNow, useStore } from "../store";
import { useHer } from "./context";
import { MenuButton } from "./menus";

/**
 * The last bubble while something of Her's is at work (docs/her-team.md §15.6, docs/her-busy-demo.html): one line,
 * the dots and who is at work, swept left to right by one light. Her while she answers ("Typing…"), then her
 * sub-agents at work by name, never what they do. One sub-agent opens its record (›). Several take turns, the next
 * each time the light has swept the line, with their number and a chevron pointing where their menu opens (up):
 * the menu lists them all, each opening its record, and Progress.
 */
export function BusyBubble({ busy, retry }: { busy: { her: boolean; agents: BusyAgent[] }; retry?: { at: number } }) {
  const { t, i18n } = useTranslation("her");
  const st = useStore();
  const her = useHer();
  const now = useNow(1000).getTime();
  const [turn, setTurn] = useState(0);
  // The names stay while the pointer is on the line or its menu is open: what is under it is what is clicked.
  const [hovered, setHovered] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const agents = busy.agents;
  const shown = agents.length ? agents[turn % agents.length] : undefined;
  const herWords = busy.her ? (retry && now - retry.at > 10_000 ? t("chat.retrying") : t("line.typing")) : null;
  const open = (a?: BusyAgent) => st.set({ selection: { kind: "progress", machine: her.machine.machineId, ...(a && { task: { agent: a.id, fromHer: true } }) } });
  const words = (
    <span className="wb-sweep" onAnimationIteration={() => agents.length > 1 && !hovered && !menuOpen && setTurn((n) => n + 1)}>
      <span className="dots" aria-hidden="true">
        <i />
        <i />
        <i />
      </span>
      <span className="wb-w">
        {herWords && <span className="wb-t">{herWords}</span>}
        {herWords && shown && <span className="wb-sep">·</span>}
        {agents.length === 1 && <span className="wb-t">{shown!.name}</span>}
        {agents.length > 1 && (
          // One cell, as wide as the widest name: the bubble keeps its width as they take turns.
          <span className="wb-rot">
            {agents.map((a) => (
              <span key={a.id} className={`wb-t ${a === shown ? "cur" : ""}`}>
                {a.name}
              </span>
            ))}
          </span>
        )}
      </span>
    </span>
  );
  const names = new Intl.ListFormat(i18n.language, { type: "conjunction" }).format(agents.map((a) => a.name));
  const lead = herWords ? `${herWords} · ` : "";
  let line: ReactNode;
  if (agents.length === 0) {
    line = (
      <div className="wb-h" role="status" aria-label={herWords ?? undefined}>
        {words}
      </div>
    );
  } else if (agents.length === 1) {
    line = (
      <button type="button" className="wb-h" aria-label={lead + t("line.agentAtWork", { name: shown!.name })} onClick={() => open(shown)}>
        {words}
        <Icon name="chevR" className="wb-go" />
      </button>
    );
  } else {
    line = (
      <MenuButton
        className="wb-h"
        title={`${lead}${t("line.agentsWorking", { count: agents.length })}: ${names}`}
        label={
          <>
            {words}
            <span className={`wb-pop ${menuOpen ? "open" : ""}`} aria-hidden="true">
              {agents.length}
              <Icon name="chevD" />
            </span>
          </>
        }
      >
        {(close) => (
          <>
            <Opened set={setMenuOpen} />
            <div className="mgh">{t("line.agentsWorking", { count: agents.length })}</div>
            {agents.map((a) => (
              <button key={a.id} className="mi wb-mi" role="menuitem" tabIndex={-1} onMouseMove={(e) => e.currentTarget.focus()} onClick={() => (close(), open(a))}>
                <span className="dots" aria-hidden="true">
                  <i />
                  <i />
                  <i />
                </span>
                <span className="wb-nm">{a.name}</span>
              </button>
            ))}
            <div className="msep" role="separator" />
            <button className="mi" role="menuitem" tabIndex={-1} onMouseMove={(e) => e.currentTarget.focus()} onClick={() => (close(), open())}>
              {t("line.allInProgress")}
            </button>
          </>
        )}
      </MenuButton>
    );
  }
  return (
    <div className="bm working">
      <div className="bm-b">
        <div className="bline">
          <div className="bbub wbub" onMouseEnter={() => setHovered(true)} onMouseLeave={() => setHovered(false)}>
            {line}
          </div>
        </div>
      </div>
    </div>
  );
}

/** Says, while it is shown, that the menu it is in is open. */
function Opened({ set }: { set: (open: boolean) => void }) {
  useEffect(() => {
    set(true);
    return () => set(false);
  }, [set]);
  return null;
}
