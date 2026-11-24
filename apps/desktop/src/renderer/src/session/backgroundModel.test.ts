import assert from "node:assert/strict";
import { test } from "node:test";
import { formatTaskDuration, formatTaskTiming, stripAnsi, taskLabel, taskSecondary, type TimeTranslate } from "./backgroundModel.ts";

const zhTime: TimeTranslate = (key, opts) => {
  const c = opts?.count;
  if (key === "time.pair") return `${opts?.big} ${opts?.small}`;
  if (key === "time.seconds") return `${c} 秒`;
  if (key === "time.minutes") return `${c} 分`;
  if (key === "time.hours") return `${c} 小时`;
  if (key === "time.days") return `${c} 天`;
  return key;
};

test("taskLabel collapses multi-line commands into a single line for shimmer alignment", () => {
  assert.equal(
    taskLabel({ taskId: "bg_1", title: "", command: "gofmt -w cmd\n  && go test ./..." }),
    "gofmt -w cmd && go test ./...",
  );
});

test("taskSecondary prefers distinct command in monospace, else clean latest output line, else kind", () => {
  assert.deepEqual(
    taskSecondary(
      { taskId: "bg_1", title: "Unit test execution", command: "pnpm test", outputTail: "PASS all", summary: "" },
      "命令",
    ),
    { text: "pnpm test", mono: true },
  );

  assert.deepEqual(
    taskSecondary(
      {
        taskId: "bg_2",
        title: "npm run dev",
        command: "npm run dev",
        outputTail: "starting...\n\x1b[32mready on http://localhost:3000\x1b[0m\n",
        summary: "",
      },
      "常驻服务",
    ),
    { text: "ready on http://localhost:3000", mono: false },
  );

  assert.deepEqual(
    taskSecondary(
      { taskId: "bg_3", title: "npm run dev", command: "npm run dev", outputTail: "", summary: "stopped by user" },
      "常驻服务",
    ),
    { text: "常驻服务", mono: false },
  );
  assert.equal(stripAnsi("\x1b[31mFAIL\x1b[0m"), "FAIL");
});

test("formatTaskTiming formats running tasks as clock and completed tasks as localized duration", () => {
  const t0 = new Date("2026-11-25T00:00:00Z");
  const t21 = new Date("2026-11-25T00:00:21Z");
  assert.equal(formatTaskTiming(true, t0, undefined, t21, zhTime), "0:21");
  assert.equal(formatTaskTiming(false, t0, new Date("2026-11-25T00:02:14Z"), t21, zhTime), "2 分 14 秒");
  assert.equal(formatTaskDuration(16_000, zhTime), "16 秒");
});
