import { expect, test, type ElectronApplication, type Page } from "@playwright/test";
import { execFile, execFileSync, spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fixture, launch, snap } from "./app";

// The sheets over the main window: adding a project, connecting an agent,
// adding a machine with its pairing code, and approving a request from a
// notification. Approvals go through the Go test signer, which says yes.

/** What the app menu, the menu bar and notifications send the page (e.g. 文件 › 连接 agent…). */
async function command(app: ElectronApplication, page: Page, c: string) {
  await expect(page.getByRole("list", { name: "会话" })).toBeVisible();
  type Main = { hermeshTest: { mainWindow(): { webContents: { send(channel: string, c: string): void } } } };
  await app.evaluate((_, c) => (globalThis as unknown as Main).hermeshTest.mainWindow().webContents.send("command", c), c);
}

/** hermeshd's local API, as the page reaches it. */
async function api<T = Record<string, never>>(page: Page, method: string, body: object = {}): Promise<T> {
  return page.evaluate(
    async ([m, b]) => {
      const r = await fetch(`app://hermesh/api/hermesh.local.v1.LocalService/${m}`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(b) });
      return r.json();
    },
    [method, body] as const,
  );
}

type Status = { connectAddress: string; localMachineId: string };
type Client = { grantId: string; grantVersion: string; label: string; expiresAt: string; spec: { machineIds: string[]; projectIds: string[]; agents: string[] } };

const running = new Set<ChildProcess>();
test.afterEach(() => running.forEach((p) => p.kill())); // a failed test leaves no login waiting

/** Runs a Hermesh binary in the background; code resolves to its pairing code, exit to its exit code. */
function background(bin: string, args: string[], env: Record<string, string> = {}) {
  const p = spawn(bin, args, { env: { ...process.env, ...env }, stdio: ["ignore", "pipe", "pipe"] });
  running.add(p);
  p.on("exit", () => running.delete(p));
  let out = "";
  const code = new Promise<string>((resolve, reject) => {
    const read = (b: Buffer) => {
      out += b.toString();
      const m = out.match(/Pairing code: ([0-9A-Z]{4}-[0-9A-Z]{4}-[0-9A-Z]{4})/);
      if (m) resolve(m[1]);
    };
    p.stdout.on("data", read);
    p.stderr.on("data", read);
    p.on("exit", () => reject(new Error(`${path.basename(bin)} exited before showing a pairing code:\n${out}`)));
  });
  code.catch(() => {});
  const exit = new Promise<number>((resolve) => p.on("exit", (c) => resolve(c ?? -1)));
  return { code, exit, output: () => out };
}

/** The agent CLI as a new agent on this computer: its own home, so it pairs anew. */
function newAgent() {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "hmagent-"));
  return {
    login: (address: string, name: string) => background(fixture.cli, ["auth", "login", address, "--name", name], { HERMESH_HOME: home }),
    run: (...args: string[]) =>
      new Promise<string>((resolve, reject) =>
        execFile(fixture.cli, args, { env: { ...process.env, HERMESH_HOME: home } }, (err, stdout, stderr) => (err ? reject(new Error(stderr || String(err))) : resolve(stdout))),
      ),
  };
}

