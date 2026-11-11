import { useEffect } from "react";
import type { Store } from "./store";

// How the window lays itself out as it narrows. The conversation keeps at
// least `conversationMin`; what is beside it gives way, the right side
// first: the info card goes (its room is kept no longer), then the
// sidebar. What gave way can still be asked for: it then shows over the
// conversation instead of beside it (a peek) until it is put away, or the
// selection changes.

/** Widths (px): the sidebar's column, the room the info card keeps on the conversation's right. */
export const sidebarWidth = 272;
export const cardRoom = 270;
/** The least a conversation needs: a composer with its controls, and a message column that still reads as one. */
export const conversationMin = 560;

/** What is over the conversation for now, though there is no room for it beside it. */
export type Peek = "sidebar" | "card" | null;

export type Layout = {
  /** Beside the conversation, over it, or not shown. */
  sidebar: "docked" | "over" | "hidden";
  card: "docked" | "over" | "hidden";
};

/** The layout at the window's width, as the person's choices and the peek have it. */
export function layoutOf(st: Pick<Store, "sidebar" | "inspector" | "peek" | "width">): Layout {
  const width = st.width;
  const sidebarFits = width >= sidebarWidth + conversationMin;
  const sidebar = sidebarFits ? (st.sidebar ? "docked" : "hidden") : st.peek === "sidebar" ? "over" : "hidden";
  const content = width - (sidebar === "docked" ? sidebarWidth : 0);
  // The card gives way first: where the sidebar has, the card has too (its room alone, 830, is under the sidebar's 832).
  const cardFits = sidebarFits && content >= conversationMin + cardRoom;
  const card = cardFits ? (st.inspector ? "docked" : "hidden") : st.peek === "card" ? "over" : "hidden";
  return { sidebar, card };
}

/** Ends a peek once what it showed fits beside the conversation again (where the person's own choice then holds). */
export function usePeekEnd(st: Store) {
  const ended =
    st.peek === "sidebar" ? layoutOf({ ...st, sidebar: true, peek: null }).sidebar === "docked" : st.peek === "card" && layoutOf({ ...st, inspector: true, peek: null }).card === "docked";
  useEffect(() => {
    if (ended) st.set({ peek: null });
  }, [ended, st.set]);
}

/** Shows or hides the sidebar: beside the conversation when there is room (the person's choice, kept), else over it for now. */
export function setSidebar(st: Store, show: boolean) {
  const fits = st.width >= sidebarWidth + conversationMin;
  st.set(fits ? { sidebar: show, peek: null } : { peek: show ? "sidebar" : null });
}

/** Shows or hides the info card: kept beside the conversation when there is room, else over it for now. */
export function setCard(st: Store, show: boolean) {
  const fits = layoutOf({ ...st, inspector: true, peek: null }).card === "docked";
  st.set(fits ? { inspector: show, peek: null } : { peek: show ? "card" : null });
}
