import assert from "node:assert/strict";
import { test } from "node:test";
import { RpcError } from "@hermesh/her-client";
import type { ProviderSummary } from "@hermesh/her-protocol";
import {
  checking,
  chooseChatGpt,
  chooseKey,
  clause,
  connectedName,
  controls,
  finish,
  folds,
  keyChoices,
  keyFailed,
  keyPage,
  keySaved,
  onboardingAfter,
  otherWay,
  pickProvider,
  providerName,
  signInEnded,
  start,
  withOnboarding,
  type Flow,
} from "./onboarding.ts";

/** A provider as models/list gives it: one that takes a key unless said. */
const provider = (id: string, name: string, o: Partial<ProviderSummary> = {}): ProviderSummary => ({ id, name, auth: "api_key", credential: "missing", ...o });

/** What was said, as "her:what" and "owner:what". */
const said = (f: Flow) => f.said.map((s) => `${s.who}:${s.what}`);

test("Her greets with the two ways, and the owner's choice of ChatGPT opens the browser", () => {
  const f = start(1);
  assert.deepEqual(said(f), ["her:greeting"]);
  assert.deepEqual([...controls(f)], [[0, "ways"]]);
  const signing = chooseChatGpt(f);
  assert.equal(signing.step, "signingIn");
  assert.deepEqual(said(signing), ["her:greeting", "owner:chatgpt", "her:browser"]);
  assert.deepEqual([...controls(signing)], [[2, "browser"]]);
  // A second click changes nothing while it signs in.
  assert.equal(chooseChatGpt(signing), signing);
});

test("a ChatGPT sign-in connects, fails or is stopped; the last two offer both ways again", () => {
  const signing = chooseChatGpt(start(1));
  const connected = signInEnded(signing, { state: "connected", account: "me@example.com" });
  assert.equal(connected.step, "connecting");
  assert.deepEqual(connected.connected, { kind: "chatgpt" });
  assert.deepEqual(connected.said.at(-1), { who: "her", what: "signedIn", account: "me@example.com" });
  assert.equal(controls(connected).size, 0);
  assert.deepEqual(signInEnded(signing, { state: "connected", account: "" }).said.at(-1), { who: "her", what: "signedIn", account: undefined });

  const failed = signInEnded(signing, { state: "failed", message: "access_denied" });
  assert.equal(failed.step, "choose");
  assert.deepEqual(failed.said.at(-1), { who: "her", what: "signInFailed", detail: "access_denied" });
  assert.deepEqual([...controls(failed)], [[3, "ways"]]);

  // Another way: the owner's words at once, the browser's buttons gone; Her answers once it has stopped.
  const stopping = otherWay(signing);
  assert.equal(stopping.step, "stopping");
  assert.deepEqual(said(stopping).slice(-1), ["owner:otherWay"]);
  assert.equal(controls(stopping).size, 0);
  const stopped = signInEnded(stopping, { state: "cancelled" });
  assert.deepEqual(said(stopped).slice(-2), ["owner:otherWay", "her:signInCancelled"]);
  assert.deepEqual([...controls(stopped)], [[4, "ways"]]);
  // It may have connected just before it stopped.
  assert.equal(signInEnded(stopping, { state: "connected" }).step, "connecting");
  // An end that comes once the onboarding is past the sign-in changes nothing.
  assert.equal(signInEnded(stopped, { state: "failed", message: "late" }), stopped);
  assert.equal(otherWay(stopped), stopped);
});

test("an API key: which provider, then its key; the providers stay under their question while it is asked", () => {
  const asking = chooseKey(start(1));
  assert.equal(asking.step, "provider");
  assert.deepEqual(said(asking), ["her:greeting", "owner:key", "her:whichProvider"]);
  assert.deepEqual([...controls(asking)], [[2, "providers"]]);

  const key = pickProvider(asking, "deepseek");
  assert.equal(key.provider, "deepseek");
  assert.deepEqual(key.said.slice(-2), [
    { who: "owner", what: "provider", provider: "deepseek" },
    { who: "her", what: "pasteKey", provider: "deepseek" },
  ]);
  assert.deepEqual([...controls(key)].sort(), [
    [2, "providers"],
    [4, "key"],
  ]);
  // Another provider instead: its key is asked for, under Her's newest message.
  const other = pickProvider(key, "openai");
  assert.equal(other.provider, "openai");
  assert.equal(controls(other).get(6), "key");
  assert.equal(controls(other).get(4), undefined);

  // While it is checked, nothing else is picked.
  const busy = checking(key);
  assert.equal(busy.step, "checking");
  assert.equal(pickProvider(busy, "openai"), busy);
  assert.equal(checking(busy), busy);
});