test("adding a project: a folder on this Mac becomes a project", async () => {
  const folder = `site-${process.pid}`;
  const dir = path.join(fs.mkdtempSync(path.join(os.tmpdir(), "hmproj-")), folder);
  fs.mkdirSync(dir);
  fs.writeFileSync(path.join(dir, "README.md"), "# site\n");
  for (const args of [["init", "-q"], ["add", "."], ["-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "init"]]) execFileSync("git", args, { cwd: dir });

  const { app, page } = await launch();
  await command(app, page, "addProject");
  const sheet = page.getByRole("dialog", { name: "添加项目" });
  await expect(sheet.getByRole("group", { name: "机器" }).getByRole("button", { name: "owner-mac" })).toHaveAttribute("aria-pressed", "true");
  await expect(sheet.getByRole("button", { name: "选择…" })).toBeVisible(); // this Mac: the folder can be picked
  await sheet.getByLabel("目录").fill(dir); // the native folder dialog can't be driven
  await expect(sheet.getByLabel("项目名")).toHaveValue(folder);
  await expect(sheet.getByRole("group", { name: "工作方式" }).getByRole("button", { name: "独立 worktree" })).toHaveAttribute("aria-pressed", "true");
  await snap(page, "sheet-add-project");
  await sheet.getByRole("button", { name: "添加" }).click();
  await expect(page.getByRole("status").filter({ hasText: `已添加项目「${folder}」` })).toBeVisible();
  await expect(sheet).toBeHidden();

  const st = await api<Status>(page, "GetStatus");
  const { projects } = await api<{ projects: { machineId: string; projectId: string; workspace?: string }[] }>(page, "ListProjects");
  expect(projects).toContainEqual(expect.objectContaining({ machineId: st.localMachineId, projectId: folder, workspace: "WORKSPACE_WORKTREE" }));
  await app.close();
});

test("connecting an agent: the message to send, its request, what it may use, approved", async () => {
  const name = `Agent ${process.pid}`; // its own, also when run again against the same daemon
  const { app, page } = await launch();
  const st = await api<Status>(page, "GetStatus");
  await command(app, page, "newGrant");
  const sheet = page.getByRole("dialog", { name: "连接你的 Agent" });
  await expect(sheet).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(sheet).toBeHidden();
  // As in the first run: the apps in a row, the first one picked, its message below.
  await command(app, page, "newGrant");
  await expect(sheet.getByRole("radio", { name: "Grok Bot" })).toHaveAttribute("aria-checked", "true");
  await expect(sheet).toContainText('--name "Grok Bot"');
  await snap(page, "sheet-new-grant");
  await sheet.getByRole("radio", { name: "其他 Agent" }).click();

  await sheet.getByLabel("它叫什么").fill(name);
  await expect(sheet).toContainText(`hermesh auth login ${st.connectAddress} --name "${name}"`);
  await sheet.getByRole("button", { name: "拷贝" }).click();
  await expect(sheet.getByRole("button", { name: "已拷贝" })).toBeVisible();
  await expect(sheet).toContainText(`等 ${name} 运行这段话里的命令`);
  await snap(page, "sheet-connect-copy");
  await sheet.getByRole("radio", { name: "Grok Bot" }).click();

  // The agent runs the command it was sent; its request comes to this page, whichever agent is picked.
  const agent = newAgent();
  const login = agent.login(st.connectAddress, name);
  await expect(sheet.getByText(`「${name}」能用什么`)).toBeVisible({ timeout: 30_000 });
  await expect(sheet).toContainText(`核对码`);

  const projects = sheet.getByRole("group", { name: "项目" });
  await projects.getByRole("button", { name: "修改" }).click();
  await projects.getByRole("checkbox", { name: "所有项目（包括以后添加的）" }).setChecked(false);
  for (const box of await projects.getByRole("checkbox").all()) {
    const text = (await box.textContent()) ?? "";
    if (!text.includes("所有项目")) await box.setChecked(text.includes("/ demo"));
  }
  await projects.getByRole("button", { name: "好" }).click();
  const agents = sheet.getByRole("group", { name: "编码 agent" });
  await agents.getByRole("button", { name: "修改" }).click();
  await agents.getByRole("checkbox", { name: "Gemini CLI" }).setChecked(true);
  for (const box of await agents.getByRole("checkbox").all()) if (!(await box.textContent())?.includes("Gemini")) await box.setChecked(false);
  await sheet.getByRole("group", { name: "有效期" }).getByRole("button", { name: "30 天" }).click();
  await agents.getByRole("button", { name: "好" }).click();
  await expect(projects).toContainText("owner-mac 上的 demo");
  await expect(agents).toContainText("Gemini CLI");
  await expect(agents).not.toContainText("Codex");
  await snap(page, "sheet-connect-scope");
  await expect(sheet.locator(".sheet-foot")).toHaveCount(1);
  for (const name of ["拒绝", "取消", "批准"]) await expect(sheet.locator(".sheet-foot").getByRole("button", { name, exact: true })).toBeVisible();
  const actionRows = await sheet.getByRole("button", { name: /^(拒绝|取消|批准)$/ }).evaluateAll((buttons) => buttons.map((b) => b.getBoundingClientRect().top));
  expect(Math.max(...actionRows) - Math.min(...actionRows)).toBeLessThanOrEqual(1);
  await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].setSize(980, 620));
  await expect(sheet.getByRole("button", { name: "批准", exact: true })).toBeInViewport();
  expect(await sheet.evaluate((e) => e.scrollWidth <= e.clientWidth)).toBe(true);
  await snap(page, "sheet-connect-scope-compact");
  await page.emulateMedia({ colorScheme: "dark" });
  await snap(page, "sheet-connect-scope-dark");

  await sheet.getByRole("button", { name: "批准" }).click(); // the signer asks the owner, and says yes
  await expect(sheet.getByText(`「${name}」已连接`)).toBeVisible({ timeout: 30_000 });
  await snap(page, "sheet-connect-done");
  expect(await login.exit, login.output()).toBe(0);

  const { clients } = await api<{ clients: Client[] }>(page, "ListClients");
  const c = clients.find((x) => x.label === name);
  expect(c?.spec).toMatchObject({ machineIds: [st.localMachineId], projectIds: ["demo"], agents: ["gemini"] });
  expect((Date.parse(c!.expiresAt) - Date.now()) / 86_400_000).toBeCloseTo(30, 0);
  const list = await agent.run("project", "list");
  expect(list.match(/^owner-mac\/\S+/gm)).toEqual(["owner-mac/demo"]); // the agent sees only what it was given

  await sheet.getByRole("button", { name: "完成" }).click();
  await expect(sheet).toBeHidden();
  await app.close();
});

test("adding a machine: its install command (a Mac or Linux one, or a Windows one), then the code its terminal shows, approved here", async () => {
  const name = `build-${process.pid}`;
  const { app, page } = await launch();
  const st = await api<Status>(page, "GetStatus");
  await command(app, page, "addMachine");
  const sheet = page.getByRole("dialog");
  await expect(sheet.getByRole("heading", { name: "添加机器" })).toBeVisible();
  await expect(sheet).toContainText(`install.sh | sh -s -- ${st.connectAddress}`);
  await snap(page, "sheet-add-machine");
  // A Windows PC runs install.ps1 in PowerShell.
  await sheet.getByRole("button", { name: "Windows" }).click();
  await expect(sheet).toContainText(`(irm https://github.com/hermeshai/hermesh/releases/latest/download/install.ps1))) ${st.connectAddress}`);
  await expect(sheet).toContainText("在 PowerShell 里");
  await snap(page, "sheet-add-machine-windows");
  await sheet.getByRole("button", { name: "macOS 或 Linux" }).click();
  await sheet.getByRole("button", { name: "没带连接地址？输入配对码…" }).click();
  await expect(sheet.getByRole("heading", { name: "输入配对码" })).toBeVisible();

  const data = fs.mkdtempSync(path.join(os.tmpdir(), "hmmachine-"));
  const pair = background(path.join(path.dirname(fixture.cli), "hermeshd"), ["pair", st.connectAddress, "--name", "box", "--no-service", "--data-dir", data]);
  await sheet.getByRole("textbox", { name: "配对码" }).fill((await pair.code).toLowerCase());
  await snap(page, "sheet-enter-code");
  await sheet.getByRole("button", { name: "查找" }).click();

  await expect(sheet.getByRole("heading", { name: "一台自称「box」的机器想加入" })).toBeVisible();
  // The code entered binds its key: no verification code to compare.
  await expect(sheet).toContainText("你输入的配对码与它的密钥相符");
  await expect(sheet).not.toContainText("核对码");
  await sheet.getByLabel("机器名").fill(name);
  await snap(page, "sheet-pairing-machine");
  await sheet.getByRole("button", { name: "批准" }).click();
  await expect(page.getByRole("status").filter({ hasText: `「${name}」已加入，去设置里给它加项目` })).toBeVisible();
  await expect(sheet).toBeHidden();
  expect(await pair.exit, pair.output()).toBe(0);
  const { machines } = await api<{ machines: { machineId: string; machineName: string }[] }>(page, "ListMachines");
  const m = machines.find((x) => x.machineName === name);
  expect(m).toBeTruthy();
  await api(page, "RemoveMachine", { machineId: m!.machineId }); // it never runs; the other tests' world stays as it was
  await app.close();
});

test("in settings: two machines ask at once, one at a time; a notification's click opens it there", async () => {
  const { app, page } = await launch();
  const st = await api<Status>(page, "GetStatus");
  const before = new Set((await api<{ machines: { machineId: string }[] }>(page, "ListMachines")).machines.map((m) => m.machineId));
  const [settings] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("machines"))]);
  await settings.waitForLoadState();
  await settings.getByRole("button", { name: "添加机器…" }).click();
  const sheet = settings.getByRole("dialog");
  await expect(sheet.getByRole("heading", { name: "添加机器" })).toBeVisible();

  const names = [`box-a-${process.pid}`, `box-b-${process.pid}`];
  const pairs = names.map((n) =>
    background(path.join(path.dirname(fixture.cli), "hermeshd"), ["pair", st.connectAddress, "--name", n, "--no-service", "--data-dir", fs.mkdtempSync(path.join(os.tmpdir(), "hmmachine-"))]),
  );
  await Promise.all(pairs.map((p) => p.code));
  const heading = sheet.getByRole("heading", { name: /^一台自称「box-[ab]-\d+」的机器想加入$/ });
  await expect(heading).toBeVisible({ timeout: 30_000 });

  // The other one's notification, clicked: it opens in settings, not in the main window.
  const shown = (await heading.textContent())!.includes(names[0]) ? 0 : 1;
  const other = names[1 - shown];
  type Hook = { hermeshTest: { notifications(): { title: string; click(): void }[] } };
  await expect
    .poll(() => app.evaluate(() => (globalThis as unknown as Hook).hermeshTest.notifications().map((n) => n.title)), { timeout: 30_000 })
    .toContain(`机器「${other}」想加入`);
  await app.evaluate((_, t) => (globalThis as unknown as Hook).hermeshTest.notifications().find((n) => n.title === t)?.click(), `机器「${other}」想加入`);
  await expect(sheet.getByRole("heading", { name: `一台自称「${other}」的机器想加入` })).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  // Approved, the other follows; approved too, the sheet closes.
  await sheet.getByRole("button", { name: "批准" }).click();
  await expect(settings.getByRole("status").filter({ hasText: `「${other}」已加入` })).toBeVisible({ timeout: 30_000 });
  await expect(sheet.getByRole("heading", { name: `一台自称「${names[shown]}」的机器想加入` })).toBeVisible();
  await sheet.getByRole("button", { name: "批准" }).click();
  await expect(sheet).toBeHidden({ timeout: 30_000 });
  for (const p of pairs) expect(await p.exit, p.output()).toBe(0);
  // Both joined (each names itself once it connects; these never run).
  const { machines } = await api<{ machines: { machineId: string }[] }>(page, "ListMachines");
  const added = machines.filter((m) => !before.has(m.machineId));
  expect(added).toHaveLength(2);
  for (const m of added) await api(page, "RemoveMachine", { machineId: m.machineId }); // the other tests' world stays as it was
  await app.close();
});

