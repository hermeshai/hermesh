export const MAX_DIAGRAM_LENGTH = 20000;

const DISALLOWED = /%%\s*\{|^\s*---|\bclick\s|https?:|data:|javascript:|url\s*\(|\bimg\s*:|\bicon\s*:|@import/im;

export function validDiagram(source: string) {
  // Theme/configuration comes from the host, never from model output. Only local
  // SVG geometry is displayed, with no URL or click callback support.
  return source.length <= MAX_DIAGRAM_LENGTH && !DISALLOWED.test(source);
}

/**
 * Escapes HTML-like angle brackets (`<id>`, `Map<K, V>`, `<script>`) as Mermaid
 * `#lt;` / `#gt;` entities in a single linear pass so DOMPurify neither strips
 * placeholders nor receives raw tags, while keeping `<<stereotype>>` annotations,
 * `<br/>` line breaks and Mermaid arrows untouched.
 */
export function prepareDiagram(source: string) {
  if (!source.includes("<")) return source;
  let out = "";
  let copied = 0;
  let inQuote = false;
  let open = 0;
  let commentEnd = -1;
  let noCommentEnd = false;
  for (let i = 0, n = source.length; i < n; i++) {
    const ch = source[i];
    if (ch === "\n") {
      inQuote = false;
      open = 0;
      commentEnd = -1;
      noCommentEnd = false;
    } else if (i === commentEnd) {
      out += source.slice(copied, i) + "#gt;";
      copied = i + 1;
      commentEnd = -1;
    } else if (ch === '"' && (inQuote || open === 0)) {
      inQuote = !inQuote;
      open = 0;
    } else if (!inQuote && (ch === "[" || ch === "]" || ch === "{" || ch === "}")) {
      open = 0;
    } else if (ch === "<") {
      if (source.startsWith("!--", i + 1)) {
        if (!noCommentEnd && i > commentEnd) {
          const nl = source.indexOf("\n", i + 4);
          const close = source.indexOf("-->", i + 4);
          if (close !== -1 && (nl === -1 || close < nl)) commentEnd = close + 2;
          else noCommentEnd = true;
        }
        out += source.slice(copied, i) + "#lt;";
        copied = i + 1;
        continue;
      }
      if (source[i + 1] === "<") {
        let j = i + 2;
        while (j < n && (source[j] === " " || source[j] === "\t")) j++;
        if (j < n && /[a-zA-Z]/.test(source[j])) {
          let k = j + 1;
          while (k < n && /[\w -]/.test(source[k])) k++;
          if (source[k] === ">" && source[k + 1] === ">") {
            i = k + 1;
            continue;
          }
        }
      }
      if ((source[i + 1] === "b" || source[i + 1] === "B") && (source[i + 2] === "r" || source[i + 2] === "R")) {
        let j = i + 3;
        while (j < n && (source[j] === " " || source[j] === "\t")) j++;
        if (source[j] === "/") j++;
        if (source[j] === ">") {
          if (j - i !== 4 || source.slice(i, j + 1) !== "<br/>") {
            out += source.slice(copied, i) + "<br/>";
            copied = j + 1;
          }
          i = j;
          continue;
        }
      }
      const next = source[i + 1];
      if (next && /[a-zA-Z/!?]/.test(next)) {
        out += source.slice(copied, i) + "#lt;";
        copied = i + 1;
        open++;
      }
    } else if (ch === ">" && open > 0) {
      const prev = source[i - 1];
      if (!inQuote && (prev === "-" || prev === "=" || prev === "." || prev === "|")) {
        open = 0;
      } else {
        out += source.slice(copied, i) + "#gt;";
        copied = i + 1;
        open--;
      }
    }
  }
  return copied === 0 ? source : out + source.slice(copied);
}


