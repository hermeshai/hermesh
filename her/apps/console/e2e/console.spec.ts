import { expect, test, type Page } from "@playwright/test";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
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

test("custom HTTP and stdio connectors", async ({ page }) => {
  await page.goto(site.url);
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
    const name = "hello-" + "a".repeat(26);
    await page.getByRole("button", { name: "Add Connector" }).click();
    const sheet = page.getByRole("dialog", { name: "Add Connector" });
    // Explicit transport switcher: Remote URL (HTTP) vs Local Program (stdio).
    await sheet.getByRole("button", { name: "Local Program (stdio)" }).click();
    const local = page.getByRole("dialog", { name: "Add Local Program (stdio)" });
    // Shows execution host callout.
    await expect(local).toContainText("Runs directly on");
    await expect(local).toContainText("(stdio, no shell)");
    // Quick paste rejects shell operators.
    await local.getByLabel("Quick paste (optional)").fill("node server.js && whoami");
    await local.getByRole("button", { name: "Fill fields" }).click();
    await expect(local).toContainText("Shell operators");
    // Fill structured Name, Executable, Arguments, and Environment variables (including a locked secret).
    await local.getByLabel("Name", { exact: true }).fill(name);
    await local.getByLabel("Executable").fill(process.execPath);
    await local.getByRole("button", { name: "Add argument" }).click();
    await local.getByLabel("Argument 1").fill(path.join(import.meta.dirname, "mcp-stdio.mjs"));
    await local.getByRole("button", { name: "Add a row" }).click();
    await local.getByLabel("Name", { exact: true }).nth(1).fill("GREETING");
    await local.getByLabel("Value").fill("hi");
    await local.getByRole("button", { name: "Add a row" }).click();
    await local.getByLabel("Name", { exact: true }).nth(2).fill("API_TOKEN");
    await local.getByLabel("Value").nth(1).fill("secret-e2e-token-12345");
    await local.getByRole("button", { name: "Add and Approve" }).click();
    await expect(local.getByText(`Added ${name}`, { exact: true })).toBeVisible();
    await expect(local.getByText("say_hello")).toBeVisible();
    await local.getByRole("button", { name: "Done" }).click();
    const row = page.locator(".cz-row", { hasText: name });
    await expect(row).toContainText("Ready · 1 tool");
    expect(read("config.toml")).toContain(`[mcp_servers.${name}]`);
    expect(read("config.toml")).toContain(`command = ${JSON.stringify(process.execPath)}`);
    expect(read("config.toml")).toContain('GREETING = "hi"');
    // Secret value is never written in plaintext to config.toml; only its credential reference is stored.
    const original = read("config.toml").match(/API_TOKEN = "(\$\{credential:mcp-stdio\.[A-Za-z0-9_-]{32}\.[a-f0-9]{20}\})"/)?.[1];
    expect(original).toBeDefined();
    expect(read("config.toml")).not.toContain("secret-e2e-token-12345");
    // Detail page shows execution host and allows structured editing.
    await row.click();
    await expect(page.getByText("Execution host")).toBeVisible();
    await page.getByRole("button", { name: "More" }).click();
    await page.getByRole("menuitem", { name: "Edit Configuration…" }).click();
    const edit = page.getByRole("dialog", { name: `Edit ${name}` });
    await expect(edit.getByLabel("Executable")).toHaveValue(process.execPath);
    await expect(edit.getByLabel("Argument 1")).toHaveValue(path.join(import.meta.dirname, "mcp-stdio.mjs"));
    const token = edit.getByLabel("Value").and(edit.locator('input[type="password"]'));
    await expect(token).toHaveAttribute("type", "password");
    await expect(token).toHaveValue("");
    await token.fill("rotated-e2e-token");
    await page.route("**/command-connector", (route) => route.fulfill({ status: 403, body: "The owner declined to sign" }), { times: 1 });
    await edit.getByRole("button", { name: "Save and Approve" }).click();
    await expect(edit.getByText("The owner declined to sign")).toBeVisible();
    expect(read("config.toml")).toContain(original!);
    expect(read("credentials.toml")).toContain("secret-e2e-token-12345");
    expect(read("credentials.toml")).not.toContain("rotated-e2e-token");
    await edit.getByRole("button", { name: "Save and Approve" }).click();
    await expect(edit).toBeHidden();
    expect(read("config.toml")).not.toContain(original!);
    expect(read("config.toml")).not.toContain("rotated-e2e-token");
    expect(read("credentials.toml")).toContain("rotated-e2e-token");
    expect(read("credentials.toml")).not.toContain("secret-e2e-token-12345");
    await page.getByRole("button", { name: "Back to Connectors" }).click();
  });

});

