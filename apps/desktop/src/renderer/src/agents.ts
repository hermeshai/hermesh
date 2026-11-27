import { useEffect, useRef } from "react";
import type { Machine } from "@hermesh/proto/local";
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

/**
 * Refreshes account rate limits (including out-of-band queries like Antigravity's
 * `agy -p /usage`) once when Limits opens, for this Mac and any online remote
 * machines. Each machine is asked at most once per mount (and rate-limited on
 * the machine itself); stale cached limits stay visible while fresh ones load.
 */
export function useCheckLimits(machines: readonly Machine[]) {
  const asked = useRef(new Set<string>());
  useEffect(() => {
    for (const m of machines) {
      const key = m.local ? "" : m.machineId;
      if ((!m.local && !m.online) || asked.current.has(key)) continue;
      asked.current.add(key);
      void local.checkAgents(m.local ? { limits: true } : { machineId: m.machineId, limits: true }).catch(() => {});
    }
  }, [machines]);
}

/** Checks them again every few seconds while on: waiting for a sign-in in Terminal. */
export function useRecheckAgents(on: boolean) {
  useEffect(() => {
    if (!on) return;
    const id = setInterval(check, 3000);
    return () => clearInterval(id);
  }, [on]);
}
