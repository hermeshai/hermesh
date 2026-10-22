import type { Session } from "@hermesh/proto/session";
import { displayTitle } from "@hermesh/core";
import { i18n } from "../../../shared/i18n";
import { local } from "../api";
import type { Store } from "../store";

// What the session page does, also for the app menu (Session → Interrupt This Turn, Answer Question, Copy Session ID).

/** Stops the turn in progress; the session stays and goes on with the next message. */
export function interrupt(st: Store, s: Session) {
  return st.run(() => local.interruptSession({ sessionId: s.sessionId }), i18n.t("session:actions.interrupted", { title: displayTitle(s) }));
}

export function copySessionId(st: Store, s: Session) {
  navigator.clipboard.writeText(s.sessionId).then(() => st.show(i18n.t("session:actions.copiedId")));
}

/** Puts the cursor in the pending question's answer field, if one is shown. */
export function focusAnswer() {
  document.getElementById("qtext")?.focus();
}
