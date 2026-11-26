import { useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../icons";
import { copyText } from "./content";
import { parseTerminalLines, stripTerminalEscapes, type AnsiSpanStyle } from "./terminalText";

function spanClass(s: AnsiSpanStyle | undefined): string | undefined {
  if (!s) return undefined;
  const cls: string[] = [];
  if (s.fgClass) cls.push(s.fgClass);
  if (s.bold) cls.push("ansi-b");
  if (s.dim) cls.push("ansi-d");
  if (s.italic) cls.push("ansi-i");
  if (s.underline) cls.push("ansi-u");
  return cls.length > 0 ? cls.join(" ") : undefined;
}

/**
 * Renders command or diff output with lightweight ANSI SGR + unified diff colouring,
 * tail-following while live (pausing if the user scrolls up), and a hover Copy button.
 */
export function TerminalOutput({
  text,
  follow = false,
  className = "out sel-text",
  testId,
}: {
  text: string;
  follow?: boolean;
  className?: string;
  testId?: string;
}) {
  const { t } = useTranslation("session");
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const [copied, setCopied] = useState(false);
  const lines = useMemo(() => parseTerminalLines(text), [text]);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && follow && stick.current) {
      el.scrollTop = el.scrollHeight;
    }
  }, [text, follow]);

  const onCopy = () => {
    void copyText(stripTerminalEscapes(text)).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <div
      ref={scroller}
      className={`${className} term-out`}
      data-testid={testId}
      onScroll={(e) => {
        const el = e.currentTarget;
        stick.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 40;
      }}
    >
      <button
        type="button"
        className="term-copy"
        onClick={(e) => {
          e.stopPropagation();
          onCopy();
        }}
        title={t("content.copy")}
        aria-label={t("content.copy")}
      >
        <Icon name={copied ? "check" : "copy"} />
      </button>
      {lines.map((line, i) => (
        <span key={i} className={line.diff ? `tline diff-${line.diff}` : "tline"}>
          {i > 0 && "\n"}
          {line.spans.map((sp, j) => {
            const cls = spanClass(sp.style);
            const color = sp.style?.fgColor;
            return cls || color ? (
              <span key={j} className={cls} style={color ? { color } : undefined}>
                {sp.text}
              </span>
            ) : (
              sp.text
            );
          })}
        </span>
      ))}
    </div>
  );
}
