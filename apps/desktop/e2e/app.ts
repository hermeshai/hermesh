import { _electron as electron, type ElectronApplication, type Page } from "@playwright/test";
import { execFile, execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";

export type Fixture = {
  socket: string;
  sessions: Record<string, string>;
  cli: string;
  cliEnv: string[];
  /** Owner daemons nobody has set up yet (first-run tests; each can be set up once). */
  fresh: { socket: string; relayURL: string; claimCode: string; gate?: string }[];
};

/** Loaded first in every launch: the app's windows open without taking focus. */
export const background = path.join(import.meta.dirname, "background.cjs");

export const fixture: Fixture = JSON.parse(fs.readFileSync(process.env.HERMESH_E2E_FIXTURE ?? "", "utf8"));

/**
 * Starts the built app (pnpm build) against the fixture's daemon (or the one
 * at socket), with a new profile unless given one. The app follows the
 * system's languages, which macOS lets a single run override
 * (-AppleLanguages); the tests read the app in Chinese unless they ask for
 * other languages (language.spec.ts).
 */
export async function launch(
  socket = fixture.socket,
  o: { systemLanguages?: string[]; profile?: string } = {},
): Promise<{ app: ElectronApplication; page: Page }> {
  const languages = o.systemLanguages ?? ["zh-Hans"];
  const app = await electron.launch({
    args: ["-r", background, path.join(process.env.HERMESH_DESKTOP_OUT || path.join(import.meta.dirname, "..", "out"), "main", "index.js"), "-AppleLanguages", `(${languages.join(", ")})`],
    // Its own profile, so app instances of parallel runs don't meet.
    env: { ...process.env, HERMESH_SOCKET: socket, HERMESH_USER_DATA: o.profile ?? fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-")) },
  });
  const page = await app.firstWindow();
  return { app, page };
}

/** Runs the agent CLI, as the fixture's agent. */
export async function cli(...args: string[]): Promise<string> {
  const env = Object.fromEntries(fixture.cliEnv.map((kv) => kv.split(/=(.*)/s).slice(0, 2)));
  const { stdout } = await promisify(execFile)(fixture.cli, args, { env: { ...process.env, ...env } });
  return stdout;
}

/** With HERMESH_SCREENS=<dir>, saves a screenshot of the page there (for looking at against the design). */
export async function snap(page: Page, name: string): Promise<void> {
  const dir = process.env.HERMESH_SCREENS;
  if (!dir) return;
  fs.mkdirSync(dir, { recursive: true });
  await page.waitForTimeout(300);
  await page.screenshot({ path: path.join(dir, `${name}.png`) });
}

/** A git repository named name, for a project. */
export function gitRepo(name: string): string {
  const dir = path.join(fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "hmrepo-"))), name);
  fs.mkdirSync(dir);
  fs.writeFileSync(path.join(dir, "README.md"), `# ${name}\n`);
  const env = { ...process.env, GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@t", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@t" };
  for (const args of [["init", "-q", "-b", "main"], ["add", "-A"], ["commit", "-q", "-m", "init"]]) execFileSync("git", args, { cwd: dir, env });
  return dir;
}

/** The native folder panel can't be driven: it answers dir. */
export async function pickAnswers(app: ElectronApplication, dir: string) {
  await app.evaluate(({ dialog }, d) => {
    dialog.showOpenDialog = (async () => ({ canceled: false, filePaths: [d] })) as typeof dialog.showOpenDialog;
  }, dir);
}

/** The native menu can't be clicked: the next one shown answers id. */
export async function answerMenu(app: ElectronApplication, id: string) {
  await app.evaluate(({ ipcMain }, id) => {
    ipcMain.removeHandler("popup-menu");
    ipcMain.handle("popup-menu", () => id);
  }, id);
}
