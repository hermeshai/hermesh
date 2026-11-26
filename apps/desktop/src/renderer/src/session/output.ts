import { useEffect, useState } from "react";
import type { ToolCall, ToolResult } from "@hermesh/proto/session";
import { sessions } from "../api";

const MAX_CACHED_OUTPUTS = 32;
const fullOutputs = new Map<string, string>();

function rememberOutput(key: string, text: string) {
  if (fullOutputs.has(key)) fullOutputs.delete(key);
  fullOutputs.set(key, text);
  if (fullOutputs.size > MAX_CACHED_OUTPUTS) {
    const oldest = fullOutputs.keys().next().value;
    if (oldest !== undefined) fullOutputs.delete(oldest);
  }
}

export function partialInput(c: ToolCall): boolean {
  return c.inputTruncated && c.inputBlobSha256 !== "";
}

export function partialOutput(r: ToolResult): boolean {
  return r.truncated && r.blobSha256 !== "";
}

function useBlobText(
  sessionId: string,
  inline: string,
  blobSha: string,
  open: boolean,
): { text: string; reading: boolean; failed: boolean } {
  const cacheKey = sessionId && blobSha ? `${sessionId}:${blobSha}` : "";
  const cached = cacheKey ? fullOutputs.get(cacheKey) : undefined;
  const [state, setState] = useState<{ key: string; text?: string; reading: boolean; failed: boolean }>({
    key: "",
    reading: false,
    failed: false,
  });

  useEffect(() => {
    if (!open || !cacheKey || cached !== undefined) return;
    let live = true;
    setState({ key: cacheKey, reading: true, failed: false });
    sessions
      .downloadBlob({ sessionId, blobSha256: blobSha })
      .then((res) => {
        const decoded = new TextDecoder("utf-8", { fatal: false }).decode(res.data);
        rememberOutput(cacheKey, decoded);
        if (live) setState({ key: cacheKey, text: decoded, reading: false, failed: false });
      })
      .catch(() => {
        if (live) setState({ key: cacheKey, reading: false, failed: true });
      });
    return () => {
      live = false;
    };
  }, [open, cacheKey, cached, sessionId, blobSha]);

  if (cached !== undefined) {
    return { text: cached, reading: false, failed: false };
  }
  if (cacheKey && state.key === cacheKey) {
    return { text: state.text ?? inline, reading: state.reading, failed: state.failed };
  }
  return { text: inline, reading: false, failed: false };
}

export function useToolInput(
  sessionId: string,
  c: ToolCall,
  open: boolean,
): { text: string; reading: boolean; failed: boolean } {
  return useBlobText(sessionId, c.input, partialInput(c) ? c.inputBlobSha256 : "", open);
}

export function useToolOutput(
  sessionId: string,
  r: ToolResult | undefined,
  _seq: bigint | undefined,
  open: boolean,
): { text: string; reading: boolean; failed: boolean } {
  return useBlobText(sessionId, r?.summary ?? "", r && partialOutput(r) ? r.blobSha256 : "", open);
}
