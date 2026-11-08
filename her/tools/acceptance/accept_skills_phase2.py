#!/usr/bin/env python3
"""Acceptance check for skills phase 2 with a real model (docs/kernel/skill-design.md §13).

Creates a throw-away git project with two skills and runs `her exec` against the model.

`/skill:name` (D-82–D-88): `release-notes` may only be invoked by the user
(disable-model-invocation), so it is not in the catalog. `/skill:release-notes 2.4.1` must
give the model its instructions with the message: RELEASE.md starts with a line that only the
skill knows, the log keeps the words as typed and names the skill, and the model needs no read
of the SKILL.md. Mentions anywhere in a message load each skill once, in order; a mention in
backticks, a bare `/name` and an unknown name load nothing (the unknown one warns).

Compaction (D-71): the model reads `commit-style` from the catalog to write a commit message,
the session is compacted with `/compact`, and a later request must still follow the skill.
The compaction keeps the skill's instructions (`kept_skills`, and their text in the log).

    DEEPSEEK_API_KEY=... python3 tools/acceptance/accept_skills_phase2.py --her target/release/her

Uses a temporary HER_HOME and HOME; the only network traffic is the model requests.
"""

import argparse
import json
import os
import pathlib
import subprocess
import sys
import tempfile

NOTES_MARKER = "R8K-NOTES"
COMMIT_TAG = "[ZX9-LOG]"

RELEASE_NOTES = f"""---
name: release-notes
description: Write the release notes file for a version.
disable-model-invocation: true
argument-hint: "[version]"
---
# Release notes

Write RELEASE.md in the project directory. Its first line must be exactly
`# Release <version> · {NOTES_MARKER}`, where <version> is the version the user gave.
Then add one line: `Changes since the last release.` Do nothing else.
"""

COMMIT_STYLE = f"""---
name: commit-style
description: >-
  This repository's rules for commit messages. Use whenever you write a commit message.
---
# Commit messages

Every commit message starts with the tag `{COMMIT_TAG}`, a space, then an imperative summary
of at most 60 characters. Nothing else goes on that line. Write the message to the file the
user names, never to the terminal only.
"""


def run(her, cwd, env, prompt, model, resume=None):
    cmd = [her, "exec", "--output", "stream-json", "-m", model, "--permission-mode", "bypass-permissions"]
    if resume:
        cmd += ["--resume", resume]
    proc = subprocess.run(cmd + [prompt], cwd=cwd, env=env, capture_output=True, text=True, timeout=600)
    events = [json.loads(line) for line in proc.stdout.splitlines() if line.startswith("{")]
    if proc.returncode != 0:
        sys.exit(f"her exited with {proc.returncode}:\n{proc.stderr}")
    return events


def calls(events):
    found = [(e["name"], e["args"]) for e in events if e["type"] == "tool_started"]
    for name, args in found:
        print(f"    {name}: {json.dumps(args, ensure_ascii=False)[:160]}")
    return found


def read_skill(found, name):
    """Whether the calls read the skill's SKILL.md with read, or printed it with bash."""
    for tool, args in found:
        target = args.get("path", "") if tool == "read" else args.get("command", "") if tool == "bash" else ""
        if f"{name}/SKILL.md" in target:
            return True
    return False


def log_entries(events, kind):
    path = next(e for e in events if e["type"] == "session_started")["session_file"]
    lines = pathlib.Path(path).read_text().splitlines()
    return [entry for entry in map(json.loads, lines) if entry.get("type") == kind]


def user_message(events):
    message = next(e for e in events if e["type"] == "message_completed" and e["message"]["role"] == "user")
    text = "".join(c.get("text", "") for c in message["message"]["content"] if c.get("type") == "text")
    return text, [s["name"] for s in message.get("skills", [])]


def warnings(events):
    """The session's warnings, leaving out those with a code (bypass-permissions says so at the start)."""
    return [e["message"] for e in events if e["type"] == "warning" and not e.get("code")]


