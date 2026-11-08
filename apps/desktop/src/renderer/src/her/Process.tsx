import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toolObject, toolOutput, type Step, type ToolView, type WorkItem } from "@hermesh/her-client";
import { Code } from "../session/Code";
import { Markdown } from "../session/Markdown";
import { Icon } from "../icons";
import { toolVerb } from "./format";

/** A routine's run on its way to its end: its words and steps, each step on its line, opened to its output. */
export function Process({ steps }: { steps: Step[] }) {
  return (
    <div className="tg-b bproc">
      {steps.map((s) => {
        switch (s.kind) {
          case "work":
            return s.items.map((it) => <StepRow key={it.key} item={it} />);
          case "text":
            return (
              <div key={s.key} className="am">
                <Markdown text={s.text} />
              </div>
            );
          default:
            return null;
        }
      })}
    </div>
  );
}

function StepRow({ item }: { item: WorkItem }) {
  const { t } = useTranslation("her");
  const [open, setOpen] = useState(false);
  if (item.kind === "thinking") {
    const text = item.text.trim();
    const gist = item.redacted ? t("steps.thinkingHidden") : (text.split("\n").find((l) => l.trim() !== "") ?? "");
    return (
      <div className={`tool ${open && text ? "open" : ""}`}>
        <button className="th" onClick={() => text && !item.redacted && setOpen(!open)} aria-expanded={text && !item.redacted ? open : undefined}>
          <span className="vb">{t("steps.thought")}</span>
          <span className="sm2">{gist}</span>
          {text && !item.redacted && <Icon name="caret" className="chev" />}
        </button>
        {text && !item.redacted && <div className="out sel-text">{text}</div>}
      </div>
    );
  }
  return <ToolStep tool={item.tool} />;
}

function ToolStep({ tool }: { tool: ToolView }) {
  const [open, setOpen] = useState(false);
  const output = toolOutput(tool);
  const object = toolObject(tool);
  return (
    <div className={`tool ${tool.state === "failed" ? "err" : ""} ${open && output ? "open" : ""}`}>
      <button className="th" onClick={() => output && setOpen(!open)} aria-expanded={output ? open : undefined}>
        <span className="vb">{toolVerb(tool)}</span>
        {tool.category === "execute" ? (
          <code className="sm2 cmd" title={tool.title}>
            <Code text={object} lang="bash" />
          </code>
        ) : (
          <span className="sm2" title={tool.title}>{object}</span>
        )}
        {output && <Icon name="caret" className="chev" />}
      </button>
      {output && <div className="out sel-text">{output}</div>}
    </div>
  );
}
