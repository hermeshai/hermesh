import type { CSSProperties, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { Machine } from "@hermesh/proto/local";
import { AgentTile } from "@hermesh/ui";
import { Cat } from "../her/Cat";
import { environments, pickEnvironment, reachable } from "../her/environments";
import { nextWhoForKey, type Who } from "./presenceKeys";
import "./presence.css";

export { nextWhoForKey, type Who };

// Who a new session goes to (docs/her-conversations.md §3.1): a coding agent, or Her. The one chosen stands in the
// middle; the other waits small beside it, Agent on the left and Her on the right, and a click on it, or ⌘N again,
// swaps them. The page's light takes the colour of the one chosen.

const whoKey = "new-session-who";

/** Who the last new session went to; the first time, Agent (D19). */
export function rememberedWho(): Who {
  try {
    return localStorage.getItem(whoKey) === "her" ? "her" : "agent";
  } catch {
    return "agent";
  }
}

export function rememberWho(who: Who) {
  try {
    localStorage.setItem(whoKey, who);
  } catch {
    // A convenience: without storage the next one is Agent.
  }
}

/** Where Her runs for a new conversation: the environment her page showed last, else the first; none while she is off everywhere. */
export function herMachine(machines: Machine[]): Machine | undefined {
  let last: string | undefined;
  try {
    last = localStorage.getItem("her-env") ?? undefined;
  } catch {
    last = undefined;
  }
  return pickEnvironment(environments(machines), last);
}

/** The light behind the page, in the colours of who is chosen. */
export function Aurora() {
  return (
    <div className="pr-aura" aria-hidden="true">
      <i />
      <i />
    </div>
  );
}

/** The two of them: the chosen one in the middle with its name under it, the other small beside it. */
export function Presence({ who, onWho }: { who: Who; onWho: (who: Who) => void }) {
  const { t } = useTranslation("thread");
  const shortcut = "Tab";
  return (
    <div className="pr" data-testid="presence">
      <div className="pr-pair" data-who={who}>
        <button
          className="pr-a pr-agent"
          data-who="agent"
          aria-pressed={who === "agent"}
          aria-label={t("launch.roleAgent")}
          title={who === "agent" ? undefined : t("launch.toAgent", { keys: shortcut })}
          onClick={() => onWho("agent")}
        >
          <span className="pr-tiles">
            <AgentTile agent="claude" size={40} />
            <AgentTile agent="codex" size={40} />
          </span>
        </button>
        <button
          className="pr-a pr-her"
          data-who="her"
          aria-pressed={who === "her"}
          aria-label={t("launch.roleHer")}
          title={who === "her" ? undefined : t("launch.toHer", { keys: shortcut })}
          onClick={() => onWho("her")}
        >
          <Cat small={false} />
        </button>
      </div>
      <div className="pr-name">{t(who === "her" ? "launch.roleHer" : "launch.roleAgent")}</div>
    </div>
  );
}

/** What a new conversation with Her asks, and where she is; offline, that the message waits for her machine. */
export function HerAsk({ top, machine, rise }: { top: ReactNode; machine: Machine; rise: boolean }) {
  const { t } = useTranslation("thread");
  return (
    <div className="th-ask">
      {top}
      <h1 key="her" className={rise ? "pr-rise" : undefined}>
        {t("launch.herAsk")}
      </h1>
      <span className={`th-on pr-where ${rise ? "pr-rise" : ""}`} style={{ "--d": "90ms" } as CSSProperties} data-testid="launch-her-where">
        <Cat small />
        {t("launch.herWhere", { machine: machine.machineName })}
        {!reachable(machine) && <span className="pr-off">· {t("launch.offlineQueues")}</span>}
      </span>
    </div>
  );
}
