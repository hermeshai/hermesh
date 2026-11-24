"""Routing regressions, including the CLI on real shallow PR merges."""

import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
import unittest


ROUTER = Path(__file__).with_name("route.py").resolve()
rules = runpy.run_path(str(ROUTER))
route = rules["route"]


class RouteTests(unittest.TestCase):
    def test_components(self):
        cases = [
            (["docs/x.md", "README.md", "README.zh-CN.md", "her/docs/x.md"], ""),
            (["docs/a b.md", "docs/中文.md", "docs/$(touch x)\nnotes.md"], ""),
            (["packages/HermeshKit/Tests/HermeshHerTests/ChatTests.swift"], "hermeshkit"),
            (["packages/HermeshKit/test.sh", "docs/x.md"], "hermeshkit"),
            (["packages/HermeshKit/Tests/KitTests.swift", "apps/ios/Hermesh/App.swift"], "ios hermeshkit"),
            (["apps/ios/scripts/test-demo.sh"], "ios hermeshkit"),
            (["apps/ios/Hermesh/Foo.swift"], "ios"),
            (["packages/HermeshKit/Sources/HermeshKit/A.swift"], "ios hermeshkit mac_native"),
            (["apps/desktop/src/renderer/x.tsx"], "app"),
            (["apps/desktop/src/main/ipc.ts"], "app windows_app"),
            (["apps/desktop/src/renderer/src/session/markdown/theme.ts"], "app ios windows_app"),
            (["apps/desktop/scripts/package-win.mjs", ".github/workflows/release.yml"], "packaging"),
            (["her/crates/her-core/src/a.rs"], ""),
            (["her/apps/console/src/a.ts"], "app her_integration"),
            (["internal/runtime/acp/acp.go"], "go"),
            (["cmd/hermesh-mobile/main.go"], "go ios"),
            (["cmd/hermesh-signer/panel_windows.go"], "windows windows_arm"),
            (["packages/her-client/testdata/deliveries.json"], "go app her_integration ios hermeshkit mac_native windows"),
            (["e2e/scenario_test.go"], "go go_e2e"),
            (["apps/screen/linux/Cargo.lock"], "screen_linux"),
            (["packages/HermeshKit/Package.resolved"], "ios hermeshkit mac_native"),
            (["apps/signer/macos/A.swift"], "mac_native"),
            (["docs/x.md", "apps/ios/a.swift", "apps/screen/linux/a.rs"], "ios screen_linux"),
        ]
        for paths, jobs in cases:
            with self.subTest(paths=paths):
                self.assertEqual(route(paths), set(jobs.split()))

    def test_full_ci_inputs(self):
        for path in [
            ".github/workflows/ci.yml", "scripts/ci/route.py", "Makefile",
            "package.json", "tsconfig.base.json", "go.mod", "buf.gen.yaml", "third_party/lib/a.go",
            "skills/foo/SKILL.md", "apps/new/x", "packages/new/src/x.ts", "her/new/src/x.rs",
        ]:
            with self.subTest(path=path):
                self.assertEqual(route(["docs/x.md", path]), rules["COMPONENTS"])
        self.assertEqual(route(["Makefile", "e2e/acp_test.go"]), rules["COMPONENTS"] | {"go_e2e"})
        self.assertIsNone(route([]))
        self.assertEqual(route([f"docs/{i}.md" for i in range(300)]), set())
        self.assertIsNone(route([f"docs/{i}.md" for i in range(301)]))


class GitRouteTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.repo = Path(directory.name) / "repo"
        self.repo.mkdir()
        self.env = {**os.environ, "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1"}
        self.git("init", "-q", "-b", "base")
        self.git("config", "user.name", "CI test")
        self.git("config", "user.email", "ci@example.test")
        self.git("config", "diff.renames", "true")
        self.git("config", "core.fileMode", "true")
        for path in ["internal/core.go", "apps/ios/a.swift", "apps/desktop/src/x.ts",
                     "scripts/install.sh", "docs/x.md"]:
            file = self.repo / path
            file.parent.mkdir(parents=True, exist_ok=True)
            file.write_text(f"original {path}\n", encoding="utf-8")
        self.base = self.commit()
        self.git("checkout", "-qb", "pr")

    def command(self, *args, cwd=None):
        return subprocess.check_output(args, cwd=cwd or self.repo, env=self.env, text=True).strip()

    def git(self, *args):
        return self.command("git", *args)

    def commit(self):
        self.git("add", "--all")
        self.git("commit", "-qm", "Fixture")
        return self.git("rev-parse", "HEAD")

    def merge(self, depth=2):
        self.head = self.commit()
        self.git("checkout", "-q", "base")
        self.git("merge", "-q", "--no-ff", "pr", "-m", "PR test merge")
        checkout = self.repo.parent / "checkout"
        self.git("clone", "-q", "--depth", str(depth), self.repo.as_uri(), str(checkout))
        return checkout

    def output(self, checkout, event="pull_request", head=None):
        result = self.command(sys.executable, "-I", str(ROUTER), event,
                              self.base, head or self.head, cwd=checkout)
        return dict(line.split("=", 1) for line in result.splitlines())

    def test_rename_from_core_to_docs(self):
        self.git("mv", "internal/core.go", "docs/core.md")
        self.assertEqual(self.output(self.merge())["go"], "true")

    def test_rename_from_ios_to_core(self):
        self.git("mv", "apps/ios/a.swift", "internal/a.go")
        result = self.output(self.merge())
        self.assertEqual((result["go"], result["ios"]), ("true", "true"))

    def test_rename_both_components_and_delete(self):
        (self.repo / "apps/screen/linux").mkdir(parents=True)
        self.git("mv", "apps/ios/a.swift", "apps/screen/linux/a.swift")
        self.git("rm", "-q", "apps/desktop/src/x.ts")
        result = self.output(self.merge())
        self.assertEqual(result["mode"], "routed")
        self.assertEqual({k for k, v in result.items() if v == "true"}, {"ios", "screen_linux", "app"})

    def test_affected_simulator_suite_and_her_docs(self):
        for path in ("apps/ios/Hermesh/Views/Screen/Frame.swift", "her/docs/cli/protocol-reference.md"):
            file = self.repo / path
            file.parent.mkdir(parents=True, exist_ok=True)
            file.touch()
        result = self.output(self.merge())
        self.assertEqual(result["ios_suite"], "ScreenViewportUITests")
        self.assertEqual(result["her_static"], "true")
        self.assertEqual(result["ios"], "true")
        self.assertEqual(result["go"], "false")

    def test_session_views_run_both_session_suites(self):
        file = self.repo / "apps/ios/Hermesh/Views/Session/Tools.swift"
        file.parent.mkdir(parents=True, exist_ok=True)
        file.touch()
        result = self.output(self.merge())
        self.assertEqual(result["ios_suite"], "SessionDetailUITests ToolStepsUITests")

    def test_console_skips_desktop_only_for_isolated_changes(self):
        file = self.repo / "her/apps/console/src/editor.ts"
        file.parent.mkdir(parents=True)
        file.touch()
        tip = self.commit()
        self.assertEqual(self.output(self.repo, "push", tip)["console_only"], "true")
        (self.repo / "apps/desktop/src/x.ts").unlink()
        self.assertNotIn("console_only", self.output(self.repo, "push", self.commit()))

    def test_file_mode_only(self):
        (self.repo / "scripts/install.sh").chmod(0o755)
        self.assertEqual(self.output(self.merge())["go"], "true")

    def test_unusual_names_and_non_pr_events(self):
        (self.repo / "docs" / "中文 $(touch x)\nmode=full.md").touch()
        checkout = self.merge()
        result = self.output(checkout)
        self.assertEqual(result.pop("mode"), "routed")
        self.assertEqual(set(result.values()), {"false"})
        self.assertEqual(self.output(checkout, head=self.base), {"mode": "full"})
        for event in ("push", "workflow_dispatch", "merge_group"):
            with self.subTest(event=event):
                self.assertEqual(self.output(checkout, event=event), {"mode": "full"})

    def test_multi_commit_push_and_manual_tip(self):
        (self.repo / "apps/ios/a.swift").unlink()
        first = self.commit()
        self.git("mv", "apps/desktop/src/x.ts", "docs/x.ts")
        tip = self.commit()
        for event, base, expected in [("push", self.base, {"app", "ios"}),
                                      ("workflow_dispatch", first, {"app"})]:
            result = self.command(sys.executable, "-I", str(ROUTER), event, base, tip)
            self.assertIn("mode=routed", result.splitlines())
            self.assertEqual({line.split("=")[0] for line in result.splitlines()
                              if line.endswith("=true")}, expected)
        for base in ("0" * 40, "bad"):
            self.assertEqual(self.command(sys.executable, "-I", str(ROUTER), "push", base, tip), "mode=full")

    def test_missing_parent(self):
        (self.repo / "docs" / "x.md").unlink()
        self.assertEqual(self.output(self.merge(depth=1)), {"mode": "full"})


if __name__ == "__main__":
    unittest.main()