test.describe("iPhone console layout", () => {
  test.use({ viewport: { width: 393, height: 852 }, hasTouch: true });
  test("custom HTTP and stdio connectors on an iPhone-sized screen", async ({ page }) => {
    await page.goto(site.url + "#connectors");
    await page.getByRole("button", { name: "Add Connector" }).tap();
    const remote = page.getByRole("dialog", { name: "Add Connector" });
    await expect(remote.getByRole("button", { name: "Remote URL (HTTP)" })).toHaveAttribute("aria-pressed", "true");
    await remote.getByLabel("Name", { exact: true }).fill("mobile-http");
    await remote.getByLabel("Address").fill(site.mcpUrl + "?mobile=1");
    await remote.getByRole("button", { name: "Connect" }).tap();
    await expect(remote.getByText("Checked: the server accepted the connection; no authentication needed.")).toBeVisible();
    await remote.getByRole("button", { name: "Add", exact: true }).tap();
    await remote.getByRole("button", { name: "Done" }).tap();
    await expect(page.locator(".cz-row", { hasText: "mobile-http" })).toContainText("Ready · 17 tools");
    await page.getByRole("button", { name: "Add Connector" }).tap();
    await remote.getByRole("button", { name: "Local Program (stdio)" }).tap();
    const local = page.getByRole("dialog", { name: "Add Local Program (stdio)" });
    await expect(local).toContainText("(stdio, no shell)");
    await local.getByLabel("Name", { exact: true }).fill("Phone_stdio-1");
    await local.getByLabel("Quick paste (optional)").fill(JSON.stringify({
      command: process.execPath,
      args: [path.join(import.meta.dirname, "mcp-stdio.mjs"), "mobile"],
    }));
    await local.getByRole("button", { name: "Fill fields" }).tap();
    await expect(local.getByLabel("Executable")).toHaveValue(process.execPath);
    await local.getByRole("button", { name: "Add and Approve" }).tap();
    await expect(local.getByText("say_hello")).toBeVisible();
    await local.getByRole("button", { name: "Done" }).tap();
    await expect(page.locator(".cz-row", { hasText: "Phone_stdio-1" })).toContainText("Ready · 1 tool");
  });
});