test("a key her saves, checked or not; one its provider refuses, or that fails, is asked for again", () => {
  const busy = checking(pickProvider(chooseKey(start(1)), "deepseek"));
  const saved = keySaved(busy, true);
  assert.equal(saved.step, "connecting");
  assert.deepEqual(saved.connected, { kind: "key", provider: "deepseek" });
  assert.equal(saved.said.length, busy.said.length);
  const unchecked = keySaved(busy, false);
  assert.deepEqual(unchecked.said.at(-1), { who: "her", what: "keyUnverified", provider: "deepseek" });
  assert.equal(unchecked.step, "connecting");

  const refused = keyFailed(busy, new RpcError({ code: -32021, message: "Authentication Fails, Your api key is invalid", data: { kind: "key_rejected" } }), "ignored");
  assert.equal(refused.step, "key");
  assert.deepEqual(refused.said.at(-1), { who: "her", what: "keyRejected", provider: "deepseek", message: "Authentication Fails, Your api key is invalid" });
  assert.equal(controls(refused).get(refused.said.length - 1), "key");

  const lost = keyFailed(busy, new RpcError({ code: -32099, message: "disconnected from her", data: { kind: "disconnected" } }), "disconnected from her");
  assert.deepEqual(lost.said.at(-1), { who: "her", what: "keyFailed", detail: "disconnected from her" });
  assert.deepEqual(keyFailed(busy, new Error("Mac mini can't be reached right now"), "Mac mini can't be reached right now").said.at(-1), {
    who: "her",
    what: "keyFailed",
    detail: "Mac mini can't be reached right now",
  });
  // An answer once it is past checking changes nothing.
  assert.equal(keySaved(saved, true), saved);
  assert.equal(keyFailed(saved, new Error("late"), "late"), saved);
});

test("the conversation opening on a model finishes it, from any step, once", () => {
  const busy = checking(pickProvider(chooseKey(start(1)), "deepseek"));
  for (const f of [start(1), busy, keySaved(busy, true), otherWay(chooseChatGpt(start(1)))]) {
    const done = finish(f, { model: "deepseek/deepseek-flash", session: "s1", after: null, at: 9 });
    assert.equal(done.step, "done");
    assert.equal(done.at, 9);
    assert.deepEqual(done.done, { model: "deepseek/deepseek-flash", session: "s1", after: null });
    assert.equal(controls(done).size, 0);
    assert.equal(finish(done, { model: "other/model", session: "s2", after: "x", at: 10 }), done);
    // Late answers change nothing.
    assert.equal(keySaved(done, true), done);
    assert.equal(signInEnded(done, { state: "connected" }), done);
  }
});

test("the providers offered: the best known first, then by name, without Token Plans, at most five", () => {
  const all = [
    provider("openrouter", "OpenRouter"),
    provider("mock", "Mock"),
    provider("glm", "Zhipu GLM"),
    provider("qwen", "Qwen"),
    provider("kimi", "Moonshot Kimi"),
    provider("google", "Google Gemini"),
    provider("anthropic", "Anthropic"),
    provider("openai", "OpenAI"),
    provider("deepseek", "DeepSeek"),
    provider("deepseek-anthropic", "DeepSeek (Anthropic-compatible endpoint)"),
    provider("bailian-token-plan", "Bailian Token Plan", { plan: { api: "openai-chat", base_url: "https://example.invalid", dynamic_models: false, key_url: "https://example.invalid/keys" } }),
    provider("ollama", "Ollama (local)", { auth: "none", credential: "not_required" }),
    provider("local-chatgpt-api-0123456789ab", "ChatGPT", { auth: "connection", credential: "connection" }),
  ];
  assert.deepEqual(keyChoices(all).map((p) => p.id), ["deepseek", "openai", "anthropic", "google", "kimi"]);
  // With fewer of them, the rest come by name.
  assert.deepEqual(keyChoices([provider("zeta", "Zeta"), provider("mock", "Mock"), provider("gemini", "Gemini"), provider("glm", "GLM"), provider("alpha", "Alpha")]).map((p) => p.id), [
    "gemini",
    "glm",
    "alpha",
    "mock",
    "zeta",
  ]);
  assert.deepEqual(keyChoices([]), []);
});

