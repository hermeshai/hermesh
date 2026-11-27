export type Who = "agent" | "her";

/**
 * Keyboard shortcuts on the uncovered new-session page:
 * - Empty composer: Tab toggles Agent ↔ Her; ← picks Agent (left) and → picks Her (right).
 * - Non-empty composer: ←/→ move the text caret normally; ⇧Tab or ⌘N/Ctrl+N switches while keeping the draft.
 */
export function nextWhoForKey(
  e: { key: string; keyCode?: number; isComposing?: boolean; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean },
  ctx: { who: Who; promptEmpty: boolean; inComposer: boolean; inOtherEditable: boolean; overlayOpen: boolean },
): Who | null {
  if (e.isComposing || e.keyCode === 229 || ctx.overlayOpen) return null;
  const flip: Who = ctx.who === "her" ? "agent" : "her";
  if ((e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "n") {
    return flip;
  }
  if (ctx.inOtherEditable || e.metaKey || e.ctrlKey || e.altKey) return null;
  if (e.key === "Tab" && e.shiftKey) return flip;
  if (e.key === "Tab" && !e.shiftKey && ctx.promptEmpty) return flip;
  if (!e.shiftKey && (!ctx.inComposer || ctx.promptEmpty)) {
    if (e.key === "ArrowLeft") return "agent";
    if (e.key === "ArrowRight") return "her";
  }
  return null;
}
