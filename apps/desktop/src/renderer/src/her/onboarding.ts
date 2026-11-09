import { RpcError } from "@hermesh/her-client";
import type { ProviderSummary, TokenPlanSummary } from "@hermesh/her-protocol";

// Her's onboarding while the environment shown has no model that works (docs/her-server.md
// §9.2): Her speaks first, but no model can speak yet, so the app writes every message on
// Her's behalf and never sends any of it to her or keeps it in her's conversation. Here: what
// is said, the steps between, the providers offered and in what order, and what Her says when
// something fails. The page draws it (ModelOnboarding.tsx).

/** A message of the onboarding: Her's, as the app writes it, or what the owner chose. */
export type Said =
  | { who: "her"; what: "greeting" | "browser" | "signInCancelled" | "whichProvider" | "whichPlan" | "whichWay" }
  | { who: "her"; what: "signedIn"; account?: string }
  | { who: "her"; what: "signInFailed" | "keyFailed"; detail: string }
  | { who: "her"; what: "pasteKey" | "keyUnverified"; provider: string }
  | { who: "her"; what: "keyRejected"; provider: string; message: string }
  | { who: "owner"; what: "chatgpt" | "plan" | "key" | "otherWay" }
  | { who: "owner"; what: "provider"; provider: string };

/**
 * Where it is: choosing a way; the browser's ChatGPT sign-in under way, or being stopped; choosing
 * a provider for a key, or a Token Plan; its key asked for, or being checked; connected, until the
 * conversation opens on a model; done.
 */
export type Step = "choose" | "signingIn" | "stopping" | "provider" | "plan" | "key" | "checking" | "connecting" | "done";

export type Flow = {
  said: Said[];
  step: Step;
  /** When it began; once done, when that was. */
  at: number;
  /** The provider (or Token Plan) whose key is asked for. */
  provider?: string;
  /** What it connected. */
  connected?: { kind: "chatgpt" } | { kind: "key"; provider: string };
  /** Once done: the model Her talks with, and where the onboarding stays: in that conversation, after its message `after` (first for null). */
  done?: { model: string; session: string | null; after: string | null };
};

/** How a ChatGPT sign-in ended (shared/herSignin.ts). */
export type SignInEnd = { state: "connected" | "failed" | "cancelled"; account?: string; message?: string };

export const start = (at: number): Flow => ({ said: [{ who: "her", what: "greeting" }], step: "choose", at });

// Each way on gives the flow after it, or the same flow when it does not apply now: an answer
// that comes after the onboarding moved on changes nothing.

export function chooseChatGpt(f: Flow): Flow {
  if (f.step !== "choose") return f;
  return { ...f, step: "signingIn", said: [...f.said, { who: "owner", what: "chatgpt" }, { who: "her", what: "browser" }] };
}

/**
 * Another way: while the browser's sign-in is under way, it is stopped, and Her answers once it
 * has; while a provider, a Token Plan or a key is asked for, Her asks which way at once.
 */
export function otherWay(f: Flow): Flow {
  if (f.step === "signingIn") return { ...f, step: "stopping", said: [...f.said, { who: "owner", what: "otherWay" }] };
  if (f.step !== "provider" && f.step !== "plan" && f.step !== "key") return f;
  return { ...f, step: "choose", provider: undefined, said: [...f.said, { who: "owner", what: "otherWay" }, { who: "her", what: "whichWay" }] };
}

export function signInEnded(f: Flow, r: SignInEnd): Flow {
  if (f.step !== "signingIn" && f.step !== "stopping") return f;
  if (r.state === "connected") return { ...f, step: "connecting", connected: { kind: "chatgpt" }, said: [...f.said, { who: "her", what: "signedIn", account: r.account || undefined }] };
  const said: Said = r.state === "cancelled" ? { who: "her", what: "signInCancelled" } : { who: "her", what: "signInFailed", detail: r.message ?? "" };
  return { ...f, step: "choose", said: [...f.said, said] };
}

export function choosePlan(f: Flow): Flow {
  if (f.step !== "choose") return f;
  return { ...f, step: "plan", said: [...f.said, { who: "owner", what: "plan" }, { who: "her", what: "whichPlan" }] };
}

export function chooseKey(f: Flow): Flow {
  if (f.step !== "choose") return f;
  return { ...f, step: "provider", said: [...f.said, { who: "owner", what: "key" }, { who: "her", what: "whichProvider" }] };
}

/** A provider or a Token Plan picked: from the question, or another one while a key is asked for. */
export function pickProvider(f: Flow, provider: string): Flow {
  if (f.step !== "provider" && f.step !== "plan" && f.step !== "key") return f;
  return { ...f, step: "key", provider, said: [...f.said, { who: "owner", what: "provider", provider }, { who: "her", what: "pasteKey", provider }] };
}

export const checking = (f: Flow): Flow => (f.step === "key" ? { ...f, step: "checking" } : f);

/** her saved the key; one it could not check with the provider is said to be so. */
export function keySaved(f: Flow, verified: boolean): Flow {
  if (f.step !== "checking" || !f.provider) return f;
  const said: Said[] = verified ? f.said : [...f.said, { who: "her", what: "keyUnverified", provider: f.provider }];
  return { ...f, step: "connecting", connected: { kind: "key", provider: f.provider }, said };
}

/** her did not save the key: its provider refused it, saying why, or it failed otherwise (`text`, the error as the page reads it). The key is asked for again. */
export function keyFailed(f: Flow, e: unknown, text: string): Flow {
  if (f.step !== "checking" || !f.provider) return f;
  const said: Said = e instanceof RpcError && e.kind === "key_rejected" ? { who: "her", what: "keyRejected", provider: f.provider, message: e.message } : { who: "her", what: "keyFailed", detail: text };
  return { ...f, step: "key", said: [...f.said, said] };
}

