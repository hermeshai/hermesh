// Code in GitHub's colours for the iPhone app: the desktop app's Shiki,
// run in JavaScriptCore (Code.swift). Each language is read the first time
// it is shown, through readLanguage, which Swift provides.
import { createHighlighterCoreSync } from "shiki/core";
import { createJavaScriptRegexEngine } from "shiki/engine/javascript";
import dark from "shiki/themes/github-dark-default.mjs";
import light from "shiki/themes/github-light-default.mjs";

const highlighter = createHighlighterCoreSync({ themes: [light, dark], langs: [], engine: createJavaScriptRegexEngine({ forgiving: true }) });
/** Each language's id by its id and aliases. */
let names;
const loaded = new Set();

function load(name) {
  names ??= JSON.parse(globalThis.readLanguage("index"));
  const id = names[name.toLowerCase()];
  if (!id || loaded.has(id)) return id;
  const grammar = JSON.parse(globalThis.readLanguage(id));
  loaded.add(id);
  for (const dep of grammar.embeddedLangs ?? []) load(dep);
  highlighter.loadLanguageSync(grammar);
  return id;
}

/**
 * The code's lines as tokens, [text, light colour, dark colour, font style]
 * each, as JSON; null for a language Shiki doesn't know.
 */
export function highlight(code, lang) {
  const id = load(lang);
  if (!id) return null;
  const { tokens } = highlighter.codeToTokens(code, { lang: id, themes: { light: light.name, dark: dark.name }, defaultColor: false });
  return JSON.stringify(tokens.map((line) => line.map((t) => [t.content, t.htmlStyle?.["--shiki-light"] ?? "", t.htmlStyle?.["--shiki-dark"] ?? "", t.fontStyle ?? 0])));
}
