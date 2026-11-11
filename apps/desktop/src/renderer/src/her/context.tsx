import { createContext, useContext } from "react";
import type { BotSummary, Methods } from "@hermesh/her-protocol";
import type { Machine } from "@hermesh/proto/local";
import type { Environment } from "./connection";

// What every part of the Her page shares: the environment shown, its
// connection, Her (its default assistant), and the page's own state.

/** Which of Her's routines is open, in the panel beside the conversation. */
export type RoutinesRoute = { routine?: string; run?: string };

/** The one place beside the conversation: Her's settings (or their memory page), or its routines. */
export type Panel = null | { kind: "settings"; page?: "memory" } | { kind: "routines"; route: RoutinesRoute };

/** A sheet over the page: another environment, or full access for Her (signed). */
export type HerSheet = null | { kind: "add" } | { kind: "access"; bot: BotSummary };

export type Her = {
  /** The environment shown: a machine Her is on for. */
  machine: Machine;
  /** Every environment. */
  envs: Machine[];
  /** Shows another environment. */
  choose: (machineId: string) => void;
  env: Environment;
  /**
   * Her: the environment's default assistant, the one the apps show for now
   * (docs/her-server.md §9.1). Others her may hold are left alone.
   */
  bot: BotSummary | undefined;
  panel: Panel;
  setPanel: (p: Panel) => void;
  sheet: HerSheet;
  setSheet: (s: HerSheet) => void;
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
