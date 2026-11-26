import assert from "node:assert/strict";
import test from "node:test";
import {
  applyChoice,
  applyCustomText,
  initialQuestionStep,
  isQuestionComplete,
  itemAnswerPreview,
  nextAutoStep,
  nextManualStep,
  parseOptionLabel,
  summarizeQuestion,
} from "./question-state.ts";

test("parseOptionLabel extracts Recommended prefix without losing clean text", () => {
  assert.deepEqual(parseOptionLabel("(Recommended) 一次性完整落地"), { recommended: true, text: "一次性完整落地" });
  assert.deepEqual(parseOptionLabel("（推荐）零配置自动选择"), { recommended: true, text: "零配置自动选择" });
  assert.deepEqual(parseOptionLabel("分两阶段落地"), { recommended: false, text: "分两阶段落地" });
});

test("single-choice auto-advance steps through unanswered required items and stops on last", () => {
  const items = [
    { id: "q1", optional: false, multiSelect: false },
    { id: "q2", optional: false, multiSelect: false },
    { id: "q3", optional: false, multiSelect: false },
  ];
  let chosen: Record<string, string[]> = {};
  let texts: Record<string, string> = {};

  assert.equal(initialQuestionStep(items, chosen, texts), 0);

  ({ chosen, texts } = applyChoice(items[0]!, "(Recommended) A1", chosen, texts));
  assert.equal(nextAutoStep(items, chosen, texts, 0), 1);

  ({ chosen, texts } = applyChoice(items[1]!, "B2", chosen, texts));
  assert.equal(nextAutoStep(items, chosen, texts, 1), 2);
  assert.equal(isQuestionComplete(items, chosen, texts), false);

  ({ chosen, texts } = applyChoice(items[2]!, "C1", chosen, texts));
  assert.equal(nextAutoStep(items, chosen, texts, 2), -1);
  assert.equal(isQuestionComplete(items, chosen, texts), true);

  // Revisiting q1 to change answer keeps focus on q1 when q2 & q3 are already answered.
  ({ chosen, texts } = applyChoice(items[0]!, "A2", chosen, texts));
  assert.equal(nextAutoStep(items, chosen, texts, 0), -1);
  assert.equal(itemAnswerPreview(items[0]!, chosen, texts), "A2");
});

test("trailing optional note does not hijack auto-advance once required choice is answered", () => {
  const items = [
    { id: "choice", optional: false, multiSelect: false },
    { id: "note", optional: true, multiSelect: false },
  ];
  const { chosen, texts } = applyChoice(items[0]!, "红", {}, {});
  assert.equal(nextAutoStep(items, chosen, texts, 0), -1);
  assert.equal(isQuestionComplete(items, chosen, texts), true);
  assert.equal(nextManualStep(items, chosen, texts, 0), 1);
});

test("custom text and single-choice selection replace each other cleanly", () => {
  const it = { id: "q1", optional: false, multiSelect: false };
  let st = applyChoice(it, "(Recommended) 方案一", {}, {});
  assert.deepEqual(st.chosen.q1, ["(Recommended) 方案一"]);

  st = applyCustomText(it, "先只改第一部分", st.chosen, st.texts);
  assert.deepEqual(st.chosen.q1, []);
  assert.equal(st.texts.q1, "先只改第一部分");
  assert.equal(itemAnswerPreview(it, st.chosen, st.texts), "先只改第一部分");

  st = applyChoice(it, "方案二", st.chosen, st.texts);
  assert.deepEqual(st.chosen.q1, ["方案二"]);
  assert.equal(st.texts.q1, "");
});

test("summarizeQuestion returns concise count and first prompt without trailing notes or legacy wrapper noise", () => {
  assert.deepEqual(summarizeQuestion([]), { count: 0, text: "" });

  // Single choice with optional free-text note counts as 1 question and omits note label.
  assert.deepEqual(
    summarizeQuestion([
      { header: "Choice", text: "选哪个颜色", optional: false },
      { header: "Note", text: "Anything else", optional: true },
    ]),
    { count: 1, text: "选哪个颜色" },
  );

  // Merged multi-question items report total count + first question only.
  assert.deepEqual(
    summarizeQuestion([
      { header: "", text: "展开单条工具时，输入与输出希望如何呈现？", optional: false },
      { header: "", text: "当工具没有返回输出内容时怎么处理？", optional: false },
      { header: "", text: "这次优化希望覆盖哪些端和会话视图？", optional: false },
    ]),
    { count: 3, text: "展开单条工具时，输入与输出希望如何呈现？" },
  );

  // Unmerged legacy ACP items strip companion free-form items and generic wrapper text.
  assert.deepEqual(
    summarizeQuestion([
      { header: "展开单条工具时，输入与输出希望如何呈现？", text: "The agent has 3 questions.", optional: true },
      { header: "Other", text: "Optional free-form answer to: 展开单条工具时，输入与输出希望如何呈现？", optional: true },
      { header: "当工具没有返回输出内容时怎么处理？", text: "The agent has 3 questions.", optional: true },
      { header: "Other", text: "Optional free-form answer to: 当工具没有返回输出内容时怎么处理？", optional: true },
      { header: "这次优化希望覆盖哪些端和会话视图？", text: "The agent has 3 questions.", optional: true },
      { header: "Other", text: "Optional free-form answer to: 这次优化希望覆盖哪些端和会话视图？", optional: true },
    ]),
    { count: 3, text: "展开单条工具时，输入与输出希望如何呈现？" },
  );
});
