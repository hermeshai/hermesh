import { create } from "@bufbuild/protobuf";
import { AgentStatusSchema } from "@hermesh/proto/machine";
import { describe, expect, test } from "vitest";
import { agentTiles, folderName, isAbsolutePath, platformTitle } from "./machines";

describe("machines", () => {
  test("platforms read as OS and architecture", () => {
    expect(platformTitle("darwin/arm64")).toBe("macOS · arm64");
    expect(platformTitle("linux/amd64")).toBe("Linux · amd64");
    expect(platformTitle("macos")).toBe("macOS");
    expect(platformTitle("plan9/386")).toBe("plan9 · 386");
    expect(platformTitle("")).toBe("");
  });

  test("agent tiles: ready, then not logged in, then the suggested missing ones", () => {
    const a = (name: string, available: boolean, loggedIn = false) => create(AgentStatusSchema, { name, available, loggedIn });
    const tiles = agentTiles([a("kimi", false), a("codex", true), a("gemini", true, true), a("claude", false), a("cursor", true, true)]);
    expect(tiles.map((t) => t.name)).toEqual(["cursor", "gemini", "codex", "claude"]);
  });
});

describe("project paths", () => {
  test("are absolute by their machine's platform", () => {
    expect(isAbsolutePath("/home/me/app", "linux/amd64")).toBe(true);
    expect(isAbsolutePath("C:\\code\\app", "linux/amd64")).toBe(false);
    expect(isAbsolutePath("C:\\code\\app", "windows/arm64")).toBe(true);
    expect(isAbsolutePath("d:/code/app", "windows/amd64")).toBe(true);
    expect(isAbsolutePath("\\\\nas\\share\\app", "windows/amd64")).toBe(true);
    expect(isAbsolutePath("/code/app", "windows/amd64")).toBe(false);
    expect(isAbsolutePath("code\\app", "windows/amd64")).toBe(false);
    expect(isAbsolutePath("~/code/app", "darwin/arm64")).toBe(true);
    expect(isAbsolutePath("~\\code\\app", "windows/amd64")).toBe(true);
  });
  test("name their last folder", () => {
    expect(folderName("/home/me/my-app/")).toBe("my-app");
    expect(folderName("C:\\code\\my-app")).toBe("my-app");
    expect(folderName("C:\\")).toBe("C:");
    expect(folderName("")).toBe("");
  });
});
