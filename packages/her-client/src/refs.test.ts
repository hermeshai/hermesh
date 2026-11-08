import assert from "node:assert/strict";
import { test } from "node:test";
import { fileOfCode, fileOfLink, hiddenChars, loopback, shellLanguage, shortURL, webOf, webOfCode } from "./refs.ts";

test("a link's target is a file with the lines it points to", () => {
  assert.deepEqual(fileOfLink("src/config.ts"), { path: "src/config.ts" });
  assert.deepEqual(fileOfLink("src/config.ts:42"), { path: "src/config.ts", line: 42 });
  assert.deepEqual(fileOfLink("src/config.ts:42:7"), { path: "src/config.ts", line: 42 });
  assert.deepEqual(fileOfLink("src/config.ts#L42-L60"), { path: "src/config.ts", line: 42, endLine: 60 });
  assert.deepEqual(fileOfLink("src/config.ts:42-60"), { path: "src/config.ts", line: 42, endLine: 60 });
  assert.deepEqual(fileOfLink("src/config.ts#L7"), { path: "src/config.ts", line: 7 });
  assert.deepEqual(fileOfLink("/Users/me/a.md"), { path: "/Users/me/a.md" });
  assert.deepEqual(fileOfLink("~/notes/a.md"), { path: "~/notes/a.md" });
  assert.deepEqual(fileOfLink("./a.md"), { path: "./a.md" });
  assert.deepEqual(fileOfLink("docs/guide.md#usage"), { path: "docs/guide.md" });
  // A file at the top of the project, with a line, is not a scheme.
  assert.deepEqual(fileOfLink("README.md:12"), { path: "README.md", line: 12 });
  assert.deepEqual(fileOfLink("Makefile:3"), { path: "Makefile", line: 3 });
  // Encoded as an address: spaces and Chinese come back.
  assert.deepEqual(fileOfLink("docs/My%20Notes/%E6%8A%A5%E5%91%8A.md:3"), { path: "docs/My Notes/报告.md", line: 3 });
  assert.deepEqual(fileOfLink("docs/报告 草稿.md"), { path: "docs/报告 草稿.md" });
  assert.deepEqual(fileOfLink("file:///Users/me/a%20b.txt#L5"), { path: "/Users/me/a b.txt", line: 5 });
  assert.deepEqual(fileOfLink("file:///C:/x/a.ts", true), { path: "C:/x/a.ts" });
  assert.deepEqual(fileOfLink("C:\\x\\a.ts:9", true), { path: "C:\\x\\a.ts", line: 9 });
  // A line 0 is no line, and then the name before the colon reads as a scheme.
  assert.equal(fileOfLink("a.ts:0"), null);
});

test("web addresses, other schemes and anchors are not files", () => {
  for (const href of ["https://example.com/a.ts", "http://localhost:5173", "mailto:a@b.co", "tel:123", "javascript:alert(1)", "data:text/html,x", "#terminal/build", "#usage", "", "  ", "file://server/share/a.txt", "C:\\x\\a.ts"]) {
    assert.equal(fileOfLink(href), null, href);
  }
});

test("inline code is a file only when it is plainly a path", () => {
  assert.deepEqual(fileOfCode("crates/her-core/src/prompt.rs:11"), { path: "crates/her-core/src/prompt.rs", line: 11 });
  assert.deepEqual(fileOfCode("package.json"), { path: "package.json" });
  assert.deepEqual(fileOfCode(".env"), { path: ".env" });
  assert.deepEqual(fileOfCode("Makefile"), { path: "Makefile" });
  assert.deepEqual(fileOfCode("apps/desktop/"), { path: "apps/desktop/" });
  assert.deepEqual(fileOfCode("apps/desktop"), { path: "apps/desktop" });
  assert.deepEqual(fileOfCode("~/Downloads/report final.xlsx"), { path: "~/Downloads/report final.xlsx" });
  assert.deepEqual(fileOfCode("/Users/me/My Notes/a.md#L3"), { path: "/Users/me/My Notes/a.md", line: 3 });
  assert.deepEqual(fileOfCode("报告/第一季度.xlsx"), { path: "报告/第一季度.xlsx" });
  // Candidates the lookup turns down (no such file), not by their look.
  assert.deepEqual(fileOfCode("feat/x"), { path: "feat/x" });
  assert.deepEqual(fileOfCode("1.2.3"), { path: "1.2.3" });
});

test("commands, globs, words and other code are not paths", () => {
  for (const code of ["cargo test", "npm run dev", "src", "build", "src/**/*.ts", "a|b", "-rf", "$HOME/a", "@hermesh/her-client", "#123", "https://x.io/a.ts", "a\nb", "/", "./", "..", "std::io", "localhost:3000", "my notes/a.md", "tel:123", "x".repeat(301) + ".ts"]) {
    assert.equal(fileOfCode(code), null, code);
  }
});

test("web addresses", () => {
  assert.equal(webOf("https://example.com/a")?.host, "example.com");
  assert.equal(webOf("http://localhost:5173/")?.port, "5173");
  assert.equal(webOf("ftp://example.com"), null);
  assert.equal(webOf("javascript:alert(1)"), null);
  assert.equal(webOf("example.com"), null);
  assert.equal(webOfCode("http://localhost:5173")?.host, "localhost:5173");
  assert.equal(webOfCode("https://api.example.com/v1/{id}"), null);
  assert.equal(webOfCode("http://<host>:8080"), null);
  assert.equal(webOfCode("curl https://x.io"), null);
});

test("this machine's hosts", () => {
  for (const h of ["localhost", "app.localhost", "127.0.0.1", "127.1.2.3", "[::1]", "0.0.0.0", "LOCALHOST"]) assert.ok(loopback(h), h);
  for (const h of ["example.com", "192.168.1.10", "localhost.example.com", "128.0.0.1"]) assert.ok(!loopback(h), h);
});

test("a bare address is shortened in the middle, its real host kept", () => {
  assert.equal(shortURL(new URL("https://www.example.com/")), "example.com");
  assert.equal(shortURL(new URL("http://localhost:5173/")), "localhost:5173");
  assert.equal(shortURL(new URL("https://github.com/hermesh/hermesh/pull/12/")), "github.com/hermesh/hermesh/pull/12");
  assert.equal(shortURL(new URL("https://example.com@evil.com/login")), "evil.com/login");
  const long = shortURL(new URL("https://docs.example.com/guides/getting-started/installation/macos/troubleshooting#permissions"), 40);
  assert.equal(long.length, 40);
  assert.ok(long.startsWith("docs.example.com/") && long.endsWith("#permissions") && long.includes("…"), long);
});

test("shell languages and hidden characters", () => {
  for (const l of ["bash", "sh", "zsh", "Shell", "console", "powershell", "cmd"]) assert.ok(shellLanguage(l), l);
  for (const l of ["python", "ts", "json", "text", ""]) assert.ok(!shellLanguage(l), l);
  assert.ok(!hiddenChars("echo 'hi'\tthere\nls -la"));
  assert.ok(hiddenChars("echo \u202Eevil"));
  assert.ok(hiddenChars("rm\u200B -rf"));
  assert.ok(hiddenChars("printf '\u001b[31m'"));
  assert.ok(hiddenChars("a\r\nb"));
});