test("an environment set up from its console", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(site.url);

  // The mock's service is in models.toml without a key: a service of the person's own, listed as
  // one; its key is set from its models, and her asks the service again with it.
  await test.step("an API key connects a model service, on the page the console opens on", async () => {
    await expect(page.getByRole("navigation", { name: "Pages" }).getByRole("button", { name: "Models", exact: true })).toHaveAttribute("aria-current", "page");
    const row = page.locator(".mrow", { hasText: "Mock" });
    await row.getByRole("button", { name: "Manage Models" }).click();
    const manage = page.getByRole("dialog", { name: /Mock/ });
    await manage.getByRole("button", { name: "Set Key…" }).click();
    const sheet = page.getByRole("dialog", { name: "Set Key" });
    await sheet.getByLabel("API Key").fill("sk-e2e");
    await sheet.getByRole("button", { name: "Save" }).click();
    await expect(sheet).toBeHidden();
    await expect(page.getByText("Key saved")).toBeVisible();
    await expect(row).toContainText(new URL(site.modelUrl).host);
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
    // Its two places open on that computer at a click: the extensions page, in the default browser
    // (Google Chrome, as the stand-in's osascript says), and the extension's folder.
    await guide.getByRole("button", { name: "Open", exact: true }).click();
    await expect.poll(() => site.opened()).toContain(linux ? "google-chrome chrome://extensions" : "open -a /Applications/Google Chrome.app chrome://extensions");
    const reveal = guide.getByRole("button", { name: "Show Folder" });
    await reveal.click();
    // hermeshd puts the extension there when it starts; here nothing has yet.
    await expect(guide.locator(".set-note.warn")).toContainText("the Hermesh extension is not in");
    const folder = path.join(site.home, "user", linux ? ".local/share/hermesh/chrome-extension" : "Library/Application Support/Hermesh/Chrome Extension");
    mkdirSync(folder, { recursive: true });
    writeFileSync(path.join(folder, "manifest.json"), "{}");
    await reveal.click();
    await expect.poll(() => site.opened()).toContain(linux ? `xdg-open ${folder}` : `open -R ${folder}`);
    await expect(guide.locator(".set-note.warn")).toBeHidden();
    await guide.getByRole("button", { name: "Close" }).click();
    await expect(guide).toBeHidden();
    // Sent here to connect it (the Mac app's chat does so): the guide opens by itself, and the address goes back to the page.
    await page.goto(`${site.url}#browser/connect`);
    await expect(page.getByRole("dialog", { name: "Connect Your Chrome" })).toBeVisible();
    await expect.poll(() => new URL(page.url()).hash).toBe("#browser");
    await page.getByRole("dialog", { name: "Connect Your Chrome" }).getByRole("button", { name: "Close" }).click();
  });

  await test.step("browser control: a switch for all of it, and new pages in the built-in browser until the owner's Chrome connects", async () => {
    const toggle = page.getByRole("switch", { name: "Allow Her to Control Browsers" });
    // No desktop app shows browsers here, and the owner's Chrome is not used: off, and not set.
    await expect(toggle).toHaveAttribute("aria-checked", "false");
    const chosen = page.getByLabel("Default Browser");
    await expect(chosen).toBeDisabled();
    await expect(chosen).toHaveValue("builtin");
    await expect(page.locator(".browser-control")).toContainText("Connect your Chrome to choose it.");
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-checked", "true");
    await expect.poll(() => read("config.toml")).toMatch(/^\[browser\][^[]*^enabled = true$/m);
    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-checked", "false");
    await expect.poll(() => read("config.toml")).toMatch(/^\[browser\][^[]*^enabled = false$/m);
    await expect(page.locator(".browser-rest")).toHaveClass(/\boff\b/);
    await toggle.click();
    await expect(page.locator(".browser-rest")).not.toHaveClass(/\boff\b/);
    await expect.poll(() => read("config.toml")).toMatch(/^\[browser\][^[]*^enabled = true$/m);
  });

  await test.step("all sites ask unless set; a site set apart asks whatever all sites allow", async () => {
    const all = page.getByLabel("Permission for All Sites");
    await expect(all).toHaveValue("ask");
    await all.selectOption("navigate");
    await expect.poll(() => read("config.toml")).toContain('"*" = "navigate"');
    await expect(page.locator(".site-row", { hasText: "All Sites" })).toContainText("acting on them asks first");
    await page.getByLabel("Site to add", { exact: true }).fill("bank.example");
    await page.getByLabel("Permission for the site to add").selectOption("ask");
    await page.getByRole("button", { name: "Add", exact: true }).click();
    await expect(page.getByLabel("Permission for bank.example")).toHaveValue("ask");
    await expect.poll(() => read("config.toml")).toContain('"https://bank.example" = "ask"');
    await all.selectOption("ask");
    await expect.poll(() => read("config.toml")).not.toContain('"*"');
  });

  await test.step("the built-in browser: the Chrome her found", async () => {
    const row = page.locator("section.builtin .frow");
    const chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
    if (process.platform === "darwin" && existsSync(chrome)) {
      await expect(row.locator(".fn")).toHaveText("Google Chrome");
      await expect(row.locator(".fn")).toHaveAttribute("title", chrome);
      await expect(row.locator(".fdesc")).toHaveText(/^Her opens pages in her own Google Chrome on the Mac she runs on, without your sign-ins/);
    } else {
      await expect(row.locator(".fdesc")).toHaveText(/without your sign-ins|There is no Chrome on the/);
    }
    await expect(page.locator("section.builtin").getByRole("switch")).toHaveCount(0);
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

// A service added by its address (her/docs/kernel/custom-endpoint-design.md §3.1), as the owner
// adds one: only its address; her probes her-mock's gateway, which speaks Chat Completions and
// Anthropic Messages and lists a Claude model, the mock's own and one nothing describes.
test("a custom service added by its address, described, checked again and removed", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const address = `${site.modelUrl}/gateway/v1`;
  const host = new URL(site.modelUrl).host;
  await page.goto(site.url);
  await nav(page, "Models");

  await test.step("an address nothing answers at is not saved, and the form says why", async () => {
    await page.getByRole("button", { name: "Add a Service" }).click();
    const sheet = page.getByRole("dialog", { name: "Add a Service" });
    await sheet.locator(".provider-choice", { hasText: "Custom Service" }).click();
    await sheet.getByLabel("Address").fill("http://127.0.0.1:9/v1");
    await sheet.getByRole("button", { name: "Connect" }).click();
    await expect(sheet.getByRole("alert")).toContainText("Cannot reach this address");
    await sheet.getByRole("button", { name: "Close" }).click();
    expect(read("models.toml")).not.toContain("127.0.0.1:9");
  });

  await test.step("the address alone is enough: her finds its protocols and models", async () => {
    await page.getByRole("button", { name: "Add a Service" }).click();
    const sheet = page.getByRole("dialog", { name: "Add a Service" });
    await sheet.locator(".provider-choice", { hasText: "Custom Service" }).click();
    await sheet.getByLabel("Address").fill(address);
    await sheet.getByLabel("Name").fill("Acme Gateway");
    await sheet.getByRole("button", { name: "Connect" }).click();
    const added = sheet.getByTestId("custom-added");
    await expect(added).toContainText("Acme Gateway");
    await expect(added).toContainText(/Found 3 models, \d with known details/);
    await expect(added).toContainText("Protocols: OpenAI Chat Completions, Anthropic Messages");
    await expect(added).toContainText("Claude models use Anthropic Messages, the others Chat Completions");
    await sheet.getByRole("button", { name: "Done" }).click();
    await expect(sheet).toBeHidden();
    await expect(page.locator(".mrow", { hasText: "Acme Gateway" })).toContainText(host);
    expect(read("models.toml")).toContain(address);
  });

  await test.step("a model nothing describes is told which model it is like", async () => {
    await page.locator(".mrow", { hasText: "Acme Gateway" }).getByRole("button", { name: "Manage Models" }).click();
    const manage = page.getByRole("dialog", { name: /Acme Gateway/ });
    const house = manage.locator(".mm-row", { hasText: "house-model-7" });
    await expect(house).toContainText("Details unknown");
    await house.getByRole("button", { name: "Set Details" }).click();
    await house.getByLabel("Which model is it like").fill("gpt-4o");
    await house.locator(".mm-suggest-row").first().click();
    await expect(page.getByText("Saved", { exact: true })).toBeVisible();
    await expect(house).toContainText(/Like GPT-4o/);
    await expect(house).not.toContainText("Details unknown");
    expect(read("models.toml")).toMatch(/canonical = "openai\/gpt-4o/);

    await manage.getByText("Connection details").click();
    await expect(manage.locator(".plan-details dd").first()).toHaveText(address);
    await manage.getByRole("button", { name: "Check Again" }).click();
    await expect(page.getByText("Checked again")).toBeVisible();
    await manage.getByRole("button", { name: "Close" }).click();
  });

  await test.step("removed, it leaves the list and models.toml", async () => {
    const row = page.locator(".mrow", { hasText: "Acme Gateway" });
    await row.getByRole("button", { name: "Remove", exact: true }).click();
    await expect(row).toContainText("Its models leave the menu");
    await expect(row.getByRole("button", { name: "Cancel" })).toBeVisible();
    await row.getByRole("button", { name: "Remove", exact: true }).click();
    await expect(row).toBeHidden();
    expect(read("models.toml")).not.toContain(address);
  });
  expect(errors).toEqual([]);
});

// The Storage page against what is really in her home: files written there by kind, sized as
// the page should say them, and counted again when the page opens again.
test("the Storage page says where her data is and what it holds", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const put = (file: string, size: number) => {
    mkdirSync(path.dirname(path.join(site.home, file)), { recursive: true });
    writeFileSync(path.join(site.home, file), Buffer.alloc(size, "x"));
  };
  put("bots/e2e/files/report.md", 1_200_000);
  put("bots/e2e/files/site/index.html", 300_000);
  put("bots/e2e/memory/owner.md", 20_000);
  put("trash/sessions/2026/11/15/old.jsonl", 3_000_000);
  const row = (name: string) => page.locator(".st-row", { has: page.locator(".fn", { hasText: new RegExp(`^${name}$`) }) });

  await page.goto(site.url);
  await nav(page, "Storage");
  await expect(page.locator(".st-path")).toHaveText(site.home);
  await expect(page.getByText("Chosen with HER_HOME")).toBeVisible();
  await expect(page.getByText(/only copy of Her's assistants/)).toBeVisible();
  await expect(page.locator(".st-row")).toHaveCount(7);
  await expect(row("Files").locator(".st-size")).toHaveText("1.5 MB");
  await expect(row("Memory").locator(".st-size")).toHaveText("20 KB");
  await expect(row("Deleted Conversations").locator(".st-size")).toHaveText("3 MB");
  await expect(page.locator(".st-total")).toHaveText(/^\d[\d.]* MB in all$/);

  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.getByRole("button", { name: "Copy Path" }).click();
  await expect(page.getByText("Path copied")).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(site.home);

  // More made meanwhile shows the next time the page opens.
  put("bots/e2e/files/deck.pptx", 2_500_000);
  await nav(page, "Models");
  await nav(page, "Storage");
  await expect(row("Files").locator(".st-size")).toHaveText("4 MB");
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
