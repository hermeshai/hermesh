// Days on the local calendar.

function startOfDay(d: Date): Date {
  const out = new Date(d);
  out.setHours(0, 0, 0, 0);
  return out;
}

/** The same time of day, n days later (earlier for a negative n). */
function addDays(d: Date, n: number): Date {
  const out = new Date(d);
  out.setDate(out.getDate() + n);
  return out;
}

export function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

export function isYesterday(d: Date, now: Date): boolean {
  return sameDay(d, addDays(now, -1));
}

/** On or after the start of the day six days before now's. */
export function withinWeek(d: Date, now: Date): boolean {
  return d >= addDays(startOfDay(now), -6);
}

export function pad2(n: number): string {
  return String(n).padStart(2, "0");
}

/** "09:05", on a 24-hour clock. */
export function hourMinute(d: Date): string {
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

/** "Sep 29", "9月29日". */
export function monthDay(d: Date, locale: string): string {
  return new Intl.DateTimeFormat(locale, { month: "short", day: "numeric" }).format(d);
}

/** "Mon", "周一". */
export function weekday(d: Date, locale: string): string {
  return new Intl.DateTimeFormat(locale, { weekday: "short" }).format(d);
}
