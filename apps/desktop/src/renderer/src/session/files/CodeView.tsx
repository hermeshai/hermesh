import { useEffect, useLayoutEffect, useRef } from "react";
import { defaultKeymap } from "@codemirror/commands";
import { foldGutter, foldKeymap, HighlightStyle, LanguageDescription, syntaxHighlighting } from "@codemirror/language";
import { languages } from "@codemirror/language-data";
import { highlightSelectionMatches, openSearchPanel, searchKeymap } from "@codemirror/search";
import { Compartment, EditorSelection, EditorState, StateEffect, StateField, type Extension } from "@codemirror/state";
import { Decoration, drawSelection, EditorView, highlightActiveLineGutter, highlightSpecialChars, keymap, lineNumbers, type DecorationSet } from "@codemirror/view";
import { tags } from "@lezer/highlight";
import { i18n } from "../../../../shared/i18n";

// A file's text as code (docs/session-files.md §3.4): FinxWork's CodeMirror editor (files/Editor.tsx),
// read-only here, loaded the first time a file is opened. The lines a reply pointed at are marked and
// brought into view; ⌘F finds in it.

const colours = HighlightStyle.define([
  { tag: [tags.keyword, tags.modifier, tags.operatorKeyword, tags.controlKeyword, tags.definitionKeyword, tags.moduleKeyword], color: "var(--syn-keyword)" },
  { tag: [tags.string, tags.special(tags.string), tags.regexp, tags.character], color: "var(--syn-string)" },
  { tag: [tags.comment, tags.lineComment, tags.blockComment, tags.docComment], color: "var(--syn-comment)", fontStyle: "italic" },
  { tag: [tags.number, tags.bool, tags.null, tags.atom, tags.constant(tags.name), tags.standard(tags.name)], color: "var(--syn-constant)" },
  { tag: [tags.function(tags.variableName), tags.function(tags.propertyName), tags.macroName], color: "var(--syn-function)" },
  { tag: [tags.typeName, tags.className, tags.namespace, tags.tagName, tags.self], color: "var(--syn-type)" },
  { tag: [tags.propertyName, tags.attributeName, tags.labelName], color: "var(--syn-property)" },
  { tag: [tags.heading], color: "var(--syn-keyword)", fontWeight: "600" },
  { tag: [tags.link, tags.url], color: "var(--syn-string)", textDecoration: "underline" },
  { tag: [tags.emphasis], fontStyle: "italic" },
  { tag: [tags.strong], fontWeight: "600" },
  { tag: [tags.invalid], color: "var(--syn-invalid)" },
]);

const look = EditorView.theme({
  "&": { height: "100%", fontSize: "12.5px", color: "var(--l1)", backgroundColor: "var(--win)" },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": { fontFamily: "var(--mono)", lineHeight: "1.6" },
  ".cm-content": { paddingBlock: "8px" },
  ".cm-gutters": { backgroundColor: "var(--win)", color: "var(--l3)", border: "none" },
  ".cm-lineNumbers .cm-gutterElement": { padding: "0 10px 0 14px" },
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--l2)" },
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": { backgroundColor: "var(--sel)" },
  ".cm-selectionMatch": { backgroundColor: "var(--fill2)" },
  ".cm-foldGutter .cm-gutterElement": { color: "var(--l3)", cursor: "pointer" },
  ".cm-target": { backgroundColor: "var(--target)" },
  ".cm-panels": { backgroundColor: "var(--win2)", color: "var(--l1)", borderColor: "var(--sep)" },
  ".cm-panels-bottom": { borderTop: ".5px solid var(--sep)" },
  ".cm-searchMatch": { backgroundColor: "var(--target)" },
  ".cm-searchMatch-selected": { backgroundColor: "var(--sel)" },
  ".cm-textfield": { backgroundColor: "var(--field)", border: ".5px solid var(--field-bd)", borderRadius: "var(--r-xs)", color: "var(--l1)" },
  ".cm-button": { backgroundImage: "none", backgroundColor: "var(--btn)", border: "none", boxShadow: "var(--btn-sh)", borderRadius: "var(--r-xs)", color: "var(--l1)" },
});

/** The search panel's words in the app's language (CodeMirror's own are English). */
const phrases = (): Extension =>
  i18n.language.startsWith("zh")
    ? EditorState.phrases.of({ Find: "查找", next: "下一个", previous: "上一个", all: "全部", "match case": "区分大小写", regexp: "正则表达式", "by word": "全词匹配", close: "关闭", "Go to line": "跳到行", go: "前往", "Fold line": "折叠", "Unfold line": "展开" })
    : [];

/** The lines a reply pointed at. */
const mark = StateEffect.define<{ from: number; to: number }>();
const marked = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(lines, tr) {
    for (const e of tr.effects) {
      if (!e.is(mark)) continue;
      const ranges = [];
      for (let n = tr.state.doc.lineAt(e.value.from).number; n <= tr.state.doc.lineAt(e.value.to).number; n++) ranges.push(Decoration.line({ class: "cm-target" }).range(tr.state.doc.line(n).from));
      lines = Decoration.set(ranges);
    }
    return lines;
  },
  provide: (f) => EditorView.decorations.from(f),
});

const language = new Compartment();

export default function CodeView({ text, name, line, endLine, wrap }: { text: string; name: string; line?: number; endLine?: number; wrap: boolean }) {
  const box = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);

  useLayoutEffect(() => {
    const v = new EditorView({
      parent: box.current!,
      state: EditorState.create({
        doc: text,
        extensions: [
          lineNumbers(),
          highlightActiveLineGutter(),
          highlightSpecialChars(),
          foldGutter(),
          drawSelection(),
          syntaxHighlighting(colours),
          highlightSelectionMatches(),
          keymap.of([...defaultKeymap, ...searchKeymap, ...foldKeymap]),
          text.includes("\r\n") ? EditorState.lineSeparator.of("\r\n") : [],
          EditorState.readOnly.of(true),
          EditorView.editable.of(true),
          language.of([]),
          wrap ? EditorView.lineWrapping : [],
          marked,
          look,
          phrases(),
        ],
      }),
    });
    view.current = v;
    const found = LanguageDescription.matchFilename(languages, name);
    if (found) void found.load().then((support) => view.current === v && v.dispatch({ effects: language.reconfigure(support) }));
    // ⌘F is the app's Search: while the code has the focus, it finds in the code.
    const find = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "f" && box.current?.contains(document.activeElement)) {
        e.preventDefault();
        openSearchPanel(v);
      }
    };
    window.addEventListener("keydown", find, true);
    return () => {
      window.removeEventListener("keydown", find, true);
      v.destroy();
      view.current = null;
    };
  }, [text, name, wrap]);

  useEffect(() => {
    const v = view.current;
    if (!v || line === undefined) return;
    const doc = v.state.doc;
    const from = doc.line(Math.min(Math.max(1, line), doc.lines));
    const to = doc.line(Math.min(Math.max(from.number, endLine ?? line), doc.lines));
    v.dispatch({ selection: EditorSelection.cursor(from.from), effects: [mark.of({ from: from.from, to: to.from }), EditorView.scrollIntoView(from.from, { y: "start", yMargin: 80 })] });
  }, [text, line, endLine]);

  return <div className="sfv-code" ref={box} data-testid="file-code" />;
}
