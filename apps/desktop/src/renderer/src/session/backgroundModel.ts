// Pure formatting and row-summary helpers for the session background tasks panel (Background.tsx),
// kept free of runtime proto enum imports so Node's strip-only test runner runs them directly.

// eslint-disable-next-line no-control-regex
const ansiPattern = /\x1b\[[0-9;]*[a-zA-Z]/g;

export type BackgroundTaskSummaryInput = {
  taskId: string;
  title: string;
  command: string;
  outputTail: string;
  summary: string;
};

export type TimeTranslate = (key: string, opts?: Record<string, unknown>) => string;

export function stripAnsi(text: string): string {
  return text.replace(ansiPattern, "");
}

/** How long a finished background task took, in its two largest units: "48 s", "2 min 14 s", "3 hr 5 min". */
export function formatTaskDuration(ms: number, tTime: TimeTranslate): string {
  const s = Math.max(1, Math.round(ms / 1000));
  const part = (unit: "seconds" | "minutes" | "hours" | "days", count: number) => tTime(`time.${unit}`, { count });
  const pair = (big: string, small: string, rest: number) => (rest > 0 ? tTime("time.pair", { big, small }) : big);
  if (s < 60) return part("seconds", s);
  if (s < 3600) return pair(part("minutes", Math.trunc(s / 60)), part("seconds", s % 60), s % 60);
  if (s < 86400) return pair(part("hours", Math.trunc(s / 3600)), part("minutes", Math.trunc(s / 60) % 60), Math.trunc(s / 60) % 60);
  return pair(part("days", Math.trunc(s / 86400)), part("hours", Math.trunc(s / 3600) % 24), Math.trunc(s / 3600) % 24);
}

/** Single-line label for a background task row, collapsed so shimmer masks match text width. */
export function taskLabel(bt: Pick<BackgroundTaskSummaryInput, "taskId" | "title" | "command">): string {
  const raw = bt.title.trim() || bt.command.trim() || bt.taskId;
  return raw.replace(/\s+/g, " ").trim();
}

/** The second line under a background task row: its command handle when distinct, else latest output or kind. */
export function taskSecondary(bt: BackgroundTaskSummaryInput, kindText: string): { text: string; mono: boolean } {
  const title = bt.title.trim();
  const cmd = bt.command.trim();
  if (title !== "" && cmd !== "" && title !== cmd) {
    return { text: cmd.replace(/\s+/g, " "), mono: true };
  }
  const cleanOut = stripAnsi(bt.outputTail);
  const lastOut = cleanOut
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean)
    .at(-1);
  if (lastOut) return { text: lastOut, mono: false };
  const sum = bt.summary.trim();
  if (sum && sum !== "stopped by user") {
    return { text: sum.replace(/\s+/g, " "), mono: false };
  }
  return { text: kindText, mono: false };
}

/** Formats a running clock ("0:21", "1:02:03") or completed duration. */
export function formatTaskTiming(
  running: boolean,
  started: Date | undefined,
  ended: Date | undefined,
  now: Date,
  tTime: TimeTranslate,
): string {
  if (!started) return "";
  if (running) {
    const sec = Math.max(0, Math.floor((now.getTime() - started.getTime()) / 1000));
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    const s = sec % 60;
    const pad = (n: number) => String(n).padStart(2, "0");
    return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
  }
  return ended ? formatTaskDuration(Math.max(0, ended.getTime() - started.getTime()), tTime) : "";
}
