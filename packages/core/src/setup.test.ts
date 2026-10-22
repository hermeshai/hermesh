import { create } from "@bufbuild/protobuf";
import { MachineSchema } from "@hermesh/proto/local";
import { describe, expect, test } from "vitest";
import { setupItems } from "./setup";

describe("setup", () => {
  test("the checklist", () => {
    const m = create(MachineSchema, { status: { agents: [{ name: "codex", available: true }] } });
    const fresh = setupItems({ localMachine: m, projects: 1, grant: "Grok Bot", finishedTask: false, machines: 1 });
    expect(fresh.map((i) => i.done)).toEqual([true, true, true, true, false, false, false]);
    expect(fresh[3].title).toBe("连接「Grok Bot」");
    expect(fresh[5].title).toBe("登录 Codex（可选）");
    const none = setupItems({ localMachine: undefined, projects: 0, grant: undefined, finishedTask: false, machines: 0 });
    expect(none.length).toBe(6);
    expect(none[1].title).toBe("安全可信地共享本机 agent");
    expect(none[3].title).toBe("连接你的 agent");
  });
});
