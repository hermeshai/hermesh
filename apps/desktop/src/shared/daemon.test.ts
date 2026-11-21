import assert from "node:assert/strict";
import { test } from "node:test";
import type { Session, SessionStatus } from "@hermesh/proto/session";
import type { GetStatusResponse, Machine, PairingDetails } from "@hermesh/proto/local";
import { assessDaemonImpact, hasActiveWork, hasAnyImpact, impactLines, resolveDaemonState, type DaemonWorld } from "./daemon.ts";
import enMain from "../locales/en/main.json" with { type: "json" };
import zhMain from "../locales/zh-CN/main.json" with { type: "json" };

const S = { QUEUED: 1 as SessionStatus, PREPARING: 2 as SessionStatus, RUNNING: 3 as SessionStatus, IDLE: 4 as SessionStatus, ARCHIVED: 5 as SessionStatus, WAITING_INPUT: 9 as SessionStatus };

const status = (overrides: Partial<GetStatusResponse> = {}): GetStatusResponse =>
  ({ initialized: true, localMachineEnabled: true, localMachineId: "m-local", machineOnly: false, ...overrides }) as GetStatusResponse;

const session = (id: string, machineId: string, st: SessionStatus, title: string): Session =>
  ({ sessionId: id, machineId, status: st, title }) as Session;

const machine = (id: string, local: boolean, extra: Record<string, unknown> = {}): Machine =>
  ({ machineId: id, local, online: true, ...extra }) as Machine;

function makeT(catalog: Record<string, unknown>) {
  return (key: string, values: Record<string, unknown> = {}): string => {
    const parts = key.split(".");
    let cur: unknown = catalog;
    for (let i = 0; i < parts.length - 1; i++) cur = (cur as Record<string, unknown>)[parts[i]];
    const group = cur as Record<string, string>;
    const leaf = parts[parts.length - 1];
    const count = typeof values.count === "number" ? values.count : undefined;
    let raw = group[leaf];
    if (raw === undefined && count !== undefined) {
      raw = group[`${leaf}_${count === 1 ? "one" : "other"}`] ?? group[`${leaf}_other`];
    }
    assert.ok(typeof raw === "string", `missing key ${key}`);
    return raw.replace(/\{\{(\w+)\}\}/g, (_, k) => String(values[k] ?? ""));
  };
}

test("assessDaemonImpact counts only local non-archived active tasks and captures screen, Her and approvals", () => {
  const world: DaemonWorld = {
    status: status(),
    machines: [
      machine("m-local", true, {
        status: { screen: { viewer: "Yi's iPhone" }, her: { enabled: true, running: true } },
        her: { enabled: true },
      }),
      machine("m-remote", false, {
        status: { screen: { viewer: "Other" }, her: { enabled: true, running: true } },
      }),
    ],
    sessions: [
      session("s1", "m-local", S.RUNNING, "修登录 bug"),
      session("s2", "m-local", S.PREPARING, "跑测试"),
      session("s3", "m-local", S.WAITING_INPUT, "选颜色"),
      session("s4", "m-local", S.IDLE, "已完成"),
      session("s5", "m-local", S.ARCHIVED, "已归档"),
      session("s6", "m-remote", S.RUNNING, "远端任务"),
    ],
    pairings: [{ code: "123456" } as PairingDetails],
  };

  const impact = assessDaemonImpact(world, true);
  assert.deepEqual(impact, {
    runningTasks: 2,
    waitingTasks: 1,
    taskTitles: ["修登录 bug", "跑测试", "选颜色"],
    screenViewer: "Yi's iPhone",
    herRunning: true,
    pendingApprovals: 2,
    localMachineOnline: true,
  });
  assert.equal(hasActiveWork(impact), true);
  assert.equal(hasAnyImpact(impact), true);

  const zhLines = impactLines(impact, makeT(zhMain));
  assert.equal(zhLines.length, 6);
  assert.match(zhLines[0], /2 个正在这台 Mac 上运行的任务会被中断.*修登录 bug、跑测试/);
  assert.match(zhLines[1], /1 个正在等待回答/);
  assert.match(zhLines[2], /Yi's iPhone 正在远程操控这台 Mac/);
  assert.match(zhLines[3], /Her 助手会停止运行/);
  assert.match(zhLines[4], /2 项待处理的连接或签名审批/);
  assert.match(zhLines[5], /其他设备（如 iPhone）和远端 Agent 将暂时无法连接这台 Mac/);

  const enLines = impactLines(impact, makeT(enMain));
  assert.equal(enLines.length, 6);
  assert.match(enLines[0], /2 tasks running on this Mac will be interrupted.*修登录 bug, 跑测试/);
  assert.match(enLines[1], /1 task on this Mac waiting for an answer/);
  assert.match(enLines[2], /Yi's iPhone is controlling this Mac/);
  assert.match(enLines[3], /Her on this Mac will stop running/);
  assert.match(enLines[4], /2 pending connection or signing approvals/);
  assert.match(enLines[5], /won’t be able to reach this Mac/);
});

test("idle initialized Mac has no active work but still reports offline impact", () => {
  const world: DaemonWorld = {
    status: status(),
    machines: [machine("m-local", true)],
    sessions: [session("s1", "m-local", S.IDLE, "写 hello")],
    pairings: [],
  };
  const impact = assessDaemonImpact(world, false);
  assert.equal(hasActiveWork(impact), false);
  assert.equal(hasAnyImpact(impact), true);
  const lines = impactLines(impact, makeT(zhMain));
  assert.equal(lines.length, 1);
  assert.match(lines[0], /暂时无法连接这台 Mac/);
});

test("uninitialized daemon with no local work reports no impact", () => {
  const world: DaemonWorld = {
    status: status({ initialized: false, localMachineEnabled: false, machineOnly: false, localMachineId: "" }),
    machines: [],
    sessions: [],
    pairings: [],
  };
  const impact = assessDaemonImpact(world, false);
  assert.equal(hasActiveWork(impact), false);
  assert.equal(hasAnyImpact(impact), false);
});

test("resolveDaemonState prioritizes transitions over connection and distinguishes user-stopped from unavailable", () => {
  assert.equal(resolveDaemonState(true, false, "stopping"), "stopping");
  assert.equal(resolveDaemonState(false, false, "starting"), "starting");
  assert.equal(resolveDaemonState(true, true, null), "running");
  assert.equal(resolveDaemonState(false, true, null), "stopped");
  assert.equal(resolveDaemonState(false, false, null), "unavailable");
});
