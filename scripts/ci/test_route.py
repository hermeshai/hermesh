"""Routing regressions, including the CLI on real shallow PR merges."""

import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
import unittest


ROUTER = Path(__file__).with_name("route.py").resolve()
route = runpy.run_path(str(ROUTER))["route"]


class RouteTests(unittest.TestCase):
    def test_components(self):
        cases = [
            (["docs/x.md", "README.md", "README.zh-CN.md", "her/docs/x.md"], ""),
            (["docs/a b.md", "docs/中文.md", "docs/$(touch x)\nnotes.md"], ""),
            (["apps/ios/Hermesh/Foo.swift"], "ios"),
            (["packages/HermeshKit/Sources/HermeshKit/A.swift"], "ios app"),
            (["apps/desktop/src/renderer/x.tsx"], "app windows_app"),
            (["apps/desktop/src/renderer/src/session/markdown/theme.ts"], "app windows_app ios"),
            (["apps/desktop/package.json"], "app windows_app ios win_package"),
            (["apps/desktop/scripts/package-win.mjs"], "app windows_app go windows_e2e win_package"),
            (["apps/desktop/build/config"], "app windows_app go windows_e2e win_package"),
            (["packages/core/src/x.ts", "packages/her-client/src/x.ts", "packages/ui/src/x.ts"],
             "app windows_app"),
            (["her/crates/her-core/src/x.rs"], "app windows_e2e windows_app win_package"),
            (["her/apps/chrome-extension/src/background.js"], "app windows_e2e windows_app win_package"),
            (["her/apps/console/src/a.ts"], "app windows_app"),
            (["e2e/x_test.go"], "go screen_linux windows windows_e2e app windows_app"),
            (["bench/fakecodex/a.go"], "go screen_linux windows windows_e2e app windows_app"),
            (["apps/screen/linux/src/a.rs"], "screen_linux"),
            (["apps/screen/macos/A.swift"], "app go"),
            (["apps/signer/macos/A.swift"], "app go"),
            (["deploy/relay/Dockerfile"], "go"),
            (["docs/x.md", "apps/ios/a.swift", "apps/screen/linux/a.rs"], "ios screen_linux"),
        ]
        for paths, jobs in cases:
            with self.subTest(paths=paths):
                self.assertEqual(route(paths), set(jobs.split()))

    def test_full_ci_inputs(self):
        for path in [
            ".github/workflows/her-ci.yml", "scripts/ci/route.py", "scripts/install.sh",
            "Makefile", "package.json", "tsconfig.base.json", "pnpm-workspace.yaml",
            "go.mod", "her/go.mod", "buf.gen.yaml", "apps/ios/.gitattributes",
            "internal/peer/x.go", "cmd/hermesh-mobile/main.go", "pkg/x.go",
            "third_party/lib/a.go", "proto/local/v1/local.proto", "gen/go/a.pb.go",
            "packages/HermeshKit/Sources/HermeshProto/a.pb.swift",
            "packages/proto/a.ts", "packages/her-protocol/a.ts", "her/crates/her-protocol/schema.json",
            "internal/preview/testdata/vectors.json", "internal/owner/testdata/her_reply.json",
            "packages/her-client/testdata/deliveries.json",
            "pnpm-lock.yaml", "her/Cargo.lock", "apps/screen/linux/Cargo.lock",
            "packages/HermeshKit/Package.resolved", "apps/desktop/package-lock.json",
            "docs/tools/uv.lock", "her/tools/go.sum", "her/rust-toolchain.toml",
            "her/Cargo.toml", "her/.cargo/config.toml", "her/apps/console/package.json",
            "skills/foo/SKILL.md", "apps/new/x", "packages/new/src/x.ts", "her/new/src/x.rs",
        ]:
            with self.subTest(path=path):
                self.assertIsNone(route(["docs/x.md", path]))
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
        self.assertEqual(self.output(self.merge()), {"mode": "full"})

    def test_rename_from_ios_to_core(self):
        self.git("mv", "apps/ios/a.swift", "internal/a.go")
        self.assertEqual(self.output(self.merge()), {"mode": "full"})

    def test_rename_both_components_and_delete(self):
        (self.repo / "apps/screen/linux").mkdir(parents=True)
        self.git("mv", "apps/ios/a.swift", "apps/screen/linux/a.swift")
        self.git("rm", "-q", "apps/desktop/src/x.ts")
        result = self.output(self.merge())
        self.assertEqual(result, {
            "mode": "routed", "go": "false", "screen_linux": "true", "windows": "false",
            "windows_arm": "false", "windows_e2e": "false", "app": "true",
            "windows_app": "true", "ios": "true", "win_package": "false",
        })

    def test_file_mode_only(self):
        (self.repo / "scripts/install.sh").chmod(0o755)
        self.assertEqual(self.output(self.merge()), {"mode": "full"})

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

    def test_missing_parent(self):
        (self.repo / "docs" / "x.md").unlink()
        self.assertEqual(self.output(self.merge(depth=1)), {"mode": "full"})


if __name__ == "__main__":
    unittest.main()
