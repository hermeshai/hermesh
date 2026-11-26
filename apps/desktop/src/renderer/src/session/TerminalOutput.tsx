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

function CopyButton({ text, title, className = "term-copy" }: { text: string; title: string; className?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className={className}
      onClick={(e) => {
        e.stopPropagation();
        void copyText(stripTerminalEscapes(text)).then(() => {
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        });
      }}
      title={title}
      aria-label={title}
    >
      <Icon name={copied ? "check" : "copy"} />
    </button>
  );
}

function TerminalLines({ text }: { text: string }) {
  const lines = useMemo(() => parseTerminalLines(text), [text]);
  return (
    <>
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
    </>
  );
}

/**
 * Renders command or diff output (and optionally structured tool input + output)
 * with lightweight ANSI SGR + unified diff colouring, tail-following while live
 * (pausing if the user scrolls up), and hover Copy controls.
 */
export function TerminalOutput({
  text,
  input = "",
  inputLabel = "",
  outputLabel = "",
  emptyOutput = "",
  follow = false,
  className = "out sel-text",
  testId,
}: {
  text: string;
  input?: string;
  inputLabel?: string;
  outputLabel?: string;
  emptyOutput?: string;
  follow?: boolean;
  className?: string;
  testId?: string;
}) {
  const { t } = useTranslation("session");
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && follow && stick.current) {
      el.scrollTop = el.scrollHeight;
    }
  }, [text, input, follow]);

  const copyLabel = t("content.copy");

  if (input !== "") {
    return (
      <div
        ref={scroller}
        className={`${className} term-out tool-io`}
        data-testid={testId}
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 40;
        }}
      >
        <div className="tio-sec">
          <div className="tio-h">
            <span>{inputLabel}</span>
            <CopyButton text={input} title={copyLabel} className="tio-copy" />
          </div>
          <div className="tio-body">
            <TerminalLines text={input} />
          </div>
        </div>
        <div className="tio-sec">
          <div className="tio-h">
            <span>{outputLabel}</span>
            {text !== "" && <CopyButton text={text} title={copyLabel} className="tio-copy" />}
          </div>
          <div className="tio-body">
            {text !== "" ? <TerminalLines text={text} /> : <span className="tio-empty">{emptyOutput}</span>}
          </div>
        </div>
      </div>
    );
  }

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
      <CopyButton text={text} title={copyLabel} />
      <TerminalLines text={text} />
    </div>
  );
}