test("requests wait as cards at the bottom right: a notification brings its own forward, Deny puts it off, View opens it", async () => {
  const name = `Asker ${process.pid}`;
  const other = `Other ${process.pid}`;
  const { app, page } = await launch();
  const st = await api<Status>(page, "GetStatus");
  const login = newAgent().login(st.connectAddress, name);
  const code = await login.code;
  const card = page.getByRole("region", { name: "待你批准" });
  // The overview lists it under 需要你处理; elsewhere, a card.
  await expect(page.getByRole("region", { name: "需要你处理" })).toContainText(`「${name}」想连接你的电脑`, { timeout: 30_000 });
  await expect(card).toHaveCount(0);
  await page.getByRole("complementary", { name: "边栏" }).getByText("跑很久", { exact: true }).click();
  // Nothing in the way: a card, no sheet.
  await expect(card).toContainText(`「${name}」想连接你的电脑`, { timeout: 30_000 });
  await expect(card).toContainText("核对码");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await snap(page, "request-card");

  // A second one stacks under it; its notification brings it forward.
  const second = newAgent().login(st.connectAddress, other);
  const otherCode = await second.code;
  await expect(card).toContainText("1 / 2", { timeout: 30_000 });
  await command(app, page, `pairing:${otherCode}`);
  await expect(card).toContainText(`「${other}」想连接你的电脑`);
  await card.getByRole("button", { name: "拒绝" }).click();
  await expect(page.getByRole("status").filter({ hasText: "先不处理" })).toBeVisible();
  await expect(card).toContainText(`「${name}」想连接你的电脑`);
  await expect(card).not.toContainText("1 / 2");

  // Put off too, then back from its notification, viewed and approved.
  await card.getByRole("button", { name: "拒绝" }).click();
  await expect(card).toBeHidden();
  await command(app, page, `pairing:${code}`);
  await card.getByRole("button", { name: "查看…" }).click();
  const sheet = page.getByRole("dialog", { name: `「${name}」想连接你的电脑` });
  await expect(sheet.getByText(`「${name}」能用什么`)).toBeVisible();
  await expect(card).toBeHidden();
  await snap(page, "sheet-pairing-agent");
  await sheet.getByRole("button", { name: "批准" }).click();
  await expect(page.getByRole("status").filter({ hasText: `「${name}」已连接` })).toBeVisible();
  expect(await login.exit, login.output()).toBe(0);
  // The other one too, then cut off: the other tests' world stays as it was.
  await command(app, page, `pairing:${otherCode}`);
  await card.getByRole("button", { name: "查看…" }).click();
  await page.getByRole("dialog", { name: `「${other}」想连接你的电脑` }).getByRole("button", { name: "批准" }).click();
  expect(await second.exit, second.output()).toBe(0);
  for (const c of (await api<{ clients: { grantId: string; label: string }[] }>(page, "ListClients")).clients) {
    if (c.label === name || c.label === other) await api(page, "RevokeClient", { grantId: c.grantId });
  }
  await app.close();
});

