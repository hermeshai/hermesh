import { createContext, useContext } from "react";
import type { BotSummary, Methods } from "@hermesh/her-protocol";
import type { Machine } from "@hermesh/proto/local";
import type { Environment } from "./connection";

// What every part of the Her page shares: the environment shown, its
// connection, the assistant on screen, and the page's own state.

/** Which of an assistant's routines is open, in the panel beside its conversation. */
export type RoutinesRoute = { routine?: string; run?: string };

/** The one place beside the conversation: an assistant's settings, or its routines. */
export type Panel = null | { kind: "settings" } | { kind: "routines"; route: RoutinesRoute };

/** A sheet over the page: a new assistant, another environment, or full access for an assistant (signed). */
export type HerSheet = null | { kind: "new" } | { kind: "add" } | { kind: "access"; bot: BotSummary };

export type Her = {
  /** The environment shown: a machine Her is on for. */
  machine: Machine;
  /** Every environment. */
  envs: Machine[];
  env: Environment;
  /** The assistant on screen. */
  bot: BotSummary | undefined;
  selectBot: (id: string) => void;
  panel: Panel;
  setPanel: (p: Panel) => void;
  sheet: HerSheet;
  setSheet: (s: HerSheet) => void;
  /** The assistant whose name is being edited in the list. */
  renaming: string | null;
  setRenaming: (id: string | null) => void;
  /** A request on the environment's connection; fails while it is down. */
  call: <M extends keyof Methods & string>(method: M, params: Methods[M][0]) => Promise<Methods[M][1]>;
  /** An action whose failure becomes a toast; resolves to whether it worked. */
  run: (action: () => Promise<unknown>, done?: string) => Promise<boolean>;
  /** The environment's console, in a window of its own. */
  openConsole: () => void;
};

export const HerContext = createContext<Her | null>(null);

export function useHer(): Her {
  const her = useContext(HerContext);
  if (!her) throw new Error("useHer outside the Her page");
  return her;
}
