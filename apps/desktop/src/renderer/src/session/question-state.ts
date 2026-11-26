export type QuestionStepItem = {
  id: string;
  optional: boolean;
  multiSelect: boolean;
};

const recPrefixRe = /^(?:\(Recommended\)|（推荐）)\s*/i;

export function parseOptionLabel(label: string): { recommended: boolean; text: string } {
  const m = recPrefixRe.exec(label);
  return m ? { recommended: true, text: label.slice(m[0].length) } : { recommended: false, text: label };
}

export function isItemAnswered(
  it: Pick<QuestionStepItem, "id">,
  chosen: Record<string, string[]>,
  texts: Record<string, string>,
): boolean {
  return (chosen[it.id] ?? []).length > 0 || (texts[it.id] ?? "").trim() !== "";
}

export function isQuestionComplete(
  items: Pick<QuestionStepItem, "id" | "optional">[],
  chosen: Record<string, string[]>,
  texts: Record<string, string>,
): boolean {
  return items.every((it) => it.optional || isItemAnswered(it, chosen, texts));
}

export function itemAnswerPreview(
  it: Pick<QuestionStepItem, "id" | "multiSelect">,
  chosen: Record<string, string[]>,
  texts: Record<string, string>,
): string {
  const txt = (texts[it.id] ?? "").trim();
  const picks = (chosen[it.id] ?? []).map((l) => parseOptionLabel(l).text);
  if (picks.length === 0) return txt;
  const joined = picks.join("、");
  return txt !== "" ? `${joined} · ${txt}` : joined;
}

export function initialQuestionStep(
  items: Pick<QuestionStepItem, "id" | "optional">[],
  chosen: Record<string, string[]>,
  texts: Record<string, string>,
): number {
  if (items.length <= 1) return 0;
  const hasRequired = items.some((it) => !it.optional);
  const idx = items.findIndex((it) => (hasRequired ? !it.optional && !isItemAnswered(it, chosen, texts) : !isItemAnswered(it, chosen, texts)));
  return idx === -1 ? 0 : idx;
}

/** Next unanswered step to auto-advance into after a single-choice pick; -1 when ready to review/submit. */
export function nextAutoStep(
  items: Pick<QuestionStepItem, "id" | "optional">[],
  chosen: Record<string, string[]>,
  texts: Record<string, string>,
  from: number,
): number {
  if (items.length <= 1) return -1;
  const hasRequired = items.some((it) => !it.optional);
  for (let offset = 1; offset < items.length; offset++) {
    const idx = (from + offset) % items.length;
    const it = items[idx]!;
    if (hasRequired ? !it.optional && !isItemAnswered(it, chosen, texts) : !isItemAnswered(it, chosen, texts)) {
      return idx;
    }
  }
  return -1;
}

/** Target step for Continue / Skip when advancing manually from multi-select, text, or optional items. */
export function nextManualStep(
  items: Pick<QuestionStepItem, "id" | "optional">[],
  chosen: Record<string, string[]>,
  texts: Record<string, string>,
  from: number,
): number {
  const auto = nextAutoStep(items, chosen, texts, from);
  if (auto !== -1) return auto;
  return from + 1 < items.length ? from + 1 : -1;
}

export function applyChoice(
  it: Pick<QuestionStepItem, "id" | "multiSelect">,
  label: string,
  chosen: Record<string, string[]>,
  texts: Record<string, string>,
): { chosen: Record<string, string[]>; texts: Record<string, string> } {
  const now = chosen[it.id] ?? [];
  if (!it.multiSelect) {
    const nextTexts = texts[it.id] ? { ...texts, [it.id]: "" } : texts;
    return { chosen: { ...chosen, [it.id]: [label] }, texts: nextTexts };
  }
  const next = now.includes(label) ? now.filter((l) => l !== label) : [...now, label];
  return { chosen: { ...chosen, [it.id]: next }, texts };
}

export function applyCustomText(
  it: Pick<QuestionStepItem, "id" | "multiSelect">,
  value: string,
  chosen: Record<string, string[]>,
  texts: Record<string, string>,
): { chosen: Record<string, string[]>; texts: Record<string, string> } {
  const nextTexts = { ...texts, [it.id]: value };
  if (!it.multiSelect && value.trim() !== "" && (chosen[it.id] ?? []).length > 0) {
    return { chosen: { ...chosen, [it.id]: [] }, texts: nextTexts };
  }
  return { chosen, texts: nextTexts };
}
