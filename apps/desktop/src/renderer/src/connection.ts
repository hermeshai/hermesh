// The one line a page with a box says about its connection (docs/connection-status-demo.html): above the box, about
// the connection that page uses, and only while it is down. The iPhone has the same rules (HermeshKit's
// ConnectionNotice.swift).

/** What the line above a page's box says, while the connection the page uses is down. */
export type ConnectionNotice = "noNetwork" | "connecting" | "cantReachHermesh" | "offline" | "notResponding";

/** What a page knows of its connection, for `connectionNotice`. Durations in milliseconds. */
export type ConnectionFacts = {
  /** This computer has no network. */
  noNetwork: boolean;
  relayUp: boolean;
  /** How long the relay has been down (0 while it is up). */
  relayDownFor: number;
  /** The page's machine is this computer: it is reached without the relay. */
  machineLocal: boolean;
  machineOnline: boolean;
  /** The machine is online and hermeshd can't reach it. */
  machineUnreachable: boolean;
  /** The connection the page uses is up (or the page needs none now). */
  linkUp: boolean;
  /** How long that connection has been down. */
  linkDownFor: number;
};

/** How long each wait goes unsaid: a short one never shows. */
export const connectionWaits = { connecting: 1000, cantReachHermesh: 10_000, notResponding: 15_000, shownAtLeast: 500 };

/** Since when the relay has been down, while it is: the window's, whichever page shows (noteRelay). */
let relayDownAt: number | null = null;

/** Told of each status (store.tsx): when the relay went down is kept until it is back. */
export function noteRelay(up: boolean) {
  if (up) relayDownAt = null;
  else relayDownAt ??= Date.now();
}

/** How long the relay has been down, as noteRelay was told. */
export const relayDownFor = (now: number) => (relayDownAt === null ? 0 : now - relayDownAt);

/** Something is being tried again meanwhile: the line shimmers. */
export const retrying = (n: ConnectionNotice) => n === "connecting" || n === "cantReachHermesh" || n === "notResponding";

/**
 * The line for a page, or null while its connection is up or has been down only a moment. Nothing is said while the
 * page's own connection is up, whatever else is down (Her on this Mac while the relay reconnects).
 */
export function connectionNotice(f: ConnectionFacts): ConnectionNotice | null {
  if (f.linkUp) return null;
  if (f.noNetwork) return "noNetwork";
  if (!f.machineLocal) {
    // While the relay is down, what is known of the machine is from before: the relay is what to say.
    if (!f.relayUp) {
      if (f.relayDownFor >= connectionWaits.cantReachHermesh) return "cantReachHermesh";
    } else if (!f.machineOnline) {
      return "offline";
    } else if (f.machineUnreachable || f.linkDownFor >= connectionWaits.notResponding) {
      return "notResponding";
    }
  }
  return f.linkDownFor >= connectionWaits.connecting ? "connecting" : null;
}
