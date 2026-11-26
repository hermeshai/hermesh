import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  cleanShellCommand,
  effectiveCodeLang,
  findSecretSpans,
  hiddenChars,
  inspectRunnableShell,
  maskSecrets,
  parseTerminalLines,
  runnableShellCommand,
  stripTerminalEscapes,
} from "./terminalText.ts";

describe("terminalText", () => {
  it("sniffs untagged and text CLI code blocks without misclassifying prose or JSON", () => {
    assert.deepEqual(effectiveCodeLang("", "git status -sb"), { lang: "bash", shell: true, sniffed: true });
    assert.deepEqual(effectiveCodeLang("text", "$ pnpm --filter @hermesh/desktop test"), { lang: "bash", shell: true, sniffed: true });
    assert.deepEqual(effectiveCodeLang("", "FOO=1 cargo test --workspace"), { lang: "bash", shell: true, sniffed: true });
    assert.deepEqual(effectiveCodeLang("", '{"name": "hermesh"}'), { lang: "", shell: false, sniffed: false });
    assert.deepEqual(effectiveCodeLang("", "make sure your server is running before testing."), { lang: "", shell: false, sniffed: false });
  });

  it("strips leading $ and % prompts and omits non-command output lines in console blocks", () => {
    assert.equal(cleanShellCommand("$ git status -sb"), "git status -sb");
    assert.equal(
      cleanShellCommand("$ echo hello\nhello\n$ git diff --stat\n 1 file changed"),
      "echo hello\ngit diff --stat",
    );
    assert.equal(
      cleanShellCommand("$ curl -X POST \\\n  https://example.com/api\n{\"ok\":true}"),
      "curl -X POST \\\n  https://example.com/api",
    );
  });

  it("masks API keys and tokens while leaving normal code untouched", () => {
    const cmd = 'OPENAI_API_KEY="sk-proj-1234567890abcdefghij" curl -H "Authorization: Bearer ghp_1234567890abcdefghijklmnop" https://api.example.com';
    assert.equal(findSecretSpans(cmd).length, 2);
    const masked = maskSecrets(cmd);
    assert.ok(!masked.includes("1234567890abcdefghij"));
    assert.ok(!masked.includes("1234567890abcdefghijklmnop"));
    assert.ok(masked.includes("••••••••"));
  });

  it("allows single-line and multi-line scripts with shebangs/comments, flags destructive commands, and blocks TUIs/bidi", () => {
    assert.deepEqual(inspectRunnableShell("bash", "$ go test ./..."), {
      command: "go test ./...",
      destructive: false,
    });
    assert.equal(runnableShellCommand("", "pnpm -r typecheck"), "pnpm -r typecheck");

    // Multi-line bash scripts with shebang and conditionals are runnable!
    const script = `#!/usr/bin/env bash\n\ndir="\${1:-.}"\nif [ ! -d "$dir" ]; then\n  exit 1\nfi\nls -la "$dir"`;
    const plan = inspectRunnableShell("bash", script);
    assert.ok(plan);
    assert.equal(plan.destructive, false);
    assert.ok(plan.command.includes('ls -la "$dir"'));

    // Comment-only blocks are not runnable
    assert.equal(inspectRunnableShell("bash", "#!/usr/bin/env bash\n# nothing here"), null);

    // Destructive multi-line scripts are flagged for inline confirmation
    const cleanScript = "# Clean build cache\nrm -rf dist apps/desktop/out\necho done";
    assert.deepEqual(inspectRunnableShell("bash", cleanScript), {
      command: cleanScript,
      destructive: true,
    });

    // Interactive TUIs / bare REPLs are blocked even inside multi-line scripts
    assert.equal(runnableShellCommand("bash", "cd foo\nvim Cargo.toml"), null);
    assert.equal(runnableShellCommand("bash", "htop"), null);
    assert.equal(runnableShellCommand("bash", "python3"), null);
    assert.equal(runnableShellCommand("bash", "python3 -m unittest"), "python3 -m unittest");

    // Trojan-source bidi/zero-width hidden characters are blocked
    assert.equal(hiddenChars("echo \u202Eevil"), true);
    assert.equal(runnableShellCommand("bash", "echo \u202Eevil"), null);
  });

  it("parses ANSI SGR colors, carriage-return progress bars, and unified diffs", () => {
    const ansi = "10%\r50%\r\x1b[1;32mPASS\x1b[0m \x1b[31m0 failed\x1b[0m";
    assert.equal(stripTerminalEscapes(ansi), "PASS 0 failed");
    const lines = parseTerminalLines(ansi);
    assert.equal(lines.length, 1);
    assert.equal(lines[0]!.spans[0]?.text, "PASS");
    assert.equal(lines[0]!.spans[0]?.style?.fgClass, "ansi-green");
    assert.equal(lines[0]!.spans[0]?.style?.bold, true);
    assert.equal(lines[0]!.spans[2]?.style?.fgClass, "ansi-red");

    const diff = parseTerminalLines("@@ -1,2 +1,2 @@\n-old line\n+new line");
    assert.equal(diff[0]!.diff, "hunk");
    assert.equal(diff[1]!.diff, "del");
    assert.equal(diff[2]!.diff, "add");
  });
});
