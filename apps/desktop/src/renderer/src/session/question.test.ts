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
