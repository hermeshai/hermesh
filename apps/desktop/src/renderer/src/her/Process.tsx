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

/**
 * A step on its line: a thought, or a call. For the Progress page's record (docs/her-team.md §15.2): `thinkingOpen`,
 * a thought shows whole to begin with; `outputOpen`, a call's output does; `spin`, a call still at work turns.
 */
export function StepRow({ item, thinkingOpen = false, outputOpen = false, spin = false }: { item: WorkItem; thinkingOpen?: boolean; outputOpen?: boolean; spin?: boolean }) {
  const { t } = useTranslation("her");
  const [open, setOpen] = useState(thinkingOpen);
  if (item.kind === "thinking") {
    const text = item.text.trim();
    const gist = item.redacted ? t("steps.thinkingHidden") : (text.split("\n").find((l) => l.trim() !== "") ?? "");
    return (
      <div className={`tool thought ${open && text ? "open" : ""}`}>
        <button className="th" onClick={() => text && !item.redacted && setOpen(!open)} aria-expanded={text && !item.redacted ? open : undefined}>
          <span className="vb">{t("steps.thought")}</span>
          <span className="sm2">{gist}</span>
          {text && !item.redacted && <Icon name="caret" className="chev" />}
        </button>
        {text && !item.redacted && <div className="out sel-text">{text}</div>}
      </div>
    );
  }
  return <ToolStep tool={item.tool} outputOpen={outputOpen} spin={spin} />;
}

function ToolStep({ tool, outputOpen, spin }: { tool: ToolView; outputOpen: boolean; spin: boolean }) {
  const [open, setOpen] = useState(outputOpen);
  const output = toolOutput(tool);
  const object = toolObject(tool);
  const live = spin && (tool.state === "running" || tool.state === "pending" || tool.state === "streaming");
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
        {live && <span className="mini-spin" aria-hidden="true" />}
        {output && <Icon name="caret" className="chev" />}
      </button>
      {output && <div className="out sel-text">{output}</div>}
    </div>
  );
}
