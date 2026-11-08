import type { Processor } from "unified";
import type { Construct, State } from "micromark-util-types";
import type { Extension } from "mdast-util-from-markdown";

declare module "micromark-util-types" { interface TokenTypeMap { texMath: "texMath" } }

/** TeX delimiters are recognized by the Markdown tokenizer, so code spans,
 * escaped backslashes and link destinations are never rewritten. */
const construct: Construct = {
  name: "texMath",
  tokenize(effects, ok, nok) {
    let close = 0;
    let size = 0;
    function start(code: Parameters<State>[0]): ReturnType<State> {
      effects.enter("texMath"); effects.consume(code); return opening;
    }
    const opening: State = (code) => {
      if (code !== 40 && code !== 91) return nok(code);
      close = code === 40 ? 41 : 93;
      effects.consume(code); return content;
    };
    const content: State = (code) => {
      if (code === null || ++size > 5000 || (close === 41 && code < -2)) return nok(code);
      if (code === 92) { effects.consume(code); return escape; }
      effects.consume(code); return content;
    };
    const escape: State = (code) => {
      if (code === close) { effects.consume(code); effects.exit("texMath"); return ok; }
      return content(code);
    };
    return start;
  },
};
const extension: Extension = {
  enter: { texMath(token) {
    this.enter({ type: "inlineMath", value: "" }, token);
  } },
  exit: { texMath(token) {
    const raw = this.sliceSerialize(token);
    const value = raw.slice(2, -2).trim();
    const node = this.stack.at(-1);
    if (node?.type === "inlineMath") {
      node.value = value;
      node.data = { hName: "code", hProperties: { className: ["language-math", raw.startsWith("\\[") ? "math-display" : "math-inline"] }, hChildren: [{ type: "text", value }] };
    }
    this.exit(token);
  } },
};
export function texMath(this: Processor) {
  const data = this.data();
  (data.micromarkExtensions ??= []).push({ text: { 92: construct } });
  (data.fromMarkdownExtensions ??= []).push(extension);
}
