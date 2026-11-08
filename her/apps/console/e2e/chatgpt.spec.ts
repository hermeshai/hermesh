import { expect, test } from "@playwright/test";
import path from "node:path";
import { startStandIn, type StandIn } from "../scripts/stand-in.ts";

// A ChatGPT sign-in from the console of a Her Server without a browser (docs/her-server.md
// §5.4): the page asks the owner's device for a catcher, her makes the authorization request for
// it, the browser signs in (her's ChatGPT fixture stands in for OpenAI's sign-in and models) and
// lands on the catcher, and what it brought completes the sign-in on her.

type Fixture = { base: string; state: { errors: Error[] }; close(): Promise<void> };

let chatgpt: Fixture;
let site: StandIn;
test.beforeAll(async () => {
  const at = path.resolve(import.meta.dirname, "../../../crates/her-connections/tests/chatgpt-fixture.mjs");
  chatgpt = await ((await import(at)) as { fixture: () => Promise<Fixture> }).fixture();
  site = await startStandIn({ herEnv: { HER_TEST_CHATGPT_BASE: chatgpt.base } });
});
test.afterAll(async () => {
  await site?.close();
  await chatgpt?.close();
});

test("signing in to ChatGPT ends on the owner's device and connects the environment", async ({ page, context }) => {
  await page.goto(`${site.url}#models`);
  const row = page.getByTestId("chatgpt-connection");
  await expect(row).toContainText("Sign in with your ChatGPT account");
  const signIn = context.waitForEvent("page");
  await row.getByRole("button", { name: "Sign In" }).click();
  // The browser's tab: OpenAI's page sends it back to the catcher on this device.
  const tab = await signIn;
  await expect(tab.getByText("You can close this window and go back to Hermesh.")).toBeVisible();
  expect(new URL(tab.url()).hostname).toBe("127.0.0.1");
  await expect(row).toContainText("tester@example.invalid");
  await expect(row).toContainText(/Connected · \d+ of \d+ models shown/);
  await expect(page.getByText("Signed in to ChatGPT")).toBeVisible();
  await expect(row.getByRole("button", { name: "Manage Models" })).toBeVisible();
  expect(chatgpt.state.errors.map((e) => e.message)).toEqual([]);

  await row.getByRole("button", { name: "Disconnect" }).click();
  await expect(row.getByRole("button", { name: "Sign In" })).toBeVisible();
});
