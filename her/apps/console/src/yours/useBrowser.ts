import { useCallback, useEffect, useState } from "react";
import type { RpcClient } from "@hermesh/her-client";
import type { BrowserStatus } from "@hermesh/her-protocol";

/** her's word on the browsers (browser/status, then browser/changed) and her user file's settings
 * (config/read, then config/changed), on the page's connection: what the Browser page's rows read. */
export function useBrowser(rpc: RpcClient | null) {
  const [status, setStatus] = useState<BrowserStatus | null>(null);
  const [config, setConfig] = useState<unknown>(null);
  const readConfig = useCallback(async () => {
    if (rpc) setConfig((await rpc.request("config/read", {})).config);
  }, [rpc]);
  useEffect(() => {
    if (!rpc) return;
    let live = true;
    const offStatus = rpc.on("browser/changed", (s) => live && setStatus(s));
    rpc.request("browser/status", {}).then((s) => live && setStatus(s), () => {});
    readConfig().catch(() => {});
    const offConfig = rpc.on("config/changed", () => void readConfig().catch(() => {}));
    return () => {
      live = false;
      offStatus();
      offConfig();
    };
  }, [rpc, readConfig]);
  return { status, config, readConfig };
}

export type BrowserState = ReturnType<typeof useBrowser>;
