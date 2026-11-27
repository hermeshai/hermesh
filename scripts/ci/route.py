"""Select CI jobs from PR test merges and pushed commit ranges."""

import argparse
import os
import re
import subprocess


FLAGS = ("go", "go_macos", "go_e2e", "linux_docker", "screen_linux", "windows", "windows_arm",
         "windows_e2e", "app", "desktop_e2e", "her_integration", "windows_app",
         "mac_native", "ios", "hermeshkit", "packaging")
# Unknown files exercise all component smoke tests. Known files in the same diff
# can still request a deeper suite, such as a changed desktop or Go E2E test.
COMPONENTS = {"go", "go_macos", "screen_linux", "windows", "windows_arm", "app",
              "windows_app", "mac_native", "ios", "hermeshkit", "packaging"}
DOC_FILES = {"AGENTS.md", "LICENSE", "README.md", "README.zh-CN.md"}
# First match wins. Keep shared inputs ahead of the directory that owns them.
RULES = (
    (("docs/", "her/docs/", "her/README.md", ".github/workflows/her-ci.yml",
      ".github/workflows/her-startup.yml"), ()),
    (("scripts/release-pubkey.pem",), ("packaging", "go", "app", "windows")),
    ((".github/workflows/release.yml", ".github/workflows/promote.yml",
      ".github/workflows/relay-image.yml", "apps/ios/scripts/testflight.sh",
      "apps/desktop/scripts/", "apps/desktop/build/", "scripts/release"), ("packaging",)),
    (("proto/", "gen/", "packages/proto/", "packages/her-protocol/",
      "her/crates/her-protocol/", "internal/preview/testdata/", "internal/owner/testdata/",
      "packages/her-client/testdata/"), ("go", "app", "her_integration", "ios", "hermeshkit", "mac_native", "windows")),
    (("apps/desktop/src/renderer/src/session/markdown/", "apps/desktop/package.json",
      "pnpm-lock.yaml", "pnpm-workspace.yaml"), ("app", "ios", "windows_app")),
    (("apps/ios/scripts/test-demo.sh",), ("ios", "hermeshkit")),
    (("apps/ios/",), ("ios",)),
    (("packages/HermeshKit/Tests/", "packages/HermeshKit/test.sh"), ("hermeshkit",)),
    (("packages/HermeshKit/",), ("ios", "hermeshkit", "mac_native")),
    (("apps/signer/macos/", "apps/screen/macos/"), ("mac_native",)),
    (("apps/screen/linux/",), ("screen_linux",)),
    (("e2e/harness", "e2e/main_test.go"), ("go", "go_e2e", "go_macos", "linux_docker", "app", "desktop_e2e",
                                           "her_integration", "windows", "windows_app", "windows_e2e", "screen_linux")),
    (("e2e/desktop_her", "apps/desktop/e2e/her", "e2e/her_", "e2e/coding_consent", "e2e/push_test"),
     ("go", "go_e2e", "app", "her_integration", "desktop_e2e")),
    (("apps/desktop/e2e/", "e2e/desktop_"), ("app", "desktop_e2e", "windows_app")),
    (("apps/desktop/src/renderer/src/her/", "packages/her-client/"), ("app", "her_integration")),
    (("apps/desktop/src/main/", "apps/desktop/src/preload/"), ("app", "windows_app")),
    (("apps/desktop/", "packages/core/", "packages/ui/"), ("app",)),
    (("her/apps/console/",), ("app", "her_integration")),
    (("internal/herhost/", "internal/herlink/"), ("app", "her_integration", "go")),
    (("her/crates/", "her/apps/chrome-extension/", "her/tools/", "her/examples/",
      "her/Cargo.", "her/.cargo/", "her/rust-toolchain"), ()),  # her-ci.yml
    (("cmd/hermesh-mobile/", "internal/mobile/"), ("go", "ios")),
    (("cmd/hermesh-signer/",), ("windows", "windows_arm")),
    (("scripts/install.ps1", "e2e/install_windows", "e2e/windows_signer"), ("windows",)),
    (("e2e/linux_screen",), ("screen_linux",)),
    (("e2e/linux_docker", "e2e/linux_firstinstall", "e2e/linux_uninstall", "e2e/linux_update",
      "e2e/linux_provider_e2e"), ("go", "linux_docker")),
    (("e2e/iphone", "e2e/mcp_iphone"), ("go", "go_macos", "ios")),
    (("e2e/", "bench/"), ("go", "go_e2e")),
    (("internal/service/",), ("go", "go_macos", "windows", "linux_docker")),
    (("internal/paths/", "internal/flock/", "internal/localipc/",
      "internal/peercred/", "internal/spawn/"), ("go", "go_macos", "windows")),
    (("scripts/install.sh", "cmd/hermeshd/", "internal/update/"), ("go", "linux_docker")),
    (("cmd/", "internal/", "pkg/", "deploy/", "scripts/test_install.py"), ("go",)),
)