test("a hosted agent shows its app's icon only when it signs in from the app's own domain", async () => {
  // A connection to the hosted MCP gateway is named "Name (host)"; any app can call itself Muse.
  const muse = `Muse (a${process.pid}.meta.ai)`;
  const impostor = `Muse (meta.ai.p${process.pid}.example)`;
  const { app, page } = await launch();
  const st = await api<Status>(page, "GetStatus");
  for (const [name, icon] of [[muse, ".pa-who img"], [impostor, ".pa-who .pa-icon.other"]]) {
    await command(app, page, "newGrant");
    const sheet = page.getByRole("dialog", { name: "连接你的 Agent" });
    const login = newAgent().login(st.connectAddress, name);
    await expect(sheet.getByText(`「${name}」能用什么`)).toBeVisible({ timeout: 30_000 });
    await expect(sheet.locator(icon)).toBeVisible();
    await sheet.getByRole("button", { name: "批准" }).click();
    await expect(sheet.getByText(`「${name}」已连接`)).toBeVisible({ timeout: 30_000 });
    expect(await login.exit, login.output()).toBe(0);
    await sheet.getByRole("button", { name: "完成" }).click();
    await expect(sheet).toBeHidden();
  }

  const [settings] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("grants"))]);
  await settings.waitForLoadState();
  const grants = settings.getByRole("list", { name: "授权" }).getByRole("button");
  // The app by its own name, with its icon; the impostor keeps where it signed in, and a letter.
  await expect(grants.filter({ has: settings.locator(".fn", { hasText: /^Muse$/ }) }).first().locator("img")).toBeVisible();
  await expect(grants.filter({ hasText: impostor }).locator(".av")).toHaveText("M");
  await app.close();
});

