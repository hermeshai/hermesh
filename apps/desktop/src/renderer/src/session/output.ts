import type { ToolResult } from "@hermesh/proto/session";

export function partialOutput(_r: ToolResult): boolean {
  return false;
}

export function useToolOutput(_sessionId: string, r: ToolResult | undefined, _seq: bigint | undefined, _open: boolean): { text: string; reading: boolean; failed: boolean } {
  return { text: r?.summary ?? "", reading: false, failed: false };
}
