/// `(path in .system, content)`
pub(crate) const FILES: &[(&str, &str)] = &[
    ("skill-creator/LICENSE.txt", include_str!("skill-creator/LICENSE.txt")),
    ("skill-creator/SKILL.md", include_str!("skill-creator/SKILL.md")),
    ("skill-creator/UPSTREAM.md", include_str!("skill-creator/UPSTREAM.md")),
    ("skill-creator/agents/analyzer.md", include_str!("skill-creator/agents/analyzer.md")),
    ("skill-creator/agents/comparator.md", include_str!("skill-creator/agents/comparator.md")),
    ("skill-creator/agents/grader.md", include_str!("skill-creator/agents/grader.md")),
    ("skill-creator/assets/eval_review.html", include_str!("skill-creator/assets/eval_review.html")),
    ("skill-creator/eval-viewer/generate_review.py", include_str!("skill-creator/eval-viewer/generate_review.py")),
    ("skill-creator/eval-viewer/viewer.html", include_str!("skill-creator/eval-viewer/viewer.html")),
    ("skill-creator/references/schemas.md", include_str!("skill-creator/references/schemas.md")),
    ("skill-creator/scripts/__init__.py", include_str!("skill-creator/scripts/__init__.py")),
    ("skill-creator/scripts/aggregate_benchmark.py", include_str!("skill-creator/scripts/aggregate_benchmark.py")),
    ("skill-creator/scripts/package_skill.py", include_str!("skill-creator/scripts/package_skill.py")),
    ("skill-creator/scripts/quick_validate.py", include_str!("skill-creator/scripts/quick_validate.py")),
];