def first_line(path):
    return path.read_text().splitlines()[0].strip() if path.exists() and path.read_text().strip() else ""


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--her", default="target/release/her")
    parser.add_argument("--model", default="deepseek/deepseek-flash")
    parser.add_argument("--keep", action="store_true", help="keep the project and its session logs")
    args = parser.parse_args()
    her = str(pathlib.Path(args.her).resolve())

    with tempfile.TemporaryDirectory(prefix="her-skills2-", delete=not args.keep) as tmp:
        if args.keep:
            print(f"  kept in {tmp}")
        base = pathlib.Path(tmp).resolve()
        home, user, proj = base / "home", base / "user", base / "proj"
        for d in (home, user, proj / ".git"):
            d.mkdir(parents=True)
        (proj / ".git" / "HEAD").write_text("ref: refs/heads/main\n")
        for name, text in (("release-notes", RELEASE_NOTES), ("commit-style", COMMIT_STYLE)):
            skill = proj / ".agents" / "skills" / name
            skill.mkdir(parents=True)
            (skill / "SKILL.md").write_text(text)
        env = dict(os.environ, HER_HOME=str(home), HOME=str(user), NO_COLOR="1")
        checks = {}

        typed = "/skill:release-notes 2.4.1"
        print(typed)
        events = run(her, proj, env, typed, args.model)
        found = calls(events)
        system = log_entries(events, "system_prompt")[0]
        checks["a user-only skill is not in the catalog"] = (
            'name="release-notes"' not in system["text"]
            and any(s["name"] == "release-notes" for s in system["skills"])
        )
        checks["the log keeps the words as typed and names the skill"] = user_message(events) == (typed, ["release-notes"])
        checks["RELEASE.md follows the skill"] = first_line(proj / "RELEASE.md") == f"# Release 2.4.1 · {NOTES_MARKER}"
        checks["no read of the SKILL.md was needed"] = not read_skill(found, "release-notes")

        typed = (
            "Prepare version 3.0.0: write the notes as /skill:release-notes says, then write a commit "
            "message for them to COMMIT_MSG0.txt following /skill:commit-style. Thanks!"
        )
        print("two mentions mid-text")
        events = run(her, proj, env, typed, args.model)
        found = calls(events)
        checks["mid-text mentions load both skills, in order"] = user_message(events) == (
            typed,
            ["release-notes", "commit-style"],
        )
        checks["RELEASE.md follows release-notes"] = first_line(proj / "RELEASE.md") == f"# Release 3.0.0 · {NOTES_MARKER}"
        checks["COMMIT_MSG0.txt follows commit-style"] = first_line(proj / "COMMIT_MSG0.txt").startswith(COMMIT_TAG + " ")
        checks["no read of either SKILL.md was needed"] = not read_skill(found, "release-notes") and not read_skill(
            found, "commit-style"
        )

        typed = "Reply with just OK. Here /release-notes, `/skill:release-notes` and /skill:nope are only words."
        print("words that are not mentions")
        events = run(her, proj, env, typed, args.model)
        calls(events)
        checks["only real mentions load skills"] = user_message(events) == (typed, [])
        checks["an unknown name warns"] = warnings(events) == ["no skill named 'nope'; \"/skill:nope\" was sent as text"]

        print("commit message, then /compact, then another one")
        events = run(her, proj, env, "Write a commit message for adding a README to COMMIT_MSG.txt.", args.model)
        found = calls(events)
        session = next(e for e in events if e["type"] == "session_started")["session_id"]
        checks["the model read commit-style from the catalog"] = read_skill(found, "commit-style")
        checks["COMMIT_MSG.txt follows the skill"] = first_line(proj / "COMMIT_MSG.txt").startswith(COMMIT_TAG + " ")

        events = run(her, proj, env, "/compact", args.model, resume=session)
        done = [e for e in events if e["type"] == "compaction_completed"]
        print(f"    compaction: {json.dumps(done[0] if done else None, ensure_ascii=False)[:300]}")
        checks["/compact keeps commit-style"] = bool(done) and done[0].get("kept_skills") == ["commit-style"]
        kept = log_entries(events, "compaction")[-1].get("skills", [])
        checks["the log holds its instructions"] = bool(kept) and COMMIT_TAG in kept[0]["content"]

        events = run(
            her,
            proj,
            env,
            "Now write a commit message for fixing a typo in the README to COMMIT_MSG2.txt.",
            args.model,
            resume=session,
        )
        found = calls(events)
        checks["after compaction COMMIT_MSG2.txt still follows the skill"] = first_line(
            proj / "COMMIT_MSG2.txt"
        ).startswith(COMMIT_TAG + " ")
        print(f"    read commit-style again after compaction: {read_skill(found, 'commit-style')}")
        print(f"    COMMIT_MSG.txt:  {first_line(proj / 'COMMIT_MSG.txt')}")
        print(f"    COMMIT_MSG2.txt: {first_line(proj / 'COMMIT_MSG2.txt')}")

        for name, ok in checks.items():
            print(f"{'PASS' if ok else 'FAIL'}  {name}")
        sys.exit(0 if all(checks.values()) else 1)


if __name__ == "__main__":
    main()
