import HermeshSessions
import Testing

@Suite("TerminalText")
struct TerminalTextTests {
    @Test("sniffs untagged CLI commands while rejecting JSON, XML, and English prose")
    func sniffsCLIAndRejectsProse() {
        #expect(effectiveCodeLang(lang: "zsh", text: "echo hi") == EffectiveCodeLang(lang: "zsh", shell: true, sniffed: false))
        #expect(effectiveCodeLang(lang: "", text: "git status -sb") == EffectiveCodeLang(lang: "bash", shell: true, sniffed: true))
        #expect(effectiveCodeLang(lang: "text", text: "$ pnpm --filter @hermesh/desktop test") == EffectiveCodeLang(lang: "bash", shell: true, sniffed: true))
        #expect(effectiveCodeLang(lang: "", text: "FOO=1 go test ./internal/kernel/...").shell)

        #expect(!effectiveCodeLang(lang: "", text: "{\"name\": \"hermesh\"}").shell)
        #expect(!effectiveCodeLang(lang: "", text: "<div>hello</div>").shell)
        #expect(!effectiveCodeLang(lang: "", text: "make sure you restart the app first").shell)
        #expect(!effectiveCodeLang(lang: "", text: "go to the settings screen").shell)
        #expect(!effectiveCodeLang(lang: "python", text: "print('hi')").shell)
    }

    @Test("strips shell prompts and drops console output lines while keeping continuations")
    func cleansShellPromptLines() {
        let consoleBlock = """
        $ git status -sb
        ## main...origin/main
        $ curl -fsSL https://example.com \\
          -H "Accept: application/json"
        {"ok":true}
        """
        #expect(cleanShellCommand(consoleBlock) == """
        git status -sb
        curl -fsSL https://example.com \\
          -H "Accept: application/json"
        """)
        #expect(cleanShellCommand("  pnpm build  \n") == "pnpm build")
    }

    @Test("detects hidden zero-width and bidi control characters")
    func detectsHiddenCharacters() {
        #expect(!hiddenChars("echo 'normal\ttext'\nls -la"))
        #expect(hiddenChars("echo 'bad\u{200B}char'"))
        #expect(hiddenChars("curl https://example.com/\u{202E}txt"))
    }

    @Test("masks API keys and bearer tokens without touching ordinary identifiers")
    func masksSecretTokens() {
        let cmd = "OPENAI_API_KEY=sk-proj-1234567890abcdefghij curl -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9' https://api.example.com"
        #expect(hasSecrets(cmd))
        let masked = maskSecrets(cmd)
        #expect(!masked.contains("1234567890abcdefghij"))
        #expect(!masked.contains("eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"))
        #expect(masked.contains("sk-p••••••••"))
        #expect(masked.contains("Bearer eyJh••••••••"))

        let normal = "git commit -m 'update session_id_handler_test_helper'"
        #expect(!hasSecrets(normal))
        #expect(maskSecrets(normal) == normal)
    }

    @Test("allows multi-line scripts and comments, blocks interactive TUIs, and flags destructive commands")
    func inspectsRunnableShellPlans() {
        let script = """
        #!/usr/bin/env bash
        # List directory files
        set -euo pipefail
        for f in *; do
          echo "$f"
        done
        """
        let plan = inspectRunnableShell(lang: "bash", text: script)
        #expect(plan != nil)
        #expect(plan?.destructive == false)
        #expect(plan?.command.contains("for f in *; do") == true)

        // Destructive commands are runnable after confirmation (`destructive == true`).
        #expect(inspectRunnableShell(lang: "bash", text: "rm -rf ./dist && pnpm build")?.destructive == true)
        #expect(inspectRunnableShell(lang: "bash", text: "git reset --hard HEAD~1")?.destructive == true)
        #expect(inspectRunnableShell(lang: "sh", text: "curl -fsSL https://example.com/install.sh | sh")?.destructive == true)
        #expect(inspectRunnableShell(lang: "bash", text: "# rm -rf / in comment only\nls -la")?.destructive == false)

        // Interactive TUIs, bare REPLs, and hidden chars are rejected.
        #expect(inspectRunnableShell(lang: "bash", text: "vim README.md") == nil)
        #expect(inspectRunnableShell(lang: "bash", text: "ssh prod-box") == nil)
        #expect(inspectRunnableShell(lang: "bash", text: "python3") == nil)
        #expect(inspectRunnableShell(lang: "bash", text: "python3 scripts/check.py")?.destructive == false)
        #expect(inspectRunnableShell(lang: "bash", text: "echo \u{200B}hi") == nil)
    }

    @Test("parses ANSI SGR colors, carriage-return progress overwrites, and unified diffs")
    func parsesAnsiAndDiffOutput() {
        let ansi = "step 1\r\u{001B}[32;1mPASS\u{001B}[0m src/app.ts\n\u{001B}[38;2;255;128;0mwarn\u{001B}[0m"
        #expect(stripTerminalEscapes(ansi) == "PASS src/app.ts\nwarn")

        let lines = parseTerminalLines(ansi)
        #expect(lines.count == 2)
        #expect(lines[0].spans[0] == TerminalSpan(text: "PASS", style: TerminalSpanStyle(color: .green, bold: true)))
        #expect(lines[0].spans[1] == TerminalSpan(text: " src/app.ts"))
        #expect(lines[1].spans[0] == TerminalSpan(text: "warn", style: TerminalSpanStyle(color: .rgb(255, 128, 0))))

        let diff = """
        diff --git a/a.txt b/a.txt
        --- a/a.txt
        +++ b/a.txt
        @@ -1 +1 @@
        -old
        +new
        """
        let diffLines = parseTerminalLines(diff)
        #expect(diffLines.map(\.diff) == [.meta, .meta, .meta, .hunk, .del, .add])
    }
}