/** The conversation opened on a model, however it was connected (the console's too). */
export function finish(f: Flow, d: { model: string; session: string | null; after: string | null; at: number }): Flow {
  if (f.step === "done") return f;
  return { ...f, step: "done", at: d.at, done: { model: d.model, session: d.session, after: d.after } };
}

/** What goes under one of Her's messages: the ways, the sign-in's, the providers, the Token Plans, or the key's field. */
export type Control = "ways" | "browser" | "providers" | "plans" | "key";

/** Which of Her's messages get controls, by index: Her's last; while a key is asked for, its question keeps its choices. */
export function controls(f: Flow): Map<number, Control> {
  const at = new Map<number, Control>();
  const last = f.said.length - 1;
  switch (f.step) {
    case "choose":
      at.set(last, "ways");
      break;
    case "signingIn":
      at.set(last, "browser");
      break;
    case "provider":
      at.set(last, "providers");
      break;
    case "plan":
      at.set(last, "plans");
      break;
    case "key":
    case "checking": {
      const question = f.said.findLastIndex((s) => s.what === "whichProvider" || s.what === "whichPlan");
      if (question >= 0) at.set(question, f.said[question].what === "whichPlan" ? "plans" : "providers");
      at.set(last, "key");
      break;
    }
  }
  return at;
}

/** The providers people know best, first, in this order (Google's is `google`, or `gemini`). */
const known = ["deepseek", "openai", "anthropic", "google", "gemini", "kimi", "qwen", "glm"];
const rank = (p: ProviderSummary) => (known.includes(p.id) ? known.indexOf(p.id) : known.length);

/** How many providers are offered for a key; the others are in the console. Every Token Plan is offered. */
export const OFFERED = 5;

/**
 * The providers offered for a key: those that take one and aren't Token Plans, the best known
 * first, then by name. The Anthropic-compatible DeepSeek endpoint is the same account as DeepSeek.
 */
export function keyChoices(providers: ProviderSummary[]): ProviderSummary[] {
  return providers
    .filter((p) => p.auth === "api_key" && !p.plan && p.id !== "deepseek-anthropic")
    .sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name))
    .slice(0, OFFERED);
}

/** The Token Plans in the console's order (her/apps/console providers.tsx), then by name. */
const plans = ["bailian-token-plan", "tencent-token-plan", "volcengine-agent-plan", "volcengine-coding-plan", "zai-coding-plan", "zhipu-coding-plan"];
const planRank = (p: ProviderSummary) => (plans.includes(p.id) ? plans.indexOf(p.id) : plans.length);

/** The Token Plans offered: all that her knows. */
export function planChoices(providers: ProviderSummary[]): ProviderSummary[] {
  return providers.filter((p) => p.plan).sort((a, b) => planRank(a) - planRank(b) || a.name.localeCompare(b.name));
}

/** A provider's name in the app's language (zh-CN takes `zh`), else its own. */
export function providerName(p: Pick<ProviderSummary, "name" | "names">, language: string): string {
  return p.names?.[language] ?? p.names?.[language.split("-")[0]] ?? p.name;
}

/** Where keys are made, for the providers the app knows. */
const keyPages: Record<string, string> = {
  deepseek: "https://platform.deepseek.com/api_keys",
  openai: "https://platform.openai.com/api-keys",
  anthropic: "https://console.anthropic.com/settings/keys",
  google: "https://aistudio.google.com/apikey",
};

/** Where a key is made: a Token Plan's own page, else the one the app knows for the provider. */
export const keyPage = (provider: string, plan?: Pick<TokenPlanSummary, "key_url"> | null): string | undefined => plan?.key_url || keyPages[provider];

/** Whether an error's own words are long enough to fold away under Her's. */
export const folds = (text: string) => text.length > 60 || text.includes("\n");

/** An error's words to go inside Her's sentence: without their full stop. */
export const clause = (text: string) => text.trim().replace(/[.。!！]+$/, "");

/**
 * What the onboarding's line says is connected: ChatGPT, or the provider (or Token Plan) whose key
 * was saved, else the provider of the model Her talks with; `name` names a provider by its id.
 */
export function connectedName(f: Flow, name: (provider: string) => string): string {
  if (f.connected?.kind === "chatgpt") return "ChatGPT";
  return name(f.connected?.kind === "key" ? f.connected.provider : (f.done?.model.split("/")[0] ?? ""));
}

/**
 * Where the onboarding goes in the conversation: after the message with this key, first for
 * null; undefined when it does not show. Under way, it is the last thing, while no model works
 * there and while one connected is coming. Done, it stays where it was, in its own conversation
 * only (a cleared one starts without it).
 */
export function onboardingAfter(f: Flow | null, o: { noModel: boolean; session: string | null; last: string | null }): string | null | undefined {
  if (!f) return undefined;
  if (f.step === "done") return f.done && f.done.session === o.session ? f.done.after : undefined;
  return o.noModel || f.step === "connecting" ? o.last : undefined;
}

/** The conversation's items with the onboarding's row in its place; without it when it does not show, or its place is gone. */
export function withOnboarding<T extends { key: string }, R>(items: T[], after: string | null | undefined, row: R): (T | R)[] {
  if (after === undefined) return items;
  const at = after === null ? 0 : items.findIndex((i) => i.key === after) + 1;
  if (after !== null && at === 0) return items;
  return [...items.slice(0, at), row, ...items.slice(at)];
}
