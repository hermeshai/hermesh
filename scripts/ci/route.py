"""Select CI jobs from the paths changed by GitHub's PR test merge."""

import argparse
import os
import re
import subprocess


DESKTOP = ("app", "windows_app")
HER = (*DESKTOP, "windows_e2e", "win_package")
FLAGS = ("go", "screen_linux", "windows", "windows_arm", "windows_e2e",
         "app", "windows_app", "ios", "win_package")

# Check shared inputs before these component prefixes.
FULL_PREFIXES = (".github/", "scripts/", "cmd/", "internal/", "pkg/",
                 "third_party/", "gen/", "proto/", "packages/proto/",
                 "packages/her-protocol/", "packages/HermeshKit/Sources/HermeshProto/",
                 "her/crates/her-protocol/", "her/.cargo/")
FULL_BASENAMES = {
    ".dockerignore", ".gitattributes", ".gitignore", ".gitmodules",
    ".node-version", ".nvmrc", ".python-version", ".tool-versions",
    "bun.lockb", "go.mod", "go.sum", "go.work", "go.work.sum",
    "npm-shrinkwrap.json", "Package.resolved", "pnpm-workspace.yaml",
    "rust-toolchain", "rust-toolchain.toml",
}
DOC_FILES = {"AGENTS.md", "LICENSE", "README.md", "README.zh-CN.md"}
PREFIXES = (
    (("docs/", "her/docs/"), ()),
    (("e2e/", "bench/"), ("go", "screen_linux", "windows", "windows_e2e", *DESKTOP)),
    (("apps/ios/",), ("ios",)),
    (("packages/HermeshKit/",), ("ios", "app")),
    (("apps/signer/macos/", "apps/screen/macos/"), ("app", "go")),
    (("apps/screen/linux/",), ("screen_linux",)),
    (("apps/desktop/src/renderer/src/session/markdown/",), (*DESKTOP, "ios")),
    (("apps/desktop/scripts/", "apps/desktop/build/"),
     (*DESKTOP, "go", "windows_e2e", "win_package")),
    (("apps/desktop/", "packages/core/", "packages/her-client/", "packages/ui/",
      "her/apps/console/"), DESKTOP),
    (("deploy/",), ("go",)),
    # Her embeds the browser extension in her-cli.
    (("her/apps/chrome-extension/", "her/crates/", "her/examples/", "her/tools/"), HER),
)


def route(paths):
    """Return selected flags, or None for full CI."""
    if not paths or len(paths) > 300:
        return None

    selected = set()
    for path in paths:
        name = path.rsplit("/", 1)[-1]
        if (path.startswith(FULL_PREFIXES) or name in FULL_BASENAMES
                or re.search(r"(?i)(?:^|[-.])lock(?:\.(?:json|yaml|yml|toml))?$", name)
                or (path.startswith("packages/") and "/testdata/" in path)
                or path == "her/Cargo.toml"):
            return None
        if path in DOC_FILES:
            continue
        if path == "apps/desktop/package.json":
            selected.update((*DESKTOP, "ios", "win_package"))
            continue
        # A workspace manifest can affect frozen installs across apps.
        if name == "package.json":
            return None
        for prefixes, jobs in PREFIXES:
            if path.startswith(prefixes):
                selected.update(jobs)
                break
        else:
            return None
    return selected


def git(*args):
    return subprocess.check_output(["git", *args], stderr=subprocess.DEVNULL)


def changed_paths(base, head):
    # GitHub checks out its two-parent test merge, even for fork PRs.
    merge = git("rev-parse", "HEAD").strip()
    parents = git("rev-list", "--parents", "-n", "1", "HEAD").split()
    if parents != [merge, os.fsencode(base), os.fsencode(head)]:
        return []
    # Disabling rename detection exposes both the old and new paths.
    paths = git("diff", "--no-ext-diff", "--no-renames", "--name-only",
                "--ignore-submodules=none", "-z", "HEAD^1", "HEAD", "--")
    return os.fsdecode(paths[:-1]).split("\0") if paths.endswith(b"\0") else []


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("event")
    parser.add_argument("base", nargs="?", default="")
    parser.add_argument("head", nargs="?", default="")
    args = parser.parse_args()

    selected = None
    if args.event == "pull_request":
        try:
            selected = route(changed_paths(args.base, args.head))
        except (OSError, subprocess.CalledProcessError):
            pass

    if selected is None:
        print("mode=full")
    else:
        print("mode=routed")
        for flag in FLAGS:
            print(f"{flag}={'true' if flag in selected else 'false'}")


if __name__ == "__main__":
    main()