test("renaming an agent from settings keeps what it may use and until when; a known app's name brings its icon", async () => {
  const name = `Renamer ${process.pid}`;
  const { app, page } = await launch();
  const st = await api<Status>(page, "GetStatus");
  const login = newAgent().login(st.connectAddress, name);
  await api(page, "ApproveClientPairing", { code: await login.code, spec: { machineIds: [st.localMachineId], projectIds: ["demo"], agents: ["gemini"], ttlDays: 7 } });
  expect(await login.exit, login.output()).toBe(0);
  const clients = async () => (await api<{ clients: Client[] }>(page, "ListClients")).clients;
  const before = (await clients()).find((c) => c.label === name)!;

  const [settings] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("grants"))]);
  await settings.waitForLoadState();
  await settings.getByRole("list", { name: "授权" }).getByRole("button", { name: new RegExp(name) }).click();
  const panel = settings.getByRole("main", { name: "授权" });
  const sheet = settings.getByRole("dialog", { name: "名字和图标" });
  // Kept as it is: nothing to sign.
  await panel.getByRole("button", { name: "名字和图标…" }).click();
  await sheet.getByRole("button", { name: "存储" }).click();
  await expect(sheet).toBeHidden();
  expect((await clients()).find((c) => c.grantId === before.grantId)!.grantVersion).toBe(before.grantVersion);

  await panel.getByRole("button", { name: "名字和图标…" }).click();
  await expect(sheet.getByRole("textbox", { name: "名字" })).toHaveValue(name);
  await sheet.getByRole("textbox", { name: "名字" }).fill("Grok Bot");
  await sheet.getByRole("button", { name: "存储" }).click(); // the signer asks, and says yes
  await expect(sheet).toBeHidden();
  await expect(panel.locator(".dhero b")).toHaveText("Grok Bot");
  await expect(panel.locator(".dhero img")).toBeVisible(); // Grok Bot's icon
  const after = (await clients()).find((c) => c.grantId === before.grantId)!;
  expect(after.label).toBe("Grok Bot");
  expect(after.expiresAt).toEqual(before.expiresAt);
  expect(after.spec).toMatchObject({ projectIds: ["demo"], agents: ["gemini"] });
  await snap(settings, "settings-grant-renamed");
  await app.close();
});

