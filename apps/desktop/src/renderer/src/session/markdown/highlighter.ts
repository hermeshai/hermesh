import { createHighlighterCoreSync, type GrammarState, type ThemedToken } from "shiki/core";
import { createJavaScriptRegexEngine } from "shiki/engine/javascript";
import { bundledLanguages, bundledLanguagesAlias, type BundledLanguage } from "shiki/langs";
import { themes } from "./theme.ts";

export const MAX_CODE_BYTES = 200 * 1024;
type Entry = { lang: string; complete: string; tokens: ThemedToken[][]; state?: GrammarState };

/** One instance per worker, with bounded completed-line state. */
export function createCodeHighlighter() {
  const core = createHighlighterCoreSync({ themes, langs: [], engine: createJavaScriptRegexEngine({ forgiving: true }) });
  const loads = new Map<string, Promise<void>>();
  const cache = new Map<string, Entry>();
  return async (id: string, text: string, language: string): Promise<ThemedToken[][] | null> => {
    const lang = language.toLowerCase();
    const grammar = bundledLanguages[lang as BundledLanguage] ?? bundledLanguagesAlias[lang as keyof typeof bundledLanguagesAlias];
    if (!grammar || new Blob([text]).size > MAX_CODE_BYTES || text.split("\n").some((line) => line.length > 10000)) return null;
    let load = loads.get(lang);
    if (!load) {
      load = core.loadLanguage(grammar).catch((e) => { loads.delete(lang); throw e; });
      loads.set(lang, load);
    }
    await load;
    const previous = cache.get(id);
    const old = previous?.lang === lang && text.startsWith(previous.complete) ? previous : undefined;
    const end = text.lastIndexOf("\n") + 1;
    const complete = text.slice(0, end);
    const start = old?.complete.length ?? 0;
    // A per-line timeout can return a partial grammar state during cold regex
    // compilation, coloring subsequent lines as strings/comments. The host's
    // 2.5s worker watchdog provides a hard budget and plain-text fallback instead.
    const options = { lang, themes: { light: "hermesh-light", dark: "hermesh-dark" }, defaultColor: false as const, tokenizeTimeLimit: 0 };
    let tokens = old?.tokens ?? [];
    let state = old?.state;
    if (end > start) {
      const result = core.codeToTokens(text.slice(start, end), { ...options, grammarState: state });
      tokens = [...tokens, ...result.tokens.slice(0, -1)];
      state = core.getLastGrammarState(result.tokens);
    }
    cache.delete(id);
    cache.set(id, { lang, complete, tokens, state });
    let bytes = [...cache.values()].reduce((n, e) => n + e.complete.length * 2, 0);
    while (cache.size > 32 || bytes > 2 * 1024 * 1024) {
      const key = cache.keys().next().value!;
      bytes -= cache.get(key)!.complete.length * 2;
      cache.delete(key);
    }
    return [...tokens, ...core.codeToTokens(text.slice(end), { ...options, grammarState: state }).tokens];
  };
}
