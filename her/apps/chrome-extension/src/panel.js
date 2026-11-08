// What the extension's page says for a state of the worker: one line, at most one sentence more,
// and at most one thing to do (your-browser-design §3). Names are message names of _locales.

/** A session the user has to do something for. */
export const isWaiting = (session) => !!session.handoff || session.state === "awaiting_approval";

function connected(state) {
  if (!state.inUse) return { dot: "", title: "statusStandby", hint: "hintStandby", action: { label: "useThis", do: "use_this" } };
  const waiting = state.sessions.filter(isWaiting).length;
  if (waiting === 1) return { dot: "warn", title: "waitingOne" };
  if (waiting > 1) return { dot: "warn", title: "waiting", count: waiting };
  return { dot: "on", title: "statusConnected" };
}

/** The top of the page. `action.closes`: the page closes on it, as what it does happens elsewhere. */
export function headline(state) {
  switch (state.host) {
    case "connected":
      return connected(state);
    case "not_open":
      return { dot: "", title: "statusNotOpen", hint: "hintNotOpen", action: { label: "openApp", do: "open_app", closes: true } };
    case "missing":
      return { dot: "", title: "statusMissing", hint: "hintMissing" };
    case "outdated":
      return state.update === "her"
        ? { dot: "bad", title: "statusUpdateHer", hint: "hintUpdateHer", action: { label: "openApp", do: "open_app", closes: true } }
        : { dot: "bad", title: "statusUpdateExtension", hint: "hintUpdateExtension", action: { label: "update", do: "update", closes: true } };
    default:
      return state.stuck
        ? { dot: "warn", title: "statusStuck", hint: "hintStuck", action: { label: "retry", do: "retry" } }
        : { dot: "busy", title: "statusConnecting" };
  }
}

/** The conversations to list: only a working connection can act on them, and who waits comes first. */
export function listed(state) {
  if (state.host !== "connected") return [];
  return [...state.sessions.filter(isWaiting), ...state.sessions.filter((s) => !isWaiting(s))];
}

/** Handing over a tab is the quiet thing to offer: not while anything else asks for the user. */
export function offersTab(state) {
  return state.host === "connected" && state.inUse && state.offerable && !state.sessions.some(isWaiting);
}
