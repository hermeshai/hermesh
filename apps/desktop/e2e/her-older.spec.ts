import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fixture, launch, snap } from "./app";

// A Mac whose Hermesh is older than the app (docs/version-skew.md §8): this Mac's her acts as if it
// lacked bots/memory (HER_TEST_WITHOUT). Run by TestDesktopHerOlder. First a her from before her listed
// her methods: opening the memory says Hermesh there needs an update, never her's "unknown method". Then
// one that lists them, without bots/memory: the row says so before it is opened. Then her has it, and the
// open page loads the memory by itself.

/** The socket of this Mac's her: the test's her program, serving (never the installed app's). */
function herSocket(): string {
  const ps = execFileSync("ps", ["-Ao", "command"], { encoding: "utf8" });
  const line = ps.split("\n").find((l) => l.startsWith(`${fixture.herBin} serve `));
  const sock = line?.match(/--listen unix:\/\/(\S+)/)?.[1];
  if (!sock) throw new Error(`no her serving:\n${ps}`);
  return sock;
}

/** Restarts this Mac's her as an update does: it stops, and hermeshd starts the program anew. */
async function restartHer(without: string) {
  fs.writeFileSync(fixture.herWithout!, without);
  const sock = herSocket();
  await new Promise<void>((resolve, reject) => {
    const s = net.connect(sock);
    let got = "";
    s.on("connect", () => {
      s.write(JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize", params: { protocol_version: 2, client_info: { name: "e2e" } } }) + "\n");
      s.write(JSON.stringify({ jsonrpc: "2.0", id: 2, method: "server/shutdown" }) + "\n");
    });
    s.on("data", (d) => {
      got += d.toString();
      if (got.includes('"id":2')) s.end();
    });
    s.on("close", () => resolve());
    s.on("error", reject);
  });
}

test("Her: a Mac older than the app says it needs an update, and the page recovers once it has one", async () => {
  test.setTimeout(240_000);
  const { app, page } = await launch(fixture.socket, { profile: fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-")) });
  await page.locator(".side-nav").getByRole("button", { name: /^Her/ }).click();
  await page.getByRole("list", { name: "选择 Her 在哪里运行" }).getByRole("listitem").filter({ hasText: "这台 Mac" }).getByRole("button", { name: "开启 Her" }).click();
  await expect(page.getByText(/^你好，我是 .+，在 .+ 上为你做事/)).toBeVisible({ timeout: 120_000 });

  // A her that lists nothing: the row can't tell, and the page says what her's answer meant.
  const settings = page.getByRole("complementary", { name: "助手设置" });
  const memory = page.getByRole("complementary", { name: "记忆" });
  const row = settings.getByRole("button", { name: /^记住的事/ });
  await page.getByRole("button", { name: "助手设置", exact: true }).click();
  await row.click();
  await expect(memory.getByTestId("needs-update")).toContainText("需要更新 Hermesh", { timeout: 30_000 });
  await expect(memory).toContainText("上的 Hermesh 版本较旧，还没有这项功能");
  await expect(memory).not.toContainText("unknown method");
  await snap(page, "her-older-memory");

  // One that lists its methods, without bots/memory: the row says so, and clearing the memory explains instead of failing.
  await memory.getByRole("button", { name: "返回" }).click();
  await restartHer("bots/memory\n");
  await expect(row).toHaveText(/需更新/, { timeout: 60_000 });
  await snap(page, "her-older-row");
  await settings.getByRole("button", { name: "清空记忆" }).click();
  await expect(page.getByText("那台电脑上的 Hermesh 版本较旧，还没有这项功能。在那台电脑上更新 Hermesh 后再试。")).toBeVisible();
  await expect(page.getByRole("dialog", { name: /^清空「.+」记住的事？$/ })).toHaveCount(0);

  // Updated: the open page loads the memory by itself.
  await row.click();
  await expect(memory.getByTestId("needs-update")).toBeVisible();
  await restartHer("");
  await expect(memory).toContainText("还没有记下什么", { timeout: 60_000 });
  await expect(memory.getByTestId("needs-update")).toHaveCount(0);
  await memory.getByRole("button", { name: "返回" }).click();
  await expect(row).toHaveText("记住的事还没有");
  await app.close();
});