test("a provider's name in the app's language, else its own; the key pages the app knows", () => {
  const kimi = provider("kimi", "Moonshot Kimi", { names: { zh: "月之暗面 Kimi", en: "Kimi" } });
  assert.equal(providerName(kimi, "zh-CN"), "月之暗面 Kimi");
  assert.equal(providerName(kimi, "en"), "Kimi");
  assert.equal(providerName(provider("deepseek", "DeepSeek"), "zh-CN"), "DeepSeek");
  assert.equal(keyPage("deepseek"), "https://platform.deepseek.com/api_keys");
  assert.equal(keyPage("openai"), "https://platform.openai.com/api-keys");
  assert.equal(keyPage("anthropic"), "https://console.anthropic.com/settings/keys");
  assert.equal(keyPage("google"), "https://aistudio.google.com/apikey");
  assert.equal(keyPage("kimi"), undefined);
  assert.equal(keyPage("mock"), undefined);
});

test("an error's own words go inside Her's sentence when short, and fold away when long", () => {
  assert.equal(folds("Authentication Fails, Your api key is invalid"), false);
  assert.equal(folds("Incorrect API key provided: sk-proj-****abcd. You can find your API key at https://platform.openai.com/account/api-keys."), true);
  assert.equal(folds("line one\nline two"), true);
  assert.equal(clause("Authentication Fails, Your api key is invalid."), "Authentication Fails, Your api key is invalid");
  assert.equal(clause(" 密钥无效。 "), "密钥无效");
  assert.equal(clause("no full stop"), "no full stop");
});

test("what the line says is connected: ChatGPT, the provider of the key, else the provider of Her's model", () => {
  const providers = [provider("deepseek", "DeepSeek"), provider("local-chatgpt-api-0123456789ab", "ChatGPT", { auth: "connection" }), provider("kimi", "Moonshot Kimi", { names: { zh: "Kimi" } })];
  const done = (f: Flow, model: string) => finish(f, { model, session: "s", after: null, at: 2 });
  const signed = signInEnded(chooseChatGpt(start(1)), { state: "connected" });
  assert.equal(connectedName(done(signed, "local-chatgpt-api-0123456789ab/gpt-5"), providers, "en"), "ChatGPT");
  const keyed = keySaved(checking(pickProvider(chooseKey(start(1)), "kimi")), true);
  assert.equal(connectedName(done(keyed, "kimi/kimi-k3.1-pro"), providers, "zh-CN"), "Kimi");
  // Set up in the console: the provider of the model the conversation opened on.
  assert.equal(connectedName(done(start(1), "deepseek/deepseek-flash"), providers, "en"), "DeepSeek");
  assert.equal(connectedName(done(start(1), "custom/model"), providers, "en"), "custom");
});

test("the onboarding's place: last while under way, where it was once done, in its own conversation only", () => {
  const items = [{ key: "a" }, { key: "b" }];
  const row = { key: "onboarding" };
  const keys = (after: string | null | undefined, list = items) => withOnboarding(list, after, row).map((i) => i.key);
  const under = chooseKey(start(1));
  // Under way: the last thing, while no model works there, and while one connected is coming.
  assert.deepEqual(keys(onboardingAfter(under, { noModel: true, session: "s", last: "b" })), ["a", "b", "onboarding"]);
  assert.deepEqual(keys(onboardingAfter(under, { noModel: true, session: null, last: null }), []), ["onboarding"]);
  assert.equal(onboardingAfter(under, { noModel: false, session: "s", last: "b" }), undefined);
  const coming = keySaved(checking(pickProvider(under, "deepseek")), true);
  assert.equal(onboardingAfter(coming, { noModel: false, session: "s", last: "b" }), "b");
  // Done: where it was when the conversation opened, before what came after.
  const done = finish(coming, { model: "deepseek/deepseek-flash", session: "s", after: null, at: 2 });
  assert.deepEqual(keys(onboardingAfter(done, { noModel: false, session: "s", last: "b" })), ["onboarding", "a", "b"]);
  const later = finish(coming, { model: "deepseek/deepseek-flash", session: "s", after: "a", at: 2 });
  assert.deepEqual(keys(onboardingAfter(later, { noModel: false, session: "s", last: "b" })), ["a", "onboarding", "b"]);
  // A cleared conversation starts without it; so does one whose place is gone.
  assert.equal(onboardingAfter(done, { noModel: false, session: "s2", last: null }), undefined);
  assert.deepEqual(keys("gone"), ["a", "b"]);
  assert.equal(onboardingAfter(null, { noModel: true, session: "s", last: "b" }), undefined);
});
