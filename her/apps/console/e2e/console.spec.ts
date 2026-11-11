import { expect, test, type Page } from "@playwright/test";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { startStandIn, type StandIn } from "../scripts/stand-in.ts";
import { zip } from "./zip.ts";

// The console as the owner uses it, in a headless browser, against hermeshd's routes (the
// stand-in) and a real her: a model from an API key, a connector by address with its tools, a
// skill uploaded and turned off, a web search key, a site allowed. What the page says is checked,
// and what her wrote in its files.

let site: StandIn;
test.beforeAll(async () => {
  site = await startStandIn();
});
test.afterAll(async () => {
  await site?.close();
});

const read = (file: string) => {
  try {
    return readFileSync(path.join(site.home, file), "utf8");
  } catch {
    return "";
  }
};
const nav = (page: Page, name: string) => page.getByRole("navigation", { name: "Pages" }).getByRole("button", { name, exact: true }).click();

test("an environment set up from its console", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(site.url);

  await test.step("an API key connects a model service, on the page the console opens on", async () => {
    await expect(page.getByRole("navigation", { name: "Pages" }).getByRole("button", { name: "Models", exact: true })).toHaveAttribute("aria-current", "page");
    await page.getByRole("button", { name: "Add API Service" }).click();
    const sheet = page.getByRole("dialog", { name: "Add API Service" });
    await sheet.locator(".provider-choice", { hasText: "Mock" }).click();
    await sheet.getByLabel("API Key").fill("sk-e2e");
    await sheet.getByRole("button", { name: "Save" }).click();
    await expect(sheet).toBeHidden();
    await expect(page.locator(".mrow", { hasText: "Mock" })).toContainText("Credential store");
  });

  await test.step("a connector added by address, checked, its tools listed", async () => {
    await nav(page, "Connectors");
    await page.getByRole("button", { name: "Add Connector" }).click();
    const sheet = page.getByRole("dialog", { name: "Add Connector" });
    await sheet.getByLabel("Name").fill("sim");
    await sheet.getByLabel("Address").fill(site.mcpUrl);
    await sheet.getByRole("button", { name: "Connect" }).click();
    await expect(sheet.getByText("Checked: the server accepted the connection; no authentication needed.")).toBeVisible();
    await sheet.getByRole("button", { name: "Add", exact: true }).click();
    await expect(sheet.getByText("Added sim")).toBeVisible();
    await expect(sheet.getByText("17 tools · available in new conversations")).toBeVisible();
    await sheet.getByRole("button", { name: "Done" }).click();
    const row = page.locator(".cz-row", { hasText: "sim" });
    await expect(row).toContainText("Ready · 17 tools");
    await row.click();
    await expect(page.locator(".cz-tool", { hasText: "repo_patch" })).toBeVisible();
    await expect(page.locator(".cz-tool")).toHaveCount(17);
    expect(read("config.toml")).toContain("[mcp_servers.sim]");
    expect(read("config.toml")).toContain(`url = "${site.mcpUrl}"`);
    await page.getByRole("button", { name: "Back to Connectors" }).click();
    await expect(row).toBeVisible();
  });

  await test.step("a connector started as a command, signed on the owner's device, never written by the page", async () => {
    await page.getByRole("button", { name: "Add Connector" }).click();
    const sheet = page.getByRole("dialog", { name: "Add Connector" });
    await sheet.getByLabel("Name").fill("hello");
    await sheet.getByLabel("Address").fill(`GREETING=hi "${process.execPath}" "${path.join(import.meta.dirname, "mcp-stdio.mjs")}"`);
    await expect(sheet.getByText("Command", { exact: true })).toBeVisible();
    await sheet.getByRole("button", { name: "Connect" }).click();
    const command = page.getByRole("dialog", { name: "Add Connector Started as a Command" });
    await command.getByRole("button", { name: "Add and Approve" }).click();
    await expect(command.getByText("Added hello", { exact: true })).toBeVisible();
    await expect(command.getByText("say_hello")).toBeVisible();
    await command.getByRole("button", { name: "Done" }).click();
    await expect(page.locator(".cz-row", { hasText: "hello" })).toContainText("Ready · 1 tool");
    expect(read("config.toml")).toContain("[mcp_servers.hello]");
    expect(read("config.toml")).toContain(`command = ${JSON.stringify(process.execPath)}`);
    expect(read("config.toml")).toContain('GREETING = "hi"');
  });

  await test.step("a skill uploaded as an archive, read, and turned off", async () => {
    const dir = mkdtempSync(path.join(tmpdir(), "her-console-e2e-"));
    const archive = path.join(dir, "weekly-notes.zip");
    writeFileSync(
      archive,
      zip({
        "weekly-notes/SKILL.md": "---\nname: weekly-notes\ndescription: Turns a week's notes into a short summary.\n---\n# Weekly notes\n\nRead `references/format.md`, then write the summary.\n",
        "weekly-notes/references/format.md": "# Format\n\nThree headings: done, next, blocked.\n",
      }),
    );
    await nav(page, "Skills");
    await page.getByRole("button", { name: "Upload Skill" }).click();
    const sheet = page.getByRole("dialog", { name: "Upload Skill" });
    await sheet.locator("input[type=file]").setInputFiles(archive);
    await expect(sheet.getByText("Found a skill. Check it:")).toBeVisible();
    rmSync(dir, { recursive: true, force: true });
    await expect(sheet.getByText("Turns a week's notes into a short summary.")).toBeVisible();
    await sheet.getByRole("button", { name: "Add", exact: true }).click();
    await expect(page.getByText("Added weekly-notes; new conversations can use it")).toBeVisible();
    const row = page.locator(".cz-row", { hasText: "weekly-notes" });
    await expect(row).toBeVisible();
    await row.click();
    await expect(page.getByRole("heading", { name: "Weekly notes" })).toBeVisible();
    await page.getByRole("navigation", { name: "Files" }).getByRole("button", { name: "format.md" }).click();
    await expect(page.getByRole("heading", { name: "Format" })).toBeVisible();
    await page.getByRole("switch", { name: "Turn Off weekly-notes" }).click();
    await expect(page.getByText("Turned off weekly-notes for new conversations")).toBeVisible();
    await expect(page.getByRole("switch", { name: "Turn On weekly-notes" })).toHaveAttribute("aria-checked", "false");
    await expect.poll(() => read("config.toml")).toMatch(/^disabled = \["[^"]*\/skills\/weekly-notes"\]$/m);
  });

  await test.step("a web search key, tried with a real search before it is kept", async () => {
    await nav(page, "Web Search");
    await page.getByRole("listitem", { name: "Tavily" }).getByRole("button", { name: "Add Key" }).click();
    const sheet = page.getByRole("dialog", { name: "Add Tavily Key" });
    await sheet.getByLabel("API Key").fill("tvly-e2e");
    await sheet.getByRole("button", { name: "Save and Test" }).click();
    await expect(page.getByText(/^Tavily works · 3 results/)).toBeVisible();
    await expect(page.getByRole("listitem", { name: "Tavily" })).toContainText("Key added · 1,000 free a month");
    expect(read("credentials.toml")).toContain("web-search.tavily");
  });

  await test.step("a site Her may open and act on without asking", async () => {
    await nav(page, "Browser");
    await page.getByLabel("Site to add", { exact: true }).fill("Example.com/docs");
    await page.getByRole("button", { name: "Add", exact: true }).click();
    await expect(page.getByText("example.com: Open and Act")).toBeVisible();
    await expect(page.getByLabel("Permission for example.com")).toHaveValue("act");
    await expect.poll(() => read("config.toml")).toContain('"https://example.com" = "act"');
  });

  await test.step("the guide to the owner's own Chrome goes by the system her runs on", async () => {
    // her runs on this computer: its folder for the extension, and its file window's shortcut.
    const linux = process.platform === "linux";
    await expect(page.locator(".yours")).toContainText(`on the ${linux ? "Linux computer" : "Mac"} Her runs on`);
    await page.getByRole("button", { name: "Connect", exact: true }).click();
    const guide = page.getByRole("dialog", { name: "Connect Your Chrome" });
    await expect(guide).toContainText(`Add the Hermesh extension to Chrome on the ${linux ? "Linux computer" : "Mac"} Her runs on.`);
    await expect(guide).toContainText(linux ? "press Ctrl+L" : "press ⇧⌘G");
    await expect(guide.locator("code").last()).toHaveText(linux ? "~/.local/share/hermesh/chrome-extension" : "~/Library/Application Support/Hermesh/Chrome Extension");
    await guide.getByRole("button", { name: "Close" }).click();
    await expect(guide).toBeHidden();
  });

  await test.step("the built-in browser: the Chrome her found, and a switch that turns it on and back to unset", async () => {
    const section = page.locator("section.builtin");
    const row = section.locator(".frow");
    const chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
    if (process.platform === "darwin" && existsSync(chrome)) {
      await expect(row.locator(".fn")).toHaveText("Google Chrome");
      await expect(row.locator(".fn")).toHaveAttribute("title", chrome);
      await expect(row.locator(".fdesc")).toHaveText("Her opens pages in her own Google Chrome on the Mac she runs on, without your sign-ins, in a window there.");
    } else {
      await expect(row.locator(".fdesc")).toHaveText(/without your sign-ins|There is no Chrome on the/);
    }
    const toggle = section.getByRole("switch", { name: "Built-in Browser" });
    await expect(toggle).toHaveAttribute("aria-checked", "false");
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-checked", "true");
    await expect.poll(() => read("config.toml")).toMatch(/^\[browser\][^[]*^enabled = true$/m);
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-checked", "false");
    await expect.poll(() => read("config.toml")).not.toMatch(/^enabled = /m);
  });

  await test.step("a dropped connection is opened again, and the page goes on working", async () => {
    site.drop();
    await expect(page.locator(".conn-bar")).toHaveText("Connecting to Her…");
    await expect(page.locator(".conn-bar")).toBeHidden();
    await nav(page, "Browser");
    await page.getByLabel("Site to add", { exact: true }).fill("docs.example.org");
    await page.getByLabel("Permission for the site to add").selectOption("navigate");
    await page.getByRole("button", { name: "Add", exact: true }).click();
    await expect(page.getByLabel("Permission for docs.example.org")).toHaveValue("navigate");
    await expect.poll(() => read("config.toml")).toContain('"https://docs.example.org" = "navigate"');
  });

  expect(errors).toEqual([]);
});

test.describe("in Chinese", () => {
  test.use({ locale: "zh-CN" });
  test("the console speaks the browser's language", async ({ page }) => {
    await page.goto(site.url);
    await expect(page.getByRole("navigation", { name: "页面" }).getByRole("button", { name: "连接器" })).toBeVisible();
    await expect(page.locator("html")).toHaveAttribute("lang", "zh-CN");
    await page.getByRole("navigation", { name: "页面" }).getByRole("button", { name: "浏览器" }).click();
    await expect(page.locator(".yours")).toContainText(process.platform === "linux" ? "就在 Her 所在的那台 Linux 电脑上" : "就在 Her 所在的那台 Mac 电脑上");
  });
});
