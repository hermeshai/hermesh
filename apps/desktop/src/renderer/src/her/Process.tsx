import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toolInput, toolObject, toolOutput, type Step, type ToolView, type WorkItem } from "@hermesh/her-client";
import { ToolLine } from "../session/Chat";
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

/** A step on its line: a thought, or a call. */
function StepRow({ item }: { item: WorkItem }) {
  const { t } = useTranslation("her");
  const [open, setOpen] = useState(false);
  if (item.kind === "thinking") {
    const text = item.text.trim();
    const gist = item.redacted ? t("steps.thinkingHidden") : ((item.live ? text.split("\n").filter((l) => l.trim() !== "").at(-1) : text.split("\n").find((l) => l.trim() !== "")) ?? "");
    return (
      <div className={`tool thought ${open && text ? "open" : ""}`}>
        <button className={`th ${item.live ? "shimmer" : ""}`} onClick={() => text && !item.redacted && setOpen(!open)} aria-expanded={text && !item.redacted ? open : undefined} aria-busy={item.live || undefined}>
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
  const spinning = tool.state === "running" || tool.state === "pending" || tool.state === "streaming";
  const denied = tool.state === "denied" || tool.state === "awaiting_approval";
  return (
    <ToolLine
      verb={toolVerb(tool)}
      object={toolObject(tool)}
      title={tool.title}
      shell={tool.category === "execute"}
      running={spinning}
      failed={tool.state === "failed" || denied}
      denied={denied}
      input={toolInput(tool)}
      output={toolOutput(tool)}
      open={open}
      onToggle={() => setOpen(!open)}
    />
  );
}
