import { useLayoutEffect } from "react";

/** Register the visible conversation or request only while its view is mounted. */
export function useNoticeTarget(target: string | undefined) {
  useLayoutEffect(() => {
    if (!target) return;
    window.hermesh?.setNoticeTarget(target, true);
    return () => window.hermesh?.setNoticeTarget(target, false);
  }, [target]);
}
