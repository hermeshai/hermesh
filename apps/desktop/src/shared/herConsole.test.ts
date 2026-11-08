import assert from "node:assert/strict";
import { test } from "node:test";
import { consoleURL, inConsole, opensOutside } from "./herConsole.ts";

test("a console's address is http on 127.0.0.1 with a port, as hermeshd gives it; nothing else opens", () => {
  assert.equal(consoleURL("http://127.0.0.1:53111/her/m_1/4f2a/")?.href, "http://127.0.0.1:53111/her/m_1/4f2a/");
  for (const bad of [
    "https://127.0.0.1:53111/her/m_1/t/",
    "http://localhost:53111/her/m_1/t/",
    "http://127.0.0.1/her/m_1/t/",
    "http://user:pw@127.0.0.1:53111/her/",
    "http://127.0.0.2:53111/",
    "http://evil.example/her/",
    "file:///etc/passwd",
    "javascript:alert(1)",
    "",
  ]) {
    assert.equal(consoleURL(bad), null, bad);
  }
});

test("the console's window stays on its origin; https links go to the browser", () => {
  const c = consoleURL("http://127.0.0.1:53111/her/m_1/4f2a/")!;
  assert.equal(inConsole(c, "http://127.0.0.1:53111/her/m_1/4f2a/models"), true);
  assert.equal(inConsole(c, "http://127.0.0.1:53112/her/m_1/4f2a/"), false);
  assert.equal(inConsole(c, "https://chatgpt.com/auth"), false);
  assert.equal(inConsole(c, "not a url"), false);
  assert.equal(opensOutside("https://auth.openai.com/authorize?x=1"), true);
  assert.equal(opensOutside("http://example.com/"), false);
  assert.equal(opensOutside("file:///tmp/x"), false);
});
