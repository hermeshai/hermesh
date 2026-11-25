import { useEffect, useState } from "react";
import type { Machine } from "@hermesh/proto/local";

/** This computer's own name (as in System Settings, e.g. "Yee HomeBook"); fallback until it is known. */
export function useComputerName(fallback = "—"): string {
  const [name, setName] = useState("");
  useEffect(() => {
    void window.hermesh?.computerName().then(setName);
  }, []);
  return name || fallback;
}

/** How Her's words name the computer she runs on: this one by its own name, another by its machine's. */
export function useMachineTitle(m: Machine): string {
  const computer = useComputerName(m.machineName);
  return m.local ? computer : m.machineName;
}
