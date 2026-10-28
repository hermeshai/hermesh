import { useEffect, useMemo, useState, type CSSProperties } from "react";
import { createHighlighterCoreSync, type HighlighterCore, type ThemedToken } from "shiki/core";
import { createJavaScriptRegexEngine } from "shiki/engine/javascript";
import { bundledLanguages, type BundledLanguage } from "shiki/langs";
import dark from "shiki/themes/github-dark-default.mjs";
import light from "shiki/themes/github-light-default.mjs";

/**
 * Code in GitHub's colours, light or dark with the app (Shiki). Plain text
 * until its language has loaded, each one the first time it is shown, and
 * plain for a language Shiki doesn't know.
 */
export function Code({ text, lang }: { text: string; lang: string }) {
  const [loads, setLoads] = useState(0);
  const lines = useMemo(() => highlight(text, lang), [text, lang, loads]);
  const ready = lines !== undefined;
  useEffect(() => {
    if (!ready) load(lang)?.then(() => setLoads((n) => n + 1));
  }, [lang, ready]);
  if (!lines) return text;
  return (
    <span className="hl">
      {lines.map((line, i) => (
        <span key={i}>
          {i > 0 && "\n"}
          {line.map((t, j) => (
            <span key={j} style={t.htmlStyle as CSSProperties}>
              {t.content}
            </span>
          ))}
        </span>
      ))}
    </span>
  );
}

let highlighter: HighlighterCore | undefined;
const loading = new Map<BundledLanguage, Promise<void>>();
const loaded = new Set<BundledLanguage>();

/** A fence's language as Shiki names it ("go", "sh", "TS"), or undefined. */
function known(lang: string): BundledLanguage | undefined {
  const l = lang.toLowerCase();
  return Object.hasOwn(bundledLanguages, l) ? (l as BundledLanguage) : undefined;
}

function core(): HighlighterCore {
  highlighter ??= createHighlighterCoreSync({ themes: [light, dark], langs: [], engine: createJavaScriptRegexEngine({ forgiving: true }) });
  return highlighter;
}

function load(lang: string): Promise<void> | undefined {
  const l = known(lang);
  if (!l) return undefined;
  let p = loading.get(l);
  if (!p) {
    p = core()
      .loadLanguage(bundledLanguages[l])
      .then(() => void loaded.add(l));
    loading.set(l, p);
  }
  return p;
}

function highlight(text: string, lang: string): ThemedToken[][] | undefined {
  const l = known(lang);
  if (!l || !loaded.has(l)) return undefined;
  return core().codeToTokens(text, { lang: l, themes: { light: light.name!, dark: dark.name! }, defaultColor: false }).tokens;
}