test("changing what an agent may use, and renewing it, from settings", async () => {
  const name = `Changer ${process.pid}`;
  const { app, page } = await launch();
  const st = await api<Status>(page, "GetStatus");
  const login = newAgent().login(st.connectAddress, name);
  const spec = { machineIds: [st.localMachineId], projectIds: ["demo"], agents: ["gemini"], ttlDays: 7 };
  await api(page, "ApproveClientPairing", { code: await login.code, spec });
  expect(await login.exit, login.output()).toBe(0);
  const grant = async () => (await api<{ clients: Client[] }>(page, "ListClients")).clients.find((c) => c.label === name)!;

  const [settings] = await Promise.all([app.waitForEvent("window"), page.evaluate(() => window.hermesh?.openSettings("grants"))]);
  await settings.waitForLoadState();
  await settings.getByRole("list", { name: "授权" }).getByRole("button", { name: new RegExp(name) }).click();
  const panel = settings.getByRole("main", { name: "授权" });
  await panel.getByRole("button", { name: "修改范围…" }).click();
  const sheet = settings.getByRole("dialog", { name: `修改「${name}」能用什么` });
  await expect(sheet.getByRole("group", { name: "项目" })).toContainText("owner-mac 上的 demo");
  await expect(sheet.getByRole("group", { name: "有效期" }).getByRole("button", { name: "30 天" })).toHaveAttribute("aria-pressed", "true");
  await sheet.getByRole("checkbox", { name: "所有项目（包括以后添加的）" }).setChecked(true);
  await snap(settings, "sheet-scope");
  await sheet.getByRole("button", { name: "保存并确认" }).click(); // the signer asks, and says yes
  await expect(settings.getByRole("status").filter({ hasText: `「${name}」已按新设置工作` })).toBeVisible();
  await expect(sheet).toBeHidden();
  expect((await grant()).spec).toMatchObject({ machineIds: ["*"], projectIds: ["*"], agents: ["gemini"] });

  await panel.getByRole("button", { name: "续期…" }).click();
  const renew = settings.getByRole("dialog", { name: `续期「${name}」的授权` });
  await renew.getByRole("button", { name: "90 天" }).click();
  await renew.getByRole("button", { name: "保存并确认" }).click();
  await expect(renew).toBeHidden();
  await expect.poll(async () => Math.round((Date.parse((await grant()).expiresAt) - Date.now()) / 86_400_000)).toBe(90);
  await app.close();
});
