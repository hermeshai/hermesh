import assert from "node:assert/strict";
import { test } from "node:test";
import {
  formatTaskDuration,
  formatTaskTiming,
  latestOutputLine,
  stripAnsi,
  taskLabel,
  taskSecondary,
  type TimeTranslate,
} from "./backgroundModel.ts";

const zhTime: TimeTranslate = (key, opts) => {
  const c = opts?.count;
  if (key === "time.pair") return `${opts?.big} ${opts?.small}`;
  if (key === "time.seconds") return `${c} 秒`;
  if (key === "time.minutes") return `${c} 分`;
  if (key === "time.hours") return `${c} 小时`;
  if (key === "time.days") return `${c} 天`;
  return key;
};

test("taskLabel collapses multi-line commands and humanizes generic schedule titles", () => {
  assert.equal(
    taskLabel({ taskId: "bg_1", title: "", command: "gofmt -w cmd\n  && go test ./...", summary: "" }),
    "gofmt -w cmd && go test ./...",
  );
  assert.equal(
    taskLabel({ taskId: "bg_sched", title: "Run schedule", command: "90s", summary: "" }),
    "Run schedule · 90s",
  );
  assert.equal(
    taskLabel({ taskId: "bg_sched2", title: "Run schedule", command: "90s", summary: "Poll TestFlight build status" }),
    "Poll TestFlight build status",
  );
});

test("taskSecondary and latestOutputLine extract clean output lines", () => {
  assert.deepEqual(
    taskSecondary(
      { taskId: "bg_1", title: "Unit test execution", command: "pnpm test", outputTail: "PASS all", summary: "" },
      "Shell",
    ),
    { text: "pnpm test", mono: true },
  );

  assert.equal(
    latestOutputLine("starting...\n\x1b[32mready on http://localhost:3000\x1b[0m\n"),
    "ready on http://localhost:3000",
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

test("formatTaskTiming formats both running elapsed time and completed duration in natural units", () => {
  const t0 = new Date("2026-11-25T00:00:00Z");
  const t21 = new Date("2026-11-25T00:00:21Z");
  const t844 = new Date("2026-11-25T00:14:04Z");
  assert.equal(formatTaskTiming(true, t0, undefined, t21, zhTime), "21 秒");
  assert.equal(formatTaskTiming(true, t0, undefined, t844, zhTime), "14 分 4 秒");
  assert.equal(formatTaskTiming(false, t0, new Date("2026-11-25T00:02:14Z"), t21, zhTime), "2 分 14 秒");
  assert.equal(formatTaskDuration(16_000, zhTime), "16 秒");
});
