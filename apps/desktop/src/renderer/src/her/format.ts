import { hourMinute, isYesterday, modelName, monthDay, sameDay, weekday, withinWeek, type ToolView } from "@hermesh/her-client";
import type { ModelSummary, ModelsListResult, PermissionMode, ThinkingLevel } from "@hermesh/her-protocol";
import { i18n } from "../../../shared/i18n";

// Her's words for times, steps, models and permissions, in the app's language.

// Keys made at run time (`steps.read.done`): the typed lookup cannot check them.
const t = i18n.getFixedT(null, "her") as unknown as (key: string, o?: Record<string, unknown>) => string;
const locale = () => i18n.language;

/** When something last changed: "15:42" today, "Yesterday", a weekday within the week, else the date. */
export function shortTime(d: Date, now: Date): string {
  if (sameDay(d, now)) return hourMinute(d);
  if (isYesterday(d, now)) return t("time.yesterday");
  if (withinWeek(d, now)) return weekday(d, locale());
  return monthDay(d, locale());
}

/** A time line before a message, only for the first one and after a pause: "Today 18:22". */
export function timeLine(at: Date, previous: Date | undefined, now: Date, pause: number): string | null {
  if (previous && at.getTime() - previous.getTime() < pause) return null;
  const day = sameDay(at, now) ? t("time.today") : isYesterday(at, now) ? t("time.yesterday") : withinWeek(at, now) ? weekday(at, locale()) : monthDay(at, locale());
  return `${day} ${hourMinute(at)}`;
}

/** A moment near now or not: "今晚 23:00", "明天 00:00", "昨天 09:00", "11 月 10 日 09:00". */
export function momentText(d: Date, now: Date): string {
  const time = hourMinute(d);
  if (sameDay(d, now)) return `${t(d.getHours() >= 18 ? "time.tonight" : "time.today")} ${time}`;
  if (isYesterday(now, d)) return `${t("time.tomorrow")} ${time}`;
  if (isYesterday(d, now)) return `${t("time.yesterday")} ${time}`;
  return `${monthDay(d, locale())} ${time}`;
}

/** A message's time beside it: when it was sent, "15:42" today, else with its day. */
export function messageTime(d: Date, now: Date): string {
  return sameDay(d, now) ? hourMinute(d) : shortTime(d, now) + " " + hourMinute(d);
}

/** A file's size as people read it. */
export const sizeText = (n: number) => (n >= 1024 * 1024 ? `${(n / 1024 / 1024).toFixed(1)} MB` : `${Math.max(1, Math.ceil(n / 1024))} KB`);

/** How long something took or has run, in its two largest units: "48 s", "2 min 14 s", "3 hr 5 min". */
export function durationText(ms: number): string {
  const s = Math.max(1, Math.round(ms / 1000));
  const part = (unit: "seconds" | "minutes" | "hours" | "days", count: number) => t(`time.${unit}`, { count });
  const pair = (big: string, small: string, rest: number) => (rest > 0 ? t("time.pair", { big, small }) : big);
  if (s < 60) return part("seconds", s);
  if (s < 3600) return pair(part("minutes", Math.trunc(s / 60)), part("seconds", s % 60), s % 60);
  if (s < 86400) return pair(part("hours", Math.trunc(s / 3600)), part("minutes", Math.trunc(s / 60) % 60), Math.trunc(s / 60) % 60);
  return pair(part("days", Math.trunc(s / 86400)), part("hours", Math.trunc(s / 3600) % 24), Math.trunc(s / 3600) % 24);
}

/** "412K", "1.8M". */
export function tokensText(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`;
  if (n >= 1000) return `${Math.round(n / 1000)}K`;
  return String(n);
}

/** The verb for a step: "Read", "Running"; a refused one is "Denied". */
export function toolVerb(tool: ToolView): string {
  if (tool.state === "denied") return t("steps.denied.done");
  if (tool.state === "cancelled") return t("steps.cancelled");
  if (tool.state === "awaiting_approval") return t("steps.awaiting");
  const live = tool.state === "running" || tool.state === "pending" || tool.state === "streaming";
  return t(`steps.${tool.category}.${live ? "doing" : "done"}`);
}

/** The verb for asking to do a step: "Run", "Edit". */
export function toolDo(category: ToolView["category"]): string {
  return t(`steps.${category}.do`);
}

export function permissionTitle(m: PermissionMode): string {
  return t(`permission.${m}.title`);
}

export function permissionDesc(m: PermissionMode): string {
  return t(`permission.${m}.desc`);
}

export function thinkingTitle(l: ThinkingLevel): string {
  return t(`thinking.${l}`);
}

/** Whether a model offers a thinking level; "default" (the provider decides) it always does. */
export function offersThinking(m: ModelSummary | undefined, l: ThinkingLevel): boolean {
  return l === "default" || !!m?.thinking_levels.includes(l);
}

/** The thinking level that goes with a model: `current` when the model offers it, else the level the model starts with. */
export function thinkingFor(m: ModelSummary | undefined, current: ThinkingLevel | null): ThinkingLevel {
  if (current && current !== "default" && offersThinking(m, current)) return current;
  return m?.default_thinking ?? "default";
}

/** A model's name as its provider lists it, else its short reference. */
export function modelTitle(ref: string, models: ModelsListResult): string {
  return models.models.find((m) => m.id === ref)?.name ?? modelName(ref);
}

/** A model menu's row; `note` says why the current model is there although the menu would leave it out. */
export type MenuModel = ModelSummary & { note?: "noKey" | "hidden" | "gone" };

/**
 * The models a model menu offers: those the environment can use (its model
 * connections are in its console), and first the current one when it is not.
 */
export function menuModels(models: ModelsListResult, current?: string | null): MenuModel[] {
  const list: MenuModel[] = models.models.filter((m) => m.available && m.shown);
  if (!current || list.some((m) => m.id === current)) return list;
  const found = models.models.find((m) => m.id === current);
  if (found) return [{ ...found, note: found.available ? "hidden" : "noKey" }, ...list];
  const provider = current.split("/")[0];
  const keyed = models.providers.some((p) => p.id === provider && p.credential !== "missing");
  const gone: MenuModel = {
    id: current,
    name: modelName(current),
    provider,
    context_window: 0,
    reasoning: false,
    thinking_levels: [],
    default_thinking: "default",
    images: false,
    available: keyed,
    shown: true,
    known: false,
    note: keyed ? "gone" : "noKey",
  };
  return [gone, ...list];
}

/** What a failed task's error kind means. */
export function failureTitle(kind: string): string {
  return i18n.exists(`her:failure.${kind}`) ? t(`failure.${kind}`) : t("failure.unknown");
}

/** What to do about it. */
export function failureAdvice(kind: string, retryable: boolean): string {
  if (i18n.exists(`her:advice.${kind}`)) return t(`advice.${kind}`);
  return retryable ? t("advice.retryable") : t("advice.unknown");
}

/** How many things, as the person reads it: 两件事, 2 things. */
export function thingsText(n: number): string {
  return n >= 1 && n <= 10 ? t(`things.${n}`) : t("things.many", { count: n });
}