def route(paths):
    """Select component and integration checks, or all suites for an unavailable diff."""
    if not paths or len(paths) > 300:
        return None
    selected = set()
    for path in paths:
        if path in DOC_FILES:
            continue
        for prefixes, jobs in RULES:
            if path.startswith(prefixes):
                selected.update(jobs)
                break
        else:
            selected.update(COMPONENTS)
        if path.endswith(".go"):
            if "_windows" in path:
                selected.add("windows")
                if path.startswith("e2e/"):
                    selected.add("windows_e2e")
            if "_darwin" in path or "_unix" in path:
                selected.update(("go", "go_macos"))
    return selected


def git(*args):
    return subprocess.check_output(["git", *args], stderr=subprocess.DEVNULL)


def changed_paths(event, base, head):
    if not all(re.fullmatch(r"[0-9a-f]{40}", sha) for sha in (base, head)):
        return []
    tip = git("rev-parse", "HEAD").strip()
    if event == "pull_request":
        # A fork PR cannot substitute its own routing rules or merge parents.
        parents = git("rev-list", "--parents", "-n", "1", "HEAD").split()
        if parents != [tip, os.fsencode(base), os.fsencode(head)]:
            return []
    elif event not in ("push", "workflow_dispatch") or os.fsencode(head) != tip:
        return []
    # Disabling rename detection exposes both the old and new paths.
    paths = git("diff", "--no-ext-diff", "--no-renames", "--name-only",
                "--ignore-submodules=none", "-z", base, "HEAD", "--")
    return os.fsdecode(paths[:-1]).split("\0") if paths.endswith(b"\0") else []


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("event")
    parser.add_argument("base", nargs="?", default="")
    parser.add_argument("head", nargs="?", default="")
    args = parser.parse_args()

    selected = None
    try:
        paths = changed_paths(args.event, args.base, args.head)
        selected = route(paths)
    except (OSError, subprocess.CalledProcessError):
        pass

    if selected is None:
        print("mode=full")
    else:
        print("mode=routed")
        if "her_integration" in selected and all(path in DOC_FILES or path.startswith(
                ("her/apps/console/", "docs/", "her/docs/")) for path in paths):
            print("console_only=true")
        if any(path.startswith(("her/docs/", "her/apps/console/")) or path == "her/README.md" for path in paths):
            print("her_static=true")
        # These suites run on a fresh simulator without an external fixture server.
        suites = []
        for prefix, name in (("apps/ios/Hermesh/Views/Screen/", "ScreenViewportUITests"),
                             ("apps/ios/Hermesh/Views/Session/", "SessionDetailUITests"),
                             ("apps/ios/Hermesh/Views/Session/", "ToolStepsUITests"),
                             ("apps/ios/Hermesh/Views/Her/", "HerReviewUITests")):
            if any(path.startswith(prefix) or path == f"apps/ios/HermeshUITests/{name}.swift"
                   for path in paths):
                suites.append(name)
        if "apps/ios/HermeshUITests/DemoUITests.swift" in paths:
            print("ios_suite=full")
        elif suites:
            print("ios_suite=" + " ".join(suites))
        for flag in FLAGS:
            print(f"{flag}={'true' if flag in selected else 'false'}")


if __name__ == "__main__":
    main()
