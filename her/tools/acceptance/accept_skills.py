#!/usr/bin/env python3
"""Acceptance check for skills with a real model (docs/kernel/skill-design.md §13, phase 1).

Creates a throw-away git project whose `.agents/skills/weekly-report` skill says to read
`references/format.md` and to run `scripts/gen.sh`, then runs `her exec` with a task that
matches the skill's description without naming it. It passes when the session shows that:

  1. the model read the SKILL.md and the reference file with read;
  2. it ran the script through bash;
  3. report.md starts with the line that only the script knows.

A second session in the same project then shows the prompt cache: its first request reads
the shared system prompt from the provider's cache (cache_read in the usage).

`--layer user` installs the skill in `~/.agents/skills` instead: outside the workspace, so the
model has to resolve the skill's relative paths against the skill's directory.

    DEEPSEEK_API_KEY=... python3 tools/acceptance/accept_skills.py --her target/release/her

Uses a temporary HER_HOME and HOME; the only network traffic is the model requests.
"""

import argparse
import json
import os
import pathlib
import subprocess
import sys
import tempfile

MARKER = "# WEEKLY-REPORT 7Q2X-SKILL"

SKILL = """---
name: weekly-report
description: >-
  Generate the team's weekly status report file (report.md) in the team's fixed format.
  Use when the user asks for a weekly report or a status report.
---
# Weekly report

1. Read `references/format.md` to learn the required format.
2. Run `scripts/gen.sh` from the project directory. It creates report.md and writes the
   header line the team requires.
3. Append one line to report.md that summarizes what the user asked for.
"""

FORMAT = """The report must start with the header line that scripts/gen.sh writes. Never write
that header by hand: the script is the only source of it.
"""

GEN = f"""#!/bin/sh
echo '{MARKER}' > report.md
echo 'report.md created'
"""


def run(her, cwd, env, prompt, model):
    cmd = [her, "exec", "--output", "stream-json", "-m", model, "--permission-mode", "bypass-permissions", prompt]
    proc = subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True, timeout=600)
    events = [json.loads(line) for line in proc.stdout.splitlines() if line.startswith("{")]
    if proc.returncode != 0:
        sys.exit(f"her exited with {proc.returncode}:\n{proc.stderr}")
    return events


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--her", default="target/release/her")
    parser.add_argument("--model", default="deepseek/deepseek-flash")
    parser.add_argument("--keep", action="store_true", help="keep the project and its session logs")
    parser.add_argument(
        "--layer",
        choices=["project", "user"],
        default="project",
        help="install the skill in the project or in ~/.agents/skills, outside the workspace",
    )
    args = parser.parse_args()
    her = str(pathlib.Path(args.her).resolve())

    with tempfile.TemporaryDirectory(prefix="her-skills-", delete=not args.keep) as tmp:
        if args.keep:
            print(f"  kept in {tmp}")
        base = pathlib.Path(tmp).resolve()
        home, user, proj = base / "home", base / "user", base / "proj"
        for d in (home, user, proj / ".git"):
            d.mkdir(parents=True)
        (proj / ".git" / "HEAD").write_text("ref: refs/heads/main\n")
        skill = (proj if args.layer == "project" else user) / ".agents" / "skills" / "weekly-report"
        (skill / "references").mkdir(parents=True)
        (skill / "scripts").mkdir()
        (skill / "SKILL.md").write_text(SKILL)
        (skill / "references" / "format.md").write_text(FORMAT)
        (skill / "scripts" / "gen.sh").write_text(GEN)
        env = dict(os.environ, HER_HOME=str(home), HOME=str(user), NO_COLOR="1")

        events = run(her, proj, env, "Please prepare this week's status report for the team.", args.model)
        calls = [(e["name"], e["args"]) for e in events if e["type"] == "tool_started"]
        for name, call in calls:
            print(f"  {name}: {json.dumps(call, ensure_ascii=False)}")
        reads = [call.get("path", "") for name, call in calls if name == "read"]
        commands = [call.get("command", "") for name, call in calls if name == "bash"]
        # The skill only says to read the reference; printing it with bash counts too.
        printed = [c for c in commands if "references/format.md" in c and any(w in c for w in ("cat ", "head ", "sed "))]
        checks = {
            "read SKILL.md": any(p.endswith("weekly-report/SKILL.md") for p in reads),
            "read the reference": any(p.endswith("references/format.md") for p in reads) or bool(printed),
            "ran the script": any("weekly-report/scripts/gen.sh" in c for c in commands),
            "report.md comes from the script": (proj / "report.md").exists()
            and (proj / "report.md").read_text().splitlines()[:1] == [MARKER],
        }

        second = run(her, proj, env, "In one sentence: which skills can you use here?", args.model)
        first_turn = next(e for e in second if e["type"] == "turn_completed")["usage"]
        total_in = first_turn.get("input", 0) + first_turn.get("cache_read", 0) + first_turn.get("cache_write", 0)
        cached = first_turn.get("cache_read", 0)
        print(f"  second session, first request: {cached} of {total_in} input tokens read from the cache")
        checks["the second session hits the cache"] = cached > 0
        answer = next(e for e in second if e["type"] == "task_completed").get("last_message") or ""
        print(f"  second session answered: {answer[:200]}")

        for name, ok in checks.items():
            print(f"{'PASS' if ok else 'FAIL'}  {name}")
        sys.exit(0 if all(checks.values()) else 1)


if __name__ == "__main__":
    main()
