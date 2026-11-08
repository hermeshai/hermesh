"""Offline checks for the bundled Skill Creator helpers.

Run after building her: HER_BIN=/absolute/path/to/her python3 -B -S tools/acceptance/test_skill_creator.py
"""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import subprocess
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[2]
SKILL = ROOT / "crates/her-core/src/skills/system/skill-creator"
sys.path.insert(0, str(SKILL))
from scripts.aggregate_benchmark import generate_benchmark, generate_markdown
from scripts.package_skill import package_skill
from scripts.quick_validate import validate_skill

spec = importlib.util.spec_from_file_location("skill_review", SKILL / "eval-viewer/generate_review.py")
review = importlib.util.module_from_spec(spec)
spec.loader.exec_module(review)


class SkillCreatorTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def skill(self, header):
        path = self.root / "skills/example"
        path.mkdir(parents=True, exist_ok=True)
        (path / "SKILL.md").write_text(f"---\n{header}\n---\n# Instructions\nDo the task.\n")
        return path

    def test_native_validation_supports_yaml_and_rejects_invalid_skill(self):
        path = self.skill("name: example\ndescription: >-\n  Write reports: use the team's format.\nargument-hint: '[week]'")
        self.assertTrue(validate_skill(path)[0])
        self.skill("name: example")
        valid, message = validate_skill(path)
        self.assertFalse(valid)
        self.assertIn("description", message)
        self.assertFalse((path / ".her-source.json").exists(), "validation must not install the skill")

    def test_package_validates_and_excludes_eval_inputs(self):
        path = self.skill("name: example\ndescription: Write a report.")
        (path / "evals").mkdir()
        (path / "evals/private.json").write_text("private test data")
        (path / "references").mkdir()
        (path / "references/format.md").write_text("Report format")
        with contextlib.redirect_stdout(io.StringIO()):
            archive = package_skill(path, self.root / "out")
        with zipfile.ZipFile(archive) as contents:
            self.assertIn("example/references/format.md", contents.namelist())
            self.assertNotIn("example/evals/private.json", contents.namelist())
        self.skill("name: example")
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertIsNone(package_skill(path, self.root / "invalid"))

    def run_fixture(self, config, passed, timing=None):
        case = self.root / "iteration-1/eval-0-report"
        case.mkdir(parents=True, exist_ok=True)
        (case / "eval_metadata.json").write_text(json.dumps({"eval_id": 0, "prompt": "Write a weekly report."}))
        run = case / config / "run-1"
        (run / "outputs").mkdir(parents=True)
        (run / "outputs/report.md").write_text("Weekly report\n</script><script>alert('output')</script>")
        (run / "grading.json").write_text(json.dumps({
            "summary": {"passed": int(passed), "failed": int(not passed), "total": 1, "pass_rate": float(passed)},
            "expectations": [{"text": "Report contains the requested sections", "passed": passed, "evidence": "Inspected report.md"}],
            "execution_metrics": {"output_chars": 99999},
        }))
        if timing is not None:
            (run / "timing.json").write_text(json.dumps(timing))
        return case.parent

    def test_benchmark_reports_real_measurements_and_unknown_usage(self):
        workspace = self.run_fixture("with_skill", True, {"total_tokens": 500, "total_duration_seconds": 2})
        self.run_fixture("without_skill", False)
        benchmark = generate_benchmark(workspace, "example")
        summary = benchmark["run_summary"]
        self.assertEqual(summary["with_skill"]["tokens"]["mean"], 500)
        self.assertIsNone(summary["without_skill"]["tokens"]["mean"])
        self.assertIsNone(summary["without_skill"]["time_seconds"]["mean"])
        self.assertEqual(summary["delta"]["tokens"], "—")
        self.assertEqual(summary["delta"]["pass_rate"], "+1.00")
        self.assertEqual(benchmark["metadata"]["runs_per_configuration"], 1)
        self.assertIn("| Tokens | 500 ± 0 | — | — |", generate_markdown(benchmark))
        subprocess.run([sys.executable, "-B", "-S", "-m", "scripts.aggregate_benchmark",
                        str(workspace), "--skill-name", "example"], cwd=SKILL,
                       check=True, capture_output=True, text=True)
        self.assertEqual(json.loads((workspace / "benchmark.json").read_text())["metadata"]["skill_name"], "example")

    def test_offline_reviewer_finds_nested_prompt_and_escapes_script_data(self):
        workspace = self.run_fixture("with_skill", True)
        runs = review.find_runs(workspace)
        self.assertEqual(runs[0]["prompt"], "Write a weekly report.")
        page = review.generate_html(runs, "example")
        self.assertNotIn("</script><script>alert", page)
        self.assertIn("\\u003c/script>", page)
        self.assertNotIn('src="https://', page)
        self.assertNotIn('href="https://', page)
        output = workspace / "review.html"
        subprocess.run([sys.executable, "-B", "-S", str(SKILL / "eval-viewer/generate_review.py"),
                        str(workspace), "--static", str(output)],
                       check=True, capture_output=True, text=True)
        self.assertTrue(output.is_file())


if __name__ == "__main__":
    os.environ.setdefault("HER_BIN", str(ROOT / "target/debug/her"))
    unittest.main()
