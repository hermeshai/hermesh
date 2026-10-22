import { useEffect } from "react";
import { local } from "./api";

// This Mac's coding agents are checked (each CLI run) only when something
// needs to know, never in the background: a page showing them asks.

const check = () => void local.checkAgents({}).catch(() => {}); // the next poll shows what came of it

/** Checks this Mac's coding agents once a page showing them opens (on: it shows them). */
export function useCheckAgents(on = true) {
  useEffect(() => {
    if (on) check();
  }, [on]);
}

/** Checks them again every few seconds while on: waiting for a sign-in in Terminal. */
export function useRecheckAgents(on: boolean) {
  useEffect(() => {
    if (!on) return;
    const id = setInterval(check, 3000);
    return () => clearInterval(id);
  }, [on]);
}
