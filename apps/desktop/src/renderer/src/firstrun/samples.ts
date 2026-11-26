// The first run's fixed examples (docs/cold-start-prd.md §5.5.2, §5.6.2): the story Her's step
// plays and the website's connection map on the sign-in step. They read nothing of the person's;
// their words are in firstrun.json (herStep.show.*, net.*).

/** A task card's state in the show. */
export type TaskState = "idle" | "run" | "ok" | "fail";
export type Card = "cc" | "cx" | "gm";

/** A frame of the show: its act, what Her says (herStep.show.sN), each card's task and state, the wires a light runs along. */
export type Frame = {
  act: 0 | 1 | 2;
  say: string;
  cards: Partial<Record<Card, [task: string, state: TaskState]>>;
  /** The question's buttons: shown (1), and the pointer pressing "fix" (2). */
  ask?: 1 | 2;
  /** The PR and the release notes. */
  out?: true;
  /** Wires a light runs along: down to the card, or (true) back up to Her. */
  go?: [Card, boolean][];
};

export const show: Frame[] = [
  { act: 0, say: "s1", cards: {} },
  { act: 0, say: "s2", cards: { cx: ["repro", "run"] }, go: [["cx", false]] },
  { act: 0, say: "s3", cards: { cx: ["repro", "ok"] }, ask: 1, go: [["cx", true]] },
  { act: 0, say: "s3", cards: { cx: ["repro", "ok"] }, ask: 2 },
  { act: 1, say: "s5", cards: { cc: ["fix", "run"], cx: ["repro", "ok"] }, go: [["cc", false]] },
  { act: 1, say: "s6", cards: { cc: ["fix", "ok"], cx: ["verifyWin", "run"], gm: ["verifyLinux", "run"] }, go: [["cx", false], ["gm", false]] },
  { act: 1, say: "s7", cards: { cc: ["again", "run"], cx: ["verifyWin", "fail"], gm: ["verifyLinux", "ok"] }, go: [["cx", true], ["cc", false]] },
  { act: 1, say: "s8", cards: { cc: ["again", "ok"], cx: ["reverify", "run"], gm: ["verifyLinux", "ok"] }, go: [["cx", false]] },
  { act: 2, say: "s9", cards: { cc: ["again", "ok"], cx: ["reverify", "ok"], gm: ["verifyLinux", "ok"] }, out: true, go: [["cx", true]] },
  { act: 2, say: "s10", cards: { cc: ["again", "ok"], cx: ["reverify", "ok"], gm: ["verifyLinux", "ok"] }, out: true },
];

/** The first frame of each act, for its tab. */
export const actStarts = [0, 4, 8];
export const frameMs = 2100;

/** The show's three wires, in its 632×232 box. */
export const showWires: Record<Card, string> = {
  cc: "M300 58C280 84 150 82 110 110",
  cx: "M316 58L316 110",
  gm: "M332 58C352 84 482 82 522 110",
};

/** The connection map's wires, in its 632×286 box: every one of the three reaches every agent. */
export const meshWires: Record<string, string> = {
  "her-cl": "M288 30C220 8 170 14 122 30",
  "her-you": "M344 30C420 8 470 14 512 30",
  "cl-cc": "M92 88C92 130 90 140 90 180",
  "cl-cx": "M100 88C120 150 250 130 290 180",
  "cl-gm": "M106 88C160 168 400 116 490 180",
  "her-cc": "M306 88C290 134 150 140 116 180",
  "her-cx": "M316 88L316 180",
  "her-gm": "M326 88C342 134 482 140 516 180",
  "you-cc": "M526 88C472 168 232 116 142 180",
  "you-cx": "M532 88C512 150 382 130 342 180",
  "you-gm": "M540 88C540 130 542 140 542 180",
};

export type Node = "cl" | "her" | "you" | "cc" | "cx" | "gm";

/** A message on the map: from whom (its face), to whom (net.toX), what (net.mN), along which wires, who lights up. */
export type Message = { from: Node; to: string; say: string; wires: [string, boolean][]; on: Node[] };

export const messages: Message[] = [
  { from: "you", to: "toCodex", say: "m1", wires: [["you-cx", false]], on: ["cx"] },
  { from: "cx", to: "toYou", say: "m2", wires: [["you-cx", true]], on: ["you"] },
  { from: "you", to: "toHer", say: "m3", wires: [["her-you", true]], on: ["her"] },
  { from: "her", to: "toClaude", say: "m4", wires: [["her-cc", false]], on: ["cc"] },
  { from: "her", to: "toTwo", say: "m5", wires: [["her-cx", false], ["her-gm", false]], on: ["cx", "gm"] },
  { from: "cl", to: "toGemini", say: "m6", wires: [["cl-gm", false]], on: ["gm"] },
  { from: "her", to: "toYou", say: "m7", wires: [["her-you", false]], on: ["you"] },
];
export const messageMs = 2400;
