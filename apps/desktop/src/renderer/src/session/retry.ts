/**
 * How long to wait, in milliseconds, before following a session's events
 * again after the stream broke: at once the first time (a connection that
 * was replaced works again right away), then longer each time it breaks
 * again with nothing received (broken: how many times in a row so far).
 */
export function followAgainIn(broken: number): number {
  const waits = [0, 1000, 2000, 5000];
  return waits[Math.min(broken, waits.length - 1)];
}
