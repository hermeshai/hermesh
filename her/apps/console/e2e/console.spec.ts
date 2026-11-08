import { expect, test, type Page } from "@playwright/test";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { startStandIn, type StandIn } from "../scripts/stand-in.ts";
import { zip } from "./zip.ts";

// The console as the owner uses it, in a headless browser, against hermeshd's routes (the
// stand-in) and a real her: a model from an API key, the defaults new assistants start with,
// instructions, a connector by address with its tools, a skill uploaded and turned off, a web
// search key, a site allowed. What the page says is checked, and what her wrote in its files.

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

  await test.step("an API key makes a model ready", async () => {
    await expect(page.getByText("No model yet")).toBeVisible();
    await page.getByRole("button", { name: "Connect a Model" }).click();
    await page.getByRole("button", { name: "Add API Service" }).click();
    const sheet = page.getByRole("dialog", { name: "Add API Service" });
    await sheet.locator(".provider-choice", { hasText: "Mock" }).click();
    await sheet.getByLabel("API Key").fill("sk-e2e");
    await sheet.getByRole("button", { name: "Save" }).click();
    await expect(sheet).toBeHidden();
    await expect(page.locator(".mrow", { hasText: "Mock" })).toContainText("Credential store");
    await nav(page, "Overview");
    await expect(page.getByText("Ready · Mock model")).toBeVisible();
  });

  await test.step("the defaults: a model, its thinking, and an approval mode short of full access", async () => {
    await nav(page, "Defaults");
    await expect(page.getByLabel("Approvals").locator("option")).toHaveText(["Ask Permissions", "Accept Edits"]);
    await page.getByLabel("Model").selectOption({ label: "DeepSeek Flash (mock catalog)" });
    await expect(page.getByLabel("Thinking").locator("option")).toHaveText(["Default", "Low", "High", "Max"]);
    await page.getByLabel("Thinking").selectOption("high");
    await page.getByLabel("Approvals").selectOption("accept-edits");
    await expect(page.getByText("Changes files in its folder without asking; asks before commands")).toBeVisible();
    await expect.poll(() => read("config.toml")).toMatch(/^model = "mock\/deepseek-flash"$/m);
    expect(read("config.toml")).toMatch(/^thinking = "high"$/m);
    expect(read("config.toml")).toMatch(/^permission_mode = "accept-edits"$/m);
    await expect(page.getByLabel("Model")).toHaveValue("mock/deepseek-flash");
  });

  await test.step("instructions every assistant is told", async () => {
    await nav(page, "Instructions");
    await page.getByRole("textbox", { name: "Instructions" }).fill("Answer briefly, and sign every email as Her.");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByText("Instructions saved")).toBeVisible();
    await expect.poll(() => read("config.toml")).toContain('extra = "Answer briefly, and sign every email as Her."');
    await page.reload();
    await expect(page.getByRole("textbox", { name: "Instructions" })).toHaveValue("Answer briefly, and sign every email as Her.");
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
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

  await test.step("the overview says what is set up", async () => {
    await nav(page, "Overview");
    await expect(page.getByText("Ready · DeepSeek Flash (mock catalog)")).toBeVisible();
    await expect(page.locator(".ov-line", { hasText: "Connectors" })).toContainText("2 connectors");
    await expect(page.locator(".ov-line", { hasText: "Web Search" })).toContainText("Through Tavily");
    await expect(page.locator(".ov-line", { hasText: "Browser" })).toContainText("1 site allowed");
    await expect(page.locator(".ov-line", { hasText: "Instructions" })).toContainText("Set");
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
  });
});
